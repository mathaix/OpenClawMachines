//go:build integration

package maint

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	return pool
}

func partitionExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var exists bool
	err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = $1)`, name).Scan(&exists)
	require.NoError(t, err)
	return exists
}

func dropPartition(t *testing.T, pool *pgxpool.Pool, name string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		fmt.Sprintf(`DROP TABLE IF EXISTS %s`, (pgx.Identifier{name}).Sanitize()))
	require.NoError(t, err)
}

// createPartitionDirect builds a partition for an arbitrary date — used by
// drop tests to seed historical partitions outside the EnsurePartitions
// "today + N future" range.
func createPartitionDirect(t *testing.T, pool *pgxpool.Pool, table string, date time.Time) string {
	t.Helper()
	name := partitionName(table, date)
	start, end := dayBounds(date)
	ddl := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ($1) TO ($2)`,
		(pgx.Identifier{name}).Sanitize(),
		(pgx.Identifier{table}).Sanitize(),
	)
	_, err := pool.Exec(context.Background(), ddl, start, end)
	require.NoError(t, err)
	return name
}

func TestEnsurePartitions_CreatesTodayAndFuture(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Drop today + future to start clean.
	for i := 0; i <= 5; i++ {
		d := now.Truncate(24*time.Hour).AddDate(0, 0, i)
		dropPartition(t, pool, partitionName("vm_metrics_1s", d))
		dropPartition(t, pool, partitionName("vm_metrics_1m", d))
	}

	require.NoError(t, EnsurePartitions(ctx, pool, 3, now))

	for i := 0; i <= 3; i++ {
		d := now.Truncate(24*time.Hour).AddDate(0, 0, i)
		assert.True(t, partitionExists(t, pool, partitionName("vm_metrics_1s", d)),
			"vm_metrics_1s partition for day +%d should exist", i)
		assert.True(t, partitionExists(t, pool, partitionName("vm_metrics_1m", d)),
			"vm_metrics_1m partition for day +%d should exist", i)
	}
}

func TestEnsurePartitions_Idempotent(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, EnsurePartitions(ctx, pool, 2, now))
	require.NoError(t, EnsurePartitions(ctx, pool, 2, now), "second call must be no-op")
}

func TestDownsample_FullMinuteFinalizes(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Minute).Add(-10 * time.Minute) // bucket 10 min in the past
	require.NoError(t, EnsurePartitions(ctx, pool, 0, now))

	vmID := uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM vm_metrics_1s WHERE vm_id = $1", vmID)
		_, _ = pool.Exec(ctx, "DELETE FROM vm_metrics_1m WHERE vm_id = $1", vmID)
	})

	// Seed 60 1s points spanning a single minute.
	for i := 0; i < 60; i++ {
		ts := now.Add(time.Duration(i) * time.Second)
		_, err := pool.Exec(ctx, `
			INSERT INTO vm_metrics_1s (account_id, host_id, vm_id, kind, ts, cpu_pct, mem_bytes)
			VALUES ($1, $2, $3, 'machine', $4, $5, $6)
		`, 100, 7, vmID, ts, float32(i), int64(i*1000))
		require.NoError(t, err)
	}

	rows, err := Downsample1s1m(ctx, pool, time.Now().UTC(),
		DefaultDownsampleWatermark, DefaultDownsampleFinalizeAfter, 30*time.Minute)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, rows, int64(1), "downsample should have inserted at least our bucket")

	var sampleCount int
	var finalized bool
	var avgCPU float32
	var avgMem int64
	err = pool.QueryRow(ctx, `
		SELECT sample_count, finalized, cpu_pct, mem_bytes
		FROM vm_metrics_1m WHERE vm_id = $1 AND ts = $2
	`, vmID, now).Scan(&sampleCount, &finalized, &avgCPU, &avgMem)
	require.NoError(t, err)
	assert.Equal(t, 60, sampleCount)
	assert.True(t, finalized, "60-sample bucket must finalize")
	assert.InDelta(t, 29.5, avgCPU, 0.01, "avg of 0..59 = 29.5")
	assert.Equal(t, int64(29500), avgMem, "avg of 0,1000,...,59000 = 29500")
}

func TestDownsample_LateArrivalUpdatesUnfinalized(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	bucket := time.Now().UTC().Truncate(time.Minute).Add(-3 * time.Minute) // recent enough to NOT finalize
	require.NoError(t, EnsurePartitions(ctx, pool, 0, bucket))

	vmID := uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM vm_metrics_1s WHERE vm_id = $1", vmID)
		_, _ = pool.Exec(ctx, "DELETE FROM vm_metrics_1m WHERE vm_id = $1", vmID)
	})

	insert := func(seconds []int) {
		for _, s := range seconds {
			ts := bucket.Add(time.Duration(s) * time.Second)
			_, err := pool.Exec(ctx, `
				INSERT INTO vm_metrics_1s (account_id, host_id, vm_id, kind, ts, cpu_pct, mem_bytes)
				VALUES ($1, 7, $2, 'machine', $3, 50.0, 1000)
				ON CONFLICT DO NOTHING
			`, 100, vmID, ts)
			require.NoError(t, err)
		}
	}

	// Round 1 — seed 30 of 60 seconds. Use a long finalizeAfter so it stays unfinalized.
	first := make([]int, 30)
	for i := range first {
		first[i] = i
	}
	insert(first)
	_, err := Downsample1s1m(ctx, pool, time.Now().UTC(),
		DefaultDownsampleWatermark, 30*time.Minute /* very long */, 30*time.Minute)
	require.NoError(t, err)

	var count int
	var finalized bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT sample_count, finalized FROM vm_metrics_1m WHERE vm_id = $1 AND ts = $2`,
		vmID, bucket).Scan(&count, &finalized))
	assert.Equal(t, 30, count)
	assert.False(t, finalized, "with long finalizeAfter and partial bucket, must stay unfinalized")

	// Round 2 — add the other 30 seconds. Re-run downsample; bucket updates.
	second := make([]int, 30)
	for i := range second {
		second[i] = i + 30
	}
	insert(second)
	_, err = Downsample1s1m(ctx, pool, time.Now().UTC(),
		DefaultDownsampleWatermark, 30*time.Minute, 30*time.Minute)
	require.NoError(t, err)

	require.NoError(t, pool.QueryRow(ctx,
		`SELECT sample_count, finalized FROM vm_metrics_1m WHERE vm_id = $1 AND ts = $2`,
		vmID, bucket).Scan(&count, &finalized))
	assert.Equal(t, 60, count, "late-arrival downsample must update unfinalized bucket")
	assert.True(t, finalized, "60-sample bucket must finalize on second pass")

	// Round 3 — add a "very late" extra point. Bucket already finalized → no update.
	insert([]int{59}) // duplicates an existing second so it's a no-op insert
	_, err = pool.Exec(ctx, `
		INSERT INTO vm_metrics_1s (account_id, host_id, vm_id, kind, ts, cpu_pct, mem_bytes)
		VALUES ($1, 7, $2, 'machine', $3, 50.0, 1000)
		ON CONFLICT DO NOTHING
	`, 100, vmID, bucket.Add(50*time.Millisecond))
	require.NoError(t, err)
	preCount := count
	_, err = Downsample1s1m(ctx, pool, time.Now().UTC(),
		DefaultDownsampleWatermark, 30*time.Minute, 30*time.Minute)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT sample_count FROM vm_metrics_1m WHERE vm_id = $1 AND ts = $2`,
		vmID, bucket).Scan(&count))
	assert.Equal(t, preCount, count, "finalized bucket must not be updated by re-runs")
}

func TestDropOldPartitions_DropsOlderThanRetention(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	now := time.Now().UTC()
	// 1s retention is 24 h. Seed a partition 5 days in the past — definitely droppable.
	oldDate := now.AddDate(0, 0, -5).Truncate(24 * time.Hour)
	old1s := createPartitionDirect(t, pool, "vm_metrics_1s", oldDate)
	old1m := createPartitionDirect(t, pool, "vm_metrics_1m", oldDate.AddDate(0, 0, -10)) // 15 days ago, 1m retention is 7d
	t.Cleanup(func() {
		dropPartition(t, pool, old1s)
		dropPartition(t, pool, old1m)
	})

	dropped, err := DropOldPartitions(ctx, pool, now)
	require.NoError(t, err)
	assert.Contains(t, dropped, old1s, "old 1s partition should be dropped")
	assert.Contains(t, dropped, old1m, "old 1m partition should be dropped")
	assert.False(t, partitionExists(t, pool, old1s))
	assert.False(t, partitionExists(t, pool, old1m))
}

func TestDropOldPartitions_KeepsRecent(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, EnsurePartitions(ctx, pool, 0, now))

	// Today's partition is well within retention — must survive.
	todayName := partitionName("vm_metrics_1s", now)
	require.True(t, partitionExists(t, pool, todayName))

	dropped, err := DropOldPartitions(ctx, pool, now)
	require.NoError(t, err)
	assert.NotContains(t, dropped, todayName, "today's 1s partition must not be dropped")
	assert.True(t, partitionExists(t, pool, todayName))
}

func TestRunWithLock_ContentionOneRunsAtATime(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	const testKey int64 = 0x564D5F5445535431 // "VM_TEST1"

	// Two goroutines race for the same key. A 200ms job inside the first
	// holder should mean the second observes ranIt=false.
	var (
		wg      sync.WaitGroup
		results [2]bool
		errs    [2]error
	)
	wg.Add(2)
	for i := range results {
		i := i
		go func() {
			defer wg.Done()
			ran, err := runWithLock(ctx, pool, testKey, func(ctx context.Context, tx pgx.Tx) error {
				time.Sleep(200 * time.Millisecond)
				return nil
			})
			results[i] = ran
			errs[i] = err
		}()
	}
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	ranCount := 0
	for _, r := range results {
		if r {
			ranCount++
		}
	}
	assert.Equal(t, 1, ranCount, "advisory lock must serialize: one ran, one observed lock held")
}

func TestRunWithLock_ReleasesOnTxRollback(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	const testKey int64 = 0x564D5F5445535432 // "VM_TEST2"
	expected := fmt.Errorf("intentional")

	// First call: job returns an error → tx rolls back → advisory lock released.
	_, err := runWithLock(ctx, pool, testKey, func(ctx context.Context, tx pgx.Tx) error {
		return expected
	})
	require.ErrorIs(t, err, expected)

	// Second call: must immediately get the lock since the first tx released it.
	ran, err := runWithLock(ctx, pool, testKey, func(ctx context.Context, tx pgx.Tx) error {
		return nil
	})
	require.NoError(t, err)
	assert.True(t, ran, "lock must release on rollback so subsequent caller can acquire")
}
