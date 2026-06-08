//go:build linux

package orchestrator

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"

	"github.com/mathaix/openclawmachines/backend/internal/config"
	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/network"
	"github.com/mathaix/openclawmachines/backend/internal/rootfs"
)

// firecrackerOrchestrator manages Firecracker VMs on Linux.
type firecrackerOrchestrator struct {
	cfg    FirecrackerConfig
	bridge *network.Bridge

	runtimeOwner firecrackerRuntimeOwner

	mu             sync.RWMutex
	vms            map[string]*runningVM // keyed by machineID
	quarantinedVMs map[string]persistedVM
	registrar      MetadataRegistrar

	browserMu             sync.Mutex
	browserCreateMu       sync.Mutex
	browserVMs            map[string]*browserRunningVM // keyed by browserVMID
	quarantinedBrowserVMs map[string]persistedBrowserVM
}

// browserRunningVM holds runtime state for a standalone browser VM.
type browserRunningVM struct {
	ID               string
	VMIP             string
	TapDevice        string
	SocketPath       string
	RootfsPath       string
	Status           string // creating, running, ready, error
	CDPError         string
	CDPPort          int
	PID              int
	RuntimeOwnerKind string
	RuntimeOwnerRef  string
	WebRTCPortBase   int // inclusive start of Neko WebRTC UDP range
	WebRTCPortEnd    int // inclusive end of Neko WebRTC UDP range
	machine          *firecracker.Machine
	cancel           context.CancelFunc
}

// WebRTC port allocation — each browser VM gets its own 100-port slice of
// UDP so multiple VMs on the same host can all stream media concurrently.
// Slot N owns ports [browserWebRTCBasePort + N*browserWebRTCSlotSize,
// browserWebRTCBasePort + (N+1)*browserWebRTCSlotSize - 1].
const (
	browserWebRTCBasePort      = 56000
	browserWebRTCSlotSize      = 100
	browserWebRTCMaxSlots      = 50
	browserCDPReadinessTimeout = 10 * time.Minute
)

var firecrackerPIDVerifier = func(pid int) bool {
	comm, err := os.ReadFile(filepath.Join("/proc", fmt.Sprintf("%d", pid), "comm"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(comm)) == "firecracker"
}

var firecrackerMachineIDVerifier = func(ctx context.Context, machine *firecracker.Machine) (string, error) {
	info, err := machine.DescribeInstanceInfo(ctx)
	if err != nil {
		return "", err
	}
	if info.ID == nil || strings.TrimSpace(*info.ID) == "" {
		return "", fmt.Errorf("firecracker instance info missing ID")
	}
	return strings.TrimSpace(*info.ID), nil
}

// browserDNATReplayer is invoked during browser VM recovery to reinstall the
// WebRTC UDP DNAT chain for an already-live browser VM. iptables state is
// ephemeral across host reboots, so without this replay CDP keeps working but
// Neko live-view silently dies after a host restart. Overridden in tests.
var browserDNATReplayer = func(b *network.Bridge, browserVMID, vmIP string, portStart, portEnd int) error {
	return b.InstallBrowserDNATChain(browserVMID, vmIP, portStart, portEnd)
}

// runningVM holds runtime state for a single Firecracker VM.
type runningVM struct {
	instance VMInstance
	machine  *firecracker.Machine
	cancel   context.CancelFunc
	metaCfg  persistedMetadataConfig
}

func (o *firecrackerOrchestrator) owner() firecrackerRuntimeOwner {
	if o.runtimeOwner != nil {
		return o.runtimeOwner
	}
	return directFirecrackerRuntimeOwner{}
}

func versionFieldsFromRuntimeSelection(selection *metadata.RuntimeSelection) (rootfsVersion, openClawVersion string) {
	if selection == nil {
		return "", ""
	}
	runtimeVersion := strings.TrimSpace(selection.ResolvedOpenClawVersion)
	if strings.TrimSpace(selection.Kind) == "hermes" {
		runtimeVersion = strings.TrimSpace(selection.ResolvedHermesVersion)
	}
	return strings.TrimSpace(selection.ResolvedRootfsVersion), runtimeVersion
}

func (o *firecrackerOrchestrator) stagedRootfsVersion() (string, error) {
	if strings.TrimSpace(o.cfg.GCSRootfsManifest) == "" {
		return "", nil
	}
	manifest, err := rootfs.ReadCachedManifest(filepath.Join(o.cfg.StateDir, ".rootfs-manifest.json"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(manifest.Version), nil
}

func (o *firecrackerOrchestrator) effectiveRootfsVersion(selection *metadata.RuntimeSelection) (string, error) {
	expected := resolvedRootfsVersion(selection)
	if rootfsManifestURI(selection, o.cfg.GCSRootfsManifest) != strings.TrimSpace(o.cfg.GCSRootfsManifest) {
		return expected, nil
	}
	actual, err := o.stagedRootfsVersion()
	if err != nil {
		if expected == "" {
			return "", nil
		}
		return "", fmt.Errorf("staged rootfs version unavailable for requested machine rootfs %q: %w", expected, err)
	}
	if actual == "" {
		return expected, nil
	}
	if expected != "" && actual != expected {
		return "", fmt.Errorf("staged rootfs version %q does not match requested machine rootfs %q", actual, expected)
	}
	return actual, nil
}

func (o *firecrackerOrchestrator) machineRootfsBaseDir() string {
	return filepath.Join(o.cfg.StateDir, "rootfs")
}

func (o *firecrackerOrchestrator) resolveMachineRootfsSource(ctx context.Context, selection *metadata.RuntimeSelection) (string, string, error) {
	expected := resolvedRootfsVersion(selection)
	manifestURI := rootfsManifestURI(selection, o.cfg.GCSRootfsManifest)
	if strings.TrimSpace(manifestURI) == "" || expected == "" {
		actual, err := o.effectiveRootfsVersion(selection)
		if err != nil {
			return "", "", err
		}
		return o.baseRootfsPath(), actual, nil
	}

	timeout := o.cfg.GCSDownloadTimeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	retries := o.cfg.GCSRetryAttempts
	if retries == 0 {
		retries = 3
	}

	stageCtx, cancel := context.WithTimeout(ctx, timeout+time.Minute)
	defer cancel()

	fetcher, err := rootfs.NewFetcher(stageCtx, rootfs.Config{
		ManifestURI:     manifestURI,
		DownloadTimeout: timeout,
		RetryAttempts:   retries,
	})
	if err != nil {
		return "", "", fmt.Errorf("create versioned rootfs fetcher: %w", err)
	}
	defer func() { _ = fetcher.Close() }()

	path, err := fetcher.EnsureRootfsRelease(stageCtx, o.machineRootfsBaseDir(), expected)
	if err != nil {
		return "", "", fmt.Errorf("ensure requested rootfs version %q: %w", expected, err)
	}
	return path, expected, nil
}

func rootfsManifestURI(selection *metadata.RuntimeSelection, fallback string) string {
	if selection != nil && strings.TrimSpace(selection.RootfsManifestURI) != "" {
		return strings.TrimSpace(selection.RootfsManifestURI)
	}
	return strings.TrimSpace(fallback)
}

// New creates a new Firecracker orchestrator on Linux.
func New(cfg FirecrackerConfig, bridge *network.Bridge) (Orchestrator, error) {
	runtimeOwner, err := runtimeOwnerFromKind(cfg.RuntimeOwnerKind)
	if err != nil {
		return nil, err
	}
	o := &firecrackerOrchestrator{
		cfg:                   cfg,
		bridge:                bridge,
		runtimeOwner:          runtimeOwner,
		vms:                   make(map[string]*runningVM),
		quarantinedVMs:        make(map[string]persistedVM),
		browserVMs:            make(map[string]*browserRunningVM),
		quarantinedBrowserVMs: make(map[string]persistedBrowserVM),
	}

	// Ensure directories exist
	if err := os.MkdirAll(cfg.SocketDir, 0755); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	if err := os.MkdirAll(cfg.StateDir, 0755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	if browserStateDir := strings.TrimSpace(cfg.BrowserStateDir); browserStateDir != "" && browserStateDir != cfg.StateDir {
		if err := os.MkdirAll(browserStateDir, 0755); err != nil {
			return nil, fmt.Errorf("create browser state dir: %w", err)
		}
	}
	if cfg.DataDir != "" {
		if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	if cfg.OpenClawRuntimeDir != "" {
		if err := os.MkdirAll(cfg.OpenClawRuntimeDir, 0755); err != nil {
			return nil, fmt.Errorf("create openclaw runtime dir: %w", err)
		}
	}
	if cfg.HermesRuntimeDir != "" {
		if err := os.MkdirAll(cfg.HermesRuntimeDir, 0755); err != nil {
			return nil, fmt.Errorf("create hermes runtime dir: %w", err)
		}
	}
	if runtimeStateDir := o.runtimeStateRoot(); runtimeStateDir != "" {
		if err := os.MkdirAll(runtimeStateDir, 0755); err != nil {
			return nil, fmt.Errorf("create runtime state dir: %w", err)
		}
	}

	// Stage base rootfs onto XFS StateDir so reflink copies are instant.
	// RootfsDir is on ext4 (boot disk), StateDir is on XFS (scratch disk).
	// Reflinks only work within the same filesystem.
	if err := o.stageBaseRootfs(); err != nil {
		return nil, fmt.Errorf("stage base rootfs: %w", err)
	}

	// Stage browser rootfs if configured (non-fatal — browser VM support is optional).
	// Browser rootfs caches are staged under BrowserStateDir when configured so
	// experimental browser images can use dedicated XFS/reflink storage without
	// moving normal VM state.
	if cfg.GCSBrowserRootfsManifest != "" {
		if _, _, err := o.stageBrowserRootfs(); err != nil {
			slog.Warn("browser_rootfs.stage.failed", "error", err,
				"note", "browser companion VM support will be unavailable")
		}
	} else {
		slog.Warn("browser_rootfs.not_configured",
			"note", "browser-rootfs-gcs-manifest not set in instance metadata — browser companion VMs will not be available")
	}

	return o, nil
}

func (o *firecrackerOrchestrator) SetMetadataRegistrar(registrar MetadataRegistrar) {
	o.registrar = registrar
}

func (o *firecrackerOrchestrator) Create(ctx context.Context, cfg VMConfig) error {
	o.mu.Lock()
	if vm, exists := o.vms[cfg.MachineID]; exists {
		// Allow creation if VM was pre-registered via RegisterPending (status="creating")
		if vm.instance.Status != "creating" {
			o.mu.Unlock()
			return fmt.Errorf("VM %s already exists", cfg.MachineID)
		}
	}
	// Insert a "creating" sentinel so concurrent Create calls for the same
	// machine are rejected while resource allocation is in progress.
	o.vms[cfg.MachineID] = &runningVM{
		instance: VMInstance{MachineID: cfg.MachineID, Status: "creating"},
	}
	// Capacity is enforced by the control plane's placement service.
	// No agent-side VM cap — the control plane is the single authority.
	o.mu.Unlock()

	// On failure, remove the sentinel so the machine ID is available again.
	var createSucceeded bool
	defer func() {
		if !createSucceeded {
			o.mu.Lock()
			if vm, exists := o.vms[cfg.MachineID]; exists && vm.instance.Status == "creating" {
				delete(o.vms, cfg.MachineID)
			}
			o.mu.Unlock()
		}
	}()

	// Apply defaults if not specified
	if cfg.VCPUs <= 0 {
		cfg.VCPUs = o.cfg.DefaultVCPUs
	}
	if cfg.MemoryMB <= 0 {
		cfg.MemoryMB = o.cfg.DefaultMemoryMB
	}

	slog.Info("vm.creating",
		"machine_id", cfg.MachineID,
		"vcpus", cfg.VCPUs,
		"memory_mb", cfg.MemoryMB,
		"vm_ip", cfg.VMIP)

	// TAP device name: "tap" + first 11 chars of UUID (15-char limit)
	tapName := "tap" + cfg.MachineID[:min(11, len(cfg.MachineID))]

	// Create TAP device — if stale TAP exists from a previous run, remove it first and retry
	if err := o.bridge.CreateTap(tapName); err != nil {
		slog.Warn("vm.create.tap_retry", "tap", tapName, "error", err)
		_ = o.bridge.RemoveTap(tapName)
		if retryErr := o.bridge.CreateTap(tapName); retryErr != nil {
			return fmt.Errorf("create tap %s: %w", tapName, retryErr)
		}
	}

	// Copy rootfs via reflink (source is on XFS if staged, so reflink is instant)
	// Acquire shared lock to prevent rootfs refresh from replacing the base image mid-copy
	if o.cfg.RootfsLock != nil {
		unlock, lockErr := o.cfg.RootfsLock.SharedLock()
		if lockErr != nil {
			if tapErr := o.bridge.RemoveTap(tapName); tapErr != nil {
				slog.Warn("vm.cleanup.tap_remove_failed", "machine_id", cfg.MachineID, "tap", tapName, "error", tapErr)
			}
			return fmt.Errorf("acquire rootfs shared lock: %w", lockErr)
		}
		defer unlock()
	}
	baseRootfs, rootfsVersion, err := o.resolveMachineRootfsSource(ctx, cfg.RuntimeSelection)
	if err != nil {
		if tapErr := o.bridge.RemoveTap(tapName); tapErr != nil {
			slog.Warn("vm.cleanup.tap_remove_failed", "machine_id", cfg.MachineID, "tap", tapName, "error", tapErr)
		}
		return fmt.Errorf("resolve boot rootfs version: %w", err)
	}
	vmRootfs := filepath.Join(o.cfg.StateDir, cfg.MachineID+".ext4")
	reflinkStart := time.Now()
	copyMode, err := copyRootfs(baseRootfs, vmRootfs, false)
	if err != nil {
		if tapErr := o.bridge.RemoveTap(tapName); tapErr != nil {
			slog.Warn("vm.cleanup.tap_remove_failed", "machine_id", cfg.MachineID, "tap", tapName, "error", tapErr)
		}
		return fmt.Errorf("copy rootfs: %w", err)
	}
	if copyMode != rootfsCopyModeReflink {
		slog.Warn("vm.rootfs.full_copy",
			"machine_id", cfg.MachineID,
			"src", baseRootfs,
			"dst", vmRootfs,
			"duration_ms", time.Since(reflinkStart).Milliseconds())
	}
	slog.Info("vm.rootfs.copied",
		"machine_id", cfg.MachineID,
		"copy_mode", copyMode,
		"duration_ms", time.Since(reflinkStart).Milliseconds())

	// Create or reuse persistent data volume
	var dataVolPath string
	if o.cfg.DataDir != "" {
		sizeGB := cfg.DataVolumeGB
		if sizeGB <= 0 {
			sizeGB = 5 // default 5GB
		}
		var err error
		dataVolPath, err = o.ensureDataVolume(cfg.MachineID, sizeGB, cfg.DataVersion)
		if err != nil {
			if tapErr := o.bridge.RemoveTap(tapName); tapErr != nil {
				slog.Warn("vm.cleanup.tap_remove_failed", "machine_id", cfg.MachineID, "tap", tapName, "error", tapErr)
			}
			_ = os.Remove(vmRootfs)
			return fmt.Errorf("ensure data volume: %w", err)
		}
	}

	machineKind := runtimeMachineKind(cfg.MachineKind, cfg.RuntimeSelection)
	if machineKind == "hermes" {
		if err := o.ensureHermesRuntime(ctx, cfg.RuntimeSelection); err != nil {
			if tapErr := o.bridge.RemoveTap(tapName); tapErr != nil {
				slog.Warn("vm.cleanup.tap_remove_failed", "machine_id", cfg.MachineID, "tap", tapName, "error", tapErr)
			}
			_ = os.Remove(vmRootfs)
			return fmt.Errorf("ensure hermes runtime: %w", err)
		}
	} else if err := o.ensureOpenClawRuntime(ctx, cfg.RuntimeSelection); err != nil {
		if tapErr := o.bridge.RemoveTap(tapName); tapErr != nil {
			slog.Warn("vm.cleanup.tap_remove_failed", "machine_id", cfg.MachineID, "tap", tapName, "error", tapErr)
		}
		_ = os.Remove(vmRootfs)
		return fmt.Errorf("ensure openclaw runtime: %w", err)
	}

	// Create the runtime ext4 drive. OpenClaw uses a read-only shared release
	// image; Hermes uses a writable per-machine image because `hermes update`
	// mutates its checkout and virtualenv in place.
	var runtimeImgPath, runtimeDriveID string
	if cfg.RuntimeSelection != nil {
		if machineKind == "hermes" {
			version := strings.TrimSpace(cfg.RuntimeSelection.ResolvedHermesVersion)
			if version != "" {
				imgPath, err := o.ensureHermesMachineRuntimeImage(cfg.MachineID, version)
				if err != nil {
					if tapErr := o.bridge.RemoveTap(tapName); tapErr != nil {
						slog.Warn("vm.cleanup.tap_remove_failed", "machine_id", cfg.MachineID, "tap", tapName, "error", tapErr)
					}
					_ = os.Remove(vmRootfs)
					return fmt.Errorf("ensure hermes machine runtime image: %w", err)
				}
				runtimeImgPath = imgPath
				runtimeDriveID = "hermes"
			}
		} else {
			version := strings.TrimSpace(cfg.RuntimeSelection.ResolvedOpenClawVersion)
			if version != "" {
				imgPath, err := o.ensureOpenClawReleaseImage(version)
				if err != nil {
					if tapErr := o.bridge.RemoveTap(tapName); tapErr != nil {
						slog.Warn("vm.cleanup.tap_remove_failed", "machine_id", cfg.MachineID, "tap", tapName, "error", tapErr)
					}
					_ = os.Remove(vmRootfs)
					return fmt.Errorf("ensure openclaw release image: %w", err)
				}
				runtimeImgPath = imgPath
				runtimeDriveID = "openclaw"
			}
		}
	}

	if machineKind != "hermes" {
		if err := o.syncHostOpenClawRuntimeState(cfg.MachineID, cfg.RuntimeSelection); err != nil {
			if tapErr := o.bridge.RemoveTap(tapName); tapErr != nil {
				slog.Warn("vm.cleanup.tap_remove_failed", "machine_id", cfg.MachineID, "tap", tapName, "error", tapErr)
			}
			_ = os.Remove(vmRootfs)
			return fmt.Errorf("sync host openclaw runtime state: %w", err)
		}
	}

	// Write config files to data volume before boot (eliminates HTTP round-trips).
	// This also persists/loads the metadata nonce on the data volume.
	if dataVolPath != "" {
		nonce, err := o.writeDataVolumeConfig(cfg.MachineID, dataVolPath, &cfg)
		if err != nil {
			slog.Warn("vm.datavol_config.failed", "machine_id", cfg.MachineID, "error", err)
			// Non-fatal: VM can still fetch config via metadata server at boot
		} else if nonce != "" {
			cfg.MetadataNonce = nonce
		}
	}

	// Socket path
	socketPath := filepath.Join(o.cfg.SocketDir, cfg.MachineID+".sock")

	// Remove stale socket if exists
	_ = os.Remove(socketPath)

	// Generate a per-VM metadata nonce if not already set (fallback if data volume write skipped).
	if cfg.MetadataNonce == "" {
		nonce, err := generateNonce(16) // 16 bytes = 32 hex chars
		if err != nil {
			if tapErr := o.bridge.RemoveTap(tapName); tapErr != nil {
				slog.Warn("vm.cleanup.tap_remove_failed", "machine_id", cfg.MachineID, "tap", tapName, "error", tapErr)
			}
			_ = os.Remove(vmRootfs)
			return fmt.Errorf("generate metadata nonce: %w", err)
		}
		cfg.MetadataNonce = nonce
	}

	metaCfg := persistedMetadataConfigFromVMConfig(cfg)

	// Register with metadata server before boot
	if o.registrar != nil {
		o.registrar.RegisterMachine(cfg.VMIP, metaCfg.toMachineConfig())
	}

	// Configure Firecracker
	vcpus := int64(cfg.VCPUs)
	memMB := int64(cfg.MemoryMB)
	smt := false

	// Build kernel args, including the metadata nonce so the VM init script
	// can authenticate with the metadata server.
	kernelArgs := fmt.Sprintf(
		"console=ttyS0 reboot=k panic=1 pci=off init=/sbin/overlay-init metadata_nonce=%s",
		cfg.MetadataNonce,
	)
	if cfg.KernelExtraArgs != "" {
		kernelArgs += " " + cfg.KernelExtraArgs
	}

	fcCfg := firecracker.Config{
		SocketPath:      socketPath,
		KernelImagePath: o.cfg.KernelPath,
		KernelArgs:      kernelArgs,
		MachineCfg: models.MachineConfiguration{
			VcpuCount:  &vcpus,
			MemSizeMib: &memMB,
			Smt:        &smt,
		},
		Drives: func() []models.Drive {
			drives := []models.Drive{
				{
					DriveID:      firecracker.String("rootfs"),
					PathOnHost:   firecracker.String(vmRootfs),
					IsRootDevice: firecracker.Bool(true),
					IsReadOnly:   firecracker.Bool(false),
				},
			}
			if dataVolPath != "" {
				drives = append(drives, models.Drive{
					DriveID:      firecracker.String("data"),
					PathOnHost:   firecracker.String(dataVolPath),
					IsRootDevice: firecracker.Bool(false),
					IsReadOnly:   firecracker.Bool(false),
				})
			}
			if runtimeImgPath != "" {
				runtimeReadOnly := machineKind != "hermes"
				drives = append(drives, models.Drive{
					DriveID:      firecracker.String(runtimeDriveID),
					PathOnHost:   firecracker.String(runtimeImgPath),
					IsRootDevice: firecracker.Bool(false),
					IsReadOnly:   firecracker.Bool(runtimeReadOnly),
				})
			}
			return drives
		}(),
		NetworkInterfaces: []firecracker.NetworkInterface{
			{
				StaticConfiguration: &firecracker.StaticNetworkConfiguration{
					MacAddress:  generateMAC(cfg.VMIP),
					HostDevName: tapName,
					IPConfiguration: &firecracker.IPConfiguration{
						IPAddr: net.IPNet{
							IP:   net.ParseIP(cfg.VMIP),
							Mask: net.CIDRMask(24, 32),
						},
						Gateway:     net.ParseIP(o.bridge.Gateway),
						Nameservers: []string{"8.8.8.8", "8.8.4.4"},
					},
				},
			},
		},
	}

	bootStart := time.Now()
	owner := o.owner()
	machine, pid, vmCancel, err := owner.start(ctx, cfg.MachineID, false, socketPath, fcCfg, vmConsoleWriter(cfg.MachineID), vmConsoleWriter(cfg.MachineID))
	if err != nil {
		o.cleanup(cfg.MachineID, tapName, vmRootfs, socketPath, cfg.VMIP)
		return err
	}
	slog.Info("vm.boot.completed",
		"machine_id", cfg.MachineID,
		"duration_ms", time.Since(bootStart).Milliseconds())
	_, openClawVersion := versionFieldsFromRuntimeSelection(cfg.RuntimeSelection)

	vm := &runningVM{
		instance: VMInstance{
			MachineID:        cfg.MachineID,
			MachineSlug:      cfg.MachineSlug,
			VMIP:             cfg.VMIP,
			TapDevice:        tapName,
			SocketPath:       socketPath,
			RootfsPath:       vmRootfs,
			RootfsVersion:    rootfsVersion,
			OpenClawVersion:  openClawVersion,
			PID:              pid,
			VCPUs:            cfg.VCPUs,
			MemoryMB:         cfg.MemoryMB,
			ProxyToken:       cfg.ProxyToken,
			GatewayToken:     cfg.GatewayToken,
			SigningKey:       cfg.SigningKey,
			RuntimeOwnerKind: owner.kind(),
			RuntimeOwnerRef:  owner.ownerRef(cfg.MachineID, false),
			Status:           "starting",
			DataVolumePath:   dataVolPath,
			CreatedAt:        time.Now(),
		},
		machine: machine,
		cancel:  vmCancel,
		metaCfg: metaCfg,
	}

	o.mu.Lock()
	o.vms[cfg.MachineID] = vm
	createSucceeded = true
	o.mu.Unlock()

	o.saveState()

	// Wait for VM to become healthy (port 7681 = ttyd terminal server)
	// Note: ttyd binds to 0.0.0.0, while clawdbot gateway binds to localhost
	go func() {
		healthStart := time.Now()
		if err := waitForPort(cfg.VMIP, 7681, 5*time.Minute); err != nil {
			slog.Error("vm.health.failed",
				"machine_id", cfg.MachineID,
				"duration_ms", time.Since(healthStart).Milliseconds(),
				"error", err)
			vm.instance.Status = "error"
			return
		}
		slog.Info("vm.health.ok",
			"machine_id", cfg.MachineID,
			"duration_ms", time.Since(healthStart).Milliseconds())
		vm.instance.Status = "running"
		if cfg.DataVersion > 0 {
			if err := o.writeVersionSidecar(cfg.MachineID, cfg.DataVersion); err != nil {
				slog.Warn("vm.version_sidecar.write_failed", "machine_id", cfg.MachineID, "error", err)
			}
		}
		o.saveState()
	}()

	slog.Info("vm.started",
		"machine_id", cfg.MachineID,
		"pid", pid,
		"vm_ip", cfg.VMIP,
		"tap_name", tapName)

	return nil
}

func (o *firecrackerOrchestrator) Upgrade(ctx context.Context, cfg VMConfig) error {
	previousCfg, previousDataVersion, err := o.vmConfigForMachine(cfg.MachineID)
	if err != nil {
		return err
	}

	slog.Info("vm.upgrade.start",
		"machine_id", cfg.MachineID,
		"current_rootfs_version", resolvedRootfsVersion(previousCfg.RuntimeSelection),
		"target_rootfs_version", resolvedRootfsVersion(cfg.RuntimeSelection),
		"current_openclaw_version", resolvedOpenClawVersion(previousCfg.RuntimeSelection),
		"target_openclaw_version", resolvedOpenClawVersion(cfg.RuntimeSelection),
	)

	if err := o.Stop(ctx, cfg.MachineID); err != nil {
		return fmt.Errorf("stop existing VM: %w", err)
	}

	if err := o.Create(ctx, cfg); err != nil {
		slog.Error("vm.upgrade.create_failed", "machine_id", cfg.MachineID, "error", err)

		if cfg.DataVersion > previousDataVersion {
			if rollbackErr := o.RollbackDataVolume(cfg.MachineID); rollbackErr != nil {
				slog.Warn("vm.upgrade.data_volume_rollback_failed", "machine_id", cfg.MachineID, "error", rollbackErr)
			} else {
				slog.Info("vm.upgrade.data_volume_rolled_back", "machine_id", cfg.MachineID, "from_version", cfg.DataVersion, "to_version", previousDataVersion)
			}
		}

		restoreErr := o.Create(ctx, previousCfg)
		if restoreErr != nil {
			return fmt.Errorf("upgrade recreate failed: %w; restore previous VM failed: %v", err, restoreErr)
		}
		return fmt.Errorf("%w: recreate failed: %v", ErrUpgradeRestoredPreviousVM, err)
	}

	slog.Info("vm.upgrade.completed", "machine_id", cfg.MachineID)
	return nil
}

func (o *firecrackerOrchestrator) Destroy(ctx context.Context, machineID string) error {
	o.mu.Lock()
	vm, exists := o.vms[machineID]
	if !exists {
		o.mu.Unlock()
		return fmt.Errorf("VM %s not found: %w", machineID, ErrVMNotFound)
	}
	o.mu.Unlock()

	slog.Info("vm.destroying", "machine_id", machineID)
	destroyStart := time.Now()

	if vm.instance.Status == "error" && vm.instance.PID == 0 && vm.instance.RuntimeOwnerRef == "" && vm.machine == nil {
		o.mu.Lock()
		delete(o.vms, machineID)
		o.mu.Unlock()
		o.cleanupEphemeralState(machineID, vm.instance.TapDevice, vm.instance.RootfsPath, vm.instance.SocketPath, vm.instance.VMIP)
		o.saveState()
		slog.Info("vm.destroy.error_state_removed",
			"machine_id", machineID,
			"duration_ms", time.Since(destroyStart).Milliseconds(),
			"data_volume_preserved", o.dataVolumePath(machineID))
		return nil
	}

	if err := o.stopOwnedVM(ctx, vm.instance.RuntimeOwnerRef, vm.machine, vm.cancel, vm.instance.PID, 10*time.Second); err != nil {
		slog.Warn("vm.shutdown.owner_stop.failed", "machine_id", machineID, "pid", vm.instance.PID, "error", err)
		return err
	}

	o.mu.Lock()
	delete(o.vms, machineID)
	o.mu.Unlock()

	// Clean up all resources including data volume
	o.cleanup(machineID, vm.instance.TapDevice, vm.instance.RootfsPath, vm.instance.SocketPath, vm.instance.VMIP)

	// Delete persistent data volume (full destroy)
	dataVolPath := o.dataVolumePath(machineID)
	if err := os.Remove(dataVolPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("vm.data_volume.delete.failed", "machine_id", machineID, "error", err)
	}

	// Delete version sidecar and pre-upgrade backup
	if path := o.versionSidecarPath(machineID); path != "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Warn("vm.cleanup.version_sidecar_remove_failed", "machine_id", machineID, "path", path, "error", err)
		}
	}
	if err := os.Remove(dataVolPath + ".pre-upgrade"); err != nil && !os.IsNotExist(err) {
		slog.Warn("vm.cleanup.pre_upgrade_remove_failed", "machine_id", machineID, "path", dataVolPath+".pre-upgrade", "error", err)
	}

	o.saveState()

	slog.Info("vm.destroyed",
		"machine_id", machineID,
		"duration_ms", time.Since(destroyStart).Milliseconds())
	return nil
}

func (o *firecrackerOrchestrator) vmConfigForMachine(machineID string) (VMConfig, int, error) {
	o.mu.RLock()
	vm, exists := o.vms[machineID]
	if !exists {
		o.mu.RUnlock()
		return VMConfig{}, 0, fmt.Errorf("VM %s not found: %w", machineID, ErrVMNotFound)
	}

	inst := vm.instance
	metaCfg := vm.metaCfg
	o.mu.RUnlock()

	dataVersion := o.readVersionSidecar(machineID)

	return VMConfig{
		MachineID:        inst.MachineID,
		MachineName:      metaCfg.MachineName,
		MachineSlug:      metaCfg.MachineSlug,
		VCPUs:            inst.VCPUs,
		MemoryMB:         inst.MemoryMB,
		VMIP:             inst.VMIP,
		GatewayToken:     metaCfg.GatewayToken,
		ProxyToken:       inst.ProxyToken,
		MetadataNonce:    metaCfg.Nonce,
		OpenClawConf:     cloneBytes(metaCfg.OpenClawConf),
		LLMEndpoint:      metaCfg.LLMEndpoint,
		Secrets:          cloneStringMap(metaCfg.Secrets),
		LLMKeys:          cloneCredentialMap(metaCfg.LLMKeys),
		AccountID:        metaCfg.AccountID,
		BudgetMicrocents: cloneInt64Ptr(metaCfg.BudgetMicrocents),
		DataVolumeGB:     dataVolumeSizeGB(inst.DataVolumePath),
		DataVersion:      dataVersion,
		SigningKey:       metaCfg.SigningKey,
		TunnelToken:      metaCfg.TunnelToken,
		VmHostname:       metaCfg.VmHostname,
		OwnerEmails:      cloneStringSlice(metaCfg.OwnerEmails),
		CfCaPubKey:       metaCfg.CfCaPubKey,
		CustomProviders:  cloneCustomProviders(metaCfg.CustomProviders),
		Souls:            cloneSoulEntries(metaCfg.Souls),
		RuntimeSelection: cloneRuntimeSelection(metaCfg.RuntimeSelection),
	}, dataVersion, nil
}

func dataVolumeSizeGB(path string) int {
	if strings.TrimSpace(path) == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() <= 0 {
		return 0
	}
	const gib = int64(1024 * 1024 * 1024)
	return int((info.Size() + gib - 1) / gib)
}

func resolvedRootfsVersion(selection *metadata.RuntimeSelection) string {
	if selection == nil {
		return ""
	}
	return strings.TrimSpace(selection.ResolvedRootfsVersion)
}

func resolvedOpenClawVersion(selection *metadata.RuntimeSelection) string {
	if selection == nil {
		return ""
	}
	return strings.TrimSpace(selection.ResolvedOpenClawVersion)
}

func runtimeMachineKind(configKind string, selection *metadata.RuntimeSelection) string {
	if strings.TrimSpace(configKind) != "" {
		return strings.TrimSpace(configKind)
	}
	if selection != nil && strings.TrimSpace(selection.Kind) != "" {
		return strings.TrimSpace(selection.Kind)
	}
	return "openclaw"
}

func (o *firecrackerOrchestrator) Stop(ctx context.Context, machineID string) error {
	o.mu.Lock()
	vm, exists := o.vms[machineID]
	if !exists {
		o.mu.Unlock()
		return fmt.Errorf("VM %s not found: %w", machineID, ErrVMNotFound)
	}
	o.mu.Unlock()

	slog.Info("vm.stopping", "machine_id", machineID)
	stopStart := time.Now()

	if err := o.stopOwnedVM(ctx, vm.instance.RuntimeOwnerRef, vm.machine, vm.cancel, vm.instance.PID, 10*time.Second); err != nil {
		slog.Warn("vm.shutdown.owner_stop.failed", "machine_id", machineID, "pid", vm.instance.PID, "error", err)
		return err
	}

	o.mu.Lock()
	delete(o.vms, machineID)
	o.mu.Unlock()

	// Clean up ephemeral resources but preserve data volume
	o.cleanupEphemeral(machineID, vm.instance.TapDevice, vm.instance.RootfsPath, vm.instance.SocketPath, vm.instance.VMIP)

	o.saveState()

	slog.Info("vm.stopped",
		"machine_id", machineID,
		"duration_ms", time.Since(stopStart).Milliseconds(),
		"data_volume_preserved", vm.instance.DataVolumePath)
	return nil
}

func (o *firecrackerOrchestrator) cleanupEphemeralState(machineID, tapDevice, rootfsPath, socketPath, vmIP string) {
	if vmIP != "" && o.registrar != nil {
		o.registrar.UnregisterMachine(vmIP)
	}
	if tapDevice != "" {
		if err := o.bridge.RemoveTap(tapDevice); err != nil {
			slog.Warn("vm.cleanup.tap_remove_failed", "machine_id", machineID, "tap", tapDevice, "error", err)
		}
	}
	if rootfsPath != "" {
		if err := os.Remove(rootfsPath); err != nil && !os.IsNotExist(err) {
			slog.Warn("vm.cleanup.rootfs_remove_failed", "machine_id", machineID, "path", rootfsPath, "error", err)
		}
	}
	if socketPath != "" {
		if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
			slog.Warn("vm.cleanup.socket_remove_failed", "machine_id", machineID, "path", socketPath, "error", err)
		}
	}
}

// cleanupEphemeral removes TAP device, rootfs copy, socket, and metadata registration
// but preserves the data volume for restart.
func (o *firecrackerOrchestrator) cleanupEphemeral(machineID, tapDevice, rootfsPath, socketPath, vmIP string) {
	if o.registrar != nil {
		o.registrar.UnregisterMachine(vmIP)
	}
	if err := o.bridge.RemoveTap(tapDevice); err != nil {
		slog.Warn("vm.cleanup.tap_remove_failed", "machine_id", machineID, "tap", tapDevice, "error", err)
	}
	if err := os.Remove(rootfsPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("vm.cleanup.rootfs_remove_failed", "machine_id", machineID, "path", rootfsPath, "error", err)
	}
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("vm.cleanup.socket_remove_failed", "machine_id", machineID, "path", socketPath, "error", err)
	}
}

func (o *firecrackerOrchestrator) Get(ctx context.Context, machineID string) (*VMInstance, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	vm, exists := o.vms[machineID]
	if !exists {
		return nil, fmt.Errorf("VM %s not found: %w", machineID, ErrVMNotFound)
	}

	inst := vm.instance // copy
	return &inst, nil
}

func (o *firecrackerOrchestrator) List(ctx context.Context) ([]VMInstance, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	result := make([]VMInstance, 0, len(o.vms))
	for _, vm := range o.vms {
		result = append(result, vm.instance)
	}
	return result, nil
}

func (o *firecrackerOrchestrator) UpdateSecrets(ctx context.Context, machineID string, secrets map[string]string) error {
	o.mu.Lock()
	vm, exists := o.vms[machineID]
	if exists {
		vm.metaCfg.Secrets = cloneStringMap(secrets)
	}
	o.mu.Unlock()

	if !exists {
		return fmt.Errorf("VM %s not found: %w", machineID, ErrVMNotFound)
	}

	if o.registrar == nil {
		return fmt.Errorf("no metadata registrar configured")
	}

	o.registrar.UpdateMachineSecrets(vm.instance.VMIP, secrets)
	o.saveState()
	slog.Info("vm.secrets.updated", "machine_id", machineID)
	return nil
}

func (o *firecrackerOrchestrator) UpdateConfig(ctx context.Context, machineID string, openClawConf []byte) error {
	o.mu.Lock()
	vm, exists := o.vms[machineID]
	if exists {
		vm.metaCfg.OpenClawConf = cloneBytes(openClawConf)
	}
	o.mu.Unlock()

	if !exists {
		return fmt.Errorf("VM %s not found: %w", machineID, ErrVMNotFound)
	}

	if o.registrar == nil {
		return fmt.Errorf("no metadata registrar configured")
	}

	o.registrar.UpdateMachineConfig(vm.instance.VMIP, openClawConf)
	o.saveState()
	slog.Info("vm.config.updated", "machine_id", machineID)
	return nil
}

func (o *firecrackerOrchestrator) UpdateLLMKeys(ctx context.Context, machineID string, llmKeys map[string]metadata.CredentialEntry) error {
	o.mu.Lock()
	vm, exists := o.vms[machineID]
	if exists {
		if vm.metaCfg.LLMKeys == nil {
			vm.metaCfg.LLMKeys = make(map[string]metadata.CredentialEntry)
		}
		for k, v := range llmKeys {
			vm.metaCfg.LLMKeys[k] = cloneCredentialEntry(v)
		}
	}
	o.mu.Unlock()

	if !exists {
		return fmt.Errorf("VM %s not found: %w", machineID, ErrVMNotFound)
	}

	if o.registrar == nil {
		return fmt.Errorf("no metadata registrar configured")
	}

	o.registrar.UpdateMachineLLMKeys(vm.instance.VMIP, llmKeys)
	o.saveState()
	slog.Info("vm.llmkeys.updated", "machine_id", machineID)
	return nil
}

func (o *firecrackerOrchestrator) ReplaceLLMKeys(ctx context.Context, machineID string, llmKeys map[string]metadata.CredentialEntry) error {
	// Validate preconditions BEFORE mutating in-memory state — otherwise a
	// missing registrar leaves vm.metaCfg.LLMKeys truncated while the
	// metadata server keeps its old map.
	if o.registrar == nil {
		return fmt.Errorf("no metadata registrar configured")
	}

	o.mu.Lock()
	vm, exists := o.vms[machineID]
	if exists {
		vm.metaCfg.LLMKeys = make(map[string]metadata.CredentialEntry, len(llmKeys))
		for k, v := range llmKeys {
			vm.metaCfg.LLMKeys[k] = cloneCredentialEntry(v)
		}
	}
	o.mu.Unlock()

	if !exists {
		slog.Warn("vm.llmkeys.replace.not_found", "machine_id", machineID)
		return fmt.Errorf("VM %s not found: %w", machineID, ErrVMNotFound)
	}

	o.registrar.ReplaceMachineLLMKeys(vm.instance.VMIP, llmKeys)
	o.saveState()
	slog.Info("vm.llmkeys.replaced", "machine_id", machineID, "key_count", len(llmKeys))
	return nil
}

// RegisterPending pre-registers a VM in "creating" state before async creation starts.
// This allows GetVM to return status during creation and capture errors.
func (o *firecrackerOrchestrator) RegisterPending(cfg VMConfig) {
	rootfsVersion, openClawVersion := versionFieldsFromRuntimeSelection(cfg.RuntimeSelection)
	o.mu.Lock()
	o.vms[cfg.MachineID] = &runningVM{
		instance: VMInstance{
			MachineID:       cfg.MachineID,
			MachineSlug:     cfg.MachineSlug,
			VMIP:            cfg.VMIP,
			RootfsVersion:   rootfsVersion,
			OpenClawVersion: openClawVersion,
			VCPUs:           cfg.VCPUs,
			MemoryMB:        cfg.MemoryMB,
			ProxyToken:      cfg.ProxyToken,
			GatewayToken:    cfg.GatewayToken,
			SigningKey:      cfg.SigningKey,
			Status:          "creating",
			CreatedAt:       time.Now(),
		},
		metaCfg: persistedMetadataConfigFromVMConfig(cfg),
	}
	o.mu.Unlock()
	o.saveState()
}

// SetError marks a VM as failed with an error message.
func (o *firecrackerOrchestrator) SetError(machineID, errMsg string) {
	o.mu.Lock()
	if vm, exists := o.vms[machineID]; exists {
		vm.instance.Status = "error"
		vm.instance.ErrorMessage = errMsg
	} else {
		o.vms[machineID] = &runningVM{
			instance: VMInstance{
				MachineID:    machineID,
				Status:       "error",
				ErrorMessage: errMsg,
				CreatedAt:    time.Now(),
			},
		}
	}
	o.mu.Unlock()
	o.saveState()
}

func (o *firecrackerOrchestrator) Drain(ctx context.Context) error {
	o.mu.Lock()
	ids := make([]string, 0, len(o.vms))
	for id := range o.vms {
		ids = append(ids, id)
	}
	o.mu.Unlock()

	slog.Info("orchestrator.drain", "vm_count", len(ids))

	for _, id := range ids {
		slog.Info("orchestrator.drain.stopping", "machine_id", id)
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := o.Stop(stopCtx, id)
		cancel()
		if err != nil {
			slog.Warn("orchestrator.drain.stop_failed", "machine_id", id, "error", err,
				"note", "VM process will be killed on agent restart")
		} else {
			slog.Info("orchestrator.drain.stopped", "machine_id", id)
		}
	}

	return nil
}

func (o *firecrackerOrchestrator) Shutdown(ctx context.Context) error {
	o.mu.RLock()
	vmCount := len(o.vms)
	o.mu.RUnlock()
	slog.Info("orchestrator.shutdown", "vm_count", vmCount, "behavior", "non_disruptive")
	o.saveState()
	return nil
}

// cleanup removes TAP device, rootfs copy, socket, and metadata registration.
func (o *firecrackerOrchestrator) cleanup(machineID, tapDevice, rootfsPath, socketPath, vmIP string) {
	if o.registrar != nil {
		o.registrar.UnregisterMachine(vmIP)
	}
	if err := o.bridge.RemoveTap(tapDevice); err != nil {
		slog.Warn("vm.cleanup.tap_remove_failed", "machine_id", machineID, "tap", tapDevice, "error", err)
	}
	if err := os.Remove(rootfsPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("vm.cleanup.rootfs_remove_failed", "machine_id", machineID, "path", rootfsPath, "error", err)
	}
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("vm.cleanup.socket_remove_failed", "machine_id", machineID, "path", socketPath, "error", err)
	}
}

type persistedMetadataConfig struct {
	MachineID        string                              `json:"machine_id"`
	MachineKind      string                              `json:"machine_kind,omitempty"`
	MachineName      string                              `json:"machine_name,omitempty"`
	MachineSlug      string                              `json:"machine_slug,omitempty"`
	GatewayToken     string                              `json:"gateway_token"`
	OpenClawConf     []byte                              `json:"openclaw_config,omitempty"`
	HermesConfigYAML []byte                              `json:"hermes_config_yaml,omitempty"`
	HermesEnv        []byte                              `json:"hermes_env,omitempty"`
	HermesAuthJSON   []byte                              `json:"hermes_auth_json,omitempty"`
	HermesSoul       []byte                              `json:"hermes_soul,omitempty"`
	LLMEndpoint      string                              `json:"llm_endpoint,omitempty"`
	Secrets          map[string]string                   `json:"secrets,omitempty"`
	LLMKeys          map[string]metadata.CredentialEntry `json:"llm_keys,omitempty"`
	Nonce            string                              `json:"nonce,omitempty"`
	AccountID        int                                 `json:"account_id,omitempty"`
	BudgetMicrocents *int64                              `json:"budget_microcents,omitempty"`
	SigningKey       string                              `json:"signing_key,omitempty"`
	VmHostname       string                              `json:"vm_hostname,omitempty"`
	TunnelToken      string                              `json:"tunnel_token,omitempty"`
	OwnerEmails      []string                            `json:"owner_emails,omitempty"`
	CfCaPubKey       string                              `json:"cf_ca_pubkey,omitempty"`
	CustomProviders  []metadata.CustomProviderConfig     `json:"custom_providers,omitempty"`
	Souls            []metadata.SoulEntry                `json:"souls,omitempty"`
	RuntimeSelection *metadata.RuntimeSelection          `json:"runtime_selection,omitempty"`
}

func persistedMetadataConfigFromVMConfig(cfg VMConfig) persistedMetadataConfig {
	return persistedMetadataConfig{
		MachineID:        cfg.MachineID,
		MachineKind:      cfg.MachineKind,
		MachineName:      cfg.MachineName,
		MachineSlug:      cfg.MachineSlug,
		GatewayToken:     cfg.GatewayToken,
		OpenClawConf:     cloneBytes(cfg.OpenClawConf),
		HermesConfigYAML: cloneBytes(cfg.HermesConfigYAML),
		HermesEnv:        cloneBytes(cfg.HermesEnv),
		HermesAuthJSON:   cloneBytes(cfg.HermesAuthJSON),
		HermesSoul:       cloneBytes(cfg.HermesSoul),
		LLMEndpoint:      cfg.LLMEndpoint,
		Secrets:          cloneStringMap(cfg.Secrets),
		LLMKeys:          cloneCredentialMap(cfg.LLMKeys),
		Nonce:            cfg.MetadataNonce,
		AccountID:        cfg.AccountID,
		BudgetMicrocents: cloneInt64Ptr(cfg.BudgetMicrocents),
		SigningKey:       cfg.SigningKey,
		VmHostname:       cfg.VmHostname,
		TunnelToken:      cfg.TunnelToken,
		OwnerEmails:      cloneStringSlice(cfg.OwnerEmails),
		CfCaPubKey:       cfg.CfCaPubKey,
		CustomProviders:  cloneCustomProviders(cfg.CustomProviders),
		Souls:            cloneSoulEntries(cfg.Souls),
		RuntimeSelection: cloneRuntimeSelection(cfg.RuntimeSelection),
	}
}

func (p persistedMetadataConfig) toMachineConfig() metadata.MachineConfig {
	return metadata.MachineConfig{
		MachineID:        p.MachineID,
		MachineKind:      p.MachineKind,
		MachineName:      p.MachineName,
		MachineSlug:      p.MachineSlug,
		GatewayToken:     p.GatewayToken,
		OpenClawConf:     cloneBytes(p.OpenClawConf),
		HermesConfigYAML: cloneBytes(p.HermesConfigYAML),
		HermesEnv:        cloneBytes(p.HermesEnv),
		HermesAuthJSON:   cloneBytes(p.HermesAuthJSON),
		HermesSoul:       cloneBytes(p.HermesSoul),
		LLMEndpoint:      p.LLMEndpoint,
		Secrets:          cloneStringMap(p.Secrets),
		LLMKeys:          cloneCredentialMap(p.LLMKeys),
		Nonce:            p.Nonce,
		AccountID:        p.AccountID,
		BudgetMicrocents: cloneInt64Ptr(p.BudgetMicrocents),
		SigningKey:       p.SigningKey,
		VmHostname:       p.VmHostname,
		TunnelToken:      p.TunnelToken,
		OwnerEmails:      cloneStringSlice(p.OwnerEmails),
		CfCaPubKey:       p.CfCaPubKey,
		CustomProviders:  cloneCustomProviders(p.CustomProviders),
		Souls:            cloneSoulEntries(p.Souls),
		RuntimeSelection: cloneRuntimeSelection(p.RuntimeSelection),
	}
}

// persistedVM is the on-disk representation of a VM for state recovery.
type persistedVM struct {
	MachineID        string                  `json:"machine_id"`
	VMIP             string                  `json:"vm_ip"`
	TapDevice        string                  `json:"tap_device"`
	SocketPath       string                  `json:"socket_path"`
	RootfsPath       string                  `json:"rootfs_path"`
	DataVolumePath   string                  `json:"data_volume_path"`
	RootfsVersion    string                  `json:"rootfs_version,omitempty"`
	OpenClawVersion  string                  `json:"openclaw_version,omitempty"`
	PID              int                     `json:"pid"`
	VCPUs            int                     `json:"vcpus"`
	MemoryMB         int                     `json:"memory_mb"`
	ProxyToken       string                  `json:"proxy_token"`
	RuntimeOwnerKind string                  `json:"runtime_owner_kind,omitempty"`
	RuntimeOwnerRef  string                  `json:"runtime_owner_ref,omitempty"`
	CreatedAt        time.Time               `json:"created_at,omitempty"`
	Status           string                  `json:"status,omitempty"`
	MetaCfg          persistedMetadataConfig `json:"metadata_config,omitempty"`
	Quarantined      bool                    `json:"quarantined,omitempty"`
	QuarantineReason string                  `json:"quarantine_reason,omitempty"`
	QuarantinedAt    time.Time               `json:"quarantined_at,omitempty"`
}

func (o *firecrackerOrchestrator) stateFilePath() string {
	return filepath.Join(o.cfg.StateDir, "vms.json")
}

func quarantinePersistedVM(p persistedVM, reason string) persistedVM {
	p.Quarantined = true
	p.QuarantineReason = reason
	p.QuarantinedAt = time.Now().UTC()
	return p
}

func quarantinePersistedBrowserVM(p persistedBrowserVM, reason string) persistedBrowserVM {
	p.Quarantined = true
	p.QuarantineReason = reason
	p.QuarantinedAt = time.Now().UTC()
	return p
}

func (o *firecrackerOrchestrator) saveState() {
	o.mu.RLock()
	persisted := make([]persistedVM, 0, len(o.vms)+len(o.quarantinedVMs))
	for _, vm := range o.vms {
		p := persistedVM{
			MachineID:        vm.instance.MachineID,
			VMIP:             vm.instance.VMIP,
			TapDevice:        vm.instance.TapDevice,
			SocketPath:       vm.instance.SocketPath,
			RootfsPath:       vm.instance.RootfsPath,
			DataVolumePath:   vm.instance.DataVolumePath,
			RootfsVersion:    vm.instance.RootfsVersion,
			OpenClawVersion:  vm.instance.OpenClawVersion,
			PID:              vm.instance.PID,
			VCPUs:            vm.instance.VCPUs,
			MemoryMB:         vm.instance.MemoryMB,
			ProxyToken:       vm.instance.ProxyToken,
			RuntimeOwnerKind: vm.instance.RuntimeOwnerKind,
			RuntimeOwnerRef:  vm.instance.RuntimeOwnerRef,
			CreatedAt:        vm.instance.CreatedAt,
			Status:           vm.instance.Status,
			MetaCfg:          vm.metaCfg,
		}
		persisted = append(persisted, p)
	}
	for machineID, p := range o.quarantinedVMs {
		if _, exists := o.vms[machineID]; exists {
			continue
		}
		persisted = append(persisted, p)
	}
	o.mu.RUnlock()

	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		slog.Error("state.marshal.failed", "error", err, "vm_count", len(persisted),
			"warning", "VM state not persisted — agent restart will orphan running VMs")
		return
	}

	stateFile := o.stateFilePath()
	tmpFile := stateFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		slog.Error("state.save.failed", "error", err, "path", tmpFile,
			"warning", "VM state not persisted — agent restart will orphan running VMs")
		return
	}
	if err := os.Chmod(tmpFile, 0600); err != nil {
		slog.Error("state.save.chmod_failed", "error", err, "path", tmpFile)
		_ = os.Remove(tmpFile)
		return
	}
	if err := os.Rename(tmpFile, stateFile); err != nil {
		slog.Error("state.save.rename_failed", "error", err, "from", tmpFile, "to", stateFile,
			"warning", "VM state not persisted — agent restart will orphan running VMs")
		_ = os.Remove(tmpFile)
	}
}

func (o *firecrackerOrchestrator) loadPersistedState() ([]persistedVM, error) {
	data, err := os.ReadFile(o.stateFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var persisted []persistedVM
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, err
	}
	return persisted, nil
}

func recoverReasonForUnitState(state systemdUnitState) string {
	if state.LoadState == "not-found" {
		return "unit_missing"
	}
	if state.ActiveState != "active" {
		if state.ActiveState == "" {
			return "unit_inactive"
		}
		return "unit_" + state.ActiveState
	}
	if state.MainPID <= 0 {
		return "unit_missing_mainpid"
	}
	return ""
}

func resolveRecoveredPID(ownerKind, ownerRef string, persistedPID int) (pid int, cleanup bool, quarantineReason string, err error) {
	if defaultRuntimeOwnerKind(ownerKind) != runtimeOwnerKindSystemdVML {
		if !pidAlive(persistedPID) {
			return 0, true, "", nil
		}
		return persistedPID, false, "", nil
	}

	if strings.TrimSpace(ownerRef) == "" {
		return 0, false, "unit_ref_missing", nil
	}

	state, err := systemdUnitStateReader(ownerRef)
	if err != nil {
		return 0, false, "unit_state_unavailable", err
	}

	if state.ActiveState == "active" && state.MainPID > 0 {
		return state.MainPID, false, "", nil
	}

	if state.MainPID <= 0 && !pidAlive(persistedPID) {
		return 0, true, "", nil
	}

	return 0, false, recoverReasonForUnitState(state), nil
}

func (o *firecrackerOrchestrator) systemdUnitInventory() (map[string]systemdUnitState, error) {
	units, err := systemdUnitLister("ocm-vm-*.service", "ocm-browser-vm-*.service")
	if err != nil {
		return nil, err
	}
	inventory := make(map[string]systemdUnitState, len(units))
	for _, unit := range units {
		if strings.TrimSpace(unit.Unit) == "" {
			continue
		}
		inventory[unit.Unit] = unit
	}
	return inventory, nil
}

func (o *firecrackerOrchestrator) inferredRuntimeOwnerRef(machineID string, browser bool, ownerKind, ownerRef string, inventory map[string]systemdUnitState) string {
	if defaultRuntimeOwnerKind(ownerKind) != runtimeOwnerKindSystemdVML {
		return ownerRef
	}
	if strings.TrimSpace(ownerRef) != "" {
		return ownerRef
	}
	candidate := machineSystemdUnitName(machineID)
	if browser {
		candidate = browserSystemdUnitName(machineID)
	}
	// nil inventory = lookup failed; fall back to the inferred name and let
	// the subsequent state read decide. A non-nil (even empty) inventory is
	// authoritative — if the candidate isn't in it, leave the ref blank so
	// recovery quarantines with "unit_ref_missing" instead of synthesizing a
	// name that will then fail as "unit_missing".
	if inventory == nil {
		return candidate
	}
	if _, ok := inventory[candidate]; ok {
		return candidate
	}
	return ""
}

func logOrphanedSystemdUnits(inventory map[string]systemdUnitState, known map[string]bool) {
	for unit, state := range inventory {
		if known[unit] {
			continue
		}
		slog.Warn("vm.recover.systemd_unit_without_persisted_state",
			"unit", unit,
			"load_state", state.LoadState,
			"active_state", state.ActiveState,
			"sub_state", state.SubState)
	}
}

func (o *firecrackerOrchestrator) Recover(ctx context.Context) error {
	persisted, err := o.loadPersistedState()
	if err != nil {
		return fmt.Errorf("load persisted state: %w", err)
	}
	unitInventory, unitInventoryErr := o.systemdUnitInventory()
	if unitInventoryErr != nil {
		slog.Warn("vm.recover.systemd_inventory_failed", "error", unitInventoryErr)
		unitInventory = nil
	}
	o.mu.Lock()
	o.quarantinedVMs = make(map[string]persistedVM)
	o.mu.Unlock()
	if len(persisted) > 0 {
		slog.Info("state.loaded", "persisted_vm_count", len(persisted))
	}

	recovered := 0
	knownUnits := make(map[string]bool)
	for _, p := range persisted {
		p.RuntimeOwnerRef = o.inferredRuntimeOwnerRef(p.MachineID, false, p.RuntimeOwnerKind, p.RuntimeOwnerRef, unitInventory)
		if p.RuntimeOwnerRef != "" {
			knownUnits[p.RuntimeOwnerRef] = true
		}
		recoveredPID, shouldCleanup, quarantineReason, resolveErr := resolveRecoveredPID(p.RuntimeOwnerKind, p.RuntimeOwnerRef, p.PID)
		if resolveErr != nil {
			slog.Warn("vm.recover.owner_state_failed",
				"machine_id", p.MachineID,
				"runtime_owner_kind", defaultRuntimeOwnerKind(p.RuntimeOwnerKind),
				"runtime_owner_ref", p.RuntimeOwnerRef,
				"error", resolveErr)
		}
		if shouldCleanup {
			o.mu.Lock()
			delete(o.quarantinedVMs, p.MachineID)
			o.mu.Unlock()
			o.cleanupPersistedVM(p, false)
			continue
		}
		if quarantineReason != "" {
			slog.Warn("vm.recover.owner_state_unresolved",
				"machine_id", p.MachineID,
				"runtime_owner_kind", defaultRuntimeOwnerKind(p.RuntimeOwnerKind),
				"runtime_owner_ref", p.RuntimeOwnerRef,
				"reason", quarantineReason)
			o.mu.Lock()
			o.quarantinedVMs[p.MachineID] = quarantinePersistedVM(p, quarantineReason)
			o.mu.Unlock()
			continue
		}
		p.PID = recoveredPID
		if !firecrackerPIDVerifier(p.PID) {
			slog.Warn("vm.recover.pid_unexpected_process", "machine_id", p.MachineID, "pid", p.PID)
			o.mu.Lock()
			o.quarantinedVMs[p.MachineID] = quarantinePersistedVM(p, "pid_not_firecracker")
			o.mu.Unlock()
			continue
		}
		if _, err := os.Stat(p.SocketPath); err != nil {
			slog.Warn("vm.recover.socket_missing", "machine_id", p.MachineID, "path", p.SocketPath, "error", err)
			o.mu.Lock()
			o.quarantinedVMs[p.MachineID] = quarantinePersistedVM(p, "socket_missing")
			o.mu.Unlock()
			continue
		}
		if p.MetaCfg.MachineID == "" {
			slog.Warn("vm.recover.metadata_missing", "machine_id", p.MachineID,
				"note", "persisted state predates metadata recovery support; falling back to cleanup")
			o.mu.Lock()
			o.quarantinedVMs[p.MachineID] = quarantinePersistedVM(p, "metadata_missing")
			o.mu.Unlock()
			continue
		}

		machine, err := firecracker.NewMachine(ctx, firecracker.Config{
			SocketPath: p.SocketPath,
			VMID:       p.MachineID,
		})
		if err != nil {
			slog.Warn("vm.recover.machine_failed", "machine_id", p.MachineID, "error", err)
			o.mu.Lock()
			o.quarantinedVMs[p.MachineID] = quarantinePersistedVM(p, "reattach_failed")
			o.mu.Unlock()
			continue
		}
		verifyCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		reattachedID, verifyErr := firecrackerMachineIDVerifier(verifyCtx, machine)
		cancel()
		if verifyErr != nil {
			slog.Warn("vm.recover.identity_failed", "machine_id", p.MachineID, "pid", p.PID, "error", verifyErr)
			o.mu.Lock()
			o.quarantinedVMs[p.MachineID] = quarantinePersistedVM(p, "identity_check_failed")
			o.mu.Unlock()
			continue
		}
		if reattachedID != p.MachineID {
			slog.Warn("vm.recover.identity_mismatch", "machine_id", p.MachineID, "reattached_id", reattachedID, "pid", p.PID)
			o.mu.Lock()
			o.quarantinedVMs[p.MachineID] = quarantinePersistedVM(p, "identity_mismatch")
			o.mu.Unlock()
			continue
		}
		rootfsVersion := strings.TrimSpace(p.RootfsVersion)
		openClawVersion := strings.TrimSpace(p.OpenClawVersion)
		if rootfsVersion == "" || openClawVersion == "" {
			resolvedRootfs, resolvedOpenClaw := versionFieldsFromRuntimeSelection(p.MetaCfg.RuntimeSelection)
			if rootfsVersion == "" {
				rootfsVersion = resolvedRootfs
			}
			if openClawVersion == "" {
				openClawVersion = resolvedOpenClaw
			}
		}

		// Use persisted status; default to "running" for old state files that
		// predate the Status field.
		recoveredStatus := p.Status
		if recoveredStatus == "" {
			recoveredStatus = "running"
		}

		vm := &runningVM{
			instance: VMInstance{
				MachineID:        p.MachineID,
				MachineSlug:      p.MetaCfg.MachineSlug,
				VMIP:             p.VMIP,
				TapDevice:        p.TapDevice,
				SocketPath:       p.SocketPath,
				RootfsPath:       p.RootfsPath,
				RootfsVersion:    rootfsVersion,
				OpenClawVersion:  openClawVersion,
				PID:              p.PID,
				VCPUs:            p.VCPUs,
				MemoryMB:         p.MemoryMB,
				ProxyToken:       p.ProxyToken,
				GatewayToken:     p.MetaCfg.GatewayToken,
				SigningKey:       p.MetaCfg.SigningKey,
				RuntimeOwnerKind: defaultRuntimeOwnerKind(p.RuntimeOwnerKind),
				RuntimeOwnerRef:  p.RuntimeOwnerRef,
				Status:           recoveredStatus,
				DataVolumePath:   p.DataVolumePath,
				CreatedAt:        p.CreatedAt,
			},
			machine: machine,
			cancel:  func() {},
			metaCfg: p.MetaCfg,
		}
		if vm.instance.CreatedAt.IsZero() {
			vm.instance.CreatedAt = time.Now()
		}

		if o.registrar != nil {
			o.registrar.RegisterMachine(p.VMIP, p.MetaCfg.toMachineConfig())
		}

		o.mu.Lock()
		o.vms[p.MachineID] = vm
		delete(o.quarantinedVMs, p.MachineID)
		o.mu.Unlock()
		recovered++
		slog.Info("vm.recovered", "machine_id", p.MachineID, "pid", p.PID, "vm_ip", p.VMIP)
	}

	browserDNATReplays, err := o.recoverBrowserVMs(ctx)
	if err != nil {
		slog.Warn("browser_vm.recover.failed", "error", err)
	}
	o.browserMu.Lock()
	liveBrowserVMIDs := make(map[string]bool, len(o.browserVMs))
	for id, bvm := range o.browserVMs {
		liveBrowserVMIDs[id] = true
		if bvm.RuntimeOwnerRef != "" {
			knownUnits[bvm.RuntimeOwnerRef] = true
		}
	}
	for _, bvm := range o.quarantinedBrowserVMs {
		if bvm.RuntimeOwnerRef != "" {
			knownUnits[bvm.RuntimeOwnerRef] = true
		}
	}
	o.browserMu.Unlock()
	logOrphanedSystemdUnits(unitInventory, knownUnits)

	// Reconcile iptables chains against the freshly-recovered live set.
	// Runs before request serving, so no concurrent CreateBrowserVM /
	// DestroyBrowserVM can race it. Drops orphan OCM-BVM-* chains and
	// legacy pre-chain DNAT sediment left over from prior agent versions
	// or crashed teardowns. Best-effort: failures are logged, not fatal.
	if err := o.bridge.ReconcileBrowserChains(liveBrowserVMIDs); err != nil {
		slog.Error("browser_vm.dnat.reconcile.failed", "error", err)
	}

	o.cleanupStaleTaps()
	o.saveState()
	slog.Info("state.recovered", "recovered_vm_count", recovered, "persisted_vm_count", len(persisted))
	o.replayRecoveredBrowserDNATAsync(ctx, browserDNATReplays)
	return nil
}

// persistedBrowserVM is the on-disk representation of a standalone browser VM
// for state recovery across agent restarts.
type persistedBrowserVM struct {
	ID               string    `json:"id"`
	VMIP             string    `json:"vm_ip"`
	TapDevice        string    `json:"tap_device"`
	SocketPath       string    `json:"socket_path"`
	RootfsPath       string    `json:"rootfs_path"`
	CDPPort          int       `json:"cdp_port"`
	WebRTCPortBase   int       `json:"webrtc_port_base,omitempty"`
	WebRTCPortEnd    int       `json:"webrtc_port_end,omitempty"`
	Status           string    `json:"status,omitempty"`
	CDPError         string    `json:"cdp_error,omitempty"`
	PID              int       `json:"pid,omitempty"`
	RuntimeOwnerKind string    `json:"runtime_owner_kind,omitempty"`
	RuntimeOwnerRef  string    `json:"runtime_owner_ref,omitempty"`
	CreatedAt        time.Time `json:"created_at,omitempty"`
	Quarantined      bool      `json:"quarantined,omitempty"`
	QuarantineReason string    `json:"quarantine_reason,omitempty"`
	QuarantinedAt    time.Time `json:"quarantined_at,omitempty"`
}

type browserDNATReplay struct {
	browserVMID string
	vmIP        string
	portStart   int
	portEnd     int
}

func (o *firecrackerOrchestrator) browserStateFilePath() string {
	return filepath.Join(o.browserStateDir(), "browser-vms.json")
}

func (o *firecrackerOrchestrator) legacyBrowserStateFilePath() string {
	return filepath.Join(o.cfg.StateDir, "browser-vms.json")
}

// saveBrowserState persists the in-memory browserVMs map to disk. Called
// whenever a browser VM is created, status-updated, or destroyed.
func (o *firecrackerOrchestrator) saveBrowserState() {
	o.browserMu.Lock()
	persisted := make([]persistedBrowserVM, 0, len(o.browserVMs)+len(o.quarantinedBrowserVMs))
	for _, bvm := range o.browserVMs {
		persisted = append(persisted, persistedBrowserVM{
			ID:               bvm.ID,
			VMIP:             bvm.VMIP,
			TapDevice:        bvm.TapDevice,
			SocketPath:       bvm.SocketPath,
			RootfsPath:       bvm.RootfsPath,
			CDPPort:          bvm.CDPPort,
			WebRTCPortBase:   bvm.WebRTCPortBase,
			WebRTCPortEnd:    bvm.WebRTCPortEnd,
			Status:           bvm.Status,
			CDPError:         bvm.CDPError,
			PID:              bvm.PID,
			RuntimeOwnerKind: bvm.RuntimeOwnerKind,
			RuntimeOwnerRef:  bvm.RuntimeOwnerRef,
			CreatedAt:        time.Now(),
		})
	}
	for browserVMID, p := range o.quarantinedBrowserVMs {
		if _, exists := o.browserVMs[browserVMID]; exists {
			continue
		}
		persisted = append(persisted, p)
	}
	o.browserMu.Unlock()

	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		slog.Error("browser_state.marshal.failed", "error", err, "vm_count", len(persisted))
		return
	}

	stateFile := o.browserStateFilePath()
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		slog.Error("browser_state.save.mkdir_failed", "error", err, "path", filepath.Dir(stateFile))
		return
	}
	tmpFile := stateFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		slog.Error("browser_state.save.failed", "error", err, "path", tmpFile)
		return
	}
	if err := os.Rename(tmpFile, stateFile); err != nil {
		slog.Error("browser_state.save.rename_failed", "error", err)
	}
}

// recoverBrowserVMs loads persisted browser VMs and re-registers live ones.
// Orphan cleanup: if the Firecracker process is dead, the TAP/rootfs/socket
// are cleaned up (like cleanupPersistedVM does for main VMs).
func (o *firecrackerOrchestrator) recoverBrowserVMs(ctx context.Context) ([]browserDNATReplay, error) {
	statePath := o.browserStateFilePath()
	data, err := os.ReadFile(statePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read browser state: %w", err)
		}
		legacyPath := o.legacyBrowserStateFilePath()
		if legacyPath == statePath {
			return nil, nil
		}
		data, err = os.ReadFile(legacyPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("read legacy browser state: %w", err)
		}
		slog.Warn("browser_state.loaded_legacy_path",
			"path", legacyPath,
			"new_path", statePath,
			"note", "set BROWSER_STATE_DIR only after active browser VMs are drained; recovered entries will be saved to the new path on the next state write")
	}
	var persisted []persistedBrowserVM
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, fmt.Errorf("unmarshal browser state: %w", err)
	}
	if len(persisted) == 0 {
		return nil, nil
	}

	slog.Info("browser_state.loaded", "persisted_bvm_count", len(persisted))
	o.browserMu.Lock()
	o.quarantinedBrowserVMs = make(map[string]persistedBrowserVM)
	o.browserMu.Unlock()
	unitInventory, unitInventoryErr := o.systemdUnitInventory()
	if unitInventoryErr != nil {
		slog.Warn("browser_vm.recover.systemd_inventory_failed", "error", unitInventoryErr)
		unitInventory = nil
	}

	recovered := 0
	var dnatReplays []browserDNATReplay
	for _, p := range persisted {
		p.RuntimeOwnerRef = o.inferredRuntimeOwnerRef(p.ID, true, p.RuntimeOwnerKind, p.RuntimeOwnerRef, unitInventory)
		recoveredPID, shouldCleanup, quarantineReason, resolveErr := resolveRecoveredPID(p.RuntimeOwnerKind, p.RuntimeOwnerRef, p.PID)
		if resolveErr != nil {
			slog.Warn("browser_vm.recover.owner_state_failed",
				"browser_vm_id", p.ID,
				"runtime_owner_kind", defaultRuntimeOwnerKind(p.RuntimeOwnerKind),
				"runtime_owner_ref", p.RuntimeOwnerRef,
				"error", resolveErr)
		}
		if shouldCleanup {
			slog.Info("browser_vm.orphan.cleanup", "browser_vm_id", p.ID, "reason", "pid_dead")
			o.browserMu.Lock()
			delete(o.quarantinedBrowserVMs, p.ID)
			o.browserMu.Unlock()
			o.cleanupBrowserResources(p.ID, p.VMIP, p.TapDevice, p.RootfsPath, p.SocketPath, p.WebRTCPortBase, p.WebRTCPortEnd)
			continue
		}
		if quarantineReason != "" {
			slog.Warn("browser_vm.recover.owner_state_unresolved",
				"browser_vm_id", p.ID,
				"runtime_owner_kind", defaultRuntimeOwnerKind(p.RuntimeOwnerKind),
				"runtime_owner_ref", p.RuntimeOwnerRef,
				"reason", quarantineReason)
			o.browserMu.Lock()
			o.quarantinedBrowserVMs[p.ID] = quarantinePersistedBrowserVM(p, quarantineReason)
			o.browserMu.Unlock()
			continue
		}
		p.PID = recoveredPID
		if !firecrackerPIDVerifier(p.PID) {
			slog.Warn("browser_vm.recover.pid_unexpected_process", "browser_vm_id", p.ID, "pid", p.PID)
			o.browserMu.Lock()
			o.quarantinedBrowserVMs[p.ID] = quarantinePersistedBrowserVM(p, "pid_not_firecracker")
			o.browserMu.Unlock()
			continue
		}
		if _, err := os.Stat(p.SocketPath); err != nil {
			slog.Warn("browser_vm.recover.socket_missing", "browser_vm_id", p.ID, "error", err)
			o.browserMu.Lock()
			o.quarantinedBrowserVMs[p.ID] = quarantinePersistedBrowserVM(p, "socket_missing")
			o.browserMu.Unlock()
			continue
		}

		machine, err := firecracker.NewMachine(ctx, firecracker.Config{
			SocketPath: p.SocketPath,
			VMID:       p.ID,
		})
		if err != nil {
			slog.Warn("browser_vm.recover.machine_failed", "browser_vm_id", p.ID, "error", err)
			o.browserMu.Lock()
			o.quarantinedBrowserVMs[p.ID] = quarantinePersistedBrowserVM(p, "reattach_failed")
			o.browserMu.Unlock()
			continue
		}
		verifyCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		reattachedID, verifyErr := firecrackerMachineIDVerifier(verifyCtx, machine)
		cancel()
		if verifyErr != nil {
			slog.Warn("browser_vm.recover.identity_failed", "browser_vm_id", p.ID, "pid", p.PID, "error", verifyErr)
			o.browserMu.Lock()
			o.quarantinedBrowserVMs[p.ID] = quarantinePersistedBrowserVM(p, "identity_check_failed")
			o.browserMu.Unlock()
			continue
		}
		if reattachedID != p.ID {
			slog.Warn("browser_vm.recover.identity_mismatch", "browser_vm_id", p.ID, "reattached_id", reattachedID, "pid", p.PID)
			o.browserMu.Lock()
			o.quarantinedBrowserVMs[p.ID] = quarantinePersistedBrowserVM(p, "identity_mismatch")
			o.browserMu.Unlock()
			continue
		}

		status := p.Status
		if status == "" {
			status = "running"
		}
		bvm := &browserRunningVM{
			ID:               p.ID,
			VMIP:             p.VMIP,
			TapDevice:        p.TapDevice,
			SocketPath:       p.SocketPath,
			RootfsPath:       p.RootfsPath,
			Status:           status,
			CDPError:         p.CDPError,
			CDPPort:          p.CDPPort,
			PID:              p.PID,
			RuntimeOwnerKind: defaultRuntimeOwnerKind(p.RuntimeOwnerKind),
			RuntimeOwnerRef:  p.RuntimeOwnerRef,
			WebRTCPortBase:   p.WebRTCPortBase,
			WebRTCPortEnd:    p.WebRTCPortEnd,
			machine:          machine,
			cancel:           func() {},
		}

		o.browserMu.Lock()
		o.browserVMs[p.ID] = bvm
		delete(o.quarantinedBrowserVMs, p.ID)
		o.browserMu.Unlock()
		recovered++
		slog.Info("browser_vm.recovered", "browser_vm_id", p.ID, "pid", p.PID, "vm_ip", p.VMIP)
		if status == "creating" {
			o.startBrowserCDPWatcher(bvm, "recovered", "unknown", browserCDPReadinessTimeout, time.Now())
		}

		// Reinstall the WebRTC UDP DNAT rules. iptables state is ephemeral
		// across host reboots; without this replay CDP keeps working but the
		// Neko live-view silently dies after a restart. Defer the replay to a
		// background worker so host startup and heartbeat are not blocked by
		// serial iptables work across every recovered browser VM.
		if o.cfg.HostExternalIP != "" && p.VMIP != "" && p.WebRTCPortBase > 0 && p.WebRTCPortEnd >= p.WebRTCPortBase {
			dnatReplays = append(dnatReplays, browserDNATReplay{
				browserVMID: p.ID,
				vmIP:        p.VMIP,
				portStart:   p.WebRTCPortBase,
				portEnd:     p.WebRTCPortEnd,
			})
		}
	}
	o.saveBrowserState()
	slog.Info("browser_state.recovered", "recovered", recovered, "persisted", len(persisted))
	return dnatReplays, nil
}

func (o *firecrackerOrchestrator) replayRecoveredBrowserDNATAsync(ctx context.Context, replays []browserDNATReplay) {
	if len(replays) == 0 {
		return
	}
	replays = append([]browserDNATReplay(nil), replays...)
	bridge := o.bridge
	replayer := browserDNATReplayer

	slog.Info("browser_vm.recover.dnat_replay.scheduled", "count", len(replays))
	go func() {
		start := time.Now()
		slog.Info("browser_vm.recover.dnat_replay.start", "count", len(replays))
		for i, replay := range replays {
			select {
			case <-ctx.Done():
				slog.Warn("browser_vm.recover.dnat_replay.cancelled",
					"remaining", len(replays)-i,
					"error", ctx.Err())
				return
			default:
			}

			portRange := fmt.Sprintf("%d-%d", replay.portStart, replay.portEnd)
			if err := replayer(bridge, replay.browserVMID, replay.vmIP, replay.portStart, replay.portEnd); err != nil {
				slog.Warn("browser_vm.recover.dnat_replay_failed",
					"browser_vm_id", replay.browserVMID,
					"vm_ip", replay.vmIP,
					"port_range", portRange,
					"error", err)
				continue
			}
			slog.Info("browser_vm.recover.dnat_replayed",
				"browser_vm_id", replay.browserVMID,
				"vm_ip", replay.vmIP,
				"port_range", portRange)
		}
		slog.Info("browser_vm.recover.dnat_replay.completed",
			"count", len(replays),
			"duration_ms", time.Since(start).Milliseconds())
	}()
}

func (o *firecrackerOrchestrator) stopOwnedVM(ctx context.Context, ownerRef string, machine *firecracker.Machine, cancel context.CancelFunc, pid int, graceTimeout time.Duration) error {
	owner := o.owner()
	stopErr := owner.stop(ctx, ownerRef, machine, cancel, graceTimeout)
	waitErr := waitForPIDExit(ctx, pid, graceTimeout)
	if waitErr != nil && isDirectRuntimeOwner(owner) {
		slog.Warn("vm.shutdown.direct_force_kill", "pid", pid, "error", waitErr)
		if killErr := killPIDAndWait(ctx, pid, graceTimeout); killErr != nil {
			if stopErr != nil {
				return fmt.Errorf("%w; force kill failed: %v", stopErr, killErr)
			}
			return fmt.Errorf("%w; force kill failed: %v", waitErr, killErr)
		}
		waitErr = nil
		stopErr = nil
	}
	if stopErr != nil {
		return stopErr
	}
	return waitErr
}

func waitForPIDExit(ctx context.Context, pid int, timeout time.Duration) error {
	if pid <= 0 {
		return nil
	}
	if !pidAlive(pid) {
		return nil
	}

	waitCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		if !pidAlive(pid) {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("process %d still alive after stop: %w", pid, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func isDirectRuntimeOwner(owner firecrackerRuntimeOwner) bool {
	switch owner.(type) {
	case directFirecrackerRuntimeOwner, *directFirecrackerRuntimeOwner:
		return true
	default:
		return false
	}
}

var killProcessByPID = func(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}

func killPIDAndWait(ctx context.Context, pid int, timeout time.Duration) error {
	if pid <= 0 || !pidAlive(pid) {
		return nil
	}
	if err := killProcessByPID(pid); err != nil {
		if !pidAlive(pid) {
			return nil
		}
		return fmt.Errorf("kill process %d: %w", pid, err)
	}
	return waitForPIDExit(ctx, pid, timeout)
}

func (o *firecrackerOrchestrator) cleanupPersistedVM(p persistedVM, kill bool) {
	slog.Info("vm.orphan.cleanup", "machine_id", p.MachineID, "pid", p.PID, "kill_process", kill)

	if kill && p.PID > 0 {
		if proc, err := os.FindProcess(p.PID); err == nil {
			if err := proc.Kill(); err != nil {
				slog.Warn("vm.orphan.kill_failed", "machine_id", p.MachineID, "pid", p.PID, "error", err)
			}
		}
	}

	if err := o.bridge.RemoveTap(p.TapDevice); err != nil {
		slog.Warn("vm.orphan.tap_remove_failed", "machine_id", p.MachineID, "tap", p.TapDevice, "error", err)
	}
	if err := os.Remove(p.RootfsPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("vm.orphan.rootfs_remove_failed", "machine_id", p.MachineID, "path", p.RootfsPath, "error", err)
	}
	if err := os.Remove(p.SocketPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("vm.orphan.socket_remove_failed", "machine_id", p.MachineID, "path", p.SocketPath, "error", err)
	}

	if p.DataVolumePath != "" {
		slog.Info("vm.orphan.data_volume.preserved", "machine_id", p.MachineID, "path", p.DataVolumePath)
	}
}

// cleanupStaleTaps removes TAP devices on the bridge that don't belong to any known VM.
// This handles the case where the agent restarted without persisting VM state
// (e.g., during self-update or crash).
func (o *firecrackerOrchestrator) cleanupStaleTaps() {
	taps, err := o.bridge.ListTaps()
	if err != nil {
		slog.Warn("tap.stale_scan_failed", "error", err)
		return
	}

	// Build set of known TAP names from loaded VMs
	knownTaps := make(map[string]bool)
	o.mu.RLock()
	for _, vm := range o.vms {
		knownTaps[vm.instance.TapDevice] = true
	}
	for _, vm := range o.quarantinedVMs {
		knownTaps[vm.TapDevice] = true
	}
	o.mu.RUnlock()
	o.browserMu.Lock()
	for _, bvm := range o.browserVMs {
		knownTaps[bvm.TapDevice] = true
	}
	for _, bvm := range o.quarantinedBrowserVMs {
		knownTaps[bvm.TapDevice] = true
	}
	o.browserMu.Unlock()

	for _, tap := range taps {
		if knownTaps[tap] {
			continue
		}
		slog.Warn("tap.stale_cleanup", "tap_name", tap)
		if err := o.bridge.RemoveTap(tap); err != nil {
			slog.Warn("tap.stale_cleanup_failed", "tap_name", tap, "error", err)
		}
	}
}

var pidAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append([]byte(nil), b...)
}

func cloneStringMap(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneCredentialEntry(src metadata.CredentialEntry) metadata.CredentialEntry {
	dst := src
	if src.ExpiresAt != nil {
		ts := *src.ExpiresAt
		dst.ExpiresAt = &ts
	}
	return dst
}

func cloneCredentialMap(src map[string]metadata.CredentialEntry) map[string]metadata.CredentialEntry {
	if src == nil {
		return nil
	}
	dst := make(map[string]metadata.CredentialEntry, len(src))
	for k, v := range src {
		dst[k] = cloneCredentialEntry(v)
	}
	return dst
}

func cloneStringSlice(src []string) []string {
	if src == nil {
		return nil
	}
	return append([]string(nil), src...)
}

func cloneInt64Ptr(src *int64) *int64 {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}

func cloneRuntimeSelection(src *metadata.RuntimeSelection) *metadata.RuntimeSelection {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}

func cloneCustomProviders(src []metadata.CustomProviderConfig) []metadata.CustomProviderConfig {
	if src == nil {
		return nil
	}
	dst := make([]metadata.CustomProviderConfig, len(src))
	for i, cfg := range src {
		dst[i] = cfg
		dst[i].AllowedHosts = cloneStringSlice(cfg.AllowedHosts)
	}
	return dst
}

func cloneSoulEntries(src []metadata.SoulEntry) []metadata.SoulEntry {
	if src == nil {
		return nil
	}
	dst := make([]metadata.SoulEntry, len(src))
	copy(dst, src)
	return dst
}

// dataVolumePath returns the path to the persistent data volume for a machine.
func (o *firecrackerOrchestrator) dataVolumePath(machineID string) string {
	if o.cfg.DataDir == "" {
		return ""
	}
	return filepath.Join(o.cfg.DataDir, machineID+".ext4")
}

// versionSidecarPath returns the path to the version sidecar file.
func (o *firecrackerOrchestrator) versionSidecarPath(machineID string) string {
	if o.cfg.DataDir == "" {
		return ""
	}
	return filepath.Join(o.cfg.DataDir, machineID+".version")
}

// readVersionSidecar reads the data version from the sidecar file.
func (o *firecrackerOrchestrator) readVersionSidecar(machineID string) int {
	path := o.versionSidecarPath(machineID)
	if path == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("vm.version_sidecar.read_failed", "machine_id", machineID, "path", path, "error", err)
		}
		return 0
	}
	v := 0
	n, _ := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &v)
	if n == 0 {
		slog.Warn("vm.version_sidecar.parse_failed", "machine_id", machineID, "path", path,
			"content", strings.TrimSpace(string(data)))
	}
	return v
}

// writeVersionSidecar writes the data version to the sidecar file.
func (o *firecrackerOrchestrator) writeVersionSidecar(machineID string, version int) error {
	path := o.versionSidecarPath(machineID)
	if path == "" {
		return nil
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\n", version)), 0644)
}

// backupDataVolume creates a pre-upgrade backup of the data volume.
func (o *firecrackerOrchestrator) backupDataVolume(machineID string) error {
	src := o.dataVolumePath(machineID)
	if src == "" {
		return nil
	}

	// Check source size and available space
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat data volume: %w", err)
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(src), &stat); err == nil {
		avail := stat.Bavail * uint64(stat.Bsize)
		// Conservative: need at least half apparent size (sparse files use less actual disk)
		if avail < uint64(info.Size()/2) {
			return fmt.Errorf("insufficient disk space for backup: %d available, %d apparent volume size", avail, info.Size())
		}
	}

	dst := src + ".pre-upgrade"

	// Remove stale pre-upgrade backup so we don't keep a snapshot from a
	// previous upgrade cycle that no longer matches the current volume.
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale pre-upgrade backup: %w", err)
	}

	slog.Info("vm.data_volume.backup.creating", "machine_id", machineID, "src", src, "dst", dst)
	cmd := exec.Command("cp", "--reflink=auto", src, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("backup data volume: %s: %w", string(out), err)
	}
	slog.Info("vm.data_volume.backup.created", "machine_id", machineID)
	return nil
}

// RollbackDataVolume restores the pre-upgrade backup of a data volume.
func (o *firecrackerOrchestrator) RollbackDataVolume(machineID string) error {
	o.mu.RLock()
	_, running := o.vms[machineID]
	o.mu.RUnlock()
	if running {
		return fmt.Errorf("cannot rollback: VM %s is running", machineID)
	}

	src := o.dataVolumePath(machineID)
	if src == "" {
		return fmt.Errorf("no data directory configured")
	}
	backup := src + ".pre-upgrade"
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		return fmt.Errorf("no pre-upgrade backup found for %s", machineID)
	}
	// Swap: remove current, rename backup to current
	if err := os.Remove(src); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove current volume: %w", err)
	}
	if err := os.Rename(backup, src); err != nil {
		return fmt.Errorf("restore backup: %w", err)
	}
	// Reset version sidecar
	versionPath := o.versionSidecarPath(machineID)
	if versionPath != "" {
		_ = os.Remove(versionPath)
	}
	slog.Info("vm.data_volume.rollback.completed", "machine_id", machineID)
	return nil
}

// ensureDataVolume creates a sparse ext4 data volume if it doesn't exist, or reuses
// the existing one. If a version upgrade is detected, creates a pre-upgrade backup.
func (o *firecrackerOrchestrator) ensureDataVolume(machineID string, sizeGB int, dataVersion int) (string, error) {
	path := o.dataVolumePath(machineID)
	if path == "" {
		return "", nil
	}

	if _, err := os.Stat(path); err == nil {
		// Volume exists — check if pre-upgrade backup is needed
		if dataVersion > 0 {
			currentVersion := o.readVersionSidecar(machineID)
			if currentVersion < dataVersion {
				slog.Info("vm.data_volume.upgrade_detected",
					"machine_id", machineID,
					"current_version", currentVersion,
					"target_version", dataVersion)
				if err := o.backupDataVolume(machineID); err != nil {
					slog.Warn("vm.data_volume.backup.failed", "machine_id", machineID, "error", err)
					// Non-fatal: proceed without backup
				}
			}
		}
		slog.Info("vm.data_volume.reuse", "machine_id", machineID, "path", path)
		return path, nil // reuse existing
	}

	// Create new volume
	slog.Info("vm.data_volume.create", "machine_id", machineID, "size_gb", sizeGB)

	sizeBytes := int64(sizeGB) * 1024 * 1024 * 1024
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("create data volume file: %w", err)
	}
	if err := f.Truncate(sizeBytes); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("truncate data volume: %w", err)
	}
	_ = f.Close()

	cmd := exec.Command("mkfs.ext4", "-m", "1", "-q", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("mkfs.ext4: %s: %w", string(out), err)
	}

	slog.Info("vm.data_volume.created", "machine_id", machineID, "path", path, "size_gb", sizeGB)
	return path, nil
}

// stageBaseRootfs stages the base rootfs image into StateDir.
// If GCS distribution is configured, it downloads from GCS and fails hard on error
// (no silent fallback to potentially stale embedded image).
// If GCS is not configured, uses the embedded rootfs from the boot disk.
func (o *firecrackerOrchestrator) stageBaseRootfs() error {
	if o.cfg.GCSRootfsManifest != "" {
		if err := o.stageFromGCS(); err != nil {
			slog.Error("rootfs.gcs.failed", "error", err,
				"manifest", o.cfg.GCSRootfsManifest)
			return fmt.Errorf("GCS rootfs download failed (will not fall back to potentially stale embedded image): %w", err)
		}
		return nil
	}
	if err := o.stageFromEmbedded(); err != nil {
		slog.Error("rootfs.embedded.staging_failed", "error", err)
		return fmt.Errorf("rootfs staging failed: %w", err)
	}
	return nil
}

// stageFromGCS downloads the rootfs from GCS using the manifest URI.
func (o *firecrackerOrchestrator) stageFromGCS() error {
	timeout := o.cfg.GCSDownloadTimeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	retries := o.cfg.GCSRetryAttempts
	if retries == 0 {
		retries = 3
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout+time.Minute)
	defer cancel()

	fetcher, err := rootfs.NewFetcher(ctx, rootfs.Config{
		ManifestURI:     o.cfg.GCSRootfsManifest,
		DownloadTimeout: timeout,
		RetryAttempts:   retries,
	})
	if err != nil {
		return fmt.Errorf("create rootfs fetcher: %w", err)
	}
	defer func() { _ = fetcher.Close() }()

	path, err := fetcher.EnsureRootfs(ctx, o.cfg.StateDir)
	if err != nil {
		return fmt.Errorf("ensure rootfs: %w", err)
	}

	slog.Info("rootfs.gcs.staged", "path", path)
	return nil
}

// stageFromEmbedded copies the base rootfs image from RootfsDir (ext4) to
// StateDir (XFS) so that subsequent reflink copies are instant CoW clones.
// Skips the copy if the staged file is already up-to-date.
func (o *firecrackerOrchestrator) stageFromEmbedded() error {
	src := filepath.Join(o.cfg.RootfsDir, "rootfs.ext4")
	dst := filepath.Join(o.cfg.StateDir, ".base-rootfs.ext4")

	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("rootfs base not found at %s: %w", src, err)
	}

	// Skip if staged copy exists and is same size + not older than source
	if dstInfo, err := os.Stat(dst); err == nil {
		if dstInfo.Size() == srcInfo.Size() && !dstInfo.ModTime().Before(srcInfo.ModTime()) {
			slog.Info("rootfs.stage.skipped", "path", dst)
			return nil
		}
	}

	slog.Info("rootfs.stage.starting", "size_mb", srcInfo.Size()/(1024*1024))
	start := time.Now()
	cmd := exec.Command("cp", "--reflink=auto", src, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rootfs stage copy failed: %s: %w", string(out), err)
	}
	slog.Info("rootfs.stage.completed", "duration_ms", time.Since(start).Milliseconds())
	return nil
}

// baseRootfsPath returns the path to the staged base rootfs on XFS (preferred)
// or falls back to the original in RootfsDir.
func (o *firecrackerOrchestrator) baseRootfsPath() string {
	staged := filepath.Join(o.cfg.StateDir, ".base-rootfs.ext4")
	if _, err := os.Stat(staged); err == nil {
		return staged
	}
	return filepath.Join(o.cfg.RootfsDir, "rootfs.ext4")
}

// allocateWebRTCPortSlot finds an unused UDP port range for a new browser VM.
// Returns (base, end) where end = base + browserWebRTCSlotSize - 1.
// Must be called with o.browserMu held or before the VM is inserted into the map.
func (o *firecrackerOrchestrator) allocateWebRTCPortSlot() (int, int, error) {
	used := make(map[int]bool)
	for _, bvm := range o.browserVMs {
		if bvm.WebRTCPortBase > 0 {
			used[bvm.WebRTCPortBase] = true
		}
	}
	for _, bvm := range o.quarantinedBrowserVMs {
		if bvm.WebRTCPortBase > 0 {
			used[bvm.WebRTCPortBase] = true
		}
	}
	for slot := 0; slot < browserWebRTCMaxSlots; slot++ {
		base := browserWebRTCBasePort + slot*browserWebRTCSlotSize
		if !used[base] {
			return base, base + browserWebRTCSlotSize - 1, nil
		}
	}
	return 0, 0, fmt.Errorf("no free WebRTC port slot (max %d browser VMs per host)", browserWebRTCMaxSlots)
}

// browserRootfsBaseDir returns the directory that holds versioned browser rootfs releases.
// Layout: {BrowserStateDir}/browser-rootfs/releases/{version}.ext4 — mirrors openClawReleasePath.
func (o *firecrackerOrchestrator) browserRootfsBaseDir() string {
	return filepath.Join(o.browserStateDir(), "browser-rootfs")
}

func (o *firecrackerOrchestrator) browserRootfsHeartbeatSidecarPath() string {
	return filepath.Join(o.browserStateDir(), ".browser-rootfs-manifest.json")
}

func (o *firecrackerOrchestrator) browserStateDir() string {
	if dir := strings.TrimSpace(o.cfg.BrowserStateDir); dir != "" {
		return dir
	}
	return o.cfg.StateDir
}

func browserRootfsLineage(manifestURI string) string {
	manifestURI = strings.TrimSpace(manifestURI)
	switch {
	case manifestURI == "":
		return "disabled"
	case strings.Contains(manifestURI, "kernel-browser-rootfs"):
		return "kernel-images-experimental"
	case strings.Contains(manifestURI, "browser-rootfs"):
		return "legacy"
	default:
		return "custom"
	}
}

func browserRootfsRequiresReflink(rootfsLineage, rootfsVersion string, allowFullCopy bool) bool {
	if allowFullCopy || rootfsLineage != "kernel-images-experimental" {
		return false
	}
	version := strings.TrimSpace(rootfsVersion)
	stableVersion := strings.TrimSpace(config.StableKernelBrowserRootfsVersion)
	if stableVersion == "" {
		return version == ""
	}
	return version != stableVersion
}

func (o *firecrackerOrchestrator) syncBrowserRootfsHeartbeatSidecar(stagedPath string) error {
	sidecarSrc := stagedPath + ".manifest.json"
	data, err := os.ReadFile(sidecarSrc)
	if err != nil {
		return fmt.Errorf("read staged browser rootfs manifest sidecar: %w", err)
	}
	if err := os.WriteFile(o.browserRootfsHeartbeatSidecarPath(), data, 0o644); err != nil {
		return fmt.Errorf("write browser rootfs heartbeat sidecar: %w", err)
	}
	return nil
}

// stageBrowserRootfs downloads the browser rootfs from GCS to a versioned path.
// Returns the staged path and the version.
func (o *firecrackerOrchestrator) stageBrowserRootfs() (string, string, error) {
	return o.stageBrowserRootfsWithSelection("", "")
}

func (o *firecrackerOrchestrator) stageBrowserRootfsWithSelection(manifestURI, versionPin string) (string, string, error) {
	manifestURI = strings.TrimSpace(manifestURI)
	versionPin = strings.TrimSpace(versionPin)
	if manifestURI == "" {
		manifestURI = strings.TrimSpace(o.cfg.GCSBrowserRootfsManifest)
	}
	if versionPin == "" {
		versionPin = strings.TrimSpace(o.cfg.GCSBrowserRootfsVersion)
	}
	if manifestURI == "" {
		return "", "", fmt.Errorf("browser rootfs manifest not configured")
	}
	stageStarted := time.Now()
	lineage := browserRootfsLineage(manifestURI)

	timeout := o.cfg.GCSDownloadTimeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	retries := o.cfg.GCSRetryAttempts
	if retries == 0 {
		retries = 3
	}
	slog.Info("browser_rootfs.stage.starting",
		"manifest_uri", manifestURI,
		"version_pin", versionPin,
		"lineage", lineage,
		"timeout", timeout.String(),
		"retries", retries)

	ctx, cancel := context.WithTimeout(context.Background(), timeout+time.Minute)
	defer cancel()

	fetcher, err := rootfs.NewFetcher(ctx, rootfs.Config{
		ManifestURI:     manifestURI,
		DownloadTimeout: timeout,
		RetryAttempts:   retries,
	})
	if err != nil {
		return "", "", fmt.Errorf("create browser rootfs fetcher: %w", err)
	}
	defer func() { _ = fetcher.Close() }()

	var path string
	if versionPin != "" {
		path, err = fetcher.EnsureRootfsRelease(ctx, o.browserRootfsBaseDir(), versionPin)
	} else {
		path, err = fetcher.EnsureRootfsVersioned(ctx, o.browserRootfsBaseDir())
	}
	if err != nil {
		return "", "", fmt.Errorf("ensure browser rootfs: %w", err)
	}
	if err := o.syncBrowserRootfsHeartbeatSidecar(path); err != nil {
		return "", "", err
	}

	// Extract version from path: {baseDir}/releases/{version}.ext4
	version := strings.TrimSuffix(filepath.Base(path), ".ext4")
	slog.Info("browser_rootfs.gcs.staged",
		"path", path,
		"version", version,
		"manifest_uri", manifestURI,
		"version_pin", versionPin,
		"lineage", lineage,
		"duration_ms", time.Since(stageStarted).Milliseconds())
	return path, version, nil
}

func (o *firecrackerOrchestrator) validateBrowserVMCreateRequest(cfg BrowserVMConfig) error {
	if strings.TrimSpace(cfg.BrowserVMID) == "" {
		return fmt.Errorf("browser VM ID is required")
	}
	if net.ParseIP(cfg.VMIP).To4() == nil {
		return fmt.Errorf("browser VM IP %q is invalid", cfg.VMIP)
	}

	o.browserMu.Lock()
	defer o.browserMu.Unlock()
	if _, exists := o.browserVMs[cfg.BrowserVMID]; exists {
		return fmt.Errorf("browser VM %s already exists on host", cfg.BrowserVMID)
	}
	if _, exists := o.quarantinedBrowserVMs[cfg.BrowserVMID]; exists {
		return fmt.Errorf("browser VM %s is quarantined on host", cfg.BrowserVMID)
	}
	for id, bvm := range o.browserVMs {
		if bvm.VMIP == cfg.VMIP {
			return fmt.Errorf("browser VM IP %s already owned by %s on host", cfg.VMIP, id)
		}
	}
	for id, bvm := range o.quarantinedBrowserVMs {
		if bvm.VMIP == cfg.VMIP {
			return fmt.Errorf("browser VM IP %s already owned by quarantined browser VM %s on host", cfg.VMIP, id)
		}
	}
	return nil
}

func (o *firecrackerOrchestrator) startBrowserCDPWatcher(bvm *browserRunningVM, rootfsVersion, rootfsLineage string, timeout time.Duration, createStarted time.Time) {
	if timeout <= 0 {
		timeout = browserCDPReadinessTimeout
	}
	browserVMID := bvm.ID
	vmIP := bvm.VMIP
	cdpPort := bvm.CDPPort
	if cdpPort == 0 {
		cdpPort = 9222
	}

	go func() {
		cdpStarted := time.Now()
		slog.Info("standalone_browser_vm.cdp.wait_start",
			"browser_vm_id", browserVMID,
			"vm_ip", vmIP,
			"timeout", timeout.String(),
			"rootfs_lineage", rootfsLineage,
			"elapsed_ms", time.Since(createStarted).Milliseconds())

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := waitForPortContext(ctx, vmIP, cdpPort, timeout); err != nil {
			slog.Warn("standalone_browser_vm.cdp.not_ready",
				"browser_vm_id", browserVMID,
				"vm_ip", vmIP,
				"rootfs_version", rootfsVersion,
				"rootfs_lineage", rootfsLineage,
				"wait_ms", time.Since(cdpStarted).Milliseconds(),
				"elapsed_ms", time.Since(createStarted).Milliseconds(),
				"error", err)
			o.browserMu.Lock()
			current := o.browserVMs[browserVMID]
			if current == bvm {
				current.Status = "error"
				current.CDPError = err.Error()
			}
			o.browserMu.Unlock()
			o.saveBrowserState()
			return
		}

		slog.Info("standalone_browser_vm.cdp.ready",
			"browser_vm_id", browserVMID,
			"vm_ip", vmIP,
			"rootfs_version", rootfsVersion,
			"rootfs_lineage", rootfsLineage,
			"wait_ms", time.Since(cdpStarted).Milliseconds(),
			"elapsed_ms", time.Since(createStarted).Milliseconds())
		o.browserMu.Lock()
		current := o.browserVMs[browserVMID]
		if current == bvm {
			current.Status = "ready"
			current.CDPError = ""
		}
		o.browserMu.Unlock()
		o.saveBrowserState()
	}()
}

// ---------------------------------------------------------------------------
// Standalone browser VM management
// ---------------------------------------------------------------------------

// CreateBrowserVM boots a standalone browser VM (not coupled to a machine VM).
func (o *firecrackerOrchestrator) CreateBrowserVM(ctx context.Context, cfg BrowserVMConfig) error {
	o.browserCreateMu.Lock()
	defer o.browserCreateMu.Unlock()

	createStarted := time.Now()
	rootfsManifest := strings.TrimSpace(cfg.RootfsManifest)
	if rootfsManifest == "" {
		rootfsManifest = o.cfg.GCSBrowserRootfsManifest
	}
	rootfsVersionPin := strings.TrimSpace(cfg.RootfsVersion)
	if rootfsVersionPin == "" {
		rootfsVersionPin = o.cfg.GCSBrowserRootfsVersion
	}
	rootfsLineage := browserRootfsLineage(rootfsManifest)
	slog.Info("standalone_browser_vm.create.start",
		"browser_vm_id", cfg.BrowserVMID,
		"vm_ip", cfg.VMIP,
		"requested_vcpus", cfg.VCPUs,
		"requested_memory_mb", cfg.MemoryMB,
		"rootfs_manifest", rootfsManifest,
		"rootfs_version_pin", rootfsVersionPin,
		"rootfs_lineage", rootfsLineage)
	if err := o.validateBrowserVMCreateRequest(cfg); err != nil {
		return err
	}

	// Stage the browser rootfs from GCS to a versioned release path.
	// Follows the same pattern as OpenClaw runtime releases:
	// {BrowserStateDir}/browser-rootfs/releases/{version}.ext4 — allowing multiple
	// versions to coexist and preventing stale cached files from colliding.
	stageStarted := time.Now()
	baseRootfs, rootfsVersion, err := o.stageBrowserRootfsWithSelection(rootfsManifest, rootfsVersionPin)
	if err != nil {
		slog.Error("standalone_browser_vm.rootfs_stage_failed",
			"browser_vm_id", cfg.BrowserVMID,
			"rootfs_lineage", rootfsLineage,
			"duration_ms", time.Since(stageStarted).Milliseconds(),
			"error", err)
		return fmt.Errorf("stage browser rootfs: %w", err)
	}
	slog.Info("standalone_browser_vm.rootfs_staged",
		"browser_vm_id", cfg.BrowserVMID,
		"version", rootfsVersion,
		"path", baseRootfs,
		"lineage", rootfsLineage,
		"duration_ms", time.Since(stageStarted).Milliseconds())

	// TAP device name: "btap" + first 10 chars of ID (15-char limit)
	tapName := "btap" + cfg.BrowserVMID[:min(10, len(cfg.BrowserVMID))]

	tapStarted := time.Now()
	if err := o.bridge.CreateTap(tapName); err != nil {
		return fmt.Errorf("create browser tap %s: %w", tapName, err)
	}
	slog.Info("standalone_browser_vm.tap.created",
		"browser_vm_id", cfg.BrowserVMID,
		"tap", tapName,
		"duration_ms", time.Since(tapStarted).Milliseconds())

	// Reflink copy browser rootfs
	browserRootfs := filepath.Join(o.browserStateDir(), cfg.BrowserVMID+"-browser.ext4")
	copyStarted := time.Now()
	requireReflink := browserRootfsRequiresReflink(rootfsLineage, rootfsVersion, o.cfg.AllowKernelBrowserFullCopy)
	copyMode, err := copyRootfs(baseRootfs, browserRootfs, requireReflink)
	if err != nil {
		_ = o.bridge.RemoveTap(tapName)
		slog.Error("standalone_browser_vm.rootfs_copy_failed",
			"browser_vm_id", cfg.BrowserVMID,
			"rootfs_lineage", rootfsLineage,
			"reflink_required", requireReflink,
			"allow_kernel_browser_full_copy", o.cfg.AllowKernelBrowserFullCopy,
			"duration_ms", time.Since(copyStarted).Milliseconds(),
			"error", err)
		return fmt.Errorf("copy browser rootfs: %w", err)
	}
	if copyMode != rootfsCopyModeReflink {
		slog.Warn("standalone_browser_vm.rootfs_full_copy",
			"browser_vm_id", cfg.BrowserVMID,
			"rootfs_lineage", rootfsLineage,
			"src", baseRootfs,
			"dst", browserRootfs,
			"duration_ms", time.Since(copyStarted).Milliseconds())
	}
	slog.Info("standalone_browser_vm.rootfs_copied",
		"browser_vm_id", cfg.BrowserVMID,
		"src", baseRootfs,
		"dst", browserRootfs,
		"copy_mode", copyMode,
		"reflink_required", requireReflink,
		"allow_kernel_browser_full_copy", o.cfg.AllowKernelBrowserFullCopy,
		"duration_ms", time.Since(copyStarted).Milliseconds())

	socketPath := filepath.Join(o.cfg.SocketDir, cfg.BrowserVMID+"-browser.sock")
	_ = os.Remove(socketPath) // remove stale socket

	vcpus := int64(cfg.VCPUs)
	if vcpus <= 0 {
		vcpus = 2
	}
	memMB := int64(cfg.MemoryMB)
	if memMB <= 0 {
		memMB = 4096
	}
	smt := false

	// Allocate a WebRTC UDP port slot for this VM. Each browser VM gets a
	// unique 100-port range so multiple VMs on the same host can stream
	// media concurrently without iptables DNAT rules conflicting.
	o.browserMu.Lock()
	webrtcBase, webrtcEnd, webrtcErr := o.allocateWebRTCPortSlot()
	o.browserMu.Unlock()
	if webrtcErr != nil {
		_ = o.bridge.RemoveTap(tapName)
		_ = os.Remove(browserRootfs)
		_ = os.Remove(socketPath)
		return fmt.Errorf("allocate WebRTC port slot: %w", webrtcErr)
	}
	slog.Info("standalone_browser_vm.webrtc_slot.allocated",
		"browser_vm_id", cfg.BrowserVMID,
		"port_base", webrtcBase,
		"port_end", webrtcEnd)

	kernelArgs := "console=ttyS0 reboot=k panic=1 pci=off init=/sbin/overlay-init"
	// Pass host external IP so Neko can advertise it via NAT1TO1 for WebRTC.
	// Without this, the browser sees private bridge IPs it can't reach and
	// the WebRTC peer connection fails with "peer failed".
	if o.cfg.HostExternalIP != "" {
		kernelArgs += " nat1to1_ip=" + o.cfg.HostExternalIP
	}
	// Pass allocated WebRTC port range; init-kernel-browser.sh exports it
	// as NEKO_WEBRTC_EPR.
	kernelArgs += fmt.Sprintf(" webrtc_port_base=%d webrtc_port_end=%d", webrtcBase, webrtcEnd)

	fcCfg := firecracker.Config{
		SocketPath:      socketPath,
		KernelImagePath: o.cfg.KernelPath,
		KernelArgs:      kernelArgs,
		MachineCfg: models.MachineConfiguration{
			VcpuCount:  &vcpus,
			MemSizeMib: &memMB,
			Smt:        &smt,
		},
		Drives: []models.Drive{
			{
				DriveID:      firecracker.String("rootfs"),
				PathOnHost:   firecracker.String(browserRootfs),
				IsRootDevice: firecracker.Bool(true),
				IsReadOnly:   firecracker.Bool(false),
			},
		},
		NetworkInterfaces: []firecracker.NetworkInterface{
			{
				StaticConfiguration: &firecracker.StaticNetworkConfiguration{
					MacAddress:  generateMAC(cfg.VMIP),
					HostDevName: tapName,
					IPConfiguration: &firecracker.IPConfiguration{
						IPAddr: net.IPNet{
							IP:   net.ParseIP(cfg.VMIP),
							Mask: net.CIDRMask(24, 32),
						},
						Gateway:     net.ParseIP(o.bridge.Gateway),
						Nameservers: []string{"8.8.8.8", "8.8.4.4"},
					},
				},
			},
		},
	}

	owner := o.owner()
	firecrackerStarted := time.Now()
	machine, pid, browserCancel, err := owner.start(ctx, cfg.BrowserVMID, true, socketPath, fcCfg, vmConsoleWriter(cfg.BrowserVMID), vmConsoleWriter(cfg.BrowserVMID))
	if err != nil {
		_ = o.bridge.RemoveTap(tapName)
		_ = os.Remove(browserRootfs)
		_ = os.Remove(socketPath)
		return err
	}

	slog.Info("standalone_browser_vm.started",
		"browser_vm_id", cfg.BrowserVMID,
		"vm_ip", cfg.VMIP,
		"tap", tapName,
		"pid", pid,
		"owner_kind", owner.kind(),
		"vcpus", vcpus,
		"memory_mb", memMB,
		"rootfs_version", rootfsVersion,
		"rootfs_lineage", rootfsLineage,
		"duration_ms", time.Since(firecrackerStarted).Milliseconds(),
		"elapsed_ms", time.Since(createStarted).Milliseconds())

	bvm := &browserRunningVM{
		ID:               cfg.BrowserVMID,
		VMIP:             cfg.VMIP,
		TapDevice:        tapName,
		SocketPath:       socketPath,
		RootfsPath:       browserRootfs,
		Status:           "creating",
		CDPPort:          9222,
		PID:              pid,
		RuntimeOwnerKind: owner.kind(),
		RuntimeOwnerRef:  owner.ownerRef(cfg.BrowserVMID, true),
		WebRTCPortBase:   webrtcBase,
		WebRTCPortEnd:    webrtcEnd,
		machine:          machine,
		cancel:           browserCancel,
	}

	o.browserMu.Lock()
	o.browserVMs[cfg.BrowserVMID] = bvm
	delete(o.quarantinedBrowserVMs, cfg.BrowserVMID)
	o.browserMu.Unlock()
	o.saveBrowserState()
	slog.Info("standalone_browser_vm.state.persisted",
		"browser_vm_id", cfg.BrowserVMID,
		"status", bvm.Status,
		"rootfs_version", rootfsVersion,
		"rootfs_lineage", rootfsLineage,
		"elapsed_ms", time.Since(createStarted).Milliseconds())

	// Set up UDP DNAT for Neko's WebRTC media stream. Browser sends UDP
	// packets to host_public_ip:webrtcBase-webrtcEnd, which are DNATed to
	// this browser VM's bridge IP. Each browser VM has a unique port slot
	// so multiple VMs on the same host can stream concurrently.
	//
	// Hard-fail: a silently-broken live preview (CDP works, WebRTC dead)
	// is the exact sediment pattern the chain refactor is trying to
	// eliminate. If chain install fails, tear the VM back down and surface
	// the error so the operator fixes the underlying iptables / capability
	// issue instead of shipping a green VM that nobody can watch.
	if o.cfg.HostExternalIP != "" && rootfsLineage != "legacy" {
		dnatStarted := time.Now()
		if err := o.bridge.InstallBrowserDNATChain(cfg.BrowserVMID, cfg.VMIP, webrtcBase, webrtcEnd); err != nil {
			slog.Error("standalone_browser_vm.dnat.install_failed",
				"browser_vm_id", cfg.BrowserVMID,
				"port_range", fmt.Sprintf("%d-%d", webrtcBase, webrtcEnd),
				"duration_ms", time.Since(dnatStarted).Milliseconds(),
				"error", err)
			_ = o.stopOwnedVM(context.Background(), owner.ownerRef(cfg.BrowserVMID, true), machine, browserCancel, pid, 5*time.Second)
			o.cleanupBrowserResources(cfg.BrowserVMID, cfg.VMIP, tapName, browserRootfs, socketPath, webrtcBase, webrtcEnd)
			o.browserMu.Lock()
			delete(o.browserVMs, cfg.BrowserVMID)
			o.browserMu.Unlock()
			o.saveBrowserState()
			return fmt.Errorf("install browser DNAT chain: %w", err)
		}
		slog.Info("standalone_browser_vm.dnat.installed",
			"browser_vm_id", cfg.BrowserVMID,
			"port_range", fmt.Sprintf("%d-%d", webrtcBase, webrtcEnd),
			"duration_ms", time.Since(dnatStarted).Milliseconds(),
			"elapsed_ms", time.Since(createStarted).Milliseconds())
	} else if rootfsLineage == "legacy" {
		slog.Info("standalone_browser_vm.dnat.skipped",
			"browser_vm_id", cfg.BrowserVMID,
			"rootfs_lineage", rootfsLineage,
			"reason", "legacy browser rootfs has no Neko/WebRTC live view",
			"elapsed_ms", time.Since(createStarted).Milliseconds())
	}

	// CDP readiness is asynchronous. Firecracker start is a durable side
	// effect, and slow Chromium readiness must not make a foreground HTTP
	// timeout release backend ownership while the browser VM is still alive.
	o.startBrowserCDPWatcher(bvm, rootfsVersion, rootfsLineage, browserCDPReadinessTimeout, createStarted)
	slog.Info("standalone_browser_vm.create.accepted",
		"browser_vm_id", cfg.BrowserVMID,
		"vm_ip", cfg.VMIP,
		"rootfs_version", rootfsVersion,
		"rootfs_lineage", rootfsLineage,
		"cdp_readiness_timeout", browserCDPReadinessTimeout.String(),
		"elapsed_ms", time.Since(createStarted).Milliseconds())

	return nil
}

// DestroyBrowserVM stops and cleans up a standalone browser VM.
//
// Handles three possible states:
//  1. Normal — the VM is in o.browserVMs. Stop it, clean up resources.
//  2. Quarantined — the VM is in o.quarantinedBrowserVMs (recovery could not
//     reattach it cleanly: orphan PID, socket missing, identity mismatch,
//     etc.). Still perform best-effort cleanup: stop the runtime-owner unit,
//     kill the PID if still alive, and remove tap/rootfs/socket/DNAT. The
//     control plane is deleting the record; we must not leave quarantined
//     host state behind with no handle back to it.
//  3. Truly unknown — not in either map. Treat as already-satisfied and
//     return nil so orphan DB rows can be cleaned up by the control plane's
//     delete flow without getting stuck on a 500.
//
// Idempotency is important but must not mask cases where there IS something
// to clean up. Case 3 (unknown) was added in 99c13b2 for delete-retries;
// case 2 (quarantined) is the correction from Codex review that prevents
// resource orphaning.
func (o *firecrackerOrchestrator) DestroyBrowserVM(ctx context.Context, browserVMID string) error {
	o.browserMu.Lock()
	bvm, exists := o.browserVMs[browserVMID]
	if !exists {
		// Case 2: quarantined. Clean up without a live *firecracker.Machine
		// handle — we only have the persisted metadata. For systemd-unit
		// owners this is enough (stop by unit name). For direct owners we
		// fall back to killing the PID if it's still alive.
		if q, quarantined := o.quarantinedBrowserVMs[browserVMID]; quarantined {
			delete(o.quarantinedBrowserVMs, browserVMID)
			o.browserMu.Unlock()
			slog.Info("standalone_browser_vm.destroy.quarantined_cleanup",
				"browser_vm_id", browserVMID,
				"reason", q.QuarantineReason,
				"owner_kind", q.RuntimeOwnerKind,
				"owner_ref", q.RuntimeOwnerRef,
				"pid", q.PID)

			// Best-effort stop via the runtime owner. If a ref is set and
			// owner().stop succeeds, the process is gone; if it errors, we
			// still fall through to PID kill + resource cleanup so the host
			// doesn't keep stale state.
			if q.RuntimeOwnerRef != "" {
				stopCtx, stopCancel := context.WithTimeout(ctx, 5*time.Second)
				if err := o.owner().stop(stopCtx, q.RuntimeOwnerRef, nil, nil, 3*time.Second); err != nil {
					slog.Warn("standalone_browser_vm.quarantined.owner_stop_failed",
						"browser_vm_id", browserVMID, "owner_ref", q.RuntimeOwnerRef, "error", err)
				}
				stopCancel()
			}
			if q.PID > 0 && pidAlive(q.PID) {
				if proc, err := os.FindProcess(q.PID); err == nil {
					if killErr := proc.Kill(); killErr != nil {
						slog.Warn("standalone_browser_vm.quarantined.kill_failed",
							"browser_vm_id", browserVMID, "pid", q.PID, "error", killErr)
					}
				}
			}
			o.saveBrowserState()
			o.cleanupBrowserResources(browserVMID, q.VMIP, q.TapDevice, q.RootfsPath, q.SocketPath, q.WebRTCPortBase, q.WebRTCPortEnd)
			slog.Info("standalone_browser_vm.destroy.quarantined_done", "browser_vm_id", browserVMID)
			return nil
		}

		// Case 3: truly unknown. Idempotent success so orphan DB rows
		// can be cleaned up via the control plane's delete flow.
		o.browserMu.Unlock()
		slog.Info("standalone_browser_vm.destroy.already_gone", "browser_vm_id", browserVMID)
		return nil
	}
	o.browserMu.Unlock()

	slog.Info("standalone_browser_vm.destroying", "browser_vm_id", browserVMID)

	if err := o.stopOwnedVM(ctx, bvm.RuntimeOwnerRef, bvm.machine, bvm.cancel, bvm.PID, 5*time.Second); err != nil {
		slog.Warn("standalone_browser_vm.shutdown.owner_stop.failed",
			"browser_vm_id", browserVMID, "pid", bvm.PID, "error", err)
		return err
	}

	o.browserMu.Lock()
	delete(o.browserVMs, browserVMID)
	o.browserMu.Unlock()
	o.saveBrowserState()

	o.cleanupBrowserResources(browserVMID, bvm.VMIP, bvm.TapDevice, bvm.RootfsPath, bvm.SocketPath, bvm.WebRTCPortBase, bvm.WebRTCPortEnd)

	slog.Info("standalone_browser_vm.destroyed", "browser_vm_id", browserVMID)
	return nil
}

func (o *firecrackerOrchestrator) cleanupBrowserResources(browserVMID, vmIP, tapDevice, rootfsPath, socketPath string, webrtcBase, webrtcEnd int) {
	// Chain-based teardown is keyed by browser-VM UUID, not (vmIP, port
	// range) — that's the whole point of the refactor: we own the chain
	// by name, not by rule-shape. webrtcBase/webrtcEnd are kept on the
	// signature for callers that still log the port slot but aren't used
	// for the teardown call itself.
	if browserVMID != "" {
		o.bridge.RemoveBrowserDNATChain(browserVMID)
	}
	if tapDevice != "" {
		if err := o.bridge.RemoveTap(tapDevice); err != nil {
			slog.Warn("standalone_browser_vm.cleanup.tap_remove_failed",
				"browser_vm_id", browserVMID, "tap", tapDevice, "error", err)
		}
	}
	if rootfsPath != "" {
		if err := os.Remove(rootfsPath); err != nil && !os.IsNotExist(err) {
			slog.Warn("standalone_browser_vm.cleanup.rootfs_remove_failed",
				"browser_vm_id", browserVMID, "path", rootfsPath, "error", err)
		}
	}
	if socketPath != "" {
		if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
			slog.Warn("standalone_browser_vm.cleanup.socket_remove_failed",
				"browser_vm_id", browserVMID, "path", socketPath, "error", err)
		}
	}
}

// PairBrowserVM adds firewall rules to allow traffic between a machine VM and a browser VM.
func (o *firecrackerOrchestrator) PairBrowserVM(machineVMIP, browserVMIP string) error {
	return o.bridge.AllowVMPair(machineVMIP, browserVMIP)
}

// UnpairBrowserVM removes firewall rules between a machine VM and a browser VM.
func (o *firecrackerOrchestrator) UnpairBrowserVM(machineVMIP, browserVMIP string) error {
	o.bridge.RemoveVMPair(machineVMIP, browserVMIP)
	return nil
}

// ListBrowserVMs returns all standalone browser VMs.
func (o *firecrackerOrchestrator) ListBrowserVMs(ctx context.Context) ([]BrowserVMInstance, error) {
	o.browserMu.Lock()
	defer o.browserMu.Unlock()
	var result []BrowserVMInstance
	for _, bvm := range o.browserVMs {
		result = append(result, BrowserVMInstance{
			ID:         bvm.ID,
			VMIP:       bvm.VMIP,
			Status:     bvm.Status,
			CDPPort:    bvm.CDPPort,
			TapDevice:  bvm.TapDevice,
			SocketPath: bvm.SocketPath,
			CDPError:   bvm.CDPError,
		})
	}
	return result, nil
}

type rootfsCopyMode string

const (
	rootfsCopyModeReflink rootfsCopyMode = "reflink"
	rootfsCopyModeFull    rootfsCopyMode = "full"
)

// copyRootfs prefers a CoW reflink copy and optionally refuses the full-copy
// fallback. Kernel Images browser rootfs uses the strict path by default so
// ext4 hosts do not silently pay multi-second or multi-minute copy cost.
func copyRootfs(src, dst string, requireReflink bool) (rootfsCopyMode, error) {
	cmd := exec.Command("cp", "--reflink=always", src, dst)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return rootfsCopyModeReflink, nil
	}

	reflinkErr := strings.TrimSpace(string(out))
	_ = os.Remove(dst)
	if requireReflink {
		return "", fmt.Errorf("reflink copy required but unavailable: cp --reflink=always %s %s: %s: %w", src, dst, reflinkErr, err)
	}

	fallback := exec.Command("cp", "--reflink=auto", "--sparse=always", src, dst)
	fallbackOut, fallbackErr := fallback.CombinedOutput()
	if fallbackErr != nil {
		return "", fmt.Errorf("full-copy fallback failed after reflink failure %q: cp --reflink=auto --sparse=always %s %s: %s: %w", reflinkErr, src, dst, strings.TrimSpace(string(fallbackOut)), fallbackErr)
	}
	return rootfsCopyModeFull, nil
}

// waitForPort waits for a TCP port to become reachable.
func waitForPort(ip string, port int, timeout time.Duration) error {
	return waitForPortContext(context.Background(), ip, port, timeout)
}

func waitForPortContext(ctx context.Context, ip string, port int, timeout time.Duration) error {
	addr := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
	if timeout <= 0 {
		timeout = time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		if err := waitCtx.Err(); err != nil {
			return fmt.Errorf("timeout waiting for %s after %s: %w", addr, timeout, err)
		}
		dialer := net.Dialer{Timeout: 1 * time.Second}
		conn, err := dialer.DialContext(waitCtx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return nil
		}

		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return fmt.Errorf("timeout waiting for %s after %s: %w", addr, timeout, waitCtx.Err())
		case <-timer.C:
		}
	}
}

// generateNonce generates a cryptographically random hex-encoded nonce.
// n is the number of random bytes; the returned string is 2*n hex characters.
func generateNonce(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read crypto/rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// generateMAC generates a deterministic MAC address from a VM IP.
func generateMAC(vmIP string) string {
	ip := net.ParseIP(vmIP).To4()
	if ip == nil {
		return "02:FC:00:00:00:01"
	}
	return fmt.Sprintf("02:FC:00:%02x:%02x:%02x", ip[1], ip[2], ip[3])
}

func (o *firecrackerOrchestrator) ensureOpenClawRuntime(ctx context.Context, selection *metadata.RuntimeSelection) error {
	if selection == nil {
		return nil
	}

	version := strings.TrimSpace(selection.ResolvedOpenClawVersion)
	if version == "" {
		return nil
	}

	if err := rootfs.ValidateOpenClawVersion(version); err != nil {
		return err
	}

	hostRelease := o.openClawReleasePath(version)
	if openClawHostReleaseReady(hostRelease) {
		o.setMountedOpenClawRuntimePaths(selection)
		return nil
	}

	// Prefer manifest URI from control plane (in RuntimeSelection), fall back to agent config.
	manifestURI := strings.TrimSpace(selection.OpenClawManifestURI)
	if manifestURI == "" {
		manifestURI = strings.TrimSpace(o.cfg.OpenClawManifestURI)
	}
	if manifestURI == "" {
		return fmt.Errorf("required openclaw artifact %s is not staged and no manifest URI configured", version)
	}

	timeout := o.cfg.OpenClawDownloadTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	retries := o.cfg.OpenClawRetryAttempts
	if retries <= 0 {
		retries = 3
	}

	fetcher, err := rootfs.NewOpenClawFetcher(ctx, rootfs.OpenClawConfig{
		ManifestURI:     manifestURI,
		DownloadTimeout: timeout,
		RetryAttempts:   retries,
	})
	if err != nil {
		return fmt.Errorf("required openclaw artifact %s fetcher unavailable: create openclaw fetcher: %w", version, err)
	}
	defer func() { _ = fetcher.Close() }()

	releasePath, err := fetcher.EnsureRelease(ctx, o.cfg.OpenClawRuntimeDir, version)
	if err != nil {
		return fmt.Errorf("required openclaw artifact %s is not staged or fetchable: stage openclaw %s: %w", version, version, err)
	}
	o.setMountedOpenClawRuntimePaths(selection)

	slog.Info("vm.runtime.openclaw.staged",
		"version", version,
		"release_path", releasePath)
	return nil
}

func (o *firecrackerOrchestrator) setMountedOpenClawRuntimePaths(selection *metadata.RuntimeSelection) {
	if selection == nil {
		return
	}

	entrypointRelpath := "bin/openclaw"
	pluginsRelpath := "dist/extensions"
	if releasePath := o.openClawReleasePath(selection.ResolvedOpenClawVersion); releasePath != "" {
		paths := rootfs.RuntimePathsForRelease(releasePath)
		entrypointRelpath = paths.EntrypointRelpath
		pluginsRelpath = paths.BundledPluginsRelpath
	}

	if strings.TrimSpace(selection.OpenClawBin) == "" {
		selection.OpenClawBin = filepath.Join("/ocm-runtime", entrypointRelpath)
	}
	if strings.TrimSpace(selection.OpenClawBundledPluginsDir) == "" {
		selection.OpenClawBundledPluginsDir = filepath.Join("/ocm-runtime", pluginsRelpath)
	}
}

func (o *firecrackerOrchestrator) ensureHermesRuntime(ctx context.Context, selection *metadata.RuntimeSelection) error {
	if selection == nil {
		return nil
	}

	version := strings.TrimSpace(selection.ResolvedHermesVersion)
	if version == "" {
		return nil
	}
	if err := rootfs.ValidateOpenClawVersion(version); err != nil {
		return err
	}

	hostRelease := o.hermesReleasePath(version)
	if rootfs.HermesReleaseReady(hostRelease) {
		return nil
	}

	manifestURI := strings.TrimSpace(selection.HermesManifestURI)
	if manifestURI == "" {
		manifestURI = strings.TrimSpace(o.cfg.HermesManifestURI)
	}
	if manifestURI == "" {
		return fmt.Errorf("hermes artifact %s is not staged and no manifest URI configured", version)
	}

	timeout := o.cfg.HermesDownloadTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	retries := o.cfg.HermesRetryAttempts
	if retries <= 0 {
		retries = 3
	}

	fetcher, err := rootfs.NewHermesFetcher(ctx, rootfs.HermesConfig{
		ManifestURI:     manifestURI,
		DownloadTimeout: timeout,
		RetryAttempts:   retries,
	})
	if err != nil {
		return fmt.Errorf("create hermes fetcher: %w", err)
	}
	defer func() { _ = fetcher.Close() }()

	releasePath, err := fetcher.EnsureRelease(ctx, o.cfg.HermesRuntimeDir, version)
	if err != nil {
		return fmt.Errorf("stage hermes %s: %w", version, err)
	}
	paths := rootfs.HermesRuntimePathsForRelease(releasePath)
	if strings.TrimSpace(selection.HermesBin) == "" {
		selection.HermesBin = filepath.Join("/opt/hermes", paths.EntrypointRelpath)
	}
	if strings.TrimSpace(selection.HermesVenvDir) == "" {
		selection.HermesVenvDir = filepath.Join("/opt/hermes", paths.VenvRelpath)
	}

	slog.Info("vm.runtime.hermes.staged",
		"version", version,
		"release_path", releasePath)
	return nil
}

// ensureOpenClawReleaseImage creates a read-only ext4 image from the release
// directory if one doesn't already exist. The image is shared across all VMs
// as a read-only Firecracker drive, eliminating the 2GB data volume copy.
func (o *firecrackerOrchestrator) ensureOpenClawReleaseImage(version string) (string, error) {
	releasePath := o.openClawReleasePath(version)
	if releasePath == "" {
		return "", fmt.Errorf("empty release path for version %s", version)
	}
	imgPath := releasePath + ".ext4"

	// Already exists
	if info, err := os.Stat(imgPath); err == nil && info.Size() > 0 {
		return imgPath, nil
	}

	// Calculate directory size for image
	var totalBytes int64
	_ = filepath.WalkDir(releasePath, func(_ string, d fs.DirEntry, _ error) error {
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				totalBytes += info.Size()
			}
		}
		return nil
	})

	// Add 50% buffer for ext4 metadata, inode tables, directory entries.
	// node_modules trees have many small files that consume inodes faster than bytes.
	imgSizeMB := (totalBytes*15)/(1024*1024*10) + 100

	slog.Info("vm.runtime.openclaw.image.creating",
		"version", version,
		"release_path", releasePath,
		"image_path", imgPath,
		"size_mb", imgSizeMB)

	// Create sparse ext4 image
	tmpImg := imgPath + ".tmp"
	if err := exec.Command("truncate", "-s", fmt.Sprintf("%dM", imgSizeMB), tmpImg).Run(); err != nil {
		return "", fmt.Errorf("truncate openclaw image: %w", err)
	}
	// -N 200000: node_modules has ~73K files+dirs, default inode count is too low
	if out, err := exec.Command("mkfs.ext4", "-F", "-m", "0", "-q", "-N", "200000", tmpImg).CombinedOutput(); err != nil {
		_ = os.Remove(tmpImg)
		return "", fmt.Errorf("mkfs.ext4 openclaw image: %s: %w", string(out), err)
	}

	// Mount, copy, unmount
	mountDir, err := os.MkdirTemp("", "ocm-openclaw-img-")
	if err != nil {
		_ = os.Remove(tmpImg)
		return "", fmt.Errorf("create temp mount dir: %w", err)
	}
	defer func() { _ = os.Remove(mountDir) }()

	if out, err := exec.Command("mount", "-o", "loop", tmpImg, mountDir).CombinedOutput(); err != nil {
		_ = os.Remove(tmpImg)
		return "", fmt.Errorf("mount openclaw image: %s: %w", string(out), err)
	}
	defer func() { _ = exec.Command("umount", mountDir).Run() }()

	// Copy release contents into the image
	if out, err := exec.Command("cp", "-a", releasePath+"/.", mountDir+"/").CombinedOutput(); err != nil {
		_ = os.Remove(tmpImg)
		return "", fmt.Errorf("copy release to image: %s: %w", string(out), err)
	}

	_ = exec.Command("umount", mountDir).Run()

	// Atomic rename
	if err := os.Rename(tmpImg, imgPath); err != nil {
		_ = os.Remove(tmpImg)
		return "", fmt.Errorf("rename openclaw image: %w", err)
	}

	slog.Info("vm.runtime.openclaw.image.created",
		"version", version,
		"image_path", imgPath,
		"size_mb", imgSizeMB)
	return imgPath, nil
}

// ensureHermesMachineRuntimeImage creates a writable per-machine Hermes runtime
// drive seeded from the resolved artifact. Hermes supports `hermes update`,
// which runs git/dependency writes in-place, so the VM must not attach the
// shared version cache directly as a read-only drive.
func (o *firecrackerOrchestrator) ensureHermesMachineRuntimeImage(machineID, version string) (string, error) {
	releasePath := o.hermesReleasePath(version)
	if releasePath == "" {
		return "", fmt.Errorf("empty hermes release path for version %s", version)
	}
	machineDir := o.hermesMachineStateDir(machineID)
	if machineDir == "" {
		return "", fmt.Errorf("empty hermes machine runtime state dir for %s", machineID)
	}
	if err := os.MkdirAll(machineDir, 0755); err != nil {
		return "", fmt.Errorf("create hermes machine runtime dir: %w", err)
	}

	imgPath := filepath.Join(machineDir, "runtime.ext4")
	if info, err := os.Stat(imgPath); err == nil && info.Size() > 0 {
		slog.Info("vm.runtime.hermes.machine_image.reusing",
			"machine_id", machineID,
			"version", version,
			"image_path", imgPath)
		return imgPath, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("stat hermes machine runtime image: %w", err)
	}

	var totalBytes int64
	_ = filepath.WalkDir(releasePath, func(_ string, d fs.DirEntry, _ error) error {
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				totalBytes += info.Size()
			}
		}
		return nil
	})
	imgSizeMB := (totalBytes*15)/(1024*1024*10) + 100

	slog.Info("vm.runtime.hermes.machine_image.creating",
		"machine_id", machineID,
		"version", version,
		"release_path", releasePath,
		"image_path", imgPath,
		"size_mb", imgSizeMB)

	tmpImg := imgPath + ".tmp"
	_ = os.Remove(tmpImg)
	if err := exec.Command("truncate", "-s", fmt.Sprintf("%dM", imgSizeMB), tmpImg).Run(); err != nil {
		return "", fmt.Errorf("truncate hermes machine image: %w", err)
	}
	if out, err := exec.Command("mkfs.ext4", "-F", "-m", "0", "-q", "-N", "200000", tmpImg).CombinedOutput(); err != nil {
		_ = os.Remove(tmpImg)
		return "", fmt.Errorf("mkfs.ext4 hermes machine image: %s: %w", string(out), err)
	}

	mountDir, err := os.MkdirTemp("", "ocm-hermes-machine-img-")
	if err != nil {
		_ = os.Remove(tmpImg)
		return "", fmt.Errorf("create temp mount dir: %w", err)
	}
	defer func() { _ = os.Remove(mountDir) }()

	mounted := false
	if out, err := exec.Command("mount", "-o", "loop", tmpImg, mountDir).CombinedOutput(); err != nil {
		_ = os.Remove(tmpImg)
		return "", fmt.Errorf("mount hermes machine image: %s: %w", string(out), err)
	}
	mounted = true
	defer func() {
		if mounted {
			_ = exec.Command("umount", mountDir).Run()
		}
	}()

	if out, err := exec.Command("cp", "-a", releasePath+"/.", mountDir+"/").CombinedOutput(); err != nil {
		_ = os.Remove(tmpImg)
		return "", fmt.Errorf("copy hermes release to machine image: %s: %w", string(out), err)
	}
	if out, err := exec.Command("chown", "-R", "10000:10000", mountDir).CombinedOutput(); err != nil {
		_ = os.Remove(tmpImg)
		return "", fmt.Errorf("chown hermes machine image: %s: %w", string(out), err)
	}

	if out, err := exec.Command("umount", mountDir).CombinedOutput(); err != nil {
		_ = os.Remove(tmpImg)
		return "", fmt.Errorf("umount hermes machine image: %s: %w", string(out), err)
	}
	mounted = false

	if err := os.Rename(tmpImg, imgPath); err != nil {
		_ = os.Remove(tmpImg)
		return "", fmt.Errorf("rename hermes machine image: %w", err)
	}
	if err := os.WriteFile(filepath.Join(machineDir, "seed_version"), []byte(version+"\n"), 0644); err != nil {
		return "", fmt.Errorf("write hermes seed_version: %w", err)
	}

	slog.Info("vm.runtime.hermes.machine_image.created",
		"machine_id", machineID,
		"version", version,
		"image_path", imgPath,
		"size_mb", imgSizeMB)
	return imgPath, nil
}

func openClawHostReleaseReady(releasePath string) bool {
	if releasePath == "" {
		return false
	}
	paths := rootfs.RuntimePathsForRelease(releasePath)
	entrypoint := filepath.Join(releasePath, paths.EntrypointRelpath)
	info, err := os.Stat(entrypoint)
	if err != nil || info.IsDir() {
		return false
	}
	info, err = os.Stat(filepath.Join(releasePath, paths.BundledPluginsRelpath))
	if err != nil || !info.IsDir() {
		return false
	}
	return true
}

// writeDataVolumeConfig mounts the data volume ext4 image via loopback, writes
// first-boot config files (openclaw.json, auth-profiles.json, soul files) when
// needed, syncs the VM-side OpenClaw runtime mirror on every boot, and persists
// the metadata nonce. Returns the nonce (loaded or generated) and any error.
// If openclaw.json already exists on the volume, config files are not
// overwritten, but runtime pointer files are still refreshed from host state.
func (o *firecrackerOrchestrator) writeDataVolumeConfig(machineID, dataVolPath string, cfg *VMConfig) (string, error) {
	mountDir, err := os.MkdirTemp("", "ocm-datavol-")
	if err != nil {
		return "", fmt.Errorf("create temp mount dir: %w", err)
	}
	defer func() { _ = os.Remove(mountDir) }()

	// Mount the ext4 image via loopback
	mountCmd := exec.Command("mount", "-o", "loop", dataVolPath, mountDir)
	if out, err := mountCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("mount data volume: %s: %w", string(out), err)
	}
	defer func() {
		umountCmd := exec.Command("umount", mountDir)
		if out, err := umountCmd.CombinedOutput(); err != nil {
			slog.Warn("vm.datavol_config.umount_failed",
				"machine_id", machineID,
				"mount_dir", mountDir,
				"error", fmt.Sprintf("%s: %s", err, string(out)))
		}
	}()

	openclawDir := filepath.Join(mountDir, "home", "openclaw", ".openclaw")
	configCurrentDir := filepath.Join(openclawDir, "config-current")
	nonceFile := filepath.Join(mountDir, ".ocm-nonce")

	// Load or generate nonce
	nonce, err := loadOrGenerateNonce(nonceFile)
	if err != nil {
		return "", fmt.Errorf("load/generate nonce: %w", err)
	}

	if runtimeMachineKind(cfg.MachineKind, cfg.RuntimeSelection) == "hermes" {
		return o.writeHermesDataVolumeConfig(mountDir, machineID, nonce, cfg)
	}

	// Check if config already exists (not first boot) — skip writing config files
	openclawJSON := filepath.Join(openclawDir, "openclaw.json")
	openclawConfigJSON := filepath.Join(configCurrentDir, "openclaw.json")
	configExists := false
	if _, err := os.Lstat(openclawJSON); err == nil {
		configExists = true
		slog.Info("vm.datavol_config.exists",
			"machine_id", machineID,
			"note", "openclaw.json exists, skipping config write")
	} else if err != nil && !os.IsNotExist(err) {
		return nonce, fmt.Errorf("stat openclaw.json: %w", err)
	} else if _, err := os.Stat(openclawConfigJSON); err == nil {
		configExists = true
		slog.Info("vm.datavol_config.exists",
			"machine_id", machineID,
			"note", "config-current/openclaw.json exists, skipping config write")
	} else if err != nil && !os.IsNotExist(err) {
		return nonce, fmt.Errorf("stat config-current/openclaw.json: %w", err)
	}

	if !configExists {
		slog.Info("vm.datavol_config.writing", "machine_id", machineID)

		// Create directory structure
		workspaceDir := filepath.Join(openclawDir, "workspace")
		dirs := []string{openclawDir, configCurrentDir, workspaceDir}
		for _, dir := range dirs {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nonce, fmt.Errorf("create dir %s: %w", dir, err)
			}
			if err := os.Chown(dir, 1000, 1000); err != nil {
				slog.Warn("vm.datavol_config.chown_failed", "path", dir, "error", err)
			}
		}

		// Also chown parent dirs up to home/openclaw
		for _, dir := range []string{
			filepath.Join(mountDir, "home"),
			filepath.Join(mountDir, "home", "openclaw"),
		} {
			if err := os.Chown(dir, 1000, 1000); err != nil {
				slog.Warn("vm.datavol_config.chown_failed", "path", dir, "error", err)
			}
		}

		// Write openclaw.json
		if len(cfg.OpenClawConf) > 0 {
			if err := os.WriteFile(openclawConfigJSON, cfg.OpenClawConf, 0644); err != nil {
				return nonce, fmt.Errorf("write openclaw.json: %w", err)
			}
			if err := os.Chown(openclawConfigJSON, 1000, 1000); err != nil {
				slog.Warn("vm.datavol_config.chown_failed", "path", openclawConfigJSON, "error", err)
			}
			if err := os.Remove(openclawJSON); err != nil && !os.IsNotExist(err) {
				return nonce, fmt.Errorf("remove stale openclaw.json: %w", err)
			}
			if err := os.Symlink(filepath.Join("config-current", "openclaw.json"), openclawJSON); err != nil {
				return nonce, fmt.Errorf("symlink openclaw.json: %w", err)
			}
			if err := os.Lchown(openclawJSON, 1000, 1000); err != nil {
				slog.Warn("vm.datavol_config.chown_failed", "path", openclawJSON, "error", err)
			}
		}

		// Write auth-profiles.json
		providerNames := extractProviderNames(cfg.OpenClawConf)
		if len(providerNames) > 0 {
			authProfilesBytes := generateAuthProfiles(providerNames, nonce)
			authProfilesPath := filepath.Join(configCurrentDir, "auth-profiles.json")
			if err := os.WriteFile(authProfilesPath, authProfilesBytes, 0644); err != nil {
				return nonce, fmt.Errorf("write auth-profiles.json: %w", err)
			}
			if err := os.Chown(authProfilesPath, 1000, 1000); err != nil {
				slog.Warn("vm.datavol_config.chown_failed", "path", authProfilesPath, "error", err)
			}
			slog.Info("vm.datavol_config.auth_profiles",
				"machine_id", machineID,
				"providers", providerNames)
		}

		// Write soul files
		for _, soul := range cfg.Souls {
			if soul.Path == "" || soul.Content == "" {
				continue
			}
			// Soul paths are absolute (e.g. /home/openclaw/.openclaw/workspace/SOUL.md).
			// Rebase them under the mount directory.
			relPath := strings.TrimPrefix(soul.Path, "/")
			soulPath := filepath.Join(mountDir, relPath)
			soulDir := filepath.Dir(soulPath)
			if err := os.MkdirAll(soulDir, 0755); err != nil {
				slog.Warn("vm.datavol_config.soul_dir_failed",
					"machine_id", machineID,
					"path", soulDir,
					"error", err)
				continue
			}
			if err := os.Chown(soulDir, 1000, 1000); err != nil {
				slog.Warn("vm.datavol_config.chown_failed", "path", soulDir, "error", err)
			}
			if err := os.WriteFile(soulPath, []byte(soul.Content), 0644); err != nil {
				slog.Warn("vm.datavol_config.soul_write_failed",
					"machine_id", machineID,
					"path", soulPath,
					"error", err)
				continue
			}
			if err := os.Chown(soulPath, 1000, 1000); err != nil {
				slog.Warn("vm.datavol_config.chown_failed", "path", soulPath, "error", err)
			}
		}
	}

	// For artifact runtime, the release is mounted as a separate read-only drive
	// (no data volume copy needed). Just set the metadata paths.
	if cfg.RuntimeSelection != nil {
		o.setMountedOpenClawRuntimePaths(cfg.RuntimeSelection)
	}

	slog.Info("vm.datavol_config.written",
		"machine_id", machineID,
		"souls", len(cfg.Souls))

	return nonce, nil
}

func (o *firecrackerOrchestrator) writeHermesDataVolumeConfig(mountDir, machineID, nonce string, cfg *VMConfig) (string, error) {
	hermesDir := filepath.Join(mountDir, "hermes")
	configPath := filepath.Join(hermesDir, "config.yaml")
	configExists := false
	if _, err := os.Lstat(configPath); err == nil {
		configExists = true
		slog.Info("vm.datavol_config.hermes.exists",
			"machine_id", machineID,
			"note", "config.yaml exists, skipping config write")
	} else if err != nil && !os.IsNotExist(err) {
		return nonce, fmt.Errorf("stat hermes config.yaml: %w", err)
	}

	dirs := []string{
		hermesDir,
		filepath.Join(hermesDir, "cron"),
		filepath.Join(hermesDir, "sessions"),
		filepath.Join(hermesDir, "logs"),
		filepath.Join(hermesDir, "hooks"),
		filepath.Join(hermesDir, "memories"),
		filepath.Join(hermesDir, "skills"),
		filepath.Join(hermesDir, "skins"),
		filepath.Join(hermesDir, "plans"),
		filepath.Join(hermesDir, "workspace"),
		filepath.Join(hermesDir, "home"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nonce, fmt.Errorf("create hermes dir %s: %w", dir, err)
		}
		if err := os.Chown(dir, 10000, 10000); err != nil {
			slog.Warn("vm.datavol_config.hermes.chown_failed", "path", dir, "error", err)
		}
	}

	if !configExists {
		files := []struct {
			path string
			data []byte
			mode os.FileMode
		}{
			{filepath.Join(hermesDir, "config.yaml"), firstNonEmptyBytes(cfg.HermesConfigYAML, []byte("model:\n  provider: auto\nplatforms:\n  api_server:\n    enabled: true\n    host: 127.0.0.1\n    port: 18789\n")), 0644},
			{filepath.Join(hermesDir, ".env"), cfg.HermesEnv, 0600},
			{filepath.Join(hermesDir, "auth.json"), firstNonEmptyBytes(cfg.HermesAuthJSON, []byte("{}")), 0600},
			{filepath.Join(hermesDir, "SOUL.md"), firstNonEmptyBytes(cfg.HermesSoul, []byte("You are a concise, careful AI assistant running inside a managed Hermes Machine.\n")), 0644},
		}
		for _, file := range files {
			if err := os.WriteFile(file.path, file.data, file.mode); err != nil {
				return nonce, fmt.Errorf("write hermes file %s: %w", filepath.Base(file.path), err)
			}
			if err := os.Chown(file.path, 10000, 10000); err != nil {
				slog.Warn("vm.datavol_config.hermes.chown_failed", "path", file.path, "error", err)
			}
		}
	}

	if cfg.RuntimeSelection != nil {
		if strings.TrimSpace(cfg.RuntimeSelection.HermesBin) == "" {
			cfg.RuntimeSelection.HermesBin = "/opt/hermes/.venv/bin/hermes"
		}
		if strings.TrimSpace(cfg.RuntimeSelection.HermesVenvDir) == "" {
			cfg.RuntimeSelection.HermesVenvDir = "/opt/hermes/.venv"
		}
	}

	slog.Info("vm.datavol_config.hermes.written", "machine_id", machineID)
	return nonce, nil
}

func firstNonEmptyBytes(values ...[]byte) []byte {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func (o *firecrackerOrchestrator) syncOpenClawRuntime(mountDir, machineID string, selection *metadata.RuntimeSelection) error {
	if selection == nil {
		return nil
	}

	version := strings.TrimSpace(selection.ResolvedOpenClawVersion)
	runtimeSource := strings.TrimSpace(selection.RuntimeSource)
	if version == "" {
		return fmt.Errorf("artifact runtime requires a resolved openclaw version")
	}
	if err := rootfs.ValidateOpenClawVersion(version); err != nil {
		return err
	}

	hostState, err := o.hostOpenClawRuntimeState(machineID, selection)
	if err != nil {
		return fmt.Errorf("load host openclaw runtime state: %w", err)
	}
	currentHostRelease := o.openClawReleasePath(hostState.CurrentVersion)
	if !openClawHostReleaseReady(currentHostRelease) {
		return fmt.Errorf("openclaw artifact %s is not staged on host", hostState.CurrentVersion)
	}

	runtimeRoot := filepath.Join(mountDir, "ocm", "runtime", "openclaw")
	releasesDir := filepath.Join(runtimeRoot, "releases")
	if err := os.MkdirAll(releasesDir, 0755); err != nil {
		return fmt.Errorf("create runtime releases dir: %w", err)
	}

	currentLink := filepath.Join(runtimeRoot, "current")
	previousLink := filepath.Join(runtimeRoot, "previous")

	if strings.TrimSpace(selection.OpenClawBin) == "" {
		selection.OpenClawBin = "/data/ocm/runtime/openclaw/current/bin/openclaw"
	}
	if strings.TrimSpace(selection.OpenClawBundledPluginsDir) == "" {
		selection.OpenClawBundledPluginsDir = "/data/ocm/runtime/openclaw/current/dist/extensions"
	}

	for _, releaseVersion := range []string{hostState.CurrentVersion, hostState.PreviousVersion} {
		releaseVersion = strings.TrimSpace(releaseVersion)
		if releaseVersion == "" {
			continue
		}
		if err := o.mirrorOpenClawRelease(machineID, releasesDir, releaseVersion); err != nil {
			return err
		}
	}
	mirroredRelease := filepath.Join(releasesDir, hostState.CurrentVersion)
	if !openClawHostReleaseReady(mirroredRelease) {
		return fmt.Errorf("openclaw artifact %s failed to mirror into VM data volume", hostState.CurrentVersion)
	}

	selectedVersion := firstNonEmptyValue(hostState.SelectedVersion, version)
	resolvedVersion := firstNonEmptyValue(hostState.ResolvedVersion, version)
	if err := os.WriteFile(filepath.Join(runtimeRoot, "selected_version"), []byte(selectedVersion+"\n"), 0644); err != nil {
		return fmt.Errorf("write selected_version: %w", err)
	}
	if err := os.WriteFile(filepath.Join(runtimeRoot, "resolved_version"), []byte(resolvedVersion+"\n"), 0644); err != nil {
		return fmt.Errorf("write resolved_version: %w", err)
	}

	if previousVersion := strings.TrimSpace(hostState.PreviousVersion); previousVersion != "" && previousVersion != hostState.CurrentVersion {
		if err := replaceSymlink(previousLink, filepath.Join("releases", previousVersion)); err != nil {
			return fmt.Errorf("update previous symlink: %w", err)
		}
	} else if err := os.RemoveAll(previousLink); err != nil {
		return fmt.Errorf("remove previous symlink: %w", err)
	}
	if err := replaceSymlink(currentLink, filepath.Join("releases", hostState.CurrentVersion)); err != nil {
		return fmt.Errorf("update current symlink: %w", err)
	}

	slog.Info("vm.runtime.openclaw.updated",
		"machine_id", machineID,
		"version", hostState.CurrentVersion,
		"runtime_source", runtimeSource,
		"openclaw_bin", selection.OpenClawBin)
	return nil
}

func (o *firecrackerOrchestrator) openClawReleasePath(version string) string {
	base := strings.TrimSpace(o.cfg.OpenClawRuntimeDir)
	version = strings.TrimSpace(version)
	if base == "" || version == "" {
		return ""
	}
	if err := rootfs.ValidateOpenClawVersion(version); err != nil {
		return ""
	}
	return filepath.Join(base, "releases", version)
}

func (o *firecrackerOrchestrator) hermesReleasePath(version string) string {
	base := strings.TrimSpace(o.cfg.HermesRuntimeDir)
	version = strings.TrimSpace(version)
	if base == "" || version == "" {
		return ""
	}
	if err := rootfs.ValidateOpenClawVersion(version); err != nil {
		return ""
	}
	return filepath.Join(base, "releases", version)
}

func (o *firecrackerOrchestrator) runtimeStateRoot() string {
	if root := strings.TrimSpace(o.cfg.RuntimeStateDir); root != "" {
		return root
	}
	if base := strings.TrimSpace(o.cfg.OpenClawRuntimeDir); base != "" {
		return filepath.Join(filepath.Dir(base), "runtime")
	}
	if stateDir := strings.TrimSpace(o.cfg.StateDir); stateDir != "" {
		return filepath.Join(stateDir, "runtime")
	}
	return ""
}

func (o *firecrackerOrchestrator) openClawMachineStateDir(machineID string) string {
	root := o.runtimeStateRoot()
	machineID = strings.TrimSpace(machineID)
	if root == "" || machineID == "" {
		return ""
	}
	return filepath.Join(root, "machines", machineID, "openclaw")
}

func (o *firecrackerOrchestrator) hermesMachineStateDir(machineID string) string {
	root := o.runtimeStateRoot()
	machineID = strings.TrimSpace(machineID)
	if root == "" || machineID == "" {
		return ""
	}
	return filepath.Join(root, "machines", machineID, "hermes")
}

type openClawMachineRuntimeState struct {
	SelectedVersion string
	ResolvedVersion string
	CurrentVersion  string
	PreviousVersion string
}

func (o *firecrackerOrchestrator) syncHostOpenClawRuntimeState(machineID string, selection *metadata.RuntimeSelection) error {
	if selection == nil {
		return nil
	}

	version := strings.TrimSpace(selection.ResolvedOpenClawVersion)
	if version == "" {
		return nil
	}
	if err := rootfs.ValidateOpenClawVersion(version); err != nil {
		return err
	}

	stateDir := o.openClawMachineStateDir(machineID)
	if stateDir == "" {
		return nil
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("create machine runtime state dir: %w", err)
	}

	currentLink := filepath.Join(stateDir, "current")
	previousLink := filepath.Join(stateDir, "previous")
	currentTarget := o.openClawReleasePath(version)
	if currentTarget == "" {
		return nil
	}

	existingCurrentTarget := ""
	if target, err := os.Readlink(currentLink); err == nil {
		existingCurrentTarget = target
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read current runtime pointer: %w", err)
	}

	if err := os.WriteFile(filepath.Join(stateDir, "selected_version"), []byte(version+"\n"), 0644); err != nil {
		return fmt.Errorf("write host selected_version: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "resolved_version"), []byte(version+"\n"), 0644); err != nil {
		return fmt.Errorf("write host resolved_version: %w", err)
	}

	if existingCurrentTarget != "" && existingCurrentTarget != currentTarget {
		if err := replaceSymlink(previousLink, existingCurrentTarget); err != nil {
			return fmt.Errorf("update host previous runtime pointer: %w", err)
		}
	}
	if err := replaceSymlink(currentLink, currentTarget); err != nil {
		return fmt.Errorf("update host current runtime pointer: %w", err)
	}

	slog.Info("vm.runtime.openclaw.host_state.updated",
		"machine_id", machineID,
		"version", version,
		"state_dir", stateDir)
	return nil
}

// hostOpenClawRuntimeState reads the current host-side runtime state for a machine.
// Callers must call syncHostOpenClawRuntimeState first to ensure state is up-to-date.
func (o *firecrackerOrchestrator) hostOpenClawRuntimeState(machineID string, selection *metadata.RuntimeSelection) (openClawMachineRuntimeState, error) {
	stateDir := o.openClawMachineStateDir(machineID)
	if stateDir == "" {
		version := ""
		if selection != nil {
			version = strings.TrimSpace(selection.ResolvedOpenClawVersion)
		}
		return openClawMachineRuntimeState{
			SelectedVersion: version,
			ResolvedVersion: version,
			CurrentVersion:  version,
		}, nil
	}

	state := openClawMachineRuntimeState{
		SelectedVersion: readRuntimeStateValue(filepath.Join(stateDir, "selected_version")),
		ResolvedVersion: readRuntimeStateValue(filepath.Join(stateDir, "resolved_version")),
		CurrentVersion:  versionFromRuntimeLink(filepath.Join(stateDir, "current")),
		PreviousVersion: versionFromRuntimeLink(filepath.Join(stateDir, "previous")),
	}

	version := ""
	if selection != nil {
		version = strings.TrimSpace(selection.ResolvedOpenClawVersion)
	}
	state.SelectedVersion = firstNonEmptyValue(state.SelectedVersion, version)
	state.ResolvedVersion = firstNonEmptyValue(state.ResolvedVersion, version)
	state.CurrentVersion = firstNonEmptyValue(state.CurrentVersion, state.ResolvedVersion)
	return state, nil
}

func (o *firecrackerOrchestrator) mirrorOpenClawRelease(machineID, releasesDir, version string) error {
	mirroredRelease := filepath.Join(releasesDir, version)
	if _, err := os.Stat(mirroredRelease); err == nil {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat mirrored release: %w", err)
	}

	hostRelease := o.openClawReleasePath(version)
	if hostRelease == "" {
		return nil
	}
	if info, err := os.Stat(hostRelease); err == nil && info.IsDir() {
		if err := copyDirAtomic(hostRelease, mirroredRelease); err != nil {
			return fmt.Errorf("mirror host release %s: %w", hostRelease, err)
		}
		slog.Info("vm.runtime.openclaw.mirrored",
			"machine_id", machineID,
			"version", version,
			"host_release", hostRelease)
		return nil
	}

	slog.Info("vm.runtime.openclaw.release_missing",
		"machine_id", machineID,
		"version", version,
		"host_release", hostRelease)
	return nil
}

func readRuntimeStateValue(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func versionFromRuntimeLink(path string) string {
	target, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	return filepath.Base(filepath.Clean(target))
}

func firstNonEmptyValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func replaceSymlink(linkPath, target string) error {
	if err := os.RemoveAll(linkPath); err != nil {
		return err
	}
	return os.Symlink(target, linkPath)
}

func copyDirAtomic(src, dst string) error {
	parent := filepath.Dir(dst)
	tmp := filepath.Join(parent, "."+filepath.Base(dst)+fmt.Sprintf(".tmp-%d", time.Now().UnixNano()))
	if err := copyDir(src, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		if _, statErr := os.Stat(dst); statErr == nil {
			_ = os.RemoveAll(tmp)
			return nil
		}
		_ = os.RemoveAll(tmp)
		return err
	}
	return nil
}

func copyDir(src, dst string) error {
	cleanSrc, err := filepath.EvalSymlinks(src)
	if err != nil {
		return fmt.Errorf("resolve source root: %w", err)
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch {
		case mode&os.ModeSymlink != 0:
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			// Validate symlink stays within source root to prevent traversal.
			if filepath.IsAbs(linkTarget) {
				return fmt.Errorf("copyDir: absolute symlink %s -> %s not allowed", rel, linkTarget)
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), linkTarget))
			resolvedRel, err := filepath.Rel(cleanSrc, resolved)
			if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("copyDir: symlink %s -> %s escapes source root", rel, linkTarget)
			}
			return os.Symlink(linkTarget, target)
		case info.IsDir():
			if err := os.MkdirAll(target, mode.Perm()); err != nil {
				return err
			}
			return os.Chmod(target, mode.Perm())
		default:
			return copyFile(path, target, mode.Perm())
		}
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, perm)
}

// loadOrGenerateNonce reads a nonce from the given file, or generates a new one
// and writes it to the file if it doesn't exist.
func loadOrGenerateNonce(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		nonce := strings.TrimSpace(string(data))
		if nonce != "" {
			return nonce, nil
		}
	}

	nonce, err := generateNonce(16) // 16 bytes = 32 hex chars
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(nonce+"\n"), 0600); err != nil {
		return "", fmt.Errorf("write nonce file: %w", err)
	}
	return nonce, nil
}

// extractProviderNames parses openclaw.json and returns the provider names
// from models.providers keys.
func extractProviderNames(openClawConf []byte) []string {
	if len(openClawConf) == 0 {
		return nil
	}
	var conf struct {
		Models struct {
			Providers map[string]json.RawMessage `json:"providers"`
		} `json:"models"`
	}
	if err := json.Unmarshal(openClawConf, &conf); err != nil {
		return nil
	}
	names := make([]string, 0, len(conf.Models.Providers))
	for name := range conf.Models.Providers {
		names = append(names, name)
	}
	return names
}

// generateAuthProfiles produces the auth-profiles.json content that the gateway
// uses for model discovery. Each provider gets a profile with the nonce as API key.
func generateAuthProfiles(providerNames []string, nonce string) []byte {
	profiles := make(map[string]interface{}, len(providerNames))
	for _, name := range providerNames {
		// openai-codex uses native OAuth — tokens written by agent after
		// user authentication, not nonce-based proxy auth.
		if name == "openai-codex" {
			continue
		}
		profiles[name+"-proxy"] = map[string]interface{}{
			"type":     "api_key",
			"provider": name,
			"key":      nonce,
		}
	}
	authProfiles := map[string]interface{}{
		"version":  1,
		"profiles": profiles,
	}
	data, err := json.MarshalIndent(authProfiles, "", "  ")
	if err != nil {
		// Should never happen with simple map types
		return []byte("{}")
	}
	return data
}

// vmConsoleWriter returns an io.Writer that logs VM console output.
func vmConsoleWriter(machineID string) io.Writer {
	return &consoleWriter{machineID: machineID}
}

type consoleWriter struct {
	machineID string
	buf       []byte
}

func (w *consoleWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	// Process complete lines
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			// No complete line yet, keep buffering
			// But if buffer gets too large, flush it anyway
			if len(w.buf) > 4096 {
				slog.Debug("vm.console", "machine_id", w.machineID, "line", string(w.buf))
				w.buf = w.buf[:0]
			}
			break
		}
		line := w.buf[:idx]
		// Skip empty lines and trim carriage returns
		line = bytes.TrimRight(line, "\r")
		if len(line) > 0 {
			slog.Debug("vm.console", "machine_id", w.machineID, "line", string(line))
		}
		w.buf = w.buf[idx+1:]
	}
	return len(p), nil
}
