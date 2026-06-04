package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

var validProviders = map[string]bool{
	"anthropic":    true,
	"openai":       true,
	"google":       true,
	"openrouter":   true,
	"openai-codex": true,
	"discord":      true,
	"telegram":     true,
	"whatsapp":     true,
	"slack":        true,
	"slack-app":    true,
}

var validCredentialTypes = map[string]bool{
	"api_key":          true,
	"subscription_key": true,
	"token":            true,
	"oauth":            true,
}

// Base URLs for provider APIs (overridden in tests).
var (
	telegramBaseURL = "https://api.telegram.org"
	whatsappBaseURL = "https://graph.facebook.com"
	slackBotBaseURL = "https://slack.com"
)

func (s *Server) shouldPushConfigForCredentialChange(ctx context.Context, provider string) bool {
	return s.isProxiedProvider(ctx, provider)
}

// handleListMachineCredentials returns credentials for a specific machine.
func (s *Server) handleListMachineCredentials(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	creds, err := s.store.ListMachineCredentials(r.Context(), machineID)
	if err != nil {
		slog.Error("credential.list_failed", "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, creds)
}

// handleSetMachineCredential creates or replaces a credential for a provider on a machine.
func (s *Server) handleSetMachineCredential(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	provider := chi.URLParam(r, "provider")
	accountID := accountIDFromContext(r.Context())

	// Check hardcoded list first, then fall back to catalog lookup.
	// Search providers (brave, exa, etc.) are not in the hardcoded list
	// but are valid if they exist in the provider_catalog table.
	if !validProviders[provider] {
		entry, catErr := s.store.GetProviderCatalogEntry(r.Context(), provider)
		switch {
		case errors.Is(catErr, pgx.ErrNoRows), catErr == nil && entry == nil:
			writeError(w, http.StatusBadRequest, "invalid provider")
			return
		case catErr != nil:
			slog.Error("credentials.catalog_lookup_failed", "provider", provider, "error", catErr)
			writeError(w, http.StatusInternalServerError, "provider lookup failed")
			return
		}
	}

	if s.secretKey == "" {
		writeError(w, http.StatusInternalServerError, "SECRET_ENCRYPTION_KEY not configured")
		return
	}

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	var req struct {
		Value          string  `json:"value"`
		CredentialType string  `json:"credential_type"`
		Label          string  `json:"label"`
		RefreshToken   *string `json:"refresh_token,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Strip all whitespace — keys pasted from terminals often contain newlines mid-string.
	req.Value = strings.Join(strings.Fields(req.Value), "")
	if req.Value == "" {
		writeError(w, http.StatusBadRequest, "value is required")
		return
	}
	if req.CredentialType == "" {
		req.CredentialType = "api_key"
	}
	if !validCredentialTypes[req.CredentialType] {
		writeError(w, http.StatusBadRequest, "invalid credential_type")
		return
	}

	// Validate key against provider API (skip for OAuth and providers without validation)
	if req.CredentialType != "oauth" {
		if err := s.validateProviderKey(r.Context(), provider, req.Value); err != nil {
			writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("key validation failed: %v", err))
			return
		}
	}

	// Extract last 4 characters
	var lastFour *string
	if len(req.Value) >= 4 {
		l4 := req.Value[len(req.Value)-4:]
		lastFour = &l4
	}

	encrypted, err := crypto.Encrypt(req.Value, s.secretKey)
	if err != nil {
		slog.Error("credential.encryption_failed", "provider", provider, "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}

	// Encrypt refresh token if provided
	var encryptedRefreshToken *string
	if req.RefreshToken != nil && *req.RefreshToken != "" {
		rt, err := crypto.Encrypt(*req.RefreshToken, s.secretKey)
		if err != nil {
			slog.Error("credential.refresh_token_encryption_failed", "provider", provider, "machine_id", machineID, "error", err)
			writeError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		encryptedRefreshToken = &rt
	}

	cred := &store.Credential{
		AccountID:      accountID,
		MachineID:      machineID,
		Provider:       provider,
		CredentialType: req.CredentialType,
		EncryptedValue: encrypted,
		Label:          req.Label,
		LastFour:       lastFour,
		RefreshToken:   encryptedRefreshToken,
		Status:         "active",
	}

	if err := s.store.SetMachineCredential(r.Context(), machineID, cred); err != nil {
		slog.Error("credential.store_set_failed", "provider", provider, "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	slog.Info("credential.set", "provider", provider, "machine_id", machineID, "credential_id", cred.ID)

	// Push updated credentials to running VM
	go s.pushCredentialsToVMByID(machineID)
	if s.shouldPushConfigForCredentialChange(r.Context(), provider) {
		go s.pushMachineConfigAsync(machineID)
	}

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.model_provider_added",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Added %s credential on '%s'", provider, machine.Name),
		Detail:    map[string]any{"provider": provider, "credential_type": req.CredentialType},
	})

	writeJSON(w, http.StatusOK, cred)
}

// handleDeleteMachineCredential removes a credential for a provider on a machine.
func (s *Server) handleDeleteMachineCredential(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	provider := chi.URLParam(r, "provider")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	if err := s.store.DeleteMachineCredential(r.Context(), machineID, provider); err != nil {
		slog.Error("credential.delete_failed", "machine_id", machineID, "provider", provider, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	slog.Info("credential.deleted", "provider", provider, "machine_id", machineID)

	// Push updated credentials to running VM
	go s.pushCredentialsToVMByID(machineID)
	if s.shouldPushConfigForCredentialChange(r.Context(), provider) {
		go s.pushMachineConfigAsync(machineID)
	}

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.model_provider_removed",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Removed %s credential from '%s'", provider, machine.Name),
		Detail:    map[string]any{"provider": provider},
	})

	w.WriteHeader(http.StatusNoContent)
}

// handleTestMachineCredential validates a stored credential by making a test call to the provider.
// Route: POST /api/accounts/{accountId}/machines/{machineId}/credentials/{provider}/test
func (s *Server) handleTestMachineCredential(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	provider := chi.URLParam(r, "provider")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	cred, err := s.store.GetMachineCredentialWithValue(r.Context(), machineID, provider)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "no credential configured"})
		return
	}

	val, err := crypto.Decrypt(cred.EncryptedValue, s.secretKey)
	if err != nil {
		slog.Error("credential.test.decrypt_failed", "machine_id", machineID, "provider", provider, "error", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "decryption failed"})
		return
	}

	// Validate the key
	if cred.CredentialType == "oauth" && provider == "openai-codex" {
		result := s.validateOpenAICodexCredential(r.Context(), machine)
		if !result.OK {
			resp := map[string]interface{}{"ok": false, "error": result.Error}
			if result.Code != "" {
				resp["code"] = result.Code
			}
			if result.Status != 0 {
				resp["status"] = result.Status
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
	} else {
		if testErr := s.validateProviderKey(r.Context(), provider, val); testErr != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": testErr.Error()})
			return
		}
	}

	result := map[string]interface{}{"ok": true}
	if cred.ExpiresAt != nil {
		hoursLeft := time.Until(*cred.ExpiresAt).Hours()
		result["expires_in_hours"] = int(hoursLeft)
	}
	writeJSON(w, http.StatusOK, result)
}

// codexTestResult holds structured test output for the UI.
type codexTestResult struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Code   string `json:"code,omitempty"`
	Status int    `json:"status,omitempty"`
}

func (s *Server) validateOpenAICodexCredential(ctx context.Context, machine *store.Machine) codexTestResult {
	// Look up a Codex model from the catalog so we don't hardcode model names.
	modelID := "gpt-5.5" // safe default
	catalog, err := s.store.ListModelCatalog(ctx)
	if err == nil {
		for _, m := range catalog {
			if m.Provider == "openai-codex" && m.Enabled {
				bare := m.ID
				if i := strings.Index(bare, "/"); i >= 0 {
					bare = bare[i+1:]
				}
				modelID = bare
				break
			}
		}
	}

	if machine == nil || machine.Status != "running" || machine.HostID == nil ||
		machine.ProxyToken == nil || *machine.ProxyToken == "" || s.agentClient == nil {
		return codexTestResult{Error: "machine must be running to test codex credential"}
	}

	host, err := s.store.GetHost(ctx, *machine.HostID)
	if err != nil {
		return codexTestResult{Error: "host lookup failed: " + err.Error()}
	}

	result, err := s.agentClient.ExecCommand(ctx, host, machine.ID, *machine.ProxyToken, []string{"codex-auth", "test", modelID})
	if err != nil {
		return codexTestResult{Error: "agent exec failed: " + err.Error()}
	}
	if result.ExitCode != 0 {
		msg := strings.TrimSpace(result.Stderr)
		if msg == "" {
			msg = "codex-auth test failed"
		}
		return codexTestResult{Error: msg}
	}

	var parsed codexTestResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &parsed); err != nil {
		return codexTestResult{Error: "decode agent response failed"}
	}
	return parsed
}

// ---- Provider key validation ----

func (s *Server) validateProviderKey(ctx context.Context, provider, key string) error {
	switch provider {
	case "anthropic":
		return validateAnthropicKey(ctx, key)
	case "openai":
		return validateOpenAIKey(ctx, key)
	case "openrouter":
		return validateOpenRouterKey(ctx, key)
	case "google":
		return validateGoogleKey(ctx, key)
	case "discord":
		return validateDiscordKey(ctx, key)
	case "telegram":
		return validateTelegramKey(ctx, key)
	case "whatsapp":
		return validateWhatsAppKey(ctx, key)
	case "slack":
		return validateSlackBotToken(ctx, key)
	case "slack-app":
		return validateSlackAppToken(key)
	default:
		return s.validateFromCatalog(ctx, provider, key)
	}
}

// validateFromCatalog validates a provider key using the catalog's validation spec.
// Returns nil if validation passes or if no validation spec exists for the provider.
func (s *Server) validateFromCatalog(ctx context.Context, provider, key string) error {
	entry, err := s.store.GetProviderCatalogEntry(ctx, provider)
	if err != nil || entry == nil || entry.Validation == nil {
		reason := "no_catalog_entry"
		if entry != nil && entry.Validation == nil {
			reason = "no_validation_spec"
		}
		if err != nil {
			reason = "catalog_lookup_error"
		}
		slog.Debug("provider_validation.skipped", "provider", provider, "reason", reason)
		return nil // no catalog entry or no validation spec — skip
	}

	var spec struct {
		URL          string            `json:"url"`
		Method       string            `json:"method"`
		AuthMethod   string            `json:"auth_method"`
		AuthHeader   string            `json:"auth_header"`
		AuthPrefix   string            `json:"auth_prefix"`
		ExtraHeaders map[string]string `json:"extra_headers"`
		BodyTemplate string            `json:"body_template"`
		ContentType  string            `json:"content_type"`
		Success      []int             `json:"success"`
		Fail         []int             `json:"fail"`
	}
	if err := json.Unmarshal(entry.Validation, &spec); err != nil {
		slog.Warn("provider_catalog.validation_parse_failed", "provider", provider, "error", err)
		return nil // bad spec — don't block the user
	}

	// Substitute {{key}} placeholder in URL and body template.
	validationURL := strings.ReplaceAll(spec.URL, "{{key}}", key)

	// Guard against SSRF: only allow https URLs in validation specs.
	if !strings.HasPrefix(validationURL, "https://") {
		slog.Warn("provider_catalog.validation_blocked_non_https", "provider", provider, "url", spec.URL)
		return nil // non-https URL — skip validation rather than making the request
	}

	var body io.Reader
	if spec.BodyTemplate != "" {
		bodyStr := strings.ReplaceAll(spec.BodyTemplate, "{{key}}", key)
		body = strings.NewReader(bodyStr)
	}

	req, err := http.NewRequestWithContext(ctx, spec.Method, validationURL, body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	// Inject auth
	switch spec.AuthMethod {
	case "header":
		header := spec.AuthHeader
		if header == "" {
			header = "x-api-key"
		}
		req.Header.Set(header, key)
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+key)
	case "query_param":
		param := spec.AuthHeader // reuses auth_header field for query param name
		if param == "" {
			param = "key"
		}
		q := req.URL.Query()
		q.Set(param, key)
		req.URL.RawQuery = q.Encode()
	case "custom":
		// Custom auth: uses auth_prefix + key in the Authorization header.
		// e.g., Discord uses "Bot " prefix → "Authorization: Bot <key>"
		prefix := spec.AuthPrefix
		if prefix == "" {
			prefix = "Bearer "
		}
		req.Header.Set("Authorization", prefix+key)
	}

	if spec.ContentType != "" {
		req.Header.Set("Content-Type", spec.ContentType)
	}
	for k, v := range spec.ExtraHeaders {
		req.Header.Set(k, v)
	}

	// Log spec.URL (pre-substitution) — validationURL may embed the API key
	// when the spec uses {{key}} in the URL (e.g., Telegram).
	slog.Debug("provider_validation.attempting", "provider", provider, "url", spec.URL, "method", spec.Method, "auth_method", spec.AuthMethod)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body) // drain

	slog.Debug("provider_validation.response", "provider", provider, "status", resp.StatusCode)

	for _, code := range spec.Fail {
		if resp.StatusCode == code {
			return fmt.Errorf("invalid API key")
		}
	}
	for _, code := range spec.Success {
		if resp.StatusCode == code {
			return nil
		}
	}

	return fmt.Errorf("unexpected status %d from %s API", resp.StatusCode, provider)
}

func validateAnthropicKey(ctx context.Context, key string) error {
	// OAuth/setup-tokens (sk-ant-oat*) authenticate via Bearer token, not x-api-key.
	if strings.HasPrefix(key, "sk-ant-oat") {
		return validateAnthropicOAuthToken(ctx, key)
	}

	// Use the read-only models endpoint to validate the key without spending tokens.
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body) // drain

	if resp.StatusCode == 401 {
		return fmt.Errorf("invalid API key")
	}
	// 200 = valid key, 429 = rate limited but key is valid
	if resp.StatusCode == 200 || resp.StatusCode == 429 {
		return nil
	}

	return fmt.Errorf("unexpected status %d from Anthropic API", resp.StatusCode)
}

// validateAnthropicOAuthToken validates an OAuth/setup-token by sending it as a Bearer token.
func validateAnthropicOAuthToken(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 200 || resp.StatusCode == 429 {
		return nil
	}

	// Anthropic returns 401 for both "OAuth not supported" (valid token, feature disabled)
	// and truly invalid tokens. Check the body to distinguish.
	bodyStr := string(body)
	if resp.StatusCode == 401 && strings.Contains(bodyStr, "OAuth") {
		return nil
	}

	return fmt.Errorf("invalid setup-token (HTTP %d)", resp.StatusCode)
}

func validateOpenAIKey(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.openai.com/v1/models", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body) // drain

	if resp.StatusCode == 401 {
		return fmt.Errorf("invalid API key")
	}
	if resp.StatusCode == 200 || resp.StatusCode == 429 {
		return nil
	}

	return fmt.Errorf("unexpected status %d from OpenAI API", resp.StatusCode)
}

func validateOpenRouterKey(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body) // drain

	if resp.StatusCode == 401 {
		return fmt.Errorf("invalid API key")
	}
	if resp.StatusCode == 200 || resp.StatusCode == 429 {
		return nil
	}

	return fmt.Errorf("unexpected status %d from OpenRouter API", resp.StatusCode)
}

func validateGoogleKey(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://generativelanguage.googleapis.com/v1beta/models", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("x-goog-api-key", key)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body) // drain

	if resp.StatusCode == 400 || resp.StatusCode == 403 {
		return fmt.Errorf("invalid API key")
	}
	if resp.StatusCode == 200 || resp.StatusCode == 429 {
		return nil
	}

	return fmt.Errorf("unexpected status %d from Google API", resp.StatusCode)
}

func validateDiscordKey(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://discord.com/api/v10/users/@me", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+key)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body) // drain

	if resp.StatusCode == 401 {
		return fmt.Errorf("invalid bot token")
	}
	if resp.StatusCode == 200 || resp.StatusCode == 429 {
		return nil
	}

	return fmt.Errorf("unexpected status %d from Discord API", resp.StatusCode)
}

func validateTelegramKey(ctx context.Context, key string) error {
	url := fmt.Sprintf("%s/bot%s/getMe", telegramBaseURL, key)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body) // drain

	if resp.StatusCode == 401 {
		return fmt.Errorf("invalid bot token")
	}
	if resp.StatusCode == 200 {
		return nil
	}
	if resp.StatusCode == 429 {
		return fmt.Errorf("rate limited by Telegram API, please try again later")
	}

	return fmt.Errorf("unexpected status %d from Telegram API", resp.StatusCode)
}

func validateWhatsAppKey(ctx context.Context, key string) error {
	url := fmt.Sprintf("%s/v18.0/me", whatsappBaseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body) // drain

	if resp.StatusCode == 401 || resp.StatusCode == 400 {
		return fmt.Errorf("invalid access token")
	}
	if resp.StatusCode == 200 || resp.StatusCode == 429 {
		return nil
	}

	return fmt.Errorf("unexpected status %d from WhatsApp API", resp.StatusCode)
}

func validateSlackBotToken(ctx context.Context, key string) error {
	if !strings.HasPrefix(key, "xoxb-") {
		return fmt.Errorf("invalid bot token format: must start with xoxb-")
	}

	url := fmt.Sprintf("%s/api/auth.test", slackBotBaseURL)
	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("invalid response from Slack API")
	}
	if !result.OK {
		return fmt.Errorf("slack API rejected token: %s", result.Error)
	}
	return nil
}

func validateSlackAppToken(key string) error {
	if !strings.HasPrefix(key, "xapp-") {
		return fmt.Errorf("invalid app token format: must start with xapp-")
	}
	return nil
}
