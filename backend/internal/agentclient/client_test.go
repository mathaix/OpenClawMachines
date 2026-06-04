package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

func TestDestroyVMTreatsNotFoundAsSuccess(t *testing.T) {
	t.Parallel()

	server, host := newTestAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/vms/m-123" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	client := New("fleet-token")
	client.httpClient = hostClientForAlias(t, server)

	if err := client.DestroyVM(context.Background(), host, "m-123"); err != nil {
		t.Fatalf("DestroyVM should treat 404 as success, got %v", err)
	}
}

func TestStopVMTreatsNotFoundAsSuccess(t *testing.T) {
	t.Parallel()

	server, host := newTestAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/vms/m-123/stop" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	client := New("fleet-token")
	client.httpClient = hostClientForAlias(t, server)

	if err := client.StopVM(context.Background(), host, "m-123"); err != nil {
		t.Fatalf("StopVM should treat 404 as success, got %v", err)
	}
}

func TestUpgradeVM(t *testing.T) {
	t.Parallel()

	server, host := newTestAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/vms/m-123/upgrade" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		var req VMRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.MachineID != "m-123" {
			t.Fatalf("expected machine_id m-123, got %q", req.MachineID)
		}
		if req.RuntimeSelection == nil || req.RuntimeSelection.ResolvedRootfsVersion != "rootfs-r2" {
			t.Fatalf("unexpected runtime selection: %+v", req.RuntimeSelection)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write(bytes.NewBufferString(`{"machine_id":"m-123","status":"upgrading","vm_ip":"192.168.100.10"}`).Bytes())
	})
	defer server.Close()

	client := New("fleet-token")
	client.httpClient = hostClientForAlias(t, server)

	gatewayToken := "gw"
	proxyToken := "px"
	signingKey := "sig"
	hostname := "m-test.openclawmachines.com"
	machine := &store.Machine{
		ID:             "m-123",
		Name:           "Test",
		Slug:           "test",
		AccountID:      1,
		VCPUs:          2,
		MemoryMB:       2048,
		DataVolumeGB:   10,
		GatewayToken:   &gatewayToken,
		ProxyToken:     &proxyToken,
		SigningKey:     &signingKey,
		TunnelHostname: &hostname,
		TunnelToken:    "tunnel",
	}

	resp, err := client.UpgradeVM(context.Background(), host, machine, "192.168.100.10", nil, nil, 2, nil, "", nil, nil, nil, nil, nil, nil, nil, &metadata.RuntimeSelection{
		ResolvedRootfsVersion:   "rootfs-r2",
		ResolvedOpenClawVersion: "openclaw-v2",
	})
	if err != nil {
		t.Fatalf("UpgradeVM returned error: %v", err)
	}
	if resp.Status != "upgrading" {
		t.Fatalf("expected upgrading status, got %q", resp.Status)
	}
}

func TestCreateBrowserVMTimeoutIsOutcomeUnknown(t *testing.T) {
	origTimeout := browserVMCreateHTTPTimeout
	browserVMCreateHTTPTimeout = 20 * time.Millisecond
	t.Cleanup(func() { browserVMCreateHTTPTimeout = origTimeout })

	server, host := newTestAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/browser-vms" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
	})
	defer server.Close()

	client := New("fleet-token")
	client.httpClient = hostClientForAlias(t, server)

	err := client.CreateBrowserVM(context.Background(), host, "bvm-1", "192.168.100.2", 2, 4096, "", "")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !IsOutcomeUnknown(err) {
		t.Fatalf("expected outcome unknown error, got %v", err)
	}
}

func TestCreateBrowserVMAcceptsAcceptedStatus(t *testing.T) {
	const rootfsManifest = "gs://example-ocm-artifacts/kernel-browser-rootfs/manifest.json"
	const rootfsVersion = "kernel-browser-rootfs-v1"
	server, host := newTestAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/browser-vms" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["rootfs_manifest"] != rootfsManifest {
			t.Fatalf("rootfs_manifest = %v, want %q", req["rootfs_manifest"], rootfsManifest)
		}
		if req["rootfs_version"] != rootfsVersion {
			t.Fatalf("rootfs_version = %v, want %q", req["rootfs_version"], rootfsVersion)
		}
		w.WriteHeader(http.StatusAccepted)
	})
	defer server.Close()

	client := New("fleet-token")
	client.httpClient = hostClientForAlias(t, server)

	if err := client.CreateBrowserVM(context.Background(), host, "bvm-1", "192.168.100.2", 2, 4096, rootfsManifest, rootfsVersion); err != nil {
		t.Fatalf("CreateBrowserVM returned error for 202 Accepted: %v", err)
	}
}

func newTestAgentServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *store.Host) {
	t.Helper()

	server := httptest.NewServer(handler)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	endpoint := fmt.Sprintf("http://agent.test:%s", port)
	return server, &store.Host{
		ID:            7,
		AgentEndpoint: &endpoint,
	}
}

func hostClientForAlias(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()

	actualAddr := server.Listener.Addr().String()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "agent.test:") {
				addr = actualAddr
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

// --- ExecCommand tests ---

// newExecTestAgent creates a test agent server with an aliased hostname (agent.test)
// and returns a Client configured to route to it. ExecCommand uses c.httpClient,
// so the alias-based transport works.
func newExecTestAgent(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *store.Host, *Client) {
	t.Helper()
	server, host := newTestAgentServer(t, handler)
	client := New("")
	client.httpClient = hostClientForAlias(t, server)
	return server, host, client
}

func TestExecCommand_Success(t *testing.T) {
	t.Parallel()
	server, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		cmd, ok := req["command"].([]interface{})
		if !ok || len(cmd) != 3 {
			t.Errorf("expected command with 3 elements, got %v", req["command"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ExecResult{
			ExitCode: 0,
			Stdout:   `{"hash":"abc123"}`,
		})
	})
	defer server.Close()

	result, err := client.ExecCommand(context.Background(), host, "m-test", "test-token",
		[]string{"openclaw", "gateway", "call"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", result.ExitCode)
	}
	if result.Stdout != `{"hash":"abc123"}` {
		t.Errorf("stdout = %q, want {\"hash\":\"abc123\"}", result.Stdout)
	}
}

func TestExecCommand_NonZeroExit(t *testing.T) {
	t.Parallel()
	server, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ExecResult{
			ExitCode: 1,
			Stderr:   "command not found",
		})
	})
	defer server.Close()

	result, err := client.ExecCommand(context.Background(), host, "m-test", "tok", []string{"bad"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", result.ExitCode)
	}
	if result.Stderr != "command not found" {
		t.Errorf("stderr = %q, want 'command not found'", result.Stderr)
	}
}

func TestExecCommand_HTTPError(t *testing.T) {
	t.Parallel()
	server, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	})
	defer server.Close()

	_, err := client.ExecCommand(context.Background(), host, "m-test", "tok", []string{"cmd"})
	if err == nil {
		t.Fatal("expected error for HTTP 500, got nil")
	}
}

func TestExecCommand_ProxyTokenHeader(t *testing.T) {
	t.Parallel()
	var gotToken string
	server, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Proxy-Token")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ExecResult{ExitCode: 0})
	})
	defer server.Close()

	_, err := client.ExecCommand(context.Background(), host, "m-test", "secret-proxy-tok", []string{"echo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotToken != "secret-proxy-tok" {
		t.Errorf("proxy token = %q, want 'secret-proxy-tok'", gotToken)
	}
}

func TestExecCommand_RequestPath(t *testing.T) {
	t.Parallel()
	var gotPath string
	server, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ExecResult{ExitCode: 0})
	})
	defer server.Close()

	_, err := client.ExecCommand(context.Background(), host, "m-abc-123", "tok", []string{"echo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/proxy/m-abc-123/exec" {
		t.Errorf("path = %q, want '/proxy/m-abc-123/exec'", gotPath)
	}
}
