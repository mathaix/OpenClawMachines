package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// ---- Mock store ----

type mockReconcilerStore struct {
	staleHosts       []store.Host
	staleErr         error
	unreachableHosts []store.Host
	unreachableErr   error

	updatedStatuses map[int]string // host_id → status
	statusMessages  map[int]string // host_id → message

	markedMachineIDs []string
	markErr          error
	markHostID       int
	markMessage      string

	machinesByHost map[int][]store.Machine

	machineStatuses map[string]struct {
		status  string
		message *string
	}

	releasedPlacements []string
}

func newMockStore() *mockReconcilerStore {
	return &mockReconcilerStore{
		updatedStatuses: make(map[int]string),
		statusMessages:  make(map[int]string),
		machinesByHost:  make(map[int][]store.Machine),
		machineStatuses: make(map[string]struct {
			status  string
			message *string
		}),
	}
}

func (m *mockReconcilerStore) ListStaleHosts(_ context.Context, _ time.Duration) ([]store.Host, error) {
	return m.staleHosts, m.staleErr
}
func (m *mockReconcilerStore) ListUnreachableHosts(_ context.Context) ([]store.Host, error) {
	return m.unreachableHosts, m.unreachableErr
}
func (m *mockReconcilerStore) SetHostMaintenanceMode(context.Context, int, bool) error {
	return nil
}
func (m *mockReconcilerStore) ReconcileHostCapacityVCPUs(context.Context, int) (int, error) {
	return 0, nil
}
func (m *mockReconcilerStore) UpdateHostStatus(_ context.Context, id int, status string) error {
	m.updatedStatuses[id] = status
	return nil
}
func (m *mockReconcilerStore) UpdateHostStatusMessage(_ context.Context, id int, status, message string) error {
	m.updatedStatuses[id] = status
	m.statusMessages[id] = message
	return nil
}
func (m *mockReconcilerStore) MarkMachinesOnHostError(_ context.Context, hostID int, message string) ([]string, error) {
	m.markHostID = hostID
	m.markMessage = message
	return m.markedMachineIDs, m.markErr
}
func (m *mockReconcilerStore) ListMachinesByHost(_ context.Context, hostID int) ([]store.Machine, error) {
	return m.machinesByHost[hostID], nil
}
func (m *mockReconcilerStore) UpdateMachineStatus(_ context.Context, id, status string, msg *string) error {
	m.machineStatuses[id] = struct {
		status  string
		message *string
	}{status, msg}
	return nil
}
func (m *mockReconcilerStore) ReleasePlacement(_ context.Context, machineID string) error {
	m.releasedPlacements = append(m.releasedPlacements, machineID)
	return nil
}

// ---- Operation methods (satisfy interface) ----

func (m *mockReconcilerStore) CreateMachineOperation(context.Context, *store.MachineOperation) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) GetActiveMachineOperation(context.Context, string) (*store.MachineOperation, error) {
	panic("not implemented")
}
func (m *mockReconcilerStore) CompleteMachineOperation(context.Context, string, string, *string) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) ExpireStaleOperations(_ context.Context, _ time.Duration) (int, error) {
	return 0, nil
}
func (m *mockReconcilerStore) FindStuckMachines(_ context.Context, _ time.Duration) ([]store.Machine, error) {
	return nil, nil
}

// ---- Unused store methods (satisfy interface) ----

func (m *mockReconcilerStore) CreateHost(context.Context, *store.Host) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) GetHost(context.Context, int) (*store.Host, error) {
	panic("not implemented")
}
func (m *mockReconcilerStore) ListHosts(context.Context) ([]store.Host, error) {
	panic("not implemented")
}
func (m *mockReconcilerStore) FindHostWithCapacity(context.Context, int, int, string, string) (*store.Host, error) {
	panic("not implemented")
}
func (m *mockReconcilerStore) AllocateHostCapacity(context.Context, int, int, int) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) ReleaseHostCapacity(context.Context, int, int, int) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) ReleaseHostCapacityWithDisk(context.Context, int, int, int, int) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) ListEligibleHosts(context.Context, store.HostFilter) ([]store.Host, error) {
	panic("not implemented")
}
func (m *mockReconcilerStore) PlaceOnHost(context.Context, int, string, int, int, int) (string, error) {
	panic("not implemented")
}
func (m *mockReconcilerStore) UpdateHostDetails(context.Context, *store.Host) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) DeleteHost(context.Context, int) error { panic("not implemented") }
func (m *mockReconcilerStore) UpdateHostSourceImage(context.Context, int, string) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) UpdateHostVersions(context.Context, int, string, string) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) UpdateHostHeartbeat(context.Context, int, string, string, string, string, string, json.RawMessage) (bool, error) {
	panic("not implemented")
}
func (m *mockReconcilerStore) CreateMachine(context.Context, *store.Machine) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) GetMachine(context.Context, string) (*store.Machine, error) {
	panic("not implemented")
}
func (m *mockReconcilerStore) GetMachineBySlug(context.Context, string) (*store.Machine, error) {
	panic("not implemented")
}
func (m *mockReconcilerStore) ListMachinesByAccount(context.Context, int) ([]store.Machine, error) {
	panic("not implemented")
}
func (m *mockReconcilerStore) ListAllMachines(context.Context) ([]store.Machine, error) {
	panic("not implemented")
}
func (m *mockReconcilerStore) UpdateMachine(context.Context, *store.Machine) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) DeleteMachine(context.Context, string) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) AssignMachineToHost(context.Context, string, int, string) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) UnassignMachineFromHost(context.Context, string) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) SetMachineTokens(context.Context, string, string, string) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) UpdateMachineVersion(context.Context, string, string, string) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) UpdateMachineTunnel(context.Context, string, string, string) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) ClearMachineTunnel(context.Context, string) error {
	panic("not implemented")
}
func (m *mockReconcilerStore) IsMachineActive(context.Context, string) (bool, error) {
	panic("not implemented")
}
func (m *mockReconcilerStore) UpdateMachineStatusCAS(context.Context, string, string, string, *string) (bool, error) {
	panic("not implemented")
}

// ---- Mock instance checker ----

type mockInstanceChecker struct {
	results map[string]bool  // vmName → exists
	errors  map[string]error // vmName → error
}

func (c *mockInstanceChecker) InstanceExists(_ context.Context, _, _, vmName string) (bool, error) {
	if err, ok := c.errors[vmName]; ok {
		return false, err
	}
	exists, ok := c.results[vmName]
	if !ok {
		return false, nil // default: not found
	}
	return exists, nil
}

// ---- Mock machine cleanup ----

type mockMachineCleanup struct {
	cleaned []string
}

func (c *mockMachineCleanup) CleanupMachineRouteAndTunnel(_ context.Context, machineID string) error {
	c.cleaned = append(c.cleaned, machineID)
	return nil
}

// ---- Tests ----

func TestReconciler_StaleHostBecomesUnreachable(t *testing.T) {
	ms := newMockStore()
	ms.staleHosts = []store.Host{
		{ID: 1, VMName: "host-1", Zone: "us-central1-a"},
		{ID: 2, VMName: "host-2", Zone: "us-central1-a"},
	}

	ic := &mockInstanceChecker{results: make(map[string]bool)}
	mc := &mockMachineCleanup{}

	r := New(ms, ic, mc, "test-project", 180*time.Second)
	r.reconcileOnce(context.Background())

	if ms.updatedStatuses[1] != "unreachable" {
		t.Fatalf("expected host 1 to be unreachable, got %q", ms.updatedStatuses[1])
	}
	if ms.updatedStatuses[2] != "unreachable" {
		t.Fatalf("expected host 2 to be unreachable, got %q", ms.updatedStatuses[2])
	}
}

func TestReconciler_UnreachableHostTerminated(t *testing.T) {
	ms := newMockStore()
	ms.unreachableHosts = []store.Host{
		{ID: 10, VMName: "dead-host", Zone: "us-central1-a"},
	}
	ms.markedMachineIDs = []string{"m-001", "m-002"}
	ms.machinesByHost[10] = []store.Machine{
		{ID: "m-003", Status: "stopped"},
	}

	ic := &mockInstanceChecker{
		results: map[string]bool{"dead-host": false}, // instance gone
	}
	mc := &mockMachineCleanup{}

	r := New(ms, ic, mc, "test-project", 180*time.Second)
	r.reconcileOnce(context.Background())

	if ms.updatedStatuses[10] != "terminated" {
		t.Fatalf("expected host to be terminated, got %q", ms.updatedStatuses[10])
	}
	if ms.markHostID != 10 {
		t.Fatalf("expected MarkMachinesOnHostError for host 10, got %d", ms.markHostID)
	}
	if len(mc.cleaned) != 2 {
		t.Fatalf("expected 2 machines cleaned up, got %d", len(mc.cleaned))
	}

	// Check placements were released for affected machines
	if len(ms.releasedPlacements) != 2 {
		t.Fatalf("expected 2 placements released, got %d", len(ms.releasedPlacements))
	}

	// Check stopped machine got status message
	if entry, ok := ms.machineStatuses["m-003"]; !ok {
		t.Fatal("expected stopped machine m-003 to get status message")
	} else if entry.message == nil || *entry.message != "data volume lost (host terminated)" {
		t.Fatalf("expected data loss message, got %v", entry.message)
	}
}

func TestReconciler_UnreachableHostStaysUnreachable(t *testing.T) {
	ms := newMockStore()
	ms.unreachableHosts = []store.Host{
		{ID: 20, VMName: "slow-host", Zone: "us-central1-a"},
	}

	ic := &mockInstanceChecker{
		results: map[string]bool{"slow-host": true}, // instance still exists
	}
	mc := &mockMachineCleanup{}

	r := New(ms, ic, mc, "test-project", 180*time.Second)
	r.reconcileOnce(context.Background())

	// Should NOT be marked terminated — still exists
	if status, ok := ms.updatedStatuses[20]; ok {
		t.Fatalf("expected no status update for host 20, got %q", status)
	}
}

func TestReconciler_KeepsUnreachableOnAPIError(t *testing.T) {
	ms := newMockStore()
	ms.unreachableHosts = []store.Host{
		{ID: 30, VMName: "err-host", Zone: "us-central1-a"},
	}

	ic := &mockInstanceChecker{
		errors: map[string]error{"err-host": fmt.Errorf("API unavailable")},
	}
	mc := &mockMachineCleanup{}

	r := New(ms, ic, mc, "test-project", 180*time.Second)
	r.reconcileOnce(context.Background())

	// Should NOT be moved to error — stays unreachable, will retry next tick
	if status, ok := ms.updatedStatuses[30]; ok {
		t.Fatalf("expected no status update for host 30 (stay unreachable), got %q", status)
	}
}

func TestReconciler_CleanupMachinesOnTermination(t *testing.T) {
	ms := newMockStore()
	ms.unreachableHosts = []store.Host{
		{ID: 40, VMName: "dead-host-2", Zone: "us-central1-b"},
	}
	ms.markedMachineIDs = []string{"m-100", "m-101", "m-102"}

	ic := &mockInstanceChecker{
		results: map[string]bool{"dead-host-2": false},
	}
	mc := &mockMachineCleanup{}

	r := New(ms, ic, mc, "test-project", 180*time.Second)
	r.reconcileOnce(context.Background())

	if len(mc.cleaned) != 3 {
		t.Fatalf("expected 3 machines cleaned, got %d", len(mc.cleaned))
	}
	expected := map[string]bool{"m-100": true, "m-101": true, "m-102": true}
	for _, id := range mc.cleaned {
		if !expected[id] {
			t.Fatalf("unexpected machine cleaned: %s", id)
		}
	}
}

func TestReconciler_NonGCPHostUsesHeartbeatOnlyChecker(t *testing.T) {
	// Non-GCP unreachable host should use HeartbeatOnlyChecker (always returns true),
	// so the host stays unreachable rather than being terminated.
	tests := []struct {
		name     string
		provider string
	}{
		{name: "ovhcloud provider", provider: "ovhcloud"},
		{name: "hetzner provider", provider: "hetzner"},
		{name: "custom provider", provider: "customer-owned"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newMockStore()
			ms.unreachableHosts = []store.Host{
				{ID: 50, VMName: "ext-host", Zone: "external", Provider: tt.provider},
			}

			// GCP checker says instance is gone — but it should NOT be consulted for non-GCP hosts
			ic := &mockInstanceChecker{
				results: map[string]bool{"ext-host": false},
			}
			mc := &mockMachineCleanup{}

			r := New(ms, ic, mc, "test-project", 180*time.Second)
			r.reconcileOnce(context.Background())

			// Non-GCP host should NOT be terminated because HeartbeatOnlyChecker returns true
			if status, ok := ms.updatedStatuses[50]; ok {
				t.Fatalf("expected no status update for non-GCP host (stays unreachable), got %q", status)
			}
		})
	}
}

func TestReconciler_GCPHostUsesGCPChecker(t *testing.T) {
	// GCP hosts (provider="" or provider="gcp") should use the GCP instance checker.
	tests := []struct {
		name     string
		provider string
	}{
		{name: "empty provider (legacy GCP)", provider: ""},
		{name: "explicit gcp provider", provider: "gcp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newMockStore()
			ms.unreachableHosts = []store.Host{
				{ID: 60, VMName: "gcp-host", Zone: "us-central1-a", Provider: tt.provider},
			}
			ms.markedMachineIDs = []string{}

			// GCP checker says instance is gone → host should be terminated
			ic := &mockInstanceChecker{
				results: map[string]bool{"gcp-host": false},
			}
			mc := &mockMachineCleanup{}

			r := New(ms, ic, mc, "test-project", 180*time.Second)
			r.reconcileOnce(context.Background())

			if ms.updatedStatuses[60] != "terminated" {
				t.Fatalf("expected GCP host to be terminated, got %q", ms.updatedStatuses[60])
			}
		})
	}
}

func TestReconciler_StaleHeartbeatDetection(t *testing.T) {
	// Stale hosts (ready but heartbeat too old) should be marked unreachable
	// regardless of provider.
	past := time.Now().Add(-10 * time.Minute)
	ms := newMockStore()
	ms.staleHosts = []store.Host{
		{ID: 70, VMName: "stale-gcp", Zone: "us-central1-a", Provider: "gcp", LastHeartbeat: &past},
		{ID: 71, VMName: "stale-ovh", Zone: "external", Provider: "ovhcloud", LastHeartbeat: &past},
	}

	ic := &mockInstanceChecker{results: make(map[string]bool)}
	mc := &mockMachineCleanup{}

	r := New(ms, ic, mc, "test-project", 180*time.Second)
	r.reconcileOnce(context.Background())

	if ms.updatedStatuses[70] != "unreachable" {
		t.Fatalf("expected stale GCP host to be unreachable, got %q", ms.updatedStatuses[70])
	}
	if ms.updatedStatuses[71] != "unreachable" {
		t.Fatalf("expected stale OVH host to be unreachable, got %q", ms.updatedStatuses[71])
	}
}
