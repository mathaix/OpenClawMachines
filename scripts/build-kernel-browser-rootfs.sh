#!/bin/bash
# Build a Kernel Images based browser rootfs for Firecracker microVMs.
#
# Creates kernel-browser-rootfs.ext4 from the Clara Kernel Images fork or from
# an already-built Docker image. This is intentionally separate from the
# existing browser-rootfs.ext4 image.
#
# Usage:
#   KERNEL_IMAGES_DIR=../ocm-kernel-images sudo -E bash scripts/build-kernel-browser-rootfs.sh
#   KERNEL_BROWSER_IMAGE=ocm-kernel-browser-rootfs sudo -E bash scripts/build-kernel-browser-rootfs.sh
#
# Must be run on a Linux machine with Docker, mkfs.ext4, and bsdtar installed.

set -euo pipefail

IMAGES_DIR="${IMAGES_DIR:-/var/lib/ocm/images}"
ROOTFS_FILE="${ROOTFS_FILE:-kernel-browser-rootfs.ext4}"
IMAGE_NAME="${KERNEL_BROWSER_IMAGE:-ocm-kernel-browser-rootfs}"
BUFFER_MB="${BUFFER_MB:-1024}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="${SCRIPT_DIR}/.."
DEFAULT_KERNEL_IMAGES_DIR="${PROJECT_ROOT}/../ocm-kernel-images"
KERNEL_IMAGES_DIR="${KERNEL_IMAGES_DIR:-${DEFAULT_KERNEL_IMAGES_DIR}}"

[ "$(uname -s)" = "Linux" ] || { echo >&2 "This script must run on Linux because it creates and mounts an ext4 rootfs."; exit 1; }
command -v docker >/dev/null 2>&1 || { echo >&2 "Docker is not installed. Aborting."; exit 1; }
MKFS_EXT4=$(command -v mkfs.ext4 2>/dev/null || echo "/usr/sbin/mkfs.ext4")
[ -x "$MKFS_EXT4" ] || { echo >&2 "mkfs.ext4 is not installed (e.g., e2fsprogs). Aborting."; exit 1; }
command -v bsdtar >/dev/null 2>&1 || { echo >&2 "bsdtar (libarchive-tools) is not installed. Aborting."; exit 1; }

if docker info &>/dev/null; then
    DOCKER="docker"
else
    echo "Using sudo for docker commands (user not in docker group)"
    DOCKER="sudo docker"
fi

TMP_DIR=$(mktemp -d)
echo "Created temporary directory: ${TMP_DIR}"

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

mkdir -p "${IMAGES_DIR}"

image_exists() {
    $DOCKER image inspect "$IMAGE_NAME" >/dev/null 2>&1
}

# Build-or-reuse policy:
#
# If the kernel-images source tree is present, ALWAYS call build-docker.sh
# — Docker BuildKit's layer cache already handles "skip if nothing changed"
# based on content hashes, so a rerun with no source changes is fast
# (layer-cache hit on every stage) and a rerun WITH source changes actually
# rebuilds the affected layers. Previously this script short-circuited on
# `image_exists`, which silently reused a stale image whenever anyone
# edited the kernel-images tree between builds. That masked real rootfs
# changes (e.g. the OCM orb animation in the Neko client) and led to
# "why does my edit not show up" surprises.
#
# Only fall back to reusing an existing image when the source tree is
# NOT present — that preserves the "operator pulled a prebuilt image from
# a registry, doesn't have the source" use case.
BUILD_SCRIPT="${KERNEL_IMAGES_DIR}/images/chromium-headful/build-docker.sh"
if [ -x "$BUILD_SCRIPT" ]; then
    echo ""
    echo "=========================================="
    echo "Building Kernel Images Docker image: ${IMAGE_NAME}"
    echo "Source: ${KERNEL_IMAGES_DIR}"
    echo "(BuildKit layer cache handles fast-path when nothing changed)"
    echo "=========================================="
    (
        cd "${KERNEL_IMAGES_DIR}/images/chromium-headful"
        IMAGE="${IMAGE_NAME}" ./build-docker.sh
    )
elif image_exists; then
    echo "Kernel Images source tree not found at ${KERNEL_IMAGES_DIR}."
    echo "Using existing Docker image: ${IMAGE_NAME}"
else
    echo "ERROR: Kernel Images build script not found or not executable: ${BUILD_SCRIPT}"
    echo "And no prebuilt image '${IMAGE_NAME}' is available locally."
    echo ""
    echo "Clone/fetch the Clara fork, then rerun:"
    echo "  git clone https://github.com/mathaix/ocm-kernel-images.git ${KERNEL_IMAGES_DIR}"
    echo "  KERNEL_IMAGES_DIR=${KERNEL_IMAGES_DIR} sudo -E bash scripts/build-kernel-browser-rootfs.sh"
    echo ""
    echo "Or pull a prebuilt image and rerun with:"
    echo "  KERNEL_BROWSER_IMAGE=${IMAGE_NAME} sudo -E bash scripts/build-kernel-browser-rootfs.sh"
    exit 1
fi

echo ""
echo "Calculating rootfs size from Docker image..."
CONTAINER_ID=$($DOCKER create "${IMAGE_NAME}")
EXPORT_SIZE_BYTES=$($DOCKER export "${CONTAINER_ID}" | wc -c)
EXPORT_SIZE_MB=$((EXPORT_SIZE_BYTES / 1024 / 1024))
ROOTFS_SIZE_MB=$((EXPORT_SIZE_MB + BUFFER_MB))
echo "Docker export size: ${EXPORT_SIZE_MB}MB + ${BUFFER_MB}MB buffer = ${ROOTFS_SIZE_MB}MB total"

echo ""
echo "Creating ext4 file: ${ROOTFS_FILE} (size: ${ROOTFS_SIZE_MB}MB)..."
truncate -s "${ROOTFS_SIZE_MB}M" "${TMP_DIR}/${ROOTFS_FILE}"
"$MKFS_EXT4" -F -m 1 "${TMP_DIR}/${ROOTFS_FILE}"

mkdir -p "${TMP_DIR}/mnt"
sudo mount "${TMP_DIR}/${ROOTFS_FILE}" "${TMP_DIR}/mnt"
echo "Mounted ${ROOTFS_FILE} to ${TMP_DIR}/mnt"

echo ""
echo "Exporting Docker container to rootfs..."
$DOCKER export "${CONTAINER_ID}" | sudo bsdtar -xf - -C "${TMP_DIR}/mnt"
$DOCKER rm "${CONTAINER_ID}" >/dev/null
CONTAINER_ID=""

INIT_TARGET="/sbin/overlay-init"
INIT_SOURCE="${SCRIPT_DIR}/init-kernel-browser.sh"

if [ ! -f "$INIT_SOURCE" ]; then
    echo "ERROR: Init script not found at $INIT_SOURCE"
    exit 1
fi

sudo rm -f "${TMP_DIR}/mnt/${INIT_TARGET}"
sudo cp "$INIT_SOURCE" "${TMP_DIR}/mnt/${INIT_TARGET}"
sudo chmod +x "${TMP_DIR}/mnt/${INIT_TARGET}"
echo "Injected init script from $INIT_SOURCE -> ${INIT_TARGET}"

sudo mkdir -p "${TMP_DIR}/mnt/etc/ocm"
sudo tee "${TMP_DIR}/mnt/etc/ocm/browser-runtime" >/dev/null <<EOF
runtime=kernel-images
source=https://github.com/mathaix/ocm-kernel-images
image=${IMAGE_NAME}
EOF

sudo umount "${TMP_DIR}/mnt"
echo "Unmounted ${ROOTFS_FILE}."
mv "${TMP_DIR}/${ROOTFS_FILE}" "${IMAGES_DIR}/${ROOTFS_FILE}"
echo "Moved ${ROOTFS_FILE} to ${IMAGES_DIR}/"

echo ""
echo "=========================================="
echo "Kernel browser rootfs build complete!"
echo "Output: ${IMAGES_DIR}/${ROOTFS_FILE}"
echo "=========================================="
