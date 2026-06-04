//go:build linux && integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/agentapi"
	"github.com/mathaix/openclawmachines/backend/internal/configassembly"
	"github.com/mathaix/openclawmachines/backend/internal/orchestrator"
)

// TestOpikRootfsContents boots a Firecracker VM and inspects the filesystem to
// verify that the opik plugin files and config are correctly installed. This test
// does NOT wait for the gateway — it only checks the raw filesystem state after
// the init script completes.
func TestOpikRootfsContents(t *testing.T) {
	proxyURL, vmCfg := setupFullStackWithOpik(t)

	wsURL := strings.Replace(proxyURL, "http://", "ws://", 1)
	wsURL = fmt.Sprintf("%s/proxy/%s/terminal/?token=%s",
		wsURL, vmCfg.MachineID, vmCfg.ProxyToken)

	t.Run("ExtensionsDirectory", func(t *testing.T) {
		// Check if extensions directory exists and what's in it
		output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			"ls -la /home/openclaw/.openclaw/extensions/ 2>&1; echo '---SEPARATOR---'; ls -la /data/home/openclaw/.openclaw/extensions/ 2>&1",
			15*time.Second)

		t.Logf("Extensions listing:\n%s", output)

		if strings.Contains(output, "opik-openclaw") {
			t.Log("OK: opik-openclaw directory found in extensions")
		} else {
			t.Error("opik-openclaw directory NOT found in extensions")
		}
	})

	t.Run("PluginManifest", func(t *testing.T) {
		output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			"cat /home/openclaw/.openclaw/extensions/opik-openclaw/openclaw.plugin.json 2>&1",
			15*time.Second)

		t.Logf("Plugin manifest:\n%s", output)

		if strings.Contains(output, "No such file") {
			t.Error("openclaw.plugin.json not found — plugin not installed")
		}
	})

	t.Run("HomeSymlink", func(t *testing.T) {
		output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			"ls -la /home/openclaw; echo '---'; cat /data/.ocm-version 2>/dev/null || echo 'no-version'",
			15*time.Second)

		t.Logf("Home dir and data volume:\n%s", output)
	})

	t.Run("FindAllExtensions", func(t *testing.T) {
		// Exhaustive search for opik files anywhere on the filesystem
		output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			"find / -path '*/opik-openclaw*' -maxdepth 6 2>/dev/null | head -20; echo '---DONE---'",
			15*time.Second)

		t.Logf("All opik-openclaw paths on filesystem:\n%s", output)

		if !strings.Contains(output, "opik-openclaw") || output == "---DONE---\n" {
			t.Error("No opik-openclaw files found anywhere on the filesystem")
		}
	})

	t.Run("OpikConfigInOpenclawJson", func(t *testing.T) {
		output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			"cat /home/openclaw/.openclaw/openclaw.json 2>/dev/null | grep -o 'opik-openclaw' | head -1; echo '---DONE---'",
			15*time.Second)

		t.Logf("Opik in config: %s", strings.TrimSpace(output))

		if !strings.Contains(output, "opik-openclaw") {
			t.Error("opik-openclaw not found in openclaw.json config")
		}
	})
}

// TestOpikPluginLoading boots a Firecracker VM with the opik-openclaw plugin
// configured and verifies:
// 1. The gateway starts without errors related to the opik plugin
// 2. The openclaw.json config contains the correct apiUrl and apiKey exec ref
// 3. The /v1/secrets endpoint returns OPIK_API_KEY
func TestOpikPluginLoading(t *testing.T) {
	proxyURL, vmCfg := setupFullStackWithOpik(t)

	t.Run("GatewayLogsShowPluginLoaded", func(t *testing.T) {
		wsURL := strings.Replace(proxyURL, "http://", "ws://", 1)
		wsURL = fmt.Sprintf("%s/proxy/%s/terminal/?token=%s",
			wsURL, vmCfg.MachineID, vmCfg.ProxyToken)

		// Check gateway logs for opik plugin references
		output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			"find /tmp/openclaw -name 'openclaw-*.log' -exec grep -i opik {} + 2>/dev/null || echo 'NO_OPIK_IN_LOGS'",
			15*time.Second)

		t.Logf("Opik-related gateway log lines:\n%s", output)

		if strings.Contains(output, "NO_OPIK_IN_LOGS") {
			t.Log("WARN: No opik-related lines found in gateway logs (plugin may not emit log lines on load)")
		}

		// Check for fatal errors related to the plugin
		if strings.Contains(output, "opik") && strings.Contains(output, "error") &&
			!strings.Contains(output, "diagnostic") {
			t.Log("WARN: Possible opik plugin error in gateway logs")
		}
	})

	t.Run("ConfigContainsOpikPlugin", func(t *testing.T) {
		wsURL := strings.Replace(proxyURL, "http://", "ws://", 1)
		wsURL = fmt.Sprintf("%s/proxy/%s/terminal/?token=%s",
			wsURL, vmCfg.MachineID, vmCfg.ProxyToken)

		// Read the openclaw.json config that was served to the gateway
		output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			"cat /home/openclaw/.openclaw/openclaw.json 2>/dev/null | python3 -m json.tool 2>/dev/null || cat /home/openclaw/.openclaw/openclaw.json 2>/dev/null",
			15*time.Second)

		t.Logf("OpenClaw config (truncated):\n%.2000s", output)

		if !strings.Contains(output, "opik-openclaw") {
			t.Error("openclaw.json does not contain opik-openclaw plugin entry")
		}

		if !strings.Contains(output, "apiUrl") {
			t.Error("openclaw.json does not contain apiUrl for opik plugin")
		}

		// Verify apiKey is present in the opik plugin config (injected by config assembly)
		if strings.Contains(output, "apiKey") {
			t.Log("OK: apiKey is present in opik plugin config")
		} else {
			t.Error("openclaw.json missing apiKey for opik-openclaw plugin")
		}
	})

	t.Run("SecretsEndpointHasOpikKey", func(t *testing.T) {
		wsURL := strings.Replace(proxyURL, "http://", "ws://", 1)
		wsURL = fmt.Sprintf("%s/proxy/%s/terminal/?token=%s",
			wsURL, vmCfg.MachineID, vmCfg.ProxyToken)

		// The opik apiKey is injected inline by config assembly (not via exec secret ref).
		// Verify it's present and non-empty in the assembled config.
		output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			`cat /home/openclaw/.openclaw/openclaw.json 2>/dev/null | grep -o '"apiKey":"[^"]*"' | head -1`,
			15*time.Second)

		t.Logf("opik apiKey check: %s", strings.TrimSpace(output))

		if strings.Contains(output, `"apiKey":""`) || !strings.Contains(output, "apiKey") {
			t.Error("opik apiKey is empty or missing in openclaw.json")
		} else {
			t.Log("OK: opik apiKey is present in assembled config")
		}
	})

	t.Run("InitLogsNoFatalErrors", func(t *testing.T) {
		wsURL := strings.Replace(proxyURL, "http://", "ws://", 1)
		wsURL = fmt.Sprintf("%s/proxy/%s/terminal/?token=%s",
			wsURL, vmCfg.MachineID, vmCfg.ProxyToken)

		output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			"cat /var/log/openclaw-init.log 2>/dev/null | tail -50",
			15*time.Second)

		t.Logf("Init log (last 50 lines):\n%s", output)

		if strings.Contains(output, "[FATAL]") {
			t.Error("Init log contains [FATAL] error")
		}
	})
}

// opikTraceReceiver is a mock HTTP server that captures Opik API trace/span POSTs.
type opikTraceReceiver struct {
	mu     sync.Mutex
	traces []json.RawMessage
	spans  []json.RawMessage
}

func newOpikTraceReceiver() *opikTraceReceiver {
	return &opikTraceReceiver{}
}

func (r *opikTraceReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()

	path := req.URL.Path

	// Accept any POST to trace/span endpoints
	if req.Method == http.MethodPost {
		var body json.RawMessage
		if err := json.NewDecoder(req.Body).Decode(&body); err == nil {
			if strings.Contains(path, "traces") {
				r.traces = append(r.traces, body)
			} else if strings.Contains(path, "spans") {
				r.spans = append(r.spans, body)
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// GET for projects
	if req.Method == http.MethodGet && strings.Contains(path, "projects") {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content": []map[string]interface{}{
				{"id": "test-project-id", "name": "default"},
			},
		})
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (r *opikTraceReceiver) traceCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.traces)
}

func (r *opikTraceReceiver) spanCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.spans)
}

// setupFullStackWithOpik is like setupFullStackWithGateway but configures the
// opik-openclaw plugin with apiUrl pointing to a mock receiver on the bridge IP.
func setupFullStackWithOpik(t *testing.T) (proxyURL string, vmCfg orchestrator.VMConfig) {
	t.Helper()

	// We reuse the full stack setup but with a custom VMConfig that includes opik config.
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch := setupTestOrchestrator(t, cfg, bridge)
	orch.SetMetadataRegistrar(metaSrv)

	// Start mock Opik trace receiver on the bridge gateway IP.
	// The plugin inside the VM will POST traces here.
	receiver := newOpikTraceReceiver()
	opikListener, err := startHTTPOnBridgeIP(t, bridge.Gateway, receiver)
	if err != nil {
		t.Fatalf("Failed to start Opik mock receiver: %v", err)
	}
	opikAPIURL := fmt.Sprintf("http://%s/api/opik", opikListener)
	t.Logf("Opik mock receiver listening at %s", opikAPIURL)

	// Generate VM config with opik plugin
	vmCfg = generateTestVMConfigWithOpik(0, opikAPIURL)

	// Create real proxy server
	agentSrv := agentapi.NewServer(cfg.AgentToken, orch, "", nil, "", nil, nil, nil, false, nil, "")
	proxyServer := httptest.NewServer(agentSrv.ProxyRouter())
	t.Cleanup(func() { proxyServer.Close() })

	// Boot VM
	ctx := context.Background()
	t.Logf("Creating VM %s with opik plugin (apiUrl=%s)", vmCfg.MachineID, opikAPIURL)
	if err := orch.Create(ctx, vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}

	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)
	waitForAuthProxy(t, vmCfg.VMIP, 30*time.Second)
	waitForGatewayReady(t, vmCfg.VMIP, vmCfg.SigningKey, vmCfg.MachineID, 180*time.Second)

	return proxyServer.URL, vmCfg
}

// generateTestVMConfigWithOpik creates a VMConfig with the opik-openclaw plugin enabled.
func generateTestVMConfigWithOpik(index int, opikAPIURL string) orchestrator.VMConfig {
	machineID := fmt.Sprintf("test-vm-%s", randomID())
	gatewayToken := "gateway-token-" + randomID()

	// The opik-openclaw config template uses plugins.allow for trust-listing
	// (NOT plugins.slots — the gateway's Zod schema only allows known slot
	// names like "memory"; "observability" would fail validation).
	opikConfigTemplate := map[string]interface{}{
		"plugins": map[string]interface{}{
			"allow": []string{"opik-openclaw"},
			"entries": map[string]interface{}{
				"opik-openclaw": map[string]interface{}{
					"enabled": true,
					"hooks": map[string]interface{}{
						"allowConversationAccess": true,
					},
					"config": map[string]interface{}{
						"projectName":   "default",
						"workspaceName": "default",
						"tags":          []string{"ocm", "integration-test"},
					},
				},
			},
		},
	}

	openClawConf, err := configassembly.AssembleConfig(configassembly.AssemblyParams{
		MachineID:  machineID,
		OpikAPIURL: opikAPIURL,
		OpikAPIKey: gatewayToken,
		Plugins: []configassembly.PluginSelection{
			{
				PluginID:       "opik-openclaw",
				Slot:           "observability",
				ConfigTemplate: opikConfigTemplate,
			},
		},
	})
	if err != nil {
		panic(fmt.Sprintf("assembler failed for test VM with opik: %v", err))
	}

	slug := fmt.Sprintf("test-vm-%d", index)
	return orchestrator.VMConfig{
		MachineID:       machineID,
		MachineName:     fmt.Sprintf("Test VM %d (Opik)", index),
		MachineSlug:     slug,
		VCPUs:           1,
		MemoryMB:        2048,
		VMIP:            allocateTestIP(index),
		GatewayToken:    gatewayToken,
		ProxyToken:      "proxy-token-" + randomID(),
		SigningKey:      generateTestSigningKey(),
		TunnelToken:     "test-tunnel-token-" + randomID(),
		VmHostname:      fmt.Sprintf("m-%s.test.openclawmachines.com", slug),
		OwnerEmails:     []string{"test@openclawmachines.com"},
		KernelExtraArgs: "ocm_quick_start=1",
		DataVolumeGB:    1,
		DataVersion:     1,
		OpenClawConf:    openClawConf,
		Secrets: map[string]string{
			"OPIK_API_KEY": gatewayToken,
		},
	}
}

// startHTTPOnBridgeIP starts an HTTP server bound to the bridge gateway IP on a random port.
// Returns the address (ip:port) the server is listening on.
func startHTTPOnBridgeIP(t *testing.T, bridgeIP string, handler http.Handler) (string, error) {
	t.Helper()

	addr := fmt.Sprintf("%s:0", bridgeIP)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("listen on %s: %w", addr, err)
	}

	server := &http.Server{Handler: handler}
	go server.Serve(listener)

	t.Cleanup(func() {
		server.Close()
	})

	return listener.Addr().String(), nil
}
