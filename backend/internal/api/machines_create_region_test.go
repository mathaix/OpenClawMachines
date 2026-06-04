package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type mockMachineCreateStore struct {
	store.Store

	createdMachine *store.Machine
	enabledPlugins []string
}

func (m *mockMachineCreateStore) CreateMachine(_ context.Context, machine *store.Machine) error {
	if machine.ID == "" {
		machine.ID = "m-1"
	}
	m.createdMachine = machine
	return nil
}

func (m *mockMachineCreateStore) EnableMachinePlugin(_ context.Context, _ string, pluginID string, _ json.RawMessage) error {
	m.enabledPlugins = append(m.enabledPlugins, pluginID)
	return nil
}

func TestHandleCreateMachine_PersistsPreferredRegion(t *testing.T) {
	ms := &mockMachineCreateStore{}
	srv := &Server{store: ms}

	body := map[string]any{
		"name":             "Region Test",
		"size":             "standard",
		"preferred_region": "US-West",
		"auto_start":       false,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{UserID: 1}))
	req = req.WithContext(context.WithValue(req.Context(), accountIDKey, 1))

	rec := httptest.NewRecorder()
	srv.handleCreateMachine(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if ms.createdMachine == nil {
		t.Fatal("expected machine to be created")
	}
	if ms.createdMachine.PreferredRegion == nil || *ms.createdMachine.PreferredRegion != "us-west" {
		t.Fatalf("preferred_region = %v, want us-west", ms.createdMachine.PreferredRegion)
	}
	if ms.createdMachine.Kind != store.MachineKindOpenClaw {
		t.Fatalf("kind = %q, want %q", ms.createdMachine.Kind, store.MachineKindOpenClaw)
	}

	var resp store.Machine
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.PreferredRegion == nil || *resp.PreferredRegion != "us-west" {
		t.Fatalf("response preferred_region = %v, want us-west", resp.PreferredRegion)
	}
}

func TestHandleCreateMachine_PersistsHermesKindAndSkipsOpenClawPlugins(t *testing.T) {
	ms := &mockMachineCreateStore{}
	srv := &Server{store: ms}

	body := map[string]any{
		"name":       "Hermes Test",
		"kind":       "hermes",
		"size":       "standard",
		"auto_start": false,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{UserID: 1}))
	req = req.WithContext(context.WithValue(req.Context(), accountIDKey, 1))

	rec := httptest.NewRecorder()
	srv.handleCreateMachine(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if ms.createdMachine == nil {
		t.Fatal("expected machine to be created")
	}
	if ms.createdMachine.Kind != store.MachineKindHermes {
		t.Fatalf("kind = %q, want %q", ms.createdMachine.Kind, store.MachineKindHermes)
	}
	if len(ms.enabledPlugins) != 0 {
		t.Fatalf("enabled plugins = %d, want 0 for Hermes preview machine", len(ms.enabledPlugins))
	}

	var resp store.Machine
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Kind != store.MachineKindHermes {
		t.Fatalf("response kind = %q, want %q", resp.Kind, store.MachineKindHermes)
	}
}

func TestHandleCreateMachine_RejectsInvalidKind(t *testing.T) {
	ms := &mockMachineCreateStore{}
	srv := &Server{store: ms}

	body := map[string]any{
		"name":       "Invalid Kind",
		"kind":       "browser",
		"size":       "standard",
		"auto_start": false,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{UserID: 1}))
	req = req.WithContext(context.WithValue(req.Context(), accountIDKey, 1))

	rec := httptest.NewRecorder()
	srv.handleCreateMachine(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleCreateMachine_RejectsInvalidPreferredRegion(t *testing.T) {
	ms := &mockMachineCreateStore{}
	srv := &Server{store: ms}

	body := map[string]any{
		"name":             "Region Test",
		"size":             "standard",
		"preferred_region": "us west",
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{UserID: 1}))
	req = req.WithContext(context.WithValue(req.Context(), accountIDKey, 1))

	rec := httptest.NewRecorder()
	srv.handleCreateMachine(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleCreateMachine_PersistsPinnedRuntimeSelection(t *testing.T) {
	ms := &mockMachineCreateStore{}
	srv := &Server{store: ms}

	body := map[string]any{
		"name":             "Pinned Runtime",
		"size":             "standard",
		"rootfs_version":   "rootfs-2026.04.01",
		"openclaw_version": "openclaw-1.2.3",
		"runtime_source":   "artifact",
		"auto_start":       false,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{UserID: 1}))
	req = req.WithContext(context.WithValue(req.Context(), accountIDKey, 1))

	rec := httptest.NewRecorder()
	srv.handleCreateMachine(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if ms.createdMachine == nil {
		t.Fatal("expected machine to be created")
	}
	if ms.createdMachine.DesiredRootfsVersion == nil || *ms.createdMachine.DesiredRootfsVersion != "rootfs-2026.04.01" {
		t.Fatalf("desired_rootfs_version = %v, want rootfs-2026.04.01", ms.createdMachine.DesiredRootfsVersion)
	}
	if ms.createdMachine.DesiredOpenclawVersion == nil || *ms.createdMachine.DesiredOpenclawVersion != "openclaw-1.2.3" {
		t.Fatalf("desired_openclaw_version = %v, want openclaw-1.2.3", ms.createdMachine.DesiredOpenclawVersion)
	}
	if ms.createdMachine.DesiredChannel != nil {
		t.Fatalf("desired_channel = %v, want nil for pinned selection", ms.createdMachine.DesiredChannel)
	}
	if ms.createdMachine.VersionSource == nil || *ms.createdMachine.VersionSource != "pinned" {
		t.Fatalf("version_source = %v, want pinned", ms.createdMachine.VersionSource)
	}
	if ms.createdMachine.RuntimeSource == nil || *ms.createdMachine.RuntimeSource != "artifact" {
		t.Fatalf("runtime_source = %v, want artifact", ms.createdMachine.RuntimeSource)
	}
}

func TestHandleCreateMachine_RejectsPinnedAndChannelTogether(t *testing.T) {
	ms := &mockMachineCreateStore{}
	srv := &Server{store: ms}

	body := map[string]any{
		"name":             "Invalid Selection",
		"size":             "standard",
		"openclaw_version": "openclaw-1.2.3",
		"channel":          "stable",
		"auto_start":       false,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{UserID: 1}))
	req = req.WithContext(context.WithValue(req.Context(), accountIDKey, 1))

	rec := httptest.NewRecorder()
	srv.handleCreateMachine(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleCreateMachine_RejectsInvalidRuntimeSource(t *testing.T) {
	ms := &mockMachineCreateStore{}
	srv := &Server{store: ms}

	body := map[string]any{
		"name":           "Invalid Runtime Source",
		"size":           "standard",
		"runtime_source": "invalid",
		"auto_start":     false,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{UserID: 1}))
	req = req.WithContext(context.WithValue(req.Context(), accountIDKey, 1))

	rec := httptest.NewRecorder()
	srv.handleCreateMachine(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleCreateMachine_RejectsBlankRuntimeSource(t *testing.T) {
	ms := &mockMachineCreateStore{}
	srv := &Server{store: ms}

	body := map[string]any{
		"name":           "Blank Runtime Source",
		"size":           "standard",
		"runtime_source": "   ",
		"auto_start":     false,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{UserID: 1}))
	req = req.WithContext(context.WithValue(req.Context(), accountIDKey, 1))

	rec := httptest.NewRecorder()
	srv.handleCreateMachine(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}
