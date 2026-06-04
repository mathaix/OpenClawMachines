#!/bin/bash
# Upload agent binary to GCS for self-update distribution
#
# Usage: bash scripts/upload-agent.sh [agent-binary-path]
#
# This script:
# 1. Computes SHA256 of the agent binary
# 2. Uploads to gs://BUCKET/agent/agent-VERSION
# 3. Generates manifest JSON
# 4. Uploads versioned manifest + latest pointer
#
# Must be run from the project root after `make build-agent`.

set -euo pipefail

# Configuration
GCS_BUCKET="${GCS_BUCKET:-openclawmachines}"
GCS_PREFIX="${GCS_PREFIX:-agent}"
AGENT_PATH="${1:-backend/agent-linux}"

# --- Sanity Checks ---
if [ ! -f "$AGENT_PATH" ]; then
    echo "ERROR: agent binary not found at $AGENT_PATH"
    echo "Run 'make build-agent' first."
    exit 1
fi
command -v gsutil >/dev/null 2>&1 || { echo "ERROR: gsutil not installed. Install via: apt-get install google-cloud-cli"; exit 1; }

# --- Version Tag ---
# CRITICAL: Read version from the compiled binary so the manifest version matches
# the binary's compiled-in version. If these differ, agents enter a restart loop.
# Try running the binary first; fall back to extracting version string from the
# binary (needed when cross-compiling, e.g. Linux binary on macOS).
VERSION=$("$AGENT_PATH" --version 2>/dev/null | awk '{print $2}' || true)
if [ -z "$VERSION" ] || [ "$VERSION" = "" ]; then
    VERSION=$(strings "$AGENT_PATH" | grep -oE '[0-9a-f]{7}-[0-9]{8}T[0-9]{6}Z' | head -1 || true)
fi
if [ -z "$VERSION" ] || [ "$VERSION" = "" ]; then
    echo "ERROR: could not read version from $AGENT_PATH"
    exit 1
fi
echo "Version: ${VERSION}"

# --- Compute SHA256 ---
echo ""
echo "Computing SHA256..."
if command -v sha256sum >/dev/null 2>&1; then
    SHA256=$(sha256sum "$AGENT_PATH" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
    SHA256=$(shasum -a 256 "$AGENT_PATH" | awk '{print $1}')
else
    echo "ERROR: neither sha256sum nor shasum found"
    exit 1
fi
SIZE_BYTES=$(stat --format='%s' "$AGENT_PATH" 2>/dev/null || stat -f '%z' "$AGENT_PATH")
echo "SHA256: ${SHA256}"
echo "Size: ${SIZE_BYTES} bytes ($(( SIZE_BYTES / 1024 / 1024 )) MB)"

# --- GCS Paths ---
GCS_AGENT="gs://${GCS_BUCKET}/${GCS_PREFIX}/agent-${VERSION}"
GCS_MANIFEST_VERSIONED="gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest-${VERSION}.json"
GCS_MANIFEST_LATEST="gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest.json"
BUILT_AT=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# --- Generate Manifest ---
MANIFEST=$(cat <<EOF
{
  "version": "${VERSION}",
  "sha256": "${SHA256}",
  "size_bytes": ${SIZE_BYTES},
  "url": "${GCS_AGENT}",
  "built_at": "${BUILT_AT}"
}
EOF
)
echo ""
echo "Manifest:"
echo "$MANIFEST" | python3 -m json.tool 2>/dev/null || echo "$MANIFEST"

# --- Upload to GCS ---
echo ""
echo "Uploading agent binary to GCS..."
gsutil cp "$AGENT_PATH" "$GCS_AGENT"

echo "Uploading versioned manifest..."
echo "$MANIFEST" | gsutil cp - "$GCS_MANIFEST_VERSIONED"

echo "Updating latest manifest..."
echo "$MANIFEST" | gsutil cp - "$GCS_MANIFEST_LATEST"

# --- Summary ---
echo ""
echo "=========================================="
echo "Agent uploaded successfully!"
echo "=========================================="
echo "Version:  ${VERSION}"
echo "GCS bin:  ${GCS_AGENT}"
echo "Manifest: ${GCS_MANIFEST_LATEST}"
echo ""
echo "To enable self-update on agents, set metadata:"
echo "  gcloud compute instances add-metadata VM_NAME \\"
echo "    --metadata agent-gcs-manifest=${GCS_MANIFEST_LATEST}"
echo ""
echo "Or set environment variable:"
echo "  AGENT_GCS_MANIFEST=${GCS_MANIFEST_LATEST}"
echo "=========================================="
