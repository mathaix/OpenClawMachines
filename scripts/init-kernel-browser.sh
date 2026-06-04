#!/bin/sh
# OpenClaw Kernel Browser VM init script.
# Injected as /sbin/overlay-init in kernel-browser-rootfs.ext4.
#
# This is PID 1 inside the Firecracker browser microVM. It configures the
# minimal guest environment, then delegates to the Kernel Images runtime.

exec 2>&1

mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs none /sys 2>/dev/null || true
mount -t devtmpfs none /dev 2>/dev/null || true
mount -t tmpfs tmpfs /tmp 2>/dev/null || true
mount -t tmpfs tmpfs /run 2>/dev/null || true

# /dev/fd → /proc/self/fd symlink. Firecracker's minimal /dev doesn't
# create this by default, but wrapper.sh's log aggregator uses bash
# process substitution `< <(find ...)` which needs /dev/fd/NN to exist.
# Must mount /proc FIRST (done above) for the symlink to resolve.
ln -sfn /proc/self/fd /dev/fd 2>/dev/null || true
ln -sfn /proc/self/fd/0 /dev/stdin 2>/dev/null || true
ln -sfn /proc/self/fd/1 /dev/stdout 2>/dev/null || true
ln -sfn /proc/self/fd/2 /dev/stderr 2>/dev/null || true

# Note: /dev/shm is intentionally NOT mounted here. wrapper.sh mounts it
# itself (when WITHDOCKER is unset). If we mount it first, wrapper.sh's
# second mount attempt fails and `set -o errexit` kills the script.
mkdir -p /dev/shm

mkdir -p /run /var/run /run/dbus /run/lock /tmp/runtime-root
chmod 700 /tmp/runtime-root 2>/dev/null || true

# The rootfs is writable and reused by Firecracker tests/agents, so runtime
# sockets can persist across boots. The Go wrapper waits only for the socket
# path before invoking supervisorctl; a stale supervisor socket races startup
# and causes xorg/chromium to never be started.
rm -f /run/supervisor.sock /var/run/supervisor.sock \
    /run/supervisord.pid /var/run/supervisord.pid

CMDLINE=$(cat /proc/cmdline 2>/dev/null || echo "")

if echo "$CMDLINE" | grep -q "ip="; then
    IP_CONFIG=$(echo "$CMDLINE" | grep -oE 'ip=[^ ]+' | cut -d= -f2)
    CLIENT_IP=$(echo "$IP_CONFIG" | cut -d: -f1)
    GATEWAY_IP=$(echo "$IP_CONFIG" | cut -d: -f3)

    ip link set lo up 2>/dev/null || true
    ip link set eth0 up 2>/dev/null || true

    if [ -n "$CLIENT_IP" ] && ! ip addr show eth0 2>/dev/null | grep -q "$CLIENT_IP"; then
        ip addr add "${CLIENT_IP}/24" dev eth0 2>/dev/null || true
    fi

    if [ -n "$GATEWAY_IP" ] && ! ip route 2>/dev/null | grep -q "default"; then
        ip route add default via "$GATEWAY_IP" 2>/dev/null || true
    fi

    {
        echo "nameserver 8.8.8.8"
        echo "nameserver 8.8.4.4"
    } > /etc/resolv.conf 2>/dev/null || true

    echo "Network: ${CLIENT_IP} gw ${GATEWAY_IP} dns 8.8.8.8,8.8.4.4"
fi

# Set a sensible PATH — Firecracker PID 1 inherits an empty environment.
# wrapper.sh, chromium-launcher, and supervisord all need to find binaries.
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

# Kernel Images defaults for Clara's interactive browser runtime.
# These can be overridden by the image environment or later init templating.
export HOME="${HOME:-/root}"
export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/tmp/runtime-root}"
export ENABLE_WEBRTC="${ENABLE_WEBRTC:-true}"
export WITH_KERNEL_IMAGES_API="${WITH_KERNEL_IMAGES_API:-true}"
export ENABLE_READONLY_VIEW="${ENABLE_READONLY_VIEW:-false}"
export DISPLAY_NUM="${DISPLAY_NUM:-1}"
export WIDTH="${WIDTH:-1280}"
export HEIGHT="${HEIGHT:-720}"
export NEKO_REFRESH_RATE="${NEKO_REFRESH_RATE:-30}"

# Keep Neko's advertised display mode in sync with the virtual display size.
# The headful Kernel Images profile reads /etc/neko/neko.yaml rather than the
# Xvfb-only WIDTH/HEIGHT envs, so patch the writable rootfs config at boot.
if [ -f /etc/neko/neko.yaml ]; then
    sed -i "s/^[[:space:]]*screen:.*/  screen: \"${WIDTH}x${HEIGHT}@${NEKO_REFRESH_RATE}\"/" /etc/neko/neko.yaml 2>/dev/null || true
fi

# WebRTC NAT1TO1 — host's public IP from kernel cmdline. Without this,
# Neko advertises its bridge IP as an ICE candidate, which the remote
# browser can't reach → "peer failed".
NAT1TO1_IP=$(echo "$CMDLINE" | grep -oE 'nat1to1_ip=[^ ]+' | cut -d= -f2)
if [ -n "$NAT1TO1_IP" ]; then
    export NEKO_WEBRTC_NAT1TO1="$NAT1TO1_IP"
    echo "Neko WebRTC NAT1TO1: $NAT1TO1_IP"
fi

# WebRTC UDP port range — the orchestrator allocates a unique slice per
# browser VM so multiple VMs on the same host can stream concurrently.
# Kernel cmdline carries webrtc_port_base=X webrtc_port_end=Y.
WEBRTC_BASE=$(echo "$CMDLINE" | grep -oE 'webrtc_port_base=[0-9]+' | cut -d= -f2)
WEBRTC_END=$(echo "$CMDLINE" | grep -oE 'webrtc_port_end=[0-9]+' | cut -d= -f2)
if [ -n "$WEBRTC_BASE" ] && [ -n "$WEBRTC_END" ]; then
    export NEKO_WEBRTC_EPR="${WEBRTC_BASE}-${WEBRTC_END}"
    echo "Neko WebRTC port range: $NEKO_WEBRTC_EPR"
else
    export NEKO_WEBRTC_EPR="${NEKO_WEBRTC_EPR:-56000-56100}"
fi
export NEKO_WEBRTC_ICELITE="${NEKO_WEBRTC_ICELITE:-true}"

# Standalone mode (no Envoy xDS / kernel.com control plane):
# - Chromium listens on 127.0.0.1:9223 (INTERNAL_PORT default) — inside the VM only
# - kernel-images-api runs a DevTools WebSocket proxy on 0.0.0.0:9222 (DEVTOOLS_PROXY_PORT)
#   forwarding to Chromium at 127.0.0.1:9223. This is what the OCM CDP proxy reaches.
# - RUN_AS_ROOT=true so Chromium runs as root (PID 1 in Firecracker).
export INTERNAL_PORT="9223"
export CHROME_PORT="9222"
export DEVTOOLS_PROXY_PORT="9222"
export DEVTOOLS_PROXY_ADDR="127.0.0.1:9223"
export RUN_AS_ROOT="true"
# Leave WITHDOCKER unset so wrapper.sh manages /dev/shm itself — we don't
# mount it above for exactly this reason.
export CHROMIUM_FLAGS="${CHROMIUM_FLAGS:-} --no-sandbox --disable-dev-shm-usage"

echo "Starting Kernel browser runtime..."
echo "ENABLE_WEBRTC=${ENABLE_WEBRTC} WITH_KERNEL_IMAGES_API=${WITH_KERNEL_IMAGES_API} ENABLE_READONLY_VIEW=${ENABLE_READONLY_VIEW}"

# Upstream kernel-images PR #234 (2026-05) replaced /wrapper.sh with a Go
# binary at /wrapper. Behavior parity is intentional — same supervisord, same
# env contract (RUN_AS_ROOT, ENABLE_WEBRTC, …). Prefer the new binary; fall
# back to the legacy shell paths so older rootfs builds still boot.
if [ -x /wrapper ]; then
    exec /wrapper
fi

if [ -x /wrapper.sh ]; then
    exec /wrapper.sh
fi

if [ -x /app/wrapper.sh ]; then
    exec /app/wrapper.sh
fi

echo "ERROR: Kernel Images wrapper not found at /wrapper, /wrapper.sh, or /app/wrapper.sh"
echo "Rootfs contents:"
ls -la / /app 2>/dev/null || true

while true; do
    sleep 60
done
