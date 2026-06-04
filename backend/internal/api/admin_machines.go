package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/fleet"
)

// handleAdminResetMachine resets a machine from error status to stopped.
func (s *Server) handleAdminResetMachine(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineId")
	if machineID == "" {
		writeError(w, http.StatusBadRequest, "machine id required")
		return
	}

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	if machine.Status == "running" {
		writeError(w, http.StatusBadRequest, "cannot reset a running machine — stop it first")
		return
	}
	if machine.Status == "stopped" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
		return
	}

	// Expire any in-flight operations so the machine can be started again
	if op, err := s.store.GetActiveMachineOperation(r.Context(), machineID); err == nil && op != nil {
		msg := "force-reset by admin"
		_ = s.store.CompleteMachineOperation(r.Context(), op.ID, "failed", &msg)
		slog.Info("admin.reset.machine.expired_operation", "machine_id", machineID, "operation_id", op.ID, "kind", op.Kind)
	}

	// For provisioned machines, check if the host is still available.
	// If host exists, reset status but keep host linkage (data volume is there).
	// If host is gone, flag migration required.
	if machine.ProvisioningCompletedAt != nil {
		hostAvailable := false
		if machine.HostID != nil {
			if h, err := s.store.GetHost(r.Context(), *machine.HostID); err == nil && h.Status != "destroyed" {
				hostAvailable = true
			}
		}
		if !hostAvailable {
			msg := "migration required; host unavailable"
			_ = s.store.UpdateMachineStatus(r.Context(), machineID, "error", &msg)
			slog.Warn("admin.reset.migration_required", "machine_id", machineID, "host_id", machine.HostID)
			writeJSON(w, http.StatusConflict, map[string]string{
				"status":  "error",
				"message": msg,
			})
			return
		}
		// Host still exists — just reset status, keep host linkage
		if err := s.store.UpdateMachineStatus(r.Context(), machineID, "stopped", nil); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		slog.Info("admin.reset.machine.completed", "machine_id", machineID, "previous_status", machine.Status, "host_id", machine.HostID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
		return
	}

	// Unprovisioned machine — full cleanup: release host, tunnels, unassign
	if machine.HostID != nil {
		if err := s.placement.Release(r.Context(), machineID, fleet.ReleaseHard); err != nil {
			slog.Error("admin.reset.machine.release.failed", "machine_id", machineID, "error", err)
		}
	}

	// Clean up stale tunnel if present
	if s.tunnelMgr != nil && machine.TunnelID != nil {
		vmHostname := s.dataPlaneHostname("m", machine.Slug)
		sshHostname := s.dataPlaneHostname("ssh", machine.Slug)
		if err := s.tunnelMgr.DeleteTunnelAndDNS(r.Context(), *machine.TunnelID, vmHostname, sshHostname); err != nil {
			slog.Warn("admin.reset.machine.tunnel_cleanup_failed", "machine_id", machineID, "error", err)
		}
		if err := s.store.ClearMachineTunnel(r.Context(), machineID); err != nil {
			slog.Warn("admin.reset.machine.tunnel_clear_failed", "machine_id", machineID, "error", err)
		}
	}

	// Force update status to stopped
	if err := s.store.UnassignMachineFromHost(r.Context(), machineID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("admin.reset.machine.completed", "machine_id", machineID, "previous_status", machine.Status)
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// handleAdminStartMachine lets a superuser start any machine regardless of account.
func (s *Server) handleAdminStartMachine(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineId")
	if machineID == "" {
		writeError(w, http.StatusBadRequest, "machine id required")
		return
	}

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	if machine.Status == "running" {
		writeError(w, http.StatusConflict, "machine already running")
		return
	}

	host, vmIP, err := s.machines.Start(r.Context(), machine.AccountID, machine)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	status := "provisioning"
	if machine.HostID != nil {
		status = "starting"
	}
	slog.Info("admin.start.machine", "machine_id", machineID, "host_id", host.ID, "status", status)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  status,
		"host_id": host.ID,
		"vm_ip":   vmIP,
	})
}

// handleAdminStopMachine lets a superuser stop any machine regardless of account.
func (s *Server) handleAdminStopMachine(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineId")
	if machineID == "" {
		writeError(w, http.StatusBadRequest, "machine id required")
		return
	}

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	if err := s.machines.Stop(r.Context(), machine); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("admin.stop.machine", "machine_id", machineID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// handleAdminClearMigration clears a migration-required error and returns the machine to stopped.
func (s *Server) handleAdminClearMigration(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineId")
	if machineID == "" {
		writeError(w, http.StatusBadRequest, "machine id required")
		return
	}
	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.Status == "running" {
		writeError(w, http.StatusConflict, "machine is running")
		return
	}
	if err := s.store.UpdateMachineStatus(r.Context(), machineID, "stopped", nil); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("admin.clear_migration", "machine_id", machineID)
	if s.activity != nil {
		mid := machineID
		s.activity.Log(r.Context(), events.LogParams{
			Category:  "machine",
			Action:    "machine.migration_cleared",
			Status:    "success",
			ActorType: "admin",
			MachineID: &mid,
			Summary:   "Migration-required flag cleared by admin",
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// handleAdminFlagMigration marks a stopped machine as requiring migration.
func (s *Server) handleAdminFlagMigration(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineId")
	if machineID == "" {
		writeError(w, http.StatusBadRequest, "machine id required")
		return
	}
	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.Status == "running" {
		writeError(w, http.StatusConflict, "cannot flag a running machine — stop it first")
		return
	}
	msg := "migration required: flagged by admin"
	if err := s.store.UpdateMachineStatus(r.Context(), machineID, "error", &msg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("admin.flag_migration", "machine_id", machineID)
	if s.activity != nil {
		mid := machineID
		s.activity.Log(r.Context(), events.LogParams{
			Category:  "machine",
			Action:    "machine.migration_flagged",
			Status:    "success",
			ActorType: "admin",
			MachineID: &mid,
			Summary:   "Machine flagged for migration by admin",
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "error", "message": msg})
}

func (s *Server) handleAdminListMachines(w http.ResponseWriter, r *http.Request) {
	machines, err := s.store.ListAllMachines(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, machines)
}
