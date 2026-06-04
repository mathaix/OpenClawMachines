package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// User-facing read endpoints for VM resource metrics. Account-scoped under
// /api/accounts/{accountId}/. Two query forms:
//
//	?from=<ISO>&to=<ISO>   — range, used by History view
//	?since=<ISO>            — cursor, used by Live view (delta polling)

const (
	metricsRangeMaxDuration = 7 * 24 * time.Hour // hard cap: 7 d
	metrics1sCutoff         = 1 * time.Hour      // ranges ≤ 1 h hit vm_metrics_1s
	metricsLiveDefaultSpan  = 5 * time.Minute    // cursor with no `since` → last 5 min
	metricsCursorMaxPoints  = 1000               // matches Store.QueryVMMetricsSince cap
)

// metricsResponse is the JSON shape returned by both range and cursor reads.
type metricsResponse struct {
	VMID       string         `json:"vm_id"`
	Kind       string         `json:"kind"`
	Resolution string         `json:"resolution"`
	From       time.Time      `json:"from"`
	To         time.Time      `json:"to"`
	Points     []metricsPoint `json:"points"`
}

type metricsPoint struct {
	Timestamp time.Time `json:"ts"`
	CPUPct    *float32  `json:"cpu_pct"`
	MemBytes  int64     `json:"mem_bytes"`
}

// errVMNotInAccount is returned by the resolver helpers when the VM lookup
// finds a row but it doesn't belong to the calling account.
var errVMNotInAccount = errors.New("vm does not belong to account")

// handleGetMachineMetrics serves metrics for an OpenClaw machine the caller
// owns. Ownership is checked via account_id on the metrics row (denormalized
// at ingest), so deleted-machine history is still returnable to the owning
// account within the retention window.
func (s *Server) handleGetMachineMetrics(w http.ResponseWriter, r *http.Request) {
	s.serveVMMetrics(w, r, store.VMMetricKindMachine, chi.URLParam(r, "id"))
}

// handleGetBrowserVMMetrics — same shape, browser-VM ownership.
func (s *Server) handleGetBrowserVMMetrics(w http.ResponseWriter, r *http.Request) {
	s.serveVMMetrics(w, r, store.VMMetricKindBrowser, chi.URLParam(r, "browserVmId"))
}

func (s *Server) serveVMMetrics(w http.ResponseWriter, r *http.Request, kind store.VMMetricKind, vmID string) {
	accountID := accountIDFromContext(r.Context())
	if accountID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if vmID == "" {
		writeError(w, http.StatusBadRequest, "vm id required")
		return
	}

	if err := s.assertVMInAccount(r.Context(), kind, vmID, accountID); err != nil {
		switch {
		case errors.Is(err, errVMNotInAccount):
			writeError(w, http.StatusForbidden, "vm does not belong to this account")
		default:
			// VM not found OR a real DB error. Return 404 — distinguishing the
			// two would either leak existence (per-account) or duplicate the
			// information already implied by 403 above.
			writeError(w, http.StatusNotFound, "vm not found")
		}
		return
	}

	q := r.URL.Query()
	switch {
	case q.Has("since"):
		s.serveVMMetricsCursor(w, r, accountID, kind, vmID, q.Get("since"))
	case q.Has("from") || q.Has("to"):
		s.serveVMMetricsRange(w, r, accountID, kind, vmID, q.Get("from"), q.Get("to"))
	default:
		// No params: live default — last 5 min via cursor form, since = now-5m.
		s.serveVMMetricsCursor(w, r, accountID, kind, vmID, "")
	}
}

func (s *Server) serveVMMetricsRange(
	w http.ResponseWriter, r *http.Request,
	accountID int, kind store.VMMetricKind, vmID string,
	fromStr, toStr string,
) {
	if fromStr == "" || toStr == "" {
		writeError(w, http.StatusBadRequest, "from and to are both required for range queries")
		return
	}
	from, err := time.Parse(time.RFC3339Nano, fromStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "from: "+err.Error())
		return
	}
	to, err := time.Parse(time.RFC3339Nano, toStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "to: "+err.Error())
		return
	}
	if !from.Before(to) {
		writeError(w, http.StatusBadRequest, "from must be before to")
		return
	}
	span := to.Sub(from)
	if span > metricsRangeMaxDuration {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("range exceeds max of %v", metricsRangeMaxDuration))
		return
	}

	res := store.VMMetricResolution1s
	resStr := "1s"
	if span > metrics1sCutoff {
		res = store.VMMetricResolution1m
		resStr = "1m"
	}

	pts, err := s.store.QueryVMMetricsRange(r.Context(), accountID, vmID, kind, from, to, res)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	writeJSON(w, http.StatusOK, metricsResponse{
		VMID:       vmID,
		Kind:       string(kind),
		Resolution: resStr,
		From:       from.UTC(),
		To:         to.UTC(),
		Points:     toMetricsPoints(pts),
	})
}

func (s *Server) serveVMMetricsCursor(
	w http.ResponseWriter, r *http.Request,
	accountID int, kind store.VMMetricKind, vmID string,
	sinceStr string,
) {
	now := time.Now().UTC()
	var since time.Time
	if sinceStr == "" {
		// Default cursor seed = now - 5min so a fresh-tab Live view gets the
		// recent context immediately.
		since = now.Add(-metricsLiveDefaultSpan)
	} else {
		t, err := time.Parse(time.RFC3339Nano, sinceStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since: "+err.Error())
			return
		}
		since = t.UTC()
	}

	pts, err := s.store.QueryVMMetricsSince(r.Context(), accountID, vmID, kind, since, metricsCursorMaxPoints)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	writeJSON(w, http.StatusOK, metricsResponse{
		VMID:       vmID,
		Kind:       string(kind),
		Resolution: "1s",
		From:       since,
		To:         now,
		Points:     toMetricsPoints(pts),
	})
}

func (s *Server) assertVMInAccount(ctx context.Context, kind store.VMMetricKind, vmID string, accountID int) error {
	switch kind {
	case store.VMMetricKindMachine:
		m, err := s.store.GetMachine(ctx, vmID)
		if err != nil {
			return err
		}
		if m == nil {
			return fmt.Errorf("machine not found")
		}
		if m.AccountID != accountID {
			return errVMNotInAccount
		}
		return nil

	case store.VMMetricKindBrowser:
		b, err := s.store.GetBrowserVM(ctx, vmID)
		if err != nil {
			return err
		}
		if b == nil {
			return fmt.Errorf("browser vm not found")
		}
		if b.AccountID != accountID {
			return errVMNotInAccount
		}
		return nil

	default:
		return fmt.Errorf("unknown kind: %q", kind)
	}
}

func toMetricsPoints(pts []store.VMMetricPoint) []metricsPoint {
	out := make([]metricsPoint, len(pts))
	for i, p := range pts {
		out[i] = metricsPoint{
			Timestamp: p.Timestamp.UTC(),
			CPUPct:    p.CPUPct,
			MemBytes:  p.MemBytes,
		}
	}
	return out
}
