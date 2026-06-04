#!/bin/bash
# Promote a staged OpenClaw runtime release to a channel.
#
# Updates both the GCS channel manifest and production artifact_releases state.
#
# Usage:
#   OPENCLAW_CHANNEL=rc bash scripts/promote-openclaw-release.sh v2026.5.26-r1

set -euo pipefail

VERSION="${1:-}"
GCS_BUCKET="${GCS_BUCKET:-openclawmachines}"
GCS_PREFIX="${OPENCLAW_GCS_PREFIX:-openclaw}"
CHANNEL="${OPENCLAW_CHANNEL:-}"
GCP_PROJECT="${GCP_PROJECT:-clarateach}"
DB_SECRET="${OCM_DB_SECRET:-OCM_DATABASE_URL}"

case "${VERSION}" in
	""|*..*|*[!A-Za-z0-9._-]*)
		echo "ERROR: invalid OpenClaw release version: ${VERSION:-<empty>}"
		exit 1
		;;
esac
if [ -z "${CHANNEL}" ]; then
	echo "ERROR: OPENCLAW_CHANNEL is required; use rc/dev/stable explicitly."
	exit 1
fi
case "${CHANNEL}" in
	""|*..*|*[!A-Za-z0-9._-]*)
		echo "ERROR: invalid channel: ${CHANNEL:-<empty>}"
		exit 1
		;;
esac

command -v gsutil >/dev/null 2>&1 || { echo "ERROR: gsutil is not installed"; exit 1; }
command -v gcloud >/dev/null 2>&1 || { echo "ERROR: gcloud is not installed"; exit 1; }
command -v psql >/dev/null 2>&1 || { echo "ERROR: psql is not installed"; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "ERROR: python3 is not installed"; exit 1; }

RELEASE_MANIFEST_URI="gs://${GCS_BUCKET}/${GCS_PREFIX}/releases/${VERSION}/manifest.json"
CHANNEL_MANIFEST_URI="gs://${GCS_BUCKET}/${GCS_PREFIX}/manifest-${CHANNEL}.json"

echo "Reading release manifest: ${RELEASE_MANIFEST_URI}"
RELEASE_MANIFEST="$(gsutil cat "${RELEASE_MANIFEST_URI}" 2>/dev/null)"
if [ -z "${RELEASE_MANIFEST}" ]; then
	echo "ERROR: release manifest not found at ${RELEASE_MANIFEST_URI}"
	exit 1
fi

readarray -t MANIFEST_FIELDS < <(echo "${RELEASE_MANIFEST}" | python3 -c '
import json, sys
d = json.load(sys.stdin)
for key in ("version", "artifact_url", "sha256", "size_bytes"):
    print(d.get(key, ""))
print(d.get("min_rootfs_version") or "")
')
MANIFEST_VERSION="${MANIFEST_FIELDS[0]}"
ARTIFACT_URL="${MANIFEST_FIELDS[1]}"
SHA256="${MANIFEST_FIELDS[2]}"
SIZE_BYTES="${MANIFEST_FIELDS[3]}"
MIN_ROOTFS_VERSION="${MANIFEST_FIELDS[4]}"

if [ "${MANIFEST_VERSION}" != "${VERSION}" ]; then
	echo "ERROR: release manifest version mismatch: got ${MANIFEST_VERSION}, want ${VERSION}"
	exit 1
fi
if [ -z "${ARTIFACT_URL}" ] || [ -z "${SHA256}" ] || [ -z "${SIZE_BYTES}" ]; then
	echo "ERROR: release manifest missing artifact_url, sha256, or size_bytes"
	exit 1
fi

DATABASE_URL="${DATABASE_URL:-}"
if [ -z "${DATABASE_URL}" ]; then
	DATABASE_URL="$(gcloud secrets versions access latest --secret="${DB_SECRET}" --project="${GCP_PROJECT}" 2>/dev/null)"
fi
if [ -z "${DATABASE_URL}" ]; then
	echo "ERROR: could not retrieve database URL from secret ${DB_SECRET}"
	exit 1
fi

UPDATED_AT="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
CHANNEL_MANIFEST="$(cat <<EOF
{
  "schema_version": 1,
  "kind": "openclaw-channel",
  "channel": "${CHANNEL}",
  "current_version": "${VERSION}",
  "updated_at": "${UPDATED_AT}"
}
EOF
)"

echo "Promoting ${VERSION} to ${CHANNEL}"
echo "${CHANNEL_MANIFEST}" | gsutil cp - "${CHANNEL_MANIFEST_URI}"

psql "${DATABASE_URL}" \
	-v version="${VERSION}" \
	-v channel="${CHANNEL}" \
	-v url="${ARTIFACT_URL}" \
	-v sha256="${SHA256}" \
	-v size_bytes="${SIZE_BYTES}" \
	-v min_rootfs_version="${MIN_ROOTFS_VERSION}" <<'SQL'
BEGIN;
INSERT INTO artifact_releases (kind, version, channel, url, sha256, size_bytes, min_rootfs_version)
VALUES ('openclaw', :'version', :'channel', :'url', :'sha256', :size_bytes, NULLIF(:'min_rootfs_version', ''))
ON CONFLICT (kind, version) DO UPDATE
   SET channel = EXCLUDED.channel,
       url = EXCLUDED.url,
       sha256 = EXCLUDED.sha256,
       size_bytes = EXCLUDED.size_bytes,
       min_rootfs_version = EXCLUDED.min_rootfs_version;
COMMIT;
SQL

GCS_CURRENT="$(gsutil cat "${CHANNEL_MANIFEST_URI}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["current_version"])')"
DB_CURRENT="$(psql "${DATABASE_URL}" -tAc "SELECT version FROM artifact_releases WHERE kind = 'openclaw' AND channel = '${CHANNEL}' ORDER BY created_at DESC LIMIT 1;")"
DB_CURRENT="$(echo "${DB_CURRENT}" | tr -d '[:space:]')"

if [ "${GCS_CURRENT}" != "${VERSION}" ]; then
	echo "ERROR: GCS channel readback mismatch: got ${GCS_CURRENT}, want ${VERSION}"
	exit 1
fi
if [ "${DB_CURRENT}" != "${VERSION}" ]; then
	echo "ERROR: DB channel readback mismatch: got ${DB_CURRENT}, want ${VERSION}"
	exit 1
fi

echo ""
echo "OpenClaw release promoted"
echo "Version: ${VERSION}"
echo "Channel: ${CHANNEL}"
echo "GCS:     ${CHANNEL_MANIFEST_URI}"
