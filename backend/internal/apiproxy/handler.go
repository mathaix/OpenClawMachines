package apiproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/metadata"
)

// handleProxy is the core request handler for the API proxy.
// It authenticates requests via the metadata server, rewrites them to the upstream
// provider, and tracks usage/cost.
func (p *Proxy) handleProxy(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	// 1. Extract provider name from first path segment
	// Path format: /{provider}/rest/of/path
	path := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "missing provider in path", http.StatusNotFound)
		return
	}
	providerName := parts[0]

	// 2. Look up provider (may be built-in or custom)
	p.mu.RLock()
	provider, ok := p.providers[providerName]
	p.mu.RUnlock()

	// 3. Extract source IP from r.RemoteAddr (strip port)
	srcIP := extractIP(r.RemoteAddr)

	// 4. Extract auth token — use provider's method if known, otherwise use generic x-api-key header
	var token string
	if ok {
		token = provider.ExtractToken(r)
	} else {
		token = r.Header.Get("x-api-key")
	}

	// 5. Look up machine via metadata server using IP + nonce
	cfg, found := p.metaSrv.GetConfigWithNonce(srcIP, token)
	if !found {
		// Truncate token to prefix for safe logging.
		tokenPrefix := token
		if len(tokenPrefix) > 8 {
			tokenPrefix = tokenPrefix[:8] + "..."
		}
		slog.Warn("apiproxy.auth_failed",
			"provider", providerName,
			"source_ip", srcIP,
			"token_prefix", tokenPrefix,
			"token_len", len(token),
			"method", r.Method,
			"path", r.URL.Path,
		)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// 5b. If provider not found in built-in set, check custom providers from machine config.
	// Custom providers are NOT cached globally to prevent cross-account leaks — each
	// account may define different upstreams for the same provider name.
	if !ok {
		for _, cp := range cfg.CustomProviders {
			if cp.Name == providerName {
				provider = CustomProviderToProxy(cp)
				ok = true
				break
			}
		}
		if !ok {
			http.Error(w, fmt.Sprintf("unknown provider: %s", providerName), http.StatusNotFound)
			return
		}
	}

	// 6. Check real key exists for this provider
	var realKey, credType string
	var keyExpiresAt *time.Time
	entry, isLLM := cfg.LLMKeys[provider.Name]
	if isLLM {
		realKey = entry.Value
		credType = entry.CredentialType
		keyExpiresAt = entry.ExpiresAt
	}
	hasKey := realKey != ""
	if !provider.KeyOptional && !hasKey {
		http.Error(w, fmt.Sprintf("no API key configured for provider %s", provider.Name), http.StatusForbidden)
		return
	}

	// 7. Pre-flight OAuth refresh: if token expires within 10 minutes, refresh proactively
	if credType == "oauth" && keyExpiresAt != nil && time.Until(*keyExpiresAt) < 10*time.Minute {
		newToken, newExpiry, _, refreshErr := p.refreshCredential(cfg.MachineID, provider.Name)
		if refreshErr != nil {
			slog.Warn("apiproxy.oauth_preflight_refresh_failed",
				"machine_id", cfg.MachineID, "provider", provider.Name, "error", refreshErr)
			// Continue with existing token — may still work if clock skew
		} else {
			realKey = newToken
			keyExpiresAt = newExpiry
			cfg.LLMKeys[provider.Name] = metadata.CredentialEntry{
				Value: newToken, CredentialType: "oauth", ExpiresAt: newExpiry,
			}
			p.metaSrv.UpdateMachineLLMKeys(srcIP, cfg.LLMKeys)
			slog.Info("apiproxy.oauth_preflight_refreshed",
				"machine_id", cfg.MachineID, "provider", provider.Name)
		}
	} else if keyExpiresAt != nil && time.Until(*keyExpiresAt) < time.Hour {
		slog.Warn("apiproxy.key_expiring_soon",
			"machine_id", cfg.MachineID,
			"provider", provider.Name,
			"credential_type", credType,
			"expires_at", keyExpiresAt.Format(time.RFC3339),
			"expires_in_min", int(time.Until(*keyExpiresAt).Minutes()),
		)
	}

	// 8. Build upstream URL with dynamic host resolution
	remainingPath := ""
	if len(parts) > 1 {
		remainingPath = "/" + parts[1]
	}
	upstreamHost := provider.UpstreamHost
	if provider.ResolveHost != nil {
		upstreamHost = provider.ResolveHost(remainingPath)
	}
	upstreamURL := fmt.Sprintf("%s://%s%s%s", provider.Scheme, upstreamHost, provider.UpstreamPathPrefix, remainingPath)

	// Copy query string
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	// 8b. Validate upstream host against provider's allowed hosts
	if !isHostAllowed(upstreamHost, provider.AllowedHosts) {
		slog.Warn("apiproxy.host_not_allowed",
			"machine_id", cfg.MachineID,
			"provider", provider.Name,
			"upstream_host", upstreamHost,
			"allowed_hosts", provider.AllowedHosts,
		)
		http.Error(w, fmt.Sprintf("host %q is not allowed for provider %s", upstreamHost, provider.Name), http.StatusForbidden)
		return
	}

	// 8c. Optionally mutate request body (e.g. inject identity for subscription tokens)
	var reqBody io.Reader = r.Body
	if provider.MutateBody != nil && isLLM {
		rawBody, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			slog.Error("apiproxy.read_body_failed", "machine_id", cfg.MachineID, "provider", provider.Name, "error", readErr)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		mutated, mutErr := provider.MutateBody(rawBody, credType)
		if mutErr != nil {
			slog.Warn("apiproxy.mutate_body_failed", "machine_id", cfg.MachineID, "provider", provider.Name, "error", mutErr)
			mutated = rawBody // fall back to original
		}
		reqBody = bytes.NewReader(mutated)
	}

	// 9. Create upstream request
	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, reqBody)
	if err != nil {
		slog.Error("apiproxy.create_request_failed", "machine_id", cfg.MachineID, "account_id", cfg.AccountID, "provider", provider.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// 10. Copy headers from client (skip Host, Connection, auth-related, encoding headers)
	for key, values := range r.Header {
		lk := strings.ToLower(key)
		switch lk {
		case "host", "connection":
			continue
		case "authorization", "x-api-key":
			continue // We'll inject the real key below
		case "accept-encoding":
			continue // Let Go's transport handle compression transparently
		}
		for _, v := range values {
			upstreamReq.Header.Add(key, v)
		}
	}

	// 11. Inject real API key (skip when no key, e.g. CDN passthrough)
	if realKey != "" {
		keyPrefix := realKey
		if len(keyPrefix) > 12 {
			keyPrefix = keyPrefix[:12] + "..."
		}
		slog.Info("apiproxy.inject_key", "machine_id", cfg.MachineID, "provider", provider.Name, "credential_type", credType, "key_prefix", keyPrefix, "is_llm", isLLM)
		provider.InjectKey(upstreamReq, realKey, credType)
	}

	// 12. Send request upstream
	resp, err := p.client.Do(upstreamReq)
	if err != nil {
		slog.Warn("apiproxy.upstream_failed",
			"machine_id", cfg.MachineID,
			"account_id", cfg.AccountID,
			"provider", provider.Name,
			"method", r.Method,
			"path", r.URL.Path,
			"source_ip", srcIP,
			"error", err.Error(),
			"duration_ms", time.Since(startTime).Milliseconds(),
		)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}

	// Post-flight OAuth retry: if upstream returned 401 and credential is OAuth, refresh and retry once
	if resp.StatusCode == http.StatusUnauthorized && credType == "oauth" {
		_ = resp.Body.Close()
		newToken, newExpiry, permanent, refreshErr := p.refreshCredential(cfg.MachineID, provider.Name)
		if refreshErr != nil {
			slog.Warn("apiproxy.oauth_retry_refresh_failed",
				"machine_id", cfg.MachineID, "provider", provider.Name,
				"permanent", permanent, "error", refreshErr)
			errCode := "oauth_refresh_failed"
			if permanent {
				errCode = "oauth_reauth_required"
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":    errCode,
				"provider": provider.Name,
				"detail":   refreshErr.Error(),
			})
			return
		}
		// Update metadata and retry once
		cfg.LLMKeys[provider.Name] = metadata.CredentialEntry{
			Value: newToken, CredentialType: "oauth", ExpiresAt: newExpiry,
		}
		p.metaSrv.UpdateMachineLLMKeys(srcIP, cfg.LLMKeys)

		// Rebuild upstream request for retry
		retryReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
		if err != nil {
			http.Error(w, "internal error creating retry request", http.StatusInternalServerError)
			return
		}
		for key, values := range upstreamReq.Header {
			for _, v := range values {
				retryReq.Header.Add(key, v)
			}
		}
		provider.InjectKey(retryReq, newToken, credType)

		resp, err = p.client.Do(retryReq)
		if err != nil {
			http.Error(w, "upstream request failed after token refresh", http.StatusBadGateway)
			return
		}
		slog.Info("apiproxy.oauth_retry_succeeded",
			"machine_id", cfg.MachineID, "provider", provider.Name, "status", resp.StatusCode)
	}

	logLevel := slog.LevelInfo
	logAttrs := []any{
		"machine_id", cfg.MachineID,
		"provider", provider.Name,
		"credential_type", credType,
		"method", r.Method,
		"path", r.URL.Path,
		"upstream_url", upstreamURL,
		"status", resp.StatusCode,
		"duration_ms", time.Since(startTime).Milliseconds(),
	}
	if resp.StatusCode >= 400 {
		logLevel = slog.LevelWarn
	}
	slog.Log(r.Context(), logLevel, "apiproxy.request", logAttrs...)

	// 13. Copy response headers to client (skip encoding headers since transport decompresses)
	for key, values := range resp.Header {
		lk := strings.ToLower(key)
		if lk == "content-encoding" || lk == "content-length" {
			continue
		}
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}

	// 14. Check Content-Type for SSE streaming — pass startTime and credType for unified debug logging
	contentType := resp.Header.Get("Content-Type")
	callCtx := &proxyCallContext{
		startTime: startTime,
		machineID: cfg.MachineID,
		accountID: cfg.AccountID,
		provider:  provider,
		credType:  credType,
		status:    resp.StatusCode,
		method:    r.Method,
		path:      r.URL.Path,
	}
	if strings.Contains(contentType, "text/event-stream") {
		w.WriteHeader(resp.StatusCode)
		p.streamSSE(w, resp.Body, callCtx)
	} else {
		p.forwardJSON(w, resp, callCtx)
	}
}

// proxyCallContext carries per-request context from handleProxy to response handlers
// so they can emit a single unified debug log with full details (tokens, cost, duration).
type proxyCallContext struct {
	startTime time.Time
	machineID string
	accountID int
	provider  *Provider
	credType  string
	status    int
	method    string
	path      string
}

// extractIP extracts the IP from a "host:port" or bare IP string.
func extractIP(remoteAddr string) string {
	if strings.Contains(remoteAddr, ":") {
		host, _, err := net.SplitHostPort(remoteAddr)
		if err == nil {
			return host
		}
	}
	return remoteAddr
}

// isHostAllowed checks whether host is in the allowed list.
func isHostAllowed(host string, allowed []string) bool {
	for _, h := range allowed {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// handleHealth is a simple health check endpoint.
func (p *Proxy) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, err := io.WriteString(w, `{"status":"ok"}`)
	if err != nil {
		slog.Warn("apiproxy.health_write_failed", "error", err)
	}
}
