# Disk Config as Source of Truth

**Date:** 2026-04-08
**Status:** Proposed
**Scope:** OpenClaw gateway config lifecycle in native config mode.

## Decision

The OpenClaw config file on the VM data volume is the source of truth after first boot.

The OCM backend can seed, patch, validate, audit, and cache config, but it should not treat `machine_config.assembled_config` or a freshly assembled DB-derived config as canonical once a VM has an on-disk `openclaw.json`.

This supersedes older backend-as-source-of-truth assumptions in the native config lifecycle docs. Those assumptions fit the earlier metadata-config model, but they conflict with the current native file-backed model where OpenClaw and users can mutate config on disk.

## Current State

After init completes, the boot path points at disk-owned config:

```text
/data/ocm/configs/<timestamp>/
  openclaw.json
  auth-profiles.json
  device.json

/home/openclaw/.openclaw/config-current -> /data/ocm/configs/<timestamp>/
/home/openclaw/.openclaw/openclaw.json -> config-current/openclaw.json
```

The backend pre-boot write stages first-boot files through `config-current/`, and `scripts/init-openclaw.sh` migrates that staged directory into `/data/ocm/configs/<timestamp>/`. Existing volumes that still have a direct `~/.openclaw/openclaw.json` file are migrated into the config directory and replaced with the symlink above. Reboots preserve the active config directory and only create a new version when needed.

The mismatch is in live update logic. Some backend handlers still do this:

```text
DB state
  -> AssembleConfig()
  -> diff against previous DB assembled_config snapshot
  -> openclaw config set/unset
  -> store assembled_config as new snapshot
```

That makes the DB snapshot act like the source of truth. It can clobber user edits or runtime edits that exist only on disk.

## Target Model

### First Boot: Seed

First boot is the only full assembly path.

```text
DB inputs + platform defaults
  -> AssembleSeedConfig()
  -> write openclaw.json to /data/ocm/configs/<timestamp>/
  -> start gateway from disk config
```

If `openclaw.json` already exists on the data volume, init should preserve it. Reboots should not reassemble from DB.

### Live Updates: Patch

After first boot, backend actions should emit narrow config operations for the domain they own.

```text
user action
  -> validate authorization and request shape
  -> update DB intent/cache if needed
  -> read current on-disk config when an operation needs current state
  -> openclaw config set/unset targeted paths
  -> optionally read back or patch DB snapshot as cache/audit
```

The diff base is the on-disk config, not `machine_config.assembled_config`.

### DB Role

The DB stores:

- Account and machine authorization state.
- Catalogs and platform defaults.
- Preboot intent, such as enabled plugins or preferred model before the VM exists.
- Secrets and credential metadata.
- Audit records.
- Optional `last_known_config` or `last_applied_config_snapshot`.

The DB does not own the full post-boot config document.

If the existing `machine_config.assembled_config` column remains, treat it as cache/audit. Do not use it as the canonical desired state for live diffs.

## Managed Domains

Backend-owned changes should be path scoped:

| Domain | Allowed live ops |
| --- | --- |
| Providers | `set/unset models.providers.<provider>` |
| Default model | `set agents.defaults.model.primary` |
| Model catalog | `set agents.defaults.models` |
| Plugins | `set/unset plugins.entries.<pluginID>` |
| Plugin allow list | `set plugins.allow` after reading current disk state |
| Skills | `set/unset skills.allowBundled` |
| Browser | `set/unset browser` |
| Identity | `set/unset ui.assistant` |
| Channels | Channel state machine owns `channels.<channelID>`, `plugins.entries.<channelID>`, and `plugins.allow` updates for channel plugins |

No unrelated endpoint should produce ops outside its domain.

## Plugin Update Rules

Plugin enable:

```text
set plugins.entries.<pluginID> <entry JSON>
read current plugins.allow from disk
set plugins.allow union(current, pluginID)
```

Plugin disable:

```text
unset plugins.entries.<pluginID>
read current plugins.allow from disk
set plugins.allow current - pluginID
```

Plugin override update:

```text
set plugins.entries.<pluginID>.config <override JSON>
```

Plugin overrides must be scoped to that plugin's config object. They must not be allowed to write top-level `gateway`, `models`, `agents`, sibling `plugins.*`, or arbitrary paths.

## Full Config Push

A full config push should mean "write this user config to disk," not "reassemble from DB."

Valid flow:

```text
submitted config JSON
  -> schema validation
  -> protected-key validation
  -> atomic disk write or OpenClaw config apply
  -> optional DB snapshot update
```

Invalid flow:

```text
button press
  -> AssembleConfig() from DB
  -> overwrite disk-owned user config
```

## Artifact and Runtime Boundary

Runtime artifact selection and config are separate concerns.

OpenClaw artifact layout should be manifest-driven. Update/readiness code should not hard-code paths like `dist/extensions`. It should read the artifact manifest, for example `bundled_plugins_relpath`, so config lifecycle and runtime delivery do not drift.

## Implementation Shape

Recommended changes:

1. Rename or document `machine_config.assembled_config` as a snapshot/cache field.
2. Add a way for the backend to read current config or targeted paths from the running VM.
3. Replace live `AssembleConfig -> diff DB snapshot -> ConfigBatch` with domain-specific op builders.
4. Update plugin enable/disable/override handlers to push scoped config ops for bundled plugins.
5. Add `plugins.allow` handling based on current disk config.
6. Keep `AssembleSeedConfig` as the full preboot assembly path.
7. Add tests proving user-owned config survives unrelated model/plugin/channel updates.

## Architecture Change Size

This is a medium architecture change, not a full rewrite.

It does not require changing the rootfs persistence model. The rootfs already has the right primitive: versioned config directories on `/data`.

It does not require changing OpenClaw artifact delivery. Artifact manifests should be cleaned up separately, but disk config ownership is independent of runtime mount delivery.

The main work is in backend config update semantics:

- Replace full reassembly as a live update mechanism.
- Introduce disk-aware read/patch operations.
- Make each config domain own its own state transitions.
- Reframe DB assembled config as cache/audit.
- Add regression coverage around preserving VM-local edits.

Expected effort:

- Small if only plugin fixes are done: 1-2 focused PRs.
- Medium if model, browser, identity, skills, and plugin updates are all moved to scoped patch flows: several PRs.
- Larger only if the product requires full bidirectional sync between the VM Control UI, CLI edits, and the dashboard. That would need a write-back or sync service design.

## Open Questions

- Should the backend read current disk config through `openclaw config get`, direct file read, or a dedicated agent endpoint?
- Should `machine_config.assembled_config` be renamed now, or should code comments and API response naming change first?
- Should full config push remain a user-facing feature, or be limited to admin/debug workflows?
- Which paths are protected from user full-config writes in disk-owned mode?
