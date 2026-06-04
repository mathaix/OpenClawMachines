package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// ReconcilerStore is the subset of store interfaces the reconciler needs.
type ReconcilerStore interface {
	store.HostRepo
	store.MachineRepo
	store.OperationRepo
	ReleasePlacement(ctx context.Context, machineID string) error
	FindStuckMachines(ctx context.Context, stuckThreshold time.Duration) ([]store.Machine, error)
}

// InstanceChecker checks whether a cloud instance exists.
type InstanceChecker interface {
	InstanceExists(ctx context.Context, project, zone, vmName string) (bool, error)
}

// MachineCleanup handles route/tunnel cleanup for machines on dead hosts.
type MachineCleanup interface {
	CleanupMachineRouteAndTunnel(ctx context.Context, machineID string) error
}

// HostReconciler periodically checks for stale/unreachable hosts and reconciles them.
type HostReconciler struct {
	store              ReconcilerStore
	instanceCheck      InstanceChecker
	heartbeatOnlyCheck InstanceChecker
	machineCleanup     MachineCleanup
	activity           *events.Activity
	project            string
	staleThreshold     time.Duration
}

// New creates a new HostReconciler.
func New(
	s ReconcilerStore,
	ic InstanceChecker,
	mc MachineCleanup,
	project string,
	staleThreshold time.Duration,
) *HostReconciler {
	return &HostReconciler{
		store:              s,
		instanceCheck:      ic,
		heartbeatOnlyCheck: &HeartbeatOnlyChecker{},
		machineCleanup:     mc,
		project:            project,
		staleThreshold:     staleThreshold,
	}
}

// SetActivity configures the activity logger for emitting machine lifecycle events.
func (r *HostReconciler) SetActivity(a *events.Activity) {
	r.activity = a
}

// Start runs the reconciler loop at the given interval until the context is cancelled.
func (r *HostReconciler) Start(ctx context.Context, interval time.Duration) {
	slog.Info("host.reconciler.started", "interval", interval, "stale_threshold", r.staleThreshold)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("host.reconciler.stopped")
			return
		case <-ticker.C:
			r.reconcileOnce(ctx)
		}
	}
}

// reconcileOnce runs a single reconciliation pass.
func (r *HostReconciler) reconcileOnce(ctx context.Context) {
	// Step 0a: Expire stale machine operations that have been in_progress too long.
	// Migrations can legitimately span backup + restore + restart cycles, so the
	// expiry window must exceed the end-to-end migration timeout.
	if expired, err := r.store.ExpireStaleOperations(ctx, 30*time.Minute); err != nil {
		slog.Error("host.reconciler.expire_operations.failed", "error", err)
	} else if expired > 0 {
		slog.Warn("host.reconciler.expired_stale_operations", "count", expired)
	}

	// Step 0b: Auto-reset machines stuck in transitional states (provisioning/stopping)
	// for >5 min. These are typically caused by interrupted workflows (e.g., Cloud Run
	// instance replacement during migration).
	r.reconcileStuckMachines(ctx)

	// Step 1: Find stale hosts (ready but heartbeat too old) → mark unreachable.
	// Skip hosts that are in maintenance/updating to avoid false unreachable during agent updates.
	staleHosts, err := r.store.ListStaleHosts(ctx, r.staleThreshold)
	if err != nil {
		slog.Error("host.reconciler.list_stale.failed", "error", err)
	} else {
		for _, h := range staleHosts {
			if h.MaintenanceMode {
				slog.Info("host.reconciler.maintenance_skip", "host_id", h.ID, "status", h.Status, "last_heartbeat", h.LastHeartbeat)
				continue
			}
			slog.Warn("host.reconciler.stale_detected", "host_id", h.ID, "vm_name", h.VMName, "last_heartbeat", h.LastHeartbeat)
			if err := r.store.UpdateHostStatus(ctx, h.ID, "unreachable"); err != nil {
				slog.Error("host.reconciler.mark_unreachable.failed", "host_id", h.ID, "error", err)
				continue
			}
			hostID := h.ID
			r.activity.Log(ctx, events.LogParams{
				Category:  "host",
				Action:    "host.unreachable",
				Status:    "failure",
				ActorType: "system",
				HostID:    &hostID,
				Summary:   "Host marked unreachable due to stale heartbeat",
				Detail:    map[string]any{"reason": "stale_heartbeat"},
			})
		}
	}

	// Step 2: Find unreachable hosts → check GCP instance existence
	unreachableHosts, err := r.store.ListUnreachableHosts(ctx)
	if err != nil {
		slog.Error("host.reconciler.list_unreachable.failed", "error", err)
		return
	}

	for _, h := range unreachableHosts {
		// Select instance checker based on provider
		checker := r.instanceCheck
		if h.Provider != "" && h.Provider != "gcp" {
			// Non-GCP hosts: use heartbeat-only checker (always returns exists=true).
			// For registered hosts, we rely on extended staleness (10 min) — once
			// unreachable, the host stays unreachable until heartbeat resumes or
			// admin intervention. The HeartbeatOnlyChecker prevents premature
			// termination since we have no provider API to verify instance state.
			checker = r.heartbeatOnlyCheck
		}

		exists, err := checker.InstanceExists(ctx, r.project, h.Zone, h.VMName)
		if err != nil {
			slog.Warn("host.reconciler.instance_check.failed", "host_id", h.ID, "vm_name", h.VMName, "error", err)
			// Stay unreachable — will retry next tick. Don't move to error on transient failures.
			continue
		}

		if exists {
			// Instance still running — may recover, keep as unreachable
			slog.Info("host.reconciler.instance_exists", "host_id", h.ID, "vm_name", h.VMName)
			continue
		}

		// Instance gone (404) → mark terminated and clean up machines
		slog.Warn("host.reconciler.instance_gone", "host_id", h.ID, "vm_name", h.VMName)
		if err := r.store.UpdateHostStatus(ctx, h.ID, "terminated"); err != nil {
			slog.Error("host.reconciler.mark_terminated.failed", "host_id", h.ID, "error", err)
			continue
		}
		terminatedHostID := h.ID
		r.activity.Log(ctx, events.LogParams{
			Category:  "host",
			Action:    "host.terminated",
			Status:    "failure",
			ActorType: "system",
			HostID:    &terminatedHostID,
			Summary:   "Host terminated: GCE instance no longer exists",
			Detail:    map[string]any{"reason": "instance_not_found"},
		})

		r.cleanupMachinesOnHost(ctx, h)
	}
}

// cleanupMachinesOnHost marks active machines on a terminated host as error and cleans up routes/tunnels.
func (r *HostReconciler) cleanupMachinesOnHost(ctx context.Context, h store.Host) {
	// Mark running/provisioning/starting machines as error
	affectedIDs, err := r.store.MarkMachinesOnHostError(ctx, h.ID, "host terminated unexpectedly")
	if err != nil {
		slog.Error("host.reconciler.mark_machines_error.failed", "host_id", h.ID, "error", err)
	} else if len(affectedIDs) > 0 {
		slog.Warn("host.reconciler.machines_marked_error", "host_id", h.ID, "machine_ids", affectedIDs)
		hostID := h.ID
		errMsg := "host terminated unexpectedly"
		for _, machineID := range affectedIDs {
			mid := machineID
			r.activity.Log(ctx, events.LogParams{
				Category:  "machine",
				Action:    "machine.error",
				Status:    "failure",
				ActorType: "system",
				MachineID: &mid,
				HostID:    &hostID,
				Summary:   fmt.Sprintf("Machine '%s' entered error state: host terminated", mid),
				ErrorMsg:  &errMsg,
			})
		}
	}

	// Release placements and clean up routes/tunnels for affected machines
	for _, machineID := range affectedIDs {
		if err := r.store.ReleasePlacement(ctx, machineID); err != nil {
			slog.Error("host.reconciler.release_placement.failed", "machine_id", machineID, "error", err)
		}
		if r.machineCleanup != nil {
			if err := r.machineCleanup.CleanupMachineRouteAndTunnel(ctx, machineID); err != nil {
				slog.Error("host.reconciler.machine_cleanup.failed", "machine_id", machineID, "error", err)
			}
		}
	}

	// Update stopped machines on this host with a status message about data loss
	machines, err := r.store.ListMachinesByHost(ctx, h.ID)
	if err != nil {
		slog.Error("host.reconciler.list_machines.failed", "host_id", h.ID, "error", err)
		return
	}
	for _, m := range machines {
		if m.Status == "stopped" {
			msg := "data volume lost (host terminated)"
			if err := r.store.UpdateMachineStatus(ctx, m.ID, "stopped", &msg); err != nil {
				slog.Error("host.reconciler.update_stopped_machine.failed", "machine_id", m.ID, "error", err)
			}
		}
	}
}

const stuckMachineThreshold = 5 * time.Minute

// reconcileStuckMachines finds machines stuck in transitional states and resets them.
func (r *HostReconciler) reconcileStuckMachines(ctx context.Context) {
	stuck, err := r.store.FindStuckMachines(ctx, stuckMachineThreshold)
	if err != nil {
		slog.Error("reconciler.stuck_machines.query_failed", "error", err)
		return
	}

	for _, m := range stuck {
		slog.Warn("reconciler.stuck_machine.detected",
			"machine_id", m.ID,
			"status", m.Status,
			"host_id", m.HostID,
		)

		// Expire any stale operation
		if op, err := r.store.GetActiveMachineOperation(ctx, m.ID); err == nil && op != nil {
			msg := "auto-expired by reconciler (machine stuck in " + m.Status + ")"
			_ = r.store.CompleteMachineOperation(ctx, op.ID, "failed", &msg)
		}

		// Release placement if assigned
		if m.HostID != nil {
			if err := r.store.ReleasePlacement(ctx, m.ID); err != nil {
				slog.Error("reconciler.stuck_machine.release_failed", "machine_id", m.ID, "error", err)
			}
		}

		// Clean up routes/tunnels
		if r.machineCleanup != nil {
			if err := r.machineCleanup.CleanupMachineRouteAndTunnel(ctx, m.ID); err != nil {
				slog.Warn("reconciler.stuck_machine.cleanup_failed", "machine_id", m.ID, "error", err)
			}
		}

		// Reset to error with a descriptive message
		msg := "auto-reset by reconciler: stuck in " + m.Status + " for >" + stuckMachineThreshold.String()
		if err := r.store.UpdateMachineStatus(ctx, m.ID, "error", &msg); err != nil {
			slog.Error("reconciler.stuck_machine.reset_failed", "machine_id", m.ID, "error", err)
		} else {
			slog.Info("reconciler.stuck_machine.reset", "machine_id", m.ID, "previous_status", m.Status)
			mid := m.ID
			r.activity.Log(ctx, events.LogParams{
				Category:  "machine",
				Action:    "machine.stall_detected",
				Status:    "failure",
				ActorType: "system",
				MachineID: &mid,
				HostID:    m.HostID,
				Summary:   fmt.Sprintf("Machine '%s' stalled in '%s', reset by reconciler", m.Name, m.Status),
				ErrorMsg:  &msg,
			})
		}
	}
}
