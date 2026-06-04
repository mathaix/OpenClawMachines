# DR-106: Remove Baked OpenClaw from Rootfs — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the dead `pnpm install -g openclaw` and all dependent steps from the rootfs Dockerfile, and clean up the build/init scripts that referenced it.

**Architecture:** Three files change: Dockerfile loses ~40 lines of baked openclaw install + de-hardlinking + plugin cleanup; build script drops the stale pnpm extension copy and fixes version detection; init script drops the `/etc/ocm-extensions-dir` fallback. Rootfs will be ~250MB smaller. ext4 sizing is dynamic so it auto-adjusts.

**Tech Stack:** Docker, shell scripts, Make

---

### Task 1: Remove baked OpenClaw install from Dockerfile

**Files:**
- Modify: `rootfs/Dockerfile.openclaw:146-187`

- [ ] **Step 1: Remove the OPENCLAW_VERSION ARGs and pnpm install block**

In `rootfs/Dockerfile.openclaw`, remove lines 146-169. This is everything from the pnpm corepack setup through the `pnpm install -g openclaw`, de-hardlinking, and `/etc/ocm-extensions-dir` marker:

Replace:
```dockerfile
# Install pnpm via corepack
ENV PNPM_HOME="/root/.local/share/pnpm"
ENV PATH="${PNPM_HOME}:${PATH}"
RUN corepack enable && corepack prepare pnpm@${PNPM_VERSION} --activate

# Install OpenClaw gateway from npm (vanilla, no fork)
ARG OPENCLAW_VERSION=2026.4.2
ARG OPENCLAW_CONTROL_UI_VERSION=2026.4.2

# No symlink needed — openclaw's wrapper script uses $0 to resolve paths,
# so it must be found via PATH (PNPM_HOME) not a symlink.
RUN echo "Installing OpenClaw v${OPENCLAW_VERSION} from npm..." \
    && pnpm install -g openclaw@${OPENCLAW_VERSION} \
    && chmod -R o+rX /root /root/.local \
    # De-hardlink pnpm assets in place. OpenClaw 2026.2.26+ rejects files
    # with nlink>1 (hardlink security check in openBoundaryFileSync). pnpm stores
    # all files as hardlinks (nlink=2). Copy to temp, remove originals, move back
    # so nlink=1. Applies to: extensions/ (plugin discovery reads package.json
    # via openBoundaryFileSync).
    && EXT_DIR=$(find /root/.local/share/pnpm -path '*/openclaw/dist/extensions' -type d | head -1) \
    && cp -r --no-preserve=links "$EXT_DIR" /tmp/dehardlink-tmp \
    && rm -rf "$EXT_DIR" \
    && mv /tmp/dehardlink-tmp "$EXT_DIR" \
    && echo "$EXT_DIR" > /etc/ocm-extensions-dir
```

With:
```dockerfile
# Install pnpm via corepack (kept for user runtime — users may install npm packages)
ENV PNPM_HOME="/root/.local/share/pnpm"
ENV PATH="${PNPM_HOME}:${PATH}"
RUN corepack enable && corepack prepare pnpm@${PNPM_VERSION} --activate
```

- [ ] **Step 2: Remove the unused bundled plugin removal block**

Remove lines 171-187 (the block that removes device-pair, phone-control, talk-voice, browser plugins from the pnpm-installed openclaw extensions). This entire block operated on the pnpm-installed openclaw which no longer exists:

Remove:
```dockerfile
# Remove bundled plugins that are unused in OCM (device pairing, phone control,
# voice). These add ~28s to gateway startup from plugin registration overhead
# and may have missing dependencies (e.g. @buape/carbon in newer versions).
# Must run BEFORE plugin install — `openclaw plugins install` loads all existing
# plugins and will fail if any have missing native dependencies.
RUN EXT_DIR=$(cat /etc/ocm-extensions-dir) \
    && echo "Extensions before cleanup:" && ls "$EXT_DIR" \
    && for d in "$EXT_DIR"/*/; do \
         name=$(basename "$d"); \
         case "$name" in \
           *device-pair*|*phone-control*|*talk-voice*|*browser*) \
             echo "Removing unused plugin: $name"; \
             rm -rf "$d"; \
             ;; \
         esac; \
       done \
    && echo "Extensions after cleanup:" && ls "$EXT_DIR"
```

- [ ] **Step 3: Fix the Opik/Composio install block**

The plugin install block (lines 189-214) references `openclaw doctor --fix` which depends on the baked `openclaw` binary. Remove that line. Change:

```dockerfile
RUN apt-get update && apt-get install -y --no-install-recommends python3 \
    && openclaw doctor --fix 2>/dev/null || true \
    && cd /tmp \
```

To:
```dockerfile
RUN apt-get update && apt-get install -y --no-install-recommends python3 \
    && cd /tmp \
```

- [ ] **Step 4: Update the default CMD**

The `CMD` at line 260 references `openclaw` which won't be on PATH. Update to a no-op since Firecracker VMs use the init script, not the Docker CMD:

```dockerfile
# Default command (init script overrides this in Firecracker VMs)
CMD ["/bin/bash"]
```

- [ ] **Step 5: Update the Makefile `update-openclaw` target**

The `update-openclaw` target in `Makefile:536-540` edits the `ARG OPENCLAW_VERSION` line we just removed. Remove or update this target. Since the version is now passed as an env var to `build-openclaw-runtime.sh`, update the target to just echo usage guidance:

Replace (in `Makefile`):
```makefile
update-openclaw:
	@test -n "$(VERSION)" || { echo "Usage: make update-openclaw VERSION=2026.3.12"; exit 1; }
	@OLD=$$(grep -oP 'OPENCLAW_VERSION=\K[0-9.]+' rootfs/Dockerfile.openclaw); \
	sed -i "s/ARG OPENCLAW_VERSION=$$OLD/ARG OPENCLAW_VERSION=$(VERSION)/" rootfs/Dockerfile.openclaw; \
	echo "Updated OpenClaw: $$OLD → $(VERSION)"
```

With:
```makefile
update-openclaw:
	@test -n "$(VERSION)" || { echo "Usage: make update-openclaw VERSION=2026.3.12"; exit 1; }
	@echo "OpenClaw version is now controlled via: OPENCLAW_VERSION=$(VERSION) make build-openclaw"
```

- [ ] **Step 6: Commit**

```bash
git add rootfs/Dockerfile.openclaw Makefile
git commit -m "feat(rootfs): remove baked openclaw install from Dockerfile (DR-106)"
```

---

### Task 2: Fix build script — remove pnpm extension copy and fix version detection

**Files:**
- Modify: `scripts/build-openclaw-runtime.sh:44-61,131-147`

- [ ] **Step 1: Make OPENCLAW_VERSION a required env var**

The build script currently auto-detects the version by running `openclaw --version` via the pnpm-installed binary (line 56). After the Dockerfile change, that binary won't exist. Since the Dockerfile no longer has a default `ARG OPENCLAW_VERSION`, the caller must provide it.

Replace lines 44-61:
```bash
BUILD_ARGS=()
if [ -n "${OPENCLAW_VERSION:-}" ]; then
	BUILD_ARGS+=(--build-arg "OPENCLAW_VERSION=${OPENCLAW_VERSION}")
	BUILD_ARGS+=(--build-arg "OPENCLAW_CONTROL_UI_VERSION=${OPENCLAW_VERSION}")
fi

echo "=========================================="
echo "Building OpenClaw runtime image: ${IMAGE_NAME}"
echo "=========================================="
DOCKER_BUILDKIT=1 $DOCKER build "${BUILD_ARGS[@]}" -t "${IMAGE_NAME}" -f "${PROJECT_ROOT}/rootfs/Dockerfile.openclaw" "${PROJECT_ROOT}/rootfs"

# Detect version from the image
OPENCLAW_VERSION="$($DOCKER run --rm "${IMAGE_NAME}" /bin/sh -lc 'PATH=/root/.local/share/pnpm:$PATH openclaw --version 2>/dev/null | grep -oE "[0-9]+\.[0-9]+\.[0-9]+" | head -1')"
if [ -z "${OPENCLAW_VERSION}" ]; then
	echo "ERROR: failed to determine OpenClaw version from image"
	exit 1
fi
echo "OpenClaw version: ${OPENCLAW_VERSION}"
```

With:
```bash
if [ -z "${OPENCLAW_VERSION:-}" ]; then
	echo "ERROR: OPENCLAW_VERSION env var is required (e.g. OPENCLAW_VERSION=2026.4.2)"
	exit 1
fi

echo "=========================================="
echo "Building rootfs base image: ${IMAGE_NAME}"
echo "=========================================="
DOCKER_BUILDKIT=1 $DOCKER build -t "${IMAGE_NAME}" -f "${PROJECT_ROOT}/rootfs/Dockerfile.openclaw" "${PROJECT_ROOT}/rootfs"

echo "OpenClaw version: ${OPENCLAW_VERSION}"
```

- [ ] **Step 2: Remove the pnpm bundled extensions copy**

Remove lines 143-147 (the block that copies from `/etc/ocm-extensions-dir`):

Remove:
```bash
PNPM_EXT_DIR="$($DOCKER exec "${CONTAINER_ID}" cat /etc/ocm-extensions-dir 2>/dev/null || true)"
if [ -n "${PNPM_EXT_DIR}" ] && $DOCKER exec "${CONTAINER_ID}" test -d "${PNPM_EXT_DIR}" 2>/dev/null; then
	echo "Copying bundled extensions from pnpm build..."
	$DOCKER cp "${CONTAINER_ID}:${PNPM_EXT_DIR}/." "${EXT_DIR}"
fi
```

The npm flat install already produces `dist/extensions/` with bundled plugins. The user plugins (Opik, Composio) are still copied from `/home/openclaw/.openclaw/extensions/` (lines 138-141).

- [ ] **Step 3: Commit**

```bash
git add scripts/build-openclaw-runtime.sh
git commit -m "fix(build): require OPENCLAW_VERSION env var, remove stale pnpm extension copy"
```

---

### Task 3: Clean up init script — remove `/etc/ocm-extensions-dir` fallback

**Files:**
- Modify: `scripts/init-openclaw.sh:331,340`

- [ ] **Step 1: Remove the `/etc/ocm-extensions-dir` read and simplify plugins dir resolution**

Replace lines 331 and 340:

```bash
_default_plugins_dir=$(cat /etc/ocm-extensions-dir 2>/dev/null || true)
```

Remove this line entirely.

Then change line 340 from:
```bash
OCM_EFFECTIVE_PLUGINS_DIR="${OCM_BUNDLED_PLUGINS_DIR:-$_default_plugins_dir}"
```

To:
```bash
if [ -z "${OCM_BUNDLED_PLUGINS_DIR:-}" ]; then
    echo "[FATAL] OCM_BUNDLED_PLUGINS_DIR is not set — artifact metadata may be missing"
    exit 1
fi
OCM_EFFECTIVE_PLUGINS_DIR="${OCM_BUNDLED_PLUGINS_DIR}"
```

- [ ] **Step 2: Remove the seeded extension cleanup block**

Lines 342-347 remove seeded extensions from `~/.openclaw/extensions/`. Since the rootfs no longer seeds any extensions (the Dockerfile plugin install is only used by the artifact build, not at runtime), this block is dead code. Remove:

```bash
# Plugins are bundled in the artifact's dist/extensions/. Remove any
# seeded copies from ~/.openclaw/extensions/ to prevent duplicate plugin warnings.
if [ -d /home/openclaw/.openclaw/extensions ]; then
    echo "  Removing seeded user extensions (artifact bundles its own):"
    ls /home/openclaw/.openclaw/extensions/ 2>/dev/null || true
    rm -rf /home/openclaw/.openclaw/extensions
fi
```

- [ ] **Step 3: Commit**

```bash
git add scripts/init-openclaw.sh
git commit -m "fix(init): remove /etc/ocm-extensions-dir fallback, require OCM_BUNDLED_PLUGINS_DIR"
```

---

### Task 4: Verify and update CurrentFeature.md

**Files:**
- Modify: `docs/CurrentFeature.md`

- [ ] **Step 1: Run tests**

```bash
make test-go
```

Expected: all Go tests pass. The changes are shell/Docker only so Go tests should be unaffected.

- [ ] **Step 2: Run frontend typecheck**

```bash
make typecheck
```

Expected: passes — no frontend changes.

- [ ] **Step 3: Update CurrentFeature.md**

Update `docs/CurrentFeature.md` with the DR-106 work:

```markdown
# Current Feature: Runtimedecouple

## Focus
Continue decoupled runtime implementation — Phases 2-5 from the implementation plan.

## Latest Status (2026-04-08)

### DR-106: Remove Baked OpenClaw from Rootfs (completed)
- [x] Removed `pnpm install -g openclaw` from rootfs Dockerfile
- [x] Removed de-hardlinking step (no longer needed without pnpm openclaw)
- [x] Removed unused bundled plugin cleanup block
- [x] Removed `openclaw doctor --fix` from plugin install (binary no longer on PATH)
- [x] Fixed build script: OPENCLAW_VERSION now required env var (no pnpm auto-detect)
- [x] Removed stale pnpm extension copy from build script
- [x] Removed `/etc/ocm-extensions-dir` fallback from init script
- [x] OCM_BUNDLED_PLUGINS_DIR is now required (fail-fast if missing)
- [x] Rootfs image ~250MB smaller (baked openclaw payload removed)

### Remaining Work
- [ ] Phase 2: Plugin tier separation (DR-201)
- [ ] Phase 3: Placement compatibility gate (DR-301-303)
- [ ] Phase 4: PTY decoupling (DR-401-402)
- [ ] Phase 5: Supervisor/control-agent split (DR-501-502)
```

- [ ] **Step 4: Commit**

```bash
git add docs/CurrentFeature.md
git commit -m "docs: update CurrentFeature.md with DR-106 completion"
```
