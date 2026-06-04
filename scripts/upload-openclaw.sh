#!/bin/bash
# Upload an OpenClaw runtime artifact to GCS and update the channel manifest.
#
# Usage:
#   bash scripts/upload-openclaw.sh [path-to-openclaw-*.tar.zst]
#
# Outputs the computed release version to OPENCLAW_RELEASE_VERSION_FILE when set.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="${SCRIPT_DIR}/.."
ARTIFACT_PATH="${1:-${OPENCLAW_ARTIFACT_PATH:-}}"
GCS_BUCKET="${GCS_BUCKET:-openclawmachines}"
GCS_PREFIX="${OPENCLAW_GCS_PREFIX:-openclaw}"
CHANNEL="${OPENCLAW_CHANNEL:-stable}"

if [ -z "${ARTIFACT_PATH}" ] && [ -n "${OPENCLAW_VERSION:-}" ]; then
	ARTIFACT_PATH="/var/lib/ocm/openclaw-artifacts/openclaw-v${OPENCLAW_VERSION}-linux-amd64.tar.zst"
fi

if [ -z "${ARTIFACT_PATH}" ]; then
	ARTIFACT_PATH="$(find /var/lib/ocm/openclaw-artifacts -maxdepth 1 -type f -name 'openclaw-*-linux-amd64.tar.zst' 2>/dev/null | sort -V | tail -1)"
fi

if [ ! -f "${ARTIFACT_PATH}" ]; then
	echo "ERROR: OpenClaw runtime artifact not found at ${ARTIFACT_PATH}"
	echo "Usage: $0 [path-to-openclaw-<version>-linux-amd64.tar.zst]"
	exit 1
fi

command -v gsutil >/dev/null 2>&1 || { echo "ERROR: gsutil is not installed"; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "ERROR: python3 is not installed"; exit 1; }

BASE_VERSION="$(basename "${ARTIFACT_PATH}" | sed -E 's/^openclaw-(.+)-linux-amd64\.tar\.zst$/\1/')"
if [ -z "${BASE_VERSION}" ] || [ "${BASE_VERSION}" = "$(basename "${ARTIFACT_PATH}")" ]; then
	echo "ERROR: failed to parse version from artifact filename: ${ARTIFACT_PATH}"
	exit 1
fi

# Auto-increment revision suffix (-rN) by querying the DB for existing releases.
# e.g. if v2026.4.2-r8 exists, this upload becomes v2026.4.2-r9.
GCP_PROJECT="${GCP_PROJECT:-clarateach}"
DB_SECRET="${OCM_DB_SECRET:-OCM_DATABASE_URL}"
_DB_URL="${DATABASE_URL:-}"
if [ -z "${_DB_URL}" ]; then
	_DB_URL="$(gcloud secrets versions access latest --secret="${DB_SECRET}" --project="${GCP_PROJECT}" 2>/dev/null)" || true
fi
if [ -n "${_DB_URL}" ]; then
	# Strip any existing -rN suffix to get the base for querying
	QUERY_BASE="$(echo "${BASE_VERSION}" | sed -E 's/-r[0-9]+$//')"
	LATEST_REV="$(psql "${_DB_URL}" -tAc "
		SELECT COALESCE(MAX(
			CASE WHEN version ~ '-r[0-9]+$'
			THEN CAST(SUBSTRING(version FROM '-r([0-9]+)$') AS INTEGER)
			ELSE 0 END
		), 0)
		FROM artifact_releases
		WHERE kind = 'openclaw' AND version LIKE '${QUERY_BASE}%';
	" 2>/dev/null || echo "0")"
	NEXT_REV=$(( LATEST_REV + 1 ))
	VERSION="${QUERY_BASE}-r${NEXT_REV}"
	echo "Auto-incremented revision: ${BASE_VERSION} → ${VERSION} (previous max: r${LATEST_REV})"
else
	if [ "${OPENCLAW_ALLOW_UNSUFFIXED_VERSION:-0}" != "1" ]; then
		echo "ERROR: could not query DB for revision suffix"
		echo "       Set DATABASE_URL or allow an unsuffixed release explicitly with OPENCLAW_ALLOW_UNSUFFIXED_VERSION=1."
		exit 1
	fi
	VERSION="${BASE_VERSION}"
	echo "WARNING: could not query DB for revision — using base version ${VERSION}"
fi

GIT_COMMIT="$(git -C "${PROJECT_ROOT}" rev-parse --short HEAD 2>/dev/null || echo "unknown")"
SHA256="$(sha256sum "${ARTIFACT_PATH}" | awk '{print $1}')"
SIZE_BYTES="$(stat --format='%s' "${ARTIFACT_PATH}" 2>/dev/null || stat -f '%z' "${ARTIFACT_PATH}")"
BUILT_AT="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"

# Optional compat floor: min rootfs version this openclaw is known to run on.
# Baked into the release manifest so register-openclaw-release.sh can pick it up.
if [ -n "${MIN_ROOTFS_VERSION:-}" ]; then
	MIN_ROOTFS_VERSION_JSON="\"${MIN_ROOTFS_VERSION}\""
else
	MIN_ROOTFS_VERSION_JSON="null"
fi

GCS_RELEASE_DIR="gs://${GCS_BUCKET}/${GCS_PREFIX}/releases/${VERSION}"
GCS_ARTIFACT_NAME="openclaw-${VERSION}-linux-amd64.tar.zst"
GCS_ARTIFACT="${GCS_RELEASE_DIR}/${GCS_ARTIFACT_NAME}"
GCS_RELEASE_MANIFEST="${GCS_RELEASE_DIR}/manifest.json"
GCS_CHANNEL_MANIFEST="gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest-${CHANNEL}.json"

RELEASE_MANIFEST="$(cat <<EOF
{
  "schema_version": 1,
  "kind": "openclaw-runtime",
  "version": "${VERSION}",
  "channel": "${CHANNEL}",
  "built_at": "${BUILT_AT}",
  "git_commit": "${GIT_COMMIT}",
  "artifact_url": "${GCS_ARTIFACT}",
  "compression": "zstd",
  "size_bytes": ${SIZE_BYTES},
  "sha256": "${SHA256}",
  "min_rootfs_version": ${MIN_ROOTFS_VERSION_JSON},
  "runtime": {
    "entrypoint_relpath": "bin/openclaw",
    "bundled_plugins_relpath": "node_modules/openclaw/dist/extensions"
  }
}
EOF
)"

CHANNEL_MANIFEST="$(cat <<EOF
{
  "schema_version": 1,
  "kind": "openclaw-channel",
  "channel": "${CHANNEL}",
  "current_version": "${VERSION}",
  "updated_at": "${BUILT_AT}"
}
EOF
)"

echo "Uploading OpenClaw artifact to ${GCS_RELEASE_DIR}..."
gsutil cp "${ARTIFACT_PATH}" "${GCS_ARTIFACT}"
echo "${RELEASE_MANIFEST}" | gsutil cp - "${GCS_RELEASE_MANIFEST}"

if [ -n "${OPENCLAW_RELEASE_VERSION_FILE:-}" ]; then
	printf '%s\n' "${VERSION}" > "${OPENCLAW_RELEASE_VERSION_FILE}"
fi

# Channel manifest update is what flips this release to "stable" for new VMs.
# Set OPENCLAW_SKIP_CHANNEL_MANIFEST=1 to upload the artifact + release
# manifest without flipping the channel — useful for staging a candidate that
# needs validation (smoke test, cold-boot timing) before promotion. Promote
# afterward with: make promote-openclaw OPENCLAW_RELEASE="${VERSION}"
if [ "${OPENCLAW_SKIP_CHANNEL_MANIFEST:-0}" = "1" ]; then
	echo ""
	echo "OPENCLAW_SKIP_CHANNEL_MANIFEST=1 — channel manifest NOT updated"
	echo "Stable channel still points at: $(gsutil cat "${GCS_CHANNEL_MANIFEST}" 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin).get("current_version","(unknown)"))' 2>/dev/null || echo unknown)"
	echo ""
	echo "To promote ${VERSION} to a non-stable channel later:"
	echo "  make promote-openclaw OPENCLAW_RELEASE=${VERSION} OPENCLAW_CHANNEL=rc"
else
	echo "${CHANNEL_MANIFEST}" | gsutil cp - "${GCS_CHANNEL_MANIFEST}"
fi

echo ""
echo "=========================================="
echo "OpenClaw runtime uploaded"
echo "=========================================="
echo "Version:  ${VERSION}"
echo "Artifact: ${GCS_ARTIFACT}"
echo "Manifest: ${GCS_CHANNEL_MANIFEST}"
echo ""
echo "Release manifest:"
echo "${RELEASE_MANIFEST}" | python3 -m json.tool
echo "=========================================="
