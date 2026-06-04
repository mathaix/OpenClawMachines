package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/mathaix/openclawmachines/backend/internal/configassembly"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

type hermesAssembledSnapshot struct {
	Kind       string          `json:"kind"`
	ConfigYAML string          `json:"config_yaml"`
	Env        string          `json:"env"`
	AuthJSON   json.RawMessage `json:"auth_json,omitempty"`
	Soul       string          `json:"soul,omitempty"`
}

func (s *Server) assembleHermesSeedConfigForMachine(ctx context.Context, machine *store.Machine) (*configassembly.HermesSeedConfig, error) {
	defaultModel := "openai/gpt-oss-120b"
	if preferredModel, err := s.store.GetMachinePreferredModel(ctx, machine.ID); err == nil && strings.TrimSpace(preferredModel) != "" {
		defaultModel = preferredModel
	}

	providerCatalog, err := s.store.ListProviderCatalog(ctx)
	if err != nil {
		slog.Warn("hermes.config.provider_catalog_load_failed", "machine_id", machine.ID, "error", err)
	}
	var catalogProviders []configassembly.CatalogProvider
	runtimeIDMap := make(map[string]string)
	for _, entry := range providerCatalog {
		cp := configassembly.CatalogProvider{
			ID:                 entry.ID,
			Category:           entry.Category,
			AutodetectOrder:    entry.AutodetectOrder,
			UpstreamPathPrefix: entry.UpstreamPathPrefix,
		}
		if entry.EnvVar != nil {
			cp.EnvVar = *entry.EnvVar
		}
		if entry.UpstreamHost != nil {
			cp.UpstreamHost = *entry.UpstreamHost
		}
		if entry.AuthMethod != nil {
			cp.AuthMethod = *entry.AuthMethod
		}
		if entry.AuthHeader != nil {
			cp.AuthHeader = *entry.AuthHeader
		}
		if entry.ExecSecretID != nil {
			cp.ExecSecretID = *entry.ExecSecretID
		}
		if entry.RuntimeProviderID != nil {
			cp.RuntimeProviderID = *entry.RuntimeProviderID
			if *entry.RuntimeProviderID != "" {
				runtimeIDMap[entry.ID] = *entry.RuntimeProviderID
			}
		}
		if entry.PluginID != nil {
			cp.PluginID = *entry.PluginID
		}
		catalogProviders = append(catalogProviders, cp)
	}

	providerCredentials := make(map[string]string)
	allChannelCredentials := make(map[string]string)
	if s.secretKey != "" {
		creds, err := s.store.ListMachineCredentialsWithValues(ctx, machine.ID)
		if err != nil {
			return nil, fmt.Errorf("list machine credentials: %w", err)
		}
		for _, cred := range creds {
			value, err := crypto.Decrypt(cred.EncryptedValue, s.secretKey)
			if err != nil {
				slog.Warn("hermes.config.credential_decrypt_failed", "machine_id", machine.ID, "provider", cred.Provider, "error", err)
				continue
			}
			if _, ok := configassembly.HermesChannelEnvVars[cred.Provider]; ok {
				allChannelCredentials[cred.Provider] = value
				continue
			}
			providerCredentials[cred.Provider] = value
			if runtimeProviderID := runtimeIDMap[cred.Provider]; runtimeProviderID != "" {
				providerCredentials[runtimeProviderID] = value
			}
		}
	}
	if s.machines != nil {
		if nebiusKey := s.machines.PlatformNebiusAPIKey(); nebiusKey != "" {
			providerCredentials["nebius"] = nebiusKey
		}
	}

	channelCredentials := make(map[string]string)
	channelSettings := make(map[string]map[string]interface{})
	caps, err := s.store.ListMachineCapabilities(ctx, machine.ID)
	if err != nil {
		return nil, fmt.Errorf("list machine capabilities: %w", err)
	}
	for _, cap := range caps {
		if !cap.Enabled {
			continue
		}
		if cap.EntryType != "" && cap.EntryType != "channel" {
			continue
		}
		if settings := channelSettingsFromCapabilityOverrides(cap.ConfigOverrides, cap.EntryID); len(settings) > 0 {
			channelSettings[cap.EntryID] = settings
		}
		switch cap.EntryID {
		case "slack":
			for _, provider := range []string{"slack", "slack-app"} {
				if value := strings.TrimSpace(allChannelCredentials[provider]); value != "" {
					channelCredentials[provider] = value
				}
			}
		default:
			if value := strings.TrimSpace(allChannelCredentials[cap.EntryID]); value != "" {
				channelCredentials[cap.EntryID] = value
			}
		}
	}

	gatewayToken := ""
	if machine.GatewayToken != nil {
		gatewayToken = *machine.GatewayToken
	}
	vmHostname := ""
	if machine.TunnelHostname != nil {
		vmHostname = *machine.TunnelHostname
	} else if machine.Slug != "" {
		vmHostname = s.dataPlaneHostname("m", machine.Slug)
	}

	soul := ""
	if machineAgents, err := s.store.ListMachineAgents(ctx, machine.ID); err == nil {
		for _, agent := range machineAgents {
			if agent.IsDefault && agent.Soul != nil {
				soul = *agent.Soul
				break
			}
		}
	} else {
		slog.Warn("hermes.config.agents_load_failed", "machine_id", machine.ID, "error", err)
	}

	browserCDPURL := ""
	if browserVMIP, err := s.resolveBrowserVMIP(ctx, machine); err != nil {
		return nil, fmt.Errorf("resolve browser VM: %w", err)
	} else if browserVMIP != "" {
		browserCDPURL = "http://192.168.100.1:9222"
	}

	return configassembly.AssembleHermesSeedConfig(configassembly.HermesSeedParams{
		MachineID:                machine.ID,
		DefaultModel:             defaultModel,
		ProxyBaseURL:             s.proxyBaseURL,
		GatewayToken:             gatewayToken,
		VMHostname:               vmHostname,
		ProviderCredentialValues: providerCredentials,
		ChannelCredentialValues:  channelCredentials,
		ChannelSettings:          channelSettings,
		ProviderCatalog:          catalogProviders,
		BrowserCDPURL:            browserCDPURL,
		Soul:                     soul,
	})
}

func channelSettingsFromCapabilityOverrides(raw json.RawMessage, channelID string) map[string]interface{} {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" {
		return nil
	}
	var overrides map[string]interface{}
	if err := json.Unmarshal(raw, &overrides); err != nil {
		return nil
	}
	channels, ok := overrides["channels"].(map[string]interface{})
	if !ok {
		return nil
	}
	settings, ok := channels[channelID].(map[string]interface{})
	if !ok || len(settings) == 0 {
		return nil
	}
	return settings
}

func (s *Server) pushHermesConfig(ctx context.Context, machine *store.Machine, restartGateway bool) (machineConfigPushResult, error) {
	seed, err := s.assembleHermesSeedConfigForMachine(ctx, machine)
	if err != nil {
		return machineConfigPushResult{}, err
	}

	configVersion := 0
	if record, err := s.store.GetMachineConfig(ctx, machine.ID); err == nil && record != nil {
		configVersion = record.ConfigVersion
	}
	newVersion := configVersion + 1

	snapshot, err := json.Marshal(hermesAssembledSnapshot{
		Kind:       store.MachineKindHermes,
		ConfigYAML: string(seed.ConfigYAML),
		Env:        string(seed.Env),
		AuthJSON:   json.RawMessage(seed.AuthJSON),
		Soul:       string(seed.Soul),
	})
	if err != nil {
		return machineConfigPushResult{}, fmt.Errorf("marshal hermes assembled config: %w", err)
	}
	if err := s.store.SetMachineAssembledConfig(ctx, machine.ID, json.RawMessage(snapshot), newVersion); err != nil {
		return machineConfigPushResult{}, err
	}

	result := machineConfigPushResult{
		ConfigVersion: newVersion,
		LiveUpdate:    "not_running",
	}
	if machine.Status != "running" || machine.HostID == nil {
		return result, nil
	}

	host, err := s.store.GetHost(ctx, *machine.HostID)
	if err != nil {
		slog.Warn("hermes.config.get_host_failed", "machine_id", machine.ID, "host_id", *machine.HostID, "error", err)
		return result, nil
	}
	if host == nil {
		slog.Warn("hermes.config.host_not_found", "machine_id", machine.ID, "host_id", *machine.HostID)
		return result, nil
	}
	if s.agentClient == nil {
		result.LiveUpdate = "skipped"
		return result, nil
	}
	if machine.ProxyToken == nil {
		result.LiveUpdate = "failed"
		result.LiveUpdateError = "no proxy token for Hermes config push"
		slog.Warn("hermes.config.live_sync_failed", "machine_id", machine.ID, "host_id", *machine.HostID, "restart_gateway", restartGateway, "error", result.LiveUpdateError)
		return result, nil
	}

	if err := s.agentClient.HermesConfigSync(ctx, host, machine.ID, *machine.ProxyToken, restartGateway); err != nil {
		result.LiveUpdate = "failed"
		result.LiveUpdateError = hermesConfigSyncUserError(err)
		slog.Warn("hermes.config.live_sync_failed", "machine_id", machine.ID, "host_id", *machine.HostID, "restart_gateway", restartGateway, "error", err, "user_error", result.LiveUpdateError)
		return result, nil
	}
	result.LiveUpdate = "sent"

	if err := s.pushCredentialsToVM(ctx, host, machine); err != nil {
		slog.Warn("hermes.config.credential_push_failed", "machine_id", machine.ID, "error", err)
	}

	return result, nil
}

func hermesConfigSyncUserError(err error) string {
	if err == nil {
		return ""
	}
	raw := strings.TrimSpace(err.Error())
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "only openclaw commands are allowed"),
		strings.Contains(lower, "ocm-hermes-config command is not allowed"):
		return "This running Hermes VM was booted before live Hermes config sync support. Restart the machine to apply the saved configuration with the current Hermes rootfs."
	case strings.Contains(lower, "ocm-hermes-config: not found"),
		strings.Contains(lower, "executable file not found"),
		strings.Contains(lower, "no such file or directory"):
		return "This running Hermes VM is missing the Hermes config sync helper. Restart the machine to apply the saved configuration with the current Hermes rootfs."
	default:
		return raw
	}
}
