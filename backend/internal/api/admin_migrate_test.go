package api

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type mockAdminMigrateStore struct {
	store.Store

	softStopCalled bool
	softStopHostID int
	softStopVCPUs  int
	softStopMemMB  int

	releaseHostCapacityCalled bool
	releaseHostHostID         int
	releaseHostVCPUs          int
	releaseHostMemMB          int

	unassignCalled bool
	unassignID     string

	releasePlacementCalled bool
	releasePlacementID     string

	updateHomeHostCalled bool
	updateHomeMachineID  string
	updateHomeHostID     *int
}

func (m *mockAdminMigrateStore) SoftStopMachine(_ context.Context, machineID string, hostID, vcpus, memoryMB, diskMB int) error {
	m.softStopCalled = true
	m.softStopHostID = hostID
	m.softStopVCPUs = vcpus
	m.softStopMemMB = memoryMB
	m.unassignID = machineID
	return nil
}

func (m *mockAdminMigrateStore) UpdateMachineHomeHost(_ context.Context, machineID string, homeHostID *int) error {
	m.updateHomeHostCalled = true
	m.updateHomeMachineID = machineID
	m.updateHomeHostID = homeHostID
	return nil
}

func (m *mockAdminMigrateStore) ReleasePlacement(_ context.Context, machineID string) error {
	m.releasePlacementCalled = true
	m.releasePlacementID = machineID
	return nil
}

func (m *mockAdminMigrateStore) ReleaseHostCapacity(_ context.Context, hostID, vcpus, memoryMB int) error {
	m.releaseHostCapacityCalled = true
	m.releaseHostHostID = hostID
	m.releaseHostVCPUs = vcpus
	m.releaseHostMemMB = memoryMB
	return nil
}

func (m *mockAdminMigrateStore) UnassignMachineFromHost(_ context.Context, machineID string) error {
	m.unassignCalled = true
	m.unassignID = machineID
	return nil
}

func (m *mockAdminMigrateStore) ReleaseMigrationSource(_ context.Context, machineID string, expectedHostID int, vcpus, memoryMB int, releaseCapacity bool) error {
	if releaseCapacity {
		m.releaseHostCapacityCalled = true
		m.releaseHostHostID = expectedHostID
		m.releaseHostVCPUs = vcpus
		m.releaseHostMemMB = memoryMB
	}
	m.unassignCalled = true
	m.unassignID = machineID
	m.updateHomeHostCalled = true
	m.updateHomeMachineID = machineID
	m.updateHomeHostID = nil
	m.releasePlacementCalled = true
	m.releasePlacementID = machineID
	return nil
}

func TestFinalizeStoppedSourceAfterMigrationAbort(t *testing.T) {
	ms := &mockAdminMigrateStore{}
	srv := &Server{store: ms}
	machine := &store.Machine{
		ID:       "m-123",
		VCPUs:    4,
		MemoryMB: 8192,
	}

	if err := srv.finalizeStoppedSourceAfterMigrationAbort(context.Background(), machine, 42); err != nil {
		t.Fatalf("finalizeStoppedSourceAfterMigrationAbort: %v", err)
	}

	if !ms.softStopCalled {
		t.Fatal("expected SoftStopMachine to be called")
	}
	if ms.softStopHostID != 42 || ms.softStopVCPUs != 4 || ms.softStopMemMB != 8192 {
		t.Fatalf("unexpected soft stop args: host=%d vcpus=%d mem=%d", ms.softStopHostID, ms.softStopVCPUs, ms.softStopMemMB)
	}
	if !ms.updateHomeHostCalled {
		t.Fatal("expected UpdateMachineHomeHost to be called")
	}
	if ms.updateHomeHostID == nil || *ms.updateHomeHostID != 42 {
		t.Fatalf("expected home host to be set to 42, got %#v", ms.updateHomeHostID)
	}
	if !ms.releasePlacementCalled || ms.releasePlacementID != "m-123" {
		t.Fatalf("expected ReleasePlacement for m-123, got called=%v id=%q", ms.releasePlacementCalled, ms.releasePlacementID)
	}
	if ms.releaseHostCapacityCalled {
		t.Fatal("did not expect ReleaseHostCapacity during abort finalization")
	}
	if ms.unassignCalled {
		t.Fatal("did not expect UnassignMachineFromHost during abort finalization")
	}
}

func TestReleaseMigrationSource(t *testing.T) {
	machine := &store.Machine{
		ID:       "m-456",
		VCPUs:    2,
		MemoryMB: 4096,
	}

	t.Run("with capacity release", func(t *testing.T) {
		ms := &mockAdminMigrateStore{}
		srv := &Server{store: ms}

		if err := srv.releaseMigrationSource(context.Background(), machine, 99, true); err != nil {
			t.Fatalf("releaseMigrationSource: %v", err)
		}

		if !ms.releaseHostCapacityCalled {
			t.Fatal("expected ReleaseHostCapacity to be called")
		}
		if ms.releaseHostHostID != 99 || ms.releaseHostVCPUs != 2 || ms.releaseHostMemMB != 4096 {
			t.Fatalf("unexpected release args: host=%d vcpus=%d mem=%d", ms.releaseHostHostID, ms.releaseHostVCPUs, ms.releaseHostMemMB)
		}
		if !ms.unassignCalled || ms.unassignID != "m-456" {
			t.Fatalf("expected UnassignMachineFromHost for m-456, got called=%v id=%q", ms.unassignCalled, ms.unassignID)
		}
		if !ms.updateHomeHostCalled {
			t.Fatal("expected UpdateMachineHomeHost to be called")
		}
		if ms.updateHomeHostID != nil {
			t.Fatalf("expected home host to be cleared, got %#v", ms.updateHomeHostID)
		}
		if !ms.releasePlacementCalled || ms.releasePlacementID != "m-456" {
			t.Fatalf("expected ReleasePlacement for m-456, got called=%v id=%q", ms.releasePlacementCalled, ms.releasePlacementID)
		}
	})

	t.Run("without capacity release", func(t *testing.T) {
		ms := &mockAdminMigrateStore{}
		srv := &Server{store: ms}

		if err := srv.releaseMigrationSource(context.Background(), machine, 99, false); err != nil {
			t.Fatalf("releaseMigrationSource: %v", err)
		}

		if ms.releaseHostCapacityCalled {
			t.Fatal("did not expect ReleaseHostCapacity to be called")
		}
		if !ms.unassignCalled || !ms.updateHomeHostCalled || !ms.releasePlacementCalled {
			t.Fatal("expected unassign, clear home host, and release placement to be called")
		}
	})
}

// --- Plugin stubs (satisfy store.Store interface) ---

func (m *mockAdminMigrateStore) ListPluginCatalog(_ context.Context) ([]store.PluginCatalogEntry, error) {
	return nil, nil
}
func (m *mockAdminMigrateStore) GetPluginCatalogEntry(_ context.Context, _ string) (*store.PluginCatalogEntry, error) {
	return nil, fmt.Errorf("not found")
}
func (m *mockAdminMigrateStore) CreatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockAdminMigrateStore) UpdatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockAdminMigrateStore) DeletePluginCatalogEntry(_ context.Context, _ string) error {
	return nil
}
func (m *mockAdminMigrateStore) ListMachinePlugins(_ context.Context, _ string) ([]store.MachinePlugin, error) {
	return nil, nil
}
func (m *mockAdminMigrateStore) EnableMachinePlugin(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockAdminMigrateStore) DisableMachinePlugin(_ context.Context, _, _ string) error {
	return nil
}
func (m *mockAdminMigrateStore) UpdateMachinePluginOverrides(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockAdminMigrateStore) UpdateMachinePluginStatus(_ context.Context, _, _, _, _ string) error {
	return nil
}
