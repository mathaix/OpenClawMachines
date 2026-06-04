# VM Configuration Lifecycle

How configuration flows through the system — from creation to boot to runtime changes to reboot.

## Actors

| Actor | Where it runs | Role |
|-------|--------------|------|
| **Backend API** | Cloud Run | Stores provider/credential info in DB, assembles seed + full config, pushes config ops |
| **Orchestrator** | Agent on GCE VM (host) | Writes seed config to data volume pre-boot, manages VM lifecycle |
| **Init script** | Inside Firecracker VM (PID 1) | Mounts data volume, sets up directory structure, starts gateway |
| **Gateway** | Inside Firecracker VM | OpenClaw runtime, watches `openclaw.json` for changes, hot-reloads |
| **Auth proxy** | Inside Firecracker VM | Intercepts LLM API calls, injects credentials using nonce |

## File Locations (Inside VM)

| File | Path | Purpose |
|------|------|---------|
| Main config | `/home/openclaw/.openclaw/openclaw.json` | Gateway reads this at startup and watches for changes |
| Auth profiles | `/home/openclaw/.openclaw/agents/main/agent/auth-profiles.json` | Symlink → `config-current/auth-profiles.json`. Gateway uses for model discovery |
| Config dir | `/home/openclaw/.openclaw/config-current` | Symlink → `/data/ocm/configs/<timestamp>/` |
| Device identity | `/home/openclaw/.openclaw/identity/device.json` | Symlink → `config-current/device.json` |
| Nonce | `/data/.ocm-nonce` | Metadata lookup token, generated once, never regenerated |
| Soul files | `/home/openclaw/.openclaw/workspace/SOUL.md` | Agent persona |

## Storage Layers and Source of Truth

Config lives in two places:

1. **Data volume (VM)** — `openclaw.json` on disk is the **source of truth** after first boot. This is the file the gateway reads and watches. User edits, config pushes, and in-VM changes all modify this file.
2. **Database** — stores provider/credential information and a snapshot of the last assembled config (used for diffing on config push). The database is **not** the source of truth for runtime config.

The database drives the **initial** config assembly. After first boot, the file on disk owns the config. The backend pushes changes as granular `openclaw config set/unset` ops — it does not replace the file wholesale.

### Managed vs unmanaged paths

The backend manages specific config paths via `buildConfigOps()`. User edits to **unmanaged** paths are fully authoritative and never touched by config pushes. User edits to **managed** paths will be overwritten on the next config push (the backend diffs its own DB snapshots, not the live disk file).

**Managed paths** (controlled by backend, overwritten on push):
- `models.providers.<name>` — LLM provider configuration
- `channels.<name>` — channel providers (Slack, etc.)
- `plugins.entries.<name>` — plugin catalog entries
- `agents.defaults.model.primary` — default model
- `agents.defaults.models` — models map
- `ui.assistant` — identity/avatar config
- `skills.allowBundled` — skills configuration
- `browser` — browser capability
- `agents.list` — agent personas

**Unmanaged paths** (user edits preserved across pushes):
- Everything else in `openclaw.json`

---

## Lifecycle: First Boot (VM Creation)

First boot has two phases: seed config pre-boot, then full config push after gateway is running.

```
User clicks "Start" (or auto_start on create)
    │
    ▼
Backend API: AssembleSeedConfig()
    │  - Builds minimal seed config (providers, credentials, identity)
    │  - Passes seed config + souls to agent.CreateVM()
    │
    ▼
Orchestrator: Create()
    │  - Creates data volume (ext4 image)
    │  - Calls writeDataVolumeConfig():
    │      1. Loop-mounts data volume on host
    │      2. Generates nonce, saves to /data/.ocm-nonce
    │      3. Creates /home/openclaw/.openclaw/config-current/ (REAL DIRECTORY)
    │      4. Writes openclaw.json (SEED config) into .openclaw/
    │      5. Writes auth-profiles.json into config-current/
    │      6. Writes SOUL.md files into workspace/
    │      7. Unmounts
    │  - Creates rootfs (reflink clone of base image)
    │  - Boots Firecracker VM
    │
    ▼
Init script (PID 1 inside VM):
    │  1. Mounts /data (data volume) at /dev/vdb
    │  2. Symlinks /home/openclaw → /data/home/openclaw
    │  3. Runs data migrations if needed
    │  4. CONFIG DIRECTORY SETUP:
    │      - Finds config-current is a real directory (from orchestrator)
    │      - Migrates contents to /data/ocm/configs/<timestamp>/
    │      - Replaces real dir with symlink: config-current → timestamped dir
    │  5. Creates auth-profiles.json symlink:
    │      agents/main/agent/auth-profiles.json → config-current/auth-profiles.json
    │  6. Writes /etc/profile.d/openclaw-providers.sh (provider env vars)
    │  7. Starts gateway process (reads openclaw.json from disk)
    │
    ▼
Gateway: starts with SEED config, discovers models from auth-profiles.json
    │
    ▼
Backend API: OnRunning callback → pushConfigToRunningMachine()
    │  - Assembles FULL config from DB (capabilities, plugins, agents, etc.)
    │  - Diffs against seed config → generates config ops
    │  - Pushes full config to running VM via openclaw config set/unset
    │  - Also writes soul files via agent WriteFile
    │
    ▼
Gateway: detects openclaw.json changes → hot-reloads with full config
```

### Key invariants

- After init script completes, `config-current` is always a **symlink** to a timestamped directory, never a real directory.
- The seed config written pre-boot is **minimal** — the full config arrives via `OnRunning` callback after the gateway is up.
- `auth-profiles.json` is written pre-boot and NOT updated by config pushes (see Known Issues).

---

## Lifecycle: Reboot (Same Config, Same Host)

```
VM reboots (stop + start, same data volume on same host)
    │
    ▼
Orchestrator: writeDataVolumeConfig()
    │  - Sees openclaw.json already exists on data volume
    │  - SKIPS writing config (preserves user edits from previous session)
    │  - Returns existing nonce
    │
    ▼
Init script:
    │  1. Mounts /data
    │  2. config-current is already a symlink (from previous boot)
    │  3. Config version matches → reuses existing config
    │  4. Gateway starts with persisted config (including any user edits)
    │
    ▼
Backend API: OnRunning callback → pushConfigToRunningMachine()
    │  - Diffs current DB config against previous DB snapshot
    │  - If DB config changed while stopped → pushes updates
    │  - If DB config unchanged → no ops sent
```

### Key invariant

User edits made via `openclaw config set` inside the VM persist across reboots because:
- They modify `openclaw.json` on the data volume
- The orchestrator skips writing if `openclaw.json` already exists
- The init script reuses the existing config-current symlink

---

## Lifecycle: Config Change via UI/API (Machine Running)

```
User changes model/capability/credential in Dashboard
    │
    ▼
Backend API: updates DB (machine_capabilities, machine_config, etc.)
    │
    ▼
User clicks "Push Config" (or automatic on certain changes)
    │
    ▼
Backend API: pushMachineConfigInternal()
    │  1. Assembles new openclaw.json from DB
    │  2. Loads previous assembled config snapshot from DB
    │  3. Diffs old vs new via buildConfigOps() → generates set/unset commands
    │  4. Writes soul files to VM via agent WriteFile
    │  5. Sends config ops batch to agent
    │
    ▼
Agent (on host): ConfigBatch()
    │  - Executes inside VM via exec:
    │      openclaw config set models.providers.anthropic '{"baseUrl":"..."}'
    │      openclaw config set plugins.entries.opik-openclaw.enabled true
    │      openclaw config unset channels.slack
    │
    ▼
Gateway: detects openclaw.json change on disk → hot-reloads
    │  - New model, capabilities, plugins active within seconds
    │  - No restart required
```

### How buildConfigOps() diffs

The diff is **selective** — only changed keys generate ops:
- For keyed sections (providers, channels, plugins): keys in old but not new → `unset`; keys in new → `set`
- For object sections (model, identity, browser, etc.): present in new → `set`; absent from new but present in old → `unset`

The diff compares the **previous DB snapshot** against the **newly assembled DB config** — it does NOT read the live disk file. This means in-VM edits to managed paths will be overwritten on the next push.

### Model changes are a special path

Model changes use a dedicated endpoint (`PUT /model`) that live-pushes directly via `openclaw config set agents.defaults.model.primary`, followed by a full `/config/push`.

---

## Lifecycle: Config Change via CLI

```
User runs: ocm config push --machine my-machine
    │
    ▼
CLI: calls POST /api/accounts/{id}/machines/{id}/config/push
    │
    ▼
(Same flow as UI/API config change above)
```

The CLI is a thin wrapper around the same API endpoints the Dashboard uses.

---

## Lifecycle: Config Change Inside the VM

```
User (or tool) runs inside VM:
    openclaw config set skills.allowBundled true
    │
    ▼
openclaw CLI: reads openclaw.json, modifies key, writes back
    │
    ▼
Gateway: detects file change → hot-reloads
```

### Source of truth behavior

- The change modifies `openclaw.json` on the data volume — this is the authoritative config
- The backend DB is **not updated** (it only stores provider/credential info and an assembly snapshot)
- Edits to **unmanaged paths** persist indefinitely — config pushes never touch them
- Edits to **managed paths** will be overwritten on the next config push from the backend
- If the VM is **destroyed or migrates to a different host**, the data volume is lost and a fresh config is assembled from the DB

---

## Lifecycle: Config Change While Machine is Stopped

Config changes in the DB while a machine is stopped are applied on next start via the `OnRunning` callback:

1. User changes capabilities/credentials/model in Dashboard while machine is stopped
2. Changes are saved to DB, assembled config snapshot updated
3. API returns `live_update: "not_running"` (no VM to push to)
4. On next start, `OnRunning` callback fires → `pushConfigToRunningMachine()`
5. Diffs the new assembled config against the previous snapshot
6. Config ops are executed to bring the running VM up to date

The onboarding wizard relies on this flow — it configures the machine before first start.

---

## Lifecycle: Host Migration (Admin-Initiated)

Migrations are initiated via `POST /admin/migrate` with a target host. The flow preserves the data volume by backing it up to GCS and restoring on the target.

```
Admin triggers migration (POST /admin/migrate)
    │
    ▼
Backend: handleAdminMigrateMachine()
    │  1. Stop VM on source host (if running)
    │  2. Create backup of data volume → GCS (encrypted)
    │  3. Destroy VM on source host (cleans up resources)
    │  4. Release source host placement (capacity counters)
    │
    ▼
Backend: Start on target host (fresh Create)
    │  - Creates NEW data volume + seed config
    │  - Generates new nonce
    │  - Wait for "running" state
    │
    ▼
Backend: Stop → Restore → Start
    │  1. Stop newly created VM
    │  2. Restore backup over the fresh data volume (agentClient.RestoreVM)
    │  3. Start again — VM boots with RESTORED data volume
    │
    ▼
Init script + Gateway: boot with restored config + workspace
    │  - All user data preserved (config, workspace files, unmanaged paths)
    │  - New nonce generated (auth proxy still works)
    │
    ▼
Backend: OnRunning → pushConfigToRunningMachine()
    │  - Pushes any DB changes made since backup was taken
```

### Backup requirements

- Machine must have `backups_enabled = true` and `backup_key` set
- Server must have `BACKUP_MASTER_KEY` configured
- Without backups, migration requires `force=true` (data volume will be lost)

### What is preserved

| Data | Preserved? | How |
|------|-----------|-----|
| `openclaw.json` config | **Yes** | Restored from backup |
| User edits (managed + unmanaged) | **Yes** | Restored from backup |
| Workspace files | **Yes** | Restored from backup |
| Nonce | **Yes** | Restored from backup (on data volume at `/data/.ocm-nonce`) |
| `auth-profiles.json` | **Yes** | Restored from backup |
| Soul files | **Yes** | Restored from backup |

### Automatic affinity break (no backup)

When a machine starts and its previous host is terminated/unavailable, `runtime.go` does a fresh `Reserve()` placement. This path does **not** attempt backup-restore — it creates a fresh data volume. User edits to unmanaged paths and workspace files are lost.

```
Machine starts → previous host is terminated/unavailable
    │
    ▼
Backend: UnassignMachineFromHost() or fresh Reserve()
    │  - Machine placed on a different host
    │
    ▼
Orchestrator on NEW host: Create()
    │  - Creates NEW data volume (host-local storage)
    │  - writeDataVolumeConfig() writes fresh seed config
    │  - Generates NEW nonce
    │  - Old data volume remains on old host (orphaned)
    │
    ▼
(Same as First Boot flow — full config pushed via OnRunning)
```

Data volumes are **host-local** (`DataDir/<machineID>.ext4`). Cross-host volume migration is handled via GCS backup/restore, not volume transfer.

---

## Lifecycle: Rootfs Upgrade (Config Version Bump)

```
New rootfs deployed (ROOTFS_CONFIG_VERSION changes)
    │
    ▼
VM boots with new rootfs, same data volume
    │
    ▼
Init script:
    │  1. config-current symlink exists from previous boot
    │  2. Reads .config-version from target dir → version mismatch
    │  3. Creates NEW timestamped config dir
    │  4. Copies forward ALL files from previous config dir (preserves user edits)
    │  5. Writes new .config-version marker
    │  6. Switches config-current symlink to new dir
    │  7. Gateway starts with migrated config
```

Old config dirs are garbage-collected (keeps latest 5).

Note: `ROOTFS_CONFIG_VERSION` only versions the `config-current` subtree. The data volume itself has a separate version tracked by `.ocm-version` with host-side `.pre-upgrade` backups managed by `ensureDataVolume()` / `RollbackDataVolume()`.

---

## Lifecycle: VM Destroyed and Recreated

```
User deletes machine → data volume is destroyed
User creates new machine with same settings
    │
    ▼
Orchestrator: creates fresh data volume
    │  - writeDataVolumeConfig() writes new seed config from DB
    │  - Generates new nonce
    │  - All previous VM-side edits are lost
    │
    ▼
(Same as First Boot flow)
```

---

## Config Precedence

The file on disk (`openclaw.json`) is the single source of truth. All writers modify the same file. Last writer wins for any given key.

| Writer | When | Scope |
|--------|------|-------|
| Pre-boot write | First boot only | Full file (seed config from DB) |
| OnRunning push | After gateway is up | Managed paths (full config from DB) |
| Backend config push | On running VM, after DB changes | Managed paths only (set/unset ops) |
| User edit inside VM | Anytime via `openclaw config set` | Any key |
| Gateway defaults | Startup | Keys not present in file |

Backend config pushes only touch managed paths. A user edit to an unmanaged path is never overwritten by a config push.

---

## Known Issues

### auth-profiles.json not updated on config push

**Symptom:** When providers are added or removed via the UI while a VM is running, `auth-profiles.json` becomes stale. The gateway may show incorrect models in the catalog.

**Cause:** `auth-profiles.json` is only written during the pre-boot `writeDataVolumeConfig()` call. The config push path (`pushMachineConfigInternal`) only sends `openclaw config set/unset` ops to modify `openclaw.json` — it does not regenerate `auth-profiles.json`.

**Impact:** Provider changes in `openclaw.json` take effect (gateway hot-reloads), but the model catalog may be incomplete until the VM is restarted (which triggers a fresh `auth-profiles.json` write).

**Fix needed:** Config push should regenerate `auth-profiles.json` via agent `WriteFile` when providers change.

### auth-profiles.json EACCES on first boot (fixed)

**Symptom:** Gateway crash-loops with `EACCES: permission denied, open '.../auth-profiles.json'` before eventually recovering.

**Cause:** The orchestrator created `config-current` as a real directory. The init script expected it to be a symlink and could not replace a non-empty directory with `ln -sfn`.

**Status:** Fixed — init script now detects real directories from the orchestrator's pre-boot write and migrates them to the timestamped dir scheme before proceeding.

---

## Diagram: Config Storage Architecture

```
┌─────────────────────────────────────────────────────────┐
│  Backend (Cloud Run)                                     │
│  ┌──────────────────────┐                                │
│  │ Database              │                               │
│  │ - machine_capabilities│  AssembleSeedConfig()          │
│  │ - machine_credentials │──────────────────┐            │
│  │ - machine_config      │                  │            │
│  │ - assembled_config    │                  ▼            │
│  └──────────────────────┘      seed openclaw.json        │
│                                      │                   │
│           OnRunning ─────────────────┼──── full config   │
│                                      │         │         │
└──────────────────────────────────────│─────────│─────────┘
                                       │         │
                    ┌──────────────────┼─────────┼────────┐
                    │  Pre-boot write  │  Config push     │
                    │  (Create only)   │  (OnRunning +    │
                    │                  │   manual push)   │
                    ▼                  ▼                  │
┌─────────────────────────────────────────────────────────┐
│  Host (GCE VM / Agent)                                   │
│  ┌─────────────────────────────────────────────────┐     │
│  │ Orchestrator                                     │    │
│  │  writeDataVolumeConfig()  │  ConfigBatch()       │    │
│  │  (loop-mount, write)      │  (exec in VM)        │    │
│  └───────────┬───────────────┴──────────┬──────────┘    │
│              │                          │               │
└──────────────│──────────────────────────│───────────────┘
               │                          │
┌──────────────│──────────────────────────│───────────────┐
│  Firecracker VM                         │               │
│              │                          │               │
│              ▼                          ▼               │
│  /data/ocm/configs/<ts>/      openclaw config set ...   │
│    ├── openclaw.json  ◄──────────── modifies ──────┘    │
│    ├── auth-profiles.json  (stale after provider change) │
│    └── SOUL.md                                          │
│              ▲                                          │
│              │ symlink                                  │
│  config-current ──┘                                     │
│              ▲                                          │
│              │ reads + watches                          │
│  ┌───────────┴──────────┐                               │
│  │ Gateway (OpenClaw)    │                              │
│  │ - hot-reloads on      │                              │
│  │   file change         │                              │
│  └───────────────────────┘                              │
└─────────────────────────────────────────────────────────┘
```
