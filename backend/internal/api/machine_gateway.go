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

const (
	wsPingInterval = 25 * time.Second
	wsPongTimeout  = 10 * time.Second
)

// logWSClose logs a structured WebSocket close event with direction and reason.
// direction is one of: "client", "upstream", "client_write", "upstream_write".
func logWSClose(machineID, proxy, direction string, err error) {
	closeCode := 0
	closeText := ""
	reason := "connection_lost"

	if closeErr, ok := err.(*websocket.CloseError); ok {
		closeCode = closeErr.Code
		closeText = closeErr.Text
		switch closeErr.Code {
		case websocket.CloseNormalClosure:
			reason = "normal_close"
		case websocket.CloseGoingAway:
			reason = "going_away"
		default:
			reason = "abnormal_close"
		}
	} else if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
		reason = "read_deadline"
	}

	if reason == "normal_close" || reason == "going_away" {
		slog.Info("proxy.ws.closed",
			"machine_id", machineID, "proxy", proxy,
			"direction", direction, "reason", reason)
	} else {
		slog.Warn("proxy.ws.closed",
			"machine_id", machineID, "proxy", proxy,
			"direction", direction, "reason", reason,
			"close_code", closeCode, "close_text", closeText,
			"error", err.Error())
	}
}

func (s *Server) handleGatewayProxy(w http.ResponseWriter, r *http.Request) {
	s.handleMachineServiceProxy(w, r, "gateway", "")
}

func (s *Server) handleDashboardProxy(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	id := chi.URLParam(r, "id")
	s.handleMachineServiceProxy(w, r, "dashboard", fmt.Sprintf("/api/accounts/%d/machines/%s/dashboard", accountID, id))
}

// handleMachineServiceProxy proxies HTTP and WebSocket requests to a VM service
// through the host agent. This enables dev-mode access via the control plane;
// in production, traffic goes through subdomain routing.
func (s *Server) handleMachineServiceProxy(w http.ResponseWriter, r *http.Request, service string, forwardedPrefix string) {
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

	if machine.HostID == nil {
		writeError(w, http.StatusBadRequest, "machine is not assigned to a host")
		return
	}

	host, err := s.store.GetHost(r.Context(), *machine.HostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "host not found")
		return
	}

	var hostIP string
	if host.ExternalIP != nil {
		hostIP = *host.ExternalIP
	} else if host.InternalIP != nil {
		hostIP = *host.InternalIP
	} else {
		writeError(w, http.StatusServiceUnavailable, "host has no reachable IP")
		return
	}

	routePath := chi.URLParam(r, "*")

	// Build upstream URL to the agent's proxy API
	agentBase := fmt.Sprintf("%s:9091/proxy/%s/%s/%s", hostIP, machine.ID, service, routePath)

	// WebSocket upgrade
	if websocket.IsWebSocketUpgrade(r) {
		upstreamURL := fmt.Sprintf("ws://%s", agentBase)
		if r.URL.RawQuery != "" {
			upstreamURL += "?" + r.URL.RawQuery
		}

		header := http.Header{}
		if machine.ProxyToken != nil {
			header.Set("X-Proxy-Token", *machine.ProxyToken)
		}
		if forwardedPrefix != "" {
			header.Set("X-Forwarded-Prefix", forwardedPrefix)
		}

		slog.Info("proxy.machine.ws.upgrade", "machine_id", id, "service", service, "target", upstreamURL)

		clientConn, err := s.wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("proxy.machine.ws.upgrade.failed", "machine_id", id, "service", service, "error", err)
			return
		}
		defer func() { _ = clientConn.Close() }()

		upstreamConn, resp, err := websocket.DefaultDialer.Dial(upstreamURL, header)
		if err != nil {
			slog.Error("proxy.machine.upstream.connect.failed", "machine_id", id, "service", service, "error", err)
			if resp != nil && resp.Body != nil {
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				slog.Error("proxy.machine.upstream.response", "machine_id", id, "service", service, "response_body", string(body))
			}
			_ = clientConn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "upstream unavailable"))
			return
		}
		defer func() { _ = upstreamConn.Close() }()

		// Setup ping/pong keepalive on client connection
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
		// Ping sender — pings both browser and upstream to keep both halves alive
		// through Cloudflare's ~100s idle timeout
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
					logWSClose(id, service, "client", err)
					errc <- err
					return
				}
				_ = clientConn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
				if err := upstreamConn.WriteMessage(msgType, msg); err != nil {
					logWSClose(id, service, "upstream_write", err)
					errc <- err
					return
				}
			}
		}()
		go func() {
			for {
				msgType, msg, err := upstreamConn.ReadMessage()
				if err != nil {
					logWSClose(id, service, "upstream", err)
					errc <- err
					return
				}
				_ = upstreamConn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
				if err := clientConn.WriteMessage(msgType, msg); err != nil {
					logWSClose(id, service, "client_write", err)
					errc <- err
					return
				}
			}
		}()
		<-errc
		ticker.Stop()
		close(stop)
		return
	}

	// HTTP proxy
	upstreamURL := fmt.Sprintf("http://%s", agentBase)
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create request")
		return
	}

	// Copy headers, adding proxy token
	for key, values := range r.Header {
		switch strings.ToLower(key) {
		case "authorization", "cookie":
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if machine.ProxyToken != nil {
		req.Header.Set("X-Proxy-Token", *machine.ProxyToken)
	}
	if forwardedPrefix != "" {
		req.Header.Set("X-Forwarded-Prefix", forwardedPrefix)
	}

	resp, err := client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to reach "+service)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Copy response headers, stripping iframe restrictions
	for key, values := range resp.Header {
		switch strings.ToLower(key) {
		case "x-frame-options":
			continue
		case "content-security-policy":
			for _, value := range values {
				cleaned := strings.Replace(value, "frame-ancestors 'none'", "frame-ancestors 'self' openclawmachines.com *.openclawmachines.com", 1)
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
