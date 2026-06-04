#!/usr/bin/env bash
# cleanup-stale-dnat.sh — remove pre-chain DNAT sediment for browser VMs.
#
# Backup path for operators who need to unblock live-preview NOW, before
# the new chain-based agent lands on a host. See
# docs/superpowers/specs/2026-04-20-browser-vm-dnat-cleanup-plan.md Step 4.
#
# Deletes:
#   * nat/PREROUTING DNAT rules whose --to-destination is inside the
#     bridge subnet AND that are NOT wrapped in an OCM-BVM-* chain.
#   * filter/FORWARD ACCEPT rules targeting -o <bridge> on UDP port
#     ranges (the companion rule AllowUDPPortRangeDNAT used to install).
#
# Preserves:
#   * Anything inside an OCM-BVM-* chain (the new chain-based state).
#   * DNAT rules targeting non-bridge subnets (Docker, libvirt, etc.).
#
# ⚠️  MUST run during a maintenance window with browser-VM creates blocked.
#     Without that, this script races an in-flight CreateBrowserVM that is
#     programming its own rules. The recommended gate:
#
#       touch /var/lib/ocm/agent/disable-browser-vm-create
#       sudo ./cleanup-stale-dnat.sh
#       rm  /var/lib/ocm/agent/disable-browser-vm-create
#
# Idempotent: safe to re-run.

set -euo pipefail

BRIDGE_NAME="${BRIDGE_NAME:-ocm-br0}"
BRIDGE_SUBNET_PREFIX="${BRIDGE_SUBNET_PREFIX:-192.168.100.}"
CHAIN_PREFIX="${CHAIN_PREFIX:-OCM-BVM-}"

if [[ $EUID -ne 0 ]]; then
  echo "error: must run as root (iptables mutations require it)" >&2
  exit 1
fi

log() { printf '[cleanup-stale-dnat] %s\n' "$*"; }

# Collect nat/PREROUTING raw DNAT rules targeting bridge subnet AND not a
# jump to an OCM-BVM-* chain. Each line is a full rule spec suitable for
# `iptables -D PREROUTING`.
collect_legacy_prerouting() {
  iptables -w -t nat -S PREROUTING \
    | awk -v subnet="$BRIDGE_SUBNET_PREFIX" -v chain="$CHAIN_PREFIX" '
      # -A PREROUTING -p udp -m udp --dport 56000:56099 -j DNAT --to-destination 192.168.100.3
      $1 != "-A" || $2 != "PREROUTING" { next }
      # Skip jumps into OCM-BVM-* chains (owned state, leave alone).
      {
        for (i=3; i<=NF; i++) {
          if ($i == "-j" && index($(i+1), chain) == 1) next
        }
      }
      # Must be a DNAT rule targeting the bridge subnet.
      {
        isDNAT = 0; dest = ""
        for (i=3; i<=NF; i++) {
          if ($i == "DNAT") isDNAT = 1
          if ($i == "--to-destination") dest = $(i+1)
        }
        if (!isDNAT || dest == "") next
        if (index(dest, subnet) != 1) next
        # Emit the rule spec with -A replaced so caller can use -D.
        out = "-D " $2
        for (i=3; i<=NF; i++) out = out " " $i
        print out
      }'
}

# Collect filter/FORWARD ACCEPT rules that look like WebRTC DNAT companions:
# -A FORWARD -i <primary> -o <bridge> -p udp -m udp --dport <range> -j ACCEPT
collect_legacy_forward() {
  iptables -w -S FORWARD \
    | awk -v bridge="$BRIDGE_NAME" '
      $1 != "-A" || $2 != "FORWARD" { next }
      {
        seenOutBridge = 0; seenAccept = 0; seenUDP = 0
        for (i=3; i<=NF; i++) {
          if ($i == "-o" && $(i+1) == bridge) seenOutBridge = 1
          if ($i == "-j" && $(i+1) == "ACCEPT") seenAccept = 1
          if ($i == "udp") seenUDP = 1
        }
        if (!seenOutBridge || !seenAccept || !seenUDP) next
        out = "-D " $2
        for (i=3; i<=NF; i++) out = out " " $i
        print out
      }'
}

delete_rules() {
  local table="$1"; shift
  while IFS= read -r rule; do
    [[ -z "$rule" ]] && continue
    log "deleting from $table: $rule"
    # shellcheck disable=SC2086
    if ! iptables -w ${table:+-t "$table"} $rule; then
      log "  (already gone or failed; continuing)"
    fi
  done
}

log "scanning nat/PREROUTING for legacy DNAT into $BRIDGE_SUBNET_PREFIX*"
legacy_nat="$(collect_legacy_prerouting || true)"

log "scanning filter/FORWARD for legacy -o $BRIDGE_NAME UDP ACCEPTs"
legacy_filter="$(collect_legacy_forward || true)"

if [[ -z "$legacy_nat" && -z "$legacy_filter" ]]; then
  log "no legacy sediment found; nothing to do"
  exit 0
fi

printf '%s\n' "$legacy_nat"    | delete_rules nat
printf '%s\n' "$legacy_filter" | delete_rules ""

log "done. Verify with:"
log "  iptables -t nat -L PREROUTING -n --line-numbers | grep $BRIDGE_SUBNET_PREFIX"
log "  iptables -L FORWARD -n --line-numbers | grep $BRIDGE_NAME"
