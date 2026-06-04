package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	signingKey := os.Getenv("SIGNING_KEY")
	machineID := os.Getenv("MACHINE_ID")
	port := getEnv("PORT", "8080")

	if signingKey == "" || machineID == "" {
		slog.Error("authproxy.missing_config", "error", "SIGNING_KEY and MACHINE_ID are required")
		os.Exit(1)
	}

	gatewayToken := os.Getenv("OPENCLAW_GATEWAY_TOKEN")
	if gatewayToken == "" {
		slog.Error("authproxy.missing_config", "error", "OPENCLAW_GATEWAY_TOKEN is required for Control UI auth")
		os.Exit(1)
	}

	mux := http.NewServeMux()

	// Health check (no auth required)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, `{"status":"ok"}`)
	})

	// All other routes require auth
	proxy := &authProxy{
		signingKey:    signingKey,
		machineID:     machineID,
		gatewayToken:  gatewayToken,
		gatewayAddr:   getEnv("GATEWAY_ADDR", "127.0.0.1:18789"),
		dashboardAddr: getEnv("DASHBOARD_ADDR", "127.0.0.1:9119"),
	}
	mux.Handle("/", proxy)

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // no timeout for WebSocket/streaming
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("authproxy.shutdown")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	slog.Info("authproxy.start", "port", port, "machine_id", machineID)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("authproxy.error", "error", err)
		os.Exit(1)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// authProxy validates machine tokens and reverse-proxies to internal VM services.
type authProxy struct {
	signingKey    string
	machineID     string
	gatewayToken  string // OPENCLAW_GATEWAY_TOKEN — injected into WS connect messages
	gatewayAddr   string // gateway target address (default "127.0.0.1:18789")
	dashboardAddr string // Hermes dashboard target address (default "127.0.0.1:9119")
}

var portPathRe = regexp.MustCompile(`^/port/(\d+)(/.*)?$`)

var (
	wsPingInterval = 25 * time.Second
	wsPongTimeout  = 10 * time.Second
	wsWriteTimeout = 5 * time.Second
)

// reservedPorts are internal OCM service ports that must not be reachable
// through the /port/* proxy. Exposing them would let users bypass auth
// (terminal on 7681, gateway on 18789) or reach agent APIs (9090/9091).
var reservedPorts = map[int]bool{
	22:    true, // SSH
	80:    true, // metadata / internal HTTP
	7681:  true, // terminal (ttyd)
	8080:  true, // auth proxy itself
	9090:  true, // agent API
	9091:  true, // agent metrics
	8082:  true, // filebrowser
	9222:  true, // Chrome DevTools
	18789: true, // gateway
	9119:  true, // Hermes dashboard
}

func (ap *authProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Port proxying: no machine token required.
	// Cloudflare Access authenticates users at the edge before traffic
	// reaches the tunnel, so the auth proxy doesn't need to re-verify.
	if strings.HasPrefix(path, "/port/") {
		matches := portPathRe.FindStringSubmatch(path)
		if matches == nil {
			http.Error(w, `{"error":"invalid port path"}`, http.StatusBadRequest)
			return
		}
		portNum, err := strconv.Atoi(matches[1])
		if err != nil || portNum < 1024 || portNum > 65535 {
			http.Error(w, `{"error":"port must be 1024-65535"}`, http.StatusBadRequest)
			return
		}
		if reservedPorts[portNum] {
			slog.Warn("port.denied", "port", portNum, "path", path)
			http.Error(w, `{"error":"port not allowed"}`, http.StatusForbidden)
			return
		}
		target := fmt.Sprintf("http://127.0.0.1:%d", portNum)
		stripPrefix := fmt.Sprintf("/port/%d", portNum)
		ap.proxyTo(w, r, target, stripPrefix, nil)
		return
	}

	// File browser: no machine token required (CF Access handles edge auth).
	// Proxies to filebrowser running on port 8082 with --baseURL /files.
	if strings.HasPrefix(path, "/files") {
		ap.proxyTo(w, r, "http://127.0.0.1:8082", "", nil)
		return
	}

	// All other routes require a machine token
	token := r.Header.Get("X-Machine-Token")
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		slog.Warn("authproxy.auth_missing_token", "path", path, "method", r.Method, "host", r.Host)
		http.Error(w, `{"error":"missing token"}`, http.StatusUnauthorized)
		return
	}

	claims, err := auth.ValidateMachineToken(token, ap.signingKey, ap.machineID)
	if err != nil {
		slog.Warn("authproxy.auth_failed", "error", err, "path", path, "method", r.Method)
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	switch {
	case strings.HasPrefix(path, "/terminal"):
		if !claims.HasScope("terminal") {
			http.Error(w, `{"error":"insufficient scope"}`, http.StatusForbidden)
			return
		}
		ap.proxyTo(w, r, "http://127.0.0.1:7681", "/terminal", claims)

	case strings.HasPrefix(path, "/gateway"):
		if !claims.HasScope("gateway") {
			http.Error(w, `{"error":"insufficient scope"}`, http.StatusForbidden)
			return
		}
		if websocket.IsWebSocketUpgrade(r) {
			ap.proxyGatewayWebSocket(w, r)
			return
		}
		ap.proxyTo(w, r, "http://"+ap.gatewayAddr, "/gateway", claims)

	case strings.HasPrefix(path, "/dashboard"):
		if !claims.HasScope("gateway") {
			http.Error(w, `{"error":"insufficient scope"}`, http.StatusForbidden)
			return
		}
		if websocket.IsWebSocketUpgrade(r) {
			ap.proxyDashboardWebSocket(w, r)
			return
		}
		ap.proxyTo(w, r, "http://"+ap.dashboardAddr, "/dashboard", claims)

	default:
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}
}

// wsUpgrader for gateway WebSocket proxying.
var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true }, // auth validated via machine JWT
}

// proxyGatewayWebSocket proxies a WebSocket connection to the gateway,
// injecting the gateway token into the first (connect) message so the
// browser Control UI can authenticate without knowing the token.
func (ap *authProxy) proxyGatewayWebSocket(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Build upstream target URL
	path := strings.TrimPrefix(r.URL.Path, "/gateway")
	if path == "" {
		path = "/"
	}
	targetURL := "ws://" + ap.gatewayAddr + path
	// Strip the machine token query param before forwarding
	q := r.URL.Query()
	q.Del("token")
	if qs := q.Encode(); qs != "" {
		targetURL += "?" + qs
	}

	// Dial the upstream gateway
	dialHeaders := http.Header{
		"Origin": {"http://" + ap.gatewayAddr},
	}
	targetConn, _, err := websocket.DefaultDialer.Dial(targetURL, dialHeaders)
	if err != nil {
		slog.Error("authproxy.gateway_ws_dial_failed", "target", targetURL, "error", err)
		http.Error(w, `{"error":"gateway unavailable"}`, http.StatusBadGateway)
		return
	}
	defer func() { _ = targetConn.Close() }()

	// Upgrade client connection
	clientConn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("authproxy.gateway_ws_upgrade_failed", "error", err)
		return
	}
	defer func() { _ = clientConn.Close() }()

	slog.Info("authproxy.gateway_ws_connected", "target", targetURL)

	// Bidirectional proxy. The gateway sends a connect.challenge first,
	// then the client sends a connect request. We inject the gateway token
	// into the first text message from the client (the connect request).
	// We track this with injected to ensure only the first message is modified.
	injected := false
	result := proxyWebSocket(clientConn, targetConn, func(mt int, msg []byte) (int, []byte) {
		if !injected && mt == websocket.TextMessage {
			msg = injectAuthToken(msg, ap.gatewayToken)
			injected = true
		}
		return mt, msg
	})
	logWebSocketClose("authproxy.gateway_ws_closed", targetURL, start, result)
}

// injectAuthToken injects or overwrites auth.token inside the params object
// of a gateway WebSocket connect message. The gateway protocol expects:
//
//	{"type":"req","method":"connect","params":{"auth":{"token":"..."},...}}
//
// If the message is not valid JSON or has no params object, it is returned unmodified.
func injectAuthToken(msg []byte, token string) []byte {
	var obj map[string]any
	if err := json.Unmarshal(msg, &obj); err != nil {
		return msg // not JSON, pass through
	}

	// Get or create the params object
	paramsRaw, ok := obj["params"]
	if !ok {
		obj["params"] = map[string]any{"auth": map[string]any{"token": token}}
	} else if params, ok := paramsRaw.(map[string]any); ok {
		authRaw, ok := params["auth"]
		if !ok {
			params["auth"] = map[string]any{"token": token}
		} else if authMap, ok := authRaw.(map[string]any); ok {
			authMap["token"] = token
		} else {
			params["auth"] = map[string]any{"token": token}
		}
	} else {
		return msg // params is not an object, pass through
	}

	injected, err := json.Marshal(obj)
	if err != nil {
		return msg // marshal failed, pass through original
	}
	return injected
}

// proxyDashboardWebSocket proxies Hermes dashboard WebSocket connections
// through the VM auth proxy. The host agent authenticates to this proxy with
// X-Machine-Token and leaves the dashboard session token in the query string.
func (ap *authProxy) proxyDashboardWebSocket(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	path := strings.TrimPrefix(r.URL.Path, "/dashboard")
	if path == "" {
		path = "/"
	}
	targetURL := "ws://" + ap.dashboardAddr + path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	dialHeaders := http.Header{
		"Origin": {"http://" + ap.dashboardAddr},
	}
	if proto := r.Header.Get("Sec-WebSocket-Protocol"); proto != "" {
		dialHeaders.Set("Sec-WebSocket-Protocol", proto)
	}

	targetConn, targetResp, err := websocket.DefaultDialer.Dial(targetURL, dialHeaders)
	if err != nil {
		statusCode := 0
		if targetResp != nil {
			statusCode = targetResp.StatusCode
		}
		slog.Error("authproxy.dashboard_ws_dial_failed", "target", targetURL, "error", err, "upstream_status", statusCode)
		http.Error(w, `{"error":"dashboard unavailable"}`, http.StatusBadGateway)
		return
	}
	defer func() { _ = targetConn.Close() }()

	upgrader := wsUpgrader
	if targetResp != nil {
		if negotiated := targetResp.Header.Get("Sec-WebSocket-Protocol"); negotiated != "" {
			upgrader.Subprotocols = []string{negotiated}
		}
	}

	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("authproxy.dashboard_ws_upgrade_failed", "error", err)
		return
	}
	defer func() { _ = clientConn.Close() }()

	slog.Info("authproxy.dashboard_ws_connected", "target", targetURL)
	result := proxyPlainWebSocket(clientConn, targetConn)
	logWebSocketClose("authproxy.dashboard_ws_closed", targetURL, start, result)
}

type wsProxyResult struct {
	Direction string
	CloseCode int
	CloseText string
	Error     string
}

type wsProxyPeer struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

func newWSProxyPeer(conn *websocket.Conn) *wsProxyPeer {
	peer := &wsProxyPeer{conn: conn}
	_ = conn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
		return nil
	})
	return peer
}

func (p *wsProxyPeer) writeMessage(mt int, msg []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return p.conn.WriteMessage(mt, msg)
}

func (p *wsProxyPeer) writePing() error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	deadline := time.Now().Add(wsWriteTimeout)
	_ = p.conn.SetWriteDeadline(deadline)
	return p.conn.WriteControl(websocket.PingMessage, nil, deadline)
}

func proxyPlainWebSocket(client, target *websocket.Conn) wsProxyResult {
	return proxyWebSocket(client, target, nil)
}

func proxyWebSocket(client, target *websocket.Conn, transformClientMessage func(int, []byte) (int, []byte)) wsProxyResult {
	clientPeer := newWSProxyPeer(client)
	targetPeer := newWSProxyPeer(target)

	done := make(chan wsProxyResult, 3)
	stop := make(chan struct{})
	var stopOnce sync.Once
	stopPing := func() {
		stopOnce.Do(func() { close(stop) })
	}
	report := func(result wsProxyResult) {
		select {
		case done <- result:
		default:
		}
	}

	go func() {
		for {
			mt, msg, err := clientPeer.conn.ReadMessage()
			if err != nil {
				report(wsProxyCloseResult("client_to_target", err))
				return
			}
			_ = clientPeer.conn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
			if transformClientMessage != nil {
				mt, msg = transformClientMessage(mt, msg)
			}
			if err := targetPeer.writeMessage(mt, msg); err != nil {
				report(wsProxyCloseResult("client_to_target_write", err))
				return
			}
		}
	}()

	go func() {
		for {
			mt, msg, err := targetPeer.conn.ReadMessage()
			if err != nil {
				report(wsProxyCloseResult("target_to_client", err))
				return
			}
			_ = targetPeer.conn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
			if err := clientPeer.writeMessage(mt, msg); err != nil {
				report(wsProxyCloseResult("target_to_client_write", err))
				return
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(wsPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := clientPeer.writePing(); err != nil {
					report(wsProxyCloseResult("client_ping", err))
					return
				}
				if err := targetPeer.writePing(); err != nil {
					report(wsProxyCloseResult("target_ping", err))
					return
				}
			}
		}
	}()

	result := <-done
	stopPing()
	_ = client.Close()
	_ = target.Close()
	<-done
	return result
}

func wsProxyCloseResult(direction string, err error) wsProxyResult {
	result := wsProxyResult{Direction: direction}
	if err == nil {
		return result
	}
	result.Error = err.Error()
	if closeErr, ok := err.(*websocket.CloseError); ok {
		result.CloseCode = closeErr.Code
		result.CloseText = closeErr.Text
	}
	return result
}

func logWebSocketClose(message, target string, start time.Time, result wsProxyResult) {
	attrs := []any{
		"target", target,
		"duration_ms", time.Since(start).Milliseconds(),
		"first_close_direction", result.Direction,
		"close_code", result.CloseCode,
		"close_text", result.CloseText,
	}
	if result.Error != "" {
		attrs = append(attrs, "error", result.Error)
	}
	slog.Info(message, attrs...)
}

func (ap *authProxy) proxyGenericWebSocket(w http.ResponseWriter, r *http.Request, targetURL *url.URL, stripPrefix string, claims *auth.MachineTokenClaims, originalPath, originalHost, origin, rewrittenOrigin string) {
	start := time.Now()
	targetPath := strings.TrimPrefix(originalPath, stripPrefix)
	if targetPath == "" {
		targetPath = "/"
	}
	scheme := "ws"
	if targetURL.Scheme == "https" {
		scheme = "wss"
	}
	upstreamURL := url.URL{
		Scheme:   scheme,
		Host:     targetURL.Host,
		Path:     targetPath,
		RawQuery: r.URL.RawQuery,
	}

	dialHeaders := http.Header{
		"Origin": {rewrittenOrigin},
	}
	if proto := r.Header.Get("Sec-WebSocket-Protocol"); proto != "" {
		dialHeaders.Set("Sec-WebSocket-Protocol", proto)
	}
	if cookie := r.Header.Get("Cookie"); cookie != "" {
		dialHeaders.Set("Cookie", cookie)
	}
	if ua := r.Header.Get("User-Agent"); ua != "" {
		dialHeaders.Set("User-Agent", ua)
	}

	statusCode := http.StatusSwitchingProtocols
	targetConn, targetResp, err := websocket.DefaultDialer.Dial(upstreamURL.String(), dialHeaders)
	if err != nil {
		statusCode = http.StatusBadGateway
		upstreamStatus := 0
		if targetResp != nil {
			upstreamStatus = targetResp.StatusCode
		}
		slog.Error("authproxy.ws_dial_failed", "target", upstreamURL.String(), "error", err, "upstream_status", upstreamStatus)
		http.Error(w, `{"error":"upstream unavailable"}`, http.StatusBadGateway)
		ap.logProxyRequest("authproxy.request", r.Method, originalPath, targetURL.String(), claims, originalHost, origin, rewrittenOrigin, true, statusCode, start, wsProxyResult{})
		return
	}
	defer func() { _ = targetConn.Close() }()

	upgrader := wsUpgrader
	if targetResp != nil {
		if negotiated := targetResp.Header.Get("Sec-WebSocket-Protocol"); negotiated != "" {
			upgrader.Subprotocols = []string{negotiated}
		}
	}

	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("authproxy.ws_upgrade_failed", "target", upstreamURL.String(), "error", err)
		ap.logProxyRequest("authproxy.request", r.Method, originalPath, targetURL.String(), claims, originalHost, origin, rewrittenOrigin, true, 0, start, wsProxyResult{Error: err.Error()})
		return
	}
	defer func() { _ = clientConn.Close() }()

	slog.Info("authproxy.ws_connected", "target", upstreamURL.String())
	result := proxyPlainWebSocket(clientConn, targetConn)
	ap.logProxyRequest("authproxy.request", r.Method, originalPath, targetURL.String(), claims, originalHost, origin, rewrittenOrigin, true, statusCode, start, result)
}

func (ap *authProxy) logProxyRequest(message, method, path, target string, claims *auth.MachineTokenClaims, originalHost, origin, rewrittenOrigin string, isWebSocket bool, statusCode int, start time.Time, result wsProxyResult) {
	var userID int
	if claims != nil {
		userID = claims.UserID
	}
	attrs := []any{
		"method", method,
		"path", path,
		"target", target,
		"user_id", userID,
		"original_host", originalHost,
		"incoming_origin", origin,
		"rewritten_origin", rewrittenOrigin,
		"is_websocket", isWebSocket,
		"status", statusCode,
		"duration_ms", time.Since(start).Milliseconds(),
	}
	if isWebSocket {
		attrs = append(attrs,
			"first_close_direction", result.Direction,
			"close_code", result.CloseCode,
			"close_text", result.CloseText,
		)
		if result.Error != "" {
			attrs = append(attrs, "error", result.Error)
		}
	}
	slog.Info(message, attrs...)
}

func (ap *authProxy) proxyTo(w http.ResponseWriter, r *http.Request, target, stripPrefix string, claims *auth.MachineTokenClaims) {
	targetURL, err := url.Parse(target)
	if err != nil {
		slog.Error("authproxy.bad_target", "target", target, "error", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Strip the path prefix before forwarding
	originalPath := r.URL.Path
	originalHost := r.Host
	origin := r.Header.Get("Origin")
	isWebSocket := websocket.IsWebSocketUpgrade(r)

	r.URL.Path = strings.TrimPrefix(r.URL.Path, stripPrefix)
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}

	// Rewrite Origin to match the target Host so the gateway's origin-vs-host
	// check passes. The upstream proxy chain (cloudflared, Cloud Run) rewrites
	// Origin to the auth proxy's address (e.g. http://192.168.100.11:8080),
	// which doesn't match the gateway's Host → "origin not allowed".
	// Security is enforced at this auth proxy layer (machine JWT), not at the gateway.
	rewrittenOrigin := targetURL.Scheme + "://" + targetURL.Host

	// Log gateway-bound requests with origin debug info
	if strings.HasPrefix(originalPath, "/gateway") && claims != nil {
		slog.Info("authproxy.gateway_proxy",
			"original_host", originalHost,
			"forwarded_host", targetURL.Host,
			"incoming_origin", origin,
			"rewritten_origin", rewrittenOrigin,
			"is_websocket", isWebSocket,
			"user_id", claims.UserID,
		)
	}

	if isWebSocket {
		ap.proxyGenericWebSocket(w, r, targetURL, stripPrefix, claims, originalPath, originalHost, origin, rewrittenOrigin)
		return
	}

	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = targetURL.Scheme
		req.URL.Host = targetURL.Host
		req.Host = targetURL.Host
		req.Header.Set("Origin", rewrittenOrigin)

		// Strip proxy forwarding headers so the gateway sees this as a
		// direct local connection. Without this, isLocalDirectRequest()
		// returns false (hasForwarded=true, remoteIsTrustedProxy=false)
		// which makes isLocalClient=false, breaking the origin check's
		// local-loopback fallback path.
		req.Header.Del("X-Forwarded-For")
		req.Header.Del("X-Forwarded-Host")
		req.Header.Del("X-Forwarded-Proto")
		req.Header.Del("X-Real-Ip")

		// Pass through Upgrade/Connection headers for WebSocket
		if upgrade := r.Header.Get("Upgrade"); upgrade != "" {
			req.Header.Set("Upgrade", upgrade)
			req.Header.Set("Connection", r.Header.Get("Connection"))
		}
	}

	// Capture upstream status code for logging and rewrite Location headers
	// so redirects from the upstream service include the /port/{N} prefix.
	var statusCode int
	proxy.ModifyResponse = func(resp *http.Response) error {
		statusCode = resp.StatusCode
		if stripPrefix != "" {
			if loc := resp.Header.Get("Location"); loc != "" {
				if strings.HasPrefix(loc, "/") {
					resp.Header.Set("Location", stripPrefix+loc)
				}
			}
		}
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Error("authproxy.proxy_error", "target", target, "error", err, "path", originalPath)
		statusCode = http.StatusBadGateway
		http.Error(w, `{"error":"upstream unavailable"}`, http.StatusBadGateway)
	}

	start := time.Now()
	proxy.ServeHTTP(w, r)

	var userID int
	if claims != nil {
		userID = claims.UserID
	}
	slog.Info("authproxy.request",
		"method", r.Method,
		"path", originalPath,
		"target", target,
		"user_id", userID,
		"original_host", originalHost,
		"incoming_origin", origin,
		"rewritten_origin", rewrittenOrigin,
		"is_websocket", isWebSocket,
		"status", statusCode,
		"duration_ms", time.Since(start).Milliseconds(),
	)
}
