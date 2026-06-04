//go:build linux && integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/agentapi"
	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/orchestrator"
)

// TestRecovery_AgentRestartPreservesRunningVM boots a VM, tears down the
// orchestrator and metadata server (simulating an agent restart), constructs
// new instances from the same state directory, calls Recover, and verifies
// the VM is still running and accessible via terminal through the new
// metadata server.
func TestRecovery_AgentRestartPreservesRunningVM(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	// --- First orchestrator: create a VM ---
	orch1 := setupTestOrchestrator(t, cfg, bridge)
	metaSrv1, stopMetaSrv1 := setupTestMetadataServerWithCancel(t, bridge.Gateway)
	orch1.SetMetadataRegistrar(metaSrv1)

	vmCfg := generateTestVMConfig(0)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: getStableOpenClawVersion(t),
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
		OpenClawManifestURI:     defaultOpenClawManifestURI,
	}
	vmCfg.DataVolumeGB = 5

	if err := orch1.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch1, vmCfg.MachineID, vmCreationTimeout)

	// Verify VM is serving PTY before restart
	proxyServer1 := httptest.NewServer(agentapi.NewServer(cfg.AgentToken, orch1, "", nil, "", nil, nil, nil, false, nil, "").ProxyRouter())
	wsURL1 := strings.Replace(proxyServer1.URL, "http://", "ws://", 1)
	wsURL1 = fmt.Sprintf("%s/proxy/%s/terminal/ws", wsURL1, vmCfg.MachineID)
	output := testTerminalCommandSplit(t, wsURL1, vmCfg.ProxyToken, "echo BEFORE_RESTART", 15*time.Second)
	if !strings.Contains(output, "BEFORE_RESTART") {
		t.Fatalf("expected terminal to work before restart, output: %q", output)
	}
	proxyServer1.Close()
	t.Log("1. VM created and terminal verified before restart")

	// Record the PID so we can confirm it's the same process after recovery
	vm1, err := orch1.Get(t.Context(), vmCfg.MachineID)
	if err != nil {
		t.Fatalf("get VM before restart: %v", err)
	}
	originalPID := vm1.PID
	t.Logf("2. VM PID before restart: %d", originalPID)

	// --- Simulate agent restart: persist state, stop metadata server ---
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := orch1.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown orch1: %v", err)
	}
	shutdownCancel()
	stopMetaSrv1()
	t.Log("3. First orchestrator and metadata server shut down (VM left running)")

	// --- Second orchestrator: recover from persisted state ---
	orch2 := setupTestOrchestrator(t, cfg, bridge)
	metaSrv2 := setupTestMetadataServer(t, bridge.Gateway)
	orch2.SetMetadataRegistrar(metaSrv2)

	// Verify metaSrv2 is actually listening on the network
	waitForMetadataServerReady(t, bridge.Gateway, 2*time.Second)

	if err := orch2.Recover(t.Context()); err != nil {
		t.Fatalf("recover orch2: %v", err)
	}
	t.Log("4. Second orchestrator recovered with new metadata server")

	// Verify VM is recovered with same PID
	vm2, err := orch2.Get(t.Context(), vmCfg.MachineID)
	if err != nil {
		t.Fatalf("get recovered VM: %v", err)
	}
	if vm2.Status != "running" {
		t.Fatalf("expected recovered VM status running, got %s", vm2.Status)
	}
	if vm2.PID != originalPID {
		t.Fatalf("expected same PID %d after recovery, got %d", originalPID, vm2.PID)
	}
	t.Logf("5. VM recovered with same PID: %d", vm2.PID)

	// Verify terminal still works through the new orchestrator
	proxyServer2 := httptest.NewServer(agentapi.NewServer(cfg.AgentToken, orch2, "", nil, "", nil, nil, nil, false, nil, "").ProxyRouter())
	t.Cleanup(func() { proxyServer2.Close() })
	wsURL2 := strings.Replace(proxyServer2.URL, "http://", "ws://", 1)
	wsURL2 = fmt.Sprintf("%s/proxy/%s/terminal/ws", wsURL2, vmCfg.MachineID)
	output = testTerminalCommandSplit(t, wsURL2, vmCfg.ProxyToken, "echo AFTER_RESTART", 15*time.Second)
	if !strings.Contains(output, "AFTER_RESTART") {
		t.Fatalf("expected terminal to work after restart, output: %q", output)
	}
	t.Log("6. Terminal works after recovery — VM survived agent restart")
}

// TestRecovery_DeadVMCleanedOnRestart boots a VM, kills the Firecracker
// process, then simulates an agent restart. The dead VM should be cleaned
// up (not reattached).
func TestRecovery_DeadVMCleanedOnRestart(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch1 := setupTestOrchestrator(t, cfg, bridge)
	metaSrv1, stopMetaSrv1 := setupTestMetadataServerWithCancel(t, bridge.Gateway)
	orch1.SetMetadataRegistrar(metaSrv1)

	vmCfg := generateTestVMConfig(0)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: getStableOpenClawVersion(t),
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
		OpenClawManifestURI:     defaultOpenClawManifestURI,
	}
	vmCfg.DataVolumeGB = 5

	if err := orch1.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch1, vmCfg.MachineID, vmCreationTimeout)

	vm1, err := orch1.Get(t.Context(), vmCfg.MachineID)
	if err != nil {
		t.Fatalf("get VM: %v", err)
	}
	t.Logf("1. VM running with PID %d", vm1.PID)

	// Persist state while VM is still in the map, then kill externally
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := orch1.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown orch1: %v", err)
	}
	shutdownCancel()
	stopMetaSrv1()

	if err := killPID(vm1.PID); err != nil {
		t.Fatalf("kill VM process: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	t.Logf("2. Externally killed VM PID %d", vm1.PID)

	// --- Second orchestrator: recover should clean up the dead VM ---
	orch2 := setupTestOrchestrator(t, cfg, bridge)
	metaSrv2 := setupTestMetadataServer(t, bridge.Gateway)
	orch2.SetMetadataRegistrar(metaSrv2)
	waitForMetadataServerReady(t, bridge.Gateway, 2*time.Second)

	if err := orch2.Recover(t.Context()); err != nil {
		t.Fatalf("recover orch2: %v", err)
	}
	t.Log("3. Second orchestrator recovered")

	_, err = orch2.Get(t.Context(), vmCfg.MachineID)
	if err == nil {
		t.Fatal("expected dead VM to not be recovered, but Get returned it")
	}
	t.Log("4. Dead VM correctly not recovered — cleanup worked")

	vms, err := orch2.List(t.Context())
	if err != nil {
		t.Fatalf("list VMs: %v", err)
	}
	if len(vms) != 0 {
		t.Fatalf("expected 0 VMs after recovery, got %d", len(vms))
	}
	t.Log("5. VM list empty after dead-VM cleanup")
}

// TestRecovery_MetadataRestoredAfterRestart verifies that metadata
// (secrets, config) persisted for a running VM is correctly restored
// to the metadata server after agent restart, and is reachable on the
// network (not just in-memory).
func TestRecovery_MetadataRestoredAfterRestart(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch1 := setupTestOrchestrator(t, cfg, bridge)
	metaSrv1, stopMetaSrv1 := setupTestMetadataServerWithCancel(t, bridge.Gateway)
	orch1.SetMetadataRegistrar(metaSrv1)

	vmCfg := generateTestVMConfig(0)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: getStableOpenClawVersion(t),
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
		OpenClawManifestURI:     defaultOpenClawManifestURI,
	}
	vmCfg.DataVolumeGB = 5
	vmCfg.Secrets = map[string]string{"TEST_SECRET": "secret-value-123"}

	if err := orch1.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch1, vmCfg.MachineID, vmCreationTimeout)
	t.Log("1. VM created with test secret")

	// Update secrets after creation (to test durable mutation)
	if err := orch1.UpdateSecrets(t.Context(), vmCfg.MachineID, map[string]string{
		"TEST_SECRET":  "updated-value-456",
		"EXTRA_SECRET": "extra-value-789",
	}); err != nil {
		t.Fatalf("update secrets: %v", err)
	}
	t.Log("2. Secrets updated after creation")

	// Persist and stop first metadata server
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := orch1.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown orch1: %v", err)
	}
	shutdownCancel()
	stopMetaSrv1()
	t.Log("3. First orchestrator and metadata server stopped")

	// New metadata server (empty) + new orchestrator
	orch2 := setupTestOrchestrator(t, cfg, bridge)
	metaSrv2 := setupTestMetadataServer(t, bridge.Gateway)
	orch2.SetMetadataRegistrar(metaSrv2)

	// Verify metaSrv2 is actually listening
	waitForMetadataServerReady(t, bridge.Gateway, 2*time.Second)

	// Before recovery, new metadata server should have no config for this VM
	_, found := metaSrv2.GetConfig(vmCfg.VMIP)
	if found {
		t.Fatal("expected new metadata server to have no config before recovery")
	}

	if err := orch2.Recover(t.Context()); err != nil {
		t.Fatalf("recover orch2: %v", err)
	}
	t.Log("4. Recovered after restart")

	// Verify metadata was restored — check in-memory state
	mc2, ok := metaSrv2.GetConfig(vmCfg.VMIP)
	if !ok {
		t.Fatal("metadata not found for VM IP after recovery")
	}
	if mc2.Secrets["TEST_SECRET"] != "updated-value-456" {
		t.Fatalf("expected restored secret to be updated value, got %q", mc2.Secrets["TEST_SECRET"])
	}
	if mc2.Secrets["EXTRA_SECRET"] != "extra-value-789" {
		t.Fatalf("expected restored EXTRA_SECRET, got %q", mc2.Secrets["EXTRA_SECRET"])
	}
	if mc2.GatewayToken != vmCfg.GatewayToken {
		t.Fatalf("expected restored gateway token %q, got %q", vmCfg.GatewayToken, mc2.GatewayToken)
	}
	t.Log("5. In-memory metadata restored correctly")

	// Verify metadata is reachable on the network (not just in-memory).
	// Hit the /health endpoint on the bridge IP to prove metaSrv2 owns port 80.
	healthURL := fmt.Sprintf("http://%s:80/health", bridge.Gateway)
	resp, err := http.Get(healthURL)
	if err != nil {
		t.Fatalf("metadata server /health unreachable after recovery: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metadata server /health returned %d, expected 200", resp.StatusCode)
	}
	t.Log("6. Metadata server reachable on network after restart")
}

func TestRecovery_UnresolvedVMIsQuarantinedAndLaterReconciled(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch1 := setupTestOrchestrator(t, cfg, bridge)
	metaSrv1, stopMetaSrv1 := setupTestMetadataServerWithCancel(t, bridge.Gateway)
	orch1.SetMetadataRegistrar(metaSrv1)

	vmCfg := generateTestVMConfig(0)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: getStableOpenClawVersion(t),
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
		OpenClawManifestURI:     defaultOpenClawManifestURI,
	}
	vmCfg.DataVolumeGB = 5

	if err := orch1.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch1, vmCfg.MachineID, vmCreationTimeout)

	vm1, err := orch1.Get(t.Context(), vmCfg.MachineID)
	if err != nil {
		t.Fatalf("get VM before restart: %v", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := orch1.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown orch1: %v", err)
	}
	shutdownCancel()
	stopMetaSrv1()

	if err := os.Remove(vm1.SocketPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove socket to force quarantine: %v", err)
	}

	orch2 := setupTestOrchestrator(t, cfg, bridge)
	metaSrv2 := setupTestMetadataServer(t, bridge.Gateway)
	orch2.SetMetadataRegistrar(metaSrv2)
	waitForMetadataServerReady(t, bridge.Gateway, 2*time.Second)

	if err := orch2.Recover(t.Context()); err != nil {
		t.Fatalf("recover orch2: %v", err)
	}
	if _, err := orch2.Get(t.Context(), vmCfg.MachineID); err == nil {
		t.Fatal("expected unresolved VM to stay out of active map")
	}

	statePath := filepath.Join(cfg.StateDir, "vms.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file after quarantine: %v", err)
	}
	var persisted []map[string]any
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("unmarshal state file after quarantine: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("expected 1 persisted VM after quarantine, got %d", len(persisted))
	}
	if persisted[0]["quarantined"] != true {
		t.Fatalf("expected quarantined=true, got %#v", persisted[0]["quarantined"])
	}
	if persisted[0]["quarantine_reason"] != "socket_missing" {
		t.Fatalf("expected quarantine_reason=socket_missing, got %#v", persisted[0]["quarantine_reason"])
	}

	if err := killPID(vm1.PID); err != nil {
		t.Fatalf("kill quarantined VM process: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	orch3 := setupTestOrchestrator(t, cfg, bridge)
	metaSrv3 := setupTestMetadataServer(t, bridge.Gateway)
	orch3.SetMetadataRegistrar(metaSrv3)
	waitForMetadataServerReady(t, bridge.Gateway, 2*time.Second)

	if err := orch3.Recover(t.Context()); err != nil {
		t.Fatalf("recover orch3: %v", err)
	}

	data, err = os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file after reconciliation: %v", err)
	}
	var finalState []map[string]any
	if err := json.Unmarshal(data, &finalState); err != nil {
		t.Fatalf("unmarshal final state file: %v", err)
	}
	if len(finalState) != 0 {
		t.Fatalf("expected quarantined record to be removed after PID died, got %d entries", len(finalState))
	}
}

func TestRecovery_UnexpectedPIDIsQuarantinedAndLaterReconciled(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch1 := setupTestOrchestrator(t, cfg, bridge)
	metaSrv1, stopMetaSrv1 := setupTestMetadataServerWithCancel(t, bridge.Gateway)
	orch1.SetMetadataRegistrar(metaSrv1)

	vmCfg := generateTestVMConfig(0)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: getStableOpenClawVersion(t),
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
		OpenClawManifestURI:     defaultOpenClawManifestURI,
	}
	vmCfg.DataVolumeGB = 5

	if err := orch1.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch1, vmCfg.MachineID, vmCreationTimeout)

	vm1, err := orch1.Get(t.Context(), vmCfg.MachineID)
	if err != nil {
		t.Fatalf("get VM before restart: %v", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := orch1.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown orch1: %v", err)
	}
	shutdownCancel()
	stopMetaSrv1()

	fakePIDCmd := exec.Command("sleep", "30")
	if err := fakePIDCmd.Start(); err != nil {
		t.Fatalf("start fake PID process: %v", err)
	}
	t.Cleanup(func() {
		_ = fakePIDCmd.Process.Kill()
		_, _ = fakePIDCmd.Process.Wait()
	})

	statePath := filepath.Join(cfg.StateDir, "vms.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file before PID rewrite: %v", err)
	}
	var persisted []map[string]any
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("unmarshal state file before PID rewrite: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("expected 1 persisted VM before PID rewrite, got %d", len(persisted))
	}
	persisted[0]["pid"] = fakePIDCmd.Process.Pid
	data, err = json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		t.Fatalf("marshal state file after PID rewrite: %v", err)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatalf("write state file after PID rewrite: %v", err)
	}

	orch2 := setupTestOrchestrator(t, cfg, bridge)
	metaSrv2 := setupTestMetadataServer(t, bridge.Gateway)
	orch2.SetMetadataRegistrar(metaSrv2)
	waitForMetadataServerReady(t, bridge.Gateway, 2*time.Second)

	if err := orch2.Recover(t.Context()); err != nil {
		t.Fatalf("recover orch2: %v", err)
	}
	if _, err := orch2.Get(t.Context(), vmCfg.MachineID); err == nil {
		t.Fatal("expected VM with non-firecracker PID to stay out of active map")
	}

	data, err = os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file after quarantine: %v", err)
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("unmarshal state file after quarantine: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("expected 1 persisted VM after quarantine, got %d", len(persisted))
	}
	if persisted[0]["quarantined"] != true {
		t.Fatalf("expected quarantined=true, got %#v", persisted[0]["quarantined"])
	}
	if persisted[0]["quarantine_reason"] != "pid_not_firecracker" {
		t.Fatalf("expected quarantine_reason=pid_not_firecracker, got %#v", persisted[0]["quarantine_reason"])
	}

	if err := killPID(fakePIDCmd.Process.Pid); err != nil {
		t.Fatalf("kill fake PID process: %v", err)
	}
	if err := killPID(vm1.PID); err != nil {
		t.Fatalf("kill original firecracker PID: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	orch3 := setupTestOrchestrator(t, cfg, bridge)
	metaSrv3 := setupTestMetadataServer(t, bridge.Gateway)
	orch3.SetMetadataRegistrar(metaSrv3)
	waitForMetadataServerReady(t, bridge.Gateway, 2*time.Second)

	if err := orch3.Recover(t.Context()); err != nil {
		t.Fatalf("recover orch3: %v", err)
	}

	data, err = os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file after reconciliation: %v", err)
	}
	var finalState []map[string]any
	if err := json.Unmarshal(data, &finalState); err != nil {
		t.Fatalf("unmarshal final state file: %v", err)
	}
	if len(finalState) != 0 {
		t.Fatalf("expected quarantined record to be removed after both PIDs died, got %d entries", len(finalState))
	}
}

func TestRecovery_BrowserVMSurvivesDoubleAgentRestart(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}
	ensureBrowserRootfs(t, cfg)

	manifestURI := os.Getenv("TEST_BROWSER_ROOTFS_GCS_MANIFEST")
	if manifestURI == "" {
		manifestURI = "gs://openclawmachines/kernel-browser-rootfs/manifest.json"
	}

	newBrowserOrchestrator := func() orchestrator.Orchestrator {
		orchCfg := orchestrator.FirecrackerConfig{
			BridgeName:               cfg.BridgeName,
			SocketDir:                cfg.SocketDir,
			RootfsDir:                cfg.ImagesDir,
			StateDir:                 cfg.StateDir,
			DataDir:                  filepath.Join(cfg.StateDir, "data"),
			RuntimeStateDir:          filepath.Join(cfg.StateDir, "runtime"),
			OpenClawRuntimeDir:       filepath.Join(cfg.StateDir, "openclaw"),
			OpenClawManifestURI:      cfg.OpenClawManifestURI,
			GCSBrowserRootfsManifest: manifestURI,
			GCSDownloadTimeout:       2 * time.Minute,
			GCSRetryAttempts:         1,
			KernelPath:               cfg.KernelPath,
			DefaultVCPUs:             1,
			DefaultMemoryMB:          2048,
			RuntimeOwnerKind:         cfg.RuntimeOwnerKind,
			OpenClawDownloadTimeout:  30 * time.Second,
			OpenClawRetryAttempts:    1,
		}
		orch, err := orchestrator.New(orchCfg, bridge)
		if err != nil {
			t.Fatalf("create browser orchestrator: %v", err)
		}
		return orch
	}

	orch1 := newBrowserOrchestrator()
	bvmID := "browser-" + cfg.TestID
	bvmCfg := orchestrator.BrowserVMConfig{
		BrowserVMID: bvmID,
		VMIP:        "192.168.200.50",
		VCPUs:       2,
		MemoryMB:    4096,
	}
	if err := orch1.CreateBrowserVM(t.Context(), bvmCfg); err != nil {
		t.Fatalf("create browser VM: %v", err)
	}

	statePath := filepath.Join(cfg.StateDir, "browser-vms.json")
	readPID := func() int {
		data, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatalf("read browser state file: %v", err)
		}
		var persisted []map[string]any
		if err := json.Unmarshal(data, &persisted); err != nil {
			t.Fatalf("unmarshal browser state file: %v", err)
		}
		if len(persisted) != 1 {
			t.Fatalf("expected 1 persisted browser VM, got %d", len(persisted))
		}
		pidValue, ok := persisted[0]["pid"].(float64)
		if !ok || int(pidValue) <= 0 {
			t.Fatalf("expected persisted browser pid > 0, got %#v", persisted[0]["pid"])
		}
		return int(pidValue)
	}

	originalPID := readPID()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := orch1.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown orch1: %v", err)
	}
	shutdownCancel()

	orch2 := newBrowserOrchestrator()
	if err := orch2.Recover(t.Context()); err != nil {
		t.Fatalf("recover orch2: %v", err)
	}
	bvms, err := orch2.ListBrowserVMs(t.Context())
	if err != nil {
		t.Fatalf("list browser VMs after first recover: %v", err)
	}
	if len(bvms) != 1 {
		t.Fatalf("expected 1 browser VM after first recover, got %d", len(bvms))
	}
	if pid := readPID(); pid != originalPID {
		t.Fatalf("expected browser PID %d after first recover, got %d", originalPID, pid)
	}

	shutdownCtx2, shutdownCancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	if err := orch2.Shutdown(shutdownCtx2); err != nil {
		t.Fatalf("shutdown orch2: %v", err)
	}
	shutdownCancel2()

	orch3 := newBrowserOrchestrator()
	if err := orch3.Recover(t.Context()); err != nil {
		t.Fatalf("recover orch3: %v", err)
	}
	bvms, err = orch3.ListBrowserVMs(t.Context())
	if err != nil {
		t.Fatalf("list browser VMs after second recover: %v", err)
	}
	if len(bvms) != 1 {
		t.Fatalf("expected 1 browser VM after second recover, got %d", len(bvms))
	}
	if pid := readPID(); pid != originalPID {
		t.Fatalf("expected browser PID %d after second recover, got %d", originalPID, pid)
	}

	destroyCtx, destroyCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer destroyCancel()
	if err := orch3.DestroyBrowserVM(destroyCtx, bvmID); err != nil {
		t.Fatalf("destroy recovered browser VM: %v", err)
	}
}

func TestRecovery_BrowserVMQuarantineReservesWebRTCSlotUntilPIDDies(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}
	ensureBrowserRootfs(t, cfg)

	manifestURI := os.Getenv("TEST_BROWSER_ROOTFS_GCS_MANIFEST")
	if manifestURI == "" {
		manifestURI = "gs://openclawmachines/kernel-browser-rootfs/manifest.json"
	}

	newBrowserOrchestrator := func() orchestrator.Orchestrator {
		orchCfg := orchestrator.FirecrackerConfig{
			BridgeName:               cfg.BridgeName,
			SocketDir:                cfg.SocketDir,
			RootfsDir:                cfg.ImagesDir,
			StateDir:                 cfg.StateDir,
			DataDir:                  filepath.Join(cfg.StateDir, "data"),
			RuntimeStateDir:          filepath.Join(cfg.StateDir, "runtime"),
			OpenClawRuntimeDir:       filepath.Join(cfg.StateDir, "openclaw"),
			OpenClawManifestURI:      cfg.OpenClawManifestURI,
			GCSBrowserRootfsManifest: manifestURI,
			GCSDownloadTimeout:       2 * time.Minute,
			GCSRetryAttempts:         1,
			KernelPath:               cfg.KernelPath,
			DefaultVCPUs:             1,
			DefaultMemoryMB:          2048,
			RuntimeOwnerKind:         cfg.RuntimeOwnerKind,
			OpenClawDownloadTimeout:  30 * time.Second,
			OpenClawRetryAttempts:    1,
		}
		orch, err := orchestrator.New(orchCfg, bridge)
		if err != nil {
			t.Fatalf("create browser orchestrator: %v", err)
		}
		return orch
	}

	statePath := filepath.Join(cfg.StateDir, "browser-vms.json")
	readPersisted := func() []map[string]any {
		data, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatalf("read browser state file: %v", err)
		}
		var persisted []map[string]any
		if err := json.Unmarshal(data, &persisted); err != nil {
			t.Fatalf("unmarshal browser state file: %v", err)
		}
		return persisted
	}
	findPersisted := func(id string, persisted []map[string]any) map[string]any {
		for _, entry := range persisted {
			if entry["id"] == id {
				return entry
			}
		}
		t.Fatalf("persisted browser VM %s not found", id)
		return nil
	}

	orch1 := newBrowserOrchestrator()
	bvm1ID := "browser-q1-" + cfg.TestID
	if err := orch1.CreateBrowserVM(t.Context(), orchestrator.BrowserVMConfig{
		BrowserVMID: bvm1ID,
		VMIP:        "192.168.200.70",
		VCPUs:       2,
		MemoryMB:    4096,
	}); err != nil {
		t.Fatalf("create browser VM 1: %v", err)
	}

	original := findPersisted(bvm1ID, readPersisted())
	originalPID := int(original["pid"].(float64))
	originalPortBase := int(original["webrtc_port_base"].(float64))
	socketPath, ok := original["socket_path"].(string)
	if !ok || socketPath == "" {
		t.Fatalf("expected socket_path for browser VM 1, got %#v", original["socket_path"])
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := orch1.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown orch1: %v", err)
	}
	shutdownCancel()

	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove browser socket to force quarantine: %v", err)
	}

	orch2 := newBrowserOrchestrator()
	if err := orch2.Recover(t.Context()); err != nil {
		t.Fatalf("recover orch2: %v", err)
	}
	if bvms, err := orch2.ListBrowserVMs(t.Context()); err != nil {
		t.Fatalf("list browser VMs after quarantine recover: %v", err)
	} else if len(bvms) != 0 {
		t.Fatalf("expected 0 active browser VMs after quarantine recover, got %d", len(bvms))
	}

	persisted := readPersisted()
	if len(persisted) != 1 {
		t.Fatalf("expected 1 persisted browser VM after quarantine, got %d", len(persisted))
	}
	quarantined := findPersisted(bvm1ID, persisted)
	if quarantined["quarantined"] != true {
		t.Fatalf("expected quarantined=true, got %#v", quarantined["quarantined"])
	}
	if quarantined["quarantine_reason"] != "socket_missing" {
		t.Fatalf("expected quarantine_reason=socket_missing, got %#v", quarantined["quarantine_reason"])
	}
	if int(quarantined["webrtc_port_base"].(float64)) != originalPortBase {
		t.Fatalf("expected quarantined browser VM to retain port base %d, got %#v", originalPortBase, quarantined["webrtc_port_base"])
	}

	bvm2ID := "browser-q2-" + cfg.TestID
	if err := orch2.CreateBrowserVM(t.Context(), orchestrator.BrowserVMConfig{
		BrowserVMID: bvm2ID,
		VMIP:        "192.168.200.71",
		VCPUs:       2,
		MemoryMB:    4096,
	}); err != nil {
		t.Fatalf("create browser VM 2 while browser VM 1 is quarantined: %v", err)
	}

	second := findPersisted(bvm2ID, readPersisted())
	secondPortBase := int(second["webrtc_port_base"].(float64))
	if secondPortBase != originalPortBase+100 {
		t.Fatalf("expected browser VM 2 to skip quarantined port base %d and use %d, got %d", originalPortBase, originalPortBase+100, secondPortBase)
	}

	shutdownCtx2, shutdownCancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	if err := orch2.Shutdown(shutdownCtx2); err != nil {
		t.Fatalf("shutdown orch2: %v", err)
	}
	shutdownCancel2()

	if err := killPID(originalPID); err != nil {
		t.Fatalf("kill quarantined browser PID: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	orch3 := newBrowserOrchestrator()
	if err := orch3.Recover(t.Context()); err != nil {
		t.Fatalf("recover orch3: %v", err)
	}

	persisted = readPersisted()
	if len(persisted) != 1 {
		t.Fatalf("expected only browser VM 2 to remain after browser VM 1 cleanup, got %d entries", len(persisted))
	}
	if persisted[0]["id"] != bvm2ID {
		t.Fatalf("expected remaining browser VM to be %s, got %#v", bvm2ID, persisted[0]["id"])
	}

	bvm3ID := "browser-q3-" + cfg.TestID
	if err := orch3.CreateBrowserVM(t.Context(), orchestrator.BrowserVMConfig{
		BrowserVMID: bvm3ID,
		VMIP:        "192.168.200.72",
		VCPUs:       2,
		MemoryMB:    4096,
	}); err != nil {
		t.Fatalf("create browser VM 3 after browser VM 1 cleanup: %v", err)
	}

	third := findPersisted(bvm3ID, readPersisted())
	if thirdPortBase := int(third["webrtc_port_base"].(float64)); thirdPortBase != originalPortBase {
		t.Fatalf("expected browser VM 3 to reuse freed port base %d, got %d", originalPortBase, thirdPortBase)
	}

	destroyCtx, destroyCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer destroyCancel()
	if err := orch3.DestroyBrowserVM(destroyCtx, bvm2ID); err != nil {
		t.Fatalf("destroy browser VM 2: %v", err)
	}
	if err := orch3.DestroyBrowserVM(destroyCtx, bvm3ID); err != nil {
		t.Fatalf("destroy browser VM 3: %v", err)
	}
}

func TestRecovery_AgentRestartPreservesRunningVM_SystemdOwner(t *testing.T) {
	skipIfNoSystemdRuntimeOwner(t)

	cfg := skipIfNoPrereqs(t)
	cfg.RuntimeOwnerKind = "systemd-unit"
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch1 := setupTestOrchestrator(t, cfg, bridge)
	metaSrv1, stopMetaSrv1 := setupTestMetadataServerWithCancel(t, bridge.Gateway)
	orch1.SetMetadataRegistrar(metaSrv1)

	vmCfg := generateTestVMConfig(0)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: getStableOpenClawVersion(t),
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
		OpenClawManifestURI:     defaultOpenClawManifestURI,
	}
	vmCfg.DataVolumeGB = 5

	if err := orch1.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch1, vmCfg.MachineID, vmCreationTimeout)

	vm1, err := orch1.Get(t.Context(), vmCfg.MachineID)
	if err != nil {
		t.Fatalf("get VM before restart: %v", err)
	}
	originalPID := vm1.PID
	expectedUnit := "ocm-vm-" + vmCfg.MachineID + ".service"
	if vm1.RuntimeOwnerRef != expectedUnit {
		t.Fatalf("expected runtime owner ref %q, got %q", expectedUnit, vm1.RuntimeOwnerRef)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := orch1.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown orch1: %v", err)
	}
	shutdownCancel()
	stopMetaSrv1()

	orch2 := setupTestOrchestrator(t, cfg, bridge)
	metaSrv2 := setupTestMetadataServer(t, bridge.Gateway)
	orch2.SetMetadataRegistrar(metaSrv2)
	waitForMetadataServerReady(t, bridge.Gateway, 2*time.Second)

	if err := orch2.Recover(t.Context()); err != nil {
		t.Fatalf("recover orch2: %v", err)
	}

	vm2, err := orch2.Get(t.Context(), vmCfg.MachineID)
	if err != nil {
		t.Fatalf("get recovered VM: %v", err)
	}
	if vm2.PID != originalPID {
		t.Fatalf("expected recovered PID %d, got %d", originalPID, vm2.PID)
	}
	if vm2.RuntimeOwnerRef != expectedUnit {
		t.Fatalf("expected recovered runtime owner ref %q, got %q", expectedUnit, vm2.RuntimeOwnerRef)
	}
}

func TestRecovery_BrowserVMSurvivesDoubleAgentRestart_SystemdOwner(t *testing.T) {
	skipIfNoSystemdRuntimeOwner(t)

	cfg := skipIfNoPrereqs(t)
	cfg.RuntimeOwnerKind = "systemd-unit"
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}
	ensureBrowserRootfs(t, cfg)

	manifestURI := os.Getenv("TEST_BROWSER_ROOTFS_GCS_MANIFEST")
	if manifestURI == "" {
		manifestURI = "gs://openclawmachines/kernel-browser-rootfs/manifest.json"
	}

	newBrowserOrchestrator := func() orchestrator.Orchestrator {
		orchCfg := orchestrator.FirecrackerConfig{
			BridgeName:               cfg.BridgeName,
			SocketDir:                cfg.SocketDir,
			RootfsDir:                cfg.ImagesDir,
			StateDir:                 cfg.StateDir,
			DataDir:                  filepath.Join(cfg.StateDir, "data"),
			RuntimeStateDir:          filepath.Join(cfg.StateDir, "runtime"),
			OpenClawRuntimeDir:       filepath.Join(cfg.StateDir, "openclaw"),
			OpenClawManifestURI:      cfg.OpenClawManifestURI,
			GCSBrowserRootfsManifest: manifestURI,
			GCSDownloadTimeout:       2 * time.Minute,
			GCSRetryAttempts:         1,
			KernelPath:               cfg.KernelPath,
			DefaultVCPUs:             1,
			DefaultMemoryMB:          2048,
			RuntimeOwnerKind:         cfg.RuntimeOwnerKind,
			OpenClawDownloadTimeout:  30 * time.Second,
			OpenClawRetryAttempts:    1,
		}
		orch, err := orchestrator.New(orchCfg, bridge)
		if err != nil {
			t.Fatalf("create browser orchestrator: %v", err)
		}
		return orch
	}

	orch1 := newBrowserOrchestrator()
	bvmID := "browser-sd-" + cfg.TestID
	bvmCfg := orchestrator.BrowserVMConfig{
		BrowserVMID: bvmID,
		VMIP:        "192.168.200.60",
		VCPUs:       2,
		MemoryMB:    4096,
	}
	if err := orch1.CreateBrowserVM(t.Context(), bvmCfg); err != nil {
		t.Fatalf("create browser VM: %v", err)
	}

	statePath := filepath.Join(cfg.StateDir, "browser-vms.json")
	readPersisted := func() map[string]any {
		data, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatalf("read browser state file: %v", err)
		}
		var persisted []map[string]any
		if err := json.Unmarshal(data, &persisted); err != nil {
			t.Fatalf("unmarshal browser state file: %v", err)
		}
		if len(persisted) != 1 {
			t.Fatalf("expected 1 persisted browser VM, got %d", len(persisted))
		}
		return persisted[0]
	}

	original := readPersisted()
	originalPID := int(original["pid"].(float64))
	if original["runtime_owner_ref"] != "ocm-browser-vm-"+bvmID+".service" {
		t.Fatalf("unexpected runtime owner ref: %#v", original["runtime_owner_ref"])
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := orch1.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown orch1: %v", err)
	}
	shutdownCancel()

	orch2 := newBrowserOrchestrator()
	if err := orch2.Recover(t.Context()); err != nil {
		t.Fatalf("recover orch2: %v", err)
	}
	if persisted := readPersisted(); int(persisted["pid"].(float64)) != originalPID {
		t.Fatalf("expected PID %d after first recover, got %#v", originalPID, persisted["pid"])
	}

	shutdownCtx2, shutdownCancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	if err := orch2.Shutdown(shutdownCtx2); err != nil {
		t.Fatalf("shutdown orch2: %v", err)
	}
	shutdownCancel2()

	orch3 := newBrowserOrchestrator()
	if err := orch3.Recover(t.Context()); err != nil {
		t.Fatalf("recover orch3: %v", err)
	}
	if persisted := readPersisted(); int(persisted["pid"].(float64)) != originalPID {
		t.Fatalf("expected PID %d after second recover, got %#v", originalPID, persisted["pid"])
	}

	destroyCtx, destroyCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer destroyCancel()
	if err := orch3.DestroyBrowserVM(destroyCtx, bvmID); err != nil {
		t.Fatalf("destroy recovered browser VM: %v", err)
	}
}

func TestRuntimeOwner_SystemdDestroyStopsUnitLifecycle(t *testing.T) {
	skipIfNoSystemdRuntimeOwner(t)

	cfg := skipIfNoPrereqs(t)
	cfg.RuntimeOwnerKind = "systemd-unit"
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)

	vmCfg := generateTestVMConfig(0)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: getStableOpenClawVersion(t),
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
		OpenClawManifestURI:     defaultOpenClawManifestURI,
	}
	vmCfg.DataVolumeGB = 5

	if err := orch.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)

	unit := "ocm-vm-" + vmCfg.MachineID + ".service"
	checkUnitGone := func() bool {
		out, err := exec.Command("systemctl", "show", unit, "--property=LoadState", "--value").CombinedOutput()
		if err != nil {
			return false
		}
		return strings.TrimSpace(string(out)) == "not-found"
	}

	destroyCtx, destroyCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer destroyCancel()
	if err := orch.Destroy(destroyCtx, vmCfg.MachineID); err != nil {
		t.Fatalf("destroy VM: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if checkUnitGone() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("expected systemd unit %s to be gone after destroy", unit)
}

// waitForMetadataServerReady polls the metadata server's /health endpoint
// until it responds or the timeout expires.
func waitForMetadataServerReady(t *testing.T, bridgeIP string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	healthURL := fmt.Sprintf("http://%s:80/health", bridgeIP)
	for time.Now().Before(deadline) {
		resp, err := http.Get(healthURL)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("metadata server at %s did not become ready within %s", healthURL, timeout)
}

// killPID sends SIGKILL to a process.
func killPID(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
