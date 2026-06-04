#!/bin/bash
# Build a Hermes runtime artifact (.tar.zst) by exporting /opt/hermes from the
# upstream Hermes Docker image.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="${SCRIPT_DIR}/.."
HERMES_SRC="${HERMES_SRC:-/home/mantiz/hermes-agent}"
OUTPUT_DIR="${HERMES_ARTIFACTS_DIR:-/var/lib/ocm/hermes-artifacts}"
IMAGE_NAME="${HERMES_IMAGE_NAME:-ocm-hermes-runtime}"

command -v docker >/dev/null 2>&1 || { echo "ERROR: docker is not installed"; exit 1; }
command -v zstd >/dev/null 2>&1 || { echo "ERROR: zstd is not installed"; exit 1; }

if [ ! -f "${HERMES_SRC}/Dockerfile" ]; then
    echo "ERROR: Hermes source Dockerfile not found at ${HERMES_SRC}/Dockerfile"
    exit 1
fi

if docker info >/dev/null 2>&1; then
    DOCKER="docker"
else
    DOCKER="sudo docker"
fi

RAW_VERSION="${HERMES_VERSION:-}"
if [ -z "$RAW_VERSION" ]; then
    RAW_VERSION="$(git -C "${HERMES_SRC}" describe --tags --always --dirty 2>/dev/null || date -u +%Y%m%dT%H%M%SZ)"
fi
VERSION="${RAW_VERSION#v}"
VERSION="${VERSION#hermes-}"
VERSION="hermes-${VERSION}"

TMP_DIR="$(mktemp -d)"
CONTAINER_ID=""
cleanup() {
    if [ -n "$CONTAINER_ID" ]; then
        $DOCKER rm -f "$CONTAINER_ID" >/dev/null 2>&1 || true
    fi
    rm -rf "$TMP_DIR"
}
trap cleanup EXIT

mkdir -p "$OUTPUT_DIR"

echo "Building Hermes image ${IMAGE_NAME} from ${HERMES_SRC}"
DOCKER_BUILDKIT=1 $DOCKER build -t "$IMAGE_NAME" "$HERMES_SRC"

CONTAINER_ID="$($DOCKER create "$IMAGE_NAME")"
BUNDLE_DIR="${TMP_DIR}/bundle"
mkdir -p "$BUNDLE_DIR"
$DOCKER cp "${CONTAINER_ID}:/opt/hermes/." "$BUNDLE_DIR/"

if [ ! -x "${BUNDLE_DIR}/.venv/bin/hermes" ]; then
    echo "ERROR: ${BUNDLE_DIR}/.venv/bin/hermes not found or not executable"
    exit 1
fi

if command -v git >/dev/null 2>&1 && git -C "$HERMES_SRC" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    GIT_DIR="$(git -C "$HERMES_SRC" rev-parse --git-dir)"
    if [[ "$GIT_DIR" != /* ]]; then
        GIT_DIR="${HERMES_SRC}/${GIT_DIR}"
    fi
    if [ -d "$GIT_DIR" ] && [ ! -e "${BUNDLE_DIR}/.git" ]; then
        cp -a "$GIT_DIR" "${BUNDLE_DIR}/.git"
    fi
    REMOTE_URL="$(git -C "$HERMES_SRC" config --get remote.origin.url 2>/dev/null || true)"
    if [ -n "$REMOTE_URL" ] && [ -d "${BUNDLE_DIR}/.git" ]; then
        git -C "$BUNDLE_DIR" config remote.origin.url "$REMOTE_URL" || true
    fi
fi

for python_bin in "${BUNDLE_DIR}/.venv/bin"/python*; do
    if [ -L "$python_bin" ]; then
        python_target="$(readlink "$python_bin")"
        if [[ "$python_target" = /* ]]; then
            rm "$python_bin"
            $DOCKER cp -L "${CONTAINER_ID}:${python_target}" "$python_bin"
            chmod 0755 "$python_bin"
        fi
    fi
done

ARTIFACT="${OUTPUT_DIR}/${VERSION}-py3-linux-amd64.tar.zst"
tar -C "$BUNDLE_DIR" --numeric-owner --owner=0 --group=0 -cf - . | zstd -T0 -3 -f -o "$ARTIFACT"

SHA256="$(sha256sum "$ARTIFACT" | awk '{print $1}')"
SIZE_BYTES="$(stat --format='%s' "$ARTIFACT" 2>/dev/null || stat -f '%z' "$ARTIFACT")"

cat > "${OUTPUT_DIR}/${VERSION}.manifest.json" <<EOF
{
  "schema_version": 1,
  "kind": "hermes-runtime",
  "version": "${VERSION}",
  "artifact_url": "",
  "compression": "zstd",
  "size_bytes": ${SIZE_BYTES},
  "sha256": "${SHA256}",
  "runtime": {
    "entrypoint_relpath": ".venv/bin/hermes",
    "venv_relpath": ".venv"
  }
}
EOF

echo "Built ${ARTIFACT}"
echo "SHA256 ${SHA256}"
echo "Manifest template ${OUTPUT_DIR}/${VERSION}.manifest.json"
