# Configuration Gaps — Implementation Plan

**Status:** Planned
**Branch:** `configuration`
**Related docs:**
- `docs/configuration.md` — Gap analysis (Sections 3, 5, 9)
- `docs/CLI-UIfeedback.md` — CLI design prototype review
- `docs/cli-design-prototype.html` — Interactive CLI wizard mockup

---

## Remaining Gaps (from `docs/configuration.md` Section 9)

### Resolved by E2E tests (no further work needed)

| Gap | Resolution |
|-----|------------|
| Gateway can't reach LLM providers | Init script passes provider `BASE_URL` env vars + writes `auth-profiles.json` |
| auth-profiles.json never written | Init script generates it at boot with `type: "api_key"` for all providers |
| Credential type handling unverified | Anthropic `api_key` and `subscription_key` both E2E tested to 200 OK |

### This plan addresses

| # | Gap | Priority |
|---|-----|----------|
| 1 | `CredentialEntry` too simple for OAuth | Critical |
| 2 | No OpenAI OAuth refresh endpoint | Critical |
| 6 | Config assembly incomplete | Important |

### Out of scope (addressed separately)

| Gap | Where addressed |
|-----|----------------|
| OpenAI CLI wizard broken (`chatgpt setup-token`) | CLI work — see `docs/CLI-UIfeedback.md` |
| Workspace files not seeded | Future init-script work |
| No `denyCommands` for hosted VMs | Included in Part B of this plan |
| CLI UX simplification (`ocm setup`, `ocm status`, `ocm doctor`) | See `docs/CLI-UIfeedback.md` and `docs/cli-design-prototype.html` |

---

## Part A: OAuth Credential Flow (Gaps 1 + 2)

### Problem

OAuth tokens (OpenAI JWT, future Anthropic OAT refresh) expire during long VM sessions. The current `CredentialEntry` only carries a `Value` string — no expiry, no refresh capability. When a token expires mid-session, the proxy forwards a stale token and every LLM request fails with 401.

The DB schema (`AccountCredential`) already has `RefreshToken` and `ExpiresAt` columns, but these don't flow through to the agent/proxy layer.

### Approach: Full background refresh

1. **Pre-refresh at machine start** — Refresh near-expiry OAuth tokens before injecting into the VM
2. **Background goroutine on control plane** — Periodically check running machines for expiring tokens, refresh and push fresh credentials

The refresh token stays on the control plane (which has DB access, encryption key, and OAuth client credentials). The agent/proxy only receives the current access token + expiry timestamp.

### A1. Extend `CredentialEntry` struct

**File:** `backend/internal/metadata/metadata.go`

Add `ExpiresAt *time.Time` field. The proxy doesn't need `RefreshToken` — refresh is a control-plane concern.

```go
type CredentialEntry struct {
    Value          string     `json:"value"`
    CredentialType string     `json:"credential_type"`
    ExpiresAt      *time.Time `json:"expires_at,omitempty"` // nil for non-expiring keys
}
```

### A2. Forward `ExpiresAt` at machine start

**File:** `backend/internal/api/server.go` (credential decryption loop, ~line 770)

Include `cred.ExpiresAt` when building the `CredentialEntry`.

### A3. Pre-refresh OAuth credentials at machine start

**File:** `backend/internal/api/server.go` (new helper)

Before injecting credentials into the VM, iterate through OAuth credentials and refresh any that are within 5 minutes of expiry. Update DB with fresh tokens so the decryption loop below picks up the new values.

### A4. Add OpenAI OAuth token endpoint

**File:** `backend/internal/api/oauth.go`

```go
var oauthTokenEndpoints = map[string]string{
    "google": "https://oauth2.googleapis.com/token",
    "openai": "https://auth.openai.com/v1/token",
}
```

Note: OpenAI endpoint URL needs verification against their docs.

### A5. Add `UpdateCredentialTokens` store method

**Files:** `backend/internal/store/store.go` (interface), `backend/internal/store/postgres.go` (implementation)

New method to atomically update access token, refresh token, and expiry in the DB.

### A6. Add credential push to running VMs

Follow the existing `UpdateVMSecrets` / `UpdateVMConfig` pattern:

| Layer | File | Method |
|-------|------|--------|
| Metadata server | `metadata/metadata.go` | `UpdateMachineLLMKeys(vmIP, llmKeys)` |
| Orchestrator interface | `orchestrator/orchestrator.go` | `UpdateLLMKeys(ctx, machineID, llmKeys)` |
| MetadataRegistrar interface | `orchestrator/orchestrator.go` | `UpdateMachineLLMKeys(vmIP, llmKeys)` |
| Firecracker impl | `orchestrator/firecracker_linux.go` | Looks up VM IP, calls registrar |
| Stub impl | `orchestrator/firecracker_stub.go` | Returns `ErrNotLinux` |
| Agent handler | `agentapi/handlers.go` | `PATCH /vms/{machineID}/credentials` |
| Agent route | `agentapi/server.go` | Register route |
| Agent client | `agentclient/client.go` | `UpdateVMCredentials(ctx, host, machineID, llmKeys)` |

Credential updates do NOT bump the config version (no gateway restart needed — the proxy reads keys from in-memory metadata on every request).

### A7. Background OAuth refresh goroutine

**File:** `backend/internal/api/oauth_refresh.go` (new)

```
StartOAuthRefreshLoop(ctx):
  every 2 minutes:
    1. Query running machines
    2. Load each machine's OAuth credentials
    3. For each with ExpiresAt < now + 10 minutes:
       a. Call refreshOAuthToken()
       b. Update DB
       c. Build fresh llmKeys map
       d. Push via agentClient.UpdateVMCredentials()
```

Started from `backend/cmd/server/main.go` after server initialization.

### A8. OAuth client configuration

Add `oauthClientID` and `oauthClientSecret` fields to the `Server` struct, read from `OAUTH_CLIENT_ID` and `OAUTH_CLIENT_SECRET` env vars.

---

## Part B: Config Assembly Completion (Gap 6)

### Problem

The assembled config served to the gateway via `/v1/config` is missing several sections that a local OpenClaw install has in `openclaw.json`. The gateway works without them (uses internal defaults), but behavior is unpredictable and security-relevant settings are absent.

### B1. Add static sections to `platformDefaults`

**File:** `backend/internal/configassembly/assembler.go`

| Section | Value | Purpose |
|---------|-------|---------|
| `commands.native` | `"auto"` | Enable native command handling |
| `commands.nativeSkills` | `"auto"` | Enable native skill commands |
| `commands.restart` | `true` | Allow gateway restart command |
| `commands.ownerDisplay` | `"raw"` | Show owner info as-is |
| `session.dmScope` | `"per-channel-peer"` | DM session isolation |
| `gateway.nodes.denyCommands` | `["camera.snap", "camera.clip", "screen.record", "calendar.add", "contacts.add", "reminders.add"]` | **Security**: restrict dangerous commands in hosted environment |

### B2. Add default model selection

**File:** `backend/internal/configassembly/assembler.go`

Add `DefaultModel string` to `AssemblyParams`. When set, generate:

```json
{
  "agents": {
    "defaults": {
      "model": { "primary": "anthropic/claude-sonnet-4-6" },
      "models": { "anthropic/claude-sonnet-4-6": {} },
      "workspace": "/home/openclaw/.openclaw/workspace"
    }
  }
}
```

### B3. Derive default model from credentials

**File:** `backend/internal/api/machine_config.go`

In `assembleConfigForMachine`, after building the credentials map, pick the default model based on which LLM providers have credentials:

| Priority | Provider | Default model |
|----------|----------|---------------|
| 1 | anthropic | `anthropic/claude-sonnet-4-6` |
| 2 | openai | `openai/gpt-4o` |
| 3 | google | `google/gemini-2.0-flash` |

### B4. Update protected config keys

**File:** `backend/internal/configassembly/assembler.go`

Add `agents`, `commands`, `session` to `ProtectedConfigKeys` and `protectedPrefixes` so user capability overrides can't tamper with platform-level settings.

### B5. Unit tests

**File:** `backend/internal/configassembly/assembler_test.go`

| Test | Verifies |
|------|----------|
| `TestPlatformDefaults_Commands` | `commands.native`, `commands.restart`, etc. present |
| `TestPlatformDefaults_Session` | `session.dmScope` present |
| `TestPlatformDefaults_DenyCommands` | `gateway.nodes.denyCommands` contains expected entries |
| `TestAgentsDefaults` | `agents.defaults.model.primary` and `workspace` when model set |
| `TestAgentsDefaults_NoModel` | `agents` section absent when no `DefaultModel` |
| `TestProtectedKeys_AgentsCommandsSession` | New protected keys stripped from overrides |

---

## Files Modified

| File | Part | Changes |
|------|------|---------|
| `backend/internal/metadata/metadata.go` | A | Add `ExpiresAt` to `CredentialEntry`, add `UpdateMachineLLMKeys` |
| `backend/internal/api/server.go` | A | Forward `ExpiresAt`, call pre-refresh, add OAuth client fields |
| `backend/internal/api/oauth.go` | A | Add OpenAI token endpoint |
| `backend/internal/api/oauth_refresh.go` | A | **NEW** — background refresh goroutine |
| `backend/internal/store/store.go` | A | Add `UpdateCredentialTokens` to interface |
| `backend/internal/store/postgres.go` | A | Implement `UpdateCredentialTokens` |
| `backend/internal/orchestrator/orchestrator.go` | A | Add `UpdateLLMKeys` to both interfaces |
| `backend/internal/orchestrator/firecracker_linux.go` | A | Implement `UpdateLLMKeys` |
| `backend/internal/orchestrator/firecracker_stub.go` | A | Stub `UpdateLLMKeys` |
| `backend/internal/agentapi/handlers.go` | A | Add `handleUpdateVMCredentials` handler |
| `backend/internal/agentapi/server.go` | A | Register credentials route |
| `backend/internal/agentclient/client.go` | A | Add `UpdateVMCredentials` method |
| `backend/internal/configassembly/assembler.go` | B | Add platformDefaults, `DefaultModel`, agents step, protected keys |
| `backend/internal/configassembly/assembler_test.go` | B | New tests for all additions |
| `backend/cmd/server/main.go` | A | Start OAuth refresh loop |

---

## Verification

### Unit tests
```bash
make test-go
```

### Config assembly output
```bash
ocm config machine-show --machine "My Bot" | jq '.commands, .session, .gateway.nodes, .agents'
```

### OAuth refresh (manual)
1. Store an OAuth credential with near-expiry `ExpiresAt`
2. Start a machine → verify pre-refresh in logs (`oauth.refresh`)
3. Wait for background tick → verify push in agent logs (`vm.credentials_updated`)

### E2E proxy tests (regression)
```bash
make test-gateway-e2e
```

---

## Relationship to CLI Design

The CLI design prototype (`docs/cli-design-prototype.html`) and review (`docs/CLI-UIfeedback.md`) identify several UX improvements that build on this backend work:

| CLI Feature | Depends on |
|-------------|------------|
| `ocm status` — show provider credential type + validation time | Part A: `ExpiresAt` in `CredentialEntry` gives the CLI visibility into token freshness |
| `ocm doctor` — "Credential recently validated" check | Part A: expired tokens are auto-refreshed, so `last_validated` stays current |
| `ocm setup` — provider auth choice (subscription vs API key) | Part A: OAuth flow works end-to-end once refresh is wired |
| `ocm setup` — verify step health checks | Part B: assembled config includes all expected sections |
| `ocm doctor` — "Config version is current" check | Part B: config assembly produces a complete config that passes gateway validation |
| Fix OpenAI auth flow (remove `chatgpt setup-token`) | Independent CLI change — see `CLI-UIfeedback.md` |

The backend changes in this plan unblock the CLI improvements but don't require them. CLI work can proceed independently on a separate branch.
