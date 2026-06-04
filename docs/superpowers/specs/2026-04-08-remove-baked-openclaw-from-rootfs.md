# DR-106: Remove Baked OpenClaw from Rootfs

Date: 2026-04-08
Status: Approved
Ticket: DR-106
Branch: Runtimedecouple
Prerequisite: PR #67 (artifact is the only runtime path, legacy_baked/auto removed)

## Context

PR #67 made artifact the only runtime path. The `legacy_baked` and `auto` fallback modes were fully removed (migration 069). The rootfs Dockerfile still installs OpenClaw via `pnpm install -g openclaw`, de-hardlinks its extensions, removes unused bundled plugins, and writes an `/etc/ocm-extensions-dir` marker. None of this is used at runtime anymore — the artifact delivery chain (GCS download, host staging, ext4 image, VM boot) is the sole path. The baked install is dead weight adding ~250MB+ to the rootfs image.

## Goal

Remove all baked OpenClaw installation steps from the rootfs Dockerfile and eliminate downstream references to the pnpm-installed OpenClaw in the build script and init script.

## Non-Goals

- Removing pnpm itself (users need it for installing npm packages at runtime).
- Restructuring the plugin installation (Opik, Composio stay in the Dockerfile — the artifact build script copies them from the Docker image).
- Changing the artifact build pipeline beyond removing the stale pnpm extension copy.
- Rootfs ext4 sizing changes (follow-up if meaningful savings observed).

## Changes

### 1. `rootfs/Dockerfile.openclaw`

**Remove (lines 151-187):**
- `ARG OPENCLAW_VERSION=2026.4.2` and `ARG OPENCLAW_CONTROL_UI_VERSION=2026.4.2`
- `pnpm install -g openclaw@${OPENCLAW_VERSION}` + `chmod -R o+rX`
- De-hardlink step (copy/remove/move dance for pnpm nlink>1 workaround)
- `/etc/ocm-extensions-dir` marker file creation
- Unused bundled plugin removal block (the `case` statement removing device-pair, phone-control, talk-voice, browser plugins)

**Keep:**
- pnpm + corepack installation (lines 146-149) — needed for user runtime
- Opik + Composio plugin installation (lines 189-214) — artifact build copies these
- Plugin move to openclaw user home (lines 226-236) — artifact build reads from `/home/openclaw/.openclaw/extensions/`

**Update:**
- The Opik/Composio install block currently runs `openclaw doctor --fix` (line 204) — this depends on the baked `openclaw` binary being on PATH. Since it won't be, remove or guard that line.
- The `CMD` at line 260 references `openclaw` — update to a no-op or informational message since the rootfs is no longer self-contained.

### 2. `scripts/build-openclaw-runtime.sh`

**Remove (lines 143-147):**
```bash
PNPM_EXT_DIR="$($DOCKER exec "${CONTAINER_ID}" cat /etc/ocm-extensions-dir 2>/dev/null || true)"
if [ -n "${PNPM_EXT_DIR}" ] && $DOCKER exec "${CONTAINER_ID}" test -d "${PNPM_EXT_DIR}" 2>/dev/null; then
    echo "Copying bundled extensions from pnpm build..."
    $DOCKER cp "${CONTAINER_ID}:${PNPM_EXT_DIR}/." "${EXT_DIR}"
fi
```

The npm flat install already produces `dist/extensions/` with bundled plugins. The pnpm copy was a redundant second source.

**Fix version detection (line 56):**
The current detection runs `openclaw --version` via the pnpm-installed binary. After removal, detect version from the npm flat install instead:
- Option: pass `OPENCLAW_VERSION` as a required env var (already supported via `BUILD_ARGS`)
- Or: read it from the npm-installed `package.json` after install completes

### 3. `scripts/init-openclaw.sh`

**Update line 331:**
```bash
_default_plugins_dir=$(cat /etc/ocm-extensions-dir 2>/dev/null || true)
```
Remove this line. The `OCM_BUNDLED_PLUGINS_DIR` env var (set by the orchestrator from the artifact's manifest) is the only source. Line 340 already uses it:
```bash
OCM_EFFECTIVE_PLUGINS_DIR="${OCM_BUNDLED_PLUGINS_DIR:-$_default_plugins_dir}"
```
Simplify to:
```bash
OCM_EFFECTIVE_PLUGINS_DIR="${OCM_BUNDLED_PLUGINS_DIR}"
```

If `OCM_BUNDLED_PLUGINS_DIR` is empty, fail with an actionable error rather than silently falling back to a nonexistent marker file.

## Files Changed

| File | Action |
|------|--------|
| `rootfs/Dockerfile.openclaw` | Remove baked openclaw install + de-hardlink + plugin cleanup |
| `scripts/build-openclaw-runtime.sh` | Remove pnpm extension copy, fix version detection |
| `scripts/init-openclaw.sh` | Remove `/etc/ocm-extensions-dir` fallback |

## Verification

1. `make build-upload-rootfs` — rootfs builds successfully without baked openclaw
2. Artifact build (`make build-openclaw`) — still bundles plugins correctly
3. `make test` — existing Go + frontend tests pass
4. Integration tests — VM boots with artifact runtime, gateway starts
5. Rootfs image size — confirm reduction (expect ~250MB smaller compressed)

## Risks

- **Build script version detection**: If `OPENCLAW_VERSION` is not passed, the build script currently auto-detects from the pnpm binary. Must ensure detection works from the npm install path instead.
- **`openclaw doctor --fix`**: Currently runs during Opik/Composio install. Without baked openclaw on PATH, this will fail. Low risk — it was a workaround for OpenClaw 4.5 config alias removal and is already guarded with `|| true`.
