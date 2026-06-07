#!/usr/bin/env bash
# setup-spot-e2e.sh — one-time setup for the admin-only Spot E2E CI lane
# (.github/workflows/spot-e2e.yml) in YOUR fork + YOUR GCP project.
#
# Creates a Workload Identity Federation pool/provider + a CI service account
# (GitHub OIDC, no long-lived keys), grants the roles the lane needs, and sets
# the GitHub repo variables + the two WIF secrets. Idempotent — safe to re-run.
#
# Usage:
#   REPO=owner/name \
#   GCP_PROJECT=my-proj \
#   GCP_ZONE=us-west1-b \
#   DATA_PLANE_DOMAIN=example.com \
#   CLOUDFLARE_ZONE_ID=<zone id> \
#   bash ci/setup-spot-e2e.sh
#
# Optional env: SA_NAME (default ocm-e2e), POOL (github), PROVIDER (github-oidc),
#   SET_BRANCH_PROTECTION=1 to require the `spot-e2e` check on the default branch.
#
# Prerequisites: gcloud (authenticated, project owner/editor) and gh
# (authenticated as a repo admin). It does NOT create your runtime secrets — see
# the Secret Manager check near the end.
set -euo pipefail

REPO="${REPO:?Set REPO=owner/name}"
GCP_PROJECT="${GCP_PROJECT:?Set GCP_PROJECT}"
GCP_ZONE="${GCP_ZONE:-us-central1-b}"
DATA_PLANE_DOMAIN="${DATA_PLANE_DOMAIN:?Set DATA_PLANE_DOMAIN (a Cloudflare zone you control)}"
CLOUDFLARE_ZONE_ID="${CLOUDFLARE_ZONE_ID:?Set CLOUDFLARE_ZONE_ID}"
SA_NAME="${SA_NAME:-ocm-e2e}"
POOL="${POOL:-github}"
PROVIDER="${PROVIDER:-github-oidc}"
SET_BRANCH_PROTECTION="${SET_BRANCH_PROTECTION:-0}"

SA_EMAIL="${SA_NAME}@${GCP_PROJECT}.iam.gserviceaccount.com"
log() { echo "==> $*"; }

command -v gcloud >/dev/null || { echo "gcloud not found"; exit 1; }
command -v gh >/dev/null || { echo "gh not found"; exit 1; }

PROJECT_NUM="$(gcloud projects describe "$GCP_PROJECT" --format='value(projectNumber)')"

# ── 1. Enable required APIs ───────────────────────────────────────────────────
log "Enabling required APIs"
gcloud services enable \
  iam.googleapis.com iamcredentials.googleapis.com sts.googleapis.com \
  compute.googleapis.com secretmanager.googleapis.com \
  --project="$GCP_PROJECT" >/dev/null

# ── 2. Service account ────────────────────────────────────────────────────────
if ! gcloud iam service-accounts describe "$SA_EMAIL" --project="$GCP_PROJECT" >/dev/null 2>&1; then
  log "Creating service account $SA_EMAIL"
  gcloud iam service-accounts create "$SA_NAME" --project="$GCP_PROJECT" --display-name="OCM Spot E2E CI" >/dev/null
else
  log "Service account already exists: $SA_EMAIL"
fi

log "Granting roles"
for ROLE in roles/compute.instanceAdmin.v1 roles/iam.serviceAccountUser roles/secretmanager.secretAccessor; do
  gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
    --member="serviceAccount:${SA_EMAIL}" --role="$ROLE" --condition=None >/dev/null
done

# ── 3. Workload Identity Federation (GitHub OIDC) ─────────────────────────────
if ! gcloud iam workload-identity-pools describe "$POOL" --project="$GCP_PROJECT" --location=global >/dev/null 2>&1; then
  log "Creating WIF pool '$POOL'"
  gcloud iam workload-identity-pools create "$POOL" --project="$GCP_PROJECT" --location=global --display-name="GitHub Actions" >/dev/null
else
  log "WIF pool already exists: $POOL"
fi

if ! gcloud iam workload-identity-pools providers describe "$PROVIDER" \
      --project="$GCP_PROJECT" --location=global --workload-identity-pool="$POOL" >/dev/null 2>&1; then
  log "Creating WIF provider '$PROVIDER' (restricted to $REPO)"
  gcloud iam workload-identity-pools providers create-oidc "$PROVIDER" \
    --project="$GCP_PROJECT" --location=global --workload-identity-pool="$POOL" \
    --display-name="GitHub OIDC" \
    --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository" \
    --attribute-condition="assertion.repository=='${REPO}'" \
    --issuer-uri="https://token.actions.githubusercontent.com" >/dev/null
else
  log "WIF provider already exists: $PROVIDER"
fi

log "Allowing $REPO to impersonate $SA_EMAIL"
gcloud iam service-accounts add-iam-policy-binding "$SA_EMAIL" --project="$GCP_PROJECT" \
  --role=roles/iam.workloadIdentityUser \
  --member="principalSet://iam.googleapis.com/projects/${PROJECT_NUM}/locations/global/workloadIdentityPools/${POOL}/attribute.repository/${REPO}" >/dev/null

WIF_PROVIDER="projects/${PROJECT_NUM}/locations/global/workloadIdentityPools/${POOL}/providers/${PROVIDER}"

# ── 4. GitHub repo variables + WIF secrets ────────────────────────────────────
log "Setting GitHub repo variables on $REPO"
gh variable set GCP_PROJECT          --repo "$REPO" --body "$GCP_PROJECT"
gh variable set GCP_ZONE             --repo "$REPO" --body "$GCP_ZONE"
gh variable set E2E_DATA_PLANE_DOMAIN --repo "$REPO" --body "$DATA_PLANE_DOMAIN"
gh variable set E2E_CLOUDFLARE_ZONE_ID --repo "$REPO" --body "$CLOUDFLARE_ZONE_ID"

log "Setting GitHub repo secrets (WIF identifiers, not credentials)"
gh secret set GCP_WIF_PROVIDER   --repo "$REPO" --body "$WIF_PROVIDER"
gh secret set GCP_SERVICE_ACCOUNT --repo "$REPO" --body "$SA_EMAIL"

# ── 5. Check the runtime secrets the lane reads from Secret Manager ───────────
log "Checking Secret Manager for the runtime secrets the lane reads"
MISSING=()
for S in OCM_CLOUDFLARE_API_TOKEN OCM_CLOUDFLARE_ACCOUNT_ID OCM_CLOUDFLARE_KV_NAMESPACE_ID \
         OCM_FC_AGENT_TOKEN OCM_SECRET_ENCRYPTION_KEY OCM_JWT_SECRET; do
  gcloud secrets describe "$S" --project="$GCP_PROJECT" >/dev/null 2>&1 || MISSING+=("$S")
done
if [ "${#MISSING[@]}" -gt 0 ]; then
  echo "WARNING: missing Secret Manager secrets (create them before running the lane):"
  printf '   - %s\n' "${MISSING[@]}"
  echo "   e.g.: printf %s \"<value>\" | gcloud secrets create <NAME> --project=$GCP_PROJECT --data-file=-"
fi

# ── 6. Optional: require the spot-e2e check on the default branch ──────────────
if [ "$SET_BRANCH_PROTECTION" = "1" ]; then
  DEFAULT_BRANCH="$(gh repo view "$REPO" --json defaultBranchRef --jq .defaultBranchRef.name)"
  log "Requiring 'spot-e2e' status check on ${DEFAULT_BRANCH}"
  gh api -X PUT "repos/${REPO}/branches/${DEFAULT_BRANCH}/protection" \
    -H "Accept: application/vnd.github+json" \
    -f 'required_status_checks[strict]=true' \
    -f 'required_status_checks[contexts][]=spot-e2e' \
    -F 'enforce_admins=false' \
    -F 'required_pull_request_reviews=null' \
    -F 'restrictions=null' >/dev/null
fi

echo ""
echo "════════════════════════════════════════════════════════════"
echo "Spot E2E CI setup complete for ${REPO}"
echo "  WIF provider: ${WIF_PROVIDER}"
echo "  Service acct: ${SA_EMAIL}"
echo ""
echo "Run it (as a repo admin) to gate a PR:"
echo "  gh workflow run spot-e2e.yml --repo ${REPO} -f pr=<PR#> -f sha=<HEAD_SHA>"
if [ "$SET_BRANCH_PROTECTION" != "1" ]; then
  echo ""
  echo "To require it for merges: add the 'spot-e2e' check under"
  echo "  Settings -> Branches -> <default branch> (or re-run with SET_BRANCH_PROTECTION=1)."
fi
echo "════════════════════════════════════════════════════════════"
