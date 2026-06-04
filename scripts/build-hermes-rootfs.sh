#!/bin/bash
# Build Hermes rootfs for Firecracker MicroVMs.

set -euo pipefail

IMAGES_DIR="${IMAGES_DIR:-/var/lib/ocm/images}"
ROOTFS_FILE="${ROOTFS_FILE:-hermes-rootfs.ext4}"
IMAGE_NAME="${HERMES_ROOTFS_IMAGE_NAME:-ocm-hermes-rootfs}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="${SCRIPT_DIR}/.."
BUFFER_MB="${ROOTFS_BUFFER_MB:-2048}"

command -v docker >/dev/null 2>&1 || { echo "ERROR: docker is not installed"; exit 1; }
MKFS_EXT4="$(command -v mkfs.ext4 2>/dev/null || echo /usr/sbin/mkfs.ext4)"
[ -x "$MKFS_EXT4" ] || { echo "ERROR: mkfs.ext4 is not installed"; exit 1; }
command -v bsdtar >/dev/null 2>&1 || { echo "ERROR: bsdtar is not installed"; exit 1; }

if docker info >/dev/null 2>&1; then
    DOCKER="docker"
else
    DOCKER="sudo docker"
fi

TMP_DIR="$(mktemp -d)"
CONTAINER_ID=""
cleanup() {
    sudo umount "${TMP_DIR}/mnt" 2>/dev/null || true
    if [ -n "$CONTAINER_ID" ]; then
        $DOCKER rm "$CONTAINER_ID" >/dev/null 2>&1 || true
    fi
    rm -rf "$TMP_DIR"
}
trap cleanup EXIT

mkdir -p "$IMAGES_DIR"
mkdir -p "${TMP_DIR}/bin"

resolve_guest_bin() {
    local bin=$1
    local cmd_dir="${PROJECT_ROOT}/backend/cmd/${bin}"
    local out="${TMP_DIR}/bin/${bin}"
    if command -v go >/dev/null 2>&1 && [ -d "$cmd_dir" ]; then
        (cd "${PROJECT_ROOT}/backend" && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -o "$out" "./cmd/${bin}")
        echo "$out"
        return
    fi
    if [ -f "/usr/local/bin/${bin}" ]; then
        echo "/usr/local/bin/${bin}"
        return
    fi
    echo "ERROR: ${bin} not found and could not be built from ${cmd_dir}" >&2
    return 1
}

DOCKER_BUILDKIT=1 $DOCKER build -t "$IMAGE_NAME" -f "${PROJECT_ROOT}/rootfs/Dockerfile.hermes" "${PROJECT_ROOT}/rootfs"

CONTAINER_ID="$($DOCKER create "$IMAGE_NAME")"
EXPORT_TAR="${TMP_DIR}/rootfs-export.tar"
$DOCKER export "$CONTAINER_ID" > "$EXPORT_TAR"
$DOCKER rm "$CONTAINER_ID" >/dev/null
CONTAINER_ID=""

EXPORT_SIZE_BYTES="$(stat --format='%s' "$EXPORT_TAR" 2>/dev/null || stat -f '%z' "$EXPORT_TAR")"
EXPORT_SIZE_MB=$((EXPORT_SIZE_BYTES / 1024 / 1024))
ROOTFS_SIZE_MB=$((EXPORT_SIZE_MB + BUFFER_MB))

truncate -s "${ROOTFS_SIZE_MB}M" "${TMP_DIR}/${ROOTFS_FILE}"
"$MKFS_EXT4" -F -m 1 "${TMP_DIR}/${ROOTFS_FILE}"
mkdir -p "${TMP_DIR}/mnt"
sudo mount "${TMP_DIR}/${ROOTFS_FILE}" "${TMP_DIR}/mnt"
sudo bsdtar -xf "$EXPORT_TAR" -C "${TMP_DIR}/mnt"

sudo rm -f "${TMP_DIR}/mnt/sbin/overlay-init"
sudo cp "${SCRIPT_DIR}/init-hermes.sh" "${TMP_DIR}/mnt/sbin/overlay-init"
sudo chmod +x "${TMP_DIR}/mnt/sbin/overlay-init"

for bin in ocmptyd authproxy ocm-secrets ocm-hermes-config; do
    bin_path="$(resolve_guest_bin "$bin")"
    sudo cp "$bin_path" "${TMP_DIR}/mnt/usr/local/bin/${bin}"
    sudo chmod 755 "${TMP_DIR}/mnt/usr/local/bin/${bin}"
done

for script in ocm-metadata ocm-env ocm-status cf-ssh-check; do
    if [ -f "${SCRIPT_DIR}/${script}" ]; then
        if [ "$script" = "cf-ssh-check" ]; then
            sudo mkdir -p "${TMP_DIR}/mnt/etc/ssh"
            sudo cp "${SCRIPT_DIR}/${script}" "${TMP_DIR}/mnt/etc/ssh/${script}"
            sudo chmod 755 "${TMP_DIR}/mnt/etc/ssh/${script}"
        else
            sudo cp "${SCRIPT_DIR}/${script}" "${TMP_DIR}/mnt/usr/local/bin/${script}"
            sudo chmod 755 "${TMP_DIR}/mnt/usr/local/bin/${script}"
        fi
    fi
done

echo '{"kind":"hermes-rootfs","data_version":1}' | sudo tee "${TMP_DIR}/mnt/etc/ocm-versions.json" >/dev/null
sudo mkdir -p "${TMP_DIR}/mnt/var/log"
sudo sync
sudo umount "${TMP_DIR}/mnt"

sudo mv "${TMP_DIR}/${ROOTFS_FILE}" "${IMAGES_DIR}/${ROOTFS_FILE}"
sudo chown "$(id -u):$(id -g)" "${IMAGES_DIR}/${ROOTFS_FILE}" 2>/dev/null || true

echo "Built ${IMAGES_DIR}/${ROOTFS_FILE}"
