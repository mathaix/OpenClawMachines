---
name: ocm-testing
description: Choose and run the smallest safe OpenClaw Machines verification path for backend, frontend, Worker, host enrollment, Firecracker, rootfs, and self-hosted changes.
---

# OCM Testing

Use this skill when deciding what to test, debugging failing checks, or
validating changes before commit, push, or PR review.

## Read First

- `AGENT.md` for repo-wide testing expectations.
- `Makefile` for supported commands.
- `docs/local-firecracker-e2e.md` for local KVM/Firecracker proof.
- `docs/kvm-integration-ci.md` for maintainer-gated KVM runners.
- Relevant package docs near the changed code.

## Default Rule

Prove the touched surface first. Do not reflexively run every expensive lane.

1. Inspect the diff and classify touched surfaces.
2. Reproduce narrowly before fixing when a bug is reported.
3. Patch the implicated owner module.
4. Rerun the narrow proof.
5. Broaden only when a shared contract, integration boundary, or user workflow
   requires it.

## Surface Map

- Backend control plane, store, runtime, routing: `cd backend && go test ./internal/<pkg>`.
- Shared backend behavior or broad API changes: `make test-go`.
- Worker routing/auth/CORS/KV behavior: `make test-worker`.
- Frontend UI or auth/client behavior: `make test-frontend` and `make typecheck`.
- Cross-cutting code changes: `make check`, `make test`, `make typecheck`.
- Docs-only: inspect rendered links/commands when practical and run `git diff --check`.
- Scripts/shell automation: `make check-scripts` if available through `make check`;
  otherwise run the script's dry-run/help path when it has one.
- Firecracker, rootfs init, agent networking, TAP/bridge/NAT, VM persistence, or
  KVM placement: run the narrowest KVM integration lane available, or state the
  exact blocker such as missing `/dev/kvm`, root privileges, artifacts, network,
  or Cloudflare credentials.
- Cloudflare Worker deployment shape: test Worker logic locally and review
  `worker/wrangler.toml` plus `docs/self-hosted.env.example` for routing loops.

## Guardrails

- Prefer `make` targets over direct tool invocations when a target exists.
- Do not run privileged KVM, Docker, Cloudflare, GCP, or deploy commands unless
  the user asked or the change genuinely needs that proof.
- Do not kill unrelated processes. Treat running services as user-owned unless
  they were started in the current task.
- Do not print secrets from env files, logs, cookies, JWTs, database URLs, or
  Cloudflare/Firebase/GCP credentials.
- If dependency install or network access is required and blocked, report the
  blocker and use the closest local proof.

## Output Habit

Report:

- touched surface
- exact command run
- pass/fail result
- any skipped broader proof and the concrete blocker
- remaining risk if only narrow proof was possible
