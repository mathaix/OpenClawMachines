# Per-VM Artifact Version Tracking

**Date:** 2026-04-08
**Status:** Proposed
**Scope:** Rootfs lifecycle follow-up from `decoupleStage3`
**Depends on:** PTY decoupling (Phase 4) and agent reattach (Phase 5) already shipped

## Problem

Each VM already boots with a resolved runtime selection, but that per-machine version identity is not surfaced through the agent API or heartbeat. This creates three practical problems:

1. The control plane cannot ask the agent which rootfs and OpenClaw version a specific running VM actually booted with.
2. Mixed-version hosts are hard to reason about because host heartbeat only reports host-staged versions, not per-VM booted versions.
3. Targeted recreate-based upgrades and rollbacks need an "actual running version" signal per machine, not just desired or host-staged versions.

## Current State

### What the database tracks

- `desired_rootfs_version`, `desired_openclaw_version`, `desired_channel`
- `resolved_rootfs_version`, `resolved_openclaw_version`
- `version_source`

This is control-plane intent and last resolved selection. It is not a direct read of the currently running VM on the host.

### What the agent already knows

- `VMConfig.RuntimeSelection` contains `ResolvedRootfsVersion` and `ResolvedOpenClawVersion` at create time.
- `persistedMetadataConfig.RuntimeSelection` already persists that resolved selection through restart and recovery.
- `Recover()` already restores `RuntimeSelection` into metadata re-registration.

So the per-machine resolved version context is not lost. It is just not exposed on `VMInstance` or agent heartbeat.

### What host heartbeat currently reports

- `rootfs_version`, `browser_rootfs_version`, `openclaw_version`

These come from cached manifest sidecars in `StateDir`. They describe what is staged on the host, not what every VM on the host is running.

## Decision

Use `RuntimeSelection` as the source of truth for per-VM artifact identity, and expose the resolved versions on `VMInstance` and later on heartbeat.

This started as a read-side change, but the branch now also includes the first recreate-based upgrade path. The agent reports the per-machine version selection it already uses at boot, the backend stores actual running versions, and targeted rootfs/OpenClaw changes can recreate a VM in place while preserving its data volume.

## Source Of Truth

### Per-VM booted versions

Per-machine version identity comes from `RuntimeSelection`:

- `RootfsVersion` <- `RuntimeSelection.ResolvedRootfsVersion`
- `OpenClawVersion` <- `RuntimeSelection.ResolvedOpenClawVersion`

These values are selected by the control plane and passed into `Create()`. They are already persisted as part of `persistedMetadataConfig`.

### Host-staged versions

Host heartbeat should continue to use cached manifest sidecars for:

- host `rootfs_version`
- host `browser_rootfs_version`
- host `openclaw_version`

Those fields answer a different question: "what artifacts are currently staged on this host?"

The key distinction is:

- host-staged version: current artifact on the host cache
- per-VM actual version: resolved version this VM booted with

## What This Solves

This design solves the missing per-VM visibility layer:

1. `List` and `Get` can report what a running VM actually booted with.
2. Mixed-version hosts become visible through agent APIs and later heartbeat.
3. The backend can compare desired vs actual per machine and identify upgrade candidates.
4. Rollback targeting becomes precise because affected machines can be identified directly.
5. Reattached VMs retain explicit per-machine version identity after agent restart.

## What This Does Not Solve

This design still does not:

- change version resolution
- make rootfs selection per-VM by itself
- allow in-place mutation of running VM artifacts

Recreate-based upgrade flows are now implemented for the existing rootfs/OpenClaw machine controls, including Linux integration coverage for upgrade success and restore-on-failure. Per-VM browser artifact tracking remains a separate change.

## Artifact Model

For the main VM, track:

| Field | Meaning | Source |
|-------|---------|--------|
| `RootfsVersion` | Rootfs version this VM was resolved to boot with | `RuntimeSelection.ResolvedRootfsVersion` |
| `OpenClawVersion` | OpenClaw version this VM was resolved to boot with | `RuntimeSelection.ResolvedOpenClawVersion` |

`ocmptyd` is intentionally not tracked as an independent field in Phase 1 because it is baked into the rootfs and versioned with it.

Browser companion artifact tracking is explicitly deferred. Host heartbeat already exposes host-staged `browser_rootfs_version`, but per-machine browser version reporting is not part of this phase.

## Data Model

### Add to `VMInstance`

```go
type VMInstance struct {
    // ...existing fields...

    RootfsVersion   string `json:"rootfs_version,omitempty"`
    OpenClawVersion string `json:"openclaw_version,omitempty"`
}
```

### Do not add duplicate persisted fields in Phase 1

Do not add `RootfsVersion` or `OpenClawVersion` to `persistedVM` yet.

Reason:

- `persistedMetadataConfig.RuntimeSelection` already persists the resolved version data.
- Adding a second persisted copy creates drift risk with no clear benefit.

Instead:

- populate `VMInstance` from `cfg.RuntimeSelection` in `Create()`
- repopulate `VMInstance` from `p.MetaCfg.RuntimeSelection` in `Recover()`

## Implementation

### Phase 1: Surface Existing Per-VM Version State

1. Add `RootfsVersion` and `OpenClawVersion` to `VMInstance`.
2. In `Create()`, set:
   - `vm.instance.RootfsVersion = cfg.RuntimeSelection.ResolvedRootfsVersion`
   - `vm.instance.OpenClawVersion = cfg.RuntimeSelection.ResolvedOpenClawVersion`
3. In `Recover()`, set:
   - `vm.instance.RootfsVersion = p.MetaCfg.RuntimeSelection.ResolvedRootfsVersion`
   - `vm.instance.OpenClawVersion = p.MetaCfg.RuntimeSelection.ResolvedOpenClawVersion`
4. Expose both fields through existing List/Get agent API JSON.

### Phase 2: Report To Backend

5. Add optional `vm_versions` to heartbeat payload using in-memory `VMInstance` values.
6. Backend stores `actual_rootfs_version` and `actual_openclaw_version` per machine.
7. Backend reconciles:
   - desired version
   - resolved version
   - actual running version

### Phase 3: Surface In UI

8. Show actual running version alongside desired/resolved version.
9. Flag machines where actual != desired as upgrade candidates.
10. Show mixed-version distribution across hosts and fleet.

## Upgrade Semantics

This design is intentionally paired with recreate-based upgrade semantics.

An upgrade should mean:

1. Resolve new target versions for the machine.
2. Compare target vs actual running version.
3. If different, stop the VM.
4. Create a replacement VM with the new resolved versions.
5. Reattach the existing data volume.
6. Rebuild metadata and proxy state.
7. Report the new actual versions.

This is not an in-place rootfs mutation.

### What Changes During Upgrade

- VM PID
- VM rootfs copy
- TAP device
- socket path
- actual running version fields

### What Stays The Same

- machine identity
- machine record
- preserved data volume
- user workspace and guest state stored on the data volume

## Config And Data Preservation

Upgrade should reuse the existing data volume and should not reprovision guest config on top of an already-initialized machine.

The current data-volume bootstrap path already supports this model:

- first boot writes `openclaw.json`, auth profiles, soul files, and nonce state
- later boots with the same data volume detect existing config and skip rewriting those files
- runtime pointer state and boot metadata can still be refreshed

So the intended upgrade behavior is:

1. reuse the existing data volume
2. run the normal pre-boot data-volume preparation
3. detect existing config on the volume
4. preserve guest config instead of overwriting it

That means upgrade is "replace VM, preserve data volume", not "run first-boot provisioning again."

If control-plane-driven guest config mutation is needed later, that should be an explicit config migration path, not an accidental side effect of upgrade.

## Future Work

### Per-VM Rootfs Selection

This document assumes per-machine resolved version selection exists in `RuntimeSelection`, but the host create path must still fully honor that selection. Longer term, the VM create flow should ensure that each VM boots from the rootfs version resolved for that machine, not just whatever rootfs is currently staged globally.

### Browser Companion Tracking

If browser companion VMs participate in upgrade, add per-machine browser artifact identity as a separate field rather than overloading main-VM rootfs tracking.

### Guest Capability Reporting

Later, guests can report capability sets so the backend can reason about feature support independently from version strings.

## Checklist

### Phase 1: Surface Existing State

- [x] Add `RootfsVersion` and `OpenClawVersion` to `VMInstance`.
- [x] Set both fields from `RuntimeSelection` in `Create()`.
- [x] Restore both fields from persisted `RuntimeSelection` in `Recover()`.
- [x] Expose both fields in List/Get API responses.
- [x] Unit test: `VMInstance` version fields are restored on recovery.
- [x] Integration-style API test: List/Get returns version fields for a running VM.

### Phase 2: Backend Reporting

- [x] Add `vm_versions` map to heartbeat payload.
- [x] Backend: add `actual_rootfs_version` and `actual_openclaw_version` columns.
- [x] Backend: reconcile heartbeat `vm_versions` with machine records.
- [x] Backend: expose actual versions in machine APIs.

### Phase 3: UI

- [x] Frontend: show actual running version in machine detail.
- [x] Frontend: flag machines where actual != desired as "upgrade available".

### Upgrade Flow

- [x] Define `POST /vms/{id}/upgrade` agent API.
- [x] Implement recreate-on-upgrade with data-volume preservation.
- [x] Ensure upgrade does not rewrite existing guest config on reused data volumes.
- [x] Backend: orchestrate targeted upgrades and rollback.
- [x] Frontend: expose recreate-based OpenClaw and rootfs controls in the runtime panel.
- [x] Backend/Frontend: expose rootfs release listing for runtime selection.
- [x] Integration test: recreate-based rootfs/OpenClaw upgrade preserves data volume and guest config.
- [x] Integration test: recreate failure restores the previous VM and leaves the machine recoverable.
- [ ] Add browser companion artifact tracking if needed.
