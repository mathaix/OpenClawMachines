package sampler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/vmmetrics/cgroup"
)

// fakePusher records each Push call and lets tests inject errors and
// inspect the points received.
type fakePusher struct {
	mu      sync.Mutex
	pushes  [][]Point
	hostIDs []int
	err     error
}

func (p *fakePusher) Push(_ context.Context, hostID int, points []Point) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.pushes = append(p.pushes, append([]Point(nil), points...))
	p.hostIDs = append(p.hostIDs, hostID)
	return nil
}

func (p *fakePusher) totalPushed() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, b := range p.pushes {
		n += len(b)
	}
	return n
}

func setUnit(t *testing.T, root, unitName string, usageUsec int64, memBytes int64) {
	t.Helper()
	dir := filepath.Join(root, unitName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cpu.stat"),
		[]byte(fmt.Sprintf("usage_usec %d\nuser_usec 0\nsystem_usec 0\n", usageUsec)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.current"),
		[]byte(fmt.Sprintf("%d\n", memBytes)), 0o644); err != nil {
		t.Fatal(err)
	}
}

const machineUUID = "9f3c1234-5678-4abc-9def-0123456789ab"
const machineUnit = "ocm-vm-9f3c1234-5678-4abc-9def-0123456789ab.service"

func newTestSampler(t *testing.T, root string, pusher Pusher) *Sampler {
	t.Helper()
	return New(Options{
		Root:      root,
		HostID:    42,
		Pusher:    pusher,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:       time.Now,
		BufferCap: 100,
	})
}

func TestTick_FirstSample_EmitsNullCPU(t *testing.T) {
	root := t.TempDir()
	setUnit(t, root, machineUnit, 1_000_000, 100)

	pusher := &fakePusher{}
	s := newTestSampler(t, root, pusher)
	s.discover()

	now := time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)
	s.tick(now)

	if got := s.bufferedLen(); got != 1 {
		t.Fatalf("buffered = %d, want 1", got)
	}
	p := s.buffer[0]
	if p.CPUPct != nil {
		t.Errorf("first sample CPUPct = %v, want nil", *p.CPUPct)
	}
	if p.MemBytes != 100 {
		t.Errorf("MemBytes = %d, want 100", p.MemBytes)
	}
	if p.VMID != machineUUID {
		t.Errorf("VMID = %q, want %q", p.VMID, machineUUID)
	}
	if p.Kind != cgroup.KindMachine {
		t.Errorf("Kind = %q, want machine", p.Kind)
	}
	if !p.Timestamp.Equal(now) {
		t.Errorf("Timestamp = %v, want %v", p.Timestamp, now)
	}
}

func TestTick_SecondSample_ComputesCPU(t *testing.T) {
	root := t.TempDir()
	setUnit(t, root, machineUnit, 0, 100)

	pusher := &fakePusher{}
	s := newTestSampler(t, root, pusher)
	s.discover()

	t0 := time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)
	s.tick(t0)
	// Bump usage by one CPU-second over one wall-second → 100 % CPU.
	setUnit(t, root, machineUnit, 1_000_000, 110)
	s.tick(t0.Add(time.Second))

	if got := s.bufferedLen(); got != 2 {
		t.Fatalf("buffered = %d, want 2", got)
	}
	p := s.buffer[1]
	if p.CPUPct == nil {
		t.Fatal("second sample CPUPct = nil, want non-nil")
	}
	if *p.CPUPct < 99.5 || *p.CPUPct > 100.5 {
		t.Errorf("CPUPct = %v, want ~100", *p.CPUPct)
	}
	if p.MemBytes != 110 {
		t.Errorf("MemBytes = %d, want 110", p.MemBytes)
	}
}

func TestTick_CgroupReset_EmitsNoPoint(t *testing.T) {
	root := t.TempDir()
	setUnit(t, root, machineUnit, 5_000_000, 100)

	pusher := &fakePusher{}
	s := newTestSampler(t, root, pusher)
	s.discover()

	t0 := time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)
	s.tick(t0)

	// Simulate cgroup destroy + same-unit reuse: usage drops to a small value.
	setUnit(t, root, machineUnit, 50, 200)
	s.tick(t0.Add(time.Second))

	// Only the FIRST tick produced a point. The second was a reset → drop.
	if got := s.bufferedLen(); got != 1 {
		t.Fatalf("buffered = %d, want 1 (reset suppressed second point)", got)
	}

	// Third tick should compute against the reset baseline normally.
	setUnit(t, root, machineUnit, 1_000_050, 250)
	s.tick(t0.Add(2 * time.Second))
	if got := s.bufferedLen(); got != 2 {
		t.Fatalf("buffered = %d, want 2", got)
	}
	p := s.buffer[1]
	if p.CPUPct == nil {
		t.Fatal("post-reset CPUPct = nil, want non-nil")
	}
	if *p.CPUPct < 99.0 || *p.CPUPct > 101.0 {
		t.Errorf("post-reset CPUPct = %v, want ~100", *p.CPUPct)
	}
}

func TestDiscover_PrunesDeadUnits(t *testing.T) {
	root := t.TempDir()
	setUnit(t, root, machineUnit, 1_000_000, 100)

	pusher := &fakePusher{}
	s := newTestSampler(t, root, pusher)
	s.discover()
	s.tick(time.Now())

	// prev now has an entry for the unit.
	if _, ok := s.prev[machineUnit]; !ok {
		t.Fatalf("prev missing entry after first sample")
	}

	// VM stops → unit dir disappears.
	if err := os.RemoveAll(filepath.Join(root, machineUnit)); err != nil {
		t.Fatal(err)
	}
	s.discover()

	if _, ok := s.prev[machineUnit]; ok {
		t.Errorf("prev entry not pruned after cgroup disappearance")
	}
	if got := len(s.refs); got != 0 {
		t.Errorf("refs after pruning = %d, want 0", got)
	}
}

func TestFlush_HappyPath(t *testing.T) {
	root := t.TempDir()
	setUnit(t, root, machineUnit, 0, 1)

	pusher := &fakePusher{}
	s := newTestSampler(t, root, pusher)
	s.discover()
	s.tick(time.Now())

	if err := s.flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := pusher.totalPushed(); got != 1 {
		t.Errorf("pushed = %d, want 1", got)
	}
	if s.bufferedLen() != 0 {
		t.Errorf("buffer not drained after successful flush")
	}
	if got := pusher.hostIDs[0]; got != 42 {
		t.Errorf("Push hostID = %d, want 42", got)
	}
}

func TestFlush_RequeueOnError_ClampsToBufferCap(t *testing.T) {
	root := t.TempDir()
	setUnit(t, root, machineUnit, 0, 1)

	pusher := &fakePusher{err: errors.New("boom")}
	s := New(Options{
		Root:      root,
		HostID:    1,
		Pusher:    pusher,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		BufferCap: 3, // tight ceiling so we can observe drop behavior
		Now:       time.Now,
	})
	s.discover()

	// Sample 5 times. Buffer cap is 3 → 2 will be dropped on the way in.
	for i := 0; i < 5; i++ {
		s.tick(time.Now().Add(time.Duration(i) * time.Second))
	}
	preDropped := s.droppedCount()

	if got := s.bufferedLen(); got != 3 {
		t.Fatalf("buffered after over-fill = %d, want 3", got)
	}
	if preDropped != 2 {
		t.Errorf("dropped after over-fill = %d, want 2", preDropped)
	}

	// flush fails → batch is requeued at front; buffer was already empty
	// after drain, so requeue puts the 3 points back.
	err := s.flush(context.Background())
	if err == nil {
		t.Fatal("flush returned nil; want error")
	}
	if got := s.bufferedLen(); got != 3 {
		t.Errorf("buffered after failed flush = %d, want 3 (requeued)", got)
	}
	if got := s.droppedCount(); got != preDropped {
		t.Errorf("dropped jumped after requeue: %d → %d", preDropped, got)
	}
}

func TestFlush_RequeueDropsOldestWhenOverCap(t *testing.T) {
	root := t.TempDir()
	setUnit(t, root, machineUnit, 0, 1)

	pusher := &fakePusher{err: errors.New("boom")}
	s := New(Options{
		Root:      root,
		HostID:    1,
		Pusher:    pusher,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		BufferCap: 3,
		Now:       time.Now,
	})
	s.discover()

	// First batch: 3 points, ts 0..2.
	for i := 0; i < 3; i++ {
		s.tick(time.Date(2026, 4, 29, 0, 0, i, 0, time.UTC))
	}
	if err := s.flush(context.Background()); err == nil {
		t.Fatal("flush returned nil; want error")
	}
	// Now: requeued buffer has the original 3 points.

	// Add 2 more samples → buffer briefly grows past cap; oldest drops.
	for i := 3; i < 5; i++ {
		s.tick(time.Date(2026, 4, 29, 0, 0, i, 0, time.UTC))
	}

	if got := s.bufferedLen(); got != 3 {
		t.Fatalf("buffered = %d, want 3", got)
	}
	// Buffer should now hold ts=2,3,4 (oldest two were dropped).
	if got := s.buffer[0].Timestamp.Second(); got != 2 {
		t.Errorf("oldest buffered Timestamp.Second = %d, want 2 (oldest dropped)", got)
	}
	if got := s.buffer[2].Timestamp.Second(); got != 4 {
		t.Errorf("newest buffered Timestamp.Second = %d, want 4", got)
	}
	if got := s.droppedCount(); got < 2 {
		t.Errorf("dropped = %d, want >= 2", got)
	}
}

func TestAdvanceBackoff_DoublesUpToCap(t *testing.T) {
	s := New(Options{
		Root: t.TempDir(), HostID: 1, Pusher: &fakePusher{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    time.Now,
	})
	t0 := time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)
	want := []time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second, 60 * time.Second, 60 * time.Second,
	}
	for i, w := range want {
		s.advanceBackoff(t0)
		if s.backoff != w {
			t.Errorf("step %d: backoff = %v, want %v", i, s.backoff, w)
		}
	}
	s.resetBackoff()
	if s.backoff != 0 || !s.backoffUntil.IsZero() {
		t.Errorf("resetBackoff didn't clear: backoff=%v until=%v", s.backoff, s.backoffUntil)
	}
}

func TestRun_CleanShutdownFlushesBuffer(t *testing.T) {
	root := t.TempDir()
	setUnit(t, root, machineUnit, 0, 1)

	pusher := &fakePusher{}
	s := New(Options{
		Root:         root,
		HostID:       7,
		Pusher:       pusher,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tick:         5 * time.Millisecond,
		DiscoverTick: 50 * time.Millisecond,
		BatchEvery:   time.Hour, // never fires; force final-flush path
		BufferCap:    1000,
		Now:          time.Now,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Let it sample a few times.
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not exit within 1 s of cancel")
	}

	if got := pusher.totalPushed(); got == 0 {
		t.Errorf("final flush pushed 0 points; want > 0 (clean shutdown should drain)")
	}
}
