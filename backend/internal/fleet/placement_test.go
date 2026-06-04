package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// mockPlacementStore tracks which methods were called and returns configured results.
type mockPlacementStore struct {
	// PlaceMachineOnHost (legacy)
	placeMachineHost   *store.Host
	placeMachineVMIP   string
	placeMachineErr    error
	placeMachineCalled bool

	// ListEligibleHosts
	listEligibleHosts    []store.Host
	listEligibleHostsErr error
	lastHostFilter       store.HostFilter

	// PlaceOnHost
	placeOnHostVMIP   string
	placeOnHostErr    error
	placeOnHostCalled bool
	placeOnHostFunc   func(hostID int) (string, error) // per-call override

	// ReservePlacement
	reservePlacementResult *store.Placement
	reservePlacementErr    error
	reservePlacementCalled bool

	// ActivatePlacement
	activatePlacementOK     bool
	activatePlacementErr    error
	activatePlacementCalled bool
	activatePlacementID     string

	// ReleasePlacement
	releasePlacementCalled bool

	// GetMachine
	getMachineResult *store.Machine
	getMachineErr    error

	// GetHost
	getHostResult *store.Host
	getHostErr    error

	// SoftStopMachine
	softStopCalled    bool
	softStopMachineID string
	softStopHostID    int

	// ReleaseHostCapacity
	releaseCapacityCalled bool
	releaseCapacityHostID int

	// ReleaseHostCapacityWithDisk
	releaseCapacityWithDiskCalled bool
	releaseCapacityWithDiskHostID int

	// UnassignMachineFromHost
	unassignCalled    bool
	unassignMachineID string

	// UpdateMachineHomeHost
	updateHomeHostCalled    bool
	updateHomeHostMachineID string
	updateHomeHostID        *int

	// ReAllocateHostCapacity
	reAllocateCalled bool
	reAllocateVMIP   string
	reAllocateErr    error

	// Browser VM placement
	placeBrowserCalled bool
	placeBrowserHostID int
}

func (m *mockPlacementStore) PlaceMachineOnHost(_ context.Context, machineID string, vcpus, memoryMB int, region, expectedImage string) (*store.Host, string, error) {
	m.placeMachineCalled = true
	if m.placeMachineErr != nil {
		return nil, "", m.placeMachineErr
	}
	return m.placeMachineHost, m.placeMachineVMIP, nil
}

func (m *mockPlacementStore) PlaceMachineOnSpecificHost(_ context.Context, _ string, _ int, _, _ int) (*store.Host, string, error) {
	m.placeMachineCalled = true
	if m.placeMachineErr != nil {
		return nil, "", m.placeMachineErr
	}
	return m.placeMachineHost, m.placeMachineVMIP, nil
}

func (m *mockPlacementStore) ListEligibleHosts(_ context.Context, filter store.HostFilter) ([]store.Host, error) {
	m.lastHostFilter = filter
	if m.listEligibleHostsErr != nil {
		return nil, m.listEligibleHostsErr
	}
	return m.listEligibleHosts, nil
}

func (m *mockPlacementStore) PlaceOnHost(_ context.Context, hostID int, machineID string, vcpus, memoryMB, diskMB int) (string, error) {
	m.placeOnHostCalled = true
	if m.placeOnHostFunc != nil {
		return m.placeOnHostFunc(hostID)
	}
	if m.placeOnHostErr != nil {
		return "", m.placeOnHostErr
	}
	return m.placeOnHostVMIP, nil
}

func (m *mockPlacementStore) ReservePlacement(_ context.Context, machineID string, hostID int, vmIP, operationID string) (*store.Placement, error) {
	m.reservePlacementCalled = true
	if m.reservePlacementErr != nil {
		return nil, m.reservePlacementErr
	}
	if m.reservePlacementResult != nil {
		return m.reservePlacementResult, nil
	}
	return &store.Placement{ID: "pl-001", MachineID: machineID, HostID: hostID, VMIP: vmIP}, nil
}

func (m *mockPlacementStore) ActivatePlacement(_ context.Context, placementID string) (bool, error) {
	m.activatePlacementCalled = true
	m.activatePlacementID = placementID
	return m.activatePlacementOK, m.activatePlacementErr
}

func (m *mockPlacementStore) ReleasePlacement(_ context.Context, machineID string) error {
	m.releasePlacementCalled = true
	return nil
}

func (m *mockPlacementStore) GetActivePlacement(_ context.Context, machineID string) (*store.Placement, error) {
	panic("not implemented")
}

func (m *mockPlacementStore) ListActivePlacementsByHost(_ context.Context, hostID int) ([]store.Placement, error) {
	panic("not implemented")
}

func (m *mockPlacementStore) ResolveCapacityPolicy(_ context.Context, hostID int, hostPool string) (*store.CapacityPolicy, error) {
	panic("not implemented")
}

func (m *mockPlacementStore) ReleaseMigrationSource(_ context.Context, _ string, _ int, _, _ int, _ bool) error {
	return nil
}

func (m *mockPlacementStore) UpdateMachineHomeHost(_ context.Context, machineID string, homeHostID *int) error {
	m.updateHomeHostCalled = true
	m.updateHomeHostMachineID = machineID
	m.updateHomeHostID = homeHostID
	return nil
}

func (m *mockPlacementStore) SoftStopMachine(_ context.Context, machineID string, hostID, vcpus, memoryMB, diskMB int) error {
	m.softStopCalled = true
	m.softStopMachineID = machineID
	m.softStopHostID = hostID
	return nil
}

func (m *mockPlacementStore) ReAllocateHostCapacity(_ context.Context, machineID string, hostID, vcpus, memoryMB, diskMB int) (string, error) {
	m.reAllocateCalled = true
	if m.reAllocateErr != nil {
		return "", m.reAllocateErr
	}
	if m.reAllocateVMIP != "" {
		return m.reAllocateVMIP, nil
	}
	if m.getMachineResult != nil && m.getMachineResult.VMIP != nil {
		return *m.getMachineResult.VMIP, nil
	}
	return "", nil
}

func (m *mockPlacementStore) GetMachine(_ context.Context, id string) (*store.Machine, error) {
	if m.getMachineErr != nil {
		return nil, m.getMachineErr
	}
	return m.getMachineResult, nil
}

func (m *mockPlacementStore) GetHost(_ context.Context, id int) (*store.Host, error) {
	if m.getHostErr != nil {
		return nil, m.getHostErr
	}
	return m.getHostResult, nil
}

func (m *mockPlacementStore) ReleaseHostCapacity(_ context.Context, hostID, vcpus, memoryMB int) error {
	m.releaseCapacityCalled = true
	m.releaseCapacityHostID = hostID
	return nil
}

func (m *mockPlacementStore) ReleaseHostCapacityWithDisk(_ context.Context, hostID, vcpus, memoryMB, diskMB int) error {
	m.releaseCapacityWithDiskCalled = true
	m.releaseCapacityWithDiskHostID = hostID
	return nil
}

func (m *mockPlacementStore) UnassignMachineFromHost(_ context.Context, machineID string) error {
	m.unassignCalled = true
	m.unassignMachineID = machineID
	return nil
}

// Stub out remaining HostRepo methods that PlacementStore requires.
func (m *mockPlacementStore) CreateHost(context.Context, *store.Host) error {
	panic("not implemented")
}
func (m *mockPlacementStore) ListHosts(context.Context) ([]store.Host, error) {
	panic("not implemented")
}
func (m *mockPlacementStore) FindHostWithCapacity(context.Context, int, int, string, string) (*store.Host, error) {
	panic("not implemented")
}
func (m *mockPlacementStore) AllocateHostCapacity(context.Context, int, int, int) error {
	panic("not implemented")
}
func (m *mockPlacementStore) UpdateHostStatus(context.Context, int, string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) UpdateHostStatusMessage(context.Context, int, string, string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) UpdateHostDetails(context.Context, *store.Host) error {
	panic("not implemented")
}
func (m *mockPlacementStore) DeleteHost(context.Context, int) error { panic("not implemented") }
func (m *mockPlacementStore) UpdateHostSourceImage(context.Context, int, string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) UpdateHostVersions(context.Context, int, string, string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) UpdateHostHeartbeat(context.Context, int, string, string, string, string, string, json.RawMessage) (bool, error) {
	panic("not implemented")
}
func (m *mockPlacementStore) ListStaleHosts(context.Context, time.Duration) ([]store.Host, error) {
	panic("not implemented")
}
func (m *mockPlacementStore) ListUnreachableHosts(context.Context) ([]store.Host, error) {
	panic("not implemented")
}
func (m *mockPlacementStore) SetHostMaintenanceMode(context.Context, int, bool) error {
	return nil
}
func (m *mockPlacementStore) ReconcileHostCapacityVCPUs(context.Context, int) (int, error) {
	panic("not implemented")
}

// Stub out remaining MachineRepo methods.
func (m *mockPlacementStore) CreateMachine(context.Context, *store.Machine) error {
	panic("not implemented")
}
func (m *mockPlacementStore) GetMachineBySlug(context.Context, string) (*store.Machine, error) {
	panic("not implemented")
}
func (m *mockPlacementStore) ListMachinesByAccount(context.Context, int) ([]store.Machine, error) {
	panic("not implemented")
}
func (m *mockPlacementStore) ListMachinesByHost(context.Context, int) ([]store.Machine, error) {
	panic("not implemented")
}
func (m *mockPlacementStore) ListAllMachines(context.Context) ([]store.Machine, error) {
	panic("not implemented")
}
func (m *mockPlacementStore) UpdateMachine(context.Context, *store.Machine) error {
	panic("not implemented")
}
func (m *mockPlacementStore) DeleteMachine(context.Context, string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) UpdateMachineStatus(context.Context, string, string, *string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) AssignMachineToHost(context.Context, string, int, string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) SetMachineTokens(context.Context, string, string, string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) UpdateMachineVersion(context.Context, string, string, string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) UpdateMachineTunnel(context.Context, string, string, string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) ClearMachineTunnel(context.Context, string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) IsMachineActive(context.Context, string) (bool, error) {
	panic("not implemented")
}
func (m *mockPlacementStore) MarkMachinesOnHostError(context.Context, int, string) ([]string, error) {
	panic("not implemented")
}
func (m *mockPlacementStore) UpdateMachineStatusCAS(context.Context, string, string, string, *string) (bool, error) {
	panic("not implemented")
}
func (m *mockPlacementStore) FindStuckMachines(context.Context, time.Duration) ([]store.Machine, error) {
	return nil, nil
}

// Stub out BrowserVMRepo methods that PlacementStore now requires.
func (m *mockPlacementStore) CreateBrowserVM(context.Context, *store.BrowserVM) error {
	panic("not implemented")
}
func (m *mockPlacementStore) GetBrowserVM(context.Context, string) (*store.BrowserVM, error) {
	panic("not implemented")
}
func (m *mockPlacementStore) ListBrowserVMsByAccount(context.Context, int) ([]store.BrowserVM, error) {
	panic("not implemented")
}
func (m *mockPlacementStore) ListBrowserVMsRequiringReconciliation(context.Context) ([]store.BrowserVM, error) {
	panic("not implemented")
}
func (m *mockPlacementStore) UpdateBrowserVMStatus(context.Context, string, string, *string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) AssignBrowserVMToHost(context.Context, string, int, string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) UnassignBrowserVMFromHost(context.Context, string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) DeleteBrowserVM(context.Context, string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) PairBrowserVM(context.Context, string, string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) UnpairBrowserVM(context.Context, string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) GetMachineByBrowserVMID(context.Context, string) (*store.Machine, error) {
	panic("not implemented")
}
func (m *mockPlacementStore) PlaceBrowserVMOnHost(_ context.Context, hostID int, browserVMID string, vcpus, memoryMB int) (string, error) {
	m.placeBrowserCalled = true
	m.placeBrowserHostID = hostID
	if m.placeOnHostFunc != nil {
		return m.placeOnHostFunc(hostID)
	}
	if m.placeOnHostErr != nil {
		return "", m.placeOnHostErr
	}
	return m.placeOnHostVMIP, nil
}
func (m *mockPlacementStore) ActivateBrowserVMPlacement(context.Context, string) error {
	panic("not implemented")
}
func (m *mockPlacementStore) ReleaseBrowserVMPlacement(context.Context, string) error {
	panic("not implemented")
}

func testConfig() PlacementConfig {
	return PlacementConfig{
		DefaultRegion:    "us-central1",
		ExpectedImage:    "img-v1",
		Strategy:         Random,
		DefaultDiskPerVM: DefaultDiskPerVM,
	}
}

// --- Tests ---

func TestReserve_Success(t *testing.T) {
	host := store.Host{ID: 7, VMName: "host-1"}
	ms := &mockPlacementStore{
		listEligibleHosts: []store.Host{host},
		placeOnHostVMIP:   "192.168.1.10",
	}
	svc := NewPlacementService(ms, testConfig())

	placement, gotHost, vmIP, err := svc.Reserve(context.Background(), "m-001", PlacementRequest{
		VCPUs:       2,
		MemoryMB:    2048,
		OperationID: "op-001",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ms.placeOnHostCalled {
		t.Fatal("PlaceOnHost should have been called")
	}
	if !ms.reservePlacementCalled {
		t.Fatal("ReservePlacement should have been called")
	}
	if gotHost.ID != 7 {
		t.Fatalf("expected host ID 7, got %d", gotHost.ID)
	}
	if vmIP != "192.168.1.10" {
		t.Fatalf("expected vmIP 192.168.1.10, got %s", vmIP)
	}
	if placement == nil {
		t.Fatal("expected placement record, got nil")
	}
}

func TestReserve_NoHost(t *testing.T) {
	ms := &mockPlacementStore{
		listEligibleHosts: nil, // no hosts
	}
	svc := NewPlacementService(ms, testConfig())

	_, _, _, err := svc.Reserve(context.Background(), "m-002", PlacementRequest{
		VCPUs:    4,
		MemoryMB: 4096,
	})
	if err == nil {
		t.Fatal("expected error when no host available")
	}
}

func TestReserveBrowserVM_TargetHost(t *testing.T) {
	ms := &mockPlacementStore{
		getHostResult:     &store.Host{ID: 42, VMName: "target-host"},
		placeOnHostVMIP:   "192.168.100.42",
		listEligibleHosts: []store.Host{{ID: 7, VMName: "wrong-host"}},
	}
	svc := NewPlacementService(ms, testConfig())

	host, vmIP, err := svc.ReserveBrowserVM(context.Background(), "bvm-001", PlacementRequest{
		VCPUs:        2,
		MemoryMB:     4096,
		TargetHostID: 42,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ms.placeBrowserCalled {
		t.Fatal("PlaceBrowserVMOnHost should have been called")
	}
	if ms.placeBrowserHostID != 42 {
		t.Fatalf("expected target host 42, got %d", ms.placeBrowserHostID)
	}
	if host.ID != 42 {
		t.Fatalf("expected host 42, got %d", host.ID)
	}
	if vmIP != "192.168.100.42" {
		t.Fatalf("expected vmIP 192.168.100.42, got %s", vmIP)
	}
}

func TestReserveBrowserVM_RequiresBrowserReflinkAutoPlacement(t *testing.T) {
	ms := &mockPlacementStore{
		listEligibleHosts: []store.Host{
			{ID: 41, VMName: "ext4-host", Capabilities: browserReflinkCaps(false)},
			{ID: 42, VMName: "xfs-host", Capabilities: browserReflinkCaps(true)},
		},
		placeOnHostFunc: func(hostID int) (string, error) {
			if hostID != 42 {
				t.Fatalf("PlaceBrowserVMOnHost called for non-reflink host %d", hostID)
			}
			return "192.168.100.42", nil
		},
	}
	svc := NewPlacementService(ms, testConfig())

	host, vmIP, err := svc.ReserveBrowserVM(context.Background(), "bvm-001", PlacementRequest{
		VCPUs:                 2,
		MemoryMB:              4096,
		RequireBrowserReflink: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ms.lastHostFilter.RequireBrowserReflink {
		t.Fatal("ListEligibleHosts should require browser reflink")
	}
	if host.ID != 42 {
		t.Fatalf("expected reflink host 42, got %d", host.ID)
	}
	if vmIP != "192.168.100.42" {
		t.Fatalf("expected vmIP 192.168.100.42, got %s", vmIP)
	}
}

func TestReserveBrowserVM_RequiresBrowserReflinkRejectsTargetHost(t *testing.T) {
	ms := &mockPlacementStore{
		getHostResult: &store.Host{ID: 42, VMName: "ext4-host", Capabilities: browserReflinkCaps(false)},
	}
	svc := NewPlacementService(ms, testConfig())

	_, _, err := svc.ReserveBrowserVM(context.Background(), "bvm-001", PlacementRequest{
		VCPUs:                 2,
		MemoryMB:              4096,
		TargetHostID:          42,
		RequireBrowserReflink: true,
	})
	if err == nil || !strings.Contains(err.Error(), "does not report reflink-capable browser storage") {
		t.Fatalf("error = %v, want reflink storage rejection", err)
	}
	if ms.placeBrowserCalled {
		t.Fatal("PlaceBrowserVMOnHost should not be called for non-reflink target")
	}
}

func browserReflinkCaps(supported bool) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"browser_storage":{"reflink_supported":%t}}`, supported))
}

func TestReserve_PlacementRecordFailure_StillSucceeds(t *testing.T) {
	host := store.Host{ID: 5, VMName: "host-2"}
	ms := &mockPlacementStore{
		listEligibleHosts:   []store.Host{host},
		placeOnHostVMIP:     "192.168.1.20",
		reservePlacementErr: fmt.Errorf("db error"),
	}
	svc := NewPlacementService(ms, testConfig())

	placement, gotHost, vmIP, err := svc.Reserve(context.Background(), "m-003", PlacementRequest{
		VCPUs:    2,
		MemoryMB: 2048,
	})
	if err != nil {
		t.Fatalf("should succeed even if placement record fails: %v", err)
	}
	if placement != nil {
		t.Fatal("placement should be nil when record creation fails")
	}
	if gotHost.ID != 5 {
		t.Fatalf("expected host ID 5, got %d", gotHost.ID)
	}
	if vmIP != "192.168.1.20" {
		t.Fatalf("expected vmIP 192.168.1.20, got %s", vmIP)
	}
}

func TestReserve_FallbackToNextHost(t *testing.T) {
	host1 := store.Host{ID: 1, VMName: "host-1", UsedMemoryMB: 200}
	host2 := store.Host{ID: 2, VMName: "host-2", UsedMemoryMB: 100}

	ms := &mockPlacementStore{
		listEligibleHosts: []store.Host{host1, host2},
		placeOnHostFunc: func(hostID int) (string, error) {
			if hostID == 1 {
				return "", fmt.Errorf("host contended")
			}
			return "192.168.1.20", nil
		},
	}
	// BinPack: most loaded first → host1 (200MB), then host2 (100MB)
	svc := NewPlacementService(ms, PlacementConfig{
		DefaultRegion:    "us-central1",
		ExpectedImage:    "img-v1",
		Strategy:         BinPack,
		DefaultDiskPerVM: DefaultDiskPerVM,
	})

	placement, gotHost, vmIP, err := svc.Reserve(context.Background(), "m-fallback", PlacementRequest{
		VCPUs:       2,
		MemoryMB:    2048,
		OperationID: "op-fallback",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotHost.ID != 2 {
		t.Fatalf("expected fallback to host 2, got host %d", gotHost.ID)
	}
	if vmIP != "192.168.1.20" {
		t.Fatalf("expected vmIP 192.168.1.20, got %s", vmIP)
	}
	if placement == nil {
		t.Fatal("expected placement record, got nil")
	}
}

func TestActivate_Success(t *testing.T) {
	ms := &mockPlacementStore{
		activatePlacementOK: true,
	}
	svc := NewPlacementService(ms, testConfig())

	err := svc.Activate(context.Background(), "pl-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ms.activatePlacementCalled {
		t.Fatal("ActivatePlacement should have been called")
	}
	if ms.activatePlacementID != "pl-001" {
		t.Fatalf("expected placement ID pl-001, got %s", ms.activatePlacementID)
	}
}

func TestActivate_NotReserved(t *testing.T) {
	ms := &mockPlacementStore{
		activatePlacementOK: false,
	}
	svc := NewPlacementService(ms, testConfig())

	err := svc.Activate(context.Background(), "pl-999")
	if err == nil {
		t.Fatal("expected error when placement not in reserved state")
	}
}

func TestReleaseSoft(t *testing.T) {
	hostID := 42
	ms := &mockPlacementStore{
		getMachineResult: &store.Machine{
			ID:       "m-010",
			HostID:   &hostID,
			VCPUs:    2,
			MemoryMB: 2048,
		},
	}
	svc := NewPlacementService(ms, testConfig())

	err := svc.Release(context.Background(), "m-010", ReleaseSoft)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ms.releasePlacementCalled {
		t.Fatal("ReleasePlacement should have been called")
	}
	if !ms.softStopCalled {
		t.Fatal("SoftStopMachine should have been called")
	}
	if ms.softStopMachineID != "m-010" {
		t.Fatalf("expected machine ID m-010, got %s", ms.softStopMachineID)
	}
	if ms.softStopHostID != 42 {
		t.Fatalf("expected host ID 42, got %d", ms.softStopHostID)
	}
	if !ms.updateHomeHostCalled {
		t.Fatal("UpdateMachineHomeHost should have been called")
	}
	if ms.updateHomeHostID == nil || *ms.updateHomeHostID != 42 {
		t.Fatal("UpdateMachineHomeHost should set home_host_id to current host_id")
	}
	// Hard release methods should NOT be called
	if ms.releaseCapacityWithDiskCalled {
		t.Fatal("ReleaseHostCapacityWithDisk should NOT be called for soft release")
	}
	if ms.unassignCalled {
		t.Fatal("UnassignMachineFromHost should NOT be called for soft release")
	}
}

func TestReleaseHard(t *testing.T) {
	hostID := 99
	ms := &mockPlacementStore{
		getMachineResult: &store.Machine{
			ID:       "m-020",
			HostID:   &hostID,
			VCPUs:    4,
			MemoryMB: 4096,
		},
	}
	svc := NewPlacementService(ms, testConfig())

	err := svc.Release(context.Background(), "m-020", ReleaseHard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ms.releasePlacementCalled {
		t.Fatal("ReleasePlacement should have been called")
	}
	if !ms.releaseCapacityWithDiskCalled {
		t.Fatal("ReleaseHostCapacityWithDisk should have been called")
	}
	if ms.releaseCapacityWithDiskHostID != 99 {
		t.Fatalf("expected host ID 99, got %d", ms.releaseCapacityWithDiskHostID)
	}
	if !ms.unassignCalled {
		t.Fatal("UnassignMachineFromHost should have been called")
	}
	if ms.unassignMachineID != "m-020" {
		t.Fatalf("expected machine ID m-020, got %s", ms.unassignMachineID)
	}
	if !ms.updateHomeHostCalled {
		t.Fatal("UpdateMachineHomeHost should have been called")
	}
	if ms.updateHomeHostID != nil {
		t.Fatal("UpdateMachineHomeHost should set home_host_id to nil for hard release")
	}
	// Soft release methods should NOT be called
	if ms.softStopCalled {
		t.Fatal("SoftStopMachine should NOT be called for hard release")
	}
}

func TestRelease_NoHost(t *testing.T) {
	ms := &mockPlacementStore{
		getMachineResult: &store.Machine{
			ID:     "m-030",
			HostID: nil,
		},
	}
	svc := NewPlacementService(ms, testConfig())

	err := svc.Release(context.Background(), "m-030", ReleaseHard)
	if err != nil {
		t.Fatalf("expected nil error for machine without host, got %v", err)
	}
	if !ms.releasePlacementCalled {
		t.Fatal("ReleasePlacement should still be called")
	}
	if ms.releaseCapacityWithDiskCalled || ms.unassignCalled || ms.softStopCalled {
		t.Fatal("no capacity operations should be called when machine has no host")
	}
}

func TestRecoverAffinity_ReusesHome(t *testing.T) {
	homeHostID := 10
	vmIP := "192.168.1.50"
	now := time.Now()
	ms := &mockPlacementStore{
		getMachineResult: &store.Machine{
			ID:         "m-040",
			HomeHostID: &homeHostID,
			VMIP:       &vmIP,
			VCPUs:      2,
			MemoryMB:   2048,
		},
		getHostResult: &store.Host{
			ID:            10,
			Status:        "ready",
			LastHeartbeat: &now,
		},
	}
	svc := NewPlacementService(ms, testConfig())

	placement, host, gotVMIP, err := svc.RecoverAffinity(context.Background(), "m-040", PlacementRequest{
		VCPUs:       2,
		MemoryMB:    2048,
		OperationID: "op-040",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ms.reAllocateCalled {
		t.Fatal("ReAllocateHostCapacity should have been called for affinity reuse")
	}
	if ms.placeOnHostCalled {
		t.Fatal("PlaceOnHost should NOT be called when affinity reuse succeeds")
	}
	if host.ID != 10 {
		t.Fatalf("expected host ID 10, got %d", host.ID)
	}
	if gotVMIP != "192.168.1.50" {
		t.Fatalf("expected vmIP 192.168.1.50, got %s", gotVMIP)
	}
	if placement == nil {
		t.Fatal("expected placement record")
	}
}

func TestRecoverAffinity_AssignsFreshIPWhenMachineIPMissing(t *testing.T) {
	homeHostID := 10
	now := time.Now()
	ms := &mockPlacementStore{
		getMachineResult: &store.Machine{
			ID:         "m-041",
			HomeHostID: &homeHostID,
			VCPUs:      2,
			MemoryMB:   2048,
		},
		getHostResult: &store.Host{
			ID:            10,
			Status:        "ready",
			LastHeartbeat: &now,
		},
		reAllocateVMIP: "192.168.100.42",
	}
	svc := NewPlacementService(ms, testConfig())

	placement, host, gotVMIP, err := svc.RecoverAffinity(context.Background(), "m-041", PlacementRequest{
		VCPUs:       2,
		MemoryMB:    2048,
		OperationID: "op-041",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ms.reAllocateCalled {
		t.Fatal("ReAllocateHostCapacity should have been called for affinity reuse")
	}
	if ms.placeOnHostCalled {
		t.Fatal("PlaceOnHost should NOT be called when affinity reuse succeeds")
	}
	if host.ID != 10 {
		t.Fatalf("expected host ID 10, got %d", host.ID)
	}
	if gotVMIP != "192.168.100.42" {
		t.Fatalf("expected fresh vmIP 192.168.100.42, got %s", gotVMIP)
	}
	if placement == nil {
		t.Fatal("expected placement record")
	}
	if placement.VMIP != "192.168.100.42" {
		t.Fatalf("expected placement IP 192.168.100.42, got %s", placement.VMIP)
	}
}

func TestRecoverAffinity_ErrorWhenHomeDead(t *testing.T) {
	homeHostID := 20
	ms := &mockPlacementStore{
		getMachineResult: &store.Machine{
			ID:         "m-050",
			HomeHostID: &homeHostID,
			VCPUs:      2,
			MemoryMB:   2048,
		},
		getHostResult: &store.Host{
			ID:     20,
			Status: "terminated", // not viable
		},
	}
	svc := NewPlacementService(ms, testConfig())

	_, _, _, err := svc.RecoverAffinity(context.Background(), "m-050", PlacementRequest{
		VCPUs:       2,
		MemoryMB:    2048,
		OperationID: "op-050",
	})
	if err == nil {
		t.Fatal("expected error when home host is terminated")
	}
	if !strings.Contains(err.Error(), "migration required") {
		t.Fatalf("expected 'migration required' in error, got: %v", err)
	}
	if ms.reAllocateCalled {
		t.Fatal("ReAllocateHostCapacity should NOT be called when home host is terminated")
	}
	if ms.placeOnHostCalled {
		t.Fatal("PlaceOnHost should NOT be called — terminated host handled before RecoverAffinity")
	}
}

func TestRecoverAffinity_ErrorWhenHomeIneligible(t *testing.T) {
	homeHostID := 21
	now := time.Now()
	stale := now.Add(-hostPlacementHeartbeatMaxAge - time.Second)

	tests := []struct {
		name    string
		host    store.Host
		wantErr string
	}{
		{
			name: "maintenance host",
			host: store.Host{
				ID:              homeHostID,
				Status:          "ready",
				MaintenanceMode: true,
				LastHeartbeat:   &now,
			},
			wantErr: "maintenance mode",
		},
		{
			name: "missing heartbeat",
			host: store.Host{
				ID:     homeHostID,
				Status: "ready",
			},
			wantErr: "no heartbeat",
		},
		{
			name: "stale heartbeat",
			host: store.Host{
				ID:            homeHostID,
				Status:        "ready",
				LastHeartbeat: &stale,
			},
			wantErr: "heartbeat is stale",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &mockPlacementStore{
				getMachineResult: &store.Machine{
					ID:         "m-055",
					HomeHostID: &homeHostID,
					VCPUs:      2,
					MemoryMB:   2048,
				},
				getHostResult: &tt.host,
			}
			svc := NewPlacementService(ms, testConfig())

			_, _, _, err := svc.RecoverAffinity(context.Background(), "m-055", PlacementRequest{
				VCPUs:       2,
				MemoryMB:    2048,
				OperationID: "op-055",
			})
			if err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
			if !strings.Contains(err.Error(), "migration required") || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want migration required and %q", err.Error(), tt.wantErr)
			}
			if ms.reAllocateCalled {
				t.Fatal("ReAllocateHostCapacity should NOT be called for ineligible home host")
			}
			if ms.placeOnHostCalled {
				t.Fatal("PlaceOnHost should NOT be called for ineligible home host")
			}
		})
	}
}

func TestRecoverAffinity_ErrorWhenCapacityFull(t *testing.T) {
	homeHostID := 15
	now := time.Now()
	ms := &mockPlacementStore{
		getMachineResult: &store.Machine{
			ID:         "m-060",
			HomeHostID: &homeHostID,
			VCPUs:      2,
			MemoryMB:   2048,
		},
		getHostResult: &store.Host{
			ID:            15,
			Status:        "ready",
			LastHeartbeat: &now,
		},
		reAllocateErr: fmt.Errorf("insufficient capacity"),
	}
	svc := NewPlacementService(ms, testConfig())

	_, _, _, err := svc.RecoverAffinity(context.Background(), "m-060", PlacementRequest{
		VCPUs:       2,
		MemoryMB:    2048,
		OperationID: "op-060",
	})
	if err == nil {
		t.Fatal("expected error when home host has no capacity")
	}
	if !strings.Contains(err.Error(), "migration required") {
		t.Fatalf("expected 'migration required' in error, got: %v", err)
	}
	if !ms.reAllocateCalled {
		t.Fatal("ReAllocateHostCapacity should have been attempted")
	}
	if ms.placeOnHostCalled {
		t.Fatal("PlaceOnHost should NOT be called — data volume is on home host")
	}
}

func TestRank_Random(t *testing.T) {
	ps := &PlacementService{config: PlacementConfig{Strategy: Random}}
	hosts := []store.Host{
		{ID: 1, UsedMemoryMB: 100},
		{ID: 2, UsedMemoryMB: 200},
		{ID: 3, UsedMemoryMB: 300},
	}
	ranked := ps.rank(hosts)
	if len(ranked) != 3 {
		t.Fatalf("expected 3 hosts, got %d", len(ranked))
	}
	// Can't test specific order for random, just verify all hosts present
	ids := map[int]bool{}
	for _, h := range ranked {
		ids[h.ID] = true
	}
	if len(ids) != 3 {
		t.Fatal("expected all 3 unique hosts in ranked output")
	}
}

func TestRank_Spread(t *testing.T) {
	ps := &PlacementService{config: PlacementConfig{Strategy: Spread}}
	hosts := []store.Host{
		{ID: 1, UsedMemoryMB: 300},
		{ID: 2, UsedMemoryMB: 100},
		{ID: 3, UsedMemoryMB: 200},
	}
	ranked := ps.rank(hosts)
	if ranked[0].ID != 2 {
		t.Fatalf("spread should pick least loaded first, got host %d", ranked[0].ID)
	}
	if ranked[1].ID != 3 {
		t.Fatalf("spread second should be host 3, got %d", ranked[1].ID)
	}
	if ranked[2].ID != 1 {
		t.Fatalf("spread third should be host 1, got %d", ranked[2].ID)
	}
}

func TestRank_BinPack(t *testing.T) {
	ps := &PlacementService{config: PlacementConfig{Strategy: BinPack}}
	hosts := []store.Host{
		{ID: 1, UsedMemoryMB: 100},
		{ID: 2, UsedMemoryMB: 300},
		{ID: 3, UsedMemoryMB: 200},
	}
	ranked := ps.rank(hosts)
	if ranked[0].ID != 2 {
		t.Fatalf("binpack should pick most loaded first, got host %d", ranked[0].ID)
	}
	if ranked[1].ID != 3 {
		t.Fatalf("binpack second should be host 3, got %d", ranked[1].ID)
	}
	if ranked[2].ID != 1 {
		t.Fatalf("binpack third should be host 1, got %d", ranked[2].ID)
	}
}

func TestEffectiveCapacity(t *testing.T) {
	tests := []struct {
		name      string
		host      *store.Host
		policy    *store.CapacityPolicy
		wantVCPUs int
		wantMemMB int
	}{
		{
			name: "2x CPU overcommit on 4-vCPU host",
			host: &store.Host{CapacityVCPUs: 4, CapacityMemoryMB: 8192},
			policy: &store.CapacityPolicy{
				CPUOvercommitRatio:    2.0,
				MemoryOvercommitRatio: 1.0,
				ReserveVCPUs:          0,
				ReserveMemoryMB:       0,
			},
			wantVCPUs: 8,
			wantMemMB: 8192,
		},
		{
			name: "reserves subtracted before overcommit",
			host: &store.Host{CapacityVCPUs: 8, CapacityMemoryMB: 16384},
			policy: &store.CapacityPolicy{
				CPUOvercommitRatio:    2.0,
				MemoryOvercommitRatio: 1.5,
				ReserveVCPUs:          2,
				ReserveMemoryMB:       2048,
			},
			wantVCPUs: 12,    // (8-2)*2
			wantMemMB: 21504, // (16384-2048)*1.5
		},
		{
			name: "negative clamped to zero",
			host: &store.Host{CapacityVCPUs: 2, CapacityMemoryMB: 1024},
			policy: &store.CapacityPolicy{
				CPUOvercommitRatio:    1.0,
				MemoryOvercommitRatio: 1.0,
				ReserveVCPUs:          4,
				ReserveMemoryMB:       2048,
			},
			wantVCPUs: 0,
			wantMemMB: 0,
		},
		{
			name: "1x ratio no reserves",
			host: &store.Host{CapacityVCPUs: 16, CapacityMemoryMB: 32768},
			policy: &store.CapacityPolicy{
				CPUOvercommitRatio:    1.0,
				MemoryOvercommitRatio: 1.0,
				ReserveVCPUs:          0,
				ReserveMemoryMB:       0,
			},
			wantVCPUs: 16,
			wantMemMB: 32768,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vcpus, memMB := EffectiveCapacity(tt.host, tt.policy)
			if vcpus != tt.wantVCPUs {
				t.Errorf("vcpus = %d, want %d", vcpus, tt.wantVCPUs)
			}
			if memMB != tt.wantMemMB {
				t.Errorf("memMB = %d, want %d", memMB, tt.wantMemMB)
			}
		})
	}
}
