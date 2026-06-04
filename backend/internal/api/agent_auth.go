package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/configassembly"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

// authenticateAgentToken validates the bearer token against per-host or fleet-wide tokens.
// Returns the host ID on success (0 for fleet-wide), or writes an error and returns false.
func (s *Server) authenticateAgentToken(w http.ResponseWriter, r *http.Request) (int, bool) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "missing bearer token")
		return 0, false
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	// Try per-host token lookup
	host, err := s.store.GetHostByAgentToken(r.Context(), token)
	if err == nil && host != nil {
		return host.ID, true
	}

	// Fall back to fleet-wide token
	if s.agentToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.agentToken)) == 1 {
		return 0, true
	}

	writeError(w, http.StatusUnauthorized, "invalid agent token")
	return 0, false
}

// validateMachinePlacement verifies that a machine is currently placed on the given host.
// For fleet-wide tokens (hostID=0), the check is skipped.
func (s *Server) validateMachinePlacement(ctx context.Context, machineID string, hostID int) error {
	if hostID == 0 {
		return nil
	}
	machine, err := s.store.GetMachine(ctx, machineID)
	if err != nil {
		return fmt.Errorf("machine not found")
	}
	if machine.HostID == nil || *machine.HostID != hostID {
		return fmt.Errorf("machine is not placed on this host")
	}
	return nil
}

func (s *Server) handleAgentAuthListAgents(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}

	machineID := chi.URLParam(r, "machineID")
	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	agents, err := s.store.ListMachineAgents(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

func (s *Server) handleAgentAuthCreateAgent(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}

	machineID := chi.URLParam(r, "machineID")
	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	var req struct {
		AgentID        string  `json:"agent_id"`
		Name           string  `json:"name"`
		Model          *string `json:"model,omitempty"`
		IdentityEmoji  *string `json:"identity_emoji,omitempty"`
		IdentityAvatar *string `json:"identity_avatar,omitempty"`
		Soul           *string `json:"soul,omitempty"`
		IsDefault      bool    `json:"is_default"`
		SortOrder      int     `json:"sort_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.AgentID == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "agent_id and name are required")
		return
	}
	if !validAgentID.MatchString(req.AgentID) {
		writeError(w, http.StatusBadRequest, "agent_id must be lowercase alphanumeric with hyphens, starting with a letter")
		return
	}
	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if err := s.validateAgentModel(r.Context(), machineID, machine.AccountID, req.Model); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	agent := &store.MachineAgent{
		MachineID:      machineID,
		AgentID:        req.AgentID,
		Name:           req.Name,
		Model:          req.Model,
		IdentityEmoji:  req.IdentityEmoji,
		IdentityAvatar: req.IdentityAvatar,
		Soul:           req.Soul,
		IsDefault:      req.IsDefault,
		SortOrder:      req.SortOrder,
	}

	if err := s.store.CreateMachineAgent(r.Context(), agent); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "agent_id already exists on this machine")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("machine.agent.created", "machine_id", machineID, "agent_id", req.AgentID, "via", "agent_auth")
	writeJSON(w, http.StatusCreated, agent)
}

func (s *Server) handleAgentAuthUpdateAgent(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}

	machineID := chi.URLParam(r, "machineID")
	agentID := chi.URLParam(r, "agentId")
	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	existing, err := s.store.GetMachineAgent(r.Context(), machineID, agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	var req struct {
		Name           *string `json:"name,omitempty"`
		Model          *string `json:"model,omitempty"`
		IdentityEmoji  *string `json:"identity_emoji,omitempty"`
		IdentityAvatar *string `json:"identity_avatar,omitempty"`
		Soul           *string `json:"soul,omitempty"`
		IsDefault      *bool   `json:"is_default,omitempty"`
		SortOrder      *int    `json:"sort_order,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if err := s.validateAgentModel(r.Context(), machineID, machine.AccountID, req.Model); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.Model != nil {
		existing.Model = req.Model
	}
	if req.IdentityEmoji != nil {
		existing.IdentityEmoji = req.IdentityEmoji
	}
	if req.IdentityAvatar != nil {
		existing.IdentityAvatar = req.IdentityAvatar
	}
	if req.Soul != nil {
		existing.Soul = req.Soul
	}
	if req.IsDefault != nil {
		existing.IsDefault = *req.IsDefault
	}
	if req.SortOrder != nil {
		existing.SortOrder = *req.SortOrder
	}

	if err := s.store.UpdateMachineAgent(r.Context(), existing); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("machine.agent.updated", "machine_id", machineID, "agent_id", agentID, "via", "agent_auth")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAgentAuthDeleteAgent(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}

	machineID := chi.URLParam(r, "machineID")
	agentID := chi.URLParam(r, "agentId")
	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	existing, err := s.store.GetMachineAgent(r.Context(), machineID, agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if existing.IsDefault {
		writeError(w, http.StatusBadRequest, "cannot delete the default agent; assign a new default first")
		return
	}

	if err := s.store.DeleteMachineAgent(r.Context(), machineID, agentID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("machine.agent.deleted", "machine_id", machineID, "agent_id", agentID, "via", "agent_auth")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAgentAuthPushConfig(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}

	machineID := chi.URLParam(r, "machineID")
	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	s.pushMachineConfigInternal(w, r, machine)
}

func (s *Server) handleAgentAuthListPlugins(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}

	machineID := chi.URLParam(r, "machineID")
	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	plugins, err := s.store.ListMachinePluginsWithCatalog(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if plugins == nil {
		plugins = []store.MachinePluginWithCatalog{}
	}
	writeJSON(w, http.StatusOK, plugins)
}

func (s *Server) handleAgentAuthUpdatePluginStatus(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}

	machineID := chi.URLParam(r, "machineID")
	pluginID := chi.URLParam(r, "pluginId")
	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	var req struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Status == "" {
		writeError(w, http.StatusBadRequest, "status is required")
		return
	}

	if err := s.store.UpdateMachinePluginStatus(r.Context(), machineID, pluginID, req.Status, req.Version); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("machine.plugin.status_updated", "machine_id", machineID, "plugin_id", pluginID, "status", req.Status, "via", "agent_auth")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAgentAuthRecordUsage(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}

	machineID := chi.URLParam(r, "machineID")
	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	var req struct {
		Provider     string `json:"provider"`
		Model        string `json:"model"`
		InputTokens  int    `json:"input_tokens"`
		OutputTokens int    `json:"output_tokens"`
		Source       string `json:"source"` // "platform" or "byok"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Source == "" {
		req.Source = "byok"
	}

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	// Look up source from model catalog.
	catalogEntry, _ := s.store.GetModelCatalogEntry(r.Context(), req.Model)
	if catalogEntry != nil {
		req.Source = catalogEntry.Source
	}

	record := &store.TokenUsageRecord{
		AccountID:    machine.AccountID,
		MachineID:    machineID,
		Provider:     req.Provider,
		Model:        req.Model,
		InputTokens:  req.InputTokens,
		OutputTokens: req.OutputTokens,
		Source:       req.Source,
	}
	if err := s.store.RecordTokenUsage(r.Context(), record); err != nil {
		slog.Error("usage.record_failed", "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to record usage")
		return
	}

	slog.Info("usage.recorded", "machine_id", machineID, "provider", req.Provider, "model", req.Model, "input_tokens", req.InputTokens, "output_tokens", req.OutputTokens, "source", req.Source, "via", "agent_auth")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleAgentAuthGetSecrets returns decrypted channel credentials for a machine,
// keyed by the secret IDs that ocm-secrets requests (e.g. "channel-telegram-botToken").
// Called by the metadata server on cache miss to resolve exec secret refs.
func (s *Server) handleAgentAuthGetSecrets(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}

	machineID := chi.URLParam(r, "machineID")
	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	searchSecretIDs := make(map[string]string)
	if entries, err := s.store.ListProviderCatalogByCategory(r.Context(), "search"); err != nil {
		slog.Warn("agent_auth.get_secrets.search_catalog_failed", "machine_id", machineID, "error", err)
	} else {
		for _, entry := range entries {
			secretID := entry.ID + "-key"
			if entry.ExecSecretID != nil && *entry.ExecSecretID != "" {
				secretID = *entry.ExecSecretID
			}
			searchSecretIDs[entry.ID] = secretID
		}
	}

	secrets := make(map[string]string)
	if s.secretKey != "" {
		creds, err := s.store.ListMachineCredentialsWithValues(r.Context(), machineID)
		if err != nil {
			slog.Warn("agent_auth.get_secrets.list_failed", "machine_id", machineID, "error", err)
		} else {
			for _, cred := range creds {
				val, err := crypto.Decrypt(cred.EncryptedValue, s.secretKey)
				if err != nil {
					slog.Warn("agent_auth.get_secrets.decrypt_failed", "machine_id", machineID, "provider", cred.Provider, "error", err)
					continue
				}
				if channelID, fieldName, ok := configassembly.ProviderToChannel(cred.Provider); ok {
					secrets[fmt.Sprintf("channel-%s-%s", channelID, fieldName)] = val
					continue
				}
				if secretID, ok := searchSecretIDs[cred.Provider]; ok {
					secrets[secretID] = val
				}
			}
		}
	} else {
		slog.Warn("agent_auth.get_secrets.no_encryption_key", "machine_id", machineID)
	}

	// Include OPIK_API_KEY (the machine's gateway token) for Opik plugin auth
	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err == nil && machine.GatewayToken != nil {
		secrets["OPIK_API_KEY"] = *machine.GatewayToken
	}
	if err == nil && store.NormalizeMachineKind(machine.Kind) == store.MachineKindHermes {
		if seed, seedErr := s.assembleHermesSeedConfigForMachine(r.Context(), machine); seedErr != nil {
			slog.Warn("agent_auth.get_secrets.hermes_assemble_failed", "machine_id", machineID, "error", seedErr)
		} else {
			secrets["HERMES_CONFIG_YAML"] = string(seed.ConfigYAML)
			secrets["HERMES_ENV"] = string(seed.Env)
			secrets["HERMES_AUTH_JSON"] = string(seed.AuthJSON)
			secrets["HERMES_SOUL_MD"] = string(seed.Soul)
		}
	}

	writeJSON(w, http.StatusOK, secrets)
}

// handleAgentActivity accepts activity reports from host agents.
// Only per-host tokens are accepted (fleet-wide tokens are rejected).
func (s *Server) handleAgentActivity(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}
	if hostID == 0 {
		writeError(w, http.StatusForbidden, "fleet-wide tokens cannot report activity")
		return
	}

	var req struct {
		Category  string         `json:"category"`
		Action    string         `json:"action"`
		Status    string         `json:"status"`
		MachineID string         `json:"machine_id,omitempty"`
		Summary   string         `json:"summary"`
		Detail    map[string]any `json:"detail,omitempty"`
		ErrorCode *string        `json:"error_code,omitempty"`
		ErrorMsg  *string        `json:"error_message,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Category == "" || req.Action == "" || req.Status == "" || req.Summary == "" {
		writeError(w, http.StatusBadRequest, "category, action, status, and summary are required")
		return
	}

	validCategories := map[string]bool{"agent": true, "vm": true, "gateway": true, "host": true}
	if !validCategories[req.Category] {
		writeError(w, http.StatusBadRequest, "category must be one of: agent, vm, gateway, host")
		return
	}

	var machineID *string
	if req.MachineID != "" {
		if err := s.validateMachinePlacement(r.Context(), req.MachineID, hostID); err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		machineID = &req.MachineID
	}

	s.activity.Log(r.Context(), events.LogParams{
		Category:  req.Category,
		Action:    req.Action,
		Status:    req.Status,
		ActorType: "agent",
		ActorID:   &hostID,
		HostID:    &hostID,
		MachineID: machineID,
		Summary:   req.Summary,
		Detail:    req.Detail,
		ErrorCode: req.ErrorCode,
		ErrorMsg:  req.ErrorMsg,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
