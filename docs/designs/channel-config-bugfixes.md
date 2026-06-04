# Channel Config State Machine — Bug Fixes

**Date:** 2026-03-27
**Status:** Draft
**Context:** Codex review of the `firebase-authentication` branch found 6 critical bugs in the channel config state machine and related flows.

## Current State

The channel config state machine (connect/disconnect/settings/token endpoints) was implemented but has several runtime bugs discovered by tracing the full execution paths. The frontend now surfaces `live_update` failures, but the backend flows themselves are broken.

## Bugs to Fix

### Bug 1: Missing AccountID on channel credentials

**File:** `backend/internal/api/channel_config.go` lines 71-78, 326-333
**Impact:** Every `handleChannelConnect` and `handleChannelUpdateToken` call fails with "failed to save credential" because `account_id` is a non-null FK in the `credentials` table and the struct leaves it at zero.

**Fix:** Set `AccountID` from the machine's `AccountID` (already loaded at line 25-33).

```go
cred := &store.Credential{
    AccountID:      machine.AccountID,  // <-- add this
    MachineID:      machineID,
    Provider:       channelID,
    ...
}
```

Same fix needed in `handleChannelUpdateToken` (line ~326).

### Bug 2: Channel tokens not pushed to metadata server on live connect

**File:** `backend/internal/api/channel_config.go` lines 109-131
**Impact:** Live channel connect pushes config ops with exec secret refs (`channel-telegram-botToken`), but the metadata server's `ChannelKeys` map is only set at VM creation time. The gateway calls `ocm-secrets` which calls the metadata server, but the metadata server doesn't have the new token. Result: gateway can't resolve the secret.

**Root cause:** No `UpdateMachineChannelKeys` method exists in the metadata server. `pushCredentialsToVM` only pushes LLM keys (line 599: `if !llmProviderSet[cred.Provider] { continue }`).

**Fix options:**

**Option A (Recommended): Extend `pushCredentialsToVM` to include channel keys.**
- In `pushCredentialsToVM`, after building `llmKeys`, also build a `channelKeys` map for non-LLM credentials
- Add `UpdateMachineChannelKeys` to the metadata server (mirrors `UpdateMachineLLMKeys`)
- Add `UpdateVMChannelKeys` to agentclient (mirrors `UpdateVMCredentials`)
- Call it from `handleChannelConnect` and `handleChannelUpdateToken` after pushing config ops

**Option B: Push plaintext tokens in config ops instead of exec refs.**
- Change `buildChannelConfig` to inject the actual token instead of an exec secret ref
- This is what `AssembleConfig` (full assembly) already does at line 593-597
- Simpler but less secure — token visible in openclaw.json on disk

**Decision:** Option A. Tokens should stay encrypted in transit and only exist in metadata server memory, not on disk.

### Bug 3: Restart path broken by removing OnRunning config push

**File:** `backend/internal/api/server.go` lines 203-206, `backend/internal/machines/runtime.go` line 379
**Impact:** When `isRestart = true`, `runtime.go` skips seed assembly entirely (line 394). The VM boots with its existing `openclaw.json` from the data volume. Previously, `OnRunning` called `pushConfigToRunningMachine` to push fresh config. With that removed, restarted machines never get updated config (new channels, changed models, updated identity, etc.).

**Fix:** Restore `pushConfigToRunningMachine` in `OnRunning` **only for restarts**. First-boot VMs don't need it because the seed has everything.

```go
machineRuntime.OnRunning = func(machineID string) {
    ctx := context.Background()
    machine, err := srv.store.GetMachine(ctx, machineID)
    if err != nil {
        slog.Error("on_running.get_machine_failed", ...)
        return
    }
    // Only push config on restart — first boot seed already has everything.
    // Detect restart: machine was placed on a host before this boot.
    if machine.HostID != nil {
        if err := srv.pushConfigToRunningMachine(ctx, machineID); err != nil {
            slog.Error("on_running.config_push_failed", ...)
        }
    }
    if err := srv.reconcileMachinePlugins(machine); err != nil {
        slog.Warn("on_running.reconcile_plugins_failed", ...)
    }
}
```

**Wait — this reintroduces the dual gateway issue.** The original problem was that `pushConfigToRunningMachine` triggers a hot-reload which races with the starting gateway. But on restart, the gateway is already running from the existing data volume. The config push happens after `OnRunning` fires (machine status = "running"), meaning the gateway has already started.

**Actually, this is the same race.** `OnRunning` fires when the agent reports the VM is running, but the gateway inside the VM may still be starting up (~55s). The push arrives during gateway startup and triggers a reload that conflicts.

**Better fix:** On restart, the `pushConfigToRunningMachine` should wait for the gateway to be healthy before pushing. Or: the push should be deferred to a "gateway ready" signal rather than "VM running" signal.

**Simplest correct fix:** Keep `pushConfigToRunningMachine` for restarts but add a health check wait loop before pushing:

```go
if machine.HostID != nil {
    // Wait for gateway health before pushing config to avoid reload race
    go func() {
        if err := srv.waitForGatewayHealth(ctx, machine, 90*time.Second); err != nil {
            slog.Warn("on_running.gateway_not_ready", ...)
            return
        }
        if err := srv.pushConfigToRunningMachine(ctx, machineID); err != nil {
            slog.Error("on_running.config_push_failed", ...)
        }
    }()
}
```

### Bug 4: Plaintext tokens in AssembleConfig (full assembly path)

**File:** `backend/internal/configassembly/assembler.go` lines 580-599
**Impact:** `AssembleConfig` injects plaintext channel tokens into the assembled config JSON. This JSON is persisted to `assembled_config` column in the DB and logged at debug level. Tokens are recoverable from DB/logs.

**Fix:** Change `AssembleConfig` to use exec secret refs for channel tokens, same as `AssembleSeedConfig`. Remove the `ChannelCredentialValues` map from `AssemblyParams` and inject exec refs in the channel config building step instead.

```go
// Instead of:
chConf[fieldName] = token  // plaintext

// Use:
chConf[fieldName] = map[string]interface{}{
    "source":   "exec",
    "provider": "ocm",
    "id":       fmt.Sprintf("channel-%s-%s", provider, fieldName),
}
```

**Dependency:** This requires Bug 2 to be fixed first — the metadata server must serve channel tokens for exec refs to resolve.

### Bug 5: Firebase unverified email bypass

**File:** `backend/internal/api/server.go` lines 1498-1513
**Impact:** Firebase token exchange accepts tokens with `email_verified: false`. An attacker can register `victim@example.com` in Firebase, keep it unverified, and get an OCM session for that email's account.

**Fix:** Check `fbClaims.EmailVerified` before proceeding:

```go
fbClaims, err := s.firebaseAuth.ValidateToken(req.IDToken)
if err != nil {
    writeError(w, http.StatusUnauthorized, "invalid Firebase token")
    return
}
if !fbClaims.EmailVerified {
    writeError(w, http.StatusForbidden, "email not verified")
    return
}
```

### Bug 6: No agentclient.RestartGateway method

**File:** `backend/internal/agentclient/client.go`
**Impact:** Channel connect/disconnect changes require a gateway restart (hot reload ignores channel changes). No way to trigger this from the backend.

**Fix:** Add `RestartGateway` to agentclient:

```go
func (c *Client) RestartGateway(ctx context.Context, host *store.Host, machineID, proxyToken string) error {
    url := fmt.Sprintf("https://%s:9090/proxy/%s/restart-gateway", host.ExternalIP, machineID)
    // POST with auth headers
}
```

Call it from `handleChannelConnect` and `handleChannelDisconnect` after pushing config ops, since the gateway requires a restart for channel changes.

## Execution Order

Bugs have dependencies:

1. **Bug 1** (AccountID) — no dependencies, prerequisite for everything else working
2. **Bug 5** (Firebase email) — no dependencies, security fix
3. **Bug 6** (RestartGateway client) — no dependencies, needed by Bug 2
4. **Bug 2** (Metadata push) — depends on Bug 6 for restart after push
5. **Bug 4** (Plaintext in AssembleConfig) — depends on Bug 2 (metadata must serve tokens)
6. **Bug 3** (Restart path) — depends on Bug 4 (full assembly must use exec refs)

## Test Plan

- **Bug 1:** Run `handleChannelConnect` against real DB — credential should save without error
- **Bug 2:** Connect a channel on a running VM, verify `ocm-secrets` can resolve `channel-telegram-botToken` from metadata server
- **Bug 3:** Stop and start a machine that has channels configured, verify channels work after restart
- **Bug 4:** Check `assembled_config` in DB after config push — no plaintext tokens
- **Bug 5:** Attempt Firebase login with unverified email — should get 403
- **Bug 6:** Call `RestartGateway` on a running VM — gateway should restart within 15s
- **End-to-end:** Connect telegram on running VM → verify bot responds to messages → disconnect → verify bot stops responding → restart VM → verify channels match DB state
