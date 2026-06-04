//go:build linux

package metadata

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// httptest.NewRequest sets RemoteAddr to "192.0.2.1:1234" by default.
const testVMIP = "192.0.2.1"

// testServerWithMachine creates a metadata server with a machine registered at
// the httptest default source IP.
func testServerWithMachine(cfg MachineConfig) *Server {
	s := New("169.254.169.253", 0)
	s.RegisterMachine(testVMIP, cfg)
	return s
}

func TestHandleMachine_ReturnsInfrastructureFields(t *testing.T) {
	s := testServerWithMachine(MachineConfig{
		MachineID:        "m-infra",
		MachineSlug:      "my-bot",
		GatewayToken:     "gw-tok-123",
		SigningKey:       "sign-key-abc",
		TunnelToken:      "tunnel-tok-xyz",
		VmHostname:       "m-my-bot.openclawmachines.com",
		OpenClawConf:     []byte(`{"identity":{"name":"test"}}`),
		RuntimeSelection: &RuntimeSelection{ResolvedRootfsVersion: "rootfs-2026.04.04", ResolvedOpenClawVersion: "openclaw-2026.04.04", VersionSource: "pinned", RuntimeSource: "artifact"},
	})
	s.AgentVersion = "abc1234-20260319T000000Z"

	req := httptest.NewRequest("GET", "/v1/machine", nil)
	w := httptest.NewRecorder()
	s.handleMachine(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", w.Header().Get("Content-Type"))
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	// Verify all infrastructure fields
	checks := map[string]string{
		"machine_id":                "m-infra",
		"machine_slug":              "my-bot",
		"gateway_token":             "gw-tok-123",
		"signing_key":               "sign-key-abc",
		"tunnel_token":              "tunnel-tok-xyz",
		"vm_hostname":               "m-my-bot.openclawmachines.com",
		"agent_version":             "abc1234-20260319T000000Z",
		"resolved_rootfs_version":   "rootfs-2026.04.04",
		"resolved_openclaw_version": "openclaw-2026.04.04",
		"version_source":            "pinned",
		"runtime_source":            "artifact",
	}
	for key, want := range checks {
		if got := resp[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	// Should NOT contain OpenClawConf fields
	if _, ok := resp["identity"]; ok {
		t.Error("machine response should not contain OpenClawConf fields")
	}
}

func TestHandleMachine_RejectsWithoutNonce(t *testing.T) {
	s := testServerWithMachine(MachineConfig{
		MachineID: "m-nonce",
		Nonce:     "secret-nonce",
	})

	req := httptest.NewRequest("GET", "/v1/machine", nil)
	w := httptest.NewRecorder()
	s.handleMachine(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestHandleMachine_AcceptsValidNonce(t *testing.T) {
	s := testServerWithMachine(MachineConfig{
		MachineID: "m-nonce",
		Nonce:     "secret-nonce",
	})

	req := httptest.NewRequest("GET", "/v1/machine", nil)
	req.Header.Set("X-Metadata-Nonce", "secret-nonce")
	w := httptest.NewRecorder()
	s.handleMachine(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleSecrets_MergesPulledChannelSecrets(t *testing.T) {
	s := testServerWithMachine(MachineConfig{
		MachineID: "m-secrets",
		Secrets:   map[string]string{"OPIK_API_KEY": "opik-key-123"},
	})

	// Configure a SecretFetcher that returns channel secrets on pull.
	s.SecretFetcher = SecretFetcherFunc(func(machineID string) (map[string]string, error) {
		return map[string]string{
			"channel-telegram-botToken": "tg-bot-token-456",
			"channel-discord-token":     "dc-token-789",
		}, nil
	})

	req := httptest.NewRequest("GET", "/v1/secrets", nil)
	w := httptest.NewRecorder()
	s.handleSecrets(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var secrets map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &secrets); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	// Platform secrets
	if secrets["OPIK_API_KEY"] != "opik-key-123" {
		t.Errorf("OPIK_API_KEY = %q, want %q", secrets["OPIK_API_KEY"], "opik-key-123")
	}

	// Channel tokens pulled from backend via SecretFetcher
	if secrets["channel-telegram-botToken"] != "tg-bot-token-456" {
		t.Errorf("channel-telegram-botToken = %q, want %q", secrets["channel-telegram-botToken"], "tg-bot-token-456")
	}
	if secrets["channel-discord-token"] != "dc-token-789" {
		t.Errorf("channel-discord-token = %q, want %q", secrets["channel-discord-token"], "dc-token-789")
	}
}

func TestHandleSecrets_NoSecretFetcher(t *testing.T) {
	s := testServerWithMachine(MachineConfig{
		MachineID: "m-no-channels",
		Secrets:   map[string]string{"OPIK_API_KEY": "opik-key"},
	})
	// No SecretFetcher configured — only platform secrets returned.

	req := httptest.NewRequest("GET", "/v1/secrets", nil)
	w := httptest.NewRecorder()
	s.handleSecrets(w, req)

	var secrets map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &secrets)

	if len(secrets) != 1 {
		t.Errorf("expected 1 secret, got %d: %v", len(secrets), secrets)
	}
	if secrets["OPIK_API_KEY"] != "opik-key" {
		t.Errorf("OPIK_API_KEY = %q, want %q", secrets["OPIK_API_KEY"], "opik-key")
	}
}

func TestHandleSecrets_EmptyBothMaps(t *testing.T) {
	s := testServerWithMachine(MachineConfig{
		MachineID: "m-empty",
	})

	req := httptest.NewRequest("GET", "/v1/secrets", nil)
	w := httptest.NewRecorder()
	s.handleSecrets(w, req)

	var secrets map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &secrets)

	if len(secrets) != 0 {
		t.Errorf("expected 0 secrets, got %d: %v", len(secrets), secrets)
	}
}

func TestHandleSecrets_PullOnCacheMiss(t *testing.T) {
	s := testServerWithMachine(MachineConfig{
		MachineID: "m-pull",
		Secrets:   map[string]string{"OPIK_API_KEY": "opik-key"},
	})
	s.SecretFetcher = SecretFetcherFunc(func(machineID string) (map[string]string, error) {
		return map[string]string{
			"channel-telegram-botToken": "pulled-tg-token",
		}, nil
	})

	req := httptest.NewRequest("GET", "/v1/secrets", nil)
	w := httptest.NewRecorder()
	s.handleSecrets(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var secrets map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &secrets); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if secrets["OPIK_API_KEY"] != "opik-key" {
		t.Errorf("OPIK_API_KEY = %q, want opik-key", secrets["OPIK_API_KEY"])
	}
	if secrets["channel-telegram-botToken"] != "pulled-tg-token" {
		t.Errorf("channel-telegram-botToken = %q, want pulled-tg-token", secrets["channel-telegram-botToken"])
	}
}

func TestHandleSecrets_PullCacheTTL(t *testing.T) {
	s := testServerWithMachine(MachineConfig{
		MachineID: "m-ttl",
	})
	callCount := 0
	s.SecretFetcher = SecretFetcherFunc(func(machineID string) (map[string]string, error) {
		callCount++
		return map[string]string{"channel-telegram-botToken": "token-v" + fmt.Sprintf("%d", callCount)}, nil
	})

	req := httptest.NewRequest("GET", "/v1/secrets", nil)
	w := httptest.NewRecorder()
	s.handleSecrets(w, req)
	if callCount != 1 {
		t.Fatalf("expected 1 fetch call, got %d", callCount)
	}

	w = httptest.NewRecorder()
	s.handleSecrets(w, httptest.NewRequest("GET", "/v1/secrets", nil))
	if callCount != 1 {
		t.Fatalf("expected still 1 fetch call (cached), got %d", callCount)
	}

	s.mu.Lock()
	cfg := s.configs[testVMIP]
	cfg.SecretCache.FetchedAt = time.Now().Add(-2 * SecretCacheTTL)
	s.configs[testVMIP] = cfg
	s.mu.Unlock()

	w = httptest.NewRecorder()
	s.handleSecrets(w, httptest.NewRequest("GET", "/v1/secrets", nil))
	if callCount != 2 {
		t.Fatalf("expected 2 fetch calls after expiry, got %d", callCount)
	}

	var secrets map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &secrets)
	if secrets["channel-telegram-botToken"] != "token-v2" {
		t.Errorf("expected refreshed token, got %q", secrets["channel-telegram-botToken"])
	}
}

func TestHandleSecrets_FreshBypassesCache(t *testing.T) {
	s := testServerWithMachine(MachineConfig{
		MachineID: "m-fresh",
	})
	callCount := 0
	s.SecretFetcher = SecretFetcherFunc(func(machineID string) (map[string]string, error) {
		callCount++
		return map[string]string{"HERMES_CONFIG_YAML": fmt.Sprintf("model:\n  default: model-v%d\n", callCount)}, nil
	})

	w := httptest.NewRecorder()
	s.handleSecrets(w, httptest.NewRequest("GET", "/v1/secrets", nil))
	if callCount != 1 {
		t.Fatalf("expected 1 fetch call, got %d", callCount)
	}

	w = httptest.NewRecorder()
	s.handleSecrets(w, httptest.NewRequest("GET", "/v1/secrets", nil))
	if callCount != 1 {
		t.Fatalf("expected cached fetch count to stay 1, got %d", callCount)
	}

	w = httptest.NewRecorder()
	s.handleSecrets(w, httptest.NewRequest("GET", "/v1/secrets?fresh=1", nil))
	if callCount != 2 {
		t.Fatalf("expected fresh request to force fetch, got %d calls", callCount)
	}
	var secrets map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &secrets)
	if got := secrets["HERMES_CONFIG_YAML"]; !strings.Contains(got, "model-v2") {
		t.Fatalf("fresh HERMES_CONFIG_YAML = %q, want model-v2", got)
	}
}

func TestHandleSecrets_PullFetchError(t *testing.T) {
	s := testServerWithMachine(MachineConfig{
		MachineID: "m-err",
		Secrets:   map[string]string{"platform-key": "val"},
	})
	s.SecretFetcher = SecretFetcherFunc(func(machineID string) (map[string]string, error) {
		return nil, fmt.Errorf("backend unreachable")
	})

	req := httptest.NewRequest("GET", "/v1/secrets", nil)
	w := httptest.NewRecorder()
	s.handleSecrets(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var secrets map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &secrets)
	if secrets["platform-key"] != "val" {
		t.Errorf("platform-key = %q, want val", secrets["platform-key"])
	}
}

func TestHandleSecrets_NoFetcherFallsBack(t *testing.T) {
	s := testServerWithMachine(MachineConfig{
		MachineID: "m-nofetcher",
		Secrets:   map[string]string{"key": "val"},
	})

	req := httptest.NewRequest("GET", "/v1/secrets", nil)
	w := httptest.NewRecorder()
	s.handleSecrets(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// --- ReplaceMachineLLMKeys tests ---

func TestReplaceMachineLLMKeys_RemovesAbsentKeys(t *testing.T) {
	s := New("169.254.169.253", 0)
	vmIP := "10.0.0.1"
	s.RegisterMachine(vmIP, MachineConfig{
		MachineID:    "m-replace-1",
		GatewayToken: "gw-tok",
		LLMKeys: map[string]CredentialEntry{
			"anthropic": {Value: "sk-ant-xxx", CredentialType: "api_key"},
			"openai":    {Value: "sk-openai-xxx", CredentialType: "api_key"},
		},
	})

	// Replace with only anthropic — openai should be gone
	s.ReplaceMachineLLMKeys(vmIP, map[string]CredentialEntry{
		"anthropic": {Value: "sk-ant-new", CredentialType: "api_key"},
	})

	cfg, ok := s.GetConfig(vmIP)
	if !ok {
		t.Fatal("VM should still be registered")
	}
	if len(cfg.LLMKeys) != 1 {
		t.Fatalf("expected 1 key after replace, got %d", len(cfg.LLMKeys))
	}
	if cfg.LLMKeys["anthropic"].Value != "sk-ant-new" {
		t.Errorf("anthropic value = %q, want sk-ant-new", cfg.LLMKeys["anthropic"].Value)
	}
	if _, exists := cfg.LLMKeys["openai"]; exists {
		t.Error("openai key should have been removed by replace")
	}
}

func TestReplaceMachineLLMKeys_EmptyMapClearsAll(t *testing.T) {
	s := New("169.254.169.253", 0)
	vmIP := "10.0.0.2"
	s.RegisterMachine(vmIP, MachineConfig{
		MachineID: "m-replace-empty",
		LLMKeys: map[string]CredentialEntry{
			"anthropic": {Value: "sk-ant-xxx", CredentialType: "api_key"},
			"openai":    {Value: "sk-openai-xxx", CredentialType: "api_key"},
		},
	})

	s.ReplaceMachineLLMKeys(vmIP, map[string]CredentialEntry{})

	cfg, ok := s.GetConfig(vmIP)
	if !ok {
		t.Fatal("VM should still be registered")
	}
	if len(cfg.LLMKeys) != 0 {
		t.Fatalf("expected 0 keys after empty replace, got %d", len(cfg.LLMKeys))
	}
}

func TestReplaceMachineLLMKeys_UnknownVMNoOp(t *testing.T) {
	s := New("169.254.169.253", 0)

	// Should not panic, just log a warning
	s.ReplaceMachineLLMKeys("10.0.0.99", map[string]CredentialEntry{
		"anthropic": {Value: "sk-ant-xxx", CredentialType: "api_key"},
	})

	_, ok := s.GetConfig("10.0.0.99")
	if ok {
		t.Error("unknown VM should not be registered after replace")
	}
}

func TestReplaceMachineLLMKeys_DoesNotAffectOtherFields(t *testing.T) {
	s := New("169.254.169.253", 0)
	vmIP := "10.0.0.3"
	s.RegisterMachine(vmIP, MachineConfig{
		MachineID:    "m-replace-fields",
		MachineName:  "my-bot",
		GatewayToken: "gw-tok-123",
		Secrets:      map[string]string{"OPIK_API_KEY": "opik-key"},
		SigningKey:   "sign-key-abc",
		LLMKeys: map[string]CredentialEntry{
			"anthropic": {Value: "sk-ant-old", CredentialType: "api_key"},
		},
	})

	// Replace LLM keys
	s.ReplaceMachineLLMKeys(vmIP, map[string]CredentialEntry{
		"google": {Value: "goog-key", CredentialType: "api_key"},
	})

	cfg, ok := s.GetConfig(vmIP)
	if !ok {
		t.Fatal("VM should still be registered")
	}

	// LLM keys should be replaced
	if len(cfg.LLMKeys) != 1 {
		t.Fatalf("expected 1 LLM key, got %d", len(cfg.LLMKeys))
	}
	if cfg.LLMKeys["google"].Value != "goog-key" {
		t.Errorf("google value = %q, want goog-key", cfg.LLMKeys["google"].Value)
	}

	// Other fields should be untouched
	if cfg.MachineID != "m-replace-fields" {
		t.Errorf("MachineID = %q, want m-replace-fields", cfg.MachineID)
	}
	if cfg.MachineName != "my-bot" {
		t.Errorf("MachineName = %q, want my-bot", cfg.MachineName)
	}
	if cfg.GatewayToken != "gw-tok-123" {
		t.Errorf("GatewayToken = %q, want gw-tok-123", cfg.GatewayToken)
	}
	if cfg.Secrets["OPIK_API_KEY"] != "opik-key" {
		t.Errorf("Secrets[OPIK_API_KEY] = %q, want opik-key", cfg.Secrets["OPIK_API_KEY"])
	}
	if cfg.SigningKey != "sign-key-abc" {
		t.Errorf("SigningKey = %q, want sign-key-abc", cfg.SigningKey)
	}
}

// Verify that UpdateMachineLLMKeys merges (for contrast with Replace behavior)
func TestUpdateMachineLLMKeys_MergesIntoExisting(t *testing.T) {
	s := New("169.254.169.253", 0)
	vmIP := "10.0.0.4"
	s.RegisterMachine(vmIP, MachineConfig{
		MachineID: "m-update-merge",
		LLMKeys: map[string]CredentialEntry{
			"anthropic": {Value: "sk-ant-old", CredentialType: "api_key"},
			"openai":    {Value: "sk-openai-xxx", CredentialType: "api_key"},
		},
	})

	// Update with just anthropic — openai should still be there (merge behavior)
	s.UpdateMachineLLMKeys(vmIP, map[string]CredentialEntry{
		"anthropic": {Value: "sk-ant-new", CredentialType: "api_key"},
	})

	cfg, ok := s.GetConfig(vmIP)
	if !ok {
		t.Fatal("VM should still be registered")
	}
	if len(cfg.LLMKeys) != 2 {
		t.Fatalf("expected 2 keys after merge update, got %d", len(cfg.LLMKeys))
	}
	if cfg.LLMKeys["anthropic"].Value != "sk-ant-new" {
		t.Errorf("anthropic value = %q, want sk-ant-new", cfg.LLMKeys["anthropic"].Value)
	}
	if cfg.LLMKeys["openai"].Value != "sk-openai-xxx" {
		t.Errorf("openai should be preserved in merge update, got %q", cfg.LLMKeys["openai"].Value)
	}
}
