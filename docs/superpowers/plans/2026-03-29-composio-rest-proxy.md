# Composio REST API Proxy — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the plugin's direct MCP connection (requires `ck_` consumer key) with a backend REST proxy that uses the platform `ak_` API key, so users never need a Composio key.

**Architecture:** The backend becomes a Composio proxy — the plugin calls our backend's `/api/composio/` endpoints instead of Composio's MCP server. The backend forwards requests to Composio's REST API (`/api/v3/tools`, `/api/v2/actions/{action}/execute`) using the platform `ak_` API key. Config assembly injects `apiUrl` (our backend URL) and `userId` (machine ID) — no `consumerKey` needed.

**Tech Stack:** Go (backend proxy), TypeScript (plugin modifications), existing Composio REST API v2/v3

---

## File Structure

| File | Action | Responsibility |
|------|--------|---------------|
| `backend/internal/api/composio_proxy.go` | Create | Proxy handlers: list tools, execute action |
| `backend/internal/api/composio_proxy_test.go` | Create | Tests for proxy handlers |
| `backend/internal/api/server.go` | Modify | Add proxy routes, wire composio client |
| `backend/internal/composio/client.go` | Modify | Add `ListTools()` and `ExecuteAction()` methods |
| `backend/internal/composio/client_test.go` | Modify | Tests for new client methods |
| `backend/internal/configassembly/assembler.go` | Modify | Replace `consumerKey`/`mcpUrl` with `apiUrl` |
| `backend/internal/configassembly/assembler_test.go` | Modify | Update composio config assertions |
| `backend/internal/gatewaye2e/gateway_test.go` | Modify | Update E2E composio config test |
| `backend/cmd/server/main.go` | Modify | Split `COMPOSIO_CONSUMER_KEY` into `COMPOSIO_API_KEY` for client, remove consumer key from config assembly |
| `ocm-openclaw-composio-plugin/index.ts` | Modify | Replace MCP calls with REST calls to our backend |
| `ocm-openclaw-composio-plugin/src/config.ts` | Modify | Replace `consumerKey`/`mcpUrl` with `apiUrl` |
| `ocm-openclaw-composio-plugin/src/types.ts` | Modify | Update config interface |
| `ocm-openclaw-composio-plugin/openclaw.plugin.json` | Modify | Update config schema |
| `rootfs/composio-plugin.tgz` | Rebuild | Rebuilt from modified plugin |

---

### Task 1: Add ListTools and ExecuteAction to Composio Client

**Files:**
- Modify: `backend/internal/composio/client.go`
- Modify: `backend/internal/composio/client_test.go`

- [ ] **Step 1: Write the failing tests for ListTools**

In `backend/internal/composio/client_test.go`, add:

```go
func TestListTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/tools" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("user_id") != "machine-123" {
			t.Errorf("missing or wrong user_id: %s", r.URL.Query().Get("user_id"))
		}
		if r.URL.Query().Get("toolkit") != "gmail" {
			t.Errorf("missing or wrong toolkit: %s", r.URL.Query().Get("toolkit"))
		}
		if r.Header.Get("x-api-key") != "ak_test" {
			t.Errorf("missing or wrong x-api-key")
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"items": []map[string]interface{}{
				{
					"name":        "GMAIL_FETCH_EMAILS",
					"description": "Fetch emails from Gmail",
					"toolkit":     "gmail",
					"parameters": map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{},
					},
				},
			},
			"total_items": 1,
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ak_test")
	tools, err := c.ListTools(context.Background(), "machine-123", "gmail")
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	if tools[0].Name != "GMAIL_FETCH_EMAILS" {
		t.Errorf("Name = %q, want GMAIL_FETCH_EMAILS", tools[0].Name)
	}
}

func TestListToolsAllToolkits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// When toolkit is empty, should not be in query params
		if r.URL.Query().Get("toolkit") != "" {
			t.Errorf("toolkit should be empty for all-tools request, got: %s", r.URL.Query().Get("toolkit"))
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"items":       []interface{}{},
			"total_items": 0,
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ak_test")
	tools, err := c.ListTools(context.Background(), "machine-123", "")
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("got %d tools, want 0", len(tools))
	}
}

func TestExecuteAction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v2/actions/GMAIL_FETCH_EMAILS/execute" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["entityId"] != "machine-456" {
			t.Errorf("entityId = %v, want machine-456", body["entityId"])
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"messages": []interface{}{"email1", "email2"},
			},
			"successful": true,
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ak_test")
	result, err := c.ExecuteAction(context.Background(), "GMAIL_FETCH_EMAILS", "machine-456", map[string]interface{}{
		"max_results": 5,
	})
	if err != nil {
		t.Fatalf("ExecuteAction error: %v", err)
	}
	if !result.Successful {
		t.Error("expected successful=true")
	}
}

func TestExecuteActionAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message": "invalid action"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ak_test")
	_, err := c.ExecuteAction(context.Background(), "INVALID_ACTION", "machine-123", nil)
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test -v -run "TestListTools|TestExecuteAction" ./internal/composio/...`
Expected: FAIL — `ListTools` and `ExecuteAction` undefined

- [ ] **Step 3: Implement ListTools and ExecuteAction**

In `backend/internal/composio/client.go`, add types and methods:

```go
// Tool represents a Composio tool definition.
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Toolkit     string                 `json:"toolkit"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ActionResult represents the result of executing a Composio action.
type ActionResult struct {
	Data       interface{} `json:"data"`
	Successful bool        `json:"successful"`
	Error      string      `json:"error,omitempty"`
}

// ListTools returns available tools, optionally filtered by toolkit slug.
func (c *Client) ListTools(ctx context.Context, userID, toolkit string) ([]Tool, error) {
	u := c.baseURL + "/api/v3/tools?user_id=" + userID
	if toolkit != "" {
		u += "&toolkit=" + toolkit
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("composio: list tools: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.apiError("list tools", resp)
	}

	var result struct {
		Items []Tool `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("composio: decode tools response: %w", err)
	}
	return result.Items, nil
}

// ExecuteAction runs a Composio action for a specific user/machine.
func (c *Client) ExecuteAction(ctx context.Context, actionName, userID string, params map[string]interface{}) (*ActionResult, error) {
	body := map[string]interface{}{
		"entityId": userID,
	}
	if params != nil {
		body["input"] = params
	}
	bodyJSON, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v2/actions/"+actionName+"/execute",
		strings.NewReader(string(bodyJSON)))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("composio: execute action: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.apiError("execute action", resp)
	}

	var result ActionResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("composio: decode execute response: %w", err)
	}
	return &result, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test -v -run "TestListTools|TestExecuteAction" ./internal/composio/...`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/composio/client.go backend/internal/composio/client_test.go
git commit -m "feat(composio): add ListTools and ExecuteAction to client"
```

---

### Task 2: Create Backend Proxy Handlers

**Files:**
- Create: `backend/internal/api/composio_proxy.go`
- Create: `backend/internal/api/composio_proxy_test.go`

- [ ] **Step 1: Write the failing test for tools proxy**

Create `backend/internal/api/composio_proxy_test.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/composio"
)

// mockComposioClient provides a test double for composio.Client.
// The real client talks to httptest servers in unit tests, but for the proxy
// handler tests we need to verify the handler logic without a real Composio server.
func setupComposioProxyTest(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()

	// Fake Composio upstream
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/tools":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"items": []map[string]interface{}{
					{"name": "GMAIL_FETCH_EMAILS", "description": "Fetch emails", "toolkit": "gmail", "parameters": map[string]interface{}{}},
				},
				"total_items": 1,
			})
		case strings.HasPrefix(r.URL.Path, "/api/v2/actions/") && strings.HasSuffix(r.URL.Path, "/execute"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data":       map[string]interface{}{"result": "ok"},
				"successful": true,
			})
		default:
			w.WriteHeader(404)
		}
	}))

	srv := &Server{
		composioClient: composio.NewClient(upstream.URL, "ak_test"),
	}
	return srv, upstream
}

func TestComposioProxyListTools(t *testing.T) {
	srv, upstream := setupComposioProxyTest(t)
	defer upstream.Close()

	req := httptest.NewRequest("GET", "/api/composio/tools?user_id=machine-123&toolkit=gmail", nil)
	w := httptest.NewRecorder()

	srv.handleComposioListTools(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Tools) != 1 || resp.Tools[0].Name != "GMAIL_FETCH_EMAILS" {
		t.Errorf("unexpected tools: %+v", resp.Tools)
	}
}

func TestComposioProxyExecuteAction(t *testing.T) {
	srv, upstream := setupComposioProxyTest(t)
	defer upstream.Close()

	body := `{"user_id":"machine-123","params":{"max_results":5}}`
	req := httptest.NewRequest("POST", "/api/composio/actions/GMAIL_FETCH_EMAILS/execute", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	// chi URL params
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("action", "GMAIL_FETCH_EMAILS")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	srv.handleComposioExecuteAction(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp composio.ActionResult
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Successful {
		t.Error("expected successful=true")
	}
}

func TestComposioProxyNoClient(t *testing.T) {
	srv := &Server{} // no composioClient

	req := httptest.NewRequest("GET", "/api/composio/tools?user_id=test", nil)
	w := httptest.NewRecorder()
	srv.handleComposioListTools(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test -v -run "TestComposioProxy" ./internal/api/...`
Expected: FAIL — `handleComposioListTools`, `handleComposioExecuteAction` undefined

- [ ] **Step 3: Implement proxy handlers**

Create `backend/internal/api/composio_proxy.go`:

```go
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// handleComposioListTools proxies tool listing to Composio REST API.
// GET /api/composio/tools?user_id=<machineID>&toolkit=<optional>
func (s *Server) handleComposioListTools(w http.ResponseWriter, r *http.Request) {
	if s.composioClient == nil || !s.composioClient.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "composio not configured")
		return
	}

	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "user_id required")
		return
	}
	toolkit := r.URL.Query().Get("toolkit")

	tools, err := s.composioClient.ListTools(r.Context(), userID, toolkit)
	if err != nil {
		slog.Error("composio.proxy.list_tools_failed", "user_id", userID, "error", err)
		writeError(w, http.StatusBadGateway, "failed to list tools: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tools": tools,
	})
}

// handleComposioExecuteAction proxies action execution to Composio REST API.
// POST /api/composio/actions/{action}/execute
// Body: {"user_id": "...", "params": {...}}
func (s *Server) handleComposioExecuteAction(w http.ResponseWriter, r *http.Request) {
	if s.composioClient == nil || !s.composioClient.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "composio not configured")
		return
	}

	actionName := chi.URLParam(r, "action")
	if actionName == "" {
		writeError(w, http.StatusBadRequest, "action name required")
		return
	}

	var body struct {
		UserID string                 `json:"user_id"`
		Params map[string]interface{} `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.UserID == "" {
		writeError(w, http.StatusBadRequest, "user_id required")
		return
	}

	result, err := s.composioClient.ExecuteAction(r.Context(), actionName, body.UserID, body.Params)
	if err != nil {
		slog.Error("composio.proxy.execute_failed", "action", actionName, "user_id", body.UserID, "error", err)
		writeError(w, http.StatusBadGateway, "failed to execute action: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test -v -run "TestComposioProxy" ./internal/api/...`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/composio_proxy.go backend/internal/api/composio_proxy_test.go
git commit -m "feat(composio): add REST proxy handlers for tools and actions"
```

---

### Task 3: Wire Proxy Routes in Server

**Files:**
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Add composio proxy routes**

In `backend/internal/api/server.go`, find the agent auth routes section (around line 550) and add the composio proxy routes. These go in the agent-auth route group since they'll be called from within VMs:

```go
// Composio proxy — plugin inside VM calls these instead of Composio MCP directly
r.Get("/api/agent/machines/{machineID}/composio/tools", srv.handleComposioProxyTools)
r.Post("/api/agent/machines/{machineID}/composio/actions/{action}/execute", srv.handleComposioProxyExecute)
```

These handlers are thin wrappers that extract machineID from the URL and delegate to the proxy handlers:

In `backend/internal/api/composio_proxy.go`, add wrapper handlers:

```go
// handleComposioProxyTools is the agent-authenticated wrapper for tool listing.
// GET /api/agent/machines/{machineID}/composio/tools
func (s *Server) handleComposioProxyTools(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}
	machineID := chi.URLParam(r, "machineID")
	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	// Override user_id with the machine ID
	q := r.URL.Query()
	q.Set("user_id", machineID)
	r.URL.RawQuery = q.Encode()

	s.handleComposioListTools(w, r)
}

// handleComposioProxyExecute is the agent-authenticated wrapper for action execution.
// POST /api/agent/machines/{machineID}/composio/actions/{action}/execute
func (s *Server) handleComposioProxyExecute(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}
	machineID := chi.URLParam(r, "machineID")
	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	s.handleComposioExecuteAction(w, r)
}
```

- [ ] **Step 2: Verify compilation**

Run: `cd backend && go build ./...`
Expected: BUILD SUCCESS

- [ ] **Step 3: Commit**

```bash
git add backend/internal/api/server.go backend/internal/api/composio_proxy.go
git commit -m "feat(composio): wire proxy routes in server"
```

---

### Task 4: Update Config Assembly — Replace consumerKey/mcpUrl with apiUrl

**Files:**
- Modify: `backend/internal/configassembly/assembler.go`
- Modify: `backend/internal/configassembly/assembler_test.go`
- Modify: `backend/internal/gatewaye2e/gateway_test.go`

- [ ] **Step 1: Update AssemblyParams**

In `backend/internal/configassembly/assembler.go`, change `AssemblyParams`:

Replace:
```go
ComposioConsumerKey string
```
With:
```go
ComposioAPIURL string // Backend proxy URL for Composio (e.g. https://api.openclawmachines.com/api/composio)
```

Do the same for `SeedParams`.

- [ ] **Step 2: Update config injection in AssembleConfig**

Replace the "5e-composio" section (around line 637):

```go
// 5e-composio. Inject Composio config: apiUrl points to our backend proxy, userId for machine scoping.
// No consumer key needed — the backend proxies to Composio REST API using the platform API key.
if params.ComposioAPIURL != "" {
	if plugins, ok := result["plugins"].(map[string]interface{}); ok {
		if entries, ok := plugins["entries"].(map[string]interface{}); ok {
			if composioEntry, ok := entries["composio"].(map[string]interface{}); ok {
				config := getOrCreateMap(composioEntry, "config")
				if _, ok := config["enabled"]; !ok {
					config["enabled"] = true
				}
				config["apiUrl"] = params.ComposioAPIURL
				config["userId"] = params.MachineID
				slog.Debug("configassembly.composio_injected", "machine_id", params.MachineID)
			}
		}
		installs := getOrCreateMap(plugins, "installs")
		installs["composio"] = map[string]interface{}{
			"source":      "archive",
			"installPath": "/home/openclaw/.openclaw/extensions/composio",
		}
	}
} else {
	slog.Debug("configassembly.composio_skipped", "reason", "no_api_url")
}
```

Do the same for the seed config section.

- [ ] **Step 3: Update tests**

In `backend/internal/configassembly/assembler_test.go`, update `TestAssembleConfig_ComposioInjection`:

Replace `ComposioConsumerKey: "ck_test_key"` with `ComposioAPIURL: "https://api.openclawmachines.com/api/composio"`.

Update assertions:
```go
// Verify apiUrl points to our backend proxy
if config["apiUrl"] != "https://api.openclawmachines.com/api/composio" {
	t.Errorf("apiUrl = %v, want https://api.openclawmachines.com/api/composio", config["apiUrl"])
}

// Verify no consumerKey or mcpUrl in config
if _, ok := config["consumerKey"]; ok {
	t.Error("consumerKey should not be in config — backend proxy handles auth")
}
if _, ok := config["mcpUrl"]; ok {
	t.Error("mcpUrl should not be in config — plugin calls backend apiUrl instead")
}
```

Update `TestAssembleConfig_PluginAllowListMerge` similarly — replace `ComposioConsumerKey` with `ComposioAPIURL`.

In `backend/internal/gatewaye2e/gateway_test.go`, update `TestGatewayE2E_ComposioConfigAssembly`:

Replace `ComposioConsumerKey: testConsumerKey` with `ComposioAPIURL: "https://api.openclawmachines.com/api/composio"`.

Update assertions to check for `apiUrl` instead of `consumerKey`.

- [ ] **Step 4: Run tests**

Run: `cd backend && go test ./internal/configassembly/... ./internal/composio/...`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/configassembly/assembler.go backend/internal/configassembly/assembler_test.go backend/internal/gatewaye2e/gateway_test.go
git commit -m "feat(composio): config assembly injects apiUrl instead of consumerKey"
```

---

### Task 5: Update main.go — Split API Key Usage

**Files:**
- Modify: `backend/cmd/server/main.go`
- Modify: `backend/internal/api/server.go`
- Modify: `backend/internal/api/machine_config.go`

- [ ] **Step 1: Update main.go**

In `backend/cmd/server/main.go`, the env var `COMPOSIO_CONSUMER_KEY` currently serves double duty (client auth + config injection). Change to:

```go
// Composio: platform API key for REST API calls (connection management + proxy)
if key := os.Getenv("COMPOSIO_CONSUMER_KEY"); key != "" {
	srv.SetComposioClient(composio.NewClient("https://backend.composio.dev", key))
	slog.Info("composio.configured")
}
```

Remove `srv.SetComposioConsumerKey(key)` — no longer needed.

In the `RuntimeService` config (around line 281), replace:
```go
ComposioConsumerKey: os.Getenv("COMPOSIO_CONSUMER_KEY"),
```
With:
```go
ComposioAPIURL: resolveComposioAPIURL(),
```

Add the resolver function (same pattern as `resolveOpikAPIURL`):
```go
func resolveComposioAPIURL() string {
	if u := os.Getenv("COMPOSIO_API_URL"); u != "" {
		return u
	}
	if u := os.Getenv("PUBLIC_URL"); u != "" {
		return u + "/api/composio"
	}
	return ""
}
```

- [ ] **Step 2: Update Server — remove composioConsumerKey field**

In `backend/internal/api/server.go`:
- Remove the `composioConsumerKey string` field
- Remove the `SetComposioConsumerKey` method
- Add `composioAPIURL string` field
- Add `SetComposioAPIURL(url string)` method

In `backend/internal/api/machine_config.go`, update `assembleConfigForMachine` to pass `ComposioAPIURL: s.composioAPIURL` instead of `ComposioConsumerKey: s.composioConsumerKey`.

- [ ] **Step 3: Update RuntimeService**

In `backend/internal/machines/runtime.go`, replace `composioConsumerKey` field with `composioAPIURL` and update all references.

- [ ] **Step 4: Update agent_auth.go secrets**

In `backend/internal/api/agent_auth.go`, remove the `COMPOSIO_CONSUMER_KEY` injection from `handleAgentAuthGetSecrets` (around lines 439-442). The key is no longer needed inside the VM.

- [ ] **Step 5: Verify compilation and tests**

Run: `cd backend && go build ./... && go test ./...`
Expected: BUILD SUCCESS, ALL TESTS PASS

- [ ] **Step 6: Commit**

```bash
git add backend/cmd/server/main.go backend/internal/api/server.go backend/internal/api/machine_config.go backend/internal/machines/runtime.go backend/internal/api/agent_auth.go
git commit -m "refactor(composio): remove consumer key plumbing, add apiUrl"
```

---

### Task 6: Modify Plugin — Replace MCP with REST Calls

**Files:**
- Modify: `ocm-openclaw-composio-plugin/index.ts`
- Modify: `ocm-openclaw-composio-plugin/src/config.ts`
- Modify: `ocm-openclaw-composio-plugin/src/types.ts`
- Modify: `ocm-openclaw-composio-plugin/openclaw.plugin.json`
- Modify: `ocm-openclaw-composio-plugin/package.json`

- [ ] **Step 1: Update config schema**

In `ocm-openclaw-composio-plugin/src/types.ts`:
```typescript
export interface ComposioConfig {
  enabled: boolean;
  apiUrl: string;
  userId: string;
}
```

In `ocm-openclaw-composio-plugin/src/config.ts`:
```typescript
import { z } from "zod";
import type { ComposioConfig } from "./types.js";

export const ComposioConfigSchema = z.object({
  enabled: z.boolean().default(true),
  apiUrl: z.string().default(""),
  userId: z.string().default("default"),
});

export function parseComposioConfig(value: unknown): ComposioConfig {
  const raw =
    value && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : {};

  const configObj = raw.config as Record<string, unknown> | undefined;

  const apiUrl =
    (typeof configObj?.apiUrl === "string" && configObj.apiUrl.trim()) ||
    (typeof raw.apiUrl === "string" && raw.apiUrl.trim()) ||
    "";

  const userId =
    (typeof configObj?.userId === "string" && configObj.userId.trim()) ||
    (typeof raw.userId === "string" && raw.userId.trim()) ||
    "default";

  return ComposioConfigSchema.parse({ ...raw, apiUrl, userId });
}

export const composioPluginConfigSchema = {
  parse: parseComposioConfig,
  uiHints: {
    enabled: {
      label: "Enable Composio",
      help: "Enable or disable the Composio integration",
    },
    apiUrl: {
      label: "API URL",
      help: "Backend proxy URL for Composio (injected by OCM, do not set manually)",
      advanced: true,
    },
    userId: {
      label: "User ID",
      help: "Machine-specific user ID (injected by OCM, do not set manually)",
      advanced: true,
    },
  },
};
```

- [ ] **Step 2: Update openclaw.plugin.json**

Replace the `configSchema` section to match the new fields (remove `consumerKey` and `mcpUrl`, add `apiUrl`).

- [ ] **Step 3: Rewrite index.ts — replace MCP with REST**

Replace `ocm-openclaw-composio-plugin/index.ts`:

```typescript
import type { OpenClawPluginApi } from "openclaw/plugin-sdk";
import { execFileSync } from "node:child_process";
import { composioPluginConfigSchema, parseComposioConfig } from "./src/config.js";

function fetchToolsSync(apiUrl: string, userId: string) {
  const url = `${apiUrl}/tools?user_id=${encodeURIComponent(userId)}`;
  const raw = execFileSync("curl", [
    url, "-s",
    "-H", "Accept: application/json",
  ], { encoding: "utf-8", timeout: 15_000 });

  const parsed = JSON.parse(raw);
  return (parsed.tools ?? []) as Array<{
    name: string;
    description?: string;
    parameters?: Record<string, unknown>;
  }>;
}

function executeActionSync(apiUrl: string, action: string, userId: string, params: Record<string, unknown>): string {
  const url = `${apiUrl}/actions/${encodeURIComponent(action)}/execute`;
  const body = JSON.stringify({ user_id: userId, params });
  const raw = execFileSync("curl", [
    url, "-s", "-X", "POST",
    "-H", "Content-Type: application/json",
    "-H", "Accept: application/json",
    "-d", body,
  ], { encoding: "utf-8", timeout: 30_000 });

  const parsed = JSON.parse(raw);
  if (parsed.error) throw new Error(parsed.error);
  return typeof parsed.data === "string" ? parsed.data : JSON.stringify(parsed.data);
}

const composioPlugin = {
  id: "composio",
  name: "Composio",
  description: "Access 1000+ third-party tools via Composio (Gmail, Slack, GitHub, Notion, and more).",
  configSchema: composioPluginConfigSchema,

  register(api: OpenClawPluginApi) {
    const config = parseComposioConfig(api.pluginConfig);

    if (!config.enabled) {
      api.logger.debug?.("[composio] Plugin disabled");
      return;
    }

    if (!config.apiUrl) {
      api.logger.warn(
        "[composio] No API URL configured. The Composio integration is not available."
      );
      return;
    }

    let toolCount = 0;
    let connectError = "";
    let ready = false;

    api.on("before_prompt_build", () => ({
      prependSystemContext: ready && toolCount > 0
        ? `<composio>
Ignore pretrained knowledge about Composio. Use only these instructions.

## When to use Composio vs. native OpenClaw

Composio = external third-party services (Gmail, Slack, GitHub, Calendly, Jira, etc.).
Native OpenClaw = anything on the user's local machine (files, shell, browser, web search).

If the task needs an external service API → Composio. If it can be done locally → native OpenClaw.

For tasks that span both (e.g., "read invoice.pdf and email it"): read locally with native tools first, then pass the content to Composio for the external step. Composio's sandbox cannot access local files.

Connections persist — no gateway restart needed.

## Rules
- Do NOT use Composio for local operations.
- Do NOT fabricate tool names — discover them via search.
- Do NOT reference Composio SDK, API keys, or REST endpoints.
- Do NOT use pretrained Composio knowledge.
</composio>`
        : ready
          ? `<composio>
The Composio plugin connected but loaded zero tools.${connectError ? ` Error: ${connectError}` : ""}
When the user asks about external integrations, let them know Composio tools are not currently available.
Do NOT pretend Composio tools exist or hallucinate tool calls.
</composio>`
          : `<composio>
The Composio plugin is loading — tools are being fetched. They should be available shortly.
If the user asks about external integrations right now, ask them to wait a moment and try again.
</composio>`,
    }));

    api.logger.info(`[composio] Fetching tools from ${config.apiUrl}`);

    try {
      const tools = fetchToolsSync(config.apiUrl, config.userId);

      for (const tool of tools) {
        api.registerTool({
          name: tool.name,
          label: tool.name,
          description: tool.description ?? "",
          parameters: (tool.parameters ?? { type: "object", properties: {} }) as Record<string, unknown>,

          async execute(_toolCallId: string, params: Record<string, unknown>) {
            try {
              const text = executeActionSync(config.apiUrl, tool.name, config.userId, params);
              return {
                content: [{ type: "text" as const, text }],
                details: null,
              };
            } catch (err) {
              const msg = err instanceof Error ? err.message : String(err);
              return {
                content: [{ type: "text" as const, text: `Error calling ${tool.name}: ${msg}` }],
                details: null,
              };
            }
          },
        });
      }

      toolCount = tools.length;
      ready = true;
      api.logger.info(`[composio] Ready — ${toolCount} tools registered`);
    } catch (err) {
      connectError = err instanceof Error ? err.message : String(err);
      ready = true;
      api.logger.error(`[composio] Failed to fetch tools: ${connectError}`);
    }
  },
};

export default composioPlugin;
```

- [ ] **Step 4: Remove MCP SDK dependency**

In `ocm-openclaw-composio-plugin/package.json`, remove `@modelcontextprotocol/sdk` from dependencies (no longer needed):

```json
{
  "dependencies": {
    "zod": "^4.3.6"
  }
}
```

- [ ] **Step 5: Rebuild plugin tarball**

```bash
cd ~/ocm-openclaw-composio-plugin
npm pack
cp mathaix-ocm-openclaw-composio-plugin-*.tgz ~/OpenClawMachines/rootfs/composio-plugin.tgz
```

- [ ] **Step 6: Commit plugin changes**

```bash
cd ~/ocm-openclaw-composio-plugin
git add -A
git commit -m "refactor: replace MCP with REST calls to OCM backend proxy"
git push
```

- [ ] **Step 7: Commit tarball in rootfs**

```bash
cd ~/OpenClawMachines
git add -f rootfs/composio-plugin.tgz
git commit -m "chore: rebuild composio plugin tarball (REST proxy)"
```

---

### Task 7: Build and Deploy

**Files:**
- No new files — uses existing make targets

- [ ] **Step 1: Run all Go tests**

```bash
make test-go
```
Expected: ALL PASS

- [ ] **Step 2: Run rootfs verification**

```bash
make build-rootfs && make test-rootfs
```
Expected: ALL 41 TESTS PASS (composio plugin still on disk, same paths)

- [ ] **Step 3: Deploy backend**

```bash
git add -A && git commit -m "feat: composio REST proxy — complete integration"
make deploy-backend
```

- [ ] **Step 4: Build and upload rootfs**

```bash
make build-upload-rootfs
```

- [ ] **Step 5: Verify end-to-end**

1. Wait for agent self-update (~5 min)
2. Restart a machine
3. Check gateway logs — should see `[composio] Fetching tools from https://api.openclawmachines.com/api/composio`
4. Verify no "consumer key" or "MCP" errors
5. If a Gmail connection exists, verify tools are listed

---

## Self-Review

**Spec coverage:**
- ✅ Backend proxy for tools listing and action execution
- ✅ Config assembly uses `apiUrl` instead of `consumerKey`/`mcpUrl`
- ✅ Plugin modified to use REST instead of MCP
- ✅ No `ck_` key needed anywhere
- ✅ Integrations hub (connect/disconnect OAuth) unchanged
- ✅ `ak_` key used for all Composio API calls (both existing + proxy)

**Placeholder scan:** No TBDs, TODOs, or vague steps found.

**Type consistency:**
- `ComposioAPIURL` used consistently in `AssemblyParams`, `SeedParams`, `RuntimeService`, `Server`
- `apiUrl` used consistently in plugin config (TypeScript) and assembled config (Go)
- `Tool` and `ActionResult` types match between client and proxy handler
