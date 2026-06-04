package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDevBypassMiddleware_SetsEmail(t *testing.T) {
	email := "dev@example.com"
	middleware := DevBypassMiddleware(email)

	var gotClaims *Claims
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if gotClaims == nil {
		t.Fatal("expected claims in context")
	}
	if gotClaims.Email != email {
		t.Errorf("Email = %q, want %q", gotClaims.Email, email)
	}
}

func TestDevBypassMiddleware_UserIDIsZero(t *testing.T) {
	middleware := DevBypassMiddleware("test@dev.com")

	var gotClaims *Claims
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if gotClaims == nil {
		t.Fatal("expected claims in context")
	}
	if gotClaims.UserID != 0 {
		t.Errorf("UserID = %d, want 0", gotClaims.UserID)
	}
	if gotClaims.CfSub != "dev-bypass" {
		t.Errorf("CfSub = %q, want %q", gotClaims.CfSub, "dev-bypass")
	}
}

func TestDevBypassMiddleware_DefaultEmail(t *testing.T) {
	middleware := DevBypassMiddleware("")

	var gotClaims *Claims
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if gotClaims == nil {
		t.Fatal("expected claims in context")
	}
	if gotClaims.Email != "dev@localhost" {
		t.Errorf("Email = %q, want %q", gotClaims.Email, "dev@localhost")
	}
}
