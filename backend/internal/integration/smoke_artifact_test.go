//go:build linux && integration

package integration

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/metadata"
)

// TestSmokeArtifactRuntime downloads the real openclaw artifact from GCS,
// boots a VM with runtime_source=artifact, and verifies the gateway starts
// using the artifact binary (not the baked-in fallback).
//
// This catches packaging issues (hard links, missing exports, broken deps)
// that synthetic test fixtures miss.
//
// Run: make smoke-test-artifact
func TestSmokeArtifactRuntime(t *testing.T) {
	// Resolve the stable channel manifest to get the current version.
	manifestURI := os.Getenv("TEST_OPENCLAW_MANIFEST_URI")
	if strings.TrimSpace(manifestURI) == "" {
		manifestURI = "gs://openclawmachines/openclaw/manifest-stable.json"
	}

	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	// Point the orchestrator at the real GCS manifest.
	cfg.OpenClawManifestURI = manifestURI

	// Read the channel manifest to discover the current stable version.
	version := discoverStableOpenClawVersion(t)
	t.Logf("Stable OpenClaw version: %s", version)

	vmCfg := generateTestVMConfig(0)
	vmCfg.DataVolumeGB = 5 // real artifact needs ~2GB+ when mirrored to data volume
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: version,
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
		OpenClawManifestURI:     manifestURI,
	}

	_, wsURL, vmCfg := setupInitTestVMWithPrepare(t, vmCfg, nil)

	// Diagnostic: dump the artifact layout before waiting for gateway.
	// The VM is already ready at this point (setupInitTestVMWithPrepare waits).
	time.Sleep(5 * time.Second) // let init script settle

	diag := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`echo "=== runtime-env ==="; cat /run/ocm-runtime-env; `+
			`echo "=== bin ==="; ls -la /ocm-runtime/bin/; `+
			`echo "=== top ==="; ls /ocm-runtime/; `+
			`echo "=== tslog ==="; ls /ocm-runtime/node_modules/openclaw/node_modules/tslog/package.json 2>&1 || echo MISSING; `+
			`echo "=== wrapper ==="; cat /ocm-runtime/bin/openclaw; `+
			`echo "=== gateway-log ==="; tail -5 /var/log/openclaw-gateway.log 2>/dev/null || echo NO_LOG`,
		20*time.Second)
	t.Logf("Artifact diagnostic:\n%s", diag)

	// Wait for gateway to be fully ready (artifact binary must start successfully).
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 180*time.Second)
	t.Log("Gateway is ready using artifact runtime")

	// Verify the init script selected the artifact runtime.
	output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`cat /run/ocm-runtime-env`,
		10*time.Second)

	if !strings.Contains(output, "OCM_EFFECTIVE_RUNTIME='artifact'") {
		t.Fatalf("expected artifact runtime, got: %s", output)
	}
	t.Log("Confirmed: OCM_EFFECTIVE_RUNTIME=artifact")

	// Verify the artifact binary path is correct.
	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`test -x /ocm-runtime/bin/openclaw && echo ARTIFACT_BIN_OK || echo ARTIFACT_BIN_MISSING`,
		10*time.Second)

	if !strings.Contains(output, "ARTIFACT_BIN_OK") {
		t.Fatal("artifact binary not found at /ocm-runtime/bin/openclaw")
	}
	t.Log("Confirmed: artifact binary exists and is executable")
}

// discoverStableOpenClawVersion reads the channel manifest to find the current stable version.
// Respects TEST_OPENCLAW_MANIFEST_URI (file://…) so tests can target a locally-built
// artifact instead of the live GCS stable channel.
func discoverStableOpenClawVersion(t *testing.T) string {
	t.Helper()

	var data []byte
	if override := os.Getenv("TEST_OPENCLAW_MANIFEST_URI"); override != "" {
		localPath := strings.TrimPrefix(override, "file://")
		b, err := os.ReadFile(localPath)
		if err != nil {
			t.Fatalf("read local channel manifest %s: %v", localPath, err)
		}
		data = b
	} else {
		tmpFile := t.TempDir() + "/manifest-stable.json"
		if err := downloadFromGCS(t, "openclaw/manifest-stable.json", tmpFile); err != nil {
			t.Fatalf("download stable channel manifest: %v", err)
		}
		b, err := os.ReadFile(tmpFile)
		if err != nil {
			t.Fatalf("read channel manifest: %v", err)
		}
		data = b
	}

	var manifest struct {
		CurrentVersion string `json:"current_version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse channel manifest: %v", err)
	}
	if manifest.CurrentVersion == "" {
		t.Fatal("channel manifest has empty current_version")
	}
	return manifest.CurrentVersion
}
