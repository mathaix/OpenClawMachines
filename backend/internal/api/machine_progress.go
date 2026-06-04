package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleMachineProgress(w http.ResponseWriter, r *http.Request) {
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

	if machine.HostID == nil {
		writeError(w, http.StatusBadRequest, "machine is not assigned to a host")
		return
	}

	host, err := s.store.GetHost(r.Context(), *machine.HostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "host not found")
		return
	}

	// Determine host IP (prefer external — Cloud Run has no VPC connector)
	var hostIP string
	if host.ExternalIP != nil {
		hostIP = *host.ExternalIP
	} else if host.InternalIP != nil {
		hostIP = *host.InternalIP
	} else {
		writeError(w, http.StatusServiceUnavailable, "host has no reachable IP")
		return
	}

	// Connect to the agent's SSE progress endpoint
	agentURL := fmt.Sprintf("http://%s:9091/progress?machine_id=%s", hostIP, machine.ID)
	agentReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, agentURL, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create agent request")
		return
	}

	if machine.ProxyToken != nil {
		agentReq.Header.Set("X-Proxy-Token", *machine.ProxyToken)
	}

	resp, err := http.DefaultClient.Do(agentReq)
	if err != nil {
		slog.Error("proxy.progress.agent.connect.failed", "machine_id", id, "error", err)
		writeError(w, http.StatusBadGateway, "failed to connect to agent")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Forward SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Pipe the SSE stream from agent to client
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			flusher.Flush()
		}
		if readErr != nil {
			if readErr != io.EOF {
				slog.Error("proxy.progress.read.error", "machine_id", id, "error", readErr)
			}
			return
		}
	}
}
