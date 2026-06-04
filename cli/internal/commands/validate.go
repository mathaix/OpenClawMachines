package commands

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// validateCredential validates a provider credential locally before sending to the backend.
// This provides fast feedback without a server round-trip.
func validateCredential(provider, key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	switch provider {
	case "anthropic":
		return validateAnthropicKey(ctx, key)
	case "openai":
		return validateOpenAIKey(ctx, key)
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
		return nil // unknown providers skip local validation
	}
}

func validateAnthropicKey(ctx context.Context, key string) error {
	// OAuth/setup-tokens (sk-ant-oat*) authenticate via Bearer token, not x-api-key.
	if strings.HasPrefix(key, "sk-ant-oat") {
		return validateAnthropicOAuthToken(ctx, key)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")

	return doValidation(req, "Anthropic")
}

// validateAnthropicOAuthToken validates an OAuth/setup-token by sending it as a Bearer token.
// Anthropic recognizes valid OAuth tokens even when OAuth is temporarily disabled,
// returning a distinct error ("OAuth authentication is currently not supported")
// vs. rejecting unknown tokens outright.
func validateAnthropicOAuthToken(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// 200 = OAuth working and token valid
	// 429 = rate limited but token recognized
	if resp.StatusCode == 200 || resp.StatusCode == 429 {
		return nil
	}

	// Anthropic returns 401 for both "OAuth not supported" (valid token, feature disabled)
	// and truly invalid tokens. Check the body to distinguish.
	bodyStr := string(body)
	if resp.StatusCode == 401 && strings.Contains(bodyStr, "OAuth") {
		// Token format is recognized by Anthropic — it's a valid OAuth token
		// even though OAuth may be temporarily disabled.
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

	return doValidation(req, "OpenAI")
}

func validateGoogleKey(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://generativelanguage.googleapis.com/v1beta/models", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("x-goog-api-key", key)

	return doValidationWithCodes(req, "Google", []int{400, 403})
}

func validateDiscordKey(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://discord.com/api/v10/users/@me", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+key)

	return doValidation(req, "Discord")
}

func validateTelegramKey(ctx context.Context, key string) error {
	// Strip any stray whitespace/control chars from paste
	key = strings.TrimSpace(key)

	// Telegram tokens look like "123456789:ABCdefGHI..."
	// If it doesn't contain a colon, it's definitely wrong
	if !strings.Contains(key, ":") {
		return fmt.Errorf("invalid bot token format (expected NUMBER:LETTERS, got %d chars — token may have been truncated during paste)", len(key))
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/getMe", key)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	if resp.StatusCode == 200 || resp.StatusCode == 429 {
		return nil
	}

	return fmt.Errorf("bot token rejected by Telegram (HTTP %d). Token length: %d, first 6 chars: %s...", resp.StatusCode, len(key), key[:min(6, len(key))])
}

func validateWhatsAppKey(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://graph.facebook.com/v18.0/me", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)

	return doValidation(req, "WhatsApp")
}

// doValidation sends the request and checks for 401 (invalid) vs 200/429 (valid).
func doValidation(req *http.Request, providerName string) error {
	return doValidationWithCodes(req, providerName, []int{401})
}

// doValidationWithCodes sends the request and checks for invalid status codes.
func doValidationWithCodes(req *http.Request, providerName string, invalidCodes []int) error {
	resp, err := httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	for _, code := range invalidCodes {
		if resp.StatusCode == code {
			return fmt.Errorf("invalid API key")
		}
	}
	if resp.StatusCode == 200 || resp.StatusCode == 429 {
		return nil
	}

	return fmt.Errorf("unexpected status %d from %s API", resp.StatusCode, providerName)
}

func validateSlackBotToken(ctx context.Context, key string) error {
	if !strings.HasPrefix(key, "xoxb-") {
		return fmt.Errorf("invalid bot token format: must start with xoxb-")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://slack.com/api/auth.test", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)

	return doValidation(req, "Slack")
}

func validateSlackAppToken(key string) error {
	if !strings.HasPrefix(key, "xapp-") {
		return fmt.Errorf("invalid app token format: must start with xapp-")
	}
	return nil
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second}
}
