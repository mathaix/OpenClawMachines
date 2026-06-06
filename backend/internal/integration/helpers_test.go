//go:build linux && integration

package integration

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/klauspost/compress/zstd"

	"github.com/mathaix/openclawmachines/backend/internal/agentapi"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/configassembly"
	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/network"
	"github.com/mathaix/openclawmachines/backend/internal/orchestrator"
)

func TestMain(m *testing.M) {
	// Enable debug logging so VM console output is visible
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	// Clean up stale network resources from previous crashed test runs.
	// Without this, leftover bridges/iptables rules cause metadata server
	// routing failures (VMs can't reach the metadata server).
	cleanupStaleTestResources()

	os.Exit(m.Run())
}

// cleanupStaleTestResources removes leftover bridges, taps, and iptables rules
// from previous test runs that may have crashed without proper cleanup.
func cleanupStaleTestResources() {
	// Find and delete stale ocm-test bridges
	out, err := exec.Command("ip", "-o", "link", "show", "type", "bridge").CombinedOutput()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			for _, f := range fields {
				name := strings.TrimSuffix(f, ":")
				if strings.HasPrefix(name, "ocm-test-") {
					slog.Info("cleanup.stale_bridge", "bridge", name)
					// Teardown NAT for this bridge before deleting it
					b := network.NewBridge(name, "192.168.200.0/24", "192.168.200.1")
					b.TeardownNAT()
					exec.Command("ip", "link", "delete", name).Run()
				}
			}
		}
	}

	// Remove any remaining stale iptables rules for the test subnet.
	// These accumulate when tests crash without cleanup.
	iface, err := network.DetectPrimaryInterface()
	if err != nil {
		return
	}
	for {
		if err := exec.Command("iptables", "-t", "nat", "-D", "POSTROUTING",
			"-s", "192.168.200.0/24", "-o", iface, "-j", "MASQUERADE").Run(); err != nil {
			break
		}
		slog.Info("cleanup.stale_nat_rule_removed")
	}
	for {
		if err := exec.Command("iptables", "-D", "FORWARD",
			"-s", "192.168.200.0/24", "-d", "169.254.169.254", "-j", "DROP").Run(); err != nil {
			break
		}
	}
}

const (
	// GCS bucket for VM assets
	gcsBucket = "openclawmachines"

	// Default paths
	defaultKernelPath = "/var/lib/ocm/images/vmlinux"
	defaultImagesDir  = "/var/lib/ocm/images"
	defaultStateDir   = "/tmp/ocm-test"
	defaultSocketDir  = "/tmp/ocm-test/sockets"

	// XFS pre-staged images (created by test-xfs-setup.sh for reflink clones)
	xfsImagesDir  = "/tmp/ocm-test/images"
	xfsKernelPath = "/tmp/ocm-test/images/vmlinux"

	// Timeouts
	vmCreationTimeout = 5 * time.Minute
	wsTestTimeout     = 10 * time.Second
	apiCallTimeout    = 30 * time.Second
)

// TestConfig holds configuration for integration tests.
type TestConfig struct {
	KernelPath              string
	ImagesDir               string
	StateDir                string
	SocketDir               string
	OpenClawManifestURI     string
	HermesManifestURI       string
	HermesRootfsManifestURI string
	BridgeName              string
	AgentToken              string
	RuntimeOwnerKind        string
	TestID                  string // Unique ID for this test run
}

// randomID generates a short random hex string.
func randomID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// getEnvOrDefault returns the environment variable value or a default.
func getEnvOrDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

// newTestConfig creates a new test configuration with unique paths.
// Prefers XFS pre-staged images (from test-xfs-setup.sh) for instant reflink
// clones during rootfs staging. Falls back to ext4 originals.
func newTestConfig(t *testing.T) *TestConfig {
	testID := randomID()
	stateDir := filepath.Join(defaultStateDir, testID)
	socketDir := filepath.Join(stateDir, "sockets")

	// Use XFS pre-staged images if available (enables reflink for rootfs staging)
	imagesDir := defaultImagesDir
	kernelPath := defaultKernelPath
	if _, err := os.Stat(filepath.Join(xfsImagesDir, "rootfs.ext4")); err == nil {
		imagesDir = xfsImagesDir
		t.Log("Using XFS pre-staged images for reflink clones")
	}
	if _, err := os.Stat(xfsKernelPath); err == nil {
		kernelPath = xfsKernelPath
	}

	return &TestConfig{
		KernelPath:              getEnvOrDefault("TEST_KERNEL_PATH", kernelPath),
		ImagesDir:               getEnvOrDefault("TEST_IMAGES_DIR", imagesDir),
		StateDir:                getEnvOrDefault("TEST_STATE_DIR", stateDir),
		SocketDir:               getEnvOrDefault("TEST_SOCKET_DIR", socketDir),
		BridgeName:              getEnvOrDefault("TEST_BRIDGE_NAME", "ocm-test-"+testID[:4]),
		AgentToken:              getEnvOrDefault("TEST_AGENT_TOKEN", "test-token-"+testID),
		RuntimeOwnerKind:        getEnvOrDefault("TEST_VM_RUNTIME_OWNER", "direct"),
		TestID:                  testID,
		OpenClawManifestURI:     getEnvOrDefault("TEST_OPENCLAW_MANIFEST_URI", defaultOpenClawManifestURI),
		HermesManifestURI:       getEnvOrDefault("TEST_HERMES_MANIFEST_URI", defaultHermesManifestURI),
		HermesRootfsManifestURI: getEnvOrDefault("TEST_HERMES_ROOTFS_MANIFEST_URI", defaultHermesRootfsManifestURI),
	}
}

// skipIfNoKVM skips the test if /dev/kvm is not available.
func skipIfNoKVM(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/dev/kvm"); os.IsNotExist(err) {
		t.Skip("Skipping: /dev/kvm not available (requires KVM-enabled host)")
	}
	// Check if we can access it
	f, err := os.Open("/dev/kvm")
	if err != nil {
		t.Skipf("Skipping: cannot access /dev/kvm: %v", err)
	}
	f.Close()
}

// skipIfNoFirecracker skips the test if firecracker binary is not in PATH.
func skipIfNoFirecracker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("firecracker"); err != nil {
		t.Skip("Skipping: firecracker binary not found in PATH")
	}
}

// skipIfNotRoot skips the test if not running as root.
func skipIfNotRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("Skipping: requires root privileges for network setup")
	}
}

func skipIfNoSystemdRuntimeOwner(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip("Skipping: systemd-run not available")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		t.Skip("Skipping: systemctl not available")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Skip("Skipping: systemd is not the active init system")
	}
}

// ensureVMAssets downloads kernel and rootfs from GCS if not present.
func ensureVMAssets(t *testing.T, cfg *TestConfig) {
	t.Helper()

	// Check kernel
	if _, err := os.Stat(cfg.KernelPath); os.IsNotExist(err) {
		t.Logf("Kernel not found at %s, downloading from GCS...", cfg.KernelPath)
		if err := downloadFromGCS(t, "vmlinux", cfg.KernelPath); err != nil {
			t.Fatalf("Failed to download kernel: %v", err)
		}
	}

	// Check rootfs
	rootfsPath := filepath.Join(cfg.ImagesDir, "rootfs.ext4")
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		t.Logf("Rootfs not found at %s, downloading from GCS...", rootfsPath)
		// Ensure images directory exists
		if err := os.MkdirAll(cfg.ImagesDir, 0755); err != nil {
			t.Fatalf("Failed to create images dir: %v", err)
		}
		if err := downloadFromGCS(t, "rootfs.ext4", rootfsPath); err != nil {
			t.Fatalf("Failed to download rootfs: %v", err)
		}
	}

	// Patch rootfs with latest source files if stale
	patchRootfsIfStale(t, cfg)
}

// projectRoot returns the absolute path to the repository root by walking up
// from this test file until we find go.mod.
func projectRoot() string {
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			// go.mod is in backend/, project root is one level up
			return filepath.Dir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root — fall back
			return ""
		}
		dir = parent
	}
}

// patchRootfsIfStale checks whether source files (init script, agent binary,
// utility scripts) are newer than what's baked into the rootfs. If so, it
// mounts the rootfs, replaces the stale files, and unmounts. A fingerprint
// file (.rootfs-patched) tracks what was last injected.
func patchRootfsIfStale(t *testing.T, cfg *TestConfig) {
	t.Helper()

	root := projectRoot()
	if root == "" {
		t.Log("rootfs-patch: could not find project root, skipping patch check")
		return
	}

	rootfsPath := filepath.Join(cfg.ImagesDir, "rootfs.ext4")
	fingerprintPath := filepath.Join(cfg.ImagesDir, ".rootfs-patched")

	// Source files that get injected into the rootfs
	type injection struct {
		src  string // source path on host
		dst  string // destination path inside rootfs
		mode os.FileMode
	}

	injections := []injection{
		{filepath.Join(root, "scripts", "init-openclaw.sh"), "/sbin/overlay-init", 0755},
		{filepath.Join(root, "scripts", "ocm-metadata"), "/usr/local/bin/ocm-metadata", 0755},
		{filepath.Join(root, "scripts", "ocm-test-llm"), "/usr/local/bin/ocm-test-llm", 0755},
		{filepath.Join(root, "scripts", "ocm-env"), "/usr/local/bin/ocm-env", 0755},
		{filepath.Join(root, "scripts", "cf-ssh-check"), "/usr/local/bin/cf-ssh-check", 0755},
	}
	svRoot := filepath.Join(root, "rootfs", "sv")
	if err := filepath.Walk(svRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(svRoot, path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0644)
		if info.Mode()&0111 != 0 || filepath.Base(path) == "run" || filepath.Base(path) == "finish" {
			mode = 0755
		}
		injections = append(injections, injection{
			src:  path,
			dst:  filepath.ToSlash(filepath.Join("/etc/sv", relPath)),
			mode: mode,
		})
		return nil
	}); err != nil && !os.IsNotExist(err) {
		t.Fatalf("rootfs-patch: failed to enumerate service definitions: %v", err)
	}
	binRoot := filepath.Join(root, "rootfs", "bin")
	if err := filepath.Walk(binRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(binRoot, path)
		if err != nil {
			return err
		}
		injections = append(injections, injection{
			src:  path,
			dst:  filepath.ToSlash(filepath.Join("/usr/local/bin", relPath)),
			mode: 0755,
		})
		return nil
	}); err != nil && !os.IsNotExist(err) {
		t.Fatalf("rootfs-patch: failed to enumerate rootfs bin files: %v", err)
	}

	// Build the dedicated PTY daemon from source and inject it.
	ocmptydBuildPath := filepath.Join(os.TempDir(), "ocm-test-ocmptyd")
	ocmptydSrcDir := filepath.Join(root, "backend", "cmd", "ocmptyd")
	if _, err := os.Stat(ocmptydSrcDir); err == nil {
		goPath, err := exec.LookPath("go")
		if err == nil {
			needsBuild := true
			if stat, err := os.Stat(ocmptydBuildPath); err == nil {
				srcMod := latestModTime(filepath.Join(root, "backend"))
				if !srcMod.After(stat.ModTime()) {
					needsBuild = false
				}
			}
			if needsBuild {
				t.Log("rootfs-patch: building ocmptyd binary from source...")
				cmd := exec.Command(goPath, "build", "-buildvcs=false", "-o", ocmptydBuildPath, "./cmd/ocmptyd")
				cmd.Dir = filepath.Join(root, "backend")
				cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Logf("rootfs-patch: ocmptyd build failed: %s: %v", out, err)
				}
			}
		}
	}
	if _, err := os.Stat(ocmptydBuildPath); err == nil {
		injections = append(injections, injection{ocmptydBuildPath, "/usr/local/bin/ocmptyd", 0755})
	}

	// Build the authproxy binary from source and inject it.
	authproxyBuildPath := filepath.Join(os.TempDir(), "ocm-test-authproxy")
	authproxySrcDir := filepath.Join(root, "backend", "cmd", "authproxy")
	if _, err := os.Stat(authproxySrcDir); err == nil {
		goPath, err := exec.LookPath("go")
		if err == nil {
			needsBuild := true
			if apStat, err := os.Stat(authproxyBuildPath); err == nil {
				srcMod := latestModTime(filepath.Join(root, "backend"))
				if !srcMod.After(apStat.ModTime()) {
					needsBuild = false
				}
			}
			if needsBuild {
				t.Log("rootfs-patch: building authproxy binary from source...")
				cmd := exec.Command(goPath, "build", "-buildvcs=false", "-o", authproxyBuildPath, "./cmd/authproxy")
				cmd.Dir = filepath.Join(root, "backend")
				cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Logf("rootfs-patch: authproxy build failed: %s: %v", out, err)
				}
			}
		}
	}
	if _, err := os.Stat(authproxyBuildPath); err == nil {
		injections = append(injections, injection{authproxyBuildPath, "/usr/local/bin/authproxy", 0755})
	}

	// Compute fingerprint of all source files
	h := sha256.New()
	for _, inj := range injections {
		data, err := os.ReadFile(inj.src)
		if err != nil {
			// Source file doesn't exist — skip it
			continue
		}
		h.Write([]byte(inj.src))
		h.Write(data)
	}
	currentFingerprint := hex.EncodeToString(h.Sum(nil))

	// Check stored fingerprint
	stored, err := os.ReadFile(fingerprintPath)
	if err == nil && strings.TrimSpace(string(stored)) == currentFingerprint {
		t.Log("rootfs-patch: rootfs is up-to-date")
		return
	}

	t.Log("rootfs-patch: rootfs is STALE, patching with latest source files...")

	// Mount the rootfs
	mountDir, err := os.MkdirTemp("", "rootfs-patch-")
	if err != nil {
		t.Fatalf("rootfs-patch: failed to create mount dir: %v", err)
	}
	defer os.RemoveAll(mountDir)

	out, err := exec.Command("mount", "-o", "loop", rootfsPath, mountDir).CombinedOutput()
	if err != nil {
		t.Fatalf("rootfs-patch: mount failed: %s: %v", out, err)
	}
	defer retryUmount(mountDir, 3)

	// Inject each file
	for _, inj := range injections {
		srcData, err := os.ReadFile(inj.src)
		if err != nil {
			t.Logf("rootfs-patch: skipping %s (not found)", inj.src)
			continue
		}
		dstPath := filepath.Join(mountDir, inj.dst)
		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			t.Fatalf("rootfs-patch: mkdir failed for %s: %v", dstPath, err)
		}
		if err := os.WriteFile(dstPath, srcData, inj.mode); err != nil {
			t.Fatalf("rootfs-patch: write failed for %s: %v", dstPath, err)
		}
		t.Logf("rootfs-patch: injected %s → %s", filepath.Base(inj.src), inj.dst)
	}

	// Update /etc/ocm-versions.json with patch info so `cat` shows what's actually running
	versionsPath := filepath.Join(mountDir, "etc", "ocm-versions.json")
	patchInfo := map[string]interface{}{
		"updated_at": time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	// Read existing versions.json and merge patch info
	if existingData, err := os.ReadFile(versionsPath); err == nil {
		var versions map[string]interface{}
		if err := json.Unmarshal(existingData, &versions); err == nil {
			for k, v := range patchInfo {
				versions[k] = v
			}
			if pretty, err := json.MarshalIndent(versions, "", "  "); err == nil {
				if err := os.WriteFile(versionsPath, append(pretty, '\n'), 0644); err != nil {
					t.Logf("rootfs-patch: warning: failed to write updated versions: %v", err)
				} else {
					t.Log("rootfs-patch: updated /etc/ocm-versions.json")
				}
			}
		}
	}

	// Unmount before writing fingerprint
	if err := retryUmount(mountDir, 3); err != nil {
		t.Fatalf("rootfs-patch: umount failed: %v", err)
	}

	// Write fingerprint
	if err := os.WriteFile(fingerprintPath, []byte(currentFingerprint), 0644); err != nil {
		t.Logf("rootfs-patch: warning: failed to write fingerprint: %v", err)
	}

	t.Log("rootfs-patch: rootfs patched successfully")
}

// retryUmount attempts to unmount a path with retries. If all attempts fail,
// it falls back to a lazy unmount (-l) as a last resort.
func retryUmount(path string, attempts int) error {
	for i := 0; i < attempts; i++ {
		if err := exec.Command("umount", path).Run(); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return exec.Command("umount", "-l", path).Run()
}

// latestModTime returns the most recent modification time of any .go file under dir.
func latestModTime(dir string) time.Time {
	var latest time.Time
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".go") && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	return latest
}

// downloadFromGCS downloads a file from the GCS bucket.
func downloadFromGCS(t *testing.T, filename, destPath string) error {
	t.Helper()

	return downloadFromGCSURI(t, fmt.Sprintf("gs://%s/%s", gcsBucket, filename), destPath)
}

// downloadFromGCSURI downloads a file from a concrete GCS URI.
func downloadFromGCSURI(t *testing.T, gcsPath, destPath string) error {
	t.Helper()

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	t.Logf("Downloading %s to %s...", gcsPath, destPath)

	cmd := exec.Command("gsutil", "cp", gcsPath, destPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gsutil cp failed: %s: %w", string(output), err)
	}

	t.Logf("Downloaded %s successfully", gcsPath)
	return nil
}

func readIntegrationManifest(t *testing.T, envKey, defaultGCSObject string) []byte {
	t.Helper()
	tmpFile := filepath.Join(t.TempDir(), "manifest.json")

	if override := strings.TrimSpace(os.Getenv(envKey)); override != "" {
		if strings.HasPrefix(override, "gs://") {
			if err := downloadFromGCSURI(t, override, tmpFile); err != nil {
				t.Fatalf("download manifest %s: %v", override, err)
			}
			return readFileOrFatal(t, tmpFile, "downloaded manifest")
		}

		localPath := strings.TrimPrefix(override, "file://")
		return readFileOrFatal(t, localPath, "local manifest")
	}

	if err := downloadFromGCS(t, defaultGCSObject, tmpFile); err != nil {
		t.Fatalf("download manifest %s: %v", defaultGCSObject, err)
	}
	return readFileOrFatal(t, tmpFile, "downloaded manifest")
}

func readFileOrFatal(t *testing.T, path, label string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s %s: %v", label, path, err)
	}
	return b
}

// skipIfNoPrereqs checks all prerequisites and skips if any are missing.
// Returns the test config if all prerequisites are met.
func skipIfNoPrereqs(t *testing.T) *TestConfig {
	t.Helper()

	skipIfNoKVM(t)
	skipIfNoFirecracker(t)
	skipIfNotRoot(t)

	cfg := newTestConfig(t)
	ensureVMAssets(t, cfg)

	return cfg
}

// setupTestDirs creates the state and socket directories.
func setupTestDirs(t *testing.T, cfg *TestConfig) {
	t.Helper()

	if err := os.MkdirAll(cfg.StateDir, 0755); err != nil {
		t.Fatalf("Failed to create state dir %s: %v", cfg.StateDir, err)
	}
	if err := os.MkdirAll(cfg.SocketDir, 0755); err != nil {
		t.Fatalf("Failed to create socket dir %s: %v", cfg.SocketDir, err)
	}

	t.Cleanup(func() {
		os.RemoveAll(cfg.StateDir)
	})
}

// setupTestBridge creates an isolated test bridge.
func setupTestBridge(t *testing.T, cfg *TestConfig) *network.Bridge {
	t.Helper()

	bridge := network.NewBridge(cfg.BridgeName, "192.168.200.0/24", "192.168.200.1")

	if err := bridge.Setup(); err != nil {
		t.Fatalf("Failed to setup bridge %s: %v", cfg.BridgeName, err)
	}

	t.Cleanup(func() {
		// Clean up iptables rules BEFORE deleting the bridge
		bridge.TeardownNAT()
		// Clean up bridge
		exec.Command("ip", "link", "delete", cfg.BridgeName).Run()
	})

	return bridge
}

// setupTestOrchestrator creates an orchestrator with test config.
func setupTestOrchestrator(t *testing.T, cfg *TestConfig, bridge *network.Bridge) orchestrator.Orchestrator {
	t.Helper()

	orchCfg := orchestrator.FirecrackerConfig{
		BridgeName:              cfg.BridgeName,
		SocketDir:               cfg.SocketDir,
		RootfsDir:               cfg.ImagesDir,
		StateDir:                cfg.StateDir,
		DataDir:                 filepath.Join(cfg.StateDir, "data"),
		RuntimeStateDir:         filepath.Join(cfg.StateDir, "runtime"),
		OpenClawRuntimeDir:      filepath.Join(cfg.StateDir, "openclaw"),
		OpenClawManifestURI:     cfg.OpenClawManifestURI,
		HermesRuntimeDir:        filepath.Join(cfg.StateDir, "hermes"),
		HermesManifestURI:       cfg.HermesManifestURI,
		KernelPath:              cfg.KernelPath,
		DefaultVCPUs:            1,
		DefaultMemoryMB:         2048,
		RuntimeOwnerKind:        cfg.RuntimeOwnerKind,
		OpenClawDownloadTimeout: 5 * time.Minute,
		OpenClawRetryAttempts:   1,
		HermesDownloadTimeout:   5 * time.Minute,
		HermesRetryAttempts:     1,
	}

	orch, err := orchestrator.New(orchCfg, bridge)
	if err != nil {
		t.Fatalf("create orchestrator: %v", err)
	}
	testOrch := &defaultRuntimeOrchestrator{
		Orchestrator: orch,
		t:            t,
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := testOrch.Drain(ctx); err != nil {
			t.Logf("drain orchestrator during cleanup: %v", err)
		}
		if err := testOrch.Shutdown(ctx); err != nil {
			t.Logf("shutdown orchestrator during cleanup: %v", err)
		}
	})

	return testOrch
}

type defaultRuntimeOrchestrator struct {
	orchestrator.Orchestrator
	t *testing.T
}

func (o *defaultRuntimeOrchestrator) defaultRuntime(cfg *orchestrator.VMConfig) {
	if cfg.RuntimeSelection == nil {
		withDefaultRuntimeSelection(o.t, cfg)
	}
}

func (o *defaultRuntimeOrchestrator) Create(ctx context.Context, cfg orchestrator.VMConfig) error {
	o.defaultRuntime(&cfg)
	return o.Orchestrator.Create(ctx, cfg)
}

func (o *defaultRuntimeOrchestrator) Upgrade(ctx context.Context, cfg orchestrator.VMConfig) error {
	o.defaultRuntime(&cfg)
	return o.Orchestrator.Upgrade(ctx, cfg)
}

func (o *defaultRuntimeOrchestrator) RegisterPending(cfg orchestrator.VMConfig) {
	o.defaultRuntime(&cfg)
	o.Orchestrator.RegisterPending(cfg)
}

// setupTestMetadataServer creates and starts a metadata server on the bridge
// gateway IP, port 80. This is what VMs expect: the init script curls
// http://<default-gateway>/health to find the metadata server.
func setupTestMetadataServer(t *testing.T, bridgeIP string) *metadata.Server {
	t.Helper()
	srv, _ := setupTestMetadataServerWithCancel(t, bridgeIP)
	return srv
}

// setupTestMetadataServerWithCancel is like setupTestMetadataServer but also
// returns a stop function that shuts down the server immediately. Use this
// when a test needs to simulate a metadata server restart (stop the first
// server, start a second one on the same port).
func setupTestMetadataServerWithCancel(t *testing.T, bridgeIP string) (*metadata.Server, func()) {
	t.Helper()

	srv := metadata.New(bridgeIP, 80)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := srv.Start(ctx); err != nil && ctx.Err() == nil {
			t.Logf("Metadata server error: %v", err)
		}
	}()

	t.Cleanup(func() {
		cancel()
	})

	// Wait for server to be ready
	time.Sleep(200 * time.Millisecond)

	stop := func() {
		cancel()
		// Wait for the listener to release the port
		time.Sleep(200 * time.Millisecond)
	}
	return srv, stop
}

// setupTestAgentServer creates agent API servers for testing.
func setupTestAgentServer(t *testing.T, cfg *TestConfig, orch orchestrator.Orchestrator) (*agentapi.Server, *http.Server, *http.Server) {
	t.Helper()

	srv := agentapi.NewServer(cfg.AgentToken, orch, "", nil, "", nil, nil, nil, false, nil, "")

	// Start control server on random port
	controlListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create control listener: %v", err)
	}
	controlServer := &http.Server{Handler: srv.ControlRouter()}
	go controlServer.Serve(controlListener)

	// Start proxy server on random port
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create proxy listener: %v", err)
	}
	proxyServer := &http.Server{Handler: srv.ProxyRouter()}
	go proxyServer.Serve(proxyListener)

	t.Cleanup(func() {
		controlServer.Close()
		proxyServer.Close()
	})

	t.Logf("Control API: %s, Proxy API: %s", controlListener.Addr(), proxyListener.Addr())

	return srv, controlServer, proxyServer
}

// waitForVMReady polls until the VM is healthy or timeout.
func waitForVMReady(t *testing.T, orch orchestrator.Orchestrator, machineID string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		vm, err := orch.Get(context.Background(), machineID)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if vm.Status == "running" {
			t.Logf("VM %s is ready", machineID)
			return
		}
		if vm.Status == "error" {
			t.Fatalf("VM %s failed: %s", machineID, vm.ErrorMessage)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("VM %s did not become ready within %s", machineID, timeout)
}

// testWebSocketTTY performs a full terminal interaction test against the PTY server.
func testWebSocketTTY(t *testing.T, wsURL string, proxyToken string) {
	t.Helper()

	// Build URL with token
	if !strings.Contains(wsURL, "?") {
		wsURL += "?token=" + proxyToken
	} else {
		wsURL += "&token=" + proxyToken
	}

	// Connect to PTY server (no subprotocol required)
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	t.Logf("Connecting to WebSocket: %s", wsURL)
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer conn.Close()

	// Send initial resize
	resizeMsg := `1{"columns":1000,"rows":50}`
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte(resizeMsg)); err != nil {
		t.Fatalf("Failed to send resize: %v", err)
	}
	t.Log("Sent initial resize message")

	// Generate unique marker for this test
	marker := fmt.Sprintf("TEST_MARKER_%s", randomID())

	// Send echo command
	cmd := fmt.Sprintf("0echo %s\n", marker)
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte(cmd)); err != nil {
		t.Fatalf("Failed to send command: %v", err)
	}
	t.Logf("Sent command: echo %s", marker)

	// Read messages and look for our marker
	conn.SetReadDeadline(time.Now().Add(wsTestTimeout))
	foundMarker := false
	for i := 0; i < 50; i++ {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Logf("Read ended after %d messages: %v", i, err)
			break
		}

		// PTY server sends output with '0' prefix
		if len(data) > 0 && data[0] == '0' {
			output := string(data[1:])
			if strings.Contains(output, marker) {
				foundMarker = true
				t.Logf("Found marker in output: %s", strings.TrimSpace(output))
				break
			}
		}
	}

	if !foundMarker {
		t.Error("Did not receive echo output with marker")
	}

	// Send new resize to verify terminal still responsive
	newResize := `1{"columns":120,"rows":40}`
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte(newResize)); err != nil {
		t.Errorf("Failed to send second resize: %v", err)
	}
	t.Log("Sent second resize message - terminal is responsive")
}

// allocateTestIP returns a unique IP for testing based on index.
func allocateTestIP(index int) string {
	return fmt.Sprintf("192.168.200.%d", 10+index)
}

// generateTestVMConfig creates a VMConfig for testing.
// Uses 2048MB RAM — Node.js V8 needs >1GB for clawdbot gateway heap.
// Enables Quick Start mode to skip heavyweight gateway sidecars (channels,
// browser, cron, Bonjour, Gmail) for ~60→15-30s boot-to-ready.
// generateTestSigningKey creates a random base64-encoded 32-byte signing key for machine tokens.
func generateTestSigningKey() string {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("failed to generate signing key: " + err.Error())
	}
	return base64.StdEncoding.EncodeToString(key)
}

// defaultOpenClawManifestURI is the GCS manifest used for all integration tests.
const defaultOpenClawManifestURI = "gs://openclawmachines/openclaw/manifest-stable.json"
const defaultHermesManifestURI = "gs://openclawmachines/hermes/manifest-stable.json"
const defaultHermesRootfsManifestURI = "gs://openclawmachines/hermes-rootfs/manifest.json"

// cachedStableVersion caches the stable OpenClaw version across tests.
var (
	cachedStableVersion     string
	cachedStableVersionOnce sync.Once
)

// getStableOpenClawVersion discovers and caches the current stable OpenClaw version from GCS.
func getStableOpenClawVersion(t *testing.T) string {
	t.Helper()
	cachedStableVersionOnce.Do(func() {
		cachedStableVersion = discoverStableOpenClawVersion(t)
	})
	if cachedStableVersion == "" {
		t.Fatal("failed to discover stable OpenClaw version")
	}
	return cachedStableVersion
}

func generateTestVMConfig(index int) orchestrator.VMConfig {
	machineID := fmt.Sprintf("test-vm-%s", randomID())
	gatewayToken := "gateway-token-" + randomID()

	// Use the real assembler to produce the OpenClawConf — same code path as production.
	// This ensures the config served by the metadata endpoint is valid for openclaw's
	// Zod schema. The assembler injects platform defaults (auth, controlUi, reload, skills)
	// and explicitly excludes port/bind (those are CLI flags only).
	openClawConf, err := configassembly.AssembleConfig(configassembly.AssemblyParams{
		MachineID: machineID,
	})
	if err != nil {
		panic(fmt.Sprintf("assembler failed for test VM: %v", err))
	}
	slug := fmt.Sprintf("test-vm-%d", index)
	return orchestrator.VMConfig{
		MachineID:       machineID,
		MachineName:     fmt.Sprintf("Test VM %d", index),
		MachineSlug:     slug,
		VCPUs:           1,
		MemoryMB:        2048,
		VMIP:            allocateTestIP(index),
		GatewayToken:    gatewayToken,
		ProxyToken:      "proxy-token-" + randomID(),
		SigningKey:      generateTestSigningKey(),
		TunnelToken:     "test-tunnel-token-" + randomID(), // init script requires non-empty
		VmHostname:      fmt.Sprintf("m-%s.test.openclawmachines.com", slug),
		OwnerEmails:     []string{"test@openclawmachines.com"},
		KernelExtraArgs: "ocm_quick_start=1",
		DataVolumeGB:    2,
		DataVersion:     1,
		OpenClawConf:    openClawConf,
	}
}

// withDefaultRuntimeSelection sets the default artifact RuntimeSelection on a
// VMConfig. Most integration tests need this because the rootfs no longer has
// a baked-in OpenClaw — the runtime must be provided via an artifact drive.
func withDefaultRuntimeSelection(t *testing.T, vmCfg *orchestrator.VMConfig) {
	t.Helper()
	version := getStableOpenClawVersion(t)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: version,
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
		OpenClawManifestURI:     getEnvOrDefault("TEST_OPENCLAW_MANIFEST_URI", defaultOpenClawManifestURI),
	}
	vmCfg.DataVolumeGB = 5 // artifact needs ~2GB+ when mirrored
}

const testOpenClawEntrypointScript = `#!/bin/sh
set -eu

cmd="${1:-}"
if [ $# -gt 0 ]; then
	shift
fi

case "$cmd" in
doctor)
	exit 0
	;;
plugins)
	if [ "${1:-}" = "list" ]; then
		printf '%s\n' \
			'memory-core loaded' \
			'composio loaded' \
			'opik-openclaw loaded'
		exit 0
	fi
	;;
gateway)
	port=18789
	while [ $# -gt 0 ]; do
		case "$1" in
		--port)
			port="${2:-18789}"
			shift 2
			;;
		*)
			shift
			;;
		esac
	done
	exec python3 - "$port" <<'PY'
import http.server
import socketserver
import sys

port = int(sys.argv[1])

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.startswith("/health"):
            body = b'{"ok":true,"status":"ok"}\n'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_response(404)
        self.end_headers()

    def log_message(self, fmt, *args):
        return

socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("127.0.0.1", port), Handler) as server:
    server.serve_forever()
PY
	;;
esac

echo "unsupported test openclaw command: $cmd" >&2
exit 2
`

func stageTestOpenClawRuntime(t *testing.T, cfg *TestConfig, version string) string {
	t.Helper()

	releaseDir := filepath.Join(cfg.StateDir, "openclaw", "releases", version)
	binDir := filepath.Join(releaseDir, "bin")
	extDir := filepath.Join(releaseDir, "dist", "extensions")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("create staged runtime bin dir: %v", err)
	}
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatalf("create staged runtime extensions dir: %v", err)
	}

	openclawBin := filepath.Join(binDir, "openclaw")
	if err := os.WriteFile(openclawBin, []byte(testOpenClawEntrypointScript), 0755); err != nil {
		t.Fatalf("write staged runtime wrapper: %v", err)
	}

	return releaseDir
}

type testOpenClawArtifactSpec struct {
	EntrypointScript  string
	SeedBundledPlugin bool
	ExtraFiles        map[string]string
}

func stageTestOpenClawRuntimeArtifact(t *testing.T, cfg *TestConfig, version string) string {
	return stageTestOpenClawRuntimeArtifactWithSpec(t, cfg, version, testOpenClawArtifactSpec{
		EntrypointScript:  testOpenClawEntrypointScript,
		SeedBundledPlugin: true,
		ExtraFiles: map[string]string{
			"release-marker.txt": version + "\n",
		},
	})
}

func stageBrokenTestOpenClawRuntimeArtifact(t *testing.T, cfg *TestConfig, version string) string {
	return stageTestOpenClawRuntimeArtifactWithSpec(t, cfg, version, testOpenClawArtifactSpec{
		EntrypointScript:  "#!/bin/sh\necho broken-artifact-runtime >&2\nexit 1\n",
		SeedBundledPlugin: true,
		ExtraFiles: map[string]string{
			"release-marker.txt": version + "\n",
		},
	})
}

func stageTestOpenClawRuntimeArtifactWithSpec(t *testing.T, cfg *TestConfig, version string, spec testOpenClawArtifactSpec) string {
	t.Helper()

	manifestRoot := filepath.Join(cfg.StateDir, "openclaw-artifacts")
	releaseDir := filepath.Join(manifestRoot, "releases", version)
	if err := os.MkdirAll(releaseDir, 0755); err != nil {
		t.Fatalf("create openclaw artifact release dir: %v", err)
	}

	payloadDir := filepath.Join(releaseDir, "payload")
	if err := os.RemoveAll(payloadDir); err != nil {
		t.Fatalf("reset openclaw artifact payload dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(payloadDir, "bin"), 0755); err != nil {
		t.Fatalf("create openclaw artifact bin dir: %v", err)
	}
	pluginsDir := filepath.Join(payloadDir, "dist", "extensions")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("create openclaw artifact bundled plugins dir: %v", err)
	}

	entrypointScript := spec.EntrypointScript
	if strings.TrimSpace(entrypointScript) == "" {
		entrypointScript = testOpenClawEntrypointScript
	}
	if err := os.WriteFile(filepath.Join(payloadDir, "bin", "openclaw"), []byte(entrypointScript), 0755); err != nil {
		t.Fatalf("write openclaw artifact entrypoint: %v", err)
	}

	if spec.SeedBundledPlugin {
		if err := seedBundledPluginsFixture(pluginsDir); err != nil {
			t.Fatalf("seed bundled plugins fixture: %v", err)
		}
	}

	if len(spec.ExtraFiles) == 0 && !spec.SeedBundledPlugin {
		spec.ExtraFiles = map[string]string{
			"dist/extensions/test.txt": "artifact\n",
		}
	}
	for relPath, contents := range spec.ExtraFiles {
		target := filepath.Join(payloadDir, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatalf("create openclaw artifact extra file dir: %v", err)
		}
		mode := os.FileMode(0644)
		if strings.HasPrefix(relPath, "bin/") {
			mode = 0755
		}
		if err := os.WriteFile(target, []byte(contents), mode); err != nil {
			t.Fatalf("write openclaw artifact extra file %s: %v", relPath, err)
		}
	}

	artifactPath := filepath.Join(releaseDir, "openclaw-"+version+"-linux-amd64.tar.zst")
	if err := writeOpenClawArtifactTarball(artifactPath, payloadDir); err != nil {
		t.Fatalf("write openclaw artifact tarball: %v", err)
	}

	artifactBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read openclaw artifact: %v", err)
	}
	artifactSHA := sha256.Sum256(artifactBytes)

	releaseManifest := fmt.Sprintf(`{
  "schema_version": 1,
  "kind": "openclaw-runtime",
  "version": "%s",
  "artifact_url": "%s",
  "compression": "zstd",
  "sha256": "%s",
  "runtime": {
    "entrypoint_relpath": "bin/openclaw",
    "bundled_plugins_relpath": "dist/extensions"
  }
}
`, version, artifactPath, hex.EncodeToString(artifactSHA[:]))
	if err := os.WriteFile(filepath.Join(releaseDir, "manifest.json"), []byte(releaseManifest), 0644); err != nil {
		t.Fatalf("write openclaw release manifest: %v", err)
	}

	channelManifest := fmt.Sprintf(`{
  "schema_version": 1,
  "kind": "openclaw-channel",
  "channel": "stable",
  "current_version": "%s"
}
`, version)
	cfg.OpenClawManifestURI = filepath.Join(manifestRoot, "manifest-stable.json")
	if err := os.WriteFile(cfg.OpenClawManifestURI, []byte(channelManifest), 0644); err != nil {
		t.Fatalf("write openclaw channel manifest: %v", err)
	}

	return artifactPath
}

func seedBundledPluginsFixture(destDir string) error {
	type bundledPlugin struct {
		ID   string
		Name string
	}

	plugins := []bundledPlugin{
		{ID: "memory-core", Name: "Memory Core"},
		{ID: "composio", Name: "Composio"},
		{ID: "opik-openclaw", Name: "Opik OpenClaw"},
	}

	for _, plugin := range plugins {
		pluginDir := filepath.Join(destDir, plugin.ID)
		if err := os.MkdirAll(pluginDir, 0755); err != nil {
			return err
		}

		manifest := fmt.Sprintf(`{
  "id": %q,
  "name": %q,
  "version": "0.0.1",
  "main": "index.js",
  "configSchema": {
    "type": "object",
    "properties": {}
  }
}
`, plugin.ID, plugin.Name)
		if err := os.WriteFile(filepath.Join(pluginDir, "openclaw.plugin.json"), []byte(manifest), 0644); err != nil {
			return err
		}

		pkg := fmt.Sprintf(`{
  "name": %q,
  "version": "0.0.1",
  "type": "module"
}
`, plugin.ID)
		if err := os.WriteFile(filepath.Join(pluginDir, "package.json"), []byte(pkg), 0644); err != nil {
			return err
		}

		index := fmt.Sprintf("export default function %sPlugin() { return {}; }\n", sanitizePluginIdentifier(plugin.ID))
		if err := os.WriteFile(filepath.Join(pluginDir, "index.js"), []byte(index), 0644); err != nil {
			return err
		}
	}

	return nil
}

func sanitizePluginIdentifier(value string) string {
	replacer := strings.NewReplacer("-", "_", ".", "_", "/", "_")
	return replacer.Replace(value)
}

func writeOpenClawArtifactTarball(artifactPath, payloadDir string) error {
	artifactFile, err := os.Create(artifactPath)
	if err != nil {
		return err
	}
	defer artifactFile.Close()

	zw, err := zstd.NewWriter(artifactFile)
	if err != nil {
		return err
	}
	defer zw.Close()

	tw := tar.NewWriter(zw)
	defer tw.Close()

	return filepath.Walk(payloadDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(payloadDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		headerName := filepath.ToSlash(rel)
		linkTarget := ""
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return err
		}
		hdr.Name = headerName
		if info.IsDir() && !strings.HasSuffix(hdr.Name, "/") {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, file)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		return err
	})
}

// issueMachineToken generates a valid machine token for testing.
func issueMachineToken(t *testing.T, machineID, signingKey string, scopes []string) string {
	t.Helper()
	token, _, err := auth.IssueMachineToken(1, "test@openclawmachines.com", machineID, 1, scopes, signingKey)
	if err != nil {
		t.Fatalf("Failed to issue machine token: %v", err)
	}
	return token
}

// waitForAuthProxy waits until the authproxy health endpoint responds inside the VM.
func waitForAuthProxy(t *testing.T, vmIP string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(fmt.Sprintf("http://%s:8080/health", vmIP))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Logf("Authproxy on %s:8080 is ready", vmIP)
				return
			}
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("Authproxy on %s:8080 did not become ready within %s", vmIP, timeout)
}

// waitForGateway polls until the gateway is reachable through the auth proxy.
// The gateway binds to 127.0.0.1:18789 (loopback), so we probe via the auth proxy
// on port 8080 using a machine token to hit /gateway/health.
func waitForGateway(t *testing.T, vmIP string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		// Check auth proxy health (no token needed) — gateway is behind it
		resp, err := client.Get(fmt.Sprintf("http://%s:8080/health", vmIP))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Logf("Auth proxy on %s:8080 is ready (gateway behind it on loopback)", vmIP)
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("Auth proxy on %s:8080 did not become ready within %s", vmIP, timeout)
}

// waitForGatewayReady polls until the gateway API actually responds through the auth proxy.
// Use this when you need the gateway itself (not just the auth proxy) to be ready.
func waitForGatewayReady(t *testing.T, vmIP, signingKey, machineID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		token := issueMachineToken(t, machineID, signingKey, []string{"gateway"})
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://%s:8080/gateway/health", vmIP), nil)
		req.Header.Set("X-Machine-Token", token)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Logf("Gateway on %s is ready (via auth proxy)", vmIP)
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("Gateway on %s did not become ready (via auth proxy) within %s", vmIP, timeout)
}

// testGatewayHealth performs an HTTP health check against the gateway via the proxy.
func testGatewayHealth(t *testing.T, proxyURL, machineID, proxyToken string) {
	t.Helper()

	url := fmt.Sprintf("%s/proxy/%s/gateway/health?token=%s",
		proxyURL, machineID, proxyToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("Gateway health request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Gateway health returned %d: %s", resp.StatusCode, body)
	}

	t.Logf("Gateway health check passed: %s", string(body))
}

// testGatewayHealthDirect performs a gateway health check through the auth proxy.
// The gateway binds to 127.0.0.1 so it's not directly reachable from the bridge.
func testGatewayHealthDirect(t *testing.T, vmIP, signingKey, machineID string) {
	t.Helper()

	token := issueMachineToken(t, machineID, signingKey, []string{"gateway"})
	url := fmt.Sprintf("http://%s:8080/gateway/health", vmIP)

	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Machine-Token", token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Gateway health (via auth proxy) request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Gateway health (via auth proxy) returned %d: %s", resp.StatusCode, body)
	}

	t.Logf("Gateway health check (via auth proxy) passed: %s", string(body))
}

type testOpenAIUpstream struct {
	mu        sync.Mutex
	requests  int
	lastModel string
	lastPath  string
	reply     string
}

func newTestOpenAIUpstream(reply string) *testOpenAIUpstream {
	return &testOpenAIUpstream{reply: reply}
}

func (u *testOpenAIUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.requests++
	u.lastPath = r.URL.Path

	if r.Method != http.MethodPost || r.URL.Path != "/openai/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	u.lastModel = req.Model

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": u.reply,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     4,
			"completion_tokens": 2,
			"total_tokens":      6,
		},
	})
}

func (u *testOpenAIUpstream) RequestCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.requests
}

func (u *testOpenAIUpstream) LastModel() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastModel
}

func startReachableBridgeHTTPServer(t *testing.T, handler http.Handler) string {
	t.Helper()

	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen for bridge-reachable HTTP server: %v", err)
	}
	server := &http.Server{Handler: handler}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			t.Logf("bridge-reachable server error: %v", err)
		}
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	port := listener.Addr().(*net.TCPAddr).Port
	return "http://192.168.200.1:" + strconv.Itoa(port)
}

func patchOpenClawConfig(t *testing.T, vmCfg *orchestrator.VMConfig, mutate func(map[string]any)) {
	t.Helper()

	var config map[string]any
	if err := json.Unmarshal(vmCfg.OpenClawConf, &config); err != nil {
		t.Fatalf("unmarshal OpenClaw config: %v", err)
	}
	mutate(config)
	updated, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal OpenClaw config: %v", err)
	}
	vmCfg.OpenClawConf = updated
}

func setOpenAIProviderBaseURL(t *testing.T, vmCfg *orchestrator.VMConfig, baseURL string) {
	t.Helper()
	patchOpenClawConfig(t, vmCfg, func(config map[string]any) {
		models := nestedConfigMap(config, "models")
		providers := nestedConfigMap(models, "providers")
		providers["openai"] = map[string]any{
			"baseUrl": strings.TrimRight(baseURL, "/") + "/openai/v1",
			"models":  []any{},
		}
		models["providers"] = providers
		config["models"] = models

		agents := nestedConfigMap(config, "agents")
		defaults := nestedConfigMap(agents, "defaults")
		defaults["model"] = map[string]any{"primary": "openai/gpt-4o-mini"}
		defaults["models"] = map[string]any{"openai/gpt-4o-mini": map[string]any{}}
		agents["defaults"] = defaults
		config["agents"] = agents
	})
}

func enableMemoryCorePlugin(t *testing.T, vmCfg *orchestrator.VMConfig) {
	t.Helper()
	patchOpenClawConfig(t, vmCfg, func(config map[string]any) {
		plugins := nestedConfigMap(config, "plugins")
		slots := nestedConfigMap(plugins, "slots")
		slots["memory"] = "memory-core"
		plugins["slots"] = slots
		config["plugins"] = plugins
	})
}

func nestedConfigMap(parent map[string]any, key string) map[string]any {
	if value, ok := parent[key]; ok {
		if nested, ok := value.(map[string]any); ok {
			return nested
		}
	}
	nested := map[string]any{}
	parent[key] = nested
	return nested
}

func gatewayChatCompletionResponse(t *testing.T, wsURL, proxyToken string) string {
	t.Helper()

	// Use base64-encoded script to avoid terminal \r corruption.
	// Heredocs and long commands get mangled by WebSocket terminal
	// bracket paste mode and 80-col line wrapping.
	// Chat completions go through the provider baseUrl configured in openclaw.json,
	// not through the gateway's WebSocket port. The baseUrl points to the test
	// upstream on the bridge IP (set by setOpenAIProviderBaseURL).
	const oaiScript = `#!/bin/sh
PROXY_URL=$(jq -r '.models.providers.openai.baseUrl // ""' /home/openclaw/.openclaw/openclaw.json 2>/dev/null)
if [ -z "$PROXY_URL" ]; then
  printf 'OAI_RAW=no openai baseUrl in config\nOAI_STATUS=1\nOAI_REPLY=\n'
  exit 0
fi
raw="$(timeout 20 curl -sS "$PROXY_URL/chat/completions" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","max_tokens":16,"messages":[{"role":"user","content":"Say hello"}]}')"
reply="$(printf '%s' "$raw" | jq -r '.choices[0].message.content' 2>/dev/null)"
status=$?
printf 'OAI_RAW=%.200s\n' "$raw"
printf 'OAI_STATUS=%s\n' "$status"
printf 'OAI_REPLY=%s\n' "$reply"
`
	b64 := base64.StdEncoding.EncodeToString([]byte(oaiScript))
	output := testTerminalCommandSplit(t, wsURL, proxyToken,
		fmt.Sprintf("echo %s|base64 -d>/tmp/oai.sh;bash /tmp/oai.sh", b64),
		30*time.Second)

	var (
		status string
		reply  string
		raw    string
	)
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "OAI_STATUS=") {
			status = strings.TrimPrefix(line, "OAI_STATUS=")
		}
		if strings.HasPrefix(line, "OAI_REPLY=") {
			reply = strings.TrimPrefix(line, "OAI_REPLY=")
		}
		if strings.HasPrefix(line, "OAI_RAW=") {
			raw = strings.TrimPrefix(line, "OAI_RAW=")
		}
	}
	if status != "0" {
		t.Fatalf("gateway chat completion failed with status %q, raw=%q: %s", status, raw, output)
	}
	if strings.TrimSpace(reply) == "" {
		t.Fatalf("gateway chat completion returned empty reply, raw=%q: %s", raw, output)
	}
	return reply
}

// collectLogSSE subscribes to the log SSE stream and collects entries.
func collectLogSSE(t *testing.T, proxyURL, machineID, proxyToken string, timeout time.Duration) []string {
	t.Helper()

	url := fmt.Sprintf("%s/proxy/%s/logs?token=%s",
		proxyURL, machineID, proxyToken)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		t.Fatalf("Failed to create log SSE request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Log SSE request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var entries []string
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data: ") {
			entries = append(entries, strings.TrimPrefix(line, "data: "))
		}
	}

	return entries
}

// assertLogContains checks that at least one log entry contains each expected substring.
func assertLogContains(t *testing.T, entries []string, expected []string) {
	t.Helper()
	for _, want := range expected {
		found := false
		for _, entry := range entries {
			if strings.Contains(entry, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected log entry containing %q not found in %d entries", want, len(entries))
		}
	}
}

// testTerminalCommand sends a command via the terminal WebSocket and returns the output.
// It connects, sends the command, reads output until the marker is found or timeout.
func testTerminalCommand(t *testing.T, wsURL, proxyToken, command string, timeout time.Duration) string {
	t.Helper()

	fullURL := wsURL
	if !strings.Contains(fullURL, "?") {
		fullURL += "?token=" + proxyToken
	} else {
		fullURL += "&token=" + proxyToken
	}

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(fullURL, nil)
	if err != nil {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer conn.Close()

	// Send resize
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte(`1{"columns":1000,"rows":50}`)); err != nil {
		t.Fatalf("Failed to send resize: %v", err)
	}

	// Send command with 0-prefix
	marker := fmt.Sprintf("END_MARKER_%s", randomID())
	fullCmd := fmt.Sprintf("0%s; echo %s\n", command, marker)
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte(fullCmd)); err != nil {
		t.Fatalf("Failed to send command: %v", err)
	}

	// Read output until marker
	conn.SetReadDeadline(time.Now().Add(timeout))
	var output strings.Builder
	for i := 0; i < 200; i++ {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if len(data) > 0 && data[0] == '0' {
			text := string(data[1:])
			output.WriteString(text)
			if strings.Contains(text, marker) {
				break
			}
		}
	}

	return output.String()
}

// setupPersistenceStack creates bridge + NAT + metadata + orchestrator for persistence testing.
// Returns orchestrator, proxy URL (from httptest), data dir path, and test config.
// Does NOT create a VM — callers control the VM lifecycle.
func setupPersistenceStack(t *testing.T) (orch orchestrator.Orchestrator, proxyURL string, dataDir string) {
	t.Helper()

	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch = setupTestOrchestrator(t, cfg, bridge)
	orch.SetMetadataRegistrar(metaSrv)

	srv := agentapi.NewServer(cfg.AgentToken, orch, "", nil, "", nil, nil, nil, false, nil, "")
	proxyServer := httptest.NewServer(srv.ProxyRouter())
	t.Cleanup(func() { proxyServer.Close() })

	dataDir = filepath.Join(cfg.StateDir, "data")
	return orch, proxyServer.URL, dataDir
}

// generateTestVMConfigNoPersistence creates a VMConfig with no data volume.
func generateTestVMConfigNoPersistence(index int) orchestrator.VMConfig {
	machineID := fmt.Sprintf("test-vm-%s", randomID())
	slug := fmt.Sprintf("test-vm-%d", index)
	return orchestrator.VMConfig{
		MachineID:       machineID,
		MachineName:     fmt.Sprintf("Test VM %d", index),
		MachineSlug:     slug,
		VCPUs:           1,
		MemoryMB:        2048,
		VMIP:            allocateTestIP(index),
		GatewayToken:    "gateway-token-" + randomID(),
		ProxyToken:      "proxy-token-" + randomID(),
		SigningKey:      generateTestSigningKey(),
		TunnelToken:     "test-tunnel-token-" + randomID(),
		VmHostname:      fmt.Sprintf("m-%s.test.openclawmachines.com", slug),
		OwnerEmails:     []string{"test@openclawmachines.com"},
		KernelExtraArgs: "ocm_quick_start=1",
		DataVolumeGB:    0,
		DataVersion:     0,
	}
}

// testTerminalCommandSplit sends a command and marker as separate WebSocket messages.
// It waits for the shell prompt before sending the command (ensuring shell initialization
// including /etc/profile.d scripts has completed), then sends the command, waits for
// the marker, and returns all collected output.
func testTerminalCommandSplit(t *testing.T, wsURL, proxyToken, command string, timeout time.Duration) string {
	t.Helper()

	fullURL := wsURL
	if !strings.Contains(fullURL, "?") {
		fullURL += "?token=" + proxyToken
	} else {
		fullURL += "&token=" + proxyToken
	}

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(fullURL, nil)
	if err != nil {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer conn.Close()

	// Send resize
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte(`1{"columns":1000,"rows":50}`)); err != nil {
		t.Fatalf("Failed to send resize: %v", err)
	}

	// Wait for shell prompt before sending command.
	// The prompt indicates shell initialization (including /etc/profile.d) is complete.
	conn.SetReadDeadline(time.Now().Add(timeout))
	for i := 0; i < 500; i++ {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("waiting for shell prompt: %v", err)
		}
		if len(data) > 0 && data[0] == '0' {
			text := string(data[1:])
			if strings.Contains(text, "$ ") {
				break
			}
		}
	}

	// Queue the command and marker in one PTY write so the marker only executes
	// after the command completes, even for slower shell snippets.
	marker := fmt.Sprintf("END_MARKER_%s", randomID())
	cmdMsg := fmt.Sprintf("0%s\necho %s\n", command, marker)
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte(cmdMsg)); err != nil {
		t.Fatalf("Failed to send command: %v", err)
	}

	// Read output until marker
	conn.SetReadDeadline(time.Now().Add(timeout))
	var output strings.Builder
	for i := 0; i < 500; i++ {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if len(data) > 0 && data[0] == '0' {
			text := string(data[1:])
			output.WriteString(text)
			if strings.Contains(text, marker) {
				break
			}
		}
	}

	return output.String()
}

// setupFullStack creates bridge + NAT + metadata server + orchestrator + agent server + a VM.
// Waits for the VM and auth proxy to be ready. For tests that also need the gateway,
// use setupFullStackWithGateway instead.
// Returns the proxy server URL, VM config, and orchestrator.
func setupFullStack(t *testing.T) (proxyURL string, vmCfg orchestrator.VMConfig, orch orchestrator.Orchestrator) {
	t.Helper()

	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	// Metadata server must be on bridge gateway IP:80 for VMs to reach it
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)

	orch = setupTestOrchestrator(t, cfg, bridge)
	orch.SetMetadataRegistrar(metaSrv)

	// Create real HTTP server for WebSocket + SSE testing
	srv := agentapi.NewServer(cfg.AgentToken, orch, "", nil, "", nil, nil, nil, false, nil, "")
	proxyServer := httptest.NewServer(srv.ProxyRouter())
	t.Cleanup(func() { proxyServer.Close() })

	// Create and boot VM
	vmCfg = generateTestVMConfig(0)
	ctx := context.Background()

	// Default to artifact runtime — rootfs has no baked openclaw.
	if vmCfg.RuntimeSelection == nil {
		withDefaultRuntimeSelection(t, &vmCfg)
	}

	t.Logf("Creating VM %s with IP %s", vmCfg.MachineID, vmCfg.VMIP)

	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}

	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForAuthProxy(t, vmCfg.VMIP, 30*time.Second)

	return proxyServer.URL, vmCfg, orch
}

// setupFullStackWithGateway is like setupFullStack but also waits for the gateway
// to be fully ready (~60s). Use this for tests that need to hit gateway endpoints.
func setupFullStackWithGateway(t *testing.T) (proxyURL string, vmCfg orchestrator.VMConfig, orch orchestrator.Orchestrator) {
	t.Helper()
	proxyURL, vmCfg, orch = setupFullStack(t)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)
	return proxyURL, vmCfg, orch
}

// skipIfNoBrowserRootfs skips the test if browser rootfs is not available.
func skipIfNoBrowserRootfs(t *testing.T, cfg *TestConfig) {
	t.Helper()
	browserRootfsPath := filepath.Join(cfg.ImagesDir, "browser-rootfs.ext4")
	if _, err := os.Stat(browserRootfsPath); os.IsNotExist(err) {
		t.Skipf("Skipping: browser rootfs not found at %s (build with: make build-browser-rootfs)", browserRootfsPath)
	}
}

// ensureBrowserRootfs checks that browser rootfs exists and patches it if stale.
func ensureBrowserRootfs(t *testing.T, cfg *TestConfig) {
	t.Helper()

	browserRootfsPath := filepath.Join(cfg.ImagesDir, "browser-rootfs.ext4")
	if _, err := os.Stat(browserRootfsPath); os.IsNotExist(err) {
		t.Logf("Browser rootfs not found at %s, attempting download from GCS...", browserRootfsPath)
		if err := downloadFromGCS(t, "browser-rootfs/browser-rootfs.ext4", browserRootfsPath); err != nil {
			t.Skipf("Browser rootfs not available: %v (build with: make build-browser-rootfs)", err)
		}
	}

	patchBrowserRootfsIfStale(t, cfg)
}

// patchBrowserRootfsIfStale patches the browser rootfs with the latest init-browser.sh.
func patchBrowserRootfsIfStale(t *testing.T, cfg *TestConfig) {
	t.Helper()

	root := projectRoot()
	if root == "" {
		return
	}

	browserRootfsPath := filepath.Join(cfg.ImagesDir, "browser-rootfs.ext4")
	fingerprintPath := filepath.Join(cfg.ImagesDir, ".browser-rootfs-patched")

	initScript := filepath.Join(root, "scripts", "init-browser.sh")
	srcData, err := os.ReadFile(initScript)
	if err != nil {
		t.Logf("browser-rootfs-patch: init-browser.sh not found, skipping patch")
		return
	}

	h := sha256.New()
	h.Write([]byte(initScript))
	h.Write(srcData)
	currentFingerprint := hex.EncodeToString(h.Sum(nil))

	stored, err := os.ReadFile(fingerprintPath)
	if err == nil && strings.TrimSpace(string(stored)) == currentFingerprint {
		t.Log("browser-rootfs-patch: browser rootfs is up-to-date")
		return
	}

	t.Log("browser-rootfs-patch: patching browser rootfs with latest init-browser.sh...")

	mountDir, err := os.MkdirTemp("", "browser-rootfs-patch-")
	if err != nil {
		t.Logf("browser-rootfs-patch: failed to create mount dir: %v", err)
		return
	}
	defer os.RemoveAll(mountDir)

	out, err := exec.Command("mount", "-o", "loop", browserRootfsPath, mountDir).CombinedOutput()
	if err != nil {
		t.Logf("browser-rootfs-patch: mount failed: %s: %v", out, err)
		return
	}
	defer retryUmount(mountDir, 3)

	dstPath := filepath.Join(mountDir, "sbin", "overlay-init")
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		t.Logf("browser-rootfs-patch: mkdir failed: %v", err)
		return
	}
	if err := os.WriteFile(dstPath, srcData, 0755); err != nil {
		t.Logf("browser-rootfs-patch: write failed: %v", err)
		return
	}
	t.Logf("browser-rootfs-patch: injected init-browser.sh → /sbin/overlay-init")

	if err := retryUmount(mountDir, 3); err != nil {
		t.Logf("browser-rootfs-patch: umount failed: %v", err)
		return
	}

	os.WriteFile(fingerprintPath, []byte(currentFingerprint), 0644)
	t.Log("browser-rootfs-patch: browser rootfs patched successfully")
}
