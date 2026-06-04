package agentapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

var updateInProgress atomic.Bool

func (s *Server) handleDrain(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	vms, err := s.orchestrator.List(r.Context())
	if err != nil {
		slog.Warn("drain.list_failed", "error", err)
	}
	vmCount := len(vms)
	slog.Info("drain.start", "vm_count", vmCount)

	if err := s.orchestrator.Drain(ctx); err != nil {
		slog.Error("drain.failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "drained",
		"vm_count": vmCount,
	})
}

func (s *Server) handleTriggerUpdate(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status":  "unavailable",
			"message": "self-update not configured",
		})
		return
	}

	if !updateInProgress.CompareAndSwap(false, true) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"status":  "already_updating",
			"message": "an update is already in progress",
		})
		return
	}

	// Count running VMs for the response
	vms, err := s.orchestrator.List(r.Context())
	if err != nil {
		slog.Warn("trigger_update.list_failed", "error", err)
	}
	vmCount := len(vms)

	slog.Info("trigger_update.accepted", "vm_count", vmCount)

	// Return 202 immediately, do the work in background
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status":   "updating",
		"vm_count": vmCount,
	})

	go func() {
		defer updateInProgress.Store(false)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// Run self-update check. Recovery happens after agent restart.
		slog.Info("trigger_update.checking")
		updated, err := s.updater.CheckAndUpdate(ctx)
		if err != nil {
			slog.Error("trigger_update.failed", "error", err)
			return
		}
		if !updated {
			slog.Info("trigger_update.already_current")
		}
		// If updated, the agent will restart via systemctl — this goroutine will be killed
	}()
}
