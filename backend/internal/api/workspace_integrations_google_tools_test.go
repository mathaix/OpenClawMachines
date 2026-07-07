package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

func googleWorkspaceManifestToolNames(t *testing.T, levels map[string]string) map[string]bool {
	t.Helper()
	var tools []workspaceIntegrationManifestTool
	if err := json.Unmarshal(googleWorkspaceToolManifest(levels), &tools); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}
	return names
}

func TestWorkspaceIntegrationGenericCreateCuratedSlugGuard(t *testing.T) {
	githubURL := workspaceIntegrationCatalogRemoteURL("github")
	if githubURL == "" {
		t.Skip("github is not a curated remote-MCP catalog entry")
	}
	// Squatting a reserved slug with a foreign endpoint is rejected at the guard.
	if _, err := workspaceIntegrationFromGenericCreateRequest("github", genericWorkspaceIntegrationCreateRequest{
		Transport: "mcp-remote",
		Endpoint:  "https://squatter.example.com/mcp",
	}); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("squatting the github slug should be rejected, got %v", err)
	}
	// The curated entry targeting its OWN endpoint passes the slug guard (it may
	// still fail later on endpoint/DNS validation, but never with "reserved").
	if _, err := workspaceIntegrationFromGenericCreateRequest("github", genericWorkspaceIntegrationCreateRequest{
		Transport:    "mcp-remote",
		Endpoint:     githubURL,
		ToolManifest: json.RawMessage("[]"),
	}); err != nil && strings.Contains(err.Error(), "reserved") {
		t.Fatalf("curated github with its own endpoint must pass the slug guard: %v", err)
	}
}

func TestWorkspaceIntegrationProbeToolCapturesInputSchema(t *testing.T) {
	// MCP tools/list returns the schema as "inputSchema" (camelCase). If the tag
	// is wrong the schema is silently dropped and agents can't build valid args.
	raw := []byte(`{"name":"notion-create-pages","description":"Create pages","inputSchema":{"type":"object","properties":{"pages":{"type":"array"}},"required":["pages"]}}`)
	var tool workspaceIntegrationProbeTool
	if err := json.Unmarshal(raw, &tool); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(tool.InputSchema) == 0 {
		t.Fatal("inputSchema not captured from tools/list (MCP uses camelCase)")
	}
	if !strings.Contains(string(tool.InputSchema), `"pages"`) {
		t.Fatalf("inputSchema = %s, want the pages array spec", tool.InputSchema)
	}
}

func TestGoogleWorkspaceToolManifestIsPermissionAware(t *testing.T) {
	// Default (nil) → gmail read, drive read, calendar read, docs off.
	def := googleWorkspaceManifestToolNames(t, nil)
	for _, want := range []string{"gmail_profile", "gmail_list_messages", "gmail_get_message", "drive_list_files", "calendar_list_events"} {
		if !def[want] {
			t.Fatalf("default manifest missing %q: %v", want, def)
		}
	}
	for _, absent := range []string{"gmail_send_message", "drive_create_file", "calendar_create_event", "docs_get_document", "docs_create_document"} {
		if def[absent] {
			t.Fatalf("default manifest unexpectedly contains %q: %v", absent, def)
		}
	}

	// All read_write → every read and write tool present.
	full := googleWorkspaceManifestToolNames(t, map[string]string{"gmail": "read_write", "drive": "read_write", "calendar": "read_write", "docs": "read_write"})
	for _, want := range []string{
		"gmail_send_message", "drive_create_file", "calendar_create_event",
		"docs_get_document", "docs_create_document",
	} {
		if !full[want] {
			t.Fatalf("read_write manifest missing %q: %v", want, full)
		}
	}

	// All off → no tools.
	off := googleWorkspaceManifestToolNames(t, map[string]string{"gmail": "off", "drive": "off", "calendar": "off", "docs": "off"})
	if len(off) != 0 {
		t.Fatalf("off manifest should expose no tools, got %v", off)
	}

	// Gmail read must not expose the send tool (no over-capability vs scope).
	readOnly := googleWorkspaceManifestToolNames(t, map[string]string{"gmail": "read"})
	if readOnly["gmail_send_message"] {
		t.Fatalf("gmail read exposed send tool: %v", readOnly)
	}
}

func TestWorkspaceIntegrationSearchToolsSemanticMatchesGoogleWorkspaceIntent(t *testing.T) {
	integration := store.WorkspaceIntegration{
		ID:           "integration-google",
		WorkspaceID:  "workspace-1",
		Slug:         "google-workspace",
		DisplayName:  "Google Workspace",
		Kind:         "google_workspace",
		Transport:    "http",
		Enabled:      true,
		ToolManifest: googleWorkspaceToolManifest(map[string]string{"gmail": "read_write", "calendar": "read", "drive": "read"}),
	}
	tests := []struct {
		name   string
		query  string
		access string
		want   string
	}{
		{
			name:   "gmail list intent",
			query:  "list inbox messages",
			access: "read",
			want:   "google-workspace.gmail_list_messages",
		},
		{
			name:   "calendar meeting intent",
			query:  "schedule meetings",
			access: "read",
			want:   "google-workspace.calendar_list_events",
		},
		{
			name:   "gmail send intent",
			query:  "send email",
			access: "write",
			want:   "google-workspace.gmail_send_message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, err := searchWorkspaceIntegrationFacadeTools([]store.WorkspaceIntegration{integration}, map[string]interface{}{
				"query":  tt.query,
				"access": tt.access,
				"method": "semantic",
				"limit":  5,
			})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(items) == 0 {
				t.Fatalf("search returned no items for %q", tt.query)
			}
			if got := items[0]["tool_id"]; got != tt.want {
				t.Fatalf("first tool = %v, want %s; items = %+v", got, tt.want, items)
			}
			if got := items[0]["match_method"]; got != workspaceIntegrationSearchMethodSemantic {
				t.Fatalf("match_method = %v, want semantic", got)
			}
			score, ok := items[0]["score"].(float64)
			if !ok || score <= 0 {
				t.Fatalf("score = %v, want positive float64", items[0]["score"])
			}
		})
	}
}

func TestWorkspaceIntegrationSearchToolsRejectsUnknownMethod(t *testing.T) {
	_, err := searchWorkspaceIntegrationFacadeTools(nil, map[string]interface{}{
		"query":  "email",
		"method": "embedding",
	})
	if err == nil || !strings.Contains(err.Error(), "method must be semantic, keyword, or hybrid") {
		t.Fatalf("expected method validation error, got %v", err)
	}
}

func TestBuildWorkspaceIntegrationHTTPRequestBody_JSON(t *testing.T) {
	spec := &workspaceIntegrationHTTPBody{
		Fields: []workspaceIntegrationHTTPBodyField{
			{Path: "summary", Required: true, Value: workspaceIntegrationHTTPValue{Source: "arg", Name: "summary"}},
			{Path: "description", Value: workspaceIntegrationHTTPValue{Source: "arg", Name: "description", OmitEmpty: true}},
			{Path: "start.dateTime", Required: true, Value: workspaceIntegrationHTTPValue{Source: "arg", Name: "start"}},
			{Path: "parents", Array: true, Value: workspaceIntegrationHTTPValue{Source: "arg", Name: "folder_id", Default: "root"}},
		},
	}
	data, contentType, err := buildWorkspaceIntegrationHTTPRequestBody(spec, map[string]interface{}{}, map[string]interface{}{
		"summary": "Sync",
		"start":   "2026-07-01T10:00:00-07:00",
	})
	if err != nil {
		t.Fatalf("build body: %v", err)
	}
	if contentType != "application/json" {
		t.Fatalf("content type = %q", contentType)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if decoded["summary"] != "Sync" {
		t.Fatalf("summary = %v", decoded["summary"])
	}
	if _, present := decoded["description"]; present {
		t.Fatalf("empty optional description should be omitted: %v", decoded)
	}
	start, ok := decoded["start"].(map[string]interface{})
	if !ok || start["dateTime"] != "2026-07-01T10:00:00-07:00" {
		t.Fatalf("nested start = %v", decoded["start"])
	}
	parents, ok := decoded["parents"].([]interface{})
	if !ok || len(parents) != 1 || parents[0] != "root" {
		t.Fatalf("parents = %v", decoded["parents"])
	}
}

func TestBuildWorkspaceIntegrationHTTPRequestBody_RequiredMissing(t *testing.T) {
	spec := &workspaceIntegrationHTTPBody{
		Fields: []workspaceIntegrationHTTPBodyField{
			{Path: "title", Required: true, Value: workspaceIntegrationHTTPValue{Source: "arg", Name: "title"}},
		},
	}
	if _, _, err := buildWorkspaceIntegrationHTTPRequestBody(spec, map[string]interface{}{}, map[string]interface{}{}); err == nil {
		t.Fatal("expected required field error")
	}
}

func TestBuildWorkspaceIntegrationGmailRawBody(t *testing.T) {
	data, contentType, err := buildWorkspaceIntegrationHTTPRequestBody(
		&workspaceIntegrationHTTPBody{Encoding: "gmail_raw"},
		map[string]interface{}{},
		map[string]interface{}{"to": "alice@example.com", "subject": "Hi", "body": "Hello there"},
	)
	if err != nil {
		t.Fatalf("build gmail body: %v", err)
	}
	if contentType != "application/json" {
		t.Fatalf("content type = %q", contentType)
	}
	var payload map[string]string
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload["raw"])
	if err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	message := string(raw)
	for _, want := range []string{"To: alice@example.com", "Subject: Hi", "Hello there"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q: %q", want, message)
		}
	}
}

func TestBuildWorkspaceIntegrationGmailRawBody_RejectsInjectionAndMissingTo(t *testing.T) {
	if _, _, err := buildWorkspaceIntegrationHTTPRequestBody(
		&workspaceIntegrationHTTPBody{Encoding: "gmail_raw"},
		map[string]interface{}{},
		map[string]interface{}{"subject": "Hi"},
	); err == nil {
		t.Fatal("expected error when 'to' missing")
	}
	if _, _, err := buildWorkspaceIntegrationHTTPRequestBody(
		&workspaceIntegrationHTTPBody{Encoding: "gmail_raw"},
		map[string]interface{}{},
		map[string]interface{}{"to": "a@b.com\r\nBcc: evil@example.com", "subject": "Hi"},
	); err == nil {
		t.Fatal("expected header-injection rejection")
	}
}

func TestWorkspaceIntegrationCallTool_GmailSend(t *testing.T) {
	var sentRaw string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/gmail/v1/users/me/messages/send" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content type = %q", r.Header.Get("Content-Type"))
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		sentRaw = body["raw"]
		writeJSON(w, http.StatusOK, map[string]interface{}{"id": "sent-1"})
	}))
	defer api.Close()

	previous := googleGmailAPIBaseURL
	googleGmailAPIBaseURL = api.URL + "/gmail/v1"
	defer func() { googleGmailAPIBaseURL = previous }()

	manifest := googleWorkspaceToolManifest(map[string]string{"gmail": "read_write"})

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

	req := workspaceIntegrationCallRequest(t, "google-workspace.gmail_send_message", map[string]interface{}{
		"arguments": map[string]interface{}{"to": "alice@example.com", "subject": "Status", "body": "Done"},
	})
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	raw, err := base64.RawURLEncoding.DecodeString(sentRaw)
	if err != nil {
		t.Fatalf("decode sent raw: %v", err)
	}
	if !strings.Contains(string(raw), "To: alice@example.com") {
		t.Fatalf("sent message = %q", string(raw))
	}
}

func TestWorkspaceIntegrationCallTool_CalendarCreateEvent(t *testing.T) {
	var gotBody map[string]interface{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/calendar/v3/calendars/primary/events" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"id": "event-1"})
	}))
	defer api.Close()

	previous := googleCalendarAPIBaseURL
	googleCalendarAPIBaseURL = api.URL + "/calendar/v3"
	defer func() { googleCalendarAPIBaseURL = previous }()

	manifest := googleWorkspaceToolManifest(map[string]string{"calendar": "read_write"})

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

	req := workspaceIntegrationCallRequest(t, "google-workspace.calendar_create_event", map[string]interface{}{
		"arguments": map[string]interface{}{
			"summary": "Planning",
			"start":   "2026-07-01T10:00:00-07:00",
			"end":     "2026-07-01T11:00:00-07:00",
		},
	})
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if gotBody["summary"] != "Planning" {
		t.Fatalf("summary = %v", gotBody["summary"])
	}
	start, ok := gotBody["start"].(map[string]interface{})
	if !ok || start["dateTime"] != "2026-07-01T10:00:00-07:00" {
		t.Fatalf("start = %v", gotBody["start"])
	}
}
