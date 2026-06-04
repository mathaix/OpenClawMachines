# Lifecycle Architecture Review

## Purpose

This document narrows the architecture review to the three parts that should move next:

1. machine lifecycle
2. host lifecycle
3. placement and reconciliation ownership

The goal is to make implementation test-first and incremental.

This document is a companion to:

- `docs/architecture_simplification.md`
- `docs/provisioning_logic.md`
- `docs/hardening.md`

## Current State Summary

## Machine Lifecycle Today

Current machine statuses:

- `stopped`
- `provisioning`
- `running`
- `error`

Current behavior:

- machine start is orchestrated in `startMachineInternal`
- machine stop is orchestrated in `handleStopMachine`
- machine delete is orchestrated in `handleDeleteMachine`
- machine reset from error is handled separately in `handleAdminResetMachine`
- start/stop/delete each directly coordinate store writes, placement, agent RPC, KV route sync, and tunnel cleanup

Current problems:

1. there is no explicit operation state
2. there is no compare-and-set style transition ownership
3. one machine can be observed as both logical state and placement state through the same row
4. failure cleanup is handler-specific instead of lifecycle-specific
5. `host_id` and `vm_ip` mean both active placement and restart affinity

## Host Lifecycle Today

Current host statuses:

- `provisioning`
- `ready`
- `draining`
- `stopped`
- `error` is used in practice even though the original schema comment only listed the four states above

Current behavior:

- host provision is a background goroutine started by `handleProvisionHost`
- host destroy is a background goroutine started by `handleDestroyHost`
- host heartbeat only updates `last_heartbeat`, IP, and versions
- no background host liveness reconciler marks hosts unreachable or terminated
- no provider-neutral enrollment lifecycle exists

Current problems:

1. no distinction between unreachable and terminated
2. no distinction between desired state and observed state
3. provider lifecycle and agent lifecycle are collapsed into one host row
4. the admin API assumes all hosts are provisioned VMs, not enrolled dedicated servers

## Placement Today

Current placement ownership is split across:

- `scheduler`
- `store.PlaceMachineOnHost`
- `store.SoftStopMachine`
- `store.ReAllocateHostCapacity`
- machine handlers

Current behavior:

- fresh placement allocates host capacity and assigns VM IP in one transaction
- stop releases capacity but keeps `host_id` and `vm_ip`
- restart reallocates capacity on the retained host
- delete releases capacity and clears placement

Current problems:

1. active placement and affinity are conflated
2. host counters are mutable denormalized state with weak invariants
3. route eligibility depends on a machine row joined to a host row rather than a placement record
4. burst placement and provider portability cannot be modeled cleanly

## Target Ownership Model

## Service Ownership

### MachineRuntimeService

Owns:

- machine start
- machine stop
- machine delete
- machine recovery
- machine state transitions

Does not own:

- host selection policy
- provider inventory
- route projection implementation

Dependencies:

- `MachineRepo`
- `PlacementService`
- `RouteService`
- `VMRuntimeClient`
- `ConfigRuntimeService`
- `CredentialService`

### HostFleetService

Owns:

- host registration
- host provisioning
- host drain and decommission
- host heartbeat processing
- host state transitions
- provider reconciliation

Does not own:

- machine-level start/stop/delete
- route resolution

Dependencies:

- `HostRepo`
- `HostProviderRegistry`
- `HostTransportRegistry`
- `FleetReconciler`

### PlacementService

Owns:

- host eligibility
- placement scoring
- capacity reservation
- affinity reuse
- placement release

Does not own:

- VM boot
- route sync
- tunnel setup

Dependencies:

- `HostRepo`
- `MachineRepo`
- `PlacementRepo`

### RouteService

Owns:

- route eligibility from DB state
- route projection to KV
- tunnel/DNS projection triggers
- route cleanup

Does not own:

- machine lifecycle
- host lifecycle

Dependencies:

- `RouteRepo`
- `KVProjector`
- `TunnelProjector`

### Reconciler Set

Owns:

- state repair when the request path fails
- drift repair between DB and providers
- drift repair between DB and KV/tunnel state

## Target Machine State Model

Split machine state into:

1. desired state
2. runtime state
3. placement state

### Desired State

- `stopped`
- `running`
- `deleted`

### Runtime State

- `new`
- `reserving`
- `starting`
- `running`
- `stopping`
- `stopped`
- `deleting`
- `deleted`
- `error`

### Placement State

- `none`
- `reserved`
- `active`
- `releasing`

### Machine State Diagram

```text
new -> stopped

stopped -> reserving -> starting -> running
starting -> error
reserving -> error

running -> stopping -> stopped
stopping -> error

stopped -> deleting -> deleted
running -> deleting -> deleted
error -> stopped        (operator/admin reset or reconciler recovery)
error -> deleting -> deleted
```

### State Semantics

#### `reserving`

- placement is being acquired
- no VM is running yet
- route must not exist yet

#### `starting`

- placement exists
- config is assembled
- agent RPC has been issued
- VM is not yet confirmed healthy

#### `running`

- VM is confirmed running
- route projection is eligible
- machine usage may accrue

#### `stopping`

- stop RPC has been issued
- placement may still exist temporarily
- route should be removed before completion

#### `error`

- terminal state for the current operation
- must include error code and operation context
- should not imply that placement is still valid

## Target Host State Model

Split host state into:

1. desired lifecycle state
2. observed lifecycle state
3. agent connectivity state

### Desired Lifecycle State

- `active`
- `draining`
- `maintenance`
- `decommissioned`

### Observed Lifecycle State

- `provisioning`
- `ready`
- `unreachable`
- `terminated`
- `stopped`
- `error`

### Agent Connectivity State

- `unknown`
- `online`
- `stale`
- `offline`

### Host State Diagram

```text
provisioning -> ready
provisioning -> error

ready -> draining
ready -> maintenance
ready -> unreachable
ready -> terminated

draining -> ready
draining -> stopped
draining -> unreachable
draining -> terminated

maintenance -> ready
maintenance -> unreachable
maintenance -> terminated

unreachable -> ready
unreachable -> terminated
unreachable -> error

stopped -> provisioning   (managed reprovision)
stopped -> ready          (registered host returns)
```

### Host Semantics

#### `unreachable`

- heartbeat stale or transport failed
- host may still exist
- not schedulable
- capacity cannot be trusted

#### `terminated`

- provider confirms the host is gone
- all active placements on the host must be reconciled

#### `maintenance`

- operator-controlled
- equivalent to unschedulable but not unhealthy

## Placement Model

## Core Rule

Placement must become a first-class record.

Do not continue storing active runtime placement only as:

- `machines.host_id`
- `machines.vm_ip`

### New Placement Concepts

#### Active Placement

The runtime location of the machine right now.

#### Home Affinity

Where host-local state wants to return when possible.

#### Portability

Whether the machine is allowed to move between providers or tiers.

## Capacity Strategy

The scheduler should stop using raw host capacity as the directly schedulable number.

Instead, every placement decision should use:

- physical capacity
- host reserve
- overprovisioning policy
- provider/tier safety class
- current active placements

### Core Rule

Schedule against `effective allocatable capacity`, not raw hardware capacity.

For each host:

```text
effective_vcpu_capacity =
  floor((physical_vcpus - reserve_vcpus) * cpu_overcommit_ratio)

effective_memory_capacity_mb =
  floor((physical_memory_mb - reserve_memory_mb) * memory_overcommit_ratio)

effective_disk_capacity_gb =
  physical_disk_gb - reserve_disk_gb
```

Then placement uses:

```text
available_vcpu = effective_vcpu_capacity - used_vcpu_by_active_placements
available_mem  = effective_memory_capacity_mb - used_mem_by_active_placements
available_disk = effective_disk_capacity_gb - used_disk_by_active_placements
```

If any available dimension is negative or below the requested workload size, the host is ineligible.

### Default Overprovisioning Strategy

Use provider and performance class to determine safe defaults.

#### Base Bare Metal

Examples:

- OVH dedicated
- Hetzner dedicated
- customer-owned server

Default policy:

- `cpu_overcommit_ratio = 2.0`
- `memory_overcommit_ratio = 1.0`
- `reserve_vcpus = 0` or `1` depending on host size
- `reserve_memory_mb = 2048` to `4096`
- `reserve_disk_gb = 10%` or fixed floor

Reasoning:

- CPU can usually be oversubscribed safely for bursty interactive workloads
- memory should stay conservative
- some headroom is needed for the host OS, tunnel, agent, browser sidecar overhead, and transient spikes

#### Burst Nested Virtualization

Examples:

- GCP nested-virt hosts

Default policy:

- `cpu_overcommit_ratio = 1.0` to `1.25`
- `memory_overcommit_ratio = 1.0`
- `reserve_vcpus = 1`
- `reserve_memory_mb = 4096`
- `reserve_disk_gb = larger than bare metal baseline`

Reasoning:

- nested virtualization already carries overhead
- burst capacity should prefer predictability over maximum density
- this tier is the expensive overflow tier and should not be packed as hard

#### Browser-Heavy Hosts

If browser companion VMs are common on a pool:

- lower CPU overcommit
- increase memory reserve
- optionally add a `browser_slot_reserve`

This should be policy-driven, not special-cased in handlers.

### Policy Hierarchy

Capacity policy should be resolved in this order:

1. host override
2. host pool policy
3. provider + performance class policy
4. global default

This allows:

- fleet-wide defaults
- OVH-specific defaults
- a special browser-heavy host pool
- one-off tuning for a problematic host

### Host Pools

Introduce host pools as the operational unit for capacity policy.

Examples:

- `ovh-base`
- `gcp-burst`
- `browser-enabled`
- `maintenance-canary`

Placement targets pools, not individual hard-coded provider assumptions.

### Scheduler Flow With Capacity Policy

For each placement request:

1. filter hosts by lifecycle and health
   - observed state must be `ready`
   - connectivity must be `online`
   - desired state must be schedulable, not `draining` or `maintenance`
2. filter by compatibility
   - provider eligibility
   - portability class
   - artifact/image compatibility
   - required capabilities
3. resolve effective capacity policy
4. compute allocatable capacity and current pressure
5. reject hosts with insufficient effective capacity
6. score remaining hosts
7. reserve placement on the best eligible host

### Recommended Scoring

Use a weighted score rather than memory-only best fit.

Inputs:

- normalized CPU pressure after placement
- normalized memory pressure after placement
- disk pressure after placement
- stale-headroom penalty
- capacity tier preference
- affinity bonus
- cost penalty

Example scoring direction:

```text
score =
  affinity_bonus
  + base_tier_bonus
  - cpu_fragmentation_penalty
  - memory_fragmentation_penalty
  - disk_fragmentation_penalty
  - cost_penalty
  - stale_headroom_penalty
```

Policy should remain explicit and inspectable in admin UI.

### Headroom Strategy

Do not run the fleet to theoretical maximum.

Add explicit headroom concepts:

- `pool_burst_headroom_percent`
- `provider_reserve_hosts`
- `recovery_reserve_percent`

Recommended first version:

- base pools keep `10%` to `15%` recovery headroom
- burst pools may run hotter, but only for eligible workloads

This matters for:

- host death recovery
- rolling updates
- tunnel or provider instability

### Overprovisioning Safety Rules

1. overprovisioning policy changes apply to new placements first
2. lowering capacity policy must not silently evict running workloads
3. if a host becomes overcommitted under the new policy, mark it:
   - `overcommitted`
   - unschedulable for new placements
   - candidate for gradual drain
4. policy must have hard validation bounds

Recommended validation bounds:

- `cpu_overcommit_ratio`: `1.0` to `3.0`
- `memory_overcommit_ratio`: `1.0` to `1.2`
- reserves must be non-negative
- effective allocatable capacity must remain positive

### Admin Panel Controls

Add a `Capacity Policies` area to the admin panel.

Required controls:

1. global default policy
2. provider-level policy
3. host-pool policy
4. host-specific override
5. enable/disable pool for scheduling
6. maintenance and canary pool controls

Fields:

- `cpu_overcommit_ratio`
- `memory_overcommit_ratio`
- `reserve_vcpus`
- `reserve_memory_mb`
- `reserve_disk_gb`
- `max_machine_count`
- `burst_headroom_percent`
- `ballooning_enabled`
- `balloon_credit_percent`
- `max_balloon_credit_mb`
- `min_guest_memory_floor_mb`
- `balloon_stats_required`
- `memory_hotplug_enabled`
- `cost_class`
- `performance_class`
- `allow_burst_eligible_only`

### Admin UX Requirements

The admin UI should show:

1. physical capacity
2. effective allocatable capacity
3. active usage
4. overcommit ratio
5. policy source
   - global
   - provider
   - pool
   - host override
6. simulation preview before applying policy changes

Simulation preview should answer:

- how many standard machines fit now
- how many would fit under the proposed policy
- which hosts become overcommitted

### Capacity Policy Data Model

Add a first-class policy table rather than burying ratios inside host rows.

```sql
CREATE TABLE capacity_policies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    scope_type TEXT NOT NULL,
    scope_ref TEXT,
    cpu_overcommit_ratio NUMERIC(4,2) NOT NULL,
    memory_overcommit_ratio NUMERIC(4,2) NOT NULL DEFAULT 1.00,
    reserve_vcpus INT NOT NULL DEFAULT 0,
    reserve_memory_mb INT NOT NULL DEFAULT 0,
    reserve_disk_gb INT NOT NULL DEFAULT 0,
    max_machine_count INT,
    burst_headroom_percent INT NOT NULL DEFAULT 0,
    ballooning_enabled BOOLEAN NOT NULL DEFAULT false,
    balloon_credit_percent INT NOT NULL DEFAULT 0,
    max_balloon_credit_mb INT NOT NULL DEFAULT 0,
    min_guest_memory_floor_mb INT NOT NULL DEFAULT 0,
    balloon_stats_required BOOLEAN NOT NULL DEFAULT true,
    memory_hotplug_enabled BOOLEAN NOT NULL DEFAULT false,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Scope examples:

- `global`
- `provider`
- `pool`
- `host`

### Placement Reservation Rule

Capacity should be charged only against active placements.

That means:

- `reserved` placements consume schedulable capacity
- `active` placements consume schedulable capacity
- `released` or inactive affinity does not

Home affinity should not continue consuming host capacity after a soft stop.

### Hybrid Capacity Rule

For `base + burst` operation:

1. base pools are filled first
2. burst pools are only used when:
   - no eligible base host exists
   - base recovery headroom would be violated
   - workload is burst-eligible
3. overprovisioning should be stricter on burst pools than on base pools

### Firecracker Memory Elasticity

Firecracker features like `virtio-balloon` and `virtio-mem` should be factored into capacity planning, but not treated as the primary hard-capacity model.

Upstream status:

- Firecracker `v1.14.0` added memory hot-plugging through `virtio-mem`
- Firecracker `v1.14.0` added `virtio-balloon` free page reporting and hinting, but explicitly marked free page reporting as developer preview and not for production
- Firecracker `v1.14.0` also expanded balloon stats support
- Firecracker `v1.14.2` fixed a memory-file corruption bug affecting VMs with multiple memory slots, including VMs using memory hot-plugging

Implication:

- ballooning is real and useful
- but it should be introduced as a controlled reclaim mechanism, not as an excuse to make memory overcommit aggressive by default

### Recommended Ballooning Policy

Use a two-layer memory model:

1. `committed_memory_mb`
   - hard memory committed by active placements
   - this is the primary schedulable dimension
2. `reclaimable_memory_mb`
   - memory that may be reclaimable through ballooning, free page reporting, or hot-plug policies
   - this must be observed, not assumed

Scheduler rule:

- only `committed_memory_mb` is used for hard placement admission in the first production version
- `reclaimable_memory_mb` is used only for:
  - alerts
  - simulation
  - future soft admission policies on explicitly enabled pools

### Why Not Schedule Directly Against Ballooning

Because reclaimable guest memory is conditional:

- guest kernel support matters
- guest driver behavior matters
- workload memory locality matters
- reclaim latency matters
- upstream free page reporting is still called out as developer preview, not production

So the safe policy is:

- CPU overcommit can be proactive
- memory reclaim should be reactive and measured

### Ballooning Rollout Levels

#### Level 0: No Scheduler Dependence

Use ballooning only to:

- collect metrics
- observe reclaimability
- reduce host pressure under stress

Scheduler still places from hard committed memory only.

This should be the first implementation.

#### Level 1: Conservative Soft Credit

Allow a small reclaim credit on explicitly enabled pools.

Example:

- scheduler may count at most `min(observed_reclaimable_mb * 0.25, configured_balloon_credit_mb)`

Conditions:

- host pool must opt in
- recent balloon stats must be available
- reclaim success rate must be above threshold
- host must not currently be under pressure

#### Level 2: Elastic Memory Pools

Only after operational proof:

- use `virtio-mem` or balloon-driven elasticity on specific pools
- treat those pools as a distinct performance class
- never mix this behavior silently into standard base-capacity pools

### Admin Policy Controls For Ballooning

Add optional capacity-policy fields:

- `ballooning_enabled`
- `balloon_credit_percent`
- `max_balloon_credit_mb`
- `min_guest_memory_floor_mb`
- `balloon_stats_required`
- `balloon_reclaim_threshold_percent`
- `memory_hotplug_enabled`

These should default to conservative values:

- ballooning disabled for scheduling credit
- metrics-only mode enabled later if instrumentation is added

### Host Pool Recommendation

Do not enable balloon-driven scheduling across the whole fleet.

Create dedicated pools such as:

- `ovh-base-standard`
- `ovh-base-elastic-memory`
- `gcp-burst-standard`

Only the elastic-memory pool should be allowed to use any reclaim credit.

### Runtime Requirements

This is not only a scheduler feature.

It requires runtime/orchestrator work:

1. configure balloon or virtio-mem in Firecracker VM config
2. expose balloon stats and memory metrics through the agent
3. record observed reclaimability per VM and per host
4. add failure handling if reclaim does not occur quickly enough

Current code impact:

- the repository uses `github.com/firecracker-microvm/firecracker-go-sdk v1.0.0`
- the current orchestrator code has no ballooning or memory hot-plug integration
- so this should be treated as a new runtime capability, not a minor scheduling tweak

### Scheduling Rule For First Implementation

For the first production rollout:

1. keep `memory_overcommit_ratio = 1.0`
2. allow CPU overcommit as planned
3. enable balloon metrics collection only
4. surface reclaimable memory in admin UI
5. do not grant scheduler memory credit from ballooning yet

### Capacity Alerts

Add alerts for:

- host overcommitted under current policy
- pool headroom below threshold
- burst spillover rate increasing
- repeated placement rejections by policy

These should feed the admin panel, not just logs.

## Proposed Schema Changes

## Machines Table

Add:

- `desired_state TEXT NOT NULL DEFAULT 'stopped'`
- `runtime_state TEXT NOT NULL DEFAULT 'stopped'`
- `runtime_error_code TEXT`
- `runtime_error_message TEXT`
- `current_operation_id UUID`
- `home_host_id INT NULL`
- `storage_mode TEXT NOT NULL DEFAULT 'host_local'`
- `portability_class TEXT NOT NULL DEFAULT 'sticky'`

Remove later:

- overloading `status` as the only lifecycle field

Migration approach:

- keep `status` during migration
- backfill `desired_state` and `runtime_state` from `status`
- migrate handlers and services
- only then deprecate `status`

## Hosts Table

Add:

- `provider TEXT NOT NULL DEFAULT 'gcp'`
- `provider_class TEXT NOT NULL DEFAULT 'managed'`
- `lifecycle_mode TEXT NOT NULL DEFAULT 'provisioned'`
- `provider_host_id TEXT`
- `desired_state TEXT NOT NULL DEFAULT 'active'`
- `observed_state TEXT NOT NULL DEFAULT 'provisioning'`
- `connectivity_state TEXT NOT NULL DEFAULT 'unknown'`
- `agent_endpoint TEXT`
- `transport_kind TEXT NOT NULL DEFAULT 'public_http'`
- `performance_class TEXT NOT NULL DEFAULT 'nested_virt'`
- `capacity_tier TEXT NOT NULL DEFAULT 'base'`
- `host_pool TEXT NOT NULL DEFAULT 'default'`
- `capacity_policy_id UUID NULL`
- `labels JSONB`
- `capabilities JSONB`
- `provider_metadata JSONB`

Keep for migration:

- `vm_name`
- `zone`
- `region`
- `machine_type`
- `source_image`

## Machine Placements Table

Create:

```sql
CREATE TABLE machine_placements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    host_id INT NOT NULL REFERENCES hosts(id),
    vm_ip TEXT,
    state TEXT NOT NULL,
    reserved_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at TIMESTAMPTZ,
    released_at TIMESTAMPTZ,
    created_by_operation_id UUID,
    UNIQUE (machine_id) WHERE released_at IS NULL
);
```

Meaning:

- exactly one active placement per machine
- placement has its own lifecycle

## Machine Operations Table

Create:

```sql
CREATE TABLE machine_operations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    state TEXT NOT NULL,
    requested_by_user_id INT,
    idempotency_key TEXT,
    error_code TEXT,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);
```

Purpose:

- operation ownership
- idempotency
- auditability

## Host Operations Table

Create:

```sql
CREATE TABLE host_operations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    host_id INT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    state TEXT NOT NULL,
    requested_by_user_id INT,
    error_code TEXT,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);
```

## Capacity Policies Table

Create:

```sql
CREATE TABLE capacity_policies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    scope_type TEXT NOT NULL,
    scope_ref TEXT,
    cpu_overcommit_ratio NUMERIC(4,2) NOT NULL,
    memory_overcommit_ratio NUMERIC(4,2) NOT NULL DEFAULT 1.00,
    reserve_vcpus INT NOT NULL DEFAULT 0,
    reserve_memory_mb INT NOT NULL DEFAULT 0,
    reserve_disk_gb INT NOT NULL DEFAULT 0,
    max_machine_count INT,
    burst_headroom_percent INT NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Purpose:

- explicit scheduling policy
- policy inheritance
- admin configurability
- auditability

## Constraints

Add hard invariants:

- `used_vcpus >= 0`
- `used_memory_mb >= 0`
- `machine_count >= 0`
- `capacity_vcpus >= used_vcpus`
- `capacity_memory_mb >= used_memory_mb`
- unique active `(host_id, vm_ip)` in placements

## Reconciler Responsibilities

## 1. Host Liveness Reconciler

Runs periodically.

Responsibilities:

- find stale heartbeats
- mark hosts `unreachable`
- query provider where possible
- promote `unreachable -> terminated` if provider says host is gone
- unschedule stale hosts immediately

Inputs:

- `last_heartbeat`
- provider observation
- transport health checks

Outputs:

- host observed state
- machine recovery actions for affected placements

## 2. Placement Reconciler

Responsibilities:

- find placements whose host is terminated or unreachable too long
- release or invalidate stale placements
- repair host counters from placement truth

Inputs:

- host observed state
- placement rows
- agent/provider observation

Outputs:

- corrected host counters
- corrected machine runtime states

## 3. Route Reconciler

Responsibilities:

- ensure running machines have KV projection
- ensure non-running machines do not
- ensure tunnel/DNS projections match DB truth

Inputs:

- machine runtime state
- active placement
- route projection status

Outputs:

- KV repair
- tunnel cleanup or recreation

## 4. Machine Runtime Reconciler

Responsibilities:

- resolve machines stuck in `starting`, `stopping`, or `deleting`
- convert timed-out operations into `error`
- reconcile agent-reported reality back into DB state

Inputs:

- machine operations
- machine runtime state
- agent runtime query

Outputs:

- machine state repair
- operation completion/failure

## Recommended Service APIs

## MachineRuntimeService

```go
type MachineRuntimeService interface {
    Start(ctx context.Context, machineID string, actor Actor, key string) (*StartResult, error)
    Stop(ctx context.Context, machineID string, actor Actor, key string) error
    Delete(ctx context.Context, machineID string, actor Actor, key string) error
    ResetError(ctx context.Context, machineID string, actor Actor) error
}
```

### Start flow ownership

1. validate machine transition
2. create operation row
3. gather secrets and credentials
4. request placement
5. assemble config
6. request route/tunnel prerequisites
7. call agent/runtime
8. set machine to `starting`
9. hand off to runtime reconciler to confirm `running`

### Stop flow ownership

1. validate transition
2. create operation row
3. set machine to `stopping`
4. remove route projection
5. request graceful stop
6. release active placement or preserve home affinity
7. complete as `stopped`

### Delete flow ownership

1. validate transition
2. create operation row
3. remove route projection
4. request destroy if active
5. release placement
6. remove machine row or mark tombstoned

## HostFleetService

```go
type HostFleetService interface {
    Provision(ctx context.Context, req ProvisionHostRequest, actor Actor) (*Host, error)
    Register(ctx context.Context, req RegisterHostRequest) (*Host, error)
    ProcessHeartbeat(ctx context.Context, hb HostHeartbeat) error
    Drain(ctx context.Context, hostID int, actor Actor) error
    EnterMaintenance(ctx context.Context, hostID int, actor Actor) error
    Decommission(ctx context.Context, hostID int, actor Actor) error
    Reconcile(ctx context.Context, hostID int) error
}
```

## PlacementService

```go
type PlacementService interface {
    Reserve(ctx context.Context, machineID string, req PlacementRequest) (*Placement, error)
    Activate(ctx context.Context, placementID string) error
    Release(ctx context.Context, machineID string, mode ReleaseMode) error
    RecoverAffinity(ctx context.Context, machineID string) (*Placement, error)
}
```

## Test-First Review Outputs

## First Test Set

### Machine lifecycle tests

Create:

- `backend/internal/machines/service_start_test.go`
- `backend/internal/machines/service_stop_test.go`
- `backend/internal/machines/service_delete_test.go`

Write first:

1. `Start` rejects invalid current states
2. `Start` creates an operation and a reserved placement before agent RPC
3. `Start` marks machine `starting`
4. `Start` rolls back placement on agent create failure
5. `Stop` removes route before completing state transition
6. `Stop` does not free capacity if runtime stop is unconfirmed unless policy explicitly allows force-stop
7. `Delete` removes route and placement in a consistent order
8. `Delete` is idempotent

### Host lifecycle tests

Create:

- `backend/internal/fleet/service_heartbeat_test.go`
- `backend/internal/fleet/service_reconcile_test.go`

Write first:

1. heartbeat updates connectivity state
2. stale heartbeat marks host `unreachable`
3. terminated provider observation marks host `terminated`
4. terminated host invalidates active placements
5. draining host is excluded from new placements

### Placement tests

Create:

- `backend/internal/fleet/placement_service_test.go`

Write first:

1. reserve fresh placement
2. reject placement on stale host
3. preserve home affinity on soft stop
4. clear active placement on hard host loss
5. keep unique `(host_id, vm_ip)` invariant
6. compute effective capacity from policy, not raw host capacity
7. respect policy precedence: host > pool > provider > global
8. lowering overcommit policy marks hosts unschedulable rather than evicting workloads
9. ballooning metrics do not grant schedulable memory credit in the initial rollout
10. balloon credit is only applied on explicitly enabled pools in later rollout stages

### Route and reconciler tests

Create:

- `backend/internal/routing/service_test.go`
- `backend/internal/reconcile/route_reconciler_test.go`

Write first:

1. DB truth produces route projection
2. non-running machine removes route projection
3. reconciler repairs missing KV route
4. reconciler removes stale KV route for stopped machine

## First PR Sequence

## PR 1: Introduce lifecycle service seams

Goal:

- move orchestration out of handlers without changing schema yet

Tests first:

- existing start/stop/delete behavior under service wrappers

Implementation:

- add `MachineRuntimeService`
- move start/stop/delete orchestration from handlers into service
- handlers become thin wrappers

## PR 2: Add explicit host liveness policy

Goal:

- stop scheduling stale hosts

Tests first:

- placement excludes stale heartbeat
- heartbeat transitions connectivity state

Implementation:

- add stale-heartbeat filter
- add host liveness reconciler skeleton
- add `unreachable` and `terminated` states

## PR 3: Add placement record

Goal:

- separate active placement from affinity
- introduce effective capacity policy

Tests first:

- fresh reservation
- affinity recovery
- release semantics
- policy-based allocatable capacity
- overcommit policy precedence
- balloon metrics without memory credit
- elastic-memory pool credit only on opt-in pools

Implementation:

- create `machine_placements`
- create `capacity_policies`
- write through placement service
- resolve effective capacity from policy hierarchy
- keep legacy `machines.host_id/vm_ip` temporarily mirrored

## PR 3A: Add balloon observability only

Goal:

- make Firecracker memory elasticity measurable before it affects scheduling

Tests first:

- host reports balloon and reclaim telemetry when enabled
- scheduler ignores balloon reclaim credit in this stage

Implementation:

- add runtime support for balloon stats collection
- expose metrics through agent and host detail APIs
- surface reclaimable memory as advisory telemetry only

## PR 4: Add operation rows and CAS semantics

Goal:

- prevent duplicate start/stop/delete races

Tests first:

- duplicate start rejected or deduplicated
- duplicate stop idempotent
- delete while start in progress rejected or serialized

Implementation:

- create operation tables
- service-level transition ownership

## PR 5: Add provider-neutral host model

Goal:

- prepare for OVH/Hetzner/self-owned host enrollment
- support pool-based capacity policy assignment

Tests first:

- provisioned host
- enrolled host
- non-GCP heartbeat path
- pool policy resolution in hybrid base/burst fleets

Implementation:

- add host provider fields
- add host pool and policy assignment fields
- introduce `HostFleetService`
- keep GCP as first provider driver

## PR 6: Move route projection into RouteService

Goal:

- stop doing KV/tunnel side effects in lifecycle handlers

Tests first:

- sync on machine running
- delete on machine stop/delete
- reconcile stale KV entries

Implementation:

- centralize route sync and cleanup

## PR 7: Remove legacy overloaded state

Goal:

- finish migration from old `status` semantics

Tests first:

- end-to-end lifecycle through desired/runtime/placement state model

Implementation:

- stop using overloaded machine and host status fields
- remove mirrored legacy writes

## Orchestrator Architecture Review

## Current Orchestrator Shape

The Firecracker orchestrator currently owns too many concerns directly:

- runtime registry
- Firecracker process startup and shutdown
- rootfs staging
- data volume version sidecars
- browser companion VM lifecycle
- crash recovery persistence
- network TAP and VM pair rule cleanup
- asynchronous readiness mutation

This is the right place for host-local runtime ownership, but it is not split into clear subcomponents. That is why the main runtime file carries lifecycle, storage, networking, and recovery complexity in one place.

## Main Orchestrator Findings

### 1. No authoritative VM state machine

`Create()` installs the VM into the in-memory map only after a large amount of setup, then a background health goroutine mutates status later. `Stop()` and `Destroy()` remove the VM from the map before shutdown succeeds.

That means:

- the registry is not an authoritative source of runtime truth
- `Get()` and `List()` can race real process state
- crash recovery persistence does not line up cleanly with runtime state transitions

### 2. Crash recovery is destructive, not reconciling

The orchestrator persists PID and path state, but on agent restart it does not attempt reattach or verified graceful shutdown. It just kills the saved PID and tears down resources.

That is architecturally wrong for a runtime layer because restart recovery should be one of:

- reattach
- reconcile and mark failed
- verified graceful termination

It should not default to blind kill and cleanup.

### 3. `RegisterPending` is a weak concurrency seam

The current pending-registration approach is an in-memory placeholder, not a real operation lease. There is still a gap between deleting the placeholder and publishing the final running VM record.

That is why duplicate create remains possible inside one agent process.

### 4. Browser companion VM is not modeled as a subresource

The browser VM is effectively a nested lifecycle inside the main VM lifecycle, but it does not have its own state model, cleanup contract, or compensating cleanup path. Pair-rule ownership is especially weak.

### 5. The interface is behind the runtime you actually need

The orchestrator interface exposes:

- `Create`
- `Stop`
- `Destroy`
- `Get`
- `List`
- metadata update methods
- `RegisterPending`
- `RollbackDataVolume`
- `Shutdown`

It does not expose or model:

- reattach
- reconcile
- operation leases
- snapshot and restore
- pause and resume
- memory elasticity telemetry
- host-local runtime events

That interface is too small for the failure handling you need.

## Firecracker Feature Gap

The current runtime does not integrate:

- memory ballooning
- memory hot-plug
- snapshot and restore
- pause and resume flows

That is not just a product choice. It means the runtime architecture has not yet separated:

- hard committed capacity
- reclaimable capacity
- resumable VM state
- restart recovery state

The first step is not enabling more Firecracker features. The first step is making runtime ownership explicit enough that new features have somewhere coherent to live.

## Target Runtime Split

Split the current orchestrator into internal components behind one public service:

### `VMRegistry`

Owns:

- authoritative in-memory runtime records
- state transitions
- operation lease ownership
- event publication for readiness and failure

### `ProcessManager`

Owns:

- Firecracker process construction
- start
- graceful shutdown
- force stop
- reattach or verified termination on restart

### `RootfsManager`

Owns:

- immutable base image selection
- local staged image activation
- per-VM writable rootfs creation
- data volume version tracking and rollback

### `VMNetworkManager`

Owns:

- TAP lifecycle
- pair-rule lifecycle
- cleanup compensation on partial failures

### `RecoveryManager`

Owns:

- persisted runtime record loading
- process verification
- reattach policy
- orphan cleanup policy

## Runtime State Rules

### Main VM

Use explicit runtime phases:

- `creating`
- `booting`
- `ready`
- `stopping`
- `stopped`
- `failed`

### Browser VM

Treat the browser VM as a separate runtime subresource:

- `absent`
- `creating`
- `ready`
- `failed`
- `stopping`
- `stopped`

The main VM should not silently absorb browser lifecycle ambiguity into one overloaded status field.

## Required Simplifications

### 1. Replace `RegisterPending` with an operation lease

One `machine_id` should have one active create operation at a time. No delete-and-reinsert registry gap.

### 2. Serialize status transitions

Background health probes should publish results back through the registry. They should not mutate shared VM state directly.

### 3. Unify failure cleanup

Every create path should unwind through a single compensating cleanup path that owns:

- TAP removal
- pair-rule removal
- rootfs removal
- socket removal
- state persistence cleanup

### 4. Make restart recovery policy explicit

For each persisted runtime record:

- verify process identity
- reattach if supported
- otherwise attempt graceful termination
- then force cleanup only as the last step

### 5. Keep new Firecracker features behind observability first

Before scheduling against ballooning or building snapshot workflows:

- collect metrics
- expose them in admin and host APIs
- validate correctness under restart and failure

## Test-First Orchestrator Plan

Write failing tests first for:

### Registry and operation ownership

- duplicate `Create()` for the same machine is rejected or deduplicated
- `Stop()` does not erase runtime ownership before shutdown result is known
- `Destroy()` does not lose cleanup ownership on partial failure

### Recovery

- persisted VM state does not lead to blind PID kill without verification
- restart cleanup preserves data volume while cleaning ephemeral resources
- browser companion cleanup removes pair rules on restart recovery

### Browser VM lifecycle

- pair rule removed on every early-return path
- browser readiness transitions do not corrupt main VM status

### Async readiness

- main VM status transitions happen through one serialized path
- background readiness failures persist state consistently

## Implementation Sequence For Runtime Simplification

### Step 1

Add failing tests around duplicate create, stop/destroy ownership, and browser pair-rule cleanup.

### Step 2

Introduce `VMRegistry` and move status mutation behind it without changing runtime behavior.

### Step 3

Introduce `VMNetworkManager` and centralize all cleanup compensation paths.

### Step 4

Introduce `RecoveryManager` and replace blind orphan kill with verified recovery policy.

### Step 5

Add observability-only support for memory elasticity telemetry.

## Recommended Immediate Next Step

Do not start with provider abstraction or admin panel changes.

Start with:

1. `MachineRuntimeService`
2. stale-host scheduling exclusion
3. basic host liveness reconciler

That sequence gives the highest leverage because it:

- reduces handler complexity
- makes fleet state more trustworthy
- prepares the system for provider-neutral evolution
- creates the seam needed for the rest of the architecture work
