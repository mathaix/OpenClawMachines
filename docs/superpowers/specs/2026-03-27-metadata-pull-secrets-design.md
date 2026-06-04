# Metadata Server Pull-Based Secrets

**Date:** 2026-03-27
**Status:** Approved
**Problem:** Gateway crashes with `SecretRefResolutionError` for channel tokens because metadata server has empty cache at startup

## Problem Statement

When a channel (e.g., Telegram) is connected on a running machine, the gateway receives an exec secret ref (`channel-telegram-botToken`) but `ocm-secrets` cannot resolve it because the metadata server's in-memory cache doesn't have the channel key. This is a race condition between config delivery and credential availability.

The current push-based approach requires multiple push endpoints (`UpdateVMChannelKeys`, `pushChannelKeysToVM`, `RemoveVMChannelKey`) and careful ordering — all of which are fragile and still fail when the credential is stored after VM creation.

## Solution

Make the metadata server **pull-based** for platform secrets. On cache miss (or TTL expiry), the metadata server fetches credentials from the backend API, caches them, and returns them to `ocm-secrets`.

### How It Works

```
Gateway startup
  → spawns ocm-secrets (exec provider)
    → ocm-secrets calls GET http://192.168.100.1/v1/secrets
      → metadata server checks in-memory cache
        → HIT + fresh: return cached value
        → MISS or stale: fetch from backend API, cache with TTL, return
```

### Backend Endpoint

`GET /api/agent/machines/{machineID}/secrets`

- **Auth:** `AgentToken` (same as heartbeat, `X-Agent-Token` header)
- **Response:** `map[string]string` keyed by the same secret IDs that `ocm-secrets` requests:
  ```json
  {
    "channel-telegram-botToken": "123:ABC-DEF",
    "channel-discord-token": "MTIz..."
  }
  ```
- The backend decrypts channel credentials and maps them to the `channel-{provider}-{fieldName}` format
- Only returns channel credentials (LLM keys use the proxy/nonce path, not this endpoint)

### Metadata Server Changes

Add to `MachineConfig`:
```go
type secretCache struct {
    secrets   map[string]string
    fetchedAt time.Time
}
```

In `handleSecrets`:
1. Build merged map from `cfg.Secrets` (platform) + `cfg.ChannelKeys` (existing in-memory) as today
2. If any requested ID is missing from the merged map, check the pull cache
3. If pull cache is missing or stale (TTL expired), fetch from backend API
4. Merge fetched secrets into response
5. Return

**TTL:** 60 seconds. Balances freshness (token rotation takes effect within 60s on next gateway restart) against backend load (at most one call per 60s per VM).

**Cache scope:** Per-VM (`MachineConfig`). Cleared when VM is unregistered.

### What Gets Removed

The push-based channel key plumbing from `db9d1b8`:

- `Server.pushChannelKeysToVM()` in `channel_config.go`
- `Server.removeChannelKeyFromVM()` in `channel_config.go`
- `Client.UpdateVMChannelKeys()` in `agentclient/client.go`
- `Client.RemoveVMChannelKey()` in `agentclient/client.go`
- `handleUpdateVMChannelKeys` in `agentapi/handlers.go`
- `handleRemoveVMChannelKey` in `agentapi/handlers.go` (if exists)
- `UpdateChannelKeys()` in orchestrator interface + implementations
- `UpdateMachineChannelKeys()` in metadata registrar interface
- Channel key push/remove calls in `handleChannelConnect` and `handleChannelDisconnect`
- `RestartGateway` client call from connect/disconnect (gateway restart is still triggered, just without the metadata push prerequisite)

### What Stays Unchanged

- `ocm-secrets` binary (no changes, stays in rootfs)
- LLM key flow (proxy/nonce swap path)
- `RegisterMachine` still accepts `ChannelKeys` for backward compat (existing VMs may still rely on it during transition)
- `handleSecrets` still merges `cfg.Secrets` and `cfg.ChannelKeys` — the pull cache is an additional source
- Init script / gateway env (`OCM_SECRETS_AUTH` still required for `ocm-secrets` auth)
- Config assembly — still generates exec secret refs for channel tokens

### Connect/Disconnect Flow (After)

**Connect:**
1. Save credential to DB
2. Push config ops (exec ref) to VM
3. Restart gateway
4. Gateway starts → `ocm-secrets` → metadata server → cache miss → fetches from backend → returns token

**Disconnect:**
1. Remove capability + credential from DB
2. Push config ops (remove channel section) to VM
3. Restart gateway
4. Gateway starts without channel section → never asks for the secret

### Token Rotation

1. User updates credential via API (saved to DB)
2. Push config ops + restart gateway
3. Gateway starts → `ocm-secrets` → metadata server → cache expired (TTL) → fetches fresh from backend → returns new token

### Error Handling

- Backend unreachable: `ocm-secrets` gets error response, gateway fails to start (same as today — no silent fallback)
- Credential not in DB: backend returns empty for that ID, `ocm-secrets` reports error, gateway fails (correct behavior — the exec ref shouldn't be in config if the credential doesn't exist)

## Files Changed

| File | Change |
|------|--------|
| `backend/internal/api/server.go` | Add `GET /api/agent/machines/{machineID}/secrets` route |
| `backend/internal/api/agent_handlers.go` (or similar) | New handler to fetch+decrypt channel creds |
| `backend/internal/metadata/metadata.go` | Add `secretCache` struct, TTL const |
| `backend/internal/metadata/server_linux.go` | Add pull-on-miss logic to `handleSecrets` |
| `backend/internal/metadata/server_linux_test.go` | Test cache miss → fetch → cache hit flow |
| `backend/internal/api/channel_config.go` | Remove `pushChannelKeysToVM`, `removeChannelKeyFromVM`, simplify connect/disconnect |
| `backend/internal/agentclient/client.go` | Remove `UpdateVMChannelKeys`, `RemoveVMChannelKey` |
| `backend/internal/agentapi/handlers.go` | Remove `handleUpdateVMChannelKeys` |
| `backend/internal/agentapi/server.go` | Remove channel-keys route |
| `backend/internal/orchestrator/orchestrator.go` | Remove `UpdateChannelKeys` from interface |
| `backend/internal/orchestrator/firecracker_linux.go` | Remove `UpdateChannelKeys` impl |
| `backend/internal/orchestrator/firecracker_stub.go` | Remove `UpdateChannelKeys` stub |

## Testing

- Unit test: metadata server `handleSecrets` with empty cache → mock backend returns credentials → verify response + cache populated
- Unit test: TTL expiry → re-fetch from backend
- Unit test: backend returns empty → error propagated
- Gateway e2e: channel with exec ref resolves correctly (existing test should pass)
