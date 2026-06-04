package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/composio"
)

// handleComposioProxyTools is the agent-authenticated wrapper for tool listing.
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

	s.serveComposioListTools(w, r, machineID)
}

// handleComposioProxyExecute is the agent-authenticated wrapper for action execution.
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

	s.serveComposioExecuteAction(w, r, machineID)
}

// proxyTool is the plugin-facing representation of a Composio tool.
// Maps from the internal Tool struct (Composio API field names) to what the plugin expects.
type proxyTool struct {
	Name        string                 `json:"name"` // Action ID (slug), used by plugin for execute calls
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"` // Schema (mapped from input_parameters)
	Toolkit     string                 `json:"toolkit"`    // Toolkit slug for display
}

// requireComposioProxyToken extracts and validates a per-machine Composio proxy
// token from the Authorization header. Returns the machine ID from the token's
// sub claim, or writes a 401/503 and returns ok=false. The caller MUST use this
// returned machineID as the Composio user_id; any user_id in the body or query
// must be ignored.
func (s *Server) requireComposioProxyToken(w http.ResponseWriter, r *http.Request) (machineID string, ok bool) {
	if s.auth == nil {
		writeError(w, http.StatusServiceUnavailable, "composio proxy auth not configured")
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
	claims, err := s.auth.ValidateComposioProxyToken(token)
	if err != nil {
		slog.Warn("composio.proxy_token_invalid", "error", err)
		writeError(w, http.StatusUnauthorized, "invalid composio proxy token")
		return "", false
	}
	return claims.MachineID, true
}

// handleComposioListTools is the in-VM plugin entry point for listing tools.
// Authenticates the caller via a per-machine Composio proxy token and uses the
// machine ID from the token as the Composio user_id — any user_id query
// parameter is ignored.
func (s *Server) handleComposioListTools(w http.ResponseWriter, r *http.Request) {
	machineID, ok := s.requireComposioProxyToken(w, r)
	if !ok {
		return
	}
	s.serveComposioListTools(w, r, machineID)
}

// serveComposioListTools performs the actual Composio call for an already-
// authenticated machineID.
func (s *Server) serveComposioListTools(w http.ResponseWriter, r *http.Request, machineID string) {
	if s.composioClient == nil || !s.composioClient.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "composio not configured")
		return
	}

	toolkit := r.URL.Query().Get("toolkit")

	var tools []composio.Tool
	var err error
	if toolkit != "" {
		tools, err = s.composioClient.ListTools(r.Context(), machineID, toolkit)
	} else {
		// No toolkit specified: fetch tools only for connected toolkits.
		tools, err = s.composioClient.ListToolsForConnected(r.Context(), machineID)
	}
	if err != nil {
		slog.Error("composio.list_tools_failed",
			"machine_id_hash", hashForLog(machineID),
			"toolkit", toolkit,
			"error", err)
		writeError(w, http.StatusBadGateway, "failed to list tools")
		return
	}

	// Map internal Tool structs to plugin-friendly format.
	out := make([]proxyTool, len(tools))
	for i, t := range tools {
		out[i] = proxyTool{
			Name:        t.Slug, // Plugin uses name as action ID for execute calls.
			Description: t.Description,
			Parameters:  t.Parameters,
			Toolkit:     t.Toolkit.Slug,
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tools": out,
	})
}

// handleComposioExecuteAction is the in-VM plugin entry point for action
// execution. Authenticates the caller via a per-machine Composio proxy token
// and overrides any user_id in the body with the machine ID from the token.
func (s *Server) handleComposioExecuteAction(w http.ResponseWriter, r *http.Request) {
	machineID, ok := s.requireComposioProxyToken(w, r)
	if !ok {
		return
	}
	s.serveComposioExecuteAction(w, r, machineID)
}

// serveComposioExecuteAction performs the actual Composio call for an
// already-authenticated machineID.
func (s *Server) serveComposioExecuteAction(w http.ResponseWriter, r *http.Request, machineID string) {
	if s.composioClient == nil || !s.composioClient.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "composio not configured")
		return
	}

	actionName := chi.URLParam(r, "action")
	if actionName == "" {
		writeError(w, http.StatusBadRequest, "action name is required")
		return
	}

	var body struct {
		AppName string                 `json:"app_name"` // Optional; derived from action name if empty.
		Params  map[string]interface{} `json:"params"`
		// Note: any "user_id" field in the request body is ignored. The caller's
		// identity is taken from the validated Composio proxy token to prevent
		// horizontal privilege escalation.
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Derive app name from action slug prefix (e.g. "GMAIL_LIST_THREADS" → "gmail").
	appName := body.AppName
	if appName == "" {
		if idx := strings.Index(actionName, "_"); idx > 0 {
			appName = strings.ToLower(actionName[:idx])
		}
	}

	result, err := s.composioClient.ExecuteAction(r.Context(), actionName, machineID, appName, body.Params)
	if err != nil {
		slog.Error("composio.execute_action_failed",
			"action", actionName,
			"machine_id_hash", hashForLog(machineID),
			"error", err)
		writeError(w, http.StatusBadGateway, "failed to execute action")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// hashForLog returns a short stable hash of an identifier for log lines, so we
// can correlate without leaking the raw value to anyone with log-read access.
func hashForLog(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}
