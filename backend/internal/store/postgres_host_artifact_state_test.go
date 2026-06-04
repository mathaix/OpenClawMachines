//go:build integration

package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpsertAndGetHostArtifactState(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()

	// This test requires a host row to exist. Skip if we can't find one.
	var hostID int
	err := pg.pool.QueryRow(ctx, "SELECT id FROM hosts LIMIT 1").Scan(&hostID)
	if err != nil {
		t.Skip("no hosts in test database, skipping")
	}

	staged := "2026.4.6"
	s := &HostArtifactState{
		HostID:        hostID,
		Kind:          "openclaw",
		StagedVersion: &staged,
	}
	require.NoError(t, pg.UpsertHostArtifactState(ctx, s))
	assert.Greater(t, s.ID, 0)

	got, err := pg.GetHostArtifactState(ctx, hostID, "openclaw")
	require.NoError(t, err)
	assert.Equal(t, &staged, got.StagedVersion)
	assert.Nil(t, got.ActiveVersion)

	// Cleanup
	_, _ = pg.pool.Exec(ctx, "DELETE FROM host_artifact_state WHERE id = $1", s.ID)
}

func TestUpsertHostArtifactState_UpdatesExisting(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()

	var hostID int
	err := pg.pool.QueryRow(ctx, "SELECT id FROM hosts LIMIT 1").Scan(&hostID)
	if err != nil {
		t.Skip("no hosts in test database, skipping")
	}

	v1 := "upsert-test-2026.4.5"
	s := &HostArtifactState{HostID: hostID, Kind: "openclaw", StagedVersion: &v1}
	require.NoError(t, pg.UpsertHostArtifactState(ctx, s))
	originalID := s.ID

	v2 := "upsert-test-2026.4.6"
	s2 := &HostArtifactState{HostID: hostID, Kind: "openclaw", StagedVersion: &v2}
	require.NoError(t, pg.UpsertHostArtifactState(ctx, s2))

	got, err := pg.GetHostArtifactState(ctx, hostID, "openclaw")
	require.NoError(t, err)
	assert.Equal(t, originalID, got.ID, "should update same row")
	assert.Equal(t, &v2, got.StagedVersion)

	// Cleanup
	_, _ = pg.pool.Exec(ctx, "DELETE FROM host_artifact_state WHERE id = $1", originalID)
}

func TestListHostArtifactStates(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()

	var hostID int
	err := pg.pool.QueryRow(ctx, "SELECT id FROM hosts LIMIT 1").Scan(&hostID)
	if err != nil {
		t.Skip("no hosts in test database, skipping")
	}

	v := "list-test-2026.4.6"
	require.NoError(t, pg.UpsertHostArtifactState(ctx, &HostArtifactState{
		HostID: hostID, Kind: "openclaw", StagedVersion: &v,
	}))
	require.NoError(t, pg.UpsertHostArtifactState(ctx, &HostArtifactState{
		HostID: hostID, Kind: "rootfs", StagedVersion: &v,
	}))
	defer func() {
		_, _ = pg.pool.Exec(ctx, "DELETE FROM host_artifact_state WHERE host_id = $1", hostID)
	}()

	list, err := pg.ListHostArtifactStates(ctx, hostID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list), 2)
}
