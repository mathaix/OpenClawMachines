#!/bin/sh
# install-browser-harness — first-boot + rootfs-upgrade installer for the
# OCM browser skills stack. Installs two workspace-scoped skills so
# OpenClaw's skill loader picks them up:
#
#   <workspaceDir>/skills/browser-harness/   — editable copy of upstream
#                                               browser-harness (agent edits
#                                               helpers.py + domain-skills/)
#   <workspaceDir>/skills/ocm-browser/       — OCM companion skill
#                                               (read-only, refreshed on
#                                               every rootfs upgrade)
#
# The editable install also creates $HOME/.local/bin/browser-harness, which
# the `bh` wrapper prefers over the frozen /usr/local/bin fallback.
#
# Idempotent. Called on every boot from scripts/init-openclaw.sh.
#
# Upgrade handling: /opt/browser-harness/.bh-rootfs-sha contains the pinned
# SHA from the rootfs build. On mismatch vs. the on-disk copy, framework
# files are refreshed while agent-owned paths (helpers.py, domain-skills/)
# are preserved. The ocm-browser companion skill has no agent-owned files
# and is always synced fresh.

set -eu

BH_SRC="/opt/browser-harness"
OCM_SKILL_SRC="/opt/ocm-browser"
WORKSPACE_DIR="${WORKSPACE_DIR:-/home/openclaw/.openclaw/workspace}"
SKILLS_DIR="$WORKSPACE_DIR/skills"
BH_DEST="$SKILLS_DIR/browser-harness"
OCM_SKILL_DEST="$SKILLS_DIR/ocm-browser"
SHA_FILE=".bh-rootfs-sha"
LOG="/tmp/ocm-bh-install.log"

log()  { echo "install-bh: $*"; }
warn() { echo "install-bh: [WARN] $*" >&2; }

mkdir -p "$SKILLS_DIR"
chown openclaw:openclaw "$SKILLS_DIR" 2>/dev/null || true

# ---- ocm-browser companion skill --------------------------------------------
# No agent-editable content; always sync fresh from /opt so rootfs upgrades
# propagate immediately.
if [ -d "$OCM_SKILL_SRC" ]; then
    rsync -a --delete "$OCM_SKILL_SRC/" "$OCM_SKILL_DEST/"
    chown -R openclaw:openclaw "$OCM_SKILL_DEST"
    log "ocm-browser skill synced to $OCM_SKILL_DEST"
else
    warn "$OCM_SKILL_SRC missing from rootfs — ocm-browser skill unavailable"
fi

# ---- browser-harness editable install ---------------------------------------
if [ ! -d "$BH_SRC" ]; then
    warn "$BH_SRC missing from rootfs; skipping editable overlay (frozen /usr/local/bin/browser-harness still works)"
    exit 0
fi

src_sha=""
if [ -f "$BH_SRC/$SHA_FILE" ]; then
    src_sha=$(cat "$BH_SRC/$SHA_FILE")
else
    warn "$BH_SRC/$SHA_FILE missing — rootfs build bug; skipping upgrade check"
fi

dest_sha=""
if [ -d "$BH_DEST" ] && [ -f "$BH_DEST/$SHA_FILE" ]; then
    dest_sha=$(cat "$BH_DEST/$SHA_FILE")
fi

# Paths the agent may edit — preserve across upgrades.
PRESERVE_FILES="helpers.py"
PRESERVE_DIRS="domain-skills"

if [ ! -d "$BH_DEST" ]; then
    cp -a "$BH_SRC" "$BH_DEST"
    chown -R openclaw:openclaw "$BH_DEST"
    log "browser-harness seeded at $BH_DEST (sha=${src_sha:-unknown})"
elif [ -n "$src_sha" ] && [ "$src_sha" != "$dest_sha" ]; then
    log "browser-harness upgrade $BH_DEST (sha=${dest_sha:-unknown} → ${src_sha})"
    preserve=$(mktemp -d)
    # shellcheck disable=SC2064 # expand now, we want the captured value
    trap "rm -rf '$preserve'" EXIT INT TERM

    for f in $PRESERVE_FILES; do
        [ -f "$BH_DEST/$f" ] && cp -a "$BH_DEST/$f" "$preserve/$f"
    done
    for d in $PRESERVE_DIRS; do
        [ -d "$BH_DEST/$d" ] && cp -a "$BH_DEST/$d" "$preserve/$d"
    done

    # --checksum forces content-based comparison instead of rsync's default
    # size+mtime quick-check. Matters here because several files in the tree
    # are small and can end up with equal sizes across SHA bumps (e.g. the
    # 17-byte .bh-rootfs-sha stamp, a one-line comment swap in admin.py).
    # The tree is ~200 files — the extra hashing is negligible.
    rsync -a --delete --checksum "$BH_SRC/" "$BH_DEST/"

    # Belt-and-suspenders: force-copy the SHA stamp even though --checksum
    # should handle it. If the next boot's comparison reads a stale stamp,
    # upgrades silently no-op — worth the one redundant cp.
    if [ -f "$BH_SRC/$SHA_FILE" ]; then
        cp -f "$BH_SRC/$SHA_FILE" "$BH_DEST/$SHA_FILE"
    fi

    for f in $PRESERVE_FILES; do
        [ -f "$preserve/$f" ] && cp -a "$preserve/$f" "$BH_DEST/$f"
    done
    for d in $PRESERVE_DIRS; do
        if [ -d "$preserve/$d" ]; then
            rm -rf "$BH_DEST/$d"
            cp -a "$preserve/$d" "$BH_DEST/$d"
        fi
    done

    chown -R openclaw:openclaw "$BH_DEST"
fi

# Editable install (or re-install if the user-scope tool dir was pruned).
# Runs as openclaw so UV_TOOL_{DIR,BIN_DIR} resolve under the data volume.
# Installing from the workspace skill dir means the agent can edit helpers.py
# right next to SKILL.md and the changes are live for the next `bh` call.
if ! su -l -s /bin/bash openclaw -c "
    set -eu
    export UV_TOOL_DIR=\"\$HOME/.local/share/uv/tools\"
    export UV_TOOL_BIN_DIR=\"\$HOME/.local/bin\"
    cd '$BH_DEST'
    uv tool install -e . --reinstall
" >"$LOG" 2>&1; then
    warn "editable install failed; see $LOG"
    warn "bh will fall back to the frozen /usr/local/bin/browser-harness"
    exit 0
fi

log "browser-harness editable install active (\$HOME/.local/bin/browser-harness → $BH_DEST)"
