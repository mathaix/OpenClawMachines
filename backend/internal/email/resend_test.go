package email

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendEmail_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/emails" {
			t.Errorf("expected /emails, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing or wrong auth header")
		}

		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["to"] != "alice@example.com" {
			t.Errorf("expected to=alice@example.com, got %v", body["to"])
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "email_123"})
	}))
	defer server.Close()

	client := NewClient("test-key", WithBaseURL(server.URL))
	err := client.SendEmail(context.Background(), Email{
		From:    "noreply@openclawmachines.com",
		To:      "alice@example.com",
		Subject: "You're invited",
		HTML:    "<p>Join us</p>",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSendEmail_Retryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient("test-key", WithBaseURL(server.URL))
	err := client.SendEmail(context.Background(), Email{
		From:    "noreply@openclawmachines.com",
		To:      "alice@example.com",
		Subject: "Test",
		HTML:    "<p>Test</p>",
	})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !IsRetryable(err) {
		t.Errorf("expected retryable error, got: %v", err)
	}
}

func TestSendEmail_NonRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "invalid email"})
	}))
	defer server.Close()

	client := NewClient("test-key", WithBaseURL(server.URL))
	err := client.SendEmail(context.Background(), Email{
		From:    "noreply@openclawmachines.com",
		To:      "bad-email",
		Subject: "Test",
		HTML:    "<p>Test</p>",
	})
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if IsRetryable(err) {
		t.Errorf("expected non-retryable error for 400, got: %v", err)
	}
}
