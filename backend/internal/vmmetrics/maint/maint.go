package maint

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Advisory lock keys. ASCII-encoded so they're greppable in pg_locks output.
//
//	0x564D5F455053  "VM_EPS"  EnsurePartitions
//	0x564D5F4453    "VM_DS"   Downsample1s1m
//	0x564D5F4452    "VM_DR"   DropOldPartitions
//
// All fit comfortably in signed int64 (Postgres bigint), required by
// pg_try_advisory_xact_lock.
const (
	LockEnsurePartitions int64 = 0x564D5F455053
	LockDownsample1s1m   int64 = 0x564D5F4453
	LockDropPartitions   int64 = 0x564D5F4452
)

// Default windows used by the scheduler. Exposed so tests can override.
const (
	DefaultDaysAhead               = 3
	DefaultDownsampleWatermark     = 90 * time.Second
	DefaultDownsampleFinalizeAfter = 5 * time.Minute
	DefaultRetention1s             = 24 * time.Hour
	DefaultRetention1m             = 7 * 24 * time.Hour
)

// runWithLock executes job inside a transaction holding a transaction-scoped
// advisory lock keyed by `key`. If another instance already holds the lock,
// returns nil without running. Transaction-scoped locks auto-release on
// commit/rollback, which composes cleanly with pgxpool (session-scoped locks
// don't, because acquire and release can land on different connections).
//
// `ranIt` is true if this caller actually ran job; false if the lock was
// held elsewhere. Useful for tests + observability.
func runWithLock(
	ctx context.Context,
	db *pgxpool.Pool,
	key int64,
	job func(context.Context, pgx.Tx) error,
) (ranIt bool, err error) {
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var got bool
		if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", key).Scan(&got); err != nil {
			return fmt.Errorf("acquire advisory lock %d: %w", key, err)
		}
		if !got {
			return nil
		}
		ranIt = true
		return job(ctx, tx)
	})
	return ranIt, err
}

// EnsurePartitions creates partitions for [today, today + daysAhead] on both
// vm_metrics_1s and vm_metrics_1m. Idempotent (CREATE TABLE IF NOT EXISTS).
// Held inside an advisory lock so multiple instances can safely race-call it.
func EnsurePartitions(ctx context.Context, db *pgxpool.Pool, daysAhead int, now time.Time) error {
	if daysAhead < 0 {
		return fmt.Errorf("daysAhead must be >= 0, got %d", daysAhead)
	}
	_, err := runWithLock(ctx, db, LockEnsurePartitions, func(ctx context.Context, tx pgx.Tx) error {
		today := now.UTC().Truncate(24 * time.Hour)
		for i := 0; i <= daysAhead; i++ {
			d := today.AddDate(0, 0, i)
			start, end := dayBounds(d)
			for _, table := range []string{"vm_metrics_1s", "vm_metrics_1m"} {
				partName := partitionName(table, d)
				// Postgres rejects parameterized values in PARTITION OF
				// FOR VALUES FROM ($1) TO ($2) — the bounds must be
				// literals visible at parse time. Migration 084 uses
				// PL/pgSQL format(%L); we inline the timestamp here.
				// Safe: timestamps come from dayBounds() and only contain
				// digits / dashes / colons / 'T' / 'Z', none of which
				// terminate a quoted SQL literal.
				ddl := fmt.Sprintf(
					`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
					(pgx.Identifier{partName}).Sanitize(),
					(pgx.Identifier{table}).Sanitize(),
					start.Format(time.RFC3339),
					end.Format(time.RFC3339),
				)
				if _, err := tx.Exec(ctx, ddl); err != nil {
					return fmt.Errorf("create partition %s: %w", partName, err)
				}
			}
		}
		return nil
	})
	return err
}

// Downsample1s1m aggregates 1-second buckets in vm_metrics_1s into 1-minute
// buckets in vm_metrics_1m. Re-runnable: ON CONFLICT … DO UPDATE refreshes
// unfinalized buckets with new sample data; finalized buckets are skipped.
//
// A bucket is finalized either when sample_count >= 60 (full minute observed)
// or when the bucket end is older than (now - finalizeAfter). The watermark
// excludes buckets too recent to safely finalize, since the agent's worst-case
// retry-with-backoff window is ~60s.
func Downsample1s1m(
	ctx context.Context,
	db *pgxpool.Pool,
	now time.Time,
	watermark, finalizeAfter, lookback time.Duration,
) (int64, error) {
	if lookback < watermark {
		return 0, fmt.Errorf("lookback (%v) must be >= watermark (%v)", lookback, watermark)
	}
	var rowsAffected int64
	_, err := runWithLock(ctx, db, LockDownsample1s1m, func(ctx context.Context, tx pgx.Tx) error {
		windowEnd := now.Add(-watermark)
		windowStart := now.Add(-lookback)

		tag, err := tx.Exec(ctx, `
			INSERT INTO vm_metrics_1m
			    (account_id, host_id, vm_id, kind, ts, cpu_pct, mem_bytes, sample_count, finalized)
			SELECT
			    MIN(account_id),
			    (array_agg(host_id ORDER BY ts DESC))[1],
			    vm_id,
			    kind,
			    date_trunc('minute', ts) AS bucket_ts,
			    AVG(cpu_pct)::real,
			    AVG(mem_bytes)::bigint,
			    COUNT(*)::int,
			    (COUNT(*) >= 60 OR (date_trunc('minute', ts) + interval '1 minute') < ($4::timestamptz - $3::interval))
			FROM vm_metrics_1s
			WHERE ts >= $1 AND ts < $2
			GROUP BY vm_id, kind, date_trunc('minute', ts)
			ON CONFLICT (vm_id, kind, ts) DO UPDATE SET
			    cpu_pct      = EXCLUDED.cpu_pct,
			    mem_bytes    = EXCLUDED.mem_bytes,
			    sample_count = EXCLUDED.sample_count,
			    host_id      = EXCLUDED.host_id,
			    finalized    = EXCLUDED.finalized
			WHERE NOT vm_metrics_1m.finalized
		`, windowStart, windowEnd, finalizeAfter, now)
		if err != nil {
			return fmt.Errorf("downsample 1s→1m: %w", err)
		}
		rowsAffected = tag.RowsAffected()
		return nil
	})
	return rowsAffected, err
}

// DropOldPartitions drops partitions whose covered date is older than the
// per-table retention. Returns the names of dropped partitions.
//
// Retention is "daily-granular" by design (rev. 3 risks section): a 1s row
// can survive up to ~48 h before its partition's nominal date crosses the
// retention threshold. That's acceptable Phase 1.
func DropOldPartitions(ctx context.Context, db *pgxpool.Pool, now time.Time) ([]string, error) {
	var dropped []string
	_, err := runWithLock(ctx, db, LockDropPartitions, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT c.relname AS child_name, p.relname AS parent_name
			FROM pg_inherits i
			JOIN pg_class c ON c.oid = i.inhrelid
			JOIN pg_class p ON p.oid = i.inhparent
			WHERE p.relname IN ('vm_metrics_1s', 'vm_metrics_1m')
		`)
		if err != nil {
			return fmt.Errorf("list partitions: %w", err)
		}
		type victim struct{ child, parent string }
		var victims []victim
		for rows.Next() {
			var child, parent string
			if err := rows.Scan(&child, &parent); err != nil {
				rows.Close()
				return fmt.Errorf("scan partition row: %w", err)
			}
			date, ok := parsePartitionDate(child)
			if !ok {
				continue // unparseable suffix; ignore (could be a dev artifact)
			}
			retention := DefaultRetention1m
			if parent == "vm_metrics_1s" {
				retention = DefaultRetention1s
			}
			// Partition end (exclusive). Drop if even its end has crossed retention.
			_, partitionEnd := dayBounds(date)
			if partitionEnd.Before(now.UTC().Add(-retention)) {
				victims = append(victims, victim{child, parent})
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate partition rows: %w", err)
		}

		for _, v := range victims {
			ddl := fmt.Sprintf("DROP TABLE IF EXISTS %s", (pgx.Identifier{v.child}).Sanitize())
			if _, err := tx.Exec(ctx, ddl); err != nil {
				return fmt.Errorf("drop %s: %w", v.child, err)
			}
			dropped = append(dropped, v.child)
		}
		return nil
	})
	return dropped, err
}
