#!/bin/bash
# Rebuild plugins/ocm-integrations-plugin.tgz from the in-repo plugin source.

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PLUGIN_DIR="${PROJECT_ROOT}/plugins/ocm-integrations-plugin"
OUTPUT="${PROJECT_ROOT}/plugins/ocm-integrations-plugin.tgz"

if [ ! -f "${PLUGIN_DIR}/package.json" ]; then
	echo "ERROR: ${PLUGIN_DIR}/package.json missing" >&2
	exit 1
fi

TMPDIR=$(mktemp -d)
cleanup() { rm -rf "${TMPDIR}"; }
trap cleanup EXIT

echo "Running 'npm pack' in ${PLUGIN_DIR}..."
(cd "${PLUGIN_DIR}" && npm pack --pack-destination "${TMPDIR}" --quiet >/dev/null)

TGZ=$(ls "${TMPDIR}"/*.tgz 2>/dev/null | head -1)
if [ -z "${TGZ}" ]; then
	echo "ERROR: npm pack produced no tarball" >&2
	exit 1
fi

cp "${TGZ}" "${OUTPUT}"

echo ""
echo "Built:  ${OUTPUT}"
echo "Size:   $(du -h "${OUTPUT}" | cut -f1)"
echo "SHA256: $(sha256sum "${OUTPUT}" | cut -d' ' -f1)"
