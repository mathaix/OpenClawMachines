package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

func runtimeUpgradeRequest(t *testing.T, accountID int, machineID string, body map[string]any) *http.Request {
	t.Helper()
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost,
		"/api/accounts/1/machines/"+machineID+"/runtime/upgrade",
		bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), accountIDKey, accountID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", machineID)
	return req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
}

func runtimeUpgradeStore() *mockMachineOpenClawStore {
	channel := "stable"
	versionSource := "channel"
	runtimeSource := "artifact"
	return &mockMachineOpenClawStore{
		machine: &store.Machine{
			ID:             "machine-1",
			AccountID:      1,
			Name:           "Bot",
			Status:         "stopped",
			VCPUs:          2,
			MemoryMB:       4096,
			CreatedAt:      time.Now(),
			DesiredChannel: &channel,
			VersionSource:  &versionSource,
			RuntimeSource:  &runtimeSource,
		},
	}
}

func TestHandleRuntimeUpgrade_WritesBothDesiredVersions(t *testing.T) {
	ms := runtimeUpgradeStore()
	srv := &Server{store: ms}

	req := runtimeUpgradeRequest(t, 1, "machine-1", map[string]any{
		"openclaw_version": "v2026.4.15-r1",
		"rootfs_version":   "1ae2d37-20260418T234618Z",
	})
	rec := httptest.NewRecorder()
	srv.handleRuntimeUpgrade(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	if ms.updatedMachine == nil {
		t.Fatal("expected machine to be updated")
	}
	if ms.updatedMachine.DesiredOpenclawVersion == nil || *ms.updatedMachine.DesiredOpenclawVersion != "v2026.4.15-r1" {
		t.Errorf("desired_openclaw_version = %v, want v2026.4.15-r1", ms.updatedMachine.DesiredOpenclawVersion)
	}
	if ms.updatedMachine.DesiredRootfsVersion == nil || *ms.updatedMachine.DesiredRootfsVersion != "1ae2d37-20260418T234618Z" {
		t.Errorf("desired_rootfs_version = %v, want 1ae2d37-20260418T234618Z", ms.updatedMachine.DesiredRootfsVersion)
	}
	if ms.updatedMachine.DesiredChannel != nil {
		t.Errorf("desired_channel = %v, want nil (switched to pinned)", ms.updatedMachine.DesiredChannel)
	}
	if ms.updatedMachine.VersionSource == nil || *ms.updatedMachine.VersionSource != "pinned" {
		t.Errorf("version_source = %v, want pinned", ms.updatedMachine.VersionSource)
	}
}

func TestHandleRuntimeUpgrade_BypassesCompatGuard(t *testing.T) {
	// Both pinned at once; the compat guard is per-field and this endpoint
	// writes both fields atomically, so even an otherwise-incompatible pin
	// should succeed.
	ms := runtimeUpgradeStore()
	current := "cd60435-20260414T143154Z"
	ms.machine.ActualRootfsVersion = &current
	srv := &Server{store: ms}

	req := runtimeUpgradeRequest(t, 1, "machine-1", map[string]any{
		"openclaw_version": "v2026.4.15-r1",
		"rootfs_version":   "1ae2d37-20260418T234618Z",
	})
	rec := httptest.NewRecorder()
	srv.handleRuntimeUpgrade(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRuntimeUpgrade_MissingOpenclawVersion(t *testing.T) {
	ms := runtimeUpgradeStore()
	srv := &Server{store: ms}

	req := runtimeUpgradeRequest(t, 1, "machine-1", map[string]any{
		"rootfs_version": "1ae2d37-20260418T234618Z",
	})
	rec := httptest.NewRecorder()
	srv.handleRuntimeUpgrade(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if ms.updatedMachine != nil {
		t.Fatal("expected no update on missing openclaw_version")
	}
}

func TestHandleRuntimeUpgrade_MissingRootfsVersion(t *testing.T) {
	ms := runtimeUpgradeStore()
	srv := &Server{store: ms}

	req := runtimeUpgradeRequest(t, 1, "machine-1", map[string]any{
		"openclaw_version": "v2026.4.15-r1",
	})
	rec := httptest.NewRecorder()
	srv.handleRuntimeUpgrade(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if ms.updatedMachine != nil {
		t.Fatal("expected no update on missing rootfs_version")
	}
}

func TestHandleRuntimeUpgrade_InvalidOpenclawFormat(t *testing.T) {
	ms := runtimeUpgradeStore()
	srv := &Server{store: ms}

	req := runtimeUpgradeRequest(t, 1, "machine-1", map[string]any{
		"openclaw_version": "not a version",
		"rootfs_version":   "1ae2d37-20260418T234618Z",
	})
	rec := httptest.NewRecorder()
	srv.handleRuntimeUpgrade(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRuntimeUpgrade_NonExistentMachine(t *testing.T) {
	ms := runtimeUpgradeStore()
	ms.machine = nil
	srv := &Server{store: ms}

	req := runtimeUpgradeRequest(t, 1, "missing", map[string]any{
		"openclaw_version": "v2026.4.15-r1",
		"rootfs_version":   "1ae2d37-20260418T234618Z",
	})
	rec := httptest.NewRecorder()
	srv.handleRuntimeUpgrade(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleRuntimeUpgrade_WrongAccount(t *testing.T) {
	ms := runtimeUpgradeStore()
	ms.machine.AccountID = 42
	srv := &Server{store: ms}

	req := runtimeUpgradeRequest(t, 1, "machine-1", map[string]any{
		"openclaw_version": "v2026.4.15-r1",
		"rootfs_version":   "1ae2d37-20260418T234618Z",
	})
	rec := httptest.NewRecorder()
	srv.handleRuntimeUpgrade(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
