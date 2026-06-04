package apiproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/metadata"
)

// Proxy is the LLM API proxy that sits on the bridge network.
// MicroVMs send requests to this proxy using their nonce as the API key.
// The proxy looks up the real API key from the metadata server, forwards
// the request to the upstream provider, and tracks usage/cost.
type Proxy struct {
	metaSrv    *metadata.Server
	bindAddr   string
	port       int
	httpServer *http.Server
	usage      *UsageTracker
	mu         sync.RWMutex
	providers  map[string]*Provider
	client     *http.Client
	refreshURL string // backend endpoint for OAuth token refresh (e.g., "http://backend:8080/api/internal/refresh-credential")
}

// New creates a new API proxy bound to the given address and port.
func New(metaSrv *metadata.Server, bindAddr string, port int) *Proxy {
	return &Proxy{
		metaSrv:   metaSrv,
		bindAddr:  bindAddr,
		port:      port,
		usage:     NewUsageTracker(),
		providers: initProviders(),
		client:    &http.Client{
			// No timeout — upstream streaming responses can be long-lived
		},
	}
}

// Start starts the proxy HTTP server. It blocks until the server is shut down
// or the context is cancelled.
func (p *Proxy) Start(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", p.bindAddr, p.port)
	log.Printf("apiproxy: starting on %s", addr)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", p.handleHealth)
	// Catch-all handler for proxy requests
	mux.HandleFunc("/", p.handleProxy)

	p.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		log.Println("apiproxy: context cancelled, shutting down")
		_ = p.httpServer.Close()
	}()

	if err := p.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("apiproxy: %w", err)
	}
	return nil
}

// Shutdown gracefully shuts down the proxy server.
func (p *Proxy) Shutdown(ctx context.Context) error {
	if p.httpServer != nil {
		return p.httpServer.Shutdown(ctx)
	}
	return nil
}

// GetUsage returns all usage records grouped by machine ID.
func (p *Proxy) GetUsage() map[string][]UsageRecord {
	return p.usage.GetAllRecords()
}

// GetMachineUsage returns usage records for a specific machine.
func (p *Proxy) GetMachineUsage(machineID string) []UsageRecord {
	return p.usage.GetRecords(machineID)
}

// FlushUsage returns and removes all usage records for a specific machine.
func (p *Proxy) FlushUsage(machineID string) []UsageRecord {
	return p.usage.FlushUsage(machineID)
}

// AddProvider registers a provider in the proxy's provider map.
// This is used to dynamically add custom providers at runtime.
func (p *Proxy) AddProvider(provider *Provider) {
	p.mu.Lock()
	p.providers[provider.Name] = provider
	p.mu.Unlock()
}

// RegisterCustomProviders creates Provider structs from custom provider configs
// and adds them to the proxy's provider map. Existing built-in providers are not overwritten.
func (p *Proxy) RegisterCustomProviders(configs []metadata.CustomProviderConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, cfg := range configs {
		// Never overwrite built-in providers
		if _, exists := p.providers[cfg.Name]; exists {
			continue
		}
		p.providers[cfg.Name] = CustomProviderToProxy(cfg)
	}
}

// CustomProviderToProxy converts a CustomProviderConfig into a proxy Provider.
func CustomProviderToProxy(cfg metadata.CustomProviderConfig) *Provider {
	scheme := cfg.Scheme
	if scheme == "" {
		scheme = "https"
	}

	allowedHosts := cfg.AllowedHosts
	if len(allowedHosts) == 0 {
		allowedHosts = []string{cfg.UpstreamHost}
	} else {
		// Ensure upstream host is always in allowed list
		found := false
		for _, h := range allowedHosts {
			if h == cfg.UpstreamHost {
				found = true
				break
			}
		}
		if !found {
			allowedHosts = append(allowedHosts, cfg.UpstreamHost)
		}
	}

	return &Provider{
		Name:         cfg.Name,
		UpstreamHost: cfg.UpstreamHost,
		Scheme:       scheme,
		PathPrefix:   "/" + cfg.Name,
		AllowedHosts: allowedHosts,
		KeyOptional:  cfg.AuthMethod == "none",
		InjectKey:    makeInjectKey(cfg.AuthMethod, cfg.AuthHeader),
		ExtractToken: func(req *http.Request) string {
			return extractBearerOrXAPIKey(req)
		},
	}
}

// reportUsageToBackend sends a usage record to the backend API for persistence.
// Runs asynchronously — errors are logged but don't affect the proxy response.
func (p *Proxy) reportUsageToBackend(rec UsageRecord) {
	backendURL := p.metaSrv.BackendURL
	agentToken := p.metaSrv.AgentToken
	if backendURL == "" || agentToken == "" {
		return // no backend configured (e.g., in tests)
	}

	payload, err := json.Marshal(map[string]interface{}{
		"provider":      rec.Provider,
		"model":         rec.Model,
		"input_tokens":  rec.InputTokens,
		"output_tokens": rec.OutputTokens,
		"source":        rec.Source,
	})
	if err != nil {
		slog.Warn("apiproxy.usage_report.marshal_failed", "error", err)
		return
	}

	url := fmt.Sprintf("%s/api/agent/machines/%s/usage", backendURL, rec.MachineID)
	req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
	if err != nil {
		slog.Warn("apiproxy.usage_report.request_failed", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+agentToken)

	resp, err := p.client.Do(req)
	if err != nil {
		slog.Warn("apiproxy.usage_report.failed", "machine_id", rec.MachineID, "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("apiproxy.usage_report.non_ok", "machine_id", rec.MachineID, "status", resp.StatusCode)
	}
}

// SetRefreshURL sets the backend endpoint URL for OAuth token refresh.
func (p *Proxy) SetRefreshURL(url string) {
	p.refreshURL = url
}

// refreshCredentialResult holds the response from the backend refresh endpoint.
type refreshCredentialResult struct {
	AccessToken    string `json:"access_token"`
	CredentialType string `json:"credential_type"`
	ExpiresAt      string `json:"expires_at"`
	Error          string `json:"error"`
	Permanent      bool   `json:"permanent"`
}

// refreshCredential calls the backend to refresh an OAuth credential.
// Returns the new token, expiry, whether the failure is permanent, and any error.
func (p *Proxy) refreshCredential(machineID, provider string) (token string, expiry *time.Time, permanent bool, err error) {
	if p.refreshURL == "" {
		return "", nil, false, fmt.Errorf("no refresh URL configured")
	}

	payload, _ := json.Marshal(map[string]string{
		"provider": provider,
	})

	url := fmt.Sprintf("%s/api/agent/machines/%s/refresh-credential", p.refreshURL, machineID)
	req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
	if err != nil {
		return "", nil, false, fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.metaSrv.AgentToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.metaSrv.AgentToken)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return "", nil, false, fmt.Errorf("refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result refreshCredentialResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", nil, false, fmt.Errorf("decode refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", nil, result.Permanent, fmt.Errorf("%s", result.Error)
	}

	var parsedExpiry time.Time
	if result.ExpiresAt != "" {
		parsedExpiry, _ = time.Parse(time.RFC3339, result.ExpiresAt)
	}
	return result.AccessToken, &parsedExpiry, false, nil
}

// makeInjectKey returns an InjectKey function based on the auth method.
func makeInjectKey(authMethod, authHeader string) func(req *http.Request, key, credentialType string) {
	switch authMethod {
	case "bearer_header":
		header := "Authorization"
		if authHeader != "" {
			header = authHeader
		}
		return func(req *http.Request, key, credentialType string) {
			req.Header.Set(header, "Bearer "+key)
		}
	case "api_key_header":
		header := "x-api-key"
		if authHeader != "" {
			header = authHeader
		}
		return func(req *http.Request, key, credentialType string) {
			req.Header.Set(header, key)
		}
	case "query_param":
		paramName := "key"
		if authHeader != "" {
			paramName = authHeader
		}
		return func(req *http.Request, key, credentialType string) {
			q := req.URL.Query()
			q.Set(paramName, key)
			req.URL.RawQuery = q.Encode()
		}
	default: // "none"
		return func(req *http.Request, key, credentialType string) {
			// No key injection
		}
	}
}
