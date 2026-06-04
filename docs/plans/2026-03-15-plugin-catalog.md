# Plugin Catalog & Managed Extensions Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a plugin catalog and per-machine plugin state to the control plane, enabling managed plugin lifecycle (enable, disable, version tracking, config assembly) for bundled plugins.

**Architecture:** Two new DB tables (`plugin_catalog`, `machine_plugins`) with store repos, admin + machine + agent-auth API endpoints, and a new config assembly step that deep-merges plugin `config_template` JSONB into assembled config. Slot exclusivity enforced via partial unique index. Phase 1A is bundled-only.

**Tech Stack:** Go 1.25, Chi router, pgx/v5 (raw SQL), PostgreSQL on Neon

**Spec:** `docs/designs/plugin-catalog.md`

---

## File Structure

| File | Action | Responsibility |
|------|--------|---------------|
| `backend/migrations/037_plugin_catalog.sql` | Create | DDL for `plugin_catalog` table + seed `memory-core` |
| `backend/migrations/038_machine_plugins.sql` | Create | DDL for `machine_plugins` table + partial unique index |
| `backend/internal/store/store.go` | Modify | Add `PluginCatalogEntry`, `MachinePlugin` structs + `PluginCatalogRepo`, `MachinePluginRepo` interfaces |
| `backend/internal/store/postgres.go` | Modify | Implement both repos (CRUD + slot exclusivity transaction) |
| `backend/internal/store/postgres_test.go` | Modify | Integration tests for slot exclusivity and deletion semantics |
| `backend/internal/api/plugins.go` | Create | Admin catalog CRUD handlers + per-machine plugin handlers |
| `backend/internal/api/plugins_test.go` | Create | Unit tests for plugin handlers |
| `backend/internal/api/agent_auth.go` | Modify | Add `handleAgentAuthListPlugins` |
| `backend/internal/api/server.go` | Modify | Register plugin routes (admin, machine, agent-auth) |
| `backend/internal/api/machine_config.go` | Modify | Load plugins in `assembleConfigForMachine`, pass to assembly |
| `backend/internal/api/machine_config_test.go` | Modify | Add mock methods + tests for plugin assembly |
| `backend/internal/configassembly/assembler.go` | Modify | Add `PluginSelection` to `AssemblyParams`, new assembly step, protect `"plugins"` key |
| `backend/internal/configassembly/assembler_test.go` | Modify | Tests for plugin merge, no-plugins backward compat, protected prefix |

---

## Chunk 1: Database & Store Layer

### Task 1: Migration — plugin_catalog table

**Files:**
- Create: `backend/migrations/037_plugin_catalog.sql`

- [ ] **Step 1: Create migration file**

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

-- Seed memory-core plugin
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

- [ ] **Step 2: Verify migration applies cleanly**

Run: `make test-go`
Expected: All tests pass (migrations are applied during test setup)

- [ ] **Step 3: Commit**

```bash
git add backend/migrations/037_plugin_catalog.sql
git commit -m "feat: add plugin_catalog table with memory-core seed (migration 037)"
```

---

### Task 2: Migration — machine_plugins table

**Files:**
- Create: `backend/migrations/038_machine_plugins.sql`

- [ ] **Step 1: Create migration file**

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
CREATE UNIQUE INDEX IF NOT EXISTS idx_machine_plugins_slot_exclusive
    ON machine_plugins(machine_id, slot) WHERE enabled = true;
```

- [ ] **Step 2: Verify migration applies cleanly**

Run: `make test-go`
Expected: All tests pass

- [ ] **Step 3: Commit**

```bash
git add backend/migrations/038_machine_plugins.sql
git commit -m "feat: add machine_plugins table with slot exclusivity index (migration 038)"
```

---

### Task 3: Store models and interfaces

**Files:**
- Modify: `backend/internal/store/store.go`

Add structs after `MachineAgent` (around line 268) and interfaces after `MachineAgentRepo` (around line 671).

- [ ] **Step 1: Add sentinel errors and structs**

Add sentinel errors in `backend/internal/store/errors.go` (after existing sentinels):

```go
var ErrPluginNotFound = errors.New("plugin catalog entry not found")
var ErrPluginStillEnabled = errors.New("plugin still enabled on machines")
var ErrMachinePluginNotFound = errors.New("machine plugin not found")
```

Add structs after the `MachineAgent` struct (line ~268) in `store.go`:

```go
// PluginCatalogEntry represents a plugin in the global catalog.
type PluginCatalogEntry struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	Description    *string          `json:"description,omitempty"`
	Slot           string           `json:"slot"`
	Version        string           `json:"version"`
	InstallKind    string           `json:"install_kind"`
	ArtifactURL    *string          `json:"artifact_url,omitempty"`
	ArtifactSHA256 *string          `json:"artifact_sha256,omitempty"`
	ConfigTemplate json.RawMessage  `json:"config_template,omitempty"`
	Status         string           `json:"status"`
	SortOrder      int              `json:"sort_order"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// MachinePlugin tracks which plugin is enabled on a specific machine.
type MachinePlugin struct {
	ID               string          `json:"id"`
	MachineID        string          `json:"machine_id"`
	PluginID         string          `json:"plugin_id"`
	Slot             string          `json:"slot"`
	Enabled          bool            `json:"enabled"`
	ConfigOverrides  json.RawMessage `json:"config_overrides,omitempty"`
	InstallStatus    string          `json:"install_status"`
	InstalledVersion *string         `json:"installed_version,omitempty"`
	InstalledAt      *time.Time      `json:"installed_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}
```

- [ ] **Step 2: Add PluginCatalogRepo interface**

Add after `MachineAgentRepo` (line ~671):

```go
// PluginCatalogRepo handles CRUD for the global plugin catalog.
type PluginCatalogRepo interface {
	ListPluginCatalog(ctx context.Context) ([]PluginCatalogEntry, error)
	GetPluginCatalogEntry(ctx context.Context, id string) (*PluginCatalogEntry, error)
	CreatePluginCatalogEntry(ctx context.Context, entry *PluginCatalogEntry) error
	UpdatePluginCatalogEntry(ctx context.Context, entry *PluginCatalogEntry) error
	DeletePluginCatalogEntry(ctx context.Context, id string) error
}

// MachinePluginRepo handles per-machine plugin state.
type MachinePluginRepo interface {
	ListMachinePlugins(ctx context.Context, machineID string) ([]MachinePlugin, error)
	EnableMachinePlugin(ctx context.Context, machineID, pluginID string, overrides json.RawMessage) error
	DisableMachinePlugin(ctx context.Context, machineID, pluginID string) error
	UpdateMachinePluginOverrides(ctx context.Context, machineID, pluginID string, overrides json.RawMessage) error
	UpdateMachinePluginStatus(ctx context.Context, machineID, pluginID, status, version string) error
}
```

- [ ] **Step 3: Add both repos to the Store interface**

In the `Store` interface (line ~677), add:

```go
	MachineAgentRepo
	PluginCatalogRepo
	MachinePluginRepo
}
```

- [ ] **Step 4: Add `"encoding/json"` to imports if not present**

Check if `json` is already imported in store.go. If not, add `"encoding/json"` to the import block.

- [ ] **Step 5: Verify compilation**

Run: `make test-go`
Expected: Compilation fails — PostgresStore doesn't implement the new interfaces yet. That's expected; Task 4 fixes this.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/store/store.go
git commit -m "feat: add PluginCatalogRepo and MachinePluginRepo interfaces"
```

---

### Task 4: PostgreSQL implementations — plugin catalog CRUD

**Files:**
- Modify: `backend/internal/store/postgres.go`

Add implementations at the end of the file (after MachineAgent methods).

- [ ] **Step 1: Implement ListPluginCatalog**

```go
// ---- Plugin Catalog ----

func (s *PostgresStore) ListPluginCatalog(ctx context.Context) ([]PluginCatalogEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, description, slot, version, install_kind, artifact_url,
		        artifact_sha256, config_template, status, sort_order, created_at, updated_at
		 FROM plugin_catalog
		 ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []PluginCatalogEntry
	for rows.Next() {
		var e PluginCatalogEntry
		if err := rows.Scan(&e.ID, &e.Name, &e.Description, &e.Slot, &e.Version,
			&e.InstallKind, &e.ArtifactURL, &e.ArtifactSHA256, &e.ConfigTemplate,
			&e.Status, &e.SortOrder, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
```

- [ ] **Step 2: Implement GetPluginCatalogEntry**

```go
func (s *PostgresStore) GetPluginCatalogEntry(ctx context.Context, id string) (*PluginCatalogEntry, error) {
	var e PluginCatalogEntry
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, description, slot, version, install_kind, artifact_url,
		        artifact_sha256, config_template, status, sort_order, created_at, updated_at
		 FROM plugin_catalog WHERE id = $1`, id).
		Scan(&e.ID, &e.Name, &e.Description, &e.Slot, &e.Version,
			&e.InstallKind, &e.ArtifactURL, &e.ArtifactSHA256, &e.ConfigTemplate,
			&e.Status, &e.SortOrder, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}
```

- [ ] **Step 3: Implement CreatePluginCatalogEntry**

```go
func (s *PostgresStore) CreatePluginCatalogEntry(ctx context.Context, entry *PluginCatalogEntry) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO plugin_catalog (id, name, description, slot, version, install_kind,
		        artifact_url, artifact_sha256, config_template, status, sort_order)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 RETURNING created_at, updated_at`,
		entry.ID, entry.Name, entry.Description, entry.Slot, entry.Version,
		entry.InstallKind, entry.ArtifactURL, entry.ArtifactSHA256,
		entry.ConfigTemplate, entry.Status, entry.SortOrder,
	).Scan(&entry.CreatedAt, &entry.UpdatedAt)
}
```

- [ ] **Step 4: Implement UpdatePluginCatalogEntry**

```go
func (s *PostgresStore) UpdatePluginCatalogEntry(ctx context.Context, entry *PluginCatalogEntry) error {
	result, err := s.pool.Exec(ctx,
		`UPDATE plugin_catalog
		 SET name = $2, description = $3, slot = $4, version = $5, install_kind = $6,
		     artifact_url = $7, artifact_sha256 = $8, config_template = $9,
		     status = $10, sort_order = $11, updated_at = NOW()
		 WHERE id = $1`,
		entry.ID, entry.Name, entry.Description, entry.Slot, entry.Version,
		entry.InstallKind, entry.ArtifactURL, entry.ArtifactSHA256,
		entry.ConfigTemplate, entry.Status, entry.SortOrder)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrPluginNotFound
	}
	return nil
}
```

- [ ] **Step 5: Implement DeletePluginCatalogEntry**

Per the spec's deletion semantics: clean up disabled machine_plugins rows, then delete catalog entry. Return error if any machine still has it enabled.

```go
func (s *PostgresStore) DeletePluginCatalogEntry(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Check if any machine has this plugin enabled
	var enabledCount int
	err = tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM machine_plugins WHERE plugin_id = $1 AND enabled = true`, id).
		Scan(&enabledCount)
	if err != nil {
		return err
	}
	if enabledCount > 0 {
		return fmt.Errorf("%w: %d machine(s)", ErrPluginStillEnabled, enabledCount)
	}

	// Delete disabled machine_plugins rows
	if _, err := tx.Exec(ctx,
		`DELETE FROM machine_plugins WHERE plugin_id = $1 AND enabled = false`, id); err != nil {
		return err
	}

	// Delete catalog entry
	result, err := tx.Exec(ctx, `DELETE FROM plugin_catalog WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrPluginNotFound
	}

	return tx.Commit(ctx)
}
```

- [ ] **Step 6: Verify compilation**

Run: `make test-go`
Expected: Fails — MachinePluginRepo methods not yet implemented. That's Task 5.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/store/postgres.go
git commit -m "feat: implement PluginCatalogRepo in PostgresStore"
```

---

### Task 5: PostgreSQL implementations — machine plugins with slot exclusivity

**Files:**
- Modify: `backend/internal/store/postgres.go`

- [ ] **Step 1: Implement ListMachinePlugins**

```go
// ---- Machine Plugins ----

func (s *PostgresStore) ListMachinePlugins(ctx context.Context, machineID string) ([]MachinePlugin, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT mp.id, mp.machine_id, mp.plugin_id, mp.slot, mp.enabled,
		        mp.config_overrides, mp.install_status, mp.installed_version,
		        mp.installed_at, mp.created_at, mp.updated_at
		 FROM machine_plugins mp
		 WHERE mp.machine_id = $1
		 ORDER BY mp.slot, mp.created_at`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var plugins []MachinePlugin
	for rows.Next() {
		var p MachinePlugin
		if err := rows.Scan(&p.ID, &p.MachineID, &p.PluginID, &p.Slot, &p.Enabled,
			&p.ConfigOverrides, &p.InstallStatus, &p.InstalledVersion,
			&p.InstalledAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		plugins = append(plugins, p)
	}
	return plugins, rows.Err()
}
```

- [ ] **Step 2: Implement EnableMachinePlugin with slot exclusivity**

This is the core method. It:
1. Looks up the plugin catalog entry (get slot + install_kind + version)
2. Rejects non-bundled plugins (Phase 1A gate)
3. Disables other enabled plugins in the same slot
4. Upserts the target plugin with reset install state

```go
func (s *PostgresStore) EnableMachinePlugin(ctx context.Context, machineID, pluginID string, overrides json.RawMessage) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// 1. Look up catalog entry
	var slot, installKind, catalogVersion string
	err = tx.QueryRow(ctx,
		`SELECT slot, install_kind, version FROM plugin_catalog WHERE id = $1 AND status = 'active'`,
		pluginID).Scan(&slot, &installKind, &catalogVersion)
	if err != nil {
		return fmt.Errorf("plugin not found or inactive: %w", err)
	}

	// 2. Phase 1A gate: reject non-bundled plugins
	if installKind != "bundled" {
		return fmt.Errorf("only bundled plugins can be enabled in Phase 1A (got %s)", installKind)
	}

	// 3. Disable other enabled plugins in the same slot
	if _, err := tx.Exec(ctx,
		`UPDATE machine_plugins SET enabled = false, updated_at = NOW()
		 WHERE machine_id = $1 AND slot = $2 AND enabled = true AND plugin_id != $3`,
		machineID, slot, pluginID); err != nil {
		return err
	}

	// 4. Upsert: insert or re-enable with reset install state
	// Use CASE for install_status so Phase 1B can remove the gate and file-copy gets 'pending'
	installStatus := "pending"
	var installedVersion *string
	if installKind == "bundled" {
		installStatus = "installed"
		installedVersion = &catalogVersion
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO machine_plugins (machine_id, plugin_id, slot, enabled, config_overrides,
		        install_status, installed_version, installed_at)
		 VALUES ($1, $2, $3, true, $4, $5, $6, CASE WHEN $5 = 'installed' THEN NOW() ELSE NULL END)
		 ON CONFLICT (machine_id, plugin_id) DO UPDATE SET
		     enabled = true,
		     config_overrides = $4,
		     install_status = $5,
		     installed_version = $6,
		     installed_at = CASE WHEN $5 = 'installed' THEN NOW() ELSE NULL END,
		     updated_at = NOW()`,
		machineID, pluginID, slot, overrides, installStatus, installedVersion)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}
```

- [ ] **Step 3: Implement DisableMachinePlugin**

```go
func (s *PostgresStore) DisableMachinePlugin(ctx context.Context, machineID, pluginID string) error {
	result, err := s.pool.Exec(ctx,
		`UPDATE machine_plugins SET enabled = false, updated_at = NOW()
		 WHERE machine_id = $1 AND plugin_id = $2 AND enabled = true`,
		machineID, pluginID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("plugin not found or already disabled")
	}
	return nil
}
```

- [ ] **Step 4: Implement UpdateMachinePluginOverrides**

```go
func (s *PostgresStore) UpdateMachinePluginOverrides(ctx context.Context, machineID, pluginID string, overrides json.RawMessage) error {
	result, err := s.pool.Exec(ctx,
		`UPDATE machine_plugins SET config_overrides = $3, updated_at = NOW()
		 WHERE machine_id = $1 AND plugin_id = $2 AND enabled = true`,
		machineID, pluginID, overrides)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("plugin not found or disabled")
	}
	return nil
}
```

- [ ] **Step 5: Implement UpdateMachinePluginStatus**

Used by the Phase 1B reconciler to report install state back to the control plane. Implemented now so the interface is complete.

```go
func (s *PostgresStore) UpdateMachinePluginStatus(ctx context.Context, machineID, pluginID, status, version string) error {
	result, err := s.pool.Exec(ctx,
		`UPDATE machine_plugins
		 SET install_status = $3, installed_version = $4, installed_at = CASE WHEN $3 = 'installed' THEN NOW() ELSE installed_at END, updated_at = NOW()
		 WHERE machine_id = $1 AND plugin_id = $2`,
		machineID, pluginID, status, version)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("machine plugin not found")
	}
	return nil
}
```

- [ ] **Step 6: Verify all tests pass**

Run: `make test-go`
Expected: All existing tests pass. New interfaces are fully implemented.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/store/postgres.go
git commit -m "feat: implement MachinePluginRepo with slot exclusivity transaction"
```

---

### Task 5B: Store integration tests

**Files:**
- Modify: `backend/internal/store/postgres_test.go`

**Note:** These tests require a real Postgres connection. Check `postgres_test.go` for the existing test setup pattern (e.g. `TestMain`, connection string from env). If the project doesn't have store-level integration tests, create them alongside the handler tests instead (in `plugins_test.go` using the mock store). The key tests below validate DB-enforced constraints that mocks can't cover.

- [ ] **Step 1: Write TestPluginCatalog_CRUD**

```go
func TestPluginCatalog_CRUD(t *testing.T) {
	// Skip if no database connection
	ctx := context.Background()
	s := testStore(t) // use existing test setup helper

	entry := &PluginCatalogEntry{
		ID: "test-plugin", Name: "Test", Slot: "test",
		Version: "1", InstallKind: "bundled", Status: "active",
	}

	// Create
	err := s.CreatePluginCatalogEntry(ctx, entry)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Get
	got, err := s.GetPluginCatalogEntry(ctx, "test-plugin")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Test" {
		t.Errorf("name = %q, want Test", got.Name)
	}

	// Update
	got.Name = "Updated"
	if err := s.UpdatePluginCatalogEntry(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}

	// List
	entries, err := s.ListPluginCatalog(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.ID == "test-plugin" && e.Name == "Updated" {
			found = true
		}
	}
	if !found {
		t.Error("updated entry not found in list")
	}

	// Delete
	if err := s.DeletePluginCatalogEntry(ctx, "test-plugin"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}
```

- [ ] **Step 2: Write TestEnableMachinePlugin_SlotExclusivity**

```go
func TestEnableMachinePlugin_SlotExclusivity(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	// Seed two plugins in same slot
	s.CreatePluginCatalogEntry(ctx, &PluginCatalogEntry{
		ID: "mem-a", Name: "A", Slot: "memory", Version: "1", InstallKind: "bundled", Status: "active",
	})
	s.CreatePluginCatalogEntry(ctx, &PluginCatalogEntry{
		ID: "mem-b", Name: "B", Slot: "memory", Version: "1", InstallKind: "bundled", Status: "active",
	})

	machineID := createTestMachine(t, s) // helper that creates a machine and returns ID

	// Enable A
	if err := s.EnableMachinePlugin(ctx, machineID, "mem-a", nil); err != nil {
		t.Fatalf("enable A: %v", err)
	}

	// Enable B — should disable A
	if err := s.EnableMachinePlugin(ctx, machineID, "mem-b", nil); err != nil {
		t.Fatalf("enable B: %v", err)
	}

	plugins, _ := s.ListMachinePlugins(ctx, machineID)
	enabledCount := 0
	for _, p := range plugins {
		if p.Enabled {
			enabledCount++
			if p.PluginID != "mem-b" {
				t.Errorf("expected mem-b enabled, got %s", p.PluginID)
			}
		}
	}
	if enabledCount != 1 {
		t.Errorf("expected 1 enabled plugin, got %d", enabledCount)
	}
}
```

- [ ] **Step 3: Write TestEnableMachinePlugin_RejectsFileCopy**

```go
func TestEnableMachinePlugin_RejectsFileCopy(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	s.CreatePluginCatalogEntry(ctx, &PluginCatalogEntry{
		ID: "ext-plugin", Name: "External", Slot: "ext", Version: "1",
		InstallKind: "file-copy", Status: "active",
	})

	machineID := createTestMachine(t, s)
	err := s.EnableMachinePlugin(ctx, machineID, "ext-plugin", nil)
	if err == nil {
		t.Fatal("expected error for file-copy plugin, got nil")
	}
	if !strings.Contains(err.Error(), "bundled") {
		t.Errorf("error should mention bundled, got: %v", err)
	}
}
```

- [ ] **Step 4: Write TestEnableMachinePlugin_ReEnable_ResetsInstallState**

```go
func TestEnableMachinePlugin_ReEnable_ResetsInstallState(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	s.CreatePluginCatalogEntry(ctx, &PluginCatalogEntry{
		ID: "mem-re", Name: "Memory", Slot: "memory", Version: "2", InstallKind: "bundled", Status: "active",
	})

	machineID := createTestMachine(t, s)

	// Enable
	s.EnableMachinePlugin(ctx, machineID, "mem-re", nil)

	// Simulate version drift
	s.UpdateMachinePluginStatus(ctx, machineID, "mem-re", "drifted", "1")

	// Re-enable — should reset to installed with version 2
	s.EnableMachinePlugin(ctx, machineID, "mem-re", nil)

	plugins, _ := s.ListMachinePlugins(ctx, machineID)
	for _, p := range plugins {
		if p.PluginID == "mem-re" {
			if p.InstallStatus != "installed" {
				t.Errorf("install_status = %q, want installed", p.InstallStatus)
			}
			if p.InstalledVersion == nil || *p.InstalledVersion != "2" {
				t.Errorf("installed_version = %v, want 2", p.InstalledVersion)
			}
		}
	}
}
```

- [ ] **Step 5: Write TestDeletePluginCatalog_BlockedByEnabled**

```go
func TestDeletePluginCatalog_BlockedByEnabled(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	s.CreatePluginCatalogEntry(ctx, &PluginCatalogEntry{
		ID: "mem-del", Name: "Memory", Slot: "memory", Version: "1", InstallKind: "bundled", Status: "active",
	})

	machineID := createTestMachine(t, s)
	s.EnableMachinePlugin(ctx, machineID, "mem-del", nil)

	err := s.DeletePluginCatalogEntry(ctx, "mem-del")
	if err == nil {
		t.Fatal("expected error deleting plugin with enabled machines")
	}
	if !errors.Is(err, ErrPluginStillEnabled) {
		t.Errorf("expected ErrPluginStillEnabled, got: %v", err)
	}
}
```

- [ ] **Step 6: Write TestDeletePluginCatalog_CleansDisabledRows**

```go
func TestDeletePluginCatalog_CleansDisabledRows(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	s.CreatePluginCatalogEntry(ctx, &PluginCatalogEntry{
		ID: "mem-clean", Name: "Memory", Slot: "memory", Version: "1", InstallKind: "bundled", Status: "active",
	})

	machineID := createTestMachine(t, s)
	s.EnableMachinePlugin(ctx, machineID, "mem-clean", nil)
	s.DisableMachinePlugin(ctx, machineID, "mem-clean")

	// Should succeed — plugin disabled, rows cleaned up by delete
	if err := s.DeletePluginCatalogEntry(ctx, "mem-clean"); err != nil {
		t.Fatalf("delete should succeed after disable: %v", err)
	}
}
```

- [ ] **Step 7: Run tests**

Run: `make test-go`
Expected: All store integration tests pass.

- [ ] **Step 8: Commit**

```bash
git add backend/internal/store/postgres_test.go
git commit -m "test: add store integration tests for plugin catalog and slot exclusivity"
```

---

## Chunk 2: Config Assembly

### Task 6: Add plugin support to config assembler

**Files:**
- Modify: `backend/internal/configassembly/assembler.go`

- [ ] **Step 1: Add "plugins" to ProtectedConfigKeys and protectedPrefixes**

In `ProtectedConfigKeys` map (line ~92), add:

```go
	"plugins": true,
```

In `protectedPrefixes` slice (line ~105), add:

```go
	"plugins.",
```

- [ ] **Step 2: Add PluginSelection struct and Plugins field to AssemblyParams**

Add after `AgentDefinition` struct (line ~221):

```go
// PluginSelection represents an enabled plugin for config assembly.
type PluginSelection struct {
	PluginID        string
	Slot            string
	ConfigTemplate  map[string]interface{}
	ConfigOverrides map[string]interface{}
}
```

Add to `AssemblyParams` (line ~235, after `Agents`):

```go
	Plugins []PluginSelection // optional enabled plugins
```

- [ ] **Step 3: Add plugin assembly step in AssembleConfig**

In `AssembleConfig` function, add **after agents (step 7) and before `skills.allowBundled` (step 9)**, as the spec requires. Find the agents step (around line ~406) and insert the plugin step before the `skills.allowBundled` block. This ordering ensures plugins are merged before skills assembly, and the `plugins` key is protected from capability overrides since it's in `ProtectedConfigKeys`:

```go
	// Plugin config: deep-merge each enabled plugin's template, then overrides
	for _, p := range params.Plugins {
		if p.ConfigTemplate != nil {
			result = deepMerge(result, p.ConfigTemplate)
		}
		if p.ConfigOverrides != nil {
			result = deepMerge(result, p.ConfigOverrides)
		}
	}
```

- [ ] **Step 4: Verify compilation**

Run: `make test-go`
Expected: All tests pass (existing tests don't set Plugins, so the new step is a no-op for them).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/configassembly/assembler.go
git commit -m "feat: add plugin assembly step and protect plugins config key"
```

---

### Task 7: Config assembly tests for plugins

**Files:**
- Modify: `backend/internal/configassembly/assembler_test.go`

- [ ] **Step 1: Write TestAssembleConfig_WithPlugin**

```go
func TestAssembleConfig_WithPlugin(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		MachineID: "m-1",
		Plugins: []PluginSelection{
			{
				PluginID: "memory-core",
				Slot:     "memory",
				ConfigTemplate: map[string]interface{}{
					"plugins": map[string]interface{}{
						"slots": map[string]interface{}{
							"memory": "memory-core",
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)
	plugins, ok := getNestedMap(m, "plugins")
	if !ok {
		t.Fatal("missing plugins section")
	}
	slots, ok := getNestedMap(plugins, "slots")
	if !ok {
		t.Fatal("missing plugins.slots")
	}
	if slots["memory"] != "memory-core" {
		t.Errorf("plugins.slots.memory = %v, want memory-core", slots["memory"])
	}
}
```

- [ ] **Step 2: Write TestAssembleConfig_NoPlugins_BackwardCompatible**

```go
func TestAssembleConfig_NoPlugins_BackwardCompatible(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		MachineID: "m-1",
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)
	if _, ok := m["plugins"]; ok {
		t.Error("plugins section should not exist when no plugins are enabled")
	}
}
```

- [ ] **Step 3: Write TestAssembleConfig_PluginOverrides**

```go
func TestAssembleConfig_PluginOverrides(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		MachineID: "m-1",
		Plugins: []PluginSelection{
			{
				PluginID: "memory-core",
				Slot:     "memory",
				ConfigTemplate: map[string]interface{}{
					"plugins": map[string]interface{}{
						"slots": map[string]interface{}{
							"memory": "memory-core",
						},
					},
				},
				ConfigOverrides: map[string]interface{}{
					"plugins": map[string]interface{}{
						"memoryConfig": map[string]interface{}{
							"maxTokens": float64(4096),
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)
	plugins, ok := getNestedMap(m, "plugins")
	if !ok {
		t.Fatal("missing plugins section")
	}
	// Template value preserved
	slots, ok := getNestedMap(plugins, "slots")
	if !ok {
		t.Fatal("missing plugins.slots")
	}
	if slots["memory"] != "memory-core" {
		t.Errorf("plugins.slots.memory = %v, want memory-core", slots["memory"])
	}
	// Override value merged
	mc, ok := getNestedMap(plugins, "memoryConfig")
	if !ok {
		t.Fatal("missing plugins.memoryConfig from overrides")
	}
	if mc["maxTokens"] != float64(4096) {
		t.Errorf("plugins.memoryConfig.maxTokens = %v, want 4096", mc["maxTokens"])
	}
}
```

- [ ] **Step 4: Write TestAssembleConfig_PluginProtectedPrefix**

Verify that capability config overrides cannot clobber the `plugins` key:

```go
func TestAssembleConfig_PluginProtectedPrefix(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		MachineID: "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "some-channel",
				EntryType: "channel",
				ConfigOverrides: map[string]interface{}{
					"plugins": map[string]interface{}{
						"slots": map[string]interface{}{
							"memory": "evil-plugin",
						},
					},
				},
			},
		},
		Plugins: []PluginSelection{
			{
				PluginID: "memory-core",
				Slot:     "memory",
				ConfigTemplate: map[string]interface{}{
					"plugins": map[string]interface{}{
						"slots": map[string]interface{}{
							"memory": "memory-core",
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)
	plugins, ok := getNestedMap(m, "plugins")
	if !ok {
		t.Fatal("missing plugins section")
	}
	slots, ok := getNestedMap(plugins, "slots")
	if !ok {
		t.Fatal("missing plugins.slots")
	}
	// Capability override should have been stripped; plugin template wins
	if slots["memory"] != "memory-core" {
		t.Errorf("plugins.slots.memory = %v, want memory-core (capability override should be stripped)", slots["memory"])
	}
}
```

- [ ] **Step 5: Run tests**

Run: `make test-go`
Expected: All 4 new tests pass, plus all existing tests still pass.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/configassembly/assembler_test.go
git commit -m "test: add plugin config assembly tests"
```

---

## Chunk 3: API Layer

### Task 8: Plugin catalog admin handlers

**Files:**
- Create: `backend/internal/api/plugins.go`

- [ ] **Step 1: Create file with imports and list handler**

```go
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// ---- Admin Plugin Catalog Handlers ----

func (s *Server) handleListPluginCatalog(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.ListPluginCatalog(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list plugins")
		return
	}
	if entries == nil {
		entries = []store.PluginCatalogEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}
```

- [ ] **Step 2: Add create handler**

```go
func (s *Server) handleCreatePluginCatalogEntry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID             string          `json:"id"`
		Name           string          `json:"name"`
		Description    *string         `json:"description,omitempty"`
		Slot           string          `json:"slot"`
		Version        string          `json:"version"`
		InstallKind    string          `json:"install_kind"`
		ArtifactURL    *string         `json:"artifact_url,omitempty"`
		ArtifactSHA256 *string         `json:"artifact_sha256,omitempty"`
		ConfigTemplate json.RawMessage `json:"config_template,omitempty"`
		Status         string          `json:"status"`
		SortOrder      int             `json:"sort_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ID == "" || req.Name == "" || req.Slot == "" {
		writeError(w, http.StatusBadRequest, "id, name, and slot are required")
		return
	}
	if req.InstallKind == "" {
		req.InstallKind = "bundled"
	}
	if req.Status == "" {
		req.Status = "active"
	}
	if req.Version == "" {
		req.Version = "1"
	}

	entry := &store.PluginCatalogEntry{
		ID:             req.ID,
		Name:           req.Name,
		Description:    req.Description,
		Slot:           req.Slot,
		Version:        req.Version,
		InstallKind:    req.InstallKind,
		ArtifactURL:    req.ArtifactURL,
		ArtifactSHA256: req.ArtifactSHA256,
		ConfigTemplate: req.ConfigTemplate,
		Status:         req.Status,
		SortOrder:      req.SortOrder,
	}

	if err := s.store.CreatePluginCatalogEntry(r.Context(), entry); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "plugin with this ID already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create plugin")
		return
	}

	slog.Info("plugin.catalog.created", "id", entry.ID, "slot", entry.Slot)
	writeJSON(w, http.StatusCreated, entry)
}
```

- [ ] **Step 3: Add update handler**

```go
func (s *Server) handleUpdatePluginCatalogEntry(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "pluginId")

	existing, err := s.store.GetPluginCatalogEntry(r.Context(), pluginID)
	if err != nil {
		writeError(w, http.StatusNotFound, "plugin not found")
		return
	}

	var req struct {
		Name           *string         `json:"name,omitempty"`
		Description    *string         `json:"description,omitempty"`
		Slot           *string         `json:"slot,omitempty"`
		Version        *string         `json:"version,omitempty"`
		InstallKind    *string         `json:"install_kind,omitempty"`
		ArtifactURL    *string         `json:"artifact_url,omitempty"`
		ArtifactSHA256 *string         `json:"artifact_sha256,omitempty"`
		ConfigTemplate json.RawMessage `json:"config_template,omitempty"`
		Status         *string         `json:"status,omitempty"`
		SortOrder      *int            `json:"sort_order,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.Description != nil {
		existing.Description = req.Description
	}
	if req.Slot != nil {
		existing.Slot = *req.Slot
	}
	if req.Version != nil {
		existing.Version = *req.Version
	}
	if req.InstallKind != nil {
		existing.InstallKind = *req.InstallKind
	}
	if req.ArtifactURL != nil {
		existing.ArtifactURL = req.ArtifactURL
	}
	if req.ArtifactSHA256 != nil {
		existing.ArtifactSHA256 = req.ArtifactSHA256
	}
	if req.ConfigTemplate != nil {
		existing.ConfigTemplate = req.ConfigTemplate
	}
	if req.Status != nil {
		existing.Status = *req.Status
	}
	if req.SortOrder != nil {
		existing.SortOrder = *req.SortOrder
	}

	if err := s.store.UpdatePluginCatalogEntry(r.Context(), existing); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update plugin")
		return
	}

	slog.Info("plugin.catalog.updated", "id", pluginID)
	writeJSON(w, http.StatusOK, existing)
}
```

- [ ] **Step 4: Add delete handler**

```go
func (s *Server) handleDeletePluginCatalogEntry(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "pluginId")

	if err := s.store.DeletePluginCatalogEntry(r.Context(), pluginID); err != nil {
		if errors.Is(err, store.ErrPluginNotFound) {
			writeError(w, http.StatusNotFound, "plugin not found")
			return
		}
		if errors.Is(err, store.ErrPluginStillEnabled) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete plugin")
		return
	}

	slog.Info("plugin.catalog.deleted", "id", pluginID)
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 5: Verify compilation**

Run: `go build ./backend/...`
Expected: Compiles (handlers exist but aren't registered yet).

- [ ] **Step 6: Commit**

```bash
git add backend/internal/api/plugins.go
git commit -m "feat: add admin plugin catalog CRUD handlers"
```

---

### Task 9: Per-machine plugin handlers

**Files:**
- Modify: `backend/internal/api/plugins.go`

- [ ] **Step 1: Add list machine plugins handler**

```go
// ---- Per-Machine Plugin Handlers ----

func (s *Server) handleListMachinePlugins(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	plugins, err := s.store.ListMachinePlugins(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list plugins")
		return
	}
	if plugins == nil {
		plugins = []store.MachinePlugin{}
	}
	writeJSON(w, http.StatusOK, plugins)
}
```

- [ ] **Step 2: Add enable machine plugin handler**

```go
func (s *Server) handleEnableMachinePlugin(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	var req struct {
		PluginID        string          `json:"plugin_id"`
		ConfigOverrides json.RawMessage `json:"config_overrides,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.PluginID == "" {
		writeError(w, http.StatusBadRequest, "plugin_id is required")
		return
	}

	if err := s.store.EnableMachinePlugin(r.Context(), machineID, req.PluginID, req.ConfigOverrides); err != nil {
		slog.Error("machine.plugin.enable.failed", "machine_id", machineID, "plugin_id", req.PluginID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	slog.Info("machine.plugin.enabled", "machine_id", machineID, "plugin_id", req.PluginID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
```

- [ ] **Step 3: Add update overrides handler**

```go
func (s *Server) handleUpdateMachinePluginOverrides(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	pluginID := chi.URLParam(r, "pluginId")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	var req struct {
		ConfigOverrides json.RawMessage `json:"config_overrides"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.store.UpdateMachinePluginOverrides(r.Context(), machineID, pluginID, req.ConfigOverrides); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	slog.Info("machine.plugin.overrides_updated", "machine_id", machineID, "plugin_id", pluginID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
```

- [ ] **Step 4: Add disable handler**

```go
func (s *Server) handleDisableMachinePlugin(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	pluginID := chi.URLParam(r, "pluginId")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	if err := s.store.DisableMachinePlugin(r.Context(), machineID, pluginID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	slog.Info("machine.plugin.disabled", "machine_id", machineID, "plugin_id", pluginID)
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/plugins.go
git commit -m "feat: add per-machine plugin enable/disable/override handlers"
```

---

### Task 10: Agent-auth plugin endpoint

**Files:**
- Modify: `backend/internal/api/agent_auth.go`

- [ ] **Step 1: Add handleAgentAuthListPlugins**

Follow the existing `handleAgentAuthListAgents` pattern (same auth + placement validation):

```go
func (s *Server) handleAgentAuthListPlugins(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}

	machineID := chi.URLParam(r, "machineID")
	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	plugins, err := s.store.ListMachinePlugins(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if plugins == nil {
		plugins = []store.MachinePlugin{}
	}
	writeJSON(w, http.StatusOK, plugins)
}
```

- [ ] **Step 2: Commit**

```bash
git add backend/internal/api/agent_auth.go
git commit -m "feat: add agent-auth endpoint for listing machine plugins"
```

---

### Task 11: Route registration

**Files:**
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Register admin plugin catalog routes**

Inside the admin route group (after line ~418, before the closing `}`):

```go
			// Plugin catalog (admin CRUD)
			r.Get("/plugins", srv.handleListPluginCatalog)
			r.Post("/plugins", srv.handleCreatePluginCatalogEntry)
			r.Put("/plugins/{pluginId}", srv.handleUpdatePluginCatalogEntry)
			r.Delete("/plugins/{pluginId}", srv.handleDeletePluginCatalogEntry)
```

- [ ] **Step 2: Register per-machine plugin routes**

Follow the agents pattern: GET list is in the general block (any account member), mutating operations in the owner/admin group.

Add list in the general block (after `r.Get("/agents/{agentId}", ...)` around line ~367):

```go
					r.Get("/plugins", srv.handleListMachinePlugins)
```

Add mutating operations in the owner/admin group (after `r.Delete("/agents/{agentId}", ...)` around line ~387):

```go
						r.Post("/plugins", srv.handleEnableMachinePlugin)
						r.Put("/plugins/{pluginId}", srv.handleUpdateMachinePluginOverrides)
						r.Delete("/plugins/{pluginId}", srv.handleDisableMachinePlugin)
```

- [ ] **Step 3: Register agent-auth plugin route**

After line ~439 (agent-auth machine operations):

```go
	r.Get("/api/agent/machines/{machineID}/plugins", srv.handleAgentAuthListPlugins)
```

- [ ] **Step 4: Verify compilation**

Run: `make test-go`
Expected: All tests pass.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/server.go
git commit -m "feat: register plugin catalog and machine plugin routes"
```

---

## Chunk 4: Config Assembly Integration & Auto-Provisioning

### Task 12: Load plugins in config assembly

**Files:**
- Modify: `backend/internal/api/machine_config.go`

- [ ] **Step 1: Add plugin loading after agent loading**

In `assembleConfigForMachine` (after the agent loading block around line ~377, before `params := ...`):

```go
	// Load enabled plugins for this machine
	var pluginSelections []configassembly.PluginSelection
	machinePlugins, err := s.store.ListMachinePlugins(ctx, machineID)
	if err != nil {
		slog.Warn("machine.config.plugins_load_failed", "machine_id", machineID, "error", err)
	}
	for _, mp := range machinePlugins {
		if !mp.Enabled {
			continue
		}
		catalogEntry, err := s.store.GetPluginCatalogEntry(ctx, mp.PluginID)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("plugin %s: catalog entry not found", mp.PluginID))
			continue
		}

		ps := configassembly.PluginSelection{
			PluginID: mp.PluginID,
			Slot:     mp.Slot,
		}

		if catalogEntry.ConfigTemplate != nil {
			var tmpl map[string]interface{}
			if json.Unmarshal(catalogEntry.ConfigTemplate, &tmpl) == nil {
				ps.ConfigTemplate = tmpl
			}
		}
		if mp.ConfigOverrides != nil {
			var overrides map[string]interface{}
			if json.Unmarshal(mp.ConfigOverrides, &overrides) == nil {
				ps.ConfigOverrides = overrides
			}
		}

		pluginSelections = append(pluginSelections, ps)
	}
```

- [ ] **Step 2: Add Plugins to AssemblyParams**

In the `params := configassembly.AssemblyParams{...}` block (line ~380), add:

```go
		Plugins:         pluginSelections,
```

- [ ] **Step 3: Verify tests pass**

Run: `make test-go`
Expected: All tests pass. Existing mock doesn't need ListMachinePlugins since the embedded `store.Store` will panic only if called — but the mock needs the method. See next step.

- [ ] **Step 4: Add mock methods to mockConfigStore**

In `backend/internal/api/machine_config_test.go`, add to the `mockConfigStore` struct:

```go
	plugins       map[string][]store.MachinePlugin // machineID -> plugins
	pluginCatalog map[string]*store.PluginCatalogEntry
```

In `newMockConfigStore()`, add:

```go
		plugins:       make(map[string][]store.MachinePlugin),
		pluginCatalog: make(map[string]*store.PluginCatalogEntry),
```

Add mock methods:

```go
func (m *mockConfigStore) ListMachinePlugins(_ context.Context, machineID string) ([]store.MachinePlugin, error) {
	return m.plugins[machineID], nil
}

func (m *mockConfigStore) GetPluginCatalogEntry(_ context.Context, id string) (*store.PluginCatalogEntry, error) {
	if e, ok := m.pluginCatalog[id]; ok {
		return e, nil
	}
	return nil, fmt.Errorf("plugin %s not found", id)
}
```

- [ ] **Step 5: Run tests**

Run: `make test-go`
Expected: All tests pass.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/api/machine_config.go backend/internal/api/machine_config_test.go
git commit -m "feat: load machine plugins in config assembly pipeline"
```

---

### Task 13: Config assembly integration test

**Files:**
- Modify: `backend/internal/api/machine_config_test.go`

- [ ] **Step 1: Write TestPushConfig_WithPlugin**

```go
func TestPushConfig_WithPlugin(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	// Add memory-core plugin to catalog and enable on machine
	ms.pluginCatalog["memory-core"] = &store.PluginCatalogEntry{
		ID:             "memory-core",
		Slot:           "memory",
		Version:        "1",
		InstallKind:    "bundled",
		ConfigTemplate: json.RawMessage(`{"plugins":{"slots":{"memory":"memory-core"}}}`),
	}
	ms.plugins["m-1"] = []store.MachinePlugin{
		{
			PluginID:      "memory-core",
			MachineID:     "m-1",
			Slot:          "memory",
			Enabled:       true,
			InstallStatus: "installed",
		},
	}

	srv := newTestConfigServer(ms)

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/m-1/config/push", "m-1", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify assembled config contains plugins section
	assembled := ms.assembledConfigs["m-1"]
	var config map[string]interface{}
	if err := json.Unmarshal(assembled, &config); err != nil {
		t.Fatalf("unmarshal assembled config: %v", err)
	}

	plugins, ok := config["plugins"].(map[string]interface{})
	if !ok {
		t.Fatal("assembled config missing plugins section")
	}
	slots, ok := plugins["slots"].(map[string]interface{})
	if !ok {
		t.Fatal("assembled config missing plugins.slots")
	}
	if slots["memory"] != "memory-core" {
		t.Errorf("plugins.slots.memory = %v, want memory-core", slots["memory"])
	}
}
```

- [ ] **Step 2: Write TestPushConfig_NoPlugins_NoPluginsSection**

```go
func TestPushConfig_NoPlugins_NoPluginsSection(t *testing.T) {
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Status: "stopped"}
	ms.accounts[1] = &store.Account{ID: 1, Slug: "test"}

	srv := newTestConfigServer(ms)

	w := httptest.NewRecorder()
	r := configRequest("POST", "/api/accounts/1/machines/m-1/config/push", "m-1", 1)
	srv.handlePushMachineConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	assembled := ms.assembledConfigs["m-1"]
	var config map[string]interface{}
	if err := json.Unmarshal(assembled, &config); err != nil {
		t.Fatalf("unmarshal assembled config: %v", err)
	}

	if _, ok := config["plugins"]; ok {
		t.Error("assembled config should not have plugins section when no plugins enabled")
	}
}
```

- [ ] **Step 3: Run tests**

Run: `make test-go`
Expected: All tests pass.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/machine_config_test.go
git commit -m "test: add config assembly integration tests for plugins"
```

---

### Task 14: Auto-provision memory-core on machine creation

**Files:**
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Add memory-core auto-provisioning after browser capability**

In `handleCreateMachine` (after the browser capability auto-enable, around line ~863):

```go
	// Auto-enable memory-core plugin for all new machines
	if err := s.store.EnableMachinePlugin(r.Context(), machine.ID, "memory-core", nil); err != nil {
		slog.Error("machine.create.memory_plugin.failed", "machine_id", machine.ID, "error", err)
	}
```

- [ ] **Step 2: Run tests**

Run: `make test-go`
Expected: All tests pass. (The mock store in creation tests needs `EnableMachinePlugin` — check if tests use the real store or mocks. If they use mocks, add the method stub.)

- [ ] **Step 3: Commit**

```bash
git add backend/internal/api/server.go
git commit -m "feat: auto-provision memory-core plugin on machine creation"
```

---

### Task 15: Add mock stubs to other test files

**Files:**
- Modify: `backend/internal/machines/runtime_test.go` (if it has a mock store that now needs the new methods)

- [ ] **Step 1: Check which test files have mock stores that embed store.Store**

Run: `grep -rn "store.Store" backend/internal/ --include="*_test.go"`

For each mock store found, add no-op stubs for the new interface methods:

```go
func (m *mockStore) ListPluginCatalog(_ context.Context) ([]store.PluginCatalogEntry, error) {
	return nil, nil
}
func (m *mockStore) GetPluginCatalogEntry(_ context.Context, _ string) (*store.PluginCatalogEntry, error) {
	return nil, fmt.Errorf("not found")
}
func (m *mockStore) CreatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockStore) UpdatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockStore) DeletePluginCatalogEntry(_ context.Context, _ string) error {
	return nil
}
func (m *mockStore) ListMachinePlugins(_ context.Context, _ string) ([]store.MachinePlugin, error) {
	return nil, nil
}
func (m *mockStore) EnableMachinePlugin(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockStore) DisableMachinePlugin(_ context.Context, _, _ string) error {
	return nil
}
func (m *mockStore) UpdateMachinePluginOverrides(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockStore) UpdateMachinePluginStatus(_ context.Context, _, _, _, _ string) error {
	return nil
}
```

- [ ] **Step 2: Run full test suite**

Run: `make test-go`
Expected: All tests pass with no compilation errors.

- [ ] **Step 3: Commit**

```bash
git add backend/internal/
git commit -m "fix: add plugin repo mock stubs to all test files"
```

---

### Task 16: Plugin handler unit tests

**Files:**
- Create: `backend/internal/api/plugins_test.go`

- [ ] **Step 1: Create test file with mock and helpers**

```go
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type mockPluginStore struct {
	store.Store

	catalog  map[string]*store.PluginCatalogEntry
	machines map[string]*store.Machine
	plugins  map[string][]store.MachinePlugin // machineID -> plugins
}

func newMockPluginStore() *mockPluginStore {
	return &mockPluginStore{
		catalog:  make(map[string]*store.PluginCatalogEntry),
		machines: make(map[string]*store.Machine),
		plugins:  make(map[string][]store.MachinePlugin),
	}
}

func (m *mockPluginStore) ListPluginCatalog(_ context.Context) ([]store.PluginCatalogEntry, error) {
	var entries []store.PluginCatalogEntry
	for _, e := range m.catalog {
		entries = append(entries, *e)
	}
	return entries, nil
}

func (m *mockPluginStore) GetPluginCatalogEntry(_ context.Context, id string) (*store.PluginCatalogEntry, error) {
	if e, ok := m.catalog[id]; ok {
		return e, nil
	}
	return nil, fmt.Errorf("not found")
}

func (m *mockPluginStore) CreatePluginCatalogEntry(_ context.Context, entry *store.PluginCatalogEntry) error {
	if _, exists := m.catalog[entry.ID]; exists {
		return fmt.Errorf("unique violation")
	}
	m.catalog[entry.ID] = entry
	return nil
}

func (m *mockPluginStore) UpdatePluginCatalogEntry(_ context.Context, entry *store.PluginCatalogEntry) error {
	if _, exists := m.catalog[entry.ID]; !exists {
		return fmt.Errorf("plugin catalog entry not found")
	}
	m.catalog[entry.ID] = entry
	return nil
}

func (m *mockPluginStore) DeletePluginCatalogEntry(_ context.Context, id string) error {
	if _, exists := m.catalog[id]; !exists {
		return fmt.Errorf("plugin catalog entry not found")
	}
	delete(m.catalog, id)
	return nil
}

func (m *mockPluginStore) GetMachine(_ context.Context, id string) (*store.Machine, error) {
	if machine, ok := m.machines[id]; ok {
		return machine, nil
	}
	return nil, fmt.Errorf("not found")
}

func (m *mockPluginStore) ListMachinePlugins(_ context.Context, machineID string) ([]store.MachinePlugin, error) {
	return m.plugins[machineID], nil
}

func (m *mockPluginStore) EnableMachinePlugin(_ context.Context, machineID, pluginID string, overrides json.RawMessage) error {
	m.plugins[machineID] = append(m.plugins[machineID], store.MachinePlugin{
		MachineID:     machineID,
		PluginID:      pluginID,
		Enabled:       true,
		InstallStatus: "installed",
	})
	return nil
}

func (m *mockPluginStore) DisableMachinePlugin(_ context.Context, machineID, pluginID string) error {
	return nil
}

func (m *mockPluginStore) UpdateMachinePluginOverrides(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}

func pluginRequest(method, path string, body interface{}, accountID int, machineID string) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), accountIDKey, accountID)

	rctx := chi.NewRouteContext()
	if machineID != "" {
		rctx.URLParams.Add("id", machineID)
	}
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func pluginAdminRequest(method, path string, body interface{}, pluginID string) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")

	rctx := chi.NewRouteContext()
	if pluginID != "" {
		rctx.URLParams.Add("pluginId", pluginID)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}
```

- [ ] **Step 2: Write TestListPluginCatalog**

```go
func TestListPluginCatalog(t *testing.T) {
	ms := newMockPluginStore()
	ms.catalog["memory-core"] = &store.PluginCatalogEntry{
		ID: "memory-core", Name: "Memory Core", Slot: "memory",
		Version: "1", InstallKind: "bundled", Status: "active",
	}
	srv := &Server{store: ms}

	w := httptest.NewRecorder()
	r := pluginAdminRequest("GET", "/api/admin/plugins", nil, "")
	srv.handleListPluginCatalog(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var entries []store.PluginCatalogEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}
```

- [ ] **Step 3: Write TestCreatePluginCatalogEntry**

```go
func TestCreatePluginCatalogEntry(t *testing.T) {
	ms := newMockPluginStore()
	srv := &Server{store: ms}

	body := map[string]interface{}{
		"id":   "test-plugin",
		"name": "Test Plugin",
		"slot": "test",
	}

	w := httptest.NewRecorder()
	r := pluginAdminRequest("POST", "/api/admin/plugins", body, "")
	srv.handleCreatePluginCatalogEntry(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	if _, exists := ms.catalog["test-plugin"]; !exists {
		t.Error("plugin should exist in catalog after creation")
	}
}
```

- [ ] **Step 4: Write TestEnableMachinePlugin**

```go
func TestEnableMachinePlugin(t *testing.T) {
	ms := newMockPluginStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1}
	srv := &Server{store: ms}

	body := map[string]interface{}{
		"plugin_id": "memory-core",
	}

	w := httptest.NewRecorder()
	r := pluginRequest("POST", "/api/accounts/1/machines/m-1/plugins", body, 1, "m-1")
	srv.handleEnableMachinePlugin(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if len(ms.plugins["m-1"]) != 1 {
		t.Error("expected 1 plugin enabled on machine")
	}
}
```

- [ ] **Step 5: Write TestEnableMachinePlugin_WrongAccount**

```go
func TestEnableMachinePlugin_WrongAccount(t *testing.T) {
	ms := newMockPluginStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 2}
	srv := &Server{store: ms}

	body := map[string]interface{}{"plugin_id": "memory-core"}

	w := httptest.NewRecorder()
	r := pluginRequest("POST", "/api/accounts/1/machines/m-1/plugins", body, 1, "m-1")
	srv.handleEnableMachinePlugin(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}
```

- [ ] **Step 6: Run tests**

Run: `make test-go`
Expected: All tests pass.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/api/plugins_test.go
git commit -m "test: add plugin handler unit tests"
```

---

### Task 17: Final verification

- [ ] **Step 1: Run full Go test suite**

Run: `make test-go`
Expected: All tests pass.

- [ ] **Step 2: Run gateway E2E tests**

Run: `make test-gateway-e2e`
Expected: All tests pass (no config assembly regressions).

- [ ] **Step 3: Run frontend typecheck**

Run: `make typecheck`
Expected: Passes (no frontend changes in this plan).

- [ ] **Step 4: Commit any remaining fixes**

If any tests fail, fix and commit.

- [ ] **Step 5: Update CurrentFeature.md**

Update `docs/CurrentFeature.md` to reflect plugin catalog completion status.
