# Auth Proxy Gateway Token Injection — Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the Control UI auth failure after OpenClaw v2026.3.12 upgrade by having the in-VM auth proxy inject the gateway token into the WebSocket connect message before forwarding to the gateway.

**Architecture:** The auth proxy (`backend/cmd/authproxy`) currently uses `httputil.ReverseProxy` for all paths, including WebSocket. For the `/gateway` path, we replace the generic reverse proxy with a `gorilla/websocket`-based proxy that intercepts the first client message, injects `auth.token` with the gateway token, and then pipes all subsequent frames bidirectionally. This follows the same pattern as `proxyBrowserWebSocket` in `backend/internal/agentapi/proxy.go`.

**Tech Stack:** Go, `gorilla/websocket` (already in `go.mod`)

---

## File Map

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `backend/cmd/authproxy/main.go` | Add `OPENCLAW_GATEWAY_TOKEN` env var, WebSocket-aware gateway proxy with token injection |
| Modify | `backend/cmd/authproxy/main_test.go` | Add tests for token injection, binary message passthrough, existing token overwrite |
| Modify | `scripts/init-openclaw.sh` | Pass `OPENCLAW_GATEWAY_TOKEN` to auth proxy process |

---

### Task 1: Pass gateway token to auth proxy in init script

**Files:**
- Modify: `scripts/init-openclaw.sh:624-625`

The auth proxy is started at line 624 with only `SIGNING_KEY` and `MACHINE_ID`. We need to add `OPENCLAW_GATEWAY_TOKEN`.

- [ ] **Step 1: Update the auth proxy launch command**

In `scripts/init-openclaw.sh`, change line 624-625 from:
```bash
SIGNING_KEY="$SIGNING_KEY" MACHINE_ID="$MACHINE_ID" /usr/local/bin/authproxy >> /var/log/authproxy.log 2>&1 &
```
to:
```bash
SIGNING_KEY="$SIGNING_KEY" MACHINE_ID="$MACHINE_ID" OPENCLAW_GATEWAY_TOKEN="$GATEWAY_TOKEN" /usr/local/bin/authproxy >> /var/log/authproxy.log 2>&1 &
```

- [ ] **Step 2: Commit**

```bash
git add scripts/init-openclaw.sh
git commit -m "feat: pass OPENCLAW_GATEWAY_TOKEN to auth proxy process"
```

---

### Task 2: Write failing tests for gateway WebSocket token injection

**Files:**
- Modify: `backend/cmd/authproxy/main_test.go`

- [ ] **Step 1: Write test for gateway WebSocket token injection**

Add a test that:
1. Starts a mock gateway WebSocket server that captures the first message via a channel
2. Creates an auth proxy with a gateway token configured
3. Connects to the auth proxy's `/gateway` path via WebSocket with a valid machine token
4. Sends a `connect` message without `auth.token`
5. Asserts the mock gateway received the message WITH `auth.token` injected

```go
func TestGatewayWSTokenInjection(t *testing.T) {
	// Mock gateway WS server that captures the first message via channel (race-safe)
	receivedCh := make(chan []byte, 1)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		receivedCh <- msg
		// Echo back a success response
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"res","ok":true}`))
	}))
	defer gw.Close()

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

	// Generate a valid machine token
	token := mintTestMachineToken(t, signingKey, machineID, []string{"gateway"})

	// Connect via WebSocket to /gateway with the token as query param
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/gateway?token=" + token
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial failed: %v", err)
	}
	defer conn.Close()

	// Send a connect message without auth.token
	connectMsg := `{"type":"connect","client":{"id":"control-ui"}}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(connectMsg)); err != nil {
		t.Fatalf("ws write failed: %v", err)
	}

	// Wait for the mock gateway to receive the message
	select {
	case receivedMsg := <-receivedCh:
		var parsed map[string]any
		if err := json.Unmarshal(receivedMsg, &parsed); err != nil {
			t.Fatalf("failed to parse forwarded message: %v", err)
		}
		authObj, ok := parsed["auth"].(map[string]any)
		if !ok {
			t.Fatal("forwarded message missing 'auth' object")
		}
		if authObj["token"] != gatewayToken {
			t.Errorf("auth.token = %q, want %q", authObj["token"], gatewayToken)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for mock gateway to receive message")
	}
}

func TestGatewayWSTokenOverwrite(t *testing.T) {
	// Verify that an existing auth.token from the client is overwritten
	receivedCh := make(chan []byte, 1)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		receivedCh <- msg
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"res","ok":true}`))
	}))
	defer gw.Close()

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
	defer conn.Close()

	// Send message WITH an existing auth.token — should be overwritten
	connectMsg := `{"type":"connect","auth":{"token":"client-fake-token"},"client":{"id":"control-ui"}}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(connectMsg)); err != nil {
		t.Fatalf("ws write failed: %v", err)
	}

	select {
	case receivedMsg := <-receivedCh:
		var parsed map[string]any
		if err := json.Unmarshal(receivedMsg, &parsed); err != nil {
			t.Fatalf("failed to parse: %v", err)
		}
		authObj := parsed["auth"].(map[string]any)
		if authObj["token"] != gatewayToken {
			t.Errorf("auth.token = %q, want %q (should overwrite client token)", authObj["token"], gatewayToken)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
}

func TestGatewayWSBinaryPassthrough(t *testing.T) {
	// Binary (non-JSON) first messages should pass through unmodified
	receivedCh := make(chan []byte, 1)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		receivedCh <- msg
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte("ok"))
	}))
	defer gw.Close()

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
	defer conn.Close()

	// Send binary message
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
```

- [ ] **Step 2: Add helper to mint test machine tokens**

```go
func mintTestMachineToken(t *testing.T, signingKey, machineID string, scopes []string) string {
	t.Helper()
	token, _, err := auth.IssueMachineToken(0, "test@test.com", machineID, 0, scopes, signingKey)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return token
}
```

- [ ] **Step 3: Add test imports**

The test file needs these additional imports:
```go
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
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `cd backend && go test ./cmd/authproxy/ -run TestGatewayWS -v`
Expected: Compilation errors (new struct fields and functions don't exist yet)

- [ ] **Step 5: Commit**

```bash
git add backend/cmd/authproxy/main_test.go
git commit -m "test: add failing tests for gateway WS token injection"
```

---

### Task 3: Implement WebSocket-aware gateway proxy in auth proxy

**Files:**
- Modify: `backend/cmd/authproxy/main.go`

The key change: when the request is a WebSocket upgrade to `/gateway`, use `gorilla/websocket` to:
1. Dial the upstream gateway
2. Upgrade the client connection
3. Read the first message from the client synchronously, inject `auth.token`, forward to gateway
4. Pipe all subsequent messages bidirectionally

- [ ] **Step 1: Add new fields, imports, and env var reading**

Add `gatewayToken` and `gatewayAddr` fields to `authProxy` struct. Read `OPENCLAW_GATEWAY_TOKEN` in `main()`. Fail loudly if missing (per project "no silent fallbacks" rule).

Add imports:
```go
"encoding/json"
"github.com/gorilla/websocket"
```

Update struct:
```go
type authProxy struct {
	signingKey   string
	machineID    string
	gatewayToken string // OPENCLAW_GATEWAY_TOKEN — injected into WS connect messages
	gatewayAddr  string // gateway target address (default "127.0.0.1:18789")
}
```

In `main()`, after the existing signingKey/machineID validation:
```go
gatewayToken := os.Getenv("OPENCLAW_GATEWAY_TOKEN")
if gatewayToken == "" {
	slog.Error("authproxy.missing_config", "error", "OPENCLAW_GATEWAY_TOKEN is required for Control UI auth")
	os.Exit(1)
}

// ...
proxy := &authProxy{
	signingKey:   signingKey,
	machineID:    machineID,
	gatewayToken: gatewayToken,
	gatewayAddr:  getEnv("GATEWAY_ADDR", "127.0.0.1:18789"),
}
```

- [ ] **Step 2: Add `injectAuthToken` helper**

```go
// injectAuthToken injects or overwrites auth.token in a JSON WebSocket message.
// If the message is not valid JSON, it is returned unmodified.
func injectAuthToken(msg []byte, token string) []byte {
	var obj map[string]any
	if err := json.Unmarshal(msg, &obj); err != nil {
		return msg // not JSON, pass through
	}
	authRaw, ok := obj["auth"]
	if !ok {
		obj["auth"] = map[string]any{"token": token}
	} else if authMap, ok := authRaw.(map[string]any); ok {
		authMap["token"] = token
	} else {
		obj["auth"] = map[string]any{"token": token}
	}
	injected, err := json.Marshal(obj)
	if err != nil {
		return msg // marshal failed, pass through original
	}
	return injected
}
```

- [ ] **Step 3: Add WebSocket gateway proxy method**

First-message handling runs **synchronously** (not in a goroutine) to avoid concurrent writes on `targetConn`. Bidirectional pump starts after first message is forwarded.

```go
var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true }, // auth validated via machine JWT
}

func (ap *authProxy) proxyGatewayWebSocket(w http.ResponseWriter, r *http.Request) {
	// Build upstream target URL
	targetURL := "ws://" + ap.gatewayAddr
	path := strings.TrimPrefix(r.URL.Path, "/gateway")
	if path == "" {
		path = "/"
	}
	targetURL += path
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
	defer targetConn.Close()

	// Upgrade client connection
	clientConn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("authproxy.gateway_ws_upgrade_failed", "error", err)
		return
	}
	defer clientConn.Close()

	slog.Info("authproxy.gateway_ws_connected", "target", targetURL)

	// Read the first message synchronously and inject the gateway token.
	// This MUST run before the bidirectional pump starts — gorilla/websocket
	// does not support concurrent writers on the same connection.
	msgType, msg, err := clientConn.ReadMessage()
	if err != nil {
		slog.Error("authproxy.gateway_ws_first_read_failed", "error", err)
		return
	}
	if msgType == websocket.TextMessage {
		msg = injectAuthToken(msg, ap.gatewayToken)
	}
	if err := targetConn.WriteMessage(msgType, msg); err != nil {
		slog.Error("authproxy.gateway_ws_first_write_failed", "error", err)
		return
	}

	// Bidirectional proxy for remaining messages
	done := make(chan struct{}, 2)

	// client → target
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			mt, m, err := clientConn.ReadMessage()
			if err != nil {
				return
			}
			if err := targetConn.WriteMessage(mt, m); err != nil {
				return
			}
		}
	}()

	// target → client
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			mt, m, err := targetConn.ReadMessage()
			if err != nil {
				return
			}
			if err := clientConn.WriteMessage(mt, m); err != nil {
				return
			}
		}
	}()

	<-done
}
```

**Note on ping/pong:** The client→auth proxy leg traverses the full tunnel chain (Cloudflare → Cloud Run → cloudflared → VM). However, the gateway itself sends tick events every 30s which serve as keepalive on the target→client direction. The client (browser) also has its own keepalive. For the initial fix this is sufficient. If idle timeouts are observed in production, add explicit ping/pong (follow the pattern in `agentapi/proxy.go:proxyWebSocketBidirectional`).

- [ ] **Step 4: Wire the WebSocket proxy into the gateway route**

In `ServeHTTP`, modify the `/gateway` case to check for WebSocket upgrade:

```go
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
```

This also uses `ap.gatewayAddr` for the HTTP fallback path, replacing the previously hardcoded `"http://127.0.0.1:18789"`.

- [ ] **Step 5: Run tests**

Run: `cd backend && go test ./cmd/authproxy/ -v -race`
Expected: All tests pass, including `TestGatewayWSTokenInjection`, `TestGatewayWSTokenOverwrite`, `TestGatewayWSBinaryPassthrough`, and existing `TestPortDenylist`. The `-race` flag confirms no data races.

- [ ] **Step 6: Commit**

```bash
git add backend/cmd/authproxy/main.go
git commit -m "feat: inject gateway token into Control UI WebSocket connect"
```

---

### Task 4: Run full test suite

- [ ] **Step 1: Run Go unit tests**

Run: `make test-go`
Expected: All pass

- [ ] **Step 2: Run gateway E2E tests**

Run: `make test-gateway-e2e`
Expected: All pass (~12s). These tests validate the full proxy → metadata → gateway chain.

---

### Task 5: Build and deploy

- [ ] **Step 1: Commit all changes and push**

```bash
git push origin platform-models
```

- [ ] **Step 2: Build and upload rootfs**

The init script change needs a rootfs rebuild:
```bash
make build-upload-rootfs
```

Wait for agent self-update (~5 min), then verify by checking agent logs.

---

## Verification

After deployment, verify the Control UI connects:
1. Open a machine's Control UI in browser
2. Check browser DevTools → Network → WS tab
3. The WebSocket connect should succeed (no `DEVICE_IDENTITY_REQUIRED` error)
4. Check auth proxy logs: `gcloud compute ssh <VM> --command="cat /var/log/authproxy.log | grep gateway_ws"`
