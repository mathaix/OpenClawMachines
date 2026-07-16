#!/bin/bash
# Build OpenClaw rootfs for Firecracker MicroVMs
# Creates rootfs.ext4 with the OpenClaw gateway + Playwright
#
# Must be run on a Linux machine with Docker, mkfs.ext4, and bsdtar installed.
# Called by: create-snapshot.sh --full (on GCP VM)

set -euo pipefail

# Configuration
IMAGES_DIR="/var/lib/ocm/images"
ROOTFS_FILE="rootfs.ext4"
IMAGE_NAME="ocm-rootfs"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="${SCRIPT_DIR}/.."
BUFFER_MB=2048  # 2GB buffer for swap file (1GB) + runtime scratch space

# --- Sanity Checks ---
command -v docker >/dev/null 2>&1 || { echo >&2 "Docker is not installed. Aborting."; exit 1; }
MKFS_EXT4=$(command -v mkfs.ext4 2>/dev/null || echo "/usr/sbin/mkfs.ext4")
[ -x "$MKFS_EXT4" ] || { echo >&2 "mkfs.ext4 is not installed (e.g., e2fsprogs). Aborting."; exit 1; }
command -v bsdtar >/dev/null 2>&1 || { echo >&2 "bsdtar (libarchive-tools) is not installed. Aborting."; exit 1; }
docker buildx version >/dev/null 2>&1 || { echo >&2 "docker buildx is not installed (build-rootfs forces DOCKER_BUILDKIT=1). Install the docker-buildx plugin. Aborting."; exit 1; }
command -v strings >/dev/null 2>&1 || { echo >&2 "strings is not installed (part of binutils). Aborting."; exit 1; }

# Use sudo for docker if user is not in docker group
if docker info &>/dev/null; then
    DOCKER="docker"
else
    echo "Using sudo for docker commands (user not in docker group)"
    DOCKER="sudo docker"
fi

# --- Create Temporary Directory ---
TMP_DIR=$(mktemp -d)
echo "Created temporary directory: ${TMP_DIR}"

# Ensure cleanup on exit
CONTAINER_ID=""
cleanup() {
    echo "Cleaning up..."
    sudo umount "${TMP_DIR}/mnt" 2>/dev/null || true
    if [ -n "${CONTAINER_ID}" ]; then
        $DOCKER rm "${CONTAINER_ID}" 2>/dev/null || true
    fi
    rm -rf "${TMP_DIR}"
    echo "Cleanup complete."
}
trap cleanup EXIT

# --- Ensure images directory exists ---
# sudo: IMAGES_DIR lives under /var/lib/ocm, created root-owned by
# provision-host.sh. The build otherwise runs as an unprivileged user with sudo
# for privileged steps (matching the final mv below).
sudo mkdir -p "${IMAGES_DIR}"

# --- Build Docker Image ---
echo ""
echo "=========================================="
echo "Building Docker image: ${IMAGE_NAME}"
echo "=========================================="
DOCKER_BUILDKIT=1 $DOCKER build -t ${IMAGE_NAME} -f "${PROJECT_ROOT}/rootfs/Dockerfile.openclaw" "${PROJECT_ROOT}/rootfs"

# --- Calculate Dynamic Size ---
# Create container once, export to a temp tar, then extract.
# docker image inspect size is compressed/deduplicated and much smaller than
# the actual exported filesystem — must use real export size.
echo ""
echo "Creating container and exporting filesystem..."
CONTAINER_ID=$($DOCKER create ${IMAGE_NAME})
EXPORT_TAR="${TMP_DIR}/rootfs-export.tar"
$DOCKER export "${CONTAINER_ID}" > "${EXPORT_TAR}"
$DOCKER rm "${CONTAINER_ID}" >/dev/null
CONTAINER_ID=""

EXPORT_SIZE_BYTES=$(stat --format='%s' "${EXPORT_TAR}" 2>/dev/null || stat -f '%z' "${EXPORT_TAR}")
EXPORT_SIZE_MB=$((EXPORT_SIZE_BYTES / 1024 / 1024))
ROOTFS_SIZE_MB=$((EXPORT_SIZE_MB + BUFFER_MB))
echo "Export size: ${EXPORT_SIZE_MB}MB + ${BUFFER_MB}MB buffer = ${ROOTFS_SIZE_MB}MB total"

# --- Create RootFS Disk Image ---
echo ""
echo "Creating ext4 file: ${ROOTFS_FILE} (size: ${ROOTFS_SIZE_MB}MB)..."
truncate -s "${ROOTFS_SIZE_MB}M" "${TMP_DIR}/${ROOTFS_FILE}"
# -m 1: Only 1% reserved blocks (default 5% wastes space)
"$MKFS_EXT4" -F -m 1 "${TMP_DIR}/${ROOTFS_FILE}"

# --- Mount RootFS ---
mkdir -p "${TMP_DIR}/mnt"
sudo mount "${TMP_DIR}/${ROOTFS_FILE}" "${TMP_DIR}/mnt"
echo "Mounted ${ROOTFS_FILE} to ${TMP_DIR}/mnt"
echo "Filesystem space after mkfs:"
df -h "${TMP_DIR}/mnt"

# --- Extract Docker Container Filesystem ---
echo ""
echo "Extracting container filesystem to rootfs..."
sudo bsdtar -xf "${EXPORT_TAR}" -C "${TMP_DIR}/mnt"
rm -f "${EXPORT_TAR}"
echo "Filesystem space after extraction:"
df -h "${TMP_DIR}/mnt"

# --- Inject init script for Firecracker ---
# The orchestrator boots with init=/sbin/overlay-init (kernel args)
INIT_TARGET="/sbin/overlay-init"
INIT_SOURCE="${SCRIPT_DIR}/init-openclaw.sh"

if [ ! -f "$INIT_SOURCE" ]; then
    echo "ERROR: Init script not found at $INIT_SOURCE"
    exit 1
fi

# Remove existing file if present (may be a symlink or binary from base image)
sudo rm -f "${TMP_DIR}/mnt/${INIT_TARGET}"
sudo cp "$INIT_SOURCE" "${TMP_DIR}/mnt/${INIT_TARGET}"
sudo chmod +x "${TMP_DIR}/mnt/${INIT_TARGET}"
echo "Injected init script from $INIT_SOURCE → ${INIT_TARGET}"

# --- Inject PTY daemon binary for terminal access ---
OCMPTYD_HOST="/usr/local/bin/ocmptyd"
OCMPTYD_TARGET="/usr/local/bin/ocmptyd"

if [ -f "$OCMPTYD_HOST" ]; then
    sudo cp "$OCMPTYD_HOST" "${TMP_DIR}/mnt/${OCMPTYD_TARGET}"
    sudo chmod +x "${TMP_DIR}/mnt/${OCMPTYD_TARGET}"
    echo "Injected PTY daemon binary from $OCMPTYD_HOST → ${OCMPTYD_TARGET}"
else
    echo "FATAL: PTY daemon binary not found at $OCMPTYD_HOST - PTY server will not work"
    exit 1
fi

# --- Inject auth proxy binary for per-VM tunnel authentication ---
AUTHPROXY_HOST="/usr/local/bin/authproxy"
AUTHPROXY_TARGET="/usr/local/bin/authproxy"

if [ -f "$AUTHPROXY_HOST" ]; then
    sudo cp "$AUTHPROXY_HOST" "${TMP_DIR}/mnt/${AUTHPROXY_TARGET}"
    sudo chmod +x "${TMP_DIR}/mnt/${AUTHPROXY_TARGET}"
    echo "Injected authproxy binary from $AUTHPROXY_HOST → ${AUTHPROXY_TARGET}"
else
    echo "FATAL: Auth proxy binary not found at $AUTHPROXY_HOST - per-VM tunnels will not work"
    exit 1
fi

# --- Inject ocm-secrets binary for native config mode ---
OCM_SECRETS_HOST="/usr/local/bin/ocm-secrets"
OCM_SECRETS_TARGET="/usr/local/bin/ocm-secrets"

if [ -f "$OCM_SECRETS_HOST" ]; then
    sudo cp "$OCM_SECRETS_HOST" "${TMP_DIR}/mnt/${OCM_SECRETS_TARGET}"
    sudo chmod 755 "${TMP_DIR}/mnt/${OCM_SECRETS_TARGET}"
    sudo chown root:root "${TMP_DIR}/mnt/${OCM_SECRETS_TARGET}"
    echo "Injected ocm-secrets binary from $OCM_SECRETS_HOST → ${OCM_SECRETS_TARGET}"
else
    echo "FATAL: ocm-secrets binary not found at $OCM_SECRETS_HOST - native config mode will not work"
    exit 1
fi

# --- Inject version file ---
# `|| true`: a locally built ocmptyd (plain `make build-ocmptyd`, no CI version
# ldflags) carries no version string, so grep exits non-zero. Without the guard,
# set -euo pipefail aborts the whole build here instead of falling through to the
# ${AGENT_VERSION:-unknown} default below.
AGENT_VERSION=$(strings "${TMP_DIR}/mnt/usr/local/bin/ocmptyd" | grep -oE '[a-f0-9]{7}-[0-9]{8}T[0-9]{6}Z' | head -1 || true)
echo "${AGENT_VERSION:-unknown}" | sudo tee "${TMP_DIR}/mnt/etc/ocm-agent-version" > /dev/null
echo "Injected version file: ${AGENT_VERSION:-unknown}"

# --- Inject log directory ---
sudo mkdir -p "${TMP_DIR}/mnt/var/log"

# --- Inject OCM utility scripts ---
for script in ocm-metadata ocm-test-llm ocm-env ocm-status debug-gateway-write.js; do
    if [ -f "${SCRIPT_DIR}/${script}" ]; then
        sudo cp "${SCRIPT_DIR}/${script}" "${TMP_DIR}/mnt/usr/local/bin/${script}"
        sudo chmod +x "${TMP_DIR}/mnt/usr/local/bin/${script}"
        echo "Injected ${script} → /usr/local/bin/${script}"
    fi
done

# cf-ssh-check goes to /etc/ssh/ (root-owned) — sshd's safe_path() requires every
# parent directory of AuthorizedPrincipalsCommand to be owned by root or the command
# user. /usr/local/bin may be chown'd to openclaw for npm, breaking safe_path().
if [ -f "${SCRIPT_DIR}/cf-ssh-check" ]; then
    sudo mkdir -p "${TMP_DIR}/mnt/etc/ssh"
    sudo cp "${SCRIPT_DIR}/cf-ssh-check" "${TMP_DIR}/mnt/etc/ssh/cf-ssh-check"
    sudo chmod 755 "${TMP_DIR}/mnt/etc/ssh/cf-ssh-check"
    echo "Injected cf-ssh-check → /etc/ssh/cf-ssh-check"
fi

# --- Inject version manifest ---
echo ""
echo "Generating /etc/ocm-versions.json..."
OCM_SNAPSHOT="${OCM_SNAPSHOT:-unknown}"
# Extract versions from rootfs binaries
if [ -z "${OCM_OPENCLAW_VERSION:-}" ]; then
    OCM_OPENCLAW_VERSION=$(sudo chroot "${TMP_DIR}/mnt" /bin/bash -c "PATH=/root/.local/share/pnpm:\$PATH openclaw --version 2>/dev/null" 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || echo "unknown")
fi
OCM_OPENCLAW_VERSION="${OCM_OPENCLAW_VERSION:-unknown}"
OCM_NODE_VERSION=$(sudo chroot "${TMP_DIR}/mnt" /bin/bash -c "node --version 2>/dev/null" | tr -d 'v' || echo "unknown")
OCM_CLOUDFLARED_VERSION=$(sudo chroot "${TMP_DIR}/mnt" /bin/bash -c "cloudflared --version 2>/dev/null" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || echo "unknown")
OCM_GH_VERSION=$(sudo chroot "${TMP_DIR}/mnt" /bin/bash -c "gh --version 2>/dev/null" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || echo "unknown")
OCM_PNPM_VERSION=$(sudo chroot "${TMP_DIR}/mnt" /bin/bash -c "pnpm --version 2>/dev/null" 2>/dev/null || echo "unknown")
OCM_DATA_VERSION=1
OCM_BUILT_AT=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# Write pretty-printed JSON so it's human-readable with cat
sudo python3 -c "
import json, sys
versions = {
    'snapshot': '${OCM_SNAPSHOT}',
    'built_at': '${OCM_BUILT_AT}',
    'openclaw': '${OCM_OPENCLAW_VERSION}',
    'agent': '${AGENT_VERSION:-unknown}',
    'node': '${OCM_NODE_VERSION}',
    'cloudflared': '${OCM_CLOUDFLARED_VERSION}',
    'gh': '${OCM_GH_VERSION}',
    'pnpm': '${OCM_PNPM_VERSION}',
    'data_version': ${OCM_DATA_VERSION}
}
json.dump(versions, sys.stdout, indent=2)
print()
" | sudo tee "${TMP_DIR}/mnt/etc/ocm-versions.json" > /dev/null
echo "Version manifest written:"
sudo cat "${TMP_DIR}/mnt/etc/ocm-versions.json"

# --- Inject migration runner and scripts ---
echo "Injecting ocm-migrate and migration scripts..."
sudo cp "${SCRIPT_DIR}/ocm-migrate" "${TMP_DIR}/mnt/usr/local/bin/ocm-migrate"
sudo chmod +x "${TMP_DIR}/mnt/usr/local/bin/ocm-migrate"
sudo mkdir -p "${TMP_DIR}/mnt/usr/local/lib/ocm-migrations"
if [ -d "${SCRIPT_DIR}/migrations" ]; then
    sudo cp "${SCRIPT_DIR}/migrations/"*.sh "${TMP_DIR}/mnt/usr/local/lib/ocm-migrations/" 2>/dev/null || true
    sudo chmod +x "${TMP_DIR}/mnt/usr/local/lib/ocm-migrations/"*.sh 2>/dev/null || true
fi
echo "Migration runner and scripts injected"

# --- Finalize ---
sudo umount "${TMP_DIR}/mnt"
echo "Unmounted ${ROOTFS_FILE}."
sudo mv "${TMP_DIR}/${ROOTFS_FILE}" "${IMAGES_DIR}/${ROOTFS_FILE}"
echo "Moved ${ROOTFS_FILE} to ${IMAGES_DIR}/"

echo ""
echo "=========================================="
echo "OpenClaw rootfs build complete!"
echo "Output: ${IMAGES_DIR}/${ROOTFS_FILE}"
echo "=========================================="
