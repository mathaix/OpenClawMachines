package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

type workspaceIntegrationTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Integration string          `json:"integration"`
	Kind        string          `json:"kind"`
	Transport   string          `json:"transport"`
	Access      string          `json:"access,omitempty"`
	Policy      string          `json:"policy,omitempty"`
	Source      string          `json:"source,omitempty"`
}

type workspaceIntegrationManifestTool struct {
	Name        string                                 `json:"name"`
	Description string                                 `json:"description,omitempty"`
	Parameters  json.RawMessage                        `json:"parameters,omitempty"`
	InputSchema json.RawMessage                        `json:"input_schema,omitempty"`
	Schema      json.RawMessage                        `json:"schema,omitempty"`
	Access      string                                 `json:"access,omitempty"`
	Source      string                                 `json:"source,omitempty"`
	Request     *workspaceIntegrationHTTPRequest       `json:"request,omitempty"`
	MCPRemote   *workspaceIntegrationMCPRemoteToolSpec `json:"mcp_remote,omitempty"`
}

const (
	workspaceIntegrationFacadeSlug              = "ocm"
	workspaceIntegrationSearchToolsName         = "ocm.search_tools"
	workspaceIntegrationDescribeToolName        = "ocm.describe_tool"
	workspaceIntegrationCallToolName            = "ocm.call_tool"
	workspaceIntegrationToolAddressPrefix       = "wi."
	workspaceIntegrationLegacyToolAddressPrefix = "wi.workspace."
	workspaceIntegrationConnectionScope         = "workspace"
	workspaceIntegrationSearchMethodSemantic    = "semantic"
	workspaceIntegrationSearchMethodKeyword     = "keyword"
	workspaceIntegrationSearchMethodHybrid      = "hybrid"
	workspaceIntegrationDefaultSearchLimit      = 10
	workspaceIntegrationMaxSearchLimit          = 50
	workspaceIntegrationPolicyAllow             = "allow"
	workspaceIntegrationPolicyRequireApproval   = "require_approval"
	workspaceIntegrationPolicyBlock             = "block"
)

var (
	workspaceIntegrationSuccessCallEventSampleRate = 0.10
	workspaceIntegrationCallEventSample            = rand.Float64
	workspaceIntegrationCallEventWriteTimeout      = 5 * time.Second
	workspaceIntegrationCallEventWriteSlots        = make(chan struct{}, 64)
	workspaceIntegrationCallEventDropped           atomic.Int64
)

type workspaceIntegrationToolDescriptor struct {
	ToolID              string                            `json:"tool_id"`
	ToolAddress         string                            `json:"tool_address"`
	LegacyToolID        string                            `json:"legacy_tool_id"`
	LegacyIntegrationID string                            `json:"legacy_integration_id,omitempty"`
	IntegrationSlug     string                            `json:"integration_slug"`
	IntegrationName     string                            `json:"integration_name"`
	SourceSlug          string                            `json:"source_slug"`
	SourceKind          string                            `json:"source_kind"`
	ConnectionID        string                            `json:"connection_id"`
	ConnectionSlug      string                            `json:"connection_slug"`
	ConnectionScope     string                            `json:"connection_scope"`
	SnapshotID          string                            `json:"snapshot_id"`
	ToolsSyncedAt       *time.Time                        `json:"tools_synced_at,omitempty"`
	Name                string                            `json:"name"`
	Description         string                            `json:"description,omitempty"`
	InputSchema         json.RawMessage                   `json:"input_schema,omitempty"`
	Access              string                            `json:"access"`
	Policy              string                            `json:"policy"`
	PolicyState         string                            `json:"policy_state"`
	AuthState           string                            `json:"auth_state"`
	Source              string                            `json:"source"`
	Guidance            *workspaceIntegrationToolGuidance `json:"guidance,omitempty"`
}

type workspaceIntegrationToolGuidance struct {
	Version            int     `json:"version"`
	Text               string  `json:"text"`
	SourceFailureClass *string `json:"source_failure_class,omitempty"`
}

type workspaceIntegrationCallTelemetry struct {
	UpstreamLatencyMS int
	RetryCount        int
	RetryAfterMS      *int
	Retryable         bool
	Terminal          bool
}

type workspaceIntegrationCallTelemetryContextKey struct{}
type workspaceIntegrationWorkspaceContextKey struct{}

func workspaceIntegrationCallTelemetryFromContext(ctx context.Context) *workspaceIntegrationCallTelemetry {
	if telemetry, ok := ctx.Value(workspaceIntegrationCallTelemetryContextKey{}).(*workspaceIntegrationCallTelemetry); ok {
		return telemetry
	}
	return nil
}

func workspaceIntegrationWorkspaceFromContext(ctx context.Context) *store.Workspace {
	if workspace, ok := ctx.Value(workspaceIntegrationWorkspaceContextKey{}).(*store.Workspace); ok {
		return workspace
	}
	return nil
}

func (t *workspaceIntegrationCallTelemetry) addUpstreamLatency(duration time.Duration) {
	if t == nil {
		return
	}
	t.UpstreamLatencyMS += int(duration.Milliseconds())
}

type workspaceIntegrationValidationError struct {
	Code       string
	SchemaPath string
	Detail     string
}

func (e *workspaceIntegrationValidationError) Error() string {
	if e.Detail != "" {
		return e.Detail
	}
	if e.Code != "" && e.SchemaPath != "" {
		return e.Code + " at " + e.SchemaPath
	}
	if e.Code != "" {
		return e.Code
	}
	return "invalid arguments"
}

type workspaceIntegrationErrorContract struct {
	Class        string `json:"class"`
	Retryable    bool   `json:"retryable"`
	RetryAfterMS *int   `json:"retry_after_ms,omitempty"`
	Terminal     bool   `json:"terminal"`
	Action       string `json:"action"`
	Detail       string `json:"detail"`
}

type workspaceIntegrationConnectorProjectionReader interface {
	ListWorkspaceIntegrationConnectorProjections(ctx context.Context, workspaceID string) ([]store.WorkspaceIntegrationConnectorProjection, error)
}

type workspaceIntegrationGuidanceOverlayReader interface {
	ListWorkspaceIntegrationGuidanceOverlays(ctx context.Context, accountID int, workspaceID, status string) ([]store.WorkspaceIntegrationGuidanceOverlay, error)
}

type workspaceIntegrationConnectionCredentialReader interface {
	GetWorkspaceIntegrationConnectionCredential(ctx context.Context, connectionID string) (*store.WorkspaceIntegrationConnectionCredential, error)
}

type workspaceIntegrationConnectionCredentialWriter interface {
	SetWorkspaceIntegrationConnectionCredential(ctx context.Context, credential *store.WorkspaceIntegrationConnectionCredential) error
}

func (s *Server) requireWorkspaceIntegrationToken(w http.ResponseWriter, r *http.Request) (machineID string, ok bool) {
	if s.auth == nil {
		writeError(w, http.StatusServiceUnavailable, "workspace integration auth not configured")
		return "", false
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "workspace integration store not configured")
		return "", false
	}
	header := r.Header.Get("Authorization")
	if header == "" {
		writeError(w, http.StatusUnauthorized, "missing authorization")
		return "", false
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		writeError(w, http.StatusUnauthorized, "invalid authorization scheme")
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		writeError(w, http.StatusUnauthorized, "empty bearer token")
		return "", false
	}
	claims, err := s.auth.ValidateWorkspaceIntegrationToken(token)
	if err != nil {
		slog.Warn("workspace_integrations.token_invalid", "error", err)
		writeError(w, http.StatusUnauthorized, "invalid workspace integration token")
		return "", false
	}
	machine, err := s.store.GetMachine(r.Context(), claims.MachineID)
	if err != nil {
		slog.Warn("workspace_integrations.machine_not_found", "machine_id_hash", hashForLog(claims.MachineID), "error", err)
		writeError(w, http.StatusForbidden, "machine not found")
		return "", false
	}
	if machine.WorkspaceIntegrationTokensValidAfter != nil && workspaceIntegrationTokenIssuedAt(claims).Before(*machine.WorkspaceIntegrationTokensValidAfter) {
		slog.Warn("workspace_integrations.token_revoked", "machine_id_hash", hashForLog(claims.MachineID))
		writeError(w, http.StatusUnauthorized, "workspace integration token has been revoked")
		return "", false
	}
	workspace, err := s.store.GetWorkspaceForMachine(r.Context(), claims.MachineID)
	if err != nil {
		slog.Warn("workspace_integrations.machine_workspace_not_found", "machine_id_hash", hashForLog(claims.MachineID), "error", err)
		writeError(w, http.StatusForbidden, "machine workspace not found")
		return "", false
	}
	if workspace != nil {
		*r = *r.WithContext(context.WithValue(r.Context(), workspaceIntegrationWorkspaceContextKey{}, workspace))
	}
	runtimeEnabled, err := s.workspaceIntegrationRuntimeEnabled(r.Context(), claims.MachineID)
	if err != nil {
		slog.Error("workspace_integrations.runtime_check_failed", "machine_id_hash", hashForLog(claims.MachineID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to validate workspace integration runtime")
		return "", false
	}
	if !runtimeEnabled {
		slog.Warn("workspace_integrations.runtime_disabled", "machine_id_hash", hashForLog(claims.MachineID))
		writeError(w, http.StatusForbidden, "workspace integration runtime disabled")
		return "", false
	}
	return claims.MachineID, true
}

func workspaceIntegrationTokenIssuedAt(claims *auth.WorkspaceIntegrationClaims) time.Time {
	if claims != nil && claims.IssuedAtUnixNano > 0 {
		return time.Unix(0, claims.IssuedAtUnixNano).UTC()
	}
	if claims != nil && claims.IssuedAt != nil {
		return claims.IssuedAt.Time
	}
	return time.Time{}
}

func (s *Server) workspaceIntegrationRuntimeEnabled(ctx context.Context, machineID string) (bool, error) {
	plugins, err := s.store.ListMachinePlugins(ctx, machineID)
	if err != nil {
		return false, err
	}
	for _, plugin := range plugins {
		if plugin.PluginID == workspaceIntegrationPluginID && plugin.Enabled {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) handleWorkspaceIntegrationManifest(w http.ResponseWriter, r *http.Request) {
	machineID, ok := s.requireWorkspaceIntegrationToken(w, r)
	if !ok {
		return
	}
	integrations, err := s.store.ListEnabledWorkspaceIntegrationsForMachine(r.Context(), machineID)
	if err != nil {
		slog.Error("workspace_integrations.list_failed", "machine_id_hash", hashForLog(machineID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list workspace integrations")
		return
	}
	tools := s.buildWorkspaceIntegrationExposedTools(r.Context(), machineID, integrations)
	integrationSummaries := make([]map[string]interface{}, 0, len(integrations))
	for _, integration := range integrations {
		integrationSummaries = append(integrationSummaries, map[string]interface{}{
			"slug":        integration.Slug,
			"displayName": integration.DisplayName,
			"kind":        integration.Kind,
			"transport":   integration.Transport,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":      "1",
		"integrations": integrationSummaries,
		"tools":        tools,
	})
}

func (s *Server) handleWorkspaceIntegrationListTools(w http.ResponseWriter, r *http.Request) {
	machineID, ok := s.requireWorkspaceIntegrationToken(w, r)
	if !ok {
		return
	}
	integrations, err := s.store.ListEnabledWorkspaceIntegrationsForMachine(r.Context(), machineID)
	if err != nil {
		slog.Error("workspace_integrations.list_tools_failed", "machine_id_hash", hashForLog(machineID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list workspace integration tools")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tools": s.buildWorkspaceIntegrationExposedTools(r.Context(), machineID, integrations),
	})
}

func (s *Server) handleWorkspaceIntegrationCallTool(w http.ResponseWriter, r *http.Request) {
	machineID, ok := s.requireWorkspaceIntegrationToken(w, r)
	if !ok {
		return
	}
	requestedTool := chi.URLParam(r, "tool")
	if requestedTool == "" {
		writeError(w, http.StatusBadRequest, "tool is required")
		return
	}

	integrations, err := s.store.ListEnabledWorkspaceIntegrationsForMachine(r.Context(), machineID)
	if err != nil {
		slog.Error("workspace_integrations.call_list_failed", "machine_id_hash", hashForLog(machineID), "tool", requestedTool, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list workspace integration tools")
		return
	}
	var body struct {
		Arguments map[string]interface{} `json:"arguments"`
		Params    map[string]interface{} `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	args := body.Arguments
	if args == nil {
		args = body.Params
	}
	if args == nil {
		args = map[string]interface{}{}
	}

	if isWorkspaceIntegrationFacadeTool(requestedTool) {
		result, status, err := s.callWorkspaceIntegrationFacadeTool(r.Context(), machineID, integrations, requestedTool, args)
		if err != nil {
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}

	integration, tool, descriptor, found := s.resolveWorkspaceIntegrationRuntimeTool(r.Context(), machineID, integrations, requestedTool)
	if !found {
		preDescriptor, preResolution := resolveWorkspaceIntegrationFacadeToolDescriptor(s.buildWorkspaceIntegrationRuntimeToolDescriptors(r.Context(), machineID, integrations, false), requestedTool)
		if preResolution.Found && !preResolution.Ambiguous && preDescriptor.LegacyIntegrationID == "" {
			writeError(w, http.StatusNotImplemented, workspaceIntegrationNormalizedRuntimeUnsupportedError(preDescriptor).Error())
			return
		}
		writeError(w, http.StatusNotFound, "tool not found")
		return
	}
	if descriptor.PolicyState == workspaceIntegrationPolicyRequireApproval || workspaceIntegrationToolPolicyState(*integration, tool.Name) == workspaceIntegrationPolicyRequireApproval {
		writeError(w, http.StatusConflict, workspaceIntegrationApprovalRequiredMessage(requestedTool))
		return
	}

	result, err := s.callWorkspaceIntegrationToolWithLog(r.Context(), machineID, *integration, *tool, args)
	if err != nil {
		writeWorkspaceIntegrationCallErrorForIntegration(w, http.StatusBadGateway, "tool call failed", *integration, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"integration": integration.Slug,
		"tool":        integration.Slug + "." + tool.Name,
		"result":      result,
	})
}

func (s *Server) callWorkspaceIntegrationToolWithLog(ctx context.Context, machineID string, integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool, args map[string]interface{}) (map[string]interface{}, error) {
	return s.callWorkspaceIntegrationToolWithLogMode(ctx, machineID, integration, tool, args, "direct")
}

func (s *Server) callWorkspaceIntegrationToolWithLogMode(ctx context.Context, machineID string, integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool, args map[string]interface{}, callMode string) (map[string]interface{}, error) {
	start := time.Now()
	telemetry := &workspaceIntegrationCallTelemetry{}
	ctx = context.WithValue(ctx, workspaceIntegrationCallTelemetryContextKey{}, telemetry)
	result, err := s.callWorkspaceIntegrationTool(ctx, integration, tool, args)
	latencyMS := int(time.Since(start).Milliseconds())
	attrs := []interface{}{
		"integration", integration.Slug,
		"tool", integration.Slug + "." + tool.Name,
		"transport", normalizeWorkspaceIntegrationTransport(integration.Transport),
		"call_mode", callMode,
		"machine_hash", hashForLog(machineID),
		"latency_ms", latencyMS,
		"retry_count", telemetry.RetryCount,
	}
	if telemetry.UpstreamLatencyMS > 0 {
		attrs = append(attrs, "upstream_latency_ms", telemetry.UpstreamLatencyMS, "ocm_overhead_ms", max(0, latencyMS-telemetry.UpstreamLatencyMS))
	}
	if telemetry.RetryAfterMS != nil {
		attrs = append(attrs, "retry_after_ms", *telemetry.RetryAfterMS)
	}
	if err != nil {
		failureClass := workspaceIntegrationFailureClass(err)
		attrs = append(attrs, "status", "error", "failure_class", failureClass)
		var upstreamStatus *int
		var upstreamErr *workspaceIntegrationUpstreamError
		if errors.As(err, &upstreamErr) {
			upstreamStatus = &upstreamErr.StatusCode
			attrs = append(attrs, "upstream_status", upstreamErr.StatusCode)
		}
		slog.Warn("workspace_integrations.tool_call", attrs...)
		s.recordWorkspaceIntegrationCallEvent(ctx, machineID, integration, tool, args, callMode, "error", failureClass, upstreamStatus, latencyMS, telemetry)
		return nil, err
	}
	attrs = append(attrs, "status", "success")
	slog.Info("workspace_integrations.tool_call", attrs...)
	s.recordWorkspaceIntegrationCallEvent(ctx, machineID, integration, tool, args, callMode, "success", "", nil, latencyMS, telemetry)
	return result, nil
}

func (s *Server) recordWorkspaceIntegrationCallEvent(ctx context.Context, machineID string, integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool, args map[string]interface{}, callMode, status, failureClass string, upstreamStatus *int, latencyMS int, telemetry *workspaceIntegrationCallTelemetry) {
	if s.store == nil || strings.TrimSpace(integration.WorkspaceID) == "" {
		return
	}
	record, sampleRate := workspaceIntegrationShouldRecordCallEvent(status)
	if !record {
		return
	}
	argKeys, argShape := workspaceIntegrationCallArgEvidence(args)
	var integrationID *string
	if strings.TrimSpace(integration.ID) != "" {
		integrationID = &integration.ID
	}
	var machineIDPtr *string
	if strings.TrimSpace(machineID) != "" {
		machineIDPtr = &machineID
	}
	toolAddress := workspaceIntegrationStructuredToolAddress(integration, tool)
	event := &store.WorkspaceIntegrationCallEvent{
		WorkspaceID:     integration.WorkspaceID,
		MachineID:       machineIDPtr,
		IntegrationID:   integrationID,
		IntegrationSlug: integration.Slug,
		ToolName:        tool.Name,
		ToolID:          integration.Slug + "." + tool.Name,
		ToolAddress:     &toolAddress,
		CallMode:        callMode,
		Transport:       normalizeWorkspaceIntegrationTransport(integration.Transport),
		Access:          workspaceIntegrationManifestToolAccess(tool),
		Status:          status,
		UpstreamStatus:  upstreamStatus,
		LatencyMS:       latencyMS,
		ArgKeys:         argKeys,
		ArgShape:        argShape,
		SampleRate:      sampleRate,
		DetailLevel:     "telemetry",
	}
	if telemetry != nil {
		if telemetry.UpstreamLatencyMS > 0 {
			upstreamLatencyMS := telemetry.UpstreamLatencyMS
			event.UpstreamLatencyMS = &upstreamLatencyMS
			ocmOverheadMS := max(0, latencyMS-upstreamLatencyMS)
			event.OCMOverheadMS = &ocmOverheadMS
		}
		event.RetryCount = telemetry.RetryCount
		event.RetryAfterMS = telemetry.RetryAfterMS
		event.Retryable = telemetry.Retryable
		event.Terminal = telemetry.Terminal
	}
	if failureClass != "" {
		event.FailureClass = &failureClass
		if !event.Retryable {
			event.Terminal = true
		}
	}
	select {
	case workspaceIntegrationCallEventWriteSlots <- struct{}{}:
	default:
		dropped := workspaceIntegrationCallEventDropped.Add(1)
		slog.Warn(
			"workspace_integrations.call_event_record_dropped",
			"workspace_id", integration.WorkspaceID,
			"integration", integration.Slug,
			"tool", tool.Name,
			"status", status,
			"dropped_total", dropped,
		)
		return
	}
	writeCtx := context.WithoutCancel(ctx)
	go func() {
		defer func() { <-workspaceIntegrationCallEventWriteSlots }()
		if workspaceIntegrationCallEventWriteTimeout > 0 {
			var cancel context.CancelFunc
			writeCtx, cancel = context.WithTimeout(writeCtx, workspaceIntegrationCallEventWriteTimeout)
			defer cancel()
		}
		if err := s.store.RecordWorkspaceIntegrationCallEvent(writeCtx, event); err != nil {
			slog.Warn("workspace_integrations.call_event_record_failed", "workspace_id", integration.WorkspaceID, "integration", integration.Slug, "tool", tool.Name, "error", err)
		}
	}()
}

func workspaceIntegrationShouldRecordCallEvent(status string) (bool, float64) {
	if status != "success" {
		return true, 1
	}
	rate := workspaceIntegrationSuccessCallEventSampleRate
	switch {
	case rate <= 0:
		return false, 0
	case rate >= 1:
		return true, 1
	default:
		return workspaceIntegrationCallEventSample() < rate, rate
	}
}

func workspaceIntegrationCallArgEvidence(args map[string]interface{}) ([]string, json.RawMessage) {
	if len(args) == 0 {
		return nil, json.RawMessage("{}")
	}
	keys := make([]string, 0, len(args))
	for key := range args {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	shape := make(map[string]interface{}, len(keys))
	for _, key := range keys {
		shape[key] = workspaceIntegrationSanitizedArgShape(key, args[key])
	}
	raw, err := json.Marshal(shape)
	if err != nil {
		return keys, json.RawMessage("{}")
	}
	return keys, raw
}

func workspaceIntegrationSanitizedArgShape(key string, value interface{}) map[string]interface{} {
	out := map[string]interface{}{
		"type": workspaceIntegrationArgShapeType(value),
	}
	switch v := value.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		out["empty"] = trimmed == ""
		out["length_bucket"] = workspaceIntegrationLengthBucket(len(trimmed))
		lowerKey := strings.ToLower(key)
		if lowerKey == "repo" || lowerKey == "repository" || strings.HasSuffix(lowerKey, "_repo") {
			out["repo_format"] = workspaceIntegrationRepoShape(trimmed)
		}
		if strings.Contains(lowerKey, "date") || lowerKey == "start" || lowerKey == "end" || strings.HasSuffix(lowerKey, "_at") {
			out["date_parse"] = workspaceIntegrationDateShape(trimmed)
		}
	case []interface{}:
		out["length_bucket"] = workspaceIntegrationLengthBucket(len(v))
	case map[string]interface{}:
		out["key_count_bucket"] = workspaceIntegrationLengthBucket(len(v))
	case nil:
		out["empty"] = true
	}
	return out
}

func workspaceIntegrationArgShapeType(value interface{}) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return "number"
	case []interface{}:
		return "array"
	case map[string]interface{}:
		return "object"
	default:
		return "other"
	}
}

func workspaceIntegrationLengthBucket(length int) string {
	switch {
	case length <= 0:
		return "0"
	case length <= 10:
		return "1-10"
	case length <= 50:
		return "11-50"
	case length <= 200:
		return "51-200"
	default:
		return "201+"
	}
}

func workspaceIntegrationRepoShape(value string) string {
	if value == "" {
		return "empty"
	}
	parts := strings.Split(value, "/")
	if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
		return "owner_repo"
	}
	if !strings.Contains(value, "/") {
		return "bare_name"
	}
	return "other"
}

func workspaceIntegrationDateShape(value string) string {
	if value == "" {
		return "empty"
	}
	if _, err := time.Parse(time.RFC3339, value); err == nil {
		return "rfc3339"
	}
	if _, err := time.Parse("2006-01-02", value); err == nil {
		return "date"
	}
	return "failed"
}

func validateWorkspaceIntegrationToolArguments(tool workspaceIntegrationManifestTool, args map[string]interface{}) error {
	schema := workspaceIntegrationToolArgumentSchema(tool)
	if len(schema) == 0 {
		return nil
	}
	var root map[string]interface{}
	decoder := json.NewDecoder(strings.NewReader(string(schema)))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil
	}
	if !workspaceIntegrationSchemaHasType(root, "object") {
		return nil
	}
	properties := workspaceIntegrationSchemaProperties(root)
	for _, key := range workspaceIntegrationSchemaStringSlice(root["required"]) {
		if isEmptyWorkspaceIntegrationHTTPValue(args[key]) {
			return &workspaceIntegrationValidationError{
				Code:       "required",
				SchemaPath: "$.required." + key,
				Detail:     fmt.Sprintf("argument %q is required", key),
			}
		}
	}
	if workspaceIntegrationSchemaAdditionalPropertiesFalse(root) && len(properties) > 0 {
		for key := range args {
			if _, ok := properties[key]; !ok {
				return &workspaceIntegrationValidationError{
					Code:       "additional_properties",
					SchemaPath: "$.properties." + key,
					Detail:     fmt.Sprintf("argument %q is not supported by this tool", key),
				}
			}
		}
	}
	for key, prop := range properties {
		value, ok := args[key]
		if !ok || isEmptyWorkspaceIntegrationHTTPValue(value) {
			continue
		}
		if err := validateWorkspaceIntegrationSchemaValue(key, value, prop); err != nil {
			return err
		}
	}
	return nil
}

func workspaceIntegrationToolArgumentSchema(tool workspaceIntegrationManifestTool) json.RawMessage {
	for _, schema := range []json.RawMessage{tool.Parameters, tool.InputSchema, tool.Schema} {
		if len(schema) > 0 && strings.TrimSpace(string(schema)) != "" {
			return schema
		}
	}
	return nil
}

func validateWorkspaceIntegrationSchemaValue(key string, value interface{}, schema map[string]interface{}) error {
	types := workspaceIntegrationSchemaStringSlice(schema["type"])
	if len(types) > 0 && !workspaceIntegrationValueMatchesAnySchemaType(value, types) {
		return &workspaceIntegrationValidationError{
			Code:       "type",
			SchemaPath: "$.properties." + key + ".type",
			Detail:     fmt.Sprintf("argument %q has the wrong type", key),
		}
	}
	if enumValues, ok := schema["enum"].([]interface{}); ok && len(enumValues) > 0 {
		for _, allowed := range enumValues {
			if workspaceIntegrationEnumValueMatches(value, allowed) {
				return nil
			}
		}
		return &workspaceIntegrationValidationError{
			Code:       "enum",
			SchemaPath: "$.properties." + key + ".enum",
			Detail:     fmt.Sprintf("argument %q must be one of the allowed values", key),
		}
	}
	return nil
}

func workspaceIntegrationSchemaHasType(schema map[string]interface{}, want string) bool {
	types := workspaceIntegrationSchemaStringSlice(schema["type"])
	if len(types) == 0 {
		return false
	}
	for _, typ := range types {
		if typ == want {
			return true
		}
	}
	return false
}

func workspaceIntegrationSchemaProperties(schema map[string]interface{}) map[string]map[string]interface{} {
	raw, ok := schema["properties"].(map[string]interface{})
	if !ok {
		return nil
	}
	out := make(map[string]map[string]interface{}, len(raw))
	for key, value := range raw {
		prop, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		out[key] = prop
	}
	return out
}

func workspaceIntegrationSchemaAdditionalPropertiesFalse(schema map[string]interface{}) bool {
	value, ok := schema["additionalProperties"].(bool)
	return ok && !value
}

func workspaceIntegrationSchemaStringSlice(raw interface{}) []string {
	switch value := raw.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return []string{strings.TrimSpace(value)}
	case []interface{}:
		out := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	default:
		return nil
	}
}

func workspaceIntegrationValueMatchesAnySchemaType(value interface{}, types []string) bool {
	for _, typ := range types {
		switch typ {
		case "string":
			if _, ok := value.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := value.(bool); ok {
				return true
			}
		case "number":
			if workspaceIntegrationValueIsNumber(value) {
				return true
			}
		case "integer":
			if workspaceIntegrationValueIsInteger(value) {
				return true
			}
		case "array":
			if _, ok := value.([]interface{}); ok {
				return true
			}
		case "object":
			if _, ok := value.(map[string]interface{}); ok {
				return true
			}
		case "null":
			if value == nil {
				return true
			}
		}
	}
	return false
}

func workspaceIntegrationValueIsNumber(value interface{}) bool {
	switch value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return true
	default:
		return false
	}
}

func workspaceIntegrationValueIsInteger(value interface{}) bool {
	switch v := value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	case float32:
		return v == float32(int64(v))
	case float64:
		return v == float64(int64(v))
	case json.Number:
		if _, err := v.Int64(); err == nil {
			return true
		}
		return false
	default:
		return false
	}
}

func workspaceIntegrationEnumValueMatches(value, allowed interface{}) bool {
	switch v := value.(type) {
	case json.Number:
		value = v.String()
	}
	switch a := allowed.(type) {
	case json.Number:
		allowed = a.String()
	}
	return fmt.Sprint(value) == fmt.Sprint(allowed)
}

func workspaceIntegrationFailureClass(err error) string {
	if err == nil {
		return ""
	}
	var validationErr *workspaceIntegrationValidationError
	if errors.As(err, &validationErr) {
		return "invalid_arguments"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "credential not configured"):
		return "credential_not_configured"
	case strings.Contains(message, "decrypt"):
		return "credential_decrypt_failed"
	case strings.Contains(message, "oauth"):
		return "oauth_failed"
	case strings.Contains(message, "upstream returned 429"):
		return "rate_limited"
	case strings.Contains(message, "upstream returned"):
		return "upstream_http_status"
	case strings.Contains(message, "mcp-remote"):
		return "mcp_remote_failed"
	case strings.Contains(message, "unsupported"), strings.Contains(message, "not implemented"):
		return "unsupported_tool"
	default:
		return "tool_call_failed"
	}
}

var (
	workspaceIntegrationBearerRedactPattern   = regexp.MustCompile(`(?i)Bearer\s+[^\s"']+`)
	workspaceIntegrationURLQueryRedactPattern = regexp.MustCompile(`\?[^\s"']+`)
)

// workspaceIntegrationClientErrorMessage gives the caller (agent/UI) a useful,
// actionable reason WITHOUT leaking upstream response bodies — those can echo
// secrets, so the full detail stays in server logs only. Upstream failures
// surface the HTTP status + a category; everything else surfaces a class-based
// reason or the redacted error text for our own (non-upstream) diagnostics.
func workspaceIntegrationClientErrorMessage(prefix string, err error) string {
	return workspaceIntegrationClientErrorMessageWithProvider(prefix, "", "", err)
}

func workspaceIntegrationClientErrorMessageForIntegration(prefix string, integration store.WorkspaceIntegration, err error) string {
	return workspaceIntegrationClientErrorMessageWithProvider(prefix, integration.Kind, integration.Slug, err)
}

func workspaceIntegrationClientErrorMessageWithProvider(prefix, providerKind, providerSlug string, err error) string {
	var upstreamErr *workspaceIntegrationUpstreamError
	if errors.As(err, &upstreamErr) {
		if message, ok := workspaceIntegrationProviderUpstreamErrorMessage(prefix, providerKind, providerSlug, upstreamErr); ok {
			return message
		}
		return fmt.Sprintf("%s: upstream returned HTTP %d%s", prefix, upstreamErr.StatusCode, workspaceIntegrationHTTPStatusHint(upstreamErr.StatusCode))
	}
	switch workspaceIntegrationFailureClass(err) {
	case "rate_limited":
		return prefix + ": upstream rate limited (HTTP 429); retry shortly"
	case "credential_not_configured":
		return prefix + ": integration credential is not configured"
	case "credential_decrypt_failed":
		return prefix + ": integration credential could not be decrypted"
	case "oauth_failed":
		return prefix + ": integration authentication (OAuth) failed"
	case "mcp_remote_failed":
		return prefix + ": the remote MCP server returned an error"
	case "upstream_http_status":
		return prefix + ": upstream returned an error status"
	case "unsupported_tool":
		return prefix + ": this tool is not supported"
	default:
		// Our own diagnostics (bad args, decode, connectivity): safe to surface
		// after stripping any credential that could ride along.
		msg := workspaceIntegrationBearerRedactPattern.ReplaceAllString(err.Error(), "Bearer [REDACTED]")
		msg = workspaceIntegrationURLQueryRedactPattern.ReplaceAllString(msg, "?[REDACTED]")
		return prefix + ": " + workspaceIntegrationCollapseSnippet(msg, 200)
	}
}

func writeWorkspaceIntegrationCallErrorForIntegration(w http.ResponseWriter, status int, prefix string, integration store.WorkspaceIntegration, err error) {
	writeJSON(w, status, map[string]interface{}{
		"error":          workspaceIntegrationClientErrorMessageForIntegration(prefix, integration, err),
		"error_contract": workspaceIntegrationErrorContractForIntegration(prefix, integration, err),
	})
}

func workspaceIntegrationErrorContractForIntegration(prefix string, integration store.WorkspaceIntegration, err error) workspaceIntegrationErrorContract {
	return workspaceIntegrationErrorContractForProvider(prefix, integration.Kind, integration.Slug, err)
}

func workspaceIntegrationErrorContractForProvider(prefix, providerKind, providerSlug string, err error) workspaceIntegrationErrorContract {
	class := workspaceIntegrationFailureClass(err)
	contract := workspaceIntegrationErrorContract{
		Class:     class,
		Terminal:  true,
		Action:    "inspect_error",
		Detail:    workspaceIntegrationClientErrorMessageWithProvider(prefix, providerKind, providerSlug, err),
		Retryable: false,
	}
	var upstreamErr *workspaceIntegrationUpstreamError
	if errors.As(err, &upstreamErr) {
		contract.Retryable = upstreamErr.Retryable
		contract.RetryAfterMS = upstreamErr.RetryAfterMS
		contract.Terminal = upstreamErr.Terminal
		if action, ok := workspaceIntegrationProviderUpstreamErrorAction(providerKind, providerSlug, upstreamErr); ok {
			contract.Action = action
		} else if upstreamErr.StatusCode == http.StatusUnauthorized {
			contract.Action = "reconnect_integration"
		} else if upstreamErr.StatusCode == http.StatusForbidden {
			contract.Action = "grant_scope_or_reconnect"
		} else if upstreamErr.StatusCode == http.StatusTooManyRequests {
			if upstreamErr.Retryable {
				contract.Action = "retry_after"
			} else {
				contract.Action = "do_not_auto_retry_write"
			}
		} else if upstreamErr.StatusCode >= 500 {
			contract.Action = "retry_later"
		} else {
			contract.Action = "check_upstream_request"
		}
		return contract
	}
	var validationErr *workspaceIntegrationValidationError
	if errors.As(err, &validationErr) {
		contract.Action = "fix_arguments"
		contract.Detail = validationErr.Error()
		return contract
	}
	switch class {
	case "credential_not_configured", "credential_decrypt_failed", "oauth_failed":
		contract.Action = "reconnect_integration"
	case "unsupported_tool":
		contract.Action = "choose_supported_tool"
	}
	return contract
}

func workspaceIntegrationProviderUpstreamErrorMessage(prefix, providerKind, providerSlug string, upstreamErr *workspaceIntegrationUpstreamError) (string, bool) {
	if upstreamErr == nil || !workspaceIntegrationIsGoogleWorkspaceProvider(providerKind, providerSlug) {
		return "", false
	}
	switch upstreamErr.StatusCode {
	case http.StatusUnauthorized:
		return prefix + ": Google Workspace authentication failed (HTTP 401); reconnect the Google account", true
	case http.StatusForbidden:
		switch workspaceIntegrationGoogleWorkspaceForbiddenReason(upstreamErr.Snippet) {
		case "missing_scope":
			return prefix + ": Google Workspace denied the request (HTTP 403); reconnect and grant the required Gmail, Drive, or Calendar scope", true
		case "admin_policy":
			return prefix + ": Google Workspace admin policy or API access blocked this request (HTTP 403); ask a Google Workspace admin to allow the app and API", true
		default:
			return prefix + ": Google Workspace denied the request (HTTP 403); reconnect with the required scope or ask a Google Workspace admin to allow access", true
		}
	default:
		return "", false
	}
}

func workspaceIntegrationProviderUpstreamErrorAction(providerKind, providerSlug string, upstreamErr *workspaceIntegrationUpstreamError) (string, bool) {
	if upstreamErr == nil || !workspaceIntegrationIsGoogleWorkspaceProvider(providerKind, providerSlug) {
		return "", false
	}
	switch upstreamErr.StatusCode {
	case http.StatusUnauthorized:
		return "reconnect_integration", true
	case http.StatusForbidden:
		if workspaceIntegrationGoogleWorkspaceForbiddenReason(upstreamErr.Snippet) == "admin_policy" {
			return "ask_google_workspace_admin", true
		}
		return "grant_scope_or_reconnect", true
	default:
		return "", false
	}
}

func workspaceIntegrationIsGoogleWorkspaceProvider(providerKind, providerSlug string) bool {
	return strings.EqualFold(strings.TrimSpace(providerKind), "google_workspace") ||
		strings.EqualFold(strings.TrimSpace(providerSlug), googleWorkspaceIntegrationSlug)
}

func workspaceIntegrationGoogleWorkspaceForbiddenReason(snippet string) string {
	lower := strings.ToLower(snippet)
	switch {
	case strings.Contains(lower, "access_token_scope_insufficient"),
		strings.Contains(lower, "insufficientpermissions"),
		strings.Contains(lower, "insufficient authentication scope"),
		strings.Contains(lower, "insufficient oauth scope"),
		strings.Contains(lower, "insufficient permission"):
		return "missing_scope"
	case strings.Contains(lower, "domainpolicy"),
		strings.Contains(lower, "admin policy"),
		strings.Contains(lower, "workspace admin"),
		strings.Contains(lower, "app_not_configured_for_user"),
		strings.Contains(lower, "admin_policy_enforced"),
		strings.Contains(lower, "accessnotconfigured"),
		strings.Contains(lower, "api has not been used"),
		strings.Contains(lower, "api access blocked"),
		strings.Contains(lower, "access blocked by your google workspace admin"):
		return "admin_policy"
	default:
		return ""
	}
}

func workspaceIntegrationHTTPStatusHint(code int) string {
	switch code {
	case http.StatusUnauthorized:
		return " (authentication required — reconnect the integration)"
	case http.StatusForbidden:
		return " (forbidden — the credential lacks access)"
	case http.StatusNotFound:
		return " (not found)"
	case http.StatusTooManyRequests:
		return " (rate limited — retry shortly)"
	default:
		if code >= 500 {
			return " (upstream server error)"
		}
		return ""
	}
}

func (s *Server) callWorkspaceIntegrationTool(ctx context.Context, integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool, args map[string]interface{}) (map[string]interface{}, error) {
	adapter, ok := workspaceIntegrationRuntimeAdapterFor(integration, tool)
	if !ok {
		return nil, fmt.Errorf("workspace integration transport %q not implemented", integration.Transport)
	}
	if err := validateWorkspaceIntegrationToolArguments(tool, args); err != nil {
		return nil, err
	}
	result, err := adapter.Call(ctx, s, integration, tool, args)
	if err != nil {
		return nil, err
	}
	return s.redactWorkspaceIntegrationToolResult(ctx, integration, result)
}

type workspaceIntegrationRuntimeAdapter interface {
	Name() string
	Supports(integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool) bool
	Call(ctx context.Context, s *Server, integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool, args map[string]interface{}) (map[string]interface{}, error)
}

type workspaceIntegrationRuntimeAdapterFunc struct {
	name     string
	supports func(integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool) bool
	call     func(ctx context.Context, s *Server, integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool, args map[string]interface{}) (map[string]interface{}, error)
}

func (a workspaceIntegrationRuntimeAdapterFunc) Name() string {
	return a.name
}

func (a workspaceIntegrationRuntimeAdapterFunc) Supports(integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool) bool {
	return a.supports(integration, tool)
}

func (a workspaceIntegrationRuntimeAdapterFunc) Call(ctx context.Context, s *Server, integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool, args map[string]interface{}) (map[string]interface{}, error) {
	return a.call(ctx, s, integration, tool, args)
}

var workspaceIntegrationRuntimeAdapters = []workspaceIntegrationRuntimeAdapter{
	workspaceIntegrationRuntimeAdapterFunc{
		name: "mock",
		supports: func(integration store.WorkspaceIntegration, _ workspaceIntegrationManifestTool) bool {
			return normalizeWorkspaceIntegrationTransport(integration.Transport) == "mock"
		},
		call: func(_ context.Context, _ *Server, integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool, args map[string]interface{}) (map[string]interface{}, error) {
			if strings.EqualFold(tool.Name, "echo") {
				return map[string]interface{}{"echo": args, "integration_id": integration.ID, "integration_slug": integration.Slug}, nil
			}
			return nil, fmt.Errorf("unsupported mock tool %q", tool.Name)
		},
	},
	workspaceIntegrationRuntimeAdapterFunc{
		name: "mcp",
		supports: func(integration store.WorkspaceIntegration, _ workspaceIntegrationManifestTool) bool {
			return normalizeWorkspaceIntegrationTransport(integration.Transport) == "mcp-remote"
		},
		call: func(ctx context.Context, s *Server, integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool, args map[string]interface{}) (map[string]interface{}, error) {
			return s.callMCPRemoteWorkspaceIntegration(ctx, integration, tool, args)
		},
	},
	workspaceIntegrationRuntimeAdapterFunc{
		name: "http",
		supports: func(integration store.WorkspaceIntegration, _ workspaceIntegrationManifestTool) bool {
			return normalizeWorkspaceIntegrationTransport(integration.Transport) == "http"
		},
		call: func(ctx context.Context, s *Server, integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool, args map[string]interface{}) (map[string]interface{}, error) {
			return s.callHTTPWorkspaceIntegration(ctx, integration, tool, args)
		},
	},
}

func workspaceIntegrationRuntimeAdapterFor(integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool) (workspaceIntegrationRuntimeAdapter, bool) {
	for _, adapter := range workspaceIntegrationRuntimeAdapters {
		if adapter.Supports(integration, tool) {
			return adapter, true
		}
	}
	return nil, false
}

func normalizeWorkspaceIntegrationTransport(transport string) string {
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "rest":
		return "http"
	default:
		return strings.ToLower(strings.TrimSpace(transport))
	}
}

func (s *Server) buildWorkspaceIntegrationExposedTools(ctx context.Context, machineID string, integrations []store.WorkspaceIntegration) []workspaceIntegrationTool {
	descriptors := s.buildWorkspaceIntegrationRuntimeToolDescriptors(ctx, machineID, integrations, false)
	if len(descriptors) == 0 {
		return workspaceIntegrationFacadeTools()
	}
	direct := buildWorkspaceIntegrationToolsFromDescriptors(descriptors)
	out := make([]workspaceIntegrationTool, 0, len(direct)+3)
	out = append(out, workspaceIntegrationFacadeTools()...)
	out = append(out, direct...)
	return out
}

func (s *Server) buildWorkspaceIntegrationRuntimeToolDescriptors(ctx context.Context, machineID string, integrations []store.WorkspaceIntegration, includeBlocked bool) []workspaceIntegrationToolDescriptor {
	descriptors, _ := s.buildWorkspaceIntegrationRuntimeToolDescriptorsWithSource(ctx, machineID, integrations, includeBlocked)
	return descriptors
}

func (s *Server) buildWorkspaceIntegrationRuntimeToolDescriptorsWithSource(ctx context.Context, machineID string, integrations []store.WorkspaceIntegration, includeBlocked bool) ([]workspaceIntegrationToolDescriptor, bool) {
	workspaceID := workspaceIntegrationWorkspaceIDFromIntegrations(integrations)
	accountID := 0
	if workspace := workspaceIntegrationWorkspaceFromContext(ctx); workspace != nil {
		workspaceID = workspace.ID
		accountID = workspace.AccountID
	}
	reader, ok := s.store.(workspaceIntegrationConnectorProjectionReader)
	if ok {
		if workspaceID != "" {
			projections, err := reader.ListWorkspaceIntegrationConnectorProjections(ctx, workspaceID)
			if err != nil {
				slog.Warn("workspace_integrations.connector_projection_list_failed", "workspace_id", workspaceID, "error", err)
			} else if len(projections) > 0 {
				descriptors := buildWorkspaceIntegrationToolDescriptorsFromConnectorProjections(projections, includeBlocked)
				s.applyWorkspaceIntegrationGuidanceOverlays(ctx, accountID, workspaceID, descriptors)
				return descriptors, true
			}
		}
	}
	descriptors := buildWorkspaceIntegrationToolDescriptors(integrations, includeBlocked)
	s.applyWorkspaceIntegrationGuidanceOverlays(ctx, accountID, workspaceID, descriptors)
	return descriptors, false
}

func workspaceIntegrationWorkspaceIDFromIntegrations(integrations []store.WorkspaceIntegration) string {
	for _, integration := range integrations {
		if strings.TrimSpace(integration.WorkspaceID) != "" {
			return integration.WorkspaceID
		}
	}
	return ""
}

func (s *Server) applyWorkspaceIntegrationGuidanceOverlays(ctx context.Context, accountID int, workspaceID string, descriptors []workspaceIntegrationToolDescriptor) {
	if accountID == 0 || workspaceID == "" {
		return
	}
	reader, ok := s.store.(workspaceIntegrationGuidanceOverlayReader)
	if !ok {
		return
	}
	overlays, err := reader.ListWorkspaceIntegrationGuidanceOverlays(ctx, accountID, workspaceID, "approved")
	if err != nil {
		slog.Warn("workspace_integrations.guidance_overlay_list_failed", "workspace_id", workspaceID, "error", err)
		return
	}
	latest := make(map[string]store.WorkspaceIntegrationGuidanceOverlay, len(overlays))
	for _, overlay := range overlays {
		for _, key := range workspaceIntegrationGuidanceOverlayKeys(overlay) {
			current, ok := latest[key]
			if !ok || overlay.Version > current.Version {
				latest[key] = overlay
			}
		}
	}
	for i := range descriptors {
		var overlay store.WorkspaceIntegrationGuidanceOverlay
		var ok bool
		if strings.TrimSpace(descriptors[i].ToolAddress) != "" {
			overlay, ok = latest[strings.TrimSpace(descriptors[i].ToolAddress)]
		}
		if !ok && strings.TrimSpace(descriptors[i].ToolID) != "" {
			overlay, ok = latest[strings.TrimSpace(descriptors[i].ToolID)]
		}
		if !ok {
			continue
		}
		descriptors[i].Guidance = &workspaceIntegrationToolGuidance{
			Version:            overlay.Version,
			Text:               overlay.Guidance,
			SourceFailureClass: overlay.SourceFailureClass,
		}
		if !strings.Contains(descriptors[i].Description, overlay.Guidance) {
			if strings.TrimSpace(descriptors[i].Description) == "" {
				descriptors[i].Description = "Workspace guidance: " + overlay.Guidance
			} else {
				descriptors[i].Description = strings.TrimSpace(descriptors[i].Description) + "\n\nWorkspace guidance: " + overlay.Guidance
			}
		}
	}
}

func workspaceIntegrationGuidanceOverlayKeys(overlay store.WorkspaceIntegrationGuidanceOverlay) []string {
	if overlay.ToolAddress != nil && strings.TrimSpace(*overlay.ToolAddress) != "" {
		return []string{strings.TrimSpace(*overlay.ToolAddress)}
	}
	if strings.TrimSpace(overlay.ToolID) != "" {
		return []string{strings.TrimSpace(overlay.ToolID)}
	}
	return nil
}

func buildWorkspaceIntegrationToolsFromDescriptors(descriptors []workspaceIntegrationToolDescriptor) []workspaceIntegrationTool {
	out := make([]workspaceIntegrationTool, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor.PolicyState == workspaceIntegrationPolicyBlock {
			continue
		}
		if !workspaceIntegrationDescriptorHasDirectRuntime(descriptor) {
			continue
		}
		name := descriptor.ToolID
		if strings.TrimSpace(name) == "" {
			name = descriptor.ToolAddress
		}
		if strings.TrimSpace(name) == "" {
			continue
		}
		out = append(out, workspaceIntegrationTool{
			Name:        name,
			Description: descriptor.Description,
			Parameters:  descriptor.InputSchema,
			Integration: descriptor.IntegrationSlug,
			Kind:        descriptor.SourceKind,
			Transport:   descriptor.Source,
			Access:      descriptor.Access,
			Policy:      descriptor.Policy,
			Source:      descriptor.Source,
		})
	}
	return out
}

func workspaceIntegrationDescriptorHasDirectRuntime(descriptor workspaceIntegrationToolDescriptor) bool {
	return strings.TrimSpace(descriptor.LegacyIntegrationID) != "" || workspaceIntegrationDescriptorSupportsNormalizedRuntime(descriptor)
}

func workspaceIntegrationDescriptorSupportsNormalizedRuntime(descriptor workspaceIntegrationToolDescriptor) bool {
	return strings.EqualFold(strings.TrimSpace(descriptor.Source), "mock") ||
		strings.EqualFold(strings.TrimSpace(descriptor.SourceKind), "mock")
}

func workspaceIntegrationFacadeTools() []workspaceIntegrationTool {
	return []workspaceIntegrationTool{
		{
			Name:        workspaceIntegrationSearchToolsName,
			Description: "Semantically search workspace integration tools by compact metadata before loading schemas.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Natural language or keyword search for a tool."},"integration":{"type":"string","description":"Optional integration slug filter."},"access":{"type":"string","enum":["any","read","write"],"default":"any"},"method":{"type":"string","enum":["semantic","keyword","hybrid"],"default":"semantic","description":"Search ranking method. semantic expands common intent and provider terms; keyword performs literal metadata matching; hybrid combines both."},"limit":{"type":"integer","minimum":1,"maximum":50,"default":10}},"additionalProperties":false}`),
			Integration: workspaceIntegrationFacadeSlug,
			Kind:        "facade",
			Transport:   "facade",
			Access:      "read",
			Policy:      "allowed",
			Source:      "ocm",
		},
		{
			Name:        workspaceIntegrationDescribeToolName,
			Description: "Load the full schema and policy metadata for one workspace integration tool.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"tool_address":{"type":"string","description":"Canonical workspace-qualified tool address returned by search_tools, for example wi.<workspace_id>.github.github-main.search_code."},"tool_id":{"type":"string","description":"Legacy alias accepted only when it resolves to exactly one enabled workspace tool."}},"anyOf":[{"required":["tool_address"]},{"required":["tool_id"]}],"additionalProperties":false}`),
			Integration: workspaceIntegrationFacadeSlug,
			Kind:        "facade",
			Transport:   "facade",
			Access:      "read",
			Policy:      "allowed",
			Source:      "ocm",
		},
		{
			Name:        workspaceIntegrationCallToolName,
			Description: "Call one workspace integration tool through OCM policy, logging, and redaction.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"tool_address":{"type":"string","description":"Canonical workspace-qualified tool address returned by search_tools."},"tool_id":{"type":"string","description":"Legacy alias accepted only when it resolves to exactly one enabled workspace tool."},"arguments":{"type":"object","description":"Arguments matching the selected tool schema.","additionalProperties":true}},"anyOf":[{"required":["tool_address"]},{"required":["tool_id"]}],"additionalProperties":false}`),
			Integration: workspaceIntegrationFacadeSlug,
			Kind:        "facade",
			Transport:   "facade",
			Access:      "write",
			Policy:      "allowed",
			Source:      "ocm",
		},
	}
}

func isWorkspaceIntegrationFacadeTool(name string) bool {
	switch name {
	case workspaceIntegrationSearchToolsName, workspaceIntegrationDescribeToolName, workspaceIntegrationCallToolName:
		return true
	default:
		return false
	}
}

func (s *Server) callWorkspaceIntegrationFacadeTool(ctx context.Context, machineID string, integrations []store.WorkspaceIntegration, name string, args map[string]interface{}) (map[string]interface{}, int, error) {
	switch name {
	case workspaceIntegrationSearchToolsName:
		items, err := searchWorkspaceIntegrationFacadeToolsFromDescriptors(s.buildWorkspaceIntegrationRuntimeToolDescriptors(ctx, machineID, integrations, false), args)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		return map[string]interface{}{"items": items}, http.StatusOK, nil
	case workspaceIntegrationDescribeToolName:
		requestedTool, err := workspaceIntegrationFacadeRequestedTool(args)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		descriptor, resolution := resolveWorkspaceIntegrationFacadeToolDescriptor(s.buildWorkspaceIntegrationRuntimeToolDescriptors(ctx, machineID, integrations, false), requestedTool)
		if resolution.Ambiguous {
			return nil, http.StatusConflict, workspaceIntegrationAmbiguousToolError(requestedTool, resolution.Candidates)
		}
		if !resolution.Found {
			return nil, http.StatusNotFound, errors.New("tool not found")
		}
		return map[string]interface{}{
			"tool_id":               descriptor.ToolID,
			"tool_address":          descriptor.ToolAddress,
			"legacy_tool_id":        descriptor.LegacyToolID,
			"legacy_integration_id": descriptor.LegacyIntegrationID,
			"integration_slug":      descriptor.IntegrationSlug,
			"integration_name":      descriptor.IntegrationName,
			"source_slug":           descriptor.SourceSlug,
			"source_kind":           descriptor.SourceKind,
			"connection_id":         descriptor.ConnectionID,
			"connection_slug":       descriptor.ConnectionSlug,
			"connection_scope":      descriptor.ConnectionScope,
			"snapshot_id":           descriptor.SnapshotID,
			"tools_synced_at":       descriptor.ToolsSyncedAt,
			"name":                  descriptor.Name,
			"description":           descriptor.Description,
			"input_schema":          jsonRawMessageOrDefault(descriptor.InputSchema),
			"access":                descriptor.Access,
			"policy":                descriptor.Policy,
			"policy_state":          descriptor.PolicyState,
			"auth_state":            descriptor.AuthState,
			"source":                descriptor.Source,
			"guidance":              descriptor.Guidance,
		}, http.StatusOK, nil
	case workspaceIntegrationCallToolName:
		requestedTool, err := workspaceIntegrationFacadeRequestedTool(args)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		callArgs, err := workspaceIntegrationFacadeArguments(args["arguments"])
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		// Build descriptors once and share them between the ambiguity check and
		// the resolution below, rather than rebuilding (and re-running the
		// projection + guidance-overlay queries) for each.
		descriptors, projectionAuthoritative := s.buildWorkspaceIntegrationRuntimeToolDescriptorsWithSource(ctx, machineID, integrations, false)
		_, resolution := resolveWorkspaceIntegrationFacadeToolDescriptor(descriptors, requestedTool)
		if resolution.Ambiguous {
			return nil, http.StatusConflict, workspaceIntegrationAmbiguousToolError(requestedTool, resolution.Candidates)
		}
		integration, tool, descriptor, found := s.resolveWorkspaceIntegrationRuntimeToolFromDescriptors(ctx, descriptors, projectionAuthoritative, integrations, requestedTool)
		if !found {
			if resolution.Found && descriptor.LegacyIntegrationID == "" {
				return nil, http.StatusNotImplemented, workspaceIntegrationNormalizedRuntimeUnsupportedError(descriptor)
			}
			return nil, http.StatusNotFound, errors.New("tool not found")
		}
		if descriptor.PolicyState == workspaceIntegrationPolicyRequireApproval || workspaceIntegrationToolPolicyState(*integration, tool.Name) == workspaceIntegrationPolicyRequireApproval {
			return nil, http.StatusConflict, errors.New(workspaceIntegrationApprovalRequiredMessage(firstNonEmpty(descriptor.ToolAddress, requestedTool)))
		}
		result, err := s.callWorkspaceIntegrationToolWithLogMode(ctx, machineID, *integration, *tool, callArgs, "facade")
		if err != nil {
			return nil, http.StatusBadGateway, errors.New(workspaceIntegrationClientErrorMessageForIntegration("tool call failed", *integration, err))
		}
		return map[string]interface{}{
			"tool_id":      descriptor.ToolID,
			"tool_address": descriptor.ToolAddress,
			"result":       result,
		}, http.StatusOK, nil
	default:
		return nil, http.StatusNotFound, errors.New("tool not found")
	}
}

func workspaceIntegrationNormalizedRuntimeUnsupportedError(descriptor workspaceIntegrationToolDescriptor) error {
	target := firstNonEmpty(descriptor.ToolAddress, descriptor.ToolID, descriptor.Name)
	if target == "" {
		target = "selected tool"
	}
	return fmt.Errorf("normalized runtime dispatch is not implemented for %s; this connection still requires a legacy workspace integration runtime row", target)
}

func workspaceIntegrationFacadeRequestedTool(args map[string]interface{}) (string, error) {
	if args == nil {
		return "", errors.New("tool_address or tool_id is required")
	}
	if toolAddress := strings.TrimSpace(workspaceIntegrationArgString(args["tool_address"])); toolAddress != "" {
		return toolAddress, nil
	}
	if toolID := strings.TrimSpace(workspaceIntegrationArgString(args["tool_id"])); toolID != "" {
		return toolID, nil
	}
	return "", errors.New("tool_address or tool_id is required")
}

func workspaceIntegrationFacadeArguments(raw interface{}) (map[string]interface{}, error) {
	if raw == nil {
		return map[string]interface{}{}, nil
	}
	args, ok := raw.(map[string]interface{})
	if !ok {
		return nil, errors.New("arguments must be an object")
	}
	return args, nil
}

func searchWorkspaceIntegrationFacadeTools(integrations []store.WorkspaceIntegration, args map[string]interface{}) ([]map[string]interface{}, error) {
	return searchWorkspaceIntegrationFacadeToolsFromDescriptors(buildWorkspaceIntegrationToolDescriptors(integrations, false), args)
}

func searchWorkspaceIntegrationFacadeToolsFromDescriptors(descriptors []workspaceIntegrationToolDescriptor, args map[string]interface{}) ([]map[string]interface{}, error) {
	query := strings.TrimSpace(workspaceIntegrationArgString(args["query"]))
	integrationFilter := strings.TrimSpace(workspaceIntegrationArgString(args["integration"]))
	accessFilter := strings.ToLower(strings.TrimSpace(workspaceIntegrationArgString(args["access"])))
	if accessFilter == "" {
		accessFilter = "any"
	}
	if accessFilter != "any" && accessFilter != "read" && accessFilter != "write" {
		return nil, fmt.Errorf("access must be any, read, or write")
	}
	method, err := workspaceIntegrationSearchMethod(args["method"])
	if err != nil {
		return nil, err
	}
	limit := workspaceIntegrationSearchLimit(args["limit"])
	type scoredDescriptor struct {
		descriptor workspaceIntegrationToolDescriptor
		score      float64
	}
	scored := make([]scoredDescriptor, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if integrationFilter != "" && !workspaceIntegrationDescriptorMatchesIntegrationFilter(descriptor, integrationFilter) {
			continue
		}
		if accessFilter != "any" && descriptor.Access != accessFilter {
			continue
		}
		score := workspaceIntegrationSearchScore(descriptor, query, method)
		if query != "" && score <= 0 {
			continue
		}
		scored = append(scored, scoredDescriptor{descriptor: descriptor, score: score})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].descriptor.ToolID < scored[j].descriptor.ToolID
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	items := make([]map[string]interface{}, 0, len(scored))
	for _, item := range scored {
		items = append(items, map[string]interface{}{
			"tool_id":               item.descriptor.ToolID,
			"tool_address":          item.descriptor.ToolAddress,
			"legacy_tool_id":        item.descriptor.LegacyToolID,
			"legacy_integration_id": item.descriptor.LegacyIntegrationID,
			"integration_slug":      item.descriptor.IntegrationSlug,
			"integration_name":      item.descriptor.IntegrationName,
			"source_slug":           item.descriptor.SourceSlug,
			"source_kind":           item.descriptor.SourceKind,
			"connection_id":         item.descriptor.ConnectionID,
			"connection_slug":       item.descriptor.ConnectionSlug,
			"connection_scope":      item.descriptor.ConnectionScope,
			"snapshot_id":           item.descriptor.SnapshotID,
			"tools_synced_at":       item.descriptor.ToolsSyncedAt,
			"name":                  item.descriptor.Name,
			"description":           item.descriptor.Description,
			"access":                item.descriptor.Access,
			"policy":                item.descriptor.Policy,
			"policy_state":          item.descriptor.PolicyState,
			"source":                item.descriptor.Source,
			"score":                 item.score,
			"match_method":          method,
		})
	}
	return items, nil
}

func workspaceIntegrationDescriptorMatchesIntegrationFilter(descriptor workspaceIntegrationToolDescriptor, filter string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return true
	}
	for _, candidate := range []string{
		descriptor.IntegrationSlug,
		descriptor.SourceSlug,
		descriptor.SourceKind,
		strings.ReplaceAll(descriptor.SourceKind, "_", "-"),
		descriptor.ConnectionSlug,
		descriptor.ConnectionID,
		descriptor.ToolID,
		descriptor.ToolAddress,
		descriptor.LegacyToolID,
	} {
		if strings.EqualFold(candidate, filter) {
			return true
		}
	}
	return false
}

func describeWorkspaceIntegrationFacadeTool(integrations []store.WorkspaceIntegration, toolID string) (workspaceIntegrationToolDescriptor, bool) {
	return describeWorkspaceIntegrationFacadeToolFromDescriptors(buildWorkspaceIntegrationToolDescriptors(integrations, false), toolID)
}

func describeWorkspaceIntegrationFacadeToolFromDescriptors(descriptors []workspaceIntegrationToolDescriptor, toolID string) (workspaceIntegrationToolDescriptor, bool) {
	descriptor, resolution := resolveWorkspaceIntegrationFacadeToolDescriptor(descriptors, toolID)
	return descriptor, resolution.Found && !resolution.Ambiguous
}

type workspaceIntegrationToolDescriptorResolution struct {
	Found      bool
	Ambiguous  bool
	Candidates []workspaceIntegrationToolDescriptor
}

func resolveWorkspaceIntegrationFacadeToolDescriptor(descriptors []workspaceIntegrationToolDescriptor, requestedTool string) (workspaceIntegrationToolDescriptor, workspaceIntegrationToolDescriptorResolution) {
	requestedTool = strings.TrimSpace(requestedTool)
	if requestedTool == "" {
		return workspaceIntegrationToolDescriptor{}, workspaceIntegrationToolDescriptorResolution{}
	}
	var matches []workspaceIntegrationToolDescriptor
	for _, descriptor := range descriptors {
		if workspaceIntegrationDescriptorMatchesRequestedTool(descriptor, requestedTool) {
			matches = appendWorkspaceIntegrationUniqueDescriptor(matches, descriptor)
		}
	}
	switch len(matches) {
	case 0:
		return workspaceIntegrationToolDescriptor{}, workspaceIntegrationToolDescriptorResolution{}
	case 1:
		return matches[0], workspaceIntegrationToolDescriptorResolution{Found: true, Candidates: matches}
	default:
		return workspaceIntegrationToolDescriptor{}, workspaceIntegrationToolDescriptorResolution{Found: true, Ambiguous: true, Candidates: matches}
	}
}

func workspaceIntegrationDescriptorMatchesRequestedTool(descriptor workspaceIntegrationToolDescriptor, requestedTool string) bool {
	if _, _, _, _, structured := workspaceIntegrationParseStructuredToolAddress(requestedTool); structured {
		if candidate := strings.TrimSpace(descriptor.ToolAddress); candidate != "" && strings.EqualFold(candidate, requestedTool) {
			return true
		}
		requestedWorkspace, requestedSource, requestedConnection, requestedName, _ := workspaceIntegrationParseStructuredToolAddress(requestedTool)
		if requestedWorkspace != "" && !strings.EqualFold(workspaceIntegrationAddressSegment(descriptorConnectionWorkspaceFallback(descriptor), "workspace"), requestedWorkspace) {
			return false
		}
		return strings.EqualFold(descriptor.SourceSlug, requestedSource) &&
			strings.EqualFold(descriptor.ConnectionSlug, requestedConnection) &&
			strings.EqualFold(workspaceIntegrationAddressSegment(descriptor.Name, "tool"), requestedName)
	}
	for _, candidate := range []string{descriptor.ToolID, descriptor.LegacyToolID} {
		if candidate != "" && strings.EqualFold(candidate, requestedTool) {
			return true
		}
	}
	return false
}

func descriptorConnectionWorkspaceFallback(descriptor workspaceIntegrationToolDescriptor) string {
	if workspaceID, _, _, _, ok := workspaceIntegrationParseStructuredToolAddress(descriptor.ToolAddress); ok && workspaceID != "" {
		return workspaceID
	}
	return "workspace"
}

func appendWorkspaceIntegrationUniqueDescriptor(matches []workspaceIntegrationToolDescriptor, descriptor workspaceIntegrationToolDescriptor) []workspaceIntegrationToolDescriptor {
	key := firstNonEmpty(descriptor.ToolAddress, descriptor.ToolID, descriptor.LegacyToolID, descriptor.ConnectionID+":"+descriptor.Name)
	for _, existing := range matches {
		existingKey := firstNonEmpty(existing.ToolAddress, existing.ToolID, existing.LegacyToolID, existing.ConnectionID+":"+existing.Name)
		if strings.EqualFold(existingKey, key) {
			return matches
		}
	}
	return append(matches, descriptor)
}

func workspaceIntegrationAmbiguousToolError(requestedTool string, candidates []workspaceIntegrationToolDescriptor) error {
	parts := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		label := firstNonEmpty(candidate.IntegrationName, candidate.ConnectionSlug, candidate.IntegrationSlug, candidate.ToolID)
		address := firstNonEmpty(candidate.ToolAddress, candidate.ToolID)
		parts = append(parts, fmt.Sprintf("%s (%s)", label, address))
	}
	sort.Strings(parts)
	return fmt.Errorf("tool_id %q is ambiguous; use tool_address. candidates: %s", requestedTool, strings.Join(parts, "; "))
}

func buildWorkspaceIntegrationToolDescriptorsFromConnectorProjections(projections []store.WorkspaceIntegrationConnectorProjection, includeBlocked bool) []workspaceIntegrationToolDescriptor {
	var out []workspaceIntegrationToolDescriptor
	for _, projection := range projections {
		connection := projection.Connection
		if !connection.Enabled {
			continue
		}
		source := projection.Source
		policies := make(map[string]string, len(projection.Policies))
		for _, policy := range projection.Policies {
			if strings.TrimSpace(policy.ToolName) == "" {
				continue
			}
			if normalized, ok := workspaceIntegrationNormalizePolicyState(policy.Policy); ok {
				policies[policy.ToolName] = normalized
			}
		}
		for _, tool := range projection.Tools {
			if strings.TrimSpace(tool.ToolName) == "" {
				continue
			}
			policyState := policies[tool.ToolName]
			if policyState == "" {
				policyState = workspaceIntegrationPolicyAllow
			}
			if policyState == workspaceIntegrationPolicyBlock && !includeBlocked {
				continue
			}
			sourceSlug := workspaceIntegrationProjectionDescriptorSourceSlug(source, tool)
			toolAddress := strings.TrimSpace(tool.ToolAddress)
			if sourceSlug != source.Slug {
				toolAddress = workspaceIntegrationStructuredToolAddressFromParts(connection.WorkspaceID, sourceSlug, connection.Slug, tool.ToolName)
			}
			legacyToolID := stringPtrValue(tool.LegacyToolID)
			toolID := legacyToolID
			if toolID == "" {
				toolID = toolAddress
			}
			if toolID == "" {
				toolID = workspaceIntegrationStructuredToolAddressFromParts(connection.WorkspaceID, sourceSlug, connection.Slug, tool.ToolName)
				toolAddress = toolID
			}
			syncedAt := tool.ToolsSyncedAt.UTC()
			if tool.ToolsSyncedAt.IsZero() {
				syncedAt = tool.UpdatedAt.UTC()
			}
			var syncedAtPtr *time.Time
			if !syncedAt.IsZero() {
				syncedAtPtr = &syncedAt
			}
			access := strings.ToLower(strings.TrimSpace(tool.Access))
			if access != "write" {
				access = "read"
			}
			legacyIntegrationID := stringPtrValue(connection.LegacyIntegrationID)
			sourceKind := strings.ToLower(strings.TrimSpace(source.Kind))
			if sourceKind == "" {
				sourceKind = strings.ToLower(strings.TrimSpace(tool.Source))
			}
			if sourceKind == "" {
				sourceKind = strings.ToLower(strings.TrimSpace(source.Importer))
			}
			if sourceKind == "" {
				sourceKind = "connector"
			}
			descriptorSource := firstNonEmpty(strings.ToLower(strings.TrimSpace(tool.Source)), strings.ToLower(strings.TrimSpace(source.Importer)), sourceKind)
			if workspaceIntegrationProjectionSourceIsGoogleWorkspace(source) {
				if serviceSource, ok := workspaceIntegrationGoogleServiceSourceSlugForToolName(tool.ToolName); ok {
					descriptorSource = serviceSource
				}
			}
			out = append(out, workspaceIntegrationToolDescriptor{
				ToolID:              toolID,
				ToolAddress:         toolAddress,
				LegacyToolID:        legacyToolID,
				LegacyIntegrationID: legacyIntegrationID,
				IntegrationSlug:     connection.Slug,
				IntegrationName:     firstNonEmpty(connection.DisplayName, source.DisplayName, connection.Slug),
				SourceSlug:          sourceSlug,
				SourceKind:          sourceKind,
				ConnectionID:        connection.ID,
				ConnectionSlug:      connection.Slug,
				ConnectionScope:     firstNonEmpty(connection.Scope, workspaceIntegrationConnectionScope),
				SnapshotID:          tool.ID,
				ToolsSyncedAt:       syncedAtPtr,
				Name:                tool.ToolName,
				Description:         tool.Description,
				InputSchema:         jsonRawMessageOrFallback(tool.InputSchema, json.RawMessage(`{"type":"object","additionalProperties":true}`)),
				Access:              access,
				Policy:              workspaceIntegrationPolicyLabel(policyState),
				PolicyState:         policyState,
				AuthState:           workspaceIntegrationConnectionAuthState(connection),
				Source:              descriptorSource,
			})
		}
	}
	return out
}

func workspaceIntegrationProjectionDescriptorSourceSlug(source store.WorkspaceIntegrationSource, tool store.WorkspaceIntegrationToolSnapshot) string {
	if workspaceIntegrationProjectionSourceIsGoogleWorkspace(source) {
		if serviceSource, ok := workspaceIntegrationGoogleServiceSourceSlugForToolName(tool.ToolName); ok {
			return serviceSource
		}
	}
	return source.Slug
}

func workspaceIntegrationProjectionSourceIsGoogleWorkspace(source store.WorkspaceIntegrationSource) bool {
	return strings.EqualFold(strings.TrimSpace(source.Kind), "google_workspace") ||
		strings.EqualFold(strings.TrimSpace(source.Slug), "google-workspace")
}

func buildWorkspaceIntegrationToolDescriptors(integrations []store.WorkspaceIntegration, includeBlocked bool) []workspaceIntegrationToolDescriptor {
	var out []workspaceIntegrationToolDescriptor
	for _, integration := range integrations {
		manifestTools, err := parseWorkspaceIntegrationManifest(integration.ToolManifest)
		if err != nil {
			slog.Warn("workspace_integrations.manifest_parse_failed", "integration", integration.Slug, "error", err)
			continue
		}
		for _, tool := range manifestTools {
			if tool.Name == "" {
				continue
			}
			policyState := workspaceIntegrationToolPolicyState(integration, tool.Name)
			if policyState == workspaceIntegrationPolicyBlock && !includeBlocked {
				continue
			}
			toolID := integration.Slug + "." + tool.Name
			out = append(out, workspaceIntegrationToolDescriptor{
				ToolID:              toolID,
				ToolAddress:         workspaceIntegrationStructuredToolAddress(integration, tool),
				LegacyToolID:        toolID,
				LegacyIntegrationID: integration.ID,
				IntegrationSlug:     integration.Slug,
				IntegrationName:     workspaceIntegrationProjectionConnectionDisplayName(integration, tool),
				SourceSlug:          workspaceIntegrationProjectionToolSourceSlug(integration, tool),
				SourceKind:          workspaceIntegrationProjectionSourceKind(integration),
				ConnectionID:        workspaceIntegrationProjectionConnectionID(integration),
				ConnectionSlug:      workspaceIntegrationProjectionConnectionSlug(integration),
				ConnectionScope:     workspaceIntegrationConnectionScope,
				SnapshotID:          workspaceIntegrationProjectionSnapshotID(integration, tool),
				ToolsSyncedAt:       workspaceIntegrationProjectionToolsSyncedAt(integration),
				Name:                tool.Name,
				Description:         tool.Description,
				InputSchema:         workspaceIntegrationToolParameters(tool),
				Access:              workspaceIntegrationManifestToolAccess(tool),
				Policy:              workspaceIntegrationPolicyLabel(policyState),
				PolicyState:         policyState,
				AuthState:           workspaceIntegrationAuthState(integration),
				Source:              workspaceIntegrationManifestToolSource(integration, tool),
			})
		}
	}
	return out
}

func (s *Server) resolveWorkspaceIntegrationRuntimeTool(ctx context.Context, machineID string, integrations []store.WorkspaceIntegration, requestedTool string) (*store.WorkspaceIntegration, *workspaceIntegrationManifestTool, workspaceIntegrationToolDescriptor, bool) {
	descriptors, projectionAuthoritative := s.buildWorkspaceIntegrationRuntimeToolDescriptorsWithSource(ctx, machineID, integrations, false)
	return s.resolveWorkspaceIntegrationRuntimeToolFromDescriptors(ctx, descriptors, projectionAuthoritative, integrations, requestedTool)
}

// resolveWorkspaceIntegrationRuntimeToolFromDescriptors resolves a requested
// tool against already-built descriptors. A caller that also needs the
// descriptors (e.g. the call_tool ambiguity check) can build them once and pass
// them in, instead of paying the projection + guidance-overlay queries twice
// per tool call.
func (s *Server) resolveWorkspaceIntegrationRuntimeToolFromDescriptors(ctx context.Context, descriptors []workspaceIntegrationToolDescriptor, projectionAuthoritative bool, integrations []store.WorkspaceIntegration, requestedTool string) (*store.WorkspaceIntegration, *workspaceIntegrationManifestTool, workspaceIntegrationToolDescriptor, bool) {
	descriptor, hasDescriptor := describeWorkspaceIntegrationFacadeToolFromDescriptors(descriptors, requestedTool)
	if !hasDescriptor && projectionAuthoritative {
		return nil, nil, descriptor, false
	}
	if hasDescriptor && descriptor.LegacyIntegrationID != "" {
		if integration, tool, found := findWorkspaceIntegrationToolByIntegrationID(integrations, descriptor.LegacyIntegrationID, descriptor.Name); found {
			return integration, tool, descriptor, true
		}
		if integration, tool, found := s.resolveWorkspaceIntegrationNormalizedRuntimeTool(ctx, descriptor); found {
			return integration, tool, descriptor, true
		}
		if projectionAuthoritative {
			return nil, nil, descriptor, false
		}
	}
	if hasDescriptor && descriptor.LegacyIntegrationID == "" {
		if integration, tool, found := s.resolveWorkspaceIntegrationNormalizedRuntimeTool(ctx, descriptor); found {
			return integration, tool, descriptor, true
		}
		if projectionAuthoritative {
			return nil, nil, descriptor, false
		}
	}
	executionTool := strings.TrimSpace(requestedTool)
	if hasDescriptor && descriptor.LegacyToolID != "" {
		executionTool = descriptor.LegacyToolID
	}
	integration, tool, found := findWorkspaceIntegrationTool(integrations, executionTool)
	if !found {
		return nil, nil, descriptor, false
	}
	if !hasDescriptor {
		if v1Descriptor, ok := describeWorkspaceIntegrationFacadeToolFromDescriptors(buildWorkspaceIntegrationToolDescriptors(integrations, false), executionTool); ok {
			descriptor = v1Descriptor
		}
	}
	return integration, tool, descriptor, true
}

func (s *Server) resolveWorkspaceIntegrationNormalizedRuntimeTool(ctx context.Context, descriptor workspaceIntegrationToolDescriptor) (*store.WorkspaceIntegration, *workspaceIntegrationManifestTool, bool) {
	reader, ok := s.store.(workspaceIntegrationConnectorProjectionReader)
	if !ok {
		return nil, nil, false
	}
	workspaceID := descriptorConnectionWorkspaceFallback(descriptor)
	if workspaceID == "" || workspaceID == "workspace" {
		if workspace := workspaceIntegrationWorkspaceFromContext(ctx); workspace != nil {
			workspaceID = workspace.ID
		}
	}
	if workspaceID == "" || workspaceID == "workspace" {
		return nil, nil, false
	}
	projections, err := reader.ListWorkspaceIntegrationConnectorProjections(ctx, workspaceID)
	if err != nil {
		slog.Warn("workspace_integrations.normalized_runtime_projection_list_failed", "workspace_id", workspaceID, "error", err)
		return nil, nil, false
	}
	for _, projection := range projections {
		if strings.TrimSpace(projection.Connection.ID) != strings.TrimSpace(descriptor.ConnectionID) {
			continue
		}
		for _, snapshot := range projection.Tools {
			if !workspaceIntegrationSnapshotMatchesDescriptor(snapshot, descriptor) {
				continue
			}
			transport := workspaceIntegrationNormalizedRuntimeTransport(projection.Source, projection.Connection, snapshot, descriptor)
			tool, ok := workspaceIntegrationNormalizedRuntimeManifestTool(snapshot, transport)
			if !ok {
				return nil, nil, false
			}
			integration := workspaceIntegrationNormalizedRuntimeIntegration(projection.Source, projection.Connection, descriptor, transport)
			return &integration, &tool, true
		}
	}
	return nil, nil, false
}

func workspaceIntegrationSnapshotMatchesDescriptor(snapshot store.WorkspaceIntegrationToolSnapshot, descriptor workspaceIntegrationToolDescriptor) bool {
	if descriptor.SnapshotID != "" && strings.EqualFold(snapshot.ID, descriptor.SnapshotID) {
		return true
	}
	if descriptor.ToolAddress != "" && strings.EqualFold(snapshot.ToolAddress, descriptor.ToolAddress) {
		return true
	}
	return strings.EqualFold(snapshot.ToolName, descriptor.Name)
}

func workspaceIntegrationNormalizedRuntimeIntegration(source store.WorkspaceIntegrationSource, connection store.WorkspaceIntegrationConnection, descriptor workspaceIntegrationToolDescriptor, transport string) store.WorkspaceIntegration {
	config := connection.Config
	if len(config) == 0 {
		config = source.Config
	}
	legacyIntegrationID := ""
	if connection.LegacyIntegrationID != nil {
		legacyIntegrationID = strings.TrimSpace(*connection.LegacyIntegrationID)
	}
	endpoint := firstNonEmpty(
		workspaceIntegrationRawConfigString(connection.Config, "endpoint"),
		workspaceIntegrationRawConfigString(connection.Config, "base_url"),
		workspaceIntegrationRawConfigString(source.Config, "endpoint"),
		workspaceIntegrationRawConfigString(source.Config, "base_url"),
	)
	var endpointPtr *string
	if endpoint != "" {
		endpointPtr = &endpoint
	}
	return store.WorkspaceIntegration{
		ID:                legacyIntegrationID,
		WorkspaceID:       connection.WorkspaceID,
		Slug:              connection.Slug,
		DisplayName:       firstNonEmpty(connection.DisplayName, source.DisplayName, connection.Slug),
		Kind:              firstNonEmpty(descriptor.SourceSlug, source.Slug, source.Kind, source.Importer),
		Transport:         transport,
		Endpoint:          endpointPtr,
		Enabled:           connection.Enabled,
		Config:            config,
		CredentialRefKind: workspaceIntegrationNormalizedRuntimeCredentialRefKind(connection),
		CredentialRefID:   workspaceIntegrationNormalizedRuntimeCredentialRefID(connection),
	}
}

func workspaceIntegrationNormalizedRuntimeCredentialRefKind(connection store.WorkspaceIntegrationConnection) string {
	if connection.LegacyIntegrationID != nil && strings.TrimSpace(*connection.LegacyIntegrationID) != "" {
		return "legacy_integration"
	}
	if strings.TrimSpace(connection.ID) != "" {
		return "connection"
	}
	return ""
}

func workspaceIntegrationNormalizedRuntimeCredentialRefID(connection store.WorkspaceIntegrationConnection) string {
	if connection.LegacyIntegrationID != nil && strings.TrimSpace(*connection.LegacyIntegrationID) != "" {
		return strings.TrimSpace(*connection.LegacyIntegrationID)
	}
	return strings.TrimSpace(connection.ID)
}

func workspaceIntegrationNormalizedRuntimeManifestTool(snapshot store.WorkspaceIntegrationToolSnapshot, transport string) (workspaceIntegrationManifestTool, bool) {
	schema := jsonRawMessageOrFallback(snapshot.InputSchema, json.RawMessage(`{"type":"object","additionalProperties":true}`))
	tool := workspaceIntegrationManifestTool{
		Name:        snapshot.ToolName,
		Description: snapshot.Description,
		Parameters:  schema,
		InputSchema: schema,
		Access:      snapshot.Access,
	}
	switch normalizeWorkspaceIntegrationTransport(transport) {
	case "mock":
		return tool, true
	case "http":
		request, ok := workspaceIntegrationNormalizedRuntimeHTTPRequest(snapshot)
		if !ok {
			return workspaceIntegrationManifestTool{}, false
		}
		tool.Request = request
		return tool, true
	default:
		return workspaceIntegrationManifestTool{}, false
	}
}

func workspaceIntegrationNormalizedRuntimeHTTPRequest(snapshot store.WorkspaceIntegrationToolSnapshot) (*workspaceIntegrationHTTPRequest, bool) {
	if len(snapshot.Provenance) == 0 {
		return nil, false
	}
	var provenance struct {
		Request json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(snapshot.Provenance, &provenance); err != nil || len(provenance.Request) == 0 || string(provenance.Request) == "null" {
		return nil, false
	}
	var request workspaceIntegrationHTTPRequest
	if err := json.Unmarshal(provenance.Request, &request); err != nil {
		return nil, false
	}
	if strings.TrimSpace(request.Method) == "" && strings.TrimSpace(request.URL) == "" && strings.TrimSpace(request.Path) == "" {
		return nil, false
	}
	return &request, true
}

func workspaceIntegrationNormalizedRuntimeTransport(source store.WorkspaceIntegrationSource, connection store.WorkspaceIntegrationConnection, snapshot store.WorkspaceIntegrationToolSnapshot, descriptor workspaceIntegrationToolDescriptor) string {
	return firstNonEmpty(
		workspaceIntegrationRawConfigString(connection.Config, "transport"),
		workspaceIntegrationRawConfigString(source.Config, "transport"),
		descriptor.Source,
		snapshot.Source,
		source.Importer,
		source.Kind,
	)
}

func workspaceIntegrationRawConfigString(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var config map[string]interface{}
	if err := json.Unmarshal(raw, &config); err != nil {
		return ""
	}
	return strings.TrimSpace(workspaceIntegrationArgString(config[key]))
}

func findWorkspaceIntegrationToolByIntegrationID(integrations []store.WorkspaceIntegration, integrationID, toolName string) (*store.WorkspaceIntegration, *workspaceIntegrationManifestTool, bool) {
	integrationID = strings.TrimSpace(integrationID)
	toolName = strings.TrimSpace(toolName)
	if integrationID == "" || toolName == "" {
		return nil, nil, false
	}
	for i := range integrations {
		integration := &integrations[i]
		if strings.TrimSpace(integration.ID) != integrationID {
			continue
		}
		manifestTools, err := parseWorkspaceIntegrationManifest(integration.ToolManifest)
		if err != nil {
			return nil, nil, false
		}
		for j := range manifestTools {
			tool := &manifestTools[j]
			if tool.Name != toolName || workspaceIntegrationToolPolicyState(*integration, tool.Name) == workspaceIntegrationPolicyBlock {
				continue
			}
			return integration, tool, true
		}
		return nil, nil, false
	}
	return nil, nil, false
}

func workspaceIntegrationConnectionAuthState(connection store.WorkspaceIntegrationConnection) string {
	if !connection.Enabled {
		return "disabled"
	}
	state := strings.ToLower(strings.TrimSpace(connection.CredentialState))
	if state == "" {
		return "connected"
	}
	return state
}

func jsonRawMessageOrFallback(raw json.RawMessage, fallback json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return fallback
	}
	return raw
}

func workspaceIntegrationSearchLimit(raw interface{}) int {
	limit := workspaceIntegrationDefaultSearchLimit
	switch typed := raw.(type) {
	case int:
		limit = typed
	case int64:
		limit = int(typed)
	case float64:
		limit = int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			limit = int(parsed)
		}
	}
	if limit <= 0 {
		return workspaceIntegrationDefaultSearchLimit
	}
	if limit > workspaceIntegrationMaxSearchLimit {
		return workspaceIntegrationMaxSearchLimit
	}
	return limit
}

func workspaceIntegrationSearchMethod(raw interface{}) (string, error) {
	method := strings.ToLower(strings.TrimSpace(workspaceIntegrationArgString(raw)))
	if method == "" {
		return workspaceIntegrationSearchMethodSemantic, nil
	}
	switch method {
	case workspaceIntegrationSearchMethodSemantic, workspaceIntegrationSearchMethodKeyword, workspaceIntegrationSearchMethodHybrid:
		return method, nil
	default:
		return "", fmt.Errorf("method must be semantic, keyword, or hybrid")
	}
}

func workspaceIntegrationSearchScore(descriptor workspaceIntegrationToolDescriptor, query string, method string) float64 {
	query = strings.TrimSpace(query)
	if query == "" {
		return 1
	}
	keywordScore := workspaceIntegrationKeywordSearchScore(descriptor, query)
	switch method {
	case workspaceIntegrationSearchMethodKeyword:
		return keywordScore
	case workspaceIntegrationSearchMethodHybrid:
		semanticScore := workspaceIntegrationSemanticSearchScore(descriptor, query)
		if semanticScore == 0 {
			return keywordScore
		}
		return semanticScore + keywordScore
	default:
		semanticScore := workspaceIntegrationSemanticSearchScore(descriptor, query)
		if semanticScore > keywordScore {
			return semanticScore
		}
		return keywordScore
	}
}

func workspaceIntegrationKeywordSearchScore(descriptor workspaceIntegrationToolDescriptor, query string) float64 {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return 1
	}
	haystack := strings.ToLower(strings.Join([]string{
		descriptor.ToolID,
		descriptor.ToolAddress,
		descriptor.LegacyToolID,
		descriptor.IntegrationSlug,
		descriptor.IntegrationName,
		descriptor.SourceSlug,
		descriptor.SourceKind,
		descriptor.ConnectionSlug,
		descriptor.Name,
		descriptor.Description,
		descriptor.Access,
		descriptor.PolicyState,
		descriptor.Source,
	}, " "))
	score := 0.0
	if strings.EqualFold(query, descriptor.ToolID) || strings.EqualFold(query, descriptor.ToolAddress) || strings.EqualFold(query, descriptor.LegacyToolID) || strings.EqualFold(query, descriptor.Name) {
		score += 2
	}
	if strings.Contains(haystack, query) {
		score += 1
	}
	tokens := strings.Fields(query)
	if len(tokens) == 0 {
		return score
	}
	matches := 0
	for _, token := range tokens {
		if strings.Contains(haystack, token) {
			matches++
		}
	}
	score += float64(matches) / float64(len(tokens))
	return score
}

func workspaceIntegrationSemanticSearchScore(descriptor workspaceIntegrationToolDescriptor, query string) float64 {
	queryTokens := workspaceIntegrationSearchTokens(query)
	if len(queryTokens) == 0 {
		return 0
	}
	weights := workspaceIntegrationDescriptorSearchWeights(descriptor)
	directWeights := workspaceIntegrationDescriptorDirectSearchWeights(descriptor)
	score := 0.0
	matches := 0
	for _, token := range queryTokens {
		best := 0.0
		for _, candidate := range workspaceIntegrationSemanticTokenSet(token) {
			if weight := weights[candidate]; weight > best {
				best = weight
			}
		}
		if directWeight := directWeights[token]; directWeight > 0 {
			best += directWeight * 0.25
		}
		if best > 0 {
			matches++
			score += best
		}
	}
	if matches == 0 {
		return 0
	}
	coverage := float64(matches) / float64(len(queryTokens))
	return score/float64(len(queryTokens)) + coverage
}

func workspaceIntegrationDescriptorSearchWeights(descriptor workspaceIntegrationToolDescriptor) map[string]float64 {
	weights := map[string]float64{}
	workspaceIntegrationAddWeightedSearchTokens(weights, descriptor.ToolID, 3)
	workspaceIntegrationAddWeightedSearchTokens(weights, descriptor.ToolAddress, 3)
	workspaceIntegrationAddWeightedSearchTokens(weights, descriptor.LegacyToolID, 3)
	workspaceIntegrationAddWeightedSearchTokens(weights, descriptor.Name, 3)
	workspaceIntegrationAddWeightedSearchTokens(weights, descriptor.IntegrationSlug, 2)
	workspaceIntegrationAddWeightedSearchTokens(weights, descriptor.IntegrationName, 2)
	workspaceIntegrationAddWeightedSearchTokens(weights, descriptor.SourceSlug, 2)
	workspaceIntegrationAddWeightedSearchTokens(weights, descriptor.SourceKind, 2)
	workspaceIntegrationAddWeightedSearchTokens(weights, descriptor.ConnectionSlug, 2)
	workspaceIntegrationAddWeightedSearchTokens(weights, descriptor.Description, 1)
	workspaceIntegrationAddWeightedSearchTokens(weights, descriptor.Access, 0.75)
	workspaceIntegrationAddWeightedSearchTokens(weights, descriptor.PolicyState, 0.5)
	workspaceIntegrationAddWeightedSearchTokens(weights, descriptor.Source, 0.75)
	return weights
}

func workspaceIntegrationDescriptorDirectSearchWeights(descriptor workspaceIntegrationToolDescriptor) map[string]float64 {
	weights := map[string]float64{}
	workspaceIntegrationAddDirectSearchTokens(weights, descriptor.ToolID, 3)
	workspaceIntegrationAddDirectSearchTokens(weights, descriptor.ToolAddress, 3)
	workspaceIntegrationAddDirectSearchTokens(weights, descriptor.LegacyToolID, 3)
	workspaceIntegrationAddDirectSearchTokens(weights, descriptor.Name, 3)
	workspaceIntegrationAddDirectSearchTokens(weights, descriptor.IntegrationSlug, 2)
	workspaceIntegrationAddDirectSearchTokens(weights, descriptor.IntegrationName, 2)
	workspaceIntegrationAddDirectSearchTokens(weights, descriptor.SourceSlug, 2)
	workspaceIntegrationAddDirectSearchTokens(weights, descriptor.SourceKind, 2)
	workspaceIntegrationAddDirectSearchTokens(weights, descriptor.ConnectionSlug, 2)
	workspaceIntegrationAddDirectSearchTokens(weights, descriptor.Description, 1)
	workspaceIntegrationAddDirectSearchTokens(weights, descriptor.Access, 0.75)
	workspaceIntegrationAddDirectSearchTokens(weights, descriptor.PolicyState, 0.5)
	workspaceIntegrationAddDirectSearchTokens(weights, descriptor.Source, 0.75)
	return weights
}

func workspaceIntegrationAddDirectSearchTokens(weights map[string]float64, text string, weight float64) {
	for _, token := range workspaceIntegrationSearchTokens(text) {
		if weight > weights[token] {
			weights[token] = weight
		}
	}
}

func workspaceIntegrationAddWeightedSearchTokens(weights map[string]float64, text string, weight float64) {
	for _, token := range workspaceIntegrationSearchTokens(text) {
		if weight > weights[token] {
			weights[token] = weight
		}
		for _, semanticToken := range workspaceIntegrationSearchAliases[token] {
			semanticToken = workspaceIntegrationNormalizeSearchToken(semanticToken)
			semanticWeight := weight * 0.65
			if semanticWeight > weights[semanticToken] {
				weights[semanticToken] = semanticWeight
			}
		}
	}
}

func workspaceIntegrationSearchTokens(text string) []string {
	var tokens []string
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		token := workspaceIntegrationNormalizeSearchToken(current.String())
		current.Reset()
		if token != "" {
			tokens = append(tokens, token)
		}
	}
	for _, r := range text {
		if unicode.IsUpper(r) && current.Len() > 0 {
			flush()
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current.WriteRune(unicode.ToLower(r))
			continue
		}
		flush()
	}
	flush()
	return tokens
}

func workspaceIntegrationNormalizeSearchToken(token string) string {
	token = strings.ToLower(strings.TrimSpace(token))
	if token == "" {
		return ""
	}
	if len(token) > 4 && strings.HasSuffix(token, "ies") {
		return strings.TrimSuffix(token, "ies") + "y"
	}
	for _, suffix := range []string{"ches", "shes", "sses", "xes", "zes", "oes"} {
		if len(token) > 4 && strings.HasSuffix(token, suffix) {
			return strings.TrimSuffix(token, "es")
		}
	}
	if len(token) > 5 && strings.HasSuffix(token, "ses") {
		return strings.TrimSuffix(token, "es")
	}
	if len(token) > 3 && strings.HasSuffix(token, "s") && !strings.HasSuffix(token, "ss") {
		return strings.TrimSuffix(token, "s")
	}
	return token
}

func workspaceIntegrationSemanticTokenSet(token string) []string {
	token = workspaceIntegrationNormalizeSearchToken(token)
	if token == "" {
		return nil
	}
	seen := map[string]bool{token: true}
	out := []string{token}
	for _, alias := range workspaceIntegrationSearchAliases[token] {
		alias = workspaceIntegrationNormalizeSearchToken(alias)
		if alias == "" || seen[alias] {
			continue
		}
		seen[alias] = true
		out = append(out, alias)
	}
	return out
}

var workspaceIntegrationSearchAliases = map[string][]string{
	"add":         {"create", "new", "write", "post", "send", "upload"},
	"appointment": {"calendar", "event", "meeting", "schedule"},
	"archive":     {"delete", "remove"},
	"bug":         {"issue", "ticket", "task"},
	"calendar":    {"event", "meeting", "schedule", "appointment"},
	"channel":     {"slack", "message"},
	"commit":      {"github", "repository", "repo", "code"},
	"create":      {"add", "new", "write", "post", "send", "upload"},
	"delete":      {"remove", "archive"},
	"doc":         {"document", "drive", "file"},
	"document":    {"doc", "drive", "file"},
	"drive":       {"file", "folder", "document", "doc"},
	"edit":        {"update", "modify", "change"},
	"email":       {"gmail", "mail", "message", "inbox"},
	"event":       {"calendar", "meeting", "schedule", "appointment"},
	"fetch":       {"get", "list", "read", "search", "show"},
	"file":        {"drive", "folder", "document", "doc"},
	"find":        {"search", "list", "read", "get", "show"},
	"folder":      {"drive", "file", "document"},
	"get":         {"fetch", "list", "read", "search", "show"},
	"github":      {"git", "repository", "repo", "pull", "request", "code"},
	"git":         {"github", "repository", "repo", "code"},
	"gmail":       {"email", "mail", "message", "inbox"},
	"graph":       {"graphql", "schema", "query"},
	"graphql":     {"graph", "schema", "query"},
	"inbox":       {"gmail", "email", "mail", "message"},
	"issue":       {"ticket", "task", "bug", "jira", "linear"},
	"jira":        {"issue", "ticket", "task", "project"},
	"linear":      {"issue", "ticket", "task", "project"},
	"list":        {"find", "get", "read", "search", "show"},
	"mail":        {"gmail", "email", "message", "inbox"},
	"meeting":     {"calendar", "event", "schedule", "appointment"},
	"message":     {"gmail", "email", "mail", "inbox", "slack"},
	"modify":      {"update", "edit", "change"},
	"new":         {"add", "create", "write", "post"},
	"notion":      {"page", "database", "wiki"},
	"openapi":     {"api", "schema", "rest"},
	"page":        {"notion", "wiki", "database"},
	"post":        {"create", "send", "write"},
	"pull":        {"github", "request", "pr", "repository", "repo"},
	"pr":          {"pull", "request", "github", "repository", "repo"},
	"query":       {"search", "graphql", "get", "read"},
	"read":        {"fetch", "get", "list", "search", "show"},
	"remove":      {"delete", "archive"},
	"repo":        {"repository", "github", "git", "code"},
	"repository":  {"repo", "github", "git", "code"},
	"request":     {"pull", "pr", "github"},
	"schedule":    {"calendar", "event", "meeting", "appointment"},
	"search":      {"find", "get", "list", "query", "read", "show"},
	"send":        {"create", "email", "mail", "message", "post", "write"},
	"show":        {"get", "list", "read", "search"},
	"slack":       {"message", "channel"},
	"task":        {"issue", "ticket", "bug", "jira", "linear"},
	"ticket":      {"issue", "task", "bug", "jira", "linear"},
	"tool":        {"integration", "connector"},
	"update":      {"edit", "modify", "change"},
	"upload":      {"add", "create", "file", "write"},
	"wiki":        {"notion", "page", "database"},
	"write":       {"add", "create", "edit", "post", "send", "update", "upload"},
}

func workspaceIntegrationManifestToolAccess(tool workspaceIntegrationManifestTool) string {
	switch strings.ToLower(strings.TrimSpace(tool.Access)) {
	case "read", "write":
		return strings.ToLower(strings.TrimSpace(tool.Access))
	}
	switch strings.ToLower(workspaceIntegrationCatalogToolAccess(tool.Name)) {
	case "write":
		return "write"
	}
	name := strings.ToLower(tool.Name)
	for _, marker := range []string{"create", "update", "delete", "remove", "send", "write", "patch", "post"} {
		if strings.Contains(name, marker) {
			return "write"
		}
	}
	return "read"
}

func workspaceIntegrationManifestToolSource(integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool) string {
	if source := strings.ToLower(strings.TrimSpace(tool.Source)); source != "" {
		return source
	}
	if source, ok := workspaceIntegrationGoogleServiceToolSourceSlug(integration, tool.Name); ok {
		return source
	}
	switch normalizeWorkspaceIntegrationTransport(integration.Transport) {
	case "http":
		switch strings.ToLower(strings.TrimSpace(integration.Kind)) {
		case "openapi", "graphql":
			return strings.ToLower(strings.TrimSpace(integration.Kind))
		}
	}
	return workspaceIntegrationToolSource(integration)
}

func workspaceIntegrationToolSource(integration store.WorkspaceIntegration) string {
	switch normalizeWorkspaceIntegrationTransport(integration.Transport) {
	case "mcp-remote":
		return "mcp"
	case "http":
		return "http"
	case "mock":
		return "mock"
	default:
		source := strings.ToLower(strings.TrimSpace(integration.Kind))
		if source == "" {
			source = strings.ToLower(strings.TrimSpace(integration.Transport))
		}
		return source
	}
}

func workspaceIntegrationAuthState(integration store.WorkspaceIntegration) string {
	if !integration.Enabled {
		return "disabled"
	}
	return "connected"
}

func workspaceIntegrationStructuredToolAddress(integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool) string {
	return workspaceIntegrationStructuredToolAddressFromParts(
		integration.WorkspaceID,
		workspaceIntegrationProjectionToolSourceSlug(integration, tool),
		workspaceIntegrationProjectionConnectionSlug(integration),
		tool.Name,
	)
}

func workspaceIntegrationStructuredToolAddressFromParts(workspaceID, sourceSlug, connectionSlug, toolName string) string {
	return workspaceIntegrationToolAddressPrefix +
		workspaceIntegrationAddressSegment(workspaceID, "workspace") + "." +
		workspaceIntegrationAddressSegment(sourceSlug, "source") + "." +
		workspaceIntegrationAddressSegment(connectionSlug, "connection") + "." +
		workspaceIntegrationAddressSegment(toolName, "tool")
}

func workspaceIntegrationParseStructuredToolAddress(address string) (workspaceID, sourceSlug, connectionSlug, toolName string, ok bool) {
	trimmed := strings.TrimSpace(address)
	if rest, legacy := strings.CutPrefix(trimmed, workspaceIntegrationLegacyToolAddressPrefix); legacy {
		parts := strings.SplitN(rest, ".", 3)
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return "", "", "", "", false
		}
		return "", parts[0], parts[1], parts[2], true
	}
	rest, ok := strings.CutPrefix(trimmed, workspaceIntegrationToolAddressPrefix)
	if !ok {
		return "", "", "", "", false
	}
	parts := strings.SplitN(rest, ".", 4)
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return "", "", "", "", false
	}
	return parts[0], parts[1], parts[2], parts[3], true
}

func workspaceIntegrationProjectionSourceSlug(integration store.WorkspaceIntegration) string {
	source := strings.ReplaceAll(strings.TrimSpace(integration.Kind), "_", "-")
	if source == "" {
		source = integration.Slug
	}
	return workspaceIntegrationAddressSegment(source, integration.Slug)
}

func workspaceIntegrationProjectionToolSourceSlug(integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool) string {
	if source, ok := workspaceIntegrationGoogleServiceToolSourceSlug(integration, tool.Name); ok {
		return source
	}
	if source := strings.ToLower(strings.TrimSpace(tool.Source)); source != "" {
		return workspaceIntegrationAddressSegment(source, workspaceIntegrationProjectionSourceSlug(integration))
	}
	return workspaceIntegrationProjectionSourceSlug(integration)
}

func workspaceIntegrationGoogleServiceToolSourceSlug(integration store.WorkspaceIntegration, toolName string) (string, bool) {
	if !workspaceIntegrationIsGoogleWorkspaceProvider(integration.Kind, integration.Slug) {
		return "", false
	}
	return workspaceIntegrationGoogleServiceSourceSlugForToolName(toolName)
}

func workspaceIntegrationGoogleServiceSourceSlugForToolName(toolName string) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(toolName))
	switch {
	case strings.HasPrefix(name, "gmail_"):
		return "gmail", true
	case strings.HasPrefix(name, "drive_"):
		return "google-drive", true
	case strings.HasPrefix(name, "calendar_"):
		return "google-calendar", true
	case strings.HasPrefix(name, "docs_"):
		return "google-docs", true
	default:
		return "", false
	}
}

func workspaceIntegrationProjectionConnectionDisplayName(integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool) string {
	sourceSlug := workspaceIntegrationProjectionToolSourceSlug(integration, tool)
	if !workspaceIntegrationIsGoogleWorkspaceProvider(integration.Kind, integration.Slug) {
		return integration.DisplayName
	}
	cfg, err := parseGoogleWorkspaceIntegrationConfig(integration)
	if err != nil || strings.TrimSpace(cfg.Email) == "" {
		return integration.DisplayName
	}
	return workspaceIntegrationGoogleSourceDisplayName(sourceSlug) + " - " + strings.TrimSpace(cfg.Email)
}

func workspaceIntegrationGoogleSourceDisplayName(sourceSlug string) string {
	switch sourceSlug {
	case "gmail":
		return "Gmail"
	case "google-drive":
		return "Google Drive"
	case "google-calendar":
		return "Google Calendar"
	case "google-docs":
		return "Google Docs"
	default:
		return "Google Workspace"
	}
}

func workspaceIntegrationProjectionSourceKind(integration store.WorkspaceIntegration) string {
	if kind := strings.ToLower(strings.TrimSpace(integration.Kind)); kind != "" {
		return kind
	}
	return workspaceIntegrationToolSource(integration)
}

func workspaceIntegrationProjectionConnectionID(integration store.WorkspaceIntegration) string {
	if strings.TrimSpace(integration.ID) != "" {
		return integration.ID
	}
	return "v1:" + workspaceIntegrationProjectionConnectionSlug(integration)
}

func workspaceIntegrationProjectionConnectionSlug(integration store.WorkspaceIntegration) string {
	return workspaceIntegrationAddressSegment(integration.Slug, "connection")
}

func workspaceIntegrationProjectionSnapshotID(integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool) string {
	return "v1:" + workspaceIntegrationProjectionConnectionSlug(integration) + ":" + workspaceIntegrationAddressSegment(tool.Name, "tool")
}

func workspaceIntegrationProjectionToolsSyncedAt(integration store.WorkspaceIntegration) *time.Time {
	switch {
	case !integration.UpdatedAt.IsZero():
		syncedAt := integration.UpdatedAt.UTC()
		return &syncedAt
	case !integration.CreatedAt.IsZero():
		syncedAt := integration.CreatedAt.UTC()
		return &syncedAt
	default:
		return nil
	}
}

func workspaceIntegrationAddressSegment(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = strings.ToLower(strings.TrimSpace(fallback))
	}
	var out strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			out.WriteRune(r)
			lastDash = false
		case r == '-':
			if !lastDash {
				out.WriteRune('-')
				lastDash = true
			}
		default:
			if !lastDash {
				out.WriteRune('-')
				lastDash = true
			}
		}
	}
	segment := strings.Trim(out.String(), "-")
	if segment == "" {
		return "item"
	}
	return segment
}

func findWorkspaceIntegrationTool(integrations []store.WorkspaceIntegration, requestedTool string) (*store.WorkspaceIntegration, *workspaceIntegrationManifestTool, bool) {
	requestedWorkspace, requestedSource, requestedConnection, requestedName, structured := workspaceIntegrationParseStructuredToolAddress(requestedTool)
	requestedIntegration, legacyName, hasIntegration := strings.Cut(requestedTool, ".")
	if structured {
		if requestedSource == "" || requestedConnection == "" || requestedName == "" {
			return nil, nil, false
		}
	} else {
		requestedName = legacyName
		if !hasIntegration || requestedIntegration == "" || requestedName == "" {
			return nil, nil, false
		}
	}
	if requestedName == "" {
		return nil, nil, false
	}
	for i := range integrations {
		integration := &integrations[i]
		if structured {
			if requestedWorkspace != "" && workspaceIntegrationAddressSegment(integration.WorkspaceID, "workspace") != requestedWorkspace {
				continue
			}
			if workspaceIntegrationProjectionConnectionSlug(*integration) != requestedConnection {
				continue
			}
		} else {
			if integration.Slug != requestedIntegration {
				continue
			}
		}
		manifestTools, err := parseWorkspaceIntegrationManifest(integration.ToolManifest)
		if err != nil {
			continue
		}
		for j := range manifestTools {
			tool := &manifestTools[j]
			if structured && workspaceIntegrationProjectionToolSourceSlug(*integration, *tool) != requestedSource {
				continue
			}
			nameMatches := tool.Name == requestedName
			if structured {
				nameMatches = workspaceIntegrationAddressSegment(tool.Name, "tool") == requestedName
			}
			if !nameMatches || workspaceIntegrationToolPolicyState(*integration, tool.Name) == workspaceIntegrationPolicyBlock {
				continue
			}
			return integration, tool, true
		}
	}
	return nil, nil, false
}

func parseWorkspaceIntegrationManifest(manifest json.RawMessage) ([]workspaceIntegrationManifestTool, error) {
	if len(manifest) == 0 {
		return nil, nil
	}
	var tools []workspaceIntegrationManifestTool
	if err := json.Unmarshal(manifest, &tools); err != nil {
		return nil, err
	}
	return tools, nil
}

func workspaceIntegrationToolParameters(tool workspaceIntegrationManifestTool) json.RawMessage {
	switch {
	case len(tool.Parameters) > 0:
		return tool.Parameters
	case len(tool.InputSchema) > 0:
		return tool.InputSchema
	case len(tool.Schema) > 0:
		return tool.Schema
	default:
		return json.RawMessage(`{"type":"object","additionalProperties":true}`)
	}
}

func workspaceIntegrationToolAllowed(integration store.WorkspaceIntegration, toolName string) bool {
	return workspaceIntegrationToolPolicyState(integration, toolName) != workspaceIntegrationPolicyBlock
}

func workspaceIntegrationToolPolicyState(integration store.WorkspaceIntegration, toolName string) string {
	fullName := integration.Slug + "." + toolName
	for _, denied := range integration.DeniedTools {
		if denied == toolName || denied == fullName {
			return workspaceIntegrationPolicyBlock
		}
	}
	if policy, ok := workspaceIntegrationConfiguredToolPolicy(integration, toolName); ok {
		return policy
	}
	if policy, ok := workspaceIntegrationConfiguredToolPolicy(integration, fullName); ok {
		return policy
	}
	if len(integration.AllowedTools) == 0 {
		return workspaceIntegrationPolicyAllow
	}
	for _, allowed := range integration.AllowedTools {
		if allowed == toolName || allowed == fullName {
			return workspaceIntegrationPolicyAllow
		}
	}
	return workspaceIntegrationPolicyBlock
}

func workspaceIntegrationConfiguredToolPolicy(integration store.WorkspaceIntegration, toolName string) (string, bool) {
	if len(integration.Config) == 0 {
		return "", false
	}
	var raw struct {
		ToolPolicy   map[string]string `json:"tool_policy"`
		ToolPolicies map[string]string `json:"tool_policies"`
	}
	if err := json.Unmarshal(integration.Config, &raw); err != nil {
		return "", false
	}
	if policy, ok := raw.ToolPolicy[toolName]; ok {
		return workspaceIntegrationNormalizePolicyState(policy)
	}
	if policy, ok := raw.ToolPolicies[toolName]; ok {
		return workspaceIntegrationNormalizePolicyState(policy)
	}
	return "", false
}

func workspaceIntegrationNormalizePolicyState(policy string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "allow", "allowed":
		return workspaceIntegrationPolicyAllow, true
	case "require_approval", "approval_required", "require-approval", "approval-required":
		return workspaceIntegrationPolicyRequireApproval, true
	case "block", "blocked", "deny", "denied":
		return workspaceIntegrationPolicyBlock, true
	default:
		return "", false
	}
}

func workspaceIntegrationPolicyLabel(policyState string) string {
	switch policyState {
	case workspaceIntegrationPolicyAllow:
		return "allowed"
	case workspaceIntegrationPolicyRequireApproval:
		return "approval_required"
	default:
		return "blocked"
	}
}

func workspaceIntegrationApprovalRequiredMessage(toolID string) string {
	return fmt.Sprintf("tool %s requires human approval before execution", toolID)
}

func (s *Server) redactWorkspaceIntegrationToolResult(ctx context.Context, integration store.WorkspaceIntegration, result map[string]interface{}) (map[string]interface{}, error) {
	secrets, err := s.workspaceIntegrationRedactionSecrets(ctx, integration)
	if err != nil {
		return nil, err
	}
	if len(secrets) == 0 {
		return result, nil
	}
	redacted, ok := redactWorkspaceIntegrationValue(result, secrets).(map[string]interface{})
	if !ok {
		return map[string]interface{}{}, nil
	}
	return redacted, nil
}

func (s *Server) workspaceIntegrationRedactionSecrets(ctx context.Context, integration store.WorkspaceIntegration) ([]string, error) {
	secrets := collectWorkspaceIntegrationConfigSecrets(integration.Config)
	credential, err := s.store.GetWorkspaceIntegrationCredential(ctx, integration.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uniqueWorkspaceIntegrationSecrets(secrets), nil
		}
		return nil, fmt.Errorf("load workspace integration credential for redaction: %w", err)
	}
	if credential == nil {
		return uniqueWorkspaceIntegrationSecrets(secrets), nil
	}
	for _, encrypted := range []string{credential.SecretEnc, stringPtrValue(credential.RefreshEnc)} {
		if strings.TrimSpace(encrypted) == "" {
			continue
		}
		if s.secretKey == "" {
			return nil, errors.New("SECRET_ENCRYPTION_KEY not configured")
		}
		secret, err := crypto.Decrypt(encrypted, s.secretKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt workspace integration credential for redaction: %w", err)
		}
		secrets = append(secrets, secret)
	}
	return uniqueWorkspaceIntegrationSecrets(secrets), nil
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func uniqueWorkspaceIntegrationSecrets(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func collectWorkspaceIntegrationConfigSecrets(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	var secrets []string
	var walk func(value interface{}, key string)
	walk = func(value interface{}, key string) {
		switch typed := value.(type) {
		case map[string]interface{}:
			for childKey, childValue := range typed {
				walk(childValue, childKey)
			}
		case []interface{}:
			for _, child := range typed {
				walk(child, key)
			}
		case string:
			if isWorkspaceIntegrationSecretKey(key) {
				secrets = append(secrets, typed)
			}
		}
	}
	walk(decoded, "")
	return secrets
}

func isWorkspaceIntegrationSecretKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return key == "key" ||
		strings.Contains(key, "api_key") ||
		strings.Contains(key, "apikey") ||
		strings.Contains(key, "secret") ||
		strings.Contains(key, "token") ||
		strings.Contains(key, "password") ||
		strings.Contains(key, "bearer")
}

func redactWorkspaceIntegrationValue(value interface{}, secrets []string) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, child := range typed {
			out[key] = redactWorkspaceIntegrationValue(child, secrets)
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(typed))
		for _, child := range typed {
			out = append(out, redactWorkspaceIntegrationValue(child, secrets))
		}
		return out
	case string:
		return redactWorkspaceIntegrationString(typed, secrets)
	default:
		return typed
	}
}

func redactWorkspaceIntegrationString(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}
