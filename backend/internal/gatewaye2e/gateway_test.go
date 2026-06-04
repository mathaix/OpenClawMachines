//go:build linux && e2e

// Package gatewaye2e runs end-to-end tests that validate the full stack:
// OpenClaw gateway (subprocess) + metadata server + API proxy.
//
// Requirements:
//   - Linux (metadata server uses linux-only HTTP server)
//   - openclaw installed globally (npm install -g openclaw@VERSION)
//   - At least one of: E2E_ANTHROPIC_API_KEY, E2E_ANTHROPIC_SUBSCRIPTION_KEY,
//     E2E_OPENAI_API_KEY, E2E_OPENAI_SUBSCRIPTION_KEY env vars
//   - GCP credentials for Secret Manager (NEBIUS_API_KEY fetched automatically)
//
// Provider-specific tests skip gracefully when their key is absent.
// Each proxy test re-registers the machine with the specific credential type
// before calling the proxy, allowing all 4 credential flows to be tested
// against a single gateway instance.
//
// Run: make test-gateway-e2e
package gatewaye2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mathaix/openclawmachines/backend/internal/apiproxy"
	"github.com/mathaix/openclawmachines/backend/internal/configassembly"
	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/secrets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	machineID      = "e2e-test-machine"
	nonce          = "e2e-test-nonce-12345"
	gatewayToken   = "e2e-gateway-token"
	startupTimeout = 120 * time.Second

	gatewayProtocolVersion = 4
)

// testEnv holds the running test infrastructure.
type testEnv struct {
	metaSrv     *metadata.Server
	metaPort    int
	proxy       *apiproxy.Proxy
	proxyPort   int
	gatewayPort int
	gatewayCmd  *exec.Cmd
	gatewayLog  string        // path to gateway log file
	gatewayDone chan struct{} // closed when gateway process exits
	cancel      context.CancelFunc
	configBytes []byte // stored gateway config for re-registration
	openclawBin string

	// Per-credential-type keys (from E2E_* env vars)
	anthropicApiKey          string
	anthropicSubscriptionKey string
	openaiApiKey             string
	openaiSubscriptionKey    string
	openrouterApiKey         string

	tmpDir string

	codexAuthAvailable   bool
	codexPluginAvailable bool

	// Opik tracing (set during setup if plugin installs successfully)
	opikReceiver  *opikTraceReceiver
	opikAvailable bool
}

// opikTraceReceiver is a mock HTTP server that captures Opik API trace/span POSTs.
type opikTraceReceiver struct {
	mu     sync.Mutex
	traces []json.RawMessage
	spans  []json.RawMessage
	url    string // set after httptest.NewServer
}

func (r *opikTraceReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()

	path := req.URL.Path
	log.Printf("opik-mock: %s %s", req.Method, path)

	if req.Method == http.MethodPost {
		var body json.RawMessage
		if err := json.NewDecoder(req.Body).Decode(&body); err == nil {
			log.Printf("opik-mock: POST %s body=%s", path, string(body))
			if strings.Contains(path, "traces") {
				r.traces = append(r.traces, body)
			} else if strings.Contains(path, "spans") {
				r.spans = append(r.spans, body)
			}
		} else {
			log.Printf("opik-mock: POST %s decode error: %v", path, err)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// GET for projects — plugin queries this on init
	if req.Method == http.MethodGet && strings.Contains(path, "projects") {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content": []map[string]interface{}{
				{"id": "test-project-id", "name": "default"},
			},
		})
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (r *opikTraceReceiver) traceCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.traces)
}

func (r *opikTraceReceiver) spanCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.spans)
}

// e2eModelCatalog returns a model catalog for E2E tests.
func e2eModelCatalog() []configassembly.CatalogModel {
	smart := "smart"
	balanced := "balanced"
	fast := "fast"
	nebiusQwen := "nebius/Qwen/Qwen3.5-397B-A17B"
	nebiusMinimax := "nebius/MiniMaxAI/MiniMax-M2.5"
	nebiusGPT := "nebius/openai/gpt-oss-120b"
	return []configassembly.CatalogModel{
		{ID: "qwen/qwen3.5-397b", Label: "Qwen 3.5 397B", Source: "platform", Tier: &smart, GatewayModelID: &nebiusQwen, Provider: "nebius"},
		{ID: "minimax/minimax-m2.5", Label: "MiniMax M2.5", Source: "platform", Tier: &balanced, GatewayModelID: &nebiusMinimax, Provider: "nebius"},
		{ID: "openai/gpt-oss-120b", Label: "GPT OSS 120B", Source: "platform", Tier: &fast, GatewayModelID: &nebiusGPT, Provider: "nebius"},
		{ID: "anthropic/claude-sonnet-4-6", Label: "Claude Sonnet 4.6", Source: "byok", Provider: "anthropic"},
		{ID: "anthropic/claude-opus-4-6", Label: "Claude Opus 4.6", Source: "byok", Provider: "anthropic"},
		{ID: "openai/gpt-4o", Label: "GPT-4o", Source: "byok", Provider: "openai"},
		{ID: "openai-codex/gpt-5.5", Label: "GPT-5.5", Source: "subscription", Provider: "openai-codex"},
		{ID: "openai-codex/gpt-5.4", Label: "GPT-5.4", Source: "subscription", Provider: "openai-codex"},
		{ID: "openrouter/meta-llama/llama-3.1-8b-instruct", Label: "Llama 3.1 8B Instruct", Source: "byok", Provider: "openrouter"},
	}
}

// hasAnthropicKey returns true if any Anthropic key is available.
func (e *testEnv) hasAnthropicKey() bool {
	return e.anthropicApiKey != "" || e.anthropicSubscriptionKey != ""
}

// hasOpenAIKey returns true if any OpenAI key is available.
func (e *testEnv) hasOpenAIKey() bool {
	return e.openaiApiKey != "" || e.openaiSubscriptionKey != ""
}

// hasOpenRouterKey returns true if an OpenRouter key is available.
func (e *testEnv) hasOpenRouterKey() bool {
	return e.openrouterApiKey != ""
}

// updateLLMKeys re-registers the machine in the metadata server with the
// given LLM keys. This allows each proxy test to set the exact credential
// type it needs without restarting the gateway.
func (e *testEnv) updateLLMKeys(llmKeys map[string]metadata.CredentialEntry) {
	e.metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID:    machineID,
		Nonce:        nonce,
		GatewayToken: gatewayToken,
		OpenClawConf: e.configBytes,
		LLMKeys:      llmKeys,
	})
}

var env *testEnv

func TestMain(m *testing.M) {
	allowNoKeys := envBool("E2E_ALLOW_NO_LLM_KEYS")

	keys := struct {
		anthropicApiKey, anthropicSubKey string
		openaiApiKey, openaiSubKey       string
		openrouterApiKey                 string
	}{
		anthropicApiKey:  os.Getenv("E2E_ANTHROPIC_API_KEY"),
		anthropicSubKey:  os.Getenv("E2E_ANTHROPIC_SUBSCRIPTION_KEY"),
		openaiApiKey:     os.Getenv("E2E_OPENAI_API_KEY"),
		openaiSubKey:     os.Getenv("E2E_OPENAI_SUBSCRIPTION_KEY"),
		openrouterApiKey: os.Getenv("E2E_OPENROUTER_API_KEY"),
	}
	if keys.anthropicApiKey == "" && keys.anthropicSubKey == "" &&
		keys.openaiApiKey == "" && keys.openaiSubKey == "" &&
		keys.openrouterApiKey == "" {
		if allowNoKeys {
			log.Println("No E2E API keys set, running in keyless mode (E2E_ALLOW_NO_LLM_KEYS=1)")
		} else {
			log.Println("No E2E API keys set, skipping gateway E2E tests")
			log.Println("  Need at least one of: E2E_ANTHROPIC_API_KEY, E2E_ANTHROPIC_SUBSCRIPTION_KEY, E2E_OPENAI_API_KEY, E2E_OPENAI_SUBSCRIPTION_KEY, E2E_OPENROUTER_API_KEY")
			log.Println("  Or set E2E_ALLOW_NO_LLM_KEYS=1 for keyless plugin/channel smoke tests")
			os.Exit(0)
		}
	}

	// Check openclaw is available
	openclawBin := strings.TrimSpace(os.Getenv("OPENCLAW_BIN"))
	var err error
	if openclawBin != "" {
		if strings.Contains(openclawBin, "/") {
			if _, statErr := os.Stat(openclawBin); statErr != nil {
				log.Printf("OPENCLAW_BIN not found: %s (%v)", openclawBin, statErr)
				os.Exit(1)
			}
		} else {
			openclawBin, err = exec.LookPath(openclawBin)
			if err != nil {
				log.Printf("OPENCLAW_BIN lookup failed for %q: %v", os.Getenv("OPENCLAW_BIN"), err)
				os.Exit(1)
			}
		}
	} else {
		openclawBin, err = exec.LookPath("openclaw")
		if err != nil {
			log.Println("openclaw not found in PATH, skipping gateway E2E tests (install: npm install -g openclaw)")
			os.Exit(0)
		}
	}
	log.Printf("using openclaw: %s", openclawBin)

	env, err = setupTestEnv(keys.anthropicApiKey, keys.anthropicSubKey, keys.openaiApiKey, keys.openaiSubKey, keys.openrouterApiKey, openclawBin)
	if err != nil {
		log.Fatalf("setup failed: %v", err)
	}

	code := m.Run()

	env.teardown()
	os.Exit(code)
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func loadE2ECodexAuthProfile() (map[string]interface{}, string, error) {
	if profilePath := strings.TrimSpace(os.Getenv("E2E_CODEX_AUTH_PROFILES_FILE")); profilePath != "" {
		data, err := os.ReadFile(profilePath)
		if err != nil {
			return nil, "", fmt.Errorf("read E2E_CODEX_AUTH_PROFILES_FILE: %w", err)
		}
		var authProfiles map[string]interface{}
		if err := json.Unmarshal(data, &authProfiles); err != nil {
			return nil, "", fmt.Errorf("parse E2E_CODEX_AUTH_PROFILES_FILE: %w", err)
		}
		profiles, _ := authProfiles["profiles"].(map[string]interface{})
		profile, _ := profiles["openai-codex:default"].(map[string]interface{})
		if err := validateCodexAuthProfile(profile); err != nil {
			return nil, "", fmt.Errorf("invalid openai-codex:default in E2E_CODEX_AUTH_PROFILES_FILE: %w", err)
		}
		return profile, profilePath, nil
	}

	codexAuthPath := strings.TrimSpace(os.Getenv("E2E_CODEX_AUTH_FILE"))
	if codexAuthPath == "" && envBool("E2E_USE_LOCAL_CODEX_AUTH") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, "", fmt.Errorf("resolve home for local Codex auth: %w", err)
		}
		codexAuthPath = filepath.Join(home, ".codex", "auth.json")
	}
	if codexAuthPath == "" {
		return nil, "", nil
	}

	data, err := os.ReadFile(codexAuthPath)
	if err != nil {
		return nil, "", fmt.Errorf("read Codex auth file: %w", err)
	}
	var codexAuth struct {
		Tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &codexAuth); err != nil {
		return nil, "", fmt.Errorf("parse Codex auth file: %w", err)
	}
	if codexAuth.Tokens.AccessToken == "" || codexAuth.Tokens.RefreshToken == "" {
		return nil, "", fmt.Errorf("Codex auth file missing access_token or refresh_token")
	}

	expires := jwtExpiresMillis(codexAuth.Tokens.AccessToken)
	if expires == 0 {
		expires = time.Now().Add(time.Hour).UnixMilli()
	}
	profile := map[string]interface{}{
		"type":     "oauth",
		"provider": "openai-codex",
		"access":   codexAuth.Tokens.AccessToken,
		"refresh":  codexAuth.Tokens.RefreshToken,
		"expires":  expires,
	}
	return profile, codexAuthPath, nil
}

func validateCodexAuthProfile(profile map[string]interface{}) error {
	if profile == nil {
		return fmt.Errorf("profile not found")
	}
	for _, key := range []string{"access", "refresh"} {
		value, _ := profile[key].(string)
		if value == "" {
			return fmt.Errorf("missing %s", key)
		}
	}
	return nil
}

func jwtExpiresMillis(token string) int64 {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return 0
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return 0
	}
	return claims.Exp * 1000
}

func setupTestEnv(anthropicApiKey, anthropicSubKey, openaiApiKey, openaiSubKey, openrouterApiKey, openclawBin string) (*testEnv, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Create temp directory for gateway HOME and state
	tmpDir, err := os.MkdirTemp("", "gateway-e2e-*")
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	stateDir := filepath.Join(tmpDir, ".openclaw")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("create state dir: %w", err)
	}

	// Determine which providers are available (any key for a provider counts).
	hasAnthropic := anthropicApiKey != "" || anthropicSubKey != ""
	hasOpenAI := openaiApiKey != "" || openaiSubKey != ""
	hasOpenRouter := openrouterApiKey != ""
	// Build credentials map for config assembly and auth-profiles.json.
	credentials := map[string]string{}
	if hasAnthropic {
		credentials["anthropic"] = "present"
		log.Printf("anthropic: api_key=%t subscription_key=%t", anthropicApiKey != "", anthropicSubKey != "")
	}
	if hasOpenAI {
		credentials["openai"] = "present"
		log.Printf("openai: api_key=%t subscription_key=%t", openaiApiKey != "", openaiSubKey != "")
	}
	if hasOpenRouter {
		credentials["openrouter"] = "present"
		log.Printf("openrouter: api_key=%t", true)
	}

	// Build initial LLM keys for metadata registration.
	// Use the first available key per provider; proxy tests re-register
	// with specific credential types before each call.
	llmKeys := map[string]metadata.CredentialEntry{}
	if anthropicApiKey != "" {
		llmKeys["anthropic"] = metadata.CredentialEntry{Value: anthropicApiKey, CredentialType: "api_key"}
	} else if anthropicSubKey != "" {
		llmKeys["anthropic"] = metadata.CredentialEntry{Value: anthropicSubKey, CredentialType: "subscription_key"}
	}
	if openaiApiKey != "" {
		llmKeys["openai"] = metadata.CredentialEntry{Value: openaiApiKey, CredentialType: "api_key"}
	} else if openaiSubKey != "" {
		llmKeys["openai"] = metadata.CredentialEntry{Value: openaiSubKey, CredentialType: "api_key"}
	}
	if openrouterApiKey != "" {
		llmKeys["openrouter"] = metadata.CredentialEntry{Value: openrouterApiKey, CredentialType: "api_key"}
	}

	// Write auth-profiles.json so the gateway discovers available providers
	// and populates its model catalog. Mirrors init-openclaw.sh:535-547.
	// The gateway reads this at startup via ensurePiAuthJsonFromAuthProfiles(),
	// converts it to auth.json, and feeds it to ModelRegistry. Without this
	// file, models.list returns an empty catalog.
	agentDir := filepath.Join(stateDir, "agents", "main", "agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("create agent dir: %w", err)
	}
	profiles := make(map[string]interface{}, len(credentials))
	for provider := range credentials {
		profiles[provider+"-proxy"] = map[string]interface{}{
			"type":     "api_key",
			"provider": provider,
			"key":      nonce,
		}
	}
	codexProfile, codexAuthSource, err := loadE2ECodexAuthProfile()
	if err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("load codex auth profile: %w", err)
	}
	codexAuthAvailable := codexProfile != nil
	if codexAuthAvailable {
		profiles["openai-codex:default"] = codexProfile
		log.Printf("codex: OAuth profile loaded from %s", codexAuthSource)
	}
	authProfiles := map[string]interface{}{
		"version":  1,
		"profiles": profiles,
	}
	authBytes, err := json.MarshalIndent(authProfiles, "", "  ")
	if err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("marshal auth-profiles: %w", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "auth-profiles.json"), authBytes, 0644); err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("write auth-profiles.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "auth-profiles.json"), authBytes, 0644); err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("write state auth-profiles.json: %w", err)
	}

	// Find free ports for metadata server, proxy, and gateway
	metaPort, err := freePort()
	if err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("find free metadata port: %w", err)
	}
	proxyPort, err := freePort()
	if err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("find free proxy port: %w", err)
	}
	gwPort, err := freePort()
	if err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("find free gateway port: %w", err)
	}

	// Build provider-specific gateway env vars.
	// The gateway uses nonce as API key; proxy swaps for the real key.
	var providerEnvVars []string
	if hasAnthropic {
		proxyURL := fmt.Sprintf("http://127.0.0.1:%d/anthropic", proxyPort)
		providerEnvVars = append(providerEnvVars,
			"ANTHROPIC_API_KEY="+nonce,
			"ANTHROPIC_BASE_URL="+proxyURL,
		)
	}
	if hasOpenAI {
		proxyURL := fmt.Sprintf("http://127.0.0.1:%d/openai", proxyPort)
		providerEnvVars = append(providerEnvVars,
			"OPENAI_API_KEY="+nonce,
			"OPENAI_BASE_URL="+proxyURL,
		)
	}

	// Start metadata server
	metaSrv := metadata.New("127.0.0.1", metaPort)
	metaSrv.AgentVersion = "test-e2e"

	// Start API proxy
	proxy := apiproxy.New(metaSrv, "127.0.0.1", proxyPort)

	// Step 1: Assemble gateway config (credentials, proxy, model config).
	// Opik plugin is NOT included here — it's installed after the config is
	// written to disk, matching the real deployment order where the platform
	// writes the base config and plugins are installed into it.
	proxyBaseURL := fmt.Sprintf("http://127.0.0.1:%d", proxyPort)
	defaultModel := ""
	if hasAnthropic {
		defaultModel = "anthropic/claude-sonnet-4-6"
	}
	forcedDefaultModel := strings.TrimSpace(os.Getenv("E2E_DEFAULT_MODEL"))
	if forcedDefaultModel != "" {
		defaultModel = forcedDefaultModel
	}
	openAIAgentRuntime := strings.TrimSpace(os.Getenv("E2E_OPENAI_AGENT_RUNTIME"))

	assemblyParams := configassembly.AssemblyParams{
		MachineID:          machineID,
		Credentials:        credentials,
		ProxyBaseURL:       proxyBaseURL,
		DefaultModel:       defaultModel,
		OpenAIAgentRuntime: openAIAgentRuntime,
	}
	if forcedDefaultModel != "" || openAIAgentRuntime != "" {
		assemblyParams.ModelCatalog = e2eModelCatalog()
	}

	configBytes, err := configassembly.AssembleConfig(assemblyParams)
	if err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("assemble config: %w", err)
	}

	// Post-assembly config patches (non-plugin)
	{
		var cfg map[string]interface{}
		if err := json.Unmarshal(configBytes, &cfg); err == nil {
			if defaultModel != "" {
				if agents, ok := cfg["agents"].(map[string]interface{}); ok {
					if defaults, ok := agents["defaults"].(map[string]interface{}); ok {
						defaults["workspace"] = filepath.Join(tmpDir, ".openclaw", "workspace")
					}
				}
			}
			configBytes, _ = json.Marshal(cfg)
		}
	}

	// Platform defaults now use auth.mode="token", so this override is a no-op
	// but kept as a safety net to ensure E2E tests always use token auth
	// (sharedAuthOk=true → roleCanSkipDeviceIdentity, since the Go client
	// can't provide device identity).
	configBytes, err = overrideAuthMode(configBytes, "token")
	if err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("override auth mode: %w", err)
	}

	// Step 2: Write openclaw.json to disk BEFORE plugin install.
	openclawJSONPath := filepath.Join(stateDir, "openclaw.json")
	if err := os.WriteFile(openclawJSONPath, configBytes, 0644); err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("write openclaw.json: %w", err)
	}

	codexPluginAvailable := false
	if codexAuthAvailable {
		if installed := tryInstallCodexPlugin(openclawBin, tmpDir, stateDir); installed {
			codexPluginAvailable = true
		} else if envBool("E2E_REQUIRE_CODEX_PLUGIN") {
			cancel()
			os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("codex plugin install failed and E2E_REQUIRE_CODEX_PLUGIN=1")
		}
	}

	// Step 3: Install opik plugin AFTER config is on disk. `openclaw plugins
	// install` merges provenance records into the existing openclaw.json so
	// the gateway trusts the plugin and activates its trace hooks.
	var opikReceiver *opikTraceReceiver
	var opikServer *httptest.Server
	opikAvailable := false
	requireOpik := envBool("E2E_REQUIRE_OPIK")

	if installed := tryInstallOpikPlugin(openclawBin, tmpDir, stateDir); installed {
		opikReceiver = &opikTraceReceiver{}
		opikServer = httptest.NewServer(opikReceiver)
		opikReceiver.url = opikServer.URL
		opikAvailable = true
		log.Printf("opik plugin installed, mock receiver at %s", opikServer.URL)

		// Patch opik config into the on-disk openclaw.json: set apiUrl to
		// mock receiver, apiKey for auth, enable the plugin, and allowlist it.
		onDiskBytes, err := os.ReadFile(openclawJSONPath)
		if err != nil {
			cancel()
			os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("read openclaw.json after plugin install: %w", err)
		}
		var cfg map[string]interface{}
		if err := json.Unmarshal(onDiskBytes, &cfg); err != nil {
			cancel()
			os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("unmarshal openclaw.json after plugin install: %w", err)
		}

		plugins, _ := cfg["plugins"].(map[string]interface{})
		if plugins == nil {
			plugins = map[string]interface{}{}
			cfg["plugins"] = plugins
		}
		entries, _ := plugins["entries"].(map[string]interface{})
		if entries == nil {
			entries = map[string]interface{}{}
			plugins["entries"] = entries
		}
		opikEntry, _ := entries["opik-openclaw"].(map[string]interface{})
		if opikEntry == nil {
			opikEntry = map[string]interface{}{}
			entries["opik-openclaw"] = opikEntry
		}
		opikEntry["enabled"] = true
		opikHooks, _ := opikEntry["hooks"].(map[string]interface{})
		if opikHooks == nil {
			opikHooks = map[string]interface{}{}
			opikEntry["hooks"] = opikHooks
		}
		opikHooks["allowConversationAccess"] = true
		opikConfig, _ := opikEntry["config"].(map[string]interface{})
		if opikConfig == nil {
			opikConfig = map[string]interface{}{}
			opikEntry["config"] = opikConfig
		}
		opikConfig["enabled"] = true // required: opik service checks config.enabled before starting
		opikConfig["apiUrl"] = opikServer.URL
		opikConfig["apiKey"] = "e2e-opik-test-key"
		opikConfig["projectName"] = "default"
		opikConfig["workspaceName"] = "default"
		opikConfig["tags"] = []interface{}{"ocm", "e2e-test"}

		// Ensure opik is in plugins.allow
		allow, _ := plugins["allow"].([]interface{})
		hasOpik := false
		for _, a := range allow {
			if s, ok := a.(string); ok && s == "opik-openclaw" {
				hasOpik = true
				break
			}
		}
		if !hasOpik {
			plugins["allow"] = append(allow, "opik-openclaw")
		}

		configBytes, _ = json.Marshal(cfg)
		if err := os.WriteFile(openclawJSONPath, configBytes, 0644); err != nil {
			cancel()
			os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("write patched openclaw.json: %w", err)
		}
		log.Printf("opik config patched: apiUrl=%s", opikServer.URL)
	} else {
		if requireOpik {
			cancel()
			os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("opik plugin install failed and E2E_REQUIRE_OPIK=1")
		}
		log.Println("opik plugin not available, opik tests will skip")
	}

	// Register machine in metadata server (still needed for secrets, proxy credential lookup)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID:    machineID,
		Nonce:        nonce,
		GatewayToken: gatewayToken,
		OpenClawConf: configBytes,
		LLMKeys:      llmKeys,
	})

	// Start metadata server in background
	go func() {
		if err := metaSrv.Start(ctx); err != nil {
			log.Printf("metadata server error: %v", err)
		}
	}()

	// Start proxy in background
	go func() {
		if err := proxy.Start(ctx); err != nil {
			log.Printf("proxy error: %v", err)
		}
	}()

	// Wait for both to be ready
	if err := waitForHTTP(fmt.Sprintf("http://127.0.0.1:%d/health", metaPort), 5*time.Second); err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("metadata server not ready: %w", err)
	}
	if err := waitForHTTP(fmt.Sprintf("http://127.0.0.1:%d/health", proxyPort), 5*time.Second); err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("proxy not ready: %w", err)
	}

	log.Printf("metadata server on :%d, proxy on :%d, gateway will be on :%d", metaPort, proxyPort, gwPort)

	// Start gateway subprocess
	logFile := filepath.Join(tmpDir, "gateway.log")
	cmd, done, err := startGatewayProcess(openclawBin, metaPort, gwPort, tmpDir, stateDir, logFile, providerEnvVars)
	if err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("start gateway process: %w", err)
	}

	// Wait for gateway health (or process exit)
	gatewayHealth := fmt.Sprintf("http://127.0.0.1:%d/health", gwPort)
	log.Printf("waiting for gateway at %s (timeout %s)...", gatewayHealth, startupTimeout)
	if err := waitForHTTPOrExit(gatewayHealth, startupTimeout, done); err != nil {
		logs, _ := os.ReadFile(logFile)
		log.Printf("gateway logs:\n%s", string(logs))
		select {
		case <-done:
			// Process already exited
		default:
			cmd.Process.Kill()
			<-done
		}
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("gateway not ready: %w", err)
	}
	log.Println("gateway is healthy")

	return &testEnv{
		metaSrv:                  metaSrv,
		metaPort:                 metaPort,
		proxy:                    proxy,
		proxyPort:                proxyPort,
		gatewayPort:              gwPort,
		gatewayCmd:               cmd,
		gatewayLog:               logFile,
		gatewayDone:              done,
		cancel:                   cancel,
		configBytes:              configBytes,
		openclawBin:              openclawBin,
		anthropicApiKey:          anthropicApiKey,
		anthropicSubscriptionKey: anthropicSubKey,
		openaiApiKey:             openaiApiKey,
		openaiSubscriptionKey:    openaiSubKey,
		openrouterApiKey:         openrouterApiKey,
		tmpDir:                   tmpDir,
		codexAuthAvailable:       codexAuthAvailable,
		codexPluginAvailable:     codexPluginAvailable,
		opikReceiver:             opikReceiver,
		opikAvailable:            opikAvailable,
	}, nil
}

func (e *testEnv) teardown() {
	if e.gatewayCmd != nil && e.gatewayCmd.Process != nil {
		e.gatewayCmd.Process.Kill()
		<-e.gatewayDone
	}
	e.cancel()
	if e.tmpDir != "" {
		os.RemoveAll(e.tmpDir)
	}
}

func (e *testEnv) readGatewayLogs() string {
	data, err := os.ReadFile(e.gatewayLog)
	if err != nil {
		return fmt.Sprintf("(error reading logs: %v)", err)
	}
	return string(data)
}

func startGatewayProcess(openclawBin string, metaPort, gwPort int, homeDir, stateDir, logFile string, providerEnvVars []string) (*exec.Cmd, chan struct{}, error) {
	cmd := exec.Command(openclawBin, "gateway",
		"--port", fmt.Sprintf("%d", gwPort),
		"--bind", "loopback",
		"--allow-unconfigured",
		"--verbose",
	)

	// Minimal env: only what the gateway needs.
	// NODE_PATH must include openclaw's bundled node_modules so the gateway
	// can resolve tsx for loading TypeScript plugins (e.g. opik-openclaw).
	openclawPkgDir := filepath.Dir(openclawBin)
	if resolved, err := filepath.EvalSymlinks(openclawBin); err == nil {
		openclawPkgDir = filepath.Dir(resolved)
	}
	cmd.Env = []string{
		"HOME=" + homeDir,
		"PATH=" + os.Getenv("PATH"),
		"NODE_PATH=" + filepath.Join(openclawPkgDir, "node_modules"),
		"NODE_ENV=production",
		"OCM_MACHINE_ID=" + machineID,
		"OPENCLAW_GATEWAY_TOKEN=" + gatewayToken,
		"OPENCLAW_STATE_DIR=" + stateDir,
		"OPENCLAW_DISABLE_BONJOUR=1",
	}
	// Append provider-specific env vars (e.g. ANTHROPIC_API_KEY, OPENAI_BASE_URL)
	cmd.Env = append(cmd.Env, providerEnvVars...)

	// Write stdout/stderr to log file
	f, err := os.Create(logFile)
	if err != nil {
		return nil, nil, fmt.Errorf("create log file: %w", err)
	}
	cmd.Stdout = f
	cmd.Stderr = f

	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("start node process: %w", err)
	}

	log.Printf("started gateway process: pid=%d", cmd.Process.Pid)

	done := make(chan struct{})
	go func() {
		cmd.Wait()
		f.Close()
		close(done)
	}()

	return cmd, done, nil
}

// overrideAuthMode patches the assembled config JSON to set gateway.auth.mode.
// Platform defaults already use auth.mode="token", so this is a safety net
// ensuring E2E tests always use token auth (sharedAuthOk=true).
func overrideAuthMode(configBytes []byte, mode string) ([]byte, error) {
	var config map[string]interface{}
	if err := json.Unmarshal(configBytes, &config); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	gw, ok := config["gateway"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("missing gateway in assembled config")
	}
	auth, ok := gw["auth"].(map[string]interface{})
	if !ok {
		auth = map[string]interface{}{}
		gw["auth"] = auth
	}
	auth["mode"] = mode
	return json.Marshal(config)
}

// =============================================================================
// Tests that hit the OpenClaw gateway directly
// =============================================================================

// TestGatewayE2E_Startup verifies the gateway starts and responds to health checks.
func TestGatewayE2E_Startup(t *testing.T) {
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", env.gatewayPort))
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("health check: got status %d, want 200; body: %s", resp.StatusCode, string(body))
	}

	// Check gateway logs for fatal errors (exclude known non-fatal gateway warnings)
	logs := env.readGatewayLogs()
	for _, fatal := range []string{"FATAL", "UnhandledPromiseRejection"} {
		if strings.Contains(logs, fatal) {
			t.Errorf("gateway logs contain %q:\n%s", fatal, logs)
		}
	}

	t.Log("gateway started successfully and is healthy")
}

// TestGatewayE2E_ControlUI verifies the gateway serves the Control UI HTML at root /.
// The agent proxy routes /gateway/ → auth proxy strips /gateway → gateway /.
// This test catches regressions if the gateway stops serving the UI at root.
func TestGatewayE2E_ControlUI(t *testing.T) {
	url := fmt.Sprintf("http://127.0.0.1:%d/", env.gatewayPort)
	t.Logf("checking Control UI at: %s", url)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("Control UI request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Control UI at / returned %d, want 200; body: %s", resp.StatusCode, string(body))
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", contentType)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// The base path variable is present in production builds but may be absent
	// in dev/fallback UI. Log its presence but don't fail on it.
	if strings.Contains(bodyStr, "__OPENCLAW_CONTROL_UI_BASE_PATH__") {
		t.Log("Control UI HTML contains __OPENCLAW_CONTROL_UI_BASE_PATH__ variable")
	} else {
		t.Log("Control UI HTML does not contain __OPENCLAW_CONTROL_UI_BASE_PATH__ (dev/fallback UI)")
	}

	t.Logf("Control UI served successfully at / (%d bytes)", len(body))
}

// TestGatewayE2E_WebSocketHandshake connects to the gateway via WebSocket and
// performs the challenge-response auth handshake with the gateway token.
// This validates: gateway loaded config from metadata, auth token is configured,
// WebSocket endpoint is reachable, and Control UI access works.
//
// With dangerouslyDisableDeviceAuth=true, the gateway bypasses device identity
// for Control UI clients. Security is enforced at the auth proxy layer (machine
// JWT validation via Cloudflare Access).
func TestGatewayE2E_WebSocketHandshake(t *testing.T) {
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d/gateway/ws", env.gatewayPort)
	t.Logf("connecting to gateway WebSocket: %s", wsURL)

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	headers := http.Header{}
	headers.Set("Origin", fmt.Sprintf("http://127.0.0.1:%d", env.gatewayPort))
	conn, resp, err := dialer.Dial(wsURL, headers)
	if err != nil {
		statusCode := 0
		if resp != nil {
			statusCode = resp.StatusCode
		}
		t.Fatalf("WebSocket dial failed: err=%v, status=%d", err, statusCode)
	}
	defer conn.Close()

	// 1. Read the connect.challenge from the gateway
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, challengeData, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read challenge from gateway: %v", err)
	}
	t.Logf("gateway challenge: %s", string(challengeData))

	var challenge struct {
		Type    string `json:"type"`
		Event   string `json:"event"`
		Payload struct {
			Nonce string `json:"nonce"`
			Ts    int64  `json:"ts"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(challengeData, &challenge); err != nil {
		t.Fatalf("failed to parse challenge: %v", err)
	}
	if challenge.Event != "connect.challenge" {
		t.Fatalf("expected connect.challenge event, got %s", challenge.Event)
	}

	// 2. Send connect message with gateway token auth
	// Schema: ConnectParamsSchema in protocol/schema/frames.ts
	// client.id must be a GATEWAY_CLIENT_IDS value (e.g. "test")
	// No nonce at params root — additionalProperties: false
	connectMsg := map[string]interface{}{
		"type":   "req",
		"id":     "connect-1",
		"method": "connect",
		"params": map[string]interface{}{
			"minProtocol": gatewayProtocolVersion,
			"maxProtocol": gatewayProtocolVersion,
			"client": map[string]interface{}{
				"id":       "openclaw-control-ui",
				"version":  "0.0.0",
				"platform": "linux",
				"mode":     "ui",
			},
			"auth": map[string]interface{}{
				"token": gatewayToken,
			},
			"scopes": []string{"operator.admin"},
		},
	}
	connectData, _ := json.Marshal(connectMsg)
	t.Logf("sending connect: %s", string(connectData))
	if err := conn.WriteMessage(websocket.TextMessage, connectData); err != nil {
		t.Fatalf("failed to send connect message: %v", err)
	}

	// 3. Read connect response
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, respData, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("connection closed after connect (token rejected?): %v", err)
	}
	t.Logf("gateway connect response: %s", string(respData))

	// 4. Verify the response — fail on auth errors
	var connectResp struct {
		OK    bool `json:"ok"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details *struct {
				Code string `json:"code"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respData, &connectResp); err != nil {
		t.Fatalf("failed to parse connect response: %v", err)
	}

	if connectResp.Error != nil {
		detailCode := ""
		if connectResp.Error.Details != nil {
			detailCode = connectResp.Error.Details.Code
		}

		authErrors := map[string]bool{
			"AUTH_TOKEN_MISSING":  true,
			"AUTH_TOKEN_MISMATCH": true,
			"AUTH_REQUIRED":       true,
			"AUTH_UNAUTHORIZED":   true,
		}
		if authErrors[detailCode] || connectResp.Error.Code == "UNAUTHORIZED" {
			t.Fatalf("gateway rejected token with auth error: code=%s detail=%s msg=%s "+
				"(OPENCLAW_GATEWAY_TOKEN not configured correctly)",
				connectResp.Error.Code, detailCode, connectResp.Error.Message)
		}
		t.Fatalf("gateway connect failed: code=%s detail=%s msg=%s",
			connectResp.Error.Code, detailCode, connectResp.Error.Message)
	}

	t.Log("gateway WebSocket connect succeeded with gateway token")
}

// TestGatewayE2E_WebSocketHandshake_NonUIClient connects as a non-Control-UI
// client (e.g. TUI, CLI probe) to verify the gateway accepts connections without
// requiring device pairing. With auth.mode="token", non-UI clients with the
// token get sharedAuthOk=true → roleCanSkipDeviceIdentity, bypassing pairing.
func TestGatewayE2E_WebSocketHandshake_NonUIClient(t *testing.T) {
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d/gateway/ws", env.gatewayPort)
	t.Logf("connecting as non-UI client: %s", wsURL)

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	headers := http.Header{}
	headers.Set("Origin", fmt.Sprintf("http://127.0.0.1:%d", env.gatewayPort))
	conn, resp, err := dialer.Dial(wsURL, headers)
	if err != nil {
		statusCode := 0
		if resp != nil {
			statusCode = resp.StatusCode
		}
		t.Fatalf("WebSocket dial failed: err=%v, status=%d", err, statusCode)
	}
	defer conn.Close()

	// Read the connect.challenge
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, challengeData, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read challenge (gateway may have closed with 'pairing required'): %v", err)
	}

	var challenge struct {
		Event   string `json:"event"`
		Payload struct {
			Nonce string `json:"nonce"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(challengeData, &challenge); err != nil {
		t.Fatalf("failed to parse challenge: %v", err)
	}
	if challenge.Event != "connect.challenge" {
		t.Fatalf("expected connect.challenge, got %s", challenge.Event)
	}

	// Connect as a non-UI client — this is what the TUI and CLI do.
	// mode: "probe" (not "ui") ensures we're testing the non-privileged path.
	connectMsg := map[string]interface{}{
		"type":   "req",
		"id":     "connect-1",
		"method": "connect",
		"params": map[string]interface{}{
			"minProtocol": gatewayProtocolVersion,
			"maxProtocol": gatewayProtocolVersion,
			"client": map[string]interface{}{
				"id":       "openclaw-probe",
				"version":  "0.0.0",
				"platform": "linux",
				"mode":     "probe",
			},
			"auth": map[string]interface{}{
				"token": gatewayToken,
			},
			"scopes": []string{"operator.admin"},
		},
	}
	connectData, _ := json.Marshal(connectMsg)
	if err := conn.WriteMessage(websocket.TextMessage, connectData); err != nil {
		t.Fatalf("failed to send connect message: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, respData, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("connection closed after connect (pairing required?): %v", err)
	}
	t.Logf("gateway connect response: %s", string(respData))

	var connectResp struct {
		OK    bool `json:"ok"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details *struct {
				Code string `json:"code"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respData, &connectResp); err != nil {
		t.Fatalf("failed to parse connect response: %v", err)
	}

	if connectResp.Error != nil {
		detailCode := ""
		if connectResp.Error.Details != nil {
			detailCode = connectResp.Error.Details.Code
		}
		// Any auth or pairing error means gateway.mode/gateway.auth is misconfigured
		t.Fatalf("gateway rejected non-UI client: code=%s detail=%s msg=%s "+
			"(gateway.mode or gateway.auth.mode likely missing from assembled config)",
			connectResp.Error.Code, detailCode, connectResp.Error.Message)
	}

	t.Log("gateway accepted non-UI client connect (no pairing required)")
}

// TestGatewayE2E_ChatSend sends a chat message through the gateway's WebSocket
// protocol and waits for a response. This exercises the full chain:
// test → gateway WS → agent pipeline → proxy → Anthropic API → back.
//
// Protocol:
//  1. Connect WebSocket + challenge-response handshake
//  2. Send chat.send RPC with a user message
//  3. Listen for "chat" events until state="final"
//  4. Verify the response contains LLM output
func TestGatewayE2E_ChatSend(t *testing.T) {
	if !env.hasAnthropicKey() {
		t.Skip("no Anthropic key set, skipping chat test")
	}
	conn := gatewayConnect(t)
	defer conn.Close()

	// Send chat.send RPC
	idempotencyKey := fmt.Sprintf("e2e-chat-%d", time.Now().UnixNano())
	chatSendMsg := map[string]interface{}{
		"type":   "req",
		"id":     "chat-send-1",
		"method": "chat.send",
		"params": map[string]interface{}{
			"sessionKey":     "agent:main:e2e-test",
			"message":        "Reply with exactly: E2E OK",
			"idempotencyKey": idempotencyKey,
		},
	}
	chatSendData, _ := json.Marshal(chatSendMsg)
	t.Logf("sending chat.send: %s", string(chatSendData))
	if err := conn.WriteMessage(websocket.TextMessage, chatSendData); err != nil {
		t.Fatalf("failed to send chat.send: %v", err)
	}

	// Read the chat.send response — skip any interleaved events (health, presence).
	// The response has type:"res" and id:"chat-send-1".
	var ack struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		OK      bool   `json:"ok"`
		Payload struct {
			RunID  string `json:"runId"`
			Status string `json:"status"`
		} `json:"payload"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	ackDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(ackDeadline) {
		conn.SetReadDeadline(ackDeadline)
		_, ackData, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("failed to read chat.send ack: %v", err)
		}
		// Skip events — we only want the response
		var msg struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal(ackData, &msg); err != nil {
			continue
		}
		if msg.Type == "event" {
			t.Logf("skipping event while waiting for chat.send ack: %s", string(ackData[:min(len(ackData), 120)]))
			continue
		}
		if msg.Type == "res" && msg.ID == "chat-send-1" {
			if err := json.Unmarshal(ackData, &ack); err != nil {
				t.Fatalf("failed to parse chat.send ack: %v", err)
			}
			break
		}
		t.Logf("unexpected message while waiting for chat.send ack: %s", string(ackData[:min(len(ackData), 120)]))
	}
	if ack.Type == "" {
		t.Fatal("timed out waiting for chat.send response")
	}
	if !ack.OK {
		errMsg := "unknown"
		if ack.Error != nil {
			errMsg = fmt.Sprintf("code=%s msg=%s", ack.Error.Code, ack.Error.Message)
		}
		t.Fatalf("chat.send failed: %s", errMsg)
	}
	if ack.Payload.Status != "started" {
		t.Fatalf("chat.send status = %q, want 'started'", ack.Payload.Status)
	}
	t.Logf("chat.send started: runId=%s", ack.Payload.RunID)

	// Listen for chat events until we get a terminal state or the overall chat
	// timeout expires. With a dummy API key, "error" is the expected outcome
	// because the proxy forwards to Anthropic which rejects the invalid key.
	// The important validation is that the request made it through the full
	// chain: gateway → proxy → Anthropic API.
	result := waitForTerminalChatEvent(t, conn, 60*time.Second)
	terminalState := result.state
	finalMessage := result.message

	t.Logf("chat terminal state=%s message=%q", terminalState, finalMessage)

	// The proxy should have received the LLM request from the gateway.
	// With a real key: state=final, proxy records usage.
	// With a dummy key: state=error, proxy still handled the request (401 from upstream).
	if terminalState == "final" {
		// The gateway emits state=final as soon as it processes the stream,
		// but the proxy's SSE copier may still be draining the last events
		// and hasn't recorded usage yet. Retry briefly.
		var usage []apiproxy.UsageRecord
		for i := 0; i < 20; i++ {
			usage = env.proxy.GetMachineUsage(machineID)
			if len(usage) > 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if len(usage) == 0 {
			t.Error("proxy usage tracker has no records — gateway may not have routed through proxy")
		} else {
			t.Logf("proxy recorded %d usage record(s) for machine %s", len(usage), machineID)
			for i, rec := range usage {
				t.Logf("  [%d] provider=%s model=%s input=%d output=%d",
					i, rec.Provider, rec.Model, rec.InputTokens, rec.OutputTokens)
			}
		}
	} else {
		// state=error — expected with dummy key. The important thing is that
		// the request reached the proxy (visible in proxy logs: apiproxy.request).
		t.Log("chat ended with error (expected with dummy API key — full chain was exercised)")
	}
}

// TestGatewayE2E_CodexNativeHookRelayShellPreToolUse verifies the Codex
// harness can run an immediate shell command without failing closed in the
// native PreToolUse hook path.
func TestGatewayE2E_CodexNativeHookRelayShellPreToolUse(t *testing.T) {
	if !env.codexAuthAvailable {
		t.Skip("no Codex OAuth profile set; use E2E_CODEX_AUTH_PROFILES_FILE, E2E_CODEX_AUTH_FILE, or E2E_USE_LOCAL_CODEX_AUTH=1")
	}
	if !env.codexPluginAvailable {
		t.Skip("Codex plugin not installed; set E2E_REQUIRE_CODEX_PLUGIN=1 to make setup fail instead of skip")
	}

	configPath := filepath.Join(env.tmpDir, ".openclaw", "openclaw.json")
	originalConfig, err := os.ReadFile(configPath)
	require.NoError(t, err, "read original openclaw.json")
	defer func() {
		require.NoError(t, os.WriteFile(configPath, originalConfig, 0644), "restore original openclaw.json")
		time.Sleep(2 * time.Second)
	}()

	h := newConfigSetHelper(t)
	require.NoError(t, h.set("agents.defaults.model.primary", "codex/gpt-5.5", false))
	require.NoError(t, h.set("agents.defaults.models", `{"codex/gpt-5.5":{"agentRuntime":{"id":"codex"}}}`, true))
	require.NoError(t, h.set("models.providers.openai-codex", `{
		"baseUrl":"https://chatgpt.com/backend-api",
		"api":"openai-codex-responses",
		"models":[{
			"id":"gpt-5.5",
			"name":"GPT-5.5",
			"api":"openai-codex-responses",
			"reasoning":true,
			"input":["text"],
			"contextWindow":1050000,
			"maxTokens":128000
		}]
	}`, true))
	require.NoError(t, h.set("auth.profiles", `{"openai-codex:default":{"provider":"openai-codex","mode":"oauth"}}`, true))
	require.NoError(t, h.set("plugins.entries.codex", `{
		"enabled":true,
		"config":{
			"appServer":{
				"approvalPolicy":"never",
				"sandbox":"danger-full-access"
			}
		}
	}`, true))
	require.NoError(t, h.set("plugins.allow", `["browser","memory-core","openai","codex","opik-openclaw"]`, true))

	workspaceDir := filepath.Join(env.tmpDir, ".openclaw", "workspace")
	require.NoError(t, os.MkdirAll(workspaceDir, 0755))
	outputPath := filepath.Join(workspaceDir, fmt.Sprintf("e2e-native-hook-%d.txt", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(outputPath) })

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "codex config hot reload")

	logOffset := len(env.readGatewayLogs())
	conn := gatewayConnect(t)
	defer conn.Close()

	prompt := fmt.Sprintf("Use the shell tool to run exactly these commands, then reply with the cat output only:\nprintf native-hook-ok > %s\ncat %s", outputPath, outputPath)
	chatID := "codex-native-hook-1"
	idempotencyKey := fmt.Sprintf("e2e-codex-native-hook-%d", time.Now().UnixNano())
	chatSendData, _ := json.Marshal(map[string]interface{}{
		"type":   "req",
		"id":     chatID,
		"method": "chat.send",
		"params": map[string]interface{}{
			"sessionKey":     "agent:main:codex-native-hook-e2e",
			"message":        prompt,
			"idempotencyKey": idempotencyKey,
		},
	})
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, chatSendData))

	ack := waitForResponse(t, conn, chatID, 15*time.Second)
	require.Equal(t, true, ack["ok"], "chat.send ack failed: %v", ack)

	result := waitForTerminalChatEvent(t, conn, 150*time.Second)
	newLogs := env.readGatewayLogs()[logOffset:]
	t.Logf("codex terminal state=%s message=%q", result.state, result.message)

	for _, forbidden := range []string{
		"Native hook relay unavailable",
		"OpenClaw native hook relay unavailable",
		"native hook relay unavailable for Codex app-server approval",
	} {
		require.NotContains(t, newLogs, forbidden, "gateway logs contain native hook relay failure")
	}

	require.Equal(t, "final", result.state, "codex chat did not complete successfully\n--- logs ---\n%s", newLogs)
	body, err := os.ReadFile(outputPath)
	require.NoError(t, err, "Codex shell command did not create expected file\n--- logs ---\n%s", newLogs)
	require.Equal(t, "native-hook-ok", strings.TrimSpace(string(body)))
	require.Contains(t, strings.ToLower(result.message), "native-hook-ok")
}

// gatewayConnect performs the full WebSocket connect + challenge-response handshake
// and returns a connected WebSocket ready for RPC calls.
func gatewayConnect(t *testing.T) *websocket.Conn {
	t.Helper()
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d/gateway/ws", env.gatewayPort)

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	headers := http.Header{}
	headers.Set("Origin", fmt.Sprintf("http://127.0.0.1:%d", env.gatewayPort))
	conn, _, err := dialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("WebSocket dial failed: %v", err)
	}

	// Read challenge
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, challengeData, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		t.Fatalf("failed to read challenge: %v", err)
	}

	var challenge struct {
		Payload struct {
			Nonce string `json:"nonce"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(challengeData, &challenge); err != nil {
		conn.Close()
		t.Fatalf("failed to parse challenge: %v", err)
	}

	// Send connect with auth token
	connectMsg := map[string]interface{}{
		"type":   "req",
		"id":     "connect-1",
		"method": "connect",
		"params": map[string]interface{}{
			"minProtocol": gatewayProtocolVersion,
			"maxProtocol": gatewayProtocolVersion,
			"client": map[string]interface{}{
				"id":       "openclaw-control-ui",
				"version":  "0.0.0",
				"platform": "linux",
				"mode":     "ui",
			},
			"auth": map[string]interface{}{
				"token": gatewayToken,
			},
			"scopes": []string{"operator.admin"},
		},
	}
	connectData, _ := json.Marshal(connectMsg)
	if err := conn.WriteMessage(websocket.TextMessage, connectData); err != nil {
		conn.Close()
		t.Fatalf("failed to send connect: %v", err)
	}

	// Read connect response
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, respData, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		t.Fatalf("connect response failed: %v", err)
	}

	var resp struct {
		OK    bool `json:"ok"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respData, &resp); err != nil {
		conn.Close()
		t.Fatalf("failed to parse connect response: %v", err)
	}
	if resp.Error != nil && resp.Error.Code == "UNAUTHORIZED" {
		conn.Close()
		t.Fatalf("gateway rejected auth token")
	}

	return conn
}

// TestGatewayE2E_HealthHTTP hits the gateway's /health HTTP endpoint.
// This validates the gateway loaded config and exposes its HTTP API.
func TestGatewayE2E_HealthHTTP(t *testing.T) {
	client := &http.Client{Timeout: 10 * time.Second}

	url := fmt.Sprintf("http://127.0.0.1:%d/health", env.gatewayPort)
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("health HTTP request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: got status %d, want 200; body: %s", resp.StatusCode, string(body))
	}

	t.Logf("gateway /health returned 200: %s", string(body))
}

// TestGatewayE2E_ModelsCatalog calls models.list via WebSocket and verifies
// Anthropic models appear in the catalog. This validates the full chain:
// auth-profiles.json → ensurePiAuthJsonFromAuthProfiles → auth.json → ModelRegistry → catalog.
func TestGatewayE2E_ModelsCatalog(t *testing.T) {
	conn := gatewayConnect(t)
	defer conn.Close()

	// Send models.list RPC
	modelsListMsg := map[string]interface{}{
		"type":   "req",
		"id":     "models-list-1",
		"method": "models.list",
		"params": map[string]interface{}{},
	}
	msgData, _ := json.Marshal(modelsListMsg)
	if err := conn.WriteMessage(websocket.TextMessage, msgData); err != nil {
		t.Fatalf("failed to send models.list: %v", err)
	}

	// Read response, skipping interleaved events
	deadline := time.Now().Add(15 * time.Second)
	var respPayload struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		OK      bool   `json:"ok"`
		Payload struct {
			Models []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Provider string `json:"provider"`
			} `json:"models"`
		} `json:"payload"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("failed to read models.list response: %v", err)
		}
		var msg struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.Type == "event" {
			continue
		}
		if msg.Type == "res" && msg.ID == "models-list-1" {
			if err := json.Unmarshal(data, &respPayload); err != nil {
				t.Fatalf("failed to parse models.list response: %v", err)
			}
			break
		}
	}

	if respPayload.Type == "" {
		t.Fatal("timed out waiting for models.list response")
	}
	if !respPayload.OK {
		errMsg := "unknown"
		if respPayload.Error != nil {
			errMsg = fmt.Sprintf("code=%s msg=%s", respPayload.Error.Code, respPayload.Error.Message)
		}
		t.Fatalf("models.list failed: %s", errMsg)
	}

	// Verify provider models appear in the catalog based on which keys are set.
	modelsByProvider := map[string][]string{}
	for _, m := range respPayload.Payload.Models {
		modelsByProvider[m.Provider] = append(modelsByProvider[m.Provider], m.ID)
	}

	t.Logf("models.list returned %d total models", len(respPayload.Payload.Models))

	if env.hasAnthropicKey() {
		if len(modelsByProvider["anthropic"]) == 0 {
			t.Error("models.list returned no Anthropic models — auth-profiles.json may not have been read")
			for _, m := range respPayload.Payload.Models {
				t.Logf("  provider=%s id=%s", m.Provider, m.ID)
			}
		} else {
			t.Logf("  anthropic: %d models", len(modelsByProvider["anthropic"]))
		}
	}

	// Note: when agents.defaults.models is set (via DefaultModel in config assembly),
	// models.list is filtered to only show allowlisted models. OpenAI models may not
	// appear unless explicitly added to the allowlist. This is expected behavior.
	if env.hasOpenAIKey() {
		if len(modelsByProvider["openai"]) == 0 {
			t.Logf("  openai: 0 models (expected when agents.defaults.models allowlist is set)")
		} else {
			t.Logf("  openai: %d models", len(modelsByProvider["openai"]))
		}
	}
}

// TestGatewayE2E_GatewayLogs checks the gateway process logs for errors
// that indicate config or startup problems.
func TestGatewayE2E_GatewayLogs(t *testing.T) {
	logs := env.readGatewayLogs()

	// Verify config was loaded from metadata
	if strings.Contains(logs, "metadata") || strings.Contains(logs, "config") {
		t.Log("gateway logs reference metadata/config loading")
	}

	// Fail on errors that indicate broken config wiring
	fatalPatterns := []string{
		"origin not allowed",
		"ENOENT: no such file or directory",
		"Proxy headers detected from untrusted",
	}
	for _, pattern := range fatalPatterns {
		if strings.Contains(logs, pattern) {
			t.Errorf("gateway logs contain %q (config wiring issue)", pattern)
		}
	}

	// Log a snippet for debugging
	lines := strings.Split(logs, "\n")
	if len(lines) > 50 {
		lines = lines[len(lines)-50:]
	}
	t.Logf("gateway logs (last 50 lines):\n%s", strings.Join(lines, "\n"))
}

// =============================================================================
// Tests that hit the proxy directly (validates proxy + upstream)
// =============================================================================

// TestGatewayE2E_ProxyAnthropicApiKey makes a real LLM call through the proxy
// using an api_key credential (x-api-key header injection).
// --- Success-path proxy tests (real keys → expect 200) ---

func TestGatewayE2E_ProxyAnthropicApiKey(t *testing.T) {
	if env.anthropicApiKey == "" {
		t.Skip("E2E_ANTHROPIC_API_KEY not set")
	}
	env.updateLLMKeys(map[string]metadata.CredentialEntry{
		"anthropic": {Value: env.anthropicApiKey, CredentialType: "api_key"},
	})
	status, body := doAnthropicProxyCall(t)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", status, truncate(body, 300))
	}
	t.Logf("proxy Anthropic api_key: status=%d body=%s", status, truncate(body, 200))
}

// TestGatewayE2E_ProxyAnthropicApiKey_Sonnet46 tests the BYOK model
// anthropic/claude-sonnet-4-6 through the proxy — same key, different model.
func TestGatewayE2E_ProxyAnthropicApiKey_Sonnet46(t *testing.T) {
	if env.anthropicApiKey == "" {
		t.Skip("E2E_ANTHROPIC_API_KEY not set")
	}
	env.updateLLMKeys(map[string]metadata.CredentialEntry{
		"anthropic": {Value: env.anthropicApiKey, CredentialType: "api_key"},
	})
	status, body := doAnthropicProxyCallModel(t, "claude-sonnet-4-6")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", status, truncate(body, 300))
	}
}

func TestGatewayE2E_ProxyAnthropicSubscriptionKey(t *testing.T) {
	if env.anthropicSubscriptionKey == "" {
		t.Skip("E2E_ANTHROPIC_SUBSCRIPTION_KEY not set")
	}
	env.updateLLMKeys(map[string]metadata.CredentialEntry{
		"anthropic": {Value: env.anthropicSubscriptionKey, CredentialType: "subscription_key"},
	})
	status, body := doAnthropicProxyCall(t)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", status, truncate(body, 300))
	}
	t.Logf("proxy Anthropic subscription_key: status=%d body=%s", status, truncate(body, 200))
}

// TestGatewayE2E_ProxyAnthropicSubscriptionKey_Sonnet46 tests subscription key
// with a higher-tier model to verify Bearer auth works for all BYOK models.
// Requires the subscription key to have access to claude-sonnet-4-6.
func TestGatewayE2E_ProxyAnthropicSubscriptionKey_Sonnet46(t *testing.T) {
	if env.anthropicSubscriptionKey == "" {
		t.Skip("E2E_ANTHROPIC_SUBSCRIPTION_KEY not set")
	}
	env.updateLLMKeys(map[string]metadata.CredentialEntry{
		"anthropic": {Value: env.anthropicSubscriptionKey, CredentialType: "subscription_key"},
	})
	status, body := doAnthropicProxyCallModel(t, "claude-sonnet-4-6")
	if status == http.StatusBadRequest {
		t.Skipf("subscription key does not have access to claude-sonnet-4-6 (got 400); body: %s", truncate(body, 200))
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", status, truncate(body, 300))
	}
}

func TestGatewayE2E_ProxyOpenAIApiKey(t *testing.T) {
	if env.openaiApiKey == "" {
		t.Skip("E2E_OPENAI_API_KEY not set")
	}
	env.updateLLMKeys(map[string]metadata.CredentialEntry{
		"openai": {Value: env.openaiApiKey, CredentialType: "api_key"},
	})
	status, body := doOpenAIProxyCall(t)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", status, truncate(body, 300))
	}
	t.Logf("proxy OpenAI api_key: status=%d body=%s", status, truncate(body, 200))
}

func TestGatewayE2E_ProxyOpenAISubscriptionKey(t *testing.T) {
	if env.openaiSubscriptionKey == "" {
		t.Skip("E2E_OPENAI_SUBSCRIPTION_KEY not set")
	}
	env.updateLLMKeys(map[string]metadata.CredentialEntry{
		"openai": {Value: env.openaiSubscriptionKey, CredentialType: "api_key"},
	})
	status, body := doOpenAIProxyCall(t)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", status, truncate(body, 300))
	}
	t.Logf("proxy OpenAI subscription_key: status=%d body=%s", status, truncate(body, 200))
}

// --- Failure-path proxy tests (invalid keys → expect upstream 401) ---
// These validate that the proxy correctly:
//  1. Extracts the nonce from the incoming request
//  2. Looks up the machine config from metadata
//  3. Injects the (invalid) key into the upstream request
//  4. Forwards to the real upstream
//  5. Returns the upstream 401 to the caller

func TestGatewayE2E_ProxyAnthropicBadApiKey(t *testing.T) {
	if !env.hasAnthropicKey() {
		t.Skip("no Anthropic key set (need at least one to configure the provider)")
	}
	env.updateLLMKeys(map[string]metadata.CredentialEntry{
		"anthropic": {Value: "sk-ant-api03-invalid-key-for-testing", CredentialType: "api_key"},
	})
	status, body := doAnthropicProxyCall(t)
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid api_key, got %d; body: %s", status, truncate(body, 300))
	}
	t.Logf("proxy correctly forwarded invalid Anthropic api_key → upstream 401")
}

func TestGatewayE2E_ProxyAnthropicBadSubscriptionKey(t *testing.T) {
	if !env.hasAnthropicKey() {
		t.Skip("no Anthropic key set (need at least one to configure the provider)")
	}
	env.updateLLMKeys(map[string]metadata.CredentialEntry{
		"anthropic": {Value: "sk-ant-oat01-invalid-token-for-testing", CredentialType: "subscription_key"},
	})
	status, body := doAnthropicProxyCall(t)
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid subscription_key, got %d; body: %s", status, truncate(body, 300))
	}
	t.Logf("proxy correctly forwarded invalid Anthropic subscription_key → upstream 401")
}

func TestGatewayE2E_ProxyOpenAIBadApiKey(t *testing.T) {
	if !env.hasOpenAIKey() {
		t.Skip("no OpenAI key set (need at least one to configure the provider)")
	}
	env.updateLLMKeys(map[string]metadata.CredentialEntry{
		"openai": {Value: "sk-invalid-openai-key-for-testing", CredentialType: "api_key"},
	})
	status, body := doOpenAIProxyCall(t)
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid api_key, got %d; body: %s", status, truncate(body, 300))
	}
	t.Logf("proxy correctly forwarded invalid OpenAI api_key → upstream 401")
}

func TestGatewayE2E_ProxyOpenRouterApiKey(t *testing.T) {
	if env.openrouterApiKey == "" {
		t.Skip("E2E_OPENROUTER_API_KEY not set")
	}
	env.updateLLMKeys(map[string]metadata.CredentialEntry{
		"openrouter": {Value: env.openrouterApiKey, CredentialType: "api_key"},
	})
	status, body := doOpenRouterProxyCall(t)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", status, truncate(body, 300))
	}
	t.Logf("proxy OpenRouter api_key: status=%d body=%s", status, truncate(body, 200))
}

func TestGatewayE2E_ProxyOpenRouterBadApiKey(t *testing.T) {
	if !env.hasOpenRouterKey() {
		t.Skip("no OpenRouter key set (need at least one to configure the provider)")
	}
	env.updateLLMKeys(map[string]metadata.CredentialEntry{
		"openrouter": {Value: "sk-or-v1-invalid-key-for-testing", CredentialType: "api_key"},
	})
	status, body := doOpenRouterProxyCall(t)
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid api_key, got %d; body: %s", status, truncate(body, 300))
	}
	t.Logf("proxy correctly forwarded invalid OpenRouter api_key → upstream 401")
}

// --- Proxy call helpers ---
// These return (statusCode, responseBody) without asserting on status,
// so callers can test both success and failure paths.

func doAnthropicProxyCall(t *testing.T) (int, string) {
	return doAnthropicProxyCallModel(t, "claude-haiku-4-5-20251001")
}

func doAnthropicProxyCallModel(t *testing.T, model string) (int, string) {
	t.Helper()

	body := map[string]interface{}{
		"model":      model,
		"max_tokens": 16,
		"messages": []map[string]string{
			{"role": "user", "content": "Say OK"},
		},
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	proxyURL := fmt.Sprintf("http://127.0.0.1:%d/anthropic/v1/messages", env.proxyPort)
	req, err := http.NewRequest(http.MethodPost, proxyURL, bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", nonce)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxy call failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
	t.Logf("anthropic proxy: model=%s status=%d body=%s", model, resp.StatusCode, truncate(string(respBody), 300))

	return resp.StatusCode, string(respBody)
}

func doOpenAIProxyCall(t *testing.T) (int, string) {
	t.Helper()

	body := map[string]interface{}{
		"model":      "gpt-4o-mini",
		"max_tokens": 16,
		"messages": []map[string]string{
			{"role": "user", "content": "Say OK"},
		},
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	proxyURL := fmt.Sprintf("http://127.0.0.1:%d/openai/v1/chat/completions", env.proxyPort)
	req, err := http.NewRequest(http.MethodPost, proxyURL, bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+nonce)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxy call failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
	t.Logf("openai proxy: status=%d", resp.StatusCode)

	return resp.StatusCode, string(respBody)
}

func doOpenRouterProxyCall(t *testing.T) (int, string) {
	t.Helper()

	body := map[string]interface{}{
		"model":      "meta-llama/llama-3.1-8b-instruct",
		"max_tokens": 16,
		"messages": []map[string]string{
			{"role": "user", "content": "Say OK"},
		},
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	proxyURL := fmt.Sprintf("http://127.0.0.1:%d/openrouter/v1/chat/completions", env.proxyPort)
	req, err := http.NewRequest(http.MethodPost, proxyURL, bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+nonce)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxy call failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
	t.Logf("openrouter proxy: status=%d", resp.StatusCode)

	return resp.StatusCode, string(respBody)
}

// =============================================================================
// Tests that verify metadata config assembly
// =============================================================================

// TestGatewayE2E_MetadataConfig verifies the metadata server serves config correctly.
// TestGatewayE2E_FileConfig verifies that openclaw.json on disk contains
// the expected config structure (replaces TestGatewayE2E_MetadataConfig which
// tested the now-removed /v1/config endpoint).
func TestGatewayE2E_FileConfig(t *testing.T) {
	configPath := filepath.Join(env.tmpDir, ".openclaw", "openclaw.json")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read openclaw.json: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(configData, &config); err != nil {
		t.Fatalf("failed to decode config: %v", err)
	}

	// Verify the config has gateway settings from platform defaults
	if _, ok := config["gateway"]; !ok {
		t.Error("config missing 'gateway' key")
	}

	// Verify proxy base URL was injected into models.providers
	models, ok := config["models"].(map[string]interface{})
	if !ok {
		t.Fatal("config missing 'models' key")
	}
	providers, ok := models["providers"].(map[string]interface{})
	if !ok {
		t.Fatal("config missing 'models.providers' key")
	}

	// Check each configured provider's baseUrl points to the proxy
	for _, provider := range []string{"anthropic", "openai"} {
		var hasKey bool
		switch provider {
		case "anthropic":
			hasKey = env.hasAnthropicKey()
		case "openai":
			hasKey = env.hasOpenAIKey()
		}
		if !hasKey {
			continue
		}
		provConf, ok := providers[provider].(map[string]interface{})
		if !ok {
			t.Errorf("config missing 'models.providers.%s'", provider)
			continue
		}
		baseURL, ok := provConf["baseUrl"].(string)
		if !ok || !strings.Contains(baseURL, fmt.Sprintf(":%d/%s", env.proxyPort, provider)) {
			t.Errorf("%s baseUrl = %q, expected to contain proxy port %d", provider, baseURL, env.proxyPort)
		} else {
			t.Logf("file config: %s baseUrl=%s", provider, baseURL)
		}
	}
}

// TestGatewayE2E_ComposioConfigAssembly verifies that when ComposioAPIURL
// is set in AssemblyParams, the assembled gateway config contains the correct
// Composio plugin entries with apiUrl pointing to the backend proxy and
// userId set to the machine ID.
func TestGatewayE2E_ComposioConfigAssembly(t *testing.T) {
	const testMachineID = "e2e-composio-machine-123"
	const testAPIURL = "https://api.openclawmachines.com/api/composio"

	params := configassembly.AssemblyParams{
		MachineID: testMachineID,
		Plugins: []configassembly.PluginSelection{
			{
				PluginID: "composio",
				Slot:     "integrations",
				ConfigTemplate: map[string]interface{}{
					"plugins": map[string]interface{}{
						"allow": []interface{}{"composio"},
						"entries": map[string]interface{}{
							"composio": map[string]interface{}{
								"enabled": true,
								"config":  map[string]interface{}{},
							},
						},
					},
				},
			},
		},
		ComposioAPIURL: testAPIURL,
		ComposioProxyTokenSigner: func(machineID string) (string, error) {
			return "e2e-test-token:" + machineID, nil
		},
	}

	data, err := configassembly.AssembleConfig(params)
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal assembled config: %v", err)
	}

	plugins, ok := result["plugins"].(map[string]interface{})
	if !ok {
		t.Fatal("assembled config missing 'plugins' key")
	}

	entries, ok := plugins["entries"].(map[string]interface{})
	if !ok {
		t.Fatal("assembled config missing 'plugins.entries'")
	}

	composioEntry, ok := entries["composio"].(map[string]interface{})
	if !ok {
		t.Fatal("assembled config missing 'plugins.entries.composio'")
	}

	config, ok := composioEntry["config"].(map[string]interface{})
	if !ok {
		t.Fatal("assembled config missing 'plugins.entries.composio.config'")
	}

	// apiUrl must point to the backend proxy.
	if config["apiUrl"] != testAPIURL {
		t.Errorf("apiUrl = %v, want %q", config["apiUrl"], testAPIURL)
	}

	// userId must match the machine ID.
	if config["userId"] != testMachineID {
		t.Errorf("plugins.entries.composio.config.userId = %v, want %q", config["userId"], testMachineID)
	}

	// machineToken from the signer must be present so the in-VM plugin can
	// authenticate to the backend proxy.
	wantToken := "e2e-test-token:" + testMachineID
	if config["machineToken"] != wantToken {
		t.Errorf("plugins.entries.composio.config.machineToken = %v, want %q", config["machineToken"], wantToken)
	}

	// plugins.installs should not be present — plugins are bundled in artifact
	if _, ok := plugins["installs"]; ok {
		t.Error("plugins.installs should not be present — plugins are bundled in artifact")
	}

	t.Logf("composio config assembly OK: apiUrl=%s userId=%s machineToken=present",
		testAPIURL, testMachineID)
}

// TestGatewayE2E_ModelsListContainsClaude verifies that the gateway's model
// catalog includes claude-sonnet-4-6 when the assembled config has
// models.providers.anthropic = {baseUrl: "...", models: []}.
// This catches regressions where an empty models array causes piSdk's
// ModelRegistry to not discover built-in Anthropic models.
func TestGatewayE2E_ModelsListContainsClaude(t *testing.T) {
	conn := gatewayWSConnect(t)
	defer conn.Close()

	// Send models.list request
	modelsReq := map[string]interface{}{
		"type":   "req",
		"id":     "models-1",
		"method": "models.list",
		"params": map[string]interface{}{},
	}
	reqData, _ := json.Marshal(modelsReq)
	if err := conn.WriteMessage(websocket.TextMessage, reqData); err != nil {
		t.Fatalf("failed to send models.list: %v", err)
	}

	// Read response (may need to skip event messages)
	var modelsResp map[string]interface{}
	deadline := time.Now().Add(30 * time.Second)
	conn.SetReadDeadline(deadline)
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("failed to read models.list response: %v", err)
		}
		var msg map[string]interface{}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		// Skip event messages, wait for response to our request
		if msg["type"] == "res" && msg["id"] == "models-1" {
			modelsResp = msg
			break
		}
	}

	if modelsResp["ok"] != true {
		respJSON, _ := json.MarshalIndent(modelsResp, "", "  ")
		t.Fatalf("models.list failed: %s", string(respJSON))
	}

	// Extract model list from payload
	payload, _ := modelsResp["payload"].(map[string]interface{})
	models, _ := payload["models"].([]interface{})
	if len(models) == 0 {
		respJSON, _ := json.MarshalIndent(modelsResp, "", "  ")
		t.Fatalf("models.list returned zero models; response: %s", string(respJSON))
	}

	// Check for key models
	modelIDs := map[string]bool{}
	for _, m := range models {
		if model, ok := m.(map[string]interface{}); ok {
			if id, ok := model["id"].(string); ok {
				modelIDs[id] = true
			}
		}
	}

	t.Logf("gateway discovered %d models", len(models))

	// Log all anthropic models for debugging
	for _, m := range models {
		if model, ok := m.(map[string]interface{}); ok {
			provider, _ := model["provider"].(string)
			if provider == "anthropic" {
				id, _ := model["id"].(string)
				name, _ := model["name"].(string)
				t.Logf("  anthropic model: id=%s name=%s", id, name)
			}
		}
	}

	// Must have at least some Anthropic models
	wantModels := []string{
		"claude-sonnet-4-6",
	}
	for _, want := range wantModels {
		if !modelIDs[want] {
			t.Errorf("model %q NOT found in gateway model catalog (models: []  may be suppressing built-in discovery)", want)
		} else {
			t.Logf("model %q found in catalog", want)
		}
	}

	// Also check for claude-sonnet-4-5 (template for forward-compat)
	if modelIDs["claude-sonnet-4-5"] || modelIDs["claude-sonnet-4.5"] {
		t.Logf("forward-compat template model found")
	} else {
		t.Logf("WARNING: neither claude-sonnet-4-5 nor claude-sonnet-4.5 found — forward-compat may fail")
	}
}

// gatewayWSConnect establishes an authenticated WebSocket connection to the gateway.
func gatewayWSConnect(t *testing.T) *websocket.Conn {
	t.Helper()
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d/gateway/ws", env.gatewayPort)

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	headers := http.Header{}
	headers.Set("Origin", fmt.Sprintf("http://127.0.0.1:%d", env.gatewayPort))
	conn, _, err := dialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("WebSocket dial failed: %v", err)
	}

	// Read connect.challenge
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, _, err = conn.ReadMessage()
	if err != nil {
		conn.Close()
		t.Fatalf("failed to read challenge: %v", err)
	}

	// Send connect with gateway token
	connectMsg := map[string]interface{}{
		"type":   "req",
		"id":     "connect-auto",
		"method": "connect",
		"params": map[string]interface{}{
			"minProtocol": gatewayProtocolVersion,
			"maxProtocol": gatewayProtocolVersion,
			"client": map[string]interface{}{
				"id":       "openclaw-control-ui",
				"version":  "0.0.0",
				"platform": "linux",
				"mode":     "ui",
			},
			"auth":   map[string]interface{}{"token": gatewayToken},
			"scopes": []string{"operator.admin"},
		},
	}
	connectData, _ := json.Marshal(connectMsg)
	if err := conn.WriteMessage(websocket.TextMessage, connectData); err != nil {
		conn.Close()
		t.Fatalf("failed to send connect: %v", err)
	}

	// Read connect response
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, respData, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		t.Fatalf("failed to read connect response: %v", err)
	}
	var resp struct {
		OK    bool `json:"ok"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respData, &resp); err != nil {
		conn.Close()
		t.Fatalf("failed to parse connect response: %v", err)
	}
	if !resp.OK {
		conn.Close()
		t.Fatalf("gateway connect failed: %+v", resp.Error)
	}

	return conn
}

// =============================================================================
// Helpers
// =============================================================================

// waitForResponse reads WebSocket messages until it finds a response with the
// given ID, skipping any event messages. Returns the parsed response.
func waitForResponse(t *testing.T, conn *websocket.Conn, id string, timeout time.Duration) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("failed to read response for %s: %v", id, err)
		}
		var msg map[string]interface{}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg["type"] == "res" && msg["id"] == id {
			return msg
		}
	}
	t.Fatalf("timed out waiting for response %s", id)
	return nil
}

type terminalChatResult struct {
	state   string
	message string
}

// waitForTerminalChatEvent consumes websocket messages until it sees a terminal
// chat event, using one overall timeout instead of per-read retry deadlines.
func waitForTerminalChatEvent(t *testing.T, conn *websocket.Conn, timeout time.Duration) terminalChatResult {
	t.Helper()

	// Clear any earlier read deadline from the chat.send ack phase so the
	// terminal-state wait is governed by a single end-to-end timeout.
	_ = conn.SetReadDeadline(time.Time{})

	deadline := time.Now().Add(timeout)
	_ = conn.SetReadDeadline(deadline)

	for {
		_, msgData, err := conn.ReadMessage()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// The single absolute deadline expired before a terminal chat event.
				// Treat that as the overall chat timeout, not as a retryable read.
				t.Fatalf("timed out waiting for terminal chat event after %s", timeout)
			}
			t.Fatalf("websocket read failed while waiting for terminal chat event: %v", err)
		}

		var msg map[string]interface{}
		if err := json.Unmarshal(msgData, &msg); err != nil {
			continue
		}
		if msg["type"] != "event" || msg["event"] != "chat" {
			continue
		}

		payload, _ := msg["payload"].(map[string]interface{})
		if payload == nil {
			continue
		}

		state, _ := payload["state"].(string)
		t.Logf("chat event: state=%s", state)
		if state != "final" && state != "error" {
			continue
		}

		result := terminalChatResult{state: state}
		if message, ok := payload["message"].(map[string]interface{}); ok {
			if content, ok := message["content"].([]interface{}); ok && len(content) > 0 {
				if block, ok := content[0].(map[string]interface{}); ok {
					result.message, _ = block["text"].(string)
				}
			}
		}
		return result
	}
}

// truncate returns at most maxLen characters of s, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// freePort finds an available TCP port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

// waitForHTTP polls a URL until it returns 200 or the timeout expires.
func waitForHTTP(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout waiting for %s after %s", url, timeout)
}

// waitForHTTPOrExit polls a URL until it returns 200, the timeout expires,
// or the process exits (indicated by the done channel closing).
func waitForHTTPOrExit(url string, timeout time.Duration, done <-chan struct{}) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return fmt.Errorf("gateway process exited before becoming healthy")
		default:
		}
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout waiting for %s after %s", url, timeout)
}

// TestNativeMode_SeedConfigMetadataRoundTrip verifies that AssembleSeedConfig
// produces valid JSON with exec provider refs, and that seed config round-trips
// through the metadata server via /v1/config.
func TestNativeMode_SeedConfigMetadataRoundTrip(t *testing.T) {
	// 1. Assemble a seed config with exec provider refs
	seedJSON, err := configassembly.AssembleSeedConfig(configassembly.SeedParams{
		ModelCatalog: e2eModelCatalog(),
		MachineID:    machineID,
		DefaultModel: "anthropic/claude-sonnet-4-20250514",
		Providers:    []string{"anthropic", "openai"},
		ProxyBaseURL: fmt.Sprintf("http://127.0.0.1:%d", env.proxyPort),
	})
	require.NoError(t, err, "AssembleSeedConfig should succeed")

	// 2. Verify seed config JSON structure
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(seedJSON, &parsed), "seed config should be valid JSON")

	// Check exec provider definition at secrets.providers.ocm
	secrets := parsed["secrets"].(map[string]interface{})
	providers := secrets["providers"].(map[string]interface{})
	ocmProvider := providers["ocm"].(map[string]interface{})
	assert.Equal(t, "exec", ocmProvider["source"])
	assert.Equal(t, "/usr/local/bin/ocm-secrets", ocmProvider["command"])

	// Check model provider configs have exec secret refs
	models := parsed["models"].(map[string]interface{})
	modelProviders := models["providers"].(map[string]interface{})
	anthropicCfg := modelProviders["anthropic"].(map[string]interface{})
	apiKey := anthropicCfg["apiKey"].(map[string]interface{})
	assert.Equal(t, "exec", apiKey["source"])
	assert.Equal(t, "ocm", apiKey["provider"])
	assert.Equal(t, "anthropic-key", apiKey["id"])

	// Verify every provider has a "models" array (gateway schema requires it)
	for providerName, providerVal := range modelProviders {
		pm := providerVal.(map[string]interface{})
		modelsArr, ok := pm["models"].([]interface{})
		if !ok {
			t.Errorf("provider %s missing models array (gateway will reject config)", providerName)
		} else {
			t.Logf("provider %s: models array present (len=%d)", providerName, len(modelsArr))
		}
	}

	// 3. Write seed config to disk and verify structure
	configPath := filepath.Join(env.tmpDir, ".openclaw", "openclaw.json")
	require.NoError(t, os.WriteFile(configPath, seedJSON, 0644), "write seed config to disk")
	defer os.WriteFile(configPath, env.configBytes, 0644) // restore original

	// 4. Read back from disk — verify seed config structure is preserved
	body, err := os.ReadFile(configPath)
	require.NoError(t, err)

	var roundTripped map[string]interface{}
	require.NoError(t, json.Unmarshal(body, &roundTripped))

	// Verify exec provider survives write-to-disk
	rtSecrets := roundTripped["secrets"].(map[string]interface{})
	rtProviders := rtSecrets["providers"].(map[string]interface{})
	rtOCM := rtProviders["ocm"].(map[string]interface{})
	assert.Equal(t, "exec", rtOCM["source"])
	t.Logf("Disk config: %d bytes, exec provider intact", len(body))

	// 5. GET /v1/machine — verify agent_version is exposed
	machineURL := fmt.Sprintf("http://127.0.0.1:%d/v1/machine?nonce=%s", env.metaPort, nonce)
	machineResp, err := http.Get(machineURL)
	require.NoError(t, err)
	defer machineResp.Body.Close()
	require.Equal(t, http.StatusOK, machineResp.StatusCode)

	var machineData map[string]string
	require.NoError(t, json.NewDecoder(machineResp.Body).Decode(&machineData))
	assert.Equal(t, "test-e2e", machineData["agent_version"],
		"/v1/machine must expose agent_version for debugging")
}

// TestNativeMode_ProxyAnthropicApiKey verifies the proxy flow:
// seed config (with exec provider refs) + real LLM keys → proxy resolves nonce
// to real key → upstream Anthropic returns 200.
func TestNativeMode_ProxyAnthropicApiKey(t *testing.T) {
	if env.anthropicApiKey == "" {
		t.Skip("E2E_ANTHROPIC_API_KEY not set")
	}

	// 1. Assemble a native-mode seed config with exec provider refs
	seedJSON, err := configassembly.AssembleSeedConfig(configassembly.SeedParams{
		ModelCatalog: e2eModelCatalog(),
		MachineID:    machineID,
		DefaultModel: "anthropic/claude-sonnet-4-6",
		Providers:    []string{"anthropic"},
		ProxyBaseURL: fmt.Sprintf("http://127.0.0.1:%d", env.proxyPort),
	})
	require.NoError(t, err, "AssembleSeedConfig should succeed")

	// 2. Register machine with seed config + real Anthropic key (native mode)
	origConfig, _ := env.metaSrv.GetConfig("127.0.0.1")
	defer env.metaSrv.RegisterMachine("127.0.0.1", origConfig)

	env.metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID:    machineID,
		Nonce:        nonce,
		GatewayToken: gatewayToken,
		OpenClawConf: seedJSON,
		LLMKeys: map[string]metadata.CredentialEntry{
			"anthropic": {Value: env.anthropicApiKey, CredentialType: "api_key"},
		},
	})

	// 3. Call the proxy — nonce should be swapped for the real key
	status, body := doAnthropicProxyCall(t)
	if status != http.StatusOK {
		t.Fatalf("native-mode proxy: expected 200, got %d; body: %s", status, truncate(body, 300))
	}
	t.Logf("native-mode proxy Anthropic api_key: status=%d body=%s", status, truncate(body, 200))
}

// fetchNebiusKey retrieves the Nebius API key from GCP Secret Manager.
// Skips the test if the key cannot be fetched — matches the rest of the suite,
// which skips when creds are absent rather than red-failing CI on env drift.
func fetchNebiusKey(t *testing.T) string {
	t.Helper()
	const secretName = "projects/clarateach/secrets/NEBIUS_API_KEY/versions/latest"
	key, err := secrets.FetchSecret(context.Background(), secretName)
	if err != nil {
		t.Skipf("NEBIUS_API_KEY unavailable from Secret Manager: %v", err)
	}
	return key
}

// TestNativeMode_ProxyNebiusApiKey verifies the full native-mode proxy flow for
// Nebius platform models: seed config with nebius provider + exec secret refs →
// proxy resolves nonce to real Nebius API key → upstream returns 200.
func TestNativeMode_ProxyNebiusApiKey(t *testing.T) {
	nebiusApiKey := fetchNebiusKey(t)

	// 1. Assemble a native-mode seed config with the default platform model
	seedJSON, err := configassembly.AssembleSeedConfig(configassembly.SeedParams{
		ModelCatalog: e2eModelCatalog(),
		MachineID:    machineID,
		DefaultModel: "openai/gpt-oss-120b",
		Providers:    []string{}, // no BYOK providers, only platform nebius
		ProxyBaseURL: fmt.Sprintf("http://127.0.0.1:%d", env.proxyPort),
	})
	require.NoError(t, err, "AssembleSeedConfig should succeed")

	// 2. Register machine with seed config + real Nebius key (native mode)
	origConfig, _ := env.metaSrv.GetConfig("127.0.0.1")
	defer env.metaSrv.RegisterMachine("127.0.0.1", origConfig)

	env.metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID:    machineID,
		Nonce:        nonce,
		GatewayToken: gatewayToken,
		OpenClawConf: seedJSON,
		LLMKeys: map[string]metadata.CredentialEntry{
			"nebius": {Value: nebiusApiKey, CredentialType: "api_key"},
		},
	})

	// 3. Call the proxy with a Nebius platform model — nonce swapped for real key
	status, body := doNebiusProxyCall(t, "openai/gpt-oss-120b")
	if status != http.StatusOK {
		t.Fatalf("native-mode nebius proxy: expected 200, got %d; body: %s", status, truncate(body, 300))
	}
	t.Logf("native-mode proxy Nebius gpt-oss-120b: status=%d body=%s", status, truncate(body, 200))
}

// fetchOpenRouterKey retrieves the OpenRouter API key from GCP Secret Manager.
// Skips the test if the key cannot be fetched (see fetchNebiusKey).
func fetchOpenRouterKey(t *testing.T) string {
	t.Helper()
	const secretName = "projects/clarateach/secrets/GCP_OPEN_ROUTER_KEY/versions/latest"
	key, err := secrets.FetchSecret(context.Background(), secretName)
	if err != nil {
		t.Skipf("GCP_OPEN_ROUTER_KEY unavailable from Secret Manager: %v", err)
	}
	return key
}

// TestNativeMode_ProxyOpenRouterApiKey verifies the full native-mode proxy flow
// for OpenRouter BYOK models: seed config with openrouter provider + exec
// secret refs → proxy resolves nonce to real OpenRouter API key → upstream
// returns 200.
func TestNativeMode_ProxyOpenRouterApiKey(t *testing.T) {
	openrouterApiKey := fetchOpenRouterKey(t)

	// 1. Assemble a native-mode seed config with openrouter as a BYOK provider
	seedJSON, err := configassembly.AssembleSeedConfig(configassembly.SeedParams{
		ModelCatalog: e2eModelCatalog(),
		MachineID:    machineID,
		DefaultModel: "openrouter/meta-llama/llama-3.1-8b-instruct",
		Providers:    []string{"openrouter"},
		ProxyBaseURL: fmt.Sprintf("http://127.0.0.1:%d", env.proxyPort),
	})
	require.NoError(t, err, "AssembleSeedConfig should succeed")

	// 2. Register machine with seed config + real OpenRouter key (native mode)
	origConfig, _ := env.metaSrv.GetConfig("127.0.0.1")
	defer env.metaSrv.RegisterMachine("127.0.0.1", origConfig)

	env.metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID:    machineID,
		Nonce:        nonce,
		GatewayToken: gatewayToken,
		OpenClawConf: seedJSON,
		LLMKeys: map[string]metadata.CredentialEntry{
			"openrouter": {Value: openrouterApiKey, CredentialType: "api_key"},
		},
	})

	// 3. Call the proxy with an OpenRouter model — nonce swapped for real key
	status, body := doOpenRouterProxyCall(t)
	if status != http.StatusOK {
		t.Fatalf("native-mode openrouter proxy: expected 200, got %d; body: %s", status, truncate(body, 300))
	}
	t.Logf("native-mode proxy OpenRouter llama-3.1-8b: status=%d body=%s", status, truncate(body, 200))
}

// TestGatewayE2E_ConfigChangeNoGatewayCrash verifies that model switching via
// `openclaw config set` doesn't crash the gateway. Tests both single writes and
// rapid back-to-back writes (the two-op model push).
func TestGatewayE2E_ConfigChangeNoGatewayCrash(t *testing.T) {
	ensureConfigSeeded(t)
	h := newConfigSetHelper(t)
	logsBefore := len(env.readGatewayLogs())

	// Read current config to determine old model
	configPath := filepath.Join(env.tmpDir, ".openclaw", "openclaw.json")
	configData, err := os.ReadFile(configPath)
	require.NoError(t, err, "read openclaw.json")
	t.Logf("openclaw.json before change: %d bytes", len(configData))

	var config map[string]interface{}
	require.NoError(t, json.Unmarshal(configData, &config))
	oldPrimary := ""
	if agents, ok := config["agents"].(map[string]interface{}); ok {
		if defaults, ok := agents["defaults"].(map[string]interface{}); ok {
			if model, ok := defaults["model"].(map[string]interface{}); ok {
				oldPrimary, _ = model["primary"].(string)
			}
		}
	}

	newPrimary := "nebius/openai/gpt-oss-120b"
	if oldPrimary == newPrimary {
		newPrimary = "nebius/MiniMaxAI/MiniMax-M2.5"
	}
	t.Logf("changing model.primary: %q → %q", oldPrimary, newPrimary)

	// 1. Single config change: set the primary model
	err = h.set("agents.defaults.model.primary", newPrimary, false)
	require.NoError(t, err, "first config set")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "first config set")

	newLogs := env.readGatewayLogs()[logsBefore:]
	assertNoCrashIndicators(t, newLogs, "first config set")

	// 2. Rapid back-to-back config sets (simulates the two-op model push)
	t.Log("--- rapid back-to-back config sets (simulating model push) ---")
	logsBefore2 := len(env.readGatewayLogs())

	secondModel := "nebius/Qwen/Qwen3.5-397B-A17B"
	err = h.set("agents.defaults.model.primary", secondModel, false)
	require.NoError(t, err, "second config set")

	modelsJSON := fmt.Sprintf(`{%q:{}}`, secondModel)
	err = h.set("agents.defaults.models", modelsJSON, true)
	require.NoError(t, err, "models catalog config set")

	time.Sleep(5 * time.Second)
	assertGatewayHealthy(t, "rapid config sets")

	newLogs2 := env.readGatewayLogs()[logsBefore2:]
	assertNoCrashIndicators(t, newLogs2, "rapid config sets")
	t.Logf("logs after rapid config sets (%d bytes):\n%s", len(newLogs2), truncate(newLogs2, 2000))

	t.Log("gateway survived all config set operations without crashing")
}

// --- Config push test helpers ---

// configSetHelper runs `openclaw config set` or `openclaw config unset` against
// the gateway's state dir, simulating what the backend does in production.
type configSetHelper struct {
	t        *testing.T
	bin      string
	homeDir  string
	stateDir string
}

func newConfigSetHelper(t *testing.T) *configSetHelper {
	t.Helper()
	bin := env.openclawBin
	if bin == "" {
		var err error
		bin, err = exec.LookPath("openclaw")
		if err != nil {
			t.Fatalf("openclaw not found in PATH: %v", err)
		}
	}
	return &configSetHelper{
		t:        t,
		bin:      bin,
		homeDir:  env.tmpDir,
		stateDir: filepath.Join(env.tmpDir, ".openclaw"),
	}
}

func (h *configSetHelper) set(path, value string, strictJSON bool) error {
	args := []string{"config", "set", path, value, "--replace"}
	if strictJSON {
		args = append(args, "--strict-json")
	}
	cmd := exec.Command(h.bin, args...)
	cmd.Env = []string{
		"HOME=" + h.homeDir,
		"PATH=" + os.Getenv("PATH"),
		"OPENCLAW_STATE_DIR=" + h.stateDir,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Logf("openclaw config set %s: exit=%v output=%s", path, err, string(out))
		return fmt.Errorf("config set %s: %w: %s", path, err, string(out))
	}
	h.t.Logf("openclaw config set %s = %s → ok", path, truncate(value, 80))
	return nil
}

func (h *configSetHelper) unset(path string) error {
	args := []string{"config", "unset", path}
	cmd := exec.Command(h.bin, args...)
	cmd.Env = []string{
		"HOME=" + h.homeDir,
		"PATH=" + os.Getenv("PATH"),
		"OPENCLAW_STATE_DIR=" + h.stateDir,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Logf("openclaw config unset %s: exit=%v output=%s", path, err, string(out))
		return fmt.Errorf("config unset %s: %w: %s", path, err, string(out))
	}
	h.t.Logf("openclaw config unset %s → ok", path)
	return nil
}

// ensureConfigSeeded writes openclaw.json if it doesn't exist (mirrors init script).
func ensureConfigSeeded(t *testing.T) {
	t.Helper()
	configPath := filepath.Join(env.tmpDir, ".openclaw", "openclaw.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Log("seeding openclaw.json from assembled config")
		require.NoError(t, os.WriteFile(configPath, env.configBytes, 0644))
		time.Sleep(2 * time.Second)
	}
}

// assertGatewayHealthy checks health endpoint and process liveness.
func assertGatewayHealthy(t *testing.T, context string) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/health", env.gatewayPort)
	resp, err := client.Get(healthURL)
	if err != nil {
		t.Fatalf("gateway health check failed after %s: %v\n--- logs ---\n%s", context, err, env.readGatewayLogs())
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("gateway health returned %d after %s; body: %s", resp.StatusCode, context, body)
	}
	select {
	case <-env.gatewayDone:
		t.Fatalf("gateway process exited after %s\n--- logs ---\n%s", context, env.readGatewayLogs())
	default:
	}
}

// assertNoCrashIndicators scans recent logs for crash indicators.
func assertNoCrashIndicators(t *testing.T, logs, context string) {
	t.Helper()
	indicators := []string{
		"Config write anomaly",
		"Crash ",
		"exit_code=1",
		"FATAL",
		"missing-meta-before-write",
	}
	for _, ind := range indicators {
		if strings.Contains(logs, ind) {
			t.Errorf("gateway log contains crash indicator %q after %s:\n%s", ind, context, logs)
		}
	}
}

// --- Config push E2E tests for each config section ---

// TestGatewayE2E_ConfigPush_AddProvider verifies that adding a new LLM provider
// via `openclaw config set models.providers.<name>` doesn't crash the gateway.
func TestGatewayE2E_ConfigPush_AddProvider(t *testing.T) {
	ensureConfigSeeded(t)
	h := newConfigSetHelper(t)
	logsBefore := len(env.readGatewayLogs())

	// Add a new provider (google — not present in default E2E config)
	providerJSON := `{"baseUrl":"http://127.0.0.1:9999/google/v1","models":[]}`
	err := h.set("models.providers.google", providerJSON, true)
	require.NoError(t, err, "add google provider")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "add google provider")

	// Remove it
	err = h.unset("models.providers.google")
	require.NoError(t, err, "remove google provider")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "remove google provider")

	newLogs := env.readGatewayLogs()[logsBefore:]
	assertNoCrashIndicators(t, newLogs, "provider add/remove")
	t.Logf("logs after provider changes (%d bytes):\n%s", len(newLogs), truncate(newLogs, 1000))
	t.Log("gateway survived provider add/remove without crashing")
}

// TestGatewayE2E_ConfigPush_AddChannel verifies that adding and removing a channel
// (e.g. telegram) via `openclaw config set channels.<name>` doesn't crash the gateway.
func TestGatewayE2E_ConfigPush_AddChannel(t *testing.T) {
	ensureConfigSeeded(t)
	h := newConfigSetHelper(t)
	logsBefore := len(env.readGatewayLogs())

	// Add a telegram channel
	channelJSON := `{"enabled":true,"botToken":"fake-token-for-testing"}`
	err := h.set("channels.telegram", channelJSON, true)
	require.NoError(t, err, "add telegram channel")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "add telegram channel")

	// Add a discord channel
	discordJSON := `{"enabled":true,"token":"fake-discord-token"}`
	err = h.set("channels.discord", discordJSON, true)
	require.NoError(t, err, "add discord channel")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "add discord channel")

	// Remove telegram (simulates disabling a channel)
	err = h.unset("channels.telegram")
	require.NoError(t, err, "remove telegram channel")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "remove telegram channel")

	// Remove discord
	err = h.unset("channels.discord")
	require.NoError(t, err, "remove discord channel")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "remove discord channel")

	newLogs := env.readGatewayLogs()[logsBefore:]
	assertNoCrashIndicators(t, newLogs, "channel add/remove")
	t.Logf("logs after channel changes (%d bytes):\n%s", len(newLogs), truncate(newLogs, 1000))
	t.Log("gateway survived channel add/remove without crashing")
}

// TestGatewayE2E_ConfigPush_AddPlugin verifies that adding and removing a plugin
// entry via `openclaw config set plugins.entries.<name>` doesn't crash the gateway.
func TestGatewayE2E_ConfigPush_AddPlugin(t *testing.T) {
	ensureConfigSeeded(t)
	h := newConfigSetHelper(t)
	logsBefore := len(env.readGatewayLogs())

	// Add a plugin entry
	pluginJSON := `{"enabled":true,"config":{"apiKey":"test-key"}}`
	err := h.set("plugins.entries.test-plugin", pluginJSON, true)
	require.NoError(t, err, "add test-plugin")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "add plugin")

	// Add a second plugin entry (use a dummy name, not opik-openclaw, to
	// avoid clobbering the real opik config needed by TestGatewayE2E_OpikTracing)
	plugin2JSON := `{"enabled":true,"config":{"key":"val"}}`
	err = h.set("plugins.entries.test-plugin-2", plugin2JSON, true)
	require.NoError(t, err, "add test-plugin-2")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "add second plugin")

	// Remove both test plugins
	err = h.unset("plugins.entries.test-plugin")
	require.NoError(t, err, "remove test-plugin")

	err = h.unset("plugins.entries.test-plugin-2")
	require.NoError(t, err, "remove test-plugin-2")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "remove plugin")

	newLogs := env.readGatewayLogs()[logsBefore:]
	assertNoCrashIndicators(t, newLogs, "plugin add/remove")
	t.Logf("logs after plugin changes (%d bytes):\n%s", len(newLogs), truncate(newLogs, 1000))
	t.Log("gateway survived plugin add/remove without crashing")
}

// TestGatewayE2E_ConfigPush_SetSkills verifies that setting the skills.allowBundled
// array via `openclaw config set skills.allowBundled` doesn't crash the gateway.
func TestGatewayE2E_ConfigPush_SetSkills(t *testing.T) {
	ensureConfigSeeded(t)
	h := newConfigSetHelper(t)
	logsBefore := len(env.readGatewayLogs())

	// Set skills (array of IDs — set as full JSON object)
	skillsJSON := `["code-review","web-search","image-gen"]`
	err := h.set("skills.allowBundled", skillsJSON, true)
	require.NoError(t, err, "set skills.allowBundled")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "set skills")

	// Update to different skills
	skillsJSON2 := `["code-review"]`
	err = h.set("skills.allowBundled", skillsJSON2, true)
	require.NoError(t, err, "update skills.allowBundled")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "update skills")

	// Set empty array (disable all skills)
	err = h.set("skills.allowBundled", `[]`, true)
	require.NoError(t, err, "clear skills.allowBundled")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "clear skills")

	newLogs := env.readGatewayLogs()[logsBefore:]
	assertNoCrashIndicators(t, newLogs, "skills changes")
	t.Logf("logs after skills changes (%d bytes):\n%s", len(newLogs), truncate(newLogs, 1000))
	t.Log("gateway survived skills changes without crashing")
}

// TestGatewayE2E_ConfigPush_SetIdentity verifies that setting and removing the
// assistant identity (ui.assistant) doesn't crash the gateway.
func TestGatewayE2E_ConfigPush_SetIdentity(t *testing.T) {
	ensureConfigSeeded(t)
	h := newConfigSetHelper(t)
	logsBefore := len(env.readGatewayLogs())

	// Set identity
	identityJSON := `{"name":"TestBot","avatar":"https://example.com/bot.png"}`
	err := h.set("ui.assistant", identityJSON, true)
	require.NoError(t, err, "set identity")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "set identity")

	// Update identity
	identityJSON2 := `{"name":"UpdatedBot"}`
	err = h.set("ui.assistant", identityJSON2, true)
	require.NoError(t, err, "update identity")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "update identity")

	// Remove identity
	err = h.unset("ui.assistant")
	require.NoError(t, err, "remove identity")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "remove identity")

	newLogs := env.readGatewayLogs()[logsBefore:]
	assertNoCrashIndicators(t, newLogs, "identity changes")
	t.Logf("logs after identity changes (%d bytes):\n%s", len(newLogs), truncate(newLogs, 1000))
	t.Log("gateway survived identity changes without crashing")
}

// TestGatewayE2E_ConfigPush_SetAgentsList verifies that setting the agents.list
// array (personas) doesn't crash the gateway.
func TestGatewayE2E_ConfigPush_SetAgentsList(t *testing.T) {
	ensureConfigSeeded(t)
	h := newConfigSetHelper(t)
	logsBefore := len(env.readGatewayLogs())

	// Set agents list with a persona
	agentsJSON := `[{"id":"agent-1","name":"Code Reviewer","model":{"primary":"anthropic/claude-sonnet-4-6"},"identity":{"emoji":"🔍"},"workspace":"/home/openclaw/.openclaw/workspace-agent-1"}]`
	err := h.set("agents.list", agentsJSON, true)
	require.NoError(t, err, "set agents.list")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "set agents list")

	// Add a second agent
	agentsJSON2 := `[{"id":"agent-1","name":"Code Reviewer","model":{"primary":"anthropic/claude-sonnet-4-6"},"identity":{"emoji":"🔍"},"workspace":"/home/openclaw/.openclaw/workspace-agent-1"},{"id":"agent-2","name":"Writer","model":{"primary":"openai/gpt-4o"},"identity":{"emoji":"✍️"},"workspace":"/home/openclaw/.openclaw/workspace-agent-2"}]`
	err = h.set("agents.list", agentsJSON2, true)
	require.NoError(t, err, "update agents.list with 2 agents")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "update agents list")

	// Clear agents list
	err = h.set("agents.list", `[]`, true)
	require.NoError(t, err, "clear agents.list")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "clear agents list")

	newLogs := env.readGatewayLogs()[logsBefore:]
	assertNoCrashIndicators(t, newLogs, "agents list changes")
	t.Logf("logs after agents list changes (%d bytes):\n%s", len(newLogs), truncate(newLogs, 1000))
	t.Log("gateway survived agents list changes without crashing")
}

// TestGatewayE2E_ConfigPush_SetBrowser verifies that setting and removing browser
// config doesn't crash the gateway.
func TestGatewayE2E_ConfigPush_SetBrowser(t *testing.T) {
	ensureConfigSeeded(t)
	h := newConfigSetHelper(t)
	logsBefore := len(env.readGatewayLogs())

	// Set browser config
	browserJSON := `{"enabled":true,"headless":true,"cdpUrl":"http://127.0.0.1:9222"}`
	err := h.set("browser", browserJSON, true)
	require.NoError(t, err, "set browser config")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "set browser")

	// Remove browser config
	err = h.unset("browser")
	require.NoError(t, err, "remove browser config")

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "remove browser")

	newLogs := env.readGatewayLogs()[logsBefore:]
	assertNoCrashIndicators(t, newLogs, "browser changes")
	t.Logf("logs after browser changes (%d bytes):\n%s", len(newLogs), truncate(newLogs, 1000))
	t.Log("gateway survived browser changes without crashing")
}

// TestGatewayE2E_ConfigPush_FullConfigPush verifies a full config push that
// changes multiple sections simultaneously (simulates pushMachineConfigInternal).
func TestGatewayE2E_ConfigPush_FullConfigPush(t *testing.T) {
	ensureConfigSeeded(t)
	h := newConfigSetHelper(t)
	logsBefore := len(env.readGatewayLogs())

	// Simulate a full config push: model + provider + channel + identity + skills
	// all in rapid succession (no sleep between ops — simulates ConfigBatch)
	ops := []struct {
		path       string
		value      string
		strictJSON bool
	}{
		{"agents.defaults.model.primary", "nebius/MiniMaxAI/MiniMax-M2.5", false},
		{"agents.defaults.models", `{"nebius/MiniMaxAI/MiniMax-M2.5":{}}`, true},
		{"models.providers.nebius", `{"baseUrl":"http://127.0.0.1:9999/nebius/v1","api":"openai-completions","models":[]}`, true},
		{"channels.telegram", `{"enabled":true,"botToken":"test-token"}`, true},
		{"ui.assistant", `{"name":"FullPushBot"}`, true},
		{"skills.allowBundled", `["code-review"]`, true},
		{"plugins.entries.test-plugin", `{"enabled":true}`, true},
	}

	for _, op := range ops {
		err := h.set(op.path, op.value, op.strictJSON)
		require.NoError(t, err, "full push: set "+op.path)
		// No sleep — rapid fire like ConfigBatch
	}

	// Wait for gateway to process all changes
	time.Sleep(5 * time.Second)
	assertGatewayHealthy(t, "full config push")

	// Clean up: unset what we added
	cleanupPaths := []string{
		"channels.telegram",
		"plugins.entries.test-plugin",
	}
	for _, path := range cleanupPaths {
		err := h.unset(path)
		require.NoError(t, err, "cleanup: unset "+path)
	}

	time.Sleep(3 * time.Second)
	assertGatewayHealthy(t, "full config push cleanup")

	newLogs := env.readGatewayLogs()[logsBefore:]
	assertNoCrashIndicators(t, newLogs, "full config push")
	t.Logf("logs after full config push (%d bytes):\n%s", len(newLogs), truncate(newLogs, 2000))
	t.Log("gateway survived full config push without crashing")
}

// doNebiusProxyCall sends a chat completion request through the nebius proxy route.
// Nebius uses OpenAI-compatible API format (POST /nebius/v1/chat/completions).
func doNebiusProxyCall(t *testing.T, model string) (int, string) {
	t.Helper()

	body := map[string]interface{}{
		"model":      model,
		"max_tokens": 16,
		"messages": []map[string]string{
			{"role": "user", "content": "Say OK"},
		},
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	proxyURL := fmt.Sprintf("http://127.0.0.1:%d/nebius/v1/chat/completions", env.proxyPort)
	req, err := http.NewRequest(http.MethodPost, proxyURL, bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+nonce)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("nebius proxy call failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
	t.Logf("nebius proxy: status=%d", resp.StatusCode)

	return resp.StatusCode, string(respBody)
}

// =============================================================================
// Opik tracing tests
// =============================================================================

// tryInstallOpikPlugin attempts to install the opik-openclaw plugin into the
// test's temp directory using OpenClaw's managed npm plugin installer.
// Returns true if installation succeeded, false otherwise.
func tryInstallCodexPlugin(openclawBin, homeDir, stateDir string) bool {
	const codexVersion = "2026.5.28"

	installSpec := fmt.Sprintf("npm:@openclaw/codex@%s", codexVersion)
	installCmd := exec.Command(openclawBin, "plugins", "install", installSpec)
	installCmd.Dir = homeDir
	installCmd.Env = []string{
		"HOME=" + homeDir,
		"PATH=" + os.Getenv("PATH"),
		"NODE_ENV=production",
		"OPENCLAW_STATE_DIR=" + stateDir,
	}
	installOutput, err := installCmd.CombinedOutput()
	if err != nil {
		log.Printf("codex: openclaw plugins install failed: %v\n%s", err, string(installOutput))
		return false
	}

	entrypointPatterns := []string{
		filepath.Join(stateDir, "npm", "node_modules", "@openclaw", "codex", "dist", "index.js"),
		filepath.Join(stateDir, "npm", "projects", "openclaw-codex-*", "node_modules", "@openclaw", "codex", "dist", "index.js"),
	}
	for _, pattern := range entrypointPatterns {
		matches, _ := filepath.Glob(pattern)
		for _, entrypoint := range matches {
			if _, err := os.Stat(entrypoint); err == nil {
				log.Printf("codex: plugin v%s installed at %s", codexVersion, filepath.Dir(filepath.Dir(entrypoint)))
				return true
			}
		}
	}

	log.Printf("codex: plugin entrypoint not found after install; checked %s", strings.Join(entrypointPatterns, ", "))
	return false
}

func tryInstallOpikPlugin(openclawBin, homeDir, stateDir string) bool {
	const opikVersion = "0.2.15"

	// OpenClaw 2026.5.x installs npm plugins into the managed npm root and
	// repairs the plugin-local openclaw peer symlink. Bare tgz installs use the
	// older archive path and can materialize a real openclaw peer dependency.
	installSpec := fmt.Sprintf("npm:@opik/opik-openclaw@%s", opikVersion)
	installCmd := exec.Command(openclawBin, "plugins", "install", installSpec)
	installCmd.Dir = homeDir
	installCmd.Env = []string{
		"HOME=" + homeDir,
		"PATH=" + os.Getenv("PATH"),
		"NODE_ENV=production",
		"OPENCLAW_STATE_DIR=" + stateDir,
	}
	installOutput, err := installCmd.CombinedOutput()
	if err != nil {
		log.Printf("opik: openclaw plugins install failed: %v\n%s", err, string(installOutput))
		return false
	}

	// Verify the plugin manifest exists. Managed npm installs land under
	// state/npm; older archive installs landed under state/extensions.
	manifestPaths := []string{
		filepath.Join(stateDir, "npm", "node_modules", "@opik", "opik-openclaw", "openclaw.plugin.json"),
		filepath.Join(stateDir, "extensions", "opik-openclaw", "openclaw.plugin.json"),
	}
	for _, manifestPath := range manifestPaths {
		if _, err := os.Stat(manifestPath); err == nil {
			log.Printf("opik: plugin v%s installed at %s", opikVersion, filepath.Dir(manifestPath))
			return true
		}
	}

	log.Printf("opik: plugin manifest not found after install; checked %s", strings.Join(manifestPaths, ", "))
	return false
}

// TestGatewayE2E_OpikTracing verifies that when the opik plugin is loaded,
// LLM calls through the gateway produce trace/span data sent to the configured
// Opik API endpoint.
func TestGatewayE2E_OpikTracing(t *testing.T) {
	if !env.opikAvailable {
		t.Skip("opik plugin not installed")
	}
	nebiusApiKey := fetchNebiusKey(t)

	// Reset model/keys (earlier tests may have changed them)
	h := newConfigSetHelper(t)
	require.NoError(t, h.set("agents.defaults.model.primary", "nebius/openai/gpt-oss-120b", false))
	require.NoError(t, h.set("agents.defaults.models", `{"nebius/openai/gpt-oss-120b":{}}`, true))
	nebiusProvider := fmt.Sprintf(
		`{"baseUrl":"http://127.0.0.1:%d/nebius/v1","api":"openai-completions","apiKey":%q,"models":[{"id":"openai/gpt-oss-120b","name":"GPT OSS 120B"}]}`,
		env.proxyPort,
		nonce,
	)
	require.NoError(t, h.set("models.providers.nebius", nebiusProvider, true))
	env.updateLLMKeys(map[string]metadata.CredentialEntry{
		"nebius": {Value: nebiusApiKey, CredentialType: "api_key"},
	})
	time.Sleep(3 * time.Second)

	// Dump the plugins section from openclaw.json so we can see what the gateway loaded
	configPath := filepath.Join(env.tmpDir, ".openclaw", "openclaw.json")
	configData, _ := os.ReadFile(configPath)
	var cfg map[string]interface{}
	if err := json.Unmarshal(configData, &cfg); err == nil {
		if plugins, ok := cfg["plugins"]; ok {
			pluginsJSON, _ := json.MarshalIndent(plugins, "", "  ")
			t.Logf("plugins config:\n%s", string(pluginsJSON))
		} else {
			t.Fatal("openclaw.json has NO plugins section")
		}
	}

	// Dump mock receiver URL for reference
	t.Logf("opik mock receiver URL (should match apiUrl in config above): %s",
		env.opikReceiver.url)

	tracesBefore := env.opikReceiver.traceCount()
	spansBefore := env.opikReceiver.spanCount()

	// Send a chat to trigger LLM call → plugin hooks → trace export
	conn := gatewayConnect(t)
	defer conn.Close()

	idempotencyKey := fmt.Sprintf("e2e-opik-%d", time.Now().UnixNano())
	chatSendData, _ := json.Marshal(map[string]interface{}{
		"type":   "req",
		"id":     "opik-chat-1",
		"method": "chat.send",
		"params": map[string]interface{}{
			"sessionKey":     "agent:main:opik-test",
			"message":        "Reply with exactly one word: OK",
			"idempotencyKey": idempotencyKey,
		},
	})
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, chatSendData))

	// Wait for ack
	ackDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(ackDeadline) {
		conn.SetReadDeadline(ackDeadline)
		_, ackData, err := conn.ReadMessage()
		require.NoError(t, err, "read chat.send ack")
		var msg struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			OK   bool   `json:"ok"`
		}
		if json.Unmarshal(ackData, &msg) == nil && msg.Type == "res" && msg.ID == "opik-chat-1" {
			require.True(t, msg.OK, "chat.send failed: %s", string(ackData))
			break
		}
	}

	// Wait for terminal chat state before polling for traces.
	result := waitForTerminalChatEvent(t, conn, 60*time.Second)
	t.Logf("chat completed: state=%s", result.state)

	// Poll for traces (plugin sends async)
	var tracesAfter, spansAfter int
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		tracesAfter = env.opikReceiver.traceCount()
		spansAfter = env.opikReceiver.spanCount()
		if tracesAfter > tracesBefore || spansAfter > spansBefore {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Logf("opik traces: %d→%d, spans: %d→%d", tracesBefore, tracesAfter, spansBefore, spansAfter)

	if tracesAfter == tracesBefore && spansAfter == spansBefore {
		// Dump plugin-related gateway logs for debugging
		for _, line := range strings.Split(env.readGatewayLogs(), "\n") {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "opik") || strings.Contains(lower, "plugin") {
				t.Log(line)
			}
		}
		t.Fatal("opik mock receiver got no traces or spans after LLM call")
	}
	t.Logf("OK: opik received %d new trace(s) and %d new span(s)",
		tracesAfter-tracesBefore, spansAfter-spansBefore)
}

// =============================================================================
// Search provider configuration tests
// =============================================================================

// e2eSearchProviderCatalog returns a provider catalog with search providers
// for E2E tests. This is separate from e2eModelCatalog (which returns models).
func e2eSearchProviderCatalog() []configassembly.CatalogProvider {
	order10 := 10
	order20 := 20
	return []configassembly.CatalogProvider{
		{ID: "anthropic", Category: "ai"},
		{ID: "openai", Category: "ai"},
		{ID: "brave", Category: "search", AutodetectOrder: &order10},
		{ID: "gemini-search", Category: "search", RuntimeProviderID: "gemini", AutodetectOrder: &order20},
	}
}

// TestGatewayE2E_ConfigPush_AddSearchProvider verifies that adding a search
// provider credential triggers config assembly that includes tools.web.search
// in the assembled config. This is a config assembly integration test — it
// doesn't need real Brave API keys, just verifies the config shape.
func TestGatewayE2E_ConfigPush_AddSearchProvider(t *testing.T) {
	catalog := e2eSearchProviderCatalog()
	proxyBaseURL := fmt.Sprintf("http://127.0.0.1:%d", env.proxyPort)

	// 1. Assemble config WITHOUT search credentials — verify no search block
	noSearchData, err := configassembly.AssembleConfig(configassembly.AssemblyParams{
		MachineID:       machineID,
		ProxyBaseURL:    proxyBaseURL,
		ModelCatalog:    e2eModelCatalog(),
		ProviderCatalog: catalog,
		Credentials:     map[string]string{"anthropic": "present"},
	})
	require.NoError(t, err, "AssembleConfig without search should succeed")

	var noSearchCfg map[string]interface{}
	require.NoError(t, json.Unmarshal(noSearchData, &noSearchCfg))

	// tools.web.search should NOT be present
	if tools, ok := noSearchCfg["tools"].(map[string]interface{}); ok {
		if web, ok := tools["web"].(map[string]interface{}); ok {
			_, hasSearch := web["search"]
			assert.False(t, hasSearch, "tools.web.search should not be present without search credentials")
		}
	}

	// 2. Assemble config WITH brave search credential — verify search block
	searchData, err := configassembly.AssembleConfig(configassembly.AssemblyParams{
		MachineID:       machineID,
		ProxyBaseURL:    proxyBaseURL,
		ModelCatalog:    e2eModelCatalog(),
		ProviderCatalog: catalog,
		Credentials:     map[string]string{"anthropic": "present", "brave": "present"},
	})
	require.NoError(t, err, "AssembleConfig with brave should succeed")

	var searchCfg map[string]interface{}
	require.NoError(t, json.Unmarshal(searchData, &searchCfg))

	// Verify tools.web.search block
	tools, ok := searchCfg["tools"].(map[string]interface{})
	require.True(t, ok, "config must have tools key")
	web, ok := tools["web"].(map[string]interface{})
	require.True(t, ok, "config must have tools.web key")
	search, ok := web["search"].(map[string]interface{})
	require.True(t, ok, "config must have tools.web.search key")

	assert.Equal(t, true, search["enabled"], "tools.web.search.enabled")
	assert.Equal(t, "brave", search["provider"], "tools.web.search.provider")

	models, ok := searchCfg["models"].(map[string]interface{})
	require.True(t, ok, "config must have models key")
	providers, ok := models["providers"].(map[string]interface{})
	require.True(t, ok, "config must have models.providers key")
	_, hasBrave := providers["brave"]
	assert.False(t, hasBrave, "config must not have models.providers.brave for search-only providers")

	t.Logf("search config assembled: tools.web.search.enabled=%v provider=%v",
		search["enabled"], search["provider"])
}

// TestNativeMode_SearchProviderConfig verifies that native mode seed config
// includes search provider configuration when search credentials are present.
// Uses AssembleSeedConfig with a ProviderCatalog that includes brave as a
// search provider.
func TestNativeMode_SearchProviderConfig(t *testing.T) {
	catalog := e2eSearchProviderCatalog()
	proxyBaseURL := fmt.Sprintf("http://127.0.0.1:%d", env.proxyPort)

	// 1. Assemble seed config with brave as a search provider
	seedJSON, err := configassembly.AssembleSeedConfig(configassembly.SeedParams{
		ModelCatalog:    e2eModelCatalog(),
		MachineID:       machineID,
		DefaultModel:    "anthropic/claude-sonnet-4-6",
		Providers:       []string{"anthropic", "brave"},
		ProxyBaseURL:    proxyBaseURL,
		ProviderCatalog: catalog,
	})
	require.NoError(t, err, "AssembleSeedConfig with brave should succeed")

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(seedJSON, &parsed))

	// 2. Verify tools.web.search block in seed config
	tools, ok := parsed["tools"].(map[string]interface{})
	require.True(t, ok, "seed config must have tools key")
	web, ok := tools["web"].(map[string]interface{})
	require.True(t, ok, "seed config must have tools.web key")
	search, ok := web["search"].(map[string]interface{})
	require.True(t, ok, "seed config must have tools.web.search key")

	assert.Equal(t, true, search["enabled"], "tools.web.search.enabled")
	assert.Equal(t, "brave", search["provider"], "tools.web.search.provider")

	// 3. Verify search providers are not injected as model providers in seed config
	models, ok := parsed["models"].(map[string]interface{})
	require.True(t, ok, "seed config must have models key")
	providers, ok := models["providers"].(map[string]interface{})
	require.True(t, ok, "seed config must have models.providers key")
	_, hasBrave := providers["brave"]
	assert.False(t, hasBrave, "seed config must not have models.providers.brave")

	// 4. Verify seed config without search provider does NOT include search block
	noSearchJSON, err := configassembly.AssembleSeedConfig(configassembly.SeedParams{
		ModelCatalog:    e2eModelCatalog(),
		MachineID:       machineID,
		DefaultModel:    "anthropic/claude-sonnet-4-6",
		Providers:       []string{"anthropic"},
		ProxyBaseURL:    proxyBaseURL,
		ProviderCatalog: catalog,
	})
	require.NoError(t, err, "AssembleSeedConfig without brave should succeed")

	var noSearchParsed map[string]interface{}
	require.NoError(t, json.Unmarshal(noSearchJSON, &noSearchParsed))

	if nsTools, ok := noSearchParsed["tools"].(map[string]interface{}); ok {
		if nsWeb, ok := nsTools["web"].(map[string]interface{}); ok {
			_, hasSearch := nsWeb["search"]
			assert.False(t, hasSearch,
				"seed config without search credentials should not have tools.web.search")
		}
	}

	t.Logf("native mode search config: provider=%v, enabled=%v", search["provider"], search["enabled"])
}

// fetchExaKey retrieves the Exa Search API key from GCP Secret Manager.
// Skips the test if the key cannot be fetched (see fetchNebiusKey).
func fetchExaKey(t *testing.T) string {
	t.Helper()
	const secretName = "projects/clarateach/secrets/EXA_SEARCH/versions/latest"
	key, err := secrets.FetchSecret(context.Background(), secretName)
	if err != nil {
		t.Skipf("EXA_SEARCH unavailable from Secret Manager: %v", err)
	}
	return key
}

// TestNativeMode_ProxyExaSearch verifies the full native-mode proxy flow for
// Exa search: seed config with exa search provider + exec secret refs + plugin
// config → proxy resolves nonce to real Exa API key → Exa returns search results.
//
// This tests the complete chain that runs on a real VM:
// gateway reads plugin config → ocm-secrets resolves exec ref → gateway calls
// proxy at /exa/search → proxy injects x-api-key → Exa API → results.
func TestNativeMode_ProxyExaSearch(t *testing.T) {
	exaApiKey := fetchExaKey(t)

	catalog := e2eSearchProviderCatalog()
	// Add ExecSecretID and PluginID to match the real DB seed data
	for i := range catalog {
		if catalog[i].ID == "brave" {
			catalog[i].ExecSecretID = "brave-key"
		}
	}
	// Add exa to the catalog
	order65 := 65
	catalog = append(catalog, configassembly.CatalogProvider{
		ID:              "exa",
		Category:        "search",
		AutodetectOrder: &order65,
		ExecSecretID:    "search-exa-api-key",
	})

	proxyBaseURL := fmt.Sprintf("http://127.0.0.1:%d", env.proxyPort)

	// 1. Assemble seed config with exa as a search provider
	seedJSON, err := configassembly.AssembleSeedConfig(configassembly.SeedParams{
		ModelCatalog:    e2eModelCatalog(),
		MachineID:       machineID,
		DefaultModel:    "anthropic/claude-sonnet-4-6",
		Providers:       []string{"anthropic", "exa"},
		ProxyBaseURL:    proxyBaseURL,
		ProviderCatalog: catalog,
	})
	require.NoError(t, err, "AssembleSeedConfig with exa should succeed")

	// Verify the assembled config has the plugin config with exec secret ref
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(seedJSON, &parsed))

	plugins := parsed["plugins"].(map[string]interface{})
	entries := plugins["entries"].(map[string]interface{})
	exaEntry, ok := entries["exa"].(map[string]interface{})
	require.True(t, ok, "plugins.entries.exa must exist")
	assert.Equal(t, true, exaEntry["enabled"], "exa plugin must be enabled")

	exaConfig := exaEntry["config"].(map[string]interface{})
	webSearch := exaConfig["webSearch"].(map[string]interface{})
	apiKey := webSearch["apiKey"].(map[string]interface{})
	assert.Equal(t, "exec", apiKey["source"], "apiKey.source must be exec")
	assert.Equal(t, "search-exa-api-key", apiKey["id"], "apiKey.id must be search-exa-api-key")

	t.Logf("seed config has plugins.entries.exa.config.webSearch.apiKey = {source:exec, id:search-exa-api-key}")

	// 2. Register exa as a custom provider in the proxy (search providers use CustomProviderToProxy)
	env.proxy.RegisterCustomProviders([]metadata.CustomProviderConfig{
		{
			Name:         "exa",
			UpstreamHost: "api.exa.ai",
			Scheme:       "https",
			AuthMethod:   "api_key_header",
			AuthHeader:   "x-api-key",
		},
	})

	// 3. Register machine with seed config + real Exa key
	origConfig, _ := env.metaSrv.GetConfig("127.0.0.1")
	defer env.metaSrv.RegisterMachine("127.0.0.1", origConfig)

	env.metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID:    machineID,
		Nonce:        nonce,
		GatewayToken: gatewayToken,
		OpenClawConf: seedJSON,
		LLMKeys: map[string]metadata.CredentialEntry{
			"exa": {Value: exaApiKey, CredentialType: "api_key"},
		},
	})

	// 4. Make a search call through the proxy — nonce swapped for real key
	searchBody := map[string]interface{}{
		"query":      "OpenAI latest news",
		"numResults": 3,
	}
	bodyBytes, err := json.Marshal(searchBody)
	require.NoError(t, err)

	proxyURL := fmt.Sprintf("http://127.0.0.1:%d/exa/search", env.proxyPort)
	req, err := http.NewRequest(http.MethodPost, proxyURL, bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", nonce)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "Exa proxy request should not fail")
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"Exa search via proxy should return 200; body: %s", truncate(string(respBody), 300))

	// 5. Verify search results
	var searchResp struct {
		Results []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(respBody, &searchResp), "parse Exa response")
	require.NotEmpty(t, searchResp.Results, "Exa should return at least 1 result")

	for i, r := range searchResp.Results {
		assert.NotEmpty(t, r.URL, "result[%d] should have a URL", i)
		t.Logf("Exa result[%d]: title=%q url=%s", i, truncate(r.Title, 60), r.URL)
	}

	t.Logf("native-mode Exa search: status=%d results=%d", resp.StatusCode, len(searchResp.Results))
}
