# Plugin Catalog & Managed Extensions Data Model

## Status

Proposed — Phase 1A of the managed extensions plan.

## Goal

Add a plugin catalog and per-machine plugin state to the control plane, so OCM can manage plugin lifecycle (enable, disable, version tracking, config assembly) without relying on in-VM mutation.

## Context

OCM already has `registry_entries` (channels, skills, tools) and `machine_capabilities` (per-machine enable/disable with config overrides). Plugins are architecturally different from skills:

- Plugins occupy **exclusive slots** (e.g. only one memory plugin active at a time)
- Plugins have **install artifacts** (bundled in rootfs or downloaded from GCS)
- Plugins need **version tracking** and **install status** per machine

This design adds plugins as a separate catalog (`plugin_catalog`) with dedicated per-machine state (`machine_plugins`), rather than overloading the existing `registry_entries` table.

## Scope

Phase 1A is **bundled plugins only**. The `install_kind` column and `file-copy` artifacts exist in the schema for forward-compatibility, but the API layer **rejects** `EnableMachinePlugin` for any plugin where `install_kind != 'bundled'` until the VM-side reconciler ships in Phase 1B. This prevents creating desired state the runtime cannot realize.

## Non-Goals

- VM-side reconciler / installer (Phase 1B)
- Gateway enforcement of managed-mode (Phase 1C)
- Admin UI for plugin management (separate spec)
- Approval workflows (Phase 2)
- `file-copy` plugin enablement (Phase 1B — schema present, API gated)

## Data Model

### `plugin_catalog` table

Stores the global catalog of available plugins. Managed by platform admins.

```sql
CREATE TABLE IF NOT EXISTS plugin_catalog (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    description     TEXT,
    slot            TEXT NOT NULL,
    version         TEXT NOT NULL DEFAULT '1',
    install_kind    TEXT NOT NULL DEFAULT 'bundled',
    artifact_url    TEXT,
    artifact_sha256 TEXT,
    config_template JSONB,
    status          TEXT NOT NULL DEFAULT 'active',
    sort_order      INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_plugin_catalog_slot ON plugin_catalog(slot);
```

**Fields:**

| Field | Type | Description |
|-------|------|-------------|
| `id` | `TEXT PK` | Slug, e.g. `"memory-core"` |
| `name` | `TEXT` | Display name |
| `description` | `TEXT` | Human-readable description |
| `slot` | `TEXT` | Exclusive slot name, e.g. `"memory"` |
| `version` | `TEXT` | Current catalog version (global — all machines follow this) |
| `install_kind` | `TEXT` | `"bundled"` (in rootfs) or `"file-copy"` (from GCS) |
| `artifact_url` | `TEXT` | GCS URL for `file-copy` installs (nullable for bundled) |
| `artifact_sha256` | `TEXT` | SHA256 hash for `file-copy` integrity (nullable for bundled) |
| `config_template` | `JSONB` | JSON fragment merged into assembled config |
| `status` | `TEXT` | `"active"` or `"deprecated"` |
| `sort_order` | `INT` | Display ordering |

### `machine_plugins` table

Tracks which plugins are enabled per machine and their install status.

```sql
CREATE TABLE IF NOT EXISTS machine_plugins (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id        UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    plugin_id         TEXT NOT NULL REFERENCES plugin_catalog(id) ON DELETE RESTRICT,
    slot              TEXT NOT NULL,
    enabled           BOOLEAN NOT NULL DEFAULT true,
    config_overrides  JSONB,
    install_status    TEXT NOT NULL DEFAULT 'pending',
    installed_version TEXT,
    installed_at      TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(machine_id, plugin_id)
);

CREATE INDEX IF NOT EXISTS idx_machine_plugins_machine_id ON machine_plugins(machine_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_machine_plugins_slot_exclusive ON machine_plugins(machine_id, slot) WHERE enabled = true;
```

**Fields:**

| Field | Type | Description |
|-------|------|-------------|
| `machine_id` | `UUID FK` | References `machines(id)` |
| `plugin_id` | `TEXT FK` | References `plugin_catalog(id)` |
| `slot` | `TEXT` | Denormalized from `plugin_catalog.slot` — set on insert, used by partial unique index for slot exclusivity |
| `enabled` | `BOOLEAN` | Whether this plugin is active |
| `config_overrides` | `JSONB` | Per-machine config overrides merged on top of template |
| `install_status` | `TEXT` | `pending`, `installing`, `installed`, `failed`, `drifted` |
| `installed_version` | `TEXT` | Actual version on the VM |
| `installed_at` | `TIMESTAMPTZ` | When last successfully installed |

### Install status lifecycle

```
pending → installing → installed
                    ↘ failed
installed → drifted (when catalog version != installed_version)
```

For `bundled` plugins, `EnableMachinePlugin` sets `install_status = 'installed'` and `installed_version` to the catalog version immediately, since the artifact is already part of the rootfs. The `drifted` status detection is deferred to Phase 1B (reconciler).

### Slot exclusivity

Enforced via a partial unique index plus application logic:

```sql
CREATE UNIQUE INDEX IF NOT EXISTS idx_machine_plugins_slot_exclusive
    ON machine_plugins (machine_id, (SELECT slot FROM plugin_catalog WHERE id = plugin_id))
    WHERE enabled = true;
```

**Note:** Postgres does not support subqueries in index expressions. Instead, add a `slot` column to `machine_plugins` (denormalized, set on insert from the catalog) and use:

```sql
CREATE UNIQUE INDEX IF NOT EXISTS idx_machine_plugins_slot_exclusive
    ON machine_plugins (machine_id, slot) WHERE enabled = true;
```

This guarantees at most one enabled plugin per slot per machine, even under concurrent transactions. The application logic in `EnableMachinePlugin` still disables prior plugins in the same slot within the transaction (belt-and-suspenders), but the unique index is the authoritative constraint.

### Seed data

Seed uses `ON CONFLICT DO NOTHING` for idempotent migrations:

```sql
INSERT INTO plugin_catalog (id, name, description, slot, version, install_kind, config_template)
VALUES (
    'memory-core',
    'Memory Core',
    'Default bundled memory plugin — conversation memory and context management',
    'memory',
    '1',
    'bundled',
    '{"plugins": {"slots": {"memory": "memory-core"}}}'
) ON CONFLICT (id) DO NOTHING;
```

**Convention:** Plugin `config_template` values SHOULD use the `"plugins"` top-level namespace to avoid colliding with other config sections. Deep-merge handles multiple plugins correctly (maps merge recursively).

### Version updates

Phase 1 uses global versioning: update the `plugin_catalog.version` row and all machines follow on next reconcile/boot. Per-machine version pinning is deferred.

**Bundled version caveat:** For `bundled` plugins, the actual artifact bits are delivered by the host rootfs. The catalog `version` field is a logical version that tracks the *intended* state, not necessarily what's on-disk. The `installed_version` in `machine_plugins` is set to the catalog version at enable-time. True version verification (comparing catalog version vs rootfs contents) is deferred to the Phase 1B reconciler, which will flip `install_status` to `drifted` on mismatch.

## Config Assembly

### New assembly step

Added after agents (step 7) and before `skills.allowBundled` (step 9):

1. Load enabled `machine_plugins` for the machine, joined with `plugin_catalog`
2. For each enabled plugin:
   - Deep-merge the catalog's `config_template` into the assembled config
   - Deep-merge the machine's `config_overrides` on top (if any)
3. If no plugins are enabled, emit no `plugins` section (gateway uses its own defaults)

### AssemblyParams addition

```go
type PluginSelection struct {
    PluginID        string
    Slot            string
    ConfigTemplate  map[string]interface{}
    ConfigOverrides map[string]interface{}
}

// Added to AssemblyParams:
Plugins []PluginSelection
```

### Protected prefix

Add `"plugins"` to both `ProtectedConfigKeys` (map) and `protectedPrefixes` (slice) so capability config overrides and user overrides cannot clobber plugin config.

### Backward compatibility

Machines with no `machine_plugins` rows get no `plugins` section in assembled config. The gateway continues using its defaults (`memory-core` via `OPENCLAW_BUNDLED_PLUGINS_DIR`). No behavior change for existing machines.

### Example assembled output

With `memory-core` enabled:

```json
{
  "plugins": {
    "slots": {
      "memory": "memory-core"
    }
  }
}
```

## Store Interface

### PluginCatalogRepo

```go
type PluginCatalogRepo interface {
    ListPluginCatalog(ctx context.Context) ([]PluginCatalogEntry, error)
    GetPluginCatalogEntry(ctx context.Context, id string) (*PluginCatalogEntry, error)
    CreatePluginCatalogEntry(ctx context.Context, entry *PluginCatalogEntry) error
    UpdatePluginCatalogEntry(ctx context.Context, entry *PluginCatalogEntry) error
    DeletePluginCatalogEntry(ctx context.Context, id string) error
}
```

### MachinePluginRepo

```go
type MachinePluginRepo interface {
    ListMachinePlugins(ctx context.Context, machineID string) ([]MachinePlugin, error)
    EnableMachinePlugin(ctx context.Context, machineID, pluginID string, overrides json.RawMessage) error
    DisableMachinePlugin(ctx context.Context, machineID, pluginID string) error
    UpdateMachinePluginOverrides(ctx context.Context, machineID, pluginID string, overrides json.RawMessage) error
    UpdateMachinePluginStatus(ctx context.Context, machineID, pluginID, status, version string) error
}
```

### Slot exclusivity in EnableMachinePlugin

```
BEGIN TRANSACTION
  1. Look up plugin_catalog entry to get slot
  2. UPDATE machine_plugins SET enabled = false, updated_at = NOW() WHERE machine_id = $1 AND enabled = true AND plugin_id IN (SELECT id FROM plugin_catalog WHERE slot = $2) AND plugin_id != $3
  3. INSERT INTO machine_plugins (..., slot, install_status, installed_version) VALUES (..., catalog_slot, CASE WHEN install_kind = 'bundled' THEN 'installed' ELSE 'pending' END, CASE WHEN install_kind = 'bundled' THEN catalog_version ELSE NULL END) ON CONFLICT (machine_id, plugin_id) DO UPDATE SET enabled = true, config_overrides = $4, install_status = CASE WHEN install_kind = 'bundled' THEN 'installed' ELSE 'pending' END, installed_version = CASE WHEN install_kind = 'bundled' THEN catalog_version ELSE NULL END, installed_at = CASE WHEN install_kind = 'bundled' THEN NOW() ELSE NULL END, updated_at = NOW()
COMMIT
```

## API Endpoints

### Plugin catalog (admin-only)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/admin/plugins` | List all catalog entries |
| `POST` | `/api/admin/plugins` | Create catalog entry |
| `PUT` | `/api/admin/plugins/{pluginId}` | Update catalog entry |
| `DELETE` | `/api/admin/plugins/{pluginId}` | Delete catalog entry (see deletion semantics below) |

### Catalog deletion semantics

`machine_plugins.plugin_id` uses `ON DELETE RESTRICT`, so a catalog entry cannot be deleted while any machine has a row referencing it (enabled or disabled). The intended workflow for removing a plugin from the catalog:

1. Set `plugin_catalog.status = 'deprecated'` — hides from new enable operations
2. Admin disables the plugin on all machines (bulk disable endpoint, or per-machine)
3. Disabled `machine_plugins` rows referencing the plugin are cleaned up (DELETE where `enabled = false AND plugin_id = $1`)
4. Now `DELETE FROM plugin_catalog WHERE id = $1` succeeds

The `DELETE /api/admin/plugins/{pluginId}` handler performs steps 3-4 atomically: it deletes all disabled `machine_plugins` rows for the plugin, then deletes the catalog entry. If any machine still has the plugin **enabled**, the request returns `409 Conflict` with the list of affected machines.

### Plugin change lifecycle

Plugin enable/disable/override changes **persist desired state only**. They do NOT auto-push config to running VMs. Changes take effect:

1. **On next VM boot** — config assembly reads `machine_plugins` and includes plugin config
2. **On explicit config push** — `POST /api/accounts/{accountId}/machines/{id}/config/push` re-assembles and pushes config including current plugin state

This matches the existing config pipeline behavior where capability and agent changes are also desired-state-first. A future Phase 1C may add automatic config push on plugin state changes.

### Per-machine plugins (owner/admin)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/accounts/{accountId}/machines/{id}/plugins` | List enabled plugins |
| `POST` | `/api/accounts/{accountId}/machines/{id}/plugins` | Enable plugin |
| `PUT` | `/api/accounts/{accountId}/machines/{id}/plugins/{pluginId}` | Update overrides |
| `DELETE` | `/api/accounts/{accountId}/machines/{id}/plugins/{pluginId}` | Disable plugin |

### Agent-auth endpoints (for VM self-config)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/agent/machines/{machineID}/plugins` | List enabled plugins |

## Testing

### Unit tests (`make test-go`)

- `TestAssembleConfig_WithPlugin` — memory-core template merged correctly
- `TestAssembleConfig_NoPlugins_BackwardCompatible` — no plugins → no `plugins` section
- `TestAssembleConfig_PluginOverrides` — per-machine overrides merge on top of template
- `TestAssembleConfig_PluginProtectedPrefix` — capability overrides can't clobber `plugins`
- `TestEnableMachinePlugin_SlotExclusivity` — enabling a plugin disables others in same slot
- `TestEnableMachinePlugin_ConcurrentSlot` — concurrent enables for same slot, only one wins (partial unique index)
- `TestEnableMachinePlugin_RejectsFileCopy` — API returns error for `file-copy` plugins in Phase 1A
- `TestEnableMachinePlugin_ReEnable_ResetsInstallState` — re-enabling resets install_status and installed_version
- `TestDeletePluginCatalog_BlockedByEnabled` — delete returns 409 when machines have plugin enabled
- `TestDeletePluginCatalog_CleansDisabledRows` — delete succeeds after all machines disabled
- `TestCreateMachine_AutoProvisions_MemoryCore` — new machines get memory-core row
- `TestPluginCatalog_CRUD` — basic catalog operations

### Gateway E2E tests (`make test-gateway-e2e`)

- Assembled config with memory plugin validates against gateway schema
- Config push with plugin changes produces correct assembled output

## Migration sequence

1. Migration 037: Create `plugin_catalog` table and seed `memory-core`
2. Migration 038: Create `machine_plugins` table (with `slot` column and partial unique index)

## Decisions

1. **New machines auto-provision `memory-core`.** `CreateMachine` inserts a `machine_plugins` row for `memory-core` (enabled, installed) so OCM owns the default memory slot from the start. This aligns with the managed-extensions plan — the gateway should not be the authority for which plugins are active.

2. **Drifted detection deferred to Phase 1B reconciler.** When the catalog version is updated, `install_status` is NOT automatically flipped. The reconciler (Phase 1B) will compare `installed_version` vs `plugin_catalog.version` and set `drifted` where they diverge.
