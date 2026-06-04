package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// mockOpikStore implements the minimal store methods needed for Opik handler tests.
type mockOpikStore struct {
	store.Store

	// machine returned by GetMachineByGatewayToken when token matches
	machine *store.Machine
	token   string

	// project returned by GetOrCreateOpikProject
	project *store.OpikProject

	upsertTraceCalled  bool
	upsertTracesCalled bool

	traces               []store.OpikTraceListItem
	traceDetail          *store.OpikTraceDetail
	feedback             []store.OpikFeedbackScore
	listAccountCalled    bool
	listAccountID        int
	listAccountFilters   store.OpikTraceSearchFilters
	listTraceCalled      bool
	listTraceAccountID   int
	listTraceMachineID   string
	getAccountCalled     bool
	getAccountID         int
	getAccountTraceID    string
	getTraceCalled       bool
	getTraceAccountID    int
	getTraceMachineID    string
	getTraceRequestedID  string
	listFeedbackCalled   bool
	createFeedbackCalled bool
	createdFeedback      *store.OpikFeedbackScore
	updateTagsCalled     bool
	updateTagsAccountID  int
	updateTagsTraceID    string
	updateTags           []string
}

func (m *mockOpikStore) GetMachine(_ context.Context, id string) (*store.Machine, error) {
	if m.machine != nil && m.machine.ID == id {
		return m.machine, nil
	}
	return nil, fmt.Errorf("machine not found for id %q", id)
}

func (m *mockOpikStore) GetMachineByGatewayToken(_ context.Context, token string) (*store.Machine, error) {
	if m.machine != nil && token == m.token {
		return m.machine, nil
	}
	return nil, fmt.Errorf("machine not found for token %q", token)
}

func (m *mockOpikStore) GetOrCreateOpikProject(_ context.Context, _ int, _ string) (*store.OpikProject, error) {
	if m.project != nil {
		return m.project, nil
	}
	return &store.OpikProject{ID: "proj-1", Name: "default", AccountID: 1}, nil
}

func (m *mockOpikStore) UpsertOpikTrace(_ context.Context, _ *store.OpikTrace) error {
	m.upsertTraceCalled = true
	return nil
}

func (m *mockOpikStore) UpsertOpikTraces(_ context.Context, _ []store.OpikTrace) error {
	m.upsertTracesCalled = true
	return nil
}

func (m *mockOpikStore) ListOpikTraces(_ context.Context, accountID int, filters store.OpikTraceSearchFilters) ([]store.OpikTraceListItem, error) {
	m.listAccountCalled = true
	m.listAccountID = accountID
	m.listAccountFilters = filters
	return m.traces, nil
}

func (m *mockOpikStore) ListOpikTracesByMachine(_ context.Context, accountID int, machineID string, _ time.Time, _ int) ([]store.OpikTraceListItem, error) {
	m.listTraceCalled = true
	m.listTraceAccountID = accountID
	m.listTraceMachineID = machineID
	return m.traces, nil
}

func (m *mockOpikStore) GetOpikTraceDetailByAccount(_ context.Context, accountID int, traceID string) (*store.OpikTraceDetail, error) {
	m.getAccountCalled = true
	m.getAccountID = accountID
	m.getAccountTraceID = traceID
	if m.traceDetail != nil {
		return m.traceDetail, nil
	}
	return nil, store.ErrOpikRecordNotFound
}

func (m *mockOpikStore) GetOpikTraceDetail(_ context.Context, accountID int, machineID, traceID string) (*store.OpikTraceDetail, error) {
	m.getTraceCalled = true
	m.getTraceAccountID = accountID
	m.getTraceMachineID = machineID
	m.getTraceRequestedID = traceID
	if m.traceDetail != nil {
		return m.traceDetail, nil
	}
	return nil, store.ErrOpikRecordNotFound
}

func (m *mockOpikStore) ListOpikFeedbackScores(_ context.Context, _ int, _ string) ([]store.OpikFeedbackScore, error) {
	m.listFeedbackCalled = true
	return m.feedback, nil
}

func (m *mockOpikStore) CreateOpikFeedbackScore(_ context.Context, score *store.OpikFeedbackScore) (*store.OpikFeedbackScore, error) {
	m.createFeedbackCalled = true
	m.createdFeedback = score
	return &store.OpikFeedbackScore{
		ID:        "feedback-1",
		AccountID: score.AccountID,
		TraceID:   score.TraceID,
		SpanID:    score.SpanID,
		Name:      score.Name,
		Value:     score.Value,
		Reason:    score.Reason,
		Source:    score.Source,
		CreatedBy: score.CreatedBy,
		CreatedAt: time.Now(),
	}, nil
}

func (m *mockOpikStore) UpdateOpikTraceTags(_ context.Context, accountID int, traceID string, tags []string) error {
	m.updateTagsCalled = true
	m.updateTagsAccountID = accountID
	m.updateTagsTraceID = traceID
	m.updateTags = append([]string(nil), tags...)
	return nil
}

func opikDashboardRequest(method, path string, accountID int, params map[string]string) *http.Request {
	return opikDashboardRequestWithBody(method, path, "", accountID, params)
}

func opikDashboardRequestWithBody(method, path, body string, accountID int, params map[string]string) *http.Request {
	reader := strings.NewReader(body)
	req := httptest.NewRequest(method, path, reader)
	ctx := context.WithValue(req.Context(), accountIDKey, accountID)
	rctx := chi.NewRouteContext()
	for key, value := range params {
		rctx.URLParams.Add(key, value)
	}
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func TestHandleOpikCreateTrace_Unauthorized(t *testing.T) {
	srv := &Server{store: &mockOpikStore{}}

	req := httptest.NewRequest(http.MethodPost, "/api/opik/v1/private/traces", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	srv.handleOpikCreateTrace(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHandleOpikCreateTrace_InvalidBearerToken(t *testing.T) {
	srv := &Server{store: &mockOpikStore{}}

	req := httptest.NewRequest(http.MethodPost, "/api/opik/v1/private/traces", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer bad-token")
	w := httptest.NewRecorder()

	srv.handleOpikCreateTrace(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHandleOpikCreateTracesBatch_NoAuth(t *testing.T) {
	srv := &Server{store: &mockOpikStore{}}

	req := httptest.NewRequest(http.MethodPost, "/api/opik/v1/private/traces/batch", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	srv.handleOpikCreateTracesBatch(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHandleOpikCreateSpan_NoAuth(t *testing.T) {
	srv := &Server{store: &mockOpikStore{}}

	req := httptest.NewRequest(http.MethodPost, "/api/opik/v1/private/spans", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	srv.handleOpikCreateSpan(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHandleOpikListProjects_NoAuth(t *testing.T) {
	srv := &Server{store: &mockOpikStore{}}

	req := httptest.NewRequest(http.MethodGet, "/api/opik/v1/private/projects", nil)
	w := httptest.NewRecorder()

	srv.handleOpikListProjects(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHandleOpikCreateTrace_ValidAuth(t *testing.T) {
	ms := &mockOpikStore{
		machine: &store.Machine{ID: "m-1", AccountID: 1},
		token:   "valid-token",
	}
	srv := &Server{store: ms}

	body := `{"id":"trace-1","name":"test trace"}`
	req := httptest.NewRequest(http.MethodPost, "/api/opik/v1/private/traces", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	w := httptest.NewRecorder()

	srv.handleOpikCreateTrace(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d; body: %s", w.Code, w.Body.String())
	}
	if !ms.upsertTraceCalled {
		t.Fatal("expected UpsertOpikTrace to be called")
	}
}

func TestHandleOpikCreateTrace_RawTokenWithoutBearerPrefix(t *testing.T) {
	ms := &mockOpikStore{
		machine: &store.Machine{ID: "m-1", AccountID: 1},
		token:   "valid-token",
	}
	srv := &Server{store: ms}

	body := `{"id":"trace-raw","name":"test trace"}`
	req := httptest.NewRequest(http.MethodPost, "/api/opik/v1/private/traces", strings.NewReader(body))
	req.Header.Set("Authorization", "valid-token") // Opik SDK sends raw token, no "Bearer " prefix
	w := httptest.NewRecorder()

	srv.handleOpikCreateTrace(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for raw token auth, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleOpikCreateTracesBatch_ValidAuth(t *testing.T) {
	ms := &mockOpikStore{
		machine: &store.Machine{ID: "m-1", AccountID: 1},
		token:   "valid-token",
	}
	srv := &Server{store: ms}

	body := `{"traces":[{"id":"t1","name":"trace 1"},{"id":"t2","name":"trace 2"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/opik/v1/private/traces/batch", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	w := httptest.NewRecorder()

	srv.handleOpikCreateTracesBatch(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d; body: %s", w.Code, w.Body.String())
	}
	if !ms.upsertTracesCalled {
		t.Fatal("expected UpsertOpikTraces to be called")
	}
}

func TestHandleListAccountTraces_ParsesFilters(t *testing.T) {
	since := time.Date(2026, 4, 27, 6, 0, 0, 0, time.UTC)
	ms := &mockOpikStore{
		traces: []store.OpikTraceListItem{{ID: "trace-1", StartTime: since}},
	}
	srv := &Server{store: ms}

	path := "/api/accounts/7/traces?since=" + since.Format(time.RFC3339) +
		"&limit=25&machine_id=m-1&project=default&status=error&q=payload&thread_id=thread-1" +
		"&tag=prod,Needs+Review&tag=Eval-Fail&feedback=low&max_feedback_score=0.4&min_tokens=100&min_cost=0.01&min_duration_ms=2000"
	req := opikDashboardRequest(http.MethodGet, path, 7, map[string]string{})
	w := httptest.NewRecorder()

	srv.handleListAccountTraces(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	if !ms.listAccountCalled {
		t.Fatal("expected ListOpikTraces to be called")
	}
	if ms.listAccountID != 7 {
		t.Fatalf("unexpected account id %d", ms.listAccountID)
	}
	filters := ms.listAccountFilters
	if !filters.Since.Equal(since) || filters.Limit != 25 || filters.MachineID != "m-1" ||
		filters.Project != "default" || filters.Status != "error" || filters.Query != "payload" ||
		filters.ThreadID != "thread-1" {
		t.Fatalf("unexpected filters: %+v", filters)
	}
	if filters.Feedback != "low" || filters.MaxFeedback == nil || *filters.MaxFeedback != 0.4 {
		t.Fatalf("unexpected feedback filters: %+v", filters)
	}
	if !reflect.DeepEqual(filters.Tags, []string{"prod", "needs-review", "eval-fail"}) {
		t.Fatalf("unexpected tags: %#v", filters.Tags)
	}
	if filters.MinTokens == nil || *filters.MinTokens != 100 {
		t.Fatalf("unexpected min tokens: %#v", filters.MinTokens)
	}
	if filters.MinCost == nil || *filters.MinCost != 0.01 {
		t.Fatalf("unexpected min cost: %#v", filters.MinCost)
	}
	if filters.MinDurationMS == nil || *filters.MinDurationMS != 2000 {
		t.Fatalf("unexpected min duration: %#v", filters.MinDurationMS)
	}
}

func TestHandleListAccountTraces_RejectsInvalidStatus(t *testing.T) {
	ms := &mockOpikStore{}
	srv := &Server{store: ms}

	req := opikDashboardRequest(http.MethodGet, "/api/accounts/7/traces?status=slow", 7, map[string]string{})
	w := httptest.NewRecorder()

	srv.handleListAccountTraces(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d; body: %s", w.Code, w.Body.String())
	}
	if ms.listAccountCalled {
		t.Fatal("did not expect ListOpikTraces to be called")
	}
}

func TestHandleGetAccountTrace_ScopesByAccountAndTrace(t *testing.T) {
	ms := &mockOpikStore{
		traceDetail: &store.OpikTraceDetail{Trace: store.OpikTraceListItem{ID: "trace-1", StartTime: time.Now()}},
	}
	srv := &Server{store: ms}

	req := opikDashboardRequest(http.MethodGet, "/api/accounts/7/traces/trace-1", 7, map[string]string{
		"traceID": "trace-1",
	})
	w := httptest.NewRecorder()

	srv.handleGetAccountTrace(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	if !ms.getAccountCalled {
		t.Fatal("expected GetOpikTraceDetailByAccount to be called")
	}
	if ms.getAccountID != 7 || ms.getAccountTraceID != "trace-1" {
		t.Fatalf("unexpected account trace scope account=%d trace=%q", ms.getAccountID, ms.getAccountTraceID)
	}
}

func TestHandleUpdateTraceTags_NormalizesAndScopes(t *testing.T) {
	ms := &mockOpikStore{
		traceDetail: &store.OpikTraceDetail{Trace: store.OpikTraceListItem{ID: "trace-1", StartTime: time.Now()}},
	}
	srv := &Server{store: ms}

	body := `{"tags":[" Needs Review ","bug","needs-review","env:prod"]}`
	req := opikDashboardRequestWithBody(http.MethodPut, "/api/accounts/7/traces/trace-1/tags", body, 7, map[string]string{
		"traceID": "trace-1",
	})
	w := httptest.NewRecorder()

	srv.handleUpdateTraceTags(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d; body: %s", w.Code, w.Body.String())
	}
	if !ms.updateTagsCalled {
		t.Fatal("expected UpdateOpikTraceTags to be called")
	}
	if ms.updateTagsAccountID != 7 || ms.updateTagsTraceID != "trace-1" {
		t.Fatalf("unexpected tag update scope account=%d trace=%q", ms.updateTagsAccountID, ms.updateTagsTraceID)
	}
	expected := []string{"needs-review", "bug", "env:prod"}
	if !reflect.DeepEqual(ms.updateTags, expected) {
		t.Fatalf("unexpected tags: %#v", ms.updateTags)
	}
}

func TestHandleUpdateTraceTags_RejectsInvalidTag(t *testing.T) {
	ms := &mockOpikStore{}
	srv := &Server{store: ms}

	req := opikDashboardRequestWithBody(http.MethodPut, "/api/accounts/7/traces/trace-1/tags", `{"tags":["bad*tag"]}`, 7, map[string]string{
		"traceID": "trace-1",
	})
	w := httptest.NewRecorder()

	srv.handleUpdateTraceTags(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d; body: %s", w.Code, w.Body.String())
	}
	if ms.updateTagsCalled {
		t.Fatal("did not expect UpdateOpikTraceTags to be called")
	}
}

func TestHandleCreateTraceFeedback_ValidatesAndScopes(t *testing.T) {
	ms := &mockOpikStore{}
	srv := &Server{store: ms}

	body := `{"span_id":"span-1","name":"correctness","value":0.25,"reason":"missed retrieved context"}`
	req := opikDashboardRequestWithBody(http.MethodPost, "/api/accounts/7/traces/trace-1/feedback", body, 7, map[string]string{
		"traceID": "trace-1",
	})
	w := httptest.NewRecorder()

	srv.handleCreateTraceFeedback(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d; body: %s", w.Code, w.Body.String())
	}
	if !ms.createFeedbackCalled {
		t.Fatal("expected CreateOpikFeedbackScore to be called")
	}
	score := ms.createdFeedback
	if score == nil || score.AccountID != 7 || score.TraceID != "trace-1" || score.Name != "correctness" || score.Value != 0.25 {
		t.Fatalf("unexpected feedback score: %+v", score)
	}
	if score.SpanID == nil || *score.SpanID != "span-1" {
		t.Fatalf("unexpected span id: %#v", score.SpanID)
	}
	if score.Reason == nil || *score.Reason != "missed retrieved context" {
		t.Fatalf("unexpected reason: %#v", score.Reason)
	}
}

func TestHandleCreateTraceFeedback_RejectsInvalidValue(t *testing.T) {
	ms := &mockOpikStore{}
	srv := &Server{store: ms}

	req := opikDashboardRequestWithBody(http.MethodPost, "/api/accounts/7/traces/trace-1/feedback", `{"name":"correctness","value":2}`, 7, map[string]string{
		"traceID": "trace-1",
	})
	w := httptest.NewRecorder()

	srv.handleCreateTraceFeedback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d; body: %s", w.Code, w.Body.String())
	}
	if ms.createFeedbackCalled {
		t.Fatal("did not expect CreateOpikFeedbackScore to be called")
	}
}

func TestHandleListMachineTraces_ScopesByAccountAndMachine(t *testing.T) {
	ms := &mockOpikStore{
		machine: &store.Machine{ID: "m-1", AccountID: 7},
		traces:  []store.OpikTraceListItem{{ID: "trace-1", StartTime: time.Now()}},
	}
	srv := &Server{store: ms}

	req := opikDashboardRequest(http.MethodGet, "/api/accounts/7/machines/m-1/traces", 7, map[string]string{"id": "m-1"})
	w := httptest.NewRecorder()

	srv.handleListMachineTraces(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	if !ms.listTraceCalled {
		t.Fatal("expected ListOpikTracesByMachine to be called")
	}
	if ms.listTraceAccountID != 7 || ms.listTraceMachineID != "m-1" {
		t.Fatalf("unexpected trace scope account=%d machine=%q", ms.listTraceAccountID, ms.listTraceMachineID)
	}
}

func TestHandleListMachineTraces_RejectsCrossAccountMachine(t *testing.T) {
	ms := &mockOpikStore{machine: &store.Machine{ID: "m-1", AccountID: 8}}
	srv := &Server{store: ms}

	req := opikDashboardRequest(http.MethodGet, "/api/accounts/7/machines/m-1/traces", 7, map[string]string{"id": "m-1"})
	w := httptest.NewRecorder()

	srv.handleListMachineTraces(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d; body: %s", w.Code, w.Body.String())
	}
	if ms.listTraceCalled {
		t.Fatal("did not expect ListOpikTracesByMachine to be called")
	}
}

func TestHandleListMachineTraces_RejectsInvalidLimit(t *testing.T) {
	ms := &mockOpikStore{machine: &store.Machine{ID: "m-1", AccountID: 7}}
	srv := &Server{store: ms}

	req := opikDashboardRequest(http.MethodGet, "/api/accounts/7/machines/m-1/traces?limit=abc", 7, map[string]string{"id": "m-1"})
	w := httptest.NewRecorder()

	srv.handleListMachineTraces(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d; body: %s", w.Code, w.Body.String())
	}
	if ms.listTraceCalled {
		t.Fatal("did not expect ListOpikTracesByMachine to be called")
	}
}

func TestHandleListMachineTraces_RejectsOutOfRangeLimit(t *testing.T) {
	ms := &mockOpikStore{machine: &store.Machine{ID: "m-1", AccountID: 7}}
	srv := &Server{store: ms}

	req := opikDashboardRequest(http.MethodGet, "/api/accounts/7/machines/m-1/traces?limit=0", 7, map[string]string{"id": "m-1"})
	w := httptest.NewRecorder()

	srv.handleListMachineTraces(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d; body: %s", w.Code, w.Body.String())
	}
	if ms.listTraceCalled {
		t.Fatal("did not expect ListOpikTracesByMachine to be called")
	}
}

func TestHandleGetMachineTrace_ScopesByAccountMachineAndTrace(t *testing.T) {
	ms := &mockOpikStore{
		machine:     &store.Machine{ID: "m-1", AccountID: 7},
		traceDetail: &store.OpikTraceDetail{Trace: store.OpikTraceListItem{ID: "trace-1", StartTime: time.Now()}},
	}
	srv := &Server{store: ms}

	req := opikDashboardRequest(http.MethodGet, "/api/accounts/7/machines/m-1/traces/trace-1", 7, map[string]string{
		"id":      "m-1",
		"traceID": "trace-1",
	})
	w := httptest.NewRecorder()

	srv.handleGetMachineTrace(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	if !ms.getTraceCalled {
		t.Fatal("expected GetOpikTraceDetail to be called")
	}
	if ms.getTraceAccountID != 7 || ms.getTraceMachineID != "m-1" || ms.getTraceRequestedID != "trace-1" {
		t.Fatalf("unexpected trace detail scope account=%d machine=%q trace=%q", ms.getTraceAccountID, ms.getTraceMachineID, ms.getTraceRequestedID)
	}
}
