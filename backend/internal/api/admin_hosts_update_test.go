package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type mockAdminHostUpdateStore struct {
	store.Store

	mu sync.Mutex

	host                *store.Host
	machines            []store.Machine
	statusUpdates       []struct{ status, message string }
	maintenanceUpdates  []bool
	getHostCallCount    int
	listMachinesByHostN int
}

type adminHostRoundTripFunc func(*http.Request) (*http.Response, error)

func (f adminHostRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (m *mockAdminHostUpdateStore) setHostStatus(status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.host.Status = status
}

func (m *mockAdminHostUpdateStore) setHostCapabilities(capabilities json.RawMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.host.Capabilities = capabilities
}

func (m *mockAdminHostUpdateStore) GetHost(_ context.Context, id int) (*store.Host, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getHostCallCount++
	return m.host, nil
}

func (m *mockAdminHostUpdateStore) ListMachinesByHost(_ context.Context, hostID int) ([]store.Machine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listMachinesByHostN++
	return append([]store.Machine(nil), m.machines...), nil
}

func (m *mockAdminHostUpdateStore) UpdateHostStatusMessage(_ context.Context, id int, status, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.host.Status = status
	m.host.StatusMessage = &message
	m.statusUpdates = append(m.statusUpdates, struct{ status, message string }{status: status, message: message})
	return nil
}

func (m *mockAdminHostUpdateStore) SetHostMaintenanceMode(_ context.Context, id int, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.host.MaintenanceMode = enabled
	m.maintenanceUpdates = append(m.maintenanceUpdates, enabled)
	return nil
}

func TestHandleTriggerHostUpdate_DoesNotDrainOrEnterMaintenance(t *testing.T) {
	setFastHostUpdatePolling(t)

	host := &store.Host{ID: 7, VMName: "host-7", Status: "ready"}
	var mockStore *mockAdminHostUpdateStore
	hostEndpoint := "https://agent.example.test"
	host.AgentEndpoint = &hostEndpoint
	mockStore = &mockAdminHostUpdateStore{host: host}
	agentCli := agentclient.New("")
	agentCli.SetHTTPClient(&http.Client{
		Transport: adminHostRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != hostEndpoint+"/trigger-update" {
				t.Fatalf("unexpected agent URL %q", req.URL.String())
			}
			mockStore.setHostStatus("ready")
			return &http.Response{
				StatusCode: http.StatusAccepted,
				Body:       io.NopCloser(http.NoBody),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	})
	srv := &Server{store: mockStore, agentClient: agentCli}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/hosts/7/trigger-update", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("hostId", "7")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	srv.handleTriggerHostUpdate(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	operationID := decodeHostUpdateOperationID(t, w)
	op := waitForHostUpdateOperationStatus(t, srv, operationID, hostUpdateStatusSucceeded)
	if op.MachinesStopped != 0 {
		t.Fatalf("expected no machines stopped, got %+v", op)
	}
	if op.MachinesRestarting {
		t.Fatalf("did not expect machine restarts for hot update, got %+v", op)
	}
	if mockStore.listMachinesByHostN != 0 {
		t.Fatalf("expected hot update to skip machine listing, got %d calls", mockStore.listMachinesByHostN)
	}
	if len(mockStore.maintenanceUpdates) != 0 {
		t.Fatalf("expected hot update to skip maintenance mode, got %+v", mockStore.maintenanceUpdates)
	}
	if len(mockStore.statusUpdates) != 1 || mockStore.statusUpdates[0].status != "updating" || mockStore.statusUpdates[0].message != "" {
		t.Fatalf("unexpected status updates: %+v", mockStore.statusUpdates)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/admin/host-update-operations/"+operationID, nil)
	getRouteCtx := chi.NewRouteContext()
	getRouteCtx.URLParams.Add("operationId", operationID)
	getReq = getReq.WithContext(context.WithValue(getReq.Context(), chi.RouteCtxKey, getRouteCtx))
	getW := httptest.NewRecorder()

	srv.handleGetHostUpdateOperation(getW, getReq)

	if getW.Code != http.StatusOK {
		t.Fatalf("expected operation lookup 200, got %d: %s", getW.Code, getW.Body.String())
	}
	var lookedUp hostUpdateOperation
	if err := json.Unmarshal(getW.Body.Bytes(), &lookedUp); err != nil {
		t.Fatalf("decode operation lookup: %v", err)
	}
	if lookedUp.ID != operationID || lookedUp.Status != hostUpdateStatusSucceeded {
		t.Fatalf("unexpected looked up operation: %+v", lookedUp)
	}
}

func TestHandleDrainHostUpdate_PreservesMaintenanceWorkflow(t *testing.T) {
	setFastHostUpdatePolling(t)

	host := &store.Host{ID: 9, VMName: "host-9", Status: "ready"}
	var mockStore *mockAdminHostUpdateStore
	hostEndpoint := "https://agent.example.test"
	host.AgentEndpoint = &hostEndpoint
	mockStore = &mockAdminHostUpdateStore{host: host}
	agentCli := agentclient.New("")
	agentCli.SetHTTPClient(&http.Client{
		Transport: adminHostRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != hostEndpoint+"/trigger-update" {
				t.Fatalf("unexpected agent URL %q", req.URL.String())
			}
			mockStore.setHostStatus("ready")
			return &http.Response{
				StatusCode: http.StatusAccepted,
				Body:       io.NopCloser(http.NoBody),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	})
	srv := &Server{store: mockStore, agentClient: agentCli}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/hosts/9/drain-update", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("hostId", "9")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	srv.handleDrainHostUpdate(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	operationID := decodeHostUpdateOperationID(t, w)
	op := waitForHostUpdateOperationStatus(t, srv, operationID, hostUpdateStatusSucceeded)
	if op.MachinesStopped != 0 || op.MachinesFailed != 0 || !op.MachinesRestarting {
		t.Fatalf("unexpected operation counters: %+v", op)
	}
	if mockStore.listMachinesByHostN != 1 {
		t.Fatalf("expected drain update to list machines once, got %d", mockStore.listMachinesByHostN)
	}
	if len(mockStore.maintenanceUpdates) != 1 || !mockStore.maintenanceUpdates[0] {
		t.Fatalf("expected drain update to enable maintenance mode, got %+v", mockStore.maintenanceUpdates)
	}
	if len(mockStore.statusUpdates) != 1 {
		t.Fatalf("expected one status update, got %+v", mockStore.statusUpdates)
	}
	var payload struct {
		PendingRestarts []string `json:"pending_restarts"`
	}
	if err := json.Unmarshal([]byte(mockStore.statusUpdates[0].message), &payload); err != nil {
		t.Fatalf("expected pending restart payload, got %q: %v", mockStore.statusUpdates[0].message, err)
	}
	if len(payload.PendingRestarts) != 0 {
		t.Fatalf("expected no pending restarts, got %+v", payload.PendingRestarts)
	}
}

func TestHandleConfigureHostBrowserStorage_EntersMaintenanceAndCallsAgent(t *testing.T) {
	setFastHostUpdatePolling(t)

	host := &store.Host{ID: 11, VMName: "host-11", Status: "ready"}
	hostEndpoint := "https://agent.example.test"
	host.AgentEndpoint = &hostEndpoint
	mockStore := &mockAdminHostUpdateStore{
		host:     host,
		machines: []store.Machine{{ID: "machine-running", Status: "running"}},
	}
	agentCli := agentclient.New("")
	agentCli.SetHTTPClient(&http.Client{
		Transport: adminHostRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != hostEndpoint+"/configure-browser-storage" {
				t.Fatalf("unexpected agent URL %q", req.URL.String())
			}
			var body struct {
				Device       string `json:"device"`
				MountPoint   string `json:"mount_point"`
				Format       bool   `json:"format"`
				RestartAgent bool   `json:"restart_agent"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode agent request: %v", err)
			}
			if body.Device != "/dev/nvme1n1" || !body.Format || !body.RestartAgent {
				t.Fatalf("unexpected agent request: %+v", body)
			}
			mockStore.setHostCapabilities(json.RawMessage(`{"browser_storage":{"browser_state_dir":"/var/lib/ocm-browser","reflink_supported":true}}`))
			mockStore.setHostStatus("ready")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(http.NoBody),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	})
	srv := &Server{store: mockStore, agentClient: agentCli}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/hosts/11/configure-browser-storage", bytes.NewBufferString(`{"device":"/dev/nvme1n1","format":true}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("hostId", "11")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	srv.handleConfigureHostBrowserStorage(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	operationID := decodeHostUpdateOperationID(t, w)
	op := waitForHostUpdateOperationStatus(t, srv, operationID, hostUpdateStatusSucceeded)
	if op.Kind != hostUpdateKindStorage {
		t.Fatalf("expected storage operation, got %+v", op)
	}
	if mockStore.listMachinesByHostN != 0 {
		t.Fatalf("expected browser storage operation to leave normal machines alone, got %d machine-list calls", mockStore.listMachinesByHostN)
	}
	if len(mockStore.maintenanceUpdates) != 1 || !mockStore.maintenanceUpdates[0] {
		t.Fatalf("expected operation to enable maintenance mode, got %+v", mockStore.maintenanceUpdates)
	}
	if len(mockStore.statusUpdates) != 1 || mockStore.statusUpdates[0].status != "updating" || !strings.Contains(mockStore.statusUpdates[0].message, "browser_storage") {
		t.Fatalf("unexpected status updates: %+v", mockStore.statusUpdates)
	}
}

func TestHandleTriggerHostUpdate_RejectsConcurrentOperation(t *testing.T) {
	host := &store.Host{ID: 7, VMName: "host-7", Status: "ready"}
	mockStore := &mockAdminHostUpdateStore{host: host}
	now := time.Now().UTC()
	srv := &Server{
		store: mockStore,
		hostUpdateOps: map[string]*hostUpdateOperation{
			"hostupd-active": {
				ID:        "hostupd-active",
				HostID:    host.ID,
				HostName:  host.VMName,
				Kind:      hostUpdateKindTrigger,
				Status:    hostUpdateStatusRunning,
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/hosts/7/trigger-update", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("hostId", "7")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	srv.handleTriggerHostUpdate(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func setFastHostUpdatePolling(t *testing.T) {
	t.Helper()

	oldTimeout := hostUpdateWaitTimeout
	oldInterval := hostUpdatePollInterval
	hostUpdateWaitTimeout = 2 * time.Second
	hostUpdatePollInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		hostUpdateWaitTimeout = oldTimeout
		hostUpdatePollInterval = oldInterval
	})
}

func decodeHostUpdateOperationID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()

	var resp struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.OperationID == "" {
		t.Fatalf("expected operation_id in response: %s", w.Body.String())
	}
	return resp.OperationID
}

func waitForHostUpdateOperationStatus(t *testing.T, srv *Server, operationID string, wantStatus string) *hostUpdateOperation {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		op, ok := srv.getHostUpdateOperation(operationID)
		if ok && op.Status == wantStatus {
			return op
		}
		time.Sleep(10 * time.Millisecond)
	}

	op, ok := srv.getHostUpdateOperation(operationID)
	if !ok {
		t.Fatalf("operation %s not found", operationID)
	}
	t.Fatalf("operation %s did not reach %s; last state: %+v", operationID, wantStatus, op)
	return nil
}
