package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type heartbeatRoundTripFunc func(*http.Request) (*http.Response, error)

func (f heartbeatRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type mockHeartbeatStore struct {
	store.Store
	host          *store.Host
	hostHeartbeat struct {
		hostID               int
		externalIP           string
		agentVersion         string
		rootfsVersion        string
		browserRootfsVersion string
		openclawVersion      string
		capabilities         json.RawMessage
	}
	actualVersions map[string]struct {
		hostID   int
		rootfs   string
		openclaw string
	}
	artifactStates []*store.HostArtifactState
	machines       []store.Machine
	browserVMs     map[string]*store.BrowserVM
}

func (m *mockHeartbeatStore) GetHost(_ context.Context, id int) (*store.Host, error) {
	return m.host, nil
}

func (m *mockHeartbeatStore) UpdateHostHeartbeat(_ context.Context, id int, externalIP, agentVersion, rootfsVersion, browserRootfsVersion, openclawVersion string, capabilities json.RawMessage) (bool, error) {
	m.hostHeartbeat.hostID = id
	m.hostHeartbeat.externalIP = externalIP
	m.hostHeartbeat.agentVersion = agentVersion
	m.hostHeartbeat.rootfsVersion = rootfsVersion
	m.hostHeartbeat.browserRootfsVersion = browserRootfsVersion
	m.hostHeartbeat.openclawVersion = openclawVersion
	m.hostHeartbeat.capabilities = append(json.RawMessage(nil), capabilities...)
	return false, nil
}

func (m *mockHeartbeatStore) UpsertHostArtifactState(_ context.Context, s *store.HostArtifactState) error {
	m.artifactStates = append(m.artifactStates, s)
	return nil
}

func (m *mockHeartbeatStore) UpdateMachineActualVersions(_ context.Context, hostID int, machineID, rootfsVersion, openclawVersion string) error {
	if m.actualVersions == nil {
		m.actualVersions = make(map[string]struct {
			hostID   int
			rootfs   string
			openclaw string
		})
	}
	m.actualVersions[machineID] = struct {
		hostID   int
		rootfs   string
		openclaw string
	}{
		hostID:   hostID,
		rootfs:   rootfsVersion,
		openclaw: openclawVersion,
	}
	return nil
}

func (m *mockHeartbeatStore) ListMachinesByHost(_ context.Context, hostID int) ([]store.Machine, error) {
	return m.machines, nil
}

func (m *mockHeartbeatStore) GetBrowserVM(_ context.Context, id string) (*store.BrowserVM, error) {
	if m.browserVMs == nil {
		return nil, nil
	}
	return m.browserVMs[id], nil
}

func TestHandleAgentHeartbeat_PersistsVMVersions(t *testing.T) {
	mock := &mockHeartbeatStore{
		host: &store.Host{
			ID:     1,
			Status: "ready",
		},
	}
	srv := &Server{
		store:      mock,
		agentToken: "fleet-token",
	}

	body, err := json.Marshal(map[string]any{
		"host_id":          1,
		"external_ip":      "203.0.113.9",
		"agent_version":    "agent-1",
		"rootfs_version":   "host-rootfs-r7",
		"openclaw_version": "host-openclaw-v7",
		"capabilities": map[string]any{
			"storage": map[string]any{
				"fs_type":           "xfs",
				"reflink_supported": true,
			},
			"browser_rootfs": map[string]any{
				"lineage": "kernel-images-experimental",
			},
		},
		"vm_versions": map[string]any{
			"machine-1": map[string]string{
				"rootfs":   "vm-rootfs-r6",
				"openclaw": "vm-openclaw-v6",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/agent/heartbeat", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer fleet-token")
	w := httptest.NewRecorder()

	srv.handleAgentHeartbeat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := mock.actualVersions["machine-1"]; got.hostID != 1 || got.rootfs != "vm-rootfs-r6" || got.openclaw != "vm-openclaw-v6" {
		t.Fatalf("unexpected machine actual versions: %+v", got)
	}
	if mock.hostHeartbeat.rootfsVersion != "host-rootfs-r7" {
		t.Fatalf("expected host rootfs version to be persisted, got %q", mock.hostHeartbeat.rootfsVersion)
	}
	if mock.hostHeartbeat.openclawVersion != "host-openclaw-v7" {
		t.Fatalf("expected host openclaw version to be persisted, got %q", mock.hostHeartbeat.openclawVersion)
	}
	if !bytes.Contains(mock.hostHeartbeat.capabilities, []byte(`"reflink_supported":true`)) {
		t.Fatalf("expected host capabilities to be persisted, got %s", string(mock.hostHeartbeat.capabilities))
	}
	if len(mock.artifactStates) != 2 {
		t.Fatalf("expected 2 host artifact state upserts, got %d", len(mock.artifactStates))
	}
}

func TestHandleAgentHeartbeat_ReplaysBrowserPairing(t *testing.T) {
	hostExternalIP := "127.0.0.1"
	host := &store.Host{
		ID:         1,
		Status:     "ready",
		ExternalIP: &hostExternalIP,
	}
	machineVMIP := "192.168.100.21"
	browserVMIP := "192.168.100.3"
	browserVMID := "bvm-1"
	mock := &mockHeartbeatStore{
		host: host,
		machines: []store.Machine{{
			ID:          "machine-1",
			Status:      "running",
			HostID:      &host.ID,
			VMIP:        &machineVMIP,
			BrowserVMID: &browserVMID,
		}},
		browserVMs: map[string]*store.BrowserVM{
			browserVMID: {
				ID:     browserVMID,
				Status: "running",
				HostID: &host.ID,
				VMIP:   &browserVMIP,
			},
		},
	}

	var pairCalls, cdpCalls int
	fakeEndpoint := "http://agent.test"
	host.AgentEndpoint = &fakeEndpoint
	agentCli := agentclient.New("fleet-token")
	agentCli.SetHTTPClient(&http.Client{
		Transport: heartbeatRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			status := http.StatusNotFound
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/browser-vms/bvm-1/pair":
				pairCalls++
				status = http.StatusOK
			case r.Method == http.MethodPost && r.URL.Path == "/vms/machine-1/cdp-target":
				cdpCalls++
				status = http.StatusOK
			}
			return &http.Response{
				StatusCode: status,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("ok")),
				Request:    r,
			}, nil
		}),
	})

	srv := &Server{
		store:       mock,
		agentToken:  "fleet-token",
		agentClient: agentCli,
	}

	body, err := json.Marshal(map[string]any{
		"host_id":       1,
		"external_ip":   "203.0.113.9",
		"agent_version": "agent-1",
		"vm_count":      1,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/agent/heartbeat", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer fleet-token")
	w := httptest.NewRecorder()

	srv.handleAgentHeartbeat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if pairCalls != 1 {
		t.Fatalf("expected 1 pair replay call, got %d", pairCalls)
	}
	if cdpCalls != 1 {
		t.Fatalf("expected 1 cdp target replay call, got %d", cdpCalls)
	}
}
