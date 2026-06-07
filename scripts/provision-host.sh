#!/bin/bash
set -euo pipefail

# provision-host.sh — Provision an Ubuntu 24.04 host as an OpenClaw worker
#
# Installs all system dependencies required to run Firecracker microVMs:
# KVM + nested virt, Firecracker, cloudflared, the guest kernel, XFS/reflink
# state storage, and sysctl/limit tuning. It does NOT register the host with
# the control plane — that is done separately:
#   - GCP UI-provisioned hosts self-bootstrap + self-register via metadata
#     (see backend/internal/provisioner/host_startup.sh).
#   - Bare/BYO hosts enroll via the install script (see docs/host-enrollment.md):
#       curl -sL <BACKEND_URL>/api/agent/install | sudo bash -s -- <ENROLLMENT_TOKEN>
#
# It is also the script used to build a golden image for fast GCP provisioning:
#   run this on a VM, then snapshot it (see scripts/create-snapshot.sh) and set
#   FC_SNAPSHOT_NAME so the provisioner boots from the prebuilt image.
#
# Usage:
#   scp scripts/provision-host.sh ubuntu@<IP>:~/
#   ssh ubuntu@<IP> 'sudo bash ~/provision-host.sh'
#   ssh ubuntu@<IP> 'sudo OCM_VM_STATE_XFS=0 bash ~/provision-host.sh'  # opt out of loop-backed VM state XFS
#   ssh ubuntu@<IP> 'sudo OCM_BROWSER_XFS_DEVICE=/dev/nvme1n1 OCM_FORMAT_BROWSER_XFS=1 bash ~/provision-host.sh'
#
# Artifact source (kernel, browser rootfs) defaults to OCM_ARTIFACT_BUCKET.
# Point it at your own bucket, or use the public default and make it readable.

FIRECRACKER_VERSION="${FIRECRACKER_VERSION:-v1.10.1}"
OCM_ARTIFACT_BUCKET="${OCM_ARTIFACT_BUCKET:-gs://openclawmachines}"
KERNEL_GCS_PATH="${OCM_KERNEL_GCS_PATH:-${OCM_ARTIFACT_BUCKET}/vmlinux}"
OCM_BASE="${OCM_BASE:-/var/lib/ocm}"
OCM_STATE_DIR="${OCM_STATE_DIR:-${OCM_BASE}/vms}"
OCM_VM_STATE_XFS="${OCM_VM_STATE_XFS:-true}"
OCM_VM_STATE_XFS_SIZE="${OCM_VM_STATE_XFS_SIZE:-200G}"
OCM_VM_STATE_XFS_IMAGE="${OCM_VM_STATE_XFS_IMAGE:-${OCM_BASE}/ocm-xfs.img}"
OCM_BROWSER_BASE="${OCM_BROWSER_BASE:-/var/lib/ocm-browser}"
OCM_XFS_DEVICE="${OCM_XFS_DEVICE:-}"
OCM_FORMAT_XFS="${OCM_FORMAT_XFS:-false}"
OCM_BROWSER_XFS_DEVICE="${OCM_BROWSER_XFS_DEVICE:-}"
OCM_FORMAT_BROWSER_XFS="${OCM_FORMAT_BROWSER_XFS:-false}"
OCM_AGENT_ENV="${OCM_AGENT_ENV:-/etc/ocm-agent/agent.env}"
OCM_BROWSER_ROOTFS_GCS_MANIFEST="${OCM_BROWSER_ROOTFS_GCS_MANIFEST:-${OCM_ARTIFACT_BUCKET}/kernel-browser-rootfs/manifest.json}"
OCM_BROWSER_ROOTFS_VERSION="${OCM_BROWSER_ROOTFS_VERSION:-}"

log() { echo "==> $*"; }
err() { echo "ERROR: $*" >&2; exit 1; }

truthy() {
    case "${1,,}" in
        1|true|yes|on) return 0 ;;
        *) return 1 ;;
    esac
}

probe_reflink_dir() {
    local dir="$1"
    local probe_dir="${dir}/.ocm-reflink-probe"
    mkdir -p "$probe_dir"
    printf 'ocm reflink probe\n' > "${probe_dir}/src"
    if cp --reflink=always "${probe_dir}/src" "${probe_dir}/dst" 2>/dev/null; then
        rm -rf "$probe_dir"
        printf 'yes'
        return 0
    fi
    rm -rf "$probe_dir"
    printf 'no'
    return 1
}

upsert_agent_env() {
    local key="$1"
    local value="$2"
    mkdir -p "$(dirname "$OCM_AGENT_ENV")"
    if [ ! -f "$OCM_AGENT_ENV" ]; then
        printf '%s=%s\n' "$key" "$value" > "$OCM_AGENT_ENV"
        return
    fi
    local tmp
    tmp="$(mktemp "${OCM_AGENT_ENV}.tmp.XXXXXX")"
    grep -v "^${key}=" "$OCM_AGENT_ENV" > "$tmp" || true
    printf '%s=%s\n' "$key" "$value" >> "$tmp"
    cat "$tmp" > "$OCM_AGENT_ENV"
    rm -f "$tmp"
}

write_storage_env_handoff() {
    local browser_state_dir="${1:-}"
    mkdir -p /etc/ocm-agent
    printf 'STATE_DIR=%s\n' "$OCM_STATE_DIR" > /etc/ocm-agent/vm-state.env
    if [ -n "$browser_state_dir" ]; then
        printf 'BROWSER_STATE_DIR=%s\n' "$browser_state_dir" > /etc/ocm-agent/browser-state.env
    fi
}

write_agent_storage_dropin() {
    if [ ! -d /etc/systemd/system ]; then
        return 0
    fi
    mkdir -p /etc/systemd/system/ocm-agent.service.d
    cat > /etc/systemd/system/ocm-agent.service.d/storage.conf <<EOF
[Unit]
RequiresMountsFor=${OCM_STATE_DIR}
EOF
}

configure_browser_rootfs_default() {
    upsert_agent_env "BROWSER_ROOTFS_GCS_MANIFEST" "$OCM_BROWSER_ROOTFS_GCS_MANIFEST"
    if [ -n "$OCM_BROWSER_ROOTFS_VERSION" ]; then
        upsert_agent_env "BROWSER_ROOTFS_VERSION" "$OCM_BROWSER_ROOTFS_VERSION"
    fi
}

ensure_vm_state_fstab_entry() {
    local fstab=/etc/fstab
    touch "$fstab"
    while read -r fs_file fs_mount fs_type _rest; do
        [ -n "${fs_file:-}" ] || continue
        case "$fs_file" in \#*) continue ;; esac
        if [ "${fs_mount:-}" = "$OCM_STATE_DIR" ]; then
            if [ "$fs_file" = "$OCM_VM_STATE_XFS_IMAGE" ] && [ "${fs_type:-}" = "xfs" ]; then
                return 0
            fi
            err "${OCM_STATE_DIR} already has a different /etc/fstab entry: ${fs_file} ${fs_mount} ${fs_type}"
        fi
    done < "$fstab"
    echo "${OCM_VM_STATE_XFS_IMAGE} ${OCM_STATE_DIR} xfs loop,noatime,nofail 0 0" >> "$fstab"
}

configure_vm_state_storage() {
    mkdir -p "$OCM_STATE_DIR"
    upsert_agent_env "STATE_DIR" "$OCM_STATE_DIR"

    if ! truthy "$OCM_VM_STATE_XFS"; then
        log "Skipping VM state XFS loop mount because OCM_VM_STATE_XFS=${OCM_VM_STATE_XFS}"
        if [ -z "$OCM_BROWSER_XFS_DEVICE" ]; then
            upsert_agent_env "BROWSER_STATE_DIR" "$OCM_STATE_DIR"
            write_storage_env_handoff "$OCM_STATE_DIR"
        fi
        return 0
    fi

    if probe_reflink_dir "$OCM_BASE" >/dev/null 2>&1; then
        log "${OCM_BASE} already supports reflink; using native VM state dir ${OCM_STATE_DIR}"
        if [ -z "$OCM_BROWSER_XFS_DEVICE" ]; then
            upsert_agent_env "BROWSER_STATE_DIR" "$OCM_STATE_DIR"
            write_storage_env_handoff "$OCM_STATE_DIR"
        fi
        write_agent_storage_dropin
        return 0
    fi

    local script_dir helper set_browser_state
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    helper="${script_dir}/configure-vm-state-xfs.sh"
    if [ -z "$OCM_BROWSER_XFS_DEVICE" ]; then
        set_browser_state=true
    else
        set_browser_state=false
    fi

    if [ -f "$helper" ]; then
        log "Configuring loop-backed XFS VM state storage at ${OCM_STATE_DIR}..."
        OCM_BASE="$OCM_BASE" \
        OCM_STATE_DIR="$OCM_STATE_DIR" \
        OCM_VM_STATE_XFS_IMAGE="$OCM_VM_STATE_XFS_IMAGE" \
        OCM_VM_STATE_XFS_SIZE="$OCM_VM_STATE_XFS_SIZE" \
        OCM_AGENT_ENV="$OCM_AGENT_ENV" \
        OCM_SET_BROWSER_STATE_DIR="$set_browser_state" \
        OCM_STOP_AGENT=true \
        OCM_RESTART_AGENT=false \
            bash "$helper"
        return 0
    fi

    if [ -n "$(find "$OCM_STATE_DIR" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null || true)" ]; then
        err "${OCM_STATE_DIR} is not empty and ${helper} is not present; copy scripts/configure-vm-state-xfs.sh for safe migration"
    fi

    log "Configuring loop-backed XFS VM state storage at ${OCM_STATE_DIR}..."
    mkdir -p "$(dirname "$OCM_VM_STATE_XFS_IMAGE")"
    if [ -e "$OCM_VM_STATE_XFS_IMAGE" ]; then
        fs_type="$(blkid -p -o value -s TYPE "$OCM_VM_STATE_XFS_IMAGE" 2>/dev/null || true)"
        [ "$fs_type" = "xfs" ] || err "${OCM_VM_STATE_XFS_IMAGE} exists but is ${fs_type:-not XFS}"
    else
        truncate -s "$OCM_VM_STATE_XFS_SIZE" "$OCM_VM_STATE_XFS_IMAGE"
        mkfs.xfs -f -m reflink=1 "$OCM_VM_STATE_XFS_IMAGE" >/dev/null
    fi
    if ! mountpoint -q "$OCM_STATE_DIR" 2>/dev/null; then
        ensure_vm_state_fstab_entry
        mount "$OCM_STATE_DIR"
    fi
    probe_reflink_dir "$OCM_STATE_DIR" >/dev/null 2>&1 || err "${OCM_STATE_DIR} does not support reflink after mount"
    if [ -z "$OCM_BROWSER_XFS_DEVICE" ]; then
        upsert_agent_env "BROWSER_STATE_DIR" "$OCM_STATE_DIR"
        write_storage_env_handoff "$OCM_STATE_DIR"
    fi
    write_agent_storage_dropin
}

configure_ocm_storage() {
    if [ -z "$OCM_XFS_DEVICE" ]; then
        return 0
    fi

    [ -b "$OCM_XFS_DEVICE" ] || err "OCM_XFS_DEVICE is not a block device: $OCM_XFS_DEVICE"

    log "Configuring ${OCM_BASE} on XFS device ${OCM_XFS_DEVICE}..."
    mkdir -p "$OCM_BASE"

    if findmnt -rn -S "$OCM_XFS_DEVICE" >/dev/null 2>&1; then
        mounted_at="$(findmnt -rn -S "$OCM_XFS_DEVICE" -o TARGET | head -1)"
        [ "$mounted_at" = "$OCM_BASE" ] || err "${OCM_XFS_DEVICE} is already mounted at ${mounted_at}, not ${OCM_BASE}"
        log "XFS device already mounted at ${OCM_BASE}"
    else
        if truthy "$OCM_FORMAT_XFS"; then
            log "Formatting ${OCM_XFS_DEVICE} as XFS with reflink=1..."
            mkfs.xfs -f -m reflink=1 "$OCM_XFS_DEVICE" >/dev/null
        else
            fs_type="$(blkid -o value -s TYPE "$OCM_XFS_DEVICE" 2>/dev/null || true)"
            [ "$fs_type" = "xfs" ] || err "${OCM_XFS_DEVICE} is ${fs_type:-unformatted}; set OCM_FORMAT_XFS=1 to format it as XFS"
        fi

        uuid="$(blkid -o value -s UUID "$OCM_XFS_DEVICE")"
        [ -n "$uuid" ] || err "Could not read UUID from ${OCM_XFS_DEVICE}"
        if ! grep -q "UUID=${uuid}[[:space:]]\+${OCM_BASE}[[:space:]]" /etc/fstab; then
            echo "UUID=${uuid} ${OCM_BASE} xfs defaults,noatime 0 2" >> /etc/fstab
        fi
        mount "$OCM_BASE"
    fi
}

configure_browser_storage() {
    if [ -z "$OCM_BROWSER_XFS_DEVICE" ]; then
        return 0
    fi

    [ -b "$OCM_BROWSER_XFS_DEVICE" ] || err "OCM_BROWSER_XFS_DEVICE is not a block device: $OCM_BROWSER_XFS_DEVICE"

    log "Configuring browser VM state on XFS device ${OCM_BROWSER_XFS_DEVICE} at ${OCM_BROWSER_BASE}..."
    mkdir -p "$OCM_BROWSER_BASE"

    if findmnt -rn -S "$OCM_BROWSER_XFS_DEVICE" >/dev/null 2>&1; then
        mounted_at="$(findmnt -rn -S "$OCM_BROWSER_XFS_DEVICE" -o TARGET | head -1)"
        [ "$mounted_at" = "$OCM_BROWSER_BASE" ] || err "${OCM_BROWSER_XFS_DEVICE} is already mounted at ${mounted_at}, not ${OCM_BROWSER_BASE}"
        log "Browser XFS device already mounted at ${OCM_BROWSER_BASE}"
    else
        if truthy "$OCM_FORMAT_BROWSER_XFS"; then
            log "Formatting ${OCM_BROWSER_XFS_DEVICE} as XFS with reflink=1..."
            mkfs.xfs -f -m reflink=1 "$OCM_BROWSER_XFS_DEVICE" >/dev/null
        else
            fs_type="$(blkid -o value -s TYPE "$OCM_BROWSER_XFS_DEVICE" 2>/dev/null || true)"
            [ "$fs_type" = "xfs" ] || err "${OCM_BROWSER_XFS_DEVICE} is ${fs_type:-unformatted}; set OCM_FORMAT_BROWSER_XFS=1 to format it as XFS"
        fi

        uuid="$(blkid -o value -s UUID "$OCM_BROWSER_XFS_DEVICE")"
        [ -n "$uuid" ] || err "Could not read UUID from ${OCM_BROWSER_XFS_DEVICE}"
        if ! grep -q "UUID=${uuid}[[:space:]]\+${OCM_BROWSER_BASE}[[:space:]]" /etc/fstab; then
            echo "UUID=${uuid} ${OCM_BROWSER_BASE} xfs defaults,noatime 0 2" >> /etc/fstab
        fi
        mount "$OCM_BROWSER_BASE"
    fi
}

# ── Preflight checks ──────────────────────────────────────────────────────────

if [ "$(id -u)" -ne 0 ]; then
    err "Must run as root (use sudo)"
fi

if ! grep -qi 'ubuntu' /etc/os-release 2>/dev/null; then
    err "This script targets Ubuntu 24.04. Detected a different OS."
fi

# ── 1. System packages ────────────────────────────────────────────────────────

log "Updating package index..."
apt-get update -qq

log "Installing system dependencies..."
apt-get install -y -qq \
    curl \
    jq \
    zstd \
    iptables \
    iproute2 \
    net-tools \
    xfsprogs \
    e2fsprogs \
    cpu-checker \
    htop \
    acl \
    ca-certificates \
    gnupg \
    > /dev/null

# ── 2. Verify KVM support ────────────────────────────────────────────────────

log "Checking KVM support..."
if [ ! -e /dev/kvm ]; then
    # Try loading the module
    modprobe kvm_amd 2>/dev/null || modprobe kvm_intel 2>/dev/null || true
fi

if [ ! -e /dev/kvm ]; then
    err "/dev/kvm not found. KVM is required for Firecracker. Ensure hardware virtualization (nested virt on cloud VMs) is enabled."
fi

# Ensure /dev/kvm is accessible
chmod 666 /dev/kvm
log "KVM available: $(kvm-ok 2>&1 | head -1)"

# ── 3. Install Firecracker ───────────────────────────────────────────────────

ARCH=$(uname -m)
if [ "$ARCH" = "x86_64" ]; then
    FC_ARCH="x86_64"
elif [ "$ARCH" = "aarch64" ]; then
    FC_ARCH="aarch64"
else
    err "Unsupported architecture: $ARCH"
fi

if [ -f /usr/local/bin/firecracker ]; then
    CURRENT_FC=$(/usr/local/bin/firecracker --version 2>&1 | head -1 || echo "unknown")
    log "Firecracker already installed: $CURRENT_FC"
else
    log "Installing Firecracker ${FIRECRACKER_VERSION}..."
    FC_TARBALL="firecracker-${FIRECRACKER_VERSION}-${FC_ARCH}.tgz"
    FC_URL="https://github.com/firecracker-microvm/firecracker/releases/download/${FIRECRACKER_VERSION}/${FC_TARBALL}"

    cd /tmp
    curl -sL "$FC_URL" -o "$FC_TARBALL"
    tar xzf "$FC_TARBALL"

    FC_DIR="release-${FIRECRACKER_VERSION}-${FC_ARCH}"
    cp "${FC_DIR}/firecracker-${FIRECRACKER_VERSION}-${FC_ARCH}" /usr/local/bin/firecracker
    cp "${FC_DIR}/jailer-${FIRECRACKER_VERSION}-${FC_ARCH}" /usr/local/bin/jailer
    chmod +x /usr/local/bin/firecracker /usr/local/bin/jailer

    rm -rf "$FC_TARBALL" "$FC_DIR"
    log "Firecracker installed: $(/usr/local/bin/firecracker --version 2>&1 | head -1)"
fi

# ── 4. Install Google Cloud SDK (for GCS artifact downloads) ─────────────────

if ! command -v gsutil &>/dev/null; then
    log "Installing Google Cloud SDK..."
    curl -sS https://packages.cloud.google.com/apt/doc/apt-key.gpg | \
        gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg
    echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" \
        > /etc/apt/sources.list.d/google-cloud-sdk.list
    apt-get update -qq
    apt-get install -y -qq google-cloud-cli > /dev/null
    log "gsutil installed: $(gsutil version 2>&1 | head -1)"
else
    log "gsutil already installed: $(gsutil version 2>&1 | head -1)"
fi

# ── 5. Configure OCM storage and create directory structure ──────────────────

configure_ocm_storage

log "Creating OCM directory structure..."
mkdir -p \
    "${OCM_BASE}/images" \
    "${OCM_BASE}/sockets" \
    "${OCM_BASE}/data" \
    /etc/ocm-agent

configure_vm_state_storage
configure_browser_storage
configure_browser_rootfs_default

if [ -n "$OCM_BROWSER_XFS_DEVICE" ]; then
    mkdir -p "${OCM_BROWSER_BASE}/browser-rootfs"
    write_storage_env_handoff "$OCM_BROWSER_BASE"
    upsert_agent_env "BROWSER_STATE_DIR" "$OCM_BROWSER_BASE"
fi

OCM_FS_TYPE="$(findmnt -T "$OCM_BASE" -n -o FSTYPE 2>/dev/null || echo unknown)"
OCM_MOUNT_POINT="$(findmnt -T "$OCM_BASE" -n -o TARGET 2>/dev/null || echo unknown)"
OCM_REFLINK="$(probe_reflink_dir "$OCM_BASE" || true)"
log "OCM storage: path=${OCM_BASE} mount=${OCM_MOUNT_POINT} fs=${OCM_FS_TYPE} reflink=${OCM_REFLINK}"
OCM_STATE_FS_TYPE="$(findmnt -T "$OCM_STATE_DIR" -n -o FSTYPE 2>/dev/null || echo unknown)"
OCM_STATE_MOUNT_POINT="$(findmnt -T "$OCM_STATE_DIR" -n -o TARGET 2>/dev/null || echo unknown)"
OCM_STATE_REFLINK="$(probe_reflink_dir "$OCM_STATE_DIR" || true)"
log "VM state storage: path=${OCM_STATE_DIR} mount=${OCM_STATE_MOUNT_POINT} fs=${OCM_STATE_FS_TYPE} reflink=${OCM_STATE_REFLINK}"
if [ "$OCM_STATE_REFLINK" != "yes" ] && [ -z "$OCM_BROWSER_XFS_DEVICE" ]; then
    log "WARNING: ${OCM_STATE_DIR} does not support reflink. New/unpinned browser rootfs canaries will be blocked unless OCM_ALLOW_KERNEL_BROWSER_FULL_COPY=1 is set."
fi
if [ -n "$OCM_BROWSER_XFS_DEVICE" ]; then
    OCM_BROWSER_FS_TYPE="$(findmnt -T "$OCM_BROWSER_BASE" -n -o FSTYPE 2>/dev/null || echo unknown)"
    OCM_BROWSER_MOUNT_POINT="$(findmnt -T "$OCM_BROWSER_BASE" -n -o TARGET 2>/dev/null || echo unknown)"
    OCM_BROWSER_REFLINK="$(probe_reflink_dir "$OCM_BROWSER_BASE" || true)"
    log "Browser VM storage: path=${OCM_BROWSER_BASE} mount=${OCM_BROWSER_MOUNT_POINT} fs=${OCM_BROWSER_FS_TYPE} reflink=${OCM_BROWSER_REFLINK}"
    if [ "$OCM_BROWSER_REFLINK" != "yes" ]; then
        log "WARNING: ${OCM_BROWSER_BASE} does not support reflink. Browser rootfs canaries will be blocked unless OCM_ALLOW_KERNEL_BROWSER_FULL_COPY=1 is set."
    fi
else
    OCM_BROWSER_FS_TYPE="$OCM_STATE_FS_TYPE"
    OCM_BROWSER_MOUNT_POINT="$OCM_STATE_MOUNT_POINT"
    OCM_BROWSER_REFLINK="$OCM_STATE_REFLINK"
fi

# ── 6. Download kernel ────────────────────────────────────────────────────────

KERNEL_PATH="${OCM_BASE}/vmlinux"
if [ -f "$KERNEL_PATH" ]; then
    log "Kernel already present at ${KERNEL_PATH}"
else
    log "Downloading Firecracker-compatible kernel from ${KERNEL_GCS_PATH}..."
    if gsutil cp "$KERNEL_GCS_PATH" "$KERNEL_PATH" 2>/dev/null; then
        chmod 644 "$KERNEL_PATH"
        log "Kernel downloaded to ${KERNEL_PATH} ($(du -h "$KERNEL_PATH" | cut -f1))"
    else
        log "WARNING: Could not download kernel from ${KERNEL_GCS_PATH}. You'll need to supply one:"
        log "  gsutil cp ${KERNEL_GCS_PATH} ${KERNEL_PATH}"
        log "  (Requires read access to the artifact bucket, or build your own — see docs/getting-started.md)"
    fi
fi

# ── 7. Install cloudflared ────────────────────────────────────────────────────

if [ -f /usr/local/bin/cloudflared ]; then
    log "cloudflared already installed: $(/usr/local/bin/cloudflared --version 2>&1 | head -1)"
else
    log "Installing cloudflared..."
    if [ "$FC_ARCH" = "aarch64" ]; then CF_ARCH="arm64"; else CF_ARCH="amd64"; fi
    curl -sL "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${CF_ARCH}" \
        -o /usr/local/bin/cloudflared
    chmod +x /usr/local/bin/cloudflared
    log "cloudflared installed: $(/usr/local/bin/cloudflared --version 2>&1 | head -1)"
fi

# ── 8. Sysctl tuning ─────────────────────────────────────────────────────────

log "Configuring sysctl for IP forwarding and networking..."
cat > /etc/sysctl.d/99-ocm.conf <<'SYSCTL'
# IP forwarding for Firecracker VM NAT
net.ipv4.ip_forward = 1

# Increase conntrack table for many VMs
net.netfilter.nf_conntrack_max = 131072

# Increase file descriptor limits
fs.file-max = 1048576

# Increase inotify limits for many VMs
fs.inotify.max_user_instances = 8192
fs.inotify.max_user_watches = 524288
SYSCTL

sysctl --system > /dev/null 2>&1

# ── 9. Increase system limits ────────────────────────────────────────────────

log "Setting file descriptor limits..."
cat > /etc/security/limits.d/99-ocm.conf <<'LIMITS'
*    soft    nofile    65536
*    hard    nofile    1048576
root soft    nofile    65536
root hard    nofile    1048576
LIMITS

# ── 10. Ensure KVM module loads on boot ───────────────────────────────────────

log "Ensuring KVM module loads on boot..."
if grep -q 'AMD' /proc/cpuinfo; then
    echo "kvm_amd" > /etc/modules-load.d/kvm.conf
    # Enable nested virtualization if not already
    if [ -f /sys/module/kvm_amd/parameters/nested ]; then
        echo "options kvm_amd nested=1" > /etc/modprobe.d/kvm-nested.conf
    fi
else
    echo "kvm_intel" > /etc/modules-load.d/kvm.conf
    if [ -f /sys/module/kvm_intel/parameters/nested ]; then
        echo "options kvm_intel nested=1" > /etc/modprobe.d/kvm-nested.conf
    fi
fi

# ── Summary ───────────────────────────────────────────────────────────────────

echo ""
echo "════════════════════════════════════════════════════════════"
log "Host provisioning complete!"
echo ""
echo "  Firecracker: $(/usr/local/bin/firecracker --version 2>&1 | head -1)"
echo "  cloudflared: $(/usr/local/bin/cloudflared --version 2>&1 | head -1)"
echo "  KVM:         $([ -e /dev/kvm ] && echo 'available' || echo 'NOT FOUND')"
echo "  Kernel:      $([ -f "${OCM_BASE}/vmlinux" ] && echo 'present' || echo 'NOT YET DOWNLOADED')"
echo "  gsutil:      $(gsutil version 2>&1 | head -1)"
echo "  OCM base:    ${OCM_MOUNT_POINT} ${OCM_FS_TYPE} reflink=${OCM_REFLINK}"
echo "  VM state:    ${OCM_STATE_MOUNT_POINT} ${OCM_STATE_FS_TYPE} reflink=${OCM_STATE_REFLINK}"
echo "  Browser VM:  ${OCM_BROWSER_MOUNT_POINT} ${OCM_BROWSER_FS_TYPE} reflink=${OCM_BROWSER_REFLINK}"
echo "  Directories: ${OCM_BASE}/{images,sockets,data} state=${OCM_STATE_DIR}"
if [ -n "$OCM_BROWSER_XFS_DEVICE" ]; then
echo "  Agent env:   set BROWSER_STATE_DIR=${OCM_BROWSER_BASE}"
fi
echo ""
echo "Next steps:"
echo "  1. Enroll this host with the control plane:"
echo "     curl -sL <BACKEND_URL>/api/agent/install | sudo bash -s -- <ENROLLMENT_TOKEN>"
echo "  2. Download the agent binary:"
echo "     sudo gsutil cp ${OCM_ARTIFACT_BUCKET}/agent/ocm-agent /usr/local/bin/ocm-agent"
echo "     sudo chmod +x /usr/local/bin/ocm-agent"
echo "  3. Start the agent:"
echo "     sudo systemctl daemon-reload"
echo "     sudo systemctl enable --now ocm-agent"
echo "════════════════════════════════════════════════════════════"
