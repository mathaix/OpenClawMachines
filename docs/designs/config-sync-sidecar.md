# Config Sync Sidecar: Eliminating the OpenClaw Fork

## Status: Deferred — Managed appliance model chosen (2026-03-15)

> Phase 1 (fork elimination) remains independently valuable and may be revisited. Phase 2 (write-back) is deferred — the managed-appliance model means agent/skill CRUD moves to the control plane instead of syncing mutations back from the VM. See `docs/configuration_architecture.md` for the decision.

## Problem

OCM maintains a fork of OpenClaw (`~/openclaw`) with a `ConfigSource` patch that replaces file-based config loading with pluggable sources (HTTP, metadata). This patch must be rebased onto every new OpenClaw release, resolved through conflicts, and repackaged as a tarball for rootfs builds.

Additionally, runtime config mutations (agents, skills, config.set) are silently lost because the fork's `HttpConfigSource` sets `persistConfig = false` — writes go to a local file that's never read back. See `docs/designs/config-write-back.md` for the full gap analysis.

## Proposed Solution: Config Sync Sidecar

Replace the fork's `ConfigSource` patch with a sidecar process that syncs config between the metadata server and a local file. OpenClaw runs **completely unpatched** — it reads and writes a local JSON config file as designed.

### Architecture

```
┌─────────────────────────────── Firecracker VM ──────────────────────────────┐
│                                                                              │
│  ┌──────────────┐     reads/writes     ┌──────────────────────────────────┐  │
│  │   OpenClaw    │ ◄──────────────────► │  /run/openclaw/config.json      │  │
│  │   (gateway)   │     (native file     │  (tmpfs — RAM-backed)           │  │
│  │   unpatched   │      behavior)       └──────────┬───────────┬──────────┘  │
│  └──────────────┘                          pull ▼   │   ▲ push (Phase 2)     │
│                                       ┌─────────────┴───┴──────────┐         │
│                                       │    Config Sync Sidecar     │         │
│                                       │    (agent --config-sync)   │         │
│                                       └─────────────┬──────────────┘         │
│                                                     │                        │
└─────────────────────────────────────────────────────┼────────────────────────┘
                                                      │ HTTP
                                              ┌───────▼────────┐
                                              │ Metadata Server │
                                              │ /v1/config      │
                                              │ /v1/config-ver  │
                                              │ /v1/user-config │ (Phase 2)
                                              └────────────────┘
```

### How It Works

**Pull (metadata → file):**
1. Sidecar fetches `/v1/config` from metadata server
2. Writes to `/run/openclaw/config.json` (tmpfs — secrets stay in RAM)
3. OpenClaw's native file watcher detects the change, reloads config
4. Sidecar polls `/v1/config-version` every 5s for platform-initiated changes

**Push — Phase 2 (file → metadata):**
1. Sidecar watches `/run/openclaw/config.json` via inotify
2. When OpenClaw writes config changes (agent CRUD, skill install, config.set), sidecar detects
3. Sidecar reads the updated file, POSTs to `PUT /v1/user-config`
4. Backend stores user config overlay per machine
5. On next config serve: platform config + user overlay + credentials

## Phased Rollout

### Phase 1: Fork Elimination (pull-only)

**Goal:** Remove the fork patch. OpenClaw runs unpatched. Same write-back limitations as today (mutations lost on reload), but upgrade friction drops to zero.

**Changes:**

| Component | Change |
|-----------|--------|
| `agent` binary | Add `--config-sync` subcommand |
| `init-openclaw.sh` | Start sidecar before gateway; remove `OCM_CONFIG_SOURCE=metadata` |
| `rootfs/Dockerfile.openclaw` | Use upstream OpenClaw release (no fork tarball) |
| Metadata server | No changes — existing `/v1/config` and `/v1/config-version` sufficient |

**Sidecar behavior (Phase 1):**
```
1. Fetch /v1/config → write /run/openclaw/config.json
2. Fetch /v1/machine → extract gateway_token, tunnel_token, etc.
3. Signal "config ready" (touch /run/openclaw/.config-ready)
4. Loop:
   a. Sleep 5s
   b. Fetch /v1/config-version
   c. If version changed → fetch /v1/config → overwrite file
   d. Gateway's hot-reload file watcher picks up the change
```

**Init script changes:**
```bash
# Before (fork, OCM_CONFIG_SOURCE=metadata):
export OCM_CONFIG_SOURCE=metadata
export OCM_METADATA_URL='$METADATA_URL'
export OCM_METADATA_NONCE='$METADATA_NONCE'
openclaw gateway --port 18789 --bind loopback

# After (sidecar, unpatched OpenClaw):
mkdir -p /run/openclaw
agent --config-sync \
  --metadata-url "$METADATA_URL" \
  --metadata-nonce "$METADATA_NONCE" \
  --config-path /run/openclaw/config.json &
# Wait for initial config
while [ ! -f /run/openclaw/.config-ready ]; do sleep 0.1; done

export OPENCLAW_CONFIG_PATH=/run/openclaw/config.json
export OPENCLAW_STATE_DIR=/run/openclaw
openclaw gateway --port 18789 --bind loopback
```

**What this eliminates:**
- `ConfigSource` interface and all implementations (`file-source.ts`, `http-source.ts`, `metadata-source.ts`, `create-config-source.ts`)
- The `server.impl.ts` patch (cfgSource.startup, cfgSource.read, etc.)
- The fork build/cherry-pick/rebase process
- `openclaw-fork.tgz` in rootfs build
- `make update-openclaw` script (version bumps become trivial: change a tag in the Dockerfile)

### Phase 2: Config Write-Back (bidirectional sync)

**Goal:** Runtime config mutations (agents, skills, config.set) persist across gateway restarts and VM migrations.

**New components:**

#### Metadata Server: `PUT /v1/user-config`

```go
// handleUserConfigUpdate stores config mutations pushed from the VM sidecar.
func (s *Server) handleUserConfigUpdate(w http.ResponseWriter, r *http.Request) {
    cfg, ok := s.configFromRequest(w, r)
    if !ok {
        return
    }

    var body struct {
        Config json.RawMessage `json:"config"`
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        http.Error(w, "invalid body", http.StatusBadRequest)
        return
    }

    // Extract user overlay: diff the pushed config against platform config,
    // keeping only user-modified sections (agents, skills entries, etc.)
    overlay := extractUserOverlay(cfg.OpenClawConf, body.Config)

    // Store overlay in DB
    s.store.SetMachineUserConfig(ctx, cfg.MachineID, overlay)

    // Bump version so other watchers know config changed
    s.configVersions[vmIP]++
}
```

#### Config Assembly: Merge User Overlay

```go
// In configassembly.AssembleConfig, after step 6 (skills.allowBundled):

// 7. Merge user config overlay (persisted runtime mutations)
if params.UserConfigOverlay != nil {
    // Strip protected keys — user overlay cannot override platform settings
    sanitized := StripProtectedKeys(params.UserConfigOverlay)
    result = deepMerge(result, sanitized)
}

// 8. Marshal to JSON (was step 7)
```

#### Database: User Config Storage

```sql
-- Migration: add user_config column
ALTER TABLE machines ADD COLUMN user_config JSONB;
```

**Option A** (recommended): Single JSONB column on `machines` table. Simple, no extra joins, config is per-machine anyway.

**Option B**: Separate `machine_config_overrides` table with key-value sections. More normalized but adds query complexity for no real benefit.

#### Sidecar: Bidirectional Sync

```
Phase 2 sidecar loop:
1. [Pull] Poll /v1/config-version
   - If version bumped → fetch /v1/config → write to file
   - Record file mtime as "last-pull-mtime"
2. [Push] Watch file via inotify
   - On file change:
     - If mtime == last-pull-mtime → skip (this was our own pull write)
     - Else → read file → PUT /v1/user-config
     - Record returned version to suppress the echo
```

**Conflict avoidance:** The sidecar tracks the config version it last wrote. When it detects a file change that wasn't from a pull, it pushes to the metadata server. The server returns the new version number, which the sidecar uses to suppress the resulting pull echo.

## Secrets Handling

### Current (HttpConfigSource)

Secrets (API keys, channel tokens) are injected by the metadata server's `handleConfig` → `injectChannelCredentials`. The gateway holds them in process memory only. They never touch disk.

### With Sidecar

The metadata server's `/v1/config` response includes injected credentials (channel tokens in the config JSON). The sidecar writes this to `/run/openclaw/config.json`.

**Mitigation: tmpfs**

`/run` is already mounted as tmpfs in `init-openclaw.sh`:
```bash
mount -t tmpfs tmpfs /run && echo "  [OK] /run mounted" || echo "  [SKIP] /run"
```

Secrets written to `/run/openclaw/config.json`:
- Live in RAM only — never written to the ext4 rootfs disk
- Cleared on VM shutdown (tmpfs is volatile)
- Same security posture as process memory: an attacker with root access can read both

**File permissions:**
```bash
mkdir -p /run/openclaw
chmod 700 /run/openclaw
chown openclaw:openclaw /run/openclaw
```

Config file written with `0600` permissions, owned by `openclaw` user.

### LLM API Keys: Separate Path

LLM API keys (Anthropic, OpenAI, Google) are NOT in the config file. They're injected by the API proxy (metadata server `handleProviders` → proxy adds `Authorization` header). This doesn't change with the sidecar approach.

Channel tokens (Telegram botToken, Discord token) ARE in the config file, injected by `injectChannelCredentials`. These end up in the tmpfs file.

### Summary

| Secret Type | Current (HttpConfigSource) | Sidecar (tmpfs) | Changed? |
|-------------|---------------------------|-----------------|----------|
| LLM API keys | Proxy process memory | Proxy process memory | No |
| Channel tokens | Gateway process memory | tmpfs file + gateway memory | Minimal — still RAM-only |
| Gateway token | Env var | Env var | No |
| User secrets | Metadata `/v1/secrets` → env | Metadata `/v1/secrets` → env | No |
| Tunnel token | Metadata `/v1/machine` → env | Metadata `/v1/machine` → env | No |

## Implementation Effort

### Phase 1 (fork elimination): ~2-3 days

| Task | Effort |
|------|--------|
| `agent --config-sync` subcommand (pull loop + initial fetch) | 4h |
| Init script changes (start sidecar, wait for ready, set env vars) | 2h |
| Remove fork tarball from Dockerfile, use upstream OpenClaw | 1h |
| Test: gateway starts, reads config from file, hot-reloads on version change | 4h |
| Test: secrets on tmpfs, not on disk | 1h |
| Remove fork-related build scripts and Makefile targets | 1h |

### Phase 2 (write-back): ~3-4 days

| Task | Effort |
|------|--------|
| `PUT /v1/user-config` metadata endpoint | 3h |
| `extractUserOverlay` diffing logic | 4h |
| DB migration + store methods for user_config | 2h |
| Config assembly: merge user overlay | 2h |
| Sidecar push loop (inotify watch + conflict avoidance) | 4h |
| Test: agent create in Control UI survives gateway restart | 2h |
| Test: skill install persists across VM migration | 2h |
| Test: platform config changes don't clobber user agents | 3h |

## Alternatives Considered

### Keep the fork (current approach)

- Pro: Working today, well-understood
- Con: Every OpenClaw release requires cherry-pick, conflict resolution, repackaging
- Con: Write-back still requires fork changes (`cfgSource.write()`)
- Verdict: Increasing maintenance burden as OpenClaw evolves

### Upstream the ConfigSource patch

- Pro: No fork maintenance
- Con: OpenClaw maintainers may not accept it (niche use case)
- Con: Still need write-back changes in OpenClaw
- Con: Tied to upstream release cadence for OCM features
- Verdict: Best long-term if accepted, but uncertain timeline

### Full dashboard management (no write-back)

- Pro: Gateway truly read-only, clean separation
- Con: Must build agent/skill CRUD UI in OCM dashboard
- Con: Users lose Control UI and CLI for agent management inside VM
- Con: High effort, duplicates existing OpenClaw UX
- Verdict: Could complement sidecar approach later, but not a prerequisite

## Decision

**Recommended: Phase 1 first (fork elimination), then Phase 2 (write-back).**

Phase 1 is low-risk, immediately eliminates the fork maintenance burden, and is independently valuable even without write-back. Phase 2 can follow when config mutations become a user-facing issue.

## Related

- `docs/designs/config-write-back.md` — detailed gap analysis of lost mutations
- `docs/openclaw-config-source-proposal.md` — original ConfigSource design
- `docs/openclaw-version-management.md` — version management
- `scripts/update-openclaw.sh` — current fork upgrade script (obsoleted by Phase 1)
