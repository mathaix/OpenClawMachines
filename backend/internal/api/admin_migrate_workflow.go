package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/jackc/pgx/v5"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/backup"
	"github.com/mathaix/openclawmachines/backend/internal/fleet"
	"github.com/mathaix/openclawmachines/backend/internal/kvstore"
	"github.com/mathaix/openclawmachines/backend/internal/machines"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/internal/workflows"
)

const (
	migrationPhasePrepare            = "prepare"
	migrationPhaseStopSource         = "stop_source"
	migrationPhaseBackup             = "backup_source"
	migrationPhaseDestroySource      = "destroy_source_vm"
	migrationPhaseReleaseSource      = "release_source"
	migrationPhaseStartTarget        = "start_target"
	migrationPhaseWaitTargetRunning  = "wait_target_running"
	migrationPhaseStopForRestore     = "stop_for_restore"
	migrationPhaseWaitStopped        = "wait_stopped"
	migrationPhaseRestore            = "restore_backup"
	migrationPhaseRestartRestored    = "restart_restored"
	migrationPhaseWaitRestored       = "wait_restored_running"
	migrationPhaseComplete           = "complete"
	migrationWorkflowName            = "api.migration"
	migrationWorkflowRequestTimeout  = 30 * time.Second
	migrationReadyPollInterval       = 3 * time.Second
	migrationReadyPollErrorThreshold = 30 // data volume upgrades can take 40+ seconds
)

type migrationWorkflowInput struct {
	Request           adminMigrationRequest `json:"request"`
	OperationID       string                `json:"operation_id"`
	RequestedByUserID *int                  `json:"requested_by_user_id,omitempty"`
	AccountID         int                   `json:"account_id"`
}

type migrationWorkflowResult struct {
	WorkflowID   string `json:"workflow_id,omitempty"`
	Status       string `json:"status"`
	MachineID    string `json:"machine_id"`
	TargetHostID int    `json:"target_host_id"`
	BackupID     *int   `json:"backup_id,omitempty"`
}

type migrationWorkflowPlan struct {
	SourceHostID    *int `json:"source_host_id,omitempty"`
	ReleaseCapacity bool `json:"release_capacity"`
	CanBackup       bool `json:"can_backup"`
}

type migrationBackupSnapshot struct {
	ID         int    `json:"id"`
	GCSPath    string `json:"gcs_path"`
	Nonce      []byte `json:"nonce"`
	HMACSHA256 []byte `json:"hmac_sha256"`
}

type migrationStartSnapshot struct {
	HostID      int    `json:"host_id"`
	PlacementID string `json:"placement_id,omitempty"`
}

type queuedMigrationResponse struct {
	Status       string `json:"status"`
	WorkflowID   string `json:"workflow_id"`
	MachineID    string `json:"machine_id"`
	TargetHostID int    `json:"target_host_id"`
}

func (s *Server) RegisterWorkflows(dbosCtx dbos.DBOSContext) {
	if s == nil {
		return
	}
	dbos.RegisterWorkflow(dbosCtx, s.runMigrationWorkflow, dbos.WithWorkflowName(migrationWorkflowName))
	dbos.RegisterWorkflow(dbosCtx, s.runInvitationEmailWorkflow, dbos.WithWorkflowName("api.invitation_email"))
}

func (s *Server) enqueueAdminMigrationWorkflow(w http.ResponseWriter, r *http.Request, req adminMigrationRequest) {
	ctx, cancel := context.WithTimeout(r.Context(), migrationWorkflowRequestTimeout)
	defer cancel()

	machine, _, _, _, err := s.validateMigrationRequest(ctx, req)
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, err.Error())
		default:
			var badReq *migrationValidationError
			if errors.As(err, &badReq) {
				writeError(w, http.StatusBadRequest, badReq.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	if s.workflows == nil || s.workflows.Context() == nil {
		writeError(w, http.StatusServiceUnavailable, "workflow runtime not configured")
		return
	}

	workflowID, err := workflows.NewID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate workflow ID")
		return
	}
	claims := auth.UserFromContext(r.Context())
	input := migrationWorkflowInput{
		Request:   req,
		AccountID: machine.AccountID,
	}
	if claims != nil {
		input.RequestedByUserID = &claims.UserID
	}

	op := &store.MachineOperation{
		MachineID:      machine.ID,
		Kind:           "migrate",
		IdempotencyKey: &workflowID,
	}
	if err := s.store.CreateMachineOperation(ctx, op); err != nil {
		writeError(w, http.StatusConflict, "machine has an in-flight operation: "+err.Error())
		return
	}
	input.OperationID = op.ID

	run, err := s.workflows.CreateRun(ctx, workflows.CreateRunParams{
		ID:          workflowID,
		Kind:        workflows.KindMigration,
		ScopeType:   "machine",
		ScopeID:     machine.ID,
		RequestedBy: input.RequestedByUserID,
		AccountID:   &machine.AccountID,
		Priority:    "high",
		InputJSON:   mustRawJSON(input),
	})
	if err != nil {
		msg := "failed to create migration workflow"
		_ = s.store.CompleteMachineOperation(context.Background(), op.ID, "failed", &msg)
		writeError(w, http.StatusInternalServerError, msg)
		return
	}

	if err := s.workflows.AddEvent(ctx, workflows.EventParams{
		WorkflowID: workflowID,
		Level:      "info",
		EventType:  "workflow.queued",
		Message:    "migration queued",
		DetailsJSON: mustRawJSON(map[string]any{
			"machine_id":      req.MachineID,
			"target_host_id":  req.TargetHostID,
			"machine_op_id":   op.ID,
			"requested_by_id": input.RequestedByUserID,
		}),
	}); err != nil {
		msg := err.Error()
		_ = s.workflows.Complete(context.Background(), workflowID, workflows.StatusFailed, nil, strPtr("workflow_queue_failed"), &msg)
		_ = s.store.CompleteMachineOperation(context.Background(), op.ID, "failed", &msg)
		writeError(w, http.StatusInternalServerError, "failed to queue migration workflow")
		return
	}

	if _, err := dbos.RunWorkflow(
		s.workflows.Context(),
		s.runMigrationWorkflow,
		input,
		dbos.WithWorkflowID(run.ID),
		dbos.WithQueue(workflows.QueueMachineLifecycle),
		dbos.WithQueuePartitionKey(req.MachineID),
	); err != nil {
		msg := err.Error()
		_ = s.workflows.Complete(context.Background(), workflowID, workflows.StatusFailed, nil, strPtr("workflow_enqueue_failed"), &msg)
		_ = s.store.CompleteMachineOperation(context.Background(), op.ID, "failed", &msg)
		writeError(w, http.StatusInternalServerError, "failed to enqueue migration workflow")
		return
	}

	writeJSON(w, http.StatusAccepted, queuedMigrationResponse{
		Status:       workflows.StatusQueued,
		WorkflowID:   workflowID,
		MachineID:    req.MachineID,
		TargetHostID: req.TargetHostID,
	})
}

func (s *Server) runMigrationWorkflow(ctx dbos.DBOSContext, input migrationWorkflowInput) (result migrationWorkflowResult, err error) {
	workflowID, wfErr := dbos.GetWorkflowID(ctx)
	if wfErr != nil {
		return migrationWorkflowResult{}, wfErr
	}

	result = migrationWorkflowResult{
		WorkflowID:   workflowID,
		MachineID:    input.Request.MachineID,
		TargetHostID: input.Request.TargetHostID,
	}
	defer s.finalizeMigrationWorkflow(workflowID, input.OperationID, &result, &err)

	s.updateMigrationWorkflowState(ctx, workflowID, workflows.StatusRunning, migrationPhasePrepare, result, "migration started", nil)

	plan, err := dbos.RunAsStep(ctx, func(stepCtx context.Context) (migrationWorkflowPlan, error) {
		_, _, sourceHostID, canBackup, err := s.validateMigrationRequest(stepCtx, input.Request)
		if err != nil {
			return migrationWorkflowPlan{}, err
		}

		machine, err := s.store.GetMachine(stepCtx, input.Request.MachineID)
		if err != nil {
			return migrationWorkflowPlan{}, err
		}

		return migrationWorkflowPlan{
			SourceHostID:    sourceHostID,
			ReleaseCapacity: machine.Status == "running" || machine.Status == "provisioning" || machine.Status == "error",
			CanBackup:       canBackup,
		}, nil
	}, dbos.WithStepName("migration.prepare"),
		dbos.WithStepMaxRetries(3),
		dbos.WithBaseInterval(500*time.Millisecond),
		dbos.WithBackoffFactor(2.0),
	)
	if err != nil {
		return result, err
	}

	if plan.SourceHostID != nil {
		s.updateMigrationWorkflowState(ctx, workflowID, workflows.StatusRunning, migrationPhaseStopSource, result, "stopping source machine", mustRawJSON(map[string]any{"source_host_id": *plan.SourceHostID}))
		if _, err := dbos.RunAsStep(ctx, func(stepCtx context.Context) (bool, error) {
			machine, err := s.store.GetMachine(stepCtx, input.Request.MachineID)
			if err != nil {
				return false, err
			}
			if machine.Status != "running" {
				return false, nil
			}
			sourceHost, err := s.store.GetHost(stepCtx, *plan.SourceHostID)
			if err != nil {
				return false, err
			}
			if err := s.agentClient.StopVM(stepCtx, sourceHost, machine.ID); err != nil {
				return false, fmt.Errorf("failed to stop machine: %w", err)
			}
			return true, nil
		}, dbos.WithStepName("migration.stop_source"),
			dbos.WithStepMaxRetries(3),
			dbos.WithBaseInterval(1*time.Second),
			dbos.WithBackoffFactor(2.0),
		); err != nil {
			return result, err
		}
	}

	var backupSnapshot *migrationBackupSnapshot
	if plan.SourceHostID != nil && plan.CanBackup {
		s.updateMigrationWorkflowState(ctx, workflowID, workflows.StatusRunning, migrationPhaseBackup, result, "creating migration backup", nil)
		backupSnapshot, err = dbos.RunAsStep(ctx, func(stepCtx context.Context) (*migrationBackupSnapshot, error) {
			machine, err := s.store.GetMachine(stepCtx, input.Request.MachineID)
			if err != nil {
				return nil, err
			}
			record, err := s.createMigrationBackup(stepCtx, machine, *plan.SourceHostID)
			if err != nil {
				if input.Request.Force {
					return nil, nil
				}
				return nil, err
			}
			return &migrationBackupSnapshot{
				ID:         record.ID,
				GCSPath:    record.GCSPath,
				Nonce:      record.Nonce,
				HMACSHA256: record.HMACSHA256,
			}, nil
		}, dbos.WithStepName("migration.backup_source"),
			dbos.WithStepMaxRetries(1),
			dbos.WithBaseInterval(2*time.Second),
		)
		if err != nil {
			// Source was already stopped by migration.stop_source — restore its state
			// so the machine isn't left stopped while the DB thinks it's running.
			if _, cleanupErr := dbos.RunAsStep(ctx, func(stepCtx context.Context) (bool, error) {
				machine, mErr := s.store.GetMachine(stepCtx, input.Request.MachineID)
				if mErr != nil {
					return false, fmt.Errorf("abort cleanup: failed to get machine: %w", mErr)
				}
				if fErr := s.finalizeStoppedSourceAfterMigrationAbort(stepCtx, machine, *plan.SourceHostID); fErr != nil {
					return false, fmt.Errorf("abort cleanup: %w", fErr)
				}
				return true, nil
			}, dbos.WithStepName("migration.abort_restore_source")); cleanupErr != nil {
				slog.Error("migration.abort_restore_source: cleanup failed", "machine_id", input.Request.MachineID, "error", cleanupErr)
			}
			return result, err
		}
		if backupSnapshot != nil {
			result.BackupID = &backupSnapshot.ID
		}
	}

	if plan.SourceHostID != nil {
		s.updateMigrationWorkflowState(ctx, workflowID, workflows.StatusRunning, migrationPhaseDestroySource, result, "destroying source VM handle", nil)
		if _, err := dbos.RunAsStep(ctx, func(stepCtx context.Context) (bool, error) {
			sourceHost, err := s.store.GetHost(stepCtx, *plan.SourceHostID)
			if err != nil {
				return false, fmt.Errorf("destroy_source_vm: failed to get host: %w", err)
			}
			if err := s.agentClient.DestroyVM(stepCtx, sourceHost, input.Request.MachineID); err != nil {
				slog.Warn("migration.destroy_source_vm: ignoring agent error (host may be down)", "machine_id", input.Request.MachineID, "host_id", *plan.SourceHostID, "error", err)
			}
			return true, nil
		}, dbos.WithStepName("migration.destroy_source_vm")); err != nil {
			return result, err
		}

		s.updateMigrationWorkflowState(ctx, workflowID, workflows.StatusRunning, migrationPhaseReleaseSource, result, "releasing source placement", nil)
		if _, err := dbos.RunAsStep(ctx, func(stepCtx context.Context) (bool, error) {
			machine, err := s.store.GetMachine(stepCtx, input.Request.MachineID)
			if err != nil {
				return false, err
			}
			if err := s.releaseMigrationSource(stepCtx, machine, *plan.SourceHostID, plan.ReleaseCapacity); err != nil {
				return false, err
			}
			return true, nil
		}, dbos.WithStepName("migration.release_source"),
			dbos.WithStepMaxRetries(3),
			dbos.WithBaseInterval(1*time.Second),
			dbos.WithBackoffFactor(2.0),
		); err != nil {
			return result, err
		}
	}

	s.updateMigrationWorkflowState(ctx, workflowID, workflows.StatusRunning, migrationPhaseStartTarget, result, "starting on target host", nil)
	targetStart, err := dbos.RunAsStep(ctx, func(stepCtx context.Context) (migrationStartSnapshot, error) {
		machine, err := s.store.GetMachine(stepCtx, input.Request.MachineID)
		if err != nil {
			return migrationStartSnapshot{}, err
		}
		if machine.HostID != nil && *machine.HostID == input.Request.TargetHostID {
			return s.currentMigrationStartSnapshot(stepCtx, machine.ID, *machine.HostID)
		}
		host, _, err := s.machines.StartWithOperation(stepCtx, machine.AccountID, machine, input.OperationID, machines.StartOptions{
			TargetHostID: input.Request.TargetHostID,
			SkipPoll:     true,
		})
		if err != nil {
			return migrationStartSnapshot{}, fmt.Errorf("failed to start on target host %d: %w", input.Request.TargetHostID, err)
		}
		return s.currentMigrationStartSnapshot(stepCtx, machine.ID, host.ID)
	}, dbos.WithStepName("migration.start_target"))
	if err != nil {
		return result, err
	}

	s.updateMigrationWorkflowState(ctx, workflowID, workflows.StatusWaiting, migrationPhaseWaitTargetRunning, result, "waiting for target machine to become running", mustRawJSON(map[string]any{"host_id": targetStart.HostID}))
	if _, err := dbos.RunAsStep(ctx, func(stepCtx context.Context) (bool, error) {
		host, err := s.store.GetHost(stepCtx, targetStart.HostID)
		if err != nil {
			return false, err
		}
		if err := s.waitForMachineRunningOnHost(stepCtx, input.Request.MachineID, host, targetStart.PlacementID, 3*time.Minute); err != nil {
			return false, err
		}
		return true, nil
	}, dbos.WithStepName("migration.wait_target_running")); err != nil {
		s.cleanupFailedMigrationTarget(context.Background(), input.Request.MachineID)
		return result, err
	}

	if backupSnapshot == nil {
		result.Status = "migrated"
		s.updateMigrationWorkflowState(ctx, workflowID, workflows.StatusRunning, migrationPhaseComplete, result, "migration completed", nil)
		return result, nil
	}

	s.updateMigrationWorkflowState(ctx, workflowID, workflows.StatusRunning, migrationPhaseStopForRestore, result, "stopping target machine for restore", mustRawJSON(map[string]any{"backup_id": backupSnapshot.ID}))
	if _, err := dbos.RunAsStep(ctx, func(stepCtx context.Context) (bool, error) {
		machine, err := s.store.GetMachine(stepCtx, input.Request.MachineID)
		if err != nil {
			return false, err
		}
		if machine.Status == "stopped" {
			return false, nil
		}
		if err := s.machines.StopWithOperation(stepCtx, machine, input.OperationID); err != nil {
			return false, fmt.Errorf("failed to stop for restore: %w", err)
		}
		return true, nil
	}, dbos.WithStepName("migration.stop_for_restore"),
		dbos.WithStepMaxRetries(3),
		dbos.WithBaseInterval(1*time.Second),
		dbos.WithBackoffFactor(2.0),
	); err != nil {
		return result, err
	}

	s.updateMigrationWorkflowState(ctx, workflowID, workflows.StatusWaiting, migrationPhaseWaitStopped, result, "waiting for machine to stop before restore", nil)
	if _, err := dbos.RunAsStep(ctx, func(stepCtx context.Context) (bool, error) {
		return true, s.waitForMachineStatus(stepCtx, input.Request.MachineID, "stopped", 90*time.Second)
	}, dbos.WithStepName("migration.wait_stopped")); err != nil {
		return result, err
	}

	s.updateMigrationWorkflowState(ctx, workflowID, workflows.StatusRunning, migrationPhaseRestore, result, "restoring backup on target host", mustRawJSON(map[string]any{"backup_id": backupSnapshot.ID}))
	if _, err := dbos.RunAsStep(ctx, func(stepCtx context.Context) (bool, error) {
		machine, err := s.store.GetMachine(stepCtx, input.Request.MachineID)
		if err != nil {
			return false, err
		}
		if machine.HostID == nil {
			return false, fmt.Errorf("machine has no host after stop")
		}
		restoreHost, err := s.store.GetHost(stepCtx, *machine.HostID)
		if err != nil {
			return false, fmt.Errorf("failed to get restore host: %w", err)
		}
		masterKey, err := hex.DecodeString(s.backupMasterKey)
		if err != nil {
			return false, fmt.Errorf("invalid backup master key config: %w", err)
		}
		encryptionKey, err := backup.DecryptKey(machine.BackupKey, masterKey)
		if err != nil {
			return false, fmt.Errorf("failed to decrypt backup key: %w", err)
		}
		if err := s.agentClient.RestoreVM(stepCtx, restoreHost, machine.ID, backupSnapshot.GCSPath, encryptionKey, backupSnapshot.Nonce, backupSnapshot.HMACSHA256); err != nil {
			return false, fmt.Errorf("restore failed: %w", err)
		}
		return true, nil
	}, dbos.WithStepName("migration.restore_backup")); err != nil {
		return result, err
	}

	s.updateMigrationWorkflowState(ctx, workflowID, workflows.StatusRunning, migrationPhaseRestartRestored, result, "restarting restored machine", nil)
	restoredStart, err := dbos.RunAsStep(ctx, func(stepCtx context.Context) (migrationStartSnapshot, error) {
		machine, err := s.store.GetMachine(stepCtx, input.Request.MachineID)
		if err != nil {
			return migrationStartSnapshot{}, err
		}
		host, _, err := s.machines.StartWithOperation(stepCtx, machine.AccountID, machine, input.OperationID, machines.StartOptions{SkipPoll: true})
		if err != nil {
			return migrationStartSnapshot{}, fmt.Errorf("restore succeeded but failed to restart: %w", err)
		}
		return s.currentMigrationStartSnapshot(stepCtx, machine.ID, host.ID)
	}, dbos.WithStepName("migration.restart_restored"))
	if err != nil {
		return result, err
	}

	s.updateMigrationWorkflowState(ctx, workflowID, workflows.StatusWaiting, migrationPhaseWaitRestored, result, "waiting for restored machine to become running", nil)
	if _, err := dbos.RunAsStep(ctx, func(stepCtx context.Context) (bool, error) {
		host, err := s.store.GetHost(stepCtx, restoredStart.HostID)
		if err != nil {
			return false, err
		}
		if err := s.waitForMachineRunningOnHost(stepCtx, input.Request.MachineID, host, restoredStart.PlacementID, 3*time.Minute); err != nil {
			return false, err
		}
		return true, nil
	}, dbos.WithStepName("migration.wait_restored_running")); err != nil {
		s.cleanupFailedMigrationTarget(context.Background(), input.Request.MachineID)
		return result, err
	}

	result.Status = "migrated_and_restored"
	s.updateMigrationWorkflowState(ctx, workflowID, workflows.StatusRunning, migrationPhaseComplete, result, "migration and restore completed", nil)
	return result, nil
}

func (s *Server) finalizeMigrationWorkflow(workflowID, operationID string, result *migrationWorkflowResult, workflowErr *error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if workflowErr != nil && *workflowErr != nil {
		msg := (*workflowErr).Error()
		s.updateMigrationWorkflowState(ctx, workflowID, workflows.StatusFailed, migrationPhaseComplete, derefMigrationResult(result), "migration failed", mustRawJSON(map[string]any{"error": msg}))
		if err := s.workflows.Complete(ctx, workflowID, workflows.StatusFailed, mustRawJSON(derefMigrationResult(result)), strPtr("migration_failed"), &msg); err != nil {
			slog.Error("finalizeMigrationWorkflow: failed to complete workflow as failed", "workflow_id", workflowID, "operation_id", operationID, "error", err)
		}
		if err := s.store.CompleteMachineOperation(ctx, operationID, "failed", &msg); err != nil {
			slog.Error("finalizeMigrationWorkflow: failed to complete machine operation as failed", "workflow_id", workflowID, "operation_id", operationID, "error", err)
		}
		return
	}

	if result != nil {
		if err := s.workflows.Complete(ctx, workflowID, workflows.StatusCompleted, mustRawJSON(*result), nil, nil); err != nil {
			slog.Error("finalizeMigrationWorkflow: failed to complete workflow as completed", "workflow_id", workflowID, "operation_id", operationID, "error", err)
		}
	}
	if err := s.store.CompleteMachineOperation(ctx, operationID, "completed", nil); err != nil {
		slog.Error("finalizeMigrationWorkflow: failed to complete machine operation as completed", "workflow_id", workflowID, "operation_id", operationID, "error", err)
	}
}

func (s *Server) validateMigrationRequest(ctx context.Context, req adminMigrationRequest) (*store.Machine, *store.Host, *int, bool, error) {
	machine, err := s.store.GetMachine(ctx, req.MachineID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil, false, err
		}
		return nil, nil, nil, false, fmt.Errorf("machine lookup failed: %w", err)
	}

	targetHost, err := s.store.GetHost(ctx, req.TargetHostID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil, false, err
		}
		return nil, nil, nil, false, fmt.Errorf("target host lookup failed: %w", err)
	}
	if targetHost.Status != "ready" {
		return nil, nil, nil, false, &migrationValidationError{msg: fmt.Sprintf("target host is %s, must be ready", targetHost.Status)}
	}
	if machine.HostID != nil && *machine.HostID == req.TargetHostID {
		return nil, nil, nil, false, &migrationValidationError{msg: "machine is already on the target host"}
	}

	sourceHostID := machine.HostID
	canBackup := sourceHostID != nil && machine.BackupsEnabled && s.backupMasterKey != ""
	if sourceHostID != nil && !canBackup && !req.Force {
		return nil, nil, nil, false, &migrationValidationError{msg: "machine has a source host but backups are not enabled or configured — data volume will be lost. Pass force=true to proceed anyway."}
	}

	return machine, targetHost, sourceHostID, canBackup, nil
}

func (s *Server) currentMigrationStartSnapshot(ctx context.Context, machineID string, hostID int) (migrationStartSnapshot, error) {
	start := migrationStartSnapshot{HostID: hostID}
	placement, err := s.store.GetActivePlacement(ctx, machineID)
	if err != nil {
		slog.Warn("currentMigrationStartSnapshot: failed to get active placement", "machine_id", machineID, "host_id", hostID, "error", err)
	} else if placement != nil {
		start.PlacementID = placement.ID
	}
	return start, nil
}

func (s *Server) waitForMachineRunningOnHost(ctx context.Context, machineID string, host *store.Host, placementID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	consecutiveErrors := 0
	var lastErr error

	for time.Now().Before(deadline) {
		vm, err := s.agentClient.GetVM(ctx, host, machineID)
		if err != nil {
			lastErr = err
			consecutiveErrors++
			if consecutiveErrors >= migrationReadyPollErrorThreshold {
				msg := fmt.Sprintf("failed to poll VM status: %v", err)
				if err := s.store.UpdateMachineStatus(context.Background(), machineID, "error", &msg); err != nil {
					slog.Error("waitForMachineRunningOnHost: failed to update machine status", "machine_id", machineID, "status", "error", "error", err)
				}
				return errors.New(msg)
			}
		} else {
			consecutiveErrors = 0
			switch vm.Status {
			case "running":
				if err := s.syncMachineRouteToKV(ctx, machineID, host); err != nil {
					msg := fmt.Sprintf("KV sync failed: %v", err)
					if err := s.store.UpdateMachineStatus(context.Background(), machineID, "error", &msg); err != nil {
						slog.Error("waitForMachineRunningOnHost: failed to update machine status", "machine_id", machineID, "status", "error", "error", err)
					}
					return errors.New(msg)
				}
				if placementID != "" {
					if _, err := s.store.ActivatePlacement(ctx, placementID); err != nil {
						return fmt.Errorf("activate placement: %w", err)
					}
				}
				if err := s.store.UpdateMachineStatus(ctx, machineID, "running", nil); err != nil {
					slog.Error("waitForMachineRunningOnHost: failed to update machine status", "machine_id", machineID, "status", "running", "error", err)
				}
				return nil
			case "error":
				msg := vm.ErrorMessage
				if msg == "" {
					msg = "VM creation failed"
				}
				if err := s.store.UpdateMachineStatus(context.Background(), machineID, "error", &msg); err != nil {
					slog.Error("waitForMachineRunningOnHost: failed to update machine status", "machine_id", machineID, "status", "error", "error", err)
				}
				return errors.New(msg)
			case "stopped":
				msg := "VM stopped unexpectedly during creation"
				if err := s.store.UpdateMachineStatus(context.Background(), machineID, "stopped", &msg); err != nil {
					slog.Error("waitForMachineRunningOnHost: failed to update machine status", "machine_id", machineID, "status", "stopped", "error", err)
				}
				return errors.New(msg)
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(migrationReadyPollInterval):
		}
	}

	msg := "timeout waiting for VM to become running"
	if lastErr != nil {
		msg = fmt.Sprintf("timeout waiting for VM to become running: %v", lastErr)
	}
	if err := s.store.UpdateMachineStatus(context.Background(), machineID, "error", &msg); err != nil {
		slog.Error("waitForMachineRunningOnHost: failed to update machine status", "machine_id", machineID, "status", "error", "error", err)
	}
	return errors.New(msg)
}

func (s *Server) syncMachineRouteToKV(ctx context.Context, machineID string, host *store.Host) error {
	if s.kvStore == nil {
		return nil
	}
	if host.TunnelURL == nil {
		return fmt.Errorf("host has no tunnel URL")
	}

	machine, err := s.store.GetMachine(ctx, machineID)
	if err != nil {
		return fmt.Errorf("failed to get machine: %w", err)
	}
	account, err := s.store.GetAccount(ctx, machine.AccountID)
	if err != nil {
		return fmt.Errorf("failed to get account: %w", err)
	}

	proxyToken := ""
	if machine.ProxyToken != nil {
		proxyToken = *machine.ProxyToken
	}
	return s.kvStore.PutRouteSync(ctx, account.Slug, machine.Slug, kvstore.RouteEntry{
		MachineID:    machine.ID,
		HostHostname: *host.TunnelURL,
		ProxyToken:   proxyToken,
	})
}

func (s *Server) updateMigrationWorkflowState(ctx context.Context, workflowID, status, phase string, result migrationWorkflowResult, message string, details json.RawMessage) {
	if s.workflows == nil {
		return
	}
	phaseCopy := phase
	summary := mustRawJSON(map[string]any{
		"workflow_id":    workflowID,
		"status":         result.Status,
		"machine_id":     result.MachineID,
		"target_host_id": result.TargetHostID,
		"backup_id":      result.BackupID,
	})
	if err := s.workflows.UpdateStatus(ctx, workflowID, status, &phaseCopy, summary, nil, nil); err != nil {
		slog.Warn("updateMigrationWorkflowState: failed to update status", "workflow_id", workflowID, "phase", phase, "error", err)
	}
	if err := s.workflows.AddEvent(ctx, workflows.EventParams{
		WorkflowID:  workflowID,
		Phase:       &phaseCopy,
		Level:       "info",
		EventType:   "workflow.phase",
		Message:     message,
		DetailsJSON: details,
	}); err != nil {
		slog.Warn("updateMigrationWorkflowState: failed to add event", "workflow_id", workflowID, "phase", phase, "error", err)
	}
}

type migrationValidationError struct {
	msg string
}

func (e *migrationValidationError) Error() string {
	return e.msg
}

func derefMigrationResult(result *migrationWorkflowResult) migrationWorkflowResult {
	if result == nil {
		return migrationWorkflowResult{}
	}
	return *result
}

func mustRawJSON(v any) json.RawMessage {
	if v == nil {
		return json.RawMessage(`null`)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		slog.Error("mustRawJSON: marshal failed", "error", err)
		return json.RawMessage(`{}`)
	}
	return raw
}

// cleanupFailedMigrationTarget stops the VM (if running), releases placement,
// and cleans up ingress/tunnels after a failed migration start. Mirrors the
// cleanup that pollVMStatus does in the normal (non-workflow) machine start path.
func (s *Server) cleanupFailedMigrationTarget(ctx context.Context, machineID string) {
	machine, err := s.store.GetMachine(ctx, machineID)
	if err != nil {
		slog.Error("migration.cleanup_target: failed to get machine", "machine_id", machineID, "error", err)
		return
	}

	// Stop the VM on the host to avoid orphaned microVMs
	if s.agentClient != nil && machine.HostID != nil {
		host, err := s.store.GetHost(ctx, *machine.HostID)
		if err != nil {
			slog.Error("migration.cleanup_target: get host failed", "machine_id", machineID, "host_id", *machine.HostID, "error", err)
		} else {
			if err := s.agentClient.DestroyVM(ctx, host, machineID); err != nil {
				slog.Warn("migration.cleanup_target: destroy VM failed (may not exist)", "machine_id", machineID, "error", err)
			}
		}
	}

	if err := s.placement.Release(ctx, machineID, fleet.ReleaseHard); err != nil {
		slog.Error("migration.cleanup_target: release placement failed", "machine_id", machineID, "error", err)
	}

	s.cleanupMachineIngress(ctx, machine)
}

func strPtr(s string) *string {
	return &s
}
