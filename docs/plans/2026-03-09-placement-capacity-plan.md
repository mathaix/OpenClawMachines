# Placement Records + Capacity Policy Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Introduce `machine_placements` table to separate active placement from home affinity, and `capacity_policies` for explicit overcommit control. Replace the thin `scheduler` package with a `PlacementService` in the `fleet` package.

**Architecture:** Three migrations (schema, policies, backfill), a new `fleet` package with `PlacementService` and policy resolution, then rewire `RuntimeService` to use placement calls instead of scheduler calls. Dual-write to both `machine_placements` and legacy `machines.host_id/vm_ip` columns.

**Tech Stack:** Go 1.25, pgx/v5 (raw SQL transactions), PostgreSQL partial unique indexes

---

### Task 1: Create Migrations

**Files:**
- Create: `backend/migrations/027_machine_placements.sql`
- Create: `backend/migrations/028_capacity_policies.sql`
- Create: `backend/migrations/029_backfill_placements.sql`

**Step 1: Write the three migration files**

See the design doc at `docs/plans/2026-03-09-placement-capacity-design.md` for the exact SQL. Key details:

- 027: `machine_placements` with partial unique indexes on `(machine_id) WHERE released_at IS NULL` and `(host_id, vm_ip) WHERE released_at IS NULL`, FK to `machine_operations(id)`, CHECK on state
- 028: `capacity_policies` with scope uniqueness index, CHECK constraints on ratios and reserves. Host gets `host_pool` and `capacity_policy_id`. Machines get `home_host_id` (FK to hosts ON DELETE SET NULL) and `storage_mode`. Insert default global policy (2x CPU, 1x memory).
- 029: Backfill — running/provisioning machines get active placements, stopped machines with host_id get `home_host_id` set

**Step 2: Apply to Neon**

Run each migration:
```bash
OCM_DB=$(gcloud secrets versions access latest --secret=OCM_DATABASE_URL)
psql "$OCM_DB" -f backend/migrations/027_machine_placements.sql
psql "$OCM_DB" -f backend/migrations/028_capacity_policies.sql
psql "$OCM_DB" -f backend/migrations/029_backfill_placements.sql
```

**Step 3: Verify**

```bash
psql "$OCM_DB" -c "\d machine_placements"
psql "$OCM_DB" -c "\d capacity_policies"
psql "$OCM_DB" -c "SELECT count(*) FROM machine_placements"
```

**Step 4: Commit**

```bash
git add backend/migrations/027*.sql backend/migrations/028*.sql backend/migrations/029*.sql
git commit -m "feat: add machine_placements and capacity_policies schema"
```

---

### Task 2: Add Store Types and PlacementStore Interface

**Files:**
- Modify: `backend/internal/store/store.go` (add types + interface)

**Step 1: Add Placement and CapacityPolicy types**

```go
type Placement struct {
    ID                   string     `json:"id"`
    MachineID            string     `json:"machine_id"`
    HostID               int        `json:"host_id"`
    VMIP                 string     `json:"vm_ip"`
    State                string     `json:"state"`
    ReservedAt           time.Time  `json:"reserved_at"`
    ActivatedAt          *time.Time `json:"activated_at,omitempty"`
    ReleasedAt           *time.Time `json:"released_at,omitempty"`
    CreatedByOperationID *string    `json:"created_by_operation_id,omitempty"`
}

type CapacityPolicy struct {
    ID                     string    `json:"id"`
    Name                   string    `json:"name"`
    ScopeType              string    `json:"scope_type"`
    ScopeRef               *string   `json:"scope_ref,omitempty"`
    CPUOvercommitRatio     float64   `json:"cpu_overcommit_ratio"`
    MemoryOvercommitRatio  float64   `json:"memory_overcommit_ratio"`
    ReserveVCPUs           int       `json:"reserve_vcpus"`
    ReserveMemoryMB        int       `json:"reserve_memory_mb"`
    MaxMachineCount        *int      `json:"max_machine_count,omitempty"`
    Enabled                bool      `json:"enabled"`
    CreatedAt              time.Time `json:"created_at"`
    UpdatedAt              time.Time `json:"updated_at"`
}
```

**Step 2: Replace PlacementRepo interface**

Replace the current `PlacementRepo` (which has the old SQL-shaped methods) with:

```go
type PlacementRepo interface {
    // New placement-based methods
    ReservePlacement(ctx context.Context, tx pgx.Tx, machineID string, hostID int, vmIP, operationID string) (*Placement, error)
    ActivatePlacement(ctx context.Context, placementID string) error
    ReleasePlacement(ctx context.Context, machineID string) error
    GetActivePlacement(ctx context.Context, machineID string) (*Placement, error)
    ListActivePlacementsByHost(ctx context.Context, hostID int) ([]Placement, error)

    // Legacy methods kept during dual-write (will be removed later)
    PlaceMachineOnHost(ctx context.Context, machineID string, vcpus, memoryMB int, region, expectedImage string) (*Host, string, error)
    SoftStopMachine(ctx context.Context, machineID string, hostID, vcpus, memoryMB int) error
    ReAllocateHostCapacity(ctx context.Context, machineID string, hostID, vcpus, memoryMB int) error

    // Capacity policy
    GetCapacityPolicy(ctx context.Context, id string) (*CapacityPolicy, error)
    ResolveCapacityPolicy(ctx context.Context, hostID int, hostPool string) (*CapacityPolicy, error)
}
```

**Step 3: Verify build**

Run: `cd backend && go build ./...`

**Step 4: Commit**

```bash
git commit -m "feat: add Placement/CapacityPolicy types and PlacementRepo interface"
```

---

### Task 3: Implement PlacementRepo in postgres.go

**Files:**
- Modify: `backend/internal/store/postgres.go`

Implement all new methods. Key implementation details:

- `ReservePlacement`: INSERT into `machine_placements` with state='reserved', returns the placement
- `ActivatePlacement`: UPDATE SET state='active', activated_at=now() WHERE id=$1 AND state='reserved'
- `ReleasePlacement`: UPDATE SET state='released', released_at=now() WHERE machine_id=$1 AND released_at IS NULL
- `GetActivePlacement`: SELECT WHERE machine_id=$1 AND released_at IS NULL
- `ListActivePlacementsByHost`: SELECT WHERE host_id=$1 AND released_at IS NULL
- `ResolveCapacityPolicy`: Try host-specific (via capacity_policy_id), then pool, then global. Return the first enabled match.

**Step: Commit**

```bash
git commit -m "feat: implement PlacementRepo methods in postgres"
```

---

### Task 4: Create PlacementService with Tests

**Files:**
- Create: `backend/internal/fleet/placement.go`
- Create: `backend/internal/fleet/placement_test.go`
- Create: `backend/internal/fleet/policy.go`

**Step 1: Write failing tests first**

Create `backend/internal/fleet/placement_test.go` with mock store. Tests:

- `TestReserve_FreshPlacement` — host selected, placement created, counters updated, dual-write
- `TestReserve_NoEligibleHost` — returns error
- `TestReserve_UsesEffectiveCapacity` — with 2x CPU policy, host with 4 physical vCPUs can place 8
- `TestActivate_MarksActive` — placement state changes to active
- `TestReleaseSoft_PreservesHomeHost` — counters decremented, home_host_id set
- `TestReleaseHard_ClearsEverything` — counters decremented, host_id/vm_ip/home_host_id cleared
- `TestRecoverAffinity_ReusesHomeHost` — uses home_host_id for placement
- `TestRecoverAffinity_FreshWhenHomeDead` — falls back to any host
- `TestPolicyResolution_HostOverridesPool` — host-specific policy wins

**Step 2: Implement PlacementService**

`Reserve` is the most complex — it must run in a single transaction:

```go
func (ps *PlacementService) Reserve(ctx context.Context, machineID string, req PlacementRequest) (*Placement, string, error) {
    // This wraps the existing PlaceMachineOnHost logic but adds:
    // 1. Policy resolution for effective capacity
    // 2. Placement record creation
    // 3. Dual-write to machines.host_id/vm_ip

    // For v1: delegate to the existing PlaceMachineOnHost (which is already atomic)
    // and then create the placement record after.
    // This is safe because PlaceMachineOnHost holds FOR UPDATE locks.

    host, vmIP, err := ps.store.PlaceMachineOnHost(ctx, machineID, req.VCPUs, req.MemoryMB, req.Region, req.ExpectedImage)
    if err != nil {
        return nil, "", err
    }

    // Create placement record (dual-write)
    placement, err := ps.store.ReservePlacement(ctx, nil, machineID, host.ID, vmIP, req.OperationID)
    if err != nil {
        slog.Error("placement.reserve.record_failed", "machine_id", machineID, "error", err)
        // Non-fatal: legacy path still works
    }

    return placement, vmIP, nil
}
```

Note: For v1, `Reserve` wraps the existing `PlaceMachineOnHost` and adds a placement record. The effective capacity calculation is added to the SQL query in a follow-up step (or we modify PlaceMachineOnHost to accept effective values).

`policy.go` handles resolution:

```go
func ResolveEffectiveCapacity(policy *CapacityPolicy, host *Host) (effectiveVCPU, effectiveMem int) {
    effectiveVCPU = int(math.Floor(float64(host.CapacityVCPUs-policy.ReserveVCPUs) * policy.CPUOvercommitRatio))
    effectiveMem = int(math.Floor(float64(host.CapacityMemoryMB-policy.ReserveMemoryMB) * policy.MemoryOvercommitRatio))
    return
}
```

**Step 3: Run tests**

Run: `cd backend && go test ./internal/fleet/ -v`

**Step 4: Commit**

```bash
git commit -m "feat: add PlacementService with Reserve/Activate/Release/RecoverAffinity"
```

---

### Task 5: Wire PlacementService into RuntimeService

**Files:**
- Modify: `backend/internal/machines/runtime.go` (replace scheduler calls)
- Modify: `backend/cmd/server/main.go` (construct PlacementService)

**Step 1: Add placement field to RuntimeService**

Replace `scheduler *scheduler.Scheduler` with `placement *fleet.PlacementService` in the struct. Update `NewRuntimeService` accordingly.

**Step 2: Replace all scheduler calls**

Replace each call site (8 total, documented in the context above):

| Line | Old | New |
|------|-----|-----|
| 282, 292, 299, 314 | `rs.scheduler.PlaceMachine(ctx, machine)` | `rs.placement.Reserve(ctx, machine.ID, req)` then extract host/vmIP |
| 305 | `rs.scheduler.ReAllocateMachine(ctx, machine)` | `rs.placement.RecoverAffinity(ctx, machine.ID, req)` |
| 459 | `rs.scheduler.SoftReleaseMachine(ctx, machine)` | `rs.placement.Release(ctx, machine.ID, fleet.ReleaseSoft)` |
| 524 | `rs.scheduler.ReleaseMachine(ctx, machine)` | `rs.placement.Release(ctx, machine.ID, fleet.ReleaseHard)` |

Also in `pollVMStatus`, after `running` is confirmed:
```go
if placement != nil {
    _ = rs.placement.Activate(ctx, placement.ID)
}
```

**Step 3: Wire in main.go**

```go
placementSvc := fleet.NewPlacementService(db, cfg.GCPRegion, cfg.SnapshotName)
machineRuntime := machines.NewRuntimeService(db, placementSvc, agentCli, kv, tunnelMgr, progressTracker, machines.RuntimeConfig{...})
```

**Step 4: Run tests**

Run: `make test-go`

**Step 5: Commit**

```bash
git commit -m "refactor: replace scheduler with PlacementService in RuntimeService"
```

---

### Task 6: Update RuntimeService Tests

**Files:**
- Modify: `backend/internal/machines/runtime_test.go`

Replace scheduler mock with placement mock. Update all tests that were using the scheduler.

**Step: Commit**

```bash
git commit -m "test: update RuntimeService tests for PlacementService"
```

---

### Task 7: Remove Scheduler Package

**Files:**
- Delete: `backend/internal/scheduler/scheduler.go`
- Delete: `backend/internal/scheduler/scheduler_test.go`

Only after all tests pass with PlacementService.

**Step 1: Remove**

```bash
rm -rf backend/internal/scheduler/
```

**Step 2: Verify no remaining imports**

```bash
grep -r "scheduler" backend/ --include="*.go" | grep -v "_test.go" | grep -v vendor
```

Fix any remaining references.

**Step 3: Run tests**

Run: `make test-go`

**Step 4: Commit**

```bash
git commit -m "refactor: remove scheduler package (absorbed into fleet/placement)"
```

---

### Task 8: Final Verification and Push

**Step 1: Full test suite**

Run: `cd backend && go test ./...`

**Step 2: Build check**

Run: `go build ./cmd/server/ ./cmd/agent/`

**Step 3: Gateway E2E**

Run: `make test-gateway-e2e`

**Step 4: Push**

```bash
git push
```
