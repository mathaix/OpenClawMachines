//go:build linux && integration

package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const gatewayProtocolVersion = 4

// ============================================================================
// Gateway Functional Tests
//
// All subtests share a single VM (booted once by the parent). This saves ~7
// separate VM boots + gateway startup waits (~21 minutes total).
// ============================================================================

func TestGatewaySuite(t *testing.T) {
	proxyURL, vmCfg, _ := setupFullStackWithGateway(t)

	t.Run("HealthDirect", func(t *testing.T) {
		testGatewayHealthDirect(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID)
	})

	t.Run("HealthViaProxy", func(t *testing.T) {
		testGatewayHealth(t, proxyURL, vmCfg.MachineID, vmCfg.ProxyToken)
	})

	t.Run("HealthEndpointStatus", func(t *testing.T) {
		url := fmt.Sprintf("%s/proxy/%s/health?token=%s",
			proxyURL, vmCfg.MachineID, vmCfg.ProxyToken)

		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("Health proxy request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		var health map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
			t.Fatalf("Failed to decode health response: %v", err)
		}

		if health["gateway"] != true {
			t.Errorf("Expected gateway=true, got %v", health["gateway"])
		}

		status, _ := health["status"].(string)
		if status != "ok" && status != "degraded" {
			t.Errorf("Expected status ok or degraded, got %q", status)
		}

		t.Logf("Health endpoint: status=%s, gateway=%v, browser=%v",
			status, health["gateway"], health["browser"])
	})

	t.Run("WebSocket", func(t *testing.T) {
		// Full end-to-end WebSocket test:
		//   test → agent proxy → auth proxy (in-VM) → gateway
		// This validates token forwarding and challenge-response auth.
		wsURL := strings.Replace(proxyURL, "http://", "ws://", 1)
		wsURL = fmt.Sprintf("%s/proxy/%s/gateway/ws?token=%s",
			wsURL, vmCfg.MachineID, vmCfg.ProxyToken)

		t.Logf("Connecting to gateway WebSocket: %s", wsURL)

		dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
		conn, resp, err := dialer.Dial(wsURL, nil)
		if err != nil {
			statusCode := 0
			if resp != nil {
				statusCode = resp.StatusCode
			}
			t.Fatalf("WebSocket dial failed: err=%v, status=%d (check auth proxy config)", err, statusCode)
		}
		defer conn.Close()

		t.Log("Gateway WebSocket connection established successfully")

		// Read the connect.challenge from the gateway
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, challengeData, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("Failed to read challenge from gateway: %v", err)
		}
		t.Logf("Gateway challenge: %s", string(challengeData))

		var challenge struct {
			Type    string `json:"type"`
			Event   string `json:"event"`
			Payload struct {
				Nonce string `json:"nonce"`
				Ts    int64  `json:"ts"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(challengeData, &challenge); err != nil {
			t.Fatalf("Failed to parse challenge: %v", err)
		}
		if challenge.Event != "connect.challenge" {
			t.Fatalf("Expected connect.challenge, got %s", challenge.Event)
		}

		connectMsg := map[string]interface{}{
			"type":   "req",
			"id":     "connect-1",
			"method": "connect",
			"params": map[string]interface{}{
				"device": map[string]interface{}{
					"name": "ocm-test",
					"type": "browser",
				},
				"nonce": challenge.Payload.Nonce,
			},
		}
		connectData, _ := json.Marshal(connectMsg)
		t.Logf("Sending connect response: %s", string(connectData))
		if err := conn.WriteMessage(websocket.TextMessage, connectData); err != nil {
			t.Fatalf("Failed to send connect response: %v", err)
		}

		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, respData, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("Connection closed after challenge response (token rejected): %v", err)
		}
		t.Logf("Gateway connect response: %s", string(respData))

		// Verify the connect response — we accept either a successful connect
		// or an INVALID_REQUEST validation error (means auth chain works, just
		// missing protocol fields like minProtocol/maxProtocol/client that
		// evolve with the gateway). We reject UNAUTHORIZED which means the
		// auth proxy didn't forward the bearer token correctly.
		var connectResp struct {
			OK    bool `json:"ok"`
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(respData, &connectResp); err != nil {
			t.Fatalf("Failed to parse connect response: %v", err)
		}
		if connectResp.Error != nil {
			if connectResp.Error.Code == "UNAUTHORIZED" {
				t.Fatalf("Gateway returned UNAUTHORIZED — bearer token was not forwarded or rejected: %s",
					connectResp.Error.Message)
			}
			// INVALID_REQUEST is acceptable — it means the auth chain works
			// but the connect params schema has evolved beyond what this test sends.
			t.Logf("Gateway connect returned %s (expected — test doesn't send full protocol fields): %s",
				connectResp.Error.Code, connectResp.Error.Message)
		} else {
			t.Log("Gateway WebSocket connect succeeded")
		}

		t.Log("Gateway WebSocket challenge-response auth completed successfully")
	})

	t.Run("AuthProtectedAPIViaProxy", func(t *testing.T) {
		client := &http.Client{Timeout: 10 * time.Second}

		url := fmt.Sprintf("%s/proxy/%s/gateway/api/v1/bots?token=%s",
			proxyURL, vmCfg.MachineID, vmCfg.ProxyToken)
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("Auth-protected gateway request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode == http.StatusUnauthorized {
			t.Fatal("Gateway returned 401 — bearer token was NOT forwarded by agent proxy")
		}
		if resp.StatusCode == http.StatusForbidden {
			t.Fatal("Gateway returned 403 — bearer token was forwarded but rejected")
		}

		t.Logf("Gateway auth-protected API responded with %d (bearer token forwarded successfully)", resp.StatusCode)
	})

	t.Run("OriginHeaderViaAuthProxy", func(t *testing.T) {
		// Simulate a browser connecting through the auth proxy with an Origin header.
		// This catches the bug where the auth proxy overwrites the Host header,
		// causing the gateway's dangerouslyAllowHostHeaderOriginFallback to fail.
		token := issueMachineToken(t, vmCfg.MachineID, vmCfg.SigningKey, []string{"gateway"})

		wsURL := fmt.Sprintf("ws://%s:8080/gateway/ws", vmCfg.VMIP)

		headers := http.Header{}
		headers.Set("X-Machine-Token", token)
		headers.Set("Origin", "https://test-origin.openclawmachines.com")
		headers.Set("Host", "test-origin.openclawmachines.com")

		dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
		conn, resp, err := dialer.Dial(wsURL, headers)
		if err != nil {
			statusCode := 0
			if resp != nil {
				statusCode = resp.StatusCode
			}
			t.Fatalf("WebSocket dial with Origin header failed: err=%v, status=%d "+
				"(auth proxy may be overwriting Host header)", err, statusCode)
		}
		defer conn.Close()

		// Read challenge to confirm gateway accepted the connection
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("Failed to read from gateway after Origin-header connect: %v", err)
		}

		// Verify we got a challenge, not an origin error
		if strings.Contains(string(data), "origin not allowed") {
			t.Fatal("Gateway rejected connection with 'origin not allowed' — " +
				"auth proxy is overwriting the Host header")
		}

		t.Logf("Gateway accepted connection with browser-like Origin header: %s",
			string(data))
	})

	t.Run("GatewayTokenHandshake", func(t *testing.T) {
		// Full gateway handshake with gateway auth token.
		// This catches the bug where OPENCLAW_GATEWAY_TOKEN wasn't exported
		// in the init script, causing the gateway to generate a random token
		// that nothing else knows — resulting in AUTH_TOKEN_MISSING or
		// AUTH_TOKEN_MISMATCH errors during the connect handshake.
		token := issueMachineToken(t, vmCfg.MachineID, vmCfg.SigningKey, []string{"gateway"})

		wsURL := fmt.Sprintf("ws://%s:8080/gateway/ws", vmCfg.VMIP)

		headers := http.Header{}
		headers.Set("X-Machine-Token", token)

		dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
		conn, resp, err := dialer.Dial(wsURL, headers)
		if err != nil {
			statusCode := 0
			if resp != nil {
				statusCode = resp.StatusCode
			}
			t.Fatalf("WebSocket dial failed: err=%v, status=%d", err, statusCode)
		}
		defer conn.Close()

		// Read the connect.challenge
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, challengeData, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("Failed to read challenge: %v", err)
		}

		var challenge struct {
			Event   string `json:"event"`
			Payload struct {
				Nonce string `json:"nonce"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(challengeData, &challenge); err != nil {
			t.Fatalf("Failed to parse challenge: %v", err)
		}
		if challenge.Event != "connect.challenge" {
			t.Fatalf("Expected connect.challenge, got %s", challenge.Event)
		}

		// Send a connect frame with the gateway token — this is what a real
		// TUI/CLI does. Uses mode: "probe" (not "ui") to test the non-privileged
		// path. With auth.mode="none" all clients are accepted without pairing.
		connectMsg := map[string]interface{}{
			"type":   "req",
			"id":     "connect-1",
			"method": "connect",
			"params": map[string]interface{}{
				"minProtocol": gatewayProtocolVersion,
				"maxProtocol": gatewayProtocolVersion,
				"client": map[string]interface{}{
					"id":       "openclaw-probe",
					"version":  "test",
					"platform": "linux",
					"mode":     "probe",
				},
				"auth": map[string]interface{}{
					"token": vmCfg.GatewayToken,
				},
			},
		}
		connectData, _ := json.Marshal(connectMsg)
		if err := conn.WriteMessage(websocket.TextMessage, connectData); err != nil {
			t.Fatalf("Failed to send connect frame: %v", err)
		}

		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, respData, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("Connection closed after connect frame: %v", err)
		}
		t.Logf("Gateway connect response: %s", string(respData))

		// Parse the response and check for auth errors specifically.
		// INVALID_REQUEST for missing schema fields is acceptable (schema evolves),
		// but auth errors mean the gateway token wasn't configured.
		var connectResp struct {
			OK    bool `json:"ok"`
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Details *struct {
					Code string `json:"code"`
				} `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(respData, &connectResp); err != nil {
			t.Fatalf("Failed to parse connect response: %v", err)
		}

		if connectResp.Error != nil {
			detailCode := ""
			if connectResp.Error.Details != nil {
				detailCode = connectResp.Error.Details.Code
			}
			authErrors := map[string]bool{
				"AUTH_TOKEN_MISSING":  true,
				"AUTH_TOKEN_MISMATCH": true,
				"AUTH_REQUIRED":       true,
				"AUTH_UNAUTHORIZED":   true,
			}
			if authErrors[detailCode] || connectResp.Error.Code == "UNAUTHORIZED" {
				t.Fatalf("Gateway rejected gateway token with auth error: code=%s detail=%s msg=%s "+
					"(OPENCLAW_GATEWAY_TOKEN likely not set in init script)",
					connectResp.Error.Code, detailCode, connectResp.Error.Message)
			}
			t.Logf("Gateway connect returned non-auth error (acceptable): code=%s detail=%s msg=%s",
				connectResp.Error.Code, detailCode, connectResp.Error.Message)
		} else {
			t.Log("Gateway accepted connect with gateway token (full handshake succeeded)")
		}
	})

	t.Run("ProxyRejectsWithoutProxyToken", func(t *testing.T) {
		client := &http.Client{Timeout: 10 * time.Second}

		url := fmt.Sprintf("%s/proxy/%s/gateway/health", proxyURL, vmCfg.MachineID)
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Expected 401 for missing proxy token, got %d", resp.StatusCode)
		} else {
			t.Log("Agent proxy correctly rejects request without proxy token")
		}
	})

	t.Run("ProxyRejectsWrongProxyToken", func(t *testing.T) {
		client := &http.Client{Timeout: 10 * time.Second}

		url := fmt.Sprintf("%s/proxy/%s/gateway/health?token=wrong-token",
			proxyURL, vmCfg.MachineID)
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Expected 403 for wrong proxy token, got %d", resp.StatusCode)
		} else {
			t.Log("Agent proxy correctly rejects wrong proxy token")
		}
	})

	t.Run("ControlUIViaProxy", func(t *testing.T) {
		// Request the gateway Control UI root through the full proxy chain:
		// test → agent proxy → auth proxy (in-VM) → gateway /ui/
		// The agent proxy routes /proxy/{id}/gateway/ → auth proxy /gateway/ui/
		// → auth proxy strips /gateway → gateway /ui/
		//
		// This catches the upstream breakage where openclaw moved the Control UI
		// from / to /ui/ (commit 223d7dc23).
		client := &http.Client{Timeout: 15 * time.Second}

		url := fmt.Sprintf("%s/proxy/%s/gateway/?token=%s",
			proxyURL, vmCfg.MachineID, vmCfg.ProxyToken)

		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("Control UI request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		body, _ := io.ReadAll(resp.Body)
		bodyStr := string(body)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Control UI returned %d, want 200; body: %.200s", resp.StatusCode, bodyStr)
		}

		contentType := resp.Header.Get("Content-Type")
		if !strings.Contains(contentType, "text/html") {
			t.Errorf("Content-Type = %q, want text/html", contentType)
		}

		// The base path variable is present in production builds.
		// In dev/fallback UI it may be absent — log but don't fail.
		if strings.Contains(bodyStr, "__OPENCLAW_CONTROL_UI_BASE_PATH__") {
			t.Log("HTML contains __OPENCLAW_CONTROL_UI_BASE_PATH__ variable")
		}

		t.Logf("Control UI served successfully through proxy (status=%d, %d bytes)", resp.StatusCode, len(body))
	})

	t.Run("InitLogs", func(t *testing.T) {
		wsURL := strings.Replace(proxyURL, "http://", "ws://", 1)
		wsURL = fmt.Sprintf("%s/proxy/%s/terminal/?token=%s",
			wsURL, vmCfg.MachineID, vmCfg.ProxyToken)

		output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			"cat /var/log/openclaw-init.log 2>/dev/null | tail -100",
			15*time.Second)

		t.Logf("Init log (last 100 lines):\n%s", output)

		if output == "" || !strings.Contains(output, "OpenClaw") {
			t.Log("WARN: Init log file may not exist — /dev/fd symlink may be missing for tee")
		}

		// Verify config source was set to metadata
		if strings.Contains(output, "Config source") {
			t.Log("OK: Init log contains config source line")
		}

		// Verify init completed (supervisor starts gateway)
		if strings.Contains(output, "supervisor") || strings.Contains(output, "Starting gateway") {
			t.Log("OK: Init log shows gateway startup")
		}

		// Fail on fatal errors
		if strings.Contains(output, "[FATAL]") {
			t.Error("Init log contains [FATAL] error")
		}
	})

	t.Run("GatewayLogs", func(t *testing.T) {
		wsURL := strings.Replace(proxyURL, "http://", "ws://", 1)
		wsURL = fmt.Sprintf("%s/proxy/%s/terminal/?token=%s",
			wsURL, vmCfg.MachineID, vmCfg.ProxyToken)

		// Use find to locate the gateway log (date-based filename)
		output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			"find /tmp/openclaw -name 'openclaw-*.log' -exec head -200 {} + 2>/dev/null",
			15*time.Second)

		t.Logf("Gateway log (first 200 lines):\n%s", output)

		if output == "" || len(strings.TrimSpace(output)) < 10 {
			t.Log("WARN: Gateway log is empty or not found")
		}

		// Verify config was loaded from metadata source
		if strings.Contains(output, "config source") ||
			strings.Contains(output, "config loaded") {
			t.Log("OK: Gateway log shows metadata config loaded")
		}

		// Fail on errors that indicate broken config wiring
		fatalPatterns := []string{
			"origin not allowed",
			"ENOENT: no such file or directory",
		}
		for _, pattern := range fatalPatterns {
			if strings.Contains(output, pattern) {
				t.Errorf("Gateway log contains fatal error: %q", pattern)
			}
		}

		// Note: "Proxy headers detected from untrusted" is expected since
		// trustedProxies is intentionally not configured (gateway binds to
		// loopback, all connections are local). Auth proxy headers from
		// 127.0.0.1 will be ignored, which is fine — security is enforced
		// at the auth proxy layer, not the gateway.
		if strings.Contains(output, "Proxy headers detected from untrusted") {
			t.Log("INFO: 'Proxy headers detected from untrusted' — expected without trustedProxies")
		}

		// Warn on non-fatal issues worth tracking
		if strings.Contains(output, "token_missing") {
			t.Log("WARN: gateway log contains 'token_missing'")
		}
	})

	// Keep this subtest last — it kicks the gateway, so subsequent checks
	// would have to re-wait for readiness.
	t.Run("RestartGateway", func(t *testing.T) {
		testRestartGateway(t, proxyURL, vmCfg.MachineID, vmCfg.ProxyToken)
	})
}

// testRestartGateway exercises the ocmptyd /restart-gateway path through the
// full proxy chain: test → agent proxy → ocmptyd (port 7681 in VM) →
// the dedicated gateway restart helper. This is the only coverage of the runit
// control path; unit tests stub out the command and can't catch permission
// issues in the real rootfs.
func testRestartGateway(t *testing.T, proxyURL, machineID, proxyToken string) {
	t.Helper()

	client := &http.Client{Timeout: 15 * time.Second}

	// Sanity check: gateway should be healthy before the restart so any
	// later "gateway unhealthy" signal is attributable to the restart.
	healthURL := fmt.Sprintf("%s/proxy/%s/gateway/health?token=%s",
		proxyURL, machineID, proxyToken)
	if err := waitForGatewayHealthy(client, healthURL, 30*time.Second); err != nil {
		t.Fatalf("gateway not healthy before restart: %v", err)
	}

	restartURL := fmt.Sprintf("%s/proxy/%s/restart-gateway?token=%s",
		proxyURL, machineID, proxyToken)
	resp, err := client.Post(restartURL, "application/json", nil)
	if err != nil {
		t.Fatalf("POST /restart-gateway failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restart-gateway: status %d, body %q", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "restart-signaled") {
		t.Fatalf("restart-gateway: unexpected body %q", string(body))
	}
	t.Logf("1. restart-gateway returned 200: %s", strings.TrimSpace(string(body)))

	// The gateway may return 502 / unhealthy while it cycles. It must come
	// back green within a reasonable window — otherwise either `sv restart`
	// didn't actually restart the service or the finish/run scripts latched
	// the crash-loop state.
	if err := waitForGatewayHealthy(client, healthURL, 90*time.Second); err != nil {
		t.Fatalf("gateway did not become healthy after restart within 90s: %v", err)
	}
	t.Log("2. Gateway recovered to healthy after supervised restart")

	// The TOOLS block advertises `sv status <name>` for all baseline
	// services. Verify the wrapper + sudoers expansion makes this work
	// from the openclaw user for at least one non-gateway service.
	wsURL := strings.Replace(proxyURL, "http://", "ws://", 1) +
		fmt.Sprintf("/proxy/%s/terminal/?token=%s", machineID, proxyToken)
	out := testTerminalCommandSplit(t, wsURL, proxyToken,
		"sv status pty-server 2>&1 | head -1", 15*time.Second)
	if !strings.Contains(out, "run: /var/service/pty-server") &&
		!strings.Contains(out, "run: pty-server") {
		t.Fatalf("`sv status pty-server` as openclaw did not report a run state: %q", out)
	}
	t.Logf("3. `sv status pty-server` from openclaw: %s", strings.TrimSpace(out))
}

func waitForGatewayHealthy(client *http.Client, healthURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastStatus int
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(healthURL)
		if err != nil {
			lastErr = err
		} else {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			lastStatus = resp.StatusCode
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(1 * time.Second)
	}
	if lastErr != nil {
		return fmt.Errorf("last status=%d err=%v", lastStatus, lastErr)
	}
	return fmt.Errorf("last status=%d", lastStatus)
}
