# Durable Workflow Infrastructure Implementation Plan

**Status:** Proposed
**Date:** 2026-03-13
**Design:** `docs/designs/durable-workflows.md`
**Goal:** Introduce durable workflow infrastructure into OCM, using admin machine migration as workflow #1 while designing the system for future long-running jobs.

## Summary

This plan implements a durable workflow layer in the control plane with these constraints:

- migration is the first workflow
- the infrastructure must be reusable for provisioning, backup, restore, installs, and approvals later
- the implementation should be incremental
- the current system must remain operable during rollout

The plan is intentionally **not** a rewrite. It introduces a workflow substrate alongside the current lifecycle code, then moves migration onto it first.

## Architectural Direction

### What changes

- long-running orchestration moves out of synchronous HTTP handlers
- workflow status becomes a first-class product/API concept
- resource exclusivity becomes lease-based and workflow-owned
- request handlers become thin "submit + observe" entrypoints

### What stays the same initially

- `RuntimeService`, placement, backup, and agent RPC remain the domain executors
- `machine_operations` remains in place for existing start/stop/delete/backup/restore flows
- current non-workflow handlers continue to work during rollout

### Key implementation principle

Phase 1 adds a **workflow substrate**. Phase 2 moves **migration** onto it. Phase 3 migrates other long-running jobs.

## Deliverables

By the end of this plan, OCM should have:

- DBOS wired into the backend
- durable workflow projection tables in Postgres
- workflow APIs for status and event history
- generic workflow lock/lease infrastructure
- a production migration workflow that no longer relies on one long HTTP request
- dashboard/CLI-visible workflow ids and status

## Workstreams

### Workstream A: Workflow Substrate

Goal: Add the reusable infrastructure with no product behavior change yet.

### Workstream B: Migration Workflow

Goal: Move admin machine migration from `handleAdminMigrateMachine` into a durable workflow.

### Workstream C: Rollout, Observability, and Follow-On Adoption

Goal: Make the new infrastructure safe to operate and ready for more workflows.

---

## Workstream A: Workflow Substrate

### Task A1: Add DBOS Runtime Dependency and Bootstrap

**Files:**
- Modify: `backend/go.mod`
- Modify: `backend/go.sum`
- Modify: `backend/cmd/server/main.go`
- Create: `backend/internal/workflows/runtime.go`
- Create: `backend/internal/workflows/config.go`

- [ ] Add DBOS Go dependency to the backend module.
- [ ] Create a small `workflows` package that encapsulates DBOS initialization.
- [ ] Wire workflow runtime startup in `backend/cmd/server/main.go`.
- [ ] Keep DBOS initialization isolated from API/router code so workers can move out later.

**Notes:**
- Do not scatter DBOS calls directly across handlers.
- `internal/workflows/runtime.go` should be the single runtime integration point.

### Task A2: Add Workflow Projection Schema

**Files:**
- Create: `backend/migrations/034_workflow_runs.sql`
- Modify: `backend/internal/store/store.go`
- Modify: `backend/internal/store/postgres.go`

- [ ] Create `workflow_runs` table.
- [ ] Create `workflow_events` table.
- [ ] Create `workflow_locks` table.
- [ ] Add store types:
  - `WorkflowRun`
  - `WorkflowEvent`
  - `WorkflowLock`
- [ ] Add store methods for:
  - create workflow run
  - update workflow summary/status
  - append workflow event
  - list workflow events
  - acquire/renew/release workflow lock
  - fetch workflow by id
  - list workflows by machine/account

**Suggested API in `store.go`:**

```go
type WorkflowRepo interface {
    CreateWorkflowRun(ctx context.Context, run *WorkflowRun) error
    GetWorkflowRun(ctx context.Context, id string) (*WorkflowRun, error)
    UpdateWorkflowRunStatus(ctx context.Context, id, status, phase string, summary json.RawMessage, errCode, errMsg *string) error
    CompleteWorkflowRun(ctx context.Context, id, status string, output json.RawMessage, errCode, errMsg *string) error
    ListWorkflowRunsByScope(ctx context.Context, scopeType, scopeID string, limit int) ([]WorkflowRun, error)

    CreateWorkflowEvent(ctx context.Context, event *WorkflowEvent) error
    ListWorkflowEvents(ctx context.Context, workflowID string, limit int) ([]WorkflowEvent, error)

    AcquireWorkflowLock(ctx context.Context, lock *WorkflowLock) error
    RenewWorkflowLock(ctx context.Context, workflowID, resourceType, resourceID, lockKind string, leaseUntil time.Time) error
    ReleaseWorkflowLocks(ctx context.Context, workflowID string) error
}
```

### Task A3: Create Workflow Service Layer

**Files:**
- Create: `backend/internal/workflows/service.go`
- Create: `backend/internal/workflows/types.go`
- Create: `backend/internal/workflows/locks.go`
- Create: `backend/internal/workflows/events.go`
- Create: `backend/internal/workflows/service_test.go`

- [ ] Add an OCM-owned workflow service on top of DBOS and Postgres.
- [ ] Define stable workflow kinds:
  - `migration`
  - `provision`
  - `backup`
  - `restore`
  - `install_plugin`
- [ ] Define stable workflow statuses:
  - `queued`
  - `running`
  - `waiting`
  - `completed`
  - `failed`
  - `cancelled`
  - `manual_action_required`
- [ ] Add helpers for:
  - enqueue workflow
  - mark phase start
  - record workflow event
  - complete/fail workflow
  - acquire and renew leases

**Important rule:**
- DBOS execution metadata is not the product API.
- `workflows/service.go` must keep OCM’s status model authoritative.

### Task A4: Add Workflow APIs

**Files:**
- Modify: `backend/internal/api/server.go`
- Create: `backend/internal/api/workflows.go`
- Create: `backend/internal/api/workflows_test.go`

- [ ] Add:
  - `GET /api/workflows/{id}`
  - `GET /api/workflows/{id}/events`
  - `GET /api/accounts/{accountId}/machines/{machineId}/workflows`
- [ ] Return stable workflow projections, not DBOS internals.
- [ ] Enforce account scoping and membership checks.
- [ ] Make response payloads suitable for both dashboard and CLI use.

**Example response:**

```json
{
  "id": "wf_123",
  "kind": "migration",
  "status": "running",
  "current_phase": "start_on_target_host",
  "scope_type": "machine",
  "scope_id": "machine-uuid",
  "summary": {
    "machine_id": "machine-uuid",
    "source_host_id": 10,
    "target_host_id": 22,
    "backup_id": 91
  },
  "created_at": "...",
  "updated_at": "..."
}
```

### Task A5: Add Workflow Logging and Trace Correlation

**Files:**
- Modify: `backend/internal/workflows/*.go`
- Modify: `backend/internal/api/*.go` where workflow submission happens
- Modify: workflow-invoked domain code paths as needed

- [ ] Standardize log fields:
  - `workflow_id`
  - `workflow_kind`
  - `phase`
  - `scope_type`
  - `scope_id`
- [ ] Ensure workflow-created domain actions carry those fields in logs.
- [ ] Emit workflow events for operator-visible state transitions.

### Task A6: Keep `machine_operations` but Narrow Their Scope

**Files:**
- Modify: `backend/internal/machines/runtime.go`
- Modify: `backend/internal/api/machine_backups.go`
- Modify: `backend/internal/reconciler/host.go`

- [ ] Keep `machine_operations` for existing non-workflow paths in Phase 1.
- [ ] Stop treating them as the future long-running orchestration model.
- [ ] Ensure migration no longer depends on stale-op expiry once moved to workflows.
- [ ] Leave start/stop/delete/backup/restore on existing operation semantics until later phases.

**Important:**
- Do not delete `machine_operations` in this plan.
- Migration should stop depending on them for progress tracking.

### Task A7: Normalize Stale-State and Idempotent Terminal Operations

**Files:**
- Modify: `backend/internal/machines/runtime.go`
- Modify: `backend/internal/agentapi/*` where VM destroy errors are returned
- Modify: `backend/internal/agentclient/*` if typed error propagation is needed
- Create or modify: `backend/internal/machines/runtime_test.go`
- Create or modify: `backend/internal/agentapi/*_test.go`

- [ ] Define a typed "VM not found" error contract between agent and backend.
- [ ] Prefer agent-side `404` or sentinel error over generic `500` for destroy of a missing VM.
- [ ] Treat `DestroyVM` / terminal cleanup "already absent" as success in backend lifecycle code.
- [ ] Audit other terminal operations for the same stale-state pattern:
  - stop on already-stopped VM
  - release of already-released placement
  - backup restore target preparation when the prior VM process is already gone
- [ ] Add tests that simulate:
  - agent restarted and lost `o.vms` runtime map
  - Firecracker process already gone
  - backend still has stale placement/host state

**Why this is in Phase 1:**
- durable workflows do not fix stale-state classification bugs by themselves
- migration and future workflows require idempotent step adapters
- "already gone" must be treated as a successful terminal state where appropriate

**Success criterion:**
- delete/destroy paths converge even when the agent's live runtime cache has already forgotten the VM
- workflows can later classify this as `vm_already_absent` and continue safely

---

## Workstream B: Migration Workflow

### Task B0: Migration Prerequisites and Safety Hardening

**Files:**
- Modify: `backend/internal/api/admin_migrate.go`
- Modify: `backend/internal/machines/runtime.go`
- Modify: `backend/internal/store/*` as needed
- Create or modify: migration-related tests

- [ ] Remove any remaining migration assumptions that depend on request lifetime.
- [ ] Ensure migration step adapters can run without hidden handler-owned state.
- [ ] Ensure post-restore start/verify paths are workflow-owned, not poller-owned.
- [ ] Ensure migration cleanup is safe under stale state and partial prior completion.

**This task is complete when:**
- migration logic can be called as pure workflow steps
- no migration step requires a synchronous HTTP handler to remain alive
- no migration step treats "already absent" or "already completed" as an unclassified hard failure

### Task B1: Define Migration Workflow Contract

**Files:**
- Create: `backend/internal/workflows/migration_types.go`
- Create: `backend/internal/workflows/migration_types_test.go`

- [ ] Define `MigrationInput`.
- [ ] Define `MigrationSummary`.
- [ ] Define migration phases as constants.
- [ ] Define retryable vs terminal vs manual-intervention error classes.

**Suggested input:**

```go
type MigrationInput struct {
    MachineID    string `json:"machine_id"`
    TargetHostID int    `json:"target_host_id"`
    Force        bool   `json:"force"`
    RequestedBy  int    `json:"requested_by"`
}
```

### Task B2: Build Migration Step Adapters

**Files:**
- Create: `backend/internal/workflows/migration_steps.go`
- Create: `backend/internal/workflows/migration_steps_test.go`
- Possibly modify:
  - `backend/internal/api/admin_migrate.go`
  - `backend/internal/machines/runtime.go`
  - `backend/internal/store/postgres.go`
  - `backend/internal/backup/*`

- [ ] Extract migration logic into bounded step functions:
  - validate request
  - stop source machine if needed
  - create backup if needed
  - release source placement
  - start on target
  - wait for running
  - stop for restore
  - restore backup
  - restart restored machine
  - verify final running state
- [ ] Make each step callable from workflow code without requiring an HTTP request.
- [ ] Ensure each step is idempotent or explicitly detects prior completion.

**Important design constraint:**
- `pollVMStatus` must not remain the hidden source of truth for migration completion.
- Waiting/verification should be workflow-owned.

### Task B3: Implement Workflow Lease Management For Migration

**Files:**
- Modify: `backend/internal/workflows/locks.go`
- Modify: `backend/internal/workflows/migration_steps.go`
- Create: `backend/internal/workflows/locks_test.go`

- [ ] Acquire a `machine:lifecycle` workflow lock at migration start.
- [ ] Optionally acquire a `host:maintenance` or `host:lifecycle` lock when needed.
- [ ] Renew leases between long-running phases.
- [ ] Release locks on terminal completion, failure, or cancellation.

**This replaces the current architectural dependence on fixed stale-op age.**

### Task B4: Implement `MachineMigrationWorkflow`

**Files:**
- Create: `backend/internal/workflows/migration_workflow.go`
- Create: `backend/internal/workflows/migration_workflow_test.go`

- [ ] Implement the DBOS-backed workflow.
- [ ] Record phase transitions in `workflow_runs.current_phase`.
- [ ] Append `workflow_events` for each important state transition.
- [ ] Map domain failures into:
  - `failed`
  - `manual_action_required`
- [ ] Ensure final output is written to `workflow_runs.output_json`.

### Task B5: Replace Synchronous Migration Endpoint

**Files:**
- Modify: `backend/internal/api/admin_migrate.go`
- Modify: `backend/internal/api/server.go`
- Create or modify: `backend/internal/api/admin_migrate_test.go`

- [ ] Change `POST /api/admin/machines/migrate` to:
  - validate input
  - create/enqueue workflow
  - return `202 Accepted`
- [ ] Response should include:
  - `workflow_id`
  - `status`
  - `machine_id`
  - `target_host_id`
- [ ] Stop performing the migration inline in the handler.

**Example response:**

```json
{
  "workflow_id": "wf_123",
  "status": "queued",
  "machine_id": "machine-uuid",
  "target_host_id": 22
}
```

### Task B6: Add Migration Status UX Contract

**Files:**
- Modify: frontend migration UI files once identified
- Possibly create:
  - `frontend/src/lib/workflows.ts`
  - `frontend/src/components/WorkflowTimeline.tsx`

- [ ] Update the admin migration UI to observe workflow status instead of waiting on one request.
- [ ] Show:
  - current phase
  - source host
  - target host
  - backup id when available
  - terminal error if any
- [ ] Allow refresh/re-entry by `workflow_id`.

**Phase 1 minimum:**
- polling is acceptable
- websockets/SSE are not required yet

### Task B7: Add End-to-End Migration Workflow Tests

**Files:**
- Create or modify:
  - `backend/internal/workflows/migration_workflow_test.go`
  - `backend/internal/api/admin_migrate_test.go`
  - `backend/internal/integration/*`

- [ ] Test successful migration with backup and restore.
- [ ] Test forced migration without backup.
- [ ] Test workflow resume after simulated process interruption.
- [ ] Test lock conflict when another lifecycle action is active.
- [ ] Test `manual_action_required` path for ambiguous failure.

---

## Workstream C: Rollout, Observability, and Follow-On Adoption

### Task C1: Feature-Flag Initial Workflow Execution

**Files:**
- Modify: `backend/internal/config/*` or server config wiring
- Modify: `backend/cmd/server/main.go`

- [ ] Add a feature flag, for example:
  - `ENABLE_DURABLE_WORKFLOWS`
  - `ENABLE_WORKFLOW_MIGRATION`
- [ ] Allow the workflow substrate to ship before migration is fully switched over.
- [ ] Allow migration workflow to be enabled separately from the substrate.

### Task C2: Add Operational Runbook

**Files:**
- Create: `docs/runbook-durable-workflows.md`

- [ ] Document:
  - how workflows are queued
  - how to inspect `workflow_runs`
  - how to inspect `workflow_events`
  - how to identify stuck leases
  - how to recover a failed migration
  - when to mark `manual_action_required`

### Task C3: Add Metrics

**Files:**
- Modify workflow runtime and server metrics exports as applicable

- [ ] Add counters and histograms for:
  - workflow submitted
  - workflow completed
  - workflow failed
  - workflow duration by kind
  - workflow retries by phase
  - lock acquisition failures

### Task C4: Define Next Adopters

**Files:**
- Update: `docs/designs/durable-workflows.md`
- Optionally create follow-on plans

- [ ] After migration is stable, move these workflows next:
  1. backup creation
  2. backup restore
  3. machine provisioning
  4. host evacuation
  5. plugin/skill install

---

## Recommended Implementation Order

1. A1: DBOS bootstrap
2. A2: workflow schema
3. A3: workflow service
4. A4: workflow APIs
5. A5: logging/correlation
6. A6: narrow `machine_operations`
7. A7: stale-state and terminal idempotency hardening
8. B0: migration prerequisites and safety hardening
9. B1: migration contract
10. B2: migration steps
11. B3: workflow locks
12. B4: migration workflow
13. B5: async migration endpoint
14. B6: migration status UI
15. B7: workflow integration tests
16. C1-C3: rollout and ops

## Explicitly Deferred

- replacing all `machine_operations`
- moving start/stop/delete to workflows
- approval workflow implementation
- websockets/SSE for workflow progress
- splitting workers into a separate service

## Risks

### Risk 1: Half-migrated ownership

If the HTTP handler still owns orchestration while a workflow record also exists, the architecture will remain confusing.

**Mitigation:** once migration is switched, the handler must only submit + return.

### Risk 2: Non-idempotent legacy steps

Existing runtime and backup code may assume one-shot execution.

**Mitigation:** explicitly harden step adapters before wiring them into workflow retries/resume paths. The stale-state destroy/delete case is the first concrete example and should be fixed early.

### Risk 3: Hidden destructive observers

Any background poller that performs cleanup without clear ownership can still damage correctness.

**Mitigation:** migration workflow must own waiting and destructive decisions.

### Risk 4: DBOS coupling leaks into product APIs

If OCM exposes DBOS runtime structures directly, future changes will be harder.

**Mitigation:** keep `workflow_runs` and `workflow_events` as the public model.

## Definition of Done

This plan is complete when:

- migrations are started asynchronously and return a `workflow_id`
- migration progress is queryable from API and visible in the UI
- migration survives request cancellation and backend restarts
- migration no longer relies on fixed stale-op expiry to remain valid
- terminal lifecycle operations converge correctly under stale agent/backend state
- workflow logs, events, and status can be correlated reliably
- the substrate is reusable for at least one additional workflow without redesign
