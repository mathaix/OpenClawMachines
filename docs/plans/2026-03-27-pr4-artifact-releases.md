# PR4 — Immutable Artifact Releases (Rootfs/Browser/OpenClaw/Agent)

Goal: replace mutable host cache assumptions with explicit release records and staged/active pointers so users can independently choose `rootfs` and `openclaw` versions per machine.

References:
- `docs/design/versioned-release-and-upgrade.md` (channel semantics and compatibility model)
- `docs/design/2026-04-03-openclaw-runtime-artifact-delivery.md`

## Current Problem Statement (2026-04-03)
- Rootfs refresh and agent update paths both stop all running machines on the host.
- Rootfs is staged as a single host-global base image; no per-machine version pin at VM create.
- OpenClaw is baked into rootfs, so OpenClaw upgrades are coupled to rootfs rollout.
- Agent owns Firecracker lifecycle; agent restart implies VM shutdown.
- Placement still carries `source_image` coupling that conflicts with runtime artifact selection.

## Scope
- Add tables:
  - `artifact_releases` for immutable release metadata and compatibility.
  - `host_artifact_state` for staged/active/default pointers (`rootfs`, `browser_rootfs`, `openclaw`, `agent`).
- Extend machine API/data model to include desired `rootfs_version` and `openclaw_version`.
- Agent heartbeat reports active release IDs per artifact kind.
- Add promotion/rollback API and CLI support for list/stage/promote/rollback.
- Remove mutable cache assumptions from host update flows.
- Add signing and verification for release manifests/artifacts.
  - Phase 1: checksum required, signature optional/flagged.
  - Phase 2+: signature required (fail closed).

## Out of Scope (PR4)
- Full VM supervisor split from control-agent binary/process.
- Billing and policy packaging changes.

## Delivery Plan
1. Schema and Backfill
- [ ] Add `artifact_releases` table with kind/version/channel/url/hash/signature/compat fields.
- [ ] Add `host_artifact_state` table keyed by host and artifact kind.
- [ ] Backfill initial release records from current host-reported versions/manifests.

2. Host State and Reporting
- [ ] Agent reports active/staged release IDs in heartbeat.
- [ ] Backend persists release pointers without relying on mutable filenames.
- [ ] Admin host APIs return release IDs for all artifact kinds.

3. Machine Version Selection
- [ ] Add machine desired/resolved fields: `rootfs_version`, `openclaw_version`, `channel`.
- [ ] Validate compatibility (`agent x rootfs x openclaw`) before placement/start.
- [ ] Update scheduler to avoid hard dependency on `source_image`.

4. Promotion and Rollback APIs
- [ ] Implement stage/promote/rollback flows with atomic pointer updates.
- [ ] Promote/rollback must be idempotent and auditable.
- [ ] Failed promotion must retain previous active release.

5. OpenClaw Decoupling
- [ ] Introduce `openclaw` artifact release kind.
- [ ] VM boot path resolves OpenClaw runtime from selected artifact, not only from baked rootfs.
- [ ] Keep temporary fallback to baked rootfs OpenClaw during migration.
- [ ] Add independent OpenClaw artifact build/upload targets:
  - `build-openclaw`, `upload-openclaw`, `build-upload-openclaw`.
- [ ] Include OpenClaw artifact in `build-components` orchestration.

6. Security and Ops
- [ ] Implement checksum verification (mandatory, fail closed).
- [ ] Implement signature verification path behind feature flag (Phase 1 optional; Phase 2+ required fail closed).
- [ ] Add GC policy for staged artifacts and LRU pruning.
- [ ] Add mixed-provider rollout controls (batch size, pause, rollback trigger).

## Cutover Strategy
- Phase A: dual-write release info while still reading legacy cache paths.
- Phase B: switch all reads to release IDs and host artifact state.
- Phase C: remove legacy mutable-cache update paths.
- Phase D: enforce compatibility checks for machine-selected rootfs/openclaw.

## Verification
- Functional:
  - [ ] Promote/rollback tests (including partial failures and retries).
  - [ ] Heartbeat reporting tests (active/staged pointers drift detection).
  - [ ] Machine create/start with explicit rootfs/openclaw selection.
- Compatibility:
  - [ ] Mixed-version smoke: old rootfs + new openclaw.
  - [ ] Mixed-version smoke: new rootfs + old openclaw.
  - [ ] Agent compatibility gate coverage.
- Load and rollout:
  - [ ] Staggered promotion across GCP/OVH/Hetzner with no state drift.
  - [ ] Disk pressure and GC behavior under multi-version cache.

## Success Criteria
- Users can independently choose `rootfs` and `openclaw` version per machine.
- Host state is expressed in immutable release IDs, not mutable cache files.
- Promotion/rollback is safe, atomic, auditable, and provider-agnostic.
- Existing stop-the-world behavior is isolated to agent lifecycle updates only (until supervisor split).
