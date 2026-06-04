package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
)

var emailRe = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

func (s *Server) handleJoinWaitlist(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email  string `json:"email"`
		Source string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Email == "" || !emailRe.MatchString(req.Email) {
		writeError(w, http.StatusBadRequest, "valid email is required")
		return
	}
	if req.Source == "" {
		req.Source = "landing"
	}

	if err := s.store.AddToWaitlist(r.Context(), req.Email, req.Source); err != nil {
		// Unique constraint violation means they already signed up — treat as success
		slog.Info("waitlist.add", "email", req.Email, "source", req.Source, "err", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleUpdateWaitlistSurvey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email  string          `json:"email"`
		Survey json.RawMessage `json:"survey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Email == "" || !emailRe.MatchString(req.Email) {
		writeError(w, http.StatusBadRequest, "valid email is required")
		return
	}
	if len(req.Survey) == 0 {
		writeError(w, http.StatusBadRequest, "survey is required")
		return
	}

	if err := s.store.UpdateWaitlistSurvey(r.Context(), req.Email, req.Survey); err != nil {
		slog.Warn("waitlist.survey.update_failed", "email", req.Email, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to save survey")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
