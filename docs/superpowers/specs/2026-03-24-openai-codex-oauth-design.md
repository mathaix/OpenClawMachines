# OpenAI Codex OAuth Integration — Design Spec

## Goal

Enable per-machine OpenAI Codex (ChatGPT subscription) authentication via the dashboard, with API call proxying for usage tracking.

## Context

OpenAI Codex uses OAuth (PKCE flow) to authenticate via ChatGPT Plus/Pro subscriptions. The OAuth tokens (access + refresh) are per-user and subject to refresh token rotation, so they cannot be shared across machines. Each machine needs its own OAuth session.

OpenClaw already supports `openclaw models auth login --provider openai-codex` which handles the full OAuth flow — token exchange, file writing, config updates. The command supports a headless mode where it prints an auth URL and accepts a pasted callback URL/code.

## Architecture

### OAuth Flow (Dashboard → VM)

```
User clicks "Connect OpenAI Codex" in machine credentials tab
  → Backend sends PTY command: openclaw models auth login --provider openai-codex
  → PTY output contains auth URL (https://auth.openai.com/oauth/authorize?...)
  → Dashboard displays URL as clickable link
  → User opens URL in local browser, logs into OpenAI
  → Browser redirects to localhost:1455/callback?code=XXX (nothing listening — page fails)
  → User copies URL from browser address bar
  → User pastes URL into dashboard text field
  → Dashboard sends pasted URL to PTY
  → OpenClaw exchanges code for tokens, writes:
      - ~/.openclaw/agents/main/agent/auth-profiles.json (OAuth tokens)
      - ~/.openclaw/openclaw.json (auth profile config + model catalog entry)
  → Gateway restart required to pick up new auth profile
```

### API Proxy for Usage Tracking

OpenAI Codex requests are routed through the existing API proxy at `192.168.100.1:4000` for usage tracking. Unlike other providers where the proxy injects an API key, openai-codex uses **token passthrough** — the gateway sends the OAuth Bearer token in the Authorization header, and the proxy forwards it as-is.

```
Gateway → http://192.168.100.1:4000/openai-codex/v1/chat/completions
  (Authorization: Bearer <oauth_access_token>)
    → Proxy forwards to https://api.openai.com/v1/chat/completions
    (Authorization: Bearer <oauth_access_token> — passed through)
    → Proxy parses response for usage (model, input_tokens, output_tokens)
    → Proxy records usage with source="subscription", cost=0
```

### File Formats (Written by OpenClaw)

**auth-profiles.json** — new profile added alongside existing proxy profiles:
```json
{
  "openai-codex:default": {
    "type": "oauth",
    "provider": "openai-codex",
    "access": "<jwt_access_token>",
    "refresh": "<refresh_token>",
    "expires": 1775181197485
  }
}
```

**openclaw.json** — sections added by `openclaw models auth login`:
```json
{
  "auth": {
    "profiles": {
      "openai-codex:default": {
        "provider": "openai-codex",
        "mode": "oauth"
      }
    }
  },
  "agents": {
    "defaults": {
      "models": {
        "openai-codex/gpt-5.4": {}
      }
    }
  }
}
```

## Components

### 1. Backend: New `openai-codex` API Proxy Provider

**File:** `backend/internal/apiproxy/providers.go`

Add `openaiCodexProvider()` to `initProviders()`:

- `Name`: `"openai-codex"`
- `UpstreamHost`: `"api.openai.com"`
- `Scheme`: `"https"`
- `AllowedHosts`: `["api.openai.com"]`
- `KeyOptional`: `true` — no key injection from metadata server; token comes from gateway
- `InjectKey`: No-op (nil or empty). The Authorization header from the gateway is passed through.
- `ExtractToken`: Extract from `Authorization: Bearer` header (same as openai provider)
- `ParseJSONUsage` / `ParseSSEEvent`: Same as openai provider (OpenAI API format)

**Key change in handler.go:** The proxy currently strips `Authorization` headers (line 166). For `openai-codex`, we need to pass through the Authorization header since the gateway provides the OAuth token. Two approaches:

- **Option A:** Don't strip Authorization when `provider.KeyOptional && realKey == ""` (no key to inject, so keep the client's header)
- **Option B:** Add a `PassthroughAuth bool` field to Provider; skip stripping when true

Option A is simpler and handles the general case.

**Pricing:** `gpt-5.4` pricing in `pricing.go`. Since this is a subscription model, set to `{InputPerMToken: 0, OutputPerMToken: 0}` for cost tracking with $0 cost. Usage (token counts) is still recorded.

**Usage source:** Set `source = "subscription"` for openai-codex (not "byok" or "platform").

### 2. Backend: Config Assembly

**File:** `backend/internal/configassembly/assembler.go`

The config assembly currently builds `models.providers` from `params.Credentials`. OpenAI Codex tokens live on the VM (not in credentials), so config assembly does NOT add the openai-codex provider — OpenClaw's `models auth login` command updates `openclaw.json` directly.

However, the first-boot seed config needs a `models.providers["openai-codex"]` entry with the proxy `baseUrl` so that if a user connects OpenAI Codex after first boot, the provider is already configured to route through the proxy. Two options:

- **Option A:** Always include `openai-codex` provider in seed config (like nebius is always included)
- **Option B:** After OAuth completes, update `openclaw.json` via a post-auth PTY command to add the proxy baseUrl

**Decision: Option A** — always include `openai-codex` provider pointing to the proxy. This way, when OpenClaw's auth command adds the auth profile, the provider baseUrl is already there. The provider config includes no `apiKey` field since auth comes from auth-profiles.json.

```json
{
  "models": {
    "providers": {
      "openai-codex": {
        "baseUrl": "http://192.168.100.1:4000/openai-codex/v1"
      }
    }
  }
}
```

### 3. Frontend: Machine Credentials Tab

**File:** `frontend/src/components/MachineCredentials.tsx`

Add "Connect OpenAI Codex" button to the machine credentials tab. When clicked:

1. Show a modal with instructions
2. Backend sends PTY command to VM
3. Poll/stream PTY output for the auth URL (regex: `https://auth\.openai\.com/oauth/authorize\?.*`)
4. Display the URL as a clickable button/link ("Open in browser")
5. Show text input: "After logging in, copy the URL from your browser and paste it here"
6. On paste, send the URL to PTY
7. Poll PTY output for success message ("Auth profile: openai-codex")
8. Show success state, prompt for gateway restart

**Prerequisite:** The frontend needs a way to send commands to the VM's PTY and read output. This may already exist for the workspace terminal. If not, a new API endpoint is needed:

```
POST /api/accounts/{accountId}/machines/{machineId}/pty/exec
Body: { command: "openclaw models auth login --provider openai-codex" }
Response: { output: "...", done: bool }
```

Or leverage the existing WebSocket PTY connection.

### 4. Frontend: Connection Status Display

The machine credentials tab should show whether OpenAI Codex is connected. Since tokens aren't in the DB, we check the VM state:

- **While running:** Query PTY or a lightweight endpoint to check if `openai-codex:default` exists in auth-profiles.json
- **While stopped:** Unknown — show "Connect" button regardless; if already connected, the auth command will update/overwrite

Simpler approach: just always show the "Connect OpenAI Codex" button. If already connected, re-running the auth command refreshes the tokens (harmless).

## Non-Goals

- **Per-token billing** — OpenAI Codex is subscription-based; cost tracking records $0
- **Budget enforcement** — No per-token cost means no budget to enforce
- **Token storage in DB** — Tokens live on VM data volume only; gateway handles refresh
- **Multi-provider OAuth** — This is OpenAI Codex only; other providers can be added later
- **Automatic model default** — User must manually set openai-codex/gpt-5.4 as default if desired

## Testing

- **E2E gateway test:** Add `TestGatewayE2E_ProxyOpenAICodex` — register openai-codex provider, send request with Bearer token, verify passthrough to OpenAI API
- **Config assembly test:** Verify openai-codex provider always included in seed config
- **Pricing test:** Verify gpt-5.4 returns 0 cost
- **Manual test:** Full OAuth flow from dashboard → paste URL → verify gateway picks up new model
