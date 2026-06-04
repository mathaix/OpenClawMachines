package configassembly

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
)

// CatalogProvider represents a provider from the provider_catalog database table.
// Passed in via AssemblyParams so the assembler doesn't need DB access.
type CatalogProvider struct {
	ID                 string
	Category           string // "ai", "search", "automation"
	UpstreamHost       string
	UpstreamPathPrefix string
	AuthMethod         string
	AuthHeader         string
	EnvVar             string
	ExecSecretID       string // default: id + "-key"
	RuntimeProviderID  string // default: id
	PluginID           string // default: id
	AutodetectOrder    *int   // lower = higher priority, nil = no autodetect
}

// CatalogModel represents a model from the model_catalog database table.
// Passed in via AssemblyParams so the assembler doesn't need DB access.
type CatalogModel struct {
	ID             string
	Label          string
	Source         string // "platform", "byok", "subscription"
	Tier           *string
	GatewayModelID *string
	Provider       string
}

// AvailableCatalogIDs returns the set of catalog model IDs available for a machine
// given its configured providers. Platform models are always included. Subscription
// and BYOK models are included only when the machine has a credential for the provider.
func AvailableCatalogIDs(catalog []CatalogModel, providers map[string]bool) map[string]bool {
	ids := make(map[string]bool)
	for _, cm := range catalog {
		switch cm.Source {
		case "platform":
			ids[cm.ID] = true
		case "subscription":
			if providers[cm.Provider] {
				ids[cm.ID] = true
			}
		case "byok":
			if providers[cm.Provider] {
				ids[cm.ID] = true
			}
		}
	}
	return ids
}

// mapModelFromCatalog maps a user-facing model ID to its gateway model ID using the catalog.
// Returns the input unchanged if no mapping exists (BYOK models pass through).
func mapModelFromCatalog(catalog []CatalogModel, modelID string) string {
	for _, m := range catalog {
		if m.ID == modelID && m.GatewayModelID != nil {
			return *m.GatewayModelID
		}
	}
	return modelID
}

// NormalizeOpenAIAgentRuntime validates the user-facing OpenAI agent runtime
// override. Empty/"auto" means no explicit policy, letting OpenClaw choose.
func NormalizeOpenAIAgentRuntime(runtime string) (string, error) {
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	switch runtime {
	case "", "auto":
		return "", nil
	case "codex", "pi":
		return runtime, nil
	default:
		return "", fmt.Errorf("unsupported OpenAI agent runtime %q", runtime)
	}
}

func agentRuntimeForModelRef(modelRef, openAIAgentRuntime string) string {
	if strings.HasPrefix(modelRef, "codex/") {
		return "codex"
	}
	if openAIAgentRuntime != "" && strings.HasPrefix(modelRef, "openai/") {
		return openAIAgentRuntime
	}
	return ""
}

func agentModelCatalogEntry(modelRef, openAIAgentRuntime string) map[string]interface{} {
	entry := map[string]interface{}{}
	if runtime := agentRuntimeForModelRef(modelRef, openAIAgentRuntime); runtime != "" {
		entry["agentRuntime"] = map[string]interface{}{"id": runtime}
	}
	return entry
}

func putAgentModelCatalogEntry(models map[string]interface{}, modelRef, openAIAgentRuntime string) {
	if existing, ok := models[modelRef].(map[string]interface{}); ok {
		if runtime := agentRuntimeForModelRef(modelRef, openAIAgentRuntime); runtime != "" {
			existing["agentRuntime"] = map[string]interface{}{"id": runtime}
		}
		return
	}
	models[modelRef] = agentModelCatalogEntry(modelRef, openAIAgentRuntime)
}

// buildNebiusModelsFromCatalog returns model entries for the Nebius provider config.
func buildNebiusModelsFromCatalog(catalog []CatalogModel) []interface{} {
	models := make([]interface{}, 0)
	for _, m := range catalog {
		if m.GatewayModelID == nil || m.Source != "platform" {
			continue
		}
		// Strip "nebius/" prefix to get the Nebius model ID
		nebiusID := strings.TrimPrefix(*m.GatewayModelID, "nebius/")
		isSmart := m.Tier != nil && *m.Tier == "smart"
		models = append(models, map[string]interface{}{
			"id":            nebiusID,
			"name":          m.Label,
			"api":           "openai-completions",
			"reasoning":     isSmart,
			"input":         []string{"text"},
			"contextWindow": 131072,
			"maxTokens":     8192,
		})
	}
	return models
}

// buildOpenRouterModelsFromCatalog returns model entries for the OpenRouter provider config.
// OpenRouter is not a built-in gateway provider, so models must be listed explicitly
// (same as Nebius). It uses the OpenAI-compatible chat completions API.
func buildOpenRouterModelsFromCatalog(catalog []CatalogModel) []interface{} {
	models := make([]interface{}, 0)
	for _, m := range catalog {
		if m.Provider != "openrouter" || m.Source != "byok" {
			continue
		}
		// Strip "openrouter/" prefix to get the bare model ID
		bareID := m.ID
		if i := strings.Index(bareID, "/"); i >= 0 {
			bareID = bareID[i+1:]
		}
		models = append(models, map[string]interface{}{
			"id":            bareID,
			"name":          m.Label,
			"api":           "openai-completions",
			"input":         []string{"text"},
			"contextWindow": 131072,
			"maxTokens":     8192,
		})
	}
	return models
}

// buildCodexProviderConfig returns the provider config block for openai-codex.
func buildCodexProviderConfig(catalog []CatalogModel) map[string]interface{} {
	return map[string]interface{}{
		"baseUrl": "https://chatgpt.com/backend-api",
		"api":     "openai-codex-responses",
		"models":  buildCodexModelsFromCatalog(catalog),
	}
}

// buildCodexModelsFromCatalog returns model entries for the openai-codex provider config.
// The gateway's built-in openai-codex plugin resolves models dynamically, but explicit
// entries ensure the gateway knows which models are available without relying solely on
// the plugin's resolveDynamicModel callback.
func buildCodexModelsFromCatalog(catalog []CatalogModel) []interface{} {
	models := make([]interface{}, 0)
	for _, m := range catalog {
		if m.Provider != "openai-codex" || m.Source != "subscription" {
			continue
		}
		// Strip "openai-codex/" prefix to get the bare model ID
		bareID := m.ID
		if i := strings.Index(bareID, "/"); i >= 0 {
			bareID = bareID[i+1:]
		}
		entry := map[string]interface{}{
			"id":            bareID,
			"name":          m.Label,
			"api":           "openai-codex-responses",
			"reasoning":     true,
			"input":         []string{"text"},
			"contextWindow": 1050000,
			"maxTokens":     128000,
		}
		models = append(models, entry)
	}
	return models
}

// ChannelTokenMapping maps a config field name to its credential provider.
type ChannelTokenMapping struct {
	FieldName string // config field name in openclaw.json (e.g. "botToken")
	Provider  string // credential provider name in DB (e.g. "slack")
}

// ChannelTokenFields maps channel IDs to their token field configurations.
// Channels not in this map (e.g. WhatsApp) use session-based auth.
var ChannelTokenFields = map[string][]ChannelTokenMapping{
	"telegram": {{FieldName: "botToken", Provider: "telegram"}},
	"discord":  {{FieldName: "token", Provider: "discord"}},
	"slack": {
		{FieldName: "botToken", Provider: "slack"},
		{FieldName: "appToken", Provider: "slack-app"},
	},
}

// ChannelTokenFieldName is a backward-compatible accessor returning the primary
// token field name for a channel provider. Used by callsites not yet migrated.
// Deprecated: use ChannelTokenFields and ProviderToChannel instead.
var ChannelTokenFieldName = map[string]string{
	"telegram": "botToken",
	"discord":  "token",
	"slack":    "botToken",
}

// ProviderToChannel maps a credential provider name back to its channel ID and field name.
// Used by agent_auth.go and machine_config.go to build correct secret IDs.
func ProviderToChannel(provider string) (channelID, fieldName string, ok bool) {
	for chID, mappings := range ChannelTokenFields {
		for _, m := range mappings {
			if m.Provider == provider {
				return chID, m.FieldName, true
			}
		}
	}
	return "", "", false
}

// platformDefaults are the base configuration values that every machine gets.
// These are not user-changeable and represent platform-level settings.
var platformDefaults = map[string]interface{}{
	"gateway": map[string]interface{}{
		// mode: "local" tells the gateway to accept local connections without
		// requiring gateway setup. Required for OCM_CONFIG_SOURCE=metadata.
		"mode": "local",
		"auth": map[string]interface{}{
			// Auth mode "token" — gateway validates the shared token from
			// OPENCLAW_GATEWAY_TOKEN env var. This makes sharedAuthOk=true for
			// any client that provides the token, which enables
			// roleCanSkipDeviceIdentity and bypasses device pairing entirely.
			//
			// Why not "none": mode "none" returns method="none" which never
			// satisfies sharedAuthOk (requires method="token"|"password").
			// Without sharedAuthOk, clients must go through device pairing.
			// Silent local pairing should auto-approve, but it's fragile —
			// depends on matching state dirs, isLocalClient detection, and no
			// stale pairing state. Token auth is simpler and deterministic.
			//
			// The token is set in init-openclaw.sh for both the gateway process
			// and PTY server env, so TUI/CLI pick it up automatically.
			"mode": "token",
		},
		// port and bind are set via CLI flags in init-openclaw.sh, not in the
		// config file, because the gateway's Zod schema may not accept them.
		"controlUi": map[string]interface{}{
			"enabled":           true,
			"allowInsecureAuth": true,
			// dangerouslyDisableDeviceAuth: the gateway reads auth tokens ONLY
			// from the WebSocket connect JSON message, NOT from HTTP headers.
			// The browser doesn't have OPENCLAW_GATEWAY_TOKEN, so it can't
			// authenticate via token auth. This flag makes evaluateMissingDeviceIdentity
			// return "allow-bypass" for Control UI clients, skipping both device
			// identity and shared auth checks. Security is enforced at the auth
			// proxy layer (machine JWT validation via Cloudflare Access).
			"dangerouslyDisableDeviceAuth": true,
			// allowedOrigins is injected dynamically with the VM hostname
			// in AssembleConfig (see step 5b).
		},
		"reload": map[string]interface{}{
			"mode": "hot",
		},
		// trustedProxies intentionally NOT set. The gateway binds to loopback
		// (--bind loopback) so ALL connections are genuinely local. Setting
		// trustedProxies=["127.0.0.1"] causes resolveClientIp to "fail closed"
		// when there's no X-Forwarded-For header — returning undefined instead
		// of 127.0.0.1. This makes isLocalClient=false for TUI connections,
		// which breaks shouldAllowSilentLocalPairing and causes "pairing
		// required" errors. Auth is enforced at the auth proxy layer (machine
		// JWT via Cloudflare Access), not at the gateway IP level.
		"nodes": map[string]interface{}{
			"denyCommands": []interface{}{
				"camera.snap", "camera.clip", "screen.record",
				"calendar.add", "contacts.add", "reminders.add",
			},
		},
		// OpenAI-compatible chat completions endpoint. Lets external tools
		// (Open WebUI, SDKs, `ocm machines serve chat`) talk to the machine
		// via the standard POST /v1/chat/completions shape. Auth is enforced
		// upstream (machine JWT at auth proxy, gateway token at the gateway).
		"http": map[string]interface{}{
			"endpoints": map[string]interface{}{
				"chatCompletions": map[string]interface{}{
					"enabled": true,
				},
			},
		},
	},
	"skills": map[string]interface{}{
		"allowBundled": []interface{}{},
	},
	"commands": map[string]interface{}{
		"native":       "auto",
		"nativeSkills": "auto",
		"restart":      true,
		"ownerDisplay": "raw",
	},
	"session": map[string]interface{}{
		"dmScope": "per-channel-peer",
		// threadBindings keeps conversation state scoped to the channel thread
		// (Slack/Discord) so a bot stays coherent across a multi-turn thread
		// without leaking context into sibling threads.
		"threadBindings": map[string]interface{}{
			"enabled": true,
		},
	},
	// Cron is the agent scheduler. Defaulted on so monitoring bots, daily
	// reports, and scheduled pulls work without per-account opt-in.
	// Intentionally NOT in ProtectedConfigKeys — users may override with
	// cron.enabled=false if they don't want scheduled work on their machine.
	"cron": map[string]interface{}{
		"enabled": true,
	},
	// Let the gateway inherit shell env vars. Our init-openclaw.sh already
	// exports provider URLs and identity via /etc/profile.d/*.sh and the
	// PTY-server env block, so enabling this makes those visible to gateway
	// plugins without per-field duplication in config. Intentionally NOT
	// in ProtectedConfigKeys — users may override with env.shellEnv.enabled=false
	// if they don't want shell env exposed to plugins.
	"env": map[string]interface{}{
		"shellEnv": map[string]interface{}{
			"enabled": true,
		},
	},
}

// ProtectedConfigKeys are top-level keys that user overrides must not touch.
// These are set by platform defaults, identity, proxy injection, or skills assembly.
var ProtectedConfigKeys = map[string]bool{
	"gateway":  true,
	"skills":   true,
	"ui":       true,
	"server":   true,
	"reload":   true,
	"agents":   true,
	"commands": true,
	"session":  true,
	"plugins":  true,
}

// protectedPrefixes are path prefixes that must be blocked at any nesting depth.
// A key path like "channels.telegram.auth.mode" would be checked against these.
var protectedPrefixes = []string{
	"gateway.",
	"skills.",
	"ui.",
	"server.",
	"reload.",
	"agents.",
	"commands.",
	"session.",
	"plugins.",
}

// StripProtectedKeys returns a copy of overrides with protected keys removed.
// This checks both top-level keys and nested paths to prevent overrides from
// setting protected values through nested maps (e.g., channels.*.gateway.auth.mode).
func StripProtectedKeys(overrides map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(overrides))
	var strippedKeys []string
	for k, v := range overrides {
		if ProtectedConfigKeys[k] {
			strippedKeys = append(strippedKeys, k)
			continue
		}
		// Recursively strip protected paths from nested maps
		if m, ok := v.(map[string]interface{}); ok {
			stripped := stripProtectedPaths(m, k+".")
			if len(stripped) > 0 {
				result[k] = stripped
			}
		} else {
			result[k] = v
		}
	}
	if len(strippedKeys) > 0 {
		slog.Warn("config.assembly.protected_keys_stripped", "stripped_keys", strippedKeys)
	}
	return result
}

// stripProtectedPaths recursively removes any nested keys whose full path
// starts with a protected prefix. For example, if pathPrefix is "channels.telegram."
// and the map contains "gateway" -> {...}, the full path "channels.telegram.gateway."
// matches the "gateway." prefix and is stripped.
func stripProtectedPaths(m map[string]interface{}, pathPrefix string) map[string]interface{} {
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		fullPath := pathPrefix + k
		// Check if this key itself is a protected top-level name
		if ProtectedConfigKeys[k] {
			continue
		}
		// Check if the full dotted path matches any protected prefix
		blocked := false
		for _, prefix := range protectedPrefixes {
			if len(fullPath) >= len(prefix) && fullPath[:len(prefix)] == prefix {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		// Recurse into nested maps
		if nested, ok := v.(map[string]interface{}); ok {
			stripped := stripProtectedPaths(nested, fullPath+".")
			if len(stripped) > 0 {
				result[k] = stripped
			}
		} else {
			result[k] = v
		}
	}
	return result
}

// FindProtectedKeyViolations walks overrides and returns dotted paths that would
// touch protected config areas. Checks both top-level ProtectedConfigKeys and
// nested protectedPrefixes to match the stripping logic in StripProtectedKeys.
func FindProtectedKeyViolations(m map[string]interface{}, prefix string) []string {
	var violations []string
	for k, v := range m {
		fullPath := k
		if prefix != "" {
			fullPath = prefix + "." + k
		}
		if ProtectedConfigKeys[k] {
			violations = append(violations, fullPath)
			continue
		}
		// Check if the full dotted path matches any protected prefix
		blocked := false
		for _, pp := range protectedPrefixes {
			if len(fullPath) >= len(pp) && fullPath[:len(pp)] == pp {
				violations = append(violations, fullPath)
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		if nested, ok := v.(map[string]interface{}); ok {
			violations = append(violations, FindProtectedKeyViolations(nested, fullPath)...)
		}
	}
	return violations
}

// AgentDefinition represents an OpenClaw persona for config assembly.
type AgentDefinition struct {
	AgentID        string
	Name           string
	Model          string // empty = inherit default
	IdentityEmoji  string
	IdentityAvatar string
	IsDefault      bool
	SortOrder      int
}

// PluginSelection represents an enabled plugin for config assembly.
type PluginSelection struct {
	PluginID        string
	Slot            string
	ConfigTemplate  map[string]interface{}
	ConfigOverrides map[string]interface{}
}

// AssemblyParams contains all inputs needed to assemble a machine's OpenClaw config.
type AssemblyParams struct {
	MachineID               string
	Capabilities            []CapabilityWithTemplate
	Identity                *Identity
	Credentials             map[string]string // provider name -> "present" (just need to know which exist)
	ChannelCredentialValues map[string]string // channel provider name -> decrypted token value
	ProxyBaseURL            string            // e.g. "http://169.254.169.253:4000"
	NativeMode              bool              // true = exec secret refs + /v1 baseUrls (native config mode)
	DefaultModel            string            // e.g. "anthropic/claude-sonnet-4-6"
	ModelFallbacks          []string          // ordered fallback models; platform mapping applied
	VMHostname              string            // e.g. "m-mybot.openclawmachines.com" — per-VM tunnel hostname
	AccountHostname         string            // e.g. "user-23.openclawmachines.com" — account subdomain for browser access
	BrowserVMIP             string            // companion browser VM IP (empty = no browser VM)
	BridgeIP                string            // bridge gateway IP for CDP proxy (e.g. "192.168.100.1")
	Agents                  []AgentDefinition // optional agent personas
	Plugins                 []PluginSelection // optional enabled plugins
	ModelCatalog            []CatalogModel    // models from the model_catalog DB table
	ProviderCatalog         []CatalogProvider // providers from the provider_catalog DB table
	SearchProvider          string            // explicit override, or "" for auto-detect
	OpenAIAgentRuntime      string            // "", "auto", "codex", or "pi"; non-auto is emitted for openai/* models
	OpikAPIURL              string            // Base URL for Opik-compatible API
	OpikAPIKey              string            // API key for Opik plugin authentication (gateway token)
	ComposioAPIURL          string            // Backend proxy URL for Composio (e.g. https://api.openclawmachines.com/api/composio)
	// ComposioProxyTokenSigner mints a short-lived per-machine token the in-VM
	// Composio plugin presents to the backend proxy as a Bearer token. The
	// backend uses the token's machine_id claim as the Composio user_id,
	// ignoring any user_id supplied by the caller. nil = skip (back-compat
	// for tests/dev; production callers must always set this).
	ComposioProxyTokenSigner func(machineID string) (string, error)
	RuntimeSource            string // Always "artifact" — plugins are bundled in the artifact.
}

// CapabilityWithTemplate pairs a machine capability with its registry entry's config template.
type CapabilityWithTemplate struct {
	EntryID         string
	EntryType       string                 // "channel", "skill", "tool"
	ConfigTemplate  map[string]interface{} // parsed from registry entry
	ConfigOverrides map[string]interface{} // parsed from machine capability
	Mode            *string
}

// Identity holds the assistant identity configuration.
type Identity struct {
	Name   *string
	Avatar *string
}

// AssembleConfig produces a complete OpenClaw config JSON from the given params.
//
// Assembly steps:
//  1. Start with a deep copy of platform defaults
//  2. For each enabled capability, deep-merge its ConfigTemplate
//  3. For each capability with ConfigOverrides, deep-merge those on top
//  4. If Identity is set, inject ui.assistant.name and ui.assistant.avatar
//  5. For channel capabilities, inject proxy base URLs where credentials exist
//  6. Build skills.allowBundled from enabled skill capabilities
//  7. Marshal to JSON bytes
func AssembleConfig(params AssemblyParams) ([]byte, error) {
	openAIAgentRuntime, err := NormalizeOpenAIAgentRuntime(params.OpenAIAgentRuntime)
	if err != nil {
		return nil, err
	}

	// 1. Start with deep copy of platform defaults
	result := deepCopy(platformDefaults)

	// 2-3. Merge each capability's template, then overrides
	for _, cap := range params.Capabilities {
		if cap.ConfigTemplate != nil {
			result = deepMerge(result, cap.ConfigTemplate)
		}
		if cap.ConfigOverrides != nil {
			sanitized := StripProtectedKeys(cap.ConfigOverrides)
			if len(sanitized) > 0 {
				result = deepMerge(result, sanitized)
			}
		}
	}

	// 3b. Inject browser CDP URL when a browser VM is paired.
	// Merges into existing browser config (from capability template in step 3a)
	// rather than replacing it, preserving any plugin-specific settings.
	// Uses the bridge gateway IP (192.168.100.1:9222) where the agent's CDP proxy
	// listens. The proxy resolves source VM IP → browser target and forwards TCP.
	browserCDPHost := params.BridgeIP
	if browserCDPHost == "" {
		browserCDPHost = params.BrowserVMIP
	}
	if browserCDPHost != "" {
		browser := getOrCreateMap(result, "browser")
		enabled, _ := browser["enabled"].(bool)
		// If a browser VM is paired, treat browser as enabled even without
		// the capability toggle (pairing implies browser is wanted).
		if !enabled && params.BrowserVMIP != "" {
			enabled = true
			browser["enabled"] = true
		}
		if enabled {
			browser["cdpUrl"] = fmt.Sprintf("http://%s:9222", browserCDPHost)
			browser["attachOnly"] = true
			browser["noSandbox"] = true
			// headless=false so Chromium runs under Xvfb for Neko WebRTC capture.
			browser["headless"] = false
			// OpenClaw v2026.4.14+ enforces a fail-closed SSRF policy on the
			// CDP endpoint by default. The bridge gateway IP is RFC1918 and
			// is not treated as loopback, so the default guard blocks the
			// cdpUrl host. Allowlist it explicitly so the guard lets the
			// managed CDP control plane through.
			ssrfPolicy := getOrCreateMap(browser, "ssrfPolicy")
			hosts := []interface{}{browserCDPHost}
			if existing, ok := ssrfPolicy["allowedHostnames"].([]interface{}); ok {
				for _, h := range existing {
					if s, ok := h.(string); ok && s != browserCDPHost {
						hosts = append(hosts, s)
					}
				}
			}
			ssrfPolicy["allowedHostnames"] = hosts
			browser["ssrfPolicy"] = ssrfPolicy
			result["browser"] = browser
		}
	}

	// 4. Identity
	if params.Identity != nil {
		ui := getOrCreateMap(result, "ui")
		assistant := getOrCreateMap(ui, "assistant")
		if params.Identity.Name != nil {
			assistant["name"] = *params.Identity.Name
		}
		if params.Identity.Avatar != nil {
			assistant["avatar"] = *params.Identity.Avatar
		}
		ui["assistant"] = assistant
		result["ui"] = ui
	}

	// 5. Inject LLM provider base URLs pointing to the API proxy.
	// The gateway reads models.providers.{name}.baseUrl to route LLM API calls.
	// By setting this to the proxy URL, the gateway sends requests through the
	// proxy which injects the real API key and forwards upstream.
	var searchPluginAllows []string
	if params.ProxyBaseURL != "" {
		providerConfigs := map[string]interface{}{}
		for provider := range params.Credentials {
			if !isProxiedProvider(params.ProviderCatalog, provider) {
				continue
			}
			if isSearchProvider(params.ProviderCatalog, provider) {
				continue
			}
			if provider == "nebius" {
				continue // nebius handled below
			}
			// Use runtime provider ID as the config key — the gateway looks up
			// providers by this name. For most providers it matches the credential
			// ID, but some (e.g. gemini-search → gemini) are remapped.
			rtID := runtimeProviderID(params.ProviderCatalog, provider)
			provCfg := buildProviderConfig(rtID, params.ProxyBaseURL, params.NativeMode, params.ProviderCatalog)
			// Set the gateway API format for each provider. Built-in providers
			// (anthropic, openai, google) may infer it, but being explicit avoids
			// ambiguity — especially for google which fails without it.
			switch provider {
			case "anthropic":
				provCfg["api"] = "anthropic-messages"
			case "openai":
				provCfg["api"] = "openai-completions"
			case "google":
				provCfg["api"] = "openai-completions"
			case "openrouter":
				provCfg["api"] = "openai-completions"
				provCfg["models"] = buildOpenRouterModelsFromCatalog(params.ModelCatalog)
			}
			providerConfigs[rtID] = provCfg
		}
		// Always inject nebius proxy URL for platform models (available to all machines).
		// Nebius uses OpenAI-compatible API, so we set api: "openai-completions" and
		// explicitly list models — the gateway has no built-in nebius provider.
		if _, exists := providerConfigs["nebius"]; !exists {
			nebiusCfg := buildProviderConfig("nebius", params.ProxyBaseURL, params.NativeMode, params.ProviderCatalog)
			nebiusCfg["api"] = "openai-completions"
			nebiusCfg["models"] = buildNebiusModelsFromCatalog(params.ModelCatalog)
			providerConfigs["nebius"] = nebiusCfg
		}
		// When a codex credential flag exists in the DB (set by frontend after OAuth),
		// inject the provider config and auth.profiles so the gateway uses native OAuth.
		// The actual token lives on the VM in auth-profiles.json, not in the backend DB.
		if _, hasCodexCred := params.Credentials["openai-codex"]; hasCodexCred {
			if _, exists := providerConfigs["openai-codex"]; !exists {
				providerConfigs["openai-codex"] = buildCodexProviderConfig(params.ModelCatalog)
			}
			auth := getOrCreateMap(result, "auth")
			authProfiles := getOrCreateMap(auth, "profiles")
			authProfiles["openai-codex:default"] = map[string]interface{}{
				"provider": "openai-codex",
				"mode":     "oauth",
			}
			auth["profiles"] = authProfiles
			result["auth"] = auth
		}

		// 5a. Inject search provider configuration.
		// Enable plugins for ALL credentialed search providers, not just the
		// active one. This lets the gateway switch providers without needing a
		// config push — tools.web.search.provider picks the active one.
		var enabledSearchPlugins []string
		for _, cp := range params.ProviderCatalog {
			if cp.Category != "search" {
				continue
			}
			if _, hasCred := params.Credentials[cp.ID]; !hasCred {
				continue
			}
			// Enable the plugin with its API key exec secret ref.
			// Search plugins read their key from plugins.entries.{plugin}.config.webSearch.apiKey
			// (see e.g. extensions/exa/src/exa-web-search-provider.ts credentialPath).
			pluginID := pluginIDForProvider(params.ProviderCatalog, cp.ID)
			secretID := execSecretID(params.ProviderCatalog, cp.ID)
			pluginEntry := map[string]interface{}{
				"enabled": true,
				"config": map[string]interface{}{
					"webSearch": map[string]interface{}{
						"apiKey": map[string]interface{}{
							"source":   "exec",
							"provider": "ocm",
							"id":       secretID,
						},
					},
				},
			}
			plugins := getOrCreateMap(result, "plugins")
			entries := getOrCreateMap(plugins, "entries")
			entries[pluginID] = pluginEntry
			plugins["entries"] = entries
			result["plugins"] = plugins
			enabledSearchPlugins = append(enabledSearchPlugins, pluginID)
			searchPluginAllows = append(searchPluginAllows, pluginID)
		}

		// Set the active search provider (highest priority credentialed provider)
		searchProvider := resolveSearchProvider(params)
		if searchProvider != "" {
			tools := getOrCreateMap(result, "tools")
			web := getOrCreateMap(tools, "web")
			web["search"] = map[string]interface{}{
				"enabled":  true,
				"provider": searchProvider,
			}
			tools["web"] = web
			result["tools"] = tools
			slog.Info("config.search_block_injected", "machine_id", params.MachineID, "provider", searchProvider, "enabled_plugins", enabledSearchPlugins)
		}

		models := getOrCreateMap(result, "models")
		models["providers"] = providerConfigs
		result["models"] = models
	}

	// 5b. allowedOrigins: allow all origins.
	// The auth proxy rewrites the Origin header to its internal address
	// (e.g. http://127.0.0.1:18789), so explicit origin lists never match the
	// real browser origin. Security is enforced at the auth proxy layer (machine
	// JWT validation via Cloudflare Access), not at the gateway origin check.
	// OpenClaw v2026.3.8+ rejects unknown origins by default when allowedOrigins
	// is absent, so we must explicitly allow all.
	if gw, ok := result["gateway"].(map[string]interface{}); ok {
		if cui, ok := gw["controlUi"].(map[string]interface{}); ok {
			cui["allowedOrigins"] = []string{"*"}
		}
	}

	// 5c. Inject agents.defaults — model, available models, workspace
	if params.DefaultModel != "" {
		// Resolve platform model IDs (e.g. "deepseek/deepseek-v3" → Nebius model ID)
		modelForConfig := mapModelFromCatalog(params.ModelCatalog, params.DefaultModel)
		agents := getOrCreateMap(result, "agents")
		defaults := getOrCreateMap(agents, "defaults")
		modelDefaults := map[string]interface{}{
			"primary": modelForConfig,
		}
		modelsCatalog := map[string]interface{}{}
		putAgentModelCatalogEntry(modelsCatalog, modelForConfig, openAIAgentRuntime)
		seenFallbacks := map[string]bool{modelForConfig: true}
		var mappedFallbacks []string
		for _, fallback := range params.ModelFallbacks {
			mapped := mapModelFromCatalog(params.ModelCatalog, fallback)
			if mapped == "" || seenFallbacks[mapped] {
				continue
			}
			seenFallbacks[mapped] = true
			mappedFallbacks = append(mappedFallbacks, mapped)
			putAgentModelCatalogEntry(modelsCatalog, mapped, openAIAgentRuntime)
		}
		if len(mappedFallbacks) > 0 {
			modelDefaults["fallbacks"] = mappedFallbacks
		}
		defaults["model"] = modelDefaults
		// Use shared filter: platform always, byok/subscription only with credentials
		credProviders := make(map[string]bool, len(params.Credentials))
		for provider := range params.Credentials {
			credProviders[provider] = true
		}
		for catalogID := range AvailableCatalogIDs(params.ModelCatalog, credProviders) {
			mapped := mapModelFromCatalog(params.ModelCatalog, catalogID)
			putAgentModelCatalogEntry(modelsCatalog, mapped, openAIAgentRuntime)
		}
		defaults["models"] = modelsCatalog
		defaults["workspace"] = "/home/openclaw/.openclaw/workspace"
		// Heartbeat fires every 15 minutes so monitoring/alerting agents have
		// a default wake-up cadence. Platform-locked (agents.* is in
		// protectedPrefixes); overriding requires a platform change.
		defaults["heartbeat"] = map[string]interface{}{
			"every": "15m",
		}
		agents["defaults"] = defaults
		result["agents"] = agents
	}

	// 5d. Render agents.list from AgentDefinition slice
	if len(params.Agents) > 0 && params.DefaultModel != "" {
		// Resolve default model for config
		defaultModelForConfig := mapModelFromCatalog(params.ModelCatalog, params.DefaultModel)
		agents := getOrCreateMap(result, "agents")
		defaults := getOrCreateMap(agents, "defaults")
		modelsMap, _ := defaults["models"].(map[string]interface{})
		if modelsMap == nil {
			modelsMap = map[string]interface{}{}
		}

		var list []interface{}
		for _, a := range params.Agents {
			entry := map[string]interface{}{
				"id":   a.AgentID,
				"name": a.Name,
			}
			if a.IsDefault {
				entry["default"] = true
				entry["workspace"] = "/home/openclaw/.openclaw/workspace"
			} else {
				entry["workspace"] = "/home/openclaw/.openclaw/workspace-" + a.AgentID
			}
			model := defaultModelForConfig
			if a.Model != "" {
				// Resolve agent-specific model ID too
				agentModel := mapModelFromCatalog(params.ModelCatalog, a.Model)
				model = agentModel
				putAgentModelCatalogEntry(modelsMap, agentModel, openAIAgentRuntime)
			}
			entry["model"] = map[string]interface{}{"primary": model}
			identity := map[string]interface{}{}
			if a.IdentityEmoji != "" {
				identity["emoji"] = a.IdentityEmoji
			}
			if a.IdentityAvatar != "" {
				identity["avatar"] = a.IdentityAvatar
			}
			if len(identity) > 0 {
				entry["identity"] = identity
			}
			list = append(list, entry)
		}
		agents["list"] = list
		defaults["models"] = modelsMap
		agents["defaults"] = defaults
		result["agents"] = agents
	}

	// Plugin config: deep-merge each enabled plugin's template, then overrides.
	// Collect all plugins.allow entries since deepMerge replaces arrays (last writer wins).
	var allPluginAllows []string
	for _, p := range params.Plugins {
		if p.ConfigTemplate != nil {
			// Extract allow entries before merge (deepMerge will overwrite the array)
			if tmpl, ok := p.ConfigTemplate["plugins"].(map[string]interface{}); ok {
				if allow, ok := tmpl["allow"].([]interface{}); ok {
					for _, a := range allow {
						if s, ok := a.(string); ok {
							allPluginAllows = append(allPluginAllows, s)
						}
					}
				}
			}
			result = deepMerge(result, p.ConfigTemplate)
		}
		if p.ConfigOverrides != nil {
			result = deepMerge(result, p.ConfigOverrides)
		}
	}
	allPluginAllows = append(allPluginAllows, searchPluginAllows...)
	// Restore the consolidated allow list (all plugins, deduplicated)
	if len(allPluginAllows) > 0 {
		if plugins, ok := result["plugins"].(map[string]interface{}); ok {
			seen := map[string]bool{}
			var dedupAllow []interface{}
			for _, a := range allPluginAllows {
				if !seen[a] {
					seen[a] = true
					dedupAllow = append(dedupAllow, a)
				}
			}
			plugins["allow"] = dedupAllow
		}
	}

	// 5d-opik. Inject Opik apiUrl, apiKey, and installs path into the opik-openclaw plugin config.
	// The apiKey is resolved at runtime via exec secret ref (ocm-secrets).
	// The installs entry tells the gateway where to find the user-installed plugin on disk.
	if params.OpikAPIURL != "" {
		if plugins, ok := result["plugins"].(map[string]interface{}); ok {
			if entries, ok := plugins["entries"].(map[string]interface{}); ok {
				if opikEntry, ok := entries["opik-openclaw"].(map[string]interface{}); ok {
					config := getOrCreateMap(opikEntry, "config")
					hooks := getOrCreateMap(opikEntry, "hooks")
					// Default to enabled when Opik is provisioned so the plugin actually starts.
					if _, ok := config["enabled"]; !ok {
						config["enabled"] = true
					}
					config["apiUrl"] = params.OpikAPIURL
					if params.OpikAPIKey != "" {
						config["apiKey"] = params.OpikAPIKey
					}
					hooks["allowConversationAccess"] = true
				}
			}
			// Plugins are bundled in the artifact's dist/extensions/ — no installs entry needed.
		}
	}

	// 5e-composio. Inject Composio config: apiUrl (backend proxy) and userId.
	if params.ComposioAPIURL != "" {
		if plugins, ok := result["plugins"].(map[string]interface{}); ok {
			if entries, ok := plugins["entries"].(map[string]interface{}); ok {
				if composioEntry, ok := entries["composio"].(map[string]interface{}); ok {
					config := getOrCreateMap(composioEntry, "config")
					// Remove legacy fields from stored config (schema has additionalProperties: false)
					delete(config, "consumerKey")
					delete(config, "mcpUrl")
					if _, ok := config["enabled"]; !ok {
						config["enabled"] = true
					}
					config["apiUrl"] = params.ComposioAPIURL
					config["userId"] = params.MachineID
					if params.ComposioProxyTokenSigner != nil {
						token, err := params.ComposioProxyTokenSigner(params.MachineID)
						if err != nil {
							return nil, fmt.Errorf("composio proxy token: %w", err)
						}
						config["machineToken"] = token
					} else {
						slog.Warn("configassembly.composio_no_token_signer",
							"machine_id", params.MachineID,
							"note", "plugin will be unable to authenticate to backend proxy")
					}
					slog.Debug("configassembly.composio_injected", "machine_id", params.MachineID)
				}
			}
			// Plugins are bundled in the artifact's dist/extensions/ — no installs entry needed.
		}
	} else {
		slog.Debug("configassembly.composio_skipped", "reason", "no_api_url")
	}

	// 5e. Inject channel credential tokens as plaintext into the config.
	if len(params.ChannelCredentialValues) > 0 {
		channels, _ := result["channels"].(map[string]interface{})
		for provider, token := range params.ChannelCredentialValues {
			channelID, fieldName, ok := ProviderToChannel(provider)
			if !ok {
				continue
			}
			if channels == nil {
				continue
			}
			chConf, _ := channels[channelID].(map[string]interface{})
			if chConf == nil {
				continue
			}
			chConf[fieldName] = token
		}
	}

	// 6. Build skills.allowBundled from enabled skill capabilities
	var skillIDs []interface{}
	for _, cap := range params.Capabilities {
		if cap.EntryType == "skill" {
			skillIDs = append(skillIDs, cap.EntryID)
		}
	}
	skills := getOrCreateMap(result, "skills")
	if skillIDs != nil {
		skills["allowBundled"] = skillIDs
	}
	result["skills"] = skills

	// 7. Marshal to JSON
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal assembled config: %w", err)
	}

	// Log assembly summary for debugging config issues
	authMode := "unknown"
	controlUiEnabled := false
	var allowedOriginsCount int
	if gw, ok := result["gateway"].(map[string]interface{}); ok {
		if a, ok := gw["auth"].(map[string]interface{}); ok {
			if m, ok := a["mode"].(string); ok {
				authMode = m
			}
		}
		if cui, ok := gw["controlUi"].(map[string]interface{}); ok {
			if e, ok := cui["enabled"].(bool); ok {
				controlUiEnabled = e
			}
			if origins, ok := cui["allowedOrigins"].([]interface{}); ok {
				allowedOriginsCount = len(origins)
			}
		}
	}
	slog.Info("config.assembly.complete",
		"machine_id", params.MachineID,
		"auth_mode", authMode,
		"control_ui_enabled", controlUiEnabled,
		"allowed_origins_count", allowedOriginsCount,
		"vm_hostname", params.VMHostname,
		"account_hostname", params.AccountHostname,
		"provider_count", len(params.Credentials),
		"skill_count", len(skillIDs),
		"default_model", params.DefaultModel,
		"capability_count", len(params.Capabilities),
		"config_bytes", len(data),
	)

	return data, nil
}

// SeedParams contains inputs for generating the native-mode seed config.
type SeedParams struct {
	MachineID          string
	DefaultModel       string                            // e.g. "anthropic/claude-sonnet-4-6" — platform mapping applied
	ModelFallbacks     []string                          // ordered fallback models; platform mapping applied
	Providers          []string                          // provider names the user has configured keys for
	ProxyBaseURL       string                            // e.g. "http://192.168.100.1:4000"
	ModelCatalog       []CatalogModel                    // models from the model_catalog DB table
	ProviderCatalog    []CatalogProvider                 // providers from the provider_catalog DB table
	SearchProvider     string                            // explicit override, or "" for auto-detect
	OpenAIAgentRuntime string                            // "", "auto", "codex", or "pi"; non-auto is emitted for openai/* models
	ChannelConfigs     map[string]map[string]interface{} // channel provider → merged config (template + overrides + token)
	Plugins            []PluginSelection                 // enabled plugins (from machine_plugins + catalog)
	OpikAPIURL         string                            // Opik tracing endpoint URL
	OpikAPIKey         string                            // API key for Opik plugin authentication (gateway token)
	ComposioAPIURL     string                            // Backend proxy URL for Composio (e.g. https://api.openclawmachines.com/api/composio)
	// ComposioProxyTokenSigner mints a short-lived per-machine token the
	// in-VM Composio plugin presents to the backend proxy. See
	// AssemblyParams.ComposioProxyTokenSigner.
	ComposioProxyTokenSigner func(machineID string) (string, error)
	RuntimeSource            string // Always "artifact"
}

// providerExecIDs maps provider names to their exec secret ref IDs.
// Deprecated: prefer catalog-based lookup via execSecretID(). Kept for backward
// compatibility when ProviderCatalog is empty (callers not yet loading the catalog).
var providerExecIDs = map[string]string{
	"anthropic":  "anthropic-key",
	"openai":     "openai-key",
	"google":     "google-key",
	"openrouter": "openrouter-key",
	"nebius":     "nebius-key",
}

// llmProviderEnvVars maps provider names to their API key env var names.
// Deprecated: prefer catalog-based lookup via envVarName(). Kept for backward
// compatibility when ProviderCatalog is empty (callers not yet loading the catalog).
// piSdk resolves env var names when apiKey matches /^[A-Z_][A-Z0-9_]*$/.
var llmProviderEnvVars = map[string]string{
	"anthropic":  "ANTHROPIC_API_KEY",
	"openai":     "OPENAI_API_KEY",
	"google":     "GOOGLE_API_KEY",
	"openrouter": "OPENROUTER_API_KEY",
	"nebius":     "NEBIUS_API_KEY",
}

// execSecretID returns the exec secret ID for a provider.
// Uses the catalog's ExecSecretID field, falling back to id + "-key".
// If the catalog is empty, falls back to the deprecated providerExecIDs map.
func execSecretID(catalog []CatalogProvider, provider string) string {
	// First try direct ID match
	for _, p := range catalog {
		if p.ID == provider {
			if p.ExecSecretID != "" {
				return p.ExecSecretID
			}
			return p.ID + "-key"
		}
	}
	// Then try RuntimeProviderID match (e.g., "gemini" → gemini-search entry).
	// This handles the case where buildProviderConfig is called with the runtime
	// ID but needs to find the exec secret from the original catalog entry.
	for _, p := range catalog {
		if p.RuntimeProviderID == provider {
			if p.ExecSecretID != "" {
				return p.ExecSecretID
			}
			return p.ID + "-key"
		}
	}
	// Fallback to hardcoded map for backward compat
	if id, ok := providerExecIDs[provider]; ok {
		slog.Warn("config.exec_secret_id.deprecated_fallback", "provider", provider, "resolved", id)
		return id
	}
	slog.Warn("config.exec_secret_id.unknown_provider", "provider", provider, "fallback", provider+"-key")
	return provider + "-key"
}

// envVarName returns the environment variable name for a provider.
// If the catalog is empty, falls back to the deprecated llmProviderEnvVars map.
func envVarName(catalog []CatalogProvider, provider string) string {
	for _, p := range catalog {
		if p.ID == provider {
			return p.EnvVar
		}
	}
	// Try RuntimeProviderID match (e.g., "gemini" → gemini-search entry)
	for _, p := range catalog {
		if p.RuntimeProviderID == provider {
			return p.EnvVar
		}
	}
	// Fallback to hardcoded map for backward compat
	if name, ok := llmProviderEnvVars[provider]; ok {
		return name
	}
	return ""
}

// isProxiedProvider returns true if the provider is an AI or search provider
// that should be proxied. Uses the catalog when available, falling back to the
// deprecated providerExecIDs map.
func isProxiedProvider(catalog []CatalogProvider, provider string) bool {
	for _, p := range catalog {
		if p.ID == provider && (p.Category == "ai" || p.Category == "search") {
			return true
		}
	}
	// Fallback to hardcoded map for backward compat
	if _, ok := providerExecIDs[provider]; ok {
		return true
	}
	return false
}

func isSearchProvider(catalog []CatalogProvider, provider string) bool {
	for _, p := range catalog {
		if p.ID == provider && p.Category == "search" {
			return true
		}
	}
	return false
}

// runtimeProviderID returns the gateway provider name for a catalog provider.
// Uses RuntimeProviderID if set, otherwise falls back to the provider's ID.
func runtimeProviderID(catalog []CatalogProvider, id string) string {
	for _, p := range catalog {
		if p.ID == id {
			if p.RuntimeProviderID != "" {
				return p.RuntimeProviderID
			}
			return p.ID
		}
	}
	return id
}

// findCatalogProvider returns the catalog entry for the given provider ID, or nil.
func findCatalogProvider(catalog []CatalogProvider, id string) *CatalogProvider {
	for i := range catalog {
		if catalog[i].ID == id {
			return &catalog[i]
		}
	}
	return nil
}

// pluginIDForProvider returns the OpenClaw plugin ID for a provider.
// Most providers use their own ID as the plugin name (e.g., "exa" → plugin "exa").
// Some are remapped (e.g., "gemini-search" → plugin "google").
// Also searches by RuntimeProviderID for cases where the resolved search provider
// name is the runtime ID, not the catalog ID.
func pluginIDForProvider(catalog []CatalogProvider, provider string) string {
	// Direct ID match
	for _, p := range catalog {
		if p.ID == provider {
			if p.PluginID != "" {
				return p.PluginID
			}
			return p.ID
		}
	}
	// RuntimeProviderID match (e.g., provider="gemini" → catalog entry gemini-search)
	for _, p := range catalog {
		if p.RuntimeProviderID == provider {
			if p.PluginID != "" {
				return p.PluginID
			}
			return p.ID
		}
	}
	return provider // fallback: use provider name as plugin ID
}

// resolveSearchProvider determines the active search provider.
// Explicit override wins; otherwise auto-detect from credentials by autodetect_order.
func resolveSearchProvider(params AssemblyParams) string {
	if params.SearchProvider != "" {
		// Reject overrides the catalog explicitly identifies as non-search
		// (e.g. an AI-category provider) so they don't land in tools.web.search.
		// When the catalog is empty (e.g. degraded mode after a load failure),
		// `cp == nil` — preserve the legacy fallback and accept the override.
		if cp := findCatalogProvider(params.ProviderCatalog, params.SearchProvider); cp != nil && cp.Category != "search" {
			slog.Warn("config.search_provider.override_not_search_provider", "machine_id", params.MachineID, "override", params.SearchProvider, "category", cp.Category)
		} else if _, ok := params.Credentials[params.SearchProvider]; ok {
			resolved := runtimeProviderID(params.ProviderCatalog, params.SearchProvider)
			slog.Info("config.search_provider.override_applied", "machine_id", params.MachineID, "override", params.SearchProvider, "resolved", resolved)
			return resolved
		} else {
			slog.Warn("config.search_provider.override_no_credential", "machine_id", params.MachineID, "override", params.SearchProvider)
		}
	}
	// Auto-detect: find the search provider with lowest autodetect_order that has a credential
	type candidate struct {
		id    string
		order int
	}
	var candidates []candidate
	for _, p := range params.ProviderCatalog {
		if p.Category != "search" || p.AutodetectOrder == nil {
			continue
		}
		if _, ok := params.Credentials[p.ID]; ok {
			candidates = append(candidates, candidate{id: p.ID, order: *p.AutodetectOrder})
		}
	}
	if len(candidates) == 0 {
		slog.Debug("config.search_provider.none", "machine_id", params.MachineID)
		return ""
	}
	// Find the candidate with lowest autodetect_order
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.order < best.order {
			best = c
		}
	}
	resolved := runtimeProviderID(params.ProviderCatalog, best.id)
	slog.Info("config.search_provider.auto_detected", "machine_id", params.MachineID, "provider", best.id, "runtime_id", resolved, "autodetect_order", best.order, "candidates", len(candidates))
	return resolved
}

// resolveSearchProviderSeed determines the active search provider for seed config.
// Same logic as resolveSearchProvider but works with SeedParams.
func resolveSearchProviderSeed(params SeedParams) string {
	if params.SearchProvider != "" {
		// Reject overrides the catalog identifies as non-search (see
		// resolveSearchProvider). Empty/missing catalog falls through to the
		// legacy provider-list check.
		if cp := findCatalogProvider(params.ProviderCatalog, params.SearchProvider); cp != nil && cp.Category != "search" {
			slog.Warn("config.seed.search_provider.override_not_search_provider", "machine_id", params.MachineID, "override", params.SearchProvider, "category", cp.Category)
		} else {
			for _, p := range params.Providers {
				if p == params.SearchProvider {
					resolved := runtimeProviderID(params.ProviderCatalog, params.SearchProvider)
					slog.Info("config.seed.search_provider.override_applied", "machine_id", params.MachineID, "override", params.SearchProvider, "resolved", resolved)
					return resolved
				}
			}
			slog.Warn("config.seed.search_provider.override_no_provider", "machine_id", params.MachineID, "override", params.SearchProvider)
		}
	}
	// Auto-detect: find the search provider with lowest autodetect_order that has a credential
	providerSet := make(map[string]bool, len(params.Providers))
	for _, p := range params.Providers {
		providerSet[p] = true
	}
	type candidate struct {
		id    string
		order int
	}
	var candidates []candidate
	for _, p := range params.ProviderCatalog {
		if p.Category != "search" || p.AutodetectOrder == nil {
			continue
		}
		if providerSet[p.ID] {
			candidates = append(candidates, candidate{id: p.ID, order: *p.AutodetectOrder})
		}
	}
	if len(candidates) == 0 {
		slog.Debug("config.seed.search_provider.none", "machine_id", params.MachineID)
		return ""
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.order < best.order {
			best = c
		}
	}
	resolved := runtimeProviderID(params.ProviderCatalog, best.id)
	slog.Info("config.seed.search_provider.auto_detected", "machine_id", params.MachineID, "provider", best.id, "runtime_id", resolved, "autodetect_order", best.order, "candidates", len(candidates))
	return resolved
}

// buildProviderConfig creates a provider config entry for the given provider.
// In native mode, uses exec secret refs and /v1 baseUrl suffix (piSdk appends
// only the resource path). In non-native mode, uses env var names and no suffix
// (piSdk prepends /v1 itself).
func buildProviderConfig(provider, proxyBaseURL string, nativeMode bool, catalog []CatalogProvider) map[string]interface{} {
	var apiKey interface{}
	baseURL := proxyBaseURL + "/" + provider
	if nativeMode {
		// Google's OpenAI-compatible endpoint lives at /v1beta/openai/ (no /v1
		// prefix). The proxy's UpstreamPathPrefix handles the rewrite, so we
		// must NOT append /v1 here — otherwise the upstream path would contain
		// a spurious /v1 segment that Google rejects.
		if provider != "google" {
			baseURL += "/v1"
		}
		apiKey = map[string]interface{}{
			"source":   "exec",
			"provider": "ocm",
			"id":       execSecretID(catalog, provider),
		}
	} else {
		apiKey = envVarName(catalog, provider)
	}
	slog.Debug("config.provider_config_built", "provider", provider, "base_url", baseURL, "native_mode", nativeMode)
	return map[string]interface{}{
		"baseUrl": baseURL,
		"apiKey":  apiKey,
		"models":  []interface{}{},
	}
}

// AssembleSeedConfig generates the seed openclaw.json for native config mode.
// This config is written to disk at boot and owned by the user after that.
// Unlike AssembleConfig (capability-based), credentials use exec secret provider refs
// instead of env var names, and the config is a standalone file rather than
// served via HTTP config source.
func AssembleSeedConfig(params SeedParams) ([]byte, error) {
	openAIAgentRuntime, err := NormalizeOpenAIAgentRuntime(params.OpenAIAgentRuntime)
	if err != nil {
		return nil, err
	}

	// Exec secret provider
	secrets := map[string]interface{}{
		"providers": map[string]interface{}{
			"ocm": map[string]interface{}{
				"source":            "exec",
				"command":           "/usr/local/bin/ocm-secrets",
				"allowInsecurePath": true,
			},
		},
	}

	// Gateway defaults (same as platform defaults, but written to file)
	gateway := map[string]interface{}{
		"mode": "local",
		"auth": map[string]interface{}{"mode": "token"},
		"controlUi": map[string]interface{}{
			"enabled":                      true,
			"allowInsecureAuth":            true,
			"dangerouslyDisableDeviceAuth": true,
			"allowedOrigins":               []string{"*"},
		},
		"reload": map[string]interface{}{"mode": "hot"},
		"nodes": map[string]interface{}{
			"denyCommands": []string{
				"camera.snap", "camera.clip", "screen.record",
				"calendar.add", "contacts.add", "reminders.add",
			},
		},
		// OpenAI-compatible chat completions endpoint. Mirrors the default in
		// platformDefaults for AssembleConfig — first-boot seed path must stay
		// in sync or new machines silently lose the feature.
		"http": map[string]interface{}{
			"endpoints": map[string]interface{}{
				"chatCompletions": map[string]interface{}{
					"enabled": true,
				},
			},
		},
	}

	// Provider configs with exec secret refs
	providerSet := make(map[string]bool)
	for _, p := range params.Providers {
		providerSet[p] = true
	}
	providerSet["nebius"] = true // always include nebius for platform models

	providerConfigs := map[string]interface{}{}
	for provider := range providerSet {
		if !isProxiedProvider(params.ProviderCatalog, provider) {
			continue
		}
		if isSearchProvider(params.ProviderCatalog, provider) {
			continue
		}
		provCfg := buildProviderConfig(provider, params.ProxyBaseURL, true, params.ProviderCatalog)
		switch provider {
		case "anthropic":
			provCfg["api"] = "anthropic-messages"
		case "openai":
			provCfg["api"] = "openai-completions"
		case "google":
			provCfg["api"] = "openai-completions"
		case "nebius":
			provCfg["api"] = "openai-completions"
			provCfg["models"] = buildNebiusModelsFromCatalog(params.ModelCatalog)
		case "openrouter":
			provCfg["api"] = "openai-completions"
			provCfg["models"] = buildOpenRouterModelsFromCatalog(params.ModelCatalog)
		}
		providerConfigs[provider] = provCfg
	}
	// openai-codex uses native OpenClaw OAuth — gateway calls chatgpt.com directly.
	// No proxy, no exec secret ref. Auth comes from auth-profiles.json.
	// Handled separately since it has no providerExecIDs entry.
	if providerSet["openai-codex"] {
		providerConfigs["openai-codex"] = buildCodexProviderConfig(params.ModelCatalog)
	}

	// Search provider selection for seed config. Search providers are exposed via
	// tools.web.search + plugins.entries.{provider}; they should not also appear
	// under models.providers.
	searchProvider := resolveSearchProviderSeed(params)

	// Apply platform model mapping
	defaultModel := mapModelFromCatalog(params.ModelCatalog, params.DefaultModel)

	plugins := map[string]interface{}{
		"entries": map[string]interface{}{},
	}

	// Build the models catalog — the gateway rejects any model not listed
	// in agents.defaults.models as "Unknown model".
	// Uses AvailableCatalogIDs for filtering, then maps to gateway model IDs.
	modelsCatalog := map[string]interface{}{}
	putAgentModelCatalogEntry(modelsCatalog, defaultModel, openAIAgentRuntime)
	modelDefaults := map[string]interface{}{"primary": defaultModel}
	seenFallbacks := map[string]bool{defaultModel: true}
	var mappedFallbacks []string
	for _, fallback := range params.ModelFallbacks {
		mapped := mapModelFromCatalog(params.ModelCatalog, fallback)
		if mapped == "" || seenFallbacks[mapped] {
			continue
		}
		seenFallbacks[mapped] = true
		mappedFallbacks = append(mappedFallbacks, mapped)
		putAgentModelCatalogEntry(modelsCatalog, mapped, openAIAgentRuntime)
	}
	if len(mappedFallbacks) > 0 {
		modelDefaults["fallbacks"] = mappedFallbacks
	}
	for catalogID := range AvailableCatalogIDs(params.ModelCatalog, providerSet) {
		mapped := mapModelFromCatalog(params.ModelCatalog, catalogID)
		putAgentModelCatalogEntry(modelsCatalog, mapped, openAIAgentRuntime)
	}

	result := map[string]interface{}{
		"secrets": secrets,
		"gateway": gateway,
		"models":  map[string]interface{}{"providers": providerConfigs},
		"agents": map[string]interface{}{
			"defaults": map[string]interface{}{
				"model":     modelDefaults,
				"models":    modelsCatalog,
				"workspace": "/home/openclaw/.openclaw/workspace",
				// Mirrors the heartbeat default added to AssembleConfig's
				// dynamic agents.defaults injection. Platform-locked because
				// agents.* is in protectedPrefixes.
				"heartbeat": map[string]interface{}{"every": "15m"},
			},
		},
		"commands": map[string]interface{}{
			"native":       "auto",
			"nativeSkills": "auto",
			"restart":      true,
			"ownerDisplay": "raw",
		},
		"session": map[string]interface{}{
			"dmScope": "per-channel-peer",
			// Mirrors platformDefaults — per-thread state for Slack/Discord bots.
			"threadBindings": map[string]interface{}{"enabled": true},
		},
		// Cron and env.shellEnv are top-level defaults in AssembleConfig's
		// platformDefaults; replicate them here so fresh machines get the
		// same behavior. Both are intentionally user-overridable.
		"cron": map[string]interface{}{"enabled": true},
		"env": map[string]interface{}{
			"shellEnv": map[string]interface{}{"enabled": true},
		},
		"plugins": plugins,
	}

	// Enable plugins for ALL credentialed search providers, not just the active one.
	var enabledSearchPlugins []string
	var searchPluginAllows []string
	for _, cp := range params.ProviderCatalog {
		if cp.Category != "search" {
			continue
		}
		// In seed config, params.Providers contains provider names (both runtime + catalog IDs)
		hasProvider := false
		for _, p := range params.Providers {
			if p == cp.ID || (cp.RuntimeProviderID != "" && p == cp.RuntimeProviderID) {
				hasProvider = true
				break
			}
		}
		if !hasProvider {
			continue
		}
		pluginID := pluginIDForProvider(params.ProviderCatalog, cp.ID)
		secretID := execSecretID(params.ProviderCatalog, cp.ID)
		pluginEntry := map[string]interface{}{
			"enabled": true,
			"config": map[string]interface{}{
				"webSearch": map[string]interface{}{
					"apiKey": map[string]interface{}{
						"source":   "exec",
						"provider": "ocm",
						"id":       secretID,
					},
				},
			},
		}
		seedPlugins := getOrCreateMap(result, "plugins")
		seedEntries := getOrCreateMap(seedPlugins, "entries")
		seedEntries[pluginID] = pluginEntry
		seedPlugins["entries"] = seedEntries
		result["plugins"] = seedPlugins
		enabledSearchPlugins = append(enabledSearchPlugins, pluginID)
		searchPluginAllows = append(searchPluginAllows, pluginID)
	}

	// Set the active search provider
	if searchProvider != "" {
		tools := getOrCreateMap(result, "tools")
		web := getOrCreateMap(tools, "web")
		web["search"] = map[string]interface{}{
			"enabled":  true,
			"provider": searchProvider,
		}
		tools["web"] = web
		result["tools"] = tools
		slog.Info("config.seed.search_block_injected", "machine_id", params.MachineID, "provider", searchProvider, "enabled_plugins", enabledSearchPlugins)
	}

	// Channel configs: each enabled channel's merged template + overrides + token.
	// Channels are managed by the channel state machine after first boot; the seed
	// ensures they're active from the moment the gateway starts.
	if len(params.ChannelConfigs) > 0 {
		channels := map[string]interface{}{}
		for provider, conf := range params.ChannelConfigs {
			channels[provider] = conf
		}
		result["channels"] = channels
	}

	// Plugin config: deep-merge each enabled plugin's template, then overrides.
	// Collect all plugins.allow entries since deepMerge replaces arrays (last writer wins).
	var seedAllPluginAllows []string
	for _, p := range params.Plugins {
		if p.ConfigTemplate != nil {
			if tmpl, ok := p.ConfigTemplate["plugins"].(map[string]interface{}); ok {
				if allow, ok := tmpl["allow"].([]interface{}); ok {
					for _, a := range allow {
						if s, ok := a.(string); ok {
							seedAllPluginAllows = append(seedAllPluginAllows, s)
						}
					}
				}
			}
			result = deepMerge(result, p.ConfigTemplate)
		}
		if p.ConfigOverrides != nil {
			result = deepMerge(result, p.ConfigOverrides)
		}
	}
	seedAllPluginAllows = append(seedAllPluginAllows, searchPluginAllows...)
	if len(seedAllPluginAllows) > 0 {
		if plugins, ok := result["plugins"].(map[string]interface{}); ok {
			seen := map[string]bool{}
			var dedupAllow []interface{}
			for _, a := range seedAllPluginAllows {
				if !seen[a] {
					seen[a] = true
					dedupAllow = append(dedupAllow, a)
				}
			}
			plugins["allow"] = dedupAllow
		}
	}

	// Inject Opik apiUrl, apiKey, and installs path into the opik-openclaw plugin config.
	if params.OpikAPIURL != "" {
		if plugins, ok := result["plugins"].(map[string]interface{}); ok {
			if entries, ok := plugins["entries"].(map[string]interface{}); ok {
				if opikEntry, ok := entries["opik-openclaw"].(map[string]interface{}); ok {
					config := getOrCreateMap(opikEntry, "config")
					hooks := getOrCreateMap(opikEntry, "hooks")
					if _, ok := config["enabled"]; !ok {
						config["enabled"] = true
					}
					config["apiUrl"] = params.OpikAPIURL
					if params.OpikAPIKey != "" {
						config["apiKey"] = params.OpikAPIKey
					}
					hooks["allowConversationAccess"] = true
					slog.Info("configassembly.seed.opik_injected", "machine_id", params.MachineID, "api_url", params.OpikAPIURL, "has_key", params.OpikAPIKey != "")
				} else {
					slog.Warn("configassembly.seed.opik_entry_missing", "machine_id", params.MachineID, "entries_keys", configMapKeys(entries))
				}
			} else {
				slog.Warn("configassembly.seed.opik_no_entries", "machine_id", params.MachineID)
			}
		} else {
			slog.Warn("configassembly.seed.opik_no_plugins", "machine_id", params.MachineID)
		}
	}

	// Inject Composio config into seed config.
	if params.ComposioAPIURL != "" {
		if plugins, ok := result["plugins"].(map[string]interface{}); ok {
			if entries, ok := plugins["entries"].(map[string]interface{}); ok {
				if composioEntry, ok := entries["composio"].(map[string]interface{}); ok {
					config := getOrCreateMap(composioEntry, "config")
					delete(config, "consumerKey")
					delete(config, "mcpUrl")
					if _, ok := config["enabled"]; !ok {
						config["enabled"] = true
					}
					config["apiUrl"] = params.ComposioAPIURL
					config["userId"] = params.MachineID
					if params.ComposioProxyTokenSigner != nil {
						token, err := params.ComposioProxyTokenSigner(params.MachineID)
						if err != nil {
							return nil, fmt.Errorf("composio proxy token (seed): %w", err)
						}
						config["machineToken"] = token
					} else {
						slog.Warn("configassembly.seed.composio_no_token_signer",
							"machine_id", params.MachineID,
							"note", "plugin will be unable to authenticate to backend proxy")
					}
				}
			}
		}
	}

	// Emit auth.profiles for codex OAuth when provider is configured.
	// The gateway reads auth.profiles from openclaw.json to know which profiles
	// to look up in auth-profiles.json. mode: "oauth" triggers native token refresh.
	for _, p := range params.Providers {
		if p == "openai-codex" {
			result["auth"] = map[string]interface{}{
				"profiles": map[string]interface{}{
					"openai-codex:default": map[string]interface{}{
						"provider": "openai-codex",
						"mode":     "oauth",
					},
				},
			}
			break
		}
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal seed config: %w", err)
	}

	slog.Info("config.seed.assembled",
		"machine_id", params.MachineID,
		"default_model", defaultModel,
		"provider_count", len(providerConfigs),
		"config_bytes", len(data),
	)

	return data, nil
}

// deepMerge recursively merges overlay into base, returning a new map.
// If both values for a key are maps, they are merged recursively.
// Otherwise the overlay value wins.
// Neither base nor overlay are modified.
func deepMerge(base, overlay map[string]interface{}) map[string]interface{} {
	result := deepCopy(base)
	for k, ov := range overlay {
		bv, exists := result[k]
		if !exists {
			result[k] = deepCopyValue(ov)
			continue
		}
		bMap, bIsMap := bv.(map[string]interface{})
		oMap, oIsMap := ov.(map[string]interface{})
		if bIsMap && oIsMap {
			result[k] = deepMerge(bMap, oMap)
		} else {
			result[k] = deepCopyValue(ov)
		}
	}
	return result
}

// deepCopy creates a deep copy of a map[string]interface{}.
func deepCopy(m map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = deepCopyValue(v)
	}
	return result
}

// deepCopyValue copies a single value, recursing into maps and slices.
func deepCopyValue(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		return deepCopy(val)
	case []interface{}:
		cp := make([]interface{}, len(val))
		for i, elem := range val {
			cp[i] = deepCopyValue(elem)
		}
		return cp
	default:
		// For typed slices/maps that aren't map[string]interface{} or []interface{},
		// fall back to JSON marshal/unmarshal. This is O(n) allocation + serialization
		// but acceptable for config assembly where payloads are small and infrequent.
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Map {
			data, err := json.Marshal(v)
			if err != nil {
				return v
			}
			var copy interface{}
			if err := json.Unmarshal(data, &copy); err != nil {
				return v
			}
			return copy
		}
		return v
	}
}

// getOrCreateMap retrieves a nested map from parent[key], creating it if missing or not a map.
func configMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func getOrCreateMap(parent map[string]interface{}, key string) map[string]interface{} {
	if v, ok := parent[key]; ok {
		if m, ok := v.(map[string]interface{}); ok {
			return m
		}
	}
	m := make(map[string]interface{})
	parent[key] = m
	return m
}
