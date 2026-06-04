#!/bin/bash
set -euo pipefail

# OCM Snapshot Creation Script
# Creates a GCP snapshot from the OCM build VM after updating agent and optionally rootfs
#
# Usage:
#   ./scripts/create-snapshot.sh [OPTIONS]
#
# Options:
#   --vm=NAME       VM to use (default: ocm)
#   --zone=ZONE     GCP zone (default: us-central1-b)
#   --full          Full build: upload rootfs + agent (default: agent only)
#   --deploy        Deploy to Cloud Run after snapshot
#   --no-restart    Don't restart the VM after snapshot
#
# Examples:
#   ./scripts/create-snapshot.sh                    # Agent-only snapshot from ocm
#   ./scripts/create-snapshot.sh --vm=ocm2          # Agent-only snapshot from ocm2
#   ./scripts/create-snapshot.sh --full --deploy    # Full build + deploy

# Defaults
VM="${VM:-ocm}"
ZONE="${ZONE:-us-central1-b}"
FULL_BUILD=false
DEPLOY=false
RESTART=true

# Parse arguments
for arg in "$@"; do
    case $arg in
        --vm=*)
            VM="${arg#*=}"
            ;;
        --zone=*)
            ZONE="${arg#*=}"
            ;;
        --full)
            FULL_BUILD=true
            ;;
        --deploy)
            DEPLOY=true
            ;;
        --no-restart)
            RESTART=false
            ;;
        --help)
            head -20 "$0" | tail -15
            exit 0
            ;;
        *)
            echo "Unknown option: $arg"
            exit 1
            ;;
    esac
done

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info() { echo -e "${GREEN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

echo "=========================================="
echo "OCM Snapshot Creation"
echo "=========================================="
echo "VM: ${VM}"
echo "Zone: ${ZONE}"
echo "Mode: $(if $FULL_BUILD; then echo 'Full (rootfs + agent)'; else echo 'Quick (agent only)'; fi)"
echo "Deploy: ${DEPLOY}"
echo "=========================================="
echo ""

# Step 1: Check VM exists and start if needed
info "Checking ${VM} status..."
STATUS=$(gcloud compute instances describe "${VM}" --zone="${ZONE}" --format="value(status)" 2>/dev/null || echo "NOT_FOUND")

if [ "$STATUS" == "NOT_FOUND" ]; then
    error "VM ${VM} not found in zone ${ZONE}"
fi

if [ "$STATUS" != "RUNNING" ]; then
    info "Starting ${VM}..."
    gcloud compute instances start "${VM}" --zone="${ZONE}"
    info "Waiting for SSH..."
    sleep 30
fi

# Wait for SSH to be ready
for i in {1..10}; do
    if gcloud compute ssh "mathewma@${VM}" --zone="${ZONE}" --command="echo ready" 2>/dev/null; then
        break
    fi
    echo "Waiting for SSH... attempt $i"
    sleep 10
done

# Step 2: Build agent + authproxy locally
info "Building agent..."
make build-agent

info "Building authproxy..."
make build-authproxy

# Step 3: Upload agent + authproxy
info "Uploading agent to ${VM}..."
gcloud compute scp backend/agent-linux "mathewma@${VM}:/tmp/agent" --zone="${ZONE}"

info "Uploading authproxy to ${VM}..."
gcloud compute scp backend/authproxy "mathewma@${VM}:/tmp/authproxy" --zone="${ZONE}"

# Step 4: Full build - upload and build rootfs
if $FULL_BUILD; then
    info "Packaging workspace files..."
    tar -czf /tmp/workspace-upload.tar.gz rootfs/ scripts/init-openclaw.sh scripts/build-rootfs.sh scripts/ocm-metadata scripts/ocm-test-llm scripts/ocm-env scripts/ocm-status scripts/ocm-migrate scripts/cf-ssh-check scripts/migrations/ Makefile

    info "Uploading workspace files to ${VM}..."
    gcloud compute scp /tmp/workspace-upload.tar.gz "mathewma@${VM}:/tmp/" --zone="${ZONE}"

    info "Building rootfs on ${VM}..."
    GIT_SHORT=$(git rev-parse --short HEAD)
    gcloud compute ssh "mathewma@${VM}" --zone="${ZONE}" --command="
        cd /tmp
        tar -xzf workspace-upload.tar.gz
        # Install agent + authproxy to /usr/local/bin BEFORE build-rootfs.sh
        # (build-rootfs.sh injects them from /usr/local/bin into the rootfs image)
        sudo cp /tmp/agent /usr/local/bin/agent && sudo chmod +x /usr/local/bin/agent
        sudo cp /tmp/authproxy /usr/local/bin/authproxy && sudo chmod +x /usr/local/bin/authproxy
        sudo OCM_SNAPSHOT=${GIT_SHORT} bash scripts/build-rootfs.sh
    "
    rm /tmp/workspace-upload.tar.gz
fi

# Step 5: Install agent on VM
info "Installing agent on ${VM}..."
gcloud compute ssh "mathewma@${VM}" --zone="${ZONE}" --command="
    set -e

    # Stop agent
    sudo systemctl stop ocm-agent 2>/dev/null || true

    # Remove old agent and install new one
    sudo rm -f /usr/local/bin/agent
    sudo mv /tmp/agent /usr/local/bin/agent
    sudo chmod +x /usr/local/bin/agent
    echo 'Agent installed'

    # Install authproxy (runs inside MicroVMs for per-VM tunnel auth)
    sudo rm -f /usr/local/bin/authproxy
    sudo mv /tmp/authproxy /usr/local/bin/authproxy
    sudo chmod +x /usr/local/bin/authproxy
    echo 'Authproxy installed'

    # Write version file for debugging (readable from inside MicroVMs too)
    AGENT_VERSION=\$(strings /usr/local/bin/agent | grep -oE '[a-f0-9]{7}-[0-9]{8}T[0-9]{6}Z' | head -1)
    echo \"\${AGENT_VERSION:-unknown}\" | sudo tee /etc/ocm-agent-version > /dev/null
    echo \"Agent version: \${AGENT_VERSION:-unknown}\"

    # Verify XFS mount
    if mountpoint -q /var/lib/ocm/vms 2>/dev/null; then
        echo 'XFS mounted at /var/lib/ocm/vms'
    else
        echo 'WARNING: XFS not mounted at /var/lib/ocm/vms'
    fi

    # Create systemd mount unit for data disk
    if [ -b /dev/disk/by-id/google-ocm-data ] || true; then
        echo 'Setting up data disk systemd units...'

        # First-boot format service
        sudo tee /etc/systemd/system/ocm-data-init.service > /dev/null << 'FMTEOF'
[Unit]
Description=Initialize OCM Data Disk
Before=var-lib-ocm-data.mount
ConditionPathExists=/dev/disk/by-id/google-ocm-data

[Service]
Type=oneshot
ExecStart=/bin/bash -c 'blkid /dev/disk/by-id/google-ocm-data || mkfs.ext4 -m 1 /dev/disk/by-id/google-ocm-data'
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
FMTEOF

        # Mount unit
        sudo tee /etc/systemd/system/var-lib-ocm-data.mount > /dev/null << 'MNTEOF'
[Unit]
Description=OCM Data Persistent Disk
After=ocm-data-init.service
Requires=ocm-data-init.service
Before=ocm-agent.service

[Mount]
What=/dev/disk/by-id/google-ocm-data
Where=/var/lib/ocm/data
Type=ext4
Options=defaults,nofail

[Install]
WantedBy=multi-user.target
MNTEOF

        sudo systemctl daemon-reload
        sudo systemctl enable ocm-data-init.service
        sudo systemctl enable var-lib-ocm-data.mount
        echo 'Data disk systemd units installed'
    fi

    # Clean up for snapshot
    sudo rm -rf /var/lib/ocm/vms/rootfs-*.ext4 2>/dev/null || true
    sudo rm -rf /tmp/*.tar.gz /tmp/*.ext4 2>/dev/null || true

    echo 'Installation complete'
"

# Step 6: Create snapshot
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
SNAPSHOT_NAME="ocm-${TIMESTAMP}"

info "Stopping ${VM} for snapshot..."
gcloud compute instances stop "${VM}" --zone="${ZONE}"

info "Waiting for ${VM} to stop..."
sleep 30

info "Creating snapshot: ${SNAPSHOT_NAME}..."
gcloud compute snapshots create "${SNAPSHOT_NAME}" \
    --source-disk="${VM}" \
    --source-disk-zone="${ZONE}" \
    --description="OCM snapshot from ${VM}"

info "Creating image from snapshot: ${SNAPSHOT_NAME}..."
gcloud compute images create "${SNAPSHOT_NAME}" \
    --source-snapshot="${SNAPSHOT_NAME}" \
    --description="OCM snapshot from ${VM}"

echo ""
echo "=========================================="
echo -e "${GREEN}Snapshot + image created: ${SNAPSHOT_NAME}${NC}"
echo "=========================================="

# Step 7: Restart VM
if $RESTART; then
    info "Restarting ${VM}..."
    gcloud compute instances start "${VM}" --zone="${ZONE}"
fi

# Step 8: Update .snapshot file
echo "${SNAPSHOT_NAME}" > .snapshot
info "Updated .snapshot to: ${SNAPSHOT_NAME}"

# Step 9: Commit
if [ -n "$(git status --porcelain .snapshot)" ]; then
    git add .snapshot
    git commit -m "ci: Update snapshot to ${SNAPSHOT_NAME}"
    git push origin "$(git branch --show-current)" || warn "Failed to push, please push manually"
fi

# Step 10: Deploy
if $DEPLOY; then
    info "Deploying backend..."
    make deploy-backend
fi

echo ""
echo "=========================================="
echo -e "${GREEN}Complete!${NC}"
echo "=========================================="
echo "Snapshot: ${SNAPSHOT_NAME}"
echo ""
echo "To deploy manually:"
echo "  make deploy-backend"
