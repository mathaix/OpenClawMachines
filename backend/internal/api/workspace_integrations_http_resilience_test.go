package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

func TestWorkspaceIntegrationUpstreamSnippet(t *testing.T) {
	googleErr := []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"User-rate limit exceeded.\nRetry shortly.","errors":[{"reason":"rateLimitExceeded"}]}}`)
	got := workspaceIntegrationUpstreamSnippet(googleErr)
	if !strings.Contains(got, "RESOURCE_EXHAUSTED") || !strings.Contains(got, "User-rate limit exceeded") {
		t.Fatalf("snippet = %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("snippet should be single line: %q", got)
	}

	// Non-JSON body falls back to a collapsed, capped raw snippet.
	raw := workspaceIntegrationUpstreamSnippet([]byte("  plain    text\n error  "))
	if raw != "plain text error" {
		t.Fatalf("raw snippet = %q", raw)
	}

	long := workspaceIntegrationUpstreamSnippet([]byte(strings.Repeat("x", 400)))
	if len([]rune(long)) > 257 { // 256 + ellipsis
		t.Fatalf("snippet not capped: len=%d", len([]rune(long)))
	}
}

func TestWorkspaceIntegrationUpstreamErrorMessage(t *testing.T) {
	err := &workspaceIntegrationUpstreamError{StatusCode: 429, Snippet: "RESOURCE_EXHAUSTED: too fast"}
	if !strings.Contains(err.Error(), "upstream returned 429") || !strings.Contains(err.Error(), "too fast") {
		t.Fatalf("error = %q", err.Error())
	}
	if workspaceIntegrationFailureClass(err) != "rate_limited" {
		t.Fatalf("failure class = %q", workspaceIntegrationFailureClass(err))
	}
}

func newGmailTestServer(t *testing.T, manifest []byte) (*Server, *workspaceIntegrationGatewayStore) {
	t.Helper()
	secretKey := "12345678901234567890123456789012"
	encrypted, err := crypto.Encrypt("google-access", secretKey)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	s.secretKey = secretKey
	fakeStore.credential = &store.WorkspaceIntegrationCredential{IntegrationID: "google-1", SecretEnc: encrypted}
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{{
		ID:           "google-1",
		WorkspaceID:  "workspace-1",
		Slug:         googleWorkspaceIntegrationSlug,
		DisplayName:  "Google Workspace",
		Kind:         "google_workspace",
		Transport:    "rest",
		Enabled:      true,
		ToolManifest: manifest,
		Config:       json.RawMessage(`{"email":"user@example.com"}`),
	}}
	return s, fakeStore
}

func TestWorkspaceIntegrationCallTool_RetriesOn429ThenSucceeds(t *testing.T) {
	forceWorkspaceIntegrationSuccessCallEventSampling(t)
	var calls int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"User-rate limit exceeded"}}`))
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"messages": []map[string]interface{}{{"id": "m-1"}}})
	}))
	defer api.Close()

	previous := googleGmailAPIBaseURL
	googleGmailAPIBaseURL = api.URL + "/gmail/v1"
	defer func() { googleGmailAPIBaseURL = previous }()
	prevBackoff := workspaceIntegrationRateLimitBackoff
	workspaceIntegrationRateLimitBackoff = 0
	defer func() { workspaceIntegrationRateLimitBackoff = prevBackoff }()

	manifest := googleWorkspaceToolManifest(map[string]string{"gmail": "read"})
	s, fakeStore := newGmailTestServer(t, manifest)

	req := workspaceIntegrationCallRequest(t, "google-workspace.gmail_list_messages", map[string]interface{}{"arguments": map[string]interface{}{}})
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after retry, got %d: %s", w.Code, w.Body.String())
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 upstream calls (429 then success), got %d", got)
	}
	events := waitForWorkspaceIntegrationCallEvents(t, fakeStore, 1)
	event := events[0]
	if event.Status != "success" || event.RetryCount != 1 || event.RetryAfterMS == nil || *event.RetryAfterMS != 0 || !event.Retryable {
		t.Fatalf("call event retry telemetry = %+v", event)
	}
}

func TestWorkspaceIntegrationCallTool_429SurfacesReasonAfterRetry(t *testing.T) {
	var calls int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"User-rate limit exceeded"}}`))
	}))
	defer api.Close()

	previous := googleGmailAPIBaseURL
	googleGmailAPIBaseURL = api.URL + "/gmail/v1"
	defer func() { googleGmailAPIBaseURL = previous }()
	prevBackoff := workspaceIntegrationRateLimitBackoff
	workspaceIntegrationRateLimitBackoff = 0
	defer func() { workspaceIntegrationRateLimitBackoff = prevBackoff }()

	manifest := googleWorkspaceToolManifest(map[string]string{"gmail": "read"})
	s, _ := newGmailTestServer(t, manifest)

	req := workspaceIntegrationCallRequest(t, "google-workspace.gmail_list_messages", map[string]interface{}{"arguments": map[string]interface{}{}})
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(w, req)

	if w.Code == http.StatusOK {
		t.Fatalf("expected failure on persistent 429")
	}
	// Retried once (2 calls), then surfaced.
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", got)
	}
	var resp struct {
		Error         string                            `json:"error"`
		ErrorContract workspaceIntegrationErrorContract `json:"error_contract"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorContract.Class != "rate_limited" || !resp.ErrorContract.Retryable || resp.ErrorContract.Action != "retry_after" || resp.ErrorContract.Terminal {
		t.Fatalf("error contract = %+v", resp.ErrorContract)
	}
}

func TestWorkspaceIntegrationCallTool_Google401ReconnectIsActionable(t *testing.T) {
	w, calls := callGmailListMessagesWithUpstreamResponse(t, http.StatusUnauthorized, `{"error":{"status":"UNAUTHENTICATED","message":"Invalid Credentials"}}`)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if calls != 2 {
		t.Fatalf("expected initial request plus forced oauth retry, got %d", calls)
	}
	resp := decodeWorkspaceIntegrationCallErrorResponse(t, w)
	if !strings.Contains(resp.Error, "Google Workspace authentication failed") || !strings.Contains(resp.Error, "reconnect the Google account") {
		t.Fatalf("error = %q", resp.Error)
	}
	if resp.ErrorContract.Action != "reconnect_integration" || resp.ErrorContract.Class != "upstream_http_status" || !resp.ErrorContract.Terminal {
		t.Fatalf("error contract = %+v", resp.ErrorContract)
	}
}

func TestWorkspaceIntegrationCallTool_Google403MissingScopeIsActionable(t *testing.T) {
	body := `{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes.","errors":[{"reason":"ACCESS_TOKEN_SCOPE_INSUFFICIENT"}]}}`
	w, calls := callGmailListMessagesWithUpstreamResponse(t, http.StatusForbidden, body)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if calls != 1 {
		t.Fatalf("expected one upstream call, got %d", calls)
	}
	resp := decodeWorkspaceIntegrationCallErrorResponse(t, w)
	if !strings.Contains(resp.Error, "Google Workspace denied the request") || !strings.Contains(resp.Error, "grant the required Gmail, Drive, or Calendar scope") {
		t.Fatalf("error = %q", resp.Error)
	}
	if resp.ErrorContract.Action != "grant_scope_or_reconnect" || resp.ErrorContract.Class != "upstream_http_status" || !resp.ErrorContract.Terminal {
		t.Fatalf("error contract = %+v", resp.ErrorContract)
	}
}

func TestWorkspaceIntegrationCallTool_Google403AdminPolicyIsActionable(t *testing.T) {
	body := `{"error":{"status":"PERMISSION_DENIED","message":"Access blocked by your Google Workspace admin.","errors":[{"reason":"domainPolicy"}]}}`
	w, calls := callGmailListMessagesWithUpstreamResponse(t, http.StatusForbidden, body)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if calls != 1 {
		t.Fatalf("expected one upstream call, got %d", calls)
	}
	resp := decodeWorkspaceIntegrationCallErrorResponse(t, w)
	if !strings.Contains(resp.Error, "admin policy") || !strings.Contains(resp.Error, "ask a Google Workspace admin") {
		t.Fatalf("error = %q", resp.Error)
	}
	if resp.ErrorContract.Action != "ask_google_workspace_admin" || resp.ErrorContract.Class != "upstream_http_status" || !resp.ErrorContract.Terminal {
		t.Fatalf("error contract = %+v", resp.ErrorContract)
	}
}

func TestWorkspaceIntegrationCallTool_DoesNotRetryWriteOn429(t *testing.T) {
	var calls int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"User-rate limit exceeded"}}`))
	}))
	defer api.Close()

	previous := googleGmailAPIBaseURL
	googleGmailAPIBaseURL = api.URL + "/gmail/v1"
	defer func() { googleGmailAPIBaseURL = previous }()
	prevBackoff := workspaceIntegrationRateLimitBackoff
	workspaceIntegrationRateLimitBackoff = 0
	defer func() { workspaceIntegrationRateLimitBackoff = prevBackoff }()

	manifest := googleWorkspaceToolManifest(map[string]string{"gmail": "read_write"})
	s, fakeStore := newGmailTestServer(t, manifest)

	req := workspaceIntegrationCallRequest(t, "google-workspace.gmail_send_message", map[string]interface{}{
		"arguments": map[string]interface{}{"to": "alice@example.com", "subject": "Status", "body": "Done"},
	})
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("write tool should not auto-retry; got %d upstream calls", got)
	}
	var resp struct {
		Error         string                            `json:"error"`
		ErrorContract workspaceIntegrationErrorContract `json:"error_contract"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorContract.Class != "rate_limited" || resp.ErrorContract.Retryable || !resp.ErrorContract.Terminal || resp.ErrorContract.Action != "do_not_auto_retry_write" {
		t.Fatalf("error contract = %+v", resp.ErrorContract)
	}
	if resp.ErrorContract.RetryAfterMS == nil || *resp.ErrorContract.RetryAfterMS != 2000 {
		t.Fatalf("retry_after_ms = %v, want 2000", resp.ErrorContract.RetryAfterMS)
	}
	events := waitForWorkspaceIntegrationCallEvents(t, fakeStore, 1)
	event := events[0]
	if event.Status != "error" || event.RetryCount != 0 || event.RetryAfterMS == nil || *event.RetryAfterMS != 2000 || event.Retryable || !event.Terminal {
		t.Fatalf("call event = %+v", event)
	}
}

type workspaceIntegrationCallErrorResponse struct {
	Error         string                            `json:"error"`
	ErrorContract workspaceIntegrationErrorContract `json:"error_contract"`
}

func callGmailListMessagesWithUpstreamResponse(t *testing.T, status int, body string) (*httptest.ResponseRecorder, int32) {
	t.Helper()
	var calls int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer api.Close()

	previous := googleGmailAPIBaseURL
	googleGmailAPIBaseURL = api.URL + "/gmail/v1"
	defer func() { googleGmailAPIBaseURL = previous }()

	manifest := googleWorkspaceToolManifest(map[string]string{"gmail": "read"})
	s, _ := newGmailTestServer(t, manifest)

	req := workspaceIntegrationCallRequest(t, "google-workspace.gmail_list_messages", map[string]interface{}{"arguments": map[string]interface{}{}})
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(w, req)
	return w, atomic.LoadInt32(&calls)
}

func decodeWorkspaceIntegrationCallErrorResponse(t *testing.T, w *httptest.ResponseRecorder) workspaceIntegrationCallErrorResponse {
	t.Helper()
	var resp workspaceIntegrationCallErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func TestWorkspaceIntegrationRateLimitRetryDelay(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	prevBackoff := workspaceIntegrationRateLimitBackoff
	prevMax := workspaceIntegrationMaxRateLimitRetryAfter
	workspaceIntegrationRateLimitBackoff = 750 * time.Millisecond
	workspaceIntegrationMaxRateLimitRetryAfter = 5 * time.Second
	defer func() {
		workspaceIntegrationRateLimitBackoff = prevBackoff
		workspaceIntegrationMaxRateLimitRetryAfter = prevMax
	}()

	if got := workspaceIntegrationRateLimitRetryDelay("2", now); got != 2*time.Second {
		t.Fatalf("seconds retry-after = %s, want 2s", got)
	}
	if got := workspaceIntegrationRateLimitRetryDelay("120", now); got != 5*time.Second {
		t.Fatalf("capped retry-after = %s, want 5s", got)
	}
	if got := workspaceIntegrationRateLimitRetryDelay(now.Add(3*time.Second).Format(http.TimeFormat), now); got != 3*time.Second {
		t.Fatalf("date retry-after = %s, want 3s", got)
	}
	if got := workspaceIntegrationRateLimitRetryDelay("not-a-date", now); got != 750*time.Millisecond {
		t.Fatalf("fallback retry-after = %s, want 750ms", got)
	}
}
