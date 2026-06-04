# Testing Strategy (Comprehensive)

This doc unifies how we run, scope, and sequence all tests across laptops, CI, and the KVM host. It complements `docs/TESTING.md` (per-suite details) with “when/where/why” guidance.

## Goals
- Fast feedback on common changes without waiting for the special KVM host.
- Deterministic, reproducible runs (containerized where possible).
- Clear gates before deploys and rootfs releases.

## Test Tiers (what they cover)
- **Unit / fast checks (any machine)**  
  - `make test-go` (backend handlers, config assembly, plugins API, auth)  
  - `make test-frontend` (React/Vitest)  
  - `make test-worker` (edge auth/routing/CORS/WebSocket)  
  - `make typecheck`, `make check`
- **Browser E2E (any machine)**  
  - `make test-playwright` (UI flows, machine lifecycle, LLM proxy via UI)
- **Prod smoke (any machine, internet)**  
  - `make test-e2e` (curl against prod endpoints)
- **Gateway E2E (Linux, no KVM)**  
  - `make test-gateway-e2e` (OpenClaw gateway subprocess + metadata + proxy; needs API keys and `openclaw` installed)
  - `make test-gateway-plugin-smoke` (fast keyless plugin/channel smoke; optional strict Opik install gate)
- **Workflow DB integration (any machine w/ Postgres)**  
  - `make test-workflows` (`integration_db` tag; real Postgres)
- **Firecracker integration (KVM host + root)**  
  - `make smoke-test` (single VM, gateway suite)  
  - `make test-integration-run TEST=...` (targeted)  
  - `make test-integration` (full 35m suite)  
  - `make test-integration-e2e` (adds Cloudflare tunnel)

## Environments
- **Laptop / standard CI runner (no KVM):** unit, typecheck, worker, frontend, Playwright, prod smoke, workflow DB (with container Postgres), gateway E2E.
- **Privileged KVM host:** Firecracker + rootfs + tunnel tests.

## Developer quick recipes
- Backend/API change: `make test-go`; if DB touch, add `make test-workflows` (with Postgres URL); if proxy/gateway logic touched, add `make test-gateway-e2e`.
- Frontend change: `make test-frontend` + `make typecheck`; add `make test-playwright` for flow/UI work.
- Worker change: `make test-worker`.
- Rootfs/init/openclaw change: `make smoke-test` (KVM); run `make test-integration` before uploading rootfs.
- OpenClaw/plugin/channel change: `make test-gateway-plugin-smoke`; for version bumps run `make test-openclaw-upgrade VERSION=<v>`

## CI Matrix (recommended)
- **PR (path-filtered):**
  - Always: `make test`, `make typecheck`, `make check`.
  - `frontend/**` → `make test-frontend`; optional `make test-playwright`.
  - `worker/**` → `make test-worker`.
  - `backend/internal/api/**`, `backend/internal/configassembly/**`, `backend/internal/store/**` → `make test-go`; if `workflows/` touched, add `make test-workflows`.
  - `backend/internal/agentapi/**`, `backend/internal/apiproxy/**`, `rootfs/**`, `scripts/init-openclaw.sh` → `make test-gateway-e2e`.
  - `backend/internal/integration/**`, `rootfs/**`, `scripts/**` (init) → trigger KVM host job: `make smoke-test` or targeted `make test-integration-run 'TestGatewaySuite|TestE2E_FullWorkflow'`.
- **Nightly:** full `make test-playwright`; `make test-gateway-e2e`; full `make test-integration` on KVM host (catch drift).
- **Pre-release / rootfs upload:** `make smoke-test` gate; full `make test-integration`; optional `make test-integration-e2e` if tunnel changes.

## Docker augmentation
- **Compose stack (proposed):** Postgres + backend + optional mock worker. Run `TEST_DATABASE_URL=... make test-workflows` and handler tests without local DB setup.
- **Gateway smoke in Docker:** Build a small image with Node + `openclaw` and run the gateway E2E on any Linux runner (no KVM). Requires API keys + egress.
- **Caches:** Mount Go `GOCACHE`, npm/pnpm cache, and Playwright browsers into volumes to keep runs fast.

## KVM host efficiency
- Always wrap with `scripts/test-cleanup.sh` and `scripts/test-xfs-setup.sh` (reflink copies avoid 3.4GB rootfs copy per test).
- Pre-stage `/var/lib/ocm/images/{rootfs.ext4,vmlinux}` to skip downloads.
- Use targeted runs: `make test-integration-run TEST=...` for change-scoped fixes.
- Reserve full `make test-integration` for scheduled/nightly or pre-release.

## Coverage notes
- **OpenClaw gateway & plugins:** Covered in `make test-gateway-e2e` (container-friendly). Plugins/config set/unset paths are also exercised in unit tests under `backend/internal/api/plugins_test.go` and `machine_config_test.go`.
- **Logs/SSE/progress, init restart, bridge/TAP/NAT:** Only validated in Firecracker integration on the KVM host; cannot be faithfully dockerized.
- **Cloudflare tunnel:** Needs `make test-integration-e2e` on the KVM host with CF creds; a privileged Docker run can test tunnel lifecycle but won’t cover VM paths.

## Secrets & creds
- Gateway E2E: one of `E2E_ANTHROPIC_API_KEY`, `E2E_ANTHROPIC_SUBSCRIPTION_KEY`, `E2E_OPENAI_API_KEY`, `E2E_OPENAI_SUBSCRIPTION_KEY`; requires `openclaw` installed.
- Tunnel tests: `CF_API_TOKEN`, `CF_ACCOUNT_ID`, `CF_ZONE_ID`, `cloudflared` in PATH.

## Release gates
- Before deploy: `make check`, `make test`, `make test-e2e`.
- Before rootfs upload: `make smoke-test` (must pass); for major changes run `make test-integration`.
- Weekly: full integration + Playwright to catch drift and dependency changes.
