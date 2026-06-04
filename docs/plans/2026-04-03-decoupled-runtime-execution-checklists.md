# Decoupled Runtime Execution Checklists

Date: 2026-04-03
Status: Draft
Companion doc: `docs/plans/2026-04-03-decoupled-runtime-implementation-plan.md`
Channel semantics: see `docs/design/versioned-release-and-upgrade.md` (Terminology section)

This document breaks the implementation into ticket-ready execution checklists with explicit file-level scopes.

## Conventions
- Ticket IDs: `DR-<phase><nn>` (for example `DR-001`, `DR-104`).
- Every ticket must land behind a feature flag unless otherwise noted.
- Every ticket includes unit/integration test updates in the same PR.

## Latest Status Update (2026-04-05)

### Completed Since Last Review
- [x] Added host-side OpenClaw artifact staging (`DR-101`) with manifest resolution, checksum verification, concurrent stage locking, and atomic publish into `OPENCLAW_RUNTIME_DIR/releases/<version>`.
- [x] Added OpenClaw artifact build/publish entrypoints:
  - `make build-openclaw`
  - `make upload-openclaw`
  - `make build-upload-openclaw`
  - `scripts/build-openclaw-runtime.sh`
  - `scripts/upload-openclaw.sh`
- [x] Closed the original Firecracker delivery gap (`DR-102`): the orchestrator now stages the selected release on the host and mirrors it into `/data/ocm/runtime/openclaw` before boot.
- [x] Added a real positive-path Firecracker/KVM integration test for staged artifact runtime boot and kept both failure-mode tests.
- [x] Added the dedicated Firecracker gate `make test-runtime-selection-integration`.
- [x] Fixed the `UpdateMachineVersion` rootfs clobber in the machine start path and kept structured resolver logging at `machine.start.runtime_resolved`.
- [x] Added host-authoritative machine runtime pointers (`selected`, `resolved`, `current`, `previous`) and derive the guest mirror from that host state on every boot.
- [x] Started `DR-104` on the backend with explicit OpenClaw upgrade/rollback routes, controlled restart, gateway health gate, and automatic rollback to the previously running version.

### Verified During Latest Analysis
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/rootfs/...`
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/orchestrator/...`
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./cmd/agent ./internal/config/...`
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/machines/...`
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/api/... -run 'TestHandle(UpdateMachine|UpgradeMachineOpenClaw|RollbackMachineOpenClaw)'`
- [x] `make test-runtime-selection-integration`

### Current Open Gaps
- [ ] Run the new Docker-based build and `gsutil` upload flow against a real GCS bucket and validate the published `OPENCLAW_GCS_MANIFEST`.
- [ ] Extend `DR-104` with Firecracker/KVM update/rollback e2e coverage.
- [ ] Expose the new OpenClaw upgrade/rollback backend flow in machine detail/update UI.
- [ ] Finish remaining Phase 0 control-plane work: `DR-001`, `DR-002`, `DR-005`, `DR-006`.

### Review Findings (2026-04-05)
- [ ] `DR-104` backend correctness fixes are still required before rollout:
  - the upgrade path persists the requested version before it acquires the machine operation lock,
  - the upgrade path persists the requested version before the restart health gate succeeds,
  - first-ever `apply_now` upgrades are incorrectly rejected,
  - restart failure paths still hard-release placement and tear down tunnels.
- [ ] Artifact/runtime hardening work is still open:
  - `runtime_source=artifact` does not fail closed pre-boot when no staged runtime is available,
  - version input is not validated before it is used in host path joins,
  - tar extraction still allows symlink-based path traversal during staging.
- [ ] Shell/runtime consistency is incomplete:
  - PTY/login shells can still use a different runtime than the gateway process,
  - staged-release readiness still assumes the default `bin/openclaw` / `dist/extensions` layout.

### Current Test Signal (2026-04-05)
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/rootfs/...`
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/store/...`
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/machines/...`
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/orchestrator/...`
- [ ] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/api/...`
  - sandbox-only IPv6 `httptest.NewServer` bind failure in `credentials_test.go`
- [ ] `make test-runtime-selection-integration`
  - currently fails on `TestInit_RuntimeSelectionAutoFallsBackToBaked` due to VM readiness timeout on port `7681`

### Branch Cross-Reference (`rootfs-rearchitecture`, 2026-04-05)

**Phase 0 overall: ~65% complete.** See per-ticket status markers below (✅ DONE / ⚠️ PARTIAL / ❌ NOT STARTED).

Key gaps to close before Phase 0 exit criteria are met:
1. **`artifact_releases` and `host_artifact_state` tables** (DR-001) — these are the foundation for Phase 1 artifact staging. Without them, DR-005 (heartbeat) is blocked.
2. **Write-path store coverage / CRUD** (DR-002) — scan/mapping tests exist now, but release and host artifact state CRUD still does not.
3. **Machine detail UI** (DR-006) — users can create with version selection but can't see or update resolved versions on existing machines.
4. **Beta-gating** (DR-006) — `rc`/`dev` channels visible to all users, should be gated.
5. **Frontend feature-flag gating** — version selector UI shown regardless of `FF_RUNTIME_VERSION_RESOLVER` state.
6. **Observability** (DR-902) — no audit trail for version resolution decisions.

Phase 1 early work is now booting through the real Firecracker path. The remaining gaps are publish-path validation, DR-104 KVM coverage, and frontend wiring.

### Hardening Checklist (Next 1-2 PRs)
- [ ] Wire `make test-runtime-selection-integration` into CI for path filters touching:
  - `backend/internal/orchestrator/**`,
  - `backend/internal/machines/**`,
  - `backend/internal/metadata/**`,
  - `scripts/init-openclaw.sh`,
  - `rootfs/**`.
- [ ] Run `make build-upload-openclaw` on a real builder with Docker and `gsutil`, then document the published manifest/runbook.
- [ ] Add Firecracker/KVM update/rollback coverage for the backend `DR-104` flow and expose the route in the machine detail/update surface.

## Phase 0 — Release Model and API Surface

### DR-001: Add Release and Host Artifact State Schema ⚠️ PARTIAL
Scope: database migration + indexes + backfill scaffolding.

Files
- Create: `backend/migrations/065_artifact_releases.sql` → **Not created** (machine columns live in `065_machine_runtime_selection.sql` instead)
- Create: `backend/migrations/066_host_artifact_state.sql` → **Not created**
- Create: `backend/migrations/067_machine_desired_versions.sql` → **Done** (delivered as `065_machine_runtime_selection.sql`)

Checklist
- [ ] Create `artifact_releases` table with `kind`, `version`, `channel`, `url`, `sha256`, optional `signature`, compatibility columns.
- [ ] Create `host_artifact_state` table with staged/active/default release references by host + kind.
- [x] Add machine desired/resolved version columns and `version_source`. *(migration 065: 7 columns with CHECK constraints and legacy backfill)*
- [x] Add supporting indexes and uniqueness constraints. *(CHECK constraints on `desired_channel` and `runtime_source`)*
- [x] Add rollback notes in migration comments.

Validation
- [ ] `cd backend && go test ./...`
- [ ] Apply migrations in local DB and verify schema with `\d` queries.

---

### DR-002: Store Models and Repository Methods ⚠️ PARTIAL
Scope: store structs/interfaces and Postgres implementation.

Files
- Modify: `backend/internal/store/store.go` → **Done**
- Modify: `backend/internal/store/postgres.go` → **Done**
- Modify: `backend/internal/store/postgres_*test.go` (or add focused tests) → **Done** (`postgres_test.go`)

Checklist
- [x] Extend `Machine` and `Host` models with desired/resolved release fields. *(7 new `*string` fields added to `Machine` struct; `scanMachine` column order verified correct; `CreateMachine` includes COALESCE for runtime_source default)*
- [ ] Add CRUD methods for release records and host artifact state. *(blocked on DR-001 `artifact_releases`/`host_artifact_state` tables)*
- [x] Add dual-read helpers for legacy fields (`rootfs_snapshot`, old openclaw version). *(resolver reads legacy fields via `firstNonEmptyPtr` precedence chain)*
- [x] Add unit tests for scan/mapping. *(`TestScanMachine_MapsRuntimeSelectionFields`)*
- [ ] Add write-path tests.

Validation
- [ ] `cd backend && go test ./internal/store/...`

---

### DR-003: API Surface for Machine Desired Versions ✅ DONE
Scope: request/response model and validation.

Files
- Modify: `backend/internal/api/machines.go` → **Done**
- Modify: `backend/internal/api/server.go` (route wiring if needed) → **No changes needed**
- Modify: `backend/internal/api/resolver_test.go` (or add new tests) → **Done** (tests in `machines_update_test.go` and `machines_create_region_test.go`)
- Modify: `backend/internal/api/machines_create_region_test.go` → **Done**

Checklist
- [x] Add request fields: `rootfs_version`, `openclaw_version`, `channel`. *(create and update handlers accept all three with `runtime_source`)*
- [x] Add validation and error responses with available versions. *(channel validation, runtime_source validation, mutual exclusion of pinned+channel)*
- [x] Return desired/resolved versions in list/get/create/update responses.
- [x] Keep legacy behavior when new fields are absent. *(nil fields pass through; COALESCE defaults `runtime_source` to `'auto'`)*

**Note**: Channel↔pinned transition bug was found and fixed — update handler now implicitly clears the opposite mode when switching. Tests: `SwitchesFromChannelToPinned`, `SwitchesFromPinnedToChannel`, `RejectsPinnedAndChannelTogether`, `RejectsBlankRuntimeSource`.

Validation
- [x] `cd backend && go test ./internal/api/... -run Machine`

---

### DR-004: Resolver and Feature Flag Wiring ✅ DONE
Scope: central version resolver with precedence rules.

Files
- Modify: `backend/internal/machines/runtime.go` → **Done**
- Modify: `backend/internal/config/config.go` → **Done** (`EnableRuntimeVersionResolver` bool)
- Modify: `backend/cmd/server/main.go` → **Done** (wired into both worker and main server `RuntimeConfig`)
- Create: `backend/internal/machines/version_resolver.go` → **Done**
- Create: `backend/internal/machines/version_resolver_test.go` → **Done**

Checklist
- [x] Implement resolver precedence: desired -> resolved -> legacy inferred -> host default. *(`firstNonEmptyPtr` chain for both rootfs and openclaw)*
- [x] Add `FF_RUNTIME_VERSION_RESOLVER` config flag.
- [x] Wire resolver into machine start path. *(`MetadataSelection()` converts resolution to metadata struct)*
- [x] Log resolver decisions including fallback source. *(`machine.start.runtime_resolved` logs resolved versions, `version_source`, and `runtime_source` at the start call site)*

**Known edge case**: `version_resolver.go` still re-reads stale `version_source` from DB when `deriveRuntimeVersionSource` returns `"default"`. Only affects direct DB edits, not API-driven updates. Low priority.

Validation
- [x] `cd backend && go test ./internal/machines/...` *(4 tests: PrefersDesiredVersions, UsesChannelWithResolvedVersions, FallsBackToLegacyAndDefaults, UsesDefaultsWhenNoHistoryExists)*

---

### DR-005: Heartbeat Support for Artifact State ❌ NOT STARTED
Scope: host reports and persists staged/active release ids.

Files
- Modify: `backend/cmd/agent/main.go`
- Modify: `backend/internal/api/agent_heartbeat.go`
- Modify: `backend/internal/store/postgres.go`
- Modify: `backend/internal/api/admin_hosts.go`

Checklist
- [ ] Extend heartbeat payload with artifact state identifiers.
- [ ] Persist reported host artifact state in DB.
- [ ] Expose artifact state in admin host endpoints.
- [ ] Keep backward compatibility when fields are absent.

**Blocked by**: DR-001 (`host_artifact_state` table not yet created).

Validation
- [ ] `cd backend && go test ./internal/api/... -run Heartbeat`

---

### DR-006: Frontend Artifact Selectors (Create + Detail + Update) ⚠️ PARTIAL
Scope: user-visible controls for rootfs/openclaw/channel selection.

Files
- Modify: `frontend/src/pages/MachineCreate.tsx` → **Done**
- Modify: `frontend/src/components/CreateMachineModal.tsx` → **Done**
- Modify: `frontend/src/pages/MachineView.tsx` → **Not done**
- Modify: `frontend/src/pages/machine-tabs/OverviewTab.tsx` (or add dedicated runtime tab component) → **Not done**
- Modify: `frontend/src/lib/api.ts` → **Done**
- Modify: `frontend/src/lib/types.ts` → **Done** (Machine type extended with 7 new fields)
- Modify: `frontend/src/lib/api.test.ts` → **Done** (API client signature tests)
- Modify/Create: frontend page/component tests for create/update version selection → **Not done**

Checklist
- [x] Add create-flow controls for either pinned versions or channel tracking. *(channel/pinned toggle UI in CreateMachineModal)*
- [ ] Gate `rc/dev` channel options behind beta tester visibility rules.
- [ ] Show desired/resolved versions and source (`pinned` vs `channel`) in machine detail.
- [ ] Add update control for OpenClaw-only version changes.
- [ ] Render compatibility/preflight errors with actionable messages.
- [x] Keep backward compatibility when API does not yet return new fields.

**Critique**: Version selector UI is always visible regardless of `FF_RUNTIME_VERSION_RESOLVER` state. Should be conditionally rendered based on feature flag or account eligibility.

Validation
- [ ] `cd frontend && npm test -- --runInBand`
- [ ] Add/update frontend E2E to cover pinned create and openclaw-only update flows.

## Phase 1 — Decouple OpenClaw from Rootfs

### DR-101: OpenClaw Artifact Fetcher/Stager ⚠️ PARTIAL
Scope: host-side OpenClaw artifact lifecycle.

Files
- Create: `backend/internal/rootfs/openclaw_fetcher.go` → **Done**
- Modify: `backend/internal/rootfs/manifest.go` (shared helpers if reused) → **No changes needed**
- Create: `backend/internal/rootfs/openclaw_fetcher_test.go` → **Done**
- Create: `scripts/build-openclaw-runtime.sh` → **Done**
- Create: `scripts/upload-openclaw.sh` → **Done**
- Modify: `Makefile` (new OpenClaw artifact targets) → **Done**

Checklist
- [x] Implement download + checksum verification (mandatory).
- [ ] Validate and constrain version input before using it in path joins.
- [ ] Harden tar extraction against symlink-based path traversal.
- [ ] Make staged-release readiness honor manifest-defined runtime relpaths, not only the default layout.
- [ ] Add pluggable signature verification path behind feature flag (optional in first iteration).
- [x] Stage artifacts under `OPENCLAW_RUNTIME_DIR/releases/<version>` (default: `/var/lib/ocm/openclaw/releases/<version>`).
- [x] Add locking for concurrent stage requests.
- [x] Return clear error classes for verify/download failures.
- [x] Implement artifact build path using Docker output extraction (Option A).
- [x] Add `make build-openclaw`, `make upload-openclaw`, `make build-upload-openclaw`.
- [x] Update `make build-components` to include OpenClaw artifact upload.

**Remaining gap**: the real publish path exists but still needs validation against a real GCS bucket and manifest.

Validation
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/rootfs/...`

---

### DR-102: Pass OpenClaw Runtime Selection Through VM Create Path ⚠️ PARTIAL
Scope: control plane -> agent client -> agent api -> orchestrator -> metadata.

Files
- Modify: `backend/internal/agentclient/client.go` → **Done** (RuntimeSelection passed in VM create)
- Modify: `backend/internal/agentapi/handlers.go` → **Done** (accepts RuntimeSelection)
- Modify: `backend/internal/orchestrator/types.go` → **Done** (`OpenClawVersion` field added to `VMConfig`)
- Modify: `backend/internal/orchestrator/firecracker_linux.go` → **Done** (selected release staged on host, mirrored into VM data volume, guest-visible runtime paths exported)
- Modify: `backend/internal/metadata/metadata.go` → **Done** (`RuntimeSelection` struct with 4 fields)
- Modify: `backend/internal/metadata/server_linux.go` → **Done** (runtime selection exposed in `/v1/machine` response)
- Modify: tests in `backend/internal/agentapi/server_test.go`, `backend/internal/machines/runtime_test.go` → **Partial**

Checklist
- [x] Add selected OpenClaw release/version fields to VM request structs.
- [x] Propagate resolved runtime metadata into VM boot configuration. *(orchestrator stages and injects guest-visible runtime paths before boot)*
- [x] Add host-side machine pointer state (`selected`, `resolved`, `current`, `previous`) as source of truth.
- [x] Mirror the selected release into VM `/data/ocm/runtime/openclaw` as derived boot-time data.
- [x] Surface runtime source (`artifact` vs `legacy_baked` vs `auto`) for observability. *(exposed in metadata `/v1/machine` response)*
- [x] Keep old request fields accepted.
- [ ] Fail strict `runtime_source=artifact` before boot when no staged runtime is available.

**Remaining gap**: the original orchestrator copy/mirror problem is fixed. The missing piece now is fuller request-path unit coverage across the agent/client boundary.

Validation
- [ ] `cd backend && go test ./internal/agentapi/...`
- [x] `cd backend && go test ./internal/machines/...`
- [x] `make test-runtime-selection-integration`

---

### DR-103: Guest Init Runtime Switch + Legacy Fallback ⚠️ PARTIAL
Scope: init script runs OpenClaw from selected runtime path.

Files
- Modify: `scripts/init-openclaw.sh` → **Done for Phase 1 boot path**
- Modify: `scripts/build-rootfs.sh` → **Not done**
- Modify: `rootfs/Dockerfile.openclaw` (minimal fallback footprint) → **Not done**

Checklist
- [x] Add explicit runtime exec resolution in init:
  - prefer `OPENCLAW_BIN`, *(reads from metadata, used in `start_gateway()`)*
  - fallback to PATH `openclaw` for legacy baked mode. *(falls through when `OPENCLAW_BIN` not set)*
- [x] Ensure symlink presence is optional (not the only runtime activation mechanism).
- [x] If artifact runtime unavailable and fallback enabled, run baked binary.
- [x] Implement `runtime_source=auto` behavior (artifact first, then legacy fallback when enabled).
- [x] Emit explicit boot logs for runtime source and fallback reason.
- [x] Preserve current crash-loop protections. *(existing crash-loop logic unchanged)*
- [ ] Mirror selected runtime env into PTY/login shells, not only gateway startup.

**Remaining gap**: post-cutover cleanup is still pending. The init path works for artifact, auto-fallback, and strict artifact failure, but rootfs/build docs still assume a baked fallback exists indefinitely.

Validation
- [x] Boot VM locally and verify logs indicate selected runtime source.
- [x] Gateway health returns running state.
- [x] Integration test verifies `runtime_source=auto` falls back to baked runtime when no artifact binary is staged.
- [x] Integration test verifies `runtime_source=artifact` enters crash-loop protection when `OPENCLAW_BIN` is unavailable.
- [x] Integration test verifies `runtime_source=artifact` boots successfully when a staged runtime is present.

---

### DR-106: Remove Baked OpenClaw from Rootfs (Post-Cutover)
Scope: eliminate fallback payload from new rootfs builds after rollout gates pass.

Files
- Modify: `rootfs/Dockerfile.openclaw`
- Modify: `scripts/build-rootfs.sh` (version manifest population if needed)
- Modify: release docs and migration notes

Checklist
- [ ] Gate by rollout flag and canary success criteria.
- [ ] Remove baked OpenClaw install steps from rootfs Dockerfile.
- [ ] Keep emergency rollback switch to legacy behavior for bounded window.
- [ ] Recompute rootfs size/perf baseline and update operational runbooks.

Validation
- [ ] Build rootfs and verify no baked OpenClaw payload is present.
- [ ] Start machine with artifact runtime and confirm normal gateway startup.

---

### DR-104: Machine OpenClaw Upgrade/Rollback Endpoint ⚠️ PARTIAL
Scope: update machine desired OpenClaw version + boot-time apply + health gate.

Files
- Modify: `backend/internal/api/machines.go` → **Done**
- Modify: `backend/internal/api/server.go` → **Done** (new OpenClaw routes)
- Modify: `backend/internal/machines/runtime.go` → **Done** (restart failure preserves host affinity/data volume)
- Modify: `backend/internal/events/*` (activity logging) → **Done** (machine activity events emitted from API path)
- Modify: `backend/internal/store/postgres.go` → **No changes needed**
- Create: `backend/internal/api/machines_openclaw_test.go` → **Done**

Checklist
- [x] Add update flow for openclaw-only version change. *(`POST /machines/{id}/openclaw/upgrade`)*
- [x] Default behavior: apply on next machine boot. *(`apply_now=false` or stopped machines queue the target version without restart)*
- [x] Optional immediate-apply path: controlled machine restart (not in-place gateway replacement).
- [x] Health-gate completion after boot; rollback to previous version on failure.
- [x] Record activity log entries for success/failure/rollback.
- [ ] Move persistence behind the machine operation lock and successful health gate.
- [ ] Allow first-ever `apply_now` upgrades while still rejecting rollbacks with no previous version.
- [ ] Preserve soft-release placement/tunnel state on restart failure paths.
- [ ] Decouple long-running restart/health-gate work from request-context cancellation.
- [ ] Add frontend wiring for the new backend route.
- [ ] Add handler tests for success, health-gate failure, rollback failure, no-op, and first-upgrade cases.
- [ ] Add dedicated Firecracker/KVM update/rollback e2e coverage.

**Current limitation**: manual rollback currently requires an explicit target version in the request body. The backend does not yet expose historical version choices in the machine detail UI.

Validation
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/api/... -run 'TestHandle(UpdateMachine|UpgradeMachineOpenClaw|RollbackMachineOpenClaw)'`
- [x] `cd backend && GOCACHE=/tmp/go-build /usr/local/go/bin/go test ./internal/machines/...`

---

### DR-105: E2E Coverage for OpenClaw Decoupling
Scope: add/extend real runtime E2E tests.

Files
- Modify: `backend/internal/integration/init_test.go`
- Modify: `backend/internal/integration/helpers_test.go`
- Create: dedicated update/rollback integration coverage after `DR-104`

Checklist
- [x] Add test: create/start with explicit openclaw artifact version.
- [x] Add test: `runtime_source=auto` falls back to baked runtime when the staged artifact binary is unavailable.
- [x] Add test: strict `runtime_source=artifact` fails into crash-loop protection when the staged artifact binary is unavailable.
- [ ] Add test: openclaw-only update on single machine.
- [ ] Add test: forced health failure triggers rollback.

Validation
- [x] Run `make test-runtime-selection-integration` in a KVM-capable environment.
- [ ] Add update/rollback integration coverage after `DR-104`.

## Phase 2 — Plugin Tier Separation

### DR-201: Formalize Bundled + User Plugin Paths
Scope: runtime plugin path contract and precedence.

Files
- Modify: `scripts/init-openclaw.sh`
- Modify: `rootfs/Dockerfile.openclaw`
- Modify: `backend/internal/machines/runtime.go` (if metadata/config assembly needs updates)
- Modify: plugin-related tests in `backend/internal/api/plugins_test.go`

Checklist
- [ ] Define read-only bundled plugin path tied to OpenClaw artifact.
- [ ] Define writable user plugin path on `/data`.
- [ ] Enforce load order: user overrides bundled.
- [ ] Document path contract in architecture/design docs.

Validation
- [ ] Test install user plugin, restart gateway, verify plugin still loaded.
- [ ] Test plugin conflict precedence.

## Phase 3 — Placement and Compatibility Gate

### DR-301: Compatibility Matrix Policy Engine
Scope: central compatibility decision module.

Files
- Create: `backend/internal/fleet/compatibility.go`
- Create: `backend/internal/fleet/compatibility_test.go`
- Modify: `backend/internal/rootfs/manifest.go` (if compatibility fields parsed here)

Checklist
- [ ] Implement `agent x rootfs x openclaw` compatibility evaluator.
- [ ] Support semver constraints (`agent_constraint`, `rootfs_constraint`) from manifest.
- [ ] Support optional allow/deny lists for exception handling.
- [ ] Return structured rejection reasons.

Validation
- [ ] `cd backend && go test ./internal/fleet/...`

---

### DR-302: Remove Hard `source_image` Placement Dependency
Scope: placement store and service behavior.

Files
- Modify: `backend/internal/fleet/placement.go`
- Modify: `backend/internal/store/postgres.go`
- Modify: `backend/cmd/server/main.go`
- Modify: `backend/internal/api/enrollment.go`
- Modify: `backend/internal/fleet/placement_test.go`

Checklist
- [ ] Stop defaulting placement to `ExpectedImage` for runtime version selection.
- [ ] Replace query filter with compatibility/capability checks.
- [ ] Keep legacy path behind compatibility flag where required.
- [ ] Update tests asserting image filter behavior.

Validation
- [ ] `cd backend && go test ./internal/fleet/...`
- [ ] `cd backend && go test ./internal/store/... -run EligibleHosts`

---

### DR-303: Preflight API Errors for Incompatible Requests
Scope: explicit user-facing failures before VM start.

Files
- Modify: `backend/internal/api/machines.go`
- Modify: `backend/internal/machines/runtime.go`
- Modify: `backend/internal/api/machines_create_region_test.go`

Checklist
- [ ] Reject incompatible requested versions with clear 409/412 error shape.
- [ ] Return candidate versions/hosts where possible.
- [ ] Ensure no partial placement state on preflight rejection.

Validation
- [ ] `cd backend && go test ./internal/api/... -run CreateMachine`

## Phase 4 — PTY Decoupling

### DR-401: Introduce Dedicated Guest PTY Runtime
Scope: guest PTY service no longer depends on `agent --pty-server`.

Files
- Create: `backend/cmd/ocmptyd/main.go`
- Modify: `backend/cmd/agent/ptyserver.go` (extract reusable package or migration shim)
- Modify: `scripts/build-rootfs.sh`
- Modify: `scripts/init-openclaw.sh`

Checklist
- [ ] Build/install `ocmptyd` binary for guest runtime.
- [ ] Start `ocmptyd` from init script instead of `agent --pty-server`.
- [ ] Keep websocket protocol compatibility for existing frontend terminal.
- [ ] Keep compatibility shim during transition.

Validation
- [ ] PTY `/health` and websocket terminal flows pass.

---

### DR-402: PTY Artifact Versioning and Health Rollback
Scope: independent PTY release state.

Files
- Modify: `backend/internal/agentapi/healthprobe.go`
- Modify: `backend/internal/api/agent_heartbeat.go`
- Modify: `backend/internal/store/store.go`
- Modify: `backend/internal/store/postgres.go`
- Create: PTY release state tests

Checklist
- [ ] Add PTY version/reporting fields where needed.
- [ ] Add PTY health gate for upgrade completion.
- [ ] Implement rollback pointer behavior on PTY health failure.

Validation
- [ ] `cd backend && go test ./internal/agentapi/...`
- [ ] `cd backend && go test ./internal/api/... -run Heartbeat`

## Phase 5 — Supervisor vs Control-Agent Split

### DR-501: Extract VM Supervisor Process
Scope: Firecracker lifecycle process separated from control API lifecycle.

Files
- Create: `backend/cmd/vm-supervisor/main.go`
- Modify: `backend/internal/orchestrator/*` (IPC or RPC-facing boundary)
- Modify: `backend/cmd/agent/main.go`
- Create: `backend/internal/supervisorclient/*`

Checklist
- [ ] Move VM lifecycle ownership into supervisor process.
- [ ] Define minimal RPC/IPC contract for create/stop/list/state.
- [ ] Keep persistence/recovery semantics equivalent.
- [ ] Add migration path from single-process agent mode.

Validation
- [ ] Integration test: supervisor restart behavior and state continuity.

---

### DR-502: Make Control-Agent Restart Non-Disruptive
Scope: control API updates without VM stop.

Files
- Modify: `backend/internal/agentapi/server.go`
- Modify: `backend/internal/agentapi/handlers_update.go`
- Modify: `backend/cmd/agent/main.go`
- Modify: `backend/internal/reconciler/host.go`

Checklist
- [ ] Remove unconditional VM shutdown from control-agent restart path.
- [ ] Keep explicit maintenance flow only for supervisor updates.
- [ ] Update host status transitions for split lifecycle model.

Validation
- [ ] E2E: trigger control-agent update while machines stay running.

## Cross-Phase Operational Tickets

### DR-901: Feature Flag Plumbing ⚠️ PARTIAL
Files
- Modify: `backend/internal/config/config.go` → **Done** (`EnableRuntimeVersionResolver` added)
- Modify: `backend/cmd/server/main.go` → **Done** (wired into both worker and main server RuntimeConfig)
- Modify: `backend/cmd/agent/main.go` → **Not done** (no agent-side flags yet)

Checklist
- [x] Add and document phase flags. *(`FF_RUNTIME_VERSION_RESOLVER` implemented; other phase flags like `FF_OPENCLAW_ARTIFACT_RUNTIME` are expected to come in later phases)*
- [x] Ensure safe defaults (off by default in production).

---

### DR-902: Observability and Audit Fields ❌ NOT STARTED
Files
- Modify: `backend/internal/events/*`
- Modify: `backend/internal/api/activity.go`
- Modify: `backend/migrations/*` (if new audit columns required)

Checklist
- [ ] Log chosen release ids, runtime source, fallback, rollback reason.
- [ ] Add metrics for upgrade success/failure and fallback rate.

---

### DR-903: Canary + Rollback Automation
Files
- Modify: deployment scripts/workflows in `.github/workflows/*` and/or `scripts/*`
- Modify: rollout docs in `docs/`

Checklist
- [ ] Add canary promotion gate tied to E2E results.
- [ ] Add automated rollback trigger thresholds.
- [ ] Add runbook with manual override commands.

## Suggested Epic Mapping
- Epic A: `DR-001` to `DR-005`
- Epic B: `DR-101` to `DR-105`
- Epic C: `DR-201`
- Epic D: `DR-301` to `DR-303`
- Epic E: `DR-401` to `DR-402`
- Epic F: `DR-501` to `DR-502`
- Epic G: `DR-901` to `DR-903`
