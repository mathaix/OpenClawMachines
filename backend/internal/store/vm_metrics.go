package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// VMMetricKind discriminates between the two VM kinds. Stored as TEXT
// in the partitioned tables so the access-path index can include it.
type VMMetricKind string

const (
	VMMetricKindMachine VMMetricKind = "machine"
	VMMetricKindBrowser VMMetricKind = "browser"
)

// VMMetricResolution names the table the caller wants to read.
type VMMetricResolution string

const (
	VMMetricResolution1s VMMetricResolution = "1s"
	VMMetricResolution1m VMMetricResolution = "1m"
)

// VMMetricPoint is one sample. Used both at ingest (with AccountID/HostID
// already resolved by the API layer from the placement check) and at read.
type VMMetricPoint struct {
	AccountID int
	HostID    int
	VMID      string
	Kind      VMMetricKind
	Timestamp time.Time
	CPUPct    *float32 // nullable — first sample after a cgroup is discovered has no delta
	MemBytes  int64
}

// ErrUnsupportedKind is returned by store methods when an unknown kind value
// reaches the SQL layer. Callers (API handlers) should validate before this
// fires; this is a safety net.
var ErrUnsupportedKind = errors.New("unsupported vm metric kind")

func (k VMMetricKind) valid() bool {
	return k == VMMetricKindMachine || k == VMMetricKindBrowser
}

// InsertVMMetrics writes a batch of points to vm_metrics_1s. Points must
// already have AccountID and HostID set by the caller. Duplicate
// (vm_id, kind, ts) rows within one batch are silently deduped via
// ON CONFLICT DO NOTHING — this matches the design's "duplicates dropped"
// ingest contract.
func (s *PostgresStore) InsertVMMetrics(ctx context.Context, points []VMMetricPoint) error {
	if len(points) == 0 {
		return nil
	}
	for _, p := range points {
		if !p.Kind.valid() {
			return fmt.Errorf("%w: %q", ErrUnsupportedKind, p.Kind)
		}
	}

	rows := make([][]any, len(points))
	for i, p := range points {
		rows[i] = []any{
			p.AccountID,
			p.HostID,
			p.VMID,
			string(p.Kind),
			p.Timestamp,
			p.CPUPct,
			p.MemBytes,
		}
	}

	// COPY with deduplication: COPY itself doesn't honor ON CONFLICT, so we
	// stage into a temp table and then INSERT … SELECT … ON CONFLICT.
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			CREATE TEMP TABLE vm_metrics_1s_stage (LIKE vm_metrics_1s INCLUDING DEFAULTS)
			ON COMMIT DROP
		`); err != nil {
			return fmt.Errorf("create stage: %w", err)
		}

		copied, err := tx.CopyFrom(
			ctx,
			pgx.Identifier{"vm_metrics_1s_stage"},
			[]string{"account_id", "host_id", "vm_id", "kind", "ts", "cpu_pct", "mem_bytes"},
			pgx.CopyFromRows(rows),
		)
		if err != nil {
			return fmt.Errorf("copy to stage: %w", err)
		}
		if int(copied) != len(points) {
			return fmt.Errorf("copy short: %d of %d", copied, len(points))
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO vm_metrics_1s (account_id, host_id, vm_id, kind, ts, cpu_pct, mem_bytes)
			SELECT account_id, host_id, vm_id, kind, ts, cpu_pct, mem_bytes
			FROM vm_metrics_1s_stage
			ON CONFLICT (vm_id, kind, ts) DO NOTHING
		`); err != nil {
			return fmt.Errorf("insert from stage: %w", err)
		}
		return nil
	})
}

// QueryVMMetricsRange returns points for one VM in [from, to), at the given
// resolution. Filters by accountID — deleted-VM history still authorizes
// because account_id is denormalized on the metrics row.
func (s *PostgresStore) QueryVMMetricsRange(
	ctx context.Context,
	accountID int,
	vmID string,
	kind VMMetricKind,
	from, to time.Time,
	res VMMetricResolution,
) ([]VMMetricPoint, error) {
	if !kind.valid() {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}
	table, err := metricsTableForResolution(res)
	if err != nil {
		return nil, err
	}

	q := fmt.Sprintf(`
		SELECT account_id, host_id, vm_id, kind, ts, cpu_pct, mem_bytes
		FROM %s
		WHERE account_id = $1
		  AND vm_id = $2
		  AND kind = $3
		  AND ts >= $4
		  AND ts < $5
		ORDER BY ts ASC
	`, table)

	rows, err := s.pool.Query(ctx, q, accountID, vmID, string(kind), from, to)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", table, err)
	}
	defer rows.Close()
	return scanVMMetricPoints(rows)
}

// QueryVMMetricsSince returns up to limit 1-second points for one VM with
// ts > since. Used by the live polling cursor. Always 1s resolution because
// the live view shows the most recent ~5 minutes.
func (s *PostgresStore) QueryVMMetricsSince(
	ctx context.Context,
	accountID int,
	vmID string,
	kind VMMetricKind,
	since time.Time,
	limit int,
) ([]VMMetricPoint, error) {
	if !kind.valid() {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}

	rows, err := s.pool.Query(ctx, `
		SELECT account_id, host_id, vm_id, kind, ts, cpu_pct, mem_bytes
		FROM vm_metrics_1s
		WHERE account_id = $1
		  AND vm_id = $2
		  AND kind = $3
		  AND ts > $4
		ORDER BY ts ASC
		LIMIT $5
	`, accountID, vmID, string(kind), since, limit)
	if err != nil {
		return nil, fmt.Errorf("query vm_metrics_1s since: %w", err)
	}
	defer rows.Close()
	return scanVMMetricPoints(rows)
}

func metricsTableForResolution(res VMMetricResolution) (string, error) {
	switch res {
	case VMMetricResolution1s:
		return "vm_metrics_1s", nil
	case VMMetricResolution1m:
		return "vm_metrics_1m", nil
	default:
		return "", fmt.Errorf("unknown resolution: %q", res)
	}
}

func scanVMMetricPoints(rows pgx.Rows) ([]VMMetricPoint, error) {
	var out []VMMetricPoint
	for rows.Next() {
		var p VMMetricPoint
		var kind string
		if err := rows.Scan(
			&p.AccountID,
			&p.HostID,
			&p.VMID,
			&kind,
			&p.Timestamp,
			&p.CPUPct,
			&p.MemBytes,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		p.Kind = VMMetricKind(kind)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}
