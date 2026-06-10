package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/configassembly"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

// retryDBQuery retries a database query up to 3 times with linear backoff
// (200ms, 400ms) to handle transient connection errors.
func retryDBQuery[T any](fn func() (T, error)) (T, error) {
	var lastErr error
	for i := range 3 {
		if i > 0 {
			time.Sleep(time.Duration(i*200) * time.Millisecond)
		}
		result, err := fn()
		if err == nil {
			return result, nil
		}
		lastErr = err
		slog.Warn("db.query.retry", "attempt", i+1, "error", err)
	}
	var zero T
	return zero, lastErr
}

func normalizePreferredModelOverride(model string) string {
	switch model {
	case "openai/gpt-5.5":
		return "openai-codex/gpt-5.5"
	default:
		return model
	}
}

func (s *Server) handleListMachineCapabilities(w http.ResponseWriter, r *http.Request) {
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
	caps, err := s.store.ListMachineCapabilities(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, caps)
}

func (s *Server) handleEnableMachineCapability(w http.ResponseWriter, r *http.Request) {
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
	if !requireMutableMachineConfig(w, machine) {
		return
	}

	var req struct {
		EntryID string  `json:"entry_id"`
		Mode    *string `json:"mode,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.EntryID == "" {
		writeError(w, http.StatusBadRequest, "entry_id is required")
		return
	}

	// Validate that registry entry exists
	if _, err := s.store.GetRegistryEntry(r.Context(), req.EntryID); err != nil {
		writeError(w, http.StatusNotFound, "registry entry not found")
		return
	}

	if err := s.store.EnableMachineCapability(r.Context(), machineID, req.EntryID, req.Mode); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("capability.enabled", "machine_id", machineID, "entry_id", req.EntryID, "account_id", accountID)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
}

func (s *Server) handleUpdateMachineCapabilityOverrides(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	entryID := chi.URLParam(r, "entryId")
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
	if !requireMutableMachineConfig(w, machine) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)

	var req struct {
		ConfigOverrides json.RawMessage `json:"config_overrides"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large (max 64KB)")
		} else {
			writeError(w, http.StatusBadRequest, "invalid request body")
		}
		return
	}

	// Validate that overrides don't contain protected keys (top-level or nested)
	var parsed map[string]interface{}
	if err := json.Unmarshal(req.ConfigOverrides, &parsed); err != nil {
		writeError(w, http.StatusBadRequest, "config_overrides must be a JSON object")
		return
	}
	if violations := configassembly.FindProtectedKeyViolations(parsed, ""); len(violations) > 0 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("override contains protected key paths: %v", violations))
		return
	}

	if err := s.store.UpdateMachineCapabilityOverrides(r.Context(), machineID, entryID, req.ConfigOverrides); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("capability.overrides.updated", "machine_id", machineID, "entry_id", entryID, "account_id", accountID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDisableMachineCapability(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	entryID := chi.URLParam(r, "entryId")
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
	if !requireMutableMachineConfig(w, machine) {
		return
	}

	if err := s.store.DisableMachineCapability(r.Context(), machineID, entryID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("capability.disabled", "machine_id", machineID, "entry_id", entryID, "account_id", accountID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleSetMachineIdentity(w http.ResponseWriter, r *http.Request) {
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
	if !requireMutableMachineConfig(w, machine) {
		return
	}

	var req struct {
		Name   *string `json:"name"`
		Avatar *string `json:"avatar"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.store.SetMachineIdentity(r.Context(), machineID, req.Name, req.Avatar); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("identity.updated", "machine_id", machineID, "account_id", accountID)

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.identity_changed",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Updated identity on '%s'", machine.Name),
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) getGatewayToken(ctx context.Context, machineID string) string {
	m, err := s.store.GetMachine(ctx, machineID)
	if err != nil || m.GatewayToken == nil {
		return ""
	}
	return *m.GatewayToken
}

const (
	openAIAgentRuntimeOverrideKey = "openai_agent_runtime"
	modelFallbacksOverrideKey     = "model_fallbacks"
	maxModelFallbacks             = 3
)

func openAIAgentRuntimeResponseValue(runtime string) string {
	normalized, err := configassembly.NormalizeOpenAIAgentRuntime(runtime)
	if err != nil || normalized == "" {
		return "auto"
	}
	return normalized
}

func openAIAgentRuntimeFromConfig(config *store.MachineConfigRecord) string {
	if config == nil || config.PlatformOverrides == nil {
		return "auto"
	}
	var overrides struct {
		OpenAIAgentRuntime string `json:"openai_agent_runtime"`
	}
	if json.Unmarshal(config.PlatformOverrides, &overrides) != nil {
		return "auto"
	}
	return openAIAgentRuntimeResponseValue(overrides.OpenAIAgentRuntime)
}

func modelFallbacksFromConfig(config *store.MachineConfigRecord) []string {
	if config == nil || config.PlatformOverrides == nil {
		return nil
	}
	var overrides struct {
		ModelFallbacks []string `json:"model_fallbacks"`
	}
	if json.Unmarshal(config.PlatformOverrides, &overrides) != nil {
		return nil
	}
	return overrides.ModelFallbacks
}

func modelCatalogEntryUsesOpenAI(entry *store.ModelCatalogEntry) bool {
	if entry == nil {
		return false
	}
	modelRef := entry.ID
	if entry.GatewayModelID != nil && *entry.GatewayModelID != "" {
		modelRef = *entry.GatewayModelID
	}
	return strings.HasPrefix(modelRef, "openai/") || entry.Provider == "openai" || entry.Provider == "openai-codex"
}

func (s *Server) machineHasOpenAIModelAccess(ctx context.Context, machineID string) (bool, error) {
	credentials, err := s.store.ListMachineCredentials(ctx, machineID)
	if err != nil {
		return false, err
	}
	for _, credential := range credentials {
		if credential.Provider == "openai" || credential.Provider == "openai-codex" {
			return true, nil
		}
	}
	return false, nil
}

// assembleConfigForMachine builds the full config for a machine by loading
// capabilities, registry entries, credentials, and identity.
func (s *Server) assembleConfigForMachine(ctx context.Context, machineID string, accountID int, vmHostname string, browserVMIP ...string) ([]byte, int, []string, error) {
	// Look up account slug for the account subdomain (used in allowedOrigins).
	// Browsers access via {account-slug}.{DATA_PLANE_DOMAIN}, so the gateway
	// needs this origin in its allowlist alongside the per-VM tunnel hostname.
	var accountHostname string
	if account, err := s.store.GetAccount(ctx, accountID); err == nil {
		accountHostname = s.accountDataPlaneHostname(account.Slug)
	}

	// The Codex provider api value is keyed on the machine's OpenClaw runtime
	// version (renamed in 2026.6.x). Prefer the version the VM actually runs.
	var resolvedOpenclawVersion string
	if m, err := s.store.GetMachine(ctx, machineID); err == nil {
		for _, v := range []*string{m.ActualOpenclawVersion, m.ResolvedOpenclawVersion, m.OpenclawVersion} {
			if v != nil && *v != "" {
				resolvedOpenclawVersion = *v
				break
			}
		}
	}

	// 1. Load capabilities
	caps, err := s.store.ListMachineCapabilities(ctx, machineID)
	if err != nil {
		return nil, 0, nil, err
	}

	// 2. Build CapabilityWithTemplate list by loading registry entries
	var cwts []configassembly.CapabilityWithTemplate
	var warnings []string
	for _, cap := range caps {
		entry, err := s.store.GetRegistryEntry(ctx, cap.EntryID)
		if err != nil {
			slog.Warn("config.assemble.entry_not_found", "entry_id", cap.EntryID, "machine_id", machineID, "account_id", accountID)
			continue
		}

		var configTemplate map[string]interface{}
		if entry.ConfigTemplate != nil {
			if err := json.Unmarshal(entry.ConfigTemplate, &configTemplate); err != nil {
				slog.Warn("config.assemble.template_parse_failed", "entry_id", cap.EntryID, "machine_id", machineID, "account_id", accountID, "error", err)
				warnings = append(warnings, fmt.Sprintf("template parse failed for %s: %v", cap.EntryID, err))
				continue
			}
		}

		var configOverrides map[string]interface{}
		if cap.ConfigOverrides != nil {
			if err := json.Unmarshal(cap.ConfigOverrides, &configOverrides); err != nil {
				slog.Warn("config.assemble.overrides_parse_failed", "entry_id", cap.EntryID, "machine_id", machineID, "account_id", accountID, "error", err)
				warnings = append(warnings, fmt.Sprintf("overrides parse failed for %s: %v", cap.EntryID, err))
				continue
			}
		}

		cwts = append(cwts, configassembly.CapabilityWithTemplate{
			EntryID:         cap.EntryID,
			EntryType:       entry.Type,
			ConfigTemplate:  configTemplate,
			ConfigOverrides: configOverrides,
			Mode:            cap.Mode,
		})
	}

	// 3. Load machine config for identity
	var identity *configassembly.Identity
	machineConfig, err := s.store.GetMachineConfig(ctx, machineID)
	if err == nil && machineConfig != nil {
		identity = &configassembly.Identity{
			Name:   machineConfig.IdentityName,
			Avatar: machineConfig.IdentityAvatar,
		}
	}

	// 4. Load machine-scoped credentials (no account-wide fallback).
	credentials := make(map[string]string)
	machineCreds, err := retryDBQuery(func() ([]store.Credential, error) {
		return s.store.ListMachineCredentials(ctx, machineID)
	})
	if err != nil {
		return nil, 0, nil, fmt.Errorf("listing machine credentials: %w", err)
	}
	for _, c := range machineCreds {
		credentials[c.Provider] = "present"
	}

	// Derive default model from available LLM credentials.
	// BYOK priority (anthropic > openai > google), falling back to platform model.
	// The user's preferred_model override (lines below) always wins.
	defaultModel := ""
	modelPriority := []struct{ provider, model string }{
		{"anthropic", "anthropic/claude-sonnet-4-6"},
		{"openai", "openai/gpt-4o"},
		{"google", "google/gemini-2.0-flash"},
	}
	for _, mp := range modelPriority {
		if _, ok := credentials[mp.provider]; ok {
			defaultModel = mp.model
			break
		}
	}

	// Fall back to platform model if no BYOK credentials
	if defaultModel == "" {
		defaultModel = "openai/gpt-oss-120b"
	}

	// Check for user-preferred model override
	var openAIAgentRuntime string
	var modelFallbacks []string
	if machineConfig != nil && machineConfig.PlatformOverrides != nil {
		var overrides struct {
			PreferredModel     string   `json:"preferred_model"`
			OpenAIAgentRuntime string   `json:"openai_agent_runtime"`
			ModelFallbacks     []string `json:"model_fallbacks"`
		}
		if json.Unmarshal(machineConfig.PlatformOverrides, &overrides) == nil {
			if overrides.PreferredModel != "" {
				defaultModel = normalizePreferredModelOverride(overrides.PreferredModel)
			}
			openAIAgentRuntime = overrides.OpenAIAgentRuntime
			modelFallbacks = overrides.ModelFallbacks
		}
	}

	// Load agents (personas) for this machine
	machineAgents, err := s.store.ListMachineAgents(ctx, machineID)
	if err != nil {
		slog.Warn("machine.config.agents_load_failed", "machine_id", machineID, "error", err)
	}

	var agentDefs []configassembly.AgentDefinition
	if len(machineAgents) > 0 && defaultModel == "" {
		warnings = append(warnings, "agents defined but no default model — agents.list skipped")
	} else {
		for _, a := range machineAgents {
			ad := configassembly.AgentDefinition{
				AgentID:   a.AgentID,
				Name:      a.Name,
				IsDefault: a.IsDefault,
				SortOrder: a.SortOrder,
			}
			if a.Model != nil {
				ad.Model = *a.Model
			}
			if a.IdentityEmoji != nil {
				ad.IdentityEmoji = *a.IdentityEmoji
			}
			if a.IdentityAvatar != nil {
				ad.IdentityAvatar = *a.IdentityAvatar
			}
			agentDefs = append(agentDefs, ad)
		}
	}

	// Load enabled plugins for this machine
	var pluginSelections []configassembly.PluginSelection
	machinePlugins, err := s.store.ListMachinePlugins(ctx, machineID)
	if err != nil {
		slog.Warn("machine.config.plugins_load_failed", "machine_id", machineID, "error", err)
	}
	for _, mp := range machinePlugins {
		if !mp.Enabled {
			continue
		}
		catalogEntry, err := s.store.GetPluginCatalogEntry(ctx, mp.PluginID)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("plugin %s: catalog entry not found", mp.PluginID))
			continue
		}

		ps := configassembly.PluginSelection{
			PluginID: mp.PluginID,
			Slot:     mp.Slot,
		}

		if catalogEntry.ConfigTemplate != nil {
			var tmpl map[string]interface{}
			if json.Unmarshal(catalogEntry.ConfigTemplate, &tmpl) == nil {
				ps.ConfigTemplate = tmpl
			}
		}
		if mp.ConfigOverrides != nil {
			var overrides map[string]interface{}
			if json.Unmarshal(mp.ConfigOverrides, &overrides) == nil {
				ps.ConfigOverrides = overrides
			}
		}

		pluginSelections = append(pluginSelections, ps)
	}

	// 5. Load decrypted channel credential values for direct injection into config.
	// Channel tokens (e.g. Telegram bot token) are injected as plaintext so the
	// gateway has them immediately at startup without needing SecretRef resolution.
	channelCredentialValues := make(map[string]string)
	if s.secretKey != "" {
		machineCredsWithValues, err := s.store.ListMachineCredentialsWithValues(ctx, machineID)
		if err != nil {
			slog.Warn("machine.config.channel_creds_load_failed", "machine_id", machineID, "error", err)
		} else {
			for _, cred := range machineCredsWithValues {
				if s.isProxiedProvider(ctx, cred.Provider) {
					continue
				}
				val, decryptErr := crypto.Decrypt(cred.EncryptedValue, s.secretKey)
				if decryptErr != nil {
					slog.Warn("machine.config.channel_cred_decrypt_failed", "machine_id", machineID, "provider", cred.Provider, "error", decryptErr)
					continue
				}
				channelCredentialValues[cred.Provider] = val
			}
		}
	}

	// 6. Load model catalog for config assembly
	catalog, err := s.store.ListModelCatalog(ctx)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("list model catalog: %w", err)
	}
	var catalogModels []configassembly.CatalogModel
	for _, e := range catalog {
		catalogModels = append(catalogModels, configassembly.CatalogModel{
			ID:             e.ID,
			Label:          e.Label,
			Source:         e.Source,
			Tier:           e.Tier,
			GatewayModelID: e.GatewayModelID,
			Provider:       e.Provider,
		})
	}

	// 6b. Load provider catalog for config assembly
	providerCatalog, err := s.store.ListProviderCatalog(ctx)
	if err != nil {
		slog.Warn("machine.config.provider_catalog_load_failed", "machine_id", machineID, "error", err)
	} else {
		slog.Debug("machine.config.provider_catalog_loaded", "machine_id", machineID, "count", len(providerCatalog))
	}
	var catalogProviders []configassembly.CatalogProvider
	for _, e := range providerCatalog {
		cp := configassembly.CatalogProvider{
			ID:              e.ID,
			Category:        e.Category,
			AutodetectOrder: e.AutodetectOrder,
		}
		if e.EnvVar != nil {
			cp.EnvVar = *e.EnvVar
		}
		if e.UpstreamHost != nil {
			cp.UpstreamHost = *e.UpstreamHost
		}
		cp.UpstreamPathPrefix = e.UpstreamPathPrefix
		if e.AuthMethod != nil {
			cp.AuthMethod = *e.AuthMethod
		}
		if e.AuthHeader != nil {
			cp.AuthHeader = *e.AuthHeader
		}
		if e.ExecSecretID != nil {
			cp.ExecSecretID = *e.ExecSecretID
		}
		if e.RuntimeProviderID != nil {
			cp.RuntimeProviderID = *e.RuntimeProviderID
		}
		if e.PluginID != nil {
			cp.PluginID = *e.PluginID
		}
		catalogProviders = append(catalogProviders, cp)
	}

	// 6c. Read search provider override from platform_overrides
	var searchProvider string
	if machineConfig != nil && machineConfig.PlatformOverrides != nil {
		var overrides struct {
			SearchProvider string `json:"search_provider"`
		}
		if json.Unmarshal(machineConfig.PlatformOverrides, &overrides) == nil && overrides.SearchProvider != "" {
			searchProvider = overrides.SearchProvider
		}
	}

	// 7. Assemble
	params := configassembly.AssemblyParams{
		MachineID:                machineID,
		Capabilities:             cwts,
		Identity:                 identity,
		Credentials:              credentials,
		ChannelCredentialValues:  channelCredentialValues,
		ProxyBaseURL:             s.proxyBaseURL,
		DefaultModel:             defaultModel,
		ModelFallbacks:           modelFallbacks,
		VMHostname:               vmHostname,
		AccountHostname:          accountHostname,
		BridgeIP:                 "192.168.100.1",
		Agents:                   agentDefs,
		Plugins:                  pluginSelections,
		NativeMode:               true, // all machines use native config mode with exec secret refs
		ResolvedOpenclawVersion:  resolvedOpenclawVersion,
		ModelCatalog:             catalogModels,
		ProviderCatalog:          catalogProviders,
		SearchProvider:           searchProvider,
		OpenAIAgentRuntime:       openAIAgentRuntime,
		OpikAPIURL:               s.opikAPIURL,
		OpikAPIKey:               s.getGatewayToken(ctx, machineID),
		ComposioAPIURL:           s.composioAPIURL,
		ComposioProxyTokenSigner: s.signComposioProxyToken,
	}
	if len(browserVMIP) > 0 && browserVMIP[0] != "" {
		params.BrowserVMIP = browserVMIP[0]
	}

	data, err := configassembly.AssembleConfig(params)
	if err != nil {
		return nil, 0, warnings, err
	}

	slog.Info("config.assembled", "machine_id", machineID, "capability_count", len(cwts), "config_bytes", len(data))

	configVersion := 0
	if machineConfig != nil {
		configVersion = machineConfig.ConfigVersion
	}

	return data, configVersion, warnings, nil
}

// waitForGatewayHealth polls the gateway health endpoint until it returns 200 or
// the timeout expires. Used before pushing config on restart to avoid racing with
// gateway startup (~55s).
func (s *Server) waitForGatewayHealth(ctx context.Context, machine *store.Machine, timeout time.Duration) error {
	if machine.HostID == nil || machine.ProxyToken == nil || s.agentClient == nil {
		return fmt.Errorf("machine not ready for gateway health check")
	}
	host, err := s.store.GetHost(ctx, *machine.HostID)
	if err != nil {
		return fmt.Errorf("get host: %w", err)
	}

	deadline := time.Now().Add(timeout)
	interval := 3 * time.Second
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.agentClient.CheckGatewayHealth(ctx, host, machine.ID, *machine.ProxyToken); err == nil {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("gateway not healthy after %s", timeout)
}

// pushCredentialsToVMByID is a convenience wrapper that resolves the machine and host,
// then pushes credentials. Used from credential set/delete handlers.
func (s *Server) pushCredentialsToVMByID(machineID string) {
	if err := s.pushCredentialsToVMByIDContext(context.Background(), machineID); err != nil {
		slog.Warn("credential_push.failed", "machine_id", machineID, "error", err)
	}
	// Re-assemble and push config — search provider may have changed
	go s.pushMachineConfigAsync(machineID)
}

func (s *Server) pushCredentialsToVMByIDContext(ctx context.Context, machineID string) error {
	machine, err := s.store.GetMachine(ctx, machineID)
	if err != nil {
		return fmt.Errorf("load machine: %w", err)
	}
	if machine.Status != "running" || machine.HostID == nil {
		return nil
	}
	host, err := s.store.GetHost(ctx, *machine.HostID)
	if err != nil {
		return fmt.Errorf("load host: %w", err)
	}
	if err := s.pushCredentialsToVM(ctx, host, machine); err != nil {
		return fmt.Errorf("push credentials: %w", err)
	}
	return nil
}

// pushCredentialsToVM loads, decrypts, and pushes LLM credentials to a running VM's
// metadata server via the agent. This ensures the proxy can authenticate requests
// for providers whose keys were added after the machine was initially started.
func (s *Server) pushCredentialsToVM(ctx context.Context, host *store.Host, machine *store.Machine) error {
	if s.secretKey == "" {
		return nil // no encryption key configured
	}

	// Load machine-scoped credentials (no account-wide fallback).
	creds, err := s.store.ListMachineCredentialsWithValues(ctx, machine.ID)
	if err != nil {
		return fmt.Errorf("list machine credentials: %w", err)
	}

	// Build runtime ID map for providers with remapped names (e.g., gemini-search → gemini).
	// The proxy looks up credentials by its provider Name (the runtime ID), not the DB credential ID.
	runtimeIDMap := make(map[string]string)
	catalog, catErr := s.store.ListProviderCatalog(ctx)
	if catErr != nil {
		slog.Error("credential_push.catalog_load_failed", "machine_id", machine.ID, "error", catErr)
		// Continue without remapping — better to push with wrong keys than not push at all
	} else {
		for _, entry := range catalog {
			if entry.RuntimeProviderID != nil && *entry.RuntimeProviderID != "" {
				runtimeIDMap[entry.ID] = *entry.RuntimeProviderID
			}
		}
	}

	llmKeys := make(map[string]metadata.CredentialEntry)
	for _, cred := range creds {
		if !s.isProxiedProvider(ctx, cred.Provider) {
			continue
		}
		val, decryptErr := crypto.Decrypt(cred.EncryptedValue, s.secretKey)
		if decryptErr != nil {
			slog.Warn("credential_push.decrypt_failed", "machine_id", machine.ID, "provider", cred.Provider, "error", decryptErr)
			continue
		}
		// Key by runtime provider ID so the proxy can find the credential.
		// e.g., credential "gemini-search" → keyed as "gemini" in LLMKeys.
		keyName := cred.Provider
		if rtID, ok := runtimeIDMap[cred.Provider]; ok {
			keyName = rtID
			slog.Debug("credential_push.remapped", "machine_id", machine.ID, "db_provider", cred.Provider, "runtime_id", rtID)
		}
		llmKeys[keyName] = metadata.CredentialEntry{
			Value:          val,
			CredentialType: cred.CredentialType,
			ExpiresAt:      cred.ExpiresAt,
		}
	}

	pushKeyNames := make([]string, 0, len(llmKeys))
	for k := range llmKeys {
		pushKeyNames = append(pushKeyNames, k)
	}
	slog.Info("credential_push.sending", "machine_id", machine.ID, "key_count", len(llmKeys), "providers", pushKeyNames, "total_creds", len(creds))

	// Include platform Nebius key so it isn't lost when the agent replaces
	// the full LLMKeys map (agents without the merge fix do a full replace).
	if s.machines != nil {
		if nebiusKey := s.machines.PlatformNebiusAPIKey(); nebiusKey != "" {
			llmKeys["nebius"] = metadata.CredentialEntry{Value: nebiusKey, CredentialType: "api_key"}
		}
	}

	// Always call replace, even with an empty map — this ensures deleted
	// credentials are removed from the agent's metadata. Skipping on empty
	// would leave the last deleted key active until VM restart.
	if err := s.agentClient.ReplaceVMCredentials(ctx, host, machine.ID, llmKeys); err != nil {
		return fmt.Errorf("replace credentials: %w", err)
	}

	return nil
}

// resolveBrowserVMIP returns the IP of the browser VM currently paired with
// this machine, or "" if the machine is unpaired or the paired browser VM is
// not yet runnable (no host, not running, or no assigned vm_ip). Those cases
// are legitimate "no browser block" signals and return (empty, nil).
//
// A non-nil error means the store call itself failed — the caller MUST NOT
// collapse that to empty, because doing so would make a transient DB error
// strip browser access from a still-paired machine.
func (s *Server) resolveBrowserVMIP(ctx context.Context, machine *store.Machine) (string, error) {
	if machine == nil || machine.BrowserVMID == nil {
		return "", nil
	}
	bvm, err := s.store.GetBrowserVM(ctx, *machine.BrowserVMID)
	if err != nil {
		return "", fmt.Errorf("get browser VM %s: %w", *machine.BrowserVMID, err)
	}
	if bvm == nil || bvm.Status != "running" || bvm.VMIP == nil {
		return "", nil
	}
	return *bvm.VMIP, nil
}

func (s *Server) handleGetMachineAssembledConfig(w http.ResponseWriter, r *http.Request) {
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

	// Resolve browser VM IP from pairing. A lookup error here is NOT the
	// same as "browser VM not runnable" — a transient store failure used
	// to silently collapse to browserVMIP="", which in turn made the
	// assembler drop the entire browser block. That would strip browser
	// access from a still-paired machine on a single failed round-trip,
	// so surface the error as a 5xx and let the caller retry instead.
	// Only treat a found-but-not-running browser VM (or missing VMIP) as
	// a deliberate "omit the block" signal.
	browserVMIP, err := s.resolveBrowserVMIP(r.Context(), machine)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve browser VM: "+err.Error())
		return
	}
	data, _, warnings, err := s.assembleConfigForMachine(r.Context(), machineID, accountID, s.dataPlaneHostname("m", machine.Slug), browserVMIP)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Inject masked channel credential markers so the preview shows what the VM
	// will actually receive (the metadata server injects real tokens at runtime).
	data = injectChannelCredentialMarkers(r.Context(), s.store, machineID, accountID, data, s.isProxiedProvider)

	if len(warnings) > 0 {
		// Return config with warnings so callers know capabilities were dropped
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"config":   json.RawMessage(data),
			"warnings": warnings,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err = w.Write(data); err != nil {
		slog.Warn("config.assembled.write_failed", "machine_id", machineID, "error", err)
	}
}

func (s *Server) handlePushMachineConfig(w http.ResponseWriter, r *http.Request) {
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
	if !requireMutableMachineConfig(w, machine) {
		return
	}

	logParams := events.LogParams{
		Category:  "config",
		Action:    "config.saved",
		Status:    "success",
		ActorType: "user",
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Config push triggered for '%s'", machine.Name),
	}
	if claims := auth.UserFromContext(r.Context()); claims != nil {
		logParams.ActorID = &claims.UserID
	}
	s.activity.Log(r.Context(), logParams)

	s.pushMachineConfigInternal(w, r, machine)
}

// pushMachineConfigInternal assembles config, saves it, and optionally pushes
// to a running VM. Shared by JWT-authenticated and agent-authenticated handlers.
func (s *Server) pushMachineConfigInternal(w http.ResponseWriter, r *http.Request, machine *store.Machine) {
	result, err := s.pushMachineConfig(r.Context(), machine)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := map[string]interface{}{
		"status":         "ok",
		"config_version": result.ConfigVersion,
		"live_update":    result.LiveUpdate,
	}
	if result.LiveUpdateError != "" {
		resp["live_update_error"] = result.LiveUpdateError
	}
	if len(result.Warnings) > 0 {
		resp["warnings"] = result.Warnings
	}
	writeJSON(w, http.StatusOK, resp)
}

type machineConfigPushResult struct {
	ConfigVersion   int
	LiveUpdate      string
	LiveUpdateError string
	Warnings        []string
}

func (s *Server) pushMachineConfigAsync(machineID string) {
	if err := s.pushMachineConfigByIDContext(context.Background(), machineID); err != nil {
		slog.Warn("config.push.async_failed", "machine_id", machineID, "error", err)
	}
}

func (s *Server) pushMachineConfigByIDContext(ctx context.Context, machineID string) error {
	machine, err := s.store.GetMachine(ctx, machineID)
	if err != nil {
		return fmt.Errorf("load machine: %w", err)
	}
	if machine.Status != "running" || machine.HostID == nil {
		return nil
	}
	_, err = s.pushMachineConfig(ctx, machine)
	return err
}

func (s *Server) pushMachineConfig(ctx context.Context, machine *store.Machine) (machineConfigPushResult, error) {
	machineID := machine.ID
	if store.NormalizeMachineKind(machine.Kind) == store.MachineKindHermes {
		return s.pushHermesConfig(ctx, machine, true)
	}

	// Resolve browser VM IP from pairing. Same policy as the preview
	// path: an error from the store is surfaced as a 5xx so a transient
	// failure doesn't silently strip the browser block from a still-paired
	// machine. "Not running" or "no VMIP" is still a legitimate "omit"
	// signal and stays non-fatal.
	browserVMIP, err := s.resolveBrowserVMIP(ctx, machine)
	if err != nil {
		return machineConfigPushResult{}, fmt.Errorf("failed to resolve browser VM: %w", err)
	}
	data, configVersion, warnings, err := s.assembleConfigForMachine(ctx, machineID, machine.AccountID, s.dataPlaneHostname("m", machine.Slug), browserVMIP)
	if err != nil {
		return machineConfigPushResult{}, err
	}

	newVersion := configVersion + 1

	// Fetch the previous assembled config BEFORE overwriting, so we can diff
	// old vs new to generate unset ops for removed keys.
	oldData := []byte("{}")
	if prevRecord, prevErr := s.store.GetMachineConfig(ctx, machineID); prevErr == nil && prevRecord != nil && prevRecord.AssembledConfig != nil {
		oldData = prevRecord.AssembledConfig
	}

	slog.Info("config.push",
		"machine_id", machineID,
		"prev_version", configVersion,
		"new_version", newVersion,
		"config_bytes", len(data),
		"account_id", machine.AccountID)
	slog.Debug("config.push.assembled_json", "machine_id", machineID, "config", string(data))

	if err := s.store.SetMachineAssembledConfig(ctx, machineID, json.RawMessage(data), newVersion); err != nil {
		return machineConfigPushResult{}, err
	}

	// Write soul files BEFORE config push (souls must be on disk before gateway reload)
	if machine.Status == "running" && machine.HostID != nil && s.agentClient != nil {
		host, hostErr := s.store.GetHost(ctx, *machine.HostID)
		if hostErr == nil && machine.ProxyToken != nil {
			agents, agentErr := s.store.ListMachineAgents(ctx, machineID)
			if agentErr == nil {
				for _, a := range agents {
					soulPath := "/home/openclaw/.openclaw/workspace-" + a.AgentID + "/SOUL.md"
					if a.IsDefault {
						soulPath = "/home/openclaw/.openclaw/workspace/SOUL.md"
					}
					// Write soul content, or empty string to clear stale SOUL.md
					content := ""
					if a.Soul != nil {
						content = *a.Soul
					}
					if err := s.agentClient.WriteFile(ctx, host, machineID, *machine.ProxyToken, soulPath, content); err != nil {
						slog.Warn("machine.config.soul_write_failed", "machine_id", machineID, "agent_id", a.AgentID, "error", err)
						warnings = append(warnings, fmt.Sprintf("soul write failed for agent %s: %v", a.AgentID, err))
					}
				}
			}
		}
	}

	result := machineConfigPushResult{
		ConfigVersion: newVersion,
		LiveUpdate:    "not_running",
		Warnings:      warnings,
	}
	if machine.Status != "running" || machine.HostID == nil {
		return result, nil
	}

	host, err := s.store.GetHost(ctx, *machine.HostID)
	if err != nil {
		slog.Warn("config.push.get_host_failed", "machine_id", machineID, "host_id", *machine.HostID, "error", err)
		return result, nil
	}
	if host == nil {
		slog.Warn("config.push.host_not_found", "machine_id", machineID, "host_id", *machine.HostID)
		return result, nil
	}
	if s.agentClient == nil {
		// agentClient is nil in test environments; in production it's always set via NewServer()
		result.LiveUpdate = "skipped"
		return result, nil
	}
	if machine.ProxyToken == nil {
		slog.Warn("config.push.no_proxy_token", "machine_id", machineID)
		result.LiveUpdate = "failed"
		result.LiveUpdateError = "no proxy token for config push"
		return result, nil
	}

	ops, buildErr := buildConfigOps(oldData, data)
	if buildErr != nil {
		slog.Error("config.push.build_ops_failed", "machine_id", machineID, "error", buildErr)
		result.LiveUpdate = "failed"
		result.LiveUpdateError = buildErr.Error()
		return result, nil
	}

	errs := s.agentClient.ConfigBatch(ctx, host, machineID, *machine.ProxyToken, ops)
	if len(errs) > 0 {
		slog.Error("config.push.config_set_failed", "machine_id", machineID, "error_count", len(errs), "first_error", errs[0].Error())
		result.LiveUpdate = "failed"
		result.LiveUpdateError = errs[0].Error()
		return result, nil
	}

	slog.Info("config.push.config_set_ok", "machine_id", machineID, "op_count", len(ops))
	result.LiveUpdate = "sent"

	// Push LLM credentials to metadata server so the proxy can
	// authenticate requests for any newly-added providers.
	if err := s.pushCredentialsToVM(ctx, host, machine); err != nil {
		slog.Warn("config.push.credential_push_failed", "machine_id", machineID, "error", err)
	}

	return result, nil
}

func (s *Server) handleGetMachineModel(w http.ResponseWriter, r *http.Request) {
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

	model, err := s.store.GetMachinePreferredModel(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	machineConfig, _ := s.store.GetMachineConfig(r.Context(), machineID)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"model":                model,
		"openai_agent_runtime": openAIAgentRuntimeFromConfig(machineConfig),
		"fallbacks":            modelFallbacksFromConfig(machineConfig),
	})
}

func (s *Server) handleSetMachineModel(w http.ResponseWriter, r *http.Request) {
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
	if !requireMutableMachineConfig(w, machine) {
		return
	}

	var req struct {
		Model              string    `json:"model"`
		OpenAIAgentRuntime *string   `json:"openai_agent_runtime,omitempty"`
		Fallbacks          *[]string `json:"fallbacks,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}
	catalogEntry, err := s.store.GetModelCatalogEntry(r.Context(), req.Model)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate model")
		return
	}
	if catalogEntry == nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("model %q is not allowed", req.Model))
		return
	}

	var fallbacks []string
	if req.Fallbacks != nil {
		seen := map[string]bool{req.Model: true}
		for _, fallback := range *req.Fallbacks {
			fallback = strings.TrimSpace(fallback)
			if fallback == "" || seen[fallback] {
				continue
			}
			if len(fallbacks) >= maxModelFallbacks {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d fallback models are allowed", maxModelFallbacks))
				return
			}
			fallbackEntry, err := s.store.GetModelCatalogEntry(r.Context(), fallback)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to validate fallback model")
				return
			}
			if fallbackEntry == nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("fallback model %q is not allowed", fallback))
				return
			}
			seen[fallback] = true
			fallbacks = append(fallbacks, fallback)
		}
	}

	var normalizedOpenAIRuntime string
	if req.OpenAIAgentRuntime != nil {
		var normalizeErr error
		normalizedOpenAIRuntime, normalizeErr = configassembly.NormalizeOpenAIAgentRuntime(*req.OpenAIAgentRuntime)
		if normalizeErr != nil {
			writeError(w, http.StatusBadRequest, normalizeErr.Error())
			return
		}
		if normalizedOpenAIRuntime != "" && !modelCatalogEntryUsesOpenAI(catalogEntry) {
			hasOpenAIModelAccess, err := s.machineHasOpenAIModelAccess(r.Context(), machineID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to validate OpenAI model access")
				return
			}
			if !hasOpenAIModelAccess {
				writeError(w, http.StatusBadRequest, "openai_agent_runtime requires an OpenAI or OpenAI Codex model")
				return
			}
		}
	}

	if err := s.store.SetMachinePreferredModel(r.Context(), machineID, req.Model); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.Fallbacks != nil {
		if err := s.store.SetMachineModelFallbacks(r.Context(), machineID, fallbacks); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if req.OpenAIAgentRuntime != nil {
		if normalizedOpenAIRuntime == "" {
			if err := s.store.DeleteMachinePlatformOverride(r.Context(), machineID, openAIAgentRuntimeOverrideKey); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		} else if err := s.store.UpdateMachinePlatformOverride(r.Context(), machineID, openAIAgentRuntimeOverrideKey, normalizedOpenAIRuntime); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	machineConfig, err := s.store.GetMachineConfig(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	openAIAgentRuntime := openAIAgentRuntimeFromConfig(machineConfig)
	storedFallbacks := modelFallbacksFromConfig(machineConfig)

	slog.Info("machine.model.updated", "machine_id", machineID, "model", req.Model, "openai_agent_runtime", openAIAgentRuntime, "fallback_count", len(storedFallbacks), "account_id", accountID)

	modelLogParams := events.LogParams{
		Category:  "config",
		Action:    "config.default_model_set",
		Status:    "success",
		ActorType: "user",
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Set default model to '%s' on '%s'", req.Model, machine.Name),
		Detail:    map[string]any{"model": req.Model, "openai_agent_runtime": openAIAgentRuntime, "fallbacks": storedFallbacks},
	}
	if claims := auth.UserFromContext(r.Context()); claims != nil {
		modelLogParams.ActorID = &claims.UserID
	}
	s.activity.Log(r.Context(), modelLogParams)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":               "ok",
		"model":                req.Model,
		"openai_agent_runtime": openAIAgentRuntime,
		"fallbacks":            storedFallbacks,
	})
}

// legacyLLMProviderSet is the fallback for providers not yet in the catalog.
var legacyLLMProviderSet = map[string]bool{"anthropic": true, "openai": true, "google": true, "openrouter": true, "nebius": true, "openai-codex": true}

// isProxiedProvider returns true if the provider's credentials should be pushed
// to the VM for proxy-based resolution (categories "ai" and "search").
// Falls back to the hardcoded set if catalog lookup fails.
func (s *Server) isProxiedProvider(ctx context.Context, provider string) bool {
	entry, err := s.store.GetProviderCatalogEntry(ctx, provider)
	if err == nil && entry != nil {
		proxied := entry.Category == "ai" || entry.Category == "search"
		slog.Debug("provider_classification", "provider", provider, "category", entry.Category, "proxied", proxied)
		return proxied
	}
	// Fallback for providers not yet in catalog
	proxied := legacyLLMProviderSet[provider]
	slog.Debug("provider_classification.fallback", "provider", provider, "proxied", proxied, "catalog_error", err)
	return proxied
}

// injectChannelCredentialMarkers adds masked botToken placeholders into the assembled
// config JSON for any channel credentials linked to the machine. This lets the config
// preview show what the VM will actually receive (the metadata server injects real
// tokens at runtime). Returns the original config unchanged on any error.
func injectChannelCredentialMarkers(ctx context.Context, st store.Store, machineID string, accountID int, configJSON []byte, isProxied func(ctx context.Context, provider string) bool) []byte {
	// Load machine-scoped credentials (no account-wide fallback).
	creds, err := st.ListMachineCredentials(ctx, machineID)
	if err != nil {
		return configJSON
	}

	// Collect channel providers (anything not proxied).
	var channelProviders []string
	for _, c := range creds {
		if !isProxied(ctx, c.Provider) {
			channelProviders = append(channelProviders, c.Provider)
		}
	}
	if len(channelProviders) == 0 {
		return configJSON
	}

	var result map[string]interface{}
	if err := json.Unmarshal(configJSON, &result); err != nil {
		return configJSON
	}

	channels, _ := result["channels"].(map[string]interface{})
	if channels == nil {
		channels = make(map[string]interface{})
	}

	for _, provider := range channelProviders {
		channelID, fieldName, ok := configassembly.ProviderToChannel(provider)
		if !ok {
			continue // provider doesn't use a token field (e.g. WhatsApp)
		}
		chConf, _ := channels[channelID].(map[string]interface{})
		if chConf == nil {
			chConf = make(map[string]interface{})
		}
		chConf[fieldName] = "••••••••"
		channels[channelID] = chConf
	}
	result["channels"] = channels

	merged, err := json.Marshal(result)
	if err != nil {
		return configJSON
	}
	return merged
}

// handleGetSearchProvider returns the search provider override and resolved provider.
func (s *Server) handleGetSearchProvider(w http.ResponseWriter, r *http.Request) {
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

	// Read explicit override from platform_overrides
	var override string
	if cfg, err := s.store.GetMachineConfig(r.Context(), machineID); err == nil && cfg != nil && cfg.PlatformOverrides != nil {
		var overrides struct {
			SearchProvider string `json:"search_provider"`
		}
		if json.Unmarshal(cfg.PlatformOverrides, &overrides) == nil {
			override = overrides.SearchProvider
		}
	}

	// Compute the resolved provider: override wins, otherwise auto-detect
	// from credentials by autodetect_order (same logic as config assembly).
	resolved := override
	if resolved == "" {
		creds, credErr := s.store.ListMachineCredentials(r.Context(), machineID)
		catalog, catErr := s.store.ListProviderCatalog(r.Context())
		if credErr == nil && catErr == nil {
			credSet := make(map[string]bool)
			for _, c := range creds {
				credSet[c.Provider] = true
			}
			type candidate struct {
				id    string
				order int
			}
			var candidates []candidate
			for _, p := range catalog {
				if p.Category != "search" || p.AutodetectOrder == nil || !credSet[p.ID] {
					continue
				}
				candidates = append(candidates, candidate{id: p.ID, order: *p.AutodetectOrder})
			}
			if len(candidates) > 0 {
				best := candidates[0]
				for _, c := range candidates[1:] {
					if c.order < best.order {
						best = c
					}
				}
				// Use runtime provider ID for the resolved name
				resolved = best.id
				for _, p := range catalog {
					if p.ID == best.id && p.RuntimeProviderID != nil && *p.RuntimeProviderID != "" {
						resolved = *p.RuntimeProviderID
						break
					}
				}
			}
		}
	}

	slog.Debug("search_provider.get", "machine_id", machineID, "override", override, "resolved", resolved)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"provider": override,
		"resolved": resolved,
	})
}

// handleSetSearchProvider sets the search provider override.
func (s *Server) handleSetSearchProvider(w http.ResponseWriter, r *http.Request) {
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
	if !requireMutableMachineConfig(w, machine) {
		return
	}

	var req struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate provider exists in catalog as a search provider
	entry, err := s.store.GetProviderCatalogEntry(r.Context(), req.Provider)
	if err != nil || entry == nil || entry.Category != "search" {
		writeError(w, http.StatusBadRequest, "invalid search provider")
		return
	}

	// Update platform_overrides
	if err := s.store.UpdateMachinePlatformOverride(r.Context(), machineID, "search_provider", req.Provider); err != nil {
		slog.Error("search_provider.set_failed", "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	slog.Info("search_provider.set", "machine_id", machineID, "provider", req.Provider)

	// Push updated config to running VM
	go s.pushMachineConfigAsync(machineID)

	writeJSON(w, http.StatusOK, map[string]string{"provider": req.Provider})
}

// handleDeleteSearchProvider clears the search provider override (reverts to auto-detect).
func (s *Server) handleDeleteSearchProvider(w http.ResponseWriter, r *http.Request) {
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
	if !requireMutableMachineConfig(w, machine) {
		return
	}

	// Clear the search_provider key from platform_overrides
	if err := s.store.DeleteMachinePlatformOverride(r.Context(), machineID, "search_provider"); err != nil {
		slog.Error("search_provider.delete_failed", "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	slog.Info("search_provider.cleared", "machine_id", machineID)

	// Push updated config to running VM
	go s.pushMachineConfigAsync(machineID)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
