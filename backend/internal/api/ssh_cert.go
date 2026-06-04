package api

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/events"
)

type sshCertRequest struct {
	PublicKey string `json:"public_key"`
}

type sshCertResponse struct {
	Certificate string `json:"certificate"`
	ExpiresAt   string `json:"expires_at"`
	Username    string `json:"username"`
	Hostname    string `json:"hostname"`
}

func (s *Server) handleSSHCert(w http.ResponseWriter, r *http.Request) {
	if s.sshCAPrivateKey == "" {
		writeError(w, http.StatusServiceUnavailable, "SSH CA not configured")
		return
	}

	// Parse request
	var req sshCertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.PublicKey == "" {
		writeError(w, http.StatusBadRequest, "public_key is required")
		return
	}

	// Parse the client's public key
	clientPubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(req.PublicKey))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid public key format")
		return
	}

	// Get authenticated user
	claims := auth.UserFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Get machine and verify ownership (AccountMiddleware already checked membership)
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

	// Check machine is running
	if machine.Status != "running" {
		writeError(w, http.StatusConflict, fmt.Sprintf("machine is %s, must be running", machine.Status))
		return
	}

	// Parse CA private key
	signer, err := ssh.ParsePrivateKey([]byte(s.sshCAPrivateKey))
	if err != nil {
		slog.Error("ssh_cert.parse_ca_key_failed", "error", err)
		writeError(w, http.StatusInternalServerError, "SSH CA configuration error")
		return
	}

	// Generate random serial
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		slog.Error("ssh_cert.serial_generation_failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	now := time.Now()
	validBefore := now.Add(8 * time.Hour)

	// Create SSH certificate
	cert := &ssh.Certificate{
		CertType:        ssh.UserCert,
		Key:             clientPubKey,
		Serial:          serial.Uint64(),
		KeyId:           fmt.Sprintf("%s@%s", claims.Email, machine.Slug),
		ValidPrincipals: []string{claims.Email, "openclaw"},
		ValidAfter:      uint64(now.Add(-5 * time.Minute).Unix()), // 5min grace for clock skew
		ValidBefore:     uint64(validBefore.Unix()),
		Permissions: ssh.Permissions{
			Extensions: map[string]string{
				"permit-pty":              "",
				"permit-port-forwarding":  "",
				"permit-agent-forwarding": "",
			},
		},
	}

	if err := cert.SignCert(rand.Reader, signer); err != nil {
		slog.Error("ssh_cert.sign_failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to sign certificate")
		return
	}

	slog.Info("ssh_cert.issued",
		"user", claims.Email,
		"machine", machine.Slug,
		"serial", cert.Serial,
		"expires", validBefore.Format(time.RFC3339),
	)

	hostname := fmt.Sprintf("ssh-%s.%s", machine.Slug, s.dataPlaneDomain)

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "machine",
		Action:    "machine.ssh_cert_issued",
		Status:    "success",
		ActorType: "user",
		ActorID:   &claims.UserID,
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("SSH certificate issued for '%s'", machine.Name),
		Detail:    map[string]any{"serial": cert.Serial, "expires_at": validBefore.Format(time.RFC3339)},
	})

	writeJSON(w, http.StatusOK, sshCertResponse{
		Certificate: string(ssh.MarshalAuthorizedKey(cert)),
		ExpiresAt:   validBefore.Format(time.RFC3339),
		Username:    "openclaw",
		Hostname:    hostname,
	})
}
