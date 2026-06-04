# Deployment Lifecycle: Dev → Staging → Production

**Date:** 2026-02-09
**Status:** Research (not yet implemented)
**Problem:** Everything deploys straight to production. No staging, no preview, no safe way to test changes end-to-end before users see them.

---

## Current State: Everything Is Production

### What Exists Today

```
make deploy-all
  ├── deploy-backend   → Cloud Run (ocm-backend, us-central1)
  ├── deploy-frontend  → Cloud Run (ocm-frontend, us-central1)
  └── deploy-worker    → Cloudflare Worker (openclawmachines.com/*)

/snapshot skill
  └── Build agent + rootfs → GCE snapshot → deploy backend with new FC_SNAPSHOT_NAME
```

Every deploy goes directly to production. There is one database, one Worker, one set of hosts, one domain.

### External Service Dependencies (All Single-Instance)

| Service | Production Instance | Environment Config |
|---------|--------------------|--------------------|
| Cloud Run (backend) | `ocm-backend` → `api.openclawmachines.com` | Hardcoded URLs in `scripts/deploy-cloud-run.sh` |
| Cloud Run (frontend) | `ocm-frontend` → `openclawmachines.com` | `VITE_API_URL=https://api.openclawmachines.com/api` |
| Cloudflare Worker | Routes `*.openclawmachines.com/*` | `HOST_MAP` hardcoded in `worker/worker.js` |
| Cloudflare KV | Namespace `7c9e3e7fb99e4a10afeaf581d2b9bd7f` | Single namespace, no staging |
| Neon PostgreSQL | Single database, `DATABASE_URL` | One connection string in Secret Manager |
| GCE Worker VMs | Provisioned from `.snapshot` | Single snapshot name, single pool |
| Cloudflare Tunnels | `ocm-host-*.openclawmachines.com` | All under production domain |
| GCP Secret Manager | `OCM_*` secrets | One set of secrets |
| Artifact Registry | `us-central1-docker.pkg.dev/clarateach/ocm` | Single registry |

### What's Hardcoded

**`scripts/deploy-cloud-run.sh`** — production URLs baked in:
```bash
CORS_ORIGINS=https://openclawmachines.com,https://www.openclawmachines.com
BACKEND_URL=https://api.openclawmachines.com
FRONTEND_URL=https://openclawmachines.com
COOKIE_DOMAIN=.openclawmachines.com
```

**`worker/worker.js`** — `HOST_MAP` hardcoded:
```javascript
const HOST_MAP = {
  "openclawmachines.com": "ocm-frontend-864969804676.us-central1.run.app",
  "www.openclawmachines.com": "ocm-frontend-864969804676.us-central1.run.app",
  "api.openclawmachines.com": "ocm-backend-864969804676.us-central1.run.app",
};
```

**`worker/wrangler.toml`** — routes locked to production:
```toml
routes = [
  { pattern = "openclawmachines.com/*", zone_name = "openclawmachines.com" },
  { pattern = "api.openclawmachines.com/*", zone_name = "openclawmachines.com" },
  { pattern = "*.openclawmachines.com/*", zone_name = "openclawmachines.com" },
]
```

**`frontend/Dockerfile`** — API URL baked at build time:
```dockerfile
ARG VITE_API_URL=https://api.openclawmachines.com/api
```

---

## Target State: Three Environments

### Environment Matrix

| | Production | Staging | Preview (per-PR) |
|---|---|---|---|
| **Domain** | `openclawmachines.com` | `ocmstg.dev` (separate domain) | Skip |
| **Backend** | `ocm-backend` | `ocm-backend-staging` | `ocm-backend-pr-N` (no-traffic) |
| **Frontend** | `ocm-frontend` | `ocm-frontend-staging` | `ocm-frontend-pr-N` (no-traffic) |
| **Worker** | `[env.production]` | `[env.staging]` | Share staging worker |
| **KV** | `ocm-routes-production` | `ocm-routes-staging` | Share staging KV |
| **Database** | Neon `main` branch | Neon `staging` branch | Neon `pr-N` branch (ephemeral) |
| **Worker VMs** | Production host pool | Dedicated staging host (optional) | None (too expensive) |
| **Snapshot** | `ocm-snapshot-*` | Same as prod (usually) | N/A |
| **Tunnels** | `ocm-host-*.openclawmachines.com` | `ocm-host-*.ocmstg.dev` | N/A |
| **Secrets** | `OCM_*` | `OCM_STAGING_*` | Inherit staging |

### Domain Strategy: Separate Staging Domain

**Decision: Use a separate domain for staging** (e.g., `ocmstg.dev`, ~$12/year).

**Why not subdomains under `openclawmachines.com`?**

Two problems:

1. **Wildcard SSL cost.** Production uses `*.openclawmachines.com` for machine routing (`{acct}.openclawmachines.com`). Staging under the same domain would need `{acct}.staging.openclawmachines.com` — a second-level wildcard (`*.staging.openclawmachines.com`) that requires Cloudflare Advanced Certificate Manager ($10/month) or a custom cert. Not worth it.

2. **Discoverability.** If a user stumbles onto `staging.openclawmachines.com`, it looks like a broken version of the real product. A separate domain like `ocmstg.dev` means nothing to anyone who finds it.

**Routing mirrors production exactly:**

```
Production:                              Staging:
  openclawmachines.com                     ocmstg.dev
  api.openclawmachines.com                 api.ocmstg.dev
  {acct}.openclawmachines.com/{machine}    {acct}.ocmstg.dev/{machine}
```

Same Worker code, same routing logic, same single-level wildcard cert (`*.ocmstg.dev`), completely isolated Cloudflare zone. No slug conflicts because each domain has its own Worker deployment, KV namespace, and database branch.

### Machine Slug Isolation

Machine slugs are scoped per-account in the DB (`UNIQUE (account_id, slug)`), and account slugs are globally unique. With separate Neon branches, staging and production have **independent databases** — so the same account "mathaix" with the same machine "my-vm" can exist in both without conflict.

**No routing collision** because:
- Production Worker routes `*.openclawmachines.com` → production KV → production backend
- Staging Worker routes `*.ocmstg.dev` → staging KV → staging backend
- Different Cloudflare zones, different KV namespaces, different databases
- Even if the same account slug exists in both, the Worker resolves against different backends

**Tunnel isolation:**
- Production hosts: `ocm-host-*.openclawmachines.com` (CNAME → tunnel in prod zone)
- Staging hosts: `ocm-host-*.ocmstg.dev` (CNAME → tunnel in staging zone)
- Separate Cloudflare tunnels, separate DNS zones, zero overlap

---

## Component-by-Component: What Changes

### 1. Cloudflare Worker — Separate Deployments Per Zone

Since staging uses a separate Cloudflare zone (`ocmstg.dev`), the Worker is deployed independently to each zone. Wrangler environments handle this cleanly — same code, different config.

**`worker/wrangler.toml`** changes:

```toml
name = "ocm-worker"
main = "worker.js"
compatibility_date = "2024-01-01"

# Shared bindings
[vars]
ENVIRONMENT = "development"

# --- Production (openclawmachines.com zone) ---
[env.production]
routes = [
  { pattern = "openclawmachines.com/*", zone_name = "openclawmachines.com" },
  { pattern = "api.openclawmachines.com/*", zone_name = "openclawmachines.com" },
  { pattern = "*.openclawmachines.com/*", zone_name = "openclawmachines.com" },
]
[env.production.vars]
ENVIRONMENT = "production"
DOMAIN = "openclawmachines.com"
[[env.production.kv_namespaces]]
binding = "OCM_ROUTES"
id = "production-kv-namespace-id"

# --- Staging (ocmstg.dev zone) ---
[env.staging]
routes = [
  { pattern = "ocmstg.dev/*", zone_name = "ocmstg.dev" },
  { pattern = "api.ocmstg.dev/*", zone_name = "ocmstg.dev" },
  { pattern = "*.ocmstg.dev/*", zone_name = "ocmstg.dev" },
]
[env.staging.vars]
ENVIRONMENT = "staging"
DOMAIN = "ocmstg.dev"
[[env.staging.kv_namespaces]]
binding = "OCM_ROUTES"
id = "staging-kv-namespace-id"
```

**`worker/worker.js`** changes — derive `HOST_MAP` from `DOMAIN` env var:

```javascript
function getHostMap(env) {
  const domain = env.DOMAIN || "openclawmachines.com";
  return {
    [domain]: `ocm-frontend${env.ENVIRONMENT === "staging" ? "-staging" : ""}-*.run.app`,
    [`www.${domain}`]: `ocm-frontend${env.ENVIRONMENT === "staging" ? "-staging" : ""}-*.run.app`,
    [`api.${domain}`]: `ocm-backend${env.ENVIRONMENT === "staging" ? "-staging" : ""}-*.run.app`,
  };
}
```

The subdomain routing logic (`{acct}.domain/{machine}/...`) works identically — the Worker strips the account slug from the hostname regardless of which domain it is.

**Deploy:**
```bash
make deploy-worker-staging   # npx wrangler deploy --env staging
make deploy-worker           # npx wrangler deploy --env production
```

**Worker secrets per environment:**
```bash
npx wrangler secret put JWT_SECRET --env staging
npx wrangler secret put JWT_SECRET --env production
```

### 2. Cloud Run — Staging Services

Deploy separate Cloud Run services with `-staging` suffix.

**`scripts/deploy-cloud-run.sh`** changes — parameterize:

```bash
ENV=${1:-production}  # production | staging

if [ "$ENV" = "staging" ]; then
  BACKEND_SERVICE="ocm-backend-staging"
  FRONTEND_SERVICE="ocm-frontend-staging"
  CORS_ORIGINS="https://ocmstg.dev,https://www.ocmstg.dev"
  BACKEND_URL="https://api.ocmstg.dev"
  FRONTEND_URL="https://ocmstg.dev"
  COOKIE_DOMAIN=".ocmstg.dev"
  DB_SECRET="OCM_STAGING_DATABASE_URL"
else
  BACKEND_SERVICE="ocm-backend"
  FRONTEND_SERVICE="ocm-frontend"
  CORS_ORIGINS="https://openclawmachines.com,https://www.openclawmachines.com"
  BACKEND_URL="https://api.openclawmachines.com"
  FRONTEND_URL="https://openclawmachines.com"
  COOKIE_DOMAIN=".openclawmachines.com"
  DB_SECRET="OCM_DATABASE_URL"
fi
```

**Frontend build** — API URL per environment:

```bash
# Staging
docker build --build-arg VITE_API_URL=https://api.ocmstg.dev/api ...

# Production
docker build --build-arg VITE_API_URL=https://api.openclawmachines.com/api ...
```

**Makefile targets:**
```makefile
deploy-backend-staging:
	./scripts/deploy-cloud-run.sh staging backend

deploy-frontend-staging:
	./scripts/deploy-cloud-run.sh staging frontend

deploy-staging: deploy-backend-staging deploy-frontend-staging deploy-worker-staging
```

**Cloud Run traffic splitting** for canary/blue-green:
```bash
# Deploy new revision with 0% traffic
gcloud run deploy ocm-backend --image=... --no-traffic --tag=canary

# Test it
curl https://canary---ocm-backend-*.run.app/health

# Shift 10% traffic
gcloud run services update-traffic ocm-backend --to-tags=canary=10

# Full rollout
gcloud run services update-traffic ocm-backend --to-latest

# Instant rollback
gcloud run services update-traffic ocm-backend --to-revisions=PREV=100
```

### 3. Neon Database — Branching

Neon's copy-on-write branching is the key enabler. Branch creation is instant regardless of database size, and you only pay for data that diverges from the parent.

**Branch structure:**

```
main (production)
  └── staging (long-lived, reset weekly)
        └── pr-123 (ephemeral, auto-deleted on PR close)
        └── pr-456 (ephemeral)
```

**Setup:**
```bash
# Create staging branch (one-time)
neonctl branches create --name staging --parent main

# Get connection string
neonctl connection-string staging
# → postgres://user:pass@ep-staging-xxx.us-east-2.aws.neon.tech/neondb

# Store in Secret Manager
gcloud secrets create OCM_STAGING_DATABASE_URL \
  --data-file=<(echo "postgres://...")
```

**Weekly staging reset** (cron or manual):
```bash
neonctl branches delete staging
neonctl branches create --name staging --parent main
# Update Secret Manager with new connection string if endpoint changed
```

**PR preview databases** (GitHub Actions):
```yaml
on:
  pull_request:
    types: [opened, synchronize]
jobs:
  create-preview-db:
    steps:
      - run: |
          neonctl branches create \
            --name pr-${{ github.event.pull_request.number }} \
            --parent staging
```

```yaml
on:
  pull_request:
    types: [closed]
jobs:
  cleanup-preview-db:
    steps:
      - run: |
          neonctl branches delete pr-${{ github.event.pull_request.number }}
```

**Migration strategy:**

Forward-only migrations. Never write "down" migrations.

```
1. Run migration on staging branch first (make migrate ENV=staging)
2. Test on staging
3. Run migration on main (make migrate ENV=production)
4. Deploy backend
```

If a migration needs to be undone, write a new forward migration that reverses it.

### 4. Worker VMs & Tunnels — The Hard Part

Worker VMs are stateful (Firecracker snapshots, data volumes, running user machines). They can't be swapped instantly like Cloud Run revisions.

**Three options:**

**Option A: Shared VM pool, environment via metadata (cheapest)**

Staging and production share the same worker VMs. The backend determines which environment a machine belongs to based on which backend API provisioned it.

```
Production backend → PlaceMachine() → finds host (source_image matches)
Staging backend    → PlaceMachine() → finds SAME hosts

Both use same agent, same snapshot, same tunnel.
Isolation is at the database level — staging machines are in the staging DB.
```

**Problem:** A bad staging deploy could affect production VMs on the same host. Agent crash = both environments down.

**Option B: Separate staging host (safer, costs ~$30-50/month)**

Provision one dedicated staging host from the same snapshot.

```
Production:
  Hosts: ocm-prod-1, ocm-prod-2, ...
  Tunnels: ocm-host-*.openclawmachines.com

Staging:
  Host: ocm-staging-1
  Tunnel: ocm-host-staging.ocmstg.dev
```

The staging backend's `FC_SNAPSHOT_NAME` and provisioner config point to the staging host. Staging machines run on the staging host only. Tunnel CNAME is in the `ocmstg.dev` zone — completely isolated from production DNS.

**Setup:**
```bash
# Provision staging host manually or via API
# Use same snapshot as production
# Create Cloudflare tunnel in ocmstg.dev zone
```

**Option C: No staging VMs (simplest)**

Staging environment has backend + frontend + worker + database but NO worker VMs. Machine provisioning returns an error in staging.

```
Staging tests:
  ✓ Auth flow (OAuth, JWT)
  ✓ Machine CRUD (create, list, update, delete in DB)
  ✓ UI/UX (all pages, modals, forms)
  ✓ API endpoints (all routes)
  ✗ Actually starting a VM (skipped)
  ✗ Terminal/browser proxy (skipped)
  ✗ LLM proxy (skipped)
```

**Recommendation:** Start with **Option C** (no staging VMs). It covers 80% of testing needs. Add **Option B** (one staging host) when you need to test snapshot or agent changes before production.

### 5. Cloudflare DNS — Staging Zone

Add `ocmstg.dev` as a new zone in Cloudflare (free plan supports multiple zones). DNS records mirror the production zone structure:

```
# ocmstg.dev zone
ocmstg.dev                 → Worker handles (frontend)
www.ocmstg.dev              → Worker handles (frontend)
api.ocmstg.dev              → Worker handles (backend)
*.ocmstg.dev                → Worker handles (machine routing)

# If using staging host (Option B):
ocm-host-staging.ocmstg.dev → CNAME → tunnel-id.cfargotunnel.com
```

**Cloudflare free plan** supports unlimited zones. The `*.ocmstg.dev` wildcard cert is covered by Cloudflare's free Universal SSL (single-level wildcard). No Advanced Certificate Manager needed.

**Cost:** Domain registration (~$12/year for `.dev`). Cloudflare hosting is free.

### 6. GCP Secret Manager — Per-Environment Secrets

```
Production:                          Staging:
  OCM_DATABASE_URL                     OCM_STAGING_DATABASE_URL
  OCM_JWT_SECRET                       OCM_STAGING_JWT_SECRET
  OCM_FC_AGENT_TOKEN                   OCM_STAGING_FC_AGENT_TOKEN
  OCM_SECRET_ENCRYPTION_KEY            OCM_STAGING_SECRET_ENCRYPTION_KEY
  ...                                  ...
```

Or use separate GCP projects:
```
clarateach (production)     → OCM_* secrets
clarateach-staging          → OCM_* secrets (same names, different values)
```

**Recommendation:** Same project, `_STAGING_` prefix. Simpler IAM, one billing account.

### 7. OAuth — Staging Redirect URIs

Google and GitHub OAuth apps need staging redirect URIs:

```
Google OAuth:
  Authorized redirect URIs:
    https://api.openclawmachines.com/api/auth/google/callback       (production)
    https://api.ocmstg.dev/api/auth/google/callback                 ← ADD (staging)

GitHub OAuth:
  Option A: Add staging callback to same OAuth app
  Option B: Create separate GitHub OAuth app for staging (cleaner)
```

**Recommendation:** Same OAuth app, add staging callback URLs. Fewer credentials to manage.

---

## Deployment Orchestration

### Current: Manual `make` commands

```bash
make deploy-all  # deploys everything to production, YOLO
```

### Target: CI/CD with Environment Gates

```
Push to main ──→ Deploy to staging automatically
                    │
                    ├── Run integration tests
                    ├── Run E2E tests (Puppeteer)
                    │
                    └── Manual approval gate
                          │
                          └── Deploy to production
```

### GitHub Actions Workflow

```yaml
# .github/workflows/deploy.yml
name: Deploy

on:
  push:
    branches: [main]

jobs:
  # 1. Detect what changed
  changes:
    runs-on: ubuntu-latest
    outputs:
      backend: ${{ steps.filter.outputs.backend }}
      frontend: ${{ steps.filter.outputs.frontend }}
      worker: ${{ steps.filter.outputs.worker }}
      migrations: ${{ steps.filter.outputs.migrations }}
    steps:
      - uses: dorny/paths-filter@v3
        id: filter
        with:
          filters: |
            backend:
              - 'backend/**'
            frontend:
              - 'frontend/**'
            worker:
              - 'worker/**'
            migrations:
              - 'backend/migrations/**'

  # 2. Run migrations on staging first
  migrate-staging:
    needs: changes
    if: needs.changes.outputs.migrations == 'true'
    runs-on: ubuntu-latest
    steps:
      - run: make migrate ENV=staging

  # 3. Deploy to staging (parallel)
  deploy-staging:
    needs: [changes, migrate-staging]
    if: always() && !failure()
    runs-on: ubuntu-latest
    steps:
      - if: needs.changes.outputs.backend == 'true'
        run: make deploy-backend-staging
      - if: needs.changes.outputs.frontend == 'true'
        run: make deploy-frontend-staging
      - if: needs.changes.outputs.worker == 'true'
        run: make deploy-worker-staging

  # 4. Integration tests against staging
  test-staging:
    needs: deploy-staging
    runs-on: ubuntu-latest
    steps:
      - run: make test-e2e ENV=staging

  # 5. Manual approval
  approve-production:
    needs: test-staging
    runs-on: ubuntu-latest
    environment: production  # GitHub environment with required reviewers
    steps:
      - run: echo "Approved for production"

  # 6. Deploy to production
  migrate-production:
    needs: [approve-production, changes]
    if: needs.changes.outputs.migrations == 'true'
    runs-on: ubuntu-latest
    steps:
      - run: make migrate ENV=production

  deploy-production:
    needs: [approve-production, migrate-production, changes]
    if: always() && !failure() && needs.approve-production.result == 'success'
    runs-on: ubuntu-latest
    steps:
      - if: needs.changes.outputs.backend == 'true'
        run: make deploy-backend
      - if: needs.changes.outputs.frontend == 'true'
        run: make deploy-frontend
      - if: needs.changes.outputs.worker == 'true'
        run: make deploy-worker

  # 7. Smoke test production
  smoke-test:
    needs: deploy-production
    runs-on: ubuntu-latest
    steps:
      - run: |
          curl -sf https://api.openclawmachines.com/health | jq .
          curl -sf https://openclawmachines.com | head -1
```

### Snapshot Pipeline (Separate, Manual)

Snapshots are expensive and infrequent. Keep them manual with a dedicated workflow:

```yaml
# .github/workflows/snapshot.yml
name: Create Snapshot

on:
  workflow_dispatch:
    inputs:
      mode:
        description: 'Build mode'
        type: choice
        options: [quick, full]
      deploy_staging:
        description: 'Deploy to staging after snapshot'
        type: boolean
        default: true

jobs:
  snapshot:
    runs-on: ubuntu-latest
    steps:
      - run: make snapshot-${{ inputs.mode }}
      - if: inputs.deploy_staging
        run: make deploy-backend-staging
```

---

## Rollback Strategy

### By Component

| Component | Rollback Speed | How |
|-----------|---------------|-----|
| Cloud Run (backend/frontend) | **Instant** (seconds) | `gcloud run services update-traffic --to-revisions=PREV=100` |
| Cloudflare Worker | **Instant** (seconds) | `npx wrangler rollback --env production` or Wrangler versions |
| Database migration | **Minutes** | Write + deploy forward migration that reverses the change |
| Snapshot (worker VMs) | **Gradual** (minutes-hours) | Update `.snapshot`, redeploy backend. Running VMs unaffected until restart |

### Rollback Scenarios

**Bad backend deploy:**
```bash
# Instant — shift all traffic back to previous revision
gcloud run services update-traffic ocm-backend \
  --to-revisions=ocm-backend-PREVIOUS=100 --region=us-central1
```

**Bad migration:**
```sql
-- Write a new migration that undoes the damage
-- backend/migrations/010_undo_009.sql
ALTER TABLE machines DROP COLUMN data_volume_gb;
```
```bash
make migrate ENV=production
make deploy-backend  # redeploy without the code that uses dropped column
```

**Bad snapshot (broken init script, bad agent):**
```bash
# 1. Revert snapshot reference
make set-snapshot NAME=ocm-snapshot-PREVIOUS

# 2. Redeploy backend (uses old snapshot for new hosts)
make deploy-backend

# 3. Running VMs are fine — they're already booted
# 4. Stopped VMs will get old snapshot on next start
# 5. If urgent: drain bad hosts, provision new ones from old snapshot
```

**Bad worker deploy:**
```bash
npx wrangler rollback --env production
# Or: redeploy previous version
git checkout HEAD~1 -- worker/
npx wrangler deploy --env production
```

---

## Implementation Phases

### Phase 0: Prerequisites (1-2 hours)

- [ ] Register `ocmstg.dev` domain (~$12/year)
- [ ] Add `ocmstg.dev` as a zone in Cloudflare (free plan)
- [ ] Create Neon `staging` branch from `main`
- [ ] Create staging secrets in GCP Secret Manager (`OCM_STAGING_*`)
- [ ] Add staging redirect URI to Google/GitHub OAuth apps (`api.ocmstg.dev`)
- [ ] Create staging KV namespace in Cloudflare

### Phase 1: Staging Environment (1-2 days)

- [ ] Parameterize `scripts/deploy-cloud-run.sh` for staging
- [ ] Add wrangler environments to `worker/wrangler.toml` (production + staging zones)
- [ ] Make `worker.js` `HOST_MAP` derive from `DOMAIN` env var
- [ ] Add Makefile targets: `deploy-staging`, `deploy-backend-staging`, etc.
- [ ] Deploy staging backend + frontend + worker
- [ ] Verify: `curl https://api.ocmstg.dev/health`
- [ ] Verify: staging frontend loads at `https://ocmstg.dev`, OAuth works, CRUD works

### Phase 2: CI/CD Pipeline (1 day)

- [ ] Create GitHub Actions workflow for staging auto-deploy
- [ ] Add production environment with manual approval gate
- [ ] Set up path-based change detection
- [ ] Add smoke tests

### Phase 3: Database Branching (half day)

- [ ] Script Neon branch creation/deletion
- [ ] Add `make migrate ENV=staging|production` target
- [ ] Test: create branch, run migration, verify, delete branch

### Phase 4: Preview Environments (optional, 1 day)

- [ ] GitHub Actions: create Neon branch on PR open
- [ ] Deploy preview backend (Cloud Run `--no-traffic`)
- [ ] Delete Neon branch on PR close
- [ ] Link preview URL in PR comment

### Phase 5: Staging Worker VM (optional, half day)

- [ ] Provision one staging host from current snapshot
- [ ] Create Cloudflare tunnel in `ocmstg.dev` zone
- [ ] Configure staging backend to use staging host
- [ ] Test: create machine on staging, verify terminal + proxy at `{acct}.ocmstg.dev`

---

## Cost Estimate

| Component | Production (current) | + Staging | Notes |
|-----------|---------------------|-----------|-------|
| Cloud Run backend | ~$15/month | +$5/month | Min 1 instance each |
| Cloud Run frontend | ~$5/month | +$0/month | Min 0, scales to zero |
| Cloudflare Worker | Free tier | Free | Same plan, different env/zone |
| Cloudflare KV | Free tier | Free | Second namespace, same plan |
| Neon database | ~$19/month | +$0-5/month | Branching is CoW, pay for diverged data only |
| Staging domain | N/A | +$1/month | `ocmstg.dev` ~$12/year |
| Staging VM (Option B) | N/A | +$30-50/month | One e2-standard-4 |
| GCP Secret Manager | ~$0.06/month | +$0.06/month | Double the secrets |
| **Total additional** | | **~$6-56/month** | $6 without staging VM, $56 with |

---

## Industry Comparison

### How Others Handle This

**Fly.io:**
- First-class staging via separate "apps" (same org, different name)
- `fly deploy --app myapp-staging`
- Machines API allows per-environment configuration
- Volumes are host-pinned, staging volumes on staging hosts

**Railway:**
- Built-in environment concept (dev/staging/prod)
- Preview environments per PR (ephemeral)
- Each environment has isolated resources + config
- One-click promote: staging → production

**Render:**
- Blueprints (YAML IaC) define all services per environment
- Preview environments from PRs
- Service groups for multi-service coordination

**Neon (database):**
- Branching is their core differentiator
- Instant branch creation (CoW, microseconds)
- Branch per PR is their recommended workflow
- Scale-to-zero on inactive branches

**Cloudflare Workers:**
- Native `[env.staging]` in wrangler.toml
- Separate KV namespaces per environment
- Gradual deployments (canary %, auto-rollback on error rate)
- Preview URLs per version upload

---

## Key Decisions

### Decision 1: Domain strategy
**Decision:** Separate staging domain (`ocmstg.dev`).
**Rationale:** Second-level wildcard certs under `openclawmachines.com` require Cloudflare Advanced Certificate Manager ($10/month). A separate domain uses a standard single-level wildcard (`*.ocmstg.dev`) covered by free Universal SSL. Also avoids discoverability — staging is invisible to production users.

### Decision 2: Staging VM pool
**Recommended:** Start without staging VMs (Option C). Add one staging host later (Option B) when needed.
**Alternative:** Shared VM pool (Option A) — cheaper but less isolated.

### Decision 3: CI/CD platform
**Recommended:** GitHub Actions (already using GitHub, free for public repos)
**Alternative:** Keep Cloud Build for snapshot pipeline only, GitHub Actions for everything else.

### Decision 4: IaC
**Recommended:** Keep Makefile + parameterized shell scripts. Add Pulumi later if managing 3+ environments.
**Alternative:** Terraform with Cloudflare + GCP providers (more upfront work, better long-term).

### Decision 5: Database branching granularity
**Recommended:** `main` (prod) + `staging` (long-lived). Add per-PR branches in Phase 4.
**Alternative:** `main` only, staging shares production DB (risky).

---

## Sources

- [Wrangler Environments — Cloudflare Workers docs](https://developers.cloudflare.com/workers/wrangler/environments/)
- [Gradual Deployments — Cloudflare Workers docs](https://developers.cloudflare.com/workers/configuration/versions-and-deployments/gradual-deployments/)
- [Database Branching Workflow — Neon docs](https://neon.com/docs/get-started/workflow-primer)
- [Branching with Preview Environments — Neon blog](https://neon.com/blog/branching-with-preview-environments)
- [Cloud Run Rollouts and Rollbacks — Google Cloud](https://docs.google.com/run/docs/rollouts-rollbacks-traffic-migration)
- [Railway Environments](https://docs.railway.com/guides/environments)
- [Fly.io Multiple Environments](https://fly.io/docs/reference/builders/)
- [The Hard Truth about GitOps and Database Rollbacks — Atlas](https://atlasgo.io/blog/2024/11/14/the-hard-truth-about-gitops-and-db-rollbacks)
- [Virtual Networks — Cloudflare Tunnel docs](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/private-net/cloudflared/tunnel-virtual-networks/)
- [Monorepo CI/CD with GitHub Actions](https://blog.logrocket.com/creating-separate-monorepo-ci-cd-pipelines-github-actions/)
