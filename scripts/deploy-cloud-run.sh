#!/bin/bash
# OCM Cloud Run Deployment Script
# Deploys both frontend and backend to Google Cloud Run
#
# HINT: Use 'make deploy-all' instead of calling this script directly.
#
set -e

# Warn if not called via make
if [ -z "${MAKEFLAGS:-}" ] && [ -z "${VIA_MAKE:-}" ]; then
    echo -e "\033[1;33m[HINT]\033[0m Consider using 'make deploy-all' instead of calling this script directly."
    echo -e "       This ensures consistent workflow. See CLAUDE.md for details.\n"
fi

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() { echo -e "${GREEN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# Configuration
export GCP_PROJECT=${GCP_PROJECT:-clarateach}
export GCP_REGION=${GCP_REGION:-us-central1}
export GCP_ZONE=${GCP_ZONE:-us-central1-a}
export REGISTRY="us-central1-docker.pkg.dev/$GCP_PROJECT/ocm"

# Determine what to deploy
DEPLOY_BACKEND=false
DEPLOY_FRONTEND=false

usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  --backend     Deploy backend only"
    echo "  --frontend    Deploy frontend only"
    echo "  --all         Deploy backend + frontend (default if no option specified)"
    echo "  --help        Show this help message"
    echo ""
    echo "Environment variables:"
    echo "  GCP_PROJECT       GCP project ID (default: clarateach)"
    echo "  GCP_REGION        GCP region (default: us-central1)"
    echo "  FC_SNAPSHOT_NAME  Firecracker snapshot name"
    exit 0
}

# Parse arguments
if [[ $# -eq 0 ]]; then
    DEPLOY_BACKEND=true
    DEPLOY_FRONTEND=true
fi

while [[ $# -gt 0 ]]; do
    case $1 in
        --backend)
            DEPLOY_BACKEND=true
            shift
            ;;
        --frontend)
            DEPLOY_FRONTEND=true
            shift
            ;;
        --all)
            DEPLOY_BACKEND=true
            DEPLOY_FRONTEND=true
            shift
            ;;
        --help)
            usage
            ;;
        *)
            error "Unknown option: $1"
            ;;
    esac
done

# Get script directory and project root
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Read snapshot name from .snapshot file (required for backend)
SNAPSHOT_FILE="$PROJECT_ROOT/.snapshot"
if [[ "$DEPLOY_BACKEND" == "true" ]]; then
    if [[ ! -f "$SNAPSHOT_FILE" ]]; then
        error "Missing .snapshot file at $SNAPSHOT_FILE. Create it with: make set-snapshot NAME=<name>"
    fi
    export FC_SNAPSHOT_NAME=${FC_SNAPSHOT_NAME:-$(head -1 "$SNAPSHOT_FILE" | tr -d '[:space:]')}
    if [[ -z "$FC_SNAPSHOT_NAME" ]]; then
        error ".snapshot file is empty. Add the snapshot name to deploy."
    fi
fi

echo "=========================================="
echo "OCM Cloud Run Deployment"
echo "=========================================="
echo "Project:   $GCP_PROJECT"
echo "Region:    $GCP_REGION"
[[ "$DEPLOY_BACKEND" == "true" ]] && echo "Snapshot:  $FC_SNAPSHOT_NAME"
echo "Backend:   $DEPLOY_BACKEND"
echo "Frontend:  $DEPLOY_FRONTEND"
echo "=========================================="
echo ""

# Authenticate Docker with GCP
log "Authenticating Docker with GCP Artifact Registry..."
gcloud auth configure-docker us-central1-docker.pkg.dev --quiet

# Deploy Backend
if [[ "$DEPLOY_BACKEND" == "true" ]]; then
    log "Building backend..."
    cd "$PROJECT_ROOT/backend"

    # Get version info for build
    GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
    BUILD_TIME=$(date -u +%Y%m%dT%H%M%SZ)
    VERSION="${GIT_COMMIT}-${BUILD_TIME}"
    log "Building with version: $VERSION (commit: $GIT_COMMIT)"

    docker build --platform linux/amd64 \
        --build-arg GIT_COMMIT="$GIT_COMMIT" \
        --build-arg BUILD_TIME="$BUILD_TIME" \
        --build-arg VERSION="$VERSION" \
        -t "$REGISTRY/backend:latest" .

    log "Pushing backend image..."
    docker push "$REGISTRY/backend:latest"

    # Custom domains (Cloudflare proxied)
    CUSTOM_FRONTEND_URL="https://openclawmachines.com"
    CUSTOM_BACKEND_URL="https://api.openclawmachines.com"

    log "BACKEND_URL: $CUSTOM_BACKEND_URL"
    log "FRONTEND_URL: $CUSTOM_FRONTEND_URL"

    log "Deploying backend to Cloud Run..."
    # Use ^;^ delimiter so commas in CORS_ORIGINS are treated as literal values
    gcloud run deploy ocm-backend \
        --image="$REGISTRY/backend:latest" \
        --region="$GCP_REGION" \
        --platform=managed \
        --allow-unauthenticated \
        --port=8080 \
        --memory=512Mi \
        --cpu=1 \
        --min-instances=1 \
        --max-instances=1 \
        --timeout=3600 \
        --set-env-vars="^;^RUN_MODE=api;GCP_PROJECT=$GCP_PROJECT;GCP_ZONE=$GCP_ZONE;GCP_REGISTRY=$REGISTRY;FC_SNAPSHOT_NAME=$FC_SNAPSHOT_NAME;CORS_ORIGINS=$CUSTOM_FRONTEND_URL,https://www.openclawmachines.com;BACKEND_URL=$CUSTOM_BACKEND_URL;PUBLIC_URL=$CUSTOM_BACKEND_URL;FRONTEND_URL=$CUSTOM_FRONTEND_URL;COOKIE_DOMAIN=.openclawmachines.com;GCP_SECRET_NAME=projects/$GCP_PROJECT/secrets/OCM_SECRET_ENCRYPTION_KEY/versions/latest;AUTH_MODE=firebase;FIREBASE_PROJECT_ID=clarateach;CF_ACCESS_TEAM_DOMAIN=openclawmachines;ROOTFS_GCS_MANIFEST=gs://openclawmachines/rootfs/manifest.json;AGENT_GCS_MANIFEST=gs://openclawmachines/agent/manifest.json;BROWSER_ROOTFS_GCS_MANIFEST=gs://openclawmachines/kernel-browser-rootfs/manifest.json;BROWSER_ROOTFS_VERSION=9a80fb2-20260411T202541Z;ENABLE_DURABLE_WORKFLOWS=1;FF_RUNTIME_VERSION_RESOLVER=1;OPENCLAW_GCS_MANIFEST=gs://openclawmachines/openclaw/manifest-stable.json;HERMES_GCS_MANIFEST=gs://openclawmachines/hermes/manifest-stable.json;HERMES_ROOTFS_GCS_MANIFEST=gs://openclawmachines/hermes-rootfs/manifest.json" \
        --set-secrets="DATABASE_URL=OCM_DATABASE_URL:latest,FC_AGENT_TOKEN=OCM_FC_AGENT_TOKEN:latest,WORKSPACE_TOKEN_SECRET=OCM_WORKSPACE_TOKEN_SECRET:latest,JWT_SECRET=OCM_JWT_SECRET:latest,SECRET_ENCRYPTION_KEY=OCM_SECRET_ENCRYPTION_KEY:latest,GOOGLE_CLIENT_ID=OCM_GOOGLE_CLIENT_ID:latest,GOOGLE_CLIENT_SECRET=OCM_GOOGLE_CLIENT_SECRET:latest,GITHUB_CLIENT_ID=OCM_GITHUB_CLIENT_ID:latest,GITHUB_CLIENT_SECRET=OCM_GITHUB_CLIENT_SECRET:latest,CLOUDFLARE_API_TOKEN=OCM_CLOUDFLARE_API_TOKEN:latest,CLOUDFLARE_ACCOUNT_ID=OCM_CLOUDFLARE_ACCOUNT_ID:latest,CLOUDFLARE_ZONE_ID=OCM_CLOUDFLARE_ZONE_ID:latest,CLOUDFLARE_KV_NAMESPACE_ID=OCM_CLOUDFLARE_KV_NAMESPACE_ID:latest,CF_SERVICE_TOKEN_ID=OCM_CF_SERVICE_TOKEN_ID:latest,CF_SERVICE_TOKEN_SECRET=OCM_CF_SERVICE_TOKEN_SECRET:latest,CF_ACCESS_AUD=CF_ACCESS_AUD:latest,CF_SSH_CA_PUBKEY=CF_SSH_CA_PUBKEY:latest,OCM_SSH_CA_PRIVATE_KEY=OCM_SSH_CA_PRIVATE_KEY:latest,GCS_SERVICE_ACCOUNT_KEY=OCM_GCS_SERVICE_ACCOUNT_KEY:latest,BACKUP_MASTER_KEY=OCM_BACKUP_MASTER_KEY:latest,RESEND_API_KEY=RESEND_API_KEY:latest,NEBIUS_API_KEY=NEBIUS_API_KEY:latest,OPIK_API_KEY=OPIK_OBSERVE_KEY:latest,COMPOSIO_CONSUMER_KEY=COMPOSIO_CONSUMER_KEY:latest"

    log "Backend deployed: $CUSTOM_BACKEND_URL"
fi

# Deploy Frontend
if [[ "$DEPLOY_FRONTEND" == "true" ]]; then
    log "Building frontend..."
    cd "$PROJECT_ROOT/frontend"
    docker build --platform linux/amd64 \
        --build-arg VITE_API_URL="/api" \
        --build-arg VITE_CF_TEAM_DOMAIN="openclawmachines" \
        --build-arg VITE_GA_MEASUREMENT_ID="G-DJFG4QVL15" \
        --build-arg VITE_FIREBASE_API_KEY="AIzaSyDRvdzUkZ5yLTEA6XBavoUjm85u8SVFTU4" \
        --build-arg VITE_FIREBASE_AUTH_DOMAIN="clarateach.firebaseapp.com" \
        --build-arg VITE_FIREBASE_PROJECT_ID="clarateach" \
        -t "$REGISTRY/frontend:latest" .

    log "Pushing frontend image..."
    docker push "$REGISTRY/frontend:latest"

    log "Deploying frontend to Cloud Run..."
    gcloud run deploy ocm-frontend \
        --image="$REGISTRY/frontend:latest" \
        --region="$GCP_REGION" \
        --platform=managed \
        --allow-unauthenticated \
        --port=8080 \
        --memory=256Mi \
        --cpu=1 \
        --min-instances=0 \
        --max-instances=10

    FRONTEND_URL=$(gcloud run services describe ocm-frontend \
        --region="$GCP_REGION" \
        --format='value(status.url)')

    log "Frontend deployed: $FRONTEND_URL"
fi

# Verification
echo ""
echo "=========================================="
log "Verifying deployment..."
echo "=========================================="

if [[ "$DEPLOY_BACKEND" == "true" ]]; then
    BACKEND_URL=$(gcloud run services describe ocm-backend \
        --region="$GCP_REGION" \
        --format='value(status.url)')
    echo -n "Backend health: "
    HEALTH_RESPONSE=$(curl -s "$BACKEND_URL/health")
    echo "$HEALTH_RESPONSE" | jq -r '"OK (version: \(.version), commit: \(.git_commit))"' 2>/dev/null || echo "$HEALTH_RESPONSE"
fi

if [[ "$DEPLOY_FRONTEND" == "true" ]]; then
    FRONTEND_URL=$(gcloud run services describe ocm-frontend \
        --region="$GCP_REGION" \
        --format='value(status.url)')
    echo -n "Frontend status: "
    curl -s -o /dev/null -w "HTTP %{http_code}" "$FRONTEND_URL"
    echo ""
fi

echo ""
echo "=========================================="
echo -e "${GREEN}Deployment Complete${NC}"
echo "=========================================="
[[ "$DEPLOY_FRONTEND" == "true" ]] && echo "Frontend:  $FRONTEND_URL"
[[ "$DEPLOY_BACKEND" == "true" ]] && echo "Backend:   $BACKEND_URL"
echo "=========================================="
