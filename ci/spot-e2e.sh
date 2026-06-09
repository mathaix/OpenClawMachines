#!/usr/bin/env bash
# spot-e2e.sh — provision a real GCE spot host, run the E2E + Playwright suite
# against it, and tear everything down. Designed to run in CI (admin-gated) but
# also locally for an admin who has gcloud auth + the required env.
#
# Required env (the CI workflow sets these after GCP auth + Secret Manager reads):
#   GCP_PROJECT, GCP_ZONE, DATA_PLANE_DOMAIN,
#   CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID, CLOUDFLARE_ZONE_ID,
#   CLOUDFLARE_KV_NAMESPACE_ID, FC_AGENT_TOKEN, SECRET_ENCRYPTION_KEY, JWT_SECRET
# Optional:
#   MACHINE_TYPE (default n2-standard-2), HOST_READY_TIMEOUT (default 900s),
#   OCM_ARTIFACT_BUCKET / OCM_ARTIFACT_BASE_URL (artifact source override)
#
# Artifacts (Playwright report, screenshots, traces) are left in
# frontend/playwright-report and frontend/test-results for the workflow to upload.
set -euo pipefail
cd "$(dirname "$0")/.."

: "${GCP_PROJECT:?}"; : "${GCP_ZONE:=us-central1-b}"; : "${DATA_PLANE_DOMAIN:?}"
: "${CLOUDFLARE_API_TOKEN:?}"; : "${CLOUDFLARE_ACCOUNT_ID:?}"; : "${CLOUDFLARE_ZONE_ID:?}"
: "${FC_AGENT_TOKEN:?}"; : "${SECRET_ENCRYPTION_KEY:?}"
MACHINE_TYPE="${MACHINE_TYPE:-n2-standard-2}"
HOST_READY_TIMEOUT="${HOST_READY_TIMEOUT:-900}"
API="http://localhost:8080"

log() { echo "==> $*"; }

TUNNEL_PID=""; BACKEND_PID=""; FRONTEND_PID=""; MACHINE_ID=""; HOST_ID=""
cleanup() {
  ec=$?
  set +e
  # On failure, surface the diagnostics the lane otherwise swallows (host
  # provisioning errors live in the backend log + the host record's status
  # message, not in this script's stdout).
  if [ "$ec" -ne 0 ]; then
    log "FAILURE (exit ${ec}) — diagnostics:"
    echo "----- /tmp/backend.log (tail 80) -----";  tail -n 80 /tmp/backend.log  2>/dev/null
    echo "----- /tmp/cf.log (tail 20) -----";       tail -n 20 /tmp/cf.log       2>/dev/null
    echo "----- /tmp/frontend.log (tail 20) -----"; tail -n 20 /tmp/frontend.log 2>/dev/null
    echo "----- host records -----";                curl -s -m5 "$API/api/admin/hosts" 2>/dev/null | python3 -m json.tool 2>/dev/null
    echo "--------------------------------------"
  fi
  log "TEARDOWN"
  [ -n "$MACHINE_ID" ] && curl -s -m20 -X DELETE "$API/api/accounts/1/machines/$MACHINE_ID" >/dev/null 2>&1
  if [ -n "$HOST_ID" ]; then
    curl -s -m30 -X DELETE "$API/api/admin/hosts/$HOST_ID" >/dev/null 2>&1
    # Wait briefly for the spot instance to be deleted (DestroyHost is async).
    for _ in $(seq 1 12); do
      n="$(gcloud compute instances list --project="$GCP_PROJECT" --filter='name~ocm-host AND status!=TERMINATED' --format='value(name)' 2>/dev/null | wc -l | tr -d ' ')"
      [ "$n" = "0" ] && break; sleep 10
    done
  fi
  # Backstop: delete any ocm-host instance this run may have leaked.
  for inst in $(gcloud compute instances list --project="$GCP_PROJECT" --filter='name~ocm-host AND status!=TERMINATED' --format='value(name,zone)' 2>/dev/null | awk '{print $1":"$2}'); do
    gcloud compute instances delete "${inst%%:*}" --zone="${inst##*:}" --project="$GCP_PROJECT" --quiet >/dev/null 2>&1
  done
  [ -n "$BACKEND_PID" ] && kill "$BACKEND_PID" 2>/dev/null
  [ -n "$FRONTEND_PID" ] && kill "$FRONTEND_PID" 2>/dev/null
  [ -n "$TUNNEL_PID" ] && kill "$TUNNEL_PID" 2>/dev/null
  docker rm -f ocm-e2e-pg >/dev/null 2>&1
  log "teardown done"
}
trap cleanup EXIT

# ── 1. Postgres + migrations ──────────────────────────────────────────────────
log "starting Postgres"
docker run -d --name ocm-e2e-pg -e POSTGRES_USER=ocm -e POSTGRES_PASSWORD=ocm -e POSTGRES_DB=ocm -p 5432:5432 postgres:16 >/dev/null
export DATABASE_URL="postgres://ocm:ocm@localhost:5432/ocm?sslmode=disable"
for _ in $(seq 1 30); do docker exec ocm-e2e-pg pg_isready -U ocm >/dev/null 2>&1 && break; sleep 2; done
bash scripts/run-migrations.sh

# Stage the latest stable artifact releases so FF_RUNTIME_VERSION_RESOLVER can
# resolve the guest rootfs + openclaw runtime versions (it reads artifact_releases
# from the DB; the fetcher builds the GCS URI from the *_GCS_MANIFEST {version}
# templates + the resolved version). Versions = newest stable in gs://openclawmachines.
log "seeding artifact_releases (latest stable rootfs + openclaw)"
docker exec -i ocm-e2e-pg psql -v ON_ERROR_STOP=1 -U ocm -d ocm >/dev/null <<'SQL'
INSERT INTO artifact_releases (kind, version, channel, url, sha256, size_bytes) VALUES
  ('rootfs','ce1c3df-20260601T214501Z','stable','gs://openclawmachines/rootfs/rootfs-ce1c3df-20260601T214501Z.ext4.zst','9092ca21760c90818d078064622bfa37193e826ec1a199e976fb084bb7047b4a',3828350976),
  ('openclaw','v2026.5.28-r4','stable','gs://openclawmachines/openclaw/releases/v2026.5.28-r4/openclaw-v2026.5.28-r4-linux-amd64.tar.zst','79ae823476afd6ad42a0656be58d67dfdadbbb2279c70f8337ad5c4c76fc2ebe',156995560)
ON CONFLICT (kind, version) DO NOTHING;
SQL

# ── 2. cloudflared tunnel for a publicly-reachable BACKEND_URL ────────────────
log "starting cloudflared tunnel"
cloudflared tunnel --url http://localhost:8080 >/tmp/cf.log 2>&1 & TUNNEL_PID=$!
for _ in $(seq 1 20); do
  # `|| true`: under `set -euo pipefail`, grep exits 1 while /tmp/cf.log has no
  # URL yet, which would abort the whole script on the first poll instead of
  # waiting for cloudflared to come up.
  BACKEND_URL="$(grep -oE 'https://[a-z0-9-]+\.trycloudflare\.com' /tmp/cf.log | head -1 || true)"
  [ -n "$BACKEND_URL" ] && break; sleep 2
done
[ -n "${BACKEND_URL:-}" ] || { echo "no tunnel URL"; exit 1; }
export BACKEND_URL
log "BACKEND_URL=$BACKEND_URL"

# ── 3. Control plane (operator profile, dev auth, spot) ───────────────────────
log "building + starting control plane"
( cd backend && go build -o ./server ./cmd/server )
export CONTROL_PLANE_PROFILE=operator AUTH_MODE=dev OCM_ALLOW_DEV_AUTH=1 \
  OCM_ADMIN_EMAILS=dev@localhost OCM_SUPERUSER_EMAILS=dev@localhost \
  PORT=8080 FRONTEND_URL=http://localhost:5173 CORS_ORIGINS=http://localhost:5173 \
  HOST_PROVISIONING_MODEL=SPOT FF_RUNTIME_VERSION_RESOLVER=1
# Host artifacts (agent + guest kernel/rootfs/openclaw) are pulled from the GCS
# bucket; the spot host reads them via its default-SA devstorage.read_only scope.
# AGENT_GCS_MANIFEST pins the host agent; the {version} templates are filled by
# the fetcher from the resolved (staged) release versions above.
export OCM_ARTIFACT_BUCKET=gs://openclawmachines \
  AGENT_GCS_MANIFEST=gs://openclawmachines/agent/manifest-299a7fe-20260527T142532Z.json \
  ROOTFS_GCS_MANIFEST=gs://openclawmachines/rootfs/manifest-ce1c3df-20260601T214501Z.json \
  OPENCLAW_GCS_MANIFEST='gs://openclawmachines/openclaw/releases/{version}/manifest.json'
./backend/server >/tmp/backend.log 2>&1 & BACKEND_PID=$!
for _ in $(seq 1 30); do curl -fsS -m3 "$API/health" >/dev/null 2>&1 && break; sleep 2; done

# ── 4. Frontend (Vite dev server, proxies /api) ───────────────────────────────
log "starting frontend"
( cd frontend; npm ci >/dev/null 2>&1 || npm install >/dev/null 2>&1 )
cat > frontend/.env.local <<EOF
VITE_API_URL=/api
VITE_DATA_PLANE_DOMAIN=$DATA_PLANE_DOMAIN
VITE_OCM_ADMIN_EMAILS=dev@localhost
EOF
( cd frontend && npm run dev >/tmp/frontend.log 2>&1 ) & FRONTEND_PID=$!
for _ in $(seq 1 30); do curl -fsS -m3 http://localhost:5173 >/dev/null 2>&1 && break; sleep 2; done

# ── 5. Provision a spot host, wait for ready ──────────────────────────────────
log "provisioning spot host ($MACHINE_TYPE in $GCP_ZONE)"
curl -fsS -m20 -X POST "$API/api/admin/hosts" -H 'Content-Type: application/json' \
  -d "{\"machine_type\":\"$MACHINE_TYPE\",\"zone\":\"$GCP_ZONE\"}" >/dev/null
deadline=$(( $(date +%s) + HOST_READY_TIMEOUT ))
while :; do
  # `|| true`: a transient empty/non-JSON response makes the pipeline (and thus
  # `read`) exit non-zero; without this `set -e` would abort instead of polling
  # until the deadline below.
  read -r HOST_ID STATUS < <(curl -s -m5 "$API/api/admin/hosts" | python3 -c 'import sys,json;d=json.load(sys.stdin);h=max(d,key=lambda x:x["id"]) if d else None;print((str(h["id"])+" "+h["status"]) if h else "0 none")') || true
  log "host $HOST_ID: $STATUS"
  [ "$STATUS" = "ready" ] && break
  [ "$STATUS" = "error" ] && { echo "host provisioning failed"; exit 1; }
  [ "$(date +%s)" -ge "$deadline" ] && { echo "timeout waiting for host ready"; exit 1; }
  sleep 15
done

# ── 6. Playwright E2E (drives create -> Firecracker boot, captures on failure) ─
log "running Playwright E2E"
export PLAYWRIGHT_BASE_URL=http://localhost:5173 CI=1 DEBIAN_FRONTEND=noninteractive
( cd frontend
  # Browser only — the GitHub runner image already ships the OS libs. `--with-deps`
  # runs apt and hung the job to the 45m timeout (leaking the spot host). Time-bound
  # both steps so a stall fails fast (→ trap tears the host down) instead of hanging.
  # Chrome download from cdn.playwright.dev can be slow/stall on cold CI runners;
  # retry a few bounded attempts (the workflow also caches ~/.cache/ms-playwright
  # so warm runs are instant).
  pw_ok=0
  for i in 1 2 3; do
    if timeout 240 npx playwright install chromium; then pw_ok=1; break; fi
    echo "playwright install attempt $i timed out/failed; retrying"
  done
  [ "$pw_ok" = 1 ] || { echo "playwright install failed after retries"; exit 1; }
  timeout 600 npx playwright test e2e/spot-smoke.spec.ts --project=chromium-dev --reporter=html )

log "E2E passed"
