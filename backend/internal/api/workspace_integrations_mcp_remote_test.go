package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

func TestExtractWorkspaceIntegrationMCPSSEData(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"single data line", "event: message\ndata: {\"id\":1}\n", `{"id":1}`},
		{"leading space stripped", "data: {\"a\":1}\n", `{"a":1}`},
		{"multi-line data joined", "data: {\ndata: \"a\":1}\n", "{\n\"a\":1}"},
		{"last event wins", "data: {\"n\":1}\n\ndata: {\"n\":2}\n", `{"n":2}`},
		{"crlf line endings", "event: message\r\ndata: {\"id\":9}\r\n", `{"id":9}`},
		{"no data frame", "event: ping\n: comment\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(extractWorkspaceIntegrationMCPSSEData([]byte(tc.in)))
			if got != tc.want {
				t.Fatalf("extractSSE(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCallMCPRemoteSendsInitializedBeforeToolsCall(t *testing.T) {
	var methods []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			ID     any    `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		methods = append(methods, req.Method)
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-123")
			writeJSON(w, http.StatusOK, map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"protocolVersion": workspaceIntegrationMCPProtocolVersion},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			if got := r.Header.Get("Mcp-Session-Id"); got != "sess-123" {
				t.Errorf("tools/call missing session id, got %q", got)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"ok": true},
			})
		default:
			t.Errorf("unexpected method %q", req.Method)
		}
	}))
	defer upstream.Close()

	endpoint := upstream.URL
	srv := &Server{allowInsecureWorkspaceIntegrationEndpoints: true}
	integration := store.WorkspaceIntegration{
		Slug: "demo", Transport: "mcp-remote", Endpoint: &endpoint,
	}
	tool := workspaceIntegrationManifestTool{Name: "search"}

	result, err := srv.callMCPRemoteWorkspaceIntegration(context.Background(), integration, tool, map[string]interface{}{"q": "x"})
	if err != nil {
		t.Fatalf("callMCPRemote: %v", err)
	}
	if result["ok"] != true {
		t.Fatalf("result = %+v, want ok=true", result)
	}
	if len(methods) != 3 || methods[0] != "initialize" || methods[1] != "notifications/initialized" || methods[2] != "tools/call" {
		t.Fatalf("method order = %v, want [initialize notifications/initialized tools/call]", methods)
	}
}
