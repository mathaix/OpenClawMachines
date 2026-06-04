package configassembly

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// HermesChannelEnvVars maps OCM credential providers to Hermes gateway env vars.
// The values are the secret destinations in $HERMES_HOME/.env.
var HermesChannelEnvVars = map[string]string{
	"telegram":  "TELEGRAM_BOT_TOKEN",
	"discord":   "DISCORD_BOT_TOKEN",
	"slack":     "SLACK_BOT_TOKEN",
	"slack-app": "SLACK_APP_TOKEN",
}

// HermesSeedParams contains the data needed to write a first-boot Hermes home.
type HermesSeedParams struct {
	MachineID                string
	DefaultModel             string
	ProxyBaseURL             string
	GatewayToken             string
	VMHostname               string
	ProviderCredentialValues map[string]string // provider id -> decrypted credential value
	ChannelCredentialValues  map[string]string // channel credential provider -> decrypted token value
	ChannelSettings          map[string]map[string]interface{}
	ProviderCatalog          []CatalogProvider
	BrowserCDPURL            string
	Soul                     string
	AuthJSON                 string
}

// HermesSeedConfig is the guest payload written to $HERMES_HOME.
type HermesSeedConfig struct {
	ConfigYAML []byte
	Env        []byte
	AuthJSON   []byte
	Soul       []byte
}

// AssembleHermesSeedConfig produces the files consumed by scripts/init-hermes.sh.
func AssembleHermesSeedConfig(params HermesSeedParams) (*HermesSeedConfig, error) {
	defaultModel := strings.TrimSpace(params.DefaultModel)
	if defaultModel == "" {
		defaultModel = "openai/gpt-oss-120b"
	}

	modelConfig := map[string]any{
		"default":  defaultModel,
		"provider": "auto",
	}
	if defaultModel == "openai/gpt-oss-120b" &&
		strings.TrimSpace(params.ProxyBaseURL) != "" &&
		strings.TrimSpace(params.ProviderCredentialValues["nebius"]) != "" {
		modelConfig["provider"] = "custom"
		modelConfig["base_url"] = "__OCM_API_PROXY_BASE_URL__/nebius/v1"
		modelConfig["api_key"] = "__OCM_API_PROXY_KEY__"
	}

	config := map[string]any{
		"model": modelConfig,
		"terminal": map[string]any{
			"backend":          "local",
			"cwd":              "/opt/data/workspace",
			"timeout":          180,
			"lifetime_seconds": 300,
		},
		"platforms": map[string]any{},
		"dashboard": map[string]any{
			"host": "127.0.0.1",
			"port": 9119,
		},
	}

	platforms := config["platforms"].(map[string]any)
	platforms["api_server"] = map[string]any{
		"enabled": true,
		"host":    "127.0.0.1",
		"port":    18789,
	}
	if params.VMHostname != "" {
		platforms["api_server"].(map[string]any)["public_url"] = "https://" + params.VMHostname + "/gateway"
	}

	for provider := range params.ChannelCredentialValues {
		switch provider {
		case "telegram":
			platforms["telegram"] = map[string]any{"enabled": true}
		case "discord":
			platforms["discord"] = map[string]any{"enabled": true}
		case "slack", "slack-app":
			slack, _ := platforms["slack"].(map[string]any)
			if slack == nil {
				slack = map[string]any{}
			}
			slack["enabled"] = true
			platforms["slack"] = slack
		}
	}

	if cdpURL := strings.TrimSpace(params.BrowserCDPURL); cdpURL != "" {
		config["browser"] = map[string]any{
			"enabled":     true,
			"cdp_url":     cdpURL,
			"attach_only": true,
			"headless":    false,
		}
	} else {
		config["browser"] = map[string]any{
			"enabled":  true,
			"headless": true,
		}
	}

	configYAML, err := yaml.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal hermes config yaml: %w", err)
	}

	envVars := map[string]string{
		"API_SERVER_ENABLED":        "true",
		"API_SERVER_HOST":           "127.0.0.1",
		"API_SERVER_PORT":           "18789",
		"API_SERVER_KEY":            params.GatewayToken,
		"HERMES_DASHBOARD":          "1",
		"HERMES_DASHBOARD_HOST":     "127.0.0.1",
		"HERMES_DASHBOARD_PORT":     "9119",
		"HERMES_DASHBOARD_TUI":      "1",
		"HERMES_INFERENCE_PROVIDER": "auto",
	}
	for provider, value := range params.ProviderCredentialValues {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		envName := envVarName(params.ProviderCatalog, provider)
		if envName == "" {
			envName = hermesFallbackProviderEnvVar(provider)
		}
		if envName != "" {
			envVars[envName] = value
		}
	}
	for provider, value := range params.ChannelCredentialValues {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if envName := HermesChannelEnvVars[provider]; envName != "" {
			envVars[envName] = value
		}
	}
	if strings.TrimSpace(params.ChannelCredentialValues["telegram"]) != "" {
		if allowedUsers := HermesTelegramAllowedUsers(params.ChannelSettings["telegram"]); allowedUsers != "" {
			envVars["TELEGRAM_ALLOWED_USERS"] = allowedUsers
		}
	}
	envBytes := renderDotEnv(envVars)

	authJSON := []byte(strings.TrimSpace(params.AuthJSON))
	if len(authJSON) == 0 {
		authJSON = []byte("{}")
	}
	if !json.Valid(authJSON) {
		return nil, fmt.Errorf("hermes auth json is invalid")
	}

	soul := strings.TrimSpace(params.Soul)
	if soul == "" {
		soul = "You are a concise, careful AI assistant running inside a managed Hermes Machine."
	}

	return &HermesSeedConfig{
		ConfigYAML: configYAML,
		Env:        envBytes,
		AuthJSON:   authJSON,
		Soul:       []byte(soul + "\n"),
	}, nil
}

func renderDotEnv(values map[string]string) []byte {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b bytes.Buffer
	for _, k := range keys {
		v := values[k]
		if v == "" {
			continue
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(quoteDotEnv(v))
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func quoteDotEnv(value string) string {
	if value == "" {
		return `""`
	}
	needsQuote := strings.ContainsAny(value, " \t\n\r\"'\\#$")
	if !needsQuote {
		return value
	}
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`)
	return `"` + replacer.Replace(value) + `"`
}

// HermesTelegramAllowedUsers normalizes the Hermes Telegram allow-list setting
// into the comma-separated value expected by TELEGRAM_ALLOWED_USERS.
func HermesTelegramAllowedUsers(settings map[string]interface{}) string {
	if len(settings) == 0 {
		return ""
	}
	for _, key := range []string{"allowedUsers", "allowed_users", "allowedUserIds", "allowed_user_ids"} {
		if value, ok := settings[key]; ok {
			if normalized := normalizeCommaList(value); normalized != "" {
				return normalized
			}
		}
	}
	return ""
}

func normalizeCommaList(value interface{}) string {
	var parts []string
	add := func(raw string) {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				parts = append(parts, part)
			}
		}
	}

	switch typed := value.(type) {
	case string:
		add(typed)
	case []string:
		for _, item := range typed {
			add(item)
		}
	case []interface{}:
		for _, item := range typed {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	}
	return strings.Join(parts, ",")
}

func hermesFallbackProviderEnvVar(provider string) string {
	switch provider {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "google", "gemini":
		return "GOOGLE_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "ollama-cloud":
		return "OLLAMA_API_KEY"
	case "zai", "glm":
		return "GLM_API_KEY"
	case "kimi", "kimi-coding":
		return "KIMI_API_KEY"
	case "minimax":
		return "MINIMAX_API_KEY"
	case "arcee":
		return "ARCEEAI_API_KEY"
	case "huggingface":
		return "HF_TOKEN"
	case "opencode-zen":
		return "OPENCODE_ZEN_API_KEY"
	case "opencode-go":
		return "OPENCODE_GO_API_KEY"
	case "nous", "nous-api":
		return "NOUS_API_KEY"
	default:
		return ""
	}
}
