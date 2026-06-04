#!/bin/bash
# Rebuild plugins/composio-plugin.tgz from a local peer checkout of the
# Composio OpenClaw plugin source repo.
#
# The plugin is NOT published to npm (the @mathaix scope name in
# package.json is reserved but the package is unpublished), so we can't
# `npm pack '@mathaix/ocm-openclaw-composio-plugin@VERSION'` the way
# build-opik-plugin.sh fetches its tarball from the npm registry.
# Instead this script runs `npm pack` inside a local clone and copies
# the resulting tarball into plugins/.
#
# Provenance trail: the committed plugins/composio-plugin.tgz should be
# bit-identical to what `npm pack` produces from the plugin repo at the
# matching git tag/commit. The OCM commit that bumps the tarball should
# reference the plugin-repo commit SHA in its message so reviewers can
# reproduce the build by running this script.
#
# Usage:
#   bash scripts/build-composio-plugin.sh
#   COMPOSIO_PLUGIN_REPO=/path/to/plugin/checkout bash scripts/build-composio-plugin.sh
#   COMPOSIO_PLUGIN_REF=v0.1.0 bash scripts/build-composio-plugin.sh
#
# Env:
#   COMPOSIO_PLUGIN_REPO   Path to a local checkout of the plugin repo.
#                          Defaults to ~/ocm-openclaw-composio-plugin (peer
#                          of this repo, matches the convention used by
#                          opik-openclaw, browser-harness, etc.).
#   COMPOSIO_PLUGIN_REF    Optional git ref (tag, branch, or SHA) to check
#                          out before packing. When set, the script
#                          verifies the working tree is clean and at the
#                          requested ref; refuses to build otherwise.
#                          When unset, builds whatever HEAD currently is —
#                          intended for iterative development; CI / release
#                          builds should always pin a ref.
#
# Output:
#   plugins/composio-plugin.tgz (from this repo's root)

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSIO_PLUGIN_REPO="${COMPOSIO_PLUGIN_REPO:-${HOME}/ocm-openclaw-composio-plugin}"
COMPOSIO_PLUGIN_REF="${COMPOSIO_PLUGIN_REF:-}"
OUTPUT="${PROJECT_ROOT}/plugins/composio-plugin.tgz"

if [ ! -d "${COMPOSIO_PLUGIN_REPO}/.git" ]; then
	echo "ERROR: ${COMPOSIO_PLUGIN_REPO} is not a git checkout." >&2
	echo "Clone it first:" >&2
	echo "  gh repo clone mathaix/ocm-openclaw-composio-plugin ${COMPOSIO_PLUGIN_REPO}" >&2
	exit 1
fi

if [ ! -f "${COMPOSIO_PLUGIN_REPO}/package.json" ]; then
	echo "ERROR: ${COMPOSIO_PLUGIN_REPO}/package.json missing — wrong directory?" >&2
	exit 1
fi

if [ -n "${COMPOSIO_PLUGIN_REF}" ]; then
	# Pinned build: verify clean tree + checkout requested ref.
	if [ -n "$(git -C "${COMPOSIO_PLUGIN_REPO}" status --porcelain)" ]; then
		echo "ERROR: ${COMPOSIO_PLUGIN_REPO} has uncommitted changes." >&2
		echo "Pinned builds (COMPOSIO_PLUGIN_REF=${COMPOSIO_PLUGIN_REF}) require a clean tree." >&2
		exit 1
	fi
	echo "Checking out ${COMPOSIO_PLUGIN_REF} in ${COMPOSIO_PLUGIN_REPO}..."
	git -C "${COMPOSIO_PLUGIN_REPO}" fetch --quiet
	git -C "${COMPOSIO_PLUGIN_REPO}" checkout --quiet "${COMPOSIO_PLUGIN_REF}"
fi

PLUGIN_COMMIT=$(git -C "${COMPOSIO_PLUGIN_REPO}" rev-parse HEAD)
PLUGIN_DESCRIBE=$(git -C "${COMPOSIO_PLUGIN_REPO}" describe --always --dirty --tags 2>/dev/null || echo "${PLUGIN_COMMIT:0:12}")

echo "Plugin source: ${COMPOSIO_PLUGIN_REPO}"
echo "Plugin commit: ${PLUGIN_COMMIT}"
echo "Plugin describe: ${PLUGIN_DESCRIBE}"

TMPDIR=$(mktemp -d)
cleanup() { rm -rf "${TMPDIR}"; }
trap cleanup EXIT

echo "Running 'npm pack' in plugin checkout..."
(cd "${COMPOSIO_PLUGIN_REPO}" && npm pack --pack-destination "${TMPDIR}" --quiet >/dev/null)

TGZ=$(ls "${TMPDIR}"/*.tgz 2>/dev/null | head -1)
if [ -z "${TGZ}" ]; then
	echo "ERROR: npm pack produced no tarball" >&2
	exit 1
fi

mkdir -p "$(dirname "${OUTPUT}")"
cp "${TGZ}" "${OUTPUT}"

echo ""
echo "Built:  ${OUTPUT}"
echo "Size:   $(du -h "${OUTPUT}" | cut -f1)"
echo "SHA256: $(sha256sum "${OUTPUT}" | cut -d' ' -f1)"
echo "Source: ${PLUGIN_DESCRIBE} (${PLUGIN_COMMIT})"
echo ""
echo "Next:"
echo "  git add ${OUTPUT}"
echo "  git commit -m \"build(composio): rebuild plugin tarball at ${PLUGIN_DESCRIBE}\""
echo "  make build-upload-openclaw   # bundle into a new OpenClaw runtime artifact"
