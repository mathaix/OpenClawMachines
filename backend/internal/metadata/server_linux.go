//go:build linux

package metadata

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// Start launches the metadata HTTP server on the bridge IP.
// Endpoints: /health, /v1/machine, /v1/secrets, /v1/logs, /v1/ssh-check, /v1/ssh-principals, /v1/cf-ca-pubkey, /v1/admin/*
// Source IP is used to look up which VM is calling.
func (s *Server) Start(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", s.BindAddr, s.Port)
	slog.Info("metadata.server.starting", "addr", addr)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/machine", s.handleMachine)
	mux.HandleFunc("/v1/secrets", s.handleSecrets)
	mux.HandleFunc("/v1/ssh-check", s.handleSSHCheck) // legacy: old cf-ssh-check scripts still call this
	mux.HandleFunc("/v1/ssh-principals", s.handleSSHPrincipals)
	mux.HandleFunc("/v1/cf-ca-pubkey", s.handleCfCaPubKey)
	mux.HandleFunc("/v1/logs", s.handleLogIngestion)
	mux.HandleFunc("/v1/admin/", s.handleAdminProxy)

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		slog.Info("metadata.server.stopping")
		_ = srv.Close()
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("metadata server: %w", err)
	}
	return nil
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "agent_version": s.AgentVersion})
}

// handleMachine returns VM infrastructure fields needed by the init script.
func (s *Server) handleMachine(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.configFromRequest(w, r)
	if !ok {
		return
	}

	sourceIP := extractSourceIP(r.RemoteAddr)
	slog.Info("metadata.serve_machine",
		"machine_id", cfg.MachineID,
		"vm_ip", sourceIP,
		"vm_hostname", cfg.VmHostname,
	)

	resp := map[string]string{
		"machine_id":    cfg.MachineID,
		"machine_kind":  cfg.MachineKind,
		"machine_slug":  cfg.MachineSlug,
		"gateway_token": cfg.GatewayToken,
		"signing_key":   cfg.SigningKey,
		"tunnel_token":  cfg.TunnelToken,
		"vm_hostname":   cfg.VmHostname,
		"agent_version": s.AgentVersion,
	}
	if cfg.RuntimeSelection != nil {
		resp["runtime_kind"] = cfg.RuntimeSelection.Kind
		resp["resolved_rootfs_version"] = cfg.RuntimeSelection.ResolvedRootfsVersion
		resp["resolved_openclaw_version"] = cfg.RuntimeSelection.ResolvedOpenClawVersion
		resp["resolved_hermes_version"] = cfg.RuntimeSelection.ResolvedHermesVersion
		resp["version_source"] = cfg.RuntimeSelection.VersionSource
		resp["runtime_source"] = cfg.RuntimeSelection.RuntimeSource
		resp["openclaw_bin"] = cfg.RuntimeSelection.OpenClawBin
		resp["openclaw_bundled_plugins_dir"] = cfg.RuntimeSelection.OpenClawBundledPluginsDir
		resp["hermes_bin"] = cfg.RuntimeSelection.HermesBin
		resp["hermes_venv_dir"] = cfg.RuntimeSelection.HermesVenvDir
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("metadata.encode.failed", "path", r.URL.Path, "error", err)
	}
}

func (s *Server) handleSecrets(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.configFromRequest(w, r)
	if !ok {
		return
	}

	// Start with platform secrets (non-channel, set at RegisterMachine time).
	merged := make(map[string]string, len(cfg.Secrets))
	for k, v := range cfg.Secrets {
		merged[k] = v
	}
	if len(cfg.HermesConfigYAML) > 0 {
		merged["HERMES_CONFIG_YAML"] = string(cfg.HermesConfigYAML)
	}
	if len(cfg.HermesEnv) > 0 {
		merged["HERMES_ENV"] = string(cfg.HermesEnv)
	}
	if len(cfg.HermesAuthJSON) > 0 {
		merged["HERMES_AUTH_JSON"] = string(cfg.HermesAuthJSON)
	}
	if len(cfg.HermesSoul) > 0 {
		merged["HERMES_SOUL_MD"] = string(cfg.HermesSoul)
	}

	// Pull channel secrets from backend API via cache.
	// The DB is the single source of truth for channel tokens — no boot-time
	// ChannelKeys, no push plumbing. Cache miss triggers a backend fetch.
	if s.SecretFetcher != nil {
		sourceIP := extractSourceIP(r.RemoteAddr)
		fresh := r.URL.Query().Get("fresh") == "1" || strings.EqualFold(r.URL.Query().Get("fresh"), "true")
		cached := s.getPulledSecrets(sourceIP, cfg.MachineID, fresh)
		for k, v := range cached {
			merged[k] = v
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(merged); err != nil {
		slog.Error("metadata.encode.failed", "path", r.URL.Path, "error", err)
	}
}

// getPulledSecrets returns cached secrets, fetching from the backend if the
// cache is empty or stale. Results are stored in the MachineConfig for reuse.
func (s *Server) getPulledSecrets(vmIP, machineID string, force bool) map[string]string {
	s.mu.RLock()
	cfg, ok := s.configs[vmIP]
	s.mu.RUnlock()
	if !ok {
		return nil
	}

	// Return cached if fresh.
	if !force && cfg.SecretCache.Secrets != nil && time.Since(cfg.SecretCache.FetchedAt) < SecretCacheTTL {
		return cfg.SecretCache.Secrets
	}

	// Fetch from backend.
	secrets, err := s.SecretFetcher.FetchSecrets(machineID)
	if err != nil {
		slog.Warn("metadata.secrets.pull_failed", "machine_id", machineID, "error", err)
		if cfg.SecretCache.Secrets != nil {
			return cfg.SecretCache.Secrets
		}
		return nil
	}

	// Update cache.
	s.mu.Lock()
	cfg, ok = s.configs[vmIP]
	if ok {
		cfg.SecretCache = SecretCacheEntry{
			Secrets:   secrets,
			FetchedAt: time.Now(),
		}
		s.configs[vmIP] = cfg
	}
	s.mu.Unlock()

	slog.Info("metadata.secrets.pulled", "machine_id", machineID, "secret_count", len(secrets))
	return secrets
}

// handleSSHCheck validates a single email against authorized owners.
// Legacy endpoint — kept for backwards compatibility with old cf-ssh-check
// scripts baked into running VMs. New VMs use /v1/ssh-principals instead.
func (s *Server) handleSSHCheck(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.configFromRequest(w, r)
	if !ok {
		return
	}

	email := r.URL.Query().Get("email")
	if email == "" {
		http.Error(w, "email parameter required", http.StatusBadRequest)
		return
	}

	for _, e := range cfg.OwnerEmails {
		if strings.EqualFold(e, email) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"allowed":true}`))
			return
		}
		if localPart, _, ok := strings.Cut(e, "@"); ok && strings.EqualFold(localPart, email) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"allowed":true}`))
			return
		}
	}

	http.Error(w, "not authorized", http.StatusForbidden)
}

// handleSSHPrincipals returns all authorized SSH principals (one per line).
// Used by AuthorizedPrincipalsCommand so sshd can match them against the
// certificate's principals list. Returns both full emails and username-only
// parts since CF Access may set either format as the cert principal.
func (s *Server) handleSSHPrincipals(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.configFromRequest(w, r)
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	seen := make(map[string]bool)
	for _, e := range cfg.OwnerEmails {
		lower := strings.ToLower(e)
		if !seen[lower] {
			seen[lower] = true
			_, _ = fmt.Fprintln(w, lower)
		}
		if localPart, _, ok := strings.Cut(e, "@"); ok {
			lp := strings.ToLower(localPart)
			if !seen[lp] {
				seen[lp] = true
				_, _ = fmt.Fprintln(w, lp)
			}
		}
	}
}

func (s *Server) handleCfCaPubKey(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.configFromRequest(w, r)
	if !ok {
		return
	}

	if cfg.CfCaPubKey == "" {
		http.Error(w, "not configured", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(cfg.CfCaPubKey))
}

func (s *Server) handleLogIngestion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cfg, ok := s.configFromRequest(w, r)
	if !ok {
		return
	}

	var req struct {
		Source string `json:"source"`
		Line   string `json:"line"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if s.LogCallback != nil {
		s.LogCallback(cfg.MachineID, req.Source, req.Line)
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleAdminProxy proxies admin requests to the backend API for self-config.
func (s *Server) handleAdminProxy(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.configFromRequest(w, r)
	if !ok {
		return
	}
	if s.BackendURL == "" || s.AgentToken == "" {
		slog.Warn("metadata.admin_proxy.not_configured", "machine_id", cfg.MachineID)
		http.Error(w, "admin proxy not configured", http.StatusServiceUnavailable)
		return
	}

	// Strip /v1/admin/ prefix and build backend URL
	// /v1/admin/agents → /api/agent/machines/{machineID}/agents
	subPath := strings.TrimPrefix(r.URL.Path, "/v1/admin/")
	targetURL := fmt.Sprintf("%s/api/agent/machines/%s/%s", s.BackendURL, cfg.MachineID, subPath)
	slog.Info("metadata.admin_proxy",
		"machine_id", cfg.MachineID,
		"method", r.Method,
		"sub_path", subPath,
		"target_url", targetURL,
	)

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, "failed to create proxy request", http.StatusInternalServerError)
		return
	}
	proxyReq.Header.Set("Authorization", "Bearer "+s.AgentToken)
	proxyReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(proxyReq)
	if err != nil {
		slog.Error("metadata.admin_proxy.backend_unreachable",
			"machine_id", cfg.MachineID,
			"target_url", targetURL,
			"error", err,
		)
		http.Error(w, "backend unreachable", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	slog.Info("metadata.admin_proxy.response",
		"machine_id", cfg.MachineID,
		"sub_path", subPath,
		"status", resp.StatusCode,
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// configFromRequest extracts the source IP and nonce, then looks up the VM config.
// The nonce can be provided via the X-Metadata-Nonce header or the ?nonce= query param.
// Returns the config, an HTTP status code for errors, and ok=true on success.
func (s *Server) configFromRequest(w http.ResponseWriter, r *http.Request) (MachineConfig, bool) {
	sourceIP := extractSourceIP(r.RemoteAddr)
	slog.Debug("metadata.request", "path", r.URL.Path, "vm_ip", sourceIP)

	// Look up by IP first to distinguish "not found" from "bad nonce".
	cfg, found := s.GetConfig(sourceIP)
	if !found {
		slog.Warn("metadata.config_from_request.vm_not_found",
			"vm_ip", sourceIP,
			"path", r.URL.Path,
		)
		http.Error(w, "unknown VM", http.StatusNotFound)
		return MachineConfig{}, false
	}

	// Extract nonce from header or query param.
	nonce := r.Header.Get("X-Metadata-Nonce")
	if nonce == "" {
		nonce = r.URL.Query().Get("nonce")
	}

	// If the VM has a nonce registered, validate it.
	if cfg.Nonce != "" {
		if nonce == "" {
			slog.Warn("metadata.nonce_missing", "machine_id", cfg.MachineID, "vm_ip", sourceIP, "path", r.URL.Path)
			http.Error(w, "metadata nonce required", http.StatusForbidden)
			return MachineConfig{}, false
		}
		if subtle.ConstantTimeCompare([]byte(nonce), []byte(cfg.Nonce)) != 1 {
			slog.Warn("metadata.nonce_invalid",
				"machine_id", cfg.MachineID,
				"vm_ip", sourceIP,
				"path", r.URL.Path,
				"provided_prefix", truncate(nonce, 8),
				"registered_prefix", truncate(cfg.Nonce, 8),
			)
			http.Error(w, "invalid metadata nonce", http.StatusForbidden)
			return MachineConfig{}, false
		}
	}

	return cfg, true
}

// extractSourceIP extracts the IP from a host:port or bare IP string.
func extractSourceIP(remoteAddr string) string {
	if strings.Contains(remoteAddr, ":") {
		host, _, err := net.SplitHostPort(remoteAddr)
		if err == nil {
			return host
		}
	}
	return remoteAddr
}
