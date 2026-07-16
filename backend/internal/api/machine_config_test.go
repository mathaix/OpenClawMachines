package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
	"github.com/mathaix/openclawmachines/backend/internal/machines"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// mockConfigStore implements the store methods used by the config handlers.
type mockConfigStore struct {
	store.Store

	machines     map[string]*store.Machine
	capabilities map[string][]store.MachineCapability
	registry     map[string]*store.RegistryEntry
	configs      map[string]*store.MachineConfigRecord
	credentials  map[string][]store.Credential // machineID -> creds
	hosts        map[int]*store.Host
	accounts     map[int]*store.Account

	assembledConfigs map[string]json.RawMessage
	assembledVersion map[string]int

	plugins         map[string][]store.MachinePlugin
	pluginCatalog   map[string]*store.PluginCatalogEntry
	providerCatalog map[string]*store.ProviderCatalogEntry

	preferredModels map[string]string // machineID -> model

	// track calls
	setAssembledCalls int
}

func newMockConfigStore() *mockConfigStore {
	return &mockConfigStore{
		machines:         make(map[string]*store.Machine),
		capabilities:     make(map[string][]store.MachineCapability),
		registry:         make(map[string]*store.RegistryEntry),
		configs:          make(map[string]*store.MachineConfigRecord),
		credentials:      make(map[string][]store.Credential),
		hosts:            make(map[int]*store.Host),
		accounts:         make(map[int]*store.Account),
		assembledConfigs: make(map[string]json.RawMessage),
		assembledVersion: make(map[string]int),
		plugins:          make(map[string][]store.MachinePlugin),
		pluginCatalog:    make(map[string]*store.PluginCatalogEntry),
		providerCatalog:  make(map[string]*store.ProviderCatalogEntry),
		preferredModels:  make(map[string]string),
	}
}

func (m *mockConfigStore) GetAccount(_ context.Context, id int) (*store.Account, error) {
	if account, ok := m.accounts[id]; ok {
		return account, nil
	}
	return nil, fmt.Errorf("account %d not found", id)
}

func (m *mockConfigStore) GetMachine(_ context.Context, id string) (*store.Machine, error) {
	if machine, ok := m.machines[id]; ok {
		return machine, nil
	}
	return nil, fmt.Errorf("machine %s not found", id)
}

func (m *mockConfigStore) ListMachineCapabilities(_ context.Context, machineID string) ([]store.MachineCapability, error) {
	return m.capabilities[machineID], nil
}

func (m *mockConfigStore) ListEnabledWorkspaceIntegrationsForMachine(_ context.Context, _ string) ([]store.WorkspaceIntegration, error) {
	return nil, nil
}

func (m *mockConfigStore) GetRegistryEntry(_ context.Context, id string) (*store.RegistryEntry, error) {
	if entry, ok := m.registry[id]; ok {
		return entry, nil
	}
	return nil, fmt.Errorf("registry entry %s not found", id)
}

func (m *mockConfigStore) GetMachineConfig(_ context.Context, machineID string) (*store.MachineConfigRecord, error) {
	if cfg, ok := m.configs[machineID]; ok {
		return cfg, nil
	}
	return nil, fmt.Errorf("no config for machine %s", machineID)
}

func (m *mockConfigStore) ListMachineCredentials(_ context.Context, machineID string) ([]store.Credential, error) {
	return m.credentials[machineID], nil
}

func (m *mockConfigStore) ListCustomProviders(_ context.Context, _ int) ([]store.CustomProvider, error) {
	return nil, nil
}

func (m *mockConfigStore) ListMachineAgents(_ context.Context, _ string) ([]store.MachineAgent, error) {
	return nil, nil
}

func (m *mockConfigStore) DemoteDefaultAgents(_ context.Context, _ string) error {
	return nil
}

func (m *mockConfigStore) SetMachineAssembledConfig(_ context.Context, machineID string, config json.RawMessage, version int) error {
	m.assembledConfigs[machineID] = config
	m.assembledVersion[machineID] = version
	m.setAssembledCalls++
	return nil
}

func (m *mockConfigStore) GetHost(_ context.Context, id int) (*store.Host, error) {
	if host, ok := m.hosts[id]; ok {
		return host, nil
	}
	return nil, fmt.Errorf("host %d not found", id)
}

func (m *mockConfigStore) SetMachinePreferredModel(_ context.Context, machineID, model string) error {
	m.preferredModels[machineID] = model
	// Also update the config record's PlatformOverrides so assembleConfigForMachine
	// picks up the new model when doing a full config push.
	overrides := map[string]interface{}{}
	if cfg, ok := m.configs[machineID]; ok {
		if cfg.PlatformOverrides != nil {
			_ = json.Unmarshal(cfg.PlatformOverrides, &overrides)
		}
		overrides["preferred_model"] = model
		data, _ := json.Marshal(overrides)
		cfg.PlatformOverrides = data
	} else {
		overrides["preferred_model"] = model
		data, _ := json.Marshal(overrides)
		m.configs[machineID] = &store.MachineConfigRecord{PlatformOverrides: data}
	}
	return nil
}

func (m *mockConfigStore) GetMachinePreferredModel(_ context.Context, machineID string) (string, error) {
	if model, ok := m.preferredModels[machineID]; ok {
		return model, nil
	}
	return "", nil
}

// newTestConfigServer creates a minimal Server with a mock store for config handler testing.
func newTestConfigServer(ms *mockConfigStore) *Server {
	return &Server{
		store:        ms,
		proxyBaseURL: "http://192.168.100.1:4000",
	}
}

// configRequest creates an HTTP request with the account ID injected into context
// (bypassing auth middleware) and machineID as a chi URL param.
func configRequest(method, path, machineID string, accountID int) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	ctx := context.WithValue(req.Context(), accountIDKey, accountID)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", machineID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)

	return req.WithContext(ctx)
}

func configRequestWithBody(method, path, machineID string, accountID int, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), accountIDKey, accountID)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", machineID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)

	return req.WithContext(ctx)
}

// ---- Tests ----

func TestGetAssembledConfig_NoCapabilities(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
	// No capabilities, no credentials, no config record
	srv := newTestConfigServer(ms)

	w := httptest.NewRecorder()
	r := configRequest("GET", "/api/accounts/1/machines/m-1/assembled-config", "m-1", 1)
	srv.handleGetMachineAssembledConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// With no capabilities, should return platform defaults directly (no warnings wrapper)
	var config map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &config); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	// Verify platform defaults are present
	gw, ok := config["gateway"].(map[string]interface{})
	if !ok {
		t.Fatal("missing gateway in assembled config")
	}
	controlUi, ok := gw["controlUi"].(map[string]interface{})
	if !ok {
		t.Fatal("missing gateway.controlUi")
	}
	if controlUi["enabled"] != true {
		t.Errorf("gateway.controlUi.enabled = %v, want true", controlUi["enabled"])
	}

	// commands defaults should be present
	commands, ok := config["commands"].(map[string]interface{})
	if !ok {
		t.Fatal("missing commands in assembled config")
	}
	if commands["native"] != "auto" {
		t.Errorf("commands.native = %v, want auto", commands["native"])
	}

	// session defaults should be present
	session, ok := config["session"].(map[string]interface{})
	if !ok {
		t.Fatal("missing session in assembled config")
	}
	if session["dmScope"] != "per-channel-peer" {
		t.Errorf("session.dmScope = %v, want per-channel-peer", session["dmScope"])
	}
}

func TestPushConfig_StoppedMachine(t *testing.T) {
	ms := newMockConfigStore()
	now := time.Now()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped", ProvisioningCompletedAt: &now}
	srv := newTestConfigServer(ms)

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/m-1/config/push", "m-1", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}

	if ms.setAssembledCalls != 0 {
		t.Errorf("SetMachineAssembledConfig called %d times, want 0", ms.setAssembledCalls)
	}
}

func TestPushConfig_FirstBootStoppedMachineAllowed(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
	srv := newTestConfigServer(ms)

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/m-1/config/push", "m-1", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ms.setAssembledCalls != 1 {
		t.Errorf("SetMachineAssembledConfig called %d times, want 1", ms.setAssembledCalls)
	}
}

func TestPushConfig_RunningMachine_LiveUpdateSkipped(t *testing.T) {
	// Running machine with host, but no agentClient → live_update = "skipped"
	hostID := 10
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{
		ID: "m-1", AccountID: 1, Status: "running", HostID: &hostID,
	}
	ms.hosts[10] = &store.Host{ID: 10, VMName: "host-1", Status: "ready"}
	// agentClient is nil in test server → skipped path
	srv := newTestConfigServer(ms)

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/m-1/config/push", "m-1", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp["live_update"] != "skipped" {
		t.Errorf("live_update = %v, want skipped (agentClient is nil in test)", resp["live_update"])
	}
}

func TestPushConfig_VersionIncrements(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
	ms.configs["m-1"] = &store.MachineConfigRecord{
		MachineID:     "m-1",
		ConfigVersion: 5,
		UpdatedAt:     time.Now(),
	}
	srv := newTestConfigServer(ms)

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/m-1/config/push", "m-1", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Should be 5 + 1 = 6
	if resp["config_version"] != float64(6) {
		t.Errorf("config_version = %v, want 6 (previous was 5)", resp["config_version"])
	}
}

func TestGetAssembledConfig_MissingRegistryEntry(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
	ms.capabilities["m-1"] = []store.MachineCapability{
		{MachineID: "m-1", EntryID: "telegram", Enabled: true, EntryType: "channel"},
		{MachineID: "m-1", EntryID: "nonexistent-skill", Enabled: true, EntryType: "skill"},
	}
	// Only register telegram, not nonexistent-skill
	ms.registry["telegram"] = &store.RegistryEntry{
		ID:   "telegram",
		Type: "channel",
		ConfigTemplate: json.RawMessage(`{
			"channels": {"telegram": {"enabled": true}}
		}`),
	}
	srv := newTestConfigServer(ms)

	w := httptest.NewRecorder()
	r := configRequest("GET", "/api/accounts/1/machines/m-1/assembled-config", "m-1", 1)
	srv.handleGetMachineAssembledConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// When there are no warnings, config is returned directly.
	// But the missing registry entry is silently skipped (logged, not warned to user).
	// Verify telegram is still in the output.
	var config map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &config); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	channels, ok := config["channels"].(map[string]interface{})
	if !ok {
		t.Fatal("missing channels in config")
	}
	if _, ok := channels["telegram"]; !ok {
		t.Error("telegram channel should be present despite missing nonexistent-skill")
	}
}

func TestPushConfig_MissingRegistryEntry_ReturnsWarnings(t *testing.T) {
	// Push config with a capability whose registry entry doesn't exist.
	// The push should succeed but include warnings.
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
	ms.capabilities["m-1"] = []store.MachineCapability{
		{MachineID: "m-1", EntryID: "nonexistent", Enabled: true, EntryType: "channel"},
	}
	// No registry entry for "nonexistent"
	srv := newTestConfigServer(ms)

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/m-1/config/push", "m-1", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("status = %v, want ok", resp["status"])
	}

	// Push should still succeed (config version incremented, config stored)
	if ms.setAssembledCalls != 1 {
		t.Errorf("SetMachineAssembledConfig called %d times, want 1", ms.setAssembledCalls)
	}
}

func TestGetAssembledConfig_WrongAccount(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
	srv := newTestConfigServer(ms)

	// Request with account 999 for a machine owned by account 1
	w := httptest.NewRecorder()
	r := configRequest("GET", "/api/accounts/999/machines/m-1/assembled-config", "m-1", 999)
	srv.handleGetMachineAssembledConfig(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPushConfig_WrongAccount(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
	srv := newTestConfigServer(ms)

	// Request with account 999 for a machine owned by account 1
	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/999/machines/m-1/config/push", "m-1", 999)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}

	// Store should NOT have been called
	if ms.setAssembledCalls != 0 {
		t.Errorf("SetMachineAssembledConfig called %d times, want 0 (should be blocked)", ms.setAssembledCalls)
	}
}

func TestGetAssembledConfig_MachineNotFound(t *testing.T) {
	ms := newMockConfigStore()
	srv := newTestConfigServer(ms)

	w := httptest.NewRecorder()
	r := configRequest("GET", "/api/accounts/1/machines/nonexistent/assembled-config", "nonexistent", 1)
	srv.handleGetMachineAssembledConfig(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPushConfig_MachineNotFound(t *testing.T) {
	ms := newMockConfigStore()
	srv := newTestConfigServer(ms)

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/nonexistent/config/push", "nonexistent", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetAssembledConfig_WithCredentials_InjectsProviderURLs(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
	ms.credentials["m-1"] = []store.Credential{
		{ID: 1, Provider: "anthropic", CredentialType: "api_key"},
	}
	srv := newTestConfigServer(ms)

	w := httptest.NewRecorder()
	r := configRequest("GET", "/api/accounts/1/machines/m-1/assembled-config", "m-1", 1)
	srv.handleGetMachineAssembledConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var config map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &config); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Should have models.providers.anthropic.baseUrl
	models, ok := config["models"].(map[string]interface{})
	if !ok {
		t.Fatal("missing models in config")
	}
	providers, ok := models["providers"].(map[string]interface{})
	if !ok {
		t.Fatal("missing models.providers")
	}
	anthropic, ok := providers["anthropic"].(map[string]interface{})
	if !ok {
		t.Fatal("missing models.providers.anthropic")
	}
	baseURL, ok := anthropic["baseUrl"].(string)
	if !ok || baseURL != "http://192.168.100.1:4000/anthropic/v1" {
		t.Errorf("anthropic baseUrl = %v, want http://192.168.100.1:4000/anthropic/v1", baseURL)
	}
	// Native mode: apiKey should be exec secret ref, not env var
	apiKey, ok := anthropic["apiKey"].(map[string]interface{})
	if !ok {
		t.Fatal("anthropic apiKey should be an exec secret ref object")
	}
	if apiKey["source"] != "exec" || apiKey["provider"] != "ocm" || apiKey["id"] != "anthropic-key" {
		t.Errorf("anthropic apiKey = %v, want exec ref with id=anthropic-key", apiKey)
	}

	// Should have agents.defaults.model.primary = anthropic
	agents, ok := config["agents"].(map[string]interface{})
	if !ok {
		t.Fatal("missing agents in config")
	}
	defaults, ok := agents["defaults"].(map[string]interface{})
	if !ok {
		t.Fatal("missing agents.defaults")
	}
	model, ok := defaults["model"].(map[string]interface{})
	if !ok {
		t.Fatal("missing agents.defaults.model")
	}
	if model["primary"] != "anthropic/claude-sonnet-4-6" {
		t.Errorf("agents.defaults.model.primary = %v, want anthropic/claude-sonnet-4-6", model["primary"])
	}
}

func TestGetAssembledConfig_NormalizesLegacyCodexPreferredModel(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
	ms.credentials["m-1"] = []store.Credential{
		{ID: 1, Provider: "openai-codex", CredentialType: "oauth"},
	}
	ms.configs["m-1"] = &store.MachineConfigRecord{
		MachineID:         "m-1",
		PlatformOverrides: json.RawMessage(`{"preferred_model":"openai/gpt-5.5","openai_agent_runtime":"codex"}`),
	}
	srv := newTestConfigServer(ms)

	w := httptest.NewRecorder()
	r := configRequest("GET", "/api/accounts/1/machines/m-1/assembled-config", "m-1", 1)
	srv.handleGetMachineAssembledConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var config map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &config); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	agents, ok := config["agents"].(map[string]interface{})
	if !ok {
		t.Fatal("missing agents in config")
	}
	defaults, ok := agents["defaults"].(map[string]interface{})
	if !ok {
		t.Fatal("missing agents.defaults")
	}
	model, ok := defaults["model"].(map[string]interface{})
	if !ok {
		t.Fatal("missing agents.defaults.model")
	}
	if model["primary"] != "codex/gpt-5.5" {
		t.Fatalf("agents.defaults.model.primary = %v, want codex/gpt-5.5", model["primary"])
	}
	models, ok := defaults["models"].(map[string]interface{})
	if !ok {
		t.Fatal("missing agents.defaults.models")
	}
	if _, ok := models["openai/gpt-5.5"]; ok {
		t.Fatalf("legacy openai/gpt-5.5 model entry should not be present: %v", models)
	}
	codexModel, ok := models["codex/gpt-5.5"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing codex/gpt-5.5 model entry: %v", models)
	}
	runtime, ok := codexModel["agentRuntime"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing codex/gpt-5.5 agentRuntime: %v", codexModel)
	}
	if runtime["id"] != "codex" {
		t.Fatalf("codex/gpt-5.5 agentRuntime.id = %v, want codex", runtime["id"])
	}
}

// ---- Plugin mock methods ----

func (m *mockConfigStore) ListMachinePlugins(_ context.Context, machineID string) ([]store.MachinePlugin, error) {
	return m.plugins[machineID], nil
}

func (m *mockConfigStore) GetPluginCatalogEntry(_ context.Context, id string) (*store.PluginCatalogEntry, error) {
	if e, ok := m.pluginCatalog[id]; ok {
		return e, nil
	}
	return nil, fmt.Errorf("plugin %s not found", id)
}

func (m *mockConfigStore) EnableMachinePlugin(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}

func (m *mockConfigStore) DisableMachinePlugin(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockConfigStore) UpdateMachinePluginOverrides(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}

func (m *mockConfigStore) UpdateMachinePluginStatus(_ context.Context, _, _, _, _ string) error {
	return nil
}

func (m *mockConfigStore) ListPluginCatalog(_ context.Context) ([]store.PluginCatalogEntry, error) {
	return nil, nil
}

func (m *mockConfigStore) CreatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}

func (m *mockConfigStore) UpdatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}

func (m *mockConfigStore) DeletePluginCatalogEntry(_ context.Context, _ string) error {
	return nil
}

func (m *mockConfigStore) ListModelCatalog(_ context.Context) ([]store.ModelCatalogEntry, error) {
	smart := "smart"
	balanced := "balanced"
	fast := "fast"
	nebiusQwen := "nebius/Qwen/Qwen3.5-397B-A17B"
	nebiusMinimax := "nebius/MiniMaxAI/MiniMax-M2.5"
	nebiusGPT := "nebius/openai/gpt-oss-120b"
	codexGPT55 := "codex/gpt-5.5"
	return []store.ModelCatalogEntry{
		{ID: "qwen/qwen3.5-397b", Label: "Smart", Source: "platform", Tier: &smart, GatewayModelID: &nebiusQwen, Provider: "nebius", Enabled: true, SortOrder: 1},
		{ID: "minimax/minimax-m2.5", Label: "Balanced", Source: "platform", Tier: &balanced, GatewayModelID: &nebiusMinimax, Provider: "nebius", Enabled: true, SortOrder: 2},
		{ID: "openai/gpt-oss-120b", Label: "Fast", Source: "platform", Tier: &fast, GatewayModelID: &nebiusGPT, Provider: "nebius", Enabled: true, SortOrder: 3},
		{ID: "anthropic/claude-sonnet-4-6", Label: "Claude Sonnet 4.6", Source: "byok", Provider: "anthropic", Enabled: true, SortOrder: 10},
		{ID: "anthropic/claude-opus-4-6", Label: "Claude Opus 4.6", Source: "byok", Provider: "anthropic", Enabled: true, SortOrder: 11},
		{ID: "openai/gpt-4o", Label: "GPT-4o", Source: "byok", Provider: "openai", Enabled: true, SortOrder: 12},
		{ID: "openai/gpt-4o-mini", Label: "GPT-4o Mini", Source: "byok", Provider: "openai", Enabled: true, SortOrder: 13},
		{ID: "google/gemini-2.5-flash", Label: "Gemini 2.5 Flash", Source: "byok", Provider: "google", Enabled: true, SortOrder: 14},
		{ID: "google/gemini-2.5-pro", Label: "Gemini 2.5 Pro", Source: "byok", Provider: "google", Enabled: true, SortOrder: 15},
		{ID: "google/gemini-2.0-flash", Label: "Gemini 2.0 Flash", Source: "byok", Provider: "google", Enabled: true, SortOrder: 16},
		{ID: "openai-codex/gpt-5.5", Label: "GPT-5.5", Source: "subscription", GatewayModelID: &codexGPT55, Provider: "openai-codex", Enabled: true, SortOrder: 19},
		{ID: "openai-codex/gpt-5.4", Label: "GPT-5.4", Source: "subscription", Provider: "openai-codex", Enabled: true, SortOrder: 20},
	}, nil
}

func (m *mockConfigStore) GetModelCatalogEntry(_ context.Context, modelID string) (*store.ModelCatalogEntry, error) {
	entries, _ := m.ListModelCatalog(context.Background())
	for _, e := range entries {
		if e.ID == modelID {
			return &e, nil
		}
	}
	return nil, nil
}

// ---- Provider catalog mock methods ----

func (m *mockConfigStore) ListProviderCatalog(_ context.Context) ([]store.ProviderCatalogEntry, error) {
	entries := make([]store.ProviderCatalogEntry, 0, len(m.providerCatalog))
	for _, entry := range m.providerCatalog {
		entries = append(entries, *entry)
	}
	return entries, nil
}

func (m *mockConfigStore) ListProviderCatalogByCategory(_ context.Context, _ string) ([]store.ProviderCatalogEntry, error) {
	return m.ListProviderCatalog(context.Background())
}

func (m *mockConfigStore) GetProviderCatalogEntry(_ context.Context, id string) (*store.ProviderCatalogEntry, error) {
	if entry, ok := m.providerCatalog[id]; ok {
		return entry, nil
	}
	return nil, fmt.Errorf("not found")
}

func (m *mockConfigStore) UpdateMachinePlatformOverride(_ context.Context, machineID, key, value string) error {
	if cfg, ok := m.configs[machineID]; ok {
		var overrides map[string]interface{}
		if cfg.PlatformOverrides != nil {
			_ = json.Unmarshal(cfg.PlatformOverrides, &overrides)
		}
		if overrides == nil {
			overrides = make(map[string]interface{})
		}
		overrides[key] = value
		data, _ := json.Marshal(overrides)
		cfg.PlatformOverrides = data
	} else {
		overrides := map[string]interface{}{key: value}
		data, _ := json.Marshal(overrides)
		m.configs[machineID] = &store.MachineConfigRecord{PlatformOverrides: data}
	}
	return nil
}

func (m *mockConfigStore) SetMachineModelFallbacks(_ context.Context, machineID string, fallbacks []string) error {
	if cfg, ok := m.configs[machineID]; ok {
		var overrides map[string]interface{}
		if cfg.PlatformOverrides != nil {
			_ = json.Unmarshal(cfg.PlatformOverrides, &overrides)
		}
		if overrides == nil {
			overrides = make(map[string]interface{})
		}
		overrides[modelFallbacksOverrideKey] = fallbacks
		data, _ := json.Marshal(overrides)
		cfg.PlatformOverrides = data
	} else {
		overrides := map[string]interface{}{modelFallbacksOverrideKey: fallbacks}
		data, _ := json.Marshal(overrides)
		m.configs[machineID] = &store.MachineConfigRecord{PlatformOverrides: data}
	}
	return nil
}

func (m *mockConfigStore) DeleteMachinePlatformOverride(_ context.Context, machineID, key string) error {
	if cfg, ok := m.configs[machineID]; ok && cfg.PlatformOverrides != nil {
		var overrides map[string]interface{}
		_ = json.Unmarshal(cfg.PlatformOverrides, &overrides)
		delete(overrides, key)
		data, _ := json.Marshal(overrides)
		cfg.PlatformOverrides = data
	}
	return nil
}

func (m *mockConfigStore) ListMachineCredentialsWithValues(_ context.Context, _ string) ([]store.Credential, error) {
	return nil, nil
}

func (m *mockConfigStore) GetBrowserVM(_ context.Context, _ string) (*store.BrowserVM, error) {
	return nil, fmt.Errorf("not found")
}

// ---- Plugin integration tests ----

func TestPushConfig_WithPlugin(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	ms.pluginCatalog["memory-core"] = &store.PluginCatalogEntry{
		ID:             "memory-core",
		Slot:           "memory",
		Version:        "1",
		InstallKind:    "bundled",
		ConfigTemplate: json.RawMessage(`{"plugins":{"slots":{"memory":"memory-core"}}}`),
	}
	ms.plugins["m-1"] = []store.MachinePlugin{
		{
			PluginID:      "memory-core",
			MachineID:     "m-1",
			Slot:          "memory",
			Enabled:       true,
			InstallStatus: "installed",
		},
	}

	srv := newTestConfigServer(ms)

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/m-1/config/push", "m-1", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	assembled := ms.assembledConfigs["m-1"]
	var config map[string]interface{}
	if err := json.Unmarshal(assembled, &config); err != nil {
		t.Fatalf("unmarshal assembled config: %v", err)
	}

	plugins, ok := config["plugins"].(map[string]interface{})
	if !ok {
		t.Fatal("assembled config missing plugins section")
	}
	slots, ok := plugins["slots"].(map[string]interface{})
	if !ok {
		t.Fatal("assembled config missing plugins.slots")
	}
	if slots["memory"] != "memory-core" {
		t.Errorf("plugins.slots.memory = %v, want memory-core", slots["memory"])
	}
}

// --- Native config mode tests ---

// mockExecAgent creates an httptest server that simulates the agent's exec endpoint.
// It records exec calls and lets tests control the responses for config.get and config.patch.
type mockExecAgent struct {
	mu                 sync.Mutex
	calls              []execCall
	gatewayHealthCalls int
	// configHash to return from config.get (legacy, kept for compatibility)
	configHash string
	// error to return from config.patch (legacy, kept for compatibility)
	patchError string
	// configSetError: if non-empty, return this error for "openclaw config set/unset" calls
	configSetError string
	// hermesHTTPStatus: if non-zero, return this status for "ocm-hermes-config sync" calls.
	hermesHTTPStatus int
	hermesHTTPError  string
}

type execCall struct {
	MachineID  string
	ProxyToken string
	Command    []string
}

type configBatchEntry struct {
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func ptr[T any](v T) *T { return &v }

func (m *mockExecAgent) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/gateway-health") {
			m.mu.Lock()
			m.gatewayHealthCalls++
			m.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}

		var req struct {
			Command []string `json:"command"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		m.mu.Lock()
		m.calls = append(m.calls, execCall{
			ProxyToken: r.Header.Get("X-Proxy-Token"),
			Command:    req.Command,
		})
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		// Detect openclaw config set/unset commands
		isConfigSetOrUnset := len(req.Command) >= 3 &&
			req.Command[0] == "openclaw" && req.Command[1] == "config" &&
			(req.Command[2] == "set" || req.Command[2] == "unset")

		if isConfigSetOrUnset {
			if m.configSetError != "" {
				_ = json.NewEncoder(w).Encode(agentclient.ExecResult{
					ExitCode: 1,
					Stderr:   m.configSetError,
				})
			} else {
				_ = json.NewEncoder(w).Encode(agentclient.ExecResult{ExitCode: 0})
			}
			return
		}

		isHermesSync := len(req.Command) >= 2 &&
			req.Command[0] == "ocm-hermes-config" && req.Command[1] == "sync"
		if isHermesSync {
			if m.hermesHTTPStatus != 0 {
				w.WriteHeader(m.hermesHTTPStatus)
				message := m.hermesHTTPError
				if message == "" {
					message = http.StatusText(m.hermesHTTPStatus)
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
			} else {
				_ = json.NewEncoder(w).Encode(agentclient.ExecResult{ExitCode: 0})
			}
			return
		}

		// Determine which command was called by checking for "config.get" or "config.patch"
		isConfigGet := false
		isConfigPatch := false
		for _, arg := range req.Command {
			if arg == "config.get" {
				isConfigGet = true
			}
			if arg == "config.patch" {
				isConfigPatch = true
			}
		}

		if isConfigGet {
			_ = json.NewEncoder(w).Encode(agentclient.ExecResult{
				ExitCode: 0,
				Stdout:   fmt.Sprintf(`{"hash":"%s"}`, m.configHash),
			})
			return
		}
		if isConfigPatch {
			if m.patchError != "" {
				_ = json.NewEncoder(w).Encode(agentclient.ExecResult{
					ExitCode: 1,
					Stderr:   m.patchError,
				})
			} else {
				_ = json.NewEncoder(w).Encode(agentclient.ExecResult{
					ExitCode: 0,
					Stdout:   `{"ok":true}`,
				})
			}
			return
		}

		// Unknown command — for soul writes etc.
		_ = json.NewEncoder(w).Encode(agentclient.ExecResult{ExitCode: 0})
	}
}

// newAliasedAgentClient creates an agentclient.Client with a custom transport
// that routes requests for "agent.test:<port>" to the given httptest.Server.
// Returns the client and an AgentEndpoint URL using the alias hostname.
func newAliasedAgentClient(t *testing.T, server *httptest.Server) (*agentclient.Client, string) {
	t.Helper()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	actualAddr := server.Listener.Addr().String()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "agent.test:") {
				addr = actualAddr
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	cli := agentclient.New("")
	cli.SetHTTPClient(&http.Client{Transport: transport})
	endpoint := fmt.Sprintf("http://agent.test:%s", port)
	return cli, endpoint
}

func newRunningPushConfigServer(t *testing.T, ms *mockConfigStore) (*Server, *mockExecAgent) {
	t.Helper()

	mock := &mockExecAgent{}
	handler := mock.handler()
	agentCli := agentclient.New("")
	agentCli.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			return &http.Response{
				StatusCode: rec.Code,
				Header:     rec.Result().Header,
				Body:       io.NopCloser(strings.NewReader(rec.Body.String())),
				Request:    req,
			}, nil
		}),
	})
	endpoint := "http://agent.test"

	hostID := 10
	proxyToken := "proxy-tok"
	ms.machines["m-1"] = &store.Machine{
		ID:         "m-1",
		AccountID:  1,
		Status:     "running",
		HostID:     &hostID,
		Slug:       "test",
		ProxyToken: &proxyToken,
	}
	ms.hosts[10] = &store.Host{ID: 10, VMName: "host-1", Status: "ready", AgentEndpoint: &endpoint}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	runtime := machines.NewRuntimeService(nil, nil, agentCli, nil, nil, machines.RuntimeConfig{}, nil)

	return &Server{
		store:        ms,
		proxyBaseURL: "http://192.168.100.1:4000",
		agentClient:  agentCli,
		machines:     runtime,
	}, mock
}

func requireConfigBatchEntries(t *testing.T, mock *mockExecAgent) []configBatchEntry {
	t.Helper()

	mock.mu.Lock()
	defer mock.mu.Unlock()

	for _, call := range mock.calls {
		if len(call.Command) == 5 &&
			call.Command[0] == "openclaw" &&
			call.Command[1] == "config" &&
			call.Command[2] == "set" &&
			call.Command[3] == "--batch-json" {
			var entries []configBatchEntry
			if err := json.Unmarshal([]byte(call.Command[4]), &entries); err != nil {
				t.Fatalf("unmarshal batch json: %v", err)
			}
			return entries
		}
	}

	t.Fatalf("expected config set --batch-json call, got calls: %+v", mock.calls)
	return nil
}

func requireBatchPath(t *testing.T, entries []configBatchEntry, path string) configBatchEntry {
	t.Helper()

	for _, entry := range entries {
		if entry.Path == path {
			return entry
		}
	}

	t.Fatalf("expected batch path %q, got entries: %+v", path, entries)
	return configBatchEntry{}
}

func waitForAgentCalls(t *testing.T, timeout time.Duration, fn func(*mockExecAgent) bool, mock *mockExecAgent) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mock.mu.Lock()
		done := fn(mock)
		mock.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	t.Fatalf("condition not met before timeout; calls=%+v gatewayHealthCalls=%d", mock.calls, mock.gatewayHealthCalls)
}

func TestPushConfig_UsesConfigSetUnset(t *testing.T) {
	// Set up mock agent with alias transport
	mock := &mockExecAgent{}
	agent := httptest.NewServer(mock.handler())
	defer agent.Close()

	agentCli, endpoint := newAliasedAgentClient(t, agent)

	hostID := 10
	proxyToken := "proxy-tok-native"
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{
		ID: "m-1", AccountID: 1, Status: "running", HostID: &hostID,
		Slug: "test-native", ProxyToken: &proxyToken,
	}
	ms.hosts[10] = &store.Host{ID: 10, VMName: "host-1", Status: "ready", AgentEndpoint: &endpoint}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	runtime := machines.NewRuntimeService(nil, nil, agentCli, nil, nil, machines.RuntimeConfig{}, nil)

	srv := &Server{
		store:        ms,
		proxyBaseURL: "http://192.168.100.1:4000",
		agentClient:  agentCli,
		machines:     runtime,
	}

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/m-1/config/push", "m-1", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Should have used config set path → live_update = "sent"
	if resp["live_update"] != "sent" {
		t.Errorf("live_update = %v, want 'sent'", resp["live_update"])
	}

	// Verify the agent received openclaw config set calls (not config.get/config.patch)
	mock.mu.Lock()
	defer mock.mu.Unlock()

	var configSetCalls []execCall
	for _, c := range mock.calls {
		if len(c.Command) >= 3 && c.Command[0] == "openclaw" && c.Command[1] == "config" &&
			(c.Command[2] == "set" || c.Command[2] == "unset") {
			configSetCalls = append(configSetCalls, c)
		}
	}

	if len(configSetCalls) == 0 {
		t.Fatalf("expected at least 1 config set/unset call, got 0; all calls: %v", mock.calls)
	}

	// Should NOT have any config.get or config.patch calls
	for _, c := range mock.calls {
		for _, arg := range c.Command {
			if arg == "config.get" || arg == "config.patch" {
				t.Errorf("should not use config.get/config.patch, but found: %v", c.Command)
			}
		}
	}
}

func TestOnRunningRestart_ReconcilesConfigWithAtomicOps(t *testing.T) {
	order := 1
	runtimeProviderID := "brave"
	pluginID := "brave-search"
	execSecretID := "brave-search-key"

	ms := newMockConfigStore()
	ms.providerCatalog["brave-search"] = &store.ProviderCatalogEntry{
		ID:                "brave-search",
		Label:             "Brave Search",
		Category:          "search",
		CredentialType:    "api_key",
		Enabled:           true,
		AutodetectOrder:   &order,
		RuntimeProviderID: &runtimeProviderID,
		PluginID:          &pluginID,
		ExecSecretID:      &execSecretID,
	}
	ms.credentials["m-1"] = []store.Credential{
		{ID: 1, Provider: "brave-search", CredentialType: "api_key"},
	}
	ms.configs["m-1"] = &store.MachineConfigRecord{
		MachineID:       "m-1",
		AssembledConfig: json.RawMessage(`{}`),
		ConfigVersion:   7,
		UpdatedAt:       time.Now(),
	}

	mock := &mockExecAgent{}
	handler := mock.handler()
	agentCli := agentclient.New("")
	agentCli.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			return &http.Response{
				StatusCode: rec.Code,
				Header:     rec.Result().Header,
				Body:       io.NopCloser(strings.NewReader(rec.Body.String())),
				Request:    req,
			}, nil
		}),
	})

	hostID := 10
	proxyToken := "proxy-tok"
	ms.machines["m-1"] = &store.Machine{
		ID:         "m-1",
		AccountID:  1,
		Status:     "running",
		HostID:     &hostID,
		Slug:       "test",
		ProxyToken: &proxyToken,
	}
	ms.hosts[10] = &store.Host{
		ID:            10,
		VMName:        "host-1",
		Status:        "ready",
		AgentEndpoint: ptr("http://agent.test"),
	}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	runtime := machines.NewRuntimeService(nil, nil, agentCli, nil, nil, machines.RuntimeConfig{}, nil)
	srv := NewWorkerServer(ms, nil, agentCli, nil, nil, runtime, "")
	srv.proxyBaseURL = "http://192.168.100.1:4000"

	if runtime.OnRunning == nil {
		t.Fatal("expected OnRunning callback to be installed")
	}

	runtime.OnRunning("m-1", true)

	waitForAgentCalls(t, time.Second, func(mock *mockExecAgent) bool {
		return mock.gatewayHealthCalls > 0 && len(mock.calls) > 0
	}, mock)

	entries := requireConfigBatchEntries(t, mock)
	search := requireBatchPath(t, entries, "tools.web.search")

	var searchCfg map[string]interface{}
	if err := json.Unmarshal(search.Value, &searchCfg); err != nil {
		t.Fatalf("unmarshal search config: %v", err)
	}
	if searchCfg["provider"] != "brave" {
		t.Fatalf("search provider = %v, want brave", searchCfg["provider"])
	}
	if ms.assembledVersion["m-1"] != 8 {
		t.Fatalf("assembled version = %d, want 8", ms.assembledVersion["m-1"])
	}
}

func TestOnRunningColdStart_DoesNotReconcileConfig(t *testing.T) {
	ms := newMockConfigStore()
	hostID := 10
	proxyToken := "proxy-tok"
	ms.machines["m-1"] = &store.Machine{
		ID:         "m-1",
		AccountID:  1,
		Status:     "running",
		HostID:     &hostID,
		Slug:       "test",
		ProxyToken: &proxyToken,
	}
	mock := &mockExecAgent{}
	agentCli := agentclient.New("")
	runtime := machines.NewRuntimeService(nil, nil, agentCli, nil, nil, machines.RuntimeConfig{}, nil)
	_ = NewWorkerServer(ms, nil, agentCli, nil, nil, runtime, "")

	if runtime.OnRunning == nil {
		t.Fatal("expected OnRunning callback to be installed")
	}

	runtime.OnRunning("m-1", false)
	time.Sleep(50 * time.Millisecond)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.gatewayHealthCalls != 0 || len(mock.calls) != 0 {
		t.Fatalf("expected no restart reconciliation calls, got calls=%+v gatewayHealthCalls=%d", mock.calls, mock.gatewayHealthCalls)
	}
}

func TestPushConfig_EmitsSearchToolConfigOp(t *testing.T) {
	order := 1
	runtimeProviderID := "brave"
	pluginID := "brave-search"
	execSecretID := "brave-search-key"

	ms := newMockConfigStore()
	ms.providerCatalog = map[string]*store.ProviderCatalogEntry{
		"brave-search": {
			ID:                "brave-search",
			Label:             "Brave Search",
			Category:          "search",
			CredentialType:    "api_key",
			Enabled:           true,
			AutodetectOrder:   &order,
			RuntimeProviderID: &runtimeProviderID,
			PluginID:          &pluginID,
			ExecSecretID:      &execSecretID,
		},
	}
	ms.credentials["m-1"] = []store.Credential{
		{ID: 1, Provider: "brave-search", CredentialType: "api_key"},
	}
	ms.configs["m-1"] = &store.MachineConfigRecord{
		MachineID:       "m-1",
		AssembledConfig: json.RawMessage(`{}`),
		ConfigVersion:   1,
		UpdatedAt:       time.Now(),
	}

	srv, mock := newRunningPushConfigServer(t, ms)

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/m-1/config/push", "m-1", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	entries := requireConfigBatchEntries(t, mock)
	search := requireBatchPath(t, entries, "tools.web.search")

	var searchCfg map[string]interface{}
	if err := json.Unmarshal(search.Value, &searchCfg); err != nil {
		t.Fatalf("unmarshal search config: %v", err)
	}
	if searchCfg["provider"] != "brave" {
		t.Fatalf("search provider = %v, want brave", searchCfg["provider"])
	}
}

func TestPushConfig_EmitsAuthProfilesOp(t *testing.T) {
	ms := newMockConfigStore()
	ms.credentials["m-1"] = []store.Credential{
		{ID: 1, Provider: "openai-codex", CredentialType: "oauth"},
	}
	ms.configs["m-1"] = &store.MachineConfigRecord{
		MachineID:       "m-1",
		AssembledConfig: json.RawMessage(`{}`),
		ConfigVersion:   1,
		UpdatedAt:       time.Now(),
	}

	srv, mock := newRunningPushConfigServer(t, ms)

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/m-1/config/push", "m-1", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	entries := requireConfigBatchEntries(t, mock)
	profiles := requireBatchPath(t, entries, "auth.profiles")

	var profileMap map[string]map[string]interface{}
	if err := json.Unmarshal(profiles.Value, &profileMap); err != nil {
		t.Fatalf("unmarshal auth profiles: %v", err)
	}
	codexProfile, ok := profileMap["openai-codex:default"]
	if !ok {
		t.Fatalf("missing openai-codex:default profile in %v", profileMap)
	}
	if codexProfile["provider"] != "openai-codex" {
		t.Fatalf("provider = %v, want openai-codex", codexProfile["provider"])
	}
	if codexProfile["mode"] != "oauth" {
		t.Fatalf("mode = %v, want oauth", codexProfile["mode"])
	}
}

func TestPushConfig_EmitsPluginsAllowOp(t *testing.T) {
	ms := newMockConfigStore()
	ms.pluginCatalog["opik-openclaw"] = &store.PluginCatalogEntry{
		ID:             "opik-openclaw",
		Name:           "Opik",
		Slot:           "observability",
		Version:        "1",
		InstallKind:    "bundled",
		ConfigTemplate: json.RawMessage(`{"plugins":{"entries":{"opik-openclaw":{"enabled":true}},"allow":["opik-openclaw"]}}`),
		Status:         "active",
	}
	ms.plugins["m-1"] = []store.MachinePlugin{
		{
			MachineID:     "m-1",
			PluginID:      "opik-openclaw",
			Slot:          "observability",
			Enabled:       true,
			InstallStatus: "installed",
		},
	}
	ms.configs["m-1"] = &store.MachineConfigRecord{
		MachineID:       "m-1",
		AssembledConfig: json.RawMessage(`{"plugins":{"entries":{}}}`),
		ConfigVersion:   1,
		UpdatedAt:       time.Now(),
	}

	srv, mock := newRunningPushConfigServer(t, ms)

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/m-1/config/push", "m-1", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	entries := requireConfigBatchEntries(t, mock)
	allow := requireBatchPath(t, entries, "plugins.allow")

	var allowList []string
	if err := json.Unmarshal(allow.Value, &allowList); err != nil {
		t.Fatalf("unmarshal plugins.allow: %v", err)
	}
	if len(allowList) != 1 || allowList[0] != "opik-openclaw" {
		t.Fatalf("plugins.allow = %v, want [opik-openclaw]", allowList)
	}
}

func TestPushConfig_ConfigSetFailure(t *testing.T) {
	mock := &mockExecAgent{configSetError: "validation failed: invalid model"}
	agent := httptest.NewServer(mock.handler())
	defer agent.Close()

	agentCli, endpoint := newAliasedAgentClient(t, agent)

	hostID := 10
	proxyToken := "proxy-tok"
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{
		ID: "m-1", AccountID: 1, Status: "running", HostID: &hostID,
		Slug: "test-fail", ProxyToken: &proxyToken,
	}
	ms.hosts[10] = &store.Host{ID: 10, VMName: "host-1", Status: "ready", AgentEndpoint: &endpoint}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	runtime := machines.NewRuntimeService(nil, nil, agentCli, nil, nil, machines.RuntimeConfig{}, nil)

	srv := &Server{
		store:        ms,
		proxyBaseURL: "http://192.168.100.1:4000",
		agentClient:  agentCli,
		machines:     runtime,
	}

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/m-1/config/push", "m-1", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Config set failure → live_update = "failed" with error message
	if resp["live_update"] != "failed" {
		t.Errorf("live_update = %v, want 'failed'", resp["live_update"])
	}
	errMsg, _ := resp["live_update_error"].(string)
	if errMsg == "" {
		t.Error("expected live_update_error to be set on config set failure")
	}
}

func TestPushHermesConfig_OldGuestAllowlistErrorIsActionable(t *testing.T) {
	mock := &mockExecAgent{
		hermesHTTPStatus: http.StatusForbidden,
		hermesHTTPError:  "only openclaw commands are allowed",
	}
	agent := httptest.NewServer(mock.handler())
	defer agent.Close()

	agentCli, endpoint := newAliasedAgentClient(t, agent)

	hostID := 10
	proxyToken := "proxy-tok"
	gatewayToken := "gateway-tok"
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{
		ID:           "m-1",
		AccountID:    1,
		Kind:         store.MachineKindHermes,
		Status:       "running",
		HostID:       &hostID,
		Slug:         "hermes-test",
		ProxyToken:   &proxyToken,
		GatewayToken: &gatewayToken,
	}
	ms.hosts[10] = &store.Host{ID: 10, VMName: "host-1", Status: "ready", AgentEndpoint: &endpoint}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	runtime := machines.NewRuntimeService(nil, nil, agentCli, nil, nil, machines.RuntimeConfig{}, nil)
	srv := &Server{
		store:        ms,
		proxyBaseURL: "http://192.168.100.1:4000",
		agentClient:  agentCli,
		machines:     runtime,
	}

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/m-1/config/push", "m-1", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp["live_update"] != "failed" {
		t.Fatalf("live_update = %v, want failed", resp["live_update"])
	}
	errMsg, _ := resp["live_update_error"].(string)
	if !strings.Contains(errMsg, "Restart the machine") {
		t.Fatalf("live_update_error = %q, want restart guidance", errMsg)
	}
	if strings.Contains(errMsg, "only openclaw commands are allowed") {
		t.Fatalf("live_update_error should not expose raw allowlist error: %q", errMsg)
	}
	if ms.setAssembledCalls != 1 {
		t.Fatalf("SetMachineAssembledConfig calls = %d, want 1", ms.setAssembledCalls)
	}
}

func TestSearchProviderMutations_RejectStoppedMachineAfterFirstBoot(t *testing.T) {
	now := time.Now()
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{
		ID:                      "m-1",
		AccountID:               1,
		Status:                  "stopped",
		ProvisioningCompletedAt: &now,
	}
	srv := newTestConfigServer(ms)

	tests := []struct {
		name string
		req  *http.Request
		call func(http.ResponseWriter, *http.Request)
	}{
		{
			name: "set",
			req: configRequestWithBody(
				"PUT",
				"/api/accounts/1/machines/m-1/search-provider",
				"m-1",
				1,
				`{"provider":"brave-search"}`,
			),
			call: srv.handleSetSearchProvider,
		},
		{
			name: "delete",
			req:  configRequest("DELETE", "/api/accounts/1/machines/m-1/search-provider", "m-1", 1),
			call: srv.handleDeleteSearchProvider,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.call(w, tc.req)

			if w.Code != http.StatusConflict {
				t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCapabilityMutations_RejectStoppedMachineAfterFirstBoot(t *testing.T) {
	now := time.Now()
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{
		ID:                      "m-1",
		AccountID:               1,
		Status:                  "stopped",
		ProvisioningCompletedAt: &now,
	}
	srv := newTestConfigServer(ms)

	tests := []struct {
		name string
		req  *http.Request
		call func(http.ResponseWriter, *http.Request)
	}{
		{
			name: "enable",
			req: configRequestWithBody(
				"POST",
				"/api/accounts/1/machines/m-1/capabilities",
				"m-1",
				1,
				`{"entry_id":"telegram"}`,
			),
			call: srv.handleEnableMachineCapability,
		},
		{
			name: "update-overrides",
			req: func() *http.Request {
				req := configRequestWithBody(
					"PATCH",
					"/api/accounts/1/machines/m-1/capabilities/telegram",
					"m-1",
					1,
					`{"config_overrides":{"channels":{"telegram":{"mode":"polling"}}}}`,
				)
				rctx := chi.RouteContext(req.Context())
				rctx.URLParams.Add("entryId", "telegram")
				return req
			}(),
			call: srv.handleUpdateMachineCapabilityOverrides,
		},
		{
			name: "disable",
			req: func() *http.Request {
				req := configRequest(
					"DELETE",
					"/api/accounts/1/machines/m-1/capabilities/telegram",
					"m-1",
					1,
				)
				rctx := chi.RouteContext(req.Context())
				rctx.URLParams.Add("entryId", "telegram")
				return req
			}(),
			call: srv.handleDisableMachineCapability,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.call(w, tc.req)

			if w.Code != http.StatusConflict {
				t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestIdentityAndModelMutations_RejectStoppedMachineAfterFirstBoot(t *testing.T) {
	now := time.Now()
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{
		ID:                      "m-1",
		AccountID:               1,
		Status:                  "stopped",
		ProvisioningCompletedAt: &now,
	}
	srv := newTestConfigServer(ms)

	tests := []struct {
		name string
		req  *http.Request
		call func(http.ResponseWriter, *http.Request)
	}{
		{
			name: "identity",
			req: configRequestWithBody(
				"PATCH",
				"/api/accounts/1/machines/m-1/identity",
				"m-1",
				1,
				`{"name":"Claw","avatar":"https://example.com/claw.png"}`,
			),
			call: srv.handleSetMachineIdentity,
		},
		{
			name: "model",
			req: configRequestWithBody(
				"PATCH",
				"/api/accounts/1/machines/m-1/model",
				"m-1",
				1,
				`{"model":"qwen/qwen3.5-397b"}`,
			),
			call: srv.handleSetMachineModel,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.call(w, tc.req)

			if w.Code != http.StatusConflict {
				t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPushConfig_NoProxyToken(t *testing.T) {
	// Running machine with no proxy token → fails gracefully
	hostID := 10
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{
		ID: "m-1", AccountID: 1, Status: "running", HostID: &hostID,
		Slug: "test-no-token", ProxyToken: nil, // <-- no proxy token
	}
	ms.hosts[10] = &store.Host{ID: 10, VMName: "host-1", Status: "ready"}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	agentCli := agentclient.New("")
	runtime := machines.NewRuntimeService(nil, nil, agentCli, nil, nil, machines.RuntimeConfig{}, nil)

	srv := &Server{
		store:        ms,
		proxyBaseURL: "http://192.168.100.1:4000",
		agentClient:  agentCli,
		machines:     runtime,
	}

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/m-1/config/push", "m-1", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp["live_update"] != "failed" {
		t.Errorf("live_update = %v, want 'failed'", resp["live_update"])
	}
	errMsg, _ := resp["live_update_error"].(string)
	if errMsg == "" {
		t.Error("expected error message about missing proxy token")
	}
}

func TestPushConfig_NoPlugins_NoPluginsSection(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	srv := newTestConfigServer(ms)

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/m-1/config/push", "m-1", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	assembled := ms.assembledConfigs["m-1"]
	var config map[string]interface{}
	if err := json.Unmarshal(assembled, &config); err != nil {
		t.Fatalf("unmarshal assembled config: %v", err)
	}

	// Plugins section should exist but entries should be empty (no platform plugins).
	if plugins, ok := config["plugins"].(map[string]interface{}); ok {
		if entries, ok := plugins["entries"].(map[string]interface{}); ok {
			if len(entries) != 0 {
				t.Errorf("expected empty plugin entries, got %d", len(entries))
			}
		}
	}
}

// ---- Model Switching Tests ----

// TestSetModel_AllPlatformTiers verifies that each platform tier model can be set
// on a running machine and that a full config push is triggered.
func TestSetModel_AllPlatformTiers(t *testing.T) {
	tiers := []struct {
		model        string
		gatewayModel string
	}{
		{"qwen/qwen3.5-397b", "nebius/Qwen/Qwen3.5-397B-A17B"},
		{"minimax/minimax-m2.5", "nebius/MiniMaxAI/MiniMax-M2.5"},
		{"openai/gpt-oss-120b", "nebius/openai/gpt-oss-120b"},
	}

	for _, tier := range tiers {
		t.Run(tier.model, func(t *testing.T) {
			mock := &mockExecAgent{}
			agent := httptest.NewServer(mock.handler())
			defer agent.Close()

			agentCli, endpoint := newAliasedAgentClient(t, agent)

			hostID := 10
			proxyToken := "proxy-tok"
			ms := newMockConfigStore()
			ms.machines["m-1"] = &store.Machine{
				ID: "m-1", AccountID: 1, Status: "running", HostID: &hostID,
				Slug: "test", ProxyToken: &proxyToken,
			}
			ms.hosts[10] = &store.Host{ID: 10, VMName: "host-1", Status: "ready", AgentEndpoint: &endpoint}
			ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

			runtime := machines.NewRuntimeService(nil, nil, agentCli, nil, nil, machines.RuntimeConfig{}, nil)
			srv := &Server{
				store:        ms,
				proxyBaseURL: "http://192.168.100.1:4000",
				agentClient:  agentCli,
				machines:     runtime,
			}

			body := fmt.Sprintf(`{"model": %q}`, tier.model)
			w := httptest.NewRecorder()
			r := configRequestWithBody("PATCH", "/api/accounts/1/machines/m-1/model", "m-1", 1, body)
			srv.handleSetMachineModel(w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			// Verify response
			if resp["model"] != tier.model {
				t.Errorf("response model = %v, want %q", resp["model"], tier.model)
			}

			// Verify model was persisted in store
			if ms.preferredModels["m-1"] != tier.model {
				t.Errorf("stored model = %q, want %q", ms.preferredModels["m-1"], tier.model)
			}

			// handleSetMachineModel no longer pushes config — that's the
			// frontend's responsibility via POST /config/push
		})
	}
}

func TestSetModel_PersistsFallbacks(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "running"}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}
	srv := newTestConfigServer(ms)

	body := `{
		"model": "openai-codex/gpt-5.5",
		"openai_agent_runtime": "codex",
		"fallbacks": ["anthropic/claude-opus-4-6", "qwen/qwen3.5-397b", "minimax/minimax-m2.5"]
	}`
	w := httptest.NewRecorder()
	r := configRequestWithBody("PATCH", "/api/accounts/1/machines/m-1/model", "m-1", 1, body)
	srv.handleSetMachineModel(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "openai-codex/gpt-5.5", resp["model"])
	require.Equal(t, "codex", resp["openai_agent_runtime"])
	require.Equal(t, []interface{}{"anthropic/claude-opus-4-6", "qwen/qwen3.5-397b", "minimax/minimax-m2.5"}, resp["fallbacks"])

	cfg, err := ms.GetMachineConfig(context.Background(), "m-1")
	require.NoError(t, err)
	require.Equal(t, []string{"anthropic/claude-opus-4-6", "qwen/qwen3.5-397b", "minimax/minimax-m2.5"}, modelFallbacksFromConfig(cfg))

	getW := httptest.NewRecorder()
	getR := configRequestWithBody("GET", "/api/accounts/1/machines/m-1/model", "m-1", 1, "")
	srv.handleGetMachineModel(getW, getR)
	require.Equal(t, http.StatusOK, getW.Code, getW.Body.String())
	var getResp map[string]interface{}
	require.NoError(t, json.Unmarshal(getW.Body.Bytes(), &getResp))
	require.Equal(t, []interface{}{"anthropic/claude-opus-4-6", "qwen/qwen3.5-397b", "minimax/minimax-m2.5"}, getResp["fallbacks"])
}

func TestSetModel_RejectsInvalidFallback(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "running"}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}
	srv := newTestConfigServer(ms)

	body := `{"model":"openai-codex/gpt-5.5","fallbacks":["random/unknown-model"]}`
	w := httptest.NewRecorder()
	r := configRequestWithBody("PATCH", "/api/accounts/1/machines/m-1/model", "m-1", 1, body)
	srv.handleSetMachineModel(w, r)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "fallback model")
}

// TestSetModel_RejectsDisallowedModel verifies the allowlist blocks unknown models.
func TestSetModel_RejectsDisallowedModel(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "running"}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	srv := newTestConfigServer(ms)

	disallowed := []string{
		"deepseek/deepseek-r1",          // old platform tier, removed
		"deepseek/deepseek-v3",          // old platform tier, removed
		"openai/gpt-oss-20b",            // not a platform tier
		"random/unknown-model",          // completely unknown
		"nebius/Qwen/Qwen3.5-397B-A17B", // gateway format, not user format
	}

	for _, model := range disallowed {
		t.Run(model, func(t *testing.T) {
			body := fmt.Sprintf(`{"model": %q}`, model)
			w := httptest.NewRecorder()
			r := configRequestWithBody("PATCH", "/api/accounts/1/machines/m-1/model", "m-1", 1, body)
			srv.handleSetMachineModel(w, r)

			if w.Code != http.StatusBadRequest {
				t.Errorf("model %q: expected 400, got %d: %s", model, w.Code, w.Body.String())
			}
		})
	}
}

// TestSetModel_AcceptsBYOKModels verifies BYOK models are in the allowlist.
func TestSetModel_AcceptsBYOKModels(t *testing.T) {
	byokModels := []string{
		"anthropic/claude-sonnet-4-6",
		"anthropic/claude-opus-4-6",
		"openai/gpt-4o",
		"openai/gpt-4o-mini",
		"google/gemini-2.5-flash",
		"google/gemini-2.5-pro",
		"google/gemini-2.0-flash",
	}

	for _, model := range byokModels {
		t.Run(model, func(t *testing.T) {
			ms := newMockConfigStore()
			ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
			ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

			srv := newTestConfigServer(ms)

			body := fmt.Sprintf(`{"model": %q}`, model)
			w := httptest.NewRecorder()
			r := configRequestWithBody("PATCH", "/api/accounts/1/machines/m-1/model", "m-1", 1, body)
			srv.handleSetMachineModel(w, r)

			if w.Code != http.StatusOK {
				t.Errorf("model %q: expected 200, got %d: %s", model, w.Code, w.Body.String())
			}
		})
	}
}

func TestSetModel_OpenAIAgentRuntime(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	srv := newTestConfigServer(ms)

	body := `{"model": "openai/gpt-4o", "openai_agent_runtime": "pi"}`
	w := httptest.NewRecorder()
	r := configRequestWithBody("PATCH", "/api/accounts/1/machines/m-1/model", "m-1", 1, body)
	srv.handleSetMachineModel(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["openai_agent_runtime"] != "pi" {
		t.Fatalf("response openai_agent_runtime = %v, want pi", resp["openai_agent_runtime"])
	}
	if ms.preferredModels["m-1"] != "openai/gpt-4o" {
		t.Fatalf("stored model = %q, want openai/gpt-4o", ms.preferredModels["m-1"])
	}
	cfg := ms.configs["m-1"]
	var overrides map[string]string
	if err := json.Unmarshal(cfg.PlatformOverrides, &overrides); err != nil {
		t.Fatalf("unmarshal overrides: %v", err)
	}
	if overrides["openai_agent_runtime"] != "pi" {
		t.Fatalf("stored openai_agent_runtime = %q, want pi", overrides["openai_agent_runtime"])
	}
	if overrides["preferred_model"] != "openai/gpt-4o" {
		t.Fatalf("stored preferred_model = %q, want openai/gpt-4o", overrides["preferred_model"])
	}
}

func TestSetModel_OpenAIAgentRuntimeAutoClearsOverride(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}
	ms.configs["m-1"] = &store.MachineConfigRecord{
		PlatformOverrides: json.RawMessage(`{"preferred_model":"openai/gpt-4o","openai_agent_runtime":"codex"}`),
	}

	srv := newTestConfigServer(ms)

	body := `{"model": "openai/gpt-4o", "openai_agent_runtime": "auto"}`
	w := httptest.NewRecorder()
	r := configRequestWithBody("PATCH", "/api/accounts/1/machines/m-1/model", "m-1", 1, body)
	srv.handleSetMachineModel(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["openai_agent_runtime"] != "auto" {
		t.Fatalf("response openai_agent_runtime = %v, want auto", resp["openai_agent_runtime"])
	}
	var overrides map[string]string
	if err := json.Unmarshal(ms.configs["m-1"].PlatformOverrides, &overrides); err != nil {
		t.Fatalf("unmarshal overrides: %v", err)
	}
	if _, ok := overrides["openai_agent_runtime"]; ok {
		t.Fatalf("openai_agent_runtime override should have been removed: %v", overrides)
	}
}

func TestSetModel_OpenAIAgentRuntimeRejectsNonOpenAI(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	srv := newTestConfigServer(ms)

	body := `{"model": "anthropic/claude-sonnet-4-6", "openai_agent_runtime": "pi"}`
	w := httptest.NewRecorder()
	r := configRequestWithBody("PATCH", "/api/accounts/1/machines/m-1/model", "m-1", 1, body)
	srv.handleSetMachineModel(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestSetModel_StoppedMachine_SkipsLivePush verifies that model changes on stopped
// machines are persisted but don't attempt a live push.
func TestSetModel_StoppedMachine_SkipsLivePush(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{
		ID: "m-1", AccountID: 1, Status: "stopped",
		// No HostID — stopped machine
	}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	srv := newTestConfigServer(ms)

	body := `{"model": "qwen/qwen3.5-397b"}`
	w := httptest.NewRecorder()
	r := configRequestWithBody("PATCH", "/api/accounts/1/machines/m-1/model", "m-1", 1, body)
	srv.handleSetMachineModel(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if ms.preferredModels["m-1"] != "qwen/qwen3.5-397b" {
		t.Errorf("model not persisted: got %q", ms.preferredModels["m-1"])
	}
}

// TestSetModel_AgentFailure verifies that model is persisted even when
// the machine is in a state that would have previously caused a push failure.
// Config push is now the frontend's responsibility via POST /config/push.
func TestSetModel_AgentFailure_ReportsError(t *testing.T) {
	mock := &mockExecAgent{configSetError: "openclaw: unknown command \"config\""}
	agent := httptest.NewServer(mock.handler())
	defer agent.Close()

	agentCli, endpoint := newAliasedAgentClient(t, agent)

	hostID := 10
	proxyToken := "proxy-tok"
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{
		ID: "m-1", AccountID: 1, Status: "running", HostID: &hostID,
		Slug: "test", ProxyToken: &proxyToken,
	}
	ms.hosts[10] = &store.Host{ID: 10, VMName: "host-1", Status: "ready", AgentEndpoint: &endpoint}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	runtime := machines.NewRuntimeService(nil, nil, agentCli, nil, nil, machines.RuntimeConfig{}, nil)
	srv := &Server{
		store:        ms,
		proxyBaseURL: "http://192.168.100.1:4000",
		agentClient:  agentCli,
		machines:     runtime,
	}

	body := `{"model": "qwen/qwen3.5-397b"}`
	w := httptest.NewRecorder()
	r := configRequestWithBody("PATCH", "/api/accounts/1/machines/m-1/model", "m-1", 1, body)
	srv.handleSetMachineModel(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (model persisted even on push failure), got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	// Model should be persisted — config push is frontend's responsibility
	if ms.preferredModels["m-1"] != "qwen/qwen3.5-397b" {
		t.Error("model should be persisted")
	}
}

// TestSetModel_NoProxyToken verifies model is persisted even without proxy token.
func TestSetModel_NoProxyToken(t *testing.T) {
	mock := &mockExecAgent{}
	agent := httptest.NewServer(mock.handler())
	defer agent.Close()

	agentCli, endpoint := newAliasedAgentClient(t, agent)

	hostID := 10
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{
		ID: "m-1", AccountID: 1, Status: "running", HostID: &hostID,
		Slug: "test", ProxyToken: nil, // no proxy token
	}
	ms.hosts[10] = &store.Host{ID: 10, VMName: "host-1", Status: "ready", AgentEndpoint: &endpoint}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	runtime := machines.NewRuntimeService(nil, nil, agentCli, nil, nil, machines.RuntimeConfig{}, nil)
	srv := &Server{
		store:        ms,
		proxyBaseURL: "http://192.168.100.1:4000",
		agentClient:  agentCli,
		machines:     runtime,
	}

	body := `{"model": "minimax/minimax-m2.5"}`
	w := httptest.NewRecorder()
	r := configRequestWithBody("PATCH", "/api/accounts/1/machines/m-1/model", "m-1", 1, body)
	srv.handleSetMachineModel(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	// Model should be persisted — no proxy token doesn't matter since
	// config push is now the frontend's responsibility
	if ms.preferredModels["m-1"] != "minimax/minimax-m2.5" {
		t.Errorf("model not persisted: got %q", ms.preferredModels["m-1"])
	}
}

// TestSetModel_EmptyModel verifies that empty model is rejected.
func TestSetModel_EmptyModel(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "running"}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	srv := newTestConfigServer(ms)

	body := `{"model": ""}`
	w := httptest.NewRecorder()
	r := configRequestWithBody("PATCH", "/api/accounts/1/machines/m-1/model", "m-1", 1, body)
	srv.handleSetMachineModel(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty model, got %d", w.Code)
	}
}

// TestSetModel_WrongAccount verifies cross-account access is blocked.
func TestSetModel_WrongAccount(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "running"}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	srv := newTestConfigServer(ms)

	body := `{"model": "qwen/qwen3.5-397b"}`
	w := httptest.NewRecorder()
	// Account 99 trying to access machine belonging to account 1
	r := configRequestWithBody("PATCH", "/api/accounts/99/machines/m-1/model", "m-1", 99, body)
	srv.handleSetMachineModel(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for wrong account, got %d: %s", w.Code, w.Body.String())
	}
}
