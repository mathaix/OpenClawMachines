package maint

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SchedulerOptions configures Run. Zero values fall back to design defaults.
type SchedulerOptions struct {
	EnsureInterval     time.Duration // default 1 hour
	DownsampleInterval time.Duration // default 1 minute
	DropInterval       time.Duration // default 1 hour

	DaysAhead               int           // default 3
	DownsampleWatermark     time.Duration // default 90 s
	DownsampleFinalizeAfter time.Duration // default 5 min
	DownsampleLookback      time.Duration // default 10 min

	Logger *slog.Logger // default slog.Default()
	Now    func() time.Time
}

func (o *SchedulerOptions) defaults() {
	if o.EnsureInterval == 0 {
		o.EnsureInterval = time.Hour
	}
	if o.DownsampleInterval == 0 {
		o.DownsampleInterval = time.Minute
	}
	if o.DropInterval == 0 {
		o.DropInterval = time.Hour
	}
	if o.DaysAhead == 0 {
		o.DaysAhead = DefaultDaysAhead
	}
	if o.DownsampleWatermark == 0 {
		o.DownsampleWatermark = DefaultDownsampleWatermark
	}
	if o.DownsampleFinalizeAfter == 0 {
		o.DownsampleFinalizeAfter = DefaultDownsampleFinalizeAfter
	}
	if o.DownsampleLookback == 0 {
		o.DownsampleLookback = 10 * time.Minute
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

// Run blocks until ctx is cancelled, firing the three jobs on their tickers.
// Each job is independently advisory-locked so multiple instances of Run
// (across Cloud Run replicas) cooperate safely.
//
// On startup it runs EnsurePartitions immediately (so a fresh deploy has
// today's partition right away) and then Downsample/Drop on their first tick.
func Run(ctx context.Context, db *pgxpool.Pool, opts SchedulerOptions) {
	opts.defaults()

	// Eager partition-create on startup so ingest can land immediately.
	if err := EnsurePartitions(ctx, db, opts.DaysAhead, opts.Now()); err != nil {
		opts.Logger.Warn("vmmetrics.maint.ensure_partitions_startup", "error", err.Error())
	}

	ensureT := time.NewTicker(opts.EnsureInterval)
	defer ensureT.Stop()
	downsampleT := time.NewTicker(opts.DownsampleInterval)
	defer downsampleT.Stop()
	dropT := time.NewTicker(opts.DropInterval)
	defer dropT.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ensureT.C:
			if err := EnsurePartitions(ctx, db, opts.DaysAhead, opts.Now()); err != nil {
				opts.Logger.Warn("vmmetrics.maint.ensure_partitions", "error", err.Error())
			}

		case <-downsampleT.C:
			rows, err := Downsample1s1m(ctx, db, opts.Now(),
				opts.DownsampleWatermark, opts.DownsampleFinalizeAfter, opts.DownsampleLookback)
			if err != nil {
				opts.Logger.Warn("vmmetrics.maint.downsample", "error", err.Error())
			} else if rows > 0 {
				opts.Logger.Debug("vmmetrics.maint.downsampled", "rows_affected", rows)
			}

		case <-dropT.C:
			dropped, err := DropOldPartitions(ctx, db, opts.Now())
			if err != nil {
				opts.Logger.Warn("vmmetrics.maint.drop_partitions", "error", err.Error())
			} else if len(dropped) > 0 {
				opts.Logger.Info("vmmetrics.maint.dropped_partitions", "names", dropped)
			}
		}
	}
}
