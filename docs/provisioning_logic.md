# Provisioning Logic Review and Recommendations

## Scope

This document captures detailed recommendations for:

- host provisioning
- machine scheduling
- bin packing
- stale host detection and recovery
- control-plane/data-plane consistency
- architectural changes needed to make the lifecycle robust

The recommendations are based on the current implementation in:

- `backend/internal/provisioner/provisioner.go`
- `backend/internal/scheduler/scheduler.go`
- `backend/internal/store/postgres.go`
- `backend/internal/api/server.go`

## Current Behavior Summary

### Host provisioning

The control plane:

1. creates a `hosts` row with `status = 'provisioning'`
2. creates a Cloudflare tunnel for the host
3. provisions a GCE instance
4. writes instance metadata and disk sizing
5. waits for agent `/health`
6. marks the host `ready`

This is implemented in `backend/internal/provisioner/provisioner.go`.

### Machine start

The control plane:

1. decrypts secrets and credentials
2. generates tokens if missing
3. creates a per-machine tunnel
4. either:
   - re-allocates capacity on the previous host for restart, or
   - places the machine on a new host
5. calls the agent to create the VM
6. polls the agent until the VM is `running` or `error`

This is implemented in `backend/internal/api/server.go` and `backend/internal/scheduler/scheduler.go`.

### Bin packing

Placement is currently:

- constrained by `status = 'ready'`
- constrained by free vCPU and free memory
- optionally constrained by region and source image
- ordered by `used_memory_mb DESC`

This is implemented in `PlaceMachineOnHost()` in `backend/internal/store/postgres.go`.

### Stop/restart affinity

Graceful stop uses a "soft release":

- host counters are decremented
- machine `status` becomes `stopped`
- `host_id` and `vm_ip` are preserved

This is intended to preserve data volume locality on restart.

## Main Problems

## 1. Stale hosts remain schedulable

The backend records `last_heartbeat`, but scheduling does not use heartbeat freshness. A host can be killed from the cloud console and remain `ready` in Postgres indefinitely, so the scheduler can continue placing machines onto a dead host.

Current gap:

- heartbeats update `last_heartbeat`
- placement only checks `status = 'ready'`
- there is no reconciler that marks stale hosts offline

Impact:

- placement to dead hosts
- repeated provisioning failures
- capacity accounting drifts away from reality
- users see machines stuck in `provisioning` or `error`

## 2. Host state conflates "schedulable" and "alive"

`status = 'ready'` is being used as both:

- "this host was healthy at some point"
- "this host is currently reachable and can take work"

Those are different facts. A host can be:

- provisioned but not yet healthy
- recently healthy but currently unreachable
- intentionally draining
- terminated externally

The current state model does not distinguish these conditions well enough.

## 3. Machine placement and machine lifecycle are not serialized strongly enough

The machine start path is not modeled as a proper state machine with a single owner. Repeated start requests, async pollers, and error paths can all mutate machine state independently.

Impact:

- duplicate placement attempts
- double capacity reservation
- state transitions that do not match actual VM state

## 4. Capacity accounting is optimistic and can drift

The database is the source of truth for capacity accounting, but it is only partially reconciled with agent reality.

Examples:

- a stop/delete agent request can fail, but the database can still release capacity
- a host can die externally, but `used_vcpus`, `used_memory_mb`, and `machine_count` remain allocated
- async VM creation failure paths do not consistently release capacity

Over time this makes scheduling unreliable.

## 5. Host affinity and active placement are encoded in the same columns

Today `host_id` and `vm_ip` mean both:

- the machine's currently active runtime placement
- the host where its persistent data volume lives

That works for graceful restart on the same host, but fails when the host disappears permanently. The system cannot distinguish:

- "machine is stopped and can restart on the same host"
- "machine points to a dead host and needs recovery"

## 6. Bin packing is too narrow

Current bin packing is effectively "pick the fullest host by memory usage that still fits." It ignores several important constraints:

- stale heartbeat / liveness
- free IP slots
- disk pressure
- actual machine count saturation
- version skew risk
- per-region warm spare targets

This is a useful first approximation, but not sufficient for reliable operations.

## Recommended Changes

## A. Immediate safety fixes

These changes should be implemented first because they prevent obviously bad scheduling decisions.

### A1. Make stale hosts unschedulable

Change host selection so a host must satisfy both:

- `status = 'ready'`
- `last_heartbeat > now() - stale_threshold`

Recommended threshold:

- heartbeat every 30s
- stale after 90s (3 missed heartbeats)

Example placement filter:

```sql
WHERE status = 'ready'
  AND last_heartbeat IS NOT NULL
  AND last_heartbeat > now() - interval '90 seconds'
```

This alone removes the most serious scheduling bug: placing onto a dead host that still looks `ready`.

### A2. Add explicit dead-host reconciliation

Add a background reconciler in the control plane that runs every 30s and:

1. finds hosts with stale or missing heartbeat
2. marks them `unreachable`
3. optionally checks GCP to confirm whether the instance still exists
4. if GCP confirms the instance is gone, marks host `terminated`

Recommended host state model:

- `provisioning`
- `ready`
- `draining`
- `unreachable`
- `terminated`
- `stopped`
- `error`

`unreachable` means:

- do not schedule here
- host may still exist and recover

`terminated` means:

- the instance is gone
- active placement on that host must be considered lost

### A3. Reconcile machines when a host is confirmed dead

When a host transitions to `terminated`:

1. list all machines with active placement on that host
2. delete KV routes
3. mark machine status `error`
4. set a clear status message such as `host lost`
5. clear active runtime placement

Do not silently leave them `stopped` or `provisioning`.

Users and operators need a clear signal that recovery is required.

### A4. Do not keep scheduling on raw DB counters after host loss

When a host is externally killed, the DB counters are no longer trustworthy. They must not be reused as-is.

For dead hosts:

- remove them from scheduling immediately
- do not attempt incremental counter repair
- treat the host as lost and recover machines separately

## B. Fix the data model

The biggest structural issue is overloading `host_id`.

### B1. Split active placement from persistence locality

Recommended machine columns:

- `active_host_id`
- `active_vm_ip`
- `data_home_host_id`
- `placement_state`

Meaning:

- `active_host_id`: where the VM is currently running
- `active_vm_ip`: current bridge IP on that host
- `data_home_host_id`: where the machine's persistent volume currently lives
- `placement_state`: `unplaced`, `placed`, `recovering`, `orphaned`, etc.

Benefits:

- graceful stop can clear `active_host_id` while preserving `data_home_host_id`
- dead host handling can mark `data_home_host_id` as unavailable without pretending the machine is still actively placed
- future host migration becomes much easier to model

### B2. Add placement attempts / operation ownership

Introduce a machine operation or lease record so only one lifecycle operation owns a machine at a time.

Recommended table or columns:

- `machines.operation_id`
- `machines.operation_type`
- `machines.operation_started_at`
- `machines.operation_owner`

Or use a separate `machine_operations` table with idempotency keys.

This prevents:

- duplicate start requests
- racing stop/start
- orphaned async pollers updating stale machine state

### B3. Preserve historical placement events

Add an explicit placement history/event record:

- `machine.placement.requested`
- `machine.placement.assigned`
- `machine.placement.failed`
- `machine.placement.lost`
- `machine.recovery.started`

This will make operations much easier to debug than inspecting a single mutable row.

## C. Make lifecycle transitions explicit

The machine lifecycle should become a state machine, not a loose collection of updates.

### C1. Recommended machine states

Use states that correspond to real control-plane operations:

- `stopped`
- `starting`
- `running`
- `stopping`
- `deleting`
- `error`
- `recovering`

Avoid using `provisioning` for everything. It currently covers multiple different phases:

- scheduler placement
- token generation
- tunnel creation
- VM creation
- boot wait

Those are not the same operational condition.

### C2. Use compare-and-set transitions

Every state transition should assert the previous state.

Examples:

- `stopped -> starting`
- `starting -> running`
- `starting -> error`
- `running -> stopping`
- `stopping -> stopped`

This should happen in SQL:

```sql
UPDATE machines
SET status = 'starting', operation_id = $2
WHERE id = $1 AND status = 'stopped'
```

If zero rows are updated, another operation already owns the machine.

### C3. Tie async pollers to operation IDs

The VM poller should not update the machine blindly. It should only finalize the machine if its operation ID still matches the active start operation.

This avoids stale pollers overwriting newer state.

## D. Improve bin packing

### D1. Replace single-column ordering with a score

Current ordering:

- `ORDER BY used_memory_mb DESC`

Recommended scoring dimensions:

- heartbeat freshness
- free vCPU percentage
- free memory percentage
- machine count saturation
- free IP slots
- disk utilization / projected disk fit
- version compatibility
- warm spare policy per region

Example scheduling strategy:

1. filter to healthy, compatible hosts
2. reject hosts with insufficient CPU, memory, disk, or IP space
3. compute a score favoring dense packing while preserving some headroom
4. choose the best score

### D2. Include free IP slots in placement

The current IP pool is finite (`192.168.100.10-250`) but IP exhaustion is handled only after selecting a host. That means the scheduler can choose a host that is "capacity-eligible" but practically unusable.

Recommendation:

- track `ip_capacity` and `ip_used` on `hosts`, or
- compute free IP slots before final host choice

This should be part of host fitness, not an afterthought.

### D3. Include disk as a first-class resource

Machine placement tracks CPU and memory, but persistent data volumes consume host disk. Since stopped machines keep data on the host, disk becomes a long-lived placement constraint.

Recommendations:

- add host-level disk capacity and disk used metrics
- track reserved data volume footprint separately from active runtime footprint
- reject placements that would overcommit durable disk

This is especially important because data persistence outlives active compute placement.

### D4. Use headroom targets, not only dense packing

Pure dense packing minimizes active hosts, but hurts latency and resilience.

Add regional headroom policy:

- maintain at least 1 warm ready host per region
- reserve X% free capacity cluster-wide
- scale up before the last healthy host is saturated

This can coexist with dense packing.

## E. Add a proper reconciler layer

The control plane currently relies too much on request-time mutation and happy-path cleanup.

You need periodic repair loops.

### E1. Host reconciler

Runs every 30s:

- detect stale heartbeat
- mark host `unreachable`
- confirm instance existence in GCP
- mark `terminated` if gone
- emit host events

### E2. Capacity reconciler

Runs every few minutes:

- for healthy hosts, query agent `/vms`
- compare actual VMs to machine rows and host counters
- repair `used_vcpus`, `used_memory_mb`, `machine_count`

Use this for drift correction on reachable hosts.

### E3. Route reconciler

There is already an explicit gap here: the system does not have a background DB-to-KV reconciler.

Add one so route state is not dependent on access-time healing only.

Responsibilities:

- add missing routes for running machines
- remove stale routes for stopped/error/deleted machines
- repair host hostname changes

### E4. Resource orphan reconciler

Periodically detect and repair:

- machine rows pointing to missing hosts
- tunnels with no corresponding machine
- hosts with machine counters not matching actual assigned/running VMs
- dead placements stuck in `starting`

## F. Make provisioning a saga

Host provisioning and machine start are both multi-step workflows with external side effects. They should be treated as sagas with compensating actions.

### F1. Host provisioning saga

Recommended phases:

1. create host row
2. create host tunnel
3. create GCE instance
4. wait for agent ready
5. mark host ready

Recommended compensating actions:

- tunnel create succeeded, GCE create failed -> delete tunnel
- GCE create succeeded, health wait failed -> mark host error and optionally destroy instance
- partial cleanup failure -> leave host in `error` with actionable metadata

Do not rely on best-effort cleanup without durable phase tracking.

### F2. Machine start saga

Recommended phases:

1. acquire machine start lease
2. prepare auth/tunnel data
3. place machine
4. request agent create
5. wait for running
6. write routes
7. finalize state

Failure handling must be different for:

- pre-placement failures
- placement failures
- post-placement but pre-agent failures
- agent create accepted but VM boot failed
- route publication failures

Each case needs explicit rollback semantics.

## G. Correct the failure semantics

### G1. Stop/delete should not assume the agent action succeeded

If `StopVM` or `DestroyVM` fails because the host is unreachable, the system should not immediately behave as though the VM is gone.

Recommended behavior:

- if host is healthy and agent confirms stop/destroy, update placement/capacity
- if host is unreachable, transition machine to `error` or `recovering`, not `stopped`
- let the reconciler decide whether the host is dead or just partitioned

### G2. Distinguish "graceful stop" from "placement lost"

These are different outcomes:

- graceful stop: preserve host affinity intentionally
- placement lost: active runtime is gone, host may be dead

The system currently handles these too similarly.

### G3. Make recovery explicit

When a host is dead, recovery should be a first-class workflow:

- mark machine `error` with `host lost`
- optionally expose `recoverable = true`
- on user restart, run a recovery flow rather than a normal start flow

Future improvements can automate this for always-on machines.

## H. Add database invariants

The DB should reject obviously invalid accounting states.

Recommended constraints:

```sql
CHECK (used_vcpus >= 0)
CHECK (used_memory_mb >= 0)
CHECK (machine_count >= 0)
CHECK (used_vcpus <= capacity_vcpus)
CHECK (used_memory_mb <= capacity_memory_mb)
```

Recommended uniqueness/integrity:

- unique active `(active_host_id, active_vm_ip)` if active placement is split out
- state constraints on host status and machine status

These constraints will turn silent accounting corruption into fast failures.

## I. Suggested implementation phases

## Phase 1: Stop the worst failures

1. filter placement by fresh heartbeat
2. add `unreachable` and `terminated` host statuses
3. add host reconciler
4. mark machines on terminated hosts `error`
5. delete stale routes for those machines

This is the minimum safe fix for console-killed hosts.

## Phase 2: Stabilize lifecycle

1. introduce machine operation IDs / leases
2. rename `provisioning` to more explicit lifecycle states
3. make async pollers operation-aware
4. tighten rollback semantics

## Phase 3: Improve scheduling quality

1. add disk and IP-slot awareness
2. replace memory-only ordering with multi-factor scoring
3. add regional headroom policy
4. add scale-up/scale-down reconciler

## Phase 4: Split placement from persistence locality

1. add `active_host_id` / `active_vm_ip`
2. add `data_home_host_id`
3. migrate stop/restart logic to use the new model
4. prepare for future cross-host recovery/migration

## J. Recommended tests

These areas need explicit tests because they are the highest-risk operational paths.

### Unit tests

- stale host is excluded from placement
- host transitions `ready -> unreachable -> terminated`
- machine on terminated host becomes `error`
- repeated start requests cannot double-place the same machine
- restart failure does not destroy persistence affinity incorrectly
- stop/delete on unreachable host does not falsely mark machine stopped

### Integration tests

- kill GCE host externally, wait for reconciler, verify:
  - host becomes `terminated`
  - machines become `error`
  - scheduler stops using that host
- restart machine after host loss and verify recovery flow
- route reconciler removes routes for machines on dead hosts

### Operational tests

- simulate 3 missed heartbeats
- simulate transient network partition where host later recovers
- simulate external instance deletion with stale DB counters

## Proposed Near-Term Deliverable

If implementing incrementally, the best near-term deliverable is:

1. add heartbeat freshness to placement
2. add a control-plane host reconciler
3. add `unreachable` / `terminated` host states
4. mark machines on terminated hosts as `error`
5. remove those machines from KV routing

That will resolve the current stale-database problem when a host is killed in the cloud console without requiring the full lifecycle redesign on day one.

## K. Firecracker and Agent Lifecycle Recommendations

This section covers lower-level issues in the VM orchestration layer that materially affect provisioning reliability and crash recovery.

## K1. Distinguish controlled shutdown from crash recovery

The orchestrator intentionally starts Firecracker with a background-scoped context so VM lifecycle is not tied to the request that created it.

That is a reasonable design choice for:

- request cancellation
- normal agent shutdown
- controlled self-update flows

However, it creates a separate crash-recovery problem:

- if the agent crashes unexpectedly, Firecracker processes may still be running
- on restart, the orchestrator does not reattach
- instead, `loadState()` kills persisted PIDs during orphan cleanup

Recommendation:

- keep request-independent VM lifetime
- but implement explicit crash-recovery behavior instead of unconditional PID kill

The key point is that the problem is primarily ungraceful restart recovery, not the normal self-update path.

## K2. Replace PID-only orphan cleanup with verified graceful recovery

Current orphan cleanup is too blunt:

- it finds the saved PID
- it calls `proc.Kill()`
- it removes TAP/rootfs/socket resources
- it preserves the data volume

Risks:

- hard kill may corrupt in-guest state or filesystem activity in progress
- PID reuse could target the wrong process
- the system throws away a potentially recoverable running VM

Recommended recovery order:

1. verify the PID still belongs to the expected Firecracker process
2. verify socket path and machine identity still match
3. attempt graceful shutdown first
4. only fall back to `SIGKILL` after timeout
5. if reattach is practical, prefer reattach over forced termination

Minimum safe improvement:

- inspect `/proc/<pid>/cmdline`
- require the expected Firecracker binary and socket path
- use `SIGTERM` plus timeout before `SIGKILL`

Better long-term improvement:

- persist enough metadata to reattach or reconcile running VMs after crash restart

## K3. Serialize `RegisterPending` to `Create` handoff

The current create flow has a real concurrency hole:

1. agent API pre-registers the machine as `creating`
2. `Create()` sees the placeholder and deletes it
3. the real VM entry is only inserted later

That leaves a gap where a second create request for the same machine ID can race through and duplicate setup work.

Potential outcomes:

- TAP device collisions
- rootfs path collisions
- socket path collisions
- confusing status flaps between `creating`, `starting`, `error`, and `running`

Recommendation:

- replace placeholder deletion with an operation lease or per-machine lock
- keep the machine reserved for the entire create path
- reject concurrent create requests for the same machine ID deterministically

Implementation options:

- per-machine mutex in orchestrator
- operation ID carried from agent API into orchestrator
- compare-and-set state machine in the control plane plus a matching guard in the agent

## K4. Fix unsynchronized status mutation in the orchestrator

The orchestrator updates `vm.instance.Status` from background goroutines without holding the orchestrator mutex, while `Get()` and `List()` read the same struct under `RLock`.

That is a Go shared-memory race.

Risks:

- inconsistent reads
- nondeterministic state observation
- hard-to-reproduce flakiness under concurrency

Recommendation:

- all `vm.instance` mutations should happen under `o.mu`
- or store mutable runtime state in atomics / a dedicated synchronized structure

This applies to:

- `starting -> running`
- `starting -> error`
- browser companion VM status changes
- any future async health/progress transitions

## K5. Make boot-readiness semantics explicit

`waitForPort()` failure in the orchestrator is not fully silent, because it flips the VM status to `error`.

However, the current behavior still creates ambiguity:

- `Create()` itself returns success once Firecracker starts
- readiness is determined later in a goroutine
- downstream systems see eventual rather than atomic success/failure

Recommendation:

- define two separate concepts explicitly:
  - `booted`: Firecracker process started
  - `ready`: required in-guest services are reachable
- tie readiness finalization to the higher-level machine operation ID
- ensure the control plane and agent agree on which state transitions are terminal

This will make the UI and control-plane status model more predictable.

## K6. Add compensating cleanup for late provisioning failures

This document already recommends host-level reconciliation, but `ProvisionHost()` needs a tighter local failure strategy as well.

If host provisioning fails after GCE instance creation, the system should not stop at:

- host row marked `error`
- tunnel maybe cleaned up
- instance still running

Recommendation:

- wrap provisioning in a success-guarded cleanup path
- if provisioning fails after instance creation, automatically destroy the instance and data disk unless explicitly marked for forensic retention

Suggested behavior:

- early create failures: delete tunnel and DB state
- late boot/health failures: destroy instance, destroy orphan disk, mark host `error` with full context
- cleanup failure: emit a host event with explicit remediation details

## K7. Treat reflink fallback as a performance SLO issue

`cp --reflink=auto` is intentionally permissive. If reflinks are unavailable, VM creation still works, but the system silently degrades to a full file copy.

That is not a correctness bug, but it is operationally important because it directly affects:

- startup latency
- scratch disk throughput
- contention during bursts

Recommendation:

- detect reflink capability during agent startup
- emit a clear high-priority warning if the filesystem falls back to standard copy
- expose a metric or health detail for reflink availability

This should be treated as a startup-performance invariant, not just an implementation detail.

## K8. Improve metadata diagnostics

The metadata nonce path is reasonably well enforced in the backend, but diagnostics are weak from an operator perspective.

Recommendation:

- extend `ocm doctor` with a metadata-specific check
- make it clear when a machine cannot fetch metadata because of:
  - metadata server unreachable
  - nonce missing
  - nonce mismatch
  - config not registered for the VM IP

Useful checks:

- control-plane can fetch assembled config
- agent metadata server is reachable
- VM can authenticate to metadata endpoints
- config version reported by metadata matches expected state

This is especially important because metadata auth failures can present as generic startup breakage rather than an obvious auth problem.

## K9. Add resumability to onboarding and launch

The onboarding launch sequence is intentionally sequential:

1. push machine config
2. start machine

That means a partial success is expected when the first step succeeds and the second fails.

Recommendation:

- make the partial-success state explicit in the UI
- allow the launch step to be retried without redoing prior onboarding steps
- show a resume action when the machine exists and config has already been pushed

Useful UX states:

- account created
- machine created
- credentials linked
- identity saved
- config pushed
- machine start pending / failed

This is primarily a resumability and operator-clarity improvement, not a data consistency fix.

## K10. Additional tests for the Firecracker layer

Add tests for:

- crash recovery with a persisted VM whose PID no longer belongs to Firecracker
- crash recovery graceful shutdown path before forced kill
- concurrent `Create()` calls for the same machine ID
- background status transitions under `-race`
- reflink unavailable startup warning
- onboarding partial success and resume behavior

These tests should complement the scheduler/provisioner tests listed earlier in the document.

## L. Prioritized Checklist

This checklist is ordered by operational risk reduction, not by architectural elegance.

## P0. Stop unsafe scheduling and resource leaks first

- [ ] Exclude stale-heartbeat hosts from placement
- [ ] Add `unreachable` and `terminated` host statuses
- [ ] Add a control-plane host reconciler that marks stale hosts unschedulable
- [ ] Confirm externally deleted hosts against GCP before marking them `terminated`
- [ ] Mark machines on terminated hosts as `error` with a clear `host lost` message
- [ ] Remove KV routes for machines on terminated hosts
- [ ] Add compensating cleanup in `ProvisionHost()` for late failures after instance creation
- [ ] Prevent stop/delete flows from assuming agent success when the host is unreachable

Success criteria:

- no new placements occur on dead hosts
- console-killed hosts stop receiving work within one heartbeat timeout window
- failed host provisioning does not leave billable orphan instances behind

## P1. Make lifecycle transitions deterministic

- [ ] Add compare-and-set machine lifecycle transitions in the control plane
- [ ] Introduce machine operation IDs or leases for start/stop/delete
- [ ] Tie async VM pollers to the active operation ID
- [ ] Serialize orchestrator create operations per machine ID
- [ ] Fix unsynchronized async status mutation in the orchestrator
- [ ] Distinguish `booted` from `ready` in status reporting

Success criteria:

- repeated start requests cannot double-place a machine
- stale async goroutines cannot overwrite newer machine state
- UI and API show consistent transition semantics

## P2. Fix crash recovery and orphan handling

- [ ] Replace PID-only orphan cleanup with verified process identity checks
- [ ] Attempt graceful shutdown before forced kill during orphan cleanup
- [ ] Add tests for PID reuse / mismatched process scenarios
- [ ] Decide whether to reattach to running Firecracker VMs or always terminate them safely
- [ ] Improve metadata diagnostics for nonce and metadata-server failures

Success criteria:

- crash restart does not blindly kill arbitrary reused PIDs
- crash recovery either safely reattaches or safely terminates with explicit validation
- metadata-auth failures are diagnosable without log spelunking

## P3. Improve scheduling quality and observability

- [ ] Add disk-awareness to placement
- [ ] Add IP-slot-awareness to placement
- [ ] Replace memory-only host ordering with multi-factor scoring
- [ ] Add reflink capability detection and warning/metrics
- [ ] Add capacity reconciler for healthy hosts
- [ ] Add route reconciler for DB-to-KV consistency

Success criteria:

- scheduling decisions reflect actual durable resource constraints
- host choice quality improves under mixed workloads
- operational drift is automatically repaired

## P4. Perform the structural data-model cleanup

- [ ] Split active placement from persistence locality
- [ ] Add `active_host_id` / `active_vm_ip`
- [ ] Add `data_home_host_id`
- [ ] Migrate stop/restart/recovery flows to the new model
- [ ] Add DB constraints for nonnegative capacity and valid placement invariants

Success criteria:

- graceful stop and dead-host recovery are modeled differently
- restart affinity no longer depends on stale active placement columns
- database invariants prevent silent accounting corruption

## P5. Improve operator and user experience

- [ ] Add onboarding resume logic for partial launch success
- [ ] Add clearer machine error messages for host-loss and recovery states
- [ ] Extend `ocm doctor` with metadata and liveness checks
- [ ] Surface host freshness / heartbeat age more prominently in admin views

Success criteria:

- users can recover from partial onboarding failures without starting over
- operators can identify metadata and host-liveness issues quickly

## M. How To Proceed

The recommended implementation sequence is intentionally conservative. Do not start with schema redesign. First stop the system from making incorrect decisions under failure.

## Step 1. Add guardrails before changing behavior

Before shipping logic changes:

- add targeted tests for the current failure modes
- add structured logs for host reconciliation decisions
- add metrics for stale hosts, terminated hosts, and provisioning cleanup failures

Recommended first tests:

- stale host excluded from placement
- terminated host marks machines `error`
- `ProvisionHost()` late failure triggers cleanup
- repeated start requests cannot double-place one machine

Reason:

These are the highest-risk regressions and they define the safety envelope for all later refactors.

## Step 2. Ship the P0 safety fixes first

Implement these together in one coherent change set:

1. stale-heartbeat placement filter
2. host reconciler
3. `unreachable` / `terminated` host states
4. machine erroring and route cleanup on host termination
5. `ProvisionHost()` compensating cleanup

Reason:

This solves the current production risk of stale DB state after external host deletion and stops the most expensive leak class.

## Step 3. Stabilize machine lifecycle semantics

After host safety is in place:

1. add machine operation IDs / leases
2. add compare-and-set transitions
3. make async pollers operation-aware
4. serialize orchestrator create per machine ID

Reason:

This reduces race conditions and makes later architectural changes much easier because state transitions become explicit.

## Step 4. Fix crash-recovery correctness

Once lifecycle ownership is explicit:

1. improve orphan cleanup validation
2. add graceful shutdown before hard kill
3. decide on reattach vs terminate strategy
4. improve metadata diagnostics and `doctor`

Reason:

Crash-recovery improvements are easier and safer when there is already a clear ownership model for machine operations.

## Step 5. Upgrade scheduler quality

Only after correctness is under control:

1. add disk and IP-slot constraints
2. add multi-factor host scoring
3. add capacity and route reconcilers
4. add headroom policy per region

Reason:

There is little value in a smarter scheduler if the system still has stale state and lifecycle races.

## Step 6. Perform the schema split for placement vs persistence

Do this after the behavior is stable enough to migrate with confidence:

1. add new columns alongside existing `host_id` / `vm_ip`
2. dual-write during transition
3. migrate read paths
4. backfill data
5. remove overloaded old semantics

Reason:

This is the largest conceptual change. It should be done after the system is already safe, observable, and test-covered.

## N. Suggested Work Breakdown

If assigning work across a small team, the cleanest split is:

### Workstream A: Safety and reconciliation

- stale-heartbeat placement filter
- host reconciler
- host termination machine cleanup
- route cleanup on host loss
- provisioning compensating cleanup

### Workstream B: Lifecycle correctness

- machine operation IDs / leases
- CAS transitions
- async poller ownership
- create serialization
- stop/delete failure semantics

### Workstream C: Orchestrator hardening

- orphan cleanup validation
- graceful orphan termination
- race detector fixes in orchestrator status mutation
- reflink capability checks
- metadata diagnostics

### Workstream D: UX and operator tooling

- onboarding resume flow
- clearer launch-state messaging
- admin heartbeat visibility
- `ocm doctor` enhancements

## O. Suggested Release Strategy

### Release 1: Safety patch

Ship:

- stale-host filter
- host reconciler
- terminated host handling
- provisioning cleanup

Do not include schema changes in this release.

### Release 2: Lifecycle stabilization

Ship:

- operation IDs / leases
- CAS transitions
- serialized create path
- safer async finalization

### Release 3: Crash-recovery hardening

Ship:

- validated orphan cleanup
- graceful termination fallback
- metadata diagnostics
- reflink observability

### Release 4: Scheduler improvement and schema refactor

Ship:

- disk/IP-aware scheduling
- capacity/route reconciliation
- active placement vs persistence split

## P. Recommended First PRs

If starting immediately, the first three PRs should be:

### PR 1

- add stale-heartbeat filter to placement
- add tests for dead-host exclusion

### PR 2

- add host reconciler
- add `unreachable` / `terminated` statuses
- mark machines on terminated hosts `error`
- remove stale routes

### PR 3

- add `ProvisionHost()` compensating cleanup for late failures
- add tests ensuring no orphan instance remains after health-check failure

This sequence gives the highest risk reduction per unit of change.

## Q. Dedicated Host Provider Model

If OCM is moving toward providers like OVHcloud Dedicated, Hetzner Dedicated, and Vultr Bare Metal, the architecture should not add an `ovh` special case. The right model is a generic dedicated-host provider layer with multiple implementations.

### Q.1 Provider classes

Support two host-provider classes:

- `managed_baremetal_api`
- `registered_dedicated_server`

`managed_baremetal_api` means the control plane can create and destroy the host using the provider API.

Examples:

- Vultr Bare Metal
- OVHcloud products where the lifecycle is API-driven enough for automated provisioning

`registered_dedicated_server` means the host lifecycle is slower, more manual, or driven by an operator workflow. The control plane should treat it as inventory that enrolls itself.

Examples:

- OVHcloud Dedicated if provisioning is handled outside OCM
- Hetzner Dedicated Root Servers
- future colo / on-prem hosts

### Q.2 Generic provider interfaces

The control plane should split host handling into separate interfaces:

- `HostInventoryProvider`
- `HostProvisioningProvider`
- `HostBootstrapProvider`
- `HostTransportProvider`
- `HostReconciler`

Suggested responsibilities:

- `HostInventoryProvider`: enumerate hosts, provider IDs, locations, labels, and power state
- `HostProvisioningProvider`: create, destroy, reboot, reinstall, and optionally attach metadata or bootstrap data
- `HostBootstrapProvider`: render the bootstrap payload or enrollment package
- `HostTransportProvider`: tell the control plane how to reach the agent
- `HostReconciler`: confirm whether the host still exists and whether its control-plane state is stale

This is the architectural split that lets one control plane support:

- OVHcloud Dedicated
- Hetzner Dedicated
- Vultr Bare Metal
- future AWS/OCI/Scaleway bare metal

without rewriting the scheduler and agent lifecycle for each provider.

### Q.3 Host lifecycle modes

The `hosts` model should explicitly record lifecycle mode:

- `provisioned`
- `registered`
- `imported`

Meanings:

- `provisioned`: OCM created the host
- `registered`: operator created the host, agent enrolled using an enrollment token
- `imported`: existing host discovered and adopted into OCM inventory

This is necessary because a Hetzner or OVH dedicated server often behaves like durable inventory, not like an instantly replaceable VM.

### Q.4 Transport model

Do not tie agent reachability to `external_ip`.

Introduce a provider-neutral endpoint model such as:

- `agent_endpoint`
- `agent_endpoint_type`
- `proxy_endpoint`

Examples of endpoint types:

- `public_http`
- `private_http`
- `reverse_tunnel`
- `overlay_network`

This matters immediately for dedicated hosts because:

- some providers expose public IPv4 by default
- some make additional IPv4s expensive
- future hosts may sit behind private interconnects or tunnels

### Q.5 Bootstrap model

Do not depend on cloud instance metadata as the primary bootstrap path.

Support provider-neutral enrollment methods:

- cloud-init or user-data
- install script plus one-time enrollment token
- ISO / rescue-based install flow
- image-based install flow

GCP metadata can remain one bootstrap adapter, but it should not remain the required mechanism.

### Q.6 Scheduler inputs

The scheduler should not pick hosts by provider-specific fields like `source_image` or `machine_type`.

Instead, it should score on:

- CPU capacity
- memory capacity
- disk class and free space
- heartbeat freshness
- provider
- region / metro
- host image release
- KVM capability
- network characteristics
- operator labels

This allows later provider onboarding without rewriting the placement logic.

### Q.7 Storage assumptions

Dedicated-host providers require a storage model that does not assume cloud block devices.

Introduce storage traits on the host:

- `local_nvme`
- `local_ssd`
- `raid1_local`
- `provider_block`
- `ephemeral_boot`

This is especially important because OVH, Hetzner, and Vultr bare-metal products differ materially in:

- local disk topology
- swap/reinstall workflows
- ability to recover from disk failure

## R. Provider Reliability Assessment

### R.1 How to read these ratings

The table below is an engineering assessment, not a vendor-issued benchmark.

The rating combines:

- published SLA if one exists
- operational tooling
- remote recovery path
- network/private-network options
- lifecycle automation maturity
- suitability for running a Firecracker host fleet

Ratings use a `1-5` scale:

- `5.0`: strongest fit for reliable automation and fleet operations
- `4.0`: good provider with manageable operational gaps
- `3.0`: viable, but reliability depends more heavily on operator processes
- below `3.0`: avoid as a primary production provider for this use case

### R.2 Current provider matrix

As of 2026-03-08:

| Provider | Product family | Reliability rating | Summary |
|---|---|---:|---|
| OVHcloud US | Advance Dedicated | 4.2 / 5 | Strong dedicated-host fit with published SLA, KVM/IPMI path, private networking, and US availability |
| Vultr | Bare Metal | 4.1 / 5 | Strong automation and a published uptime guarantee, but bare-metal snapshot/recovery tooling is weaker |
| Hetzner | Dedicated Root Servers | 3.7 / 5 | Excellent price/performance and good rescue tooling, but less suitable as a US-first primary provider and more operator-driven in practice |

### R.3 Why OVHcloud scores highest for this path

OVHcloud Dedicated is the strongest match for the first dedicated-host rollout because:

- the US dedicated ranges publish SLAs from `99.95%` to `99.99%`
- the product pages explicitly include IPMI and KVM-on-IP access
- the platform exposes private-network options
- the product line behaves like long-lived dedicated infrastructure, which fits the OCM host model better than cloud VMs

Architecturally, OVHcloud is a good first provider for a generic dedicated-host path because:

- it can be used in `registered` mode first
- it does not force GCP-specific metadata assumptions
- it still leaves room to add a more API-driven provider later

### R.4 Why Vultr is close behind

Vultr Bare Metal is operationally attractive because:

- it publishes a `100%` uptime SLA
- it has a cleaner API-oriented model than traditional dedicated-server platforms
- it is still true bare metal, so Firecracker is a fit

It scores slightly lower for this use case because:

- official docs say bare metal does not support snapshots
- recovery and backup workflows therefore rely more on OCM-side replication and host reinstall automation
- pricing is materially higher than OVHcloud for the same kind of fleet density

### R.5 Why Hetzner is lower for this specific rollout

Hetzner Dedicated is still viable, but it should not be the first implementation for a US-first Firecracker host fleet.

Reasons:

- the dedicated-server tooling is good, especially rescue, installimage, failover IPs, and KVM access
- however, the dedicated-server footprint is still more Europe-centered operationally
- US dedicated availability is constrained compared with OVHcloud US and Vultr
- in practice it fits better as durable inventory than as a provider you expect to scale elastically on demand

The main positive architectural point is that a generic `registered_dedicated_server` flow would make later Hetzner onboarding straightforward.

### R.6 Recommended onboarding order

If the goal is to stay generic while supporting later Hetzner and Vultr onboarding, the recommended provider order is:

1. OVHcloud Dedicated in `registered` mode
2. Vultr Bare Metal in `managed_baremetal_api` mode
3. Hetzner Dedicated in `registered_dedicated_server` mode

This sequence validates both provider classes early:

- first a durable inventory model
- then an API-managed bare-metal model
- then a second durable inventory provider

### R.7 Design implication

If the architecture works for all three providers, then it is probably generic enough.

That means the implementation should be considered incomplete until it supports:

- operator-created host enrollment
- provider-aware reconciliation
- provider-neutral agent transport
- provider-neutral bootstrap
- host capability labels instead of cloud-specific fields

## S. Implementation Checklist

This section turns the dedicated-host architecture into an execution plan that remains generic across OVHcloud, Hetzner, Vultr, and self-owned servers.

### S.1 Phase 1: Decouple the current GCP path

Goal:

- preserve current behavior
- stop hard-coding provider assumptions into the control plane

Required changes:

1. Add provider fields to `hosts`

- `provider`
- `provider_class`
- `lifecycle_mode`
- `provider_host_id`
- `provider_region`
- `provider_zone`
- `provider_sku`
- `host_image_release`
- `agent_endpoint`
- `agent_endpoint_type`
- `proxy_endpoint`
- `provider_metadata`
- `capabilities`
- `labels`

2. Keep existing GCP fields during migration

- `vm_name`
- `vm_id`
- `zone`
- `region`
- `machine_type`
- `source_image`
- `external_ip`
- `internal_ip`

3. Introduce provider interfaces in the backend

- `HostInventoryProvider`
- `HostProvisioningProvider`
- `HostBootstrapProvider`
- `HostTransportProvider`
- `HostReconciler`

4. Wrap the existing GCP implementation behind those interfaces first

Exit criteria:

- GCP still works
- no host lifecycle code outside the provider layer depends directly on GCP SDK objects

### S.2 Phase 2: Add generic host enrollment

Goal:

- let operator-created hosts join OCM without cloud metadata

Required changes:

1. Add host enrollment tokens

- one-time enrollment token
- optional short-lived bootstrap token
- host identity rotation support

2. Add an agent registration API

- `POST /api/agent/register`
- validates enrollment token
- creates or binds the host record
- returns agent credentials and bootstrap config

3. Support multiple bootstrap methods

- environment file
- install script
- cloud-init
- rescue/install mode for dedicated servers

Exit criteria:

- a fresh Linux server can install the agent and register itself without GCP metadata

### S.3 Phase 3: Replace IP-based agent assumptions

Goal:

- remove the requirement that a host have a public external IP directly stored in the DB

Required changes:

1. Add endpoint-based agent addressing

- `agent_endpoint`
- `agent_endpoint_type`
- `proxy_endpoint`

2. Update all agent-control and proxy call sites

- agent client
- logs proxy
- terminal proxy
- gateway proxy
- file proxy

3. Update heartbeat payloads

- heartbeat should advertise endpoint information
- endpoint freshness should not depend only on `external_ip`

Exit criteria:

- OCM can manage a host through a provider-neutral endpoint model

### S.4 Phase 4: Add registered dedicated-host providers

Goal:

- support durable inventory providers without automated host creation

Required changes:

1. Add `registered_dedicated_server` provider implementations

- OVHcloud Dedicated
- Hetzner Dedicated
- later customer-owned / colocated servers

2. Implement provider-aware reconciliation

- host exists
- host reachable
- host power state known if available
- endpoint or networking changes surfaced to OCM

3. Add operator workflows

- register host
- put host in maintenance
- reinstall host
- replace failed disk or rebuild host

Exit criteria:

- OVHcloud Dedicated works without special-case control-plane logic

### S.5 Phase 5: Add managed bare-metal providers

Goal:

- support providers where host lifecycle can be automated

Required changes:

1. Add `managed_baremetal_api` implementations

- Vultr Bare Metal
- later AWS bare metal / OCI / Scaleway Elastic Metal

2. Reuse the same bootstrap, transport, and reconciliation layers

3. Keep the scheduler provider-neutral

Exit criteria:

- a new provider implementation only fills in provider interfaces
- the rest of the control plane remains unchanged

### S.6 Concrete file-level checklist

#### Database and store layer

- add new host columns and migrations
- extend [`backend/internal/store/store.go`](/Users/mantiz/openclawmachines/backend/internal/store/store.go)
- update [`backend/internal/store/postgres.go`](/Users/mantiz/openclawmachines/backend/internal/store/postgres.go)
- add queries for host registration, endpoint updates, provider metadata, and capability labels

#### Config and server wiring

- split provider config from GCP-specific config in [`backend/internal/config/config.go`](/Users/mantiz/openclawmachines/backend/internal/config/config.go)
- replace direct `scheduler.New(db, cfg.GCPRegion, cfg.SnapshotName)` assumptions in [`backend/cmd/server/main.go`](/Users/mantiz/openclawmachines/backend/cmd/server/main.go#L67)
- replace direct provisioner construction with provider registry wiring in [`backend/cmd/server/main.go`](/Users/mantiz/openclawmachines/backend/cmd/server/main.go#L74)

#### Provisioning and provider layer

- split [`backend/internal/provisioner/provisioner.go`](/Users/mantiz/openclawmachines/backend/internal/provisioner/provisioner.go) into generic orchestration plus provider drivers
- keep GCP as the first provider driver
- add a `registered host` driver that does not create hardware

#### Agent bootstrap and enrollment

- replace `prefetchGCPMetadata` in [`backend/cmd/agent/main.go`](/Users/mantiz/openclawmachines/backend/cmd/agent/main.go#L400) with a generic bootstrap source chain
- add agent registration flow in the control-plane API
- allow the agent to start from env/bootstrap bundle without metadata service access

#### Agent transport

- replace `ExternalIP` / `InternalIP` addressing in [`backend/internal/agentclient/client.go`](/Users/mantiz/openclawmachines/backend/internal/agentclient/client.go#L439)
- replace host-IP assumptions in:
  - [`backend/internal/api/machine_terminal.go`](/Users/mantiz/openclawmachines/backend/internal/api/machine_terminal.go#L46)
  - [`backend/internal/api/machine_logs.go`](/Users/mantiz/openclawmachines/backend/internal/api/machine_logs.go#L38)
  - [`backend/internal/api/machine_gateway.go`](/Users/mantiz/openclawmachines/backend/internal/api/machine_gateway.go#L89)
  - [`backend/internal/api/machine_files.go`](/Users/mantiz/openclawmachines/backend/internal/api/machine_files.go#L172)

#### Scheduler

- replace `region + expectedImage` placement assumptions in [`backend/internal/scheduler/scheduler.go`](/Users/mantiz/openclawmachines/backend/internal/scheduler/scheduler.go#L12)
- move placement filtering to provider-neutral capabilities and image-release compatibility

#### Admin and operator APIs

- split `POST /hosts` into:
  - `provision host`
  - `register host`
  - `import host`
- update admin host UI and API messaging to distinguish managed and registered hosts

### S.7 Recommended first PRs for the provider refactor

1. Schema and store changes for generic host fields
2. Provider interface extraction with the current GCP implementation behind it
3. Agent endpoint abstraction
4. Host enrollment API
5. OVHcloud Dedicated registered-host implementation

## T. Self-Owned or Colocated Servers

If you buy your own server and wire it into the cloud, that should be treated as a first-class provider type, not an exception.

### T.1 Recommended provider model

Use:

- `provider = customer_owned`
- `provider_class = registered_dedicated_server`
- `lifecycle_mode = registered` or `imported`

This keeps the architecture generic and lets the same host-enrollment path support:

- OVHcloud Dedicated
- Hetzner Dedicated
- customer-owned colo servers
- on-prem racks with routed connectivity

### T.2 What “wired into the cloud” should mean architecturally

This should not mean “pretend it is a cloud VM.”

It should mean:

- OCM can reach the agent through a stable endpoint
- the server can reach the control plane and required object storage
- the server advertises its capabilities and health
- the control plane can mark it unavailable and drain it safely

Connectivity options:

- public IPv4 with firewall allowlist
- Cloudflare Tunnel
- Tailscale or WireGuard overlay
- site-to-site VPN or private interconnect

The important design point is that transport becomes an endpoint capability, not a provider assumption.

### T.3 Reliability characteristics of self-owned servers

Customer-owned or colocated hardware can be excellent for Firecracker if you control the hardware and networking well.

But reliability depends much more on your operations than on a provider SLA.

Suggested engineering rating:

- well-managed colo / leased rack with remote hands and redundant power: `4.0 / 5`
- office or lab server with consumer connectivity: `2.5 / 5`

This is why self-owned hardware should share the `registered_dedicated_server` path:

- lifecycle is inventory-driven
- recovery is operator-driven
- OCM should not assume instant replacement

### T.4 Additional capabilities needed for self-owned servers

Add host capability flags such as:

- `remote_console`
- `remote_power_control`
- `redundant_power`
- `raid_rebuild_supported`
- `private_backhaul`
- `dedicated_uplink`
- `local_backup_target`

These are more operationally meaningful than cloud-specific fields.

### T.5 Operational requirements before production use

A self-owned Firecracker host should not be accepted into production unless it has:

1. remote console access
2. remote power-cycle path
3. documented reinstall flow
4. monitored disk health
5. monitored temperature and hardware alerts
6. stable endpoint connectivity to the control plane
7. backup or replication plan for persistent machine data

### T.6 Recommendation

Architecturally, self-owned servers are fully compatible with the provider model proposed here.

In fact, if the design handles customer-owned servers cleanly, it will usually also handle OVHcloud and Hetzner dedicated servers cleanly.

That makes `customer_owned` a useful test of whether the provider abstraction is genuinely generic.

## U. Test-Driven Delivery Policy

The provider refactor should be implemented with a strict test-driven flow.

The rule is:

1. write the test first
2. run the test and verify it fails for the expected reason
3. implement the smallest change that makes it pass
4. rerun the targeted tests
5. rerun the broader package tests
6. only then move to the next step

This matters because the provider work cuts across:

- schema
- API shape
- agent bootstrap
- agent transport
- scheduler behavior
- host reconciliation

Without tests first, it is too easy to preserve the GCP path accidentally while leaving the generic path broken.

### U.1 Required workflow per PR

Every PR in the provider migration should follow this sequence:

1. add or update the tests
2. run only the new tests and capture the expected failure
3. implement the minimal production change
4. rerun the focused test package
5. rerun neighboring package tests
6. document any remaining integration gaps

### U.2 Test categories

The minimum required categories are:

- migration tests
- store tests
- API tests
- agent bootstrap tests
- transport resolution tests
- scheduler tests
- provider-driver tests

### U.3 Tests to add first

#### PR 1: Generic host schema and store behavior

Add tests first:

- `backend/internal/store/postgres_host_registration_test.go`
- `backend/internal/store/postgres_host_endpoint_test.go`
- `backend/internal/store/postgres_host_capabilities_test.go`

Test cases:

- create host with generic provider fields
- update host endpoint without changing legacy IP fields
- list hosts with provider metadata
- preserve legacy GCP fields during migration
- reject invalid provider class / lifecycle mode combinations

Expected first failure:

- schema columns and store scan code do not exist yet

#### PR 2: Provider registry and GCP extraction

Add tests first:

- `backend/internal/provider/registry_test.go`
- `backend/internal/provider/gcp_driver_test.go`

Test cases:

- provider registry resolves named provider
- unknown provider is rejected
- host orchestration uses provider interface instead of direct GCP code
- GCP driver returns provider-neutral host facts

Expected first failure:

- provider package and interfaces do not exist yet

#### PR 3: Agent endpoint abstraction

Add tests first:

- `backend/internal/agentclient/endpoint_test.go`
- `backend/internal/api/host_proxy_endpoint_test.go`

Test cases:

- prefer `agent_endpoint` when present
- fall back to legacy IPs during migration
- reject missing endpoint for registered hosts
- terminal/logs/gateway/files proxy all use the same endpoint resolver

Expected first failure:

- current code hardcodes `ExternalIP` / `InternalIP`

#### PR 4: Generic host enrollment API

Add tests first:

- `backend/internal/api/agent_registration_test.go`
- `backend/internal/api/host_enrollment_test.go`

Test cases:

- create enrollment token
- register host with valid enrollment token
- reject reused or expired token
- bind registered agent to an existing host record
- return bootstrap response with agent credentials and host identity

Expected first failure:

- registration route and enrollment model do not exist yet

#### PR 5: OVHcloud registered-host implementation

Add tests first:

- `backend/internal/provider/ovh_registered_driver_test.go`
- `backend/internal/api/ovh_host_registration_test.go`

Test cases:

- register an OVH host as `registered_dedicated_server`
- store provider metadata and hardware capabilities
- reconcile heartbeat-only liveness for OVH registered hosts
- mark host `unreachable` when heartbeat is stale

Expected first failure:

- OVH provider driver and registration workflow do not exist yet

### U.4 Suggested verification commands

Commands should be run in this order:

1. targeted test file or package
2. immediate package
3. related integration package

Examples:

```bash
go test ./backend/internal/store -run TestPostgresHostRegistration
go test ./backend/internal/provider -run TestProviderRegistry
go test ./backend/internal/agentclient -run TestAgentEndpointResolution
go test ./backend/internal/api -run TestAgentRegisterHost
```

Then broader verification:

```bash
go test ./backend/internal/store/...
go test ./backend/internal/api/...
go test ./backend/internal/agentclient/...
go test ./backend/internal/provider/...
```

### U.5 Merge criteria

Do not merge a provider-refactor PR unless:

- the new focused tests were written first
- the expected failure was verified before implementation
- legacy GCP behavior still passes
- the new generic path passes
- transport and enrollment regressions are covered

## V. Target Host Schema and Provider Interfaces

This is the target shape for the provider-neutral host model.

### V.1 Target host schema

Do not delete legacy GCP fields in the first migration. Add generic fields first, backfill, then remove old assumptions later.

Suggested new columns on `hosts`:

```sql
ALTER TABLE hosts
ADD COLUMN provider TEXT,
ADD COLUMN provider_class TEXT,
ADD COLUMN lifecycle_mode TEXT,
ADD COLUMN provider_host_id TEXT,
ADD COLUMN provider_region TEXT,
ADD COLUMN provider_zone TEXT,
ADD COLUMN provider_sku TEXT,
ADD COLUMN host_image_release TEXT,
ADD COLUMN agent_endpoint TEXT,
ADD COLUMN agent_endpoint_type TEXT,
ADD COLUMN proxy_endpoint TEXT,
ADD COLUMN provider_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
ADD COLUMN capabilities JSONB NOT NULL DEFAULT '{}'::jsonb,
ADD COLUMN labels JSONB NOT NULL DEFAULT '{}'::jsonb,
ADD COLUMN enrolled_at TIMESTAMPTZ,
ADD COLUMN last_reconciled_at TIMESTAMPTZ,
ADD COLUMN registration_source TEXT;
```

Suggested constraints:

```sql
ALTER TABLE hosts
ADD CONSTRAINT hosts_provider_class_check
CHECK (provider_class IN ('managed_baremetal_api', 'registered_dedicated_server', 'managed_vm_cloud')),
ADD CONSTRAINT hosts_lifecycle_mode_check
CHECK (lifecycle_mode IN ('provisioned', 'registered', 'imported')),
ADD CONSTRAINT hosts_agent_endpoint_type_check
CHECK (agent_endpoint_type IN ('public_http', 'private_http', 'reverse_tunnel', 'overlay_network'));
```

Suggested indexes:

```sql
CREATE INDEX hosts_provider_idx ON hosts(provider);
CREATE INDEX hosts_provider_class_idx ON hosts(provider_class);
CREATE INDEX hosts_lifecycle_mode_idx ON hosts(lifecycle_mode);
CREATE INDEX hosts_agent_endpoint_idx ON hosts(agent_endpoint);
```

### V.2 Capability model

`capabilities` should describe what the scheduler and operator actually care about.

Suggested fields:

```json
{
  "kvm": true,
  "arch": "x86_64",
  "cpu_threads": 12,
  "memory_mb": 65536,
  "storage_class": "raid1_local",
  "remote_console": true,
  "remote_power_control": true,
  "private_backhaul": true,
  "nested_virtualization": false
}
```

### V.3 Label model

`labels` should carry placement and operational preferences.

Suggested fields:

```json
{
  "provider": "ovhcloud",
  "metro": "vin",
  "country": "us",
  "fleet": "primary",
  "hardware_gen": "advance-1-2024",
  "cost_tier": "budget"
}
```

### V.4 Provider interfaces

Suggested provider package layout:

- `backend/internal/provider/interfaces.go`
- `backend/internal/provider/registry.go`
- `backend/internal/provider/gcp/driver.go`
- `backend/internal/provider/ovh/registered.go`
- `backend/internal/provider/vultr/driver.go`

Suggested interfaces:

```go
package provider

import "context"

type HostFacts struct {
    Provider          string
    ProviderClass     string
    ProviderHostID    string
    ProviderRegion    string
    ProviderZone      string
    ProviderSKU       string
    HostImageRelease  string
    AgentEndpoint     string
    AgentEndpointType string
    ProxyEndpoint     string
    Metadata          map[string]any
    Capabilities      map[string]any
    Labels            map[string]string
}

type InventoryProvider interface {
    Name() string
    GetHost(ctx context.Context, providerHostID string) (*HostFacts, error)
    ListHosts(ctx context.Context) ([]*HostFacts, error)
}

type ProvisioningProvider interface {
    Name() string
    CreateHost(ctx context.Context, req CreateHostRequest) (*HostFacts, error)
    DestroyHost(ctx context.Context, providerHostID string) error
    RebootHost(ctx context.Context, providerHostID string) error
}

type BootstrapProvider interface {
    Name() string
    RenderBootstrap(ctx context.Context, req BootstrapRequest) (*BootstrapBundle, error)
}

type TransportProvider interface {
    ResolveAgentEndpoint(ctx context.Context, host *store.Host) (string, error)
    ResolveProxyEndpoint(ctx context.Context, host *store.Host) (string, error)
}

type Reconciler interface {
    Name() string
    ReconcileHost(ctx context.Context, host *store.Host) (*ReconcileResult, error)
}
```

### V.5 Control-plane services to add

Add service-level abstractions above the raw provider interfaces:

- `HostEnrollmentService`
- `HostEndpointResolver`
- `HostReconciliationService`
- `HostProvisioningService`

This prevents API handlers from becoming provider-specific.

## W. First OVH Registered-Host Enrollment Flow

This is the first concrete implementation target. It should be built generically enough that the same flow later supports Hetzner and customer-owned servers.

### W.1 Chosen model

Use:

- `provider = ovhcloud`
- `provider_class = registered_dedicated_server`
- `lifecycle_mode = registered`

This means:

- the operator acquires the server outside OCM
- OCM does not create the hardware
- the server enrolls into OCM with a one-time token

### W.2 Enrollment sequence

1. operator creates a host enrollment record in the control plane
2. control plane returns:
   - enrollment token
   - install script URL or bootstrap bundle
   - expected provider and host labels
3. operator boots the OVH server and installs Linux with KVM enabled
4. operator installs the OCM agent using the bootstrap bundle
5. agent calls `POST /api/agent/register`
6. control plane validates the token and creates or binds the host record
7. control plane returns:
   - host ID
   - agent auth token
   - endpoint config
   - object-store manifest config
8. agent begins heartbeat and advertises endpoint + capabilities
9. reconciler marks host `ready` once registration and health checks pass

### W.3 Suggested API shape

Operator API:

```http
POST /api/admin/hosts/enrollment-tokens
POST /api/admin/hosts/register
POST /api/admin/hosts/import
```

Agent API:

```http
POST /api/agent/register
POST /api/agent/heartbeat
```

Suggested registration request:

```json
{
  "enrollment_token": "tok_...",
  "provider": "ovhcloud",
  "provider_host_id": "ns123456.ip-203-0-113.net",
  "provider_sku": "advance-1-2024",
  "agent_endpoint": "https://203.0.113.10:9090",
  "agent_endpoint_type": "public_http",
  "proxy_endpoint": "https://203.0.113.10:9091",
  "capabilities": {
    "kvm": true,
    "arch": "x86_64",
    "cpu_threads": 12,
    "memory_mb": 65536,
    "storage_class": "raid1_local",
    "remote_console": true
  },
  "labels": {
    "provider": "ovhcloud",
    "fleet": "primary",
    "hardware_gen": "advance-1-2024"
  }
}
```

### W.4 Failure handling

The registration flow should fail closed.

Reject registration if:

- token is expired
- token is reused
- provider does not match the enrollment token
- required capabilities are missing
- endpoint is malformed
- the host attempts to overwrite another registered host identity

### W.5 Tests to write before implementing OVH registration

Write these tests first:

- `TestCreateEnrollmentToken`
- `TestRegisterHostRejectsExpiredToken`
- `TestRegisterHostRejectsReusedToken`
- `TestRegisterHostCreatesOVHRegisteredHost`
- `TestRegisterHostRejectsProviderMismatch`
- `TestRegisterHostStoresEndpointAndCapabilities`
- `TestHeartbeatUsesRegisteredHostEndpoint`
- `TestStaleRegisteredHostBecomesUnreachable`

### W.6 Minimum viable OVH rollout

The first OVH implementation should deliberately avoid too much provider-specific automation.

Ship:

1. enrollment token creation
2. generic agent registration endpoint
3. registered-host persistence
4. endpoint-based control-plane transport
5. heartbeat-based liveness
6. admin UI to register, drain, and disable host

Do not block the first rollout on:

- automated OS reinstall
- hardware inventory synchronization
- remote power API integration
- automated private-network provisioning

### W.7 Why this flow stays generic

The same registration flow can later support:

- Hetzner Dedicated
- customer-owned colocated hosts
- imported legacy servers

Only the provider metadata, labels, and optional reconciliation logic change.

## X. Concrete PR Plan

This is the implementation order recommended for the provider migration.

Every PR below must follow the `tests first -> verify failure -> implement -> rerun` workflow defined in section `U`.

### X.1 PR 1: Generic host schema

Scope:

- add generic host columns
- extend store models and scans
- keep legacy GCP fields

Tests first:

- `postgres_host_registration_test.go`
- `postgres_host_endpoint_test.go`
- `postgres_host_capabilities_test.go`

Implementation after failures are verified:

- migration
- store model updates
- create/list/get/update host queries

### X.2 PR 2: Provider interfaces and registry

Scope:

- add provider interfaces
- add provider registry
- wrap current GCP implementation

Tests first:

- `registry_test.go`
- `gcp_driver_test.go`

Implementation after failures are verified:

- new provider package
- GCP driver adapter
- server wiring update

### X.3 PR 3: Endpoint-based transport

Scope:

- add endpoint resolver
- replace IP-specific transport assumptions

Tests first:

- `endpoint_test.go`
- `host_proxy_endpoint_test.go`

Implementation after failures are verified:

- update agent client
- update terminal, logs, gateway, files proxy handlers
- keep legacy IP fallback temporarily

### X.4 PR 4: Host enrollment API

Scope:

- create enrollment tokens
- add agent registration endpoint
- bind registered hosts

Tests first:

- `agent_registration_test.go`
- `host_enrollment_test.go`

Implementation after failures are verified:

- enrollment token storage
- registration handler
- host binding logic
- response payload for bootstrap completion

### X.5 PR 5: OVH registered-host rollout

Scope:

- first provider implementation for dedicated hosts
- heartbeat and reconciliation for registered OVH hosts

Tests first:

- `ovh_registered_driver_test.go`
- `ovh_host_registration_test.go`

Implementation after failures are verified:

- OVH registered provider metadata support
- admin workflows
- stale-host reconciliation for registered hosts

### X.6 PR 6: Scheduler modernization

Scope:

- move from `region + expectedImage` to provider-neutral capabilities

Tests first:

- `provider_placement_test.go`
- `host_capability_filter_test.go`

Implementation after failures are verified:

- scheduler input model update
- store query updates
- image-release compatibility filter

### X.7 PR 7: Generic dedicated-host expansion

Scope:

- add Hetzner registered-host implementation
- add `customer_owned` implementation

Tests first:

- `hetzner_registered_driver_test.go`
- `customer_owned_driver_test.go`

Implementation after failures are verified:

- provider metadata mapping
- operator flows for imported and customer-owned hosts

### X.8 Exit criteria

The provider migration is complete enough to proceed with production rollout when:

1. GCP still works through the provider registry
2. OVH registered-host enrollment works end-to-end
3. all agent RPC paths use endpoint resolution
4. scheduler no longer depends on GCP-specific placement assumptions
5. the same registration path can onboard `ovhcloud`, `hetzner`, and `customer_owned`

## Y. Admin Control Panel Architecture

The current admin surface is useful but too narrow for a mixed-provider host fleet.

Current backend admin routes are limited to:

- list hosts
- list machines on a host
- fetch host logs
- fetch VM stats
- provision host
- destroy host
- refresh rootfs
- trigger update

See:

- [`backend/internal/api/server.go`](/Users/mantiz/openclawmachines/backend/internal/api/server.go#L245)
- [`frontend/src/pages/admin/AdminHosts.tsx`](/Users/mantiz/openclawmachines/frontend/src/pages/admin/AdminHosts.tsx)
- [`frontend/src/lib/api.ts`](/Users/mantiz/openclawmachines/frontend/src/lib/api.ts#L371)

That is enough for a GCP-only environment, but not enough for:

- registered dedicated hosts
- multiple provider classes
- enrollment workflows
- maintenance workflows
- fleet health operations
- host reliability and recovery management

### Y.1 Admin panel goals

The admin control panel should become the operational console for the host fleet.

It should support:

1. inventory management
2. health and capacity monitoring
3. lifecycle actions
4. enrollment and import workflows
5. reconciliation and recovery workflows
6. auditability and operator safety

### Y.2 Core views

The minimum recommended admin information architecture is:

1. `Fleet Overview`
2. `Hosts`
3. `Host Detail`
4. `Enrollment`
5. `Provider Health`
6. `Operations Log`
7. `Alerts`

### Y.3 Fleet Overview

This should be the landing page for admins.

Key panels:

- total hosts by status
- total machines by status
- total allocatable CPU and memory
- used versus free capacity
- hosts by provider
- hosts with stale heartbeat
- hosts in maintenance
- hosts requiring agent or rootfs update
- hosts with active alerts

Primary actions:

- register host
- provision host
- import host
- drain host
- reconcile host

### Y.4 Hosts list view

The current hosts page should evolve into a filterable fleet table.

Required columns:

- host name
- provider
- provider class
- lifecycle mode
- provider SKU
- region / metro
- status
- last heartbeat
- agent endpoint type
- capacity CPU
- capacity memory
- machine count
- health score
- agent version
- host image release
- active alerts

Required filters:

- provider
- provider class
- lifecycle mode
- status
- region / metro
- needs update
- stale heartbeat
- has alert
- maintenance mode
- free capacity threshold

Required bulk actions:

- mark maintenance
- drain
- disable scheduling
- enable scheduling
- trigger reconcile
- export inventory

### Y.5 Host detail view

Each host needs a real operational detail page, not just a card with logs.

Sections:

1. `Summary`
2. `Capacity`
3. `Machines`
4. `Networking`
5. `Storage`
6. `Versions`
7. `Health`
8. `Provider Metadata`
9. `Events`
10. `Operator Actions`

The host detail page should show:

- provider and provider host ID
- enrollment source
- scheduling state
- maintenance state
- agent endpoint
- proxy endpoint
- external and internal addressing if present
- local storage traits
- capability flags
- recent heartbeats
- reconciliation result
- machine placement list
- update status
- audit/event history

### Y.6 Enrollment view

Registered dedicated hosts need a dedicated enrollment workflow in the admin UI.

This view should support:

- create enrollment token
- select provider
- select provider class
- attach labels
- attach expected capabilities
- generate install instructions
- show token expiration
- revoke unused token
- view pending enrollments
- retry failed enrollment

This is especially important for:

- OVHcloud Dedicated
- Hetzner Dedicated
- customer-owned servers

### Y.7 Provider health view

This view should summarize provider-specific fleet state without leaking provider-specific logic into the rest of the UI.

Examples:

- OVH registered hosts with stale heartbeat
- Vultr bare-metal hosts not matching OCM inventory
- GCP hosts with provider state drift
- customer-owned hosts missing endpoint connectivity

The UI should render provider-specific facts, but actions should still map to generic workflows:

- reconcile
- drain
- disable
- re-enroll
- decommission

### Y.8 Operations log

The admin panel should expose a first-class operations log.

This should include:

- host created
- host registered
- host imported
- host drained
- host disabled
- host reconciled
- heartbeat missed
- endpoint changed
- machine placement failed
- provider reconciliation failed
- rootfs refresh triggered
- agent update triggered

This log should be filterable by:

- host
- provider
- event type
- operator
- time range

### Y.9 Alerts

The admin panel should promote alerts from hidden logs to explicit operator state.

Minimum alerts:

- stale heartbeat
- host unreachable
- host terminated
- endpoint mismatch
- capacity accounting anomaly
- host overcommitted
- machine placement failures
- outdated agent version
- outdated host image release
- disk pressure
- repeated agent request failures

### Y.10 Safety rails

The admin panel must prevent destructive operator mistakes.

Required safeguards:

- confirm dangerous actions
- show blast radius before drain or destroy
- disable destroy for hosts with running machines unless explicitly forced
- require reason when entering maintenance mode
- support dry-run reconcile where possible
- record actor and timestamp for each admin action

### Y.11 RBAC

The current `superuser only` model is too coarse for a real fleet console.

Recommended roles:

- `fleet_viewer`
- `fleet_operator`
- `fleet_admin`
- `platform_admin`

Suggested permissions:

- viewers can inspect fleet state
- operators can drain, reconcile, and manage enrollment
- fleet admins can import or decommission hosts
- platform admins can perform destructive actions and provider config changes

### Y.12 API expansion needed

Add generic admin endpoints such as:

```http
GET    /api/admin/hosts
GET    /api/admin/hosts/{hostId}
POST   /api/admin/hosts/provision
POST   /api/admin/hosts/register
POST   /api/admin/hosts/import
POST   /api/admin/hosts/enrollment-tokens
POST   /api/admin/hosts/{hostId}/drain
POST   /api/admin/hosts/{hostId}/maintenance
POST   /api/admin/hosts/{hostId}/reconcile
POST   /api/admin/hosts/{hostId}/disable-scheduling
POST   /api/admin/hosts/{hostId}/enable-scheduling
GET    /api/admin/hosts/{hostId}/events
GET    /api/admin/hosts/{hostId}/health
GET    /api/admin/alerts
GET    /api/admin/operations
```

The old host routes can be preserved temporarily, but the long-term surface should map cleanly to generic fleet operations.

### Y.13 Frontend rollout plan

Recommended rollout:

1. extend the existing `AdminHosts` page into a real fleet table
2. add `HostDetail` route and detail page
3. add `Enrollment` route and token workflow
4. add `Operations` route
5. add `Alerts` route

Suggested frontend files:

- `frontend/src/pages/admin/AdminHosts.tsx`
- `frontend/src/pages/admin/AdminHostDetail.tsx`
- `frontend/src/pages/admin/AdminEnrollment.tsx`
- `frontend/src/pages/admin/AdminOperations.tsx`
- `frontend/src/pages/admin/AdminAlerts.tsx`

### Y.14 Test-driven rollout for the admin panel

The admin panel should follow the same test-first rule defined in section `U`.

Write tests first for:

- new admin API routes
- host detail payload shape
- enrollment token lifecycle
- alert generation
- bulk host actions
- RBAC restrictions

Suggested backend test files:

- `backend/internal/api/admin_hosts_test.go`
- `backend/internal/api/admin_host_detail_test.go`
- `backend/internal/api/admin_enrollment_test.go`
- `backend/internal/api/admin_alerts_test.go`
- `backend/internal/api/admin_operations_test.go`

Suggested frontend test files:

- `frontend/src/pages/admin/AdminHosts.test.tsx`
- `frontend/src/pages/admin/AdminHostDetail.test.tsx`
- `frontend/src/pages/admin/AdminEnrollment.test.tsx`
- `frontend/src/pages/admin/AdminAlerts.test.tsx`

### Y.15 Why this stays generic

If the admin control panel is designed around:

- inventory
- health
- capabilities
- lifecycle mode
- endpoint model
- reconciliation

then it will work for:

- GCP
- OVHcloud Dedicated
- Hetzner Dedicated
- Vultr Bare Metal
- customer-owned servers

without building a provider-specific admin experience for each one.

## Z. Admin Panel PR Sequence

The admin panel should be delivered in parallel with the provider migration, but still in a test-driven sequence.

### Z.1 PR A: Host detail API

Tests first:

- `admin_host_detail_test.go`

Then implement:

- `GET /api/admin/hosts/{hostId}`
- host detail payload
- provider metadata and capability exposure

### Z.2 PR B: Enrollment admin APIs

Tests first:

- `admin_enrollment_test.go`

Then implement:

- enrollment token CRUD
- pending enrollment list
- registration status view

### Z.3 PR C: Host lifecycle actions

Tests first:

- `admin_hosts_test.go`

Then implement:

- drain
- maintenance
- scheduling enable/disable
- reconcile

### Z.4 PR D: Operations log and alerts

Tests first:

- `admin_operations_test.go`
- `admin_alerts_test.go`

Then implement:

- operation/event feed
- alert materialization
- alert listing and filtering

### Z.5 PR E: Frontend fleet table and detail page

Tests first:

- `AdminHosts.test.tsx`
- `AdminHostDetail.test.tsx`

Then implement:

- fleet table
- host detail route
- action controls

### Z.6 PR F: Enrollment and alerts UI

Tests first:

- `AdminEnrollment.test.tsx`
- `AdminAlerts.test.tsx`

Then implement:

- enrollment workflow UI
- alerts dashboard
- operations feed view

## AA. Hybrid Capacity Strategy: OVH Base, GCP Burst

The target operating model should not treat OVH and GCP as interchangeable pools. They have different cost, performance, failure, and lifecycle characteristics.

The recommended shape is:

- `OVHcloud Dedicated` or other bare metal providers for base capacity
- `GCP nested virtualization` hosts for burst capacity

That strategy is operationally sound for Firecracker because:

- bare metal is the better steady-state home for KVM and Firecracker
- GCP nested virtualization can absorb spikes and recovery events
- GCP should not be treated as the primary performance tier for Firecracker-heavy workloads

### AA.1 Goals

The hybrid model should achieve four things:

- keep steady-state costs low by filling dedicated hosts first
- maintain fast overflow capacity without overbuying base hardware
- preserve predictable performance for sticky and performance-sensitive machines
- allow controlled recovery when a base host fails

### AA.2 Capacity tiers

Add explicit capacity tiers to the host model:

- `base`
- `burst`
- `spot_burst`

Recommended initial mapping:

- OVHcloud Dedicated: `capacity_tier = base`
- GCP on-demand nested virtualization hosts: `capacity_tier = burst`
- GCP Spot nested virtualization hosts: `capacity_tier = spot_burst`

The scheduler must treat these tiers as policy inputs, not just labels for display.

### AA.3 Performance classes

Add an explicit performance classification to each host:

- `bare_metal`
- `nested_virt`

This is important because capacity alone is not enough. A `nested_virt` host with nominally similar CPU and RAM should not automatically be scored equal to `bare_metal` for Firecracker placement.

### AA.4 Machine portability model

Hybrid capacity only works if the control plane knows which machines are portable.

Each machine should expose placement traits such as:

- `burst_eligible`
- `portable`
- `storage_mode`
- `performance_sensitivity`
- `eviction_tolerance`

Suggested initial values:

- `burst_eligible = true` only for machines that are safe to place on GCP
- `portable = true` only for machines whose durable state is not pinned to a specific host-local disk
- `storage_mode = host_local | portable_persistent | ephemeral`
- `performance_sensitivity = low | medium | high`
- `eviction_tolerance = allowed | disallowed`

Without this split, the scheduler will eventually make the wrong decision for a machine that was originally placed on OVH using local persistent storage.

### AA.5 Separate active placement from home affinity

The current system should move toward distinct concepts for:

- active runtime placement
- data or restart affinity

Recommended fields:

- `active_host_id`
- `active_vm_ip`
- `data_home_host_id`
- `preferred_capacity_tier`

This matters in the hybrid model because a machine can have:

- an OVH host as its storage home
- no current active placement
- a policy that allows temporary burst placement elsewhere

Without separating these concepts, stop, restart, recovery, and rebalance behavior will remain ambiguous.

### AA.6 Placement policy

The scheduler should apply this policy order:

1. Reject hosts that are stale, unreachable, draining, or otherwise unschedulable.
2. Filter hosts by hard machine constraints:
   - CPU
   - memory
   - architecture
   - KVM support
   - storage mode compatibility
   - provider allowlist or denylist
   - performance class requirements
3. Prefer `base` capacity over `burst`.
4. Prefer `burst` over `spot_burst`.
5. Use bin packing within each tier.
6. Apply hysteresis to avoid flapping between OVH and GCP.

The first viable production rule set should be:

- fill OVH first
- stop placing on OVH once headroom falls below a configured threshold
- use GCP only for `burst_eligible` machines
- drain GCP first when load falls

### AA.7 Headroom policy

Hybrid capacity only works well if burst is triggered before the base tier is completely full.

Add per-tier headroom policies such as:

- `base_min_free_cpu_percent`
- `base_min_free_memory_percent`
- `base_recovery_reserve_hosts`
- `burst_scale_out_threshold`
- `burst_scale_in_threshold`

Recommended initial policy:

- reserve `10%` to `20%` free capacity in the OVH base pool
- maintain at least enough spare capacity to absorb one host failure in the current failure domain
- trigger GCP burst when:
  - OVH headroom drops below threshold
  - queue delay exceeds target
  - an OVH host failure consumes the recovery reserve

### AA.8 Storage rules

Storage policy is the main constraint on hybrid bursting.

Initial storage rules should be explicit:

- `host_local` machines are not burstable across providers by default
- `portable_persistent` machines may move between OVH and GCP if the storage backend supports it
- `ephemeral` machines are fully burstable

Suggested first release policy:

- only allow GCP bursting for `ephemeral` and explicitly `portable_persistent` machines
- keep `host_local` machines pinned to the base provider unless there is an operator override

### AA.9 GCP burst pool design

The GCP burst tier should be split into two pools:

- `reliable burst`
- `cheap interruptible burst`

Recommended mapping:

- on-demand GCP nested virtualization hosts for `reliable burst`
- GCP Spot nested virtualization hosts for `cheap interruptible burst`

The first implementation should start with only the reliable burst pool. Spot should come later.

The burst pool should be built around:

- a prebuilt host image release
- provider templates or profiles
- warm provisioning paths
- quota validation
- per-region capacity policy

### AA.10 Scheduling examples

Expected outcomes for common scenarios:

- normal steady-state demand:
  - place on OVH
- OVH near capacity and machine is `burst_eligible`:
  - place on GCP on-demand burst host
- OVH near capacity and machine is not `burst_eligible`:
  - queue or reject based on policy
- low-priority ephemeral workload during cost pressure:
  - place on GCP Spot if explicitly allowed
- load drops after a burst:
  - drain and remove GCP hosts first

### AA.11 Failure recovery

Hybrid capacity should also be used as a recovery mechanism.

When an OVH host fails:

- mark the host unavailable through the reconciler
- immediately stop scheduling onto that host
- determine which affected machines are burst-eligible
- re-place burst-eligible machines onto GCP if recovery reserve is exhausted
- keep non-portable machines in an explicit recovery-required state

Do not hide non-portability by silently moving machines whose state is actually pinned to a dead host.

### AA.12 Cost controls

The scheduler and autoscaler should expose simple cost guardrails:

- maximum burst host count
- maximum burst spend per day
- maximum Spot usage for a workload class
- provider preference by environment

Suggested defaults:

- production persistent workloads prefer OVH
- overflow uses GCP on-demand
- only retryable or ephemeral workloads may use GCP Spot

### AA.13 Admin control panel requirements for hybrid capacity

The admin panel should expose hybrid-specific views and controls:

- per-provider capacity summary
- per-tier capacity summary
- base headroom vs burst usage
- machine portability flags
- burst policy thresholds
- current GCP burst host count
- queued machines blocked by portability or policy

Operators should be able to answer:

- how full is the OVH base pool
- how much burst is currently active
- why a machine was not allowed to burst
- whether burst was triggered by demand, recovery, or policy

### AA.14 Test-driven implementation order

This section should follow the delivery policy from section `U`.

PR 1 should add failing policy tests for provider-aware scheduling:

- base tier is preferred over burst when both satisfy constraints
- stale OVH hosts are excluded
- non-portable machines are not placed on GCP
- `burst_eligible` machines can be placed on GCP when OVH headroom is exhausted
- Spot is never selected for non-evictable workloads

Suggested test files:

- `backend/internal/scheduler/hybrid_policy_test.go`
- `backend/internal/store/host_capability_test.go`
- `backend/internal/api/admin_capacity_test.go`

PR 2 should add failing reconciler tests:

- OVH host failure consumes recovery reserve
- burst placement is triggered only for eligible machines
- non-portable machines remain in recovery-required state

Suggested test files:

- `backend/internal/reconciler/hybrid_recovery_test.go`
- `backend/internal/api/host_recovery_test.go`

PR 3 should add failing admin API tests:

- provider and tier summaries are exposed
- blocked-burst reasons are visible
- burst host counts and spend caps are visible

Suggested test files:

- `backend/internal/api/admin_hybrid_capacity_test.go`

Only after those failures are verified should the scheduler, reconciler, and admin views be updated.

### AA.15 Recommended first production policy

The simplest safe initial hybrid deployment is:

- OVH as the only base provider
- GCP on-demand as the only burst provider
- no Spot in phase 1
- only `ephemeral` and explicitly `burst_eligible` machines may burst
- OVH headroom threshold set to `15%`
- scale out quickly, scale in slowly

That policy is conservative, but it is the right starting point. It matches the real difference between dedicated bare metal and nested-virtualization overflow capacity instead of pretending they are identical.
