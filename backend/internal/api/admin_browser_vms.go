package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
)

type browserVMReconcileResponse struct {
	BrowserVMID string                               `json:"browser_vm_id"`
	Action      string                               `json:"action"`
	Status      string                               `json:"status"`
	Message     string                               `json:"message,omitempty"`
	AgentHealth *agentclient.BrowserVMHealthResponse `json:"agent_health,omitempty"`
}

const browserVMReconcileTimeout = 15 * time.Second

func (s *Server) handleListBrowserVMReconcileCandidates(w http.ResponseWriter, r *http.Request) {
	bvms, err := s.store.ListBrowserVMsRequiringReconciliation(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list browser VM reconcile candidates")
		return
	}
	writeJSON(w, http.StatusOK, bvms)
}

func (s *Server) handleReconcileBrowserVM(w http.ResponseWriter, r *http.Request) {
	if s.agentClient == nil {
		writeError(w, http.StatusServiceUnavailable, "agent client not configured")
		return
	}

	bvmID := chi.URLParam(r, "browserVmId")
	resp, err := s.reconcileBrowserVM(r.Context(), bvmID)
	if err != nil {
		status := http.StatusInternalServerError
		if httpErr, ok := err.(interface{ HTTPStatus() int }); ok {
			status = httpErr.HTTPStatus()
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type browserVMReconcileError struct {
	status  int
	message string
}

func (e browserVMReconcileError) Error() string   { return e.message }
func (e browserVMReconcileError) HTTPStatus() int { return e.status }

func newBrowserVMReconcileError(status int, message string) error {
	return browserVMReconcileError{status: status, message: message}
}

func (s *Server) reconcileBrowserVM(ctx context.Context, bvmID string) (*browserVMReconcileResponse, error) {
	if s.agentClient == nil {
		return nil, newBrowserVMReconcileError(http.StatusServiceUnavailable, "agent client not configured")
	}

	bvm, err := s.store.GetBrowserVM(ctx, bvmID)
	if err != nil {
		return nil, newBrowserVMReconcileError(http.StatusNotFound, "browser VM not found")
	}
	if bvm.HostID == nil {
		return nil, newBrowserVMReconcileError(http.StatusConflict, "browser VM has no assigned host")
	}

	host, err := s.store.GetHost(ctx, *bvm.HostID)
	if err != nil || host == nil {
		return nil, newBrowserVMReconcileError(http.StatusServiceUnavailable, "host unavailable")
	}

	queryCtx, cancel := context.WithTimeout(ctx, browserVMReconcileTimeout)
	defer cancel()
	health, err := s.agentClient.GetBrowserVMHealth(queryCtx, host, bvmID)
	if err != nil {
		if agentclient.IsHTTPStatus(err, http.StatusNotFound) {
			if relErr := s.store.ReleaseBrowserVMPlacement(ctx, bvmID); relErr != nil {
				return nil, newBrowserVMReconcileError(http.StatusInternalServerError, "failed to release browser VM placement")
			}
			if unassignErr := s.store.UnassignBrowserVMFromHost(ctx, bvmID); unassignErr != nil {
				return nil, newBrowserVMReconcileError(http.StatusInternalServerError, "failed to unassign browser VM from host")
			}
			if statusErr := s.store.UpdateBrowserVMStatus(ctx, bvmID, "stopped", nil); statusErr != nil {
				return nil, newBrowserVMReconcileError(http.StatusInternalServerError, "failed to update browser VM status")
			}
			return &browserVMReconcileResponse{
				BrowserVMID: bvmID,
				Action:      "released_absent_agent_resource",
				Status:      "stopped",
				Message:     "agent reported browser VM not found; placement released",
			}, nil
		}
		return nil, newBrowserVMReconcileError(http.StatusBadGateway, "failed to query browser VM on agent: "+err.Error())
	}

	if health.CDPReachable || health.Status == "ready" {
		if err := s.store.ActivateBrowserVMPlacement(ctx, bvmID); err != nil {
			return nil, newBrowserVMReconcileError(http.StatusInternalServerError, "failed to activate browser VM placement")
		}
		if err := s.store.UpdateBrowserVMStatus(ctx, bvmID, "running", nil); err != nil {
			return nil, newBrowserVMReconcileError(http.StatusInternalServerError, "failed to update browser VM status")
		}
		return &browserVMReconcileResponse{
			BrowserVMID: bvmID,
			Action:      "marked_running",
			Status:      "running",
			Message:     "agent reported browser VM ready",
			AgentHealth: health,
		}, nil
	}

	msg := "agent still has browser VM but CDP is not ready"
	if health.CDPError != "" {
		msg = msg + ": " + health.CDPError
	}
	_ = s.store.UpdateBrowserVMStatus(ctx, bvmID, "error", &msg)
	return &browserVMReconcileResponse{
		BrowserVMID: bvmID,
		Action:      "kept_agent_resource",
		Status:      "error",
		Message:     msg,
		AgentHealth: health,
	}, nil
}

func (s *Server) reconcileBrowserVMCandidatesOnce(ctx context.Context) (int, error) {
	if s.agentClient == nil {
		return 0, nil
	}
	bvms, err := s.store.ListBrowserVMsRequiringReconciliation(ctx)
	if err != nil {
		return 0, err
	}
	reconciled := 0
	for _, bvm := range bvms {
		resp, err := s.reconcileBrowserVM(ctx, bvm.ID)
		if err != nil {
			slog.Warn("browser_vm.reconciler.item_failed",
				"browser_vm_id", bvm.ID,
				"host_id", bvm.HostID,
				"vm_ip", bvm.VMIP,
				"error", err)
			continue
		}
		reconciled++
		slog.Info("browser_vm.reconciler.item_completed",
			"browser_vm_id", bvm.ID,
			"action", resp.Action,
			"status", resp.Status)
	}
	return reconciled, nil
}

func (s *Server) StartBrowserVMReconciler(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	if s.agentClient == nil {
		slog.Info("browser_vm.reconciler.disabled", "reason", "agent client not configured")
		return
	}
	slog.Info("browser_vm.reconciler.started", "interval", interval.String())
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				slog.Info("browser_vm.reconciler.stopped")
				return
			case <-ticker.C:
				started := time.Now()
				n, err := s.reconcileBrowserVMCandidatesOnce(ctx)
				if err != nil {
					slog.Error("browser_vm.reconciler.failed", "error", err)
					continue
				}
				if n > 0 {
					slog.Info("browser_vm.reconciler.completed", "count", n, "duration_ms", time.Since(started).Milliseconds())
				}
			}
		}
	}()
}
