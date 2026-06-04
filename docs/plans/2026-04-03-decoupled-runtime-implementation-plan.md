# Decoupled Runtime Implementation Plan

Date: 2026-04-03
Status: Draft
Owner: Platform Runtime
References:
- `docs/design/versioned-release-and-upgrade.md` (channel semantics and release model)
- `docs/design/2026-04-03-openclaw-runtime-artifact-delivery.md`

## Objective
Implement phased decoupling so users can independently choose machine `rootfs_version` and `openclaw_version`, with a clear path to independent PTY updates and non-disruptive host control-plane updates.

## Goals
- Independent per-machine version selection for `rootfs` and `openclaw`.
- OpenClaw upgrades/rollbacks without rootfs rebuild.
- Replace placement `source_image` coupling with compatibility/capability checks.
- Preserve legacy VM behavior during migration with safe fallback.
- Prepare for PTY and host control lifecycle decoupling.

## Non-Goals (This Plan)
- Full billing/policy redesign.
- Single-shot cutover of all hosts and machines.
- Immediate removal of all legacy fields in first rollout.
- In-place OpenClaw process replacement inside already-running VMs in Phase 1.

## Success Criteria
- Machine create/start supports explicit `rootfs_version` and `openclaw_version`.
- OpenClaw upgrade changes only target machine behavior.
- Mixed compatible combos work (`old rootfs + new openclaw`, `new rootfs + old openclaw`).
- Failed OpenClaw updates auto-rollback with no data loss.
- Legacy machines continue to boot during transition.

## Delivery Strategy
- Feature-flagged rollout by phase.
- Dual-write, dual-read migration strategy.
- Canary host group first, then account opt-in, then general availability.
- Hard rollback gates at every phase.

## Latest Status Update (2026-04-05)

### Completed Since Draft
- [x] Added agent-side OpenClaw artifact staging (`DR-101`) with:
  - checksum verification,
  - local/GCS manifest resolution,
  - concurrent stage locking,
  - atomic publish into `OPENCLAW_RUNTIME_DIR/releases/<version>`.
- [x] Added OpenClaw artifact build/publish entrypoints:
  - `make build-openclaw`
  - `make upload-openclaw`
  - `make build-upload-openclaw`
  - `scripts/build-openclaw-runtime.sh`
  - `scripts/upload-openclaw.sh`
- [x] Closed the original `DR-102` critical gap:
  - orchestrator now stages the selected release on the host before boot,
  - then mirrors it into `/data/ocm/runtime/openclaw` for Firecracker guests.
- [x] Added a real positive-path Firecracker integration test for staged artifact runtime boot.
- [x] Kept the existing failure-mode coverage for:
  - `runtime_source=auto` fallback to baked runtime,
  - `runtime_source=artifact` crash-loop when the artifact binary is unavailable.
- [x] Added host-authoritative machine runtime pointers (`selected`, `resolved`, `current`, `previous`) and derive the guest mirror from that host state.
- [x] Started `DR-104` on the backend:
  - `POST /machines/{id}/openclaw/upgrade`,
  - `POST /machines/{id}/openclaw/rollback`,
  - optional apply-on-next-boot behavior,
  - controlled restart for running machines,
  - gateway health gate with automatic rollback to the previously running version.

### Verified in Latest Analysis
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/rootfs/...`
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/orchestrator/...`
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./cmd/agent ./internal/config/...`
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/machines/...`
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/api/... -run 'TestHandle(UpdateMachine|UpgradeMachineOpenClaw|RollbackMachineOpenClaw)'`
- [x] `make test-runtime-selection-integration`

### Current Gaps
- [ ] The new Docker-based build and `gsutil` upload flow for OpenClaw artifacts has not yet been exercised against a real GCS bucket.
- [ ] `DR-104` still needs dedicated Firecracker/KVM update/rollback e2e and frontend wiring.

### Immediate Next Actions
- [ ] Run `make build-upload-openclaw` on a real builder and validate the published `OPENCLAW_GCS_MANIFEST`.
- [ ] Extend the machine-scoped OpenClaw upgrade/rollback flow with Firecracker/KVM e2e.
- [ ] Wire the new backend route into machine detail/update UI.

### Branch Implementation Status (`rootfs-rearchitecture`, 2026-04-05)

**Phase 0 overall: ~65% complete.** Core schema, API, resolver, frontend create flows, and store scan coverage are in place. Missing: release/host artifact tables, store CRUD for releases, heartbeat wiring, machine detail UI, beta-gating, and frontend tests.

#### Phase 0 Progress by Ticket

| Ticket | Status | Notes |
|--------|--------|-------|
| DR-001 | **Partial** | Migration 065 adds 7 machine columns (`desired_rootfs_version`, `desired_openclaw_version`, `desired_channel`, `resolved_rootfs_version`, `resolved_openclaw_version`, `version_source`, `runtime_source`) with CHECK constraints and legacy backfill. **Missing**: `artifact_releases` table and `host_artifact_state` table (planned as migrations 066/067). |
| DR-002 | **Partial** | `store.Machine` struct extended with 7 new `*string` fields. `scanMachine` column order verified correct. `CreateMachine` and `UpdateMachine` include new fields. Store scan coverage now exists for the 7 runtime-selection columns. **Missing**: release and host artifact state CRUD methods plus write-path tests. |
| DR-003 | **Done** | API create accepts `rootfs_version`, `openclaw_version`, `channel`, `runtime_source` with validation. Update handler supports channel↔pinned transitions (implicitly clears the opposite). Tests: `SwitchesFromChannelToPinned`, `SwitchesFromPinnedToChannel`, `RejectsPinnedAndChannelTogether`, `RejectsBlankRuntimeSource`, `PersistsPinnedRuntimeSelection`, `RejectsInvalidRuntimeSource`. |
| DR-004 | **Done** | `version_resolver.go` implements 4-level precedence (`desired → resolved → legacy → default`). `deriveRuntimeVersionSource` returns `pinned`/`channel`/`default`. Feature flag `FF_RUNTIME_VERSION_RESOLVER` wired through config and `main.go`. `runtime.go` now preserves the resolver-selected rootfs version when writing resolved machine versions. |
| DR-005 | **Not started** | No heartbeat artifact state reporting. |
| DR-006 | **Partial** | `CreateMachineModal.tsx` and `MachineCreate.tsx` have channel/pinned toggle UI. `api.ts` and `types.ts` extended. **Missing**: `MachineView.tsx` runtime display, `OverviewTab.tsx` update affordance, beta-gating of `rc`/`dev` channels, frontend tests. |
| DR-901 | **Partial** | `FF_RUNTIME_VERSION_RESOLVER` implemented. **Missing**: other phase flags (`FF_OPENCLAW_ARTIFACT_RUNTIME`, etc.) — expected, as those belong to later phases. |
| DR-902 | **Not started** | No observability or audit fields yet. |

#### Phase 1 Early Work

| Ticket | Status | Notes |
|--------|--------|-------|
| DR-101 | **Partial** | `openclaw_fetcher.go` stages OpenClaw artifacts into `OPENCLAW_RUNTIME_DIR/releases/<version>` with checksum verification, locking, and atomic publish. Build/publish entrypoints exist (`build-openclaw`, `upload-openclaw`, `build-upload-openclaw`). **Missing**: version validation before path joins, symlink-safe extraction hardening, manifest-path-aware readiness checks, signature verification, and real GCS publish validation. |
| DR-102 | **Partial** | `RuntimeSelection` flows control plane -> agent -> orchestrator. The orchestrator now stages the selected host release, persists host-authoritative machine runtime pointers, and mirrors the selected release into `/data/ocm/runtime/openclaw` before boot, setting guest-visible `OPENCLAW_BIN` and bundled plugin paths. **Missing**: strict `artifact` mode must fail closed before boot when the staged runtime is unavailable, plus fuller request-path unit coverage across the agent/client boundary. |
| DR-103 | **Partial** | `init-openclaw.sh` now covers the critical migration behaviors: artifact runtime boot, `auto` fallback to baked runtime when the staged binary is missing, and crash-loop protection for strict `artifact` mode. **Missing**: PTY/login shell parity with the selected runtime plus post-cutover cleanup and removal of baked-rootfs assumptions in rootfs build/runtime docs. |
| DR-104 | **Partial** | Backend endpoints now persist OpenClaw-only version changes, support apply-on-next-boot or controlled restart for running machines, and health-gate the restart with automatic rollback to the previously running version. **Missing**: fix persistence ordering and concurrency around the machine operation lock, allow first-ever immediate upgrades, preserve restart soft-state on failures, decouple long-running restart work from request-context cancellation, frontend/UI wiring, and Firecracker/KVM update/rollback e2e coverage. |
| DR-105 | **Partial** | Firecracker/KVM integration now covers positive artifact boot plus both failure modes via `make test-runtime-selection-integration`. **Missing**: machine update/rollback e2e once `DR-104` exists, and the current branch is red because the fallback case times out waiting for VM readiness. |

#### Known Code Issues

1. **`DR-104` persistence ordering is unsafe**: the upgrade path writes the requested version before it acquires the machine operation lock and before the restart health gate succeeds.
2. **First-ever immediate upgrades are rejected**: `apply_now` currently blocks when the machine has no previous/current OpenClaw version, even on upgrade.
3. **Restart failure handling still tears down restart soft-state**: restart failures still hard-release placement and tear down routes/tunnels in `pollVMStatus`.
4. **Request cancellation can strand upgrade/restart work**: the handler passes the request context through stop/start/health-gate work instead of detaching a durable operation context.
5. **Strict artifact mode does not fail closed pre-boot**: when no staged runtime is available, the orchestrator can still boot the VM and let the guest discover the failure later.
6. **Artifact extraction still allows symlink traversal**: tar entry names are sanitized, but symlink targets are not.
7. **Version input is still not validated**: requested OpenClaw versions are trimmed but not constrained before they are used in host path joins.
8. **PTY/login shell runtime parity is incomplete**: the selected runtime env is exported for gateway startup but not for general shell sessions.
9. **OpenClaw publish path still unverified in production**: the Docker-based artifact packaging and `gsutil` upload flow exist, but a real publish/runbook validation is still pending.
10. **Frontend UI not gated by feature flag**: Version selector UI is always visible regardless of `FF_RUNTIME_VERSION_RESOLVER` state. Should be conditionally rendered.

#### Current Test Signal

- `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/rootfs/...` ✅
- `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/store/...` ✅
- `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/machines/...` ✅
- `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/orchestrator/...` ✅
- `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/api/...` ⚠️ blocked in this sandbox by IPv6 `httptest.NewServer` binding in `credentials_test.go`
- `make test-runtime-selection-integration` ❌ currently failing on `TestInit_RuntimeSelectionAutoFallsBackToBaked` with a VM readiness timeout on port `7681`

## Phase 0: Release Model and API Surface

### Scope
- Add immutable release metadata and host artifact state.
- Add machine desired/resolved version fields.
- Keep legacy fields operational.

### Backend Work
- Add tables:
  - `artifact_releases` (`kind`, `version`, `channel`, `url`, `sha256`, `signature`, compatibility fields).
  - `host_artifact_state` (`host_id`, `kind`, `staged_release_id`, `active_release_id`, `default_release_id`).
- Extend machine schema:
  - `desired_rootfs_version`, `desired_openclaw_version`, `desired_channel`.
  - `resolved_rootfs_version`, `resolved_openclaw_version`, `version_source`.
- API v2 additions:
  - machine create/update accepts desired versions and channel.
  - machine get/list returns desired/resolved versions.
- Resolver:
  - priority = explicit desired -> resolved -> legacy inferred -> host default.

### Frontend/UI Work
- Add artifact selectors to machine create flows:
  - `frontend/src/pages/MachineCreate.tsx`
  - `frontend/src/components/CreateMachineModal.tsx`
- Extend API client + types for desired/resolved fields and compatibility responses:
  - `frontend/src/lib/api.ts`
  - `frontend/src/lib/types.ts`
- Add machine detail runtime display and update affordance:
  - `frontend/src/pages/MachineView.tsx`
  - `frontend/src/pages/machine-tabs/OverviewTab.tsx` (or a dedicated Runtime tab/card)
- Add beta-gated channel picker visibility (`stable` always, `rc/dev` only for eligible users).

### Agent/Host Work
- Heartbeat reports active/staged release IDs per artifact kind.
- Host admin endpoints expose current artifact state.

### Tests
- Unit: resolver precedence, schema model mapping, validation failures.
- Integration: heartbeat update/persistence, API read/write compatibility.
- Migration test: legacy machine with null desired fields still starts.
- Frontend:
  - unit tests for form payloads (pin vs channel),
  - E2E create/update flows with version selectors and compatibility error rendering.

### Exit Criteria
- New schema live with backward compatibility.
- Resolver enabled behind `FF_RUNTIME_VERSION_RESOLVER`.
- No regressions in existing machine lifecycle endpoints.

---

## Phase 1: Decouple OpenClaw from Rootfs

### Scope
- Move OpenClaw runtime to standalone artifact.
- Keep temporary fallback to baked rootfs OpenClaw.
- Build and publish OpenClaw artifact independently from rootfs (`make build-upload-openclaw`).

### Runtime Design
- Host cache path: `/var/lib/ocm/openclaw/{version}/...`.
- Host-managed machine pointers are authoritative (keyed by machine id).
- VM `/data/ocm/runtime/openclaw` pointers are mirrors of host state.
- `runtime_source` modes during migration:
  - `artifact`: require artifact runtime.
  - `legacy_baked`: require baked rootfs runtime.
  - `auto`: try artifact first, fallback to baked runtime when enabled.
- VM boot runtime resolution:
  - If selected OpenClaw artifact available and verified, use artifact path.
  - Else if fallback enabled, use baked rootfs OpenClaw path.
  - Else fail machine start with actionable error.
- Machine state includes `resolved_openclaw_version` and `legacy_fallback_used`.

### Init/Boot Changes
- Replace hard dependency on rootfs OpenClaw path with explicit runtime exec resolution:
  - prefer `OPENCLAW_BIN` when set (artifact runtime),
  - fallback to `openclaw` from PATH for legacy baked runtime.
- Optional compatibility symlink may exist, but must not be the only resolution path.
- Keep existing gateway startup contract and health checks.
- Add startup telemetry: chosen runtime source (`artifact` or `legacy_baked`).
- Export `OPENCLAW_BUNDLED_PLUGINS_DIR` based on selected runtime (`${current}/dist/extensions` for artifact mode).

### Build/Release Changes
- Add OpenClaw artifact build + upload pipeline (Option A: extract runtime tree from Docker build output).
- Introduce Make targets:
  - `build-openclaw`
  - `upload-openclaw`
  - `build-upload-openclaw`
- Update `build-components` to include OpenClaw artifact distribution.

### Tests
- Unit: OpenClaw path resolver and fallback behavior.
- Integration: artifact staging, checksum failures, startup selection.
- Integration must explicitly cover guest boot outcomes for:
  - `runtime_source=auto` with no staged artifact binary (falls back to baked runtime),
  - `runtime_source=artifact` with no staged artifact binary (fails into crash-loop protection with actionable logs).
- E2E:
  - create/start with explicit OpenClaw version.
  - update only OpenClaw version on one machine (apply on next boot or controlled machine restart).
  - rollback on health failure.

### Exit Criteria
- OpenClaw update/rollback works machine-by-machine.
- Rootfs rebuild no longer required for OpenClaw-only releases.
- Canary accounts pass upgrade/rollback E2E suite.
- OpenClaw artifact can be promoted independently via manifest updates.

---

## Phase 2: Plugin Tier Separation

### Scope
- Formalize bundled vs user plugin paths.

### Runtime Rules
- Bundled plugins (read-only): tied to OpenClaw artifact release.
- User plugins (writable): persisted on `/data/home/openclaw/.openclaw/extensions`.
- Load precedence: user plugin overrides bundled plugin with same id.

### Tests
- Integration: both paths loaded, conflict precedence, persistence across OpenClaw upgrade.
- E2E: install plugin from terminal in user path, upgrade OpenClaw, verify plugin remains.

### Exit Criteria
- Plugin ownership model is explicit and enforced.
- No user plugin loss during OpenClaw upgrades.
- Terminal-driven plugin installs persist across OpenClaw version switches.

---

## Phase 3: Placement and Compatibility Gate

### Scope
- Remove hard `source_image` dependency from placement decisions.
- Enforce explicit compatibility checks.

### Placement Changes
- Eligibility based on:
  - host supports/stages requested rootfs release,
  - host supports/stages requested OpenClaw release,
  - compatibility matrix (`agent x rootfs x openclaw`) passes.
- Return clear 409/412 class errors for unavailable/incompatible requests.

### Tests
- Unit: compatibility policy matrix.
- Integration: placement on mixed host fleets with selective staged artifacts.
- E2E: incompatible selection rejected before VM start; compatible selection succeeds.

### Exit Criteria
- `source_image` no longer required for runtime version matching.
- Placement behavior validated across provider classes.

---

## Phase 4: PTY Decoupling

### Scope
- Replace guest dependency on `agent --pty-server` with dedicated PTY runtime artifact.

### Runtime Changes
- Introduce guest PTY binary/service (for example `ocm-ptyd`) with its own version.
- Add machine/host PTY release tracking.
- Update health probes to PTY release-aware checks.

### Build Changes
- Remove rootfs requirement to inject host `agent` binary for PTY.
- Keep compatibility mode during migration behind flag.

### Tests
- Integration: PTY startup/restart/health/rollback.
- E2E: terminal websocket continuity across PTY-only update.

### Exit Criteria
- PTY updates are independent of host agent updates.
- Rootfs build no longer depends on host `agent` injection for terminal functionality.

---

## Phase 5: Host Lifecycle Split (Supervisor vs Control Agent)

### Scope
- Separate VM lifecycle owner from updatable control API process.

### Design
- `vm-supervisor`: stable process owning Firecracker lifecycle and state.
- `control-agent`: API, reporting, update coordination, artifact state sync.
- Control-agent restarts must not stop running VMs.

### Tests
- Integration: control-agent restart while VMs continue running.
- E2E: trigger control-agent update with active machines, verify no VM stop.

### Exit Criteria
- Control-agent upgrades are non-disruptive.
- Maintenance windows required only for supervisor upgrades.

---

## Cross-Cutting Test Plan

### Test Layers
- Unit: resolver logic, compatibility checks, rollback state machines.
- Integration: DB + store + agent/orchestrator interactions.
- E2E (real Firecracker): lifecycle, update, rollback, terminal/gateway paths.
- Soak/canary: staggered host/account rollout with drift detection.

### Required E2E Matrix
- Machine create/start with explicit versions.
- OpenClaw-only update on single machine.
- OpenClaw rollback on failed health.
- Mixed version compatibility combinations.
- Legacy machine boot under dual-read/write resolver.
- Terminal and gateway route health after each update type.

### Failure Injection
- Corrupt artifact payload.
- Signature mismatch (when signature verification is enabled).
- Staging timeout/network failure.
- Gateway crash-loop after version switch.
- Host disk pressure during artifact caching.

## Legacy VM Migration Path
1. Schema backfill from legacy version signals (`rootfs_snapshot`, host manifest, `/data/.openclaw_version` when available).
2. Resolver defaults legacy VMs to inferred versions.
3. First-restart migration attempts artifact OpenClaw path.
4. On failure, automatic legacy baked fallback with reason recorded.
5. Promote VM to managed mode after successful boots/upgrades.
6. Remove fallback only after fleet threshold + error budget target.

## Rollout Plan
1. Internal dev hosts only.
2. Canary host pool (5%).
3. Opt-in accounts.
4. Regional staged rollout.
5. GA with legacy fallback still enabled.
6. Legacy path deprecation and removal in a later release.

## Post-Cutover Cleanup
- After stability gates pass:
  - remove baked OpenClaw install from `rootfs/Dockerfile.openclaw`,
  - reduce rootfs image size and rebuild baseline snapshots,
  - keep emergency rollback flag for temporary legacy re-enable window.

## Operational Guardrails
- Feature flags per phase:
  - `FF_RUNTIME_VERSION_RESOLVER`
  - `FF_OPENCLAW_ARTIFACT_RUNTIME`
  - `FF_PLUGIN_TIER_SPLIT`
  - `FF_COMPAT_PLACEMENT_GATE`
  - `FF_PTY_ARTIFACT_RUNTIME`
  - `FF_AGENT_SUPERVISOR_SPLIT`
- Automated rollback triggers:
  - startup health failures,
  - error-rate threshold breach,
  - canary failure gates.
- Audit and observability:
  - release id chosen, source, fallback reason, rollback reason, health timeline.

## PR Breakdown (Suggested)
- PR1: schema + models + basic APIs.
- PR2: resolver + dual-read/write compatibility.
- PR3: host artifact state heartbeat wiring.
- PR4: OpenClaw artifact staging + runtime selection + fallback.
- PR5: OpenClaw update/rollback endpoints + health gates.
- PR6: plugin tier separation.
- PR7: compatibility matrix + placement gate.
- PR8: PTY runtime artifact path.
- PR9: supervisor/control-agent split (multi-PR epic).

## Open Questions
- Exact compatibility policy source of truth (manifest semver constraints vs control-plane policy table).
- Host disk retention policy defaults by provider class.
- Whether self-managed terminal updates are supported in managed mode.
- Time window and criteria for removing legacy fallback.
