package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// mockMetricsStore lets tests stub GetMachine/GetBrowserVM/Query* without a
// real Postgres. Embeds store.Store so the rest of the surface is unchanged.
type mockMetricsStore struct {
	store.Store
	machine    *store.Machine
	browserVM  *store.BrowserVM
	rangeCalls []rangeCall
	sinceCalls []sinceCall
	rangeOut   []store.VMMetricPoint
	sinceOut   []store.VMMetricPoint
	err        error
}

type rangeCall struct {
	accountID int
	vmID      string
	kind      store.VMMetricKind
	from, to  time.Time
	res       store.VMMetricResolution
}

type sinceCall struct {
	accountID int
	vmID      string
	kind      store.VMMetricKind
	since     time.Time
	limit     int
}

func (m *mockMetricsStore) GetMachine(_ context.Context, _ string) (*store.Machine, error) {
	return m.machine, m.err
}
func (m *mockMetricsStore) GetBrowserVM(_ context.Context, _ string) (*store.BrowserVM, error) {
	return m.browserVM, m.err
}
func (m *mockMetricsStore) QueryVMMetricsRange(
	_ context.Context, accountID int, vmID string, kind store.VMMetricKind,
	from, to time.Time, res store.VMMetricResolution,
) ([]store.VMMetricPoint, error) {
	m.rangeCalls = append(m.rangeCalls, rangeCall{accountID, vmID, kind, from, to, res})
	return m.rangeOut, m.err
}
func (m *mockMetricsStore) QueryVMMetricsSince(
	_ context.Context, accountID int, vmID string, kind store.VMMetricKind,
	since time.Time, limit int,
) ([]store.VMMetricPoint, error) {
	m.sinceCalls = append(m.sinceCalls, sinceCall{accountID, vmID, kind, since, limit})
	return m.sinceOut, m.err
}

// withRouterFor returns an http.Handler that wires the same routing
// machinery a real request would hit, then injects accountID into the
// context (mimics AccountMiddleware) and dispatches to the handler.
func withRouterFor(srv *Server, handler http.HandlerFunc, paramKey, paramVal string, accountID int) http.Handler {
	r := chi.NewRouter()
	r.Get("/{"+paramKey+"}", func(w http.ResponseWriter, req *http.Request) {
		ctx := context.WithValue(req.Context(), accountIDKey, accountID)
		handler(w, req.WithContext(ctx))
	})
	_ = srv // not needed, retained for future expansion
	return r
}

func mkPoint(ts time.Time, cpu *float32, mem int64) store.VMMetricPoint {
	return store.VMMetricPoint{Timestamp: ts, CPUPct: cpu, MemBytes: mem}
}

func TestMachineMetrics_RangeForm_PicksResolutionByDuration(t *testing.T) {
	srv := &Server{}
	hostID := 7
	mock := &mockMetricsStore{
		machine:  &store.Machine{ID: testMachineID, AccountID: testAccountID, HostID: &hostID, VCPUs: 2},
		rangeOut: []store.VMMetricPoint{mkPoint(time.Now().UTC(), nil, 100)},
	}
	srv.store = mock

	now := time.Now().UTC()
	cases := []struct {
		name           string
		from, to       time.Time
		wantResolution store.VMMetricResolution
		wantStr        string
	}{
		{"30 minutes → 1s", now.Add(-30 * time.Minute), now, store.VMMetricResolution1s, "1s"},
		{"61 minutes → 1m", now.Add(-61 * time.Minute), now, store.VMMetricResolution1m, "1m"},
		{"24 hours → 1m", now.Add(-24 * time.Hour), now, store.VMMetricResolution1m, "1m"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mock.rangeCalls = nil
			req := httptest.NewRequest(http.MethodGet,
				"/?from="+c.from.Format(time.RFC3339Nano)+"&to="+c.to.Format(time.RFC3339Nano), nil)
			w := httptest.NewRecorder()
			h := withRouterFor(srv, srv.handleGetMachineMetrics, "id", testMachineID, testAccountID)
			req.URL.Path = "/" + testMachineID
			h.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
			}
			if len(mock.rangeCalls) != 1 {
				t.Fatalf("range calls = %d, want 1", len(mock.rangeCalls))
			}
			if mock.rangeCalls[0].res != c.wantResolution {
				t.Errorf("store called with res = %q, want %q", mock.rangeCalls[0].res, c.wantResolution)
			}
			var resp metricsResponse
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
			if resp.Resolution != c.wantStr {
				t.Errorf("response resolution = %q, want %q", resp.Resolution, c.wantStr)
			}
		})
	}
}

func TestMachineMetrics_CursorForm_DefaultsToLast5Min(t *testing.T) {
	srv := &Server{}
	hostID := 7
	mock := &mockMetricsStore{
		machine: &store.Machine{ID: testMachineID, AccountID: testAccountID, HostID: &hostID},
	}
	srv.store = mock

	req := httptest.NewRequest(http.MethodGet, "/"+testMachineID, nil)
	w := httptest.NewRecorder()
	h := withRouterFor(srv, srv.handleGetMachineMetrics, "id", testMachineID, testAccountID)
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if len(mock.sinceCalls) != 1 {
		t.Fatalf("since calls = %d, want 1 (no params should default to cursor form)", len(mock.sinceCalls))
	}
	gap := time.Since(mock.sinceCalls[0].since)
	if gap < 4*time.Minute || gap > 6*time.Minute {
		t.Errorf("default since gap = %v, want ~5min", gap)
	}
	if mock.sinceCalls[0].limit != metricsCursorMaxPoints {
		t.Errorf("limit = %d, want %d", mock.sinceCalls[0].limit, metricsCursorMaxPoints)
	}
}

func TestMachineMetrics_CursorForm_ExplicitSince(t *testing.T) {
	srv := &Server{}
	hostID := 7
	mock := &mockMetricsStore{
		machine: &store.Machine{ID: testMachineID, AccountID: testAccountID, HostID: &hostID},
	}
	srv.store = mock

	since := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	req := httptest.NewRequest(http.MethodGet, "/"+testMachineID+"?since="+since.Format(time.RFC3339Nano), nil)
	w := httptest.NewRecorder()
	h := withRouterFor(srv, srv.handleGetMachineMetrics, "id", testMachineID, testAccountID)
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !mock.sinceCalls[0].since.Equal(since) {
		t.Errorf("since = %v, want %v", mock.sinceCalls[0].since, since)
	}
}

func TestMachineMetrics_NotInAccount_403(t *testing.T) {
	srv := &Server{}
	hostID := 7
	mock := &mockMetricsStore{
		machine: &store.Machine{ID: testMachineID, AccountID: testAccountID + 99, HostID: &hostID},
	}
	srv.store = mock

	req := httptest.NewRequest(http.MethodGet, "/"+testMachineID, nil)
	w := httptest.NewRecorder()
	h := withRouterFor(srv, srv.handleGetMachineMetrics, "id", testMachineID, testAccountID)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestMachineMetrics_MachineNotFound_404(t *testing.T) {
	srv := &Server{}
	mock := &mockMetricsStore{machine: nil}
	srv.store = mock

	req := httptest.NewRequest(http.MethodGet, "/"+testMachineID, nil)
	w := httptest.NewRecorder()
	h := withRouterFor(srv, srv.handleGetMachineMetrics, "id", testMachineID, testAccountID)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestMachineMetrics_RangeForm_FromBeforeToValidation(t *testing.T) {
	srv := &Server{}
	hostID := 7
	srv.store = &mockMetricsStore{
		machine: &store.Machine{ID: testMachineID, AccountID: testAccountID, HostID: &hostID},
	}

	now := time.Now().UTC()
	url := "/" + testMachineID + "?from=" + now.Format(time.RFC3339Nano) + "&to=" + now.Add(-time.Minute).Format(time.RFC3339Nano)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	h := withRouterFor(srv, srv.handleGetMachineMetrics, "id", testMachineID, testAccountID)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for from > to", w.Code)
	}
}

func TestMachineMetrics_RangeForm_OverMaxDuration_400(t *testing.T) {
	srv := &Server{}
	hostID := 7
	srv.store = &mockMetricsStore{
		machine: &store.Machine{ID: testMachineID, AccountID: testAccountID, HostID: &hostID},
	}

	now := time.Now().UTC()
	url := "/" + testMachineID + "?from=" + now.Add(-8*24*time.Hour).Format(time.RFC3339Nano) + "&to=" + now.Format(time.RFC3339Nano)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	h := withRouterFor(srv, srv.handleGetMachineMetrics, "id", testMachineID, testAccountID)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for >7d range", w.Code)
	}
}

func TestMachineMetrics_RangeForm_BadFromTimestamp_400(t *testing.T) {
	srv := &Server{}
	hostID := 7
	srv.store = &mockMetricsStore{
		machine: &store.Machine{ID: testMachineID, AccountID: testAccountID, HostID: &hostID},
	}

	now := time.Now().UTC()
	url := "/" + testMachineID + "?from=not-a-date&to=" + now.Format(time.RFC3339Nano)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	h := withRouterFor(srv, srv.handleGetMachineMetrics, "id", testMachineID, testAccountID)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestMachineMetrics_ResponseShape(t *testing.T) {
	srv := &Server{}
	hostID := 7
	cpu := float32(42.5)
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	mock := &mockMetricsStore{
		machine: &store.Machine{ID: testMachineID, AccountID: testAccountID, HostID: &hostID},
		sinceOut: []store.VMMetricPoint{
			{Timestamp: now, CPUPct: nil, MemBytes: 100},
			{Timestamp: now.Add(time.Second), CPUPct: &cpu, MemBytes: 110},
		},
	}
	srv.store = mock

	req := httptest.NewRequest(http.MethodGet, "/"+testMachineID, nil)
	w := httptest.NewRecorder()
	h := withRouterFor(srv, srv.handleGetMachineMetrics, "id", testMachineID, testAccountID)
	h.ServeHTTP(w, req)

	var resp metricsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.VMID != testMachineID || resp.Kind != "machine" || resp.Resolution != "1s" {
		t.Errorf("response header fields wrong: %+v", resp)
	}
	if len(resp.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(resp.Points))
	}
	if resp.Points[0].CPUPct != nil {
		t.Errorf("first point cpu_pct = %v, want nil", *resp.Points[0].CPUPct)
	}
	if resp.Points[1].CPUPct == nil || *resp.Points[1].CPUPct != cpu {
		t.Errorf("second point cpu_pct mismatch: %v", resp.Points[1].CPUPct)
	}

	// Verify wire-level null vs number for cpu_pct.
	if !strings.Contains(w.Body.String(), `"cpu_pct":null`) {
		t.Errorf("wire body missing cpu_pct:null for first point: %s", w.Body.String())
	}
}

func TestBrowserVMMetrics_OwnershipCheck(t *testing.T) {
	srv := &Server{}
	hostID := 7
	mock := &mockMetricsStore{
		browserVM: &store.BrowserVM{ID: testBrowserID, AccountID: testAccountID, HostID: &hostID},
	}
	srv.store = mock

	req := httptest.NewRequest(http.MethodGet, "/"+testBrowserID, nil)
	w := httptest.NewRecorder()
	h := withRouterFor(srv, srv.handleGetBrowserVMMetrics, "browserVmId", testBrowserID, testAccountID)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	if len(mock.sinceCalls) != 1 || mock.sinceCalls[0].kind != store.VMMetricKindBrowser {
		t.Errorf("expected one cursor query with kind=browser; got %+v", mock.sinceCalls)
	}
}

func TestBrowserVMMetrics_NotInAccount_403(t *testing.T) {
	srv := &Server{}
	hostID := 7
	mock := &mockMetricsStore{
		browserVM: &store.BrowserVM{ID: testBrowserID, AccountID: testAccountID + 99, HostID: &hostID},
	}
	srv.store = mock

	req := httptest.NewRequest(http.MethodGet, "/"+testBrowserID, nil)
	w := httptest.NewRecorder()
	h := withRouterFor(srv, srv.handleGetBrowserVMMetrics, "browserVmId", testBrowserID, testAccountID)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}
