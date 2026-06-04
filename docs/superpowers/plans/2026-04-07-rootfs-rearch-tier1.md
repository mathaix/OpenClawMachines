# Rootfs Rearchitecture Tier 1 — Merge-Blocking Work

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete the three merge-blocking items: artifact/host-state DB schema + store CRUD, upgrade handler safety fixes, and frontend feature-flag gating.

**Architecture:** Extend the existing Phase 0 schema (migration 065) with two new tables (`artifact_releases`, `host_artifact_state`) and corresponding store methods. Fix the upgrade handler's persistence ordering and context lifetime bugs. Gate the frontend runtime UI behind the `version_source` field presence.

**Tech Stack:** Go 1.25, pgx/v5 (raw SQL), Chi router, React 18 + TypeScript, Tailwind CSS

---

### Task 1: Add `artifact_releases` Table (Migration 066)

**Files:**
- Create: `backend/migrations/066_artifact_releases.sql`

- [ ] **Step 1: Write migration SQL**

```sql
-- Immutable catalog of published artifact releases.
-- Each row represents one published version of a rootfs or openclaw artifact.
CREATE TABLE IF NOT EXISTS artifact_releases (
    id          SERIAL PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('rootfs', 'openclaw')),
    version     TEXT NOT NULL,
    channel     TEXT NOT NULL DEFAULT 'stable' CHECK (channel IN ('stable', 'rc', 'dev')),
    url         TEXT NOT NULL,
    sha256      TEXT NOT NULL,
    size_bytes  BIGINT,
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_artifact_releases_kind_version
    ON artifact_releases (kind, version);

CREATE INDEX IF NOT EXISTS idx_artifact_releases_kind_channel
    ON artifact_releases (kind, channel, created_at DESC);
```

- [ ] **Step 2: Verify migration applies cleanly**

Run: `cd backend && /usr/local/go/bin/go test ./internal/store/... -run TestMigrations -v -count=1`
Expected: PASS (or, if no migration test harness, verify manually with `psql`)

- [ ] **Step 3: Commit**

```bash
git add backend/migrations/066_artifact_releases.sql
git commit -m "feat: add artifact_releases table (DR-001)"
```

---

### Task 2: Add `host_artifact_state` Table (Migration 067)

**Files:**
- Create: `backend/migrations/067_host_artifact_state.sql`

- [ ] **Step 1: Write migration SQL**

```sql
-- Per-host artifact staging state. Tracks which release version is staged,
-- active (currently booting VMs), and the host-level default.
-- One row per (host_id, kind) pair.
CREATE TABLE IF NOT EXISTS host_artifact_state (
    id                  SERIAL PRIMARY KEY,
    host_id             INT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    kind                TEXT NOT NULL CHECK (kind IN ('rootfs', 'openclaw')),
    staged_version      TEXT,
    active_version      TEXT,
    default_version     TEXT,
    last_staged_at      TIMESTAMPTZ,
    last_activated_at   TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (host_id, kind)
);

CREATE INDEX IF NOT EXISTS idx_host_artifact_state_host
    ON host_artifact_state (host_id);
```

- [ ] **Step 2: Commit**

```bash
git add backend/migrations/067_host_artifact_state.sql
git commit -m "feat: add host_artifact_state table (DR-001)"
```

---

### Task 3: Add Store Types and `ArtifactReleaseRepo` Interface

**Files:**
- Modify: `backend/internal/store/store.go`

- [ ] **Step 1: Add entity types after `Host` struct (around line 141)**

Add after the existing `Host` struct:

```go
// ArtifactRelease is an immutable record of a published artifact version.
type ArtifactRelease struct {
	ID        int             `json:"id"`
	Kind      string          `json:"kind"`      // "rootfs" or "openclaw"
	Version   string          `json:"version"`
	Channel   string          `json:"channel"`   // "stable", "rc", "dev"
	URL       string          `json:"url"`
	SHA256    string          `json:"sha256"`
	SizeBytes *int64          `json:"size_bytes,omitempty"`
	Metadata  json.RawMessage `json:"metadata"`
	CreatedAt time.Time       `json:"created_at"`
}

// HostArtifactState tracks per-host artifact staging for a single kind.
type HostArtifactState struct {
	ID              int        `json:"id"`
	HostID          int        `json:"host_id"`
	Kind            string     `json:"kind"` // "rootfs" or "openclaw"
	StagedVersion   *string    `json:"staged_version,omitempty"`
	ActiveVersion   *string    `json:"active_version,omitempty"`
	DefaultVersion  *string    `json:"default_version,omitempty"`
	LastStagedAt    *time.Time `json:"last_staged_at,omitempty"`
	LastActivatedAt *time.Time `json:"last_activated_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}
```

- [ ] **Step 2: Add repo interfaces (near the other repo interfaces, around line 560)**

```go
// ArtifactReleaseRepo handles artifact release persistence.
type ArtifactReleaseRepo interface {
	CreateArtifactRelease(ctx context.Context, r *ArtifactRelease) error
	GetArtifactRelease(ctx context.Context, kind, version string) (*ArtifactRelease, error)
	ListArtifactReleases(ctx context.Context, kind, channel string, limit int) ([]ArtifactRelease, error)
	GetLatestArtifactRelease(ctx context.Context, kind, channel string) (*ArtifactRelease, error)
}

// HostArtifactStateRepo handles per-host artifact staging state.
type HostArtifactStateRepo interface {
	UpsertHostArtifactState(ctx context.Context, s *HostArtifactState) error
	GetHostArtifactState(ctx context.Context, hostID int, kind string) (*HostArtifactState, error)
	ListHostArtifactStates(ctx context.Context, hostID int) ([]HostArtifactState, error)
}
```

- [ ] **Step 3: Add repos to `Store` interface (around line 1004)**

Add `ArtifactReleaseRepo` and `HostArtifactStateRepo` to the `Store` interface list.

- [ ] **Step 4: Verify compilation**

Run: `cd backend && /usr/local/go/bin/go build ./...`
Expected: compilation error in `postgres.go` because the new interface methods are not implemented yet (expected — we implement them in Task 4)

- [ ] **Step 5: Commit**

```bash
git add backend/internal/store/store.go
git commit -m "feat: add ArtifactRelease and HostArtifactState store types and interfaces (DR-002)"
```

---

### Task 4: Implement Postgres Store Methods

**Files:**
- Create: `backend/internal/store/postgres_artifact_releases.go`
- Create: `backend/internal/store/postgres_host_artifact_state.go`

- [ ] **Step 1: Write `postgres_artifact_releases.go`**

```go
package store

import (
	"context"
	"fmt"
)

func (pg *PostgresStore) CreateArtifactRelease(ctx context.Context, r *ArtifactRelease) error {
	return pg.pool.QueryRow(ctx, `
		INSERT INTO artifact_releases (kind, version, channel, url, sha256, size_bytes, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at`,
		r.Kind, r.Version, r.Channel, r.URL, r.SHA256, r.SizeBytes, r.Metadata,
	).Scan(&r.ID, &r.CreatedAt)
}

func (pg *PostgresStore) GetArtifactRelease(ctx context.Context, kind, version string) (*ArtifactRelease, error) {
	var r ArtifactRelease
	err := pg.pool.QueryRow(ctx, `
		SELECT id, kind, version, channel, url, sha256, size_bytes, metadata, created_at
		FROM artifact_releases
		WHERE kind = $1 AND version = $2`,
		kind, version,
	).Scan(&r.ID, &r.Kind, &r.Version, &r.Channel, &r.URL, &r.SHA256, &r.SizeBytes, &r.Metadata, &r.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("get artifact release: %w", err)
	}
	return &r, nil
}

func (pg *PostgresStore) ListArtifactReleases(ctx context.Context, kind, channel string, limit int) ([]ArtifactRelease, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := pg.pool.Query(ctx, `
		SELECT id, kind, version, channel, url, sha256, size_bytes, metadata, created_at
		FROM artifact_releases
		WHERE kind = $1 AND channel = $2
		ORDER BY created_at DESC
		LIMIT $3`,
		kind, channel, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list artifact releases: %w", err)
	}
	defer rows.Close()
	var out []ArtifactRelease
	for rows.Next() {
		var r ArtifactRelease
		if err := rows.Scan(&r.ID, &r.Kind, &r.Version, &r.Channel, &r.URL, &r.SHA256, &r.SizeBytes, &r.Metadata, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan artifact release: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (pg *PostgresStore) GetLatestArtifactRelease(ctx context.Context, kind, channel string) (*ArtifactRelease, error) {
	var r ArtifactRelease
	err := pg.pool.QueryRow(ctx, `
		SELECT id, kind, version, channel, url, sha256, size_bytes, metadata, created_at
		FROM artifact_releases
		WHERE kind = $1 AND channel = $2
		ORDER BY created_at DESC
		LIMIT 1`,
		kind, channel,
	).Scan(&r.ID, &r.Kind, &r.Version, &r.Channel, &r.URL, &r.SHA256, &r.SizeBytes, &r.Metadata, &r.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("get latest artifact release: %w", err)
	}
	return &r, nil
}
```

- [ ] **Step 2: Write `postgres_host_artifact_state.go`**

```go
package store

import (
	"context"
	"fmt"
)

func (pg *PostgresStore) UpsertHostArtifactState(ctx context.Context, s *HostArtifactState) error {
	return pg.pool.QueryRow(ctx, `
		INSERT INTO host_artifact_state (host_id, kind, staged_version, active_version, default_version, last_staged_at, last_activated_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		ON CONFLICT (host_id, kind) DO UPDATE SET
			staged_version    = EXCLUDED.staged_version,
			active_version    = EXCLUDED.active_version,
			default_version   = EXCLUDED.default_version,
			last_staged_at    = EXCLUDED.last_staged_at,
			last_activated_at = EXCLUDED.last_activated_at,
			updated_at        = NOW()
		RETURNING id, updated_at`,
		s.HostID, s.Kind, s.StagedVersion, s.ActiveVersion, s.DefaultVersion, s.LastStagedAt, s.LastActivatedAt,
	).Scan(&s.ID, &s.UpdatedAt)
}

func (pg *PostgresStore) GetHostArtifactState(ctx context.Context, hostID int, kind string) (*HostArtifactState, error) {
	var s HostArtifactState
	err := pg.pool.QueryRow(ctx, `
		SELECT id, host_id, kind, staged_version, active_version, default_version,
		       last_staged_at, last_activated_at, updated_at
		FROM host_artifact_state
		WHERE host_id = $1 AND kind = $2`,
		hostID, kind,
	).Scan(&s.ID, &s.HostID, &s.Kind, &s.StagedVersion, &s.ActiveVersion, &s.DefaultVersion,
		&s.LastStagedAt, &s.LastActivatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get host artifact state: %w", err)
	}
	return &s, nil
}

func (pg *PostgresStore) ListHostArtifactStates(ctx context.Context, hostID int) ([]HostArtifactState, error) {
	rows, err := pg.pool.Query(ctx, `
		SELECT id, host_id, kind, staged_version, active_version, default_version,
		       last_staged_at, last_activated_at, updated_at
		FROM host_artifact_state
		WHERE host_id = $1
		ORDER BY kind`,
		hostID,
	)
	if err != nil {
		return nil, fmt.Errorf("list host artifact states: %w", err)
	}
	defer rows.Close()
	var out []HostArtifactState
	for rows.Next() {
		var s HostArtifactState
		if err := rows.Scan(&s.ID, &s.HostID, &s.Kind, &s.StagedVersion, &s.ActiveVersion, &s.DefaultVersion,
			&s.LastStagedAt, &s.LastActivatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan host artifact state: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
```

- [ ] **Step 3: Verify compilation**

Run: `cd backend && /usr/local/go/bin/go build ./...`
Expected: PASS (all interface methods now implemented)

- [ ] **Step 4: Commit**

```bash
git add backend/internal/store/postgres_artifact_releases.go backend/internal/store/postgres_host_artifact_state.go
git commit -m "feat: implement ArtifactRelease and HostArtifactState Postgres store methods (DR-002)"
```

---

### Task 5: Add Store Tests for Artifact Releases and Host Artifact State

**Files:**
- Create: `backend/internal/store/postgres_artifact_releases_test.go`
- Create: `backend/internal/store/postgres_host_artifact_state_test.go`

- [ ] **Step 1: Write `postgres_artifact_releases_test.go`**

```go
package store

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateAndGetArtifactRelease(t *testing.T) {
	pg := testPostgresStore(t)
	ctx := testContext(t)

	r := &ArtifactRelease{
		Kind:    "openclaw",
		Version: "2026.4.6",
		Channel: "stable",
		URL:     "gs://ocm-artifacts/openclaw/2026.4.6/openclaw-2026.4.6.tar.zst",
		SHA256:  "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		Metadata: json.RawMessage(`{}`),
	}
	require.NoError(t, pg.CreateArtifactRelease(ctx, r))
	assert.Greater(t, r.ID, 0)
	assert.False(t, r.CreatedAt.IsZero())

	got, err := pg.GetArtifactRelease(ctx, "openclaw", "2026.4.6")
	require.NoError(t, err)
	assert.Equal(t, r.ID, got.ID)
	assert.Equal(t, "openclaw", got.Kind)
	assert.Equal(t, "2026.4.6", got.Version)
	assert.Equal(t, "stable", got.Channel)
	assert.Equal(t, r.SHA256, got.SHA256)
}

func TestCreateArtifactRelease_DuplicateVersionRejected(t *testing.T) {
	pg := testPostgresStore(t)
	ctx := testContext(t)

	r := &ArtifactRelease{
		Kind: "openclaw", Version: "2026.4.6", Channel: "stable",
		URL: "gs://test/1", SHA256: "aaa", Metadata: json.RawMessage(`{}`),
	}
	require.NoError(t, pg.CreateArtifactRelease(ctx, r))

	dup := &ArtifactRelease{
		Kind: "openclaw", Version: "2026.4.6", Channel: "stable",
		URL: "gs://test/2", SHA256: "bbb", Metadata: json.RawMessage(`{}`),
	}
	err := pg.CreateArtifactRelease(ctx, dup)
	assert.Error(t, err, "duplicate (kind, version) should be rejected")
}

func TestListArtifactReleases_OrderAndLimit(t *testing.T) {
	pg := testPostgresStore(t)
	ctx := testContext(t)

	for _, v := range []string{"2026.4.1", "2026.4.2", "2026.4.3"} {
		require.NoError(t, pg.CreateArtifactRelease(ctx, &ArtifactRelease{
			Kind: "openclaw", Version: v, Channel: "stable",
			URL: "gs://test/" + v, SHA256: v, Metadata: json.RawMessage(`{}`),
		}))
	}

	list, err := pg.ListArtifactReleases(ctx, "openclaw", "stable", 2)
	require.NoError(t, err)
	assert.Len(t, list, 2)
	assert.Equal(t, "2026.4.3", list[0].Version, "newest first")
	assert.Equal(t, "2026.4.2", list[1].Version)
}

func TestGetLatestArtifactRelease(t *testing.T) {
	pg := testPostgresStore(t)
	ctx := testContext(t)

	for _, v := range []string{"2026.4.1", "2026.4.5"} {
		require.NoError(t, pg.CreateArtifactRelease(ctx, &ArtifactRelease{
			Kind: "openclaw", Version: v, Channel: "stable",
			URL: "gs://test/" + v, SHA256: v, Metadata: json.RawMessage(`{}`),
		}))
	}

	latest, err := pg.GetLatestArtifactRelease(ctx, "openclaw", "stable")
	require.NoError(t, err)
	assert.Equal(t, "2026.4.5", latest.Version)
}
```

- [ ] **Step 2: Write `postgres_host_artifact_state_test.go`**

```go
package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpsertAndGetHostArtifactState(t *testing.T) {
	pg := testPostgresStore(t)
	ctx := testContext(t)
	host := createTestHost(t, pg)

	staged := "2026.4.6"
	now := time.Now().UTC().Truncate(time.Microsecond)
	s := &HostArtifactState{
		HostID:        host.ID,
		Kind:          "openclaw",
		StagedVersion: &staged,
		LastStagedAt:  &now,
	}
	require.NoError(t, pg.UpsertHostArtifactState(ctx, s))
	assert.Greater(t, s.ID, 0)

	got, err := pg.GetHostArtifactState(ctx, host.ID, "openclaw")
	require.NoError(t, err)
	assert.Equal(t, &staged, got.StagedVersion)
	assert.Nil(t, got.ActiveVersion)
}

func TestUpsertHostArtifactState_UpdatesExisting(t *testing.T) {
	pg := testPostgresStore(t)
	ctx := testContext(t)
	host := createTestHost(t, pg)

	v1 := "2026.4.5"
	s := &HostArtifactState{HostID: host.ID, Kind: "openclaw", StagedVersion: &v1}
	require.NoError(t, pg.UpsertHostArtifactState(ctx, s))
	originalID := s.ID

	v2 := "2026.4.6"
	s2 := &HostArtifactState{HostID: host.ID, Kind: "openclaw", StagedVersion: &v2}
	require.NoError(t, pg.UpsertHostArtifactState(ctx, s2))

	got, err := pg.GetHostArtifactState(ctx, host.ID, "openclaw")
	require.NoError(t, err)
	assert.Equal(t, originalID, got.ID, "should update same row")
	assert.Equal(t, &v2, got.StagedVersion)
}

func TestListHostArtifactStates(t *testing.T) {
	pg := testPostgresStore(t)
	ctx := testContext(t)
	host := createTestHost(t, pg)

	v := "2026.4.6"
	require.NoError(t, pg.UpsertHostArtifactState(ctx, &HostArtifactState{
		HostID: host.ID, Kind: "openclaw", StagedVersion: &v,
	}))
	require.NoError(t, pg.UpsertHostArtifactState(ctx, &HostArtifactState{
		HostID: host.ID, Kind: "rootfs", StagedVersion: &v,
	}))

	list, err := pg.ListHostArtifactStates(ctx, host.ID)
	require.NoError(t, err)
	assert.Len(t, list, 2)
}
```

Note: `testPostgresStore`, `testContext`, and `createTestHost` are existing test helpers in the store package. Check `backend/internal/store/postgres_test.go` for the exact signatures. If `createTestHost` doesn't exist, create a minimal one that inserts a host row.

- [ ] **Step 3: Run tests**

Run: `cd backend && /usr/local/go/bin/go test ./internal/store/... -run 'TestCreate.*ArtifactRelease|TestList.*ArtifactRelease|TestGetLatest|TestUpsert.*HostArtifact|TestList.*HostArtifact' -v -count=1`
Expected: PASS (or SKIP if no test DB — that's fine for CI, the important thing is they compile)

- [ ] **Step 4: Commit**

```bash
git add backend/internal/store/postgres_artifact_releases_test.go backend/internal/store/postgres_host_artifact_state_test.go
git commit -m "test: add store tests for artifact releases and host artifact state (DR-002)"
```

---

### Task 6: Fix Upgrade Handler — Defer Persistence Until After Health Gate

**Files:**
- Modify: `backend/internal/api/machines.go:537-762`

The current code writes the desired version to the DB (line 659) BEFORE the health gate for the `apply_now=false` path, and for `apply_now=true` it persists only after success (line 685). The `apply_now=false` path is correct — it's just queuing a change. But the `apply_now=true` path has a subtler issue: the `operationCtx` is derived from `context.Background()` with a 10-minute timeout (line 595), which is correct. However, the request context `r.Context()` is still used for activity logging after the long-running operation, which could be cancelled.

The main fixes needed:

1. **Detach activity logging from request context**: use `context.Background()` for `logMachineOpenClawActivity` calls that happen after the long-running operation.
2. **Allow first-ever immediate upgrades**: the noop check at line 635 compares `targetVersion == previousVersion`, but `previousVersion` can be empty string for machines that have never had an OpenClaw version set. When empty, `apply_now=true` should proceed (it's the first version set).

- [ ] **Step 1: Fix first-ever upgrade rejection**

In `handleApplyMachineOpenClawChange`, find the noop check (around line 635):

```go
	if targetVersion == previousVersion && *runtimeSource == previousRuntimeSource && !hasPendingChange {
```

Replace with:

```go
	isFirstVersion := previousVersion == ""
	if !isFirstVersion && targetVersion == previousVersion && *runtimeSource == previousRuntimeSource && !hasPendingChange {
```

- [ ] **Step 2: Detach logging context from request**

Change the `logMachineOpenClawActivity` calls inside the `apply_now=true` block (lines 692, 719, 742) to use `context.Background()` instead of `r.Context()`:

Find all three occurrences of:
```go
	s.logMachineOpenClawActivity(r.Context(), claims, accountID,
```

In the apply-now success and failure paths (after `restartMachineWithOpenClawHealthGate`), replace `r.Context()` with `context.Background()`.

Note: the `apply_now=false` paths at lines 637 and 665 should keep using `r.Context()` since those complete synchronously.

- [ ] **Step 3: Run existing upgrade/rollback tests**

Run: `cd backend && /usr/local/go/bin/go test ./internal/api/... -run 'TestHandle(UpgradeMachineOpenClaw|RollbackMachineOpenClaw)' -v -count=1`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/machines.go
git commit -m "fix: allow first-ever openclaw upgrade and detach logging from request context (DR-104)"
```

---

### Task 7: Gate Frontend Runtime UI Behind Feature Flag

**Files:**
- Modify: `frontend/src/pages/machine-tabs/OverviewTab.tsx`

The `RuntimeVersionSection` component already checks `resolverActive` (line 66) to decide whether to show the upgrade/rollback controls. But the entire section is always rendered in the overview. We need to hide it entirely when the resolver is not active (i.e., when `version_source` is null or "legacy").

- [ ] **Step 1: Conditionally render RuntimeVersionSection**

In `OverviewTab.tsx`, find where `RuntimeVersionSection` is rendered (search for `<RuntimeVersionSection`). Wrap it with a condition:

```tsx
{machine.version_source != null && machine.version_source !== "legacy" && (
  <RuntimeVersionSection machine={machine} accountId={accountId} />
)}
```

This ensures the entire runtime section (including the collapsed header showing version info) is hidden for machines on legacy version management.

- [ ] **Step 2: Verify build**

Run: `cd frontend && npx tsc --noEmit`
Expected: PASS (no type errors)

- [ ] **Step 3: Commit**

```bash
git add frontend/src/pages/machine-tabs/OverviewTab.tsx
git commit -m "fix: gate RuntimeVersionSection behind version resolver feature flag (DR-006)"
```

---

### Task 8: Update CurrentFeature.md

**Files:**
- Modify: `docs/CurrentFeature.md`

- [ ] **Step 1: Update status**

Update the "Remaining Gaps" section to reflect completed items:
- Mark "DR-001 / DR-002 release + host artifact tables and store CRUD" as done
- Mark "DR-104 persistence ordering and first-upgrade fix" as done
- Mark "DR-006 frontend feature flag gating" as done
- Update the "Current Test Signal" section with latest results

- [ ] **Step 2: Commit**

```bash
git add docs/CurrentFeature.md
git commit -m "docs: update CurrentFeature.md after Tier 1 completion"
```

---

### Task 9: Run Full Test Suite

- [ ] **Step 1: Run store tests**

Run: `cd backend && /usr/local/go/bin/go test ./internal/store/... -v -count=1`
Expected: PASS

- [ ] **Step 2: Run machines tests**

Run: `cd backend && /usr/local/go/bin/go test ./internal/machines/... -v -count=1`
Expected: PASS

- [ ] **Step 3: Run API tests (upgrade/rollback)**

Run: `cd backend && /usr/local/go/bin/go test ./internal/api/... -run 'TestHandle(UpgradeMachineOpenClaw|RollbackMachineOpenClaw|UpdateMachine|CreateMachine)' -v -count=1`
Expected: PASS

- [ ] **Step 4: Run frontend typecheck**

Run: `cd frontend && npx tsc --noEmit`
Expected: PASS

- [ ] **Step 5: Run rootfs tests**

Run: `cd backend && /usr/local/go/bin/go test ./internal/rootfs/... -v -count=1`
Expected: PASS
