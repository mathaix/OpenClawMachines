//go:build linux && integration

package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mathaix/openclawmachines/backend/internal/agentapi"
)

// ============================================================================
// Dataplane Tests — Per-VM Auth Proxy
//
// All subtests share a single VM (booted once by the parent). This saves ~5
// separate VM boots (~10 minutes total).
//
// The authproxy validates HS256 machine tokens and reverse-proxies to
// internal services. These tests boot a real Firecracker VM with authproxy
// and hit it directly from the host bridge.
// ============================================================================

// issueMachineToken and waitForAuthProxy live in helpers_test.go.

func TestDataplaneSuite(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)

	proxyServer := httptest.NewServer(agentapi.NewServer(cfg.AgentToken, orch, "", nil, "", nil, nil, nil, false, nil, "").ProxyRouter())
	defer proxyServer.Close()

	vmCfg := generateTestVMConfig(0)
	withDefaultRuntimeSelection(t, &vmCfg)
	if err := orch.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForAuthProxy(t, vmCfg.VMIP, 30*time.Second)

	t.Run("AuthProxyHealth", func(t *testing.T) {
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(fmt.Sprintf("http://%s:8080/health", vmCfg.VMIP))
		if err != nil {
			t.Fatalf("Health check failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		var health map[string]string
		if err := json.Unmarshal(body, &health); err == nil {
			if health["status"] != "ok" {
				t.Errorf("Expected status ok, got %s", health["status"])
			}
		}

		t.Log("Authproxy health check passed")
	})

	t.Run("AuthProxyRejectsNoToken", func(t *testing.T) {
		client := &http.Client{Timeout: 5 * time.Second}

		// Request /terminal without token → 401
		resp, err := client.Get(fmt.Sprintf("http://%s:8080/terminal/", vmCfg.VMIP))
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Expected 401 for missing token, got %d", resp.StatusCode)
		}

		// Request /gateway without token → 401
		resp, err = client.Get(fmt.Sprintf("http://%s:8080/gateway/", vmCfg.VMIP))
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Expected 401 for missing token on /gateway, got %d", resp.StatusCode)
		}

		t.Log("Authproxy correctly rejects requests without token")
	})

	t.Run("AuthProxyRejectsInvalidToken", func(t *testing.T) {
		client := &http.Client{Timeout: 5 * time.Second}

		// Token signed with wrong key
		wrongToken := issueMachineToken(t, vmCfg.MachineID, generateTestSigningKey(), []string{"all"})
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://%s:8080/terminal/", vmCfg.VMIP), nil)
		req.Header.Set("X-Machine-Token", wrongToken)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Expected 401 for wrong-key token, got %d", resp.StatusCode)
		}

		// Garbage token
		req, _ = http.NewRequest("GET", fmt.Sprintf("http://%s:8080/terminal/", vmCfg.VMIP), nil)
		req.Header.Set("X-Machine-Token", "not-a-real-jwt")
		resp, err = client.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Expected 401 for garbage token, got %d", resp.StatusCode)
		}

		t.Log("Authproxy correctly rejects invalid tokens")
	})

	t.Run("AuthProxyTerminal", func(t *testing.T) {
		// Issue a valid machine token with terminal scope
		token := issueMachineToken(t, vmCfg.MachineID, vmCfg.SigningKey, []string{"terminal"})

		// Connect to terminal WebSocket through authproxy using query param
		wsURL := fmt.Sprintf("ws://%s:8080/terminal/ws?token=%s", vmCfg.VMIP, token)
		t.Logf("Connecting to terminal through authproxy: %s", wsURL)

		dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
		conn, _, err := dialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("WebSocket dial through authproxy failed: %v", err)
		}
		defer conn.Close()

		// Send resize
		if err := conn.WriteMessage(websocket.BinaryMessage, []byte(`1{"columns":80,"rows":24}`)); err != nil {
			t.Fatalf("Failed to send resize: %v", err)
		}

		// Send echo command
		marker := fmt.Sprintf("DATAPLANE_TEST_%s", randomID())
		cmd := fmt.Sprintf("0echo %s\n", marker)
		if err := conn.WriteMessage(websocket.BinaryMessage, []byte(cmd)); err != nil {
			t.Fatalf("Failed to send command: %v", err)
		}

		// Read output and look for marker
		conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		found := false
		for i := 0; i < 100; i++ {
			_, data, err := conn.ReadMessage()
			if err != nil {
				break
			}
			if len(data) > 0 && data[0] == '0' {
				if strings.Contains(string(data[1:]), marker) {
					found = true
					break
				}
			}
		}

		if !found {
			t.Error("Did not receive echo output through authproxy → terminal")
		} else {
			t.Log("Terminal echo test through authproxy passed")
		}
	})

	t.Run("SSHPrincipalsEndpoint", func(t *testing.T) {
		// Use terminal to run curl from inside the VM (metadata uses source IP matching)
		wsURL := strings.Replace(
			fmt.Sprintf("%s/proxy/%s/terminal/ws", proxyServer.URL, vmCfg.MachineID),
			"http://", "ws://", 1)

		gwIP := bridge.Gateway

		// Test: /v1/ssh-principals returns authorized principals (one per line)
		output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			fmt.Sprintf(`NONCE=$(cat /run/ocm-nonce); curl -sf -H "X-Metadata-Nonce: $NONCE" "http://%s/v1/ssh-principals"`, gwIP),
			10*time.Second)
		t.Logf("SSH principals output: %s", output)

		// Should contain the full email
		if !strings.Contains(output, "test@openclawmachines.com") {
			t.Errorf("Expected principals to contain test@openclawmachines.com, got: %s", output)
		}
		// Should also contain the username-only part
		if !strings.Contains(output, "test") {
			t.Errorf("Expected principals to contain username 'test', got: %s", output)
		}

		t.Log("SSH principals endpoint returns authorized principals")
	})

	t.Run("SSHCheckEndpointLegacy", func(t *testing.T) {
		// Legacy /v1/ssh-check endpoint — kept for backwards compatibility
		wsURL := strings.Replace(
			fmt.Sprintf("%s/proxy/%s/terminal/ws", proxyServer.URL, vmCfg.MachineID),
			"http://", "ws://", 1)

		gwIP := bridge.Gateway

		// Test: allowed email returns 200
		output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			fmt.Sprintf(`NONCE=$(cat /run/ocm-nonce); curl -sf -o /dev/null -w '%%{http_code}' -H "X-Metadata-Nonce: $NONCE" "http://%s/v1/ssh-check?email=test@openclawmachines.com"`, gwIP),
			10*time.Second)
		if !strings.Contains(output, "200") {
			t.Errorf("Expected 200 for allowed email, output: %s", output)
		}

		// Test: denied email returns 403
		output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			fmt.Sprintf(`NONCE=$(cat /run/ocm-nonce); curl -s -o /dev/null -w '%%{http_code}' -H "X-Metadata-Nonce: $NONCE" "http://%s/v1/ssh-check?email=hacker@evil.com"`, gwIP),
			10*time.Second)
		if !strings.Contains(output, "403") {
			t.Errorf("Expected 403 for denied email, output: %s", output)
		}

		t.Log("Legacy SSH check endpoint works for backwards compatibility")
	})

	// Wait for gateway (slower to start) before running gateway-dependent tests
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	t.Run("AuthProxyGateway", func(t *testing.T) {
		// Issue token with gateway scope
		token := issueMachineToken(t, vmCfg.MachineID, vmCfg.SigningKey, []string{"gateway"})

		// Hit gateway health through authproxy with X-Machine-Token header
		client := &http.Client{Timeout: 10 * time.Second}
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://%s:8080/gateway/health", vmCfg.VMIP), nil)
		req.Header.Set("X-Machine-Token", token)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Gateway request through authproxy failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("Expected 200 for gateway health, got %d: %s", resp.StatusCode, body)
		} else {
			t.Log("Gateway health check through authproxy passed")
		}

		// Verify wrong scope is rejected
		terminalOnlyToken := issueMachineToken(t, vmCfg.MachineID, vmCfg.SigningKey, []string{"terminal"})
		req, _ = http.NewRequest("GET", fmt.Sprintf("http://%s:8080/gateway/health", vmCfg.VMIP), nil)
		req.Header.Set("X-Machine-Token", terminalOnlyToken)
		resp, err = client.Do(req)
		if err != nil {
			t.Fatalf("Scope test request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Expected 403 for wrong scope, got %d", resp.StatusCode)
		} else {
			t.Log("Authproxy correctly rejects wrong scope")
		}
	})
}
