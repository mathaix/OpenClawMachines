# PR1: Store Split & Handler Delegation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract handlers from the 2,761-line `server.go` into domain-specific files, replace direct `s.store.XXX` calls with service-layer calls where appropriate, and remove the legacy `EventRepo` in favor of the unified `ActivityRepo`.

**Architecture:** The 23 repo interfaces already exist in `store.go`. `RuntimeService` and `PlacementService` already use narrow store interfaces. The work is: (1) move handlers out of server.go into domain files, (2) migrate legacy EventRepo callers to ActivityRepo, (3) remove dead event code, (4) verify all tests pass after each task.

**Tech Stack:** Go 1.25, Chi router, pgx/v5, PostgreSQL

---

## File Structure

### Files to Create
- `backend/internal/api/accounts.go` — account CRUD handlers (extracted from server.go)
- `backend/internal/api/machines.go` — machine CRUD + lifecycle handlers (extracted from server.go)
- `backend/internal/api/auth_handlers.go` — auth handlers: me, cli-token, session-exchange, logout (extracted from server.go)
- `backend/internal/api/admin_hosts.go` — admin host management handlers (extracted from server.go)
- `backend/internal/api/admin_machines.go` — admin machine handlers: reset, start, stop, list (extracted from server.go)
- `backend/internal/api/activity.go` — activity log handlers (extracted from server.go)
- `backend/internal/api/agent_heartbeat.go` — agent heartbeat + shutdown handlers (extracted from server.go)

### Files to Modify
- `backend/internal/api/server.go` — shrink to: struct, constructor, router setup, middleware, helpers
- `backend/internal/api/members.go` — migrate from `CreateAccountEvent` to `activity.Log`
- `backend/internal/api/invitations.go` — migrate from `CreateAccountEvent` to `activity.Log`
- `backend/internal/reconciler/host.go` — migrate from `CreateHostEvent` to `activity.Log`
- `backend/internal/progress/progress.go` — migrate from `CreateMachineEvent` to `activity.Log`
- `backend/internal/machines/runtime.go` — remove `store.EventRepo` from `RuntimeStore` interface
- `backend/internal/store/store.go` — remove `EventRepo` interface and event types
- `backend/internal/store/postgres.go` — remove event table implementations

### Test Files to Modify
- `backend/internal/machines/runtime_test.go` — remove `EventRepo` mock methods
- `backend/internal/api/members_test.go` — update mock to use `ActivityRepo`
- `backend/internal/api/invitations_test.go` — update mock to use `ActivityRepo`
- `backend/internal/reconciler/host_test.go` — remove `EventRepo` mock methods

---

## Task 1: Extract Account Handlers

**Files:**
- Create: `backend/internal/api/accounts.go`
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Create `accounts.go` with account handlers**

Move these functions from `server.go` to `accounts.go`:
- `handleListAccounts` (server.go:750)
- `handleCreateAccount` (server.go:760)
- `handleGetAccount` (server.go:844)
- `handleUpdateAccount` (server.go:854)

The file needs `package api` and imports. All handlers are `(s *Server)` methods — they move as-is with no signature changes.

```go
// backend/internal/api/accounts.go
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/mathaix/openclawmachines/backend/internal/auth"
)
```

Then paste the 4 handler functions exactly as they appear in server.go.

- [ ] **Step 2: Remove the 4 functions from server.go**

Delete the `handleListAccounts`, `handleCreateAccount`, `handleGetAccount`, and `handleUpdateAccount` function bodies from server.go. Do NOT change the router wiring — the routes in `NewServer` still reference `srv.handleListAccounts` etc. and will resolve because they're in the same package.

- [ ] **Step 3: Verify build and tests**

Run:
```bash
cd backend && go build ./... && go test ./internal/api/... -count=1
```
Expected: all pass, no compile errors.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/accounts.go backend/internal/api/server.go
git commit -m "refactor: extract account handlers from server.go into accounts.go"
```

---

## Task 2: Extract Auth Handlers

**Files:**
- Create: `backend/internal/api/auth_handlers.go`
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Create `auth_handlers.go` with auth handlers**

Move these functions from `server.go`:
- `handleAuthMe` (server.go:1640)
- `handleCliToken` (server.go:1734)
- `handleSessionExchange` (server.go:1778)
- `handleLogout` (server.go:1905)
- `userResolverMiddleware` (find in server.go)

The file needs `package api` and the imports these functions use (auth, store, net/http, etc.).

- [ ] **Step 2: Remove the functions from server.go**

Delete the moved function bodies from server.go. Route wiring stays.

- [ ] **Step 3: Verify build and tests**

Run:
```bash
cd backend && go build ./... && go test ./internal/api/... -count=1
```

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/auth_handlers.go backend/internal/api/server.go
git commit -m "refactor: extract auth handlers from server.go into auth_handlers.go"
```

---

## Task 3: Extract Machine CRUD & Lifecycle Handlers

**Files:**
- Create: `backend/internal/api/machines.go`
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Create `machines.go` with machine handlers**

Move these functions from `server.go`:
- `handleListMachines` (server.go:912)
- `handleCreateMachine` (server.go:922)
- `handleGetMachine` (server.go:1086)
- `handleUpdateMachine` (server.go:1104)
- `handleDeleteMachine` (server.go:1195)
- `handleStartMachine` (server.go:1230)
- `handleStopMachine` (server.go:1295)
- `handleGetMachineToken` (server.go:1551)
- `handleRollbackMachine` (server.go:1597)
- `handleSetCDPTarget` (server.go:1479)
- `handleResetCDPTarget` (server.go:1521)

Also move any helper functions only used by these handlers (e.g. `startMachineInternal` if it exists, `backfillBackupKeys`).

- [ ] **Step 2: Remove the functions from server.go**

- [ ] **Step 3: Verify build and tests**

Run:
```bash
cd backend && go build ./... && go test ./internal/api/... -count=1
```

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/machines.go backend/internal/api/server.go
git commit -m "refactor: extract machine handlers from server.go into machines.go"
```

---

## Task 4: Extract Activity Handlers

**Files:**
- Create: `backend/internal/api/activity.go`
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Create `activity.go` with activity handlers**

Move these functions from `server.go`:
- `handleListMachineActivity` (server.go:1342)
- `handleListAccountActivity` (server.go:1377)
- `handleAdminListEvents` (server.go:1402)
- `parseActivityFilter` (server.go:1447)

- [ ] **Step 2: Remove the functions from server.go**

- [ ] **Step 3: Verify build and tests**

Run:
```bash
cd backend && go build ./... && go test ./internal/api/... -count=1
```

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/activity.go backend/internal/api/server.go
git commit -m "refactor: extract activity handlers from server.go into activity.go"
```

---

## Task 5: Extract Admin Host Handlers

**Files:**
- Create: `backend/internal/api/admin_hosts.go`
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Create `admin_hosts.go` with host management handlers**

Move these functions from `server.go`:
- `handleProvisionHost` (server.go:1921)
- `handleListHosts` (server.go:1979)
- `handleDestroyHost` (server.go:1988)
- `handleListHostMachines` (server.go:2158)
- `handleHostVMStats` (server.go:2202)
- `handleRefreshRootfs` (server.go:2224)
- `handleTriggerHostUpdate` (server.go:2258)
- `handleHostLogs` (server.go:2404)
- `handleLatestVersions` (if in server.go)

- [ ] **Step 2: Remove the functions from server.go**

- [ ] **Step 3: Verify build and tests**

Run:
```bash
cd backend && go build ./... && go test ./internal/api/... -count=1
```

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/admin_hosts.go backend/internal/api/server.go
git commit -m "refactor: extract admin host handlers from server.go into admin_hosts.go"
```

---

## Task 6: Extract Admin Machine Handlers

**Files:**
- Create: `backend/internal/api/admin_machines.go`
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Create `admin_machines.go` with admin machine handlers**

Move these functions from `server.go`:
- `handleAdminResetMachine` (server.go:2031)
- `handleAdminStartMachine` (server.go:2090)
- `handleAdminStopMachine` (server.go:2127)
- `handleAdminListMachines` (server.go:2149)

- [ ] **Step 2: Remove the functions from server.go**

- [ ] **Step 3: Verify build and tests**

Run:
```bash
cd backend && go build ./... && go test ./internal/api/... -count=1
```

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/admin_machines.go backend/internal/api/server.go
git commit -m "refactor: extract admin machine handlers from server.go into admin_machines.go"
```

---

## Task 7: Extract Agent Heartbeat & Shutdown Handlers

**Files:**
- Create: `backend/internal/api/agent_heartbeat.go`
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Create `agent_heartbeat.go` with agent lifecycle handlers**

Move these functions from `server.go`:
- `handleAgentHeartbeat` (server.go:2539)
- `handleAgentShutdownNotify` (server.go:2687)
- `handleInternalResolve` (server.go:2457)

These are large handlers (~150-200 lines each). Move with all helper functions they reference that are only used by them.

- [ ] **Step 2: Remove the functions from server.go**

- [ ] **Step 3: Verify build and tests**

Run:
```bash
cd backend && go build ./... && go test ./internal/api/... -count=1
```

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/agent_heartbeat.go backend/internal/api/server.go
git commit -m "refactor: extract agent heartbeat handlers from server.go into agent_heartbeat.go"
```

---

## Task 8: Migrate Legacy EventRepo Callers to ActivityRepo

The old `EventRepo` (`CreateHostEvent`, `CreateAccountEvent`, `CreateMachineEvent`) writes to legacy event tables. The new `ActivityRepo` via `events.Activity` is the replacement. Migrate all callers.

**Files:**
- Modify: `backend/internal/api/members.go`
- Modify: `backend/internal/api/invitations.go`
- Modify: `backend/internal/api/agent_heartbeat.go` (or server.go if not yet extracted)
- Modify: `backend/internal/reconciler/host.go`
- Modify: `backend/internal/progress/progress.go`

- [ ] **Step 1: Migrate `members.go` from CreateAccountEvent to activity.Log**

In `members.go`, find the `CreateAccountEvent` call (around line 121) and replace with `s.activity.Log`. The activity logger is already on the Server struct.

Before:
```go
_ = s.store.CreateAccountEvent(r.Context(), &store.AccountEvent{
    AccountID: accountID,
    Type:      "member_removed",
    Detail:    fmt.Sprintf("..."),
})
```

After:
```go
s.activity.Log(r.Context(), events.LogParams{
    Category:  "account",
    Action:    "account.member_removed",
    Status:    "success",
    ActorType: "user",
    ActorID:   &claims.UserID,
    AccountID: &accountID,
    Summary:   fmt.Sprintf("..."),
})
```

- [ ] **Step 2: Migrate `invitations.go` from CreateAccountEvent to activity.Log**

Find the `CreateAccountEvent` call in invitations.go and replace similarly.

- [ ] **Step 3: Migrate agent heartbeat handlers from CreateHostEvent to activity.Log**

The heartbeat handler has multiple `CreateHostEvent` calls. Replace each with `s.activity.Log` using category `"host"`.

- [ ] **Step 4: Migrate `reconciler/host.go` from CreateHostEvent to activity.Log**

The reconciler creates host events for IP changes and recovery. It needs an `*events.Activity` field added to its struct and wired in `cmd/server/main.go`.

In `reconciler/host.go`:
```go
type HostReconciler struct {
    store    ReconcilerStore
    agent    *agentclient.Client
    activity *events.Activity  // add this
}
```

Replace `CreateHostEvent` calls with `r.activity.Log(...)`.

The reconciler already has `SetActivity` from commit `4472804` — verify it's wired.

- [ ] **Step 5: Migrate `progress/progress.go` from CreateMachineEvent to activity.Log**

The progress tracker uses `CreateMachineEvent` to log provisioning steps. Replace with activity logging.

- [ ] **Step 6: Verify build and tests**

Run:
```bash
cd backend && go build ./... && go test ./... -count=1
```

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "refactor: migrate legacy EventRepo callers to unified ActivityRepo"
```

---

## Task 9: Remove Legacy EventRepo Interface and Implementations

Now that no code calls `EventRepo` methods, remove them.

**Files:**
- Modify: `backend/internal/store/store.go` — remove `EventRepo` interface, `HostEvent`/`AccountEvent`/`MachineEvent` types
- Modify: `backend/internal/store/postgres.go` — remove `CreateHostEvent`, `ListHostEvents`, `CreateAccountEvent`, `ListAccountEvents`, `CreateMachineEvent`, `ListMachineEvents` implementations
- Modify: `backend/internal/machines/runtime.go` — remove `store.EventRepo` from `RuntimeStore` interface
- Modify: `backend/internal/machines/runtime_test.go` — remove `EventRepo` mock methods
- Modify: `backend/internal/reconciler/host.go` — remove `store.EventRepo` from `ReconcilerStore` interface
- Modify: `backend/internal/reconciler/host_test.go` — remove `EventRepo` mock methods
- Modify: `backend/internal/api/members_test.go` — remove `EventRepo` mock methods
- Modify: `backend/internal/api/invitations_test.go` — remove `EventRepo` mock methods
- Modify: `backend/internal/progress/progress.go` — remove `EventRepo` from store interface

- [ ] **Step 1: Remove `EventRepo` from store.go**

Delete the `EventRepo` interface and the `HostEvent`, `AccountEvent`, `MachineEvent` struct types from `store.go`. Remove `EventRepo` from the aggregate `Store` interface embedding list.

- [ ] **Step 2: Remove EventRepo implementations from postgres.go**

Delete the 6 methods: `CreateHostEvent`, `ListHostEvents`, `CreateAccountEvent`, `ListAccountEvents`, `CreateMachineEvent`, `ListMachineEvents`.

- [ ] **Step 3: Remove EventRepo from RuntimeStore, ReconcilerStore, and progress store interfaces**

In `runtime.go`, remove `store.EventRepo` from the `RuntimeStore` interface.
In `reconciler/host.go`, remove `store.EventRepo` from `ReconcilerStore`.
In `progress/progress.go`, remove `store.EventRepo` from whatever store interface it uses.

- [ ] **Step 4: Remove EventRepo mock methods from all test files**

Remove the `CreateHostEvent`, `ListHostEvents`, `CreateAccountEvent`, `ListAccountEvents`, `CreateMachineEvent`, `ListMachineEvents` methods from mock structs in:
- `runtime_test.go`
- `host_test.go` (reconciler)
- `members_test.go`
- `invitations_test.go`

- [ ] **Step 5: Verify build and tests**

Run:
```bash
cd backend && go build ./... && go test ./... -count=1
```

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor: remove legacy EventRepo interface and implementations"
```

---

## Task 10: Final Verification and server.go Audit

- [ ] **Step 1: Measure server.go size**

Run:
```bash
wc -l backend/internal/api/server.go
```

Expected: significantly smaller than 2,761 lines. Should contain only: struct definition, constructors, router setup, middleware, and utility helpers.

- [ ] **Step 2: Verify no handler functions remain in server.go**

Run:
```bash
grep -c '^func (s \*Server) handle' backend/internal/api/server.go
```

Expected: 1 (only `handleHealth` should remain, as it's a trivial 3-line function).

- [ ] **Step 3: Run full test suite**

Run:
```bash
cd backend && go test ./... -count=1
```

Expected: all pass.

- [ ] **Step 4: Run gateway E2E tests**

Run:
```bash
make test-gateway-e2e
```

Expected: all pass.

- [ ] **Step 5: Commit any cleanup**

```bash
git add -A
git commit -m "refactor: PR1 store split — server.go shrunk, handlers delegated, EventRepo removed"
```

---

## Verification Checklist (from PR1 spec)

- [ ] `server.go` no longer contains domain handler logic (only struct, constructor, router, middleware)
- [ ] RuntimeService tests pass (start/stop/delete)
- [ ] Handler API parity tests pass (existing tests still work after extraction)
- [ ] Legacy event tables confirmed replaced by ActivityRepo — EventRepo removed
- [ ] No change to DB schema or API shapes
- [ ] `go vet ./...` and `go test ./...` pass
