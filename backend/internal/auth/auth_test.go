package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// testCfSetup creates a CfAccessAuth with a test RSA key pre-loaded (no JWKS fetch needed).
func testCfSetup(t *testing.T) (*CfAccessAuth, *rsa.PrivateKey) {
	t.Helper()
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	cfAuth := NewCfAccess("test-team", "test-aud")
	cfAuth.keys["test-kid"] = &privKey.PublicKey
	cfAuth.lastFetch = time.Now()

	return cfAuth, privKey
}

// signCfJWT creates a CF Access-style RS256 JWT.
func signCfJWT(privKey *rsa.PrivateKey, kid, email, iss, aud string, exp time.Time) string {
	claims := &CfAccessClaims{
		Email: email,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    iss,
			Audience:  jwt.ClaimStrings{aud},
			Subject:   "cf-sub-123",
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(privKey)
	if err != nil {
		panic("sign JWT: " + err.Error())
	}
	return signed
}

func TestDualMode_CfJWTValid(t *testing.T) {
	cfAuth, privKey := testCfSetup(t)
	legacyAuth := New("legacy-secret-key-1234567890")

	tokenStr := signCfJWT(privKey, "test-kid", "cf@test.com",
		"https://test-team.cloudflareaccess.com", "test-aud",
		time.Now().Add(5*time.Minute))

	middleware := DualModeMiddleware(cfAuth, legacyAuth)

	var gotClaims *Claims
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if gotClaims == nil {
		t.Fatal("expected claims in context")
	}
	if gotClaims.Email != "cf@test.com" {
		t.Errorf("Email = %q, want %q", gotClaims.Email, "cf@test.com")
	}
	if gotClaims.CfSub != "cf-sub-123" {
		t.Errorf("CfSub = %q, want %q", gotClaims.CfSub, "cf-sub-123")
	}
}

func TestDualMode_CfJWTInvalid_NoFallback(t *testing.T) {
	cfAuth, _ := testCfSetup(t)
	legacyAuth := New("legacy-secret-key-1234567890")

	middleware := DualModeMiddleware(cfAuth, legacyAuth)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called on invalid CF JWT")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", "invalid-jwt-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestDualMode_NoCfJWT_ValidBearer(t *testing.T) {
	cfAuth, _ := testCfSetup(t)
	legacyAuth := New("legacy-secret-key-1234567890")

	legacyToken, err := legacyAuth.GenerateToken(42, "legacy@test.com")
	if err != nil {
		t.Fatalf("generate legacy token: %v", err)
	}

	middleware := DualModeMiddleware(cfAuth, legacyAuth)

	var gotClaims *Claims
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("Authorization", "Bearer "+legacyToken)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if gotClaims == nil {
		t.Fatal("expected claims in context")
	}
	if gotClaims.UserID != 42 {
		t.Errorf("UserID = %d, want 42", gotClaims.UserID)
	}
	if gotClaims.Email != "legacy@test.com" {
		t.Errorf("Email = %q, want %q", gotClaims.Email, "legacy@test.com")
	}
}

func TestDualMode_NoCfJWT_ValidCookie(t *testing.T) {
	cfAuth, _ := testCfSetup(t)
	legacyAuth := New("legacy-secret-key-1234567890")

	legacyToken, err := legacyAuth.GenerateToken(99, "cookie@test.com")
	if err != nil {
		t.Fatalf("generate legacy token: %v", err)
	}

	middleware := DualModeMiddleware(cfAuth, legacyAuth)

	var gotClaims *Claims
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.AddCookie(&http.Cookie{Name: "ocm_token", Value: legacyToken})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if gotClaims == nil {
		t.Fatal("expected claims in context")
	}
	if gotClaims.UserID != 99 {
		t.Errorf("UserID = %d, want 99", gotClaims.UserID)
	}
}

func TestDualMode_NoCfJWT_InvalidToken(t *testing.T) {
	cfAuth, _ := testCfSetup(t)
	legacyAuth := New("legacy-secret-key-1234567890")

	middleware := DualModeMiddleware(cfAuth, legacyAuth)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with invalid token")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("Authorization", "Bearer invalid-legacy-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestDualMode_XCfAccessJwt_Valid(t *testing.T) {
	cfAuth, privKey := testCfSetup(t)
	legacyAuth := New("legacy-secret-key-1234567890")

	tokenStr := signCfJWT(privKey, "test-kid", "xcf@test.com",
		"https://test-team.cloudflareaccess.com", "test-aud",
		time.Now().Add(5*time.Minute))

	middleware := DualModeMiddleware(cfAuth, legacyAuth)

	var gotClaims *Claims
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("X-Cf-Access-Jwt", tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if gotClaims == nil {
		t.Fatal("expected claims in context")
	}
	if gotClaims.Email != "xcf@test.com" {
		t.Errorf("Email = %q, want %q", gotClaims.Email, "xcf@test.com")
	}
	if gotClaims.CfSub != "cf-sub-123" {
		t.Errorf("CfSub = %q, want %q", gotClaims.CfSub, "cf-sub-123")
	}
}

func TestDualMode_XCfAccessJwt_Invalid(t *testing.T) {
	cfAuth, _ := testCfSetup(t)
	legacyAuth := New("legacy-secret-key-1234567890")

	middleware := DualModeMiddleware(cfAuth, legacyAuth)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called on invalid X-Cf-Access-Jwt")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("X-Cf-Access-Jwt", "invalid-jwt")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestDualMode_XCfAccessJwt_Precedence(t *testing.T) {
	cfAuth, privKey := testCfSetup(t)
	legacyAuth := New("legacy-secret-key-1234567890")

	validToken := signCfJWT(privKey, "test-kid", "native@test.com",
		"https://test-team.cloudflareaccess.com", "test-aud",
		time.Now().Add(5*time.Minute))

	middleware := DualModeMiddleware(cfAuth, legacyAuth)

	var gotClaims *Claims
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", validToken)
	req.Header.Set("X-Cf-Access-Jwt", "invalid-jwt")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (native header should win)", rr.Code)
	}
	if gotClaims == nil {
		t.Fatal("expected claims in context")
	}
	if gotClaims.Email != "native@test.com" {
		t.Errorf("Email = %q, want %q (native header should take precedence)", gotClaims.Email, "native@test.com")
	}
}

func TestDualMode_NoCfJWT_NoToken(t *testing.T) {
	cfAuth, _ := testCfSetup(t)
	legacyAuth := New("legacy-secret-key-1234567890")

	middleware := DualModeMiddleware(cfAuth, legacyAuth)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with no token")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}
