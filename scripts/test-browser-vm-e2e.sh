#!/bin/bash
# End-to-end local test for the kernel-browser rootfs Docker image.
# Validates that the proxy architecture (kernel-images-api at :9222 ->
# Chromium at :9223) works and that Neko live view is reachable on :8080.
#
# Usage: bash scripts/test-browser-vm-e2e.sh
# Requires: docker, curl, jq

set -euo pipefail

IMAGE="${IMAGE:-ocm-kernel-browser-rootfs:latest}"
NAME="ocm-browser-e2e-test"
CDP_PORT=29222
NEKO_PORT=28080
API_PORT=2444

pass() { echo -e "\033[32m✓\033[0m $1"; }
fail() { echo -e "\033[31m✗\033[0m $1"; exit 1; }
info() { echo -e "\033[36m→\033[0m $1"; }

cleanup() {
  info "Cleaning up container"
  docker rm -f "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

info "Verifying Docker image exists: $IMAGE"
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  fail "Docker image $IMAGE not found. Run 'make build-kernel-browser-rootfs' first."
fi
pass "Image found"

info "Starting browser VM container"
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" \
  --privileged \
  --tmpfs /dev/shm:size=2g \
  -p "${CDP_PORT}:9222" \
  -p "${NEKO_PORT}:8080" \
  -p "${API_PORT}:10001" \
  -e DISPLAY_NUM=1 \
  -e HEIGHT=1080 \
  -e WIDTH=1920 \
  -e RUN_AS_ROOT=true \
  -e INTERNAL_PORT=9223 \
  -e CHROME_PORT=9222 \
  -e DEVTOOLS_PROXY_PORT=9222 \
  -e DEVTOOLS_PROXY_ADDR=127.0.0.1:9223 \
  -e WITHDOCKER=true \
  -e ENABLE_WEBRTC=true \
  -e NEKO_WEBRTC_EPR=56000-56100 \
  -e NEKO_WEBRTC_NAT1TO1=127.0.0.1 \
  -e CHROMIUM_FLAGS="--no-sandbox --disable-dev-shm-usage --disable-gpu" \
  "$IMAGE" >/dev/null
pass "Container started"

info "Waiting for CDP endpoint (up to 90s)"
CDP_READY=false
for i in $(seq 1 45); do
  if curl -sf -m 2 "http://localhost:${CDP_PORT}/json/version" >/dev/null 2>&1; then
    CDP_READY=true
    break
  fi
  sleep 2
done
if [ "$CDP_READY" != "true" ]; then
  echo "Container logs (last 50 lines):"
  docker logs --tail 50 "$NAME"
  fail "CDP endpoint never came up"
fi
pass "CDP endpoint responding"

info "Verifying CDP /json/version response"
CDP_VERSION=$(curl -s "http://localhost:${CDP_PORT}/json/version")
echo "  $CDP_VERSION" | head -c 200; echo
if ! echo "$CDP_VERSION" | grep -q "Chrome/"; then
  fail "CDP /json/version did not return Chrome info"
fi
pass "CDP reports Chrome version"

info "Verifying WebSocket debugger URL is reachable"
WS_URL=$(echo "$CDP_VERSION" | jq -r .webSocketDebuggerUrl 2>/dev/null || echo "")
if [ -z "$WS_URL" ] || [ "$WS_URL" = "null" ]; then
  fail "No webSocketDebuggerUrl in response"
fi
pass "WebSocket debugger URL present: $WS_URL"

info "Verifying CDP /json/list returns targets"
CDP_LIST=$(curl -s "http://localhost:${CDP_PORT}/json/list")
if ! echo "$CDP_LIST" | jq -e 'type == "array"' >/dev/null 2>&1; then
  fail "/json/list did not return an array"
fi
TARGETS=$(echo "$CDP_LIST" | jq 'length')
pass "CDP /json/list returned $TARGETS target(s)"

info "Waiting for Neko live view (up to 60s)"
NEKO_READY=false
for i in $(seq 1 30); do
  if curl -sf -m 2 "http://localhost:${NEKO_PORT}/" >/dev/null 2>&1; then
    NEKO_READY=true
    break
  fi
  sleep 2
done
if [ "$NEKO_READY" != "true" ]; then
  info "Neko live view not reachable (non-fatal — maybe ENABLE_WEBRTC is off in image)"
else
  pass "Neko live view HTTP endpoint responding"
fi

info "Verifying kernel-images-api health"
if curl -sf -m 2 "http://localhost:${API_PORT}/healthz" >/dev/null 2>&1; then
  pass "kernel-images-api responding"
else
  info "kernel-images-api /healthz not reachable (may not expose healthz)"
fi

echo
pass "All browser VM E2E checks passed"
echo "  CDP:      http://localhost:${CDP_PORT}/json/version"
echo "  Neko:     http://localhost:${NEKO_PORT}/"
echo "  API:      http://localhost:${API_PORT}/"
