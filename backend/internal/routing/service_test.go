package routing

import (
	"context"
	"errors"
	"testing"
)

// --- Mock implementations ---

type mockTunnel struct {
	// CreateVMTunnel
	createTunnelID    string
	createTunnelToken string
	createTunnelErr   error
	createCalled      bool

	// ConfigureVMTunnel
	configureCalled bool
	configureErr    error

	// CreateDNSRoute
	dnsRoutesCalled []string
	dnsRouteErr     error

	// DeleteTunnelAndDNS
	deleteCalled    bool
	deleteTunnelID  string
	deleteHostnames []string
	deleteErr       error
}

func (m *mockTunnel) CreateVMTunnel(_ context.Context, slug string) (string, string, error) {
	m.createCalled = true
	return m.createTunnelID, m.createTunnelToken, m.createTunnelErr
}

func (m *mockTunnel) ConfigureVMTunnel(_ context.Context, tunnelID, httpHost, sshHost string) error {
	m.configureCalled = true
	return m.configureErr
}

func (m *mockTunnel) CreateDNSRoute(_ context.Context, tunnelID, hostname string) error {
	m.dnsRoutesCalled = append(m.dnsRoutesCalled, hostname)
	return m.dnsRouteErr
}

func (m *mockTunnel) DeleteTunnelAndDNS(_ context.Context, tunnelID string, hostnames ...string) error {
	m.deleteCalled = true
	m.deleteTunnelID = tunnelID
	m.deleteHostnames = hostnames
	return m.deleteErr
}

type mockKV struct {
	// PutRouteSync
	putRouteCalled      bool
	putRouteAccountSlug string
	putRouteMachineSlug string
	putRouteEntry       KVRouteEntry
	putRouteErr         error

	// DeleteRouteSync
	deleteRouteCalled      bool
	deleteRouteAccountSlug string
	deleteRouteMachineSlug string
	deleteRouteErr         error

	// PutAccount
	putAccountCalled bool
	putAccountSlug   string
	putAccountEntry  KVAccountEntry
	putAccountErr    error
}

func (m *mockKV) PutRouteSync(_ context.Context, accountSlug, machineSlug string, entry KVRouteEntry) error {
	m.putRouteCalled = true
	m.putRouteAccountSlug = accountSlug
	m.putRouteMachineSlug = machineSlug
	m.putRouteEntry = entry
	return m.putRouteErr
}

func (m *mockKV) DeleteRouteSync(_ context.Context, accountSlug, machineSlug string) error {
	m.deleteRouteCalled = true
	m.deleteRouteAccountSlug = accountSlug
	m.deleteRouteMachineSlug = machineSlug
	return m.deleteRouteErr
}

func (m *mockKV) PutAccount(_ context.Context, slug string, entry KVAccountEntry) error {
	m.putAccountCalled = true
	m.putAccountSlug = slug
	m.putAccountEntry = entry
	return m.putAccountErr
}

type mockStore struct {
	// UpdateMachineTunnel
	updateTunnelCalled     bool
	updateTunnelMachineID  string
	updateTunnelID         string
	updateTunnelSigningKey string
	updateTunnelErr        error

	// ClearMachineTunnel
	clearTunnelCalled    bool
	clearTunnelMachineID string
	clearTunnelErr       error
}

func (m *mockStore) UpdateMachineTunnel(_ context.Context, machineID, tunnelID, signingKey string) error {
	m.updateTunnelCalled = true
	m.updateTunnelMachineID = machineID
	m.updateTunnelID = tunnelID
	m.updateTunnelSigningKey = signingKey
	return m.updateTunnelErr
}

func (m *mockStore) ClearMachineTunnel(_ context.Context, machineID string) error {
	m.clearTunnelCalled = true
	m.clearTunnelMachineID = machineID
	return m.clearTunnelErr
}

// --- Tests ---

func TestSetupRoute_CreatesTunnelDNSAndStoresInDB(t *testing.T) {
	tunnel := &mockTunnel{
		createTunnelID:    "tunnel-abc",
		createTunnelToken: "token-xyz",
	}
	kv := &mockKV{}
	store := &mockStore{}

	svc := New(tunnel, kv, store)
	req := SetupRequest{
		MachineID:   "machine-1",
		MachineSlug: "my-machine",
	}

	result, err := svc.SetupRoute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Tunnel was created
	if !tunnel.createCalled {
		t.Error("expected CreateVMTunnel to be called")
	}
	if result.TunnelID != "tunnel-abc" {
		t.Errorf("expected TunnelID=tunnel-abc, got %q", result.TunnelID)
	}
	if result.TunnelToken != "token-xyz" {
		t.Errorf("expected TunnelToken=token-xyz, got %q", result.TunnelToken)
	}

	// VMHostname and SSHHostname are set correctly
	wantVM := "m-my-machine." + defaultDataPlaneDomain
	wantSSH := "ssh-my-machine." + defaultDataPlaneDomain
	if result.VMHostname != wantVM {
		t.Errorf("expected VMHostname=%q, got %q", wantVM, result.VMHostname)
	}
	if result.SSHHostname != wantSSH {
		t.Errorf("expected SSHHostname=%q, got %q", wantSSH, result.SSHHostname)
	}

	// Tunnel was configured
	if !tunnel.configureCalled {
		t.Error("expected ConfigureVMTunnel to be called")
	}

	// DNS routes were created for both hostnames
	if len(tunnel.dnsRoutesCalled) != 2 {
		t.Errorf("expected 2 DNS routes, got %d: %v", len(tunnel.dnsRoutesCalled), tunnel.dnsRoutesCalled)
	}

	// Signing key was generated (non-empty, 64 hex chars = 32 bytes)
	if len(result.SigningKey) != 64 {
		t.Errorf("expected 64-char signing key, got len=%d: %q", len(result.SigningKey), result.SigningKey)
	}

	// DB was updated with tunnel info
	if !store.updateTunnelCalled {
		t.Error("expected UpdateMachineTunnel to be called")
	}
	if store.updateTunnelMachineID != "machine-1" {
		t.Errorf("expected machine_id=machine-1, got %q", store.updateTunnelMachineID)
	}
	if store.updateTunnelID != "tunnel-abc" {
		t.Errorf("expected tunnel_id=tunnel-abc, got %q", store.updateTunnelID)
	}
	if store.updateTunnelSigningKey != result.SigningKey {
		t.Errorf("expected signing key to match result")
	}
}

func TestTeardownRoute_DeletesKVAndTunnel(t *testing.T) {
	tunnel := &mockTunnel{}
	kv := &mockKV{}
	store := &mockStore{}

	svc := New(tunnel, kv, store)
	req := TeardownRequest{
		MachineID:   "machine-1",
		MachineSlug: "my-machine",
		AccountSlug: "acme",
		TunnelID:    "tunnel-abc",
	}

	if err := svc.TeardownRoute(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// KV route was deleted
	if !kv.deleteRouteCalled {
		t.Error("expected DeleteRouteSync to be called")
	}
	if kv.deleteRouteAccountSlug != "acme" {
		t.Errorf("expected account_slug=acme, got %q", kv.deleteRouteAccountSlug)
	}
	if kv.deleteRouteMachineSlug != "my-machine" {
		t.Errorf("expected machine_slug=my-machine, got %q", kv.deleteRouteMachineSlug)
	}

	// Tunnel and DNS were deleted
	if !tunnel.deleteCalled {
		t.Error("expected DeleteTunnelAndDNS to be called")
	}
	if tunnel.deleteTunnelID != "tunnel-abc" {
		t.Errorf("expected tunnelID=tunnel-abc, got %q", tunnel.deleteTunnelID)
	}
	if len(tunnel.deleteHostnames) != 2 {
		t.Errorf("expected 2 hostnames for deletion, got %d: %v", len(tunnel.deleteHostnames), tunnel.deleteHostnames)
	}

	// DB was cleared
	if !store.clearTunnelCalled {
		t.Error("expected ClearMachineTunnel to be called")
	}
	if store.clearTunnelMachineID != "machine-1" {
		t.Errorf("expected machine_id=machine-1, got %q", store.clearTunnelMachineID)
	}
}

func TestTeardownRoute_SkipsWhenNoTunnel(t *testing.T) {
	tunnel := &mockTunnel{}
	kv := &mockKV{}
	store := &mockStore{}

	svc := New(tunnel, kv, store)
	req := TeardownRequest{
		MachineID:   "machine-1",
		MachineSlug: "my-machine",
		AccountSlug: "acme",
		TunnelID:    "", // no tunnel
	}

	if err := svc.TeardownRoute(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// KV still deleted even without a tunnel
	if !kv.deleteRouteCalled {
		t.Error("expected DeleteRouteSync to be called even when TunnelID is empty")
	}

	// Tunnel delete was NOT called because TunnelID is empty
	if tunnel.deleteCalled {
		t.Error("expected DeleteTunnelAndDNS to be skipped when TunnelID is empty")
	}

	// DB clear still happens
	if !store.clearTunnelCalled {
		t.Error("expected ClearMachineTunnel to be called")
	}
}

func TestSetupRoute_NilTunnel(t *testing.T) {
	svc := New(nil, nil, nil)
	req := SetupRequest{
		MachineID:   "machine-1",
		MachineSlug: "my-machine",
	}

	result, err := svc.SetupRoute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	} else if wantVM := "m-my-machine." + defaultDataPlaneDomain; result.VMHostname != wantVM {
		t.Errorf("expected VMHostname=%q, got %q", wantVM, result.VMHostname)
	}

	// No tunnel ID since tunnel manager is nil
	if result.TunnelID != "" {
		t.Errorf("expected empty TunnelID, got %q", result.TunnelID)
	}
}

func TestSyncRouteToKV(t *testing.T) {
	kv := &mockKV{}
	svc := New(nil, kv, nil)

	req := SyncKVRequest{
		AccountSlug:  "acme",
		MachineSlug:  "my-machine",
		MachineID:    "machine-1",
		HostHostname: "host.example.com",
		ProxyToken:   "proxy-tok",
	}

	if err := svc.SyncRouteToKV(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !kv.putRouteCalled {
		t.Error("expected PutRouteSync to be called")
	}
	if kv.putRouteAccountSlug != "acme" {
		t.Errorf("expected account_slug=acme, got %q", kv.putRouteAccountSlug)
	}
	if kv.putRouteEntry.MachineID != "machine-1" {
		t.Errorf("expected MachineID=machine-1, got %q", kv.putRouteEntry.MachineID)
	}
	if kv.putRouteEntry.HostHostname != "host.example.com" {
		t.Errorf("expected HostHostname=host.example.com, got %q", kv.putRouteEntry.HostHostname)
	}
	if kv.putRouteEntry.ProxyToken != "proxy-tok" {
		t.Errorf("expected ProxyToken=proxy-tok, got %q", kv.putRouteEntry.ProxyToken)
	}
}

func TestSyncRouteToKV_NilKV(t *testing.T) {
	svc := New(nil, nil, nil)
	err := svc.SyncRouteToKV(context.Background(), SyncKVRequest{
		AccountSlug: "acme",
		MachineSlug: "my-machine",
	})
	if err != nil {
		t.Fatalf("expected nil error when KV is nil, got: %v", err)
	}
}

func TestTeardownRoute_BestEffort_ContinuesOnKVError(t *testing.T) {
	tunnel := &mockTunnel{}
	kv := &mockKV{deleteRouteErr: errors.New("KV unavailable")}
	store := &mockStore{}

	svc := New(tunnel, kv, store)
	req := TeardownRequest{
		MachineID:   "machine-1",
		MachineSlug: "my-machine",
		AccountSlug: "acme",
		TunnelID:    "tunnel-abc",
	}

	// Should return nil even if KV fails (best-effort)
	if err := svc.TeardownRoute(context.Background(), req); err != nil {
		t.Fatalf("expected nil error (best-effort), got: %v", err)
	}

	// Tunnel delete should still have been attempted
	if !tunnel.deleteCalled {
		t.Error("expected DeleteTunnelAndDNS to be called despite KV error")
	}

	// DB clear should still have been attempted
	if !store.clearTunnelCalled {
		t.Error("expected ClearMachineTunnel to be called despite KV error")
	}
}
