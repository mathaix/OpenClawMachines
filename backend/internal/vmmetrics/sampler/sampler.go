// Package sampler runs the per-host VM metric sampling loop.
//
// One goroutine per agent process. Three timers:
//   - Sample tick (1 Hz): read each known cgroup, append a Point.
//   - Discover tick (10 s): refresh the cgroup set, prune prev entries
//     whose unit no longer exists.
//   - Batch tick (10 s): drain the buffer and POST to the backend; on
//     non-2xx, requeue with exponential backoff (cap 60 s).
package sampler

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/vmmetrics/cgroup"
)

// Point is one sample produced by the sampler. CPUPct is nullable: nil for
// the first sample after a cgroup is discovered (no prev to delta against).
// Reset cases (cur.UsageUsec < prev.UsageUsec) emit NO point at all.
type Point struct {
	VMID      string
	Kind      cgroup.Kind
	Timestamp time.Time
	CPUPct    *float32
	MemBytes  int64
}

// Pusher sends a batch of points to the backend. Non-nil error → caller
// (Sampler) requeues the batch and applies backoff to subsequent flushes.
type Pusher interface {
	Push(ctx context.Context, hostID int, points []Point) error
}

// Stats is observability surface returned by Sampler.Stats(). Counts are
// monotonic since process start.
type Stats struct {
	Buffered int
	Dropped  int
	KnownVMs int
}

// Options configures a Sampler. Required: Root, HostID, Pusher.
// All time/buffer fields fall back to production defaults if zero.
type Options struct {
	Root         string        // cgroup root, e.g. /sys/fs/cgroup/system.slice
	Tick         time.Duration // 1 s
	DiscoverTick time.Duration // 10 s
	BatchEvery   time.Duration // 10 s
	BufferCap    int           // hard ceiling on un-sent points; oldest dropped beyond this
	HostID       int
	Pusher       Pusher
	Logger       *slog.Logger
	Now          func() time.Time
}

// Sampler is the per-host loop. Construct with New, then call Run.
type Sampler struct {
	opts Options

	mu           sync.Mutex
	prev         map[string]cgroup.Sample // keyed by unit name
	refs         []cgroup.VMRef           // last discovery result
	buffer       []Point
	dropped      int
	backoff      time.Duration
	backoffUntil time.Time
}

// New returns a Sampler with sensible defaults applied.
func New(opts Options) *Sampler {
	if opts.Tick == 0 {
		opts.Tick = time.Second
	}
	if opts.DiscoverTick == 0 {
		opts.DiscoverTick = 10 * time.Second
	}
	if opts.BatchEvery == 0 {
		opts.BatchEvery = 10 * time.Second
	}
	if opts.BufferCap == 0 {
		opts.BufferCap = 30 * 32 // 30s × 32 VMs/host upper bound
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Sampler{
		opts: opts,
		prev: make(map[string]cgroup.Sample),
	}
}

// Run starts the three timers and blocks until ctx is cancelled. On exit it
// makes a best-effort final flush so a clean shutdown loses at most one
// batch's worth of in-flight data.
func (s *Sampler) Run(ctx context.Context) error {
	// Initial discovery so the first sample tick has units to read.
	s.discover()

	sampleTimer := time.NewTicker(s.opts.Tick)
	defer sampleTimer.Stop()
	discoverTimer := time.NewTicker(s.opts.DiscoverTick)
	defer discoverTimer.Stop()
	batchTimer := time.NewTicker(s.opts.BatchEvery)
	defer batchTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			// Best-effort final flush. Use a derived ctx with a small budget
			// so a hung backend doesn't block agent shutdown indefinitely.
			flushCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.flush(flushCtx)
			cancel()
			return ctx.Err()

		case <-sampleTimer.C:
			s.tick(s.opts.Now())

		case <-discoverTimer.C:
			s.discover()

		case <-batchTimer.C:
			now := s.opts.Now()
			if now.Before(s.backoffUntil) {
				continue
			}
			if err := s.flush(ctx); err != nil {
				s.advanceBackoff(now)
				s.opts.Logger.Warn("vmmetrics.sampler.flush_failed",
					"error", err.Error(),
					"backoff_ms", s.backoff.Milliseconds(),
					"buffered", s.bufferedLen(),
					"dropped", s.droppedCount())
			} else {
				s.resetBackoff()
			}
		}
	}
}

// Stats returns a snapshot for observability.
func (s *Sampler) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{
		Buffered: len(s.buffer),
		Dropped:  s.dropped,
		KnownVMs: len(s.refs),
	}
}

// tick samples every known cgroup once. Points with valid deltas are buffered;
// reset cases (cur < prev) emit no point but DO replace prev. First-sample
// cases (no prev) emit a memory-only point with CPUPct=nil.
func (s *Sampler) tick(now time.Time) {
	s.mu.Lock()
	refs := append([]cgroup.VMRef(nil), s.refs...)
	s.mu.Unlock()

	for _, ref := range refs {
		s.sampleOne(ref, now)
	}
}

func (s *Sampler) sampleOne(ref cgroup.VMRef, now time.Time) {
	unitDir := filepath.Join(s.opts.Root, ref.UnitName)
	cur, err := cgroup.ReadSample(unitDir, now)
	if err != nil {
		// Cgroup likely vanished between discovery and now — ignore. The next
		// discover tick will prune it from prev.
		s.opts.Logger.Debug("vmmetrics.sampler.read_skip", "unit", ref.UnitName, "error", err.Error())
		return
	}

	s.mu.Lock()
	prev, hadPrev := s.prev[ref.UnitName]
	s.prev[ref.UnitName] = cur
	s.mu.Unlock()

	if hadPrev && cur.UsageUsec < prev.UsageUsec {
		// Reset: cgroup destroyed and unit name reused. Drop this tick;
		// next tick computes from the new prev we just stored.
		s.opts.Logger.Info("vmmetrics.sampler.cgroup_reset", "unit", ref.UnitName,
			"prev_usage_usec", prev.UsageUsec, "cur_usage_usec", cur.UsageUsec)
		return
	}

	var cpuPct *float32
	if hadPrev {
		if pct, ok := cgroup.CPUPercent(prev, cur); ok {
			cpuPct = &pct
		}
	}
	s.appendPoint(Point{
		VMID:      ref.VMID,
		Kind:      ref.Kind,
		Timestamp: now,
		CPUPct:    cpuPct,
		MemBytes:  cur.MemBytes,
	})
}

// discover refreshes the cgroup set and prunes prev entries whose unit
// is no longer present.
func (s *Sampler) discover() {
	refs, err := cgroup.Discover(s.opts.Root)
	if err != nil {
		s.opts.Logger.Warn("vmmetrics.sampler.discover_failed", "root", s.opts.Root, "error", err.Error())
		return
	}
	known := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		known[r.UnitName] = struct{}{}
	}

	s.mu.Lock()
	for unit := range s.prev {
		if _, ok := known[unit]; !ok {
			delete(s.prev, unit)
		}
	}
	s.refs = refs
	s.mu.Unlock()
}

// appendPoint stores a point in the buffer, dropping the oldest when the
// buffer is full. The drop is counted so callers can see sustained pressure.
func (s *Sampler) appendPoint(p Point) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buffer) >= s.opts.BufferCap {
		s.dropped++
		// Compact the slice so we don't grow the backing array unbounded.
		copy(s.buffer, s.buffer[1:])
		s.buffer = s.buffer[:len(s.buffer)-1]
	}
	s.buffer = append(s.buffer, p)
}

// flush drains the buffer and pushes via the configured Pusher. On error the
// batch is requeued at the front of the buffer (clamped to BufferCap so a
// long backend outage starts dropping rather than growing memory).
func (s *Sampler) flush(ctx context.Context) error {
	s.mu.Lock()
	if len(s.buffer) == 0 {
		s.mu.Unlock()
		return nil
	}
	batch := append([]Point(nil), s.buffer...)
	s.buffer = s.buffer[:0]
	s.mu.Unlock()

	if err := s.opts.Pusher.Push(ctx, s.opts.HostID, batch); err != nil {
		s.requeue(batch)
		return fmt.Errorf("push: %w", err)
	}
	return nil
}

// requeue prepends the failed batch to the buffer, clamping to BufferCap.
// Anything beyond the cap is dropped (oldest first) and counted.
func (s *Sampler) requeue(batch []Point) {
	s.mu.Lock()
	defer s.mu.Unlock()
	merged := make([]Point, 0, len(batch)+len(s.buffer))
	merged = append(merged, batch...)
	merged = append(merged, s.buffer...)
	if len(merged) > s.opts.BufferCap {
		s.dropped += len(merged) - s.opts.BufferCap
		merged = merged[len(merged)-s.opts.BufferCap:]
	}
	s.buffer = merged
}

// advanceBackoff doubles the backoff up to 60 s. Called after a failed flush.
func (s *Sampler) advanceBackoff(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backoff == 0 {
		s.backoff = time.Second
	} else {
		s.backoff *= 2
		if s.backoff > 60*time.Second {
			s.backoff = 60 * time.Second
		}
	}
	s.backoffUntil = now.Add(s.backoff)
}

func (s *Sampler) resetBackoff() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backoff = 0
	s.backoffUntil = time.Time{}
}

func (s *Sampler) bufferedLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buffer)
}

func (s *Sampler) droppedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}
