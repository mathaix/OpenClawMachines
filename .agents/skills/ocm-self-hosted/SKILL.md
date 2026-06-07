---
name: ocm-self-hosted
description: Plan, implement, or review local/operator/self-hosted OpenClaw Machines work without leaking secrets or smuggling hosted-only assumptions into public core.
---

# OCM Self-Hosted

Use this skill for local profile, operator profile, host enrollment,
self-hosted deployment, Cloudflare Worker/KV, Firebase, Cloudflare Access,
domain, cookie, and public-core portability work.

## Read First

- `AGENT.md`.
- `docs/control-plane-profiles.md`.
- `docs/self-hosted-control-plane.md`.
- `docs/local-setup.md`.
- `docs/local-firecracker-e2e.md` for single-box KVM flow.
- `llms/self-hosted-setup.txt` for operator-oriented setup.
- `.agents/maintainer-notes/routing.md`.
- `.agents/maintainer-notes/runtime.md`.

## Profile Contract

- `local`: trusted localhost development. Dev auth may be enabled. Do not expose
  publicly.
- `operator`: self-hosted production-like deployment on operator-managed
  infrastructure. It should preserve the hosted architecture: edge protection,
  control plane, OCM session token, Worker/KV data plane, enrolled KVM workers,
  and Firecracker VMs.
- `hosted`: hosted/operator profile. Do not introduce private Mathaix defaults
  into public core.

## Workflow

1. Start with discovery. Inspect branch, dirty state, env examples, docs, and
   config without printing secrets.
2. Identify operator inputs: domains, auth mode, Cloudflare account/zone/KV,
   Worker routes, control-plane URL, cookie domain, artifacts, and KVM hosts.
3. Produce a redacted plan listing resources to create, update, or leave alone.
4. Ask before changing live Cloudflare, DNS, Tunnel, Worker, KV, Firebase,
   Cloudflare Access, host enrollment, or running services.
5. Implement neutral public-core primitives only. Leave private hosted policy,
   billing, and commercial admin behavior out of this repo.
6. Validate with the smallest smoke that matches the change:
   login, `/api/auth/me`, `ocm_token`, host online, machine create/start, route
   resolution, terminal/gateway, optional SSH, stop/delete.

## Review Checks

- Worker origin hosts must be distinct backend origins, not routed hostnames
  served by the same Worker.
- Cookie domains must work for operator domains and omit localhost domains.
- `/api/internal/resolve` must be protected by service-token or equivalent
  operator controls in production-like deployments.
- Worker and backend config examples must not encourage routing loops.
- No-tunnel local routes still need persisted VM signing keys.
- Worker hosts are infrastructure and must use enrollment/agent tokens, not
  Firebase human auth.

## Output Habit

Report:

- profile and deployment shape assumed
- operator inputs used or missing
- files/config touched
- commands/proof run
- live infrastructure actions skipped or performed
- remaining deployment risks
