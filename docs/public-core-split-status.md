# Public Core Split Status

Date: 2026-06-04 UTC

## Repositories

- `mathaix/ocm-cloud` is the renamed original private repository. It keeps the existing history and remains the place for hosted/cloud/private-overlay work.
- `mathaix/OpenClawMachines` is the new clean-history repository. It currently has one root commit, `b42017d`, from the allowlist export.
- `mathaix/OpenClawMachines` is intentionally private until the public-core readiness scrub passes.

## Product Boundary

- Public core license target: Apache-2.0.
- Public core scope: Firecracker/microVM runtime and the minimum control plane that can run locally or be hosted by an operator.
- Companion CLI scope: the Apache-2.0 `ocm` CLI lives in `mathaix/ocm-cli`.
- Private overlay scope: billing, plan enforcement, commercial admin, enterprise-only hosted flows, launch/pricing material, confidential infrastructure notes, and other hosted business features.

## Actions Already Taken

- Renamed the original GitHub repository from `mathaix/OpenClawMachines` to `mathaix/ocm-cloud`.
- Created a new private `mathaix/OpenClawMachines` repository.
- Exported a clean-history public-core snapshot from the private repository using an allowlist export script.
- Pushed the clean snapshot to the new repository on `main`.
- Created readiness labels and issues:
  - #1 `core-local`: local/BYO-host runtime readiness.
  - #2 `core-control-plane`: minimum hosted-capable control plane.
  - #3 `docs-oss`: public docs and OSS hygiene.
  - #4 `ci-release`: public-safe CI and release lanes.
  - #5 `cli`: superseded for this repository; CLI work belongs in `mathaix/ocm-cli`.
  - #6 `overlay-boundary`: public/private overlay boundary scrub.

## Aborted Worker Attempt

The repo-local `scripts/ao.sh` runner was invoked first. That was the wrong worker path because the script launches `claude -p`.

Status of that attempt:

- The Claude processes were stopped.
- No PRs were opened.
- Nothing was pushed.
- Local `.claude/` worktrees may remain as discarded scratch state and must not be committed.

## Current Worker Plan

Use Codex sub-agents, not `scripts/ao.sh`.

Assignments:

- Worker A: issue #1, local/BYO-host runtime and `make preflight`.
- Worker B: issue #2, deployment profile and minimum control-plane dependency boundary.
- Worker C: issue #6, billing/pricing/commercial overlay scrub.
- Worker D: issue #5 was redirected; main-repo CLI code is dead and should be removed. External CLI readiness belongs in `mathaix/ocm-cli`.
- Worker E: issue #4, public-safe CI and release lane review.
- Worker F: issue #3, public docs and OSS hygiene.

Integration rules:

- Treat worker output as proposed patches, not automatically accepted truth.
- Avoid merging overlapping edits without local review.
- Keep `mathaix/OpenClawMachines` private until issue #6 and issue #3 pass review.
- Do not publish public PRs that run privileged KVM jobs on arbitrary code.

## Immediate Risks

- The clean export still contains hosted/commercial code and docs. Issue #6 is the release blocker.
- Docs still include material that may be useful internally but wrong for a public Apache core. Issue #3 is the public-readiness blocker.
- The local/BYO-host path needs a real preflight target and clear setup path before adoption messaging is credible.
