package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

const (
	browseSessionLeaseDuration   = 3 * time.Minute
	browseSessionJanitorInterval = 1 * time.Minute
)

// browserConfigJSON is the config pushed to the gateway via ConfigBatch
// when a browse session starts. It tells the gateway to attach to the
// user's Chrome via the SSH reverse tunnel on localhost:9222.
const browserConfigJSON = `{"enabled":true,"cdpUrl":"http://127.0.0.1:9222","attachOnly":true,"noSandbox":true,"headless":false}`

func (s *Server) handleCreateBrowseSession(w http.ResponseWriter, r *http.Request) {
	claims := auth.UserFromContext(r.Context())
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}
	if machine.Status != "running" {
		writeError(w, http.StatusConflict, "machine is not running")
		return
	}

	var req struct {
		CDPTarget string `json:"cdp_target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CDPTarget == "" {
		req.CDPTarget = "127.0.0.1:9222"
	}

	session := &store.BrowseSession{
		MachineID: machineID,
		AccountID: accountID,
		CDPTarget: req.CDPTarget,
		ExpiresAt: time.Now().Add(browseSessionLeaseDuration),
	}

	if err := s.store.CreateBrowseSession(r.Context(), session); err != nil {
		if store.IsConflict(err) {
			writeError(w, http.StatusConflict, "a browse session is already active for this machine")
		} else {
			slog.Error("browse_session.create_failed", "machine_id", machineID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to create browse session")
		}
		return
	}

	// Set CDP target on agent (host-side proxy routing)
	if s.agentClient != nil && machine.HostID != nil {
		host, err := s.store.GetHost(r.Context(), *machine.HostID)
		if err != nil {
			slog.Error("browse_session.host_lookup_failed", "machine_id", machineID, "error", err)
			_ = s.store.DeleteBrowseSession(r.Context(), session.ID)
			writeError(w, http.StatusInternalServerError, "failed to resolve host")
			return
		}
		if err := s.agentClient.SetCDPTarget(r.Context(), host, machineID, req.CDPTarget); err != nil {
			slog.Error("browse_session.cdp_target_failed", "machine_id", machineID, "error", err)
			_ = s.store.DeleteBrowseSession(r.Context(), session.ID)
			writeError(w, http.StatusBadGateway, "failed to set CDP target on agent")
			return
		}

		// Push browser config via ConfigBatch
		if machine.ProxyToken != nil {
			ops := []agentclient.ConfigOp{{
				Op: "set", Path: "browser", Value: browserConfigJSON, StrictJSON: true,
			}}
			if errs := s.agentClient.ConfigBatch(r.Context(), host, machineID, *machine.ProxyToken, ops); len(errs) > 0 {
				slog.Error("browse_session.config_push_failed", "machine_id", machineID, "error", errs[0])
				// Best effort: CDP target is set, config push failed. Clean up CDP target too.
				_ = s.agentClient.ResetCDPTarget(r.Context(), host, machineID)
				_ = s.store.DeleteBrowseSession(r.Context(), session.ID)
				writeError(w, http.StatusBadGateway, "failed to push browser config")
				return
			}

			// Browser config changes require a gateway restart (hot-reload ignores them).
			if err := s.agentClient.RestartGateway(r.Context(), host, machineID, *machine.ProxyToken); err != nil {
				slog.Warn("browse_session.gateway_restart_failed", "machine_id", machineID, "error", err)
				// Non-fatal: config is on disk, gateway will pick it up on next restart.
			}
		}
	}

	slog.Info("browse_session.created", "session_id", session.ID, "machine_id", machineID)

	logParams := events.LogParams{
		Category:  "machine",
		Action:    "machine.browse_started",
		Status:    "success",
		ActorType: "user",
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Started browse session on '%s'", machine.Name),
		Detail:    map[string]any{"session_id": session.ID, "cdp_target": req.CDPTarget},
	}
	if claims != nil {
		logParams.ActorID = &claims.UserID
	}
	s.activity.Log(r.Context(), logParams)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"session_id": session.ID,
		"expires_at": session.ExpiresAt.Format(time.RFC3339),
	})
}

func (s *Server) handleHeartbeatBrowseSession(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")
	sessionID := chi.URLParam(r, "sessionId")

	session, err := s.store.GetBrowseSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "browse session not found")
		return
	}
	if session.AccountID != accountID || session.MachineID != machineID {
		writeError(w, http.StatusForbidden, "session does not belong to this account/machine")
		return
	}

	newExpiry := time.Now().Add(browseSessionLeaseDuration)
	if err := s.store.HeartbeatBrowseSession(r.Context(), sessionID, newExpiry); err != nil {
		writeError(w, http.StatusNotFound, "browse session not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"expires_at": newExpiry.Format(time.RFC3339),
	})
}

func (s *Server) handleDeleteBrowseSession(w http.ResponseWriter, r *http.Request) {
	claims := auth.UserFromContext(r.Context())
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")
	sessionID := chi.URLParam(r, "sessionId")

	session, err := s.store.GetBrowseSession(r.Context(), sessionID)
	if err != nil {
		// Idempotent: already deleted or expired
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if session.AccountID != accountID || session.MachineID != machineID {
		writeError(w, http.StatusForbidden, "session does not belong to this account/machine")
		return
	}

	s.cleanupBrowseSession(r.Context(), session)

	logParams := events.LogParams{
		Category:  "machine",
		Action:    "machine.browse_ended",
		Status:    "success",
		ActorType: "user",
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Ended browse session on machine %s", machineID),
		Detail:    map[string]any{"session_id": session.ID},
	}
	if claims != nil {
		logParams.ActorID = &claims.UserID
	}
	s.activity.Log(r.Context(), logParams)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// cleanupBrowseSession removes browser config, resets CDP target, and deletes the DB record.
// If remote cleanup fails on a running machine, the DB record is kept so the janitor can retry.
// Returns true if the session was fully cleaned up (DB row deleted).
func (s *Server) cleanupBrowseSession(ctx context.Context, session *store.BrowseSession) bool {
	machine, err := s.store.GetMachine(ctx, session.MachineID)
	if err != nil {
		slog.Warn("browse_session.cleanup.machine_not_found", "session_id", session.ID, "machine_id", session.MachineID)
		_ = s.store.DeleteBrowseSession(ctx, session.ID)
		return true
	}

	remoteCleanupFailed := false
	if s.agentClient != nil && machine.HostID != nil && machine.Status == "running" {
		host, err := s.store.GetHost(ctx, *machine.HostID)
		if err != nil {
			// Can't reach host — defer cleanup so janitor retries.
			slog.Warn("browse_session.cleanup.host_lookup_failed", "session_id", session.ID, "error", err)
			return false
		}
		// Unset browser config
		if machine.ProxyToken != nil {
			ops := []agentclient.ConfigOp{{Op: "unset", Path: "browser"}}
			if errs := s.agentClient.ConfigBatch(ctx, host, session.MachineID, *machine.ProxyToken, ops); len(errs) > 0 {
				slog.Warn("browse_session.cleanup.config_unset_failed", "session_id", session.ID, "error", errs[0])
				remoteCleanupFailed = true
			}
			// Restart gateway so it drops the browser attachment.
			if err := s.agentClient.RestartGateway(ctx, host, session.MachineID, *machine.ProxyToken); err != nil {
				slog.Warn("browse_session.cleanup.gateway_restart_failed", "session_id", session.ID, "error", err)
				remoteCleanupFailed = true
			}
		}
		// Reset CDP target
		if err := s.agentClient.ResetCDPTarget(ctx, host, session.MachineID); err != nil {
			slog.Warn("browse_session.cleanup.cdp_reset_failed", "session_id", session.ID, "error", err)
			remoteCleanupFailed = true
		}
	}

	if remoteCleanupFailed {
		slog.Warn("browse_session.cleanup.deferred", "session_id", session.ID, "machine_id", session.MachineID)
		return false // keep DB record so janitor can retry
	}

	_ = s.store.DeleteBrowseSession(ctx, session.ID)
	slog.Info("browse_session.cleaned_up", "session_id", session.ID, "machine_id", session.MachineID)
	return true
}

// StartBrowseSessionJanitor runs a background loop that cleans up expired browse sessions.
func (s *Server) StartBrowseSessionJanitor(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(browseSessionJanitorInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				expired, err := s.store.ListExpiredBrowseSessions(ctx)
				if err != nil {
					slog.Error("browse_session.janitor.list_failed", "error", err)
					continue
				}
				for _, session := range expired {
					slog.Info("browse_session.janitor.expiring", "session_id", session.ID, "machine_id", session.MachineID)
					if !s.cleanupBrowseSession(ctx, &session) {
						continue // cleanup deferred — don't log expiry yet
					}

					s.activity.Log(ctx, events.LogParams{
						Category:  "machine",
						Action:    "machine.browse_expired",
						Status:    "success",
						ActorType: "system",
						AccountID: &session.AccountID,
						MachineID: &session.MachineID,
						Summary:   "Browse session expired (no heartbeat)",
						Detail:    map[string]any{"session_id": session.ID},
					})
				}
			}
		}
	}()
	slog.Info("browse_session.janitor.started", "interval", browseSessionJanitorInterval)
}
