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

func (s *Server) handleTerminalProxy(w http.ResponseWriter, r *http.Request) {
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

	// Determine host IP (prefer external — Cloud Run has no VPC connector)
	var hostIP string
	if host.ExternalIP != nil {
		hostIP = *host.ExternalIP
	} else if host.InternalIP != nil {
		hostIP = *host.InternalIP
	} else {
		writeError(w, http.StatusServiceUnavailable, "host has no reachable IP")
		return
	}

	// Extract the wildcard path after /terminal/
	routePath := chi.URLParam(r, "*")
	if routePath == "" {
		routePath = "/"
	}

	// Build upstream WebSocket URL to agent's terminal proxy
	upstreamURL := fmt.Sprintf("ws://%s:9091/proxy/%s/terminal/%s",
		hostIP, machine.ID, strings.TrimPrefix(routePath, "/"))
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	// Build headers for upstream
	header := http.Header{}
	if machine.ProxyToken != nil {
		header.Set("X-Proxy-Token", *machine.ProxyToken)
	}

	// Upgrade the client connection
	clientConn, err := s.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("proxy.terminal.ws.upgrade.failed", "machine_id", id, "error", err)
		return
	}
	defer func() { _ = clientConn.Close() }()

	// Connect to the upstream agent
	upstreamConn, resp, err := websocket.DefaultDialer.Dial(upstreamURL, header)
	if err != nil {
		slog.Error("proxy.terminal.upstream.connect.failed", "machine_id", id, "error", err)
		if resp != nil && resp.Body != nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			slog.Error("proxy.terminal.upstream.response", "machine_id", id, "response_body", string(body))
		}
		_ = clientConn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "upstream unavailable"))
		return
	}
	defer func() { _ = upstreamConn.Close() }()

	// Setup ping/pong keepalive to prevent Cloudflare idle timeout (~100s)
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

	// Client -> Upstream
	go func() {
		for {
			msgType, msg, err := clientConn.ReadMessage()
			if err != nil {
				logWSClose(id, "terminal", "client", err)
				errc <- err
				return
			}
			_ = clientConn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
			if err := upstreamConn.WriteMessage(msgType, msg); err != nil {
				logWSClose(id, "terminal", "upstream_write", err)
				errc <- err
				return
			}
		}
	}()

	// Upstream -> Client
	go func() {
		for {
			msgType, msg, err := upstreamConn.ReadMessage()
			if err != nil {
				logWSClose(id, "terminal", "upstream", err)
				errc <- err
				return
			}
			_ = upstreamConn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
			if err := clientConn.WriteMessage(msgType, msg); err != nil {
				logWSClose(id, "terminal", "client_write", err)
				errc <- err
				return
			}
		}
	}()

	// Wait for either direction to error/close
	<-errc
	ticker.Stop()
	close(stop)
}
