package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

func (s *Server) handleBrowserVMLiveProxy(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	bvmID := chi.URLParam(r, "browserVmId")

	bvm, err := s.store.GetBrowserVM(r.Context(), bvmID)
	if err != nil {
		writeError(w, http.StatusNotFound, "browser VM not found")
		return
	}
	if bvm.AccountID != accountID {
		writeError(w, http.StatusForbidden, "browser VM does not belong to this account")
		return
	}
	if bvm.Status != "running" {
		writeError(w, http.StatusConflict, "browser VM is not running")
		return
	}
	if bvm.HostID == nil {
		writeError(w, http.StatusConflict, "browser VM is not assigned to a host")
		return
	}

	host, err := s.store.GetHost(r.Context(), *bvm.HostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "host not found")
		return
	}
	if s.agentClient == nil {
		writeError(w, http.StatusServiceUnavailable, "agent client is not configured")
		return
	}

	// Resolve the host's proxy base URL via agentClient (respects AgentEndpoint,
	// falling back to ExternalIP/InternalIP). Then strip the scheme so we can
	// build ws:// / http:// URLs below.
	proxyBase := s.agentClient.ProxyURL(host)
	proxyBase = strings.TrimSuffix(proxyBase, "/")
	proxyBaseNoScheme := strings.TrimPrefix(strings.TrimPrefix(proxyBase, "http://"), "https://")

	routePath := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	agentPath := fmt.Sprintf("%s/browser-vms/%s/live/%s", proxyBaseNoScheme, bvmID, routePath)

	if websocket.IsWebSocketUpgrade(r) {
		upstreamURL := "ws://" + agentPath
		if strings.HasPrefix(proxyBase, "https://") {
			upstreamURL = "wss://" + agentPath
		}
		if r.URL.RawQuery != "" {
			upstreamURL += "?" + r.URL.RawQuery
		}
		header := http.Header{
			"Authorization": {"Bearer " + s.agentClient.TokenForHost(host)},
		}
		// Forward client's requested WebSocket subprotocols (e.g., binary/base64
		// for noVNC/websockify clients) so the server can negotiate correctly.
		if proto := r.Header.Get("Sec-WebSocket-Protocol"); proto != "" {
			header.Set("Sec-WebSocket-Protocol", proto)
		}
		slog.Info("browser_vm.live.ws.upgrade", "browser_vm_id", bvmID, "target", upstreamURL)
		s.proxyBrowserLiveWebSocket(w, r, bvmID, upstreamURL, header)
		return
	}

	upstreamURL := proxyBase + "/browser-vms/" + bvmID + "/live/" + routePath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create live-view request")
		return
	}
	for key, values := range r.Header {
		switch strings.ToLower(key) {
		case "authorization", "cookie", "host":
			continue
		default:
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	}
	req.Header.Set("Authorization", "Bearer "+s.agentClient.TokenForHost(host))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("browser_vm.live.http_failed", "browser_vm_id", bvmID, "target", upstreamURL, "error", err)
		writeError(w, http.StatusBadGateway, "browser live view unavailable")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	copyProxyHeaders(w.Header(), resp.Header)
	stripFrameHeaders(w.Header())
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (s *Server) proxyBrowserLiveWebSocket(w http.ResponseWriter, r *http.Request, bvmID, upstreamURL string, header http.Header) {
	// Dial upstream first so we know what subprotocol (if any) the upstream
	// negotiated, then advertise that when upgrading the client connection.
	upstreamConn, resp, err := websocket.DefaultDialer.Dial(upstreamURL, header)
	if err != nil {
		slog.Error("browser_vm.live.ws_connect_failed", "browser_vm_id", bvmID, "target", upstreamURL, "error", err)
		if resp != nil && resp.Body != nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			slog.Error("browser_vm.live.ws_upstream_response", "browser_vm_id", bvmID, "response_body", string(body))
		}
		http.Error(w, "browser live view unavailable", http.StatusBadGateway)
		return
	}
	defer func() { _ = upstreamConn.Close() }()

	// Honor the negotiated upstream subprotocol when upgrading the client.
	upgrader := s.wsUpgrader
	if resp != nil {
		if negotiated := resp.Header.Get("Sec-WebSocket-Protocol"); negotiated != "" {
			upgrader.Subprotocols = []string{negotiated}
		}
	}
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("browser_vm.live.ws.upgrade_failed", "browser_vm_id", bvmID, "error", err)
		return
	}
	defer func() { _ = clientConn.Close() }()

	_ = clientConn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
	clientConn.SetPongHandler(func(string) error {
		_ = clientConn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
		return nil
	})
	_ = upstreamConn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
	upstreamConn.SetPongHandler(func(string) error {
		_ = upstreamConn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
		return nil
	})

	ticker := time.NewTicker(wsPingInterval)
	errc := make(chan error, 2)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := clientConn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					return
				}
				if err := upstreamConn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					return
				}
			}
		}
	}()
	go func() {
		for {
			msgType, msg, err := clientConn.ReadMessage()
			if err != nil {
				logWSClose(bvmID, "browser_live", "client", err)
				errc <- err
				return
			}
			_ = clientConn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
			if err := upstreamConn.WriteMessage(msgType, msg); err != nil {
				errc <- err
				return
			}
		}
	}()
	go func() {
		for {
			msgType, msg, err := upstreamConn.ReadMessage()
			if err != nil {
				logWSClose(bvmID, "browser_live", "upstream", err)
				errc <- err
				return
			}
			_ = upstreamConn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
			if err := clientConn.WriteMessage(msgType, msg); err != nil {
				errc <- err
				return
			}
		}
	}()

	<-errc
	ticker.Stop()
	close(stop)
}

func copyProxyHeaders(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func stripFrameHeaders(h http.Header) {
	h.Del("X-Frame-Options")
	h.Del("Content-Security-Policy")
}
