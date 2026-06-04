//go:build linux && integration

package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/agentapi"
)

// ============================================================================
// Filebrowser Integration Tests
//
// Boots a real Firecracker VM and tests filebrowser access through the
// agent proxy, verifying:
//   - Filebrowser starts with noauth (no login page)
//   - HTML is served with asset paths rewritten to include machine slug
//   - Files can be listed and downloaded via the filebrowser API
//   - The full proxy chain works: agent proxy → auth proxy → filebrowser
// ============================================================================

func TestFilebrowserSuite(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)

	proxyServer := httptest.NewServer(agentapi.NewServer(cfg.AgentToken, orch, "", nil, "", nil, nil, nil, false, nil, "").ProxyRouter())
	defer proxyServer.Close()

	vmCfg := generateTestVMConfig(0)
	withDefaultRuntimeSelection(t, &vmCfg)
	vmCfg.KernelExtraArgs = "" // disable quick-start so filebrowser is started
	if err := orch.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)

	// Wait for filebrowser to start (it's launched by init script after auth proxy)
	waitForFilebrowser(t, vmCfg.VMIP, 30*time.Second)

	client := &http.Client{Timeout: 10 * time.Second}

	t.Run("DirectAccess_NoAuth", func(t *testing.T) {
		// Access filebrowser directly through auth proxy (no machine token needed for /files)
		resp, err := client.Get(fmt.Sprintf("http://%s:8080/files/", vmCfg.VMIP))
		if err != nil {
			t.Fatalf("Failed to reach filebrowser: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("Expected 200, got %d: %s", resp.StatusCode, string(body))
		}

		body, _ := io.ReadAll(resp.Body)
		html := string(body)

		// Should get the filebrowser HTML, not a login redirect
		if !strings.Contains(html, "<!doctype html>") && !strings.Contains(html, "<!DOCTYPE html>") {
			t.Errorf("Expected HTML document, got: %.200s", html)
		}

		// Should NOT contain a login form (noauth is working)
		// Filebrowser login page has id="login" or a login form
		if strings.Contains(html, `id="login"`) {
			t.Error("Filebrowser is showing login page — noauth is not working")
		}

		t.Log("Filebrowser serves HTML without login prompt")
	})

	t.Run("DirectAccess_ListFiles", func(t *testing.T) {
		// Filebrowser noauth mode: POST to /api/login returns a JWT token in the
		// response body. The SPA stores it and sends it as X-Auth header on API calls.
		token := filebrowserLogin(t, fmt.Sprintf("http://%s:8080/files/api/login", vmCfg.VMIP), nil)

		// Now list files via API with auth token
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://%s:8080/files/api/resources/", vmCfg.VMIP), nil)
		req.Header.Set("X-Auth", token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Failed to list files: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("Expected 200, got %d: %s", resp.StatusCode, string(body))
		}

		body, _ := io.ReadAll(resp.Body)
		t.Logf("File listing response: %.500s", string(body))

		// Should be JSON
		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "application/json") {
			t.Errorf("Expected JSON content type, got: %s", ct)
		}
	})

	t.Run("ProxyAccess_HTMLRewritten", func(t *testing.T) {
		// Access through agent proxy (same path as production Cloudflare worker)
		proxyURL := fmt.Sprintf("%s/proxy/%s/files/", proxyServer.URL, vmCfg.MachineID)
		req, _ := http.NewRequest("GET", proxyURL, nil)
		req.Header.Set("X-Proxy-Token", vmCfg.ProxyToken)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Proxy request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("Expected 200 through proxy, got %d: %s", resp.StatusCode, string(body))
		}

		body, _ := io.ReadAll(resp.Body)
		html := string(body)

		// Asset paths should be rewritten to include machine slug
		slug := vmCfg.MachineSlug
		if slug != "" {
			// Check that /files/ paths were rewritten to /{slug}/files/
			if strings.Contains(html, `"/files/static/`) || strings.Contains(html, `'/files/static/`) {
				t.Errorf("Found unrewritten /files/static/ path in HTML — URL rewriting not working:\n%.500s", html)
			}
			expectedPrefix := fmt.Sprintf("/%s/files/", slug)
			if !strings.Contains(html, expectedPrefix) {
				t.Errorf("Expected rewritten paths with prefix %q not found in HTML:\n%.500s", expectedPrefix, html)
			}
			t.Logf("Asset paths correctly rewritten with slug %q", slug)
		}

		// X-Frame-Options should be stripped for iframe embedding
		if resp.Header.Get("X-Frame-Options") != "" {
			t.Error("X-Frame-Options should be stripped by proxy")
		}
	})

	t.Run("ProxyAccess_CurlFileContents", func(t *testing.T) {
		// Create a test file via terminal, then download it through filebrowser
		wsURL := strings.Replace(proxyServer.URL, "http://", "ws://", 1)
		wsURL = fmt.Sprintf("%s/proxy/%s/terminal/ws", wsURL, vmCfg.MachineID)

		// Create a test file in /workspace
		testContent := "hello-from-filebrowser-test-" + randomID()
		cmd := fmt.Sprintf("echo '%s' > /workspace/test-file.txt", testContent)
		output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken, cmd, 10*time.Second)
		t.Logf("File creation output: %q", output)

		// Give filebrowser a moment to pick up the new file
		time.Sleep(1 * time.Second)

		// Get filebrowser auth token through the proxy
		proxyHeaders := map[string]string{"X-Proxy-Token": vmCfg.ProxyToken}
		loginURL := fmt.Sprintf("%s/proxy/%s/files/api/login", proxyServer.URL, vmCfg.MachineID)
		fbToken := filebrowserLogin(t, loginURL, proxyHeaders)

		// Now download the file through filebrowser API via agent proxy
		proxyURL := fmt.Sprintf("%s/proxy/%s/files/api/raw/workspace/test-file.txt", proxyServer.URL, vmCfg.MachineID)
		req, _ := http.NewRequest("GET", proxyURL, nil)
		req.Header.Set("X-Proxy-Token", vmCfg.ProxyToken)
		req.Header.Set("X-Auth", fbToken)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Failed to download file through proxy: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("Expected 200 for file download, got %d: %s", resp.StatusCode, string(body))
		}

		body, _ := io.ReadAll(resp.Body)
		downloaded := strings.TrimSpace(string(body))
		if downloaded != testContent {
			t.Errorf("File content mismatch:\n  got:  %q\n  want: %q", downloaded, testContent)
		}

		t.Logf("Successfully downloaded file through filebrowser proxy: %q", downloaded)
	})
}

// waitForFilebrowser waits until filebrowser is responding on port 8082 (via auth proxy at /files).
func waitForFilebrowser(t *testing.T, vmIP string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}

	for time.Now().Before(deadline) {
		resp, err := client.Get(fmt.Sprintf("http://%s:8080/files/", vmIP))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Log("Filebrowser is ready")
				return
			}
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("Filebrowser did not become ready within %v", timeout)
}

// filebrowserLogin POSTs to filebrowser's login endpoint (noauth mode accepts empty body)
// and returns the JWT token from the response body. Extra headers (e.g. X-Proxy-Token) can
// be passed for requests that go through the agent proxy.
func filebrowserLogin(t *testing.T, loginURL string, extraHeaders map[string]string) string {
	t.Helper()
	req, _ := http.NewRequest("POST", loginURL, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("filebrowserLogin: request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("filebrowserLogin: expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	token := strings.Trim(strings.TrimSpace(string(body)), "\"")
	if token == "" {
		t.Fatal("filebrowserLogin: got empty token")
	}
	t.Logf("filebrowserLogin: got token (len=%d)", len(token))
	return token
}
