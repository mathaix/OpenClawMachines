package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

const (
	hostUpdateKindTrigger = "trigger_update"
	hostUpdateKindDrain   = "drain_update"
	hostUpdateKindStorage = "browser_storage"

	hostUpdateStatusQueued    = "queued"
	hostUpdateStatusRunning   = "running"
	hostUpdateStatusSucceeded = "succeeded"
	hostUpdateStatusTimedOut  = "timed_out"
	hostUpdateStatusFailed    = "failed"
)

var (
	errHostUpdateOperationActive = errors.New("host update operation already active")

	hostUpdateWaitTimeout  = 7 * time.Minute
	hostUpdatePollInterval = 5 * time.Second
)

type hostUpdateOperation struct {
	ID                 string     `json:"id"`
	HostID             int        `json:"host_id"`
	HostName           string     `json:"host_name"`
	Kind               string     `json:"kind"`
	Status             string     `json:"status"`
	MachinesStopped    int        `json:"machines_stopped"`
	MachinesFailed     int        `json:"machines_failed"`
	MachinesRestarting bool       `json:"machines_restarting"`
	Message            string     `json:"message,omitempty"`
	Error              string     `json:"error,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

type hostUpdateWaitResult struct {
	status  string
	message string
}

func writeHostUpdateOperationAccepted(w http.ResponseWriter, op *hostUpdateOperation) {
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":        op.Status,
		"operation_id":  op.ID,
		"operation_url": "/api/admin/host-update-operations/" + op.ID,
		"operation":     op,
	})
}

func (s *Server) handleGetHostUpdateOperation(w http.ResponseWriter, r *http.Request) {
	operationID := chi.URLParam(r, "operationId")
	if operationID == "" {
		writeError(w, http.StatusBadRequest, "missing operation id")
		return
	}

	op, ok := s.getHostUpdateOperation(operationID)
	if !ok {
		writeError(w, http.StatusNotFound, "operation not found")
		return
	}
	writeJSON(w, http.StatusOK, op)
}

func (s *Server) createHostUpdateOperation(host *store.Host, kind string) (*hostUpdateOperation, error) {
	now := time.Now().UTC()

	s.hostUpdateOpsMu.Lock()
	defer s.hostUpdateOpsMu.Unlock()
	s.ensureHostUpdateOpsLocked()

	for _, op := range s.hostUpdateOps {
		if op.HostID == host.ID && isActiveHostUpdateOperationStatus(op.Status) {
			return nil, errHostUpdateOperationActive
		}
	}

	for attempts := 0; attempts < 5; attempts++ {
		id := "hostupd-" + generateShortID()
		if _, exists := s.hostUpdateOps[id]; exists {
			continue
		}
		op := &hostUpdateOperation{
			ID:        id,
			HostID:    host.ID,
			HostName:  host.VMName,
			Kind:      kind,
			Status:    hostUpdateStatusQueued,
			CreatedAt: now,
			UpdatedAt: now,
		}
		s.hostUpdateOps[id] = op
		return cloneHostUpdateOperation(op), nil
	}

	return nil, errors.New("failed to allocate host update operation id")
}

func (s *Server) getHostUpdateOperation(operationID string) (*hostUpdateOperation, bool) {
	s.hostUpdateOpsMu.RLock()
	defer s.hostUpdateOpsMu.RUnlock()

	op, ok := s.hostUpdateOps[operationID]
	if !ok {
		return nil, false
	}
	return cloneHostUpdateOperation(op), true
}

func (s *Server) markHostUpdateOperationRunning(operationID, message string) {
	s.updateHostUpdateOperation(operationID, func(op *hostUpdateOperation) {
		op.Status = hostUpdateStatusRunning
		op.Message = message
		op.Error = ""
	})
}

func (s *Server) updateHostUpdateOperationProgress(operationID string, machinesStopped, machinesFailed int, machinesRestarting bool, message string) {
	s.updateHostUpdateOperation(operationID, func(op *hostUpdateOperation) {
		op.MachinesStopped = machinesStopped
		op.MachinesFailed = machinesFailed
		op.MachinesRestarting = machinesRestarting
		op.Message = message
	})
}

func (s *Server) completeHostUpdateOperation(operationID, status, message, errorMessage string) {
	now := time.Now().UTC()
	s.updateHostUpdateOperation(operationID, func(op *hostUpdateOperation) {
		op.Status = status
		op.Message = message
		op.Error = errorMessage
		op.CompletedAt = &now
	})
}

func (s *Server) updateHostUpdateOperation(operationID string, fn func(*hostUpdateOperation)) {
	s.hostUpdateOpsMu.Lock()
	defer s.hostUpdateOpsMu.Unlock()
	s.ensureHostUpdateOpsLocked()

	op, ok := s.hostUpdateOps[operationID]
	if !ok {
		return
	}
	fn(op)
	op.UpdatedAt = time.Now().UTC()
}

func (s *Server) ensureHostUpdateOpsLocked() {
	if s.hostUpdateOps == nil {
		s.hostUpdateOps = make(map[string]*hostUpdateOperation)
	}
}

func isActiveHostUpdateOperationStatus(status string) bool {
	return status == hostUpdateStatusQueued || status == hostUpdateStatusRunning
}

func cloneHostUpdateOperation(op *hostUpdateOperation) *hostUpdateOperation {
	if op == nil {
		return nil
	}
	clone := *op
	if op.CompletedAt != nil {
		completedAt := *op.CompletedAt
		clone.CompletedAt = &completedAt
	}
	return &clone
}

func (s *Server) runTriggerHostUpdateOperation(ctx context.Context, operationID string, hostID int) {
	s.markHostUpdateOperationRunning(operationID, "triggering host update")

	host, err := s.store.GetHost(ctx, hostID)
	if err != nil {
		slog.Error("admin.trigger_update.host_lookup_failed", "operation_id", operationID, "host_id", hostID, "error", err)
		s.completeHostUpdateOperation(operationID, hostUpdateStatusFailed, "failed to look up host", err.Error())
		return
	}
	if host.Status != "ready" {
		msg := "host is not ready (status: " + host.Status + ")"
		slog.Warn("admin.trigger_update.host_not_ready", "operation_id", operationID, "host_id", hostID, "status", host.Status)
		s.completeHostUpdateOperation(operationID, hostUpdateStatusFailed, msg, msg)
		return
	}

	slog.Info("admin.trigger_update.starting", "operation_id", operationID, "host_id", hostID, "host_name", host.VMName)
	if err := s.store.UpdateHostStatusMessage(ctx, hostID, "updating", ""); err != nil {
		slog.Error("admin.trigger_update.host_status_failed", "operation_id", operationID, "host_id", hostID, "error", err)
	}
	if err := s.agentClient.TriggerUpdate(ctx, host); err != nil {
		slog.Error("admin.trigger_update.agent_call_failed", "operation_id", operationID, "host_id", hostID, "error", err)
		s.completeHostUpdateOperation(operationID, hostUpdateStatusFailed, "failed to trigger agent update", err.Error())
		return
	}

	s.finishHostUpdateOperationAfterWait(ctx, operationID, hostID, host.VMName, 0, 0, false)
}

func (s *Server) runDrainHostUpdateOperation(ctx context.Context, operationID string, hostID int) {
	s.markHostUpdateOperationRunning(operationID, "draining host before update")

	host, err := s.store.GetHost(ctx, hostID)
	if err != nil {
		slog.Error("admin.drain_update.host_lookup_failed", "operation_id", operationID, "host_id", hostID, "error", err)
		s.completeHostUpdateOperation(operationID, hostUpdateStatusFailed, "failed to look up host", err.Error())
		return
	}
	if host.Status != "ready" {
		msg := "host is not ready (status: " + host.Status + ")"
		slog.Warn("admin.drain_update.host_not_ready", "operation_id", operationID, "host_id", hostID, "status", host.Status)
		s.completeHostUpdateOperation(operationID, hostUpdateStatusFailed, msg, msg)
		return
	}

	slog.Info("admin.drain_update.starting", "operation_id", operationID, "host_id", hostID, "host_name", host.VMName)

	machineList, err := s.store.ListMachinesByHost(ctx, hostID)
	if err != nil {
		slog.Error("admin.drain_update.list_machines_failed", "operation_id", operationID, "host_id", hostID, "error", err)
		s.completeHostUpdateOperation(operationID, hostUpdateStatusFailed, "failed to list machines", err.Error())
		return
	}

	var running []store.Machine
	for _, m := range machineList {
		if m.Status == "running" {
			running = append(running, m)
		}
	}
	if len(running) > 0 && s.machines == nil {
		err := errors.New("machine runtime not configured")
		slog.Error("admin.drain_update.runtime_missing", "operation_id", operationID, "host_id", hostID, "running_machines", len(running))
		s.completeHostUpdateOperation(operationID, hostUpdateStatusFailed, "failed to stop running machines", err.Error())
		return
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stopCancel()

	var wg sync.WaitGroup
	var mu sync.Mutex
	var stoppedIDs []string
	var stopErrors []string

	for _, m := range running {
		wg.Add(1)
		go func(machine store.Machine) {
			defer wg.Done()
			if err := s.machines.StopForUpdate(stopCtx, &machine); err != nil {
				slog.Error("admin.drain_update.stop_failed",
					"operation_id", operationID, "machine_id", machine.ID, "host_id", hostID, "error", err)
				mu.Lock()
				stopErrors = append(stopErrors, machine.ID+": "+err.Error())
				mu.Unlock()
				return
			}
			slog.Info("admin.drain_update.stopped",
				"operation_id", operationID, "machine_id", machine.ID, "machine_slug", machine.Slug, "host_id", hostID)
			mu.Lock()
			stoppedIDs = append(stoppedIDs, machine.ID)
			mu.Unlock()
		}(m)
	}
	wg.Wait()

	if len(stopErrors) > 0 {
		slog.Error("admin.drain_update.partial_stop_failure",
			"operation_id", operationID, "host_id", hostID, "errors", strings.Join(stopErrors, "; "))
	}
	s.updateHostUpdateOperationProgress(operationID, len(stoppedIDs), len(stopErrors), true, "triggering host update")

	pendingJSON, marshalErr := json.Marshal(map[string][]string{"pending_restarts": stoppedIDs})
	if marshalErr != nil {
		slog.Error("admin.drain_update.marshal_failed", "operation_id", operationID, "host_id", hostID, "error", marshalErr)
		pendingJSON = []byte(`{"pending_restarts":[]}`)
	}
	if err := s.store.UpdateHostStatusMessage(ctx, hostID, "updating", string(pendingJSON)); err != nil {
		slog.Error("admin.drain_update.host_status_failed", "operation_id", operationID, "host_id", hostID, "error", err)
	}
	if err := s.store.SetHostMaintenanceMode(ctx, hostID, true); err != nil {
		slog.Error("admin.drain_update.maintenance_mode_failed", "operation_id", operationID, "host_id", hostID, "error", err)
	}

	if err := s.agentClient.TriggerUpdate(ctx, host); err != nil {
		slog.Error("admin.drain_update.agent_call_failed", "operation_id", operationID, "host_id", hostID, "error", err)
		s.completeHostUpdateOperation(operationID, hostUpdateStatusFailed, "failed to trigger agent update", err.Error())
		return
	}

	s.finishHostUpdateOperationAfterWait(ctx, operationID, hostID, host.VMName, len(stoppedIDs), len(stopErrors), true)
}

func (s *Server) runConfigureHostBrowserStorageOperation(ctx context.Context, operationID string, hostID int, device, mountPoint string, format bool) {
	s.markHostUpdateOperationRunning(operationID, "configuring browser storage")

	host, err := s.store.GetHost(ctx, hostID)
	if err != nil {
		slog.Error("admin.browser_storage.host_lookup_failed", "operation_id", operationID, "host_id", hostID, "error", err)
		s.completeHostUpdateOperation(operationID, hostUpdateStatusFailed, "failed to look up host", err.Error())
		return
	}
	if host.Status != "ready" {
		msg := "host is not ready (status: " + host.Status + ")"
		slog.Warn("admin.browser_storage.host_not_ready", "operation_id", operationID, "host_id", hostID, "status", host.Status)
		s.completeHostUpdateOperation(operationID, hostUpdateStatusFailed, msg, msg)
		return
	}

	slog.Info("admin.browser_storage.starting",
		"operation_id", operationID,
		"host_id", hostID,
		"host_name", host.VMName,
		"device", device,
		"mount_point", mountPoint,
		"format", format)

	if err := s.store.UpdateHostStatusMessage(ctx, hostID, "updating", `{"pending_restarts":[],"browser_storage":true}`); err != nil {
		slog.Error("admin.browser_storage.host_status_failed", "operation_id", operationID, "host_id", hostID, "error", err)
	}
	if err := s.store.SetHostMaintenanceMode(ctx, hostID, true); err != nil {
		slog.Error("admin.browser_storage.maintenance_mode_failed", "operation_id", operationID, "host_id", hostID, "error", err)
	}

	if err := s.agentClient.ConfigureBrowserStorage(ctx, host, device, mountPoint, format, true); err != nil {
		slog.Error("admin.browser_storage.agent_call_failed", "operation_id", operationID, "host_id", hostID, "error", err)
		_ = s.store.SetHostMaintenanceMode(context.Background(), hostID, false)
		_ = s.store.UpdateHostStatusMessage(context.Background(), hostID, "ready", "")
		s.completeHostUpdateOperation(operationID, hostUpdateStatusFailed, "failed to configure browser storage", err.Error())
		return
	}

	s.finishBrowserStorageOperationAfterWait(ctx, operationID, hostID, host.VMName, mountPoint)
}

func (s *Server) finishBrowserStorageOperationAfterWait(ctx context.Context, operationID string, hostID int, hostName, mountPoint string) {
	s.updateHostUpdateOperationProgress(operationID, 0, 0, false, "waiting for host agent to report browser storage ready")

	result := s.waitForBrowserStorageResult(ctx, hostID, hostName, mountPoint)
	s.completeHostUpdateOperation(operationID, result.status, result.message, "")
}

func (s *Server) waitForBrowserStorageResult(ctx context.Context, hostID int, hostName, mountPoint string) hostUpdateWaitResult {
	expectedStateDir := strings.TrimSpace(mountPoint)
	if expectedStateDir == "" {
		expectedStateDir = "/var/lib/ocm-browser"
	}

	slog.Info("admin.browser_storage.waiting_for_agent",
		"host_id", hostID,
		"host_name", hostName,
		"expected_state_dir", expectedStateDir)

	pollCtx, pollCancel := context.WithTimeout(ctx, hostUpdateWaitTimeout)
	defer pollCancel()

	for {
		h, err := s.store.GetHost(pollCtx, hostID)
		if err == nil && h.Status == "ready" && hostReportsBrowserStorageReady(h, expectedStateDir) {
			slog.Info("admin.browser_storage.completed", "host_id", hostID, "host_name", hostName, "browser_state_dir", expectedStateDir)
			return hostUpdateWaitResult{
				status:  hostUpdateStatusSucceeded,
				message: hostName + " browser storage configured and reflink ready",
			}
		}

		select {
		case <-pollCtx.Done():
			slog.Warn("admin.browser_storage.timeout", "host_id", hostID, "host_name", hostName, "expected_state_dir", expectedStateDir)
			return hostUpdateWaitResult{
				status:  hostUpdateStatusTimedOut,
				message: "browser storage command completed but host has not reported reflink-ready browser storage yet; check host status",
			}
		case <-time.After(hostUpdatePollInterval):
		}
	}
}

func hostReportsBrowserStorageReady(host *store.Host, expectedStateDir string) bool {
	if host == nil || len(host.Capabilities) == 0 {
		return false
	}
	var caps struct {
		BrowserStorage struct {
			StateDir         string `json:"state_dir"`
			BrowserStateDir  string `json:"browser_state_dir"`
			ReflinkSupported bool   `json:"reflink_supported"`
		} `json:"browser_storage"`
	}
	if err := json.Unmarshal(host.Capabilities, &caps); err != nil {
		return false
	}
	stateDir := caps.BrowserStorage.BrowserStateDir
	if stateDir == "" {
		stateDir = caps.BrowserStorage.StateDir
	}
	return caps.BrowserStorage.ReflinkSupported && stateDir == expectedStateDir
}

func (s *Server) finishHostUpdateOperationAfterWait(ctx context.Context, operationID string, hostID int, hostName string, machinesStopped int, machinesFailed int, machinesRestarting bool) {
	s.updateHostUpdateOperationProgress(operationID, machinesStopped, machinesFailed, machinesRestarting, "waiting for host agent to report ready")

	result := s.waitForHostUpdateResult(ctx, hostID, hostName, machinesStopped, machinesFailed, machinesRestarting)
	s.completeHostUpdateOperation(operationID, result.status, result.message, "")
}

func (s *Server) waitForHostUpdateResult(ctx context.Context, hostID int, hostName string, machinesStopped int, machinesFailed int, machinesRestarting bool) hostUpdateWaitResult {
	slog.Info("admin.host_update.waiting_for_agent", "host_id", hostID, "host_name", hostName, "machines_stopped", machinesStopped)

	pollCtx, pollCancel := context.WithTimeout(ctx, hostUpdateWaitTimeout)
	defer pollCancel()

	for {
		h, err := s.store.GetHost(pollCtx, hostID)
		if err == nil && h.Status == "ready" {
			slog.Info("admin.host_update.completed", "host_id", hostID, "host_name", hostName, "machines_stopped", machinesStopped)
			message := hostName + " updated and back online"
			if machinesRestarting {
				message += "; stopped machines will restart from pending restart state"
			}
			return hostUpdateWaitResult{status: hostUpdateStatusSucceeded, message: message}
		}

		select {
		case <-pollCtx.Done():
			slog.Warn("admin.host_update.timeout", "host_id", hostID, "host_name", hostName, "machines_failed", machinesFailed)
			return hostUpdateWaitResult{
				status:  hostUpdateStatusTimedOut,
				message: "update triggered but agent has not come back yet; check host status",
			}
		case <-time.After(hostUpdatePollInterval):
		}
	}
}
