# DR-201: Plugin Tier Separation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move all plugin installation into the artifact build script and make the rootfs plugin-free, establishing two explicit tiers: bundled (read-only, in artifact) and user (writable, on data volume).

**Architecture:** The build script already installs Opik's npm dependency — we add the OpenClaw plugin wrapper and Composio alongside it, then stop relying on the Dockerfile for plugin installation entirely. The init script drops first-boot plugin seeding and gains a duplicate-ID check. The rootfs becomes a generic infrastructure image.

**Tech Stack:** Docker, shell scripts, `composio-plugin.tgz` (custom tarball)

---

### Task 1: Move plugin installation into the artifact build script

**Files:**
- Modify: `scripts/build-openclaw-runtime.sh:64-71,124-133`

- [ ] **Step 1: Add Composio plugin installation to the build container**

The build script runs npm inside a Docker container (lines 66-71). Currently it installs `openclaw` and the `opik` npm SDK. We need to also install `@opik/opik-openclaw` (the OpenClaw plugin wrapper) and extract the Composio plugin from its tgz.

The `composio-plugin.tgz` file lives at `rootfs/composio-plugin.tgz`. We need to copy it into the container before the install. The `COPY` instruction in the Dockerfile already puts it at `/tmp/composio-plugin.tgz` — but since we're removing it from the Dockerfile in Task 2, we need to copy it into the container directly from the build script.

First, move `rootfs/composio-plugin.tgz` to a new location at project root level since it's no longer a rootfs concern:

```bash
mv rootfs/composio-plugin.tgz plugins/composio-plugin.tgz
```

Create the `plugins/` directory first if it doesn't exist.

Then modify the build script. Replace lines 64-71:

```bash
# Install inside a container and copy out the flat tree.
# npm --prefix puts everything under PREFIX/lib/node_modules/.
CONTAINER_ID="$($DOCKER run -d "${IMAGE_NAME}" /bin/sh -c "
	npm install -g openclaw@${OPENCLAW_VERSION} --prefix /tmp/oc 2>&1 && \
	npm install --prefix /tmp/oc/lib/node_modules/openclaw opik@latest 2>&1 && \
	echo 'NPM_INSTALL_OK' > /tmp/oc/.done && \
	sleep 600
")"
```

With:

```bash
# Install inside a container and copy out the flat tree.
# npm --prefix puts everything under PREFIX/lib/node_modules/.
# Platform plugins (opik-openclaw, composio) are bundled here alongside openclaw
# so they're versioned together in the artifact.
CONTAINER_ID="$($DOCKER run -d "${IMAGE_NAME}" /bin/sh -c "
	npm install -g openclaw@${OPENCLAW_VERSION} --prefix /tmp/oc 2>&1 && \
	npm install --prefix /tmp/oc/lib/node_modules/openclaw opik@latest 2>&1 && \
	echo 'NPM_INSTALL_OK' > /tmp/oc/.done && \
	sleep 600
")"
```

(No change to the npm commands yet — opik SDK is already installed. We'll handle plugin extraction in the copy step below.)

- [ ] **Step 2: Install plugins into dist/extensions/ during artifact packaging**

Replace lines 124-133 (the plugin copy block):

```bash
# --- Copy bundled plugins into the openclaw package's dist/extensions ---
# The artifact is the single source of truth for plugins.
# User-installed plugins (opik, composio) are copied from ~/.openclaw/extensions/.
# The init script must NOT also place them in ~/.openclaw/extensions/ or openclaw will see duplicates.
EXT_DIR="${OC_PKG}/dist/extensions"
mkdir -p "${EXT_DIR}"
if $DOCKER exec "${CONTAINER_ID}" test -d /home/openclaw/.openclaw/extensions 2>/dev/null; then
	echo "Copying user-installed plugins (opik, composio)..."
	$DOCKER cp "${CONTAINER_ID}:/home/openclaw/.openclaw/extensions/." "${EXT_DIR}"
fi
```

With:

```bash
# --- Bundle platform plugins into dist/extensions ---
# These are bundled alongside openclaw in the artifact so versions stay in sync.
# User-installed plugins live separately on the data volume (~/.openclaw/extensions/).
OPIK_PLUGIN_VERSION="${OPIK_PLUGIN_VERSION:-0.2.9}"
EXT_DIR="${OC_PKG}/dist/extensions"
mkdir -p "${EXT_DIR}"

echo "Installing opik-openclaw plugin v${OPIK_PLUGIN_VERSION}..."
$DOCKER exec "${CONTAINER_ID}" /bin/sh -c "
	cd /tmp && \
	npm pack @opik/opik-openclaw@${OPIK_PLUGIN_VERSION} --quiet 2>&1 && \
	mkdir -p /tmp/opik-openclaw && \
	tar -xzf /tmp/opik-opik-openclaw-*.tgz -C /tmp/opik-openclaw --strip-components=1 && \
	rm -f /tmp/opik-opik-openclaw-*.tgz
"
$DOCKER cp "${CONTAINER_ID}:/tmp/opik-openclaw" "${EXT_DIR}/opik-openclaw"

echo "Installing composio plugin..."
COMPOSIO_TGZ="${PROJECT_ROOT}/plugins/composio-plugin.tgz"
if [ ! -f "${COMPOSIO_TGZ}" ]; then
	echo "ERROR: ${COMPOSIO_TGZ} not found"
	exit 1
fi
mkdir -p "${EXT_DIR}/composio"
tar -xzf "${COMPOSIO_TGZ}" -C "${EXT_DIR}/composio" --strip-components=1

echo "Bundled plugins: $(ls "${EXT_DIR}")"
```

- [ ] **Step 3: Commit**

```bash
mkdir -p plugins
mv rootfs/composio-plugin.tgz plugins/composio-plugin.tgz
git add scripts/build-openclaw-runtime.sh plugins/composio-plugin.tgz
git rm rootfs/composio-plugin.tgz
git commit -m "feat(build): install plugins directly in artifact build script (DR-201)

Move opik-openclaw and composio plugin installation from Dockerfile
into the artifact build script. Plugins are now bundled in the artifact's
dist/extensions/ alongside openclaw, versioned together.

Moved composio-plugin.tgz from rootfs/ to plugins/ since it's no longer
a rootfs concern.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Remove plugin installation from Dockerfile

**Files:**
- Modify: `rootfs/Dockerfile.openclaw:151-189`

- [ ] **Step 1: Remove plugin installation blocks**

Remove lines 151-189 from `rootfs/Dockerfile.openclaw`. This includes:
- `ARG OPIK_PLUGIN_VERSION=0.2.9`
- `COPY composio-plugin.tgz /tmp/composio-plugin.tgz`
- The entire `RUN apt-get ... npm pack ... tar -xzf ...` block (lines 157-167)
- The "Clean package manager caches" comment can stay but remove the reference to "Note: agent binary..."
- The plugin move block (lines 179-189)

Remove:
```dockerfile
# Install Opik + Composio plugins.
# Plugins are extracted manually (npm pack + tar) rather than via `openclaw plugins install`
# to avoid openclaw CLI dependency. Files land in /root/.openclaw/extensions/ and are
# moved to the openclaw user's home after user creation below.
ARG OPIK_PLUGIN_VERSION=0.2.9
COPY composio-plugin.tgz /tmp/composio-plugin.tgz
RUN apt-get update && apt-get install -y --no-install-recommends python3 \
    && cd /tmp \
    && npm pack @opik/opik-openclaw@${OPIK_PLUGIN_VERSION} --quiet \
    && mkdir -p /root/.openclaw/extensions/opik-openclaw \
    && tar -xzf "/tmp/opik-opik-openclaw-${OPIK_PLUGIN_VERSION}.tgz" -C /root/.openclaw/extensions/opik-openclaw --strip-components=1 \
    && rm -f /tmp/opik-opik-openclaw-*.tgz \
    && mkdir -p /root/.openclaw/extensions/composio \
    && tar -xzf /tmp/composio-plugin.tgz -C /root/.openclaw/extensions/composio --strip-components=1 \
    && rm -f /tmp/composio-plugin.tgz \
    && apt-get purge -y --auto-remove python3 \
    && apt-get clean && rm -rf /var/lib/apt/lists/*
```

Also remove lines 179-189:
```dockerfile
# Move plugins from root's home to openclaw user's extensions dir.
# The installer placed them at /root/.openclaw/extensions/{opik-openclaw,composio}/.
RUN mkdir -p /home/openclaw/.openclaw/extensions \
    && for plugin in opik-openclaw composio; do \
         if [ -d "/root/.openclaw/extensions/$plugin" ]; then \
           cp -r "/root/.openclaw/extensions/$plugin" /home/openclaw/.openclaw/extensions/ \
           && echo "Moved plugin: $plugin"; \
         fi; \
       done \
    && chown -R openclaw:openclaw /home/openclaw/.openclaw/extensions \
    && rm -rf /root/.openclaw/extensions
```

- [ ] **Step 2: Update Dockerfile header comment**

Change line 5 from:
```dockerfile
# - OpenClaw gateway (openclaw) on port 18789
```
To:
```dockerfile
# - Base image for Firecracker VMs (no OpenClaw baked in — delivered via artifact)
```

- [ ] **Step 3: Commit**

```bash
git add rootfs/Dockerfile.openclaw
git commit -m "feat(rootfs): remove all plugin installation from Dockerfile (DR-201)

Rootfs is now a generic infrastructure image. Plugins (opik-openclaw,
composio) are bundled in the artifact by the build script instead.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Remove first-boot plugin seeding from init script

**Files:**
- Modify: `scripts/init-openclaw.sh:83-88`

- [ ] **Step 1: Remove the plugin seeding block**

In `scripts/init-openclaw.sh`, remove lines 83-88:

```bash
            # Seed extensions from rootfs if not already on data volume
            if [ -d /home/openclaw/.openclaw/extensions ] && [ ! -d /data/home/openclaw/.openclaw/extensions ]; then
                mkdir -p /data/home/openclaw/.openclaw
                cp -r /home/openclaw/.openclaw/extensions /data/home/openclaw/.openclaw/extensions
                chown -R openclaw:openclaw /data/home/openclaw/.openclaw/extensions
            fi
```

The rootfs no longer has plugins to seed. User plugins are installed by users via terminal.

- [ ] **Step 2: Commit**

```bash
git add scripts/init-openclaw.sh
git commit -m "fix(init): remove first-boot plugin seeding — rootfs has no plugins

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Add duplicate-ID check to every-boot init

**Files:**
- Modify: `scripts/init-openclaw.sh` (after line 343, before line 345)

- [ ] **Step 1: Add the duplicate-ID check**

After line 343 (`OCM_EFFECTIVE_PLUGINS_DIR="${OCM_BUNDLED_PLUGINS_DIR}"`), add the following block:

```bash
# Remove user-installed plugins that conflict with bundled plugins.
# Bundled plugins (in artifact) always win — user copies are deleted with a warning.
if [ -d /home/openclaw/.openclaw/extensions ] && [ -d "$OCM_EFFECTIVE_PLUGINS_DIR" ]; then
    for bundled_dir in "$OCM_EFFECTIVE_PLUGINS_DIR"/*/; do
        [ -d "$bundled_dir" ] || continue
        bundled_manifest="${bundled_dir}openclaw.plugin.json"
        [ -f "$bundled_manifest" ] || continue
        bundled_id=$(grep -o '"id"[[:space:]]*:[[:space:]]*"[^"]*"' "$bundled_manifest" | head -1 | sed 's/.*"id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/')
        [ -n "$bundled_id" ] || continue
        for user_dir in /home/openclaw/.openclaw/extensions/*/; do
            [ -d "$user_dir" ] || continue
            user_manifest="${user_dir}openclaw.plugin.json"
            [ -f "$user_manifest" ] || continue
            user_id=$(grep -o '"id"[[:space:]]*:[[:space:]]*"[^"]*"' "$user_manifest" | head -1 | sed 's/.*"id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/')
            if [ "$user_id" = "$bundled_id" ]; then
                echo "  [WARN] Removing user plugin '${user_id}' — conflicts with bundled plugin"
                rm -rf "$user_dir"
            fi
        done
    done
fi
```

- [ ] **Step 2: Commit**

```bash
git add scripts/init-openclaw.sh
git commit -m "feat(init): add duplicate-ID check — bundled plugins win over user plugins

On every boot, scan both bundled and user plugin directories. If a
user-installed plugin has the same ID as a bundled plugin, delete the
user copy and log a warning. Bundled plugins (from artifact) always win.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Remove plugin checks from test-rootfs.sh

**Files:**
- Modify: `scripts/test-rootfs.sh:67-86`

- [ ] **Step 1: Remove Opik and Composio plugin check blocks**

Remove lines 67-86 from `scripts/test-rootfs.sh`:

```bash
# --- Plugins: Opik ---
echo "Plugin: opik-openclaw"
check "opik-openclaw dir exists"              "test -d /home/openclaw/.openclaw/extensions/opik-openclaw"
check "opik-openclaw/index.ts exists"         "test -f /home/openclaw/.openclaw/extensions/opik-openclaw/index.ts"
check "opik-openclaw/openclaw.plugin.json"    "test -f /home/openclaw/.openclaw/extensions/opik-openclaw/openclaw.plugin.json"
check "opik-openclaw/node_modules exists"     "test -d /home/openclaw/.openclaw/extensions/opik-openclaw/node_modules"
check "opik-openclaw owned by openclaw"       "test \$(stat -c %U /home/openclaw/.openclaw/extensions/opik-openclaw) = openclaw"
check_output "opik-openclaw plugin id"        "cat /home/openclaw/.openclaw/extensions/opik-openclaw/openclaw.plugin.json" '"id"'
echo ""

# --- Plugins: Composio ---
echo "Plugin: composio"
check "composio dir exists"                   "test -d /home/openclaw/.openclaw/extensions/composio"
check "composio/index.ts exists"              "test -f /home/openclaw/.openclaw/extensions/composio/index.ts"
check "composio/openclaw.plugin.json"         "test -f /home/openclaw/.openclaw/extensions/composio/openclaw.plugin.json"
check "composio/src/config.ts exists"         "test -f /home/openclaw/.openclaw/extensions/composio/src/config.ts"
check "composio/skills dir exists"            "test -d /home/openclaw/.openclaw/extensions/composio/skills"
check "composio owned by openclaw"            "test \$(stat -c %U /home/openclaw/.openclaw/extensions/composio) = openclaw"
check_output "composio plugin id"             "cat /home/openclaw/.openclaw/extensions/composio/openclaw.plugin.json" '"id": "composio"'
echo ""
```

These plugins are no longer in the rootfs — they're bundled in the artifact.

- [ ] **Step 2: Commit**

```bash
git add scripts/test-rootfs.sh
git commit -m "fix(test): remove plugin checks from rootfs test — plugins are in artifact now

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Verify and update docs

**Files:**
- Modify: `docs/CurrentFeature.md`

- [ ] **Step 1: Run tests**

```bash
make typecheck
```

Expected: passes (no frontend changes).

- [ ] **Step 2: Verify no stale plugin references in scripts**

```bash
grep -rn 'openclaw/extensions' scripts/ rootfs/Dockerfile.openclaw
```

Expected: only the duplicate-ID check in init-openclaw.sh and the data volume symlink logic.

- [ ] **Step 3: Update CurrentFeature.md**

Update `docs/CurrentFeature.md` to add DR-201 completion:

```markdown
# Current Feature: Runtimedecouple

## Focus
Continue decoupled runtime implementation — Phases 3-5 from the implementation plan.

## Latest Status (2026-04-08)

### DR-201: Plugin Tier Separation (completed)
- [x] Moved Opik + Composio plugin installation from Dockerfile into artifact build script
- [x] Moved `composio-plugin.tgz` from `rootfs/` to `plugins/`
- [x] Removed all plugin installation from rootfs Dockerfile
- [x] Removed first-boot plugin seeding from init script
- [x] Added duplicate-ID check: bundled plugins win over user plugins at boot
- [x] Removed plugin checks from test-rootfs.sh
- [x] Rootfs is now fully plugin-free (generic infrastructure image)
- [x] Plugin path contract: bundled at `$OPENCLAW_BUNDLED_PLUGINS_DIR`, user at `~/.openclaw/extensions/`

### DR-106: Remove Baked OpenClaw from Rootfs (completed)
- [x] Removed `pnpm install -g openclaw` from rootfs Dockerfile
- [x] Removed de-hardlinking step (no longer needed without pnpm openclaw)
- [x] Removed unused bundled plugin cleanup block
- [x] Removed `openclaw doctor --fix` from plugin install (binary no longer on PATH)
- [x] Fixed build script: OPENCLAW_VERSION now required env var (no pnpm auto-detect)
- [x] Removed stale pnpm extension copy from build script
- [x] Removed `/etc/ocm-extensions-dir` fallback from init script
- [x] OCM_BUNDLED_PLUGINS_DIR is now required (fail-fast if missing)
- [x] Removed stale `openclaw` and `/etc/ocm-extensions-dir` checks from test-rootfs.sh
- [x] Updated Makefile `update-openclaw` target (version now controlled via env var)

### Remaining Work
- [ ] Phase 3: Placement compatibility gate (DR-301-303)
- [ ] Phase 4: PTY decoupling (DR-401-402)
- [ ] Phase 5: Supervisor/control-agent split (DR-501-502)
- [ ] Minor: Clean up stale test dirs (testdir/, testdir2/, src_test/, dst_test/)
- [ ] Minor: Fix runtime_test.go mockStore missing CreateArtifactRelease
- [ ] Minor: DR-006 beta-gating for rc/dev channels
- [ ] Minor: DR-902 audit events for version resolution
```

- [ ] **Step 4: Commit**

```bash
git add docs/CurrentFeature.md
git commit -m "docs: update CurrentFeature.md with DR-201 completion

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```
