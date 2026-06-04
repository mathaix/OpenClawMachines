package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/backup"
	"github.com/mathaix/openclawmachines/backend/internal/machines"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type adminMigrationRequest struct {
	MachineID    string `json:"machine_id"`
	TargetHostID int    `json:"target_host_id"`
	Force        bool   `json:"force"` // If true, allow migration even without backup capability
}

// handleAdminMigrateMachine moves a machine from its current host to a target host.
// Flow: acquire lock → stop → backup → release → start on new host.
// If a backup is available, the migration restores it on the target host before
// completing. The migration operation remains active until the target machine
// reaches its final running state.
func (s *Server) handleAdminMigrateMachine(w http.ResponseWriter, r *http.Request) {
	var req adminMigrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.MachineID == "" || req.TargetHostID == 0 {
		writeError(w, http.StatusBadRequest, "machine_id and target_host_id are required")
		return
	}

	if s.workflows != nil && s.workflows.Context() != nil {
		s.enqueueAdminMigrationWorkflow(w, r, req)
		return
	}

	// Detach migration from the request lifetime so an admin page refresh or proxy
	// timeout cannot cancel a half-finished move after the source host is released.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 20*time.Minute)
	defer cancel()

	machine, err := s.store.GetMachine(ctx, req.MachineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	targetHost, err := s.store.GetHost(ctx, req.TargetHostID)
	if err != nil {
		writeError(w, http.StatusNotFound, "target host not found")
		return
	}
	if targetHost.Status != "ready" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("target host is %s, must be ready", targetHost.Status))
		return
	}

	if machine.HostID != nil && *machine.HostID == req.TargetHostID {
		writeError(w, http.StatusBadRequest, "machine is already on the target host")
		return
	}

	sourceHostID := machine.HostID
	canBackup := sourceHostID != nil && machine.BackupsEnabled && s.backupMasterKey != ""

	// Reject migration if data would be lost unless force=true
	if sourceHostID != nil && !canBackup && !req.Force {
		writeError(w, http.StatusBadRequest,
			"machine has a source host but backups are not enabled or configured — "+
				"data volume will be lost. Pass force=true to proceed anyway.")
		return
	}

	// Acquire a migration operation to prevent concurrent lifecycle actions.
	op := &store.MachineOperation{MachineID: machine.ID, Kind: "migrate"}
	if err := s.store.CreateMachineOperation(ctx, op); err != nil {
		writeError(w, http.StatusConflict, "machine has an in-flight operation: "+err.Error())
		return
	}
	opState := "completed"
	var opErrMsg *string
	defer func() {
		_ = s.store.CompleteMachineOperation(context.Background(), op.ID, opState, opErrMsg)
	}()
	failMigration := func(msg string) {
		opState = "failed"
		opErrMsg = &msg
	}

	slog.Info("admin.migrate.start", "machine_id", req.MachineID, "source_host_id", sourceHostID, "target_host_id", req.TargetHostID, "can_backup", canBackup, "force", req.Force)

	// Step 1: Stop the machine if running.
	// For migration, we use the agent's StopVM directly instead of RuntimeService.Stop()
	// because Stop() does ReleaseSoft (decrements capacity counters) and we need to
	// do a full ReleaseHard later. By calling StopVM directly, we keep the capacity
	// counters intact and handle the full release ourselves in step 4.
	sourceStoppedDirectly := false
	if machine.Status == "running" {
		if sourceHostID == nil {
			msg := "machine is running but has no host"
			failMigration(msg)
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		sourceHost, err := s.store.GetHost(ctx, *sourceHostID)
		if err != nil {
			msg := "failed to get source host"
			failMigration(msg)
			writeError(w, http.StatusInternalServerError, msg)
			return
		}
		if err := s.agentClient.StopVM(ctx, sourceHost, machine.ID); err != nil {
			msg := "failed to stop machine: " + err.Error()
			failMigration(msg)
			writeError(w, http.StatusInternalServerError, msg)
			return
		}
		sourceStoppedDirectly = true
		slog.Info("admin.migrate.stopped", "machine_id", req.MachineID)
	}

	// Step 2: Create backup on source host (if backups are available).
	// The backup handler reads the data volume file directly — works on stopped VMs.
	// With force=true, skip backup if it fails (source host may be crashed).
	var backupRecord *store.BackupRecord
	if canBackup {
		br, err := s.createMigrationBackup(ctx, machine, *sourceHostID)
		if err != nil {
			if req.Force {
				slog.Warn("admin.migrate.backup_skipped_force", "machine_id", req.MachineID, "error", err)
			} else {
				msg := "backup failed: " + err.Error()
				if sourceStoppedDirectly && sourceHostID != nil {
					if cleanupErr := s.finalizeStoppedSourceAfterMigrationAbort(context.Background(), machine, *sourceHostID); cleanupErr != nil {
						slog.Error("admin.migrate.abort_cleanup.failed", "machine_id", req.MachineID, "error", cleanupErr)
						msg = fmt.Sprintf("%s (machine was stopped, but failed to finalize stopped state: %v)", msg, cleanupErr)
					} else {
						msg = fmt.Sprintf("%s (machine has been left stopped on the source host)", msg)
					}
				}
				failMigration(msg)
				writeError(w, http.StatusInternalServerError, msg)
				return
			}
		} else {
			backupRecord = br
		}
	}

	// Step 3: Destroy VM on source host to clean up the data volume.
	// After StopVM, the agent's orchestrator has removed the VM from its map but
	// preserved the data volume on disk. DestroyVM won't find it in the map —
	// this is expected for stopped VMs. The orphaned data volume on the source
	// host will be cleaned up by the agent's orphan cleanup process.
	// For running→stopped VMs this is a known limitation; for crashed hosts,
	// the destroy call will also fail and is skipped.
	if sourceHostID != nil {
		sourceHost, _ := s.store.GetHost(ctx, *sourceHostID)
		if sourceHost != nil {
			if err := s.agentClient.DestroyVM(ctx, sourceHost, machine.ID); err != nil {
				slog.Warn("admin.migrate.destroy_source.skipped", "machine_id", req.MachineID, "error", err)
			}
		}
	}

	// Step 4: Release source host placement.
	// Since we called StopVM directly (not RuntimeService.Stop()), the capacity
	// counters have NOT been decremented yet. We need ReleaseHard to decrement
	// counters AND clear host_id/vm_ip/home_host_id.
	// For already-stopped machines (status != "running"), Stop() was called previously
	// which already did ReleaseSoft (decremented counters, kept host_id/vm_ip).
	// In that case, we only need to clear the assignment without re-decrementing.
	if sourceHostID != nil {
		releaseCapacity := machine.Status == "running" || machine.Status == "provisioning" || machine.Status == "error"
		if err := s.releaseMigrationSource(ctx, machine, *sourceHostID, releaseCapacity); err != nil {
			msg := "failed to release source host state: " + err.Error()
			failMigration(msg)
			writeError(w, http.StatusInternalServerError, msg)
			return
		}
	}
	slog.Info("admin.migrate.released", "machine_id", req.MachineID)

	// Step 5: Start on target host using targeted placement.
	// If this fails, the machine is stopped with no host — admin can retry or start normally.
	machine, err = s.store.GetMachine(ctx, machine.ID)
	if err != nil {
		msg := "failed to reload machine before start"
		failMigration(msg)
		writeError(w, http.StatusInternalServerError, msg)
		return
	}
	host, _, err := s.machines.StartWithOperation(ctx, machine.AccountID, machine, op.ID, machines.StartOptions{
		TargetHostID: req.TargetHostID,
	})
	if err != nil {
		msg := fmt.Sprintf(
			"failed to start on target host %d: %s (machine is stopped, can be started manually)", req.TargetHostID, err)
		failMigration(msg)
		writeError(w, http.StatusInternalServerError, msg)
		return
	}

	slog.Info("admin.migrate.started_on_target", "machine_id", req.MachineID, "host_id", host.ID)

	// Step 6: If we have a backup, auto-restore it.
	// Wait for running → stop → restore → start.
	if backupRecord != nil {
		// Wait for machine to reach "running" state
		if err := s.waitForMachineStatus(ctx, machine.ID, "running", 3*time.Minute); err != nil {
			msg := fmt.Sprintf(
				"machine started but didn't reach running state: %s (backup_id=%d, restore manually)", err, backupRecord.ID)
			failMigration(msg)
			writeError(w, http.StatusInternalServerError, msg)
			return
		}
		slog.Info("admin.migrate.running_on_target", "machine_id", req.MachineID)

		// Reload machine after Start — it now has new host_id and status
		machine, err = s.store.GetMachine(ctx, machine.ID)
		if err != nil {
			msg := fmt.Sprintf("failed to reload machine after start: %s (backup_id=%d, restore manually)", err, backupRecord.ID)
			failMigration(msg)
			writeError(w, http.StatusInternalServerError, msg)
			return
		}

		// Stop so we can restore
		if err := s.machines.StopWithOperation(ctx, machine, op.ID); err != nil {
			msg := fmt.Sprintf(
				"failed to stop for restore: %s (backup_id=%d, restore manually)", err, backupRecord.ID)
			failMigration(msg)
			writeError(w, http.StatusInternalServerError, msg)
			return
		}

		// Wait for stopped
		if err := s.waitForMachineStatus(ctx, machine.ID, "stopped", 90*time.Second); err != nil {
			msg := fmt.Sprintf(
				"machine didn't stop for restore: %s (backup_id=%d, restore manually)", err, backupRecord.ID)
			failMigration(msg)
			writeError(w, http.StatusInternalServerError, msg)
			return
		}
		slog.Info("admin.migrate.stopped_for_restore", "machine_id", req.MachineID)

		// Restore backup
		machine, err = s.store.GetMachine(ctx, machine.ID)
		if err != nil {
			msg := fmt.Sprintf("failed to reload machine before restore: %s (backup_id=%d, restore manually)", err, backupRecord.ID)
			failMigration(msg)
			writeError(w, http.StatusInternalServerError, msg)
			return
		}
		if machine.HostID == nil {
			msg := fmt.Sprintf(
				"machine has no host after stop (backup_id=%d, restore manually)", backupRecord.ID)
			failMigration(msg)
			writeError(w, http.StatusInternalServerError, msg)
			return
		}
		restoreHost, err := s.store.GetHost(ctx, *machine.HostID)
		if err != nil {
			msg := fmt.Sprintf(
				"failed to get restore host: %s (backup_id=%d, restore manually)", err, backupRecord.ID)
			failMigration(msg)
			writeError(w, http.StatusInternalServerError, msg)
			return
		}

		masterKey, err := hex.DecodeString(s.backupMasterKey)
		if err != nil {
			slog.Error("admin.migrate.master_key_decode_failed", "error", err)
			msg := fmt.Sprintf(
				"invalid backup master key config (backup_id=%d, restore manually)", backupRecord.ID)
			failMigration(msg)
			writeError(w, http.StatusInternalServerError, msg)
			return
		}
		encryptionKey, err := backup.DecryptKey(machine.BackupKey, masterKey)
		if err != nil {
			msg := fmt.Sprintf(
				"failed to decrypt backup key: %s (backup_id=%d, restore manually)", err, backupRecord.ID)
			failMigration(msg)
			writeError(w, http.StatusInternalServerError, msg)
			return
		}

		if err := s.agentClient.RestoreVM(ctx, restoreHost, machine.ID, backupRecord.GCSPath, encryptionKey, backupRecord.Nonce, backupRecord.HMACSHA256); err != nil {
			msg := fmt.Sprintf(
				"restore failed: %s (backup_id=%d, restore manually)", err, backupRecord.ID)
			failMigration(msg)
			writeError(w, http.StatusInternalServerError, msg)
			return
		}
		slog.Info("admin.migrate.restored", "machine_id", req.MachineID, "backup_id", backupRecord.ID)

		// Start again with restored data
		machine, _ = s.store.GetMachine(ctx, machine.ID)
		restartHost, _, err := s.machines.StartWithOperation(ctx, machine.AccountID, machine, op.ID, machines.StartOptions{SkipPoll: true})
		if err != nil {
			msg := fmt.Sprintf(
				"restore succeeded but failed to restart: %s (start manually)", err)
			failMigration(msg)
			writeError(w, http.StatusInternalServerError, msg)
			return
		}
		placementID := ""
		if placement, placementErr := s.store.GetActivePlacement(ctx, machine.ID); placementErr == nil && placement != nil {
			placementID = placement.ID
		}
		if err := s.waitForMachineRunningOnHost(ctx, machine.ID, restartHost, placementID, 3*time.Minute); err != nil {
			msg := fmt.Sprintf(
				"restore succeeded but machine did not return to running: %s (backup_id=%d, inspect target host)", err, backupRecord.ID)
			failMigration(msg)
			writeError(w, http.StatusInternalServerError, msg)
			return
		}
		slog.Info("admin.migrate.complete", "machine_id", req.MachineID, "backup_id", backupRecord.ID)

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":         "migrated_and_restored",
			"machine_id":     req.MachineID,
			"target_host_id": host.ID,
			"backup_id":      backupRecord.ID,
		})
		return
	}

	if err := s.waitForMachineStatus(ctx, machine.ID, "running", 3*time.Minute); err != nil {
		msg := fmt.Sprintf("machine started on target but did not reach running state: %s", err)
		failMigration(msg)
		writeError(w, http.StatusInternalServerError, msg)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":         "migrated",
		"machine_id":     req.MachineID,
		"target_host_id": host.ID,
	})
}

// waitForMachineStatus polls until the machine reaches the desired status or timeout.
func (s *Server) waitForMachineStatus(ctx context.Context, machineID, desiredStatus string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m, err := s.store.GetMachine(ctx, machineID)
		if err != nil {
			return fmt.Errorf("failed to get machine: %w", err)
		}
		if m.Status == desiredStatus {
			return nil
		}
		if m.Status == "error" {
			return fmt.Errorf("machine entered error state")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return fmt.Errorf("timed out waiting for status %s", desiredStatus)
}

// createMigrationBackup creates and persists a backup on the source host for migration.
func (s *Server) createMigrationBackup(ctx context.Context, machine *store.Machine, sourceHostID int) (*store.BackupRecord, error) {
	sourceHost, err := s.store.GetHost(ctx, sourceHostID)
	if err != nil {
		return nil, fmt.Errorf("failed to get source host: %w", err)
	}

	masterKey, err := hex.DecodeString(s.backupMasterKey)
	if err != nil {
		return nil, fmt.Errorf("invalid backup master key: %w", err)
	}
	encryptionKey, err := backup.DecryptKey(machine.BackupKey, masterKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt backup key: %w", err)
	}

	result, err := s.agentClient.BackupVM(ctx, sourceHost, machine.ID, encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("backup VM failed: %w", err)
	}

	ts, _ := time.Parse(time.RFC3339, result.Timestamp)
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	record := &store.BackupRecord{
		MachineID:       machine.ID,
		Timestamp:       ts,
		GCSPath:         result.GCSPath,
		SizeBytes:       result.SizeBytes,
		CompressedBytes: result.CompressedBytes,
		ChecksumSHA256:  result.ChecksumSHA256,
		HMACSHA256:      result.HMAC,
		Nonce:           result.Nonce,
		Trigger:         "migration",
		HostID:          &sourceHostID,
	}
	if err := s.store.CreateBackupRecord(ctx, record); err != nil {
		return nil, fmt.Errorf("failed to save backup record: %w", err)
	}

	slog.Info("admin.migrate.backup_created", "machine_id", machine.ID, "backup_id", record.ID, "gcs_path", result.GCSPath)
	return record, nil
}

func (s *Server) finalizeStoppedSourceAfterMigrationAbort(ctx context.Context, machine *store.Machine, sourceHostID int) error {
	if err := s.store.SoftStopMachine(ctx, machine.ID, sourceHostID, machine.VCPUs, machine.MemoryMB, 0); err != nil {
		return fmt.Errorf("soft stop source machine: %w", err)
	}
	sourceHost := sourceHostID
	if err := s.store.UpdateMachineHomeHost(ctx, machine.ID, &sourceHost); err != nil {
		return fmt.Errorf("set home host: %w", err)
	}
	if err := s.store.ReleasePlacement(ctx, machine.ID); err != nil {
		return fmt.Errorf("release placement record: %w", err)
	}
	s.cleanupMachineIngress(ctx, machine)
	return nil
}

func (s *Server) releaseMigrationSource(ctx context.Context, machine *store.Machine, sourceHostID int, releaseCapacity bool) error {
	if err := s.store.ReleaseMigrationSource(ctx, machine.ID, sourceHostID, machine.VCPUs, machine.MemoryMB, releaseCapacity); err != nil {
		return fmt.Errorf("release migration source: %w", err)
	}
	s.cleanupMachineIngress(ctx, machine)
	return nil
}

func (s *Server) cleanupMachineIngress(ctx context.Context, machine *store.Machine) {
	if s.kvStore != nil {
		account, err := s.store.GetAccount(ctx, machine.AccountID)
		if err != nil {
			slog.Warn("admin.migrate.cleanup.route.account_failed", "machine_id", machine.ID, "error", err)
		} else if err := s.kvStore.DeleteRouteSync(ctx, account.Slug, machine.Slug); err != nil {
			slog.Warn("admin.migrate.cleanup.route_failed", "machine_id", machine.ID, "error", err)
		}
	}

	if s.tunnelMgr != nil && machine.TunnelID != nil {
		vmHostname := s.dataPlaneHostname("m", machine.Slug)
		sshHostname := s.dataPlaneHostname("ssh", machine.Slug)
		if err := s.tunnelMgr.DeleteTunnelAndDNS(ctx, *machine.TunnelID, vmHostname, sshHostname); err != nil {
			slog.Warn("admin.migrate.cleanup.tunnel_failed", "machine_id", machine.ID, "error", err)
		}
		if err := s.store.ClearMachineTunnel(ctx, machine.ID); err != nil {
			slog.Warn("admin.migrate.cleanup.tunnel_clear_failed", "machine_id", machine.ID, "error", err)
		}
	}
}
