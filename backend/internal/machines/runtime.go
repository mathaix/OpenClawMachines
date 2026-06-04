package machines

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
	"github.com/mathaix/openclawmachines/backend/internal/configassembly"
	"github.com/mathaix/openclawmachines/backend/internal/fleet"
	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/progress"
	"github.com/mathaix/openclawmachines/backend/internal/routing"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/internal/tunnel"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
	"github.com/mathaix/openclawmachines/backend/pkg/version"
)

// RuntimeStore is the subset of store interfaces the RuntimeService needs.
type RuntimeStore interface {
	store.MachineRepo
	store.AccountRepo
	store.UserRepo
	store.HostRepo
	store.PlacementRepo
	store.CredentialRepo
	store.ConfigRepo
	store.RegistryRepo
	store.OperationRepo
	store.BackupRepo
	store.MachineAgentRepo
	store.ModelCatalogRepo
	store.ProviderCatalogRepo
	store.PluginCatalogRepo
	store.MachinePluginRepo
	store.ArtifactReleaseRepo
	store.BrowserVMRepo
}

// RuntimeService manages machine start/stop/delete lifecycle.
// Handlers call this service instead of orchestrating dependencies directly.
type RuntimeService struct {
	store       RuntimeStore
	placement   *fleet.PlacementService
	agentClient *agentclient.Client
	tunnelMgr   *tunnel.Manager
	routing     *routing.Service
	progress    *progress.Tracker
	secretKey   string

	rootfsDataVersion            int
	cfSSHCAPubKey                string
	proxyBaseURL                 string
	nebiusAPIKey                 string
	opikAPIURL                   string
	composioAPIURL               string
	composioProxyTokenSigner     func(machineID string) (string, error)
	enableRuntimeVersionResolver bool
	openClawManifestURI          string
	hermesManifestURI            string
	hermesRootfsManifestURI      string

	// OnRunning is called after a VM transitions to "running" status.
	// Used to trigger post-start actions like plugin reconciliation.
	// isRestart is true when the VM was restarted (had a prior host assignment).
	OnRunning func(machineID string, isRestart bool)
}

// RuntimeConfig holds configuration for the RuntimeService.
type RuntimeConfig struct {
	RootfsDataVersion int
	CfSSHCAPubKey     string
	SecretKey         string
	ProxyBaseURL      string
	NebiusAPIKey      string // platform Nebius Token Factory key
	OpikAPIURL        string // Opik tracing endpoint URL
	ComposioAPIURL    string // Backend proxy URL for Composio tools
	// ComposioProxyTokenSigner mints per-machine tokens injected into the
	// in-VM Composio plugin's config. The backend proxy validates this token
	// and uses its machine_id claim as the Composio user_id, ignoring any
	// user_id supplied by the caller (IDOR mitigation).
	ComposioProxyTokenSigner     func(machineID string) (string, error)
	EnableRuntimeVersionResolver bool
	OpenClawManifestURI          string // GCS manifest URI for openclaw artifact staging
	HermesManifestURI            string // GCS manifest URI for Hermes artifact staging
	HermesRootfsManifestURI      string // GCS manifest URI for Hermes rootfs staging
}

// NewRuntimeService creates a new RuntimeService.
func NewRuntimeService(
	s RuntimeStore,
	placement *fleet.PlacementService,
	agent *agentclient.Client,
	tmgr *tunnel.Manager,
	prog *progress.Tracker,
	cfg RuntimeConfig,
	routeSvc *routing.Service,
) *RuntimeService {
	hermesManifestURI := strings.TrimSpace(cfg.HermesManifestURI)
	hermesRootfsManifestURI := strings.TrimSpace(cfg.HermesRootfsManifestURI)

	return &RuntimeService{
		store:                        s,
		placement:                    placement,
		agentClient:                  agent,
		tunnelMgr:                    tmgr,
		routing:                      routeSvc,
		progress:                     prog,
		secretKey:                    cfg.SecretKey,
		rootfsDataVersion:            cfg.RootfsDataVersion,
		cfSSHCAPubKey:                cfg.CfSSHCAPubKey,
		proxyBaseURL:                 cfg.ProxyBaseURL,
		nebiusAPIKey:                 cfg.NebiusAPIKey,
		opikAPIURL:                   cfg.OpikAPIURL,
		composioAPIURL:               cfg.ComposioAPIURL,
		composioProxyTokenSigner:     cfg.ComposioProxyTokenSigner,
		enableRuntimeVersionResolver: cfg.EnableRuntimeVersionResolver,
		openClawManifestURI:          cfg.OpenClawManifestURI,
		hermesManifestURI:            hermesManifestURI,
		hermesRootfsManifestURI:      hermesRootfsManifestURI,
	}
}

// RuntimeVersionResolverEnabled returns true if the runtime version resolver feature flag is on.
func (rs *RuntimeService) RuntimeVersionResolverEnabled() bool {
	return rs != nil && rs.enableRuntimeVersionResolver
}

// PlatformNebiusAPIKey returns the platform Nebius API key, or "" if not configured.
func (rs *RuntimeService) PlatformNebiusAPIKey() string {
	return rs.nebiusAPIKey
}

// StartOptions configures optional behavior for Start.
type StartOptions struct {
	TargetHostID       int  // If non-zero, force placement on this specific host (admin migration)
	SkipPoll           bool // If true, skip the background poller; caller manages readiness checks.
	SkipVersionPersist bool // If true, defer resolved version persistence to the caller.
}

// Start boots a machine: decrypts secrets, fetches credentials, generates tokens,
// creates tunnel, places on host, calls agent to create VM.
func (rs *RuntimeService) Start(ctx context.Context, accountID int, machine *store.Machine, opts ...StartOptions) (host *store.Host, vmIP string, err error) {
	return rs.start(ctx, accountID, machine, "", opts...)
}

// StartWithOperation boots a machine while reusing an already-held machine operation lock.
// The caller remains responsible for completing the operation.
func (rs *RuntimeService) StartWithOperation(ctx context.Context, accountID int, machine *store.Machine, operationID string, opts ...StartOptions) (host *store.Host, vmIP string, err error) {
	return rs.start(ctx, accountID, machine, operationID, opts...)
}

// vmBootstrap accumulates state across the sequential phases of VM boot.
// Used by both Start (first boot) and UpgradeWithOperation (restart with new rootfs).
type vmBootstrap struct {
	rs        *RuntimeService
	ctx       context.Context
	machine   *store.Machine
	accountID int

	// Phase outputs
	secrets         map[string]string
	llmKeys         map[string]metadata.CredentialEntry
	runtimeIDMap    map[string]string
	ownerEmails     []string
	customProviders []metadata.CustomProviderConfig
	seedConfig      []byte
	hermesSeed      *configassembly.HermesSeedConfig
	souls           []metadata.SoulEntry

	// Internal: loaded by loadCredentials, used by buildCustomProviders.
	storeCustomProviders []store.CustomProvider
	providerCredentials  map[string]string
	channelCredentials   map[string]string
}

// loadSecrets fetches and decrypts machine secrets.
func (b *vmBootstrap) loadSecrets() error {
	secrets, err := b.rs.store.ListSecretsWithValues(b.ctx, b.machine.ID)
	if err != nil {
		return fmt.Errorf("listing secrets: %w", err)
	}
	b.secrets = make(map[string]string)
	if b.rs.secretKey != "" {
		for _, sec := range secrets {
			val, err := crypto.Decrypt(sec.EncryptedValue, b.rs.secretKey)
			if err != nil {
				slog.Error("machine.start.secret.decrypt.failed", "machine_id", b.machine.ID, "secret_key", sec.Key, "error", err)
				continue
			}
			b.secrets[sec.Key] = val
		}
	} else if len(secrets) > 0 {
		slog.Warn("machine.start.secrets.encryption.key.missing", "machine_id", b.machine.ID)
	}
	return nil
}

// loadCredentials fetches machine-scoped credentials, loads custom providers and
// search providers from the catalog, decrypts and classifies credentials into LLM keys.
func (b *vmBootstrap) loadCredentials() error {
	// Fetch and decrypt machine-scoped credentials (no account-wide fallback).
	credsToInject, err := b.rs.store.ListMachineCredentialsWithValues(b.ctx, b.machine.ID)
	if err != nil {
		return fmt.Errorf("listing machine credentials: %w", err)
	}
	// Load custom providers for this account to determine LLM vs channel classification
	customProviders, err := b.rs.store.ListCustomProviders(b.ctx, b.accountID)
	if err != nil {
		slog.Warn("machine.start.custom_providers.list_failed", "account_id", b.accountID, "error", err)
	}
	b.storeCustomProviders = customProviders
	llmProviderSet := map[string]bool{"anthropic": true, "openai": true, "google": true, "openrouter": true, "openai-codex": true}
	for _, cp := range customProviders {
		if cp.IsLLM {
			llmProviderSet[cp.Name] = true
		}
	}
	// Include search providers from catalog — their credentials are proxied the same way as LLM keys.
	// Build a runtime ID mapping so credentials are keyed by the proxy's provider name.
	b.runtimeIDMap = make(map[string]string) // credential ID → runtime provider ID
	searchProviders, catErr := b.rs.store.ListProviderCatalogByCategory(b.ctx, "search")
	if catErr != nil {
		slog.Error("machine.start.search_catalog_load_failed", "machine_id", b.machine.ID, "error", catErr)
	} else {
		for _, sp := range searchProviders {
			llmProviderSet[sp.ID] = true
			rtID := sp.ID
			if sp.RuntimeProviderID != nil && *sp.RuntimeProviderID != "" {
				rtID = *sp.RuntimeProviderID
			}
			b.runtimeIDMap[sp.ID] = rtID
		}
	}
	if len(b.runtimeIDMap) > 0 {
		slog.Info("machine.start.runtime_id_mappings", "machine_id", b.machine.ID, "mappings", b.runtimeIDMap)
	}

	b.llmKeys = make(map[string]metadata.CredentialEntry)
	b.providerCredentials = make(map[string]string)
	b.channelCredentials = make(map[string]string)
	if b.rs.secretKey != "" {
		for _, cred := range credsToInject {
			val, err := crypto.Decrypt(cred.EncryptedValue, b.rs.secretKey)
			if err != nil {
				slog.Warn("machine.start.credential.decrypt_failed", "provider", cred.Provider, "credential_id", cred.ID, "error", err)
				continue
			}
			if _, ok := configassembly.HermesChannelEnvVars[cred.Provider]; ok {
				b.channelCredentials[cred.Provider] = val
			} else {
				b.providerCredentials[cred.Provider] = val
			}
			entry := metadata.CredentialEntry{Value: val, CredentialType: cred.CredentialType, ExpiresAt: cred.ExpiresAt}
			if llmProviderSet[cred.Provider] {
				// Key by runtime provider ID so the proxy can find it.
				// e.g., credential "gemini-search" → keyed as "gemini".
				keyName := cred.Provider
				if rtID, ok := b.runtimeIDMap[cred.Provider]; ok {
					keyName = rtID
					slog.Info("machine.start.credential_remapped", "machine_id", b.machine.ID, "db_provider", cred.Provider, "runtime_id", rtID)
					b.providerCredentials[rtID] = val
				}
				b.llmKeys[keyName] = entry
			}
			// Channel keys (non-LLM) are NOT injected at boot time.
			// They are pulled on demand by the metadata server's SecretFetcher.
		}
	} else if len(credsToInject) > 0 {
		slog.Warn("machine.start.credentials.no_encryption_key", "machine_id", b.machine.ID)
	}

	// Inject platform Nebius key if configured (always available for all machines)
	if b.rs.nebiusAPIKey != "" {
		b.llmKeys["nebius"] = metadata.CredentialEntry{Value: b.rs.nebiusAPIKey, CredentialType: "api_key"}
		b.providerCredentials["nebius"] = b.rs.nebiusAPIKey
	}

	keyNames := make([]string, 0, len(b.llmKeys))
	for k := range b.llmKeys {
		keyNames = append(keyNames, k)
	}
	slog.Info("machine.start.credentials_ready", "machine_id", b.machine.ID, "count", len(b.llmKeys), "providers", keyNames)
	return nil
}

// buildCustomProviders converts custom providers to metadata config format and adds
// catalog search providers so the proxy can route requests to search APIs.
func (b *vmBootstrap) buildCustomProviders() error {
	// Convert custom providers to metadata config format (needed on both paths —
	// metadata server uses these for credential routing)
	for _, cp := range b.storeCustomProviders {
		authHeader := ""
		if cp.AuthHeader != nil {
			authHeader = *cp.AuthHeader
		}
		b.customProviders = append(b.customProviders, metadata.CustomProviderConfig{
			Name:         cp.Name,
			UpstreamHost: cp.UpstreamHost,
			Scheme:       cp.Scheme,
			AuthMethod:   cp.AuthMethod,
			AuthHeader:   authHeader,
			PathPrefix:   cp.PathPrefix,
			AllowedHosts: cp.AllowedHosts,
			IsLLM:        cp.IsLLM,
		})
	}

	// Add catalog search providers as custom providers so the proxy can route
	// requests to search APIs (e.g. Tavily, Brave Search).
	// Use RuntimeProviderID as the provider name so the proxy path matches
	// what config assembly emits (e.g. gemini-search → /gemini).
	if catalogSearchProviders, catErr := b.rs.store.ListProviderCatalogByCategory(b.ctx, "search"); catErr == nil {
		for _, sp := range catalogSearchProviders {
			authMethod := "bearer_header"
			if sp.AuthMethod != nil {
				authMethod = *sp.AuthMethod
			}
			authHeader := ""
			if sp.AuthHeader != nil {
				authHeader = *sp.AuthHeader
			}
			upstreamHost := ""
			if sp.UpstreamHost != nil {
				upstreamHost = *sp.UpstreamHost
			}
			providerName := sp.ID
			if sp.RuntimeProviderID != nil && *sp.RuntimeProviderID != "" {
				providerName = *sp.RuntimeProviderID
			}
			slog.Info("machine.start.search_provider_registered", "machine_id", b.machine.ID, "provider", providerName, "upstream_host", upstreamHost, "auth_method", authMethod)
			b.customProviders = append(b.customProviders, metadata.CustomProviderConfig{
				Name:         providerName,
				UpstreamHost: upstreamHost,
				Scheme:       sp.Scheme,
				AuthMethod:   authMethod,
				AuthHeader:   authHeader,
				PathPrefix:   sp.UpstreamPathPrefix,
				AllowedHosts: sp.AllowedHosts,
				IsLLM:        false,
			})
		}
	}
	return nil
}

// assembleSeedConfig assembles the seed config for first boot (not restart).
// Loads model catalog, builds channel configs, loads plugins, loads provider catalog,
// reads search provider override, calls AssembleSeedConfig, persists to DB, and loads souls.
func (b *vmBootstrap) assembleSeedConfig() error {
	if store.NormalizeMachineKind(b.machine.Kind) == store.MachineKindHermes {
		return b.assembleHermesSeedConfig()
	}

	// First boot: assemble seed config with exec secret provider.
	// Use BOTH runtime IDs (what the proxy knows) and catalog IDs (what the
	// assembler's resolveSearchProviderSeed matches against). Without catalog
	// IDs, remapped providers like gemini-search would never auto-detect.
	providerNameSet := make(map[string]bool, len(b.llmKeys))
	for name := range b.llmKeys {
		providerNameSet[name] = true
	}
	// Add catalog IDs for any remapped providers (e.g., gemini-search → gemini)
	for catalogID, runtimeID := range b.runtimeIDMap {
		if providerNameSet[runtimeID] {
			providerNameSet[catalogID] = true
		}
	}
	providerNames := make([]string, 0, len(providerNameSet))
	for name := range providerNameSet {
		providerNames = append(providerNames, name)
	}
	defaultModel := "openai/gpt-oss-120b"
	if preferredModel, e := b.rs.store.GetMachinePreferredModel(b.ctx, b.machine.ID); e == nil && preferredModel != "" {
		defaultModel = preferredModel
	}
	// Load model catalog for seed config assembly
	catalog, catErr := b.rs.store.ListModelCatalog(b.ctx)
	if catErr != nil {
		return fmt.Errorf("list model catalog: %w", catErr)
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
	// Build channel configs for seed: load enabled channel capabilities,
	// merge registry template + overrides, and inject decrypted tokens.
	channelConfigs := map[string]map[string]interface{}{}
	if caps, capErr := b.rs.store.ListMachineCapabilities(b.ctx, b.machine.ID); capErr == nil {
		for _, cap := range caps {
			if cap.EntryType != "channel" || !cap.Enabled {
				continue
			}
			entry, entryErr := b.rs.store.GetRegistryEntry(b.ctx, cap.EntryID)
			if entryErr != nil {
				slog.Warn("machine.start.seed.channel_entry_failed", "machine_id", b.machine.ID, "channel", cap.EntryID, "error", entryErr)
				continue
			}
			// Extract channels.<provider> from registry template
			chConf := map[string]interface{}{}
			if entry.ConfigTemplate != nil {
				var tmpl map[string]interface{}
				if err := json.Unmarshal(entry.ConfigTemplate, &tmpl); err == nil {
					if channels, ok := tmpl["channels"].(map[string]interface{}); ok {
						if ch, ok := channels[cap.EntryID].(map[string]interface{}); ok {
							chConf = ch
						}
					}
				}
			}
			// Merge capability overrides
			if cap.ConfigOverrides != nil {
				var overrides map[string]interface{}
				if err := json.Unmarshal(cap.ConfigOverrides, &overrides); err == nil {
					if channels, ok := overrides["channels"].(map[string]interface{}); ok {
						if chOv, ok := channels[cap.EntryID].(map[string]interface{}); ok {
							for k, v := range chOv {
								chConf[k] = v
							}
						}
					}
				}
			}
			// Inject exec secret refs — resolved at runtime by ocm-secrets via metadata service
			if fields, ok := configassembly.ChannelTokenFields[cap.EntryID]; ok {
				for _, field := range fields {
					chConf[field.FieldName] = map[string]interface{}{
						"source":   "exec",
						"provider": "ocm",
						"id":       fmt.Sprintf("channel-%s-%s", cap.EntryID, field.FieldName),
					}
				}
			}
			if len(chConf) > 0 {
				channelConfigs[cap.EntryID] = chConf
				slog.Info("machine.start.seed.channel", "machine_id", b.machine.ID, "channel", cap.EntryID)
			}
		}
	}

	// Load enabled plugins for seed config
	var pluginSelections []configassembly.PluginSelection
	if machinePlugins, plugErr := b.rs.store.ListMachinePlugins(b.ctx, b.machine.ID); plugErr == nil {
		for _, mp := range machinePlugins {
			slog.Debug("machine.start.seed.plugin_entry", "machine_id", b.machine.ID, "plugin_id", mp.PluginID, "enabled", mp.Enabled, "slot", mp.Slot)
			if !mp.Enabled {
				continue
			}
			ps := configassembly.PluginSelection{
				PluginID: mp.PluginID,
				Slot:     mp.Slot,
			}
			if catalogEntry, catErr := b.rs.store.GetPluginCatalogEntry(b.ctx, mp.PluginID); catErr == nil && catalogEntry.ConfigTemplate != nil {
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
	}

	// Resolve opik API key from gateway token
	var opikAPIKey string
	if b.rs.opikAPIURL != "" && b.machine.GatewayToken != nil {
		opikAPIKey = *b.machine.GatewayToken
	}
	slog.Info("machine.start.seed.opik",
		"machine_id", b.machine.ID,
		"opik_api_url", b.rs.opikAPIURL,
		"has_api_key", opikAPIKey != "",
		"plugin_count", len(pluginSelections),
	)

	// Load provider catalog for seed config
	var seedCatalogProviders []configassembly.CatalogProvider
	provCatalog, pcErr := b.rs.store.ListProviderCatalog(b.ctx)
	if pcErr != nil {
		slog.Error("machine.start.seed.provider_catalog_load_failed", "machine_id", b.machine.ID, "error", pcErr)
	} else {
		for _, e := range provCatalog {
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
			seedCatalogProviders = append(seedCatalogProviders, cp)
		}
	}

	// Read search provider override
	var seedSearchProvider string
	var seedOpenAIAgentRuntime string
	var seedModelFallbacks []string
	if machineConfig, mcErr := b.rs.store.GetMachineConfig(b.ctx, b.machine.ID); mcErr == nil && machineConfig != nil && machineConfig.PlatformOverrides != nil {
		var overrides struct {
			SearchProvider     string   `json:"search_provider"`
			OpenAIAgentRuntime string   `json:"openai_agent_runtime"`
			ModelFallbacks     []string `json:"model_fallbacks"`
		}
		if json.Unmarshal(machineConfig.PlatformOverrides, &overrides) == nil {
			seedSearchProvider = overrides.SearchProvider
			seedOpenAIAgentRuntime = overrides.OpenAIAgentRuntime
			seedModelFallbacks = overrides.ModelFallbacks
		}
	}

	var err error
	b.seedConfig, err = configassembly.AssembleSeedConfig(configassembly.SeedParams{
		MachineID:                b.machine.ID,
		DefaultModel:             defaultModel,
		ModelFallbacks:           seedModelFallbacks,
		Providers:                providerNames,
		ProxyBaseURL:             b.rs.proxyBaseURL,
		ModelCatalog:             catalogModels,
		ProviderCatalog:          seedCatalogProviders,
		SearchProvider:           seedSearchProvider,
		OpenAIAgentRuntime:       seedOpenAIAgentRuntime,
		ChannelConfigs:           channelConfigs,
		Plugins:                  pluginSelections,
		OpikAPIURL:               b.rs.opikAPIURL,
		OpikAPIKey:               opikAPIKey,
		ComposioAPIURL:           b.rs.composioAPIURL,
		ComposioProxyTokenSigner: b.rs.composioProxyTokenSigner,
	})
	if err != nil {
		return fmt.Errorf("assemble seed config: %w", err)
	}

	// Persist seed config to DB so the assembled_config blob is in sync with
	// what the VM has on disk. This ensures pluginsAllowOps and other code
	// that reads the blob has correct data from the start.
	if err := b.rs.store.SetMachineAssembledConfig(b.ctx, b.machine.ID, json.RawMessage(b.seedConfig), 1); err != nil {
		slog.Warn("machine.start.seed.persist_assembled_failed", "machine_id", b.machine.ID, "error", err)
	}

	// Fetch agent souls for cold-start delivery
	if machineAgents, err := b.rs.store.ListMachineAgents(b.ctx, b.machine.ID); err == nil {
		for _, a := range machineAgents {
			soulPath := "/home/openclaw/.openclaw/workspace-" + a.AgentID + "/SOUL.md"
			if a.IsDefault {
				soulPath = "/home/openclaw/.openclaw/workspace/SOUL.md"
			}
			content := ""
			if a.Soul != nil {
				content = *a.Soul
			}
			b.souls = append(b.souls, metadata.SoulEntry{
				AgentID: a.AgentID,
				Path:    soulPath,
				Content: content,
			})
		}
	}
	return nil
}

func (b *vmBootstrap) assembleHermesSeedConfig() error {
	defaultModel := "openai/gpt-oss-120b"
	if preferredModel, e := b.rs.store.GetMachinePreferredModel(b.ctx, b.machine.ID); e == nil && preferredModel != "" {
		defaultModel = preferredModel
	}

	provCatalog, pcErr := b.rs.store.ListProviderCatalog(b.ctx)
	if pcErr != nil {
		slog.Error("machine.start.hermes.provider_catalog_load_failed", "machine_id", b.machine.ID, "error", pcErr)
	}
	var seedCatalogProviders []configassembly.CatalogProvider
	for _, e := range provCatalog {
		cp := configassembly.CatalogProvider{
			ID:                 e.ID,
			Category:           e.Category,
			AutodetectOrder:    e.AutodetectOrder,
			UpstreamPathPrefix: e.UpstreamPathPrefix,
		}
		if e.EnvVar != nil {
			cp.EnvVar = *e.EnvVar
		}
		if e.UpstreamHost != nil {
			cp.UpstreamHost = *e.UpstreamHost
		}
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
		seedCatalogProviders = append(seedCatalogProviders, cp)
	}

	channelValues := make(map[string]string)
	channelSettings := make(map[string]map[string]interface{})
	if caps, capErr := b.rs.store.ListMachineCapabilities(b.ctx, b.machine.ID); capErr == nil {
		for _, cap := range caps {
			if cap.EntryType != "channel" || !cap.Enabled {
				continue
			}
			if settings := hermesChannelSettingsFromCapabilityOverrides(cap.ConfigOverrides, cap.EntryID); len(settings) > 0 {
				channelSettings[cap.EntryID] = settings
			}
			switch cap.EntryID {
			case "slack":
				for _, provider := range []string{"slack", "slack-app"} {
					if value := strings.TrimSpace(b.channelCredentials[provider]); value != "" {
						channelValues[provider] = value
					}
				}
			default:
				if value := strings.TrimSpace(b.channelCredentials[cap.EntryID]); value != "" {
					channelValues[cap.EntryID] = value
				}
			}
		}
	}

	gatewayToken := ""
	if b.machine.GatewayToken != nil {
		gatewayToken = *b.machine.GatewayToken
	}
	vmHostname := ""
	if b.machine.TunnelHostname != nil {
		vmHostname = *b.machine.TunnelHostname
	}
	soul := ""
	if machineAgents, err := b.rs.store.ListMachineAgents(b.ctx, b.machine.ID); err == nil {
		for _, a := range machineAgents {
			if a.IsDefault && a.Soul != nil {
				soul = *a.Soul
				break
			}
		}
	}

	seed, err := configassembly.AssembleHermesSeedConfig(configassembly.HermesSeedParams{
		MachineID:                b.machine.ID,
		DefaultModel:             defaultModel,
		ProxyBaseURL:             b.rs.proxyBaseURL,
		GatewayToken:             gatewayToken,
		VMHostname:               vmHostname,
		ProviderCredentialValues: b.providerCredentials,
		ChannelCredentialValues:  channelValues,
		ChannelSettings:          channelSettings,
		ProviderCatalog:          seedCatalogProviders,
		Soul:                     soul,
	})
	if err != nil {
		return fmt.Errorf("assemble hermes seed config: %w", err)
	}
	b.hermesSeed = seed
	return nil
}

func hermesChannelSettingsFromCapabilityOverrides(raw json.RawMessage, channelID string) map[string]interface{} {
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

// loadOwnerEmails looks up owner emails for SSH cert validation.
func (b *vmBootstrap) loadOwnerEmails() {
	if members, err := b.rs.store.ListAccountMembers(b.ctx, b.accountID); err == nil {
		for _, m := range members {
			if u, err := b.rs.store.GetUser(b.ctx, m.UserID); err == nil {
				b.ownerEmails = append(b.ownerEmails, u.Email)
			}
		}
	}
}

func (rs *RuntimeService) start(ctx context.Context, accountID int, machine *store.Machine, callerOperationID string, opts ...StartOptions) (host *store.Host, vmIP string, err error) {
	var startOpts StartOptions
	if len(opts) > 0 {
		startOpts = opts[0]
	}
	managesOperation := callerOperationID == ""
	var op *store.MachineOperation
	if managesOperation {
		// Check for in-flight operation
		existingOp, opErr := rs.store.GetActiveMachineOperation(ctx, machine.ID)
		if opErr == nil && existingOp != nil {
			return nil, "", fmt.Errorf("machine has in-flight %s operation (id=%s)", existingOp.Kind, existingOp.ID)
		}

		// Create operation record
		op = &store.MachineOperation{MachineID: machine.ID, Kind: "start"}
		if createErr := rs.store.CreateMachineOperation(ctx, op); createErr != nil {
			return nil, "", fmt.Errorf("failed to create start operation: %w", createErr)
		}
	} else {
		var opErr error
		op, opErr = rs.requireActiveOperation(ctx, machine.ID, callerOperationID)
		if opErr != nil {
			return nil, "", opErr
		}
	}

	// Ensure operation is completed on exit if the poller was never started.
	// Once the poller starts, it owns operation completion for all terminal states.
	pollerStarted := false
	defer func() {
		if managesOperation && !pollerStarted && err != nil {
			errMsg := err.Error()
			if err := rs.store.CompleteMachineOperation(context.Background(), op.ID, "failed", &errMsg); err != nil {
				slog.Error("machine.operation.complete.failed", "operation_id", op.ID, "error", err)
			}
		}
	}()

	b := &vmBootstrap{rs: rs, ctx: ctx, machine: machine, accountID: accountID}

	if err := b.loadSecrets(); err != nil {
		return nil, "", err
	}
	if err := b.loadCredentials(); err != nil {
		return nil, "", err
	}

	// Generate tokens if not set:
	// - GatewayToken: used for OpenClaw gateway authentication
	// - ProxyToken: used for browser/terminal/logs proxy authentication (separate per-machine token)
	if machine.GatewayToken == nil || machine.ProxyToken == nil {
		gatewayToken := machine.GatewayToken
		proxyToken := machine.ProxyToken

		if gatewayToken == nil {
			tokenBytes := make([]byte, 16)
			if _, err := rand.Read(tokenBytes); err != nil {
				return nil, "", fmt.Errorf("failed to generate gateway token: %w", err)
			}
			token := hex.EncodeToString(tokenBytes)
			gatewayToken = &token
		}

		if proxyToken == nil {
			tokenBytes := make([]byte, 16)
			if _, err := rand.Read(tokenBytes); err != nil {
				return nil, "", fmt.Errorf("failed to generate proxy token: %w", err)
			}
			token := hex.EncodeToString(tokenBytes)
			proxyToken = &token
		}

		if err := rs.store.SetMachineTokens(ctx, machine.ID, *gatewayToken, *proxyToken); err != nil {
			return nil, "", fmt.Errorf("failed to store tokens: %w", err)
		}
		machine.GatewayToken = gatewayToken
		machine.ProxyToken = proxyToken
	}

	// Provision routing (tunnel, DNS, signing key) via RouteService
	if rs.routing != nil {
		result, err := rs.routing.SetupRoute(ctx, routing.SetupRequest{
			MachineID:   machine.ID,
			MachineSlug: machine.Slug,
		})
		if err != nil {
			slog.Error("machine.start.route.setup.failed", "machine_id", machine.ID, "error", err)
		} else {
			machine.TunnelToken = result.TunnelToken
			if result.TunnelID != "" {
				machine.TunnelID = &result.TunnelID
				machine.SigningKey = &result.SigningKey
			}
			if result.VMHostname != "" {
				machine.TunnelHostname = &result.VMHostname
			}
		}
	}

	// Build placement request from machine spec.
	// DiskMB: rootfs (~3 GB) + data volume. If DataVolumeGB is set, use it; otherwise
	// the placement service falls back to DefaultDiskPerVM.
	var diskMB int
	if machine.DataVolumeGB > 0 {
		diskMB = (machine.DataVolumeGB + 3) * 1024
	}
	placementReq := fleet.PlacementRequest{
		VCPUs:        machine.VCPUs,
		MemoryMB:     machine.MemoryMB,
		DiskMB:       diskMB,
		OperationID:  op.ID,
		TargetHostID: startOpts.TargetHostID,
	}
	if machine.PreferredRegion != nil {
		placementReq.Region = *machine.PreferredRegion
	}
	slog.Info("machine.start.input_versions",
		"machine_id", machine.ID,
		"desired_rootfs_version", firstNonEmptyPtr(machine.DesiredRootfsVersion),
		"resolved_rootfs_version", firstNonEmptyPtr(machine.ResolvedRootfsVersion),
		"actual_rootfs_version", firstNonEmptyPtr(machine.ActualRootfsVersion),
		"rootfs_snapshot", firstNonEmptyPtr(machine.RootfsSnapshot),
		"desired_openclaw_version", firstNonEmptyPtr(machine.DesiredOpenclawVersion),
		"resolved_openclaw_version", firstNonEmptyPtr(machine.ResolvedOpenclawVersion),
		"actual_openclaw_version", firstNonEmptyPtr(machine.ActualOpenclawVersion),
		"openclaw_version", firstNonEmptyPtr(machine.OpenclawVersion),
		"desired_channel", firstNonEmptyPtr(machine.DesiredChannel),
		"version_source", firstNonEmptyPtr(machine.VersionSource),
	)
	machineKind := store.NormalizeMachineKind(machine.Kind)
	var runtimeSelection *metadata.RuntimeSelection
	if rs.enableRuntimeVersionResolver || machineKind == store.MachineKindHermes {
		runtimeSelection = rs.resolveRuntimeSelection(ctx, machine, latestArtifactVersion(ctx, rs.store, rootfsArtifactKind(machineKind), "stable"))
		if runtimeSelection.ResolvedRootfsVersion != "" {
			placementReq.ExpectedImage = runtimeSelection.ResolvedRootfsVersion
		}
		slog.Info("machine.start.runtime_resolved",
			"machine_id", machine.ID,
			"kind", runtimeSelection.Kind,
			"resolved_rootfs_version", runtimeSelection.ResolvedRootfsVersion,
			"resolved_openclaw_version", runtimeSelection.ResolvedOpenClawVersion,
			"resolved_hermes_version", runtimeSelection.ResolvedHermesVersion,
			"version_source", runtimeSelection.VersionSource,
			"runtime_source", runtimeSelection.RuntimeSource,
		)
	}

	var placement *store.Placement

	provisioned := machine.ProvisioningCompletedAt != nil

	requireMigration := func(reason string) error {
		msg := "migration required: " + reason
		_ = rs.store.UpdateMachineStatus(ctx, machine.ID, "error", &msg)
		slog.Warn("machine.start.migration_required", "machine_id", machine.ID, "reason", reason)
		return errors.New(msg)
	}

	// Targeted placement (admin migration) bypasses affinity logic
	if startOpts.TargetHostID != 0 {
		placement, host, vmIP, err = rs.placement.Reserve(ctx, machine.ID, placementReq)
		if err != nil {
			return nil, "", fmt.Errorf("failed to place on target host %d: %w", startOpts.TargetHostID, err)
		}
		slog.Info("machine.start.targeted_placement", "machine_id", machine.ID, "host_id", host.ID, "vm_ip", vmIP)
	} else if machine.HostID != nil {
		// Host affinity: if machine has a host assignment (data volume exists), restart on same host
		// vm_ip may be nil if the machine was stopped (IP released) — RecoverAffinity assigns a fresh one.
		// Restart path: check if previous host is still alive
		prevHost, hostErr := rs.store.GetHost(ctx, *machine.HostID)
		if hostErr != nil {
			return nil, "", fmt.Errorf("failed to look up home host %d: %w", *machine.HostID, hostErr)
		} else if prevHost.Status == "terminated" {
			// Host was marked terminated (likely due to heartbeat loss) — require migration instead
			return nil, "", requireMigration(fmt.Sprintf("home host %d terminated", *machine.HostID))
		} else if prevHost.Status == "unreachable" || prevHost.Status == "error" || prevHost.Status == "draining" {
			return nil, "", requireMigration(fmt.Sprintf("home host %d is %s", *machine.HostID, prevHost.Status))
		} else {
			// Normal restart on same host — use affinity recovery
			placement, host, vmIP, err = rs.placement.RecoverAffinity(ctx, machine.ID, placementReq)
			if err != nil {
				return nil, "", err
			}
			slog.Info("machine.start.host_affinity", "machine_id", machine.ID, "host_id", host.ID, "vm_ip", vmIP)
		}
	} else {
		if provisioned {
			return nil, "", requireMigration("no host assigned to provisioned machine")
		}
		// Fresh placement: first-time start
		placement, host, vmIP, err = rs.placement.Reserve(ctx, machine.ID, placementReq)
		if err != nil {
			return nil, "", err
		}
	}

	// Auto-unpair browser VM if it's on a different host than where this
	// machine landed. Best-effort cleanup of firewall rules and CDP target
	// on the OLD host (where the browser VM still is), since the DB unpair
	// would otherwise orphan those iptables/metaserver entries.
	//
	// Critical policy: only unpair on an AUTHORITATIVE signal.
	//
	//   - pgx.ErrNoRows / (nil, nil): the row is gone. Treat as an
	//     authoritative "clear the stale pointer" signal (the FK is
	//     ON DELETE SET NULL, but a reconciliation race can briefly
	//     leave the pointer after the row was dropped).
	//   - any other error: transient DB failure. Surface it and let
	//     the caller retry — the pairing is still intact in the DB,
	//     so we must not silently drop it from a single flaky call.
	//   - row found with HostID != host.ID: real cross-host placement.
	if machine.BrowserVMID != nil {
		bvm, bvmErr := rs.store.GetBrowserVM(ctx, *machine.BrowserVMID)
		if bvmErr != nil && !errors.Is(bvmErr, pgx.ErrNoRows) {
			return nil, "", fmt.Errorf("machine.start: look up paired browser VM %s: %w", *machine.BrowserVMID, bvmErr)
		}

		hostMismatch := bvm == nil || bvm.HostID == nil || *bvm.HostID != host.ID
		if hostMismatch {
			slog.Info("machine.start.auto_unpair", "machine_id", machine.ID,
				"browser_vm_id", *machine.BrowserVMID, "reason", "host_mismatch")

			// Clean up agent state on the old host (where the browser VM
			// currently lives). Failure here is logged but doesn't block the
			// machine start — the host may be unreachable, and leaving a
			// rule in place on a dead host is acceptable.
			if bvm != nil && bvm.HostID != nil && rs.agentClient != nil {
				if oldHost, hostErr := rs.store.GetHost(ctx, *bvm.HostID); hostErr == nil && oldHost != nil {
					if machine.VMIP != nil && bvm.VMIP != nil {
						if err := rs.agentClient.UnpairBrowserVM(ctx, oldHost, bvm.ID, *machine.VMIP); err != nil {
							slog.Warn("machine.start.auto_unpair.firewall_cleanup_failed",
								"machine_id", machine.ID, "old_host_id", oldHost.ID, "error", err)
						}
					}
					if err := rs.agentClient.ResetCDPTarget(ctx, oldHost, machine.ID); err != nil {
						slog.Warn("machine.start.auto_unpair.cdp_reset_failed",
							"machine_id", machine.ID, "old_host_id", oldHost.ID, "error", err)
					}
				}
			}

			// DB unpair failure is NOT best-effort — if we log
			// "auto_unpair" but the DB still says the pair is intact,
			// the next start will run through this branch again and
			// the config assembler will happily serve browser config
			// pointing at a browser VM on the wrong host. Surface the
			// failure so the caller retries.
			if unpairErr := rs.store.UnpairBrowserVM(ctx, machine.ID); unpairErr != nil {
				return nil, "", fmt.Errorf("machine.start: auto-unpair browser VM %s: %w", *machine.BrowserVMID, unpairErr)
			}
		}
	}

	b.loadOwnerEmails()

	// Detect restart: machine has been placed on a host before and has a data volume
	// with existing config. On restart, skip seed config assembly and soul fetching —
	// the data volume already has openclaw.json and SOUL.md files from the first boot.
	// Config updates happen via pushConfigToRunningMachine after the VM is running.
	isRestart := machine.HostID != nil

	// Update status: "starting" for restarts (has data volume), "provisioning" for first boot.
	// Placement sets "provisioning" by default; override to "starting" for restarts so the
	// UI shows the correct phase.
	if isRestart {
		if err := rs.store.UpdateMachineStatus(ctx, machine.ID, "starting", nil); err != nil {
			slog.Warn("machine.start.status_update_failed", "machine_id", machine.ID, "error", err)
		}
	}

	if isRestart {
		slog.Info("machine.start.restart", "machine_id", machine.ID, "host_id", *machine.HostID,
			"note", "skipping seed config and souls — data volume has existing config")
	} else {
		if err := b.assembleSeedConfig(); err != nil {
			return nil, "", err
		}
	}

	if err := b.buildCustomProviders(); err != nil {
		return nil, "", err
	}

	// Call Host Agent to create Firecracker MicroVM
	if rs.agentClient != nil {
		// Pre-cleanup: destroy any stale VM resources (TAP devices, sockets) from previous runs
		if err := rs.agentClient.DestroyVM(ctx, host, machine.ID); err != nil {
			slog.Warn("machine.start.pre_cleanup.failed", "machine_id", machine.ID, "host_id", host.ID, "error", err)
		}

		var hermesConfigYAML, hermesEnv, hermesAuthJSON, hermesSoul []byte
		if b.hermesSeed != nil {
			hermesConfigYAML = b.hermesSeed.ConfigYAML
			hermesEnv = b.hermesSeed.Env
			hermesAuthJSON = b.hermesSeed.AuthJSON
			hermesSoul = b.hermesSeed.Soul
		}
		_, err := rs.agentClient.CreateVM(ctx, host, machine, vmIP, b.secrets, b.llmKeys, rs.rootfsDataVersion, b.ownerEmails, rs.cfSSHCAPubKey, b.seedConfig, hermesConfigYAML, hermesEnv, hermesAuthJSON, hermesSoul, b.customProviders, b.souls, runtimeSelection)
		if err != nil {
			slog.Error("machine.start.vm.create.failed", "machine_id", machine.ID, "error", err)
			releaseMode := fleet.ReleaseHard
			if isRestart {
				// Preserve host affinity and the data volume for restart/update failures.
				releaseMode = fleet.ReleaseSoft
			}
			_ = rs.placement.Release(ctx, machine.ID, releaseMode)
			// Clean up tunnel only for first boot placement failures. Restarts preserve the
			// existing route because the machine is expected to retry on the same host.
			if !isRestart && rs.tunnelMgr != nil && machine.TunnelID != nil && rs.routing != nil {
				vmHostname, sshHostname := rs.routing.Hostnames(machine.Slug)
				_ = rs.tunnelMgr.DeleteTunnelAndDNS(ctx, *machine.TunnelID, vmHostname, sshHostname)
				_ = rs.store.ClearMachineTunnel(ctx, machine.ID)
			}
			return nil, "", fmt.Errorf("failed to create VM: %w", err)
		}
		slog.Info("machine.start.vm.created", "machine_id", machine.ID, "host_id", host.ID, "vm_ip", vmIP)

		// Record version info
		snapshot := ""
		if runtimeSelection != nil && runtimeSelection.ResolvedRootfsVersion != "" {
			snapshot = runtimeSelection.ResolvedRootfsVersion
		} else if host.SourceImage != nil {
			snapshot = *host.SourceImage
		}
		resolvedRuntimeVersion := version.Version
		if runtimeSelection != nil {
			if runtimeSelection.Kind == store.MachineKindHermes && runtimeSelection.ResolvedHermesVersion != "" {
				resolvedRuntimeVersion = runtimeSelection.ResolvedHermesVersion
			} else if runtimeSelection.ResolvedOpenClawVersion != "" {
				resolvedRuntimeVersion = runtimeSelection.ResolvedOpenClawVersion
			}
		}
		if !startOpts.SkipVersionPersist {
			if err := rs.store.UpdateMachineVersion(ctx, machine.ID, snapshot, resolvedRuntimeVersion); err != nil {
				slog.Warn("machine.start.version.update.failed", "machine_id", machine.ID, "error", err)
			}
		}

		if startOpts.SkipPoll {
			if managesOperation {
				if err := rs.store.CompleteMachineOperation(context.Background(), op.ID, "completed", nil); err != nil {
					slog.Error("machine.operation.complete.failed", "operation_id", op.ID, "error", err)
				}
			}
			pollerStarted = true
			slog.Info("machine.start.skip_poll", "machine_id", machine.ID, "host_id", host.ID)
		} else {
			// Poll agent in background until VM is running, then update DB.
			// The poller now owns completing the operation for all terminal states
			// only when Start created the lifecycle operation itself.
			placementID := ""
			if placement != nil {
				placementID = placement.ID
			}
			pollOperationID := ""
			if managesOperation {
				pollOperationID = op.ID
				pollerStarted = true
			}
			hostCopy := *host
			go rs.pollVMStatus(&hostCopy, machine.ID, pollOperationID, placementID, isRestart)
		}
	} else {
		slog.Info("machine.start.placed.no.agent", "machine_id", machine.ID, "host_id", host.ID, "vm_ip", vmIP)
	}

	return host, vmIP, nil
}

// Stop gracefully stops a machine: calls agent, releases capacity, cleans routes/tunnels.
func (rs *RuntimeService) Stop(ctx context.Context, machine *store.Machine) (err error) {
	return rs.stop(ctx, machine, "")
}

func latestArtifactVersion(ctx context.Context, store RuntimeStore, kind, channel string) string {
	if store == nil {
		return ""
	}
	latest, err := store.GetLatestArtifactRelease(ctx, kind, channel)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(latest.Version)
}

func rootfsArtifactKind(machineKind string) string {
	if store.NormalizeMachineKind(machineKind) == store.MachineKindHermes {
		return "hermes-rootfs"
	}
	return "rootfs"
}

func runtimeArtifactKind(machineKind string) string {
	if store.NormalizeMachineKind(machineKind) == store.MachineKindHermes {
		return "hermes"
	}
	return "openclaw"
}

func (rs *RuntimeService) resolveRuntimeSelection(ctx context.Context, machine *store.Machine, rootfsDefault string) *metadata.RuntimeSelection {
	machineKind := store.NormalizeMachineKind(machine.Kind)
	defaultRuntimeVersion := ""
	if latest, err := rs.store.GetLatestArtifactRelease(ctx, runtimeArtifactKind(machineKind), "stable"); err == nil {
		defaultRuntimeVersion = latest.Version
	}
	resolution := ResolveMachineRuntime(machine, RuntimeDefaults{
		DefaultRootfsVersion:   rootfsDefault,
		DefaultOpenClawVersion: defaultRuntimeVersion,
		MachineKind:            machineKind,
	})
	selection := resolution.MetadataSelection()
	selection.Kind = machineKind
	switch machineKind {
	case store.MachineKindHermes:
		selection.RootfsManifestURI = rs.hermesRootfsManifestURI
		selection.HermesManifestURI = rs.hermesManifestURI
	default:
		selection.OpenClawManifestURI = rs.openClawManifestURI
	}
	return selection
}

// StopWithOperation stops a machine while reusing an already-held machine operation lock.
// The caller remains responsible for completing the operation.
func (rs *RuntimeService) StopWithOperation(ctx context.Context, machine *store.Machine, operationID string) (err error) {
	return rs.stop(ctx, machine, operationID)
}

func (rs *RuntimeService) stop(ctx context.Context, machine *store.Machine, callerOperationID string) (err error) {
	managesOperation := callerOperationID == ""
	var op *store.MachineOperation

	// Check for in-flight operation
	if managesOperation {
		existingOp, opErr := rs.store.GetActiveMachineOperation(ctx, machine.ID)
		if opErr == nil && existingOp != nil {
			if existingOp.Kind == "stop" {
				// Ensure machine is actually stopped — previous stop may have failed to update status
				if machine.Status != "stopped" {
					_ = rs.store.UpdateMachineStatus(ctx, machine.ID, "stopped", nil)
				}
				return nil // idempotent
			}
			return fmt.Errorf("machine has in-flight %s operation (id=%s)", existingOp.Kind, existingOp.ID)
		}

		// Create operation record
		op = &store.MachineOperation{MachineID: machine.ID, Kind: "stop"}
		if createErr := rs.store.CreateMachineOperation(ctx, op); createErr != nil {
			return fmt.Errorf("failed to create stop operation: %w", createErr)
		}
	} else {
		var opErr error
		op, opErr = rs.requireActiveOperation(ctx, machine.ID, callerOperationID)
		if opErr != nil {
			return opErr
		}
	}

	// Complete operation on exit
	defer func() {
		if !managesOperation {
			return
		}
		state := "completed"
		var errMsg *string
		if err != nil {
			state = "failed"
			msg := err.Error()
			errMsg = &msg
		}
		if err := rs.store.CompleteMachineOperation(context.Background(), op.ID, state, errMsg); err != nil {
			slog.Error("machine.operation.complete.failed", "operation_id", op.ID, "error", err)
		}
	}()

	// Call Host Agent to stop (not destroy) Firecracker MicroVM, preserving data volume
	if rs.agentClient != nil && machine.HostID != nil {
		host, err := rs.store.GetHost(ctx, *machine.HostID)
		if err == nil {
			if stopErr := rs.agentClient.StopVM(ctx, host, machine.ID); stopErr != nil {
				slog.Error("machine.stop.vm.stop.failed", "machine_id", machine.ID, "error", stopErr)
				// If the host is reachable, the VM may still be running — don't release capacity
				if host.Status != "unreachable" && host.Status != "terminated" {
					return fmt.Errorf("failed to stop VM (host reachable): %w", stopErr)
				}
				// Host is unreachable/terminated — continue cleanup since we can't contact the agent
				slog.Warn("machine.stop.host.unreachable.continuing.cleanup", "machine_id", machine.ID, "host_status", host.Status)
			}
		}
	}

	// Soft release: free capacity but keep host_id and vm_ip for data volume persistence
	if err := rs.placement.Release(ctx, machine.ID, fleet.ReleaseSoft); err != nil {
		return fmt.Errorf("failed to release machine: %w", err)
	}

	if rs.routing != nil {
		account, acctErr := rs.store.GetAccount(ctx, machine.AccountID)
		accountSlug := ""
		if acctErr != nil {
			slog.Error("machine.stop.account_lookup_failed", "machine_id", machine.ID, "account_id", machine.AccountID, "error", acctErr)
		} else if account != nil {
			accountSlug = account.Slug
		}
		tunnelID := ""
		if machine.TunnelID != nil {
			tunnelID = *machine.TunnelID
		}
		_ = rs.routing.TeardownRoute(ctx, routing.TeardownRequest{
			MachineID:   machine.ID,
			MachineSlug: machine.Slug,
			AccountSlug: accountSlug,
			TunnelID:    tunnelID,
		})
	}

	// Explicitly set status to stopped — placement.Release may skip this when host_id is NULL
	if err := rs.store.UpdateMachineStatus(ctx, machine.ID, "stopped", nil); err != nil {
		return fmt.Errorf("failed to set machine status to stopped: %w", err)
	}

	return nil
}

// StopForUpdate stops a machine's VM and releases capacity but preserves tunnels, DNS, and KV
// routes. Used during host agent updates where the machine will restart on the same host.
// The caller is responsible for coordination (e.g., holding an operation lock if needed).
func (rs *RuntimeService) StopForUpdate(ctx context.Context, machine *store.Machine) error {
	// Call Host Agent to stop the Firecracker MicroVM (preserves data volume)
	if rs.agentClient != nil && machine.HostID != nil {
		host, err := rs.store.GetHost(ctx, *machine.HostID)
		if err != nil {
			slog.Warn("machine.stop_for_update.host.lookup.failed", "machine_id", machine.ID, "host_id", *machine.HostID, "error", err)
			return fmt.Errorf("failed to look up host for VM stop: %w", err)
		}
		if stopErr := rs.agentClient.StopVM(ctx, host, machine.ID); stopErr != nil {
			slog.Error("machine.stop_for_update.vm.stop.failed", "machine_id", machine.ID, "error", stopErr)
			if host.Status != "unreachable" && host.Status != "terminated" {
				return fmt.Errorf("failed to stop VM (host reachable): %w", stopErr)
			}
			slog.Warn("machine.stop_for_update.host.unreachable.continuing", "machine_id", machine.ID, "host_status", host.Status)
		}
	}

	// Soft release: free capacity but keep host_id and vm_ip for data volume persistence
	if err := rs.placement.Release(ctx, machine.ID, fleet.ReleaseSoft); err != nil {
		return fmt.Errorf("failed to release machine: %w", err)
	}

	// Set status to stopped — skip KV route, tunnel, and DNS cleanup (machine returns to same host)
	if err := rs.store.UpdateMachineStatus(ctx, machine.ID, "stopped", nil); err != nil {
		return fmt.Errorf("failed to set machine status to stopped: %w", err)
	}

	return nil
}

// UpgradeWithOperation recreates a running machine on its current host while preserving
// the data volume. The caller must already hold the machine operation lock.
func (rs *RuntimeService) UpgradeWithOperation(ctx context.Context, accountID int, machine *store.Machine, operationID string) (*store.Host, string, error) {
	if machine.HostID == nil {
		return nil, "", fmt.Errorf("machine has no host assignment")
	}
	if machine.VMIP == nil || *machine.VMIP == "" {
		return nil, "", fmt.Errorf("machine has no VM IP assignment")
	}
	if _, err := rs.requireActiveOperation(ctx, machine.ID, operationID); err != nil {
		return nil, "", err
	}

	host, err := rs.store.GetHost(ctx, *machine.HostID)
	if err != nil {
		return nil, "", fmt.Errorf("look up host: %w", err)
	}
	if host.Status == "terminated" || host.Status == "unreachable" || host.Status == "error" || host.Status == "draining" {
		return nil, "", fmt.Errorf("host %d is %s", host.ID, host.Status)
	}

	b := &vmBootstrap{rs: rs, ctx: ctx, machine: machine, accountID: accountID}

	if err := b.loadSecrets(); err != nil {
		return nil, "", err
	}
	if err := b.loadCredentials(); err != nil {
		return nil, "", err
	}

	// Tokens should already exist for provisioned machines, but keep the same guard as Start().
	if machine.GatewayToken == nil || machine.ProxyToken == nil {
		gatewayToken := machine.GatewayToken
		proxyToken := machine.ProxyToken
		if gatewayToken == nil {
			tokenBytes := make([]byte, 16)
			if _, err := rand.Read(tokenBytes); err != nil {
				return nil, "", fmt.Errorf("failed to generate gateway token: %w", err)
			}
			token := hex.EncodeToString(tokenBytes)
			gatewayToken = &token
		}
		if proxyToken == nil {
			tokenBytes := make([]byte, 16)
			if _, err := rand.Read(tokenBytes); err != nil {
				return nil, "", fmt.Errorf("failed to generate proxy token: %w", err)
			}
			token := hex.EncodeToString(tokenBytes)
			proxyToken = &token
		}
		if err := rs.store.SetMachineTokens(ctx, machine.ID, *gatewayToken, *proxyToken); err != nil {
			return nil, "", fmt.Errorf("failed to store tokens: %w", err)
		}
		machine.GatewayToken = gatewayToken
		machine.ProxyToken = proxyToken
	}

	// Tunnel connector tokens are transient and are not persisted in the DB.
	// Recreate route credentials for in-place upgrades so the replacement VM
	// can start cloudflared after the old VM is torn down.
	if rs.routing != nil && machine.TunnelToken == "" {
		result, err := rs.routing.SetupRoute(ctx, routing.SetupRequest{
			MachineID:   machine.ID,
			MachineSlug: machine.Slug,
		})
		if err != nil {
			slog.Error("machine.upgrade.route.setup.failed", "machine_id", machine.ID, "error", err)
		} else {
			machine.TunnelToken = result.TunnelToken
			if result.TunnelID != "" {
				machine.TunnelID = &result.TunnelID
				machine.SigningKey = &result.SigningKey
			}
			if result.VMHostname != "" {
				machine.TunnelHostname = &result.VMHostname
			}
		}
	}

	b.loadOwnerEmails()

	if err := b.buildCustomProviders(); err != nil {
		return nil, "", err
	}

	var runtimeSelection *metadata.RuntimeSelection
	machineKind := store.NormalizeMachineKind(machine.Kind)
	if rs.enableRuntimeVersionResolver || machineKind == store.MachineKindHermes {
		rootfsDefault := firstNonEmptyPtr(machine.ActualRootfsVersion, machine.ResolvedRootfsVersion, machine.RootfsSnapshot)
		if rootfsDefault == "" {
			rootfsDefault = latestArtifactVersion(ctx, rs.store, rootfsArtifactKind(machineKind), "stable")
		}
		runtimeSelection = rs.resolveRuntimeSelection(ctx, machine, rootfsDefault)
		slog.Info("machine.upgrade.runtime_resolved",
			"machine_id", machine.ID,
			"kind", runtimeSelection.Kind,
			"resolved_rootfs_version", runtimeSelection.ResolvedRootfsVersion,
			"resolved_openclaw_version", runtimeSelection.ResolvedOpenClawVersion,
			"resolved_hermes_version", runtimeSelection.ResolvedHermesVersion,
			"version_source", runtimeSelection.VersionSource,
			"runtime_source", runtimeSelection.RuntimeSource,
		)
	}

	previousStatus := machine.Status
	if err := rs.store.UpdateMachineStatus(ctx, machine.ID, "starting", nil); err != nil {
		slog.Warn("machine.upgrade.status_update_failed", "machine_id", machine.ID, "error", err)
	}

	if rs.agentClient != nil {
		if _, err := rs.agentClient.UpgradeVM(ctx, host, machine, *machine.VMIP, b.secrets, b.llmKeys, rs.rootfsDataVersion, b.ownerEmails, rs.cfSSHCAPubKey, nil, nil, nil, nil, nil, b.customProviders, nil, runtimeSelection); err != nil {
			slog.Error("machine.upgrade.vm.recreate.failed", "machine_id", machine.ID, "error", err)
			// Restore previous status so the machine isn't stuck in "starting"
			if restoreErr := rs.store.UpdateMachineStatus(ctx, machine.ID, previousStatus, nil); restoreErr != nil {
				slog.Error("machine.upgrade.status_restore_failed", "machine_id", machine.ID, "previous_status", previousStatus, "error", restoreErr)
			}
			return nil, "", fmt.Errorf("failed to upgrade VM: %w", err)
		}
		slog.Info("machine.upgrade.vm.recreate.started", "machine_id", machine.ID, "host_id", host.ID, "vm_ip", *machine.VMIP)
	}

	return host, *machine.VMIP, nil
}

func (rs *RuntimeService) requireActiveOperation(ctx context.Context, machineID, operationID string) (*store.MachineOperation, error) {
	op, err := rs.store.GetActiveMachineOperation(ctx, machineID)
	if err != nil {
		return nil, fmt.Errorf("failed to get active operation for machine %s: %w", machineID, err)
	}
	if op == nil {
		return nil, fmt.Errorf("machine %s has no active operation (expected %s)", machineID, operationID)
	}
	if op.ID != operationID {
		return nil, fmt.Errorf("machine %s active operation is %s (%s), expected %s", machineID, op.Kind, op.ID, operationID)
	}
	return op, nil
}

// Delete destroys a machine: calls agent, releases capacity, cleans everything, deletes record.
func (rs *RuntimeService) Delete(ctx context.Context, machine *store.Machine) (err error) {
	// Check for in-flight operation — allow delete to supersede a completed stop
	// that hasn't been marked completed yet (race between stop response and DB commit).
	existingOp, opErr := rs.store.GetActiveMachineOperation(ctx, machine.ID)
	if opErr == nil && existingOp != nil {
		if existingOp.Kind == "stop" && machine.Status == "stopped" {
			// Force-complete the stale stop operation so delete can proceed
			_ = rs.store.CompleteMachineOperation(ctx, existingOp.ID, "completed", nil)
		} else {
			return fmt.Errorf("machine has in-flight %s operation (id=%s)", existingOp.Kind, existingOp.ID)
		}
	}

	// Create operation record
	op := &store.MachineOperation{MachineID: machine.ID, Kind: "delete"}
	if createErr := rs.store.CreateMachineOperation(ctx, op); createErr != nil {
		return fmt.Errorf("failed to create delete operation: %w", createErr)
	}

	// Complete operation on exit
	defer func() {
		state := "completed"
		var errMsg *string
		if err != nil {
			state = "failed"
			msg := err.Error()
			errMsg = &msg
		}
		if err := rs.store.CompleteMachineOperation(context.Background(), op.ID, state, errMsg); err != nil {
			slog.Error("machine.operation.complete.failed", "operation_id", op.ID, "error", err)
		}
	}()

	// Release host capacity and destroy VM if machine is assigned to a host
	if machine.HostID != nil {
		if rs.agentClient != nil {
			host, hostErr := rs.store.GetHost(ctx, *machine.HostID)
			if hostErr == nil {
				if destroyErr := rs.agentClient.DestroyVM(ctx, host, machine.ID); destroyErr != nil {
					slog.Error("machine.delete.vm.destroy.failed", "machine_id", machine.ID, "error", destroyErr)
					// If the host is reachable, the VM may still be running — don't release capacity
					if host.Status != "unreachable" && host.Status != "terminated" {
						return fmt.Errorf("failed to destroy VM (host reachable): %w", destroyErr)
					}
					// Host is unreachable/terminated — continue cleanup
					slog.Warn("machine.delete.host.unreachable.continuing.cleanup", "machine_id", machine.ID, "host_status", host.Status)
				}
			}
		}
		if err := rs.placement.Release(ctx, machine.ID, fleet.ReleaseHard); err != nil {
			slog.Error("machine.delete.capacity.release.failed", "machine_id", machine.ID, "error", err)
		}
	}

	if rs.routing != nil {
		account, acctErr := rs.store.GetAccount(ctx, machine.AccountID)
		accountSlug := ""
		if acctErr != nil {
			slog.Error("machine.delete.account_lookup_failed", "machine_id", machine.ID, "account_id", machine.AccountID, "error", acctErr)
		} else if account != nil {
			accountSlug = account.Slug
		}
		tunnelID := ""
		if machine.TunnelID != nil {
			tunnelID = *machine.TunnelID
		}
		_ = rs.routing.TeardownRoute(ctx, routing.TeardownRequest{
			MachineID:   machine.ID,
			MachineSlug: machine.Slug,
			AccountSlug: accountSlug,
			TunnelID:    tunnelID,
		})
	}

	// Clean up GCS backup objects before DeleteMachine triggers CASCADE on backup records
	if machine.HostID != nil && rs.agentClient != nil {
		if host, hostErr := rs.store.GetHost(ctx, *machine.HostID); hostErr == nil {
			backups, listErr := rs.store.ListBackupRecords(ctx, machine.ID)
			if listErr == nil {
				for _, b := range backups {
					if delErr := rs.agentClient.DeleteBackupObject(ctx, host, machine.ID, b.GCSPath); delErr != nil {
						slog.Warn("machine.delete.backup_object.failed", "machine_id", machine.ID, "gcs_path", b.GCSPath, "error", delErr)
					}
				}
			}
		}
	}

	if err := rs.store.DeleteMachine(ctx, machine.ID); err != nil {
		return err
	}

	return nil
}

// pollVMStatus polls the agent until the VM reaches "running" or "error" status,
// then updates the machine record in the database accordingly.
// It owns completing the operation (operationID) on every terminal path.
func (rs *RuntimeService) pollVMStatus(host *store.Host, machineID string, operationID string, placementID string, isRestart bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	completeOperation := func(ctx context.Context, state string, errMsg *string) {
		if operationID == "" {
			return
		}
		if err := rs.store.CompleteMachineOperation(ctx, operationID, state, errMsg); err != nil {
			slog.Error("machine.operation.complete.failed", "operation_id", operationID, "error", err)
		}
	}

	// releaseMachine is a helper to release capacity on failure paths.
	releaseMachine := func() {
		releaseMode := fleet.ReleaseHard
		if isRestart {
			releaseMode = fleet.ReleaseSoft
		}
		if err := rs.placement.Release(context.Background(), machineID, releaseMode); err != nil {
			slog.Error("vm.poll.release.failed", "machine_id", machineID, "error", err)
		}
	}

	// cleanupTunnel removes the per-VM tunnel, DNS, and KV route if one was created.
	cleanupTunnel := func() {
		if isRestart {
			return
		}
		if rs.routing == nil {
			return
		}
		machine, err := rs.store.GetMachine(context.Background(), machineID)
		if err != nil {
			return
		}
		account, acctErr := rs.store.GetAccount(context.Background(), machine.AccountID)
		accountSlug := ""
		if acctErr != nil {
			slog.Error("vm.poll.account_lookup_failed", "machine_id", machineID, "account_id", machine.AccountID, "error", acctErr)
		} else if account != nil {
			accountSlug = account.Slug
		}
		tunnelID := ""
		if machine.TunnelID != nil {
			tunnelID = *machine.TunnelID
		}
		if err := rs.routing.TeardownRoute(context.Background(), routing.TeardownRequest{
			MachineID:   machine.ID,
			MachineSlug: machine.Slug,
			AccountSlug: accountSlug,
			TunnelID:    tunnelID,
		}); err != nil {
			slog.Error("vm.poll.tunnel.cleanup.failed", "machine_id", machineID, "error", err)
		}
	}

	var lastErr error
	var consecutiveErrors int

	for {
		select {
		case <-ctx.Done():
			slog.Error("vm.poll.timeout", "machine_id", machineID)
			msg := "timeout waiting for VM to become running"
			if lastErr != nil {
				msg = fmt.Sprintf("timeout: last error was: %v", lastErr)
			}
			if err := rs.store.UpdateMachineStatus(ctx, machineID, "error", &msg); err != nil {
				slog.Error("machine.status.update.failed", "machine_id", machineID, "status", "error", "error", err)
			}
			completeOperation(context.Background(), "failed", &msg)
			releaseMachine()
			cleanupTunnel()
			return
		case <-time.After(3 * time.Second):
			vm, err := rs.agentClient.GetVM(ctx, host, machineID)
			if err != nil {
				lastErr = err
				consecutiveErrors++
				slog.Error("vm.poll.error", "machine_id", machineID, "attempt", consecutiveErrors, "error", err)
				// After 30 consecutive errors (~90s), give up and mark as error
				if consecutiveErrors >= 30 {
					msg := fmt.Sprintf("failed to poll VM status: %v", err)
					if err := rs.store.UpdateMachineStatus(ctx, machineID, "error", &msg); err != nil {
						slog.Error("machine.status.update.failed", "machine_id", machineID, "status", "error", "error", err)
					}
					completeOperation(ctx, "failed", &msg)
					releaseMachine()
					cleanupTunnel()
					return
				}
				continue
			}
			consecutiveErrors = 0 // reset on success

			switch vm.Status {
			case "running":
				slog.Info("vm.poll.running", "machine_id", machineID)
				if rs.routing != nil {
					machine, err := rs.store.GetMachine(ctx, machineID)
					if err == nil && host.TunnelURL != nil {
						account, err := rs.store.GetAccount(ctx, machine.AccountID)
						if err == nil {
							proxyToken := ""
							if machine.ProxyToken != nil {
								proxyToken = *machine.ProxyToken
							}
							if syncErr := rs.routing.SyncRouteToKV(ctx, routing.SyncKVRequest{
								AccountSlug:  account.Slug,
								MachineSlug:  machine.Slug,
								MachineID:    machine.ID,
								HostHostname: *host.TunnelURL,
								ProxyToken:   proxyToken,
							}); syncErr != nil {
								slog.Error("machine.start.kv_sync.failed", "machine_id", machineID, "error", syncErr)
								msg := fmt.Sprintf("KV sync failed: %v", syncErr)
								if err := rs.store.UpdateMachineStatus(ctx, machineID, "error", &msg); err != nil {
									slog.Error("machine.status.update.failed", "machine_id", machineID, "status", "error", "error", err)
								}
								completeOperation(ctx, "failed", &msg)
								// VM is actually running — stop it and release capacity
								if rs.agentClient != nil {
									if stopErr := rs.agentClient.StopVM(context.Background(), host, machineID); stopErr != nil {
										slog.Error("vm.poll.kv_fail.stop.failed", "machine_id", machineID, "error", stopErr)
									}
								}
								releaseMachine()
								cleanupTunnel()
								return
							}
						}
					}
				}
				if placementID != "" {
					_ = rs.placement.Activate(ctx, placementID)
				}
				if err := rs.store.UpdateMachineStatus(ctx, machineID, "running", nil); err != nil {
					slog.Error("machine.status.update.failed", "machine_id", machineID, "status", "running", "error", err)
				}
				completeOperation(ctx, "completed", nil)
				if rs.OnRunning != nil {
					go rs.OnRunning(machineID, isRestart)
				}
				return
			case "error":
				slog.Error("vm.poll.failed", "machine_id", machineID, "error_message", vm.ErrorMessage)
				msg := vm.ErrorMessage
				if msg == "" {
					msg = "VM creation failed"
				}
				if err := rs.store.UpdateMachineStatus(ctx, machineID, "error", &msg); err != nil {
					slog.Error("machine.status.update.failed", "machine_id", machineID, "status", "error", "error", err)
				}
				completeOperation(ctx, "failed", &msg)
				releaseMachine()
				cleanupTunnel()
				return
			case "stopped":
				slog.Warn("vm.poll.stopped.unexpectedly", "machine_id", machineID)
				msg := "VM stopped unexpectedly during creation"
				if err := rs.store.UpdateMachineStatus(ctx, machineID, "stopped", &msg); err != nil {
					slog.Error("machine.status.update.failed", "machine_id", machineID, "status", "stopped", "error", err)
				}
				completeOperation(ctx, "failed", &msg)
				releaseMachine()
				cleanupTunnel()
				return
			}
			// Still creating/booting — keep polling
		}
	}
}

// CleanupMachineRouteAndTunnel removes the KV route and per-VM tunnel for a machine.
// Used by the reconciler to clean up machines on dead hosts.
func (rs *RuntimeService) CleanupMachineRouteAndTunnel(ctx context.Context, machineID string) error {
	machine, err := rs.store.GetMachine(ctx, machineID)
	if err != nil {
		return fmt.Errorf("get machine: %w", err)
	}

	if rs.routing != nil {
		account, acctErr := rs.store.GetAccount(ctx, machine.AccountID)
		accountSlug := ""
		if acctErr != nil {
			slog.Error("cleanup.account_lookup_failed", "machine_id", machineID, "account_id", machine.AccountID, "error", acctErr)
		} else if account != nil {
			accountSlug = account.Slug
		}
		tunnelID := ""
		if machine.TunnelID != nil {
			tunnelID = *machine.TunnelID
		}
		if err := rs.routing.TeardownRoute(ctx, routing.TeardownRequest{
			MachineID:   machine.ID,
			MachineSlug: machine.Slug,
			AccountSlug: accountSlug,
			TunnelID:    tunnelID,
		}); err != nil {
			slog.Warn("cleanup.route.failed", "machine_id", machineID, "error", err)
		}
	}

	return nil
}
