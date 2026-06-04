# Channel Credential Delivery — Design Document

**Date:** 2026-03-27
**Status:** Fixed — reverted to plaintext injection
**Problem:** Gateway crashes with `SecretRefResolutionError` for `channel-telegram-botToken`

## Problem Statement

When a machine has Telegram (or any channel) credentials configured, the gateway crashes at startup because it cannot resolve the `channel-telegram-botToken` SecretRef via the `ocm-secrets` exec provider.

## Root Cause Analysis

The channel credential delivery has a **race condition** between config assembly (which generates SecretRefs) and credential availability in the metadata server (which resolves them).

### How Config Assembly Works

`configassembly/assembler.go` injects SecretRefs into the gateway config for any channel with stored credentials:

```json
{
  "channels": {
    "telegram": {
      "enabled": true,
      "botToken": {
        "source": "exec",
        "provider": "ocm",
        "id": "channel-telegram-botToken"
      }
    }
  }
}
```

### How SecretRef Resolution Works

1. Gateway starts and encounters the SecretRef
2. Gateway calls `ocm-secrets` binary (exec provider)
3. `ocm-secrets` reads nonce from `/run/ocm-nonce`
4. `ocm-secrets` calls `GET http://{gateway_ip}/v1/secrets` with `X-Metadata-Nonce` header
5. Metadata server looks up `cfg.ChannelKeys["telegram"]` and returns it as `channel-telegram-botToken`
6. Gateway receives the resolved value and starts

### The Race Condition

There are **two paths** for channel credentials to reach the metadata server:

#### Path A: Initial VM Creation (runtime.go → CreateVM → RegisterMachine)

```
runtime.go:ListMachineCredentialsWithValues()
  → classifies credentials as LLM vs channel
  → passes channelKeys to agentClient.CreateVM()
    → agent handler builds VMConfig with ChannelKeys
      → orchestrator.Create() calls RegisterMachine() with ChannelKeys
```

This path **should** make channel keys available before the gateway starts, since `RegisterMachine` is called before the VM boots. The code correctly passes `ChannelKeys` through the entire chain (`runtime.go:210` → `agentclient/client.go:137` → `agentapi/handlers.go:284` → `firecracker_linux.go:236`).

**Unconfirmed:** The `RegisterMachine` log does not include channel key count, so we cannot verify from production logs whether channel keys are actually present at registration time. The log only shows:
```
metadata.registered vm_ip=X machine_id=Y has_llm_keys=true llm_key_count=3
```

#### Path B: Post-Creation Live Push (pushCredentialsToVM → UpdateVMCredentials)

```
pushConfigToRunningMachine()
  → pushCredentialsToVM()
    → agentClient.UpdateVMCredentials()
      → agent handler calls orchestrator.UpdateLLMKeys()
        → metadata.UpdateMachineLLMKeys()
```

This path runs **after** the VM transitions to "running" status (~5 seconds after creation). Agent logs confirm the timing:
```
00:22:45  vm.create_start
00:22:47  metadata.registered (llm_key_count:3, no channel info)
00:22:52  metadata.channelkey.routed provider=telegram  ← from live push
```

### Why It Fails

The most likely explanation: **Path A delivers empty ChannelKeys at registration time.** Even though the code chain is correct, the credentials may not be in the database when `ListMachineCredentialsWithValues()` is called, or the classification logic may have a bug.

The gateway starts within seconds of `RegisterMachine`, before Path B's live push arrives at ~5 seconds post-creation. If Path A didn't actually populate ChannelKeys, the `/v1/secrets` endpoint returns no value for `channel-telegram-botToken`, and `ocm-secrets` reports an error back to the gateway, which crashes.

### Previous Fix Attempts (Reverted)

Two commits attempted to fix this by routing channel credentials through the existing LLM credential push endpoint:

1. `dec3cb3` — Added `UpdateChannelKeys` path through the full agent chain
2. `e0883ab` — Simplified to auto-route non-LLM providers in `UpdateMachineLLMKeys`

These were reverted because the agent on the host had already self-updated to the intermediate code and was causing issues. The reverts restored the original behavior where `pushCredentialsToVM` only pushes LLM credentials.

## Proposed Solutions

### Option 1: Remove Channel SecretRef Injection (Quick Fix)

Remove the SecretRef injection from `configassembly/assembler.go` (lines 580-603). Instead, inject the plaintext bot token directly into the config during assembly (the backend has access to the decrypted value).

**Pros:** Simple, no race condition, gateway starts immediately
**Cons:** Bot token is in plaintext in the assembled config (stored in DB, pushed via config batch). Less secure than SecretRef approach — but the token is already decrypted and passed through the config push HTTP calls anyway.

### Option 2: Ensure Path A Delivers Channel Keys (Proper Fix)

1. Add channel key count logging to `RegisterMachine` to confirm whether keys arrive
2. If keys are empty at registration, trace back to find where they're lost
3. If keys are present but gateway starts before registration completes, add a readiness gate

**Pros:** Keeps SecretRef security model intact
**Cons:** More investigation needed, may require init script changes for readiness

### Option 3: Retry/Backoff in ocm-secrets or Gateway

Have `ocm-secrets` retry the metadata fetch with a short backoff (e.g., 3 retries, 2s apart) when a platform secret is not found. Or configure the gateway to retry SecretRef resolution.

**Pros:** Handles the race condition gracefully, works for any future secrets
**Cons:** Adds latency to gateway startup, may mask real configuration errors

### Option 4: Make Channel Tokens Non-Required Secrets

Configure the gateway to treat channel token SecretRefs as optional rather than required. The gateway would start without them and the channel would enter a degraded state until the credentials arrive via live push.

**Pros:** Gateway never crashes due to missing channel credentials
**Cons:** Requires gateway-side changes (may not be configurable)

## Resolution

**Chosen approach:** Revert to the pre-`f14db3d` approach — inject plaintext channel tokens during config assembly.

The SecretRef approach was introduced in `f14db3d` (2026-03-21) but never worked correctly. Before that commit, channel credentials were handled by `injectChannelCredentials()` in the metadata server, which patched real tokens into the config JSON before serving it to the VM. When the config delivery was refactored to use config batch operations (pushed directly to disk), the metadata-based injection no longer had a single interception point.

The fix replaces `ChannelCredentials map[string]string` (just `"present"` markers) with `ChannelCredentialValues map[string]string` (decrypted token values). Config assembly injects the plaintext token directly into the assembled config (e.g., `channels.telegram.botToken = "123:ABC"`), which is pushed via config batch to the gateway's `openclaw.json` on disk. No SecretRef, no `ocm-secrets` resolution, no race condition.

**Security note:** The bot token is now in the assembled config stored in the DB and pushed via HTTP to the agent. This is acceptable because: (1) the assembled config is already pushed over the agent's authenticated HTTPS connection, (2) the DB field is only readable by the backend service account, (3) the previous SecretRef approach also transmitted the token over HTTP (metadata server → ocm-secrets), just at a different stage.

Also removed: `opik-openclaw` platform plugin injection — it was using a SecretRef for `OPIK_API_KEY` that had the same resolution failure. The plugin is not bundled in the rootfs, so injecting it only caused startup warnings/errors.

## Test Coverage Gaps

- No integration test covers the full SecretRef → `ocm-secrets` → metadata → channel key resolution flow
- Gateway e2e tests use plaintext bot tokens, not SecretRefs
- `ocm-secrets` tests use mock fetchers, not a real metadata server
- No test verifies that `RegisterMachine` receives non-empty `ChannelKeys` from `CreateVM`

## Files Involved

| File | Role |
|------|------|
| `backend/internal/configassembly/assembler.go:580-603` | Injects channel SecretRefs |
| `backend/internal/configassembly/assembler.go:105-111` | `ChannelTokenFieldName` map |
| `backend/cmd/ocm-secrets/main.go` | Resolves SecretRefs inside VM |
| `backend/internal/metadata/server_linux.go:88-113` | `/v1/secrets` endpoint serves channel keys |
| `backend/internal/metadata/metadata.go:98-110` | `RegisterMachine` stores config |
| `backend/internal/machines/runtime.go:197-215` | Classifies credentials at VM creation |
| `backend/internal/api/machine_config.go:572-614` | `pushCredentialsToVM` (live push) |
| `backend/internal/orchestrator/firecracker_linux.go:225-248` | Passes ChannelKeys to RegisterMachine |
