#!/bin/bash
# Hermes Machines MicroVM init script.
# Injected as /sbin/overlay-init in Hermes rootfs images.

set -uo pipefail
exec 2>&1

mount -t proc proc /proc 2>/dev/null || true
[ ! -e /dev/fd ] && ln -sf /proc/self/fd /dev/fd
exec > >(tee -a /var/log/hermes-init.log) 2>&1

log() { echo "$(date -Iseconds) [$1] ${*:2}"; }
phase() { echo "=== $* ==="; }

phase "mount"
mount -t proc none /proc 2>/dev/null || true
mount -t sysfs none /sys 2>/dev/null || true
mount -t devtmpfs none /dev 2>/dev/null || true
ln -sf /proc/self/fd/0 /dev/stdin 2>/dev/null || true
ln -sf /proc/self/fd/1 /dev/stdout 2>/dev/null || true
ln -sf /proc/self/fd/2 /dev/stderr 2>/dev/null || true
mkdir -p /dev/pts /run /tmp
mount -t devpts devpts /dev/pts 2>/dev/null || true
mount -t tmpfs tmpfs /run 2>/dev/null || true
mount -t tmpfs tmpfs /tmp 2>/dev/null || true

phase "network"
CMDLINE="$(cat /proc/cmdline 2>/dev/null || true)"
if echo "$CMDLINE" | grep -q "ip="; then
    IP_CONFIG="$(echo "$CMDLINE" | grep -oE 'ip=[^ ]+' | cut -d= -f2)"
    CLIENT_IP="$(echo "$IP_CONFIG" | cut -d: -f1)"
    GATEWAY_IP="$(echo "$IP_CONFIG" | cut -d: -f3)"
    ip link set lo up 2>/dev/null || true
    ip link set eth0 up 2>/dev/null || true
    ip addr show eth0 | grep -q "$CLIENT_IP" 2>/dev/null || ip addr add "${CLIENT_IP}/24" dev eth0
    ip route | grep -q '^default ' 2>/dev/null || ip route add default via "$GATEWAY_IP"
fi
GATEWAY_IP="${GATEWAY_IP:-192.168.100.1}"
printf '%s\n' "$GATEWAY_IP" > /run/ocm-gateway-ip
chmod 444 /run/ocm-gateway-ip
printf 'nameserver 8.8.8.8\nnameserver 8.8.4.4\n' > /etc/resolv.conf 2>/dev/null || true
echo "127.0.0.1 localhost hermes" >> /etc/hosts 2>/dev/null || true
hostname hermes 2>/dev/null || true

phase "data"
if [ -b /dev/vdb ]; then
    blkid /dev/vdb >/dev/null 2>&1 || mkfs.ext4 -m 1 -q /dev/vdb
    e2fsck -p /dev/vdb >/dev/null 2>&1 || true
    mkdir -p /data
    mount -o nodev,nosuid /dev/vdb /data || { log FATAL "failed to mount /dev/vdb"; exit 1; }
else
    log FATAL "missing data volume /dev/vdb"
    exit 1
fi

SWAPFILE=/data/swapfile
if [ ! -f "$SWAPFILE" ]; then
    dd if=/dev/zero of="$SWAPFILE" bs=1M count=1024 status=none 2>/dev/null || true
    chmod 600 "$SWAPFILE"
    mkswap "$SWAPFILE" >/dev/null 2>&1 || true
fi
swapon -s | grep -q "$SWAPFILE" || swapon "$SWAPFILE" 2>/dev/null || true

phase "runtime"
mkdir -p /opt/hermes
if [ -b /dev/vdc ]; then
    mount -o rw /dev/vdc /opt/hermes || { log FATAL "failed to mount Hermes runtime /dev/vdc"; exit 1; }
else
    log FATAL "missing Hermes runtime drive /dev/vdc"
    exit 1
fi

phase "metadata"
METADATA_NONCE=""
if echo "$CMDLINE" | grep -qE 'metadata_nonce=[^ ]+'; then
    METADATA_NONCE="$(echo "$CMDLINE" | grep -oE 'metadata_nonce=[^ ]+' | cut -d= -f2)"
    echo "$METADATA_NONCE" > /run/ocm-nonce
    chmod 444 /run/ocm-nonce
fi
NONCE_ARGS=()
if [ -n "$METADATA_NONCE" ]; then
    NONCE_ARGS=(-H "X-Metadata-Nonce:${METADATA_NONCE}")
fi
METADATA_URL="http://${GATEWAY_IP}"

for i in $(seq 1 120); do
    if curl -sf "${NONCE_ARGS[@]}" "$METADATA_URL/health" >/dev/null 2>&1; then
        break
    fi
    sleep 0.5
done

MACHINE_CONFIG="$(curl -sf "${NONCE_ARGS[@]}" "$METADATA_URL/v1/machine")" || { log FATAL "metadata /v1/machine failed"; exit 1; }
SECRETS="$(curl -sf "${NONCE_ARGS[@]}" "$METADATA_URL/v1/secrets" 2>/dev/null || echo '{}')"

MACHINE_ID="$(echo "$MACHINE_CONFIG" | jq -r '.machine_id // empty')"
GATEWAY_TOKEN="$(echo "$MACHINE_CONFIG" | jq -r '.gateway_token // empty')"
SIGNING_KEY="$(echo "$MACHINE_CONFIG" | jq -r '.signing_key // empty')"
TUNNEL_TOKEN="$(echo "$MACHINE_CONFIG" | jq -r '.tunnel_token // empty')"
VM_HOSTNAME="$(echo "$MACHINE_CONFIG" | jq -r '.vm_hostname // empty')"
HERMES_BIN="$(echo "$MACHINE_CONFIG" | jq -r '.hermes_bin // "/opt/hermes/.venv/bin/hermes"')"
HERMES_VENV_DIR="$(echo "$MACHINE_CONFIG" | jq -r '.hermes_venv_dir // "/opt/hermes/.venv"')"

phase "hermes-home"
HERMES_HOME=/opt/data
mkdir -p /data/hermes
for dir in cron sessions logs hooks memories skills skins plans workspace home .local/bin; do
    mkdir -p "/data/hermes/$dir"
done
chown -R hermes:hermes /data/hermes
rm -rf /opt/data
mkdir -p /opt/data
mount --bind /data/hermes /opt/data || { log FATAL "failed to bind /data/hermes to /opt/data"; exit 1; }

write_secret_file() {
    local json_key=$1 target=$2 default_value=$3 mode=$4
    if [ -f "$target" ]; then
        return
    fi
    local value
    value="$(echo "$SECRETS" | jq -r --arg k "$json_key" '.[$k] // empty')"
    if [ -z "$value" ]; then
        value="$default_value"
    fi
    printf '%s' "$value" > "$target"
    chmod "$mode" "$target"
    chown hermes:hermes "$target"
}

write_secret_file HERMES_CONFIG_YAML /opt/data/config.yaml $'model:\n  provider: auto\nplatforms:\n  api_server:\n    enabled: true\n    host: 127.0.0.1\n    port: 18789\n' 0644
write_secret_file HERMES_ENV /opt/data/.env $'API_SERVER_ENABLED=true\nAPI_SERVER_HOST=127.0.0.1\nAPI_SERVER_PORT=18789\nHERMES_DASHBOARD=1\nHERMES_DASHBOARD_HOST=127.0.0.1\nHERMES_DASHBOARD_PORT=9119\n' 0600
write_secret_file HERMES_AUTH_JSON /opt/data/auth.json '{}' 0600
write_secret_file HERMES_SOUL_MD /opt/data/SOUL.md $'You are a concise, careful AI assistant running inside a managed Hermes Machine.\n' 0644

if grep -q '__OCM_API_PROXY_BASE_URL__\|__OCM_API_PROXY_KEY__' /opt/data/config.yaml 2>/dev/null; then
    sed -i \
        -e "s#__OCM_API_PROXY_BASE_URL__#http://${GATEWAY_IP}:4000#g" \
        -e "s#__OCM_API_PROXY_KEY__#${METADATA_NONCE}#g" \
        /opt/data/config.yaml
fi

if ! grep -q '^API_SERVER_KEY=' /opt/data/.env 2>/dev/null && [ -n "$GATEWAY_TOKEN" ]; then
    printf '\nAPI_SERVER_KEY=%s\n' "$GATEWAY_TOKEN" >> /opt/data/.env
fi

if [ -d /opt/hermes/skills ]; then
    rsync -a --ignore-existing /opt/hermes/skills/ /opt/data/skills/ || true
    chown -R hermes:hermes /opt/data/skills
fi

phase "ssh"
mkdir -p /run/sshd
ssh-keygen -A >/dev/null 2>&1 || true
/usr/sbin/sshd -t >/tmp/sshd-config-err 2>&1 && /usr/sbin/sshd || cat /tmp/sshd-config-err

phase "services"
mkdir -p /run/ocm-service-env /var/service /var/log
touch /var/log/hermes-gateway.log /var/log/hermes-dashboard.log /var/log/pty-server.log /var/log/authproxy.log /var/log/cloudflared.log
chown hermes:hermes /var/log/hermes-gateway.log /var/log/hermes-dashboard.log /var/log/pty-server.log /var/log/authproxy.log /var/log/cloudflared.log

write_envdir() {
    local dir=$1
    shift
    rm -rf "$dir"
    mkdir -p "$dir"
    for kv in "$@"; do
        local k=${kv%%=*}
        local v=${kv#*=}
        printf '%s' "$v" > "$dir/$k"
        chmod 600 "$dir/$k"
    done
    chown -R hermes:hermes "$dir"
    chmod 700 "$dir"
}

COMMON_PATH="${HERMES_VENV_DIR}/bin:/opt/data/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
write_envdir /run/ocm-service-env/hermes-gateway \
    "HOME=/opt/data/home" \
    "HERMES_HOME=/opt/data" \
    "HERMES_BIN=$HERMES_BIN" \
    "VIRTUAL_ENV=$HERMES_VENV_DIR" \
    "PATH=$COMMON_PATH" \
    "API_SERVER_KEY=$GATEWAY_TOKEN"
write_envdir /run/ocm-service-env/hermes-dashboard \
    "HOME=/opt/data/home" \
    "HERMES_HOME=/opt/data" \
    "HERMES_BIN=$HERMES_BIN" \
    "VIRTUAL_ENV=$HERMES_VENV_DIR" \
    "PATH=$COMMON_PATH" \
    "HERMES_DASHBOARD_HOST=127.0.0.1" \
    "HERMES_DASHBOARD_PORT=9119"
write_envdir /run/ocm-service-env/pty-server \
    "HOME=/opt/data/home" \
    "HERMES_HOME=/opt/data" \
    "OCM_PTY_WORKDIR=/opt/data/workspace" \
    "HERMES_BIN=$HERMES_BIN" \
    "VIRTUAL_ENV=$HERMES_VENV_DIR" \
    "PATH=$COMMON_PATH" \
    "TERM=xterm-256color" \
    "API_SERVER_KEY=$GATEWAY_TOKEN"
write_envdir /run/ocm-service-env/cloudflared \
    "TUNNEL_TOKEN=$TUNNEL_TOKEN"
write_envdir /run/ocm-service-env/authproxy \
    "SIGNING_KEY=$SIGNING_KEY" \
    "MACHINE_ID=$MACHINE_ID" \
    "OPENCLAW_GATEWAY_TOKEN=$GATEWAY_TOKEN" \
    "GATEWAY_ADDR=127.0.0.1:18789" \
    "DASHBOARD_ADDR=127.0.0.1:9119"

ln -sfn /etc/sv/hermes-gateway /var/service/hermes-gateway
ln -sfn /etc/sv/hermes-dashboard /var/service/hermes-dashboard
ln -sfn /etc/sv/pty-server /var/service/pty-server
ln -sfn /etc/sv/cloudflared /var/service/cloudflared
ln -sfn /etc/sv/authproxy /var/service/authproxy

forward_logs() {
    local src="$1" logfile="$2"
    tail -F "$logfile" 2>/dev/null | while IFS= read -r line; do
        curl -sf -X POST "$METADATA_URL/v1/logs" \
            -H "X-Metadata-Nonce: $METADATA_NONCE" \
            -H "Content-Type: application/json" \
            -d "{\"source\":\"$src\",\"line\":$(printf '%s' "$line" | jq -Rs .)}" \
            >/dev/null 2>&1 || true
    done
}
forward_logs hermes-gateway /var/log/hermes-gateway.log &
forward_logs hermes-dashboard /var/log/hermes-dashboard.log &
forward_logs pty-server /var/log/pty-server.log &
forward_logs authproxy /var/log/authproxy.log &
forward_logs cloudflared /var/log/cloudflared.log &

log INFO "handing off to runsvdir"
exec runsvdir -P /var/service
