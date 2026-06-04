package store

import (
	"context"
	"fmt"
)

func (pg *PostgresStore) CreateArtifactRelease(ctx context.Context, r *ArtifactRelease) error {
	return pg.pool.QueryRow(ctx, `
		INSERT INTO artifact_releases (kind, version, channel, url, sha256, size_bytes, metadata, min_rootfs_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`,
		r.Kind, r.Version, r.Channel, r.URL, r.SHA256, r.SizeBytes, r.Metadata, r.MinRootfsVersion,
	).Scan(&r.ID, &r.CreatedAt)
}

func (pg *PostgresStore) GetArtifactRelease(ctx context.Context, kind, version string) (*ArtifactRelease, error) {
	var r ArtifactRelease
	err := pg.pool.QueryRow(ctx, `
		SELECT id, kind, version, channel, url, sha256, size_bytes, metadata, created_at, min_rootfs_version
		FROM artifact_releases
		WHERE kind = $1 AND version = $2`,
		kind, version,
	).Scan(&r.ID, &r.Kind, &r.Version, &r.Channel, &r.URL, &r.SHA256, &r.SizeBytes, &r.Metadata, &r.CreatedAt, &r.MinRootfsVersion)
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
		SELECT id, kind, version, channel, url, sha256, size_bytes, metadata, created_at, min_rootfs_version
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
		if err := rows.Scan(&r.ID, &r.Kind, &r.Version, &r.Channel, &r.URL, &r.SHA256, &r.SizeBytes, &r.Metadata, &r.CreatedAt, &r.MinRootfsVersion); err != nil {
			return nil, fmt.Errorf("scan artifact release: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (pg *PostgresStore) GetLatestArtifactRelease(ctx context.Context, kind, channel string) (*ArtifactRelease, error) {
	var r ArtifactRelease
	err := pg.pool.QueryRow(ctx, `
		SELECT id, kind, version, channel, url, sha256, size_bytes, metadata, created_at, min_rootfs_version
		FROM artifact_releases
		WHERE kind = $1 AND channel = $2
		ORDER BY created_at DESC
		LIMIT 1`,
		kind, channel,
	).Scan(&r.ID, &r.Kind, &r.Version, &r.Channel, &r.URL, &r.SHA256, &r.SizeBytes, &r.Metadata, &r.CreatedAt, &r.MinRootfsVersion)
	if err != nil {
		return nil, fmt.Errorf("get latest artifact release: %w", err)
	}
	return &r, nil
}
