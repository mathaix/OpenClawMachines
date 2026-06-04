# OpenAI Codex Local Token Storage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Store OpenAI Codex OAuth tokens locally on the VM using the native OpenClaw auth-profiles format, so the gateway handles token refresh natively — eliminating the backend DB/proxy round-trip for subscription credentials.

**Architecture:** After OAuth exchange, the agent writes tokens directly into `auth-profiles.json` (native OpenClaw format with `type: "oauth"`). Config assembly emits `auth.profiles` config so the gateway knows to use OAuth mode. The gateway calls `chatgpt.com/backend-api` directly and refreshes tokens itself. No backend DB, no proxy, no ocm-secrets involvement for codex.

**Tech Stack:** Go (agent, orchestrator, config assembly)

---

## Scope

**In scope:** OpenAI Codex subscription OAuth tokens only.

**Out of scope:** All other providers (BYOK, platform, other OAuth). Those continue using the proxy/nonce approach.

## Native OpenClaw Format Reference

The gateway expects these two files to work together:

**`auth-profiles.json`** — token storage:
```json
{
  "version": 1,
  "profiles": {
    "openai-codex:default": {
      "type": "oauth",
      "provider": "openai-codex",
      "access": "<jwt_access_token>",
      "refresh": "<refresh_token>",
      "expires": 1775181197485
    },
    "nebius-proxy": {
      "type": "api_key",
      "provider": "nebius",
      "key": "<nonce>"
    }
  }
}
```

**`openclaw.json`** — auth profile config:
```json
{
  "auth": {
    "profiles": {
      "openai-codex:default": {
        "provider": "openai-codex",
        "mode": "oauth"
      }
    }
  }
}
```

The gateway reads `mode: "oauth"` → looks up the profile in `auth-profiles.json` → uses the access token → auto-refreshes using the refresh token when expired.

## File paths on the VM

- `auth-profiles.json` real location: `/home/openclaw/.openclaw/config/config-current/auth-profiles.json`
- Symlink: `/home/openclaw/.openclaw/agents/main/agent/auth-profiles.json` → `config-current/auth-profiles.json`
- `openclaw.json`: `/home/openclaw/.openclaw/config/config-current/openclaw.json`

## File Structure

| File | Action | Responsibility |
|------|--------|---------------|
| `backend/cmd/agent/codex_auth.go` | Modify | After OAuth exchange, write tokens to auth-profiles.json |
| `backend/internal/configassembly/assembler.go` | Modify | Emit `auth.profiles` config for codex; point baseUrl at chatgpt.com |
| `backend/internal/configassembly/assembler_test.go` | Modify | Test new codex config output |
| `backend/internal/orchestrator/firecracker_linux.go` | Modify | Skip codex in nonce-based auth profile generation |
| `backend/internal/api/oauth.go` | Modify | Remove codex-specific entries from token/clientID maps |
| `backend/internal/api/credentials.go` | Modify | Codex validation only via VM, no backend fallback |
| `backend/cmd/ocm-secrets/main.go` | Modify | Remove `openai-codex-key` from proxyKeyIDs (no longer used) |

---

### Task 1: Agent Writes Tokens to auth-profiles.json

**Files:**
- Modify: `backend/cmd/agent/codex_auth.go`

After OAuth exchange, the agent writes tokens into the gateway's `auth-profiles.json` file (native OpenClaw format). The gateway then handles refresh natively.

- [ ] **Step 1: Add auth-profiles constants and helper**

Add to `codex_auth.go` after the existing constants:

```go
const (
	// authProfilesPath is the symlinked auth-profiles.json used by the gateway.
	authProfilesPath = "/home/openclaw/.openclaw/agents/main/agent/auth-profiles.json"
)
```

Add helper functions:

```go
// readAuthProfiles reads the current auth-profiles.json, returning the full structure.
func readAuthProfiles() (map[string]interface{}, error) {
	data, err := os.ReadFile(authProfilesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]interface{}{
				"version":  float64(1),
				"profiles": map[string]interface{}{},
			}, nil
		}
		return nil, err
	}
	var profiles map[string]interface{}
	if err := json.Unmarshal(data, &profiles); err != nil {
		return nil, fmt.Errorf("parse auth-profiles.json: %w", err)
	}
	return profiles, nil
}

// writeCodexAuthProfile merges the codex OAuth profile into auth-profiles.json
// alongside existing profiles (e.g. nebius-proxy, anthropic-proxy).
func writeCodexAuthProfile(accessToken, refreshToken string, expiresAt time.Time) error {
	authProfiles, err := readAuthProfiles()
	if err != nil {
		return fmt.Errorf("read existing auth-profiles: %w", err)
	}

	profiles, ok := authProfiles["profiles"].(map[string]interface{})
	if !ok {
		profiles = map[string]interface{}{}
	}

	// Add/update the codex OAuth profile
	profiles["openai-codex:default"] = map[string]interface{}{
		"type":     "oauth",
		"provider": "openai-codex",
		"access":   accessToken,
		"refresh":  refreshToken,
		"expires":  expiresAt.UnixMilli(),
	}

	authProfiles["profiles"] = profiles

	data, err := json.MarshalIndent(authProfiles, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth-profiles: %w", err)
	}
	if err := os.WriteFile(authProfilesPath, data, 0644); err != nil {
		return fmt.Errorf("write auth-profiles.json: %w", err)
	}
	fmt.Fprintf(os.Stderr, "codex-auth: wrote OAuth profile to %s (expires=%s)\n",
		authProfilesPath, expiresAt.Format(time.RFC3339))
	return nil
}
```

- [ ] **Step 2: Modify `codexAuthExchange()` to write auth-profiles.json**

Replace the "Store tokens in backend via metadata admin proxy" section (lines 278-311) with local auth-profiles.json write:

```go
	// Write tokens to auth-profiles.json (native OpenClaw format).
	// The gateway reads this file and handles OAuth refresh natively.
	expiresAt := time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	if err := writeCodexAuthProfile(tokens.AccessToken, tokens.RefreshToken, expiresAt); err != nil {
		return "", fmt.Errorf("writing auth profile: %w", err)
	}
```

Remove the nonce check, storeURL, storePayload, storeReq/storeResp code that was posting to the backend.

- [ ] **Step 3: Modify `codexAuthCheck()` to read auth-profiles.json**

Replace the proxy-based check with a local file check:

```go
func codexAuthCheck() string {
	authProfiles, err := readAuthProfiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex-auth check: cannot read auth-profiles: %v\n", err)
		result, _ := json.Marshal(map[string]interface{}{"connected": false, "error": err.Error()})
		return string(result)
	}

	profiles, _ := authProfiles["profiles"].(map[string]interface{})
	codexProfile, ok := profiles["openai-codex:default"].(map[string]interface{})
	if !ok {
		result, _ := json.Marshal(map[string]interface{}{"connected": false})
		return string(result)
	}

	accessToken, _ := codexProfile["access"].(string)
	if accessToken == "" {
		result, _ := json.Marshal(map[string]interface{}{"connected": false})
		return string(result)
	}

	// Check if expired
	if expiresMs, ok := codexProfile["expires"].(float64); ok {
		expiresAt := time.UnixMilli(int64(expiresMs))
		if time.Now().After(expiresAt) {
			fmt.Fprintf(os.Stderr, "codex-auth check: token expired at %s\n", expiresAt.Format(time.RFC3339))
			result, _ := json.Marshal(map[string]interface{}{"connected": true, "expired": true})
			return string(result)
		}
	}

	result, _ := json.Marshal(map[string]interface{}{"connected": true})
	return string(result)
}
```

- [ ] **Step 4: Modify `codexAuthTest()` to call chatgpt.com directly**

Replace the proxy-based test with a direct API call using the token from auth-profiles.json:

```go
func codexAuthTest(model string) string {
	authProfiles, err := readAuthProfiles()
	if err != nil {
		result, _ := json.Marshal(map[string]interface{}{"ok": false, "error": "cannot read auth-profiles: " + err.Error()})
		return string(result)
	}

	profiles, _ := authProfiles["profiles"].(map[string]interface{})
	codexProfile, ok := profiles["openai-codex:default"].(map[string]interface{})
	if !ok {
		result, _ := json.Marshal(map[string]interface{}{"ok": false, "error": "no codex profile in auth-profiles.json"})
		return string(result)
	}

	accessToken, _ := codexProfile["access"].(string)
	if accessToken == "" {
		result, _ := json.Marshal(map[string]interface{}{"ok": false, "error": "no access token in codex profile"})
		return string(result)
	}

	fmt.Fprintf(os.Stderr, "codex-auth test: model=%s, token=%s...\n", model, truncateStr(accessToken, 12))

	payload, _ := json.Marshal(map[string]interface{}{
		"model":        model,
		"instructions": "Reply OK.",
		"input":        []map[string]string{{"role": "user", "content": "hi"}},
		"stream":       true,
		"store":        false,
	})

	testURL := "https://chatgpt.com/backend-api/codex/responses"
	fmt.Fprintf(os.Stderr, "codex-auth test: POST %s\n", testURL)
	req, _ := http.NewRequest("POST", testURL, bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		result, _ := json.Marshal(map[string]interface{}{"ok": false, "error": "request failed: " + err.Error()})
		return string(result)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Fprintf(os.Stderr, "codex-auth test: HTTP %d, body=%s\n", resp.StatusCode, truncateStr(string(body), 200))

	if resp.StatusCode == http.StatusUnauthorized {
		result, _ := json.Marshal(map[string]interface{}{"ok": false, "error": "invalid or expired token"})
		return string(result)
	}
	if resp.StatusCode >= 400 {
		var apiErr struct {
			Detail string `json:"detail"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Detail != "" {
			result, _ := json.Marshal(map[string]interface{}{"ok": false, "error": apiErr.Detail})
			return string(result)
		}
		result, _ := json.Marshal(map[string]interface{}{"ok": false, "error": fmt.Sprintf("HTTP %d", resp.StatusCode)})
		return string(result)
	}

	result, _ := json.Marshal(map[string]interface{}{"ok": true})
	return string(result)
}
```

- [ ] **Step 5: Clean up unused code**

Remove `readNonce()`, `metadataBaseURL`, and `proxyBaseURL` constants if no longer used by any function in codex_auth.go.

- [ ] **Step 6: Verify build**

Run: `cd /home/mantiz/OpenClawMachines && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./backend/cmd/agent/`
Expected: Clean build

- [ ] **Step 7: Commit**

```bash
git add backend/cmd/agent/codex_auth.go
git commit -m "feat: write codex OAuth tokens to auth-profiles.json (native OpenClaw format)"
```

---

### Task 2: Config Assembly — Emit auth.profiles + Direct baseUrl

**Files:**
- Modify: `backend/internal/configassembly/assembler.go`
- Modify: `backend/internal/configassembly/assembler_test.go`

Config assembly needs to:
1. Emit `auth.profiles.openai-codex:default` with `mode: "oauth"` in `openclaw.json`
2. Set `baseUrl` to `https://chatgpt.com/backend-api` (not the proxy)
3. Remove the exec secret ref / nonce-based `apiKey` for codex

- [ ] **Step 1: Write/update failing test for AssembleConfig**

In `assembler_test.go`, find the test for openai-codex config and update assertions:

```go
// Assert codex provider points directly at chatgpt.com
codexProvider := mustGetNestedMap(t, cfg, "models", "providers", "openai-codex")
if codexProvider["baseUrl"] != "https://chatgpt.com/backend-api" {
	t.Errorf("openai-codex baseUrl = %v, want https://chatgpt.com/backend-api", codexProvider["baseUrl"])
}
if codexProvider["api"] != "openai-codex-responses" {
	t.Errorf("openai-codex api = %v, want openai-codex-responses", codexProvider["api"])
}

// Assert auth.profiles section exists for codex
authProfiles := mustGetNestedMap(t, cfg, "auth", "profiles", "openai-codex:default")
if authProfiles["provider"] != "openai-codex" {
	t.Errorf("auth.profiles provider = %v, want openai-codex", authProfiles["provider"])
}
if authProfiles["mode"] != "oauth" {
	t.Errorf("auth.profiles mode = %v, want oauth", authProfiles["mode"])
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/configassembly/ -v -run TestCodex`
Expected: FAIL

- [ ] **Step 3: Modify `AssembleConfig` for codex**

In the openai-codex handling sections (~lines 427-434 and 446-458), change baseUrl and remove apiKey:

```go
if provider == "openai-codex" {
	// Codex uses native OpenClaw OAuth — gateway calls chatgpt.com directly.
	// No proxy, no exec secret ref. Auth comes from auth-profiles.json.
	codexCfg := map[string]interface{}{
		"baseUrl": "https://chatgpt.com/backend-api",
		"api":     "openai-codex-responses",
		"models":  buildCodexModelsFromCatalog(params.ModelCatalog),
	}
	providerConfigs[provider] = codexCfg
	continue
}
```

And add `auth.profiles` section after the providers block:

```go
// Emit auth.profiles for codex OAuth when credential exists
if _, hasCodexCred := params.Credentials["openai-codex"]; hasCodexCred {
	auth := getOrCreateMap(result, "auth")
	authProfiles := getOrCreateMap(auth, "profiles")
	authProfiles["openai-codex:default"] = map[string]interface{}{
		"provider": "openai-codex",
		"mode":     "oauth",
	}
	auth["profiles"] = authProfiles
	result["auth"] = auth
}
```

- [ ] **Step 4: Modify `AssembleSeedConfig` for codex**

In the seed config path (~line 784), same change:

```go
} else if provider == "openai-codex" {
	provCfg["baseUrl"] = "https://chatgpt.com/backend-api"
	provCfg["api"] = "openai-codex-responses"
	// Remove apiKey — auth comes from auth-profiles.json, not exec secret
	delete(provCfg, "apiKey")
}
```

And add auth.profiles for seed config too.

- [ ] **Step 5: Run tests**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/configassembly/ -v`
Expected: All tests pass

- [ ] **Step 6: Run gateway E2E tests**

Run: `make test-gateway-e2e`
Expected: Pass

- [ ] **Step 7: Commit**

```bash
git add backend/internal/configassembly/assembler.go backend/internal/configassembly/assembler_test.go
git commit -m "feat: codex config points directly at chatgpt.com with auth.profiles oauth mode"
```

---

### Task 3: Orchestrator — Skip Codex in Nonce Auth Profiles

**Files:**
- Modify: `backend/internal/orchestrator/firecracker_linux.go`

The `generateAuthProfiles` function currently creates `{provider}-proxy` entries with the nonce for every provider. For codex, the agent writes the OAuth profile after user authentication — the orchestrator should NOT write a nonce-based entry for codex.

- [ ] **Step 1: Modify `generateAuthProfiles` to skip codex**

In `firecracker_linux.go` at line 1575:

```go
func generateAuthProfiles(providerNames []string, nonce string) []byte {
	profiles := make(map[string]interface{}, len(providerNames))
	for _, name := range providerNames {
		// openai-codex uses native OAuth — tokens written by agent after
		// user authentication, not nonce-based proxy auth.
		if name == "openai-codex" {
			continue
		}
		profiles[name+"-proxy"] = map[string]interface{}{
			"type":     "api_key",
			"provider": name,
			"key":      nonce,
		}
	}
	authProfiles := map[string]interface{}{
		"version":  1,
		"profiles": profiles,
	}
	data, err := json.MarshalIndent(authProfiles, "", "  ")
	if err != nil {
		return []byte("{}")
	}
	return data
}
```

- [ ] **Step 2: Verify build and tests**

Run: `cd /home/mantiz/OpenClawMachines && make test-go`
Expected: All pass

- [ ] **Step 3: Commit**

```bash
git add backend/internal/orchestrator/firecracker_linux.go
git commit -m "fix: skip codex in nonce-based auth profile generation"
```

---

### Task 4: Clean Up Backend — Remove Codex from DB Paths

**Files:**
- Modify: `backend/internal/api/oauth.go`
- Modify: `backend/internal/api/credentials.go`
- Modify: `backend/cmd/ocm-secrets/main.go`

Remove codex-specific entries from backend token storage/refresh maps and ocm-secrets.

- [ ] **Step 1: Remove codex from oauth.go maps**

In `oauth.go`, remove `openai-codex` from `oauthTokenEndpoints` and `oauthClientIDs`:

```go
var oauthTokenEndpoints = map[string]string{
	"google": "https://oauth2.googleapis.com/token",
	"openai": "https://auth.openai.com/v1/token",
}

var oauthClientIDs = map[string]string{}
```

- [ ] **Step 2: Update codex credential validation in credentials.go**

In `handleTestMachineCredential` (~line 237), update the codex path to always go through the VM:

```go
if cred.CredentialType == "oauth" && provider == "openai-codex" {
	// Codex tokens live on the VM (auth-profiles.json), not in backend DB.
	// Validation must go through the VM's codex-auth test command.
	if machine.Status != "running" || machine.HostID == nil ||
		machine.ProxyToken == nil || *machine.ProxyToken == "" || s.agentClient == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":    false,
			"error": "machine must be running to test codex credential",
		})
		return
	}
	result := s.validateOpenAICodexCredential(r.Context(), machine, "")
	resp := map[string]interface{}{"ok": result.OK}
	if !result.OK {
		resp["error"] = result.Error
		if result.Code != "" {
			resp["code"] = result.Code
		}
	}
	writeJSON(w, http.StatusOK, resp)
	return
}
```

In `validateOpenAICodexCredential`, remove the backend fallback (`validateOpenAICodexToken` call):

```go
func (s *Server) validateOpenAICodexCredential(ctx context.Context, machine *store.Machine, _ string) codexTestResult {
	modelID := "gpt-5.4"
	catalog, err := s.store.ListModelCatalog(ctx)
	if err == nil {
		for _, m := range catalog {
			if m.Provider == "openai-codex" && m.Enabled {
				bare := m.ID
				if i := strings.Index(bare, "/"); i >= 0 {
					bare = bare[i+1:]
				}
				modelID = bare
				break
			}
		}
	}

	if machine == nil || machine.Status != "running" || machine.HostID == nil ||
		machine.ProxyToken == nil || *machine.ProxyToken == "" || s.agentClient == nil {
		return codexTestResult{Error: "machine must be running to test codex credential"}
	}

	host, err := s.store.GetHost(ctx, *machine.HostID)
	if err != nil {
		return codexTestResult{Error: "host lookup failed: " + err.Error()}
	}

	result, err := s.agentClient.ExecCommand(ctx, host, machine.ID, *machine.ProxyToken, []string{"codex-auth", "test", modelID})
	if err != nil {
		return codexTestResult{Error: "agent exec failed: " + err.Error()}
	}
	if result.ExitCode != 0 {
		msg := strings.TrimSpace(result.Stderr)
		if msg == "" {
			msg = "codex-auth test failed"
		}
		return codexTestResult{Error: msg}
	}

	var parsed codexTestResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &parsed); err != nil {
		return codexTestResult{Error: "decode agent response failed"}
	}
	return parsed
}
```

- [ ] **Step 3: Remove `openai-codex-key` from ocm-secrets proxyKeyIDs**

In `backend/cmd/ocm-secrets/main.go`, remove the codex entry since the gateway no longer uses the exec secret provider for codex:

```go
var proxyKeyIDs = map[string]bool{
	"anthropic-key":  true,
	"openai-key":     true,
	"nebius-key":     true,
	"google-key":     true,
	"openrouter-key": true,
}
```

- [ ] **Step 4: Remove `openai-codex` from providerExecIDs in assembler.go**

In `assembler.go` (~line 695), remove the codex entry:

```go
var providerExecIDs = map[string]string{
	"anthropic":  "anthropic-key",
	"openai":     "openai-key",
	"google":     "google-key",
	"openrouter": "openrouter-key",
	"nebius":     "nebius-key",
}
```

- [ ] **Step 5: Run all tests**

Run: `make test-go`
Expected: All pass

Run: `make test-gateway-e2e`
Expected: All pass

- [ ] **Step 6: Commit**

```bash
git add backend/internal/api/oauth.go backend/internal/api/credentials.go backend/cmd/ocm-secrets/main.go backend/internal/configassembly/assembler.go
git commit -m "refactor: remove codex from backend DB/proxy token paths"
```

---

### Task 5: Integration Verification

- [ ] **Step 1: Full test suite**

Run: `make test-go && make test-gateway-e2e && make test-frontend`
Expected: All pass

- [ ] **Step 2: Check codex-test binary**

Read `backend/cmd/codex-test/main.go` and update if it uses proxy/backend endpoints. It should call chatgpt.com directly or use auth-profiles.json.

- [ ] **Step 3: Final commit if needed**

```bash
git add -A
git commit -m "fix: integration fixes for codex local token storage"
```

---

## Summary of New Data Flow

```
USER BROWSER
    | (clicks "Connect OpenAI Codex")
AGENT (codex_auth.go)
    | 1. Generates PKCE challenge
    | 2. User authenticates at auth.openai.com
    | 3. Agent exchanges code for tokens
    | 4. Writes OAuth profile to auth-profiles.json
    |    (native OpenClaw format: type=oauth, access, refresh, expires)
    |
GATEWAY (OpenClaw — native OAuth mode)
    | - Config says: auth.profiles.openai-codex:default.mode = "oauth"
    | - Reads access_token from auth-profiles.json
    | - baseUrl = https://chatgpt.com/backend-api
    | - Calls chatgpt.com directly with Bearer token
    | - Auto-refreshes when token expires (native gateway behavior)
    |
CHATGPT.COM
    | (returns response)
```

No backend DB. No proxy. No ocm-secrets. The gateway owns the credential lifecycle, exactly like local OpenClaw.
