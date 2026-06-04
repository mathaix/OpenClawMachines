#!/bin/bash
# Register a Hermes runtime release in artifact_releases with DB kind='hermes'.

set -euo pipefail

GCS_BUCKET="${GCS_BUCKET:-openclawmachines}"
GCS_PREFIX="${HERMES_GCS_PREFIX:-hermes}"
CHANNEL="${HERMES_CHANNEL:-stable}"
GCP_PROJECT="${GCP_PROJECT:-clarateach}"
DB_SECRET="${OCM_DB_SECRET:-OCM_DATABASE_URL}"
VERSION="${1:-}"

if [ -z "$VERSION" ]; then
    CHANNEL_MANIFEST="$(gsutil cat "gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest-${CHANNEL}.json" 2>/dev/null)"
    VERSION="$(echo "$CHANNEL_MANIFEST" | python3 -c "import sys,json; print(json.load(sys.stdin)['current_version'])")"
fi

RELEASE_MANIFEST="$(gsutil cat "gs://${GCS_BUCKET}/${GCS_PREFIX}/releases/${VERSION}/manifest.json" 2>/dev/null)"
ARTIFACT_URL="$(echo "$RELEASE_MANIFEST" | python3 -c "import sys,json; print(json.load(sys.stdin)['artifact_url'])")"
SHA256="$(echo "$RELEASE_MANIFEST" | python3 -c "import sys,json; print(json.load(sys.stdin)['sha256'])")"
SIZE_BYTES="$(echo "$RELEASE_MANIFEST" | python3 -c "import sys,json; print(json.load(sys.stdin)['size_bytes'])")"

DATABASE_URL="${DATABASE_URL:-}"
if [ -z "$DATABASE_URL" ]; then
    DATABASE_URL="$(gcloud secrets versions access latest --secret="${DB_SECRET}" --project="${GCP_PROJECT}" 2>/dev/null)"
fi
if [ -z "$DATABASE_URL" ]; then
    echo "ERROR: could not retrieve database URL"
    exit 1
fi

psql "$DATABASE_URL" -c "
    INSERT INTO artifact_releases (kind, version, channel, url, sha256, size_bytes)
    VALUES ('hermes', '${VERSION}', '${CHANNEL}', '${ARTIFACT_URL}', '${SHA256}', ${SIZE_BYTES})
    ON CONFLICT (kind, version) DO NOTHING;
"

psql "$DATABASE_URL" -c "
    SELECT version, channel, created_at
    FROM artifact_releases
    WHERE kind = 'hermes'
    ORDER BY created_at DESC
    LIMIT 5;
"
