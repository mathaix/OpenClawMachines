# OpenClaw Runtime Artifact Delivery Design

Date: 2026-04-03
Status: Proposed
Owner: Platform Runtime
Related:
- `docs/design/versioned-release-and-upgrade.md`
- `docs/plans/2026-04-03-decoupled-runtime-implementation-plan.md`
- Channel definition: see **Terminology → Channel** in `docs/design/versioned-release-and-upgrade.md`

## Summary
Deliver OpenClaw as an immutable, verifiable runtime artifact (`.tar.zst`) staged on hosts and activated per machine.

This replaces "install/update OpenClaw via rootfs build" as the primary delivery mechanism and enables independent machine-level OpenClaw version selection, fast rollback, and compatibility gating with rootfs/agent versions.

## Goals
- Per-machine `openclaw_version` selection independent of `rootfs_version`.
- Deterministic, auditable releases (checksum required; signature phased in).
- Fast machine-scoped upgrade and rollback.
- No package-manager/network installs in VM boot critical path.

## Non-Goals
- Replacing rootfs delivery in this design (rootfs remains separate artifact plane).
- Full supervisor/control-agent split in this design.
- User-managed plugin policy redesign beyond path ownership and precedence.

## Artifact Format and Storage

### Artifact Format
- File: `openclaw-{version}-linux-amd64.tar.zst`
- Contents (minimum):
  - `bin/openclaw` (launcher/wrapper is acceptable)
  - `dist/` (runtime assets)
  - `dist/extensions/` (bundled plugins)
- Phase 1 runtime assumption:
  - Node.js remains provided by rootfs.
  - Artifact does not need to bundle Node.js in the first iteration.
- Future option:
  - Fully self-contained OpenClaw runtime artifact (Node + app payload) can be added in a later phase.

### Build and Packaging Strategy (Phase 1)
- Preferred implementation: **Option A (extract from Docker build output)**.
  - Reuse `rootfs/Dockerfile.openclaw` (or slim variant) to produce a runtime tree matching current production behavior.
  - Package the OpenClaw runtime payload as `.tar.zst` and publish with manifest metadata.
- Rationale:
  - minimizes drift versus what is currently shipped in rootfs,
  - keeps rollout risk lower for initial decoupling.
- Required build/release targets:
  - `make build-openclaw`
  - `make upload-openclaw`
  - `make build-upload-openclaw`
  - include OpenClaw artifact in `make build-components`.

### Object Storage Layout
```text
gs://openclawmachines/openclaw/
  manifest-stable.json
  manifest-rc.json
  manifest-dev.json
  releases/
    v2026.4.3/
      openclaw-v2026.4.3-linux-amd64.tar.zst
      manifest.json
      manifest.sig
```

## Manifest Schemas

### Channel Manifest
`gs://openclawmachines/openclaw/manifest-{channel}.json`

```json
{
  "schema_version": 1,
  "kind": "openclaw-channel",
  "channel": "stable",
  "current_version": "v2026.4.3",
  "updated_at": "2026-04-03T18:20:00Z"
}
```

### Release Manifest
`gs://openclawmachines/openclaw/releases/{version}/manifest.json`

```json
{
  "schema_version": 1,
  "kind": "openclaw-runtime",
  "version": "v2026.4.3",
  "channel": "stable",
  "built_at": "2026-04-03T17:58:00Z",
  "git_commit": "abc1234",
  "artifact_url": "gs://openclawmachines/openclaw/releases/v2026.4.3/openclaw-v2026.4.3-linux-amd64.tar.zst",
  "compression": "zstd",
  "size_bytes": 183442110,
  "sha256": "8f0e...d2",
  "signature": {
    "alg": "ed25519",
    "key_id": "ocm-release-key-1",
    "sig_url": "gs://openclawmachines/openclaw/releases/v2026.4.3/manifest.sig"
  },
  "compatibility": {
    "agent_constraint": ">=v2026.4.0 <v2026.10.0",
    "rootfs_constraint": ">=v2026.3.0 <v2026.6.0",
    "rootfs_allowlist": [],
    "rootfs_denylist": []
  },
  "runtime": {
    "entrypoint_relpath": "bin/openclaw",
    "bundled_plugins_relpath": "dist/extensions"
  }
}
```

## Trust Model
- Phase 1:
  - Verify artifact SHA256 before extraction (mandatory, fail closed).
  - Signature verification is optional and feature-flagged while key infrastructure is introduced.
- Phase 2+:
  - Verify release manifest signature before trusting manifest contents (mandatory, fail closed).
- Keep key rotation via `signature.key_id`.

## Host Filesystem Layout
```text
/var/lib/ocm/openclaw/
  releases/
    v2026.4.3/              # immutable unpacked runtime
  staging/
    <txn-id>/               # temp download/verify/extract path
  cache-index.json          # refcount + last-used metadata
```

Requirements:
- `releases/{version}` is immutable after publish.
- staging path is cleaned after success/failure.
- extraction + publish is atomic (`rename` into final path).

## Per-Machine Runtime Pointers
Source of truth is host-side (agent-managed), keyed by machine id.

### Host-Side Pointers (Authoritative)
```text
/var/lib/ocm/runtime/machines/{machine_id}/openclaw/
  selected_version
  resolved_version
  current -> /var/lib/ocm/openclaw/releases/v2026.4.3
  previous -> /var/lib/ocm/openclaw/releases/v2026.4.2
```

### VM-Side Mirror (Derived)
```text
/data/ocm/runtime/openclaw/
  selected_version          # mirrored desired
  resolved_version          # mirrored active
  current -> /var/lib/ocm/openclaw/releases/v2026.4.3
  previous -> /var/lib/ocm/openclaw/releases/v2026.4.2
```

Pointer rules:
- Host pointers are authoritative for activation/rollback decisions.
- VM-side pointers are informational/runtime mirrors and can be rebuilt from host state.
- `current` always points to a verified installed release.
- `previous` updated only after successful switch to a new `current`.
- `resolved_version` must match `basename(current)`.

## Plugin Path Ownership
Two plugin tiers are required:

1. Bundled plugins (read-only)
- Source: `${current}/dist/extensions`
- Ownership: release artifact

2. User plugins (persistent writable)
- Source: `/data/home/openclaw/.openclaw/extensions`
- Ownership: machine/user

Load order:
- User plugins override bundled plugins by plugin id/name conflict.

## Runtime Discovery in Init Script
The init script should not depend on a symlink-only model for selecting the executable.

Resolution precedence:
1. `OPENCLAW_BIN` (set by host/agent for artifact runtime; points to injected runtime entrypoint).
2. `openclaw` from PATH (legacy baked rootfs fallback).

Notes:
- Optional symlink (`/usr/local/bin/openclaw`) may be created for compatibility, but must not be the sole activation mechanism.
- `OPENCLAW_BUNDLED_PLUGINS_DIR` should point to `${current}/dist/extensions` for artifact runtime and be exported before gateway start.

## Activation Flow (Machine-Scoped)
Phase 1 lands boot-time version activation first.

1. Resolve desired version:
- pinned machine `openclaw_version`, or
- channel manifest current version.

2. Verify compatibility:
- `agent x rootfs x openclaw` policy must pass.

3. Ensure staged on host:
- download manifest (+ signature metadata when enabled),
- verify signature when signature verification is enabled,
- download artifact,
- verify SHA256,
- extract to staging,
- atomic publish to `releases/{version}`.

4. Switch machine pointers:
- set `previous <- current`,
- set `current <- new version`,
- update `resolved_version`.

5. Apply change via machine lifecycle:
- default: take effect on next VM boot.
- optional operator action: controlled machine restart to apply immediately.
- explicit non-goal in Phase 1: in-place gateway process upgrade for a running VM.

6. Health gate:
- after VM boot with new runtime, if healthy, finalize and log success.
- if unhealthy, rollback pointers and boot previous runtime.

## Rollback Semantics
Automatic rollback triggers:
- gateway health check fails after switch,
- runtime process crash-loop during grace window,
- activation post-check timeout.

Rollback action:
- reset `current` to `previous`,
- set `resolved_version` accordingly,
- apply on next boot (or controlled machine restart),
- emit audit event with rollback reason.

## GC Policy
Retention baseline:
- keep `current` and `previous` for every machine reference,
- keep at least N additional recent versions (default N=3).

Never delete:
- any release referenced by machine `current` or `previous` pointers.

GC order:
- delete unreferenced least-recently-used versions first.

## API/Data Model Requirements
Machine model additions:
- `desired_openclaw_version`
- `resolved_openclaw_version`
- `desired_channel` (optional)
- `runtime_source` (`artifact` | `legacy_baked` | `auto` during migration)

Host model additions:
- staged/active/default OpenClaw release ids or versions.

Operational APIs:
- machine update/upgrade endpoint accepts `openclaw_version`.
- dry-run endpoint returns compatibility and staging feasibility.

## Compatibility and Migration
During transition:
- keep baked-in rootfs OpenClaw backward compatibility behind feature flag.
- `runtime_source=auto` behavior:
  - try artifact runtime first,
  - fallback to baked runtime if artifact path is unavailable and fallback is enabled.
- record fallback reason per machine start.
- migrate legacy machines on restart to artifact runtime when possible.

After stable rollout:
- disable baked fallback by default.
- remove OpenClaw install from new rootfs builds (after rollout gates pass).
- keep emergency kill switch for rollback to legacy behavior.

## Observability and Audit
Required telemetry:
- selected version, resolved version, runtime source,
- activation duration and stage timings,
- verification failure reason,
- rollback reason and result.

Required audit events:
- `openclaw.upgrade.requested`
- `openclaw.upgrade.succeeded`
- `openclaw.upgrade.failed`
- `openclaw.rollback.executed`

## Operational Risks and Mitigations
Risk: host disk pressure from multi-version cache.
- Mitigation: refcount-aware LRU GC + disk watermark checks before stage.

Risk: release drift across hosts.
- Mitigation: host artifact-state reporting + reconciliation job.

Risk: incompatible version selection by users.
- Mitigation: strict preflight compatibility gate with actionable errors.

## Open Questions
- Whether compatibility rules live only in manifest, or also in control-plane policy table.
- Default retention `N` by provider class/disk size.
- Whether channel auto-updates should be opt-in per machine or per account.
