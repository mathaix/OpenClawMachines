package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	// Verify machine exists and belongs to account
	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	secrets, err := s.store.ListSecrets(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return keys only (no values)
	writeJSON(w, http.StatusOK, secrets)
}

func (s *Server) handleSetSecret(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())

	id := chi.URLParam(r, "id")
	key := chi.URLParam(r, "key")

	if s.secretKey == "" {
		writeError(w, http.StatusInternalServerError, "SECRET_ENCRYPTION_KEY not configured")
		return
	}

	// Verify machine exists and belongs to account
	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	var req struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Value == "" {
		writeError(w, http.StatusBadRequest, "value is required")
		return
	}

	encrypted, err := crypto.Encrypt(req.Value, s.secretKey)
	if err != nil {
		slog.Error("secret.encrypt.failed", "machine_id", id, "secret_key", key, "error", err)
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}

	if err := s.store.SetSecret(r.Context(), id, key, encrypted); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Push updated secrets to running VM (best-effort, don't fail the API call)
	go s.pushSecretsToRunningVM(context.Background(), machine.ID)

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.secret_added",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &id,
		Summary:   fmt.Sprintf("Added secret '%s' to '%s'", key, machine.Name),
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())

	id := chi.URLParam(r, "id")
	key := chi.URLParam(r, "key")

	// Verify machine exists and belongs to account
	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	if err := s.store.DeleteSecret(r.Context(), id, key); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Push updated secrets to running VM (best-effort)
	go s.pushSecretsToRunningVM(context.Background(), machine.ID)

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.secret_removed",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &id,
		Summary:   fmt.Sprintf("Removed secret '%s' from '%s'", key, machine.Name),
	})

	w.WriteHeader(http.StatusNoContent)
}

// pushSecretsToRunningVM decrypts all secrets and pushes them to the agent
// if the machine is currently running on a host.
func (s *Server) pushSecretsToRunningVM(ctx context.Context, machineID string) {
	machine, err := s.store.GetMachine(ctx, machineID)
	if err != nil {
		slog.Error("secrets.push.machine.get.failed", "machine_id", machineID, "error", err)
		return
	}

	// Only push if machine is running and assigned to a host
	if machine.Status != "running" || machine.HostID == nil {
		return
	}

	host, err := s.store.GetHost(ctx, *machine.HostID)
	if err != nil {
		slog.Error("secrets.push.host.get.failed", "machine_id", machineID, "host_id", *machine.HostID, "error", err)
		return
	}

	// Decrypt all secrets
	secrets, err := s.store.ListSecretsWithValues(ctx, machineID)
	if err != nil {
		slog.Error("secrets.push.list.failed", "machine_id", machineID, "error", err)
		return
	}

	decrypted := make(map[string]string)
	for _, sec := range secrets {
		val, err := crypto.Decrypt(sec.EncryptedValue, s.secretKey)
		if err != nil {
			slog.Error("secrets.push.decrypt.failed", "machine_id", machineID, "secret_key", sec.Key, "error", err)
			continue
		}
		decrypted[sec.Key] = val
	}

	if err := s.agentClient.UpdateVMSecrets(ctx, host, machineID, decrypted); err != nil {
		slog.Error("secrets.push.agent.failed", "machine_id", machineID, "error", err)
		return
	}

	slog.Info("secrets.push.completed", "machine_id", machineID, "host_name", host.VMName, "secret_count", len(decrypted))
}
