package api

import "testing"

func TestCookieDomainUsesExplicitOverride(t *testing.T) {
	srv := &Server{}
	srv.SetDataPlaneDomain("openclawmachines.com")
	srv.SetCookieDomain(".example.com")

	if got := srv.cookieDomain(); got != ".example.com" {
		t.Fatalf("cookieDomain() = %q, want .example.com", got)
	}
}

func TestCookieDomainFallsBackToDataPlaneDomain(t *testing.T) {
	srv := &Server{}
	srv.SetDataPlaneDomain("example.com")

	if got := srv.cookieDomain(); got != ".example.com" {
		t.Fatalf("cookieDomain() = %q, want .example.com", got)
	}
}

func TestCookieDomainOmitsLocalhost(t *testing.T) {
	srv := &Server{}
	srv.SetDataPlaneDomain("localhost")

	if got := srv.cookieDomain(); got != "" {
		t.Fatalf("cookieDomain() = %q, want empty localhost domain", got)
	}
}
