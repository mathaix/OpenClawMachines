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
