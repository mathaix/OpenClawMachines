#!/bin/bash
# Firecracker-level end-to-end test for the kernel-browser rootfs.
# Boots the rootfs inside an actual Firecracker microVM, waits for CDP
# on port 9222, and verifies the live view endpoint on port 8080.
#
# This catches bugs that Docker can't reproduce (mount conflicts,
# init script errors, kernel cmdline parsing, etc).
#
# Usage: sudo bash scripts/test-browser-vm-firecracker-e2e.sh
# Requires: firecracker, sudo (for network setup), ip, brctl

set -euo pipefail

: "${ROOTFS:=/tmp/bvm-fc-test/rootfs.ext4}"
: "${KERNEL:=/var/lib/ocm/vmlinux}"
: "${MANIFEST_URI:=gs://openclawmachines/kernel-browser-rootfs/manifest.json}"
: "${BRIDGE_NAME:=fctest-br0}"
: "${BRIDGE_IP:=192.168.200.1}"
: "${VM_IP:=192.168.200.10}"
: "${TAP_NAME:=fctesttap0}"
: "${VM_ID:=fc-bvm-test}"
: "${WORK_DIR:=/tmp/bvm-fc-test}"
: "${WAIT_CDP_SECS:=180}"
: "${VCPUS:=2}"
: "${MEM_MIB:=4096}"
: "${SKIP_MANIFEST:=false}"

pass() { echo -e "\033[32m✓\033[0m $1"; }
fail() { echo -e "\033[31m✗\033[0m $1"; exit 1; }
info() { echo -e "\033[36m→\033[0m $1"; }

if [ "$(id -u)" -ne 0 ]; then
    fail "Must run as root (bridge + tap setup requires sudo)"
fi

mkdir -p "$WORK_DIR"
SOCKET="$WORK_DIR/fc.sock"
CONSOLE_LOG="$WORK_DIR/console.log"
FC_PID=""

cleanup() {
    info "Cleaning up"
    if [ -n "$FC_PID" ] && kill -0 "$FC_PID" 2>/dev/null; then
        kill -KILL "$FC_PID" 2>/dev/null || true
    fi
    pkill -f "firecracker.*$SOCKET" 2>/dev/null || true
    ip link del "$TAP_NAME" 2>/dev/null || true
    ip link del "$BRIDGE_NAME" 2>/dev/null || true
    # Remove iptables rules (best-effort; won't fail if already removed)
    if [ -n "${PRIMARY_IFACE:-}" ]; then
        iptables -t nat -D POSTROUTING -s "192.168.200.0/24" -o "$PRIMARY_IFACE" -j MASQUERADE 2>/dev/null || true
        iptables -D FORWARD -i "$BRIDGE_NAME" -o "$PRIMARY_IFACE" -j ACCEPT 2>/dev/null || true
        iptables -D FORWARD -i "$PRIMARY_IFACE" -o "$BRIDGE_NAME" -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true
    fi
    rm -f "$SOCKET"
}
trap cleanup EXIT

# --- Verify prerequisites ---
info "Checking prerequisites"
[ -x /usr/local/bin/firecracker ] || fail "firecracker not found at /usr/local/bin/firecracker"
[ -f "$KERNEL" ] || fail "kernel not found at $KERNEL"
pass "firecracker + kernel present"

# --- Fetch the manifest rootfs unless caller explicitly provides a local one ---
if [ "$SKIP_MANIFEST" = "1" ] || [ "$SKIP_MANIFEST" = "true" ]; then
    [ -f "$ROOTFS" ] || fail "local rootfs not found at $ROOTFS"
    pass "Using local rootfs $ROOTFS ($(du -h "$ROOTFS" | cut -f1))"
else
    info "Fetching manifest from $MANIFEST_URI"
    MANIFEST=$(gsutil cat "$MANIFEST_URI" 2>/dev/null) || fail "failed to fetch manifest"
    URL=$(echo "$MANIFEST" | jq -r .url)
    VERSION=$(echo "$MANIFEST" | jq -r .version)
    VERSION_SIDECAR="$WORK_DIR/rootfs.version"

    CACHED_VERSION=""
    [ -f "$VERSION_SIDECAR" ] && CACHED_VERSION=$(cat "$VERSION_SIDECAR")

    if [ -f "$ROOTFS" ] && [ "$CACHED_VERSION" = "$VERSION" ]; then
        pass "Using cached rootfs $VERSION"
    else
        [ -f "$ROOTFS" ] && info "Cache version $CACHED_VERSION != $VERSION — refetching"
        info "Downloading rootfs version $VERSION"
        gsutil cp "$URL" "$WORK_DIR/rootfs.ext4.zst" >/dev/null 2>&1 || fail "download failed"
        zstd -d -f "$WORK_DIR/rootfs.ext4.zst" -o "$ROOTFS" >/dev/null 2>&1 || fail "decompress failed"
        rm -f "$WORK_DIR/rootfs.ext4.zst"
        echo "$VERSION" > "$VERSION_SIDECAR"
        pass "Rootfs downloaded + decompressed ($(du -h "$ROOTFS" | cut -f1))"
    fi
fi

# --- Set up bridge + TAP ---
info "Setting up bridge $BRIDGE_NAME and tap $TAP_NAME"
ip link del "$TAP_NAME" 2>/dev/null || true
ip link del "$BRIDGE_NAME" 2>/dev/null || true

ip link add name "$BRIDGE_NAME" type bridge
ip addr add "${BRIDGE_IP}/24" dev "$BRIDGE_NAME"
ip link set "$BRIDGE_NAME" up

ip tuntap add "$TAP_NAME" mode tap
ip link set "$TAP_NAME" master "$BRIDGE_NAME"
ip link set "$TAP_NAME" up
pass "Network set up: bridge=$BRIDGE_IP tap=$TAP_NAME"

# --- Enable IP forwarding + NAT so the VM can reach the outside ---
sysctl -w net.ipv4.ip_forward=1 >/dev/null
PRIMARY_IFACE=$(ip route show default | awk '{print $5}' | head -1)
if [ -n "$PRIMARY_IFACE" ]; then
    iptables -t nat -C POSTROUTING -s "192.168.200.0/24" -o "$PRIMARY_IFACE" -j MASQUERADE 2>/dev/null || \
        iptables -t nat -A POSTROUTING -s "192.168.200.0/24" -o "$PRIMARY_IFACE" -j MASQUERADE
    iptables -C FORWARD -i "$BRIDGE_NAME" -o "$PRIMARY_IFACE" -j ACCEPT 2>/dev/null || \
        iptables -A FORWARD -i "$BRIDGE_NAME" -o "$PRIMARY_IFACE" -j ACCEPT
    iptables -C FORWARD -i "$PRIMARY_IFACE" -o "$BRIDGE_NAME" -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || \
        iptables -A FORWARD -i "$PRIMARY_IFACE" -o "$BRIDGE_NAME" -m state --state RELATED,ESTABLISHED -j ACCEPT
    pass "NAT set up via $PRIMARY_IFACE"
fi

# --- Generate deterministic MAC from VM IP ---
mac_from_ip() {
    local ip="$1"
    IFS='.' read -r a b c d <<< "$ip"
    printf "02:FC:00:%02x:%02x:%02x" "$b" "$c" "$d"
}
MAC=$(mac_from_ip "$VM_IP")
info "MAC: $MAC"

# --- Start firecracker ---
rm -f "$SOCKET"
info "Starting Firecracker (socket: $SOCKET)"
firecracker --api-sock "$SOCKET" >"$CONSOLE_LOG" 2>&1 &
FC_PID=$!
sleep 1

# Wait for socket
for i in {1..10}; do
    [ -S "$SOCKET" ] && break
    sleep 0.5
done
[ -S "$SOCKET" ] || fail "Firecracker socket did not appear"
pass "Firecracker started (pid=$FC_PID)"

# --- Configure VM via API socket ---
fc_api() {
    local method="$1" path="$2" body="${3:-}"
    if [ -n "$body" ]; then
        curl --silent --unix-socket "$SOCKET" -X "$method" "http://localhost$path" \
            -H "Content-Type: application/json" -H "Accept: application/json" -d "$body"
    else
        curl --silent --unix-socket "$SOCKET" -X "$method" "http://localhost$path"
    fi
}

info "Configuring VM"
fc_api PUT /machine-config "{
  \"vcpu_count\": $VCPUS,
  \"mem_size_mib\": $MEM_MIB,
  \"smt\": false
}"

KERNEL_ARGS="console=ttyS0 reboot=k panic=1 pci=off init=/sbin/overlay-init ip=${VM_IP}::${BRIDGE_IP}:255.255.255.0::eth0:off"
fc_api PUT /boot-source "{
  \"kernel_image_path\": \"$KERNEL\",
  \"boot_args\": \"$KERNEL_ARGS\"
}"

fc_api PUT /drives/rootfs "{
  \"drive_id\": \"rootfs\",
  \"path_on_host\": \"$ROOTFS\",
  \"is_root_device\": true,
  \"is_read_only\": false
}"

fc_api PUT /network-interfaces/eth0 "{
  \"iface_id\": \"eth0\",
  \"host_dev_name\": \"$TAP_NAME\",
  \"guest_mac\": \"$MAC\"
}"
pass "VM configured"

info "Starting VM"
fc_api PUT /actions '{"action_type":"InstanceStart"}'
pass "VM started"

# --- Wait for CDP ---
info "Waiting for CDP on ${VM_IP}:9222 (up to ${WAIT_CDP_SECS}s)"
CDP_READY=false
for i in $(seq 1 $((WAIT_CDP_SECS / 2))); do
    if curl -sf -m 2 "http://${VM_IP}:9222/json/version" >/dev/null 2>&1; then
        CDP_READY=true
        break
    fi
    sleep 2
done

if [ "$CDP_READY" != "true" ]; then
    echo
    echo "=== Last 60 lines of console log ==="
    tail -60 "$CONSOLE_LOG" || true
    fail "CDP endpoint never came up after ${WAIT_CDP_SECS}s"
fi
pass "CDP endpoint responding"

# --- Verify CDP payload ---
CDP_VERSION=$(curl -s "http://${VM_IP}:9222/json/version")
echo "  $CDP_VERSION" | head -c 200; echo
echo "$CDP_VERSION" | grep -q "Chrome/" || fail "CDP /json/version did not return Chrome info"
pass "CDP reports Chrome version"

# --- Verify CDP target list ---
CDP_LIST=$(curl -s "http://${VM_IP}:9222/json/list")
echo "$CDP_LIST" | jq -e 'type == "array"' >/dev/null 2>&1 || fail "/json/list did not return an array"
TARGETS=$(echo "$CDP_LIST" | jq 'length')
pass "CDP /json/list returned $TARGETS target(s)"

# --- Verify Neko live view ---
info "Checking Neko live view on ${VM_IP}:8080"
if curl -sf -m 3 "http://${VM_IP}:8080/" >/dev/null 2>&1; then
    pass "Neko live view HTTP endpoint responding"
else
    info "Neko live view not reachable (non-fatal — verify ENABLE_WEBRTC)"
fi

echo
pass "All Firecracker browser VM E2E checks passed"
echo "  CDP:     http://${VM_IP}:9222/json/version"
echo "  Neko:    http://${VM_IP}:8080/"
echo "  Console: $CONSOLE_LOG"
