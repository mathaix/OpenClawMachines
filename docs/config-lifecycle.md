# Config Lifecycle: Boot, Seed, and Runtime Updates

## Overview

Every OpenClaw Machine needs an `openclaw.json` config file that tells the gateway which LLM providers, plugins, channels, and models to use. This config goes through three phases:

1. **Assembly** — backend builds JSON from DB state (plugins, credentials, capabilities)
2. **Seed write** — orchestrator writes `openclaw.json` to data volume before VM boots
3. **Runtime updates** — backend pushes config ops to running VMs via the agent

```
                  ┌──────────────────────────────────────────────┐
                  │            Control Plane                      │
                  │                                              │
 User clicks      │  machines.Start()                            │
 "Start" ────────►│      │                                       │
                  │      ├─ Load machine_plugins + plugin_catalog│
                  │      ├─ Load credentials + model catalog     │
                  │      ├─ Load channel configs                 │
                  │      │                                       │
                  │      ▼                                       │
                  │  AssembleSeedConfig(SeedParams{...})          │
                  │      │                                       │
                  │      ▼                                       │
                  │  openclaw.json bytes ──► agentClient.CreateVM│
                  └──────────────┬───────────────────────────────┘
                                 │ HTTP POST /vms
                                 ▼
                  ┌──────────────────────────────────────────────┐
                  │            Host Agent                         │
                  │                                              │
                  │  writeDataVolumeConfig()                     │
                  │      │                                       │
                  │      ├─ Mount data volume (ext4 loopback)    │
                  │      ├─ Load/generate nonce                  │
                  │      ├─ Write openclaw.json (if first boot)  │
                  │      ├─ Write auth-profiles.json             │
                  │      └─ Unmount                              │
                  │                                              │
                  │  Start Firecracker ──► VM boots              │
                  └──────────────────────────────────────────────┘
                                 │
                                 ▼
                  ┌──────────────────────────────────────────────┐
                  │            Inside MicroVM                     │
                  │                                              │
                  │  init-openclaw.sh                            │
                  │      ├─ Mount data volume at /data           │
                  │      ├─ Seed extensions from rootfs          │
                  │      ├─ Symlink /home/openclaw → /data/...   │
                  │      ├─ Config versioning (config-current)   │
                  │      └─ Start gateway                        │
                  │                                              │
                  │  Gateway reads /home/openclaw/.openclaw/     │
                  │      ├─ openclaw.json                        │
                  │      ├─ extensions/opik-openclaw/            │
                  │      └─ config-current/ (symlinked)          │
                  └──────────────────────────────────────────────┘
```

## Two Config Assembly Paths

There are two separate assembler functions. This is the most important architectural detail to understand — using the wrong one is a common source of bugs.

### `AssembleSeedConfig(SeedParams)` — First Boot

- **When**: Called by `machines.RuntimeService.Start()` during provisioning (first boot only, not restarts)
- **Where**: `backend/internal/configassembly/assembler.go`
- **Output**: Written to data volume as `openclaw.json` before Firecracker boots
- **Features**: Exec secret refs (ocm-secrets), plugins, opik injection, channel configs, auth profiles
- **Key params**: `Plugins`, `OpikAPIURL`, `OpikAPIKey`, `Providers`, `ChannelConfigs`, `ModelCatalog`

### `AssembleConfig(AssemblyParams)` — Runtime Push

- **When**: Called exclusively by `handlePushMachineConfig` (POST `/config/push`) — the single entry point for all runtime config changes
- **Where**: `backend/internal/configassembly/assembler.go`
- **Output**: Pushed to running VMs as atomic config ops via the agent
- **Features**: Full capability-based assembly, credential injection, plugin config, browser CDP
- **Key params**: `Capabilities`, `Credentials`, `Plugins`, `OpikAPIURL`, `OpikAPIKey`, `NativeMode`

### Why Two Paths?

The seed config is a minimal bootstrap — just enough to get the gateway running on first boot (default model, basic provider config). After that, config grows organically through explicit user actions (adding credentials, enabling plugins, changing models), each pushed via `POST /config/push`.

The two paths do not need to stay in sync. Seed is small and one-time. The machine's data volume is always the source of truth for config.

## Config Push Design Principle

**All runtime config changes to a live machine MUST go through `handlePushMachineConfig` (POST `/config/push`).** The frontend is responsible for calling this endpoint after any state change that affects config.

### Why explicit pushes only

Previously, the backend had implicit config pushes scattered across multiple code paths:

- `OnRunning` callback pushed config on VM restart
- `handleSetMachineModel` pushed config after model change
- Credential add/delete handlers pushed config for codex OAuth

This caused problems:
- **Double pushes**: Frontend already calls `pushMachineConfig` after model changes, so the server-side push was redundant and created race conditions
- **Error-prone**: Background goroutines pushing config from credential handlers masked failures and made debugging harder
- **Inconsistent**: Only codex credentials triggered auto-push, not other providers
- **Hard to reason about**: Multiple code paths modifying live VM state made it unclear what config the VM actually had

### Frontend responsibility

After any action that changes config-relevant state, the frontend must call `pushMachineConfig`:

| User Action | API Call | Then Push Config? |
|-------------|----------|-------------------|
| Change model | `PUT /model` | Yes — frontend calls `POST /config/push` |
| Add BYOK key | `PUT /credentials/{provider}` | Yes — frontend calls `POST /config/push` |
| Remove credential | `DELETE /credentials/{provider}` | Yes — frontend calls `POST /config/push` |
| Connect subscription | `PUT /credentials/{provider}` | Yes — frontend calls `POST /config/push` |
| Enable/disable plugin | `PUT /plugins/{id}` | Yes — frontend calls `POST /config/push` |
| Configure channel | `PUT /channels/{id}` | Yes — frontend calls `POST /config/push` |

### Restart behavior

On restart, the VM boots with the config already on the data volume from the previous session. Config does not change on restart — the machine comes back with exactly the config it had when it was stopped. The `OnRunning` callback does not push config.

## Data Volume Persistence

```
/data/                          ← ext4 data volume, persists across reboots
├── .ocm-nonce                  ← generated once at first boot, never regenerated
├── .ocm-version                ← data version marker
├── home/openclaw/
│   ├── .openclaw/
│   │   ├── openclaw.json       ← seed config (written by orchestrator at first boot)
│   │   ├── extensions/         ← plugin files (seeded from rootfs at first boot)
│   │   │   └── opik-openclaw/  ← bundled plugin (installed in rootfs Dockerfile)
│   │   ├── config-current/     ← symlink to timestamped config dir
│   │   └── workspace/
│   ├── .bashrc, .profile       ← seeded from rootfs at first boot
│   └── ...
├── workspace/                  ← user workspace files
└── ocm/
    └── configs/                ← timestamped config directories
        ├── 20260328T120000Z/
        └── 20260328T130000Z/
```

### First Boot vs Restart

| Action | First Boot | Restart |
|--------|-----------|---------|
| `openclaw.json` | Written by orchestrator | Skipped (already on disk) |
| Extensions seeding | Copied from rootfs | Skipped (already on disk) |
| Nonce | Generated | Loaded from disk |
| Config push | After first user action that changes config | None — boots with existing config |

On restart, the orchestrator's `writeDataVolumeConfig` detects that `openclaw.json` already exists and returns early. The config on the data volume is the config — it does not change on restart.

## Plugin Config Flow (e.g., opik-openclaw)

Plugins require config in three places:

1. **`plugins.allow`** — trust-lists the plugin so the gateway loads it
2. **`plugins.entries.<id>`** — plugin config (apiUrl, apiKey, enabled, tags)
3. **`plugins.installs.<id>`** — tells gateway where plugin files are on disk

All three are injected by the assembler when `OpikAPIURL` is set and the plugin template (from `plugin_catalog`) contains the entry.

### Plugin data flow:

```
machine_plugins table ──► ListMachinePlugins() ──► PluginSelection
plugin_catalog table  ──► GetPluginCatalogEntry() ──► ConfigTemplate
                                                         │
                                                         ▼
                                              SeedParams.Plugins
                                                         │
                                                         ▼
                                          AssembleSeedConfig() deep-merges
                                          template into result, then injects
                                          apiUrl/apiKey/installs
```

## Config Versioning (init script)

The init script manages config directories with a version scheme:

- `ROOTFS_CONFIG_VERSION` (currently 1) is bumped when config format changes
- Each version creates a new timestamped directory under `/data/ocm/configs/`
- `config-current` symlink points to the active config dir
- Previous config files are copied forward (preserving user edits)
- Old config dirs are garbage-collected (keep latest 5)

## Implementation Status and Cleanup

### Code to remove

The following implicit config push paths violate the explicit-push principle and should be deleted:

1. **`pushConfigToRunningMachine`** (`machine_config.go`) — the entire function. Duplicate of `pushMachineConfigInternal` with no callers once the below are cleaned up.
2. **`waitForGatewayHealth`** (`machine_config.go`) — keep the function. Move it to the `OnRunning` callback as a standalone health check (log warning if unhealthy, but do not push config after).
3. **`OnRunning` callback** (`server.go`, two constructors) — remove `pushConfigToRunningMachine` and `reconcileMachinePlugins` calls. Keep `waitForGatewayHealth` as a health check only (log warning if unhealthy, no action after).
4. **`handleSetMachineModel` auto-push** (`machine_config.go`) — remove the `pushConfigToRunningMachine` call. Frontend already calls `POST /config/push` after `PUT /model`.
5. **Codex credential auto-push** (`credentials.go`, two locations) — remove the `pushConfigToRunningMachine` goroutines from `handleSetCredential` and `handleDeleteCredential`. Frontend should call `POST /config/push` after credential changes.

### Features that need reimplementation

These handlers currently only save to DB. They need to be redesigned to properly push changes to the running VM through the explicit config push path:

1. **Identity** (`handleSetMachineIdentity` in `machine_config.go`)
   - Currently: saves name/avatar to DB via `store.SetMachineIdentity`, no push to VM
   - Needed: identity changes should be picked up by `AssembleConfig` and pushed via `POST /config/push`
   - The frontend must call `pushMachineConfig` after setting identity

2. **Agent/Soul CRUD** (`machine_agents.go` — list, create, get, update, delete)
   - Currently: saves agent config (name, model, soul, identity) to DB, no push to VM
   - Soul content is only written to the VM inside `pushMachineConfigInternal` (the `POST /config/push` handler)
   - Needed: frontend must call `pushMachineConfig` after any agent create/update/delete so that:
     - Soul files get written to the VM
     - Agent model overrides and workspace configs take effect
   - The backend handlers themselves are fine (DB-only is correct), but the frontend doesn't push after agent changes

3. **Credential changes** (BYOK keys, subscriptions)
   - Currently: frontend refreshes UI state after credential add/remove (`fetchCredentials` + `fetchModels`) but does NOT call `pushMachineConfig`
   - Needed: frontend must call `pushMachineConfig` after credential add/remove so the gateway gets updated provider config

### Channel push path (acceptable as-is)

Channels have their own direct push path (`pushChannelOps` + `restartGateway`) in `channel_config.go`. This is acceptable because channel changes require a gateway restart (hot-reload ignores them), which is different from the atomic config ops used by `POST /config/push`. Config does not change on restart — channels use whatever config is already on the data volume.

## Related Documentation

- vm-provisioning — Full provisioning pipeline from user click to gateway responding
- rootfs-design — Persistence model (ephemeral rootfs vs. persistent data volume)
- configuration — OpenClaw config schema analysis and gaps
- design-loadconfig-metadata — Runtime config loading in metadata mode
