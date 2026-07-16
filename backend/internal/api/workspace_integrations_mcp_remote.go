package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

const workspaceIntegrationMCPProtocolVersion = "2025-03-26"

type workspaceIntegrationMCPRemoteToolSpec struct {
	Name string                        `json:"name,omitempty"`
	Auth *workspaceIntegrationHTTPAuth `json:"auth,omitempty"`
}

type workspaceIntegrationMCPJSONRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type workspaceIntegrationMCPJSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (s *Server) callMCPRemoteWorkspaceIntegration(ctx context.Context, integration store.WorkspaceIntegration, tool workspaceIntegrationManifestTool, args map[string]interface{}) (map[string]interface{}, error) {
	if integration.Endpoint == nil || strings.TrimSpace(*integration.Endpoint) == "" {
		return nil, fmt.Errorf("mcp-remote integration %q is missing endpoint", integration.Slug)
	}
	remoteTool := tool.Name
	authSpec := (*workspaceIntegrationHTTPAuth)(nil)
	if tool.MCPRemote != nil {
		if strings.TrimSpace(tool.MCPRemote.Name) != "" {
			remoteTool = strings.TrimSpace(tool.MCPRemote.Name)
		}
		authSpec = tool.MCPRemote.Auth
	}
	endpoint := strings.TrimSpace(*integration.Endpoint)
	sessionID, err := s.initializeWorkspaceIntegrationMCPRemote(ctx, integration, endpoint, authSpec)
	if err != nil {
		return nil, err
	}
	// Per the MCP lifecycle, the client must send the `notifications/initialized`
	// notification after a successful initialize before issuing requests. Strict
	// servers reject tools/call until they receive it.
	if err := s.notifyWorkspaceIntegrationMCPRemoteInitialized(ctx, integration, endpoint, authSpec, sessionID); err != nil {
		return nil, err
	}
	result, err := s.postWorkspaceIntegrationMCPRemote(ctx, integration, endpoint, authSpec, sessionID, workspaceIntegrationMCPJSONRPCRequest{
		JSONRPC: "2.0",
		ID:      "call-tool",
		Method:  "tools/call",
		Params: map[string]interface{}{
			"name":      remoteTool,
			"arguments": args,
		},
	})
	if err != nil {
		return nil, err
	}
	var decoded interface{}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return nil, fmt.Errorf("decode mcp-remote tool result: %w", err)
	}
	if object, ok := decoded.(map[string]interface{}); ok {
		return object, nil
	}
	return map[string]interface{}{"value": decoded}, nil
}

func (s *Server) initializeWorkspaceIntegrationMCPRemote(ctx context.Context, integration store.WorkspaceIntegration, endpoint string, authSpec *workspaceIntegrationHTTPAuth) (string, error) {
	resp, err := s.postWorkspaceIntegrationMCPRemoteRaw(ctx, integration, endpoint, authSpec, "", workspaceIntegrationMCPJSONRPCRequest{
		JSONRPC: "2.0",
		ID:      "initialize",
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": workspaceIntegrationMCPProtocolVersion,
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]interface{}{
				"name":    "ocm-workspace-integrations",
				"version": "0.1.0",
			},
		},
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("mcp-remote initialize returned %d", resp.StatusCode)
	}
	body, err := readWorkspaceIntegrationMCPResponseBody(resp)
	if err != nil {
		return "", err
	}
	if body.Error != nil {
		return "", fmt.Errorf("mcp-remote initialize failed: %s", body.Error.Message)
	}
	return resp.Header.Get("Mcp-Session-Id"), nil
}

// notifyWorkspaceIntegrationMCPRemoteInitialized sends the MCP
// `notifications/initialized` notification. Notifications carry no id and expect
// no JSON-RPC response (servers typically reply 202 Accepted with an empty body),
// so we only check the HTTP status.
func (s *Server) notifyWorkspaceIntegrationMCPRemoteInitialized(ctx context.Context, integration store.WorkspaceIntegration, endpoint string, authSpec *workspaceIntegrationHTTPAuth, sessionID string) error {
	resp, err := s.postWorkspaceIntegrationMCPRemoteRaw(ctx, integration, endpoint, authSpec, sessionID, workspaceIntegrationMCPJSONRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mcp-remote initialized notification returned %d", resp.StatusCode)
	}
	return nil
}

func (s *Server) postWorkspaceIntegrationMCPRemote(ctx context.Context, integration store.WorkspaceIntegration, endpoint string, authSpec *workspaceIntegrationHTTPAuth, sessionID string, payload workspaceIntegrationMCPJSONRPCRequest) (json.RawMessage, error) {
	resp, err := s.postWorkspaceIntegrationMCPRemoteRaw(ctx, integration, endpoint, authSpec, sessionID, payload)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mcp-remote upstream returned %d", resp.StatusCode)
	}
	body, err := readWorkspaceIntegrationMCPResponseBody(resp)
	if err != nil {
		return nil, err
	}
	if body.Error != nil {
		return nil, fmt.Errorf("mcp-remote call failed: %s", body.Error.Message)
	}
	if len(body.Result) == 0 {
		return nil, errors.New("mcp-remote response missing result")
	}
	return body.Result, nil
}

func (s *Server) postWorkspaceIntegrationMCPRemoteRaw(ctx context.Context, integration store.WorkspaceIntegration, endpoint string, authSpec *workspaceIntegrationHTTPAuth, sessionID string, payload workspaceIntegrationMCPJSONRPCRequest) (*http.Response, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", workspaceIntegrationMCPProtocolVersion)
	if strings.TrimSpace(sessionID) != "" {
		req.Header.Set("Mcp-Session-Id", strings.TrimSpace(sessionID))
	}
	if err := s.applyWorkspaceIntegrationHTTPAuth(ctx, req, integration, authSpec, false); err != nil {
		return nil, err
	}
	return callWorkspaceIntegrationHTTPClient(s.allowInsecureWorkspaceIntegrationEndpoints).Do(req)
}

func readWorkspaceIntegrationMCPResponseBody(resp *http.Response) (workspaceIntegrationMCPJSONRPCResponse, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return workspaceIntegrationMCPJSONRPCResponse{}, err
	}
	raw := bytes.TrimSpace(body)
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		raw = extractWorkspaceIntegrationMCPSSEData(raw)
	}
	if len(raw) == 0 {
		return workspaceIntegrationMCPJSONRPCResponse{}, errors.New("mcp-remote response body is empty")
	}
	var decoded workspaceIntegrationMCPJSONRPCResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return workspaceIntegrationMCPJSONRPCResponse{}, fmt.Errorf("decode mcp-remote response: %w", err)
	}
	return decoded, nil
}

// extractWorkspaceIntegrationMCPSSEData pulls the data payload out of an SSE
// stream. Per the SSE spec a single event may carry multiple `data:` lines that
// are concatenated with newlines, and events are separated by blank lines. We
// return the data of the LAST complete event (the JSON-RPC response for our
// request, which may be preceded by notification/progress events). The previous
// implementation kept only the final `data:` line, silently dropping earlier
// lines of a multi-line event.
func extractWorkspaceIntegrationMCPSSEData(body []byte) []byte {
	var current [][]byte
	var last []byte
	flush := func() {
		if len(current) > 0 {
			last = bytes.Join(current, []byte("\n"))
			current = nil
		}
	}
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if len(bytes.TrimSpace(line)) == 0 {
			flush() // event boundary
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			value := bytes.TrimPrefix(line, []byte("data:"))
			value = bytes.TrimPrefix(value, []byte(" ")) // SSE strips one leading space
			current = append(current, value)
		}
	}
	flush()
	return bytes.TrimSpace(last)
}
