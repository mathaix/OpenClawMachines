# OpenAI Codex Config Assembly — Design Review

## Status

**Rejected.** The original proposal to emit `auth.profiles.openai-codex:default.mode = "oauth"` is incorrect for OCM's proxy-based architecture. See Analysis section below.

A separate bug in `AssembleConfig` was discovered during review and is documented in the Bug section.

## Original Problem Statement

The generated machine `.openclaw` config includes:

- `models.providers.openai-codex`
- `agents.defaults.model.primary = "openai-codex/gpt-5.4"`

but it does not include:

- `auth.profiles.openai-codex:default = { provider: "openai-codex", mode: "oauth" }`

The local working reference config (`~/.openclaw/openclaw.json`) does include that `auth.profiles` entry.

## Analysis: Why the Proposal Is Wrong

The proposal assumes OCM and local openclaw have the same auth architecture. They do not.

### Local OpenClaw

The gateway holds the **real OAuth access_token and refresh_token** in `auth-profiles.json`. The `mode: "oauth"` field tells the gateway to:

1. Track token expiry
2. Automatically refresh using the stored refresh_token when expired
3. Fall back to other profiles if refresh fails

This is correct — the gateway owns the full credential lifecycle.

### OCM

The gateway holds a **nonce** (a proxy authentication token) in `auth-profiles.json`. The request flow is:

```
gateway (sends nonce) → proxy (swaps nonce for real OAuth token) → upstream (chatgpt.com)
```

The proxy handles:
- OAuth token injection (replacing nonce with real access_token)
- Pre-flight refresh (when token expires within 10 min)
- Post-flight retry (on upstream 401, refresh + retry once)
- Permanent failure detection (invalid_grant → reauth_required)

The gateway never sees the real OAuth token.

### What `mode: "oauth"` Would Do in OCM

Setting `auth.profiles.openai-codex:default.mode = "oauth"` would cause the gateway to:

1. Look for `expires` in `auth-profiles.json` — the nonce has no expiry
2. Attempt to call `refreshOAuthTokenWithLock()` on the nonce — will fail because there's no refresh_token
3. Potentially reject the nonce as expired before it reaches the proxy

### Correct Behavior

The gateway should treat `openai-codex` like any other exec-secret-backed provider: pass the nonce through to the proxy without attempting refresh. No `auth.profiles` entry is needed. The `auth-profiles.json` file already contains the nonce with `type: "api_key"`, which is the correct type for a proxy nonce.

### Reference: Gateway Auth Mode Behavior

From the openclaw gateway source (`~/openclaw/src/agents/auth-profiles/oauth.ts`):

| Mode | Expiry tracking | Auto-refresh | Bearer injection |
|------|----------------|-------------|-----------------|
| `api_key` | No | No | Yes |
| `token` | Yes (reject if expired) | No | Yes |
| `oauth` | Yes | Yes (refresh_token exchange) | Yes |

For OCM's nonce-based auth, `api_key` is the correct mode — which is already what happens when no `auth.profiles` entry exists.

## Bug: `AssembleConfig` Produces Wrong openai-codex Provider Config

### Observed

`AssembleConfig` output for openai-codex:

```json
"openai-codex": {
  "baseUrl": "http://169.254.169.253:4000/openai-codex/v1",
  "apiKey": { "source": "exec", "provider": "ocm", "id": "openai-codex-key" },
  "models": []
}
```

### Expected

```json
"openai-codex": {
  "baseUrl": "http://169.254.169.253:4000/openai-codex/backend-api",
  "api": "openai-codex-responses",
  "apiKey": { "source": "exec", "provider": "ocm", "id": "openai-codex-key" },
  "models": []
}
```

### Root Cause

In `assembler.go`, the credential loop (line 390-398) iterates all credentials and calls `buildProviderConfig()` for each, including `openai-codex`. This creates a standard provider entry with `/v1` suffix and no `api` field.

The openai-codex special-case block (lines 413-421) is meant to override these values, but it checks `if _, exists := providerConfigs["openai-codex"]; !exists` — which is always false because the credential loop already created the entry. The override is dead code.

`AssembleSeedConfig` does not have this bug because it handles `openai-codex` inside the same loop that creates provider configs (lines 771-774).

### Impact

- `baseUrl` ends in `/v1` instead of `/backend-api` — the proxy won't route correctly
- Missing `api: "openai-codex-responses"` — the gateway won't use the Codex Responses API format
- Result: all openai-codex requests fail for machines using the `AssembleConfig` path

### Fix

Move the openai-codex handling inside the credential loop (before the entry is added to `providerConfigs`), or skip `openai-codex` from the generic loop and let the special-case block handle it entirely.

## Bug: `ocm-secrets` Does Not Hand Out Codex Nonce

The gateway in OCM gets provider credentials via the `ocm-secrets` exec provider. That helper returns the VM nonce (proxy auth token) only for IDs listed in `proxyKeyIDs`. The map currently includes Anthropic/OpenAI/Google/OpenRouter/Nebius but **excludes `openai-codex-key`** (`backend/cmd/ocm-secrets/main.go`).

Impact:

- When the gateway requests `openai-codex-key`, `ocm-secrets` responds with an error instead of the nonce.
- The API proxy receives no nonce and returns `403` → surfacing as `Token failed: no openai-codex credential configured` even after a successful OAuth exchange and live credential push.

Fix:

- Add `"openai-codex-key": true` to `proxyKeyIDs` in `backend/cmd/ocm-secrets/main.go`.
- Rebuild and ship the rootfs / agent bundle so the updated binary is on running VMs.

## Test Plan

1. `AssembleConfig` with openai-codex credential:
   - assert `models.providers.openai-codex.baseUrl` ends with `/openai-codex/backend-api`
   - assert `models.providers.openai-codex.api == "openai-codex-responses"`
   - assert no `auth.profiles` section exists

2. `AssembleSeedConfig` with openai-codex provider:
   - assert same baseUrl and api values
   - assert no `auth.profiles` section exists

3. Negative case:
   - when openai-codex is absent, no openai-codex provider entry should exist

4. `ocm-secrets` regression:
   - unit/integ: request `openai-codex-key` via stdin JSON → expect nonce echo, no error
   - E2E on running VM: `codex-auth test` should no longer return `no openai-codex credential configured`

## Non-Goals

This document should not lead to:

- Emitting `auth.profiles` for any provider in OCM
- Writing OAuth tokens into `openclaw.json`
- Changing the proxy's OAuth token injection or refresh logic
- Modifying `auth-profiles.json` format
