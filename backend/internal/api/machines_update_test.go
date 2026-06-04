package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type mockMachineUpdateStore struct {
	store.Store

	machine        *store.Machine
	updatedMachine *store.Machine

	// releases is keyed by kind+"/"+version, e.g. "openclaw/v2026.4.15-r1".
	releases map[string]*store.ArtifactRelease
}

func (m *mockMachineUpdateStore) GetMachine(_ context.Context, id string) (*store.Machine, error) {
	if m.machine == nil || m.machine.ID != id {
		return nil, fmt.Errorf("machine not found")
	}
	cp := *m.machine
	return &cp, nil
}

func (m *mockMachineUpdateStore) UpdateMachine(_ context.Context, machine *store.Machine) error {
	cp := *machine
	m.updatedMachine = &cp
	m.machine = &cp
	return nil
}

func (m *mockMachineUpdateStore) GetArtifactRelease(_ context.Context, kind, version string) (*store.ArtifactRelease, error) {
	if m.releases == nil {
		return nil, fmt.Errorf("release not found")
	}
	r, ok := m.releases[kind+"/"+version]
	if !ok {
		return nil, fmt.Errorf("release not found")
	}
	cp := *r
	return &cp, nil
}

func TestHandleUpdateMachine_SwitchesFromChannelToPinned(t *testing.T) {
	channel := "stable"
	versionSource := "channel"
	runtimeSource := "artifact"
	ms := &mockMachineUpdateStore{
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
	srv := &Server{store: ms}

	body := map[string]any{
		"rootfs_version": "rootfs-2026.04.01",
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/accounts/1/machines/machine-1", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), accountIDKey, 1)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "machine-1")
	req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	srv.handleUpdateMachine(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ms.updatedMachine == nil {
		t.Fatal("expected machine to be updated")
	}
	if ms.updatedMachine.DesiredChannel != nil {
		t.Fatalf("desired_channel = %v, want nil after switching to pinned mode", ms.updatedMachine.DesiredChannel)
	}
	if ms.updatedMachine.DesiredRootfsVersion == nil || *ms.updatedMachine.DesiredRootfsVersion != "rootfs-2026.04.01" {
		t.Fatalf("desired_rootfs_version = %v, want rootfs-2026.04.01", ms.updatedMachine.DesiredRootfsVersion)
	}
	if ms.updatedMachine.VersionSource == nil || *ms.updatedMachine.VersionSource != "pinned" {
		t.Fatalf("version_source = %v, want pinned", ms.updatedMachine.VersionSource)
	}
}

func TestHandleUpdateMachine_SwitchesFromPinnedToChannel(t *testing.T) {
	rootfsVersion := "rootfs-2026.04.01"
	openclawVersion := "openclaw-1.2.3"
	versionSource := "pinned"
	runtimeSource := "artifact"
	ms := &mockMachineUpdateStore{
		machine: &store.Machine{
			ID:                     "machine-1",
			AccountID:              1,
			Name:                   "Bot",
			Status:                 "stopped",
			VCPUs:                  2,
			MemoryMB:               4096,
			CreatedAt:              time.Now(),
			DesiredRootfsVersion:   &rootfsVersion,
			DesiredOpenclawVersion: &openclawVersion,
			VersionSource:          &versionSource,
			RuntimeSource:          &runtimeSource,
		},
	}
	srv := &Server{store: ms}

	body := map[string]any{
		"channel": "rc",
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/accounts/1/machines/machine-1", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), accountIDKey, 1)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "machine-1")
	req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	srv.handleUpdateMachine(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ms.updatedMachine == nil {
		t.Fatal("expected machine to be updated")
	}
	if ms.updatedMachine.DesiredRootfsVersion != nil {
		t.Fatalf("desired_rootfs_version = %v, want nil after switching to channel mode", ms.updatedMachine.DesiredRootfsVersion)
	}
	if ms.updatedMachine.DesiredOpenclawVersion != nil {
		t.Fatalf("desired_openclaw_version = %v, want nil after switching to channel mode", ms.updatedMachine.DesiredOpenclawVersion)
	}
	if ms.updatedMachine.DesiredChannel == nil || *ms.updatedMachine.DesiredChannel != "rc" {
		t.Fatalf("desired_channel = %v, want rc", ms.updatedMachine.DesiredChannel)
	}
	if ms.updatedMachine.VersionSource == nil || *ms.updatedMachine.VersionSource != "channel" {
		t.Fatalf("version_source = %v, want channel", ms.updatedMachine.VersionSource)
	}
}

func TestHandleUpdateMachine_RejectsPinnedAndChannelTogether(t *testing.T) {
	channel := "stable"
	versionSource := "channel"
	runtimeSource := "artifact"
	ms := &mockMachineUpdateStore{
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
	srv := &Server{store: ms}

	body := map[string]any{
		"channel":          "rc",
		"openclaw_version": "openclaw-1.2.4",
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/accounts/1/machines/machine-1", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), accountIDKey, 1)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "machine-1")
	req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	srv.handleUpdateMachine(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if ms.updatedMachine != nil {
		t.Fatal("expected update to be rejected before persistence")
	}
}

// --- Runtime compat guard tests ---

func compatStore(t *testing.T, currentRootfs, minRootfs string) *mockMachineUpdateStore {
	t.Helper()
	runtimeSource := "artifact"
	versionSource := "pinned"
	ms := &mockMachineUpdateStore{
		machine: &store.Machine{
			ID:                  "machine-1",
			AccountID:           1,
			Name:                "Bot",
			Status:              "running",
			VCPUs:               2,
			MemoryMB:            4096,
			CreatedAt:           time.Now(),
			ActualRootfsVersion: &currentRootfs,
			VersionSource:       &versionSource,
			RuntimeSource:       &runtimeSource,
		},
		releases: map[string]*store.ArtifactRelease{
			"openclaw/v2026.4.15-r1": {
				Kind: "openclaw", Version: "v2026.4.15-r1",
				MinRootfsVersion: &minRootfs,
			},
			"rootfs/" + minRootfs: {
				Kind: "rootfs", Version: minRootfs,
				CreatedAt: time.Date(2026, 4, 18, 23, 46, 18, 0, time.UTC),
			},
			"rootfs/" + currentRootfs: {
				Kind: "rootfs", Version: currentRootfs,
				CreatedAt: time.Date(2026, 4, 14, 14, 31, 54, 0, time.UTC),
			},
		},
	}
	return ms
}

func newCompatRequest(t *testing.T, body map[string]any) *http.Request {
	t.Helper()
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/accounts/1/machines/machine-1", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), accountIDKey, 1)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "machine-1")
	return req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
}

func TestHandleUpdateMachine_CompatGuard_OpenclawOnlyStaleRootfsRejected(t *testing.T) {
	ms := compatStore(t, "cd60435-20260414T143154Z", "1ae2d37-20260418T234618Z")
	srv := &Server{store: ms}

	req := newCompatRequest(t, map[string]any{"openclaw_version": "v2026.4.15-r1"})
	rec := httptest.NewRecorder()
	srv.handleUpdateMachine(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 body=%s", rec.Code, rec.Body.String())
	}
	if ms.updatedMachine != nil {
		t.Fatal("expected update to be rejected before persistence")
	}
	body := rec.Body.String()
	for _, want := range []string{"v2026.4.15-r1", "1ae2d37-20260418T234618Z", "cd60435-20260414T143154Z"} {
		if !bytes.Contains([]byte(body), []byte(want)) {
			t.Errorf("error body missing %q: %s", want, body)
		}
	}
}

func TestHandleUpdateMachine_CompatGuard_OpenclawOnlyCurrentRootfsOk(t *testing.T) {
	ms := compatStore(t, "1ae2d37-20260418T234618Z", "1ae2d37-20260418T234618Z")
	srv := &Server{store: ms}

	req := newCompatRequest(t, map[string]any{"openclaw_version": "v2026.4.15-r1"})
	rec := httptest.NewRecorder()
	srv.handleUpdateMachine(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	if ms.updatedMachine == nil {
		t.Fatal("expected machine to be updated")
	}
}

func TestHandleUpdateMachine_CompatGuard_BothVersionsBypassGuard(t *testing.T) {
	ms := compatStore(t, "cd60435-20260414T143154Z", "1ae2d37-20260418T234618Z")
	srv := &Server{store: ms}

	req := newCompatRequest(t, map[string]any{
		"openclaw_version": "v2026.4.15-r1",
		"rootfs_version":   "1ae2d37-20260418T234618Z",
	})
	rec := httptest.NewRecorder()
	srv.handleUpdateMachine(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateMachine_CompatGuard_NoMinRootfsPasses(t *testing.T) {
	ms := compatStore(t, "cd60435-20260414T143154Z", "1ae2d37-20260418T234618Z")
	ms.releases["openclaw/v2026.4.15-r1"].MinRootfsVersion = nil
	srv := &Server{store: ms}

	req := newCompatRequest(t, map[string]any{"openclaw_version": "v2026.4.15-r1"})
	rec := httptest.NewRecorder()
	srv.handleUpdateMachine(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateMachine_CompatGuard_OpenclawNotInCatalogPasses(t *testing.T) {
	ms := compatStore(t, "cd60435-20260414T143154Z", "1ae2d37-20260418T234618Z")
	delete(ms.releases, "openclaw/v2026.4.15-r1")
	srv := &Server{store: ms}

	req := newCompatRequest(t, map[string]any{"openclaw_version": "v2026.4.15-r1"})
	rec := httptest.NewRecorder()
	srv.handleUpdateMachine(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateMachine_RejectsBlankRuntimeSource(t *testing.T) {
	channel := "stable"
	versionSource := "channel"
	runtimeSource := "artifact"
	ms := &mockMachineUpdateStore{
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
	srv := &Server{store: ms}

	body := map[string]any{
		"runtime_source": "   ",
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/accounts/1/machines/machine-1", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), accountIDKey, 1)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "machine-1")
	req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	srv.handleUpdateMachine(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if ms.updatedMachine != nil {
		t.Fatal("expected update to be rejected before persistence")
	}
}
