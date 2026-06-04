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

// TestSmokeOcmptyd boots a VM and verifies that the PTY server is served by
// the standalone ocmptyd binary (not agent --pty-server). This catches rootfs
// build issues where ocmptyd is missing or the init script still references
// the old agent binary.
//
// Run: make smoke-test (included in the pre-upload gate)
func TestSmokeOcmptyd(t *testing.T) {
	vmCfg := generateTestVMConfig(0)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: getStableOpenClawVersion(t),
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
		OpenClawManifestURI:     defaultOpenClawManifestURI,
	}
	vmCfg.DataVolumeGB = 5

	_, wsURL, vmCfg := setupInitTestVM(t, vmCfg)

	// 1. Verify terminal works (basic PTY smoke)
	output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		"echo PTY_SMOKE_OK", 15*time.Second)
	if !strings.Contains(output, "PTY_SMOKE_OK") {
		t.Fatalf("terminal echo failed, output: %q", output)
	}
	t.Log("1. Terminal works")

	// 2. Verify the PTY server process is ocmptyd (not agent)
	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`ps -eo pid,comm | grep -E 'ocmptyd|agent.*pty' | head -5; `+
			`echo PTYD_CHECK:$(pgrep -c ocmptyd 2>/dev/null || echo 0):$(pgrep -cf 'agent.*pty-server' 2>/dev/null || echo 0)`,
		10*time.Second)
	t.Logf("PTY process check: %s", output)

	if strings.Contains(output, "PTYD_CHECK:0:") {
		t.Fatal("ocmptyd process not found — PTY server may still be using agent --pty-server")
	}
	if !strings.Contains(output, "PTYD_CHECK:") {
		t.Fatalf("could not parse PTY process check, output: %q", output)
	}
	t.Log("2. Confirmed: ocmptyd is serving the PTY")

	// 3. Verify the /health endpoint works (served by ocmptyd)
	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`curl -sf http://127.0.0.1:7681/health && echo HEALTH_OK || echo HEALTH_FAIL`,
		10*time.Second)
	if !strings.Contains(output, "HEALTH_OK") {
		t.Fatalf("ocmptyd /health endpoint failed, output: %q", output)
	}
	t.Log("3. Confirmed: ocmptyd /health endpoint responding")

	// 4. Verify session reconnect via session_id.
	// Open a raw WebSocket, capture the session_id from the "s" message,
	// write a unique marker, disconnect, reconnect with ?session_id=,
	// and verify the replay buffer ("r" message) contains the marker.
	t.Log("4. Testing session reconnect...")
	testPTYSessionReconnect(t, wsURL, vmCfg.ProxyToken)
	t.Log("4. Session reconnect verified — replay buffer contains marker from previous connection")
}

func TestSmokeOcmServiceCLI(t *testing.T) {
	vmCfg := generateTestVMConfig(0)
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: getStableOpenClawVersion(t),
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
		OpenClawManifestURI:     defaultOpenClawManifestURI,
	}
	vmCfg.DataVolumeGB = 5

	_, wsURL, vmCfg := setupInitTestVM(t, vmCfg)

	const (
		serviceName = "smoke-http"
		servicePort = "8765"
		serviceDir  = "/home/openclaw/.openclaw/workspace/smoke-http"
	)

	output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		fmt.Sprintf(`mkdir -p %s && printf 'service-ok\n' > %s/index.html && echo SETUP_OK`, serviceDir, serviceDir),
		15*time.Second,
	)
	if !strings.Contains(output, "SETUP_OK") {
		t.Fatalf("service setup failed: %q", output)
	}

	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		fmt.Sprintf(`ocm service create %s --cmd "exec python3 -m http.server %s" --cwd %s --port %s`,
			serviceName, servicePort, serviceDir, servicePort),
		30*time.Second,
	)
	if !strings.Contains(output, "created service "+serviceName) {
		t.Fatalf("service create output missing success marker: %q", output)
	}

	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		fmt.Sprintf(`for i in $(seq 1 20); do sv status %s 2>/dev/null | grep -q '^run:' && break; sleep 1; done; sv status %s`,
			serviceName, serviceName),
		30*time.Second,
	)
	if !strings.Contains(output, "run:") {
		t.Fatalf("service did not report a running state: %q", output)
	}

	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken, `ocm service list`, 15*time.Second)
	if !strings.Contains(output, serviceName) {
		t.Fatalf("service list did not include the created service: %q", output)
	}

	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		fmt.Sprintf(`curl -sf http://127.0.0.1:%s/index.html`, servicePort),
		15*time.Second,
	)
	if !strings.Contains(output, "service-ok") {
		t.Fatalf("service did not serve expected HTTP body: %q", output)
	}

	output = testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		fmt.Sprintf(`ocm service remove %s && if [ -e /var/service/u-%s ]; then echo REMOVE_FAIL; else echo REMOVE_OK; fi`,
			serviceName, serviceName),
		20*time.Second,
	)
	if !strings.Contains(output, "removed service "+serviceName) || !strings.Contains(output, "REMOVE_OK") {
		t.Fatalf("service remove did not complete cleanly: %q", output)
	}

	t.Log("ocm service create/list/status/remove worked against a real VM")
}

// testPTYSessionReconnect exercises the real PTY reconnect path:
// connect → capture session_id → write marker → disconnect → reconnect
// with session_id → verify replay contains marker.
func testPTYSessionReconnect(t *testing.T, wsURL, proxyToken string) {
	t.Helper()

	fullURL := wsURL
	if !strings.Contains(fullURL, "?") {
		fullURL += "?token=" + proxyToken
	} else {
		fullURL += "&token=" + proxyToken
	}

	// --- First connection: create session, write marker ---
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn1, _, err := dialer.Dial(fullURL, nil)
	if err != nil {
		t.Fatalf("first WebSocket dial failed: %v", err)
	}

	// Read messages until we get the session ID ("s" prefix)
	var sessionID string
	conn1.SetReadDeadline(time.Now().Add(10 * time.Second))
	for i := 0; i < 100; i++ {
		_, data, err := conn1.ReadMessage()
		if err != nil {
			t.Fatalf("reading session ID: %v", err)
		}
		if len(data) > 1 && data[0] == 's' {
			sessionID = string(data[1:])
			break
		}
	}
	if sessionID == "" {
		t.Fatal("never received session ID from PTY server")
	}
	t.Logf("  session_id: %s", sessionID)

	// Send a resize so the shell prompt renders
	_ = conn1.WriteMessage(websocket.TextMessage, []byte(`1{"columns":200,"rows":50}`))

	// Wait for shell prompt
	conn1.SetReadDeadline(time.Now().Add(10 * time.Second))
	for i := 0; i < 200; i++ {
		_, data, err := conn1.ReadMessage()
		if err != nil {
			t.Fatalf("waiting for prompt: %v", err)
		}
		if len(data) > 0 && data[0] == '0' && strings.Contains(string(data[1:]), "$ ") {
			break
		}
	}

	// Write a unique marker
	marker := fmt.Sprintf("SESSRECONN_%s", randomID())
	cmd := fmt.Sprintf("0echo %s\n", marker)
	if err := conn1.WriteMessage(websocket.TextMessage, []byte(cmd)); err != nil {
		t.Fatalf("writing marker: %v", err)
	}

	// Read until we see the marker echoed back (confirms it's in the ring buffer)
	conn1.SetReadDeadline(time.Now().Add(10 * time.Second))
	markerSeen := false
	for i := 0; i < 200; i++ {
		_, data, err := conn1.ReadMessage()
		if err != nil {
			break
		}
		if len(data) > 0 && data[0] == '0' && strings.Contains(string(data[1:]), marker) {
			markerSeen = true
			break
		}
	}
	if !markerSeen {
		t.Fatal("marker never echoed back on first connection")
	}

	// Disconnect (close without sending close frame — simulates network drop)
	conn1.Close()
	time.Sleep(500 * time.Millisecond) // let ocmptyd notice the disconnect

	// --- Second connection: reconnect with session_id ---
	reconnectURL := fullURL + "&session_id=" + sessionID
	conn2, _, err := dialer.Dial(reconnectURL, nil)
	if err != nil {
		t.Fatalf("reconnect WebSocket dial failed: %v", err)
	}
	defer conn2.Close()

	// Read messages — expect "s" (same session ID) then "r" (replay with marker)
	var gotSessionID string
	var replayData string
	conn2.SetReadDeadline(time.Now().Add(10 * time.Second))
	for i := 0; i < 100; i++ {
		_, data, err := conn2.ReadMessage()
		if err != nil {
			t.Fatalf("reading from reconnected session: %v", err)
		}
		if len(data) == 0 {
			continue
		}
		switch data[0] {
		case 's':
			gotSessionID = string(data[1:])
		case 'r':
			replayData = string(data[1:])
		}
		// Stop once we have both
		if gotSessionID != "" && replayData != "" {
			break
		}
	}

	if gotSessionID != sessionID {
		t.Fatalf("reconnected to different session: got %q, want %q", gotSessionID, sessionID)
	}
	if !strings.Contains(replayData, marker) {
		t.Fatalf("replay buffer does not contain marker %q, replay: %q", marker, replayData)
	}
}
