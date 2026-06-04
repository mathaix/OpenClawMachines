//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Behavioral round-trips for vm_metrics_1s and vm_metrics_1m introduced in
// migration 084. These run against a real Postgres (TEST_DATABASE_URL).
// File-shape invariants live in migration_084_test.go and run without a DB.

func newVMMetricFixture(t *testing.T) (string, int, int, int) {
	t.Helper()
	// VM ids are strings of UUID; account/host ids are arbitrary ints we
	// don't bother creating real rows for — vm_metrics has no FK to them.
	return uuid.NewString(), 9001, 7, 1
}

func float32Ptr(f float32) *float32 { return &f }

func TestInsertVMMetrics_RoundTrip(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()
	vmID, accountID, hostID, _ := newVMMetricFixture(t)
	t.Cleanup(func() { _, _ = pg.pool.Exec(ctx, "DELETE FROM vm_metrics_1s WHERE vm_id = $1", vmID) })

	base := time.Now().UTC().Truncate(time.Second)
	points := []VMMetricPoint{
		{AccountID: accountID, HostID: hostID, VMID: vmID, Kind: VMMetricKindMachine, Timestamp: base, CPUPct: nil, MemBytes: 100_000_000},
		{AccountID: accountID, HostID: hostID, VMID: vmID, Kind: VMMetricKindMachine, Timestamp: base.Add(time.Second), CPUPct: float32Ptr(12.5), MemBytes: 110_000_000},
		{AccountID: accountID, HostID: hostID, VMID: vmID, Kind: VMMetricKindMachine, Timestamp: base.Add(2 * time.Second), CPUPct: float32Ptr(204.7), MemBytes: 120_000_000},
	}

	require.NoError(t, pg.InsertVMMetrics(ctx, points))

	got, err := pg.QueryVMMetricsRange(ctx, accountID, vmID, VMMetricKindMachine, base, base.Add(10*time.Second), VMMetricResolution1s)
	require.NoError(t, err)
	require.Len(t, got, 3)

	// First sample's cpu_pct must round-trip as nil — design rev. 3 keeps
	// cpu_pct nullable for the first-tick case.
	assert.Nil(t, got[0].CPUPct, "first sample cpu_pct should be NULL")
	require.NotNil(t, got[1].CPUPct)
	assert.InDelta(t, 12.5, *got[1].CPUPct, 0.01)
	require.NotNil(t, got[2].CPUPct)
	assert.InDelta(t, 204.7, *got[2].CPUPct, 0.01, "multi-core CPU >100%% should round-trip")

	assert.Equal(t, int64(120_000_000), got[2].MemBytes)
	assert.Equal(t, VMMetricKindMachine, got[0].Kind)
	assert.Equal(t, accountID, got[0].AccountID)
	assert.Equal(t, hostID, got[0].HostID)
}

func TestInsertVMMetrics_DuplicatesDropped(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()
	vmID, accountID, hostID, _ := newVMMetricFixture(t)
	t.Cleanup(func() { _, _ = pg.pool.Exec(ctx, "DELETE FROM vm_metrics_1s WHERE vm_id = $1", vmID) })

	base := time.Now().UTC().Truncate(time.Second)
	dup := VMMetricPoint{AccountID: accountID, HostID: hostID, VMID: vmID, Kind: VMMetricKindMachine, Timestamp: base, CPUPct: float32Ptr(1.0), MemBytes: 1}

	// Same (vm_id, kind, ts) twice in one batch — second is silently dropped.
	require.NoError(t, pg.InsertVMMetrics(ctx, []VMMetricPoint{dup, dup}))

	got, err := pg.QueryVMMetricsRange(ctx, accountID, vmID, VMMetricKindMachine, base, base.Add(time.Second), VMMetricResolution1s)
	require.NoError(t, err)
	assert.Len(t, got, 1, "duplicate (vm_id, kind, ts) within a batch should dedupe")

	// Insert again with different cpu_pct — also dropped (no row update).
	updated := dup
	updated.CPUPct = float32Ptr(99.0)
	require.NoError(t, pg.InsertVMMetrics(ctx, []VMMetricPoint{updated}))
	got, err = pg.QueryVMMetricsRange(ctx, accountID, vmID, VMMetricKindMachine, base, base.Add(time.Second), VMMetricResolution1s)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].CPUPct)
	assert.InDelta(t, 1.0, *got[0].CPUPct, 0.01, "ON CONFLICT DO NOTHING — original row preserved")
}

func TestInsertVMMetrics_RejectsUnknownKind(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()
	vmID, accountID, hostID, _ := newVMMetricFixture(t)

	err := pg.InsertVMMetrics(ctx, []VMMetricPoint{{
		AccountID: accountID, HostID: hostID, VMID: vmID,
		Kind: "rogue", Timestamp: time.Now(), MemBytes: 1,
	}})
	require.ErrorIs(t, err, ErrUnsupportedKind)
}

func TestQueryVMMetricsRange_FiltersByAccountAndVM(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()
	vmA, accountA, hostID, _ := newVMMetricFixture(t)
	vmB := uuid.NewString()
	accountB := accountA + 1
	t.Cleanup(func() {
		_, _ = pg.pool.Exec(ctx, "DELETE FROM vm_metrics_1s WHERE vm_id = ANY($1)", []string{vmA, vmB})
	})

	base := time.Now().UTC().Truncate(time.Second)
	pts := []VMMetricPoint{
		{AccountID: accountA, HostID: hostID, VMID: vmA, Kind: VMMetricKindMachine, Timestamp: base, MemBytes: 1},
		{AccountID: accountB, HostID: hostID, VMID: vmB, Kind: VMMetricKindMachine, Timestamp: base, MemBytes: 2},
	}
	require.NoError(t, pg.InsertVMMetrics(ctx, pts))

	// Account A querying VM A: 1 row.
	gotA, err := pg.QueryVMMetricsRange(ctx, accountA, vmA, VMMetricKindMachine, base, base.Add(time.Second), VMMetricResolution1s)
	require.NoError(t, err)
	require.Len(t, gotA, 1)
	assert.Equal(t, int64(1), gotA[0].MemBytes)

	// Account A trying to read VM B (belongs to account B): 0 rows.
	cross, err := pg.QueryVMMetricsRange(ctx, accountA, vmB, VMMetricKindMachine, base, base.Add(time.Second), VMMetricResolution1s)
	require.NoError(t, err)
	assert.Empty(t, cross, "account A must not see VM B's metrics — denormalized account_id enforces ownership")
}

func TestQueryVMMetricsRange_RejectsUnknownResolution(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()

	_, err := pg.QueryVMMetricsRange(ctx, 1, "00000000-0000-0000-0000-000000000000", VMMetricKindMachine, time.Now(), time.Now(), "5m")
	require.Error(t, err)
}

func TestQueryVMMetricsSince_Cursor(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()
	vmID, accountID, hostID, _ := newVMMetricFixture(t)
	t.Cleanup(func() { _, _ = pg.pool.Exec(ctx, "DELETE FROM vm_metrics_1s WHERE vm_id = $1", vmID) })

	base := time.Now().UTC().Truncate(time.Second)
	var pts []VMMetricPoint
	for i := 0; i < 5; i++ {
		pts = append(pts, VMMetricPoint{
			AccountID: accountID, HostID: hostID, VMID: vmID, Kind: VMMetricKindMachine,
			Timestamp: base.Add(time.Duration(i) * time.Second), MemBytes: int64(i),
		})
	}
	require.NoError(t, pg.InsertVMMetrics(ctx, pts))

	// since == base+1s → expect 3 rows (ts > since means >, not >=).
	got, err := pg.QueryVMMetricsSince(ctx, accountID, vmID, VMMetricKindMachine, base.Add(time.Second), 100)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, int64(2), got[0].MemBytes)
	assert.Equal(t, int64(4), got[2].MemBytes)

	// limit clamps.
	got, err = pg.QueryVMMetricsSince(ctx, accountID, vmID, VMMetricKindMachine, base.Add(-time.Second), 2)
	require.NoError(t, err)
	require.Len(t, got, 2, "limit must clamp")
}

func TestQueryVMMetricsRange_PicksCorrectTable(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()
	vmID, accountID, hostID, _ := newVMMetricFixture(t)
	t.Cleanup(func() {
		_, _ = pg.pool.Exec(ctx, "DELETE FROM vm_metrics_1s WHERE vm_id = $1", vmID)
		_, _ = pg.pool.Exec(ctx, "DELETE FROM vm_metrics_1m WHERE vm_id = $1", vmID)
	})

	// Seed one row in each table at the same minute. The 1s table holds the
	// "raw" sample, the 1m table holds the "aggregated" sample with a
	// distinct sentinel mem_bytes so we can tell them apart on read.
	now := time.Now().UTC().Truncate(time.Minute)
	require.NoError(t, pg.InsertVMMetrics(ctx, []VMMetricPoint{{
		AccountID: accountID, HostID: hostID, VMID: vmID, Kind: VMMetricKindMachine, Timestamp: now, MemBytes: 1,
	}}))
	_, err := pg.pool.Exec(ctx, `
		INSERT INTO vm_metrics_1m (account_id, host_id, vm_id, kind, ts, cpu_pct, mem_bytes, sample_count, finalized)
		VALUES ($1, $2, $3, 'machine', $4, NULL, 999, 60, true)
	`, accountID, hostID, vmID, now)
	require.NoError(t, err)

	got1s, err := pg.QueryVMMetricsRange(ctx, accountID, vmID, VMMetricKindMachine, now, now.Add(time.Minute), VMMetricResolution1s)
	require.NoError(t, err)
	require.Len(t, got1s, 1)
	assert.Equal(t, int64(1), got1s[0].MemBytes, "1s resolution must hit vm_metrics_1s")

	got1m, err := pg.QueryVMMetricsRange(ctx, accountID, vmID, VMMetricKindMachine, now, now.Add(time.Minute), VMMetricResolution1m)
	require.NoError(t, err)
	require.Len(t, got1m, 1)
	assert.Equal(t, int64(999), got1m[0].MemBytes, "1m resolution must hit vm_metrics_1m")
}
