# Managed Extensions Control-Plane Plan

## Status

Proposed.

This plan implements the Phase 1 product direction described in `docs/configuration_architecture.md`:

- skills and plugins are installed from the control plane
- installs are templated and pinned
- sandboxing and related safety policy are platform-owned
- the VM is not authoritative for extension lifecycle
- approval workflows are explicitly deferred to Phase 2

## Goal

Make OpenClaw Machines behave like a managed runtime for extensions and policy:

- OCM owns the extension catalog
- OCM owns desired extension state per machine
- the VM reconciles that desired state onto persistent disk
- OpenClaw runs approved extensions but does not self-author extension lifecycle

## Non-Goals

Phase 1 does **not** include:

- Telegram or other admin approval flows
- generic config write-back from the VM
- user-authored arbitrary plugin installs
- full support for self-extending machines
- replacing all current OpenClaw mutation surfaces at once

**Constraint:** Remote access (Cloudflare tunnels, device pairing, future iOS app) must continue working. Remote access paths must not bypass the managed extension model — they are transport, not extension-authoring surfaces.

## Product Contract

Phase 1 should make these rules explicit:

1. Skills/plugins/tools that affect executable capability must come from the control plane.
2. Sandboxing and related safety policy are enabled and configured by the control plane.
3. The machine Gateway is a runtime endpoint, not the source of truth for extension lifecycle.
4. Local runtime state on `/data` remains local unless OCM explicitly models it.

## Target Architecture

```text
Catalog / Templates (OCM DB)
    ->
Desired state per machine (OCM DB)
    ->
Metadata / desired-state endpoint
    ->
VM reconciler installs approved artifacts to /data
    ->
Config assembly renders runtime config for approved state
    ->
OpenClaw Gateway runs approved extensions under platform policy
```

## Workstreams

### 1. Catalog and Data Model

Add first-class managed extension data structures in the control plane.

#### Deliverables

- registry support for managed `skill`, `plugin`, and optionally `tool` entries
- install template metadata per entry
- pinned version and integrity metadata
- runtime config template metadata
- optional slot metadata, such as `memory`
- optional sandbox/profile metadata
- machine desired-state records for enabled extensions
- machine install status records for actual installed versions and health

#### Notes

Install templates must be constrained and deterministic. Avoid arbitrary shell from DB.

Phase 1 supports two installer kinds:

- **bundled** — extension is part of the rootfs or platform image
- **file copy** — extension artifact is downloaded from GCS and placed on `/data`

Future installer kinds (npm package, uv tool, go install, package archive) are deferred until Phase 1 extensions are proven. All installs must be pinned by version and integrity.

### 2. Config Assembly Ownership

Extend config assembly so extension-related config is no longer authored locally in the VM.

#### Deliverables

- render approved extension config from OCM desired state
- render plugin allowlists/entries from approved state
- render exclusive slot selections such as `plugins.slots.memory`
- render sandbox defaults and platform safety policy
- keep channel credentials and other secret injection as control-plane owned concerns

#### Acceptance Criteria

- a machine with no approved extensions gets only platform defaults and bundled platform-owned behavior
- a machine with approved extensions receives all extension config from assembly
- local VM-side extension config changes are ignored or overwritten by the next reconcile/refresh

### 3. VM Reconciler / Installer

Add a reconciler in the VM that installs approved artifacts to persistent storage before the Gateway is considered ready.

#### Deliverables

- desired-state fetch from metadata/control-plane endpoint
- install root on `/data`, separate from ephemeral rootfs
- deterministic install flow from pinned template metadata
- status reporting back to OCM
- reinstall on missing/drifted artifacts
- rollback on install failure: revert to last-known-good artifact and report degraded status

#### DBOS integration

Extension installs should be modeled as DBOS workflows (on the `artifact-install` queue). This gives:

- crash-safe execution — if the worker is preempted mid-install, another worker resumes
- step-level retry — transient GCS download failures retry automatically
- status tracking — `workflow_runs` provides install progress visible in the admin UI
- audit trail — `workflow_events` records each install phase

The install workflow would be: fetch desired state → download artifact → verify integrity → place on `/data` → report status.

#### Rollback triggers

Rollback to last-known-good occurs when:

- artifact integrity check fails (checksum mismatch)
- the Gateway fails its health check within 60s of an extension install
- the reconciler detects a drifted artifact that cannot be restored

Rollback is automatic for the first two cases. Manual rollback is available via the admin UI for all cases.

#### Acceptance Criteria

- if a machine restarts, approved extensions are present without manual reinstall
- if the rootfs changes, the reconciler restores approved artifacts from desired state
- if an install fails, the machine rolls back to last-known-good and reports degraded status clearly

### 4. Gateway and VM Enforcement

Prevent the VM from becoming the authority for extension lifecycle again.

#### Phase 1

Use the least invasive enforcement that works:

- remove or hide extension mutation surfaces from the OCM UI
- block conflicting gateway methods at the proxy/API layer where feasible
- do not expose local plugin/skill installation as a supported workflow

#### Phase 1.5

If soft enforcement is insufficient, add a small managed-mode patch or upstream feature so the Gateway can reject:

- `skills.install`
- `skills.update`
- plugin install/update flows
- slot mutation from local config UI

#### Acceptance Criteria

- an end user cannot accidentally create durable extension state that OCM does not know about
- the supported path for extension lifecycle is always the control plane

### 5. Memory Architecture Integration

Handle the hybrid memory model explicitly.

#### Deliverables

- pin `plugins.slots.memory` to `memory-core` by default
- support curated alternate memory plugins later, such as `memory-lancedb`
- keep `memory.*` config rendered by OCM templates/policy
- expose memory slot choice and health in the admin UI

#### Acceptance Criteria

- memory tools remain available with the default bundled memory plugin
- switching to an alternate memory plugin is a control-plane decision, not a VM-local install
- memory-related config stays coherent across restart and upgrade

### 6. Sandboxing and Policy

Make sandboxing a platform-owned default.

#### Deliverables

- default-on sandbox policy in platform config assembly
- template-level policy exceptions for approved extensions only
- clear separation between normal tools and elevated/break-glass paths
- auditable mapping from approved extension to granted policy exceptions

#### Acceptance Criteria

- sandboxing is enabled by default on managed machines
- users cannot weaken sandbox posture from inside the VM
- any extension-specific exceptions are traceable to approved templates

### 7. Admin UI

Provide the control-plane surfaces needed to operate the model.

#### Deliverables

- extension catalog view
- extension template detail/edit view
- machine enable/disable controls for approved extensions
- machine install status and health
- memory slot selection if alternate memory plugins are supported
- clear indication that approval flow is not yet part of Phase 1

#### Acceptance Criteria

- an admin can fully manage extension lifecycle without shelling into the VM
- machine status surfaces show desired state, actual state, and drift/failure clearly

### 8. Testing and Rollout

Add tests that reflect the new ownership model.

#### Tests

**Unit tests** (`make test-go`, ~20s):
- config assembly tests for managed extension rendering
- sandbox policy tests for default-on behavior
- RBAC tests for extension management endpoints (owner/admin only)

**Workflow integration tests** (`make test-workflows`, ~5min, requires Postgres):
- installer workflow: enqueue → download → verify → place → report status
- installer retry on transient GCS failure
- installer rollback on integrity check failure

**Gateway E2E tests** (`make test-gateway-e2e`, ~12s):
- pairing regression tests, especially channel vs node pairing
- extension config rendered correctly in assembled config

**Integration tests** (`make test-integration`, ~35min, requires Firecracker):
- restart/persistence tests: install extension → restart VM → verify extension present
- rootfs change tests: update rootfs → restart → reconciler restores extensions
- failure-mode tests: partial install → rollback → degraded status reported

#### Rollout

1. land data model and catalog changes behind feature flags
2. land VM reconciler with status reporting but no hard enforcement
3. switch selected test machines to control-plane-managed installs
4. remove or block conflicting user-facing mutation paths
5. make managed mode the default

## Sequencing

### Phase 0: Product Lock

Decide and document:

- managed machine is the Phase 1 product
- approval flow is deferred
- self-extending machine behavior is unsupported in Phase 1

### Phase 1A: Foundations

- data model
- catalog/templates
- desired-state and status endpoints
- config assembly changes

### Phase 1B: Reconcile and Operate

- VM installer/reconciler
- install status reporting
- admin UI
- memory slot and sandbox policy integration

### Phase 1C: Enforce

- remove/hide conflicting UI paths
- proxy/API-layer blocking where possible
- optional managed-mode patch if soft enforcement proves insufficient

### Phase 2: Approval Flow

Deferred by design.

Possible future additions:

- mutation intents from OpenClaw to OCM
- admin approval over Telegram or other channels
- policy-driven auto-approval rules
- audit and RBAC around requested changes

## Risks

### 1. Half-managed state

If local mutation surfaces remain usable, users will continue creating unsupported local state and the platform will drift back into split-brain behavior.

### 2. Overly flexible templates

If install templates are too open-ended, the control plane becomes a remote-code-execution engine rather than a curated extension catalog.

### 3. Memory complexity

Memory is not fully plugin-isolated yet. OCM must own both plugin slot selection and top-level `memory.*` config until upstream simplifies that boundary.

### 4. Fork pressure

Hard enforcement may require a small OpenClaw patch or upstream managed-mode support. Keep that patch minimal and narrowly scoped.

## Decisions

1. **Agents are control-plane-managed in Phase 1.** Agents are the most common user-authored entity. If agents remain VM-local, users will continue creating durable state that OCM doesn't own — the same split-brain problem this plan aims to fix. Agent CRUD (create, update, delete, per-agent model overrides) must be modeled in OCM and rendered through config assembly.

## Open Questions

1. Should bundled extensions be represented explicitly in the catalog, or treated as implicit install templates?
2. How should extension secrets be modeled: in template schema, separate credential references, or both?
3. What status granularity does the UI need: desired/installing/installed/failed/drifted?
4. Is soft enforcement sufficient, or should managed mode be hard-enforced from the start?

## Success Criteria

Phase 1 is successful when:

- OCM can install and configure approved skills/plugins without using VM-local self-service flows
- restart and upgrade preserve approved extension behavior deterministically
- sandboxing and extension policy are control-plane-owned
- the machine no longer appears self-extending in unsupported ways
- users can still use approved extensions normally, but the platform remains authoritative
