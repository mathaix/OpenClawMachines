//go:build linux && integration

package integration

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/agentapi"
	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/orchestrator"
)

// ============================================================================
// Init Script Validation Tests
// ============================================================================

func setupInitTestVM(t *testing.T, vmCfg orchestrator.VMConfig) (proxyURL string, wsURL string, readyVM orchestrator.VMConfig) {
	return setupInitTestVMWithPrepare(t, vmCfg, nil)
}

func setupInitTestVMWithPrepare(t *testing.T, vmCfg orchestrator.VMConfig, prepare func(*TestConfig)) (proxyURL string, wsURL string, readyVM orchestrator.VMConfig) {
	proxyURL, wsURL, _, readyVM = setupInitTestVMStackWithPrepare(t, vmCfg, prepare)
	return proxyURL, wsURL, readyVM
}

func setupInitTestVMStackWithPrepare(t *testing.T, vmCfg orchestrator.VMConfig, prepare func(*TestConfig)) (proxyURL string, wsURL string, orch orchestrator.Orchestrator, readyVM orchestrator.VMConfig) {
	t.Helper()

	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)
	if prepare != nil {
		prepare(cfg)
	}

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch = setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)

	proxyServer := httptest.NewServer(agentapi.NewServer(cfg.AgentToken, orch, "", nil, "", nil, nil, nil, false, nil, "").ProxyRouter())
	t.Cleanup(func() { proxyServer.Close() })

	// Default to artifact runtime — rootfs has no baked openclaw.
	if vmCfg.RuntimeSelection == nil {
		version := getStableOpenClawVersion(t)
		vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
			ResolvedOpenClawVersion: version,
			VersionSource:           "pinned",
			RuntimeSource:           "artifact",
			OpenClawManifestURI:     defaultOpenClawManifestURI,
		}
		vmCfg.DataVolumeGB = 5 // artifact needs ~2GB+ when mirrored
	}

	if err := orch.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)

	proxyURL = proxyServer.URL
	wsURL = strings.Replace(proxyServer.URL, "http://", "ws://", 1)
	wsURL = fmt.Sprintf("%s/proxy/%s/terminal/ws", wsURL, vmCfg.MachineID)
	return proxyURL, wsURL, orch, vmCfg
}

func TestInit_MetadataFetch(t *testing.T) {
	// Create VM with known config
	vmCfg := generateTestVMConfig(0)
	_, wsURL, vmCfg := setupInitTestVM(t, vmCfg)

	// Wait for init script to complete (gateway startup etc.)
	time.Sleep(10 * time.Second)

	// First, verify terminal works with a simple echo
	echoOutput := testTerminalCommand(t, wsURL, vmCfg.ProxyToken,
		"echo TERMINAL_WORKS",
		15*time.Second)
	t.Logf("Terminal echo test output (%d chars): %q", len(echoOutput), echoOutput)

	// Verify that openclaw.json was written to disk pre-boot by the orchestrator.
	// Config is written to the data volume before VM start (config simplification).
	output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`echo confcheck:$(test -f /home/openclaw/.openclaw/openclaw.json && echo y || echo n)`,
		15*time.Second)
	t.Logf("openclaw.json check: %q", output)
	if strings.Contains(output, "confcheck:n") {
		t.Fatalf("openclaw.json should exist on disk (written pre-boot by orchestrator)")
	}
	if strings.Contains(output, "confcheck:y") {
		t.Log("Confirmed: openclaw.json exists on disk (written pre-boot)")
	} else {
		t.Logf("WARN: could not verify openclaw.json presence (output: %q)", output)
	}

	// Check OCM_MACHINE_ID environment variable
	// Use testTerminalCommandSplit to wait for shell prompt before sending,
	// ensuring profile.d files have been sourced.
	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"source /etc/profile.d/openclaw-identity.sh 2>/dev/null; echo $OCM_MACHINE_ID",
		10*time.Second)

	// The env var should be set (it's sourced from /etc/profile.d/)
	// Note: the terminal may include prompt characters, so just check the machine ID is present
	if !strings.Contains(output, vmCfg.MachineID) {
		t.Errorf("OCM_MACHINE_ID not set correctly. Expected to contain %q, output: %s",
			vmCfg.MachineID, output)
	}

	t.Log("Init metadata fetch validated successfully")
}

func TestInit_GatewayRestart(t *testing.T) {
	vmCfg := generateTestVMConfig(0)
	_, wsURL, vmCfg := setupInitTestVM(t, vmCfg)

	// Wait for gateway to be fully up
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)
	t.Log("1. Gateway is running")

	// Kill the gateway process via terminal
	// Native mode: gateway runs as "openclaw-gateway" process (child of su wrapper).
	// The su wrapper (root) runs "exec openclaw gateway" which becomes "openclaw-gateway".
	// Terminal runs as openclaw user, so kill the openclaw-owned process.
	// Kill the gateway, then verify it's gone via $() subshell.
	// $() hides markers from the command echo so output parsing works.
	testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"pkill -u openclaw -f openclaw-gateway",
		10*time.Second)
	time.Sleep(2 * time.Second)

	output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`echo gwcheck:$(pgrep -cu openclaw -f openclaw-gateway 2>/dev/null || echo 0)`,
		5*time.Second)
	t.Logf("Gateway process count after kill: %q", output)

	if !strings.Contains(output, "gwcheck:0") {
		t.Fatal("Failed to kill gateway process — still running after pkill")
	}
	t.Log("2. Gateway process killed")

	// Wait for supervisor to notice and restart (supervisor polls every 5s)
	t.Log("3. Waiting for supervisor to restart gateway...")
	time.Sleep(15 * time.Second)

	// Check if gateway came back
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 90*time.Second)
	t.Log("4. Gateway restarted successfully by supervisor")

	// Verify gateway is actually serving (through auth proxy since gateway is on loopback)
	testGatewayHealthDirect(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID)
	t.Log("5. Gateway health check passed after restart")
}

func TestInit_RuntimeSelectionArtifactMissingBinaryFailsPreBoot(t *testing.T) {
	vmCfg := generateTestVMConfig(0)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedRootfsVersion:   "rootfs-e2e-2026.04.04",
		ResolvedOpenClawVersion: "openclaw-e2e-2026.04.04",
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
		OpenClawBin:             "/ocm-runtime/bin/openclaw",
	}

	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)

	err := orch.Create(t.Context(), vmCfg)
	if err == nil {
		t.Fatal("expected strict artifact runtime boot to fail when artifact is not staged")
	}
	if !strings.Contains(err.Error(), "required openclaw artifact") {
		t.Fatalf("expected missing staged artifact error, got: %v", err)
	}
}

func TestInit_RuntimeSelectionArtifactUsesStagedRuntime(t *testing.T) {
	const version = "openclaw-e2e-2026.04.05"

	vmCfg := generateTestVMConfig(0)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedRootfsVersion:   "rootfs-e2e-2026.04.05",
		ResolvedOpenClawVersion: version,
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
	}

	_, wsURL, vmCfg := setupInitTestVMWithPrepare(t, vmCfg, func(cfg *TestConfig) {
		stageTestOpenClawRuntimeArtifact(t, cfg, version)
	})

	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`test -x /ocm-runtime/bin/openclaw && `+
			`test -d /ocm-runtime/dist/extensions && `+
			`test "$(cat /data/ocm/runtime/openclaw/resolved_version 2>/dev/null)" = "`+version+`" && `+
			`test "$(readlink /data/ocm/runtime/openclaw/current 2>/dev/null)" = "releases/`+version+`" && `+
			`grep -F 'selected_runtime=artifact' /var/log/openclaw-gateway.log >/dev/null && `+
			`grep -F 'resolved_openclaw_version=`+version+`' /var/log/openclaw-gateway.log >/dev/null && `+
			`echo RUNTIME_ARTIFACT_OK || echo RUNTIME_ARTIFACT_FAILED`,
		20*time.Second)

	if !strings.Contains(output, "RUNTIME_ARTIFACT_OK") {
		t.Fatalf("expected staged artifact runtime markers, output: %q", output)
	}
}

func TestInit_RuntimeSelectionArtifactServesGatewayChatCompletions(t *testing.T) {
	const version = "openclaw-e2e-2026.04.06"

	upstream := newTestOpenAIUpstream("artifact-gateway-ok")
	baseURL := startReachableBridgeHTTPServer(t, upstream)

	vmCfg := generateTestVMConfig(0)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedRootfsVersion:   "rootfs-e2e-2026.04.06",
		ResolvedOpenClawVersion: version,
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
	}
	setOpenAIProviderBaseURL(t, &vmCfg, baseURL)

	_, wsURL, vmCfg := setupInitTestVMWithPrepare(t, vmCfg, func(cfg *TestConfig) {
		stageTestOpenClawRuntimeArtifact(t, cfg, version)
	})

	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	reply := gatewayChatCompletionResponse(t, wsURL, vmCfg.ProxyToken)
	if !strings.Contains(reply, "artifact-gateway-ok") {
		t.Fatalf("gateway chat completion reply = %q, want artifact-gateway-ok", reply)
	}
	if upstream.RequestCount() == 0 {
		t.Fatal("expected gateway to proxy at least one OpenAI-compatible request upstream")
	}

	output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`export HOME=/home/openclaw OPENCLAW_STATE_DIR=/home/openclaw/.openclaw OPENCLAW_BUNDLED_PLUGINS_DIR=/ocm-runtime/node_modules/openclaw/dist/extensions; `+
			`timeout 20 openclaw plugins list >/tmp/artifact-plugins.txt 2>&1 && `+
			`grep -F 'selected_runtime=artifact' /var/log/openclaw-gateway.log >/dev/null && `+
			`grep -F 'memory-core' /tmp/artifact-plugins.txt >/dev/null && `+
			`grep -F 'composio' /tmp/artifact-plugins.txt >/dev/null && `+
			`grep -F 'loaded' /tmp/artifact-plugins.txt >/dev/null && `+
			`echo ARTIFACT_GATEWAY_CHAT_OK || echo ARTIFACT_GATEWAY_CHAT_FAILED`,
		20*time.Second)
	if !strings.Contains(output, "ARTIFACT_GATEWAY_CHAT_OK") {
		t.Fatalf("expected bundled plugin discovery plus artifact runtime selection, output: %q", output)
	}
}

func TestInit_RuntimeSelectionArtifactUpgradeSwitchesVersions(t *testing.T) {
	const (
		versionA = "openclaw-e2e-2026.04.07-a"
		versionB = "openclaw-e2e-2026.04.07-b"
	)

	upstream := newTestOpenAIUpstream("artifact-upgrade-ok")
	baseURL := startReachableBridgeHTTPServer(t, upstream)

	vmCfg := generateTestVMConfig(0)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedRootfsVersion:   "rootfs-e2e-2026.04.07",
		ResolvedOpenClawVersion: versionA,
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
	}
	setOpenAIProviderBaseURL(t, &vmCfg, baseURL)

	_, wsURL, orch, vmCfg := setupInitTestVMStackWithPrepare(t, vmCfg, func(cfg *TestConfig) {
		stageTestOpenClawRuntimeArtifact(t, cfg, versionA)
		stageTestOpenClawRuntimeArtifact(t, cfg, versionB)
	})

	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)
	reply := gatewayChatCompletionResponse(t, wsURL, vmCfg.ProxyToken)
	if !strings.Contains(reply, "artifact-upgrade-ok") {
		t.Fatalf("initial artifact runtime reply = %q, want artifact-upgrade-ok", reply)
	}

	if err := orch.Stop(t.Context(), vmCfg.MachineID); err != nil {
		t.Fatalf("stop VM before upgrade: %v", err)
	}

	vmCfg.RuntimeSelection.ResolvedOpenClawVersion = versionB
	if err := orch.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("restart VM with upgraded artifact runtime: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	reply = gatewayChatCompletionResponse(t, wsURL, vmCfg.ProxyToken)
	if !strings.Contains(reply, "artifact-upgrade-ok") {
		t.Fatalf("upgraded artifact runtime reply = %q, want artifact-upgrade-ok", reply)
	}

	output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`test "$(cat /data/ocm/runtime/openclaw/resolved_version 2>/dev/null)" = "`+versionB+`" && `+
			`test "$(readlink /data/ocm/runtime/openclaw/current 2>/dev/null)" = "releases/`+versionB+`" && `+
			`test "$(readlink /data/ocm/runtime/openclaw/previous 2>/dev/null)" = "releases/`+versionA+`" && `+
			`echo ARTIFACT_UPGRADE_OK || echo ARTIFACT_UPGRADE_FAILED`,
		20*time.Second)
	if !strings.Contains(output, "ARTIFACT_UPGRADE_OK") {
		t.Fatalf("expected artifact runtime upgrade markers, output: %q", output)
	}
}

func TestInit_RuntimeSelectionArtifactRollbackRestoresPreviousVersionAfterFailedUpgrade(t *testing.T) {
	const (
		versionA = "openclaw-e2e-2026.04.08-a"
		versionB = "openclaw-e2e-2026.04.08-broken"
	)

	upstream := newTestOpenAIUpstream("artifact-rollback-ok")
	baseURL := startReachableBridgeHTTPServer(t, upstream)

	vmCfg := generateTestVMConfig(0)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedRootfsVersion:   "rootfs-e2e-2026.04.08",
		ResolvedOpenClawVersion: versionA,
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
	}
	setOpenAIProviderBaseURL(t, &vmCfg, baseURL)

	_, wsURL, orch, vmCfg := setupInitTestVMStackWithPrepare(t, vmCfg, func(cfg *TestConfig) {
		stageTestOpenClawRuntimeArtifact(t, cfg, versionA)
		stageBrokenTestOpenClawRuntimeArtifact(t, cfg, versionB)
	})

	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)
	reply := gatewayChatCompletionResponse(t, wsURL, vmCfg.ProxyToken)
	if !strings.Contains(reply, "artifact-rollback-ok") {
		t.Fatalf("initial artifact runtime reply = %q, want artifact-rollback-ok", reply)
	}

	if err := orch.Stop(t.Context(), vmCfg.MachineID); err != nil {
		t.Fatalf("stop VM before broken upgrade: %v", err)
	}

	vmCfg.RuntimeSelection.ResolvedOpenClawVersion = versionB
	if err := orch.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("restart VM with broken artifact runtime: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForAuthProxy(t, vmCfg.VMIP, 30*time.Second)

	output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`i=0; while [ "$i" -lt 30 ]; do `+
			`status=$(cat /run/ocm-gateway-status 2>/dev/null || echo missing); `+
			`[ "$status" = "crash-loop" ] && break; `+
			`i=$((i+1)); sleep 1; `+
			`done; `+
			`test "$(cat /run/ocm-gateway-status 2>/dev/null)" = "crash-loop" && `+
			`test "$(cat /data/ocm/runtime/openclaw/resolved_version 2>/dev/null)" = "`+versionB+`" && `+
			`echo BROKEN_UPGRADE_CONFIRMED || echo BROKEN_UPGRADE_NOT_CONFIRMED`,
		40*time.Second)
	if !strings.Contains(output, "BROKEN_UPGRADE_CONFIRMED") {
		t.Fatalf("expected broken artifact upgrade to enter crash loop, output: %q", output)
	}

	if err := orch.Stop(t.Context(), vmCfg.MachineID); err != nil {
		t.Fatalf("stop VM before rollback: %v", err)
	}

	vmCfg.RuntimeSelection.ResolvedOpenClawVersion = versionA
	if err := orch.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("restart VM with rollback artifact runtime: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	reply = gatewayChatCompletionResponse(t, wsURL, vmCfg.ProxyToken)
	if !strings.Contains(reply, "artifact-rollback-ok") {
		t.Fatalf("rolled back artifact runtime reply = %q, want artifact-rollback-ok", reply)
	}

	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`test "$(cat /data/ocm/runtime/openclaw/resolved_version 2>/dev/null)" = "`+versionA+`" && `+
			`test "$(readlink /data/ocm/runtime/openclaw/current 2>/dev/null)" = "releases/`+versionA+`" && `+
			`test "$(readlink /data/ocm/runtime/openclaw/previous 2>/dev/null)" = "releases/`+versionB+`" && `+
			`echo ARTIFACT_ROLLBACK_OK || echo ARTIFACT_ROLLBACK_FAILED`,
		20*time.Second)
	if !strings.Contains(output, "ARTIFACT_ROLLBACK_OK") {
		t.Fatalf("expected artifact rollback markers, output: %q", output)
	}
}

func TestInit_OrchestratorUpgradePreservesDataAndGuestConfig(t *testing.T) {
	const (
		versionA = "openclaw-e2e-2026.04.09-a"
		versionB = "openclaw-e2e-2026.04.09-b"
	)

	upstreamA := newTestOpenAIUpstream("upgrade-preserve-old-config")
	upstreamB := newTestOpenAIUpstream("upgrade-should-not-use-new-config")
	baseURLA := startReachableBridgeHTTPServer(t, upstreamA)
	baseURLB := startReachableBridgeHTTPServer(t, upstreamB)

	vmCfg := generateTestVMConfig(0)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedRootfsVersion:   "rootfs-e2e-2026.04.09",
		ResolvedOpenClawVersion: versionA,
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
	}
	setOpenAIProviderBaseURL(t, &vmCfg, baseURLA)

	_, wsURL, orch, vmCfg := setupInitTestVMStackWithPrepare(t, vmCfg, func(cfg *TestConfig) {
		stageTestOpenClawRuntimeArtifact(t, cfg, versionA)
		stageTestOpenClawRuntimeArtifact(t, cfg, versionB)
	})

	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	reply := gatewayChatCompletionResponse(t, wsURL, vmCfg.ProxyToken)
	if !strings.Contains(reply, "upgrade-preserve-old-config") {
		t.Fatalf("initial artifact runtime reply = %q, want upgrade-preserve-old-config", reply)
	}

	testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"echo upgrade-persisted-data > /workspace/upgrade-preserve.txt && sync",
		20*time.Second)

	upgradeCfg := vmCfg
	upgradeCfg.OpenClawConf = append([]byte(nil), vmCfg.OpenClawConf...)
	upgradeCfg.DataVersion = vmCfg.DataVersion + 1
	if vmCfg.RuntimeSelection != nil {
		runtimeSelection := *vmCfg.RuntimeSelection
		upgradeCfg.RuntimeSelection = &runtimeSelection
	}
	upgradeCfg.RuntimeSelection.ResolvedOpenClawVersion = versionB
	setOpenAIProviderBaseURL(t, &upgradeCfg, baseURLB)

	if err := orch.Upgrade(t.Context(), upgradeCfg); err != nil {
		t.Fatalf("orch upgrade: %v", err)
	}
	waitForVMReady(t, orch, upgradeCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, upgradeCfg.VMIP, upgradeCfg.SigningKey, upgradeCfg.MachineID, 120*time.Second)

	reply = gatewayChatCompletionResponse(t, wsURL, upgradeCfg.ProxyToken)
	if !strings.Contains(reply, "upgrade-preserve-old-config") {
		t.Fatalf("upgraded artifact runtime reply = %q, want preserved original config reply", reply)
	}
	if upstreamB.RequestCount() != 0 {
		t.Fatalf("expected upgraded VM to keep existing config and avoid new upstream, got %d requests", upstreamB.RequestCount())
	}

	output := testTerminalCommandSplit(t, wsURL, upgradeCfg.ProxyToken,
		`test "$(cat /workspace/upgrade-preserve.txt 2>/dev/null)" = "upgrade-persisted-data" && `+
			`test "$(jq -r '.models.providers.openai.baseUrl // ""' /home/openclaw/.openclaw/openclaw.json 2>/dev/null)" = "`+strings.TrimRight(baseURLA, "/")+`/openai/v1" && `+
			`test "$(cat /data/ocm/runtime/openclaw/resolved_version 2>/dev/null)" = "`+versionB+`" && `+
			`test "$(readlink /data/ocm/runtime/openclaw/current 2>/dev/null)" = "releases/`+versionB+`" && `+
			`test "$(readlink /data/ocm/runtime/openclaw/previous 2>/dev/null)" = "releases/`+versionA+`" && `+
			`echo ORCH_UPGRADE_PRESERVE_OK || echo ORCH_UPGRADE_PRESERVE_FAILED`,
		25*time.Second)
	if !strings.Contains(output, "ORCH_UPGRADE_PRESERVE_OK") {
		t.Fatalf("expected orch upgrade to preserve data and guest config, output: %q", output)
	}
}

func TestInit_OrchestratorUpgradeCreateFailureRestoresPreviousVM(t *testing.T) {
	const (
		versionA       = "openclaw-e2e-2026.04.10-a"
		missingVersion = "openclaw-e2e-2026.04.10-missing"
	)

	upstream := newTestOpenAIUpstream("upgrade-restore-ok")
	baseURL := startReachableBridgeHTTPServer(t, upstream)

	vmCfg := generateTestVMConfig(0)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedRootfsVersion:   "rootfs-e2e-2026.04.10",
		ResolvedOpenClawVersion: versionA,
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
	}
	setOpenAIProviderBaseURL(t, &vmCfg, baseURL)

	_, wsURL, orch, vmCfg := setupInitTestVMStackWithPrepare(t, vmCfg, func(cfg *TestConfig) {
		stageTestOpenClawRuntimeArtifact(t, cfg, versionA)
	})

	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	reply := gatewayChatCompletionResponse(t, wsURL, vmCfg.ProxyToken)
	if !strings.Contains(reply, "upgrade-restore-ok") {
		t.Fatalf("initial artifact runtime reply = %q, want upgrade-restore-ok", reply)
	}

	testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"echo restored-data > /workspace/upgrade-restore.txt && sync",
		20*time.Second)

	upgradeCfg := vmCfg
	upgradeCfg.OpenClawConf = append([]byte(nil), vmCfg.OpenClawConf...)
	if vmCfg.RuntimeSelection != nil {
		runtimeSelection := *vmCfg.RuntimeSelection
		upgradeCfg.RuntimeSelection = &runtimeSelection
	}
	upgradeCfg.RuntimeSelection.ResolvedOpenClawVersion = missingVersion

	err := orch.Upgrade(t.Context(), upgradeCfg)
	if !errors.Is(err, orchestrator.ErrUpgradeRestoredPreviousVM) {
		t.Fatalf("expected ErrUpgradeRestoredPreviousVM, got %v", err)
	}

	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	reply = gatewayChatCompletionResponse(t, wsURL, vmCfg.ProxyToken)
	if !strings.Contains(reply, "upgrade-restore-ok") {
		t.Fatalf("restored artifact runtime reply = %q, want upgrade-restore-ok", reply)
	}

	output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`test "$(cat /workspace/upgrade-restore.txt 2>/dev/null)" = "restored-data" && `+
			`test "$(cat /data/ocm/runtime/openclaw/resolved_version 2>/dev/null)" = "`+versionA+`" && `+
			`test "$(readlink /data/ocm/runtime/openclaw/current 2>/dev/null)" = "releases/`+versionA+`" && `+
			`echo ORCH_UPGRADE_RESTORE_OK || echo ORCH_UPGRADE_RESTORE_FAILED`,
		25*time.Second)
	if !strings.Contains(output, "ORCH_UPGRADE_RESTORE_OK") {
		t.Fatalf("expected restored VM to keep previous runtime and data, output: %q", output)
	}
}
