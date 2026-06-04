package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

func (s *Server) handleProvisionHost(w http.ResponseWriter, r *http.Request) {
	if s.provisioner == nil {
		writeError(w, http.StatusServiceUnavailable, "provisioner not configured")
		return
	}

	var req struct {
		MachineType string `json:"machine_type"`
		Zone        string `json:"zone"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	// Machine type presets (must support KVM / nested virtualization).
	// vCPU capacity = physical CPU threads * oversubscription ratio.
	// The ratio is read from VCPU_OVERSUBSCRIPTION_RATIO (default 2, max 3).
	type preset struct {
		physicalCPUs int
		memoryMB     int
	}
	validTypes := map[string]preset{
		"n2-standard-2": {2, 8192},
		"n2-standard-4": {4, 16384},
		"n2-standard-8": {8, 32768},
		"c2-standard-4": {4, 16384},
	}

	if req.MachineType == "" {
		req.MachineType = "n2-standard-2"
	}
	p, ok := validTypes[req.MachineType]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid machine_type: must be one of n2-standard-2, n2-standard-4, n2-standard-8, c2-standard-4")
		return
	}

	machineType := req.MachineType
	physicalCPUs := p.physicalCPUs
	vcpus := physicalCPUs * s.vcpuOversubRatio
	memoryMB := p.memoryMB

	go func() {
		ctx := context.Background()
		host, err := s.provisioner.ProvisionHost(ctx, machineType, physicalCPUs, vcpus, memoryMB, req.Zone)
		if err != nil {
			slog.Error("host.provision.failed", "error", err)
			return
		}
		slog.Info("host.provision.completed", "host_id", host.ID, "host_name", host.VMName)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "provisioning",
		"message": "Host provisioning started in background",
	})
}

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.store.ListHosts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, hosts)
}

func (s *Server) handleDestroyHost(w http.ResponseWriter, r *http.Request) {
	if s.provisioner == nil {
		writeError(w, http.StatusServiceUnavailable, "provisioner not configured")
		return
	}

	hostID, err := parseIntParam(r, "hostId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}

	go func() {
		ctx := context.Background()

		// Clean up all machines assigned to this host before destroying it.
		machines, err := s.store.ListMachinesByHost(ctx, hostID)
		if err != nil {
			slog.Error("host.destroy.machines.list.failed", "host_id", hostID, "error", err)
		}
		for _, m := range machines {
			if err := s.machines.CleanupMachineRouteAndTunnel(ctx, m.ID); err != nil {
				slog.Error("host.destroy.kv.route.delete.failed", "machine_id", m.ID, "host_id", hostID, "error", err)
			}
			if err := s.store.DeleteMachine(ctx, m.ID); err != nil {
				slog.Error("host.destroy.machine.delete.failed", "machine_id", m.ID, "host_id", hostID, "error", err)
			} else {
				slog.Info("host.destroy.machine.cleaned.up", "machine_id", m.ID, "host_id", hostID)
			}
		}

		if err := s.provisioner.DestroyHost(ctx, hostID); err != nil {
			slog.Error("host.destroy.failed", "host_id", hostID, "error", err)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "destroying",
		"message": "Host destruction started in background",
	})
}

func (s *Server) handleListHostMachines(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseIntParam(r, "hostId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}

	machines, err := s.store.ListMachinesByHost(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return machine info with metadata
	type hostMachine struct {
		ID              string     `json:"id"`
		Name            string     `json:"name"`
		Slug            string     `json:"slug"`
		Status          string     `json:"status"`
		VMIP            *string    `json:"vm_ip,omitempty"`
		DataVolumeGB    int        `json:"data_volume_gb"`
		RootfsSnapshot  *string    `json:"rootfs_snapshot,omitempty"`
		OpenclawVersion *string    `json:"openclaw_version,omitempty"`
		CreatedAt       time.Time  `json:"created_at"`
		StartedAt       *time.Time `json:"started_at,omitempty"`
	}
	result := make([]hostMachine, len(machines))
	for i, m := range machines {
		result[i] = hostMachine{
			ID:              m.ID,
			Name:            m.Name,
			Slug:            m.Slug,
			Status:          m.Status,
			VMIP:            m.VMIP,
			DataVolumeGB:    m.DataVolumeGB,
			RootfsSnapshot:  m.RootfsSnapshot,
			OpenclawVersion: m.OpenclawVersion,
			CreatedAt:       m.CreatedAt,
			StartedAt:       m.StartedAt,
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleHostVMStats(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseIntParam(r, "hostId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}

	host, err := s.store.GetHost(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}

	stats, err := s.agentClient.ListVMs(r.Context(), host)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to reach agent: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleRefreshRootfs(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseIntParam(r, "hostId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}

	host, err := s.store.GetHost(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}

	ctx := r.Context()
	slog.Info("admin.rootfs.starting", "host_id", hostID, "host_name", host.VMName)

	// Enable maintenance mode so reconciler leaves the host alone
	if err := s.store.SetHostMaintenanceMode(ctx, hostID, true); err != nil {
		slog.Error("admin.rootfs.maintenance_mode_failed", "host_id", hostID, "error", err)
	}

	// Stop all running machines on this host for a clean shutdown
	machineList, err := s.store.ListMachinesByHost(ctx, hostID)
	if err != nil {
		_ = s.store.SetHostMaintenanceMode(ctx, hostID, false)
		writeError(w, http.StatusInternalServerError, "failed to list machines: "+err.Error())
		return
	}

	var running []store.Machine
	for _, m := range machineList {
		if m.Status == "running" {
			running = append(running, m)
		}
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
				slog.Error("admin.rootfs.stop_failed",
					"machine_id", machine.ID, "host_id", hostID, "error", err)
				mu.Lock()
				stopErrors = append(stopErrors, machine.ID+": "+err.Error())
				mu.Unlock()
				return
			}
			slog.Info("admin.rootfs.stopped",
				"machine_id", machine.ID, "machine_slug", machine.Slug, "host_id", hostID)
			mu.Lock()
			stoppedIDs = append(stoppedIDs, machine.ID)
			mu.Unlock()
		}(m)
	}
	wg.Wait()

	if len(stopErrors) > 0 {
		slog.Error("admin.rootfs.partial_stop_failure",
			"host_id", hostID, "errors", strings.Join(stopErrors, "; "))
	}
	slog.Info("admin.rootfs.machines_stopped",
		"host_id", hostID, "stopped", len(stoppedIDs), "failed", len(stopErrors))

	// Refresh rootfs on the agent
	if err := s.agentClient.RefreshRootfs(ctx, host); err != nil {
		slog.Error("admin.rootfs.refresh_failed", "host_id", hostID, "error", err)
		// Restart VMs that were stopped — refresh failed, nothing changed
		s.restartMachinesAfterUpdate(hostID, stoppedIDs)
		_ = s.store.SetHostMaintenanceMode(ctx, hostID, false)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Update source_image in DB if provided
	sourceImage := r.URL.Query().Get("source_image")
	if sourceImage != "" {
		if err := s.store.UpdateHostSourceImage(ctx, hostID, sourceImage); err != nil {
			slog.Error("admin.rootfs.update_source_image_failed", "host_id", hostID, "error", err)
		}
	}
	slog.Info("admin.rootfs.refreshed", "host_id", hostID, "host_name", host.VMName, "source_image", sourceImage)

	// Clear maintenance mode and restart stopped machines
	if err := s.store.SetHostMaintenanceMode(ctx, hostID, false); err != nil {
		slog.Error("admin.rootfs.maintenance_mode_clear_failed", "host_id", hostID, "error", err)
	}
	s.restartMachinesAfterUpdate(hostID, stoppedIDs)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":            "refreshed",
		"machines_stopped":  len(stoppedIDs),
		"machines_failed":   len(stopErrors),
		"machines_starting": len(stoppedIDs),
	})
}

func (s *Server) handleTriggerHostUpdate(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseIntParam(r, "hostId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}

	host, err := s.store.GetHost(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}

	if host.Status != "ready" {
		writeError(w, http.StatusConflict, "host is not ready (status: "+host.Status+")")
		return
	}

	op, err := s.createHostUpdateOperation(host, hostUpdateKindTrigger)
	if err != nil {
		if err == errHostUpdateOperationActive {
			writeError(w, http.StatusConflict, "host update already in progress")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	go s.runTriggerHostUpdateOperation(context.Background(), op.ID, hostID)

	writeHostUpdateOperationAccepted(w, op)
}

func (s *Server) handleDrainHostUpdate(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseIntParam(r, "hostId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}

	host, err := s.store.GetHost(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}

	if host.Status != "ready" {
		writeError(w, http.StatusConflict, "host is not ready (status: "+host.Status+")")
		return
	}

	op, err := s.createHostUpdateOperation(host, hostUpdateKindDrain)
	if err != nil {
		if err == errHostUpdateOperationActive {
			writeError(w, http.StatusConflict, "host update already in progress")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	go s.runDrainHostUpdateOperation(context.Background(), op.ID, hostID)

	writeHostUpdateOperationAccepted(w, op)
}

func (s *Server) handleConfigureHostBrowserStorage(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseIntParam(r, "hostId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}

	var req struct {
		Device     string `json:"device"`
		MountPoint string `json:"mount_point"`
		Format     bool   `json:"format"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Device = strings.TrimSpace(req.Device)
	req.MountPoint = strings.TrimSpace(req.MountPoint)
	if req.Device == "" {
		writeError(w, http.StatusBadRequest, "device is required")
		return
	}

	host, err := s.store.GetHost(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}

	if host.Status != "ready" {
		writeError(w, http.StatusConflict, "host is not ready (status: "+host.Status+")")
		return
	}

	op, err := s.createHostUpdateOperation(host, hostUpdateKindStorage)
	if err != nil {
		if err == errHostUpdateOperationActive {
			writeError(w, http.StatusConflict, "host update already in progress")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	go s.runConfigureHostBrowserStorageOperation(context.Background(), op.ID, hostID, req.Device, req.MountPoint, req.Format)

	writeHostUpdateOperationAccepted(w, op)
}

// restartMachinesAfterUpdate starts machines that were stopped for an update.
// Called from the heartbeat handler when a host transitions from "updating" to "ready".
func (s *Server) restartMachinesAfterUpdate(hostID int, pendingIDs []string) {
	if len(pendingIDs) == 0 {
		slog.Info("heartbeat.restart_machines.none_pending", "host_id", hostID)
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		slog.Info("heartbeat.restart_machines.starting", "host_id", hostID, "count", len(pendingIDs))

		for _, machineID := range pendingIDs {
			machine, err := s.store.GetMachine(ctx, machineID)
			if err != nil {
				slog.Error("heartbeat.restart_machines.get_failed",
					"machine_id", machineID, "host_id", hostID, "error", err)
				continue
			}

			if machine.Status != "stopped" {
				slog.Warn("heartbeat.restart_machines.skip_not_stopped",
					"machine_id", machineID, "status", machine.Status, "host_id", hostID)
				continue
			}

			slog.Info("heartbeat.restart_machines.starting_machine",
				"machine_id", machine.ID, "machine_slug", machine.Slug, "host_id", hostID)

			_, _, err = s.machines.Start(ctx, machine.AccountID, machine)
			if err != nil {
				slog.Error("heartbeat.restart_machines.start_failed",
					"machine_id", machine.ID, "host_id", hostID, "error", err)
				msg := "failed to restart after update: " + err.Error()
				_ = s.store.UpdateMachineStatus(ctx, machine.ID, "stopped", &msg)
			} else {
				slog.Info("heartbeat.restart_machines.started",
					"machine_id", machine.ID, "machine_slug", machine.Slug, "host_id", hostID)
			}
		}

		slog.Info("heartbeat.restart_machines.completed", "host_id", hostID, "count", len(pendingIDs))
	}()
}

func (s *Server) handleHostLogs(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseIntParam(r, "hostId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}

	host, err := s.store.GetHost(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}

	lines := 100
	if l := r.URL.Query().Get("lines"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 500 {
			lines = parsed
		}
	}

	logs, err := s.agentClient.GetLogs(r.Context(), host, lines)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"logs": logs})
}

func (s *Server) handleSetHostMaintenance(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseIntParam(r, "hostId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}

	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if _, err := s.store.GetHost(r.Context(), hostID); err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}

	if err := s.store.SetHostMaintenanceMode(r.Context(), hostID, body.Enabled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	action := "enabled"
	if !body.Enabled {
		action = "disabled"
	}
	slog.Info("admin.host.maintenance", "host_id", hostID, "enabled", body.Enabled)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "maintenance_mode": action})
}
