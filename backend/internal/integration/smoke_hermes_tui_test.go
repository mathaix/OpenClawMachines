//go:build linux && integration

package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mathaix/openclawmachines/backend/internal/metadata"
)

// TestSmokeHermesTUICommandRuntime boots a Hermes VM and verifies that the
// browser terminal's command=hermes path starts the real Hermes TUI and echoes
// typed input promptly. This covers the interactive Ink TUI path, not just the
// shell and service-health path covered by TestSmokeHermesArtifactRuntime.
func TestSmokeHermesTUICommandRuntime(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	hermesVersion := discoverStableHermesVersion(t)
	rootfsVersion := discoverHermesRootfsVersion(t)
	t.Logf("Hermes version: %s", hermesVersion)
	t.Logf("Hermes rootfs version: %s", rootfsVersion)

	vmCfg := generateTestVMConfig(0)
	vmCfg.MachineKind = "hermes"
	vmCfg.MachineName = "Hermes TUI Smoke"
	vmCfg.OpenClawConf = nil
	vmCfg.HermesConfigYAML = []byte("model:\n  provider: auto\nplatforms:\n  api_server:\n    enabled: true\n    host: 127.0.0.1\n    port: 18789\ndashboard:\n  host: 127.0.0.1\n  port: 9119\n")
	vmCfg.HermesEnv = []byte("API_SERVER_ENABLED=true\nAPI_SERVER_HOST=127.0.0.1\nAPI_SERVER_PORT=18789\nHERMES_DASHBOARD=1\nHERMES_DASHBOARD_HOST=127.0.0.1\nHERMES_DASHBOARD_PORT=9119\n")
	vmCfg.HermesAuthJSON = []byte("{}")
	vmCfg.HermesSoul = []byte("You are running the Hermes TUI Firecracker smoke test.\n")
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
	waitForAuthProxy(t, vmCfg.VMIP, 60*time.Second)
	out := exerciseHermesTUI(t, wsURL, vmCfg.ProxyToken, 90*time.Second)
	t.Logf("Hermes TUI output sample:\n%s", tailString(out, 3000))
}

func exerciseHermesTUI(t *testing.T, wsURL, proxyToken string, timeout time.Duration) string {
	t.Helper()

	fullURL := wsURL
	if strings.Contains(fullURL, "?") {
		fullURL += "&command=hermes"
	} else {
		fullURL += "?command=hermes"
	}
	fullURL += "&token=" + proxyToken

	conn := dialHermesTUI(t, fullURL, 30*time.Second)
	defer conn.Close()

	if err := conn.WriteMessage(websocket.BinaryMessage, []byte(`1{"columns":120,"rows":40}`)); err != nil {
		t.Fatalf("send initial TUI resize: %v", err)
	}

	var output strings.Builder
	deadline := time.Now().Add(timeout)
	_ = conn.SetReadDeadline(deadline)
	sawTUIFrame := false
	for time.Now().Before(deadline) {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("Hermes TUI connection closed before initial frame: %v; output:\n%s", err, output.String())
		}
		if len(data) == 0 || data[0] != '0' {
			continue
		}
		text := string(data[1:])
		output.WriteString(text)
		if len(text) > 200 || strings.Contains(strings.ToLower(text), "hermes") {
			sawTUIFrame = true
			break
		}
	}
	if !sawTUIFrame {
		t.Fatalf("Hermes TUI did not draw an initial frame within %s; output:\n%s", timeout, output.String())
	}

	probe := "ocm-tui-probe"
	start := time.Now()
	_ = conn.SetReadDeadline(start.Add(10 * time.Second))
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("0"+probe)); err != nil {
		t.Fatalf("send Hermes TUI input probe: %v", err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte(`1{"columns":100,"rows":32}`)); err != nil {
		t.Fatalf("send Hermes TUI resize probe: %v", err)
	}

	for time.Since(start) < 10*time.Second {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("Hermes TUI connection closed before input probe echo: %v; output:\n%s", err, output.String())
		}
		if len(data) == 0 || data[0] != '0' {
			continue
		}
		text := string(data[1:])
		output.WriteString(text)
		if strings.Contains(output.String(), probe) {
			t.Logf("Hermes TUI echoed input probe in %s", time.Since(start).Round(time.Millisecond))
			_ = conn.WriteMessage(websocket.BinaryMessage, []byte("0\x03"))
			return output.String()
		}
	}

	t.Fatalf("Hermes TUI did not echo input probe within 10s; output:\n%s", output.String())
	return output.String()
}

func dialHermesTUI(t *testing.T, fullURL string, timeout time.Duration) *websocket.Conn {
	t.Helper()

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	deadline := time.Now().Add(timeout)
	var lastErr error
	var lastStatus string
	for time.Now().Before(deadline) {
		conn, resp, err := dialer.Dial(fullURL, nil)
		if err == nil {
			return conn
		}
		lastErr = err
		if resp != nil {
			lastStatus = resp.Status
			_ = resp.Body.Close()
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("Hermes TUI WebSocket dial failed after %s: %v %s", timeout, lastErr, lastStatus)
	return nil
}

func tailString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return fmt.Sprintf("...%s", s[len(s)-max:])
}
