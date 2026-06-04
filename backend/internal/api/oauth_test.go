package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

// secretKey is a 32-byte key for test encryption/decryption.
const testSecretKey = "01234567890123456789012345678901"

type mockOAuthStore struct {
	store.Store

	machine          *store.Machine
	storedCredential *store.Credential
}

func (m *mockOAuthStore) GetMachine(_ context.Context, machineID string) (*store.Machine, error) {
	if m.machine != nil && m.machine.ID == machineID {
		return m.machine, nil
	}
	return nil, fmt.Errorf("machine %s not found", machineID)
}

func (m *mockOAuthStore) SetMachineCredential(_ context.Context, _ string, cred *store.Credential) error {
	clone := *cred
	m.storedCredential = &clone
	return nil
}

func TestRefreshOAuthToken_NearExpiry(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Errorf("expected grant_type=refresh_token, got %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "my-refresh-token" {
			t.Errorf("expected refresh_token=my-refresh-token, got %q", r.Form.Get("refresh_token"))
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(oauthTokenResponse{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			ExpiresIn:    3600,
			TokenType:    "Bearer",
		})
	}))
	defer ts.Close()

	old := oauthTokenEndpoints["google"]
	oauthTokenEndpoints["google"] = ts.URL
	defer func() { oauthTokenEndpoints["google"] = old }()

	encRefresh, err := crypto.Encrypt("my-refresh-token", testSecretKey)
	if err != nil {
		t.Fatalf("encrypt refresh token: %v", err)
	}
	nearExpiry := time.Now().Add(2 * time.Minute)
	cred := &store.Credential{
		Provider:       "google",
		CredentialType: "oauth",
		RefreshToken:   &encRefresh,
		ExpiresAt:      &nearExpiry,
	}

	newAccess, newRefresh, newExpiry, permanent, err := refreshOAuthToken(context.Background(), cred, testSecretKey, "test-client-id", "test-client-secret")
	if err != nil {
		t.Fatalf("refreshOAuthToken: %v", err)
	}
	if permanent {
		t.Error("expected permanent=false")
	}
	if newAccess != "new-access-token" {
		t.Errorf("access token = %q, want new-access-token", newAccess)
	}
	if newRefresh != "new-refresh-token" {
		t.Errorf("refresh token = %q, want new-refresh-token", newRefresh)
	}
	if newExpiry.Before(time.Now().Add(59 * time.Minute)) {
		t.Errorf("expiry %v should be ~1h from now", newExpiry)
	}
}

func TestRefreshOAuthToken_NoRefreshToken(t *testing.T) {
	nearExpiry := time.Now().Add(2 * time.Minute)
	cred := &store.Credential{
		Provider:       "google",
		CredentialType: "oauth",
		RefreshToken:   nil,
		ExpiresAt:      &nearExpiry,
	}

	_, _, _, permanent, err := refreshOAuthToken(context.Background(), cred, testSecretKey, "", "")
	if err == nil {
		t.Fatal("expected error for missing refresh token")
	}
	if !permanent {
		t.Error("expected permanent=true for missing refresh token")
	}
}

func TestRefreshOAuthToken_UnknownProvider(t *testing.T) {
	encRefresh, _ := crypto.Encrypt("tok", testSecretKey)
	nearExpiry := time.Now().Add(2 * time.Minute)
	cred := &store.Credential{
		Provider:       "unknown-provider",
		CredentialType: "oauth",
		RefreshToken:   &encRefresh,
		ExpiresAt:      &nearExpiry,
	}

	_, _, _, permanent, err := refreshOAuthToken(context.Background(), cred, testSecretKey, "", "")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !permanent {
		t.Error("expected permanent=true for unknown provider")
	}
}

func TestRefreshOAuthToken_EndpointReturnsInvalidGrant(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer ts.Close()

	old := oauthTokenEndpoints["google"]
	oauthTokenEndpoints["google"] = ts.URL
	defer func() { oauthTokenEndpoints["google"] = old }()

	encRefresh, _ := crypto.Encrypt("bad-token", testSecretKey)
	nearExpiry := time.Now().Add(2 * time.Minute)
	cred := &store.Credential{
		Provider:       "google",
		CredentialType: "oauth",
		RefreshToken:   &encRefresh,
		ExpiresAt:      &nearExpiry,
	}

	_, _, _, permanent, err := refreshOAuthToken(context.Background(), cred, testSecretKey, "", "")
	if err == nil {
		t.Fatal("expected error for failed refresh")
	}
	if !permanent {
		t.Error("expected permanent=true for invalid_grant")
	}
}

func TestRefreshOAuthToken_KeepsOldRefreshToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(oauthTokenResponse{
			AccessToken: "new-access",
			ExpiresIn:   3600,
			TokenType:   "Bearer",
		})
	}))
	defer ts.Close()

	old := oauthTokenEndpoints["google"]
	oauthTokenEndpoints["google"] = ts.URL
	defer func() { oauthTokenEndpoints["google"] = old }()

	encRefresh, _ := crypto.Encrypt("original-refresh", testSecretKey)
	nearExpiry := time.Now().Add(2 * time.Minute)
	cred := &store.Credential{
		Provider:       "google",
		CredentialType: "oauth",
		RefreshToken:   &encRefresh,
		ExpiresAt:      &nearExpiry,
	}

	_, newRefresh, _, _, err := refreshOAuthToken(context.Background(), cred, testSecretKey, "", "")
	if err != nil {
		t.Fatalf("refreshOAuthToken: %v", err)
	}
	if newRefresh != "original-refresh" {
		t.Errorf("refresh token = %q, want original-refresh (should keep old when not rotated)", newRefresh)
	}
}

func TestHandleAgentStoreOAuthToken_OpenAICodexStoresWithoutLiveValidation(t *testing.T) {
	mockStore := &mockOAuthStore{
		machine: &store.Machine{
			ID:        "m-codex",
			AccountID: 42,
			Status:    "stopped",
		},
	}
	srv := &Server{
		store:     mockStore,
		secretKey: testSecretKey,
	}

	req := httptest.NewRequest(http.MethodPost, "/api/agent/machines/m-codex/oauth-token", strings.NewReader(`{
		"provider": "openai-codex",
		"access_token": "access-token",
		"refresh_token": "refresh-token",
		"expires_in": 3600
	}`))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("machineID", "m-codex")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	rr := httptest.NewRecorder()
	http.HandlerFunc(srv.handleAgentStoreOAuthToken).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d. body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if mockStore.storedCredential == nil {
		t.Fatal("expected credential to be stored")
	}
	if mockStore.storedCredential.Provider != "openai-codex" {
		t.Fatalf("provider = %q, want openai-codex", mockStore.storedCredential.Provider)
	}
	if mockStore.storedCredential.CredentialType != "oauth" {
		t.Fatalf("credential_type = %q, want oauth", mockStore.storedCredential.CredentialType)
	}

	accessToken, err := crypto.Decrypt(mockStore.storedCredential.EncryptedValue, testSecretKey)
	if err != nil {
		t.Fatalf("decrypt access token: %v", err)
	}
	if accessToken != "access-token" {
		t.Fatalf("access token = %q, want access-token", accessToken)
	}

	if mockStore.storedCredential.RefreshToken == nil {
		t.Fatal("expected encrypted refresh token")
	}
	refreshToken, err := crypto.Decrypt(*mockStore.storedCredential.RefreshToken, testSecretKey)
	if err != nil {
		t.Fatalf("decrypt refresh token: %v", err)
	}
	if refreshToken != "refresh-token" {
		t.Fatalf("refresh token = %q, want refresh-token", refreshToken)
	}
}
