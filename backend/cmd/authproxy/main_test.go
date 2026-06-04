package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
)

func mintTestMachineToken(t *testing.T, signingKey, machineID string, scopes []string) string {
	t.Helper()
	token, _, err := auth.IssueMachineToken(0, "test@test.com", machineID, 0, scopes, signingKey)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return token
}

// newMockGatewayWS starts a mock WebSocket server that mimics the real gateway protocol:
// 1. Server sends connect.challenge
// 2. Client sends connect request (captured and returned via channel)
// 3. Server sends response
func newMockGatewayWS(t *testing.T) (*httptest.Server, <-chan []byte) {
	t.Helper()
	receivedCh := make(chan []byte, 1)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Step 1: send connect.challenge (like the real gateway)
		challenge := `{"type":"event","event":"connect.challenge","payload":{"nonce":"test-nonce","ts":1234}}`
		_ = conn.WriteMessage(websocket.TextMessage, []byte(challenge))
		// Step 2: read the client's connect request
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		receivedCh <- msg
		// Step 3: send response
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"res","ok":true}`))
	}))
	t.Cleanup(gw.Close)
	return gw, receivedCh
}

func newMockDashboardWS(t *testing.T) (*httptest.Server, <-chan *http.Request) {
	t.Helper()
	requestCh := make(chan *http.Request, 1)
	dash := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		requestCh <- r.Clone(r.Context())
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ready"}`))
	}))
	t.Cleanup(dash.Close)
	return dash, requestCh
}

func TestDashboardWebSocketProxy(t *testing.T) {
	dash, requestCh := newMockDashboardWS(t)

	signingKey := "test-signing-key-1234567890abcdef"
	machineID := "test-machine"
	ap := &authProxy{
		signingKey:    signingKey,
		machineID:     machineID,
		gatewayToken:  "unused-gateway-token",
		dashboardAddr: strings.TrimPrefix(dash.URL, "http://"),
	}

	srv := httptest.NewServer(ap)
	defer srv.Close()

	machineToken := mintTestMachineToken(t, signingKey, machineID, []string{"gateway"})
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/dashboard/api/ws?token=session-token"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"X-Machine-Token": {machineToken},
	})
	if err != nil {
		t.Fatalf("ws dial failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read dashboard message: %v", err)
	}
	if string(msg) != `{"type":"ready"}` {
		t.Fatalf("dashboard message = %s, want ready event", msg)
	}

	select {
	case req := <-requestCh:
		if req.URL.Path != "/api/ws" {
			t.Errorf("dashboard path = %q, want /api/ws", req.URL.Path)
		}
		if got := req.URL.Query().Get("token"); got != "session-token" {
			t.Errorf("dashboard session token = %q, want session-token", got)
		}
		if got := req.Header.Get("Origin"); got != "http://"+ap.dashboardAddr {
			t.Errorf("dashboard Origin = %q, want %q", got, "http://"+ap.dashboardAddr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for dashboard websocket request")
	}
}

func TestGenericWebSocketProxySendsKeepAlivePing(t *testing.T) {
	oldPingInterval := wsPingInterval
	oldPongTimeout := wsPongTimeout
	oldWriteTimeout := wsWriteTimeout
	wsPingInterval = 20 * time.Millisecond
	wsPongTimeout = 200 * time.Millisecond
	wsWriteTimeout = 200 * time.Millisecond
	t.Cleanup(func() {
		wsPingInterval = oldPingInterval
		wsPongTimeout = oldPongTimeout
		wsWriteTimeout = oldWriteTimeout
	})

	requestCh := make(chan *http.Request, 1)
	pingCh := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		requestCh <- r.Clone(r.Context())
		conn.SetPingHandler(func(appData string) error {
			select {
			case pingCh <- struct{}{}:
			default:
			}
			return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(upstream.Close)

	ap := &authProxy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ap.proxyTo(w, r, upstream.URL, "/terminal", nil)
	}))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/terminal/ws?token=session-token"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial failed: %v", err)
	}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		select {
		case <-readDone:
		case <-time.After(time.Second):
			t.Error("timed out waiting for websocket reader to exit")
		}
	})

	select {
	case req := <-requestCh:
		if req.URL.Path != "/ws" {
			t.Errorf("upstream websocket path = %q, want /ws", req.URL.Path)
		}
		if got := req.URL.Query().Get("token"); got != "session-token" {
			t.Errorf("upstream token query = %q, want session-token", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for upstream websocket request")
	}

	select {
	case <-pingCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for upstream keepalive ping")
	}
}

func TestGatewayWSTokenInjection(t *testing.T) {
	gw, receivedCh := newMockGatewayWS(t)

	signingKey := "test-signing-key-1234567890abcdef"
	machineID := "test-machine"
	gatewayToken := "test-gateway-token-secret"
	ap := &authProxy{
		signingKey:   signingKey,
		machineID:    machineID,
		gatewayToken: gatewayToken,
		gatewayAddr:  strings.TrimPrefix(gw.URL, "http://"),
	}

	srv := httptest.NewServer(ap)
	defer srv.Close()

	token := mintTestMachineToken(t, signingKey, machineID, []string{"gateway"})
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/gateway?token=" + token
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Read the challenge from the mock gateway (forwarded via proxy)
	_, _, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read challenge: %v", err)
	}

	// Send a connect message without auth.token (gateway protocol nests auth inside params)
	connectMsg := `{"type":"req","method":"connect","params":{"client":{"id":"control-ui"}}}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(connectMsg)); err != nil {
		t.Fatalf("ws write failed: %v", err)
	}

	select {
	case receivedMsg := <-receivedCh:
		var parsed map[string]any
		if err := json.Unmarshal(receivedMsg, &parsed); err != nil {
			t.Fatalf("failed to parse forwarded message: %v", err)
		}
		params, ok := parsed["params"].(map[string]any)
		if !ok {
			t.Fatal("forwarded message missing 'params' object")
		}
		authObj, ok := params["auth"].(map[string]any)
		if !ok {
			t.Fatal("forwarded message missing 'params.auth' object")
		}
		if authObj["token"] != gatewayToken {
			t.Errorf("params.auth.token = %q, want %q", authObj["token"], gatewayToken)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for mock gateway to receive message")
	}
}

func TestGatewayWSTokenOverwrite(t *testing.T) {
	gw, receivedCh := newMockGatewayWS(t)

	signingKey := "test-signing-key-1234567890abcdef"
	machineID := "test-machine"
	gatewayToken := "real-token"
	ap := &authProxy{
		signingKey:   signingKey,
		machineID:    machineID,
		gatewayToken: gatewayToken,
		gatewayAddr:  strings.TrimPrefix(gw.URL, "http://"),
	}

	srv := httptest.NewServer(ap)
	defer srv.Close()

	token := mintTestMachineToken(t, signingKey, machineID, []string{"gateway"})
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/gateway?token=" + token
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Read the challenge first
	_, _, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read challenge: %v", err)
	}

	// Send message WITH an existing params.auth.token — should be overwritten
	connectMsg := `{"type":"req","method":"connect","params":{"auth":{"token":"client-fake-token"},"client":{"id":"control-ui"}}}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(connectMsg)); err != nil {
		t.Fatalf("ws write failed: %v", err)
	}

	select {
	case receivedMsg := <-receivedCh:
		var parsed map[string]any
		if err := json.Unmarshal(receivedMsg, &parsed); err != nil {
			t.Fatalf("failed to parse: %v", err)
		}
		params := parsed["params"].(map[string]any)
		authObj := params["auth"].(map[string]any)
		if authObj["token"] != gatewayToken {
			t.Errorf("params.auth.token = %q, want %q (should overwrite client token)", authObj["token"], gatewayToken)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
}

func TestGatewayWSBinaryPassthrough(t *testing.T) {
	gw, receivedCh := newMockGatewayWS(t)

	signingKey := "test-signing-key-1234567890abcdef"
	machineID := "test-machine"
	ap := &authProxy{
		signingKey:   signingKey,
		machineID:    machineID,
		gatewayToken: "some-token",
		gatewayAddr:  strings.TrimPrefix(gw.URL, "http://"),
	}

	srv := httptest.NewServer(ap)
	defer srv.Close()

	token := mintTestMachineToken(t, signingKey, machineID, []string{"gateway"})
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/gateway?token=" + token
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Read the challenge first (text message from gateway)
	_, _, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read challenge: %v", err)
	}

	// Send binary message — should pass through unmodified
	binaryData := []byte{0x00, 0x01, 0x02, 0xFF}
	if err := conn.WriteMessage(websocket.BinaryMessage, binaryData); err != nil {
		t.Fatalf("ws write failed: %v", err)
	}

	select {
	case receivedMsg := <-receivedCh:
		if !bytes.Equal(receivedMsg, binaryData) {
			t.Errorf("binary message modified: got %v, want %v", receivedMsg, binaryData)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
}

func TestInjectAuthToken(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		token    string
		wantAuth string
	}{
		{
			name:     "adds auth when params exists but no auth",
			input:    `{"type":"req","method":"connect","params":{"client":{"id":"ui"}}}`,
			token:    "secret",
			wantAuth: "secret",
		},
		{
			name:     "adds params and auth when both missing",
			input:    `{"type":"req","method":"connect"}`,
			token:    "secret",
			wantAuth: "secret",
		},
		{
			name:     "overwrites existing token in params",
			input:    `{"type":"req","method":"connect","params":{"auth":{"token":"old"},"client":{"id":"ui"}}}`,
			token:    "new",
			wantAuth: "new",
		},
		{
			name:     "replaces non-object auth in params",
			input:    `{"type":"req","method":"connect","params":{"auth":"bad"}}`,
			token:    "fixed",
			wantAuth: "fixed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := injectAuthToken([]byte(tt.input), tt.token)
			var parsed map[string]any
			if err := json.Unmarshal(result, &parsed); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}
			params, ok := parsed["params"].(map[string]any)
			if !ok {
				t.Fatal("result missing params object")
			}
			authObj, ok := params["auth"].(map[string]any)
			if !ok {
				t.Fatal("result missing params.auth object")
			}
			if authObj["token"] != tt.wantAuth {
				t.Errorf("token = %q, want %q", authObj["token"], tt.wantAuth)
			}
		})
	}
}

func TestInjectAuthTokenInvalidJSON(t *testing.T) {
	input := []byte("not json at all")
	result := injectAuthToken(input, "token")
	if !bytes.Equal(result, input) {
		t.Errorf("invalid JSON should pass through unmodified")
	}
}

func TestPortDenylist(t *testing.T) {
	ap := &authProxy{signingKey: "test-key", machineID: "test-machine"}

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		// Reserved ports must return 403
		{"terminal port blocked", "/port/7681", http.StatusForbidden},
		{"auth proxy port blocked", "/port/8080", http.StatusForbidden},
		{"agent API blocked", "/port/9090", http.StatusForbidden},
		{"agent metrics blocked", "/port/9091", http.StatusForbidden},
		{"devtools blocked", "/port/9222", http.StatusForbidden},
		{"gateway blocked", "/port/18789", http.StatusForbidden},

		// Reserved ports with subpaths
		{"terminal with subpath blocked", "/port/7681/ws", http.StatusForbidden},
		{"gateway with subpath blocked", "/port/18789/api/v1", http.StatusForbidden},

		// Non-reserved user ports should NOT get 403
		// (they may get 502/other status because nothing is listening, which is fine)
		{"user port 3000 allowed", "/port/3000", 0},
		{"user port 5173 allowed", "/port/5173", 0},
		{"user port 4000 allowed", "/port/4000", 0},

		// Out-of-range ports still return 400
		{"port below range", "/port/80", http.StatusBadRequest},
		{"port above range", "/port/70000", http.StatusBadRequest},

		// Invalid port path
		{"invalid port path", "/port/abc", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()

			ap.ServeHTTP(rec, req)

			if tt.wantStatus == 0 {
				// wantStatus 0 means "any status except 403"
				if rec.Code == http.StatusForbidden {
					t.Errorf("GET %s: got 403 Forbidden, want any non-403 status; body: %s",
						tt.path, rec.Body.String())
				}
			} else if rec.Code != tt.wantStatus {
				t.Errorf("GET %s: got status %d, want %d; body: %s",
					tt.path, rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}
