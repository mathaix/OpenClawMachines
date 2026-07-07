package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

func TestWorkspaceIntegrationOpenAPIImport_PreviewSaveAndRuntime(t *testing.T) {
	const secretKey = "12345678901234567890123456789012"
	var sawReadAuth, sawWriteAuth bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") == "openapi-secret" {
			switch r.URL.Path {
			case "/records":
				if r.Method == http.MethodGet {
					sawReadAuth = true
					writeJSON(w, http.StatusOK, []map[string]interface{}{{"id": "rec-1", "title": "One"}})
					return
				}
				if r.Method == http.MethodPost {
					sawWriteAuth = true
					var body map[string]interface{}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatalf("decode upstream body: %v", err)
					}
					if body["title"] != "Two" {
						t.Fatalf("upstream body = %+v, want title Two", body)
					}
					writeJSON(w, http.StatusOK, map[string]interface{}{"id": "rec-2", "title": body["title"]})
					return
				}
			}
		}
		http.Error(w, "unexpected request", http.StatusTeapot)
	}))
	defer upstream.Close()

	spec := `
openapi: 3.0.3
info:
  title: Records API
  version: 1.0.0
servers:
  - url: ` + upstream.URL + `
paths:
  /records:
    get:
      operationId: list_records
      summary: List records
      parameters:
        - name: limit
          in: query
          schema:
            type: integer
            minimum: 1
            maximum: 50
      responses:
        "200":
          description: OK
    post:
      operationId: create_record
      summary: Create a record
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/RecordInput"
      responses:
        "200":
          description: OK
components:
  schemas:
    RecordInput:
      type: object
      properties:
        title:
          type: string
      required: [title]
      additionalProperties: false
`

	ms := newMockWorkspaceIntegrationManagementStore()
	ms.connectionCredentialsByLegacy = map[string][]store.WorkspaceIntegrationConnection{
		"integration-1": {{
			ID:          "connection-records-api",
			WorkspaceID: "workspace-1",
			Slug:        "records-api",
		}},
	}
	srv := &Server{
		store:     ms,
		secretKey: secretKey,
		allowInsecureWorkspaceIntegrationEndpoints: true,
		activity: events.New(ms, nil),
	}
	previewReq := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/import/preview", map[string]interface{}{
		"type":         "openapi",
		"slug":         "records-api",
		"display_name": "Records API",
		"spec_text":    spec,
		"auth": map[string]interface{}{
			"type":   "api_key_header",
			"header": "X-API-Key",
		},
	}, 1, "workspace-1", "", 7)
	previewW := httptest.NewRecorder()
	srv.handlePreviewWorkspaceIntegrationImport(previewW, previewReq)
	if previewW.Code != http.StatusOK {
		t.Fatalf("preview expected 200, got %d: %s", previewW.Code, previewW.Body.String())
	}
	var preview workspaceIntegrationImportPreviewResponse
	if err := json.NewDecoder(previewW.Body).Decode(&preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if preview.Kind != "openapi" || preview.AuthKind != "api_key_header" || len(preview.Tools) != 2 {
		t.Fatalf("preview = %+v", preview)
	}
	if preview.Tools[0].Source != "openapi" || preview.Tools[0].Access != "Read" || preview.Tools[1].Access != "Write" {
		t.Fatalf("preview tools = %+v", preview.Tools)
	}
	if len(preview.DeniedTools) != 1 || preview.DeniedTools[0] != "create_record" {
		t.Fatalf("denied tools = %+v, want create_record blocked by default", preview.DeniedTools)
	}

	createReq := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/import", map[string]interface{}{
		"type":         "openapi",
		"slug":         "records-api",
		"display_name": "Records API",
		"spec_text":    spec,
		"auth": map[string]interface{}{
			"type":   "api_key_header",
			"header": "X-API-Key",
		},
		"token": "openapi-secret",
	}, 1, "workspace-1", "", 7)
	createW := httptest.NewRecorder()
	srv.handleCreateWorkspaceIntegrationImport(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("create expected 200, got %d: %s", createW.Code, createW.Body.String())
	}
	if ms.upserted == nil || ms.upserted.Kind != "openapi" || ms.upserted.Transport != "http" {
		t.Fatalf("upserted = %+v", ms.upserted)
	}
	if strings.Contains(string(ms.upserted.Config), "openapi-secret") || strings.Contains(createW.Body.String(), "openapi-secret") {
		t.Fatalf("import leaked token in config/response")
	}
	if ms.credential == nil || ms.credential.SecretEnc == "" || ms.credential.SecretEnc == "openapi-secret" {
		t.Fatalf("credential was not encrypted: %+v", ms.credential)
	}
	if ms.connectionCredential == nil {
		t.Fatal("normalized connection credential was not saved")
	}
	if ms.connectionCredential.ConnectionID != "connection-records-api" {
		t.Fatalf("connection credential connection_id = %q, want connection-records-api", ms.connectionCredential.ConnectionID)
	}
	if ms.connectionCredential.SecretEnc == "" || ms.connectionCredential.SecretEnc == "openapi-secret" {
		t.Fatalf("connection credential was not encrypted: %+v", ms.connectionCredential)
	}
	normalizedSecret, err := crypto.Decrypt(ms.connectionCredential.SecretEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt connection credential: %v", err)
	}
	if normalizedSecret != "openapi-secret" {
		t.Fatalf("connection credential decrypted to %q, want openapi-secret", normalizedSecret)
	}

	gateway, fakeStore := newWorkspaceIntegrationGatewayServer()
	gateway.secretKey = secretKey
	fakeStore.credential = ms.credential
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{*ms.upserted}
	authHeader := workspaceIntegrationAuthHeader(t, gateway, "machine-123")

	searchReq := workspaceIntegrationCallRequest(t, workspaceIntegrationSearchToolsName, map[string]interface{}{
		"arguments": map[string]interface{}{"query": "list records", "integration": "records-api"},
	})
	searchReq.Header.Set("Authorization", authHeader)
	searchW := httptest.NewRecorder()
	gateway.handleWorkspaceIntegrationCallTool(searchW, searchReq)
	if searchW.Code != http.StatusOK {
		t.Fatalf("search expected 200, got %d: %s", searchW.Code, searchW.Body.String())
	}
	if strings.Contains(searchW.Body.String(), "openapi-secret") || !strings.Contains(searchW.Body.String(), `"source":"openapi"`) {
		t.Fatalf("search response = %s", searchW.Body.String())
	}

	readReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_id":   "records-api.list_records",
			"arguments": map[string]interface{}{"limit": 5},
		},
	})
	readReq.Header.Set("Authorization", authHeader)
	readW := httptest.NewRecorder()
	gateway.handleWorkspaceIntegrationCallTool(readW, readReq)
	if readW.Code != http.StatusOK {
		t.Fatalf("read expected 200, got %d: %s", readW.Code, readW.Body.String())
	}
	if !sawReadAuth {
		t.Fatal("fixture did not receive API key on read call")
	}

	blockedWriteReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_id":   "records-api.create_record",
			"arguments": map[string]interface{}{"body": map[string]interface{}{"title": "Two"}},
		},
	})
	blockedWriteReq.Header.Set("Authorization", authHeader)
	blockedWriteW := httptest.NewRecorder()
	gateway.handleWorkspaceIntegrationCallTool(blockedWriteW, blockedWriteReq)
	if blockedWriteW.Code != http.StatusNotFound {
		t.Fatalf("blocked write expected 404, got %d: %s", blockedWriteW.Code, blockedWriteW.Body.String())
	}

	enabled := fakeStore.integrations["machine-123"][0]
	enabled.DeniedTools = nil
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{enabled}
	writeReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_id":   "records-api.create_record",
			"arguments": map[string]interface{}{"body": map[string]interface{}{"title": "Two"}},
		},
	})
	writeReq.Header.Set("Authorization", authHeader)
	writeW := httptest.NewRecorder()
	gateway.handleWorkspaceIntegrationCallTool(writeW, writeReq)
	if writeW.Code != http.StatusOK {
		t.Fatalf("write expected 200 after policy enable, got %d: %s", writeW.Code, writeW.Body.String())
	}
	if !sawWriteAuth {
		t.Fatal("fixture did not receive API key on write call")
	}
}

func TestWorkspaceIntegrationOpenAPIImportRejectsNormalizedConnectionSlugCollision(t *testing.T) {
	spec := `
openapi: 3.0.3
info:
  title: Records API
  version: 1.0.0
servers:
  - url: https://records.example.test
paths:
  /records:
    get:
      operationId: list_records
      summary: List records
      responses:
        "200":
          description: OK
`

	ms := newMockWorkspaceIntegrationManagementStore()
	ms.connectorProjections = map[string][]store.WorkspaceIntegrationConnectorProjection{
		"workspace-1": {{
			Source: store.WorkspaceIntegrationSource{
				ID:          "source-openapi",
				WorkspaceID: "workspace-1",
				Slug:        "openapi",
				DisplayName: "OpenAPI",
				Kind:        "openapi",
				Importer:    "openapi",
			},
			Connection: store.WorkspaceIntegrationConnection{
				ID:              "connection-records-api",
				WorkspaceID:     "workspace-1",
				SourceID:        "source-openapi",
				Slug:            "records-api",
				DisplayName:     "Records API",
				Scope:           "workspace",
				CredentialState: "connected",
				Enabled:         true,
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{{
				ID:           "snapshot-list",
				WorkspaceID:  "workspace-1",
				ConnectionID: "connection-records-api",
				ToolName:     "list_records",
				ToolAddress:  "wi.workspace-1.openapi.records-api.list_records",
				Access:       "read",
				Source:       "openapi",
			}},
		}},
	}
	srv := &Server{store: ms}
	createReq := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/import", map[string]interface{}{
		"type":         "openapi",
		"slug":         "records-api",
		"display_name": "Records API",
		"spec_text":    spec,
	}, 1, "workspace-1", "", 7)
	createW := httptest.NewRecorder()

	srv.handleCreateWorkspaceIntegrationImport(createW, createReq)

	if createW.Code != http.StatusConflict {
		t.Fatalf("create expected 409, got %d: %s", createW.Code, createW.Body.String())
	}
	if ms.upserted != nil {
		t.Fatalf("slug collision should not upsert legacy integration: %+v", ms.upserted)
	}
	if !strings.Contains(createW.Body.String(), "connection slug already exists") {
		t.Fatalf("response = %s", createW.Body.String())
	}
}

func TestWorkspaceIntegrationOpenAPIImport_DuplicateConnectionsRouteByToolAddress(t *testing.T) {
	upstream := func(label string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/records" || r.Method != http.MethodGet {
				http.Error(w, "unexpected request", http.StatusTeapot)
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"connection": label})
		}))
	}
	primary := upstream("primary")
	defer primary.Close()
	secondary := upstream("secondary")
	defer secondary.Close()

	openAPISpec := func(baseURL string) string {
		return `
openapi: 3.0.3
info:
  title: Records API
  version: 1.0.0
servers:
  - url: ` + baseURL + `
paths:
  /records:
    get:
      operationId: list_records
      summary: List records
      responses:
        "200":
          description: OK
`
	}

	srv := &Server{allowInsecureWorkspaceIntegrationEndpoints: true}
	buildPrimary, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:        "openapi",
		Slug:        "records-primary",
		DisplayName: "Records Primary",
		SpecText:    openAPISpec(primary.URL),
	})
	if err != nil {
		t.Fatalf("build primary import: %v", err)
	}
	buildSecondary, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:        "openapi",
		Slug:        "records-secondary",
		DisplayName: "Records Secondary",
		SpecText:    openAPISpec(secondary.URL),
	})
	if err != nil {
		t.Fatalf("build secondary import: %v", err)
	}
	primaryIntegration := buildPrimary.Integration
	primaryIntegration.ID = "integration-records-primary"
	primaryIntegration.WorkspaceID = "workspace-1"
	primaryIntegration.Enabled = true
	secondaryIntegration := buildSecondary.Integration
	secondaryIntegration.ID = "integration-records-secondary"
	secondaryIntegration.WorkspaceID = "workspace-1"
	secondaryIntegration.Enabled = true

	gateway, fakeStore := newWorkspaceIntegrationGatewayServer()
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{primaryIntegration, secondaryIntegration}
	authHeader := workspaceIntegrationAuthHeader(t, gateway, "machine-123")

	searchReq := workspaceIntegrationCallRequest(t, workspaceIntegrationSearchToolsName, map[string]interface{}{
		"arguments": map[string]interface{}{"query": "list records", "integration": "openapi", "limit": 10},
	})
	searchReq.Header.Set("Authorization", authHeader)
	searchW := httptest.NewRecorder()
	gateway.handleWorkspaceIntegrationCallTool(searchW, searchReq)
	if searchW.Code != http.StatusOK {
		t.Fatalf("search expected 200, got %d: %s", searchW.Code, searchW.Body.String())
	}
	var searchResp struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(searchW.Body.Bytes(), &searchResp); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	addresses := map[string]string{}
	for _, item := range searchResp.Items {
		if item["name"] == "list_records" {
			addresses[fmt.Sprint(item["connection_slug"])] = fmt.Sprint(item["tool_address"])
		}
	}
	primaryAddress := addresses["records-primary"]
	secondaryAddress := addresses["records-secondary"]
	if primaryAddress == "" || secondaryAddress == "" || primaryAddress == secondaryAddress {
		t.Fatalf("search did not return distinct connection addresses: %+v", searchResp.Items)
	}

	describeReq := workspaceIntegrationCallRequest(t, workspaceIntegrationDescribeToolName, map[string]interface{}{
		"arguments": map[string]interface{}{"tool_address": secondaryAddress},
	})
	describeReq.Header.Set("Authorization", authHeader)
	describeW := httptest.NewRecorder()
	gateway.handleWorkspaceIntegrationCallTool(describeW, describeReq)
	if describeW.Code != http.StatusOK {
		t.Fatalf("describe expected 200, got %d: %s", describeW.Code, describeW.Body.String())
	}
	if !strings.Contains(describeW.Body.String(), `"connection_slug":"records-secondary"`) {
		t.Fatalf("describe selected wrong connection: %s", describeW.Body.String())
	}

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": secondaryAddress,
			"arguments":    map[string]interface{}{},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	gateway.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusOK {
		t.Fatalf("call expected 200, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"connection":"secondary"`) || strings.Contains(callW.Body.String(), `"connection":"primary"`) {
		t.Fatalf("call did not route by selected tool_address: %s", callW.Body.String())
	}
}

func TestWorkspaceIntegrationOpenAPIImport_RejectsOversizedRuntimeResponse(t *testing.T) {
	const responseLimit = 1 << 20
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/records" || r.Method != http.MethodGet {
			http.Error(w, "unexpected request", http.StatusTeapot)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		_, _ = w.Write([]byte(strings.Repeat(" ", responseLimit)))
	}))
	defer upstream.Close()

	spec := `
openapi: 3.0.3
info:
  title: Records API
  version: 1.0.0
servers:
  - url: ` + upstream.URL + `
paths:
  /records:
    get:
      operationId: list_records
      responses:
        "200":
          description: OK
`

	srv := &Server{allowInsecureWorkspaceIntegrationEndpoints: true}
	build, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:     "openapi",
		Slug:     "records-api",
		SpecText: spec,
	})
	if err != nil {
		t.Fatalf("build import: %v", err)
	}
	integration := build.Integration
	integration.ID = "integration-records-api"
	integration.WorkspaceID = "workspace-1"
	integration.Enabled = true

	gateway, fakeStore := newWorkspaceIntegrationGatewayServer()
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{integration}
	authHeader := workspaceIntegrationAuthHeader(t, gateway, "machine-123")

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_id":   "records-api.list_records",
			"arguments": map[string]interface{}{},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	gateway.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusBadGateway {
		t.Fatalf("oversized response expected 502, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), fmt.Sprintf("response exceeds %d bytes", responseLimit)) {
		t.Fatalf("oversized response error = %s", callW.Body.String())
	}
}

func TestWorkspaceIntegrationGraphQLImport_PreviewSaveAndRuntime(t *testing.T) {
	var sawVariables bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode graphql body: %v", err)
		}
		if variables, ok := body["variables"].(map[string]interface{}); ok && variables["id"] == "rec-1" {
			sawVariables = true
		}
		if !strings.Contains(body["query"].(string), "query Record") {
			t.Fatalf("query = %v", body["query"])
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"data": map[string]interface{}{"record": map[string]interface{}{"__typename": "Record"}}})
	}))
	defer upstream.Close()

	schema := `
type Query {
  record(id: ID!): Record
}
type Mutation {
  createRecord(input: RecordInput!): Record
}
input RecordInput {
  title: String!
}
type Record {
  id: ID!
  title: String
}
`

	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{
		store: ms,
		allowInsecureWorkspaceIntegrationEndpoints: true,
		activity: events.New(ms, nil),
	}
	createReq := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/import", map[string]interface{}{
		"type":         "graphql",
		"slug":         "records-graphql",
		"display_name": "Records GraphQL",
		"endpoint":     upstream.URL,
		"spec_text":    schema,
		"auth":         map[string]interface{}{"type": "none"},
	}, 1, "workspace-1", "", 7)
	createW := httptest.NewRecorder()
	srv.handleCreateWorkspaceIntegrationImport(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("create expected 200, got %d: %s", createW.Code, createW.Body.String())
	}
	if ms.upserted == nil || ms.upserted.Kind != "graphql" {
		t.Fatalf("upserted = %+v", ms.upserted)
	}
	if len(ms.upserted.DeniedTools) != 1 || ms.upserted.DeniedTools[0] != "createRecord" {
		t.Fatalf("graphql denied tools = %+v, want mutation blocked by default", ms.upserted.DeniedTools)
	}

	gateway, fakeStore := newWorkspaceIntegrationGatewayServer()
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{*ms.upserted}
	authHeader := workspaceIntegrationAuthHeader(t, gateway, "machine-123")

	searchReq := workspaceIntegrationCallRequest(t, workspaceIntegrationSearchToolsName, map[string]interface{}{
		"arguments": map[string]interface{}{"query": "create record", "integration": "records-graphql", "access": "write"},
	})
	searchReq.Header.Set("Authorization", authHeader)
	searchW := httptest.NewRecorder()
	gateway.handleWorkspaceIntegrationCallTool(searchW, searchReq)
	if searchW.Code != http.StatusOK {
		t.Fatalf("search expected 200, got %d: %s", searchW.Code, searchW.Body.String())
	}
	if strings.Contains(searchW.Body.String(), "createRecord") {
		t.Fatalf("blocked mutation appeared in search: %s", searchW.Body.String())
	}

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_id":   "records-graphql.record",
			"arguments": map[string]interface{}{"id": "rec-1"},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	gateway.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusOK {
		t.Fatalf("query expected 200, got %d: %s", callW.Code, callW.Body.String())
	}
	if !sawVariables {
		t.Fatal("fixture did not receive generated GraphQL variables")
	}
	if !strings.Contains(callW.Body.String(), `"__typename":"Record"`) {
		t.Fatalf("query response = %s", callW.Body.String())
	}
}

func TestWorkspaceIntegrationGraphQLImport_ParsesNoArgumentFields(t *testing.T) {
	srv := &Server{allowInsecureWorkspaceIntegrationEndpoints: true}
	build, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:     "graphql",
		Slug:     "status-graphql",
		Endpoint: "http://example.test/graphql",
		SpecText: `
type Query {
  status: String
  record(id: ID!): String
}
`,
	})
	if err != nil {
		t.Fatalf("build graphql import: %v", err)
	}
	names := map[string]bool{}
	var statusQuery string
	for _, tool := range build.Tools {
		names[tool.Name] = true
		if tool.Name == "status" && tool.Request != nil && tool.Request.Body != nil {
			statusQuery = tool.Request.Body.Query
		}
	}
	if !names["status"] || !names["record"] {
		t.Fatalf("generated GraphQL tools = %+v, want status and record", names)
	}
	if statusQuery != "query Status { status }" {
		t.Fatalf("status query = %q", statusQuery)
	}
}

func TestWorkspaceIntegrationGraphQLImport_ParsesCompactSDLFields(t *testing.T) {
	srv := &Server{allowInsecureWorkspaceIntegrationEndpoints: true}
	build, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:     "graphql",
		Slug:     "compact-graphql",
		Endpoint: "http://example.test/graphql",
		SpecText: `
schema { query: RootQuery mutation: RootMutation }
type RootQuery { status: String record(id: ID!): String }
type RootMutation { publish(input: PublishInput!): PublishResult }
input PublishInput { title: String! count: Int }
type PublishResult { id: ID! }
`,
	})
	if err != nil {
		t.Fatalf("build compact graphql import: %v", err)
	}
	names := map[string]bool{}
	var publishSchema map[string]interface{}
	for _, tool := range build.Tools {
		names[tool.Name] = true
		if tool.Name == "publish" {
			if err := json.Unmarshal(tool.Parameters, &publishSchema); err != nil {
				t.Fatalf("decode publish schema: %v", err)
			}
		}
	}
	for _, name := range []string{"status", "record", "publish"} {
		if !names[name] {
			t.Fatalf("generated GraphQL tools = %+v, missing %s", names, name)
		}
	}
	if len(build.DeniedTools) != 1 || build.DeniedTools[0] != "publish" {
		t.Fatalf("compact mutation denied tools = %+v, want publish", build.DeniedTools)
	}
	properties, _ := publishSchema["properties"].(map[string]interface{})
	input, _ := properties["input"].(map[string]interface{})
	inputProperties, _ := input["properties"].(map[string]interface{})
	if inputProperties["title"] == nil || inputProperties["count"] == nil {
		t.Fatalf("compact input schema did not parse all fields: %+v", publishSchema)
	}
}

func TestWorkspaceIntegrationGraphQLImport_UsesSchemaRootOperationTypes(t *testing.T) {
	srv := &Server{allowInsecureWorkspaceIntegrationEndpoints: true}
	build, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:     "graphql",
		Slug:     "custom-root-graphql",
		Endpoint: "http://example.test/graphql",
		SpecText: `
schema {
  query: RootQuery
  mutation: RootMutation
}
type RootQuery {
  status: String
}
type RootMutation {
  publish(input: PublishInput!): PublishResult
}
input PublishInput {
  title: String!
}
type PublishResult {
  id: ID!
}
`,
	})
	if err != nil {
		t.Fatalf("build graphql import: %v", err)
	}
	names := map[string]bool{}
	access := map[string]string{}
	for _, tool := range build.Tools {
		names[tool.Name] = true
		access[tool.Name] = tool.Access
	}
	if !names["status"] || access["status"] != "read" {
		t.Fatalf("custom query root did not generate read status tool: names=%+v access=%+v", names, access)
	}
	if !names["publish"] || access["publish"] != "write" {
		t.Fatalf("custom mutation root did not generate write publish tool: names=%+v access=%+v", names, access)
	}
	if len(build.DeniedTools) != 1 || build.DeniedTools[0] != "publish" {
		t.Fatalf("custom mutation root denied tools = %+v, want publish", build.DeniedTools)
	}
}

func TestWorkspaceIntegrationGraphQLImport_SchemaBlockDoesNotDefaultOmittedMutation(t *testing.T) {
	srv := &Server{allowInsecureWorkspaceIntegrationEndpoints: true}
	build, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:     "graphql",
		Slug:     "query-only-root-graphql",
		Endpoint: "http://example.test/graphql",
		SpecText: `
schema {
  query: RootQuery
}
type RootQuery {
  status: String
}
type Mutation {
  publish: String
}
`,
	})
	if err != nil {
		t.Fatalf("build graphql import: %v", err)
	}
	for _, tool := range build.Tools {
		if tool.Name == "publish" {
			t.Fatalf("schema block omitted mutation root but generated mutation tool: %+v", build.Tools)
		}
	}
}

func TestWorkspaceIntegrationGraphQLImport_ParsesSDLTypeModifiers(t *testing.T) {
	srv := &Server{allowInsecureWorkspaceIntegrationEndpoints: true}
	build, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:     "graphql",
		Slug:     "modifier-graphql",
		Endpoint: "http://example.test/graphql",
		SpecText: `
directive @tag on OBJECT
directive @oneOf on INPUT_OBJECT
interface Node {
  id: ID!
}
schema {
  query: RootQuery
  mutation: RootMutation
}
type RootQuery implements Node @tag {
  id: ID!
  status: String
}
type RootMutation @tag {
  publish(input: PublishInput!): PublishResult
}
input PublishInput @oneOf {
  title: String!
}
type PublishResult implements Node {
  id: ID!
}
`,
	})
	if err != nil {
		t.Fatalf("build graphql import: %v", err)
	}
	names := map[string]bool{}
	var publishSchema map[string]interface{}
	for _, tool := range build.Tools {
		names[tool.Name] = true
		if tool.Name == "publish" {
			if err := json.Unmarshal(tool.Parameters, &publishSchema); err != nil {
				t.Fatalf("decode publish schema: %v", err)
			}
		}
	}
	if !names["status"] || !names["publish"] {
		t.Fatalf("generated GraphQL tools = %+v, want status and publish", names)
	}
	properties, _ := publishSchema["properties"].(map[string]interface{})
	input, _ := properties["input"].(map[string]interface{})
	inputProperties, _ := input["properties"].(map[string]interface{})
	if inputProperties["title"] == nil {
		t.Fatalf("publish input schema did not parse directed input object: %+v", publishSchema)
	}
}

func TestWorkspaceIntegrationGraphQLImport_IntrospectionUsesRootOperationTypes(t *testing.T) {
	srv := &Server{allowInsecureWorkspaceIntegrationEndpoints: true}
	spec := `{
		"data": {
			"__schema": {
				"queryType": {"name": "RootQuery"},
				"mutationType": {"name": "RootMutation"},
				"types": [
					{
						"kind": "OBJECT",
						"name": "RootQuery",
						"fields": [
							{
								"name": "status",
								"args": [],
								"type": {"kind": "SCALAR", "name": "String", "ofType": null}
							}
						]
					},
					{
						"kind": "OBJECT",
						"name": "RootMutation",
						"fields": [
							{
								"name": "publish",
								"args": [
									{
										"name": "input",
										"type": {
											"kind": "NON_NULL",
											"name": null,
											"ofType": {"kind": "INPUT_OBJECT", "name": "PublishInput", "ofType": null}
										}
									}
								],
								"type": {"kind": "OBJECT", "name": "PublishResult", "ofType": null}
							}
						]
					},
					{
						"kind": "INPUT_OBJECT",
						"name": "PublishInput",
						"inputFields": [
							{
								"name": "title",
								"type": {
									"kind": "NON_NULL",
									"name": null,
									"ofType": {"kind": "SCALAR", "name": "String", "ofType": null}
								}
							}
						]
					}
				]
			}
		}
	}`
	build, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:     "graphql",
		Slug:     "introspection-root-graphql",
		Endpoint: "http://example.test/graphql",
		SpecText: spec,
	})
	if err != nil {
		t.Fatalf("build graphql introspection import: %v", err)
	}
	names := map[string]bool{}
	access := map[string]string{}
	var publishSchema map[string]interface{}
	for _, tool := range build.Tools {
		names[tool.Name] = true
		access[tool.Name] = tool.Access
		if tool.Name == "publish" {
			if err := json.Unmarshal(tool.Parameters, &publishSchema); err != nil {
				t.Fatalf("decode publish schema: %v", err)
			}
		}
	}
	if !names["status"] || access["status"] != "read" {
		t.Fatalf("introspection query root did not generate read status tool: names=%+v access=%+v", names, access)
	}
	if !names["publish"] || access["publish"] != "write" {
		t.Fatalf("introspection mutation root did not generate write publish tool: names=%+v access=%+v", names, access)
	}
	if len(build.DeniedTools) != 1 || build.DeniedTools[0] != "publish" {
		t.Fatalf("introspection mutation denied tools = %+v, want publish", build.DeniedTools)
	}
	properties, _ := publishSchema["properties"].(map[string]interface{})
	input, _ := properties["input"].(map[string]interface{})
	inputProperties, _ := input["properties"].(map[string]interface{})
	if inputProperties["title"] == nil {
		t.Fatalf("publish input schema did not expand introspection input object: %+v", publishSchema)
	}
}

func TestWorkspaceIntegrationGraphQLImport_RejectsInvalidIntrospectionNames(t *testing.T) {
	srv := &Server{allowInsecureWorkspaceIntegrationEndpoints: true}
	spec := `{
		"data": {
			"__schema": {
				"queryType": {"name": "Query"},
				"types": [
					{
						"kind": "OBJECT",
						"name": "Query",
						"fields": [
							{
								"name": "status } mutation Evil { publish",
								"args": [],
								"type": {"kind": "SCALAR", "name": "String", "ofType": null}
							}
						]
					}
				]
			}
		}
	}`
	_, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:     "graphql",
		Slug:     "invalid-introspection-graphql",
		Endpoint: "http://example.test/graphql",
		SpecText: spec,
	})
	if err == nil || !strings.Contains(err.Error(), `invalid GraphQL field name "status } mutation Evil { publish"`) {
		t.Fatalf("expected invalid GraphQL field name rejection, got %v", err)
	}
}

func TestWorkspaceIntegrationGraphQLImport_RejectsInvalidSDLNames(t *testing.T) {
	srv := &Server{allowInsecureWorkspaceIntegrationEndpoints: true}
	tests := []struct {
		name    string
		spec    string
		wantErr string
	}{
		{
			name: "field",
			spec: `
type Query {
  status: String
  bad-name: String
}
`,
			wantErr: `invalid GraphQL field name "bad-name"`,
		},
		{
			name: "argument",
			spec: `
type Query {
  status(bad-name: String): String
}
`,
			wantErr: `invalid GraphQL argument name "bad-name" on field "status"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
				Type:     "graphql",
				Slug:     "invalid-sdl-graphql-" + tt.name,
				Endpoint: "http://example.test/graphql",
				SpecText: tt.spec,
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestWorkspaceIntegrationGraphQLImport_DuplicateConnectionsRouteByToolAddress(t *testing.T) {
	upstream := func(label string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method", http.StatusMethodNotAllowed)
				return
			}
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode graphql body: %v", err)
			}
			if !strings.Contains(fmt.Sprint(body["query"]), "query Record") {
				t.Fatalf("query = %v", body["query"])
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"data": map[string]interface{}{"record": label}})
		}))
	}
	primary := upstream("primary")
	defer primary.Close()
	secondary := upstream("secondary")
	defer secondary.Close()

	const schema = `
type Query {
  record: String
}
`

	srv := &Server{allowInsecureWorkspaceIntegrationEndpoints: true}
	buildPrimary, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:        "graphql",
		Slug:        "graphql-primary",
		DisplayName: "GraphQL Primary",
		Endpoint:    primary.URL,
		SpecText:    schema,
	})
	if err != nil {
		t.Fatalf("build primary import: %v", err)
	}
	buildSecondary, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:        "graphql",
		Slug:        "graphql-secondary",
		DisplayName: "GraphQL Secondary",
		Endpoint:    secondary.URL,
		SpecText:    schema,
	})
	if err != nil {
		t.Fatalf("build secondary import: %v", err)
	}
	primaryIntegration := buildPrimary.Integration
	primaryIntegration.ID = "integration-graphql-primary"
	primaryIntegration.WorkspaceID = "workspace-1"
	primaryIntegration.Enabled = true
	secondaryIntegration := buildSecondary.Integration
	secondaryIntegration.ID = "integration-graphql-secondary"
	secondaryIntegration.WorkspaceID = "workspace-1"
	secondaryIntegration.Enabled = true

	gateway, fakeStore := newWorkspaceIntegrationGatewayServer()
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{primaryIntegration, secondaryIntegration}
	authHeader := workspaceIntegrationAuthHeader(t, gateway, "machine-123")

	searchReq := workspaceIntegrationCallRequest(t, workspaceIntegrationSearchToolsName, map[string]interface{}{
		"arguments": map[string]interface{}{"query": "record", "integration": "graphql", "limit": 10},
	})
	searchReq.Header.Set("Authorization", authHeader)
	searchW := httptest.NewRecorder()
	gateway.handleWorkspaceIntegrationCallTool(searchW, searchReq)
	if searchW.Code != http.StatusOK {
		t.Fatalf("search expected 200, got %d: %s", searchW.Code, searchW.Body.String())
	}
	var searchResp struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(searchW.Body.Bytes(), &searchResp); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	addresses := map[string]string{}
	for _, item := range searchResp.Items {
		if item["name"] == "record" {
			addresses[fmt.Sprint(item["connection_slug"])] = fmt.Sprint(item["tool_address"])
		}
	}
	primaryAddress := addresses["graphql-primary"]
	secondaryAddress := addresses["graphql-secondary"]
	if primaryAddress == "" || secondaryAddress == "" || primaryAddress == secondaryAddress {
		t.Fatalf("search did not return distinct connection addresses: %+v", searchResp.Items)
	}

	describeReq := workspaceIntegrationCallRequest(t, workspaceIntegrationDescribeToolName, map[string]interface{}{
		"arguments": map[string]interface{}{"tool_address": secondaryAddress},
	})
	describeReq.Header.Set("Authorization", authHeader)
	describeW := httptest.NewRecorder()
	gateway.handleWorkspaceIntegrationCallTool(describeW, describeReq)
	if describeW.Code != http.StatusOK {
		t.Fatalf("describe expected 200, got %d: %s", describeW.Code, describeW.Body.String())
	}
	if !strings.Contains(describeW.Body.String(), `"connection_slug":"graphql-secondary"`) {
		t.Fatalf("describe selected wrong connection: %s", describeW.Body.String())
	}

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": secondaryAddress,
			"arguments":    map[string]interface{}{},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	gateway.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusOK {
		t.Fatalf("call expected 200, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"record":"secondary"`) || strings.Contains(callW.Body.String(), `"record":"primary"`) {
		t.Fatalf("call did not route by selected tool_address: %s", callW.Body.String())
	}
}

func TestWorkspaceIntegrationImportRejectsUnsafeURLs(t *testing.T) {
	srv := &Server{}
	spec := `{"openapi":"3.0.3","info":{"title":"Unsafe","version":"1"},"paths":{"/x":{"get":{"operationId":"get_x","responses":{"200":{"description":"ok"}}}}}}`
	if _, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:     "openapi",
		Slug:     "unsafe-api",
		SpecText: spec,
		BaseURL:  "http://127.0.0.1:8080",
	}); err == nil {
		t.Fatal("expected private base_url to be rejected")
	}
	if _, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:    "openapi",
		Slug:    "unsafe-api",
		SpecURL: "http://127.0.0.1/openapi.json",
		BaseURL: "https://api.example.com",
	}); err == nil {
		t.Fatal("expected unsafe spec_url to be rejected")
	}
	if _, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:     "openapi",
		Slug:     "unsafe-api",
		SpecText: spec,
		BaseURL:  "https://api.example.com",
		Auth: workspaceIntegrationImportAuth{
			Type:     "oauth",
			TokenURL: "http://127.0.0.1/token",
		},
	}); err == nil {
		t.Fatal("expected unsafe oauth token_url to be rejected")
	}
}

func TestWorkspaceIntegrationOpenAPIImportRejectsRemoteRefsBeforeSkipping(t *testing.T) {
	srv := &Server{allowInsecureWorkspaceIntegrationEndpoints: true}
	spec := `{
		"openapi":"3.0.3",
		"info":{"title":"Remote ref","version":"1"},
		"servers":[{"url":"http://example.test"}],
		"paths":{
			"/records":{"get":{"operationId":"list_records","responses":{"200":{"description":"ok"}}}},
			"/external":{"$ref":"https://schemas.example.test/openapi/paths.yaml#/ExternalPath"}
		}
	}`

	_, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:     "openapi",
		Slug:     "remote-ref-api",
		SpecText: spec,
	})
	if err == nil || !strings.Contains(err.Error(), `remote $ref "https://schemas.example.test/openapi/paths.yaml#/ExternalPath" is not supported`) {
		t.Fatalf("expected remote $ref rejection, got %v", err)
	}
}

func TestWorkspaceIntegrationOpenAPIImportRejectsNonHTTPSchemeServerURL(t *testing.T) {
	srv := &Server{allowInsecureWorkspaceIntegrationEndpoints: true}
	spec := `{
		"openapi":"3.0.3",
		"info":{"title":"Bad scheme","version":"1"},
		"servers":[{"url":"ftp://api.example.test"}],
		"paths":{
			"/records":{"get":{"operationId":"list_records","responses":{"200":{"description":"ok"}}}}
		}
	}`

	_, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:     "openapi",
		Slug:     "bad-scheme-api",
		SpecText: spec,
	})
	if err == nil || !strings.Contains(err.Error(), "endpoint must be an absolute http or https URL") {
		t.Fatalf("expected non-http endpoint rejection, got %v", err)
	}
}

func TestWorkspaceIntegrationImportRejectsTooManyGeneratedTools(t *testing.T) {
	srv := &Server{allowInsecureWorkspaceIntegrationEndpoints: true}

	var openAPI strings.Builder
	openAPI.WriteString(`{"openapi":"3.0.3","info":{"title":"Large","version":"1"},"servers":[{"url":"http://example.test"}],"paths":{`)
	for i := 0; i <= workspaceIntegrationImportMaxGeneratedTools; i++ {
		if i > 0 {
			openAPI.WriteString(",")
		}
		fmt.Fprintf(&openAPI, `"/records/%d":{"get":{"operationId":"get_record_%d","responses":{"200":{"description":"ok"}}}}`, i, i)
	}
	openAPI.WriteString("}}")
	if _, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:     "openapi",
		Slug:     "too-many-openapi",
		SpecText: openAPI.String(),
	}); err == nil || !strings.Contains(err.Error(), "generated") {
		t.Fatalf("expected OpenAPI generated-tool limit error, got %v", err)
	}

	var graphQL strings.Builder
	graphQL.WriteString("type Query {\n")
	for i := 0; i <= workspaceIntegrationImportMaxGeneratedTools; i++ {
		fmt.Fprintf(&graphQL, "  record%d: String\n", i)
	}
	graphQL.WriteString("}\n")
	if _, err := srv.buildWorkspaceIntegrationImport(t.Context(), workspaceIntegrationImportRequest{
		Type:     "graphql",
		Slug:     "too-many-graphql",
		Endpoint: "http://example.test/graphql",
		SpecText: graphQL.String(),
	}); err == nil || !strings.Contains(err.Error(), "generated") {
		t.Fatalf("expected GraphQL generated-tool limit error, got %v", err)
	}
}
