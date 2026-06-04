//go:build e2e

package apiproxy

// Credential Redesign E2E Tests
//
// Interactive test (runs full OAuth flow):
//
//   cd backend && go test -tags e2e -run TestE2E_CodexOAuth -v -timeout 5m ./internal/apiproxy/
//
//   The test will:
//   1. Generate a PKCE challenge and print an OpenAI auth URL
//   2. Start a local callback server on :1455
//   3. You open the URL in your browser and complete the OpenAI login
//   4. The callback server catches the redirect automatically
//   5. The test exchanges the code for OAuth tokens
//   6. The test runs the proxy with the real tokens
//
// Mock-based tests (no user interaction):
//
//   cd backend && go test -tags e2e -run "TestE2E_CodexOAuth_(PostflightRetry|PermanentRefresh|BrokenPassthrough)" -v ./internal/apiproxy/

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/metadata"
)

const (
	codexClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexRedirectURI = "http://localhost:1455/auth/callback"
	codexAuthURL     = "https://auth.openai.com/oauth/authorize"
	codexTokenURL    = "https://auth.openai.com/oauth/token"
)

// codexOAuthResult holds tokens from the interactive OAuth flow.
type codexOAuthResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
}

// runCodexOAuthFlow runs the full interactive PKCE OAuth flow:
// generates auth URL, starts callback server, waits for user, exchanges code.
func runCodexOAuthFlow(t *testing.T) *codexOAuthResult {
	t.Helper()

	// 1. Generate PKCE verifier + challenge
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		t.Fatalf("generating PKCE verifier: %v", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])

	// 2. Generate state
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		t.Fatalf("generating state: %v", err)
	}
	state := fmt.Sprintf("%x", stateBytes)

	// 3. Build auth URL
	params := url.Values{
		"response_type":              {"code"},
		"client_id":                  {codexClientID},
		"redirect_uri":               {codexRedirectURI},
		"scope":                      {"openid profile email offline_access"},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"state":                      {state},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"originator":                 {"pi"},
	}
	authURL := codexAuthURL + "?" + params.Encode()

	// 4. Start callback server on :1455
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		gotState := r.URL.Query().Get("state")
		if gotState != state {
			errCh <- fmt.Errorf("state mismatch: got %q, want %q", gotState, state)
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- fmt.Errorf("no code in callback: %s", r.URL.String())
			http.Error(w, "no code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><h2>✓ OAuth callback received!</h2><p>You can close this tab. The test is continuing...</p></body></html>`)
		codeCh <- code
	})

	listener, err := net.Listen("tcp", ":1455")
	if err != nil {
		t.Fatalf("failed to listen on :1455 (is something else using it?): %v", err)
	}

	srv := &http.Server{Handler: mux}
	go srv.Serve(listener)
	defer srv.Shutdown(context.Background())

	// 5. Prompt user
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "╔═══════════════════════════════════════════════════════════════╗")
	fmt.Fprintln(os.Stderr, "║  OpenAI Codex OAuth — Interactive E2E Test                    ║")
	fmt.Fprintln(os.Stderr, "║                                                               ║")
	fmt.Fprintln(os.Stderr, "║  Open this URL in your browser and log in to OpenAI:          ║")
	fmt.Fprintln(os.Stderr, "╚═══════════════════════════════════════════════════════════════╝")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, authURL)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Waiting for callback on http://localhost:1455/auth/callback ...")
	fmt.Fprintln(os.Stderr, "")

	// 6. Wait for callback (up to 3 minutes)
	var code string
	select {
	case code = <-codeCh:
		t.Log("OAuth callback received, exchanging code for tokens...")
	case err := <-errCh:
		t.Fatalf("callback error: %v", err)
	case <-time.After(3 * time.Minute):
		t.Fatal("timed out waiting for OAuth callback (3 minutes)")
	}

	// 7. Exchange code for tokens
	postData := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {codexRedirectURI},
		"client_id":     {codexClientID},
		"code_verifier": {verifier},
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.PostForm(codexTokenURL, postData)
	if err != nil {
		t.Fatalf("token exchange request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		t.Fatalf("reading token response: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("token exchange failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokens); err != nil {
		t.Fatalf("parsing token response: %v", err)
	}
	if tokens.AccessToken == "" {
		t.Fatalf("no access_token in response: %s", string(body))
	}
	if tokens.RefreshToken == "" {
		t.Fatalf("no refresh_token in response: %s", string(body))
	}

	t.Logf("OAuth tokens acquired: access_token=%s..., refresh_token=%s..., expires_in=%ds",
		tokens.AccessToken[:min(20, len(tokens.AccessToken))],
		tokens.RefreshToken[:min(10, len(tokens.RefreshToken))],
		tokens.ExpiresIn)

	return &codexOAuthResult{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresIn:    tokens.ExpiresIn,
	}
}

// ---------------------------------------------------------------------------
// Test 1: Full interactive OAuth → proxy test (the core redesign validation)
// ---------------------------------------------------------------------------
// Runs the PKCE flow, gets real tokens, then sends a request through the
// proxy with nonce-based auth + injected OAuth token.

func TestE2E_CodexOAuth_Interactive(t *testing.T) {
	if os.Getenv("E2E_CODEX_INTERACTIVE") == "" && os.Getenv("E2E_OPENAI_CODEX_TOKEN") == "" {
		t.Skip("Set E2E_CODEX_INTERACTIVE=1 to run the interactive OAuth test, or E2E_OPENAI_CODEX_TOKEN=<token> to skip the login")
	}

	var oauthToken string
	var refreshToken string
	var expiresIn int

	if token := os.Getenv("E2E_OPENAI_CODEX_TOKEN"); token != "" {
		// Fast path: token provided directly
		oauthToken = token
		expiresIn = 3600
		t.Log("Using token from E2E_OPENAI_CODEX_TOKEN env var")
	} else {
		// Interactive path: full PKCE flow
		result := runCodexOAuthFlow(t)
		oauthToken = result.AccessToken
		refreshToken = result.RefreshToken
		expiresIn = result.ExpiresIn
	}

	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	// --- Sub-test A: nonce-based auth (the fix) ---
	t.Run("NonceBased", func(t *testing.T) {
		metaSrv := metadata.New("127.0.0.1", 0)
		metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
			MachineID: "e2e-codex-oauth",
			AccountID: 1,
			Nonce:     "e2e-codex-nonce",
			LLMKeys: map[string]metadata.CredentialEntry{
				"openai-codex": {
					Value:          oauthToken,
					CredentialType: "oauth",
					ExpiresAt:      &expiresAt,
				},
			},
		})

		providers := initProviders()
		providers["openai-codex"] = redesignedCodexProvider()
		proxy := newTestProxy(providers, metaSrv)

		body := map[string]interface{}{
			"model":      "gpt-4o-mini",
			"max_tokens": 1,
			"messages":   []map[string]string{{"role": "user", "content": "Say OK"}},
		}
		bodyBytes, _ := json.Marshal(body)

		req := httptest.NewRequest(http.MethodPost, "/openai-codex/v1/chat/completions", bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", "e2e-codex-nonce") // nonce, NOT the OAuth token
		req.RemoteAddr = "127.0.0.1:12345"

		rr := httptest.NewRecorder()
		proxy.handleProxy(rr, req)

		respBody, _ := io.ReadAll(rr.Body)

		if rr.Code != http.StatusOK {
			t.Fatalf("nonce-based: status %d, want 200\nbody: %s", rr.Code, string(respBody))
		}

		var resp struct {
			Choices []struct {
				Message struct{ Content string } `json:"message"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(respBody, &resp); err != nil {
			t.Fatalf("parse: %v; body: %s", err, string(respBody))
		}
		if len(resp.Choices) == 0 {
			t.Fatal("expected at least one choice")
		}

		records := proxy.usage.GetRecords("e2e-codex-oauth")
		if len(records) == 0 {
			t.Error("no usage records")
		} else if records[0].Provider != "openai-codex" {
			t.Errorf("usage provider = %q, want %q", records[0].Provider, "openai-codex")
		}

		t.Logf("PASS: nonce-based OAuth → status=%d, prompt=%d, completion=%d",
			rr.Code, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	})

	// --- Sub-test B: verify the OLD broken passthrough still fails ---
	t.Run("BrokenPassthrough", func(t *testing.T) {
		metaSrv := metadata.New("127.0.0.1", 0)
		metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
			MachineID: "e2e-codex-broken",
			AccountID: 1,
			Nonce:     "the-real-nonce",
			LLMKeys:   map[string]metadata.CredentialEntry{},
		})

		providers := initProviders() // current (unfixed) providers
		proxy := newTestProxy(providers, metaSrv)

		body := map[string]interface{}{
			"model":      "gpt-4o-mini",
			"max_tokens": 1,
			"messages":   []map[string]string{{"role": "user", "content": "Say OK"}},
		}
		bodyBytes, _ := json.Marshal(body)

		req := httptest.NewRequest(http.MethodPost, "/openai-codex/v1/chat/completions", bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+oauthToken) // sending real token as Bearer (the bug)
		req.RemoteAddr = "127.0.0.1:12345"

		rr := httptest.NewRecorder()
		proxy.handleProxy(rr, req)

		// This should fail — token != nonce
		if rr.Code == http.StatusOK {
			t.Error("broken passthrough returned 200 — the bug is gone? Check if openai-codex provider changed.")
		}
		t.Logf("PASS: broken passthrough confirmed → status=%d (not 200)", rr.Code)
	})

	// --- Sub-test C: direct API call (sanity check that the token works) ---
	t.Run("DirectAPICall", func(t *testing.T) {
		body := map[string]interface{}{
			"model":      "gpt-4o-mini",
			"max_tokens": 1,
			"messages":   []map[string]string{{"role": "user", "content": "Say OK"}},
		}
		bodyBytes, _ := json.Marshal(body)

		req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+oauthToken)

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("direct API call failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		respBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("direct API call: status %d (token may be expired)\nbody: %s", resp.StatusCode, string(respBody))
		}
		t.Logf("PASS: direct API call → status=%d (token is valid)", resp.StatusCode)
	})

	// --- Sub-test D: token refresh via real OpenAI endpoint ---
	if refreshToken != "" {
		t.Run("TokenRefresh", func(t *testing.T) {
			postData := url.Values{
				"grant_type":    {"refresh_token"},
				"refresh_token": {refreshToken},
				"client_id":     {codexClientID},
			}

			client := &http.Client{Timeout: 15 * time.Second}
			resp, err := client.PostForm(codexTokenURL, postData)
			if err != nil {
				t.Fatalf("refresh request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				t.Fatalf("refresh failed (HTTP %d): %s", resp.StatusCode, string(body))
			}

			var tokens struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
				ExpiresIn    int    `json:"expires_in"`
			}
			if err := json.Unmarshal(body, &tokens); err != nil {
				t.Fatalf("parse refresh response: %v", err)
			}
			if tokens.AccessToken == "" {
				t.Fatal("no access_token in refresh response")
			}

			t.Logf("PASS: token refresh → new access_token=%s..., expires_in=%ds",
				tokens.AccessToken[:min(20, len(tokens.AccessToken))], tokens.ExpiresIn)
		})
	}
}

// ---------------------------------------------------------------------------
// Test 2: Codex OAuth — post-flight 401 retry (mock-based, no interaction)
// ---------------------------------------------------------------------------

func TestE2E_CodexOAuth_PostflightRetry(t *testing.T) {
	var callCount atomic.Int32
	mockUpstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"message": "Incorrect API key provided", "type": "invalid_request_error"},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{{"message": map[string]string{"content": "OK"}}},
			"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 1},
		})
	}))
	defer mockUpstream.Close()

	var refreshCalled atomic.Int32
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalled.Add(1)
		newExpiry := time.Now().Add(1 * time.Hour)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "refreshed-token-xxx", "credential_type": "oauth",
			"expires_at": newExpiry.Format(time.RFC3339),
		})
	}))
	defer refreshServer.Close()

	expiresAt := time.Now().Add(30 * time.Minute)
	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "e2e-codex-retry", AccountID: 1, Nonce: "e2e-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"openai-codex": {Value: "stale-token", CredentialType: "oauth", ExpiresAt: &expiresAt},
		},
	})

	codexProvider := redesignedCodexProvider()
	codexProvider.UpstreamHost = mockUpstream.Listener.Addr().String()
	codexProvider.Scheme = "https"
	codexProvider.AllowedHosts = []string{mockUpstream.Listener.Addr().String()}

	providers := map[string]*Provider{"openai-codex": codexProvider}
	proxy := newTestProxy(providers, metaSrv)
	proxy.refreshURL = refreshServer.URL
	proxy.client = mockUpstream.Client()

	bodyBytes, _ := json.Marshal(map[string]interface{}{
		"model": "gpt-4o-mini", "max_tokens": 1,
		"messages": []map[string]string{{"role": "user", "content": "Say OK"}},
	})

	req := httptest.NewRequest(http.MethodPost, "/openai-codex/v1/chat/completions", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "e2e-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)
	respBody, _ := io.ReadAll(rr.Body)

	if rr.Code != http.StatusOK {
		t.Fatalf("post-flight retry: status %d, want 200\nbody: %s", rr.Code, string(respBody))
	}
	if callCount.Load() != 2 {
		t.Errorf("expected 2 upstream calls (401 + retry), got %d", callCount.Load())
	}
	if refreshCalled.Load() != 1 {
		t.Errorf("expected 1 refresh call, got %d", refreshCalled.Load())
	}
	t.Logf("PASS: post-flight retry → status=%d, upstream=%d, refresh=%d", rr.Code, callCount.Load(), refreshCalled.Load())
}

// ---------------------------------------------------------------------------
// Test 3: Codex OAuth — permanent refresh failure (mock-based, no interaction)
// ---------------------------------------------------------------------------

func TestE2E_CodexOAuth_PermanentRefreshFailure(t *testing.T) {
	var callCount atomic.Int32
	mockUpstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": "invalid token"}})
	}))
	defer mockUpstream.Close()

	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "oauth_reauth_required", "permanent": true, "detail": "invalid_grant",
		})
	}))
	defer refreshServer.Close()

	expiresAt := time.Now().Add(30 * time.Minute)
	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "e2e-codex-perm-fail", AccountID: 1, Nonce: "e2e-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"openai-codex": {Value: "dead-token", CredentialType: "oauth", ExpiresAt: &expiresAt},
		},
	})

	codexProvider := redesignedCodexProvider()
	codexProvider.UpstreamHost = mockUpstream.Listener.Addr().String()
	codexProvider.Scheme = "https"
	codexProvider.AllowedHosts = []string{mockUpstream.Listener.Addr().String()}

	providers := map[string]*Provider{"openai-codex": codexProvider}
	proxy := newTestProxy(providers, metaSrv)
	proxy.refreshURL = refreshServer.URL
	proxy.client = mockUpstream.Client()

	bodyBytes, _ := json.Marshal(map[string]interface{}{
		"model": "gpt-4o-mini", "max_tokens": 1,
		"messages": []map[string]string{{"role": "user", "content": "Say OK"}},
	})

	req := httptest.NewRequest(http.MethodPost, "/openai-codex/v1/chat/completions", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "e2e-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)
	respBody, _ := io.ReadAll(rr.Body)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("permanent failure: status %d, want 502\nbody: %s", rr.Code, string(respBody))
	}

	var errResp map[string]interface{}
	if err := json.Unmarshal(respBody, &errResp); err != nil {
		t.Fatalf("parse error: %v; body: %s", err, string(respBody))
	}
	if errMsg, _ := errResp["error"].(string); errMsg != "oauth_reauth_required" {
		t.Errorf("error = %q, want %q", errMsg, "oauth_reauth_required")
	}
	if callCount.Load() != 1 {
		t.Errorf("expected 1 upstream call (no retry after permanent failure), got %d", callCount.Load())
	}
	t.Logf("PASS: permanent failure → status=%d", rr.Code)
}

// ---------------------------------------------------------------------------
// Test 4: Bug regression — OAuth token as Bearer (the broken flow)
// ---------------------------------------------------------------------------

func TestE2E_CodexOAuth_BrokenPassthrough(t *testing.T) {
	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "e2e-codex-broken", AccountID: 1, Nonce: "the-real-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{},
	})

	providers := initProviders()
	proxy := newTestProxy(providers, metaSrv)

	bodyBytes, _ := json.Marshal(map[string]interface{}{
		"model": "gpt-4o-mini", "max_tokens": 1,
		"messages": []map[string]string{{"role": "user", "content": "Say OK"}},
	})

	req := httptest.NewRequest(http.MethodPost, "/openai-codex/v1/chat/completions", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer some-oauth-token-not-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code == http.StatusOK {
		t.Error("broken passthrough returned 200 — shouldn't happen with fake token")
	}
	t.Logf("PASS: broken passthrough → status=%d (confirms bug: token != nonce → rejected)", rr.Code)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func redesignedCodexProvider() *Provider {
	base := openaiCodexProvider()
	return &Provider{
		Name:         "openai-codex",
		UpstreamHost: base.UpstreamHost,
		Scheme:       "https",
		PathPrefix:   base.PathPrefix,
		AllowedHosts: base.AllowedHosts,
		KeyOptional:  false, // CHANGED: require key from metadata
		InjectKey: func(req *http.Request, key, credType string) {
			req.Header.Set("Authorization", "Bearer "+key)
		},
		ExtractToken: func(req *http.Request) string {
			return req.Header.Get("x-api-key") // nonce, like every other provider
		},
		ParseJSONUsage: base.ParseJSONUsage,
		ParseSSEEvent:  base.ParseSSEEvent,
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Suppress the init() banner — only used for the old env-var approach.
// The interactive test prints its own instructions.
var _ = strings.TrimSpace
