package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// sanitizeFilePath cleans a file path and rejects traversal attempts.
// Returns the cleaned path or an error message if the path is invalid.
func sanitizeFilePath(filePath string) (string, bool) {
	cleaned := path.Clean(filePath)
	if strings.Contains(cleaned, "..") {
		return "", false
	}
	return cleaned, true
}

// handleListFiles proxies a directory listing request to the agent gateway.
// GET /api/accounts/{accountId}/machines/{id}/files?path=/
func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
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

	hostIP, err := s.resolveHostIP(r.Context(), machine)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		filePath = "/"
	}
	filePath, ok := sanitizeFilePath(filePath)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid file path: directory traversal not allowed")
		return
	}

	agentURL := fmt.Sprintf("http://%s:9091/proxy/%s/gateway/api/files?path=%s",
		hostIP, machine.ID, url.QueryEscape(filePath))

	s.proxyHTTPGet(w, r, agentURL, machine.ProxyToken, "list files")
}

// handleDownloadFile proxies a single file download from the agent gateway.
// GET /api/accounts/{accountId}/machines/{id}/files/download?path=...
func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
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

	hostIP, err := s.resolveHostIP(r.Context(), machine)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	filePath, ok := sanitizeFilePath(filePath)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid file path: directory traversal not allowed")
		return
	}

	agentURL := fmt.Sprintf("http://%s:9091/proxy/%s/gateway/api/files/download?path=%s",
		hostIP, machine.ID, url.QueryEscape(filePath))

	s.proxyHTTPGet(w, r, agentURL, machine.ProxyToken, "download file")
}

// handleDownloadZip proxies a directory zip download from the agent gateway.
// GET /api/accounts/{accountId}/machines/{id}/files/download-zip?path=...
func (s *Server) handleDownloadZip(w http.ResponseWriter, r *http.Request) {
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

	hostIP, err := s.resolveHostIP(r.Context(), machine)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		filePath = "/"
	}
	filePath, ok := sanitizeFilePath(filePath)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid file path: directory traversal not allowed")
		return
	}

	agentURL := fmt.Sprintf("http://%s:9091/proxy/%s/gateway/api/files/download-zip?path=%s",
		hostIP, machine.ID, url.QueryEscape(filePath))

	s.proxyHTTPGet(w, r, agentURL, machine.ProxyToken, "download zip")
}

// resolveHostIP looks up the machine's host and returns a reachable IP.
func (s *Server) resolveHostIP(ctx context.Context, machine *store.Machine) (string, error) {
	if machine.HostID == nil {
		return "", fmt.Errorf("machine is not assigned to a host")
	}

	host, err := s.store.GetHost(ctx, *machine.HostID)
	if err != nil {
		return "", fmt.Errorf("host not found")
	}

	if host.ExternalIP != nil {
		return *host.ExternalIP, nil
	}
	if host.InternalIP != nil {
		return *host.InternalIP, nil
	}
	return "", fmt.Errorf("host has no reachable IP")
}

// proxyHTTPGet makes a GET request to the agent and pipes the response back.
func (s *Server) proxyHTTPGet(w http.ResponseWriter, r *http.Request, agentURL string, proxyToken *string, label string) {
	agentReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, agentURL, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create agent request")
		return
	}
	if proxyToken != nil {
		agentReq.Header.Set("X-Proxy-Token", *proxyToken)
	}

	resp, err := http.DefaultClient.Do(agentReq)
	if err != nil {
		slog.Error("proxy.file.agent.connect.failed", "label", label, "error", err)
		writeError(w, http.StatusBadGateway, "failed to connect to agent")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Forward headers
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
