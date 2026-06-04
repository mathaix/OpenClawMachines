#!/bin/bash
# Build and upload CLI binaries to GCS (multi-platform)
#
# Usage: bash scripts/upload-cli.sh
#
# This script:
# 1. Builds CLI for linux/amd64, darwin/amd64, darwin/arm64
# 2. Uploads each binary to gs://BUCKET/cli/
# 3. Generates per-platform manifests + latest pointer
#
# Must be run from the project root.

set -euo pipefail

# Configuration
GCS_BUCKET="${GCS_BUCKET:-openclawmachines}"
GCS_PREFIX="${GCS_PREFIX:-cli}"
CLI_DIR="cli"

# --- Sanity Checks ---
if [ ! -d "$CLI_DIR" ]; then
    echo "ERROR: must be run from project root (cli/ directory not found)"
    exit 1
fi
command -v gsutil >/dev/null 2>&1 || { echo "ERROR: gsutil not installed. Install via: apt-get install google-cloud-cli"; exit 1; }

# --- Version Tag ---
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
TIMESTAMP=$(date -u +%Y%m%dT%H%M%SZ)
VERSION="${GIT_COMMIT}-${TIMESTAMP}"
BUILT_AT=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS="-w -s -X 'github.com/mathaix/openclawmachines/cli/internal/commands.Version=${VERSION}' \
-X 'github.com/mathaix/openclawmachines/cli/internal/commands.GitCommit=${GIT_COMMIT}' \
-X 'github.com/mathaix/openclawmachines/cli/internal/commands.BuildTime=${TIMESTAMP}'"

echo "Version: ${VERSION}"
echo ""

# --- Platforms ---
PLATFORMS=("linux/amd64" "darwin/amd64" "darwin/arm64")

for PLATFORM in "${PLATFORMS[@]}"; do
    GOOS="${PLATFORM%%/*}"
    GOARCH="${PLATFORM##*/}"
    BINARY_NAME="ocm-${VERSION}-${GOOS}-${GOARCH}"
    OUTPUT_PATH="${CLI_DIR}/${BINARY_NAME}"

    echo "Building ${GOOS}/${GOARCH}..."
    cd "${CLI_DIR}" && CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" go build -ldflags "${LDFLAGS}" -o "${BINARY_NAME}" . && cd ..

    # Verify version from native-platform binary
    NATIVE_OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    NATIVE_ARCH=$(uname -m)
    [ "$NATIVE_ARCH" = "x86_64" ] && NATIVE_ARCH="amd64"
    [ "$NATIVE_ARCH" = "aarch64" ] && NATIVE_ARCH="arm64"
    if [ "${GOOS}" = "${NATIVE_OS}" ] && [ "${GOARCH}" = "${NATIVE_ARCH}" ]; then
        BINARY_VERSION=$("./$OUTPUT_PATH" version 2>/dev/null | awk '{print $3}' || true)
        if [ -n "$BINARY_VERSION" ] && [ "$BINARY_VERSION" != "$VERSION" ]; then
            echo "ERROR: binary reports version '${BINARY_VERSION}' but expected '${VERSION}'"
            exit 1
        fi
    fi

    # Compute SHA256
    if command -v sha256sum >/dev/null 2>&1; then
        SHA256=$(sha256sum "$OUTPUT_PATH" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
        SHA256=$(shasum -a 256 "$OUTPUT_PATH" | awk '{print $1}')
    else
        echo "ERROR: neither sha256sum nor shasum found"
        exit 1
    fi
    SIZE_BYTES=$(stat --format='%s' "$OUTPUT_PATH" 2>/dev/null || stat -f '%z' "$OUTPUT_PATH")

    GCS_BINARY="gs://${GCS_BUCKET}/${GCS_PREFIX}/${BINARY_NAME}"

    echo "  SHA256: ${SHA256}"
    echo "  Size: ${SIZE_BYTES} bytes"
    echo "  Uploading to ${GCS_BINARY}..."
    gsutil cp "$OUTPUT_PATH" "$GCS_BINARY"

    # Generate per-platform manifest
    PLATFORM_MANIFEST=$(cat <<EOF
{
  "version": "${VERSION}",
  "sha256": "${SHA256}",
  "size_bytes": ${SIZE_BYTES},
  "url": "${GCS_BINARY}",
  "built_at": "${BUILT_AT}",
  "os": "${GOOS}",
  "arch": "${GOARCH}"
}
EOF
)
    GCS_MANIFEST_VERSIONED="gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest-${VERSION}-${GOOS}-${GOARCH}.json"
    echo "$PLATFORM_MANIFEST" | gsutil cp - "$GCS_MANIFEST_VERSIONED"

    # Clean up binary
    rm -f "$OUTPUT_PATH"

    echo "  Done."
    echo ""
done

# --- Generate latest manifest (all platforms) ---
echo "Generating latest manifest..."
LATEST_MANIFEST=$(cat <<EOF
{
  "version": "${VERSION}",
  "built_at": "${BUILT_AT}",
  "platforms": {
    "linux/amd64": "gs://${GCS_BUCKET}/${GCS_PREFIX}/ocm-${VERSION}-linux-amd64",
    "darwin/amd64": "gs://${GCS_BUCKET}/${GCS_PREFIX}/ocm-${VERSION}-darwin-amd64",
    "darwin/arm64": "gs://${GCS_BUCKET}/${GCS_PREFIX}/ocm-${VERSION}-darwin-arm64"
  }
}
EOF
)
GCS_MANIFEST_LATEST="gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest.json"
GCS_MANIFEST_VERSIONED="gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest-${VERSION}.json"
echo "$LATEST_MANIFEST" | gsutil cp - "$GCS_MANIFEST_LATEST"
echo "$LATEST_MANIFEST" | gsutil cp - "$GCS_MANIFEST_VERSIONED"

# --- Summary ---
echo ""
echo "=========================================="
echo "CLI binaries uploaded successfully!"
echo "=========================================="
echo "Version: ${VERSION}"
echo "Platforms: ${PLATFORMS[*]}"
echo "Manifest: ${GCS_MANIFEST_LATEST}"
echo "=========================================="
