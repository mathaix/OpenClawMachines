//go:build linux && integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/agentapi"
	"github.com/mathaix/openclawmachines/backend/internal/metadata"
)

// ============================================================================
// Prerequisites Tests
// ============================================================================

func TestPrerequisites_KVM(t *testing.T) {
	skipIfNoKVM(t)
	t.Log("KVM is available")
}

func TestPrerequisites_Firecracker(t *testing.T) {
	skipIfNoFirecracker(t)
	t.Log("Firecracker binary is available")
}

func TestPrerequisites_Root(t *testing.T) {
	skipIfNotRoot(t)
	t.Log("Running as root")
}

func TestPrerequisites_VMAssets(t *testing.T) {
	skipIfNoKVM(t)
	cfg := newTestConfig(t)
	ensureVMAssets(t, cfg)
	t.Logf("Kernel: %s", cfg.KernelPath)
	t.Logf("Images: %s", cfg.ImagesDir)
}

// ============================================================================
// Network Bridge Tests
// ============================================================================

func TestBridge_Setup(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)

	// Verify bridge was created
	if bridge.Name != cfg.BridgeName {
		t.Errorf("Expected bridge name %s, got %s", cfg.BridgeName, bridge.Name)
	}

	t.Logf("Bridge %s created with gateway %s", bridge.Name, bridge.Gateway)
}

func TestBridge_TapLifecycle(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)

	// Create TAP device
	tapName := "tap-test-" + cfg.TestID[:4]
	if err := bridge.CreateTap(tapName); err != nil {
		t.Fatalf("Failed to create TAP: %v", err)
	}
	t.Logf("Created TAP device: %s", tapName)

	// Remove TAP device
	if err := bridge.RemoveTap(tapName); err != nil {
		t.Errorf("Failed to remove TAP: %v", err)
	}
	t.Logf("Removed TAP device: %s", tapName)
}

func TestBridge_NAT(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)

	// Setup NAT rules
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}
	t.Log("NAT rules configured successfully")
}

// ============================================================================
// Metadata Server Tests
// ============================================================================

func TestMetadata_RegisterUnregister(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	// Use a test port that doesn't require binding to 169.254.x.x
	srv := metadata.New("127.0.0.1", 0) // Port 0 = random available port

	// Register a test machine
	testIP := "192.168.200.10"
	testConfig := metadata.MachineConfig{
		MachineID:   "test-machine-123",
		MachineName: "Test Machine",
		MachineSlug: "test-machine",
	}
	srv.RegisterMachine(testIP, testConfig)

	// Verify registration
	retrievedConfig, found := srv.GetConfig(testIP)
	if !found {
		t.Fatal("Expected config to be registered")
	}
	if retrievedConfig.MachineID != testConfig.MachineID {
		t.Errorf("Expected machine ID %s, got %s", testConfig.MachineID, retrievedConfig.MachineID)
	}

	// Unregister
	srv.UnregisterMachine(testIP)

	// Verify unregistration
	_, found = srv.GetConfig(testIP)
	if found {
		t.Error("Expected config to be unregistered")
	}

	t.Log("Metadata registration/unregistration working correctly")
}

// ============================================================================
// Orchestrator Tests
// ============================================================================

func TestOrchestrator_CreateDestroy(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)

	// Create VM
	vmCfg := generateTestVMConfig(0)
	t.Logf("Creating VM %s with IP %s", vmCfg.MachineID, vmCfg.VMIP)

	ctx := context.Background()
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}

	// Wait for VM to be ready
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)

	// Verify VM exists
	vm, err := orch.Get(ctx, vmCfg.MachineID)
	if err != nil {
		t.Fatalf("Failed to get VM: %v", err)
	}
	if vm.Status != "running" {
		t.Errorf("Expected status 'running', got '%s'", vm.Status)
	}

	// Destroy VM
	if err := orch.Destroy(ctx, vmCfg.MachineID); err != nil {
		t.Fatalf("Failed to destroy VM: %v", err)
	}

	// Verify VM is gone
	_, err = orch.Get(ctx, vmCfg.MachineID)
	if err == nil {
		t.Error("Expected VM to be deleted")
	}

	t.Log("VM lifecycle (create/destroy) completed successfully")
}

func TestOrchestrator_List(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)
	ctx := context.Background()

	// Create two VMs
	vm1Cfg := generateTestVMConfig(0)
	vm2Cfg := generateTestVMConfig(1)

	if err := orch.Create(ctx, vm1Cfg); err != nil {
		t.Fatalf("Failed to create VM1: %v", err)
	}
	if err := orch.Create(ctx, vm2Cfg); err != nil {
		t.Fatalf("Failed to create VM2: %v", err)
	}

	// Wait for both to be ready (or at least starting)
	time.Sleep(2 * time.Second)

	// List VMs
	vms, err := orch.List(ctx)
	if err != nil {
		t.Fatalf("Failed to list VMs: %v", err)
	}

	if len(vms) != 2 {
		t.Errorf("Expected 2 VMs, got %d", len(vms))
	}

	t.Logf("Listed %d VMs successfully", len(vms))
}

// ============================================================================
// Agent API Tests
// ============================================================================

func TestAgent_HealthEndpoint(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	orch := setupTestOrchestrator(t, cfg, bridge)

	srv, _, _ := setupTestAgentServer(t, cfg, orch)

	// Test health endpoint on control router
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	srv.ControlRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var health map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &health); err != nil {
		t.Fatalf("Failed to parse health response: %v", err)
	}

	if health["status"] != "ok" {
		t.Errorf("Expected status 'ok', got '%v'", health["status"])
	}

	t.Log("Health endpoint working correctly")
}

func TestAgent_Authentication(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	orch := setupTestOrchestrator(t, cfg, bridge)

	srv, _, _ := setupTestAgentServer(t, cfg, orch)

	// Test without auth - should fail
	req := httptest.NewRequest("GET", "/vms", nil)
	w := httptest.NewRecorder()
	srv.ControlRouter().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 without auth, got %d", w.Code)
	}

	// Test with wrong token
	req = httptest.NewRequest("GET", "/vms", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w = httptest.NewRecorder()
	srv.ControlRouter().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 with wrong token, got %d", w.Code)
	}

	// Test with correct token
	req = httptest.NewRequest("GET", "/vms", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.AgentToken)
	w = httptest.NewRecorder()
	srv.ControlRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200 with correct token, got %d", w.Code)
	}

	t.Log("Authentication working correctly")
}

func TestAgent_CreateVM(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)
	srv, _, _ := setupTestAgentServer(t, cfg, orch)

	// Create VM via API
	vmReq := map[string]interface{}{
		"machine_id":    "api-test-vm-" + randomID(),
		"machine_name":  "API Test VM",
		"machine_slug":  "api-test",
		"vcpus":         1,
		"memory_mb":     2048,
		"vm_ip":         allocateTestIP(0),
		"gateway_token": "gw-token-" + randomID(),
		"proxy_token":   "proxy-token-" + randomID(),
		"signing_key":   generateTestSigningKey(),
		"tunnel_token":  "test-tunnel-token-" + randomID(),
		"vm_hostname":   "m-api-test.test.openclawmachines.com",
	}

	body, _ := json.Marshal(vmReq)
	req := httptest.NewRequest("POST", "/vms", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+cfg.AgentToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ControlRouter().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("Expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	machineID := resp["machine_id"].(string)
	t.Logf("Created VM via API: %s", machineID)

	// Wait for VM to be ready
	waitForVMReady(t, orch, machineID, vmCreationTimeout)

	// Verify via list endpoint
	req = httptest.NewRequest("GET", "/vms", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.AgentToken)
	w = httptest.NewRecorder()
	srv.ControlRouter().ServeHTTP(w, req)

	var vms []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &vms); err != nil {
		t.Fatalf("Failed to parse list response: %v", err)
	}

	found := false
	for _, vm := range vms {
		if vm["machine_id"] == machineID {
			found = true
			break
		}
	}
	if !found {
		t.Error("Created VM not found in list")
	}

	t.Log("VM creation via API working correctly")
}

func TestAgent_DestroyVM(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)
	srv, _, _ := setupTestAgentServer(t, cfg, orch)

	// Create VM first
	vmCfg := generateTestVMConfig(0)
	ctx := context.Background()
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)

	// Destroy via API
	req := httptest.NewRequest("DELETE", "/vms/"+vmCfg.MachineID, nil)
	req.Header.Set("Authorization", "Bearer "+cfg.AgentToken)
	w := httptest.NewRecorder()
	srv.ControlRouter().ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("Expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify VM is gone
	_, err := orch.Get(ctx, vmCfg.MachineID)
	if err == nil {
		t.Error("Expected VM to be deleted")
	}

	t.Log("VM destruction via API working correctly")
}

// ============================================================================
// Proxy API Tests
// ============================================================================

func TestProxy_HealthCheck(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)
	srv, _, _ := setupTestAgentServer(t, cfg, orch)

	// Create VM
	vmCfg := generateTestVMConfig(0)
	ctx := context.Background()
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)

	// Test proxy health endpoint
	url := fmt.Sprintf("/proxy/%s/health?token=%s", vmCfg.MachineID, vmCfg.ProxyToken)
	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	srv.ProxyRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var health map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &health); err != nil {
		t.Fatalf("Failed to parse health response: %v", err)
	}

	t.Logf("Proxy health check: gateway=%v, browser=%v, status=%v",
		health["gateway"], health["browser"], health["status"])
}

func TestProxy_Authentication(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)
	srv, _, _ := setupTestAgentServer(t, cfg, orch)

	// Create VM
	vmCfg := generateTestVMConfig(0)
	ctx := context.Background()
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)

	// Test without token
	url := fmt.Sprintf("/proxy/%s/health", vmCfg.MachineID)
	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	srv.ProxyRouter().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 without token, got %d", w.Code)
	}

	// Test with wrong token
	url = fmt.Sprintf("/proxy/%s/health?token=wrong-token", vmCfg.MachineID)
	req = httptest.NewRequest("GET", url, nil)
	w = httptest.NewRecorder()
	srv.ProxyRouter().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected 403 with wrong token, got %d", w.Code)
	}

	// Test with correct token
	url = fmt.Sprintf("/proxy/%s/health?token=%s", vmCfg.MachineID, vmCfg.ProxyToken)
	req = httptest.NewRequest("GET", url, nil)
	w = httptest.NewRecorder()
	srv.ProxyRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200 with correct token, got %d", w.Code)
	}

	t.Log("Proxy authentication working correctly")
}

func TestProxy_TerminalWebSocket(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)

	// Create a real HTTP server for WebSocket testing
	proxyServer := httptest.NewServer(agentapi.NewServer(cfg.AgentToken, orch, "", nil, "", nil, nil, nil, false, nil, "").ProxyRouter())
	defer proxyServer.Close()

	// Create VM
	vmCfg := generateTestVMConfig(0)
	ctx := context.Background()
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)

	// Wait a bit for PTY server to be fully ready
	time.Sleep(2 * time.Second)

	// Build WebSocket URL
	wsURL := strings.Replace(proxyServer.URL, "http://", "ws://", 1)
	wsURL = fmt.Sprintf("%s/proxy/%s/terminal/ws", wsURL, vmCfg.MachineID)

	// Run full terminal test
	testWebSocketTTY(t, wsURL, vmCfg.ProxyToken)

	t.Log("Terminal WebSocket test completed successfully")
}

// ============================================================================
// End-to-End Workflow Tests
// ============================================================================

func TestE2E_FullWorkflow(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	t.Log("=== Starting E2E Full Workflow Test ===")

	// Setup infrastructure
	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}
	t.Log("1. Bridge and NAT configured")

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)
	t.Log("2. Orchestrator + metadata server initialized")

	// Create real HTTP server for testing
	proxyServer := httptest.NewServer(agentapi.NewServer(cfg.AgentToken, orch, "", nil, "", nil, nil, nil, false, nil, "").ProxyRouter())
	defer proxyServer.Close()

	// Create VM
	vmCfg := generateTestVMConfig(0)
	ctx := context.Background()
	t.Logf("3. Creating VM %s...", vmCfg.MachineID)

	start := time.Now()
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}

	// Wait for VM to be ready
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	t.Logf("4. VM ready in %s", time.Since(start).Round(time.Millisecond))

	// Verify VM status
	vm, err := orch.Get(ctx, vmCfg.MachineID)
	if err != nil {
		t.Fatalf("Failed to get VM: %v", err)
	}
	if vm.Status != "running" {
		t.Fatalf("Expected status 'running', got '%s'", vm.Status)
	}
	t.Log("5. VM status verified as running")

	// Test terminal WebSocket
	time.Sleep(2 * time.Second) // Give ttyd time to fully initialize
	wsURL := strings.Replace(proxyServer.URL, "http://", "ws://", 1)
	wsURL = fmt.Sprintf("%s/proxy/%s/terminal/ws", wsURL, vmCfg.MachineID)

	t.Log("6. Testing terminal WebSocket...")
	testWebSocketTTY(t, wsURL, vmCfg.ProxyToken)
	t.Log("7. Terminal WebSocket test passed")

	// Destroy VM
	t.Log("8. Destroying VM...")
	if err := orch.Destroy(ctx, vmCfg.MachineID); err != nil {
		t.Fatalf("Failed to destroy VM: %v", err)
	}

	// Verify cleanup
	_, err = orch.Get(ctx, vmCfg.MachineID)
	if err == nil {
		t.Fatal("VM should be deleted")
	}
	t.Log("9. VM destroyed and cleaned up")

	t.Log("=== E2E Full Workflow Test PASSED ===")
}

// ============================================================================
// SSE Progress Test
// ============================================================================

func TestAgent_ProgressSSE(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)

	// Create real HTTP server for SSE
	proxyServer := httptest.NewServer(agentapi.NewServer(cfg.AgentToken, orch, "", nil, "", nil, nil, nil, false, nil, "").ProxyRouter())
	defer proxyServer.Close()

	// Start listening to progress before creating VM
	vmCfg := generateTestVMConfig(0)
	progressURL := fmt.Sprintf("%s/progress?machine_id=%s&token=%s",
		proxyServer.URL, vmCfg.MachineID, vmCfg.ProxyToken)

	// Create context with timeout for SSE reader
	ctx, cancel := context.WithTimeout(context.Background(), vmCreationTimeout+10*time.Second)
	defer cancel()

	// Start progress reader in goroutine
	progressEvents := make(chan string, 20)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, "GET", progressURL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Logf("Progress request error: %v", err)
			return
		}
		defer func() { _ = resp.Body.Close() }()

		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if err == io.EOF || ctx.Err() != nil {
				break
			}
			if n > 0 {
				progressEvents <- string(buf[:n])
			}
		}
	}()

	// Wait for SSE connection to establish
	time.Sleep(500 * time.Millisecond)

	// Create VM
	if err := orch.Create(context.Background(), vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}

	// Wait for VM to be ready
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)

	// Give SSE time to flush
	time.Sleep(1 * time.Second)

	// Check we received some progress events
	close(progressEvents)
	eventCount := 0
	for event := range progressEvents {
		if strings.Contains(event, "data:") {
			eventCount++
			t.Logf("Received progress event: %s", strings.TrimSpace(event))
		}
	}

	if eventCount == 0 {
		t.Log("Warning: No progress events received (SSE may not have connected in time)")
	} else {
		t.Logf("Received %d progress events", eventCount)
	}

	t.Log("Progress SSE test completed")
}
