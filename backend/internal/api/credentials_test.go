package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

func TestValidateTelegramKey_Valid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botVALID-TOKEN/getMe" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	old := telegramBaseURL
	telegramBaseURL = ts.URL
	defer func() { telegramBaseURL = old }()

	if err := validateTelegramKey(context.Background(), "VALID-TOKEN"); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestValidateTelegramKey_Invalid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	defer ts.Close()

	old := telegramBaseURL
	telegramBaseURL = ts.URL
	defer func() { telegramBaseURL = old }()

	err := validateTelegramKey(context.Background(), "BAD-TOKEN")
	if err == nil {
		t.Fatal("expected error for invalid token, got nil")
	}
	if err.Error() != "invalid bot token" {
		t.Fatalf("expected 'invalid bot token', got %q", err.Error())
	}
}

func TestValidateTelegramKey_RateLimited(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	old := telegramBaseURL
	telegramBaseURL = ts.URL
	defer func() { telegramBaseURL = old }()

	err := validateTelegramKey(context.Background(), "RATE-LIMITED-TOKEN")
	if err == nil {
		t.Fatal("expected error for rate-limited response, got nil")
	}
	if err.Error() != "rate limited by Telegram API, please try again later" {
		t.Fatalf("expected rate limit error, got %q", err.Error())
	}
}

func TestValidateWhatsAppKey_Valid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v18.0/me" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer VALID-TOKEN" {
			t.Errorf("unexpected Authorization header: %s", auth)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"123"}`))
	}))
	defer ts.Close()

	old := whatsappBaseURL
	whatsappBaseURL = ts.URL
	defer func() { whatsappBaseURL = old }()

	if err := validateWhatsAppKey(context.Background(), "VALID-TOKEN"); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestValidateWhatsAppKey_Unauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	old := whatsappBaseURL
	whatsappBaseURL = ts.URL
	defer func() { whatsappBaseURL = old }()

	err := validateWhatsAppKey(context.Background(), "BAD-TOKEN")
	if err == nil {
		t.Fatal("expected error for invalid token, got nil")
	}
	if err.Error() != "invalid access token" {
		t.Fatalf("expected 'invalid access token', got %q", err.Error())
	}
}

func TestValidateWhatsAppKey_BadRequest(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()

	old := whatsappBaseURL
	whatsappBaseURL = ts.URL
	defer func() { whatsappBaseURL = old }()

	err := validateWhatsAppKey(context.Background(), "MALFORMED-TOKEN")
	if err == nil {
		t.Fatal("expected error for bad request, got nil")
	}
	if err.Error() != "invalid access token" {
		t.Fatalf("expected 'invalid access token', got %q", err.Error())
	}
}

func TestValidateWhatsAppKey_RateLimited(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	old := whatsappBaseURL
	whatsappBaseURL = ts.URL
	defer func() { whatsappBaseURL = old }()

	if err := validateWhatsAppKey(context.Background(), "RATE-LIMITED-TOKEN"); err != nil {
		t.Fatalf("expected nil error for rate-limited (key is valid), got %v", err)
	}
}

func TestValidateProviderKey_TelegramRouted(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	old := telegramBaseURL
	telegramBaseURL = ts.URL
	defer func() { telegramBaseURL = old }()

	srv := &Server{}
	if err := srv.validateProviderKey(context.Background(), "telegram", "test-token"); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestValidateProviderKey_WhatsAppRouted(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	old := whatsappBaseURL
	whatsappBaseURL = ts.URL
	defer func() { whatsappBaseURL = old }()

	srv := &Server{}
	if err := srv.validateProviderKey(context.Background(), "whatsapp", "test-token"); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestValidProviders_IncludesTelegramAndWhatsApp(t *testing.T) {
	for _, provider := range []string{"telegram", "whatsapp"} {
		if !validProviders[provider] {
			t.Errorf("expected %q in validProviders", provider)
		}
	}
}

func TestValidateSlackBotToken_Valid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth.test" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer xoxb-valid-token" {
			t.Errorf("unexpected Authorization header: %s", auth)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"team":"Test Team"}`))
	}))
	defer ts.Close()

	old := slackBotBaseURL
	slackBotBaseURL = ts.URL
	defer func() { slackBotBaseURL = old }()

	if err := validateSlackBotToken(context.Background(), "xoxb-valid-token"); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestValidateSlackBotToken_Invalid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
	}))
	defer ts.Close()

	old := slackBotBaseURL
	slackBotBaseURL = ts.URL
	defer func() { slackBotBaseURL = old }()

	err := validateSlackBotToken(context.Background(), "xoxb-bad-token")
	if err == nil {
		t.Fatal("expected error for invalid token, got nil")
	}
	if err.Error() != "slack API rejected token: invalid_auth" {
		t.Fatalf("expected slack API error, got %q", err.Error())
	}
}

func TestValidateSlackBotToken_BadPrefix(t *testing.T) {
	err := validateSlackBotToken(context.Background(), "not-a-slack-token")
	if err == nil {
		t.Fatal("expected error for bad prefix")
	}
}

func TestValidateSlackAppToken_Valid(t *testing.T) {
	if err := validateSlackAppToken("xapp-1-valid-token"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestValidateSlackAppToken_BadPrefix(t *testing.T) {
	if err := validateSlackAppToken("not-xapp-token"); err == nil {
		t.Fatal("expected error for bad prefix")
	}
}

func TestValidProviders_IncludesSlack(t *testing.T) {
	for _, provider := range []string{"slack", "slack-app"} {
		if !validProviders[provider] {
			t.Errorf("expected %q in validProviders", provider)
		}
	}
}

// --- Catalog-based validation tests ---

// mockCatalogStore is a minimal mock that only implements GetProviderCatalogEntry.
type mockCatalogStore struct {
	store.Store // embed to satisfy interface; only GetProviderCatalogEntry is called
	entry       *store.ProviderCatalogEntry
	err         error
}

func (m *mockCatalogStore) GetProviderCatalogEntry(_ context.Context, _ string) (*store.ProviderCatalogEntry, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.entry, nil
}

func TestValidateFromCatalog(t *testing.T) {
	// catalogValidationSpec builds a JSON validation spec for use in test cases.
	catalogValidationSpec := func(url, method, authMethod, authHeader, authPrefix string, success, fail []int) json.RawMessage {
		spec := map[string]interface{}{
			"url":         url,
			"method":      method,
			"auth_method": authMethod,
		}
		if authHeader != "" {
			spec["auth_header"] = authHeader
		}
		if authPrefix != "" {
			spec["auth_prefix"] = authPrefix
		}
		if success != nil {
			spec["success"] = success
		}
		if fail != nil {
			spec["fail"] = fail
		}
		b, _ := json.Marshal(spec)
		return b
	}

	t.Run("no catalog entry returns nil", func(t *testing.T) {
		srv := &Server{store: &mockCatalogStore{err: nil, entry: nil}}
		err := srv.validateFromCatalog(context.Background(), "unknown-provider", "test-key")
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("catalog entry with store error returns nil", func(t *testing.T) {
		srv := &Server{store: &mockCatalogStore{err: context.DeadlineExceeded}}
		err := srv.validateFromCatalog(context.Background(), "broken-provider", "test-key")
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("catalog entry with nil validation returns nil", func(t *testing.T) {
		srv := &Server{store: &mockCatalogStore{
			entry: &store.ProviderCatalogEntry{
				ID:         "some-provider",
				Validation: nil,
			},
		}}
		err := srv.validateFromCatalog(context.Background(), "some-provider", "test-key")
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("SSRF guard blocks non-https URL", func(t *testing.T) {
		srv := &Server{store: &mockCatalogStore{
			entry: &store.ProviderCatalogEntry{
				ID: "evil-provider",
				Validation: catalogValidationSpec(
					"http://internal-service.local/validate",
					"GET", "bearer", "", "",
					[]int{200}, []int{401},
				),
			},
		}}
		err := srv.validateFromCatalog(context.Background(), "evil-provider", "test-key")
		if err != nil {
			t.Fatalf("expected nil (blocked silently), got %v", err)
		}
	})

	// Note: {{key}} URL substitution is tested as part of the "auth methods and
	// status codes" subtests below, which use a TLS test server with {{key}} in
	// the URL path and verify lastPath contains the substituted key value.

	// For HTTP-based tests, we need to trust the test server's TLS cert.
	// Create a single TLS test server for the remaining test cases.
	t.Run("auth methods and status codes", func(t *testing.T) {
		var (
			lastAuthHeader string
			lastQueryKey   string
			lastPath       string
		)
		responseStatus := http.StatusOK

		ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			lastAuthHeader = r.Header.Get("Authorization")
			lastQueryKey = r.URL.Query().Get("key")
			lastPath = r.URL.Path
			// Also capture custom header
			if v := r.Header.Get("x-api-key"); v != "" {
				lastAuthHeader = "x-api-key:" + v
			}
			if v := r.Header.Get("x-custom-key"); v != "" {
				lastAuthHeader = "x-custom-key:" + v
			}
			w.WriteHeader(responseStatus)
		}))
		defer ts.Close()

		// validateFromCatalog creates its own http.Client{} which uses
		// http.DefaultTransport. Override it with the TLS test server's transport
		// so the self-signed cert is trusted.
		origTransport := http.DefaultTransport
		http.DefaultTransport = ts.Client().Transport
		defer func() { http.DefaultTransport = origTransport }()

		tests := []struct {
			name           string
			authMethod     string
			authHeader     string
			authPrefix     string
			serverStatus   int
			successCodes   []int
			failCodes      []int
			wantErr        bool
			wantErrContain string
			checkAuth      func(t *testing.T)
		}{
			{
				name:         "header auth with default header (x-api-key)",
				authMethod:   "header",
				authHeader:   "",
				serverStatus: 200,
				successCodes: []int{200},
				checkAuth: func(t *testing.T) {
					if lastAuthHeader != "x-api-key:test-secret-key" {
						t.Errorf("expected x-api-key header with key, got auth=%q", lastAuthHeader)
					}
				},
			},
			{
				name:         "header auth with custom header",
				authMethod:   "header",
				authHeader:   "x-custom-key",
				serverStatus: 200,
				successCodes: []int{200},
				checkAuth: func(t *testing.T) {
					if lastAuthHeader != "x-custom-key:test-secret-key" {
						t.Errorf("expected x-custom-key header with key, got auth=%q", lastAuthHeader)
					}
				},
			},
			{
				name:         "bearer auth",
				authMethod:   "bearer",
				serverStatus: 200,
				successCodes: []int{200},
				checkAuth: func(t *testing.T) {
					if lastAuthHeader != "Bearer test-secret-key" {
						t.Errorf("expected Bearer auth, got %q", lastAuthHeader)
					}
				},
			},
			{
				name:         "query_param auth with default param name",
				authMethod:   "query_param",
				authHeader:   "",
				serverStatus: 200,
				successCodes: []int{200},
				checkAuth: func(t *testing.T) {
					if lastQueryKey != "test-secret-key" {
						t.Errorf("expected query param 'key'=test-secret-key, got %q", lastQueryKey)
					}
				},
			},
			{
				name:         "custom auth with prefix",
				authMethod:   "custom",
				authPrefix:   "Bot ",
				serverStatus: 200,
				successCodes: []int{200},
				checkAuth: func(t *testing.T) {
					if lastAuthHeader != "Bot test-secret-key" {
						t.Errorf("expected 'Bot test-secret-key', got %q", lastAuthHeader)
					}
				},
			},
			{
				name:         "custom auth with default prefix (Bearer)",
				authMethod:   "custom",
				authPrefix:   "",
				serverStatus: 200,
				successCodes: []int{200},
				checkAuth: func(t *testing.T) {
					if lastAuthHeader != "Bearer test-secret-key" {
						t.Errorf("expected 'Bearer test-secret-key', got %q", lastAuthHeader)
					}
				},
			},
			{
				name:           "fail status code returns error",
				authMethod:     "bearer",
				serverStatus:   401,
				successCodes:   []int{200},
				failCodes:      []int{401, 403},
				wantErr:        true,
				wantErrContain: "invalid API key",
			},
			{
				name:         "success status code returns nil",
				authMethod:   "bearer",
				serverStatus: 200,
				successCodes: []int{200, 429},
				failCodes:    []int{401},
				wantErr:      false,
			},
			{
				name:         "rate limited (429) in success codes returns nil",
				authMethod:   "bearer",
				serverStatus: 429,
				successCodes: []int{200, 429},
				failCodes:    []int{401},
				wantErr:      false,
			},
			{
				name:           "unexpected status code returns error",
				authMethod:     "bearer",
				serverStatus:   503,
				successCodes:   []int{200},
				failCodes:      []int{401},
				wantErr:        true,
				wantErrContain: "unexpected status 503",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				// Reset captured values
				lastAuthHeader = ""
				lastQueryKey = ""
				lastPath = ""
				responseStatus = tt.serverStatus

				spec := catalogValidationSpec(
					ts.URL+"/validate/{{key}}/check",
					"GET", tt.authMethod, tt.authHeader, tt.authPrefix,
					tt.successCodes, tt.failCodes,
				)

				srv := &Server{store: &mockCatalogStore{
					entry: &store.ProviderCatalogEntry{
						ID:         "test-provider",
						Validation: spec,
					},
				}}

				err := srv.validateFromCatalog(context.Background(), "test-provider", "test-secret-key")

				if tt.wantErr {
					if err == nil {
						t.Fatal("expected error, got nil")
					}
					if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
						t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrContain)
					}
				} else {
					if err != nil {
						t.Fatalf("expected nil, got %v", err)
					}
				}

				// Verify key substitution in URL path
				if lastPath != "" && !strings.Contains(lastPath, "test-secret-key") {
					t.Errorf("expected URL path to contain key, got %q", lastPath)
				}

				if tt.checkAuth != nil {
					tt.checkAuth(t)
				}
			})
		}
	})
}

func TestShouldPushConfigForCredentialChange(t *testing.T) {
	t.Run("search provider from catalog", func(t *testing.T) {
		srv := &Server{store: &mockCatalogStore{
			entry: &store.ProviderCatalogEntry{
				ID:       "exa",
				Category: "search",
			},
		}}
		if !srv.shouldPushConfigForCredentialChange(context.Background(), "exa") {
			t.Fatal("expected search provider credential change to trigger config push")
		}
	})

	t.Run("ai provider from catalog", func(t *testing.T) {
		srv := &Server{store: &mockCatalogStore{
			entry: &store.ProviderCatalogEntry{
				ID:       "openai",
				Category: "ai",
			},
		}}
		if !srv.shouldPushConfigForCredentialChange(context.Background(), "openai") {
			t.Fatal("expected ai provider credential change to trigger config push")
		}
	})

	t.Run("channel provider from catalog", func(t *testing.T) {
		srv := &Server{store: &mockCatalogStore{
			entry: &store.ProviderCatalogEntry{
				ID:       "telegram",
				Category: "channel",
			},
		}}
		if srv.shouldPushConfigForCredentialChange(context.Background(), "telegram") {
			t.Fatal("did not expect channel credential change to trigger config push")
		}
	})

	t.Run("legacy fallback provider", func(t *testing.T) {
		srv := &Server{store: &mockCatalogStore{err: context.DeadlineExceeded}}
		if !srv.shouldPushConfigForCredentialChange(context.Background(), "google") {
			t.Fatal("expected legacy proxied provider to trigger config push on fallback")
		}
	})
}
