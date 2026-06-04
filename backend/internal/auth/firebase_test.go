package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewFirebaseAuth(t *testing.T) {
	fa := NewFirebaseAuth("example-project")
	if fa == nil {
		t.Fatal("expected non-nil FirebaseAuth")
		return
	}
	if fa.projectID != "example-project" {
		t.Errorf("expected projectID 'example-project', got %q", fa.projectID)
	}
}

func TestFirebaseAuth_ProjectID(t *testing.T) {
	fa := NewFirebaseAuth("my-project")
	if fa.ProjectID() != "my-project" {
		t.Errorf("expected 'my-project', got %q", fa.ProjectID())
	}
}

func TestFirebaseAuth_ValidateToken_InvalidToken(t *testing.T) {
	fa := NewFirebaseAuth("example-project")
	_, err := fa.ValidateToken("not.a.valid.token")
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
}

func TestFirebaseAuth_ValidateToken_MalformedJWT(t *testing.T) {
	fa := NewFirebaseAuth("example-project")
	_, err := fa.ValidateToken("totally-not-a-jwt")
	if err == nil {
		t.Fatal("expected error for malformed token")
	}
}

func TestFirebaseMiddleware_FallsBackToLegacyToken(t *testing.T) {
	legacyAuth := New("test-secret-at-least16")
	token, err := legacyAuth.GenerateToken(42, "user@example.com")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	fa := NewFirebaseAuth("test-project")
	middleware := FirebaseMiddleware(fa, legacyAuth)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := UserFromContext(r.Context())
		if claims == nil {
			t.Fatal("expected claims in context")
			return
		}
		if claims.UserID != 42 {
			t.Errorf("expected UserID 42, got %d", claims.UserID)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.AddCookie(&http.Cookie{Name: "ocm_token", Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestFirebaseMiddleware_FallsBackToBearer(t *testing.T) {
	legacyAuth := New("test-secret-at-least16")
	token, err := legacyAuth.GenerateToken(99, "bearer@example.com")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	fa := NewFirebaseAuth("test-project")
	middleware := FirebaseMiddleware(fa, legacyAuth)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := UserFromContext(r.Context())
		if claims == nil {
			t.Fatal("expected claims in context")
			return
		}
		if claims.UserID != 99 {
			t.Errorf("expected UserID 99, got %d", claims.UserID)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestFirebaseMiddleware_RejectsNoAuth(t *testing.T) {
	fa := NewFirebaseAuth("test-project")
	legacyAuth := New("test-secret-at-least16")
	middleware := FirebaseMiddleware(fa, legacyAuth)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}
