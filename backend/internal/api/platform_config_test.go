package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlePlatformConfig(t *testing.T) {
	srv := &Server{
		authMode:           "firebase",
		dataPlaneDomain:    "example.com",
		cfAccessAuthDomain: "example", // team name only
	}
	srv.SetFrontendURL("https://example.com")

	req := httptest.NewRequest(http.MethodGet, "/api/platform/config", nil)
	w := httptest.NewRecorder()

	srv.handlePlatformConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp platformConfigResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.DataPlaneDomain != "example.com" {
		t.Errorf("data_plane_domain = %q, want %q", resp.DataPlaneDomain, "example.com")
	}
	if resp.AuthMode != "firebase" {
		t.Errorf("auth_mode = %q, want %q", resp.AuthMode, "firebase")
	}
	if resp.FrontendURL != "https://example.com" {
		t.Errorf("frontend_url = %q, want %q", resp.FrontendURL, "https://example.com")
	}
	// Team name "example" should be expanded to full domain
	if resp.CfAccessAuthDomain != "example.cloudflareaccess.com" {
		t.Errorf("cf_access_auth_domain = %q, want %q", resp.CfAccessAuthDomain, "example.cloudflareaccess.com")
	}
}

func TestHandlePlatformConfig_FullDomainPassthrough(t *testing.T) {
	srv := &Server{
		authMode:           "firebase",
		cfAccessAuthDomain: "custom.cloudflareaccess.com", // already full domain
	}

	req := httptest.NewRequest(http.MethodGet, "/api/platform/config", nil)
	w := httptest.NewRecorder()

	srv.handlePlatformConfig(w, req)

	var resp platformConfigResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.CfAccessAuthDomain != "custom.cloudflareaccess.com" {
		t.Errorf("cf_access_auth_domain = %q, want passthrough", resp.CfAccessAuthDomain)
	}
}

func TestHandlePlatformConfig_Defaults(t *testing.T) {
	srv := &Server{
		authMode: "cfaccess",
	}

	req := httptest.NewRequest(http.MethodGet, "/api/platform/config", nil)
	w := httptest.NewRecorder()

	srv.handlePlatformConfig(w, req)

	var resp platformConfigResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// frontendURL() returns empty when frontendBaseURL is not set
	if resp.FrontendURL != "" {
		t.Errorf("frontend_url = %q, want empty", resp.FrontendURL)
	}
	if resp.CfAccessAuthDomain != "" {
		t.Errorf("cf_access_auth_domain = %q, want empty", resp.CfAccessAuthDomain)
	}
}
