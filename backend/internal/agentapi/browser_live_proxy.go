package agentapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// kernelBrowserLivePort is the port Neko (WebRTC browser streaming server)
// listens on inside the browser VM. Neko serves both the HTML shell at
// /var/www and the WebSocket signalling on the same port.
const kernelBrowserLivePort = 8080

func (s *Server) handleBrowserVMLiveProxy(w http.ResponseWriter, r *http.Request) {
	bvmID := chi.URLParam(r, "browserVmId")
	bvmIP, ok := s.lookupBrowserVMIP(r, bvmID)
	if !ok {
		http.Error(w, "browser VM not found on this host", http.StatusNotFound)
		return
	}

	routePath := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	if websocket.IsWebSocketUpgrade(r) {
		targetURL := fmt.Sprintf("ws://%s/%s", liveViewHost(bvmIP), routePath)
		if r.URL.RawQuery != "" {
			targetURL += "?" + r.URL.RawQuery
		}
		proxyBrowserLiveWebSocket(w, r, targetURL)
		return
	}

	target := &url.URL{
		Scheme: "http",
		Host:   liveViewHost(bvmIP),
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = "/" + routePath
		req.Host = target.Host
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		stripFrameDenyHeaders(resp.Header)
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Warn("browser_vm.live_proxy.http_failed", "browser_vm_id", bvmID, "target", target.String(), "error", err)
		http.Error(w, "browser live view unavailable", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

func (s *Server) lookupBrowserVMIP(r *http.Request, bvmID string) (string, bool) {
	bvms, err := s.orchestrator.ListBrowserVMs(r.Context())
	if err != nil {
		slog.Warn("browser_vm.live_proxy.list_failed", "browser_vm_id", bvmID, "error", err)
		return "", false
	}
	for _, bvm := range bvms {
		if bvm.ID == bvmID && bvm.VMIP != "" {
			return bvm.VMIP, true
		}
	}
	return "", false
}

func liveViewHost(ip string) string {
	return fmt.Sprintf("%s:%d", ip, kernelBrowserLivePort)
}

func proxyBrowserLiveWebSocket(w http.ResponseWriter, r *http.Request, targetURL string) {
	dialHeaders := make(http.Header)
	if proto := r.Header.Get("Sec-WebSocket-Protocol"); proto != "" {
		dialHeaders.Set("Sec-WebSocket-Protocol", proto)
	}
	if u, parseErr := url.Parse(targetURL); parseErr == nil {
		dialHeaders.Set("Origin", "http://"+u.Host)
	}

	dialer := websocket.Dialer{
		Proxy: http.ProxyFromEnvironment,
	}
	targetConn, targetResp, err := dialer.Dial(targetURL, dialHeaders)
	if err != nil {
		statusCode := 0
		if targetResp != nil {
			statusCode = targetResp.StatusCode
		}
		slog.Error("browser_vm.live_proxy.ws_connect_failed", "target", targetURL, "error", err, "upstream_status", statusCode)
		http.Error(w, "failed to connect to browser live view", http.StatusBadGateway)
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
		slog.Error("browser_vm.live_proxy.ws_client_upgrade_failed", "target", targetURL, "error", err)
		return
	}
	defer func() { _ = clientConn.Close() }()

	start := time.Now()
	proxyWebSocketBidirectional(clientConn, targetConn)
	slog.Info("browser_vm.live_proxy.ws_closed", "target", targetURL, "duration_s", int(time.Since(start).Seconds()))
}

func stripFrameDenyHeaders(h http.Header) {
	h.Del("X-Frame-Options")
	h.Del("Content-Security-Policy")
}
