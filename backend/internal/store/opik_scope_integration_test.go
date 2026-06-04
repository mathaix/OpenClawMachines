//go:build integration

package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func insertOpikScopeTestProject(t *testing.T, pg *PostgresStore, accountID int) string {
	t.Helper()

	ctx := context.Background()
	var projectID string
	name := fmt.Sprintf("scope-test-%d-%d", accountID, time.Now().UnixNano())
	err := pg.pool.QueryRow(ctx,
		`INSERT INTO opik_projects (name, account_id) VALUES ($1, $2) RETURNING id::text`,
		name, accountID,
	).Scan(&projectID)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = pg.pool.Exec(ctx, `DELETE FROM opik_spans WHERE project_id = $1`, projectID)
		_, _ = pg.pool.Exec(ctx, `DELETE FROM opik_traces WHERE project_id = $1`, projectID)
		_, _ = pg.pool.Exec(ctx, `DELETE FROM opik_projects WHERE id = $1`, projectID)
	})

	return projectID
}

func nextOpikScopeTestUUID(t *testing.T, pg *PostgresStore) string {
	t.Helper()

	var id string
	err := pg.pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id)
	require.NoError(t, err)
	return id
}

func TestPostgresUpsertOpikSpanBeforeTraceCreatesPlaceholder(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()
	projectID := insertOpikScopeTestProject(t, pg, 301)
	traceID := nextOpikScopeTestUUID(t, pg)
	spanID := nextOpikScopeTestUUID(t, pg)
	start := time.Now().UTC().Add(-time.Minute)

	err := pg.UpsertOpikSpan(ctx, &OpikSpan{
		ID:        spanID,
		TraceID:   traceID,
		ProjectID: projectID,
		AccountID: 301,
		MachineID: "m-span",
		Type:      "llm",
		StartTime: start,
	})
	require.NoError(t, err)

	var placeholder bool
	var machineID string
	err = pg.pool.QueryRow(ctx,
		`SELECT is_placeholder, machine_id FROM opik_traces WHERE id = $1`,
		traceID,
	).Scan(&placeholder, &machineID)
	require.NoError(t, err)
	require.True(t, placeholder)
	require.Equal(t, "m-span", machineID)

	name := "real trace"
	err = pg.UpsertOpikTrace(ctx, &OpikTrace{
		ID:        traceID,
		ProjectID: projectID,
		AccountID: 301,
		MachineID: "m-owner",
		Name:      &name,
		StartTime: start.Add(-time.Second),
	})
	require.NoError(t, err)

	err = pg.pool.QueryRow(ctx,
		`SELECT is_placeholder, machine_id, name FROM opik_traces WHERE id = $1`,
		traceID,
	).Scan(&placeholder, &machineID, &name)
	require.NoError(t, err)
	require.False(t, placeholder)
	require.Equal(t, "m-owner", machineID)
	require.Equal(t, "real trace", name)
}

func TestPostgresUpsertOpikTraceRejectsCrossMachineCollision(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()
	projectID := insertOpikScopeTestProject(t, pg, 302)
	traceID := nextOpikScopeTestUUID(t, pg)
	start := time.Now().UTC()

	require.NoError(t, pg.UpsertOpikTrace(ctx, &OpikTrace{
		ID:        traceID,
		ProjectID: projectID,
		AccountID: 302,
		MachineID: "m-owner",
		StartTime: start,
	}))

	err := pg.UpsertOpikTrace(ctx, &OpikTrace{
		ID:        traceID,
		ProjectID: projectID,
		AccountID: 302,
		MachineID: "m-other",
		StartTime: start,
	})
	require.ErrorIs(t, err, ErrOpikScopeConflict)
}

func TestPostgresUpsertOpikSpanRejectsCrossMachineCollision(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()
	projectID := insertOpikScopeTestProject(t, pg, 303)
	traceID := nextOpikScopeTestUUID(t, pg)
	spanID := nextOpikScopeTestUUID(t, pg)
	start := time.Now().UTC()

	require.NoError(t, pg.UpsertOpikTrace(ctx, &OpikTrace{
		ID:        traceID,
		ProjectID: projectID,
		AccountID: 303,
		MachineID: "m-owner",
		StartTime: start,
	}))
	require.NoError(t, pg.UpsertOpikSpan(ctx, &OpikSpan{
		ID:        spanID,
		TraceID:   traceID,
		ProjectID: projectID,
		AccountID: 303,
		MachineID: "m-owner",
		Type:      "tool",
		StartTime: start,
	}))

	err := pg.UpsertOpikSpan(ctx, &OpikSpan{
		ID:        spanID,
		TraceID:   traceID,
		ProjectID: projectID,
		AccountID: 303,
		MachineID: "m-other",
		Type:      "tool",
		StartTime: start,
	})
	require.ErrorIs(t, err, ErrOpikScopeConflict)
}

func TestPostgresUpsertOpikSpanRejectsOtherAccountTrace(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()
	ownerProjectID := insertOpikScopeTestProject(t, pg, 304)
	otherProjectID := insertOpikScopeTestProject(t, pg, 305)
	traceID := nextOpikScopeTestUUID(t, pg)
	spanID := nextOpikScopeTestUUID(t, pg)
	start := time.Now().UTC()

	require.NoError(t, pg.UpsertOpikTrace(ctx, &OpikTrace{
		ID:        traceID,
		ProjectID: ownerProjectID,
		AccountID: 304,
		MachineID: "m-owner",
		StartTime: start,
	}))

	err := pg.UpsertOpikSpan(ctx, &OpikSpan{
		ID:        spanID,
		TraceID:   traceID,
		ProjectID: otherProjectID,
		AccountID: 305,
		MachineID: "m-other-account",
		Type:      "llm",
		StartTime: start,
	})
	require.ErrorIs(t, err, ErrOpikScopeConflict)
}
