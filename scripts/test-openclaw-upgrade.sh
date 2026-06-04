#!/usr/bin/env bash
# Validate OpenClaw upgrade compatibility for plugin/channel behavior.
# Runs a focused gateway E2E smoke suite against one or more OpenClaw versions.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if ! command -v npm >/dev/null 2>&1; then
  echo "ERROR: npm is required" >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "ERROR: go is required" >&2
  exit 1
fi

declare -a versions=()
if [ "$#" -gt 0 ]; then
  versions=("$@")
else
  if [ ! -f versions.json ]; then
    echo "ERROR: versions.json not found and no version argument provided" >&2
    exit 1
  fi
  pinned=""
  if command -v jq >/dev/null 2>&1; then
    pinned="$(jq -r '.openclaw // empty' versions.json)"
  else
    pinned="$(awk -F'"' '/"openclaw"/ {print $4; exit}' versions.json)"
  fi
  if [ -z "$pinned" ]; then
    echo "ERROR: could not read .openclaw from versions.json" >&2
    exit 1
  fi
  versions=("$pinned")
fi

echo "=== Preflight: plugin/channel config unit tests ==="
(
  cd backend
  go test \
    ./internal/configassembly \
    ./internal/api \
    ./internal/agentclient \
    -run 'TestAssemble(Config|SeedConfig).*Opik|TestSlack(Connect|Disconnect)Ops|TestBuildChannelSetOps|TestBuildConfigOps_RemovePlugin|TestConfigBatch_MixedSetAndUnset|TestPushConfig_UsesConfigSetUnset' \
    -count=1
)

declare -a tmp_dirs=()
cleanup() {
  for d in "${tmp_dirs[@]}"; do
    rm -rf "$d"
  done
}
trap cleanup EXIT

require_opik="${E2E_REQUIRE_OPIK:-1}"
passed=0
failed=0

echo ""
echo "=== Upgrade Matrix ==="
for version in "${versions[@]}"; do
  normalized="${version#v}"
  workdir="$(mktemp -d)"
  tmp_dirs+=("$workdir")
  install_log="$workdir/npm-install.log"
  openclaw_bin="$workdir/node_modules/.bin/openclaw"

  echo ""
  echo "--- openclaw@${normalized} ---"
  if ! npm install --silent --prefix "$workdir" --no-save "openclaw@${normalized}" >"$install_log" 2>&1; then
    echo "FAIL: npm install openclaw@${normalized} (see $install_log)" >&2
    failed=$((failed + 1))
    continue
  fi

  if [ ! -x "$openclaw_bin" ]; then
    echo "FAIL: openclaw binary missing after install: $openclaw_bin" >&2
    failed=$((failed + 1))
    continue
  fi

  "$openclaw_bin" --version | head -n 1 || true

  if OPENCLAW_BIN="$openclaw_bin" E2E_REQUIRE_OPIK="$require_opik" make test-gateway-plugin-smoke; then
    echo "PASS: openclaw@${normalized}"
    passed=$((passed + 1))
  else
    echo "FAIL: openclaw@${normalized}" >&2
    failed=$((failed + 1))
  fi
done

echo ""
echo "=== Summary ==="
echo "Passed: $passed"
echo "Failed: $failed"

if [ "$failed" -ne 0 ]; then
  exit 1
fi
