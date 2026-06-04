#!/bin/bash
# Upload a Hermes runtime artifact to GCS and update the channel manifest.

set -euo pipefail

ARTIFACT_PATH="${1:-${HERMES_ARTIFACT_PATH:-}}"
GCS_BUCKET="${GCS_BUCKET:-openclawmachines}"
GCS_PREFIX="${HERMES_GCS_PREFIX:-hermes}"
CHANNEL="${HERMES_CHANNEL:-stable}"

if [ -z "$ARTIFACT_PATH" ]; then
    ARTIFACT_PATH="$(find /var/lib/ocm/hermes-artifacts -maxdepth 1 -type f -name 'hermes-*-py3-linux-amd64.tar.zst' 2>/dev/null | sort -V | tail -1)"
fi
if [ ! -f "$ARTIFACT_PATH" ]; then
    echo "ERROR: Hermes runtime artifact not found"
    exit 1
fi
command -v gsutil >/dev/null 2>&1 || { echo "ERROR: gsutil is not installed"; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "ERROR: python3 is not installed"; exit 1; }

BASE="$(basename "$ARTIFACT_PATH")"
VERSION="$(echo "$BASE" | sed -E 's/^hermes-(.+)-py3-linux-amd64\.tar\.zst$/\1/')"
if [ -z "$VERSION" ] || [ "$VERSION" = "$BASE" ]; then
    echo "ERROR: failed to parse Hermes version from $BASE"
    exit 1
fi

GIT_COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
SHA256="$(sha256sum "$ARTIFACT_PATH" | awk '{print $1}')"
SIZE_BYTES="$(stat --format='%s' "$ARTIFACT_PATH" 2>/dev/null || stat -f '%z' "$ARTIFACT_PATH")"
BUILT_AT="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"

GCS_RELEASE_DIR="gs://${GCS_BUCKET}/${GCS_PREFIX}/releases/${VERSION}"
GCS_ARTIFACT="${GCS_RELEASE_DIR}/hermes-${VERSION}-py3-linux-amd64.tar.zst"
GCS_RELEASE_MANIFEST="${GCS_RELEASE_DIR}/manifest.json"
GCS_CHANNEL_MANIFEST="gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest-${CHANNEL}.json"

RELEASE_MANIFEST="$(cat <<EOF
{
  "schema_version": 1,
  "kind": "hermes-runtime",
  "version": "${VERSION}",
  "channel": "${CHANNEL}",
  "built_at": "${BUILT_AT}",
  "git_commit": "${GIT_COMMIT}",
  "artifact_url": "${GCS_ARTIFACT}",
  "compression": "zstd",
  "size_bytes": ${SIZE_BYTES},
  "sha256": "${SHA256}",
  "runtime": {
    "entrypoint_relpath": ".venv/bin/hermes",
    "venv_relpath": ".venv"
  }
}
EOF
)"

CHANNEL_MANIFEST="$(cat <<EOF
{
  "schema_version": 1,
  "kind": "hermes-channel",
  "channel": "${CHANNEL}",
  "current_version": "${VERSION}",
  "updated_at": "${BUILT_AT}"
}
EOF
)"

gsutil cp "$ARTIFACT_PATH" "$GCS_ARTIFACT"
echo "$RELEASE_MANIFEST" | gsutil cp - "$GCS_RELEASE_MANIFEST"
if [ "${HERMES_SKIP_CHANNEL_MANIFEST:-0}" != "1" ]; then
    echo "$CHANNEL_MANIFEST" | gsutil cp - "$GCS_CHANNEL_MANIFEST"
fi

echo "Hermes runtime uploaded: ${VERSION}"
echo "$RELEASE_MANIFEST" | python3 -m json.tool
