package configassembly

import (
	"strings"
	"testing"
)

func TestAssembleHermesSeedConfig_ProjectsProvidersAndChannelsToEnv(t *testing.T) {
	key := "ANTHROPIC_API_KEY"
	cfg, err := AssembleHermesSeedConfig(HermesSeedParams{
		DefaultModel: "anthropic/claude-sonnet-4-6",
		GatewayToken: "gateway-token",
		ProviderCredentialValues: map[string]string{
			"anthropic": "sk-ant-test",
		},
		ChannelCredentialValues: map[string]string{
			"telegram":  "telegram-token",
			"slack":     "xoxb-token",
			"slack-app": "xapp-token",
		},
		ChannelSettings: map[string]map[string]interface{}{
			"telegram": {"allowedUsers": "12345, 67890"},
		},
		ProviderCatalog: []CatalogProvider{
			{ID: "anthropic", Category: "ai", EnvVar: key},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHermesSeedConfig: %v", err)
	}

	env := string(cfg.Env)
	for _, want := range []string{
		"ANTHROPIC_API_KEY=sk-ant-test",
		"TELEGRAM_BOT_TOKEN=telegram-token",
		"TELEGRAM_ALLOWED_USERS=12345,67890",
		"SLACK_BOT_TOKEN=xoxb-token",
		"SLACK_APP_TOKEN=xapp-token",
		"API_SERVER_KEY=gateway-token",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("env missing %q:\n%s", want, env)
		}
	}

	yaml := string(cfg.ConfigYAML)
	for _, want := range []string{
		"default: anthropic/claude-sonnet-4-6",
		"provider: auto",
		"api_server:",
		"telegram:",
		"slack:",
	} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("config yaml missing %q:\n%s", want, yaml)
		}
	}
}

func TestAssembleHermesSeedConfig_RejectsInvalidAuthJSON(t *testing.T) {
	_, err := AssembleHermesSeedConfig(HermesSeedParams{AuthJSON: "{not-json"})
	if err == nil {
		t.Fatal("expected invalid auth json error")
	}
}

func TestAssembleHermesSeedConfig_DefaultsToPlatformNebiusProxy(t *testing.T) {
	cfg, err := AssembleHermesSeedConfig(HermesSeedParams{
		ProxyBaseURL: "http://192.168.100.1:4000",
		ProviderCredentialValues: map[string]string{
			"nebius": "platform-key",
		},
	})
	if err != nil {
		t.Fatalf("AssembleHermesSeedConfig: %v", err)
	}

	yaml := string(cfg.ConfigYAML)
	for _, want := range []string{
		"default: openai/gpt-oss-120b",
		"provider: custom",
		"base_url: __OCM_API_PROXY_BASE_URL__/nebius/v1",
		"api_key: __OCM_API_PROXY_KEY__",
	} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("config yaml missing %q:\n%s", want, yaml)
		}
	}
}

func TestHermesChannelEnvVarsCoverOpenClawTokenChannels(t *testing.T) {
	for channel, fields := range ChannelTokenFields {
		for _, field := range fields {
			if HermesChannelEnvVars[field.Provider] == "" {
				t.Fatalf("channel %s provider %s has no Hermes env var", channel, field.Provider)
			}
		}
	}
}
