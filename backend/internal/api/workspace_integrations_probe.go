package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

type workspaceIntegrationProbeRequest struct {
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}

type workspaceIntegrationProbeResponse struct {
	URL             string                          `json:"url"`
	ProtocolVersion string                          `json:"protocol_version,omitempty"`
	Server          workspaceIntegrationProbeServer `json:"server"`
	Capabilities    map[string]interface{}          `json:"capabilities,omitempty"`
	Tools           []workspaceIntegrationProbeTool `json:"tools"`
	AuthRequired    bool                            `json:"auth_required"`
	OAuth           *workspaceIntegrationProbeOAuth `json:"oauth,omitempty"`
	ProbedAt        time.Time                       `json:"probed_at"`
}

type workspaceIntegrationProbeServer struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

type workspaceIntegrationProbeTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// MCP tools/list returns the schema as "inputSchema" (camelCase). Using the
	// wrong tag silently drops it, leaving tools with a permissive default schema
	// so agents can't construct correct arguments.
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type workspaceIntegrationProbeOAuth struct {
	Available                     bool     `json:"available"`
	ResourceMetadataURL           string   `json:"resource_metadata_url,omitempty"`
	Resource                      string   `json:"resource,omitempty"`
	AuthorizationServer           string   `json:"authorization_server,omitempty"`
	Issuer                        string   `json:"issuer,omitempty"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint,omitempty"`
	TokenEndpoint                 string   `json:"token_endpoint,omitempty"`
	RegistrationEndpoint          string   `json:"registration_endpoint,omitempty"`
	DynamicClientRegistration     bool     `json:"dynamic_client_registration"`
	ScopesSupported               []string `json:"scopes_supported,omitempty"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported,omitempty"`
}

func (s *Server) handleProbeWorkspaceIntegration(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	if _, ok := s.workspaceForIntegrationRequest(w, r, accountID); !ok {
		return
	}

	var req workspaceIntegrationProbeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	endpoint := strings.TrimSpace(req.URL)
	if err := workspaceIntegrationProbeEndpointSafe(r.Context(), endpoint, s.allowInsecureWorkspaceIntegrationEndpoints); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	probe, err := s.probeWorkspaceIntegrationMCPRemote(r.Context(), endpoint, workspaceIntegrationProbeAuthHeader(req.Token))
	if err != nil {
		if errors.Is(err, errWorkspaceIntegrationProbeAuthRequired) {
			writeError(w, http.StatusUnauthorized, "remote MCP server requires a bearer token or API key")
			return
		}
		writeError(w, http.StatusBadGateway, workspaceIntegrationClientErrorMessage("remote MCP probe failed", err))
		return
	}
	writeJSON(w, http.StatusOK, probe)
}

var errWorkspaceIntegrationProbeAuthRequired = errors.New("mcp-remote authentication required")

type workspaceIntegrationProbeAuthRequiredError struct {
	header http.Header
}

func (e workspaceIntegrationProbeAuthRequiredError) Error() string {
	return errWorkspaceIntegrationProbeAuthRequired.Error()
}

func (e workspaceIntegrationProbeAuthRequiredError) Unwrap() error {
	return errWorkspaceIntegrationProbeAuthRequired
}

func (s *Server) probeWorkspaceIntegrationMCPRemote(ctx context.Context, endpoint, authHeader string) (workspaceIntegrationProbeResponse, error) {
	client := workspaceIntegrationProbeHTTPClient(s.allowInsecureWorkspaceIntegrationEndpoints)
	probedAt := time.Now().UTC()
	initResp, sessionID, err := s.initializeWorkspaceIntegrationMCPRemoteForProbe(ctx, client, endpoint, authHeader)
	if err != nil {
		if errors.Is(err, errWorkspaceIntegrationProbeAuthRequired) && strings.TrimSpace(authHeader) == "" {
			oauth, oauthErr := s.discoverWorkspaceIntegrationMCPOAuth(ctx, client, err)
			if oauthErr == nil && oauth.Available {
				return workspaceIntegrationProbeResponse{
					URL:          endpoint,
					Tools:        []workspaceIntegrationProbeTool{},
					AuthRequired: true,
					OAuth:        &oauth,
					ProbedAt:     probedAt,
				}, nil
			}
		}
		return workspaceIntegrationProbeResponse{}, err
	}
	if err := s.notifyWorkspaceIntegrationMCPRemoteInitializedWithClient(ctx, client, endpoint, sessionID, authHeader); err != nil {
		return workspaceIntegrationProbeResponse{}, err
	}
	tools, err := s.listWorkspaceIntegrationMCPRemoteTools(ctx, client, endpoint, sessionID, authHeader)
	if err != nil {
		return workspaceIntegrationProbeResponse{}, err
	}
	return workspaceIntegrationProbeResponse{
		URL:             endpoint,
		ProtocolVersion: initResp.ProtocolVersion,
		Server:          initResp.ServerInfo,
		Capabilities:    initResp.Capabilities,
		Tools:           tools,
		AuthRequired:    false,
		ProbedAt:        probedAt,
	}, nil
}

func workspaceIntegrationProbeAuthHeader(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if strings.Contains(token, " ") {
		return token
	}
	return "Bearer " + token
}

type workspaceIntegrationMCPInitializeResult struct {
	ProtocolVersion string                          `json:"protocolVersion,omitempty"`
	ServerInfo      workspaceIntegrationProbeServer `json:"serverInfo,omitempty"`
	Capabilities    map[string]interface{}          `json:"capabilities,omitempty"`
}

func (s *Server) initializeWorkspaceIntegrationMCPRemoteForProbe(ctx context.Context, client *http.Client, endpoint, authHeader string) (workspaceIntegrationMCPInitializeResult, string, error) {
	resp, err := postWorkspaceIntegrationMCPRemoteRawWithClient(ctx, client, endpoint, "", authHeader, workspaceIntegrationMCPJSONRPCRequest{
		JSONRPC: "2.0",
		ID:      "initialize",
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": workspaceIntegrationMCPProtocolVersion,
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]interface{}{
				"name":    "ocm-workspace-integrations-probe",
				"version": "0.1.0",
			},
		},
	})
	if err != nil {
		return workspaceIntegrationMCPInitializeResult{}, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return workspaceIntegrationMCPInitializeResult{}, "", workspaceIntegrationProbeAuthRequiredError{header: resp.Header.Clone()}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return workspaceIntegrationMCPInitializeResult{}, "", fmt.Errorf("mcp-remote initialize returned %d", resp.StatusCode)
	}
	body, err := readWorkspaceIntegrationMCPResponseBody(resp)
	if err != nil {
		return workspaceIntegrationMCPInitializeResult{}, "", err
	}
	if body.Error != nil {
		return workspaceIntegrationMCPInitializeResult{}, "", fmt.Errorf("mcp-remote initialize failed: %s", body.Error.Message)
	}
	var result workspaceIntegrationMCPInitializeResult
	if len(body.Result) > 0 {
		if err := json.Unmarshal(body.Result, &result); err != nil {
			return workspaceIntegrationMCPInitializeResult{}, "", fmt.Errorf("decode mcp-remote initialize result: %w", err)
		}
	}
	return result, resp.Header.Get("Mcp-Session-Id"), nil
}

type workspaceIntegrationOAuthProtectedResourceMetadata struct {
	Resource             string   `json:"resource,omitempty"`
	AuthorizationServers []string `json:"authorization_servers,omitempty"`
	ScopesSupported      []string `json:"scopes_supported,omitempty"`
}

type workspaceIntegrationOAuthAuthorizationServerMetadata struct {
	Issuer                        string   `json:"issuer,omitempty"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint,omitempty"`
	TokenEndpoint                 string   `json:"token_endpoint,omitempty"`
	RegistrationEndpoint          string   `json:"registration_endpoint,omitempty"`
	ScopesSupported               []string `json:"scopes_supported,omitempty"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported,omitempty"`
}

func (s *Server) discoverWorkspaceIntegrationMCPOAuth(ctx context.Context, client *http.Client, authErr error) (workspaceIntegrationProbeOAuth, error) {
	var requiredErr workspaceIntegrationProbeAuthRequiredError
	if !errors.As(authErr, &requiredErr) {
		return workspaceIntegrationProbeOAuth{}, errors.New("oauth metadata unavailable")
	}
	resourceMetadataURL := workspaceIntegrationOAuthResourceMetadataURL(requiredErr.header.Values("WWW-Authenticate"))
	if resourceMetadataURL == "" {
		return workspaceIntegrationProbeOAuth{}, errors.New("oauth resource metadata unavailable")
	}
	if err := workspaceIntegrationProbeEndpointSafe(ctx, resourceMetadataURL, s.allowInsecureWorkspaceIntegrationEndpoints); err != nil {
		return workspaceIntegrationProbeOAuth{}, err
	}
	var resourceMetadata workspaceIntegrationOAuthProtectedResourceMetadata
	if err := getWorkspaceIntegrationOAuthMetadata(ctx, client, resourceMetadataURL, &resourceMetadata); err != nil {
		return workspaceIntegrationProbeOAuth{}, err
	}
	if len(resourceMetadata.AuthorizationServers) == 0 {
		return workspaceIntegrationProbeOAuth{}, errors.New("oauth resource metadata missing authorization_servers")
	}
	authorizationServer := strings.TrimSpace(resourceMetadata.AuthorizationServers[0])
	if err := workspaceIntegrationProbeEndpointSafe(ctx, authorizationServer, s.allowInsecureWorkspaceIntegrationEndpoints); err != nil {
		return workspaceIntegrationProbeOAuth{}, err
	}
	authServerMetadataURL, err := workspaceIntegrationAuthorizationServerMetadataURL(authorizationServer)
	if err != nil {
		return workspaceIntegrationProbeOAuth{}, err
	}
	if err := workspaceIntegrationProbeEndpointSafe(ctx, authServerMetadataURL, s.allowInsecureWorkspaceIntegrationEndpoints); err != nil {
		return workspaceIntegrationProbeOAuth{}, err
	}
	var authMetadata workspaceIntegrationOAuthAuthorizationServerMetadata
	if err := getWorkspaceIntegrationOAuthMetadata(ctx, client, authServerMetadataURL, &authMetadata); err != nil {
		return workspaceIntegrationProbeOAuth{}, err
	}
	if strings.TrimSpace(authMetadata.AuthorizationEndpoint) == "" || strings.TrimSpace(authMetadata.TokenEndpoint) == "" {
		return workspaceIntegrationProbeOAuth{}, errors.New("oauth authorization metadata missing endpoints")
	}
	for _, endpoint := range []string{authMetadata.AuthorizationEndpoint, authMetadata.TokenEndpoint, authMetadata.RegistrationEndpoint} {
		if strings.TrimSpace(endpoint) == "" {
			continue
		}
		if err := workspaceIntegrationProbeEndpointSafe(ctx, endpoint, s.allowInsecureWorkspaceIntegrationEndpoints); err != nil {
			return workspaceIntegrationProbeOAuth{}, err
		}
	}
	scopes := authMetadata.ScopesSupported
	if len(scopes) == 0 {
		scopes = resourceMetadata.ScopesSupported
	}
	return workspaceIntegrationProbeOAuth{
		Available:                     true,
		ResourceMetadataURL:           resourceMetadataURL,
		Resource:                      resourceMetadata.Resource,
		AuthorizationServer:           authorizationServer,
		Issuer:                        authMetadata.Issuer,
		AuthorizationEndpoint:         authMetadata.AuthorizationEndpoint,
		TokenEndpoint:                 authMetadata.TokenEndpoint,
		RegistrationEndpoint:          authMetadata.RegistrationEndpoint,
		DynamicClientRegistration:     strings.TrimSpace(authMetadata.RegistrationEndpoint) != "",
		ScopesSupported:               scopes,
		CodeChallengeMethodsSupported: authMetadata.CodeChallengeMethodsSupported,
	}, nil
}

func getWorkspaceIntegrationOAuthMetadata(ctx context.Context, client *http.Client, metadataURL string, dest interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("oauth metadata returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return fmt.Errorf("decode oauth metadata: %w", err)
	}
	return nil
}

func workspaceIntegrationOAuthResourceMetadataURL(headers []string) string {
	params := workspaceIntegrationBearerChallengeParams(headers)
	for _, key := range []string{"resource_metadata", "resource_metadata_uri"} {
		if value := strings.TrimSpace(params[key]); value != "" {
			return value
		}
	}
	return ""
}

func workspaceIntegrationBearerChallengeParams(headers []string) map[string]string {
	params := map[string]string{}
	for _, header := range headers {
		value := strings.TrimSpace(header)
		if len(value) < len("Bearer") || !strings.EqualFold(value[:len("Bearer")], "Bearer") {
			continue
		}
		rest := strings.TrimSpace(value[len("Bearer"):])
		for _, part := range splitWorkspaceIntegrationAuthParams(rest) {
			key, rawValue, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			key = strings.ToLower(strings.TrimSpace(key))
			rawValue = strings.TrimSpace(rawValue)
			if unquoted, err := strconv.Unquote(rawValue); err == nil {
				rawValue = unquoted
			}
			params[key] = rawValue
		}
	}
	return params
}

func splitWorkspaceIntegrationAuthParams(value string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false
	escaped := false
	for _, r := range value {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\' && inQuote:
			current.WriteRune(r)
			escaped = true
		case r == '"':
			current.WriteRune(r)
			inQuote = !inQuote
		case r == ',' && !inQuote:
			if part := strings.TrimSpace(current.String()); part != "" {
				parts = append(parts, part)
			}
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if part := strings.TrimSpace(current.String()); part != "" {
		parts = append(parts, part)
	}
	return parts
}

func workspaceIntegrationAuthorizationServerMetadataURL(authorizationServer string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(authorizationServer))
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("oauth authorization server must be an absolute URL")
	}
	issuerPath := strings.TrimRight(parsed.EscapedPath(), "/")
	if issuerPath == "" {
		parsed.Path = "/.well-known/oauth-authorization-server"
	} else {
		parsed.Path = path.Join("/.well-known/oauth-authorization-server", issuerPath)
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (s *Server) notifyWorkspaceIntegrationMCPRemoteInitializedWithClient(ctx context.Context, client *http.Client, endpoint, sessionID, authHeader string) error {
	resp, err := postWorkspaceIntegrationMCPRemoteRawWithClient(ctx, client, endpoint, sessionID, authHeader, workspaceIntegrationMCPJSONRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mcp-remote initialized notification returned %d", resp.StatusCode)
	}
	return nil
}

func (s *Server) listWorkspaceIntegrationMCPRemoteTools(ctx context.Context, client *http.Client, endpoint, sessionID, authHeader string) ([]workspaceIntegrationProbeTool, error) {
	resp, err := postWorkspaceIntegrationMCPRemoteRawWithClient(ctx, client, endpoint, sessionID, authHeader, workspaceIntegrationMCPJSONRPCRequest{
		JSONRPC: "2.0",
		ID:      "tools-list",
		Method:  "tools/list",
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mcp-remote tools/list returned %d", resp.StatusCode)
	}
	body, err := readWorkspaceIntegrationMCPResponseBody(resp)
	if err != nil {
		return nil, err
	}
	if body.Error != nil {
		return nil, fmt.Errorf("mcp-remote tools/list failed: %s", body.Error.Message)
	}
	var result struct {
		Tools []workspaceIntegrationProbeTool `json:"tools"`
	}
	if err := json.Unmarshal(body.Result, &result); err != nil {
		return nil, fmt.Errorf("decode mcp-remote tools/list result: %w", err)
	}
	if len(result.Tools) > workspaceIntegrationMaxDiscoveredTools {
		return nil, fmt.Errorf("mcp-remote tools/list returned %d tools, max %d", len(result.Tools), workspaceIntegrationMaxDiscoveredTools)
	}
	for _, tool := range result.Tools {
		if strings.TrimSpace(tool.Name) == "" {
			return nil, errors.New("mcp-remote tools/list returned a tool without a name")
		}
		if strings.Contains(tool.Name, ".") || strings.ContainsAny(tool.Name, " \t\r\n") {
			return nil, fmt.Errorf("mcp-remote tool %q must not contain dots or whitespace", tool.Name)
		}
	}
	return result.Tools, nil
}

func workspaceIntegrationProbeEndpointSafe(ctx context.Context, endpoint string, allowInsecure bool) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return errors.New("url is required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid URL")
	}
	if parsed.Scheme != "https" && (!allowInsecure || parsed.Scheme != "http") {
		return errors.New("remote MCP URL must use https")
	}
	if parsed.Hostname() == "" {
		return errors.New("remote MCP URL host is required")
	}
	if allowInsecure {
		return nil
	}
	if err := workspaceIntegrationEndpointHostSafe(ctx, parsed.Hostname()); err != nil {
		return err
	}
	return nil
}

func workspaceIntegrationEndpointHostSafe(ctx context.Context, host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return errors.New("endpoint host is required")
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") ||
		strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".local") {
		return fmt.Errorf("endpoint host %q is not allowed", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if workspaceIntegrationIPBlocked(ip) {
			return fmt.Errorf("endpoint host %q is not allowed", host)
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve endpoint host %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("endpoint host %q did not resolve", host)
	}
	for _, addr := range addrs {
		if workspaceIntegrationIPBlocked(addr.IP) {
			return fmt.Errorf("endpoint host %q resolves to blocked address %s", host, addr.IP.String())
		}
	}
	return nil
}

func workspaceIntegrationIPBlocked(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

func workspaceIntegrationHTTPClient(allowInsecure bool) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return workspaceIntegrationProbeDialContext(ctx, network, address, allowInsecure)
		},
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			return workspaceIntegrationProbeEndpointSafe(req.Context(), req.URL.String(), allowInsecure)
		},
	}
}

func workspaceIntegrationProbeHTTPClient(allowInsecure bool) *http.Client {
	return workspaceIntegrationHTTPClient(allowInsecure)
}

func postWorkspaceIntegrationMCPRemoteRawWithClient(ctx context.Context, client *http.Client, endpoint, sessionID, authHeader string, payload workspaceIntegrationMCPJSONRPCRequest) (*http.Response, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", workspaceIntegrationMCPProtocolVersion)
	if strings.TrimSpace(sessionID) != "" {
		req.Header.Set("Mcp-Session-Id", strings.TrimSpace(sessionID))
	}
	if strings.TrimSpace(authHeader) != "" {
		req.Header.Set("Authorization", strings.TrimSpace(authHeader))
	}
	return client.Do(req)
}

func workspaceIntegrationProbeDialContext(ctx context.Context, network, address string, allowInsecure bool) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if allowInsecure {
		return dialer.DialContext(ctx, network, address)
	}
	if ip := net.ParseIP(host); ip != nil {
		if workspaceIntegrationIPBlocked(ip) {
			return nil, fmt.Errorf("endpoint host %q is not allowed", host)
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	if err := workspaceIntegrationEndpointHostSafe(ctx, host); err != nil {
		return nil, err
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve endpoint host %q: %w", host, err)
	}
	var lastErr error
	for _, addr := range addrs {
		if workspaceIntegrationIPBlocked(addr.IP) {
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("endpoint host %q resolves only to blocked addresses", host)
}
