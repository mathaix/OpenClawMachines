#!/usr/bin/env bash
# test-cleanup.sh — Clean up stale resources from integration tests.
#
# Kills leftover processes, removes network devices, and frees ports
# that linger after a crashed or interrupted integration test run.
#
# Safe to run at any time; idempotent.
# Requires root (same as the tests themselves).

set -euo pipefail

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

cleaned=0

log()  { printf "${GREEN}[cleanup]${NC} %s\n" "$*"; }
warn() { printf "${YELLOW}[cleanup]${NC} %s\n" "$*"; }

# --- 1. Kill stale integration test processes ---
while IFS= read -r pid; do
    cmdline=$(tr '\0' ' ' < /proc/"$pid"/cmdline 2>/dev/null || true)
    if [[ "$cmdline" == *"integration.test"* ]]; then
        log "Killing stale integration.test (PID $pid)"
        kill -9 "$pid" 2>/dev/null || true
        cleaned=$((cleaned + 1))
    fi
done < <(pgrep -f 'integration\.test' 2>/dev/null || true)

# --- 2. Kill stale firecracker processes ---
while IFS= read -r pid; do
    log "Killing stale firecracker (PID $pid)"
    kill -9 "$pid" 2>/dev/null || true
    cleaned=$((cleaned + 1))
done < <(pgrep -x firecracker 2>/dev/null || true)

# --- 3. Kill anything holding port 80 (metadata server) ---
port80_pid=$(ss -tlnp sport = :80 2>/dev/null | grep -oP 'pid=\K\d+' | head -1 || true)
if [ -n "$port80_pid" ]; then
    log "Killing stale metadata server on port 80 (PID $port80_pid)"
    kill -9 "$port80_pid" 2>/dev/null || true
    cleaned=$((cleaned + 1))
fi

# --- 4. Delete stale ocm-test-* bridge interfaces ---
for br in $(ip -o link show type bridge 2>/dev/null | grep -oP 'ocm-test-\S+' | tr -d ':'); do
    log "Deleting stale bridge $br"
    ip link set "$br" down 2>/dev/null || true
    ip link delete "$br" 2>/dev/null || true
    cleaned=$((cleaned + 1))
done

# --- 5. Delete stale taptest-* tap interfaces ---
for tap in $(ip -o link show type tun 2>/dev/null | grep -oP 'taptest-\S+' | tr -d ':'); do
    log "Deleting stale tap $tap"
    ip link delete "$tap" 2>/dev/null || true
    cleaned=$((cleaned + 1))
done

# --- 6. Clean up stale iptables rules for the test subnet ---
PRIMARY_IFACE=$(ip route | awk '/default/ {print $5; exit}')
if [ -n "$PRIMARY_IFACE" ]; then
    while iptables -t nat -D POSTROUTING -s 192.168.200.0/24 -o "$PRIMARY_IFACE" -j MASQUERADE 2>/dev/null; do
        log "Removed stale NAT MASQUERADE rule"
        cleaned=$((cleaned + 1))
    done
    while iptables -D FORWARD -s 192.168.200.0/24 -d 169.254.169.254 -j DROP 2>/dev/null; do
        log "Removed stale FORWARD DROP rule"
        cleaned=$((cleaned + 1))
    done
fi

# --- 7. Clean up stale socket/state directories ---
# Unmount XFS loop device if present (used for reflink test acceleration)
if mountpoint -q /tmp/ocm-test 2>/dev/null; then
    log "Unmounting XFS loop device at /tmp/ocm-test"
    umount /tmp/ocm-test 2>/dev/null || umount -l /tmp/ocm-test 2>/dev/null || true
    cleaned=$((cleaned + 1))
fi
if [ -f /tmp/ocm-test-xfs.img ]; then
    log "Removing XFS image /tmp/ocm-test-xfs.img"
    rm -f /tmp/ocm-test-xfs.img
    cleaned=$((cleaned + 1))
fi
if [ -d /tmp/ocm-test ]; then
    log "Removing /tmp/ocm-test state directory"
    rm -rf /tmp/ocm-test
    cleaned=$((cleaned + 1))
fi

# --- Summary ---
if [ "$cleaned" -eq 0 ]; then
    log "Nothing to clean up — environment is clean."
else
    log "Cleaned up $cleaned stale resource(s)."
fi
