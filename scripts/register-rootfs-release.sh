#!/bin/bash
# Register a rootfs release in the production database.
#
# Reads the rootfs manifest from GCS and inserts a row into artifact_releases.
# Idempotent: skips if the version already exists.
#
# Usage:
#   bash scripts/register-rootfs-release.sh [version]
#   bash scripts/register-rootfs-release.sh          # uses latest from GCS manifest

set -euo pipefail

GCS_BUCKET="${GCS_BUCKET:-openclawmachines}"
GCS_PREFIX="${GCS_PREFIX:-rootfs}"
CHANNEL="${ROOTFS_CHANNEL:-stable}"
GCP_PROJECT="${GCP_PROJECT:-clarateach}"
DB_SECRET="${OCM_DB_SECRET:-OCM_DATABASE_URL}"

VERSION="${1:-}"

# If no version given, read the current latest manifest from GCS.
if [ -z "${VERSION}" ]; then
	MANIFEST="$(gsutil cat "gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest.json" 2>/dev/null)"
	VERSION="$(echo "${MANIFEST}" | python3 -c "import sys,json; print(json.load(sys.stdin)['version'])")"
	if [ -z "${VERSION}" ]; then
		echo "ERROR: could not determine current version from manifest"
		exit 1
	fi
fi

echo "Registering rootfs release: ${VERSION}"

# Fetch the versioned manifest from GCS.
MANIFEST="$(gsutil cat "gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest-${VERSION}.json" 2>/dev/null)"
if [ -z "${MANIFEST}" ]; then
	echo "ERROR: manifest not found at gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest-${VERSION}.json"
	exit 1
fi

ARTIFACT_URL="$(echo "${MANIFEST}" | python3 -c "import sys,json; print(json.load(sys.stdin)['url'])")"
SHA256="$(echo "${MANIFEST}" | python3 -c "import sys,json; print(json.load(sys.stdin)['sha256'])")"
SIZE_BYTES="$(echo "${MANIFEST}" | python3 -c "import sys,json; print(json.load(sys.stdin)['size_bytes'])")"

echo "  URL:    ${ARTIFACT_URL}"
echo "  SHA256: ${SHA256}"
echo "  Size:   ${SIZE_BYTES} bytes"

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
	INSERT INTO artifact_releases (kind, version, channel, url, sha256, size_bytes)
	VALUES ('rootfs', '${VERSION}', '${CHANNEL}', '${ARTIFACT_URL}', '${SHA256}', ${SIZE_BYTES})
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
	WHERE kind = 'rootfs'
	ORDER BY created_at DESC
	LIMIT 5;
"
