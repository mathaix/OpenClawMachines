#!/bin/bash
# OpenClaw Machines MicroVM Init Script
# Injected as /sbin/overlay-init in the rootfs image.
#
# This is PID 1 inside every Firecracker MicroVM.
# It mounts filesystems, seeds entropy, configures networking,
# fetches config from the metadata service, and starts the OpenClaw gateway.

exec 2>&1

# Mount /proc early and create /dev/fd symlink so process substitution
# (>(tee ...)) works — it needs /dev/fd/ which points to /proc/self/fd.
mount -t proc proc /proc 2>/dev/null
[ ! -e /dev/fd ] && ln -sf /proc/self/fd /dev/fd

# Persist boot log for debugging
exec > >(tee -a /var/log/openclaw-init.log) 2>&1

echo "=============================================="
echo "OpenClaw MicroVM starting..."
echo "=============================================="
echo "Init script PID: $$"
echo "Date: $(date)"

# Timing helpers
phase_start() { PHASE_START=$(date +%s%N); echo "=== PHASE: $1 ==="; }
phase_end() {
    local elapsed=$(( ($(date +%s%N) - PHASE_START) / 1000000 ))
    echo "  [DONE] ${1} (${elapsed}ms)"
}

# Structured logging helper
log() {
    local level="$1"; shift
    echo "$(date -Iseconds) [$level] $*"
}

# ============================================
# 1. Mount essential filesystems
# ============================================
echo ""
phase_start "mount"
mount -t proc none /proc && echo "  [OK] /proc mounted" || echo "  [SKIP] /proc"
mount -t sysfs none /sys && echo "  [OK] /sys mounted" || echo "  [SKIP] /sys"
mount -t devtmpfs none /dev && echo "  [OK] /dev mounted" || echo "  [SKIP] /dev"
# Create standard /dev/std* symlinks (no udev in Firecracker)
ln -sf /proc/self/fd/0 /dev/stdin 2>/dev/null
ln -sf /proc/self/fd/1 /dev/stdout 2>/dev/null
ln -sf /proc/self/fd/2 /dev/stderr 2>/dev/null
mkdir -p /dev/pts
mount -t devpts devpts /dev/pts && echo "  [OK] /dev/pts mounted" || echo "  [SKIP] /dev/pts"
mount -t tmpfs tmpfs /run && echo "  [OK] /run mounted" || echo "  [SKIP] /run"
mount -t tmpfs tmpfs /tmp && echo "  [OK] /tmp mounted" || echo "  [SKIP] /tmp"
phase_end "mount"

# ============================================
# 1b. Mount persistent data volume
# ============================================
echo ""
phase_start "data-volume"
if [ -b /dev/vdb ]; then
    if ! blkid /dev/vdb >/dev/null 2>&1; then
        echo "  Formatting data volume (ext4)..."
        mkfs.ext4 -m 1 -q /dev/vdb
    else
        echo "  Running filesystem check..."
        e2fsck -p /dev/vdb 2>/dev/null || true
    fi
    mkdir -p /data
    if ! mount -o nodev,nosuid /dev/vdb /data; then
        echo "  [FATAL] Failed to mount /dev/vdb — running without persistence"
    fi
    if mountpoint -q /data; then
        mkdir -p /data/home/openclaw /data/workspace /data/ocm/configs
        # V8 bytecode cache for faster gateway restarts on the same VM
        # (per `openclaw doctor` recommendation). First boot still pays full
        # cost — cache is populated on first compilation. Path is on the
        # persisted data volume so it survives VM reboots.
        mkdir -p /data/openclaw-compile-cache
        chown openclaw:openclaw /data/home/openclaw /data/workspace /data/openclaw-compile-cache
        # Seed default dotfiles from rootfs into data volume (first boot only).
        # On subsequent boots these already exist on the data volume and are preserved.
        if [ -d /home/openclaw ] && [ ! -L /home/openclaw ]; then
            for f in /home/openclaw/.bashrc /home/openclaw/.profile /home/openclaw/.bash_logout; do
                target="/data/home/openclaw/$(basename "$f")"
                if [ -f "$f" ] && [ ! -f "$target" ]; then
                    cp "$f" "$target" && chown openclaw:openclaw "$target"
                fi
            done
        fi
        # Bind-mount persistent dirs so realpath() returns /home/openclaw/...
        # consistently (not /data/home/openclaw/...). The gateway's
        # assertNoPathAliasEscape rejects writes when a symlink causes the
        # canonical path to differ from the lexical path.
        rm -rf /home/openclaw
        mkdir -p /home/openclaw
        if ! mount --bind /data/home/openclaw /home/openclaw; then
            echo "  [FATAL] Failed to bind-mount /data/home/openclaw → /home/openclaw"
            exit 1
        fi
        # Verify bind mount took effect
        if ! grep -q '/home/openclaw' /proc/mounts; then
            echo "  [FATAL] bind-mount /home/openclaw not visible in /proc/mounts"
            exit 1
        fi
        rm -rf /workspace
        mkdir -p /workspace
        if ! mount --bind /data/workspace /workspace; then
            echo "  [FATAL] Failed to bind-mount /data/workspace → /workspace"
            exit 1
        fi
        echo "  [OK] Data volume mounted at /data"
    else
        echo "  [WARN] /data not mounted — persistence disabled for this boot"
    fi
else
    echo "  [WARN] No data volume detected, running without persistence"
fi
phase_end "data-volume"

# ============================================
# 1b2. Enable swap (1 GB)
# Prefer /data/swapfile (persistent, avoids 1GB dd on rootfs overlay every boot).
# Fall back to /swapfile on the overlay only when /data is not mounted.
# ============================================
echo ""
phase_start "swap"
SWAP_SIZE_MB=1024
if mountpoint -q /data 2>/dev/null; then
    SWAPFILE="/data/swapfile"
else
    SWAPFILE="/swapfile"
fi
if [ ! -f "$SWAPFILE" ]; then
    dd if=/dev/zero of="$SWAPFILE" bs=1M count=$SWAP_SIZE_MB status=none 2>/dev/null
    chmod 600 "$SWAPFILE"
    mkswap "$SWAPFILE" >/dev/null 2>&1
    echo "  [OK] Created ${SWAP_SIZE_MB}MB swap file at $SWAPFILE"
fi
if ! swapon -s | grep -q "$SWAPFILE"; then
    swapon "$SWAPFILE" 2>/dev/null && echo "  [OK] Swap enabled (${SWAP_SIZE_MB}MB at $SWAPFILE)" || echo "  [WARN] Failed to enable swap"
else
    echo "  [OK] Swap already active"
fi
phase_end "swap"

# ============================================
# 1c. Data volume versioning & migrations
# ============================================
echo ""
phase_start "data-version"
if mountpoint -q /data 2>/dev/null; then
    ROOTFS_DATA_VERSION=2
    DATA_VERSION_FILE="/data/.ocm-version"
    if [ -f "$DATA_VERSION_FILE" ]; then
        CURRENT=$(cat "$DATA_VERSION_FILE")
    else
        CURRENT=0
    fi
    echo "  Data version: current=$CURRENT, target=$ROOTFS_DATA_VERSION"
    if [ "$CURRENT" -lt "$ROOTFS_DATA_VERSION" ]; then
        echo "  Running migrations from v${CURRENT} to v${ROOTFS_DATA_VERSION}..."
        if [ -x /usr/local/bin/ocm-migrate ]; then
            /usr/local/bin/ocm-migrate "$CURRENT" "$ROOTFS_DATA_VERSION"
        fi
        # v1→v2: create persistent user install directories for npm globals
        if [ "$CURRENT" -lt 2 ]; then
            echo "  [migrate v2] Creating ~/.local/{bin,lib} for persistent npm installs"
            mkdir -p /data/home/openclaw/.local/bin /data/home/openclaw/.local/lib
            chown -R openclaw:openclaw /data/home/openclaw/.local
        fi
        echo "$ROOTFS_DATA_VERSION" > "$DATA_VERSION_FILE"
        echo "  [OK] Data volume upgraded to v${ROOTFS_DATA_VERSION}"
    else
        echo "  [OK] Data volume is up to date"
    fi

    # Write OpenClaw version from rootfs manifest for informational tracking
    if [ -f /etc/ocm-versions.json ]; then
        OC_VER=$(jq -r '.openclaw // "unknown"' /etc/ocm-versions.json 2>/dev/null || echo "unknown")
        echo "$OC_VER" > /data/.openclaw_version
        echo "  OpenClaw version: $OC_VER"
    fi
fi
phase_end "data-version"

# ============================================
# 1d. User install locations (npm globals persist on data volume)
# ============================================
if mountpoint -q /data 2>/dev/null; then
    # Scope to the openclaw user only; gateway runs under this user via su below.
    mkdir -p /data/home/openclaw/.local/bin /data/home/openclaw/.local/lib /data/home/openclaw/.local/share/pnpm \
             /data/home/openclaw/.local/python
    cat >/etc/profile.d/ocm-user-installs.sh <<'EOF'
# Persist npm / pnpm / pip / uv / bun installs under the user's home on the data
# volume. All paths under $HOME/.local are bind-mounted from /data so writes
# survive VM restart.
export NPM_CONFIG_PREFIX="$HOME/.local"
export PNPM_HOME="$HOME/.local/share/pnpm"
# Where `pip install --user <pkg>` and `pip install <pkg>` (outside a venv,
# when PEP 668 externally-managed or --user is requested) land. Intentionally
# NOT setting PIP_USER=1 globally — that would break `pip install` inside
# activated virtualenvs. Users who want persistent system-level Python tools
# should use `uv tool install` or `pip install --user`.
export PYTHONUSERBASE="$HOME/.local"
# uv python install ... caches CPython downloads under the persistent home too
export UV_PYTHON_INSTALL_DIR="$HOME/.local/python"
# Redirect Bun's global installs from its default ~/.bun to ~/.local so bun
# shares the same bin dir as npm/pnpm/uv — one PATH entry instead of four.
export BUN_INSTALL="$HOME/.local"
export PATH="$HOME/.local/bin:$PNPM_HOME:$PATH"
EOF
    chown -R openclaw:openclaw /data/home/openclaw/.local
fi

# ============================================
# 2. Seed entropy (critical for Firecracker)
# ============================================
echo ""
echo "=== Seeding entropy pool (RNDADDENTROPY) ==="
ENTROPY_BEFORE=$(cat /proc/sys/kernel/random/entropy_avail 2>/dev/null || echo "0")
echo "  Entropy before: $ENTROPY_BEFORE bits"

/usr/local/bin/seed-entropy || echo "  [WARN] seed-entropy failed, services may block on entropy"

ENTROPY_AFTER=$(cat /proc/sys/kernel/random/entropy_avail 2>/dev/null || echo "0")
echo "  Entropy after: $ENTROPY_AFTER bits"

# ============================================
# 3. Hostname
# ============================================
hostname openclaw 2>/dev/null || true
echo ""
echo "=== Hostname: $(hostname) ==="

# ============================================
# 4. Network configuration
# ============================================
echo ""
phase_start "network"
CMDLINE=$(cat /proc/cmdline 2>/dev/null || echo "")

if echo "$CMDLINE" | grep -q "ip="; then
    IP_CONFIG=$(echo "$CMDLINE" | grep -oE 'ip=[^ ]+' | cut -d= -f2)
    CLIENT_IP=$(echo "$IP_CONFIG" | cut -d: -f1)
    GATEWAY_IP=$(echo "$IP_CONFIG" | cut -d: -f3)

    echo "  Parsed: IP=$CLIENT_IP, Gateway=$GATEWAY_IP"

    ip link set lo up
    ip link set eth0 up

    if ip addr show eth0 | grep -q "$CLIENT_IP"; then
        echo "  [SKIP] IP $CLIENT_IP already assigned"
    else
        ip addr add "${CLIENT_IP}/24" dev eth0
    fi

    if ip route | grep -q "default"; then
        echo "  [SKIP] Default route already exists"
    else
        ip route add default via "$GATEWAY_IP"
    fi
fi
phase_end "network"

# ============================================
# 5. DNS
# ============================================
echo ""
echo "=== DNS Configuration ==="
echo "nameserver 8.8.8.8" > /etc/resolv.conf 2>/dev/null || true
echo "nameserver 8.8.4.4" >> /etc/resolv.conf 2>/dev/null || true
echo "127.0.0.1 localhost openclaw" >> /etc/hosts 2>/dev/null || true

# ============================================
# 6. Read metadata nonce from kernel cmdline
# ============================================
METADATA_NONCE=""
if echo "$CMDLINE" | grep -qE 'metadata_nonce=[^ ]+'; then
    METADATA_NONCE=$(echo "$CMDLINE" | grep -oE 'metadata_nonce=[^ ]+' | cut -d= -f2)
    echo "  Metadata nonce: ${METADATA_NONCE:0:8}..."
fi
NONCE_ARGS=()
if [ -n "$METADATA_NONCE" ]; then
    NONCE_ARGS=(-H "X-Metadata-Nonce:${METADATA_NONCE}")
    # Persist nonce so /etc/profile.d/ scripts can authenticate with metadata
    echo "$METADATA_NONCE" > /run/ocm-nonce
    chmod 444 /run/ocm-nonce
fi

# Persist gateway IP so profile.d scripts can reach the metadata service
echo "${GATEWAY_IP:-192.168.100.1}" > /run/ocm-gateway-ip
chmod 444 /run/ocm-gateway-ip

# Quick Start mode: skip heavyweight gateway sidecars (channels, browser, cron,
# Bonjour discovery, Gmail watcher) to cut boot-to-ready from ~60s to ~15-30s.
# Pass ocm_quick_start=1 on the kernel cmdline to enable.
OCM_QUICK_START=""
if echo "$CMDLINE" | grep -q "ocm_quick_start=1"; then
    OCM_QUICK_START=1
    echo "  [OK] Quick Start mode enabled — skipping heavyweight sidecars"
fi

# ============================================
# 7. Wait for metadata service
# ============================================
METADATA_URL="http://${GATEWAY_IP:-192.168.100.1}"

echo ""
phase_start "metadata"
for _ in $(seq 1 30); do
    if curl -sf "$METADATA_URL/health" > /dev/null 2>&1; then
        echo "  [OK] Metadata service available"
        break
    fi
    sleep 0.2
done

# ============================================
# 8. Fetch configuration
# ============================================
echo ""
echo "=== Fetching Configuration ==="
# Fetch machine infrastructure config (identity, tokens, tunnel) from dedicated endpoint.
# This is separate from /v1/config (which serves OpenClawConf for the gateway).
MACHINE_CONFIG=$(curl -sf "${NONCE_ARGS[@]}" "$METADATA_URL/v1/machine" 2>&1)
CURL_RC=$?
if [ $CURL_RC -ne 0 ] || [ -z "$MACHINE_CONFIG" ]; then
    echo "[FATAL] Failed to fetch machine config from $METADATA_URL/v1/machine (curl exit=$CURL_RC)"
    echo "[FATAL] MACHINE_CONFIG keys: $(echo "$MACHINE_CONFIG" | jq -r 'keys | join(", ")' 2>/dev/null || echo '<parse failed>')"
    exit 1
fi
# Extract all fields in a single jq call.
eval "$(echo "$MACHINE_CONFIG" | jq -r '@sh "MACHINE_ID=\(.machine_id // "") GATEWAY_TOKEN=\(.gateway_token // "") MACHINE_SLUG=\(.machine_slug // "") VM_HOSTNAME=\(.vm_hostname // "") OCM_RUNTIME_SOURCE=\(.runtime_source // "artifact") OCM_RESOLVED_ROOTFS_VERSION=\(.resolved_rootfs_version // "") OCM_RESOLVED_OPENCLAW_VERSION=\(.resolved_openclaw_version // "") OCM_VERSION_SOURCE=\(.version_source // "") OPENCLAW_BIN=\(.openclaw_bin // "") OCM_BUNDLED_PLUGINS_DIR=\(.openclaw_bundled_plugins_dir // "") SIGNING_KEY=\(.signing_key // "") TUNNEL_TOKEN=\(.tunnel_token // "")"')"
if [ -z "$MACHINE_ID" ]; then
    echo "[FATAL] machine_id is empty — metadata server returned incomplete config"
    echo "[FATAL] MACHINE_CONFIG keys: $(echo "$MACHINE_CONFIG" | jq -r 'keys | join(", ")' 2>/dev/null || echo '<parse failed>')"
    exit 1
fi
if [ -z "$GATEWAY_TOKEN" ]; then
    echo "[FATAL] gateway_token is empty — metadata server returned incomplete config"
    echo "[FATAL] MACHINE_CONFIG keys: $(echo "$MACHINE_CONFIG" | jq -r 'keys | join(", ")' 2>/dev/null || echo '<parse failed>')"
    exit 1
fi

# Removed: curl /v1/providers — provider config now on disk from data volume pre-boot write

echo "  Machine ID: ${MACHINE_ID:-<not set>}"
echo "  Runtime source: ${OCM_RUNTIME_SOURCE:-artifact}"
if [ -n "$OCM_RESOLVED_OPENCLAW_VERSION" ]; then
    echo "  Resolved OpenClaw: $OCM_RESOLVED_OPENCLAW_VERSION"
fi
phase_end "metadata"

# True when the resolved OpenClaw runtime (≥2026.6) stores auth profiles in
# openclaw-agent.sqlite instead of auth-profiles.json. Accepts "2026.6.5",
# "v2026.6.5-r1", "2026.6.5-beta.2". Unknown/unparseable versions return
# false (legacy JSON behavior, matching the deployed fleet).
ocm_sqlite_auth_runtime() {
    local v="${OCM_RESOLVED_OPENCLAW_VERSION#v}"
    v="${v%%-*}"
    local year="${v%%.*}"
    local rest="${v#*.}"
    local month="${rest%%.*}"
    case "$year" in ''|*[!0-9]*) return 1 ;; esac
    case "$month" in ''|*[!0-9]*) return 1 ;; esac
    [ "$year" -gt 2026 ] && return 0
    [ "$year" -eq 2026 ] && [ "$month" -ge 6 ] && return 0
    return 1
}

# Mount the openclaw runtime drive if present (/dev/vdc = third virtio drive).
# This is a read-only ext4 image containing the artifact runtime, passed by the
# orchestrator instead of copying 2GB into the data volume.
if [ -b /dev/vdc ]; then
    mkdir -p /ocm-runtime
    if mount -o ro /dev/vdc /ocm-runtime 2>/dev/null; then
        echo "  [OK] OpenClaw runtime drive mounted at /ocm-runtime"
    else
        echo "  [WARN] Failed to mount openclaw runtime drive /dev/vdc"
    fi
fi

# Resolve the effective runtime binary and plugins dir once, then persist to
# /run/ocm-runtime-env so all contexts (gateway, PTY server, login shells)
# use the same runtime. Artifact is the only supported runtime path.
if [ -z "$OPENCLAW_BIN" ] || [ ! -x "$OPENCLAW_BIN" ]; then
    echo "[FATAL] Artifact runtime binary is unavailable"
    echo "[FATAL]   OPENCLAW_BIN=$OPENCLAW_BIN"
    echo "[FATAL]   OCM_RUNTIME_SOURCE=$OCM_RUNTIME_SOURCE"
    exit 1
fi
if [ -z "${OCM_BUNDLED_PLUGINS_DIR:-}" ] || [ ! -d "${OCM_BUNDLED_PLUGINS_DIR}" ]; then
    echo "[FATAL] OCM_BUNDLED_PLUGINS_DIR is missing or does not exist: ${OCM_BUNDLED_PLUGINS_DIR:-<unset>}"
    exit 1
fi
OCM_EFFECTIVE_RUNTIME="artifact"
OCM_EFFECTIVE_BIN="$OPENCLAW_BIN"
OCM_EFFECTIVE_PLUGINS_DIR="${OCM_BUNDLED_PLUGINS_DIR}"

# Remove user-installed plugins that conflict with bundled plugins.
# Bundled plugins (in artifact) always win — user copies are deleted with a warning.
# Build a set of bundled IDs once with jq, then single-pass scan user plugins.
# Safety: skip symlinks and verify resolved path stays under the extensions root.
USER_EXT_ROOT="/home/openclaw/.openclaw/extensions"
if [ -d "$USER_EXT_ROOT" ] && [ -d "$OCM_EFFECTIVE_PLUGINS_DIR" ]; then
    # Collect all bundled plugin IDs in one pass
    BUNDLED_IDS=""
    for bundled_manifest in "$OCM_EFFECTIVE_PLUGINS_DIR"/*/openclaw.plugin.json; do
        [ -f "$bundled_manifest" ] || continue
        bid=$(jq -r '.id // empty' "$bundled_manifest" 2>/dev/null)
        [ -n "$bid" ] && BUNDLED_IDS="${BUNDLED_IDS}${bid}\n"
    done

    if [ -n "$BUNDLED_IDS" ]; then
        resolved_ext_root=$(readlink -f "$USER_EXT_ROOT")
        for user_dir in "$USER_EXT_ROOT"/*/; do
            user_path="${user_dir%/}"
            [ -d "$user_path" ] || continue
            # Skip symlinks — only delete real directories
            [ -L "$user_path" ] && continue
            # Verify resolved path is inside the extensions root
            resolved_path=$(readlink -f "$user_path")
            case "$resolved_path" in
                "$resolved_ext_root"/*) ;;
                *) echo "  [WARN] Skipping plugin outside extensions root: $user_path"; continue ;;
            esac
            user_manifest="${user_path}/openclaw.plugin.json"
            [ -f "$user_manifest" ] || continue
            user_id=$(jq -r '.id // empty' "$user_manifest" 2>/dev/null)
            [ -n "$user_id" ] || continue
            if printf '%b' "$BUNDLED_IDS" | grep -qxF "$user_id"; then
                echo "  [WARN] Removing user plugin '${user_id}' — conflicts with bundled plugin"
                rm -rf -- "$user_path"
            fi
        done
    fi
fi

# Write resolved runtime env for PTY server, login shells, and gateway
cat > /run/ocm-runtime-env <<RUNTIMEEOF
OCM_EFFECTIVE_RUNTIME='${OCM_EFFECTIVE_RUNTIME}'
OCM_EFFECTIVE_BIN='${OCM_EFFECTIVE_BIN}'
OCM_EFFECTIVE_PLUGINS_DIR='${OCM_EFFECTIVE_PLUGINS_DIR}'
RUNTIMEEOF
chmod 644 /run/ocm-runtime-env

# Prepend artifact bin dir to PATH if using artifact runtime
OCM_RUNTIME_PATH_PREFIX=""
if [ "$OCM_EFFECTIVE_RUNTIME" = "artifact" ] && [ -n "$OCM_EFFECTIVE_BIN" ]; then
    _artifact_bin_dir=$(dirname "$OCM_EFFECTIVE_BIN")
    if [ "$_artifact_bin_dir" != "." ] && [ -d "$_artifact_bin_dir" ]; then
        OCM_RUNTIME_PATH_PREFIX="${_artifact_bin_dir}:"
    fi
fi
echo "  Effective runtime: $OCM_EFFECTIVE_RUNTIME (bin=$OCM_EFFECTIVE_BIN)"

# Removed: section 9 provider logging — providers now on disk, no metadata fetch needed

# ============================================
# 10. Write OpenClaw gateway config (versioned, atomic)
# ============================================
OPENCLAW_DIR="/home/openclaw/.openclaw"
mkdir -p "$OPENCLAW_DIR"
mkdir -p "$OPENCLAW_DIR/workspace"
mkdir -p "$OPENCLAW_DIR/agents/main/agent" "$OPENCLAW_DIR/agents/main/sessions"
mkdir -p "$OPENCLAW_DIR/identity"
# Tighten state-dir perms to 700 (per `openclaw doctor` recommendation).
# Otherwise sshd/openssh-strict checks and any group/world reader can see
# the gateway state directory; openclaw warns about this on every fresh VM.
chmod 700 "$OPENCLAW_DIR"

echo "  Config source: data volume pre-boot write"

# --- Timestamped config directory ---
# Each config version creates a new dir under /data/ocm/configs/<ts>/.
# A stable symlink (config-current) points to the active version.
# User edits stay in the timestamped dir; new boots copy forward missing files.
CONFIG_BASE="/data/ocm/configs"
CONFIG_LINK="$OPENCLAW_DIR/config-current"
# Bump when config format/defaults change in the rootfs
ROOTFS_CONFIG_VERSION=1

# ----------------------------------------------------------------------------
# Helpers for merging machine-specific content into openclaw's workspace files.
#
# Openclaw ships IDENTITY.md and TOOLS.md templates inside its npm package at
# <openclaw_root>/docs/reference/templates/. At the first agent session openclaw
# seeds them into /workspace via fs.writeFile(...wx) — "create only if absent."
# We want the scaffolding openclaw provides AND a machine-specific section
# (slug/hostname/port-URL for IDENTITY.md; rootfs tool inventory for TOOLS.md).
# Strategy: pre-seed the files ourselves with openclaw's template content + a
# fenced block. Openclaw's later writeFileIfMissing is then a no-op, and our
# fenced block can be idempotently refreshed on each boot.
# ----------------------------------------------------------------------------

# Echo the on-disk path of an openclaw workspace template (IDENTITY.md /
# TOOLS.md / ...). Empty output + non-zero exit means not found.
#
# Real artifact layout (verified against an extracted openclaw-vX.Y.Z.tar.zst):
#   <artifact>/bin/openclaw                                  ← OCM_EFFECTIVE_BIN
#   <artifact>/node_modules/openclaw/docs/reference/templates/IDENTITY.md
#   <artifact>/node_modules/openclaw/docs/reference/templates/TOOLS.md
# Earlier artifact schemes may have hoisted docs/ to the artifact root, so we
# try that as a fallback before giving up.
locate_openclaw_template() {
    _name="$1"
    _resolved_bin=$(readlink -f "${OCM_EFFECTIVE_BIN:-}" 2>/dev/null || echo "${OCM_EFFECTIVE_BIN:-}")
    _bin_dir=$(dirname "$_resolved_bin" 2>/dev/null || true)
    for _c in \
        "${_bin_dir}/../node_modules/openclaw/docs/reference/templates/$_name" \
        "${_bin_dir}/../docs/reference/templates/$_name" \
        "${_bin_dir}/docs/reference/templates/$_name" \
        "${OCM_EFFECTIVE_PLUGINS_DIR:-}/../../node_modules/openclaw/docs/reference/templates/$_name" \
        "${OCM_EFFECTIVE_PLUGINS_DIR:-}/../docs/reference/templates/$_name" \
        "/usr/local/share/openclaw/templates/$_name"; do
        if [ -n "$_c" ] && [ -f "$_c" ]; then
            readlink -f "$_c" 2>/dev/null || echo "$_c"
            return 0
        fi
    done
    return 1
}

# Strip leading YAML frontmatter from stdin. Mirrors openclaw's stripFrontMatter:
# if content starts with '---\n', drop through the next '---\n' line and any
# blank lines immediately after.
strip_front_matter() {
    awk '
        BEGIN { in_fm = 0; saw_end = 0 }
        NR == 1 && /^---$/ { in_fm = 1; next }
        in_fm && /^---$/   { in_fm = 0; saw_end = 1; next }
        in_fm              { next }
        saw_end && NF == 0 { next }
        { saw_end = 0; print }
    '
}

# Ensure $1 exists as a real regular file carrying openclaw's template content
# (frontmatter stripped). Handles three legacy cases:
#   a) $1 is a symlink (from earlier rootfs): dereference + promote to real file.
#   b) $1 does not exist: seed from openclaw's template.
#   c) $1 exists but lacks the openclaw scaffolding (heuristic: first lines
#      don't contain $3, the expected heading marker): prepend template content.
# Leaves $1 owned by openclaw:openclaw.
seed_workspace_file() {
    _file="$1"
    _template_name="$2"
    _heading="$3"

    if [ -L "$_file" ]; then
        _target=$(readlink -f "$_file" 2>/dev/null || true)
        rm -f "$_file"
        if [ -n "$_target" ] && [ -f "$_target" ]; then
            cp "$_target" "$_file"
        fi
    fi

    if [ ! -f "$_file" ]; then
        _tpath=$(locate_openclaw_template "$_template_name" || true)
        if [ -n "$_tpath" ]; then
            strip_front_matter < "$_tpath" > "$_file"
        else
            echo "  [WARN] openclaw template $_template_name not found; writing minimal placeholder"
            printf '%s\n\n' "$_heading" > "$_file"
        fi
    elif ! head -5 "$_file" 2>/dev/null | grep -Fq "$_heading" \
         && ! grep -Fq "<!-- BEGIN ocm-" "$_file" 2>/dev/null; then
        # Legacy-content repair: file has neither the openclaw heading nor any
        # of our fence markers — likely from the c2d9ccf-era rootfs that wrote
        # ocm content to ~/.openclaw/ without openclaw scaffolding. Once we've
        # added a fence on a later boot, the fence check below prevents a
        # re-prepend even if a tenant removes or edits the template heading.
        _tpath=$(locate_openclaw_template "$_template_name" || true)
        if [ -n "$_tpath" ]; then
            _tmp=$(mktemp)
            strip_front_matter < "$_tpath" > "$_tmp"
            printf '\n' >> "$_tmp"
            cat "$_file" >> "$_tmp"
            mv "$_tmp" "$_file"
        fi
    fi

    chown openclaw:openclaw "$_file" 2>/dev/null || true
}

# Replace (or append) a fenced block inside $1 named "ocm-$2". Block content
# comes from stdin. Content outside the fence is untouched.
# Fence format:
#   <!-- BEGIN ocm-<name> -->
#   ...
#   <!-- END ocm-<name> -->
merge_fenced_block() {
    _file="$1"
    _name="$2"
    _body=$(cat)

    _begin="<!-- BEGIN ocm-${_name} -->"
    _end="<!-- END ocm-${_name} -->"

    _tmp=$(mktemp)
    # Strip any pre-existing block + the (optional) blank line immediately before
    # and after, so repeated regeneration doesn't accumulate blank lines.
    awk -v B="$_begin" -v E="$_end" '
        BEGIN { in_block = 0; pending_blank = 0 }
        $0 == B {
            in_block = 1
            if (buf_len > 0 && buf[buf_len-1] == "") { buf_len-- }
            next
        }
        in_block {
            if ($0 == E) { in_block = 0; pending_blank = 1 }
            next
        }
        pending_blank && NF == 0 { pending_blank = 0; next }
        { pending_blank = 0; buf[buf_len++] = $0 }
        END { for (i = 0; i < buf_len; i++) print buf[i] }
    ' "$_file" > "$_tmp"

    # Append a blank line (if needed) + fresh fenced block.
    {
        cat "$_tmp"
        _last=$(tail -c 1 "$_tmp" 2>/dev/null | od -An -c | tr -d ' ' || echo "")
        # Ensure separation from prior content.
        printf '\n'
        printf '%s\n' "$_begin"
        printf '%s\n' "$_body"
        printf '%s\n' "$_end"
    } > "$_file"
    rm -f "$_tmp"
    chown openclaw:openclaw "$_file" 2>/dev/null || true
}

# Emit the machine-identity block body on stdout. Requires VM_HOSTNAME + MACHINE_SLUG.
emit_machine_identity_block() {
    cat <<IDBLK
## Machine

- **Slug:** ${MACHINE_SLUG}
- **Hostname:** ${VM_HOSTNAME}
- **Preview URL pattern:** https://${VM_HOSTNAME}/port/{PORT}/

Any web server listening on a port inside this VM is reachable at
\`https://${VM_HOSTNAME}/port/{PORT}/\`. Share that URL so the user sees it.
IDBLK
}

# Emit the tool-inventory block body on stdout. Uses VM_HOSTNAME where known.
emit_tool_inventory_block() {
    cat <<TOOLSBLK
## Installed Tools on This Machine
_Refreshed every boot from the rootfs manifest. Agent / tenant edits outside this block survive._

### Web Preview
Any process listening on a port → \`https://${VM_HOSTNAME:-<hostname>}/port/{PORT}/\`.
Example: \`python3 -m http.server 3000\` → share \`https://${VM_HOSTNAME:-<hostname>}/port/3000/\`.

### Package Managers (installs persist under \`~/.local\`)
- \`npm install -g <pkg>\` → \`~/.local/{bin,lib/node_modules}\`
- \`pnpm add -g <pkg>\` → \`~/.local/share/pnpm\`
- \`pip install <pkg>\` → \`~/.local/lib/pythonX.Y/site-packages\` (\`--user\` by default)
- \`uv tool install <pkg>\` → isolated venv at \`~/.local/share/uv/tools/\`
- \`uv venv && uv pip install <pkg>\` or \`python -m venv .venv\` → project-local \`.venv/\`
- \`bun install -g <pkg>\` / \`bun add -g <pkg>\` → \`~/.local/{bin,share/bun}\` (\`BUN_INSTALL\` is pre-set)
- \`bun install\` / \`bun add <pkg>\` in a project → project-local \`node_modules/\`
- \`bunx <cli>\` → one-shot tool runner (like \`npx\`, faster)

### OpenClaw Skill Directory
- \`clawhub\` (alias \`clawdhub\`) — search / install / publish openclaw skills. Run \`clawhub --help\` for subcommands.

### Structured-Output CLI Tools
Prefer these over POSIX equivalents — their output is easier to parse:
- \`rg\` — recursive search, gitignore-aware, \`--json\` mode. Use instead of \`grep -r\`.
- \`fd\` — file finder with sane defaults. Use instead of \`find\`.
- \`bat\` — file viewer with syntax highlighting. Use instead of \`cat\`.
- \`xh\` — HTTPie-compatible HTTP client. Use instead of \`curl\` for JSON APIs.
- \`yq\` — jq-style processor for YAML / TOML / XML.
- \`xsv\` — CSV toolkit (\`select\`, \`join\`, \`stats\`, \`sort\`).
- \`ast-grep\` (alias \`sg\`) — structural AST search/rewrite.
- \`delta\` — syntax-highlighted \`git diff\` viewer.
- \`jq\` — JSON processor.

### Document Conversion
- \`markitdown <file>\` — convert PDF / DOCX / XLSX / PPTX / Outlook \`.msg\` to Markdown.
- \`sqlite-utils\` — query / transform SQLite databases.

### Browser Automation
The \`browser-harness\`, \`ocm-browser\`, and \`ocm-integrations\` skills live under \`\${WORKSPACE_DIR}/skills/\` and are loaded by the gateway. Consult \`ocm-browser\` first for browser work: it covers the readiness check (\`bh --check\`) and bridge semantics. Consult \`ocm-integrations\` first for workspace integrations and connected SaaS tools. Everything else for browser automation (helpers, CDP access) comes from \`browser-harness\`.

### Other Baked-In
- \`gh\`, \`cloudflared\`, \`tmux\`, \`op\`, \`gws\`, \`himalaya\`, \`ffmpeg\`.

### Services
- \`ocm service create <name> --cmd "..." [--cwd /home/openclaw/... ] [--port N] [--description "..."]\` — create and start a persistent user service.
- \`ocm service list\` — list your user services.
- \`ocm service status <name>\` — inspect a user service.
- \`ocm service logs <name> [-f]\` — view service logs.
- \`ocm service restart <name>\` — restart a user service.
- \`ocm service remove <name>\` — remove a user service.
- \`sv status <name>\` also works for inspection; bare user-service names map to the \`u-\` namespace automatically.
- Do not create runit services directly under \`/etc/sv\` or use \`sudo\` to manage platform daemons.
- Mutating built-in platform services from the shell (\`sv restart gateway\`, \`sv stop pty-server\`, etc.) is intentionally blocked.
- To restart the gateway, use \`curl -sf -X POST http://127.0.0.1:7681/restart-gateway\`.
- \`ls /var/service/\` — inspect the runit service directory.
TOOLSBLK
}

# Emit the always-on Workspace Integrations pointer block for TOOLS.md. Static
# content (no host vars), so use a quoted heredoc - literal backticks/markdown.
# Detailed/provider-specific guidance lives in the ocm-integrations skill; this
# is only the awareness + search/describe/call rule.
emit_workspace_integrations_block() {
    cat <<'WIBLK'
### Workspace Integrations (connected SaaS tools)
This workspace may have integrations - connected external tools (Google
Workspace, GitHub, Jira, Linear, Slack, Notion, custom REST/OpenAPI APIs, remote
MCP servers) - surfaced by the native `mcp.servers.ocm` MCP server. Credentials
stay server-side; you only receive policy-filtered OCM facade tools.

To use ANY connected/external tool, you MUST go through the OCM facade:
1. `ocm.search_tools` first (describe intent + needed action/object)
2. `ocm.describe_tool` on the one result you pick (use its `tool_address`)
3. `ocm.call_tool` with that same `tool_address`

Prefer `tool_address` - it distinguishes multiple accounts/repos for one app.
Never invent provider tool names or call providers directly. If no `ocm.*` tools
are visible, tell the user OCM integrations aren't loaded this session. The
`ocm-integrations` skill has the full flow, semantic-search tips, and Google
recovery.
WIBLK
}

if mountpoint -q /data 2>/dev/null; then
    NEED_NEW_CONFIG=0
    PREV_CONFIG=""

    if [ -d "$CONFIG_LINK" ] && [ ! -L "$CONFIG_LINK" ]; then
        # Orchestrator pre-boot write created config-current as a real directory.
        # Move its contents into a timestamped config dir so the symlink scheme works.
        ts=$(date -u +%Y%m%dT%H%M%SZ)
        PREV_CONFIG="$CONFIG_BASE/$ts"
        mkdir -p "$PREV_CONFIG"
        cp -a "$CONFIG_LINK"/. "$PREV_CONFIG"/
        rm -rf "$CONFIG_LINK"
        ln -sfn "$PREV_CONFIG" "$CONFIG_LINK"
        echo "$ROOTFS_CONFIG_VERSION" > "$PREV_CONFIG/.config-version"
        chown -R openclaw:openclaw "$PREV_CONFIG"
        echo "  [OK] Migrated pre-boot config dir to $PREV_CONFIG"
    fi

    if [ -L "$CONFIG_LINK" ] && [ -d "$(readlink -f "$CONFIG_LINK" 2>/dev/null)" ]; then
        PREV_CONFIG=$(readlink -f "$CONFIG_LINK")
        PREV_VERSION=$(cat "$PREV_CONFIG/.config-version" 2>/dev/null || echo "0")
        if [ "$PREV_VERSION" != "$ROOTFS_CONFIG_VERSION" ]; then
            NEED_NEW_CONFIG=1
            echo "  Config version changed ($PREV_VERSION -> $ROOTFS_CONFIG_VERSION)"
        else
            echo "  Config version matches, reusing $PREV_CONFIG"
        fi
    else
        NEED_NEW_CONFIG=1
        echo "  No previous config found, creating initial"
    fi

    if [ "$NEED_NEW_CONFIG" -eq 1 ]; then
        ts=$(date -u +%Y%m%dT%H%M%SZ)
        NEW_CONFIG="$CONFIG_BASE/$ts"
        # Handle timestamp collision (near-impossible but defensive)
        if [ -d "$NEW_CONFIG" ]; then
            suffix=1
            while [ -d "${NEW_CONFIG}-${suffix}" ]; do
                suffix=$((suffix + 1))
            done
            NEW_CONFIG="${NEW_CONFIG}-${suffix}"
        fi
        mkdir -p "$NEW_CONFIG"

        # Copy forward from previous config (preserve user edits)
        if [ -n "$PREV_CONFIG" ] && [ -d "$PREV_CONFIG" ]; then
            for f in "$PREV_CONFIG"/*; do
                [ -e "$f" ] || continue
                local_base=$(basename "$f")
                [ "$local_base" = ".config-version" ] && continue
                [ -f "$NEW_CONFIG/$local_base" ] || cp "$f" "$NEW_CONFIG/$local_base"
            done
            echo "  Copied forward from $PREV_CONFIG"
        fi

        # Write version marker
        echo "$ROOTFS_CONFIG_VERSION" > "$NEW_CONFIG/.config-version"
        chown -R openclaw:openclaw "$NEW_CONFIG"

        # Atomic switch
        ln -sfn "$NEW_CONFIG" "$CONFIG_LINK"
        echo "  [OK] Config switched to $NEW_CONFIG"

        # GC: keep latest 5 config dirs, never delete config-current target
        CURRENT_TARGET=$(readlink -f "$CONFIG_LINK" 2>/dev/null || echo "")
        find "$CONFIG_BASE" -maxdepth 1 -name '[0-9]*' -printf '%T@ %p\n' 2>/dev/null | sort -rn | tail -n +6 | cut -d' ' -f2- | while read -r old; do
            old_real=$(readlink -f "$old")
            [ "$old_real" = "$CURRENT_TARGET" ] && continue
            rm -rf "$old"
            echo "  [GC] Removed old config: $old"
        done
    fi

    config_real=$(readlink -f "$CONFIG_LINK")

    # --- Migrate and symlink openclaw.json ---
    openclaw_config_src="$OPENCLAW_DIR/openclaw.json"
    if [ -f "$openclaw_config_src" ] && [ ! -L "$openclaw_config_src" ]; then
        mv "$openclaw_config_src" "$config_real/openclaw.json"
        chown openclaw:openclaw "$config_real/openclaw.json"
        echo "  [OK] Migrated openclaw.json to config dir"
    fi
    if [ -f "$CONFIG_LINK/openclaw.json" ]; then
        ln -sfn "config-current/openclaw.json" "$openclaw_config_src"
        # Tighten perms on the real file (and any prior copies) to 600 — config
        # contains tenant tokens; doctor flags it as world-readable on every VM
        # otherwise. chmod follows the symlink so the underlying file is fixed.
        chmod 600 "$openclaw_config_src" 2>/dev/null || true
        chmod 600 "$config_real/openclaw.json" 2>/dev/null || true
        echo "  [OK] openclaw.json symlinked to config dir"
    else
        echo "  [WARN] openclaw.json missing from config dir"
    fi

    # --- Migrate and symlink auth-profiles.json (runtime-generation aware) ---
    # ≤2026.5 runtimes read auth-profiles.json directly: keep it on the
    # persistent config dir via symlink (legacy behavior, unchanged).
    # ≥2026.6 runtimes store auth profiles in openclaw-agent.sqlite (persisted
    # via the /home/openclaw bind mount). The JSON is read exactly once, by the
    # boot-time `openclaw doctor --fix` below, which imports it into SQLite and
    # retires it to *.bak. Re-linking the config-dir copy on those runtimes
    # would re-import a stale token over a newer one on every boot.
    auth_src="$OPENCLAW_DIR/agents/main/agent/auth-profiles.json"
    if ocm_sqlite_auth_runtime; then
        if [ -L "$auth_src" ]; then
            # First boot after upgrading from a legacy runtime: materialize the
            # config-dir copy as a real file so doctor --fix imports it, then
            # retire the config-dir copy so later boots don't re-import it.
            real_auth=$(readlink -f "$auth_src" 2>/dev/null || echo "")
            rm -f "$auth_src"
            if [ -n "$real_auth" ] && [ -f "$real_auth" ]; then
                cp "$real_auth" "$auth_src"
                chown openclaw:openclaw "$auth_src"
                chmod 600 "$auth_src"
                mv "$real_auth" "${real_auth}.imported"
                echo "  [OK] Staged legacy auth-profiles.json for sqlite import"
            fi
        elif [ -f "$config_real/auth-profiles.json" ] && [ ! -e "$auth_src" ]; then
            cp "$config_real/auth-profiles.json" "$auth_src"
            chown openclaw:openclaw "$auth_src"
            chmod 600 "$auth_src"
            mv "$config_real/auth-profiles.json" "$config_real/auth-profiles.json.imported"
            echo "  [OK] Staged config-dir auth-profiles.json for sqlite import"
        fi
    else
        if [ -f "$auth_src" ] && [ ! -L "$auth_src" ]; then
            cp "$auth_src" "$config_real/auth-profiles.json"
            chown openclaw:openclaw "$config_real/auth-profiles.json"
            echo "  [OK] Migrated auth-profiles.json to config dir"
        fi
        ln -sfn "$CONFIG_LINK/auth-profiles.json" "$auth_src"
    fi

    # --- Migrate and symlink device.json (existence guard: no dangling symlinks) ---
    device_src="$OPENCLAW_DIR/identity/device.json"
    if [ -f "$device_src" ] && [ ! -L "$device_src" ]; then
        mv "$device_src" "$config_real/device.json"
        chown openclaw:openclaw "$config_real/device.json"
        ln -sfn "$CONFIG_LINK/device.json" "$device_src"
        echo "  [OK] Migrated device.json to config dir"
    elif [ -f "$CONFIG_LINK/device.json" ]; then
        ln -sfn "$CONFIG_LINK/device.json" "$device_src"
        echo "  [OK] device.json symlinked (preserved from previous boot)"
    else
        echo "  [OK] device.json: will be created on first TUI connect"
    fi

    # Legacy migration A: very old rootfs put IDENTITY.md under $CONFIG_LINK/
    # (rotating config dir). If nothing at the agent workspace yet, seed from
    # that file so tenant edits carry forward.
    _agent_ws="$OPENCLAW_DIR/workspace"
    if [ -n "$VM_HOSTNAME" ] && [ -n "$MACHINE_SLUG" ] \
       && [ ! -e "$_agent_ws/IDENTITY.md" ] && [ -f "$CONFIG_LINK/IDENTITY.md" ]; then
        cp "$CONFIG_LINK/IDENTITY.md" "$_agent_ws/IDENTITY.md"
        chown openclaw:openclaw "$_agent_ws/IDENTITY.md"
        echo "  [OK] IDENTITY.md restored from legacy config dir → $_agent_ws"
    fi

    # Legacy migration B: the c2d9ccf / e432f6c / e079db8 rootfs releases wrote
    # IDENTITY.md / TOOLS.md to /workspace (wrong — that's the user-code dir).
    # Openclaw reads from ~/.openclaw/workspace/ (set by configassembly). Only
    # touch files that we can prove we generated, i.e. those carrying an ocm
    # fence marker, so a tenant's own /workspace/IDENTITY.md or TOOLS.md (e.g.
    # in a project repo they've checked out) is left alone.
    for _legacy in IDENTITY.md TOOLS.md; do
        _src="/workspace/$_legacy"
        _dst="$_agent_ws/$_legacy"
        if [ -L "$_src" ]; then
            # Symlinks only came from the c2d9ccf rootfs — safe to drop.
            rm -f "$_src"
            echo "  [OK] removed stale symlink /workspace/$_legacy (from c2d9ccf rootfs)"
        elif [ -f "$_src" ] && grep -Fq "<!-- BEGIN ocm-" "$_src" 2>/dev/null; then
            # Real file with our fence — we generated it on a prior rootfs.
            if [ ! -e "$_dst" ]; then
                mv "$_src" "$_dst"
                chown openclaw:openclaw "$_dst" 2>/dev/null || true
                echo "  [OK] moved ocm-generated /workspace/$_legacy → $_dst"
            else
                rm -f "$_src"
                echo "  [OK] removed stale ocm-generated /workspace/$_legacy (canonical copy at $_dst)"
            fi
        fi
        # else: /workspace/$_legacy is either missing or a user-authored file
        # without our fence marker — leave it alone.
    done

    # Clean up prior-scheme copies at $OPENCLAW_DIR root (wrong parent, pre-c2d9ccf).
    rm -f "$OPENCLAW_DIR/IDENTITY.md" "$OPENCLAW_DIR/TOOLS.md"
else
    echo "  [WARN] No data volume — config versioning disabled"
fi

# --------------------------------------------------------------------------
# IDENTITY.md / TOOLS.md in the openclaw AGENT workspace.
# Location matches `configassembly/assembler.go` (workspace=/home/openclaw/.openclaw/workspace)
# and openclaw's `resolveDefaultAgentWorkspaceDir` ($HOME/.openclaw/workspace).
# Gateway's writeFileIfMissing is a no-op since we've already seeded these.
# --------------------------------------------------------------------------
mkdir -p "$OPENCLAW_DIR/workspace"
chown openclaw:openclaw "$OPENCLAW_DIR/workspace" 2>/dev/null || true
if [ -n "$VM_HOSTNAME" ] && [ -n "$MACHINE_SLUG" ]; then
    seed_workspace_file "$OPENCLAW_DIR/workspace/IDENTITY.md" IDENTITY.md "# IDENTITY.md"
    emit_machine_identity_block | merge_fenced_block "$OPENCLAW_DIR/workspace/IDENTITY.md" machine-identity
    echo "  [OK] $OPENCLAW_DIR/workspace/IDENTITY.md: template seeded + machine-identity block refreshed"
fi

seed_workspace_file "$OPENCLAW_DIR/workspace/TOOLS.md" TOOLS.md "# TOOLS.md"
emit_tool_inventory_block | merge_fenced_block "$OPENCLAW_DIR/workspace/TOOLS.md" tool-inventory
emit_workspace_integrations_block | merge_fenced_block "$OPENCLAW_DIR/workspace/TOOLS.md" workspace-integrations
echo "  [OK] $OPENCLAW_DIR/workspace/TOOLS.md: template seeded + tool-inventory and workspace-integrations blocks refreshed"

# Per-feature setup hooks live at /usr/local/libexec/ocm/. They're first-boot
# + upgrade idempotent; init just invokes them. Keeps browser-harness (and
# future tool setups) out of this script.
if [ -x /usr/local/libexec/ocm/install-browser-harness ]; then
    /usr/local/libexec/ocm/install-browser-harness || true
fi

# Removed: section 7b soul files fetch — soul files now on disk from data volume pre-boot write

# Targeted chown on directories we created (not recursive over growing state)
chown openclaw:openclaw "$OPENCLAW_DIR" \
    "$OPENCLAW_DIR/workspace" \
    "$OPENCLAW_DIR/agents" \
    "$OPENCLAW_DIR/agents/main" \
    "$OPENCLAW_DIR/agents/main/agent" \
    "$OPENCLAW_DIR/agents/main/sessions" \
    "$OPENCLAW_DIR/identity" 2>/dev/null

# ============================================
# 11. Dynamic provider env vars for login shells
# ============================================
echo ""
echo "=== Configuring Provider Env Vars ==="
cat > /etc/profile.d/openclaw-providers.sh << 'PROVIDEREOF'
# Read provider names from openclaw.json on disk (no metadata round-trip)
_OCM_NONCE=""
[ -f /run/ocm-nonce ] && _OCM_NONCE=$(cat /run/ocm-nonce)
_OCM_CONFIG="/home/openclaw/.openclaw/openclaw.json"
_PROVIDERS=$(jq -r '.models.providers // {} | keys[]' "$_OCM_CONFIG" 2>/dev/null)
for _OCM_PNAME in $_PROVIDERS; do
    # Base URL uses the gateway proxy on localhost
    _OCM_BURL="http://127.0.0.1:18789/v1"
    case "$_OCM_PNAME" in
        anthropic)
            export ANTHROPIC_API_KEY="$_OCM_NONCE"
            export ANTHROPIC_BASE_URL="$_OCM_BURL"
            ;;
        openai)
            export OPENAI_API_KEY="$_OCM_NONCE"
            export OPENAI_BASE_URL="$_OCM_BURL"
            ;;
        google)
            export GOOGLE_API_KEY="$_OCM_NONCE"
            export GOOGLE_BASE_URL="$_OCM_BURL"
            ;;
        *)
            _OCM_UPPER=$(echo "$_OCM_PNAME" | tr '[:lower:]' '[:upper:]' | tr '-' '_')
            export "${_OCM_UPPER}_API_KEY=$_OCM_NONCE"
            export "${_OCM_UPPER}_BASE_URL=$_OCM_BURL"
            ;;
    esac
done
unset _OCM_NONCE _OCM_CONFIG _PROVIDERS _OCM_PNAME _OCM_BURL _OCM_UPPER
PROVIDEREOF
chmod 644 /etc/profile.d/openclaw-providers.sh
echo "  [OK] /etc/profile.d/openclaw-providers.sh written"

# Also write OCM identity vars for shells
cat > /etc/profile.d/openclaw-identity.sh << IDENTITYEOF
export OCM_MACHINE_ID='${MACHINE_ID}'
export OPENCLAW_GATEWAY_TOKEN='${GATEWAY_TOKEN}'
IDENTITYEOF
chmod 644 /etc/profile.d/openclaw-identity.sh

# Add pnpm global bin and artifact runtime bin to PATH for terminal sessions.
# PNPM_HOME resolves at shell-source-time via \$HOME so it picks up the sourcer's
# home (openclaw users land on the bind-mounted /home/openclaw, which persists).
cat > /etc/profile.d/openclaw-path.sh << PATHEOF
export PNPM_HOME="\$HOME/.local/share/pnpm"
case ":\$PATH:" in
    *":\$PNPM_HOME:"*) ;;
    *) export PATH="\$PNPM_HOME:\$PATH" ;;
esac
# Source resolved runtime env if available (artifact bin, plugins dir)
if [ -f /run/ocm-runtime-env ]; then
    . /run/ocm-runtime-env
    if [ "\$OCM_EFFECTIVE_RUNTIME" = "artifact" ] && [ -n "\$OCM_EFFECTIVE_BIN" ]; then
        _adir=\$(dirname "\$OCM_EFFECTIVE_BIN")
        case ":\$PATH:" in
            *":\$_adir:"*) ;;
            *) export PATH="\$_adir:\$PATH" ;;
        esac
    fi
    if [ -n "\$OCM_EFFECTIVE_PLUGINS_DIR" ]; then
        export OPENCLAW_BUNDLED_PLUGINS_DIR="\$OCM_EFFECTIVE_PLUGINS_DIR"
    fi
fi
PATHEOF
chmod 644 /etc/profile.d/openclaw-path.sh

# ============================================
# 9b. Device identity
# ============================================
# device.json is preserved across reboots in config-current/ (set up in section 10).
# The gateway's shouldAllowSilentLocalPairing auto-approves local device pairing
# requests, so a fresh identity would also auto-pair — but preserving avoids
# unnecessary re-pairing delays.
# To force re-pairing, set RESET_DEVICE_IDENTITY=1 on the kernel cmdline.
if echo "$CMDLINE" | grep -q "RESET_DEVICE_IDENTITY=1"; then
    rm -f "$CONFIG_LINK/device.json" 2>/dev/null
    rm -f "$OPENCLAW_DIR/identity/device.json" 2>/dev/null
    echo "  [OK] device.json reset (RESET_DEVICE_IDENTITY=1)"
elif [ -f "$CONFIG_LINK/device.json" ] 2>/dev/null; then
    echo "  [OK] device.json preserved from previous boot"
fi

write_envdir() {
    dir=$1
    shift
    rm -rf "$dir"
    mkdir -p "$dir"
    for kv in "$@"; do
        k=${kv%%=*}
        v=${kv#*=}
        printf '%s' "$v" > "$dir/$k"
        chmod 600 "$dir/$k"
    done
    chown -R openclaw:openclaw "$dir"
    chmod 700 "$dir"
}

write_envdir_file() {
    file=$1
    value=$2
    printf '%s' "$value" > "$file"
    chown openclaw:openclaw "$file"
    chmod 600 "$file"
}

# ============================================
# 10. Prepare supervised PTY server
# ============================================
echo ""
phase_start "pty-server"
touch /var/log/pty-server.log
chown openclaw:openclaw /var/log/pty-server.log
echo "  [OK] PTY server prepared for runit supervision on port 7681"
phase_end "pty-server"

# ============================================
# 11. Prepare auth proxy + cloudflared (per-VM tunnel)
# ============================================
echo ""
phase_start "auth-proxy"

# SIGNING_KEY, TUNNEL_TOKEN, and VM_HOSTNAME already extracted in section 8
if [ -z "$SIGNING_KEY" ]; then
    echo "[FATAL] signing_key is empty — auth proxy cannot start, all proxy traffic will fail"
    echo "[FATAL] MACHINE_CONFIG keys: $(echo "$MACHINE_CONFIG" | jq -r 'keys | join(", ")' 2>/dev/null || echo '<parse failed>')"
    exit 1
fi
# tunnel_token and vm_hostname are only needed for the per-VM Cloudflare tunnel
# (hosted profile). In local/self-hosted profiles without a tunnel they are
# empty and the VM is reached through the agent proxy instead — warn, don't die.
if [ -z "$TUNNEL_TOKEN" ]; then
    echo "  [WARN] tunnel_token is empty — cloudflared disabled (VM reachable via agent proxy)"
fi
if [ -z "$VM_HOSTNAME" ]; then
    echo "  [WARN] vm_hostname is empty — per-VM tunnel has no hostname"
fi

touch /var/log/authproxy.log /var/log/cloudflared.log
chown openclaw:openclaw /var/log/authproxy.log /var/log/cloudflared.log

# Start filebrowser (web file manager on port 8082, behind auth proxy at /files)
# Skipped in quick-start mode — not needed for fast boot-to-ready scenarios.
if [ -n "$OCM_QUICK_START" ]; then
    echo "  [SKIP] Filebrowser (quick start mode)"
elif command -v filebrowser >/dev/null 2>&1; then
    touch /var/log/filebrowser.log
    chown openclaw:openclaw /var/log/filebrowser.log
    su -s /bin/bash openclaw -c "
      filebrowser config init -d /tmp/filebrowser.db >> /var/log/filebrowser.log 2>&1 || true
      filebrowser users add openclaw adminadminadmin -d /tmp/filebrowser.db >> /var/log/filebrowser.log 2>&1 || true
      filebrowser config set --auth.method=noauth -d /tmp/filebrowser.db >> /var/log/filebrowser.log 2>&1
      if [ \$? -ne 0 ]; then
        echo 'ERROR: filebrowser config set --auth.method=noauth failed' >> /var/log/filebrowser.log
      fi
    "
    echo "  [OK] Filebrowser database initialized"
else
    echo "  [SKIP] Filebrowser (binary not found)"
fi

echo "  [OK] Auth proxy will be supervised on port 8080"
echo "  [OK] cloudflared will be supervised"
echo "  Hostname: ${VM_HOSTNAME:-<not set>}"
phase_end "auth-proxy"

# ============================================
# 12. SSH cert validation (Cloudflare Access)
# ============================================
echo ""
phase_start "ssh-certs"

CF_CA_PUBKEY=$(curl -sf "${NONCE_ARGS[@]}" "$METADATA_URL/v1/cf-ca-pubkey" 2>/dev/null || echo "")
if [ -n "$CF_CA_PUBKEY" ]; then
    echo "$CF_CA_PUBKEY" > /etc/ssh/cf_ca.pub
    chmod 644 /etc/ssh/cf_ca.pub

    # cf-ssh-check is installed to /etc/ssh/ at rootfs build time (root-owned).
    # sshd's safe_path() requires every parent of AuthorizedPrincipalsCommand to
    # be owned by root or the command user — /etc/ssh/ satisfies this.
    if [ ! -f /etc/ssh/cf-ssh-check ]; then
        echo "  [FAIL] /etc/ssh/cf-ssh-check missing — rootfs is too old, rebuild with make build-upload-rootfs"
    else

    # Strip legacy SSH cert directives from main sshd_config to avoid conflicts
    # with the drop-in file. These were appended directly in earlier rootfs versions.
    sed -i '/^# Cloudflare Access SSH certificate validation$/d' /etc/ssh/sshd_config
    sed -i '/^TrustedUserCAKeys /d' /etc/ssh/sshd_config
    sed -i '/^AuthorizedPrincipalsCommand /d' /etc/ssh/sshd_config
    sed -i '/^AuthorizedPrincipalsCommandUser /d' /etc/ssh/sshd_config

    # Ensure sshd_config includes drop-in directory (idempotent).
    # Include must appear early — sshd uses first-match for most directives.
    mkdir -p /etc/ssh/sshd_config.d
    if ! grep -q '^Include /etc/ssh/sshd_config.d/\*.conf' /etc/ssh/sshd_config; then
        sed -i '1i Include /etc/ssh/sshd_config.d/*.conf' /etc/ssh/sshd_config
        echo "  [OK] Added Include directive to sshd_config"
    fi

    # Write drop-in config for CF Access SSH certificate validation.
    # AuthorizedPrincipalsCommand fetches allowed principals from the metadata
    # server — sshd matches them against the cert's principals (email or username).
    cat > /etc/ssh/sshd_config.d/cf-access.conf << 'SSHEOF'
TrustedUserCAKeys /etc/ssh/cf_ca.pub
AuthorizedPrincipalsCommand /etc/ssh/cf-ssh-check %i
AuthorizedPrincipalsCommandUser nobody
GatewayPorts yes
SSHEOF
    chmod 644 /etc/ssh/sshd_config.d/cf-access.conf
    echo "  [OK] Wrote /etc/ssh/sshd_config.d/cf-access.conf"

    # Persist SSH host keys on data volume so they survive migration.
    # Without this, users get "REMOTE HOST IDENTIFICATION HAS CHANGED" after migration.
    mkdir -p /run/sshd
    SSH_KEY_STORE="/data/home/openclaw/.ssh-host-keys"
    if [ -d "$SSH_KEY_STORE" ] && ls "$SSH_KEY_STORE"/ssh_host_* >/dev/null 2>&1; then
        cp "$SSH_KEY_STORE"/ssh_host_* /etc/ssh/
        echo "  [OK] Restored SSH host keys from data volume"
    else
        ssh-keygen -A 2>/dev/null || true
        mkdir -p "$SSH_KEY_STORE"
        cp /etc/ssh/ssh_host_* "$SSH_KEY_STORE"/
        echo "  [OK] Generated new SSH host keys (saved to data volume)"
    fi

    # Validate config — hard fail if broken. Do not start sshd with bad config.
    if ! /usr/sbin/sshd -t 2>/tmp/sshd-config-err; then
        echo "  [FAIL] sshd config validation failed — sshd will NOT start:"
        cat /tmp/sshd-config-err
    else
        echo "  [OK] sshd config validated (sshd -t)"

        # Full stop/start instead of HUP reload (HUP can silently fail to pick up changes)
        if [ -f /var/run/sshd.pid ] && kill -0 "$(cat /var/run/sshd.pid)" 2>/dev/null; then
            kill "$(cat /var/run/sshd.pid)" 2>/dev/null
            for _i in 1 2 3; do
                kill -0 "$(cat /var/run/sshd.pid 2>/dev/null)" 2>/dev/null || break
                sleep 0.5
            done
        fi
        /usr/sbin/sshd

        # Post-start verification
        if /usr/sbin/sshd -T 2>/dev/null | grep -q "trustedusercakeys /etc/ssh/cf_ca.pub"; then
            echo "  [OK] sshd running — cert auth active (verified with sshd -T)"
        else
            echo "  [WARN] sshd started but TrustedUserCAKeys not in effective config"
        fi
    fi

    fi # /etc/ssh/cf-ssh-check exists
else
    echo "  [SKIP] No CF CA pubkey available"
fi

phase_end "ssh-certs"

# ============================================
# 13. Gateway supervision handoff
# ============================================
echo ""
phase_start "gateway"
echo "=== Preparing OpenClaw Gateway (supervised) ==="

mkdir -p /run/ocm-service-env /var/service
touch /var/log/openclaw-gateway.log
chown openclaw:openclaw /var/log/openclaw-gateway.log

printf 'unknown\n' > /run/ocm-gateway-status
printf '0 0\n' > /run/ocm-gateway-manual-restart
printf '0\n' > /run/ocm-gateway-crash-loop
rm -f /run/ocm-gateway-crashes
chown openclaw:openclaw /run/ocm-gateway-status /run/ocm-gateway-manual-restart /run/ocm-gateway-crash-loop
chmod 644 /run/ocm-gateway-status /run/ocm-gateway-manual-restart /run/ocm-gateway-crash-loop

write_envdir /run/ocm-service-env/gateway \
    "HOME=/home/openclaw" \
    "PATH=${OCM_RUNTIME_PATH_PREFIX}/home/openclaw/.local/bin:/home/openclaw/.local/share/pnpm:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" \
    "PNPM_HOME=/home/openclaw/.local/share/pnpm" \
    "NPM_CONFIG_PREFIX=/home/openclaw/.local" \
    "NODE_ENV=production" \
    "NODE_OPTIONS=--max-old-space-size=2048" \
    "NODE_COMPILE_CACHE=/data/openclaw-compile-cache" \
    "OPENCLAW_NO_RESPAWN=1" \
    "WORKSPACE_DIR=/workspace" \
    "OPENCLAW_STATE_DIR=$OPENCLAW_DIR" \
    "OPENCLAW_DISABLE_BONJOUR=1" \
    "OPENCLAW_GATEWAY_TOKEN=$GATEWAY_TOKEN" \
    "OCM_MACHINE_ID=$MACHINE_ID" \
    "OCM_RUNTIME_SOURCE=artifact" \
    "OCM_RESOLVED_ROOTFS_VERSION=$OCM_RESOLVED_ROOTFS_VERSION" \
    "OCM_RESOLVED_OPENCLAW_VERSION=$OCM_RESOLVED_OPENCLAW_VERSION" \
    "OCM_VERSION_SOURCE=$OCM_VERSION_SOURCE" \
    "OPENCLAW_BIN=$OCM_EFFECTIVE_BIN"
if [ -n "$OCM_EFFECTIVE_PLUGINS_DIR" ]; then
    write_envdir_file /run/ocm-service-env/gateway/OPENCLAW_BUNDLED_PLUGINS_DIR "$OCM_EFFECTIVE_PLUGINS_DIR"
fi
if [ -n "$OCM_QUICK_START" ]; then
    for v in CHANNELS BROWSER_CONTROL_SERVER CANVAS_HOST CRON GMAIL_WATCHER; do
        write_envdir_file "/run/ocm-service-env/gateway/OPENCLAW_SKIP_$v" "1"
    done
fi

write_envdir /run/ocm-service-env/pty-server \
    "HOME=/home/openclaw" \
    "PATH=${OCM_RUNTIME_PATH_PREFIX}/home/openclaw/.local/bin:/home/openclaw/.local/share/pnpm:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" \
    "PNPM_HOME=/home/openclaw/.local/share/pnpm" \
    "TERM=xterm-256color" \
    "OPENCLAW_GATEWAY_TOKEN=$GATEWAY_TOKEN" \
    "OCM_RUNTIME_SOURCE=$OCM_RUNTIME_SOURCE" \
    "OCM_RESOLVED_OPENCLAW_VERSION=$OCM_RESOLVED_OPENCLAW_VERSION" \
    "OPENCLAW_BIN=$OCM_EFFECTIVE_BIN"
if [ -n "$OCM_EFFECTIVE_PLUGINS_DIR" ]; then
    write_envdir_file /run/ocm-service-env/pty-server/OPENCLAW_BUNDLED_PLUGINS_DIR "$OCM_EFFECTIVE_PLUGINS_DIR"
fi

write_envdir /run/ocm-service-env/cloudflared \
    "TUNNEL_TOKEN=$TUNNEL_TOKEN"

write_envdir /run/ocm-service-env/authproxy \
    "SIGNING_KEY=$SIGNING_KEY" \
    "MACHINE_ID=$MACHINE_ID" \
    "OPENCLAW_GATEWAY_TOKEN=$GATEWAY_TOKEN"

# Forward VM logs to host log manager via metadata endpoint
forward_logs() {
    local src="$1" logfile="$2"
    tail -F "$logfile" 2>/dev/null | while IFS= read -r line; do
        curl -sf -X POST "$METADATA_URL/v1/logs" \
            -H "X-Metadata-Nonce: $METADATA_NONCE" \
            -H "Content-Type: application/json" \
            -d "{\"source\":\"$src\",\"line\":$(printf '%s' "$line" | jq -Rs .)}" \
            2>/dev/null || true
    done
}

forward_logs "gateway" /var/log/openclaw-gateway.log &
forward_logs "init" /var/log/openclaw-init.log &
forward_logs "authproxy" /var/log/authproxy.log &

# One-shot plugin-registry repair (per `openclaw doctor` recommendation).
# On fresh VMs the bundled plugins (composio, memory-core, opik-openclaw) are
# present in dist/extensions/ but not registered in ~/.openclaw/plugins/installs.json.
# `doctor --fix` rebuilds the registry from the enabled-plugins set in
# openclaw.json (already written above from the metadata config). Runs as
# openclaw user, before the gateway service starts, so the gateway sees a
# consistent registry on its first read. Non-fatal: log warnings but don't
# fail boot if doctor errors.
if [ -n "$OCM_EFFECTIVE_BIN" ] && [ -x "$OCM_EFFECTIVE_BIN" ] && [ -f "$OPENCLAW_DIR/openclaw.json" ]; then
    echo "  Running openclaw doctor --fix (plugin registry repair, 30s timeout)..."
    # Hard 30s timeout: doctor is on the boot critical path (gateway service
    # only starts after this returns). If doctor hangs we don't want VMs
    # stuck booting indefinitely. SIGTERM at 30s, SIGKILL 5s later.
    timeout --kill-after=5 30 su -s /bin/bash openclaw -c "
        export HOME=/home/openclaw
        export OPENCLAW_STATE_DIR=$OPENCLAW_DIR
        export OPENCLAW_NO_RESPAWN=1
        '$OCM_EFFECTIVE_BIN' doctor --fix --non-interactive 2>&1 | sed 's/^/    /'
    " || {
        rc=$?
        if [ "$rc" = "124" ] || [ "$rc" = "137" ]; then
            echo "  [WARN] openclaw doctor --fix timed out after 30s — boot continues"
        else
            echo "  [WARN] openclaw doctor --fix exited $rc — boot continues"
        fi
    }
fi

for svc in gateway pty-server; do
    rm -f "/etc/sv/$svc/down"
    ln -sfn "/etc/sv/$svc" "/var/service/$svc"
done

if [ -z "$TUNNEL_TOKEN" ]; then
    rm -f /var/service/cloudflared
    echo "  [SKIP] cloudflared supervision (no tunnel_token — local/self-hosted)"
elif command -v cloudflared >/dev/null 2>&1; then
    rm -f /etc/sv/cloudflared/down
    ln -sfn /etc/sv/cloudflared /var/service/cloudflared
else
    rm -f /var/service/cloudflared
    echo "  [SKIP] cloudflared supervision (binary not found)"
fi

if [ -f /usr/local/bin/authproxy ]; then
    rm -f /etc/sv/authproxy/down
    ln -sfn /etc/sv/authproxy /var/service/authproxy
else
    rm -f /var/service/authproxy
    echo "  [SKIP] authproxy supervision (binary not found)"
fi

if command -v filebrowser >/dev/null 2>&1; then
    rm -f /etc/sv/filebrowser/down
    ln -sfn /etc/sv/filebrowser /var/service/filebrowser
else
    rm -f /var/service/filebrowser
fi
if [ -n "$OCM_QUICK_START" ]; then
    : > /etc/sv/filebrowser/down
fi

if [ -x /usr/local/bin/ocm-service-admin ]; then
    /usr/local/bin/ocm-service-admin rematerialize-all || echo "  [WARN] user-service rematerialization failed"
else
    echo "  [WARN] ocm-service-admin missing — user services unavailable"
fi

echo "  [OK] Services activated under /var/service"
phase_end "gateway"

log INFO "handing off to runsvdir as PID 1 supervisor"
exec runsvdir -P /var/service
