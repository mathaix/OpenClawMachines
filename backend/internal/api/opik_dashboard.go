package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

func parseTraceLimit(r *http.Request) (int, bool) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return 0, false
		}
		limit = parsed
	}
	if limit < 1 || limit > 200 {
		return 0, false
	}
	return limit, true
}

func parseTraceSince(r *http.Request) (time.Time, bool) {
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, false
		}
		return parsed, true
	}
	return time.Now().UTC().Add(-7 * 24 * time.Hour), true
}

func parseOptionalTraceInt(r *http.Request, key string) (*int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil, true
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		return nil, false
	}
	return &parsed, true
}

func parseOptionalTraceInt64(r *http.Request, key string) (*int64, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil, true
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < 0 {
		return nil, false
	}
	return &parsed, true
}

func parseOptionalTraceFloat(r *http.Request, key string) (*float64, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil, true
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil || parsed < 0 {
		return nil, false
	}
	return &parsed, true
}

func normalizeTraceTag(raw string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(raw))), "-")
}

func validateTraceTag(tag string) (bool, string) {
	if len(tag) > 64 {
		return false, "tags must be 64 characters or fewer"
	}
	for _, ch := range tag {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == ':' || ch == '/' {
			continue
		}
		return false, "tags may only contain letters, numbers, spaces, dashes, underscores, dots, colons, or slashes"
	}
	return true, ""
}

func parseTraceTags(r *http.Request) ([]string, bool, string) {
	var tags []string
	seen := map[string]struct{}{}
	for _, raw := range r.URL.Query()["tag"] {
		for _, part := range strings.Split(raw, ",") {
			tag := normalizeTraceTag(part)
			if tag == "" {
				continue
			}
			if ok, message := validateTraceTag(tag); !ok {
				return nil, false, message
			}
			if _, ok := seen[tag]; ok {
				continue
			}
			seen[tag] = struct{}{}
			tags = append(tags, tag)
			if len(tags) > 20 {
				return nil, false, "a trace filter can include at most 20 tags"
			}
		}
	}
	return tags, true, ""
}

func normalizeEditableTraceTags(rawTags []string) ([]string, bool, string) {
	tags := make([]string, 0, len(rawTags))
	seen := map[string]struct{}{}
	for _, raw := range rawTags {
		tag := normalizeTraceTag(raw)
		if tag == "" {
			continue
		}
		if ok, message := validateTraceTag(tag); !ok {
			return nil, false, message
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
		if len(tags) > 20 {
			return nil, false, "a trace can have at most 20 tags"
		}
	}
	return tags, true, ""
}

func parseTraceSearchFilters(r *http.Request) (store.OpikTraceSearchFilters, bool, string) {
	since, ok := parseTraceSince(r)
	if !ok {
		return store.OpikTraceSearchFilters{}, false, "invalid since parameter: use RFC3339 format"
	}
	limit, ok := parseTraceLimit(r)
	if !ok {
		return store.OpikTraceSearchFilters{}, false, "invalid limit parameter: use an integer from 1 to 200"
	}
	minTokens, ok := parseOptionalTraceInt(r, "min_tokens")
	if !ok {
		return store.OpikTraceSearchFilters{}, false, "invalid min_tokens parameter: use a non-negative integer"
	}
	minCost, ok := parseOptionalTraceFloat(r, "min_cost")
	if !ok {
		return store.OpikTraceSearchFilters{}, false, "invalid min_cost parameter: use a non-negative number"
	}
	minDurationMS, ok := parseOptionalTraceInt64(r, "min_duration_ms")
	if !ok {
		return store.OpikTraceSearchFilters{}, false, "invalid min_duration_ms parameter: use a non-negative integer"
	}
	maxFeedback, ok := parseOptionalTraceFloat(r, "max_feedback_score")
	if !ok || (maxFeedback != nil && *maxFeedback > 1) {
		return store.OpikTraceSearchFilters{}, false, "invalid max_feedback_score parameter: use a number from 0 to 1"
	}

	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && status != "ok" && status != "error" {
		return store.OpikTraceSearchFilters{}, false, "invalid status parameter: use ok or error"
	}
	feedback := strings.TrimSpace(r.URL.Query().Get("feedback"))
	if feedback != "" && feedback != "reviewed" && feedback != "unreviewed" && feedback != "low" {
		return store.OpikTraceSearchFilters{}, false, "invalid feedback parameter: use reviewed, unreviewed, or low"
	}
	tags, ok, message := parseTraceTags(r)
	if !ok {
		return store.OpikTraceSearchFilters{}, false, message
	}

	return store.OpikTraceSearchFilters{
		Since:         since,
		Limit:         limit,
		MachineID:     strings.TrimSpace(r.URL.Query().Get("machine_id")),
		Project:       strings.TrimSpace(r.URL.Query().Get("project")),
		Status:        status,
		Query:         strings.TrimSpace(r.URL.Query().Get("q")),
		ThreadID:      strings.TrimSpace(r.URL.Query().Get("thread_id")),
		Tags:          tags,
		Feedback:      feedback,
		MaxFeedback:   maxFeedback,
		MinTokens:     minTokens,
		MinCost:       minCost,
		MinDurationMS: minDurationMS,
	}, true, ""
}

func (s *Server) verifyMachineAccount(w http.ResponseWriter, r *http.Request, accountID int, machineID string) bool {
	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return false
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return false
	}
	return true
}

func (s *Server) handleListAccountTraces(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	filters, ok, message := parseTraceSearchFilters(r)
	if !ok {
		writeError(w, http.StatusBadRequest, message)
		return
	}

	traces, err := s.store.ListOpikTraces(r.Context(), accountID, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list traces")
		return
	}
	if traces == nil {
		traces = []store.OpikTraceListItem{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"account_id": accountID,
		"since":      filters.Since.Format(time.RFC3339),
		"traces":     traces,
	})
}

func (s *Server) handleGetAccountTrace(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	traceID := chi.URLParam(r, "traceID")

	detail, err := s.store.GetOpikTraceDetailByAccount(r.Context(), accountID, traceID)
	if err != nil {
		if errors.Is(err, store.ErrOpikRecordNotFound) {
			writeError(w, http.StatusNotFound, "trace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get trace")
		return
	}
	if detail.Spans == nil {
		detail.Spans = []store.OpikTraceSpan{}
	}
	if detail.Feedback == nil {
		detail.Feedback = []store.OpikFeedbackScore{}
	}

	writeJSON(w, http.StatusOK, detail)
}

type updateTraceTagsRequest struct {
	Tags []string `json:"tags"`
}

func (s *Server) handleUpdateTraceTags(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	traceID := chi.URLParam(r, "traceID")

	var req updateTraceTagsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	tags, ok, message := normalizeEditableTraceTags(req.Tags)
	if !ok {
		writeError(w, http.StatusBadRequest, message)
		return
	}

	err := s.store.UpdateOpikTraceTags(r.Context(), accountID, traceID, tags)
	if err != nil {
		if errors.Is(err, store.ErrOpikRecordNotFound) {
			writeError(w, http.StatusNotFound, "trace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update trace tags")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type createOpikFeedbackRequest struct {
	SpanID *string  `json:"span_id"`
	Name   string   `json:"name"`
	Value  *float64 `json:"value"`
	Reason *string  `json:"reason"`
}

func (s *Server) handleListTraceFeedback(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	traceID := chi.URLParam(r, "traceID")

	scores, err := s.store.ListOpikFeedbackScores(r.Context(), accountID, traceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list feedback")
		return
	}
	if scores == nil {
		scores = []store.OpikFeedbackScore{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"trace_id": traceID,
		"feedback": scores,
	})
}

func (s *Server) handleCreateTraceFeedback(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	traceID := chi.URLParam(r, "traceID")

	var req createOpikFeedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 80 {
		writeError(w, http.StatusBadRequest, "name is required and must be 80 characters or fewer")
		return
	}
	if req.Value == nil || *req.Value < 0 || *req.Value > 1 {
		writeError(w, http.StatusBadRequest, "value is required and must be between 0 and 1")
		return
	}

	var spanID *string
	if req.SpanID != nil {
		trimmed := strings.TrimSpace(*req.SpanID)
		if trimmed != "" {
			spanID = &trimmed
		}
	}
	var reason *string
	if req.Reason != nil {
		trimmed := strings.TrimSpace(*req.Reason)
		if trimmed != "" {
			if len(trimmed) > 2000 {
				writeError(w, http.StatusBadRequest, "reason must be 2000 characters or fewer")
				return
			}
			reason = &trimmed
		}
	}

	score, err := s.store.CreateOpikFeedbackScore(r.Context(), &store.OpikFeedbackScore{
		AccountID: accountID,
		TraceID:   traceID,
		SpanID:    spanID,
		Name:      name,
		Value:     *req.Value,
		Reason:    reason,
		Source:    "human",
		CreatedBy: userIDFromContext(r.Context()),
	})
	if err != nil {
		if errors.Is(err, store.ErrOpikRecordNotFound) {
			writeError(w, http.StatusNotFound, "trace or span not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create feedback")
		return
	}

	writeJSON(w, http.StatusCreated, score)
}

func (s *Server) handleListMachineTraces(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")

	if !s.verifyMachineAccount(w, r, accountID, machineID) {
		return
	}

	since, ok := parseTraceSince(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid since parameter: use RFC3339 format")
		return
	}

	limit, ok := parseTraceLimit(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid limit parameter: use an integer from 1 to 200")
		return
	}

	traces, err := s.store.ListOpikTracesByMachine(r.Context(), accountID, machineID, since, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list traces")
		return
	}
	if traces == nil {
		traces = []store.OpikTraceListItem{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"machine_id": machineID,
		"since":      since.Format(time.RFC3339),
		"traces":     traces,
	})
}

func (s *Server) handleGetMachineTrace(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")
	traceID := chi.URLParam(r, "traceID")

	if !s.verifyMachineAccount(w, r, accountID, machineID) {
		return
	}

	detail, err := s.store.GetOpikTraceDetail(r.Context(), accountID, machineID, traceID)
	if err != nil {
		if errors.Is(err, store.ErrOpikRecordNotFound) {
			writeError(w, http.StatusNotFound, "trace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get trace")
		return
	}
	if detail.Spans == nil {
		detail.Spans = []store.OpikTraceSpan{}
	}
	if detail.Feedback == nil {
		detail.Feedback = []store.OpikFeedbackScore{}
	}

	writeJSON(w, http.StatusOK, detail)
}
