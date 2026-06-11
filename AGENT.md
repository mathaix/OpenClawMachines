# OpenClaw Machines Agent Guide

This file gives coding agents shared project context. It is intentionally
tool-neutral: use it from Claude, Codex, Gemini, or any other coding agent.

## Mission

OpenClaw Machines is the Apache-2.0 public core for running AI agents in
KVM-backed Firecracker microVMs on infrastructure the operator controls.

The public core should provide:

- A minimum control plane that can run locally or as an operator-hosted service.
- Host enrollment and worker agents for KVM hosts.
- Machine lifecycle, placement, routing primitives, and runtime telemetry.
- Firecracker/rootfs/runtime pieces needed to boot isolated OpenClaw machines.
- API surfaces that remain compatible with the separate `ocm` CLI project.

The public core is not the commercial hosted overlay. Billing, plan enforcement,
pricing, reservations, hosted-only admin, customer management, private
infrastructure runbooks, and confidential deployment assumptions belong in the
private overlay.

Rule of thumb: if a feature is required to run OpenClaw Machines yourself, keep
it public and provider-neutral. If it sells, gates, meters, or administers the
hosted business, keep it out of this repo.

## Architecture

The target production-like architecture is:

```text
User
  -> Cloudflare edge
  -> Cloudflare Tunnel
  -> OCM control plane
  -> OCM-issued ocm_token
  -> Cloudflare Worker + KV data plane
  -> KVM worker tunnel
  -> Firecracker VM
```

Local development can use trusted dev auth and localhost services. A
self-hosted/operator deployment should preserve the hosted architecture:
Cloudflare Tunnel ingress, Worker/KV data-plane routing, Firebase or Cloudflare
Access human auth, OCM-issued `ocm_token`, enrolled KVM workers, and Firecracker
VMs. Do not replace this with a different auth provider, routing layer, or direct
public exposure of worker hosts.

Human auth modes currently documented for operator deployments:

- Firebase: app login inside OCM, then `/api/auth/session/exchange`.
- Cloudflare Access: identity enforced at the edge, then resolved by OCM.

Workers are infrastructure, not human users. Authenticate workers with OCM
enrollment/agent tokens and optional Cloudflare service tokens, not Firebase.

## Repository Layout

- `backend/`: Go control plane, worker/agent APIs, provisioning, metadata,
  routing, orchestration, auth, and store code.
- `backend/cmd/server`: control-plane server.
- `backend/cmd/agent`: KVM host worker agent.
- `backend/cmd/authproxy`: guest auth proxy.
- `backend/cmd/ocm-secrets`: helper for secret material.
- `frontend/`: React + TypeScript + Vite UI.
- `worker/`: Cloudflare Worker data-plane routing code.
- `rootfs/` and `scripts/init-*.sh`: guest image/runtime initialization.
- `scripts/`: local development, preflight, image, runtime, and test helpers.
- `ci/` and `.github/workflows/`: public CI and trusted KVM integration lanes.
- `docs/`: public-core setup, operator, CI, and boundary documentation.
- `llms/`: copy-pasteable operator runbooks for LLM-driven setup.
- `.agents/`: repo-local agent skills and maintainer notes for repeatable
  review, testing, debugging, and self-hosted workflows.

The `ocm` CLI lives in the separate Apache-2.0 repository:
<https://github.com/mathaix/ocm-cli>. Do not reintroduce or patch CLI
implementation code in this repo. Keep API contracts and docs compatible with
that project.

## Read First

Before changing architecture, deployment behavior, auth, routing, host
enrollment, Firecracker runtime, or public docs, read the relevant docs:

- `README.md`: public-core overview and scope.
- `docs/overlay-boundary.md`: public core vs private overlay boundary.
- `docs/local-setup.md`: trusted local/BYO-host development path.
- `docs/control-plane-profiles.md`: `local`, `operator`, and `hosted` profile
  semantics.
- `docs/self-hosted-control-plane.md`: hosted-parity self-hosted prerequisites.
- `docs/ci-release.md`: public CI vs trusted KVM/release lanes.
- `docs/kvm-integration-ci.md`: maintainer-gated KVM runner contract.
- `docs/cli.md`: relationship to `mathaix/ocm-cli`.
- `llms/self-hosted-setup.txt`: operator setup workflow for LLM agents.

When behavior changes, update the relevant docs in the same change.

Repo-local agent skills live under `.agents/skills/`. Use them as focused
workflow overlays after reading this guide:

- `.agents/skills/ocm-testing/SKILL.md`: choosing the smallest safe proof.
- `.agents/skills/ocm-review/SKILL.md`: PR/review-comment/code-review posture.
- `.agents/skills/ocm-debugging/SKILL.md`: boundary-first debugging.
- `.agents/skills/ocm-self-hosted/SKILL.md`: local/operator/self-hosted work.

Maintainer notes under `.agents/maintainer-notes/` capture project decisions
that should be enforced during review, especially routing and runtime
invariants.

## Operating Rules

- Prefer `make` targets over invoking scripts directly. The Makefile documents
  the supported local commands.
- Keep changes tightly scoped. Avoid unrelated refactors, broad doc scrubs, or
  generated churn.
- Do not commit secrets, tokens, private URLs, customer data, hosted-only
  runbooks, or private overlay assumptions.
- Never print secret values in logs or final reports. Redact tokens, keys,
  database URLs, cookies, JWT secrets, encryption keys, and service-token
  secrets.
- Do not add billing, plan enforcement, pricing, commercial quotas, or hosted
  SaaS admin to public-core code.
- Do not add `pull_request` or `pull_request_target` execution paths for KVM,
  rootfs upload, deployment, Cloudflare, GCP, or other privileged workflows.
- Do not expose KVM worker hosts directly to users. User-facing machine access
  should go through the configured data-plane protection model.
- Do not guess model IDs, API versions, provider config, domains, or registry
  names. Inspect repo config/docs or ask the operator.
- If operator-domain support is blocked by hard-coded hosted values, report the
  gap or open/update an issue rather than inventing a new architecture.

## Common Commands

Development:

```bash
make preflight
make local-env
make local-postgres
make local-backend
make local-frontend
make status
```

Checks:

```bash
make check
make test
make typecheck
```

Focused checks:

```bash
make test-go
make test-unit
make test-frontend
make test-worker
```

KVM/Firecracker integration tests require a trusted Linux KVM host with root
privileges and Firecracker:

```bash
make integration-kvm
make test-integration-run TEST=TestName
```

Public PR CI must stay limited to unprivileged checks on GitHub-hosted runners:
`make check`, `make test`, `make typecheck`, and static secret scanning.

Claude Code slash commands live in `.claude/commands/` and wrap these workflows:
`/start`, `/stop`, `/status`, `/test`, `/verify`, `/pr`, `/currentfeature`,
`/codex`, `/freshclone`. See `.claude/commands/README.md` (the private Cloud Run /
GCS deploy commands are intentionally omitted).

## Local vs Operator Profiles

`CONTROL_PLANE_PROFILE=local` is for trusted local development.

- It defaults toward dev auth.
- `AUTH_MODE=dev` is allowed only for trusted local use.
- Do not expose dev auth publicly.
- Cloudflare and Firebase are optional for local dev.

`CONTROL_PLANE_PROFILE=operator` is for a self-hosted control plane on
operator-managed infrastructure.

- Set an explicit auth mode such as `firebase` or `cfaccess`.
- Configure real domains, Cloudflare Tunnel, Worker/KV, cookies, CORS, and
  OCM secrets when hosted-parity behavior is required.
- Use KVM host enrollment for workers.

`CONTROL_PLANE_PROFILE=hosted` is an operator/hosted deployment profile. It must
not smuggle private Mathaix-hosted defaults into public core.

## Firecracker And Runtime Notes

- Firecracker requires Linux with `/dev/kvm`; macOS, Windows/WSL, and standard
  cloud VMs without nested virtualization cannot run the runtime.
- The worker creates bridges, TAP devices, NAT rules, rootfs/data images, and
  per-VM runtime state.
- `VM_RUNTIME_OWNER=systemd-unit` is the production-like default. Direct owner
  mode is experimental.
- Guest init is not systemd. `scripts/init-openclaw.sh` runs as PID 1 and must
  explicitly set up mounts, devices, services, and environment.
- Environment needed by gateway, PTY server, and login shells may need to be
  added in separate init-script contexts.
- XFS VM-state storage is test/local runtime state. Keep its configured size
  realistic for local testing and document changes that affect host storage.

## Testing Expectations

Run tests that match the blast radius.

- Docs-only: review links, commands, and public/private boundary.
- Backend changes: `make test-go`; add focused Go tests near changed behavior.
- Frontend changes: `make test-frontend` and `make typecheck`.
- Worker changes: `make test-worker`.
- Cross-cutting code changes: `make check`, `make test`, `make typecheck`.
- Firecracker, rootfs, init, agent runtime, networking, or persistence changes:
  run KVM integration on a trusted KVM host when feasible.

If a test cannot be run because KVM, browser, network, or credentials are
missing, state that clearly with the exact blocker.

## Self-Hosted Setup Work

For self-hosted/operator setup tasks:

1. Start with discovery. Inspect branch, git state, env files, docs, and config
   without printing secrets.
2. Ask for missing operator inputs before modifying infrastructure.
3. Produce a redacted plan that lists resources to create, update, or leave
   untouched.
4. Ask before changing Cloudflare, DNS, Tunnel, Worker, KV, Firebase,
   Cloudflare Access, host enrollment, or running services.
5. Validate with a smoke test: login, `/api/auth/me`, `ocm_token`, host online,
   machine create/start, route resolution, terminal/gateway, optional SSH,
   stop/delete.

Use `llms/self-hosted-setup.txt` as the detailed operator runbook.

## PR And Review Hygiene

- Every change starts with a tracking issue. Before writing code, open a GitHub
  issue (or reuse an existing one) describing the work, then create a branch
  associated with it, named `issue-<n>-short-slug` (for example
  `issue-17-spot-e2e-tunnel-fix`).
- Work on that issue branch. Do not push directly to `main`.
- Open a pull request that references the issue (use `Closes #<n>` when the PR
  fully resolves it). Keep pull requests focused and explain test coverage.
- Read review comments against the referenced code before dismissing them.
- For public PRs, keep KVM and privileged checks maintainer-gated.
- If a change touches both public-core primitives and private-overlay policy,
  keep the neutral primitive here and leave policy/UI to the overlay.
