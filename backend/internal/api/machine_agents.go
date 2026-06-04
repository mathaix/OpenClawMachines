package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

var validAgentID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func (s *Server) handleListMachineAgents(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	agents, err := s.store.ListMachineAgents(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

func (s *Server) handleCreateMachineAgent(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
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

	slog.Info("machine.agent.created", "machine_id", machineID, "agent_id", req.AgentID)

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.agent_created",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Created agent '%s' on '%s'", req.Name, machine.Name),
		Detail:    map[string]any{"agent_id": req.AgentID},
	})

	writeJSON(w, http.StatusCreated, agent)
}

func (s *Server) handleGetMachineAgent(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	agentID := chi.URLParam(r, "agentId")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	agent, err := s.store.GetMachineAgent(r.Context(), machineID, agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) handleUpdateMachineAgent(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	agentID := chi.URLParam(r, "agentId")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
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

	slog.Info("machine.agent.updated", "machine_id", machineID, "agent_id", agentID)

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.agent_updated",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Updated agent '%s' on '%s'", agentID, machine.Name),
		Detail:    map[string]any{"agent_id": agentID},
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteMachineAgent(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	agentID := chi.URLParam(r, "agentId")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
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

	slog.Info("machine.agent.deleted", "machine_id", machineID, "agent_id", agentID)

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.agent_deleted",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Deleted agent '%s' from '%s'", agentID, machine.Name),
		Detail:    map[string]any{"agent_id": agentID},
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// isUniqueViolation checks if a pgx error is a unique constraint violation.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	type pgErr interface{ SQLState() string }
	if pe, ok := err.(pgErr); ok {
		return pe.SQLState() == "23505"
	}
	return false
}

// validateAgentModel validates a per-agent model override against both the
// platform allowlist and available credentials (machine-linked first, then account-level fallback).
func (s *Server) validateAgentModel(ctx context.Context, machineID string, accountID int, model *string) error {
	if model == nil || *model == "" {
		return nil
	}
	catalogEntry, err := s.store.GetModelCatalogEntry(ctx, *model)
	if err != nil {
		return fmt.Errorf("failed to validate model: %w", err)
	}
	if catalogEntry == nil {
		return fmt.Errorf("model %q is not in the allowed models list", *model)
	}
	provider := strings.Split(*model, "/")[0]

	// Check machine-scoped credentials (no account-wide fallback).
	creds, err := s.store.ListMachineCredentials(ctx, machineID)
	if err != nil {
		return fmt.Errorf("failed to check credentials: %w", err)
	}
	for _, c := range creds {
		if c.Provider == provider {
			return nil
		}
	}
	return fmt.Errorf("machine has no credentials for provider %q required by model %q", provider, *model)
}
