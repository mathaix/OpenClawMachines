#!/bin/bash
# Fetch the upstream opik-openclaw plugin tarball from npm.
#
# v0.2.15 added the OpenClaw 5.x activation manifest field
# ("activation": { "onStartup": true }) and the conversation-hook
# access flag, so the plugin loads under 5.x's narrowed runtime
# preloader. Earlier OCM builds bundled a fork from
# mathaix/opik-openclaw#fix/opik-embedded-tracing; that fix
# (PR #59) was upstreamed in v0.2.10, so the fork is no longer
# needed.
#
# Usage:
#   bash scripts/build-opik-plugin.sh
#   OPIK_VERSION=0.2.15 bash scripts/build-opik-plugin.sh

set -euo pipefail

OPIK_VERSION="${OPIK_VERSION:-0.2.15}"
OUTPUT="${1:-plugins/opik-openclaw-plugin.tgz}"
TMPDIR=$(mktemp -d)

cleanup() {
	rm -rf "$TMPDIR"
}
trap cleanup EXIT

echo "Fetching @opik/opik-openclaw@${OPIK_VERSION} from npm..."
npm pack "@opik/opik-openclaw@${OPIK_VERSION}" --pack-destination "${TMPDIR}" --quiet >/dev/null

TGZ=$(ls "${TMPDIR}"/*.tgz | head -1)
if [ -z "${TGZ}" ]; then
	echo "ERROR: npm pack produced no tarball"
	exit 1
fi

mkdir -p "$(dirname "${OUTPUT}")"
cp "${TGZ}" "${OUTPUT}"

echo "Built: ${OUTPUT}"
echo "Size:  $(du -h "${OUTPUT}" | cut -f1)"
echo "Contents (first 20 entries):"
# sed avoids the SIGPIPE that `tar | head` produces under `set -o pipefail`.
tar tzf "${OUTPUT}" | sed -n '1,20p'
