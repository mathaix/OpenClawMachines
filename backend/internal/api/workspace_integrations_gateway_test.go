package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

const workspaceIntegrationTestSecret = "workspace-integration-test-secret"

type workspaceIntegrationGatewayStore struct {
	store.Store

	mu                    sync.Mutex
	machines              map[string]*store.Machine
	workspaces            map[string]*store.Workspace
	plugins               map[string][]store.MachinePlugin
	integrations          map[string][]store.WorkspaceIntegration
	connectorProjections  map[string][]store.WorkspaceIntegrationConnectorProjection
	guidanceOverlays      map[string][]store.WorkspaceIntegrationGuidanceOverlay
	credential            *store.WorkspaceIntegrationCredential
	credentials           map[string]store.WorkspaceIntegrationCredential
	connectionCredentials map[string]store.WorkspaceIntegrationConnectionCredential
	callEvents            []store.WorkspaceIntegrationCallEvent
	callEventContextErrs  []error
	callEventBlock        chan struct{}
	callEventStarted      chan struct{}
	workspaceMachineIDs   []string
	listMachineIDs        []string
}

type workspaceIntegrationGatewayGuidanceCandidate struct {
	toolID          string
	toolAddress     *string
	integrationSlug string
	toolName        string
	failureClass    string
	count           int
	repoBareName    bool
	dateParseFailed bool
}

func stringPtr(value string) *string {
	return &value
}

func (m *workspaceIntegrationGatewayStore) GetMachine(_ context.Context, machineID string) (*store.Machine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if machine, ok := m.machines[machineID]; ok {
		next := *machine
		return &next, nil
	}
	return nil, fmt.Errorf("machine not found")
}

func (m *workspaceIntegrationGatewayStore) GetWorkspaceForMachine(_ context.Context, machineID string) (*store.Workspace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workspaceMachineIDs = append(m.workspaceMachineIDs, machineID)
	if workspace, ok := m.workspaces[machineID]; ok {
		return workspace, nil
	}
	return nil, fmt.Errorf("workspace not found")
}

func (m *workspaceIntegrationGatewayStore) ListMachinePlugins(_ context.Context, machineID string) ([]store.MachinePlugin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.MachinePlugin(nil), m.plugins[machineID]...), nil
}

func (m *workspaceIntegrationGatewayStore) ListEnabledWorkspaceIntegrationsForMachine(_ context.Context, machineID string) ([]store.WorkspaceIntegration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listMachineIDs = append(m.listMachineIDs, machineID)
	return m.integrations[machineID], nil
}

func (m *workspaceIntegrationGatewayStore) ListWorkspaceIntegrationConnectorProjections(_ context.Context, workspaceID string) ([]store.WorkspaceIntegrationConnectorProjection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	projections := m.connectorProjections[workspaceID]
	out := make([]store.WorkspaceIntegrationConnectorProjection, len(projections))
	copy(out, projections)
	return out, nil
}

func (m *workspaceIntegrationGatewayStore) GetWorkspaceIntegrationCredential(_ context.Context, integrationID string) (*store.WorkspaceIntegrationCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.credentials != nil {
		if credential, ok := m.credentials[integrationID]; ok {
			next := credential
			return &next, nil
		}
	}
	if m.credential != nil && m.credential.IntegrationID == integrationID {
		credential := *m.credential
		return &credential, nil
	}
	return nil, pgx.ErrNoRows
}

func (m *workspaceIntegrationGatewayStore) SetWorkspaceIntegrationCredential(_ context.Context, credential *store.WorkspaceIntegrationCredential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := *credential
	m.credential = &next
	if m.credentials == nil {
		m.credentials = map[string]store.WorkspaceIntegrationCredential{}
	}
	m.credentials[next.IntegrationID] = next
	return nil
}

func (m *workspaceIntegrationGatewayStore) GetWorkspaceIntegrationConnectionCredential(_ context.Context, connectionID string) (*store.WorkspaceIntegrationConnectionCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connectionCredentials != nil {
		if credential, ok := m.connectionCredentials[connectionID]; ok {
			next := credential
			return &next, nil
		}
	}
	return nil, pgx.ErrNoRows
}

func (m *workspaceIntegrationGatewayStore) SetWorkspaceIntegrationConnectionCredential(_ context.Context, credential *store.WorkspaceIntegrationConnectionCredential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if credential == nil {
		return errors.New("workspace integration connection credential is nil")
	}
	next := *credential
	if m.connectionCredentials == nil {
		m.connectionCredentials = map[string]store.WorkspaceIntegrationConnectionCredential{}
	}
	m.connectionCredentials[next.ConnectionID] = next
	return nil
}

func (m *workspaceIntegrationGatewayStore) RecordWorkspaceIntegrationCallEvent(ctx context.Context, event *store.WorkspaceIntegrationCallEvent) error {
	m.mu.Lock()
	m.callEventContextErrs = append(m.callEventContextErrs, ctx.Err())
	started := m.callEventStarted
	block := m.callEventBlock
	m.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := *event
	if next.ID == "" {
		next.ID = fmt.Sprintf("call-event-%d", len(m.callEvents)+1)
	}
	if next.AccountID == 0 {
		for _, workspace := range m.workspaces {
			if workspace.ID == next.WorkspaceID {
				next.AccountID = workspace.AccountID
				break
			}
		}
	}
	if next.CreatedAt.IsZero() {
		next.CreatedAt = time.Now().UTC()
	}
	m.callEvents = append(m.callEvents, next)
	event.ID = next.ID
	event.CreatedAt = next.CreatedAt
	return nil
}

func waitForWorkspaceIntegrationCallEvents(t *testing.T, m *workspaceIntegrationGatewayStore, want int) []store.WorkspaceIntegrationCallEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.Lock()
		events := append([]store.WorkspaceIntegrationCallEvent(nil), m.callEvents...)
		m.mu.Unlock()
		if len(events) >= want {
			return events
		}
		if time.Now().After(deadline) {
			t.Fatalf("call events = %+v, want at least %d", events, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func forceWorkspaceIntegrationSuccessCallEventSampling(t *testing.T) {
	t.Helper()
	prevRate := workspaceIntegrationSuccessCallEventSampleRate
	prevSample := workspaceIntegrationCallEventSample
	workspaceIntegrationSuccessCallEventSampleRate = 1
	workspaceIntegrationCallEventSample = func() float64 { return 0 }
	t.Cleanup(func() {
		workspaceIntegrationSuccessCallEventSampleRate = prevRate
		workspaceIntegrationCallEventSample = prevSample
	})
}

func (m *workspaceIntegrationGatewayStore) ListWorkspaceIntegrationToolHealth(_ context.Context, accountID int, workspaceID string, query store.WorkspaceIntegrationHealthQuery) ([]store.WorkspaceIntegrationToolHealth, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	type aggregate struct {
		health       store.WorkspaceIntegrationToolHealth
		failures     map[string]int64
		latencies    []int
		retryCount   int64
		retrySamples int64
	}
	aggregates := map[string]*aggregate{}
	for _, event := range m.callEvents {
		if accountID != 0 && event.AccountID != accountID {
			continue
		}
		if workspaceID != "" && event.WorkspaceID != workspaceID {
			continue
		}
		if !query.Since.IsZero() && event.CreatedAt.Before(query.Since) {
			continue
		}
		if event.DetailLevel != "" && event.DetailLevel != "telemetry" {
			continue
		}
		toolID := event.ToolID
		if toolID == "" && event.IntegrationSlug != "" && event.ToolName != "" {
			toolID = event.IntegrationSlug + "." + event.ToolName
		}
		if toolID == "" {
			continue
		}
		aggregateKey := toolID
		if event.ToolAddress != nil && strings.TrimSpace(*event.ToolAddress) != "" {
			aggregateKey = *event.ToolAddress
		}
		agg := aggregates[aggregateKey]
		if agg == nil {
			agg = &aggregate{
				health: store.WorkspaceIntegrationToolHealth{
					ToolID:          toolID,
					ToolAddress:     event.ToolAddress,
					IntegrationSlug: event.IntegrationSlug,
					ToolName:        event.ToolName,
					Transport:       event.Transport,
					Access:          event.Access,
				},
				failures: map[string]int64{},
			}
			aggregates[aggregateKey] = agg
		}
		weight := int64(1)
		if event.Status == "success" && event.SampleRate > 0 && event.SampleRate < 1 {
			weight = int64(math.Round(1 / event.SampleRate))
			if weight < 1 {
				weight = 1
			}
		}
		agg.health.TotalCalls += weight
		agg.latencies = append(agg.latencies, event.LatencyMS)
		agg.retryCount += int64(event.RetryCount)
		agg.retrySamples++
		if event.Status == "error" {
			agg.health.ErrorCalls++
			if event.FailureClass != nil && strings.TrimSpace(*event.FailureClass) != "" {
				agg.failures[*event.FailureClass]++
			}
		} else {
			agg.health.SuccessCalls += weight
		}
	}
	out := make([]store.WorkspaceIntegrationToolHealth, 0, len(aggregates))
	for _, agg := range aggregates {
		if agg.health.TotalCalls > 0 {
			agg.health.SuccessRate = float64(agg.health.SuccessCalls) / float64(agg.health.TotalCalls)
		}
		if agg.retrySamples > 0 {
			agg.health.AvgRetryCount = float64(agg.retryCount) / float64(agg.retrySamples)
		}
		sort.Ints(agg.latencies)
		if len(agg.latencies) > 0 {
			agg.health.P50LatencyMS = float64(agg.latencies[(len(agg.latencies)-1)/2])
			agg.health.P95LatencyMS = float64(agg.latencies[(len(agg.latencies)*95-1)/100])
		}
		for class, count := range agg.failures {
			agg.health.TopFailureClasses = append(agg.health.TopFailureClasses, store.WorkspaceIntegrationFailureCount{
				Class: class,
				Count: count,
			})
		}
		sort.Slice(agg.health.TopFailureClasses, func(i, j int) bool {
			left := agg.health.TopFailureClasses[i]
			right := agg.health.TopFailureClasses[j]
			if left.Count != right.Count {
				return left.Count > right.Count
			}
			return left.Class < right.Class
		})
		out = append(out, agg.health)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ErrorCalls != out[j].ErrorCalls {
			return out[i].ErrorCalls > out[j].ErrorCalls
		}
		if out[i].TotalCalls != out[j].TotalCalls {
			return out[i].TotalCalls > out[j].TotalCalls
		}
		return out[i].ToolID < out[j].ToolID
	})
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

func (m *workspaceIntegrationGatewayStore) ListWorkspaceIntegrationGuidanceOverlays(_ context.Context, _ int, workspaceID, status string) ([]store.WorkspaceIntegrationGuidanceOverlay, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := m.guidanceOverlays[workspaceID]
	out := make([]store.WorkspaceIntegrationGuidanceOverlay, 0, len(rows))
	for _, row := range rows {
		if status == "" || row.Status == status {
			out = append(out, row)
		}
	}
	return out, nil
}

func (m *workspaceIntegrationGatewayStore) CreateWorkspaceIntegrationGuidanceDraftsFromTelemetry(_ context.Context, accountID int, workspaceID string, since time.Time, limit int, createdBy *int) ([]store.WorkspaceIntegrationGuidanceOverlay, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	candidates := map[string]*workspaceIntegrationGatewayGuidanceCandidate{}
	for _, event := range m.callEvents {
		if accountID != 0 && event.AccountID != accountID {
			continue
		}
		if workspaceID != "" && event.WorkspaceID != workspaceID {
			continue
		}
		if !since.IsZero() && event.CreatedAt.Before(since) {
			continue
		}
		if event.Status != "error" || event.FailureClass == nil || strings.TrimSpace(*event.FailureClass) == "" {
			continue
		}
		toolID := event.ToolID
		if toolID == "" && event.IntegrationSlug != "" && event.ToolName != "" {
			toolID = event.IntegrationSlug + "." + event.ToolName
		}
		aggregateKey := toolID
		if event.ToolAddress != nil && strings.TrimSpace(*event.ToolAddress) != "" {
			aggregateKey = *event.ToolAddress
		}
		key := aggregateKey + "\x00" + *event.FailureClass
		next := candidates[key]
		if next == nil {
			next = &workspaceIntegrationGatewayGuidanceCandidate{
				toolID:          toolID,
				toolAddress:     event.ToolAddress,
				integrationSlug: event.IntegrationSlug,
				toolName:        event.ToolName,
				failureClass:    *event.FailureClass,
			}
			candidates[key] = next
		}
		next.count++
		if workspaceIntegrationArgShapeHas(event.ArgShape, "repo_format", "bare_name") {
			next.repoBareName = true
		}
		if workspaceIntegrationArgShapeHas(event.ArgShape, "date_parse", "failed") {
			next.dateParseFailed = true
		}
	}
	ordered := make([]*workspaceIntegrationGatewayGuidanceCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		ordered = append(ordered, candidate)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].count != ordered[j].count {
			return ordered[i].count > ordered[j].count
		}
		if ordered[i].toolID != ordered[j].toolID {
			return ordered[i].toolID < ordered[j].toolID
		}
		return ordered[i].failureClass < ordered[j].failureClass
	})
	if limit <= 0 || limit > 25 {
		limit = 10
	}
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	created := make([]store.WorkspaceIntegrationGuidanceOverlay, 0, len(ordered))
	for _, candidate := range ordered {
		pattern, _ := json.Marshal(map[string]interface{}{
			"failure_class":         candidate.failureClass,
			"failure_count":         candidate.count,
			"repo_format_bare_name": candidate.repoBareName,
			"date_parse_failed":     candidate.dateParseFailed,
		})
		overlay := store.WorkspaceIntegrationGuidanceOverlay{
			ID:                 fmt.Sprintf("guidance-%d", len(m.guidanceOverlays[workspaceID])+1),
			AccountID:          accountID,
			WorkspaceID:        workspaceID,
			ToolID:             candidate.toolID,
			ToolAddress:        candidate.toolAddress,
			IntegrationSlug:    candidate.integrationSlug,
			ToolName:           candidate.toolName,
			Status:             "draft",
			Version:            workspaceIntegrationGatewayNextGuidanceVersion(m.guidanceOverlays[workspaceID], candidate.toolID, candidate.toolAddress),
			Guidance:           workspaceIntegrationGatewayDraftGuidance(candidate),
			SourceFailureClass: stringPtr(candidate.failureClass),
			SanitizedPattern:   pattern,
			CreatedBy:          createdBy,
			CreatedAt:          time.Now().UTC(),
			UpdatedAt:          time.Now().UTC(),
		}
		m.guidanceOverlays[workspaceID] = append(m.guidanceOverlays[workspaceID], overlay)
		created = append(created, overlay)
	}
	return created, nil
}

func (m *workspaceIntegrationGatewayStore) ApproveWorkspaceIntegrationGuidanceOverlay(_ context.Context, accountID int, workspaceID, overlayID string, approvedBy int) (*store.WorkspaceIntegrationGuidanceOverlay, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	for i := range m.guidanceOverlays[workspaceID] {
		overlay := &m.guidanceOverlays[workspaceID][i]
		if overlay.AccountID == accountID && overlay.ID == overlayID {
			overlay.Status = "approved"
			overlay.ApprovedBy = &approvedBy
			overlay.ApprovedAt = &now
			overlay.UpdatedAt = now
			next := *overlay
			return &next, nil
		}
	}
	return nil, pgx.ErrNoRows
}

func workspaceIntegrationArgShapeHas(raw json.RawMessage, key, want string) bool {
	var shape map[string]map[string]interface{}
	if err := json.Unmarshal(raw, &shape); err != nil {
		return false
	}
	for _, entry := range shape {
		if got, _ := entry[key].(string); got == want {
			return true
		}
	}
	return false
}

func workspaceIntegrationGatewayNextGuidanceVersion(rows []store.WorkspaceIntegrationGuidanceOverlay, toolID string, toolAddress *string) int {
	next := 1
	key := workspaceIntegrationGatewayGuidanceVersionKey(toolID, toolAddress)
	for _, row := range rows {
		if workspaceIntegrationGatewayGuidanceVersionKey(row.ToolID, row.ToolAddress) == key && row.Version >= next {
			next = row.Version + 1
		}
	}
	return next
}

func workspaceIntegrationGatewayGuidanceVersionKey(toolID string, toolAddress *string) string {
	if toolAddress != nil && strings.TrimSpace(*toolAddress) != "" {
		return strings.TrimSpace(*toolAddress)
	}
	return strings.TrimSpace(toolID)
}

func workspaceIntegrationGatewayDraftGuidance(candidate *workspaceIntegrationGatewayGuidanceCandidate) string {
	if candidate.repoBareName {
		return "Repository arguments should use owner/name format; bare repository names have failed for this tool."
	}
	if candidate.dateParseFailed {
		return "Date and time arguments should use RFC3339 timestamps or YYYY-MM-DD dates accepted by the tool schema."
	}
	switch candidate.failureClass {
	case "invalid_arguments":
		return "Check required arguments, enum values, and schema types before calling this tool."
	case "rate_limited":
		return "Narrow or batch requests where possible, then retry only after the returned retry_after delay."
	case "credential_not_configured":
		return "Reconnect this integration before retrying the tool."
	default:
		return "Review the tool schema and the recorded failure class before retrying this tool."
	}
}

func newWorkspaceIntegrationGatewayServer() (*Server, *workspaceIntegrationGatewayStore) {
	fakeStore := &workspaceIntegrationGatewayStore{
		machines: map[string]*store.Machine{
			"machine-123": {ID: "machine-123", AccountID: 1, WorkspaceID: stringPtr("workspace-1")},
		},
		workspaces: map[string]*store.Workspace{
			"machine-123": {ID: "workspace-1", AccountID: 1, Slug: "default", Name: "Default"},
		},
		plugins: map[string][]store.MachinePlugin{
			"machine-123": {
				{MachineID: "machine-123", PluginID: workspaceIntegrationPluginID, Enabled: true},
			},
		},
		integrations: map[string][]store.WorkspaceIntegration{
			"machine-123": {
				{
					ID:           "integration-1",
					WorkspaceID:  "workspace-1",
					Slug:         "mock",
					DisplayName:  "Mock",
					Kind:         "mock",
					Transport:    "mock",
					Endpoint:     stringPtr("https://secret.example.test"),
					Enabled:      true,
					ToolManifest: json.RawMessage(`[{"name":"echo","description":"Echo input","parameters":{"type":"object","properties":{"message":{"type":"string"}}}}]`),
					Config:       json.RawMessage(`{"api_key":"super-secret"}`),
				},
			},
		},
		connectorProjections:  map[string][]store.WorkspaceIntegrationConnectorProjection{},
		guidanceOverlays:      map[string][]store.WorkspaceIntegrationGuidanceOverlay{},
		credentials:           map[string]store.WorkspaceIntegrationCredential{},
		connectionCredentials: map[string]store.WorkspaceIntegrationConnectionCredential{},
	}
	return &Server{
		store: fakeStore,
		auth:  auth.New(workspaceIntegrationTestSecret),
		allowInsecureWorkspaceIntegrationEndpoints: true,
	}, fakeStore
}

func workspaceIntegrationAuthHeader(t *testing.T, s *Server, machineID string) string {
	t.Helper()
	return "Bearer " + workspaceIntegrationToken(t, s, machineID)
}

func workspaceIntegrationToken(t *testing.T, s *Server, machineID string) string {
	t.Helper()
	token, err := s.auth.IssueWorkspaceIntegrationToken(machineID, time.Minute)
	if err != nil {
		t.Fatalf("mint workspace integration token: %v", err)
	}
	return token
}

func workspaceIntegrationCallRequest(t *testing.T, tool string, body map[string]interface{}) *http.Request {
	t.Helper()
	encoded, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/ocm-integrations/tools/"+tool+"/call", bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tool", tool)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func captureWorkspaceIntegrationLogs(t *testing.T, fn func()) string {
	t.Helper()
	var logBuf bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	fn()
	return logBuf.String()
}

func workspaceIntegrationToolNameSet(tools []workspaceIntegrationTool) map[string]bool {
	out := make(map[string]bool, len(tools))
	for _, tool := range tools {
		out[tool.Name] = true
	}
	return out
}

func newDeterministicWorkspaceIntegrationMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload workspaceIntegrationMCPJSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode mcp payload: %v", err)
		}
		switch payload.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session-test-mcp")
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      payload.ID,
				"result": map[string]interface{}{
					"protocolVersion": workspaceIntegrationMCPProtocolVersion,
					"serverInfo": map[string]interface{}{
						"name":    "Deterministic Test MCP",
						"version": "1.0.0",
					},
					"capabilities": map[string]interface{}{
						"tools": map[string]interface{}{},
					},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "session-test-mcp" {
				t.Fatalf("session header = %q", r.Header.Get("Mcp-Session-Id"))
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      payload.ID,
				"result": map[string]interface{}{
					"tools": []map[string]interface{}{
						{
							"name":        "echo",
							"description": "Echo a stable message.",
							"inputSchema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"message": map[string]interface{}{"type": "string"},
								},
								"required":             []string{"message"},
								"additionalProperties": false,
							},
						},
						{
							"name":        "create_record",
							"description": "Create a deterministic test record.",
							"inputSchema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"title":       map[string]interface{}{"type": "string"},
									"force_error": map[string]interface{}{"type": "boolean"},
								},
								"required":             []string{"title"},
								"additionalProperties": false,
							},
						},
					},
				},
			})
		case "tools/call":
			if r.Header.Get("Mcp-Session-Id") != "session-test-mcp" {
				t.Fatalf("session header = %q", r.Header.Get("Mcp-Session-Id"))
			}
			params, ok := payload.Params.(map[string]interface{})
			if !ok {
				t.Fatalf("tools/call params = %T", payload.Params)
			}
			name, _ := params["name"].(string)
			args, _ := params["arguments"].(map[string]interface{})
			if name == "create_record" && args["force_error"] == true {
				writeJSON(w, http.StatusOK, map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      payload.ID,
					"error": map[string]interface{}{
						"code":    -32000,
						"message": "forced deterministic failure",
					},
				})
				return
			}
			switch name {
			case "echo":
				writeJSON(w, http.StatusOK, map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      payload.ID,
					"result": map[string]interface{}{
						"content": []map[string]interface{}{{"type": "text", "text": "echo:" + workspaceIntegrationArgString(args["message"])}},
						"structuredContent": map[string]interface{}{
							"message": workspaceIntegrationArgString(args["message"]),
						},
					},
				})
			case "create_record":
				writeJSON(w, http.StatusOK, map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      payload.ID,
					"result": map[string]interface{}{
						"content": []map[string]interface{}{{"type": "text", "text": "created"}},
						"structuredContent": map[string]interface{}{
							"id":    "rec_123",
							"title": workspaceIntegrationArgString(args["title"]),
						},
					},
				})
			default:
				t.Fatalf("unexpected tool call %q", name)
			}
		default:
			t.Fatalf("unexpected mcp method %q", payload.Method)
		}
	}))
}

func createDeterministicTestMCPIntegrationViaCustomPath(t *testing.T, endpoint string) store.WorkspaceIntegration {
	t.Helper()
	ms := newMockWorkspaceIntegrationManagementStore()
	workspaceID := "workspace-1"
	ms.machines["machine-123"] = &store.Machine{ID: "machine-123", AccountID: 1, WorkspaceID: &workspaceID, Kind: store.MachineKindOpenClaw, Name: "Builder", Status: "running"}
	srv := &Server{
		store:    ms,
		activity: events.New(ms, nil),
		allowInsecureWorkspaceIntegrationEndpoints: true,
	}

	probeReq := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/probe", map[string]interface{}{
		"url": endpoint,
	}, 1, "workspace-1", "", 7)
	probeW := httptest.NewRecorder()
	srv.handleProbeWorkspaceIntegration(probeW, probeReq)
	if probeW.Code != http.StatusOK {
		t.Fatalf("probe expected 200, got %d: %s", probeW.Code, probeW.Body.String())
	}
	var probe workspaceIntegrationProbeResponse
	if err := json.NewDecoder(probeW.Body).Decode(&probe); err != nil {
		t.Fatalf("decode probe response: %v", err)
	}
	if len(probe.Tools) != 2 {
		t.Fatalf("probe tools = %+v, want deterministic read and write tools", probe.Tools)
	}

	manifest := make([]map[string]interface{}, 0, len(probe.Tools))
	for _, tool := range probe.Tools {
		manifest = append(manifest, map[string]interface{}{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": jsonRawMessageOrDefault(tool.InputSchema),
		})
	}
	createReq := workspaceIntegrationPolicyRequest(http.MethodPost, "/api/accounts/1/workspaces/workspace-1/integrations/test-mcp", map[string]interface{}{
		"display_name":  "Deterministic Test MCP",
		"kind":          "custom-mcp",
		"transport":     "mcp-remote",
		"endpoint":      endpoint,
		"tool_manifest": manifest,
	}, 1, "workspace-1", "test-mcp", 7)
	createW := httptest.NewRecorder()
	srv.handleCreateWorkspaceIntegration(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("create expected 200, got %d: %s", createW.Code, createW.Body.String())
	}
	if ms.upserted == nil {
		t.Fatal("expected custom MCP integration to be upserted")
	}
	return *ms.upserted
}

func TestWorkspaceIntegrationRuntimeHTTPClientRejectsBlockedConcreteIP(t *testing.T) {
	client := callWorkspaceIntegrationHTTPClient(false)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.DialContext == nil {
		t.Fatalf("runtime client transport = %T, want hardened *http.Transport with DialContext", client.Transport)
	}

	conn, err := transport.DialContext(context.Background(), "tcp", "169.254.169.254:443")

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

func TestWorkspaceIntegrationRuntimeHTTPClientRejectsRedirectToBlockedHost(t *testing.T) {
	client := callWorkspaceIntegrationHTTPClient(false)
	if client.CheckRedirect == nil {
		t.Fatal("runtime client missing redirect guard")
	}
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

func TestWorkspaceIntegrationListTools_DerivesMachineFromTokenAndOmitsSecrets(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()

	req := httptest.NewRequest(http.MethodGet, "/api/ocm-integrations/tools?machine_id=victim&workspace_id=other", nil)
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()

	s.handleWorkspaceIntegrationListTools(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := fakeStore.workspaceMachineIDs; len(got) != 1 || got[0] != "machine-123" {
		t.Fatalf("workspace lookup machine IDs = %v, want [machine-123]", got)
	}
	if got := fakeStore.listMachineIDs; len(got) != 1 || got[0] != "machine-123" {
		t.Fatalf("list machine IDs = %v, want [machine-123]", got)
	}
	body := w.Body.String()
	if strings.Contains(body, "super-secret") || strings.Contains(body, "secret.example.test") {
		t.Fatalf("response leaked integration secret data: %s", body)
	}

	var resp struct {
		Tools []workspaceIntegrationTool `json:"tools"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	names := workspaceIntegrationToolNameSet(resp.Tools)
	for _, want := range []string{workspaceIntegrationSearchToolsName, workspaceIntegrationDescribeToolName, workspaceIntegrationCallToolName, "mock.echo"} {
		if !names[want] {
			t.Fatalf("tools = %v, missing %s", resp.Tools, want)
		}
	}
}

func TestWorkspaceIntegrationRuntimeUsesConnectorProjectionMetadata(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	legacyToolID := "mock.echo"
	projection := store.WorkspaceIntegrationConnectorProjection{
		Source: store.WorkspaceIntegrationSource{
			ID:          "source-mock",
			WorkspaceID: "workspace-1",
			Slug:        "mock",
			DisplayName: "Mock Source",
			Kind:        "mock",
			Importer:    "mock",
		},
		Connection: store.WorkspaceIntegrationConnection{
			ID:                  "connection-mock",
			WorkspaceID:         "workspace-1",
			SourceID:            "source-mock",
			LegacyIntegrationID: stringPtr("integration-1"),
			Slug:                "mock",
			DisplayName:         "Mock Connection",
			Scope:               workspaceIntegrationConnectionScope,
			CredentialState:     "connected",
			Enabled:             true,
		},
		Tools: []store.WorkspaceIntegrationToolSnapshot{
			{
				ID:            "snapshot-echo",
				WorkspaceID:   "workspace-1",
				ConnectionID:  "connection-mock",
				ToolName:      "echo",
				ToolAddress:   "wi.workspace-1.mock.mock.echo",
				LegacyToolID:  &legacyToolID,
				Description:   "Normalized Echo",
				InputSchema:   json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"},"normalized":{"type":"boolean"}}}`),
				Access:        "read",
				Source:        "mock",
				ToolsSyncedAt: now,
			},
		},
		Policies: []store.WorkspaceIntegrationToolPolicy{
			{
				ID:           "policy-echo",
				WorkspaceID:  "workspace-1",
				ConnectionID: "connection-mock",
				ToolName:     "echo",
				Policy:       workspaceIntegrationPolicyRequireApproval,
				Source:       "test",
			},
		},
	}
	fakeStore.connectorProjections["workspace-1"] = []store.WorkspaceIntegrationConnectorProjection{projection}

	listReq := httptest.NewRequest(http.MethodGet, "/api/ocm-integrations/tools", nil)
	listReq.Header.Set("Authorization", authHeader)
	listW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationListTools(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list expected 200, got %d: %s", listW.Code, listW.Body.String())
	}
	if !strings.Contains(listW.Body.String(), "Normalized Echo") || strings.Contains(listW.Body.String(), "Echo input") {
		t.Fatalf("list did not prefer normalized projection metadata: %s", listW.Body.String())
	}

	searchReq := workspaceIntegrationCallRequest(t, workspaceIntegrationSearchToolsName, map[string]interface{}{
		"arguments": map[string]interface{}{"query": "normalized echo", "integration": "mock", "limit": 1},
	})
	searchReq.Header.Set("Authorization", authHeader)
	searchW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(searchW, searchReq)
	if searchW.Code != http.StatusOK {
		t.Fatalf("search expected 200, got %d: %s", searchW.Code, searchW.Body.String())
	}
	var searchResp struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(searchW.Body.Bytes(), &searchResp); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if len(searchResp.Items) != 1 {
		t.Fatalf("search items = %+v", searchResp.Items)
	}
	if got := searchResp.Items[0]["snapshot_id"]; got != "snapshot-echo" {
		t.Fatalf("snapshot_id = %v", got)
	}
	if got := searchResp.Items[0]["tool_address"]; got != "wi.workspace-1.mock.mock.echo" {
		t.Fatalf("tool_address = %v", got)
	}
	if got := searchResp.Items[0]["policy_state"]; got != workspaceIntegrationPolicyRequireApproval {
		t.Fatalf("policy_state = %v", got)
	}
	if _, ok := searchResp.Items[0]["input_schema"]; ok {
		t.Fatalf("search response should stay compact, got input_schema in %+v", searchResp.Items[0])
	}

	describeReq := workspaceIntegrationCallRequest(t, workspaceIntegrationDescribeToolName, map[string]interface{}{
		"arguments": map[string]interface{}{"tool_address": "wi.workspace-1.mock.mock.echo"},
	})
	describeReq.Header.Set("Authorization", authHeader)
	describeW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(describeW, describeReq)
	if describeW.Code != http.StatusOK {
		t.Fatalf("describe expected 200, got %d: %s", describeW.Code, describeW.Body.String())
	}
	if !strings.Contains(describeW.Body.String(), `"snapshot_id":"snapshot-echo"`) || !strings.Contains(describeW.Body.String(), `"normalized"`) {
		t.Fatalf("describe did not use normalized projection schema: %s", describeW.Body.String())
	}

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": "wi.workspace-1.mock.mock.echo",
			"arguments":    map[string]interface{}{"message": "blocked by normalized policy"},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusConflict {
		t.Fatalf("approval-required call expected 409, got %d: %s", callW.Code, callW.Body.String())
	}

	projection.Policies[0].Policy = workspaceIntegrationPolicyAllow
	fakeStore.connectorProjections["workspace-1"] = []store.WorkspaceIntegrationConnectorProjection{projection}
	callReq = workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": "wi.workspace-1.mock.mock.echo",
			"arguments":    map[string]interface{}{"message": "allowed by normalized policy"},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW = httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusOK {
		t.Fatalf("normalized-address call expected 200 after allow, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"message":"allowed by normalized policy"`) {
		t.Fatalf("normalized-address call response = %s", callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"tool_address":"wi.workspace-1.mock.mock.echo"`) {
		t.Fatalf("normalized-address call missing canonical address: %s", callW.Body.String())
	}

	projection.Policies[0].Policy = workspaceIntegrationPolicyBlock
	fakeStore.connectorProjections["workspace-1"] = []store.WorkspaceIntegrationConnectorProjection{projection}
	listReq = httptest.NewRequest(http.MethodGet, "/api/ocm-integrations/tools", nil)
	listReq.Header.Set("Authorization", authHeader)
	listW = httptest.NewRecorder()
	s.handleWorkspaceIntegrationListTools(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("blocked projection list expected 200, got %d: %s", listW.Code, listW.Body.String())
	}
	if strings.Contains(listW.Body.String(), "mock.echo") || strings.Contains(listW.Body.String(), "Echo input") {
		t.Fatalf("blocked normalized projection fell back to v1 list metadata: %s", listW.Body.String())
	}
	legacyCallReq := workspaceIntegrationCallRequest(t, "mock.echo", map[string]interface{}{
		"arguments": map[string]interface{}{"message": "legacy bypass"},
	})
	legacyCallReq.Header.Set("Authorization", authHeader)
	legacyCallW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(legacyCallW, legacyCallReq)
	if legacyCallW.Code != http.StatusNotFound {
		t.Fatalf("blocked normalized projection should prevent legacy direct call, got %d: %s", legacyCallW.Code, legacyCallW.Body.String())
	}
}

func TestWorkspaceIntegrationRuntimeAdapterRegistryCoversImporterSources(t *testing.T) {
	tests := []struct {
		name        string
		integration store.WorkspaceIntegration
		tool        workspaceIntegrationManifestTool
		wantAdapter string
	}{
		{
			name: "remote mcp",
			integration: store.WorkspaceIntegration{
				Slug:      "test-mcp",
				Kind:      "custom-mcp",
				Transport: "mcp-remote",
			},
			tool:        workspaceIntegrationManifestTool{Name: "echo", MCPRemote: &workspaceIntegrationMCPRemoteToolSpec{Name: "echo"}},
			wantAdapter: "mcp",
		},
		{
			name: "openapi import",
			integration: store.WorkspaceIntegration{
				Slug:      "records-api",
				Kind:      "openapi",
				Transport: "http",
			},
			tool: workspaceIntegrationManifestTool{
				Name:    "list_records",
				Source:  "openapi",
				Request: &workspaceIntegrationHTTPRequest{Method: http.MethodGet, Path: "/records"},
			},
			wantAdapter: "http",
		},
		{
			name: "graphql import",
			integration: store.WorkspaceIntegration{
				Slug:      "records-graphql",
				Kind:      "graphql",
				Transport: "http",
			},
			tool: workspaceIntegrationManifestTool{
				Name:   "record",
				Source: "graphql",
				Request: &workspaceIntegrationHTTPRequest{
					Method: http.MethodPost,
					Body:   &workspaceIntegrationHTTPBody{Encoding: "graphql", Query: "query Record { record { id } }"},
				},
			},
			wantAdapter: "http",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, ok := workspaceIntegrationRuntimeAdapterFor(tt.integration, tt.tool)
			if !ok {
				t.Fatalf("adapter not found for %+v / %+v", tt.integration, tt.tool)
			}
			if adapter.Name() != tt.wantAdapter {
				t.Fatalf("adapter = %q, want %q", adapter.Name(), tt.wantAdapter)
			}
		})
	}
}

func TestWorkspaceIntegrationTokenIssuedBeforeRevocationIsRejected(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	token := workspaceIntegrationToken(t, s, "machine-123")
	validAfter := time.Now().Add(time.Second).UTC()
	fakeStore.machines["machine-123"].WorkspaceIntegrationTokensValidAfter = &validAfter

	req := httptest.NewRequest(http.MethodGet, "/api/ocm-integrations/tools", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	s.handleWorkspaceIntegrationListTools(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "machine-123") {
		t.Fatalf("response leaked raw machine ID: %s", w.Body.String())
	}
}

func TestWorkspaceIntegrationTokenRejectedWhenRuntimePluginDisabled(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	fakeStore.mu.Lock()
	fakeStore.plugins["machine-123"] = []store.MachinePlugin{
		{MachineID: "machine-123", PluginID: workspaceIntegrationPluginID, Enabled: false},
	}
	fakeStore.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/ocm-integrations/tools", nil)
	req.Header.Set("Authorization", authHeader)
	w := httptest.NewRecorder()

	s.handleWorkspaceIntegrationListTools(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "machine-123") {
		t.Fatalf("response leaked raw machine ID: %s", w.Body.String())
	}
}

func TestWorkspaceIntegrationGatewayToolsDisappearAfterRevoke(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	req := httptest.NewRequest(http.MethodGet, "/api/ocm-integrations/tools", nil)
	req.Header.Set("Authorization", authHeader)
	w := httptest.NewRecorder()
	s.handleWorkspaceIntegrationListTools(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected initial list 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "mock.echo") {
		t.Fatalf("initial tools = %s, want mock.echo", w.Body.String())
	}

	fakeStore.mu.Lock()
	fakeStore.integrations["machine-123"] = nil
	fakeStore.mu.Unlock()

	req = httptest.NewRequest(http.MethodGet, "/api/ocm-integrations/tools", nil)
	req.Header.Set("Authorization", authHeader)
	w = httptest.NewRecorder()
	s.handleWorkspaceIntegrationListTools(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected post-revoke list 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "mock.echo") {
		t.Fatalf("post-revoke tools still include revoked tool: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), workspaceIntegrationSearchToolsName) {
		t.Fatalf("post-revoke tools should still include facade tools: %s", w.Body.String())
	}

	callReq := workspaceIntegrationCallRequest(t, "mock.echo", map[string]interface{}{"arguments": map[string]interface{}{"message": "hello"}})
	callReq.Header.Set("Authorization", authHeader)
	w = httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(w, callReq)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected post-revoke call 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkspaceIntegrationCallTool_MockEcho(t *testing.T) {
	s, _ := newWorkspaceIntegrationGatewayServer()
	req := workspaceIntegrationCallRequest(t, "mock.echo", map[string]interface{}{
		"arguments": map[string]interface{}{"message": "hello"},
	})
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()

	s.handleWorkspaceIntegrationCallTool(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	result := resp["result"].(map[string]interface{})
	echo := result["echo"].(map[string]interface{})
	if echo["message"] != "hello" {
		t.Fatalf("echo result = %v", echo)
	}
}

func TestWorkspaceIntegrationCallTool_InvalidArgumentsReturnStructuredContract(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	fakeStore.integrations["machine-123"][0].ToolManifest = json.RawMessage(`[{"name":"echo","description":"Echo input","parameters":{"type":"object","properties":{"message":{"type":"string"}},"required":["message"],"additionalProperties":false}}]`)
	req := workspaceIntegrationCallRequest(t, "mock.echo", map[string]interface{}{
		"arguments": map[string]interface{}{"extra": "do-not-store"},
	})
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()

	s.handleWorkspaceIntegrationCallTool(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Error         string                            `json:"error"`
		ErrorContract workspaceIntegrationErrorContract `json:"error_contract"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorContract.Class != "invalid_arguments" || resp.ErrorContract.Action != "fix_arguments" || resp.ErrorContract.Retryable || !resp.ErrorContract.Terminal {
		t.Fatalf("error contract = %+v", resp.ErrorContract)
	}
	if strings.Contains(w.Body.String(), "do-not-store") {
		t.Fatalf("error response leaked raw argument value: %s", w.Body.String())
	}
	events := waitForWorkspaceIntegrationCallEvents(t, fakeStore, 1)
	event := events[0]
	if event.Status != "error" || event.FailureClass == nil || *event.FailureClass != "invalid_arguments" || !event.Terminal {
		t.Fatalf("call event = %+v", event)
	}
	if strings.Contains(string(event.ArgShape), "do-not-store") {
		t.Fatalf("arg shape leaked raw argument value: %s", event.ArgShape)
	}
}

func TestWorkspaceIntegrationCallEventsSampleSuccessesButKeepErrors(t *testing.T) {
	prevRate := workspaceIntegrationSuccessCallEventSampleRate
	prevSample := workspaceIntegrationCallEventSample
	workspaceIntegrationSuccessCallEventSampleRate = 0
	workspaceIntegrationCallEventSample = func() float64 { return 0 }
	t.Cleanup(func() {
		workspaceIntegrationSuccessCallEventSampleRate = prevRate
		workspaceIntegrationCallEventSample = prevSample
	})

	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	integration := fakeStore.integrations["machine-123"][0]
	tool := workspaceIntegrationManifestTool{Name: "echo", Access: "read"}

	s.recordWorkspaceIntegrationCallEvent(context.Background(), "machine-123", integration, tool, map[string]interface{}{"message": "sampled-out"}, "direct", "success", "", nil, 1, nil)
	time.Sleep(25 * time.Millisecond)
	fakeStore.mu.Lock()
	successEvents := append([]store.WorkspaceIntegrationCallEvent(nil), fakeStore.callEvents...)
	fakeStore.mu.Unlock()
	if len(successEvents) != 0 {
		t.Fatalf("sampled-out success should not record event: %+v", successEvents)
	}

	s.recordWorkspaceIntegrationCallEvent(context.Background(), "machine-123", integration, tool, map[string]interface{}{"message": "error-kept"}, "direct", "error", "tool_call_failed", nil, 1, nil)
	events := waitForWorkspaceIntegrationCallEvents(t, fakeStore, 1)
	event := events[0]
	if event.Status != "error" || event.FailureClass == nil || *event.FailureClass != "tool_call_failed" || event.SampleRate != 1 {
		t.Fatalf("error event should be unsampled and retained: %+v", event)
	}
}

func TestWorkspaceIntegrationCallEventWriteIsAsyncAndDetached(t *testing.T) {
	forceWorkspaceIntegrationSuccessCallEventSampling(t)
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	fakeStore.callEventBlock = make(chan struct{})
	fakeStore.callEventStarted = make(chan struct{}, 1)
	integration := fakeStore.integrations["machine-123"][0]
	tool := workspaceIntegrationManifestTool{Name: "echo", Access: "read"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		s.recordWorkspaceIntegrationCallEvent(ctx, "machine-123", integration, tool, map[string]interface{}{"message": "detached"}, "direct", "success", "", nil, 1, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("recordWorkspaceIntegrationCallEvent blocked on telemetry write")
	}
	select {
	case <-fakeStore.callEventStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("async telemetry write did not start")
	}
	close(fakeStore.callEventBlock)
	events := waitForWorkspaceIntegrationCallEvents(t, fakeStore, 1)
	if events[0].Status != "success" {
		t.Fatalf("event = %+v", events[0])
	}
	fakeStore.mu.Lock()
	errs := append([]error(nil), fakeStore.callEventContextErrs...)
	fakeStore.mu.Unlock()
	if len(errs) != 1 || errs[0] != nil {
		t.Fatalf("telemetry write context was not detached from canceled request: %+v", errs)
	}
}

func TestWorkspaceIntegrationCallEventWriteDropsWhenAsyncLimitSaturated(t *testing.T) {
	forceWorkspaceIntegrationSuccessCallEventSampling(t)
	prevSlots := workspaceIntegrationCallEventWriteSlots
	prevDropped := workspaceIntegrationCallEventDropped.Load()
	testSlots := make(chan struct{}, 1)
	testSlots <- struct{}{}
	workspaceIntegrationCallEventWriteSlots = testSlots
	workspaceIntegrationCallEventDropped.Store(0)
	t.Cleanup(func() {
		<-testSlots
		workspaceIntegrationCallEventWriteSlots = prevSlots
		workspaceIntegrationCallEventDropped.Store(prevDropped)
	})

	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	integration := fakeStore.integrations["machine-123"][0]
	tool := workspaceIntegrationManifestTool{Name: "echo", Access: "read"}

	done := make(chan struct{})
	go func() {
		s.recordWorkspaceIntegrationCallEvent(context.Background(), "machine-123", integration, tool, map[string]interface{}{"message": "drop"}, "direct", "success", "", nil, 1, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("recordWorkspaceIntegrationCallEvent blocked when telemetry write slots were saturated")
	}
	if got := workspaceIntegrationCallEventDropped.Load(); got != 1 {
		t.Fatalf("dropped telemetry count = %d, want 1", got)
	}
	time.Sleep(25 * time.Millisecond)
	fakeStore.mu.Lock()
	defer fakeStore.mu.Unlock()
	if len(fakeStore.callEvents) != 0 {
		t.Fatalf("saturated telemetry write should be dropped, got events %+v", fakeStore.callEvents)
	}
}

func TestWorkspaceIntegrationApprovedGuidanceOverlaysReachRuntimeTools(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	fakeStore.guidanceOverlays["workspace-1"] = []store.WorkspaceIntegrationGuidanceOverlay{
		{
			ID:                 "guidance-1",
			AccountID:          1,
			WorkspaceID:        "workspace-1",
			ToolID:             "mock.echo",
			IntegrationSlug:    "mock",
			ToolName:           "echo",
			Status:             "approved",
			Version:            3,
			Guidance:           "Always send a concise message argument.",
			SourceFailureClass: stringPtr("invalid_arguments"),
		},
	}
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	listReq := httptest.NewRequest(http.MethodGet, "/api/ocm-integrations/tools", nil)
	listReq.Header.Set("Authorization", authHeader)
	listW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationListTools(listW, listReq)

	if listW.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", listW.Code, listW.Body.String())
	}
	if !strings.Contains(listW.Body.String(), "Workspace guidance: Always send a concise message argument.") {
		t.Fatalf("tools list missing approved guidance overlay: %s", listW.Body.String())
	}

	describeReq := workspaceIntegrationCallRequest(t, workspaceIntegrationDescribeToolName, map[string]interface{}{
		"arguments": map[string]interface{}{"tool_id": "mock.echo"},
	})
	describeReq.Header.Set("Authorization", authHeader)
	describeW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(describeW, describeReq)

	if describeW.Code != http.StatusOK {
		t.Fatalf("expected describe 200, got %d: %s", describeW.Code, describeW.Body.String())
	}
	if !strings.Contains(describeW.Body.String(), `"version":3`) || !strings.Contains(describeW.Body.String(), "Always send a concise message argument.") {
		t.Fatalf("describe response missing guidance metadata: %s", describeW.Body.String())
	}
}

func TestWorkspaceIntegrationAddressGuidanceWinsAndDoesNotLeakToSiblingTools(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	addressA := "wi.workspace-1.github.github-main.repo_info"
	addressB := "wi.workspace-1.github.github-secondary.repo_info"
	fakeStore.guidanceOverlays["workspace-1"] = []store.WorkspaceIntegrationGuidanceOverlay{
		{
			ID:              "guidance-broad",
			AccountID:       1,
			WorkspaceID:     "workspace-1",
			ToolID:          "github.repo_info",
			IntegrationSlug: "github",
			ToolName:        "repo_info",
			Status:          "approved",
			Version:         9,
			Guidance:        "Broad repo guidance.",
		},
		{
			ID:              "guidance-address-a",
			AccountID:       1,
			WorkspaceID:     "workspace-1",
			ToolID:          "github.repo_info",
			ToolAddress:     &addressA,
			IntegrationSlug: "github-main",
			ToolName:        "repo_info",
			Status:          "approved",
			Version:         1,
			Guidance:        "Exact main repo guidance.",
		},
	}
	descriptors := []workspaceIntegrationToolDescriptor{
		{
			ToolID:      "github.repo_info",
			ToolAddress: addressA,
			Description: "Fetch repo info from main.",
		},
		{
			ToolID:      "github.repo_info",
			ToolAddress: addressB,
			Description: "Fetch repo info from secondary.",
		},
	}

	s.applyWorkspaceIntegrationGuidanceOverlays(context.Background(), 1, "workspace-1", descriptors)

	if descriptors[0].Guidance == nil || descriptors[0].Guidance.Text != "Exact main repo guidance." || descriptors[0].Guidance.Version != 1 {
		t.Fatalf("address-specific guidance did not win for first descriptor: %+v", descriptors[0].Guidance)
	}
	if descriptors[1].Guidance == nil || descriptors[1].Guidance.Text != "Broad repo guidance." || descriptors[1].Guidance.Version != 9 {
		t.Fatalf("broad guidance should apply to sibling without exact guidance: %+v", descriptors[1].Guidance)
	}

	fakeStore.guidanceOverlays["workspace-1"] = []store.WorkspaceIntegrationGuidanceOverlay{
		{
			ID:              "guidance-address-a-only",
			AccountID:       1,
			WorkspaceID:     "workspace-1",
			ToolID:          "github.repo_info",
			ToolAddress:     &addressA,
			IntegrationSlug: "github-main",
			ToolName:        "repo_info",
			Status:          "approved",
			Version:         4,
			Guidance:        "Only main repo guidance.",
		},
	}
	descriptors = []workspaceIntegrationToolDescriptor{
		{
			ToolID:      "github.repo_info",
			ToolAddress: addressA,
			Description: "Fetch repo info from main.",
		},
		{
			ToolID:      "github.repo_info",
			ToolAddress: addressB,
			Description: "Fetch repo info from secondary.",
		},
	}

	s.applyWorkspaceIntegrationGuidanceOverlays(context.Background(), 1, "workspace-1", descriptors)

	if descriptors[0].Guidance == nil || descriptors[0].Guidance.Text != "Only main repo guidance." {
		t.Fatalf("address-specific guidance missing for first descriptor: %+v", descriptors[0].Guidance)
	}
	if descriptors[1].Guidance != nil {
		t.Fatalf("address-specific guidance leaked to sibling descriptor: %+v", descriptors[1].Guidance)
	}
}

func TestWorkspaceIntegrationGuidanceReplayFromSanitizedTelemetry(t *testing.T) {
	forceWorkspaceIntegrationSuccessCallEventSampling(t)
	repoAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repo" {
			t.Fatalf("unexpected replay path %q", r.URL.Path)
		}
		repo := r.URL.Query().Get("repo")
		if !strings.Contains(repo, "/") {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"error": map[string]interface{}{
					"status":  "INVALID_ARGUMENT",
					"message": "repo must use owner/name format",
				},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"full_name": repo,
			"private":   false,
		})
	}))
	defer repoAPI.Close()

	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{
		{
			ID:          "integration-replay-github",
			WorkspaceID: "workspace-1",
			Slug:        "github",
			DisplayName: "GitHub",
			Kind:        "github",
			Transport:   "http",
			Endpoint:    stringPtr(repoAPI.URL),
			Enabled:     true,
			ToolManifest: json.RawMessage(`[
				{
					"name": "repo_info",
					"description": "Fetch repository metadata.",
					"access": "read",
					"parameters": {
						"type": "object",
						"properties": {
							"repo": {"type": "string"}
						},
						"required": ["repo"],
						"additionalProperties": false
					},
					"request": {
						"method": "GET",
						"path": "/repo",
						"query": {
							"repo": {"source": "arg", "name": "repo"}
						}
					}
				}
			]`),
		},
	}
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	failReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_id":   "github.repo_info",
			"arguments": map[string]interface{}{"repo": "ocm_cloud"},
		},
	})
	failReq.Header.Set("Authorization", authHeader)
	failW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(failW, failReq)
	if failW.Code != http.StatusBadGateway {
		t.Fatalf("bare repo call expected 502, got %d: %s", failW.Code, failW.Body.String())
	}
	events := waitForWorkspaceIntegrationCallEvents(t, fakeStore, 1)
	failureEvent := events[0]
	if failureEvent.Status != "error" || failureEvent.FailureClass == nil || *failureEvent.FailureClass != "upstream_http_status" {
		t.Fatalf("failure event = %+v", failureEvent)
	}
	if !workspaceIntegrationArgShapeHas(failureEvent.ArgShape, "repo_format", "bare_name") {
		t.Fatalf("failure event missing sanitized repo_format evidence: %s", failureEvent.ArgShape)
	}
	if strings.Contains(string(failureEvent.ArgShape), "ocm_cloud") {
		t.Fatalf("failure arg shape leaked raw repo name: %s", failureEvent.ArgShape)
	}

	beforeHealth, err := fakeStore.ListWorkspaceIntegrationToolHealth(context.Background(), 1, "workspace-1", store.WorkspaceIntegrationHealthQuery{
		Since: failureEvent.CreatedAt.Add(-time.Second),
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("list pre-guidance health: %v", err)
	}
	if len(beforeHealth) != 1 || len(beforeHealth[0].TopFailureClasses) != 1 || beforeHealth[0].TopFailureClasses[0].Class != "upstream_http_status" {
		t.Fatalf("pre-guidance health = %+v", beforeHealth)
	}

	operatorID := 7
	drafts, err := fakeStore.CreateWorkspaceIntegrationGuidanceDraftsFromTelemetry(
		context.Background(),
		1,
		"workspace-1",
		failureEvent.CreatedAt.Add(-time.Second),
		1,
		&operatorID,
	)
	if err != nil {
		t.Fatalf("draft guidance from telemetry: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("drafts = %+v, want one", drafts)
	}
	draft := drafts[0]
	if draft.Status != "draft" || draft.Version != 1 || !strings.Contains(draft.Guidance, "owner/name") {
		t.Fatalf("draft = %+v", draft)
	}
	if !strings.Contains(string(draft.SanitizedPattern), `"repo_format_bare_name":true`) || strings.Contains(string(draft.SanitizedPattern), "ocm_cloud") {
		t.Fatalf("draft sanitized pattern = %s", draft.SanitizedPattern)
	}

	if _, err := fakeStore.ApproveWorkspaceIntegrationGuidanceOverlay(context.Background(), 1, "workspace-1", draft.ID, operatorID); err != nil {
		t.Fatalf("approve guidance: %v", err)
	}
	describeReq := workspaceIntegrationCallRequest(t, workspaceIntegrationDescribeToolName, map[string]interface{}{
		"arguments": map[string]interface{}{"tool_id": "github.repo_info"},
	})
	describeReq.Header.Set("Authorization", authHeader)
	describeW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(describeW, describeReq)
	if describeW.Code != http.StatusOK {
		t.Fatalf("describe expected 200, got %d: %s", describeW.Code, describeW.Body.String())
	}
	if !strings.Contains(describeW.Body.String(), "Workspace guidance: Repository arguments should use owner/name format") || !strings.Contains(describeW.Body.String(), `"version":1`) {
		t.Fatalf("describe missing approved replay guidance: %s", describeW.Body.String())
	}

	postGuidanceSince := time.Now().UTC()
	time.Sleep(time.Millisecond)
	successReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_id":   "github.repo_info",
			"arguments": map[string]interface{}{"repo": "mathaix/ocm_cloud"},
		},
	})
	successReq.Header.Set("Authorization", authHeader)
	successW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(successW, successReq)
	if successW.Code != http.StatusOK {
		t.Fatalf("corrected repo call expected 200, got %d: %s", successW.Code, successW.Body.String())
	}
	waitForWorkspaceIntegrationCallEvents(t, fakeStore, 2)
	postHealth, err := fakeStore.ListWorkspaceIntegrationToolHealth(context.Background(), 1, "workspace-1", store.WorkspaceIntegrationHealthQuery{
		Since: postGuidanceSince,
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("list post-guidance health: %v", err)
	}
	if len(postHealth) != 1 || postHealth[0].SuccessCalls != 1 || postHealth[0].ErrorCalls != 0 || len(postHealth[0].TopFailureClasses) != 0 {
		t.Fatalf("post-guidance health did not drop failure class: %+v", postHealth)
	}
}

func TestWorkspaceIntegrationToolHealthSeparatesSameToolIDByToolAddress(t *testing.T) {
	_, fakeStore := newWorkspaceIntegrationGatewayServer()
	now := time.Now().UTC()
	addressA := "wi.workspace-1.github.github-main.repo_info"
	addressB := "wi.workspace-1.github.github-secondary.repo_info"
	for _, event := range []store.WorkspaceIntegrationCallEvent{
		{
			AccountID:       1,
			WorkspaceID:     "workspace-1",
			IntegrationSlug: "github-main",
			ToolName:        "repo_info",
			ToolID:          "github.repo_info",
			ToolAddress:     &addressA,
			Transport:       "http",
			Access:          "read",
			Status:          "success",
			LatencyMS:       11,
			SampleRate:      1,
			DetailLevel:     "telemetry",
			CreatedAt:       now,
		},
		{
			AccountID:       1,
			WorkspaceID:     "workspace-1",
			IntegrationSlug: "github-secondary",
			ToolName:        "repo_info",
			ToolID:          "github.repo_info",
			ToolAddress:     &addressB,
			Transport:       "http",
			Access:          "read",
			Status:          "error",
			FailureClass:    stringPtr("upstream_http_status"),
			LatencyMS:       27,
			SampleRate:      1,
			DetailLevel:     "telemetry",
			CreatedAt:       now.Add(time.Millisecond),
		},
	} {
		next := event
		if err := fakeStore.RecordWorkspaceIntegrationCallEvent(context.Background(), &next); err != nil {
			t.Fatalf("record call event: %v", err)
		}
	}

	health, err := fakeStore.ListWorkspaceIntegrationToolHealth(context.Background(), 1, "workspace-1", store.WorkspaceIntegrationHealthQuery{
		Since: now.Add(-time.Second),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("list health: %v", err)
	}
	if len(health) != 2 {
		t.Fatalf("health rows = %+v, want one row per tool_address", health)
	}
	byAddress := map[string]store.WorkspaceIntegrationToolHealth{}
	for _, row := range health {
		if row.ToolAddress == nil {
			t.Fatalf("health row missing tool_address: %+v", row)
		}
		byAddress[*row.ToolAddress] = row
	}
	if got := byAddress[addressA]; got.IntegrationSlug != "github-main" || got.SuccessCalls != 1 || got.ErrorCalls != 0 {
		t.Fatalf("health for %s = %+v", addressA, got)
	}
	if got := byAddress[addressB]; got.IntegrationSlug != "github-secondary" || got.SuccessCalls != 0 || got.ErrorCalls != 1 {
		t.Fatalf("health for %s = %+v", addressB, got)
	}
}

func TestWorkspaceIntegrationGuidanceDraftVersionsAreAddressQualified(t *testing.T) {
	_, fakeStore := newWorkspaceIntegrationGatewayServer()
	now := time.Now().UTC()
	addressA := "wi.workspace-1.github.github-main.repo_info"
	addressB := "wi.workspace-1.github.github-secondary.repo_info"
	fakeStore.guidanceOverlays["workspace-1"] = []store.WorkspaceIntegrationGuidanceOverlay{
		{
			ID:              "guidance-existing",
			AccountID:       1,
			WorkspaceID:     "workspace-1",
			ToolID:          "github.repo_info",
			ToolAddress:     &addressA,
			IntegrationSlug: "github-main",
			ToolName:        "repo_info",
			Status:          "approved",
			Version:         3,
			Guidance:        "Existing main guidance.",
		},
	}
	event := store.WorkspaceIntegrationCallEvent{
		AccountID:       1,
		WorkspaceID:     "workspace-1",
		IntegrationSlug: "github-secondary",
		ToolName:        "repo_info",
		ToolID:          "github.repo_info",
		ToolAddress:     &addressB,
		Status:          "error",
		FailureClass:    stringPtr("invalid_arguments"),
		DetailLevel:     "telemetry",
		CreatedAt:       now,
	}
	if err := fakeStore.RecordWorkspaceIntegrationCallEvent(context.Background(), &event); err != nil {
		t.Fatalf("record call event: %v", err)
	}

	drafts, err := fakeStore.CreateWorkspaceIntegrationGuidanceDraftsFromTelemetry(context.Background(), 1, "workspace-1", now.Add(-time.Second), 10, nil)
	if err != nil {
		t.Fatalf("draft guidance from telemetry: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("drafts = %+v, want one", drafts)
	}
	if drafts[0].ToolAddress == nil || *drafts[0].ToolAddress != addressB || drafts[0].Version != 1 {
		t.Fatalf("draft for secondary address = %+v, want version 1 for %s", drafts[0], addressB)
	}
}

func TestWorkspaceIntegrationFacadeAmbiguousLegacyToolIDRequiresToolAddress(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	source := store.WorkspaceIntegrationSource{
		ID:          "source-github",
		WorkspaceID: "workspace-1",
		Slug:        "github",
		DisplayName: "GitHub",
		Kind:        "github",
		Importer:    "github",
	}
	legacyToolID := "github.repo_info"
	fakeStore.connectorProjections["workspace-1"] = []store.WorkspaceIntegrationConnectorProjection{
		{
			Source: source,
			Connection: store.WorkspaceIntegrationConnection{
				ID:              "connection-main",
				WorkspaceID:     "workspace-1",
				SourceID:        "source-github",
				Slug:            "github-main",
				DisplayName:     "GitHub Main",
				Scope:           workspaceIntegrationConnectionScope,
				CredentialState: "connected",
				Enabled:         true,
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{{
				ID:            "snapshot-main",
				WorkspaceID:   "workspace-1",
				ConnectionID:  "connection-main",
				ToolName:      "repo_info",
				ToolAddress:   "wi.workspace-1.github.github-main.repo_info",
				LegacyToolID:  &legacyToolID,
				Description:   "Fetch repository metadata from main GitHub.",
				InputSchema:   json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"}}}`),
				Access:        "read",
				Source:        "github",
				ToolsSyncedAt: time.Now().UTC(),
			}},
		},
		{
			Source: source,
			Connection: store.WorkspaceIntegrationConnection{
				ID:              "connection-secondary",
				WorkspaceID:     "workspace-1",
				SourceID:        "source-github",
				Slug:            "github-secondary",
				DisplayName:     "GitHub Secondary",
				Scope:           workspaceIntegrationConnectionScope,
				CredentialState: "connected",
				Enabled:         true,
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{{
				ID:            "snapshot-secondary",
				WorkspaceID:   "workspace-1",
				ConnectionID:  "connection-secondary",
				ToolName:      "repo_info",
				ToolAddress:   "wi.workspace-1.github.github-secondary.repo_info",
				LegacyToolID:  &legacyToolID,
				Description:   "Fetch repository metadata from secondary GitHub.",
				InputSchema:   json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"}}}`),
				Access:        "read",
				Source:        "github",
				ToolsSyncedAt: time.Now().UTC(),
			}},
		},
	}

	describeReq := workspaceIntegrationCallRequest(t, workspaceIntegrationDescribeToolName, map[string]interface{}{
		"arguments": map[string]interface{}{"tool_id": legacyToolID},
	})
	describeReq.Header.Set("Authorization", authHeader)
	describeW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(describeW, describeReq)
	if describeW.Code != http.StatusConflict {
		t.Fatalf("ambiguous describe expected 409, got %d: %s", describeW.Code, describeW.Body.String())
	}
	for _, want := range []string{"wi.workspace-1.github.github-main.repo_info", "wi.workspace-1.github.github-secondary.repo_info", "GitHub Main", "GitHub Secondary"} {
		if !strings.Contains(describeW.Body.String(), want) {
			t.Fatalf("ambiguous describe response missing %q: %s", want, describeW.Body.String())
		}
	}

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_id":   legacyToolID,
			"arguments": map[string]interface{}{"repo": "mathaix/ocm_cloud"},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusConflict {
		t.Fatalf("ambiguous call expected 409, got %d: %s", callW.Code, callW.Body.String())
	}

	mcpDescribeBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": workspaceIntegrationDescribeToolName,
			"arguments": map[string]interface{}{
				"tool_id": legacyToolID,
			},
		},
	})
	if err != nil {
		t.Fatalf("encode mcp ambiguous describe: %v", err)
	}
	mcpDescribeReq := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", bytes.NewReader(mcpDescribeBody))
	mcpDescribeReq.Header.Set("Authorization", authHeader)
	mcpDescribeW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(mcpDescribeW, mcpDescribeReq)
	if mcpDescribeW.Code != http.StatusOK {
		t.Fatalf("mcp ambiguous describe expected JSON-RPC 200, got %d: %s", mcpDescribeW.Code, mcpDescribeW.Body.String())
	}
	if !strings.Contains(mcpDescribeW.Body.String(), `"code":-32602`) {
		t.Fatalf("mcp ambiguous describe should be invalid params: %s", mcpDescribeW.Body.String())
	}
	for _, want := range []string{"wi.workspace-1.github.github-main.repo_info", "wi.workspace-1.github.github-secondary.repo_info", "GitHub Main", "GitHub Secondary"} {
		if !strings.Contains(mcpDescribeW.Body.String(), want) {
			t.Fatalf("mcp ambiguous describe response missing %q: %s", want, mcpDescribeW.Body.String())
		}
	}

	mcpCallBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": workspaceIntegrationCallToolName,
			"arguments": map[string]interface{}{
				"tool_id":   legacyToolID,
				"arguments": map[string]interface{}{"repo": "mathaix/ocm_cloud"},
			},
		},
	})
	if err != nil {
		t.Fatalf("encode mcp ambiguous call: %v", err)
	}
	mcpCallReq := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", bytes.NewReader(mcpCallBody))
	mcpCallReq.Header.Set("Authorization", authHeader)
	mcpCallW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(mcpCallW, mcpCallReq)
	if mcpCallW.Code != http.StatusOK {
		t.Fatalf("mcp ambiguous call expected JSON-RPC 200, got %d: %s", mcpCallW.Code, mcpCallW.Body.String())
	}
	if !strings.Contains(mcpCallW.Body.String(), `"code":-32602`) || !strings.Contains(mcpCallW.Body.String(), "wi.workspace-1.github.github-main.repo_info") || !strings.Contains(mcpCallW.Body.String(), "wi.workspace-1.github.github-secondary.repo_info") {
		t.Fatalf("mcp ambiguous call response = %s", mcpCallW.Body.String())
	}

	describeByAddressReq := workspaceIntegrationCallRequest(t, workspaceIntegrationDescribeToolName, map[string]interface{}{
		"arguments": map[string]interface{}{"tool_address": "wi.workspace-1.github.github-secondary.repo_info"},
	})
	describeByAddressReq.Header.Set("Authorization", authHeader)
	describeByAddressW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(describeByAddressW, describeByAddressReq)
	if describeByAddressW.Code != http.StatusOK {
		t.Fatalf("describe by tool_address expected 200, got %d: %s", describeByAddressW.Code, describeByAddressW.Body.String())
	}
	if !strings.Contains(describeByAddressW.Body.String(), `"connection_slug":"github-secondary"`) {
		t.Fatalf("describe by tool_address selected wrong connection: %s", describeByAddressW.Body.String())
	}
}

func TestWorkspaceIntegrationFacadeProjectionCallUsesLegacyIntegrationID(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{
		{
			ID:           "integration-main",
			WorkspaceID:  "workspace-1",
			Slug:         "github-main",
			DisplayName:  "GitHub Main",
			Kind:         "github",
			Transport:    "mock",
			Enabled:      true,
			ToolManifest: json.RawMessage(`[{"name":"echo","description":"Echo from main","parameters":{"type":"object","properties":{"message":{"type":"string"}}}}]`),
		},
		{
			ID:           "integration-secondary",
			WorkspaceID:  "workspace-1",
			Slug:         "github-secondary",
			DisplayName:  "GitHub Secondary",
			Kind:         "github",
			Transport:    "mock",
			Enabled:      true,
			ToolManifest: json.RawMessage(`[{"name":"echo","description":"Echo from secondary","parameters":{"type":"object","properties":{"message":{"type":"string"}}}}]`),
		},
	}

	source := store.WorkspaceIntegrationSource{
		ID:          "source-github",
		WorkspaceID: "workspace-1",
		Slug:        "github",
		DisplayName: "GitHub",
		Kind:        "github",
		Importer:    "github",
	}
	mainLegacyID := "integration-main"
	secondaryLegacyID := "integration-secondary"
	sharedLegacyToolID := "github.echo"
	now := time.Now().UTC()
	fakeStore.connectorProjections["workspace-1"] = []store.WorkspaceIntegrationConnectorProjection{
		{
			Source: source,
			Connection: store.WorkspaceIntegrationConnection{
				ID:                  "connection-main",
				WorkspaceID:         "workspace-1",
				SourceID:            "source-github",
				LegacyIntegrationID: &mainLegacyID,
				Slug:                "github-main",
				DisplayName:         "GitHub Main",
				Scope:               workspaceIntegrationConnectionScope,
				CredentialState:     "connected",
				Enabled:             true,
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{{
				ID:            "snapshot-main",
				WorkspaceID:   "workspace-1",
				ConnectionID:  "connection-main",
				ToolName:      "echo",
				ToolAddress:   "wi.workspace-1.github.github-main.echo",
				LegacyToolID:  &sharedLegacyToolID,
				Description:   "Echo through main GitHub connection.",
				InputSchema:   json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}}}`),
				Access:        "read",
				Source:        "github",
				ToolsSyncedAt: now,
			}},
		},
		{
			Source: source,
			Connection: store.WorkspaceIntegrationConnection{
				ID:                  "connection-secondary",
				WorkspaceID:         "workspace-1",
				SourceID:            "source-github",
				LegacyIntegrationID: &secondaryLegacyID,
				Slug:                "github-secondary",
				DisplayName:         "GitHub Secondary",
				Scope:               workspaceIntegrationConnectionScope,
				CredentialState:     "connected",
				Enabled:             true,
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{{
				ID:            "snapshot-secondary",
				WorkspaceID:   "workspace-1",
				ConnectionID:  "connection-secondary",
				ToolName:      "echo",
				ToolAddress:   "wi.workspace-1.github.github-secondary.echo",
				LegacyToolID:  &sharedLegacyToolID,
				Description:   "Echo through secondary GitHub connection.",
				InputSchema:   json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}}}`),
				Access:        "read",
				Source:        "github",
				ToolsSyncedAt: now,
			}},
		},
	}

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": "wi.workspace-1.github.github-secondary.echo",
			"arguments":    map[string]interface{}{"message": "route-secondary"},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusOK {
		t.Fatalf("call by projection tool_address expected 200, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"integration_id":"integration-secondary"`) || !strings.Contains(callW.Body.String(), `"integration_slug":"github-secondary"`) {
		t.Fatalf("call by projection tool_address dispatched to wrong integration: %s", callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"tool_address":"wi.workspace-1.github.github-secondary.echo"`) {
		t.Fatalf("call response missing selected tool_address: %s", callW.Body.String())
	}
}

func TestWorkspaceIntegrationFacadeNormalizedOnlyMockProjectionExecutes(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	source := store.WorkspaceIntegrationSource{
		ID:          "source-normalized",
		WorkspaceID: "workspace-1",
		Slug:        "normalized",
		DisplayName: "Normalized Source",
		Kind:        "mock",
		Importer:    "mock",
	}
	const address = "wi.workspace-1.normalized.normalized-only.echo"
	fakeStore.connectorProjections["workspace-1"] = []store.WorkspaceIntegrationConnectorProjection{
		{
			Source: source,
			Connection: store.WorkspaceIntegrationConnection{
				ID:              "connection-normalized-only",
				WorkspaceID:     "workspace-1",
				SourceID:        "source-normalized",
				Slug:            "normalized-only",
				DisplayName:     "Normalized Only",
				Scope:           workspaceIntegrationConnectionScope,
				CredentialState: "connected",
				Enabled:         true,
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{{
				ID:            "snapshot-normalized-only",
				WorkspaceID:   "workspace-1",
				ConnectionID:  "connection-normalized-only",
				ToolName:      "echo",
				ToolAddress:   address,
				Description:   "Echo from a normalized-only connection.",
				InputSchema:   json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}}}`),
				Access:        "read",
				Source:        "mock",
				ToolsSyncedAt: time.Now().UTC(),
			}},
			Policies: []store.WorkspaceIntegrationToolPolicy{{
				ID:           "policy-normalized-only",
				WorkspaceID:  "workspace-1",
				ConnectionID: "connection-normalized-only",
				ToolName:     "echo",
				Policy:       workspaceIntegrationPolicyAllow,
				Source:       "test",
			}},
		},
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/ocm-integrations/tools", nil)
	listReq.Header.Set("Authorization", authHeader)
	listW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationListTools(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list normalized-only projection expected 200, got %d: %s", listW.Code, listW.Body.String())
	}
	if !strings.Contains(listW.Body.String(), address) || !strings.Contains(listW.Body.String(), "Echo from a normalized-only connection.") {
		t.Fatalf("legacy direct tool list did not advertise normalized mock runtime: %s", listW.Body.String())
	}

	describeReq := workspaceIntegrationCallRequest(t, workspaceIntegrationDescribeToolName, map[string]interface{}{
		"arguments": map[string]interface{}{"tool_address": address},
	})
	describeReq.Header.Set("Authorization", authHeader)
	describeW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(describeW, describeReq)
	if describeW.Code != http.StatusOK {
		t.Fatalf("describe normalized-only projection expected 200, got %d: %s", describeW.Code, describeW.Body.String())
	}
	if !strings.Contains(describeW.Body.String(), `"connection_id":"connection-normalized-only"`) {
		t.Fatalf("describe did not resolve normalized-only projection: %s", describeW.Body.String())
	}

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": address,
			"arguments":    map[string]interface{}{"message": "hello"},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusOK {
		t.Fatalf("normalized-only mock projection call expected 200, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"message":"hello"`) || !strings.Contains(callW.Body.String(), `"tool_address":"`+address+`"`) {
		t.Fatalf("normalized-only mock projection call response = %s", callW.Body.String())
	}

	directReq := workspaceIntegrationCallRequest(t, address, map[string]interface{}{
		"arguments": map[string]interface{}{"message": "hello"},
	})
	directReq.Header.Set("Authorization", authHeader)
	directW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(directW, directReq)
	if directW.Code != http.StatusOK {
		t.Fatalf("normalized-only mock direct call expected 200, got %d: %s", directW.Code, directW.Body.String())
	}
	if !strings.Contains(directW.Body.String(), `"message":"hello"`) || !strings.Contains(directW.Body.String(), `"integration":"normalized-only"`) {
		t.Fatalf("normalized-only mock direct call response = %s", directW.Body.String())
	}
}

func TestWorkspaceIntegrationFacadeNormalizedOnlyHTTPProjectionExecutesWithRequestProvenance(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/records" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("limit"); got != "7" {
			t.Fatalf("limit query = %q, want 7", got)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"records": []string{"alpha", "beta"}})
	}))
	t.Cleanup(api.Close)

	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	source := store.WorkspaceIntegrationSource{
		ID:          "source-normalized-http",
		WorkspaceID: "workspace-1",
		Slug:        "normalized-http",
		DisplayName: "Normalized HTTP",
		Kind:        "openapi",
		Importer:    "openapi",
	}
	const address = "wi.workspace-1.normalized-http.normalized-http-only.list_records"
	fakeStore.connectorProjections["workspace-1"] = []store.WorkspaceIntegrationConnectorProjection{
		{
			Source: source,
			Connection: store.WorkspaceIntegrationConnection{
				ID:              "connection-normalized-http-only",
				WorkspaceID:     "workspace-1",
				SourceID:        "source-normalized-http",
				Slug:            "normalized-http-only",
				DisplayName:     "Normalized HTTP Only",
				Scope:           workspaceIntegrationConnectionScope,
				CredentialState: "connected",
				Enabled:         true,
				Config:          json.RawMessage(fmt.Sprintf(`{"transport":"http","endpoint":%q}`, api.URL)),
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{{
				ID:           "snapshot-normalized-http-only",
				WorkspaceID:  "workspace-1",
				ConnectionID: "connection-normalized-http-only",
				ToolName:     "list_records",
				ToolAddress:  address,
				Description:  "List records from a normalized-only HTTP connection.",
				InputSchema:  json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer"}}}`),
				Access:       "read",
				Source:       "openapi",
				Provenance: json.RawMessage(`{
					"request":{
						"method":"GET",
						"path":"/records",
						"query":{
							"limit":{"source":"arg","name":"limit"}
						}
					}
				}`),
				ToolsSyncedAt: time.Now().UTC(),
			}},
			Policies: []store.WorkspaceIntegrationToolPolicy{{
				ID:           "policy-normalized-http-only",
				WorkspaceID:  "workspace-1",
				ConnectionID: "connection-normalized-http-only",
				ToolName:     "list_records",
				Policy:       workspaceIntegrationPolicyAllow,
				Source:       "test",
			}},
		},
	}

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": address,
			"arguments":    map[string]interface{}{"limit": 7},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusOK {
		t.Fatalf("normalized-only HTTP projection call expected 200, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"records":["alpha","beta"]`) || !strings.Contains(callW.Body.String(), `"tool_address":"`+address+`"`) {
		t.Fatalf("normalized-only HTTP projection call response = %s", callW.Body.String())
	}
}

func TestWorkspaceIntegrationFacadeNormalizedHTTPProjectionUsesLegacyCredentialReferenceWithoutV1RuntimeRow(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/records" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-secret" {
			t.Fatalf("authorization = %q, want bearer credential", got)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	}))
	t.Cleanup(api.Close)

	secretKey := "12345678901234567890123456789012"
	encrypted, err := crypto.Encrypt("upstream-secret", secretKey)
	if err != nil {
		t.Fatalf("encrypt token: %v", err)
	}
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	s.secretKey = secretKey
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	const legacyIntegrationID = "legacy-http-credential"
	fakeStore.integrations["machine-123"] = nil
	fakeStore.credentials[legacyIntegrationID] = store.WorkspaceIntegrationCredential{
		IntegrationID: legacyIntegrationID,
		SecretEnc:     encrypted,
	}

	source := store.WorkspaceIntegrationSource{
		ID:          "source-normalized-http",
		WorkspaceID: "workspace-1",
		Slug:        "normalized-http",
		DisplayName: "Normalized HTTP",
		Kind:        "openapi",
		Importer:    "openapi",
	}
	const address = "wi.workspace-1.normalized-http.normalized-http-credentialed.list_records"
	fakeStore.connectorProjections["workspace-1"] = []store.WorkspaceIntegrationConnectorProjection{
		{
			Source: source,
			Connection: store.WorkspaceIntegrationConnection{
				ID:                  "connection-normalized-http-credentialed",
				WorkspaceID:         "workspace-1",
				SourceID:            "source-normalized-http",
				LegacyIntegrationID: stringPtr(legacyIntegrationID),
				Slug:                "normalized-http-credentialed",
				DisplayName:         "Normalized HTTP Credentialed",
				Scope:               workspaceIntegrationConnectionScope,
				CredentialState:     "connected",
				Enabled:             true,
				Config:              json.RawMessage(fmt.Sprintf(`{"transport":"http","endpoint":%q}`, api.URL)),
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{{
				ID:           "snapshot-normalized-http-credentialed",
				WorkspaceID:  "workspace-1",
				ConnectionID: "connection-normalized-http-credentialed",
				ToolName:     "list_records",
				ToolAddress:  address,
				Description:  "List records from a normalized HTTP projection with bearer auth.",
				InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false}`),
				Access:       "read",
				Source:       "openapi",
				Provenance: json.RawMessage(`{
					"request":{
						"method":"GET",
						"path":"/records",
						"auth":{"type":"bearer","required":true}
					}
				}`),
				ToolsSyncedAt: time.Now().UTC(),
			}},
			Policies: []store.WorkspaceIntegrationToolPolicy{{
				ID:           "policy-normalized-http-credentialed",
				WorkspaceID:  "workspace-1",
				ConnectionID: "connection-normalized-http-credentialed",
				ToolName:     "list_records",
				Policy:       workspaceIntegrationPolicyAllow,
				Source:       "test",
			}},
		},
	}

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": address,
			"arguments":    map[string]interface{}{},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusOK {
		t.Fatalf("credentialed normalized HTTP projection call expected 200, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"ok":true`) || !strings.Contains(callW.Body.String(), `"tool_address":"`+address+`"`) {
		t.Fatalf("credentialed normalized HTTP projection call response = %s", callW.Body.String())
	}
}

func TestWorkspaceIntegrationFacadeNormalizedHTTPProjectionUsesConnectionCredentialWithoutLegacyIntegration(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/records" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer normalized-secret" {
			t.Fatalf("authorization = %q, want normalized connection credential", got)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	}))
	t.Cleanup(api.Close)

	secretKey := "12345678901234567890123456789012"
	encrypted, err := crypto.Encrypt("normalized-secret", secretKey)
	if err != nil {
		t.Fatalf("encrypt token: %v", err)
	}
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	s.secretKey = secretKey
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	const connectionID = "connection-normalized-http-secret"
	fakeStore.integrations["machine-123"] = nil
	fakeStore.connectionCredentials[connectionID] = store.WorkspaceIntegrationConnectionCredential{
		ConnectionID: connectionID,
		SecretEnc:    encrypted,
	}

	source := store.WorkspaceIntegrationSource{
		ID:          "source-normalized-http",
		WorkspaceID: "workspace-1",
		Slug:        "normalized-http",
		DisplayName: "Normalized HTTP",
		Kind:        "openapi",
		Importer:    "openapi",
	}
	const address = "wi.workspace-1.normalized-http.normalized-http-secret.list_records"
	fakeStore.connectorProjections["workspace-1"] = []store.WorkspaceIntegrationConnectorProjection{
		{
			Source: source,
			Connection: store.WorkspaceIntegrationConnection{
				ID:              connectionID,
				WorkspaceID:     "workspace-1",
				SourceID:        "source-normalized-http",
				Slug:            "normalized-http-secret",
				DisplayName:     "Normalized HTTP Secret",
				Scope:           workspaceIntegrationConnectionScope,
				CredentialState: "connected",
				Enabled:         true,
				Config:          json.RawMessage(fmt.Sprintf(`{"transport":"http","endpoint":%q}`, api.URL)),
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{{
				ID:           "snapshot-normalized-http-secret",
				WorkspaceID:  "workspace-1",
				ConnectionID: connectionID,
				ToolName:     "list_records",
				ToolAddress:  address,
				Description:  "List records from a normalized-only HTTP credential.",
				InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false}`),
				Access:       "read",
				Source:       "openapi",
				Provenance: json.RawMessage(`{
					"request":{
						"method":"GET",
						"path":"/records",
						"auth":{"type":"bearer","required":true}
					}
				}`),
				ToolsSyncedAt: time.Now().UTC(),
			}},
			Policies: []store.WorkspaceIntegrationToolPolicy{{
				ID:           "policy-normalized-http-secret",
				WorkspaceID:  "workspace-1",
				ConnectionID: connectionID,
				ToolName:     "list_records",
				Policy:       workspaceIntegrationPolicyAllow,
				Source:       "test",
			}},
		},
	}

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": address,
			"arguments":    map[string]interface{}{},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusOK {
		t.Fatalf("normalized HTTP connection credential call expected 200, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"ok":true`) || !strings.Contains(callW.Body.String(), `"tool_address":"`+address+`"`) {
		t.Fatalf("normalized HTTP connection credential call response = %s", callW.Body.String())
	}
}

func TestWorkspaceIntegrationFacadeNormalizedHTTPProjectionRefreshesConnectionOAuthCredential(t *testing.T) {
	var upstreamCalls int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/records" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		call := atomic.AddInt32(&upstreamCalls, 1)
		switch call {
		case 1:
			if got := r.Header.Get("Authorization"); got != "Bearer stale-access" {
				t.Fatalf("first authorization = %q", got)
			}
			writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"error": "expired"})
		case 2:
			if got := r.Header.Get("Authorization"); got != "Bearer fresh-access" {
				t.Fatalf("retry authorization = %q", got)
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
		default:
			t.Fatalf("unexpected upstream retry count %d", call)
		}
	}))
	t.Cleanup(api.Close)

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse token refresh form: %v", err)
		}
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Fatalf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "refresh-token" {
			t.Fatalf("refresh_token = %q", r.Form.Get("refresh_token"))
		}
		if r.Form.Get("client_id") != "normalized-client" {
			t.Fatalf("client_id = %q", r.Form.Get("client_id"))
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"access_token":  "fresh-access",
			"refresh_token": "fresh-refresh",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	t.Cleanup(tokenServer.Close)

	secretKey := "12345678901234567890123456789012"
	accessEnc, err := crypto.Encrypt("stale-access", secretKey)
	if err != nil {
		t.Fatalf("encrypt access token: %v", err)
	}
	refreshEnc, err := crypto.Encrypt("refresh-token", secretKey)
	if err != nil {
		t.Fatalf("encrypt refresh token: %v", err)
	}
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	s.secretKey = secretKey
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	const connectionID = "connection-normalized-http-oauth"
	fakeStore.integrations["machine-123"] = nil
	expiresAt := time.Now().Add(time.Hour)
	fakeStore.connectionCredentials[connectionID] = store.WorkspaceIntegrationConnectionCredential{
		ConnectionID: connectionID,
		SecretEnc:    accessEnc,
		RefreshEnc:   &refreshEnc,
		ExpiresAt:    &expiresAt,
	}

	source := store.WorkspaceIntegrationSource{
		ID:          "source-normalized-http",
		WorkspaceID: "workspace-1",
		Slug:        "normalized-http",
		DisplayName: "Normalized HTTP",
		Kind:        "openapi",
		Importer:    "openapi",
	}
	const address = "wi.workspace-1.normalized-http.normalized-http-oauth.list_records"
	fakeStore.connectorProjections["workspace-1"] = []store.WorkspaceIntegrationConnectorProjection{
		{
			Source: source,
			Connection: store.WorkspaceIntegrationConnection{
				ID:              connectionID,
				WorkspaceID:     "workspace-1",
				SourceID:        "source-normalized-http",
				Slug:            "normalized-http-oauth",
				DisplayName:     "Normalized HTTP OAuth",
				Scope:           workspaceIntegrationConnectionScope,
				CredentialState: "connected",
				Enabled:         true,
				Config:          json.RawMessage(fmt.Sprintf(`{"transport":"http","endpoint":%q}`, api.URL)),
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{{
				ID:           "snapshot-normalized-http-oauth",
				WorkspaceID:  "workspace-1",
				ConnectionID: connectionID,
				ToolName:     "list_records",
				ToolAddress:  address,
				Description:  "List records from a normalized-only OAuth credential.",
				InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false}`),
				Access:       "read",
				Source:       "openapi",
				Provenance: json.RawMessage(fmt.Sprintf(`{
					"request":{
						"method":"GET",
						"path":"/records",
						"auth":{"type":"oauth","required":true,"token_url":%q,"client_id":"normalized-client"}
					}
				}`, tokenServer.URL)),
				ToolsSyncedAt: time.Now().UTC(),
			}},
			Policies: []store.WorkspaceIntegrationToolPolicy{{
				ID:           "policy-normalized-http-oauth",
				WorkspaceID:  "workspace-1",
				ConnectionID: connectionID,
				ToolName:     "list_records",
				Policy:       workspaceIntegrationPolicyAllow,
				Source:       "test",
			}},
		},
	}

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": address,
			"arguments":    map[string]interface{}{},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusOK {
		t.Fatalf("normalized HTTP OAuth connection credential call expected 200, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"ok":true`) || !strings.Contains(callW.Body.String(), `"tool_address":"`+address+`"`) {
		t.Fatalf("normalized HTTP OAuth connection credential call response = %s", callW.Body.String())
	}

	credential, err := fakeStore.GetWorkspaceIntegrationConnectionCredential(context.Background(), connectionID)
	if err != nil {
		t.Fatalf("load refreshed connection credential: %v", err)
	}
	access, err := crypto.Decrypt(credential.SecretEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt refreshed access token: %v", err)
	}
	if access != "fresh-access" {
		t.Fatalf("refreshed access token = %q", access)
	}
	if credential.RefreshEnc == nil {
		t.Fatal("expected refreshed refresh token")
	}
	refresh, err := crypto.Decrypt(*credential.RefreshEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt refreshed refresh token: %v", err)
	}
	if refresh != "fresh-refresh" {
		t.Fatalf("refreshed refresh token = %q", refresh)
	}
}

func TestWorkspaceIntegrationFacadeNormalizedOnlyUnsupportedProjectionReportsBoundary(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	source := store.WorkspaceIntegrationSource{
		ID:          "source-normalized-http",
		WorkspaceID: "workspace-1",
		Slug:        "normalized-http",
		DisplayName: "Normalized HTTP",
		Kind:        "openapi",
		Importer:    "openapi",
	}
	const address = "wi.workspace-1.normalized-http.normalized-http-only.list_records"
	fakeStore.connectorProjections["workspace-1"] = []store.WorkspaceIntegrationConnectorProjection{
		{
			Source: source,
			Connection: store.WorkspaceIntegrationConnection{
				ID:              "connection-normalized-http-only",
				WorkspaceID:     "workspace-1",
				SourceID:        "source-normalized-http",
				Slug:            "normalized-http-only",
				DisplayName:     "Normalized HTTP Only",
				Scope:           workspaceIntegrationConnectionScope,
				CredentialState: "connected",
				Enabled:         true,
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{{
				ID:            "snapshot-normalized-http-only",
				WorkspaceID:   "workspace-1",
				ConnectionID:  "connection-normalized-http-only",
				ToolName:      "list_records",
				ToolAddress:   address,
				Description:   "List records from a normalized-only HTTP connection.",
				InputSchema:   json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer"}}}`),
				Access:        "read",
				Source:        "openapi",
				ToolsSyncedAt: time.Now().UTC(),
			}},
			Policies: []store.WorkspaceIntegrationToolPolicy{{
				ID:           "policy-normalized-http-only",
				WorkspaceID:  "workspace-1",
				ConnectionID: "connection-normalized-http-only",
				ToolName:     "list_records",
				Policy:       workspaceIntegrationPolicyAllow,
				Source:       "test",
			}},
		},
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/ocm-integrations/tools", nil)
	listReq.Header.Set("Authorization", authHeader)
	listW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationListTools(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list normalized-only unsupported projection expected 200, got %d: %s", listW.Code, listW.Body.String())
	}
	if strings.Contains(listW.Body.String(), address) || strings.Contains(listW.Body.String(), "List records from a normalized-only HTTP connection.") {
		t.Fatalf("legacy direct tool list advertised unsupported normalized-only runtime: %s", listW.Body.String())
	}

	describeReq := workspaceIntegrationCallRequest(t, workspaceIntegrationDescribeToolName, map[string]interface{}{
		"arguments": map[string]interface{}{"tool_address": address},
	})
	describeReq.Header.Set("Authorization", authHeader)
	describeW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(describeW, describeReq)
	if describeW.Code != http.StatusOK {
		t.Fatalf("describe unsupported normalized-only projection expected 200, got %d: %s", describeW.Code, describeW.Body.String())
	}

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": address,
			"arguments":    map[string]interface{}{"limit": 5},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusNotImplemented {
		t.Fatalf("unsupported normalized-only projection call expected 501, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), "normalized runtime dispatch is not implemented") || !strings.Contains(callW.Body.String(), address) {
		t.Fatalf("unsupported normalized-only projection call response = %s", callW.Body.String())
	}
}

// TestWorkspaceIntegrationFacadePerConnectionPolicyIsolation proves that two
// connections under the same source exposing the same tool name honor divergent
// per-connection policy independently across search, describe, and call on the
// production projection-authoritative path. A tool blocked on one connection must
// be absent from search, must not describe, and must not dispatch when called by
// its tool_address, while the sibling connection's same-name tool stays usable.
// This is the plan's "describe and call by tool_address route to the selected
// connection's policy" acceptance plus the multi-connection negative cases.
func TestWorkspaceIntegrationFacadePerConnectionPolicyIsolation(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{
		{
			ID:           "integration-main",
			WorkspaceID:  "workspace-1",
			Slug:         "github-main",
			DisplayName:  "GitHub Main",
			Kind:         "github",
			Transport:    "mock",
			Enabled:      true,
			ToolManifest: json.RawMessage(`[{"name":"echo","description":"Echo from main","parameters":{"type":"object","properties":{"message":{"type":"string"}}}}]`),
		},
		{
			ID:           "integration-secondary",
			WorkspaceID:  "workspace-1",
			Slug:         "github-secondary",
			DisplayName:  "GitHub Secondary",
			Kind:         "github",
			Transport:    "mock",
			Enabled:      true,
			ToolManifest: json.RawMessage(`[{"name":"echo","description":"Echo from secondary","parameters":{"type":"object","properties":{"message":{"type":"string"}}}}]`),
		},
	}

	source := store.WorkspaceIntegrationSource{
		ID:          "source-github",
		WorkspaceID: "workspace-1",
		Slug:        "github",
		DisplayName: "GitHub",
		Kind:        "github",
		Importer:    "github",
	}
	mainLegacyID := "integration-main"
	secondaryLegacyID := "integration-secondary"
	sharedLegacyToolID := "github.echo"
	now := time.Now().UTC()
	const blockedAddress = "wi.workspace-1.github.github-main.echo"
	const allowedAddress = "wi.workspace-1.github.github-secondary.echo"
	fakeStore.connectorProjections["workspace-1"] = []store.WorkspaceIntegrationConnectorProjection{
		{
			Source: source,
			Connection: store.WorkspaceIntegrationConnection{
				ID:                  "connection-main",
				WorkspaceID:         "workspace-1",
				SourceID:            "source-github",
				LegacyIntegrationID: &mainLegacyID,
				Slug:                "github-main",
				DisplayName:         "GitHub Main",
				Scope:               workspaceIntegrationConnectionScope,
				CredentialState:     "connected",
				Enabled:             true,
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{{
				ID:            "snapshot-main",
				WorkspaceID:   "workspace-1",
				ConnectionID:  "connection-main",
				ToolName:      "echo",
				ToolAddress:   blockedAddress,
				LegacyToolID:  &sharedLegacyToolID,
				Description:   "Echo through main GitHub connection.",
				InputSchema:   json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}}}`),
				Access:        "read",
				Source:        "github",
				ToolsSyncedAt: now,
			}},
			// Block this connection's echo while the sibling connection allows it.
			Policies: []store.WorkspaceIntegrationToolPolicy{{
				WorkspaceID:  "workspace-1",
				ConnectionID: "connection-main",
				ToolName:     "echo",
				Policy:       "block",
				Source:       "api",
			}},
		},
		{
			Source: source,
			Connection: store.WorkspaceIntegrationConnection{
				ID:                  "connection-secondary",
				WorkspaceID:         "workspace-1",
				SourceID:            "source-github",
				LegacyIntegrationID: &secondaryLegacyID,
				Slug:                "github-secondary",
				DisplayName:         "GitHub Secondary",
				Scope:               workspaceIntegrationConnectionScope,
				CredentialState:     "connected",
				Enabled:             true,
			},
			Tools: []store.WorkspaceIntegrationToolSnapshot{{
				ID:            "snapshot-secondary",
				WorkspaceID:   "workspace-1",
				ConnectionID:  "connection-secondary",
				ToolName:      "echo",
				ToolAddress:   allowedAddress,
				LegacyToolID:  &sharedLegacyToolID,
				Description:   "Echo through secondary GitHub connection.",
				InputSchema:   json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}}}`),
				Access:        "read",
				Source:        "github",
				ToolsSyncedAt: now,
			}},
		},
	}

	// 1. Search must surface only the allowed connection's echo, never the blocked one.
	searchReq := workspaceIntegrationCallRequest(t, workspaceIntegrationSearchToolsName, map[string]interface{}{
		"arguments": map[string]interface{}{"query": "echo", "limit": 10},
	})
	searchReq.Header.Set("Authorization", authHeader)
	searchW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(searchW, searchReq)
	if searchW.Code != http.StatusOK {
		t.Fatalf("search expected 200, got %d: %s", searchW.Code, searchW.Body.String())
	}
	var searchResp struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(searchW.Body.Bytes(), &searchResp); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	echoAddresses := map[string]bool{}
	for _, item := range searchResp.Items {
		if item["name"] == "echo" {
			echoAddresses[fmt.Sprint(item["tool_address"])] = true
		}
	}
	if !echoAddresses[allowedAddress] {
		t.Fatalf("search omitted the allowed connection echo tool: %+v", searchResp.Items)
	}
	if echoAddresses[blockedAddress] {
		t.Fatalf("search leaked the blocked connection echo tool: %+v", searchResp.Items)
	}

	// 2. Describe must succeed for the allowed connection's tool_address.
	describeAllowed := workspaceIntegrationCallRequest(t, workspaceIntegrationDescribeToolName, map[string]interface{}{
		"arguments": map[string]interface{}{"tool_address": allowedAddress},
	})
	describeAllowed.Header.Set("Authorization", authHeader)
	describeAllowedW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(describeAllowedW, describeAllowed)
	if describeAllowedW.Code != http.StatusOK {
		t.Fatalf("describe allowed expected 200, got %d: %s", describeAllowedW.Code, describeAllowedW.Body.String())
	}

	// 3. Describe must NOT reveal the blocked connection's tool_address.
	describeBlocked := workspaceIntegrationCallRequest(t, workspaceIntegrationDescribeToolName, map[string]interface{}{
		"arguments": map[string]interface{}{"tool_address": blockedAddress},
	})
	describeBlocked.Header.Set("Authorization", authHeader)
	describeBlockedW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(describeBlockedW, describeBlocked)
	if describeBlockedW.Code == http.StatusOK {
		t.Fatalf("describe blocked tool_address must not succeed: %s", describeBlockedW.Body.String())
	}

	// 4. Call must route to the allowed connection and dispatch its mock tool.
	callAllowed := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": allowedAddress,
			"arguments":    map[string]interface{}{"message": "route-secondary"},
		},
	})
	callAllowed.Header.Set("Authorization", authHeader)
	callAllowedW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(callAllowedW, callAllowed)
	if callAllowedW.Code != http.StatusOK {
		t.Fatalf("call allowed expected 200, got %d: %s", callAllowedW.Code, callAllowedW.Body.String())
	}
	if !strings.Contains(callAllowedW.Body.String(), `"integration_slug":"github-secondary"`) {
		t.Fatalf("call allowed dispatched to wrong connection: %s", callAllowedW.Body.String())
	}

	// 5. Call by the blocked connection's tool_address must be denied before dispatch.
	callBlocked := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": blockedAddress,
			"arguments":    map[string]interface{}{"message": "route-main"},
		},
	})
	callBlocked.Header.Set("Authorization", authHeader)
	callBlockedW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(callBlockedW, callBlocked)
	if callBlockedW.Code == http.StatusOK {
		t.Fatalf("call blocked tool_address must not succeed: %s", callBlockedW.Body.String())
	}
	if strings.Contains(callBlockedW.Body.String(), `"integration_slug":"github-main"`) {
		t.Fatalf("call blocked tool_address bypassed policy and dispatched: %s", callBlockedW.Body.String())
	}
}

func TestWorkspaceIntegrationDiscoveryFacade_RESTEndToEndWithTestMCP(t *testing.T) {
	mcp := newDeterministicWorkspaceIntegrationMCPServer(t)
	defer mcp.Close()
	testIntegration := createDeterministicTestMCPIntegrationViaCustomPath(t, mcp.URL)

	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{testIntegration}
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	listReq := httptest.NewRequest(http.MethodGet, "/api/ocm-integrations/tools", nil)
	listReq.Header.Set("Authorization", authHeader)
	listW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationListTools(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("tools/list expected 200, got %d: %s", listW.Code, listW.Body.String())
	}
	var listResp struct {
		Tools []workspaceIntegrationTool `json:"tools"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode tools list: %v", err)
	}
	names := workspaceIntegrationToolNameSet(listResp.Tools)
	for _, want := range []string{workspaceIntegrationSearchToolsName, workspaceIntegrationDescribeToolName, workspaceIntegrationCallToolName, "test-mcp.echo", "test-mcp.create_record"} {
		if !names[want] {
			t.Fatalf("tools = %+v, missing %s", listResp.Tools, want)
		}
	}

	searchReq := workspaceIntegrationCallRequest(t, workspaceIntegrationSearchToolsName, map[string]interface{}{
		"arguments": map[string]interface{}{"query": "echo stable", "integration": "test-mcp", "limit": 5},
	})
	searchReq.Header.Set("Authorization", authHeader)
	searchW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(searchW, searchReq)
	if searchW.Code != http.StatusOK {
		t.Fatalf("search expected 200, got %d: %s", searchW.Code, searchW.Body.String())
	}
	if strings.Contains(searchW.Body.String(), "input_schema") || strings.Contains(searchW.Body.String(), "properties") {
		t.Fatalf("search response must stay compact and schema-free: %s", searchW.Body.String())
	}
	var searchResp struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(searchW.Body.Bytes(), &searchResp); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if len(searchResp.Items) == 0 || searchResp.Items[0]["tool_id"] != "test-mcp.echo" {
		t.Fatalf("search items = %+v, want test-mcp.echo first", searchResp.Items)
	}

	describeReq := workspaceIntegrationCallRequest(t, workspaceIntegrationDescribeToolName, map[string]interface{}{
		"arguments": map[string]interface{}{"tool_id": "test-mcp.echo"},
	})
	describeReq.Header.Set("Authorization", authHeader)
	describeW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(describeW, describeReq)
	if describeW.Code != http.StatusOK {
		t.Fatalf("describe expected 200, got %d: %s", describeW.Code, describeW.Body.String())
	}
	var describeResp struct {
		ToolID      string                 `json:"tool_id"`
		InputSchema map[string]interface{} `json:"input_schema"`
		AuthState   string                 `json:"auth_state"`
		Policy      string                 `json:"policy"`
	}
	if err := json.Unmarshal(describeW.Body.Bytes(), &describeResp); err != nil {
		t.Fatalf("decode describe: %v", err)
	}
	if describeResp.ToolID != "test-mcp.echo" || describeResp.Policy != "allowed" || describeResp.AuthState != "connected" {
		t.Fatalf("describe = %+v", describeResp)
	}
	properties, _ := describeResp.InputSchema["properties"].(map[string]interface{})
	if _, ok := properties["message"]; !ok {
		t.Fatalf("describe input_schema = %+v, want exact probed message schema", describeResp.InputSchema)
	}

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_id":   "test-mcp.echo",
			"arguments": map[string]interface{}{"message": "hello"},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	logs := captureWorkspaceIntegrationLogs(t, func() {
		s.handleWorkspaceIntegrationCallTool(callW, callReq)
	})
	if callW.Code != http.StatusOK {
		t.Fatalf("call expected 200, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"tool_id":"test-mcp.echo"`) || !strings.Contains(callW.Body.String(), `"message":"hello"`) {
		t.Fatalf("call response = %s", callW.Body.String())
	}
	if !strings.Contains(logs, "call_mode=facade") || !strings.Contains(logs, "tool=test-mcp.echo") {
		t.Fatalf("facade call logs = %s", logs)
	}

	errorReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_id": "test-mcp.create_record",
			"arguments": map[string]interface{}{
				"title":       "bad",
				"force_error": true,
			},
		},
	})
	errorReq.Header.Set("Authorization", authHeader)
	errorW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(errorW, errorReq)
	if errorW.Code != http.StatusBadGateway {
		t.Fatalf("forced upstream error expected 502, got %d: %s", errorW.Code, errorW.Body.String())
	}
	if !strings.Contains(errorW.Body.String(), "the remote MCP server returned an error") {
		t.Fatalf("forced upstream error response = %s", errorW.Body.String())
	}

	testIntegration.DeniedTools = []string{"echo"}
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{testIntegration}
	searchW = httptest.NewRecorder()
	searchReq = workspaceIntegrationCallRequest(t, workspaceIntegrationSearchToolsName, map[string]interface{}{
		"arguments": map[string]interface{}{"query": "echo", "integration": "test-mcp"},
	})
	searchReq.Header.Set("Authorization", authHeader)
	s.handleWorkspaceIntegrationCallTool(searchW, searchReq)
	if searchW.Code != http.StatusOK {
		t.Fatalf("denied search expected 200, got %d: %s", searchW.Code, searchW.Body.String())
	}
	if strings.Contains(searchW.Body.String(), "test-mcp.echo") {
		t.Fatalf("denied tool appeared in search: %s", searchW.Body.String())
	}
	describeW = httptest.NewRecorder()
	describeReq = workspaceIntegrationCallRequest(t, workspaceIntegrationDescribeToolName, map[string]interface{}{
		"arguments": map[string]interface{}{"tool_id": "test-mcp.echo"},
	})
	describeReq.Header.Set("Authorization", authHeader)
	s.handleWorkspaceIntegrationCallTool(describeW, describeReq)
	if describeW.Code != http.StatusNotFound {
		t.Fatalf("denied describe expected 404, got %d: %s", describeW.Code, describeW.Body.String())
	}
	deniedCallReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_id":   "test-mcp.echo",
			"arguments": map[string]interface{}{"message": "blocked"},
		},
	})
	deniedCallReq.Header.Set("Authorization", authHeader)
	deniedCallW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(deniedCallW, deniedCallReq)
	if deniedCallW.Code != http.StatusNotFound {
		t.Fatalf("denied facade call expected 404, got %d: %s", deniedCallW.Code, deniedCallW.Body.String())
	}
}

func TestWorkspaceIntegrationFacadeProjectionStructuredAddress(t *testing.T) {
	integration := store.WorkspaceIntegration{
		ID:           "integration-gh-main",
		WorkspaceID:  "workspace-1",
		Slug:         "github-main",
		DisplayName:  "GitHub Main",
		Kind:         "github",
		Transport:    "mock",
		Enabled:      true,
		ToolManifest: json.RawMessage(`[{"name":"echo","description":"Echo repository input","parameters":{"type":"object","properties":{"message":{"type":"string"}}}}]`),
	}

	descriptors := buildWorkspaceIntegrationToolDescriptors([]store.WorkspaceIntegration{integration}, false)
	if len(descriptors) != 1 {
		t.Fatalf("descriptors = %+v, want one descriptor", descriptors)
	}
	descriptor := descriptors[0]
	if descriptor.ToolID != "github-main.echo" {
		t.Fatalf("ToolID = %q", descriptor.ToolID)
	}
	if descriptor.LegacyToolID != "github-main.echo" {
		t.Fatalf("LegacyToolID = %q", descriptor.LegacyToolID)
	}
	if descriptor.ToolAddress != "wi.workspace-1.github.github-main.echo" {
		t.Fatalf("ToolAddress = %q", descriptor.ToolAddress)
	}
	if descriptor.SourceSlug != "github" || descriptor.ConnectionSlug != "github-main" || descriptor.ConnectionScope != "workspace" {
		t.Fatalf("projection source/connection = %+v", descriptor)
	}
	if descriptor.SnapshotID != "v1:github-main:echo" {
		t.Fatalf("SnapshotID = %q", descriptor.SnapshotID)
	}

	found, ok := describeWorkspaceIntegrationFacadeTool([]store.WorkspaceIntegration{integration}, descriptor.ToolAddress)
	if !ok {
		t.Fatal("describe by structured address did not find tool")
	}
	if found.ToolID != descriptor.ToolID || found.ToolAddress != descriptor.ToolAddress {
		t.Fatalf("describe by structured address = %+v", found)
	}

	resolvedIntegration, resolvedTool, ok := findWorkspaceIntegrationTool([]store.WorkspaceIntegration{integration}, descriptor.ToolAddress)
	if !ok {
		t.Fatal("find by structured address did not resolve")
	}
	if resolvedIntegration.Slug != "github-main" || resolvedTool.Name != "echo" {
		t.Fatalf("resolved integration/tool = %s/%s", resolvedIntegration.Slug, resolvedTool.Name)
	}

	legacyAddress := "wi.workspace.github.github-main.echo"
	resolvedIntegration, resolvedTool, ok = findWorkspaceIntegrationTool([]store.WorkspaceIntegration{integration}, legacyAddress)
	if !ok {
		t.Fatal("find by legacy structured address did not resolve")
	}
	if resolvedIntegration.Slug != "github-main" || resolvedTool.Name != "echo" {
		t.Fatalf("legacy resolved integration/tool = %s/%s", resolvedIntegration.Slug, resolvedTool.Name)
	}
}

func TestWorkspaceIntegrationFacadeGoogleStructuredAddressUsesServiceSource(t *testing.T) {
	integration := store.WorkspaceIntegration{
		ID:          "google-user",
		WorkspaceID: "workspace-1",
		Slug:        "google-user-example-com",
		DisplayName: "Google Workspace user@example.com",
		Kind:        "google_workspace",
		Transport:   "rest",
		Enabled:     true,
		ToolManifest: googleWorkspaceToolManifest(map[string]string{
			"gmail":    "read",
			"drive":    "read",
			"calendar": "off",
			"docs":     "off",
		}),
		Config: json.RawMessage(`{"email":"user@example.com"}`),
	}

	descriptors := buildWorkspaceIntegrationToolDescriptors([]store.WorkspaceIntegration{integration}, false)
	byName := map[string]workspaceIntegrationToolDescriptor{}
	for _, descriptor := range descriptors {
		byName[descriptor.Name] = descriptor
	}
	gmail := byName["gmail_profile"]
	if gmail.SourceSlug != "gmail" || gmail.ToolAddress != "wi.workspace-1.gmail.google-user-example-com.gmail_profile" || gmail.IntegrationName != "Gmail - user@example.com" {
		t.Fatalf("gmail descriptor = %+v", gmail)
	}
	drive := byName["drive_list_files"]
	if drive.SourceSlug != "google-drive" || drive.ToolAddress != "wi.workspace-1.google-drive.google-user-example-com.drive_list_files" || drive.IntegrationName != "Google Drive - user@example.com" {
		t.Fatalf("drive descriptor = %+v", drive)
	}

	resolvedIntegration, resolvedTool, ok := findWorkspaceIntegrationTool([]store.WorkspaceIntegration{integration}, gmail.ToolAddress)
	if !ok {
		t.Fatal("find by google service structured address did not resolve")
	}
	if resolvedIntegration.Slug != "google-user-example-com" || resolvedTool.Name != "gmail_profile" {
		t.Fatalf("resolved integration/tool = %s/%s", resolvedIntegration.Slug, resolvedTool.Name)
	}
}

func TestWorkspaceIntegrationFacadeGoogleProjectionRewritesServiceSource(t *testing.T) {
	legacyID := "google-user"
	projection := store.WorkspaceIntegrationConnectorProjection{
		Source: store.WorkspaceIntegrationSource{
			ID:          "source-google-workspace",
			WorkspaceID: "workspace-1",
			Slug:        "google-workspace",
			DisplayName: "Google Workspace",
			Kind:        "google_workspace",
			Importer:    "http",
		},
		Connection: store.WorkspaceIntegrationConnection{
			ID:                  "connection-google-user",
			WorkspaceID:         "workspace-1",
			SourceID:            "source-google-workspace",
			LegacyIntegrationID: &legacyID,
			Slug:                "google-user-example-com",
			DisplayName:         "Google Workspace user@example.com",
			Scope:               workspaceIntegrationConnectionScope,
			CredentialState:     "connected",
			Enabled:             true,
		},
		Tools: []store.WorkspaceIntegrationToolSnapshot{
			{
				ID:            "snapshot-gmail-profile",
				WorkspaceID:   "workspace-1",
				ConnectionID:  "connection-google-user",
				ToolName:      "gmail_profile",
				ToolAddress:   "wi.workspace-1.google-workspace.google-user-example-com.gmail-profile",
				LegacyToolID:  stringPtr("google-user-example-com.gmail_profile"),
				Description:   "Get the connected Gmail profile summary",
				InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false}`),
				Access:        "read",
				Source:        "http",
				ToolsSyncedAt: time.Now().UTC(),
			},
		},
	}

	descriptors := buildWorkspaceIntegrationToolDescriptorsFromConnectorProjections([]store.WorkspaceIntegrationConnectorProjection{projection}, false)
	if len(descriptors) != 1 {
		t.Fatalf("descriptors = %+v, want one descriptor", descriptors)
	}
	descriptor := descriptors[0]
	if descriptor.SourceSlug != "gmail" || descriptor.Source != "gmail" || descriptor.ToolAddress != "wi.workspace-1.gmail.google-user-example-com.gmail_profile" {
		t.Fatalf("google projection descriptor did not use service source: %+v", descriptor)
	}
}

func TestWorkspaceIntegrationFacadeStructuredAddressCallAndApprovalPolicy(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	integration := fakeStore.integrations["machine-123"][0]
	integration.Config = json.RawMessage(`{"tool_policy":{"echo":"require_approval"}}`)
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{integration}
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	searchReq := workspaceIntegrationCallRequest(t, workspaceIntegrationSearchToolsName, map[string]interface{}{
		"arguments": map[string]interface{}{"query": "echo", "integration": "mock", "limit": 1},
	})
	searchReq.Header.Set("Authorization", authHeader)
	searchW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(searchW, searchReq)
	if searchW.Code != http.StatusOK {
		t.Fatalf("search expected 200, got %d: %s", searchW.Code, searchW.Body.String())
	}
	var searchResp struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(searchW.Body.Bytes(), &searchResp); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if len(searchResp.Items) != 1 {
		t.Fatalf("search items = %+v", searchResp.Items)
	}
	toolAddress, _ := searchResp.Items[0]["tool_address"].(string)
	if toolAddress != "wi.workspace-1.mock.mock.echo" {
		t.Fatalf("tool_address = %q", toolAddress)
	}
	if got := searchResp.Items[0]["policy_state"]; got != workspaceIntegrationPolicyRequireApproval {
		t.Fatalf("policy_state = %v", got)
	}
	if got := searchResp.Items[0]["policy"]; got != "approval_required" {
		t.Fatalf("policy = %v", got)
	}

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": toolAddress,
			"arguments":    map[string]interface{}{"message": "needs approval"},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusConflict {
		t.Fatalf("approval-required call expected 409, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), "requires human approval") {
		t.Fatalf("approval-required response = %s", callW.Body.String())
	}

	integration.Config = json.RawMessage(`{"tool_policy":{"echo":"allow"}}`)
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{integration}
	callReq = workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": toolAddress,
			"arguments":    map[string]interface{}{"message": "needs approval"},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW = httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusOK {
		t.Fatalf("structured address call expected 200 after allow, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"message":"needs approval"`) {
		t.Fatalf("structured address call response = %s", callW.Body.String())
	}
}

func TestWorkspaceIntegrationDiscoveryFacade_NativeMCPBridgeWithTestMCP(t *testing.T) {
	mcp := newDeterministicWorkspaceIntegrationMCPServer(t)
	defer mcp.Close()
	testIntegration := createDeterministicTestMCPIntegrationViaCustomPath(t, mcp.URL)

	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{testIntegration}
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	listReq := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	listReq.Header.Set("Authorization", authHeader)
	listW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("mcp tools/list expected 200, got %d: %s", listW.Code, listW.Body.String())
	}
	if !strings.Contains(listW.Body.String(), workspaceIntegrationSearchToolsName) || !strings.Contains(listW.Body.String(), workspaceIntegrationDescribeToolName) || !strings.Contains(listW.Body.String(), workspaceIntegrationCallToolName) {
		t.Fatalf("mcp tools/list missing facade tools: %s", listW.Body.String())
	}
	if strings.Contains(listW.Body.String(), "test-mcp.echo") || strings.Contains(listW.Body.String(), "test-mcp.create_record") {
		t.Fatalf("mcp tools/list exposed direct provider tools instead of facade-only surface: %s", listW.Body.String())
	}

	searchReq := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ocm.search_tools","arguments":{"query":"echo stable","integration":"test-mcp","limit":5}}}`))
	searchReq.Header.Set("Authorization", authHeader)
	searchW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(searchW, searchReq)
	if searchW.Code != http.StatusOK {
		t.Fatalf("mcp search expected 200, got %d: %s", searchW.Code, searchW.Body.String())
	}
	if strings.Contains(searchW.Body.String(), "input_schema") {
		t.Fatalf("mcp search response = %s", searchW.Body.String())
	}
	var searchResp struct {
		Error  *workspaceIntegrationMCPError `json:"error,omitempty"`
		Result struct {
			StructuredContent struct {
				Items []map[string]interface{} `json:"items"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(searchW.Body.Bytes(), &searchResp); err != nil {
		t.Fatalf("decode mcp search: %v", err)
	}
	if searchResp.Error != nil {
		t.Fatalf("mcp search error = %+v", searchResp.Error)
	}
	if len(searchResp.Result.StructuredContent.Items) == 0 || searchResp.Result.StructuredContent.Items[0]["tool_id"] != "test-mcp.echo" {
		t.Fatalf("mcp search items = %+v, want test-mcp.echo first", searchResp.Result.StructuredContent.Items)
	}
	toolAddress, _ := searchResp.Result.StructuredContent.Items[0]["tool_address"].(string)
	if toolAddress == "" {
		t.Fatalf("mcp search item missing tool_address: %+v", searchResp.Result.StructuredContent.Items[0])
	}

	describeBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": "ocm.describe_tool",
			"arguments": map[string]interface{}{
				"tool_address": toolAddress,
			},
		},
	})
	if err != nil {
		t.Fatalf("encode mcp describe: %v", err)
	}
	describeReq := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", bytes.NewReader(describeBody))
	describeReq.Header.Set("Authorization", authHeader)
	describeW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(describeW, describeReq)
	if describeW.Code != http.StatusOK {
		t.Fatalf("mcp describe expected 200, got %d: %s", describeW.Code, describeW.Body.String())
	}
	if !strings.Contains(describeW.Body.String(), `"tool_id":"test-mcp.echo"`) || !strings.Contains(describeW.Body.String(), `"tool_address":"`+toolAddress+`"`) || !strings.Contains(describeW.Body.String(), `"message"`) {
		t.Fatalf("mcp describe response = %s", describeW.Body.String())
	}

	callBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": "ocm.call_tool",
			"arguments": map[string]interface{}{
				"tool_address": toolAddress,
				"arguments":    map[string]interface{}{"message": "mcp"},
			},
		},
	})
	if err != nil {
		t.Fatalf("encode mcp call: %v", err)
	}
	callReq := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", bytes.NewReader(callBody))
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(callW, callReq)
	if callW.Code != http.StatusOK {
		t.Fatalf("mcp call expected 200, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"tool_id":"test-mcp.echo"`) || !strings.Contains(callW.Body.String(), `"tool_address":"`+toolAddress+`"`) || !strings.Contains(callW.Body.String(), `"message":"mcp"`) {
		t.Fatalf("mcp call response = %s", callW.Body.String())
	}

	createRecordAddress := workspaceIntegrationStructuredToolAddress(testIntegration, workspaceIntegrationManifestTool{Name: "create_record"})
	errorBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": "ocm.call_tool",
			"arguments": map[string]interface{}{
				"tool_address": createRecordAddress,
				"arguments": map[string]interface{}{
					"title":       "bad",
					"force_error": true,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("encode mcp upstream error call: %v", err)
	}
	errorReq := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", bytes.NewReader(errorBody))
	errorReq.Header.Set("Authorization", authHeader)
	errorW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(errorW, errorReq)
	if errorW.Code != http.StatusOK {
		t.Fatalf("mcp upstream error call expected JSON-RPC 200, got %d: %s", errorW.Code, errorW.Body.String())
	}
	if !strings.Contains(errorW.Body.String(), `"error"`) || !strings.Contains(errorW.Body.String(), "the remote MCP server returned an error") {
		t.Fatalf("mcp upstream error response = %s", errorW.Body.String())
	}

	testIntegration.DeniedTools = []string{"echo"}
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{testIntegration}
	deniedSearchReq := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"ocm.search_tools","arguments":{"query":"echo","integration":"test-mcp","limit":5}}}`))
	deniedSearchReq.Header.Set("Authorization", authHeader)
	deniedSearchW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(deniedSearchW, deniedSearchReq)
	if deniedSearchW.Code != http.StatusOK {
		t.Fatalf("mcp denied search expected 200, got %d: %s", deniedSearchW.Code, deniedSearchW.Body.String())
	}
	if strings.Contains(deniedSearchW.Body.String(), "test-mcp.echo") {
		t.Fatalf("mcp denied search exposed blocked tool: %s", deniedSearchW.Body.String())
	}

	deniedDescribeBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      7,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": "ocm.describe_tool",
			"arguments": map[string]interface{}{
				"tool_address": toolAddress,
			},
		},
	})
	if err != nil {
		t.Fatalf("encode mcp denied describe: %v", err)
	}
	deniedDescribeReq := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", bytes.NewReader(deniedDescribeBody))
	deniedDescribeReq.Header.Set("Authorization", authHeader)
	deniedDescribeW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(deniedDescribeW, deniedDescribeReq)
	if deniedDescribeW.Code != http.StatusOK {
		t.Fatalf("mcp denied describe expected JSON-RPC 200, got %d: %s", deniedDescribeW.Code, deniedDescribeW.Body.String())
	}
	if !strings.Contains(deniedDescribeW.Body.String(), `"error"`) || !strings.Contains(deniedDescribeW.Body.String(), "tool not found") {
		t.Fatalf("mcp denied describe response = %s", deniedDescribeW.Body.String())
	}

	deniedCallBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      8,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": "ocm.call_tool",
			"arguments": map[string]interface{}{
				"tool_address": toolAddress,
				"arguments":    map[string]interface{}{"message": "blocked"},
			},
		},
	})
	if err != nil {
		t.Fatalf("encode mcp denied call: %v", err)
	}
	deniedCallReq := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", bytes.NewReader(deniedCallBody))
	deniedCallReq.Header.Set("Authorization", authHeader)
	deniedCallW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(deniedCallW, deniedCallReq)
	if deniedCallW.Code != http.StatusOK {
		t.Fatalf("mcp denied call expected JSON-RPC 200, got %d: %s", deniedCallW.Code, deniedCallW.Body.String())
	}
	if !strings.Contains(deniedCallW.Body.String(), `"error"`) || !strings.Contains(deniedCallW.Body.String(), "tool not found") {
		t.Fatalf("mcp denied call response = %s", deniedCallW.Body.String())
	}
}

func TestWorkspaceIntegrationMCPBridgeExecuteReadOnlyFixture(t *testing.T) {
	mcp := newDeterministicWorkspaceIntegrationMCPServer(t)
	defer mcp.Close()
	testIntegration := createDeterministicTestMCPIntegrationViaCustomPath(t, mcp.URL)

	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	s.workspaceIntegrationExecuteEnabled = true
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{testIntegration}
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	listReq := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	listReq.Header.Set("Authorization", authHeader)
	listW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("mcp tools/list expected 200, got %d: %s", listW.Code, listW.Body.String())
	}
	if !strings.Contains(listW.Body.String(), workspaceIntegrationExecuteToolName) {
		t.Fatalf("tools/list missing execute tool when flag enabled: %s", listW.Body.String())
	}

	executeBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": workspaceIntegrationExecuteToolName,
			"arguments": map[string]interface{}{
				"script": `
					const found = tools.search({query: "echo stable", integration: "test-mcp", limit: 5});
					const address = found.items[0].tool_address;
					const described = tools.describe(address);
					const called = tools.call(address, {message: "from execute"});
					return {tool_id: described.tool_id, message: called.result.structuredContent.message};
				`,
			},
		},
	})
	if err != nil {
		t.Fatalf("encode execute call: %v", err)
	}
	executeReq := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", bytes.NewReader(executeBody))
	executeReq.Header.Set("Authorization", authHeader)
	executeW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(executeW, executeReq)
	if executeW.Code != http.StatusOK {
		t.Fatalf("mcp execute expected JSON-RPC 200, got %d: %s", executeW.Code, executeW.Body.String())
	}
	for _, want := range []string{`"execution_id"`, `"script_hash"`, `"tool_id":"test-mcp.echo"`, `"message":"from execute"`, `"name":"ocm.call_tool"`} {
		if !strings.Contains(executeW.Body.String(), want) {
			t.Fatalf("mcp execute response missing %q: %s", want, executeW.Body.String())
		}
	}
}

func TestWorkspaceIntegrationMCPBridgeExecuteRejectsWriteTool(t *testing.T) {
	mcp := newDeterministicWorkspaceIntegrationMCPServer(t)
	defer mcp.Close()
	testIntegration := createDeterministicTestMCPIntegrationViaCustomPath(t, mcp.URL)
	writeAddress := workspaceIntegrationStructuredToolAddress(testIntegration, workspaceIntegrationManifestTool{Name: "create_record"})

	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	s.workspaceIntegrationExecuteEnabled = true
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{testIntegration}
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	executeBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": workspaceIntegrationExecuteToolName,
			"arguments": map[string]interface{}{
				"script": fmt.Sprintf(`return tools.call(%q, {title: "blocked"});`, writeAddress),
			},
		},
	})
	if err != nil {
		t.Fatalf("encode execute write call: %v", err)
	}
	executeReq := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", bytes.NewReader(executeBody))
	executeReq.Header.Set("Authorization", authHeader)
	executeW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(executeW, executeReq)
	if executeW.Code != http.StatusOK {
		t.Fatalf("mcp execute expected JSON-RPC 200, got %d: %s", executeW.Code, executeW.Body.String())
	}
	if !strings.Contains(executeW.Body.String(), `"error"`) || !strings.Contains(executeW.Body.String(), "ocm.execute can only call read tools") {
		t.Fatalf("execute write call should be rejected before dispatch: %s", executeW.Body.String())
	}
	if strings.Contains(executeW.Body.String(), `"rec_123"`) {
		t.Fatalf("execute write call dispatched unexpectedly: %s", executeW.Body.String())
	}
}

func TestWorkspaceIntegrationMCPBridgeExecuteHasNoRawIOGlobals(t *testing.T) {
	s, _ := newWorkspaceIntegrationGatewayServer()
	s.workspaceIntegrationExecuteEnabled = true
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	executeBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ocm.execute","arguments":{"script":"const escape = globalThis.constructor.constructor; return {process: typeof process, require: typeof require, fetch: typeof fetch, XMLHttpRequest: typeof XMLHttpRequest, WebSocket: typeof WebSocket, Deno: typeof Deno, Bun: typeof Bun, open: typeof open, exec: typeof exec, spawn: typeof spawn, readFile: typeof readFile, escapedProcess: escape('return typeof process')(), escapedRequire: escape('return typeof require')(), escapedFetch: escape('return typeof fetch')()};"}}}`
	executeReq := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(executeBody))
	executeReq.Header.Set("Authorization", authHeader)
	executeW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(executeW, executeReq)
	if executeW.Code != http.StatusOK {
		t.Fatalf("mcp execute expected JSON-RPC 200, got %d: %s", executeW.Code, executeW.Body.String())
	}
	for _, want := range []string{
		`"process":"undefined"`,
		`"require":"undefined"`,
		`"fetch":"undefined"`,
		`"XMLHttpRequest":"undefined"`,
		`"WebSocket":"undefined"`,
		`"Deno":"undefined"`,
		`"Bun":"undefined"`,
		`"open":"undefined"`,
		`"exec":"undefined"`,
		`"spawn":"undefined"`,
		`"readFile":"undefined"`,
		`"escapedProcess":"undefined"`,
		`"escapedRequire":"undefined"`,
		`"escapedFetch":"undefined"`,
	} {
		if !strings.Contains(executeW.Body.String(), want) {
			t.Fatalf("execute sandbox raw IO global check missing %q: %s", want, executeW.Body.String())
		}
	}
}

func TestWorkspaceIntegrationExecuteEnforcesRuntimeLimits(t *testing.T) {
	s, _ := newWorkspaceIntegrationGatewayServer()
	s.workspaceIntegrationExecuteEnabled = true

	_, status, err := s.callWorkspaceIntegrationExecute(context.Background(), "machine-123", nil, map[string]interface{}{
		"script": strings.Repeat("x", workspaceIntegrationExecuteMaxScriptBytes+1),
	})
	if status != http.StatusBadRequest || err == nil || !strings.Contains(err.Error(), "script exceeds") {
		t.Fatalf("oversized script status=%d err=%v", status, err)
	}

	_, status, err = s.callWorkspaceIntegrationExecute(context.Background(), "machine-123", nil, map[string]interface{}{
		"script": fmt.Sprintf(`return "x".repeat(%d);`, workspaceIntegrationExecuteMaxOutputBytes+1),
	})
	if status != http.StatusBadRequest || err == nil || !strings.Contains(err.Error(), "output exceeds") {
		t.Fatalf("oversized output status=%d err=%v", status, err)
	}

	_, status, err = s.callWorkspaceIntegrationExecute(context.Background(), "machine-123", nil, map[string]interface{}{
		"script": fmt.Sprintf(`for (let i = 0; i < %d; i++) { tools.search({query: ""}); } return true;`, workspaceIntegrationExecuteMaxToolCalls+1),
	})
	if status != http.StatusBadRequest || err == nil || !strings.Contains(err.Error(), "tool call limit") {
		t.Fatalf("tool call limit status=%d err=%v", status, err)
	}

	prevTimeout := workspaceIntegrationExecuteWallTimeout
	workspaceIntegrationExecuteWallTimeout = 10 * time.Millisecond
	t.Cleanup(func() {
		workspaceIntegrationExecuteWallTimeout = prevTimeout
	})
	_, status, err = s.callWorkspaceIntegrationExecute(context.Background(), "machine-123", nil, map[string]interface{}{
		"script": `while (true) {}`,
	})
	if status != http.StatusBadRequest || err == nil || !strings.Contains(err.Error(), "ocm.execute timed out") {
		t.Fatalf("timeout status=%d err=%v", status, err)
	}
}

func TestWorkspaceIntegrationMCPBridgeExecuteEmitsTraceWithoutRawScript(t *testing.T) {
	mcp := newDeterministicWorkspaceIntegrationMCPServer(t)
	defer mcp.Close()
	testIntegration := createDeterministicTestMCPIntegrationViaCustomPath(t, mcp.URL)
	readAddress := workspaceIntegrationStructuredToolAddress(testIntegration, workspaceIntegrationManifestTool{Name: "echo"})
	writeAddress := workspaceIntegrationStructuredToolAddress(testIntegration, workspaceIntegrationManifestTool{Name: "create_record"})

	var tracesMu sync.Mutex
	traces := []workspaceIntegrationExecuteTrace{}
	prevTraceSink := workspaceIntegrationExecuteTraceSink
	workspaceIntegrationExecuteTraceSink = func(_ context.Context, trace workspaceIntegrationExecuteTrace) {
		tracesMu.Lock()
		defer tracesMu.Unlock()
		traces = append(traces, trace)
	}
	t.Cleanup(func() {
		workspaceIntegrationExecuteTraceSink = prevTraceSink
	})

	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	s.workspaceIntegrationExecuteEnabled = true
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{testIntegration}
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	rawScriptSecret := "raw-script-secret-123"
	executeBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": workspaceIntegrationExecuteToolName,
			"arguments": map[string]interface{}{
				"script": fmt.Sprintf(`const hidden = %q; return tools.call(%q, {message: "trace"});`, rawScriptSecret, readAddress),
			},
		},
	})
	if err != nil {
		t.Fatalf("encode execute trace success call: %v", err)
	}
	executeReq := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", bytes.NewReader(executeBody))
	executeReq.Header.Set("Authorization", authHeader)
	executeW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(executeW, executeReq)
	if executeW.Code != http.StatusOK || strings.Contains(executeW.Body.String(), `"error"`) {
		t.Fatalf("mcp execute expected success, got %d: %s", executeW.Code, executeW.Body.String())
	}

	executeBody, err = json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": workspaceIntegrationExecuteToolName,
			"arguments": map[string]interface{}{
				"script": fmt.Sprintf(`return tools.call(%q, {title: "blocked"});`, writeAddress),
			},
		},
	})
	if err != nil {
		t.Fatalf("encode execute trace denial call: %v", err)
	}
	executeReq = httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", bytes.NewReader(executeBody))
	executeReq.Header.Set("Authorization", authHeader)
	executeW = httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(executeW, executeReq)
	if executeW.Code != http.StatusOK || !strings.Contains(executeW.Body.String(), `"error"`) {
		t.Fatalf("mcp execute expected policy error, got %d: %s", executeW.Code, executeW.Body.String())
	}

	tracesMu.Lock()
	gotTraces := append([]workspaceIntegrationExecuteTrace(nil), traces...)
	tracesMu.Unlock()
	if len(gotTraces) != 2 {
		t.Fatalf("execute trace count = %d, want 2: %+v", len(gotTraces), gotTraces)
	}
	successTrace := gotTraces[0]
	if successTrace.ExecutionID == "" || successTrace.ScriptHash == "" || successTrace.Status != "success" {
		t.Fatalf("success trace missing execution metadata: %+v", successTrace)
	}
	if len(successTrace.ToolCalls) != 1 {
		t.Fatalf("success trace tool calls = %+v, want one facade call", successTrace.ToolCalls)
	}
	if call := successTrace.ToolCalls[0]; call.Name != workspaceIntegrationCallToolName || call.ToolAddress != readAddress || call.Access != "read" || call.PolicyDecision != "allowed" {
		t.Fatalf("success trace call = %+v, want read allowed call to %s", call, readAddress)
	}
	traceJSON, err := json.Marshal(successTrace)
	if err != nil {
		t.Fatalf("marshal success trace: %v", err)
	}
	if strings.Contains(string(traceJSON), rawScriptSecret) {
		t.Fatalf("execute trace leaked raw script content: %s", traceJSON)
	}

	deniedTrace := gotTraces[1]
	if deniedTrace.Status != "error" || !strings.Contains(deniedTrace.Error, "ocm.execute can only call read tools") {
		t.Fatalf("denied trace missing policy error: %+v", deniedTrace)
	}
	if len(deniedTrace.ToolCalls) != 1 {
		t.Fatalf("denied trace tool calls = %+v, want one denied attempted call", deniedTrace.ToolCalls)
	}
	if call := deniedTrace.ToolCalls[0]; call.ToolAddress != writeAddress || call.Access != "write" || call.PolicyDecision != "read_only_denied" || call.Status != "error" {
		t.Fatalf("denied trace call = %+v, want read-only denial for %s", call, writeAddress)
	}
	if strings.Contains(executeW.Body.String(), `"rec_123"`) {
		t.Fatalf("denied execute call dispatched unexpectedly: %s", executeW.Body.String())
	}
}

func TestWorkspaceIntegrationCallTool_GitHubGetRepo(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octocat/Hello-World" {
			t.Fatalf("unexpected github path %s", r.URL.Path)
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Fatalf("missing github accept header")
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

	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{
		{
			ID:           "github-1",
			WorkspaceID:  "workspace-1",
			Slug:         "github",
			DisplayName:  "GitHub octocat/Hello-World",
			Kind:         "github",
			Transport:    "rest",
			Enabled:      true,
			ToolManifest: githubToolManifest(),
			Config:       json.RawMessage(`{"owner":"octocat","repo":"Hello-World"}`),
		},
	}
	req := workspaceIntegrationCallRequest(t, "github.get_repo", map[string]interface{}{"arguments": map[string]interface{}{}})
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()

	s.handleWorkspaceIntegrationCallTool(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	result := resp["result"].(map[string]interface{})
	if result["full_name"] != "octocat/Hello-World" {
		t.Fatalf("github result = %v", result)
	}
}

func TestWorkspaceIntegrationFacadeGitHubMultipleConnectionsRouteByToolAddress(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/octocat/Hello-World":
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
		case "/repos/mathaix/ocm_cloud":
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"full_name":         "mathaix/ocm_cloud",
				"description":       "OCM Cloud",
				"private":           true,
				"html_url":          "https://github.com/mathaix/ocm_cloud",
				"default_branch":    "main",
				"open_issues_count": 7,
				"stargazers_count":  2,
				"forks_count":       1,
				"updated_at":        "2026-06-17T12:00:00Z",
			})
		default:
			t.Fatalf("unexpected github path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	previousBaseURL := githubAPIBaseURL
	githubAPIBaseURL = api.URL
	defer func() { githubAPIBaseURL = previousBaseURL }()

	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{
		{
			ID:           "github-octocat",
			WorkspaceID:  "workspace-1",
			Slug:         "github-octocat-hello-world",
			DisplayName:  "GitHub octocat/Hello-World",
			Kind:         "github",
			Transport:    "http",
			Enabled:      true,
			ToolManifest: githubToolManifest(),
			Config:       json.RawMessage(`{"owner":"octocat","repo":"Hello-World"}`),
		},
		{
			ID:           "github-mathaix",
			WorkspaceID:  "workspace-1",
			Slug:         "github-mathaix-ocm-cloud",
			DisplayName:  "GitHub mathaix/ocm_cloud",
			Kind:         "github",
			Transport:    "http",
			Enabled:      true,
			ToolManifest: githubToolManifest(),
			Config:       json.RawMessage(`{"owner":"mathaix","repo":"ocm_cloud"}`),
		},
	}
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	searchReq := workspaceIntegrationCallRequest(t, workspaceIntegrationSearchToolsName, map[string]interface{}{
		"arguments": map[string]interface{}{"query": "repo summary", "integration": "github", "limit": 10},
	})
	searchReq.Header.Set("Authorization", authHeader)
	searchW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(searchW, searchReq)
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
		if item["name"] == "get_repo" {
			addresses[fmt.Sprint(item["connection_slug"])] = fmt.Sprint(item["tool_address"])
		}
	}
	if addresses["github-octocat-hello-world"] == "" || addresses["github-mathaix-ocm-cloud"] == "" || addresses["github-octocat-hello-world"] == addresses["github-mathaix-ocm-cloud"] {
		t.Fatalf("search did not return distinct github repo addresses: %+v", searchResp.Items)
	}

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": addresses["github-mathaix-ocm-cloud"],
			"arguments":    map[string]interface{}{},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusOK {
		t.Fatalf("call expected 200, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"full_name":"mathaix/ocm_cloud"`) || strings.Contains(callW.Body.String(), `"full_name":"octocat/Hello-World"`) {
		t.Fatalf("call did not route by selected github tool_address: %s", callW.Body.String())
	}
}

func TestWorkspaceIntegrationCallTool_GoogleWorkspaceGmailProfile(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gmail/v1/users/me/profile" {
			t.Fatalf("unexpected google path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer google-access" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"emailAddress":  "user@example.com",
			"messagesTotal": 42,
			"threadsTotal":  12,
		})
	}))
	defer api.Close()

	previousGmailBaseURL := googleGmailAPIBaseURL
	googleGmailAPIBaseURL = api.URL + "/gmail/v1"
	defer func() { googleGmailAPIBaseURL = previousGmailBaseURL }()

	secretKey := "12345678901234567890123456789012"
	encrypted, err := crypto.Encrypt("google-access", secretKey)
	if err != nil {
		t.Fatalf("encrypt google token: %v", err)
	}

	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	s.secretKey = secretKey
	fakeStore.credential = &store.WorkspaceIntegrationCredential{
		IntegrationID: "google-1",
		SecretEnc:     encrypted,
	}
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{
		{
			ID:           "google-1",
			WorkspaceID:  "workspace-1",
			Slug:         googleWorkspaceIntegrationSlug,
			DisplayName:  "Google Workspace",
			Kind:         "google_workspace",
			Transport:    "rest",
			Enabled:      true,
			ToolManifest: googleWorkspaceToolManifest(nil),
			Config:       json.RawMessage(`{"email":"user@example.com"}`),
		},
	}
	req := workspaceIntegrationCallRequest(t, "google-workspace.gmail_profile", map[string]interface{}{"arguments": map[string]interface{}{}})
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()

	s.handleWorkspaceIntegrationCallTool(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	result := resp["result"].(map[string]interface{})
	if result["emailAddress"] != "user@example.com" {
		t.Fatalf("google result = %v", result)
	}
}

func TestWorkspaceIntegrationFacadeGoogleMultipleAccountsRouteByToolAddress(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gmail/v1/users/me/profile" {
			t.Fatalf("unexpected google path %s", r.URL.Path)
		}
		switch r.Header.Get("Authorization") {
		case "Bearer google-user-access":
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"emailAddress":  "user@example.com",
				"messagesTotal": 10,
				"threadsTotal":  5,
			})
		case "Bearer google-admin-access":
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"emailAddress":  "admin@company.test",
				"messagesTotal": 22,
				"threadsTotal":  11,
			})
		default:
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
	}))
	defer api.Close()

	previousGmailBaseURL := googleGmailAPIBaseURL
	googleGmailAPIBaseURL = api.URL + "/gmail/v1"
	defer func() { googleGmailAPIBaseURL = previousGmailBaseURL }()

	secretKey := "12345678901234567890123456789012"
	userEncrypted, err := crypto.Encrypt("google-user-access", secretKey)
	if err != nil {
		t.Fatalf("encrypt user google token: %v", err)
	}
	adminEncrypted, err := crypto.Encrypt("google-admin-access", secretKey)
	if err != nil {
		t.Fatalf("encrypt admin google token: %v", err)
	}

	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	s.secretKey = secretKey
	fakeStore.credentials = map[string]store.WorkspaceIntegrationCredential{
		"google-user": {
			IntegrationID: "google-user",
			SecretEnc:     userEncrypted,
		},
		"google-admin": {
			IntegrationID: "google-admin",
			SecretEnc:     adminEncrypted,
		},
	}
	manifest := googleWorkspaceToolManifest(map[string]string{
		"gmail":    "read",
		"drive":    "off",
		"calendar": "off",
		"docs":     "off",
	})
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{
		{
			ID:           "google-user",
			WorkspaceID:  "workspace-1",
			Slug:         "google-user-example-com",
			DisplayName:  "Google Workspace user@example.com",
			Kind:         "google_workspace",
			Transport:    "rest",
			Enabled:      true,
			ToolManifest: manifest,
			Config:       json.RawMessage(`{"email":"user@example.com"}`),
		},
		{
			ID:           "google-admin",
			WorkspaceID:  "workspace-1",
			Slug:         "google-admin-company-test",
			DisplayName:  "Google Workspace admin@company.test",
			Kind:         "google_workspace",
			Transport:    "rest",
			Enabled:      true,
			ToolManifest: manifest,
			Config:       json.RawMessage(`{"email":"admin@company.test"}`),
		},
	}
	authHeader := workspaceIntegrationAuthHeader(t, s, "machine-123")

	searchReq := workspaceIntegrationCallRequest(t, workspaceIntegrationSearchToolsName, map[string]interface{}{
		"arguments": map[string]interface{}{"query": "gmail profile", "integration": "google-workspace", "limit": 10},
	})
	searchReq.Header.Set("Authorization", authHeader)
	searchW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(searchW, searchReq)
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
	sources := map[string]string{}
	for _, item := range searchResp.Items {
		if item["name"] == "gmail_profile" {
			addresses[fmt.Sprint(item["connection_slug"])] = fmt.Sprint(item["tool_address"])
			sources[fmt.Sprint(item["connection_slug"])] = fmt.Sprint(item["source_slug"])
		}
	}
	if addresses["google-user-example-com"] == "" || addresses["google-admin-company-test"] == "" || addresses["google-user-example-com"] == addresses["google-admin-company-test"] {
		t.Fatalf("search did not return distinct google account addresses: %+v", searchResp.Items)
	}
	if sources["google-user-example-com"] != "gmail" || sources["google-admin-company-test"] != "gmail" {
		t.Fatalf("search did not return gmail source slugs: %+v", searchResp.Items)
	}

	callReq := workspaceIntegrationCallRequest(t, workspaceIntegrationCallToolName, map[string]interface{}{
		"arguments": map[string]interface{}{
			"tool_address": addresses["google-admin-company-test"],
			"arguments":    map[string]interface{}{},
		},
	})
	callReq.Header.Set("Authorization", authHeader)
	callW := httptest.NewRecorder()
	s.handleWorkspaceIntegrationCallTool(callW, callReq)
	if callW.Code != http.StatusOK {
		t.Fatalf("call expected 200, got %d: %s", callW.Code, callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"emailAddress":"admin@company.test"`) || strings.Contains(callW.Body.String(), `"emailAddress":"user@example.com"`) {
		t.Fatalf("call did not route by selected google account tool_address: %s", callW.Body.String())
	}
	if !strings.Contains(callW.Body.String(), `"tool_address":"`+addresses["google-admin-company-test"]+`"`) {
		t.Fatalf("call response missing selected tool_address: %s", callW.Body.String())
	}
}

func TestWorkspaceIntegrationCallTool_GoogleWorkspaceResourceAllowlists(t *testing.T) {
	var upstreamCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.Header.Get("Authorization") != "Bearer google-access" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/drive/v3/files":
			if r.URL.Query().Get("q") != "'folder-allowed' in parents and trashed = false" {
				t.Fatalf("drive q = %q", r.URL.Query().Get("q"))
			}
			if r.URL.Query().Get("pageSize") != "7" {
				t.Fatalf("drive pageSize = %q", r.URL.Query().Get("pageSize"))
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"files": []map[string]interface{}{{"id": "file-1"}}})
		case "/calendar/v3/calendars/team-cal/events":
			if r.URL.Query().Get("maxResults") != "3" {
				t.Fatalf("calendar maxResults = %q", r.URL.Query().Get("maxResults"))
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"items": []map[string]interface{}{{"id": "event-1"}}})
		default:
			t.Fatalf("unexpected google path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	previousDriveBaseURL := googleDriveAPIBaseURL
	previousCalendarBaseURL := googleCalendarAPIBaseURL
	googleDriveAPIBaseURL = api.URL + "/drive/v3"
	googleCalendarAPIBaseURL = api.URL + "/calendar/v3"
	defer func() {
		googleDriveAPIBaseURL = previousDriveBaseURL
		googleCalendarAPIBaseURL = previousCalendarBaseURL
	}()

	secretKey := "12345678901234567890123456789012"
	encrypted, err := crypto.Encrypt("google-access", secretKey)
	if err != nil {
		t.Fatalf("encrypt google token: %v", err)
	}

	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	s.secretKey = secretKey
	fakeStore.credential = &store.WorkspaceIntegrationCredential{
		IntegrationID: "google-1",
		SecretEnc:     encrypted,
	}
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{
		{
			ID:           "google-1",
			WorkspaceID:  "workspace-1",
			Slug:         googleWorkspaceIntegrationSlug,
			DisplayName:  "Google Workspace",
			Kind:         "google_workspace",
			Transport:    "rest",
			Enabled:      true,
			ToolManifest: googleWorkspaceToolManifest(nil),
			Config:       json.RawMessage(`{"email":"user@example.com","allowed_drive_folder_ids":["folder-allowed"],"allowed_calendar_ids":["team-cal"]}`),
		},
	}

	callTool := func(tool string, args map[string]interface{}) *httptest.ResponseRecorder {
		req := workspaceIntegrationCallRequest(t, tool, map[string]interface{}{"arguments": args})
		req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
		w := httptest.NewRecorder()
		s.handleWorkspaceIntegrationCallTool(w, req)
		return w
	}

	w := callTool("google-workspace.drive_list_files", map[string]interface{}{"folder_id": "folder-allowed", "page_size": 7})
	if w.Code != http.StatusOK {
		t.Fatalf("expected drive 200, got %d: %s", w.Code, w.Body.String())
	}
	allowedCalls := upstreamCalls
	w = callTool("google-workspace.drive_list_files", map[string]interface{}{"folder_id": "folder-blocked", "page_size": 7})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected blocked drive 502, got %d: %s", w.Code, w.Body.String())
	}
	if upstreamCalls != allowedCalls {
		t.Fatalf("blocked drive call reached upstream: calls %d -> %d", allowedCalls, upstreamCalls)
	}

	w = callTool("google-workspace.calendar_list_events", map[string]interface{}{"calendar_id": "team-cal", "max_results": 3})
	if w.Code != http.StatusOK {
		t.Fatalf("expected calendar 200, got %d: %s", w.Code, w.Body.String())
	}
	allowedCalls = upstreamCalls
	w = callTool("google-workspace.calendar_list_events", map[string]interface{}{"calendar_id": "other-cal", "max_results": 3})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected blocked calendar 502, got %d: %s", w.Code, w.Body.String())
	}
	if upstreamCalls != allowedCalls {
		t.Fatalf("blocked calendar call reached upstream: calls %d -> %d", allowedCalls, upstreamCalls)
	}
}

func TestWorkspaceIntegrationOAuthAccessTokenRefreshesUnderCredentialLock(t *testing.T) {
	var refreshCalls int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Fatalf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "refresh-1" {
			t.Fatalf("refresh_token = %q", r.Form.Get("refresh_token"))
		}
		atomic.AddInt32(&refreshCalls, 1)
		time.Sleep(50 * time.Millisecond)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"access_token":  "access-2",
			"refresh_token": "refresh-2",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	defer tokenServer.Close()

	secretKey := "12345678901234567890123456789012"
	accessEnc, err := crypto.Encrypt("access-1", secretKey)
	if err != nil {
		t.Fatalf("encrypt access token: %v", err)
	}
	refreshEnc, err := crypto.Encrypt("refresh-1", secretKey)
	if err != nil {
		t.Fatalf("encrypt refresh token: %v", err)
	}
	expired := time.Now().Add(-time.Hour)
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	s.secretKey = secretKey
	s.oauthClientID = "google-client"
	s.oauthClientSecret = "google-secret"
	fakeStore.credential = &store.WorkspaceIntegrationCredential{
		IntegrationID: "oauth-lock-test",
		SecretEnc:     accessEnc,
		RefreshEnc:    &refreshEnc,
		ExpiresAt:     &expired,
	}

	const callers = 6
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			token, err := s.workspaceIntegrationOAuthAccessToken(context.Background(), "google-workspace", "oauth-lock-test", tokenServer.URL, false)
			if err != nil {
				errs <- err
				return
			}
			if token != "access-2" {
				errs <- fmt.Errorf("token = %q, want access-2", token)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	credential, err := fakeStore.GetWorkspaceIntegrationCredential(context.Background(), "oauth-lock-test")
	if err != nil {
		t.Fatalf("load refreshed credential: %v", err)
	}
	access, err := crypto.Decrypt(credential.SecretEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt refreshed access token: %v", err)
	}
	if access != "access-2" {
		t.Fatalf("stored access token = %q, want access-2", access)
	}
	if credential.RefreshEnc == nil {
		t.Fatal("expected rotated refresh token")
	}
	refresh, err := crypto.Decrypt(*credential.RefreshEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt refreshed refresh token: %v", err)
	}
	if refresh != "refresh-2" {
		t.Fatalf("stored refresh token = %q, want refresh-2", refresh)
	}
}

func TestHTTPWorkspaceIntegrationOAuthRefreshesAndRetriesAfterUnauthorized(t *testing.T) {
	var refreshCalls int
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh-1" {
			t.Fatalf("unexpected refresh form: %v", r.Form)
		}
		refreshCalls++
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"access_token":  "fresh-access",
			"refresh_token": "refresh-2",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	defer tokenServer.Close()

	var upstreamCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		switch upstreamCalls {
		case 1:
			if r.Header.Get("Authorization") != "Bearer stale-access" {
				t.Fatalf("first authorization = %q", r.Header.Get("Authorization"))
			}
			writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"error": "expired"})
		case 2:
			if r.Header.Get("Authorization") != "Bearer fresh-access" {
				t.Fatalf("retry authorization = %q", r.Header.Get("Authorization"))
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
		default:
			t.Fatalf("unexpected upstream call %d", upstreamCalls)
		}
	}))
	defer api.Close()

	secretKey := "12345678901234567890123456789012"
	accessEnc, err := crypto.Encrypt("stale-access", secretKey)
	if err != nil {
		t.Fatalf("encrypt access token: %v", err)
	}
	refreshEnc, err := crypto.Encrypt("refresh-1", secretKey)
	if err != nil {
		t.Fatalf("encrypt refresh token: %v", err)
	}
	future := time.Now().Add(time.Hour)
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	s.secretKey = secretKey
	s.oauthClientID = "google-client"
	s.oauthClientSecret = "google-secret"
	s.allowInsecureWorkspaceIntegrationEndpoints = true
	fakeStore.credential = &store.WorkspaceIntegrationCredential{
		IntegrationID: "oauth-401-test",
		SecretEnc:     accessEnc,
		RefreshEnc:    &refreshEnc,
		ExpiresAt:     &future,
	}

	result, err := s.callHTTPWorkspaceIntegration(context.Background(), store.WorkspaceIntegration{
		ID:        "oauth-401-test",
		Slug:      "oauth-test",
		Transport: "http",
	}, workspaceIntegrationManifestTool{
		Name: "profile",
		Request: &workspaceIntegrationHTTPRequest{
			Method: http.MethodGet,
			URL:    api.URL + "/profile",
			Auth:   &workspaceIntegrationHTTPAuth{Type: "oauth", TokenURL: tokenServer.URL, Required: true},
		},
	}, nil)
	if err != nil {
		t.Fatalf("call http workspace integration: %v", err)
	}
	if result["ok"] != true {
		t.Fatalf("result = %+v, want ok", result)
	}
	if upstreamCalls != 2 {
		t.Fatalf("upstream calls = %d, want first 401 plus retry", upstreamCalls)
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls)
	}
	credential, err := fakeStore.GetWorkspaceIntegrationCredential(context.Background(), "oauth-401-test")
	if err != nil {
		t.Fatalf("load refreshed credential: %v", err)
	}
	access, err := crypto.Decrypt(credential.SecretEnc, secretKey)
	if err != nil {
		t.Fatalf("decrypt refreshed access token: %v", err)
	}
	if access != "fresh-access" {
		t.Fatalf("stored access token = %q, want fresh-access", access)
	}
}

func TestHTTPWorkspaceIntegrationOAuthRejectsUnsafeRuntimeTokenURL(t *testing.T) {
	var tokenEndpointCalled bool
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenEndpointCalled = true
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"access_token": "unsafe-access",
			"expires_in":   3600,
			"token_type":   "Bearer",
		})
	}))
	defer tokenServer.Close()

	secretKey := "12345678901234567890123456789012"
	accessEnc, err := crypto.Encrypt("expired-access", secretKey)
	if err != nil {
		t.Fatalf("encrypt access token: %v", err)
	}
	refreshEnc, err := crypto.Encrypt("refresh-1", secretKey)
	if err != nil {
		t.Fatalf("encrypt refresh token: %v", err)
	}
	expired := time.Now().Add(-time.Hour)
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	s.secretKey = secretKey
	s.allowInsecureWorkspaceIntegrationEndpoints = false
	fakeStore.credential = &store.WorkspaceIntegrationCredential{
		IntegrationID: "unsafe-token-url-test",
		SecretEnc:     accessEnc,
		RefreshEnc:    &refreshEnc,
		ExpiresAt:     &expired,
	}

	token, err := s.workspaceIntegrationOAuthAccessTokenWithClient(context.Background(), "unsafe-token-url-test", tokenServer.URL, "client-id", "", "", false)
	if err == nil || !strings.Contains(err.Error(), "oauth token endpoint must use https") {
		t.Fatalf("expected unsafe token endpoint rejection, token=%q err=%v", token, err)
	}
	if tokenEndpointCalled {
		t.Fatal("unsafe token endpoint should not be called")
	}
}

func TestWorkspaceIntegrationHTTPToolRejectsUnsafeRuntimeEndpoint(t *testing.T) {
	var upstreamCalled bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	}))
	defer api.Close()

	s, _ := newWorkspaceIntegrationGatewayServer()
	s.allowInsecureWorkspaceIntegrationEndpoints = false
	result, err := s.callHTTPWorkspaceIntegration(context.Background(), store.WorkspaceIntegration{
		ID:        "unsafe-runtime-test",
		Slug:      "unsafe-runtime",
		Transport: "http",
		Endpoint:  stringPtr("https://" + strings.TrimPrefix(api.URL, "http://")),
	}, workspaceIntegrationManifestTool{
		Name:   "list_records",
		Access: "read",
		Request: &workspaceIntegrationHTTPRequest{
			Method: http.MethodGet,
			Path:   "/records",
		},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "endpoint host") || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected unsafe endpoint rejection, result=%+v err=%v", result, err)
	}
	if upstreamCalled {
		t.Fatal("unsafe runtime endpoint should not be called")
	}
}

func TestWorkspaceIntegrationHTTPToolRejectsNonHTTPSchemeRuntimeURL(t *testing.T) {
	s, _ := newWorkspaceIntegrationGatewayServer()
	s.allowInsecureWorkspaceIntegrationEndpoints = true
	result, err := s.callHTTPWorkspaceIntegration(context.Background(), store.WorkspaceIntegration{
		ID:        "bad-scheme-runtime-test",
		Slug:      "bad-scheme-runtime",
		Transport: "http",
	}, workspaceIntegrationManifestTool{
		Name:   "list_records",
		Access: "read",
		Request: &workspaceIntegrationHTTPRequest{
			Method: http.MethodGet,
			URL:    "ftp://api.example.test/records",
		},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "endpoint must be an absolute http or https URL") {
		t.Fatalf("expected non-http runtime URL rejection, result=%+v err=%v", result, err)
	}
}

func TestWorkspaceIntegrationCallTool_HTTPTransport(t *testing.T) {
	forceWorkspaceIntegrationSuccessCallEventSampling(t)
	tests := []struct {
		name       string
		statusCode int
		wantStatus int
	}{
		{name: "success", statusCode: http.StatusOK, wantStatus: http.StatusOK},
		{name: "upstream 4xx", statusCode: http.StatusUnauthorized, wantStatus: http.StatusBadGateway},
		{name: "upstream 5xx", statusCode: http.StatusBadGateway, wantStatus: http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/widgets/alpha" {
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
				if r.URL.Query().Get("limit") != "3" {
					t.Fatalf("limit query = %q", r.URL.Query().Get("limit"))
				}
				if r.Header.Get("Authorization") != "Bearer upstream-secret" {
					t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
				}
				if tt.statusCode != http.StatusOK {
					writeJSON(w, tt.statusCode, map[string]interface{}{"secret": "do-not-return"})
					return
				}
				writeJSON(w, http.StatusOK, map[string]interface{}{
					"id":      "alpha",
					"name":    "Alpha",
					"ignored": "not projected",
				})
			}))
			defer api.Close()

			secretKey := "12345678901234567890123456789012"
			encrypted, err := crypto.Encrypt("upstream-secret", secretKey)
			if err != nil {
				t.Fatalf("encrypt token: %v", err)
			}
			s, fakeStore := newWorkspaceIntegrationGatewayServer()
			s.secretKey = secretKey
			fakeStore.credential = &store.WorkspaceIntegrationCredential{
				IntegrationID: "http-1",
				SecretEnc:     encrypted,
			}
			fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{
				{
					ID:          "http-1",
					WorkspaceID: "workspace-1",
					Slug:        "fixture",
					DisplayName: "Fixture",
					Kind:        "fixture",
					Transport:   "http",
					Endpoint:    stringPtr(api.URL),
					Enabled:     true,
					ToolManifest: mustWorkspaceIntegrationToolManifest([]workspaceIntegrationManifestTool{{
						Name:        "get_widget",
						Description: "Get a widget",
						Parameters:  json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"limit":{"type":"integer"}}}`),
						Request: &workspaceIntegrationHTTPRequest{
							Method: http.MethodGet,
							Path:   "/v1/widgets/{id}",
							PathParams: map[string]workspaceIntegrationHTTPValue{
								"id": {Source: "arg", Name: "id"},
							},
							Query: map[string]workspaceIntegrationHTTPValue{
								"limit": {Source: "arg", Name: "limit", Default: 1, Min: workspaceIntegrationIntPtr(1), Max: workspaceIntegrationIntPtr(5)},
							},
							Auth: &workspaceIntegrationHTTPAuth{Type: "bearer", Required: true},
							Response: &workspaceIntegrationHTTPResponseTransform{Fields: map[string]string{
								"id":   "id",
								"name": "name",
							}},
						},
					}}),
					Config: json.RawMessage(`{}`),
				},
			}

			req := workspaceIntegrationCallRequest(t, "fixture.get_widget", map[string]interface{}{
				"arguments": map[string]interface{}{"id": "alpha", "limit": 3},
			})
			req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
			w := httptest.NewRecorder()

			s.handleWorkspaceIntegrationCallTool(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tt.wantStatus, w.Code, w.Body.String())
			}
			events := waitForWorkspaceIntegrationCallEvents(t, fakeStore, 1)
			event := events[0]
			if event.WorkspaceID != "workspace-1" || event.ToolID != "fixture.get_widget" || event.Transport != "http" || event.CallMode != "direct" {
				t.Fatalf("unexpected call event identity: %+v", event)
			}
			if event.LatencyMS < 0 {
				t.Fatalf("latency should be non-negative: %+v", event)
			}
			if got := strings.Join(event.ArgKeys, ","); got != "id,limit" {
				t.Fatalf("arg keys = %q", got)
			}
			argShape := string(event.ArgShape)
			if strings.Contains(argShape, "alpha") || strings.Contains(argShape, "3") {
				t.Fatalf("arg shape leaked raw values: %s", argShape)
			}
			if !strings.Contains(argShape, `"id"`) || !strings.Contains(argShape, `"length_bucket"`) {
				t.Fatalf("arg shape missing sanitized evidence: %s", argShape)
			}
			if tt.wantStatus != http.StatusOK {
				if strings.Contains(w.Body.String(), "do-not-return") {
					t.Fatalf("upstream body leaked to caller: %s", w.Body.String())
				}
				if event.Status != "error" || event.FailureClass == nil || *event.FailureClass == "" || event.UpstreamStatus == nil || *event.UpstreamStatus != tt.statusCode {
					t.Fatalf("unexpected error call event: %+v", event)
				}
				return
			}
			if event.Status != "success" || event.FailureClass != nil || event.UpstreamStatus != nil {
				t.Fatalf("unexpected success call event: %+v", event)
			}
			var resp struct {
				Result map[string]interface{} `json:"result"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.Result["id"] != "alpha" || resp.Result["name"] != "Alpha" {
				t.Fatalf("result = %v", resp.Result)
			}
			if _, ok := resp.Result["ignored"]; ok {
				t.Fatalf("unexpected unprojected field in result: %v", resp.Result)
			}
		})
	}
}

func TestWorkspaceIntegrationCallTool_HTTPTransportRedactsStoredSecretFromResult(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"authorization": r.Header.Get("Authorization"),
			"nested": map[string]interface{}{
				"echo": "prefix-upstream-secret-suffix",
			},
			"list": []string{"upstream-secret"},
		})
	}))
	defer api.Close()

	secretKey := "12345678901234567890123456789012"
	encrypted, err := crypto.Encrypt("upstream-secret", secretKey)
	if err != nil {
		t.Fatalf("encrypt token: %v", err)
	}
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	s.secretKey = secretKey
	fakeStore.credential = &store.WorkspaceIntegrationCredential{
		IntegrationID: "http-1",
		SecretEnc:     encrypted,
	}
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{
		{
			ID:          "http-1",
			WorkspaceID: "workspace-1",
			Slug:        "fixture",
			DisplayName: "Fixture",
			Kind:        "fixture",
			Transport:   "http",
			Endpoint:    stringPtr(api.URL),
			Enabled:     true,
			ToolManifest: mustWorkspaceIntegrationToolManifest([]workspaceIntegrationManifestTool{{
				Name:        "echo_secret",
				Description: "Echo an upstream response without projection",
				Request: &workspaceIntegrationHTTPRequest{
					Method: http.MethodGet,
					Path:   "/",
					Auth:   &workspaceIntegrationHTTPAuth{Type: "bearer", Required: true},
				},
			}}),
			Config: json.RawMessage(`{"api_key":"config-secret"}`),
		},
	}
	req := workspaceIntegrationCallRequest(t, "fixture.echo_secret", map[string]interface{}{"arguments": map[string]interface{}{}})
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()

	s.handleWorkspaceIntegrationCallTool(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "upstream-secret") {
		t.Fatalf("tool result leaked upstream secret: %s", body)
	}
	if !strings.Contains(body, "Bearer [REDACTED]") || !strings.Contains(body, "prefix-[REDACTED]-suffix") {
		t.Fatalf("tool result was not redacted as expected: %s", body)
	}
}

func TestWorkspaceIntegrationCallTool_HTTPUnknownTool(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{
		{
			ID:           "http-1",
			WorkspaceID:  "workspace-1",
			Slug:         "fixture",
			DisplayName:  "Fixture",
			Kind:         "fixture",
			Transport:    "http",
			Endpoint:     stringPtr("https://example.test"),
			Enabled:      true,
			ToolManifest: mustWorkspaceIntegrationToolManifest([]workspaceIntegrationManifestTool{{Name: "known", Request: &workspaceIntegrationHTTPRequest{Path: "/"}}}),
			Config:       json.RawMessage(`{}`),
		},
	}
	req := workspaceIntegrationCallRequest(t, "fixture.unknown", map[string]interface{}{"arguments": map[string]interface{}{}})
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()

	s.handleWorkspaceIntegrationCallTool(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkspaceIntegrationCallTool_MCPRemoteTransport(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantStatus int
	}{
		{name: "success", statusCode: http.StatusOK, wantStatus: http.StatusOK},
		{name: "upstream 4xx", statusCode: http.StatusUnauthorized, wantStatus: http.StatusBadGateway},
		{name: "upstream 5xx", statusCode: http.StatusBadGateway, wantStatus: http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var initialized bool
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer mcp-secret" {
					t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
				}
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
						"result":  map[string]interface{}{"protocolVersion": workspaceIntegrationMCPProtocolVersion},
					})
				case "notifications/initialized":
					w.WriteHeader(http.StatusAccepted)
				case "tools/call":
					if !initialized {
						t.Fatal("tools/call before initialize")
					}
					if r.Header.Get("Mcp-Session-Id") != "session-1" {
						t.Fatalf("session header = %q", r.Header.Get("Mcp-Session-Id"))
					}
					if tt.statusCode != http.StatusOK {
						writeJSON(w, tt.statusCode, map[string]interface{}{"secret": "do-not-return"})
						return
					}
					writeJSON(w, http.StatusOK, map[string]interface{}{
						"jsonrpc": "2.0",
						"id":      payload.ID,
						"result": map[string]interface{}{
							"content": []map[string]interface{}{{"type": "text", "text": "pong"}},
							"structuredContent": map[string]interface{}{
								"ok": true,
							},
						},
					})
				default:
					t.Fatalf("unexpected mcp method %q", payload.Method)
				}
			}))
			defer api.Close()

			secretKey := "12345678901234567890123456789012"
			encrypted, err := crypto.Encrypt("mcp-secret", secretKey)
			if err != nil {
				t.Fatalf("encrypt token: %v", err)
			}
			s, fakeStore := newWorkspaceIntegrationGatewayServer()
			s.secretKey = secretKey
			fakeStore.credential = &store.WorkspaceIntegrationCredential{IntegrationID: "mcp-1", SecretEnc: encrypted}
			fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{
				{
					ID:          "mcp-1",
					WorkspaceID: "workspace-1",
					Slug:        "remote",
					DisplayName: "Remote MCP",
					Kind:        "mcp",
					Transport:   "mcp-remote",
					Endpoint:    stringPtr(api.URL),
					Enabled:     true,
					ToolManifest: mustWorkspaceIntegrationToolManifest([]workspaceIntegrationManifestTool{{
						Name:      "ping",
						MCPRemote: &workspaceIntegrationMCPRemoteToolSpec{Auth: &workspaceIntegrationHTTPAuth{Type: "bearer", Required: true}},
					}}),
					Config: json.RawMessage(`{}`),
				},
			}
			req := workspaceIntegrationCallRequest(t, "remote.ping", map[string]interface{}{"arguments": map[string]interface{}{"input": "hello"}})
			req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
			w := httptest.NewRecorder()

			s.handleWorkspaceIntegrationCallTool(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tt.wantStatus, w.Code, w.Body.String())
			}
			if tt.wantStatus != http.StatusOK {
				if strings.Contains(w.Body.String(), "do-not-return") {
					t.Fatalf("upstream body leaked to caller: %s", w.Body.String())
				}
				return
			}
			var resp struct {
				Result map[string]interface{} `json:"result"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.Result["structuredContent"] == nil {
				t.Fatalf("mcp result = %v", resp.Result)
			}
		})
	}
}

func TestWorkspaceIntegrationCallTool_MCPRemoteErrorRedactedFromCallerAndLogs(t *testing.T) {
	var logBuf bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	const leakedSecret = "mcp-error-secret"
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
				"result":  map[string]interface{}{"protocolVersion": workspaceIntegrationMCPProtocolVersion},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			if !initialized {
				t.Fatal("tools/call before initialize")
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      payload.ID,
				"error": map[string]interface{}{
					"code":    -32000,
					"message": "provider failure with " + leakedSecret,
				},
			})
		default:
			t.Fatalf("unexpected mcp method %q", payload.Method)
		}
	}))
	defer api.Close()

	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{
		{
			ID:          "mcp-1",
			WorkspaceID: "workspace-1",
			Slug:        "remote",
			DisplayName: "Remote MCP",
			Kind:        "mcp",
			Transport:   "mcp-remote",
			Endpoint:    stringPtr(api.URL),
			Enabled:     true,
			ToolManifest: mustWorkspaceIntegrationToolManifest([]workspaceIntegrationManifestTool{{
				Name:      "ping",
				MCPRemote: &workspaceIntegrationMCPRemoteToolSpec{},
			}}),
			Config: json.RawMessage(`{}`),
		},
	}
	req := workspaceIntegrationCallRequest(t, "remote.ping", map[string]interface{}{"arguments": map[string]interface{}{}})
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()

	s.handleWorkspaceIntegrationCallTool(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), leakedSecret) {
		t.Fatalf("caller response leaked upstream secret: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "the remote MCP server returned an error") {
		t.Fatalf("caller response = %s, want generic tool failure", w.Body.String())
	}
	logs := logBuf.String()
	if strings.Contains(logs, leakedSecret) {
		t.Fatalf("logs leaked upstream secret: %s", logs)
	}
	for _, expected := range []string{
		"workspace_integrations.tool_call",
		"integration=remote",
		"tool=remote.ping",
		"machine_hash=" + hashForLog("machine-123"),
		"status=error",
		"failure_class=mcp_remote_failed",
	} {
		if !strings.Contains(logs, expected) {
			t.Fatalf("logs missing %q: %s", expected, logs)
		}
	}
}

func TestWorkspaceIntegrationToolCallStructuredLogs(t *testing.T) {
	upstreamFailure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"secret": "log-shape-upstream-secret"})
	}))
	defer upstreamFailure.Close()

	mcpFailure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"secret": "log-shape-mcp-secret"})
	}))
	defer mcpFailure.Close()

	httpTool := func(name string, rawURL string, authSpec *workspaceIntegrationHTTPAuth) workspaceIntegrationManifestTool {
		return workspaceIntegrationManifestTool{
			Name: name,
			Request: &workspaceIntegrationHTTPRequest{
				Method: http.MethodGet,
				URL:    rawURL,
				Auth:   authSpec,
			},
		}
	}

	tests := []struct {
		name         string
		integration  store.WorkspaceIntegration
		tool         workspaceIntegrationManifestTool
		setup        func(*workspaceIntegrationGatewayStore)
		wantStatus   string
		wantFailure  string
		wantError    bool
		wantRedacted []string
	}{
		{
			name: "success",
			integration: store.WorkspaceIntegration{
				ID:        "mock-1",
				Slug:      "mock",
				Transport: "mock",
			},
			tool:       workspaceIntegrationManifestTool{Name: "echo"},
			wantStatus: "success",
		},
		{
			name: "credential_not_configured",
			integration: store.WorkspaceIntegration{
				ID:        "missing-credential",
				Slug:      "http-auth",
				Transport: "http",
				Config:    json.RawMessage(`{}`),
			},
			tool:        httpTool("fetch", "http://example.invalid", &workspaceIntegrationHTTPAuth{Type: "bearer", Required: true}),
			wantStatus:  "error",
			wantFailure: "credential_not_configured",
			wantError:   true,
		},
		{
			name: "credential_decrypt_failed",
			integration: store.WorkspaceIntegration{
				ID:        "bad-credential",
				Slug:      "http-decrypt",
				Transport: "http",
				Config:    json.RawMessage(`{}`),
			},
			tool: httpTool("fetch", "http://example.invalid", &workspaceIntegrationHTTPAuth{Type: "bearer", Required: true}),
			setup: func(fakeStore *workspaceIntegrationGatewayStore) {
				fakeStore.credential = &store.WorkspaceIntegrationCredential{
					IntegrationID: "bad-credential",
					SecretEnc:     "log-shape-clear-secret",
				}
			},
			wantStatus:   "error",
			wantFailure:  "credential_decrypt_failed",
			wantError:    true,
			wantRedacted: []string{"log-shape-clear-secret"},
		},
		{
			name: "oauth_failed",
			integration: store.WorkspaceIntegration{
				ID:        "oauth-1",
				Slug:      "http-oauth",
				Transport: "http",
				Config:    json.RawMessage(`{}`),
			},
			tool:        httpTool("profile", "http://example.invalid", &workspaceIntegrationHTTPAuth{Type: "oauth", Required: true}),
			wantStatus:  "error",
			wantFailure: "oauth_failed",
			wantError:   true,
		},
		{
			name: "upstream_http_status",
			integration: store.WorkspaceIntegration{
				ID:        "upstream-1",
				Slug:      "http-upstream",
				Transport: "http",
				Config:    json.RawMessage(`{}`),
			},
			tool:         httpTool("fetch", upstreamFailure.URL, nil),
			wantStatus:   "error",
			wantFailure:  "upstream_http_status",
			wantError:    true,
			wantRedacted: []string{"log-shape-upstream-secret"},
		},
		{
			name: "mcp_remote_failed",
			integration: store.WorkspaceIntegration{
				ID:        "mcp-1",
				Slug:      "remote",
				Transport: "mcp-remote",
				Endpoint:  stringPtr(mcpFailure.URL),
				Config:    json.RawMessage(`{}`),
			},
			tool:         workspaceIntegrationManifestTool{Name: "ping", MCPRemote: &workspaceIntegrationMCPRemoteToolSpec{}},
			wantStatus:   "error",
			wantFailure:  "mcp_remote_failed",
			wantError:    true,
			wantRedacted: []string{"log-shape-mcp-secret"},
		},
		{
			name: "unsupported_tool",
			integration: store.WorkspaceIntegration{
				ID:        "custom-1",
				Slug:      "custom",
				Transport: "custom",
			},
			tool:        workspaceIntegrationManifestTool{Name: "noop"},
			wantStatus:  "error",
			wantFailure: "unsupported_tool",
			wantError:   true,
		},
		{
			name: "tool_call_failed",
			integration: store.WorkspaceIntegration{
				ID:        "bad-config-1",
				Slug:      "bad-config",
				Transport: "http",
				Config:    json.RawMessage(`{`),
			},
			tool:        httpTool("fetch", "http://example.invalid", nil),
			wantStatus:  "error",
			wantFailure: "tool_call_failed",
			wantError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, fakeStore := newWorkspaceIntegrationGatewayServer()
			s.secretKey = "12345678901234567890123456789012"
			if tt.setup != nil {
				tt.setup(fakeStore)
			}

			logs := captureWorkspaceIntegrationLogs(t, func() {
				result, err := s.callWorkspaceIntegrationToolWithLog(context.Background(), "machine-123", tt.integration, tt.tool, map[string]interface{}{"message": "hello"})
				if tt.wantError {
					if err == nil {
						t.Fatal("expected tool call error")
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected tool call error: %v", err)
				}
				if result["echo"] == nil {
					t.Fatalf("result = %v, want echo payload", result)
				}
			})

			for _, expected := range []string{
				"workspace_integrations.tool_call",
				"integration=" + tt.integration.Slug,
				"tool=" + tt.integration.Slug + "." + tt.tool.Name,
				"machine_hash=" + hashForLog("machine-123"),
				"latency_ms=",
				"status=" + tt.wantStatus,
			} {
				if !strings.Contains(logs, expected) {
					t.Fatalf("logs missing %q: %s", expected, logs)
				}
			}
			if tt.wantFailure != "" {
				if !strings.Contains(logs, "failure_class="+tt.wantFailure) {
					t.Fatalf("logs missing failure class %q: %s", tt.wantFailure, logs)
				}
			} else if strings.Contains(logs, "failure_class=") {
				t.Fatalf("success logs should not include failure_class: %s", logs)
			}
			if strings.Contains(logs, "machine-123") {
				t.Fatalf("logs leaked raw machine ID: %s", logs)
			}
			for _, forbidden := range tt.wantRedacted {
				if strings.Contains(logs, forbidden) {
					t.Fatalf("logs leaked secret %q: %s", forbidden, logs)
				}
			}
		})
	}
}

func TestWorkspaceIntegrationMCPBridge(t *testing.T) {
	s, _ := newWorkspaceIntegrationGatewayServer()

	req := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`))
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("initialize expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), workspaceIntegrationMCPServerName) {
		t.Fatalf("initialize response missing server name: %s", w.Body.String())
	}
	var initResp struct {
		Result struct {
			Capabilities map[string]interface{} `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &initResp); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	if _, ok := initResp.Result.Capabilities["tools"]; !ok {
		t.Fatalf("initialize capabilities missing tools: %s", w.Body.String())
	}
	for _, supported := range []string{"resources", "prompts"} {
		if _, ok := initResp.Result.Capabilities[supported]; !ok {
			t.Fatalf("initialize capabilities missing %s: %s", supported, w.Body.String())
		}
	}

	req = httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w = httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tools/list response = %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), workspaceIntegrationSearchToolsName) || !strings.Contains(w.Body.String(), workspaceIntegrationDescribeToolName) || !strings.Contains(w.Body.String(), workspaceIntegrationCallToolName) {
		t.Fatalf("tools/list missing facade tools: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "mock.echo") {
		t.Fatalf("tools/list exposed direct provider tool over native MCP: %s", w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"resources/list","params":{}}`))
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w = httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("resources/list expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), workspaceIntegrationMCPAgentGuidanceResourceURI) || !strings.Contains(w.Body.String(), "text/markdown") {
		t.Fatalf("resources/list missing agent guidance resource: %s", w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"ocm://workspace-integrations/agent-guidance"}}`))
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w = httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("resources/read expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ocm.search_tools") || !strings.Contains(w.Body.String(), "tool_address") || !strings.Contains(w.Body.String(), "Google Workspace") {
		t.Fatalf("resources/read missing facade guidance: %s", w.Body.String())
	}
	for _, want := range []string{
		"Use direct search, describe, and call for one-off single actions.",
		"Use ocm.execute only for one-off multi-step work",
		"Use saved workflows only for repeated, scheduled, shared, durable, or approval-heavy work",
		"Do not claim execute or workflow support unless those tools are explicitly listed by tools/list.",
	} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("resources/read missing orchestration guidance %q: %s", want, w.Body.String())
		}
	}

	req = httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":5,"method":"prompts/list","params":{}}`))
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w = httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("prompts/list expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), workspaceIntegrationMCPAgentGuidancePromptName) {
		t.Fatalf("prompts/list missing agent guidance prompt: %s", w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":6,"method":"prompts/get","params":{"name":"ocm_workspace_integrations"}}`))
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w = httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("prompts/get expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ocm.call_tool") || !strings.Contains(w.Body.String(), "reconnect the Google account") {
		t.Fatalf("prompts/get missing agent guidance prompt content: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "future `ocm.execute`") || strings.Contains(w.Body.String(), "future ocm.execute") {
		t.Fatalf("prompts/get still describes execute as future-only: %s", w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"mock.echo","arguments":{"message":"hello"}}}`))
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w = httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tools/call expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"error"`) || !strings.Contains(w.Body.String(), "Tool not found") {
		t.Fatalf("direct native MCP tools/call should be rejected: %s", w.Body.String())
	}
}

func TestWorkspaceIntegrationOrchestrationFeatureFlagsDefaultDisabled(t *testing.T) {
	t.Setenv("OCM_WORKSPACE_INTEGRATIONS_EXECUTE_ENABLED", "")
	t.Setenv("OCM_WORKSPACE_INTEGRATIONS_WORKFLOWS_ENABLED", "")

	flags := workspaceIntegrationOrchestrationFeatureFlagsFromEnv()
	if flags.Execute || flags.Workflows {
		t.Fatalf("orchestration flags default enabled: %+v", flags)
	}
}

func TestWorkspaceIntegrationOrchestrationFeatureFlagsRequireExplicitOptIn(t *testing.T) {
	t.Setenv("OCM_WORKSPACE_INTEGRATIONS_EXECUTE_ENABLED", "true")
	t.Setenv("OCM_WORKSPACE_INTEGRATIONS_WORKFLOWS_ENABLED", "1")

	flags := workspaceIntegrationOrchestrationFeatureFlagsFromEnv()
	if !flags.Execute || !flags.Workflows {
		t.Fatalf("orchestration flags did not opt in: %+v", flags)
	}
}

func TestWorkspaceIntegrationMCPBridgePhase3ToolsDisabledByDefault(t *testing.T) {
	s, _ := newWorkspaceIntegrationGatewayServer()

	req := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tools/list expected 200, got %d: %s", w.Code, w.Body.String())
	}
	for _, disabledTool := range []string{
		workspaceIntegrationExecuteToolName,
		workspaceIntegrationCreateWorkflowToolName,
		workspaceIntegrationRunWorkflowToolName,
		workspaceIntegrationResumeWorkflowToolName,
	} {
		if strings.Contains(w.Body.String(), disabledTool) {
			t.Fatalf("tools/list exposed disabled Phase 3 tool %s: %s", disabledTool, w.Body.String())
		}
	}

	req = httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ocm.execute","arguments":{"script":"return tools.search({query: 'echo'})"}}}`))
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w = httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tools/call expected JSON-RPC 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"error"`) || !strings.Contains(w.Body.String(), "Tool not found") {
		t.Fatalf("disabled execute call should be rejected as unavailable: %s", w.Body.String())
	}
}

func TestWorkspaceIntegrationMCPBridgeWorkflowToolsRequireRestrictedRuntime(t *testing.T) {
	s, _ := newWorkspaceIntegrationGatewayServer()
	s.workspaceIntegrationWorkflowsEnabled = true

	req := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tools/list expected 200, got %d: %s", w.Code, w.Body.String())
	}
	for _, workflowTool := range []string{
		workspaceIntegrationCreateWorkflowToolName,
		workspaceIntegrationRunWorkflowToolName,
		workspaceIntegrationResumeWorkflowToolName,
	} {
		if !strings.Contains(w.Body.String(), workflowTool) {
			t.Fatalf("tools/list missing workflow tool %s when workflow flag enabled: %s", workflowTool, w.Body.String())
		}
	}
	if strings.Contains(w.Body.String(), workspaceIntegrationExecuteToolName) {
		t.Fatalf("workflow flag should not expose execute tool: %s", w.Body.String())
	}

	for _, workflowTool := range []string{
		workspaceIntegrationCreateWorkflowToolName,
		workspaceIntegrationRunWorkflowToolName,
		workspaceIntegrationResumeWorkflowToolName,
	} {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, workflowTool)
		req = httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
		w = httptest.NewRecorder()
		s.handleWorkspaceIntegrationMCP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("tools/call expected JSON-RPC 200, got %d: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"error"`) ||
			!strings.Contains(w.Body.String(), "restricted Lobster") ||
			!strings.Contains(w.Body.String(), "Task Flow") {
			t.Fatalf("%s should stop at restricted workflow runtime boundary: %s", workflowTool, w.Body.String())
		}
	}
}

func TestWorkspaceIntegrationBridgePolicy_DenyWinsAndRequiresQualifiedToolNames(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{
		{
			ID:           "policy-1",
			WorkspaceID:  "workspace-1",
			Slug:         "policy",
			DisplayName:  "Policy Fixture",
			Kind:         "mock",
			Transport:    "mock",
			Enabled:      true,
			ToolManifest: json.RawMessage(`[{"name":"allowed","description":"Allowed"},{"name":"denied","description":"Denied"},{"name":"hidden","description":"Hidden"}]`),
			AllowedTools: []string{"allowed", "denied"},
			DeniedTools:  []string{"policy.denied"},
			Config:       json.RawMessage(`{}`),
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/ocm-integrations/tools", nil)
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()
	s.handleWorkspaceIntegrationListTools(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tools/list expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Tools []workspaceIntegrationTool `json:"tools"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	names := workspaceIntegrationToolNameSet(listResp.Tools)
	if !names[workspaceIntegrationSearchToolsName] || !names[workspaceIntegrationDescribeToolName] || !names[workspaceIntegrationCallToolName] || !names["policy.allowed"] {
		t.Fatalf("tools = %+v, missing facade or policy.allowed", listResp.Tools)
	}
	if names["policy.denied"] || names["policy.hidden"] {
		t.Fatalf("tools = %+v, denied/hidden direct tools should not be listed", listResp.Tools)
	}

	for _, toolName := range []string{"policy.denied", "policy.hidden", "allowed"} {
		req := workspaceIntegrationCallRequest(t, toolName, map[string]interface{}{"arguments": map[string]interface{}{}})
		req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
		w := httptest.NewRecorder()
		s.handleWorkspaceIntegrationCallTool(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s expected 404, got %d: %s", toolName, w.Code, w.Body.String())
		}
	}
}

func TestWorkspaceIntegrationMCPBridgePolicy_DeniedToolHiddenAndRejected(t *testing.T) {
	s, fakeStore := newWorkspaceIntegrationGatewayServer()
	fakeStore.integrations["machine-123"] = []store.WorkspaceIntegration{
		{
			ID:           "policy-1",
			WorkspaceID:  "workspace-1",
			Slug:         "policy",
			DisplayName:  "Policy Fixture",
			Kind:         "mock",
			Transport:    "mock",
			Enabled:      true,
			ToolManifest: json.RawMessage(`[{"name":"allowed","description":"Allowed"},{"name":"denied","description":"Denied"}]`),
			AllowedTools: []string{"policy.allowed", "policy.denied"},
			DeniedTools:  []string{"denied"},
			Config:       json.RawMessage(`{}`),
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w := httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tools/list expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), workspaceIntegrationSearchToolsName) || !strings.Contains(w.Body.String(), workspaceIntegrationDescribeToolName) || !strings.Contains(w.Body.String(), workspaceIntegrationCallToolName) {
		t.Fatalf("mcp tools/list missing facade tools: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "policy.allowed") || strings.Contains(w.Body.String(), "policy.denied") {
		t.Fatalf("mcp tools/list exposed direct provider tools instead of facade-only surface: %s", w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/workspace-integrations/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"policy.denied","arguments":{}}}`))
	req.Header.Set("Authorization", workspaceIntegrationAuthHeader(t, s, "machine-123"))
	w = httptest.NewRecorder()
	s.handleWorkspaceIntegrationMCP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tools/call expected JSON-RPC 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"error"`) || !strings.Contains(w.Body.String(), "Tool not found") {
		t.Fatalf("mcp denied call response = %s", w.Body.String())
	}
}

func TestWorkspaceIntegrationGatewayHasNoNameBasedExecutionBranches(t *testing.T) {
	data, err := os.ReadFile("workspace_integrations_gateway.go")
	if err != nil {
		t.Fatalf("read gateway source: %v", err)
	}
	source := string(data)
	start := strings.Index(source, "func (s *Server) handleWorkspaceIntegrationCallTool")
	if start < 0 {
		t.Fatal("handleWorkspaceIntegrationCallTool not found")
	}
	end := strings.Index(source[start+1:], "\nfunc ")
	if end < 0 {
		t.Fatal("could not isolate handleWorkspaceIntegrationCallTool")
	}
	handler := source[start : start+1+end]
	for _, forbidden := range []string{
		"isGitHubWorkspaceIntegration",
		"isGoogleWorkspaceIntegration",
		"callGitHubWorkspaceIntegration",
		"callGoogleWorkspaceIntegration",
	} {
		if strings.Contains(handler, forbidden) {
			t.Fatalf("gateway handler still contains name-based branch %q", forbidden)
		}
	}
}

func TestOCMIntegrationsPluginDoesNotPlaceMachineTokenInCurlArgvOrSurfacedErrors(t *testing.T) {
	data, err := os.ReadFile("../../../plugins/ocm-integrations-plugin/index.ts")
	if err != nil {
		t.Fatalf("read ocm integrations plugin source: %v", err)
	}
	source := string(data)
	for _, forbidden := range []string{
		"github",
		"google",
		"apollo",
		"apiKey",
		"accessToken",
		"refreshToken",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("plugin must stay generic and credential-free; found %q in source", forbidden)
		}
	}
	for _, forbidden := range []string{
		"authArgs(config.machineToken)",
		`"-H", ` + "`Authorization: Bearer",
		`'-H', ` + "`Authorization: Bearer",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("plugin still risks putting the machine token in curl argv via %q", forbidden)
		}
	}
	if !strings.Contains(source, `args: ["-K", "-"]`) {
		t.Fatal("plugin should pass the Authorization header through curl stdin config")
	}
	if !strings.Contains(source, "surfacedErrorMessage(err, config)") {
		t.Fatal("plugin should scrub surfaced curl/tool errors before prompt or tool output")
	}
}

func TestOCMIntegrationsPluginRemainsLegacyRESTFallback(t *testing.T) {
	data, err := os.ReadFile("../../../plugins/ocm-integrations-plugin/index.ts")
	if err != nil {
		t.Fatalf("read ocm integrations plugin source: %v", err)
	}
	source := string(data)
	for _, required := range []string{
		"Legacy REST fallback",
		"toolsUrl",
		"fetchToolsSync(config)",
		"api.registerTool",
		"callToolSync(config, tool.name, params)",
		"${config.apiUrl}/tools/${encodeURIComponent(toolName)}/call",
		"Do not invent or prefer provider-specific tool names",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("plugin no longer proves legacy REST fallback boundary; missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"/api/workspace-integrations/mcp",
		"mcp.servers",
		"Direct provider tools may be present",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("legacy REST fallback should not encode stale runtime guidance; found %q", forbidden)
		}
	}
}

func TestWorkspaceIntegrationRejectsComposioToken(t *testing.T) {
	s, _ := newWorkspaceIntegrationGatewayServer()
	token, err := s.auth.IssueComposioProxyToken("machine-123", time.Minute)
	if err != nil {
		t.Fatalf("mint composio token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/ocm-integrations/tools", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	s.handleWorkspaceIntegrationListTools(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}
