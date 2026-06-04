package apiproxy

import (
	"encoding/json"
	"net/http"
	"strings"
)

func extractBearerOrXAPIKey(req *http.Request) string {
	auth := strings.TrimSpace(req.Header.Get("Authorization"))
	if auth != "" {
		if token, ok := strings.CutPrefix(auth, "Bearer "); ok {
			token = strings.TrimSpace(token)
			if token != "" {
				return token
			}
		}
	}
	return strings.TrimSpace(req.Header.Get("x-api-key"))
}

func stripJSONFields(body []byte, fields ...string) ([]byte, error) {
	if len(fields) == 0 {
		return body, nil
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, err
	}

	changed := false
	for _, field := range fields {
		if _, ok := payload[field]; ok {
			delete(payload, field)
			changed = true
		}
	}
	if !changed {
		return body, nil
	}

	mutated, err := json.Marshal(payload)
	if err != nil {
		return body, err
	}
	return mutated, nil
}

// Provider defines how to interact with a specific LLM API provider.
type Provider struct {
	Name               string
	UpstreamHost       string
	Scheme             string // always "https"
	PathPrefix         string // e.g. "/anthropic", "/openai", "/google"
	UpstreamPathPrefix string // prepended to remaining path before forwarding (e.g. "/api" for OpenRouter)
	AllowedHosts       []string
	ResolveHost        func(path string) string // optional: dynamic upstream host from request path
	KeyOptional        bool                     // true = skip LLMKeys check (CDN passthrough)
	InjectKey          func(req *http.Request, key, credentialType string)
	MutateBody         func(body []byte, credentialType string) ([]byte, error) // optional: modify request body before upstream
	ExtractToken       func(req *http.Request) string
	ParseJSONUsage     func(body []byte) (model string, inputTokens, outputTokens int)
	ParseSSEEvent      func(eventType, data string) (model string, inputTokens, outputTokens int, isFinal bool)
}

// initProviders returns a map of all supported providers.
func initProviders() map[string]*Provider {
	return map[string]*Provider{
		"anthropic":    anthropicProvider(),
		"openai":       openaiProvider(),
		"openai-codex": openaiCodexProvider(),
		"google":       googleProvider(),
		"openrouter":   openrouterProvider(),
		"nebius":       nebiusProvider(),
		"telegram":     telegramProvider(),
		"discord_bot":  discordBotProvider(),
		"whatsapp":     whatsappProvider(),
	}
}

// --- Anthropic ---

func anthropicProvider() *Provider {
	return &Provider{
		Name:         "anthropic",
		UpstreamHost: "api.anthropic.com",
		Scheme:       "https",
		PathPrefix:   "/anthropic",
		AllowedHosts: []string{"api.anthropic.com"},
		InjectKey: func(req *http.Request, key, credentialType string) {
			// OAuth tokens use Bearer auth; regular API keys use x-api-key
			if credentialType == "subscription_key" || credentialType == "oauth" {
				req.Header.Set("Authorization", "Bearer "+key)
				req.Header.Set("anthropic-beta", "oauth-2025-04-20")
			} else {
				req.Header.Set("x-api-key", key)
			}
			// Ensure anthropic-version header is present
			if req.Header.Get("anthropic-version") == "" {
				req.Header.Set("anthropic-version", "2023-06-01")
			}
		},
		MutateBody: func(body []byte, credentialType string) ([]byte, error) {
			// Subscription tokens require a Claude Code identity system prompt
			// to access Opus/Sonnet models. Prepend it to the system field.
			if credentialType != "subscription_key" {
				return body, nil
			}
			return injectAnthropicSubscriptionIdentity(body)
		},
		ExtractToken: func(req *http.Request) string {
			return req.Header.Get("x-api-key")
		},
		ParseJSONUsage: func(body []byte) (string, int, int) {
			var resp struct {
				Model string `json:"model"`
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return "", 0, 0
			}
			return resp.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens
		},
		ParseSSEEvent: func(eventType, data string) (string, int, int, bool) {
			switch eventType {
			case "message_start":
				var event struct {
					Message struct {
						Model string `json:"model"`
						Usage struct {
							InputTokens int `json:"input_tokens"`
						} `json:"usage"`
					} `json:"message"`
				}
				if err := json.Unmarshal([]byte(data), &event); err != nil {
					return "", 0, 0, false
				}
				return event.Message.Model, event.Message.Usage.InputTokens, 0, false

			case "message_delta":
				var event struct {
					Usage struct {
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
				}
				if err := json.Unmarshal([]byte(data), &event); err != nil {
					return "", 0, 0, false
				}
				return "", 0, event.Usage.OutputTokens, true

			default:
				return "", 0, 0, false
			}
		},
	}
}

// --- OpenAI ---

func openaiProvider() *Provider {
	return &Provider{
		Name:         "openai",
		UpstreamHost: "api.openai.com",
		Scheme:       "https",
		PathPrefix:   "/openai",
		AllowedHosts: []string{"api.openai.com"},
		InjectKey: func(req *http.Request, key, credentialType string) {
			req.Header.Set("Authorization", "Bearer "+key)
		},
		ExtractToken: func(req *http.Request) string {
			auth := req.Header.Get("Authorization")
			return strings.TrimPrefix(auth, "Bearer ")
		},
		ParseJSONUsage: func(body []byte) (string, int, int) {
			var resp struct {
				Model string `json:"model"`
				Usage struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return "", 0, 0
			}
			return resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens
		},
		ParseSSEEvent: func(eventType, data string) (string, int, int, bool) {
			data = strings.TrimSpace(data)
			if data == "[DONE]" {
				return "", 0, 0, true
			}

			var chunk struct {
				Model string `json:"model"`
				Usage *struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return "", 0, 0, false
			}
			if chunk.Usage != nil {
				return chunk.Model, chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens, true
			}
			return chunk.Model, 0, 0, false
		},
	}
}

// --- OpenAI Codex (ChatGPT subscription OAuth) ---

func openaiCodexProvider() *Provider {
	return &Provider{
		Name:         "openai-codex",
		UpstreamHost: "chatgpt.com",
		Scheme:       "https",
		PathPrefix:   "/openai-codex",
		AllowedHosts: []string{"chatgpt.com"},
		KeyOptional:  false,
		InjectKey: func(req *http.Request, key, credType string) {
			req.Header.Set("Authorization", "Bearer "+key)
		},
		ExtractToken: func(req *http.Request) string {
			return req.Header.Get("x-api-key") // nonce, like other providers
		},
		// Responses API usage: input_tokens/output_tokens in usage object
		ParseJSONUsage: func(body []byte) (string, int, int) {
			var resp struct {
				Model string `json:"model"`
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return "", 0, 0
			}
			return resp.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens
		},
		ParseSSEEvent: func(eventType, data string) (string, int, int, bool) {
			data = strings.TrimSpace(data)
			if data == "[DONE]" {
				return "", 0, 0, true
			}

			var chunk struct {
				Model string `json:"model"`
				Usage *struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return "", 0, 0, false
			}
			if chunk.Usage != nil {
				return chunk.Model, chunk.Usage.InputTokens, chunk.Usage.OutputTokens, true
			}
			return chunk.Model, 0, 0, false
		},
	}
}

// --- OpenRouter ---

func openrouterProvider() *Provider {
	return &Provider{
		Name:               "openrouter",
		UpstreamHost:       "openrouter.ai",
		Scheme:             "https",
		PathPrefix:         "/openrouter",
		UpstreamPathPrefix: "/api",
		AllowedHosts:       []string{"openrouter.ai"},
		InjectKey: func(req *http.Request, key, credentialType string) {
			req.Header.Set("Authorization", "Bearer "+key)
		},
		ExtractToken: func(req *http.Request) string {
			auth := req.Header.Get("Authorization")
			return strings.TrimPrefix(auth, "Bearer ")
		},
		ParseJSONUsage: func(body []byte) (string, int, int) {
			var resp struct {
				Model string `json:"model"`
				Usage struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return "", 0, 0
			}
			return resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens
		},
		ParseSSEEvent: func(eventType, data string) (string, int, int, bool) {
			data = strings.TrimSpace(data)
			if data == "[DONE]" {
				return "", 0, 0, true
			}

			var chunk struct {
				Model string `json:"model"`
				Usage *struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return "", 0, 0, false
			}
			if chunk.Usage != nil {
				return chunk.Model, chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens, true
			}
			return chunk.Model, 0, 0, false
		},
	}
}

// --- Nebius ---

func nebiusProvider() *Provider {
	return &Provider{
		Name:         "nebius",
		UpstreamHost: "api.tokenfactory.nebius.com",
		Scheme:       "https",
		PathPrefix:   "/nebius",
		AllowedHosts: []string{"api.tokenfactory.nebius.com"},
		InjectKey: func(req *http.Request, key, credentialType string) {
			req.Header.Set("Authorization", "Bearer "+key)
		},
		ExtractToken: func(req *http.Request) string {
			auth := req.Header.Get("Authorization")
			return strings.TrimPrefix(auth, "Bearer ")
		},
		MutateBody: func(body []byte, credentialType string) ([]byte, error) {
			// Request usage in streaming responses — Nebius (OpenAI-compatible) only
			// returns token counts when stream_options.include_usage is true.
			var req map[string]interface{}
			if err := json.Unmarshal(body, &req); err != nil {
				return body, nil
			}
			if _, hasStream := req["stream"]; hasStream {
				req["stream_options"] = map[string]interface{}{
					"include_usage": true,
				}
				modified, err := json.Marshal(req)
				if err != nil {
					return body, nil
				}
				return modified, nil
			}
			return body, nil
		},
		ParseJSONUsage: func(body []byte) (string, int, int) {
			var resp struct {
				Model string `json:"model"`
				Usage struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return "", 0, 0
			}
			return resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens
		},
		ParseSSEEvent: func(eventType, data string) (string, int, int, bool) {
			data = strings.TrimSpace(data)
			if data == "[DONE]" {
				return "", 0, 0, true
			}

			var chunk struct {
				Model string `json:"model"`
				Usage *struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return "", 0, 0, false
			}
			if chunk.Usage != nil {
				return chunk.Model, chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens, true
			}
			return chunk.Model, 0, 0, false
		},
	}
}

// --- Telegram ---

func telegramProvider() *Provider {
	return &Provider{
		Name:         "telegram",
		UpstreamHost: "api.telegram.org",
		Scheme:       "https",
		PathPrefix:   "/telegram",
		AllowedHosts: []string{"api.telegram.org"},
		InjectKey: func(req *http.Request, key, credentialType string) {
			path := strings.TrimPrefix(req.URL.Path, "/telegram")
			if strings.HasPrefix(path, "/file/") {
				// File download: /telegram/file/{filePath} → /file/bot{KEY}/{filePath}
				filePath := strings.TrimPrefix(path, "/file")
				req.URL.Path = "/file/bot" + key + filePath
			} else {
				// API call: /telegram/{method} → /bot{KEY}/{method}
				req.URL.Path = "/bot" + key + path
			}
		},
		ExtractToken: func(req *http.Request) string {
			return req.Header.Get("x-api-key")
		},
	}
}

// --- Discord Bot ---

func discordBotProvider() *Provider {
	return &Provider{
		Name:         "discord_bot",
		UpstreamHost: "discord.com",
		Scheme:       "https",
		PathPrefix:   "/discord",
		AllowedHosts: []string{"discord.com", "gateway.discord.gg", "cdn.discordapp.com", "media.discordapp.net"},
		ResolveHost: func(path string) string {
			if strings.HasPrefix(path, "/cdn/") || strings.HasPrefix(path, "/attachments/") {
				return "cdn.discordapp.com"
			}
			if strings.HasPrefix(path, "/media/") || strings.HasPrefix(path, "/stickers/") {
				return "media.discordapp.net"
			}
			return "discord.com"
		},
		InjectKey: func(req *http.Request, key, credentialType string) {
			// CDN URLs are pre-signed — no auth header needed
			if req.URL.Host == "cdn.discordapp.com" || req.URL.Host == "media.discordapp.net" {
				return
			}
			req.Header.Set("Authorization", "Bot "+key)
		},
		ExtractToken: func(req *http.Request) string {
			return req.Header.Get("x-api-key")
		},
	}
}

// --- WhatsApp ---

func whatsappProvider() *Provider {
	return &Provider{
		Name:         "whatsapp",
		UpstreamHost: "graph.facebook.com",
		Scheme:       "https",
		PathPrefix:   "/whatsapp",
		AllowedHosts: []string{"graph.facebook.com", "lookaside.fbsbx.com"},
		ResolveHost: func(path string) string {
			if strings.HasPrefix(path, "/media/") {
				return "lookaside.fbsbx.com"
			}
			return "graph.facebook.com"
		},
		InjectKey: func(req *http.Request, key, credentialType string) {
			req.Header.Set("Authorization", "Bearer "+key)
		},
		ExtractToken: func(req *http.Request) string {
			return req.Header.Get("x-api-key")
		},
	}
}

// --- Google ---

func googleProvider() *Provider {
	return &Provider{
		Name:               "google",
		UpstreamHost:       "generativelanguage.googleapis.com",
		Scheme:             "https",
		PathPrefix:         "/google",
		UpstreamPathPrefix: "/v1beta/openai",
		AllowedHosts:       []string{"generativelanguage.googleapis.com"},
		InjectKey: func(req *http.Request, key, credentialType string) {
			// Google's OpenAI-compatible endpoint expects Bearer auth.
			req.Header.Set("Authorization", "Bearer "+key)
		},
		MutateBody: func(body []byte, credentialType string) ([]byte, error) {
			// Gemini's OpenAI-compatible chat/completions endpoint rejects some
			// OpenAI-only request fields such as `store`.
			return stripJSONFields(body, "store")
		},
		ExtractToken: func(req *http.Request) string {
			// OpenAI-compatible runtimes may send the nonce as Authorization: Bearer
			// or x-api-key depending on the client path. Accept both.
			return extractBearerOrXAPIKey(req)
		},
		// Responses come back in OpenAI format from the compatibility endpoint.
		ParseJSONUsage: func(body []byte) (string, int, int) {
			var resp struct {
				Model string `json:"model"`
				Usage struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return "", 0, 0
			}
			return resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens
		},
		ParseSSEEvent: func(eventType, data string) (string, int, int, bool) {
			data = strings.TrimSpace(data)
			if data == "[DONE]" {
				return "", 0, 0, true
			}

			var chunk struct {
				Model string `json:"model"`
				Usage *struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return "", 0, 0, false
			}
			if chunk.Usage != nil {
				return chunk.Model, chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens, true
			}
			return chunk.Model, 0, 0, false
		},
	}
}

// anthropicSubscriptionIdentity is the system prompt Anthropic requires for
// subscription (setup) tokens to access Opus/Sonnet models.
const anthropicSubscriptionIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

// injectAnthropicSubscriptionIdentity prepends the required identity system
// prompt to an Anthropic messages API request body. If a system field already
// exists, the identity text is prepended; otherwise it's created.
func injectAnthropicSubscriptionIdentity(body []byte) ([]byte, error) {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return body, err
	}

	identityBlock := map[string]interface{}{
		"type": "text",
		"text": anthropicSubscriptionIdentity,
	}

	switch existing := req["system"].(type) {
	case []interface{}:
		// Prepend identity to existing system blocks
		req["system"] = append([]interface{}{identityBlock}, existing...)
	case string:
		// Convert string system to array with identity + original
		req["system"] = []interface{}{
			identityBlock,
			map[string]interface{}{"type": "text", "text": existing},
		}
	default:
		// No system field — create one
		req["system"] = []interface{}{identityBlock}
	}

	return json.Marshal(req)
}
