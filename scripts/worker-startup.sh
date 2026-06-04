#!/bin/bash
# Worker fleet startup script for GCE spot instances
#
# This script runs on instance boot via GCE startup-script metadata.
# It downloads the backend binary from GCS, configures environment,
# and starts the OCM worker as a systemd service.
#
# Prerequisites:
#   - Instance metadata: worker-manifest (JSON), database-url
#   - GCS access: storage-ro scope on service account

set -euo pipefail

LOG_TAG="ocm-worker-startup"
log() { logger -t "$LOG_TAG" "$@"; echo "[$(date -u +%H:%M:%S)] $*"; }

log "Starting OCM worker setup..."

# --- Install dependencies ---
if ! command -v jq >/dev/null 2>&1; then
    log "Installing jq..."
    apt-get update -qq && apt-get install -y -qq jq
fi

# --- Fetch metadata ---
META_BASE="http://metadata.google.internal/computeMetadata/v1"
META_HEADER="Metadata-Flavor: Google"

fetch_meta() {
    curl -sf -H "$META_HEADER" "$META_BASE/$1"
}

INSTANCE_NAME=$(fetch_meta "instance/name")
ZONE=$(fetch_meta "instance/zone" | awk -F/ '{print $NF}')

log "Instance: ${INSTANCE_NAME} (${ZONE})"

# --- GCS helper (gsutil with gcloud fallback) ---
gcs_cat() {
    if command -v gsutil >/dev/null 2>&1; then
        gsutil cat "$1"
    elif command -v gcloud >/dev/null 2>&1; then
        gcloud storage cat "$1" --quiet
    else
        return 1
    fi
}

# --- Download worker binary from GCS ---
# Fetch manifest from GCS (always gets latest), with instance metadata fallback
GCS_MANIFEST=$(fetch_meta "instance/attributes/worker-manifest-gcs" 2>/dev/null || echo "gs://openclawmachines/worker/manifest.json")
MANIFEST_JSON=$(gcs_cat "$GCS_MANIFEST" 2>/dev/null || fetch_meta "instance/attributes/worker-manifest")
VERSION=$(echo "$MANIFEST_JSON" | jq -r .version)
URL=$(echo "$MANIFEST_JSON" | jq -r .url)
SHA=$(echo "$MANIFEST_JSON" | jq -r .sha256)

log "Downloading worker binary v${VERSION}..."
BINARY_PATH="/usr/local/bin/ocm-backend"

# Use gcloud storage if available, fall back to gsutil
if command -v gcloud >/dev/null 2>&1; then
    gcloud storage cp "$URL" "$BINARY_PATH" --quiet
elif command -v gsutil >/dev/null 2>&1; then
    gsutil -q cp "$URL" "$BINARY_PATH"
else
    log "ERROR: neither gcloud nor gsutil found"
    exit 1
fi

# Verify integrity
echo "$SHA  $BINARY_PATH" | sha256sum -c - || {
    log "ERROR: SHA256 mismatch — binary integrity check failed"
    exit 1
}
chmod +x "$BINARY_PATH"
log "Binary verified: v${VERSION}"

# --- Fetch secrets ---
DATABASE_URL=$(fetch_meta "instance/attributes/database-url")

# Required for migration workflows (agent, KV, tunnel, encryption)
AGENT_TOKEN=$(fetch_meta "instance/attributes/agent-token" 2>/dev/null || echo "")
CLOUDFLARE_API_TOKEN=$(fetch_meta "instance/attributes/cloudflare-api-token" 2>/dev/null || echo "")
CLOUDFLARE_ACCOUNT_ID=$(fetch_meta "instance/attributes/cloudflare-account-id" 2>/dev/null || echo "")
CLOUDFLARE_ZONE_ID=$(fetch_meta "instance/attributes/cloudflare-zone-id" 2>/dev/null || echo "")
CLOUDFLARE_KV_NAMESPACE_ID=$(fetch_meta "instance/attributes/cloudflare-kv-namespace-id" 2>/dev/null || echo "")
SECRET_ENCRYPTION_KEY=$(fetch_meta "instance/attributes/secret-encryption-key" 2>/dev/null || echo "")
GCP_SECRET_NAME=$(fetch_meta "instance/attributes/gcp-secret-name" 2>/dev/null || echo "")
BACKUP_MASTER_KEY=$(fetch_meta "instance/attributes/backup-master-key" 2>/dev/null || echo "")
GCP_REGION=$(fetch_meta "instance/attributes/gcp-region" 2>/dev/null || echo "us-central1")

# Optional: Resend API key for email workflows
RESEND_API_KEY=$(fetch_meta "instance/attributes/resend-api-key" 2>/dev/null || echo "")

# Optional: Frontend URL for email links
FRONTEND_URL=$(fetch_meta "instance/attributes/frontend-url" 2>/dev/null || echo "https://openclawmachines.com")

# --- Create systemd service ---
log "Configuring systemd service..."

# Use EnvironmentFile to avoid systemd % specifier issues in DATABASE_URL
cat > /etc/ocm-worker.env <<ENVEOF
RUN_MODE=worker
DATABASE_URL=${DATABASE_URL}
EXECUTOR_ID=${INSTANCE_NAME}-${ZONE}
ENABLE_DURABLE_WORKFLOWS=1
PORT=8080
FC_AGENT_TOKEN=${AGENT_TOKEN}
CLOUDFLARE_API_TOKEN=${CLOUDFLARE_API_TOKEN}
CLOUDFLARE_ACCOUNT_ID=${CLOUDFLARE_ACCOUNT_ID}
CLOUDFLARE_ZONE_ID=${CLOUDFLARE_ZONE_ID}
CLOUDFLARE_KV_NAMESPACE_ID=${CLOUDFLARE_KV_NAMESPACE_ID}
SECRET_ENCRYPTION_KEY=${SECRET_ENCRYPTION_KEY}
GCP_SECRET_NAME=${GCP_SECRET_NAME}
BACKUP_MASTER_KEY=${BACKUP_MASTER_KEY}
GCP_REGION=${GCP_REGION}
RESEND_API_KEY=${RESEND_API_KEY}
FRONTEND_URL=${FRONTEND_URL}
ENVEOF
chmod 600 /etc/ocm-worker.env

cat > /etc/systemd/system/ocm-worker.service <<EOF
[Unit]
Description=OCM Workflow Worker
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${BINARY_PATH}
EnvironmentFile=/etc/ocm-worker.env
Restart=always
RestartSec=5
TimeoutStopSec=35

[Install]
WantedBy=multi-user.target
EOF

# TimeoutStopSec=35 gives DBOS 30s for graceful shutdown + 5s buffer
# (matches GCE preemption warning window)

systemctl daemon-reload
systemctl enable ocm-worker
systemctl restart ocm-worker

log "OCM worker started successfully (v${VERSION})"
