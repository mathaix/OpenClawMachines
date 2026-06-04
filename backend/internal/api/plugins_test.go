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

type mockPluginStore struct {
	store.Store

	catalog  map[string]*store.PluginCatalogEntry
	machines map[string]*store.Machine
	plugins  map[string][]store.MachinePlugin
}

func newMockPluginStore() *mockPluginStore {
	return &mockPluginStore{
		catalog:  make(map[string]*store.PluginCatalogEntry),
		machines: make(map[string]*store.Machine),
		plugins:  make(map[string][]store.MachinePlugin),
	}
}

func (m *mockPluginStore) ListPluginCatalog(_ context.Context) ([]store.PluginCatalogEntry, error) {
	var entries []store.PluginCatalogEntry
	for _, e := range m.catalog {
		entries = append(entries, *e)
	}
	return entries, nil
}

func (m *mockPluginStore) GetPluginCatalogEntry(_ context.Context, id string) (*store.PluginCatalogEntry, error) {
	if e, ok := m.catalog[id]; ok {
		return e, nil
	}
	return nil, fmt.Errorf("not found")
}

func (m *mockPluginStore) CreatePluginCatalogEntry(_ context.Context, entry *store.PluginCatalogEntry) error {
	if _, exists := m.catalog[entry.ID]; exists {
		return fmt.Errorf("unique violation")
	}
	m.catalog[entry.ID] = entry
	return nil
}

func (m *mockPluginStore) UpdatePluginCatalogEntry(_ context.Context, entry *store.PluginCatalogEntry) error {
	if _, exists := m.catalog[entry.ID]; !exists {
		return store.ErrPluginNotFound
	}
	m.catalog[entry.ID] = entry
	return nil
}

func (m *mockPluginStore) DeletePluginCatalogEntry(_ context.Context, id string) error {
	if _, exists := m.catalog[id]; !exists {
		return store.ErrPluginNotFound
	}
	delete(m.catalog, id)
	return nil
}

func (m *mockPluginStore) GetMachine(_ context.Context, id string) (*store.Machine, error) {
	if machine, ok := m.machines[id]; ok {
		return machine, nil
	}
	return nil, fmt.Errorf("not found")
}

func (m *mockPluginStore) ListMachinePlugins(_ context.Context, machineID string) ([]store.MachinePlugin, error) {
	return m.plugins[machineID], nil
}

func (m *mockPluginStore) EnableMachinePlugin(_ context.Context, machineID, pluginID string, _ json.RawMessage) error {
	m.plugins[machineID] = append(m.plugins[machineID], store.MachinePlugin{
		MachineID:     machineID,
		PluginID:      pluginID,
		Enabled:       true,
		InstallStatus: "installed",
	})
	return nil
}

func (m *mockPluginStore) DisableMachinePlugin(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockPluginStore) UpdateMachinePluginOverrides(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}

func (m *mockPluginStore) UpdateMachinePluginStatus(_ context.Context, _, _, _, _ string) error {
	return nil
}

func pluginRequest(method, path string, body interface{}, accountID int, machineID string) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), accountIDKey, accountID)

	rctx := chi.NewRouteContext()
	if machineID != "" {
		rctx.URLParams.Add("id", machineID)
	}
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func pluginAdminRequest(method, path string, body interface{}, pluginID string) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")

	rctx := chi.NewRouteContext()
	if pluginID != "" {
		rctx.URLParams.Add("pluginId", pluginID)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func TestListPluginCatalog(t *testing.T) {
	ms := newMockPluginStore()
	ms.catalog["memory-core"] = &store.PluginCatalogEntry{
		ID: "memory-core", Name: "Memory Core", Slot: "memory",
		Version: "1", InstallKind: "bundled", Status: "active",
	}
	srv := &Server{store: ms}

	w := httptest.NewRecorder()
	r := pluginAdminRequest("GET", "/api/admin/plugins", nil, "")
	srv.handleListPluginCatalog(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var entries []store.PluginCatalogEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}

func TestCreatePluginCatalogEntry(t *testing.T) {
	ms := newMockPluginStore()
	srv := &Server{store: ms}

	body := map[string]interface{}{
		"id":   "test-plugin",
		"name": "Test Plugin",
		"slot": "test",
	}

	w := httptest.NewRecorder()
	r := pluginAdminRequest("POST", "/api/admin/plugins", body, "")
	srv.handleCreatePluginCatalogEntry(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	if _, exists := ms.catalog["test-plugin"]; !exists {
		t.Error("plugin should exist in catalog after creation")
	}
}

func TestEnableMachinePlugin(t *testing.T) {
	ms := newMockPluginStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1}
	srv := &Server{store: ms}

	body := map[string]interface{}{
		"plugin_id": "memory-core",
	}

	w := httptest.NewRecorder()
	r := pluginRequest("POST", "/api/accounts/1/machines/m-1/plugins", body, 1, "m-1")
	srv.handleEnableMachinePlugin(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if len(ms.plugins["m-1"]) != 1 {
		t.Error("expected 1 plugin enabled on machine")
	}
}

func TestEnableMachinePlugin_WrongAccount(t *testing.T) {
	ms := newMockPluginStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 2}
	srv := &Server{store: ms}

	body := map[string]interface{}{"plugin_id": "memory-core"}

	w := httptest.NewRecorder()
	r := pluginRequest("POST", "/api/accounts/1/machines/m-1/plugins", body, 1, "m-1")
	srv.handleEnableMachinePlugin(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestEnableMachinePlugin_RejectsStoppedMachineAfterFirstBoot(t *testing.T) {
	ms := newMockPluginStore()
	now := time.Now()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped", ProvisioningCompletedAt: &now}
	srv := &Server{store: ms}

	body := map[string]interface{}{"plugin_id": "memory-core"}

	w := httptest.NewRecorder()
	r := pluginRequest("POST", "/api/accounts/1/machines/m-1/plugins", body, 1, "m-1")
	srv.handleEnableMachinePlugin(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListMachinePlugins(t *testing.T) {
	ms := newMockPluginStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1}
	ms.plugins["m-1"] = []store.MachinePlugin{
		{PluginID: "memory-core", Enabled: true},
	}
	srv := &Server{store: ms}

	w := httptest.NewRecorder()
	r := pluginRequest("GET", "/api/accounts/1/machines/m-1/plugins", nil, 1, "m-1")
	srv.handleListMachinePlugins(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var plugins []store.MachinePlugin
	if err := json.Unmarshal(w.Body.Bytes(), &plugins); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(plugins) != 1 {
		t.Errorf("expected 1 plugin, got %d", len(plugins))
	}
}
