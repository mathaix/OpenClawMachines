#!/bin/bash
# Build browser companion rootfs for Firecracker MicroVMs
# Creates browser-rootfs.ext4 with Alpine + headless Chromium
#
# Must be run on a Linux machine with Docker, mkfs.ext4, and bsdtar installed.

set -euo pipefail

# Configuration
IMAGES_DIR="/var/lib/ocm/images"
ROOTFS_FILE="browser-rootfs.ext4"
IMAGE_NAME="ocm-browser-rootfs"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="${SCRIPT_DIR}/.."
BUFFER_MB=256  # 256MB buffer (browser rootfs is much smaller)

# --- Sanity Checks ---
command -v docker >/dev/null 2>&1 || { echo >&2 "Docker is not installed. Aborting."; exit 1; }
MKFS_EXT4=$(command -v mkfs.ext4 2>/dev/null || echo "/usr/sbin/mkfs.ext4")
[ -x "$MKFS_EXT4" ] || { echo >&2 "mkfs.ext4 is not installed (e.g., e2fsprogs). Aborting."; exit 1; }
command -v bsdtar >/dev/null 2>&1 || { echo >&2 "bsdtar (libarchive-tools) is not installed. Aborting."; exit 1; }

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
mkdir -p "${IMAGES_DIR}"

# --- Build Docker Image ---
echo ""
echo "=========================================="
echo "Building Docker image: ${IMAGE_NAME}"
echo "=========================================="
$DOCKER build -t ${IMAGE_NAME} -f "${PROJECT_ROOT}/rootfs/Dockerfile.browser" "${PROJECT_ROOT}/rootfs"

# --- Calculate Dynamic Size ---
echo ""
echo "Calculating rootfs size from Docker image..."
CONTAINER_ID=$($DOCKER create ${IMAGE_NAME})
EXPORT_SIZE_BYTES=$($DOCKER export "${CONTAINER_ID}" | wc -c)
EXPORT_SIZE_MB=$((EXPORT_SIZE_BYTES / 1024 / 1024))
ROOTFS_SIZE_MB=$((EXPORT_SIZE_MB + BUFFER_MB))
echo "Docker export size: ${EXPORT_SIZE_MB}MB + ${BUFFER_MB}MB buffer = ${ROOTFS_SIZE_MB}MB total"

# --- Create RootFS Disk Image ---
echo ""
echo "Creating ext4 file: ${ROOTFS_FILE} (size: ${ROOTFS_SIZE_MB}MB)..."
truncate -s "${ROOTFS_SIZE_MB}M" "${TMP_DIR}/${ROOTFS_FILE}"
"$MKFS_EXT4" -F -m 1 "${TMP_DIR}/${ROOTFS_FILE}"

# --- Mount RootFS ---
mkdir -p "${TMP_DIR}/mnt"
sudo mount "${TMP_DIR}/${ROOTFS_FILE}" "${TMP_DIR}/mnt"
echo "Mounted ${ROOTFS_FILE} to ${TMP_DIR}/mnt"

# --- Export Docker Container Filesystem ---
echo ""
echo "Exporting Docker container to rootfs..."
$DOCKER export "${CONTAINER_ID}" | sudo bsdtar -xf - -C "${TMP_DIR}/mnt"
$DOCKER rm "${CONTAINER_ID}" >/dev/null
CONTAINER_ID=""

# --- Inject init script for Firecracker ---
INIT_TARGET="/sbin/overlay-init"
INIT_SOURCE="${SCRIPT_DIR}/init-browser.sh"

if [ ! -f "$INIT_SOURCE" ]; then
    echo "ERROR: Init script not found at $INIT_SOURCE"
    exit 1
fi

sudo rm -f "${TMP_DIR}/mnt/${INIT_TARGET}"
sudo cp "$INIT_SOURCE" "${TMP_DIR}/mnt/${INIT_TARGET}"
sudo chmod +x "${TMP_DIR}/mnt/${INIT_TARGET}"
echo "Injected init script from $INIT_SOURCE → ${INIT_TARGET}"

# --- Finalize ---
sudo umount "${TMP_DIR}/mnt"
echo "Unmounted ${ROOTFS_FILE}."
mv "${TMP_DIR}/${ROOTFS_FILE}" "${IMAGES_DIR}/${ROOTFS_FILE}"
echo "Moved ${ROOTFS_FILE} to ${IMAGES_DIR}/"

echo ""
echo "=========================================="
echo "Browser rootfs build complete!"
echo "Output: ${IMAGES_DIR}/${ROOTFS_FILE}"
echo "=========================================="
