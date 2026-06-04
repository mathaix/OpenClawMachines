# A3b: Placement Records + Capacity Policy Design

Date: 2026-03-09
Branch: `3rdpartyprov-part2`
Status: Approved

## Goal

Separate active machine placement from home affinity by introducing a `machine_placements` table, and add explicit capacity policies with overcommit support.

## Schema

### Migration 027: machine_placements

```sql
CREATE TABLE machine_placements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    host_id INT NOT NULL REFERENCES hosts(id),
    vm_ip TEXT,
    state TEXT NOT NULL DEFAULT 'reserved',
    reserved_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at TIMESTAMPTZ,
    released_at TIMESTAMPTZ,
    created_by_operation_id UUID REFERENCES machine_operations(id),
    CONSTRAINT machine_placements_state_check CHECK (state IN ('reserved', 'active', 'released'))
);

-- One active placement per machine
CREATE UNIQUE INDEX idx_placements_active_machine
ON machine_placements(machine_id) WHERE released_at IS NULL;

-- One active (host_id, vm_ip) pair
CREATE UNIQUE INDEX idx_placements_active_host_ip
ON machine_placements(host_id, vm_ip) WHERE released_at IS NULL;

-- Fast lookup by host for reconciler
CREATE INDEX idx_placements_host ON machine_placements(host_id) WHERE released_at IS NULL;
```

### Migration 028: capacity_policies + host/machine columns

```sql
CREATE TABLE capacity_policies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    scope_type TEXT NOT NULL,
    scope_ref TEXT,
    cpu_overcommit_ratio NUMERIC(4,2) NOT NULL DEFAULT 1.00,
    memory_overcommit_ratio NUMERIC(4,2) NOT NULL DEFAULT 1.00,
    reserve_vcpus INT NOT NULL DEFAULT 0,
    reserve_memory_mb INT NOT NULL DEFAULT 0,
    max_machine_count INT,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT capacity_policies_scope_check CHECK (scope_type IN ('global', 'host_pool', 'host')),
    CONSTRAINT capacity_policies_cpu_ratio CHECK (cpu_overcommit_ratio BETWEEN 1.0 AND 3.0),
    CONSTRAINT capacity_policies_mem_ratio CHECK (memory_overcommit_ratio BETWEEN 1.0 AND 1.2),
    CONSTRAINT capacity_policies_reserve_vcpus CHECK (reserve_vcpus >= 0),
    CONSTRAINT capacity_policies_reserve_mem CHECK (reserve_memory_mb >= 0)
);

-- One enabled policy per scope
CREATE UNIQUE INDEX idx_capacity_policies_scope_unique
ON capacity_policies(scope_type, scope_ref) WHERE enabled = true;

ALTER TABLE hosts ADD COLUMN IF NOT EXISTS host_pool TEXT NOT NULL DEFAULT 'default';
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS capacity_policy_id UUID REFERENCES capacity_policies(id);
ALTER TABLE machines ADD COLUMN IF NOT EXISTS home_host_id INT REFERENCES hosts(id) ON DELETE SET NULL;
ALTER TABLE machines ADD COLUMN IF NOT EXISTS storage_mode TEXT NOT NULL DEFAULT 'host_local';

-- Default global policy: 2x CPU overcommit (matches current implicit behavior), 1x memory
INSERT INTO capacity_policies (name, scope_type, cpu_overcommit_ratio, memory_overcommit_ratio)
VALUES ('default', 'global', 2.0, 1.0);
```

### Migration 029: backfill placements

```sql
-- Backfill active placements for running/provisioning machines
INSERT INTO machine_placements (machine_id, host_id, vm_ip, state, activated_at)
SELECT id, host_id, vm_ip, 'active', now()
FROM machines
WHERE host_id IS NOT NULL AND vm_ip IS NOT NULL AND status IN ('running', 'provisioning');

-- Backfill home_host_id for stopped machines with host affinity
UPDATE machines SET home_host_id = host_id
WHERE host_id IS NOT NULL AND status = 'stopped';
```

## PlacementService

Package: `backend/internal/fleet/placement.go`

```go
type PlacementRequest struct {
    VCPUs         int
    MemoryMB      int
    Region        string
    ExpectedImage string
    OperationID   string
}

type ReleaseMode int
const (
    ReleaseSoft ReleaseMode = iota  // stop: free counters, preserve home_host_id
    ReleaseHard                      // delete: free everything
)

type PlacementService struct {
    store PlacementStore
}

func (ps *PlacementService) Reserve(ctx, machineID string, req PlacementRequest) (*Placement, error)
func (ps *PlacementService) Activate(ctx, placementID string) error
func (ps *PlacementService) Release(ctx, machineID string, mode ReleaseMode) error
func (ps *PlacementService) RecoverAffinity(ctx, machineID string, req PlacementRequest) (*Placement, error)
```

### Reserve flow (single transaction)

1. Resolve effective capacity policy (host override → pool → global)
2. Compute effective allocatable: `floor((physical - reserve) * overcommit_ratio)`
3. `SELECT host FOR UPDATE` with heartbeat freshness + capacity filter using effective values
4. Allocate next available VM IP (same logic as current `PlaceMachineOnHost`)
5. `INSERT INTO machine_placements` (state=reserved)
6. `UPDATE hosts` (increment counters)
7. `UPDATE machines SET host_id, vm_ip` (dual-write for backward compat)
8. Commit

### Activate flow

1. `UPDATE machine_placements SET state='active', activated_at=now() WHERE id=$1 AND state='reserved'`

### Release(soft) flow

1. `UPDATE machine_placements SET state='released', released_at=now() WHERE machine_id=$1 AND released_at IS NULL`
2. `UPDATE hosts` (decrement counters)
3. `UPDATE machines SET home_host_id=host_id` (preserve affinity)
4. Do NOT clear `machines.host_id/vm_ip` yet (backward compat for reads)

### Release(hard) flow

1. `UPDATE machine_placements SET state='released', released_at=now() WHERE machine_id=$1 AND released_at IS NULL`
2. `UPDATE hosts` (decrement counters)
3. `UPDATE machines SET host_id=NULL, vm_ip=NULL, home_host_id=NULL`

### RecoverAffinity flow

1. Check `machines.home_host_id` — if set and host is viable (ready + fresh heartbeat), Reserve on that host specifically
2. If home host is dead/unreachable, do fresh Reserve (any eligible host)

## Capacity Policy Resolution

```go
func (ps *PlacementService) resolvePolicy(ctx, hostID int, hostPool string) (*CapacityPolicy, error) {
    // 1. Host-specific override
    // 2. Pool policy
    // 3. Global default
}
```

Effective capacity:
```
effective_vcpu = floor((capacity_vcpus - reserve_vcpus) * cpu_overcommit_ratio)
effective_mem  = floor((capacity_memory_mb - reserve_memory_mb) * memory_overcommit_ratio)
available_vcpu = effective_vcpu - used_vcpus
available_mem  = effective_mem - used_memory_mb
```

## Integration with RuntimeService

Replace scheduler calls:

| Current | New |
|---------|-----|
| `scheduler.PlaceMachine(ctx, machine)` | `placement.Reserve(ctx, machine.ID, req)` |
| `scheduler.ReAllocateMachine(ctx, machine)` | `placement.RecoverAffinity(ctx, machine.ID, req)` |
| `scheduler.SoftReleaseMachine(ctx, machine)` | `placement.Release(ctx, machine.ID, ReleaseSoft)` |
| `scheduler.ReleaseMachine(ctx, machine)` | `placement.Release(ctx, machine.ID, ReleaseHard)` |

After VM creation, in pollVMStatus on `running`:
```go
placement.Activate(ctx, placement.ID)
```

## Migration Strategy

Three-phase rollout:

1. **Schema + backfill** (migrations 027-029): Create tables, backfill from existing data
2. **Dual-write code**: PlacementService writes to both tables. Reads still from `machines.host_id/vm_ip` for non-placement code paths (route resolution, admin list, etc.)
3. **Read migration** (future PR): Switch remaining read paths to use placements. Then remove legacy columns.

This PR implements phases 1 and 2. Phase 3 is a cleanup PR after verification.

## What Gets Replaced

The `backend/internal/scheduler/` package is absorbed. Its 4 methods become PlacementService methods. The scheduler tests are migrated to fleet/placement tests.

## Tests

- Reserve fresh placement (host selected, placement created, counters updated, dual-write)
- Reserve rejects when no eligible host (no capacity)
- Reserve uses effective capacity from policy (2x CPU overcommit)
- Reserve atomic: no IP collision under concurrent requests (partial unique index)
- Activate marks placement active
- Release soft: placement released, counters decremented, home_host_id set
- Release hard: placement released, counters decremented, host_id/vm_ip/home_host_id cleared
- RecoverAffinity: reuses home host if viable
- RecoverAffinity: fresh placement if home host dead
- Policy resolution: host > pool > global
- Backfill: running machines get active placements
- Backfill: stopped machines get home_host_id

## Files Changed

| File | Change |
|------|--------|
| `backend/migrations/027_machine_placements.sql` | New — placements table |
| `backend/migrations/028_capacity_policies.sql` | New — policies table + host/machine columns |
| `backend/migrations/029_backfill_placements.sql` | New — backfill from existing data |
| `backend/internal/fleet/placement.go` | New — PlacementService |
| `backend/internal/fleet/placement_test.go` | New — tests |
| `backend/internal/fleet/policy.go` | New — capacity policy resolution |
| `backend/internal/store/store.go` | Add PlacementStore interface |
| `backend/internal/store/postgres.go` | Implement placement queries |
| `backend/internal/machines/runtime.go` | Replace scheduler calls with placement calls |
| `backend/cmd/server/main.go` | Wire PlacementService |
| `backend/internal/scheduler/` | Remove (absorbed into fleet) |
