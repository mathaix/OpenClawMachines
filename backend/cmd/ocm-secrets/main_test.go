package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// --- Test helpers ---

func writeNonceFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "nonce")
	if err := os.WriteFile(path, []byte(content), 0444); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeGatewayIPFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway-ip")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func makeRequest(ids ...string) string {
	req := execRequest{ProtocolVersion: 1, Provider: "ocm", IDs: ids}
	b, _ := json.Marshal(req)
	return string(b)
}

// mockFetcher implements secretFetcher for unit tests.
type mockFetcher struct {
	secrets map[string]string
	err     error
	called  bool
	gotIP   string
	gotAuth string
}

func (m *mockFetcher) FetchSecrets(gatewayIP, nonce string) (map[string]string, error) {
	m.called = true
	m.gotIP = gatewayIP
	m.gotAuth = nonce
	if m.err != nil {
		return nil, m.err
	}
	return m.secrets, nil
}

// parseResponse decodes the JSON exec protocol response from stdout.
func parseResponse(t *testing.T, stdout *bytes.Buffer) execResponse {
	t.Helper()
	var resp execResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, stdout.String())
	}
	return resp
}

// assertValue checks that a specific key in the response has the expected string value.
func assertValue(t *testing.T, resp execResponse, key, want string) {
	t.Helper()
	got, ok := resp.Values[key]
	if !ok {
		t.Errorf("values[%q] missing, want %q", key, want)
		return
	}
	if got != want {
		t.Errorf("values[%q] = %v, want %q", key, got, want)
	}
}

// assertError checks that a specific key has an error in the response.
func assertError(t *testing.T, resp execResponse, key string) {
	t.Helper()
	if resp.Errors == nil {
		t.Errorf("errors map is nil, expected error for %q", key)
		return
	}
	if _, ok := resp.Errors[key]; !ok {
		t.Errorf("errors[%q] missing, expected an error", key)
	}
}

// assertNoError checks that a specific key does NOT have an error.
func assertNoError(t *testing.T, resp execResponse, key string) {
	t.Helper()
	if resp.Errors != nil {
		if _, ok := resp.Errors[key]; ok {
			t.Errorf("errors[%q] = %v, expected no error", key, resp.Errors[key])
		}
	}
}

// assertNoValue checks that a specific key is NOT in the values map.
func assertNoValue(t *testing.T, resp execResponse, key string) {
	t.Helper()
	if v, ok := resp.Values[key]; ok {
		t.Errorf("values[%q] = %v, expected it to be absent", key, v)
	}
}

// --- Proxy key tests ---

func TestProxyKeys_ReturnNonce(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
	}{
		{"anthropic", []string{"anthropic-key"}},
		{"openai", []string{"openai-key"}},
		{"nebius", []string{"nebius-key"}},
		{"google", []string{"google-key"}},
		{"openrouter", []string{"openrouter-key"}},
		{"brave (convention)", []string{"brave-key"}},
		{"gemini-search (convention)", []string{"gemini-search-key"}},
		{"all proxy keys", []string{"anthropic-key", "openai-key", "nebius-key", "google-key", "openrouter-key", "brave-key"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			noncePath := writeNonceFile(t, "test-nonce-123")
			fetcher := &mockFetcher{}
			cfg := runConfig{
				noncePath:     noncePath,
				gatewayIPPath: filepath.Join(t.TempDir(), "no-such-file"),
				fetcher:       fetcher,
			}

			stdin := bytes.NewBufferString(makeRequest(tt.ids...))
			var stdout bytes.Buffer
			exitCode := run(stdin, &stdout, cfg)

			if exitCode != 0 {
				t.Fatalf("exit code = %d, want 0", exitCode)
			}

			resp := parseResponse(t, &stdout)
			for _, id := range tt.ids {
				assertValue(t, resp, id, "test-nonce-123")
			}

			// Proxy keys must NOT trigger a metadata fetch.
			if fetcher.called {
				t.Error("metadata fetcher was called for proxy keys — should not happen")
			}
		})
	}
}

func TestProxyKeys_NonceTrimmed(t *testing.T) {
	noncePath := writeNonceFile(t, "  nonce-with-spaces  \n")
	cfg := runConfig{
		noncePath:     noncePath,
		gatewayIPPath: filepath.Join(t.TempDir(), "no-such-file"),
		fetcher:       &mockFetcher{},
	}

	stdin := bytes.NewBufferString(makeRequest("anthropic-key"))
	var stdout bytes.Buffer
	exitCode := run(stdin, &stdout, cfg)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	resp := parseResponse(t, &stdout)
	assertValue(t, resp, "anthropic-key", "nonce-with-spaces")
}

// --- Convention-based proxy credential detection ---

func TestIsProxyCredential(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		// Existing LLM providers
		{"anthropic-key", true},
		{"openai-key", true},
		{"nebius-key", true},
		{"google-key", true},
		{"openrouter-key", true},
		// New providers work automatically via convention
		{"brave-key", true},
		{"gemini-search-key", true},
		{"perplexity-key", true},
		// Platform secrets never end in "-key"
		{"gateway-token", false},
		{"channel-telegram-botToken", false},
		{"channel-discord-token", false},
		// Channel IDs ending in -key must NOT match (channel- prefix excluded)
		{"channel-some-service-key", false},
		{"channel-custom-api-key", false},
		// Search plugin secrets must fetch the real API key from metadata.
		{"search-exa-api-key", false},
		{"search-gemini-api-key", false},
		// Edge cases
		{"-key", true}, // degenerate but matches convention
		{"key", false}, // no hyphen prefix
		{"", false},    // empty string
		{"my-api-token", false},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := isProxyCredential(tt.id); got != tt.want {
				t.Errorf("isProxyCredential(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

// --- Platform secret resolution ---

func TestPlatformSecrets_Resolved(t *testing.T) {
	noncePath := writeNonceFile(t, "valid-nonce")
	fetcher := &mockFetcher{
		secrets: map[string]string{
			"channel-telegram-botToken": "tg-token-123",
		},
	}
	cfg := runConfig{
		noncePath:     noncePath,
		gatewayIPPath: filepath.Join(t.TempDir(), "no-such-file"),
		fetcher:       fetcher,
	}

	stdin := bytes.NewBufferString(makeRequest("channel-telegram-botToken"))
	var stdout bytes.Buffer
	exitCode := run(stdin, &stdout, cfg)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}

	resp := parseResponse(t, &stdout)
	assertValue(t, resp, "channel-telegram-botToken", "tg-token-123")
	assertNoError(t, resp, "channel-telegram-botToken")

	if !fetcher.called {
		t.Error("metadata fetcher was not called — expected it to fetch platform secrets")
	}
}

func TestPlatformSecrets_MultipleFetched(t *testing.T) {
	noncePath := writeNonceFile(t, "auth-nonce")
	fetcher := &mockFetcher{
		secrets: map[string]string{
			"channel-telegram-botToken": "tg-token",
			"channel-discord-token":     "dc-token",
		},
	}
	cfg := runConfig{
		noncePath:     noncePath,
		gatewayIPPath: filepath.Join(t.TempDir(), "no-such-file"),
		fetcher:       fetcher,
	}

	stdin := bytes.NewBufferString(makeRequest("channel-telegram-botToken", "channel-discord-token"))
	var stdout bytes.Buffer
	exitCode := run(stdin, &stdout, cfg)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}

	resp := parseResponse(t, &stdout)
	assertValue(t, resp, "channel-telegram-botToken", "tg-token")
	assertValue(t, resp, "channel-discord-token", "dc-token")
}

func TestPlatformSecrets_MissingKeyReturnsError(t *testing.T) {
	noncePath := writeNonceFile(t, "auth-nonce")
	fetcher := &mockFetcher{
		secrets: map[string]string{"channel-telegram-botToken": "exists"},
	}
	cfg := runConfig{
		noncePath:     noncePath,
		gatewayIPPath: filepath.Join(t.TempDir(), "no-such-file"),
		fetcher:       fetcher,
	}

	stdin := bytes.NewBufferString(makeRequest("channel-telegram-botToken", "channel-nonexistent-token"))
	var stdout bytes.Buffer
	exitCode := run(stdin, &stdout, cfg)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}

	resp := parseResponse(t, &stdout)
	assertValue(t, resp, "channel-telegram-botToken", "exists")
	assertNoValue(t, resp, "channel-nonexistent-token")
	assertError(t, resp, "channel-nonexistent-token")
}

func TestPlatformSecrets_FetchErrorReturnsErrors(t *testing.T) {
	noncePath := writeNonceFile(t, "auth-nonce")
	fetcher := &mockFetcher{
		err: fmt.Errorf("connection refused"),
	}
	cfg := runConfig{
		noncePath:     noncePath,
		gatewayIPPath: filepath.Join(t.TempDir(), "no-such-file"),
		fetcher:       fetcher,
	}

	stdin := bytes.NewBufferString(makeRequest("channel-telegram-botToken"))
	var stdout bytes.Buffer
	exitCode := run(stdin, &stdout, cfg)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}

	resp := parseResponse(t, &stdout)
	assertNoValue(t, resp, "channel-telegram-botToken")
	assertError(t, resp, "channel-telegram-botToken")
}

func TestPlatformSecrets_PassesNonceToFetcher(t *testing.T) {
	noncePath := writeNonceFile(t, "my-nonce-value")
	fetcher := &mockFetcher{secrets: map[string]string{"channel-telegram-botToken": "val"}}
	ipPath := writeGatewayIPFile(t, "10.0.0.5")
	cfg := runConfig{
		noncePath:     noncePath,
		gatewayIPPath: ipPath,
		fetcher:       fetcher,
	}

	stdin := bytes.NewBufferString(makeRequest("channel-telegram-botToken"))
	var stdout bytes.Buffer
	run(stdin, &stdout, cfg)

	if fetcher.gotAuth != "my-nonce-value" {
		t.Errorf("fetcher received nonce %q, want %q", fetcher.gotAuth, "my-nonce-value")
	}
	if fetcher.gotIP != "10.0.0.5" {
		t.Errorf("fetcher received gateway IP %q, want %q", fetcher.gotIP, "10.0.0.5")
	}
}

// --- Mixed requests (proxy + platform in same call) ---

func TestMixed_BothSucceed(t *testing.T) {
	noncePath := writeNonceFile(t, "mix-nonce")
	fetcher := &mockFetcher{
		secrets: map[string]string{"channel-telegram-botToken": "real-tg-token"},
	}
	cfg := runConfig{
		noncePath:     noncePath,
		gatewayIPPath: filepath.Join(t.TempDir(), "no-such-file"),
		fetcher:       fetcher,
	}

	stdin := bytes.NewBufferString(makeRequest("anthropic-key", "openai-key", "channel-telegram-botToken"))
	var stdout bytes.Buffer
	exitCode := run(stdin, &stdout, cfg)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}

	resp := parseResponse(t, &stdout)
	assertValue(t, resp, "anthropic-key", "mix-nonce")
	assertValue(t, resp, "openai-key", "mix-nonce")
	assertValue(t, resp, "channel-telegram-botToken", "real-tg-token")
}

func TestMixed_ProxySucceedsPlatformFetchFails(t *testing.T) {
	noncePath := writeNonceFile(t, "mix-nonce")
	fetcher := &mockFetcher{err: fmt.Errorf("network error")}
	cfg := runConfig{
		noncePath:     noncePath,
		gatewayIPPath: filepath.Join(t.TempDir(), "no-such-file"),
		fetcher:       fetcher,
	}

	stdin := bytes.NewBufferString(makeRequest("anthropic-key", "channel-telegram-botToken"))
	var stdout bytes.Buffer
	exitCode := run(stdin, &stdout, cfg)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}

	resp := parseResponse(t, &stdout)
	// Proxy key still succeeds even when platform fetch fails.
	assertValue(t, resp, "anthropic-key", "mix-nonce")
	// Platform secret has error.
	assertNoValue(t, resp, "channel-telegram-botToken")
	assertError(t, resp, "channel-telegram-botToken")
}

func TestMixed_PartialPlatformSuccess(t *testing.T) {
	noncePath := writeNonceFile(t, "mix-nonce")
	fetcher := &mockFetcher{
		secrets: map[string]string{"channel-telegram-botToken": "found-it"},
		// channel-discord-token is not in the secrets map
	}
	cfg := runConfig{
		noncePath:     noncePath,
		gatewayIPPath: filepath.Join(t.TempDir(), "no-such-file"),
		fetcher:       fetcher,
	}

	stdin := bytes.NewBufferString(makeRequest("anthropic-key", "channel-telegram-botToken", "channel-discord-token"))
	var stdout bytes.Buffer
	exitCode := run(stdin, &stdout, cfg)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}

	resp := parseResponse(t, &stdout)
	assertValue(t, resp, "anthropic-key", "mix-nonce")
	assertValue(t, resp, "channel-telegram-botToken", "found-it")
	assertNoValue(t, resp, "channel-discord-token")
	assertError(t, resp, "channel-discord-token")
}

// --- Gateway IP resolution ---

func TestGatewayIP_FromFile(t *testing.T) {
	path := writeGatewayIPFile(t, "10.0.0.42\n")
	got := readGatewayIP(path)
	if got != "10.0.0.42" {
		t.Errorf("readGatewayIP() = %q, want %q", got, "10.0.0.42")
	}
}

func TestGatewayIP_DefaultWhenMissing(t *testing.T) {
	got := readGatewayIP(filepath.Join(t.TempDir(), "no-such-file"))
	if got != defaultGatewayIP {
		t.Errorf("readGatewayIP() = %q, want default %q", got, defaultGatewayIP)
	}
}

func TestGatewayIP_DefaultWhenEmpty(t *testing.T) {
	path := writeGatewayIPFile(t, "  \n")
	got := readGatewayIP(path)
	if got != defaultGatewayIP {
		t.Errorf("readGatewayIP() = %q, want default %q", got, defaultGatewayIP)
	}
}

// --- Input validation ---

func TestInput_InvalidJSON(t *testing.T) {
	noncePath := writeNonceFile(t, "nonce")
	cfg := runConfig{noncePath: noncePath, fetcher: &mockFetcher{}}
	stdin := bytes.NewBufferString("not json")
	var stdout bytes.Buffer
	if code := run(stdin, &stdout, cfg); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestInput_EmptyIDs(t *testing.T) {
	noncePath := writeNonceFile(t, "nonce")
	cfg := runConfig{noncePath: noncePath, fetcher: &mockFetcher{}}
	stdin := bytes.NewBufferString(`{"protocolVersion":1,"provider":"ocm","ids":[]}`)
	var stdout bytes.Buffer
	if code := run(stdin, &stdout, cfg); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestInput_NonceFileMissing(t *testing.T) {
	cfg := runConfig{
		noncePath: filepath.Join(t.TempDir(), "nonexistent"),
		fetcher:   &mockFetcher{},
	}
	stdin := bytes.NewBufferString(makeRequest("anthropic-key"))
	var stdout bytes.Buffer
	if code := run(stdin, &stdout, cfg); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestInput_NonceFileEmpty(t *testing.T) {
	noncePath := writeNonceFile(t, "")
	cfg := runConfig{noncePath: noncePath, fetcher: &mockFetcher{}}
	stdin := bytes.NewBufferString(makeRequest("anthropic-key"))
	var stdout bytes.Buffer
	if code := run(stdin, &stdout, cfg); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestInput_NonceFileWhitespaceOnly(t *testing.T) {
	noncePath := writeNonceFile(t, "  \n  ")
	cfg := runConfig{noncePath: noncePath, fetcher: &mockFetcher{}}
	stdin := bytes.NewBufferString(makeRequest("anthropic-key"))
	var stdout bytes.Buffer
	if code := run(stdin, &stdout, cfg); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

// --- Protocol compliance ---

func TestProtocol_ResponseHasProtocolVersion(t *testing.T) {
	noncePath := writeNonceFile(t, "nonce")
	cfg := runConfig{
		noncePath: noncePath,
		fetcher:   &mockFetcher{},
	}
	stdin := bytes.NewBufferString(makeRequest("anthropic-key"))
	var stdout bytes.Buffer
	run(stdin, &stdout, cfg)

	resp := parseResponse(t, &stdout)
	if resp.ProtocolVersion != 1 {
		t.Errorf("protocolVersion = %d, want 1", resp.ProtocolVersion)
	}
}

func TestProtocol_NoErrorsFieldWhenAllSucceed(t *testing.T) {
	noncePath := writeNonceFile(t, "nonce")
	cfg := runConfig{
		noncePath: noncePath,
		fetcher:   &mockFetcher{},
	}
	stdin := bytes.NewBufferString(makeRequest("anthropic-key"))
	var stdout bytes.Buffer
	run(stdin, &stdout, cfg)

	// Check raw JSON — errors field should be absent, not null or empty.
	var raw map[string]interface{}
	_ = json.Unmarshal(stdout.Bytes(), &raw)
	if _, exists := raw["errors"]; exists {
		t.Errorf("errors field should be omitted when all keys succeed, got: %v", raw["errors"])
	}
}

func TestProtocol_ErrorsFieldPresentOnFetchFailure(t *testing.T) {
	noncePath := writeNonceFile(t, "nonce")
	cfg := runConfig{
		noncePath: noncePath,
		fetcher:   &mockFetcher{err: fmt.Errorf("unreachable")},
	}
	stdin := bytes.NewBufferString(makeRequest("channel-telegram-botToken"))
	var stdout bytes.Buffer
	run(stdin, &stdout, cfg)

	var raw map[string]interface{}
	_ = json.Unmarshal(stdout.Bytes(), &raw)
	if _, exists := raw["errors"]; !exists {
		t.Error("errors field should be present when platform secret fetch fails")
	}
}

// --- HTTP fetcher integration tests (with httptest) ---

func TestHTTPFetcher_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/secrets" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("X-Metadata-Nonce"); got != "test-nonce" {
			t.Errorf("nonce header = %q, want %q", got, "test-nonce")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"channel-telegram-botToken": "real-key-from-metadata",
		})
	}))
	defer server.Close()

	// Extract host:port from test server URL (strip http://).
	addr := server.URL[len("http://"):]
	fetcher := &httpSecretFetcher{client: server.Client()}
	secrets, err := fetcher.FetchSecrets(addr, "test-nonce")
	if err != nil {
		t.Fatalf("FetchSecrets() error: %v", err)
	}
	if secrets["channel-telegram-botToken"] != "real-key-from-metadata" {
		t.Errorf("channel-telegram-botToken = %q, want %q", secrets["channel-telegram-botToken"], "real-key-from-metadata")
	}
}

func TestHTTPFetcher_Non200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer server.Close()

	addr := server.URL[len("http://"):]
	fetcher := &httpSecretFetcher{client: server.Client()}
	_, err := fetcher.FetchSecrets(addr, "nonce")
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

func TestHTTPFetcher_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	addr := server.URL[len("http://"):]
	fetcher := &httpSecretFetcher{client: server.Client()}
	_, err := fetcher.FetchSecrets(addr, "nonce")
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestHTTPFetcher_ConnectionRefused(t *testing.T) {
	fetcher := &httpSecretFetcher{client: http.DefaultClient}
	// Use a port that's almost certainly not listening.
	_, err := fetcher.FetchSecrets("127.0.0.1:1", "nonce")
	if err == nil {
		t.Fatal("expected error for connection refused")
	}
}
