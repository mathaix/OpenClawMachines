#!/bin/sh
# OpenClaw Browser Companion VM Init Script
# Injected as /sbin/overlay-init in the browser rootfs.
#
# This is PID 1 inside the browser companion Firecracker MicroVM.
# It mounts filesystems, configures networking from kernel cmdline,
# and exec's into headless Chromium with CDP enabled on port 9222.

exec 2>&1

# Mount essential filesystems
mount -t proc proc /proc 2>/dev/null
mount -t sysfs none /sys 2>/dev/null
mount -t devtmpfs none /dev 2>/dev/null
mount -t tmpfs tmpfs /tmp 2>/dev/null

# Create /dev/shm for Chromium (uses shared memory for tabs)
mkdir -p /dev/shm
mount -t tmpfs -o size=256m tmpfs /dev/shm

# Parse ip= from kernel cmdline (injected by Firecracker SDK)
CMDLINE=$(cat /proc/cmdline 2>/dev/null || echo "")

if echo "$CMDLINE" | grep -q "ip="; then
    IP_CONFIG=$(echo "$CMDLINE" | grep -oE 'ip=[^ ]+' | cut -d= -f2)
    CLIENT_IP=$(echo "$IP_CONFIG" | cut -d: -f1)
    GATEWAY_IP=$(echo "$IP_CONFIG" | cut -d: -f3)

    ip link set lo up
    ip link set eth0 up

    if ! ip addr show eth0 | grep -q "$CLIENT_IP"; then
        ip addr add "${CLIENT_IP}/24" dev eth0
    fi

    if ! ip route | grep -q "default"; then
        ip route add default via "$GATEWAY_IP"
    fi

    # Configure DNS (not auto-configured in minimal VM — no systemd/dhclient)
    echo "nameserver 8.8.8.8" > /etc/resolv.conf 2>/dev/null || true
    echo "nameserver 8.8.4.4" >> /etc/resolv.conf 2>/dev/null || true

    echo "Network: ${CLIENT_IP} gw ${GATEWAY_IP} dns 8.8.8.8,8.8.4.4"
fi

echo "Starting Chromium (stealth mode) on port 9222..."

# Profile directory required for --load-extension
mkdir -p /tmp/chrome-profile

# CDP port relay: --headless=new ignores --remote-debugging-address and always
# binds to 127.0.0.1. Use socat to expose CDP on 0.0.0.0:9222 for the main VM.
socat TCP-LISTEN:9222,bind=0.0.0.0,reuseaddr,fork TCP:127.0.0.1:9223 &

# Exec into Chromium — replaces PID 1
# --headless=new: full browser engine, supports extensions (Chrome 112+)
# --disable-gpu-compositing + --use-gl=swiftshader: prevent GPU process crash loops on Alpine
# Stealth extension at /opt/stealth-extension injects 13 anti-detection patches
exec chromium-browser \
    --headless=new \
    --no-sandbox \
    --disable-gpu \
    --disable-gpu-compositing \
    --use-gl=swiftshader \
    --disable-dev-shm-usage \
    --disable-software-rasterizer \
    --remote-debugging-port=9223 \
    --disable-background-networking \
    --disable-default-apps \
    --disable-sync \
    --disable-translate \
    --no-first-run \
    --mute-audio \
    --disable-blink-features=AutomationControlled \
    --window-size=1920,1080 \
    --user-agent='Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36' \
    --user-data-dir=/tmp/chrome-profile \
    --load-extension=/opt/stealth-extension \
    --lang=en-US,en \
    about:blank
