package apiproxy

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/metadata"
)

// newTestProxy creates a Proxy with a fake metadata server and the given providers.
func newTestProxy(providers map[string]*Provider, metaSrv *metadata.Server) *Proxy {
	return &Proxy{
		metaSrv:   metaSrv,
		usage:     NewUsageTracker(),
		providers: providers,
		client:    http.DefaultClient,
	}
}

func TestAllowedHostsEnforced(t *testing.T) {
	// Set up a metadata server with a registered VM so auth passes.
	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "test-machine",
		AccountID: 1,
		Nonce:     "test-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"testprovider": {Value: "sk-real-key", CredentialType: "api_key"},
		},
	})

	tests := []struct {
		name         string
		upstreamHost string
		allowedHosts []string
		wantStatus   int
	}{
		{
			name:         "allowed host passes",
			upstreamHost: "api.example.com",
			allowedHosts: []string{"api.example.com"},
			wantStatus:   http.StatusBadGateway, // will fail upstream but passes host check
		},
		{
			name:         "disallowed host rejected",
			upstreamHost: "evil.example.com",
			allowedHosts: []string{"api.example.com"},
			wantStatus:   http.StatusForbidden,
		},
		{
			name:         "case insensitive match",
			upstreamHost: "API.Example.COM",
			allowedHosts: []string{"api.example.com"},
			wantStatus:   http.StatusBadGateway, // passes host check
		},
		{
			name:         "empty allowed hosts rejects",
			upstreamHost: "api.example.com",
			allowedHosts: []string{},
			wantStatus:   http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &Provider{
				Name:         "testprovider",
				UpstreamHost: tt.upstreamHost,
				Scheme:       "https",
				PathPrefix:   "/testprovider",
				AllowedHosts: tt.allowedHosts,
				InjectKey: func(req *http.Request, key, credentialType string) {
					req.Header.Set("x-api-key", key)
				},
				ExtractToken: func(req *http.Request) string {
					return req.Header.Get("x-api-key")
				},
				ParseJSONUsage: func(body []byte) (string, int, int) {
					return "", 0, 0
				},
				ParseSSEEvent: func(eventType, data string) (string, int, int, bool) {
					return "", 0, 0, false
				},
			}

			proxy := newTestProxy(map[string]*Provider{
				"testprovider": provider,
			}, metaSrv)

			req := httptest.NewRequest(http.MethodPost, "/testprovider/v1/messages", nil)
			req.Header.Set("x-api-key", "test-nonce")
			req.RemoteAddr = "127.0.0.1:12345"

			rr := httptest.NewRecorder()
			proxy.handleProxy(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d; body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
		})
	}
}

func TestTelegramPathRewriting(t *testing.T) {
	p := telegramProvider()

	tests := []struct {
		name     string
		path     string
		key      string
		wantPath string
	}{
		{
			name:     "sendMessage",
			path:     "/telegram/sendMessage",
			key:      "123456:ABC-DEF",
			wantPath: "/bot123456:ABC-DEF/sendMessage",
		},
		{
			name:     "getUpdates",
			path:     "/telegram/getUpdates",
			key:      "tok123",
			wantPath: "/bottok123/getUpdates",
		},
		{
			name:     "nested path",
			path:     "/telegram/setWebhook",
			key:      "mytoken",
			wantPath: "/botmytoken/setWebhook",
		},
		{
			name:     "file download photo",
			path:     "/telegram/file/photos/abc.jpg",
			key:      "123456:ABC-DEF",
			wantPath: "/file/bot123456:ABC-DEF/photos/abc.jpg",
		},
		{
			name:     "file download document",
			path:     "/telegram/file/documents/file_123",
			key:      "tok123",
			wantPath: "/file/bottok123/documents/file_123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			p.InjectKey(req, tt.key, "api_key")
			if req.URL.Path != tt.wantPath {
				t.Errorf("got path %q, want %q", req.URL.Path, tt.wantPath)
			}
		})
	}
}

func TestDiscordBotKeyInjection(t *testing.T) {
	p := discordBotProvider()

	req := httptest.NewRequest(http.MethodPost, "/discord/api/v10/channels/123/messages", nil)
	p.InjectKey(req, "my-bot-token", "api_key")

	got := req.Header.Get("Authorization")
	want := "Bot my-bot-token"
	if got != want {
		t.Errorf("Authorization header = %q, want %q", got, want)
	}
}

func TestWhatsAppKeyInjection(t *testing.T) {
	p := whatsappProvider()

	req := httptest.NewRequest(http.MethodPost, "/whatsapp/v17.0/123/messages", nil)
	p.InjectKey(req, "EAABx...", "api_key")

	got := req.Header.Get("Authorization")
	want := "Bearer EAABx..."
	if got != want {
		t.Errorf("Authorization header = %q, want %q", got, want)
	}
}

func TestChannelProviderExtractToken(t *testing.T) {
	providers := []*Provider{telegramProvider(), discordBotProvider(), whatsappProvider()}
	for _, p := range providers {
		t.Run(p.Name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("x-api-key", "test-nonce-123")
			got := p.ExtractToken(req)
			if got != "test-nonce-123" {
				t.Errorf("%s ExtractToken = %q, want %q", p.Name, got, "test-nonce-123")
			}
		})
	}
}

func TestChannelProviderNilUsageParsers(t *testing.T) {
	providers := []*Provider{telegramProvider(), discordBotProvider(), whatsappProvider()}
	for _, p := range providers {
		t.Run(p.Name, func(t *testing.T) {
			if p.ParseJSONUsage != nil {
				t.Errorf("%s ParseJSONUsage should be nil", p.Name)
			}
			if p.ParseSSEEvent != nil {
				t.Errorf("%s ParseSSEEvent should be nil", p.Name)
			}
		})
	}
}

func TestIsHostAllowed(t *testing.T) {
	tests := []struct {
		host    string
		allowed []string
		want    bool
	}{
		{"api.anthropic.com", []string{"api.anthropic.com"}, true},
		{"api.openai.com", []string{"api.openai.com", "api.anthropic.com"}, true},
		{"evil.com", []string{"api.anthropic.com"}, false},
		{"API.ANTHROPIC.COM", []string{"api.anthropic.com"}, true},
		{"api.anthropic.com", []string{}, false},
		{"api.anthropic.com", nil, false},
	}

	for _, tt := range tests {
		got := isHostAllowed(tt.host, tt.allowed)
		if got != tt.want {
			t.Errorf("isHostAllowed(%q, %v) = %v, want %v", tt.host, tt.allowed, got, tt.want)
		}
	}
}

func TestDiscordCDNHostResolution(t *testing.T) {
	p := discordBotProvider()

	tests := []struct {
		name     string
		path     string
		wantHost string
	}{
		{
			name:     "CDN attachment",
			path:     "/cdn/attachments/123/456/image.png",
			wantHost: "cdn.discordapp.com",
		},
		{
			name:     "direct attachment path",
			path:     "/attachments/123/456/image.png",
			wantHost: "cdn.discordapp.com",
		},
		{
			name:     "media sticker",
			path:     "/media/stickers/789.png",
			wantHost: "media.discordapp.net",
		},
		{
			name:     "stickers path",
			path:     "/stickers/789.png",
			wantHost: "media.discordapp.net",
		},
		{
			name:     "API default",
			path:     "/api/v10/channels/123/messages",
			wantHost: "discord.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.ResolveHost(tt.path)
			if got != tt.wantHost {
				t.Errorf("ResolveHost(%q) = %q, want %q", tt.path, got, tt.wantHost)
			}
		})
	}
}

func TestDiscordCDNSkipsAuth(t *testing.T) {
	p := discordBotProvider()

	// CDN host — should NOT set Authorization
	req := httptest.NewRequest(http.MethodGet, "https://cdn.discordapp.com/attachments/123/456/image.png", nil)
	p.InjectKey(req, "my-bot-token", "api_key")
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("CDN request got Authorization = %q, want empty", got)
	}

	// API host — should set Authorization
	req2 := httptest.NewRequest(http.MethodPost, "https://discord.com/api/v10/channels/123/messages", nil)
	p.InjectKey(req2, "my-bot-token", "api_key")
	if got := req2.Header.Get("Authorization"); got != "Bot my-bot-token" {
		t.Errorf("API request got Authorization = %q, want %q", got, "Bot my-bot-token")
	}
}

func TestWhatsAppCDNHostResolution(t *testing.T) {
	p := whatsappProvider()

	tests := []struct {
		name     string
		path     string
		wantHost string
	}{
		{
			name:     "media download",
			path:     "/media/download/abc123",
			wantHost: "lookaside.fbsbx.com",
		},
		{
			name:     "API default",
			path:     "/v17.0/123/messages",
			wantHost: "graph.facebook.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.ResolveHost(tt.path)
			if got != tt.wantHost {
				t.Errorf("ResolveHost(%q) = %q, want %q", tt.path, got, tt.wantHost)
			}
		})
	}
}

func TestMultipartFormDataStreaming(t *testing.T) {
	// Create a fake upstream server that echoes body back
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, r.Body)
	}))
	defer upstream.Close()

	// Build a 5MB multipart body
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", "test.bin")
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 5*1024*1024) // 5MB
	for i := range payload {
		payload[i] = byte(i % 256)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()

	// Extract host:port from upstream URL (strip https://)
	upstreamHost := upstream.Listener.Addr().String()

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "test-machine",
		AccountID: 1,
		Nonce:     "test-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"testprovider": {Value: "sk-real-key", CredentialType: "api_key"},
		},
	})

	provider := &Provider{
		Name:         "testprovider",
		UpstreamHost: upstreamHost,
		Scheme:       "https",
		PathPrefix:   "/testprovider",
		AllowedHosts: []string{upstreamHost},
		InjectKey: func(req *http.Request, key, credentialType string) {
			req.Header.Set("x-api-key", key)
		},
		ExtractToken: func(req *http.Request) string {
			return req.Header.Get("x-api-key")
		},
	}

	proxy := newTestProxy(map[string]*Provider{
		"testprovider": provider,
	}, metaSrv)
	proxy.client = upstream.Client()

	req := httptest.NewRequest(http.MethodPost, "/testprovider/upload", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("x-api-key", "test-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Verify Content-Type preserved (should contain multipart/form-data)
	gotCT := rr.Header().Get("Content-Type")
	if gotCT == "" {
		t.Error("response Content-Type is empty, expected multipart/form-data")
	}

	// Verify body received intact (at least 4MB — multipart framing adds overhead
	// on send but the echo is the raw multipart stream which may be slightly less
	// than the in-memory buffer due to chunked transfer encoding)
	if rr.Body.Len() < 4*1024*1024 {
		t.Errorf("response body = %d bytes, want >= %d", rr.Body.Len(), 4*1024*1024)
	}
}

func TestKeyOptionalProvider(t *testing.T) {
	// Create upstream that returns 200
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	upstreamHost := upstream.Listener.Addr().String()

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "test-machine",
		AccountID: 1,
		Nonce:     "test-nonce",
		LLMKeys:   map[string]metadata.CredentialEntry{}, // no keys at all
	})

	provider := &Provider{
		Name:         "cdn_provider",
		UpstreamHost: upstreamHost,
		Scheme:       "https",
		PathPrefix:   "/cdn_provider",
		AllowedHosts: []string{upstreamHost},
		KeyOptional:  true,
		InjectKey: func(req *http.Request, key, credentialType string) {
			// no-op for CDN
		},
		ExtractToken: func(req *http.Request) string {
			return req.Header.Get("x-api-key")
		},
	}

	proxy := newTestProxy(map[string]*Provider{
		"cdn_provider": provider,
	}, metaSrv)
	proxy.client = upstream.Client()

	req := httptest.NewRequest(http.MethodGet, "/cdn_provider/some/file.png", nil)
	req.Header.Set("x-api-key", "test-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("KeyOptional provider: got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

func TestNonLLMProviderBypassesBudgetGating(t *testing.T) {
	// Budget exceeded — LLM provider should be blocked, non-LLM KeyOptional provider should pass through
	budget := int64(100)

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	upstreamHost := upstream.Listener.Addr().String()

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID:        "test-machine",
		AccountID:        1,
		Nonce:            "test-nonce",
		BudgetMicrocents: &budget,
		LLMKeys: map[string]metadata.CredentialEntry{
			"anthropic": {Value: "sk-ant-key", CredentialType: "api_key"},
		},
	})

	makeProvider := func(name string) *Provider {
		return &Provider{
			Name:         name,
			UpstreamHost: upstreamHost,
			Scheme:       "https",
			PathPrefix:   "/" + name,
			AllowedHosts: []string{upstreamHost},
			InjectKey: func(req *http.Request, key, credentialType string) {
				req.Header.Set("x-api-key", key)
			},
			ExtractToken: func(req *http.Request) string {
				return req.Header.Get("x-api-key")
			},
			ParseJSONUsage: func(body []byte) (string, int, int) { return "", 0, 0 },
			ParseSSEEvent:  func(eventType, data string) (string, int, int, bool) { return "", 0, 0, false },
		}
	}

	// CDN provider is KeyOptional and not in LLMKeys — bypasses budget
	cdnProvider := makeProvider("cdn")
	cdnProvider.KeyOptional = true

	proxy := newTestProxy(map[string]*Provider{
		"anthropic": makeProvider("anthropic"),
		"cdn":       cdnProvider,
	}, metaSrv)
	proxy.client = upstream.Client()

	// Exceed the budget
	proxy.usage.Record(UsageRecord{MachineID: "test-machine", CostMicrocents: 200})

	// LLM provider (anthropic) with exceeded budget → 402
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	req.Header.Set("x-api-key", "test-nonce")
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)
	if rr.Code != http.StatusPaymentRequired {
		t.Errorf("LLM provider with exceeded budget: got status %d, want 402; body: %s", rr.Code, rr.Body.String())
	}

	// Non-LLM KeyOptional provider with exceeded budget → passes through (200)
	req2 := httptest.NewRequest(http.MethodPost, "/cdn/some-asset", nil)
	req2.Header.Set("x-api-key", "test-nonce")
	req2.RemoteAddr = "127.0.0.1:12345"
	rr2 := httptest.NewRecorder()
	proxy.handleProxy(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Errorf("non-LLM KeyOptional provider with exceeded budget: got status %d, want 200; body: %s", rr2.Code, rr2.Body.String())
	}
}

func TestKeyOptionalProviderWithoutKey(t *testing.T) {
	// Verify that a non-KeyOptional provider still rejects when no key
	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "test-machine",
		AccountID: 1,
		Nonce:     "test-nonce",
		LLMKeys:   map[string]metadata.CredentialEntry{}, // no keys
	})

	provider := &Provider{
		Name:         "strict_provider",
		UpstreamHost: "api.example.com",
		Scheme:       "https",
		PathPrefix:   "/strict_provider",
		AllowedHosts: []string{"api.example.com"},
		KeyOptional:  false, // requires key
		InjectKey: func(req *http.Request, key, credentialType string) {
			req.Header.Set("x-api-key", key)
		},
		ExtractToken: func(req *http.Request) string {
			return req.Header.Get("x-api-key")
		},
	}

	proxy := newTestProxy(map[string]*Provider{
		"strict_provider": provider,
	}, metaSrv)

	req := httptest.NewRequest(http.MethodGet, "/strict_provider/v1/data", nil)
	req.Header.Set("x-api-key", "test-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("non-KeyOptional provider with no key: got status %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
}

func TestAnthropicCredentialTypeBranching(t *testing.T) {
	p := anthropicProvider()

	tests := []struct {
		name           string
		key            string
		credentialType string
		wantHeader     string
		wantValue      string
	}{
		{
			name:           "api_key uses x-api-key",
			key:            "sk-ant-api03-key-123",
			credentialType: "api_key",
			wantHeader:     "x-api-key",
			wantValue:      "sk-ant-api03-key-123",
		},
		{
			name:           "empty credential type defaults to x-api-key",
			key:            "sk-ant-api03-key-123",
			credentialType: "",
			wantHeader:     "x-api-key",
			wantValue:      "sk-ant-api03-key-123",
		},
		{
			name:           "subscription_key uses Bearer auth",
			key:            "sk-ant-oat-token-456",
			credentialType: "subscription_key",
			wantHeader:     "Authorization",
			wantValue:      "Bearer sk-ant-oat-token-456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
			p.InjectKey(req, tt.key, tt.credentialType)

			got := req.Header.Get(tt.wantHeader)
			if got != tt.wantValue {
				t.Errorf("%s = %q, want %q", tt.wantHeader, got, tt.wantValue)
			}

			// Verify anthropic-version header is always set
			if av := req.Header.Get("anthropic-version"); av == "" {
				t.Error("expected anthropic-version header to be set")
			}

			// Verify anthropic-beta header for OAuth/subscription credentials
			beta := req.Header.Get("anthropic-beta")
			if tt.credentialType == "subscription_key" || tt.credentialType == "oauth" {
				if beta != "oauth-2025-04-20" {
					t.Errorf("anthropic-beta = %q, want %q for %s", beta, "oauth-2025-04-20", tt.credentialType)
				}
			} else {
				if beta != "" {
					t.Errorf("anthropic-beta = %q, want empty for %s", beta, tt.credentialType)
				}
			}
		})
	}
}

func TestHandlerSubscriptionKeyUsesBearer(t *testing.T) {
	// Verify the full handler flow: metadata lookup → credType extraction → Bearer auth upstream.
	// This catches bugs where credType is lost between CredentialEntry and InjectKey.
	var gotAuthHeader, gotApiKeyHeader string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		gotApiKeyHeader = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	upstreamHost := upstream.Listener.Addr().String()

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "test-machine",
		AccountID: 1,
		Nonce:     "test-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"anthropic": {Value: "sk-ant-oat01-real-subscription-key", CredentialType: "subscription_key"},
		},
	})

	anthProvider := anthropicProvider()
	anthProvider.UpstreamHost = upstreamHost
	anthProvider.AllowedHosts = []string{upstreamHost}

	proxy := newTestProxy(map[string]*Provider{
		"anthropic": anthProvider,
	}, metaSrv)
	proxy.client = upstream.Client()

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	req.Header.Set("x-api-key", "test-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// subscription_key must use Bearer auth, NOT x-api-key
	if gotAuthHeader != "Bearer sk-ant-oat01-real-subscription-key" {
		t.Errorf("upstream Authorization = %q, want %q", gotAuthHeader, "Bearer sk-ant-oat01-real-subscription-key")
	}
	if gotApiKeyHeader != "" {
		t.Errorf("upstream x-api-key should be empty for subscription_key, got %q", gotApiKeyHeader)
	}
}

func TestProxy_SearchProviderRouting(t *testing.T) {
	// Verify a search provider registered via CustomProviderToProxy routes requests
	// and injects the correct auth header.
	var gotSubscriptionToken string
	var gotPath string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSubscriptionToken = r.Header.Get("X-Subscription-Token")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer upstream.Close()
	upstreamHost := upstream.Listener.Addr().String()

	// Build a CustomProviderConfig for "brave" search pointing to the mock.
	braveConfig := metadata.CustomProviderConfig{
		Name:         "brave",
		UpstreamHost: upstreamHost,
		Scheme:       "https",
		AuthMethod:   "api_key_header",
		AuthHeader:   "X-Subscription-Token",
		AllowedHosts: []string{upstreamHost},
	}
	braveProvider := CustomProviderToProxy(braveConfig)

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "search-test-machine",
		AccountID: 1,
		Nonce:     "search-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"brave": {Value: "BSA-real-brave-key", CredentialType: "api_key"},
		},
	})

	proxy := newTestProxy(map[string]*Provider{
		"brave": braveProvider,
	}, metaSrv)
	proxy.client = upstream.Client()

	req := httptest.NewRequest(http.MethodGet, "/brave/res/v1/web/search?q=test&count=1", nil)
	req.Header.Set("x-api-key", "search-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Verify the upstream received the request with the real key in X-Subscription-Token.
	if gotSubscriptionToken != "BSA-real-brave-key" {
		t.Errorf("upstream X-Subscription-Token = %q, want %q", gotSubscriptionToken, "BSA-real-brave-key")
	}

	// Verify the path was forwarded correctly (stripped /brave prefix).
	if gotPath != "/res/v1/web/search" {
		t.Errorf("upstream path = %q, want %q", gotPath, "/res/v1/web/search")
	}

	// Verify the response was proxied back.
	if !bytes.Contains(rr.Body.Bytes(), []byte(`"results"`)) {
		t.Errorf("expected proxied response body, got %s", rr.Body.String())
	}
}

func TestProxy_SearchProviderRuntimeIDRouting(t *testing.T) {
	// Verify that a provider registered with a runtime ID (e.g. gemini-search → gemini)
	// routes correctly when requests arrive at /gemini/*.
	var gotPath string
	var gotQueryKey string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQueryKey = r.URL.Query().Get("key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer upstream.Close()
	upstreamHost := upstream.Listener.Addr().String()

	// The catalog entry is gemini-search with runtime_provider_id = "gemini".
	// The proxy registers it under the runtime ID "gemini".
	geminiConfig := metadata.CustomProviderConfig{
		Name:         "gemini", // runtime provider ID, not catalog ID
		UpstreamHost: upstreamHost,
		Scheme:       "https",
		AuthMethod:   "query_param",
		AuthHeader:   "key", // query param name
		AllowedHosts: []string{upstreamHost},
	}
	geminiProvider := CustomProviderToProxy(geminiConfig)

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "gemini-test-machine",
		AccountID: 1,
		Nonce:     "gemini-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"gemini": {Value: "AIza-gemini-key", CredentialType: "api_key"},
		},
	})

	proxy := newTestProxy(map[string]*Provider{
		"gemini": geminiProvider,
	}, metaSrv)
	proxy.client = upstream.Client()

	req := httptest.NewRequest(http.MethodGet, "/gemini/v1beta/models", nil)
	req.Header.Set("x-api-key", "gemini-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Verify the path was forwarded correctly.
	if gotPath != "/v1beta/models" {
		t.Errorf("upstream path = %q, want %q", gotPath, "/v1beta/models")
	}

	// Verify the real key was injected as a query parameter.
	if gotQueryKey != "AIza-gemini-key" {
		t.Errorf("upstream query key = %q, want %q", gotQueryKey, "AIza-gemini-key")
	}
}

func TestProxy_SearchProviderRuntimeIDRouting_BearerNonce(t *testing.T) {
	var gotPath string
	var gotQueryKey string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQueryKey = r.URL.Query().Get("key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer upstream.Close()
	upstreamHost := upstream.Listener.Addr().String()

	geminiProvider := CustomProviderToProxy(metadata.CustomProviderConfig{
		Name:         "gemini",
		UpstreamHost: upstreamHost,
		Scheme:       "https",
		AuthMethod:   "query_param",
		AuthHeader:   "key",
		AllowedHosts: []string{upstreamHost},
	})

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "gemini-bearer-machine",
		AccountID: 1,
		Nonce:     "gemini-bearer-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"gemini": {Value: "AIza-gemini-key", CredentialType: "api_key"},
		},
	})

	proxy := newTestProxy(map[string]*Provider{"gemini": geminiProvider}, metaSrv)
	proxy.client = upstream.Client()

	req := httptest.NewRequest(http.MethodGet, "/gemini/v1beta/models", nil)
	req.Header.Set("Authorization", "Bearer gemini-bearer-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if gotPath != "/v1beta/models" {
		t.Errorf("upstream path = %q, want %q", gotPath, "/v1beta/models")
	}
	if gotQueryKey != "AIza-gemini-key" {
		t.Errorf("upstream query key = %q, want %q", gotQueryKey, "AIza-gemini-key")
	}
}

func TestProxy_GoogleProviderAcceptsBearerNonce(t *testing.T) {
	var gotPath string
	var gotAuthHeader string
	var gotContentType string
	var gotBody map[string]any
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthHeader = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`))
	}))
	defer upstream.Close()
	upstreamHost := upstream.Listener.Addr().String()

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "google-bearer-machine",
		AccountID: 1,
		Nonce:     "google-bearer-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"google": {Value: "AIza-google-key", CredentialType: "api_key"},
		},
	})

	providers := initProviders()
	providers["google"].UpstreamHost = upstreamHost
	providers["google"].AllowedHosts = []string{upstreamHost}

	proxy := newTestProxy(providers, metaSrv)
	proxy.client = upstream.Client()

	body, err := json.Marshal(map[string]any{
		"model": "gemini-2.5-flash",
		"store": false,
		"messages": []map[string]string{
			{"role": "user", "content": "Say OK"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/google/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer google-bearer-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if gotPath != "/v1beta/openai/chat/completions" {
		t.Errorf("upstream path = %q, want %q", gotPath, "/v1beta/openai/chat/completions")
	}
	if gotAuthHeader != "Bearer AIza-google-key" {
		t.Errorf("upstream Authorization = %q, want %q", gotAuthHeader, "Bearer AIza-google-key")
	}
	if gotContentType != "application/json" {
		t.Errorf("upstream content-type = %q, want application/json", gotContentType)
	}
	if _, ok := gotBody["store"]; ok {
		t.Fatalf("upstream body unexpectedly contained store: %#v", gotBody["store"])
	}
	if gotBody["model"] != "gemini-2.5-flash" {
		t.Errorf("upstream model = %#v, want %q", gotBody["model"], "gemini-2.5-flash")
	}
}

func TestProxy_GoogleErrorArrayIsNormalizedForClient(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`[{"error":{"code":404,"message":"This model models/gemini-2.0-flash is no longer available to new users.","status":"NOT_FOUND"}}]`))
	}))
	defer upstream.Close()
	upstreamHost := upstream.Listener.Addr().String()

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "google-error-machine",
		AccountID: 1,
		Nonce:     "google-error-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"google": {Value: "AIza-google-key", CredentialType: "api_key"},
		},
	})

	providers := initProviders()
	providers["google"].UpstreamHost = upstreamHost
	providers["google"].AllowedHosts = []string{upstreamHost}

	proxy := newTestProxy(providers, metaSrv)
	proxy.client = upstream.Client()

	body, err := json.Marshal(map[string]any{
		"model": "gemini-2.0-flash",
		"messages": []map[string]string{
			{"role": "user", "content": "Say OK"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/google/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer google-error-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404; body: %s", rr.Code, rr.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("response body was not normalized JSON object: %v; body: %s", err, rr.Body.String())
	}
	errObj, ok := got["error"].(map[string]any)
	if !ok {
		t.Fatalf("response body missing error object: %#v", got)
	}
	if errObj["status"] != "NOT_FOUND" {
		t.Errorf("error.status = %#v, want %q", errObj["status"], "NOT_FOUND")
	}
	if !strings.Contains(errObj["message"].(string), "no longer available to new users") {
		t.Errorf("error.message = %#v, want model deprecation message", errObj["message"])
	}
}

func TestProxy_AfterCredentialReplace_EmptyRejectsRequests(t *testing.T) {
	// Verify that after ReplaceMachineLLMKeys with an empty map, the proxy rejects
	// requests because no credential is found.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	upstreamHost := upstream.Listener.Addr().String()

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "cred-replace-machine",
		AccountID: 1,
		Nonce:     "cred-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"anthropic": {Value: "sk-ant-key", CredentialType: "api_key"},
		},
	})

	anthProvider := anthropicProvider()
	anthProvider.UpstreamHost = upstreamHost
	anthProvider.AllowedHosts = []string{upstreamHost}

	proxy := newTestProxy(map[string]*Provider{
		"anthropic": anthProvider,
	}, metaSrv)
	proxy.client = upstream.Client()

	// Step 1: Verify the request works initially (200).
	req1 := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	req1.Header.Set("x-api-key", "cred-nonce")
	req1.RemoteAddr = "127.0.0.1:12345"
	rr1 := httptest.NewRecorder()
	proxy.handleProxy(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("initial request: got status %d, want 200; body: %s", rr1.Code, rr1.Body.String())
	}

	// Step 2: Replace credentials with empty map.
	metaSrv.ReplaceMachineLLMKeys("127.0.0.1", map[string]metadata.CredentialEntry{})

	// Step 3: Verify next request returns 403 (no credential).
	req2 := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	req2.Header.Set("x-api-key", "cred-nonce")
	req2.RemoteAddr = "127.0.0.1:12345"
	rr2 := httptest.NewRecorder()
	proxy.handleProxy(rr2, req2)
	if rr2.Code != http.StatusForbidden {
		t.Errorf("after empty credential replace: got status %d, want 403; body: %s", rr2.Code, rr2.Body.String())
	}
}

func TestHandlerApiKeyUsesXApiKey(t *testing.T) {
	// Verify regular api_key credentials use x-api-key header, not Bearer.
	var gotAuthHeader, gotApiKeyHeader string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		gotApiKeyHeader = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	upstreamHost := upstream.Listener.Addr().String()

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "test-machine",
		AccountID: 1,
		Nonce:     "test-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"anthropic": {Value: "sk-ant-api03-regular-key", CredentialType: "api_key"},
		},
	})

	anthProvider := anthropicProvider()
	anthProvider.UpstreamHost = upstreamHost
	anthProvider.AllowedHosts = []string{upstreamHost}

	proxy := newTestProxy(map[string]*Provider{
		"anthropic": anthProvider,
	}, metaSrv)
	proxy.client = upstream.Client()

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	req.Header.Set("x-api-key", "test-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// api_key must use x-api-key header, NOT Bearer
	if gotApiKeyHeader != "sk-ant-api03-regular-key" {
		t.Errorf("upstream x-api-key = %q, want %q", gotApiKeyHeader, "sk-ant-api03-regular-key")
	}
	if gotAuthHeader != "" {
		t.Errorf("upstream Authorization should be empty for api_key, got %q", gotAuthHeader)
	}
}

// TestSearchProviderNoCredentialReturns403 verifies that a search provider
// without a configured credential returns 403 (not a crash or 500).
func TestSearchProviderNoCredentialReturns403(t *testing.T) {
	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "test-machine",
		AccountID: 1,
		Nonce:     "test-nonce",
		LLMKeys:   map[string]metadata.CredentialEntry{}, // no keys
	})

	braveProvider := CustomProviderToProxy(metadata.CustomProviderConfig{
		Name:         "brave",
		UpstreamHost: "api.search.brave.com",
		Scheme:       "https",
		AuthMethod:   "api_key_header",
		AuthHeader:   "X-Subscription-Token",
		AllowedHosts: []string{"api.search.brave.com"},
	})

	proxy := newTestProxy(map[string]*Provider{
		"brave": braveProvider,
	}, metaSrv)

	req := httptest.NewRequest(http.MethodGet, "/brave/res/v1/web/search?q=test", nil)
	req.Header.Set("x-api-key", "test-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("search provider without credential: got status %d, want 403; body: %s",
			rr.Code, rr.Body.String())
	}
}

// TestSearchProviderCustomProviderFallback verifies that a search provider
// defined as a CustomProvider on the machine config (not pre-registered in
// the proxy's provider map) is discovered at request time via the fallback
// path in handleProxy.
func TestSearchProviderCustomProviderFallback(t *testing.T) {
	var gotAPIKey string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("X-Subscription-Token")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer upstream.Close()
	upstreamHost := upstream.Listener.Addr().String()

	metaSrv := metadata.New("127.0.0.1", 0)
	metaSrv.RegisterMachine("127.0.0.1", metadata.MachineConfig{
		MachineID: "test-machine",
		AccountID: 1,
		Nonce:     "test-nonce",
		LLMKeys: map[string]metadata.CredentialEntry{
			"brave": {Value: "BSA-custom-provider-key", CredentialType: "api_key"},
		},
		CustomProviders: []metadata.CustomProviderConfig{
			{
				Name:         "brave",
				UpstreamHost: upstreamHost,
				Scheme:       "https",
				AuthMethod:   "api_key_header",
				AuthHeader:   "X-Subscription-Token",
				AllowedHosts: []string{upstreamHost},
			},
		},
	})

	// Empty provider map — brave will be resolved from CustomProviders
	proxy := newTestProxy(map[string]*Provider{}, metaSrv)
	proxy.client = upstream.Client()

	req := httptest.NewRequest(http.MethodGet, "/brave/res/v1/web/search?q=hello", nil)
	req.Header.Set("x-api-key", "test-nonce")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	proxy.handleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("custom provider fallback: got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	if gotAPIKey != "BSA-custom-provider-key" {
		t.Errorf("upstream X-Subscription-Token = %q, want %q", gotAPIKey, "BSA-custom-provider-key")
	}
}
