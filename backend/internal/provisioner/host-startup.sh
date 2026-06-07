#!/bin/bash
# OCM host startup-script — runs once on first boot of a stock Ubuntu GCE VM
# when no prebaked image (FC_SNAPSHOT_NAME) is configured. It installs the
# Firecracker host dependencies and the ocm-agent, then starts the agent, which
# self-configures from the GCE metadata the provisioner injected (agent-token,
# backend-url, host-id, tunnel-token, manifests). Artifacts (agent binary +
# guest kernel) are pulled from the operator's OCM_ARTIFACT_BUCKET using the
# instance's default service account (devstorage.read_only scope).
#
# This makes GCP provisioning work for ANY operator without a golden image.
set -euo pipefail
exec > >(tee /var/log/ocm-startup.log) 2>&1
echo "[ocm-startup] begin $(date -u)"

FIRECRACKER_VERSION="v1.10.1"
OCM_BASE="/var/lib/ocm"

MD="http://metadata.google.internal/computeMetadata/v1/instance/attributes"
md() { curl -s -H "Metadata-Flavor: Google" "$MD/$1" 2>/dev/null || true; }

AGENT_TOKEN="$(md agent-token)"
BACKEND_URL="$(md backend-url)"
HOST_ID="$(md host-id)"
TUNNEL_TOKEN="$(md tunnel-token)"
ROOTFS_MANIFEST="$(md rootfs-gcs-manifest)"
AGENT_MANIFEST="$(md agent-gcs-manifest)"
BROWSER_ROOTFS_MANIFEST="$(md browser-rootfs-gcs-manifest)"
BROWSER_ROOTFS_VERSION="$(md browser-rootfs-version)"
ARTIFACT_BUCKET="$(md artifact-bucket)"
KERNEL_GCS_PATH="$(md kernel-gcs-path)"

if [ -z "$ARTIFACT_BUCKET" ] && [ -z "$AGENT_MANIFEST" ]; then
    echo "[ocm-startup] FATAL: no artifact-bucket or agent-gcs-manifest metadata; cannot fetch the agent"
    exit 1
fi
[ -z "$KERNEL_GCS_PATH" ] && KERNEL_GCS_PATH="${ARTIFACT_BUCKET%/}/vmlinux"

# ── 1. System packages ────────────────────────────────────────────────────────
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq curl jq zstd iptables iproute2 net-tools \
    xfsprogs e2fsprogs cpu-checker ca-certificates gnupg >/dev/null

# Google Cloud SDK (gsutil) for artifact downloads via the instance service account
if ! command -v gsutil >/dev/null 2>&1; then
    curl -sS https://packages.cloud.google.com/apt/doc/apt-key.gpg | gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg
    echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" > /etc/apt/sources.list.d/google-cloud-sdk.list
    apt-get update -qq && apt-get install -y -qq google-cloud-cli >/dev/null
fi

# ── 2. KVM + nested virt ──────────────────────────────────────────────────────
[ -e /dev/kvm ] || modprobe kvm_amd 2>/dev/null || modprobe kvm_intel 2>/dev/null || true
if [ ! -e /dev/kvm ]; then echo "[ocm-startup] FATAL: /dev/kvm missing — enable nested virtualization"; exit 1; fi
chmod 666 /dev/kvm
if grep -q AMD /proc/cpuinfo; then echo "kvm_amd" > /etc/modules-load.d/kvm.conf; else echo "kvm_intel" > /etc/modules-load.d/kvm.conf; fi

# ── 3. Firecracker ────────────────────────────────────────────────────────────
ARCH="$(uname -m)"; FC_ARCH="$ARCH"
if [ ! -f /usr/local/bin/firecracker ]; then
    cd /tmp
    curl -sL "https://github.com/firecracker-microvm/firecracker/releases/download/${FIRECRACKER_VERSION}/firecracker-${FIRECRACKER_VERSION}-${FC_ARCH}.tgz" -o fc.tgz
    tar xzf fc.tgz
    cp "release-${FIRECRACKER_VERSION}-${FC_ARCH}/firecracker-${FIRECRACKER_VERSION}-${FC_ARCH}" /usr/local/bin/firecracker
    cp "release-${FIRECRACKER_VERSION}-${FC_ARCH}/jailer-${FIRECRACKER_VERSION}-${FC_ARCH}" /usr/local/bin/jailer
    chmod +x /usr/local/bin/firecracker /usr/local/bin/jailer
    rm -rf fc.tgz "release-${FIRECRACKER_VERSION}-${FC_ARCH}"
fi

# ── 4. cloudflared ────────────────────────────────────────────────────────────
if [ ! -f /usr/local/bin/cloudflared ]; then
    if [ "$FC_ARCH" = "aarch64" ]; then CF_ARCH="arm64"; else CF_ARCH="amd64"; fi
    curl -sL "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${CF_ARCH}" -o /usr/local/bin/cloudflared
    chmod +x /usr/local/bin/cloudflared
fi

# ── 5. Networking + storage dirs ──────────────────────────────────────────────
cat > /etc/sysctl.d/99-ocm.conf <<'SYSCTL'
net.ipv4.ip_forward = 1
net.netfilter.nf_conntrack_max = 131072
fs.file-max = 1048576
fs.inotify.max_user_instances = 8192
fs.inotify.max_user_watches = 524288
SYSCTL
sysctl --system >/dev/null 2>&1 || true
mkdir -p "${OCM_BASE}"/{images,sockets,data,vms} /etc/ocm-agent

# ── 6. Guest kernel ───────────────────────────────────────────────────────────
if [ ! -f "${OCM_BASE}/vmlinux" ]; then
    echo "[ocm-startup] fetching kernel from ${KERNEL_GCS_PATH}"
    gsutil cp "$KERNEL_GCS_PATH" "${OCM_BASE}/vmlinux" && chmod 644 "${OCM_BASE}/vmlinux" \
        || echo "[ocm-startup] WARNING: kernel download failed; supply ${OCM_BASE}/vmlinux"
fi

# ── 7. Agent binary ───────────────────────────────────────────────────────────
AGENT_SRC=""
if [ -n "$AGENT_MANIFEST" ]; then
    AGENT_SRC="$(gsutil cat "$AGENT_MANIFEST" 2>/dev/null | jq -r '.url // empty')"
fi
[ -z "$AGENT_SRC" ] && AGENT_SRC="${ARTIFACT_BUCKET%/}/agent/ocm-agent"
echo "[ocm-startup] fetching agent from ${AGENT_SRC}"
gsutil cp "$AGENT_SRC" /usr/local/bin/ocm-agent
chmod +x /usr/local/bin/ocm-agent

# ── 8. Agent env + systemd unit ───────────────────────────────────────────────
cat > /etc/ocm-agent/agent.env <<EOF
FC_AGENT_TOKEN=${AGENT_TOKEN}
BACKEND_URL=${BACKEND_URL}
HOST_ID=${HOST_ID}
TUNNEL_TOKEN=${TUNNEL_TOKEN}
ROOTFS_GCS_MANIFEST=${ROOTFS_MANIFEST}
AGENT_GCS_MANIFEST=${AGENT_MANIFEST}
BROWSER_ROOTFS_GCS_MANIFEST=${BROWSER_ROOTFS_MANIFEST}
BROWSER_ROOTFS_VERSION=${BROWSER_ROOTFS_VERSION}
STATE_DIR=${OCM_BASE}/vms
EOF

cat > /etc/systemd/system/ocm-agent.service <<'EOF'
[Unit]
Description=OCM Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/ocm-agent/agent.env
ExecStart=/usr/local/bin/ocm-agent
Restart=always
RestartSec=5
LimitNOFILE=65536
# Keep Firecracker children alive across agent restarts/self-updates.
KillMode=process
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now ocm-agent
echo "[ocm-startup] done $(date -u) — agent started"
