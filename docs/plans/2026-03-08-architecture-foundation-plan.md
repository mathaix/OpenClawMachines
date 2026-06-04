# Architecture Foundation Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Extract machine lifecycle into a service, block reserved ports, and harden rootfs staging — establishing the architectural foundation for P0 safety fixes and provider abstraction.

**Architecture:** Three independent workstreams. A1 restructures control-plane machine lifecycle into `backend/internal/machines/`. B1 adds a port denylist in the auth proxy. B3 makes rootfs refresh atomic and resilient to GCS outages.

**Tech Stack:** Go 1.25, Chi router, pgx/v5, Cloudflare API, GCS, Firecracker

---

## Workstream A1: MachineRuntimeService

### Task A1.1: Create RuntimeService Skeleton + Start Test

**Files:**
- Create: `backend/internal/machines/runtime.go`
- Create: `backend/internal/machines/runtime_test.go`

**Step 1: Write the failing test**

Create `backend/internal/machines/runtime_test.go`:

```go
package machines

import (
	"context"
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type mockDeps struct {
	startVMCalled bool
	startVMErr    error
	placedHost    *store.Host
	placedVMIP    string
	placeErr      error
	secrets       []store.Secret
}

func TestStart_Success(t *testing.T) {
	// Verify Start calls dependencies in order and returns host+vmIP
	t.Skip("RuntimeService not implemented yet")
}
```

**Step 2: Run test to verify it compiles but skips**

Run: `cd backend && go test ./internal/machines/ -v -run TestStart_Success`
Expected: SKIP

**Step 3: Create the service skeleton**

Create `backend/internal/machines/runtime.go`:

```go
package machines

import (
	"context"

	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
	"github.com/mathaix/openclawmachines/backend/internal/kvstore"
	"github.com/mathaix/openclawmachines/backend/internal/scheduler"
	"github.com/mathaix/openclawmachines/backend/internal/secrets"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/internal/tunnel"
)

// RuntimeService manages machine start/stop/delete lifecycle.
// Handlers call this service instead of orchestrating dependencies directly.
type RuntimeService struct {
	store       store.Store
	scheduler   *scheduler.Scheduler
	agentClient *agentclient.Client
	kvStore     *kvstore.KVStore
	tunnelMgr   *tunnel.Manager
	secrets     *secrets.Manager
	secretKey   string

	// Config carried from server
	rootfsDataVersion int
	cfSSHCAPubKey     string
}

// RuntimeConfig holds configuration for the RuntimeService.
type RuntimeConfig struct {
	RootfsDataVersion int
	CfSSHCAPubKey     string
	SecretKey         string
}

// NewRuntimeService creates a new RuntimeService.
func NewRuntimeService(
	s store.Store,
	sched *scheduler.Scheduler,
	agent *agentclient.Client,
	kv *kvstore.KVStore,
	tmgr *tunnel.Manager,
	sec *secrets.Manager,
	cfg RuntimeConfig,
) *RuntimeService {
	return &RuntimeService{
		store:             s,
		scheduler:         sched,
		agentClient:       agent,
		kvStore:           kv,
		tunnelMgr:         tmgr,
		secrets:           sec,
		secretKey:         cfg.SecretKey,
		rootfsDataVersion: cfg.RootfsDataVersion,
		cfSSHCAPubKey:     cfg.CfSSHCAPubKey,
	}
}

// Start boots a machine: decrypts secrets, fetches credentials, generates tokens,
// creates tunnel, places on host, calls agent to create VM.
func (rs *RuntimeService) Start(ctx context.Context, accountID int, machine *store.Machine) (*store.Host, string, error) {
	return nil, "", nil // placeholder
}

// Stop gracefully stops a machine: calls agent, releases capacity, cleans routes/tunnels.
func (rs *RuntimeService) Stop(ctx context.Context, machine *store.Machine) error {
	return nil // placeholder
}

// Delete destroys a machine: calls agent, releases capacity, cleans everything, deletes record.
func (rs *RuntimeService) Delete(ctx context.Context, machine *store.Machine) error {
	return nil // placeholder
}
```

**Step 4: Run test to verify it compiles**

Run: `cd backend && go test ./internal/machines/ -v`
Expected: SKIP (test skipped, but package compiles)

**Step 5: Commit**

```bash
git add backend/internal/machines/
git commit -m "feat: add MachineRuntimeService skeleton"
```

---

### Task A1.2: Move Start Logic Into Service

**Files:**
- Modify: `backend/internal/machines/runtime.go` (fill in Start method)
- Modify: `backend/internal/api/server.go:758-931` (extract logic)
- Modify: `backend/internal/api/server.go:1034-1110` (pollVMStatus moves too)

**Step 1: Move startMachineInternal body into RuntimeService.Start**

Copy the body of `startMachineInternal` (server.go:758-931) into `RuntimeService.Start`. Replace `s.store` with `rs.store`, `s.scheduler` with `rs.scheduler`, etc.

Also move `pollVMStatus` (server.go:1049-1110) into the machines package as `rs.pollVMStatus`.

Also move `syncRouteToKV` (server.go:1112-1146) and `deleteRouteFromKV` (server.go:1140-1147) as methods on RuntimeService.

The Start method needs access to account members (for owner emails) — add `ListAccountMembers` and `GetUser` calls using `rs.store`.

**Step 2: Update server.go handlers to call RuntimeService**

Add `machines *machines.RuntimeService` field to Server struct.

Replace `handleStartMachine` (line 595-616) and `handleCreateMachine` (line 520-616) to call `s.machines.Start()`.

Replace `handleStopMachine` (line 1149-1198) to call `s.machines.Stop()`.

Replace `handleDeleteMachine` (line 704-753) to call `s.machines.Delete()`.

**Step 3: Wire RuntimeService in main.go**

In `backend/cmd/server/main.go`, construct `machines.NewRuntimeService(...)` before `api.NewServer(...)` and pass it to the server.

**Step 4: Run all tests**

Run: `make test-go`
Expected: PASS — behavior is identical, just reorganized

**Step 5: Commit**

```bash
git add backend/internal/machines/ backend/internal/api/server.go backend/cmd/server/main.go
git commit -m "refactor: move machine start/stop/delete into RuntimeService"
```

---

### Task A1.3: Write RuntimeService Unit Tests

**Files:**
- Modify: `backend/internal/machines/runtime_test.go` (real tests with mocks)

**Step 1: Write tests with mock dependencies**

Remove the skip. Create a proper test with mock store, mock scheduler, etc.:

```go
func TestStart_Success(t *testing.T) {
	// Setup: mock store returns secrets, credentials, mock scheduler places machine
	// Act: call rs.Start(ctx, accountID, machine)
	// Assert: host and vmIP returned, agent CreateVM was called
}

func TestStart_RollbackOnAgentFailure(t *testing.T) {
	// Setup: mock agent returns error from CreateVM
	// Act: call rs.Start(ctx, accountID, machine)
	// Assert: error returned, scheduler.ReleaseMachine was called (rollback)
}

func TestStop_Success(t *testing.T) {
	// Setup: machine has host_id, mock agent stops successfully
	// Act: call rs.Stop(ctx, machine)
	// Assert: agent StopVM called, scheduler.SoftReleaseMachine called,
	//         KV route deleted, tunnel cleaned up
}

func TestDelete_Success(t *testing.T) {
	// Setup: machine has host_id and tunnel_id
	// Act: call rs.Delete(ctx, machine)
	// Assert: agent DestroyVM called, scheduler.ReleaseMachine called,
	//         KV route deleted, tunnel deleted, machine record deleted
}
```

**Step 2: Run tests**

Run: `cd backend && go test ./internal/machines/ -v`
Expected: PASS

**Step 3: Run full suite**

Run: `make test-go`
Expected: PASS

**Step 4: Commit**

```bash
git add backend/internal/machines/runtime_test.go
git commit -m "test: add RuntimeService unit tests for start/stop/delete"
```

---

### Task A1.4: Run Gateway E2E and Push

**Step 1: Run E2E tests**

Run: `make test-gateway-e2e`
Expected: PASS

**Step 2: Push**

```bash
git push
```

---

## Workstream B1: Port Denylist

### Task B1.1: Write Port Denylist Test

**Files:**
- Create: `backend/cmd/authproxy/main_test.go`

**Step 1: Write failing tests**

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPortDenylist_BlocksReservedPorts(t *testing.T) {
	reserved := []int{22, 80, 7681, 8080, 9090, 9091, 9222, 18789}
	for _, port := range reserved {
		t.Run(fmt.Sprintf("port_%d", port), func(t *testing.T) {
			req := httptest.NewRequest("GET", fmt.Sprintf("/port/%d/", port), nil)
			w := httptest.NewRecorder()
			ap := &authProxy{} // minimal construction
			ap.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Errorf("port %d: expected 403, got %d", port, w.Code)
			}
		})
	}
}

func TestPortDenylist_AllowsUserPorts(t *testing.T) {
	// Port 3000 should be proxied (not blocked)
	req := httptest.NewRequest("GET", "/port/3000/", nil)
	w := httptest.NewRecorder()
	ap := &authProxy{} // will need target setup for full test
	ap.ServeHTTP(w, req)
	if w.Code == http.StatusForbidden {
		t.Error("port 3000 should not be blocked")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd backend && go test ./cmd/authproxy/ -v -run TestPortDenylist`
Expected: FAIL — reserved ports currently get proxied, not 403'd

**Step 3: Add the denylist**

In `backend/cmd/authproxy/main.go`, add after line 101 (after `portNum < 1024 || portNum > 65535` check):

```go
// Block reserved internal ports
var reservedPorts = map[int]bool{
	22: true, 80: true, 7681: true, 8080: true,
	9090: true, 9091: true, 9222: true, 18789: true,
}

// In ServeHTTP, after port range validation:
if reservedPorts[portNum] {
	slog.Warn("port.denied", "port", portNum, "path", path)
	http.Error(w, `{"error":"port not allowed"}`, http.StatusForbidden)
	return
}
```

**Step 4: Run tests**

Run: `cd backend && go test ./cmd/authproxy/ -v -run TestPortDenylist`
Expected: PASS

**Step 5: Commit and push**

```bash
git add backend/cmd/authproxy/
git commit -m "security: block /port/* from reaching reserved internal ports"
git push
```

---

## Workstream B3: Rootfs Hardening

### Task B3.1: Atomic Rootfs Refresh

**Files:**
- Modify: `backend/internal/agentapi/handlers.go:666-699`

**Step 1: Write failing test**

Add to `backend/internal/agentapi/server_test.go` (or create a new test file):

```go
func TestRefreshRootfs_AtomicWrite(t *testing.T) {
	// Setup: create a source rootfs file, create an existing destination
	// Act: call the refresh handler
	// Assert: destination was updated atomically (no .tmp left behind)
	// Assert: if we interrupt mid-copy, destination still has old content
	t.Skip("atomic refresh not implemented yet")
}
```

**Step 2: Replace in-place copy with atomic pattern**

In `backend/internal/agentapi/handlers.go`, replace the handleRefreshRootfs body (lines 680-692):

```go
func (s *Server) handleRefreshRootfs(w http.ResponseWriter, r *http.Request) {
	src := rootfsSourcePath
	dst := rootfsCachePath
	tmpDst := dst + ".tmp"

	slog.Info("rootfs.refresh_start", "src", src, "dst", dst)

	srcFile, err := os.Open(src)
	if err != nil {
		slog.Error("rootfs.open_failed", "error", err, "path", src)
		http.Error(w, fmt.Sprintf("failed to open source: %v", err), http.StatusInternalServerError)
		return
	}
	defer srcFile.Close()

	// Write to temp file first
	tmpFile, err := os.Create(tmpDst)
	if err != nil {
		slog.Error("rootfs.create_tmp_failed", "error", err, "path", tmpDst)
		http.Error(w, fmt.Sprintf("failed to create temp file: %v", err), http.StatusInternalServerError)
		return
	}

	if _, err := io.Copy(tmpFile, srcFile); err != nil {
		tmpFile.Close()
		os.Remove(tmpDst)
		slog.Error("rootfs.copy_failed", "error", err)
		http.Error(w, fmt.Sprintf("failed to copy: %v", err), http.StatusInternalServerError)
		return
	}

	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpDst)
		slog.Error("rootfs.close_tmp_failed", "error", err)
		http.Error(w, fmt.Sprintf("failed to flush temp file: %v", err), http.StatusInternalServerError)
		return
	}

	// Atomic rename
	if err := os.Rename(tmpDst, dst); err != nil {
		os.Remove(tmpDst)
		slog.Error("rootfs.rename_failed", "error", err)
		http.Error(w, fmt.Sprintf("failed to rename: %v", err), http.StatusInternalServerError)
		return
	}

	slog.Info("rootfs.refresh_complete")
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "rootfs cache refreshed",
	})
}
```

**Step 3: Run tests**

Run: `cd backend && go test ./internal/agentapi/ -v`
Expected: PASS

**Step 4: Commit**

```bash
git add backend/internal/agentapi/handlers.go
git commit -m "fix: make rootfs refresh atomic with temp file + rename"
```

---

### Task B3.2: GCS Manifest Failure Resilience

**Files:**
- Modify: `backend/internal/rootfs/gcs.go:54-57` (EnsureRootfs)
- Create: `backend/internal/rootfs/gcs_test.go`

**Step 1: Write failing test**

Create `backend/internal/rootfs/gcs_test.go`:

```go
package rootfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureRootfs_FallbackOnManifestFailure(t *testing.T) {
	// Setup: create a previously staged rootfs + sidecar manifest
	tmpDir := t.TempDir()
	rootfsPath := filepath.Join(tmpDir, ".base-rootfs.ext4")
	manifestPath := filepath.Join(tmpDir, ".rootfs-manifest.json")

	// Write a fake staged rootfs
	os.WriteFile(rootfsPath, []byte("fake-rootfs-content"), 0644)
	os.WriteFile(manifestPath, []byte(`{"version":"v1","sha256":"abc","size_bytes":19}`), 0644)

	// Act: call EnsureRootfs with a broken GCS manifest URI
	// Assert: should succeed and return the cached path with a warning
	t.Skip("manifest failure fallback not implemented yet")
}

func TestEnsureRootfs_FailsWithNoCache(t *testing.T) {
	// Setup: empty directory, broken GCS manifest URI
	// Act: call EnsureRootfs
	// Assert: should fail because no cached rootfs exists
	t.Skip("manifest failure fallback not implemented yet")
}
```

**Step 2: Implement manifest failure fallback**

In `backend/internal/rootfs/gcs.go`, modify `EnsureRootfs` (around line 54-57):

```go
func (f *Fetcher) EnsureRootfs(ctx context.Context, stateDir string) (string, error) {
	rootfsPath := filepath.Join(stateDir, ".base-"+f.prefix+".ext4")
	cachePath := filepath.Join(stateDir, "."+f.prefix+"-manifest.json")

	manifest, err := f.fetchManifest(ctx)
	if err != nil {
		// Check if we have a previously verified cached rootfs
		if _, statErr := os.Stat(rootfsPath); statErr == nil {
			slog.Warn("rootfs.manifest_fetch_failed.using_cache",
				"error", err,
				"cached_path", rootfsPath)
			return rootfsPath, nil
		}
		return "", fmt.Errorf("fetch manifest (no cached fallback): %w", err)
	}

	// ... rest of existing logic unchanged
```

**Step 3: Run tests**

Run: `cd backend && go test ./internal/rootfs/ -v`
Expected: PASS

**Step 4: Commit**

```bash
git add backend/internal/rootfs/gcs.go backend/internal/rootfs/gcs_test.go
git commit -m "fix: rootfs staging falls back to cached image on manifest failure"
```

---

### Task B3.3: File Lock for Concurrent Access

**Files:**
- Create: `backend/internal/rootfs/lock.go`
- Modify: `backend/internal/agentapi/handlers.go` (acquire exclusive lock in refresh)
- Modify: `backend/internal/orchestrator/firecracker_linux.go:147-159` (acquire shared lock for reflink)

**Step 1: Create lock helper**

Create `backend/internal/rootfs/lock.go`:

```go
package rootfs

import (
	"os"
	"path/filepath"
	"syscall"
)

// RootfsLock provides flock-based concurrency control for rootfs operations.
// Refresh acquires an exclusive lock. VM create acquires a shared lock.
type RootfsLock struct {
	path string
}

// NewLock creates a lock for the given rootfs directory.
func NewLock(stateDir string) *RootfsLock {
	return &RootfsLock{path: filepath.Join(stateDir, ".rootfs.lock")}
}

// SharedLock acquires a shared (read) lock. Multiple shared locks can coexist.
// Returns an unlock function.
func (l *RootfsLock) SharedLock() (unlock func(), err error) {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDONLY, 0644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// ExclusiveLock acquires an exclusive (write) lock. Blocks until all shared locks release.
// Returns an unlock function.
func (l *RootfsLock) ExclusiveLock() (unlock func(), err error) {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
```

**Step 2: Use exclusive lock in refresh handler**

In `backend/internal/agentapi/handlers.go`, at the top of `handleRefreshRootfs`, before opening source file:

```go
lock := rootfs.NewLock(filepath.Dir(dst))
unlock, err := lock.ExclusiveLock()
if err != nil {
	http.Error(w, "failed to acquire rootfs lock", http.StatusServiceUnavailable)
	return
}
defer unlock()
```

**Step 3: Use shared lock in orchestrator reflink**

In `backend/internal/orchestrator/firecracker_linux.go`, before `reflinkCopy` (around line 147):

```go
lock := rootfs.NewLock(o.cfg.StateDir)
unlock, err := lock.SharedLock()
if err != nil {
	return fmt.Errorf("acquire rootfs lock: %w", err)
}
defer unlock()
```

**Step 4: Run tests**

Run: `make test-go`
Expected: PASS

**Step 5: Commit and push**

```bash
git add backend/internal/rootfs/lock.go backend/internal/agentapi/handlers.go backend/internal/orchestrator/firecracker_linux.go
git commit -m "fix: add flock to prevent concurrent rootfs refresh and VM create races"
git push
```

---

### Task B3.4: Final Verification

**Step 1: Run full test suite**

Run: `make test-go`
Expected: PASS

**Step 2: Build check**

Run: `CGO_ENABLED=0 GOOS=linux go build ./backend/cmd/agent/ && CGO_ENABLED=0 GOOS=linux go build ./backend/cmd/server/`
Expected: Both compile

**Step 3: Push all**

```bash
git push
```
