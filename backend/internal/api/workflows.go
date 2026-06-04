package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request) {
	if s.workflows == nil {
		writeError(w, http.StatusServiceUnavailable, "workflow service not configured")
		return
	}

	run, err := s.workflows.GetRun(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workflow not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if !s.canReadWorkflow(r, run.AccountID) {
		writeError(w, http.StatusForbidden, "access denied to this workflow")
		return
	}

	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleListWorkflowEvents(w http.ResponseWriter, r *http.Request) {
	if s.workflows == nil {
		writeError(w, http.StatusServiceUnavailable, "workflow service not configured")
		return
	}

	run, err := s.workflows.GetRun(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workflow not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if !s.canReadWorkflow(r, run.AccountID) {
		writeError(w, http.StatusForbidden, "access denied to this workflow")
		return
	}

	events, err := s.workflows.ListEvents(r.Context(), run.ID, workflowLimitFromRequest(r, 200))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) handleListMachineWorkflows(w http.ResponseWriter, r *http.Request) {
	if s.workflows == nil {
		writeError(w, http.StatusServiceUnavailable, "workflow service not configured")
		return
	}

	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")
	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	runs, err := s.workflows.ListRunsByScope(r.Context(), "machine", machineID, workflowLimitFromRequest(r, 50))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

// handleAdminListWorkflows lists all workflows globally (admin only).
// Supports query params: kind, status, limit.
func (s *Server) handleAdminListWorkflows(w http.ResponseWriter, r *http.Request) {
	if s.workflows == nil {
		writeError(w, http.StatusServiceUnavailable, "workflow service not configured")
		return
	}

	kind := r.URL.Query().Get("kind")
	status := r.URL.Query().Get("status")
	limit := workflowLimitFromRequest(r, 50)

	runs, err := s.store.ListAllWorkflowRuns(r.Context(), kind, status, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Enrich with machine names for scope_type=machine
	type enrichedRun struct {
		*store.WorkflowRun
		MachineName string `json:"machine_name,omitempty"`
		MachineSlug string `json:"machine_slug,omitempty"`
		HostID      *int   `json:"host_id,omitempty"`
	}

	result := make([]enrichedRun, len(runs))
	for i := range runs {
		result[i].WorkflowRun = &runs[i]
		if runs[i].ScopeType == "machine" {
			if m, err := s.store.GetMachine(r.Context(), runs[i].ScopeID); err == nil {
				result[i].MachineName = m.Name
				result[i].MachineSlug = m.Slug
				result[i].HostID = m.HostID
			}
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// handleAdminListWorkflowEvents lists events for any workflow (admin only, no account check).
func (s *Server) handleAdminListWorkflowEvents(w http.ResponseWriter, r *http.Request) {
	if s.workflows == nil {
		writeError(w, http.StatusServiceUnavailable, "workflow service not configured")
		return
	}

	workflowID := chi.URLParam(r, "workflowId")
	events, err := s.workflows.ListEvents(r.Context(), workflowID, workflowLimitFromRequest(r, 200))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func workflowLimitFromRequest(r *http.Request, fallback int) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return fallback
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return fallback
	}
	const maxWorkflowLimit = 1000
	if limit > maxWorkflowLimit {
		limit = maxWorkflowLimit
	}
	return limit
}

func (s *Server) canReadWorkflow(r *http.Request, accountID *int) bool {
	claims := auth.UserFromContext(r.Context())
	if claims == nil {
		return false
	}
	if claims.Email == "mathewma@gmail.com" {
		return true
	}
	if accountID == nil {
		return false
	}
	_, err := s.store.GetAccountMember(r.Context(), *accountID, claims.UserID)
	return err == nil
}
