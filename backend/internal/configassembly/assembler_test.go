package configassembly

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func testModelCatalog() []CatalogModel {
	smart := "smart"
	balanced := "balanced"
	fast := "fast"
	nebiusQwen := "nebius/Qwen/Qwen3.5-397B-A17B"
	nebiusMinimax := "nebius/MiniMaxAI/MiniMax-M2.5"
	nebiusGPT := "nebius/openai/gpt-oss-120b"
	codexGPT55 := "codex/gpt-5.5"
	return []CatalogModel{
		{ID: "qwen/qwen3.5-397b", Label: "Qwen 3.5 397B", Source: "platform", Tier: &smart, GatewayModelID: &nebiusQwen, Provider: "nebius"},
		{ID: "minimax/minimax-m2.5", Label: "MiniMax M2.5", Source: "platform", Tier: &balanced, GatewayModelID: &nebiusMinimax, Provider: "nebius"},
		{ID: "openai/gpt-oss-120b", Label: "GPT OSS 120B", Source: "platform", Tier: &fast, GatewayModelID: &nebiusGPT, Provider: "nebius"},
		{ID: "anthropic/claude-sonnet-4-6", Label: "Claude Sonnet 4.6", Source: "byok", Provider: "anthropic"},
		{ID: "anthropic/claude-opus-4-6", Label: "Claude Opus 4.6", Source: "byok", Provider: "anthropic"},
		{ID: "anthropic/claude-haiku-4-5", Label: "Claude Haiku 4.5", Source: "byok", Provider: "anthropic"},
		{ID: "openai/gpt-4o", Label: "GPT-4o", Source: "byok", Provider: "openai"},
		{ID: "openai/gpt-4o-mini", Label: "GPT-4o Mini", Source: "byok", Provider: "openai"},
		{ID: "openai/o3", Label: "o3", Source: "byok", Provider: "openai"},
		{ID: "google/gemini-2.5-flash", Label: "Gemini 2.5 Flash", Source: "byok", Provider: "google"},
		{ID: "google/gemini-2.5-pro", Label: "Gemini 2.5 Pro", Source: "byok", Provider: "google"},
		{ID: "google/gemini-2.0-flash", Label: "Gemini 2.0 Flash", Source: "byok", Provider: "google"},
		{ID: "openai-codex/gpt-5.5", Label: "GPT-5.5", Source: "subscription", GatewayModelID: &codexGPT55, Provider: "openai-codex"},
		{ID: "openai-codex/gpt-5.4", Label: "GPT-5.4", Source: "subscription", Provider: "openai-codex"},
		{ID: "openrouter/meta-llama/llama-3.1-8b-instruct", Label: "Llama 3.1 8B Instruct", Source: "byok", Provider: "openrouter"},
		{ID: "openrouter/openai/gpt-4o", Label: "GPT-4o (OpenRouter)", Source: "byok", Provider: "openrouter"},
	}
}

func mustUnmarshalMap(t *testing.T, data []byte) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return m
}

func getNestedMap(m map[string]interface{}, keys ...string) (map[string]interface{}, bool) {
	cur := m
	for _, k := range keys {
		v, ok := cur[k]
		if !ok {
			return nil, false
		}
		next, ok := v.(map[string]interface{})
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func TestEmptyCapabilities_PlatformDefaults(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// Check gateway defaults (port and bind are CLI-only, not in config)
	gw, ok := getNestedMap(m, "gateway")
	if !ok {
		t.Fatal("missing gateway")
	}
	if _, hasPort := gw["port"]; hasPort {
		t.Error("gateway.port should not be in config (set via CLI flag)")
	}
	if _, hasBind := gw["bind"]; hasBind {
		t.Error("gateway.bind should not be in config (set via CLI flag)")
	}
	// gateway.mode must be "local" so the gateway accepts token-based auth
	// without requiring device pairing for WebSocket connections (e.g. TUI).
	if gw["mode"] != "local" {
		t.Errorf("gateway.mode = %v, want \"local\"", gw["mode"])
	}
	// gateway.auth.mode must be "token" — makes sharedAuthOk=true when a client
	// provides the OPENCLAW_GATEWAY_TOKEN, enabling roleCanSkipDeviceIdentity
	// and bypassing device pairing entirely. The token is set via env var in
	// init-openclaw.sh for both the gateway and PTY server (TUI).
	gwAuth, hasAuth := getNestedMap(m, "gateway", "auth")
	if !hasAuth {
		t.Fatal("missing gateway.auth")
	}
	if gwAuth["mode"] != "token" {
		t.Errorf("gateway.auth.mode = %v, want \"token\"", gwAuth["mode"])
	}
	controlUi, ok := getNestedMap(m, "gateway", "controlUi")
	if !ok {
		t.Fatal("missing gateway.controlUi")
	}
	if controlUi["enabled"] != true {
		t.Errorf("gateway.controlUi.enabled = %v, want true", controlUi["enabled"])
	}
	if controlUi["allowInsecureAuth"] != true {
		t.Errorf("gateway.controlUi.allowInsecureAuth = %v, want true", controlUi["allowInsecureAuth"])
	}
	// dangerouslyDisableDeviceAuth must be true — the gateway reads auth tokens
	// only from WebSocket connect JSON, not HTTP headers. The browser doesn't have
	// OPENCLAW_GATEWAY_TOKEN, so this flag bypasses device identity for Control UI
	// clients. Security is enforced at the auth proxy layer (machine JWT).
	if controlUi["dangerouslyDisableDeviceAuth"] != true {
		t.Errorf("gateway.controlUi.dangerouslyDisableDeviceAuth = %v, want true", controlUi["dangerouslyDisableDeviceAuth"])
	}
	// dangerouslyAllowHostHeaderOriginFallback must NOT be present — allowedOrigins
	// is intentionally not set (auth proxy rewrites Origin header).
	if _, has := controlUi["dangerouslyAllowHostHeaderOriginFallback"]; has {
		t.Error("gateway.controlUi.dangerouslyAllowHostHeaderOriginFallback should not be present")
	}
	reload, ok := getNestedMap(m, "gateway", "reload")
	if !ok {
		t.Fatal("missing gateway.reload")
	}
	if reload["mode"] != "hot" {
		t.Errorf("gateway.reload.mode = %v, want hot", reload["mode"])
	}
	// trustedProxies must NOT be set — the gateway binds to loopback, so all
	// connections are local. Setting trustedProxies=["127.0.0.1"] causes
	// resolveClientIp to return undefined for direct localhost connections
	// (TUI), making isLocalClient=false and breaking silent local pairing.
	if _, hasTp := gw["trustedProxies"]; hasTp {
		t.Error("gateway.trustedProxies should not be present (breaks TUI silent local pairing)")
	}

	// Check skills.allowBundled is empty
	skills, ok := getNestedMap(m, "skills")
	if !ok {
		t.Fatal("missing skills")
	}
	ab, ok := skills["allowBundled"]
	if !ok {
		t.Fatal("missing skills.allowBundled")
	}
	abSlice, ok := ab.([]interface{})
	if !ok {
		t.Fatalf("skills.allowBundled is %T, want []interface{}", ab)
	}
	if len(abSlice) != 0 {
		t.Errorf("skills.allowBundled = %v, want empty", abSlice)
	}
}

func TestEnableTelegram(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "telegram",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"channels": map[string]interface{}{
						"telegram": map[string]interface{}{
							"enabled": true,
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	tg, ok := getNestedMap(m, "channels", "telegram")
	if !ok {
		t.Fatal("missing channels.telegram")
	}
	if tg["enabled"] != true {
		t.Errorf("channels.telegram.enabled = %v, want true", tg["enabled"])
	}
}

func TestTelegramAndWebSearch(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "telegram",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"channels": map[string]interface{}{
						"telegram": map[string]interface{}{
							"enabled": true,
						},
					},
				},
			},
			{
				EntryID:   "web-search",
				EntryType: "skill",
				ConfigTemplate: map[string]interface{}{
					"skills": map[string]interface{}{
						"entries": map[string]interface{}{
							"web-search": map[string]interface{}{
								"enabled": true,
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// Telegram channel present
	tg, ok := getNestedMap(m, "channels", "telegram")
	if !ok {
		t.Fatal("missing channels.telegram")
	}
	if tg["enabled"] != true {
		t.Errorf("channels.telegram.enabled = %v, want true", tg["enabled"])
	}

	// Web search skill entry present
	ws, ok := getNestedMap(m, "skills", "entries", "web-search")
	if !ok {
		t.Fatal("missing skills.entries.web-search")
	}
	if ws["enabled"] != true {
		t.Errorf("skills.entries.web-search.enabled = %v, want true", ws["enabled"])
	}

	// skills.allowBundled = ["web-search"]
	skills, ok := getNestedMap(m, "skills")
	if !ok {
		t.Fatal("missing skills")
	}
	ab := skills["allowBundled"].([]interface{})
	if len(ab) != 1 || ab[0] != "web-search" {
		t.Errorf("skills.allowBundled = %v, want [web-search]", ab)
	}
}

func TestAssembleConfig_OpenAICodexProviderOverrides(t *testing.T) {
	// Codex provider injected when credential flag exists in DB
	params := AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		ProxyBaseURL: "http://192.168.100.1:4000",
		Credentials: map[string]string{
			"openai-codex": "vm-managed",
		},
	}

	data, err := AssembleConfig(params)
	if err != nil {
		t.Fatalf("AssembleConfig error: %v", err)
	}

	m := mustUnmarshalMap(t, data)
	providers, ok := getNestedMap(m, "models", "providers")
	if !ok {
		t.Fatalf("missing models.providers")
	}
	cfg, _ := providers["openai-codex"].(map[string]interface{})
	if cfg == nil {
		t.Fatalf("missing openai-codex provider")
	}

	// Codex must point directly at chatgpt.com, not the proxy
	if got := cfg["baseUrl"]; got != "https://chatgpt.com/backend-api" {
		t.Fatalf("baseUrl = %v, want https://chatgpt.com/backend-api", got)
	}
	if got := cfg["api"]; got != "openai-codex-responses" {
		t.Fatalf("api = %v, want openai-codex-responses", got)
	}
	// Codex must NOT have an apiKey — auth comes from auth-profiles.json
	if _, hasAPIKey := cfg["apiKey"]; hasAPIKey {
		t.Errorf("openai-codex provider must not have apiKey field (uses OAuth via auth-profiles)")
	}

	// Verify models are populated from catalog
	models, ok := cfg["models"].([]interface{})
	if !ok {
		t.Fatalf("models is not an array: %T", cfg["models"])
	}
	if len(models) != 2 {
		t.Fatalf("models count = %d, want 2 (gpt-5.5, gpt-5.4)", len(models))
	}
	// Check first model has bare ID (no provider prefix)
	first, _ := models[0].(map[string]interface{})
	if first["id"] != "gpt-5.5" {
		t.Errorf("first model id = %v, want gpt-5.5", first["id"])
	}
	if first["api"] != "openai-codex-responses" {
		t.Errorf("first model api = %v, want openai-codex-responses", first["api"])
	}
}

func TestAssembleConfig_CodexAuthProfiles(t *testing.T) {
	// With codex credential flag: auth.profiles should be emitted
	params := AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		ProxyBaseURL: "http://192.168.100.1:4000",
		Credentials: map[string]string{
			"openai-codex": "vm-managed",
		},
	}

	data, err := AssembleConfig(params)
	if err != nil {
		t.Fatalf("AssembleConfig error: %v", err)
	}

	m := mustUnmarshalMap(t, data)
	authProfiles, ok := getNestedMap(m, "auth", "profiles")
	if !ok {
		t.Fatalf("missing auth.profiles when codex credential flag exists")
	}
	codexProfile, _ := authProfiles["openai-codex:default"].(map[string]interface{})
	if codexProfile == nil {
		t.Fatalf("missing auth.profiles[openai-codex:default]")
	}
	if got := codexProfile["provider"]; got != "openai-codex" {
		t.Errorf("auth.profiles[openai-codex:default].provider = %v, want openai-codex", got)
	}
	if got := codexProfile["mode"]; got != "oauth" {
		t.Errorf("auth.profiles[openai-codex:default].mode = %v, want oauth", got)
	}
}

func TestAssembleConfig_NoCodexAuthProfilesWithoutCredential(t *testing.T) {
	// Without codex credential flag: no auth.profiles for codex
	params := AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		ProxyBaseURL: "http://192.168.100.1:4000",
		Credentials:  map[string]string{},
	}

	data, err := AssembleConfig(params)
	if err != nil {
		t.Fatalf("AssembleConfig error: %v", err)
	}

	m := mustUnmarshalMap(t, data)
	if authMap, ok := m["auth"].(map[string]interface{}); ok {
		if profilesMap, ok := authMap["profiles"].(map[string]interface{}); ok {
			if _, hasCodexProfile := profilesMap["openai-codex:default"]; hasCodexProfile {
				t.Errorf("auth.profiles[openai-codex:default] must not exist without codex credential flag")
			}
		}
	}
}

func TestConfigOverrides(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "telegram",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"channels": map[string]interface{}{
						"telegram": map[string]interface{}{
							"enabled": true,
						},
					},
				},
				ConfigOverrides: map[string]interface{}{
					"channels": map[string]interface{}{
						"telegram": map[string]interface{}{
							"groupPolicy": "open",
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	tg, ok := getNestedMap(m, "channels", "telegram")
	if !ok {
		t.Fatal("missing channels.telegram")
	}
	if tg["enabled"] != true {
		t.Errorf("channels.telegram.enabled = %v, want true (should be preserved after merge)", tg["enabled"])
	}
	if tg["groupPolicy"] != "open" {
		t.Errorf("channels.telegram.groupPolicy = %v, want open", tg["groupPolicy"])
	}
}

func TestTelegramGroupConfig(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "telegram",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"channels": map[string]interface{}{
						"telegram": map[string]interface{}{
							"enabled":  true,
							"dmPolicy": "pairing",
							"groups": map[string]interface{}{
								"*": map[string]interface{}{
									"requireMention": true,
								},
							},
						},
					},
				},
				ConfigOverrides: map[string]interface{}{
					"channels": map[string]interface{}{
						"telegram": map[string]interface{}{
							"groups": map[string]interface{}{
								"-1003839925040": map[string]interface{}{
									"groupPolicy":    "open",
									"requireMention": false,
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	tg, ok := getNestedMap(m, "channels", "telegram")
	if !ok {
		t.Fatal("missing channels.telegram")
	}
	if tg["enabled"] != true {
		t.Errorf("channels.telegram.enabled = %v, want true", tg["enabled"])
	}
	if tg["dmPolicy"] != "pairing" {
		t.Errorf("channels.telegram.dmPolicy = %v, want pairing", tg["dmPolicy"])
	}

	groups, ok := getNestedMap(m, "channels", "telegram", "groups")
	if !ok {
		t.Fatal("missing channels.telegram.groups")
	}
	// Default wildcard preserved from template
	star, ok := groups["*"].(map[string]interface{})
	if !ok {
		t.Fatal("missing channels.telegram.groups.* (default group config)")
	}
	if star["requireMention"] != true {
		t.Errorf("groups.*.requireMention = %v, want true", star["requireMention"])
	}
	// Per-group override merged
	specific, ok := groups["-1003839925040"].(map[string]interface{})
	if !ok {
		t.Fatal("missing channels.telegram.groups.-1003839925040")
	}
	if specific["groupPolicy"] != "open" {
		t.Errorf("groups.-1003839925040.groupPolicy = %v, want open", specific["groupPolicy"])
	}
	if specific["requireMention"] != false {
		t.Errorf("groups.-1003839925040.requireMention = %v, want false", specific["requireMention"])
	}
}

func TestIdentity(t *testing.T) {
	name := "MyBot"
	avatar := "robot"
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
		Identity: &Identity{
			Name:   &name,
			Avatar: &avatar,
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	assistant, ok := getNestedMap(m, "ui", "assistant")
	if !ok {
		t.Fatal("missing ui.assistant")
	}
	if assistant["name"] != "MyBot" {
		t.Errorf("ui.assistant.name = %v, want MyBot", assistant["name"])
	}
	if assistant["avatar"] != "robot" {
		t.Errorf("ui.assistant.avatar = %v, want robot", assistant["avatar"])
	}
}

func TestChannelConfigNoBaseUrlInjected(t *testing.T) {
	// Channel configs should not contain baseUrl — the gateway connects
	// directly to channel APIs and rejects unrecognized keys.
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "telegram",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"channels": map[string]interface{}{
						"telegram": map[string]interface{}{
							"enabled": true,
						},
					},
				},
			},
		},
		Credentials: map[string]string{
			"telegram": "present",
		},
		ProxyBaseURL: "http://169.254.169.253:4000",
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	tg, ok := getNestedMap(m, "channels", "telegram")
	if !ok {
		t.Fatal("missing channels.telegram")
	}
	if _, hasBaseUrl := tg["baseUrl"]; hasBaseUrl {
		t.Errorf("channels.telegram should not contain baseUrl, got %v", tg["baseUrl"])
	}
}

func TestDeepMergeDoesNotMutateBase(t *testing.T) {
	base := map[string]interface{}{
		"a": map[string]interface{}{
			"b": "original",
		},
	}
	overlay := map[string]interface{}{
		"a": map[string]interface{}{
			"c": "new",
		},
	}

	result := deepMerge(base, overlay)

	// base should be unmodified
	aBase := base["a"].(map[string]interface{})
	if _, ok := aBase["c"]; ok {
		t.Error("deepMerge mutated the base map")
	}

	// result should have both
	aResult := result["a"].(map[string]interface{})
	if aResult["b"] != "original" {
		t.Errorf("result[a][b] = %v, want original", aResult["b"])
	}
	if aResult["c"] != "new" {
		t.Errorf("result[a][c] = %v, want new", aResult["c"])
	}
}

func TestStripProtectedKeys(t *testing.T) {
	input := map[string]interface{}{
		"gateway":  map[string]interface{}{"port": float64(9999)},
		"channels": map[string]interface{}{"telegram": map[string]interface{}{"enabled": true}},
		"server":   map[string]interface{}{"host": "evil"},
	}

	result := StripProtectedKeys(input)

	if _, ok := result["gateway"]; ok {
		t.Error("gateway should have been stripped")
	}
	if _, ok := result["server"]; ok {
		t.Error("server should have been stripped")
	}
	if _, ok := result["channels"]; !ok {
		t.Error("channels should have survived")
	}
	if len(result) != 1 {
		t.Errorf("expected 1 key in result, got %d", len(result))
	}
}

func TestProtectedKeysNotMergedFromOverrides(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "telegram",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"channels": map[string]interface{}{
						"telegram": map[string]interface{}{
							"enabled": true,
						},
					},
				},
				ConfigOverrides: map[string]interface{}{
					"gateway": map[string]interface{}{
						"port": float64(9999),
					},
					"channels": map[string]interface{}{
						"telegram": map[string]interface{}{
							"groupPolicy": "open",
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// gateway.port must NOT be set — the override tried to inject port=9999 but
	// "gateway" is a protected key so the entire override is stripped. And port is
	// no longer in platform defaults (it's set via CLI flags only).
	gw, ok := getNestedMap(m, "gateway")
	if !ok {
		t.Fatal("missing gateway")
	}
	if _, hasPort := gw["port"]; hasPort {
		t.Errorf("gateway.port = %v, should not be present (override should have been stripped, no default)", gw["port"])
	}

	// channels override should still apply (not a protected key)
	tg, ok := getNestedMap(m, "channels", "telegram")
	if !ok {
		t.Fatal("missing channels.telegram")
	}
	if tg["groupPolicy"] != "open" {
		t.Errorf("channels.telegram.groupPolicy = %v, want open", tg["groupPolicy"])
	}
}

func TestPlatformDefaultsNotMutated(t *testing.T) {
	// Take a snapshot of platform defaults before
	before, _ := json.Marshal(platformDefaults)

	_, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "telegram",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"gateway": map[string]interface{}{
						"extra": "should-not-leak",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	after, _ := json.Marshal(platformDefaults)
	if string(before) != string(after) {
		t.Errorf("platformDefaults was mutated:\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestPortAndBindNotInConfig(t *testing.T) {
	// port and bind must NOT appear in assembled config — they're CLI-only.
	// The gateway's Zod schema rejects them in the config file.
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "telegram",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"channels": map[string]interface{}{
						"telegram": map[string]interface{}{"enabled": true},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	gw, ok := getNestedMap(m, "gateway")
	if !ok {
		t.Fatal("missing gateway")
	}
	if _, hasPort := gw["port"]; hasPort {
		t.Error("gateway.port must NOT appear in config (CLI-only)")
	}
	if _, hasBind := gw["bind"]; hasBind {
		t.Error("gateway.bind must NOT appear in config (CLI-only)")
	}
}

func TestMultipleSkillsInAllowBundled(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "web-search",
				EntryType: "skill",
				ConfigTemplate: map[string]interface{}{
					"skills": map[string]interface{}{
						"entries": map[string]interface{}{
							"web-search": map[string]interface{}{"enabled": true},
						},
					},
				},
			},
			{
				EntryID:   "code-interpreter",
				EntryType: "skill",
				ConfigTemplate: map[string]interface{}{
					"skills": map[string]interface{}{
						"entries": map[string]interface{}{
							"code-interpreter": map[string]interface{}{"enabled": true},
						},
					},
				},
			},
			{
				EntryID:   "telegram",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"channels": map[string]interface{}{
						"telegram": map[string]interface{}{"enabled": true},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// skills.allowBundled should contain both skills but not the channel
	skills, ok := getNestedMap(m, "skills")
	if !ok {
		t.Fatal("missing skills")
	}
	ab := skills["allowBundled"].([]interface{})
	if len(ab) != 2 {
		t.Fatalf("skills.allowBundled has %d entries, want 2: %v", len(ab), ab)
	}

	found := map[string]bool{}
	for _, v := range ab {
		found[v.(string)] = true
	}
	if !found["web-search"] {
		t.Error("skills.allowBundled missing web-search")
	}
	if !found["code-interpreter"] {
		t.Error("skills.allowBundled missing code-interpreter")
	}
}

func TestOverlayOrderMatters(t *testing.T) {
	// Later capabilities should override earlier ones for the same key
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "cap1",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"channels": map[string]interface{}{
						"shared": map[string]interface{}{
							"value": "first",
						},
					},
				},
			},
			{
				EntryID:   "cap2",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"channels": map[string]interface{}{
						"shared": map[string]interface{}{
							"value": "second",
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)
	shared, ok := getNestedMap(m, "channels", "shared")
	if !ok {
		t.Fatal("missing channels.shared")
	}
	if shared["value"] != "second" {
		t.Errorf("channels.shared.value = %v, want second (later cap should win)", shared["value"])
	}
}

func TestFindProtectedKeyViolations(t *testing.T) {
	tests := []struct {
		name       string
		input      map[string]interface{}
		wantCount  int
		wantSubstr string
	}{
		{
			name: "top-level gateway",
			input: map[string]interface{}{
				"gateway": map[string]interface{}{"port": 9999},
			},
			wantCount:  1,
			wantSubstr: "gateway",
		},
		{
			name: "top-level server",
			input: map[string]interface{}{
				"server": map[string]interface{}{"host": "evil"},
			},
			wantCount:  1,
			wantSubstr: "server",
		},
		{
			name: "no violations",
			input: map[string]interface{}{
				"channels": map[string]interface{}{"telegram": true},
			},
			wantCount: 0,
		},
		{
			name: "multiple violations",
			input: map[string]interface{}{
				"gateway": map[string]interface{}{},
				"ui":      map[string]interface{}{},
				"server":  map[string]interface{}{},
			},
			wantCount: 3,
		},
		{
			name: "nested protected path via prefix",
			input: map[string]interface{}{
				"channels": map[string]interface{}{
					"telegram": map[string]interface{}{
						"gateway": map[string]interface{}{"port": 9999},
					},
				},
			},
			wantCount:  1,
			wantSubstr: "gateway",
		},
		{
			name: "nested skills prefix",
			input: map[string]interface{}{
				"channels": map[string]interface{}{
					"slack": map[string]interface{}{
						"skills": map[string]interface{}{"allowBundled": []interface{}{"evil"}},
					},
				},
			},
			wantCount:  1,
			wantSubstr: "skills",
		},
		{
			name: "safe nested key not blocked",
			input: map[string]interface{}{
				"channels": map[string]interface{}{
					"telegram": map[string]interface{}{
						"token": "abc123",
					},
				},
			},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			violations := FindProtectedKeyViolations(tt.input, "")
			if len(violations) != tt.wantCount {
				t.Errorf("got %d violations, want %d: %v", len(violations), tt.wantCount, violations)
			}
		})
	}
}

func TestPlatformDefaults_Commands(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	cmds, ok := getNestedMap(m, "commands")
	if !ok {
		t.Fatal("missing commands")
	}
	if cmds["native"] != "auto" {
		t.Errorf("commands.native = %v, want auto", cmds["native"])
	}
	if cmds["nativeSkills"] != "auto" {
		t.Errorf("commands.nativeSkills = %v, want auto", cmds["nativeSkills"])
	}
	if cmds["restart"] != true {
		t.Errorf("commands.restart = %v, want true", cmds["restart"])
	}
	if cmds["ownerDisplay"] != "raw" {
		t.Errorf("commands.ownerDisplay = %v, want raw", cmds["ownerDisplay"])
	}
}

func TestPlatformDefaults_Session(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	session, ok := getNestedMap(m, "session")
	if !ok {
		t.Fatal("missing session")
	}
	if session["dmScope"] != "per-channel-peer" {
		t.Errorf("session.dmScope = %v, want per-channel-peer", session["dmScope"])
	}
}

func TestPlatformDefaults_DenyCommands(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	nodes, ok := getNestedMap(m, "gateway", "nodes")
	if !ok {
		t.Fatal("missing gateway.nodes")
	}
	dc, ok := nodes["denyCommands"]
	if !ok {
		t.Fatal("missing gateway.nodes.denyCommands")
	}
	dcSlice, ok := dc.([]interface{})
	if !ok {
		t.Fatalf("gateway.nodes.denyCommands is %T, want []interface{}", dc)
	}

	expected := map[string]bool{
		"camera.snap":   true,
		"camera.clip":   true,
		"screen.record": true,
		"calendar.add":  true,
		"contacts.add":  true,
		"reminders.add": true,
	}
	if len(dcSlice) != len(expected) {
		t.Fatalf("gateway.nodes.denyCommands has %d entries, want %d: %v", len(dcSlice), len(expected), dcSlice)
	}
	for _, v := range dcSlice {
		s, ok := v.(string)
		if !ok {
			t.Errorf("denyCommands entry is %T, want string", v)
			continue
		}
		if !expected[s] {
			t.Errorf("unexpected denyCommands entry: %s", s)
		}
	}
}

func TestAgentsDefaults(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
		DefaultModel: "anthropic/claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// agents.defaults.model.primary
	model, ok := getNestedMap(m, "agents", "defaults", "model")
	if !ok {
		t.Fatal("missing agents.defaults.model")
	}
	if model["primary"] != "anthropic/claude-sonnet-4-6" {
		t.Errorf("agents.defaults.model.primary = %v, want anthropic/claude-sonnet-4-6", model["primary"])
	}

	// agents.defaults.models should contain the model key
	defaults, ok := getNestedMap(m, "agents", "defaults")
	if !ok {
		t.Fatal("missing agents.defaults")
	}
	models, ok := defaults["models"].(map[string]interface{})
	if !ok {
		t.Fatalf("agents.defaults.models is %T, want map[string]interface{}", defaults["models"])
	}
	if _, ok := models["anthropic/claude-sonnet-4-6"]; !ok {
		t.Error("agents.defaults.models missing anthropic/claude-sonnet-4-6 key")
	}

	// agents.defaults.workspace
	if defaults["workspace"] != "/home/openclaw/.openclaw/workspace" {
		t.Errorf("agents.defaults.workspace = %v, want /home/openclaw/.openclaw/workspace", defaults["workspace"])
	}
}

func TestAgentsDefaults_NoInstructions(t *testing.T) {
	// instructions is NOT a valid gateway schema key under agents.defaults.
	// Hostname info is delivered via /workspace/IDENTITY.md (init script), not config.
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
		DefaultModel: "anthropic/claude-sonnet-4-6",
		VMHostname:   "m-mybot.openclawmachines.com",
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	defaults, ok := getNestedMap(m, "agents", "defaults")
	if !ok {
		t.Fatal("missing agents.defaults")
	}
	if _, exists := defaults["instructions"]; exists {
		t.Error("agents.defaults.instructions should not be set (not in gateway schema)")
	}
}

func TestAgentsDefaults_NoModel(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
		DefaultModel: "",
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	if _, ok := m["agents"]; ok {
		t.Error("agents section should not be present when DefaultModel is empty")
	}
}

func TestAssembleConfig_WithAgents(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		DefaultModel: "anthropic/claude-sonnet-4-6",
		Agents: []AgentDefinition{
			{AgentID: "support", Name: "Support Bot", IsDefault: true, IdentityEmoji: "\U0001F916"},
			{AgentID: "creative", Name: "Creative Writer", Model: "anthropic/claude-opus-4-6", IdentityEmoji: "\u270D\uFE0F"},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)
	agents, ok := getNestedMap(m, "agents")
	if !ok {
		t.Fatal("missing agents key")
	}
	list, ok := agents["list"].([]interface{})
	if !ok || len(list) != 2 {
		t.Fatalf("agents.list: got %v, want 2-element array", agents["list"])
	}
	a0 := list[0].(map[string]interface{})
	if a0["id"] != "support" {
		t.Errorf("agents.list[0].id = %v, want support", a0["id"])
	}
	if a0["default"] != true {
		t.Errorf("agents.list[0].default = %v, want true", a0["default"])
	}
	if a0["workspace"] != "/home/openclaw/.openclaw/workspace" {
		t.Errorf("default agent workspace = %v, want /home/openclaw/.openclaw/workspace", a0["workspace"])
	}
	a1 := list[1].(map[string]interface{})
	if a1["id"] != "creative" {
		t.Errorf("agents.list[1].id = %v, want creative", a1["id"])
	}
	model1 := a1["model"].(map[string]interface{})
	if model1["primary"] != "anthropic/claude-opus-4-6" {
		t.Errorf("agent model.primary = %v, want anthropic/claude-opus-4-6", model1["primary"])
	}
	if a1["workspace"] != "/home/openclaw/.openclaw/workspace-creative" {
		t.Errorf("non-default workspace = %v, want workspace-creative", a1["workspace"])
	}
	defaults := agents["defaults"].(map[string]interface{})
	models := defaults["models"].(map[string]interface{})
	if _, ok := models["anthropic/claude-opus-4-6"]; !ok {
		t.Error("override model missing from agents.defaults.models")
	}
}

func TestAssembleConfig_NoAgents_BackwardCompatible(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		DefaultModel: "anthropic/claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)
	agents, ok := getNestedMap(m, "agents")
	if !ok {
		t.Fatal("missing agents key")
	}
	if _, ok := agents["list"]; ok {
		t.Error("agents.list should not exist when no agents provided")
	}
}

func TestAssembleConfig_IncludesAllPlatformModels(t *testing.T) {
	catalog := testModelCatalog()
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: catalog,
		MachineID:    "m-1",
		DefaultModel: "openai/gpt-oss-120b",
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)
	models := mustGetNestedMap(t, m, "agents", "defaults", "models")

	// All platform models should be present in the catalog
	for _, cm := range catalog {
		if cm.Source != "platform" {
			continue
		}
		gatewayRef := mapModelFromCatalog(catalog, cm.ID)
		if _, ok := models[gatewayRef]; !ok {
			t.Errorf("platform model %q (%s) missing from agents.defaults.models", cm.ID, gatewayRef)
		}
	}
}

// TestAssembleConfig_BYOKModelsInCatalog verifies that when a user has BYOK
// credentials (e.g. anthropic), the corresponding models appear in
// agents.defaults.models so the gateway's models.list allowlist includes them.
// Without this, switching to an Anthropic model would fail because the gateway
// filters models.list to only those in agents.defaults.models.
func TestAssembleConfig_BYOKModelsInCatalog(t *testing.T) {
	tests := []struct {
		name         string
		credentials  map[string]string
		defaultModel string
		wantModels   []string // models that MUST appear in agents.defaults.models
	}{
		{
			name:         "anthropic credential adds claude models",
			credentials:  map[string]string{"anthropic": "present"},
			defaultModel: "anthropic/claude-sonnet-4-6",
			wantModels:   []string{"anthropic/claude-sonnet-4-6", "anthropic/claude-opus-4-6", "anthropic/claude-haiku-4-5"},
		},
		{
			name:         "anthropic credential with nebius default still adds claude models",
			credentials:  map[string]string{"anthropic": "present"},
			defaultModel: "qwen/qwen3.5-397b", // user on platform model but has anthropic key
			wantModels:   []string{"anthropic/claude-sonnet-4-6", "anthropic/claude-opus-4-6"},
		},
		{
			name:         "openai credential adds openai models",
			credentials:  map[string]string{"openai": "present"},
			defaultModel: "openai/gpt-4o",
			wantModels:   []string{"openai/gpt-4o"},
		},
		{
			name:         "no credentials still has platform models",
			credentials:  map[string]string{},
			defaultModel: "openai/gpt-oss-120b",
			wantModels: []string{
				"nebius/Qwen/Qwen3.5-397B-A17B",
				"nebius/MiniMaxAI/MiniMax-M2.5",
				"nebius/openai/gpt-oss-120b",
			},
		},
	}

	mc := testModelCatalog()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := AssembleConfig(AssemblyParams{
				MachineID:    "m-1",
				ProxyBaseURL: "http://192.168.100.1:4000",
				Credentials:  tt.credentials,
				DefaultModel: tt.defaultModel,
				NativeMode:   true,
				ModelCatalog: mc,
			})
			if err != nil {
				t.Fatalf("AssembleConfig: %v", err)
			}
			m := mustUnmarshalMap(t, data)
			catalog := mustGetNestedMap(t, m, "agents", "defaults", "models")

			for _, wantModel := range tt.wantModels {
				// Platform models get mapped (e.g. "qwen/qwen3.5-397b" → "nebius/Qwen/...")
				resolved := mapModelFromCatalog(mc, wantModel)
				if _, ok := catalog[resolved]; !ok {
					t.Errorf("model %q (resolved: %q) missing from agents.defaults.models; catalog keys: %v",
						wantModel, resolved, mapKeys(catalog))
				}
			}
		})
	}
}

func TestAssembleConfig_OpenAIAgentRuntimePolicy(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		MachineID:          "m-1",
		ProxyBaseURL:       "http://192.168.100.1:4000",
		Credentials:        map[string]string{"openai": "present"},
		DefaultModel:       "openai/gpt-4o",
		OpenAIAgentRuntime: "pi",
		NativeMode:         true,
		ModelCatalog:       testModelCatalog(),
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)
	models := mustGetNestedMap(t, m, "agents", "defaults", "models")
	entry := mustGetNestedMap(t, models, "openai/gpt-4o", "agentRuntime")
	if entry["id"] != "pi" {
		t.Fatalf("openai/gpt-4o agentRuntime.id = %v, want pi", entry["id"])
	}
}

func TestAssembleConfig_OpenAICodexMappedRuntimePolicy(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		MachineID:          "m-1",
		ProxyBaseURL:       "http://192.168.100.1:4000",
		Credentials:        map[string]string{"openai-codex": "vm-managed"},
		DefaultModel:       "openai-codex/gpt-5.5",
		OpenAIAgentRuntime: "codex",
		NativeMode:         true,
		ModelCatalog:       testModelCatalog(),
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)
	primary := mustGetNestedMap(t, m, "agents", "defaults", "model")
	if primary["primary"] != "codex/gpt-5.5" {
		t.Fatalf("agents.defaults.model.primary = %v, want codex/gpt-5.5", primary["primary"])
	}
	models := mustGetNestedMap(t, m, "agents", "defaults", "models")
	entry := mustGetNestedMap(t, models, "codex/gpt-5.5", "agentRuntime")
	if entry["id"] != "codex" {
		t.Fatalf("codex/gpt-5.5 agentRuntime.id = %v, want codex", entry["id"])
	}
}

func TestAssembleSeedConfig_OpenAIAgentRuntimePolicy(t *testing.T) {
	data, err := AssembleSeedConfig(SeedParams{
		MachineID:          "m-1",
		DefaultModel:       "openai/gpt-4o",
		Providers:          []string{"openai"},
		ProxyBaseURL:       "http://192.168.100.1:4000",
		ModelCatalog:       testModelCatalog(),
		OpenAIAgentRuntime: "pi",
	})
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)
	models := mustGetNestedMap(t, m, "agents", "defaults", "models")
	entry := mustGetNestedMap(t, models, "openai/gpt-4o", "agentRuntime")
	if entry["id"] != "pi" {
		t.Fatalf("seed openai/gpt-4o agentRuntime.id = %v, want pi", entry["id"])
	}
}

func TestAssembleSeedConfig_ModelFallbacks(t *testing.T) {
	data, err := AssembleSeedConfig(SeedParams{
		MachineID:      "m-1",
		DefaultModel:   "openai-codex/gpt-5.5",
		ModelFallbacks: []string{"anthropic/claude-opus-4-6", "qwen/qwen3.5-397b", "minimax/minimax-m2.5"},
		Providers:      []string{"openai-codex", "anthropic"},
		ProxyBaseURL:   "http://192.168.100.1:4000",
		ModelCatalog:   testModelCatalog(),
	})
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)
	model := mustGetNestedMap(t, m, "agents", "defaults", "model")
	if model["primary"] != "codex/gpt-5.5" {
		t.Fatalf("primary = %v, want codex/gpt-5.5", model["primary"])
	}
	fallbacks, ok := model["fallbacks"].([]interface{})
	if !ok {
		t.Fatalf("fallbacks missing or wrong type: %T", model["fallbacks"])
	}
	got := []string{fallbacks[0].(string), fallbacks[1].(string), fallbacks[2].(string)}
	want := []string{"anthropic/claude-opus-4-6", "nebius/Qwen/Qwen3.5-397B-A17B", "nebius/MiniMaxAI/MiniMax-M2.5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fallbacks = %v, want %v", got, want)
	}

	models := mustGetNestedMap(t, m, "agents", "defaults", "models")
	for _, id := range append([]string{"codex/gpt-5.5"}, want...) {
		if _, ok := models[id]; !ok {
			t.Fatalf("models catalog missing fallback/default %q; keys=%v", id, mapKeys(models))
		}
	}
}

func TestAssembleConfig_ModelFallbacks(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog:   testModelCatalog(),
		MachineID:      "m-1",
		NativeMode:     true,
		DefaultModel:   "openai-codex/gpt-5.5",
		ModelFallbacks: []string{"anthropic/claude-opus-4-6", "qwen/qwen3.5-397b"},
		Credentials: map[string]string{
			"openai-codex": "present",
			"anthropic":    "present",
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)
	model := mustGetNestedMap(t, m, "agents", "defaults", "model")
	if model["primary"] != "codex/gpt-5.5" {
		t.Fatalf("primary = %v, want codex/gpt-5.5", model["primary"])
	}
	fallbacks := model["fallbacks"].([]interface{})
	got := []string{fallbacks[0].(string), fallbacks[1].(string)}
	want := []string{"anthropic/claude-opus-4-6", "nebius/Qwen/Qwen3.5-397B-A17B"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fallbacks = %v, want %v", got, want)
	}
}

func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestAssembleConfig_AgentIdentity(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		DefaultModel: "anthropic/claude-sonnet-4-6",
		Agents: []AgentDefinition{
			{AgentID: "bot", Name: "Bot", IsDefault: true, IdentityEmoji: "\U0001F916", IdentityAvatar: "robot"},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)
	agents, _ := getNestedMap(m, "agents")
	list := agents["list"].([]interface{})
	a0 := list[0].(map[string]interface{})
	identity := a0["identity"].(map[string]interface{})
	if identity["emoji"] != "\U0001F916" {
		t.Errorf("identity.emoji = %v", identity["emoji"])
	}
	if identity["avatar"] != "robot" {
		t.Errorf("identity.avatar = %v", identity["avatar"])
	}
}

func TestAllowedOrigins_Wildcard(t *testing.T) {
	// allowedOrigins must be ["*"] — the auth proxy rewrites the Origin header
	// to its internal address (e.g. http://127.0.0.1:18789), so explicit
	// origin lists never match the real browser origin. OpenClaw v2026.3.8+
	// rejects unknown origins by default, so we must allow all.
	// Security is enforced at the auth proxy layer (machine JWT).
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog:    testModelCatalog(),
		MachineID:       "m-1",
		Capabilities:    nil,
		VMHostname:      "m-mybot.openclawmachines.com",
		AccountHostname: "user-23.openclawmachines.com",
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	controlUi, ok := getNestedMap(m, "gateway", "controlUi")
	if !ok {
		t.Fatal("missing gateway.controlUi")
	}
	origins, has := controlUi["allowedOrigins"]
	if !has {
		t.Fatal("gateway.controlUi.allowedOrigins should be present")
	}
	originsList, ok := origins.([]interface{})
	if !ok || len(originsList) != 1 || originsList[0] != "*" {
		t.Errorf("gateway.controlUi.allowedOrigins should be [\"*\"], got %v", origins)
	}
}

func TestProtectedKeys_AgentsCommandsSession(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "telegram",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"channels": map[string]interface{}{
						"telegram": map[string]interface{}{"enabled": true},
					},
				},
				ConfigOverrides: map[string]interface{}{
					"agents": map[string]interface{}{
						"defaults": map[string]interface{}{
							"model": "evil-model",
						},
					},
					"commands": map[string]interface{}{
						"native": "never",
					},
					"session": map[string]interface{}{
						"dmScope": "global",
					},
					"channels": map[string]interface{}{
						"telegram": map[string]interface{}{
							"groupPolicy": "open",
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// agents should not be present (no DefaultModel set, and override was stripped)
	if _, ok := m["agents"]; ok {
		t.Error("agents should have been stripped from overrides")
	}

	// commands should be platform defaults, not the override
	cmds, ok := getNestedMap(m, "commands")
	if !ok {
		t.Fatal("missing commands")
	}
	if cmds["native"] != "auto" {
		t.Errorf("commands.native = %v, want auto (platform default, not overridden)", cmds["native"])
	}

	// session should be platform defaults, not the override
	session, ok := getNestedMap(m, "session")
	if !ok {
		t.Fatal("missing session")
	}
	if session["dmScope"] != "per-channel-peer" {
		t.Errorf("session.dmScope = %v, want per-channel-peer (platform default, not overridden)", session["dmScope"])
	}

	// channels override should still apply (not a protected key)
	tg, ok := getNestedMap(m, "channels", "telegram")
	if !ok {
		t.Fatal("missing channels.telegram")
	}
	if tg["groupPolicy"] != "open" {
		t.Errorf("channels.telegram.groupPolicy = %v, want open", tg["groupPolicy"])
	}
}

func TestBrowserCdpUrlInjection(t *testing.T) {
	// When BrowserVMIP is set (machine has a paired browser VM), the full browser
	// block is emitted unconditionally using the paired VM's IP directly.
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-browser",
		BrowserVMIP:  "192.168.100.110",
		BridgeIP:     "192.168.100.1",
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)
	browser, ok := getNestedMap(m, "browser")
	if !ok {
		t.Fatal("missing browser section")
	}
	if browser["cdpUrl"] != "http://192.168.100.1:9222" {
		t.Errorf("browser.cdpUrl = %v, want http://192.168.100.1:9222", browser["cdpUrl"])
	}
	if browser["enabled"] != true {
		t.Errorf("browser.enabled = %v, want true", browser["enabled"])
	}
	if browser["attachOnly"] != true {
		t.Errorf("browser.attachOnly = %v, want true", browser["attachOnly"])
	}
	ssrf, ok := getNestedMap(browser, "ssrfPolicy")
	if !ok {
		t.Fatal("missing browser.ssrfPolicy — 4.14+ fail-closed default blocks private cdpUrl without allowlist")
	}
	hosts, _ := ssrf["allowedHostnames"].([]interface{})
	found := false
	for _, h := range hosts {
		if h == "192.168.100.1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("browser.ssrfPolicy.allowedHostnames = %v, want to contain 192.168.100.1", hosts)
	}
}

func TestBrowserCdpUrlNotSetWithoutIP(t *testing.T) {
	// When BrowserVMIP is empty (no paired browser VM), the browser block is not
	// emitted by the assembler — no cdpUrl and no browser section at all.
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-no-browser-vm",
		BrowserVMIP:  "",
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)
	// The contract is that the whole browser section is omitted when no
	// browser VM is paired — the previous assertion only caught an
	// erroneous cdpUrl and would silently pass if the assembler emitted
	// browser:{} or browser with other stale fields.
	if browser, ok := getNestedMap(m, "browser"); ok {
		t.Fatalf("browser section should not be present when BrowserVMIP is empty, got %v", browser)
	}
}

func TestAssembleConfig_WithPlugin(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Plugins: []PluginSelection{
			{
				PluginID: "memory-core",
				Slot:     "memory",
				ConfigTemplate: map[string]interface{}{
					"plugins": map[string]interface{}{
						"slots": map[string]interface{}{
							"memory": "memory-core",
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)
	plugins, ok := getNestedMap(m, "plugins")
	if !ok {
		t.Fatal("missing plugins section")
	}
	slots, ok := getNestedMap(plugins, "slots")
	if !ok {
		t.Fatal("missing plugins.slots")
	}
	if slots["memory"] != "memory-core" {
		t.Errorf("plugins.slots.memory = %v, want memory-core", slots["memory"])
	}
}

func TestAssembleConfig_NoPlugins_BackwardCompatible(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	// With no user-enabled plugins, plugins.entries should be empty.
	m := mustUnmarshalMap(t, data)
	plugins, ok := getNestedMap(m, "plugins")
	if !ok {
		// plugins section may be absent entirely — that's fine
		return
	}
	entries, ok := getNestedMap(plugins, "entries")
	if !ok {
		return
	}
	if len(entries) != 0 {
		t.Errorf("expected empty plugins.entries, got %d entries", len(entries))
	}
}

func TestAssembleConfig_PluginOverrides(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Plugins: []PluginSelection{
			{
				PluginID: "memory-core",
				Slot:     "memory",
				ConfigTemplate: map[string]interface{}{
					"plugins": map[string]interface{}{
						"slots": map[string]interface{}{
							"memory": "memory-core",
						},
					},
				},
				ConfigOverrides: map[string]interface{}{
					"plugins": map[string]interface{}{
						"memoryConfig": map[string]interface{}{
							"maxTokens": float64(4096),
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)
	plugins, ok := getNestedMap(m, "plugins")
	if !ok {
		t.Fatal("missing plugins section")
	}
	slots, ok := getNestedMap(plugins, "slots")
	if !ok {
		t.Fatal("missing plugins.slots")
	}
	if slots["memory"] != "memory-core" {
		t.Errorf("plugins.slots.memory = %v, want memory-core", slots["memory"])
	}
	mc, ok := getNestedMap(plugins, "memoryConfig")
	if !ok {
		t.Fatal("missing plugins.memoryConfig from overrides")
	}
	if mc["maxTokens"] != float64(4096) {
		t.Errorf("plugins.memoryConfig.maxTokens = %v, want 4096", mc["maxTokens"])
	}
}

func TestAssembleConfig_PluginProtectedPrefix(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "some-channel",
				EntryType: "channel",
				ConfigOverrides: map[string]interface{}{
					"plugins": map[string]interface{}{
						"slots": map[string]interface{}{
							"memory": "evil-plugin",
						},
					},
				},
			},
		},
		Plugins: []PluginSelection{
			{
				PluginID: "memory-core",
				Slot:     "memory",
				ConfigTemplate: map[string]interface{}{
					"plugins": map[string]interface{}{
						"slots": map[string]interface{}{
							"memory": "memory-core",
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)
	plugins, ok := getNestedMap(m, "plugins")
	if !ok {
		t.Fatal("missing plugins section")
	}
	slots, ok := getNestedMap(plugins, "slots")
	if !ok {
		t.Fatal("missing plugins.slots")
	}
	if slots["memory"] != "memory-core" {
		t.Errorf("plugins.slots.memory = %v, want memory-core (capability override should be stripped)", slots["memory"])
	}
}

// mustGetNestedMap wraps getNestedMap with t.Fatal on missing keys.
func mustGetNestedMap(t *testing.T, m map[string]interface{}, keys ...string) map[string]interface{} {
	t.Helper()
	result, ok := getNestedMap(m, keys...)
	if !ok {
		t.Fatalf("missing nested key path: %v", keys)
	}
	return result
}

func TestAssembleSeedConfig_BasicStructure(t *testing.T) {
	params := SeedParams{
		ModelCatalog: testModelCatalog(),
		DefaultModel: "anthropic/claude-sonnet-4-6",
		Providers:    []string{"anthropic"},
		ProxyBaseURL: "http://192.168.100.1:4000",
		MachineID:    "m-test",
	}
	data, err := AssembleSeedConfig(params)
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Check exec provider defined
	secrets := mustGetNestedMap(t, cfg, "secrets", "providers", "ocm")
	if secrets["source"] != "exec" {
		t.Errorf("secrets.providers.ocm.source = %v, want exec", secrets["source"])
	}
	if secrets["command"] != "/usr/local/bin/ocm-secrets" {
		t.Errorf("secrets.providers.ocm.command = %v, want /usr/local/bin/ocm-secrets", secrets["command"])
	}

	// Check gateway defaults
	gw := mustGetNestedMap(t, cfg, "gateway")
	if gw["mode"] != "local" {
		t.Errorf("gateway.mode = %v, want local", gw["mode"])
	}
	auth := mustGetNestedMap(t, cfg, "gateway", "auth")
	if auth["mode"] != "token" {
		t.Errorf("gateway.auth.mode = %v, want token", auth["mode"])
	}

	// Check provider with exec ref
	anthropic := mustGetNestedMap(t, cfg, "models", "providers", "anthropic")
	if anthropic["baseUrl"] != "http://192.168.100.1:4000/anthropic/v1" {
		t.Errorf("anthropic.baseUrl = %v", anthropic["baseUrl"])
	}
	apiKey := anthropic["apiKey"].(map[string]interface{})
	if apiKey["source"] != "exec" || apiKey["provider"] != "ocm" || apiKey["id"] != "anthropic-key" {
		t.Errorf("anthropic.apiKey = %v", apiKey)
	}
	// Gateway requires models to be an array (not undefined)
	if models, ok := anthropic["models"].([]interface{}); !ok {
		t.Errorf("anthropic.models should be an empty array, got %T: %v", anthropic["models"], anthropic["models"])
	} else if len(models) != 0 {
		t.Errorf("anthropic.models should be empty, got %v", models)
	}

	// Check default model
	agents := mustGetNestedMap(t, cfg, "agents", "defaults")
	model := agents["model"].(map[string]interface{})
	if model["primary"] != "anthropic/claude-sonnet-4-6" {
		t.Errorf("agents.defaults.model.primary = %v", model["primary"])
	}
}

func TestAssembleSeedConfig_NebiusAlwaysIncluded(t *testing.T) {
	params := SeedParams{
		ModelCatalog: testModelCatalog(),
		DefaultModel: "anthropic/claude-sonnet-4-6",
		Providers:    []string{"anthropic"}, // no nebius in user providers
		ProxyBaseURL: "http://192.168.100.1:4000",
		MachineID:    "m-test",
	}
	data, err := AssembleSeedConfig(params)
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal seed config: %v", err)
	}

	// Nebius must always be present for platform models
	nebius := mustGetNestedMap(t, cfg, "models", "providers", "nebius")
	if nebius["baseUrl"] != "http://192.168.100.1:4000/nebius/v1" {
		t.Errorf("nebius.baseUrl = %v", nebius["baseUrl"])
	}
}

func TestAssembleSeedConfig_PlatformModelMapping(t *testing.T) {
	tests := []struct {
		input  string
		mapped string
	}{
		{"qwen/qwen3.5-397b", "nebius/Qwen/Qwen3.5-397B-A17B"},
		{"minimax/minimax-m2.5", "nebius/MiniMaxAI/MiniMax-M2.5"},
		{"openai/gpt-oss-120b", "nebius/openai/gpt-oss-120b"},
		{"anthropic/claude-sonnet-4-6", "anthropic/claude-sonnet-4-6"}, // unmapped, passthrough
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			params := SeedParams{
				ModelCatalog: testModelCatalog(),
				DefaultModel: tt.input,
				Providers:    []string{},
				ProxyBaseURL: "http://192.168.100.1:4000",
				MachineID:    "m-test",
			}
			data, err := AssembleSeedConfig(params)
			if err != nil {
				t.Fatalf("AssembleSeedConfig: %v", err)
			}

			var cfg map[string]interface{}
			if err := json.Unmarshal(data, &cfg); err != nil {
				t.Fatalf("unmarshal seed config: %v", err)
			}

			agents := mustGetNestedMap(t, cfg, "agents", "defaults")
			model := agents["model"].(map[string]interface{})
			if model["primary"] != tt.mapped {
				t.Errorf("model %q not mapped correctly: got %v, want %v", tt.input, model["primary"], tt.mapped)
			}
		})
	}
}

func TestAssembleSeedConfig_ExcludesUnconfiguredProviders(t *testing.T) {
	params := SeedParams{
		ModelCatalog: testModelCatalog(),
		DefaultModel: "anthropic/claude-sonnet-4-6",
		Providers:    []string{"anthropic"}, // only anthropic configured
		ProxyBaseURL: "http://192.168.100.1:4000",
		MachineID:    "m-test",
	}
	data, err := AssembleSeedConfig(params)
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal seed config: %v", err)
	}

	providers := mustGetNestedMap(t, cfg, "models", "providers")
	// anthropic and nebius should be present
	if _, ok := providers["anthropic"]; !ok {
		t.Error("anthropic should be present")
	}
	if _, ok := providers["nebius"]; !ok {
		t.Error("nebius should always be present")
	}
	// openai, google, openrouter should NOT be present
	for _, p := range []string{"openai", "google", "openrouter"} {
		if _, ok := providers[p]; ok {
			t.Errorf("%s should NOT be present when not in Providers list", p)
		}
	}
}

func TestAssembleSeedConfig_IncludesOpenRouterWhenConfigured(t *testing.T) {
	params := SeedParams{
		ModelCatalog: testModelCatalog(),
		DefaultModel: "openrouter/meta-llama/llama-3.1-8b-instruct",
		Providers:    []string{"openrouter"},
		ProxyBaseURL: "http://192.168.100.1:4000",
		MachineID:    "m-test",
	}
	data, err := AssembleSeedConfig(params)
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal seed config: %v", err)
	}

	// openrouter should be in models.providers with exec secret ref
	providers := mustGetNestedMap(t, cfg, "models", "providers")
	orProvider, ok := providers["openrouter"].(map[string]interface{})
	if !ok {
		t.Fatal("openrouter should be present in models.providers when in Providers list")
	}

	// Verify apiKey uses exec provider (ocm-secrets)
	apiKey, ok := orProvider["apiKey"].(map[string]interface{})
	if !ok {
		t.Fatal("openrouter.apiKey should be an exec provider object")
	}
	if apiKey["source"] != "exec" {
		t.Errorf("openrouter.apiKey.source = %v, want exec", apiKey["source"])
	}
	if apiKey["id"] != "openrouter-key" {
		t.Errorf("openrouter.apiKey.id = %v, want openrouter-key", apiKey["id"])
	}

	// Verify baseUrl points to the proxy (seed config = native mode, appends /v1)
	// The proxy handles the /api prefix rewrite via UpstreamPathPrefix.
	if baseURL, ok := orProvider["baseUrl"].(string); !ok || baseURL != "http://192.168.100.1:4000/openrouter/v1" {
		t.Errorf("openrouter.baseUrl = %v, want http://192.168.100.1:4000/openrouter/v1", orProvider["baseUrl"])
	}

	// Verify OpenRouter models appear in agents.defaults.models
	modelsCatalog := mustGetNestedMap(t, cfg, "agents", "defaults", "models")
	if _, ok := modelsCatalog["openrouter/meta-llama/llama-3.1-8b-instruct"]; !ok {
		t.Error("openrouter/meta-llama/llama-3.1-8b-instruct should be in agents.defaults.models")
	}

	// Verify per-provider models array is populated (gateway needs this for
	// non-built-in providers like openrouter — empty array = no models available)
	orModels, ok := orProvider["models"].([]interface{})
	if !ok {
		t.Fatal("openrouter.models should be an array")
	}
	if len(orModels) == 0 {
		t.Fatal("openrouter.models should NOT be empty — gateway needs explicit model entries for non-built-in providers")
	}
	// Verify model entries have the expected structure
	firstModel := orModels[0].(map[string]interface{})
	if firstModel["api"] != "openai-completions" {
		t.Errorf("openrouter model api = %v, want openai-completions", firstModel["api"])
	}
	if firstModel["id"] == nil || firstModel["id"] == "" {
		t.Error("openrouter model id should not be empty")
	}

	// nebius should still always be present
	if _, ok := providers["nebius"]; !ok {
		t.Error("nebius should always be present")
	}

	// anthropic should NOT be present (not in Providers list)
	if _, ok := providers["anthropic"]; ok {
		t.Error("anthropic should NOT be present when not in Providers list")
	}
}

func TestAvailableCatalogIDs_OpenRouterByokFiltering(t *testing.T) {
	catalog := testModelCatalog()

	// Without openrouter credentials: OpenRouter models should NOT be available
	withoutOR := AvailableCatalogIDs(catalog, map[string]bool{"anthropic": true})
	if withoutOR["openrouter/meta-llama/llama-3.1-8b-instruct"] {
		t.Error("OpenRouter model should not be available without openrouter credential")
	}

	// With openrouter credentials: OpenRouter models should be available
	withOR := AvailableCatalogIDs(catalog, map[string]bool{"anthropic": true, "openrouter": true})
	if !withOR["openrouter/meta-llama/llama-3.1-8b-instruct"] {
		t.Error("OpenRouter model should be available with openrouter credential")
	}
	if !withOR["openrouter/openai/gpt-4o"] {
		t.Error("OpenRouter gpt-4o model should be available with openrouter credential")
	}

	// Platform models should always be available regardless
	if !withoutOR["qwen/qwen3.5-397b"] {
		t.Error("platform model should always be available")
	}
}

func TestAssembleSeedConfig_GoogleOpenAICompatible(t *testing.T) {
	params := SeedParams{
		ModelCatalog: testModelCatalog(),
		DefaultModel: "google/gemini-2.5-flash",
		Providers:    []string{"google"},
		ProxyBaseURL: "http://192.168.100.1:4000",
		MachineID:    "m-test-google",
	}
	data, err := AssembleSeedConfig(params)
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal seed config: %v", err)
	}

	providers := mustGetNestedMap(t, cfg, "models", "providers")
	googleProvider, ok := providers["google"].(map[string]interface{})
	if !ok {
		t.Fatal("google should be present in models.providers when in Providers list")
	}

	// Google's OpenAI-compatible endpoint is at /v1beta/openai/ — no /v1 prefix.
	// The proxy prepends /v1beta/openai via UpstreamPathPrefix, so the baseUrl
	// should NOT include /v1.
	if baseURL, ok := googleProvider["baseUrl"].(string); !ok || baseURL != "http://192.168.100.1:4000/google" {
		t.Errorf("google.baseUrl = %v, want http://192.168.100.1:4000/google (no /v1 — proxy prepends /v1beta/openai)", googleProvider["baseUrl"])
	}

	// Should use openai-completions api (not google-generative-ai) so requests
	// go through the proxy with x-api-key header auth.
	if api := googleProvider["api"]; api != "openai-completions" {
		t.Errorf("google.api = %v, want openai-completions", api)
	}
}

func TestAssembleSeedConfig_PluginsEmpty(t *testing.T) {
	params := SeedParams{
		ModelCatalog: testModelCatalog(),
		DefaultModel: "anthropic/claude-sonnet-4-6",
		ProxyBaseURL: "http://192.168.100.1:4000",
		MachineID:    "m-test",
	}
	data, err := AssembleSeedConfig(params)
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal seed config: %v", err)
	}

	// Plugins entries should be empty (opik not bundled in rootfs yet)
	entries := mustGetNestedMap(t, cfg, "plugins", "entries")
	if len(entries) != 0 {
		t.Errorf("plugins.entries should be empty, got %v", entries)
	}
}

// TestAssembleSeedConfig_GatewayModelResolution simulates the gateway's model
// resolution logic to verify that configured models can actually be found.
//
// The gateway splits model references on the FIRST "/" into provider + model,
// then looks up: entry.provider == provider && entry.id == model.
// For example, "nebius/deepseek-ai/DeepSeek-V3-0324" splits into:
//
//	provider="nebius", model="deepseek-ai/DeepSeek-V3-0324"
//
// This test catches model ID format bugs that only manifest at runtime in the
// gateway — the proxy tests bypass the gateway entirely and don't catch these.
func TestAssembleSeedConfig_GatewayModelResolution(t *testing.T) {
	for _, userModel := range []string{
		"qwen/qwen3.5-397b",
		"minimax/minimax-m2.5",
		"openai/gpt-oss-120b",
	} {
		t.Run(userModel, func(t *testing.T) {
			data, err := AssembleSeedConfig(SeedParams{
				ModelCatalog: testModelCatalog(),
				DefaultModel: userModel,
				Providers:    []string{},
				ProxyBaseURL: "http://192.168.100.1:4000",
				MachineID:    "m-test",
			})
			if err != nil {
				t.Fatalf("AssembleSeedConfig: %v", err)
			}

			var cfg map[string]interface{}
			if err := json.Unmarshal(data, &cfg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			// 1. Get the model primary (what the gateway will resolve)
			primary := mustGetNestedMap(t, cfg, "agents", "defaults", "model")["primary"].(string)

			// 2. Simulate gateway's parseModelRef: split on first "/"
			slashIdx := -1
			for i, c := range primary {
				if c == '/' {
					slashIdx = i
					break
				}
			}
			if slashIdx <= 0 {
				t.Fatalf("model primary %q has no provider prefix (no '/')", primary)
			}
			refProvider := primary[:slashIdx]
			refModel := primary[slashIdx+1:]

			// 3. Build a catalog from models.providers (simulates models.json)
			type catalogEntry struct {
				provider string
				id       string
			}
			var catalog []catalogEntry
			providers := mustGetNestedMap(t, cfg, "models", "providers")
			for provName, v := range providers {
				prov := v.(map[string]interface{})
				models, ok := prov["models"].([]interface{})
				if !ok {
					continue
				}
				for _, m := range models {
					entry := m.(map[string]interface{})
					catalog = append(catalog, catalogEntry{
						provider: provName,
						id:       entry["id"].(string),
					})
				}
			}

			// 4. Simulate gateway's catalog lookup:
			//    entry.provider === refProvider && entry.id === refModel
			found := false
			for _, entry := range catalog {
				if entry.provider == refProvider && entry.id == refModel {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("gateway would reject model: primary=%q parsed as provider=%q model=%q\n"+
					"  No catalog entry matches (provider=%q AND id=%q)\n"+
					"  Catalog entries: %v",
					primary, refProvider, refModel, refProvider, refModel, catalog)
			}

			// 5. Verify model is also in agents.defaults.models catalog
			modelsCatalog := mustGetNestedMap(t, cfg, "agents", "defaults", "models")
			if _, ok := modelsCatalog[primary]; !ok {
				t.Errorf("model %q not in agents.defaults.models", primary)
			}

			// 6. Verify provider baseUrl includes /v1 suffix.
			// piSdk appends only /chat/completions to baseUrl, so the version
			// prefix must be part of baseUrl (matching OpenAI SDK convention).
			provConf := providers[refProvider].(map[string]interface{})
			baseURL, _ := provConf["baseUrl"].(string)
			if !strings.HasSuffix(baseURL, "/v1") {
				t.Errorf("provider %q baseUrl=%q must end with /v1 (piSdk appends /chat/completions)", refProvider, baseURL)
			}
		})
	}
}

func TestChannelToken_TelegramWithCredential(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "telegram",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"channels": map[string]interface{}{
						"telegram": map[string]interface{}{
							"enabled": true,
						},
					},
				},
			},
		},
		ChannelCredentialValues: map[string]string{
			"telegram": "123456:ABC-DEF",
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	tg, ok := getNestedMap(m, "channels", "telegram")
	if !ok {
		t.Fatal("missing channels.telegram")
	}
	if tg["enabled"] != true {
		t.Errorf("channels.telegram.enabled = %v, want true", tg["enabled"])
	}
	// Assembler injects channel credentials as plaintext tokens.
	if tg["botToken"] != "123456:ABC-DEF" {
		t.Errorf("channels.telegram.botToken = %v, want 123456:ABC-DEF", tg["botToken"])
	}
}

func TestChannelToken_DiscordWithCredential(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "discord",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"channels": map[string]interface{}{
						"discord": map[string]interface{}{
							"enabled": true,
						},
					},
				},
			},
		},
		ChannelCredentialValues: map[string]string{
			"discord": "discord-bot-token-xyz",
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	dc, ok := getNestedMap(m, "channels", "discord")
	if !ok {
		t.Fatal("missing channels.discord")
	}
	// Assembler injects channel credentials as plaintext tokens.
	if dc["token"] != "discord-bot-token-xyz" {
		t.Errorf("channels.discord.token = %v, want discord-bot-token-xyz", dc["token"])
	}
}

func TestChannelToken_NoCredentialNoToken(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "telegram",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"channels": map[string]interface{}{
						"telegram": map[string]interface{}{
							"enabled": true,
						},
					},
				},
			},
		},
		// No ChannelCredentialValues
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	tg, ok := getNestedMap(m, "channels", "telegram")
	if !ok {
		t.Fatal("missing channels.telegram")
	}
	if _, hasBotToken := tg["botToken"]; hasBotToken {
		t.Error("channels.telegram.botToken should not be set when no credential exists")
	}
}

func TestBrowserCdpUrlEmittedFromPairingAlone(t *testing.T) {
	// When BrowserVMIP is set, the browser block is emitted unconditionally —
	// no browser capability needs to be enabled on the machine.
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-paired",
		Capabilities: nil,
		BrowserVMIP:  "192.168.100.110",
		BridgeIP:     "192.168.100.1",
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)
	browser, ok := getNestedMap(m, "browser")
	if !ok {
		t.Fatal("missing browser section — expected it when BrowserVMIP is set")
	}
	if browser["cdpUrl"] != "http://192.168.100.1:9222" {
		t.Errorf("browser.cdpUrl = %v, want http://192.168.100.1:9222", browser["cdpUrl"])
	}
	if browser["enabled"] != true {
		t.Errorf("browser.enabled = %v, want true", browser["enabled"])
	}
	ssrf, ok := getNestedMap(browser, "ssrfPolicy")
	if !ok {
		t.Fatal("missing browser.ssrfPolicy — 4.14+ fail-closed default blocks private cdpUrl without allowlist")
	}
	hosts, _ := ssrf["allowedHostnames"].([]interface{})
	found := false
	for _, h := range hosts {
		if h == "192.168.100.1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("browser.ssrfPolicy.allowedHostnames = %v, want to contain 192.168.100.1", hosts)
	}
}

func TestAssembleSeedConfig_CodexDirectBaseUrlAndAuthProfiles(t *testing.T) {
	params := SeedParams{
		ModelCatalog: testModelCatalog(),
		DefaultModel: "anthropic/claude-sonnet-4-6",
		Providers:    []string{"anthropic", "openai-codex"},
		ProxyBaseURL: "http://192.168.100.1:4000",
		MachineID:    "m-test",
	}
	data, err := AssembleSeedConfig(params)
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	cfg := mustUnmarshalMap(t, data)

	// Codex provider should point directly at chatgpt.com
	codex := mustGetNestedMap(t, cfg, "models", "providers", "openai-codex")
	if codex["baseUrl"] != "https://chatgpt.com/backend-api" {
		t.Errorf("openai-codex baseUrl = %v, want https://chatgpt.com/backend-api", codex["baseUrl"])
	}
	if codex["api"] != "openai-codex-responses" {
		t.Errorf("openai-codex api = %v, want openai-codex-responses", codex["api"])
	}
	if _, hasAPIKey := codex["apiKey"]; hasAPIKey {
		t.Error("openai-codex should NOT have apiKey (auth comes from auth-profiles.json)")
	}

	// auth.profiles should have codex entry
	authProfile := mustGetNestedMap(t, cfg, "auth", "profiles", "openai-codex:default")
	if authProfile["provider"] != "openai-codex" {
		t.Errorf("auth.profiles provider = %v, want openai-codex", authProfile["provider"])
	}
	if authProfile["mode"] != "oauth" {
		t.Errorf("auth.profiles mode = %v, want oauth", authProfile["mode"])
	}

	// Other providers should still have proxy-based config
	anthropic := mustGetNestedMap(t, cfg, "models", "providers", "anthropic")
	if anthropic["baseUrl"] != "http://192.168.100.1:4000/anthropic/v1" {
		t.Errorf("anthropic baseUrl = %v, want proxy URL", anthropic["baseUrl"])
	}
}

func TestAssembleSeedConfig_ChannelConfigs(t *testing.T) {
	params := SeedParams{
		ModelCatalog: testModelCatalog(),
		DefaultModel: "anthropic/claude-sonnet-4-6",
		Providers:    []string{"anthropic"},
		ProxyBaseURL: "http://192.168.100.1:4000",
		MachineID:    "m-test",
		ChannelConfigs: map[string]map[string]interface{}{
			"telegram": {
				"enabled":  true,
				"dmPolicy": "open",
				"botToken": map[string]interface{}{
					"source":   "exec",
					"provider": "ocm",
					"id":       "channel-telegram-botToken",
				},
			},
		},
	}

	data, err := AssembleSeedConfig(params)
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	telegram := mustGetNestedMap(t, cfg, "channels", "telegram")
	if telegram["enabled"] != true {
		t.Error("channels.telegram.enabled should be true")
	}
	if telegram["dmPolicy"] != "open" {
		t.Errorf("channels.telegram.dmPolicy = %v, want open", telegram["dmPolicy"])
	}
	// Token should be an exec secret ref, not plaintext
	tokenRef := mustGetNestedMap(t, cfg, "channels", "telegram", "botToken")
	if tokenRef["source"] != "exec" {
		t.Errorf("channels.telegram.botToken.source = %v, want exec", tokenRef["source"])
	}
	if tokenRef["id"] != "channel-telegram-botToken" {
		t.Errorf("channels.telegram.botToken.id = %v, want channel-telegram-botToken", tokenRef["id"])
	}
}

func TestAssembleSeedConfig_NoChannels(t *testing.T) {
	params := SeedParams{
		ModelCatalog: testModelCatalog(),
		DefaultModel: "anthropic/claude-sonnet-4-6",
		Providers:    []string{"anthropic"},
		ProxyBaseURL: "http://192.168.100.1:4000",
		MachineID:    "m-test",
	}

	data, err := AssembleSeedConfig(params)
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, exists := cfg["channels"]; exists {
		t.Error("seed config should not have channels key when no channels configured")
	}
}

func TestAssembleSeedConfig_OpikPlugin(t *testing.T) {
	opikConfigTemplate := map[string]interface{}{
		"plugins": map[string]interface{}{
			"allow": []interface{}{"opik-openclaw"},
			"entries": map[string]interface{}{
				"opik-openclaw": map[string]interface{}{
					"enabled": true,
					"config": map[string]interface{}{
						"projectName":   "default",
						"workspaceName": "default",
						"tags":          []interface{}{"ocm"},
					},
				},
			},
		},
	}

	data, err := AssembleSeedConfig(SeedParams{
		MachineID:    "m-seed-opik",
		DefaultModel: "anthropic/claude-sonnet-4-6",
		ModelCatalog: testModelCatalog(),
		Plugins: []PluginSelection{
			{
				PluginID:       "opik-openclaw",
				Slot:           "observability",
				ConfigTemplate: opikConfigTemplate,
			},
		},
		OpikAPIURL: "https://api.example.com/api/opik",
		OpikAPIKey: "test-gw-token",
	})
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// plugins.allow should include opik-openclaw
	plugins := mustGetNestedMap(t, m, "plugins")
	allowRaw, ok := plugins["allow"]
	if !ok {
		t.Fatal("missing plugins.allow in seed config")
	}
	allowSlice := allowRaw.([]interface{})
	found := false
	for _, v := range allowSlice {
		if v == "opik-openclaw" {
			found = true
		}
	}
	if !found {
		t.Errorf("plugins.allow = %v, want opik-openclaw", allowSlice)
	}

	// plugins.entries.opik-openclaw should have apiUrl and apiKey
	opikEntry := mustGetNestedMap(t, m, "plugins", "entries", "opik-openclaw")
	config := mustGetNestedMap(t, opikEntry, "config")
	if config["apiUrl"] != "https://api.example.com/api/opik" {
		t.Errorf("apiUrl = %v, want https://api.example.com/api/opik", config["apiUrl"])
	}
	if config["apiKey"] != "test-gw-token" {
		t.Errorf("apiKey = %v, want test-gw-token", config["apiKey"])
	}
	hooks := mustGetNestedMap(t, opikEntry, "hooks")
	if hooks["allowConversationAccess"] != true {
		t.Errorf("hooks.allowConversationAccess = %v, want true", hooks["allowConversationAccess"])
	}

	// Artifact mode: no plugins.installs — plugins are bundled in the artifact.
	if _, hasInstalls := plugins["installs"]; hasInstalls {
		t.Error("plugins.installs should not be present — artifact bundles plugins")
	}
}

func TestAssembleConfig_OpikPluginFullPipeline(t *testing.T) {
	// Simulate the config template from migration 050 (fixed version without slots)
	opikConfigTemplate := map[string]interface{}{
		"plugins": map[string]interface{}{
			"allow": []interface{}{"opik-openclaw"},
			"entries": map[string]interface{}{
				"opik-openclaw": map[string]interface{}{
					"enabled": true,
					"config": map[string]interface{}{
						"projectName":   "default",
						"workspaceName": "default",
						"tags":          []interface{}{"ocm"},
					},
				},
			},
		},
	}

	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-opik-test",
		OpikAPIURL:   "http://192.168.100.1:9090/api/opik",
		OpikAPIKey:   "test-gateway-token",
		Plugins: []PluginSelection{
			{
				PluginID:       "opik-openclaw",
				Slot:           "observability",
				ConfigTemplate: opikConfigTemplate,
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// 1. plugins.allow should include opik-openclaw
	plugins := mustGetNestedMap(t, m, "plugins")
	allowRaw, ok := plugins["allow"]
	if !ok {
		t.Fatal("missing plugins.allow")
	}
	allowSlice, ok := allowRaw.([]interface{})
	if !ok {
		t.Fatalf("plugins.allow is %T, want []interface{}", allowRaw)
	}
	found := false
	for _, v := range allowSlice {
		if v == "opik-openclaw" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("plugins.allow = %v, should contain opik-openclaw", allowSlice)
	}

	// 2. plugins.entries.opik-openclaw should have apiUrl and apiKey exec ref
	opikEntry := mustGetNestedMap(t, m, "plugins", "entries", "opik-openclaw")
	if opikEntry["enabled"] != true {
		t.Errorf("opik-openclaw.enabled = %v, want true", opikEntry["enabled"])
	}

	config := mustGetNestedMap(t, m, "plugins", "entries", "opik-openclaw", "config")
	if config["enabled"] != true {
		t.Errorf("opik-openclaw.config.enabled = %v, want true", config["enabled"])
	}
	if config["apiUrl"] != "http://192.168.100.1:9090/api/opik" {
		t.Errorf("config.apiUrl = %v, want http://192.168.100.1:9090/api/opik", config["apiUrl"])
	}
	if config["projectName"] != "default" {
		t.Errorf("config.projectName = %v, want default", config["projectName"])
	}

	// apiKey should be a plain string (gateway token)
	apiKeyRaw, ok := config["apiKey"]
	if !ok {
		t.Fatal("missing config.apiKey")
	}
	apiKey, ok := apiKeyRaw.(string)
	if !ok {
		t.Fatalf("config.apiKey is %T, want string", apiKeyRaw)
	}
	if apiKey != "test-gateway-token" {
		t.Errorf("apiKey = %v, want test-gateway-token", apiKey)
	}
	hooks := mustGetNestedMap(t, opikEntry, "hooks")
	if hooks["allowConversationAccess"] != true {
		t.Errorf("hooks.allowConversationAccess = %v, want true", hooks["allowConversationAccess"])
	}

	// 3. Artifact mode: no plugins.installs — plugins are bundled in the artifact.
	if _, hasInstalls := plugins["installs"]; hasInstalls {
		t.Error("plugins.installs should not be present — artifact bundles plugins")
	}

	// 4. No invalid slots key (the bug from migration 049)
	if _, hasSlots := plugins["slots"]; hasSlots {
		slotsMap, _ := plugins["slots"].(map[string]interface{})
		if _, hasObs := slotsMap["observability"]; hasObs {
			t.Error("plugins.slots.observability should NOT be present — observability is not a valid gateway slot")
		}
	}
}

func TestAssembleConfig_OpikNotInjectedWithoutURL(t *testing.T) {
	// When OpikAPIURL is empty, no opik-specific config should be injected
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-no-opik",
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// plugins.installs should not exist or not contain opik-openclaw
	if plugins, ok := getNestedMap(m, "plugins"); ok {
		if installs, ok := getNestedMap(plugins, "installs"); ok {
			if _, hasOpik := installs["opik-openclaw"]; hasOpik {
				t.Error("plugins.installs.opik-openclaw should NOT be present when OpikAPIURL is empty")
			}
		}
	}
}

func TestAssembleConfig_ComposioInjection(t *testing.T) {
	params := AssemblyParams{
		MachineID: "machine-uuid-123",
		Plugins: []PluginSelection{
			{
				PluginID: "composio",
				Slot:     "integrations",
				ConfigTemplate: map[string]interface{}{
					"plugins": map[string]interface{}{
						"allow": []interface{}{"composio"},
						"entries": map[string]interface{}{
							"composio": map[string]interface{}{
								"enabled": true,
								"config":  map[string]interface{}{},
							},
						},
					},
				},
			},
		},
		ComposioAPIURL: "https://api.example.com/api/composio",
		ComposioProxyTokenSigner: func(machineID string) (string, error) {
			return "test-token-for-" + machineID, nil
		},
	}

	data, err := AssembleConfig(params)
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	plugins, ok := result["plugins"].(map[string]interface{})
	if !ok {
		t.Fatal("missing plugins key")
	}

	entries, ok := plugins["entries"].(map[string]interface{})
	if !ok {
		t.Fatal("missing plugins.entries")
	}

	composioEntry, ok := entries["composio"].(map[string]interface{})
	if !ok {
		t.Fatal("missing plugins.entries.composio")
	}

	config, ok := composioEntry["config"].(map[string]interface{})
	if !ok {
		t.Fatal("missing plugins.entries.composio.config")
	}

	if config["apiUrl"] != "https://api.example.com/api/composio" {
		t.Errorf("apiUrl = %v, want https://api.example.com/api/composio", config["apiUrl"])
	}

	if config["userId"] != "machine-uuid-123" {
		t.Errorf("userId = %v, want machine-uuid-123", config["userId"])
	}

	if config["machineToken"] != "test-token-for-machine-uuid-123" {
		t.Errorf("machineToken = %v, want test-token-for-machine-uuid-123", config["machineToken"])
	}

	if _, ok := config["consumerKey"]; ok {
		t.Error("consumerKey should not be in config")
	}
	if _, ok := config["mcpUrl"]; ok {
		t.Error("mcpUrl should not be in config")
	}

	// Verify plugins.installs is absent (plugins are bundled in artifact, not installed from path)
	if _, ok := plugins["installs"]; ok {
		t.Error("plugins.installs should not be present — plugins are bundled in artifact")
	}

	// Verify plugins.allow includes composio
	allow, ok := plugins["allow"].([]interface{})
	if !ok {
		t.Fatal("missing plugins.allow")
	}
	found := false
	for _, a := range allow {
		if a == "composio" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("plugins.allow = %v, want composio in list", allow)
	}
}

func TestAssembleConfig_ComposioInjection_NoSigner(t *testing.T) {
	// Without a signer, the assembled config still includes apiUrl + userId
	// (back-compat for dev/tests) but omits machineToken. Production must
	// always set the signer.
	params := AssemblyParams{
		MachineID: "machine-uuid-123",
		Plugins: []PluginSelection{
			{
				PluginID: "composio",
				Slot:     "integrations",
				ConfigTemplate: map[string]interface{}{
					"plugins": map[string]interface{}{
						"allow": []interface{}{"composio"},
						"entries": map[string]interface{}{
							"composio": map[string]interface{}{
								"enabled": true,
								"config":  map[string]interface{}{},
							},
						},
					},
				},
			},
		},
		ComposioAPIURL: "https://api.example.com/api/composio",
		// ComposioProxyTokenSigner intentionally nil.
	}

	data, err := AssembleConfig(params)
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	plugins := result["plugins"].(map[string]interface{})
	entries := plugins["entries"].(map[string]interface{})
	composioEntry := entries["composio"].(map[string]interface{})
	config := composioEntry["config"].(map[string]interface{})

	if _, has := config["machineToken"]; has {
		t.Errorf("machineToken should be absent when signer is nil, got %v", config["machineToken"])
	}
	if config["userId"] != "machine-uuid-123" {
		t.Errorf("userId still expected even without signer; got %v", config["userId"])
	}
}

func TestAssembleConfig_ComposioInjection_SignerError(t *testing.T) {
	// A signer that fails must propagate the error up — config assembly must
	// not silently fall back to emitting a config without the token.
	params := AssemblyParams{
		MachineID: "machine-uuid-123",
		Plugins: []PluginSelection{
			{
				PluginID: "composio",
				Slot:     "integrations",
				ConfigTemplate: map[string]interface{}{
					"plugins": map[string]interface{}{
						"allow": []interface{}{"composio"},
						"entries": map[string]interface{}{
							"composio": map[string]interface{}{
								"enabled": true,
								"config":  map[string]interface{}{},
							},
						},
					},
				},
			},
		},
		ComposioAPIURL: "https://api.example.com/api/composio",
		ComposioProxyTokenSigner: func(machineID string) (string, error) {
			return "", fmt.Errorf("simulated signer failure for %s", machineID)
		},
	}

	if _, err := AssembleConfig(params); err == nil {
		t.Fatal("expected error from failing signer, got nil")
	}
}

func TestAssembleConfig_PluginAllowListMerge(t *testing.T) {
	// When both opik and composio plugins are enabled, plugins.allow should contain both.
	params := AssemblyParams{
		MachineID: "machine-uuid-123",
		Plugins: []PluginSelection{
			{
				PluginID: "opik-openclaw",
				Slot:     "observability",
				ConfigTemplate: map[string]interface{}{
					"plugins": map[string]interface{}{
						"allow": []interface{}{"opik-openclaw"},
						"entries": map[string]interface{}{
							"opik-openclaw": map[string]interface{}{
								"enabled": true,
								"config":  map[string]interface{}{},
							},
						},
					},
				},
			},
			{
				PluginID: "composio",
				Slot:     "integrations",
				ConfigTemplate: map[string]interface{}{
					"plugins": map[string]interface{}{
						"allow": []interface{}{"composio"},
						"entries": map[string]interface{}{
							"composio": map[string]interface{}{
								"enabled": true,
								"config":  map[string]interface{}{},
							},
						},
					},
				},
			},
		},
		OpikAPIURL:     "https://opik.example.com",
		OpikAPIKey:     "test-opik-key",
		ComposioAPIURL: "https://api.example.com/api/composio",
	}

	data, err := AssembleConfig(params)
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	plugins, ok := result["plugins"].(map[string]interface{})
	if !ok {
		t.Fatal("missing plugins key")
	}

	allow, ok := plugins["allow"].([]interface{})
	if !ok {
		t.Fatal("missing plugins.allow")
	}

	allowSet := map[string]bool{}
	for _, a := range allow {
		if s, ok := a.(string); ok {
			allowSet[s] = true
		}
	}

	if !allowSet["opik-openclaw"] {
		t.Errorf("plugins.allow %v missing opik-openclaw", allow)
	}
	if !allowSet["composio"] {
		t.Errorf("plugins.allow %v missing composio", allow)
	}
}

func TestCodexAPIForVersion(t *testing.T) {
	tests := []struct {
		version string
		want    string
	}{
		{"v2026.5.28-r4", "openai-codex-responses"},
		{"2026.5.28", "openai-codex-responses"},
		{"2026.4.2", "openai-codex-responses"},
		{"v2026.6.5-r1", "openai-chatgpt-responses"},
		{"2026.6.5", "openai-chatgpt-responses"},
		{"2026.6.5-beta.2", "openai-chatgpt-responses"},
		{"2026.12.1", "openai-chatgpt-responses"},
		{"2027.1.0", "openai-chatgpt-responses"},
		// Unknown → legacy shape (matches the currently deployed fleet)
		{"", "openai-codex-responses"},
		{"unknown", "openai-codex-responses"},
	}
	for _, tt := range tests {
		if got := codexAPIForVersion(tt.version); got != tt.want {
			t.Errorf("codexAPIForVersion(%q) = %q, want %q", tt.version, got, tt.want)
		}
	}
}

func TestAssembleConfig_CodexProviderVersionKeyed(t *testing.T) {
	assemble := func(version string) map[string]interface{} {
		t.Helper()
		data, err := AssembleConfig(AssemblyParams{
			MachineID:               "m-test",
			ModelCatalog:            testModelCatalog(),
			Credentials:             map[string]string{"openai-codex": "present"},
			ProxyBaseURL:            "http://192.168.100.1:4000",
			ResolvedOpenclawVersion: version,
		})
		if err != nil {
			t.Fatalf("AssembleConfig: %v", err)
		}
		var cfg map[string]interface{}
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatalf("parse config: %v", err)
		}
		models, _ := cfg["models"].(map[string]interface{})
		providers, _ := models["providers"].(map[string]interface{})
		codex, _ := providers["openai-codex"].(map[string]interface{})
		if codex == nil {
			t.Fatalf("openai-codex provider missing from config: %s", data)
		}
		return codex
	}

	legacy := assemble("v2026.5.28-r4")
	if api, _ := legacy["api"].(string); api != "openai-codex-responses" {
		t.Errorf("5.28 codex api = %q, want openai-codex-responses", api)
	}

	current := assemble("v2026.6.5-r1")
	if api, _ := current["api"].(string); api != "openai-chatgpt-responses" {
		t.Errorf("6.5 codex api = %q, want openai-chatgpt-responses", api)
	}
	// Model entries carry the same version-keyed api value.
	if entries, _ := current["models"].([]interface{}); len(entries) > 0 {
		first, _ := entries[0].(map[string]interface{})
		if api, _ := first["api"].(string); api != "openai-chatgpt-responses" {
			t.Errorf("6.5 codex model api = %q, want openai-chatgpt-responses", api)
		}
	}
}

func TestChannelTokenFields_TelegramMapping(t *testing.T) {
	fields, ok := ChannelTokenFields["telegram"]
	if !ok {
		t.Fatal("missing telegram in ChannelTokenFields")
	}
	if len(fields) != 1 {
		t.Fatalf("expected 1 field for telegram, got %d", len(fields))
	}
	if fields[0].FieldName != "botToken" || fields[0].Provider != "telegram" {
		t.Errorf("telegram field = %+v, want {botToken, telegram}", fields[0])
	}
}

func TestChannelTokenFields_DiscordMapping(t *testing.T) {
	fields, ok := ChannelTokenFields["discord"]
	if !ok {
		t.Fatal("missing discord in ChannelTokenFields")
	}
	if len(fields) != 1 {
		t.Fatalf("expected 1 field for discord, got %d", len(fields))
	}
	if fields[0].FieldName != "token" || fields[0].Provider != "discord" {
		t.Errorf("discord field = %+v, want {token, discord}", fields[0])
	}
}

func TestChannelTokenFields_SlackMapping(t *testing.T) {
	fields, ok := ChannelTokenFields["slack"]
	if !ok {
		t.Fatal("missing slack in ChannelTokenFields")
	}
	if len(fields) != 2 {
		t.Fatalf("expected 2 fields for slack, got %d", len(fields))
	}
	if fields[0].FieldName != "botToken" || fields[0].Provider != "slack" {
		t.Errorf("slack[0] = %+v, want {botToken, slack}", fields[0])
	}
	if fields[1].FieldName != "appToken" || fields[1].Provider != "slack-app" {
		t.Errorf("slack[1] = %+v, want {appToken, slack-app}", fields[1])
	}
}

func TestProviderToChannel_Telegram(t *testing.T) {
	chID, fieldName, ok := ProviderToChannel("telegram")
	if !ok || chID != "telegram" || fieldName != "botToken" {
		t.Errorf("ProviderToChannel(telegram) = (%q, %q, %v), want (telegram, botToken, true)", chID, fieldName, ok)
	}
}

func TestProviderToChannel_SlackApp(t *testing.T) {
	chID, fieldName, ok := ProviderToChannel("slack-app")
	if !ok || chID != "slack" || fieldName != "appToken" {
		t.Errorf("ProviderToChannel(slack-app) = (%q, %q, %v), want (slack, appToken, true)", chID, fieldName, ok)
	}
}

func TestProviderToChannel_Unknown(t *testing.T) {
	_, _, ok := ProviderToChannel("unknown-provider")
	if ok {
		t.Error("expected ok=false for unknown provider")
	}
}

// --- Search provider resolution and catalog-driven config assembly tests ---

func testSearchCatalog() []CatalogProvider {
	order10 := 10
	order20 := 20
	order60 := 60
	order65 := 65
	order70 := 70
	return []CatalogProvider{
		{ID: "brave", Category: "search", AutodetectOrder: &order10, ExecSecretID: "search-brave-api-key"},
		{ID: "exa", Category: "search", AutodetectOrder: &order65, ExecSecretID: "search-exa-api-key"},
		{ID: "gemini-search", Category: "search", RuntimeProviderID: "gemini", PluginID: "google", ExecSecretID: "search-gemini-api-key", AutodetectOrder: &order20},
		{ID: "firecrawl", Category: "search", AutodetectOrder: &order60, ExecSecretID: "search-firecrawl-api-key"},
		{ID: "tavily", Category: "search", AutodetectOrder: &order70, ExecSecretID: "search-tavily-api-key"},
		{ID: "anthropic", Category: "ai"},
	}
}

func testFullProviderCatalog() []CatalogProvider {
	order10 := 10
	order20 := 20
	order60 := 60
	order70 := 70
	return []CatalogProvider{
		{ID: "anthropic", Category: "ai", ExecSecretID: "anthropic-key", EnvVar: "ANTHROPIC_API_KEY"},
		{ID: "openai", Category: "ai", ExecSecretID: "openai-key", EnvVar: "OPENAI_API_KEY"},
		{ID: "google", Category: "ai", EnvVar: "GOOGLE_API_KEY"},
		{ID: "nebius", Category: "ai", ExecSecretID: "nebius-key", EnvVar: "NEBIUS_API_KEY"},
		{ID: "brave", Category: "search", AutodetectOrder: &order10, ExecSecretID: "search-brave-api-key", EnvVar: "BRAVE_API_KEY"},
		{ID: "gemini-search", Category: "search", RuntimeProviderID: "gemini", ExecSecretID: "search-gemini-api-key", AutodetectOrder: &order20, EnvVar: "GEMINI_SEARCH_API_KEY"},
		{ID: "firecrawl", Category: "search", ExecSecretID: "search-firecrawl-api-key", AutodetectOrder: &order60},
		{ID: "tavily", Category: "search", ExecSecretID: "search-tavily-api-key", AutodetectOrder: &order70},
		{ID: "discord", Category: "automation"},
		{ID: "telegram", Category: "automation"},
	}
}

func TestResolveSearchProvider(t *testing.T) {
	catalog := testSearchCatalog()

	tests := []struct {
		name           string
		credentials    map[string]string
		searchProvider string // explicit override
		want           string
	}{
		{
			name:        "no search providers in catalog",
			credentials: map[string]string{"brave": "present"},
			want:        "",
		},
		{
			name:        "no search credentials",
			credentials: map[string]string{"anthropic": "present"},
			want:        "",
		},
		{
			name:        "one search credential brave",
			credentials: map[string]string{"brave": "present"},
			want:        "brave",
		},
		{
			name:        "multiple search credentials picks lowest autodetect_order",
			credentials: map[string]string{"brave": "present", "tavily": "present"},
			want:        "brave",
		},
		{
			name:        "tavily only credential",
			credentials: map[string]string{"tavily": "present"},
			want:        "tavily",
		},
		{
			name:           "explicit override with valid credential",
			credentials:    map[string]string{"brave": "present", "tavily": "present"},
			searchProvider: "tavily",
			want:           "tavily",
		},
		{
			name:           "explicit override without credential falls back to auto-detect",
			credentials:    map[string]string{"brave": "present"},
			searchProvider: "tavily",
			want:           "brave",
		},
		{
			name:        "gemini-search credential returns runtime provider ID gemini",
			credentials: map[string]string{"gemini-search": "present"},
			want:        "gemini",
		},
		{
			name:           "explicit override gemini-search with credential returns gemini",
			credentials:    map[string]string{"gemini-search": "present", "brave": "present"},
			searchProvider: "gemini-search",
			want:           "gemini",
		},
		{
			name:        "brave beats gemini-search by autodetect_order",
			credentials: map[string]string{"brave": "present", "gemini-search": "present"},
			want:        "brave",
		},
		{
			name:        "gemini-search beats firecrawl by autodetect_order",
			credentials: map[string]string{"gemini-search": "present", "firecrawl": "present"},
			want:        "gemini",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := AssemblyParams{
				Credentials:     tt.credentials,
				SearchProvider:  tt.searchProvider,
				ProviderCatalog: catalog,
			}
			// Special case: test with empty catalog
			if tt.name == "no search providers in catalog" {
				params.ProviderCatalog = nil
			}
			got := resolveSearchProvider(params)
			if got != tt.want {
				t.Errorf("resolveSearchProvider() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveSearchProviderSeed(t *testing.T) {
	catalog := testSearchCatalog()

	tests := []struct {
		name           string
		providers      []string
		searchProvider string
		want           string
	}{
		{
			name:      "no providers",
			providers: []string{},
			want:      "",
		},
		{
			name:      "brave provider auto-detected",
			providers: []string{"brave"},
			want:      "brave",
		},
		{
			name:      "multiple providers picks lowest order",
			providers: []string{"tavily", "brave"},
			want:      "brave",
		},
		{
			name:           "explicit override with matching provider",
			providers:      []string{"brave", "tavily"},
			searchProvider: "tavily",
			want:           "tavily",
		},
		{
			name:           "explicit override without matching provider falls back",
			providers:      []string{"brave"},
			searchProvider: "tavily",
			want:           "brave",
		},
		{
			name:      "gemini-search returns runtime ID gemini",
			providers: []string{"gemini-search"},
			want:      "gemini",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := SeedParams{
				Providers:       tt.providers,
				SearchProvider:  tt.searchProvider,
				ProviderCatalog: catalog,
			}
			got := resolveSearchProviderSeed(params)
			if got != tt.want {
				t.Errorf("resolveSearchProviderSeed() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExecSecretID(t *testing.T) {
	catalog := testFullProviderCatalog()

	tests := []struct {
		name     string
		catalog  []CatalogProvider
		provider string
		want     string
	}{
		{
			name:     "anthropic with ExecSecretID set in catalog",
			catalog:  catalog,
			provider: "anthropic",
			want:     "anthropic-key",
		},
		{
			name:     "google without ExecSecretID falls back to id-key",
			catalog:  catalog,
			provider: "google",
			want:     "google-key",
		},
		{
			name:     "gemini-search with ExecSecretID override",
			catalog:  catalog,
			provider: "gemini-search",
			want:     "search-gemini-api-key",
		},
		{
			name:     "brave with ExecSecretID override",
			catalog:  catalog,
			provider: "brave",
			want:     "search-brave-api-key",
		},
		{
			name:     "unknown provider not in catalog falls back to hardcoded map",
			catalog:  nil,
			provider: "nebius",
			want:     "nebius-key",
		},
		{
			name:     "totally unknown provider falls back to id-key",
			catalog:  nil,
			provider: "some-new-provider",
			want:     "some-new-provider-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := execSecretID(tt.catalog, tt.provider)
			if got != tt.want {
				t.Errorf("execSecretID(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}

func TestEnvVarName(t *testing.T) {
	catalog := testFullProviderCatalog()

	tests := []struct {
		name     string
		catalog  []CatalogProvider
		provider string
		want     string
	}{
		{
			name:     "anthropic from catalog",
			catalog:  catalog,
			provider: "anthropic",
			want:     "ANTHROPIC_API_KEY",
		},
		{
			name:     "brave from catalog",
			catalog:  catalog,
			provider: "brave",
			want:     "BRAVE_API_KEY",
		},
		{
			name:     "fallback to hardcoded map with empty catalog",
			catalog:  nil,
			provider: "openai",
			want:     "OPENAI_API_KEY",
		},
		{
			name:     "unknown provider returns empty",
			catalog:  nil,
			provider: "totally-unknown",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := envVarName(tt.catalog, tt.provider)
			if got != tt.want {
				t.Errorf("envVarName(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}

func TestIsProxiedProvider(t *testing.T) {
	catalog := testFullProviderCatalog()

	tests := []struct {
		name     string
		catalog  []CatalogProvider
		provider string
		want     bool
	}{
		{
			name:     "anthropic is ai category -> proxied",
			catalog:  catalog,
			provider: "anthropic",
			want:     true,
		},
		{
			name:     "brave is search category -> proxied",
			catalog:  catalog,
			provider: "brave",
			want:     true,
		},
		{
			name:     "discord is automation category -> not proxied",
			catalog:  catalog,
			provider: "discord",
			want:     false,
		},
		{
			name:     "telegram is automation category -> not proxied",
			catalog:  catalog,
			provider: "telegram",
			want:     false,
		},
		{
			name:     "unknown not in catalog with empty catalog falls back to hardcoded map",
			catalog:  nil,
			provider: "nebius",
			want:     true,
		},
		{
			name:     "unknown not in catalog or hardcoded map -> not proxied",
			catalog:  nil,
			provider: "totally-unknown",
			want:     false,
		},
		{
			name:     "gemini-search is search category -> proxied",
			catalog:  catalog,
			provider: "gemini-search",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isProxiedProvider(tt.catalog, tt.provider)
			if got != tt.want {
				t.Errorf("isProxiedProvider(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

func TestRuntimeProviderID(t *testing.T) {
	catalog := testFullProviderCatalog()

	tests := []struct {
		name string
		id   string
		want string
	}{
		{
			name: "brave has no override -> returns brave",
			id:   "brave",
			want: "brave",
		},
		{
			name: "gemini-search overridden to gemini",
			id:   "gemini-search",
			want: "gemini",
		},
		{
			name: "anthropic has no override -> returns anthropic",
			id:   "anthropic",
			want: "anthropic",
		},
		{
			name: "unknown provider -> returns unknown",
			id:   "totally-unknown",
			want: "totally-unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runtimeProviderID(catalog, tt.id)
			if got != tt.want {
				t.Errorf("runtimeProviderID(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestFindCatalogProvider(t *testing.T) {
	catalog := testFullProviderCatalog()

	t.Run("found", func(t *testing.T) {
		p := findCatalogProvider(catalog, "anthropic")
		if p == nil {
			t.Fatal("expected non-nil provider for anthropic")
		}
		if p.ID != "anthropic" || p.Category != "ai" {
			t.Errorf("findCatalogProvider(anthropic) = %+v", p)
		}
	})

	t.Run("not found", func(t *testing.T) {
		p := findCatalogProvider(catalog, "nonexistent")
		if p != nil {
			t.Errorf("expected nil for nonexistent provider, got %+v", p)
		}
	})

	t.Run("gemini-search has runtime override", func(t *testing.T) {
		p := findCatalogProvider(catalog, "gemini-search")
		if p == nil {
			t.Fatal("expected non-nil provider for gemini-search")
		}
		if p.RuntimeProviderID != "gemini" {
			t.Errorf("gemini-search RuntimeProviderID = %q, want gemini", p.RuntimeProviderID)
		}
	})
}

func TestAssembleConfig_SearchProviderBrave(t *testing.T) {
	catalog := testSearchCatalog()

	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog:    testModelCatalog(),
		MachineID:       "m-search",
		ProxyBaseURL:    "http://192.168.100.1:4000",
		ProviderCatalog: catalog,
		Credentials:     map[string]string{"brave": "present"},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// Verify tools.web.search block
	search := mustGetNestedMap(t, m, "tools", "web", "search")
	if search["enabled"] != true {
		t.Errorf("tools.web.search.enabled = %v, want true", search["enabled"])
	}
	if search["provider"] != "brave" {
		t.Errorf("tools.web.search.provider = %v, want brave", search["provider"])
	}

	allow, ok := m["plugins"].(map[string]interface{})["allow"].([]interface{})
	if !ok {
		t.Fatal("plugins.allow missing or not an array")
	}
	foundBrave := false
	for _, item := range allow {
		if s, ok := item.(string); ok && s == "brave" {
			foundBrave = true
			break
		}
	}
	if !foundBrave {
		t.Error("plugins.allow should include brave for search-only providers")
	}

	providers := mustGetNestedMap(t, m, "models", "providers")
	if _, exists := providers["brave"]; exists {
		t.Error("models.providers.brave should not be present for search-only providers")
	}
}

func TestAssembleConfig_SearchProviderGeminiSearch(t *testing.T) {
	catalog := testSearchCatalog()

	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog:    testModelCatalog(),
		MachineID:       "m-search-gemini",
		ProxyBaseURL:    "http://192.168.100.1:4000",
		ProviderCatalog: catalog,
		Credentials:     map[string]string{"gemini-search": "present"},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// Verify tools.web.search.provider is "gemini" (runtime ID, not "gemini-search")
	search := mustGetNestedMap(t, m, "tools", "web", "search")
	if search["enabled"] != true {
		t.Errorf("tools.web.search.enabled = %v, want true", search["enabled"])
	}
	if search["provider"] != "gemini" {
		t.Errorf("tools.web.search.provider = %v, want gemini (runtime ID)", search["provider"])
	}

	// Verify search providers are not injected under models.providers.
	providers := mustGetNestedMap(t, m, "models", "providers")
	if _, ok := providers["gemini"]; ok {
		t.Error("models.providers should not have gemini key for search-only providers")
	}
	if _, ok := providers["gemini-search"]; ok {
		t.Error("models.providers should not have gemini-search key for search-only providers")
	}
}

func TestAssembleConfig_NoSearchWithoutCredential(t *testing.T) {
	catalog := testSearchCatalog()

	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog:    testModelCatalog(),
		MachineID:       "m-no-search",
		ProxyBaseURL:    "http://192.168.100.1:4000",
		ProviderCatalog: catalog,
		Credentials:     map[string]string{"anthropic": "present"},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// tools.web.search should not be present
	if tools, ok := getNestedMap(m, "tools"); ok {
		if web, ok := getNestedMap(tools, "web"); ok {
			if _, ok := web["search"]; ok {
				t.Error("tools.web.search should not be present without search credentials")
			}
		}
	}
}

// E2E: Verify the full plugin config structure matches what OpenClaw extensions expect.
// Each search plugin reads its key from plugins.entries.{plugin}.config.webSearch.apiKey
// (see extensions/exa/src/exa-web-search-provider.ts credentialPath).
func TestAssembleConfig_SearchPluginExecSecretRef(t *testing.T) {
	catalog := testSearchCatalog()

	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog:    testModelCatalog(),
		MachineID:       "m-plugin-e2e",
		ProxyBaseURL:    "http://192.168.100.1:4000",
		NativeMode:      true,
		ProviderCatalog: catalog,
		Credentials:     map[string]string{"brave": "present"},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// Verify plugins.entries.brave has the full config structure
	braveEntry := mustGetNestedMap(t, m, "plugins", "entries", "brave")
	if braveEntry["enabled"] != true {
		t.Errorf("plugins.entries.brave.enabled = %v, want true", braveEntry["enabled"])
	}
	webSearch := mustGetNestedMap(t, m, "plugins", "entries", "brave", "config", "webSearch")
	apiKey := mustGetNestedMap(t, m, "plugins", "entries", "brave", "config", "webSearch", "apiKey")
	if apiKey["source"] != "exec" {
		t.Errorf("apiKey.source = %v, want exec", apiKey["source"])
	}
	if apiKey["provider"] != "ocm" {
		t.Errorf("apiKey.provider = %v, want ocm", apiKey["provider"])
	}
	if apiKey["id"] != "search-brave-api-key" {
		t.Errorf("apiKey.id = %v, want search-brave-api-key", apiKey["id"])
	}
	_ = webSearch // used via mustGetNestedMap
}

func TestAssembleConfig_SearchPluginGeminiSearchExecSecretRef(t *testing.T) {
	catalog := testSearchCatalog()

	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog:    testModelCatalog(),
		MachineID:       "m-plugin-gemini-e2e",
		ProxyBaseURL:    "http://192.168.100.1:4000",
		NativeMode:      true,
		ProviderCatalog: catalog,
		Credentials:     map[string]string{"gemini-search": "present"},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// gemini-search uses plugin_id="google" and a dedicated search exec secret ID.
	googleEntry := mustGetNestedMap(t, m, "plugins", "entries", "google")
	if googleEntry["enabled"] != true {
		t.Errorf("plugins.entries.google.enabled = %v, want true", googleEntry["enabled"])
	}
	apiKey := mustGetNestedMap(t, m, "plugins", "entries", "google", "config", "webSearch", "apiKey")
	if apiKey["id"] != "search-gemini-api-key" {
		t.Errorf("apiKey.id = %v, want search-gemini-api-key", apiKey["id"])
	}

	// Should NOT have a gemini-search entry (uses google plugin)
	entries := mustGetNestedMap(t, m, "plugins", "entries")
	if _, ok := entries["gemini-search"]; ok {
		t.Error("plugins.entries should NOT have gemini-search (should use google)")
	}
}

func TestAssembleConfig_MultipleSearchProvidersAllEnabled(t *testing.T) {
	catalog := testSearchCatalog()

	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog:    testModelCatalog(),
		MachineID:       "m-multi-search",
		ProxyBaseURL:    "http://192.168.100.1:4000",
		NativeMode:      true,
		ProviderCatalog: catalog,
		Credentials:     map[string]string{"brave": "present", "exa": "present", "gemini-search": "present"},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// All three plugins should be enabled with exec secret refs
	entries := mustGetNestedMap(t, m, "plugins", "entries")
	for _, tc := range []struct {
		pluginID string
		secretID string
	}{
		{"brave", "search-brave-api-key"},
		{"exa", "search-exa-api-key"},
		{"google", "search-gemini-api-key"}, // gemini-search → google plugin
	} {
		entry, ok := entries[tc.pluginID].(map[string]interface{})
		if !ok {
			t.Errorf("plugins.entries.%s missing", tc.pluginID)
			continue
		}
		if entry["enabled"] != true {
			t.Errorf("plugins.entries.%s.enabled = %v, want true", tc.pluginID, entry["enabled"])
		}
		apiKey := mustGetNestedMap(t, m, "plugins", "entries", tc.pluginID, "config", "webSearch", "apiKey")
		if apiKey["id"] != tc.secretID {
			t.Errorf("plugins.entries.%s.config.webSearch.apiKey.id = %v, want %s", tc.pluginID, apiKey["id"], tc.secretID)
		}
	}

	// Active provider should be brave (lowest autodetect_order=10)
	search := mustGetNestedMap(t, m, "tools", "web", "search")
	if search["provider"] != "brave" {
		t.Errorf("tools.web.search.provider = %v, want brave (lowest autodetect_order)", search["provider"])
	}

	allow, ok := m["plugins"].(map[string]interface{})["allow"].([]interface{})
	if !ok {
		t.Fatal("plugins.allow missing or not an array")
	}
	for _, pluginID := range []string{"brave", "exa", "google"} {
		found := false
		for _, item := range allow {
			if s, ok := item.(string); ok && s == pluginID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("plugins.allow should include %s", pluginID)
		}
	}

	// Search providers should not appear under models.providers.
	providers := mustGetNestedMap(t, m, "models", "providers")
	for _, name := range []string{"brave", "exa", "gemini"} {
		if _, ok := providers[name]; ok {
			t.Errorf("models.providers.%s should not be present for search-only providers", name)
		}
	}
}

func TestAssembleSeedConfig_SearchProviderBrave(t *testing.T) {
	catalog := testSearchCatalog()

	data, err := AssembleSeedConfig(SeedParams{
		ModelCatalog:    testModelCatalog(),
		MachineID:       "m-seed-search",
		DefaultModel:    "anthropic/claude-sonnet-4-6",
		ProxyBaseURL:    "http://192.168.100.1:4000",
		Providers:       []string{"anthropic", "brave"},
		ProviderCatalog: catalog,
	})
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// Verify tools.web.search block in seed config
	search := mustGetNestedMap(t, m, "tools", "web", "search")
	if search["enabled"] != true {
		t.Errorf("tools.web.search.enabled = %v, want true", search["enabled"])
	}
	if search["provider"] != "brave" {
		t.Errorf("tools.web.search.provider = %v, want brave", search["provider"])
	}

	allow, ok := m["plugins"].(map[string]interface{})["allow"].([]interface{})
	if !ok {
		t.Fatal("plugins.allow missing or not an array")
	}
	foundBrave := false
	for _, item := range allow {
		if s, ok := item.(string); ok && s == "brave" {
			foundBrave = true
			break
		}
	}
	if !foundBrave {
		t.Error("plugins.allow should include brave for search-only providers")
	}

	providers := mustGetNestedMap(t, m, "models", "providers")
	if _, exists := providers["brave"]; exists {
		t.Error("models.providers.brave should not be present for search-only providers")
	}
}

func TestAssembleSeedConfig_SearchProviderGeminiSearch(t *testing.T) {
	catalog := testSearchCatalog()

	data, err := AssembleSeedConfig(SeedParams{
		ModelCatalog:    testModelCatalog(),
		MachineID:       "m-seed-search-gemini",
		DefaultModel:    "anthropic/claude-sonnet-4-6",
		ProxyBaseURL:    "http://192.168.100.1:4000",
		Providers:       []string{"anthropic", "gemini-search"},
		ProviderCatalog: catalog,
	})
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	// Verify search provider is runtime ID "gemini", not "gemini-search"
	search := mustGetNestedMap(t, m, "tools", "web", "search")
	if search["provider"] != "gemini" {
		t.Errorf("tools.web.search.provider = %v, want gemini (runtime ID)", search["provider"])
	}
}

func TestChannelToken_SlackWithBothCredentials(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "slack",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"channels": map[string]interface{}{
						"slack": map[string]interface{}{
							"enabled": true,
						},
					},
				},
			},
		},
		ChannelCredentialValues: map[string]string{
			"slack":     "xoxb-fake-bot-token",
			"slack-app": "xapp-fake-app-token",
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	sl, ok := getNestedMap(m, "channels", "slack")
	if !ok {
		t.Fatal("missing channels.slack")
	}
	if sl["enabled"] != true {
		t.Errorf("channels.slack.enabled = %v, want true", sl["enabled"])
	}

	if sl["botToken"] != "xoxb-fake-bot-token" {
		t.Errorf("channels.slack.botToken = %v, want xoxb-fake-bot-token", sl["botToken"])
	}
	if sl["appToken"] != "xapp-fake-app-token" {
		t.Errorf("channels.slack.appToken = %v, want xapp-fake-app-token", sl["appToken"])
	}
}

func TestPlatformDefaults_ChatCompletionsEnabled(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)

	endpoints, ok := getNestedMap(m, "gateway", "http", "endpoints")
	if !ok {
		t.Fatal("missing gateway.http.endpoints")
	}
	chat, ok := endpoints["chatCompletions"].(map[string]interface{})
	if !ok {
		t.Fatalf("gateway.http.endpoints.chatCompletions missing or wrong type: %T", endpoints["chatCompletions"])
	}
	if chat["enabled"] != true {
		t.Errorf("gateway.http.endpoints.chatCompletions.enabled = %v, want true", chat["enabled"])
	}
}

func TestPlatformDefaults_CronEnabled(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)

	cron, ok := getNestedMap(m, "cron")
	if !ok {
		t.Fatal("missing cron")
	}
	if cron["enabled"] != true {
		t.Errorf("cron.enabled = %v, want true", cron["enabled"])
	}
}

func TestPlatformDefaults_ShellEnvEnabled(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)

	shellEnv, ok := getNestedMap(m, "env", "shellEnv")
	if !ok {
		t.Fatal("missing env.shellEnv")
	}
	if shellEnv["enabled"] != true {
		t.Errorf("env.shellEnv.enabled = %v, want true", shellEnv["enabled"])
	}
}

func TestPlatformDefaults_ThreadBindingsEnabled(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)

	threadBindings, ok := getNestedMap(m, "session", "threadBindings")
	if !ok {
		t.Fatal("missing session.threadBindings")
	}
	if threadBindings["enabled"] != true {
		t.Errorf("session.threadBindings.enabled = %v, want true", threadBindings["enabled"])
	}
}

func TestAgentsDefaults_Heartbeat(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
		DefaultModel: "anthropic/claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)

	defaults, ok := getNestedMap(m, "agents", "defaults")
	if !ok {
		t.Fatal("missing agents.defaults")
	}
	hb, ok := defaults["heartbeat"].(map[string]interface{})
	if !ok {
		t.Fatalf("agents.defaults.heartbeat missing or wrong type: %T", defaults["heartbeat"])
	}
	if hb["every"] != "15m" {
		t.Errorf("agents.defaults.heartbeat.every = %v, want \"15m\"", hb["every"])
	}
}

// TestAssembleSeedConfig_MirrorsPlatformDefaults verifies the seed-config
// path emits the same five defaults as AssembleConfig/platformDefaults.
// These paths are separate by design (seed is for first-boot native-mode
// writes; AssembleConfig serves over HTTP), but they must stay in sync.
func TestAssembleSeedConfig_MirrorsPlatformDefaults(t *testing.T) {
	data, err := AssembleSeedConfig(SeedParams{
		ModelCatalog: testModelCatalog(),
		DefaultModel: "anthropic/claude-sonnet-4-6",
		Providers:    []string{"anthropic"},
		ProxyBaseURL: "http://192.168.100.1:4000",
		MachineID:    "m-test",
	})
	if err != nil {
		t.Fatalf("AssembleSeedConfig: %v", err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// gateway.http.endpoints.chatCompletions.enabled
	chat, ok := getNestedMap(cfg, "gateway", "http", "endpoints", "chatCompletions")
	if !ok {
		t.Fatal("seed: missing gateway.http.endpoints.chatCompletions")
	}
	if chat["enabled"] != true {
		t.Errorf("seed: gateway.http.endpoints.chatCompletions.enabled = %v, want true", chat["enabled"])
	}

	// session.threadBindings.enabled
	tb, ok := getNestedMap(cfg, "session", "threadBindings")
	if !ok {
		t.Fatal("seed: missing session.threadBindings")
	}
	if tb["enabled"] != true {
		t.Errorf("seed: session.threadBindings.enabled = %v, want true", tb["enabled"])
	}

	// cron.enabled
	cron, ok := getNestedMap(cfg, "cron")
	if !ok {
		t.Fatal("seed: missing cron")
	}
	if cron["enabled"] != true {
		t.Errorf("seed: cron.enabled = %v, want true", cron["enabled"])
	}

	// env.shellEnv.enabled
	shellEnv, ok := getNestedMap(cfg, "env", "shellEnv")
	if !ok {
		t.Fatal("seed: missing env.shellEnv")
	}
	if shellEnv["enabled"] != true {
		t.Errorf("seed: env.shellEnv.enabled = %v, want true", shellEnv["enabled"])
	}

	// agents.defaults.heartbeat.every
	defaults, ok := getNestedMap(cfg, "agents", "defaults")
	if !ok {
		t.Fatal("seed: missing agents.defaults")
	}
	hb, ok := defaults["heartbeat"].(map[string]interface{})
	if !ok {
		t.Fatalf("seed: agents.defaults.heartbeat missing or wrong type: %T", defaults["heartbeat"])
	}
	if hb["every"] != "15m" {
		t.Errorf("seed: agents.defaults.heartbeat.every = %v, want \"15m\"", hb["every"])
	}
}

// restFallbackResult builds a config where the legacy ocm-integrations REST
// plugin is present and enabled, used to check native-MCP injection behavior.
func restFallbackResult() map[string]interface{} {
	return map[string]interface{}{
		"plugins": map[string]interface{}{
			"allow": []interface{}{"ocm-integrations"},
			"entries": map[string]interface{}{
				"ocm-integrations": map[string]interface{}{
					"enabled": true,
					"config":  map[string]interface{}{"apiUrl": "https://x/api"},
				},
			},
		},
	}
}

// A configured-but-unavailable signer (nil, or one that errors) must not abort
// config assembly, and must leave the REST fallback plugin in place. Regression
// test for the fleet-wide config-push failure when JWT_SECRET was weak/unset.
func TestInjectWorkspaceIntegrationNativeMCPConfig_UnavailableSignerDoesNotAbort(t *testing.T) {
	cases := map[string]func(string) (string, error){
		"nil signer":      nil,
		"erroring signer": func(string) (string, error) { return "", fmt.Errorf("signer not configured") },
	}
	for name, signer := range cases {
		t.Run(name, func(t *testing.T) {
			result := restFallbackResult()
			if err := injectWorkspaceIntegrationNativeMCPConfig(result, "machine-1", "https://backend/api/ocm-integrations", signer, nil, false); err != nil {
				t.Fatalf("injector must not abort on an unavailable signer, got: %v", err)
			}
			if mcp, ok := result["mcp"].(map[string]interface{}); ok {
				if servers, ok := mcp["servers"].(map[string]interface{}); ok {
					if _, present := servers["ocm"]; present {
						t.Fatal("native MCP server should not be injected without a working signer")
					}
				}
			}
			plugins := result["plugins"].(map[string]interface{})
			allow := plugins["allow"].([]interface{})
			if len(allow) != 1 || allow[0] != "ocm-integrations" {
				t.Fatalf("REST fallback should remain in allow-list, got %v", allow)
			}
			entry := plugins["entries"].(map[string]interface{})["ocm-integrations"].(map[string]interface{})
			if entry["enabled"] != true {
				t.Fatal("REST fallback should stay enabled when native MCP is not injected")
			}
		})
	}
}

func TestInjectWorkspaceIntegrationNativeMCPConfig_WorkingSignerInjectsAndDisablesREST(t *testing.T) {
	result := restFallbackResult()
	signer := func(string) (string, error) { return "tok-123", nil }
	if err := injectWorkspaceIntegrationNativeMCPConfig(result, "machine-1", "https://backend/api/ocm-integrations", signer, nil, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	servers := result["mcp"].(map[string]interface{})["servers"].(map[string]interface{})
	if _, ok := servers["ocm"]; !ok {
		t.Fatal("native MCP server should be injected with a working signer")
	}
	plugins := result["plugins"].(map[string]interface{})
	if allow := plugins["allow"].([]interface{}); len(allow) != 0 {
		t.Fatalf("REST plugin should be removed from allow-list, got %v", allow)
	}
	entry := plugins["entries"].(map[string]interface{})["ocm-integrations"].(map[string]interface{})
	if entry["enabled"] != false {
		t.Fatal("REST plugin should be disabled once native MCP is injected")
	}
}
