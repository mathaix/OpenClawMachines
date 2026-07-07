package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

type mockWorkspaceIntegrationManagementStore struct {
	store.Store

	workspace                       store.Workspace
	integrations                    []store.WorkspaceIntegration
	machines                        map[string]*store.Machine
	plugins                         map[string][]store.MachinePlugin
	memberRole                      string
	credential                      *store.WorkspaceIntegrationCredential
	connectionCredential            *store.WorkspaceIntegrationConnectionCredential
	connectionCredentialsByLegacy   map[string][]store.WorkspaceIntegrationConnection
	connectorProjections            map[string][]store.WorkspaceIntegrationConnectorProjection
	replacedPolicyConnectionID      string
	replacedPolicies                []store.WorkspaceIntegrationToolPolicy
	deletedConnection               *store.WorkspaceIntegrationConnectorProjection
	credentialErr                   error
	credentialSetErr                error
	upserted                        *store.WorkspaceIntegration
	deleted                         *store.WorkspaceIntegration
	created                         *store.Workspace
	healthRows                      []store.WorkspaceIntegrationToolHealth
	healthQuery                     store.WorkspaceIntegrationHealthQuery
	healthAccountID                 int
	healthWorkspaceID               string
	guidanceRows                    []store.WorkspaceIntegrationGuidanceOverlay
	guidanceStatus                  string
	guidanceAccountID               int
	guidanceWorkspaceID             string
	createdGuidance                 *store.WorkspaceIntegrationGuidanceOverlay
	draftedGuidanceSince            time.Time
	draftedGuidanceLimit            int
	draftedGuidanceBy               *int
	approvedGuidanceID              string
	approvedGuidanceBy              int
	activities                      chan store.ActivityLog
	revokedWorkspaceTokenWorkspaces []string
}

func newMockWorkspaceIntegrationManagementStore() *mockWorkspaceIntegrationManagementStore {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	return &mockWorkspaceIntegrationManagementStore{
		workspace: store.Workspace{
			ID:        "workspace-1",
			AccountID: 1,
			Slug:      "default",
			Name:      "Default",
			CreatedAt: now,
			UpdatedAt: now,
		},
		machines:   make(map[string]*store.Machine),
		plugins:    make(map[string][]store.MachinePlugin),
		activities: make(chan store.ActivityLog, 10),
	}
}

func (m *mockWorkspaceIntegrationManagementStore) GetOrCreateDefaultWorkspace(_ context.Context, accountID int) (*store.Workspace, error) {
	if m.workspace.ID == "" {
		return nil, fmt.Errorf("workspace not configured")
	}
	w := m.workspace
	w.AccountID = accountID
	return &w, nil
}

func (m *mockWorkspaceIntegrationManagementStore) ListWorkspacesByAccount(_ context.Context, accountID int) ([]store.Workspace, error) {
	w := m.workspace
	w.AccountID = accountID
	return []store.Workspace{w}, nil
}

func (m *mockWorkspaceIntegrationManagementStore) GetWorkspace(_ context.Context, accountID int, workspaceID string) (*store.Workspace, error) {
	if m.workspace.ID != workspaceID {
		return nil, fmt.Errorf("not found")
	}
	w := m.workspace
	w.AccountID = accountID
	return &w, nil
}

func (m *mockWorkspaceIntegrationManagementStore) GetAccountMember(_ context.Context, accountID, userID int) (*store.AccountMember, error) {
	if accountID != 1 || userID == 0 {
		return nil, pgx.ErrNoRows
	}
	role := m.memberRole
	if role == "" {
		role = "owner"
	}
	return &store.AccountMember{AccountID: accountID, UserID: userID, Role: role}, nil
}

func (m *mockWorkspaceIntegrationManagementStore) CreateWorkspace(_ context.Context, workspace *store.Workspace) error {
	workspace.ID = "workspace-created"
	workspace.CreatedAt = time.Date(2026, 6, 16, 13, 0, 0, 0, time.UTC)
	workspace.UpdatedAt = workspace.CreatedAt
	m.created = workspace
	return nil
}

func (m *mockWorkspaceIntegrationManagementStore) ListWorkspaceIntegrations(_ context.Context, workspaceID string) ([]store.WorkspaceIntegration, error) {
	var out []store.WorkspaceIntegration
	for _, integration := range m.integrations {
		if integration.WorkspaceID == workspaceID {
			out = append(out, integration)
		}
	}
	return out, nil
}

func (m *mockWorkspaceIntegrationManagementStore) UpsertWorkspaceIntegration(_ context.Context, integration *store.WorkspaceIntegration) error {
	now := time.Date(2026, 6, 16, 13, 0, 0, 0, time.UTC)
	if integration.ID == "" {
		integration.ID = "integration-1"
	}
	integration.CreatedAt = now
	integration.UpdatedAt = now
	m.upserted = integration
	for i := range m.integrations {
		if m.integrations[i].WorkspaceID == integration.WorkspaceID && m.integrations[i].Slug == integration.Slug {
			m.integrations[i] = *integration
			return nil
		}
	}
	m.integrations = append(m.integrations, *integration)
	return nil
}

func (m *mockWorkspaceIntegrationManagementStore) DeleteWorkspaceIntegration(_ context.Context, workspaceID, slug string) (*store.WorkspaceIntegration, error) {
	for i, integration := range m.integrations {
		if integration.WorkspaceID != workspaceID || integration.Slug != slug {
			continue
		}
		deleted := integration
		m.integrations = append(m.integrations[:i], m.integrations[i+1:]...)
		m.deleted = &deleted
		return &deleted, nil
	}
	return nil, pgx.ErrNoRows
}

func (m *mockWorkspaceIntegrationManagementStore) DeleteWorkspaceIntegrationConnection(_ context.Context, workspaceID, connectionSlug string) (*store.WorkspaceIntegrationConnectorProjection, error) {
	projections := m.connectorProjections[workspaceID]
	for i, projection := range projections {
		if projection.Connection.Slug != connectionSlug && projection.Connection.ID != connectionSlug {
			continue
		}
		deleted := projection
		m.connectorProjections[workspaceID] = append(projections[:i], projections[i+1:]...)
		m.deletedConnection = &deleted
		return &deleted, nil
	}
	return nil, pgx.ErrNoRows
}

func (m *mockWorkspaceIntegrationManagementStore) GetWorkspaceIntegrationCredential(_ context.Context, integrationID string) (*store.WorkspaceIntegrationCredential, error) {
	if m.credentialErr != nil {
		return nil, m.credentialErr
	}
	if m.credential != nil && m.credential.IntegrationID == integrationID {
		return m.credential, nil
	}
	return nil, pgx.ErrNoRows
}

func (m *mockWorkspaceIntegrationManagementStore) SetWorkspaceIntegrationCredential(_ context.Context, credential *store.WorkspaceIntegrationCredential) error {
	if m.credentialSetErr != nil {
		return m.credentialSetErr
	}
	credential.ID = "credential-1"
	m.credential = credential
	return nil
}

func (m *mockWorkspaceIntegrationManagementStore) ListWorkspaceIntegrationConnectionsByLegacyIntegration(_ context.Context, integrationID string) ([]store.WorkspaceIntegrationConnection, error) {
	return append([]store.WorkspaceIntegrationConnection(nil), m.connectionCredentialsByLegacy[integrationID]...), nil
}

func (m *mockWorkspaceIntegrationManagementStore) SetWorkspaceIntegrationConnectionCredential(_ context.Context, credential *store.WorkspaceIntegrationConnectionCredential) error {
	if m.credentialSetErr != nil {
		return m.credentialSetErr
	}
	credential.ID = "connection-credential-1"
	m.connectionCredential = credential
	return nil
}

func (m *mockWorkspaceIntegrationManagementStore) ListWorkspaceIntegrationConnectorProjections(_ context.Context, workspaceID string) ([]store.WorkspaceIntegrationConnectorProjection, error) {
	return append([]store.WorkspaceIntegrationConnectorProjection(nil), m.connectorProjections[workspaceID]...), nil
}

func (m *mockWorkspaceIntegrationManagementStore) ReplaceWorkspaceIntegrationToolPolicies(_ context.Context, connectionID string, policies []store.WorkspaceIntegrationToolPolicy) error {
	m.replacedPolicyConnectionID = connectionID
	m.replacedPolicies = append([]store.WorkspaceIntegrationToolPolicy(nil), policies...)
	return nil
}

func (m *mockWorkspaceIntegrationManagementStore) ListWorkspaceIntegrationToolHealth(_ context.Context, accountID int, workspaceID string, query store.WorkspaceIntegrationHealthQuery) ([]store.WorkspaceIntegrationToolHealth, error) {
	m.healthAccountID = accountID
	m.healthWorkspaceID = workspaceID
	m.healthQuery = query
	return append([]store.WorkspaceIntegrationToolHealth(nil), m.healthRows...), nil
}

func (m *mockWorkspaceIntegrationManagementStore) ListWorkspaceIntegrationGuidanceOverlays(_ context.Context, accountID int, workspaceID, status string) ([]store.WorkspaceIntegrationGuidanceOverlay, error) {
	m.guidanceAccountID = accountID
	m.guidanceWorkspaceID = workspaceID
	m.guidanceStatus = status
	out := make([]store.WorkspaceIntegrationGuidanceOverlay, 0, len(m.guidanceRows))
	for _, row := range m.guidanceRows {
		if status == "" || row.Status == status {
			out = append(out, row)
		}
	}
	return out, nil
}

func (m *mockWorkspaceIntegrationManagementStore) CreateWorkspaceIntegrationGuidanceOverlay(_ context.Context, overlay *store.WorkspaceIntegrationGuidanceOverlay) error {
	next := *overlay
	if next.ID == "" {
		next.ID = "guidance-1"
	}
	if next.Version == 0 {
		next.Version = 1
	}
	if next.CreatedAt.IsZero() {
		next.CreatedAt = time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC)
		next.UpdatedAt = next.CreatedAt
	}
	m.createdGuidance = &next
	m.guidanceRows = append(m.guidanceRows, next)
	*overlay = next
	return nil
}

func (m *mockWorkspaceIntegrationManagementStore) CreateWorkspaceIntegrationGuidanceDraftsFromTelemetry(_ context.Context, accountID int, workspaceID string, since time.Time, limit int, createdBy *int) ([]store.WorkspaceIntegrationGuidanceOverlay, error) {
	m.guidanceAccountID = accountID
	m.guidanceWorkspaceID = workspaceID
	m.draftedGuidanceSince = since
	m.draftedGuidanceLimit = limit
	m.draftedGuidanceBy = createdBy
	draft := store.WorkspaceIntegrationGuidanceOverlay{
		ID:                 "guidance-draft-generated",
		AccountID:          accountID,
		WorkspaceID:        workspaceID,
		ToolID:             "github.list_issues",
		IntegrationSlug:    "github",
		ToolName:           "list_issues",
		Status:             "draft",
		Version:            4,
		Guidance:           "Repository arguments should use owner/name format; bare repository names have failed for this tool.",
		SourceFailureClass: stringPtr("invalid_arguments"),
		SanitizedPattern:   json.RawMessage(`{"repo_format_bare_name":true}`),
	}
	m.guidanceRows = append(m.guidanceRows, draft)
	return []store.WorkspaceIntegrationGuidanceOverlay{draft}, nil
}

func (m *mockWorkspaceIntegrationManagementStore) ApproveWorkspaceIntegrationGuidanceOverlay(_ context.Context, accountID int, workspaceID, overlayID string, approvedBy int) (*store.WorkspaceIntegrationGuidanceOverlay, error) {
	m.guidanceAccountID = accountID
	m.guidanceWorkspaceID = workspaceID
	m.approvedGuidanceID = overlayID
	m.approvedGuidanceBy = approvedBy
	for i := range m.guidanceRows {
		if m.guidanceRows[i].ID == overlayID {
			now := time.Date(2026, 6, 25, 9, 30, 0, 0, time.UTC)
			m.guidanceRows[i].Status = "approved"
			m.guidanceRows[i].ApprovedBy = &approvedBy
			m.guidanceRows[i].ApprovedAt = &now
			return &m.guidanceRows[i], nil
		}
	}
	return nil, pgx.ErrNoRows
}

func (m *mockWorkspaceIntegrationManagementStore) RevokeWorkspaceIntegrationTokensForWorkspace(_ context.Context, workspaceID string) error {
	m.revokedWorkspaceTokenWorkspaces = append(m.revokedWorkspaceTokenWorkspaces, workspaceID)
	return nil
}

func (m *mockWorkspaceIntegrationManagementStore) ListMachinesByAccount(_ context.Context, accountID int) ([]store.Machine, error) {
	var machines []store.Machine
	for _, machine := range m.machines {
		if machine.AccountID == accountID {
			machines = append(machines, *machine)
		}
	}
	return machines, nil
}

func (m *mockWorkspaceIntegrationManagementStore) GetMachine(_ context.Context, id string) (*store.Machine, error) {
	if machine, ok := m.machines[id]; ok {
		return machine, nil
	}
	return nil, fmt.Errorf("not found")
}

func (m *mockWorkspaceIntegrationManagementStore) ListMachinePlugins(_ context.Context, machineID string) ([]store.MachinePlugin, error) {
	return m.plugins[machineID], nil
}

func (m *mockWorkspaceIntegrationManagementStore) EnableMachinePlugin(_ context.Context, machineID, pluginID string, _ json.RawMessage) error {
	if pluginID != workspaceIntegrationPluginID {
		return fmt.Errorf("unexpected plugin %q", pluginID)
	}
	plugins := m.plugins[machineID]
	for i := range plugins {
		if plugins[i].PluginID == pluginID {
			plugins[i].Enabled = true
			plugins[i].InstallStatus = "installed"
			m.plugins[machineID] = plugins
			return nil
		}
	}
	m.plugins[machineID] = append(plugins, store.MachinePlugin{
		MachineID:     machineID,
		PluginID:      pluginID,
		Slot:          "workspace-integrations",
		Enabled:       true,
		InstallStatus: "installed",
	})
	return nil
}

func (m *mockWorkspaceIntegrationManagementStore) CreateActivity(_ context.Context, activity *store.ActivityLog) error {
	if m.activities != nil {
		m.activities <- *activity
	}
	return nil
}

func workspaceIntegrationManagementRequest(method, path string, accountID int, machineID string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	ctx := context.WithValue(req.Context(), accountIDKey, accountID)
	rctx := chi.NewRouteContext()
	if machineID != "" {
		rctx.URLParams.Add("machineID", machineID)
	}
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func workspaceRequest(method, path string, accountID int, workspaceID string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	ctx := context.WithValue(req.Context(), accountIDKey, accountID)
	rctx := chi.NewRouteContext()
	if workspaceID != "" {
		rctx.URLParams.Add("workspaceID", workspaceID)
	}
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func workspaceIntegrationManagementJSONRequest(method, path string, body interface{}, accountID int, workspaceID ...string) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), accountIDKey, accountID)
	if len(workspaceID) > 0 && workspaceID[0] != "" {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("workspaceID", workspaceID[0])
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}
	return req.WithContext(ctx)
}

func workspaceIntegrationManagementSlugRequest(method, path string, accountID int, workspaceID string, slug string, userID int) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	ctx := context.WithValue(req.Context(), accountIDKey, accountID)
	ctx = auth.WithUser(ctx, &auth.Claims{UserID: userID, Email: "owner@example.test"})
	rctx := chi.NewRouteContext()
	if workspaceID != "" {
		rctx.URLParams.Add("workspaceID", workspaceID)
	}
	if slug != "" {
		rctx.URLParams.Add("integrationSlug", slug)
	}
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func workspaceIntegrationPolicyRequest(method, path string, body interface{}, accountID int, workspaceID string, slug string, userID int) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), accountIDKey, accountID)
	ctx = auth.WithUser(ctx, &auth.Claims{UserID: userID, Email: "owner@example.test"})
	rctx := chi.NewRouteContext()
	if workspaceID != "" {
		rctx.URLParams.Add("workspaceID", workspaceID)
	}
	if slug != "" {
		rctx.URLParams.Add("integrationSlug", slug)
	}
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func waitForWorkspaceIntegrationActivity(t *testing.T, ms *mockWorkspaceIntegrationManagementStore) store.ActivityLog {
	t.Helper()
	select {
	case activity := <-ms.activities:
		return activity
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for workspace integration activity")
		return store.ActivityLog{}
	}
}

func TestListWorkspacesReturnsSummaries(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	workspaceID := "workspace-1"
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, WorkspaceID: &workspaceID, Name: "Builder", Status: "running"}
	ms.integrations = []store.WorkspaceIntegration{
		{ID: "integration-1", WorkspaceID: "workspace-1", Slug: "github", DisplayName: "GitHub", Kind: "github", Transport: "rest"},
	}
	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceRequest(http.MethodGet, "/api/accounts/1/workspaces", 1, "")

	srv.handleListWorkspaces(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp []workspaceSummaryResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp) != 1 || resp[0].MachineCount != 1 || resp[0].IntegrationCount != 1 {
		t.Fatalf("workspaces = %+v, want one summary with counts", resp)
	}
}

func TestCreateWorkspace(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementJSONRequest(http.MethodPost, "/api/accounts/1/workspaces", map[string]interface{}{"name": "Growth Team"}, 1)

	srv.handleCreateWorkspace(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if ms.created == nil || ms.created.Slug != "growth-team" || ms.created.AccountID != 1 {
		t.Fatalf("created workspace = %+v", ms.created)
	}
}

func TestListWorkspaceIntegrationsManagementRedactsSensitiveFields(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	endpoint := "https://secret.example.test/mcp"
	now := time.Date(2026, 6, 16, 12, 30, 0, 0, time.UTC)
	ms.integrations = []store.WorkspaceIntegration{
		{
			ID:           "integration-1",
			WorkspaceID:  "workspace-1",
			Slug:         "remote-mcp",
			DisplayName:  "Remote MCP",
			Kind:         "mcp",
			Transport:    "http",
			Endpoint:     &endpoint,
			Enabled:      true,
			ToolManifest: json.RawMessage(`[{"name":"search"},{"name":"lookup"}]`),
			Config:       json.RawMessage(`{"apiKey":"secret"}`),
			CreatedAt:    now,
			UpdatedAt:    now,
		},
	}
	workspaceID := "workspace-1"
	otherWorkspaceID := "workspace-2"
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, WorkspaceID: &workspaceID, Name: "Builder", Status: "running"}
	ms.machines["m-2"] = &store.Machine{ID: "m-2", AccountID: 1, WorkspaceID: &otherWorkspaceID, Name: "Other", Status: "running"}
	ms.machines["m-3"] = &store.Machine{ID: "m-3", AccountID: 1, Name: "Legacy", Status: "running"}
	ms.plugins["m-1"] = []store.MachinePlugin{
		{MachineID: "m-1", PluginID: workspaceIntegrationPluginID, Enabled: true, InstallStatus: "installed"},
	}

	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementRequest(http.MethodGet, "/api/accounts/1/workspace-integrations", 1, "")
	srv.handleListWorkspaceIntegrations(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret.example.test") || strings.Contains(w.Body.String(), "apiKey") {
		t.Fatalf("response leaked sensitive integration data: %s", w.Body.String())
	}

	var resp workspaceIntegrationManagementResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Workspace.ID != "workspace-1" {
		t.Fatalf("workspace ID = %q, want workspace-1", resp.Workspace.ID)
	}
	if len(resp.Integrations) != 1 || resp.Integrations[0].ToolCount != 2 {
		t.Fatalf("integrations = %+v, want one integration with two tools", resp.Integrations)
	}
	if len(resp.Machines) != 1 || !resp.Machines[0].PluginEnabled {
		t.Fatalf("machines = %+v, want enabled runtime consumer", resp.Machines)
	}
}

func TestListWorkspaceIntegrationsIncludesNormalizedOnlyConnectionsWithoutLegacyDuplicates(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	now := time.Date(2026, 6, 16, 12, 30, 0, 0, time.UTC)
	legacyID := "integration-1"
	ms.integrations = []store.WorkspaceIntegration{
		{
			ID:           legacyID,
			WorkspaceID:  "workspace-1",
			Slug:         "github",
			DisplayName:  "GitHub octocat/Hello-World",
			Kind:         "github",
			Transport:    "http",
			Enabled:      true,
			ToolManifest: githubToolManifest(),
			Config:       json.RawMessage(`{"owner":"octocat","repo":"Hello-World"}`),
			CreatedAt:    now,
			UpdatedAt:    now,
		},
	}
	ms.connectorProjections = map[string][]store.WorkspaceIntegrationConnectorProjection{
		"workspace-1": {
			{
				Source: store.WorkspaceIntegrationSource{
					ID:          "source-github",
					WorkspaceID: "workspace-1",
					Slug:        "github",
					DisplayName: "GitHub",
					Kind:        "github",
					Importer:    "mcp",
				},
				Connection: store.WorkspaceIntegrationConnection{
					ID:                  "connection-github",
					WorkspaceID:         "workspace-1",
					SourceID:            "source-github",
					LegacyIntegrationID: &legacyID,
					Slug:                "github",
					DisplayName:         "GitHub octocat/Hello-World",
					Scope:               "workspace",
					CredentialState:     "connected",
					Enabled:             true,
				},
				Tools: []store.WorkspaceIntegrationToolSnapshot{{
					ID:           "snapshot-github",
					WorkspaceID:  "workspace-1",
					ConnectionID: "connection-github",
					ToolName:     "get_repo",
					ToolAddress:  "wi.workspace-1.github.github.get_repo",
					Access:       "read",
					Source:       "github",
				}},
			},
			{
				Source: store.WorkspaceIntegrationSource{
					ID:          "source-openapi",
					WorkspaceID: "workspace-1",
					Slug:        "openapi",
					DisplayName: "OpenAPI",
					Kind:        "openapi",
					Importer:    "openapi",
				},
				Connection: store.WorkspaceIntegrationConnection{
					ID:              "connection-records",
					WorkspaceID:     "workspace-1",
					SourceID:        "source-openapi",
					Slug:            "records-prod",
					DisplayName:     "Records Prod",
					Scope:           "workspace",
					CredentialState: "connected",
					Enabled:         true,
					Config:          json.RawMessage(`{"transport":"http","endpoint":"https://secret.example.test"}`),
					CreatedAt:       now,
					UpdatedAt:       now,
				},
				Tools: []store.WorkspaceIntegrationToolSnapshot{
					{
						ID:           "snapshot-list",
						WorkspaceID:  "workspace-1",
						ConnectionID: "connection-records",
						ToolName:     "list_records",
						ToolAddress:  "wi.workspace-1.openapi.records-prod.list_records",
						Description:  "List records",
						Access:       "read",
						Source:       "openapi",
					},
					{
						ID:           "snapshot-create",
						WorkspaceID:  "workspace-1",
						ConnectionID: "connection-records",
						ToolName:     "create_record",
						ToolAddress:  "wi.workspace-1.openapi.records-prod.create_record",
						Description:  "Create records",
						Access:       "write",
						Source:       "openapi",
					},
				},
				Policies: []store.WorkspaceIntegrationToolPolicy{
					{WorkspaceID: "workspace-1", ConnectionID: "connection-records", ToolName: "list_records", Policy: workspaceIntegrationPolicyAllow, Source: "api"},
					{WorkspaceID: "workspace-1", ConnectionID: "connection-records", ToolName: "create_record", Policy: workspaceIntegrationPolicyBlock, Source: "api"},
				},
			},
		},
	}
	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementRequest(http.MethodGet, "/api/accounts/1/workspace-integrations", 1, "")

	srv.handleListWorkspaceIntegrations(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret.example.test") {
		t.Fatalf("response leaked normalized endpoint: %s", w.Body.String())
	}
	var resp workspaceIntegrationManagementResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Integrations) != 2 {
		t.Fatalf("integrations = %+v, want legacy github plus normalized records", resp.Integrations)
	}
	bySlug := map[string]workspaceIntegrationManagementItem{}
	for _, integration := range resp.Integrations {
		bySlug[integration.Slug] = integration
	}
	if _, ok := bySlug["github"]; !ok {
		t.Fatalf("legacy github integration missing from response: %+v", resp.Integrations)
	}
	records := bySlug["records-prod"]
	if records.ID != "connection-records" || records.Kind != "openapi" || records.ToolCount != 2 {
		t.Fatalf("normalized records item = %+v", records)
	}
	if got := strings.Join(records.AllowedTools, ","); got != "list_records" {
		t.Fatalf("records allowed tools = %q, want list_records", got)
	}
	if got := strings.Join(records.DeniedTools, ","); got != "create_record" {
		t.Fatalf("records denied tools = %q, want create_record", got)
	}
}

func TestListWorkspaceIntegrationsSuppressesNormalizedSlugCollisions(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	now := time.Date(2026, 6, 16, 12, 30, 0, 0, time.UTC)
	ms.integrations = []store.WorkspaceIntegration{
		{
			ID:           "integration-1",
			WorkspaceID:  "workspace-1",
			Slug:         "github",
			DisplayName:  "GitHub octocat/Hello-World",
			Kind:         "github",
			Transport:    "http",
			Enabled:      true,
			ToolManifest: githubToolManifest(),
			Config:       json.RawMessage(`{"owner":"octocat","repo":"Hello-World"}`),
			CreatedAt:    now,
			UpdatedAt:    now,
		},
	}
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
				ID:              "connection-shadow-github",
				WorkspaceID:     "workspace-1",
				SourceID:        "source-openapi",
				Slug:            "github",
				DisplayName:     "Shadow GitHub",
				Scope:           "workspace",
				CredentialState: "connected",
				Enabled:         true,
				CreatedAt:       now,
				UpdatedAt:       now,
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{{
				ID:           "snapshot-shadow",
				WorkspaceID:  "workspace-1",
				ConnectionID: "connection-shadow-github",
				ToolName:     "list_shadow_repos",
				ToolAddress:  "wi.workspace-1.openapi.github.list_shadow_repos",
				Access:       "read",
				Source:       "openapi",
			}},
		}},
	}
	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementRequest(http.MethodGet, "/api/accounts/1/workspace-integrations", 1, "")

	srv.handleListWorkspaceIntegrations(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp workspaceIntegrationManagementResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Integrations) != 1 {
		t.Fatalf("integrations = %+v, want only legacy github row", resp.Integrations)
	}
	if got := resp.Integrations[0].ID; got != "integration-1" {
		t.Fatalf("integration id = %q, want legacy integration-1", got)
	}
	if strings.Contains(w.Body.String(), "Shadow GitHub") || strings.Contains(w.Body.String(), "connection-shadow-github") {
		t.Fatalf("response included slug-colliding normalized projection: %s", w.Body.String())
	}
}

func TestListWorkspaceIntegrationsIncludesGoogleServiceStatus(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	checkedAt := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	config, err := json.Marshal(googleWorkspaceIntegrationConfig{
		Email: "user@example.com",
		Scopes: []string{
			"openid",
			"email",
			"profile",
			"https://www.googleapis.com/auth/gmail.readonly",
		},
		PermissionLevels: map[string]string{
			"gmail":    "read",
			"drive":    "off",
			"calendar": "off",
			"docs":     "off",
		},
		ServiceStatus: map[string]googleWorkspaceServiceStatus{
			"gmail": {
				Service:   "gmail",
				Status:    "connected",
				Detail:    "Gmail preflight succeeded.",
				CheckedAt: &checkedAt,
			},
			"drive": {
				Service: "drive",
				Status:  "missing_scope",
				Action:  "grant_scope_or_reconnect",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	now := time.Date(2026, 6, 28, 11, 0, 0, 0, time.UTC)
	ms.integrations = []store.WorkspaceIntegration{
		{
			ID:           "google-1",
			WorkspaceID:  "workspace-1",
			Slug:         googleWorkspaceIntegrationSlug,
			DisplayName:  "Google Workspace",
			Kind:         "google_workspace",
			Transport:    "http",
			Enabled:      true,
			ToolManifest: googleWorkspaceToolManifest(map[string]string{"gmail": "read", "drive": "off", "calendar": "off", "docs": "off"}),
			Config:       config,
			CreatedAt:    now,
			UpdatedAt:    now,
		},
	}
	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementRequest(http.MethodGet, "/api/accounts/1/workspace-integrations", 1, "")
	srv.handleListWorkspaceIntegrations(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp workspaceIntegrationManagementResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Integrations) != 1 {
		t.Fatalf("integrations = %+v", resp.Integrations)
	}
	google := resp.Integrations[0]
	if google.Target != "user@example.com" {
		t.Fatalf("target = %q", google.Target)
	}
	if google.ServiceStatus["gmail"].Status != "connected" || google.ServiceStatus["drive"].Action != "grant_scope_or_reconnect" {
		t.Fatalf("service status = %+v", google.ServiceStatus)
	}
}

func TestWorkspaceIntegrationHealthReturnsToolAggregates(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	since := time.Date(2026, 6, 25, 8, 30, 0, 0, time.UTC)
	toolAddress := "wi.workspace-1.google-workspace.google-workspace.gmail_list_messages"
	ms.healthRows = []store.WorkspaceIntegrationToolHealth{
		{
			ToolID:          "google-workspace.gmail_list_messages",
			ToolAddress:     &toolAddress,
			IntegrationSlug: "google-workspace",
			ToolName:        "gmail_list_messages",
			Transport:       "http",
			Access:          "read",
			TotalCalls:      10,
			SuccessCalls:    8,
			ErrorCalls:      2,
			SuccessRate:     0.8,
			P50LatencyMS:    120,
			P95LatencyMS:    450,
			AvgRetryCount:   0.2,
			TopFailureClasses: []store.WorkspaceIntegrationFailureCount{
				{Class: "rate_limited", Count: 2},
			},
		},
	}
	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceRequest(http.MethodGet, "/api/accounts/1/workspaces/workspace-1/integrations/health?since="+url.QueryEscape(since.Format(time.RFC3339))+"&limit=7", 1, "workspace-1")

	srv.handleWorkspaceIntegrationHealth(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ms.healthAccountID != 1 || ms.healthWorkspaceID != "workspace-1" {
		t.Fatalf("health query identity = account %d workspace %q", ms.healthAccountID, ms.healthWorkspaceID)
	}
	if !ms.healthQuery.Since.Equal(since) || ms.healthQuery.Limit != 7 {
		t.Fatalf("health query = %+v, want since %s limit 7", ms.healthQuery, since)
	}
	var resp workspaceIntegrationHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Workspace.ID != "workspace-1" || !resp.Since.Equal(since) {
		t.Fatalf("response workspace/since = %q %s", resp.Workspace.ID, resp.Since)
	}
	if len(resp.Tools) != 1 || resp.Tools[0].ToolID != "google-workspace.gmail_list_messages" || resp.Tools[0].SuccessRate != 0.8 {
		t.Fatalf("health tools = %+v", resp.Tools)
	}
}

func TestWorkspaceIntegrationGuidanceCreateListApprove(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	approvedAt := time.Date(2026, 6, 25, 8, 0, 0, 0, time.UTC)
	approvedBy := 7
	ms.guidanceRows = []store.WorkspaceIntegrationGuidanceOverlay{
		{
			ID:              "guidance-approved",
			AccountID:       1,
			WorkspaceID:     "workspace-1",
			ToolID:          "github.list_issues",
			IntegrationSlug: "github",
			ToolName:        "list_issues",
			Status:          "approved",
			Version:         2,
			Guidance:        "Use owner/repo format for repository fields.",
			ApprovedBy:      &approvedBy,
			ApprovedAt:      &approvedAt,
			CreatedAt:       approvedAt.Add(-time.Hour),
			UpdatedAt:       approvedAt,
		},
		{
			ID:              "guidance-draft",
			AccountID:       1,
			WorkspaceID:     "workspace-1",
			ToolID:          "github.list_issues",
			IntegrationSlug: "github",
			ToolName:        "list_issues",
			Status:          "draft",
			Version:         3,
			Guidance:        "Draft guidance.",
		},
	}
	srv := &Server{store: ms}

	listW := httptest.NewRecorder()
	listReq := workspaceRequest(http.MethodGet, "/api/accounts/1/workspaces/workspace-1/integrations/guidance?status=approved", 1, "workspace-1")
	srv.handleListWorkspaceIntegrationGuidance(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", listW.Code, listW.Body.String())
	}
	if ms.guidanceAccountID != 1 || ms.guidanceWorkspaceID != "workspace-1" || ms.guidanceStatus != "approved" {
		t.Fatalf("guidance list scope = account %d workspace %q status %q", ms.guidanceAccountID, ms.guidanceWorkspaceID, ms.guidanceStatus)
	}
	var listResp workspaceIntegrationGuidanceResponse
	if err := json.NewDecoder(listW.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listResp.Overlays) != 1 || listResp.Overlays[0].ID != "guidance-approved" {
		t.Fatalf("list overlays = %+v", listResp.Overlays)
	}

	createBody := map[string]interface{}{
		"tool_id":              "github.list_issues",
		"integration_slug":     "github",
		"tool_name":            "list_issues",
		"guidance":             "Repository input must include owner and name.",
		"source_failure_class": "invalid_arguments",
		"sanitized_pattern":    map[string]interface{}{"repo_format": "bare_name"},
	}
	createW := httptest.NewRecorder()
	createReq := workspaceIntegrationManagementJSONRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/guidance", createBody, 1, "workspace-1")
	createReq = createReq.WithContext(auth.WithUser(createReq.Context(), &auth.Claims{UserID: 7, Email: "owner@example.test"}))
	srv.handleCreateWorkspaceIntegrationGuidance(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("expected create 201, got %d: %s", createW.Code, createW.Body.String())
	}
	if ms.createdGuidance == nil || ms.createdGuidance.Status != "draft" || ms.createdGuidance.CreatedBy == nil || *ms.createdGuidance.CreatedBy != 7 {
		t.Fatalf("created guidance = %+v", ms.createdGuidance)
	}
	if strings.Contains(createW.Body.String(), "secret") {
		t.Fatalf("guidance response leaked unexpected secret-shaped text: %s", createW.Body.String())
	}

	draftSince := time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC)
	draftW := httptest.NewRecorder()
	draftReq := workspaceRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/guidance/draft?since="+url.QueryEscape(draftSince.Format(time.RFC3339))+"&limit=3", 1, "workspace-1")
	draftReq = draftReq.WithContext(auth.WithUser(draftReq.Context(), &auth.Claims{UserID: 8, Email: "operator@example.test"}))
	srv.handleDraftWorkspaceIntegrationGuidance(draftW, draftReq)
	if draftW.Code != http.StatusCreated {
		t.Fatalf("expected draft 201, got %d: %s", draftW.Code, draftW.Body.String())
	}
	if !ms.draftedGuidanceSince.Equal(draftSince) || ms.draftedGuidanceLimit != 3 || ms.draftedGuidanceBy == nil || *ms.draftedGuidanceBy != 8 {
		t.Fatalf("draft args = since %s limit %d by %v", ms.draftedGuidanceSince, ms.draftedGuidanceLimit, ms.draftedGuidanceBy)
	}
	var draftResp workspaceIntegrationGuidanceResponse
	if err := json.NewDecoder(draftW.Body).Decode(&draftResp); err != nil {
		t.Fatalf("decode draft response: %v", err)
	}
	if len(draftResp.Overlays) != 1 || draftResp.Overlays[0].Status != "draft" || !strings.Contains(draftResp.Overlays[0].Guidance, "owner/name") {
		t.Fatalf("draft overlays = %+v", draftResp.Overlays)
	}

	approveW := httptest.NewRecorder()
	approveReq := workspaceRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/guidance/guidance-draft/approve", 1, "workspace-1")
	rctx := chi.RouteContext(approveReq.Context())
	rctx.URLParams.Add("overlayID", "guidance-draft")
	approveReq = approveReq.WithContext(auth.WithUser(approveReq.Context(), &auth.Claims{UserID: 9, Email: "admin@example.test"}))
	srv.handleApproveWorkspaceIntegrationGuidance(approveW, approveReq)
	if approveW.Code != http.StatusOK {
		t.Fatalf("expected approve 200, got %d: %s", approveW.Code, approveW.Body.String())
	}
	if ms.approvedGuidanceID != "guidance-draft" || ms.approvedGuidanceBy != 9 {
		t.Fatalf("approved guidance id/by = %q %d", ms.approvedGuidanceID, ms.approvedGuidanceBy)
	}
}

func TestListWorkspaceIntegrationCatalogReturnsKnownApps(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementRequest(http.MethodGet, "/api/accounts/1/workspaces/workspace-1/integrations/catalog", 1, "")

	srv.handleListWorkspaceIntegrationCatalog(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "token") {
		t.Fatalf("catalog response leaked secret-shaped implementation detail: %s", w.Body.String())
	}
	var resp struct {
		Integrations []workspaceIntegrationCatalogItem `json:"integrations"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Integrations) < 2 {
		t.Fatalf("catalog integrations = %+v, want known apps", resp.Integrations)
	}
	bySlug := map[string]workspaceIntegrationCatalogItem{}
	for _, item := range resp.Integrations {
		bySlug[item.Slug] = item
	}
	google := bySlug[googleWorkspaceIntegrationSlug]
	if google.DisplayName != "Google Workspace" || google.AuthKind != "oauth" || google.Transport != "http" {
		t.Fatalf("google catalog item = %+v", google)
	}
	if len(google.Tools) == 0 {
		t.Fatalf("google catalog item missing tools: %+v", google)
	}
	for _, service := range []struct {
		slug          string
		displayName   string
		googleService string
		wantTool      string
	}{
		{slug: "gmail", displayName: "Gmail", googleService: "gmail", wantTool: "gmail_list_messages"},
		{slug: "google-drive", displayName: "Google Drive", googleService: "drive", wantTool: "drive_list_files"},
		{slug: "google-calendar", displayName: "Google Calendar", googleService: "calendar", wantTool: "calendar_list_events"},
	} {
		item := bySlug[service.slug]
		if item.DisplayName != service.displayName || item.AuthKind != "oauth" || item.Transport != "http" || item.ConnectionSlug != googleWorkspaceIntegrationSlug || item.GoogleService != service.googleService {
			t.Fatalf("%s catalog item = %+v", service.slug, item)
		}
		var found bool
		for _, tool := range item.Tools {
			if tool.Name == service.wantTool {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s catalog tools = %+v, want %s", service.slug, item.Tools, service.wantTool)
		}
	}
	github := bySlug["github"]
	if github.DisplayName != "GitHub" || github.AuthKind != "bearer" {
		t.Fatalf("github catalog item = %+v", github)
	}
}

func TestProbeWorkspaceIntegrationMCPRemoteDiscoversTools(t *testing.T) {
	var initialized bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload workspaceIntegrationMCPJSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		switch payload.Method {
		case "initialize":
			initialized = true
			w.Header().Set("Mcp-Session-Id", "session-1")
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      payload.ID,
				"result": map[string]interface{}{
					"protocolVersion": workspaceIntegrationMCPProtocolVersion,
					"serverInfo": map[string]interface{}{
						"name":    "Fixture MCP",
						"version": "1.2.3",
					},
					"capabilities": map[string]interface{}{
						"tools": map[string]interface{}{},
					},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if !initialized {
				t.Fatal("tools/list before initialize")
			}
			if r.Header.Get("Mcp-Session-Id") != "session-1" {
				t.Fatalf("session header = %q", r.Header.Get("Mcp-Session-Id"))
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      payload.ID,
				"result": map[string]interface{}{
					"tools": []map[string]interface{}{
						{
							"name":        "search",
							"description": "Search records",
							"inputSchema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"query": map[string]interface{}{"type": "string"},
								},
							},
						},
						{"name": "create_record", "description": "Create records"},
					},
				},
			})
		default:
			t.Fatalf("unexpected mcp method %q", payload.Method)
		}
	}))
	defer api.Close()

	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{store: ms, allowInsecureWorkspaceIntegrationEndpoints: true}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/probe", map[string]interface{}{
		"url": api.URL,
	}, 1, "workspace-1", "", 7)

	srv.handleProbeWorkspaceIntegration(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted != nil || ms.credential != nil {
		t.Fatalf("probe persisted integration=%+v credential=%+v", ms.upserted, ms.credential)
	}
	var resp workspaceIntegrationProbeResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Server.Name != "Fixture MCP" || resp.Server.Version != "1.2.3" {
		t.Fatalf("server info = %+v", resp.Server)
	}
	if resp.ProtocolVersion != workspaceIntegrationMCPProtocolVersion {
		t.Fatalf("protocol version = %q, want %q", resp.ProtocolVersion, workspaceIntegrationMCPProtocolVersion)
	}
	if len(resp.Tools) != 2 || resp.Tools[0].Name != "search" || resp.Tools[1].Name != "create_record" {
		t.Fatalf("tools = %+v", resp.Tools)
	}
	if resp.AuthRequired {
		t.Fatalf("auth required = true, want false")
	}
}

func TestProbeWorkspaceIntegrationMCPRemoteReportsAuthRequiredWithoutToken(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing auth", http.StatusUnauthorized)
	}))
	defer api.Close()

	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{store: ms, allowInsecureWorkspaceIntegrationEndpoints: true}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/probe", map[string]interface{}{
		"url": api.URL,
	}, 1, "workspace-1", "", 7)

	srv.handleProbeWorkspaceIntegration(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "bearer token or API key") {
		t.Fatalf("auth-required response should direct user to bearer/API-key fallback: %s", w.Body.String())
	}
	if ms.upserted != nil || ms.credential != nil {
		t.Fatalf("auth-required probe persisted integration=%+v credential=%+v", ms.upserted, ms.credential)
	}
}

func TestProbeWorkspaceIntegrationMCPRemoteDiscoversOAuthMetadata(t *testing.T) {
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mcp":
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+api.URL+`/metadata"`)
			http.Error(w, "missing auth", http.StatusUnauthorized)
		case "/metadata":
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"resource":              api.URL + "/mcp",
				"authorization_servers": []string{api.URL},
				"scopes_supported":      []string{"records.read", "records.write"},
			})
		case "/.well-known/oauth-authorization-server":
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"issuer":                                api.URL,
				"authorization_endpoint":                api.URL + "/authorize",
				"token_endpoint":                        api.URL + "/token",
				"registration_endpoint":                 api.URL + "/register",
				"scopes_supported":                      []string{"records.read", "records.write"},
				"code_challenge_methods_supported":      []string{"S256"},
				"response_types_supported":              []string{"code"},
				"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
				"token_endpoint_auth_methods_supported": []string{"none"},
			})
		default:
			t.Fatalf("unexpected oauth metadata path %q", r.URL.Path)
		}
	}))
	defer api.Close()

	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{store: ms, allowInsecureWorkspaceIntegrationEndpoints: true}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/probe", map[string]interface{}{
		"url": api.URL + "/mcp",
	}, 1, "workspace-1", "", 7)

	srv.handleProbeWorkspaceIntegration(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted != nil || ms.credential != nil {
		t.Fatalf("oauth metadata probe persisted integration=%+v credential=%+v", ms.upserted, ms.credential)
	}
	var resp workspaceIntegrationProbeResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.AuthRequired {
		t.Fatalf("auth required = false, want true")
	}
	if len(resp.Tools) != 0 {
		t.Fatalf("oauth metadata probe should not claim tools without auth: %+v", resp.Tools)
	}
	if resp.OAuth == nil || !resp.OAuth.Available {
		t.Fatalf("oauth metadata missing: %+v", resp.OAuth)
	}
	if resp.OAuth.ResourceMetadataURL != api.URL+"/metadata" {
		t.Fatalf("resource metadata url = %q", resp.OAuth.ResourceMetadataURL)
	}
	if resp.OAuth.AuthorizationServer != api.URL || resp.OAuth.Issuer != api.URL {
		t.Fatalf("oauth issuer/server = %+v", resp.OAuth)
	}
	if resp.OAuth.AuthorizationEndpoint != api.URL+"/authorize" || resp.OAuth.TokenEndpoint != api.URL+"/token" {
		t.Fatalf("oauth endpoints = %+v", resp.OAuth)
	}
	if resp.OAuth.RegistrationEndpoint != api.URL+"/register" {
		t.Fatalf("registration endpoint = %q", resp.OAuth.RegistrationEndpoint)
	}
	if !resp.OAuth.DynamicClientRegistration {
		t.Fatalf("expected dynamic client registration available")
	}
	if !containsString(resp.OAuth.ScopesSupported, "records.write") {
		t.Fatalf("scopes = %+v", resp.OAuth.ScopesSupported)
	}
	if !containsString(resp.OAuth.CodeChallengeMethodsSupported, "S256") {
		t.Fatalf("pkce methods = %+v", resp.OAuth.CodeChallengeMethodsSupported)
	}
}

func TestProbeWorkspaceIntegrationMCPRemoteUsesBearerTokenWithoutPersisting(t *testing.T) {
	const probeToken = "secret-probe-token"
	var initialized bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+probeToken {
			t.Fatalf("authorization header = %q", r.Header.Get("Authorization"))
		}
		var payload workspaceIntegrationMCPJSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		switch payload.Method {
		case "initialize":
			initialized = true
			w.Header().Set("Mcp-Session-Id", "session-auth")
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      payload.ID,
				"result": map[string]interface{}{
					"protocolVersion": workspaceIntegrationMCPProtocolVersion,
					"serverInfo":      map[string]interface{}{"name": "Private MCP"},
					"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
				},
			})
		case "notifications/initialized":
			if !initialized {
				t.Fatal("initialized notification before initialize")
			}
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "session-auth" {
				t.Fatalf("session header = %q", r.Header.Get("Mcp-Session-Id"))
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      payload.ID,
				"result":  map[string]interface{}{"tools": []map[string]interface{}{{"name": "search_private"}}},
			})
		default:
			t.Fatalf("unexpected mcp method %q", payload.Method)
		}
	}))
	defer api.Close()

	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{store: ms, allowInsecureWorkspaceIntegrationEndpoints: true}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/probe", map[string]interface{}{
		"url":   api.URL,
		"token": probeToken,
	}, 1, "workspace-1", "", 7)

	srv.handleProbeWorkspaceIntegration(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), probeToken) {
		t.Fatalf("probe response leaked token: %s", w.Body.String())
	}
	if ms.upserted != nil || ms.credential != nil {
		t.Fatalf("probe must not persist integration=%+v credential=%+v", ms.upserted, ms.credential)
	}
	var resp workspaceIntegrationProbeResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Tools) != 1 || resp.Tools[0].Name != "search_private" {
		t.Fatalf("tools = %+v", resp.Tools)
	}
}

func TestProbeWorkspaceIntegrationRejectsUnsafeURLs(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "invalid", url: "://not-a-url"},
		{name: "localhost", url: "http://localhost:8788/mcp"},
		{name: "private ip", url: "http://10.0.0.1/mcp"},
		{name: "link local metadata", url: "http://169.254.169.254/mcp"},
		{name: "non https production", url: "http://example.com/mcp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newMockWorkspaceIntegrationManagementStore()
			srv := &Server{store: ms}
			w := httptest.NewRecorder()
			r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/probe", map[string]interface{}{
				"url": tt.url,
			}, 1, "workspace-1", "", 7)

			srv.handleProbeWorkspaceIntegration(w, r)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
			if ms.upserted != nil || ms.credential != nil {
				t.Fatalf("unsafe probe persisted integration=%+v credential=%+v", ms.upserted, ms.credential)
			}
		})
	}
}

func TestProbeWorkspaceIntegrationDialerRejectsBlockedConcreteIP(t *testing.T) {
	conn, err := workspaceIntegrationProbeDialContext(context.Background(), "tcp", "169.254.169.254:443", false)
	if err == nil {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatal("expected blocked concrete IP dial to fail")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("dial error = %v, want not allowed", err)
	}
}

func TestProbeWorkspaceIntegrationRejectsRedirectToBlockedHost(t *testing.T) {
	client := workspaceIntegrationProbeHTTPClient(false)
	req := httptest.NewRequest(http.MethodGet, "https://169.254.169.254/mcp", nil)
	via := []*http.Request{httptest.NewRequest(http.MethodGet, "https://example.com/mcp", nil)}

	err := client.CheckRedirect(req, via)

	if err == nil {
		t.Fatal("expected blocked redirect target to fail")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("redirect error = %v, want not allowed", err)
	}
}

func TestProbeWorkspaceIntegrationHandlesDeterministicUnreachableHost(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve local port: %v", err)
	}
	endpoint := "http://" + listener.Addr().String() + "/mcp"
	if err := listener.Close(); err != nil {
		t.Fatalf("close reserved local port: %v", err)
	}

	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{store: ms, allowInsecureWorkspaceIntegrationEndpoints: true}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/probe", map[string]interface{}{
		"url": endpoint,
	}, 1, "workspace-1", "", 7)

	srv.handleProbeWorkspaceIntegration(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted != nil || ms.credential != nil {
		t.Fatalf("unreachable probe persisted integration=%+v credential=%+v", ms.upserted, ms.credential)
	}
}

func TestProbeWorkspaceIntegrationRejectsNonMCPResponse(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	}))
	defer api.Close()

	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{store: ms, allowInsecureWorkspaceIntegrationEndpoints: true}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/probe", map[string]interface{}{
		"url": api.URL,
	}, 1, "workspace-1", "", 7)

	srv.handleProbeWorkspaceIntegration(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted != nil || ms.credential != nil {
		t.Fatalf("non-MCP probe persisted integration=%+v credential=%+v", ms.upserted, ms.credential)
	}
}

func TestProbeWorkspaceIntegrationRejectsTooManyTools(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload workspaceIntegrationMCPJSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		switch payload.Method {
		case "initialize":
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      payload.ID,
				"result": map[string]interface{}{
					"protocolVersion": workspaceIntegrationMCPProtocolVersion,
					"serverInfo":      map[string]interface{}{"name": "Huge MCP"},
					"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			tools := make([]map[string]interface{}, workspaceIntegrationMaxDiscoveredTools+1)
			for i := range tools {
				tools[i] = map[string]interface{}{"name": fmt.Sprintf("tool_%03d", i)}
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      payload.ID,
				"result":  map[string]interface{}{"tools": tools},
			})
		default:
			t.Fatalf("unexpected mcp method %q", payload.Method)
		}
	}))
	defer api.Close()

	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{store: ms, allowInsecureWorkspaceIntegrationEndpoints: true}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/probe", map[string]interface{}{
		"url": api.URL,
	}, 1, "workspace-1", "", 7)

	srv.handleProbeWorkspaceIntegration(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted != nil || ms.credential != nil {
		t.Fatalf("oversized probe must not persist integration=%+v credential=%+v", ms.upserted, ms.credential)
	}
}

func TestCreateMockWorkspaceIntegrationUpsertsDefaultWorkspaceIntegration(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementRequest(http.MethodPost, "/api/accounts/1/workspace-integrations/mock", 1, "")

	srv.handleCreateMockWorkspaceIntegration(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted == nil {
		t.Fatal("expected upserted workspace integration")
	}
	if ms.upserted.WorkspaceID != "workspace-1" || ms.upserted.Slug != "mock-echo" || ms.upserted.Kind != "mock" {
		t.Fatalf("upserted = %+v", ms.upserted)
	}
	if strings.Contains(w.Body.String(), `"config"`) {
		t.Fatalf("response leaked config: %s", w.Body.String())
	}

	var item workspaceIntegrationManagementItem
	if err := json.NewDecoder(w.Body).Decode(&item); err != nil {
		t.Fatalf("decode item: %v", err)
	}
	if item.ToolCount != 1 || !item.Enabled {
		t.Fatalf("item = %+v, want enabled mock with one tool", item)
	}
	// Regression: adding an integration is additive and must NOT revoke workspace
	// machine tokens (that dropped all tools on running machines until restart).
	if len(ms.revokedWorkspaceTokenWorkspaces) != 0 {
		t.Fatalf("create must not revoke workspace tokens, got %v", ms.revokedWorkspaceTokenWorkspaces)
	}
}

func TestCreateWorkspaceIntegrationAssignsRuntimeToWorkspaceMachines(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	workspaceID := "workspace-1"
	otherWorkspaceID := "workspace-2"
	now := time.Date(2026, 6, 16, 12, 30, 0, 0, time.UTC)
	ms.machines["m-running"] = &store.Machine{ID: "m-running", AccountID: 1, WorkspaceID: &workspaceID, Kind: store.MachineKindOpenClaw, Name: "Running", Status: "running"}
	ms.machines["m-stopped"] = &store.Machine{ID: "m-stopped", AccountID: 1, WorkspaceID: &workspaceID, Kind: store.MachineKindOpenClaw, Name: "Stopped", Status: "stopped", ProvisioningCompletedAt: &now}
	ms.machines["m-hermes"] = &store.Machine{ID: "m-hermes", AccountID: 1, WorkspaceID: &workspaceID, Kind: store.MachineKindHermes, Name: "Hermes", Status: "running"}
	ms.machines["m-other"] = &store.Machine{ID: "m-other", AccountID: 1, WorkspaceID: &otherWorkspaceID, Kind: store.MachineKindOpenClaw, Name: "Other", Status: "running"}
	ms.machines["m-legacy"] = &store.Machine{ID: "m-legacy", AccountID: 1, Kind: store.MachineKindOpenClaw, Name: "Legacy", Status: "running"}
	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementRequest(http.MethodPost, "/api/accounts/1/workspace-integrations/mock", 1, "")

	srv.handleCreateMockWorkspaceIntegration(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	for _, machineID := range []string{"m-running", "m-stopped"} {
		plugins := ms.plugins[machineID]
		if len(plugins) != 1 || plugins[0].PluginID != workspaceIntegrationPluginID || !plugins[0].Enabled {
			t.Fatalf("plugins[%s] = %+v, want enabled workspace runtime", machineID, plugins)
		}
	}
	for _, machineID := range []string{"m-hermes", "m-other", "m-legacy"} {
		if len(ms.plugins[machineID]) != 0 {
			t.Fatalf("plugins[%s] = %+v, want no workspace runtime assignment", machineID, ms.plugins[machineID])
		}
	}
}

func TestCreateGitHubWorkspaceIntegrationStoresEncryptedToken(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	ms.connectionCredentialsByLegacy = map[string][]store.WorkspaceIntegrationConnection{
		"integration-1": {{
			ID:          "connection-github-octocat-hello-world",
			WorkspaceID: "workspace-1",
			Slug:        "github-octocat-hello-world",
		}},
	}
	secretKey := "12345678901234567890123456789012"
	srv := &Server{store: ms, secretKey: secretKey, allowInsecureWorkspaceIntegrationEndpoints: true}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementJSONRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/github", map[string]interface{}{
		"owner":        "octocat",
		"repo":         "Hello-World",
		"display_name": "GitHub Repo",
		"token":        "ghp_secret",
	}, 1, "workspace-1")
	r = r.WithContext(auth.WithUser(r.Context(), &auth.Claims{UserID: 7, Email: "owner@example.test"}))

	srv.handleCreateGitHubWorkspaceIntegration(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted == nil {
		t.Fatal("expected github integration upsert")
	}
	if ms.upserted.WorkspaceID != "workspace-1" || ms.upserted.Slug != "github-octocat-hello-world" || ms.upserted.Kind != "github" || ms.upserted.Transport != "http" {
		t.Fatalf("upserted = %+v", ms.upserted)
	}
	if ms.upserted.ApprovedBy == nil || *ms.upserted.ApprovedBy != 7 || ms.upserted.ConnectedBy == nil || *ms.upserted.ConnectedBy != 7 {
		t.Fatalf("governance actor metadata = approved_by:%v connected_by:%v", ms.upserted.ApprovedBy, ms.upserted.ConnectedBy)
	}
	if ms.upserted.ApprovedAt == nil || ms.upserted.ConnectedAt == nil {
		t.Fatalf("expected approval and connection timestamps: %+v", ms.upserted)
	}
	if strings.Contains(w.Body.String(), "ghp_secret") || strings.Contains(w.Body.String(), `"config"`) {
		t.Fatalf("response leaked secret/config: %s", w.Body.String())
	}
	if ms.credential == nil {
		t.Fatal("expected stored workspace integration credential")
	}
	if ms.credential.SecretEnc == "ghp_secret" {
		t.Fatal("credential was not encrypted")
	}
	token, err := crypto.Decrypt(ms.credential.SecretEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt stored credential: %v", err)
	}
	if token != "ghp_secret" {
		t.Fatalf("stored token = %q, want ghp_secret", token)
	}
	if ms.connectionCredential == nil {
		t.Fatal("expected stored normalized connection credential")
	}
	if ms.connectionCredential.ConnectionID != "connection-github-octocat-hello-world" {
		t.Fatalf("connection credential connection_id = %q", ms.connectionCredential.ConnectionID)
	}
	connectionToken, err := crypto.Decrypt(ms.connectionCredential.SecretEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt connection credential: %v", err)
	}
	if connectionToken != "ghp_secret" {
		t.Fatalf("connection credential token = %q, want ghp_secret", connectionToken)
	}

	var item workspaceIntegrationManagementItem
	if err := json.NewDecoder(w.Body).Decode(&item); err != nil {
		t.Fatalf("decode item: %v", err)
	}
	if item.Target != "octocat/Hello-World" || item.ToolCount != 2 {
		t.Fatalf("item = %+v, want github target with two tools", item)
	}
	if !item.Approved || item.ApprovedBy == nil || *item.ApprovedBy != 7 || item.ConnectedBy == nil || *item.ConnectedBy != 7 {
		t.Fatalf("item governance metadata = %+v", item)
	}
	// Regression: additive create must NOT revoke workspace machine tokens.
	if len(ms.revokedWorkspaceTokenWorkspaces) != 0 {
		t.Fatalf("create must not revoke workspace tokens, got %v", ms.revokedWorkspaceTokenWorkspaces)
	}
}

func TestCreateGitHubWorkspaceIntegrationRejectsNormalizedConnectionSlugCollision(t *testing.T) {
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
				ID:              "connection-github-shadow",
				WorkspaceID:     "workspace-1",
				SourceID:        "source-openapi",
				Slug:            "github-octocat-hello-world",
				DisplayName:     "Shadow GitHub",
				Scope:           "workspace",
				CredentialState: "connected",
				Enabled:         true,
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{{
				ID:           "snapshot-list",
				WorkspaceID:  "workspace-1",
				ConnectionID: "connection-github-shadow",
				ToolName:     "list_records",
				ToolAddress:  "wi.workspace-1.openapi.github-octocat-hello-world.list_records",
				Access:       "read",
				Source:       "openapi",
			}},
		}},
	}
	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementJSONRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/github", map[string]interface{}{
		"owner": "octocat",
		"repo":  "Hello-World",
	}, 1, "workspace-1")
	r = r.WithContext(auth.WithUser(r.Context(), &auth.Claims{UserID: 7, Email: "owner@example.test"}))

	srv.handleCreateGitHubWorkspaceIntegration(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted != nil {
		t.Fatalf("slug collision should not upsert integration: %+v", ms.upserted)
	}
	if !strings.Contains(w.Body.String(), "connection slug already exists") {
		t.Fatalf("response = %s", w.Body.String())
	}
}

func TestWorkspaceIntegrationManagementItemTargetUsesConfigShape(t *testing.T) {
	tests := []struct {
		name   string
		config json.RawMessage
		want   string
	}{
		{
			name:   "explicit display target",
			config: json.RawMessage(`{"display_target":"project-alpha"}`),
			want:   "project-alpha",
		},
		{
			name:   "email target",
			config: json.RawMessage(`{"email":" user@example.test "}`),
			want:   "user@example.test",
		},
		{
			name:   "owner repo target",
			config: json.RawMessage(`{"owner":"octocat","repo":"Hello-World"}`),
			want:   "octocat/Hello-World",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := workspaceIntegrationManagementItemFromStore(store.WorkspaceIntegration{
				ID:           "integration-1",
				WorkspaceID:  "workspace-1",
				Slug:         "example",
				DisplayName:  "Example",
				Kind:         "example",
				Transport:    "http",
				ToolManifest: json.RawMessage(`[{"name":"read"}]`),
				Config:       tt.config,
			})

			if item.Target != tt.want {
				t.Fatalf("target = %q, want %q", item.Target, tt.want)
			}
		})
	}
}

func TestWorkspaceIntegrationManagementProjectionTargetUsesConfigShape(t *testing.T) {
	tests := []struct {
		name   string
		config json.RawMessage
		want   string
	}{
		{
			name:   "explicit display target",
			config: json.RawMessage(`{"display_target":"project-alpha","endpoint":"https://secret.example.test"}`),
			want:   "project-alpha",
		},
		{
			name:   "email target",
			config: json.RawMessage(`{"email":" user@example.test ","endpoint":"https://secret.example.test"}`),
			want:   "user@example.test",
		},
		{
			name:   "owner repo target",
			config: json.RawMessage(`{"owner":"octocat","repo":"Hello-World","endpoint":"https://secret.example.test"}`),
			want:   "octocat/Hello-World",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := workspaceIntegrationManagementItemFromProjection(store.WorkspaceIntegrationConnectorProjection{
				Source: store.WorkspaceIntegrationSource{
					ID:          "source-example",
					WorkspaceID: "workspace-1",
					Slug:        "example",
					DisplayName: "Example",
					Kind:        "example",
					Importer:    "openapi",
				},
				Connection: store.WorkspaceIntegrationConnection{
					ID:              "connection-example",
					WorkspaceID:     "workspace-1",
					SourceID:        "source-example",
					Slug:            "example-prod",
					DisplayName:     "Example Prod",
					Scope:           "workspace",
					CredentialState: "connected",
					Enabled:         true,
					Config:          tt.config,
				},
				Tools: []store.WorkspaceIntegrationToolSnapshot{{
					ID:           "snapshot-read",
					WorkspaceID:  "workspace-1",
					ConnectionID: "connection-example",
					ToolName:     "read",
					ToolAddress:  "wi.workspace-1.example.example-prod.read",
					Access:       "read",
					Source:       "openapi",
				}},
			}, nil, nil)

			if item.Target != tt.want {
				t.Fatalf("target = %q, want %q", item.Target, tt.want)
			}
			encoded, err := json.Marshal(item)
			if err != nil {
				t.Fatalf("marshal item: %v", err)
			}
			if strings.Contains(string(encoded), "secret.example.test") {
				t.Fatalf("normalized projection item leaked raw endpoint: %s", encoded)
			}
		})
	}
}

func TestWorkspaceIntegrationManagementItemExposesCustomMCPSnapshotMetadata(t *testing.T) {
	item := workspaceIntegrationManagementItemFromStore(store.WorkspaceIntegration{
		ID:           "integration-1",
		WorkspaceID:  "workspace-1",
		Slug:         "team-mcp",
		DisplayName:  "Team MCP",
		Kind:         "custom-mcp",
		Transport:    "mcp-remote",
		Enabled:      true,
		ToolManifest: json.RawMessage(`[{"name":"search","mcp_remote":{"name":"search"}}]`),
		Config: json.RawMessage(`{
			"display_target":"https://mcp.example.com",
			"server_name":"Example MCP",
			"server_version":"1.2.3",
			"protocol_version":"2025-03-26",
			"probed_at":"2026-06-20T12:00:00Z"
		}`),
	})

	if item.Snapshot == nil {
		t.Fatal("expected snapshot metadata")
	}
	if item.Snapshot.ServerName != "Example MCP" || item.Snapshot.ServerVersion != "1.2.3" {
		t.Fatalf("snapshot server = %+v", item.Snapshot)
	}
	if item.Snapshot.ProtocolVersion != workspaceIntegrationMCPProtocolVersion {
		t.Fatalf("snapshot protocol = %q", item.Snapshot.ProtocolVersion)
	}
	if item.Snapshot.ProbedAt == nil || !item.Snapshot.ProbedAt.Equal(time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("snapshot probed_at = %v", item.Snapshot.ProbedAt)
	}
}

func TestWorkspaceIntegrationManagementProjectionExposesSnapshotMetadata(t *testing.T) {
	item := workspaceIntegrationManagementItemFromProjection(store.WorkspaceIntegrationConnectorProjection{
		Source: store.WorkspaceIntegrationSource{
			ID:          "source-custom-mcp",
			WorkspaceID: "workspace-1",
			Slug:        "custom-mcp",
			DisplayName: "Custom MCP",
			Kind:        "custom-mcp",
			Importer:    "mcp",
		},
		Connection: store.WorkspaceIntegrationConnection{
			ID:              "connection-team-mcp",
			WorkspaceID:     "workspace-1",
			SourceID:        "source-custom-mcp",
			Slug:            "team-mcp",
			DisplayName:     "Team MCP",
			Scope:           "workspace",
			CredentialState: "connected",
			Enabled:         true,
			Config: json.RawMessage(`{
				"display_target":"Team MCP",
				"endpoint":"https://secret-mcp.example.test",
				"server_name":"Example MCP",
				"server_version":"1.2.3",
				"protocol_version":"2025-03-26",
				"probed_at":"2026-06-20T12:00:00Z"
			}`),
		},
		Tools: []store.WorkspaceIntegrationToolSnapshot{{
			ID:           "snapshot-search",
			WorkspaceID:  "workspace-1",
			ConnectionID: "connection-team-mcp",
			ToolName:     "search",
			ToolAddress:  "wi.workspace-1.custom-mcp.team-mcp.search",
			Description:  "Search records",
			Access:       "read",
			Source:       "mcp",
		}},
	}, nil, nil)

	if item.Target != "Team MCP" {
		t.Fatalf("target = %q, want display target", item.Target)
	}
	if item.Snapshot == nil {
		t.Fatal("expected normalized projection snapshot metadata")
	}
	if item.Snapshot.ServerName != "Example MCP" || item.Snapshot.ServerVersion != "1.2.3" {
		t.Fatalf("snapshot server = %+v", item.Snapshot)
	}
	if item.Snapshot.ProtocolVersion != workspaceIntegrationMCPProtocolVersion {
		t.Fatalf("snapshot protocol = %q", item.Snapshot.ProtocolVersion)
	}
	if item.Snapshot.ProbedAt == nil || !item.Snapshot.ProbedAt.Equal(time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("snapshot probed_at = %v", item.Snapshot.ProbedAt)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal item: %v", err)
	}
	if strings.Contains(string(encoded), "secret-mcp.example.test") {
		t.Fatalf("normalized projection item leaked raw endpoint: %s", encoded)
	}
}

func TestCreateGitHubWorkspaceIntegrationClearsCredentialWhenTargetChangesWithoutToken(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	ms.connectionCredentialsByLegacy = map[string][]store.WorkspaceIntegrationConnection{
		"integration-1": {{
			ID:          "connection-github",
			WorkspaceID: "workspace-1",
			Slug:        "github",
		}},
	}
	now := time.Date(2026, 6, 16, 12, 30, 0, 0, time.UTC)
	ms.integrations = []store.WorkspaceIntegration{
		{
			ID:           "integration-1",
			WorkspaceID:  "workspace-1",
			Slug:         "github",
			DisplayName:  "GitHub octocat/Hello-World",
			Kind:         "github",
			Transport:    "http",
			Enabled:      true,
			ToolManifest: githubToolManifest(),
			Config:       json.RawMessage(`{"owner":"octocat","repo":"Hello-World"}`),
			CreatedAt:    now,
			UpdatedAt:    now,
		},
	}
	ms.credential = &store.WorkspaceIntegrationCredential{
		IntegrationID: "integration-1",
		SecretEnc:     "encrypted-old-pat",
	}
	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementJSONRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/github", map[string]interface{}{
		"owner":           "new-owner",
		"repo":            "New-Repo",
		"connection_slug": "github",
		"token":           "",
	}, 1, "workspace-1")
	r = r.WithContext(auth.WithUser(r.Context(), &auth.Claims{UserID: 7, Email: "owner@example.test"}))

	srv.handleCreateGitHubWorkspaceIntegration(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted == nil || githubWorkspaceIntegrationTarget(*ms.upserted) != "new-owner/New-Repo" {
		t.Fatalf("upserted = %+v, want new github target", ms.upserted)
	}
	if ms.upserted.Slug != "github" {
		t.Fatalf("upserted slug = %q, want legacy github slug", ms.upserted.Slug)
	}
	if ms.credential == nil {
		t.Fatal("expected credential row to be cleared")
	}
	if ms.credential.SecretEnc != "" {
		t.Fatalf("credential secret = %q, want cleared", ms.credential.SecretEnc)
	}
	if ms.connectionCredential == nil {
		t.Fatal("expected normalized connection credential row to be cleared")
	}
	if ms.connectionCredential.ConnectionID != "connection-github" || ms.connectionCredential.SecretEnc != "" {
		t.Fatalf("connection credential = %+v, want cleared connection-github", ms.connectionCredential)
	}
}

func TestCreateGitHubWorkspaceIntegrationAddsMultipleRepoConnections(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	secretKey := "12345678901234567890123456789012"
	srv := &Server{store: ms, secretKey: secretKey, allowInsecureWorkspaceIntegrationEndpoints: true}

	first := workspaceIntegrationManagementJSONRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/github", map[string]interface{}{
		"owner": "octocat",
		"repo":  "Hello-World",
		"token": "first-token",
	}, 1, "workspace-1")
	first = first.WithContext(auth.WithUser(first.Context(), &auth.Claims{UserID: 7, Email: "owner@example.test"}))
	firstW := httptest.NewRecorder()
	srv.handleCreateGitHubWorkspaceIntegration(firstW, first)
	if firstW.Code != http.StatusOK {
		t.Fatalf("first create expected 200, got %d: %s", firstW.Code, firstW.Body.String())
	}
	if ms.upserted == nil || ms.upserted.Slug != "github-octocat-hello-world" {
		t.Fatalf("first upserted = %+v", ms.upserted)
	}
	if ms.credential == nil || ms.credential.SecretEnc == "" {
		t.Fatalf("expected first credential, got %+v", ms.credential)
	}
	firstCredential := *ms.credential

	second := workspaceIntegrationManagementJSONRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/github", map[string]interface{}{
		"owner": "mathaix",
		"repo":  "ocm_cloud",
		"token": "",
	}, 1, "workspace-1")
	second = second.WithContext(auth.WithUser(second.Context(), &auth.Claims{UserID: 7, Email: "owner@example.test"}))
	secondW := httptest.NewRecorder()
	srv.handleCreateGitHubWorkspaceIntegration(secondW, second)
	if secondW.Code != http.StatusOK {
		t.Fatalf("second create expected 200, got %d: %s", secondW.Code, secondW.Body.String())
	}
	if ms.upserted == nil || ms.upserted.Slug != "github-mathaix-ocm-cloud" {
		t.Fatalf("second upserted = %+v", ms.upserted)
	}
	if len(ms.integrations) != 2 {
		t.Fatalf("integrations = %+v, want two github connections", ms.integrations)
	}
	slugs := map[string]string{}
	for _, integration := range ms.integrations {
		slugs[integration.Slug] = githubWorkspaceIntegrationTarget(integration)
	}
	if slugs["github-octocat-hello-world"] != "octocat/Hello-World" || slugs["github-mathaix-ocm-cloud"] != "mathaix/ocm_cloud" {
		t.Fatalf("github connections = %+v", slugs)
	}
	if ms.credential == nil || ms.credential.SecretEnc != firstCredential.SecretEnc {
		t.Fatalf("second connection without token should not clear first credential: before=%+v after=%+v", firstCredential, ms.credential)
	}
}

func TestCreateGitHubWorkspaceIntegrationRejectsInvalidConnectionSlug(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementJSONRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/github", map[string]interface{}{
		"owner":           "octocat",
		"repo":            "Hello-World",
		"connection_slug": "GitHub Bad Slug",
	}, 1, "workspace-1")

	srv.handleCreateGitHubWorkspaceIntegration(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted != nil {
		t.Fatalf("invalid connection slug should not upsert: %+v", ms.upserted)
	}
}

func TestCreateGenericWorkspaceIntegrationStoresDataAndEncryptedToken(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	workspaceID := "workspace-1"
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, WorkspaceID: &workspaceID}
	secretKey := "12345678901234567890123456789012"
	srv := &Server{store: ms, secretKey: secretKey, activity: events.New(ms, nil)}

	body := map[string]interface{}{
		"display_name": "Linear",
		"kind":         "linear",
		"transport":    "http",
		"endpoint":     "https://api.linear.example",
		"config":       map[string]interface{}{"team_id": "team-123"},
		"token":        "linear-token",
		"token_type":   "bearer",
		"tool_manifest": []map[string]interface{}{
			{
				"name":        "list_issues",
				"description": "List issues for a Linear team",
				"request": map[string]interface{}{
					"method": "GET",
					"path":   "/issues",
					"query": map[string]interface{}{
						"teamId": map[string]interface{}{"source": "config", "name": "team_id"},
					},
					"auth": map[string]interface{}{"type": "bearer", "required": true},
				},
			},
		},
	}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/linear", body, 1, "workspace-1", "linear", 7)

	srv.handleCreateWorkspaceIntegration(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted == nil {
		t.Fatal("expected generic integration to be upserted")
	}
	if ms.upserted.Slug != "linear" || ms.upserted.DisplayName != "Linear" || ms.upserted.Transport != "http" {
		t.Fatalf("upserted integration = %+v", ms.upserted)
	}
	if ms.upserted.Endpoint == nil || *ms.upserted.Endpoint != "https://api.linear.example" {
		t.Fatalf("endpoint = %v", ms.upserted.Endpoint)
	}
	if got := string(ms.upserted.Config); got != `{"team_id":"team-123"}` {
		t.Fatalf("config = %s", got)
	}
	if ms.credential == nil || ms.credential.SecretEnc == "" || ms.credential.SecretEnc == "linear-token" {
		t.Fatalf("credential was not encrypted: %+v", ms.credential)
	}
	decrypted, err := crypto.Decrypt(ms.credential.SecretEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt credential: %v", err)
	}
	if decrypted != "linear-token" {
		t.Fatalf("stored token = %q, want linear-token", decrypted)
	}
	// Regression: additive create must NOT revoke workspace machine tokens.
	if len(ms.revokedWorkspaceTokenWorkspaces) != 0 {
		t.Fatalf("create must not revoke workspace tokens, got %v", ms.revokedWorkspaceTokenWorkspaces)
	}
	if plugins := ms.plugins["m-1"]; len(plugins) != 1 || plugins[0].PluginID != workspaceIntegrationPluginID || !plugins[0].Enabled {
		t.Fatalf("plugins = %+v, want enabled workspace integration runtime", plugins)
	}
	activity := waitForWorkspaceIntegrationActivity(t, ms)
	if activity.Action != "config.workspace_integration_created" || activity.ActorID == nil || *activity.ActorID != 7 {
		t.Fatalf("activity = %+v", activity)
	}
}

func TestCreateGenericWorkspaceIntegrationRejectsNormalizedConnectionSlugCollision(t *testing.T) {
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
				ID:              "connection-linear",
				WorkspaceID:     "workspace-1",
				SourceID:        "source-openapi",
				Slug:            "linear",
				DisplayName:     "Linear",
				Scope:           "workspace",
				CredentialState: "connected",
				Enabled:         true,
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{{
				ID:           "snapshot-list",
				WorkspaceID:  "workspace-1",
				ConnectionID: "connection-linear",
				ToolName:     "list_issues",
				ToolAddress:  "wi.workspace-1.openapi.linear.list_issues",
				Access:       "read",
				Source:       "openapi",
			}},
		}},
	}
	srv := &Server{store: ms}
	body := map[string]interface{}{
		"display_name": "Linear",
		"kind":         "linear",
		"transport":    "http",
		"endpoint":     "https://api.linear.example",
		"tool_manifest": []map[string]interface{}{
			{
				"name":        "list_issues",
				"description": "List issues",
				"request": map[string]interface{}{
					"method": "GET",
					"path":   "/issues",
				},
			},
		},
	}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/linear", body, 1, "workspace-1", "linear", 7)

	srv.handleCreateWorkspaceIntegration(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted != nil {
		t.Fatalf("slug collision should not upsert integration: %+v", ms.upserted)
	}
	if !strings.Contains(w.Body.String(), "connection slug already exists") {
		t.Fatalf("response = %s", w.Body.String())
	}
}

func TestCreateGenericWorkspaceIntegrationRejectsSecretConfig(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{store: ms}

	body := map[string]interface{}{
		"display_name": "Linear",
		"transport":    "http",
		"endpoint":     "https://api.linear.example",
		"config":       map[string]interface{}{"api_key": "must-not-store"},
		"tool_manifest": []map[string]interface{}{
			{
				"name": "list_issues",
				"request": map[string]interface{}{
					"path": "/issues",
				},
			},
		},
	}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/linear", body, 1, "workspace-1", "linear", 7)

	srv.handleCreateWorkspaceIntegration(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "must-not-store") {
		t.Fatalf("response leaked rejected secret: %s", w.Body.String())
	}
	if ms.upserted != nil || ms.credential != nil {
		t.Fatalf("secret config should not persist integration or credential: integration=%+v credential=%+v", ms.upserted, ms.credential)
	}
}

func TestCreateGenericMCPWorkspaceIntegrationRejectsKnownAppSlugCollision(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{store: ms}
	body := map[string]interface{}{
		"display_name": "Custom GitHub MCP",
		"transport":    "mcp-remote",
		"endpoint":     "https://mcp.example.test",
		"tool_manifest": []map[string]interface{}{
			{"name": "search"},
		},
	}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/github", body, 1, "workspace-1", "github", 7)

	srv.handleCreateWorkspaceIntegration(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted != nil || ms.credential != nil {
		t.Fatalf("slug collision should not persist integration=%+v credential=%+v", ms.upserted, ms.credential)
	}
}

func TestCreateGenericMCPWorkspaceIntegrationAllowsCuratedCatalogEndpoint(t *testing.T) {
	githubURL := workspaceIntegrationCatalogRemoteURL("github")
	if githubURL == "" {
		t.Skip("github is not a curated remote-MCP catalog entry")
	}
	ms := newMockWorkspaceIntegrationManagementStore()
	workspaceID := "workspace-1"
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, WorkspaceID: &workspaceID}
	srv := &Server{store: ms, activity: events.New(ms, nil)}
	body := map[string]interface{}{
		"display_name": "GitHub MCP",
		"kind":         "github",
		"transport":    "mcp-remote",
		"endpoint":     githubURL,
		"tool_manifest": []map[string]interface{}{
			{
				"name":        "search_code",
				"description": "Search code",
				"input_schema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
	}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/github", body, 1, "workspace-1", "github", 7)

	srv.handleCreateWorkspaceIntegration(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted == nil || ms.upserted.Slug != "github" || ms.upserted.Endpoint == nil || *ms.upserted.Endpoint != githubURL {
		t.Fatalf("upserted integration = %+v", ms.upserted)
	}
	if !strings.Contains(string(ms.upserted.ToolManifest), `"input_schema"`) || !strings.Contains(string(ms.upserted.ToolManifest), `"query"`) {
		t.Fatalf("tool manifest did not preserve input_schema: %s", string(ms.upserted.ToolManifest))
	}
}

func TestCreateGitHubWorkspaceIntegrationAcceptsCuratedMCPPayload(t *testing.T) {
	githubURL := workspaceIntegrationCatalogRemoteURL("github")
	if githubURL == "" {
		t.Skip("github is not a curated remote-MCP catalog entry")
	}
	ms := newMockWorkspaceIntegrationManagementStore()
	workspaceID := "workspace-1"
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, WorkspaceID: &workspaceID}
	secretKey := "12345678901234567890123456789012"
	srv := &Server{store: ms, secretKey: secretKey, activity: events.New(ms, nil)}
	body := map[string]interface{}{
		"display_name": "GitHub",
		"kind":         "github",
		"transport":    "mcp-remote",
		"endpoint":     githubURL,
		"token":        "ghp_remote_mcp_secret",
		"token_type":   "bearer",
		"tool_manifest": []map[string]interface{}{
			{
				"name":        "search_code",
				"description": "Search code",
			},
		},
	}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/github", body, 1, "workspace-1", "github", 7)

	srv.handleCreateGitHubWorkspaceIntegration(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted == nil {
		t.Fatal("expected curated github MCP integration to be upserted")
	}
	if ms.upserted.Slug != "github" || ms.upserted.Kind != "github" || ms.upserted.Transport != "mcp-remote" {
		t.Fatalf("upserted integration = %+v", ms.upserted)
	}
	if ms.upserted.Endpoint == nil || *ms.upserted.Endpoint != githubURL {
		t.Fatalf("endpoint = %v, want %s", ms.upserted.Endpoint, githubURL)
	}
	if ms.upserted.ConnectedBy == nil || *ms.upserted.ConnectedBy != 7 || ms.upserted.ConnectedAt == nil {
		t.Fatalf("connection provenance = connected_by:%v connected_at:%v", ms.upserted.ConnectedBy, ms.upserted.ConnectedAt)
	}
	if ms.credential == nil {
		t.Fatal("expected stored bearer credential")
	}
	token, err := crypto.Decrypt(ms.credential.SecretEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt credential: %v", err)
	}
	if token != "ghp_remote_mcp_secret" {
		t.Fatalf("stored token = %q, want ghp_remote_mcp_secret", token)
	}
	if ms.credential.TokenType == nil || *ms.credential.TokenType != "bearer" {
		t.Fatalf("token type = %v, want bearer", ms.credential.TokenType)
	}
	if plugins := ms.plugins["m-1"]; len(plugins) != 1 || plugins[0].PluginID != workspaceIntegrationPluginID || !plugins[0].Enabled {
		t.Fatalf("plugins = %+v, want enabled workspace integration runtime", plugins)
	}
}

func TestCreateGenericMCPWorkspaceIntegrationRejectsEnabledToolBudgetOverflow(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{store: ms}
	tools := make([]map[string]interface{}, workspaceIntegrationMaxEnabledTools+1)
	for i := range tools {
		tools[i] = map[string]interface{}{"name": fmt.Sprintf("tool_%03d", i)}
	}
	body := map[string]interface{}{
		"display_name":  "Huge MCP",
		"transport":     "mcp-remote",
		"endpoint":      "https://mcp.example.test",
		"tool_manifest": tools,
	}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/huge-mcp", body, 1, "workspace-1", "huge-mcp", 7)

	srv.handleCreateWorkspaceIntegration(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted != nil || ms.credential != nil {
		t.Fatalf("tool budget overflow should not persist integration=%+v credential=%+v", ms.upserted, ms.credential)
	}
}

// A mid-sequence failure on a NEW integration must roll back the insert so we
// never leave an enabled integration without its credential/assignment.
func TestCreateGenericWorkspaceIntegrationRollsBackInsertOnCredentialFailure(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	ms.credentialSetErr = fmt.Errorf("boom")
	srv := &Server{store: ms, secretKey: "12345678901234567890123456789012", activity: events.New(ms, nil)}

	body := map[string]interface{}{
		"display_name":  "Linear",
		"transport":     "http",
		"endpoint":      "https://api.linear.example",
		"token":         "linear-token",
		"tool_manifest": genericGitHubToolManifest(),
	}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/linear", body, 1, "workspace-1", "linear", 7)

	srv.handleCreateWorkspaceIntegration(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	if ms.deleted == nil || ms.deleted.Slug != "linear" {
		t.Fatalf("new integration must be rolled back on failure, deleted=%+v", ms.deleted)
	}
	if len(ms.integrations) != 0 {
		t.Fatalf("integration must not remain after rollback: %+v", ms.integrations)
	}
}

func TestCreateGenericWorkspaceIntegrationRejectsInternalEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1:8080/x",
		"http://10.0.0.5/x",
		"http://192.168.1.10/x",
		"http://localhost/x",
		"http://api.internal/x",
	} {
		t.Run(endpoint, func(t *testing.T) {
			ms := newMockWorkspaceIntegrationManagementStore()
			srv := &Server{store: ms}
			body := map[string]interface{}{
				"transport":     "http",
				"endpoint":      endpoint,
				"tool_manifest": genericGitHubToolManifest(),
			}
			w := httptest.NewRecorder()
			r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/evil", body, 1, "workspace-1", "evil", 7)
			srv.handleCreateWorkspaceIntegration(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("endpoint %q: expected 400, got %d: %s", endpoint, w.Code, w.Body.String())
			}
			if ms.upserted != nil {
				t.Fatalf("endpoint %q: integration must not be persisted", endpoint)
			}
		})
	}
}

func genericGitHubToolManifest() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"name": "get_repo",
			"request": map[string]interface{}{
				"method": "GET",
				"path":   "/repos/{owner}/{repo}",
				"path_params": map[string]interface{}{
					"owner": map[string]interface{}{"source": "config", "name": "owner"},
					"repo":  map[string]interface{}{"source": "config", "name": "repo"},
				},
				"auth": map[string]interface{}{"type": "bearer", "required": false},
			},
		},
	}
}

// Reconnecting a bearer integration with a blank token (the UI's "leave blank to
// keep existing token") must NOT overwrite the stored credential.
func TestCreateGenericWorkspaceIntegrationBlankTokenPreservesCredential(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	workspaceID := "workspace-1"
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, WorkspaceID: &workspaceID}
	ms.integrations = []store.WorkspaceIntegration{{
		ID: "integration-1", WorkspaceID: workspaceID, Slug: "github",
		DisplayName: "GitHub octocat/Hello-World", Kind: "github", Transport: "http",
		Enabled: true, ToolManifest: githubToolManifest(),
		Config: json.RawMessage(`{"owner":"octocat","repo":"Hello-World"}`),
	}}
	ms.credential = &store.WorkspaceIntegrationCredential{IntegrationID: "integration-1", SecretEnc: "encrypted-existing-pat"}
	srv := &Server{store: ms, secretKey: "12345678901234567890123456789012", activity: events.New(ms, nil)}

	body := map[string]interface{}{
		"display_name":  "GitHub new-owner/New-Repo",
		"kind":          "github",
		"transport":     "http",
		"endpoint":      "https://api.github.com",
		"config":        map[string]interface{}{"owner": "new-owner", "repo": "New-Repo"},
		"token":         "",
		"tool_manifest": genericGitHubToolManifest(),
	}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/github", body, 1, "workspace-1", "github", 7)

	srv.handleCreateWorkspaceIntegration(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ms.credential == nil || ms.credential.SecretEnc != "encrypted-existing-pat" {
		t.Fatalf("blank token must preserve existing credential, got %+v", ms.credential)
	}
}

// Reconnecting/editing must preserve the original approver and the enabled
// state — the handler passes nil provenance (kept by the upsert COALESCE) and
// does not silently re-enable a disabled integration.
func TestCreateGenericWorkspaceIntegrationReconnectPreservesProvenanceAndEnabled(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	workspaceID := "workspace-1"
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, WorkspaceID: &workspaceID}
	origApprover := 3
	origTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ms.integrations = []store.WorkspaceIntegration{{
		ID: "integration-1", WorkspaceID: workspaceID, Slug: "github",
		DisplayName: "GitHub octocat/Hello-World", Kind: "github", Transport: "http",
		Enabled:    false, // previously disabled by an admin
		ApprovedBy: &origApprover, ApprovedAt: &origTime,
		ConnectedBy: &origApprover, ConnectedAt: &origTime,
		ToolManifest: githubToolManifest(),
		Config:       json.RawMessage(`{"owner":"octocat","repo":"Hello-World"}`),
	}}
	srv := &Server{store: ms, secretKey: "12345678901234567890123456789012", activity: events.New(ms, nil)}

	body := map[string]interface{}{
		"display_name":  "GitHub octocat/Other-Repo",
		"kind":          "github",
		"transport":     "http",
		"endpoint":      "https://api.github.com",
		"config":        map[string]interface{}{"owner": "octocat", "repo": "Other-Repo"},
		"token":         "",
		"tool_manifest": genericGitHubToolManifest(),
	}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/github", body, 1, "workspace-1", "github", 7)

	srv.handleCreateWorkspaceIntegration(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted.Enabled {
		t.Fatal("reconnect must not re-enable a disabled integration")
	}
	if ms.upserted.ApprovedBy != nil || ms.upserted.ApprovedAt != nil {
		t.Fatalf("reconnect must defer approval provenance to COALESCE (nil), got by=%v at=%v", ms.upserted.ApprovedBy, ms.upserted.ApprovedAt)
	}
	if ms.upserted.ConnectedBy != nil || ms.upserted.ConnectedAt != nil {
		t.Fatalf("reconnect without a new token must not restamp connection provenance, got by=%v at=%v", ms.upserted.ConnectedBy, ms.upserted.ConnectedAt)
	}
}

func TestWorkspaceIntegrationExtensibilityUsesGenericCreateRoute(t *testing.T) {
	serverSourceBytes, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server source: %v", err)
	}
	serverSource := string(serverSourceBytes)
	for _, required := range []string{
		`/workspaces/{workspaceID}/integrations/{integrationSlug}`,
		`/workspace-integrations/{integrationSlug}`,
	} {
		if !strings.Contains(serverSource, required) {
			t.Fatalf("server source missing generic create route %q", required)
		}
	}
	for _, forbidden := range []string{
		"/workspaces/{workspaceID}/integrations/linear",
		"/workspace-integrations/linear",
		"handleCreateLinearWorkspaceIntegration",
	} {
		if strings.Contains(serverSource, forbidden) {
			t.Fatalf("new integration type should not need bespoke route/handler %q", forbidden)
		}
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read api directory: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == "workspace_integrations_linear.go" {
			t.Fatal("new integration type should not need a workspace_integrations_linear.go file")
		}
	}

	gatewaySourceBytes, err := os.ReadFile("workspace_integrations_gateway.go")
	if err != nil {
		t.Fatalf("read gateway source: %v", err)
	}
	gatewaySource := string(gatewaySourceBytes)
	for _, forbidden := range []string{"isLinearWorkspaceIntegration", "callLinearWorkspaceIntegration"} {
		if strings.Contains(gatewaySource, forbidden) {
			t.Fatalf("new integration type should not need gateway branch %q", forbidden)
		}
	}
}

func TestTestWorkspaceIntegrationRunsFirstAllowedManifestTool(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octocat/Hello-World" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"full_name":         "octocat/Hello-World",
			"description":       "Example repository",
			"private":           false,
			"html_url":          "https://github.com/octocat/Hello-World",
			"default_branch":    "main",
			"open_issues_count": 3,
			"stargazers_count":  80,
			"forks_count":       9,
			"updated_at":        "2026-06-16T12:00:00Z",
		})
	}))
	defer api.Close()
	previousBaseURL := githubAPIBaseURL
	githubAPIBaseURL = api.URL
	defer func() { githubAPIBaseURL = previousBaseURL }()

	ms := newMockWorkspaceIntegrationManagementStore()
	ms.integrations = []store.WorkspaceIntegration{
		{
			ID:           "integration-1",
			WorkspaceID:  "workspace-1",
			Slug:         "github",
			DisplayName:  "GitHub octocat/Hello-World",
			Kind:         "github",
			Transport:    "http",
			Enabled:      true,
			ToolManifest: githubToolManifest(),
			Config:       json.RawMessage(`{"owner":"octocat","repo":"Hello-World"}`),
		},
	}
	srv := &Server{store: ms, allowInsecureWorkspaceIntegrationEndpoints: true}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementSlugRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/github/test", 1, "workspace-1", "github", 7)

	srv.handleTestWorkspaceIntegration(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Status string                 `json:"status"`
		Result map[string]interface{} `json:"result"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "ok" || resp.Result["full_name"] != "octocat/Hello-World" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestTestWorkspaceIntegrationRunsNormalizedConnectionTool(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/records" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "source": "normalized"})
	}))
	defer api.Close()

	requestMapping, err := json.Marshal(workspaceIntegrationHTTPRequest{
		Method: "GET",
		Path:   "/records",
	})
	if err != nil {
		t.Fatalf("marshal request mapping: %v", err)
	}
	provenance, err := json.Marshal(map[string]json.RawMessage{"request": requestMapping})
	if err != nil {
		t.Fatalf("marshal provenance: %v", err)
	}
	connectionConfig, err := json.Marshal(map[string]interface{}{
		"transport": "http",
		"endpoint":  api.URL,
	})
	if err != nil {
		t.Fatalf("marshal connection config: %v", err)
	}
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
				ID:              "connection-records",
				WorkspaceID:     "workspace-1",
				SourceID:        "source-openapi",
				Slug:            "records-prod",
				DisplayName:     "Records Prod",
				Scope:           "workspace",
				CredentialState: "connected",
				Enabled:         true,
				Config:          connectionConfig,
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{
				{
					ID:           "snapshot-create",
					WorkspaceID:  "workspace-1",
					ConnectionID: "connection-records",
					ToolName:     "create_record",
					ToolAddress:  "wi.workspace-1.openapi.records-prod.create_record",
					Description:  "Create record",
					Access:       "write",
					Source:       "openapi",
					Provenance:   provenance,
				},
				{
					ID:           "snapshot-list",
					WorkspaceID:  "workspace-1",
					ConnectionID: "connection-records",
					ToolName:     "list_records",
					ToolAddress:  "wi.workspace-1.openapi.records-prod.list_records",
					Description:  "List records",
					Access:       "read",
					Source:       "openapi",
					Provenance:   provenance,
				},
			},
			Policies: []store.WorkspaceIntegrationToolPolicy{
				{WorkspaceID: "workspace-1", ConnectionID: "connection-records", ToolName: "create_record", Policy: workspaceIntegrationPolicyBlock, Source: "api"},
				{WorkspaceID: "workspace-1", ConnectionID: "connection-records", ToolName: "list_records", Policy: workspaceIntegrationPolicyAllow, Source: "api"},
			},
		}},
	}
	srv := &Server{store: ms, allowInsecureWorkspaceIntegrationEndpoints: true}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementSlugRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/records-prod/test", 1, "workspace-1", "records-prod", 7)

	srv.handleTestWorkspaceIntegration(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Status string                 `json:"status"`
		Result map[string]interface{} `json:"result"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "ok" || resp.Result["source"] != "normalized" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestRevokeWorkspaceIntegrationDeletesAndAuditsActor(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	now := time.Date(2026, 6, 16, 12, 30, 0, 0, time.UTC)
	actorID := 7
	ms.integrations = []store.WorkspaceIntegration{
		{
			ID:           "integration-1",
			WorkspaceID:  "workspace-1",
			Slug:         "github",
			DisplayName:  "GitHub octocat/Hello-World",
			Kind:         "github",
			Transport:    "http",
			Enabled:      true,
			ToolManifest: githubToolManifest(),
			Config:       json.RawMessage(`{"owner":"octocat","repo":"Hello-World"}`),
			ApprovedBy:   &actorID,
			ApprovedAt:   &now,
			ConnectedBy:  &actorID,
			ConnectedAt:  &now,
			CreatedAt:    now,
			UpdatedAt:    now,
		},
	}
	srv := &Server{store: ms, activity: events.New(ms, nil)}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementSlugRequest(http.MethodDelete, "/api/accounts/1/workspaces/workspace-1/integrations/github", 1, "workspace-1", "github", actorID)

	srv.handleRevokeWorkspaceIntegration(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ms.deleted == nil || ms.deleted.Slug != "github" {
		t.Fatalf("deleted integration = %+v", ms.deleted)
	}
	if len(ms.integrations) != 0 {
		t.Fatalf("integrations after revoke = %+v, want empty", ms.integrations)
	}
	var resp struct {
		Status      string                             `json:"status"`
		Integration workspaceIntegrationManagementItem `json:"integration"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "revoked" || resp.Integration.Target != "octocat/Hello-World" {
		t.Fatalf("response = %+v", resp)
	}
	activity := waitForWorkspaceIntegrationActivity(t, ms)
	if activity.Action != "config.workspace_integration_revoked" || activity.ActorID == nil || *activity.ActorID != actorID {
		t.Fatalf("activity = %+v", activity)
	}
	if !strings.Contains(string(activity.Detail), `"integration_slug":"github"`) {
		t.Fatalf("activity detail = %s", string(activity.Detail))
	}
	// Revoking an integration is enforced live by the gateway; it must NOT bump the
	// token watermark (that dropped all tools on every running machine until restart).
	if len(ms.revokedWorkspaceTokenWorkspaces) != 0 {
		t.Fatalf("revoke must not revoke workspace tokens, got %v", ms.revokedWorkspaceTokenWorkspaces)
	}
}

func TestRevokeWorkspaceIntegrationDeletesNormalizedConnection(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	actorID := 7
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
				ID:              "connection-records",
				WorkspaceID:     "workspace-1",
				SourceID:        "source-openapi",
				Slug:            "records-prod",
				DisplayName:     "Records Prod",
				Scope:           "workspace",
				CredentialState: "connected",
				Enabled:         true,
				Config:          json.RawMessage(`{"transport":"http","endpoint":"https://records.example.test"}`),
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{
				{
					ID:           "snapshot-list",
					WorkspaceID:  "workspace-1",
					ConnectionID: "connection-records",
					ToolName:     "list_records",
					ToolAddress:  "wi.workspace-1.openapi.records-prod.list_records",
					Description:  "List records",
					Access:       "read",
					Source:       "openapi",
				},
			},
		}},
	}
	srv := &Server{store: ms, activity: events.New(ms, nil)}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementSlugRequest(http.MethodDelete, "/api/accounts/1/workspaces/workspace-1/integrations/records-prod", 1, "workspace-1", "records-prod", actorID)

	srv.handleRevokeWorkspaceIntegration(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ms.deletedConnection == nil || ms.deletedConnection.Connection.ID != "connection-records" {
		t.Fatalf("deleted connection = %+v", ms.deletedConnection)
	}
	if len(ms.connectorProjections["workspace-1"]) != 0 {
		t.Fatalf("projections after revoke = %+v, want empty", ms.connectorProjections["workspace-1"])
	}
	var resp struct {
		Status      string                             `json:"status"`
		Integration workspaceIntegrationManagementItem `json:"integration"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "revoked" || resp.Integration.ID != "connection-records" || resp.Integration.Slug != "records-prod" || resp.Integration.Kind != "openapi" {
		t.Fatalf("response = %+v", resp)
	}
	activity := waitForWorkspaceIntegrationActivity(t, ms)
	if activity.Action != "config.workspace_integration_revoked" || activity.ActorID == nil || *activity.ActorID != actorID {
		t.Fatalf("activity = %+v", activity)
	}
	if !strings.Contains(string(activity.Detail), `"connection_id":"connection-records"`) {
		t.Fatalf("activity detail = %s", string(activity.Detail))
	}
	if len(ms.revokedWorkspaceTokenWorkspaces) != 0 {
		t.Fatalf("normalized revoke must not revoke workspace tokens, got %v", ms.revokedWorkspaceTokenWorkspaces)
	}
}

func TestUpdateWorkspaceIntegrationPolicyNormalizesAndAuditsActor(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	now := time.Date(2026, 6, 16, 12, 30, 0, 0, time.UTC)
	actorID := 7
	ms.integrations = []store.WorkspaceIntegration{
		{
			ID:           "integration-1",
			WorkspaceID:  "workspace-1",
			Slug:         "github",
			DisplayName:  "GitHub octocat/Hello-World",
			Kind:         "github",
			Transport:    "http",
			Enabled:      true,
			ToolManifest: githubToolManifest(),
			Config:       json.RawMessage(`{"owner":"octocat","repo":"Hello-World"}`),
			ApprovedBy:   &actorID,
			ApprovedAt:   &now,
			ConnectedBy:  &actorID,
			ConnectedAt:  &now,
			CreatedAt:    now,
			UpdatedAt:    now,
		},
	}
	srv := &Server{store: ms, activity: events.New(ms, nil)}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPut, "/api/accounts/1/workspaces/workspace-1/integrations/github/policy", map[string]interface{}{
		"allowed_tools": []string{"github.get_repo", "get_repo"},
		"denied_tools":  []string{"list_issues"},
	}, 1, "workspace-1", "github", actorID)

	srv.handleUpdateWorkspaceIntegrationPolicy(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted == nil {
		t.Fatal("expected policy update to upsert integration")
	}
	if got := strings.Join(ms.upserted.AllowedTools, ","); got != "get_repo" {
		t.Fatalf("allowed tools = %q, want get_repo", got)
	}
	if got := strings.Join(ms.upserted.DeniedTools, ","); got != "list_issues" {
		t.Fatalf("denied tools = %q, want list_issues", got)
	}
	var item workspaceIntegrationManagementItem
	if err := json.NewDecoder(w.Body).Decode(&item); err != nil {
		t.Fatalf("decode item: %v", err)
	}
	if got := strings.Join(item.AllowedTools, ","); got != "get_repo" {
		t.Fatalf("response allowed tools = %q, want get_repo", got)
	}
	if got := strings.Join(item.DeniedTools, ","); got != "list_issues" {
		t.Fatalf("response denied tools = %q, want list_issues", got)
	}
	activity := waitForWorkspaceIntegrationActivity(t, ms)
	if activity.Action != "config.workspace_integration_policy_updated" || activity.ActorID == nil || *activity.ActorID != actorID {
		t.Fatalf("activity = %+v", activity)
	}
	if !strings.Contains(string(activity.Detail), `"denied_tools":["list_issues"]`) {
		t.Fatalf("activity detail = %s", string(activity.Detail))
	}
	// Policy is enforced live by the gateway; a tightened policy must NOT bump the
	// token watermark (that dropped all tools on every running machine until restart).
	if len(ms.revokedWorkspaceTokenWorkspaces) != 0 {
		t.Fatalf("policy update must not revoke workspace tokens, got %v", ms.revokedWorkspaceTokenWorkspaces)
	}
}

func TestUpdateWorkspaceIntegrationPolicyWritesNormalizedConnectionPolicies(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	actorID := 7
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
				ID:              "connection-records",
				WorkspaceID:     "workspace-1",
				SourceID:        "source-openapi",
				Slug:            "records-prod",
				DisplayName:     "Records Prod",
				Scope:           "workspace",
				CredentialState: "connected",
				Enabled:         true,
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{
				{
					ID:           "snapshot-list",
					WorkspaceID:  "workspace-1",
					ConnectionID: "connection-records",
					ToolName:     "list_records",
					ToolAddress:  "wi.workspace-1.openapi.records-prod.list_records",
					Description:  "List records",
					Access:       "read",
					Source:       "openapi",
				},
				{
					ID:           "snapshot-create",
					WorkspaceID:  "workspace-1",
					ConnectionID: "connection-records",
					ToolName:     "create_record",
					ToolAddress:  "wi.workspace-1.openapi.records-prod.create_record",
					Description:  "Create records",
					Access:       "write",
					Source:       "openapi",
				},
			},
		}},
	}
	srv := &Server{store: ms, activity: events.New(ms, nil)}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPut, "/api/accounts/1/workspaces/workspace-1/integrations/records-prod/policy", map[string]interface{}{
		"allowed_tools": []string{"wi.workspace-1.openapi.records-prod.list_records", "records-prod.list_records"},
		"denied_tools":  []string{"create_record"},
	}, 1, "workspace-1", "records-prod", actorID)

	srv.handleUpdateWorkspaceIntegrationPolicy(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted != nil {
		t.Fatalf("normalized policy update should not upsert legacy integration: %+v", ms.upserted)
	}
	if ms.replacedPolicyConnectionID != "connection-records" {
		t.Fatalf("policy connection id = %q, want connection-records", ms.replacedPolicyConnectionID)
	}
	policies := map[string]string{}
	for _, policy := range ms.replacedPolicies {
		policies[policy.ToolName] = policy.Policy
		if policy.WorkspaceID != "workspace-1" || policy.ConnectionID != "connection-records" || policy.Source != "api" {
			t.Fatalf("policy row = %+v", policy)
		}
	}
	if policies["list_records"] != workspaceIntegrationPolicyAllow || policies["create_record"] != workspaceIntegrationPolicyBlock {
		t.Fatalf("policies = %+v, want list allow and create block", policies)
	}
	var item workspaceIntegrationManagementItem
	if err := json.NewDecoder(w.Body).Decode(&item); err != nil {
		t.Fatalf("decode item: %v", err)
	}
	if item.ID != "connection-records" || item.Slug != "records-prod" || item.Kind != "openapi" || item.ToolCount != 2 {
		t.Fatalf("response item = %+v", item)
	}
	if got := strings.Join(item.AllowedTools, ","); got != "list_records" {
		t.Fatalf("response allowed tools = %q, want list_records", got)
	}
	if got := strings.Join(item.DeniedTools, ","); got != "create_record" {
		t.Fatalf("response denied tools = %q, want create_record", got)
	}
	activity := waitForWorkspaceIntegrationActivity(t, ms)
	if activity.Action != "config.workspace_integration_policy_updated" || activity.ActorID == nil || *activity.ActorID != actorID {
		t.Fatalf("activity = %+v", activity)
	}
	if !strings.Contains(string(activity.Detail), `"connection_id":"connection-records"`) {
		t.Fatalf("activity detail = %s", string(activity.Detail))
	}
}

func TestUpdateWorkspaceIntegrationPolicyRejectsUnknownTool(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	ms.integrations = []store.WorkspaceIntegration{
		{
			ID:           "integration-1",
			WorkspaceID:  "workspace-1",
			Slug:         "github",
			DisplayName:  "GitHub",
			Kind:         "github",
			Transport:    "http",
			Enabled:      true,
			ToolManifest: githubToolManifest(),
			Config:       json.RawMessage(`{"owner":"octocat","repo":"Hello-World"}`),
		},
	}
	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceIntegrationPolicyRequest(http.MethodPut, "/api/accounts/1/workspaces/workspace-1/integrations/github/policy", map[string]interface{}{
		"denied_tools": []string{"delete_repo"},
	}, 1, "workspace-1", "github", 7)

	srv.handleUpdateWorkspaceIntegrationPolicy(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if ms.upserted != nil {
		t.Fatalf("unexpected upsert on invalid policy: %+v", ms.upserted)
	}
}

func TestStartGoogleWorkspaceOAuthReturnsConsentURL(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{
		store:             ms,
		secretKey:         "12345678901234567890123456789012",
		oauthClientID:     "google-client",
		oauthClientSecret: "google-secret",
		backendURL:        "https://api.example.test",
	}
	req := workspaceIntegrationManagementSlugRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/google-workspace/oauth/connect", 1, "workspace-1", googleWorkspaceIntegrationSlug, 7)
	w := httptest.NewRecorder()

	srv.handleStartWorkspaceIntegrationOAuthBySlug(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	parsed, err := url.Parse(resp["url"])
	if err != nil {
		t.Fatalf("parse oauth url: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "accounts.google.com" {
		t.Fatalf("oauth host = %s://%s, want Google authorization endpoint", parsed.Scheme, parsed.Host)
	}
	values := parsed.Query()
	if values.Get("client_id") != "google-client" {
		t.Fatalf("client_id = %q", values.Get("client_id"))
	}
	if values.Get("access_type") != "offline" {
		t.Fatalf("access_type = %q", values.Get("access_type"))
	}
	if values.Get("redirect_uri") != "https://api.example.test/api/workspace-integrations/oauth/callback" {
		t.Fatalf("redirect_uri = %q", values.Get("redirect_uri"))
	}
	if !strings.Contains(values.Get("scope"), "https://www.googleapis.com/auth/calendar.events.readonly") {
		t.Fatalf("scope missing calendar events readonly: %q", values.Get("scope"))
	}
	if !strings.Contains(values.Get("scope"), "https://www.googleapis.com/auth/gmail.readonly") {
		t.Fatalf("scope missing gmail readonly: %q", values.Get("scope"))
	}
	for _, overBroadScope := range []string{
		"https://www.googleapis.com/auth/documents.readonly",
		"https://www.googleapis.com/auth/drive.file",
	} {
		if strings.Contains(values.Get("scope"), overBroadScope) {
			t.Fatalf("scope contains over-broad grant %q: %q", overBroadScope, values.Get("scope"))
		}
	}
	state, err := srv.verifyWorkspaceIntegrationOAuthState(values.Get("state"))
	if err != nil {
		t.Fatalf("state should verify: %v", err)
	}
	if state.Provider != googleWorkspaceIntegrationSlug {
		t.Fatalf("state provider = %q", state.Provider)
	}
}

func TestStartGoogleWorkspaceOAuthUsesRequestedPermissionLevels(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{
		store:             ms,
		secretKey:         "12345678901234567890123456789012",
		oauthClientID:     "google-client",
		oauthClientSecret: "google-secret",
		backendURL:        "https://api.example.test",
	}
	req := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/google-workspace/oauth/connect", map[string]interface{}{
		"permissions": map[string]string{
			"gmail":    "read_write",
			"drive":    "read",
			"calendar": "off",
			"docs":     "read_write",
		},
	}, 1, "workspace-1", googleWorkspaceIntegrationSlug, 7)
	w := httptest.NewRecorder()

	srv.handleStartWorkspaceIntegrationOAuthBySlug(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	parsed, err := url.Parse(resp["url"])
	if err != nil {
		t.Fatalf("parse oauth url: %v", err)
	}
	scope := parsed.Query().Get("scope")
	for _, want := range []string{
		"https://www.googleapis.com/auth/gmail.readonly",
		"https://www.googleapis.com/auth/gmail.send",
		"https://www.googleapis.com/auth/drive.metadata.readonly",
		"https://www.googleapis.com/auth/drive.file",
	} {
		if !strings.Contains(scope, want) {
			t.Fatalf("scope missing %q: %q", want, scope)
		}
	}
	if strings.Contains(scope, "https://www.googleapis.com/auth/calendar.events.readonly") {
		t.Fatalf("calendar should be off in scope: %q", scope)
	}
	state, err := srv.verifyWorkspaceIntegrationOAuthState(parsed.Query().Get("state"))
	if err != nil {
		t.Fatalf("state should verify: %v", err)
	}
	if state.PermissionLevels["calendar"] != "off" || state.PermissionLevels["gmail"] != "read_write" {
		t.Fatalf("state permission levels = %+v", state.PermissionLevels)
	}
}

func TestStartWorkspaceIntegrationOAuthRejectsUnknownProvider(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{
		store:             ms,
		secretKey:         "12345678901234567890123456789012",
		oauthClientID:     "google-client",
		oauthClientSecret: "google-secret",
	}
	req := workspaceIntegrationManagementSlugRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/unknown/oauth/connect", 1, "workspace-1", "unknown", 7)
	w := httptest.NewRecorder()

	srv.handleStartWorkspaceIntegrationOAuthBySlug(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCustomMCPDynamicOAuthStartCallbackStoresTokensAndReprobes(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	ms.connectionCredentialsByLegacy = map[string][]store.WorkspaceIntegrationConnection{
		"integration-1": {{
			ID:          "connection-oauth-mcp",
			WorkspaceID: "workspace-1",
			Slug:        "oauth-mcp",
		}},
	}
	workspaceID := "workspace-1"
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, WorkspaceID: &workspaceID, Kind: store.MachineKindOpenClaw, Name: "Builder", Status: "running"}
	secretKey := "12345678901234567890123456789012"
	srv := &Server{
		store:           ms,
		secretKey:       secretKey,
		backendURL:      "https://api.example.test",
		frontendBaseURL: "https://app.example.test",
		allowInsecureWorkspaceIntegrationEndpoints: true,
	}

	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mcp":
			if r.Header.Get("Authorization") == "" {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+api.URL+`/metadata"`)
				http.Error(w, "missing auth", http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Authorization") != "Bearer custom-access" {
				t.Fatalf("mcp authorization = %q", r.Header.Get("Authorization"))
			}
			var payload workspaceIntegrationMCPJSONRPCRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode mcp payload: %v", err)
			}
			switch payload.Method {
			case "initialize":
				w.Header().Set("Mcp-Session-Id", "session-custom-oauth")
				writeJSON(w, http.StatusOK, map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      payload.ID,
					"result": map[string]interface{}{
						"protocolVersion": workspaceIntegrationMCPProtocolVersion,
						"serverInfo":      map[string]interface{}{"name": "OAuth MCP", "version": "2.0.0"},
						"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
					},
				})
			case "notifications/initialized":
				w.WriteHeader(http.StatusAccepted)
			case "tools/list":
				if r.Header.Get("Mcp-Session-Id") != "session-custom-oauth" {
					t.Fatalf("session id = %q", r.Header.Get("Mcp-Session-Id"))
				}
				writeJSON(w, http.StatusOK, map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      payload.ID,
					"result": map[string]interface{}{
						"tools": []map[string]interface{}{
							{"name": "search_records", "description": "Search records"},
							{"name": "create_record", "description": "Create records"},
						},
					},
				})
			default:
				t.Fatalf("unexpected mcp method %q", payload.Method)
			}
		case "/metadata":
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"resource":              api.URL + "/mcp",
				"authorization_servers": []string{api.URL},
				"scopes_supported":      []string{"records.read", "records.write"},
			})
		case "/.well-known/oauth-authorization-server":
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"issuer":                           api.URL,
				"authorization_endpoint":           api.URL + "/authorize",
				"token_endpoint":                   api.URL + "/token",
				"registration_endpoint":            api.URL + "/register",
				"scopes_supported":                 []string{"records.read", "records.write"},
				"code_challenge_methods_supported": []string{"S256"},
			})
		case "/register":
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode registration: %v", err)
			}
			redirects, _ := body["redirect_uris"].([]interface{})
			if len(redirects) != 1 || redirects[0] != "https://api.example.test/api/workspace-integrations/oauth/callback" {
				t.Fatalf("redirect_uris = %+v", body["redirect_uris"])
			}
			writeJSON(w, http.StatusCreated, map[string]interface{}{
				"client_id":                  "custom-client",
				"token_endpoint_auth_method": "none",
			})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse token form: %v", err)
			}
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "custom-code" {
				t.Fatalf("token grant/code = %v", r.Form)
			}
			if r.Form.Get("client_id") != "custom-client" || r.Form.Get("code_verifier") == "" {
				t.Fatalf("token client/verifier = %v", r.Form)
			}
			if r.Form.Get("resource") != api.URL+"/mcp" {
				t.Fatalf("resource = %q", r.Form.Get("resource"))
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"access_token":  "custom-access",
				"refresh_token": "custom-refresh",
				"expires_in":    3600,
				"token_type":    "Bearer",
				"scope":         "records.read",
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer api.Close()

	startReq := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/oauth-mcp/oauth/connect", map[string]interface{}{
		"url":          api.URL + "/mcp",
		"display_name": "OAuth MCP",
		"scopes":       []string{"records.read"},
	}, 1, "workspace-1", "oauth-mcp", 7)
	startW := httptest.NewRecorder()
	srv.handleStartWorkspaceIntegrationOAuthBySlug(startW, startReq)
	if startW.Code != http.StatusOK {
		t.Fatalf("start expected 200, got %d: %s", startW.Code, startW.Body.String())
	}
	var startResp map[string]string
	if err := json.NewDecoder(startW.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	authURL, err := url.Parse(startResp["url"])
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	if !strings.HasPrefix(authURL.String(), api.URL+"/authorize") {
		t.Fatalf("auth url = %q", authURL.String())
	}
	authValues := authURL.Query()
	if authValues.Get("client_id") != "custom-client" || authValues.Get("code_challenge_method") != "S256" {
		t.Fatalf("auth values = %v", authValues)
	}
	if authValues.Get("resource") != api.URL+"/mcp" {
		t.Fatalf("auth resource = %q", authValues.Get("resource"))
	}
	if authValues.Get("scope") != "records.read" {
		t.Fatalf("auth scope = %q", authValues.Get("scope"))
	}
	state, err := srv.verifyWorkspaceIntegrationOAuthState(authValues.Get("state"))
	if err != nil {
		t.Fatalf("state should verify: %v", err)
	}
	if state.Provider != "oauth-mcp" || state.CustomMCP == nil || state.CustomMCP.ClientID != "custom-client" {
		t.Fatalf("state = %+v", state)
	}
	// PKCE verifier must be encrypted in the browser-visible state, not plaintext.
	// crypto.Decrypt only succeeds on ciphertext, and the challenge must match the
	// decrypted verifier.
	decryptedVerifier, err := crypto.Decrypt(state.CustomMCP.CodeVerifier, srv.secretKey)
	if err != nil {
		t.Fatalf("code verifier should be encrypted in state: %v", err)
	}
	if workspaceIntegrationOAuthCodeChallenge(decryptedVerifier) != authValues.Get("code_challenge") {
		t.Fatalf("code_challenge does not match decrypted verifier")
	}

	callbackReq := httptest.NewRequest(http.MethodGet, "/api/workspace-integrations/oauth/callback?code=custom-code&state="+url.QueryEscape(authValues.Get("state")), nil)
	callbackReq = callbackReq.WithContext(auth.WithUser(callbackReq.Context(), &auth.Claims{UserID: 7, Email: "owner@example.test"}))
	callbackW := httptest.NewRecorder()
	srv.handleWorkspaceIntegrationOAuthCallback(callbackW, callbackReq)
	if callbackW.Code != http.StatusFound {
		t.Fatalf("callback expected 302, got %d: %s", callbackW.Code, callbackW.Body.String())
	}
	if location := callbackW.Header().Get("Location"); location != "https://app.example.test/workspaces/workspace-1/integrations?oauth-mcp=connected" {
		t.Fatalf("redirect location = %q", location)
	}
	if ms.upserted == nil || ms.upserted.Slug != "oauth-mcp" || ms.upserted.Kind != "custom-mcp" || ms.upserted.Transport != "mcp-remote" {
		t.Fatalf("upserted integration = %+v", ms.upserted)
	}
	if ms.upserted.DisplayName != "OAuth MCP" || ms.upserted.Endpoint == nil || *ms.upserted.Endpoint != api.URL+"/mcp" {
		t.Fatalf("upserted target = %+v endpoint=%v", ms.upserted, ms.upserted.Endpoint)
	}
	if !containsString(ms.upserted.AllowedTools, "search_records") || !containsString(ms.upserted.DeniedTools, "create_record") {
		t.Fatalf("policy allowed=%+v denied=%+v", ms.upserted.AllowedTools, ms.upserted.DeniedTools)
	}
	if !strings.Contains(string(ms.upserted.ToolManifest), `"type":"oauth"`) || !strings.Contains(string(ms.upserted.ToolManifest), `"client_id":"custom-client"`) {
		t.Fatalf("manifest = %s", string(ms.upserted.ToolManifest))
	}
	if ms.credential == nil {
		t.Fatal("expected encrypted oauth credential")
	}
	access, err := crypto.Decrypt(ms.credential.SecretEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt access token: %v", err)
	}
	if access != "custom-access" {
		t.Fatalf("access token = %q", access)
	}
	if ms.credential.RefreshEnc == nil {
		t.Fatal("expected refresh token")
	}
	refresh, err := crypto.Decrypt(*ms.credential.RefreshEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt refresh token: %v", err)
	}
	if refresh != "custom-refresh" {
		t.Fatalf("refresh token = %q", refresh)
	}
	if ms.connectionCredential == nil {
		t.Fatal("expected encrypted normalized oauth connection credential")
	}
	if ms.connectionCredential.ConnectionID != "connection-oauth-mcp" {
		t.Fatalf("connection credential connection_id = %q", ms.connectionCredential.ConnectionID)
	}
	connectionAccess, err := crypto.Decrypt(ms.connectionCredential.SecretEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt connection access token: %v", err)
	}
	if connectionAccess != "custom-access" {
		t.Fatalf("connection access token = %q", connectionAccess)
	}
	if ms.connectionCredential.RefreshEnc == nil {
		t.Fatal("expected normalized connection refresh token")
	}
	connectionRefresh, err := crypto.Decrypt(*ms.connectionCredential.RefreshEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt connection refresh token: %v", err)
	}
	if connectionRefresh != "custom-refresh" {
		t.Fatalf("connection refresh token = %q", connectionRefresh)
	}
	if len(ms.plugins["m-1"]) != 1 || !ms.plugins["m-1"][0].Enabled {
		t.Fatalf("runtime plugins = %+v", ms.plugins["m-1"])
	}
	// Regression: saving a custom MCP server is additive and must NOT revoke tokens.
	if len(ms.revokedWorkspaceTokenWorkspaces) != 0 {
		t.Fatalf("custom MCP connect must not revoke workspace tokens, got %v", ms.revokedWorkspaceTokenWorkspaces)
	}
}

func TestCustomMCPStaticClientOAuthStartCallbackStoresTokensAndReprobes(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	workspaceID := "workspace-1"
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, WorkspaceID: &workspaceID, Kind: store.MachineKindOpenClaw, Name: "Builder", Status: "running"}
	secretKey := "12345678901234567890123456789012"
	srv := &Server{
		store:           ms,
		secretKey:       secretKey,
		backendURL:      "https://api.example.test",
		frontendBaseURL: "https://app.example.test",
		allowInsecureWorkspaceIntegrationEndpoints: true,
	}

	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mcp":
			if r.Header.Get("Authorization") == "" {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+api.URL+`/metadata"`)
				http.Error(w, "missing auth", http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Authorization") != "Bearer static-access" {
				t.Fatalf("mcp authorization = %q", r.Header.Get("Authorization"))
			}
			var payload workspaceIntegrationMCPJSONRPCRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode mcp payload: %v", err)
			}
			switch payload.Method {
			case "initialize":
				w.Header().Set("Mcp-Session-Id", "session-static-oauth")
				writeJSON(w, http.StatusOK, map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      payload.ID,
					"result": map[string]interface{}{
						"protocolVersion": workspaceIntegrationMCPProtocolVersion,
						"serverInfo":      map[string]interface{}{"name": "Static OAuth MCP", "version": "2.0.0"},
						"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
					},
				})
			case "notifications/initialized":
				w.WriteHeader(http.StatusAccepted)
			case "tools/list":
				if r.Header.Get("Mcp-Session-Id") != "session-static-oauth" {
					t.Fatalf("session id = %q", r.Header.Get("Mcp-Session-Id"))
				}
				writeJSON(w, http.StatusOK, map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      payload.ID,
					"result": map[string]interface{}{
						"tools": []map[string]interface{}{
							{"name": "search_records", "description": "Search records"},
						},
					},
				})
			default:
				t.Fatalf("unexpected mcp method %q", payload.Method)
			}
		case "/metadata":
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"resource":              api.URL + "/mcp",
				"authorization_servers": []string{api.URL},
				"scopes_supported":      []string{"records.read"},
			})
		case "/.well-known/oauth-authorization-server":
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"issuer":                           api.URL,
				"authorization_endpoint":           api.URL + "/authorize",
				"token_endpoint":                   api.URL + "/token",
				"scopes_supported":                 []string{"records.read"},
				"code_challenge_methods_supported": []string{"S256"},
			})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse token form: %v", err)
			}
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "static-code" {
				t.Fatalf("token grant/code = %v", r.Form)
			}
			if r.Form.Get("client_id") != "static-public-client" || r.Form.Get("client_secret") != "" || r.Form.Get("code_verifier") == "" {
				t.Fatalf("token client/verifier = %v", r.Form)
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"access_token":  "static-access",
				"refresh_token": "static-refresh",
				"expires_in":    3600,
				"token_type":    "Bearer",
				"scope":         "records.read",
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer api.Close()

	startReq := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/static-oauth-mcp/oauth/connect", map[string]interface{}{
		"url":          api.URL + "/mcp",
		"display_name": "Static OAuth MCP",
		"scopes":       []string{"records.read"},
		"client_id":    "static-public-client",
	}, 1, "workspace-1", "static-oauth-mcp", 7)
	startW := httptest.NewRecorder()
	srv.handleStartWorkspaceIntegrationOAuthBySlug(startW, startReq)
	if startW.Code != http.StatusOK {
		t.Fatalf("start expected 200, got %d: %s", startW.Code, startW.Body.String())
	}
	var startResp map[string]string
	if err := json.NewDecoder(startW.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	authURL, err := url.Parse(startResp["url"])
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	authValues := authURL.Query()
	if authValues.Get("client_id") != "static-public-client" || authValues.Get("code_challenge_method") != "S256" {
		t.Fatalf("auth values = %v", authValues)
	}
	state, err := srv.verifyWorkspaceIntegrationOAuthState(authValues.Get("state"))
	if err != nil {
		t.Fatalf("state should verify: %v", err)
	}
	if state.CustomMCP == nil || state.CustomMCP.ClientID != "static-public-client" || state.CustomMCP.RegistrationEndpoint != "" {
		t.Fatalf("state custom mcp = %+v", state.CustomMCP)
	}

	callbackReq := httptest.NewRequest(http.MethodGet, "/api/workspace-integrations/oauth/callback?code=static-code&state="+url.QueryEscape(authValues.Get("state")), nil)
	callbackReq = callbackReq.WithContext(auth.WithUser(callbackReq.Context(), &auth.Claims{UserID: 7, Email: "owner@example.test"}))
	callbackW := httptest.NewRecorder()
	srv.handleWorkspaceIntegrationOAuthCallback(callbackW, callbackReq)
	if callbackW.Code != http.StatusFound {
		t.Fatalf("callback expected 302, got %d: %s", callbackW.Code, callbackW.Body.String())
	}
	if ms.upserted == nil || ms.upserted.Slug != "static-oauth-mcp" {
		t.Fatalf("expected static oauth mcp integration to be saved: %+v", ms.upserted)
	}
	if !strings.Contains(string(ms.upserted.Config), `"oauth_client_id":"static-public-client"`) || strings.Contains(string(ms.upserted.Config), "static-refresh") {
		t.Fatalf("saved config = %s", string(ms.upserted.Config))
	}
	if ms.credential == nil || ms.credential.SecretEnc == "" || ms.credential.RefreshEnc == nil {
		t.Fatalf("expected encrypted oauth credential")
	}
}

func TestCustomMCPOAuthAccessTokenRefreshUsesDynamicClientAndResource(t *testing.T) {
	secretKey := "12345678901234567890123456789012"
	refreshEnc, err := crypto.Encrypt("custom-refresh", secretKey)
	if err != nil {
		t.Fatalf("encrypt refresh: %v", err)
	}
	accessEnc, err := crypto.Encrypt("expired-access", secretKey)
	if err != nil {
		t.Fatalf("encrypt access: %v", err)
	}
	expired := time.Now().Add(-time.Hour)
	ms := newMockWorkspaceIntegrationManagementStore()
	ms.credential = &store.WorkspaceIntegrationCredential{
		IntegrationID: "integration-custom-oauth",
		SecretEnc:     accessEnc,
		RefreshEnc:    &refreshEnc,
		TokenType:     stringPtr("Bearer"),
		ExpiresAt:     &expired,
	}
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "custom-refresh" {
			t.Fatalf("refresh form = %v", r.Form)
		}
		if r.Form.Get("client_id") != "custom-client" {
			t.Fatalf("client_id = %q", r.Form.Get("client_id"))
		}
		if r.Form.Get("client_secret") != "" {
			t.Fatalf("client_secret should be omitted for public dynamic client: %v", r.Form)
		}
		if r.Form.Get("resource") != "https://mcp.example.com/mcp" {
			t.Fatalf("resource = %q", r.Form.Get("resource"))
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"access_token": "fresh-custom-access",
			"expires_in":   3600,
			"token_type":   "Bearer",
		})
	}))
	defer tokenServer.Close()

	srv := &Server{store: ms, secretKey: secretKey, allowInsecureWorkspaceIntegrationEndpoints: true}
	token, err := srv.workspaceIntegrationOAuthAccessTokenForAuth(context.Background(), store.WorkspaceIntegration{
		ID:   "integration-custom-oauth",
		Slug: "custom-oauth",
		Kind: "custom-mcp",
	}, &workspaceIntegrationHTTPAuth{
		Type:     "oauth",
		TokenURL: tokenServer.URL,
		ClientID: "custom-client",
		Resource: "https://mcp.example.com/mcp",
	}, false)
	if err != nil {
		t.Fatalf("refresh custom MCP oauth: %v", err)
	}
	if token != "fresh-custom-access" {
		t.Fatalf("token = %q", token)
	}
	if ms.credential == nil || ms.credential.RefreshEnc == nil {
		t.Fatal("expected refreshed credential with preserved refresh token")
	}
	decryptedRefresh, err := crypto.Decrypt(*ms.credential.RefreshEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt refresh: %v", err)
	}
	if decryptedRefresh != "custom-refresh" {
		t.Fatalf("refresh token = %q", decryptedRefresh)
	}
}

func TestGoogleWorkspaceOAuthCallbackStoresEncryptedTokens(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	workspaceID := "workspace-1"
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, WorkspaceID: &workspaceID, Kind: store.MachineKindOpenClaw, Name: "Builder", Status: "running"}
	secretKey := "12345678901234567890123456789012"
	srv := &Server{
		store:             ms,
		secretKey:         secretKey,
		oauthClientID:     "google-client",
		oauthClientSecret: "google-secret",
		backendURL:        "https://api.example.test",
		frontendBaseURL:   "https://app.example.test",
		allowInsecureWorkspaceIntegrationEndpoints: true,
	}
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			if r.Form.Get("code") != "auth-code" || r.Form.Get("client_id") != "google-client" || r.Form.Get("client_secret") != "google-secret" {
				t.Fatalf("unexpected token form: %v", r.Form)
			}
			if r.Form.Get("redirect_uri") != "https://api.example.test/api/workspace-integrations/oauth/callback" {
				t.Fatalf("redirect_uri = %q", r.Form.Get("redirect_uri"))
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"access_token":  "google-access",
				"refresh_token": "google-refresh",
				"expires_in":    3600,
				"token_type":    "Bearer",
				"scope":         strings.Join(googleWorkspaceScopes, " "),
			})
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer google-access" {
				t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"email":          "user@example.com",
				"email_verified": true,
				"name":           "Workspace User",
			})
		case "/gmail/v1/users/me/profile":
			if r.Header.Get("Authorization") != "Bearer google-access" {
				t.Fatalf("gmail authorization = %q", r.Header.Get("Authorization"))
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"emailAddress": "user@example.com", "messagesTotal": 1})
		case "/drive/v3/files":
			if r.Header.Get("Authorization") != "Bearer google-access" {
				t.Fatalf("drive authorization = %q", r.Header.Get("Authorization"))
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"files": []map[string]interface{}{}})
		case "/calendar/v3/calendars/primary/events":
			if r.Header.Get("Authorization") != "Bearer google-access" {
				t.Fatalf("calendar authorization = %q", r.Header.Get("Authorization"))
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"items": []map[string]interface{}{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer tokenServer.Close()
	oldTokenURL := googleOAuthTokenURL
	oldUserInfoURL := googleUserInfoURL
	googleOAuthTokenURL = tokenServer.URL + "/token"
	googleUserInfoURL = tokenServer.URL + "/userinfo"
	restoreGoogleAPIs := overrideGoogleWorkspaceAPIBases(t, tokenServer.URL)
	defer func() {
		restoreGoogleAPIs()
		googleOAuthTokenURL = oldTokenURL
		googleUserInfoURL = oldUserInfoURL
	}()

	state, err := srv.signWorkspaceIntegrationOAuthState(workspaceIntegrationOAuthState{
		Provider:    googleWorkspaceIntegrationSlug,
		AccountID:   1,
		WorkspaceID: "workspace-1",
		UserID:      7,
		ReturnPath:  "/workspaces/workspace-1/integrations",
		ExpiresAt:   time.Now().Add(10 * time.Minute).Unix(),
		Nonce:       "nonce",
	})
	if err != nil {
		t.Fatalf("sign state: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/workspace-integrations/oauth/callback?code=auth-code&state="+url.QueryEscape(state), nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{UserID: 7, Email: "owner@example.test"}))
	w := httptest.NewRecorder()

	srv.handleWorkspaceIntegrationOAuthCallback(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	if location != "https://app.example.test/workspaces/workspace-1/integrations?google-workspace=connected" {
		t.Fatalf("redirect location = %q", location)
	}
	if ms.upserted == nil || ms.upserted.Slug != "google-user-example-com" || ms.upserted.Kind != "google_workspace" {
		t.Fatalf("upserted integration = %+v", ms.upserted)
	}
	if ms.upserted.DisplayName != "Google Workspace" {
		t.Fatalf("display name = %q", ms.upserted.DisplayName)
	}
	if ms.credential == nil {
		t.Fatal("expected credential to be stored")
	}
	access, err := crypto.Decrypt(ms.credential.SecretEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt access token: %v", err)
	}
	if access != "google-access" {
		t.Fatalf("access token = %q", access)
	}
	if ms.credential.RefreshEnc == nil {
		t.Fatal("expected refresh token")
	}
	refresh, err := crypto.Decrypt(*ms.credential.RefreshEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt refresh token: %v", err)
	}
	if refresh != "google-refresh" {
		t.Fatalf("refresh token = %q", refresh)
	}
	if ms.credential.ExpiresAt == nil {
		t.Fatal("expected expiry")
	}
	var cfg googleWorkspaceIntegrationConfig
	if err := json.Unmarshal(ms.upserted.Config, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.Email != "user@example.com" {
		t.Fatalf("config email = %q", cfg.Email)
	}
	if cfg.PermissionLevels["gmail"] != "read" || cfg.PermissionLevels["calendar"] != "read" || cfg.PermissionLevels["docs"] != "off" {
		t.Fatalf("permission levels = %+v", cfg.PermissionLevels)
	}
	if !containsString(cfg.Scopes, "https://www.googleapis.com/auth/gmail.readonly") {
		t.Fatalf("saved scopes missing gmail readonly: %+v", cfg.Scopes)
	}
	for _, service := range []string{"gmail", "drive", "calendar"} {
		if cfg.ServiceStatus[service].Status != "connected" {
			t.Fatalf("%s service status = %+v", service, cfg.ServiceStatus[service])
		}
	}
	plugins := ms.plugins["m-1"]
	if len(plugins) != 1 || plugins[0].PluginID != workspaceIntegrationPluginID || !plugins[0].Enabled {
		t.Fatalf("plugins = %+v, want OAuth callback to assign workspace runtime", plugins)
	}
	// Regression: connecting an OAuth integration is additive and must NOT revoke tokens.
	if len(ms.revokedWorkspaceTokenWorkspaces) != 0 {
		t.Fatalf("oauth connect must not revoke workspace tokens, got %v", ms.revokedWorkspaceTokenWorkspaces)
	}
}

func TestGoogleWorkspaceOAuthSaveAddsMultipleAccountConnections(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	secretKey := "12345678901234567890123456789012"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gmail/v1/users/me/profile" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"emailAddress": "user@example.com"})
	}))
	defer api.Close()
	restoreGoogleAPIs := overrideGoogleWorkspaceAPIBases(t, api.URL)
	defer restoreGoogleAPIs()

	srv := &Server{
		store:     ms,
		secretKey: secretKey,
		allowInsecureWorkspaceIntegrationEndpoints: true,
	}

	if err := srv.saveWorkspaceOAuthIntegration(context.Background(), googleWorkspaceOAuthProvider(), "workspace-1", "user@example.com", map[string]string{
		"gmail":    "read",
		"drive":    "off",
		"calendar": "off",
		"docs":     "off",
	}, workspaceIntegrationOAuthTokenResponse{
		AccessToken: "google-access-1",
		ExpiresIn:   3600,
		TokenType:   "Bearer",
		Scope:       "openid email profile https://www.googleapis.com/auth/gmail.readonly",
	}); err != nil {
		t.Fatalf("save first google workspace integration: %v", err)
	}
	if ms.upserted == nil || ms.upserted.Slug != "google-user-example-com" {
		t.Fatalf("first upserted = %+v", ms.upserted)
	}
	firstCredential := *ms.credential

	if err := srv.saveWorkspaceOAuthIntegration(context.Background(), googleWorkspaceOAuthProvider(), "workspace-1", "admin@company.test", map[string]string{
		"gmail":    "read",
		"drive":    "off",
		"calendar": "off",
		"docs":     "off",
	}, workspaceIntegrationOAuthTokenResponse{
		AccessToken: "google-access-2",
		ExpiresIn:   3600,
		TokenType:   "Bearer",
		Scope:       "openid email profile https://www.googleapis.com/auth/gmail.readonly",
	}); err != nil {
		t.Fatalf("save second google workspace integration: %v", err)
	}
	if ms.upserted == nil || ms.upserted.Slug != "google-admin-company-test" {
		t.Fatalf("second upserted = %+v", ms.upserted)
	}
	if len(ms.integrations) != 2 {
		t.Fatalf("integrations = %+v, want two google account connections", ms.integrations)
	}
	targets := map[string]string{}
	for _, integration := range ms.integrations {
		cfg, err := parseGoogleWorkspaceIntegrationConfig(integration)
		if err != nil {
			t.Fatalf("parse google config for %s: %v", integration.Slug, err)
		}
		targets[integration.Slug] = cfg.Email
	}
	if targets["google-user-example-com"] != "user@example.com" || targets["google-admin-company-test"] != "admin@company.test" {
		t.Fatalf("google account targets = %+v", targets)
	}
	if ms.credential == nil || ms.credential.SecretEnc == firstCredential.SecretEnc {
		t.Fatalf("expected second account credential to be distinct: first=%+v second=%+v", firstCredential, ms.credential)
	}
}

func TestGoogleWorkspaceIntegrationConnectionSlug(t *testing.T) {
	tests := map[string]string{
		"user@example.com":                 "google-user-example-com",
		"admin@company.test":               "google-admin-company-test",
		"workspace_user+calendar@test.dev": "google-workspace-user-calendar-test-dev",
		"":                                 "google-account",
	}
	for email, want := range tests {
		if got := googleWorkspaceIntegrationConnectionSlug(email); got != want {
			t.Fatalf("connection slug for %q = %q, want %q", email, got, want)
		}
	}
}

func overrideGoogleWorkspaceAPIBases(t *testing.T, baseURL string) func() {
	t.Helper()
	oldGmail := googleGmailAPIBaseURL
	oldDrive := googleDriveAPIBaseURL
	oldCalendar := googleCalendarAPIBaseURL
	googleGmailAPIBaseURL = baseURL + "/gmail/v1"
	googleDriveAPIBaseURL = baseURL + "/drive/v3"
	googleCalendarAPIBaseURL = baseURL + "/calendar/v3"
	return func() {
		googleGmailAPIBaseURL = oldGmail
		googleDriveAPIBaseURL = oldDrive
		googleCalendarAPIBaseURL = oldCalendar
	}
}

func TestGoogleWorkspaceOAuthSaveUsesGrantedScopesForManifest(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	secretKey := "12345678901234567890123456789012"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gmail/v1/users/me/profile" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"emailAddress": "user@example.com"})
	}))
	defer api.Close()
	restoreGoogleAPIs := overrideGoogleWorkspaceAPIBases(t, api.URL)
	defer restoreGoogleAPIs()

	srv := &Server{
		store:     ms,
		secretKey: secretKey,
		allowInsecureWorkspaceIntegrationEndpoints: true,
	}

	err := srv.saveWorkspaceOAuthIntegration(context.Background(), googleWorkspaceOAuthProvider(), "workspace-1", "user@example.com", map[string]string{
		"gmail":    "read_write",
		"drive":    "read",
		"calendar": "read",
		"docs":     "off",
	}, workspaceIntegrationOAuthTokenResponse{
		AccessToken: "google-access",
		ExpiresIn:   3600,
		TokenType:   "Bearer",
		Scope:       "openid email profile https://www.googleapis.com/auth/gmail.readonly",
	})
	if err != nil {
		t.Fatalf("save google workspace integration: %v", err)
	}
	if ms.upserted == nil {
		t.Fatal("expected integration to be saved")
	}

	var cfg googleWorkspaceIntegrationConfig
	if err := json.Unmarshal(ms.upserted.Config, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if !containsString(cfg.Scopes, "https://www.googleapis.com/auth/gmail.readonly") {
		t.Fatalf("saved scopes missing granted gmail readonly: %+v", cfg.Scopes)
	}
	for _, missing := range []string{
		"https://www.googleapis.com/auth/gmail.send",
		"https://www.googleapis.com/auth/drive.metadata.readonly",
		"https://www.googleapis.com/auth/calendar.events.readonly",
	} {
		if containsString(cfg.Scopes, missing) {
			t.Fatalf("saved scopes should reflect granted token scopes, found %q in %+v", missing, cfg.Scopes)
		}
	}
	if cfg.PermissionLevels["gmail"] != "read" || cfg.PermissionLevels["drive"] != "off" || cfg.PermissionLevels["calendar"] != "off" {
		t.Fatalf("effective permission levels = %+v", cfg.PermissionLevels)
	}
	if cfg.RequestedPermissionLevels["gmail"] != "read_write" || cfg.RequestedPermissionLevels["drive"] != "read" || cfg.RequestedPermissionLevels["calendar"] != "read" {
		t.Fatalf("requested permission levels = %+v", cfg.RequestedPermissionLevels)
	}
	if cfg.ServiceStatus["gmail"].Status != "connected" {
		t.Fatalf("gmail status = %+v", cfg.ServiceStatus["gmail"])
	}
	if cfg.ServiceStatus["drive"].Status != "missing_scope" || cfg.ServiceStatus["drive"].Action != "grant_scope_or_reconnect" {
		t.Fatalf("drive status = %+v", cfg.ServiceStatus["drive"])
	}
	if cfg.ServiceStatus["calendar"].Status != "missing_scope" {
		t.Fatalf("calendar status = %+v", cfg.ServiceStatus["calendar"])
	}

	var tools []workspaceIntegrationManifestTool
	if err := json.Unmarshal(ms.upserted.ToolManifest, &tools); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"gmail_profile", "gmail_list_messages", "gmail_get_message"} {
		if !names[want] {
			t.Fatalf("manifest missing granted Gmail read tool %q: %+v", want, names)
		}
	}
	for _, absent := range []string{"gmail_send_message", "drive_list_files", "calendar_list_events"} {
		if names[absent] {
			t.Fatalf("manifest exposed tool without granted scope %q: %+v", absent, names)
		}
	}
}

func TestGoogleWorkspaceOAuthSavePreflightAdminPolicyHidesServiceTools(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	secretKey := "12345678901234567890123456789012"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gmail/v1/users/me/profile" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"status":"PERMISSION_DENIED","message":"Access blocked by your Google Workspace admin.","errors":[{"reason":"domainPolicy"}]}}`))
	}))
	defer api.Close()
	restoreGoogleAPIs := overrideGoogleWorkspaceAPIBases(t, api.URL)
	defer restoreGoogleAPIs()

	srv := &Server{
		store:     ms,
		secretKey: secretKey,
		allowInsecureWorkspaceIntegrationEndpoints: true,
	}

	err := srv.saveWorkspaceOAuthIntegration(context.Background(), googleWorkspaceOAuthProvider(), "workspace-1", "user@example.com", map[string]string{
		"gmail":    "read",
		"drive":    "off",
		"calendar": "off",
		"docs":     "off",
	}, workspaceIntegrationOAuthTokenResponse{
		AccessToken: "google-access",
		ExpiresIn:   3600,
		TokenType:   "Bearer",
		Scope:       "openid email profile https://www.googleapis.com/auth/gmail.readonly",
	})
	if err != nil {
		t.Fatalf("save google workspace integration: %v", err)
	}
	var cfg googleWorkspaceIntegrationConfig
	if err := json.Unmarshal(ms.upserted.Config, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.ServiceStatus["gmail"].Status != "admin_policy_blocked" || cfg.ServiceStatus["gmail"].Action != "ask_google_workspace_admin" {
		t.Fatalf("gmail status = %+v", cfg.ServiceStatus["gmail"])
	}
	var tools []workspaceIntegrationManifestTool
	if err := json.Unmarshal(ms.upserted.ToolManifest, &tools); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name, "gmail_") {
			t.Fatalf("manifest exposed admin-policy-blocked Gmail tool %q: %+v", tool.Name, tools)
		}
	}
}

func TestGoogleWorkspaceOAuthSaveTransientPreflightKeepsGrantedTools(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantStatus string
	}{
		{
			name:       "rate limited",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Rate limit exceeded"}}`,
			wantStatus: "rate_limited",
		},
		{
			name:       "upstream unavailable",
			statusCode: http.StatusServiceUnavailable,
			body:       `{"error":{"status":"UNAVAILABLE","message":"Try later"}}`,
			wantStatus: "upstream_unavailable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newMockWorkspaceIntegrationManagementStore()
			secretKey := "12345678901234567890123456789012"
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/gmail/v1/users/me/profile" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer api.Close()
			restoreGoogleAPIs := overrideGoogleWorkspaceAPIBases(t, api.URL)
			defer restoreGoogleAPIs()

			srv := &Server{
				store:     ms,
				secretKey: secretKey,
				allowInsecureWorkspaceIntegrationEndpoints: true,
			}

			err := srv.saveWorkspaceOAuthIntegration(context.Background(), googleWorkspaceOAuthProvider(), "workspace-1", "user@example.com", map[string]string{
				"gmail":    "read",
				"drive":    "off",
				"calendar": "off",
				"docs":     "off",
			}, workspaceIntegrationOAuthTokenResponse{
				AccessToken: "google-access",
				ExpiresIn:   3600,
				TokenType:   "Bearer",
				Scope:       "openid email profile https://www.googleapis.com/auth/gmail.readonly",
			})
			if err != nil {
				t.Fatalf("save google workspace integration: %v", err)
			}
			var cfg googleWorkspaceIntegrationConfig
			if err := json.Unmarshal(ms.upserted.Config, &cfg); err != nil {
				t.Fatalf("parse config: %v", err)
			}
			if cfg.ServiceStatus["gmail"].Status != tt.wantStatus {
				t.Fatalf("gmail status = %+v, want %q", cfg.ServiceStatus["gmail"], tt.wantStatus)
			}
			var tools []workspaceIntegrationManifestTool
			if err := json.Unmarshal(ms.upserted.ToolManifest, &tools); err != nil {
				t.Fatalf("parse manifest: %v", err)
			}
			names := make(map[string]bool, len(tools))
			for _, tool := range tools {
				names[tool.Name] = true
			}
			for _, want := range []string{"gmail_profile", "gmail_list_messages", "gmail_get_message"} {
				if !names[want] {
					t.Fatalf("manifest hid granted Gmail tool %q after transient status %q: %+v", want, tt.wantStatus, names)
				}
			}
		})
	}
}

func TestGoogleWorkspacePreflightServiceStatusClassifiesFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantStatus string
		wantAction string
	}{
		{
			name:       "token revoked",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":{"status":"UNAUTHENTICATED","message":"Invalid Credentials"}}`,
			wantStatus: "token_revoked",
			wantAction: "reconnect_integration",
		},
		{
			name:       "rate limited",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Rate limit exceeded"}}`,
			wantStatus: "rate_limited",
			wantAction: "retry_later",
		},
		{
			name:       "upstream unavailable",
			statusCode: http.StatusServiceUnavailable,
			body:       `{"error":{"status":"UNAVAILABLE","message":"Try later"}}`,
			wantStatus: "upstream_unavailable",
			wantAction: "retry_later",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/gmail/v1/users/me/profile" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer api.Close()
			restoreGoogleAPIs := overrideGoogleWorkspaceAPIBases(t, api.URL)
			defer restoreGoogleAPIs()

			srv := &Server{allowInsecureWorkspaceIntegrationEndpoints: true}
			statuses := srv.googleWorkspacePreflightServiceStatus(context.Background(),
				map[string]string{"gmail": "read", "drive": "off", "calendar": "off", "docs": "off"},
				map[string]string{"gmail": "read", "drive": "off", "calendar": "off", "docs": "off"},
				[]string{"https://www.googleapis.com/auth/gmail.readonly"},
				"google-access",
			)
			if statuses["gmail"].Status != tt.wantStatus || statuses["gmail"].Action != tt.wantAction {
				t.Fatalf("gmail status = %+v, want status %q action %q", statuses["gmail"], tt.wantStatus, tt.wantAction)
			}
		})
	}
}

func TestGoogleWorkspaceOAuthCallbackRejectsMismatchedStateUser(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{
		store:             ms,
		secretKey:         "12345678901234567890123456789012",
		oauthClientID:     "google-client",
		oauthClientSecret: "google-secret",
		frontendBaseURL:   "https://app.example.test",
	}
	state, err := srv.signWorkspaceIntegrationOAuthState(workspaceIntegrationOAuthState{
		Provider:    googleWorkspaceIntegrationSlug,
		AccountID:   1,
		WorkspaceID: "workspace-1",
		UserID:      7,
		ReturnPath:  "/workspaces/workspace-1/integrations",
		ExpiresAt:   time.Now().Add(10 * time.Minute).Unix(),
		Nonce:       "nonce",
	})
	if err != nil {
		t.Fatalf("sign state: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/workspace-integrations/oauth/callback?code=auth-code&state="+url.QueryEscape(state), nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{UserID: 8, Email: "other@example.test"}))
	w := httptest.NewRecorder()

	srv.handleWorkspaceIntegrationOAuthCallback(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", w.Code, w.Body.String())
	}
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect location: %v", err)
	}
	if location.Query().Get("google-workspace") != "error" || !strings.Contains(location.Query().Get("message"), "started this Google Workspace OAuth flow") {
		t.Fatalf("redirect location = %q", location.String())
	}
	if ms.upserted != nil || ms.credential != nil {
		t.Fatalf("callback wrote integration or credential on user mismatch: integration=%+v credential=%+v", ms.upserted, ms.credential)
	}
}

func TestGoogleWorkspaceOAuthCallbackRejectsMemberRole(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	ms.memberRole = "member"
	srv := &Server{
		store:             ms,
		secretKey:         "12345678901234567890123456789012",
		oauthClientID:     "google-client",
		oauthClientSecret: "google-secret",
		frontendBaseURL:   "https://app.example.test",
	}
	state, err := srv.signWorkspaceIntegrationOAuthState(workspaceIntegrationOAuthState{
		Provider:    googleWorkspaceIntegrationSlug,
		AccountID:   1,
		WorkspaceID: "workspace-1",
		UserID:      7,
		ReturnPath:  "/workspaces/workspace-1/integrations",
		ExpiresAt:   time.Now().Add(10 * time.Minute).Unix(),
		Nonce:       "nonce",
	})
	if err != nil {
		t.Fatalf("sign state: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/workspace-integrations/oauth/callback?code=auth-code&state="+url.QueryEscape(state), nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{UserID: 7, Email: "member@example.test"}))
	w := httptest.NewRecorder()

	srv.handleWorkspaceIntegrationOAuthCallback(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", w.Code, w.Body.String())
	}
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect location: %v", err)
	}
	if location.Query().Get("google-workspace") != "error" || !strings.Contains(location.Query().Get("message"), "Owner or admin access is required") {
		t.Fatalf("redirect location = %q", location.String())
	}
	if ms.upserted != nil || ms.credential != nil {
		t.Fatalf("callback wrote integration or credential for member role: integration=%+v credential=%+v", ms.upserted, ms.credential)
	}
}

func TestGoogleWorkspaceOAuthRedirectRequiresFrontendURL(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest(http.MethodGet, "https://api.openclawmachines.com/api/workspace-integrations/oauth/callback", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()

	srv.redirectWorkspaceIntegrationOAuthResult(w, req, googleWorkspaceIntegrationSlug, "/workspaces/workspace-1/integrations", "connected", "")

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	if location := w.Header().Get("Location"); location != "" {
		t.Fatalf("unexpected redirect location = %q", location)
	}
	if !strings.Contains(w.Body.String(), "FRONTEND_URL is required") {
		t.Fatalf("response = %s, want clear FRONTEND_URL error", w.Body.String())
	}
}

func TestSaveWorkspaceOAuthIntegrationAbortsWhenExistingRefreshTokenLookupFails(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	ms.credentialErr = errors.New("database temporarily unavailable")
	secretKey := "12345678901234567890123456789012"
	srv := &Server{
		store:     ms,
		secretKey: secretKey,
	}

	err := srv.saveWorkspaceOAuthIntegration(context.Background(), googleWorkspaceOAuthProvider(), "workspace-1", "user@example.com", nil, workspaceIntegrationOAuthTokenResponse{
		AccessToken: "google-access",
		ExpiresIn:   3600,
		TokenType:   "Bearer",
		Scope:       "openid email profile",
	})

	if err == nil {
		t.Fatal("expected save to fail when existing refresh token lookup fails")
	}
	if !strings.Contains(err.Error(), "load existing workspace oauth credential") {
		t.Fatalf("error = %v", err)
	}
	if ms.credential != nil {
		t.Fatalf("credential was overwritten despite lookup error: %+v", ms.credential)
	}
}

func TestEnableWorkspaceIntegrationRuntimeEnablesOCMPlugin(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	workspaceID := "workspace-1"
	ms.machines["m-1"] = &store.Machine{
		ID:          "m-1",
		AccountID:   1,
		WorkspaceID: &workspaceID,
		Name:        "Builder",
		Status:      "stopped",
	}
	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementRequest(http.MethodPost, "/api/accounts/1/workspace-integrations/machines/m-1/enable", 1, "m-1")

	srv.handleEnableWorkspaceIntegrationRuntime(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	plugins := ms.plugins["m-1"]
	if len(plugins) != 1 {
		t.Fatalf("plugins = %+v, want one plugin", plugins)
	}
	if plugins[0].PluginID != workspaceIntegrationPluginID || !plugins[0].Enabled {
		t.Fatalf("plugin = %+v, want enabled %s", plugins[0], workspaceIntegrationPluginID)
	}
}

func TestEnableWorkspaceIntegrationRuntimeRejectsMachineWithoutWorkspace(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 1, Name: "Legacy", Status: "stopped"}
	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementRequest(http.MethodPost, "/api/accounts/1/workspace-integrations/machines/m-1/enable", 1, "m-1")

	srv.handleEnableWorkspaceIntegrationRuntime(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
	if len(ms.plugins["m-1"]) != 0 {
		t.Fatalf("plugins = %+v, want no plugin assignment", ms.plugins["m-1"])
	}
}

func TestEnableWorkspaceIntegrationRuntimeRejectsWrongAccountMachine(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	ms.machines["m-1"] = &store.Machine{ID: "m-1", AccountID: 2, Name: "Other", Status: "stopped"}
	srv := &Server{store: ms}
	w := httptest.NewRecorder()
	r := workspaceIntegrationManagementRequest(http.MethodPost, "/api/accounts/1/workspace-integrations/machines/m-1/enable", 1, "m-1")

	srv.handleEnableWorkspaceIntegrationRuntime(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// The revocation primitive still works when called deliberately; it is just no
// longer auto-fired by routine integration mutations (see the handler tests that
// assert no revocation on create/connect/revoke/policy/disable).
func TestRevokeWorkspaceIntegrationTokensForWorkspaceHelper(t *testing.T) {
	ms := newMockWorkspaceIntegrationManagementStore()
	srv := &Server{store: ms}
	if err := srv.revokeWorkspaceIntegrationTokensForWorkspace(context.Background(), "workspace-1"); err != nil {
		t.Fatalf("revoke helper: %v", err)
	}
	if got := strings.Join(ms.revokedWorkspaceTokenWorkspaces, ","); got != "workspace-1" {
		t.Fatalf("revoked workspaces = %q, want workspace-1", got)
	}
}
