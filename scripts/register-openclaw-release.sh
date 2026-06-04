#!/bin/bash
# Register an OpenClaw runtime release in the production database.
#
# Reads the release manifest from GCS and inserts a row into artifact_releases.
# Idempotent: skips if the version already exists.
#
# Usage:
#   bash scripts/register-openclaw-release.sh [version]
#   bash scripts/register-openclaw-release.sh          # uses latest from GCS manifest

set -euo pipefail

GCS_BUCKET="${GCS_BUCKET:-openclawmachines}"
GCS_PREFIX="${OPENCLAW_GCS_PREFIX:-openclaw}"
CHANNEL="${OPENCLAW_CHANNEL:-stable}"
GCP_PROJECT="${GCP_PROJECT:-clarateach}"
DB_SECRET="${OCM_DB_SECRET:-OCM_DATABASE_URL}"

VERSION="${1:-}"

# If no version given, read the current stable channel manifest from GCS.
if [ -z "${VERSION}" ]; then
	CHANNEL_MANIFEST="$(gsutil cat "gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest-${CHANNEL}.json" 2>/dev/null)"
	VERSION="$(echo "${CHANNEL_MANIFEST}" | python3 -c "import sys,json; print(json.load(sys.stdin)['current_version'])")"
	if [ -z "${VERSION}" ]; then
		echo "ERROR: could not determine current version from channel manifest"
		exit 1
	fi
fi

echo "Registering OpenClaw release: ${VERSION}"

# Fetch the release manifest from GCS.
RELEASE_MANIFEST="$(gsutil cat "gs://${GCS_BUCKET}/${GCS_PREFIX}/releases/${VERSION}/manifest.json" 2>/dev/null)"
if [ -z "${RELEASE_MANIFEST}" ]; then
	echo "ERROR: release manifest not found at gs://${GCS_BUCKET}/${GCS_PREFIX}/releases/${VERSION}/manifest.json"
	exit 1
fi

ARTIFACT_URL="$(echo "${RELEASE_MANIFEST}" | python3 -c "import sys,json; print(json.load(sys.stdin)['artifact_url'])")"
SHA256="$(echo "${RELEASE_MANIFEST}" | python3 -c "import sys,json; print(json.load(sys.stdin)['sha256'])")"
SIZE_BYTES="$(echo "${RELEASE_MANIFEST}" | python3 -c "import sys,json; print(json.load(sys.stdin)['size_bytes'])")"

# min_rootfs_version: env override wins, else manifest, else empty (NULL in DB).
MANIFEST_MIN_ROOTFS="$(echo "${RELEASE_MANIFEST}" | python3 -c "import sys,json; d=json.load(sys.stdin); v=d.get('min_rootfs_version'); print(v if v else '')")"
MIN_ROOTFS_VERSION="${MIN_ROOTFS_VERSION:-${MANIFEST_MIN_ROOTFS}}"

if [ -n "${MIN_ROOTFS_VERSION}" ]; then
	MIN_ROOTFS_SQL="'${MIN_ROOTFS_VERSION}'"
else
	MIN_ROOTFS_SQL="NULL"
fi

echo "  URL:       ${ARTIFACT_URL}"
echo "  SHA256:    ${SHA256}"
echo "  Size:      ${SIZE_BYTES} bytes"
echo "  Min rootfs: ${MIN_ROOTFS_VERSION:-<none>}"

# Get database URL from GCP Secret Manager.
DATABASE_URL="${DATABASE_URL:-}"
if [ -z "${DATABASE_URL}" ]; then
	DATABASE_URL="$(gcloud secrets versions access latest --secret="${DB_SECRET}" --project="${GCP_PROJECT}" 2>/dev/null)"
fi
if [ -z "${DATABASE_URL}" ]; then
	echo "ERROR: could not retrieve database URL from secret ${DB_SECRET}"
	exit 1
fi

# Insert the release (skip if already exists).
RESULT="$(psql "${DATABASE_URL}" -tAc "
	INSERT INTO artifact_releases (kind, version, channel, url, sha256, size_bytes, min_rootfs_version)
	VALUES ('openclaw', '${VERSION}', '${CHANNEL}', '${ARTIFACT_URL}', '${SHA256}', ${SIZE_BYTES}, ${MIN_ROOTFS_SQL})
	ON CONFLICT (kind, version) DO NOTHING
	RETURNING version;
" 2>&1)"

if [ -n "${RESULT}" ]; then
	echo ""
	echo "Registered: ${VERSION}"
else
	echo ""
	echo "Already registered: ${VERSION} (no changes)"
fi

# Show current latest.
echo ""
echo "Latest releases:"
psql "${DATABASE_URL}" -c "
	SELECT version, channel, created_at
	FROM artifact_releases
	WHERE kind = 'openclaw'
	ORDER BY created_at DESC
	LIMIT 3;
"
