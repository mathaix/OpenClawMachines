# PR2: RouteService & Projector — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Postgres authoritative for routing by extracting tunnel/DNS/KV operations from RuntimeService into a dedicated RouteService, then adding a projector that keeps KV in sync with DB state.

**Architecture:** Today, `RuntimeService.Start/Stop/Delete` directly call KV writes, tunnel creation, and DNS operations inline. This plan extracts those into a `RouteService` that owns all routing side-effects. A background `Projector` periodically syncs DB → KV (ensuring KV is always a projection of DB truth). A drift reconciler detects stale/orphaned KV entries and repairs them. Handlers and RuntimeService call RouteService only — zero direct KV/tunnel writes.

**Tech Stack:** Go 1.25, Cloudflare KV API, Cloudflare Tunnel API, Cloudflare DNS API, PostgreSQL

---

## File Structure

### Files to Create
- `backend/internal/routing/service.go` — RouteService: owns tunnel create/delete, DNS create/delete, KV write/delete
- `backend/internal/routing/service_test.go` — unit tests with mock tunnel/KV/store
- `backend/internal/routing/projector.go` — background projector: DB → KV sync loop
- `backend/internal/routing/projector_test.go` — projector tests
- `backend/internal/routing/reconciler.go` — drift reconciler: detects stale KV, repairs
- `backend/internal/routing/reconciler_test.go` — reconciler tests

### Files to Modify
- `backend/internal/machines/runtime.go` — replace inline KV/tunnel/DNS calls with RouteService calls
- `backend/internal/machines/runtime_test.go` — update mocks for RouteService interface
- `backend/internal/api/server.go` — wire RouteService, start projector
- `backend/internal/api/agent_heartbeat.go` — replace inline KV self-heal with RouteService call
- `backend/cmd/server/main.go` — create and inject RouteService

---

## Task 1: Create RouteService Interface and Struct

**Files:**
- Create: `backend/internal/routing/service.go`
- Test: `backend/internal/routing/service_test.go`

- [ ] **Step 1: Write the failing test for SetupRoute**

```go
// backend/internal/routing/service_test.go
package routing

import (
	"context"
	"testing"
)

type mockTunnelMgr struct {
	createVMTunnelCalled    bool
	configureVMTunnelCalled bool
	createDNSRouteCalled    int
	deleteTunnelAndDNSCalled bool

	tunnelID    string
	tunnelToken string
}

func (m *mockTunnelMgr) CreateVMTunnel(ctx context.Context, slug string) (string, string, error) {
	m.createVMTunnelCalled = true
	return m.tunnelID, m.tunnelToken, nil
}

func (m *mockTunnelMgr) ConfigureVMTunnel(ctx context.Context, tunnelID, httpHost, sshHost string) error {
	m.configureVMTunnelCalled = true
	return nil
}

func (m *mockTunnelMgr) CreateDNSRoute(ctx context.Context, tunnelID, hostname string) error {
	m.createDNSRouteCalled++
	return nil
}

func (m *mockTunnelMgr) DeleteTunnelAndDNS(ctx context.Context, tunnelID string, hostnames ...string) error {
	m.deleteTunnelAndDNSCalled = true
	return nil
}

type mockKV struct {
	putRouteCalled    bool
	deleteRouteCalled bool
	putAccountCalled  bool
	lastRouteKey      string
}

func (m *mockKV) PutRouteSync(ctx context.Context, accountSlug, machineSlug string, entry KVRouteEntry) error {
	m.putRouteCalled = true
	m.lastRouteKey = accountSlug + ":" + machineSlug
	return nil
}

func (m *mockKV) DeleteRouteSync(ctx context.Context, accountSlug, machineSlug string) error {
	m.deleteRouteCalled = true
	return nil
}

func (m *mockKV) PutAccount(ctx context.Context, slug string, entry KVAccountEntry) error {
	m.putAccountCalled = true
	return nil
}

type mockStore struct {
	updateTunnelCalled bool
	clearTunnelCalled  bool
}

func (m *mockStore) UpdateMachineTunnel(ctx context.Context, machineID, tunnelID, signingKey string) error {
	m.updateTunnelCalled = true
	return nil
}

func (m *mockStore) ClearMachineTunnel(ctx context.Context, machineID string) error {
	m.clearTunnelCalled = true
	return nil
}

func TestSetupRoute_CreatesTunnelDNSAndStoresInDB(t *testing.T) {
	tmgr := &mockTunnelMgr{tunnelID: "tun-123", tunnelToken: "tok-abc"}
	kv := &mockKV{}
	st := &mockStore{}
	svc := New(tmgr, kv, st)

	result, err := svc.SetupRoute(context.Background(), SetupRequest{
		MachineID:   "machine-1",
		MachineSlug: "my-machine",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TunnelID != "tun-123" {
		t.Errorf("got tunnel ID %q, want %q", result.TunnelID, "tun-123")
	}
	if result.TunnelToken != "tok-abc" {
		t.Errorf("got tunnel token %q, want %q", result.TunnelToken, "tok-abc")
	}
	if !tmgr.createVMTunnelCalled {
		t.Error("expected CreateVMTunnel to be called")
	}
	if !tmgr.configureVMTunnelCalled {
		t.Error("expected ConfigureVMTunnel to be called")
	}
	if tmgr.createDNSRouteCalled != 2 {
		t.Errorf("expected 2 DNS route calls, got %d", tmgr.createDNSRouteCalled)
	}
	if !st.updateTunnelCalled {
		t.Error("expected UpdateMachineTunnel to be called")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/routing/... -count=1 -run TestSetupRoute`
Expected: FAIL — package doesn't exist yet

- [ ] **Step 3: Write minimal RouteService implementation**

```go
// backend/internal/routing/service.go
package routing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
)

// TunnelManager is the subset of tunnel.Manager methods RouteService needs.
type TunnelManager interface {
	CreateVMTunnel(ctx context.Context, slug string) (tunnelID, token string, err error)
	ConfigureVMTunnel(ctx context.Context, tunnelID, httpHostname, sshHostname string) error
	CreateDNSRoute(ctx context.Context, tunnelID, hostname string) error
	DeleteTunnelAndDNS(ctx context.Context, tunnelID string, hostnames ...string) error
}

// KVRouteEntry mirrors kvstore.RouteEntry to avoid import cycle.
type KVRouteEntry struct {
	MachineID    string `json:"machine_id"`
	HostHostname string `json:"host_hostname"`
	ProxyToken   string `json:"proxy_token"`
}

// KVAccountEntry mirrors kvstore.AccountEntry.
type KVAccountEntry struct {
	AccountID int   `json:"account_id"`
	UserIDs   []int `json:"user_ids"`
}

// KVWriter is the subset of kvstore.KVStore methods RouteService needs.
type KVWriter interface {
	PutRouteSync(ctx context.Context, accountSlug, machineSlug string, entry KVRouteEntry) error
	DeleteRouteSync(ctx context.Context, accountSlug, machineSlug string) error
	PutAccount(ctx context.Context, slug string, entry KVAccountEntry) error
}

// RouteStore is the subset of store methods RouteService needs.
type RouteStore interface {
	UpdateMachineTunnel(ctx context.Context, machineID, tunnelID, signingKey string) error
	ClearMachineTunnel(ctx context.Context, machineID string) error
}

const machineDomain = "openclawmachines.com"

// SetupRequest contains the info needed to set up routing for a machine.
type SetupRequest struct {
	MachineID   string
	MachineSlug string
}

// SetupResult contains the tunnel info created by SetupRoute.
type SetupResult struct {
	TunnelID       string
	TunnelToken    string
	SigningKey      string
	VMHostname     string
	SSHHostname    string
}

// TeardownRequest contains the info needed to tear down routing for a machine.
type TeardownRequest struct {
	MachineID    string
	MachineSlug  string
	AccountSlug  string
	TunnelID     string // empty if no tunnel
}

// SyncKVRequest contains the info needed to sync a route to KV.
type SyncKVRequest struct {
	AccountSlug  string
	MachineSlug  string
	MachineID    string
	HostHostname string
	ProxyToken   string
}

// Service owns all routing side-effects: tunnel, DNS, and KV.
type Service struct {
	tunnel TunnelManager
	kv     KVWriter
	store  RouteStore
}

// New creates a new routing Service.
func New(tunnel TunnelManager, kv KVWriter, store RouteStore) *Service {
	return &Service{tunnel: tunnel, kv: kv, store: store}
}

// SetupRoute creates a per-VM tunnel, configures DNS, and stores tunnel info in DB.
// Called during machine start.
func (s *Service) SetupRoute(ctx context.Context, req SetupRequest) (*SetupResult, error) {
	if s.tunnel == nil {
		return &SetupResult{}, nil
	}

	vmHostname := "m-" + req.MachineSlug + "." + machineDomain
	sshHostname := "ssh-" + req.MachineSlug + "." + machineDomain

	// Generate signing key
	signingKeyBytes := make([]byte, 32)
	if _, err := rand.Read(signingKeyBytes); err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}
	signingKey := hex.EncodeToString(signingKeyBytes)

	// Create tunnel
	tunnelID, tunnelToken, err := s.tunnel.CreateVMTunnel(ctx, req.MachineSlug)
	if err != nil {
		slog.Error("route.setup.tunnel.failed", "machine_id", req.MachineID, "error", err)
		return &SetupResult{VMHostname: vmHostname, SSHHostname: sshHostname}, nil
	}

	// Configure tunnel ingress
	if err := s.tunnel.ConfigureVMTunnel(ctx, tunnelID, vmHostname, sshHostname); err != nil {
		slog.Error("route.setup.tunnel.configure.failed", "machine_id", req.MachineID, "error", err)
	}

	// Create DNS records
	if err := s.tunnel.CreateDNSRoute(ctx, tunnelID, vmHostname); err != nil {
		slog.Error("route.setup.dns.failed", "machine_id", req.MachineID, "error", err)
	}
	if err := s.tunnel.CreateDNSRoute(ctx, tunnelID, sshHostname); err != nil {
		slog.Error("route.setup.ssh_dns.failed", "machine_id", req.MachineID, "error", err)
	}

	// Store tunnel info in DB
	if err := s.store.UpdateMachineTunnel(ctx, req.MachineID, tunnelID, signingKey); err != nil {
		slog.Error("route.setup.store.failed", "machine_id", req.MachineID, "error", err)
	}

	slog.Info("route.setup.complete", "machine_id", req.MachineID, "tunnel_id", tunnelID)

	return &SetupResult{
		TunnelID:    tunnelID,
		TunnelToken: tunnelToken,
		SigningKey:   signingKey,
		VMHostname:  vmHostname,
		SSHHostname: sshHostname,
	}, nil
}

// TeardownRoute deletes KV route, per-VM tunnel, and DNS records.
// Called during machine stop and delete.
func (s *Service) TeardownRoute(ctx context.Context, req TeardownRequest) error {
	// Delete KV route
	if s.kv != nil && req.AccountSlug != "" {
		if err := s.kv.DeleteRouteSync(ctx, req.AccountSlug, req.MachineSlug); err != nil {
			slog.Error("route.teardown.kv.failed", "machine_id", req.MachineID, "error", err)
		}
	}

	// Delete tunnel and DNS
	if s.tunnel != nil && req.TunnelID != "" {
		vmHostname := "m-" + req.MachineSlug + "." + machineDomain
		sshHostname := "ssh-" + req.MachineSlug + "." + machineDomain
		if err := s.tunnel.DeleteTunnelAndDNS(ctx, req.TunnelID, vmHostname, sshHostname); err != nil {
			slog.Error("route.teardown.tunnel.failed", "machine_id", req.MachineID, "error", err)
		}
		if err := s.store.ClearMachineTunnel(ctx, req.MachineID); err != nil {
			slog.Error("route.teardown.clear.failed", "machine_id", req.MachineID, "error", err)
		}
	}

	return nil
}

// SyncRouteToKV writes a route entry to KV for a running machine.
// Called after VM reaches "running" status.
func (s *Service) SyncRouteToKV(ctx context.Context, req SyncKVRequest) error {
	if s.kv == nil {
		return nil
	}
	return s.kv.PutRouteSync(ctx, req.AccountSlug, req.MachineSlug, KVRouteEntry{
		MachineID:    req.MachineID,
		HostHostname: req.HostHostname,
		ProxyToken:   req.ProxyToken,
	})
}

// SyncAccountToKV writes an account entry to KV.
// Used by the resolve handler to self-heal account cache.
func (s *Service) SyncAccountToKV(ctx context.Context, slug string, accountID int, userIDs []int) error {
	if s.kv == nil {
		return nil
	}
	return s.kv.PutAccount(ctx, slug, KVAccountEntry{
		AccountID: accountID,
		UserIDs:   userIDs,
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/routing/... -count=1 -run TestSetupRoute`
Expected: PASS

- [ ] **Step 5: Write test for TeardownRoute**

```go
// Add to service_test.go

func TestTeardownRoute_DeletesKVAndTunnel(t *testing.T) {
	tmgr := &mockTunnelMgr{}
	kv := &mockKV{}
	st := &mockStore{}
	svc := New(tmgr, kv, st)

	err := svc.TeardownRoute(context.Background(), TeardownRequest{
		MachineID:   "machine-1",
		MachineSlug: "my-machine",
		AccountSlug: "myteam",
		TunnelID:    "tun-123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !kv.deleteRouteCalled {
		t.Error("expected KV delete to be called")
	}
	if !tmgr.deleteTunnelAndDNSCalled {
		t.Error("expected DeleteTunnelAndDNS to be called")
	}
	if !st.clearTunnelCalled {
		t.Error("expected ClearMachineTunnel to be called")
	}
}

func TestTeardownRoute_SkipsWhenNoTunnel(t *testing.T) {
	tmgr := &mockTunnelMgr{}
	kv := &mockKV{}
	st := &mockStore{}
	svc := New(tmgr, kv, st)

	err := svc.TeardownRoute(context.Background(), TeardownRequest{
		MachineID:   "machine-1",
		MachineSlug: "my-machine",
		AccountSlug: "myteam",
		TunnelID:    "", // no tunnel
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !kv.deleteRouteCalled {
		t.Error("expected KV delete even without tunnel")
	}
	if tmgr.deleteTunnelAndDNSCalled {
		t.Error("expected tunnel delete to be skipped when no tunnel ID")
	}
}
```

- [ ] **Step 6: Run all routing tests**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/routing/... -count=1 -v`
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add backend/internal/routing/
git commit -m "feat: add routing.Service with SetupRoute, TeardownRoute, and SyncRouteToKV"
```

---

## Task 2: Adapt KVStore to Implement KVWriter Interface

The `KVWriter` interface uses `KVRouteEntry`/`KVAccountEntry` from the routing package. The existing `kvstore.KVStore` uses `kvstore.RouteEntry`/`kvstore.AccountEntry`. We need an adapter so KVStore satisfies `routing.KVWriter`.

**Files:**
- Create: `backend/internal/routing/kvadapter.go`
- Test: `backend/internal/routing/kvadapter_test.go`

- [ ] **Step 1: Create KV adapter**

```go
// backend/internal/routing/kvadapter.go
package routing

import (
	"context"

	"github.com/mathaix/openclawmachines/backend/internal/kvstore"
)

// KVAdapter wraps kvstore.KVStore to satisfy the KVWriter interface.
type KVAdapter struct {
	kv *kvstore.KVStore
}

// NewKVAdapter creates a KVWriter from an existing KVStore. Returns nil if kv is nil.
func NewKVAdapter(kv *kvstore.KVStore) KVWriter {
	if kv == nil {
		return nil
	}
	return &KVAdapter{kv: kv}
}

func (a *KVAdapter) PutRouteSync(ctx context.Context, accountSlug, machineSlug string, entry KVRouteEntry) error {
	return a.kv.PutRouteSync(ctx, accountSlug, machineSlug, kvstore.RouteEntry{
		MachineID:    entry.MachineID,
		HostHostname: entry.HostHostname,
		ProxyToken:   entry.ProxyToken,
	})
}

func (a *KVAdapter) DeleteRouteSync(ctx context.Context, accountSlug, machineSlug string) error {
	return a.kv.DeleteRouteSync(ctx, accountSlug, machineSlug)
}

func (a *KVAdapter) PutAccount(ctx context.Context, slug string, entry KVAccountEntry) error {
	return a.kv.PutAccount(ctx, slug, kvstore.AccountEntry{
		AccountID: entry.AccountID,
		UserIDs:   entry.UserIDs,
	})
}
```

- [ ] **Step 2: Verify build**

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./...`
Expected: clean

- [ ] **Step 3: Commit**

```bash
git add backend/internal/routing/kvadapter.go
git commit -m "feat: add KVAdapter to bridge kvstore.KVStore → routing.KVWriter"
```

---

## Task 3: Adapt tunnel.Manager to Implement TunnelManager Interface

The `TunnelManager` interface uses method signatures that match `tunnel.Manager` exactly, so we need to verify compatibility and create a thin adapter only if needed.

**Files:**
- Modify: `backend/internal/routing/service.go` (if adapter needed)

- [ ] **Step 1: Check tunnel.Manager method signatures**

Run: `grep -n 'func (m \*Manager).*CreateVMTunnel\|ConfigureVMTunnel\|CreateDNSRoute\|DeleteTunnelAndDNS' backend/internal/tunnel/tunnel.go`

Verify `tunnel.Manager` satisfies `routing.TunnelManager`. If method signatures match, `*tunnel.Manager` can be passed directly. If they differ (e.g., `DeleteTunnelAndDNS` takes `[]string` instead of `...string`), create a thin adapter.

- [ ] **Step 2: Write a compile-time check**

Add to `service.go`:

```go
// Compile-time check (removed if tunnel.Manager already satisfies TunnelManager).
// If this line causes a compile error, create a TunnelAdapter.
```

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./internal/routing/...`

If it compiles, tunnel.Manager satisfies the interface. If not, create an adapter similar to KVAdapter.

- [ ] **Step 3: Commit if any changes**

```bash
git add backend/internal/routing/
git commit -m "feat: verify tunnel.Manager satisfies routing.TunnelManager"
```

---

## Task 4: Replace Inline KV/Tunnel Calls in RuntimeService with RouteService

This is the core migration: RuntimeService stops directly calling KV and tunnel APIs and instead delegates to RouteService.

**Files:**
- Modify: `backend/internal/machines/runtime.go`
- Modify: `backend/internal/machines/runtime_test.go`

- [ ] **Step 1: Add RouteService field to RuntimeService**

In `runtime.go`, add `routing *routing.Service` to the `RuntimeService` struct and `NewRuntimeService` constructor.

Add to imports:
```go
"github.com/mathaix/openclawmachines/backend/internal/routing"
```

Add to struct:
```go
type RuntimeService struct {
	// ... existing fields ...
	routing *routing.Service
}
```

Add to constructor parameter and assignment:
```go
func NewRuntimeService(
	s RuntimeStore,
	placement *fleet.PlacementService,
	agent *agentclient.Client,
	kv *kvstore.KVStore,
	tmgr *tunnel.Manager,
	prog *progress.Tracker,
	cfg RuntimeConfig,
	routeSvc *routing.Service,
) *RuntimeService {
	return &RuntimeService{
		// ... existing ...
		routing: routeSvc,
	}
}
```

- [ ] **Step 2: Replace tunnel creation in Start with RouteService.SetupRoute**

In the `Start` method (~line 252-295), replace the inline tunnel/DNS/signing-key block with:

```go
// Set up per-VM tunnel, DNS, and signing key via RouteService
if rs.routing != nil {
	result, err := rs.routing.SetupRoute(ctx, routing.SetupRequest{
		MachineID:   machine.ID,
		MachineSlug: machine.Slug,
	})
	if err != nil {
		slog.Error("machine.start.route.setup.failed", "machine_id", machine.ID, "error", err)
	} else {
		machine.TunnelToken = result.TunnelToken
		if result.TunnelID != "" {
			machine.TunnelID = &result.TunnelID
			machine.SigningKey = &result.SigningKey
		}
		if result.VMHostname != "" {
			machine.TunnelHostname = &result.VMHostname
		}
	}
}
```

Remove the old block that directly calls `rs.tunnelMgr.CreateVMTunnel`, `ConfigureVMTunnel`, `CreateDNSRoute`, `rs.store.UpdateMachineTunnel`, and the inline `rand.Read` for signing key.

- [ ] **Step 3: Replace KV sync in pollVMStatus with RouteService.SyncRouteToKV**

Replace the `syncRouteToKV` call in `pollVMStatus` (~line 932) with a call to `rs.routing.SyncRouteToKV`. The caller needs to build the `SyncKVRequest` from the machine and host.

Replace `rs.syncRouteToKV(ctx, machineID, host)` with:

```go
if rs.routing != nil {
	machine, err := rs.store.GetMachine(ctx, machineID)
	if err == nil {
		account, err := rs.store.GetAccount(ctx, machine.AccountID)
		if err == nil {
			proxyToken := ""
			if machine.ProxyToken != nil {
				proxyToken = *machine.ProxyToken
			}
			if err := rs.routing.SyncRouteToKV(ctx, routing.SyncKVRequest{
				AccountSlug:  account.Slug,
				MachineSlug:  machine.Slug,
				MachineID:    machine.ID,
				HostHostname: *host.TunnelURL,
				ProxyToken:   proxyToken,
			}); err != nil {
				slog.Error("machine.start.kv_sync.failed", "machine_id", machineID, "error", err)
			}
		}
	}
}
```

- [ ] **Step 4: Replace teardown in Stop with RouteService.TeardownRoute**

In `Stop` (~line 669-682), replace `rs.DeleteRouteFromKV` and the tunnel cleanup block with:

```go
if rs.routing != nil {
	account, _ := rs.store.GetAccount(ctx, machine.AccountID)
	accountSlug := ""
	if account != nil {
		accountSlug = account.Slug
	}
	tunnelID := ""
	if machine.TunnelID != nil {
		tunnelID = *machine.TunnelID
	}
	_ = rs.routing.TeardownRoute(ctx, routing.TeardownRequest{
		MachineID:   machine.ID,
		MachineSlug: machine.Slug,
		AccountSlug: accountSlug,
		TunnelID:    tunnelID,
	})
}
```

- [ ] **Step 5: Replace teardown in Delete with RouteService.TeardownRoute**

In `Delete` (~line 796-806), replace the tunnel cleanup and KV delete with the same `TeardownRoute` pattern as Stop.

- [ ] **Step 6: Replace CleanupMachineRouteAndTunnel with RouteService.TeardownRoute**

In `CleanupMachineRouteAndTunnel` (~line 1000-1022), replace the inline logic with a RouteService call.

- [ ] **Step 7: Remove now-unused private methods**

Delete these methods from `runtime.go` — they're replaced by RouteService:
- `syncRouteToKV` (~line 967-996)
- `DeleteRouteFromKV` (~line 1024-1034)

Also remove the `kvstore` import if no longer used, and `crypto/rand`/`encoding/hex` if only used for signing key generation (now in RouteService).

- [ ] **Step 8: Update runtime_test.go mocks**

Update the `NewRuntimeService` call in tests to pass the routing.Service (or nil for tests that don't need it). Check that tests still compile and pass.

- [ ] **Step 9: Verify build and tests**

Run:
```bash
cd /home/mantiz/OpenClawMachines/backend && go build ./... && go test ./internal/machines/... ./internal/routing/... -count=1
```
Expected: all pass.

- [ ] **Step 10: Commit**

```bash
git add -A
git commit -m "refactor: replace inline KV/tunnel calls in RuntimeService with RouteService"
```

---

## Task 5: Replace KV Self-Heal in handleInternalResolve with RouteService

**Files:**
- Modify: `backend/internal/api/agent_heartbeat.go`
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Add RouteService field to Server struct**

In `server.go`, add:
```go
routing *routing.Service
```
to the `Server` struct, and add a setter:
```go
func (s *Server) SetRouting(r *routing.Service) {
	s.routing = r
}
```

- [ ] **Step 2: Replace KV self-heal in handleInternalResolve**

In `agent_heartbeat.go`, `handleInternalResolve` currently does inline `kvStore.PutRoute` and `kvStore.PutAccount` calls. Replace with:

```go
// Self-heal KV cache via RouteService
if s.routing != nil {
	if route.HostHostname != "" {
		_ = s.routing.SyncRouteToKV(r.Context(), routing.SyncKVRequest{
			AccountSlug:  req.AccountSlug,
			MachineSlug:  req.MachineSlug,
			MachineID:    route.MachineID,
			HostHostname: route.HostHostname,
			ProxyToken:   route.ProxyToken,
		})
	}
	_ = s.routing.SyncAccountToKV(r.Context(), req.AccountSlug, route.AccountID, route.UserIDs)
}
```

Remove the old direct `s.kvStore.PutRoute` and `s.kvStore.PutAccount` calls.

- [ ] **Step 3: Verify build and tests**

Run:
```bash
cd /home/mantiz/OpenClawMachines/backend && go build ./... && go test ./internal/api/... -count=1
```

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/agent_heartbeat.go backend/internal/api/server.go
git commit -m "refactor: replace inline KV self-heal in handleInternalResolve with RouteService"
```

---

## Task 6: Wire RouteService in main.go

**Files:**
- Modify: `backend/cmd/server/main.go`

- [ ] **Step 1: Create and inject RouteService**

In `main.go`, after creating `tunnelMgr` and `kvStore`, create the RouteService and pass it to RuntimeService and Server:

```go
import "github.com/mathaix/openclawmachines/backend/internal/routing"

// After tunnelMgr and kvStore creation:
kvAdapter := routing.NewKVAdapter(kvStore)
routeSvc := routing.New(tunnelMgr, kvAdapter, db)
```

Pass `routeSvc` to `NewRuntimeService` (new parameter) and call `srv.SetRouting(routeSvc)`.

Note: The `db` (postgres store) must satisfy `routing.RouteStore`. Since `routing.RouteStore` requires `UpdateMachineTunnel` and `ClearMachineTunnel`, and `PostgresStore` already has both, this works.

- [ ] **Step 2: Verify build and full test suite**

Run:
```bash
cd /home/mantiz/OpenClawMachines/backend && go build ./... && go test ./... -count=1
```

- [ ] **Step 3: Commit**

```bash
git add backend/cmd/server/main.go
git commit -m "feat: wire RouteService into main.go, inject into RuntimeService and Server"
```

---

## Task 7: Add Route Projector (DB → KV Sync)

The projector periodically reads all running routes from DB and ensures KV is up to date.

**Files:**
- Create: `backend/internal/routing/projector.go`
- Create: `backend/internal/routing/projector_test.go`

- [ ] **Step 1: Write the failing test for Projector**

```go
// backend/internal/routing/projector_test.go
package routing

import (
	"context"
	"testing"
	"time"
)

type mockRouteReader struct {
	routes []RouteSnapshot
}

func (m *mockRouteReader) ListRunningRoutes(ctx context.Context) ([]RouteSnapshot, error) {
	return m.routes, nil
}

type recordingKV struct {
	putRoutes []string
}

func (r *recordingKV) PutRouteSync(ctx context.Context, accountSlug, machineSlug string, entry KVRouteEntry) error {
	r.putRoutes = append(r.putRoutes, accountSlug+":"+machineSlug)
	return nil
}

func (r *recordingKV) DeleteRouteSync(ctx context.Context, accountSlug, machineSlug string) error {
	return nil
}

func (r *recordingKV) PutAccount(ctx context.Context, slug string, entry KVAccountEntry) error {
	return nil
}

func TestProjector_SyncsAllRunningRoutes(t *testing.T) {
	reader := &mockRouteReader{
		routes: []RouteSnapshot{
			{AccountSlug: "team-a", MachineSlug: "vm-1", MachineID: "id-1", HostHostname: "host-1.example.com", ProxyToken: "tok-1"},
			{AccountSlug: "team-b", MachineSlug: "vm-2", MachineID: "id-2", HostHostname: "host-2.example.com", ProxyToken: "tok-2"},
		},
	}
	kv := &recordingKV{}

	p := NewProjector(reader, kv)
	count, err := p.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 routes synced, got %d", count)
	}
	if len(kv.putRoutes) != 2 {
		t.Errorf("expected 2 KV puts, got %d", len(kv.putRoutes))
	}
}

func TestProjector_StartStop(t *testing.T) {
	reader := &mockRouteReader{routes: nil}
	kv := &recordingKV{}
	p := NewProjector(reader, kv)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Start(ctx, 50*time.Millisecond)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// OK — projector stopped
	case <-time.After(2 * time.Second):
		t.Fatal("projector did not stop within timeout")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/routing/... -count=1 -run TestProjector`
Expected: FAIL — `NewProjector` not defined

- [ ] **Step 3: Implement Projector**

```go
// backend/internal/routing/projector.go
package routing

import (
	"context"
	"log/slog"
	"time"
)

// RouteSnapshot is a single running route from the DB.
// Mirrors store.RouteData to avoid import cycle.
type RouteSnapshot struct {
	AccountSlug  string
	MachineID    string
	MachineSlug  string
	HostHostname string
	ProxyToken   string
}

// RouteReader reads running routes from the DB.
type RouteReader interface {
	ListRunningRoutes(ctx context.Context) ([]RouteSnapshot, error)
}

// Projector periodically syncs DB routes → KV.
type Projector struct {
	reader RouteReader
	kv     KVWriter
}

// NewProjector creates a new Projector.
func NewProjector(reader RouteReader, kv KVWriter) *Projector {
	return &Projector{reader: reader, kv: kv}
}

// SyncOnce reads all running routes from DB and writes them to KV.
// Returns the number of routes synced.
func (p *Projector) SyncOnce(ctx context.Context) (int, error) {
	routes, err := p.reader.ListRunningRoutes(ctx)
	if err != nil {
		return 0, err
	}

	synced := 0
	for _, r := range routes {
		if err := p.kv.PutRouteSync(ctx, r.AccountSlug, r.MachineSlug, KVRouteEntry{
			MachineID:    r.MachineID,
			HostHostname: r.HostHostname,
			ProxyToken:   r.ProxyToken,
		}); err != nil {
			slog.Warn("projector.sync.failed", "account", r.AccountSlug, "machine", r.MachineSlug, "error", err)
			continue
		}
		synced++
	}

	return synced, nil
}

// Start runs the projector in a loop until ctx is cancelled.
func (p *Projector) Start(ctx context.Context, interval time.Duration) {
	slog.Info("projector.started", "interval", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("projector.stopped")
			return
		case <-ticker.C:
			count, err := p.SyncOnce(ctx)
			if err != nil {
				slog.Error("projector.sync.error", "error", err)
			} else {
				slog.Debug("projector.sync.complete", "routes_synced", count)
			}
		}
	}
}
```

- [ ] **Step 4: Run tests**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/routing/... -count=1 -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/routing/projector.go backend/internal/routing/projector_test.go
git commit -m "feat: add routing Projector for periodic DB → KV sync"
```

---

## Task 8: Add RouteReader Adapter for Store

The `Projector` needs a `RouteReader` that returns `[]RouteSnapshot`. The store returns `[]store.RouteData`. Bridge them.

**Files:**
- Create: `backend/internal/routing/storeadapter.go`

- [ ] **Step 1: Create store adapter**

```go
// backend/internal/routing/storeadapter.go
package routing

import (
	"context"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// StoreRouteReader adapts store.RouteRepo to satisfy RouteReader.
type StoreRouteReader struct {
	repo store.RouteRepo
}

// NewStoreRouteReader creates a RouteReader from a store.RouteRepo.
func NewStoreRouteReader(repo store.RouteRepo) RouteReader {
	return &StoreRouteReader{repo: repo}
}

func (s *StoreRouteReader) ListRunningRoutes(ctx context.Context) ([]RouteSnapshot, error) {
	routes, err := s.repo.ListRunningRoutes(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]RouteSnapshot, len(routes))
	for i, r := range routes {
		result[i] = RouteSnapshot{
			AccountSlug:  r.AccountSlug,
			MachineID:    r.MachineID,
			MachineSlug:  r.MachineSlug,
			HostHostname: r.HostHostname,
			ProxyToken:   r.ProxyToken,
		}
	}
	return result, nil
}
```

- [ ] **Step 2: Verify build**

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./...`

- [ ] **Step 3: Commit**

```bash
git add backend/internal/routing/storeadapter.go
git commit -m "feat: add StoreRouteReader adapter for routing Projector"
```

---

## Task 9: Add Drift Reconciler

The reconciler detects KV entries that don't match DB truth and repairs them by deleting stale entries.

**Files:**
- Create: `backend/internal/routing/reconciler.go`
- Create: `backend/internal/routing/reconciler_test.go`

- [ ] **Step 1: Write the failing test**

```go
// backend/internal/routing/reconciler_test.go
package routing

import (
	"context"
	"testing"
)

type mockKVLister struct {
	routes map[string]KVRouteEntry // key = "account:machine"
	deleted []string
}

func (m *mockKVLister) PutRouteSync(ctx context.Context, accountSlug, machineSlug string, entry KVRouteEntry) error {
	return nil
}

func (m *mockKVLister) DeleteRouteSync(ctx context.Context, accountSlug, machineSlug string) error {
	m.deleted = append(m.deleted, accountSlug+":"+machineSlug)
	return nil
}

func (m *mockKVLister) PutAccount(ctx context.Context, slug string, entry KVAccountEntry) error {
	return nil
}

func TestReconciler_DeletesStaleRoutes(t *testing.T) {
	// DB has 1 running route
	reader := &mockRouteReader{
		routes: []RouteSnapshot{
			{AccountSlug: "team-a", MachineSlug: "vm-1", MachineID: "id-1", HostHostname: "host-1", ProxyToken: "tok-1"},
		},
	}
	// KV has 2 routes (vm-2 is stale — not in DB)
	kv := &mockKVLister{
		routes: map[string]KVRouteEntry{
			"team-a:vm-1": {MachineID: "id-1"},
			"team-a:vm-2": {MachineID: "id-2"},
		},
	}

	r := NewReconciler(reader, kv)
	stale, err := r.ReconcileOnce(context.Background(), kv.routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stale != 1 {
		t.Errorf("expected 1 stale route, got %d", stale)
	}
	if len(kv.deleted) != 1 || kv.deleted[0] != "team-a:vm-2" {
		t.Errorf("expected stale route team-a:vm-2 deleted, got %v", kv.deleted)
	}
}

func TestReconciler_NoStaleRoutes(t *testing.T) {
	reader := &mockRouteReader{
		routes: []RouteSnapshot{
			{AccountSlug: "team-a", MachineSlug: "vm-1", MachineID: "id-1", HostHostname: "host-1", ProxyToken: "tok-1"},
		},
	}
	kv := &mockKVLister{
		routes: map[string]KVRouteEntry{
			"team-a:vm-1": {MachineID: "id-1"},
		},
	}

	r := NewReconciler(reader, kv)
	stale, err := r.ReconcileOnce(context.Background(), kv.routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stale != 0 {
		t.Errorf("expected 0 stale routes, got %d", stale)
	}
}
```

- [ ] **Step 2: Implement Reconciler**

```go
// backend/internal/routing/reconciler.go
package routing

import (
	"context"
	"log/slog"
)

// Reconciler detects stale KV routes not backed by a running DB machine.
type Reconciler struct {
	reader RouteReader
	kv     KVWriter
}

// NewReconciler creates a new drift Reconciler.
func NewReconciler(reader RouteReader, kv KVWriter) *Reconciler {
	return &Reconciler{reader: reader, kv: kv}
}

// ReconcileOnce compares kvRoutes against DB truth and deletes stale entries.
// kvRoutes is a map of "accountSlug:machineSlug" → entry, representing current KV state.
// Returns the number of stale routes deleted.
func (r *Reconciler) ReconcileOnce(ctx context.Context, kvRoutes map[string]KVRouteEntry) (int, error) {
	dbRoutes, err := r.reader.ListRunningRoutes(ctx)
	if err != nil {
		return 0, err
	}

	// Build set of valid DB route keys
	valid := make(map[string]struct{}, len(dbRoutes))
	for _, route := range dbRoutes {
		valid[route.AccountSlug+":"+route.MachineSlug] = struct{}{}
	}

	// Delete KV routes not in DB
	stale := 0
	for key := range kvRoutes {
		if _, ok := valid[key]; !ok {
			// Parse key back into account:machine
			// Key format: "accountSlug:machineSlug"
			for i := 0; i < len(key); i++ {
				if key[i] == ':' {
					accountSlug := key[:i]
					machineSlug := key[i+1:]
					if err := r.kv.DeleteRouteSync(ctx, accountSlug, machineSlug); err != nil {
						slog.Warn("reconciler.delete.failed", "key", key, "error", err)
					} else {
						slog.Info("reconciler.deleted_stale", "key", key)
						stale++
					}
					break
				}
			}
		}
	}

	return stale, nil
}
```

- [ ] **Step 3: Run tests**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/routing/... -count=1 -v`
Expected: all PASS

- [ ] **Step 4: Commit**

```bash
git add backend/internal/routing/reconciler.go backend/internal/routing/reconciler_test.go
git commit -m "feat: add routing Reconciler for KV drift detection and repair"
```

---

## Task 10: Wire Projector in main.go and Start It

**Files:**
- Modify: `backend/cmd/server/main.go`

- [ ] **Step 1: Create and start Projector**

After creating `routeSvc`, add:

```go
// Start route projector (DB → KV sync every 60s)
routeReader := routing.NewStoreRouteReader(db)
projector := routing.NewProjector(routeReader, kvAdapter)
go projector.Start(ctx, 60*time.Second)
```

- [ ] **Step 2: Verify build and full test suite**

Run:
```bash
cd /home/mantiz/OpenClawMachines/backend && go build ./... && go test ./... -count=1
```

- [ ] **Step 3: Commit**

```bash
git add backend/cmd/server/main.go
git commit -m "feat: start routing Projector in main.go (60s interval, DB → KV sync)"
```

---

## Task 11: Remove Direct KVStore Usage from RuntimeService

After wiring RouteService, RuntimeService should no longer import or use `kvstore` directly.

**Files:**
- Modify: `backend/internal/machines/runtime.go`

- [ ] **Step 1: Remove kvStore field from RuntimeService**

Remove the `kvStore *kvstore.KVStore` field from the struct and the `kv` parameter from `NewRuntimeService`. Remove the `kvstore` import.

- [ ] **Step 2: Update all callers of NewRuntimeService**

Update `cmd/server/main.go` and any tests that call `NewRuntimeService` to drop the `kv` parameter.

- [ ] **Step 3: Verify build and tests**

Run:
```bash
cd /home/mantiz/OpenClawMachines/backend && go build ./... && go test ./... -count=1
```

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor: remove direct kvstore dependency from RuntimeService"
```

---

## Task 12: Final Verification

- [ ] **Step 1: Verify no direct KV/tunnel writes in handlers or RuntimeService**

Run:
```bash
grep -rn 'kvStore\.\|kvStore ' backend/internal/machines/ backend/internal/api/agent_heartbeat.go
```

Expected: no hits (all KV access goes through RouteService now). The server.go struct may still have `kvStore` for the account KV cache used by members/invitations — that's OK for now.

- [ ] **Step 2: Run full test suite**

Run:
```bash
cd /home/mantiz/OpenClawMachines/backend && go test ./... -count=1
```

- [ ] **Step 3: Run gateway E2E tests**

Run:
```bash
make test-gateway-e2e
```

- [ ] **Step 4: Count lines and verify architecture**

Run:
```bash
wc -l backend/internal/routing/*.go
```

Expected: routing package exists with service, projector, reconciler, and adapters.

- [ ] **Step 5: Commit any cleanup**

```bash
git add -A
git commit -m "refactor: PR2 route service and projector — DB is routing truth, KV is projection"
```

---

## Verification Checklist (from PR2 spec)

- [ ] RouteService resolves routes from DB; KV is cache-only
- [ ] Projector writes KV + ensures routes match DB; errors logged
- [ ] Drift reconciler can detect stale KV entries
- [ ] Handlers and RuntimeService contain zero direct KV/tunnel writes
- [ ] API contract unchanged (resolve/start/stop/delete parity)
- [ ] All existing tests pass after migration
