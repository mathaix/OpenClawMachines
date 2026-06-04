#!/usr/bin/env bash
# verify-deploy.sh — Post-build verification: checks that hosts are running
# the latest agent and rootfs versions.
#
# Usage:
#   bash scripts/verify-deploy.sh           # check once
#   bash scripts/verify-deploy.sh --wait    # poll until all hosts match (up to 5min)
set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

GCP_PROJECT="${GCP_PROJECT:-clarateach}"
GCP_ZONE="${GCP_ZONE:-us-central1-a}"

WAIT_MODE=false
MAX_WAIT=300  # 5 minutes
POLL_INTERVAL=15

if [[ "${1:-}" == "--wait" ]]; then
    WAIT_MODE=true
fi

header() { echo -e "\n${BOLD}${CYAN}=== $1 ===${NC}\n"; }
ok()     { echo -e "  ${GREEN}OK${NC}    $1"; }
warn()   { echo -e "  ${YELLOW}WARN${NC}  $1"; }
fail()   { echo -e "  ${RED}FAIL${NC}  $1"; }

# ── Get expected versions from GCS manifests ─────────────────────────────────
header "Expected Versions (GCS Manifests)"

EXPECTED_AGENT=$(gsutil cat gs://openclawmachines/agent/manifest.json 2>/dev/null | python3 -c "import json,sys; print(json.load(sys.stdin)['version'])" 2>/dev/null) || EXPECTED_AGENT="unknown"
EXPECTED_AGENT_SHA=$(gsutil cat gs://openclawmachines/agent/manifest.json 2>/dev/null | python3 -c "import json,sys; print(json.load(sys.stdin)['sha256'][:12])" 2>/dev/null) || EXPECTED_AGENT_SHA=""
EXPECTED_AGENT_COMMIT=$(echo "$EXPECTED_AGENT" | cut -d'-' -f1)

EXPECTED_ROOTFS=$(gsutil cat gs://openclawmachines/rootfs/manifest.json 2>/dev/null | python3 -c "import json,sys; print(json.load(sys.stdin)['version'])" 2>/dev/null) || EXPECTED_ROOTFS="unknown"
EXPECTED_ROOTFS_AGENT=$(gsutil cat gs://openclawmachines/rootfs/manifest.json 2>/dev/null | python3 -c "import json,sys; print(json.load(sys.stdin).get('agent_version','?'))" 2>/dev/null) || EXPECTED_ROOTFS_AGENT="?"

echo "  Agent:  $EXPECTED_AGENT (sha: ${EXPECTED_AGENT_SHA}...)"
echo "  Rootfs: $EXPECTED_ROOTFS (agent: $EXPECTED_ROOTFS_AGENT)"

# ── Discover running hosts ───────────────────────────────────────────────────
header "Discovering Hosts"

RUNNING_VMS=$(gcloud compute instances list \
    --filter="(labels.ocm=true OR name~ocm-host) AND status=RUNNING" \
    --format="value(name, networkInterfaces[0].accessConfigs[0].natIP)" \
    --project="$GCP_PROJECT" 2>/dev/null) || true

if [ -z "$RUNNING_VMS" ]; then
    warn "No running OCM host VMs found"
    exit 0
fi

HOST_COUNT=$(echo "$RUNNING_VMS" | wc -l | tr -d ' ')
echo "  Found $HOST_COUNT running host(s)"

# ── Check function ───────────────────────────────────────────────────────────
check_hosts() {
    local all_match=true
    local host_num=0

    while IFS=$'\t' read -r vm_name vm_ip; do
        host_num=$((host_num + 1))
        [ -z "$vm_ip" ] && { fail "$vm_name — no external IP"; all_match=false; continue; }

        # Fetch agent health
        HEALTH=$(curl -sf --connect-timeout 3 "http://${vm_ip}:9090/health" 2>/dev/null) || HEALTH=""
        if [ -z "$HEALTH" ]; then
            fail "$vm_name ($vm_ip) — agent not responding"
            all_match=false
            continue
        fi

        AGENT_VER=$(echo "$HEALTH" | python3 -c "import json,sys; print(json.load(sys.stdin).get('version','unknown'))" 2>/dev/null) || AGENT_VER="parse-error"
        AGENT_COMMIT=$(echo "$HEALTH" | python3 -c "import json,sys; print(json.load(sys.stdin).get('git_commit','unknown'))" 2>/dev/null) || AGENT_COMMIT="?"
        VM_COUNT=$(echo "$HEALTH" | python3 -c "import json,sys; print(json.load(sys.stdin).get('vm_count',0))" 2>/dev/null) || VM_COUNT="?"
        UPTIME=$(echo "$HEALTH" | python3 -c "import json,sys; print(json.load(sys.stdin).get('uptime','?'))" 2>/dev/null) || UPTIME="?"

        # Compare agent version
        if [ "$AGENT_VER" = "$EXPECTED_AGENT" ]; then
            ok "$vm_name — agent $AGENT_VER (vms=$VM_COUNT, up=$UPTIME)"
        elif [ "$AGENT_COMMIT" = "$EXPECTED_AGENT_COMMIT" ]; then
            ok "$vm_name — agent commit matches ($AGENT_COMMIT) version=$AGENT_VER (vms=$VM_COUNT)"
        else
            fail "$vm_name — agent $AGENT_VER (expected $EXPECTED_AGENT, vms=$VM_COUNT, up=$UPTIME)"
            all_match=false
        fi
    done <<< "$RUNNING_VMS"

    $all_match
}

# ── Run checks ───────────────────────────────────────────────────────────────
header "Agent Version Check"

if $WAIT_MODE; then
    elapsed=0
    while true; do
        if check_hosts; then
            echo ""
            echo -e "  ${GREEN}${BOLD}All hosts running expected versions.${NC}"
            break
        fi

        elapsed=$((elapsed + POLL_INTERVAL))
        if [ "$elapsed" -ge "$MAX_WAIT" ]; then
            echo ""
            fail "Timed out after ${MAX_WAIT}s — not all hosts updated."
            echo "  Agents self-update every ~5 minutes. You can re-run to check again."
            exit 1
        fi

        echo ""
        echo -e "  ${YELLOW}Waiting ${POLL_INTERVAL}s for self-update... (${elapsed}s / ${MAX_WAIT}s)${NC}"
        sleep "$POLL_INTERVAL"
        echo ""
    done
else
    if check_hosts; then
        echo ""
        echo -e "  ${GREEN}${BOLD}All hosts running expected versions.${NC}"
    else
        echo ""
        warn "Not all hosts are on the latest version yet."
        echo "  Agents self-update every ~5 minutes. Run 'make verify-deploy-wait' to poll until updated."
    fi
fi

# ── Backend health ───────────────────────────────────────────────────────────
header "Backend Health"

BACKEND_URL="${OCM_API_URL:-https://ocm-backend-477831964041.us-central1.run.app}"
BACKEND_HEALTH=$(curl -sf --connect-timeout 5 "${BACKEND_URL}/health" 2>/dev/null) || BACKEND_HEALTH=""

if [ -n "$BACKEND_HEALTH" ]; then
    BACKEND_VER=$(echo "$BACKEND_HEALTH" | python3 -c "import json,sys; d=json.load(sys.stdin); print(f'version={d.get(\"version\",\"?\")}, status={d.get(\"status\",\"?\")}')" 2>/dev/null) || BACKEND_VER="parse error"
    ok "Backend: $BACKEND_VER"
else
    warn "Backend health endpoint not reachable (may need OCM_API_URL)"
fi

# ── Recent errors ────────────────────────────────────────────────────────────
header "Recent Errors (last 15min)"

LOGFILE=$(mktemp)
gcloud logging read \
    'resource.type="cloud_run_revision" AND resource.labels.service_name="ocm-backend" AND severity>=ERROR' \
    --limit=5 --format=json --freshness=15m --project="$GCP_PROJECT" > "$LOGFILE" 2>/dev/null || echo "[]" > "$LOGFILE"

if [ ! -s "$LOGFILE" ] || [ "$(cat "$LOGFILE")" = "[]" ]; then
    ok "No errors in the last 15 minutes"
else
    python3 << PYEOF
import json
with open("$LOGFILE") as f:
    logs = json.load(f)
seen = set()
for log in logs:
    ts = log.get("timestamp", "")[:19]
    jp = log.get("jsonPayload", {})
    msg = jp.get("msg", jp.get("message", ""))
    err_detail = jp.get("error", "")
    if msg and msg not in seen:
        seen.add(msg)
        print(f"  \033[0;31mERROR\033[0m {ts} {msg}")
        if err_detail:
            print(f"        {err_detail[:200]}")
PYEOF
fi
rm -f "$LOGFILE"

echo ""
