package api

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

const stallCheckInterval = 5 * time.Minute
const stallThreshold = 10 * time.Minute

// StartStallDetectionLoop runs a background goroutine that periodically
// checks for workflows that have been running/waiting with no events
// for longer than the stall threshold.
func (s *Server) StartStallDetectionLoop(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(stallCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				stalled, err := s.store.FindStalledWorkflows(ctx, stallThreshold)
				if err != nil {
					// Don't spam errors if workflow_runs table hasn't been created yet
					if strings.Contains(err.Error(), "does not exist") {
						slog.Debug("stall_detection.skipped", "reason", "table not yet created")
					} else {
						slog.Error("stall_detection.failed", "error", err)
					}
					continue
				}
				for _, wf := range stalled {
					slog.Warn("stall_detection.stalled_workflow",
						"workflow_id", wf.ID,
						"kind", wf.Kind,
						"scope", wf.ScopeType+"/"+wf.ScopeID,
						"last_event_at", wf.LastEventAt,
						"stuck_minutes", time.Since(wf.LastEventAt).Minutes(),
					)
				}
			}
		}
	}()
	slog.Info("stall_detection.loop.started", "interval", stallCheckInterval, "threshold", stallThreshold)
}
