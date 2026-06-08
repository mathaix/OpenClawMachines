#!/usr/bin/env bash
# local-e2e-firecracker.sh — stand up the full OpenClaw control plane + a LOCAL
# Firecracker worker on a single KVM box, so you can provision a REAL Firecracker
# microVM through the browser (or via frontend/e2e/machine-lifecycle.spec.ts).
#
# This is the deterministic distillation of an interactive bring-up. It does NOT
# drive the browser — that is the Playwright layer (see docs/local-firecracker-e2e.md).
#
# Prereqs on this host:
#   - /dev/kvm present, nested virt, `firecracker` in PATH
#   - VM assets already on disk: /var/lib/ocm/images/{rootfs.ext4,vmlinux}
#   - XFS reflink mount at /var/lib/ocm/vms  (scripts/test-xfs-setup.sh / provision-host.sh)
#   - Docker (for Postgres), Go, Node, passwordless sudo
#   - The self-hosted-portability code fixes applied (see docs/local-firecracker-e2e.md)
#
# Usage:
#   scripts/local-e2e-firecracker.sh up                    # stack up, register host #1, start agent #1
#   scripts/local-e2e-firecracker.sh enroll-host [INDEX]   # enroll an EXTRA worker (INDEX 2..4) -> ready
#   scripts/local-e2e-firecracker.sh deregister-host <ID>  # stop its agent + remove the host record
#   scripts/local-e2e-firecracker.sh down                  # stop agents + backend + frontend (keeps Postgres)
#   scripts/local-e2e-firecracker.sh status                # show component health
#
# The enrollment flow (mint token -> register -> agent heartbeat -> ready) is fully
# deterministic; enroll-host scripts it. The admin "Enroll Host" wizard in the browser
# is just UI sugar over the same createEnrollmentToken/register APIs. See
# docs/local-firecracker-e2e.md for the agent-driven vs deterministic breakdown.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${OCM_E2E_RUN_DIR:-/tmp/ocm-local-e2e}"
BACKEND_URL="http://localhost:8080"
FRONTEND_URL="http://localhost:5173"
AGENT_ENDPOINT="http://127.0.0.1:9090"
mkdir -p "$RUN_DIR"

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

wait_http() { # url, name, max_secs
  local url="$1" name="$2" max="${3:-60}" i
  for ((i = 1; i <= max; i++)); do
    [ "$(curl -s -o /dev/null -w '%{http_code}' "$url" 2>/dev/null)" = "200" ] && { log "$name ready"; return 0; }
    sleep 1
  done
  die "$name not ready after ${max}s (see $RUN_DIR)"
}

cmd_up() {
  command -v firecracker >/dev/null || die "firecracker not in PATH"
  [ -e /dev/kvm ] || die "/dev/kvm missing — this host cannot run Firecracker"
  [ -f /var/lib/ocm/images/rootfs.ext4 ] || die "missing /var/lib/ocm/images/rootfs.ext4 (run build-rootfs / provision-host.sh)"
  sudo -n true 2>/dev/null || die "passwordless sudo required (agent sets up bridge/NAT/KVM)"

  # 1. Env + admin emails (backend admin is gated by OCM_ADMIN_EMAILS, not VITE_*)
  log "Generating .env.local"
  bash "$ROOT_DIR/scripts/local-dev.sh" env >/dev/null
  if ! grep -q '^OCM_ADMIN_EMAILS=' "$ROOT_DIR/.env.local"; then
    log "Adding OCM_ADMIN_EMAILS/OCM_SUPERUSER_EMAILS=dev@localhost"
    printf 'OCM_ADMIN_EMAILS=dev@localhost\nOCM_SUPERUSER_EMAILS=dev@localhost\n' >>"$ROOT_DIR/.env.local"
  fi
  # Resolve OpenClaw runtime versions from artifact_releases (needed to stage the
  # runtime so the in-VM gateway starts). See docs/local-firecracker-e2e.md.
  if ! grep -q '^FF_RUNTIME_VERSION_RESOLVER=' "$ROOT_DIR/.env.local"; then
    log "Adding FF_RUNTIME_VERSION_RESOLVER=1"
    printf 'FF_RUNTIME_VERSION_RESOLVER=1\n' >>"$ROOT_DIR/.env.local"
  fi

  # 2. Postgres + migrations + control plane (RUN_MODE=api, profile=local, dev auth)
  log "Starting Postgres"
  bash "$ROOT_DIR/scripts/local-dev.sh" postgres >/dev/null
  log "Starting control plane :8080 (logs: $RUN_DIR/backend.log)"
  nohup bash "$ROOT_DIR/scripts/local-dev.sh" backend >"$RUN_DIR/backend.log" 2>&1 &
  echo $! >"$RUN_DIR/backend.pid"
  wait_http "$BACKEND_URL/health" "control plane" 90

  # 3. Frontend dev server (for the browser)
  log "Starting frontend :5173 (logs: $RUN_DIR/frontend.log)"
  nohup bash "$ROOT_DIR/scripts/local-dev.sh" frontend >"$RUN_DIR/frontend.log" 2>&1 &
  echo $! >"$RUN_DIR/frontend.pid"

  # 4. Build the agent (worker) binary
  log "Building ocm-agent"
  (cd "$ROOT_DIR/backend" && go build -o "$RUN_DIR/ocm-agent" ./cmd/agent)

  # 5. Mint enrollment token (admin) and register THIS box as a local host
  log "Minting enrollment token"
  local enroll
  enroll=$(curl -s -X POST "$BACKEND_URL/api/admin/hosts/enrollment-tokens" \
    -H 'Content-Type: application/json' \
    -d '{"provider":"customer_owned","provider_class":"registered","expires_in_hours":24}' \
    | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')
  [ -n "$enroll" ] || die "failed to mint enrollment token (is dev@localhost admin?)"

  local nproc mem_mb
  nproc=$(nproc); mem_mb=$(awk '/MemTotal/{printf "%d",$2/1024*0.75}' /proc/meminfo)
  log "Registering host (cpu_threads=$nproc memory_mb=$mem_mb)"
  local reg
  reg=$(curl -s -X POST "$BACKEND_URL/api/agent/register" -H 'Content-Type: application/json' \
    -d "{\"enrollment_token\":\"$enroll\",\"agent_endpoint\":\"$AGENT_ENDPOINT\",\"agent_endpoint_type\":\"public_http\",\"capabilities\":{\"cpu_threads\":$nproc,\"memory_mb\":$mem_mb,\"disk_mb\":40000}}")
  echo "$reg" >"$RUN_DIR/host-reg.json"
  local host_id agent_token
  host_id=$(echo "$reg" | python3 -c 'import json,sys;print(json.load(sys.stdin)["host_id"])')
  agent_token=$(echo "$reg" | python3 -c 'import json,sys;print(json.load(sys.stdin)["agent_token"])')
  log "Registered host_id=$host_id"

  # 6. Launch the LOCAL worker agent (direct Firecracker runtime, no systemd/tunnel)
  log "Starting ocm-agent worker (logs: $RUN_DIR/agent.log)"
  sudo -n env \
    FC_AGENT_TOKEN="$agent_token" \
    HOST_ID="$host_id" \
    BACKEND_URL="$BACKEND_URL" \
    AGENT_ENDPOINT="$AGENT_ENDPOINT" \
    VM_RUNTIME_OWNER=direct \
    CONTROL_ALLOWED_CIDRS=0.0.0.0/0 \
    nohup "$RUN_DIR/ocm-agent" >"$RUN_DIR/agent.log" 2>&1 &
  echo $! >"$RUN_DIR/agent.pid"

  # 7. Wait for the agent's first heartbeat to promote the host provisioning->ready
  log "Waiting for agent control API + host=ready"
  wait_http "$AGENT_ENDPOINT/health" "agent :9090" 90
  local i st
  for ((i = 1; i <= 90; i++)); do
    st=$(curl -s "$BACKEND_URL/api/admin/hosts" -o /dev/null -w '%{http_code}') # warm
    st=$(docker exec ocm-postgres psql -U ocm -d ocm -At -c "select status from hosts where id=$host_id;" 2>/dev/null || true)
    [ "$st" = "ready" ] && { log "host_id=$host_id is READY"; break; }
    sleep 1
  done

  cat <<EOF

\033[1;32mStack is up.\033[0m
  Control plane : $BACKEND_URL   (profile=local, auto-login dev@localhost)
  Frontend      : $FRONTEND_URL
  Worker host   : id=$host_id, agent $AGENT_ENDPOINT, runtime=direct
  Run dir/logs  : $RUN_DIR

Now drive the browser to provision a real Firecracker VM, either:
  - manually at $FRONTEND_URL/dashboard  (New Machine -> Basic / region External -> Create -> Start), or
  - headless:  cd frontend && PLAYWRIGHT_BASE_URL=$FRONTEND_URL npx playwright test e2e/machine-lifecycle.spec.ts

Tear down with: scripts/local-e2e-firecracker.sh down
EOF
}

cmd_down() {
  log "Stopping agent"
  sudo -n pkill -f "$RUN_DIR/ocm-agent" 2>/dev/null || true
  for p in backend frontend; do
    [ -f "$RUN_DIR/$p.pid" ] && kill "$(cat "$RUN_DIR/$p.pid")" 2>/dev/null || true
  done
  log "Stopping control plane / frontend on :8080 / :5173"
  fuser -k 8080/tcp 2>/dev/null || true
  pkill -f 'exe/server' 2>/dev/null || true
  fuser -k 5173/tcp 2>/dev/null || true
  # remove any extra worker bridges created by enroll-host (ocm-br1..ocm-br3)
  for n in 1 2 3; do sudo -n ip link delete "ocm-br$n" 2>/dev/null || true; done
  log "Done (Postgres container left running — 'scripts/local-dev.sh stop' to remove)."
}

# enroll-host [INDEX] — deterministically enroll an ADDITIONAL worker on this box
# (INDEX 2..4). Mints a token (the admin API the wizard's "Generate" button calls),
# registers (the POST /api/agent/register the install script runs), launches an
# isolated agent (distinct ports/bridge/state dirs), and waits for ready.
# NOTE: a 2nd host reaches 'ready' but cannot run VMs on the SAME box — the VM subnet
# 192.168.100.0/24 + IP allocation are hardcoded (in prod one host == one machine).
cmd_enroll_host() {
  local idx="${1:-2}"
  case "$idx" in 2 | 3 | 4) ;; *) die "enroll-host INDEX must be 2..4 (1 is the primary from 'up')" ;; esac
  local cport=$((9090 + (idx - 1) * 2)) pport brnum
  pport=$((cport + 1))
  brnum=$((idx - 1))
  local brip="192.168.$((100 + brnum)).1/24"
  local endpoint="http://127.0.0.1:${cport}"
  [ -x "$RUN_DIR/ocm-agent" ] || (cd "$ROOT_DIR/backend" && go build -o "$RUN_DIR/ocm-agent" ./cmd/agent)

  log "Mint enrollment token (admin API — the wizard's 'Generate' button)"
  local tok
  tok=$(curl -s -X POST "$BACKEND_URL/api/admin/hosts/enrollment-tokens" -H 'Content-Type: application/json' \
    -d '{"provider":"customer_owned","provider_class":"registered","expires_in_hours":24}' \
    | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')
  [ -n "$tok" ] || die "failed to mint token (is dev@localhost a backend admin?)"

  log "Register host #$idx at $endpoint (POST /api/agent/register — same as the install script)"
  local reg host_id agent_token
  reg=$(curl -s -X POST "$BACKEND_URL/api/agent/register" -H 'Content-Type: application/json' \
    -d "{\"enrollment_token\":\"$tok\",\"agent_endpoint\":\"$endpoint\",\"agent_endpoint_type\":\"public_http\",\"capabilities\":{\"kvm\":true,\"arch\":\"$(uname -m)\",\"cpu_threads\":$(nproc),\"memory_mb\":8000,\"disk_mb\":20000}}")
  host_id=$(echo "$reg" | python3 -c 'import json,sys;print(json.load(sys.stdin)["host_id"])' 2>/dev/null || true)
  agent_token=$(echo "$reg" | python3 -c 'import json,sys;print(json.load(sys.stdin)["agent_token"])' 2>/dev/null || true)
  [ -n "$host_id" ] || die "register failed: $reg"
  log "Registered host_id=$host_id"

  log "Launch isolated agent #$idx (bridge ocm-br$brnum @ ${brip%/*}, ports $cport/$pport, state /var/lib/ocm/vms$idx)"
  sudo -n env \
    FC_AGENT_TOKEN="$agent_token" HOST_ID="$host_id" BACKEND_URL="$BACKEND_URL" \
    AGENT_ENDPOINT="$endpoint" VM_RUNTIME_OWNER=direct CONTROL_ALLOWED_CIDRS=0.0.0.0/0 \
    CONTROL_PORT="$cport" PROXY_PORT="$pport" BRIDGE_NAME="ocm-br$brnum" BRIDGE_IP="$brip" \
    STATE_DIR="/var/lib/ocm/vms$idx" BROWSER_STATE_DIR="/var/lib/ocm/vms$idx" \
    SOCKET_DIR="/var/lib/ocm/sockets$idx" DATA_DIR="/var/lib/ocm/data$idx" \
    RUNTIME_STATE_DIR="/var/lib/ocm/runtime$idx" HERMES_RUNTIME_DIR="/var/lib/ocm/hermes$idx" \
    OPENCLAW_RUNTIME_DIR=/var/lib/ocm/openclaw IMAGES_DIR=/var/lib/ocm/images KERNEL_PATH=/var/lib/ocm/vmlinux \
    nohup "$RUN_DIR/ocm-agent" >"$RUN_DIR/agent-h$host_id.log" 2>&1 &
  echo $! >"$RUN_DIR/agent-h$host_id.pid"

  log "Wait for host_id=$host_id -> ready (rootfs staging ~30s)"
  local i st
  for ((i = 1; i <= 90; i++)); do
    st=$(docker exec ocm-postgres psql -U ocm -d ocm -At -c "select status from hosts where id=$host_id;" 2>/dev/null || true)
    [ "$st" = "ready" ] && { log "host_id=$host_id is READY (logs: $RUN_DIR/agent-h$host_id.log)"; return 0; }
    sleep 1
  done
  die "host_id=$host_id did not reach ready (see $RUN_DIR/agent-h$host_id.log)"
}

# deregister-host <HOST_ID> — stop its agent + remove the host record (cleanup).
cmd_deregister_host() {
  local id="${1:?usage: deregister-host <HOST_ID>}"
  if [ -f "$RUN_DIR/agent-h$id.pid" ]; then
    log "Stopping agent for host $id"
    sudo -n kill "$(cat "$RUN_DIR/agent-h$id.pid")" 2>/dev/null || true
    rm -f "$RUN_DIR/agent-h$id.pid"
  fi
  log "Deregister host $id"
  curl -s -X POST "$BACKEND_URL/api/admin/hosts/$id/deregister" -o /dev/null -w 'deregister HTTP %{http_code}\n'
}

cmd_status() {
  printf 'control plane :8080  %s\n' "$(curl -s -o /dev/null -w '%{http_code}' $BACKEND_URL/health 2>/dev/null)"
  printf 'frontend     :5173  %s\n' "$(curl -s -o /dev/null -w '%{http_code}' $FRONTEND_URL/ 2>/dev/null)"
  printf 'agent        :9090  %s\n' "$(curl -s -o /dev/null -w '%{http_code}' $AGENT_ENDPOINT/health 2>/dev/null)"
  printf 'firecracker procs   %s\n' "$(pgrep -fc '[f]irecracker' || echo 0)"
  docker exec ocm-postgres psql -U ocm -d ocm -At \
    -c "select 'host '||id||' '||status from hosts; select 'machine '||name||' '||status from machines;" 2>/dev/null || true
}

case "${1:-}" in
  up)              cmd_up ;;
  down)            cmd_down ;;
  status)          cmd_status ;;
  enroll-host)     cmd_enroll_host "${2:-2}" ;;
  deregister-host) cmd_deregister_host "${2:-}" ;;
  *) die "usage: $0 {up|down|status|enroll-host [INDEX]|deregister-host <HOST_ID>}" ;;
esac
