# Config Persistence — Design Document

**Date:** 2026-03-27
**Status:** Design complete — ready for implementation
**Scope:** Channel configuration (Phase 1). Model, plugin, browser, identity, skills domains will follow the same pattern later.

## Problem Statement

Channel configuration (Telegram, Discord) is unreliable. Any dashboard action (model change, soul update, plugin toggle) can wipe a working channel from the running gateway.

**Root cause:** There is no config state machine. Every action funnels through one path — full reassembly + full diff — instead of being a well-defined state transition that touches only its own config domain.

## Current Architecture (The Problem)

### No State Machine — One Giant Transition

```
ANY dashboard action
  → pushMachineConfig API
    → assembleConfigForMachine() — reads ALL capabilities, credentials, plugins from DB
    → buildConfigOps() — diffs ENTIRE old config vs ENTIRE new config
    → ConfigBatch() — sends ALL set/unset ops to VM
```

A model change can generate `unset channels.telegram` if the Telegram capability fails to load during assembly. The assembly has no way to distinguish "user removed this channel" from "I failed to load this channel."

### VM Lifecycle Has a State Machine. Config Does Not.

The VM lifecycle is well-modeled:

```
provisioning → starting → running → stopping → stopped
                                  → error
```

Each transition has clear semantics, guards, and side effects. You can't accidentally stop a VM by starting one.

Config has no equivalent. There's one transition: "reassemble everything." Every action triggers the same reassembly. There's no concept of independent config domains with their own states and transitions.

## Solution: Channel Config State Machine

Model channel config updates as state transitions. Each transition is scoped to `channels.<name>` and cannot affect any other config domain.

### Channel State Machine

```
                    ┌──────────────┐
          ┌────────▶│ disconnected │◀────────┐
          │         └──────┬───────┘         │
          │                │                 │
          │           connect()              │
          │          [save cred +            │
          │           enable cap +        disconnect()
          │           push config]       [disable cap +
          │                │              delete cred +
          │                ▼              push unset]
          │         ┌──────────────┐         │
          │         │  connected   │─────────┘
          │         └──────┬───────┘
          │                │
          │        updateSettings()
          │       [save overrides +
          │        push config]
          │                │
          │          updateToken()
          │        [save credential +
          │         push config]
          │                │
          │                ▼
          │         ┌──────────────┐
          └─────────│  connected   │ (same state, updated config)
                    └──────────────┘
```

**State:** `disconnected` | `connected`

**Transitions:**

| Transition | DB writes | Config ops | Guards |
|-----------|-----------|------------|--------|
| `connect(token, settings)` | Save credential + enable capability + save overrides | `set channels.<name> <merged config>` | Machine is running |
| `disconnect()` | Disable capability + delete credential | `unset channels.<name>` | Machine is running |
| `updateSettings(overrides)` | Save overrides | `set channels.<name> <merged config>` | Channel is connected, machine is running |
| `updateToken(token)` | Save credential | `set channels.<name> <merged config>` | Channel is connected, machine is running |

**Invariant:** A channel transition only produces ops for `channels.<name>`. It never touches `models.*`, `plugins.*`, or any other domain.

**Persistence:** `openclaw config set/unset` (the gateway CLI) writes directly to `openclaw.json` on the data volume. Each transition persists automatically. No separate write-back needed.

## Config Lifecycle

### First Boot (Seed)

The seed is the **only** full assembly. It reads all DB state — capabilities, credentials, plugins, identity, models — and writes the complete `openclaw.json` to the data volume. The machine boots with everything the user configured before starting.

```
seed config = platform defaults + models/providers + channels + plugins + identity + skills + browser
```

`AssembleSeedConfig` needs to be expanded to include channels (currently it only includes platform defaults + models/providers). Other domains (plugins, identity, etc.) will be added when those domains get their own state machines.

### Live (State Transitions)

All changes after first boot happen while the machine is live. Each user action is one state transition in one domain.

### Reboot

The data volume persists. `openclaw.json` reflects the seed + all subsequent transitions. Nothing to do.

## Why This Eliminates the Config Wipe Bug

With the current architecture:
1. User changes model → full reassembly → fails to load Telegram → `unset channels.telegram`

With the channel state machine:
1. User changes model → model handler runs (currently still full reassembly, but channels are not in its scope)
2. Channel transitions are the only code path that produces `channels.*` ops

**The bug is eliminated by construction.** No code path outside the channel state machine can produce `set/unset channels.*` ops.

## Implementation

### Backend: Channel Transition Handlers

```go
func (s *Server) handleChannelConnect(w http.ResponseWriter, r *http.Request)      // connect()
func (s *Server) handleChannelDisconnect(w http.ResponseWriter, r *http.Request)   // disconnect()
func (s *Server) handleChannelSettings(w http.ResponseWriter, r *http.Request)     // updateSettings()
func (s *Server) handleChannelUpdateToken(w http.ResponseWriter, r *http.Request)  // updateToken()
```

Each handler:
1. Validates the transition (guards: machine running, channel connected/disconnected as appropriate)
2. Saves state to DB (credential, capability, overrides)
3. Builds the merged channel config (registry template + overrides + token)
4. Computes ops: single `set channels.<name> <json>` or `unset channels.<name>`
5. Pushes ops to VM via `ConfigBatch`
6. Patches the assembled config in DB (for seed consistency)

### Channel Connect — Detail

```go
func (s *Server) handleChannelConnect(w http.ResponseWriter, r *http.Request) {
    channelID := chi.URLParam(r, "channel")  // "telegram", "discord"

    // 1. Parse request: token + optional settings (dmPolicy, groups, etc.)
    // 2. Validate token (testMachineCredential)
    // 3. Save credential to DB (SetMachineCredential)
    // 4. Enable capability (EnableMachineCapability)
    // 5. Save settings overrides if provided (UpdateMachineCapabilityOverrides)
    // 6. Build merged config:
    //    - Load registry entry config template (e.g., {channels: {telegram: {enabled: true, dmPolicy: "pairing"}}})
    //    - Deep-merge overrides (e.g., {dmPolicy: "open", groups: {"*": {requireMention: true}}})
    //    - Inject bot token
    // 7. Push single op: set channels.<channelID> <merged json>
    // 8. Patch assembled config in DB
}
```

### Channel Disconnect — Detail

```go
func (s *Server) handleChannelDisconnect(w http.ResponseWriter, r *http.Request) {
    channelID := chi.URLParam(r, "channel")

    // 1. Disable capability (DisableMachineCapability)
    // 2. Delete credential (DeleteMachineCredential)
    // 3. Push single op: unset channels.<channelID>
    // 4. Patch assembled config in DB (remove channel section)
}
```

### Channel Update Settings — Detail

```go
func (s *Server) handleChannelSettings(w http.ResponseWriter, r *http.Request) {
    channelID := chi.URLParam(r, "channel")

    // 1. Parse request: dmPolicy, groups, etc.
    // 2. Save overrides (UpdateMachineCapabilityOverrides)
    // 3. Rebuild merged config (template + new overrides + existing token)
    // 4. Push single op: set channels.<channelID> <merged json>
    // 5. Patch assembled config in DB
}
```

### Channel Update Token — Detail

```go
func (s *Server) handleChannelUpdateToken(w http.ResponseWriter, r *http.Request) {
    channelID := chi.URLParam(r, "channel")

    // 1. Parse request: new token
    // 2. Validate token
    // 3. Save credential (SetMachineCredential)
    // 4. Rebuild merged config (template + existing overrides + new token)
    // 5. Push single op: set channels.<channelID> <merged json>
    // 6. Patch assembled config in DB
}
```

### Backend: Assembled Config Patch

After each transition, patch the stored assembled config in DB so it stays in sync:

```go
func (s *Server) patchAssembledConfig(ctx context.Context, machineID string, ops []ConfigOp) error
```

This uses `config_version` as a CAS guard to prevent concurrent patches from clobbering each other.

### Backend: Seed Expansion

Expand `AssembleSeedConfig` to include channel config so machines boot with channels pre-configured:

- Load enabled channel capabilities for the machine
- Load registry entries for each capability → get config templates
- Load capability overrides (dmPolicy, groups, etc.)
- Load and decrypt channel credentials → inject bot tokens
- Deep-merge into the seed config

### Backend: Remove Channel Ops from Full Reassembly

Remove `channels` from `buildConfigOps` so the legacy full-reassembly path can no longer generate `set/unset channels.*` ops. This is the safety net: even if `pushMachineConfig` is still called by other domains (model, plugin, etc.), it cannot touch channels.

### Frontend: Replace Generic Push with Channel-Specific Calls

**ChannelsTab.tsx:**

| Current Flow | New Flow |
|-------------|----------|
| `enableCapability → putCredential → pushMachineConfig` | `POST /channels/{channel}/connect` (single call) |
| `updateOverrides → pushMachineConfig` | `PUT /channels/{channel}/settings` (single call) |
| `disableCapability → deleteCredential → pushMachineConfig` | `POST /channels/{channel}/disconnect` (single call) |

The frontend no longer needs to orchestrate multiple API calls for channel operations. Each transition is a single backend call.

### API Changes

**New endpoints:**

| Method | Path | Transition |
|--------|------|------------|
| POST | `/machines/{id}/channels/{channel}/connect` | `channel.connect()` |
| POST | `/machines/{id}/channels/{channel}/disconnect` | `channel.disconnect()` |
| PUT | `/machines/{id}/channels/{channel}/settings` | `channel.updateSettings()` |
| PUT | `/machines/{id}/channels/{channel}/token` | `channel.updateToken()` |

**Modified:**

| File | Change |
|------|--------|
| `config_ops.go` | Remove `channels` from `buildConfigOps` / `diffKeyedSection` |

**Deprecated (for channels):**

The `pushMachineConfig` frontend calls in ChannelsTab are replaced by the new endpoints. The generic `POST /config/push` endpoint remains for other domains until they get their own state machines.

## Files Involved

| File | Changes |
|------|---------|
| `backend/internal/api/machine_config.go` | Add `handleChannelConnect`, `handleChannelDisconnect`, `handleChannelSettings`, `handleChannelUpdateToken`; add `patchAssembledConfig` helper; add `buildChannelConfig` helper |
| `backend/internal/api/config_ops.go` | Remove `channels` from `buildConfigOps` |
| `backend/internal/api/server.go` | Register new channel endpoints |
| `backend/internal/configassembly/assembler.go` | Expand `AssembleSeedConfig` to include channel config |
| `backend/internal/machines/runtime.go` | Pass channel capabilities + credentials to seed assembly |
| `frontend/src/lib/api.ts` | Add `connectChannel`, `disconnectChannel`, `updateChannelSettings`, `updateChannelToken` |
| `frontend/src/pages/machine-tabs/ChannelsTab.tsx` | Replace multi-step `pushMachineConfig` flows with single-call transitions |

## Test Plan

1. **`connect()` transition:**
   - Unit test: generates single `set channels.telegram <json>` op with merged template + overrides + token
   - Unit test: saves credential, enables capability, saves overrides in DB
   - E2E: connect Telegram on running machine → gateway picks up channel

2. **`disconnect()` transition:**
   - Unit test: generates single `unset channels.telegram` op
   - Unit test: disables capability, deletes credential in DB
   - E2E: disconnect Telegram → gateway removes channel, other config untouched

3. **`updateSettings()` transition:**
   - Unit test: generates `set channels.telegram <json>` with new overrides, existing token preserved
   - E2E: change dmPolicy → gateway reloads with new policy

4. **`updateToken()` transition:**
   - Unit test: generates `set channels.telegram <json>` with new token, existing overrides preserved
   - E2E: change bot token → gateway reconnects with new token

5. **Cross-domain isolation:**
   - E2E: change model on machine with Telegram connected → Telegram still configured
   - E2E: toggle plugin → Telegram still configured

6. **Seed includes channels:**
   - Unit test: `AssembleSeedConfig` with Telegram capability → seed config includes `channels.telegram`
   - E2E: configure Telegram before starting → machine boots with Telegram active

7. **Reboot persistence:**
   - E2E: connect channel → reboot → channel still configured

## Relationship to Other Open Issues

1. **Channel credential live-update gap** — Solved by `updateToken()` transition.

2. **Plaintext tokens in logs** — Orthogonal. Still needs redaction in PTY server and debug logging.

3. **ChannelsTab override round-trip safety** — Solved. Each transition loads the full channel state (template + overrides + token) and pushes the complete merged config. No partial-state issue.

## Future Phases

The same state machine pattern applies to other config domains:

| Phase | Domain | Pattern |
|-------|--------|---------|
| 1 (this doc) | Channels | connect / disconnect / updateSettings / updateToken |
| 2 | Models | change (refactor existing `handleSetMachineModel`) |
| 3 | Plugins | enable / disable (match `plugins.slots.*` schema) |
| 4 | Identity | set / unset |
| 5 | Skills | set / unset |
| 6 | Browser | enable / disable (next-boot semantics — requires companion VM) |

Each phase removes that domain from `buildConfigOps` and adds domain-scoped transition handlers. Once all phases complete, `buildConfigOps` and the generic `pushMachineConfig` can be retired.
