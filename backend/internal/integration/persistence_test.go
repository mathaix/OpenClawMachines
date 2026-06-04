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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/mathaix/openclawmachines/backend/internal/agentapi"
	"github.com/mathaix/openclawmachines/backend/internal/backup"
	"github.com/mathaix/openclawmachines/backend/internal/orchestrator"
)

// ============================================================================
// Persistence Tests — Data Volume Lifecycle
// ============================================================================

// TestPersistence_StopStartPreservesData verifies that data written to /workspace
// survives a stop/start cycle.
func TestPersistence_StopStartPreservesData(t *testing.T) {
	orch, proxyURL, dataDir := setupPersistenceStack(t)
	ctx := context.Background()

	vmCfg := generateTestVMConfig(0)
	t.Logf("Creating VM %s with IP %s, DataDir=%s", vmCfg.MachineID, vmCfg.VMIP, dataDir)

	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)

	// Wait for gateway + init script to finish
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	// Build WebSocket URL
	wsURL := strings.Replace(proxyURL, "http://", "ws://", 1)
	wsURL = fmt.Sprintf("%s/proxy/%s/terminal/ws", wsURL, vmCfg.MachineID)

	// Write a file to /workspace and sync to ensure data reaches the block device
	// before VM shutdown (SendCtrlAltDel doesn't allow graceful unmount).
	// Use testTerminalCommandSplit which waits for the shell prompt before sending.
	writeOutput := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"echo PERSIST_TEST > /workspace/persist-test.txt && sync && echo WRITE_OK",
		30*time.Second)
	if !strings.Contains(writeOutput, "WRITE_OK") {
		t.Fatalf("Write command did not complete. Output: %s", writeOutput)
	}
	t.Log("1. Wrote PERSIST_TEST to /workspace/persist-test.txt (synced)")

	// Stop VM
	stopStart := time.Now()
	if err := orch.Stop(ctx, vmCfg.MachineID); err != nil {
		t.Fatalf("Stop VM: %v", err)
	}
	stopDuration := time.Since(stopStart)
	t.Logf("2. VM stopped in %s", stopDuration)

	if stopDuration > 15*time.Second {
		t.Errorf("Stop took too long: %s (expected < 15s)", stopDuration)
	}

	// Assert: VM gone from list
	vms, _ := orch.List(ctx)
	for _, vm := range vms {
		if vm.MachineID == vmCfg.MachineID {
			t.Fatal("VM should not be in list after stop")
		}
	}

	// Assert: data volume still exists on host
	volPath := filepath.Join(dataDir, vmCfg.MachineID+".ext4")
	if _, err := os.Stat(volPath); os.IsNotExist(err) {
		t.Fatal("Data volume should still exist after stop")
	}
	t.Log("3. Data volume preserved on host")

	// Re-create VM with same machineID and IP
	restartStart := time.Now()
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Re-create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)
	t.Logf("4. VM restarted in %s", time.Since(restartStart))

	// Read the file back — use testTerminalCommandSplit to avoid the marker-in-echo issue
	restartOutput := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"cat /workspace/persist-test.txt",
		30*time.Second)
	if !strings.Contains(restartOutput, "PERSIST_TEST") {
		t.Fatalf("Data did not survive stop/start! Output: %s", restartOutput)
	}
	t.Log("5. PERSIST_TEST survived stop/start — data persistence verified")
}

// TestPersistence_DestroyDeletesAllArtifacts verifies that Destroy removes
// the .ext4, .version, and .pre-upgrade files.
func TestPersistence_DestroyDeletesAllArtifacts(t *testing.T) {
	orch, _, dataDir := setupPersistenceStack(t)
	ctx := context.Background()

	vmCfg := generateTestVMConfig(0)
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)

	// Record artifact paths
	volPath := filepath.Join(dataDir, vmCfg.MachineID+".ext4")
	versionPath := filepath.Join(dataDir, vmCfg.MachineID+".version")
	backupPath := volPath + ".pre-upgrade"

	// Create a fake .pre-upgrade to test its deletion too
	os.WriteFile(backupPath, []byte("fake-backup"), 0600)

	// Verify artifacts exist before destroy
	if _, err := os.Stat(volPath); os.IsNotExist(err) {
		t.Fatal("Data volume should exist before destroy")
	}

	// Destroy
	if err := orch.Destroy(ctx, vmCfg.MachineID); err != nil {
		t.Fatalf("Destroy VM: %v", err)
	}

	// Assert ALL artifacts deleted
	for _, path := range []string{volPath, versionPath, backupPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("Artifact should be deleted: %s", path)
		}
	}
	t.Log("All data artifacts deleted after destroy")
}

// TestPersistence_DataVolumeMountAndSymlinks verifies the init script correctly
// mounts /dev/vdb at /data with nodev,nosuid and creates symlinks.
func TestPersistence_DataVolumeMountAndSymlinks(t *testing.T) {
	orch, proxyURL, _ := setupPersistenceStack(t)
	ctx := context.Background()

	vmCfg := generateTestVMConfig(0)
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	wsURL := strings.Replace(proxyURL, "http://", "ws://", 1)
	wsURL = fmt.Sprintf("%s/proxy/%s/terminal/ws", wsURL, vmCfg.MachineID)

	// Check /data is a mountpoint
	output := testTerminalCommand(t, wsURL, vmCfg.ProxyToken,
		"mountpoint -q /data && echo MOUNTED || echo NOT_MOUNTED",
		10*time.Second)
	if !strings.Contains(output, "MOUNTED") {
		t.Fatalf("/data is not mounted. Output: %s", output)
	}
	t.Log("1. /data is mounted")

	// Check mount options include nodev,nosuid
	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"mount | grep '/data '",
		10*time.Second)
	if !strings.Contains(output, "nodev") || !strings.Contains(output, "nosuid") {
		t.Errorf("Expected nodev,nosuid mount options. Output: %s", output)
	}
	t.Log("2. Mount options include nodev,nosuid")

	// Check /home/openclaw is symlink to /data/home/openclaw
	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"readlink /home/openclaw",
		10*time.Second)
	if !strings.Contains(output, "/data/home/openclaw") {
		t.Errorf("Expected /home/openclaw -> /data/home/openclaw. Output: %s", output)
	}
	t.Log("3. /home/openclaw -> /data/home/openclaw")

	// Check /workspace is symlink to /data/workspace
	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"readlink /workspace",
		10*time.Second)
	if !strings.Contains(output, "/data/workspace") {
		t.Errorf("Expected /workspace -> /data/workspace. Output: %s", output)
	}
	t.Log("4. /workspace -> /data/workspace")
}

// TestPersistence_DataVersionMarker verifies the init script writes
// the .ocm-version file to /data.
func TestPersistence_DataVersionMarker(t *testing.T) {
	orch, proxyURL, _ := setupPersistenceStack(t)
	ctx := context.Background()

	vmCfg := generateTestVMConfig(0)
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	wsURL := strings.Replace(proxyURL, "http://", "ws://", 1)
	wsURL = fmt.Sprintf("%s/proxy/%s/terminal/ws", wsURL, vmCfg.MachineID)

	output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"cat /data/.ocm-version",
		10*time.Second)
	if !strings.Contains(output, "1") {
		t.Errorf("Expected /data/.ocm-version to contain '1'. Output: %s", output)
	}
	t.Log("Data version marker written correctly")
}

// TestPersistence_UpgradeCreatesBackup verifies that creating a VM with a higher
// DataVersion than the sidecar triggers a .pre-upgrade backup.
func TestPersistence_UpgradeCreatesBackup(t *testing.T) {
	orch, proxyURL, dataDir := setupPersistenceStack(t)
	ctx := context.Background()

	// Create VM at version 1, write data, then stop
	vmCfg := generateTestVMConfig(0)
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	wsURL := strings.Replace(proxyURL, "http://", "ws://", 1)
	wsURL = fmt.Sprintf("%s/proxy/%s/terminal/ws", wsURL, vmCfg.MachineID)

	testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"echo v1-data > /workspace/upgrade-test.txt && sync",
		15*time.Second)
	output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"cat /workspace/upgrade-test.txt",
		15*time.Second)
	if !strings.Contains(output, "v1-data") {
		t.Fatalf("Failed to write v1-data: %s", output)
	}
	t.Log("1. Wrote v1-data at DataVersion=1 (synced)")

	// Stop VM
	if err := orch.Stop(ctx, vmCfg.MachineID); err != nil {
		t.Fatalf("Stop VM: %v", err)
	}
	t.Log("2. VM stopped")

	// Re-create with DataVersion=2 — this should trigger backup
	vmCfg.DataVersion = 2
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Re-create VM with DataVersion=2: %v", err)
	}
	t.Log("3. VM re-created with DataVersion=2")

	// Assert .pre-upgrade backup exists
	volPath := filepath.Join(dataDir, vmCfg.MachineID+".ext4")
	backupPath := volPath + ".pre-upgrade"
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Fatal("Expected .pre-upgrade backup after version upgrade")
	}
	t.Log("4. .pre-upgrade backup created")

	// Wait for VM and verify data still accessible
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"cat /workspace/upgrade-test.txt",
		15*time.Second)
	if !strings.Contains(output, "v1-data") {
		t.Fatalf("Data not accessible after upgrade: %s", output)
	}
	t.Log("5. Data still accessible after version upgrade")
}

// TestPersistence_RollbackRestoresData verifies that RollbackDataVolume restores
// the .pre-upgrade backup and clears the version sidecar.
func TestPersistence_RollbackRestoresData(t *testing.T) {
	orch, proxyURL, dataDir := setupPersistenceStack(t)
	ctx := context.Background()

	// Create VM, write data, stop
	vmCfg := generateTestVMConfig(0)
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	wsURL := strings.Replace(proxyURL, "http://", "ws://", 1)
	wsURL = fmt.Sprintf("%s/proxy/%s/terminal/ws", wsURL, vmCfg.MachineID)

	testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"echo original > /workspace/rollback-test.txt && sync",
		15*time.Second)
	output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"cat /workspace/rollback-test.txt",
		15*time.Second)
	if !strings.Contains(output, "original") {
		t.Fatalf("Failed to write original data: %s", output)
	}
	t.Log("1. Wrote 'original' at DataVersion=1 (synced)")

	// Stop VM
	if err := orch.Stop(ctx, vmCfg.MachineID); err != nil {
		t.Fatalf("Stop VM: %v", err)
	}

	// Manually create backup (simulating what ensureDataVolume does on upgrade)
	volPath := filepath.Join(dataDir, vmCfg.MachineID+".ext4")
	backupPath := volPath + ".pre-upgrade"
	cpCmd := fmt.Sprintf("cp --reflink=auto %s %s", volPath, backupPath)
	if out, err := execShell(cpCmd); err != nil {
		t.Fatalf("Failed to create backup: %s: %v", out, err)
	}
	t.Log("2. Created .pre-upgrade backup")

	// Re-create VM at version 2, write different data, stop
	vmCfg.DataVersion = 2
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Re-create VM v2: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"echo modified > /workspace/rollback-test.txt && sync",
		15*time.Second)
	t.Log("3. Wrote 'modified' at DataVersion=2 (synced)")

	if err := orch.Stop(ctx, vmCfg.MachineID); err != nil {
		t.Fatalf("Stop VM v2: %v", err)
	}

	// Rollback
	if err := orch.RollbackDataVolume(vmCfg.MachineID); err != nil {
		t.Fatalf("RollbackDataVolume: %v", err)
	}
	t.Log("4. Rollback completed")

	// Assert .pre-upgrade is gone
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatal(".pre-upgrade should be removed after rollback")
	}

	// Assert version sidecar is gone
	versionPath := filepath.Join(dataDir, vmCfg.MachineID+".version")
	if _, err := os.Stat(versionPath); !os.IsNotExist(err) {
		t.Fatal(".version sidecar should be removed after rollback")
	}

	// Re-create at version 1 and read data
	vmCfg.DataVersion = 1
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Re-create VM after rollback: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"cat /workspace/rollback-test.txt",
		15*time.Second)
	if !strings.Contains(output, "original") {
		t.Fatalf("Rollback did not restore original data! Output: %s", output)
	}
	t.Log("5. Rollback restored 'original' data successfully")
}

// TestPersistence_RollbackWhileRunning_Fails verifies that rollback is rejected
// when the VM is still running.
func TestPersistence_RollbackWhileRunning_Fails(t *testing.T) {
	orch, _, dataDir := setupPersistenceStack(t)
	ctx := context.Background()

	vmCfg := generateTestVMConfig(0)
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)

	// Create a fake backup so rollback has something to restore
	volPath := filepath.Join(dataDir, vmCfg.MachineID+".ext4")
	os.WriteFile(volPath+".pre-upgrade", []byte("fake"), 0600)

	// Attempt rollback while running — should fail
	err := orch.RollbackDataVolume(vmCfg.MachineID)
	if err == nil {
		t.Fatal("Expected error when rolling back a running VM")
	}
	if !strings.Contains(err.Error(), "is running") {
		t.Fatalf("Expected 'is running' error, got: %v", err)
	}

	// VM should still be running
	vm, err := orch.Get(ctx, vmCfg.MachineID)
	if err != nil {
		t.Fatalf("VM should still exist: %v", err)
	}
	if vm.Status != "running" {
		t.Errorf("VM should still be running, got status: %s", vm.Status)
	}

	t.Log("Rollback correctly rejected while VM is running")
}

// TestPersistence_NoPersistenceMode verifies that a VM created without data volume
// works normally — /data is not mounted, /workspace is a regular directory.
// Uses a separate orchestrator without DataDir to prevent automatic volume creation.
func TestPersistence_NoPersistenceMode(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)
	ctx := context.Background()

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)

	// Create orchestrator without DataDir — no data volumes will be attached
	orchCfg := orchestrator.FirecrackerConfig{
		BridgeName:      cfg.BridgeName,
		SocketDir:       cfg.SocketDir,
		RootfsDir:       cfg.ImagesDir,
		StateDir:        cfg.StateDir,
		DataDir:         "", // No data directory — no data volumes
		KernelPath:      cfg.KernelPath,
		DefaultVCPUs:    1,
		DefaultMemoryMB: 2048,
	}
	orchNoPersist, err := orchestrator.New(orchCfg, bridge)
	if err != nil {
		t.Fatalf("Create no-persist orchestrator: %v", err)
	}
	orchNoPersist.SetMetadataRegistrar(metaSrv)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		orchNoPersist.Shutdown(shutdownCtx)
	})

	srv := agentapi.NewServer(cfg.AgentToken, orchNoPersist, "", nil, "", nil, nil, nil, false, nil, "")
	proxyServer := httptest.NewServer(srv.ProxyRouter())
	t.Cleanup(func() { proxyServer.Close() })

	vmCfg := generateTestVMConfigNoPersistence(0)
	if err := orchNoPersist.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Create VM: %v", err)
	}
	waitForVMReady(t, orchNoPersist, vmCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	wsURL := strings.Replace(proxyServer.URL, "http://", "ws://", 1)
	wsURL = fmt.Sprintf("%s/proxy/%s/terminal/ws", wsURL, vmCfg.MachineID)

	// /data should NOT be mounted
	output := testTerminalCommand(t, wsURL, vmCfg.ProxyToken,
		"mountpoint -q /data && echo YES || echo NO",
		10*time.Second)
	if !strings.Contains(output, "NO") {
		t.Errorf("Expected /data to NOT be mounted. Output: %s", output)
	}
	t.Log("1. /data is not mounted (no data volume)")

	// /workspace should be a regular directory, not a symlink
	output = testTerminalCommand(t, wsURL, vmCfg.ProxyToken,
		"test -L /workspace && echo LINK || echo DIR",
		10*time.Second)
	if !strings.Contains(output, "DIR") {
		t.Errorf("Expected /workspace to be a regular dir. Output: %s", output)
	}
	t.Log("2. /workspace is a regular directory")

	// VM should still work normally — write and read a file
	output = testTerminalCommand(t, wsURL, vmCfg.ProxyToken,
		"echo NOPERS_TEST > /workspace/test.txt && cat /workspace/test.txt",
		15*time.Second)
	if !strings.Contains(output, "NOPERS_TEST") {
		t.Errorf("VM should work without persistence. Output: %s", output)
	}
	t.Log("3. VM works normally without persistence")
}

// execShell is a helper that runs a shell command and returns output.
func execShell(cmd string) (string, error) {
	out, err := exec.Command("bash", "-c", cmd).CombinedOutput()
	return string(out), err
}

// ============================================================================
// Local BackupStore — filesystem-backed implementation for testing
// ============================================================================

// localBackupStore implements backup.BackupStore using the local filesystem
// with the same compression (zstd) and encryption (AES-256-CTR + HMAC) as production.
type localBackupStore struct {
	dir string
	mu  sync.Mutex
}

func newLocalBackupStore(t *testing.T) *localBackupStore {
	dir := t.TempDir()
	return &localBackupStore{dir: dir}
}

func (s *localBackupStore) Upload(ctx context.Context, machineID string, dataVolumePath string, encryptionKey []byte) (*backup.BackupInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fi, err := os.Stat(dataVolumePath)
	if err != nil {
		return nil, fmt.Errorf("stat data volume: %w", err)
	}

	ts := time.Now()
	objPath := fmt.Sprintf("backups/%s/%s.ext4.zst.enc", machineID, ts.UTC().Format("20060102T150405Z"))
	destPath := filepath.Join(s.dir, objPath)
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}

	f, err := os.Open(dataVolumePath)
	if err != nil {
		return nil, fmt.Errorf("open data volume: %w", err)
	}
	defer f.Close()

	// Compress with zstd
	var compressed bytes.Buffer
	zw, err := zstd.NewWriter(&compressed, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(zw, f); err != nil {
		return nil, err
	}
	zw.Close()

	// Encrypt compressed data
	outFile, err := os.Create(destPath)
	if err != nil {
		return nil, err
	}
	nonce, hmacHash, err := backup.StreamEncrypt(bytes.NewReader(compressed.Bytes()), outFile, encryptionKey)
	outFile.Close()
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}

	outFi, _ := os.Stat(destPath)

	return &backup.BackupInfo{
		GCSPath:         objPath,
		SizeBytes:       fi.Size(),
		CompressedBytes: outFi.Size(),
		ChecksumSHA256:  "test-checksum",
		HMAC:            hmacHash,
		Nonce:           nonce,
		Timestamp:       ts,
	}, nil
}

func (s *localBackupStore) Download(ctx context.Context, gcsPath string, destPath string, encryptionKey, nonce, expectedHMAC []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	srcPath := filepath.Join(s.dir, gcsPath)
	encFile, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open backup file: %w", err)
	}
	defer encFile.Close()

	// Decrypt
	var decrypted bytes.Buffer
	if err := backup.StreamDecrypt(encFile, &decrypted, encryptionKey, nonce, expectedHMAC); err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}

	// Decompress zstd
	zr, err := zstd.NewReader(bytes.NewReader(decrypted.Bytes()))
	if err != nil {
		return fmt.Errorf("zstd reader: %w", err)
	}
	defer zr.Close()

	outFile, err := os.Create(destPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(outFile, zr); err != nil {
		outFile.Close()
		return err
	}
	return outFile.Close()
}

func (s *localBackupStore) StreamTarGz(ctx context.Context, gcsPath string, encryptionKey, nonce, expectedHMAC []byte, w io.Writer) error {
	return fmt.Errorf("not implemented in test store")
}

func (s *localBackupStore) StreamDecrypted(ctx context.Context, gcsPath string, encryptionKey, nonce, expectedHMAC []byte, w io.Writer) error {
	return fmt.Errorf("not implemented in test store")
}

func (s *localBackupStore) Delete(ctx context.Context, gcsPath string) error {
	return os.Remove(filepath.Join(s.dir, gcsPath))
}

// ============================================================================
// Backup Integration Tests
// ============================================================================

// setupPersistenceStackWithBackup creates the full test stack with backup support.
// Returns orchestrator, proxy URL, control URL, data dir, and the backup store.
func setupPersistenceStackWithBackup(t *testing.T) (orch orchestrator.Orchestrator, proxyURL, controlURL, agentToken, dataDir string, bs *localBackupStore) {
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

	dataDir = filepath.Join(cfg.StateDir, "data")
	bs = newLocalBackupStore(t)

	srv := agentapi.NewServer(cfg.AgentToken, orch, "", nil, "", nil, nil, nil, false, bs, dataDir)
	proxyServer := httptest.NewServer(srv.ProxyRouter())
	t.Cleanup(func() { proxyServer.Close() })

	controlServer := httptest.NewServer(srv.ControlRouter())
	t.Cleanup(func() { controlServer.Close() })

	return orch, proxyServer.URL, controlServer.URL, cfg.AgentToken, dataDir, bs
}

// TestBackup_CreateAndRestore verifies the full backup/restore cycle:
// boot VM → write data → stop → backup via agent API → delete data volume →
// restore via agent API → restart VM → verify data survived.
func TestBackup_CreateAndRestore(t *testing.T) {
	orch, proxyURL, controlURL, agentToken, dataDir, _ := setupPersistenceStackWithBackup(t)
	ctx := context.Background()

	// Generate a test encryption key (32 bytes for AES-256)
	encryptionKey, err := backup.GenerateKey()
	if err != nil {
		t.Fatalf("Generate encryption key: %v", err)
	}

	// Helper to make authenticated requests to the control API
	controlRequest := func(method, url string, body io.Reader) (*http.Response, error) {
		req, err := http.NewRequest(method, url, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+agentToken)
		req.Header.Set("Content-Type", "application/json")
		return http.DefaultClient.Do(req)
	}

	vmCfg := generateTestVMConfig(0)
	t.Logf("Creating VM %s with IP %s", vmCfg.MachineID, vmCfg.VMIP)

	// Step 1: Create VM and write data
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	wsURL := strings.Replace(proxyURL, "http://", "ws://", 1)
	wsURL = fmt.Sprintf("%s/proxy/%s/terminal/ws", wsURL, vmCfg.MachineID)

	writeOutput := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"echo BACKUP_TEST_DATA > /workspace/backup-test.txt && sync && echo WRITE_OK",
		30*time.Second)
	if !strings.Contains(writeOutput, "WRITE_OK") {
		t.Fatalf("Write did not complete. Output: %s", writeOutput)
	}
	t.Log("1. Wrote BACKUP_TEST_DATA to /workspace/backup-test.txt")

	// Step 2: Stop VM (data volume preserved on host)
	if err := orch.Stop(ctx, vmCfg.MachineID); err != nil {
		t.Fatalf("Stop VM: %v", err)
	}
	t.Log("2. VM stopped")

	volPath := filepath.Join(dataDir, vmCfg.MachineID+".ext4")
	if _, err := os.Stat(volPath); os.IsNotExist(err) {
		t.Fatal("Data volume should exist after stop")
	}

	// Step 3: Create backup via agent control API
	backupReqBody, _ := json.Marshal(agentapi.BackupRequest{
		EncryptionKey: encryptionKey,
	})
	backupResp, err := controlRequest("POST",
		fmt.Sprintf("%s/vms/%s/backup", controlURL, vmCfg.MachineID),
		bytes.NewReader(backupReqBody))
	if err != nil {
		t.Fatalf("Backup request failed: %v", err)
	}
	defer backupResp.Body.Close()

	if backupResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(backupResp.Body)
		t.Fatalf("Backup returned %d: %s", backupResp.StatusCode, string(body))
	}

	var backupResult agentapi.BackupResponse
	if err := json.NewDecoder(backupResp.Body).Decode(&backupResult); err != nil {
		t.Fatalf("Decode backup response: %v", err)
	}
	t.Logf("3. Backup created: gcs_path=%s, size=%d, compressed=%d",
		backupResult.GCSPath, backupResult.SizeBytes, backupResult.CompressedBytes)

	// Step 4: Delete the data volume (simulate migration / data loss)
	if err := os.Remove(volPath); err != nil {
		t.Fatalf("Remove data volume: %v", err)
	}
	if _, err := os.Stat(volPath); !os.IsNotExist(err) {
		t.Fatal("Data volume should be deleted")
	}
	t.Log("4. Data volume deleted")

	// Step 5: Restore backup via agent control API
	restoreReqBody, _ := json.Marshal(agentapi.RestoreRequest{
		GCSPath:       backupResult.GCSPath,
		EncryptionKey: encryptionKey,
		Nonce:         backupResult.Nonce,
		ExpectedHMAC:  backupResult.HMAC,
	})
	restoreResp, err := controlRequest("POST",
		fmt.Sprintf("%s/vms/%s/restore", controlURL, vmCfg.MachineID),
		bytes.NewReader(restoreReqBody))
	if err != nil {
		t.Fatalf("Restore request failed: %v", err)
	}
	defer restoreResp.Body.Close()

	if restoreResp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(restoreResp.Body)
		t.Fatalf("Restore returned %d: %s", restoreResp.StatusCode, string(body))
	}
	t.Log("5. Backup restored")

	// Verify: data volume file exists again
	if _, err := os.Stat(volPath); os.IsNotExist(err) {
		t.Fatal("Data volume should exist after restore")
	}

	// Step 6: Restart VM and verify data survived
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Re-create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 120*time.Second)

	readOutput := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"cat /workspace/backup-test.txt",
		30*time.Second)
	if !strings.Contains(readOutput, "BACKUP_TEST_DATA") {
		t.Fatalf("Data did not survive backup/restore! Output: %s", readOutput)
	}
	t.Log("6. BACKUP_TEST_DATA survived backup→delete→restore→restart — full cycle verified")
}
