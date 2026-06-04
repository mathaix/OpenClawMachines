#!/bin/bash
# Upload rootfs to GCS for distribution to agents
#
# Usage: bash scripts/upload-rootfs.sh [rootfs-path]
#
# This script:
# 1. Computes SHA256 of uncompressed rootfs.ext4
# 2. Compresses with zstd -3
# 3. Extracts version metadata from the rootfs
# 4. Generates manifest JSON
# 5. Uploads compressed rootfs + manifest to GCS
#
# Must be run from the project root.

set -euo pipefail

# Configuration
GCS_BUCKET="${GCS_BUCKET:-openclawmachines}"
GCS_PREFIX="${GCS_PREFIX:-rootfs}"
ROOTFS_PATH="${1:-/var/lib/ocm/images/rootfs.ext4}"
ZSTD_LEVEL="${ZSTD_LEVEL:-3}"

# --- Sanity Checks ---
if [ ! -f "$ROOTFS_PATH" ]; then
    echo "ERROR: rootfs not found at $ROOTFS_PATH"
    echo "Usage: $0 [path-to-rootfs.ext4]"
    exit 1
fi
command -v zstd >/dev/null 2>&1 || { echo "ERROR: zstd not installed. Install via: apt-get install zstd"; exit 1; }
command -v gsutil >/dev/null 2>&1 || { echo "ERROR: gsutil not installed. Install via: apt-get install google-cloud-cli"; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "ERROR: python3 not installed"; exit 1; }

# --- Version Tag ---
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
TIMESTAMP=$(date -u +%Y%m%dT%H%M%SZ)
VERSION="${GIT_COMMIT}-${TIMESTAMP}"
echo "Version: ${VERSION}"

# --- Compute SHA256 of uncompressed rootfs ---
echo ""
echo "Computing SHA256 of uncompressed rootfs..."
SHA256=$(sha256sum "$ROOTFS_PATH" | awk '{print $1}')
SIZE_BYTES=$(stat --format='%s' "$ROOTFS_PATH" 2>/dev/null || stat -f '%z' "$ROOTFS_PATH")
echo "SHA256: ${SHA256}"
echo "Size: ${SIZE_BYTES} bytes ($(( SIZE_BYTES / 1024 / 1024 )) MB)"

# --- Compress with zstd (multi-threaded) ---
# Use /tmp for compressed output since rootfs dir may be root-owned
COMPRESSED_PATH="/tmp/rootfs-${VERSION}.ext4.zst"
echo ""
echo "Compressing with zstd -${ZSTD_LEVEL} -T0 (all cores)..."
COMPRESS_START=$(date +%s)
zstd -"${ZSTD_LEVEL}" -T0 -f -o "$COMPRESSED_PATH" "$ROOTFS_PATH"
COMPRESS_END=$(date +%s)
COMPRESSED_SIZE=$(stat --format='%s' "$COMPRESSED_PATH" 2>/dev/null || stat -f '%z' "$COMPRESSED_PATH")
echo "Compressed: ${COMPRESSED_SIZE} bytes ($(( COMPRESSED_SIZE / 1024 / 1024 )) MB) in $(( COMPRESS_END - COMPRESS_START ))s"
echo "Ratio: $(( SIZE_BYTES / COMPRESSED_SIZE ))x"

# --- Extract version metadata from rootfs ---
echo ""
echo "Extracting version metadata from rootfs..."
TMP_MNT=$(mktemp -d)
cleanup() {
    sudo umount "$TMP_MNT" 2>/dev/null || true
    rmdir "$TMP_MNT" 2>/dev/null || true
    # Don't remove compressed file — user may want it
}
trap cleanup EXIT

sudo mount -o ro "$ROOTFS_PATH" "$TMP_MNT"

OPENCLAW_VERSION="unknown"
AGENT_VERSION="unknown"
DATA_VERSION=1
if [ -f "$TMP_MNT/etc/ocm-versions.json" ]; then
    OPENCLAW_VERSION=$(python3 -c "import json; print(json.load(open('$TMP_MNT/etc/ocm-versions.json')).get('openclaw','unknown'))" 2>/dev/null || echo "unknown")
    AGENT_VERSION=$(python3 -c "import json; print(json.load(open('$TMP_MNT/etc/ocm-versions.json')).get('snapshot','unknown'))" 2>/dev/null || echo "unknown")
    DATA_VERSION=$(python3 -c "import json; print(json.load(open('$TMP_MNT/etc/ocm-versions.json')).get('data_version',1))" 2>/dev/null || echo "1")
fi
sudo umount "$TMP_MNT"

echo "OpenClaw: ${OPENCLAW_VERSION}"
echo "Agent: ${AGENT_VERSION}"
echo "Data version: ${DATA_VERSION}"

# --- GCS Paths ---
GCS_ROOTFS="gs://${GCS_BUCKET}/${GCS_PREFIX}/rootfs-${VERSION}.ext4.zst"
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
  "compression": "zstd",
  "openclaw_version": "${OPENCLAW_VERSION}",
  "agent_version": "${AGENT_VERSION}",
  "data_version": ${DATA_VERSION}
}
EOF
)
echo ""
echo "Manifest:"
echo "$MANIFEST" | python3 -m json.tool 2>/dev/null || echo "$MANIFEST"

# --- Upload to GCS ---
echo ""
echo "Uploading compressed rootfs to GCS..."
UPLOAD_START=$(date +%s)
gsutil -o "GSUtil:parallel_composite_upload_threshold=150M" cp "$COMPRESSED_PATH" "$GCS_ROOTFS"
UPLOAD_END=$(date +%s)
echo "Uploaded in $(( UPLOAD_END - UPLOAD_START ))s"

echo "Uploading versioned manifest..."
echo "$MANIFEST" | gsutil cp - "$GCS_MANIFEST_VERSIONED"

echo "Updating latest manifest..."
echo "$MANIFEST" | gsutil cp - "$GCS_MANIFEST_LATEST"

# --- Clean up compressed file ---
rm -f "$COMPRESSED_PATH"

# --- Summary ---
echo ""
echo "=========================================="
echo "Rootfs uploaded successfully!"
echo "=========================================="
echo "Version:    ${VERSION}"
echo "GCS rootfs: ${GCS_ROOTFS}"
echo "Manifest:   ${GCS_MANIFEST_LATEST}"
echo ""
echo "To use on agents, set metadata:"
echo "  gcloud compute instances add-metadata VM_NAME \\"
echo "    --metadata rootfs-gcs-manifest=${GCS_MANIFEST_LATEST}"
echo ""
echo "Or set environment variable:"
echo "  ROOTFS_GCS_MANIFEST=${GCS_MANIFEST_LATEST}"
echo "=========================================="
