package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

func (s *Server) handleListMachineActivity(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	filter := s.parseActivityFilter(r)
	activities, err := s.store.ListActivitiesByMachine(r.Context(), id, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list activity")
		return
	}

	type response struct {
		Activities     []store.ActivityLog `json:"activities"`
		NextCursorTime *time.Time          `json:"next_cursor_time,omitempty"`
		NextCursorID   *int64              `json:"next_cursor_id,omitempty"`
	}
	resp := response{Activities: activities}
	if len(activities) > 0 {
		last := activities[len(activities)-1]
		resp.NextCursorTime = &last.StartedAt
		resp.NextCursorID = &last.ID
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleListAccountActivity(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())

	filter := s.parseActivityFilter(r)
	activities, err := s.store.ListActivities(r.Context(), accountID, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list activity")
		return
	}

	type response struct {
		Activities     []store.ActivityLog `json:"activities"`
		NextCursorTime *time.Time          `json:"next_cursor_time,omitempty"`
		NextCursorID   *int64              `json:"next_cursor_id,omitempty"`
	}
	resp := response{Activities: activities}
	if len(activities) > 0 {
		last := activities[len(activities)-1]
		resp.NextCursorTime = &last.StartedAt
		resp.NextCursorID = &last.ID
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAdminListEvents returns activity events with full admin-level filtering.
func (s *Server) handleAdminListEvents(w http.ResponseWriter, r *http.Request) {
	filter := s.parseActivityFilter(r)

	// Admin-specific filter params: account_id, machine_id, host_id, actor_id
	q := r.URL.Query()
	if v := q.Get("account_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.AccountID = &n
		}
	}
	if v := q.Get("machine_id"); v != "" {
		filter.MachineID = &v
	}
	if v := q.Get("host_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.HostID = &n
		}
	}
	if v := q.Get("actor_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.ActorID = &n
		}
	}

	activities, err := s.store.ListActivitiesAdmin(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list events")
		return
	}

	type response struct {
		Events         []store.ActivityLog `json:"events"`
		NextCursorTime *time.Time          `json:"next_cursor_time,omitempty"`
		NextCursorID   *int64              `json:"next_cursor_id,omitempty"`
	}
	resp := response{Events: activities}
	if len(activities) > 0 {
		last := activities[len(activities)-1]
		resp.NextCursorTime = &last.StartedAt
		resp.NextCursorID = &last.ID
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseActivityFilter extracts ActivityFilter from query parameters.
func (s *Server) parseActivityFilter(r *http.Request) store.ActivityFilter {
	q := r.URL.Query()
	filter := store.ActivityFilter{
		Category:  q.Get("category"),
		Action:    q.Get("action"),
		Status:    q.Get("status"),
		ActorType: q.Get("actor_type"),
	}

	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			filter.Limit = n
		}
	}
	if filter.Limit == 0 || filter.Limit > 500 {
		filter.Limit = 50
	}

	if v := q.Get("cursor_time"); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			filter.CursorTime = &t
		}
	}
	if v := q.Get("cursor_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			filter.CursorID = &n
		}
	}

	return filter
}
