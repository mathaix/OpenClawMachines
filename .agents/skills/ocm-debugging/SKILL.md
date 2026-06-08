---
name: ocm-debugging
description: Debug OpenClaw Machines profile, auth, Worker routing, host enrollment, agent API, Firecracker, rootfs, and VM runtime behavior by choosing the right boundary and proof before changing code.
---

# OCM Debugging

Use this skill when behavior differs between local, operator, hosted-like,
Worker, agent, guest VM, or KVM integration paths and the next move should be a
debug signal rather than a guess.

## Default Loop

1. State the suspected boundary:
   - profile/config
   - auth/cookie/session exchange
   - Cloudflare Worker/KV routing
   - host enrollment or heartbeat
   - placement/capacity
   - backend-to-agent API
   - metadata server
   - guest init/rootfs
   - Firecracker network/storage
   - runtime artifact/version selection
2. Add or enable the narrowest signal that proves that boundary.
3. Reproduce with the same profile, env, domain shape, host state, and runtime
   version. Do not switch profiles or providers unless that variable is under
   test.
4. Compare configured state with actual activated state.
5. Patch the root cause.
6. Rerun the exact failing probe, then broaden only if a shared contract needs
   it.

## Useful Starting Points

- Config/profile load: `backend/internal/config`.
- Session cookies and auth: `backend/internal/api/auth_handlers.go`.
- Worker route selection and resolve fallback: `worker/worker.js`.
- Route provisioning and signing keys: `backend/internal/routing`.
- Machine start/upgrade lifecycle: `backend/internal/machines/runtime.go`.
- Host enrollment: `backend/internal/api/enrollment.go`.
- Agent control API: `backend/internal/agentapi`.
- Firecracker orchestration: `backend/internal/orchestrator`.
- Guest metadata: `backend/internal/metadata`.
- Guest init: `scripts/init-openclaw.sh`.
- Local full-stack runbook: `docs/local-firecracker-e2e.md`.

## Proof Choices

- Single backend invariant: focused Go package test.
- Worker route/auth bug: `make test-worker`.
- Frontend/auth UI bug: focused frontend test plus `make typecheck`.
- Local control-plane setup: `make local-status` or the relevant
  `scripts/local-dev.sh` command.
- Firecracker/agent/rootfs/network bug: KVM integration or local
  `scripts/local-e2e-firecracker.sh status`, with exact blocker reported when
  unavailable.
- Cloudflare/operator routing bug: local Worker test plus config/doc review;
  live Cloudflare proof only when credentials and operator approval are present.

## Output Habit

Report:

- boundary tested
- command or probe used, with secrets redacted
- observed signal
- fix location
- narrow proof
- remaining risk
