//go:build linux

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"

	"github.com/mathaix/openclawmachines/backend/internal/network"
)

func TestValidateBrowserVMCreateRequestRejectsDuplicateLiveAndQuarantinedState(t *testing.T) {
	o := newTestStateOrchestrator(t)
	o.browserVMs["live"] = &browserRunningVM{ID: "live", VMIP: "192.168.100.20"}
	o.quarantinedBrowserVMs["quarantined"] = persistedBrowserVM{ID: "quarantined", VMIP: "192.168.100.30"}

	if err := o.validateBrowserVMCreateRequest(BrowserVMConfig{BrowserVMID: "live", VMIP: "192.168.100.21"}); err == nil {
		t.Fatal("expected duplicate live browser VM ID to be rejected")
	}
	if err := o.validateBrowserVMCreateRequest(BrowserVMConfig{BrowserVMID: "new", VMIP: "192.168.100.20"}); err == nil {
		t.Fatal("expected duplicate live browser VM IP to be rejected")
	}
	if err := o.validateBrowserVMCreateRequest(BrowserVMConfig{BrowserVMID: "quarantined", VMIP: "192.168.100.31"}); err == nil {
		t.Fatal("expected quarantined browser VM ID to be rejected")
	}
	if err := o.validateBrowserVMCreateRequest(BrowserVMConfig{BrowserVMID: "new", VMIP: "192.168.100.30"}); err == nil {
		t.Fatal("expected quarantined browser VM IP to be rejected")
	}
	if err := o.validateBrowserVMCreateRequest(BrowserVMConfig{BrowserVMID: "new", VMIP: "192.168.100.40"}); err != nil {
		t.Fatalf("expected unique browser VM request to pass, got %v", err)
	}
}

func TestCopyRootfsFallsBackWhenReflinkUnavailable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.ext4")
	dst := filepath.Join(dir, "dst.ext4")
	if err := os.WriteFile(src, []byte("rootfs-data"), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	mode, err := copyRootfs(src, dst, false)
	if err != nil {
		t.Fatalf("copyRootfs returned error: %v", err)
	}
	if mode != rootfsCopyModeReflink && mode != rootfsCopyModeFull {
		t.Fatalf("unexpected copy mode: %q", mode)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != "rootfs-data" {
		t.Fatalf("destination content mismatch: %q", string(got))
	}
}

func TestBrowserRootfsRequiresReflinkForKernelImagesUnlessAllowed(t *testing.T) {
	if !browserRootfsRequiresReflink("kernel-images-experimental", "", false) {
		t.Fatal("expected latest Kernel Images browser rootfs to require reflink by default")
	}
	if browserRootfsRequiresReflink("kernel-images-experimental", "operator-pinned-version", false) {
		t.Fatal("expected pinned Kernel Images browser rootfs to allow fallback copy")
	}
	if browserRootfsRequiresReflink("kernel-images-experimental", "new-version", true) {
		t.Fatal("expected explicit full-copy allowance to disable strict reflink requirement")
	}
	if browserRootfsRequiresReflink("legacy", "", false) {
		t.Fatal("expected legacy browser rootfs to allow fallback copy")
	}
}

func TestWaitForPortContextCanceledReturnsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := waitForPortContext(ctx, "192.0.2.1", 9222, time.Minute)
	if err == nil {
		t.Fatal("expected canceled wait to fail")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("canceled wait took too long: %s", elapsed)
	}
}

func TestBrowserCDPWatcherTransitionsCreatingToReady(t *testing.T) {
	o := newTestStateOrchestrator(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("parse port: %v", err)
	}

	bvm := &browserRunningVM{ID: "b1", VMIP: "127.0.0.1", Status: "creating", CDPPort: port}
	o.browserVMs[bvm.ID] = bvm
	o.startBrowserCDPWatcher(bvm, "test-rootfs", "legacy", time.Second, time.Now())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		o.browserMu.Lock()
		status := bvm.Status
		o.browserMu.Unlock()
		if status == "ready" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected watcher to mark browser VM ready, got status %q", bvm.Status)
}

type stubRuntimeOwner struct {
	stopFn func(context.Context, string, *firecracker.Machine, context.CancelFunc, time.Duration) error
}

func (s stubRuntimeOwner) kind() string {
	return runtimeOwnerKindDirect
}

func (s stubRuntimeOwner) ownerRef(_ string, _ bool) string {
	return ""
}

func (s stubRuntimeOwner) start(context.Context, string, bool, string, firecracker.Config, io.Writer, io.Writer) (*firecracker.Machine, int, context.CancelFunc, error) {
	panic("unexpected start call in test")
}

func (s stubRuntimeOwner) stop(ctx context.Context, ownerRef string, machine *firecracker.Machine, cancel context.CancelFunc, graceTimeout time.Duration) error {
	if s.stopFn != nil {
		return s.stopFn(ctx, ownerRef, machine, cancel, graceTimeout)
	}
	return nil
}

func TestRecoverBrowserVMsPersistsRecoveredState(t *testing.T) {
	o := newTestStateOrchestrator(t)
	stubRecoveryVerification(t)

	socketPath := filepath.Join(o.cfg.StateDir, "b1.sock")
	if err := os.WriteFile(socketPath, []byte("socket"), 0o600); err != nil {
		t.Fatalf("write socket placeholder: %v", err)
	}

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	persisted := []persistedBrowserVM{{
		ID:              "b1",
		VMIP:            "192.168.100.50",
		TapDevice:       "tapb1",
		SocketPath:      socketPath,
		RootfsPath:      filepath.Join(o.cfg.StateDir, "b1.ext4"),
		CDPPort:         9222,
		Status:          "ready",
		PID:             cmd.Process.Pid,
		RuntimeOwnerRef: "browser-owner-ref",
		CreatedAt:       time.Unix(123, 0).UTC(),
	}}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal persisted browser state: %v", err)
	}
	if err := os.WriteFile(o.browserStateFilePath(), data, 0o600); err != nil {
		t.Fatalf("write browser state file: %v", err)
	}

	if _, err := o.recoverBrowserVMs(context.Background()); err != nil {
		t.Fatalf("recover browser VMs: %v", err)
	}

	data, err = os.ReadFile(o.browserStateFilePath())
	if err != nil {
		t.Fatalf("read browser state after recovery: %v", err)
	}
	var recovered []persistedBrowserVM
	if err := json.Unmarshal(data, &recovered); err != nil {
		t.Fatalf("unmarshal recovered browser state: %v", err)
	}
	if len(recovered) != 1 {
		t.Fatalf("expected 1 recovered browser VM, got %d", len(recovered))
	}
	if recovered[0].PID != cmd.Process.Pid {
		t.Fatalf("expected recovered PID %d, got %d", cmd.Process.Pid, recovered[0].PID)
	}
	if recovered[0].Quarantined {
		t.Fatalf("expected recovered browser VM to remain unquarantined")
	}
}

func TestRecoverBrowserVMsPlansDNATReplay(t *testing.T) {
	o := newTestStateOrchestrator(t)
	o.cfg.HostExternalIP = "203.0.113.10"
	stubRecoveryVerification(t)

	socketPath := filepath.Join(o.cfg.StateDir, "b1.sock")
	if err := os.WriteFile(socketPath, []byte("socket"), 0o600); err != nil {
		t.Fatalf("write socket placeholder: %v", err)
	}

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	webrtcBase := browserWebRTCBasePort
	webrtcEnd := browserWebRTCBasePort + browserWebRTCSlotSize - 1
	persisted := []persistedBrowserVM{{
		ID:              "b1",
		VMIP:            "192.168.100.50",
		TapDevice:       "tapb1",
		SocketPath:      socketPath,
		RootfsPath:      filepath.Join(o.cfg.StateDir, "b1.ext4"),
		CDPPort:         9222,
		Status:          "ready",
		PID:             cmd.Process.Pid,
		RuntimeOwnerRef: "browser-owner-ref",
		WebRTCPortBase:  webrtcBase,
		WebRTCPortEnd:   webrtcEnd,
		CreatedAt:       time.Unix(123, 0).UTC(),
	}}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal persisted browser state: %v", err)
	}
	if err := os.WriteFile(o.browserStateFilePath(), data, 0o600); err != nil {
		t.Fatalf("write browser state file: %v", err)
	}

	dnatReplays, err := o.recoverBrowserVMs(context.Background())
	if err != nil {
		t.Fatalf("recover browser VMs: %v", err)
	}

	if len(dnatReplays) != 1 {
		t.Fatalf("expected 1 planned DNAT replay, got %d", len(dnatReplays))
	}
	got := dnatReplays[0]
	if got.vmIP != "192.168.100.50" {
		t.Fatalf("expected replay VM IP 192.168.100.50, got %q", got.vmIP)
	}
	if got.browserVMID != "b1" {
		t.Fatalf("expected replay browser VM ID %q, got %q", "b1", got.browserVMID)
	}
	if got.portStart != webrtcBase || got.portEnd != webrtcEnd {
		t.Fatalf("expected replay port range %d-%d, got %d-%d", webrtcBase, webrtcEnd, got.portStart, got.portEnd)
	}
}

func TestRecoverBrowserVMsSkipsDNATReplayWithoutHostExternalIP(t *testing.T) {
	o := newTestStateOrchestrator(t)
	// HostExternalIP intentionally left empty.
	stubRecoveryVerification(t)

	socketPath := filepath.Join(o.cfg.StateDir, "b1.sock")
	if err := os.WriteFile(socketPath, []byte("socket"), 0o600); err != nil {
		t.Fatalf("write socket placeholder: %v", err)
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	persisted := []persistedBrowserVM{{
		ID:             "b1",
		VMIP:           "192.168.100.50",
		TapDevice:      "tapb1",
		SocketPath:     socketPath,
		RootfsPath:     filepath.Join(o.cfg.StateDir, "b1.ext4"),
		CDPPort:        9222,
		Status:         "ready",
		PID:            cmd.Process.Pid,
		WebRTCPortBase: browserWebRTCBasePort,
		WebRTCPortEnd:  browserWebRTCBasePort + browserWebRTCSlotSize - 1,
	}}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal persisted browser state: %v", err)
	}
	if err := os.WriteFile(o.browserStateFilePath(), data, 0o600); err != nil {
		t.Fatalf("write browser state file: %v", err)
	}

	dnatReplays, err := o.recoverBrowserVMs(context.Background())
	if err != nil {
		t.Fatalf("recover browser VMs: %v", err)
	}
	if len(dnatReplays) != 0 {
		t.Fatalf("expected DNAT replay to be skipped when HostExternalIP is empty, got %d", len(dnatReplays))
	}
}

func TestReplayRecoveredBrowserDNATAsyncReplaysRules(t *testing.T) {
	o := newTestStateOrchestrator(t)
	calls := make(chan browserDNATReplay, 1)

	origReplayer := browserDNATReplayer
	browserDNATReplayer = func(_ *network.Bridge, browserVMID, vmIP string, portStart, portEnd int) error {
		calls <- browserDNATReplay{
			browserVMID: browserVMID,
			vmIP:        vmIP,
			portStart:   portStart,
			portEnd:     portEnd,
		}
		return nil
	}
	t.Cleanup(func() { browserDNATReplayer = origReplayer })

	o.replayRecoveredBrowserDNATAsync(context.Background(), []browserDNATReplay{{
		browserVMID: "b1",
		vmIP:        "192.168.100.50",
		portStart:   56000,
		portEnd:     56099,
	}})

	select {
	case got := <-calls:
		if got.browserVMID != "b1" || got.vmIP != "192.168.100.50" || got.portStart != 56000 || got.portEnd != 56099 {
			t.Fatalf("unexpected replay call: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async DNAT replay")
	}
}

func TestAllocateWebRTCPortSlotSkipsQuarantinedBrowserVMs(t *testing.T) {
	o := newTestOrchestrator(t)
	o.browserVMs["live"] = &browserRunningVM{
		ID:             "live",
		WebRTCPortBase: browserWebRTCBasePort + browserWebRTCSlotSize,
		WebRTCPortEnd:  browserWebRTCBasePort + 2*browserWebRTCSlotSize - 1,
	}
	o.quarantinedBrowserVMs["quarantined"] = persistedBrowserVM{
		ID:             "quarantined",
		WebRTCPortBase: browserWebRTCBasePort,
		WebRTCPortEnd:  browserWebRTCBasePort + browserWebRTCSlotSize - 1,
	}

	base, end, err := o.allocateWebRTCPortSlot()
	if err != nil {
		t.Fatalf("allocate WebRTC slot: %v", err)
	}
	wantBase := browserWebRTCBasePort + 2*browserWebRTCSlotSize
	wantEnd := wantBase + browserWebRTCSlotSize - 1
	if base != wantBase || end != wantEnd {
		t.Fatalf("expected slot %d-%d, got %d-%d", wantBase, wantEnd, base, end)
	}
}

func TestStopReturnsErrorWhilePIDStillAlive(t *testing.T) {
	o := newTestOrchestrator(t)
	o.runtimeOwner = stubRuntimeOwner{}

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	o.vms["m1"] = &runningVM{
		instance: VMInstance{
			MachineID: "m1",
			PID:       cmd.Process.Pid,
		},
		cancel: func() {},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := o.Stop(ctx, "m1"); err == nil {
		t.Fatalf("expected stop to fail while PID is still alive")
	}
	if _, exists := o.vms["m1"]; !exists {
		t.Fatalf("expected VM to remain tracked after failed stop")
	}
}

func TestDestroyReturnsErrorWhilePIDStillAlive(t *testing.T) {
	o := newTestOrchestrator(t)
	o.runtimeOwner = stubRuntimeOwner{}

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	o.vms["m1"] = &runningVM{
		instance: VMInstance{
			MachineID: "m1",
			PID:       cmd.Process.Pid,
		},
		cancel: func() {},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := o.Destroy(ctx, "m1"); err == nil {
		t.Fatalf("expected destroy to fail while PID is still alive")
	}
	if _, exists := o.vms["m1"]; !exists {
		t.Fatalf("expected VM to remain tracked after failed destroy")
	}
}

func TestDestroyBrowserVMReturnsErrorWhilePIDStillAlive(t *testing.T) {
	o := newTestOrchestrator(t)
	o.runtimeOwner = stubRuntimeOwner{}

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	o.browserVMs["b1"] = &browserRunningVM{
		ID:     "b1",
		PID:    cmd.Process.Pid,
		cancel: func() {},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := o.DestroyBrowserVM(ctx, "b1"); err == nil {
		t.Fatalf("expected browser destroy to fail while PID is still alive")
	}
	if _, exists := o.browserVMs["b1"]; !exists {
		t.Fatalf("expected browser VM to remain tracked after failed destroy")
	}
}

func TestStopOwnedVMForceKillsDirectOwnerPID(t *testing.T) {
	o := newTestOrchestrator(t)

	originalPIDAlive := pidAlive
	originalKillProcessByPID := killProcessByPID
	alive := true
	killed := false
	const pid = 4242
	pidAlive = func(got int) bool {
		if got != pid {
			t.Errorf("pidAlive called with pid %d, want %d", got, pid)
			return false
		}
		return alive
	}
	killProcessByPID = func(got int) error {
		if got != pid {
			t.Errorf("killProcessByPID called with pid %d, want %d", got, pid)
			return nil
		}
		killed = true
		alive = false
		return nil
	}
	t.Cleanup(func() {
		pidAlive = originalPIDAlive
		killProcessByPID = originalKillProcessByPID
	})

	if err := o.stopOwnedVM(context.Background(), "", nil, nil, pid, time.Millisecond); err != nil {
		t.Fatalf("stopOwnedVM returned error after direct force kill: %v", err)
	}
	if !killed {
		t.Fatalf("expected direct owner fallback to kill pid")
	}
}

// TestDestroyBrowserVMIdempotentWhenMissing documents the contract that
// destroying a browser VM the orchestrator has no record of is a success,
// not a failure. Without this, orphan DB rows at the control plane (created
// by a prior agent that no longer owns the state) become permanently stuck
// in "error" because the delete flow keeps hitting agent-side 500s.
func TestDestroyBrowserVMIdempotentWhenMissing(t *testing.T) {
	o := newTestOrchestrator(t)
	o.runtimeOwner = stubRuntimeOwner{}

	if _, exists := o.browserVMs["ghost"]; exists {
		t.Fatalf("test precondition: no browser VM registered for id 'ghost'")
	}

	if err := o.DestroyBrowserVM(context.Background(), "ghost"); err != nil {
		t.Fatalf("expected idempotent success for unknown browser VM, got error: %v", err)
	}
}

func TestSetErrorCreatesMissingVMEntry(t *testing.T) {
	o := &firecrackerOrchestrator{
		cfg: FirecrackerConfig{
			StateDir: t.TempDir(),
		},
		vms: make(map[string]*runningVM),
	}

	o.SetError("machine-create-failed", "rootfs missing")

	vm, err := o.Get(context.Background(), "machine-create-failed")
	if err != nil {
		t.Fatalf("get error VM: %v", err)
	}
	if vm.Status != "error" {
		t.Fatalf("status = %q, want error", vm.Status)
	}
	if vm.ErrorMessage != "rootfs missing" {
		t.Fatalf("error_message = %q, want rootfs missing", vm.ErrorMessage)
	}
}

func TestDestroyRemovesErrorStateWithoutDeletingDataVolume(t *testing.T) {
	stateDir := t.TempDir()
	dataDir := t.TempDir()
	machineID := "machine-create-failed"
	dataPath := filepath.Join(dataDir, machineID+".ext4")
	if err := os.WriteFile(dataPath, []byte("persistent data"), 0o600); err != nil {
		t.Fatalf("write data volume: %v", err)
	}
	o := &firecrackerOrchestrator{
		cfg: FirecrackerConfig{
			StateDir: stateDir,
			DataDir:  dataDir,
		},
		vms: map[string]*runningVM{
			machineID: {
				instance: VMInstance{
					MachineID:    machineID,
					Status:       "error",
					ErrorMessage: "create failed",
				},
			},
		},
	}

	if err := o.Destroy(context.Background(), machineID); err != nil {
		t.Fatalf("destroy error state: %v", err)
	}
	if _, exists := o.vms[machineID]; exists {
		t.Fatalf("expected error VM entry to be removed")
	}
	if _, err := os.Stat(dataPath); err != nil {
		t.Fatalf("expected data volume to be preserved, stat error: %v", err)
	}
}

// TestDestroyBrowserVMCleansUpQuarantined covers the Codex-flagged case
// where a recovered browser VM sits in quarantinedBrowserVMs (couldn't
// reattach cleanly). Without this path, the idempotent fallback would
// return "already_gone" and the control plane would drop the DB row,
// leaving the quarantined unit + tap + rootfs + socket on the host with
// no handle back to clean them up.
func TestDestroyBrowserVMCleansUpQuarantined(t *testing.T) {
	o := newTestOrchestrator(t)
	o.runtimeOwner = stubRuntimeOwner{}

	// Seed quarantine with a realistic persisted record. PID is 0 so the
	// test doesn't need to fork a real process — the cleanup path only
	// kills when pid > 0 and pidAlive.
	o.quarantinedBrowserVMs["q1"] = persistedBrowserVM{
		ID:               "q1",
		VMIP:             "192.168.100.80",
		TapDevice:        "btap-q1-test",
		SocketPath:       "/tmp/test-q1.sock",
		RootfsPath:       "/tmp/test-q1.ext4",
		CDPPort:          9222,
		WebRTCPortBase:   52100,
		WebRTCPortEnd:    52199,
		RuntimeOwnerKind: "systemd-unit",
		RuntimeOwnerRef:  "ocm-browser-vm-q1.service",
		Quarantined:      true,
		QuarantineReason: "reattach_failed",
	}

	if err := o.DestroyBrowserVM(context.Background(), "q1"); err != nil {
		t.Fatalf("expected quarantined cleanup to succeed, got error: %v", err)
	}

	if _, stillThere := o.quarantinedBrowserVMs["q1"]; stillThere {
		t.Fatalf("expected quarantined entry to be removed after destroy, still present")
	}
}
