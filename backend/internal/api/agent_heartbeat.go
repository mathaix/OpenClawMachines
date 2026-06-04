package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/routing"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type machineActualVersionUpdater interface {
	UpdateMachineActualVersions(ctx context.Context, hostID int, machineID, rootfsVersion, openclawVersion string) error
}

func (s *Server) reconcileHostBrowserPairings(ctx context.Context, host *store.Host) {
	if s.agentClient == nil || host == nil {
		return
	}

	machines, err := s.store.ListMachinesByHost(ctx, host.ID)
	if err != nil {
		slog.Warn("heartbeat.browser_pairings.list_machines_failed", "host_id", host.ID, "error", err)
		return
	}

	for _, machine := range machines {
		if machine.Status != "running" || machine.BrowserVMID == nil || machine.VMIP == nil {
			continue
		}

		bvm, err := s.store.GetBrowserVM(ctx, *machine.BrowserVMID)
		if err != nil {
			slog.Warn("heartbeat.browser_pairings.get_browser_vm_failed",
				"host_id", host.ID,
				"machine_id", machine.ID,
				"browser_vm_id", *machine.BrowserVMID,
				"error", err)
			continue
		}
		if bvm == nil || bvm.Status != "running" || bvm.VMIP == nil || bvm.HostID == nil || *bvm.HostID != host.ID {
			continue
		}

		if err := s.agentClient.PairBrowserVM(ctx, host, bvm.ID, *machine.VMIP); err != nil {
			if agentclient.IsBrowserVMNotFound(err) {
				slog.Warn("heartbeat.browser_pairings.browser_vm_gone",
					"host_id", host.ID,
					"machine_id", machine.ID,
					"browser_vm_id", bvm.ID,
					"action", "auto_unpair")
				_ = s.store.UnpairBrowserVM(ctx, machine.ID)
				errMsg := "agent reports browser VM no longer exists on host"
				_ = s.store.UpdateBrowserVMStatus(ctx, bvm.ID, "error", &errMsg)
				_ = s.store.UnassignBrowserVMFromHost(ctx, bvm.ID)
				_ = s.store.ReleaseBrowserVMPlacement(ctx, bvm.ID)
				continue
			}
			slog.Warn("heartbeat.browser_pairings.replay_pair_failed",
				"host_id", host.ID,
				"machine_id", machine.ID,
				"browser_vm_id", bvm.ID,
				"machine_vm_ip", *machine.VMIP,
				"browser_vm_ip", *bvm.VMIP,
				"error", err)
			continue
		}

		cdpTarget := fmt.Sprintf("%s:9222", *bvm.VMIP)
		if err := s.agentClient.SetCDPTarget(ctx, host, machine.ID, cdpTarget); err != nil {
			slog.Warn("heartbeat.browser_pairings.replay_cdp_target_failed",
				"host_id", host.ID,
				"machine_id", machine.ID,
				"browser_vm_id", bvm.ID,
				"target", cdpTarget,
				"error", err)
			continue
		}

		slog.Info("heartbeat.browser_pairings.replayed",
			"host_id", host.ID,
			"machine_id", machine.ID,
			"browser_vm_id", bvm.ID,
			"machine_vm_ip", *machine.VMIP,
			"target", cdpTarget)
	}
}

func (s *Server) ServiceTokenMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfServiceTokenID == "" {
			writeError(w, http.StatusForbidden, "service token not configured")
			return
		}

		clientID := r.Header.Get("CF-Access-Client-Id")
		clientSecret := r.Header.Get("CF-Access-Client-Secret")

		idMatch := subtle.ConstantTimeCompare([]byte(clientID), []byte(s.cfServiceTokenID)) == 1
		secMatch := subtle.ConstantTimeCompare([]byte(clientSecret), []byte(s.cfServiceTokenSec)) == 1

		if !idMatch || !secMatch {
			writeError(w, http.StatusForbidden, "invalid service token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleInternalResolve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountSlug string `json:"account_slug"`
		MachineSlug string `json:"machine_slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.AccountSlug == "" || req.MachineSlug == "" {
		writeError(w, http.StatusBadRequest, "account_slug and machine_slug are required")
		return
	}

	route, err := s.store.ResolveRoute(r.Context(), req.AccountSlug, req.MachineSlug)
	if err != nil {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}

	// Self-heal KV cache via RouteService (best-effort, already returning DB result)
	if s.routing != nil {
		if route.HostHostname != "" {
			_ = s.routing.SyncRouteToKV(r.Context(), routing.SyncKVRequest{
				AccountSlug:  req.AccountSlug,
				MachineSlug:  req.MachineSlug,
				MachineID:    route.MachineID,
				HostHostname: route.HostHostname,
				ProxyToken:   route.ProxyToken,
			})
		}
		_ = s.routing.SyncAccountToKV(r.Context(), req.AccountSlug, route.AccountID, route.UserIDs)
	}

	writeJSON(w, http.StatusOK, route)
}

// authenticateAgent validates the agent bearer token. It checks per-host token first
// (if hostID is provided), then falls back to fleet-wide token.
func (s *Server) authenticateAgent(ctx context.Context, r *http.Request, hostID int) (bool, error) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return false, fmt.Errorf("missing bearer token")
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	// Check per-host token first
	if hostID != 0 {
		host, err := s.store.GetHost(ctx, hostID)
		if err == nil && host.AgentToken != nil && *host.AgentToken != "" {
			if subtle.ConstantTimeCompare([]byte(token), []byte(*host.AgentToken)) == 1 {
				return true, nil
			}
			return false, fmt.Errorf("invalid agent token for host")
		}
	}

	// Fall back to fleet-wide token
	if s.agentToken == "" {
		return false, fmt.Errorf("agent token not configured")
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.agentToken)) != 1 {
		return false, fmt.Errorf("invalid agent token")
	}
	return true, nil
}

// handleAgentHeartbeat receives periodic heartbeats from host agents.
// Auth: Bearer token must match FC_AGENT_TOKEN or per-host agent_token.
func (s *Server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HostID               json.Number     `json:"host_id"`
		ExternalIP           string          `json:"external_ip"`
		AgentVersion         string          `json:"agent_version"`
		VMCount              int             `json:"vm_count"`
		RootfsVersion        string          `json:"rootfs_version"`
		BrowserRootfsVersion string          `json:"browser_rootfs_version"`
		AgentEndpoint        string          `json:"agent_endpoint,omitempty"`
		OpenclawVersion      string          `json:"openclaw_version,omitempty"`
		Capabilities         json.RawMessage `json:"capabilities,omitempty"`
		VMVersions           map[string]struct {
			Rootfs   string `json:"rootfs,omitempty"`
			OpenClaw string `json:"openclaw,omitempty"`
		} `json:"vm_versions,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	hostID, err := strconv.Atoi(strings.Trim(req.HostID.String(), `"`))
	if err != nil || hostID == 0 || req.ExternalIP == "" {
		writeError(w, http.StatusBadRequest, "host_id and external_ip are required")
		return
	}

	ctx := r.Context()

	// Authenticate: check per-host token first, then fall back to fleet-wide token
	if ok, authErr := s.authenticateAgent(ctx, r, hostID); !ok {
		if authErr != nil && authErr.Error() == "missing bearer token" {
			writeError(w, http.StatusUnauthorized, authErr.Error())
		} else {
			writeError(w, http.StatusForbidden, authErr.Error())
		}
		return
	}

	ipChanged, err := s.store.UpdateHostHeartbeat(ctx, hostID, req.ExternalIP, req.AgentVersion, req.RootfsVersion, req.BrowserRootfsVersion, req.OpenclawVersion, req.Capabilities)
	if err != nil {
		slog.Error("heartbeat.update_failed", "host_id", hostID, "error", err)
		writeError(w, http.StatusInternalServerError, "heartbeat update failed")
		return
	}

	// Persist artifact state for observability.
	if req.RootfsVersion != "" {
		_ = s.store.UpsertHostArtifactState(ctx, &store.HostArtifactState{
			HostID:        hostID,
			Kind:          "rootfs",
			StagedVersion: strPtr(req.RootfsVersion),
		})
	}
	if req.OpenclawVersion != "" {
		_ = s.store.UpsertHostArtifactState(ctx, &store.HostArtifactState{
			HostID:        hostID,
			Kind:          "openclaw",
			StagedVersion: strPtr(req.OpenclawVersion),
		})
	}
	if updater, ok := s.store.(machineActualVersionUpdater); ok {
		for machineID, versions := range req.VMVersions {
			machineID = strings.TrimSpace(machineID)
			if machineID == "" {
				continue
			}
			if err := updater.UpdateMachineActualVersions(ctx, hostID, machineID, strings.TrimSpace(versions.Rootfs), strings.TrimSpace(versions.OpenClaw)); err != nil {
				slog.Warn("heartbeat.machine_versions_update_failed", "host_id", hostID, "machine_id", machineID, "error", err)
			}
		}
	}

	// Update agent_endpoint if reported
	if req.AgentEndpoint != "" {
		if err := s.store.UpdateHostAgentEndpoint(ctx, hostID, req.AgentEndpoint, "public_http"); err != nil {
			slog.Warn("heartbeat.update_endpoint_failed", "host_id", hostID, "error", err)
		}
	}

	if ipChanged {
		slog.Warn("heartbeat.ip_changed", "host_id", hostID, "new_ip", req.ExternalIP)
		s.activity.Log(ctx, events.LogParams{
			Category:  "host",
			Action:    "host.ip_changed",
			Status:    "success",
			ActorType: "agent",
			ActorID:   &hostID,
			HostID:    &hostID,
			Summary:   fmt.Sprintf("Host IP changed to %s", req.ExternalIP),
			Detail:    map[string]any{"new_ip": req.ExternalIP},
		})
	}

	// Auto-recover hosts when heartbeat resumes (unreachable from stale heartbeat,
	// draining from shutdown-notify during restart/redeploy, or updating from an
	// agent-only update / drain-and-update workflow)
	host, err := s.store.GetHost(ctx, hostID)
	if err == nil && (host.Status == "unreachable" || host.Status == "draining" || host.Status == "updating") {
		prevStatus := host.Status
		if err := s.store.UpdateHostStatus(ctx, hostID, "ready"); err != nil {
			slog.Error("heartbeat.recovery.failed", "host_id", hostID, "error", err)
		} else {
			slog.Info("host.reconciler.recovered", "host_id", hostID, "previous_status", prevStatus)
			s.activity.Log(ctx, events.LogParams{
				Category:  "host",
				Action:    "host.recovered",
				Status:    "success",
				ActorType: "system",
				HostID:    &hostID,
				Summary:   "Host recovered after being unreachable",
				Detail:    map[string]any{"previous_status": prevStatus},
			})
		}

		// Drain-and-update uses maintenance mode and pending_restarts. Agent-only
		// updates leave both empty, so this becomes a no-op beyond the status flip.
		if prevStatus == "updating" {
			if err := s.store.SetHostMaintenanceMode(ctx, hostID, false); err != nil {
				slog.Error("heartbeat.maintenance_mode_clear_failed", "host_id", hostID, "error", err)
			}
			var pendingIDs []string
			if host.StatusMessage != nil {
				var payload struct {
					PendingRestarts []string `json:"pending_restarts"`
				}
				if err := json.Unmarshal([]byte(*host.StatusMessage), &payload); err == nil {
					pendingIDs = payload.PendingRestarts
				}
			}
			s.restartMachinesAfterUpdate(hostID, pendingIDs)
		}
	}

	// Auto-promote provisioning hosts on first heartbeat
	if err == nil && host.Status == "provisioning" {
		if err := s.store.UpdateHostStatus(ctx, hostID, "ready"); err != nil {
			slog.Error("heartbeat.promotion.failed", "host_id", hostID, "error", err)
		} else {
			slog.Info("host.enrolled.promoted", "host_id", hostID)
			s.activity.Log(ctx, events.LogParams{
				Category:  "host",
				Action:    "host.enrolled",
				Status:    "success",
				ActorType: "system",
				HostID:    &hostID,
				Summary:   "New host enrolled via first heartbeat",
			})
		}
	}

	if err == nil && host != nil && req.VMCount > 0 {
		s.reconcileHostBrowserPairings(ctx, host)
	}

	slog.Info("heartbeat.received", "host_id", hostID, "ip", req.ExternalIP, "version", req.AgentVersion, "vm_count", req.VMCount, "ip_changed", ipChanged)

	resp := map[string]interface{}{
		"status":     "ok",
		"ip_changed": ipChanged,
	}
	// Signal that backups are enabled so agents can lazily initialize GCS client.
	// We do NOT send the master key — per-machine encryption keys are sent
	// in individual backup/restore RPC payloads.
	if s.backupMasterKey != "" {
		resp["backup_enabled"] = true
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleAgentShutdownNotify receives shutdown notifications from host agents.
// Auth: Bearer token checked via per-host token first, then fleet-wide token.
func (s *Server) handleAgentShutdownNotify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HostID string `json:"host_id"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	hostID, err := strconv.Atoi(req.HostID)
	if err != nil || hostID == 0 {
		writeError(w, http.StatusBadRequest, "host_id is required")
		return
	}

	ctx := r.Context()

	// Authenticate: check per-host token first, then fall back to fleet-wide token
	if ok, authErr := s.authenticateAgent(ctx, r, hostID); !ok {
		if authErr != nil && authErr.Error() == "missing bearer token" {
			writeError(w, http.StatusUnauthorized, authErr.Error())
		} else {
			writeError(w, http.StatusForbidden, authErr.Error())
		}
		return
	}

	// Mark host as draining
	if err := s.store.UpdateHostStatus(ctx, hostID, "draining"); err != nil {
		slog.Error("shutdown_notify.update_status_failed", "host_id", hostID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update host status")
		return
	}

	// Record the shutdown event
	s.activity.Log(ctx, events.LogParams{
		Category:  "agent",
		Action:    "agent.shutdown",
		Status:    "success",
		ActorType: "agent",
		ActorID:   &hostID,
		HostID:    &hostID,
		Summary:   fmt.Sprintf("Agent shutting down: %s", req.Reason),
		Detail:    map[string]any{"reason": req.Reason},
	})

	slog.Info("shutdown_notify.received", "host_id", hostID, "reason", req.Reason)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
