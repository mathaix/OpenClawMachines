# Design: Durable Workflow Infrastructure

**Status:** Proposed
**Date:** 2026-03-13
**Scope:** Control-plane infrastructure for long-running jobs
**First workflow:** Admin machine migration
**Implementation plan:** `docs/plans/2026-03-13-durable-workflows-plan.md`

## Summary

OpenClaw Machines should introduce a durable workflow layer for long-running control-plane operations.

The immediate driver is admin migration, which currently:

- runs inside a synchronous HTTP handler
- holds a machine operation open for a long period
- mixes lock ownership, progress tracking, and request lifetime
- is vulnerable to proxy/browser timeout behavior
- has already produced poller and stale-operation edge cases

The recommendation is to adopt a general workflow substrate now, with **machine migration** as the first implementation and future reuse for:

- machine provisioning
- backups and restores
- plugin and skill installation
- host maintenance and evacuations
- approval flows
- config reconciliation

This design recommends using the **open-source DBOS Go library** as the workflow runtime, while keeping OCM's domain model, APIs, locks, and UI projections under our control.

## Why This Makes The Platform More Reliable

Yes, this should make the infrastructure both easier to debug and more reliable.

### Reliability gains

- Long-running operations survive page refreshes, proxy timeouts, and backend restarts.
- Workflow progress is durably stored instead of living in request memory.
- Steps resume from the last completed durable boundary instead of starting over blindly.
- Retries, cancellation, and timeouts become explicit platform behavior instead of handler-specific logic.

### Debugging gains

- Every long-running operation gets a stable `workflow_id`.
- Each workflow exposes a step timeline, current phase, and terminal error.
- Logs, events, and UI can all be correlated by the same workflow id.
- Operators can inspect, resume, cancel, or retry a workflow without reconstructing state from logs.

### Architectural gains

- API handlers stop orchestrating multi-minute jobs directly.
- Runtime services perform idempotent domain steps.
- Workflow infrastructure owns sequencing, waiting, retries, and recovery.
- Resource locks become concurrency primitives, not overloaded progress records.

## Problem

Today, OCM does not have a durable orchestration layer. Long-running work is handled through a combination of:

- synchronous HTTP handlers
- background goroutines
- ad hoc poll loops
- machine operations used as both lock and progress state

This is already showing strain in the migration path:

- admin migration detaches from request cancellation but still waits inline
- stale operation expiry can conflict with real workflow duration
- readiness polling and destructive cleanup are too tightly coupled
- there is no first-class, durable workflow status API for the UI or operators

The problem is broader than migration. The platform is accumulating more operations with the same shape:

- multi-step
- long-running
- retriable
- stateful
- partially destructive
- operator-visible

Those should not be implemented as large request handlers.

## Decision

Introduce a **Durable Workflow Infrastructure** with these rules:

1. Long-running orchestration lives in workflows, not request handlers.
2. Workflows are durable and resumable.
3. Domain side effects happen inside idempotent steps.
4. Resource exclusivity is handled by leases/locks, not by the workflow status record itself.
5. The control plane owns workflow status, API projection, and auditability.
6. Migration is workflow #1, but the infrastructure is generic from day one.

## Runtime Choice

### Recommended: DBOS Go

Use DBOS Go as the workflow runtime because it already provides the capabilities we need:

- durable workflows persisted in Postgres
- step-based recovery from the last completed step
- durable timeouts and sleep
- workflow queues with concurrency and rate limits
- workflow management primitives like list, cancel, resume, and fork

This is a better fit than a homegrown scheduler because we already know the platform will have many long-running jobs.

This is a better fit than adopting a larger workflow platform immediately because:

- the library is open source
- it uses Postgres, which OCM already depends on
- it does not require a new critical-path control-plane service
- it is a smaller architectural jump than Temporal

### Important boundary

DBOS should be treated as the **execution engine**, not the entire product-facing workflow model.

OCM still needs its own:

- workflow APIs
- domain projections
- resource locks
- operator UX
- policy decisions

That keeps us from coupling the product surface directly to DBOS internals.

## Goals

- Make migration durable, resumable, and observable.
- Establish a shared substrate for future long-running workflows.
- Separate request lifetime from orchestration lifetime.
- Replace fixed stale-operation guessing with explicit workflow and lease state.
- Make workflow status queryable by the dashboard, CLI, and operators.
- Allow future split between API service and worker service without redesigning the domain model.

## Non-Goals

- Replacing all short-lived handlers with workflows.
- Exposing arbitrary user-authored workflows in v1.
- Solving approval flows in this phase.
- Rebuilding the entire machine runtime around DBOS.
- Making DBOS-specific tables the primary product API.

## Supported Workflow Classes

This infrastructure should be treated as a generic control-plane workflow substrate, not just a migration fix.

It should support these workflow classes over time.

### 1. Machine lifecycle workflows

- machine provisioning
- machine recovery
- machine migration
- backup restore with restart and verification
- controlled machine deletion where cleanup spans multiple systems

### 2. Backup and data workflows

- manual backup
- scheduled backup
- backup verification
- restore
- cross-host or cross-provider data movement

### 3. Host lifecycle workflows

- host provisioning
- host enrollment and bootstrap
- host drain
- host evacuation
- host maintenance
- host decommission

### 4. Upgrade workflows

- host upgrades
- rootfs / OpenClaw rollout
- system upgrades that require canaries, phased rollout, or rollback
- data-volume upgrade or migration sequences

### 5. Extension and configuration workflows

- plugin installation
- skill installation
- artifact verification and pinning
- config reconciliation or repair
- credential and secret rotation with dependent restarts

### 6. Notification workflows

- invitation email delivery (with retry on provider failure)
- backup completion notifications
- migration completion notifications
- account alerts and digest emails
- webhook delivery for integrations

### 7. Approval and operator workflows

- approval-gated extension changes
- break-glass maintenance actions
- manual intervention / resume flows
- multi-step admin operations that wait on human input

### 8. Fleet repair and reconciliation workflows

- route repair
- stale placement repair
- machine / host state repair
- metadata or config re-sync

The common rule is:

> if an operation is long-running, stateful, retriable, and operator-visible, it is a strong workflow candidate

## What Should Not Use Workflows

Not every platform action should be put on the workflow substrate.

These should remain ordinary request/response code or reconciler logic:

- simple reads
- short synchronous writes
- low-latency RPC passthrough
- tight polling loops that are purely observational
- small CRUD actions that complete in one request without retries or waiting

Good examples of things that should usually not be workflows:

- fetching machine details
- updating a display name
- listing credentials
- toggling a small metadata field
- gateway RPC passthrough for immediate actions

The taxonomy should be:

- workflow for long-running state transitions
- reconciler for continuous drift detection and repair
- API handler for short synchronous operations

## Platform Capability Requirements

If this is a generic substrate, it must support more than just "run a function later."

These are the platform capabilities the workflow layer should be designed to support from the start.

### Parent and child workflows

Examples:

- host upgrade orchestrates many machine migrations
- system upgrade orchestrates canary, fleet expansion, and rollback phases

### Multi-resource locks

The system must handle workflows that need exclusivity on more than one resource, for example:

- machine + host
- account + artifact
- host pool + host

### Durable waiting

Workflows must be able to wait durably for:

- machine reaches `running`
- host heartbeat stabilizes
- maintenance window opens
- approval arrives
- retry backoff expires

### Cancellation and pause/resume

Operators need to be able to:

- cancel a workflow
- pause a workflow
- resume after manual remediation

### Manual intervention state

Some workflows should stop in a safe, inspectable state instead of repeatedly retrying destructive actions.

This is especially important for:

- migration
- restore
- host drain
- system upgrades

### Priority, concurrency, and queue policy

The substrate must support:

- priority separation
- per-queue concurrency
- per-resource concurrency
- throttling for fleet-wide operations

This prevents:

- backups starving interactive lifecycle operations
- system upgrades overwhelming host maintenance capacity

### Idempotency keys and deduplication

Repeated submissions of the same logical action should not create ambiguous duplicate workflows.

### Workflow versioning

Upgrade and long-lived workflows will eventually need versioned behavior.

This matters most for:

- system upgrades
- host upgrades
- approval-gated workflows

### Compensation and rollback hooks

Not every workflow can be fully rolled back, but the substrate should allow explicit compensation steps where possible.

Examples:

- undo route projection
- release placement
- mark machine stopped instead of stuck provisioning
- revert a canary rollout before expanding

## Architecture

```text
Admin UI / CLI
    |
    v
HTTP API
  - validates request
  - creates workflow run projection
  - enqueues workflow by workflow_id
  - returns 202 Accepted
    |
    v
Durable Workflow Runtime (DBOS)
  - queues work
  - executes workflow steps
  - persists recovery state in Postgres
  - resumes after crashes/restarts
    |
    +--> OCM Workflow Projection
    |     - workflow_runs
    |     - workflow_events
    |     - workflow_locks
    |
    +--> Domain Services
          - RuntimeService
          - PlacementService
          - BackupService
          - AgentClient
          - Store
```

## Where It Runs

**Phase 1 (current scaffold):** The workflow system runs inside the existing Cloud Run backend service. This works for development but has a critical limitation — Cloud Run throttles CPU between requests, starving background DBOS executor goroutines.

**Phase 1b (target):** The system splits into two deployment modes. Cloud Run serves the API (enqueue + status). A spot worker fleet on GCE runs the DBOS executors. See [Deployment Model](#deployment-model) for details.

### Process Layout

```
Cloud Run Container (backend)
│
├── HTTP Server (Chi router, port 8080)
│   ├── POST /api/admin/machines/migrate  → enqueueAdminMigrationWorkflow
│   ├── GET  /api/workflows/{id}          → handleGetWorkflow
│   ├── GET  /api/workflows/{id}/events   → handleListWorkflowEvents
│   └── GET  /api/machines/{id}/workflows → handleListMachineWorkflows
│
├── DBOS Runtime (goroutines, launched at startup)
│   ├── Workflow executor — picks up queued workflows, runs step functions
│   ├── Queue: machine-lifecycle
│   ├── Queue: host-maintenance
│   ├── Queue: artifact-install
│   └── Queue: reconcile
│
├── Workflow Service (workflows.Service)
│   ├── CreateRun, GetRun, ListRunsByScope — projection CRUD
│   ├── AddEvent, UpdateStatus, Complete  — progress tracking
│   └── AcquireLock, RenewLock, ReleaseLocks — exclusivity
│
└── Postgres (Neon)
    ├── DBOS internal tables (dbos_* — recovery state, step results)
    ├── workflow_runs     — OCM projection of workflow status
    ├── workflow_events   — append-only timeline
    ├── workflow_locks    — resource exclusivity leases
    └── machine_operations — legacy locking (coexists for now)
```

### Startup Sequence

```
main.go
  │
  1. NewServer(...)                    — create API server with all dependencies
  │
  2. workflows.NewService(ctx, db, Config{
       AppName:       "openclawmachines-control-plane",
       DatabaseURL:   cfg.DatabaseURL,
       EnableRuntime: cfg.EnableDurableWorkflows,    ← feature flag
       Register:      srv.RegisterWorkflows,
     })
     │
     ├── NewRuntime(ctx, cfg)
     │   ├── dbos.NewDBOSContext(...)         — connect to Postgres, create dbos_* tables
     │   ├── dbos.NewWorkflowQueue(...)  ×4   — register named queues
     │   ├── cfg.Register(dbosCtx)            — calls srv.RegisterWorkflows
     │   │   └── dbos.RegisterWorkflow(ctx, s.runMigrationWorkflow, ...)
     │   └── dbos.Launch(dbosCtx)             — start executor goroutines
     │
     └── return &Service{repo: db, runtime: runtime}
  │
  3. srv.SetWorkflowService(workflowSvc)   — wire into API server
  │
  4. http.ListenAndServe(":8080", ...)     — start serving requests
```

### Files and Their Roles

| File | What it does |
|------|-------------|
| `backend/cmd/server/main.go:136-148` | Creates workflow service, wires it into the API server |
| `backend/internal/workflows/runtime.go` | Initializes DBOS runtime, registers queues, manages lifecycle |
| `backend/internal/workflows/service.go` | Projection CRUD (workflow runs, events, locks) — thin layer over store |
| `backend/internal/workflows/types.go` | Constants: statuses, kinds, queue names, parameter structs |
| `backend/internal/api/admin_migrate_workflow.go` | Migration workflow: enqueue handler + step implementations |
| `backend/internal/api/workflows.go` | Read-only API endpoints for workflow status and events |
| `backend/internal/api/server.go:368` | Route: `POST /api/admin/machines/migrate` |
| `backend/internal/api/server.go:249-250` | Routes: `GET /api/workflows/{id}` and `GET /api/workflows/{id}/events` |
| `backend/internal/store/postgres.go` | SQL implementations for workflow_runs, workflow_events, workflow_locks |
| `backend/internal/store/store.go` | Interface definitions: `WorkflowRepo` |
| `backend/migrations/034_workflow_runs.sql` | DDL for workflow_runs, workflow_events, workflow_locks tables |

### Feature Flag

Workflow execution is gated by `cfg.EnableDurableWorkflows`. When disabled:

- The DBOS runtime does not start (no executor goroutines)
- `POST /api/admin/machines/migrate` falls through to the synchronous handler
- Workflow read endpoints return `503 Service Unavailable`
- The projection tables still exist but remain empty

### Database Tables

```sql
-- OCM projection (our tables)
workflow_runs    — id, kind, scope, status, phase, input/output JSON, timestamps
workflow_events  — append-only timeline per workflow (phase changes, errors)
workflow_locks   — resource exclusivity leases (PK: resource_type, resource_id, lock_kind)

-- DBOS internal tables (managed by DBOS library)
dbos_*           — step results, workflow state, recovery data
                   (do NOT query directly — DBOS owns these)
```

## Sequence Diagrams

### 1. Migration Workflow — Enqueue (API Handler)

```
Admin UI                    Backend API                    Postgres
  │                            │                              │
  │  POST /api/admin/          │                              │
  │  machines/migrate          │                              │
  │  {machine_id, host_id}     │                              │
  │───────────────────────────>│                              │
  │                            │                              │
  │                            │  validateMigrationRequest    │
  │                            │─────────────────────────────>│
  │                            │  machine, targetHost         │
  │                            │<─────────────────────────────│
  │                            │                              │
  │                            │  workflows.NewID()           │
  │                            │  → "wf_a1b2c3d4e5f6"        │
  │                            │                              │
  │                            │  CreateMachineOperation      │
  │                            │  (kind=migrate, idempotency  │
  │                            │   key=workflow_id)            │
  │                            │─────────────────────────────>│
  │                            │  op.ID                       │
  │                            │<─────────────────────────────│
  │                            │                              │
  │                            │  workflows.CreateRun         │
  │                            │  INSERT workflow_runs        │
  │                            │  (status=queued)             │
  │                            │─────────────────────────────>│
  │                            │<─────────────────────────────│
  │                            │                              │
  │                            │  workflows.AddEvent          │
  │                            │  INSERT workflow_events      │
  │                            │  (workflow.queued)            │
  │                            │─────────────────────────────>│
  │                            │<─────────────────────────────│
  │                            │                              │
  │                            │  dbos.RunWorkflow            │
  │                            │  (queue=machine-lifecycle,   │
  │                            │   partition=machine_id)      │
  │                            │─────────────────────────────>│
  │                            │                              │
  │  202 Accepted              │                              │
  │  {workflow_id, status:     │                              │
  │   "queued"}                │                              │
  │<───────────────────────────│                              │
```

### 2. Migration Workflow — Execution (DBOS Runtime)

```
DBOS Executor               runMigrationWorkflow            Domain Services
(goroutine)                  (step functions)                (Store, Agent, Placement)
  │                              │                              │
  │  Pick up wf_a1b2c3          │                              │
  │─────────────────────────────>│                              │
  │                              │                              │
  │  RunAsStep("prepare")       │                              │
  │                              │  validateMigrationRequest   │
  │                              │─────────────────────────────>│
  │                              │  plan: {sourceHostID,       │
  │                              │   canBackup, releaseCap}    │
  │                              │<─────────────────────────────│
  │                              │                              │
  │  RunAsStep("stop_source")   │                              │
  │                              │  agentClient.StopVM         │
  │                              │─────────────────────────────>│ Agent (source host)
  │                              │  (stopped)                  │
  │                              │<─────────────────────────────│
  │                              │                              │
  │  RunAsStep("backup_source") │                              │
  │                              │  createMigrationBackup      │
  │                              │  agentClient.BackupVM       │
  │                              │─────────────────────────────>│ Agent → GCS
  │                              │  backup record              │
  │                              │<─────────────────────────────│
  │                              │                              │
  │  RunAsStep("destroy_src")   │                              │
  │                              │  agentClient.DestroyVM      │
  │                              │─────────────────────────────>│ Agent (source)
  │                              │<─────────────────────────────│
  │                              │                              │
  │  RunAsStep("release_src")   │                              │
  │                              │  releaseMigrationSource     │
  │                              │─────────────────────────────>│ Store (Postgres)
  │                              │<─────────────────────────────│
  │                              │                              │
  │  RunAsStep("start_target")  │                              │
  │                              │  machines.StartWithOperation│
  │                              │  (SkipPoll: true)           │
  │                              │─────────────────────────────>│ Agent (target host)
  │                              │  host, placement            │
  │                              │<─────────────────────────────│
  │                              │                              │
  │  RunAsStep("wait_running")  │                              │
  │                              │  waitForMachineRunningOnHost│
  │                              │  (poll loop, 3min timeout)  │
  │                              │  ┌────────────────────────┐ │
  │                              │  │ GetVM → provisioning   │ │
  │                              │  │ sleep 3s               │ │
  │                              │  │ GetVM → running ✓      │ │
  │                              │  └────────────────────────┘ │
  │                              │  syncMachineRouteToKV       │
  │                              │  ActivatePlacement          │
  │                              │  UpdateMachineStatus        │
  │                              │─────────────────────────────>│
  │                              │<─────────────────────────────│
  │                              │                              │
  │  ══ If backup exists, continue with restore ══             │
  │                              │                              │
  │  RunAsStep("stop_restore")  │  machines.StopWithOperation  │
  │  RunAsStep("wait_stopped")  │  waitForMachineStatus        │
  │  RunAsStep("restore")       │  agentClient.RestoreVM       │
  │  RunAsStep("restart")       │  machines.StartWithOperation │
  │  RunAsStep("wait_restored") │  waitForMachineRunningOnHost │
  │                              │                              │
  │  ══ Finalize ══════════════════════════════════             │
  │                              │                              │
  │  defer finalizeMigrationWorkflow                           │
  │                              │  workflows.Complete         │
  │                              │  (status=completed)         │
  │                              │─────────────────────────────>│ Postgres
  │                              │  CompleteMachineOperation   │
  │                              │  (status=completed)         │
  │                              │─────────────────────────────>│ Postgres
  │                              │<─────────────────────────────│
```

### 3. Workflow Status Query (Dashboard)

```
Dashboard                   Backend API                    Postgres
  │                            │                              │
  │  GET /api/machines/        │                              │
  │  {id}/workflows            │                              │
  │───────────────────────────>│                              │
  │                            │  GetMachine (verify account) │
  │                            │─────────────────────────────>│
  │                            │<─────────────────────────────│
  │                            │                              │
  │                            │  ListRunsByScope             │
  │                            │  (scope=machine, id={id})    │
  │                            │─────────────────────────────>│
  │  [{id, kind, status,       │  workflow_runs rows          │
  │    phase, timestamps}]     │<─────────────────────────────│
  │<───────────────────────────│                              │
  │                            │                              │
  │  GET /api/workflows/       │                              │
  │  {wf_id}/events            │                              │
  │───────────────────────────>│                              │
  │                            │  ListWorkflowEvents          │
  │                            │─────────────────────────────>│
  │  [{phase, level, type,     │  workflow_events rows        │
  │    message, timestamp}]    │<─────────────────────────────│
  │<───────────────────────────│                              │
```

### 4. Crash Recovery (DBOS Replay)

```
Cloud Run                  DBOS Runtime                   Postgres
(instance restart)             │                              │
  │                            │                              │
  │  main.go starts            │                              │
  │  workflows.NewService      │                              │
  │───────────────────────────>│                              │
  │                            │  dbos.NewDBOSContext         │
  │                            │  dbos.Launch                 │
  │                            │                              │
  │                            │  Scan for incomplete         │
  │                            │  workflows in dbos_* tables  │
  │                            │─────────────────────────────>│
  │                            │  wf_a1b2c3 was running,      │
  │                            │  last completed step:        │
  │                            │  "migration.backup_source"   │
  │                            │<─────────────────────────────│
  │                            │                              │
  │                            │  Resume runMigrationWorkflow │
  │                            │  from after backup_source    │
  │                            │                              │
  │                            │  RunAsStep("destroy_src")    │
  │                            │  → step result exists in     │
  │                            │    dbos_* → replay cached    │
  │                            │    result, skip execution    │
  │                            │                              │
  │                            │  RunAsStep("release_src")    │
  │                            │  → no cached result          │
  │                            │  → execute step normally     │
  │                            │─────────────────────────────>│
  │                            │                              │
  │                            │  ... continues from here ... │
```

## Core Components

### 1. Workflow API Layer

API handlers become thin entrypoints.

Responsibilities:

- validate input and authorization
- create a domain projection row
- enqueue a workflow
- return a workflow id immediately

Example:

```text
POST /api/admin/migrations
  -> validate machine_id + target_host_id
  -> create workflow_runs row
  -> enqueue MachineMigrationWorkflow
  -> return 202 { workflow_id }
```

The handler no longer waits for stop, backup, release, start, restore, or verification.

### 2. Workflow Runtime

DBOS executes workflow code durably.

Use DBOS features for:

- durable execution and recovery
- workflow queues
- durable timeout and sleep
- workflow management

Use OCM workflow code for:

- domain step boundaries
- retry policy
- error classification
- progress events
- resource locking

### 3. Workflow Projection

OCM needs product-facing tables independent of DBOS internals.

#### `workflow_runs`

Tracks the current summary of a workflow.

Suggested shape:

```sql
CREATE TABLE workflow_runs (
    id                TEXT PRIMARY KEY,      -- same as DBOS workflow_id
    kind              TEXT NOT NULL,         -- migration, provision, backup, restore, install_plugin, ...
    scope_type        TEXT NOT NULL,         -- machine, host, account, system
    scope_id          TEXT NOT NULL,
    status            TEXT NOT NULL,         -- queued, running, waiting, completed, failed, cancelled, manual_action_required
    current_phase     TEXT,
    requested_by      INT REFERENCES users(id),
    account_id        INT REFERENCES accounts(id),
    priority          TEXT NOT NULL DEFAULT 'normal',
    input_json        JSONB NOT NULL,
    output_json       JSONB,
    summary_json      JSONB,
    error_code        TEXT,
    error_message     TEXT,
    started_at        TIMESTAMPTZ,
    completed_at      TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

#### `workflow_events`

Append-only event stream for phase changes and notable actions.

Suggested shape:

```sql
CREATE TABLE workflow_events (
    id                BIGSERIAL PRIMARY KEY,
    workflow_id       TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    phase             TEXT,
    level             TEXT NOT NULL,         -- info, warn, error
    event_type        TEXT NOT NULL,         -- phase.started, phase.completed, retry.scheduled, waiting, manual_action_required, ...
    message           TEXT NOT NULL,
    details_json      JSONB,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

This table is what the UI and CLI should read to show a timeline.

### 4. Resource Locks / Leases

We still need explicit exclusivity for domain resources.

Workflows and short-lived handlers must not both mutate the same machine or host concurrently.

#### `workflow_locks`

```sql
CREATE TABLE workflow_locks (
    resource_type     TEXT NOT NULL,         -- machine, host, account
    resource_id       TEXT NOT NULL,
    lock_kind         TEXT NOT NULL,         -- lifecycle, maintenance, config, billing
    workflow_id       TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    lease_expires_at  TIMESTAMPTZ NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (resource_type, resource_id, lock_kind)
);
```

Rules:

- locks are for exclusivity only
- `workflow_runs` is for progress/state only
- leases must be renewed while the workflow is active
- expiry is based on missed heartbeats, not a single hardcoded stale-op timeout

### 5. Domain Step Adapters

Existing services like `RuntimeService`, placement, backup, and the agent client should become step adapters.

That means:

- they do one bounded thing well
- they are safe to call repeatedly
- they do not assume request ownership
- they do not own workflow progress or UI semantics

This is especially important for pollers and cleanup code.

Observation and cleanup must be decoupled.

## Workflow Status Model

Platform-level workflow statuses:

- `queued`
- `running`
- `waiting`
- `completed`
- `failed`
- `cancelled`
- `manual_action_required`

Rules:

- `running` means a worker is actively executing or retrying a step
- `waiting` means the workflow is durably sleeping or waiting for an external condition
- `manual_action_required` means automatic recovery stopped at a deliberate boundary

This is a better operator model than trying to infer status from machine rows or operation locks.

## Queues

Use queue separation to avoid one class of workflow starving another.

Initial queues:

- `machine-lifecycle` — migrations, provisioning, start/stop
- `host-maintenance` — drain, evacuation, decommission
- `artifact-install` — plugins, skills, rootfs
- `reconcile` — fleet repair, state sync
- `notifications` — email delivery, webhook calls, alerts

Initial principle:

- keep machine lifecycle workflows rate-limited and bounded
- allow backup/reconcile work to scale independently
- prefer separate queue names over ad hoc handler throttles

DBOS queues should control coarse concurrency. Resource locks should control per-resource exclusivity.

## Migration Workflow

Migration is the first workflow because it already has all the characteristics that justify this infrastructure:

- long-running
- multi-step
- partially destructive
- operator initiated
- recovery-sensitive

### Input

```json
{
  "machine_id": "uuid",
  "target_host_id": 123,
  "force": false,
  "requested_by": 42
}
```

### Output

```json
{
  "machine_id": "uuid",
  "target_host_id": 123,
  "source_host_id": 45,
  "backup_id": 678,
  "result": "migrated_and_restored"
}
```

### Phases

1. `validate_request`
2. `acquire_machine_lifecycle_lock`
3. `stop_source_vm_if_running`
4. `create_migration_backup_if_needed`
5. `release_source_placement`
6. `start_on_target_host`
7. `wait_for_target_running`
8. `stop_for_restore`
9. `restore_backup`
10. `restart_restored_vm`
11. `verify_running`
12. `finalize`

### Important rules

- Each phase must be idempotent.
- Each phase must emit a workflow event.
- No phase should rely on a live HTTP connection.
- No phase should use a background goroutine as its sole source of progress.
- Ambiguous states should transition to `manual_action_required`, not hide behind repeated destructive retries.

### Retry model

Retries should be configured per step, not globally.

Examples:

- `GetHost`, `GetMachine`, `CreateBackup`, `RestoreVM`: retryable with bounded backoff
- `invalid target host`, `backup key missing`, `email/account mismatch`: terminal failure
- `source placement released but target start unknown`: may require manual intervention

### Waiting model

Waiting for machine state should be a durable workflow wait, not an HTTP loop.

Examples:

- wait for `running`
- wait for `stopped`
- wait for a retry backoff

If polling is still needed initially, it should be done inside the workflow with explicit progress updates and bounded timeouts.

## Step Design Rules

For this infrastructure to actually improve reliability, every workflow step must follow the same discipline.

### 1. Idempotent inputs and side effects

Examples:

- stopping an already stopped VM is success
- releasing an already released placement is success
- restoring the same backup to the same stopped machine should detect prior completion
- creating the same workflow twice should be deduplicated by request id or domain lock

### 2. Explicit result classification

Each step returns one of:

- success
- retryable error
- terminal error
- manual intervention required

### 3. No hidden destructive cleanup

Pollers and observers must not unilaterally hard-release machines during workflow-owned operations.

Destructive cleanup should only happen when:

- the workflow explicitly chooses it
- a reconciler with clear ownership chooses it
- the system has enough evidence to do so safely

### 4. Log every step with `workflow_id`

Every domain log emitted from a workflow path must include:

- `workflow_id`
- `workflow_kind`
- `scope_type`
- `scope_id`
- `phase`

## Retry and Error Handling Policy

### Current gap

The migration workflow has **zero retry configuration** on any step. Every `dbos.RunAsStep` call uses defaults (`WithStepMaxRetries(0)`). If a transient error occurs — network blip to agent, momentary Postgres hiccup, GCS timeout — the entire workflow fails immediately.

### DBOS retry capabilities

DBOS Go supports per-step retry with exponential backoff out of the box:

```go
dbos.RunAsStep(ctx, stepFunc,
    dbos.WithStepName("backup_source"),
    dbos.WithStepMaxRetries(3),
    dbos.WithBaseInterval(1 * time.Second),
    dbos.WithBackoffFactor(2.0),
    dbos.WithMaxInterval(30 * time.Second),
)
```

If a step exhausts all retry attempts, it returns an error to the calling workflow, which can then decide whether to fail, enter manual intervention, or run compensation steps.

### Recommended retry tiers

Steps should be classified into retry tiers based on their side-effect profile:

| Tier | Retry policy | Examples |
|------|-------------|----------|
| **Read-only / query** | 3 retries, 500ms base, 2x backoff | `GetMachine`, `GetHost`, `GetVM`, `ListMembers` |
| **Idempotent write** | 3 retries, 1s base, 2x backoff | `StopVM` (already stopped = success), `DeleteRouteSync`, `UpdateMachineStatus` |
| **Non-idempotent external** | 1 retry, 2s base | `CreateVM`, `BackupVM`, `RestoreVM` — must add idempotency guards before increasing |
| **Destructive** | 0 retries | `DestroyVM` — log and escalate to manual intervention on failure |
| **Notification** | 5 retries, 5s base, 3x backoff, 5min max | Email delivery, webhook calls — transient provider failures are common |

### Error classification

Steps should classify errors before deciding retry behavior:

- **Retryable**: network timeout, 502/503/504, connection refused, temporary DNS failure
- **Terminal**: 400 bad request, 404 not found (resource doesn't exist), 409 conflict (idempotency violation), validation errors
- **Escalate to manual**: ambiguous state (step partially executed), resource in unexpected state, persistent failures after retry exhaustion

### What happens when a workflow fails

When a workflow reaches terminal failure:

1. `finalizeMigrationWorkflow` (or equivalent) marks `workflow_runs.status = 'failed'` with error code and message
2. Associated `MachineOperation` is completed with `status = 'failed'`
3. Workflow locks are released
4. The workflow event timeline preserves the full history for debugging
5. **No automatic re-execution** — failed workflows stay failed until an operator intervenes

There is no spin. DBOS does not automatically re-queue a workflow that returns an error from its top-level function. The workflow is done. Recovery options are:

- Operator inspects the timeline and manually re-triggers
- A future `POST /api/workflows/{id}/retry` endpoint re-enqueues with the same input
- A reconciler detects the stuck state and creates a new workflow

### Crash recovery vs. error failure

These are different scenarios:

| Scenario | Behavior |
|----------|----------|
| **Step returns error** | Workflow function receives error, runs compensation/cleanup, marks failed. Done. |
| **Process crashes mid-step** | DBOS replays from last checkpoint on restart. In-flight step re-executes. |
| **Process crashes between steps** | DBOS resumes at the next step. Completed step results are replayed from DB. |
| **Process crashes during retry** | DBOS resumes the step, which may retry again up to remaining attempts. |

The crash-recovery replay is what makes idempotency guards important: a step that creates a VM might be replayed, so it should check "does this VM already exist?" before creating.

## Notification Workflows

### Email for invitations

Invitation email delivery is the first notification workflow. It shares the same durable properties as machine operations:

- Must survive API handler completion (fire-and-forget from the user's perspective)
- Must retry on transient email provider failures (SMTP timeout, rate limit, temporary bounce)
- Must be auditable (who was invited, when, how many delivery attempts, final status)
- Must not send duplicate emails on crash recovery (idempotency via invitation token)

### Workflow kind: `send_notification`

```json
{
  "kind": "send_notification",
  "scope_type": "account",
  "scope_id": "42",
  "input_json": {
    "notification_type": "invitation",
    "recipient_email": "alice@example.com",
    "invitation_token": "inv_abc123",
    "account_name": "My Team",
    "inviter_email": "bob@example.com",
    "role": "member"
  }
}
```

### Sequence Diagram — Invitation Email (End-to-End)

This diagram captures the full lifecycle: admin creates the invitation, the email workflow delivers it durably, the recipient clicks the link, accepts, and the KV cache is refreshed.

```
Admin                Backend API           Postgres            DBOS Runtime         Email Provider
  │                      │                     │                    │                      │
  │  POST /invitations   │                     │                    │                      │
  │  {email:"alice@      │                     │                    │                      │
  │   example.com",      │                     │                    │                      │
  │   role:"member"}     │                     │                    │                      │
  │─────────────────────>│                     │                    │                      │
  │                      │                     │                    │                      │
  │                      │  Validate:          │                    │                      │
  │                      │  - caller is owner  │                    │                      │
  │                      │    or admin          │                    │                      │
  │                      │  - not self-invite   │                    │                      │
  │                      │  - not already       │                    │                      │
  │                      │    member            │                    │                      │
  │                      │  - no duplicate      │                    │                      │
  │                      │    pending invite    │                    │                      │
  │                      │                     │                    │                      │
  │                      │  INSERT INTO         │                    │                      │
  │                      │  account_invitations │                    │                      │
  │                      │  (status=pending,    │                    │                      │
  │                      │   token=inv_abc123,  │                    │                      │
  │                      │   expires_at=+7d)    │                    │                      │
  │                      │────────────────────>│                    │                      │
  │                      │  inv.ID, inv.Token   │                    │                      │
  │                      │<────────────────────│                    │                      │
  │                      │                     │                    │                      │
  │                      │  workflows.CreateRun │                    │                      │
  │                      │  (kind=send_notif,   │                    │                      │
  │                      │   scope=account:42)  │                    │                      │
  │                      │────────────────────>│                    │                      │
  │                      │                     │                    │                      │
  │                      │  dbos.RunWorkflow    │                    │                      │
  │                      │  (sendNotification,  │                    │                      │
  │                      │   queue=notifications,│                   │                      │
  │                      │   id=inv_abc123)     │                    │                      │
  │                      │─────────────────────────────────────────>│                      │
  │                      │                     │                    │                      │
  │  201 Created         │                     │                    │                      │
  │  {id, token, status: │                     │                    │                      │
  │   "pending"}         │                     │                    │                      │
  │<─────────────────────│                     │                    │                      │
  │                      │                     │                    │                      │
  ═══ Admin is done. Everything below happens asynchronously. ═══════════════════════════
  │                      │                     │                    │                      │
  │                      │                     │                    │  Pick up workflow     │
  │                      │                     │                    │  from notifications   │
  │                      │                     │                    │  queue                │
  │                      │                     │                    │                      │
  │                      │                     │                    │  RunAsStep            │
  │                      │                     │                    │  ("render_email")     │
  │                      │                     │                    │                      │
  │                      │                     │                    │  Build email:         │
  │                      │                     │                    │  To: alice@example.com│
  │                      │                     │                    │  Subject: "Bob        │
  │                      │                     │                    │   invited you to      │
  │                      │                     │                    │   My Team"            │
  │                      │                     │                    │  Body: HTML with      │
  │                      │                     │                    │   accept/decline      │
  │                      │                     │                    │   links containing    │
  │                      │                     │                    │   inv_abc123 token    │
  │                      │                     │                    │                      │
  │                      │                     │                    │  RunAsStep            │
  │                      │                     │                    │  ("deliver_email")    │
  │                      │                     │                    │  5 retries, 5s base,  │
  │                      │                     │                    │  3x backoff, 5min max │
  │                      │                     │                    │─────────────────────>│
  │                      │                     │                    │  (transient 429)      │
  │                      │                     │                    │<─────────────────────│
  │                      │                     │                    │                      │
  │                      │                     │                    │  ... wait 5s ...      │
  │                      │                     │                    │                      │
  │                      │                     │                    │─────────────────────>│
  │                      │                     │                    │  200 OK (delivered)   │
  │                      │                     │                    │<─────────────────────│
  │                      │                     │                    │                      │
  │                      │                     │                    │  RunAsStep            │
  │                      │                     │                    │  ("record_delivery")  │
  │                      │                     │  UPDATE            │                      │
  │                      │                     │  workflow_runs     │                      │
  │                      │                     │  status=completed  │                      │
  │                      │                     │<───────────────────│                      │
  │                      │                     │                    │                      │
```

### Sequence Diagram — Recipient Accepts Invitation

```
Recipient (Alice)       Frontend               Backend API            Postgres          KV Store
  │                        │                       │                      │                 │
  │  Clicks email link     │                       │                      │                 │
  │  /invite/inv_abc123    │                       │                      │                 │
  │───────────────────────>│                       │                      │                 │
  │                        │                       │                      │                 │
  │                        │  GET /invitations/    │                      │                 │
  │                        │  inv_abc123           │                      │                 │
  │                        │──────────────────────>│                      │                 │
  │                        │                       │  GetInvitationByToken│                 │
  │                        │                       │─────────────────────>│                 │
  │                        │                       │  inv: pending,       │                 │
  │                        │                       │  account="My Team"   │                 │
  │                        │                       │<─────────────────────│                 │
  │                        │                       │                      │                 │
  │                        │  {account_name:       │                      │                 │
  │                        │   "My Team",          │                      │                 │
  │                        │   role: "member",     │                      │                 │
  │                        │   status: "pending",  │                      │                 │
  │                        │   inviter: "bob@..."}│                      │                 │
  │                        │<──────────────────────│                      │                 │
  │                        │                       │                      │                 │
  │  Sees invitation page: │                       │                      │                 │
  │  "Bob invited you to   │                       │                      │                 │
  │   My Team as member"   │                       │                      │                 │
  │  [Accept] [Decline]    │                       │                      │                 │
  │                        │                       │                      │                 │
  │  Clicks [Accept]       │                       │                      │                 │
  │───────────────────────>│                       │                      │                 │
  │                        │                       │                      │                 │
  │                        │  POST /invitations/   │                      │                 │
  │                        │  inv_abc123/accept    │                      │                 │
  │                        │──────────────────────>│                      │                 │
  │                        │                       │                      │                 │
  │                        │                       │  Verify:             │                 │
  │                        │                       │  - token valid       │                 │
  │                        │                       │  - status=pending    │                 │
  │                        │                       │  - not expired       │                 │
  │                        │                       │  - email matches JWT │                 │
  │                        │                       │                      │                 │
  │                        │                       │  AcceptInvitation    │                 │
  │                        │                       │  (atomic: UPDATE     │                 │
  │                        │                       │   status=accepted +  │                 │
  │                        │                       │   INSERT member)     │                 │
  │                        │                       │─────────────────────>│                 │
  │                        │                       │  rows=1 (claimed)    │                 │
  │                        │                       │<─────────────────────│                 │
  │                        │                       │                      │                 │
  │                        │                       │  refreshAccountKVCache                 │
  │                        │                       │  ListAccountMembers  │                 │
  │                        │                       │─────────────────────>│                 │
  │                        │                       │  [bob, alice]        │                 │
  │                        │                       │<─────────────────────│                 │
  │                        │                       │                      │                 │
  │                        │                       │  PutAccount("myteam",│                 │
  │                        │                       │   {user_ids:[7,42]}) │                 │
  │                        │                       │──────────────────────────────────────>│
  │                        │                       │                      │                 │
  │                        │                       │  CreateAccountEvent  │                 │
  │                        │                       │  (member.accepted)   │                 │
  │                        │                       │─────────────────────>│                 │
  │                        │                       │                      │                 │
  │                        │  {account_id: 42,     │                      │                 │
  │                        │   role: "member",     │                      │                 │
  │                        │   message: "accepted"}│                      │                 │
  │                        │<──────────────────────│                      │                 │
  │                        │                       │                      │                 │
  │  Redirected to         │                       │                      │                 │
  │  /dashboard            │                       │                      │                 │
  │  (now sees My Team     │                       │                      │                 │
  │   in account switcher) │                       │                      │                 │
  │                        │                       │                      │                 │
  ═══ Alice can now access My Team's machines via Cloudflare Worker ═══
  │                        │                       │                      │                 │
  │  GET myteam.ocm.com/  │                       │                      │                 │
  │  bot/                  │  ──────────────────── Cloudflare Worker ────────────────────  │
  │                        │                       │                      │                 │
  │                        │  GET account:myteam   │                      │              KV │
  │                        │──────────────────────────────────────────────────────────────>│
  │                        │  {user_ids:[7,42]}    │                      │                 │
  │                        │<──────────────────────────────────────────────────────────────│
  │                        │                       │                      │                 │
  │                        │  42 in [7,42]? ✓      │                      │                 │
  │                        │  Proxy to agent       │                      │                 │
  │  <workspace response>  │                       │                      │                 │
  │<───────────────────────│                       │                      │                 │
```

### Sequence Diagram — Invitation Email Delivery Failure

Shows what happens when the email provider is persistently down and retries are exhausted.

```
DBOS Runtime              Email Provider            Postgres               Admin (later)
  │                            │                        │                       │
  │  RunAsStep                 │                        │                       │
  │  ("deliver_email")        │                        │                       │
  │  MaxRetries=5, 5s base,   │                        │                       │
  │  3x backoff, 5min max     │                        │                       │
  │                            │                        │                       │
  │  Attempt 1                 │                        │                       │
  │───────────────────────────>│                        │                       │
  │  500 Internal Server Error │                        │                       │
  │<───────────────────────────│                        │                       │
  │                            │                        │                       │
  │  ... wait 5s ...           │                        │                       │
  │                            │                        │                       │
  │  Attempt 2                 │                        │                       │
  │───────────────────────────>│                        │                       │
  │  500 Internal Server Error │                        │                       │
  │<───────────────────────────│                        │                       │
  │                            │                        │                       │
  │  ... wait 15s ...          │                        │                       │
  │                            │                        │                       │
  │  Attempt 3                 │                        │                       │
  │───────────────────────────>│                        │                       │
  │  Connection refused        │                        │                       │
  │<───────────────────────────│                        │                       │
  │                            │                        │                       │
  │  ... wait 45s ...          │                        │                       │
  │                            │                        │                       │
  │  Attempt 4                 │                        │                       │
  │───────────────────────────>│                        │                       │
  │  Connection timeout        │                        │                       │
  │<───────────────────────────│                        │                       │
  │                            │                        │                       │
  │  ... wait 135s ...         │                        │                       │
  │                            │                        │                       │
  │  Attempt 5                 │                        │                       │
  │───────────────────────────>│                        │                       │
  │  Connection timeout        │                        │                       │
  │<───────────────────────────│                        │                       │
  │                            │                        │                       │
  │  Step exhausted retries    │                        │                       │
  │  → returns error           │                        │                       │
  │                            │                        │                       │
  │  Workflow marks failed:    │                        │                       │
  │  UPDATE workflow_runs      │                        │                       │
  │  status=failed,            │                        │                       │
  │  error_code=               │                        │                       │
  │   "email_delivery_failed", │                        │                       │
  │  error_message=            │                        │                       │
  │   "5 attempts exhausted"   │                        │                       │
  │────────────────────────────────────────────────────>│                       │
  │                            │                        │                       │
  │  INSERT workflow_events    │                        │                       │
  │  (level=error,             │                        │                       │
  │   type=delivery.failed,    │                        │                       │
  │   message="email delivery  │                        │                       │
  │    failed after 5          │                        │                       │
  │    attempts")              │                        │                       │
  │────────────────────────────────────────────────────>│                       │
  │                            │                        │                       │
  │  ══ NO SPIN. Workflow is done. ══                   │                       │
  │                            │                        │                       │
  │                            │                        │       (hours later)   │
  │                            │                        │                       │
  │                            │                        │  Sees failed workflow  │
  │                            │                        │  in dashboard          │
  │                            │                        │  "invitation email to  │
  │                            │                        │   alice@... failed"    │
  │                            │                        │                       │
  │                            │                        │  Options:              │
  │                            │                        │  1. Retry workflow     │
  │                            │                        │  2. Revoke & re-invite │
  │                            │                        │  3. Share link manually│
  │                            │                        │<──────────────────────│
```

### Queue: `notifications`

Notification workflows should run on a dedicated queue, separate from machine lifecycle operations:

- Notification delivery should never block or delay machine migrations, backups, or provisioning
- Notifications can tolerate higher latency (seconds to minutes) without user impact
- The queue can have higher concurrency limits than lifecycle queues

Add to the queue list:

```go
dbos.NewWorkflowQueue(dbosCtx, QueueNotifications)  // "notifications"
```

### Future notification types

The same workflow kind and infrastructure supports:

| Notification | Trigger | Template |
|-------------|---------|----------|
| Invitation email | `handleCreateInvitation` | Invite link with accept/decline |
| Invitation accepted | `handleAcceptInvitation` | Notify inviter that recipient joined |
| Member removed | `handleRemoveMember` | Notify removed member |
| Backup complete | backup workflow completion | Machine name, backup size, duration |
| Migration complete | migration workflow completion | Machine name, source→target host |
| Machine stopped unexpectedly | reconciler | Machine name, last known state |
| Account quota warning | periodic check | Current usage vs. limits |

### Idempotency

Notification idempotency is keyed on the triggering event:

- Invitation email: `invitation_token` — same token never triggers two sends
- Workflow completion: `workflow_id` — same workflow never triggers two notifications
- Periodic alerts: `alert_type + scope_id + date` — one alert per resource per day

## Workflow Dashboard

### Purpose

The workflow dashboard gives operators visibility into all running and recent jobs. Without it, the only way to know a workflow failed is to check logs — which means failures can go unnoticed for hours.

### Required views

#### 1. Workflow list (main view)

| Column | Source |
|--------|--------|
| ID | `workflow_runs.id` |
| Kind | `workflow_runs.kind` (migration, backup, send_notification, ...) |
| Scope | `workflow_runs.scope_type + scope_id` (e.g., "machine: bot-001") |
| Status | `workflow_runs.status` with color coding |
| Phase | `workflow_runs.current_phase` |
| Started | `workflow_runs.started_at` |
| Duration | computed from started_at to completed_at or now |
| Requested by | `workflow_runs.requested_by` → user email |
| Error | `workflow_runs.error_message` (truncated) |

Filters: status, kind, scope, date range. Default: last 24h, most recent first.

#### 2. Workflow detail (drill-down)

- Full input/output JSON
- Step-by-step event timeline from `workflow_events`
- Each event shows: timestamp, phase, level (info/warn/error), message, details
- Error details with full error code and message
- Operator actions: cancel, retry (future)

#### 3. Machine workflow tab

On the existing machine detail page, add a "Workflows" tab showing all workflows scoped to that machine. This gives context when debugging a specific machine.

### API endpoints (already exist)

- `GET /api/workflows/{id}` — single workflow
- `GET /api/workflows/{id}/events` — event timeline
- `GET /api/machines/{id}/workflows` — workflows for a machine

### API endpoints (needed)

- `GET /api/accounts/{slug}/workflows?status=&kind=&limit=` — list workflows for an account (the main dashboard query)
- `POST /api/workflows/{id}/cancel` — cancel a running workflow
- `POST /api/workflows/{id}/retry` — re-enqueue a failed workflow with the same input

### Audit trail

The combination of `workflow_runs` and `workflow_events` provides a complete audit trail:

- **Who** triggered the workflow (`requested_by`)
- **What** was requested (`input_json`, `kind`)
- **When** each phase started and completed (`workflow_events.created_at`)
- **How** it ended (`status`, `error_code`, `error_message`, `output_json`)
- **Where** it ran (`scope_type`, `scope_id`)

This audit trail is automatic — every workflow gets it by virtue of using the substrate. No per-workflow audit code needed.

## Operator and UI Surface

### New APIs

- `POST /api/admin/migrations`
- `GET /api/workflows/{id}`
- `GET /api/workflows/{id}/events`
- `GET /api/machines/{id}/workflows`
- `POST /api/workflows/{id}/cancel`
- `POST /api/workflows/{id}/resume`

Potential future:

- `POST /api/workflows/{id}/fork`

### Dashboard

The dashboard should show:

- current workflow status
- current phase
- step timeline
- terminal error, if any
- operator actions when allowed

The migration UI should stop waiting for a single long HTTP response. It should start a workflow and then observe it.

## Deployment Model

### Why not Cloud Run for workflow execution

Cloud Run's default request-based billing throttles CPU to zero between requests. DBOS executor goroutines run as background work, not inside request handlers. This means:

- Workflow steps stall or die when no HTTP requests are active
- Adding `--no-cpu-throttling` fixes it but costs ~$65/month for always-on CPU on the API server
- The API server doesn't need always-on CPU — it's a low-traffic control plane

Running workflows inside Cloud Run is architecturally wrong for this workload.

### Recommended: Spot worker fleet

Use a small fleet of GCE spot (preemptible) instances dedicated to workflow execution.

```
                                    ┌──────────────────────┐
                                    │  Cloud Run (API)     │
                                    │  - HTTP handlers     │
                                    │  - enqueue workflows │
                                    │  - serve status      │
                                    │  - NO executor       │
                                    └──────────┬───────────┘
                                               │
                                               │ dbos.RunWorkflow()
                                               │ (writes to Postgres)
                                               │
                                    ┌──────────▼───────────┐
                                    │     Postgres (Neon)   │
                                    │  - dbos_* tables      │
                                    │  - workflow_runs      │
                                    │  - workflow_events    │
                                    │  - workflow_locks     │
                                    └──────────┬───────────┘
                                               │
                              ┌────────────────┼────────────────┐
                              │                                 │
                   ┌──────────▼───────────┐          ┌──────────▼───────────┐
                   │  Spot Worker A       │          │  Spot Worker B       │
                   │  e2-small (~$2.50/mo)│          │  e2-small (~$2.50/mo)│
                   │                      │          │                      │
                   │  DBOS executor       │          │  DBOS executor       │
                   │  - machine-lifecycle │          │  - machine-lifecycle │
                   │  - notifications     │          │  - notifications     │
                   │  - host-maintenance  │          │  - host-maintenance  │
                   │  - reconcile         │          │  - reconcile         │
                   │                      │          │                      │
                   │  Unique executor ID  │          │  Unique executor ID  │
                   │  Preemption handler  │          │  Preemption handler  │
                   └──────────────────────┘          └──────────────────────┘
```

**Cost:** ~$5/month total for two spot workers vs. ~$65/month for always-on Cloud Run CPU.

### Same binary, different mode

The backend binary supports two modes:

```bash
# API mode (Cloud Run) — HTTP server, no DBOS executor
ocm-backend --mode=api

# Worker mode (spot instances) — DBOS executor, no HTTP server
ocm-backend --mode=worker
```

Both modes connect to the same Postgres database. The API server enqueues workflows; workers execute them.

### Worker fleet configuration

```yaml
# GCE Managed Instance Group
instance_template:
  machine_type: e2-small          # 2 vCPU, 2GB RAM — shared-core
  provisioning_model: SPOT        # 60-91% cheaper than on-demand
  maintenance_policy: TERMINATE   # spot behavior

managed_instance_group:
  target_size: 2                  # two workers for redundancy
  auto_healing:
    health_check: /healthz        # lightweight TCP check
    initial_delay: 60s
```

### Worker startup sequence

```
Worker VM starts
  │
  1. Read executor ID from instance metadata
  │   (e.g., "worker-{zone}-{instance-id}")
  │
  2. Connect to Postgres (same Neon database as API)
  │
  3. workflows.NewService(ctx, db, Config{
  │     AppName:       "openclawmachines-worker",
  │     DatabaseURL:   cfg.DatabaseURL,
  │     EnableRuntime: true,
  │     Register:      srv.RegisterWorkflows,
  │   })
  │   │
  │   ├── dbos.NewDBOSContext(...)
  │   ├── dbos.NewWorkflowQueue(...)  ×5
  │   ├── Register all workflow functions
  │   └── dbos.Launch(...)
  │       └── Scans for incomplete workflows → resumes them
  │
  4. Listen for preemption signal (metadata endpoint)
  │
  5. Block until shutdown signal
```

### What happens when a worker dies

#### Scenario 1: Spot preemption (30-second warning)

```
GCE                     Worker A                DBOS / Postgres         Worker B
  │                        │                         │                      │
  │  Preemption notice     │                         │                      │
  │  (metadata endpoint)   │                         │                      │
  │───────────────────────>│                         │                      │
  │                        │                         │                      │
  │                        │  dbos.Shutdown(25s)     │                      │
  │                        │  - finish current step  │                      │
  │                        │  - checkpoint result    │                      │
  │                        │────────────────────────>│                      │
  │                        │                         │                      │
  │  VM terminated         │                         │                      │
  │───────────────────────>│                         │                      │
  │                        ×                         │                      │
  │                                                  │                      │
  │  MIG recreates VM      │                         │                      │
  │  (new Worker A)        │                         │                      │
  │                        │                         │                      │
  │                        │                         │  Meanwhile:          │
  │                        │                         │  Worker B picks up   │
  │                        │                         │  orphaned workflows  │
  │                        │                         │──────────────────────>
  │                        │                         │  Resume from last    │
  │                        │                         │  completed step      │
```

**Result:** Workflow pauses for seconds. Worker B resumes it, or new Worker A picks it up after recreation.

#### Scenario 2: Worker crash (OOM, panic, no warning)

```
Worker A                DBOS / Postgres         Worker B
  │                         │                      │
  │  Running step           │                      │
  │  "backup_source"        │                      │
  │                         │                      │
  ×  (OOM kill / panic)     │                      │
                            │                      │
                            │  Step NOT checkpointed│
                            │  (was still in progress)│
                            │                      │
                            │  DBOS detects executor│
                            │  missed heartbeat     │
                            │  (configurable timeout)│
                            │                      │
                            │  Worker B recovery:   │
                            │  - replay completed   │
                            │    steps from DB      │
                            │  - re-execute         │
                            │    "backup_source"    │
                            │    (NOT checkpointed) │
                            │──────────────────────>│
                            │                      │
                            │  ⚠ This is why steps │
                            │  must be idempotent:  │
                            │  backup may already   │
                            │  exist in GCS         │
```

**Result:** Workflow resumes from last completed step. The in-flight step re-executes. Idempotency guards prevent duplicates.

#### Scenario 3: Both workers die simultaneously

```
Worker A     Worker B     MIG                  Postgres
  ×            ×           │                      │
                           │                      │
                           │  Both unhealthy      │  Workflows stay in
                           │  Auto-heal triggers  │  last recorded status
                           │                      │  (running/waiting)
                           │                      │
                           │  Recreate Worker A   │  No data loss.
                           │  Recreate Worker B   │  No spin.
                           │  (~60-90s)           │  Nothing happens
                           │                      │  until a worker
                           │                      │  comes back.
                           │                      │
     New A starts          │                      │
       │                   │                      │
       │  dbos.Launch()    │                      │
       │  Scan incomplete  │                      │
       │  workflows        │                      │
       │──────────────────────────────────────────>│
       │  Resume all       │                      │
       │  orphaned work    │                      │
```

**Result:** All workflows pause for ~60-90 seconds while MIG recreates instances. No data loss. Resume is automatic.

#### Scenario 4: Worker dies during destructive step

```
Worker A                     Agent (source host)         Postgres
  │                               │                         │
  │  RunAsStep("destroy_src")     │                         │
  │  agentClient.DestroyVM()      │                         │
  │──────────────────────────────>│                         │
  │                               │  VM destroyed           │
  ×  (crash before checkpoint)    │                         │
                                  │                         │
  ═══ Recovery on Worker B ═══════════════════════════════
                                  │                         │
  │  Re-execute "destroy_src"     │                         │
  │  agentClient.DestroyVM()      │                         │
  │──────────────────────────────>│                         │
  │                               │  404: VM not found      │
  │                               │  (already destroyed)    │
  │  Treat 404 as success ✓       │                         │
  │  Checkpoint step result       │                         │
  │──────────────────────────────────────────────────────>│
```

**Key rule:** Destructive steps must treat "resource already gone" as success, not as an error. This is the idempotency guard for destructive operations.

### Monitoring and stall detection

Workers must be monitored to detect stalled workflows. A workflow is considered stalled when:

- Status is `running` or `waiting` but no `workflow_events` have been emitted for longer than a threshold
- The owning executor has not renewed its heartbeat

#### Health signals

| Signal | Source | Alert threshold |
|--------|--------|----------------|
| Worker alive | MIG health check (`/healthz`) | Auto-healed by MIG |
| Executor heartbeat | DBOS internal (Postgres) | Configurable in DBOS |
| Workflow progress | `workflow_events.created_at` | No event for >10 min on `running` workflow |
| Step duration | Time since last event | Step-specific (backup: 30min, email: 5min) |
| Queue depth | Count of `queued` workflows | >10 queued for >5 min |

#### Dashboard health indicators

The workflow dashboard should surface:

- **Active workers:** count of live executor instances
- **Queue depth per queue:** how many workflows are waiting
- **Stalled workflows:** running workflows with no recent events
- **Failed workflows:** recent failures with error details
- **Recovery events:** workflows that were resumed after worker death (indicates preemption frequency)

### Deploy and version management

Deploying a new version of the worker binary requires draining in-flight workflows. DBOS only recovers workflows that match the current `ApplicationVersion`.

```
Deploy sequence:
  1. Build new worker image with version N+1
  2. Drain: send SIGTERM to workers, wait for dbos.Shutdown()
     (active workflows checkpoint their current step)
  3. Update instance template with new image
  4. Rolling restart via MIG
  5. New workers start with version N+1
  6. DBOS scans for incomplete workflows with version N+1
     → resumes from last checkpoint

  ⚠ Workflows started with version N that were checkpointed
    mid-flight will NOT auto-resume on version N+1.
    Options:
    a) Pin ApplicationVersion to a stable value (e.g., "1")
       instead of the binary version — simplest
    b) Use DBOS version recovery API to explicitly adopt
       orphaned workflows from version N
    c) Drain all workflows before deploy (wait for completion)
```

**Recommendation:** Pin `ApplicationVersion` to a stable value (option a). The binary version changes on every deploy, but the workflow step contract rarely changes. Only bump the app version when workflow step signatures actually change.

### Cost comparison

| Option | Instances | Monthly cost | Pros | Cons |
|--------|-----------|-------------|------|------|
| Cloud Run request-based (current) | — | ~$5-10 | Cheapest | Workflows broken (CPU throttled) |
| Cloud Run `--no-cpu-throttling` | — | ~$65 | Simple fix | Overpaying for idle API CPU |
| **Spot worker fleet (start here)** | **2× e2-small** | **~$5** | Cheapest, dedicated, crash-resilient | Preemption requires idempotent steps, 2 instances for redundancy |
| On-demand worker (scale-up target) | 1× e2-small | ~$12 | No preemption, simpler (1 instance) | Slightly more expensive than spot |

**Growth path:** Start with 2× spot at ~$5/month. When traffic justifies it, switch to 1× on-demand at ~$12/month — no preemption handling needed, simpler ops, and only $7/month more. On-demand needs only one instance because the redundancy was there to cover spot preemption, not load.

## Future Workflows On The Same Infrastructure

After migration, the next good candidates are:

1. **notification delivery** (invitation emails, completion alerts, webhooks) — proves the substrate works beyond machine ops, low risk, high visibility
2. machine provisioning
3. backup creation
4. backup restore
5. host drain / evacuation
6. plugin and skill installation
7. host provisioning
8. host upgrades
9. rootfs / OpenClaw system upgrades
10. approval workflows
11. config reconcile / repair

These all share the same core needs:

- durability
- visibility
- retries
- explicit ownership
- resource locking

## Phased Rollout

### Phase 1: Scaffold — migration workflow

Phase 1 is explicitly a scaffold. Migration and backup workflows will use shortcuts that are acceptable for two workflows but must not become the permanent pattern:

- add DBOS runtime to backend
- add `workflow_runs`, `workflow_events`, `workflow_locks`
- add workflow API endpoints
- implement migration workflow end-to-end
- stop using synchronous HTTP migration orchestration

Known scaffolding shortcuts accepted in Phase 1:

- workflow step definitions live in the API package (`api/admin_migrate_workflow.go`)
- `MachineOperation` coexists with workflow locks as a parallel exclusivity mechanism
- poll loops inside DBOS steps for waiting on machine state

### Phase 1b: Hardening — worker fleet + retry policy + dashboard + notifications

Phase 1b hardens the scaffold before adding more workflow kinds. These are prerequisites for production confidence:

- **Deploy spot worker fleet** — 2× `e2-small` spot instances in GCE managed instance group, `--mode=worker`, preemption handler with graceful shutdown
- **Split binary modes** — `--mode=api` (Cloud Run, no executor) and `--mode=worker` (spot instances, no HTTP server)
- **Pin `ApplicationVersion`** to a stable value instead of binary version, to avoid stranding workflows on deploy
- **Add step-level retry policies** to all migration workflow steps using DBOS `WithStepMaxRetries` / `WithBackoffFactor` / `WithBaseInterval`
- **Classify errors** as retryable vs terminal in each step (network timeouts retry, 404s don't)
- **Add idempotency guards** to non-idempotent steps (check VM exists before creating, check backup exists before creating)
- **Build workflow dashboard** — list view with status/kind/scope filters, detail view with event timeline, worker health indicators
- **Add account-scoped workflow list endpoint** (`GET /api/accounts/{slug}/workflows`)
- **Add `notifications` queue** and `send_notification` workflow kind
- **Implement invitation email workflow** — fire-and-forget from `handleCreateInvitation`, durable retry on delivery failure
- **Add stall detection** — alert on running workflows with no events for >10 minutes

### Phase 2: Backup workflow + refactor to core

Phase 2 is the refactoring trigger. The backup workflow must not copy Phase 1's shortcuts. Instead, building the second workflow is the forcing function to extract the real abstractions:

- extract workflow steps into domain packages (`workflows/migration/`, `workflows/backup/`)
- unify exclusivity: `MachineOperation` becomes a projection of the workflow lock, not a parallel system
- replace poll-loop steps with durable sleep + re-check patterns
- backup workflow
- restore workflow
- add notification workflows for backup/restore completion

### Phase 3: Move adjacent lifecycle operations

By Phase 3 the substrate is stable and new workflows follow the established pattern:

- provisioning workflow
- host drain / evacuation workflow
- host provisioning workflow
- expand notification workflows (member events, machine alerts)

### Phase 4: Platform-wide workflow adoption

- plugin and skill install workflows
- host upgrade workflows
- system upgrade workflows
- approval workflows
- scheduled repair and reconciliation workflows
- webhook delivery workflows
- worker-service split if needed

## Risks

### 1. Treating the framework as the architecture

DBOS is the execution engine, not the product model. If we expose DBOS internals directly and skip OCM's own projection layer, the system will be harder to evolve.

### 2. Non-idempotent legacy steps

If existing runtime and backup logic are not made idempotent, durable execution will amplify ambiguity instead of fixing it.

### 3. Hidden side effects in observers

Any poller or reconciler that still does destructive cleanup without clear ownership will undermine workflow correctness.

### 4. Half-adopting workflows

The worst outcome is a hybrid where handlers still own orchestration but also create workflow records. The request path must become thin, or the architecture does not improve much.

### 5. Scaffolding shortcuts calcifying

The Phase 1 implementation accepts three shortcuts (steps in API package, parallel locking, poll loops in steps). If these patterns are copied into the backup workflow and beyond, they become the de facto architecture. Phase 2 must refactor them — not just add a second workflow on top of the same patterns.

### 6. Enqueue atomicity gap

The current scaffold creates a `MachineOperation`, a `workflow_runs` row, and a DBOS enqueue as three non-atomic steps. A crash between row creation and DBOS enqueue leaves a stuck `queued` row with no driver. Mitigation: stall detection should flag `queued` workflows with no corresponding DBOS workflow entry, and operators can cancel + re-trigger them. A naive wall-clock reaper (e.g., "expire after 5 minutes") is too aggressive — queue rate limits, partitions, and version drains can legitimately delay pickup.

### 7. Spot worker preemption during destructive steps

If a worker is preempted mid-way through a destructive step (e.g., `DestroyVM`), the step is not checkpointed. On recovery, DBOS re-executes the step. All destructive steps must treat "resource already gone" as success (e.g., agent returns 404 for an already-destroyed VM). Without this idempotency guard, recovery creates confusing errors.

### 8. Sensitive data in workflow projections

Workflow `input_json` and `output_json` may contain invitation tokens, email addresses, and eventually secret rotation data. The dashboard must not display these without a redaction policy. Options: (a) strip sensitive fields before storing in projection tables, (b) apply a field allowlist at API response time, (c) mark workflows as containing sensitive data and restrict dashboard access.

## Recommendation

Proceed with a durable workflow infrastructure now.

Use:

- **DBOS Go** for durable execution
- **OCM-owned workflow projection tables** for product APIs and UI
- **generic workflow locks** for exclusivity
- **machine migration** as workflow #1

This is one of the highest-leverage infrastructure changes available to the platform because it does not just fix migration. It establishes the correct foundation for every future long-running control-plane operation.
