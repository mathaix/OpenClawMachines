package commands

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateTelegramKey_Valid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true,"result":{"id":123,"is_bot":true,"first_name":"TestBot"}}`))
	}))
	defer ts.Close()

	// We can't easily override the URL in the production code without refactoring,
	// so test the helper logic directly via doValidation instead.
	// This test validates the pattern works with a real HTTP server.
	req, _ := http.NewRequest("GET", ts.URL+"/botTOKEN/getMe", nil)
	if err := doValidation(req, "Telegram"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestValidateTelegramKey_Invalid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/botBAD/getMe", nil)
	err := doValidation(req, "Telegram")
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
	if err.Error() != "invalid API key" {
		t.Fatalf("expected 'invalid API key', got %q", err.Error())
	}
}

func TestValidateCredential_UnknownProvider(t *testing.T) {
	// Unknown providers should skip validation (return nil)
	if err := validateCredential("some-future-provider", "any-key"); err != nil {
		t.Fatalf("expected nil for unknown provider, got %v", err)
	}
}

func TestDoValidation_RateLimited(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
	}))
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL, nil)
	// 429 is treated as "key is valid, just rate limited"
	if err := doValidation(req, "Test"); err != nil {
		t.Fatalf("expected nil for 429, got %v", err)
	}
}

func TestDoValidationWithCodes_GoogleInvalid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
	}))
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL, nil)
	err := doValidationWithCodes(req, "Google", []int{400, 403})
	if err == nil {
		t.Fatal("expected error for 403")
	}
}

func TestValidateSlackBotToken_Valid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true,"team":"Test"}`))
	}))
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/auth.test", nil)
	req.Header.Set("Authorization", "Bearer xoxb-test")
	if err := doValidation(req, "Slack"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestValidateSlackBotToken_Invalid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/auth.test", nil)
	err := doValidation(req, "Slack")
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
}

func TestValidateSlackAppToken_Prefix(t *testing.T) {
	if err := validateSlackAppToken("xapp-1-valid"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if err := validateSlackAppToken("not-xapp"); err == nil {
		t.Fatal("expected error for bad prefix")
	}
}

func TestValidateCredential_Slack(t *testing.T) {
	err := validateCredential("slack", "not-xoxb-token")
	if err == nil {
		t.Fatal("expected error for bad prefix")
	}
}
