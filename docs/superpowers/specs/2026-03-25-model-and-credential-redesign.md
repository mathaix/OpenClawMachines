# Model Organization & Credential Redesign

## Problem

The model and credential systems have accumulated design debt from layered transitions:

1. **Credential identity crisis** — Credentials are stored account-wide (`account_credentials`) but the UI presents them as per-machine. An account-wide fallback silently leaks credentials to machines that shouldn't have them.
2. **Model catalog is incomplete** — Only 11 models seeded. The curated Nebius list in `platform-models-nebius.md` has 8 models but the catalog only has 3. BYOK provider lists are minimal (2 Anthropic, 2 OpenAI, 3 Google).
3. **Dead code** — Three unused frontend components (~1500 LOC) from the old account-wide credential UI.
4. **No validation on key entry** — `ModelTab.tsx` creates credentials without validating them against the provider API.
5. **Duplicate credentials possible** — Schema allows multiple credentials per provider per account with no dedup in the UI.
6. **OpenAI Codex OAuth is broken on OCM** — The OAuth token is stored VM-local in `auth-profiles.json`. The gateway sends the real OAuth token to the proxy, but the proxy expects the nonce for authentication and returns 401. Works on local OpenClaw (no proxy) but fails on OCM.

## Design Principles

1. **A credential belongs to a machine.** No account-wide fallback, no ambiguity.
2. **The model catalog is the single source of truth.** If a model isn't in the catalog, it doesn't exist.
3. **Three model sources, three experiences.** Platform, BYOK, and Subscription each have distinct UX and backend flows.
4. **Clean slate.** No real users yet. No backward compatibility required. Break what needs breaking. Ship the right design, not the safe migration.

---

## Part 1: Model Organization

### Three Model Sources

#### 1. Platform Models (Nebius)

Always available. No credentials needed. Metered per-token at 1.5x Nebius cost.

Users see friendly names ("Smart", "Balanced", "Fast" tiers). They never see "Nebius."

**Current catalog (3 models):**

| User ID | Label | Tier | Gateway Model ID |
|---------|-------|------|------------------|
| `qwen/qwen3.5-397b` | Smart | smart | `nebius/Qwen/Qwen3.5-397B-A17B` |
| `minimax/minimax-m2.5` | Balanced | balanced | `nebius/MiniMaxAI/MiniMax-M2.5` |
| `openai/gpt-oss-120b` | Fast | fast | `nebius/openai/gpt-oss-120b` |

**Proposed expansion** (from `platform-models-nebius.md`, updated for current Nebius availability):

| User ID | Label | Tier | Input/M | Output/M |
|---------|-------|------|---------|----------|
| `deepseek/deepseek-r1` | DeepSeek R1 | smart | $3.00 | $9.00 |
| `qwen/qwen3.5-397b` | Qwen 3.5 397B | smart | $0.60 | $3.60 |
| `minimax/minimax-m2.5` | MiniMax M2.5 | balanced | $0.30 | $1.20 |
| `qwen/qwen3-coder-480b` | Qwen 3 Coder | balanced | $0.60 | $2.70 |
| `openai/gpt-oss-120b` | GPT OSS 120B | fast | $0.15 | $0.60 |
| `mistral/devstral-small` | Devstral Small | fast | $0.12 | $0.36 |

> **Decision needed:** Which models to include at launch. This is a product decision — more models = more choice but also more support surface. Recommend starting with 4-6 and expanding.

#### 2. BYOK Models (API Key)

Available when user adds their own API key on the machine. Billed directly by the provider.

Each provider has a **curated list** of models in the catalog. The gateway only allows models in this list — users can't type arbitrary model IDs.

**Proposed curated lists:**

**Anthropic** (`provider: "anthropic"`):
| Model ID | Label |
|----------|-------|
| `anthropic/claude-opus-4-6` | Claude Opus 4.6 |
| `anthropic/claude-sonnet-4-6` | Claude Sonnet 4.6 |
| `anthropic/claude-haiku-4-5` | Claude Haiku 4.5 |

**OpenAI** (`provider: "openai"`):
| Model ID | Label |
|----------|-------|
| `openai/o3` | o3 |
| `openai/o4-mini` | o4-mini |
| `openai/gpt-4.1` | GPT-4.1 |
| `openai/gpt-4.1-mini` | GPT-4.1 Mini |
| `openai/gpt-4.1-nano` | GPT-4.1 Nano |
| `openai/gpt-4o` | GPT-4o |
| `openai/gpt-4o-mini` | GPT-4o Mini |

**Google** (`provider: "google"`):
| Model ID | Label |
|----------|-------|
| `google/gemini-2.5-pro` | Gemini 2.5 Pro |
| `google/gemini-2.5-flash` | Gemini 2.5 Flash |
| `google/gemini-2.0-flash` | Gemini 2.0 Flash |

**OpenRouter** (`provider: "openrouter"`):
| Model ID | Label |
|----------|-------|
| TBD | TBD |

> **Decision needed:** OpenRouter is a pass-through — users can access any model. Do we curate a subset, or allow freeform model IDs for OpenRouter only?

> **Decision needed:** Google model IDs use preview suffixes today (e.g., `gemini-2.5-flash-preview-05-20`). Should we use stable aliases or track preview versions?

#### 3. Subscription Models (OAuth)

Available when user completes OAuth flow on the machine. No API key needed — uses their existing subscription. OAuth tokens are stored in the backend credentials table (not VM-local).

| Model ID | Label | Provider | OAuth Flow |
|----------|-------|----------|------------|
| `openai-codex/gpt-5.4` | GPT-5.4 | openai-codex | Device OAuth (PKCE), backend-stored |

> **Future:** Anthropic Max subscription could use a similar OAuth flow.

### Model Catalog Schema

No schema changes needed. The existing `model_catalog` table handles all three sources. We just need to:

1. **Add missing models** via migration (new BYOK entries, expanded platform list)
2. **Remove stale models** (disable old preview-suffixed Google entries)
3. **Keep `PROVIDER_MODELS` in types.ts in sync** or remove it entirely in favor of API-only data

### Config Assembly: Model Allowlist

Small logic change needed. With OAuth tokens stored in the backend, subscription models follow the same credential-gated pattern as BYOK:

```
Platform models  → always in allowlist
BYOK models      → only if machine has credential for that provider
Subscription     → only if machine has credential for that provider (OAuth token stored)
```

Today's code always includes subscription models in the allowlist. After the OAuth fix, `openai-codex` models should only appear when the machine has an `openai-codex` credential (meaning the user completed the OAuth flow).

The account-wide credential fallback must also be removed so the credential map accurately reflects what the machine actually has.

---

## Part 2: Credential Redesign

### Goal: Machine-Scoped Credentials

A credential belongs to one machine. No sharing, no fallback, no ambiguity.

### Schema: Clean Break

No users, no migration. Drop both old tables and create a clean one.

```sql
-- Drop old tables
DROP TABLE IF EXISTS machine_credentials;
DROP TABLE IF EXISTS account_credentials;

-- New credentials table: one credential per provider per machine
CREATE TABLE credentials (
    id SERIAL PRIMARY KEY,
    account_id INTEGER NOT NULL REFERENCES accounts(id),
    machine_id UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    credential_type TEXT NOT NULL DEFAULT 'api_key',
    encrypted_value TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    last_validated TIMESTAMPTZ,
    last_four TEXT,
    expires_at TIMESTAMPTZ,
    refresh_token TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT credentials_machine_provider_unique UNIQUE (machine_id, provider)
);

CREATE INDEX idx_credentials_account ON credentials(account_id);
CREATE INDEX idx_credentials_machine ON credentials(machine_id);
```

One credential per provider per machine. No junction table. No account-wide fallback. `account_id` kept for billing/audit queries only.

### Backend Changes

#### Remove Account-Wide Fallback (3 locations)

1. **Config assembly** — `machine_config.go:309-324`: Delete the `else` branch that falls back to `ListAccountCredentials`.

2. **Credential push** — `machine_config.go:~570`: Same pattern, delete the fallback.

3. **OAuth refresh loop** — `oauth_refresh.go:~65`: Same pattern, delete the fallback.

After removing fallback, if a machine has no credentials, `credentials` map is empty. Platform models still work. BYOK models are excluded from the allowlist. This is correct behavior.

#### Simplify Store Interface

```go
// Replace these:
ListAccountCredentials(ctx, accountID)
ListMachineCredentials(ctx, machineID)
LinkMachineCredential(ctx, machineID, credentialID)
UnlinkMachineCredential(ctx, machineID, credentialID)

// With these:
ListMachineCredentials(ctx, machineID)           // SELECT ... WHERE machine_id = $1
SetMachineCredential(ctx, machineID, cred)       // UPSERT by (machine_id, provider)
DeleteMachineCredential(ctx, machineID, provider) // DELETE by (machine_id, provider)
GetMachineCredentialWithValue(ctx, machineID, provider) // For decryption
```

#### Simplify API Endpoints

```
# Remove (account-wide):
GET    /accounts/{id}/credentials
PUT    /accounts/{id}/credentials/{provider}
DELETE /accounts/{id}/credentials/{credentialId}
POST   /accounts/{id}/credentials/{credentialId}/validate
POST   /accounts/{id}/credentials/{provider}/refresh

# Keep (machine-scoped), adjust to use machine_id directly:
GET    /accounts/{id}/machines/{mid}/credentials
PUT    /accounts/{id}/machines/{mid}/credentials/{provider}
DELETE /accounts/{id}/machines/{mid}/credentials/{provider}
POST   /accounts/{id}/machines/{mid}/credentials/{provider}/validate
```

The `PUT` endpoint creates or replaces the credential for that provider on that machine. No linking step needed.

#### Add Validation on Credential Creation

`ModelTab.tsx` currently skips validation. The new `PUT /machines/{mid}/credentials/{provider}` endpoint should validate the key before storing it (same logic as the existing `validateProviderKey()`). Return 422 if invalid.

### Frontend Changes

#### Delete Dead Code

| File | Lines | Status |
|------|-------|--------|
| `components/APIKeys.tsx` | 922 | Delete — unused |
| `components/CredentialSelector.tsx` | 186 | Delete — unused |
| `components/MachineCredentials.tsx` | 413 | Delete — replaced by ModelTab |

#### Update ModelTab.tsx

Change the "Add API Key" flow from:
```
putCredential(accountId, provider, ...) → linkMachineCredential(accountId, machineId, credId)
```
To:
```
putMachineCredential(accountId, machineId, provider, value)
```

Single call. Backend validates, encrypts, and stores with `machine_id`.

#### Remove `PROVIDER_MODELS` from types.ts

This hardcoded fallback duplicates the catalog. Remove it — ModelTab already fetches from the API.

### OAuth Flow: Backend-Stored Tokens

#### The Problem with VM-Local OAuth

Today, OpenAI Codex OAuth tokens are stored on the VM data volume in `auth-profiles.json`. This is an OpenClaw construct — the gateway reads it to discover provider credentials. OCM generates it at boot with fake nonce-as-key profiles so the gateway routes through the proxy.

The Codex OAuth flow (`codex_auth.go`) writes **real** OAuth tokens directly into `auth-profiles.json`. When the gateway makes API calls, it sends the real OAuth token as `Authorization: Bearer <token>` to the proxy. But the proxy authenticates requests by matching the token against the VM's nonce — and the OAuth token is not the nonce. **Result: 401 Unauthorized.**

This works on a local OpenClaw install (no proxy, gateway talks directly to `api.openai.com`) but fails on OCM.

#### The Fix: Store OAuth Tokens in the Backend

Move OAuth tokens into the same backend-stored credential flow as API keys. The proxy injects the token like any other credential — no passthrough, no special cases.

**New flow:**

```
Frontend → agent: codex-auth generate-url
Agent: generates PKCE challenge, saves verifier to /tmp, returns auth URL
User: completes OAuth in browser, copies callback URL
Frontend → agent: codex-auth exchange <callback_url>
Agent: exchanges code for tokens with OpenAI (existing PKCE flow)
Agent → backend: POST /api/agent/machines/{machineID}/oauth-token
  { provider: "openai-codex", access_token, refresh_token, expires_in }
Backend: encrypts + stores in credentials table (machine_id, provider, type="oauth")
Backend: pushes credential to metadata server
Backend: triggers config push (adds openai-codex to provider configs + model allowlist)
```

**What changes:**

| Component | Before | After |
|-----------|--------|-------|
| Token storage | `auth-profiles.json` on VM data volume | `credentials` table in DB |
| Proxy auth | Gateway sends OAuth token (fails nonce check) | Gateway sends nonce, proxy injects OAuth token |
| Token refresh | Not automatic | Backend refresh loop (every 2 min, existing infra) |
| Config assembly | `openai-codex` provider has `KeyOptional: true`, no apiKey | `openai-codex` provider has exec secret ref like any other |
| VM survival | Tokens on data volume (lost if VM recreated) | Tokens in DB (survives VM recreation) |

**Agent → Backend endpoint (new):**

```
POST /api/agent/machines/{machineID}/oauth-token
Authorization: Bearer <agent_token>

{
  "provider": "openai-codex",
  "access_token": "...",
  "refresh_token": "...",
  "expires_in": 3600
}
```

The agent already has `agentToken` for backend auth (same as usage reporting). This endpoint encrypts the tokens and stores them as a machine credential.

**Config assembly changes:**

When `openai-codex` credential exists in the machine's credential map:

```go
// Before: KeyOptional passthrough
providerConfigs["openai-codex"] = map[string]interface{}{
    "baseUrl": proxyBaseURL + "/openai-codex/v1",
    "models":  []interface{}{},
}

// After: Normal provider with exec secret ref
providerConfigs["openai-codex"] = map[string]interface{}{
    "baseUrl": proxyBaseURL + "/openai-codex/v1",
    "apiKey":  map[string]interface{}{
        "source": "exec", "provider": "ocm", "id": "openai-codex-key",
    },
}
```

When no `openai-codex` credential exists, the provider block is omitted entirely (no point configuring a provider with no credentials).

**Proxy provider changes:**

```go
// Before
func openaiCodexProvider() *Provider {
    return &Provider{
        Name:         "openai-codex",
        UpstreamHost: "api.openai.com",
        KeyOptional:  true,  // passthrough mode
        // InjectKey is nil
        ExtractToken: func(req *http.Request) string {
            return strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
        },
    }
}

// After
func openaiCodexProvider() *Provider {
    return &Provider{
        Name:         "openai-codex",
        UpstreamHost: "api.openai.com",
        KeyOptional:  false,
        InjectKey: func(req *http.Request, key, credType string) {
            req.Header.Set("Authorization", "Bearer "+key)
        },
        ExtractToken: func(req *http.Request) string {
            return req.Header.Get("x-api-key")  // nonce, like other providers
        },
    }
}
```

**`codex_auth.go` changes:**

- After token exchange, POST tokens to backend instead of writing `auth-profiles.json`
- Remove all `auth-profiles.json` writes for codex
- Remove `openclaw.json` mutation (model allowlist managed by config push)
- Keep PKCE flow unchanged (generate-url, exchange still run on agent)

**`auth-profiles.json` impact:**

The `generateAuthProfiles()` function continues to write nonce-based profiles for BYOK providers. It no longer needs an `openai-codex` profile. The gateway discovers openai-codex via `openclaw.json` provider config (with exec secret ref) instead of `auth-profiles.json`.

**Token refresh:**

The existing refresh loop (`oauth_refresh.go`) already supports OpenAI:

```go
var oauthTokenEndpoints = map[string]string{
    "google":       "https://oauth2.googleapis.com/token",
    "openai":       "https://auth.openai.com/v1/token",
}
```

With tokens in the credentials table, the loop will find them and auto-refresh before expiry. This is a significant improvement — today Codex tokens expire silently on the VM.

> **Note:** The refresh endpoint key is `"openai"`, not `"openai-codex"`. We need to either add an `"openai-codex"` entry or map the provider name during refresh lookup.

#### OAuth Token Refresh Architecture

**Background:** OAuth access tokens are short-lived (~1 hour for OpenAI). When they expire, the system needs to use the long-lived refresh token to obtain a new access token without re-authenticating the user.

**Design: Single refresh path — proxy-driven, inline.**

No background loop. No race conditions. The proxy checks token expiry on every OAuth request and refreshes inline when needed.

##### How It Works

```
Every API call through the proxy:
  1. Look up credential from metadata (includes ExpiresAt)
  2. Is it OAuth AND expires within 10 minutes?
     No  → use token as-is, forward to upstream
     Yes → call backend to refresh before forwarding
  3. Forward to upstream with (possibly refreshed) token
  4. If upstream returns 401 (token revoked, not just expired):
     → return 401 to gateway, don't retry
```

**Proxy code in `handler.go`:**

```go
// Between step 6 (key lookup) and step 9 (build upstream request):

// Pre-flight refresh: token near expiry
if credType == "oauth" && keyExpiresAt != nil && time.Until(*keyExpiresAt) < 10*time.Minute {
    newToken, newExpiry, err := p.refreshCredential(cfg.MachineID, provider.Name)
    if err != nil {
        slog.Warn("apiproxy.oauth_preflight_refresh_failed",
            "machine_id", cfg.MachineID, "provider", provider.Name, "error", err)
        // Continue with existing token — may still work if clock skew
    } else {
        realKey = newToken
        keyExpiresAt = &newExpiry
        cfg.LLMKeys[provider.Name] = metadata.CredentialEntry{
            Value: newToken, CredentialType: "oauth", ExpiresAt: &newExpiry,
        }
        p.metaSrv.UpdateMachineLLMKeys(srcIP, cfg.LLMKeys)
    }
}

// ... build and send upstream request ...

// Post-flight retry: upstream returned 401 (token revoked or stale)
if resp.StatusCode == http.StatusUnauthorized && credType == "oauth" {
    resp.Body.Close()
    newToken, newExpiry, err := p.refreshCredential(cfg.MachineID, provider.Name)
    if err != nil {
        slog.Warn("apiproxy.oauth_retry_refresh_failed",
            "machine_id", cfg.MachineID, "provider", provider.Name, "error", err)
        // Return a clear error — not the raw 401
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusBadGateway)
        json.NewEncoder(w).Encode(map[string]string{
            "error":    "oauth_refresh_failed",
            "provider": provider.Name,
            "detail":   err.Error(),
        })
        return
    }
    // Update metadata and retry once
    cfg.LLMKeys[provider.Name] = metadata.CredentialEntry{
        Value: newToken, CredentialType: "oauth", ExpiresAt: &newExpiry,
    }
    p.metaSrv.UpdateMachineLLMKeys(srcIP, cfg.LLMKeys)
    provider.InjectKey(upstreamReq, newToken, credType)
    resp, err = p.client.Do(upstreamReq)
    if err != nil {
        http.Error(w, "upstream request failed after token refresh", http.StatusBadGateway)
        return
    }
    // Fall through to normal response handling with fresh response
}
```

**Two checks, one flow:**

1. **Pre-flight** (before sending): If token expires within 10 minutes, refresh proactively. If refresh fails, try anyway with the old token.
2. **Post-flight** (after 401): If upstream rejects the token, refresh and retry once. If refresh fails, return 502 with `oauth_refresh_failed` error (not a raw 401, so the gateway/user can distinguish "your key is bad" from "the refresh system failed").

**Error responses the gateway sees:**

| Scenario | Proxy Returns | Meaning |
|----------|--------------|---------|
| Token valid | 200 (from upstream) | Normal |
| Token near-expiry, refresh succeeds | 200 (from upstream) | Transparent refresh |
| Token near-expiry, refresh fails, token still works | 200 (from upstream) | Lucky, clock skew saved us |
| Token expired/revoked, refresh succeeds, retry succeeds | 200 (from upstream) | Recovered |
| Token expired/revoked, refresh succeeds, retry still 401 | 401 (from upstream) | Refresh token also revoked, credential flagged |
| Token expired/revoked, refresh fails (backend down) | 502 `oauth_refresh_failed` | Transient, retry later |
| Token expired/revoked, refresh fails (`invalid_grant`) | 502 `oauth_reauth_required` | Credential flagged, user must reconnect |

#### Credential Health: Flagging Stale OAuth Connections

When a refresh token is permanently dead (`invalid_grant` from the provider, or a successful refresh followed by another 401), the credential is broken and no automated fix is possible. The user must re-do the OAuth flow.

**Detection — in the backend refresh endpoint:**

The `POST /api/internal/refresh-credential` endpoint distinguishes two failure modes:

```go
// Provider returns 400 with "invalid_grant" → permanent failure
// Provider returns 5xx or network error → transient failure

type RefreshResult struct {
    AccessToken string     `json:"access_token,omitempty"`
    ExpiresAt   *time.Time `json:"expires_at,omitempty"`
    Error       string     `json:"error,omitempty"`
    Permanent   bool       `json:"permanent"`  // true = reauth required
}
```

**Flagging — credential status column:**

Add a `status` column to the `credentials` table:

```sql
CREATE TABLE credentials (
    ...
    status TEXT NOT NULL DEFAULT 'active',  -- 'active', 'reauth_required', 'invalid'
    status_detail TEXT,                      -- e.g., "invalid_grant at 2026-03-25T13:00:00Z"
    ...
);
```

When the backend refresh endpoint gets `invalid_grant`:
1. Update credential: `SET status = 'reauth_required', status_detail = 'refresh token revoked'`
2. Return `{ "error": "oauth_reauth_required", "permanent": true }` to proxy
3. Proxy returns 502 with `oauth_reauth_required` to gateway

**Notification — the user sees it:**

The frontend polls machine status (already does this for machine health). The credentials list endpoint includes `status`:

```json
GET /accounts/{id}/machines/{mid}/credentials

[
  {
    "provider": "openai-codex",
    "credential_type": "oauth",
    "status": "reauth_required",
    "status_detail": "refresh token revoked",
    "last_four": null
  }
]
```

**`ModelTab.tsx`** and **`OpenAICodexConnect.tsx`** check credential status:
- `status: "active"` → show "Connected" (green)
- `status: "reauth_required"` → show "Reconnect required" (red) with a button to redo the OAuth flow
- `status: "invalid"` → show "Invalid" (red) with a button to remove and re-add

**Re-authentication flow:**
1. User clicks "Reconnect" on ModelTab
2. Same OAuth flow as initial connect (`codex-auth generate-url` → OAuth → `codex-auth exchange`)
3. Agent POSTs new tokens to backend
4. Backend updates credential: new tokens, `status = 'active'`, `status_detail = NULL`
5. Config push adds provider back to allowlist

**What triggers each status:**

| Event | New Status | How Detected |
|-------|-----------|--------------|
| User completes OAuth | `active` | Agent POSTs tokens to backend |
| Refresh succeeds | `active` (unchanged) | Backend refresh endpoint |
| Refresh gets `invalid_grant` | `reauth_required` | Backend refresh endpoint |
| Retry after refresh still gets 401 | `reauth_required` | Proxy post-flight check |
| User removes credential | (row deleted) | DELETE endpoint |
| User re-authenticates | `active` | Agent POSTs fresh tokens |
| API key validation fails on creation | (not stored) | PUT endpoint rejects |
| API key starts returning 401 consistently | `invalid` | Future: could detect, but not in scope now |

##### Backend Refresh Endpoint

```
POST /api/internal/refresh-credential
Authorization: Bearer <agent_token>

{ "machine_id": "abc-123", "provider": "openai-codex" }

Response (200):
{
    "access_token": "new-token-...",
    "credential_type": "oauth",
    "expires_at": "2026-03-25T13:00:00Z"
}

Response (502):
{ "error": "provider refresh failed", "detail": "invalid_grant" }
```

The backend:
1. Loads credential from DB by `(machine_id, provider)`
2. Decrypts refresh_token
3. POSTs to provider token endpoint (`grant_type=refresh_token`, `client_id` only for Codex — public PKCE client, no secret)
4. Encrypts + stores new access_token and rotated refresh_token
5. Returns plaintext access_token + expiry to proxy

##### Why No Background Loop

| Concern | Background Loop + Proxy 401 | Proxy-Only (this design) |
|---------|----------------------------|--------------------------|
| Race condition | Two paths refresh same token concurrently | One path, no race |
| Complexity | Background goroutine + inline retry + CAS | Inline check only |
| Wasted refreshes | Refreshes even with no API traffic | Only refreshes when needed |
| Latency | Usually zero, ~200ms on miss | ~200ms once per refresh window |
| Code | ~80 lines | ~15 lines |

The tradeoff: the first request in the 10-minute refresh window pays ~200ms for the backend round-trip. Every subsequent request uses the cached fresh token. With 1-hour tokens, that's one slow request per 50 minutes of active usage.

##### Token Lifecycle Timeline

```
 0 min     User completes OAuth on machine.
           Agent POSTs tokens to backend.
           Backend stores in DB, pushes to metadata.
           access_token valid for 60 min.

 0-50 min  All API calls use the access_token directly. No refresh.

50 min     First API call in the refresh window (expires_at - 10 min).
           Proxy: "expires in 10 min, refreshing."
           → Backend decrypts refresh_token
           → POST to OpenAI → new access_token (60 min) + rotated refresh_token
           → Store in DB, return to proxy
           → Proxy updates metadata, forwards with new token
           (~200ms added to this one request)

51-110 min All API calls use the new token. No refresh.

110 min    Next refresh window. Cycle repeats.

Edge case: No API calls between 50-60 min
           Token expires at 60 min.
           First call at 65 min: proxy sees expired token, refreshes inline.
           Same flow, just the token is already expired instead of near-expiry.
           Works because the refresh_token is still valid (months-long lifetime).
```

##### What to Delete

- `oauth_refresh.go` — entire background loop file
- `StartOAuthRefreshLoop()` call in `server.go`
- `preRefreshOAuthCredentials()` in `oauth.go` — proxy handles refresh at request time

##### What to Keep

- `refreshOAuthToken()` in `oauth.go` — the actual token exchange logic, called by the backend refresh endpoint
- `POST /api/internal/refresh-credential` — new endpoint the proxy calls

##### Future: Agent-Local SQLite Cache

For higher resilience, the agent could cache credentials (including refresh tokens) in a local SQLite database. The proxy would refresh tokens locally (~10ms) without any backend round-trip. Deferred until needed — the backend round-trip is sufficient for launch.

---

## Part 3: Dead Code Cleanup

| Item | Action |
|------|--------|
| `APIKeys.tsx` (922 LOC) | Delete |
| `CredentialSelector.tsx` (186 LOC) | Delete |
| `MachineCredentials.tsx` (413 LOC) | Delete |
| `PROVIDER_MODELS` in `types.ts` | Delete |
| Account credential API endpoints | Delete handlers + routes |
| `ListAccountCredentials` store methods | Delete |
| `ListCredentialMachineLinks` store method | Delete |
| Account-wide fallback in 3 backend locations | Delete |
| `auth-profiles.json` codex writes in `codex_auth.go` | Delete (replaced by backend storage) |
| `openclaw.json` mutation in `codex_auth.go` | Delete (replaced by config push) |

---

## Implementation Order

No users, no phased migration. Ship the right design in one pass.

### Phase 1: Schema + Backend (do first)

**Database:**
1. Drop `machine_credentials` and `account_credentials`, create `credentials` table
2. Drop and recreate `model_catalog` with expanded seed data (platform + BYOK + subscription)

**Store layer:**
3. Replace all credential store methods with machine-scoped versions:
   - `ListMachineCredentials(ctx, machineID)`
   - `SetMachineCredential(ctx, machineID, cred)` — UPSERT by `(machine_id, provider)`
   - `DeleteMachineCredential(ctx, machineID, provider)`
   - `GetMachineCredentialWithValue(ctx, machineID, provider)`
   - `ListMachineCredentialsWithValues(ctx, machineID)`
4. Delete all account-wide credential store methods

**API endpoints:**
5. Delete all account-wide credential endpoints (`/accounts/{id}/credentials/*`)
6. Rewrite machine credential endpoints:
   - `GET /accounts/{id}/machines/{mid}/credentials` — list
   - `PUT /accounts/{id}/machines/{mid}/credentials/{provider}` — create/replace (validates key)
   - `DELETE /accounts/{id}/machines/{mid}/credentials/{provider}` — delete
7. Add `POST /api/agent/machines/{machineID}/oauth-token` — agent stores OAuth tokens
8. Add `POST /api/internal/refresh-credential` — proxy triggers refresh on 401

**Remove all fallback logic** (every location, not just 3):
9. `machine_config.go` — config assembly
10. `machine_config.go` — credential push
11. `oauth_refresh.go` — refresh loop
12. `runtime.go` — VM startup
13. `machine_agents.go` — agent model validation
14. `machine_config.go` — config preview

**Add `openai-codex` to LLM provider classification** (every location):
15. `llmProviderSet` in `machine_config.go`
16. Exec-secret ID mapping in `assembler.go`
17. Credential push classification in `machine_config.go`
18. Refresh repush classification in `oauth_refresh.go`
19. Startup LLM classification in `runtime.go`
20. `oauthTokenEndpoints` map — add `"openai-codex": "https://auth.openai.com/oauth/token"`

**Config assembly:**
21. Subscription models credential-gated (not always in allowlist)
22. `openai-codex` provider uses exec secret ref when credential exists, omitted when not
23. `handleSetMachineModel` validates credential availability, not just catalog membership

**OAuth refresh:**
24. Delete `oauth_refresh.go` (background loop), `StartOAuthRefreshLoop()` call, `preRefreshOAuthCredentials()`
25. Add `POST /api/internal/refresh-credential` endpoint — called by proxy inline

### Phase 2: Proxy + Agent

**Proxy:**
1. `openai-codex` provider: `KeyOptional: false`, `InjectKey` sets `Authorization: Bearer`, `ExtractToken` reads `x-api-key` (nonce)
2. Add pre-flight refresh: before forwarding OAuth requests, check `ExpiresAt` — if within 10 min, call backend `/api/internal/refresh-credential`, update metadata, forward with fresh token. If refresh fails, forward with existing token (may still work).
3. Add post-flight retry: if upstream returns 401 on an OAuth credential, refresh and retry once. If refresh fails, return 502 with `oauth_refresh_failed` (not raw 401) so the gateway can distinguish auth failure from refresh failure.

**Agent (`codex_auth.go`):**
4. `exchange`: POST tokens to backend instead of writing `auth-profiles.json`
5. `check`: query backend/metadata for `openai-codex` credential existence instead of reading `auth-profiles.json`
6. `test`: validate via proxy request (POST to `/v1/chat/completions` with `max_tokens=1` through proxy) instead of direct OpenAI call
7. Remove all `auth-profiles.json` writes for codex
8. Remove `openclaw.json` mutation (config push handles allowlist)
9. Make PKCE verifier file machine-scoped: `/tmp/ocm-codex-pkce-{machineID}.json` with TTL cleanup

**`auth-profiles.json`:**
10. Keep nonce-based proxy profile for `openai-codex` in `generateAuthProfiles()` — gateway discovers provider via this profile + `openclaw.json` provider config. Do NOT write real OAuth tokens here.

### Phase 3: Frontend

1. Delete `APIKeys.tsx`, `CredentialSelector.tsx`, `MachineCredentials.tsx`
2. Delete `PROVIDER_MODELS` from `types.ts`
3. Update `ModelTab.tsx`: single `PUT /machines/{mid}/credentials/{provider}` call, show validation result inline
4. Update `ChannelsTab.tsx`: use machine-scoped credential endpoint (not `putCredential + linkMachineCredential`)
5. Update `ChannelSetup.tsx`: same
6. Delete or update `OnboardingWizard.tsx` if it uses old credential flow
7. Update `ModelPicker.tsx`: subscription models disabled when no `openai-codex` credential (same pattern as BYOK)
8. Update `OpenAICodexConnect.tsx`: `check`/`test` now query backend, not local files

### Phase 4: Model Catalog Expansion

Migration only, no code changes.

1. Add BYOK models: Claude Haiku 4.5, o3, o4-mini, GPT-4.1 family, stable Google aliases
2. Expand platform models: DeepSeek R1, Devstral, Qwen Coder
3. Disable stale entries (old preview-suffixed Google models, old GPT-4o if replaced)
4. Decide Google alias strategy: stable aliases passed through (no `gateway_model_id`), or mapped

### Future: Agent-Local SQLite Cache

Deferred until needed. Not required for launch.

1. Add `modernc.org/sqlite` to agent
2. `credential_cache` table caches credentials locally
3. Proxy refresh-on-401 reads from SQLite instead of backend round-trip
4. Agent restart loads from SQLite (no backend dependency)
5. Async-sync refreshed tokens back to backend

---

## Codex Review Findings (Addressed)

Findings from OpenAI Codex review, with resolutions:

| # | Severity | Finding | Resolution |
|---|----------|---------|------------|
| 1 | CRITICAL | Phase 1 fallback removal breaks machines relying on account creds | No users. Clean slate. All fallback removed in Phase 1. |
| 2 | CRITICAL | Phase 2 (OAuth) depends on Phase 4 (schema) | Merged. Schema is Phase 1 step 1. OAuth storage works from day one. |
| 3 | IMPORTANT | Migration SQL unsafe for multi-linked/same-provider conflicts | No migration. Drop and recreate. No data to preserve. |
| 4 | IMPORTANT | `openai-codex` missing from LLM provider sets everywhere | Added explicit checklist: 6 locations in Phase 1 steps 15-20. |
| 5 | IMPORTANT | Model selection under-specified for credential-gated models | Phase 1 step 23: `handleSetMachineModel` validates credential availability. Phase 3 step 7: ModelPicker gates subscription models. |
| 6 | IMPORTANT | `codex-auth check/test` read `auth-profiles.json` | Phase 2 steps 5-6: rewrite to query backend/metadata. |
| 7 | IMPORTANT | Refresh race between background loop and proxy 401 | Eliminated. Single refresh path (proxy-driven inline). Background loop deleted. No race possible. |
| 8 | IMPORTANT | ChannelsTab, ChannelSetup, OnboardingWizard use old credential flow | Phase 3 steps 4-6: update all frontend flows. |
| 9 | IMPORTANT | `auth-profiles.json` still needed for gateway discovery | Phase 2 step 10: keep nonce-based profile, just don't write real tokens. |
| 10 | IMPORTANT | PKCE verifier file is global, races on multi-machine agent | Phase 2 step 9: machine-scoped file path + TTL cleanup. |
| 11 | MINOR | Google alias mapping unclear | Phase 4 step 4: explicit decision point. |
| 12 | MINOR | 401 after refresh failure indistinguishable | Phase 2 step 3: return 502 for transient failures, 401 for `invalid_grant`. |

---

## Open Questions

1. **Platform model list** — Which Nebius models to include at launch?
2. **OpenRouter** — Curated subset or freeform model IDs?
3. **Google model IDs** — Stable aliases (passed through) or mapped via `gateway_model_id`?
4. **Multi-key per provider** — Current design says no (unique on `machine_id, provider`). Correct?
