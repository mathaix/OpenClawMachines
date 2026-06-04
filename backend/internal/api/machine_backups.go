package api

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/backup"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

func (s *Server) handleListMachineBackups(w http.ResponseWriter, r *http.Request) {
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

	backups, err := s.store.ListBackupRecords(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list backups")
		return
	}
	if backups == nil {
		backups = []store.BackupRecord{}
	}

	writeJSON(w, http.StatusOK, backups)
}

func (s *Server) handleCreateMachineBackup(w http.ResponseWriter, r *http.Request) {
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
	if machine.Status != "stopped" {
		writeError(w, http.StatusBadRequest, "machine must be stopped to create a backup")
		return
	}
	if !machine.BackupsEnabled {
		writeError(w, http.StatusBadRequest, "backups are not enabled for this machine")
		return
	}
	if machine.HostID == nil {
		writeError(w, http.StatusBadRequest, "machine has no host assigned (never started)")
		return
	}

	// Acquire operation lock to prevent concurrent backup/restore/start/stop
	op := &store.MachineOperation{MachineID: machineID, Kind: "backup"}
	if err := s.store.CreateMachineOperation(r.Context(), op); err != nil {
		writeError(w, http.StatusConflict, "machine has an in-flight operation")
		return
	}
	defer func() {
		_ = s.store.CompleteMachineOperation(r.Context(), op.ID, "completed", nil)
	}()

	masterKey, err := hex.DecodeString(s.backupMasterKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid backup master key config")
		return
	}
	encryptionKey, err := backup.DecryptKey(machine.BackupKey, masterKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decrypt backup key")
		return
	}

	host, err := s.store.GetHost(r.Context(), *machine.HostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get host")
		return
	}

	result, err := s.agentClient.BackupVM(r.Context(), host, machineID, encryptionKey)
	if err != nil {
		slog.Error("backup.create.agent_failed", "machine_id", machineID, "error", err)
		errMsg := err.Error()
		s.activity.Log(r.Context(), events.LogParams{
			Category:  "backup",
			Action:    "backup.create_failed",
			Status:    "failure",
			ActorType: "user",
			ActorID:   userIDFromContext(r.Context()),
			AccountID: &accountID,
			MachineID: &machineID,
			Summary:   fmt.Sprintf("Backup creation failed for '%s'", machine.Name),
			ErrorMsg:  &errMsg,
		})
		writeError(w, http.StatusInternalServerError, "backup failed: "+err.Error())
		return
	}

	ts, _ := time.Parse(time.RFC3339, result.Timestamp)
	record := &store.BackupRecord{
		MachineID:       machineID,
		Timestamp:       ts,
		GCSPath:         result.GCSPath,
		SizeBytes:       result.SizeBytes,
		CompressedBytes: result.CompressedBytes,
		ChecksumSHA256:  result.ChecksumSHA256,
		HMACSHA256:      result.HMAC,
		Nonce:           result.Nonce,
		Trigger:         "manual",
		HostID:          machine.HostID,
	}
	if err := s.store.CreateBackupRecord(r.Context(), record); err != nil {
		slog.Error("backup.create.store_failed", "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "backup created but failed to store metadata")
		return
	}

	// Enforce retention: delete oldest if > 3
	s.enforceBackupRetention(r.Context(), machineID, host)

	writeJSON(w, http.StatusCreated, record)

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "backup",
		Action:    "backup.created",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Created backup for '%s'", machine.Name),
		Detail:    map[string]any{"size_bytes": result.SizeBytes},
	})
}

func (s *Server) handleRestoreMachineBackup(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")
	backupIDStr := chi.URLParam(r, "backupId")
	backupID, err := strconv.Atoi(backupIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid backup ID")
		return
	}

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil || machine.AccountID != accountID {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.Status != "stopped" {
		writeError(w, http.StatusBadRequest, "machine must be stopped to restore a backup")
		return
	}
	if machine.HostID == nil {
		writeError(w, http.StatusBadRequest, "machine has no host assigned")
		return
	}

	record, err := s.store.GetBackupRecord(r.Context(), backupID)
	if err != nil || record.MachineID != machineID {
		writeError(w, http.StatusNotFound, "backup not found")
		return
	}

	// Acquire operation lock to prevent concurrent backup/restore/start/stop
	op := &store.MachineOperation{MachineID: machineID, Kind: "restore"}
	if err := s.store.CreateMachineOperation(r.Context(), op); err != nil {
		writeError(w, http.StatusConflict, "machine has an in-flight operation")
		return
	}
	defer func() {
		_ = s.store.CompleteMachineOperation(r.Context(), op.ID, "completed", nil)
	}()

	masterKey, err := hex.DecodeString(s.backupMasterKey)
	if err != nil {
		slog.Error("backup.restore.master_key_decode_failed", "error", err)
		writeError(w, http.StatusInternalServerError, "invalid backup master key config")
		return
	}
	encryptionKey, err := backup.DecryptKey(machine.BackupKey, masterKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decrypt backup key")
		return
	}

	host, err := s.store.GetHost(r.Context(), *machine.HostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get host")
		return
	}

	if err := s.agentClient.RestoreVM(r.Context(), host, machineID, record.GCSPath, encryptionKey, record.Nonce, record.HMACSHA256); err != nil {
		errMsg := err.Error()
		s.activity.Log(r.Context(), events.LogParams{
			Category:  "backup",
			Action:    "backup.restore_failed",
			Status:    "failure",
			ActorType: "user",
			ActorID:   userIDFromContext(r.Context()),
			AccountID: &accountID,
			MachineID: &machineID,
			Summary:   fmt.Sprintf("Backup restore failed for '%s'", machine.Name),
			ErrorMsg:  &errMsg,
		})
		writeError(w, http.StatusInternalServerError, "restore failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "restored"})

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "backup",
		Action:    "backup.restored",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Restored backup for '%s'", machine.Name),
		Detail:    map[string]any{"backup_id": backupID},
	})
}

func (s *Server) handleDeleteMachineBackup(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")
	backupIDStr := chi.URLParam(r, "backupId")
	backupID, _ := strconv.Atoi(backupIDStr)

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil || machine.AccountID != accountID {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	record, err := s.store.GetBackupRecord(r.Context(), backupID)
	if err != nil || record.MachineID != machineID {
		writeError(w, http.StatusNotFound, "backup not found")
		return
	}

	// Delete GCS object via agent — fail the request if GCS delete fails so the user can retry
	if machine.HostID != nil {
		host, hostErr := s.store.GetHost(r.Context(), *machine.HostID)
		if hostErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to get host")
			return
		}
		if delErr := s.agentClient.DeleteBackupObject(r.Context(), host, machineID, record.GCSPath); delErr != nil {
			slog.Error("backup.delete_gcs_object.failed", "gcs_path", record.GCSPath, "error", delErr)
			writeError(w, http.StatusBadGateway, "failed to delete backup from storage — try again later")
			return
		}
	}

	if err := s.store.DeleteBackupRecord(r.Context(), backupID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete backup")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDownloadMachineBackup(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")
	backupIDStr := chi.URLParam(r, "backupId")
	backupID, _ := strconv.Atoi(backupIDStr)

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil || machine.AccountID != accountID {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	record, err := s.store.GetBackupRecord(r.Context(), backupID)
	if err != nil || record.MachineID != machineID {
		writeError(w, http.StatusNotFound, "backup not found")
		return
	}

	masterKey, err := hex.DecodeString(s.backupMasterKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid backup config")
		return
	}
	encryptionKey, err := backup.DecryptKey(machine.BackupKey, masterKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decrypt backup key")
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "tar.gz"
	}

	// Path 1: Host available → proxy through agent (supports tar.gz and ext4)
	if machine.HostID != nil {
		host, hostErr := s.store.GetHost(r.Context(), *machine.HostID)
		if hostErr == nil {
			if proxyErr := s.proxyBackupDownload(w, r, host, machineID, record, encryptionKey, format); proxyErr == nil {
				return // proxy succeeded
			} else {
				slog.Warn("backup.download.agent_proxy_failed", "machine_id", machineID, "error", proxyErr)
				// Fall through to direct GCS
			}
		} else {
			slog.Warn("backup.download.host_lookup_failed", "machine_id", machineID, "error", hostErr)
		}
	}

	// Path 2: No host → direct GCS stream (ext4 only — Cloud Run can't mount for tar.gz)
	if format == "tar.gz" {
		writeError(w, http.StatusBadRequest, "tar.gz format requires a running host; use format=ext4 or start the machine first")
		return
	}

	if s.backupStore == nil {
		writeError(w, http.StatusServiceUnavailable, "direct backup downloads not configured")
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	filename := fmt.Sprintf("%s-backup-%d.ext4", machineID, backupID)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	cw := &countWriter{w: w}
	if err := s.backupStore.StreamDecrypted(r.Context(), record.GCSPath, encryptionKey, record.Nonce, record.HMACSHA256, cw); err != nil {
		slog.Error("backup.download.direct_stream_failed", "machine_id", machineID, "backup_id", backupID, "error", err)
		if cw.n == 0 {
			// Headers not committed yet — send a proper error response
			w.Header().Del("Content-Disposition")
			w.Header().Del("Content-Type")
			writeError(w, http.StatusInternalServerError, "backup download failed")
		}
		return
	}
}

// backfillBackupKeys generates and stores backup keys for all existing machines
// that don't have one yet. Runs once at startup in a goroutine.
func (s *Server) backfillBackupKeys(ctx context.Context) {
	ids, err := s.store.ListMachinesWithoutBackupKey(ctx)
	if err != nil {
		slog.Error("backup.backfill.list_failed", "error", err)
		return
	}
	if len(ids) == 0 {
		return
	}

	masterKey, err := hex.DecodeString(s.backupMasterKey)
	if err != nil {
		slog.Error("backup.backfill.master_key_decode_failed", "error", err)
		return
	}

	count := 0
	for _, id := range ids {
		machineKey, err := backup.GenerateKey()
		if err != nil {
			slog.Error("backup.backfill.generate_key_failed", "machine_id", id, "error", err)
			continue
		}
		encryptedKey, err := backup.EncryptKey(machineKey, masterKey)
		if err != nil {
			slog.Error("backup.backfill.encrypt_key_failed", "machine_id", id, "error", err)
			continue
		}
		if err := s.store.EnableBackups(ctx, id, encryptedKey); err != nil {
			slog.Error("backup.backfill.enable_failed", "machine_id", id, "error", err)
			continue
		}
		count++
	}
	slog.Info("backup.backfill.complete", "total", len(ids), "enabled", count)
}

// countWriter wraps an io.Writer and counts bytes written.
type countWriter struct {
	w io.Writer
	n int64
}

func (cw *countWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.n += int64(n)
	return n, err
}

// proxyBackupDownload proxies a backup download through the host agent.
// Returns an error if the agent is unreachable (caller should fall back to direct GCS).
// Returns nil if the proxy completed (regardless of agent response status).
func (s *Server) proxyBackupDownload(w http.ResponseWriter, r *http.Request, host *store.Host, machineID string, record *store.BackupRecord, encryptionKey []byte, format string) error {
	agentURL := fmt.Sprintf("%s/vms/%s/backup-download?gcs_path=%s&format=%s",
		s.agentClient.AgentURL(host), machineID, record.GCSPath, format)

	agentReq, _ := http.NewRequestWithContext(r.Context(), "GET", agentURL, nil)
	agentReq.Header.Set("Authorization", "Bearer "+s.agentClient.TokenForHost(host))
	agentReq.Header.Set("X-Backup-Key", hex.EncodeToString(encryptionKey))
	agentReq.Header.Set("X-Backup-Nonce", hex.EncodeToString(record.Nonce))
	agentReq.Header.Set("X-Backup-HMAC", hex.EncodeToString(record.HMACSHA256))

	resp, err := http.DefaultClient.Do(agentReq)
	if err != nil {
		return fmt.Errorf("agent unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return nil
}

func (s *Server) enforceBackupRetention(ctx context.Context, machineID string, host *store.Host) {
	backups, err := s.store.ListBackupRecords(ctx, machineID)
	if err != nil || len(backups) <= 3 {
		return
	}
	for _, b := range backups[3:] {
		// Delete GCS object via agent — skip DB delete if GCS delete fails to avoid orphaning
		if host != nil {
			if delErr := s.agentClient.DeleteBackupObject(ctx, host, machineID, b.GCSPath); delErr != nil {
				slog.Warn("backup.retention.delete_gcs.failed", "gcs_path", b.GCSPath, "error", delErr)
				continue
			}
		}
		_ = s.store.DeleteBackupRecord(ctx, b.ID)
	}
}
