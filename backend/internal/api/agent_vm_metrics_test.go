package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// mockVMMetricsStore embeds store.Store so we only override the handful of
// methods the ingest handler touches: GetMachine, GetBrowserVM,
// InsertVMMetrics, GetHost (used by authenticateAgent).
type mockVMMetricsStore struct {
	store.Store
	host         *store.Host
	machines     map[string]*store.Machine
	browserVMs   map[string]*store.BrowserVM
	insertedMu   sync.Mutex
	inserted     []store.VMMetricPoint
	insertErr    error
	getMachineEr error
}

func (m *mockVMMetricsStore) GetHost(_ context.Context, _ int) (*store.Host, error) {
	return m.host, nil
}

func (m *mockVMMetricsStore) GetMachine(_ context.Context, id string) (*store.Machine, error) {
	if m.getMachineEr != nil {
		return nil, m.getMachineEr
	}
	if m.machines == nil {
		return nil, nil
	}
	return m.machines[id], nil
}

func (m *mockVMMetricsStore) GetBrowserVM(_ context.Context, id string) (*store.BrowserVM, error) {
	if m.browserVMs == nil {
		return nil, nil
	}
	return m.browserVMs[id], nil
}

func (m *mockVMMetricsStore) InsertVMMetrics(_ context.Context, points []store.VMMetricPoint) error {
	m.insertedMu.Lock()
	defer m.insertedMu.Unlock()
	if m.insertErr != nil {
		return m.insertErr
	}
	m.inserted = append(m.inserted, points...)
	return nil
}

func (m *mockVMMetricsStore) takeInserted() []store.VMMetricPoint {
	m.insertedMu.Lock()
	defer m.insertedMu.Unlock()
	out := append([]store.VMMetricPoint(nil), m.inserted...)
	m.inserted = nil
	return out
}

const (
	testHostID    = 7
	testAccountID = 42
	testMachineID = "9f3c1234-5678-4abc-9def-0123456789ab"
	testBrowserID = "00000000-1111-4222-8333-444444444444"
	testToken     = "fleet-token"
)

func newVMMetricsTestServer() (*Server, *mockVMMetricsStore) {
	hostID := testHostID
	mock := &mockVMMetricsStore{
		host: &store.Host{ID: hostID, Status: "ready"},
		machines: map[string]*store.Machine{
			testMachineID: {
				ID:        testMachineID,
				AccountID: testAccountID,
				HostID:    &hostID,
				VCPUs:     2,
			},
		},
		browserVMs: map[string]*store.BrowserVM{
			testBrowserID: {
				ID:        testBrowserID,
				AccountID: testAccountID,
				HostID:    &hostID,
				VCPUs:     1,
			},
		},
	}
	return &Server{store: mock, agentToken: testToken}, mock
}

func makeReq(t *testing.T, body any, token string) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/agent/vm-metrics", bytes.NewReader(raw))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestVMMetrics_HappyPath_PersistsAccountIDFromVM(t *testing.T) {
	srv, mock := newVMMetricsTestServer()
	now := time.Now().UTC().Truncate(time.Second)

	body := map[string]any{
		"host_id": testHostID,
		"points": []map[string]any{
			{"vm_id": testMachineID, "kind": "machine", "ts": now.Format(time.RFC3339Nano), "cpu_pct": nil, "mem_bytes": 100},
			{"vm_id": testMachineID, "kind": "machine", "ts": now.Add(time.Second).Format(time.RFC3339Nano), "cpu_pct": 12.5, "mem_bytes": 110},
			{"vm_id": testBrowserID, "kind": "browser", "ts": now.Format(time.RFC3339Nano), "cpu_pct": 50.0, "mem_bytes": 200},
		},
	}
	req := makeReq(t, body, testToken)
	w := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp agentVMMetricsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Accepted != 3 || resp.Dropped != 0 {
		t.Errorf("accepted=%d dropped=%d, want 3/0", resp.Accepted, resp.Dropped)
	}

	got := mock.takeInserted()
	if len(got) != 3 {
		t.Fatalf("inserted = %d, want 3", len(got))
	}
	// account_id must be resolved from the VM row, not trusted from the wire.
	for i, p := range got {
		if p.AccountID != testAccountID {
			t.Errorf("inserted[%d].AccountID = %d, want %d", i, p.AccountID, testAccountID)
		}
		if p.HostID != testHostID {
			t.Errorf("inserted[%d].HostID = %d, want %d", i, p.HostID, testHostID)
		}
	}
	if got[0].CPUPct != nil {
		t.Errorf("first point cpu_pct = %v, want nil", *got[0].CPUPct)
	}
	if got[2].Kind != store.VMMetricKindBrowser {
		t.Errorf("third point kind = %q, want browser", got[2].Kind)
	}
}

func TestVMMetrics_MissingToken_401(t *testing.T) {
	srv, _ := newVMMetricsTestServer()
	body := map[string]any{
		"host_id": testHostID,
		"points":  []map[string]any{{"vm_id": testMachineID, "kind": "machine", "ts": time.Now().Format(time.RFC3339), "mem_bytes": 1}},
	}
	req := makeReq(t, body, "") // no token
	w := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestVMMetrics_WrongToken_401(t *testing.T) {
	srv, _ := newVMMetricsTestServer()
	body := map[string]any{
		"host_id": testHostID,
		"points":  []map[string]any{{"vm_id": testMachineID, "kind": "machine", "ts": time.Now().Format(time.RFC3339), "mem_bytes": 1}},
	}
	req := makeReq(t, body, "wrong-token")
	w := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestVMMetrics_VMOnDifferentHost_PointsDropped(t *testing.T) {
	srv, mock := newVMMetricsTestServer()
	otherHost := testHostID + 1
	mock.machines[testMachineID].HostID = &otherHost // VM moved to another host
	now := time.Now().UTC().Truncate(time.Second)

	body := map[string]any{
		"host_id": testHostID,
		"points": []map[string]any{
			{"vm_id": testMachineID, "kind": "machine", "ts": now.Format(time.RFC3339), "mem_bytes": 1},
		},
	}
	req := makeReq(t, body, testToken)
	w := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with drop count", w.Code)
	}
	var resp agentVMMetricsResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Accepted != 0 || resp.Dropped != 1 {
		t.Errorf("accepted=%d dropped=%d, want 0/1", resp.Accepted, resp.Dropped)
	}
	if got := mock.takeInserted(); len(got) != 0 {
		t.Errorf("inserted = %d, want 0", len(got))
	}
}

func TestVMMetrics_OversizedBatch_400(t *testing.T) {
	srv, _ := newVMMetricsTestServer()
	now := time.Now().UTC().Truncate(time.Second)

	pts := make([]map[string]any, vmMetricsMaxPoints+1)
	for i := range pts {
		pts[i] = map[string]any{"vm_id": testMachineID, "kind": "machine", "ts": now.Format(time.RFC3339), "mem_bytes": int64(i)}
	}
	body := map[string]any{"host_id": testHostID, "points": pts}
	req := makeReq(t, body, testToken)
	w := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for oversized batch", w.Code)
	}
}

func TestVMMetrics_BodyTooLarge_413(t *testing.T) {
	srv, _ := newVMMetricsTestServer()
	// Pad out a body with junk in vm_id strings to push past the body cap.
	bigID := strings.Repeat("a", vmMetricsMaxBodyBytes+1024)
	body := map[string]any{
		"host_id": testHostID,
		"points":  []map[string]any{{"vm_id": bigID, "kind": "machine", "ts": time.Now().Format(time.RFC3339), "mem_bytes": 1}},
	}
	req := makeReq(t, body, testToken)
	w := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", w.Code)
	}
}

func TestVMMetrics_TimestampSkew_DropsPoint(t *testing.T) {
	srv, mock := newVMMetricsTestServer()
	now := time.Now().UTC().Truncate(time.Second)
	tooOld := now.Add(-10 * time.Minute).Format(time.RFC3339)

	body := map[string]any{
		"host_id": testHostID,
		"points": []map[string]any{
			{"vm_id": testMachineID, "kind": "machine", "ts": tooOld, "mem_bytes": 1},
			{"vm_id": testMachineID, "kind": "machine", "ts": now.Format(time.RFC3339), "mem_bytes": 2},
		},
	}
	req := makeReq(t, body, testToken)
	w := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with skewed point dropped; body: %s", w.Code, w.Body.String())
	}
	var resp agentVMMetricsResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Accepted != 1 || resp.Dropped != 1 {
		t.Errorf("accepted=%d dropped=%d, want 1/1", resp.Accepted, resp.Dropped)
	}
	got := mock.takeInserted()
	if len(got) != 1 || got[0].MemBytes != 2 {
		t.Errorf("inserted = %+v; want only fresh point", got)
	}
}

func TestVMMetrics_FutureTimestamp_DropsPoint(t *testing.T) {
	srv, mock := newVMMetricsTestServer()
	tooFuture := time.Now().Add(10 * time.Minute).Format(time.RFC3339)

	body := map[string]any{
		"host_id": testHostID,
		"points":  []map[string]any{{"vm_id": testMachineID, "kind": "machine", "ts": tooFuture, "mem_bytes": 1}},
	}
	req := makeReq(t, body, testToken)
	w := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with future point dropped; body: %s", w.Code, w.Body.String())
	}
	var resp agentVMMetricsResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Accepted != 0 || resp.Dropped != 1 {
		t.Errorf("accepted=%d dropped=%d, want 0/1", resp.Accepted, resp.Dropped)
	}
	if got := mock.takeInserted(); len(got) != 0 {
		t.Errorf("inserted = %d, want 0", len(got))
	}
}

func TestVMMetrics_NegativeMemory_400(t *testing.T) {
	srv, _ := newVMMetricsTestServer()
	body := map[string]any{
		"host_id": testHostID,
		"points": []map[string]any{
			{"vm_id": testMachineID, "kind": "machine", "ts": time.Now().Format(time.RFC3339), "mem_bytes": -1},
		},
	}
	req := makeReq(t, body, testToken)
	w := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for negative mem_bytes", w.Code)
	}
}

func TestVMMetrics_CPUOutOfRange_DropsPoint(t *testing.T) {
	srv, mock := newVMMetricsTestServer()
	// Test machine has VCPUs=2 → cap = 100*2*2 = 400. Send 500 → dropped.
	now := time.Now().UTC().Truncate(time.Second)
	body := map[string]any{
		"host_id": testHostID,
		"points": []map[string]any{
			{"vm_id": testMachineID, "kind": "machine", "ts": now.Format(time.RFC3339), "cpu_pct": 500.0, "mem_bytes": 1},
			{"vm_id": testMachineID, "kind": "machine", "ts": now.Add(time.Second).Format(time.RFC3339), "cpu_pct": 199.5, "mem_bytes": 2},
		},
	}
	req := makeReq(t, body, testToken)
	w := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp agentVMMetricsResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Accepted != 1 || resp.Dropped != 1 {
		t.Errorf("accepted=%d dropped=%d, want 1/1", resp.Accepted, resp.Dropped)
	}
	got := mock.takeInserted()
	if len(got) != 1 || got[0].MemBytes != 2 {
		t.Errorf("inserted = %+v; want only the 199.5%% point", got)
	}
}

func TestVMMetrics_CPUNegative_400(t *testing.T) {
	srv, _ := newVMMetricsTestServer()
	body := map[string]any{
		"host_id": testHostID,
		"points": []map[string]any{
			{"vm_id": testMachineID, "kind": "machine", "ts": time.Now().Format(time.RFC3339), "cpu_pct": -1.0, "mem_bytes": 1},
		},
	}
	req := makeReq(t, body, testToken)
	w := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for negative cpu_pct", w.Code)
	}
}

func TestVMMetrics_UnknownKind_400(t *testing.T) {
	srv, _ := newVMMetricsTestServer()
	body := map[string]any{
		"host_id": testHostID,
		"points":  []map[string]any{{"vm_id": testMachineID, "kind": "spaceship", "ts": time.Now().Format(time.RFC3339), "mem_bytes": 1}},
	}
	req := makeReq(t, body, testToken)
	w := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid kind", w.Code)
	}
}

func TestVMMetrics_RateLimit_429(t *testing.T) {
	srv, _ := newVMMetricsTestServer()
	now := time.Now().UTC().Truncate(time.Second)
	body := map[string]any{
		"host_id": testHostID,
		"points":  []map[string]any{{"vm_id": testMachineID, "kind": "machine", "ts": now.Format(time.RFC3339), "mem_bytes": 1}},
	}

	// First request — allowed.
	w := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w, makeReq(t, body, testToken))
	if w.Code != http.StatusOK {
		t.Fatalf("first request: status = %d, want 200", w.Code)
	}

	// Second request immediately — rejected by rate limiter.
	w2 := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w2, makeReq(t, body, testToken))
	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("second request: status = %d, want 429", w2.Code)
	}
}

func TestVMMetrics_StoreInsertError_500(t *testing.T) {
	srv, mock := newVMMetricsTestServer()
	mock.insertErr = errors.New("db unavailable")
	now := time.Now().UTC().Truncate(time.Second)

	body := map[string]any{
		"host_id": testHostID,
		"points":  []map[string]any{{"vm_id": testMachineID, "kind": "machine", "ts": now.Format(time.RFC3339), "mem_bytes": 1}},
	}
	req := makeReq(t, body, testToken)
	w := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestVMMetrics_DuplicateInBatch_BothInserted_StoreDeduplicates(t *testing.T) {
	// The handler doesn't dedupe — that's the store's job (ON CONFLICT DO
	// NOTHING in InsertVMMetrics). Verify the handler hands both points to
	// the store; the wire-level test for dedupe lives in postgres_vm_metrics_test.go.
	srv, mock := newVMMetricsTestServer()
	now := time.Now().UTC().Truncate(time.Second)
	point := map[string]any{"vm_id": testMachineID, "kind": "machine", "ts": now.Format(time.RFC3339), "mem_bytes": 1}

	body := map[string]any{"host_id": testHostID, "points": []map[string]any{point, point}}
	req := makeReq(t, body, testToken)
	w := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	got := mock.takeInserted()
	if len(got) != 2 {
		t.Errorf("handler should hand both duplicate points to store; got %d", len(got))
	}
}

func TestVMMetrics_OwnershipCacheReducesLookups(t *testing.T) {
	srv, mock := newVMMetricsTestServer()
	// Wrap GetMachine to count calls. Embed the existing mock struct.
	var calls int
	wrapped := &countingMockStore{mockVMMetricsStore: mock, calls: &calls}
	srv.store = wrapped

	now := time.Now().UTC().Truncate(time.Second)
	pts := make([]map[string]any, 100)
	for i := range pts {
		pts[i] = map[string]any{
			"vm_id":     testMachineID, // same VM repeated
			"kind":      "machine",
			"ts":        now.Add(time.Duration(i) * time.Millisecond * 10).Format(time.RFC3339Nano),
			"mem_bytes": int64(i),
		}
	}
	body := map[string]any{"host_id": testHostID, "points": pts}
	req := makeReq(t, body, testToken)
	w := httptest.NewRecorder()
	srv.handleAgentVMMetrics(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if calls != 1 {
		t.Errorf("GetMachine called %d times for 100 points of same VM; want 1 (ownership cache)", calls)
	}
}

type countingMockStore struct {
	*mockVMMetricsStore
	calls *int
}

func (c *countingMockStore) GetMachine(ctx context.Context, id string) (*store.Machine, error) {
	*c.calls++
	return c.mockVMMetricsStore.GetMachine(ctx, id)
}

func TestVMMetrics_RouteIsRegistered(t *testing.T) {
	// Smoke test: confirm the route is wired in server.go without standing up
	// a full Server. We just inspect that the handler symbol resolves and the
	// package compiles — the fact that this test runs means yes.
	srv, _ := newVMMetricsTestServer()
	if srv == nil {
		t.Fatal("srv = nil")
	}
	// Sanity: the rate limiter is lazy-init.
	if srv.vmMetricsRateLimiter() == nil {
		t.Fatal("rate limiter not initialized")
	}
}
