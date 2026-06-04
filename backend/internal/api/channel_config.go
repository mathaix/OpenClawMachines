package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
	"github.com/mathaix/openclawmachines/backend/internal/configassembly"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

// handleChannelConnect implements the channel.connect() state transition.
// It saves the credential, enables the capability, saves overrides, builds
// the merged channel config, and pushes a single "set channels.<name>" op to the VM.
func (s *Server) handleChannelConnect(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	channelID := chi.URLParam(r, "channel")
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
	machineKind := store.NormalizeMachineKind(machine.Kind)
	if !requireMutableMachineConfig(w, machine) {
		return
	}

	var req struct {
		Token    string                 `json:"token"`
		AppToken string                 `json:"app_token,omitempty"`
		Settings map[string]interface{} `json:"settings,omitempty"` // dmPolicy, groups, etc.
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	if !validateHermesChannelSettings(w, machineKind, channelID, req.Settings) {
		return
	}

	// Validate the token against the provider API
	if err := s.validateProviderKey(r.Context(), channelID, req.Token); err != nil {
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("token validation failed: %v", err))
		return
	}

	// Require and validate app token for channels that need it (e.g. Slack Socket Mode)
	if _, ok := configassembly.ChannelTokenFields[channelID]; ok && len(configassembly.ChannelTokenFields[channelID]) > 1 && req.AppToken == "" {
		writeError(w, http.StatusBadRequest, "app_token is required for "+channelID)
		return
	}
	if req.AppToken != "" {
		if err := s.validateProviderKey(r.Context(), channelID+"-app", req.AppToken); err != nil {
			writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("app token validation failed: %v", err))
			return
		}
	}

	if s.secretKey == "" {
		writeError(w, http.StatusInternalServerError, "SECRET_ENCRYPTION_KEY not configured")
		return
	}

	// 1. Save credential
	encrypted, err := crypto.Encrypt(req.Token, s.secretKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}
	var lastFour *string
	if len(req.Token) >= 4 {
		l4 := req.Token[len(req.Token)-4:]
		lastFour = &l4
	}
	credType := "api_key"
	cred := &store.Credential{
		AccountID:      machine.AccountID,
		MachineID:      machineID,
		Provider:       channelID,
		EncryptedValue: encrypted,
		CredentialType: credType,
		Label:          channelID + " bot",
		LastFour:       lastFour,
	}
	if err := s.store.SetMachineCredential(r.Context(), machineID, cred); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save credential")
		return
	}

	// Save app token as separate credential (Slack dual-token)
	if req.AppToken != "" {
		appProvider := channelID + "-app"
		appEncrypted, err := crypto.Encrypt(req.AppToken, s.secretKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "app token encryption failed")
			return
		}
		var appLastFour *string
		if len(req.AppToken) >= 4 {
			l4 := req.AppToken[len(req.AppToken)-4:]
			appLastFour = &l4
		}
		appCred := &store.Credential{
			AccountID:      machine.AccountID,
			MachineID:      machineID,
			Provider:       appProvider,
			EncryptedValue: appEncrypted,
			CredentialType: "token",
			Label:          channelID + " app token",
			LastFour:       appLastFour,
		}
		if err := s.store.SetMachineCredential(r.Context(), machineID, appCred); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save app token credential")
			return
		}
	}

	// 2. Enable capability
	if err := s.store.EnableMachineCapability(r.Context(), machineID, channelID, nil); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enable capability")
		return
	}

	// 3. Save settings overrides if provided
	if len(req.Settings) > 0 {
		// Wrap settings under channels.<channelID> for the override format
		overrides := map[string]interface{}{
			"channels": map[string]interface{}{
				channelID: req.Settings,
			},
		}
		overridesJSON, err := json.Marshal(overrides)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to marshal overrides")
			return
		}
		if err := s.store.UpdateMachineCapabilityOverrides(r.Context(), machineID, channelID, overridesJSON); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save overrides")
			return
		}
	}

	if machineKind == store.MachineKindHermes {
		result, err := s.pushHermesConfig(r.Context(), machine, true)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update Hermes config: %v", err))
			return
		}
		slog.Info("channel.connected.hermes", "machine_id", machineID, "channel", channelID, "live_update", result.LiveUpdate, "live_update_error", result.LiveUpdateError)
		resp := map[string]interface{}{
			"status":      "connected",
			"channel":     channelID,
			"live_update": result.LiveUpdate,
		}
		if result.LiveUpdateError != "" {
			resp["live_update_error"] = result.LiveUpdateError
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// 4. Build merged channel config (with plaintext tokens) and push to VM
	tokens := map[string]string{channelID: req.Token}
	if req.AppToken != "" {
		tokens[channelID+"-app"] = req.AppToken
	}
	channelConfig, err := s.buildChannelConfig(r.Context(), machineID, channelID, tokens)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to build channel config: %v", err))
		return
	}

	ops := buildChannelSetOps(channelID, channelConfig)

	// Channels are gateway plugins since OpenClaw 2026.3.28. Enable the plugin
	// entry and add to plugins.allow so the gateway loads the channel.
	ops = append(ops, agentclient.ConfigOp{
		Op:         "set",
		Path:       "plugins.entries." + channelID + ".enabled",
		Value:      "true",
		StrictJSON: true,
	})
	allowOps, err := s.pluginsAllowOps(r.Context(), machineID, channelID, "add")
	if err != nil {
		slog.Error("channel.connect.plugins_allow_db_failed", "machine_id", machineID, "error", err)
	} else {
		ops = append(ops, allowOps...)
	}

	liveUpdate := "not_running"
	if machine.Status == "running" && machine.HostID != nil && s.agentClient != nil {
		if err := s.pushChannelOps(r.Context(), machine, ops); err != nil {
			slog.Error("channel.connect.push_failed", "machine_id", machineID, "channel", channelID, "error", err)
			liveUpdate = "failed"
		} else {
			if err := s.restartGateway(r.Context(), machine); err != nil {
				slog.Warn("channel.connect.restart_gateway_failed", "machine_id", machineID, "error", err)
			}
			liveUpdate = "sent"
		}
	}

	// Patch assembled config in DB with the same ops (same plaintext config)
	if err := s.patchAssembledConfig(r.Context(), machineID, ops); err != nil {
		slog.Warn("channel.connect.patch_assembled_failed", "machine_id", machineID, "error", err)
	}

	slog.Info("channel.connected", "machine_id", machineID, "channel", channelID, "live_update", liveUpdate)

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.channel_added",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Connected %s channel on '%s'", channelID, machine.Name),
		Detail:    map[string]any{"channel": channelID, "live_update": liveUpdate},
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "connected",
		"channel":     channelID,
		"live_update": liveUpdate,
	})
}

// handleChannelDisconnect implements the channel.disconnect() state transition.
func (s *Server) handleChannelDisconnect(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	channelID := chi.URLParam(r, "channel")
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
	machineKind := store.NormalizeMachineKind(machine.Kind)
	if !requireMutableMachineConfig(w, machine) {
		return
	}

	// 1. Disable capability
	if err := s.store.DisableMachineCapability(r.Context(), machineID, channelID); err != nil {
		slog.Warn("channel.disconnect.disable_cap_failed", "machine_id", machineID, "channel", channelID, "error", err)
	}

	// 2. Delete credential
	if err := s.store.DeleteMachineCredential(r.Context(), machineID, channelID); err != nil {
		slog.Warn("channel.disconnect.delete_cred_failed", "machine_id", machineID, "channel", channelID, "error", err)
	}

	// Delete companion credential (e.g. slack-app for slack)
	appProvider := channelID + "-app"
	if _, _, ok := configassembly.ProviderToChannel(appProvider); ok {
		if err := s.store.DeleteMachineCredential(r.Context(), machineID, appProvider); err != nil {
			slog.Warn("channel.disconnect.delete_app_cred_failed", "machine_id", machineID, "channel", channelID, "provider", appProvider, "error", err)
		}
	}

	if machineKind == store.MachineKindHermes {
		result, err := s.pushHermesConfig(r.Context(), machine, true)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update Hermes config: %v", err))
			return
		}
		slog.Info("channel.disconnected.hermes", "machine_id", machineID, "channel", channelID, "live_update", result.LiveUpdate, "live_update_error", result.LiveUpdateError)
		resp := map[string]interface{}{
			"status":      "disconnected",
			"channel":     channelID,
			"live_update": result.LiveUpdate,
		}
		if result.LiveUpdateError != "" {
			resp["live_update_error"] = result.LiveUpdateError
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// 3. Push unset to VM — remove channel config and plugin entry.
	ops := []agentclient.ConfigOp{
		{Op: "unset", Path: "channels." + channelID},
		{Op: "unset", Path: "plugins.entries." + channelID},
	}
	allowOps, err := s.pluginsAllowOps(r.Context(), machineID, channelID, "remove")
	if err != nil {
		slog.Error("channel.disconnect.plugins_allow_failed", "machine_id", machineID, "error", err)
	} else {
		ops = append(ops, allowOps...)
	}

	liveUpdate := "not_running"
	if machine.Status == "running" && machine.HostID != nil && s.agentClient != nil {
		if err := s.pushChannelOps(r.Context(), machine, ops); err != nil {
			slog.Error("channel.disconnect.push_failed", "machine_id", machineID, "channel", channelID, "error", err)
			liveUpdate = "failed"
		} else {
			// Restart gateway — channel changes require restart.
			if err := s.restartGateway(r.Context(), machine); err != nil {
				slog.Warn("channel.disconnect.restart_gateway_failed", "machine_id", machineID, "error", err)
			}
			liveUpdate = "sent"
		}
	}

	// 4. Patch assembled config in DB
	if err := s.patchAssembledConfig(r.Context(), machineID, ops); err != nil {
		slog.Warn("channel.disconnect.patch_assembled_failed", "machine_id", machineID, "error", err)
	}

	slog.Info("channel.disconnected", "machine_id", machineID, "channel", channelID, "live_update", liveUpdate)

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.channel_removed",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Disconnected %s channel from '%s'", channelID, machine.Name),
		Detail:    map[string]any{"channel": channelID},
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "disconnected",
		"channel":     channelID,
		"live_update": liveUpdate,
	})
}

// handleChannelSettings implements the channel.updateSettings() state transition.
func (s *Server) handleChannelSettings(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	channelID := chi.URLParam(r, "channel")
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
	machineKind := store.NormalizeMachineKind(machine.Kind)
	if !requireMutableMachineConfig(w, machine) {
		return
	}

	var req struct {
		Settings map[string]interface{} `json:"settings"` // dmPolicy, groups, etc.
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validateHermesChannelSettings(w, machineKind, channelID, req.Settings) {
		return
	}

	// 1. Save overrides
	overrides := map[string]interface{}{
		"channels": map[string]interface{}{
			channelID: req.Settings,
		},
	}
	overridesJSON, err := json.Marshal(overrides)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to marshal overrides")
		return
	}
	if err := s.store.UpdateMachineCapabilityOverrides(r.Context(), machineID, channelID, overridesJSON); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save overrides")
		return
	}

	if machineKind == store.MachineKindHermes {
		result, err := s.pushHermesConfig(r.Context(), machine, true)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update Hermes config: %v", err))
			return
		}
		slog.Info("channel.settings.hermes.updated", "machine_id", machineID, "channel", channelID, "live_update", result.LiveUpdate, "live_update_error", result.LiveUpdateError)
		resp := map[string]interface{}{
			"status":      "updated",
			"channel":     channelID,
			"live_update": result.LiveUpdate,
		}
		if result.LiveUpdateError != "" {
			resp["live_update_error"] = result.LiveUpdateError
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// 2. Rebuild merged config with existing tokens
	tokens, err := s.decryptChannelTokens(r.Context(), machineID, channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load channel tokens")
		return
	}

	channelConfig, err := s.buildChannelConfig(r.Context(), machineID, channelID, tokens)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to build channel config: %v", err))
		return
	}

	ops := buildChannelSetOps(channelID, channelConfig)

	liveUpdate := "not_running"
	if machine.Status == "running" && machine.HostID != nil && s.agentClient != nil {
		if err := s.pushChannelOps(r.Context(), machine, ops); err != nil {
			slog.Error("channel.settings.push_failed", "machine_id", machineID, "channel", channelID, "error", err)
			liveUpdate = "failed"
		} else {
			liveUpdate = "sent"
		}
	}

	// 3. Patch assembled config
	if err := s.patchAssembledConfig(r.Context(), machineID, ops); err != nil {
		slog.Warn("channel.settings.patch_assembled_failed", "machine_id", machineID, "error", err)
	}

	slog.Info("channel.settings.updated", "machine_id", machineID, "channel", channelID, "live_update", liveUpdate)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "updated",
		"channel":     channelID,
		"live_update": liveUpdate,
	})
}

func validateHermesChannelSettings(w http.ResponseWriter, machineKind, channelID string, settings map[string]interface{}) bool {
	if machineKind != store.MachineKindHermes || channelID != "telegram" {
		return true
	}
	allowedUsers := configassembly.HermesTelegramAllowedUsers(settings)
	if allowedUsers == "" {
		writeError(w, http.StatusBadRequest, "allowed Telegram user IDs are required for Hermes Telegram")
		return false
	}
	for _, userID := range strings.Split(allowedUsers, ",") {
		userID = strings.TrimSpace(userID)
		if userID == "" || !isTelegramNumericID(userID) {
			writeError(w, http.StatusBadRequest, "allowed Telegram user IDs must be comma-separated numeric IDs")
			return false
		}
	}
	return true
}

func isTelegramNumericID(value string) bool {
	if value == "" {
		return false
	}
	if value[0] == '-' {
		value = value[1:]
	}
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// handleChannelUpdateToken implements the channel.updateToken() state transition.
func (s *Server) handleChannelUpdateToken(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	channelID := chi.URLParam(r, "channel")
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
	machineKind := store.NormalizeMachineKind(machine.Kind)
	if !requireMutableMachineConfig(w, machine) {
		return
	}

	var req struct {
		Token    string `json:"token"`
		AppToken string `json:"app_token,omitempty"`
		Label    string `json:"label,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}

	// Validate all tokens before saving any credentials
	if err := s.validateProviderKey(r.Context(), channelID, req.Token); err != nil {
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("token validation failed: %v", err))
		return
	}
	// Require app token for multi-token channels (e.g. Slack Socket Mode).
	// Without it, buildChannelConfig injects a secret ref with no backing
	// credential, breaking the gateway on restart.
	if fields, ok := configassembly.ChannelTokenFields[channelID]; ok && len(fields) > 1 && req.AppToken == "" {
		writeError(w, http.StatusBadRequest, "app_token is required for "+channelID)
		return
	}
	if req.AppToken != "" {
		if err := s.validateProviderKey(r.Context(), channelID+"-app", req.AppToken); err != nil {
			writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("app token validation failed: %v", err))
			return
		}
	}

	if s.secretKey == "" {
		writeError(w, http.StatusInternalServerError, "SECRET_ENCRYPTION_KEY not configured")
		return
	}

	// 1. Save new credential
	encrypted, err := crypto.Encrypt(req.Token, s.secretKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}
	var lastFour *string
	if len(req.Token) >= 4 {
		l4 := req.Token[len(req.Token)-4:]
		lastFour = &l4
	}
	label := req.Label
	if label == "" {
		label = channelID + " bot"
	}
	cred := &store.Credential{
		AccountID:      machine.AccountID,
		MachineID:      machineID,
		Provider:       channelID,
		EncryptedValue: encrypted,
		CredentialType: "api_key",
		Label:          label,
		LastFour:       lastFour,
	}
	if err := s.store.SetMachineCredential(r.Context(), machineID, cred); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save credential")
		return
	}

	// Update app token if provided (already validated above)
	if req.AppToken != "" {
		appEncrypted, err := crypto.Encrypt(req.AppToken, s.secretKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "app token encryption failed")
			return
		}
		var appLastFour *string
		if len(req.AppToken) >= 4 {
			l4 := req.AppToken[len(req.AppToken)-4:]
			appLastFour = &l4
		}
		appCred := &store.Credential{
			AccountID:      machine.AccountID,
			MachineID:      machineID,
			Provider:       channelID + "-app",
			EncryptedValue: appEncrypted,
			CredentialType: "token",
			Label:          channelID + " app token",
			LastFour:       appLastFour,
		}
		if err := s.store.SetMachineCredential(r.Context(), machineID, appCred); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save app token credential")
			return
		}
	}

	if machineKind == store.MachineKindHermes {
		result, err := s.pushHermesConfig(r.Context(), machine, true)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update Hermes config: %v", err))
			return
		}
		slog.Info("channel.token.hermes.updated", "machine_id", machineID, "channel", channelID, "live_update", result.LiveUpdate, "live_update_error", result.LiveUpdateError)
		resp := map[string]interface{}{
			"status":      "updated",
			"channel":     channelID,
			"live_update": result.LiveUpdate,
		}
		if result.LiveUpdateError != "" {
			resp["live_update_error"] = result.LiveUpdateError
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// 2. Rebuild merged config with new tokens
	tokens := map[string]string{channelID: req.Token}
	if req.AppToken != "" {
		tokens[channelID+"-app"] = req.AppToken
	}
	channelConfig, err := s.buildChannelConfig(r.Context(), machineID, channelID, tokens)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to build channel config: %v", err))
		return
	}

	ops := buildChannelSetOps(channelID, channelConfig)

	liveUpdate := "not_running"
	if machine.Status == "running" && machine.HostID != nil && s.agentClient != nil {
		if err := s.pushChannelOps(r.Context(), machine, ops); err != nil {
			slog.Error("channel.token.push_failed", "machine_id", machineID, "channel", channelID, "error", err)
			liveUpdate = "failed"
		} else {
			// Restart gateway to pick up new token.
			// The metadata server will pull the updated token from the backend on demand.
			if err := s.restartGateway(r.Context(), machine); err != nil {
				slog.Warn("channel.token.restart_gateway_failed", "machine_id", machineID, "error", err)
			}
			liveUpdate = "sent"
		}
	}

	// 3. Patch assembled config
	if err := s.patchAssembledConfig(r.Context(), machineID, ops); err != nil {
		slog.Warn("channel.token.patch_assembled_failed", "machine_id", machineID, "error", err)
	}

	slog.Info("channel.token.updated", "machine_id", machineID, "channel", channelID, "live_update", liveUpdate)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "updated",
		"channel":     channelID,
		"live_update": liveUpdate,
	})
}

// buildChannelConfig builds the merged config for a single channel by loading
// the registry template, deep-merging overrides, and injecting tokens as plaintext.
// tokens maps credential provider names to their plaintext values (e.g. "slack" -> "xoxb-...", "slack-app" -> "xapp-...").
// Returns the config for that channel (NOT wrapped in channels.<name>).
func (s *Server) buildChannelConfig(ctx context.Context, machineID, channelID string, tokens map[string]string) (map[string]interface{}, error) {
	// Load registry entry for config template
	entry, err := s.store.GetRegistryEntry(ctx, channelID)
	if err != nil {
		return nil, fmt.Errorf("registry entry %s not found: %w", channelID, err)
	}

	// Parse config template and extract the channel-specific part
	var fullTemplate map[string]interface{}
	if entry.ConfigTemplate != nil {
		if err := json.Unmarshal(entry.ConfigTemplate, &fullTemplate); err != nil {
			return nil, fmt.Errorf("parse config template: %w", err)
		}
	}

	// Extract channels.<channelID> from template
	channelConfig := map[string]interface{}{}
	if channels, ok := fullTemplate["channels"].(map[string]interface{}); ok {
		if ch, ok := channels[channelID].(map[string]interface{}); ok {
			channelConfig = deepCopyMap(ch)
		}
	}

	// Load and merge capability overrides
	caps, err := s.store.ListMachineCapabilities(ctx, machineID)
	if err == nil {
		for _, cap := range caps {
			if cap.EntryID != channelID || cap.ConfigOverrides == nil {
				continue
			}
			var overrides map[string]interface{}
			if err := json.Unmarshal(cap.ConfigOverrides, &overrides); err != nil {
				continue
			}
			// Extract channels.<channelID> from overrides
			if channels, ok := overrides["channels"].(map[string]interface{}); ok {
				if chOverrides, ok := channels[channelID].(map[string]interface{}); ok {
					channelConfig = deepMergeMap(channelConfig, chOverrides)
				}
			}
		}
	}

	// Inject all tokens as plaintext
	if fields, ok := configassembly.ChannelTokenFields[channelID]; ok {
		for _, field := range fields {
			if tok, exists := tokens[field.Provider]; exists && tok != "" {
				channelConfig[field.FieldName] = tok
			}
		}
	}

	// Slack Socket Mode requires mode: "socket" — ensure it's always set
	// when both token fields (botToken + appToken) are present.
	if channelID == "slack" {
		if _, hasMode := channelConfig["mode"]; !hasMode {
			channelConfig["mode"] = "socket"
		}
	}

	return channelConfig, nil
}

// decryptChannelTokens loads and decrypts all credentials for a channel.
// Returns a map of provider name -> plaintext value.
func (s *Server) decryptChannelTokens(ctx context.Context, machineID, channelID string) (map[string]string, error) {
	if s.secretKey == "" {
		return nil, fmt.Errorf("SECRET_ENCRYPTION_KEY not configured")
	}
	fields, ok := configassembly.ChannelTokenFields[channelID]
	if !ok {
		return nil, fmt.Errorf("unknown channel %s", channelID)
	}
	providers := make(map[string]bool, len(fields))
	for _, f := range fields {
		providers[f.Provider] = true
	}

	creds, err := s.store.ListMachineCredentialsWithValues(ctx, machineID)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}

	tokens := make(map[string]string)
	for _, c := range creds {
		if !providers[c.Provider] {
			continue
		}
		val, err := crypto.Decrypt(c.EncryptedValue, s.secretKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt %s credential: %w", c.Provider, err)
		}
		tokens[c.Provider] = val
	}
	return tokens, nil
}

// pushChannelOps sends config ops to a running VM.
func (s *Server) pushChannelOps(ctx context.Context, machine *store.Machine, ops []agentclient.ConfigOp) error {
	host, err := s.store.GetHost(ctx, *machine.HostID)
	if err != nil {
		return fmt.Errorf("get host: %w", err)
	}
	if machine.ProxyToken == nil {
		return fmt.Errorf("no proxy token")
	}
	errs := s.agentClient.ConfigBatch(ctx, host, machine.ID, *machine.ProxyToken, ops)
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// restartGateway triggers a gateway restart on a running VM.
func (s *Server) restartGateway(ctx context.Context, machine *store.Machine) error {
	host, err := s.store.GetHost(ctx, *machine.HostID)
	if err != nil {
		return fmt.Errorf("get host: %w", err)
	}
	if machine.ProxyToken == nil {
		return fmt.Errorf("no proxy token")
	}
	return s.agentClient.RestartGateway(ctx, host, machine.ID, *machine.ProxyToken)
}

// patchAssembledConfig applies config ops to the stored assembled config in DB.
// This keeps the DB in sync with the on-disk openclaw.json for seed consistency.
func (s *Server) patchAssembledConfig(ctx context.Context, machineID string, ops []agentclient.ConfigOp) error {
	record, err := s.store.GetMachineConfig(ctx, machineID)
	if err != nil || record == nil {
		return fmt.Errorf("get machine config: %w", err)
	}

	var config map[string]interface{}
	if record.AssembledConfig != nil {
		if err := json.Unmarshal(record.AssembledConfig, &config); err != nil {
			config = map[string]interface{}{}
		}
	} else {
		config = map[string]interface{}{}
	}

	// Apply ops
	for _, op := range ops {
		switch op.Op {
		case "set":
			if err := setNestedValue(config, op.Path, op.Value, op.StrictJSON); err != nil {
				slog.Warn("patch_assembled.set_failed", "path", op.Path, "error", err)
			}
		case "unset":
			unsetNestedValue(config, op.Path)
		}
	}

	updated, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal patched config: %w", err)
	}

	return s.store.SetMachineAssembledConfig(ctx, machineID, json.RawMessage(updated), record.ConfigVersion+1)
}

// pluginsAllowOps returns config ops to add or remove an entry from the
// plugins.allow array. It reads the current array from the DB assembled config,
// applies the change, and returns a "set plugins.allow" op with the full array.
// This is needed because the config CLI only supports set/unset, not array append.
// Returns nil ops and an error if the DB config cannot be read — callers must
// not push a partial/empty allow list that would drop existing plugins.
func (s *Server) pluginsAllowOps(ctx context.Context, machineID, pluginID, action string) ([]agentclient.ConfigOp, error) {
	var allow []string

	record, err := s.store.GetMachineConfig(ctx, machineID)
	if err != nil {
		return nil, fmt.Errorf("read machine config for plugins.allow: %w", err)
	}
	if record != nil && record.AssembledConfig != nil {
		var config map[string]interface{}
		if err := json.Unmarshal(record.AssembledConfig, &config); err != nil {
			return nil, fmt.Errorf("parse assembled config for plugins.allow: %w", err)
		}
		if plugins, ok := config["plugins"].(map[string]interface{}); ok {
			if arr, ok := plugins["allow"].([]interface{}); ok {
				for _, v := range arr {
					if s, ok := v.(string); ok {
						allow = append(allow, s)
					}
				}
			}
		}
	}

	switch action {
	case "add":
		found := false
		for _, v := range allow {
			if v == pluginID {
				found = true
				break
			}
		}
		if !found {
			allow = append(allow, pluginID)
		}
	case "remove":
		filtered := allow[:0]
		for _, v := range allow {
			if v != pluginID {
				filtered = append(filtered, v)
			}
		}
		allow = filtered
	}

	allowJSON, _ := json.Marshal(allow)
	return []agentclient.ConfigOp{{
		Op:         "set",
		Path:       "plugins.allow",
		Value:      string(allowJSON),
		StrictJSON: true,
	}}, nil
}

// buildChannelSetOps creates a single "set channels.<channelID>" config op.
func buildChannelSetOps(channelID string, channelConfig map[string]interface{}) []agentclient.ConfigOp {
	configJSON, _ := json.Marshal(channelConfig)
	return []agentclient.ConfigOp{{
		Op:         "set",
		Path:       "channels." + channelID,
		Value:      string(configJSON),
		StrictJSON: true,
	}}
}

// setNestedValue sets a value at a dotted path in a nested map.
// e.g., setNestedValue(m, "channels.telegram", value) sets m["channels"]["telegram"] = value
func setNestedValue(m map[string]interface{}, path, value string, strictJSON bool) error {
	parts := splitDotPath(path)
	if len(parts) == 0 {
		return fmt.Errorf("empty path")
	}

	// Navigate to parent
	current := m
	for _, key := range parts[:len(parts)-1] {
		next, ok := current[key].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			current[key] = next
		}
		current = next
	}

	// Set value
	lastKey := parts[len(parts)-1]
	if strictJSON {
		var parsed interface{}
		if err := json.Unmarshal([]byte(value), &parsed); err != nil {
			return fmt.Errorf("parse JSON value for %s: %w", path, err)
		}
		current[lastKey] = parsed
	} else {
		current[lastKey] = value
	}
	return nil
}

// unsetNestedValue removes a value at a dotted path in a nested map.
func unsetNestedValue(m map[string]interface{}, path string) {
	parts := splitDotPath(path)
	if len(parts) == 0 {
		return
	}

	current := m
	for _, key := range parts[:len(parts)-1] {
		next, ok := current[key].(map[string]interface{})
		if !ok {
			return
		}
		current = next
	}
	delete(current, parts[len(parts)-1])
}

// splitDotPath splits a dotted path like "channels.telegram" into ["channels", "telegram"].
func splitDotPath(path string) []string {
	if path == "" {
		return nil
	}
	var parts []string
	start := 0
	for i := 0; i < len(path); i++ {
		if path[i] == '.' {
			if i > start {
				parts = append(parts, path[start:i])
			}
			start = i + 1
		}
	}
	if start < len(path) {
		parts = append(parts, path[start:])
	}
	return parts
}

// deepCopyMap creates a deep copy of a map.
func deepCopyMap(m map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		if nested, ok := v.(map[string]interface{}); ok {
			result[k] = deepCopyMap(nested)
		} else {
			result[k] = v
		}
	}
	return result
}

// deepMergeMap merges overlay into base, returning a new map.
func deepMergeMap(base, overlay map[string]interface{}) map[string]interface{} {
	result := deepCopyMap(base)
	for k, ov := range overlay {
		bv, exists := result[k]
		if !exists {
			result[k] = ov
			continue
		}
		bMap, bIsMap := bv.(map[string]interface{})
		oMap, oIsMap := ov.(map[string]interface{})
		if bIsMap && oIsMap {
			result[k] = deepMergeMap(bMap, oMap)
		} else {
			result[k] = ov
		}
	}
	return result
}
