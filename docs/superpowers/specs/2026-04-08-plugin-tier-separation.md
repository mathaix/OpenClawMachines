# DR-201: Plugin Tier Separation

Date: 2026-04-08
Status: Approved
Ticket: DR-201
Branch: Runtimedecouple
Prerequisite: DR-106 (baked OpenClaw removed from rootfs)

## Context

Plugins are currently installed in the rootfs Dockerfile (Opik, Composio), seeded to the data volume on first boot, and also copied into the artifact at build time. This creates three copies of the same plugins and a version coupling problem: rootfs-seeded plugins can drift from the artifact version.

Since plugins are tightly coupled to the OpenClaw version (API contracts, compatibility), they belong in the artifact — versioned together with OpenClaw.

## Goal

Establish two explicit plugin tiers with clear ownership:

1. **Bundled plugins** (read-only): Shipped inside the artifact's `dist/extensions/`. Tied to the OpenClaw version. Managed entirely by the artifact build pipeline.
2. **User plugins** (writable): Installed by users via `openclaw plugins install` in the terminal. Persisted on the data volume at `~/.openclaw/extensions/`. Survive OpenClaw upgrades. Cannot override bundled plugins.

## Non-Goals

- UI-driven plugin management (later phase)
- Plugin marketplace or registry
- Control-plane-driven plugin installation
- Per-plugin version pinning in machine config

## Design

### Plugin Discovery

The OpenClaw gateway discovers plugins from two paths:
- `OPENCLAW_BUNDLED_PLUGINS_DIR` env var → artifact's `dist/extensions/` (read-only)
- `~/.openclaw/extensions/` → user-installed plugins on data volume (writable)

Both paths already work with the OpenClaw gateway. No changes to plugin discovery itself.

### Duplicate Prevention

On every VM boot, the init script checks for plugin ID conflicts between the two tiers. If a user-installed plugin has the same ID as a bundled plugin, the user copy is deleted and a warning is logged. Bundled always wins.

The check reads `openclaw.plugin.json` from each plugin directory in both paths, extracts the `"id"` field, and compares.

### Artifact Build Changes

Move all plugin installation into the build script (`build-openclaw-runtime.sh`). Currently:
- Opik is installed via `npm install opik@latest` inside the build container (line 68) — this installs the opik SDK as a dependency, but the OpenClaw plugin wrapper (`@opik/opik-openclaw`) is installed in the Dockerfile and copied from `~/.openclaw/extensions/`.
- Composio is extracted from `composio-plugin.tgz` in the Dockerfile and copied from `~/.openclaw/extensions/`.

After this change:
- Both `@opik/opik-openclaw` and Composio are installed directly in the build script's container.
- The build script no longer depends on the Dockerfile having any plugin knowledge.
- The copy from `~/.openclaw/extensions/` (lines 130-133) is removed.
- Plugin versions are pinned in the build script alongside the OpenClaw version.

### Dockerfile Changes

Remove from `rootfs/Dockerfile.openclaw`:
- `ARG OPIK_PLUGIN_VERSION`
- `COPY composio-plugin.tgz`
- The entire Opik/Composio install RUN block (lines 151-167)
- The plugin move RUN block (lines 179-189)
- `composio-plugin.tgz` file from `rootfs/` directory

The rootfs becomes fully plugin-free. It provides: OS, Node.js, pnpm, system tools.

### Init Script Changes

**Remove first-boot plugin seeding** (lines 83-88 of `init-openclaw.sh`):
```bash
if [ -d /home/openclaw/.openclaw/extensions ] && [ ! -d /data/home/openclaw/.openclaw/extensions ]; then
    mkdir -p /data/home/openclaw/.openclaw
    cp -r /home/openclaw/.openclaw/extensions /data/home/openclaw/.openclaw/extensions
    chown -R openclaw:openclaw /data/home/openclaw/.openclaw/extensions
fi
```
This block copies rootfs plugins to the data volume. Since the rootfs no longer has plugins, remove it.

**Add duplicate-ID check** in the every-boot section (after runtime resolution, before gateway startup):
- Scan `$OCM_EFFECTIVE_PLUGINS_DIR` (bundled) for plugin IDs
- Scan `~/.openclaw/extensions/` (user) for plugin IDs
- If overlap found, delete the user copy and log: `[WARN] Removing user plugin '<id>' — conflicts with bundled plugin`

### Test Script Changes

Remove Opik and Composio plugin checks from `scripts/test-rootfs.sh` (lines 68-92). These plugins are no longer in the rootfs.

## Files Changed

| File | Action |
|------|--------|
| `scripts/build-openclaw-runtime.sh` | Install Opik + Composio plugins directly; remove `~/.openclaw/extensions/` copy |
| `rootfs/Dockerfile.openclaw` | Remove all plugin installation (Opik, Composio, ARGs, COPY, move block) |
| `rootfs/composio-plugin.tgz` | Delete |
| `scripts/init-openclaw.sh` | Remove first-boot plugin seeding; add duplicate-ID check |
| `scripts/test-rootfs.sh` | Remove Opik/Composio rootfs checks |

## Plugin Path Contract

| Tier | Path | Owner | Lifecycle | Writability |
|------|------|-------|-----------|-------------|
| Bundled | `$OPENCLAW_BUNDLED_PLUGINS_DIR` (`dist/extensions/`) | Artifact build | Tied to OpenClaw version | Read-only |
| User | `~/.openclaw/extensions/` on data volume | User (terminal install) | Persists across upgrades | Writable |

**Rules:**
- Bundled plugins cannot be modified or removed by users.
- User plugins persist across OpenClaw version changes.
- If a plugin ID exists in both tiers, bundled wins and user copy is removed at boot.
- User plugins are the user's responsibility — OCM does not manage, update, or guarantee compatibility.

## Verification

1. Artifact build produces `dist/extensions/` with Opik + Composio + OpenClaw bundled plugins
2. Rootfs builds successfully with no plugin content
3. VM boots, gateway loads bundled plugins from artifact
4. User can `openclaw plugins install <name>` and it persists across VM restart
5. User installs plugin with same ID as bundled → removed at next boot with warning
6. OpenClaw upgrade → bundled plugins update, user plugins unchanged
7. `make test` passes
