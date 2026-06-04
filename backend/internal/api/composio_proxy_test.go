package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/composio"
)

const composioProxyTestSecret = "test-secret-at-least-16-bytes-long"

type composioRoundTripFunc func(*http.Request) (*http.Response, error)

func (f composioRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// composioCapture lets tests assert what user_id the upstream Composio API was
// called with — the most security-relevant property.
type composioCapture struct {
	listToolsUserID    string
	listToolsToolkit   string
	executeRequestBody []byte
}

// setupComposioProxyTest creates an in-memory Composio upstream, captures call
// metadata, and wires a Server with auth + composio client.
func setupComposioProxyTest(t *testing.T) (*Server, *composioCapture) {
	t.Helper()

	cap := &composioCapture{}
	client := composio.NewClient("http://composio.test", "ck_test")
	client.SetHTTPClient(&http.Client{
		Transport: composioRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			status := http.StatusNotFound
			body := `{"error":"not found"}`

			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/v3/tools":
				cap.listToolsUserID = r.URL.Query().Get("user_id")
				// Upstream Composio API uses toolkit_slug, not toolkit.
				cap.listToolsToolkit = r.URL.Query().Get("toolkit_slug")
				status = http.StatusOK
				body = `{"items":[{"slug":"GMAIL_FETCH_EMAILS","name":"Fetch emails from Gmail","description":"Fetch emails from Gmail","toolkit":{"slug":"gmail","name":"gmail"},"input_parameters":{"type":"object"}}],"total_items":1}`
			case r.Method == http.MethodPost && r.URL.Path == "/api/v3/tools/execute/GMAIL_FETCH_EMAILS":
				cap.executeRequestBody, _ = io.ReadAll(r.Body)
				status = http.StatusOK
				body = `{"data":{"emails":[]},"successful":true}`
			}

			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}, nil
		}),
	})

	s := &Server{
		composioClient: client,
		auth:           auth.New(composioProxyTestSecret),
	}
	return s, cap
}

func authHeaderForMachine(t *testing.T, s *Server, machineID string) string {
	t.Helper()
	token, err := s.auth.IssueComposioProxyToken(machineID, time.Minute)
	if err != nil {
		t.Fatalf("mint test token: %v", err)
	}
	return "Bearer " + token
}

func newExecuteRequest(t *testing.T, action string, body map[string]interface{}) *http.Request {
	t.Helper()
	encoded, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost,
		"/api/composio/actions/"+action+"/execute",
		bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("action", action)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestComposioProxyListTools(t *testing.T) {
	s, cap := setupComposioProxyTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/composio/tools?toolkit=gmail", nil)
	req.Header.Set("Authorization", authHeaderForMachine(t, s, "machine-123"))
	w := httptest.NewRecorder()

	s.handleComposioListTools(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if cap.listToolsUserID != "machine-123" {
		t.Errorf("upstream user_id = %q, want %q (must come from token, not query)",
			cap.listToolsUserID, "machine-123")
	}
	if cap.listToolsToolkit != "gmail" {
		t.Errorf("upstream toolkit = %q, want %q", cap.listToolsToolkit, "gmail")
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	tools, ok := resp["tools"].([]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("expected 1 tool in response, got: %v", resp)
	}
	tool := tools[0].(map[string]interface{})
	if tool["name"] != "GMAIL_FETCH_EMAILS" {
		t.Errorf("tool name = %v, want GMAIL_FETCH_EMAILS", tool["name"])
	}
	if tool["toolkit"] != "gmail" {
		t.Errorf("tool toolkit = %v, want gmail", tool["toolkit"])
	}
}

func TestComposioProxyListTools_IgnoresUserIDQuery(t *testing.T) {
	// Even if a caller smuggles a user_id query param, the backend must use
	// the machine ID from the validated token. This is the IDOR fix.
	s, cap := setupComposioProxyTest(t)

	req := httptest.NewRequest(http.MethodGet,
		"/api/composio/tools?toolkit=gmail&user_id="+url.QueryEscape("attacker-victim-machine"),
		nil)
	req.Header.Set("Authorization", authHeaderForMachine(t, s, "legit-machine"))
	w := httptest.NewRecorder()

	s.handleComposioListTools(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if cap.listToolsUserID != "legit-machine" {
		t.Errorf("upstream user_id = %q, want %q (smuggled query param must be ignored)",
			cap.listToolsUserID, "legit-machine")
	}
}

func TestComposioProxyListTools_RejectsMissingAuth(t *testing.T) {
	s, _ := setupComposioProxyTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/composio/tools", nil)
	w := httptest.NewRecorder()

	s.handleComposioListTools(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestComposioProxyListTools_RejectsBadToken(t *testing.T) {
	s, _ := setupComposioProxyTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/composio/tools", nil)
	req.Header.Set("Authorization", "Bearer not-a-valid-jwt")
	w := httptest.NewRecorder()

	s.handleComposioListTools(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestComposioProxyListTools_RejectsWrongScheme(t *testing.T) {
	s, _ := setupComposioProxyTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/composio/tools", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	w := httptest.NewRecorder()

	s.handleComposioListTools(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestComposioProxyExecuteAction(t *testing.T) {
	s, cap := setupComposioProxyTest(t)

	req := newExecuteRequest(t, "GMAIL_FETCH_EMAILS", map[string]interface{}{
		"params": map[string]interface{}{"max_results": 10},
	})
	req.Header.Set("Authorization", authHeaderForMachine(t, s, "machine-123"))

	w := httptest.NewRecorder()
	s.handleComposioExecuteAction(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Upstream call must carry the machine ID from the token as user_id.
	if len(cap.executeRequestBody) == 0 {
		t.Fatal("upstream execute body was empty")
	}
	var upstream map[string]interface{}
	if err := json.Unmarshal(cap.executeRequestBody, &upstream); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	if upstream["user_id"] != "machine-123" {
		t.Errorf("upstream user_id = %v, want machine-123", upstream["user_id"])
	}

	var result map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["successful"] != true {
		t.Errorf("expected successful=true, got: %v", result["successful"])
	}
}

func TestComposioProxyExecuteAction_IgnoresBodyUserID(t *testing.T) {
	// Even if a caller sends a "user_id" field in the body, the backend must
	// override it with the machine ID from the validated token. IDOR fix.
	s, cap := setupComposioProxyTest(t)

	req := newExecuteRequest(t, "GMAIL_FETCH_EMAILS", map[string]interface{}{
		"user_id": "attacker-victim-machine",
		"params":  map[string]interface{}{"max_results": 10},
	})
	req.Header.Set("Authorization", authHeaderForMachine(t, s, "legit-machine"))

	w := httptest.NewRecorder()
	s.handleComposioExecuteAction(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var upstream map[string]interface{}
	if err := json.Unmarshal(cap.executeRequestBody, &upstream); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	if upstream["user_id"] != "legit-machine" {
		t.Errorf("upstream user_id = %v, want legit-machine (smuggled body user_id must be ignored)",
			upstream["user_id"])
	}
}

func TestComposioProxyExecuteAction_RejectsMissingAuth(t *testing.T) {
	s, _ := setupComposioProxyTest(t)

	req := newExecuteRequest(t, "GMAIL_FETCH_EMAILS", map[string]interface{}{
		"params": map[string]interface{}{},
	})

	w := httptest.NewRecorder()
	s.handleComposioExecuteAction(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestComposioProxyExecuteAction_RejectsBadToken(t *testing.T) {
	s, _ := setupComposioProxyTest(t)

	req := newExecuteRequest(t, "GMAIL_FETCH_EMAILS", map[string]interface{}{
		"params": map[string]interface{}{},
	})
	req.Header.Set("Authorization", "Bearer not-a-valid-jwt")

	w := httptest.NewRecorder()
	s.handleComposioExecuteAction(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestComposioProxyNoAuth(t *testing.T) {
	// When the server has no Auth wired (dev mode without JWT_SECRET), the
	// proxy must refuse to serve rather than silently allow unauth'd calls.
	s := &Server{
		composioClient: nil,
		auth:           nil,
	}

	t.Run("list_tools", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/composio/tools", nil)
		req.Header.Set("Authorization", "Bearer doesnt-matter")
		w := httptest.NewRecorder()
		s.handleComposioListTools(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", w.Code)
		}
	})

	t.Run("execute_action", func(t *testing.T) {
		req := newExecuteRequest(t, "FOO", map[string]interface{}{})
		req.Header.Set("Authorization", "Bearer doesnt-matter")
		w := httptest.NewRecorder()
		s.handleComposioExecuteAction(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", w.Code)
		}
	})
}

func TestComposioProxyNoClient(t *testing.T) {
	// When the upstream client isn't configured but auth IS wired, an
	// authenticated caller still gets 503 (Composio not configured).
	s := &Server{
		composioClient: nil,
		auth:           auth.New(composioProxyTestSecret),
	}

	t.Run("list_tools", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/composio/tools", nil)
		req.Header.Set("Authorization", authHeaderForMachine(t, s, "machine-123"))
		w := httptest.NewRecorder()
		s.handleComposioListTools(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", w.Code)
		}
	})

	t.Run("execute_action", func(t *testing.T) {
		req := newExecuteRequest(t, "FOO", map[string]interface{}{})
		req.Header.Set("Authorization", authHeaderForMachine(t, s, "machine-123"))
		w := httptest.NewRecorder()
		s.handleComposioExecuteAction(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", w.Code)
		}
	})
}
