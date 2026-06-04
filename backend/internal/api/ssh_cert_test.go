package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// sshCertTestStore is a minimal store stub that only implements GetMachine.
type sshCertTestStore struct {
	store.Store // embed interface — all methods panic unless overridden
	machines    map[string]*store.Machine
}

func (s *sshCertTestStore) GetMachine(_ context.Context, id string) (*store.Machine, error) {
	m, ok := s.machines[id]
	if !ok {
		return nil, fmt.Errorf("machine %s not found", id)
	}
	return m, nil
}

func (s *sshCertTestStore) GetAccountMember(_ context.Context, accountID, userID int) (*store.AccountMember, error) {
	return &store.AccountMember{AccountID: accountID, UserID: userID, Role: "owner"}, nil
}

// generateTestCAKey creates a CA private key in OpenSSH PEM format for testing.
func generateTestCAKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	// MarshalPrivateKey returns a *pem.Block; encode to PEM format.
	pemBlock, err := ssh.MarshalPrivateKey(priv, "test-ca")
	if err != nil {
		t.Fatal(err)
	}
	_ = signer // just to verify it works
	return string(pem.EncodeToMemory(pemBlock))
}

// generateTestPublicKey creates an ephemeral client key for testing.
func generateTestPublicKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(sshPub))
}

func TestHandleSSHCert_Success(t *testing.T) {
	caKey := generateTestCAKey(t)
	clientPubKey := generateTestPublicKey(t)

	testStore := &sshCertTestStore{
		machines: map[string]*store.Machine{
			"m1": {
				ID:        "m1",
				AccountID: 1,
				Slug:      "my-bot",
				Status:    "running",
			},
		},
	}

	srv := &Server{
		store:           testStore,
		sshCAPrivateKey: caKey,
		dataPlaneDomain: "openclawmachines.com",
	}

	body, _ := json.Marshal(sshCertRequest{PublicKey: clientPubKey})
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines/m1/ssh-cert", bytes.NewReader(body))

	// Set auth context
	ctx := auth.WithUser(req.Context(), &auth.Claims{UserID: 1, Email: "test@example.com"})
	ctx = context.WithValue(ctx, accountIDKey, 1)

	// Set chi URL params
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "m1")
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)

	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	srv.handleSSHCert(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp sshCertResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Certificate == "" {
		t.Fatal("expected non-empty certificate")
	}
	if resp.Username != "openclaw" {
		t.Errorf("username = %q, want openclaw", resp.Username)
	}
	if resp.Hostname != "ssh-my-bot.openclawmachines.com" {
		t.Errorf("hostname = %q, want ssh-my-bot.openclawmachines.com", resp.Hostname)
	}
	if resp.ExpiresAt == "" {
		t.Error("expected non-empty expires_at")
	}

	// Parse the certificate and verify principals
	certKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(resp.Certificate))
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	cert, ok := certKey.(*ssh.Certificate)
	if !ok {
		t.Fatal("parsed key is not a certificate")
	}
	if cert.CertType != ssh.UserCert {
		t.Errorf("cert type = %d, want UserCert", cert.CertType)
	}
	// Check principals
	principals := make(map[string]bool)
	for _, p := range cert.ValidPrincipals {
		principals[p] = true
	}
	if !principals["test@example.com"] {
		t.Error("expected user email in principals")
	}
	if !principals["openclaw"] {
		t.Error("expected 'openclaw' in principals")
	}
}

func TestHandleSSHCert_NoCA(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/ssh-cert", nil)
	w := httptest.NewRecorder()
	srv.handleSSHCert(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleSSHCert_MissingPublicKey(t *testing.T) {
	srv := &Server{sshCAPrivateKey: "dummy"}

	body, _ := json.Marshal(sshCertRequest{})
	req := httptest.NewRequest(http.MethodPost, "/ssh-cert", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleSSHCert(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleSSHCert_InvalidPublicKey(t *testing.T) {
	srv := &Server{sshCAPrivateKey: "dummy"}

	body, _ := json.Marshal(sshCertRequest{PublicKey: "not-a-key"})
	req := httptest.NewRequest(http.MethodPost, "/ssh-cert", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleSSHCert(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleSSHCert_MachineNotRunning(t *testing.T) {
	caKey := generateTestCAKey(t)
	clientPubKey := generateTestPublicKey(t)

	testStore := &sshCertTestStore{
		machines: map[string]*store.Machine{
			"m1": {ID: "m1", AccountID: 1, Slug: "my-bot", Status: "stopped"},
		},
	}

	srv := &Server{
		store:           testStore,
		sshCAPrivateKey: caKey,
	}

	body, _ := json.Marshal(sshCertRequest{PublicKey: clientPubKey})
	req := httptest.NewRequest(http.MethodPost, "/ssh-cert", bytes.NewReader(body))

	ctx := auth.WithUser(req.Context(), &auth.Claims{UserID: 1, Email: "test@example.com"})
	ctx = context.WithValue(ctx, accountIDKey, 1)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "m1")
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	srv.handleSSHCert(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleSSHCert_WrongAccount(t *testing.T) {
	caKey := generateTestCAKey(t)
	clientPubKey := generateTestPublicKey(t)

	testStore := &sshCertTestStore{
		machines: map[string]*store.Machine{
			"m1": {ID: "m1", AccountID: 99, Slug: "my-bot", Status: "running"},
		},
	}

	srv := &Server{
		store:           testStore,
		sshCAPrivateKey: caKey,
	}

	body, _ := json.Marshal(sshCertRequest{PublicKey: clientPubKey})
	req := httptest.NewRequest(http.MethodPost, "/ssh-cert", bytes.NewReader(body))

	ctx := auth.WithUser(req.Context(), &auth.Claims{UserID: 1, Email: "test@example.com"})
	ctx = context.WithValue(ctx, accountIDKey, 1) // account 1, but machine belongs to 99
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "m1")
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	srv.handleSSHCert(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}
