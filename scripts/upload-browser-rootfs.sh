#!/bin/bash
# Upload browser rootfs to GCS for distribution to agents
#
# Usage: bash scripts/upload-browser-rootfs.sh [rootfs-path]
#
# Follows the same pattern as upload-rootfs.sh but for the browser companion VM.

set -euo pipefail

# Configuration
GCS_BUCKET="${GCS_BUCKET:-openclawmachines}"
GCS_PREFIX="${GCS_PREFIX:-browser-rootfs}"
ROOTFS_BASENAME="${ROOTFS_BASENAME:-browser-rootfs}"
ROOTFS_PATH="${1:-/var/lib/ocm/images/${ROOTFS_BASENAME}.ext4}"
ZSTD_LEVEL="${ZSTD_LEVEL:-3}"

# --- Sanity Checks ---
if [ ! -f "$ROOTFS_PATH" ]; then
    echo "ERROR: browser rootfs not found at $ROOTFS_PATH"
    echo "Usage: $0 [path-to-browser-rootfs.ext4]"
    exit 1
fi
command -v zstd >/dev/null 2>&1 || { echo "ERROR: zstd not installed. Install via: apt-get install zstd"; exit 1; }
command -v gsutil >/dev/null 2>&1 || { echo "ERROR: gsutil not installed. Install via: apt-get install google-cloud-cli"; exit 1; }

# --- Version Tag ---
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
TIMESTAMP=$(date -u +%Y%m%dT%H%M%SZ)
VERSION="${GIT_COMMIT}-${TIMESTAMP}"
echo "Version: ${VERSION}"

# --- Compute SHA256 ---
echo ""
echo "Computing SHA256 of uncompressed browser rootfs..."
SHA256=$(sha256sum "$ROOTFS_PATH" | awk '{print $1}')
SIZE_BYTES=$(stat --format='%s' "$ROOTFS_PATH" 2>/dev/null || stat -f '%z' "$ROOTFS_PATH")
echo "SHA256: ${SHA256}"
echo "Size: ${SIZE_BYTES} bytes ($(( SIZE_BYTES / 1024 / 1024 )) MB)"

# --- Compress with zstd ---
COMPRESSED_PATH="/tmp/${ROOTFS_BASENAME}-${VERSION}.ext4.zst"
echo ""
echo "Compressing with zstd -${ZSTD_LEVEL}..."
zstd -"${ZSTD_LEVEL}" -f -o "$COMPRESSED_PATH" "$ROOTFS_PATH"
COMPRESSED_SIZE=$(stat --format='%s' "$COMPRESSED_PATH" 2>/dev/null || stat -f '%z' "$COMPRESSED_PATH")
echo "Compressed: ${COMPRESSED_SIZE} bytes ($(( COMPRESSED_SIZE / 1024 / 1024 )) MB)"

# --- GCS Paths ---
GCS_ROOTFS="gs://${GCS_BUCKET}/${GCS_PREFIX}/${ROOTFS_BASENAME}-${VERSION}.ext4.zst"
GCS_MANIFEST_VERSIONED="gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest-${VERSION}.json"
GCS_MANIFEST_LATEST="gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest.json"
BUILT_AT=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# --- Generate Manifest ---
MANIFEST=$(cat <<EOF
{
  "version": "${VERSION}",
  "sha256": "${SHA256}",
  "size_bytes": ${SIZE_BYTES},
  "compressed_size_bytes": ${COMPRESSED_SIZE},
  "url": "${GCS_ROOTFS}",
  "built_at": "${BUILT_AT}",
  "compression": "zstd"
}
EOF
)
echo ""
echo "Manifest:"
echo "$MANIFEST"

# --- Upload to GCS ---
echo ""
echo "Uploading compressed browser rootfs to GCS..."
gsutil cp "$COMPRESSED_PATH" "$GCS_ROOTFS"

echo "Uploading versioned manifest..."
echo "$MANIFEST" | gsutil cp - "$GCS_MANIFEST_VERSIONED"

echo "Updating latest manifest..."
echo "$MANIFEST" | gsutil cp - "$GCS_MANIFEST_LATEST"

# --- Clean up ---
rm -f "$COMPRESSED_PATH"

echo ""
echo "=========================================="
echo "Browser rootfs uploaded successfully!"
echo "=========================================="
echo "Version:    ${VERSION}"
echo "GCS rootfs: ${GCS_ROOTFS}"
echo "Manifest:   ${GCS_MANIFEST_LATEST}"
echo "=========================================="
