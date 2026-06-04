#!/bin/bash
# Verify the local openclaw binary version matches the pin in versions.json.
#
# The gateway e2e tests resolve the binary via `OPENCLAW_BIN` (env override)
# or `exec.LookPath("openclaw")` (PATH). When the OCM project upgrades the
# OpenClaw pin in versions.json but the developer hasn't re-run
# `make setup-local-openclaw`, the tests start hitting CLI flags and gateway
# behaviours that don't exist in the older binary, producing cryptic
# unknown-option errors that look like real regressions.
#
# This script fails fast with an actionable message in that case.
#
# Skip via OPENCLAW_SKIP_VERSION_CHECK=1 when you intentionally want to test
# against a non-pinned openclaw version.
#
# Usage:
#   bash scripts/check-openclaw-version.sh
#   OPENCLAW_BIN=/path/to/openclaw bash scripts/check-openclaw-version.sh

set -euo pipefail

if [ "${OPENCLAW_SKIP_VERSION_CHECK:-0}" = "1" ]; then
	echo "openclaw version check: SKIPPED (OPENCLAW_SKIP_VERSION_CHECK=1)"
	exit 0
fi

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSIONS_JSON="${PROJECT_ROOT}/versions.json"

if [ ! -f "${VERSIONS_JSON}" ]; then
	echo "openclaw version check: WARN — ${VERSIONS_JSON} not found; skipping" >&2
	exit 0
fi

# Pinned version from versions.json. jq if available, else a permissive grep.
if command -v jq >/dev/null 2>&1; then
	PINNED=$(jq -r '.openclaw // empty' "${VERSIONS_JSON}")
else
	PINNED=$(grep -E '"openclaw"\s*:' "${VERSIONS_JSON}" | head -1 | sed -E 's/.*"openclaw"\s*:\s*"([^"]+)".*/\1/')
fi

if [ -z "${PINNED}" ]; then
	echo "openclaw version check: WARN — no 'openclaw' key in versions.json; skipping" >&2
	exit 0
fi

# Resolve the binary the tests will use.
OPENCLAW_BIN_RESOLVED="${OPENCLAW_BIN:-}"
if [ -z "${OPENCLAW_BIN_RESOLVED}" ]; then
	OPENCLAW_BIN_RESOLVED=$(command -v openclaw 2>/dev/null || true)
fi
if [ -z "${OPENCLAW_BIN_RESOLVED}" ] || [ ! -x "${OPENCLAW_BIN_RESOLVED}" ]; then
	cat <<EOF >&2
openclaw version check: FAIL — no openclaw binary on PATH or via \$OPENCLAW_BIN.

The gateway e2e tests require an openclaw binary matching versions.json
("openclaw": "${PINNED}"). Either:

  1. Install from npm into a scratch location and point the tests at it:
       mkdir -p /tmp/oc-pinned
       npm install --prefix /tmp/oc-pinned --no-save openclaw@${PINNED}
       export OPENCLAW_BIN=/tmp/oc-pinned/node_modules/.bin/openclaw

  2. Or update your peer checkout (\${HOME}/openclaw) and run:
       cd \${HOME}/openclaw && git fetch origin --tags && git checkout v${PINNED}
       cd \${HOME}/OpenClawMachines && make setup-local-openclaw

  3. Or skip this check (NOT recommended) by exporting
     OPENCLAW_SKIP_VERSION_CHECK=1.
EOF
	exit 1
fi

# Parse "OpenClaw <version> (<sha>)" from --version output. Take the
# version token after "OpenClaw " and before the optional " (" suffix.
ACTUAL_RAW=$("${OPENCLAW_BIN_RESOLVED}" --version 2>&1 | head -1)
ACTUAL=$(echo "${ACTUAL_RAW}" | sed -E 's/.*OpenClaw[[:space:]]+([^[:space:]]+).*/\1/' | head -1)

if [ -z "${ACTUAL}" ]; then
	echo "openclaw version check: WARN — could not parse version from '${ACTUAL_RAW}'; skipping" >&2
	exit 0
fi

if [ "${ACTUAL}" = "${PINNED}" ]; then
	echo "openclaw version check: OK (${PINNED} matches versions.json)"
	exit 0
fi

cat <<EOF >&2
openclaw version check: FAIL

  Binary:   ${OPENCLAW_BIN_RESOLVED}
  Version:  ${ACTUAL}
  Required: ${PINNED}   (from versions.json)

The gateway e2e tests will use CLI flags / behaviours from OpenClaw
${PINNED} that may not exist in ${ACTUAL}. Running anyway will produce
confusing "unknown option" errors that look like test regressions.

Fix:

  1. Install the pinned version into a scratch location:
       mkdir -p /tmp/oc-pinned
       npm install --prefix /tmp/oc-pinned --no-save openclaw@${PINNED}
       export OPENCLAW_BIN=/tmp/oc-pinned/node_modules/.bin/openclaw

  2. Or update your peer checkout:
       cd \${HOME}/openclaw && git fetch origin --tags && git checkout v${PINNED}
       cd \${HOME}/OpenClawMachines && make setup-local-openclaw

  3. Or skip this check (NOT recommended) by exporting
     OPENCLAW_SKIP_VERSION_CHECK=1.

See: docs/learnings.md → "Stale local openclaw checkout silently breaks e2e"
EOF
exit 1
