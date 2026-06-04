//go:build linux && integration

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/agentapi"
)

// ============================================================================
// Logging SSE Tests
// ============================================================================

func TestLogging_SSE(t *testing.T) {
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

	// Inject test log entries into the server's log manager.
	// In production, the VM handler writes these; in tests we inject them manually.
	logMgr := srv.LogManagerRef()
	logMgr.Append(vmCfg.MachineID, "Metadata nonce: abc123")
	logMgr.Append(vmCfg.MachineID, "PTY server started on port 7681")
	logMgr.Append(vmCfg.MachineID, "Gateway started")

	// Collect log entries (late subscriber gets buffered history)
	entries := collectLogSSE(t, proxyServer.URL, vmCfg.MachineID, vmCfg.ProxyToken, 15*time.Second)

	if len(entries) == 0 {
		t.Fatal("No log entries received from SSE stream")
	}

	t.Logf("Received %d log entries", len(entries))

	// Assert init sequence entries are present
	assertLogContains(t, entries, []string{
		"Metadata nonce",
		"PTY server started",
	})
}

func TestLogging_SSEContentType(t *testing.T) {
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

	// Inject a log entry so the SSE handler has data to send
	// (otherwise the handler blocks before sending headers)
	logMgr := srv.LogManagerRef()
	logMgr.Append(vmCfg.MachineID, "test log entry")

	// Verify SSE response headers
	url := fmt.Sprintf("%s/proxy/%s/logs?token=%s",
		proxyServer.URL, vmCfg.MachineID, vmCfg.ProxyToken)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Log SSE request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/event-stream") {
		t.Errorf("Expected Content-Type text/event-stream, got %q", contentType)
	}

	cacheControl := resp.Header.Get("Cache-Control")
	if !strings.Contains(cacheControl, "no-cache") {
		t.Errorf("Expected Cache-Control no-cache, got %q", cacheControl)
	}

	t.Log("SSE response headers are correct")
}

func TestLogging_ProgressFullSequence(t *testing.T) {
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

	vmCfg := generateTestVMConfig(0)

	// Create VM via control API (progress is emitted by the handler layer)
	controlServer := httptest.NewServer(srv.ControlRouter())
	defer controlServer.Close()

	vmReq := map[string]interface{}{
		"machine_id":    vmCfg.MachineID,
		"machine_name":  vmCfg.MachineName,
		"machine_slug":  vmCfg.MachineSlug,
		"vcpus":         vmCfg.VCPUs,
		"memory_mb":     vmCfg.MemoryMB,
		"vm_ip":         vmCfg.VMIP,
		"gateway_token": vmCfg.GatewayToken,
		"proxy_token":   vmCfg.ProxyToken,
		"signing_key":   vmCfg.SigningKey,
		"tunnel_token":  vmCfg.TunnelToken,
		"vm_hostname":   vmCfg.VmHostname,
	}
	body, _ := json.Marshal(vmReq)

	req, _ := http.NewRequest("POST", controlServer.URL+"/vms", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+cfg.AgentToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to create VM via API: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected 201, got %d", resp.StatusCode)
	}

	// Wait for VM to be ready
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)

	// Now subscribe to progress — late subscriber gets history replay
	progressURL := fmt.Sprintf("%s/proxy/%s/progress?token=%s",
		proxyServer.URL, vmCfg.MachineID, vmCfg.ProxyToken)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	progressReq, _ := http.NewRequestWithContext(ctx, "GET", progressURL, nil)
	progressResp, err := http.DefaultClient.Do(progressReq)
	if err != nil {
		t.Fatalf("Progress SSE request failed: %v", err)
	}
	defer progressResp.Body.Close()

	// Read progress events from the SSE stream
	var steps []string
	reader := bufio.NewReader(progressResp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(data), &parsed); err == nil {
				if step, ok := parsed["step"].(string); ok {
					steps = append(steps, step)
				}
			}
		}
	}

	t.Logf("Received progress steps: %v", steps)

	// Verify expected steps are present (from history replay)
	expectedSteps := []string{"allocating", "rootfs", "network", "booting"}
	for _, expected := range expectedSteps {
		found := false
		for _, step := range steps {
			if step == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected progress step %q not found in %v", expected, steps)
		}
	}

	if len(steps) == 0 {
		t.Error("No progress events received")
	}
}
