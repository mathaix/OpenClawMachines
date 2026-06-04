package api

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleMachineExec(w http.ResponseWriter, r *http.Request) {
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

	if machine.Status != "running" {
		writeError(w, http.StatusBadRequest, "machine is not running")
		return
	}

	if machine.HostID == nil {
		writeError(w, http.StatusBadRequest, "machine is not assigned to a host")
		return
	}

	host, err := s.store.GetHost(r.Context(), *machine.HostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "host not found")
		return
	}

	if s.agentClient == nil {
		writeError(w, http.StatusServiceUnavailable, "agent client not configured")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	agentURL := fmt.Sprintf("%s/proxy/%s/exec", s.agentClient.ProxyURL(host), machine.ID)

	slog.Info("machine.exec", "machine_id", id, "account_id", accountID)

	client := &http.Client{Timeout: 40 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, agentURL, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if machine.ProxyToken != nil {
		req.Header.Set("X-Proxy-Token", *machine.ProxyToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to reach agent")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
