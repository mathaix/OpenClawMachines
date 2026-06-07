---
name: ocm-review
description: Review OpenClaw Machines changes by tracing behavior beyond the diff, checking ownership boundaries, and requiring focused proof for bug fixes.
---

# OCM Review

Use this skill for PR review, review-comment resolution, post-fix review, or
any request asking whether a change is correct, safe, or the best fix.

## Review Posture

Review behavior, not prose. A convincing PR description is not proof. Read the
real code path, adjacent tests, docs, and deployment assumptions before giving a
verdict.

## Required Pass

1. State what the change does and why the surface matters.
2. Read the changed files plus enough surrounding code to identify:
   - runtime entry point
   - owner module
   - one caller
   - one callee
   - sibling surfaces that should share the invariant
   - adjacent tests
   - relevant config/docs
3. Compare with current `origin/main` behavior when regression or compatibility
   matters.
4. For dependency-backed behavior, read official docs/source/types instead of
   relying on memory.
5. Decide whether the patch is the best owner-boundary fix or only a plausible
   mitigation.
6. Require focused proof for bug fixes: reproduction/log/failing test when
   practical, root cause in code, fix touching the implicated path, and a
   regression test or explicit reason no test was added.

## OCM-Specific Checks

- Local/operator paths must not depend on hosted-only Cloudflare, GCP, Firebase,
  or private-domain defaults.
- Every VM path must preserve the gateway/proxy token and signing-key contract.
- Worker origins must not point back at hostnames served by the same Worker
  route.
- User-facing machine access must go through the configured data-plane
  protection model; do not expose KVM worker hosts directly.
- Host enrollment must keep workers authenticated as infrastructure, not human
  users.
- Runtime/rootfs changes need guest-init and agent-client compatibility checks.
- Public-core changes must not add billing, commercial plan enforcement, or
  private hosted overlay assumptions.

## Output Shape

Use this compact structure:

- `Findings:` ordered by severity with file/line references.
- `Best-fix verdict:` best / acceptable mitigation / wrong layer / too narrow /
  too broad.
- `Alternatives considered:` one to three concrete alternatives and why they
  were rejected.
- `Code read:` main files/contracts inspected.
- `Proof:` tests, logs, or commands checked.
- `Remaining uncertainty:` what was not proven.

If there are no findings, say that clearly and still include proof and remaining
risk.
