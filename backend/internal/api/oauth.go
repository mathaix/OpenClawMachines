package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

// oauthTokenEndpoints maps provider names to their OAuth2 token endpoints.
var oauthTokenEndpoints = map[string]string{
	"google": "https://oauth2.googleapis.com/token",
	"openai": "https://auth.openai.com/v1/token",
}

// oauthClientIDs maps provider names to their OAuth2 client IDs.
// Providers with PKCE-based public clients use a fixed client_id and no client_secret.
// Currently empty — openai-codex was removed (tokens managed locally on VM).
var oauthClientIDs = map[string]string{}

// oauthTokenResponse represents the JSON response from an OAuth2 token endpoint.
type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// refreshOAuthToken refreshes an OAuth access token using the stored refresh token.
// Returns the new access token, new refresh token (if rotated), new expiry time, and any error.
func refreshOAuthToken(ctx context.Context, cred *store.Credential, secretKey, clientID, clientSecret string) (newAccessToken string, newRefreshToken string, newExpiry time.Time, isPermanentFailure bool, err error) {
	if cred.RefreshToken == nil || *cred.RefreshToken == "" {
		return "", "", time.Time{}, true, fmt.Errorf("credential has no refresh token")
	}

	// Look up the token endpoint for this provider
	tokenEndpoint, ok := oauthTokenEndpoints[cred.Provider]
	if !ok {
		return "", "", time.Time{}, true, fmt.Errorf("no OAuth token endpoint configured for provider %q", cred.Provider)
	}

	// Decrypt the refresh token
	refreshToken, err := crypto.Decrypt(*cred.RefreshToken, secretKey)
	if err != nil {
		return "", "", time.Time{}, true, fmt.Errorf("decrypt refresh token: %w", err)
	}

	// Build the token refresh request.
	// Use provider-specific client_id if available (PKCE public clients use a
	// fixed client_id and no client_secret).
	providerClientID := clientID
	providerClientSecret := clientSecret
	if pid, ok := oauthClientIDs[cred.Provider]; ok {
		providerClientID = pid
		providerClientSecret = "" // PKCE public client — no secret
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	if providerClientID != "" {
		form.Set("client_id", providerClientID)
	}
	if providerClientSecret != "" {
		form.Set("client_secret", providerClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", time.Time{}, false, fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", time.Time{}, false, fmt.Errorf("refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", time.Time{}, false, fmt.Errorf("read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		slog.Warn("oauth.refresh_error_response", "provider", cred.Provider, "status", resp.StatusCode, "body", string(body))
		// Check for permanent failures (invalid_grant = refresh token revoked)
		permanent := strings.Contains(string(body), "invalid_grant")
		return "", "", time.Time{}, permanent, fmt.Errorf("oauth refresh failed for provider %s: status %d", cred.Provider, resp.StatusCode)
	}

	var tokenResp oauthTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", "", time.Time{}, false, fmt.Errorf("parse refresh response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return "", "", time.Time{}, false, fmt.Errorf("refresh response missing access_token")
	}

	// Calculate new expiry time
	expiry := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	// Use the new refresh token if the provider rotated it, otherwise keep the old one
	returnedRefreshToken := refreshToken
	if tokenResp.RefreshToken != "" {
		returnedRefreshToken = tokenResp.RefreshToken
	}

	return tokenResp.AccessToken, returnedRefreshToken, expiry, false, nil
}

// handleAgentStoreOAuthToken handles the agent POST-ing OAuth tokens to the backend.
// The agent completes the OAuth flow on the VM and sends the tokens here for secure storage.
// Route: POST /api/agent/machines/{machineID}/oauth-token
func (s *Server) handleAgentStoreOAuthToken(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	if s.secretKey == "" {
		writeError(w, http.StatusInternalServerError, "SECRET_ENCRYPTION_KEY not configured")
		return
	}

	var req struct {
		Provider     string `json:"provider"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Provider == "" || req.AccessToken == "" {
		writeError(w, http.StatusBadRequest, "provider and access_token are required")
		return
	}

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	// Encrypt access token
	encValue, err := crypto.Encrypt(req.AccessToken, s.secretKey)
	if err != nil {
		slog.Error("oauth_store.encrypt_access_failed", "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}

	// Encrypt refresh token
	var encRefresh *string
	if req.RefreshToken != "" {
		rt, err := crypto.Encrypt(req.RefreshToken, s.secretKey)
		if err != nil {
			slog.Error("oauth_store.encrypt_refresh_failed", "machine_id", machineID, "error", err)
			writeError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		encRefresh = &rt
	}

	expiresAt := time.Now().Add(time.Duration(req.ExpiresIn) * time.Second)

	cred := &store.Credential{
		AccountID:      machine.AccountID,
		MachineID:      machineID,
		Provider:       req.Provider,
		CredentialType: "oauth",
		EncryptedValue: encValue,
		ExpiresAt:      &expiresAt,
		RefreshToken:   encRefresh,
		Status:         "active",
	}

	if err := s.store.SetMachineCredential(r.Context(), machineID, cred); err != nil {
		slog.Error("oauth_store.set_credential_failed", "machine_id", machineID, "provider", req.Provider, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to store credential")
		return
	}

	slog.Info("oauth_store.stored", "machine_id", machineID, "provider", req.Provider, "expires_at", expiresAt)

	// For a running machine, only report OAuth success once the fresh credential
	// is visible to the VM's live metadata/proxy path.
	if err := s.pushCredentialsToVMByIDContext(r.Context(), machineID); err != nil {
		slog.Error("oauth_store.push_credential_failed", "machine_id", machineID, "provider", req.Provider, "error", err)
		writeError(w, http.StatusBadGateway, "credential stored but failed to reach running machine")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":     "stored",
		"expires_at": expiresAt,
	})
}

// handleRefreshCredentialInternal handles the proxy's request to refresh an OAuth credential.
// The proxy calls this when it detects a near-expiry or rejected OAuth token.
// Route: POST /api/agent/machines/{machineID}/refresh-credential
func (s *Server) handleRefreshCredentialInternal(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	if s.secretKey == "" {
		writeError(w, http.StatusInternalServerError, "SECRET_ENCRYPTION_KEY not configured")
		return
	}

	var req struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Provider == "" {
		writeError(w, http.StatusBadRequest, "provider is required")
		return
	}

	// Load the credential with encrypted values
	cred, err := s.store.GetMachineCredentialWithValue(r.Context(), machineID, req.Provider)
	if err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	if cred.CredentialType != "oauth" {
		writeError(w, http.StatusBadRequest, "credential is not an OAuth credential")
		return
	}

	// Attempt refresh
	newAccess, newRefresh, newExpiry, permanent, err := refreshOAuthToken(r.Context(), cred, s.secretKey, s.oauthClientID, s.oauthClientSecret)
	if err != nil {
		if permanent {
			// Mark credential as reauth_required
			detail := fmt.Sprintf("refresh token revoked: %v", err)
			if updateErr := s.store.UpdateCredentialStatus(r.Context(), machineID, req.Provider, "reauth_required", &detail); updateErr != nil {
				slog.Error("refresh_credential.status_update_failed", "machine_id", machineID, "provider", req.Provider, "error", updateErr)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error":     "oauth_reauth_required",
				"permanent": true,
				"detail":    err.Error(),
			})
			return
		}
		// Transient failure
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":     "oauth_refresh_failed",
			"permanent": false,
			"detail":    err.Error(),
		})
		return
	}

	// Encrypt new tokens and update DB
	encAccess, err := crypto.Encrypt(newAccess, s.secretKey)
	if err != nil {
		slog.Error("refresh_credential.encrypt_access", "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}
	encRefresh, err := crypto.Encrypt(newRefresh, s.secretKey)
	if err != nil {
		slog.Error("refresh_credential.encrypt_refresh", "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}
	if err := s.store.UpdateCredentialTokens(r.Context(), cred.ID, encAccess, encRefresh, newExpiry); err != nil {
		slog.Error("refresh_credential.db_update", "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update credential")
		return
	}

	slog.Info("refresh_credential.refreshed", "machine_id", machineID, "provider", req.Provider, "new_expiry", newExpiry)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"access_token":    newAccess,
		"credential_type": "oauth",
		"expires_at":      newExpiry,
	})
}
