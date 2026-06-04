# Init Script Refactor: Persistent State Across VM Reboots

**Status:** Design captured, not yet implemented
**Branch:** `UI-improvements`
**Date:** 2026-03-04
**Last updated:** 2026-03-05 (v3 — upgrade path for existing installs, dangling symlink fixes)

## Problem

When a Firecracker MicroVM reboots (stop/start cycle), all ephemeral state on the rootfs is lost. The data volume (`/dev/vdb` → `/data`) persists, but the init script currently:

1. **Deletes `device.json` on every boot** (step 9b) — forces TUI re-pairing each time, adding unnecessary delay
2. **Writes `auth-profiles.json` unconditionally** — causes churn even on crash restarts where nothing changed
3. **Writes `IDENTITY.md` to `/workspace/`** — lost if workspace symlinks to data volume but identity info hasn't changed
4. **Runs `chown -R` on the entire `.openclaw` dir** — gets slower as state grows (sessions, agent data)
5. **Has no config versioning** — when rootfs changes config format/defaults, old data volume state can conflict

## Design: Timestamped Config Directories

### Core Concept

Introduce a **versioned config directory** under `/data/ocm/configs/<timestamp>/` with a stable symlink at `~/.openclaw/config-current`. Gateway-expected paths (e.g., `agents/main/agent/auth-profiles.json`) become symlinks into the config dir.

```
/data/ocm/configs/
├── 20260304T120000Z/        # config from first boot
│   ├── .config-version      # "1"
│   ├── auth-profiles.json
│   ├── device.json
│   └── IDENTITY.md
├── 20260305T080000Z/        # config from rootfs upgrade
│   ├── .config-version      # "2"
│   ├── auth-profiles.json   # copied forward
│   ├── device.json          # copied forward (user edits preserved)
│   └── IDENTITY.md          # regenerated if missing
└── ...

/home/openclaw/.openclaw/
├── config-current → /data/ocm/configs/20260305T080000Z
├── identity/
│   └── device.json → config-current/device.json
├── agents/main/agent/
│   └── auth-profiles.json → config-current/auth-profiles.json
└── workspace/
```

### Version Detection & Reuse Logic

A `ROOTFS_CONFIG_VERSION` constant in the init script controls when a new config dir is created. The lookup is deterministic:

```
1. Read symlink: config-current → /data/ocm/configs/<ts>
2. If symlink exists AND target dir exists:
     a. Read <target>/.config-version
     b. If it equals ROOTFS_CONFIG_VERSION → REUSE (fast path, no copies)
     c. If it differs → CREATE NEW (version bump)
3. If symlink missing or target dir gone → CREATE NEW (first boot / recovery)
```

The symlink is the single source of truth — no scanning of timestamp dirs, no "highest timestamp" heuristic. This makes the reuse decision O(1) and unambiguous.

**Idempotency guarantee:** Re-running init with the same `ROOTFS_CONFIG_VERSION` will not create new dirs. The fast path reads the symlink, checks the version, and skips.

This decouples "rootfs changed" from "config format changed" — most rootfs rebuilds won't bump the config version.

### State Diagram

```
                    ┌──────────────────────┐
                    │  Read config-current  │
                    └──────────┬───────────┘
                               │
                    ┌──────────▼───────────┐
                    │ Symlink valid + dir   │
                    │     exists?           │
                    └──┬───────────────┬───┘
                      yes              no
                       │                │
              ┌────────▼────────┐       │
              │ .config-version │       │
              │ == ROOTFS_CFG_V?│       │
              └──┬──────────┬──┘       │
                yes         no         │
                 │           │          │
        ┌────────▼──┐  ┌────▼──────────▼──┐
        │  REUSE    │  │  CREATE NEW       │
        │  (no-op)  │  │  - mkdir <ts>     │
        └───────────┘  │  - copy forward   │
                       │  - write .version │
                       │  - ln -sfn        │
                       │  - GC old dirs    │
                       └──────────────────┘

  Provider change (same config version):
  ┌─────────────────────────────────────────┐
  │ start_gateway() always re-reads         │
  │ /v1/providers, generates auth-profiles, │
  │ checksums against existing file in      │
  │ current config dir, writes only if      │
  │ changed (atomic .tmp + mv)              │
  └─────────────────────────────────────────┘
```

### Timestamp Collision Handling

Two boots in the same UTC second would generate the same timestamp dir name. To guarantee uniqueness:

```bash
ts=$(date -u +%Y%m%dT%H%M%SZ)
NEW_CONFIG="$CONFIG_BASE/$ts"
if [ -d "$NEW_CONFIG" ]; then
    # Collision — append monotonic suffix
    suffix=1
    while [ -d "${NEW_CONFIG}-${suffix}" ]; do
        suffix=$((suffix + 1))
    done
    NEW_CONFIG="${NEW_CONFIG}-${suffix}"
fi
mkdir -p "$NEW_CONFIG"
```

In practice, Firecracker boot takes seconds so collisions are near-impossible, but the suffix handles it defensively.

### Garbage Collection

Keep the latest 5 config dirs, delete older ones. Prevents unbounded growth on long-lived VMs.

**Safety rule:** Never delete the dir that `config-current` currently points to, regardless of age ranking:

```bash
CURRENT_TARGET=$(readlink -f "$CONFIG_LINK" 2>/dev/null || echo "")
ls -dt "$CONFIG_BASE"/[0-9]* 2>/dev/null | tail -n +6 | while read -r old; do
    old_real=$(readlink -f "$old")
    [ "$old_real" = "$CURRENT_TARGET" ] && continue  # never GC the active config
    rm -rf "$old"
    echo "  [GC] Removed old config: $old"
done
```

### Consumer Symlinks

The gateway reads files from hardcoded paths. Rather than changing the gateway, symlink those paths into `config-current/`:

| Gateway path | Symlink target |
|---|---|
| `~/.openclaw/agents/main/agent/auth-profiles.json` | `config-current/auth-profiles.json` |
| `~/.openclaw/identity/device.json` | `config-current/device.json` |
| `/workspace/IDENTITY.md` | `config-current/IDENTITY.md` |

**File watcher compatibility:** The gateway uses Node.js `fs.readFileSync` / `fs.writeFileSync` for these files — no persistent `fs.watch`. Node follows symlinks transparently on read/write. The two-hop chain (`gateway-path → config-current → /data/ocm/configs/<ts>/file`) resolves via kernel VFS, invisible to Node. If the gateway ever adds `fs.watch`, it should use `{ persistent: false }` and follow symlinks (the default) — but this is not currently a concern.

### Upgrade Path for Existing Installs

Existing VMs have been running the old init script. On the first boot with the new rootfs:

#### Pre-upgrade state (old init script)

```
/home/openclaw/.openclaw/
├── agents/main/agent/
│   └── auth-profiles.json      # real file (written by old start_gateway)
├── identity/
│   └── (device.json absent)    # old 9b deletes it every boot
├── workspace/
└── ...

/data/
├── home/openclaw/              # symlinked from /home/openclaw
├── workspace/                  # symlinked from /workspace
├── .ocm-version                # data volume version
└── (no /data/ocm/configs/)     # doesn't exist yet
```

#### What happens on first boot with new init

| Step | Action | Notes |
|------|--------|-------|
| 1b | `mkdir -p /data/ocm/configs` | Creates new dir on data volume |
| 1b | Dotfile seeding | Skipped — `/home/openclaw` is already a symlink (`[ ! -L ]` guard) |
| 10 | `config-current` symlink missing → `NEED_NEW_CONFIG=1` | Creates `/data/ocm/configs/20260305T...Z/`, writes `.config-version` |
| 10 | No previous config to copy forward | Empty dir except `.config-version` |
| 10 | Migrate `auth-profiles.json` | **Must copy real file into config dir before symlinking** (see below) |
| 10 | Migrate `device.json` | Absent (old script deleted it) — **don't create dangling symlink** |
| 10 | IDENTITY.md | Written fresh to config dir, symlinked to `/workspace/` |
| 9b | Device identity | No `config-current/device.json` to preserve — first TUI connect creates it |
| `start_gateway()` | Auth-profiles | Re-fetches providers, writes to config dir, ensures symlink |

#### Dangling symlink problem

The naive approach — create symlinks immediately in section 10 — creates **dangling symlinks** for files that don't yet exist in the config dir:

1. **`auth-profiles.json`**: Old real file at `agents/main/agent/auth-profiles.json` would be overwritten by a symlink pointing to `config-current/auth-profiles.json` which doesn't exist yet (written later in `start_gateway()`). Between section 10 and gateway start, the symlink is dangling.

2. **`device.json`**: Old script deleted it every boot, so it's absent. Symlinking to `config-current/device.json` creates a dangling symlink. When the gateway tries to write a new device identity via `fs.writeFileSync` through the dangling symlink, Node.js throws ENOENT.

#### Solution: migrate-then-symlink with existence guards

```bash
# --- auth-profiles.json: migrate existing real file first ---
auth_src="$OPENCLAW_DIR/agents/main/agent/auth-profiles.json"
config_real=$(readlink -f "$CONFIG_LINK")
if [ -f "$auth_src" ] && [ ! -L "$auth_src" ]; then
    # Copy old real file into config dir so symlink has a target
    cp "$auth_src" "$config_real/auth-profiles.json"
    echo "  [OK] Migrated auth-profiles.json to config dir"
fi
# Now safe to symlink (target exists or will be created by start_gateway)
ln -sfn "$CONFIG_LINK/auth-profiles.json" "$auth_src"

# --- device.json: only symlink if target exists ---
device_src="$OPENCLAW_DIR/identity/device.json"
if [ -f "$device_src" ] && [ ! -L "$device_src" ]; then
    # Real file exists (VM stopped mid-session) — migrate it
    mv "$device_src" "$config_real/device.json"
    ln -sfn "$CONFIG_LINK/device.json" "$device_src"
    echo "  [OK] Migrated device.json to config dir"
elif [ -f "$CONFIG_LINK/device.json" ]; then
    # Config dir has it (reboot case) — just symlink
    ln -sfn "$CONFIG_LINK/device.json" "$device_src"
else
    # Neither exists — leave identity/device.json absent (no dangling symlink)
    # Gateway writes real file here on first TUI connect.
    # Next boot, migration code moves it into config dir.
    echo "  [OK] device.json: will be created on first TUI connect"
fi
```

#### Two-boot convergence for device.json

| Boot | device.json state | What happens |
|------|-------------------|--------------|
| **1st boot (upgrade)** | Absent (old script deleted it) | No symlink created. Gateway writes real file to `identity/device.json` on first TUI connect. |
| **2nd boot** | Real file at `identity/device.json` | Migration code moves it to `config-current/device.json`, creates symlink. Now persistent. |
| **3rd+ boot** | Symlink to `config-current/device.json` | Preserved. Fast path. |

This is safe — no dangling symlinks, no ENOENT. The only cost is one extra TUI re-pair on the upgrade boot (same as current behavior).

## Design Decisions & Rationale

### 1. Preserve `device.json` across reboots

**Before:** Deleted on every boot → TUI re-pairs each time
**After:** Preserved in config dir, symlinked to gateway's expected path
**Escape hatch:** `RESET_DEVICE_IDENTITY=1` on the kernel cmdline (read from `/proc/cmdline` via existing `$CMDLINE` var)
**Why:** The gateway's `shouldAllowSilentLocalPairing` auto-approves local requests anyway, so a fresh identity also auto-pairs — but preserving avoids the ~2s re-pairing delay and generates less noise in gateway logs.

**Reset mechanism detail:** The init script reads `RESET_DEVICE_IDENTITY=1` from `/proc/cmdline` (same `$CMDLINE` variable used for `ip=`, `metadata_nonce=`, etc.). This check runs in section 9b, **after** section 10 creates/reuses the config dir and establishes the `config-current` symlink. Order matters: the symlink must exist before we can `rm -f "$CONFIG_LINK/device.json"`. The kernel cmdline is the right mechanism (not env var) because the agent controls cmdline args when launching the VM.

### 2. Atomic `auth-profiles.json` writes with checksum

**Before:** Overwritten on every gateway start
**After:** Write to `.tmp` then `mv` (atomic), only if md5sum differs from existing
**Why:** On crash restarts, the config hasn't changed — writing it again just creates unnecessary I/O and can briefly leave a truncated file if the crash happens mid-write.

**Change detection detail:** `start_gateway()` always re-fetches `/v1/providers` from the metadata endpoint (source of truth). It generates the full `auth-profiles.json` in memory, computes `md5sum`, and compares against the existing file's checksum. This means:
- **Provider list changes** (e.g., user adds OpenRouter) trigger a write even if `ROOTFS_CONFIG_VERSION` hasn't changed
- **Crash restarts** with no provider changes skip the write (checksums match)
- **Config watcher restarts** (SIGUSR1) also re-fetch and compare — only write if providers actually changed

The auth-profiles file lives in `config-current/` and is accessed via consumer symlink from `agents/main/agent/`. The `start_gateway()` function writes to the real path (resolved via `readlink -f`) and ensures the symlink exists afterward.

### 3. Targeted `chown` instead of recursive

**Before:** `chown -R openclaw:openclaw "$OPENCLAW_DIR"` — walks the entire tree
**After:** Explicit list of directories we created:
```bash
chown openclaw:openclaw "$OPENCLAW_DIR" \
    "$OPENCLAW_DIR/workspace" \
    "$OPENCLAW_DIR/agents" "$OPENCLAW_DIR/agents/main" \
    "$OPENCLAW_DIR/agents/main/agent" "$OPENCLAW_DIR/agents/main/sessions" \
    "$OPENCLAW_DIR/identity"
```
**Why:** As session data grows, recursive chown gets slower. We only need to own the directories we create; files inside are created by the openclaw user.

**Config dirs ownership:** New config dirs under `/data/ocm/configs/<ts>/` get `chown -R openclaw:openclaw` at creation time (section 10). This is a one-time recursive chown on a small, newly-created dir (typically 0-3 files copied forward). The `/data/ocm/configs/` parent is created in section 1b alongside other data volume dirs.

### 4. Dotfile seeding on first boot

**Before:** `rm -rf /home/openclaw` replaces the dir with a symlink to `/data/home/openclaw`, losing `.bashrc`, `.profile`, `.bash_logout`
**After:** Before the symlink swap, copy dotfiles from rootfs to data volume (only if they don't already exist there)
**Why:** Users may customize dotfiles in-VM. First boot seeds defaults; subsequent boots preserve their edits.

**Seed rule (copy-if-missing):**
```bash
# Only runs when /home/openclaw is still a real directory (not yet symlinked)
if [ -d /home/openclaw ] && [ ! -L /home/openclaw ]; then
    for f in /home/openclaw/.bashrc /home/openclaw/.profile /home/openclaw/.bash_logout; do
        [ -f "$f" ] && [ ! -f "/data/home/openclaw/$(basename "$f")" ] && \
            cp "$f" "/data/home/openclaw/$(basename "$f")"
    done
fi
# THEN: rm -rf /home/openclaw && ln -sf /data/home/openclaw /home/openclaw
```

The `[ ! -L /home/openclaw ]` guard ensures this only runs on first boot (before the symlink swap). On subsequent boots, `/home/openclaw` is already a symlink to `/data/home/openclaw`, so the guard skips. User edits to `.bashrc` etc. on the data volume are never overwritten.

### 5. IDENTITY.md in config dir, not workspace

**Before:** Written directly to `/workspace/IDENTITY.md`
**After:** Written to `config-current/IDENTITY.md`, symlinked to `/workspace/`
**Why:** If hostname/slug haven't changed, preserve the existing file. Consumer symlink means the workspace path still works.

### 6. Config version vs data volume version

Two separate versioning schemes:

| Scheme | Location | Controls |
|---|---|---|
| `ROOTFS_DATA_VERSION` | `/data/.ocm-version` | Data volume migrations (e.g., creating `.local/bin`) |
| `ROOTFS_CONFIG_VERSION` | `/data/ocm/configs/<ts>/.config-version` | Config format/defaults changes |

They're independent because data volume migrations (creating directories, changing permissions) are different from config file format changes.

### 7. Fallback when data volume is missing

If `/data` is not mounted (no `/dev/vdb`, or mount failed), the entire config versioning scheme is skipped:

```bash
if mountpoint -q /data 2>/dev/null; then
    # ... timestamped config dirs, symlinks, GC ...
else
    echo "  [WARN] No data volume — config versioning disabled"
fi
```

**Behavior without data volume:**
- No `config-current` symlink is created
- `auth-profiles.json` is written directly to `agents/main/agent/` (current behavior, no symlink indirection)
- `device.json` is ephemeral (deleted on reboot, as before)
- `IDENTITY.md` is written directly to `/workspace/` (current behavior)
- A warning is logged so operators know persistence is degraded

This is the correct fallback: VMs without a data volume are inherently ephemeral, so there's nothing to preserve. No temp-dir-on-rootfs workaround is needed.

## Files Changed

All changes are in `scripts/init-openclaw.sh`:

| Section | What changes |
|---|---|
| 1b (data volume) | Add `/data/ocm/configs` mkdir, dotfile seeding |
| 8 (fetch config) | Extract `MACHINE_SLUG`/`VM_HOSTNAME` earlier (no longer write IDENTITY.md here) |
| 10 (gateway config) | Entire timestamped config directory system, IDENTITY.md, consumer symlinks, targeted chown |
| 9b (device identity) | Preserve instead of delete, add `RESET_DEVICE_IDENTITY` escape hatch |
| `start_gateway()` | Write auth-profiles.json to config dir (atomic, checksummed), maintain consumer symlink |

## Risks & Open Questions

1. **Symlink chain depth** — gateway reads `agents/main/agent/auth-profiles.json` which is a symlink to `config-current/auth-profiles.json`, and `config-current` is itself a symlink to `/data/ocm/configs/<ts>`. Two hops. Should work fine — Node.js resolves via kernel VFS. No file watchers in play (confirmed: gateway uses `readFileSync`/`writeFileSync`).

2. **Race on crash restart** — if the gateway crashes mid-write of `auth-profiles.json`, the atomic write (`.tmp` + `mv`) prevents corruption. GC safety rule (never delete `config-current` target) prevents broken symlinks.

3. ~~**First-boot migration**~~ → **Resolved.** Migrate-then-symlink pattern with existence guards prevents dangling symlinks. `device.json` uses two-boot convergence (real file on upgrade boot, migrated on next boot). See "Upgrade Path" section.

4. **Config watcher interaction** — the config watcher sends SIGUSR1 to restart the gateway on config version change (metadata `/v1/config-version`). This is independent of `ROOTFS_CONFIG_VERSION`. The new `start_gateway()` re-reads providers and writes auth-profiles atomically — clean interaction.

5. **`start_gateway()` must handle both modes** — when data volume is present, it writes to config dir via `readlink -f "$CONFIG_LINK"`. When absent, `$CONFIG_LINK` doesn't exist and `readlink -f` fails — must fall back to writing directly to `agents/main/agent/`. Use: `config_dir=$(readlink -f "$CONFIG_LINK" 2>/dev/null || echo "$OPENCLAW_DIR")`.

## Testing Plan

### Core scenarios

1. **Fresh VM (no data volume history):** Verify initial config dir created, dotfiles seeded, IDENTITY.md written, device.json created on first TUI connect
2. **Reboot (same rootfs):** Verify config dir reused (not re-created), device.json preserved, auth-profiles only written if changed. Confirm no new dirs in `/data/ocm/configs/`.
3. **Rootfs upgrade (bumped ROOTFS_CONFIG_VERSION):** Verify new config dir created, files copied forward from previous, old config GC'd after 5 dirs accumulate
4. **RESET_DEVICE_IDENTITY=1:** Verify device.json deleted from config dir, TUI re-pairs on next connect
5. **Crash restart:** Verify auth-profiles not rewritten if unchanged (checksum match in logs)

### Edge cases

6. **Provider change without config version bump:** User adds OpenRouter via dashboard → config watcher fires SIGUSR1 → `start_gateway()` re-fetches `/v1/providers` → auth-profiles.json updated atomically in current config dir (checksum differs). Verify new provider appears in auth-profiles without creating a new config dir.
7. **No data volume:** VM launched without `/dev/vdb`. Verify warning logged, init completes successfully, gateway starts with direct file writes (no symlinks), everything works as before.
8. **Idempotency:** Run init twice with same `ROOTFS_CONFIG_VERSION`. Verify only one config dir exists, no duplicate writes, no errors.
9. **GC safety:** Accumulate 7 config dirs (manually create extras), verify GC removes oldest 2 but never the one `config-current` points to.
10. **Timestamp collision:** Manually create a dir matching the expected timestamp before init runs. Verify init appends `-1` suffix and proceeds.

### Upgrade scenarios

11. **Existing VM, first boot with new rootfs:** Verify:
    - `/data/ocm/configs/` created
    - auth-profiles.json migrated from real file to config dir (not dangling)
    - device.json absent → no symlink created → gateway writes real file on TUI connect
    - IDENTITY.md written to config dir and symlinked to `/workspace/`
    - Gateway starts and serves requests normally
12. **Existing VM, second boot after upgrade:** Verify:
    - device.json (real file from first TUI connect) migrated into config dir, symlinked
    - Config dir reused (same `ROOTFS_CONFIG_VERSION`)
    - device.json preserved → no re-pairing
13. **Existing VM with device.json present (stopped mid-session):** Verify:
    - `device.json` real file detected, moved into config dir, symlinked in one boot
    - No two-boot convergence needed (single-boot migration)
