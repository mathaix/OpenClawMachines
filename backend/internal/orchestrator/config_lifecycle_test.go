//go:build linux

package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigLifecycle_FirstBoot_AuthProfilesReadable simulates the full first-boot
// config lifecycle: orchestrator pre-boot write → init script config-current setup →
// auth-profiles.json must be readable at the gateway's expected path.
//
// This test reproduces a bug where the orchestrator created config-current as a real
// directory, but the init script expected it to be a symlink. The auth-profiles.json
// symlink chain would resolve incorrectly, causing EACCES errors on gateway startup.
func TestConfigLifecycle_FirstBoot_AuthProfilesReadable(t *testing.T) {
	// This test doesn't need root — it simulates the directory structure
	// that writeDataVolumeConfig creates and what the init script does.

	// --- Setup: simulate data volume as mounted at tmpDir ---
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	homeDir := filepath.Join(dataDir, "home", "openclaw")
	openclawDir := filepath.Join(homeDir, ".openclaw")
	configBase := filepath.Join(dataDir, "ocm", "configs")

	// Create directory structure (what the init script does at mount time)
	for _, d := range []string{
		configBase,
		filepath.Join(openclawDir, "agents", "main", "agent"),
		filepath.Join(openclawDir, "agents", "main", "sessions"),
		filepath.Join(openclawDir, "identity"),
		filepath.Join(openclawDir, "workspace"),
	} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	// --- Step 1: Simulate orchestrator pre-boot write ---
	// writeDataVolumeConfig creates config-current as a REAL DIRECTORY
	configCurrentDir := filepath.Join(openclawDir, "config-current")
	if err := os.MkdirAll(configCurrentDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write openclaw.json into config-current and expose the stable symlink
	// (what writeDataVolumeConfig creates pre-boot).
	openclawJSON := []byte(`{"models":{"providers":{"anthropic":{"baseUrl":"http://proxy/anthropic/v1"}}}}`)
	if err := os.WriteFile(filepath.Join(configCurrentDir, "openclaw.json"), openclawJSON, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("config-current", "openclaw.json"), filepath.Join(openclawDir, "openclaw.json")); err != nil {
		t.Fatal(err)
	}

	// Write auth-profiles.json into config-current (real dir)
	authProfilesContent := []byte(`{"version":1,"profiles":{"anthropic-proxy":{"type":"api_key","provider":"anthropic","key":"test-nonce"}}}`)
	if err := os.WriteFile(filepath.Join(configCurrentDir, "auth-profiles.json"), authProfilesContent, 0644); err != nil {
		t.Fatal(err)
	}

	// --- Verify pre-condition: config-current is a real directory, not a symlink ---
	fi, err := os.Lstat(configCurrentDir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("pre-condition failed: config-current should be a real directory from orchestrator")
	}
	if !fi.IsDir() {
		t.Fatal("pre-condition failed: config-current should be a directory")
	}

	// --- Step 2: Simulate init script config-current setup ---
	// This is the logic from init-openclaw.sh lines 312-392
	configLink := configCurrentDir // same path as CONFIG_LINK in init script

	// The init script runs this shell logic. We simulate it in Go.
	simulateInitScriptConfigSetup(t, configLink, configBase, openclawDir)

	// --- Step 3: Verify auth-profiles.json is readable at the gateway path ---
	authSrc := filepath.Join(openclawDir, "agents", "main", "agent", "auth-profiles.json")

	// The init script creates this symlink:
	//   agents/main/agent/auth-profiles.json → config-current/auth-profiles.json
	// config-current is now a symlink → /data/ocm/configs/<ts>/
	// So the full chain is: auth-profiles.json → config-current/auth-profiles.json → <ts>/auth-profiles.json

	data, err := os.ReadFile(authSrc)
	if err != nil {
		t.Fatalf("FAILED: auth-profiles.json not readable at gateway path %s: %v\n"+
			"This is the bug — gateway would get EACCES and crash-loop", authSrc, err)
	}

	if !strings.Contains(string(data), "anthropic-proxy") {
		t.Fatalf("auth-profiles.json content wrong: %s", string(data))
	}

	// Also verify config-current is now a symlink (init script invariant)
	fi, err = os.Lstat(configLink)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config-current should be a symlink after init script setup")
	}

	// Verify openclaw.json is still readable through the config directory
	resolvedDir, err := filepath.EvalSymlinks(configLink)
	if err != nil {
		t.Fatalf("cannot resolve config-current symlink: %v", err)
	}
	authInConfig, err := os.ReadFile(filepath.Join(resolvedDir, "auth-profiles.json"))
	if err != nil {
		t.Fatalf("auth-profiles.json not in resolved config dir: %v", err)
	}
	if !strings.Contains(string(authInConfig), "anthropic-proxy") {
		t.Fatalf("auth-profiles.json content wrong in config dir: %s", string(authInConfig))
	}

	openclawData, err := os.ReadFile(filepath.Join(openclawDir, "openclaw.json"))
	if err != nil {
		t.Fatalf("openclaw.json not readable through stable symlink: %v", err)
	}
	if !strings.Contains(string(openclawData), "anthropic") {
		t.Fatalf("openclaw.json content wrong: %s", string(openclawData))
	}

	t.Log("OK: auth-profiles.json is readable at all expected paths after first boot")
}

// TestConfigLifecycle_Reboot_PreservesConfig verifies that on reboot (config-current
// is already a symlink), the init script reuses the existing config and auth-profiles.json
// remains readable.
func TestConfigLifecycle_Reboot_PreservesConfig(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	homeDir := filepath.Join(dataDir, "home", "openclaw")
	openclawDir := filepath.Join(homeDir, ".openclaw")
	configBase := filepath.Join(dataDir, "ocm", "configs")

	for _, d := range []string{
		configBase,
		filepath.Join(openclawDir, "agents", "main", "agent"),
	} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	// --- Simulate state after first boot (config-current is a symlink) ---
	tsDir := filepath.Join(configBase, "20260323T120000Z")
	if err := os.MkdirAll(tsDir, 0755); err != nil {
		t.Fatal(err)
	}

	authContent := []byte(`{"version":1,"profiles":{"openai-proxy":{"type":"api_key","provider":"openai","key":"nonce123"}}}`)
	if err := os.WriteFile(filepath.Join(tsDir, "auth-profiles.json"), authContent, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tsDir, ".config-version"), []byte("1"), 0644); err != nil {
		t.Fatal(err)
	}

	configLink := filepath.Join(openclawDir, "config-current")
	if err := os.Symlink(tsDir, configLink); err != nil {
		t.Fatal(err)
	}

	// --- Simulate init script on reboot ---
	simulateInitScriptConfigSetup(t, configLink, configBase, openclawDir)

	// --- Verify auth-profiles.json is readable ---
	authSrc := filepath.Join(openclawDir, "agents", "main", "agent", "auth-profiles.json")
	data, err := os.ReadFile(authSrc)
	if err != nil {
		t.Fatalf("auth-profiles.json not readable on reboot: %v", err)
	}
	if !strings.Contains(string(data), "openai-proxy") {
		t.Fatalf("auth-profiles.json content wrong: %s", string(data))
	}

	t.Log("OK: auth-profiles.json preserved and readable after reboot")
}

// TestConfigLifecycle_ConfigVersionUpgrade verifies that on rootfs upgrade (config version
// changes), files are copied forward and auth-profiles.json remains accessible.
func TestConfigLifecycle_ConfigVersionUpgrade(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	homeDir := filepath.Join(dataDir, "home", "openclaw")
	openclawDir := filepath.Join(homeDir, ".openclaw")
	configBase := filepath.Join(dataDir, "ocm", "configs")

	for _, d := range []string{
		configBase,
		filepath.Join(openclawDir, "agents", "main", "agent"),
	} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	// --- Old config at version 0 (previous rootfs) ---
	oldDir := filepath.Join(configBase, "20260322T000000Z")
	if err := os.MkdirAll(oldDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "auth-profiles.json"), []byte(`{"profiles":{"old":"data"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "user-custom.json"), []byte(`{"custom":"preserved"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, ".config-version"), []byte("0"), 0644); err != nil {
		t.Fatal(err)
	}

	configLink := filepath.Join(openclawDir, "config-current")
	if err := os.Symlink(oldDir, configLink); err != nil {
		t.Fatal(err)
	}

	// --- Simulate init script with new ROOTFS_CONFIG_VERSION=1 ---
	simulateInitScriptConfigSetup(t, configLink, configBase, openclawDir)

	// --- Verify config was upgraded ---
	resolvedDir, err := filepath.EvalSymlinks(configLink)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedDir == oldDir {
		t.Fatal("config-current should point to a new timestamped dir after upgrade")
	}

	// Verify files were copied forward
	customData, err := os.ReadFile(filepath.Join(resolvedDir, "user-custom.json"))
	if err != nil {
		t.Fatalf("user-custom.json not copied forward: %v", err)
	}
	if !strings.Contains(string(customData), "preserved") {
		t.Fatalf("user-custom.json content wrong: %s", string(customData))
	}

	// Verify auth-profiles.json is readable at gateway path
	authSrc := filepath.Join(openclawDir, "agents", "main", "agent", "auth-profiles.json")
	data, err := os.ReadFile(authSrc)
	if err != nil {
		t.Fatalf("auth-profiles.json not readable after upgrade: %v", err)
	}
	if !strings.Contains(string(data), "old") {
		t.Fatalf("auth-profiles.json content wrong after copy-forward: %s", string(data))
	}

	t.Log("OK: config upgraded, files copied forward, auth-profiles.json readable")
}

// TestConfigLifecycle_FirstBoot_OldInitScript_Broken reproduces the original bug.
// The old init script had no real-directory migration — it only checked for symlinks.
// When the orchestrator created config-current as a real directory, the init script
// could not replace it with a symlink (ln -sfn fails on non-empty directories).
// The key invariant violated: config-current MUST be a symlink after init setup.
func TestConfigLifecycle_FirstBoot_OldInitScript_Broken(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	homeDir := filepath.Join(dataDir, "home", "openclaw")
	openclawDir := filepath.Join(homeDir, ".openclaw")
	configBase := filepath.Join(dataDir, "ocm", "configs")

	for _, d := range []string{
		configBase,
		filepath.Join(openclawDir, "agents", "main", "agent"),
	} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	// Orchestrator creates config-current as a REAL DIRECTORY
	configCurrentDir := filepath.Join(openclawDir, "config-current")
	if err := os.MkdirAll(configCurrentDir, 0755); err != nil {
		t.Fatal(err)
	}
	authContent := []byte(`{"version":1,"profiles":{"anthropic-proxy":{"type":"api_key"}}}`)
	if err := os.WriteFile(filepath.Join(configCurrentDir, "auth-profiles.json"), authContent, 0644); err != nil {
		t.Fatal(err)
	}

	// Run OLD init script logic (no real-directory migration)
	simulateOldInitScriptConfigSetup(t, configCurrentDir, configBase, openclawDir)

	// The key invariant: config-current MUST be a symlink after init setup.
	// With the old code, it stays as a real directory because ln -sfn can't
	// replace a non-empty directory.
	fi, err := os.Lstat(configCurrentDir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("Expected config-current to still be a real directory with old init script — bug not reproduced")
	}
	t.Log("Confirmed bug: config-current is still a real directory (not a symlink) with old init script")

	// The new timestamped config dir exists but has NO auth-profiles.json
	// (nothing was copied forward because prevConfig was empty)
	newConfigDir := filepath.Join(configBase, "new-config")
	if _, err := os.Stat(filepath.Join(newConfigDir, "auth-profiles.json")); !os.IsNotExist(err) {
		t.Fatal("Expected auth-profiles.json to NOT exist in new config dir (nothing copied forward)")
	}
	t.Log("Confirmed bug: auth-profiles.json not copied to new config dir")
}

// simulateOldInitScriptConfigSetup reproduces the BROKEN init script logic that
// existed before the fix. It only checks for symlinks, ignoring real directories.
func simulateOldInitScriptConfigSetup(t *testing.T, configLink, configBase, openclawDir string) {
	t.Helper()

	const rootfsConfigVersion = "1"
	needNewConfig := false
	prevConfig := ""

	// OLD CODE: only checks [ -L "$CONFIG_LINK" ], misses real directories
	fi, err := os.Lstat(configLink)
	if err == nil && fi.Mode()&os.ModeSymlink != 0 {
		resolved, _ := filepath.EvalSymlinks(configLink)
		if fi2, err := os.Stat(resolved); err == nil && fi2.IsDir() {
			prevConfig = resolved
			versionData, _ := os.ReadFile(filepath.Join(prevConfig, ".config-version"))
			prevVersion := strings.TrimSpace(string(versionData))
			if prevVersion == "" {
				prevVersion = "0"
			}
			if prevVersion != rootfsConfigVersion {
				needNewConfig = true
			}
		} else {
			needNewConfig = true
		}
	} else {
		// config-current is a real dir but this branch treats it as "no config"
		needNewConfig = true
	}

	if needNewConfig {
		newConfig := filepath.Join(configBase, "new-config")
		_ = os.MkdirAll(newConfig, 0755)
		// prevConfig is empty — nothing to copy forward (auth-profiles.json lost!)
		_ = os.WriteFile(filepath.Join(newConfig, ".config-version"), []byte(rootfsConfigVersion), 0644)

		// Simulate shell: ln -sfn on a non-empty directory FAILS.
		// unlink() returns EISDIR, so config-current stays as a real directory.
		// The new timestamped dir is created but config-current is NOT updated.
	}

	// Create auth-profiles.json symlink — points through config-current
	// With the bug, config-current is still a real dir, so this symlink
	// happens to work for READING. But the invariant is violated:
	// future boots expect config-current to be a symlink and will break.
	authSrc := filepath.Join(openclawDir, "agents", "main", "agent", "auth-profiles.json")
	_ = os.Remove(authSrc)
	_ = os.Symlink(filepath.Join(configLink, "auth-profiles.json"), authSrc)
}

// simulateInitScriptConfigSetup reproduces the FIXED config directory setup logic from
// init-openclaw.sh (lines 308-392). This is the Go equivalent of the shell code
// so we can test the orchestrator→init script interaction without booting a VM.
//
// ROOTFS_CONFIG_VERSION is hardcoded to "1" (matching the current init script).
func simulateInitScriptConfigSetup(t *testing.T, configLink, configBase, openclawDir string) {
	t.Helper()

	const rootfsConfigVersion = "1"
	needNewConfig := false
	prevConfig := ""

	// --- Handle real directory from orchestrator pre-boot write ---
	fi, err := os.Lstat(configLink)
	if err == nil && fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
		// config-current is a real directory — migrate to timestamped dir
		tsDir := filepath.Join(configBase, "preboot")
		if err := os.MkdirAll(tsDir, 0755); err != nil {
			t.Fatal(err)
		}
		// cp -a contents
		cpCmd := exec.Command("cp", "-a", configLink+"/.", tsDir+"/")
		if out, err := cpCmd.CombinedOutput(); err != nil {
			t.Fatalf("cp -a failed: %s: %v", string(out), err)
		}
		if err := os.RemoveAll(configLink); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(tsDir, configLink); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tsDir, ".config-version"), []byte(rootfsConfigVersion), 0644); err != nil {
			t.Fatal(err)
		}
		prevConfig = tsDir
		t.Log("Migrated pre-boot config dir to", tsDir)
	}

	// --- Check if config-current is a valid symlink ---
	fi, err = os.Lstat(configLink)
	if err == nil && fi.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(configLink)
		if err == nil {
			fi2, err := os.Stat(resolved)
			if err == nil && fi2.IsDir() {
				prevConfig = resolved
				versionData, _ := os.ReadFile(filepath.Join(prevConfig, ".config-version"))
				prevVersion := strings.TrimSpace(string(versionData))
				if prevVersion == "" {
					prevVersion = "0"
				}
				if prevVersion != rootfsConfigVersion {
					needNewConfig = true
					t.Logf("Config version changed (%s -> %s)", prevVersion, rootfsConfigVersion)
				} else {
					t.Logf("Config version matches, reusing %s", prevConfig)
				}
			} else {
				needNewConfig = true
			}
		} else {
			needNewConfig = true
		}
	} else if prevConfig == "" {
		needNewConfig = true
		t.Log("No previous config found, creating initial")
	}

	// --- Create new config dir if needed ---
	if needNewConfig {
		newConfig := filepath.Join(configBase, "new-config")
		if err := os.MkdirAll(newConfig, 0755); err != nil {
			t.Fatal(err)
		}

		// Copy forward from previous config
		if prevConfig != "" {
			entries, _ := os.ReadDir(prevConfig)
			for _, e := range entries {
				if e.Name() == ".config-version" {
					continue
				}
				src := filepath.Join(prevConfig, e.Name())
				dst := filepath.Join(newConfig, e.Name())
				if _, err := os.Stat(dst); os.IsNotExist(err) {
					data, err := os.ReadFile(src)
					if err == nil {
						_ = os.WriteFile(dst, data, 0644)
					}
				}
			}
			t.Logf("Copied forward from %s", prevConfig)
		}

		if err := os.WriteFile(filepath.Join(newConfig, ".config-version"), []byte(rootfsConfigVersion), 0644); err != nil {
			t.Fatal(err)
		}

		// Atomic switch — remove old symlink, create new one
		_ = os.Remove(configLink)
		if err := os.Symlink(newConfig, configLink); err != nil {
			t.Fatal(err)
		}
		t.Logf("Config switched to %s", newConfig)
	}

	// --- Create auth-profiles.json symlink ---
	openclawConfigSrc := filepath.Join(openclawDir, "openclaw.json")
	if fi, err := os.Lstat(openclawConfigSrc); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		if err := os.Rename(openclawConfigSrc, filepath.Join(prevConfig, "openclaw.json")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(configLink, "openclaw.json")); err == nil {
		_ = os.Remove(openclawConfigSrc)
		if err := os.Symlink(filepath.Join("config-current", "openclaw.json"), openclawConfigSrc); err != nil {
			t.Fatal(err)
		}
	}

	authSrc := filepath.Join(openclawDir, "agents", "main", "agent", "auth-profiles.json")
	// Remove existing file/symlink
	_ = os.Remove(authSrc)
	// Symlink: auth-profiles.json → config-current/auth-profiles.json
	if err := os.Symlink(filepath.Join(configLink, "auth-profiles.json"), authSrc); err != nil {
		t.Fatal(err)
	}
}
