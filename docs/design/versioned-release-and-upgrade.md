# Versioned API & Multi-Channel Release Design

> One-stop reference for versioned APIs, tagged releases, multi-version artifacts, channels, gating, maintenance, and testing. Keep this in sync with `docs/testing-strategy.md`.

## Goals
- Support multiple API versions concurrently (breaking changes isolated).
- Ship tagged releases of `agent`, `rootfs`, and `openclaw-runtime` with stable + pre-release channels.
- Let users independently select machine `rootfs` and `openclaw` versions.
- Gate pre-release use to testers; production users stay on stable until they opt in.
- Make upgrades explicit and reversible while reducing fleet-wide disruption.

## Scope
- API contract versioning (backend).
- Release tagging and manifests for `agent`, `rootfs`, and `openclaw-runtime`.
- Host-side multi-version install and selection.
- Upgrade policy and user controls (API/CLI/UI).
- Compatibility matrix and CI/CD test implications.
- Out of scope: data schema migration internals (covered elsewhere), billing plans.

## Current Constraints (Validated 2026-04-03)
- Host rootfs refresh and host agent update both stop all running machines on the target host.
- Agent process owns Firecracker lifecycle; agent restart/shutdown implies VM shutdown.
- Rootfs is staged as a host-global singleton (`.base-rootfs.ext4`) and all VMs clone from that image.
- Placement defaults still rely on `source_image`/`ExpectedImage` coupling.
- Machine create API has no user-facing `rootfs_version` or `openclaw_version` selectors.
- `openclaw` is currently baked into immutable rootfs, so openclaw upgrades are tied to rootfs rollout.

## Terminology
- **Channel:** release track selector (`stable`, `rc`, `dev`) used to resolve default artifact versions when a machine is not pinned to an explicit version.
  - `stable`: production default.
  - `rc`: pre-release candidate track.
  - `dev`: fast-moving development/testing track.
- **Pinned version vs channel:** explicit `rootfs_version`/`openclaw_version` pin takes precedence over channel tracking.
- **Version tag:** `v{MAJOR}.{MINOR}.{PATCH}`; pre-release `vX.Y.Z-rc.N`, `vX.Y.Z-dev.N`.
- **Manifest:** signed JSON published per channel listing artifacts and checksums.
- **Default channel:** `stable` unless host/machine opts into another.
- **Artifact planes:**
  - Host control plane: `agent` (host-wide).
  - VM base plane: `rootfs`.
  - VM app/runtime plane: `openclaw-runtime`.

## API Versioning
- URL-based: `/api/v1/...`, `/api/v2/...`; v1 remains read-only after grace period.
- Media-type alias (optional): `Accept: application/vnd.ocm.v2+json` mapped internally to v2 handlers; URL remains canonical.
- Deprecation headers on v1: `Deprecation: true`, `Sunset: <date>`, `Link: <doc>`.
- OpenAPI per version: generated snapshots under `docs/api/openapi-v{n}.json`; contract tests run per version.
- Routing: version-aware mux dispatching to separate handler sets; shared middleware remains version-neutral.

## Artifacts and Manifests
- Publish channel manifests to GCS:
  - `gs://openclawmachines/agent/manifest-{channel}.json`
  - `gs://openclawmachines/rootfs/manifest-{channel}.json`
  - `gs://openclawmachines/openclaw/manifest-{channel}.json`
- Required manifest fields: `version`, `channel`, `url`, `sha256`, `created_at`, `deprecated`, `notes`.
- Optional compatibility fields:
  - Rootfs manifest: `compatible_openclaw`, `data_version`.
  - OpenClaw manifest: `agent_constraint`, `rootfs_constraint`, optional allow/deny lists for exceptions.
- Signing rollout:
  - Phase 1: checksum verification required; signature verification optional/flagged.
  - Phase 2+: signature verification required (fail closed).
- Retention: keep last 5 versions per channel; GC older versions.

## Host-Side Multi-Version Install
- Install locations:
  - Agents: `/var/lib/ocm/agent/{version}/agent`
  - Rootfs: `/var/lib/ocm/images/{version}/rootfs.ext4`
  - OpenClaw runtime: `/var/lib/ocm/openclaw/{version}/` (bundle unpack)
- Host artifact state tracks staged/active/default pointers per artifact kind.
- Download policy:
  - Hosts auto-fetch `stable` unless `ALLOW_PRERELEASE=1`.
  - Per-machine request can allow pre-release with `allow_prerelease=true`.
  - If requested version absent and fetch is disallowed, return 409 with available versions.
- Cleanup: nightly GC removes least-recently-used artifacts after retention limit.

## Machine Version Selection
- API additions (v2):
  - Machine create/update accepts `rootfs_version`, `openclaw_version`, `channel` (optional).
  - Response echoes resolved versions and compatibility decision.
  - Validation rejects unknown/disallowed/incompatible combinations and returns `available_versions`.
- Scheduler:
  - Must place only on hosts that can stage requested versions.
  - Fallback allowed only with explicit `allow_fallback=true`.
  - Avoid host `source_image` as a hard requirement for runtime selection.

## Host Agent Updates and VM Lifecycle Ownership
- Near term: agent remains host-wide and still requires maintenance/drain for replacement.
- Target state: split into:
  - `vm-supervisor` (stable, owns Firecracker lifecycle).
  - `control-agent` (updatable API/reporting/release manager).
- Expected benefit: control-agent updates can occur without stopping user VMs; only supervisor upgrades require maintenance.

## Pre-Release Gating
- Account/user flag `beta_tester=true` enables RC/dev visibility.
- UI/CLI surfaces channel choice when the flag is set; hides RC/dev otherwise.
- Host opt-in: `ALLOW_PRERELEASE=1` or host label in control plane.
- RC to stable promotion after burn-in window (for example 7 days) unless blocked.

## Upgrade Flows
- **Manual machine upgrade:** `POST /machines/{id}/upgrade` with `rootfs_version` and/or `openclaw_version`, or `channel`. Supports `dry_run=true`.
- **Track channel:** per machine/account setting `channel=stable|rc|dev`; controller upgrades when newer compatible versions are available.
- **Rollback:** same endpoint with older version (must be installed or fetch-allowed).
- **Host prefetch:** background job stages latest allowed artifacts for faster machine startup.
- **Host agent upgrade:** explicit operator action during maintenance until supervisor split is complete.

## Release Pipeline
- Tag in git: `vX.Y.Z` (optional `-rc.N`).
- CI stages:
  1) Build `agent`, `rootfs`, `openclaw-runtime`.
     - OpenClaw runtime is built/published independently from rootfs (`build-openclaw`, `upload-openclaw`, `build-upload-openclaw`).
     - `build-components` should include OpenClaw artifact publication.
  2) Run fast tests (`make test`, `make typecheck`, gateway E2E).
  3) Run KVM smoke with selected version combinations.
  4) Publish artifacts and update manifests for RC/stable.
  5) For stable, run integration suite and auto-demote manifest entry on critical regression.

## Testing Matrix (per tag)
- Unit, lint, typecheck.
- Gateway E2E (OpenClaw config paths and provider credentials).
- Workflow DB tests (`make test-workflows`).
- KVM smoke:
  - old rootfs + new openclaw.
  - new rootfs + old openclaw.
  - current rootfs + current openclaw.
- Agent compatibility smoke against current and previous minor control-plane versions.
- Contract tests per API version.

## UI/CLI Changes
- Machine create/update includes explicit selectors for `rootfs` and `openclaw` versions.
- Display resolved rootfs/openclaw/channel per machine.
- "Upgrade now" supports dry-run compatibility preview and pre-release warnings.
- UI artifact-selection behavior:
  - Create flow (`MachineCreate` and dashboard create modal) supports:
    - `channel` picker (`stable`/`rc`/`dev` gated by beta flag), or
    - explicit `rootfs_version` + `openclaw_version` pin.
  - Machine detail includes "Runtime Versions" card:
    - desired and resolved versions for rootfs/openclaw,
    - version source (`pinned` or `channel`),
    - compatibility/preflight result before submit.
  - Update flow allows OpenClaw-only change without requiring rootfs change.
- CLI examples:
  - `ocm machines upgrade --rootfs v1.9.2 --openclaw v2026.4.0`
  - `ocm machines upgrade --channel rc --allow-prerelease`

## Compatibility Rules
- Control-plane and agent protocol remains backward compatible across N minor versions.
- Machine launch enforces compatibility matrix (`agent x rootfs x openclaw`).
- Rootfs/openclaw upgrades are user-controlled unless channel tracking is explicitly enabled.
- API v1 sunset is announced with explicit dates and read-only transition.

## Security and Safety
- Phase 1: checksum verification required before install; signature verification optional/flagged.
- Phase 2+: signed manifests required and enforced fail-closed.
- Pre-release disabled by default; both host and user opt-in required.
- Clear audit log entries for upgrade attempts and outcomes.
- Download concurrency and bandwidth caps to avoid host overload.

## Maintenance Windows
- **Today:** agent update still requires host maintenance and VM drain.
- **Target:** only supervisor upgrades require maintenance; control-agent upgrades become non-disruptive.
- Rootfs/openclaw upgrades are per-machine and should not require unrelated VMs on the same host to stop.

## Migration Plan (Incremental)
1) Add manifest publishing for `openclaw-runtime` alongside existing artifacts.
2) Add release tables and host artifact state (`staged`/`active` pointers).
3) Add machine API fields: `rootfs_version`, `openclaw_version`, `channel`.
4) Implement host multi-version cache and compatibility checks at placement/start.
5) Move OpenClaw out of immutable rootfs into independently versioned runtime artifact.
6) Remove hard placement dependency on `source_image`; use artifact compatibility/capabilities.
7) Split `vm-supervisor` from `control-agent` to decouple control updates from VM lifecycle.
8) Keep migration compatibility mode (`runtime_source=auto`) so legacy baked rootfs machines continue booting while artifact runtime rolls out.
9) After rollout gates pass, remove baked OpenClaw from new rootfs builds.

## Operational Simplicity
- Keep release rules in this doc and testing rules in `docs/testing-strategy.md`.
- Prefer explicit compatibility checks over implicit snapshot/image assumptions.
- Retention stays bounded (3 to 5 versions per channel).
- Fail closed on artifact trust validation failures.

## Open Questions
- What is the default retention on hosts with constrained disks?
- Should fetch-on-demand be allowed for user-pinned historical versions?
- Which component signs manifests in OSS/white-label deployments?
- What is the minimum viable supervisor split to remove stop-the-world control-agent updates?
