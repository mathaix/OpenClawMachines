package agentapi

import (
	"bytes"
	"crypto/subtle"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/mathaix/openclawmachines/backend/internal/auth"
)

// wsUpgrader for agent-side WebSocket proxies.
var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     checkWSOrigin,
}

// checkWSOrigin validates WebSocket upgrade requests by origin.
// Accepts: empty origin (non-browser clients), the configured data-plane domain,
// and localhost (any port, any scheme) for development.
func checkWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Non-browser clients (curl, agent-to-agent) don't send Origin.
		return true
	}

	// Parse the origin to extract scheme and host.
	// Origin header format: scheme "://" host [ ":" port ]
	// We need at least "://".
	idx := strings.Index(origin, "://")
	if idx < 0 {
		return false
	}
	scheme := strings.ToLower(origin[:idx])
	hostPort := origin[idx+3:] // host or host:port

	// Strip port to get bare hostname.
	host := hostPort
	if colonIdx := strings.LastIndex(hostPort, ":"); colonIdx >= 0 {
		host = hostPort[:colonIdx]
	}
	host = strings.ToLower(host)

	// Allow localhost for development (any port, any scheme).
	if host == "localhost" || host == "127.0.0.1" {
		return true
	}

	// Allow the configured data-plane domain over HTTPS only.
	if scheme != "https" {
		return false
	}
	if domain := configuredDataPlaneDomain(); domain != "" && (host == domain || strings.HasSuffix(host, "."+domain)) {
		return true
	}

	return false
}

func configuredDataPlaneDomain() string {
	if d := strings.Trim(strings.TrimSpace(os.Getenv("OCM_DATA_PLANE_DOMAIN")), "."); d != "" {
		return d
	}
	return strings.Trim(strings.TrimSpace(os.Getenv("DATA_PLANE_DOMAIN")), ".")
}

func frameAncestorsDirective() string {
	domain := configuredDataPlaneDomain()
	if domain == "" {
		return "frame-ancestors 'self'"
	}
	return fmt.Sprintf("frame-ancestors 'self' %s *.%s", domain, domain)
}

// machineInfo holds resolved VM info for proxy handlers.
type machineInfo struct {
	MachineID    string
	MachineSlug  string
	VMIP         string
	ProxyToken   string
	GatewayToken string
	proxyPort    int // VM auth proxy port (default 8080)
	SigningKey   string
}

// mintInternalToken mints a short-lived machine JWT for proxying requests
// through the VM's auth proxy. Returns empty string on failure.
func (mi *machineInfo) mintInternalToken() string {
	if mi.SigningKey == "" {
		slog.Warn("proxy.mint_token_skipped", "machine_id", mi.MachineID, "reason", "signing_key_empty")
		return ""
	}
	token, _, err := auth.IssueMachineToken(0, "agent@internal", mi.MachineID, 0, []string{"all"}, mi.SigningKey)
	if err != nil {
		slog.Error("proxy.mint_token_failed", "machine_id", mi.MachineID, "error", err)
		return ""
	}
	return token
}

// getMachineInfo resolves a machineID to its bridge IP and proxy token via the orchestrator.
func (s *Server) getMachineInfo(r *http.Request) (*machineInfo, error) {
	machineID := chi.URLParam(r, "machineID")
	if machineID == "" {
		return nil, fmt.Errorf("missing machineID")
	}

	if s.orchestrator == nil {
		return nil, fmt.Errorf("orchestrator not available")
	}

	vm, err := s.orchestrator.Get(r.Context(), machineID)
	if err != nil {
		return nil, fmt.Errorf("VM %s not found: %w", machineID, err)
	}

	return &machineInfo{
		MachineID:    machineID,
		MachineSlug:  vm.MachineSlug,
		VMIP:         vm.VMIP,
		ProxyToken:   vm.ProxyToken,
		GatewayToken: vm.GatewayToken,
		SigningKey:   vm.SigningKey,
		proxyPort:    s.vmProxyPort,
	}, nil
}

// validateProxyToken checks the per-machine proxy token from query param or header.
// Returns an error and writes an HTTP response if validation fails.
func validateProxyToken(w http.ResponseWriter, r *http.Request, expected string) bool {
	if expected == "" {
		// No token configured — reject request (defense in depth)
		slog.Warn("proxy.token_not_configured")
		http.Error(w, "proxy token not configured for this VM", http.StatusForbidden)
		return false
	}

	// Check header first (Worker always sets X-Proxy-Token header).
	// Fall back to query param for direct/non-Worker clients.
	// Header takes priority because the browser may include a "token" query
	// param for gateway auth (clawdbot SPA), which would collide with proxy auth.
	token := r.Header.Get("X-Proxy-Token")
	if token == "" {
		token = r.URL.Query().Get("token")
	}

	if token == "" {
		http.Error(w, "missing proxy token", http.StatusUnauthorized)
		return false
	}

	if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
		http.Error(w, "invalid proxy token", http.StatusForbidden)
		return false
	}

	return true
}

// handleHealthProxy checks if the VM's auth proxy (port 8080) is responding.
func (s *Server) handleHealthProxy(w http.ResponseWriter, r *http.Request) {
	mi, err := s.getMachineInfo(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !validateProxyToken(w, r, mi.ProxyToken) {
		return
	}

	authProxyOK := checkPort(mi.VMIP, 8080, 2*time.Second)
	gatewayOK := probeGatewayHealth(mi.VMIP, 2*time.Second)

	status := "ok"
	if !authProxyOK {
		status = "unreachable"
	} else if !gatewayOK {
		status = "degraded"
	}

	resp := map[string]interface{}{
		"status":     status,
		"auth_proxy": authProxyOK,
		"gateway":    gatewayOK,
	}

	slog.Debug("proxy.health_check", "machine_id", mi.MachineID, "status", status, "auth_proxy", authProxyOK, "gateway", gatewayOK)

	writeJSON(w, http.StatusOK, resp)
}

// handleGatewayProxy proxies HTTP and WebSocket connections to the openclaw
// gateway running inside the MicroVM, routed through the auth proxy on port 8080.
func (s *Server) handleGatewayProxy(w http.ResponseWriter, r *http.Request) {
	mi, err := s.getMachineInfo(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !validateProxyToken(w, r, mi.ProxyToken) {
		return
	}

	path := chi.URLParam(r, "*")

	if websocket.IsWebSocketUpgrade(r) {
		token := mi.mintInternalToken()
		if token == "" {
			http.Error(w, "failed to mint internal token", http.StatusInternalServerError)
			return
		}
		// Build target URL, forwarding query params but stripping
		// the proxy-internal "token" param to avoid leaking it to the VM.
		q := r.URL.Query()
		q.Del("token")
		targetURL := fmt.Sprintf("ws://%s:%d/gateway/%s", mi.VMIP, mi.proxyPort, path)
		if qs := q.Encode(); qs != "" {
			targetURL += "?" + qs
		}
		slog.Info("proxy.ws_upgrade", "machine_id", mi.MachineID, "target", targetURL, "service", "gateway")
		headers := http.Header{
			"X-Machine-Token": {token},
			"Authorization":   {"Bearer " + mi.GatewayToken},
		}
		proxyBrowserWebSocket(w, r, targetURL, headers)
		return
	}

	// For the root HTML page, rewrite the base path so the openclaw SPA
	// knows its prefix when served behind the subdomain proxy.
	if path == "" && mi.MachineSlug != "" {
		proxyGatewayRoot(w, r, mi)
		return
	}

	proxyHTTPWithMachineToken(w, r, mi, "gateway/"+path)
}

// handleDashboardProxy proxies HTTP and WebSocket connections to the Hermes
// dashboard/webchat surface running inside the MicroVM.
func (s *Server) handleDashboardProxy(w http.ResponseWriter, r *http.Request) {
	mi, err := s.getMachineInfo(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !validateProxyToken(w, r, mi.ProxyToken) {
		return
	}

	path := chi.URLParam(r, "*")
	prefix := r.Header.Get("X-Forwarded-Prefix")
	if prefix == "" && mi.MachineSlug != "" {
		prefix = "/" + mi.MachineSlug + "/dashboard"
	}
	if prefix == "" {
		prefix = "/dashboard"
	}

	if websocket.IsWebSocketUpgrade(r) {
		token := mi.mintInternalToken()
		if token == "" {
			http.Error(w, "failed to mint internal token", http.StatusInternalServerError)
			return
		}
		targetURL := fmt.Sprintf("ws://%s:%d/dashboard/%s", mi.VMIP, mi.proxyPort, path)
		if r.URL.RawQuery != "" {
			targetURL += "?" + r.URL.RawQuery
		}
		slog.Info("proxy.ws_upgrade", "machine_id", mi.MachineID, "target", targetURL, "service", "dashboard")
		headers := http.Header{
			"X-Machine-Token":    {token},
			"X-Forwarded-Prefix": {prefix},
		}
		proxyBrowserWebSocket(w, r, targetURL, headers)
		return
	}

	proxyHTTPWithMachineTokenOptions(w, r, mi, "dashboard/"+path, http.Header{
		"X-Forwarded-Prefix": {prefix},
	}, false)
}

// proxyGatewayRoot proxies the gateway root HTML and rewrites the openclaw
// base path so the SPA works correctly behind the subdomain proxy.
// Without this, the openclaw JS sees __OPENCLAW_CONTROL_UI_BASE_PATH__=""
// and may fail to construct correct URLs at sub-paths like /{slug}/gateway/.
func proxyGatewayRoot(w http.ResponseWriter, r *http.Request, mi *machineInfo) {
	token := mi.mintInternalToken()
	if token == "" {
		http.Error(w, "failed to mint internal token", http.StatusInternalServerError)
		return
	}

	targetURL := fmt.Sprintf("http://%s:%d/gateway/", mi.VMIP, mi.proxyPort)
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	for key, values := range r.Header {
		switch strings.ToLower(key) {
		case "authorization", "cookie", "x-proxy-token", "x-machine-token", "x-forwarded-prefix":
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("X-Machine-Token", token)
	if mi.GatewayToken != "" {
		req.Header.Set("Authorization", "Bearer "+mi.GatewayToken)
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("proxy.gateway_root_failed", "machine_id", mi.MachineID, "target", targetURL, "error", err, "duration_ms", time.Since(start).Milliseconds())
		http.Error(w, "failed to reach VM", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		slog.Warn("proxy.gateway_root_error", "machine_id", mi.MachineID, "target", targetURL, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
	} else {
		slog.Debug("proxy.gateway_root_ok", "machine_id", mi.MachineID, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
	}

	// Copy response headers (same rules as proxyHTTPWithMachineToken).
	for key, values := range resp.Header {
		switch strings.ToLower(key) {
		case "x-frame-options":
			continue
		case "content-security-policy":
			for _, value := range values {
				cleaned := strings.Replace(value, "frame-ancestors 'none'", frameAncestorsDirective(), 1)
				w.Header().Add(key, cleaned)
			}
			continue
		case "content-length":
			continue // will be recalculated after rewriting
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "failed to read response", http.StatusBadGateway)
		return
	}

	// Rewrite the base path in HTML responses so the openclaw SPA knows
	// its URL prefix (e.g. "/my-machine/gateway").
	if strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		basePath := "/" + mi.MachineSlug + "/gateway"
		body = bytes.Replace(body,
			[]byte(`window.__OPENCLAW_CONTROL_UI_BASE_PATH__=""`),
			[]byte(`window.__OPENCLAW_CONTROL_UI_BASE_PATH__="`+basePath+`"`), 1)
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// proxyBrowserWebSocket proxies a WebSocket connection bidirectionally.
// extraHeaders are merged into the dial request (e.g. Authorization for gateway auth).
func proxyBrowserWebSocket(w http.ResponseWriter, r *http.Request, targetURL string, extraHeaders http.Header) {
	// Start with any extra headers (like Authorization for gateway auth).
	dialHeaders := make(http.Header)
	for k, v := range extraHeaders {
		dialHeaders[k] = v
	}
	// Forward subprotocols from the client to the target if present.
	if proto := r.Header.Get("Sec-WebSocket-Protocol"); proto != "" {
		dialHeaders.Set("Sec-WebSocket-Protocol", proto)
	}
	// Set Origin header to match the target so the gateway's browser-origin
	// check passes. gorilla/websocket's DefaultDialer does not set Origin,
	// causing the gateway to reject control-UI connections with "origin
	// missing or invalid". The agent is a trusted proxy (already validated
	// via proxy_token), so this is safe.
	if u, parseErr := url.Parse(targetURL); parseErr == nil {
		dialHeaders.Set("Origin", "http://"+u.Host)
	}

	// Connect to the target (VM's browser/terminal)
	targetConn, targetResp, err := websocket.DefaultDialer.Dial(targetURL, dialHeaders)
	if err != nil {
		statusCode := 0
		if targetResp != nil {
			statusCode = targetResp.StatusCode
		}
		slog.Error("proxy.ws_connect_failed", "target", targetURL, "error", err, "upstream_status", statusCode)
		http.Error(w, "failed to connect to target", http.StatusBadGateway)
		return
	}
	defer func() { _ = targetConn.Close() }()

	slog.Info("proxy.ws_connected", "target", targetURL)

	// Use a per-call upgrader so we can set the negotiated subprotocol.
	upgrader := wsUpgrader
	if targetResp != nil {
		if negotiated := targetResp.Header.Get("Sec-WebSocket-Protocol"); negotiated != "" {
			upgrader.Subprotocols = []string{negotiated}
		}
	}

	// Upgrade the client connection
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("proxy.ws_client_upgrade_failed", "target", targetURL, "error", err)
		return
	}
	defer func() { _ = clientConn.Close() }()

	// Bidirectional proxy
	start := time.Now()
	proxyWebSocketBidirectional(clientConn, targetConn)
	slog.Info("proxy.ws_closed", "target", targetURL, "duration_s", int(time.Since(start).Seconds()))
}

var (
	wsPingInterval = 25 * time.Second
	wsPongTimeout  = 10 * time.Second
)

// proxyWebSocketBidirectional copies messages between two WebSocket connections
// with ping/pong keepalive to prevent idle timeouts from intermediary proxies.
func proxyWebSocketBidirectional(client, target *websocket.Conn) {
	done := make(chan struct{}, 2)
	stop := make(chan struct{})

	// Setup ping/pong keepalive on client connection.
	// We send pings to the browser client; the browser auto-responds with pongs.
	_ = client.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
	client.SetPongHandler(func(string) error {
		_ = client.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
		return nil
	})

	// Setup read deadline on target (VM) connection.
	// The VM gateway sends tick events every 30s which reset this deadline.
	_ = target.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
	target.SetPongHandler(func(string) error {
		_ = target.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
		return nil
	})

	// Ping both legs. Some upstream services, including the Hermes dashboard,
	// can be silent for longer than the target read deadline.
	ticker := time.NewTicker(wsPingInterval)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := client.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					return
				}
				if err := target.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					return
				}
			}
		}
	}()

	// Client → Target
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			msgType, msg, err := client.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
					slog.Warn("proxy.ws_client_read_error", "error", err)
				}
				return
			}
			_ = client.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
			if err := target.WriteMessage(msgType, msg); err != nil {
				slog.Debug("proxy.ws_target_write_error", "error", err)
				return
			}
		}
	}()

	// Target → Client
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			msgType, msg, err := target.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
					slog.Warn("proxy.ws_target_read_error", "error", err)
				}
				return
			}
			_ = target.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
			if err := client.WriteMessage(msgType, msg); err != nil {
				slog.Debug("proxy.ws_client_write_error", "error", err)
				return
			}
		}
	}()

	<-done
	ticker.Stop()
	close(stop)
}

// proxyHTTPWithMachineToken proxies an HTTP request to the VM's auth proxy,
// injecting an X-Machine-Token header for authentication.
func proxyHTTPWithMachineToken(w http.ResponseWriter, r *http.Request, mi *machineInfo, path string) {
	proxyHTTPWithMachineTokenOptions(w, r, mi, path, nil, true)
}

func proxyHTTPWithMachineTokenOptions(w http.ResponseWriter, r *http.Request, mi *machineInfo, path string, extraHeaders http.Header, injectGatewayAuth bool) {
	token := mi.mintInternalToken()
	if token == "" {
		http.Error(w, "failed to mint internal token", http.StatusInternalServerError)
		return
	}

	targetURL := fmt.Sprintf("http://%s:%d/%s", mi.VMIP, mi.proxyPort, path)
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	for key, values := range r.Header {
		switch strings.ToLower(key) {
		case "authorization", "cookie", "x-proxy-token", "x-machine-token", "x-forwarded-prefix":
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("X-Machine-Token", token)
	for key, values := range extraHeaders {
		for _, value := range values {
			req.Header.Set(key, value)
		}
	}
	// Inject gateway bearer token so the gateway accepts the request after
	// the auth proxy forwards it. The auth proxy passes through headers
	// (including Authorization) via httputil.ReverseProxy.
	if injectGatewayAuth && mi.GatewayToken != "" {
		req.Header.Set("Authorization", "Bearer "+mi.GatewayToken)
	} else if injectGatewayAuth {
		slog.Warn("proxy.no_gateway_token", "machine_id", mi.MachineID, "target", targetURL)
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("proxy.http_machine_token_failed", "machine_id", mi.MachineID, "target", targetURL, "error", err, "duration_ms", time.Since(start).Milliseconds())
		http.Error(w, "failed to reach VM", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		slog.Warn("proxy.http_upstream_error", "machine_id", mi.MachineID, "target", targetURL, "status", resp.StatusCode, "method", r.Method, "duration_ms", time.Since(start).Milliseconds())
	} else {
		slog.Debug("proxy.http_ok", "machine_id", mi.MachineID, "path", path, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
	}

	for key, values := range resp.Header {
		switch strings.ToLower(key) {
		case "x-frame-options":
			continue
		case "content-security-policy":
			for _, value := range values {
				cleaned := strings.Replace(value, "frame-ancestors 'none'", frameAncestorsDirective(), 1)
				w.Header().Add(key, cleaned)
			}
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handleFilesProxy proxies requests to filebrowser running inside the VM.
// Filebrowser listens on port 8082 with --baseURL /files, behind the auth proxy
// at /files/*. The auth proxy does not require a machine token for /files/*.
// This proxy route bypasses Cloudflare Access headers (X-Frame-Options, CSP)
// so filebrowser can be embedded in an iframe in the frontend.
//
// In production, the iframe loads at https://{account}.{DATA_PLANE_DOMAIN}/{machineSlug}/files/.
// Filebrowser generates absolute asset paths like /files/static/assets/... which the browser
// resolves without the machine slug prefix, causing 404s. For text responses (HTML, JS, CSS),
// we rewrite /files/ paths to /{machineSlug}/files/ so assets load through the correct route.
func (s *Server) handleFilesProxy(w http.ResponseWriter, r *http.Request) {
	mi, err := s.getMachineInfo(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !validateProxyToken(w, r, mi.ProxyToken) {
		return
	}

	path := chi.URLParam(r, "*")
	targetURL := fmt.Sprintf("http://%s:%d/files/%s", mi.VMIP, mi.proxyPort, path)
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	for key, values := range r.Header {
		switch strings.ToLower(key) {
		case "authorization", "x-proxy-token", "x-machine-token":
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		slog.Error("proxy.files_failed", "machine_id", mi.MachineID, "target", targetURL, "error", err)
		http.Error(w, "failed to reach filebrowser", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Copy response headers, stripping X-Frame-Options and rewriting CSP
	// so filebrowser can be embedded in an iframe.
	for key, values := range resp.Header {
		switch strings.ToLower(key) {
		case "x-frame-options":
			continue
		case "content-security-policy":
			for _, value := range values {
				cleaned := strings.Replace(value, "frame-ancestors 'none'", frameAncestorsDirective(), 1)
				w.Header().Add(key, cleaned)
			}
			continue
		case "content-length":
			// Will be recalculated if we rewrite the body.
			if mi.MachineSlug != "" && isRewritableContent(resp.Header.Get("Content-Type")) {
				continue
			}
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// For text responses, rewrite absolute /files paths to /{slug}/files
	// so the browser resolves asset URLs through the correct machine-prefixed route.
	// Two rewrites are needed:
	//   1. "/files/" in HTML/CSS href/src attributes
	//   2. '"/files"' (quoted, no trailing slash) — the SPA's baseURL used to
	//      construct runtime API calls like "/files" + "/api/login"
	if mi.MachineSlug != "" && isRewritableContent(resp.Header.Get("Content-Type")) {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, "failed to read response", http.StatusBadGateway)
			return
		}
		// The external base is the URL prefix the browser uses to reach
		// filebrowser. Production: /{slug}/files (subdomain routing). Dev: the
		// control plane forwards X-Forwarded-Prefix (…/machines/{id}/files) so
		// assets resolve through the control-plane proxy instead of a
		// slug-prefixed path that only exists behind Cloudflare.
		externalBase := r.Header.Get("X-Forwarded-Prefix")
		if externalBase == "" {
			externalBase = "/" + mi.MachineSlug + "/files"
		}
		// Rewrite "/files/" paths (href, src, url() references)
		rewritten := bytes.ReplaceAll(body, []byte("/files/"), []byte(externalBase+"/"))
		// Rewrite quoted "/files" baseURL in JS (e.g. baseURL:"/files" → baseURL:"{base}")
		rewritten = bytes.ReplaceAll(rewritten, []byte(`"/files"`), []byte(`"`+externalBase+`"`))
		rewritten = bytes.ReplaceAll(rewritten, []byte(`'/files'`), []byte(`'`+externalBase+`'`))
		w.Header().Set("Content-Length", strconv.Itoa(len(rewritten)))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(rewritten)
		return
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// isRewritableContent returns true for text-based content types where
// filebrowser's absolute /files/ paths need to be rewritten.
func isRewritableContent(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.HasPrefix(ct, "text/html") ||
		strings.HasPrefix(ct, "text/css") ||
		strings.HasPrefix(ct, "text/javascript") ||
		strings.HasPrefix(ct, "application/javascript") ||
		strings.HasPrefix(ct, "application/json")
}

// handleTerminalProxy proxies WebSocket connections to the PTY server
// running inside the MicroVM, routed through the auth proxy on port 8080.
func (s *Server) handleTerminalProxy(w http.ResponseWriter, r *http.Request) {
	mi, err := s.getMachineInfo(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !validateProxyToken(w, r, mi.ProxyToken) {
		return
	}

	// Extract the path after /proxy/{machineID}/terminal/
	path := chi.URLParam(r, "*")
	if path == "" {
		path = "ws"
	}

	// Check if this is a WebSocket upgrade
	if !websocket.IsWebSocketUpgrade(r) {
		http.Error(w, "WebSocket upgrade required", http.StatusBadRequest)
		return
	}

	token := mi.mintInternalToken()
	if token == "" {
		http.Error(w, "failed to mint internal token", http.StatusInternalServerError)
		return
	}

	targetURL := fmt.Sprintf("ws://%s:%d/terminal/%s", mi.VMIP, mi.proxyPort, path)
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	slog.Info("proxy.ws_upgrade", "machine_id", mi.MachineID, "target", targetURL, "service", "terminal")

	// Use the standard WebSocket proxy with machine token for auth proxy
	headers := http.Header{"X-Machine-Token": {token}}
	proxyBrowserWebSocket(w, r, targetURL, headers)
}

// handleGatewayHealthProxy proxies a health check to the PTY server's /health endpoint
// which reports the gateway supervisor status (running, crash-loop, unknown).
func (s *Server) handleGatewayHealthProxy(w http.ResponseWriter, r *http.Request) {
	mi, err := s.getMachineInfo(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !validateProxyToken(w, r, mi.ProxyToken) {
		return
	}

	targetURL := fmt.Sprintf("http://%s:7681/health", mi.VMIP)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(targetURL)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"gateway": "unreachable"})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handleRestartGateway signals the VM supervisor to restart the gateway process.
func (s *Server) handleRestartGateway(w http.ResponseWriter, r *http.Request) {
	mi, err := s.getMachineInfo(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !validateProxyToken(w, r, mi.ProxyToken) {
		return
	}

	targetURL := fmt.Sprintf("http://%s:7681/restart-gateway", mi.VMIP)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(targetURL, "application/json", nil)
	if err != nil {
		http.Error(w, "failed to reach VM", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handleWriteFileProxy proxies POST /write-file to the PTY server inside the VM.
func (s *Server) handleWriteFileProxy(w http.ResponseWriter, r *http.Request) {
	mi, err := s.getMachineInfo(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !validateProxyToken(w, r, mi.ProxyToken) {
		return
	}

	targetURL := fmt.Sprintf("http://%s:7681/write-file", mi.VMIP)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(targetURL, "application/json", r.Body)
	if err != nil {
		http.Error(w, "failed to reach VM", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handleExecProxy proxies structured command execution to the PTY server
// running inside the MicroVM.
func (s *Server) handleExecProxy(w http.ResponseWriter, r *http.Request) {
	mi, err := s.getMachineInfo(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !validateProxyToken(w, r, mi.ProxyToken) {
		return
	}

	targetURL := fmt.Sprintf("http://%s:7681/exec", mi.VMIP)
	client := &http.Client{Timeout: 35 * time.Second}
	resp, err := client.Post(targetURL, "application/json", r.Body)
	if err != nil {
		http.Error(w, "failed to reach VM", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// checkPort tests if a TCP port is reachable.
func checkPort(ip string, port int, timeout time.Duration) bool {
	addr := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
