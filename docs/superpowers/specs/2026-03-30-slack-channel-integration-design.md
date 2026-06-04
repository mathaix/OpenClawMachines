# Slack Channel Integration Design

**Date:** 2026-03-30
**Branch:** slackchannel
**Status:** Reviewed (Codex-reviewed, findings addressed)

## Problem

Slack is listed in the Channels UI but shows "Soon". The backend has no validation, no token mapping, and no config injection for Slack. The CLI doesn't support `ocm channels setup slack`. Users cannot connect Slack to their machines.

## Key Constraint: Dual Tokens

Unlike Telegram (one bot token) and Discord (one token), Slack requires **two tokens**:

| Token | Format | Source | Purpose |
|-------|--------|--------|---------|
| Bot User OAuth Token | `xoxb-...` | OAuth & Permissions page | API calls, message sending |
| App-Level Token | `xapp-...` | Basic Information → App-Level Tokens | Socket Mode WebSocket connection |

The current channel framework assumes one token per channel via `ChannelTokenFieldName map[string]string`. This must be extended.

## Design

### 1. Token Field Mapping Refactor

Replace `ChannelTokenFieldName` in `configassembly/assembler.go` with a struct-based mapping:

```go
type ChannelTokenMapping struct {
    FieldName string // config field name in openclaw.json (e.g. "botToken")
    Provider  string // credential provider name in DB (e.g. "slack")
}

var ChannelTokenFields = map[string][]ChannelTokenMapping{
    "telegram": {{FieldName: "botToken", Provider: "telegram"}},
    "discord":  {{FieldName: "token", Provider: "discord"}},
    "slack": {
        {FieldName: "botToken", Provider: "slack"},
        {FieldName: "appToken", Provider: "slack-app"},
    },
}
```

Add a reverse lookup helper for `agent_auth.go`:

```go
// ProviderToChannel maps a credential provider name back to its channel ID and field name.
// Used by agent_auth.go to build correct secret IDs (channel-{channelID}-{fieldName}).
func ProviderToChannel(provider string) (channelID, fieldName string, ok bool) {
    for chID, mappings := range ChannelTokenFields {
        for _, m := range mappings {
            if m.Provider == provider {
                return chID, m.FieldName, true
            }
        }
    }
    return "", "", false
}
```

### 2. Registry Migration

`buildChannelConfig()` calls `s.store.GetRegistryEntry(ctx, channelID)` and fails if no entry exists. Telegram and Discord are seeded in migration 017. Slack needs a new migration:

```sql
-- Add Slack channel to registry
INSERT INTO registry_entries (id, type, name, description, config_template, required_credentials, status, sort_order)
VALUES
    ('slack', 'channel', 'Slack', 'Slack messaging integration',
     '{"channels":{"slack":{"enabled":true,"dmPolicy":"pairing","groups":{"*":{"requireMention":true}}}}}'::jsonb,
     ARRAY['slack'], 'active', 3)
ON CONFLICT (id) DO NOTHING;
```

The `config_template` follows the same pattern as Telegram (enabled, dmPolicy, groups with requireMention default).

### 3. Credential Storage

Slack uses two credential entries per machine:

| Provider | CredentialType | Stores | Last Four |
|----------|---------------|--------|-----------|
| `"slack"` | `"token"` | `xoxb-...` bot token | Last 4 of bot token |
| `"slack-app"` | `"token"` | `xapp-...` app-level token | Last 4 of app token |

Both are encrypted via `crypto.Encrypt()` and stored in the credentials table. The `"slack-app"` provider is internal — the UI groups both under the "Slack" channel.

### 4. Token Validation

**Bot token (`xoxb-`):** Call Slack's `auth.test` API:
```
POST https://slack.com/api/auth.test
Authorization: Bearer xoxb-...
```
Response `{"ok": true}` means valid. `{"ok": false, "error": "invalid_auth"}` means invalid.

**App token (`xapp-`):** Prefix-only validation — check that it starts with `xapp-`. No simple REST endpoint exists to validate app-level tokens without opening a WebSocket.

### 5. Channel Connect API Changes

Extend the `handleChannelConnect` request body:

```go
var req struct {
    Token    string                 `json:"token"`               // primary token (bot token)
    AppToken string                 `json:"app_token,omitempty"` // Slack app-level token
    Settings map[string]interface{} `json:"settings,omitempty"`
}
```

**Connect flow for Slack:**
1. Validate bot token via `auth.test` API
2. Validate app token prefix (`xapp-`)
3. Encrypt and store bot token as credential (provider `"slack"`)
4. Encrypt and store app token as credential (provider `"slack-app"`)
5. Enable capability, save overrides, build merged config (standard)
6. Push config + restart gateway (standard)

**Disconnect flow:** Delete both `"slack"` and `"slack-app"` credentials. Delete the primary credential first; if the second delete fails, log a warning (matches existing disconnect error handling — best-effort, not transactional). An orphaned `"slack-app"` credential with no enabled capability is inert.

**Update token flow:** Accept optional `app_token` alongside `token`. Update both if provided. Same best-effort approach — update primary first, then secondary.

**Settings flow:** `handleChannelSettings()` calls `decryptChannelToken(machineID, channelID)` which currently returns one token. For Slack, `buildChannelConfig` must load ALL tokens for the channel. Extend `buildChannelConfig` to accept a map of tokens (or load them internally via `ChannelTokenFields`) instead of a single token string. For single-token channels, behavior is unchanged.

### 6. Config Assembly & Secret Resolution

**Five callsites** use `ChannelTokenFieldName` and must all migrate to `ChannelTokenFields` + `ProviderToChannel()`:

1. **`channel_config.go:buildChannelConfig()`** — injects exec secret refs during channel connect/settings/update-token. Must iterate `ChannelTokenFields[channelID]` and inject a ref for each field.

2. **`assembler.go` (lines 664-683)** — full config assembly injects exec refs keyed by provider via `ChannelCredentialValues`. For `"slack-app"`, the provider doesn't match a channel name, so `channels[provider]` would look for `channels["slack-app"]` (wrong). Must use `ProviderToChannel()` to map back to `channels["slack"]` and inject under the correct field.

3. **`agent_auth.go:handleAgentAuthGetSecrets()`** — builds secret ID map from credentials. Must use `ProviderToChannel()` instead of `ChannelTokenFieldName[cred.Provider]` so that provider `"slack-app"` maps to secret ID `channel-slack-appToken` (not `channel-slack-app-appToken`).

4. **`machine_config.go:injectChannelCredentialMarkers()`** — config preview. Must use `ProviderToChannel()` to map `"slack-app"` back to `channels["slack"]["appToken"]` instead of looking for `channels["slack-app"]`.

5. **`machines/runtime.go` (line 449)** — cold-start seed config. Builds the initial `openclaw.json` before first boot. Uses `ChannelTokenFieldName[cap.EntryID]` to inject exec secret refs for channel capabilities. Must iterate `ChannelTokenFields[cap.EntryID]` to inject refs for ALL token fields, not just the first. Without this fix, newly created Slack machines would get `botToken` but miss `appToken`, breaking Socket Mode on first boot.

**Resulting config for Slack:**
```json
{
  "botToken": {"source": "exec", "provider": "ocm", "id": "channel-slack-botToken"},
  "appToken": {"source": "exec", "provider": "ocm", "id": "channel-slack-appToken"}
}
```

**Secret ID mapping:**
- Credential provider `"slack"` → secret ID `channel-slack-botToken`
- Credential provider `"slack-app"` → secret ID `channel-slack-appToken`

The existing `ocm-secrets` binary and metadata server need no changes — they already pass through any secret ID from the metadata endpoint.

### 7. Frontend Changes

**`ChannelsTab.tsx`** — Update the Slack channel definition:

```typescript
{
  id: "slack",
  label: "Slack",
  shortDesc: "Bot token · Socket Mode",
  credentialProvider: "slack",
  hasSettings: true,
  instructions: {
    title: "Connect Slack",
    steps: [
      "Go to api.slack.com/apps and create a New App from manifest",
      "Install the app to your workspace (Install App → Install to Workspace)",
      "Copy the Bot User OAuth Token (xoxb-...) from OAuth & Permissions",
      "Generate an App-Level Token with connections:write scope from Basic Information",
    ],
    link: "https://api.slack.com/apps",
    linkLabel: "Open Slack API",
  },
}
```

**Setup dialog** — Add a second input field for the app token when the channel is Slack. The dialog currently shows one "Bot Token" field. For Slack, show:
1. "Bot Token" — for the `xoxb-` token
2. "App Token" — for the `xapp-` token

The connect button calls the API with both `token` and `app_token`.

**`types.ts`** — Add `"slack"` to `CredentialProvider` type and `CREDENTIAL_PROVIDERS` array. Do NOT add `"slack-app"` to `CREDENTIAL_PROVIDERS` — it is an internal provider that should not appear in onboarding, generic provider pickers, or the automation-channel UI. Add `"slack-app"` only to the `CredentialProvider` union type (for TypeScript compatibility) and to the backend `validProviders` map.

### 8. CLI Changes

**`channels_setup.go`** — Add Slack to `channelInstructions`:

```go
"slack": {
    DisplayName: "Slack Bot",
    TokenURL:    "https://api.slack.com/apps",
    Steps: []string{
        "Go to api.slack.com/apps → Create New App → From manifest",
        "Install to Workspace, copy Bot Token (xoxb-...)",
        "Generate App-Level Token with connections:write scope (xapp-...)",
    },
    Provider: "slack",
},
```

The CLI setup wizard prompts for both tokens sequentially (bot token first, then app token). It stores both via the channel connect API.

**`validate.go`** — Add `validateSlackBotToken()` (auth.test call) and `validateSlackAppToken()` (prefix check).

**`providers.go`** — Add `"slack"` to `validProviderNames`, add `"slack"` case to `inferCredentialType` (returns `"token"`).

### 9. Files Changed

| File | Change |
|------|--------|
| `backend/migrations/055_slack_channel.sql` | New migration: seed Slack channel registry entry |
| `backend/internal/configassembly/assembler.go` | Replace `ChannelTokenFieldName` with `ChannelTokenFields` struct, add `ProviderToChannel()`. Update exec ref injection loop (lines 664-683) to use `ProviderToChannel()` |
| `backend/internal/api/credentials.go` | Add `"slack"`, `"slack-app"` to `validProviders`, add `validateSlackBotToken()`, `validateSlackAppToken()` |
| `backend/internal/api/channel_config.go` | Add `app_token` to connect/update/disconnect requests, save second credential, iterate `ChannelTokenFields` in `buildChannelConfig`, extend settings flow to load all tokens |
| `backend/internal/api/agent_auth.go` | Use `ProviderToChannel()` for secret ID mapping |
| `backend/internal/api/machine_config.go` | Update `injectChannelCredentialMarkers()` to use `ProviderToChannel()` for config preview |
| `backend/internal/machines/runtime.go` | Update cold-start seed config to iterate `ChannelTokenFields` for multi-token channels |
| `frontend/src/pages/machine-tabs/ChannelsTab.tsx` | Add Slack instructions, second token input for Slack |
| `frontend/src/lib/types.ts` | Add `"slack"` to `CredentialProvider` union and `CREDENTIAL_PROVIDERS`, add `"slack-app"` to union type only |
| `cli/internal/commands/channels_setup.go` | Add Slack to `channelInstructions` |
| `cli/internal/commands/validate.go` | Add Slack validation functions |
| `cli/internal/commands/providers.go` | Add `"slack"` to provider lists |

### 10. What Does NOT Change

- `ocm-secrets` binary — already passes through any secret ID
- Metadata server — already serves all secrets from the backend API
- Database schema — uses existing credentials table
- Init script — no Slack-specific env vars needed
- Skills registry — Slack skill already registered in migration 039

### 11. Testing

**Backend unit tests:**
- `validateSlackBotToken` with valid/invalid tokens (mock Slack API)
- `validateSlackAppToken` prefix check
- `buildChannelConfig` with Slack channel — verify both exec refs injected
- `handleAgentAuthGetSecrets` — verify both `channel-slack-botToken` and `channel-slack-appToken` emitted

**Gateway E2E tests:**
- Channel connect with both tokens → verify assembled config has both exec refs
- Channel disconnect → verify both credentials removed
- Channel update token → verify both updated

**CLI tests:**
- `channels setup slack` accepted (not "unsupported channel")
- Validation functions work correctly

### 12. Slack App Manifest (for docs/user reference)

Users create their Slack app from this manifest:

```json
{
  "display_information": {
    "name": "openclaw",
    "description": "OpenClaw AI agent gateway",
    "background_color": "#2c2d30"
  },
  "features": {
    "bot_user": {
      "display_name": "openclaw",
      "always_online": true
    }
  },
  "oauth_config": {
    "scopes": {
      "bot": [
        "app_mentions:read", "channels:history", "channels:read",
        "chat:write", "files:read", "files:write",
        "groups:history", "groups:read",
        "im:history", "im:read", "im:write",
        "reactions:read", "reactions:write",
        "team:read", "users:read", "users:read.email"
      ]
    }
  },
  "settings": {
    "event_subscriptions": {
      "bot_events": ["app_mention", "message.channels", "message.groups", "message.im"]
    },
    "interactivity": {"is_enabled": false},
    "org_deploy_enabled": false,
    "socket_mode_enabled": true,
    "token_rotation_enabled": false
  }
}
```
