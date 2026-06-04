//go:build e2e

package apiproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/secrets"
)

// TestE2E_Anthropic makes a real API call to Anthropic through the proxy handler.
// Requires ANTHROPIC_API_KEY env var.
func TestE2E_Anthropic(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "e2e-test",
		AccountID: 1,
		Nonce:     "e2e-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"anthropic": {Value: apiKey, CredentialType: "api_key"},
		},
	})

	proxy := newTestProxy(initProviders(), metaSrv)

	body := map[string]interface{}{
		"model":      "claude-haiku-4-5-20251001",
		"max_tokens": 16,
		"messages": []map[string]string{
			{"role": "user", "content": "Say OK"},
		},
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "e2e-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Anthropic call: got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Type  string `json:"type"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	respBody, _ := io.ReadAll(rr.Body)
	if err := json.Unmarshal(respBody, &resp); err != nil {
		t.Fatalf("failed to parse response: %v; body: %s", err, string(respBody))
	}

	if resp.Type != "message" {
		t.Errorf("response type = %q, want %q", resp.Type, "message")
	}
	if resp.Usage.InputTokens == 0 {
		t.Error("expected non-zero input_tokens")
	}
	if resp.Usage.OutputTokens == 0 {
		t.Error("expected non-zero output_tokens")
	}

	// Verify usage tracker recorded the call
	records := proxy.usage.GetRecords("e2e-test")
	if len(records) == 0 {
		t.Error("usage tracker has no records for e2e-test")
	} else {
		rec := records[0]
		if rec.Provider != "anthropic" {
			t.Errorf("usage record provider = %q, want %q", rec.Provider, "anthropic")
		}
		if rec.InputTokens == 0 {
			t.Error("usage record input_tokens should be > 0")
		}
	}

	t.Logf("Anthropic E2E: status=%d, input_tokens=%d, output_tokens=%d",
		rr.Code, resp.Usage.InputTokens, resp.Usage.OutputTokens)
}

// TestE2E_OpenAI makes a real API call to OpenAI through the proxy handler.
// Requires OPENAI_API_KEY env var.
func TestE2E_OpenAI(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "e2e-test-openai",
		AccountID: 1,
		Nonce:     "e2e-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"openai": {Value: apiKey, CredentialType: "api_key"},
		},
	})

	proxy := newTestProxy(initProviders(), metaSrv)

	body := map[string]interface{}{
		"model":      "gpt-4o-mini",
		"max_tokens": 16,
		"messages": []map[string]string{
			{"role": "user", "content": "Say OK"},
		},
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer e2e-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("OpenAI call: got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	respBody, _ := io.ReadAll(rr.Body)
	if err := json.Unmarshal(respBody, &resp); err != nil {
		t.Fatalf("failed to parse response: %v; body: %s", err, string(respBody))
	}

	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice in response")
	}
	if resp.Usage.PromptTokens == 0 {
		t.Error("expected non-zero prompt_tokens")
	}

	// Verify usage tracker recorded the call
	records := proxy.usage.GetRecords("e2e-test-openai")
	if len(records) == 0 {
		t.Error("usage tracker has no records for e2e-test-openai")
	}

	t.Logf("OpenAI E2E: status=%d, prompt_tokens=%d, completion_tokens=%d",
		rr.Code, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
}

// TestE2E_AnthropicSubscriptionKey verifies that subscription_key credentials
// use Bearer auth and identity injection through the full proxy handler flow.
func TestE2E_AnthropicSubscriptionKey(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_SUBSCRIPTION_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_SUBSCRIPTION_KEY not set")
	}

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "e2e-test-sub",
		AccountID: 1,
		Nonce:     "e2e-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"anthropic": {Value: apiKey, CredentialType: "subscription_key"},
		},
	})

	proxy := newTestProxy(initProviders(), metaSrv)

	// Test all subscription models — the identity injection is required for Opus/Sonnet
	models := []string{"claude-haiku-4-5-20251001", "claude-sonnet-4-6", "claude-opus-4-6"}
	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			body := map[string]interface{}{
				"model":      model,
				"max_tokens": 16,
				"messages": []map[string]string{
					{"role": "user", "content": "Say OK"},
				},
			}
			bodyBytes, _ := json.Marshal(body)

			req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("x-api-key", "e2e-nonce")
			req.RemoteAddr = "127.0.0.1:12345"

			rr := httptest.NewRecorder()
			proxy.handleProxy(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("subscription_key %s: got status %d, want 200; body: %s", model, rr.Code, rr.Body.String())
			}

			t.Logf("subscription_key %s: status=%d", model, rr.Code)
		})
	}
}

// TestE2E_Google makes a real API call to Google Gemini through the proxy handler.
// Requires GOOGLE_API_KEY env var.
func TestE2E_Google(t *testing.T) {
	apiKey := os.Getenv("GOOGLE_API_KEY")
	if apiKey == "" {
		t.Skip("GOOGLE_API_KEY not set")
	}

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "e2e-test-google",
		AccountID: 1,
		Nonce:     "e2e-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"google": {Value: apiKey, CredentialType: "api_key"},
		},
	})

	proxy := newTestProxy(initProviders(), metaSrv)

	// Use OpenAI-compatible format — the gateway sends openai-completions requests
	// to the proxy, which forwards to Google's /v1beta/openai/ endpoint.
	body := map[string]interface{}{
		"model": "gemini-2.5-flash",
		"messages": []map[string]string{
			{"role": "user", "content": "Say OK"},
		},
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	// Path: /google/chat/completions — no /v1 prefix since Google's OpenAI
	// endpoint is at /v1beta/openai/chat/completions (the proxy prepends
	// /v1beta/openai via UpstreamPathPrefix).
	req := httptest.NewRequest(http.MethodPost, "/google/chat/completions", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "e2e-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Google call: got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Response is in OpenAI format from Google's compatibility endpoint
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	respBody, _ := io.ReadAll(rr.Body)
	if err := json.Unmarshal(respBody, &resp); err != nil {
		t.Fatalf("failed to parse response: %v; body: %s", err, string(respBody))
	}

	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice in response")
	}
	if resp.Usage.PromptTokens == 0 {
		t.Error("expected non-zero prompt_tokens")
	}

	// Verify usage tracker recorded the call
	records := proxy.usage.GetRecords("e2e-test-google")
	if len(records) == 0 {
		t.Error("usage tracker has no records for e2e-test-google")
	}

	t.Logf("Google E2E: status=%d, prompt_tokens=%d, completion_tokens=%d",
		rr.Code, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
}

// fetchNebiusKey retrieves the Nebius API key from GCP Secret Manager.
// Fails the test if the key cannot be fetched — no silent fallbacks.
func fetchNebiusKey(t *testing.T) string {
	t.Helper()
	const secretName = "projects/example-project/secrets/NEBIUS_API_KEY/versions/latest"
	key, err := secrets.FetchSecret(context.Background(), secretName)
	if err != nil {
		t.Fatalf("failed to fetch NEBIUS_API_KEY from Secret Manager: %v", err)
	}
	return key
}

// TestE2E_NebiusPlatformTiers tests all 3 platform tier models through the proxy
// using the Nebius Token Factory API key from GCP Secret Manager.
func TestE2E_NebiusPlatformTiers(t *testing.T) {
	apiKey := fetchNebiusKey(t)

	tiers := []struct {
		name      string
		modelID   string
		maxTokens int
	}{
		{"Smart_Qwen3.5-397B", "Qwen/Qwen3.5-397B-A17B", 4000},
		{"Balanced_MiniMax-M2.5", "MiniMaxAI/MiniMax-M2.5", 4000},
		{"Fast_gpt-oss-120b", "openai/gpt-oss-120b", 200},
	}

	for _, tier := range tiers {
		t.Run(tier.name, func(t *testing.T) {
			metaSrv := metadata.New("127.0.0.1", 0)
			metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
				MachineID: "e2e-nebius-" + tier.name,
				AccountID: 1,
				Nonce:     "e2e-nonce",
				LLMKeys: map[string]metadata.CredentialEntry{
					"nebius": {Value: apiKey, CredentialType: "api_key"},
				},
			})

			proxy := newTestProxy(initProviders(), metaSrv)

			body := map[string]interface{}{
				"model":      tier.modelID,
				"max_tokens": tier.maxTokens,
				"messages": []map[string]string{
					{"role": "user", "content": "Say hello in one sentence."},
				},
			}
			bodyBytes, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}

			req := httptest.NewRequest(http.MethodPost, "/nebius/v1/chat/completions", bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer e2e-nonce")
			req.RemoteAddr = "127.0.0.1:12345"

			rr := httptest.NewRecorder()
			proxy.handleProxy(rr, req)

			respBody, _ := io.ReadAll(rr.Body)

			if rr.Code != http.StatusOK {
				t.Fatalf("Nebius %s: got status %d, want 200; body: %s", tier.name, rr.Code, string(respBody))
			}

			var resp struct {
				Model   string `json:"model"`
				Choices []struct {
					Message struct {
						Content          string `json:"content"`
						ReasoningContent string `json:"reasoning_content"`
						Reasoning        string `json:"reasoning"`
					} `json:"message"`
				} `json:"choices"`
				Usage struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(respBody, &resp); err != nil {
				t.Fatalf("failed to parse response: %v; body: %s", err, string(respBody))
			}

			if len(resp.Choices) == 0 {
				t.Fatal("expected at least one choice")
			}
			content := resp.Choices[0].Message.Content
			if content == "" {
				content = resp.Choices[0].Message.ReasoningContent
			}
			if content == "" {
				content = resp.Choices[0].Message.Reasoning
			}
			if content == "" {
				t.Error("expected non-empty content, reasoning_content, or reasoning")
			}
			if resp.Usage.PromptTokens == 0 {
				t.Error("expected non-zero prompt_tokens")
			}
			if resp.Usage.CompletionTokens == 0 {
				t.Error("expected non-zero completion_tokens")
			}

			// Verify usage tracker recorded the call
			records := proxy.usage.GetRecords("e2e-nebius-" + tier.name)
			if len(records) == 0 {
				t.Error("usage tracker has no records")
			} else {
				rec := records[0]
				if rec.Provider != "nebius" {
					t.Errorf("usage record provider = %q, want %q", rec.Provider, "nebius")
				}
				if rec.InputTokens == 0 || rec.OutputTokens == 0 {
					t.Error("usage record should include input and output tokens")
				}
				t.Logf("usage: provider=%s model=%s input=%d output=%d",
					rec.Provider, rec.Model, rec.InputTokens, rec.OutputTokens)
			}

			t.Logf("Nebius %s: model=%s prompt=%d completion=%d content=%q",
				tier.name, resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, truncateStr(content, 100))
		})
	}
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// fetchExaKey retrieves the Exa Search API key from GCP Secret Manager.
func fetchExaKey(t *testing.T) string {
	t.Helper()
	const secretName = "projects/example-project/secrets/EXA_SEARCH/versions/latest"
	key, err := secrets.FetchSecret(context.Background(), secretName)
	if err != nil {
		t.Fatalf("failed to fetch EXA_SEARCH from Secret Manager: %v", err)
	}
	return key
}

// TestE2E_ExaSearch makes a real search call to Exa through the proxy handler.
// Verifies the full chain: proxy auth → key injection → upstream request → results.
// Requires GCP Secret Manager access for EXA_SEARCH secret.
func TestE2E_ExaSearch(t *testing.T) {
	apiKey := fetchExaKey(t)

	// Register Exa as a custom provider (same as how runtime.go adds search providers)
	exaProvider := CustomProviderToProxy(metadata.CustomProviderConfig{
		Name:         "exa",
		UpstreamHost: "api.exa.ai",
		Scheme:       "https",
		AuthMethod:   "api_key_header",
		AuthHeader:   "x-api-key",
	})

	providers := initProviders()
	providers["exa"] = exaProvider

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "e2e-exa-search",
		AccountID: 1,
		Nonce:     "e2e-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"exa": {Value: apiKey, CredentialType: "api_key"},
		},
	})

	proxy := newTestProxy(providers, metaSrv)

	// Exa search API: POST /search with JSON body
	body := map[string]interface{}{
		"query":      "OpenAI latest news",
		"numResults": 3,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/exa/search", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "e2e-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Exa search: got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	respBody, _ := io.ReadAll(rr.Body)

	var resp struct {
		Results []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
			Text  string `json:"text"`
		} `json:"results"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		t.Fatalf("failed to parse Exa response: %v; body: %s", err, truncateStr(string(respBody), 500))
	}

	if len(resp.Results) == 0 {
		t.Fatal("Exa search returned 0 results")
	}

	for i, r := range resp.Results {
		if r.URL == "" {
			t.Errorf("result[%d] has empty URL", i)
		}
		t.Logf("Exa result[%d]: title=%q url=%s", i, truncateStr(r.Title, 60), r.URL)
	}

	t.Logf("Exa E2E: status=%d, results=%d", rr.Code, len(resp.Results))
}
