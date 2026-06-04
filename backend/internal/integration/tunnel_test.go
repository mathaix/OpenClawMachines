//go:build linux && integration

package integration

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mathaix/openclawmachines/backend/internal/agentapi"
	"github.com/mathaix/openclawmachines/backend/internal/tunnel"
)

// ============================================================================
// Cloudflare Tunnel Tests
//
// These tests create real Cloudflare tunnels, route traffic through them,
// and verify the full production path works.
//
// Required env vars:
//   CF_API_TOKEN   — Cloudflare API token (tunnel + DNS permissions)
//   CF_ACCOUNT_ID  — Cloudflare account ID
//   CF_ZONE_ID     — Cloudflare zone ID
// ============================================================================

// tunnelEnv holds shared tunnel infrastructure for E2E tests.
type tunnelEnv struct {
	manager     *tunnel.Manager
	tunnelID    string
	tunnelToken string
	hostname    string
	cfProcess   *exec.Cmd
	proxyURL    string // local proxy URL that tunnel points to
}

var (
	sharedTunnel     *tunnelEnv
	sharedTunnelOnce sync.Once
	sharedTunnelErr  error
)

// skipIfNoTunnelCreds skips the test if Cloudflare credentials are not set.
func skipIfNoTunnelCreds(t *testing.T) {
	t.Helper()
	if os.Getenv("CF_API_TOKEN") == "" {
		t.Skip("Skipping: CF_API_TOKEN not set (requires Cloudflare credentials)")
	}
	if os.Getenv("CF_ACCOUNT_ID") == "" {
		t.Skip("Skipping: CF_ACCOUNT_ID not set")
	}
	if os.Getenv("CF_ZONE_ID") == "" {
		t.Skip("Skipping: CF_ZONE_ID not set")
	}
	if _, err := exec.LookPath("cloudflared"); err != nil {
		t.Skip("Skipping: cloudflared binary not found in PATH")
	}
}

// setupSharedTunnel creates a Cloudflare tunnel that points to the given local proxy URL.
// Uses sync.Once so multiple tests share the same tunnel.
func setupSharedTunnel(t *testing.T, localProxyURL string) *tunnelEnv {
	t.Helper()
	skipIfNoTunnelCreds(t)

	sharedTunnelOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		mgr := tunnel.New(
			os.Getenv("CF_API_TOKEN"),
			os.Getenv("CF_ACCOUNT_ID"),
			os.Getenv("CF_ZONE_ID"),
		)

		ts := time.Now().Unix()
		name := fmt.Sprintf("ocm-test-%d", ts)
		hostname := fmt.Sprintf("test-%d.openclawmachines.com", ts)

		t.Logf("Creating tunnel %s → %s", name, hostname)

		tunnelID, tunnelToken, err := mgr.CreateTunnel(ctx, name)
		if err != nil {
			sharedTunnelErr = fmt.Errorf("create tunnel: %w", err)
			return
		}

		if err := mgr.ConfigureTunnel(ctx, tunnelID, hostname); err != nil {
			mgr.DeleteTunnel(ctx, tunnelID)
			sharedTunnelErr = fmt.Errorf("configure tunnel: %w", err)
			return
		}

		if err := mgr.CreateDNSRoute(ctx, tunnelID, hostname); err != nil {
			mgr.DeleteTunnel(ctx, tunnelID)
			sharedTunnelErr = fmt.Errorf("create DNS route: %w", err)
			return
		}

		// Start cloudflared
		cfProcess := exec.Command("cloudflared", "tunnel", "run", "--token", tunnelToken)
		cfProcess.Stdout = os.Stdout
		cfProcess.Stderr = os.Stderr
		if err := cfProcess.Start(); err != nil {
			mgr.DeleteDNSRoute(ctx, hostname)
			mgr.DeleteTunnel(ctx, tunnelID)
			sharedTunnelErr = fmt.Errorf("start cloudflared: %w", err)
			return
		}

		sharedTunnel = &tunnelEnv{
			manager:     mgr,
			tunnelID:    tunnelID,
			tunnelToken: tunnelToken,
			hostname:    hostname,
			cfProcess:   cfProcess,
			proxyURL:    localProxyURL,
		}

		t.Logf("Tunnel created: %s (id=%s)", hostname, tunnelID)
	})

	if sharedTunnelErr != nil {
		t.Fatalf("Shared tunnel setup failed: %v", sharedTunnelErr)
	}
	if sharedTunnel == nil {
		t.Fatal("Shared tunnel not initialized")
	}

	// Register cleanup (idempotent — only first cleanup does work)
	t.Cleanup(func() {
		cleanupSharedTunnel(t)
	})

	return sharedTunnel
}

var cleanupOnce sync.Once

func cleanupSharedTunnel(t *testing.T) {
	cleanupOnce.Do(func() {
		if sharedTunnel == nil {
			return
		}

		t.Log("Cleaning up shared tunnel...")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Kill cloudflared
		if sharedTunnel.cfProcess != nil && sharedTunnel.cfProcess.Process != nil {
			sharedTunnel.cfProcess.Process.Signal(syscall.SIGTERM)
			done := make(chan error, 1)
			go func() { done <- sharedTunnel.cfProcess.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				sharedTunnel.cfProcess.Process.Kill()
			}
		}

		// Delete DNS + tunnel
		if err := sharedTunnel.manager.DeleteDNSRoute(ctx, sharedTunnel.hostname); err != nil {
			t.Logf("Warning: failed to delete DNS route: %v", err)
		}
		if err := sharedTunnel.manager.DeleteTunnel(ctx, sharedTunnel.tunnelID); err != nil {
			t.Logf("Warning: failed to delete tunnel: %v", err)
		}

		t.Logf("Tunnel %s cleaned up", sharedTunnel.hostname)
	})
}

// waitForTunnelReady polls the tunnel URL until it responds.
func waitForTunnelReady(t *testing.T, tunnelURL string, timeout time.Duration) {
	t.Helper()

	// Use a client that skips TLS verification for test domains
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(tunnelURL)
		if err == nil {
			resp.Body.Close()
			t.Logf("Tunnel is ready (status=%d)", resp.StatusCode)
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("Tunnel did not become ready at %s within %s", tunnelURL, timeout)
}

// ============================================================================
// Tunnel Lifecycle Test
// ============================================================================

func TestTunnel_Lifecycle(t *testing.T) {
	skipIfNoTunnelCreds(t)
	skipIfNotRoot(t) // consistency with other integration tests

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	mgr := tunnel.New(
		os.Getenv("CF_API_TOKEN"),
		os.Getenv("CF_ACCOUNT_ID"),
		os.Getenv("CF_ZONE_ID"),
	)

	ts := time.Now().Unix()
	name := fmt.Sprintf("ocm-lifecycle-test-%d", ts)
	hostname := fmt.Sprintf("lifecycle-test-%d.openclawmachines.com", ts)

	// Create tunnel
	tunnelID, token, err := mgr.CreateTunnel(ctx, name)
	if err != nil {
		t.Fatalf("CreateTunnel failed: %v", err)
	}
	if tunnelID == "" || token == "" {
		t.Fatal("Expected non-empty tunnel ID and token")
	}
	t.Logf("1. Created tunnel: id=%s", tunnelID)

	// Configure tunnel
	if err := mgr.ConfigureTunnel(ctx, tunnelID, hostname); err != nil {
		mgr.DeleteTunnel(ctx, tunnelID)
		t.Fatalf("ConfigureTunnel failed: %v", err)
	}
	t.Log("2. Configured tunnel ingress")

	// Create DNS route
	if err := mgr.CreateDNSRoute(ctx, tunnelID, hostname); err != nil {
		mgr.DeleteTunnel(ctx, tunnelID)
		t.Fatalf("CreateDNSRoute failed: %v", err)
	}
	t.Log("3. Created DNS route")

	// Verify tunnel exists
	foundID, err := mgr.FindTunnelIDByName(ctx, name)
	if err != nil {
		t.Errorf("FindTunnelIDByName failed: %v", err)
	}
	if foundID != tunnelID {
		t.Errorf("Expected tunnel ID %s, got %s", tunnelID, foundID)
	}
	t.Log("4. Verified tunnel exists via API")

	// Delete DNS route
	if err := mgr.DeleteDNSRoute(ctx, hostname); err != nil {
		t.Errorf("DeleteDNSRoute failed: %v", err)
	}
	t.Log("5. Deleted DNS route")

	// Delete tunnel
	if err := mgr.DeleteTunnel(ctx, tunnelID); err != nil {
		t.Fatalf("DeleteTunnel failed: %v", err)
	}
	t.Log("6. Deleted tunnel")

	// Verify tunnel is gone
	_, err = mgr.FindTunnelIDByName(ctx, name)
	if err == nil {
		t.Error("Expected tunnel to be deleted, but FindTunnelIDByName succeeded")
	}
	t.Log("7. Verified tunnel is deleted")

	t.Log("Tunnel lifecycle test passed")
}

// ============================================================================
// Tunnel E2E Tests (traffic through real Cloudflare tunnel)
// ============================================================================

func TestTunnel_HealthE2E(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)

	srv := agentapi.NewServer(cfg.AgentToken, orch, "", nil, "", nil, nil, nil, false, nil, "")
	proxyServer := httptest.NewServer(srv.ProxyRouter())
	defer proxyServer.Close()

	// Create VM
	vmCfg := generateTestVMConfig(0)
	if err := orch.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	// Setup shared tunnel pointing to local proxy
	te := setupSharedTunnel(t, proxyServer.URL)

	// Wait for tunnel to be ready
	tunnelHealthURL := fmt.Sprintf("https://%s/health", te.hostname)
	waitForTunnelReady(t, tunnelHealthURL, 60*time.Second)

	// Check VM health through tunnel
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	healthURL := fmt.Sprintf("https://%s/proxy/%s/health?token=%s",
		te.hostname, vmCfg.MachineID, vmCfg.ProxyToken)

	resp, err := client.Get(healthURL)
	if err != nil {
		t.Fatalf("Health check through tunnel failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}

	t.Log("Health check through Cloudflare tunnel passed")
}

func TestTunnel_TerminalE2E(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)

	srv := agentapi.NewServer(cfg.AgentToken, orch, "", nil, "", nil, nil, nil, false, nil, "")
	proxyServer := httptest.NewServer(srv.ProxyRouter())
	defer proxyServer.Close()

	// Create VM
	vmCfg := generateTestVMConfig(0)
	if err := orch.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)

	// Wait for PTY to be ready
	time.Sleep(3 * time.Second)

	// Setup shared tunnel
	te := setupSharedTunnel(t, proxyServer.URL)
	tunnelHealthURL := fmt.Sprintf("https://%s/health", te.hostname)
	waitForTunnelReady(t, tunnelHealthURL, 60*time.Second)

	// Connect to terminal through tunnel
	wsURL := fmt.Sprintf("wss://%s/proxy/%s/terminal/ws?token=%s",
		te.hostname, vmCfg.MachineID, vmCfg.ProxyToken)

	t.Logf("Connecting to terminal through tunnel: %s", wsURL)

	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
	}

	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket dial through tunnel failed: %v", err)
	}
	defer conn.Close()

	// Send resize
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte(`1{"columns":80,"rows":24}`)); err != nil {
		t.Fatalf("Failed to send resize: %v", err)
	}

	// Send echo command
	marker := fmt.Sprintf("TUNNEL_TEST_%s", randomID())
	cmd := fmt.Sprintf("0echo %s\n", marker)
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte(cmd)); err != nil {
		t.Fatalf("Failed to send command: %v", err)
	}

	// Read output and look for marker
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	found := false
	for i := 0; i < 50; i++ {
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
		t.Error("Did not receive echo output through tunnel")
	} else {
		t.Log("Terminal echo test through Cloudflare tunnel passed")
	}
}

func TestTunnel_GatewayE2E(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)

	srv := agentapi.NewServer(cfg.AgentToken, orch, "", nil, "", nil, nil, nil, false, nil, "")
	proxyServer := httptest.NewServer(srv.ProxyRouter())
	defer proxyServer.Close()

	// Create VM
	vmCfg := generateTestVMConfig(0)
	if err := orch.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	// Setup shared tunnel
	te := setupSharedTunnel(t, proxyServer.URL)
	tunnelHealthURL := fmt.Sprintf("https://%s/health", te.hostname)
	waitForTunnelReady(t, tunnelHealthURL, 60*time.Second)

	// Check gateway health through tunnel
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	gatewayURL := fmt.Sprintf("https://%s/proxy/%s/gateway/health?token=%s",
		te.hostname, vmCfg.MachineID, vmCfg.ProxyToken)

	resp, err := client.Get(gatewayURL)
	if err != nil {
		t.Fatalf("Gateway health through tunnel failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}

	t.Log("Gateway health check through Cloudflare tunnel passed")
}
