# Native Config Mode Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the OpenClaw fork by using vanilla OpenClaw with a native exec secret provider, behind a global feature flag.

**Architecture:** A new `ocm-secrets` Go binary bridges OpenClaw's exec provider protocol to the existing metadata service. The init script writes a seed `openclaw.json` at boot instead of using the fork's HTTP config source. The LiteLLM proxy continues to handle real API keys. A global `OCM_CONFIG_MODE` env var switches between fork and native paths.

**Tech Stack:** Go (ocm-secrets binary), Bash (init script), Docker (rootfs), OpenClaw exec provider protocol v1

**Spec:** `docs/superpowers/specs/2026-03-18-native-config-mode-design.md`

**Scope:** This plan covers the backend, infrastructure, and init script changes (Tasks 1-9). Frontend dashboard UI changes (spec section 7: converting Skills/Plugins/Agents tabs to exec-based convenience helpers) and custom registry configuration (spec section 8: `.clawhubrc`, `.npmrc` defaults) are deferred to a follow-up plan after the backend is validated end-to-end.

---

## File Map

| Action | File | Responsibility |
|--------|------|---------------|
| Create | `backend/cmd/ocm-secrets/main.go` | Exec secret provider binary — reads stdin, calls metadata, returns nonces |
| Create | `backend/cmd/ocm-secrets/main_test.go` | Unit tests for protocol handling |
| Modify | `backend/internal/configassembly/assembler.go` | Add `AssembleSeedConfig()` for native mode (minimal seed generation) |
| Modify | `backend/internal/configassembly/assembler_test.go` | Tests for seed config generation |
| Modify | `backend/internal/config/config.go` | Add `ConfigMode` field (read from `OCM_CONFIG_MODE` env var) |
| Modify | `backend/internal/machines/runtime.go` | Branch on ConfigMode for assembly path |
| Modify | `scripts/init-openclaw.sh` | Add native mode startup path (write seed config, skip config watcher) |
| Modify | `rootfs/Dockerfile.openclaw` | Add native mode OpenClaw install path (from npm) |
| Modify | `scripts/build-rootfs.sh` | Inject `ocm-secrets` binary into rootfs |
| Modify | `Makefile` | Add `build-ocm-secrets` target, update `build-rootfs` deps |

---

## Task 1: `ocm-secrets` Binary

**Files:**
- Create: `backend/cmd/ocm-secrets/main.go`
- Create: `backend/cmd/ocm-secrets/main_test.go`

- [ ] **Step 1: Write failing tests for the exec provider protocol**

Create `backend/cmd/ocm-secrets/main_test.go` with table-driven tests:

```go
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestResolveSecrets(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		metadataBody   map[string]string
		metadataStatus int
		wantValues     map[string]interface{}
		wantErrors     map[string]interface{}
		wantExitCode   int
	}{
		{
			name:  "single key found",
			input: `{"protocolVersion":1,"provider":"ocm","ids":["anthropic-key"]}`,
			metadataBody: map[string]string{
				"anthropic-key": "nonce-abc123",
			},
			metadataStatus: 200,
			wantValues:     map[string]interface{}{"anthropic-key": "nonce-abc123"},
			wantExitCode:   0,
		},
		{
			name:  "multiple keys found",
			input: `{"protocolVersion":1,"provider":"ocm","ids":["anthropic-key","openai-key"]}`,
			metadataBody: map[string]string{
				"anthropic-key": "nonce-abc",
				"openai-key":    "nonce-def",
			},
			metadataStatus: 200,
			wantValues: map[string]interface{}{
				"anthropic-key": "nonce-abc",
				"openai-key":    "nonce-def",
			},
			wantExitCode: 0,
		},
		{
			name:  "key not found in metadata",
			input: `{"protocolVersion":1,"provider":"ocm","ids":["missing-key"]}`,
			metadataBody:   map[string]string{},
			metadataStatus: 200,
			wantValues:     map[string]interface{}{},
			wantErrors:     map[string]interface{}{"missing-key": map[string]interface{}{"message": "key not configured"}},
			wantExitCode:   0,
		},
		{
			name:  "partial: some found, some missing",
			input: `{"protocolVersion":1,"provider":"ocm","ids":["anthropic-key","missing-key"]}`,
			metadataBody: map[string]string{
				"anthropic-key": "nonce-abc",
			},
			metadataStatus: 200,
			wantValues:     map[string]interface{}{"anthropic-key": "nonce-abc"},
			wantErrors:     map[string]interface{}{"missing-key": map[string]interface{}{"message": "key not configured"}},
			wantExitCode:   0,
		},
		{
			name:           "metadata server error",
			input:          `{"protocolVersion":1,"provider":"ocm","ids":["anthropic-key"]}`,
			metadataBody:   nil,
			metadataStatus: 500,
			wantExitCode:   1,
		},
		{
			name:         "invalid JSON input",
			input:        `not json`,
			wantExitCode: 1,
		},
		{
			name:         "empty ids array",
			input:        `{"protocolVersion":1,"provider":"ocm","ids":[]}`,
			metadataBody: map[string]string{},
			metadataStatus: 200,
			wantValues:   map[string]interface{}{},
			wantExitCode: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up metadata mock server
			var srv *httptest.Server
			if tt.metadataStatus > 0 {
				srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/secrets" {
						t.Errorf("unexpected path: %s", r.URL.Path)
						http.NotFound(w, r)
						return
					}
					w.WriteHeader(tt.metadataStatus)
					if tt.metadataBody != nil {
						json.NewEncoder(w).Encode(tt.metadataBody)
					}
				}))
				defer srv.Close()
			}

			metadataURL := "http://unreachable:9999"
			if srv != nil {
				metadataURL = srv.URL
			}

			stdin := bytes.NewBufferString(tt.input)
			var stdout bytes.Buffer

			exitCode := run(stdin, &stdout, metadataURL)

			if exitCode != tt.wantExitCode {
				t.Fatalf("exit code = %d, want %d (stdout: %s)", exitCode, tt.wantExitCode, stdout.String())
			}
			if tt.wantExitCode != 0 {
				return // don't check output for error cases
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
				t.Fatalf("invalid JSON output: %v\n%s", err, stdout.String())
			}

			if v, ok := resp["protocolVersion"]; !ok || v != float64(1) {
				t.Errorf("protocolVersion = %v, want 1", v)
			}

			gotValues, _ := resp["values"].(map[string]interface{})
			for k, want := range tt.wantValues {
				if got := gotValues[k]; got != want {
					t.Errorf("values[%s] = %v, want %v", k, got, want)
				}
			}

			gotErrors, _ := resp["errors"].(map[string]interface{})
			if tt.wantErrors != nil {
				for k := range tt.wantErrors {
					if _, ok := gotErrors[k]; !ok {
						t.Errorf("errors[%s] missing, want error entry", k)
					}
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
cd backend && go test ./cmd/ocm-secrets/ -v -count=1
```

Expected: compilation error (package/functions don't exist yet)

- [ ] **Step 3: Implement `ocm-secrets`**

Create `backend/cmd/ocm-secrets/main.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const defaultMetadataURL = "http://169.254.169.253"

type execRequest struct {
	ProtocolVersion int      `json:"protocolVersion"`
	Provider        string   `json:"provider"`
	IDs             []string `json:"ids"`
}

type execResponse struct {
	ProtocolVersion int                    `json:"protocolVersion"`
	Values          map[string]interface{} `json:"values"`
	Errors          map[string]interface{} `json:"errors,omitempty"`
}

func run(stdin io.Reader, stdout io.Writer, metadataURL string) int {
	// Read request from stdin
	input, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocm-secrets: read stdin: %v\n", err)
		return 1
	}

	var req execRequest
	if err := json.Unmarshal(input, &req); err != nil {
		fmt.Fprintf(os.Stderr, "ocm-secrets: invalid JSON input: %v\n", err)
		return 1
	}

	// Fetch all secrets from metadata service (single bulk call)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(metadataURL + "/v1/secrets")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocm-secrets: metadata request failed: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "ocm-secrets: metadata returned %d\n", resp.StatusCode)
		return 1
	}

	var allSecrets map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&allSecrets); err != nil {
		fmt.Fprintf(os.Stderr, "ocm-secrets: decode metadata response: %v\n", err)
		return 1
	}

	// Build response: pick requested IDs from the secrets map
	out := execResponse{
		ProtocolVersion: 1,
		Values:          make(map[string]interface{}),
	}
	for _, id := range req.IDs {
		if val, ok := allSecrets[id]; ok {
			out.Values[id] = val
		} else {
			if out.Errors == nil {
				out.Errors = make(map[string]interface{})
			}
			out.Errors[id] = map[string]string{"message": "key not configured"}
		}
	}

	if err := json.NewEncoder(stdout).Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "ocm-secrets: encode response: %v\n", err)
		return 1
	}
	return 0
}

func main() {
	metadataURL := os.Getenv("OCM_METADATA_URL")
	if metadataURL == "" {
		metadataURL = defaultMetadataURL
	}
	os.Exit(run(os.Stdin, os.Stdout, metadataURL))
}
```

- [ ] **Step 4: Run tests — verify they pass**

```bash
cd backend && go test ./cmd/ocm-secrets/ -v -count=1
```

Expected: all 7 tests PASS

- [ ] **Step 5: Commit**

```bash
git add backend/cmd/ocm-secrets/
git commit -m "feat: add ocm-secrets exec provider binary

Bridges OpenClaw's native exec secret provider protocol (v1) to the
OCM metadata service. Fetches all secrets in one bulk call to
/v1/secrets, picks requested IDs, returns protocol-compliant JSON.

This binary runs inside the VM, owned by root, and only returns
nonce tokens — never real API keys."
```

---

## Task 2: Seed Config Assembly

**Files:**
- Modify: `backend/internal/configassembly/assembler.go`
- Modify: `backend/internal/configassembly/assembler_test.go`

- [ ] **Step 1: Write failing tests for `AssembleSeedConfig`**

Add to `backend/internal/configassembly/assembler_test.go`:

```go
// mustGetNestedMap wraps getNestedMap with t.Fatal on missing keys.
// The existing getNestedMap signature is: getNestedMap(m, keys...) (map, bool)
func mustGetNestedMap(t *testing.T, m map[string]interface{}, keys ...string) map[string]interface{} {
	t.Helper()
	result, ok := getNestedMap(m, keys...)
	if !ok {
		t.Fatalf("missing nested key path: %v", keys)
	}
	return result
}

func TestAssembleSeedConfig_BasicStructure(t *testing.T) {
	params := SeedParams{
		DefaultModel: "anthropic/claude-sonnet-4-6",
		Providers:    []string{"anthropic"},
		ProxyBaseURL: "http://192.168.100.1:4000",
		MachineID:    "m-test",
	}
	data, err := AssembleSeedConfig(params)
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Check exec provider defined
	secrets := mustGetNestedMap(t, cfg, "secrets", "providers", "ocm")
	if secrets["source"] != "exec" {
		t.Errorf("secrets.providers.ocm.source = %v, want exec", secrets["source"])
	}
	if secrets["command"] != "/usr/local/bin/ocm-secrets" {
		t.Errorf("secrets.providers.ocm.command = %v, want /usr/local/bin/ocm-secrets", secrets["command"])
	}

	// Check gateway defaults
	gw := mustGetNestedMap(t, cfg, "gateway")
	if gw["mode"] != "local" {
		t.Errorf("gateway.mode = %v, want local", gw["mode"])
	}
	auth := mustGetNestedMap(t, cfg, "gateway", "auth")
	if auth["mode"] != "token" {
		t.Errorf("gateway.auth.mode = %v, want token", auth["mode"])
	}

	// Check provider with exec ref
	anthropic := mustGetNestedMap(t, cfg, "models", "providers", "anthropic")
	if anthropic["baseUrl"] != "http://192.168.100.1:4000/anthropic" {
		t.Errorf("anthropic.baseUrl = %v", anthropic["baseUrl"])
	}
	apiKey := anthropic["apiKey"].(map[string]interface{})
	if apiKey["source"] != "exec" || apiKey["provider"] != "ocm" || apiKey["id"] != "anthropic-key" {
		t.Errorf("anthropic.apiKey = %v", apiKey)
	}

	// Check default model
	agents := mustGetNestedMap(t, cfg, "agents", "defaults")
	model := agents["model"].(map[string]interface{})
	if model["primary"] != "anthropic/claude-sonnet-4-6" {
		t.Errorf("agents.defaults.model.primary = %v", model["primary"])
	}
}

func TestAssembleSeedConfig_NebiusAlwaysIncluded(t *testing.T) {
	params := SeedParams{
		DefaultModel: "anthropic/claude-sonnet-4-6",
		Providers:    []string{"anthropic"}, // no nebius in user providers
		ProxyBaseURL: "http://192.168.100.1:4000",
		MachineID:    "m-test",
	}
	data, err := AssembleSeedConfig(params)
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	var cfg map[string]interface{}
	json.Unmarshal(data, &cfg)

	// Nebius must always be present for platform models
	nebius := mustGetNestedMap(t, cfg, "models", "providers", "nebius")
	if nebius["baseUrl"] != "http://192.168.100.1:4000/nebius" {
		t.Errorf("nebius.baseUrl = %v", nebius["baseUrl"])
	}
}

func TestAssembleSeedConfig_PlatformModelMapping(t *testing.T) {
	// Test all entries in platformModelMap to avoid hardcoding values
	// that could drift from the source of truth in assembler.go.
	// platformModelMap is package-level in assembler.go (unexported),
	// so we test via the public AssembleSeedConfig interface.
	//
	// Known mappings (from assembler.go:12-15):
	//   "deepseek/deepseek-r1" → "deepseek-ai/DeepSeek-R1-0528"
	//   "deepseek/deepseek-v3" → "deepseek-ai/DeepSeek-V3-0324"
	//   "openai/gpt-oss-20b"  → "openai/gpt-oss-20b"
	tests := []struct {
		input  string
		mapped string
	}{
		{"deepseek/deepseek-r1", "deepseek-ai/DeepSeek-R1-0528"},
		{"deepseek/deepseek-v3", "deepseek-ai/DeepSeek-V3-0324"},
		{"openai/gpt-oss-20b", "openai/gpt-oss-20b"},
		{"anthropic/claude-sonnet-4-6", "anthropic/claude-sonnet-4-6"}, // unmapped, passthrough
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			params := SeedParams{
				DefaultModel: tt.input,
				Providers:    []string{},
				ProxyBaseURL: "http://192.168.100.1:4000",
				MachineID:    "m-test",
			}
			data, err := AssembleSeedConfig(params)
			if err != nil {
				t.Fatalf("AssembleSeedConfig: %v", err)
			}

			var cfg map[string]interface{}
			json.Unmarshal(data, &cfg)

			agents := mustGetNestedMap(t, cfg, "agents", "defaults")
			model := agents["model"].(map[string]interface{})
			if model["primary"] != tt.mapped {
				t.Errorf("model %q not mapped correctly: got %v, want %v", tt.input, model["primary"], tt.mapped)
			}
		})
	}
}

func TestAssembleSeedConfig_ExcludesUnconfiguredProviders(t *testing.T) {
	params := SeedParams{
		DefaultModel: "anthropic/claude-sonnet-4-6",
		Providers:    []string{"anthropic"}, // only anthropic configured
		ProxyBaseURL: "http://192.168.100.1:4000",
		MachineID:    "m-test",
	}
	data, err := AssembleSeedConfig(params)
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	var cfg map[string]interface{}
	json.Unmarshal(data, &cfg)

	providers := mustGetNestedMap(t, cfg, "models", "providers")
	// anthropic and nebius should be present
	if _, ok := providers["anthropic"]; !ok {
		t.Error("anthropic should be present")
	}
	if _, ok := providers["nebius"]; !ok {
		t.Error("nebius should always be present")
	}
	// openai, google, openrouter should NOT be present
	for _, p := range []string{"openai", "google", "openrouter"} {
		if _, ok := providers[p]; ok {
			t.Errorf("%s should NOT be present when not in Providers list", p)
		}
	}
}

func TestAssembleSeedConfig_OpikPlugin(t *testing.T) {
	params := SeedParams{
		DefaultModel: "anthropic/claude-sonnet-4-6",
		ProxyBaseURL: "http://192.168.100.1:4000",
		MachineID:    "m-test",
	}
	data, err := AssembleSeedConfig(params)
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	var cfg map[string]interface{}
	json.Unmarshal(data, &cfg)

	opik := mustGetNestedMap(t, cfg, "plugins", "entries", "opik-openclaw")
	if opik["enabled"] != true {
		t.Errorf("opik not enabled")
	}
	opikCfg := opik["config"].(map[string]interface{})
	if opikCfg["projectName"] != "admin" {
		t.Errorf("opik projectName = %v", opikCfg["projectName"])
	}
}
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
cd backend && go test ./internal/configassembly/ -run TestAssembleSeedConfig -v -count=1
```

Expected: compilation error (`SeedParams` and `AssembleSeedConfig` don't exist)

- [ ] **Step 3: Implement `AssembleSeedConfig`**

Add to `backend/internal/configassembly/assembler.go`:

```go
// SeedParams contains inputs for generating the native-mode seed config.
type SeedParams struct {
	MachineID    string
	DefaultModel string   // e.g. "anthropic/claude-sonnet-4-6" — platform mapping applied
	Providers    []string // provider names the user has configured keys for
	ProxyBaseURL string   // e.g. "http://192.168.100.1:4000"
}

// providerExecIDs maps provider names to their exec secret ref IDs.
var providerExecIDs = map[string]string{
	"anthropic":  "anthropic-key",
	"openai":     "openai-key",
	"google":     "google-key",
	"openrouter": "openrouter-key",
	"nebius":     "nebius-key",
}

// AssembleSeedConfig generates the seed openclaw.json for native config mode.
// This config is written to disk at boot and owned by the user after that.
func AssembleSeedConfig(params SeedParams) ([]byte, error) {
	// Exec secret provider
	secrets := map[string]interface{}{
		"providers": map[string]interface{}{
			"ocm": map[string]interface{}{
				"source":            "exec",
				"command":           "/usr/local/bin/ocm-secrets",
				"allowInsecurePath": true,
			},
		},
	}

	// Gateway defaults (same as platform defaults, but written to file)
	gateway := map[string]interface{}{
		"mode": "local",
		"auth": map[string]interface{}{"mode": "token"},
		"controlUi": map[string]interface{}{
			"enabled":                      true,
			"allowInsecureAuth":            true,
			"dangerouslyDisableDeviceAuth": true,
			"allowedOrigins":               []string{"*"},
		},
		"reload": map[string]interface{}{"mode": "hot"},
		"nodes": map[string]interface{}{
			"denyCommands": []string{
				"camera.snap", "camera.clip", "screen.record",
				"calendar.add", "contacts.add", "reminders.add",
			},
		},
	}

	// Provider configs with exec secret refs
	providerSet := make(map[string]bool)
	for _, p := range params.Providers {
		providerSet[p] = true
	}
	providerSet["nebius"] = true // always include nebius for platform models

	providerConfigs := map[string]interface{}{}
	for provider := range providerSet {
		execID, ok := providerExecIDs[provider]
		if !ok {
			continue
		}
		providerConfigs[provider] = map[string]interface{}{
			"baseUrl": params.ProxyBaseURL + "/" + provider,
			"apiKey": map[string]interface{}{
				"source":   "exec",
				"provider": "ocm",
				"id":       execID,
			},
		}
	}

	// Apply platform model mapping
	defaultModel := params.DefaultModel
	if mapped, ok := platformModelMap[defaultModel]; ok {
		defaultModel = mapped
	}

	// Opik plugin
	plugins := map[string]interface{}{
		"entries": map[string]interface{}{
			"opik-openclaw": map[string]interface{}{
				"enabled": true,
				"config": map[string]interface{}{
					"enabled":       true,
					"projectName":   "admin",
					"workspaceName": "openclawmachines",
					"tags":          []string{"openclaw", params.MachineID},
				},
			},
		},
	}

	result := map[string]interface{}{
		"secrets": secrets,
		"gateway": gateway,
		"models":  map[string]interface{}{"providers": providerConfigs},
		"agents": map[string]interface{}{
			"defaults": map[string]interface{}{
				"model":     map[string]interface{}{"primary": defaultModel},
				"workspace": "/home/openclaw/.openclaw/workspace",
			},
		},
		"commands": map[string]interface{}{
			"native":       "auto",
			"nativeSkills": "auto",
			"restart":      true,
			"ownerDisplay": "raw",
		},
		"session": map[string]interface{}{
			"dmScope": "per-channel-peer",
		},
		"plugins": plugins,
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal seed config: %w", err)
	}

	slog.Info("config.seed.assembled",
		"machine_id", params.MachineID,
		"default_model", defaultModel,
		"provider_count", len(providerConfigs),
		"config_bytes", len(data),
	)

	return data, nil
}
```

- [ ] **Step 4: Run tests — verify they pass**

```bash
cd backend && go test ./internal/configassembly/ -run TestAssembleSeedConfig -v -count=1
```

Expected: all 5 tests PASS

- [ ] **Step 5: Verify existing tests still pass**

```bash
cd backend && go test ./internal/configassembly/ -v -count=1
```

Expected: all tests PASS (old + new)

- [ ] **Step 6: Commit**

```bash
git add backend/internal/configassembly/assembler.go backend/internal/configassembly/assembler_test.go
git commit -m "feat: add AssembleSeedConfig for native config mode

Generates a seed openclaw.json that uses OpenClaw's native exec
secret provider instead of the fork's HTTP config source. Includes
gateway defaults, provider proxy URLs with exec refs, Opik plugin,
and platform model mapping. Nebius always included for platform models."
```

---

## Task 3: Config Mode Flag + Runtime Wiring

**Files:**
- Modify: `backend/internal/config/config.go`
- Modify: `backend/internal/machines/runtime.go`

- [ ] **Step 1: Add `ConfigMode` to server config**

In `backend/internal/config/config.go`, add to the `Config` struct and `Load()`:

```go
// In Config struct:
ConfigMode string // "fork" (default) or "native"

// In Load():
cfg.ConfigMode = os.Getenv("OCM_CONFIG_MODE")
if cfg.ConfigMode == "" {
    cfg.ConfigMode = "fork"
}
```

- [ ] **Step 2: Branch config assembly in runtime**

In `backend/internal/machines/runtime.go`:

1. Add `configMode string` field to the `RuntimeService` struct (around line 61, near `opikAPIKey`).
2. Add `ConfigMode string` field to the `RuntimeConfig` struct (around line 79, near `OpikAPIKey`).
3. Set it in `NewRuntimeService()` (around line 104): `configMode: cfg.ConfigMode,`
4. In `server.go`, pass `cfg.ConfigMode` into `RuntimeConfig{...}` where the service is created.
3. In the `start()` method (line 126, not the public `Start()`), find where `rs.ConfigAssembler` is called (around line 418-421). The method has named returns `(host *store.Host, vmIP string, err error)`, so `err` is already in scope. Replace the existing `:=` declaration with a `var` and branch:

```go
var openClawConf []byte
if rs.configMode == "native" {
    // Native mode: generate seed config with exec secret provider
    providerNames := make([]string, 0, len(llmKeys))
    for name := range llmKeys {
        providerNames = append(providerNames, name)
    }
    openClawConf, err = configassembly.AssembleSeedConfig(configassembly.SeedParams{
        MachineID:    machine.ID,
        DefaultModel: machine.DefaultModel,
        Providers:    providerNames,
        ProxyBaseURL: rs.proxyBaseURL,
    })
    if err != nil {
        return nil, "", fmt.Errorf("assemble seed config: %w", err)
    }
} else {
    // Fork mode: full config assembly (existing code)
    openClawConf, _, _, err = rs.ConfigAssembler(ctx, machine.ID, accountID, "m-"+machine.Slug+".openclawmachines.com", browserVMIP)
    if err != nil {
        return nil, "", fmt.Errorf("assemble config: %w", err)
    }
}
```

Note: `rs.proxyBaseURL` is already set from `RuntimeConfig.ProxyBaseURL` (line 102). Do not re-declare `err` — it's a named return. The seed config is stored in `MachineConfig.OpenClawConf` and served via `/v1/config` — the init script writes it to disk in native mode.

- [ ] **Step 3: Run existing tests**

```bash
cd backend && go test ./internal/machines/ -v -count=1
```

Expected: all existing tests PASS

- [ ] **Step 4: Commit**

```bash
git add backend/internal/config/config.go backend/internal/machines/runtime.go
git commit -m "feat: add OCM_CONFIG_MODE flag and runtime branching

When OCM_CONFIG_MODE=native, uses AssembleSeedConfig instead of the
full config assembly pipeline. The seed config is still passed through
the metadata server — the init script inside the VM decides the
startup path based on config_mode metadata."
```

---

## Task 4: Build Pipeline — `ocm-secrets` Binary

**Files:**
- Modify: `Makefile`
- Modify: `scripts/build-rootfs.sh`

- [ ] **Step 1: Add Makefile target for `ocm-secrets`**

Add to Makefile (near `build-agent` and `build-authproxy` targets):

```makefile
build-ocm-secrets:
	@echo "Building ocm-secrets..."
	cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
		-ldflags="-s -w" \
		-o ocm-secrets ./cmd/ocm-secrets
	@echo "Built: backend/ocm-secrets"
```

Update `build-rootfs` target dependencies to include `build-ocm-secrets`.

- [ ] **Step 2: Inject `ocm-secrets` into rootfs in `build-rootfs.sh`**

After the existing agent and authproxy injection (around line 132), add:

```bash
# Inject ocm-secrets binary (exec secret provider for native config mode)
OCM_SECRETS_BIN="${OCM_SECRETS_BIN:-backend/ocm-secrets}"
if [ -f "$OCM_SECRETS_BIN" ]; then
    sudo cp "$OCM_SECRETS_BIN" "${TMP_DIR}/mnt/usr/local/bin/ocm-secrets"
    sudo chmod 755 "${TMP_DIR}/mnt/usr/local/bin/ocm-secrets"
    sudo chown root:root "${TMP_DIR}/mnt/usr/local/bin/ocm-secrets"
    echo "Injected ocm-secrets binary"
fi
```

- [ ] **Step 3: Verify build**

```bash
make build-ocm-secrets
file backend/ocm-secrets
```

Expected: `backend/ocm-secrets: ELF 64-bit LSB executable, x86-64, statically linked`

- [ ] **Step 4: Commit**

```bash
git add Makefile scripts/build-rootfs.sh
git commit -m "feat: add build pipeline for ocm-secrets binary

New Makefile target builds the static Go binary. build-rootfs.sh
injects it into /usr/local/bin/ocm-secrets (root-owned, mode 0755)."
```

---

## Task 5: Rootfs — Vanilla OpenClaw Install Path

**Files:**
- Modify: `rootfs/Dockerfile.openclaw`

- [ ] **Step 1: Add native mode install path**

Add a Docker build arg and conditional install. The native path installs from npm instead of the fork tarball:

```dockerfile
ARG CONFIG_MODE=fork
ARG OPENCLAW_VERSION=2026.3.12

# Install OpenClaw — fork mode (existing) or native mode (vanilla from npm)
RUN if [ "$CONFIG_MODE" = "native" ]; then \
      echo "Installing vanilla OpenClaw v${OPENCLAW_VERSION} from npm..."; \
      pnpm install -g openclaw@${OPENCLAW_VERSION}; \
    else \
      echo "Installing OpenClaw fork from tarball..."; \
      pnpm install -g /tmp/openclaw-fork.tgz; \
    fi \
    && rm -f /tmp/openclaw-fork.tgz \
    && chmod -R o+rX /root /root/.local \
    # De-hardlink pnpm assets (same for both modes)
    && CUI_DIR=$(find /root/.local/share/pnpm -path '*/openclaw/dist/control-ui' -type d | head -1) \
    && EXT_DIR="${CUI_DIR%/dist/control-ui}/extensions" \
    && for dir in "$CUI_DIR" "$EXT_DIR"; do \
         cp -r --no-preserve=links "$dir" /tmp/dehardlink-tmp \
         && rm -rf "$dir" \
         && mv /tmp/dehardlink-tmp "$dir"; \
       done \
    && echo "$EXT_DIR" > /etc/ocm-extensions-dir
```

Also make the fork tarball COPY conditional:

```dockerfile
# Only copy fork tarball if it exists (not needed for native mode)
COPY openclaw-fork.tgz* /tmp/
```

- [ ] **Step 2: Verify native mode builds**

```bash
docker build -t ocm-rootfs-native \
  --build-arg CONFIG_MODE=native \
  --build-arg OPENCLAW_VERSION=2026.3.12 \
  -f rootfs/Dockerfile.openclaw rootfs/
```

Expected: successful build with vanilla OpenClaw from npm

- [ ] **Step 3: Verify fork mode still builds**

```bash
make build-openclaw-fork
docker build -t ocm-rootfs-fork -f rootfs/Dockerfile.openclaw rootfs/
```

Expected: successful build with fork tarball (existing behavior unchanged)

- [ ] **Step 4: Commit**

```bash
git add rootfs/Dockerfile.openclaw
git commit -m "feat: add native mode rootfs install path

Docker build arg CONFIG_MODE selects between fork tarball (default)
and vanilla OpenClaw from npm. Both paths share the de-hardlinking
step. Fork mode behavior is unchanged."
```

---

## Task 6: Pass Config Mode Through Metadata

**Files:**
- Modify: `backend/internal/metadata/metadata.go`
- Modify: `backend/internal/machines/runtime.go`

Note: `server_linux.go` does not need modification — the existing `handleMachine` endpoint serializes the full `MachineConfig` struct, so the new JSON-tagged field is served automatically.

- [ ] **Step 1: Add `ConfigMode` to `MachineConfig`**

In `backend/internal/metadata/metadata.go`, add to the `MachineConfig` struct (around line 42, near `GatewayToken`):

```go
ConfigMode string `json:"config_mode,omitempty"` // "fork" or "native"
```

- [ ] **Step 2: Set `ConfigMode` when creating VM metadata**

In `backend/internal/machines/runtime.go`, find where `MachineConfig` is populated (in the `Start()` method where fields like `MachineID`, `GatewayToken` are set). Add:

```go
ConfigMode: rs.configMode,
```

- [ ] **Step 3: Run tests**

```bash
cd backend && go test ./internal/metadata/ -v -count=1
cd backend && go test ./internal/machines/ -v -count=1
```

Expected: all tests PASS

- [ ] **Step 4: Commit**

```bash
git add backend/internal/metadata/metadata.go backend/internal/machines/runtime.go
git commit -m "feat: pass config_mode through metadata to VM

MachineConfig now includes config_mode field, served via /v1/machine.
Init script reads this to decide between fork and native startup paths."
```

---

## Task 7: Init Script — Native Mode Startup Path

**Files:**
- Modify: `scripts/init-openclaw.sh`

This is the most complex task. The init script needs a branching path for native mode. Depends on Task 6 (config_mode must be in metadata for the init script to read).

- [ ] **Step 1: Add config mode detection**

Near the top of the init script (after fetching `/v1/machine`, around line 279), add:

```bash
# Detect config mode from metadata
CONFIG_MODE=$(echo "$MACHINE_CONFIG" | jq -r '.config_mode // "fork"')
log "Config mode: $CONFIG_MODE"
```

(Note: `MACHINE_CONFIG` is already fetched from `/v1/machine` at line 261.)

- [ ] **Step 2: Add `write_seed_config` function**

Add a new function (before `start_gateway`, around line 749):

```bash
write_seed_config() {
    log "Writing seed openclaw.json for native config mode..."
    local config_dir="/home/openclaw/.openclaw"
    mkdir -p "$config_dir"

    # Fetch seed config from metadata (the backend assembled it)
    local seed_config
    seed_config=$(curl -sf "$METADATA_URL/v1/config")
    if [ -z "$seed_config" ]; then
        log "ERROR: Failed to fetch seed config from metadata"
        return 1
    fi

    # Write config file (owned by openclaw user)
    echo "$seed_config" > "$config_dir/openclaw.json"
    chown openclaw:openclaw "$config_dir/openclaw.json"
    chmod 644 "$config_dir/openclaw.json"
    log "Seed config written to $config_dir/openclaw.json"
}
```

- [ ] **Step 3: Branch `start_gateway` for native mode**

In the `start_gateway` function (around line 749), add a native mode path at the top that returns early. This path:
- Calls `write_seed_config` (writes openclaw.json to disk)
- Does NOT set `OCM_CONFIG_SOURCE=metadata`
- Does NOT set `OCM_METADATA_URL` or `OCM_METADATA_NONCE`
- Does NOT write `auth-profiles.json` (exec provider handles credentials)
- Still sets `OPENCLAW_GATEWAY_TOKEN` (from `/v1/machine`)
- Still sets `OPIK_API_KEY` (from `/v1/secrets`)
- Still applies quick-start flags if enabled

```bash
if [ "$CONFIG_MODE" = "native" ]; then
    write_seed_config || { log "FATAL: seed config write failed"; exit 1; }

    # Fetch platform secrets (OPIK_API_KEY)
    local secrets_json opik_envs=""
    secrets_json=$(curl -sf "$METADATA_URL/v1/secrets" || echo "{}")
    local opik_key
    opik_key=$(echo "$secrets_json" | jq -r '.OPIK_API_KEY // empty' 2>/dev/null)
    if [ -n "$opik_key" ]; then
        opik_envs="export OPIK_API_KEY='${opik_key}'"
    fi

    # Quick Start: set OPENCLAW_SKIP_* env vars (same logic as fork mode, line 830)
    local quick_start_envs=""
    if [ -n "$OCM_QUICK_START" ]; then
        quick_start_envs="
            export OPENCLAW_SKIP_CHANNELS=1
            export OPENCLAW_SKIP_BROWSER=1
            export OPENCLAW_SKIP_CANVAS=1
            export OPENCLAW_SKIP_CRON=1
            export OPENCLAW_SKIP_GMAIL=1
            export OPENCLAW_SKIP_BONJOUR=1
        "
    fi

    su -s /bin/bash openclaw -c "
        export HOME=/home/openclaw
        export PATH=\"/root/.local/share/pnpm:\$PATH\"
        export OPENCLAW_STATE_DIR=/home/openclaw/.openclaw
        export OPENCLAW_GATEWAY_TOKEN='$GATEWAY_TOKEN'
        $opik_envs
        $quick_start_envs
        exec openclaw gateway --port 18789 --bind loopback --allow-unconfigured
    " &
    GATEWAY_PID=$!
    return  # skip the fork-mode path below
fi
```

- [ ] **Step 4: Skip auth-profiles generation in native mode**

Find the auth-profiles.json generation block (around lines 814-829 in `start_gateway`). The `return` in Step 3 already skips it since the native path returns early. Verify this is the case — the auth-profiles block must be in the fork-mode path that follows the `if [ "$CONFIG_MODE" = "native" ]` block.

- [ ] **Step 5: Disable config watcher in native mode**

In the config watcher section (around line 921), wrap it:

```bash
if [ "$CONFIG_MODE" != "native" ]; then
    config_watcher &
fi
```

- [ ] **Step 6: Test init script syntax**

```bash
bash -n scripts/init-openclaw.sh
```

Expected: no syntax errors

- [ ] **Step 7: Commit**

```bash
git add scripts/init-openclaw.sh
git commit -m "feat: add native config mode startup path in init script

When config_mode=native (from metadata):
- Writes seed openclaw.json to disk (fetched from /v1/config)
- Skips OCM_CONFIG_SOURCE/metadata env vars
- Skips auth-profiles.json (exec provider handles credentials)
- Skips config watcher (file watcher handles reload)
- Still sets OPENCLAW_GATEWAY_TOKEN and OPIK_API_KEY
- Exits non-zero if seed config write fails"
```

---

## Task 8: Gateway E2E Tests

**Depends on:** Tasks 2 and 6 (needs `AssembleSeedConfig` and `ConfigMode` on `MachineConfig`)

**Files:**
- Modify: `backend/internal/gatewaye2e/gateway_test.go`

The existing gateway E2E suite (`backend/internal/gatewaye2e/`) spins up a real metadata server + gateway subprocess + API proxy. Native mode tests validate that seed configs produced by `AssembleSeedConfig` integrate correctly with the metadata server endpoints that `ocm-secrets` will call at runtime.

- [ ] **Step 1: Add E2E test for seed config + metadata round-trip**

Add to `backend/internal/gatewaye2e/gateway_test.go`:

```go
// TestNativeMode_SeedConfigMetadataRoundTrip verifies that AssembleSeedConfig
// output can be served through the metadata server and that the /v1/secrets
// endpoint returns the expected nonce values for ocm-secrets to consume.
func TestNativeMode_SeedConfigMetadataRoundTrip(t *testing.T) {
	// Generate seed config using the same function the runtime would use
	seedData, err := configassembly.AssembleSeedConfig(configassembly.SeedParams{
		MachineID:    machineID,
		DefaultModel: "anthropic/claude-sonnet-4-6",
		Providers:    []string{"anthropic"},
		ProxyBaseURL: fmt.Sprintf("http://127.0.0.1:%d", env.proxyPort),
	})
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	// Verify seed config is valid JSON with exec provider ref
	var cfg map[string]interface{}
	if err := json.Unmarshal(seedData, &cfg); err != nil {
		t.Fatalf("seed config is not valid JSON: %v", err)
	}
	// Check exec provider is defined
	secrets, ok := cfg["secrets"].(map[string]interface{})
	if !ok {
		t.Fatal("seed config missing 'secrets' key")
	}
	providers, ok := secrets["providers"].(map[string]interface{})
	if !ok {
		t.Fatal("seed config missing 'secrets.providers'")
	}
	ocmProvider, ok := providers["ocm"].(map[string]interface{})
	if !ok {
		t.Fatal("seed config missing 'secrets.providers.ocm'")
	}
	if ocmProvider["source"] != "exec" {
		t.Errorf("exec provider source = %v, want 'exec'", ocmProvider["source"])
	}
	if ocmProvider["command"] != "/usr/local/bin/ocm-secrets" {
		t.Errorf("exec provider command = %v, want '/usr/local/bin/ocm-secrets'", ocmProvider["command"])
	}

	// Register machine with seed config + secrets in metadata server
	testSecrets := map[string]string{
		"anthropic-key": "nonce-e2e-test-abc",
		"OPIK_API_KEY":  "opik-test-key",
	}
	env.metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID:    machineID,
		Nonce:        nonce,
		GatewayToken: gatewayToken,
		OpenClawConf: seedData,
		ConfigMode:   "native",
		Secrets:      testSecrets,
	})

	// Verify /v1/config returns the seed config
	configResp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/config", env.metaPort))
	if err != nil {
		t.Fatalf("GET /v1/config: %v", err)
	}
	defer configResp.Body.Close()
	if configResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/config: status %d", configResp.StatusCode)
	}
	configBody, _ := io.ReadAll(configResp.Body)
	var roundTrippedCfg map[string]interface{}
	if err := json.Unmarshal(configBody, &roundTrippedCfg); err != nil {
		t.Fatalf("/v1/config returned invalid JSON: %v", err)
	}
	// Verify it contains our exec provider
	rtSecrets := roundTrippedCfg["secrets"].(map[string]interface{})
	rtProviders := rtSecrets["providers"].(map[string]interface{})
	if _, ok := rtProviders["ocm"]; !ok {
		t.Error("/v1/config response missing ocm exec provider")
	}

	// Verify /v1/secrets returns the registered secrets (what ocm-secrets calls)
	secretsResp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/secrets", env.metaPort))
	if err != nil {
		t.Fatalf("GET /v1/secrets: %v", err)
	}
	defer secretsResp.Body.Close()
	if secretsResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/secrets: status %d", secretsResp.StatusCode)
	}
	var gotSecrets map[string]string
	if err := json.NewDecoder(secretsResp.Body).Decode(&gotSecrets); err != nil {
		t.Fatalf("/v1/secrets decode: %v", err)
	}
	if gotSecrets["anthropic-key"] != "nonce-e2e-test-abc" {
		t.Errorf("secrets[anthropic-key] = %q, want %q", gotSecrets["anthropic-key"], "nonce-e2e-test-abc")
	}
	if gotSecrets["OPIK_API_KEY"] != "opik-test-key" {
		t.Errorf("secrets[OPIK_API_KEY] = %q, want %q", gotSecrets["OPIK_API_KEY"], "opik-test-key")
	}

	// Restore original machine registration for subsequent tests
	env.updateLLMKeys(map[string]metadata.CredentialEntry{})
}
```

- [ ] **Step 2: Run gateway E2E tests**

```bash
make test-gateway-e2e
```

Expected: all tests PASS including `TestNativeMode_SeedConfigMetadataRoundTrip`

- [ ] **Step 3: Run full Go test suite**

```bash
make test-go
```

Expected: all tests PASS

- [ ] **Step 4: Commit**

```bash
git add backend/internal/gatewaye2e/gateway_test.go
git commit -m "test: add gateway E2E test for native config mode

Verifies seed config assembly round-trips through the metadata server:
- AssembleSeedConfig produces valid JSON with exec provider refs
- /v1/config serves the seed config
- /v1/secrets returns registered nonces for ocm-secrets consumption"
```

---

## Task 9: Documentation Update

**Files:**
- Modify: `docs/CurrentFeature.md`
- Modify: `docs/configuration_architecture.md`

- [ ] **Step 1: Update `docs/CurrentFeature.md`**

Add a new section documenting the native config mode work and its status.

- [ ] **Step 2: Update `docs/configuration_architecture.md`**

Add a note at the top referencing the superseding design document at `docs/superpowers/specs/2026-03-18-native-config-mode-design.md`.

- [ ] **Step 3: Commit**

```bash
git add docs/CurrentFeature.md docs/configuration_architecture.md
git commit -m "docs: update architecture docs for native config mode

References the new design spec. Updates CurrentFeature with native
config mode work items."
```

---

## Verification Checklist

Before marking the feature complete:

- [ ] `make test-go` — all backend tests pass
- [ ] `make test-gateway-e2e` — gateway E2E tests pass (including new native mode tests)
- [ ] `make build-ocm-secrets` — binary builds successfully
- [ ] `docker build --build-arg CONFIG_MODE=native` — native rootfs builds
- [ ] `docker build` (default) — fork rootfs still builds unchanged
- [ ] `bash -n scripts/init-openclaw.sh` — no syntax errors
- [ ] Manual test: set `OCM_CONFIG_MODE=native`, start a machine, verify OpenClaw boots with exec provider
