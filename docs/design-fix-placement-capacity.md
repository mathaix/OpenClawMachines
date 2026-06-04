# Fix Machine Placement Capacity Accounting

## Problem

Machines fail to provision when a host's agent-side VM count limit (`MAX_VMS=10`) is reached, even though the host has ample CPU and memory, and other hosts in the same region are completely idle.

### Root Cause

The agent enforces a hard `MAX_VMS` limit (default 10) that is disconnected from the control plane's capacity model. The control plane tracks vCPU and memory capacity per host and uses these to decide placement. The agent ignores this and applies its own arbitrary VM count cap. When the cap is hit, the agent rejects creates even though the host has 33 GB of free RAM and 14 free vCPUs.

Additionally:
- The placement strategy uses bin-packing (`ORDER BY used_memory_mb DESC`), always preferring the fullest host. An idle host in the same region never receives a single placement attempt.
- **Disk capacity is not tracked.** Each VM needs ~3 GB rootfs (CoW) + 5 GB data volume = ~8 GB. This is fine on bare-metal (878 GB disk), but GCP VMs have smaller disks sized from `10 + maxVMs*3` (boot) and `maxVMs*5 + 5` (data). Without disk tracking, placement can fill a host's disk before memory runs out.

### Impact (2026-04-02 Incident)

- Host 104 (`ocm-west-delta2`): 10 VMs, 33 GB free RAM, 14 free vCPUs — agent rejected creates with `"host at capacity (10 VMs)"`
- Host 105 (`ocm-west2`): 0 VMs, completely idle — never received a single placement attempt
- User-facing result: machine stuck in provisioning, never started

## Design Principles

1. **The control plane is the single authority for capacity management.** The agent must not enforce its own capacity limits. Capacity is defined by three resource dimensions — vCPUs, memory, and disk. The placement service checks all three. No artificial VM count cap needed.

2. **Placement logic lives in Go, not SQL.** Host selection and ranking are Go code — testable without a database. But the mutation path (claim capacity + allocate IP + assign machine) stays in a single SQL transaction for atomicity.

3. **All resource dimensions are tracked.** If placement doesn't know about a resource, it can't prevent exhaustion. Every resource that limits VM count must be visible to the placement service.

## Solution

### 1. Add disk capacity tracking

Add disk capacity fields to the hosts table, mirroring the existing vCPU and memory pattern:

```sql
ALTER TABLE hosts ADD COLUMN capacity_disk_mb BIGINT NOT NULL DEFAULT 0;
ALTER TABLE hosts ADD COLUMN used_disk_mb BIGINT NOT NULL DEFAULT 0;
```

Per-VM disk usage is defined by machine spec (default: ~8 GB = 3 GB rootfs + 5 GB data volume). The placement service checks `(capacity_disk_mb - used_disk_mb) >= needed` alongside vCPU and memory.

Disk capacity is set at:
- **GCP hosts**: Calculated from boot + data disk sizes at provisioning time
- **OVH/registered hosts**: Reported in capabilities at enrollment, or set via admin API

### 2. Remove agent-side `MAX_VMS` enforcement

With vCPU, memory, and disk all tracked, the placement service dynamically limits how many VMs a host can run. No artificial cap needed.

Remove the capacity guard in the orchestrator:

```go
// REMOVE (firecracker_linux.go:119-121)
if o.cfg.MaxVMs > 0 && len(o.vms) >= o.cfg.MaxVMs {
    return fmt.Errorf("host at capacity (%d VMs)", o.cfg.MaxVMs)
}
```

Also remove `MaxVMs` from `AgentConfig` and the `MAX_VMS` env var.

### 3. Refactor placement: Go selection + atomic SQL mutation

Currently `fleet/PlacementService` is a thin wrapper that delegates to `store.PlaceMachineOnHost` — a 100-line SQL transaction that combines host selection, capacity allocation, IP assignment, and machine update in one function. This makes placement policy impossible to unit test without a real database.

**New design**: Split host selection/ranking (Go) from the mutation path (atomic SQL transaction). The key insight: we can move the *policy* into Go while keeping the *mutation* atomic.

#### Store layer

```go
type HostRepo interface {
    // ListEligibleHosts returns hosts that are ready, have fresh heartbeats,
    // and have sufficient resources across all three dimensions.
    // Read-only, no locks. Returns all candidates for Go-side ranking.
    ListEligibleHosts(ctx context.Context, filter HostFilter) ([]Host, error)

    // PlaceOnHost atomically claims capacity, allocates a VM IP, and assigns
    // the machine — all in one transaction. Uses FOR UPDATE SKIP LOCKED on
    // the host row. Returns error if host is contended or capacity changed
    // since ListEligibleHosts was called.
    PlaceOnHost(ctx context.Context, hostID int, machineID string, vcpus, memoryMB, diskMB int) (vmIP string, err error)

    // ReleaseHostCapacity decrements counters (with GREATEST(0, ...) guard).
    ReleaseHostCapacity(ctx context.Context, hostID int, vcpus, memoryMB, diskMB int) error
}

type HostFilter struct {
    MinVCPUs    int
    MinMemoryMB int
    MinDiskMB   int
    Region      string
    Image       string
}
```

`PlaceOnHost` replaces the current `PlaceMachineOnHost`. It runs a single transaction:

```sql
BEGIN;
  -- Lock host, re-verify capacity (may have changed since listing)
  SELECT ... FROM hosts
  WHERE id = $1
    AND status = 'ready'
    AND last_heartbeat > now() - interval '180 seconds'
    AND (capacity_vcpus - used_vcpus) >= $2
    AND (capacity_memory_mb - used_memory_mb) >= $3
    AND (capacity_disk_mb - used_disk_mb) >= $4
  FOR UPDATE SKIP LOCKED;

  -- Increment counters
  UPDATE hosts SET used_vcpus = used_vcpus + $2,
                   used_memory_mb = used_memory_mb + $3,
                   used_disk_mb = used_disk_mb + $4
  WHERE id = $1;

  -- Allocate next free IP
  SELECT vm_ip FROM machines WHERE host_id = $1 AND vm_ip IS NOT NULL;
  -- pick first unused in 192.168.100.10-250

  -- Assign machine
  UPDATE machines SET host_id = $1, vm_ip = $ip, status = 'provisioning',
                      provisioning_started_at = now()
  WHERE id = $machineID;
COMMIT;
```

If `FOR UPDATE SKIP LOCKED` can't lock the row (another placement is using it), the SELECT returns no rows and `PlaceOnHost` returns an error. The service tries the next host.

#### Placement service — owns selection and ranking

```go
type PlacementService struct {
    store  PlacementStore
    config PlacementConfig
}

type PlacementConfig struct {
    DefaultRegion    string
    ExpectedImage    string
    Strategy         Strategy
    DefaultDiskPerVM int // MB, used when machine spec doesn't specify disk
}

type Strategy int

const (
    Random  Strategy = iota // shuffle eligible hosts (default)
    Spread                  // prefer least-loaded host
    BinPack                 // prefer most-loaded host
)

func (ps *PlacementService) Reserve(ctx context.Context, machineID string, req PlacementRequest) (*Placement, *Host, string, error) {
    if req.TargetHostID != 0 {
        return ps.placeOnSpecificHost(ctx, machineID, req)
    }
    return ps.placeAutoSelect(ctx, machineID, req)
}

func (ps *PlacementService) placeAutoSelect(ctx context.Context, machineID string, req PlacementRequest) (*Placement, *Host, string, error) {
    diskMB := req.DiskMB
    if diskMB == 0 {
        diskMB = ps.config.DefaultDiskPerVM
    }

    // 1. Get eligible hosts (read-only, no locks)
    hosts, err := ps.store.ListEligibleHosts(ctx, HostFilter{
        MinVCPUs:    req.VCPUs,
        MinMemoryMB: req.MemoryMB,
        MinDiskMB:   diskMB,
        Region:      coalesce(req.Region, ps.config.DefaultRegion),
        Image:       coalesce(req.ExpectedImage, ps.config.ExpectedImage),
    })
    if err != nil || len(hosts) == 0 {
        return nil, nil, "", fmt.Errorf("no hosts with sufficient capacity")
    }

    // 2. Rank by strategy (pure Go — unit testable)
    ranked := ps.rank(hosts)

    // 3. Try hosts in ranked order — PlaceOnHost is atomic per attempt
    for _, host := range ranked {
        vmIP, err := ps.store.PlaceOnHost(ctx, host.ID, machineID, req.VCPUs, req.MemoryMB, diskMB)
        if err != nil {
            slog.Warn("placement.attempt_failed", "host_id", host.ID, "error", err)
            continue // contended or capacity changed — try next
        }

        placement, _ := ps.store.ReservePlacement(ctx, machineID, host.ID, vmIP, req.OperationID)
        return placement, &host, vmIP, nil
    }

    return nil, nil, "", fmt.Errorf("all eligible hosts at capacity or contended")
}

func (ps *PlacementService) placeOnSpecificHost(ctx context.Context, machineID string, req PlacementRequest) (*Placement, *Host, string, error) {
    diskMB := req.DiskMB
    if diskMB == 0 {
        diskMB = ps.config.DefaultDiskPerVM
    }

    vmIP, err := ps.store.PlaceOnHost(ctx, req.TargetHostID, machineID, req.VCPUs, req.MemoryMB, diskMB)
    if err != nil {
        return nil, nil, "", fmt.Errorf("target host %d: %w", req.TargetHostID, err)
    }

    host, _ := ps.store.GetHost(ctx, req.TargetHostID)
    placement, _ := ps.store.ReservePlacement(ctx, machineID, req.TargetHostID, vmIP, req.OperationID)
    return placement, host, vmIP, nil
}

// rank orders hosts by placement strategy. Pure function — unit testable.
func (ps *PlacementService) rank(hosts []Host) []Host {
    out := make([]Host, len(hosts))
    copy(out, hosts)
    switch ps.config.Strategy {
    case Random:
        rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
    case Spread:
        sort.Slice(out, func(i, j int) bool {
            return out[i].UsedMemoryMB < out[j].UsedMemoryMB
        })
    case BinPack:
        sort.Slice(out, func(i, j int) bool {
            return out[i].UsedMemoryMB > out[j].UsedMemoryMB
        })
    }
    return out
}
```

### 4. Switch to random scheduling

- **Bin-packing** (`ORDER BY used_memory_mb DESC`): Optimizes for scaling down idle hosts. Irrelevant for always-on bare-metal.
- **Random** (shuffle eligible hosts): For identical hosts in a region, random selection is the simplest correct strategy. No state to track, no tie-breaking edge cases, naturally distributes load. Statistically equivalent to round-robin at scale.
- **Spread** (`ORDER BY used_memory_mb ASC`): Available as option for heterogeneous fleets where hosts have different capacities.

Default strategy is `Random`. Configurable via `PlacementConfig.Strategy`.

### 5. Disk behavior on soft stop

When a machine is soft-stopped (keeping host affinity for restart):
- **vCPUs and memory are released** — the VM is not running, these resources are free
- **Disk is NOT released** — the data volume stays on the host, consuming disk space

This prevents overbooking disk on hosts with stopped machines waiting to restart. The `SoftStopMachine` store method releases vCPU/memory counters but leaves `used_disk_mb` unchanged. Disk is only released on hard release (machine destroyed or migrated away).

### 6. Sync `vm_count` from agent heartbeat

The agent reports its actual running VM count in heartbeats. The control plane uses this to detect and correct `machine_count` drift:

```go
if req.VMCountReported {
    if req.VMCount != host.MachineCount {
        slog.Warn("heartbeat.machine_count_drift",
            "host_id", hostID,
            "db_count", host.MachineCount,
            "agent_count", req.VMCount)
        store.ReconcileHostMachineCount(ctx, hostID, req.VMCount)
    }
}
```

Agent heartbeat payload adds:
```json
{
  "vm_count": 5,
  "vm_count_reported": true
}
```

The `vm_count_reported` flag distinguishes "0 running VMs" from "old agent that doesn't send this field".

## Capacity Model

Three resource dimensions, all tracked the same way:

| Resource | Capacity field | Used field | Per-VM default | Release on soft stop |
|----------|---------------|------------|----------------|---------------------|
| vCPUs | `capacity_vcpus` | `used_vcpus` | 2 | Yes |
| Memory | `capacity_memory_mb` | `used_memory_mb` | 6144 MB | Yes |
| Disk | `capacity_disk_mb` | `used_disk_mb` | ~8192 MB | **No** (data volume persists) |

A host is eligible for placement when **all three** dimensions have sufficient free capacity. The tightest resource determines the effective VM limit — no hardcoded cap needed.

## Migration Plan

1. **Migration**: Add `capacity_disk_mb` and `used_disk_mb` columns to hosts table
2. **Backfill**: Calculate disk capacity for existing hosts (OVH: from `df`, GCP: from disk sizes)
3. **Refactor**: Extract placement logic from `postgres.go` into `fleet/PlacementService`
   - New store methods: `ListEligibleHosts`, `PlaceOnHost` (atomic transaction)
   - Deprecate `PlaceMachineOnHost` and `PlaceMachineOnSpecificHost`
   - Random scheduling as default strategy
4. **Deploy backend**: Placement is immediately fixed with random scheduling. Agent `MAX_VMS` may still reject on a host that happens to be full, but random distribution makes this unlikely.
5. **Agent change** (follow-up): Remove `MAX_VMS` config and capacity guard, add `vm_count` + `vm_count_reported` to heartbeat
6. **Deploy agents**: Via self-update. Old agents still work (redundant capacity check until removed)

Steps 1–4 are the **hotfix**. Steps 5–6 are cleanup.

## Files to Change

### Hotfix (backend only)

| File | Change |
|------|--------|
| `backend/migrations/0XX_host_disk_capacity.sql` | Add `capacity_disk_mb`, `used_disk_mb` columns, backfill |
| `backend/internal/store/postgres.go` | Add `ListEligibleHosts`, `PlaceOnHost` (atomic txn with disk); update `ReleaseHostCapacity`, `SoftStopMachine` for disk semantics |
| `backend/internal/store/store.go` | Add disk fields to `Host` struct, new store interface methods |
| `backend/internal/fleet/placement.go` | Rewrite `Reserve` with Go-side ranking + `PlaceOnHost` fallback loop; preserve `TargetHostID` path |
| `backend/internal/fleet/placement_test.go` | Unit tests for `rank()`, fallback behavior — no DB needed |
| `backend/internal/api/enrollment.go` | Read disk capacity from host capabilities at enrollment |
| `backend/internal/provisioner/provisioner.go` | Set `capacity_disk_mb` from calculated boot + data disk sizes |

### Follow-up (agent changes)

| File | Change |
|------|--------|
| `backend/internal/orchestrator/firecracker_linux.go` | Remove `MaxVMs` capacity guard |
| `backend/internal/config/config.go` | Remove `MaxVMs` from `AgentConfig`, remove `MAX_VMS` env var |
| `backend/cmd/agent/main.go` | Add `vm_count` and `vm_count_reported` to heartbeat payload |
| `backend/internal/api/agent_heartbeat.go` | Read `vm_count`/`vm_count_reported`, reconcile `machine_count` on drift |
| `backend/internal/store/postgres.go` | Add `ReconcileHostMachineCount` method |
| `backend/internal/store/store.go` | Add `ReconcileHostMachineCount` to interface |

## Atomicity and Race Safety

The design uses a **hybrid model**: Go-side selection + atomic SQL mutation.

| Step | Where | Mechanism |
|------|-------|-----------|
| List eligible hosts | Go | Read-only snapshot, no locks |
| Rank by strategy | Go | Pure function, unit testable |
| Claim capacity + allocate IP + assign machine | SQL | **Single transaction** with `FOR UPDATE SKIP LOCKED`. If host is contended or capacity changed, returns error. |
| Try next host | Go | Fallback loop calls `PlaceOnHost` on next ranked host |

This preserves the atomicity of the current design (no counter leaks, no duplicate IPs) while making the selection policy testable in Go. A process crash between `ListEligibleHosts` and `PlaceOnHost` is harmless — no mutations have occurred. A crash during `PlaceOnHost` is handled by Postgres transaction rollback.

## Out of Scope

- **Capacity policies table** (`capacity_policies`): Has `max_machine_count` but is unused. Can layer on later for per-pool or per-host policy overrides.
- **Cross-region failover**: If all hosts in a region are full, placement fails (no silent cross-region placement).
- **Dynamic disk usage monitoring**: This design tracks disk at placement/release time (bookkeeping). Real-time disk monitoring (e.g., agent reporting actual `df` usage) is a separate observability concern.
- **Multi-dimensional ranking**: Random strategy doesn't need it. If we switch to spread for heterogeneous fleets, ranking could weight all dimensions instead of memory only.
