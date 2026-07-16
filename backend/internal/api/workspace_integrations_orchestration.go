package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/google/uuid"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

const (
	workspaceIntegrationExecuteToolName        = "ocm.execute"
	workspaceIntegrationCreateWorkflowToolName = "ocm.create_workflow"
	workspaceIntegrationRunWorkflowToolName    = "ocm.run_workflow"
	workspaceIntegrationResumeWorkflowToolName = "ocm.resume_workflow"

	workspaceIntegrationExecuteEnabledEnv   = "OCM_WORKSPACE_INTEGRATIONS_EXECUTE_ENABLED"
	workspaceIntegrationWorkflowsEnabledEnv = "OCM_WORKSPACE_INTEGRATIONS_WORKFLOWS_ENABLED"

	workspaceIntegrationExecuteMaxScriptBytes = 64 * 1024
	workspaceIntegrationExecuteMaxOutputBytes = 128 * 1024
	workspaceIntegrationExecuteMaxToolCalls   = 25
)

var workspaceIntegrationExecuteWallTimeout = 30 * time.Second

type workspaceIntegrationOrchestrationFeatureFlags struct {
	Execute   bool
	Workflows bool
}

type workspaceIntegrationExecuteToolCall struct {
	Name           string `json:"name"`
	ToolID         string `json:"tool_id,omitempty"`
	ToolAddress    string `json:"tool_address,omitempty"`
	Access         string `json:"access,omitempty"`
	PolicyState    string `json:"policy_state,omitempty"`
	PolicyDecision string `json:"policy_decision,omitempty"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
}

type workspaceIntegrationExecuteTrace struct {
	ExecutionID string                                `json:"execution_id"`
	ScriptHash  string                                `json:"script_hash"`
	Status      string                                `json:"status"`
	Error       string                                `json:"error,omitempty"`
	TimedOut    bool                                  `json:"timed_out,omitempty"`
	DurationMS  int                                   `json:"duration_ms"`
	OutputBytes int                                   `json:"output_bytes,omitempty"`
	ToolCalls   []workspaceIntegrationExecuteToolCall `json:"tool_calls,omitempty"`
}

type workspaceIntegrationExecuteRunner struct {
	ctx          context.Context
	server       *Server
	vm           *goja.Runtime
	machineID    string
	integrations []store.WorkspaceIntegration
	toolCalls    []workspaceIntegrationExecuteToolCall
}

var workspaceIntegrationExecuteTraceSink func(context.Context, workspaceIntegrationExecuteTrace)

func workspaceIntegrationOrchestrationFeatureFlagsFromEnv() workspaceIntegrationOrchestrationFeatureFlags {
	return workspaceIntegrationOrchestrationFeatureFlags{
		Execute:   workspaceIntegrationTruthyEnv(os.Getenv(workspaceIntegrationExecuteEnabledEnv)),
		Workflows: workspaceIntegrationTruthyEnv(os.Getenv(workspaceIntegrationWorkflowsEnabledEnv)),
	}
}

func workspaceIntegrationTruthyEnv(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y", "on", "enabled":
		return true
	default:
		return false
	}
}

func (s *Server) workspaceIntegrationMCPFacadeTools() []workspaceIntegrationTool {
	tools := append([]workspaceIntegrationTool{}, workspaceIntegrationFacadeTools()...)
	if s.workspaceIntegrationExecuteEnabled {
		tools = append(tools, workspaceIntegrationExecuteTool())
	}
	if s.workspaceIntegrationWorkflowsEnabled {
		tools = append(tools, workspaceIntegrationWorkflowTools()...)
	}
	return tools
}

func (s *Server) isWorkspaceIntegrationMCPFacadeTool(name string) bool {
	if isWorkspaceIntegrationFacadeTool(name) {
		return true
	}
	if s.workspaceIntegrationExecuteEnabled && name == workspaceIntegrationExecuteToolName {
		return true
	}
	return s.isWorkspaceIntegrationWorkflowTool(name)
}

func (s *Server) isWorkspaceIntegrationWorkflowTool(name string) bool {
	if !s.workspaceIntegrationWorkflowsEnabled {
		return false
	}
	switch name {
	case workspaceIntegrationCreateWorkflowToolName, workspaceIntegrationRunWorkflowToolName, workspaceIntegrationResumeWorkflowToolName:
		return true
	default:
		return false
	}
}

func workspaceIntegrationExecuteTool() workspaceIntegrationTool {
	return workspaceIntegrationTool{
		Name:        workspaceIntegrationExecuteToolName,
		Description: "Run a bounded, read-only JavaScript orchestration over OCM workspace integration facade bindings.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"script":{"type":"string","description":"JavaScript body executed as a function. Use tools.search(...), tools.describe(...), and read-only tools.call(...).","maxLength":65536}},"required":["script"],"additionalProperties":false}`),
		Integration: workspaceIntegrationFacadeSlug,
		Kind:        "facade",
		Transport:   "facade",
		Access:      "read",
		Policy:      "allowed",
		Source:      "ocm",
	}
}

func workspaceIntegrationWorkflowTools() []workspaceIntegrationTool {
	return []workspaceIntegrationTool{
		{
			Name:        workspaceIntegrationCreateWorkflowToolName,
			Description: "Create and validate a versioned saved workspace-integration workflow definition. Unavailable until restricted Lobster validation and Task Flow tracking are configured.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Human-readable workflow name."},"definition":{"type":"object","description":"Restricted Lobster workflow definition. Provider access must use OCM facade calls only.","additionalProperties":true}},"required":["name","definition"],"additionalProperties":false}`),
			Integration: workspaceIntegrationFacadeSlug,
			Kind:        "facade",
			Transport:   "facade",
			Access:      "write",
			Policy:      "allowed",
			Source:      "ocm",
		},
		{
			Name:        workspaceIntegrationRunWorkflowToolName,
			Description: "Start a new Task Flow run from a saved workspace-integration workflow definition. Unavailable until restricted Lobster execution and Task Flow tracking are configured.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"workflow_id":{"type":"string","description":"Saved workflow definition ID."},"input":{"type":"object","description":"Input values for the new run.","additionalProperties":true}},"required":["workflow_id"],"additionalProperties":false}`),
			Integration: workspaceIntegrationFacadeSlug,
			Kind:        "facade",
			Transport:   "facade",
			Access:      "write",
			Policy:      "allowed",
			Source:      "ocm",
		},
		{
			Name:        workspaceIntegrationResumeWorkflowToolName,
			Description: "Resume, approve, cancel, or retry an existing workspace-integration Task Flow run. Unavailable until restricted Lobster execution and Task Flow tracking are configured.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"run_id":{"type":"string","description":"Existing Task Flow run ID."},"action":{"type":"string","enum":["resume","approve","cancel","retry"],"description":"How to continue the existing run."},"input":{"type":"object","description":"Optional resume or approval payload.","additionalProperties":true}},"required":["run_id","action"],"additionalProperties":false}`),
			Integration: workspaceIntegrationFacadeSlug,
			Kind:        "facade",
			Transport:   "facade",
			Access:      "write",
			Policy:      "allowed",
			Source:      "ocm",
		},
	}
}

func (s *Server) callWorkspaceIntegrationWorkflowTool(_ context.Context, name string, _ map[string]interface{}) (map[string]interface{}, int, error) {
	if !s.isWorkspaceIntegrationWorkflowTool(name) {
		return nil, http.StatusNotFound, errors.New("tool not found")
	}
	return nil, http.StatusServiceUnavailable, errors.New("workspace integration saved workflows require restricted Lobster validation and Task Flow run tracking before they can be used")
}

func (s *Server) callWorkspaceIntegrationExecute(ctx context.Context, machineID string, integrations []store.WorkspaceIntegration, args map[string]interface{}) (map[string]interface{}, int, error) {
	if !s.workspaceIntegrationExecuteEnabled {
		return nil, http.StatusNotFound, errors.New("tool not found")
	}
	script, ok := args["script"].(string)
	if !ok || strings.TrimSpace(script) == "" {
		return nil, http.StatusBadRequest, errors.New("script is required")
	}
	if len([]byte(script)) > workspaceIntegrationExecuteMaxScriptBytes {
		return nil, http.StatusBadRequest, fmt.Errorf("script exceeds %d byte limit", workspaceIntegrationExecuteMaxScriptBytes)
	}

	start := time.Now()
	executionID := uuid.NewString()
	scriptHash := sha256.Sum256([]byte(script))
	scriptHashHex := hex.EncodeToString(scriptHash[:])
	runCtx, cancel := context.WithTimeout(ctx, workspaceIntegrationExecuteWallTimeout)
	defer cancel()

	vm := goja.New()
	runner := &workspaceIntegrationExecuteRunner{
		ctx:          runCtx,
		server:       s,
		vm:           vm,
		machineID:    machineID,
		integrations: integrations,
	}
	tools := vm.NewObject()
	if err := tools.Set("search", runner.search); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if err := tools.Set("describe", runner.describe); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if err := tools.Set("call", runner.call); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if err := vm.Set("tools", tools); err != nil {
		return nil, http.StatusInternalServerError, err
	}

	timer := time.AfterFunc(workspaceIntegrationExecuteWallTimeout, func() {
		vm.Interrupt("ocm.execute timed out")
	})
	defer timer.Stop()

	value, err := vm.RunString("(function(){\n" + script + "\n})()")
	if err != nil {
		s.emitWorkspaceIntegrationExecuteTrace(ctx, workspaceIntegrationExecuteTrace{
			ExecutionID: executionID,
			ScriptHash:  scriptHashHex,
			Status:      "error",
			Error:       workspaceIntegrationExecuteSafeError(err),
			TimedOut:    workspaceIntegrationExecuteTimedOut(runCtx, err),
			DurationMS:  int(time.Since(start).Milliseconds()),
			ToolCalls:   workspaceIntegrationExecuteTraceToolCalls(runner.toolCalls),
		})
		return nil, http.StatusBadRequest, err
	}
	if err := runCtx.Err(); err != nil {
		s.emitWorkspaceIntegrationExecuteTrace(ctx, workspaceIntegrationExecuteTrace{
			ExecutionID: executionID,
			ScriptHash:  scriptHashHex,
			Status:      "error",
			Error:       workspaceIntegrationExecuteSafeError(err),
			TimedOut:    true,
			DurationMS:  int(time.Since(start).Milliseconds()),
			ToolCalls:   workspaceIntegrationExecuteTraceToolCalls(runner.toolCalls),
		})
		return nil, http.StatusBadRequest, err
	}

	result := map[string]interface{}{
		"execution_id": executionID,
		"script_hash":  scriptHashHex,
		"result":       workspaceIntegrationExecuteExportValue(value),
		"tool_calls":   runner.toolCalls,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("marshal execute result: %w", err)
	}
	if len(encoded) > workspaceIntegrationExecuteMaxOutputBytes {
		err := fmt.Errorf("ocm.execute output exceeds %d byte limit", workspaceIntegrationExecuteMaxOutputBytes)
		s.emitWorkspaceIntegrationExecuteTrace(ctx, workspaceIntegrationExecuteTrace{
			ExecutionID: executionID,
			ScriptHash:  scriptHashHex,
			Status:      "error",
			Error:       workspaceIntegrationExecuteSafeError(err),
			DurationMS:  int(time.Since(start).Milliseconds()),
			OutputBytes: len(encoded),
			ToolCalls:   workspaceIntegrationExecuteTraceToolCalls(runner.toolCalls),
		})
		return nil, http.StatusBadRequest, err
	}
	s.emitWorkspaceIntegrationExecuteTrace(ctx, workspaceIntegrationExecuteTrace{
		ExecutionID: executionID,
		ScriptHash:  scriptHashHex,
		Status:      "success",
		DurationMS:  int(time.Since(start).Milliseconds()),
		OutputBytes: len(encoded),
		ToolCalls:   workspaceIntegrationExecuteTraceToolCalls(runner.toolCalls),
	})
	return result, http.StatusOK, nil
}

func (r *workspaceIntegrationExecuteRunner) search(call goja.FunctionCall) goja.Value {
	args := map[string]interface{}{}
	if len(call.Arguments) > 0 && !goja.IsUndefined(call.Argument(0)) && !goja.IsNull(call.Argument(0)) {
		var err error
		args, err = workspaceIntegrationExecuteValueObject(call.Argument(0), "tools.search input")
		if err != nil {
			r.fail(err)
		}
	}
	result, status, err := r.callFacade(workspaceIntegrationSearchToolsName, args, workspaceIntegrationExecuteToolCall{Name: workspaceIntegrationSearchToolsName})
	if err != nil {
		r.fail(workspaceIntegrationExecuteFacadeError(status, err))
	}
	return r.vm.ToValue(result)
}

func (r *workspaceIntegrationExecuteRunner) describe(call goja.FunctionCall) goja.Value {
	if len(call.Arguments) == 0 {
		r.fail(errors.New("tools.describe selector is required"))
	}
	args, requestedTool, err := workspaceIntegrationExecuteSelectorArgs(call.Argument(0).Export())
	if err != nil {
		r.fail(err)
	}
	result, status, err := r.callFacade(workspaceIntegrationDescribeToolName, args, workspaceIntegrationExecuteToolCall{
		Name:        workspaceIntegrationDescribeToolName,
		ToolAddress: workspaceIntegrationExecuteTraceToolAddress(requestedTool),
		ToolID:      workspaceIntegrationExecuteTraceToolID(requestedTool),
	})
	if err != nil {
		r.fail(workspaceIntegrationExecuteFacadeError(status, err))
	}
	return r.vm.ToValue(result)
}

func (r *workspaceIntegrationExecuteRunner) call(call goja.FunctionCall) goja.Value {
	if len(call.Arguments) == 0 {
		r.fail(errors.New("tools.call selector is required"))
	}
	args, requestedTool, err := workspaceIntegrationExecuteSelectorArgs(call.Argument(0).Export())
	if err != nil {
		r.fail(err)
	}
	callArgs := map[string]interface{}{}
	if len(call.Arguments) > 1 && !goja.IsUndefined(call.Argument(1)) && !goja.IsNull(call.Argument(1)) {
		callArgs, err = workspaceIntegrationExecuteValueObject(call.Argument(1), "tools.call arguments")
		if err != nil {
			r.fail(err)
		}
	}
	descriptor, status, err := r.resolveReadTool(requestedTool)
	if err != nil {
		r.fail(workspaceIntegrationExecuteFacadeError(status, err))
	}
	args["arguments"] = callArgs
	result, status, err := r.callFacade(workspaceIntegrationCallToolName, args, workspaceIntegrationExecuteToolCall{
		Name:           workspaceIntegrationCallToolName,
		ToolAddress:    descriptor.ToolAddress,
		ToolID:         descriptor.ToolID,
		Access:         descriptor.Access,
		PolicyState:    firstNonEmpty(descriptor.PolicyState, workspaceIntegrationPolicyAllow),
		PolicyDecision: "allowed",
	})
	if err != nil {
		r.fail(workspaceIntegrationExecuteFacadeError(status, err))
	}
	return r.vm.ToValue(result)
}

func (r *workspaceIntegrationExecuteRunner) resolveReadTool(requestedTool string) (workspaceIntegrationToolDescriptor, int, error) {
	descriptor, resolution := resolveWorkspaceIntegrationFacadeToolDescriptor(
		r.server.buildWorkspaceIntegrationRuntimeToolDescriptors(r.ctx, r.machineID, r.integrations, false),
		requestedTool,
	)
	if resolution.Ambiguous {
		return workspaceIntegrationToolDescriptor{}, http.StatusConflict, workspaceIntegrationAmbiguousToolError(requestedTool, resolution.Candidates)
	}
	if !resolution.Found {
		return workspaceIntegrationToolDescriptor{}, http.StatusNotFound, errors.New("tool not found")
	}
	if !strings.EqualFold(strings.TrimSpace(descriptor.Access), "read") {
		if _, err := r.recordToolCall(workspaceIntegrationExecuteToolCall{
			Name:           workspaceIntegrationCallToolName,
			ToolAddress:    descriptor.ToolAddress,
			ToolID:         descriptor.ToolID,
			Access:         descriptor.Access,
			PolicyState:    firstNonEmpty(descriptor.PolicyState, workspaceIntegrationPolicyAllow),
			PolicyDecision: "read_only_denied",
			Status:         "error",
			Error:          "ocm.execute can only call read tools in this phase",
		}); err != nil {
			return workspaceIntegrationToolDescriptor{}, http.StatusBadRequest, err
		}
		return workspaceIntegrationToolDescriptor{}, http.StatusBadRequest, errors.New("ocm.execute can only call read tools in this phase")
	}
	return descriptor, http.StatusOK, nil
}

func (r *workspaceIntegrationExecuteRunner) callFacade(name string, args map[string]interface{}, callRecord workspaceIntegrationExecuteToolCall) (map[string]interface{}, int, error) {
	if callRecord.Name == "" {
		callRecord.Name = name
	}
	callRecord.Status = "started"
	callIndex, err := r.recordToolCall(callRecord)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	result, status, err := r.server.callWorkspaceIntegrationFacadeTool(r.ctx, r.machineID, r.integrations, name, args)
	if err != nil {
		r.toolCalls[callIndex].Status = "error"
		r.toolCalls[callIndex].Error = workspaceIntegrationExecuteSafeError(err)
		return nil, status, err
	}
	r.toolCalls[callIndex].Status = "success"
	return result, status, nil
}

func (r *workspaceIntegrationExecuteRunner) recordToolCall(call workspaceIntegrationExecuteToolCall) (int, error) {
	if len(r.toolCalls) >= workspaceIntegrationExecuteMaxToolCalls {
		return -1, fmt.Errorf("ocm.execute exceeded %d tool call limit", workspaceIntegrationExecuteMaxToolCalls)
	}
	r.toolCalls = append(r.toolCalls, call)
	return len(r.toolCalls) - 1, nil
}

func (r *workspaceIntegrationExecuteRunner) fail(err error) {
	panic(r.vm.NewGoError(err))
}

func workspaceIntegrationExecuteFacadeError(status int, err error) error {
	if err == nil {
		return nil
	}
	if status == http.StatusBadRequest || status == http.StatusNotFound || status == http.StatusConflict {
		return err
	}
	return fmt.Errorf("workspace integration facade call failed: %w", err)
}

func workspaceIntegrationExecuteValueObject(value goja.Value, label string) (map[string]interface{}, error) {
	exported := value.Export()
	if exported == nil {
		return nil, fmt.Errorf("%s must be an object", label)
	}
	object, ok := exported.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%s must be an object", label)
	}
	return object, nil
}

func workspaceIntegrationExecuteSelectorArgs(selector interface{}) (map[string]interface{}, string, error) {
	switch value := selector.(type) {
	case string:
		requestedTool := strings.TrimSpace(value)
		if requestedTool == "" {
			return nil, "", errors.New("tool selector cannot be empty")
		}
		if strings.HasPrefix(requestedTool, workspaceIntegrationToolAddressPrefix) {
			return map[string]interface{}{"tool_address": requestedTool}, requestedTool, nil
		}
		return map[string]interface{}{"tool_id": requestedTool}, requestedTool, nil
	case map[string]interface{}:
		if toolAddress := strings.TrimSpace(workspaceIntegrationArgString(value["tool_address"])); toolAddress != "" {
			return map[string]interface{}{"tool_address": toolAddress}, toolAddress, nil
		}
		if toolID := strings.TrimSpace(workspaceIntegrationArgString(value["tool_id"])); toolID != "" {
			return map[string]interface{}{"tool_id": toolID}, toolID, nil
		}
	}
	return nil, "", errors.New("tool selector must be a string or object with tool_address or tool_id")
}

func workspaceIntegrationExecuteTraceToolAddress(requestedTool string) string {
	requestedTool = strings.TrimSpace(requestedTool)
	if strings.HasPrefix(requestedTool, workspaceIntegrationToolAddressPrefix) {
		return requestedTool
	}
	return ""
}

func workspaceIntegrationExecuteTraceToolID(requestedTool string) string {
	requestedTool = strings.TrimSpace(requestedTool)
	if requestedTool == "" || strings.HasPrefix(requestedTool, workspaceIntegrationToolAddressPrefix) {
		return ""
	}
	return requestedTool
}

func workspaceIntegrationExecuteExportValue(value goja.Value) interface{} {
	if value == nil || goja.IsUndefined(value) || goja.IsNull(value) {
		return nil
	}
	return value.Export()
}

func (s *Server) emitWorkspaceIntegrationExecuteTrace(ctx context.Context, trace workspaceIntegrationExecuteTrace) {
	trace.ToolCalls = workspaceIntegrationExecuteTraceToolCalls(trace.ToolCalls)
	attrs := []interface{}{
		"execution_id", trace.ExecutionID,
		"script_hash", trace.ScriptHash,
		"status", trace.Status,
		"duration_ms", trace.DurationMS,
		"tool_call_count", len(trace.ToolCalls),
	}
	if trace.OutputBytes > 0 {
		attrs = append(attrs, "output_bytes", trace.OutputBytes)
	}
	if trace.TimedOut {
		attrs = append(attrs, "timed_out", true)
	}
	if trace.Error != "" {
		attrs = append(attrs, "error", trace.Error)
	}
	if len(trace.ToolCalls) > 0 {
		toolAddresses := make([]string, 0, len(trace.ToolCalls))
		policyDecisions := make([]string, 0, len(trace.ToolCalls))
		for _, call := range trace.ToolCalls {
			if call.ToolAddress != "" {
				toolAddresses = append(toolAddresses, call.ToolAddress)
			}
			if call.PolicyDecision != "" {
				policyDecisions = append(policyDecisions, call.PolicyDecision)
			}
		}
		if len(toolAddresses) > 0 {
			attrs = append(attrs, "tool_addresses", toolAddresses)
		}
		if len(policyDecisions) > 0 {
			attrs = append(attrs, "policy_decisions", policyDecisions)
		}
	}
	if trace.Status == "error" {
		slog.Warn("workspace_integrations.execute", attrs...)
	} else {
		slog.Info("workspace_integrations.execute", attrs...)
	}
	if workspaceIntegrationExecuteTraceSink != nil {
		workspaceIntegrationExecuteTraceSink(ctx, trace)
	}
}

func workspaceIntegrationExecuteTraceToolCalls(calls []workspaceIntegrationExecuteToolCall) []workspaceIntegrationExecuteToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]workspaceIntegrationExecuteToolCall, len(calls))
	copy(out, calls)
	return out
}

func workspaceIntegrationExecuteSafeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if before, _, found := strings.Cut(message, "\n"); found {
		message = strings.TrimSpace(before)
	}
	const maxExecuteTraceErrorBytes = 512
	if len(message) > maxExecuteTraceErrorBytes {
		message = message[:maxExecuteTraceErrorBytes] + "...[truncated]"
	}
	return message
}

func workspaceIntegrationExecuteTimedOut(ctx context.Context, err error) bool {
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	return err != nil && strings.Contains(err.Error(), "ocm.execute timed out")
}
