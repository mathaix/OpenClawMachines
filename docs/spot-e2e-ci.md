# Spot E2E — admin-gated merge check

The **Spot E2E** lane (`.github/workflows/spot-e2e.yml`) provisions a **real GCE
spot host**, runs the Playwright E2E (login → create machine → Firecracker VM
boots) against it, captures screenshots/traces, and tears everything down. It
gates merges to `main` via the `spot-e2e` commit status.

It costs real money (a short-lived spot VM) and uses GCP + Cloudflare
credentials, so it is **admin-only** and never runs on untrusted PRs.

## Security model

Mirrors the `kvm-integration` lane:

- **No `pull_request` / `pull_request_target` trigger.** Secrets and billable
  VMs are never exposed to fork PRs.
- **`workflow_dispatch` only** (plus the admin check below). Dispatching requires
  repo write access; the workflow additionally verifies the actor has the
  **admin** role and fails otherwise.
- Teardown runs in a `trap … EXIT` in `ci/spot-e2e.sh` and the host is also
  deleted by the API call, so a failed run does not leak a paid VM. (Spot
  instances are created with `InstanceTerminationAction=DELETE` as a backstop.)

## How an admin gates a PR

1. Push the PR; the normal required checks (unit/lint/worker) run automatically.
2. An **admin** runs the Spot E2E lane against the PR head:
   ```bash
   gh workflow run spot-e2e.yml -f pr=<PR_NUMBER> -f sha=<PR_HEAD_SHA>
   ```
   (or via the Actions UI → Spot E2E → Run workflow).
3. The lane validates `sha` matches the PR head, runs, and posts a `spot-e2e`
   commit status (pending → success/failure) on that SHA.
4. Branch protection requires the `spot-e2e` status, so the PR can only merge
   after an admin's run passes.

## One-time setup

### 1. GCP auth — Workload Identity Federation (recommended)

Create a WIF pool/provider for GitHub OIDC and a service account the workflow
impersonates, with roles: `roles/compute.instanceAdmin.v1`,
`roles/iam.serviceAccountUser`, and `roles/secretmanager.secretAccessor`.
Then set repo **secrets**:

- `GCP_WIF_PROVIDER` — e.g. `projects/123/locations/global/workloadIdentityPools/github/providers/oidc`
- `GCP_SERVICE_ACCOUNT` — e.g. `ocm-e2e@PROJECT.iam.gserviceaccount.com`

(Prefer WIF over a long-lived SA key. If you must use a key, swap the
`google-github-actions/auth` step to `credentials_json: ${{ secrets.GCP_SA_KEY }}`.)

### 2. Repo variables (non-secret)

- `GCP_PROJECT` — the project to provision in
- `GCP_ZONE` — e.g. `us-west1-b` (ensure SSD/CPU quota for n2 spot there)
- `E2E_DATA_PLANE_DOMAIN` — a Cloudflare zone you control, e.g. `openclawswarm.com`
- `E2E_CLOUDFLARE_ZONE_ID` — that zone's id

### 3. Secret Manager (read by the WIF SA)

`OCM_CLOUDFLARE_API_TOKEN`, `OCM_CLOUDFLARE_ACCOUNT_ID`,
`OCM_CLOUDFLARE_KV_NAMESPACE_ID`, `OCM_FC_AGENT_TOKEN`,
`OCM_SECRET_ENCRYPTION_KEY`, `OCM_JWT_SECRET`.

### 4. Branch protection

On `main`, add **`spot-e2e`** to the required status checks.

## What it captures, and where

Playwright's HTML report + screenshots + traces are uploaded as the
**`spot-e2e-playwright`** GitHub Actions artifact (14-day retention) — the right
place for per-run test output (not committed to git). To keep durable/shareable
links, add a step that mirrors `frontend/playwright-report` to a GCS bucket keyed
by SHA and posts the link as a PR comment.

## Local run (admin with gcloud auth)

```bash
export GCP_PROJECT=… GCP_ZONE=us-west1-b DATA_PLANE_DOMAIN=openclawswarm.com \
       CLOUDFLARE_API_TOKEN=… CLOUDFLARE_ACCOUNT_ID=… CLOUDFLARE_ZONE_ID=… \
       CLOUDFLARE_KV_NAMESPACE_ID=… FC_AGENT_TOKEN=… SECRET_ENCRYPTION_KEY=… JWT_SECRET=…
bash ci/spot-e2e.sh
```
Requires `gcloud` auth (ADC), Docker, Go, Node, and `cloudflared` on PATH.
