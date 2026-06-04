package agentapi

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestProxyWebSocket_BidirectionalMessages(t *testing.T) {
	// Create a target WebSocket server (simulates VM gateway)
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("target upgrade error: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		// Echo messages back
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(msgType, msg); err != nil {
				return
			}
		}
	}))
	defer targetServer.Close()

	// Create proxy server that uses proxyBrowserWebSocket
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetURL := "ws" + targetServer.URL[4:] // http → ws
		proxyBrowserWebSocket(w, r, targetURL, nil)
	}))
	defer proxyServer.Close()

	// Connect client to proxy
	wsURL := "ws" + proxyServer.URL[4:]
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Send message and verify echo
	testMsg := "hello proxy"
	if err := client.WriteMessage(websocket.TextMessage, []byte(testMsg)); err != nil {
		t.Fatalf("client write: %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	msgType, msg, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	if msgType != websocket.TextMessage || string(msg) != testMsg {
		t.Fatalf("expected text %q, got type=%d msg=%q", testMsg, msgType, string(msg))
	}
}

func TestProxyWebSocket_CleanClose(t *testing.T) {
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Read until close
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer targetServer.Close()

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetURL := "ws" + targetServer.URL[4:]
		proxyBrowserWebSocket(w, r, targetURL, nil)
	}))
	defer proxyServer.Close()

	wsURL := "ws" + proxyServer.URL[4:]
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}

	// Send close message
	err = client.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	if err != nil {
		t.Fatalf("client close: %v", err)
	}

	// Should receive close back (or connection closed)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = client.ReadMessage()
	if err == nil {
		t.Fatal("expected error after close, got nil")
	}
	// The error should be a close error
	if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		// It's OK if it's just a connection close too
		t.Logf("close error (acceptable): %v", err)
	}
}

func TestProxyWebSocket_MultipleMessages(t *testing.T) {
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			// Echo with prefix
			if err := conn.WriteMessage(msgType, append([]byte("echo:"), msg...)); err != nil {
				return
			}
		}
	}))
	defer targetServer.Close()

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetURL := "ws" + targetServer.URL[4:]
		proxyBrowserWebSocket(w, r, targetURL, nil)
	}))
	defer proxyServer.Close()

	wsURL := "ws" + proxyServer.URL[4:]
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	messages := []string{"msg1", "msg2", "msg3", "msg4", "msg5"}
	for _, m := range messages {
		if err := client.WriteMessage(websocket.TextMessage, []byte(m)); err != nil {
			t.Fatalf("write %s: %v", m, err)
		}

		_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, msg, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("read for %s: %v", m, err)
		}
		expected := "echo:" + m
		if string(msg) != expected {
			t.Fatalf("expected %q, got %q", expected, string(msg))
		}
	}
}

func TestProxyWebSocket_PingsSilentTarget(t *testing.T) {
	oldPingInterval := wsPingInterval
	oldPongTimeout := wsPongTimeout
	wsPingInterval = 25 * time.Millisecond
	wsPongTimeout = 100 * time.Millisecond
	defer func() {
		wsPingInterval = oldPingInterval
		wsPongTimeout = oldPongTimeout
	}()

	targetPing := make(chan struct{}, 1)
	var targetPingOnce sync.Once
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		defaultPingHandler := conn.PingHandler()
		conn.SetPingHandler(func(appData string) error {
			targetPingOnce.Do(func() { targetPing <- struct{}{} })
			return defaultPingHandler(appData)
		})
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(msgType, msg); err != nil {
				return
			}
		}
	}))
	defer targetServer.Close()

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetURL := "ws" + targetServer.URL[4:]
		proxyBrowserWebSocket(w, r, targetURL, nil)
	}))
	defer proxyServer.Close()

	wsURL := "ws" + proxyServer.URL[4:]
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	clientMessages := make(chan string, 1)
	clientErrs := make(chan error, 1)
	go func() {
		for {
			_, msg, err := client.ReadMessage()
			if err != nil {
				clientErrs <- err
				return
			}
			clientMessages <- string(msg)
		}
	}()

	select {
	case <-targetPing:
	case err := <-clientErrs:
		t.Fatalf("client closed before target ping: %v", err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for proxy to ping silent target")
	}

	time.Sleep(wsPingInterval + wsPongTimeout + 25*time.Millisecond)
	if err := client.WriteMessage(websocket.TextMessage, []byte("still-open")); err != nil {
		t.Fatalf("client write after target keepalive window: %v", err)
	}

	select {
	case msg := <-clientMessages:
		if msg != "still-open" {
			t.Fatalf("echo = %q, want still-open", msg)
		}
	case err := <-clientErrs:
		t.Fatalf("client closed before echo: %v", err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for echo after target keepalive window")
	}
}
