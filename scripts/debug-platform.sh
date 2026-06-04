#!/usr/bin/env bash
# debug-platform.sh — Platform diagnostics for OpenClaw Machines
# Usage: bash scripts/debug-platform.sh [section]
# Sections: all, hosts, machines, logs, schema, agent
set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

GCP_PROJECT="${GCP_PROJECT:-clarateach}"
GCP_REGION="${GCP_REGION:-us-central1}"
GCP_ZONE="${GCP_ZONE:-us-central1-a}"
DB_URL="${DATABASE_URL:-}"
SECTION="${1:-all}"

header() { echo -e "\n${BOLD}${CYAN}=== $1 ===${NC}\n"; }
ok()     { echo -e "  ${GREEN}OK${NC}  $1"; }
warn()   { echo -e "  ${YELLOW}WARN${NC}  $1"; }
fail()   { echo -e "  ${RED}FAIL${NC}  $1"; }

# ── Hosts: DB vs GCE reality ─────────────────────────────────────────────────
debug_hosts() {
    header "Host Health (DB vs GCE)"

    # Get hosts marked ready/provisioning in DB
    DB_HOSTS=$(gcloud logging read "" --limit=0 2>/dev/null; \
        psql "$DB_URL" -t -A -F'|' -c \
        "SELECT id, vm_name, external_ip, status, machine_count FROM hosts WHERE status IN ('ready','provisioning') ORDER BY id DESC" 2>/dev/null) || true

    if [ -z "$DB_HOSTS" ]; then
        # Fall back to gcloud SQL if no direct DB access — just check GCE
        echo "  (No direct DB access — checking GCE instances only)"
        echo ""
    fi

    # Get all running GCE instances
    echo -e "  ${BOLD}GCE Instances:${NC}"
    gcloud compute instances list \
        --filter="labels.ocm=true OR name~ocm-host" \
        --format="table(name, zone, networkInterfaces[0].accessConfigs[0].natIP:label=EXTERNAL_IP, status)" \
        --project="$GCP_PROJECT" 2>/dev/null || echo "  (failed to list instances)"

    echo ""
    echo -e "  ${BOLD}Agent Health Check:${NC}"

    # For each running instance, try to hit the agent health endpoint
    RUNNING_VMS=$(gcloud compute instances list \
        --filter="(labels.ocm=true OR name~ocm-host) AND status=RUNNING" \
        --format="value(name, networkInterfaces[0].accessConfigs[0].natIP)" \
        --project="$GCP_PROJECT" 2>/dev/null)

    if [ -z "$RUNNING_VMS" ]; then
        warn "No running OCM host VMs found"
        return
    fi

    while IFS=$'\t' read -r vm_name vm_ip; do
        if [ -z "$vm_ip" ]; then
            fail "$vm_name — no external IP"
            continue
        fi

        HEALTH=$(curl -sf --connect-timeout 3 "http://${vm_ip}:9090/health" 2>/dev/null) || HEALTH=""
        if [ -n "$HEALTH" ]; then
            AGENT_VER=$(echo "$HEALTH" | python3 -c "import json,sys; d=json.load(sys.stdin); print(f'v={d.get(\"version\",\"?\")}, vms={d.get(\"vm_count\",\"?\")}, up={d.get(\"uptime\",\"?\")}')" 2>/dev/null || echo "parse error")
            ok "$vm_name ($vm_ip) — $AGENT_VER"
        else
            fail "$vm_name ($vm_ip:9090) — agent not responding"
        fi
    done <<< "$RUNNING_VMS"

    # Check for IP mismatches between DB and GCE
    echo ""
    echo -e "  ${BOLD}IP Mismatch Detection:${NC}"
    MISMATCHES=0

    while IFS=$'\t' read -r vm_name vm_ip; do
        [ -z "$vm_name" ] && continue
        # Query DB for this host's stored IP (via backend API or direct DB)
        # We compare against what GCE reports
        DB_IP=$(psql "$DB_URL" -t -A -c \
            "SELECT external_ip FROM hosts WHERE vm_name = '$vm_name' AND status IN ('ready','provisioning')" 2>/dev/null | head -1) || DB_IP="(no db access)"

        if [ "$DB_IP" = "(no db access)" ]; then
            echo "  (Skipped — no direct DB access. Use 'make debug-hosts-db' with DATABASE_URL set)"
            break
        fi

        if [ -n "$DB_IP" ] && [ "$DB_IP" != "$vm_ip" ]; then
            fail "$vm_name: DB has $DB_IP, GCE has $vm_ip — STALE IP"
            MISMATCHES=$((MISMATCHES + 1))
        elif [ -n "$DB_IP" ]; then
            ok "$vm_name: DB=$DB_IP matches GCE"
        fi
    done <<< "$RUNNING_VMS"

    if [ "$MISMATCHES" -gt 0 ]; then
        echo ""
        warn "Found $MISMATCHES IP mismatch(es). VMs likely restarted and got new ephemeral IPs."
        echo "  Fix: VMs should use static IPs, or agent heartbeat should update the IP."
    fi
}

# ── Recent backend errors ─────────────────────────────────────────────────────
debug_logs() {
    header "Recent Backend Errors (last 1h)"

    LOGFILE=$(mktemp)
    gcloud logging read \
        'resource.type="cloud_run_revision" AND resource.labels.service_name="ocm-backend" AND (severity>=WARNING OR jsonPayload.level="ERROR" OR jsonPayload.level="WARN")' \
        --limit=20 --format=json --freshness=1h --project="$GCP_PROJECT" > "$LOGFILE" 2>/dev/null || echo "[]" > "$LOGFILE"

    if [ ! -s "$LOGFILE" ] || [ "$(cat "$LOGFILE")" = "[]" ]; then
        ok "No errors in the last hour"
        rm -f "$LOGFILE"
        return
    fi

    python3 << PYEOF
import json, sys
with open("$LOGFILE") as f:
    logs = json.load(f)
seen = set()
for log in logs:
    ts = log.get("timestamp", "")[:19]
    jp = log.get("jsonPayload", {})
    sev = jp.get("level", log.get("severity", ""))
    msg = jp.get("msg", jp.get("message", ""))
    err = jp.get("error", "")
    key = f"{msg}|{err}"
    if key in seen or not msg:
        continue
    seen.add(key)
    color = "\033[0;31m" if sev in ("ERROR",) else "\033[0;33m"
    print(f"  {color}{sev:5s}\033[0m {ts} {msg}")
    if err:
        print(f"        {err[:200]}")
PYEOF
    rm -f "$LOGFILE"
}

# ── Machine status ────────────────────────────────────────────────────────────
debug_machines() {
    header "Machine Status"

    SLUG="${2:-}"
    if [ -n "$SLUG" ]; then
        echo "  Looking up machine: $SLUG"
        # Would need API access or DB access
        echo "  (Use: ocm machines get $SLUG)"
        return
    fi

    echo "  Recent machines (via CLI):"
    if command -v ocm >/dev/null 2>&1; then
        ocm machines list 2>/dev/null || warn "ocm machines list failed (not logged in?)"
    else
        warn "ocm CLI not found on PATH"
    fi
}

# ── DB schema check ───────────────────────────────────────────────────────────
debug_schema() {
    header "Database Schema Check"

    # Check for tables that backend code references but may not exist
    EXPECTED_TABLES=(
        "users" "accounts" "account_members" "machines" "hosts"
        "machine_events" "machine_secrets" "account_credentials"
        "machine_credentials" "custom_providers" "machine_capabilities"
        "machine_skills" "machine_tools" "machine_channels"
    )

    if [ -z "$DB_URL" ]; then
        echo "  DATABASE_URL not set. Checking via backend error logs instead..."
        echo ""

        SCHEMAFILE=$(mktemp)
        gcloud logging read \
            'resource.type="cloud_run_revision" AND resource.labels.service_name="ocm-backend" AND jsonPayload.error=~"does not exist"' \
            --limit=20 --format=json --freshness=24h --project="$GCP_PROJECT" > "$SCHEMAFILE" 2>/dev/null || echo "[]" > "$SCHEMAFILE"

        if [ ! -s "$SCHEMAFILE" ] || [ "$(cat "$SCHEMAFILE")" = "[]" ]; then
            ok "No missing-table/column errors in the last 24h"
        else
            python3 << PYEOF
import json
with open("$SCHEMAFILE") as f:
    logs = json.load(f)
seen = set()
for log in logs:
    jp = log.get("jsonPayload", {})
    err = jp.get("error", "")
    msg = jp.get("msg", "")
    if err and err not in seen:
        seen.add(err)
        print(f"  \033[0;31mMISSING\033[0m {msg}: {err}")
PYEOF
        fi
        rm -f "$SCHEMAFILE"
        return
    fi

    echo "  Checking expected tables..."
    for table in "${EXPECTED_TABLES[@]}"; do
        EXISTS=$(psql "$DB_URL" -t -A -c \
            "SELECT 1 FROM information_schema.tables WHERE table_name = '$table'" 2>/dev/null)
        if [ "$EXISTS" = "1" ]; then
            ok "$table"
        else
            fail "$table — MISSING"
        fi
    done
}

# ── Agent detail (for a specific host) ────────────────────────────────────────
debug_agent() {
    header "Agent Detail"

    VM_NAME="${2:-}"
    if [ -z "$VM_NAME" ]; then
        # Auto-detect: pick the first running OCM host
        VM_NAME=$(gcloud compute instances list \
            --filter="(labels.ocm=true OR name~ocm-host) AND status=RUNNING" \
            --format="value(name)" --limit=1 --project="$GCP_PROJECT" 2>/dev/null)
        if [ -z "$VM_NAME" ]; then
            fail "No running OCM host found"
            return
        fi
        echo "  Auto-selected: $VM_NAME"
    fi

    VM_IP=$(gcloud compute instances describe "$VM_NAME" \
        --zone="$GCP_ZONE" --project="$GCP_PROJECT" \
        --format="value(networkInterfaces[0].accessConfigs[0].natIP)" 2>/dev/null)

    if [ -z "$VM_IP" ]; then
        fail "Could not get IP for $VM_NAME"
        return
    fi

    echo "  IP: $VM_IP"
    echo ""

    # Health
    echo -e "  ${BOLD}Health:${NC}"
    curl -sf --connect-timeout 5 "http://${VM_IP}:9090/health" 2>/dev/null | python3 -m json.tool || fail "Agent not responding on :9090"

    echo ""

    # VMs running on this host
    echo -e "  ${BOLD}VMs:${NC}"
    curl -sf --connect-timeout 5 "http://${VM_IP}:9090/vms" 2>/dev/null | python3 -m json.tool || fail "Could not list VMs"

    echo ""

    # IP type
    echo -e "  ${BOLD}IP Type:${NC}"
    IP_TYPE=$(gcloud compute instances describe "$VM_NAME" \
        --zone="$GCP_ZONE" --project="$GCP_PROJECT" \
        --format="value(networkInterfaces[0].accessConfigs[0].type)" 2>/dev/null)
    NAT_IP=$(gcloud compute instances describe "$VM_NAME" \
        --zone="$GCP_ZONE" --project="$GCP_PROJECT" \
        --format="value(networkInterfaces[0].accessConfigs[0].natIP)" 2>/dev/null)
    echo "  Type: $IP_TYPE"
    echo "  IP:   $NAT_IP"

    # Check if it's a static reservation
    STATIC=$(gcloud compute addresses list \
        --filter="address=$NAT_IP" \
        --format="value(name, status)" --project="$GCP_PROJECT" 2>/dev/null)
    if [ -n "$STATIC" ]; then
        ok "IP $NAT_IP is a static reservation: $STATIC"
    else
        warn "IP $NAT_IP is EPHEMERAL — will change on VM restart!"
    fi
}

# ── Run sections ──────────────────────────────────────────────────────────────
case "$SECTION" in
    all)
        debug_hosts
        debug_logs
        debug_schema
        ;;
    hosts)   debug_hosts ;;
    logs)    debug_logs ;;
    machines) debug_machines "$@" ;;
    schema)  debug_schema ;;
    agent)   debug_agent "$@" ;;
    *)
        echo "Usage: $0 [all|hosts|logs|machines|schema|agent]"
        echo ""
        echo "Sections:"
        echo "  all       Run hosts + logs + schema checks (default)"
        echo "  hosts     Compare DB host records vs GCE reality (IP mismatches)"
        echo "  logs      Show recent backend errors (last 1h)"
        echo "  machines  List machine status"
        echo "  schema    Check for missing DB tables/columns"
        echo "  agent     Detailed agent health for a host (auto-detects or pass VM name)"
        exit 1
        ;;
esac

echo ""
