//go:build linux

package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/network"
)

type testRegistrar struct {
	registered map[string]metadata.MachineConfig
}

func newTestRegistrar() *testRegistrar {
	return &testRegistrar{registered: make(map[string]metadata.MachineConfig)}
}

func (r *testRegistrar) RegisterMachine(vmIP string, config metadata.MachineConfig) {
	r.registered[vmIP] = config
}

func (r *testRegistrar) UnregisterMachine(vmIP string) {
	delete(r.registered, vmIP)
}

func (r *testRegistrar) UpdateMachineSecrets(vmIP string, secrets map[string]string) {
	cfg := r.registered[vmIP]
	cfg.Secrets = secrets
	r.registered[vmIP] = cfg
}

func (r *testRegistrar) UpdateMachineConfig(vmIP string, openClawConf []byte) {
	cfg := r.registered[vmIP]
	cfg.OpenClawConf = openClawConf
	r.registered[vmIP] = cfg
}

func (r *testRegistrar) UpdateMachineLLMKeys(vmIP string, llmKeys map[string]metadata.CredentialEntry) {
	cfg := r.registered[vmIP]
	cfg.LLMKeys = llmKeys
	r.registered[vmIP] = cfg
}

func (r *testRegistrar) ReplaceMachineLLMKeys(vmIP string, llmKeys map[string]metadata.CredentialEntry) {
	cfg := r.registered[vmIP]
	cfg.LLMKeys = llmKeys
	r.registered[vmIP] = cfg
}

func newTestStateOrchestrator(t *testing.T) *firecrackerOrchestrator {
	t.Helper()
	return &firecrackerOrchestrator{
		cfg: FirecrackerConfig{
			StateDir: t.TempDir(),
		},
		bridge:                network.NewBridge("test-br0", "192.168.100.0/24", "192.168.100.1"),
		vms:                   make(map[string]*runningVM),
		quarantinedVMs:        make(map[string]persistedVM),
		browserVMs:            make(map[string]*browserRunningVM),
		quarantinedBrowserVMs: make(map[string]persistedBrowserVM),
	}
}

func stubRecoveryVerification(t *testing.T) {
	t.Helper()
	origPIDVerifier := firecrackerPIDVerifier
	origMachineIDVerifier := firecrackerMachineIDVerifier
	firecrackerPIDVerifier = func(pid int) bool {
		return pidAlive(pid)
	}
	firecrackerMachineIDVerifier = func(_ context.Context, machine *firecracker.Machine) (string, error) {
		return machine.Cfg.VMID, nil
	}
	t.Cleanup(func() {
		firecrackerPIDVerifier = origPIDVerifier
		firecrackerMachineIDVerifier = origMachineIDVerifier
	})
}

func stubSystemdState(t *testing.T, reader func(string) (systemdUnitState, error), stopCtx func(context.Context, string) error, lister func(...string) ([]systemdUnitState, error)) {
	t.Helper()
	origReader := systemdUnitStateReader
	origStopCtx := systemdUnitStopperContext
	origLister := systemdUnitLister
	if reader != nil {
		systemdUnitStateReader = reader
	}
	if stopCtx != nil {
		systemdUnitStopperContext = stopCtx
	}
	if lister != nil {
		systemdUnitLister = lister
	}
	t.Cleanup(func() {
		systemdUnitStateReader = origReader
		systemdUnitStopperContext = origStopCtx
		systemdUnitLister = origLister
	})
}

func TestSaveStateWritesRootOnlyFile(t *testing.T) {
	o := newTestStateOrchestrator(t)
	o.vms["m1"] = &runningVM{
		instance: VMInstance{
			MachineID:        "m1",
			MachineSlug:      "machine-1",
			VMIP:             "192.168.100.10",
			TapDevice:        "tapm1",
			SocketPath:       "/tmp/m1.sock",
			RootfsPath:       "/tmp/m1.ext4",
			PID:              1234,
			VCPUs:            2,
			MemoryMB:         2048,
			ProxyToken:       "proxy-token",
			GatewayToken:     "gateway-token",
			RootfsVersion:    "rootfs-actual",
			OpenClawVersion:  "openclaw-actual",
			RuntimeOwnerKind: runtimeOwnerKindDirect,
			RuntimeOwnerRef:  "owner-ref",
			DataVolumePath:   "/tmp/m1-data.ext4",
			Status:           "running",
			CreatedAt:        time.Unix(100, 0).UTC(),
		},
		cancel: func() {},
		metaCfg: persistedMetadataConfig{
			MachineID:    "m1",
			MachineName:  "Machine 1",
			MachineSlug:  "machine-1",
			GatewayToken: "gateway-token",
			Nonce:        "nonce-1",
			Secrets: map[string]string{
				"OPENAI_API_KEY": "secret",
			},
		},
	}

	o.saveState()

	info, err := os.Stat(o.stateFilePath())
	if err != nil {
		t.Fatalf("stat state file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("expected state file mode 0600, got %o", got)
	}

	data, err := os.ReadFile(o.stateFilePath())
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var persisted []persistedVM
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("unmarshal state file: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("expected 1 persisted VM, got %d", len(persisted))
	}
	if persisted[0].RuntimeOwnerKind != runtimeOwnerKindDirect {
		t.Fatalf("expected persisted owner kind %q, got %q", runtimeOwnerKindDirect, persisted[0].RuntimeOwnerKind)
	}
	if persisted[0].RuntimeOwnerRef != "owner-ref" {
		t.Fatalf("expected persisted owner ref owner-ref, got %q", persisted[0].RuntimeOwnerRef)
	}
	if persisted[0].RootfsVersion != "rootfs-actual" {
		t.Fatalf("expected persisted rootfs version rootfs-actual, got %q", persisted[0].RootfsVersion)
	}
	if persisted[0].OpenClawVersion != "openclaw-actual" {
		t.Fatalf("expected persisted openclaw version openclaw-actual, got %q", persisted[0].OpenClawVersion)
	}
}

func TestRecoverReattachesLiveVM(t *testing.T) {
	o := newTestStateOrchestrator(t)
	registrar := newTestRegistrar()
	o.registrar = registrar
	stubRecoveryVerification(t)

	socketPath := filepath.Join(o.cfg.StateDir, "m1.sock")
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

	persisted := []persistedVM{{
		MachineID:      "m1",
		VMIP:           "192.168.100.10",
		TapDevice:      "tapm1",
		SocketPath:     socketPath,
		RootfsPath:     filepath.Join(o.cfg.StateDir, "m1.ext4"),
		DataVolumePath: filepath.Join(o.cfg.StateDir, "m1-data.ext4"),
		PID:            cmd.Process.Pid,
		VCPUs:          2,
		MemoryMB:       2048,
		ProxyToken:     "proxy-token",
		CreatedAt:      time.Unix(123, 0).UTC(),
		MetaCfg: persistedMetadataConfig{
			MachineID:    "m1",
			MachineName:  "Machine 1",
			MachineSlug:  "machine-1",
			GatewayToken: "gateway-token",
			Nonce:        "nonce-1",
			RuntimeSelection: &metadata.RuntimeSelection{
				ResolvedRootfsVersion:   "rootfs-2026.04.08",
				ResolvedOpenClawVersion: "openclaw-2026.04.08",
			},
		},
	}}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal persisted state: %v", err)
	}
	if err := os.WriteFile(o.stateFilePath(), data, 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	if err := o.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	vm, err := o.Get(context.Background(), "m1")
	if err != nil {
		t.Fatalf("get recovered vm: %v", err)
	}
	if vm.Status != "running" {
		t.Fatalf("expected recovered vm status running, got %s", vm.Status)
	}
	if vm.RootfsVersion != "rootfs-2026.04.08" {
		t.Fatalf("expected recovered rootfs version, got %q", vm.RootfsVersion)
	}
	if vm.OpenClawVersion != "openclaw-2026.04.08" {
		t.Fatalf("expected recovered openclaw version, got %q", vm.OpenClawVersion)
	}
	if _, ok := registrar.registered["192.168.100.10"]; !ok {
		t.Fatalf("expected metadata registration to be restored")
	}
}

func TestRecoverUsesPersistedActualVersions(t *testing.T) {
	o := newTestStateOrchestrator(t)
	registrar := newTestRegistrar()
	o.registrar = registrar
	stubRecoveryVerification(t)

	socketPath := filepath.Join(o.cfg.StateDir, "m1.sock")
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

	persisted := []persistedVM{{
		MachineID:       "m1",
		VMIP:            "192.168.100.10",
		TapDevice:       "tapm1",
		SocketPath:      socketPath,
		RootfsPath:      filepath.Join(o.cfg.StateDir, "m1.ext4"),
		DataVolumePath:  filepath.Join(o.cfg.StateDir, "m1-data.ext4"),
		RootfsVersion:   "rootfs-actual",
		OpenClawVersion: "openclaw-actual",
		PID:             cmd.Process.Pid,
		VCPUs:           2,
		MemoryMB:        2048,
		ProxyToken:      "proxy-token",
		CreatedAt:       time.Unix(123, 0).UTC(),
		MetaCfg: persistedMetadataConfig{
			MachineID:    "m1",
			MachineName:  "Machine 1",
			MachineSlug:  "machine-1",
			GatewayToken: "gateway-token",
			Nonce:        "nonce-1",
			RuntimeSelection: &metadata.RuntimeSelection{
				ResolvedRootfsVersion:   "rootfs-requested",
				ResolvedOpenClawVersion: "openclaw-requested",
			},
		},
	}}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal persisted state: %v", err)
	}
	if err := os.WriteFile(o.stateFilePath(), data, 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	if err := o.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	vm, err := o.Get(context.Background(), "m1")
	if err != nil {
		t.Fatalf("get recovered VM: %v", err)
	}
	if vm.RootfsVersion != "rootfs-actual" {
		t.Fatalf("recovered rootfs version = %q, want rootfs-actual", vm.RootfsVersion)
	}
	if vm.OpenClawVersion != "openclaw-actual" {
		t.Fatalf("recovered openclaw version = %q, want openclaw-actual", vm.OpenClawVersion)
	}
}

func TestRecoverCleansDeadVMArtifacts(t *testing.T) {
	o := newTestStateOrchestrator(t)
	stubRecoveryVerification(t)

	rootfsPath := filepath.Join(o.cfg.StateDir, "dead.ext4")
	socketPath := filepath.Join(o.cfg.StateDir, "dead.sock")
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o600); err != nil {
		t.Fatalf("write rootfs placeholder: %v", err)
	}
	if err := os.WriteFile(socketPath, []byte("socket"), 0o600); err != nil {
		t.Fatalf("write socket placeholder: %v", err)
	}

	persisted := []persistedVM{{
		MachineID:      "dead",
		VMIP:           "192.168.100.11",
		TapDevice:      "tapdead",
		SocketPath:     socketPath,
		RootfsPath:     rootfsPath,
		DataVolumePath: filepath.Join(o.cfg.StateDir, "dead-data.ext4"),
		PID:            999999,
		VCPUs:          1,
		MemoryMB:       1024,
		ProxyToken:     "proxy-token",
		MetaCfg: persistedMetadataConfig{
			MachineID:    "dead",
			MachineName:  "Dead VM",
			MachineSlug:  "dead-vm",
			GatewayToken: "gateway-token",
			Nonce:        "nonce-dead",
		},
	}}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal persisted state: %v", err)
	}
	if err := os.WriteFile(o.stateFilePath(), data, 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	if err := o.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if _, err := o.Get(context.Background(), "dead"); err == nil {
		t.Fatalf("expected dead vm to not be recovered")
	}
	if _, err := os.Stat(rootfsPath); !os.IsNotExist(err) {
		t.Fatalf("expected rootfs placeholder to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("expected socket placeholder to be removed, stat err=%v", err)
	}
}

func TestSaveBrowserStateUsesStoredPID(t *testing.T) {
	o := newTestStateOrchestrator(t)
	o.browserVMs["b1"] = &browserRunningVM{
		ID:               "b1",
		VMIP:             "192.168.100.50",
		TapDevice:        "tapb1",
		SocketPath:       "/tmp/b1.sock",
		RootfsPath:       "/tmp/b1.ext4",
		Status:           "ready",
		CDPPort:          9222,
		PID:              4321,
		RuntimeOwnerKind: runtimeOwnerKindDirect,
		RuntimeOwnerRef:  "browser-owner-ref",
		cancel:           func() {},
	}

	o.saveBrowserState()

	data, err := os.ReadFile(o.browserStateFilePath())
	if err != nil {
		t.Fatalf("read browser state: %v", err)
	}
	var persisted []persistedBrowserVM
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("unmarshal browser state: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("expected 1 persisted browser VM, got %d", len(persisted))
	}
	if persisted[0].PID != 4321 {
		t.Fatalf("expected persisted PID 4321, got %d", persisted[0].PID)
	}
	if persisted[0].RuntimeOwnerKind != runtimeOwnerKindDirect {
		t.Fatalf("expected persisted browser owner kind %q, got %q", runtimeOwnerKindDirect, persisted[0].RuntimeOwnerKind)
	}
	if persisted[0].RuntimeOwnerRef != "browser-owner-ref" {
		t.Fatalf("expected persisted browser owner ref browser-owner-ref, got %q", persisted[0].RuntimeOwnerRef)
	}
}

func TestRecoverBrowserVMPreservesPIDAcrossSave(t *testing.T) {
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
		ID:         "b1",
		VMIP:       "192.168.100.50",
		TapDevice:  "tapb1",
		SocketPath: socketPath,
		RootfsPath: filepath.Join(o.cfg.StateDir, "b1.ext4"),
		CDPPort:    9222,
		Status:     "ready",
		PID:        cmd.Process.Pid,
		CreatedAt:  time.Unix(123, 0).UTC(),
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
	o.saveBrowserState()

	data, err = os.ReadFile(o.browserStateFilePath())
	if err != nil {
		t.Fatalf("read browser state after save: %v", err)
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
}

func TestRecoverQuarantinesUnresolvedVM(t *testing.T) {
	o := newTestStateOrchestrator(t)
	stubRecoveryVerification(t)

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	persisted := []persistedVM{{
		MachineID:      "q1",
		VMIP:           "192.168.100.60",
		TapDevice:      "tapq1",
		SocketPath:     filepath.Join(o.cfg.StateDir, "missing.sock"),
		RootfsPath:     filepath.Join(o.cfg.StateDir, "q1.ext4"),
		DataVolumePath: filepath.Join(o.cfg.StateDir, "q1-data.ext4"),
		PID:            cmd.Process.Pid,
		VCPUs:          1,
		MemoryMB:       1024,
		ProxyToken:     "proxy-token",
		MetaCfg: persistedMetadataConfig{
			MachineID:    "q1",
			MachineName:  "Quarantine VM",
			MachineSlug:  "q1",
			GatewayToken: "gateway-token",
			Nonce:        "nonce-q1",
		},
	}}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal persisted state: %v", err)
	}
	if err := os.WriteFile(o.stateFilePath(), data, 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	if err := o.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	data, err = os.ReadFile(o.stateFilePath())
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var saved []persistedVM
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("unmarshal saved state: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("expected 1 persisted VM after quarantine, got %d", len(saved))
	}
	if !saved[0].Quarantined {
		t.Fatalf("expected persisted VM to be quarantined")
	}
	if saved[0].QuarantineReason != "socket_missing" {
		t.Fatalf("expected quarantine reason socket_missing, got %q", saved[0].QuarantineReason)
	}
}

func TestRecoverQuarantinesUnresolvedBrowserVM(t *testing.T) {
	o := newTestStateOrchestrator(t)
	stubRecoveryVerification(t)

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	persisted := []persistedBrowserVM{{
		ID:         "bq1",
		VMIP:       "192.168.100.61",
		TapDevice:  "tapbq1",
		SocketPath: filepath.Join(o.cfg.StateDir, "missing-browser.sock"),
		RootfsPath: filepath.Join(o.cfg.StateDir, "bq1.ext4"),
		CDPPort:    9222,
		Status:     "ready",
		PID:        cmd.Process.Pid,
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
	o.saveBrowserState()

	data, err = os.ReadFile(o.browserStateFilePath())
	if err != nil {
		t.Fatalf("read browser state file: %v", err)
	}
	var saved []persistedBrowserVM
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("unmarshal saved browser state: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("expected 1 persisted browser VM after quarantine, got %d", len(saved))
	}
	if !saved[0].Quarantined {
		t.Fatalf("expected persisted browser VM to be quarantined")
	}
	if saved[0].QuarantineReason != "socket_missing" {
		t.Fatalf("expected quarantine reason socket_missing, got %q", saved[0].QuarantineReason)
	}
}

func TestRecoverSystemdOwnedVMUsesUnitMainPID(t *testing.T) {
	o := newTestStateOrchestrator(t)
	registrar := newTestRegistrar()
	o.registrar = registrar
	stubRecoveryVerification(t)

	socketPath := filepath.Join(o.cfg.StateDir, "systemd.sock")
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

	stubSystemdState(t, func(unit string) (systemdUnitState, error) {
		if unit != "ocm-vm-m1.service" {
			t.Fatalf("unexpected unit lookup: %s", unit)
		}
		return systemdUnitState{
			LoadState:   "loaded",
			ActiveState: "active",
			SubState:    "running",
			MainPID:     cmd.Process.Pid,
		}, nil
	}, nil, nil)

	persisted := []persistedVM{{
		MachineID:        "m1",
		VMIP:             "192.168.100.10",
		TapDevice:        "tapm1",
		SocketPath:       socketPath,
		RootfsPath:       filepath.Join(o.cfg.StateDir, "m1.ext4"),
		DataVolumePath:   filepath.Join(o.cfg.StateDir, "m1-data.ext4"),
		PID:              1,
		VCPUs:            2,
		MemoryMB:         2048,
		RuntimeOwnerKind: runtimeOwnerKindSystemdVML,
		RuntimeOwnerRef:  "ocm-vm-m1.service",
		MetaCfg: persistedMetadataConfig{
			MachineID:    "m1",
			MachineName:  "Machine 1",
			MachineSlug:  "machine-1",
			GatewayToken: "gateway-token",
			Nonce:        "nonce-1",
		},
	}}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal persisted state: %v", err)
	}
	if err := os.WriteFile(o.stateFilePath(), data, 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	if err := o.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	vm, err := o.Get(context.Background(), "m1")
	if err != nil {
		t.Fatalf("get recovered vm: %v", err)
	}
	if vm.PID != cmd.Process.Pid {
		t.Fatalf("expected recovered PID %d from systemd state, got %d", cmd.Process.Pid, vm.PID)
	}
}

func TestRecoverSystemdOwnedVMQuarantinesInactiveUnit(t *testing.T) {
	o := newTestStateOrchestrator(t)
	stubRecoveryVerification(t)

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	stubSystemdState(t, func(unit string) (systemdUnitState, error) {
		return systemdUnitState{
			LoadState:   "loaded",
			ActiveState: "inactive",
			SubState:    "dead",
			MainPID:     0,
		}, nil
	}, nil, nil)

	persisted := []persistedVM{{
		MachineID:        "m2",
		VMIP:             "192.168.100.12",
		TapDevice:        "tapm2",
		SocketPath:       filepath.Join(o.cfg.StateDir, "missing.sock"),
		RootfsPath:       filepath.Join(o.cfg.StateDir, "m2.ext4"),
		DataVolumePath:   filepath.Join(o.cfg.StateDir, "m2-data.ext4"),
		PID:              cmd.Process.Pid,
		RuntimeOwnerKind: runtimeOwnerKindSystemdVML,
		RuntimeOwnerRef:  "ocm-vm-m2.service",
		MetaCfg: persistedMetadataConfig{
			MachineID:    "m2",
			MachineName:  "Machine 2",
			MachineSlug:  "machine-2",
			GatewayToken: "gateway-token",
			Nonce:        "nonce-2",
		},
	}}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal persisted state: %v", err)
	}
	if err := os.WriteFile(o.stateFilePath(), data, 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	if err := o.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	data, err = os.ReadFile(o.stateFilePath())
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var saved []persistedVM
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("unmarshal saved state: %v", err)
	}
	if len(saved) != 1 || !saved[0].Quarantined {
		t.Fatalf("expected quarantined persisted VM, got %#v", saved)
	}
	if saved[0].QuarantineReason != "unit_inactive" {
		t.Fatalf("expected quarantine reason unit_inactive, got %q", saved[0].QuarantineReason)
	}
}

func TestSystemdOwnerStopUsesUnitRef(t *testing.T) {
	var stoppedUnit string
	stubSystemdState(t, nil, func(_ context.Context, unit string) error {
		stoppedUnit = unit
		return nil
	}, nil)

	owner := systemdUnitRuntimeOwner{}
	if err := owner.stop(context.Background(), "ocm-vm-stopme.service", nil, nil, time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if stoppedUnit != "ocm-vm-stopme.service" {
		t.Fatalf("expected systemd stop for unit ref, got %q", stoppedUnit)
	}
}

func TestRecoverSystemdOwnedVMInfersMissingOwnerRef(t *testing.T) {
	o := newTestStateOrchestrator(t)
	stubRecoveryVerification(t)

	socketPath := filepath.Join(o.cfg.StateDir, "m3.sock")
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

	stubSystemdState(t,
		func(unit string) (systemdUnitState, error) {
			if unit != "ocm-vm-m3.service" {
				t.Fatalf("unexpected unit lookup: %s", unit)
			}
			return systemdUnitState{
				Unit:        unit,
				LoadState:   "loaded",
				ActiveState: "active",
				SubState:    "running",
				MainPID:     cmd.Process.Pid,
			}, nil
		},
		nil,
		func(patterns ...string) ([]systemdUnitState, error) {
			return []systemdUnitState{{
				Unit:        "ocm-vm-m3.service",
				LoadState:   "loaded",
				ActiveState: "active",
				SubState:    "running",
			}}, nil
		},
	)

	persisted := []persistedVM{{
		MachineID:        "m3",
		VMIP:             "192.168.100.13",
		TapDevice:        "tapm3",
		SocketPath:       socketPath,
		RootfsPath:       filepath.Join(o.cfg.StateDir, "m3.ext4"),
		DataVolumePath:   filepath.Join(o.cfg.StateDir, "m3-data.ext4"),
		PID:              123,
		RuntimeOwnerKind: runtimeOwnerKindSystemdVML,
		MetaCfg: persistedMetadataConfig{
			MachineID:    "m3",
			MachineName:  "Machine 3",
			MachineSlug:  "machine-3",
			GatewayToken: "gateway-token",
			Nonce:        "nonce-3",
		},
	}}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal persisted state: %v", err)
	}
	if err := os.WriteFile(o.stateFilePath(), data, 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	if err := o.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	vm, err := o.Get(context.Background(), "m3")
	if err != nil {
		t.Fatalf("get recovered vm: %v", err)
	}
	if vm.RuntimeOwnerRef != "ocm-vm-m3.service" {
		t.Fatalf("expected inferred owner ref ocm-vm-m3.service, got %q", vm.RuntimeOwnerRef)
	}
	if vm.PID != cmd.Process.Pid {
		t.Fatalf("expected recovered PID %d, got %d", cmd.Process.Pid, vm.PID)
	}
}

func TestRecoverSystemdOwnedVMQuarantinesWhenInventoryLacksUnit(t *testing.T) {
	o := newTestStateOrchestrator(t)
	stubRecoveryVerification(t)

	stubSystemdState(t,
		func(unit string) (systemdUnitState, error) {
			t.Fatalf("state reader should not be called when owner ref is left blank, got %q", unit)
			return systemdUnitState{}, nil
		},
		nil,
		func(patterns ...string) ([]systemdUnitState, error) {
			// Authoritative inventory that does NOT include ocm-vm-m4.service —
			// inference should leave the ref blank so recovery quarantines the
			// VM with "unit_ref_missing".
			return []systemdUnitState{{
				Unit:        "ocm-vm-someone-else.service",
				LoadState:   "loaded",
				ActiveState: "active",
				SubState:    "running",
			}}, nil
		},
	)

	persisted := []persistedVM{{
		MachineID:        "m4",
		VMIP:             "192.168.100.14",
		TapDevice:        "tapm4",
		SocketPath:       filepath.Join(o.cfg.StateDir, "m4.sock"),
		RootfsPath:       filepath.Join(o.cfg.StateDir, "m4.ext4"),
		DataVolumePath:   filepath.Join(o.cfg.StateDir, "m4-data.ext4"),
		PID:              99999,
		RuntimeOwnerKind: runtimeOwnerKindSystemdVML,
		MetaCfg: persistedMetadataConfig{
			MachineID:    "m4",
			MachineName:  "Machine 4",
			MachineSlug:  "machine-4",
			GatewayToken: "gateway-token",
			Nonce:        "nonce-4",
		},
	}}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal persisted state: %v", err)
	}
	if err := os.WriteFile(o.stateFilePath(), data, 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	if err := o.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	data, err = os.ReadFile(o.stateFilePath())
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var saved []persistedVM
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("unmarshal saved state: %v", err)
	}
	if len(saved) != 1 || !saved[0].Quarantined {
		t.Fatalf("expected quarantined persisted VM, got %#v", saved)
	}
	if saved[0].QuarantineReason != "unit_ref_missing" {
		t.Fatalf("expected quarantine reason unit_ref_missing, got %q", saved[0].QuarantineReason)
	}
}

func TestReplaceLLMKeysRemovesAbsentKeys(t *testing.T) {
	o := newTestStateOrchestrator(t)
	registrar := newTestRegistrar()
	o.registrar = registrar

	vmIP := "192.168.100.20"
	o.vms["replace-test"] = &runningVM{
		instance: VMInstance{
			MachineID:   "replace-test",
			MachineSlug: "replace-test",
			VMIP:        vmIP,
			Status:      "running",
		},
		cancel: func() {},
		metaCfg: persistedMetadataConfig{
			MachineID:    "replace-test",
			MachineSlug:  "replace-test",
			GatewayToken: "gw-token",
			Nonce:        "nonce-1",
			LLMKeys: map[string]metadata.CredentialEntry{
				"anthropic": {Value: "sk-ant-key", CredentialType: "api_key"},
				"openai":    {Value: "sk-openai-key", CredentialType: "api_key"},
			},
		},
	}

	// Register the machine in the registrar with both keys.
	registrar.RegisterMachine(vmIP, metadata.MachineConfig{
		MachineID: "replace-test",
		Nonce:     "nonce-1",
		LLMKeys: map[string]metadata.CredentialEntry{
			"anthropic": {Value: "sk-ant-key", CredentialType: "api_key"},
			"openai":    {Value: "sk-openai-key", CredentialType: "api_key"},
		},
	})

	// Replace with only anthropic — openai should be removed.
	err := o.ReplaceLLMKeys(context.Background(), "replace-test", map[string]metadata.CredentialEntry{
		"anthropic": {Value: "sk-ant-key-new", CredentialType: "api_key"},
	})
	if err != nil {
		t.Fatalf("ReplaceLLMKeys: %v", err)
	}

	// Verify the registrar's config only has anthropic.
	cfg := registrar.registered[vmIP]
	if len(cfg.LLMKeys) != 1 {
		t.Fatalf("expected 1 LLM key after replace, got %d: %v", len(cfg.LLMKeys), cfg.LLMKeys)
	}
	if _, ok := cfg.LLMKeys["anthropic"]; !ok {
		t.Fatalf("expected anthropic key to remain, got %v", cfg.LLMKeys)
	}
	if _, ok := cfg.LLMKeys["openai"]; ok {
		t.Fatalf("expected openai key to be removed, but it still exists")
	}

	// Verify in-memory VM state matches.
	vm := o.vms["replace-test"]
	if len(vm.metaCfg.LLMKeys) != 1 {
		t.Fatalf("expected 1 in-memory LLM key, got %d", len(vm.metaCfg.LLMKeys))
	}
	if vm.metaCfg.LLMKeys["anthropic"].Value != "sk-ant-key-new" {
		t.Fatalf("expected updated anthropic key value, got %q", vm.metaCfg.LLMKeys["anthropic"].Value)
	}
}

func TestReplaceLLMKeysEmptyMapClearsAll(t *testing.T) {
	o := newTestStateOrchestrator(t)
	registrar := newTestRegistrar()
	o.registrar = registrar

	vmIP := "192.168.100.21"
	o.vms["clear-test"] = &runningVM{
		instance: VMInstance{
			MachineID:   "clear-test",
			MachineSlug: "clear-test",
			VMIP:        vmIP,
			Status:      "running",
		},
		cancel: func() {},
		metaCfg: persistedMetadataConfig{
			MachineID:    "clear-test",
			MachineSlug:  "clear-test",
			GatewayToken: "gw-token",
			Nonce:        "nonce-2",
			LLMKeys: map[string]metadata.CredentialEntry{
				"anthropic": {Value: "sk-ant-key", CredentialType: "api_key"},
				"openai":    {Value: "sk-openai-key", CredentialType: "api_key"},
			},
		},
	}

	registrar.RegisterMachine(vmIP, metadata.MachineConfig{
		MachineID: "clear-test",
		Nonce:     "nonce-2",
		LLMKeys: map[string]metadata.CredentialEntry{
			"anthropic": {Value: "sk-ant-key", CredentialType: "api_key"},
			"openai":    {Value: "sk-openai-key", CredentialType: "api_key"},
		},
	})

	// Replace with empty map — all keys should be cleared.
	err := o.ReplaceLLMKeys(context.Background(), "clear-test", map[string]metadata.CredentialEntry{})
	if err != nil {
		t.Fatalf("ReplaceLLMKeys: %v", err)
	}

	cfg := registrar.registered[vmIP]
	if len(cfg.LLMKeys) != 0 {
		t.Fatalf("expected 0 LLM keys after empty replace, got %d: %v", len(cfg.LLMKeys), cfg.LLMKeys)
	}

	vm := o.vms["clear-test"]
	if len(vm.metaCfg.LLMKeys) != 0 {
		t.Fatalf("expected 0 in-memory LLM keys, got %d", len(vm.metaCfg.LLMKeys))
	}
}

func TestReplaceLLMKeysPreservesOtherConfig(t *testing.T) {
	o := newTestStateOrchestrator(t)
	registrar := newTestRegistrar()
	o.registrar = registrar

	vmIP := "192.168.100.22"
	o.vms["preserve-test"] = &runningVM{
		instance: VMInstance{
			MachineID:   "preserve-test",
			MachineSlug: "preserve-test",
			VMIP:        vmIP,
			Status:      "running",
		},
		cancel: func() {},
		metaCfg: persistedMetadataConfig{
			MachineID:    "preserve-test",
			MachineSlug:  "preserve-test",
			GatewayToken: "gw-token-preserve",
			Nonce:        "nonce-3",
			Secrets: map[string]string{
				"SECRET_ONE": "value-one",
				"SECRET_TWO": "value-two",
			},
			LLMKeys: map[string]metadata.CredentialEntry{
				"anthropic": {Value: "sk-ant-key", CredentialType: "api_key"},
			},
		},
	}

	registrar.RegisterMachine(vmIP, metadata.MachineConfig{
		MachineID:    "preserve-test",
		GatewayToken: "gw-token-preserve",
		Nonce:        "nonce-3",
		Secrets: map[string]string{
			"SECRET_ONE": "value-one",
			"SECRET_TWO": "value-two",
		},
		LLMKeys: map[string]metadata.CredentialEntry{
			"anthropic": {Value: "sk-ant-key", CredentialType: "api_key"},
		},
	})

	// Replace LLM keys with a different set.
	err := o.ReplaceLLMKeys(context.Background(), "preserve-test", map[string]metadata.CredentialEntry{
		"google": {Value: "AIza-new-key", CredentialType: "api_key"},
	})
	if err != nil {
		t.Fatalf("ReplaceLLMKeys: %v", err)
	}

	// Verify MachineID is unchanged.
	vm := o.vms["preserve-test"]
	if vm.metaCfg.MachineID != "preserve-test" {
		t.Fatalf("expected MachineID preserve-test, got %q", vm.metaCfg.MachineID)
	}

	// Verify GatewayToken is unchanged.
	if vm.metaCfg.GatewayToken != "gw-token-preserve" {
		t.Fatalf("expected GatewayToken gw-token-preserve, got %q", vm.metaCfg.GatewayToken)
	}

	// Verify Secrets are unchanged.
	if len(vm.metaCfg.Secrets) != 2 {
		t.Fatalf("expected 2 secrets, got %d", len(vm.metaCfg.Secrets))
	}
	if vm.metaCfg.Secrets["SECRET_ONE"] != "value-one" {
		t.Fatalf("expected SECRET_ONE = value-one, got %q", vm.metaCfg.Secrets["SECRET_ONE"])
	}
	if vm.metaCfg.Secrets["SECRET_TWO"] != "value-two" {
		t.Fatalf("expected SECRET_TWO = value-two, got %q", vm.metaCfg.Secrets["SECRET_TWO"])
	}

	// Verify LLM keys were replaced.
	if len(vm.metaCfg.LLMKeys) != 1 {
		t.Fatalf("expected 1 LLM key, got %d", len(vm.metaCfg.LLMKeys))
	}
	if _, ok := vm.metaCfg.LLMKeys["google"]; !ok {
		t.Fatalf("expected google key, got %v", vm.metaCfg.LLMKeys)
	}
}

func TestRecoverBrowserOnlyStateWithoutMainVMs(t *testing.T) {
	o := newTestStateOrchestrator(t)
	stubRecoveryVerification(t)

	socketPath := filepath.Join(o.cfg.StateDir, "browser-only.sock")
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

	browserPersisted := []persistedBrowserVM{{
		ID:         "browser-only",
		VMIP:       "192.168.100.70",
		TapDevice:  "tapbrowseronly",
		SocketPath: socketPath,
		RootfsPath: filepath.Join(o.cfg.StateDir, "browser-only.ext4"),
		CDPPort:    9222,
		Status:     "ready",
		PID:        cmd.Process.Pid,
	}}
	data, err := json.Marshal(browserPersisted)
	if err != nil {
		t.Fatalf("marshal browser state: %v", err)
	}
	if err := os.WriteFile(o.browserStateFilePath(), data, 0o600); err != nil {
		t.Fatalf("write browser state file: %v", err)
	}

	if err := o.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	browserVMs, err := o.ListBrowserVMs(context.Background())
	if err != nil {
		t.Fatalf("list browser VMs: %v", err)
	}
	if len(browserVMs) != 1 {
		t.Fatalf("expected 1 recovered browser VM, got %d", len(browserVMs))
	}
	if browserVMs[0].ID != "browser-only" {
		t.Fatalf("expected recovered browser VM id browser-only, got %q", browserVMs[0].ID)
	}
}
