# Architecture Simplification Plan

## Goal

Reduce the number of places that own the same concept.

Today the system works, but the main complexity problem is structural:

- machine lifecycle is split across API handlers, scheduler, store SQL, agent, and tunnel/KV side effects
- host lifecycle is split across API handlers, provisioner, GCP metadata, heartbeats, and admin UI
- routing is split across Postgres, Cloudflare KV, Cloudflare tunnels/DNS, and proxy tokens
- configuration is split across registry data, capability data, machine config records, runtime metadata injection, and OpenClaw-specific platform defaults

The simplification target is:

- one owner per domain concept
- one source of truth per state category
- side effects handled by projection/reconciliation, not by every handler
- provider-neutral host model
- test-first refactors in small, reversible steps

## Current Complexity Drivers

### 1. Control Plane Has No Strong Domain Boundaries

`backend/internal/api/server.go` is effectively:

- HTTP router
- auth layer
- machine orchestration layer
- host admin layer
- config manager
- route sync coordinator
- tunnel lifecycle coordinator
- partial reconciler bootstrap

`backend/internal/store/store.go` is similarly a single interface for:

- users and accounts
- machines and hosts
- routing
- events
- credentials and secrets
- registry and machine capabilities
- usage and budgets

That means almost every meaningful change crosses too many layers.

### 2. Scheduler Is Not a Real Scheduling Domain

`backend/internal/scheduler` is a thin wrapper around store methods.

Actual policy is split between:

- SQL in `PlaceMachineOnHost`
- lifecycle behavior in API handlers
- host creation in `provisioner`
- restart affinity embedded in `machine.host_id` and `machine.vm_ip`

This makes the scheduler hard to evolve because most policy is not in one place.

### 3. Provider Logic Is Hard-Coded Into Core Flows

The current stack assumes:

- GCP Compute for managed host lifecycle
- GCP metadata for agent bootstrap and host identity
- public external IP for host heartbeat identity
- GCP snapshot/image naming as host compatibility marker

That blocks OVH, Hetzner dedicated, Vultr bare metal, and customer-owned servers from fitting the existing model cleanly.

### 4. Routing Uses Multiple Operational Truth Stores

The routing path currently depends on:

- Postgres machine + host state
- Cloudflare KV route cache
- Cloudflare tunnel/DNS state
- machine proxy token state

The database is already the authoritative source because `/api/internal/resolve` queries it and backfills KV. The rest should be treated as projections.

### 5. Runtime Trust Model Has Layering Problems

VM access control currently combines:

- Cloudflare Access at the edge
- in-VM machine JWT auth
- gateway shared token auth
- bridge firewall rules
- source-IP-based metadata identity
- localhost port proxying

The model is not impossible to secure, but it is harder than necessary because internal and user-facing services still share overlapping transport surfaces.

### 6. Artifact Lifecycle Is Mutable

Rootfs and browser rootfs are handled as mutable files and staged caches.

That leaks deployment state into:

- agent admin operations
- orchestrator boot path
- heartbeat version reporting
- admin update UX

Artifacts should be immutable releases selected by a host-level active release pointer.

## Target Architecture

### Principles

1. HTTP handlers should translate requests, not run workflows.
2. Services should own orchestration.
3. Repositories should own persistence details.
4. Postgres should be the system of record.
5. External systems should be projections or providers.
6. Reconcilers should repair drift.
7. Provider-specific behavior should live behind provider interfaces.
8. Runtime internals should be isolated from user app networking.

### Control Plane Domains

Create explicit domain packages and services.

### 1. Identity Domain

Owns:

- users
- auth resolution
- account membership
- service token policy

Suggested packages:

- `backend/internal/identity`
- `backend/internal/identity/repo`
- `backend/internal/identity/service`

### 2. Machine Domain

Owns:

- machine CRUD
- machine desired state
- machine runtime state
- machine events
- machine start/stop/delete transitions

Suggested packages:

- `backend/internal/machines`
- `backend/internal/machines/repo`
- `backend/internal/machines/service`

Important rule:

- handlers should call `MachineRuntimeService.Start/Stop/Delete`
- handlers should not call scheduler, KV, tunnel manager, and agent client directly

### 3. Fleet Domain

Owns:

- host catalog
- host state
- host capacity
- placement
- provider inventory
- fleet reconciliation

Suggested packages:

- `backend/internal/fleet`
- `backend/internal/fleet/repo`
- `backend/internal/fleet/service`

This domain absorbs most of what is now split between `scheduler`, `provisioner`, and host-related store methods.

### 4. Routing Domain

Owns:

- route resolution
- route projection to KV
- tunnel registration state
- route reconciliation

Suggested packages:

- `backend/internal/routing`
- `backend/internal/routing/repo`
- `backend/internal/routing/service`
- `backend/internal/routing/projector`

Important rule:

- Postgres is authoritative
- KV is a cache
- tunnel/DNS state is a projection dependency

### 5. Config Domain

Owns:

- registry entries
- machine capabilities
- config assembly
- validation
- compiled machine config

Suggested packages:

- `backend/internal/configruntime`
- `backend/internal/configruntime/repo`
- `backend/internal/configruntime/service`

Important rule:

- config assembly should compile typed capability input into a typed intermediate structure before emitting JSON
- raw merge behavior should not be the primary policy layer

### 6. Billing Domain

Owns:

- usage ingestion
- budgets
- cost summaries

Suggested packages:

- `backend/internal/billing`

This can remain separate from lifecycle and placement logic.

### Target Service Interfaces

### MachineRuntimeService

Responsibilities:

- validate machine state transition
- gather machine inputs
- request placement
- request route/tunnel provisioning
- request agent operation
- update desired and observed machine state

Candidate interface:

```go
type MachineRuntimeService interface {
    Start(ctx context.Context, machineID string, actor Actor) (*StartResult, error)
    Stop(ctx context.Context, machineID string, actor Actor) error
    Delete(ctx context.Context, machineID string, actor Actor) error
    Reconcile(ctx context.Context, machineID string) error
}
```

### PlacementService

Responsibilities:

- determine eligible hosts
- apply scoring
- reserve capacity
- distinguish active placement from affinity

Candidate interface:

```go
type PlacementService interface {
    Reserve(ctx context.Context, machineID string, req PlacementRequest) (*Placement, error)
    Release(ctx context.Context, machineID string) error
    ReuseAffinity(ctx context.Context, machineID string) (*Placement, error)
}
```

### HostFleetService

Responsibilities:

- host registration
- provider-backed provisioning
- maintenance/drain
- status transitions
- heartbeat processing
- host reconciliation

Candidate interface:

```go
type HostFleetService interface {
    Register(ctx context.Context, req RegisterHostRequest) (*Host, error)
    Provision(ctx context.Context, req ProvisionHostRequest) (*Host, error)
    Drain(ctx context.Context, hostID int, actor Actor) error
    Decommission(ctx context.Context, hostID int, actor Actor) error
    ProcessHeartbeat(ctx context.Context, hb Heartbeat) error
    ReconcileHost(ctx context.Context, hostID int) error
}
```

### RouteService

Responsibilities:

- compute effective route from DB state
- project route to KV
- ensure tunnel/DNS state
- remove stale route state

Candidate interface:

```go
type RouteService interface {
    Resolve(ctx context.Context, accountSlug, machineSlug string) (*ResolvedRoute, error)
    SyncMachine(ctx context.Context, machineID string) error
    DeleteMachine(ctx context.Context, machineID string) error
}
```

### Data Model Simplification

#### 1. Split Active Placement From Affinity

Current problem:

- `machines.host_id` means both current placement and restart affinity
- `machines.vm_ip` means both active network assignment and sticky restart identity

Target model:

- `machine_placements`
  - `machine_id`
  - `host_id`
  - `vm_ip`
  - `status`
  - `reserved_at`
  - `released_at`
- `machines.home_host_id` or `machines.data_home_host_id`
- `machines.storage_mode`

That allows:

- graceful stop while preserving locality
- host death without pretending the machine is still placed
- burst placement onto GCP for portable workloads

#### 2. Make Hosts Provider-Neutral

Current host shape is GCP VM-shaped.

Target fields:

- `provider`
- `provider_class`
- `lifecycle_mode`
- `provider_host_id`
- `provider_region`
- `provider_zone`
- `provider_sku`
- `agent_endpoint`
- `transport_kind`
- `performance_class`
- `capacity_tier`
- `labels`
- `capabilities`
- `provider_metadata`
- `last_heartbeat`
- `desired_state`
- `observed_state`

Keep current GCP fields during migration, then collapse them into generic fields.

#### 3. Make Artifact Releases Explicit

Add a release model:

- `artifact_releases`
  - `kind`
  - `version`
  - `uri`
  - `sha256`
  - `metadata`
- `host_artifact_state`
  - `host_id`
  - `rootfs_release`
  - `browser_rootfs_release`
  - `agent_release`
  - `staged_at`
  - `active_at`

This eliminates mutable cache behavior as the primary operational model.

### Provider Model

Support two provider classes.

### 1. Managed Provider

Examples:

- GCP
- Vultr bare metal API
- AWS bare metal

Capabilities:

- create host
- destroy host
- inspect host state
- attach bootstrap metadata

### 2. Registered Host Provider

Examples:

- OVH dedicated
- Hetzner dedicated
- self-owned server

Capabilities:

- enrollment
- inventory reconciliation
- optional provider lookup

The generic abstraction should be:

```go
type HostProvider interface {
    Name() string
    Class() ProviderClass
    Provision(ctx context.Context, req ProvisionRequest) (*ProviderHost, error)
    Destroy(ctx context.Context, host ProviderHostRef) error
    Inspect(ctx context.Context, host ProviderHostRef) (*ProviderObservation, error)
    RenderBootstrap(req BootstrapRequest) (*BootstrapArtifact, error)
}
```

For registered-host providers, `Provision` may be unsupported and enrollment is handled separately.

### Runtime Simplification

#### 1. Split Agent Responsibilities

The agent currently owns too much:

- bridge setup
- metadata
- LLM/channel proxy
- CDP proxy
- orchestrator
- control API
- proxy API
- heartbeat
- self-update

Target runtime packages:

- `hostruntime`
  - process lifecycle
  - self-update
  - heartbeat
- `vmruntime`
  - Firecracker orchestration
  - VM state
  - data volume management
- `hostnetwork`
  - bridge
  - NAT
  - isolation rules
- `artifactmanager`
  - staged release management
- `dataplane`
  - metadata
  - API proxy
  - CDP proxy

This does not require multiple binaries immediately. It requires cleaner boundaries inside the agent first.

#### 2. Separate Internal VM Services From User App Ports

Target rule:

- internal OCM services use Unix sockets where possible
- user applications use TCP and are exposed through a constrained port-preview path

This removes the current overlap between:

- terminal
- gateway
- preview/dashboard ports

#### 3. Treat Source IP As a Hint, Not Full Identity

Metadata and proxy services should stop relying solely on bridge source IP.

Target model:

- IP identifies candidate VM
- per-VM nonce or local credential authenticates the request
- bridge rules enforce anti-spoofing per TAP

### Routing Simplification

#### Source of Truth

Postgres is authoritative for:

- machine running state
- active placement
- host transport endpoint
- route eligibility

KV is:

- read-through cache only

Tunnel and DNS state are:

- projected infrastructure state

#### Runtime Projection Pattern

When a machine enters `running`:

1. machine runtime updates DB state
2. route service computes effective route
3. route projector writes KV
4. tunnel projector ensures tunnel/DNS state if required

When a machine exits `running`:

1. DB state changes first
2. projection cleanup runs
3. reconciler repairs any failed external cleanup

Handlers should not manage this sequence manually.

### Admin Control Plane Simplification

The admin panel should be organized around fleet operations, not GCP host fields.

Primary objects:

- host
- machine
- provider
- route
- release
- alert

Primary actions:

- enroll host
- provision host
- drain host
- mark maintenance
- reconcile host
- decommission host
- promote artifact release
- view route health

The API should expose provider-neutral host templates and enrollment flows, not only `machine_type`.

### Recommended Package Migration

### Current to Target

- `backend/internal/api/server.go`
  - split by domain handlers
- `backend/internal/store`
  - split into domain repositories
- `backend/internal/scheduler`
  - replace with `fleet/placement`
- `backend/internal/provisioner`
  - replace with `fleet/providers` + `fleet/service`
- `backend/internal/configassembly`
  - fold into `configruntime`
- `backend/internal/tunnel` + `backend/internal/kvstore`
  - orchestrated by `routing/service`

## Delivery Plan

All phases should be test-first.

Required sequence for each PR:

1. write or expand tests
2. verify current failure or missing behavior
3. implement minimal change
4. rerun tests
5. only then continue to the next refactor

## Phase 1: Control-Plane Boundary Cleanup

Goal:

- reduce handler-level orchestration without changing external behavior

Steps:

1. introduce `MachineRuntimeService`
2. move `startMachineInternal`, stop, delete flows out of handlers
3. keep current store interface initially

Tests first:

- machine start success
- machine start rollback on agent failure
- machine stop behavior
- machine delete behavior

## Phase 2: Repository Split

Goal:

- remove the god `Store` interface

Steps:

1. define repositories:
   - `UserRepo`
   - `AccountRepo`
   - `MachineRepo`
   - `HostRepo`
   - `RouteRepo`
   - `CredentialRepo`
   - `RegistryRepo`
2. adapt services to use narrow interfaces

Tests first:

- service tests using fakes per domain
- ensure handlers no longer need full store mocks

## Phase 3: Placement and Host Lifecycle Refactor

Goal:

- move host selection and capacity reservation into a real fleet domain

Steps:

1. introduce `PlacementService`
2. add placement records
3. add host reconciler skeleton
4. remove direct capacity mutation from handlers

Tests first:

- fresh placement
- affinity restart
- host unavailable
- stale heartbeat exclusion
- placement rollback

## Phase 4: Provider Abstraction

Goal:

- support enrolled dedicated hosts and hybrid base/burst capacity

Steps:

1. add provider-neutral host fields
2. implement `gcp` provider driver behind `HostProvider`
3. add registered-host enrollment service
4. add `customer_owned` and `ovh_registered` provider modes

Tests first:

- managed host provisioning contract tests
- registered-host enrollment tests
- heartbeat processing on non-GCP hosts

## Phase 5: Routing and Projection Refactor

Goal:

- make DB authoritative and projections repairable

Steps:

1. introduce `RouteService`
2. centralize KV projection
3. centralize tunnel/DNS projection
4. add reconciler for stale route projections

Tests first:

- route resolution from DB
- KV write failure does not corrupt DB truth
- stop/delete projection cleanup
- reconciler repairs stale KV/tunnel state

## Phase 6: Runtime Simplification

Goal:

- reduce trust-surface overlap and mutable runtime artifact behavior

Steps:

1. move internal VM services to Unix sockets
2. add explicit port preview policy
3. add artifact release manager
4. enforce per-TAP anti-spoofing

Tests first:

- `/port/*` cannot reach internal services
- artifact promotion is atomic
- cached artifact fallback behavior
- spoofing prevention on bridge rules

## Phase 7: Admin Panel Modernization

Goal:

- make operators manage a heterogeneous fleet coherently

Steps:

1. provider-neutral host list/detail APIs
2. enrollment APIs
3. reconcile/drain/maintenance actions
4. release visibility
5. route health and alert views

Tests first:

- host detail API contract
- enrollment workflow
- reconcile action behavior
- frontend tests for fleet list/detail states

## First PRs I Would Actually Start

### PR 1

Introduce `MachineRuntimeService` and move start/stop/delete orchestration out of handlers without changing DB schema.

### PR 2

Split `Store` into narrow interfaces used only by `MachineRuntimeService` and the existing handlers.

### PR 3

Add stale-heartbeat exclusion and a basic host reconciler so fleet state becomes trustworthy before larger provider work.

### PR 4

Add provider-neutral host fields and registered-host enrollment flow.

## What Not To Do

1. Do not start by rewriting the entire backend package layout.
2. Do not add more scheduling heuristics before the fleet boundary is fixed.
3. Do not add OVH/Vultr/Hetzner drivers before host identity and enrollment are generic.
4. Do not keep expanding handler logic.
5. Do not rely on Cloudflare KV or tunnel state as authoritative state.

## Success Criteria

The architecture is simpler when the following are true:

1. starting a machine is one service call, not a handler-scripted workflow
2. stopping or deleting a machine changes DB truth first and projections second
3. host lifecycle works for GCP, OVH dedicated, and customer-owned hosts through the same fleet domain
4. scheduling policy lives in one place
5. admin UI is provider-neutral
6. runtime internals are not reachable through preview paths
7. artifact rollout is immutable and observable
