package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// Wire format constants. Design rev. 3 specified 64 KiB / 1000 points, but
// with realistic UUIDs + RFC3339 timestamps each point is ~100 bytes, so
// 1000 points needs ~100 KiB. Body cap was bumped to 256 KiB so the count
// cap actually has headroom; the body cap remains defense against abusive
// payloads.
const (
	vmMetricsMaxBodyBytes  = 256 * 1024 // 256 KiB; oversize → 413
	vmMetricsMaxPoints     = 1000       // batch points cap; over → 400
	vmMetricsMaxSkew       = 5 * time.Minute
	vmMetricsRatePerHostHz = 1 // defense-in-depth: 1 req/s per host
)

// agentVMMetricsRequest is the wire body the agent posts.
type agentVMMetricsRequest struct {
	HostID json.Number               `json:"host_id"`
	Points []agentVMMetricsPointWire `json:"points"`
}

type agentVMMetricsPointWire struct {
	VMID     string   `json:"vm_id"`
	Kind     string   `json:"kind"`
	Ts       string   `json:"ts"`      // RFC3339 / RFC3339Nano
	CPUPct   *float32 `json:"cpu_pct"` // nullable
	MemBytes int64    `json:"mem_bytes"`
}

// agentVMMetricsResponse is the JSON returned on a successful batch.
type agentVMMetricsResponse struct {
	Accepted int `json:"accepted"`
	Dropped  int `json:"dropped"` // points dropped due to ownership mismatch
}

// vmMetricsRateLimiter is a per-host token bucket simplified to "one slot,
// reset after 1 / vmMetricsRatePerHostHz seconds." Keyed by host_id (int).
// Concurrency-safe; entries persist for the lifetime of the process — fine
// because the host_id space is small (<<1000) and entries are small.
type vmMetricsRateLimiter struct {
	mu   sync.Mutex
	last map[int]time.Time
}

func newVMMetricsRateLimiter() *vmMetricsRateLimiter {
	return &vmMetricsRateLimiter{last: make(map[int]time.Time)}
}

func (rl *vmMetricsRateLimiter) allow(hostID int, now time.Time) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if t, ok := rl.last[hostID]; ok {
		if now.Sub(t) < time.Second/time.Duration(vmMetricsRatePerHostHz) {
			return false
		}
	}
	rl.last[hostID] = now
	return true
}

// handleAgentVMMetrics ingests a batch of VM resource samples from a host
// agent. Auth: heartbeat-style via authenticateAgent (per-host token preferred,
// fleet token works as fallback because every host today provisions without a
// per-host token — see design rev. 3).
//
// Validation policy:
//   - Body shape (parse, size, batch length, host_id) → 400/413
//   - Malformed per-point fields (kind, vm_id, ts parse, mem_bytes, cpu_pct) → 400
//   - Per-point timestamp skew and ownership checks → drop + count
//   - Per-point VM-specific CPU upper bound → drop + count
func (s *Server) handleAgentVMMetrics(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, vmMetricsMaxBodyBytes)

	var req agentVMMetricsRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	hostID, err := strconv.Atoi(strings.Trim(req.HostID.String(), `"`))
	if err != nil || hostID <= 0 {
		writeError(w, http.StatusBadRequest, "host_id is required and must be a positive integer")
		return
	}
	if len(req.Points) == 0 {
		writeError(w, http.StatusBadRequest, "points must be non-empty")
		return
	}
	if len(req.Points) > vmMetricsMaxPoints {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("points exceeds max of %d", vmMetricsMaxPoints))
		return
	}

	// Auth — heartbeat parity. Mismatched token → 401. Fleet-token fallback is
	// allowed because hosts.agent_token IS NULL on the entire current fleet.
	if ok, err := s.authenticateAgent(r.Context(), r, hostID); err != nil || !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	now := time.Now()
	if !s.vmMetricsRateLimiter().allow(hostID, now) {
		writeError(w, http.StatusTooManyRequests, "metrics rate limit exceeded for host")
		return
	}

	// Parse + static-validate every point. Malformed points short-circuit the
	// batch, but timestamp-skewed points are dropped so one expired retry sample
	// cannot poison the host's metrics queue.
	parsed, staticDropped, err := parseAndStaticValidate(req.Points, now)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Per-point ownership check + cpu_pct VM-specific bound.
	insertable, dropped, err := s.resolveOwnershipAndFinalCheck(r.Context(), hostID, parsed)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dropped += staticDropped

	if len(insertable) > 0 {
		if err := s.store.InsertVMMetrics(r.Context(), insertable); err != nil {
			slog.Error("vmmetrics.ingest.insert_failed", "host_id", hostID, "n", len(insertable), "error", err.Error())
			writeError(w, http.StatusInternalServerError, "insert failed")
			return
		}
	}

	if dropped > 0 {
		slog.Info("vmmetrics.ingest.points_dropped", "host_id", hostID, "dropped", dropped, "accepted", len(insertable))
	}
	writeJSON(w, http.StatusOK, agentVMMetricsResponse{Accepted: len(insertable), Dropped: dropped})
}

// parsedPoint carries the wire fields after time parsing and kind canonicalization.
type parsedPoint struct {
	VMID     string
	Kind     store.VMMetricKind
	Ts       time.Time
	CPUPct   *float32
	MemBytes int64
}

// parseAndStaticValidate runs all checks that don't require a DB lookup.
// Malformed points fail the whole batch with a descriptive error. Timestamp
// skew is a per-point freshness check, so skewed points are dropped and counted.
func parseAndStaticValidate(wire []agentVMMetricsPointWire, now time.Time) ([]parsedPoint, int, error) {
	out := make([]parsedPoint, 0, len(wire))
	dropped := 0
	for i, w := range wire {
		kind := store.VMMetricKind(w.Kind)
		if kind != store.VMMetricKindMachine && kind != store.VMMetricKindBrowser {
			return nil, 0, fmt.Errorf("points[%d].kind: must be 'machine' or 'browser'", i)
		}
		if w.VMID == "" {
			return nil, 0, fmt.Errorf("points[%d].vm_id: required", i)
		}
		ts, err := time.Parse(time.RFC3339Nano, w.Ts)
		if err != nil {
			return nil, 0, fmt.Errorf("points[%d].ts: %v", i, err)
		}
		ts = ts.UTC().Truncate(time.Second) // second-precision per design
		skew := ts.Sub(now)
		if skew < -vmMetricsMaxSkew || skew > vmMetricsMaxSkew {
			dropped++
			continue
		}
		if w.MemBytes < 0 {
			return nil, 0, fmt.Errorf("points[%d].mem_bytes: must be >= 0", i)
		}
		if w.CPUPct != nil {
			v := float64(*w.CPUPct)
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, 0, fmt.Errorf("points[%d].cpu_pct: must be finite", i)
			}
			if v < 0 {
				return nil, 0, fmt.Errorf("points[%d].cpu_pct: must be >= 0", i)
			}
			// Upper bound is VM-specific (100 * vcpus * 2); checked after lookup.
		}
		out = append(out, parsedPoint{
			VMID:     w.VMID,
			Kind:     kind,
			Ts:       ts,
			CPUPct:   w.CPUPct,
			MemBytes: w.MemBytes,
		})
	}
	return out, dropped, nil
}

// resolveOwnershipAndFinalCheck looks up each point's VM, drops points whose
// vm_id is not placed on this host_id (the agent might reference a moved or
// deleted VM during transitions), and applies the cpu_pct upper bound based
// on the VM's vcpus.
//
// Caches GetMachine / GetBrowserVM results within the request so a 200-point
// batch about 5 VMs incurs ~5 DB round trips, not 200.
func (s *Server) resolveOwnershipAndFinalCheck(
	ctx context.Context,
	hostID int,
	pts []parsedPoint,
) ([]store.VMMetricPoint, int, error) {
	cache := make(map[string]ownership) // key = kind + ":" + vmID

	out := make([]store.VMMetricPoint, 0, len(pts))
	dropped := 0

	for i, p := range pts {
		key := string(p.Kind) + ":" + p.VMID
		o, ok := cache[key]
		if !ok {
			res, err := s.lookupVMOwnership(ctx, p.VMID, p.Kind, hostID)
			if err != nil {
				return nil, 0, fmt.Errorf("ownership lookup for points[%d]: %w", i, err)
			}
			o = res
			cache[key] = o
		}
		if !o.ok {
			dropped++
			continue
		}

		// VM-specific upper bound: 100 * vcpus * 2 (the *2 is slack for noisy
		// kernel accounting and brief saturation overshoots).
		if p.CPUPct != nil {
			limit := float32(100 * o.vcpus * 2)
			if *p.CPUPct > limit {
				dropped++ // out-of-range points get dropped, not whole-batch fail
				slog.Debug("vmmetrics.ingest.cpu_out_of_range",
					"vm_id", p.VMID, "kind", p.Kind, "vcpus", o.vcpus, "cpu_pct", *p.CPUPct)
				continue
			}
		}

		out = append(out, store.VMMetricPoint{
			AccountID: o.accountID,
			HostID:    hostID,
			VMID:      p.VMID,
			Kind:      p.Kind,
			Timestamp: p.Ts,
			CPUPct:    p.CPUPct,
			MemBytes:  p.MemBytes,
		})
	}
	return out, dropped, nil
}

// ownership is a single shared shape for both the per-request cache entry
// and the lookup return value.
type ownership struct {
	accountID int
	vcpus     int
	ok        bool
}

func (s *Server) lookupVMOwnership(ctx context.Context, vmID string, kind store.VMMetricKind, hostID int) (ownership, error) {
	switch kind {
	case store.VMMetricKindMachine:
		m, err := s.store.GetMachine(ctx, vmID)
		if err != nil {
			return ownership{}, err
		}
		if m == nil || m.HostID == nil || *m.HostID != hostID {
			return ownership{ok: false}, nil
		}
		return ownership{accountID: m.AccountID, vcpus: m.VCPUs, ok: true}, nil

	case store.VMMetricKindBrowser:
		bvm, err := s.store.GetBrowserVM(ctx, vmID)
		if err != nil {
			return ownership{}, err
		}
		if bvm == nil || bvm.HostID == nil || *bvm.HostID != hostID {
			return ownership{ok: false}, nil
		}
		return ownership{accountID: bvm.AccountID, vcpus: bvm.VCPUs, ok: true}, nil

	default:
		return ownership{}, fmt.Errorf("unknown kind: %q", kind)
	}
}

// vmMetricsRateLimiter returns the lazily-initialized per-server limiter.
func (s *Server) vmMetricsRateLimiter() *vmMetricsRateLimiter {
	s.vmMetricsRLOnce.Do(func() { s.vmMetricsRL = newVMMetricsRateLimiter() })
	return s.vmMetricsRL
}
