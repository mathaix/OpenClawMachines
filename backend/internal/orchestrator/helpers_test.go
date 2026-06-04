//go:build linux

package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/network"
	"github.com/mathaix/openclawmachines/backend/internal/rootfs"
)

func newTestOrchestrator(t *testing.T) *firecrackerOrchestrator {
	t.Helper()
	tmpDir := t.TempDir()
	return &firecrackerOrchestrator{
		cfg: FirecrackerConfig{
			DataDir:         filepath.Join(tmpDir, "data"),
			StateDir:        filepath.Join(tmpDir, "state"),
			RuntimeStateDir: filepath.Join(tmpDir, "runtime"),
		},
		vms:                   make(map[string]*runningVM),
		quarantinedVMs:        make(map[string]persistedVM),
		browserVMs:            make(map[string]*browserRunningVM),
		quarantinedBrowserVMs: make(map[string]persistedBrowserVM),
	}
}

// skipIfNoMkfsExt4 skips the test if mkfs.ext4 is not available.
func skipIfNoMkfsExt4(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("Skipping: mkfs.ext4 not available")
	}
}

func TestDataVolumePath_WithDataDir(t *testing.T) {
	o := newTestOrchestrator(t)
	path := o.dataVolumePath("machine-123")
	expected := filepath.Join(o.cfg.DataDir, "machine-123.ext4")
	if path != expected {
		t.Fatalf("expected %s, got %s", expected, path)
	}
}

func TestDataVolumePath_EmptyDataDir(t *testing.T) {
	o := &firecrackerOrchestrator{
		cfg: FirecrackerConfig{DataDir: ""},
		vms: make(map[string]*runningVM),
	}
	path := o.dataVolumePath("machine-123")
	if path != "" {
		t.Fatalf("expected empty path, got %s", path)
	}
}

func TestSetMountedOpenClawRuntimePathsUsesReleaseManifest(t *testing.T) {
	o := newTestOrchestrator(t)
	o.cfg.OpenClawRuntimeDir = t.TempDir()

	version := "openclaw-test-1"
	releaseDir := filepath.Join(o.cfg.OpenClawRuntimeDir, "releases", version)
	if err := os.MkdirAll(releaseDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "schema_version": 1,
  "kind": "openclaw-runtime",
  "version": "openclaw-test-1",
  "artifact_url": "file:///tmp/openclaw-test.tar.zst",
  "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "runtime": {
    "entrypoint_relpath": "custom/bin/openclaw",
    "bundled_plugins_relpath": "custom/plugins"
  }
}
`
	if err := os.WriteFile(filepath.Join(releaseDir, "manifest.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	selection := &metadata.RuntimeSelection{ResolvedOpenClawVersion: version}
	o.setMountedOpenClawRuntimePaths(selection)

	if selection.OpenClawBin != "/ocm-runtime/custom/bin/openclaw" {
		t.Fatalf("OpenClawBin = %q", selection.OpenClawBin)
	}
	if selection.OpenClawBundledPluginsDir != "/ocm-runtime/custom/plugins" {
		t.Fatalf("OpenClawBundledPluginsDir = %q", selection.OpenClawBundledPluginsDir)
	}
}

func TestVersionSidecarPath(t *testing.T) {
	o := newTestOrchestrator(t)
	path := o.versionSidecarPath("machine-abc")
	expected := filepath.Join(o.cfg.DataDir, "machine-abc.version")
	if path != expected {
		t.Fatalf("expected %s, got %s", expected, path)
	}
}

func TestVersionSidecarPath_EmptyDataDir(t *testing.T) {
	o := &firecrackerOrchestrator{
		cfg: FirecrackerConfig{DataDir: ""},
	}
	path := o.versionSidecarPath("machine-abc")
	if path != "" {
		t.Fatalf("expected empty path, got %s", path)
	}
}

func TestVersionSidecar_ReadWrite(t *testing.T) {
	o := newTestOrchestrator(t)
	if err := os.MkdirAll(o.cfg.DataDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Read non-existent sidecar returns 0
	v := o.readVersionSidecar("m-001")
	if v != 0 {
		t.Fatalf("expected 0 for missing sidecar, got %d", v)
	}

	// Write version
	if err := o.writeVersionSidecar("m-001", 3); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	// Read it back
	v = o.readVersionSidecar("m-001")
	if v != 3 {
		t.Fatalf("expected 3, got %d", v)
	}

	// Overwrite
	if err := o.writeVersionSidecar("m-001", 5); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	v = o.readVersionSidecar("m-001")
	if v != 5 {
		t.Fatalf("expected 5, got %d", v)
	}
}

func TestEffectiveRootfsVersion_RejectsStagedMismatch(t *testing.T) {
	o := newTestOrchestrator(t)
	o.cfg.GCSRootfsManifest = "gs://openclawmachines/rootfs/manifest.json"
	if err := os.MkdirAll(o.cfg.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := &rootfs.Manifest{Version: "rootfs-actual"}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(o.cfg.StateDir, ".rootfs-manifest.json"), data, 0o644); err != nil {
		t.Fatalf("write manifest sidecar: %v", err)
	}

	got, err := o.effectiveRootfsVersion(&metadata.RuntimeSelection{ResolvedRootfsVersion: "rootfs-actual"})
	if err != nil {
		t.Fatalf("effectiveRootfsVersion match: %v", err)
	}
	if got != "rootfs-actual" {
		t.Fatalf("effectiveRootfsVersion = %q, want rootfs-actual", got)
	}

	if _, err := o.effectiveRootfsVersion(&metadata.RuntimeSelection{ResolvedRootfsVersion: "rootfs-requested"}); err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestSyncBrowserRootfsHeartbeatSidecar(t *testing.T) {
	o := newTestOrchestrator(t)
	stagedPath := filepath.Join(o.browserRootfsBaseDir(), "releases", "browser-rootfs-v1.ext4")
	if err := os.MkdirAll(filepath.Dir(stagedPath), 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := &rootfs.Manifest{Version: "browser-rootfs-v1", SHA256: "deadbeef"}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(stagedPath+".manifest.json", data, 0o644); err != nil {
		t.Fatalf("write staged manifest sidecar: %v", err)
	}

	if err := o.syncBrowserRootfsHeartbeatSidecar(stagedPath); err != nil {
		t.Fatalf("syncBrowserRootfsHeartbeatSidecar: %v", err)
	}

	got, err := rootfs.ReadCachedManifest(o.browserRootfsHeartbeatSidecarPath())
	if err != nil {
		t.Fatalf("ReadCachedManifest: %v", err)
	}
	if got.Version != "browser-rootfs-v1" {
		t.Fatalf("heartbeat sidecar version = %q, want browser-rootfs-v1", got.Version)
	}
}

func TestBrowserStateDirOverridesBrowserRootfsAndStatePaths(t *testing.T) {
	o := newTestOrchestrator(t)
	browserDir := filepath.Join(t.TempDir(), "browser-state")
	o.cfg.BrowserStateDir = browserDir

	if got := o.browserRootfsBaseDir(); got != filepath.Join(browserDir, "browser-rootfs") {
		t.Fatalf("browserRootfsBaseDir = %q, want browser state dir path", got)
	}
	if got := o.browserRootfsHeartbeatSidecarPath(); got != filepath.Join(browserDir, ".browser-rootfs-manifest.json") {
		t.Fatalf("browserRootfsHeartbeatSidecarPath = %q, want browser state dir path", got)
	}
	if got := o.browserStateFilePath(); got != filepath.Join(browserDir, "browser-vms.json") {
		t.Fatalf("browserStateFilePath = %q, want browser state dir path", got)
	}
}

func TestSaveStatePersistsActualRuntimeVersions(t *testing.T) {
	o := newTestOrchestrator(t)
	if err := os.MkdirAll(o.cfg.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	o.vms["m1"] = &runningVM{
		instance: VMInstance{
			MachineID:       "m1",
			VMIP:            "192.168.100.10",
			RootfsVersion:   "rootfs-actual",
			OpenClawVersion: "openclaw-actual",
			Status:          "running",
		},
	}

	o.saveState()

	persisted, err := o.loadPersistedState()
	if err != nil {
		t.Fatalf("load persisted state: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("expected 1 persisted VM, got %d", len(persisted))
	}
	if persisted[0].RootfsVersion != "rootfs-actual" {
		t.Fatalf("persisted rootfs version = %q, want rootfs-actual", persisted[0].RootfsVersion)
	}
	if persisted[0].OpenClawVersion != "openclaw-actual" {
		t.Fatalf("persisted openclaw version = %q, want openclaw-actual", persisted[0].OpenClawVersion)
	}
}

func TestBackupDataVolume_CreatesPreUpgrade(t *testing.T) {
	o := newTestOrchestrator(t)
	if err := os.MkdirAll(o.cfg.DataDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a fake data volume
	volPath := o.dataVolumePath("m-backup")
	if err := os.WriteFile(volPath, []byte("fake-ext4-data"), 0644); err != nil {
		t.Fatalf("create test volume: %v", err)
	}

	// Backup
	if err := o.backupDataVolume("m-backup"); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// Verify backup exists
	backupPath := volPath + ".pre-upgrade"
	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(data) != "fake-ext4-data" {
		t.Fatalf("backup content mismatch: %s", string(data))
	}
}

func TestBackupDataVolume_NoExistingVolume(t *testing.T) {
	o := newTestOrchestrator(t)
	if err := os.MkdirAll(o.cfg.DataDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Backup non-existent volume should fail
	err := o.backupDataVolume("m-nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent volume")
	}
}

func TestRollbackDataVolume_Success(t *testing.T) {
	o := newTestOrchestrator(t)
	if err := os.MkdirAll(o.cfg.DataDir, 0755); err != nil {
		t.Fatal(err)
	}

	machineID := "m-rollback"
	volPath := o.dataVolumePath(machineID)

	// Create current volume and backup
	if err := os.WriteFile(volPath, []byte("new-data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(volPath+".pre-upgrade", []byte("old-data"), 0644); err != nil {
		t.Fatal(err)
	}
	// Write a version sidecar
	if err := o.writeVersionSidecar(machineID, 2); err != nil {
		t.Fatal(err)
	}

	// Rollback
	if err := o.RollbackDataVolume(machineID); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// Verify current volume has old data
	data, err := os.ReadFile(volPath)
	if err != nil {
		t.Fatalf("read volume: %v", err)
	}
	if string(data) != "old-data" {
		t.Fatalf("expected old-data, got %s", string(data))
	}

	// Verify backup is gone
	if _, err := os.Stat(volPath + ".pre-upgrade"); !os.IsNotExist(err) {
		t.Fatal("backup should be removed after rollback")
	}

	// Verify version sidecar is gone
	if _, err := os.Stat(o.versionSidecarPath(machineID)); !os.IsNotExist(err) {
		t.Fatal("version sidecar should be removed after rollback")
	}
}

func TestRollbackDataVolume_NoBackup(t *testing.T) {
	o := newTestOrchestrator(t)
	if err := os.MkdirAll(o.cfg.DataDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create volume without backup
	volPath := o.dataVolumePath("m-nobackup")
	if err := os.WriteFile(volPath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	err := o.RollbackDataVolume("m-nobackup")
	if err == nil {
		t.Fatal("expected error when no backup exists")
	}
}

func TestRollbackDataVolume_NoDataDir(t *testing.T) {
	o := &firecrackerOrchestrator{
		cfg: FirecrackerConfig{DataDir: ""},
	}

	err := o.RollbackDataVolume("m-any")
	if err == nil {
		t.Fatal("expected error when no data dir configured")
	}
}

func TestSystemdUnitNames(t *testing.T) {
	if got := machineSystemdUnitName("machine-123"); got != "ocm-vm-machine-123.service" {
		t.Fatalf("unexpected machine unit name: %s", got)
	}
	if got := browserSystemdUnitName("browser-abc"); got != "ocm-browser-vm-browser-abc.service" {
		t.Fatalf("unexpected browser unit name: %s", got)
	}
}

func TestDefaultRuntimeOwnerKind(t *testing.T) {
	if got := defaultRuntimeOwnerKind(""); got != runtimeOwnerKindDirect {
		t.Fatalf("expected default owner kind %q, got %q", runtimeOwnerKindDirect, got)
	}
	if got := defaultRuntimeOwnerKind(runtimeOwnerKindSystemdVML); got != runtimeOwnerKindSystemdVML {
		t.Fatalf("expected explicit owner kind to survive, got %q", got)
	}
}

func TestRuntimeOwnerFromKind(t *testing.T) {
	owner, err := runtimeOwnerFromKind("")
	if err != nil {
		t.Fatalf("unexpected error for default owner: %v", err)
	}
	if owner.kind() != runtimeOwnerKindDirect {
		t.Fatalf("expected direct owner, got %q", owner.kind())
	}

	owner, err = runtimeOwnerFromKind(runtimeOwnerKindSystemdVML)
	if err != nil {
		t.Fatalf("unexpected error for systemd owner: %v", err)
	}
	if owner.kind() != runtimeOwnerKindSystemdVML {
		t.Fatalf("expected systemd owner, got %q", owner.kind())
	}

	if _, err := runtimeOwnerFromKind("nope"); err == nil {
		t.Fatal("expected invalid owner kind to fail")
	}
}

func TestRollbackDataVolume_RejectsRunningVM(t *testing.T) {
	o := newTestOrchestrator(t)
	if err := os.MkdirAll(o.cfg.DataDir, 0755); err != nil {
		t.Fatal(err)
	}

	machineID := "m-running"
	volPath := o.dataVolumePath(machineID)

	// Create volume and backup
	if err := os.WriteFile(volPath, []byte("new-data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(volPath+".pre-upgrade", []byte("old-data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Register VM as running
	o.vms[machineID] = &runningVM{
		instance: VMInstance{
			MachineID: machineID,
			Status:    "running",
		},
	}

	// Rollback should be rejected
	err := o.RollbackDataVolume(machineID)
	if err == nil {
		t.Fatal("expected error when VM is running")
	}
	if !strings.Contains(err.Error(), "is running") {
		t.Fatalf("expected 'is running' error, got: %v", err)
	}

	// Verify data was NOT modified
	data, _ := os.ReadFile(volPath)
	if string(data) != "new-data" {
		t.Fatalf("volume should not have been modified, got: %s", string(data))
	}
}

// ============================================================================
// extractProviderNames Tests
// ============================================================================

func TestExtractProviderNames_Valid(t *testing.T) {
	conf := []byte(`{"models":{"providers":{"anthropic":{"baseUrl":"http://proxy/anthropic/v1"},"openai":{"baseUrl":"http://proxy/openai/v1"}}}}`)
	names := extractProviderNames(conf)
	if len(names) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(names))
	}
	nameSet := map[string]bool{}
	for _, n := range names {
		nameSet[n] = true
	}
	if !nameSet["anthropic"] || !nameSet["openai"] {
		t.Fatalf("expected anthropic and openai, got %v", names)
	}
}

func TestExtractProviderNames_Empty(t *testing.T) {
	names := extractProviderNames(nil)
	if len(names) != 0 {
		t.Fatalf("expected 0 providers for nil input, got %d", len(names))
	}

	names = extractProviderNames([]byte("{}"))
	if len(names) != 0 {
		t.Fatalf("expected 0 providers for empty config, got %d", len(names))
	}
}

func TestExtractProviderNames_InvalidJSON(t *testing.T) {
	names := extractProviderNames([]byte("not json"))
	if len(names) != 0 {
		t.Fatalf("expected 0 providers for invalid JSON, got %d", len(names))
	}
}

func TestExtractProviderNames_NoModels(t *testing.T) {
	conf := []byte(`{"channels":{"slack":{}}}`)
	names := extractProviderNames(conf)
	if len(names) != 0 {
		t.Fatalf("expected 0 providers, got %d", len(names))
	}
}

// ============================================================================
// generateAuthProfiles Tests
// ============================================================================

func TestGenerateAuthProfiles(t *testing.T) {
	nonce := "abc123"
	data := generateAuthProfiles([]string{"anthropic", "openai"}, nonce)

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal auth profiles: %v", err)
	}

	version, ok := result["version"].(float64)
	if !ok || version != 1 {
		t.Fatalf("expected version 1, got %v", result["version"])
	}

	profiles, ok := result["profiles"].(map[string]interface{})
	if !ok {
		t.Fatal("missing profiles")
	}

	for _, provider := range []string{"anthropic", "openai"} {
		profileKey := provider + "-proxy"
		profile, ok := profiles[profileKey].(map[string]interface{})
		if !ok {
			t.Fatalf("missing profile %s", profileKey)
		}
		if profile["type"] != "api_key" {
			t.Fatalf("expected type api_key for %s, got %v", profileKey, profile["type"])
		}
		if profile["provider"] != provider {
			t.Fatalf("expected provider %s, got %v", provider, profile["provider"])
		}
		if profile["key"] != nonce {
			t.Fatalf("expected key %s, got %v", nonce, profile["key"])
		}
	}
}

func TestGenerateAuthProfiles_Empty(t *testing.T) {
	data := generateAuthProfiles(nil, "nonce")
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	profiles, ok := result["profiles"].(map[string]interface{})
	if !ok {
		t.Fatal("missing profiles")
	}
	if len(profiles) != 0 {
		t.Fatalf("expected empty profiles, got %d", len(profiles))
	}
}

// ============================================================================
// loadOrGenerateNonce Tests
// ============================================================================

func TestLoadOrGenerateNonce_CreatesNew(t *testing.T) {
	tmpDir := t.TempDir()
	noncePath := filepath.Join(tmpDir, ".ocm-nonce")

	nonce, err := loadOrGenerateNonce(noncePath)
	if err != nil {
		t.Fatalf("loadOrGenerateNonce: %v", err)
	}
	if len(nonce) != 32 { // 16 bytes = 32 hex chars
		t.Fatalf("expected 32-char nonce, got %d chars: %s", len(nonce), nonce)
	}

	// Verify file was written
	data, err := os.ReadFile(noncePath)
	if err != nil {
		t.Fatalf("read nonce file: %v", err)
	}
	if strings.TrimSpace(string(data)) != nonce {
		t.Fatalf("nonce file content mismatch: %s vs %s", string(data), nonce)
	}
}

func TestLoadOrGenerateNonce_LoadsExisting(t *testing.T) {
	tmpDir := t.TempDir()
	noncePath := filepath.Join(tmpDir, ".ocm-nonce")

	// Write a known nonce
	if err := os.WriteFile(noncePath, []byte("deadbeef12345678deadbeef12345678\n"), 0600); err != nil {
		t.Fatal(err)
	}

	nonce, err := loadOrGenerateNonce(noncePath)
	if err != nil {
		t.Fatalf("loadOrGenerateNonce: %v", err)
	}
	if nonce != "deadbeef12345678deadbeef12345678" {
		t.Fatalf("expected existing nonce, got %s", nonce)
	}
}

func TestLoadOrGenerateNonce_RegeneratesEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	noncePath := filepath.Join(tmpDir, ".ocm-nonce")

	// Write empty nonce file
	if err := os.WriteFile(noncePath, []byte("\n"), 0600); err != nil {
		t.Fatal(err)
	}

	nonce, err := loadOrGenerateNonce(noncePath)
	if err != nil {
		t.Fatalf("loadOrGenerateNonce: %v", err)
	}
	if len(nonce) != 32 {
		t.Fatalf("expected 32-char nonce for empty file, got %d chars", len(nonce))
	}
}

// TestInterfaceSatisfied verifies firecrackerOrchestrator satisfies Orchestrator.
func TestInterfaceSatisfied(t *testing.T) {
	var _ Orchestrator = &firecrackerOrchestrator{}
}

// Ensure New() returns an Orchestrator (basic smoke test).
func TestNew_ReturnsOrchestrator(t *testing.T) {
	tmpDir := t.TempDir()
	rootfsDir := filepath.Join(tmpDir, "rootfs")
	if err := os.MkdirAll(rootfsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create a dummy rootfs file so stageBaseRootfs succeeds.
	if err := os.WriteFile(filepath.Join(rootfsDir, "rootfs.ext4"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := FirecrackerConfig{
		SocketDir:  filepath.Join(tmpDir, "sockets"),
		StateDir:   filepath.Join(tmpDir, "state"),
		RootfsDir:  rootfsDir,
		DataDir:    filepath.Join(tmpDir, "data"),
		KernelPath: "/nonexistent/vmlinux",
	}
	bridge := &network.Bridge{Gateway: "192.168.100.1"}
	orch, err := New(cfg, bridge)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if orch == nil {
		t.Fatal("New() returned nil")
	}
}

// ============================================================================
// ensureDataVolume Tests
// ============================================================================

func TestEnsureDataVolume_CreatesNewVolume(t *testing.T) {
	skipIfNoMkfsExt4(t)

	o := newTestOrchestrator(t)
	if err := os.MkdirAll(o.cfg.DataDir, 0755); err != nil {
		t.Fatal(err)
	}

	path, err := o.ensureDataVolume("m-new", 1, 1)
	if err != nil {
		t.Fatalf("ensureDataVolume: %v", err)
	}

	expected := filepath.Join(o.cfg.DataDir, "m-new.ext4")
	if path != expected {
		t.Fatalf("expected path %s, got %s", expected, path)
	}

	// Verify file exists and has 0600 permissions
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat volume: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("expected permissions 0600, got %04o", perm)
	}

	// Verify no .pre-upgrade was created for a new volume
	if _, err := os.Stat(path + ".pre-upgrade"); !os.IsNotExist(err) {
		t.Fatal("new volume should not have .pre-upgrade backup")
	}
}

func TestEnsureDataVolume_ReusesExisting(t *testing.T) {
	o := newTestOrchestrator(t)
	if err := os.MkdirAll(o.cfg.DataDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a fake existing volume
	volPath := o.dataVolumePath("m-reuse")
	if err := os.WriteFile(volPath, []byte("existing-data"), 0600); err != nil {
		t.Fatal(err)
	}

	// Write version sidecar matching the requested version
	if err := o.writeVersionSidecar("m-reuse", 2); err != nil {
		t.Fatal(err)
	}

	path, err := o.ensureDataVolume("m-reuse", 1, 2)
	if err != nil {
		t.Fatalf("ensureDataVolume: %v", err)
	}
	if path != volPath {
		t.Fatalf("expected reuse path %s, got %s", volPath, path)
	}

	// Verify content unchanged
	data, _ := os.ReadFile(volPath)
	if string(data) != "existing-data" {
		t.Fatalf("expected existing-data, got %s", string(data))
	}

	// Verify no backup was created (version matches)
	if _, err := os.Stat(volPath + ".pre-upgrade"); !os.IsNotExist(err) {
		t.Fatal("should not create backup when version matches")
	}
}

func TestEnsureDataVolume_BackupOnUpgrade(t *testing.T) {
	o := newTestOrchestrator(t)
	if err := os.MkdirAll(o.cfg.DataDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create fake existing volume at version 1
	volPath := o.dataVolumePath("m-upgrade")
	if err := os.WriteFile(volPath, []byte("v1-data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := o.writeVersionSidecar("m-upgrade", 1); err != nil {
		t.Fatal(err)
	}

	// Request version 2 — should trigger backup
	path, err := o.ensureDataVolume("m-upgrade", 1, 2)
	if err != nil {
		t.Fatalf("ensureDataVolume: %v", err)
	}
	if path != volPath {
		t.Fatalf("expected reuse path %s, got %s", volPath, path)
	}

	// Verify .pre-upgrade backup was created
	backupPath := volPath + ".pre-upgrade"
	backupData, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("expected backup to exist: %v", err)
	}
	if string(backupData) != "v1-data" {
		t.Fatalf("backup content mismatch: got %s", string(backupData))
	}
}

func TestEnsureDataVolume_NoDataDir(t *testing.T) {
	o := &firecrackerOrchestrator{
		cfg: FirecrackerConfig{DataDir: ""},
		vms: make(map[string]*runningVM),
	}

	path, err := o.ensureDataVolume("m-any", 1, 1)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if path != "" {
		t.Fatalf("expected empty path, got %s", path)
	}
}

// ============================================================================
// DataDir Permissions Test
// ============================================================================

func TestDataDir_Permissions(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	rootfsDir := filepath.Join(tmpDir, "rootfs")
	if err := os.MkdirAll(rootfsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootfsDir, "rootfs.ext4"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := FirecrackerConfig{
		SocketDir:  filepath.Join(tmpDir, "sockets"),
		StateDir:   filepath.Join(tmpDir, "state"),
		RootfsDir:  rootfsDir,
		DataDir:    dataDir,
		KernelPath: "/nonexistent/vmlinux",
	}
	bridge := &network.Bridge{Gateway: "192.168.100.1"}
	_, err := New(cfg, bridge)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("stat DataDir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Fatalf("expected DataDir permissions 0700, got %04o", perm)
	}
}

func TestSyncOpenClawRuntime_MirrorsReleaseAndSetsGuestPaths(t *testing.T) {
	o := newTestOrchestrator(t)
	hostRuntimeDir := filepath.Join(t.TempDir(), "host-openclaw")
	o.cfg.OpenClawRuntimeDir = hostRuntimeDir
	o.cfg.RuntimeStateDir = filepath.Join(t.TempDir(), "host-runtime")

	version := "openclaw-test-1"
	hostRelease := filepath.Join(hostRuntimeDir, "releases", version)
	if err := os.MkdirAll(filepath.Join(hostRelease, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(hostRelease, "dist", "extensions"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostRelease, "bin", "openclaw"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}

	mountDir := t.TempDir()
	selection := &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: version,
		RuntimeSource:           "artifact",
	}
	if err := o.syncHostOpenClawRuntimeState("m-artifact", selection); err != nil {
		t.Fatalf("syncHostOpenClawRuntimeState: %v", err)
	}
	if err := o.syncOpenClawRuntime(mountDir, "m-artifact", selection); err != nil {
		t.Fatalf("syncOpenClawRuntime: %v", err)
	}

	if selection.OpenClawBin != "/data/ocm/runtime/openclaw/current/bin/openclaw" {
		t.Fatalf("OpenClawBin = %q", selection.OpenClawBin)
	}
	if selection.OpenClawBundledPluginsDir != "/data/ocm/runtime/openclaw/current/dist/extensions" {
		t.Fatalf("OpenClawBundledPluginsDir = %q", selection.OpenClawBundledPluginsDir)
	}

	mirroredBin := filepath.Join(mountDir, "ocm", "runtime", "openclaw", "releases", version, "bin", "openclaw")
	if info, err := os.Stat(mirroredBin); err != nil {
		t.Fatalf("stat mirrored bin: %v", err)
	} else if info.Mode().Perm() != 0755 {
		t.Fatalf("mirrored bin perms = %04o", info.Mode().Perm())
	}

	currentTarget, err := os.Readlink(filepath.Join(mountDir, "ocm", "runtime", "openclaw", "current"))
	if err != nil {
		t.Fatalf("read current symlink: %v", err)
	}
	if currentTarget != filepath.Join("releases", version) {
		t.Fatalf("current symlink = %q", currentTarget)
	}

	resolvedData, err := os.ReadFile(filepath.Join(mountDir, "ocm", "runtime", "openclaw", "resolved_version"))
	if err != nil {
		t.Fatalf("read resolved_version: %v", err)
	}
	if strings.TrimSpace(string(resolvedData)) != version {
		t.Fatalf("resolved_version = %q", strings.TrimSpace(string(resolvedData)))
	}

	hostCurrentTarget, err := os.Readlink(filepath.Join(o.cfg.RuntimeStateDir, "machines", "m-artifact", "openclaw", "current"))
	if err != nil {
		t.Fatalf("read host current symlink: %v", err)
	}
	if hostCurrentTarget != filepath.Join(hostRuntimeDir, "releases", version) {
		t.Fatalf("host current symlink = %q", hostCurrentTarget)
	}
}

func TestSyncOpenClawRuntime_MissingHostReleaseReturnsError(t *testing.T) {
	o := newTestOrchestrator(t)
	o.cfg.OpenClawRuntimeDir = filepath.Join(t.TempDir(), "host-openclaw")
	o.cfg.RuntimeStateDir = filepath.Join(t.TempDir(), "host-runtime")

	selection := &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: "openclaw-missing",
		RuntimeSource:           "artifact",
	}
	if err := o.syncHostOpenClawRuntimeState("m-missing", selection); err != nil {
		t.Fatalf("syncHostOpenClawRuntimeState: %v", err)
	}
	err := o.syncOpenClawRuntime(t.TempDir(), "m-missing", selection)
	if err == nil {
		t.Fatal("expected error when artifact release is not staged on host")
	}
	if !strings.Contains(err.Error(), "not staged on host") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSyncOpenClawRuntime_ArtifactModeRequiresMirroredRelease(t *testing.T) {
	o := newTestOrchestrator(t)
	o.cfg.OpenClawRuntimeDir = filepath.Join(t.TempDir(), "host-openclaw")
	o.cfg.RuntimeStateDir = filepath.Join(t.TempDir(), "host-runtime")

	selection := &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: "openclaw-missing",
		RuntimeSource:           "artifact",
	}
	if err := o.syncHostOpenClawRuntimeState("m-missing", selection); err != nil {
		t.Fatalf("syncHostOpenClawRuntimeState: %v", err)
	}
	err := o.syncOpenClawRuntime(t.TempDir(), "m-missing", selection)
	if err == nil {
		t.Fatal("expected strict artifact mode to fail when the staged release is missing")
	}
	if !strings.Contains(err.Error(), "not staged on host") {
		t.Fatalf("expected missing staged artifact error, got: %v", err)
	}
}

func TestEnsureOpenClawRuntime_ArtifactModeRequiresManifestWhenCacheMisses(t *testing.T) {
	o := newTestOrchestrator(t)
	o.cfg.OpenClawRuntimeDir = filepath.Join(t.TempDir(), "host-openclaw")

	err := o.ensureOpenClawRuntime(context.Background(), &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: "openclaw-test-1",
		RuntimeSource:           "artifact",
	})
	if err == nil {
		t.Fatal("expected strict artifact mode to fail when the runtime is missing and no manifest is configured")
	}
	if !strings.Contains(err.Error(), "is not staged and no manifest URI configured") {
		t.Fatalf("expected manifest-not-configured error, got: %v", err)
	}
}

func TestSyncHostOpenClawRuntimeState_TracksPreviousRelease(t *testing.T) {
	o := newTestOrchestrator(t)
	o.cfg.OpenClawRuntimeDir = filepath.Join(t.TempDir(), "host-openclaw")
	o.cfg.RuntimeStateDir = filepath.Join(t.TempDir(), "host-runtime")

	first := &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: "openclaw-test-1",
		RuntimeSource:           "artifact",
	}
	second := &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: "openclaw-test-2",
		RuntimeSource:           "artifact",
	}

	if err := o.syncHostOpenClawRuntimeState("m-stateful", first); err != nil {
		t.Fatalf("sync first host state: %v", err)
	}
	if err := o.syncHostOpenClawRuntimeState("m-stateful", second); err != nil {
		t.Fatalf("sync second host state: %v", err)
	}

	stateDir := filepath.Join(o.cfg.RuntimeStateDir, "machines", "m-stateful", "openclaw")
	selected := readRuntimeStateValue(filepath.Join(stateDir, "selected_version"))
	if selected != "openclaw-test-2" {
		t.Fatalf("selected_version = %q", selected)
	}
	resolved := readRuntimeStateValue(filepath.Join(stateDir, "resolved_version"))
	if resolved != "openclaw-test-2" {
		t.Fatalf("resolved_version = %q", resolved)
	}

	currentTarget, err := os.Readlink(filepath.Join(stateDir, "current"))
	if err != nil {
		t.Fatalf("read current symlink: %v", err)
	}
	if currentTarget != filepath.Join(o.cfg.OpenClawRuntimeDir, "releases", "openclaw-test-2") {
		t.Fatalf("current symlink = %q", currentTarget)
	}

	previousTarget, err := os.Readlink(filepath.Join(stateDir, "previous"))
	if err != nil {
		t.Fatalf("read previous symlink: %v", err)
	}
	if previousTarget != filepath.Join(o.cfg.OpenClawRuntimeDir, "releases", "openclaw-test-1") {
		t.Fatalf("previous symlink = %q", previousTarget)
	}
}

func TestSyncOpenClawRuntime_MirrorsPreviousReleaseFromHostState(t *testing.T) {
	o := newTestOrchestrator(t)
	hostRuntimeDir := filepath.Join(t.TempDir(), "host-openclaw")
	o.cfg.OpenClawRuntimeDir = hostRuntimeDir
	o.cfg.RuntimeStateDir = filepath.Join(t.TempDir(), "host-runtime")

	for _, version := range []string{"openclaw-test-1", "openclaw-test-2"} {
		hostRelease := filepath.Join(hostRuntimeDir, "releases", version)
		if err := os.MkdirAll(filepath.Join(hostRelease, "bin"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(hostRelease, "dist", "extensions"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(hostRelease, "bin", "openclaw"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	if err := o.syncHostOpenClawRuntimeState("m-switch", &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: "openclaw-test-1",
		RuntimeSource:           "artifact",
	}); err != nil {
		t.Fatalf("sync first host state: %v", err)
	}
	selection := &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: "openclaw-test-2",
		RuntimeSource:           "artifact",
	}
	if err := o.syncHostOpenClawRuntimeState("m-switch", selection); err != nil {
		t.Fatalf("sync second host state: %v", err)
	}
	mountDir := t.TempDir()
	if err := o.syncOpenClawRuntime(mountDir, "m-switch", selection); err != nil {
		t.Fatalf("syncOpenClawRuntime: %v", err)
	}

	previousTarget, err := os.Readlink(filepath.Join(mountDir, "ocm", "runtime", "openclaw", "previous"))
	if err != nil {
		t.Fatalf("read guest previous symlink: %v", err)
	}
	if previousTarget != filepath.Join("releases", "openclaw-test-1") {
		t.Fatalf("guest previous symlink = %q", previousTarget)
	}

	if _, err := os.Stat(filepath.Join(mountDir, "ocm", "runtime", "openclaw", "releases", "openclaw-test-1", "bin", "openclaw")); err != nil {
		t.Fatalf("stat mirrored previous release: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mountDir, "ocm", "runtime", "openclaw", "releases", "openclaw-test-2", "bin", "openclaw")); err != nil {
		t.Fatalf("stat mirrored current release: %v", err)
	}
}

func TestCopyDir_RejectsEscapingSymlink(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	dst := filepath.Join(t.TempDir(), "dst")

	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "legit.txt"), []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}
	// Create a symlink that escapes the source root.
	if err := os.Symlink("../../etc/passwd", filepath.Join(src, "escape")); err != nil {
		t.Fatal(err)
	}

	err := copyDir(src, dst)
	if err == nil {
		t.Fatal("expected copyDir to reject escaping symlink")
	}
	if !strings.Contains(err.Error(), "escapes source root") {
		t.Fatalf("expected 'escapes source root' error, got: %v", err)
	}
}

func TestCopyDir_RejectsAbsoluteSymlink(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	dst := filepath.Join(t.TempDir(), "dst")

	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "abs")); err != nil {
		t.Fatal(err)
	}

	err := copyDir(src, dst)
	if err == nil {
		t.Fatal("expected copyDir to reject absolute symlink")
	}
	if !strings.Contains(err.Error(), "absolute symlink") {
		t.Fatalf("expected 'absolute symlink' error, got: %v", err)
	}
}

func TestCopyDirAtomic_ExistingDestinationWins(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	dstRoot := t.TempDir()
	dst := filepath.Join(dstRoot, "release")

	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "from-src.txt"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "existing.txt"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := copyDirAtomic(src, dst); err != nil {
		t.Fatalf("copyDirAtomic: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dst, "existing.txt")); err != nil {
		t.Fatalf("expected existing destination content to remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "from-src.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected source content not to replace existing destination, err=%v", err)
	}
}
