#!/bin/bash
# Upload worker binary to GCS for spot instance fleet
#
# Usage: bash scripts/upload-worker-binary.sh [binary-path]
#
# This script:
# 1. Builds the backend binary for Linux (if not provided)
# 2. Computes SHA256
# 3. Uploads to gs://BUCKET/worker/backend-VERSION
# 4. Generates manifest JSON (versioned + latest)
#
# Must be run from the project root.

set -euo pipefail

# Configuration
GCS_BUCKET="${GCS_BUCKET:-openclawmachines}"
GCS_PREFIX="${GCS_PREFIX:-worker}"
BINARY_PATH="${1:-backend/server-linux}"

# --- Build if needed ---
if [ ! -f "$BINARY_PATH" ]; then
    echo "Building worker binary..."
    cd backend
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-w -s $(make -s -C .. print-ldflags 2>/dev/null || echo '')" -o server-linux ./cmd/server/
    cd ..
    BINARY_PATH="backend/server-linux"
fi

# --- Sanity Checks ---
if [ ! -f "$BINARY_PATH" ]; then
    echo "ERROR: binary not found at $BINARY_PATH"
    echo "Run 'make build-worker-binary' first."
    exit 1
fi

if ! command -v gsutil >/dev/null 2>&1 && ! command -v gcloud >/dev/null 2>&1; then
    echo "ERROR: neither gsutil nor gcloud found"
    exit 1
fi

# --- Version Tag ---
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME=$(date -u +"%Y%m%dT%H%M%SZ")
VERSION="${GIT_COMMIT}-${BUILD_TIME}"
echo "Version: ${VERSION}"

# --- Compute SHA256 ---
echo ""
echo "Computing SHA256..."
if command -v sha256sum >/dev/null 2>&1; then
    SHA256=$(sha256sum "$BINARY_PATH" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
    SHA256=$(shasum -a 256 "$BINARY_PATH" | awk '{print $1}')
else
    echo "ERROR: neither sha256sum nor shasum found"
    exit 1
fi
SIZE_BYTES=$(stat --format='%s' "$BINARY_PATH" 2>/dev/null || stat -f '%z' "$BINARY_PATH")
echo "SHA256: ${SHA256}"
echo "Size: ${SIZE_BYTES} bytes ($(( SIZE_BYTES / 1024 / 1024 )) MB)"

# --- GCS Paths ---
GCS_BINARY="gs://${GCS_BUCKET}/${GCS_PREFIX}/backend-${VERSION}"
GCS_MANIFEST_VERSIONED="gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest-${VERSION}.json"
GCS_MANIFEST_LATEST="gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest.json"

# --- Generate Manifest ---
MANIFEST=$(cat <<EOF
{
  "version": "${VERSION}",
  "sha256": "${SHA256}",
  "size_bytes": ${SIZE_BYTES},
  "url": "${GCS_BINARY}",
  "built_at": "${BUILD_TIME}"
}
EOF
)
echo ""
echo "Manifest:"
echo "$MANIFEST" | python3 -m json.tool 2>/dev/null || echo "$MANIFEST"

# --- Upload helper (gsutil with gcloud fallback) ---
gcs_cp() {
    local src="$1" dst="$2"
    if command -v gsutil >/dev/null 2>&1; then
        gsutil cp "$src" "$dst"
    else
        gcloud storage cp "$src" "$dst" --quiet
    fi
}

gcs_cp_stdin() {
    local dst="$1"
    local tmp
    tmp=$(mktemp)
    cat > "$tmp"
    gcs_cp "$tmp" "$dst"
    rm -f "$tmp"
}

# --- Upload to GCS ---
echo ""
echo "Uploading worker binary to GCS..."
gcs_cp "$BINARY_PATH" "$GCS_BINARY"

echo "Uploading versioned manifest..."
echo "$MANIFEST" | gcs_cp_stdin "$GCS_MANIFEST_VERSIONED"

echo "Updating latest manifest..."
echo "$MANIFEST" | gcs_cp_stdin "$GCS_MANIFEST_LATEST"

# --- Summary ---
echo ""
echo "=========================================="
echo "Worker binary uploaded successfully!"
echo "=========================================="
echo "Version:  ${VERSION}"
echo "GCS bin:  ${GCS_BINARY}"
echo "Manifest: ${GCS_MANIFEST_LATEST}"
echo ""
echo "To deploy workers:"
echo "  make restart-workers"
echo ""
echo "To set up a new instance template:"
echo "  gcloud compute instance-templates create ocm-worker-v${GIT_COMMIT} \\"
echo "    --metadata worker-manifest='${MANIFEST}'"
echo "=========================================="
