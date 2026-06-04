package agentapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/orchestrator"
)

func TestHandleConfigureBrowserStorageAllowsActiveMachineVMs(t *testing.T) {
	resetUpdateInProgressForTest(t)

	orch := newMockOrchestrator()
	orch.addVM("machine-1", "192.168.100.10", "proxy-token")
	srv := &Server{orchestrator: orch}

	origConfigure := configureBrowserStorageOnHost
	configureBrowserStorageOnHost = func(context.Context, configureBrowserStorageRequest) (*configureBrowserStorageResult, error) {
		return &configureBrowserStorageResult{
			BrowserStateDir:  "/var/lib/ocm-browser",
			MountPoint:       "/var/lib/ocm-browser",
			FSType:           "xfs",
			ReflinkSupported: true,
		}, nil
	}
	t.Cleanup(func() { configureBrowserStorageOnHost = origConfigure })

	req := httptest.NewRequest(http.MethodPost, "/configure-browser-storage", bytes.NewBufferString(`{"device":"/dev/nvme1n1","format":true}`))
	w := httptest.NewRecorder()

	srv.handleConfigureBrowserStorage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleConfigureBrowserStorageRejectsActiveBrowserVMs(t *testing.T) {
	resetUpdateInProgressForTest(t)

	orch := newMockOrchestrator()
	orch.browserVMs["bvm-1"] = orchestrator.BrowserVMInstance{ID: "bvm-1", VMIP: "192.168.100.3", Status: "running"}
	srv := &Server{orchestrator: orch}

	called := false
	origConfigure := configureBrowserStorageOnHost
	configureBrowserStorageOnHost = func(context.Context, configureBrowserStorageRequest) (*configureBrowserStorageResult, error) {
		called = true
		return &configureBrowserStorageResult{}, nil
	}
	t.Cleanup(func() { configureBrowserStorageOnHost = origConfigure })

	req := httptest.NewRequest(http.MethodPost, "/configure-browser-storage", bytes.NewBufferString(`{"device":"/dev/nvme1n1","format":true}`))
	w := httptest.NewRecorder()

	srv.handleConfigureBrowserStorage(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if called {
		t.Fatal("configure should not be called while active VMs exist")
	}
}

func TestHandleConfigureBrowserStorageConfiguresAndSchedulesRestart(t *testing.T) {
	resetUpdateInProgressForTest(t)

	orch := newMockOrchestrator()
	srv := &Server{orchestrator: orch}

	origConfigure := configureBrowserStorageOnHost
	configureBrowserStorageOnHost = func(_ context.Context, req configureBrowserStorageRequest) (*configureBrowserStorageResult, error) {
		if req.Device != "/dev/nvme1n1" || !req.Format || !req.RestartAgent {
			t.Fatalf("unexpected request: %+v", req)
		}
		return &configureBrowserStorageResult{
			BrowserStateDir:  "/var/lib/ocm-browser",
			MountPoint:       "/var/lib/ocm-browser",
			FSType:           "xfs",
			ReflinkSupported: true,
		}, nil
	}
	t.Cleanup(func() { configureBrowserStorageOnHost = origConfigure })

	restarted := make(chan struct{})
	origRestart := restartAgentService
	restartAgentService = func() error {
		close(restarted)
		return nil
	}
	t.Cleanup(func() { restartAgentService = origRestart })

	req := httptest.NewRequest(http.MethodPost, "/configure-browser-storage", bytes.NewBufferString(`{"device":"/dev/nvme1n1","format":true,"restart_agent":true}`))
	w := httptest.NewRecorder()

	srv.handleConfigureBrowserStorage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("agent restart was not scheduled")
	}
}
