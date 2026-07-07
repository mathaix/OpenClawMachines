package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type workspaceSummaryResponse struct {
	store.Workspace
	MachineCount     int `json:"machine_count"`
	IntegrationCount int `json:"integration_count"`
}

func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	workspaces, err := s.store.ListWorkspacesByAccount(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workspaces")
		return
	}
	if len(workspaces) == 0 {
		defaultWorkspace, err := s.store.GetOrCreateDefaultWorkspace(r.Context(), accountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create default workspace")
			return
		}
		workspaces = []store.Workspace{*defaultWorkspace}
	}
	summaries, err := s.workspaceSummaries(r, accountID, workspaces)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summaries)
}

func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	var req struct {
		Name string `json:"name"`
		Slug string `json:"slug,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if slug == "" {
		slug = workspaceSlugFromName(name)
	}
	if !isValidSlug(slug) {
		writeError(w, http.StatusBadRequest, "invalid workspace slug")
		return
	}
	workspace := &store.Workspace{AccountID: accountID, Slug: slug, Name: name}
	if err := s.store.CreateWorkspace(r.Context(), workspace); err != nil {
		if store.IsConflict(err) {
			writeError(w, http.StatusConflict, "workspace slug already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create workspace")
		return
	}
	writeJSON(w, http.StatusCreated, workspaceSummaryResponse{Workspace: *workspace})
}

func (s *Server) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	workspace, ok := s.workspaceFromRequest(w, r, accountID)
	if !ok {
		return
	}
	summaries, err := s.workspaceSummaries(r, accountID, []store.Workspace{*workspace})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summaries[0])
}

func (s *Server) handleListWorkspaceMachines(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	workspace, ok := s.workspaceFromRequest(w, r, accountID)
	if !ok {
		return
	}
	machines, err := s.store.ListMachinesByAccount(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list machines")
		return
	}
	out := make([]store.Machine, 0, len(machines))
	for _, machine := range machines {
		if machine.WorkspaceID == nil || *machine.WorkspaceID != workspace.ID {
			continue
		}
		out = append(out, machine)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) workspaceFromRequest(w http.ResponseWriter, r *http.Request, accountID int) (*store.Workspace, bool) {
	workspaceID := chi.URLParam(r, "workspaceID")
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return nil, false
	}
	workspace, err := s.store.GetWorkspace(r.Context(), accountID, workspaceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return nil, false
	}
	return workspace, true
}

func (s *Server) workspaceForIntegrationRequest(w http.ResponseWriter, r *http.Request, accountID int) (*store.Workspace, bool) {
	if chi.URLParam(r, "workspaceID") != "" {
		return s.workspaceFromRequest(w, r, accountID)
	}
	workspace, err := s.store.GetOrCreateDefaultWorkspace(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load workspace")
		return nil, false
	}
	return workspace, true
}

func (s *Server) workspaceSummaries(r *http.Request, accountID int, workspaces []store.Workspace) ([]workspaceSummaryResponse, error) {
	summaries := make([]workspaceSummaryResponse, 0, len(workspaces))
	machineCounts := map[string]int{}
	integrationCounts := map[string]int{}

	machines, err := s.store.ListMachinesByAccount(r.Context(), accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to list machines")
	}
	for _, machine := range machines {
		if machine.WorkspaceID == nil {
			continue
		}
		machineCounts[*machine.WorkspaceID]++
	}
	for _, workspace := range workspaces {
		integrations, err := s.store.ListWorkspaceIntegrations(r.Context(), workspace.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to list workspace integrations")
		}
		integrationCounts[workspace.ID] = len(integrations)
	}
	for _, workspace := range workspaces {
		summaries = append(summaries, workspaceSummaryResponse{
			Workspace:        workspace,
			MachineCount:     machineCounts[workspace.ID],
			IntegrationCount: integrationCounts[workspace.ID],
		})
	}
	return summaries, nil
}

var workspaceSlugCleanup = regexp.MustCompile(`[^a-z0-9-]+`)

func workspaceSlugFromName(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = workspaceSlugCleanup.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "workspace"
	}
	if len(slug) > 63 {
		slug = strings.Trim(slug[:63], "-")
	}
	if len(slug) < 3 {
		slug += "-workspace"
	}
	return slug
}
