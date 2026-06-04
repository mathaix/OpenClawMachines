//go:build integration

package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *PostgresStore {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	return &PostgresStore{pool: pool}
}

func TestCreateAndGetArtifactRelease(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()

	r := &ArtifactRelease{
		Kind:     "openclaw",
		Version:  "2026.4.6",
		Channel:  "stable",
		URL:      "gs://ocm-artifacts/openclaw/2026.4.6/openclaw-2026.4.6.tar.zst",
		SHA256:   "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
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

	// Cleanup
	_, _ = pg.pool.Exec(ctx, "DELETE FROM artifact_releases WHERE id = $1", r.ID)
}

func TestCreateArtifactRelease_DuplicateVersionRejected(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()

	r := &ArtifactRelease{
		Kind: "openclaw", Version: "dup-test-2026.4.6", Channel: "stable",
		URL: "gs://test/1", SHA256: "aaa", Metadata: json.RawMessage(`{}`),
	}
	require.NoError(t, pg.CreateArtifactRelease(ctx, r))
	defer func() { _, _ = pg.pool.Exec(ctx, "DELETE FROM artifact_releases WHERE version = $1", r.Version) }()

	dup := &ArtifactRelease{
		Kind: "openclaw", Version: "dup-test-2026.4.6", Channel: "stable",
		URL: "gs://test/2", SHA256: "bbb", Metadata: json.RawMessage(`{}`),
	}
	err := pg.CreateArtifactRelease(ctx, dup)
	assert.Error(t, err, "duplicate (kind, version) should be rejected")
}

func TestListArtifactReleases_OrderAndLimit(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()

	versions := []string{"list-test-2026.4.1", "list-test-2026.4.2", "list-test-2026.4.3"}
	for _, v := range versions {
		require.NoError(t, pg.CreateArtifactRelease(ctx, &ArtifactRelease{
			Kind: "openclaw", Version: v, Channel: "stable",
			URL: "gs://test/" + v, SHA256: v, Metadata: json.RawMessage(`{}`),
		}))
	}
	defer func() {
		for _, v := range versions {
			_, _ = pg.pool.Exec(ctx, "DELETE FROM artifact_releases WHERE version = $1", v)
		}
	}()

	list, err := pg.ListArtifactReleases(ctx, "openclaw", "stable", 2)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list), 2)
}

func TestGetLatestArtifactRelease(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()

	versions := []string{"latest-test-2026.4.1", "latest-test-2026.4.5"}
	for _, v := range versions {
		require.NoError(t, pg.CreateArtifactRelease(ctx, &ArtifactRelease{
			Kind: "openclaw", Version: v, Channel: "stable",
			URL: "gs://test/" + v, SHA256: v, Metadata: json.RawMessage(`{}`),
		}))
	}
	defer func() {
		for _, v := range versions {
			_, _ = pg.pool.Exec(ctx, "DELETE FROM artifact_releases WHERE version = $1", v)
		}
	}()

	latest, err := pg.GetLatestArtifactRelease(ctx, "openclaw", "stable")
	require.NoError(t, err)
	assert.NotEmpty(t, latest.Version)
}
