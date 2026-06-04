# Architecture Foundation Design

Date: 2026-03-08
Branch: `thirdparty_provisioning`
Status: Approved

## Overview

Three parallel workstreams that establish the architectural foundation before P0 safety fixes and provider abstraction.

- **A1**: Extract MachineRuntimeService from server.go
- **B1**: Block /port/* from reserved internal ports
- **B3**: Rootfs atomic staging and refresh hardening

## A1: MachineRuntimeService Extraction

### Goal

Move machine start/stop/delete orchestration out of API handlers into a dedicated service. Handlers become thin HTTP translators.

### What moves

1. **Start** — `startMachineInternal` (server.go:758): decrypt secrets, fetch credentials, generate tokens, create tunnel, place machine, call agent, poll status
2. **Stop** — stop handler logic: call agent StopVM, soft-release capacity, delete KV route, optionally clear tunnel
3. **Delete** — delete handler logic: call agent DestroyVM, release capacity, delete KV route, delete tunnel, delete machine record

### Service struct

```go
// backend/internal/machines/runtime.go
type RuntimeService struct {
    store        store.Store
    scheduler    *scheduler.Scheduler
    agentClient  *agentclient.Client
    kvStore      *kvstore.KVStore
    tunnelMgr    *tunnel.Manager
    secrets      *secrets.Manager
    progress     *progress.Tracker
}
```

The service calls the agent client directly (not via interface). Dependencies are injected at construction time.

### Handler pattern after extraction

```go
func (s *Server) handleStartMachine(w http.ResponseWriter, r *http.Request) {
    // parse request, auth, get machine from DB
    host, vmIP, err := s.machines.Start(r.Context(), accountID, machine)
    // write response or error
}
```

### What stays in server.go

- HTTP routing and auth middleware
- Request parsing and response writing
- Server struct holds RuntimeService and delegates

### What NOT to do

- Don't change the Store interface (that's A2)
- Don't change the scheduler (that's A3)
- Don't move machine CRUD (create, update, list) — only runtime lifecycle
- Don't refactor the async VM poller — move it as-is

### Tests

New file: `backend/internal/machines/runtime_test.go`

- Start success path (all dependencies called in order)
- Start rollback when agent CreateVM fails (capacity released, tunnel cleaned up)
- Stop on healthy host (agent called, capacity released, route deleted)
- Delete cleans up all resources (agent, capacity, route, tunnel, DB record)

## B1: Port Denylist

### Goal

Block `/port/{port}` from reaching reserved internal OCM services inside the VM.

### Change

In `backend/cmd/authproxy/main.go`, add a denylist check before proxying `/port/{port}` requests.

Denylist: `22, 80, 7681, 8080, 9090, 9091, 9222, 18789`

```go
var reservedPorts = map[int]bool{
    22: true, 80: true, 7681: true, 8080: true,
    9090: true, 9091: true, 9222: true, 18789: true,
}
```

If the requested port is in the denylist, return `403 Forbidden`.

Dedicated paths (`/gateway/*`, `/terminal/*`) are unaffected — they use their own proxy targets.

### Tests

New tests in `backend/cmd/authproxy/main_test.go`:

- `/port/3000` → proxied (allowed)
- `/port/7681` → 403 (PTY server)
- `/port/18789` → 403 (gateway)
- `/port/9090` → 403 (control API)
- `/gateway/*` still works
- `/terminal/*` still works

## B3: Rootfs Atomic Staging

### Goal

Make rootfs refresh safe against concurrent access and resilient to GCS outages.

### Sub-change 1: Atomic refresh

In the refresh handler (`backend/internal/agentapi/handlers.go`):

1. Write to `<path>.tmp`
2. `os.Rename("<path>.tmp", "<path>")` — atomic on same filesystem
3. Remove `.tmp` on error

### Sub-change 2: File lock

Use `syscall.Flock` on a lockfile (`<rootfs-dir>/.rootfs.lock`):

- Refresh handler acquires exclusive (write) lock
- Orchestrator's reflink copy acquires shared (read) lock
- This serializes refresh vs. VM create without blocking concurrent VM creates

### Sub-change 3: GCS manifest failure resilience

In `backend/internal/rootfs/gcs.go`, when `fetchManifest` fails:

1. Check if previously staged rootfs exists at expected path
2. Check if sidecar manifest exists and file size matches
3. If both valid: log warning, continue with cached version
4. If no cached version: fail (current behavior)

### Tests

- Refresh writes atomically (no partial file on error)
- Manifest failure with valid cached rootfs → continues with warning
- Manifest failure with no cached rootfs → fails
- Concurrent create waits for refresh lock (no corruption)

## Files Changed

### A1
| File | Change |
|------|--------|
| `backend/internal/machines/runtime.go` | **New** — RuntimeService with Start/Stop/Delete |
| `backend/internal/machines/runtime_test.go` | **New** — unit tests with mock dependencies |
| `backend/internal/api/server.go` | Remove orchestration logic, delegate to RuntimeService |
| `backend/cmd/server/main.go` | Construct RuntimeService, pass to Server |

### B1
| File | Change |
|------|--------|
| `backend/cmd/authproxy/main.go` | Add reserved port denylist check |
| `backend/cmd/authproxy/main_test.go` | **New** — port denylist tests |

### B3
| File | Change |
|------|--------|
| `backend/internal/agentapi/handlers.go` | Atomic refresh with temp file + rename |
| `backend/internal/rootfs/gcs.go` | Manifest failure fallback to cached rootfs |
| `backend/internal/orchestrator/firecracker_linux.go` | Shared flock before reflink copy |
| `backend/internal/agentapi/handlers_test.go` | Atomic refresh tests |
| `backend/internal/rootfs/gcs_test.go` | Manifest failure resilience tests |
