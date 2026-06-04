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

// TestSmokeHermesArtifactRuntime downloads Hermes rootfs/runtime artifacts,
// boots a Hermes VM, and verifies the runtime drive plus supervised services
// are reachable through the same Firecracker path OpenClaw uses.
//
// Run:
//
//	TEST_HERMES_MANIFEST_URI=gs://example-ocm-artifacts/hermes/manifest-stable.json \
//	TEST_HERMES_ROOTFS_MANIFEST_URI=gs://example-ocm-artifacts/hermes-rootfs/manifest.json \
//	go test -tags=integration ./internal/integration -run TestSmokeHermesArtifactRuntime -v
func TestSmokeHermesArtifactRuntime(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	hermesVersion := discoverStableHermesVersion(t)
	rootfsVersion := discoverHermesRootfsVersion(t)
	t.Logf("Hermes version: %s", hermesVersion)
	t.Logf("Hermes rootfs version: %s", rootfsVersion)

	vmCfg := generateTestVMConfig(0)
	vmCfg.MachineKind = "hermes"
	vmCfg.MachineName = "Hermes Smoke"
	vmCfg.OpenClawConf = nil
	vmCfg.HermesConfigYAML = []byte("model:\n  provider: auto\nplatforms:\n  api_server:\n    enabled: true\n    host: 127.0.0.1\n    port: 18789\ndashboard:\n  host: 127.0.0.1\n  port: 9119\n")
	vmCfg.HermesEnv = []byte("API_SERVER_ENABLED=true\nAPI_SERVER_HOST=127.0.0.1\nAPI_SERVER_PORT=18789\nHERMES_DASHBOARD=1\nHERMES_DASHBOARD_HOST=127.0.0.1\nHERMES_DASHBOARD_PORT=9119\n")
	vmCfg.HermesAuthJSON = []byte("{}")
	vmCfg.HermesSoul = []byte("You are running the Hermes Firecracker smoke test.\n")
	vmCfg.DataVolumeGB = 5
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		Kind:                  "hermes",
		ResolvedRootfsVersion: rootfsVersion,
		ResolvedHermesVersion: hermesVersion,
		VersionSource:         "pinned",
		RuntimeSource:         "artifact",
		RootfsManifestURI:     cfg.HermesRootfsManifestURI,
		HermesManifestURI:     cfg.HermesManifestURI,
	}

	_, wsURL, vmCfg := setupInitTestVMWithPrepare(t, vmCfg, nil)

	time.Sleep(5 * time.Second)
	diag := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`echo "=== hermes-runtime ==="; ls -la /opt/hermes/.venv/bin/hermes; `+
			`echo "=== hermes-home ==="; ls -la /opt/data; `+
			`echo "=== services ==="; sv status /var/service/hermes-gateway /var/service/hermes-dashboard /var/service/authproxy /var/service/pty-server 2>&1; `+
			`echo "=== logs ==="; tail -20 /var/log/hermes-gateway.log 2>/dev/null || true`,
		20*time.Second)
	t.Logf("Hermes diagnostic:\n%s", diag)
	if strings.Contains(diag, "GLIBC_") {
		t.Fatalf("Hermes runtime has glibc compatibility errors:\n%s", diag)
	}

	output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`test -x /opt/hermes/.venv/bin/hermes && echo HERMES_BIN_OK || echo HERMES_BIN_MISSING`,
		10*time.Second)
	if !strings.Contains(output, "HERMES_BIN_OK") {
		t.Fatalf("Hermes artifact binary missing: %s", output)
	}

	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`mount | grep ' /opt/hermes ' && test -w /opt/hermes && touch /opt/hermes/.ocm-write-test && rm -f /opt/hermes/.ocm-write-test && echo HERMES_RUNTIME_WRITABLE`,
		10*time.Second)
	if !strings.Contains(output, "HERMES_RUNTIME_WRITABLE") {
		t.Fatalf("Hermes runtime is not writable for self-update: %s", output)
	}

	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`git -C /opt/hermes rev-parse --is-inside-work-tree && echo HERMES_GIT_OK`,
		10*time.Second)
	if !strings.Contains(output, "HERMES_GIT_OK") {
		t.Fatalf("Hermes runtime is not a git checkout for self-update: %s", output)
	}

	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`curl -sf http://127.0.0.1:8080/health && echo AUTHPROXY_OK`,
		10*time.Second)
	if !strings.Contains(output, "AUTHPROXY_OK") {
		t.Fatalf("authproxy health failed: %s", output)
	}

	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`for i in $(seq 1 90); do gateway_ready=; dashboard_ready=; `+
			`curl -sf http://127.0.0.1:18789/health >/tmp/hermes-gateway-health.json && gateway_ready=1 || true; `+
			`curl -sf http://127.0.0.1:9119/ >/tmp/hermes-dashboard.html && dashboard_ready=1 || true; `+
			`if [ -n "$gateway_ready" ] && [ -n "$dashboard_ready" ]; then cat /tmp/hermes-gateway-health.json; echo HERMES_GATEWAY_OK; echo HERMES_DASHBOARD_OK; break; fi; `+
			`sleep 1; done; `+
			`[ -n "$gateway_ready" ] || { echo HERMES_GATEWAY_FAILED; tail -80 /var/log/hermes-gateway.log 2>/dev/null || true; }; `+
			`[ -n "$dashboard_ready" ] || { echo HERMES_DASHBOARD_FAILED; tail -80 /var/log/hermes-dashboard.log 2>/dev/null || true; }`,
		120*time.Second)
	if !strings.Contains(output, "HERMES_GATEWAY_OK") {
		t.Fatalf("Hermes gateway health failed: %s", output)
	}
	if !strings.Contains(output, "HERMES_DASHBOARD_OK") {
		t.Fatalf("Hermes dashboard health failed: %s", output)
	}
}

func discoverStableHermesVersion(t *testing.T) string {
	t.Helper()
	data := readChannelManifest(t, "TEST_HERMES_MANIFEST_URI", "hermes/manifest-stable.json")
	var manifest struct {
		CurrentVersion string `json:"current_version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse Hermes channel manifest: %v", err)
	}
	if manifest.CurrentVersion == "" {
		t.Fatal("Hermes channel manifest has empty current_version")
	}
	return manifest.CurrentVersion
}

func discoverHermesRootfsVersion(t *testing.T) string {
	t.Helper()
	data := readChannelManifest(t, "TEST_HERMES_ROOTFS_MANIFEST_URI", "hermes-rootfs/manifest.json")
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse Hermes rootfs manifest: %v", err)
	}
	if manifest.Version == "" {
		t.Fatal("Hermes rootfs manifest has empty version")
	}
	return manifest.Version
}

func readChannelManifest(t *testing.T, envKey, gcsObject string) []byte {
	t.Helper()
	if override := os.Getenv(envKey); override != "" {
		localPath := strings.TrimPrefix(override, "file://")
		b, err := os.ReadFile(localPath)
		if err != nil {
			t.Fatalf("read local manifest %s: %v", localPath, err)
		}
		return b
	}
	tmpFile := t.TempDir() + "/manifest.json"
	if err := downloadFromGCS(t, gcsObject, tmpFile); err != nil {
		t.Fatalf("download manifest %s: %v", gcsObject, err)
	}
	b, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("read downloaded manifest: %v", err)
	}
	return b
}
