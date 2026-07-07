package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

const workspaceIntegrationMCPServerName = "ocm-workspace-integrations"

const (
	workspaceIntegrationMCPAgentGuidanceResourceURI = "ocm://workspace-integrations/agent-guidance"
	workspaceIntegrationMCPAgentGuidancePromptName  = "ocm_workspace_integrations"
)

type workspaceIntegrationMCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type workspaceIntegrationMCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type workspaceIntegrationMCPResponse struct {
	JSONRPC string                        `json:"jsonrpc"`
	ID      interface{}                   `json:"id,omitempty"`
	Result  interface{}                   `json:"result,omitempty"`
	Error   *workspaceIntegrationMCPError `json:"error,omitempty"`
}

type workspaceIntegrationMCPCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

type workspaceIntegrationMCPReadResourceParams struct {
	URI string `json:"uri"`
}

type workspaceIntegrationMCPGetPromptParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

func (s *Server) handleWorkspaceIntegrationMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	machineID, ok := s.requireWorkspaceIntegrationToken(w, r)
	if !ok {
		return
	}
	var req workspaceIntegrationMCPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeWorkspaceIntegrationMCPError(w, nil, -32700, "Parse error")
		return
	}
	if req.Method == "notifications/initialized" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeWorkspaceIntegrationMCPError(w, req.ID, -32600, "Invalid Request")
		return
	}
	switch req.Method {
	case "initialize":
		writeWorkspaceIntegrationMCPResult(w, req.ID, map[string]interface{}{
			"protocolVersion": workspaceIntegrationMCPProtocolVersion,
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{
					"listChanged": false,
				},
				"resources": map[string]interface{}{
					"listChanged": false,
				},
				"prompts": map[string]interface{}{
					"listChanged": false,
				},
			},
			"serverInfo": map[string]interface{}{
				"name":    workspaceIntegrationMCPServerName,
				"version": "0.1.0",
			},
		})
	case "tools/list":
		tools := s.workspaceIntegrationMCPFacadeTools()
		mcpTools := make([]map[string]interface{}, 0, len(tools))
		for _, tool := range tools {
			mcpTools = append(mcpTools, map[string]interface{}{
				"name":        tool.Name,
				"description": tool.Description,
				"inputSchema": jsonRawMessageOrDefault(tool.Parameters),
			})
		}
		writeWorkspaceIntegrationMCPResult(w, req.ID, map[string]interface{}{"tools": mcpTools})
	case "resources/list":
		writeWorkspaceIntegrationMCPResult(w, req.ID, map[string]interface{}{
			"resources": workspaceIntegrationMCPResources(),
		})
	case "resources/read":
		var params workspaceIntegrationMCPReadResourceParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeWorkspaceIntegrationMCPError(w, req.ID, -32602, "Invalid resource read params")
				return
			}
		}
		resource, ok := workspaceIntegrationMCPResourceContent(params.URI)
		if !ok {
			writeWorkspaceIntegrationMCPError(w, req.ID, -32602, "Resource not found")
			return
		}
		writeWorkspaceIntegrationMCPResult(w, req.ID, map[string]interface{}{
			"contents": []map[string]interface{}{
				resource,
			},
		})
	case "prompts/list":
		writeWorkspaceIntegrationMCPResult(w, req.ID, map[string]interface{}{
			"prompts": workspaceIntegrationMCPPrompts(),
		})
	case "prompts/get":
		var params workspaceIntegrationMCPGetPromptParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeWorkspaceIntegrationMCPError(w, req.ID, -32602, "Invalid prompt get params")
				return
			}
		}
		prompt, ok := workspaceIntegrationMCPPromptContent(params.Name)
		if !ok {
			writeWorkspaceIntegrationMCPError(w, req.ID, -32602, "Prompt not found")
			return
		}
		writeWorkspaceIntegrationMCPResult(w, req.ID, prompt)
	case "tools/call":
		var params workspaceIntegrationMCPCallParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeWorkspaceIntegrationMCPError(w, req.ID, -32602, "Invalid tool call params")
				return
			}
		}
		if params.Name == "" {
			writeWorkspaceIntegrationMCPError(w, req.ID, -32602, "Tool name is required")
			return
		}
		if params.Arguments == nil {
			params.Arguments = map[string]interface{}{}
		}
		if !s.isWorkspaceIntegrationMCPFacadeTool(params.Name) {
			writeWorkspaceIntegrationMCPError(w, req.ID, -32602, "Tool not found")
			return
		}
		if s.isWorkspaceIntegrationWorkflowTool(params.Name) {
			_, status, err := s.callWorkspaceIntegrationWorkflowTool(r.Context(), params.Name, params.Arguments)
			if err != nil {
				if status == http.StatusBadRequest || status == http.StatusNotFound || status == http.StatusConflict {
					writeWorkspaceIntegrationMCPError(w, req.ID, -32602, err.Error())
					return
				}
				writeWorkspaceIntegrationMCPError(w, req.ID, -32603, err.Error())
				return
			}
		}
		integrations, err := s.store.ListEnabledWorkspaceIntegrationsForMachine(r.Context(), machineID)
		if err != nil {
			writeWorkspaceIntegrationMCPError(w, req.ID, -32603, "Failed to list workspace integration tools")
			return
		}
		var result map[string]interface{}
		var status int
		if params.Name == workspaceIntegrationExecuteToolName {
			result, status, err = s.callWorkspaceIntegrationExecute(r.Context(), machineID, integrations, params.Arguments)
		} else {
			result, status, err = s.callWorkspaceIntegrationFacadeTool(r.Context(), machineID, integrations, params.Name, params.Arguments)
		}
		if err != nil {
			if status == http.StatusBadRequest || status == http.StatusNotFound || status == http.StatusConflict {
				writeWorkspaceIntegrationMCPError(w, req.ID, -32602, err.Error())
				return
			}
			writeWorkspaceIntegrationMCPError(w, req.ID, -32603, err.Error())
			return
		}
		text, _ := json.Marshal(result)
		writeWorkspaceIntegrationMCPResult(w, req.ID, map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": string(text)},
			},
			"structuredContent": result,
		})
	default:
		writeWorkspaceIntegrationMCPError(w, req.ID, -32601, "Method not found")
	}
}

func workspaceIntegrationMCPResources() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"uri":         workspaceIntegrationMCPAgentGuidanceResourceURI,
			"name":        "OCM workspace integrations agent guidance",
			"description": "How agents should discover and call OCM workspace integration tools.",
			"mimeType":    "text/markdown",
		},
	}
}

func workspaceIntegrationMCPResourceContent(uri string) (map[string]interface{}, bool) {
	if strings.TrimSpace(uri) != workspaceIntegrationMCPAgentGuidanceResourceURI {
		return nil, false
	}
	return map[string]interface{}{
		"uri":      workspaceIntegrationMCPAgentGuidanceResourceURI,
		"mimeType": "text/markdown",
		"text":     workspaceIntegrationMCPAgentGuidanceText(),
	}, true
}

func workspaceIntegrationMCPPrompts() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"name":        workspaceIntegrationMCPAgentGuidancePromptName,
			"description": "Use OCM workspace integrations through search, describe, and call.",
		},
	}
}

func workspaceIntegrationMCPPromptContent(name string) (map[string]interface{}, bool) {
	if strings.TrimSpace(name) != workspaceIntegrationMCPAgentGuidancePromptName {
		return nil, false
	}
	return map[string]interface{}{
		"description": "Use OCM workspace integrations through the OCM facade.",
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": map[string]interface{}{
					"type": "text",
					"text": workspaceIntegrationMCPAgentGuidanceText(),
				},
			},
		},
	}, true
}

func workspaceIntegrationMCPAgentGuidanceText() string {
	return strings.TrimSpace(`
# OCM Workspace Integrations

Use the OCM facade for every workspace integration call.

1. Search first with ocm.search_tools using the user's intent.
2. Select one result and keep its tool_address.
3. Describe that exact tool with ocm.describe_tool and the same tool_address.
4. Call it with ocm.call_tool and the same tool_address.

Prefer tool_address over tool_id because a workspace can connect the same provider more than once. Use tool_id only when search or describe proves it is unambiguous.

Do not invent provider tool names, call provider tools directly, expose machine tokens, or bypass OCM with raw provider APIs.

If Google Workspace returns an authentication or authorization error, explain the recovery in user terms:

- HTTP 401: reconnect the Google account.
- HTTP 403 with missing scopes: reconnect and grant the required Gmail, Drive, or Calendar scope.
- HTTP 403 with admin policy: ask a Google Workspace admin to allow the app or API.
- HTTP 429: retry later unless the call was a write operation.

Use direct search, describe, and call for one-off single actions.

Use ocm.execute only for one-off multi-step work that needs local filtering, joining, pagination, or batching. Do not use it for a single tool call.

Use saved workflows only for repeated, scheduled, shared, durable, or approval-heavy work.

Do not claim execute or workflow support unless those tools are explicitly listed by tools/list.
`)
}

func jsonRawMessageOrDefault(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return map[string]interface{}{"type": "object", "additionalProperties": true}
	}
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return map[string]interface{}{"type": "object", "additionalProperties": true}
	}
	return decoded
}

func writeWorkspaceIntegrationMCPResult(w http.ResponseWriter, id interface{}, result interface{}) {
	writeJSON(w, http.StatusOK, workspaceIntegrationMCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func writeWorkspaceIntegrationMCPError(w http.ResponseWriter, id interface{}, code int, message string) {
	writeJSON(w, http.StatusOK, workspaceIntegrationMCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &workspaceIntegrationMCPError{
			Code:    code,
			Message: message,
		},
	})
}
