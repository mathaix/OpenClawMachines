package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// authenticateOpikToken validates a gateway_token from the Authorization header.
// Returns the machine on success, or writes a 401 error and returns nil, false.
func (s *Server) authenticateOpikToken(w http.ResponseWriter, r *http.Request) (*store.Machine, bool) {
	authHeader := r.Header.Get("Authorization")

	if authHeader == "" {
		writeError(w, http.StatusUnauthorized, "missing authorization header")
		return nil, false
	}

	// Accept both "Bearer <token>" (standard) and raw "<token>" (Opik SDK)
	token := strings.TrimPrefix(authHeader, "Bearer ")

	machine, err := s.store.GetMachineByGatewayToken(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return nil, false
	}
	return machine, true
}

// resolveProject looks up or creates an Opik project by name for the given account.
// Defaults the name to "default" if empty.
func (s *Server) resolveProject(w http.ResponseWriter, r *http.Request, accountID int, projectName string) (*store.OpikProject, bool) {
	if projectName == "" {
		projectName = "default"
	}
	proj, err := s.store.GetOrCreateOpikProject(r.Context(), accountID, projectName)
	if err != nil {
		slog.Error("opik.resolve_project_failed", "account_id", accountID, "project_name", projectName, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve project")
		return nil, false
	}
	return proj, true
}

func (s *Server) handleOpikCreateTrace(w http.ResponseWriter, r *http.Request) {
	machine, ok := s.authenticateOpikToken(w, r)
	if !ok {
		return
	}

	var trace store.OpikTrace
	if err := json.NewDecoder(r.Body).Decode(&trace); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	proj, ok := s.resolveProject(w, r, machine.AccountID, trace.ProjectName)
	if !ok {
		return
	}

	trace.ProjectID = proj.ID
	trace.AccountID = machine.AccountID
	trace.MachineID = machine.ID

	if err := s.store.UpsertOpikTrace(r.Context(), &trace); err != nil {
		if errors.Is(err, store.ErrOpikScopeConflict) {
			writeError(w, http.StatusConflict, "trace id belongs to a different machine")
			return
		}
		slog.Error("opik.create_trace_failed", "trace_id", trace.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create trace")
		return
	}

	writeJSON(w, http.StatusCreated, trace)
}

func (s *Server) handleOpikCreateTracesBatch(w http.ResponseWriter, r *http.Request) {
	machine, ok := s.authenticateOpikToken(w, r)
	if !ok {
		return
	}

	var req struct {
		Traces []store.OpikTrace `json:"traces"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Traces) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(req.Traces) > 1000 {
		writeError(w, http.StatusBadRequest, "batch size exceeds maximum of 1000")
		return
	}

	// Cache project lookups by name to avoid redundant DB calls.
	projectCache := make(map[string]*store.OpikProject)
	for i := range req.Traces {
		t := &req.Traces[i]
		name := t.ProjectName
		if name == "" {
			name = "default"
		}
		proj, cached := projectCache[name]
		if !cached {
			var ok bool
			proj, ok = s.resolveProject(w, r, machine.AccountID, name)
			if !ok {
				return
			}
			projectCache[name] = proj
		}
		t.ProjectID = proj.ID
		t.AccountID = machine.AccountID
		t.MachineID = machine.ID
	}

	if err := s.store.UpsertOpikTraces(r.Context(), req.Traces); err != nil {
		if errors.Is(err, store.ErrOpikScopeConflict) {
			writeError(w, http.StatusConflict, "trace id belongs to a different machine")
			return
		}
		slog.Error("opik.create_traces_batch_failed", "count", len(req.Traces), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create traces")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleOpikUpdateTrace(w http.ResponseWriter, r *http.Request) {
	machine, ok := s.authenticateOpikToken(w, r)
	if !ok {
		return
	}

	traceID := chi.URLParam(r, "traceID")

	var trace store.OpikTrace
	if err := json.NewDecoder(r.Body).Decode(&trace); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.store.UpdateOpikTrace(r.Context(), machine.AccountID, machine.ID, traceID, &trace); err != nil {
		if errors.Is(err, store.ErrOpikRecordNotFound) {
			writeError(w, http.StatusNotFound, "trace not found")
			return
		}
		slog.Error("opik.update_trace_failed", "trace_id", traceID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update trace")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleOpikCreateSpan(w http.ResponseWriter, r *http.Request) {
	machine, ok := s.authenticateOpikToken(w, r)
	if !ok {
		return
	}

	var span store.OpikSpan
	if err := json.NewDecoder(r.Body).Decode(&span); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if span.Type == "" {
		span.Type = "general"
	}

	proj, ok := s.resolveProject(w, r, machine.AccountID, span.ProjectName)
	if !ok {
		return
	}

	span.ProjectID = proj.ID
	span.AccountID = machine.AccountID
	span.MachineID = machine.ID

	if err := s.store.UpsertOpikSpan(r.Context(), &span); err != nil {
		if errors.Is(err, store.ErrOpikScopeConflict) {
			writeError(w, http.StatusConflict, "span id or trace id belongs to a different scope")
			return
		}
		slog.Error("opik.create_span_failed", "span_id", span.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create span")
		return
	}

	writeJSON(w, http.StatusCreated, span)
}

func (s *Server) handleOpikCreateSpansBatch(w http.ResponseWriter, r *http.Request) {
	machine, ok := s.authenticateOpikToken(w, r)
	if !ok {
		return
	}

	var req struct {
		Spans []store.OpikSpan `json:"spans"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Spans) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(req.Spans) > 1000 {
		writeError(w, http.StatusBadRequest, "batch size exceeds maximum of 1000")
		return
	}

	projectCache := make(map[string]*store.OpikProject)
	for i := range req.Spans {
		sp := &req.Spans[i]
		if sp.Type == "" {
			sp.Type = "general"
		}
		name := sp.ProjectName
		if name == "" {
			name = "default"
		}
		proj, cached := projectCache[name]
		if !cached {
			var ok bool
			proj, ok = s.resolveProject(w, r, machine.AccountID, name)
			if !ok {
				return
			}
			projectCache[name] = proj
		}
		sp.ProjectID = proj.ID
		sp.AccountID = machine.AccountID
		sp.MachineID = machine.ID
	}

	if err := s.store.UpsertOpikSpans(r.Context(), req.Spans); err != nil {
		if errors.Is(err, store.ErrOpikScopeConflict) {
			writeError(w, http.StatusConflict, "span id or trace id belongs to a different scope")
			return
		}
		slog.Error("opik.create_spans_batch_failed", "count", len(req.Spans), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create spans")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleOpikUpdateSpan(w http.ResponseWriter, r *http.Request) {
	machine, ok := s.authenticateOpikToken(w, r)
	if !ok {
		return
	}

	spanID := chi.URLParam(r, "spanID")

	var span store.OpikSpan
	if err := json.NewDecoder(r.Body).Decode(&span); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.store.UpdateOpikSpan(r.Context(), machine.AccountID, machine.ID, spanID, &span); err != nil {
		if errors.Is(err, store.ErrOpikRecordNotFound) {
			writeError(w, http.StatusNotFound, "span not found")
			return
		}
		slog.Error("opik.update_span_failed", "span_id", spanID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update span")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleOpikListProjects(w http.ResponseWriter, r *http.Request) {
	machine, ok := s.authenticateOpikToken(w, r)
	if !ok {
		return
	}

	projects, err := s.store.ListOpikProjects(r.Context(), machine.AccountID)
	if err != nil {
		slog.Error("opik.list_projects_failed", "account_id", machine.AccountID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list projects")
		return
	}
	if projects == nil {
		projects = []store.OpikProject{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"content": projects,
		"page":    1,
		"size":    len(projects),
		"total":   len(projects),
	})
}

func (s *Server) handleOpikCreateProject(w http.ResponseWriter, r *http.Request) {
	machine, ok := s.authenticateOpikToken(w, r)
	if !ok {
		return
	}

	var req struct {
		Name        string  `json:"name"`
		Description *string `json:"description,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	proj, err := s.store.GetOrCreateOpikProject(r.Context(), machine.AccountID, req.Name)
	if err != nil {
		slog.Error("opik.create_project_failed", "account_id", machine.AccountID, "name", req.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create project")
		return
	}

	writeJSON(w, http.StatusCreated, proj)
}

func (s *Server) handleOpikGetProject(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authenticateOpikToken(w, r)
	if !ok {
		return
	}

	// Placeholder for future implementation.
	writeError(w, http.StatusNotFound, "not found")
}
