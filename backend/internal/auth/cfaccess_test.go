package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testTeamDomain = "test-team"
	testAudTag     = "test-audience-tag"
	testKid        = "test-kid-1"
)

// testCfAuth creates a CfAccessAuth with a pre-populated RSA key for testing.
func testCfAuth(t *testing.T) (*CfAccessAuth, *rsa.PrivateKey) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	cfAuth := NewCfAccess(testTeamDomain, testAudTag)
	cfAuth.keys[testKid] = &privateKey.PublicKey
	cfAuth.lastFetch = time.Now()

	return cfAuth, privateKey
}

// signTestJWT signs a JWT with RS256 and the test kid header.
func signTestJWT(t *testing.T, privateKey *rsa.PrivateKey, claims jwt.Claims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = testKid
	signed, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	return signed
}

func validClaims() *CfAccessClaims {
	return &CfAccessClaims{
		Email: "user@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    fmt.Sprintf("https://%s.cloudflareaccess.com", testTeamDomain),
			Audience:  jwt.ClaimStrings{testAudTag},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   "cf-user-id-123",
		},
	}
}

func TestValidateCfJWT_ValidToken(t *testing.T) {
	cfAuth, privateKey := testCfAuth(t)

	tokenStr := signTestJWT(t, privateKey, validClaims())

	claims, err := cfAuth.ValidateCfJWT(tokenStr)
	if err != nil {
		t.Fatalf("expected valid token, got error: %v", err)
	}
	if claims.Email != "user@example.com" {
		t.Errorf("email = %q, want %q", claims.Email, "user@example.com")
	}
	if claims.Subject != "cf-user-id-123" {
		t.Errorf("subject = %q, want %q", claims.Subject, "cf-user-id-123")
	}
}

func TestValidateCfJWT_ExpiredToken(t *testing.T) {
	cfAuth, privateKey := testCfAuth(t)

	c := validClaims()
	c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))

	tokenStr := signTestJWT(t, privateKey, c)

	_, err := cfAuth.ValidateCfJWT(tokenStr)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("expected expiry error, got: %v", err)
	}
}

func TestValidateCfJWT_WrongAudience(t *testing.T) {
	cfAuth, privateKey := testCfAuth(t)

	c := validClaims()
	c.Audience = jwt.ClaimStrings{"wrong-audience"}

	tokenStr := signTestJWT(t, privateKey, c)

	_, err := cfAuth.ValidateCfJWT(tokenStr)
	if err == nil {
		t.Fatal("expected error for wrong audience, got nil")
	}
	if !strings.Contains(err.Error(), "aud") {
		t.Errorf("expected audience error, got: %v", err)
	}
}

func TestValidateCfJWT_WrongIssuer(t *testing.T) {
	cfAuth, privateKey := testCfAuth(t)

	c := validClaims()
	c.Issuer = "https://wrong-team.cloudflareaccess.com"

	tokenStr := signTestJWT(t, privateKey, c)

	_, err := cfAuth.ValidateCfJWT(tokenStr)
	if err == nil {
		t.Fatal("expected error for wrong issuer, got nil")
	}
	if !strings.Contains(err.Error(), "iss") {
		t.Errorf("expected issuer error, got: %v", err)
	}
}

func TestValidateCfJWT_WrongAlgorithm(t *testing.T) {
	cfAuth, _ := testCfAuth(t)

	// Sign with HS256 instead of RS256
	c := validClaims()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	token.Header["kid"] = testKid
	tokenStr, err := token.SignedString([]byte("hmac-secret"))
	if err != nil {
		t.Fatalf("sign HS256 JWT: %v", err)
	}

	_, err = cfAuth.ValidateCfJWT(tokenStr)
	if err == nil {
		t.Fatal("expected error for wrong algorithm, got nil")
	}
	if !strings.Contains(err.Error(), "signing method") {
		t.Errorf("expected signing method error, got: %v", err)
	}
}

func TestJWKSCache_WithinTTL(t *testing.T) {
	cfAuth, privateKey := testCfAuth(t)

	// keys and lastFetch are already set by testCfAuth.
	// getKey should return the cached key without attempting FetchJWKS.
	// If FetchJWKS were called, it would fail (no real server), but
	// a cache hit means we get the key successfully.
	tokenStr := signTestJWT(t, privateKey, validClaims())

	claims, err := cfAuth.ValidateCfJWT(tokenStr)
	if err != nil {
		t.Fatalf("expected cache hit, got error: %v", err)
	}
	if claims.Email != "user@example.com" {
		t.Errorf("email = %q, want %q", claims.Email, "user@example.com")
	}
}

func TestJWKSCache_Expired(t *testing.T) {
	cfAuth, _ := testCfAuth(t)

	// Set lastFetch to 2 hours ago so the cache is stale.
	cfAuth.lastFetch = time.Now().Add(-2 * time.Hour)

	// getKey should attempt FetchJWKS because the cache is stale.
	// Even though our test key is in cache, stale cache triggers a refresh.
	// After refresh (whether it succeeds or fails), our test-kid won't
	// be returned directly from cache — proving the refresh was triggered.
	key, err := cfAuth.getKey(testKid)

	// The key should NOT be returned from stale cache directly.
	// Either: refresh succeeds but test-kid-1 isn't in real JWKS → "unknown kid",
	// or: refresh fails → "stale and refresh failed".
	if key != nil {
		t.Error("expected stale cache to NOT return key directly; refresh should have been triggered")
	}
	if err == nil {
		t.Fatal("expected error when cache is expired, got nil")
	}
}

func TestJWKSCache_StaleAndFetchFails(t *testing.T) {
	// Use a teamDomain with a null byte to ensure FetchJWKS fails immediately
	// (Go's net/http rejects URLs with control characters).
	cfAuth := NewCfAccess("test\x00invalid", testAudTag)

	// Pre-populate a key and mark the cache as stale.
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	cfAuth.keys[testKid] = &privateKey.PublicKey
	cfAuth.lastFetch = time.Now().Add(-2 * time.Hour)

	// Even though the key IS in cache, because the cache is stale and
	// FetchJWKS fails, getKey must return an error (fail closed).
	// It must NOT fall back to the stale cached key.
	key, gotErr := cfAuth.getKey(testKid)
	if gotErr == nil {
		t.Fatal("expected fail-closed error, got nil (stale key was returned)")
	}
	if key != nil {
		t.Error("expected nil key on fail-closed, got non-nil")
	}
	if !strings.Contains(gotErr.Error(), "stale") {
		t.Errorf("expected stale error, got: %v", gotErr)
	}
}

func TestMiddleware_ValidJWT(t *testing.T) {
	cfAuth, privateKey := testCfAuth(t)

	tokenStr := signTestJWT(t, privateKey, validClaims())

	var gotClaims *Claims
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", tokenStr)
	rr := httptest.NewRecorder()

	cfAuth.Middleware(next).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if gotClaims == nil {
		t.Fatal("expected claims in context, got nil")
	}
	if gotClaims.Email != "user@example.com" {
		t.Errorf("email = %q, want %q", gotClaims.Email, "user@example.com")
	}
	if gotClaims.CfSub != "cf-user-id-123" {
		t.Errorf("CfSub = %q, want %q", gotClaims.CfSub, "cf-user-id-123")
	}
}

func TestMiddleware_MissingHeader(t *testing.T) {
	cfAuth, _ := testCfAuth(t)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	// No Cf-Access-Jwt-Assertion header
	rr := httptest.NewRecorder()

	cfAuth.Middleware(next).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestMiddleware_InvalidJWT(t *testing.T) {
	cfAuth, _ := testCfAuth(t)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", "invalid.jwt.token")
	rr := httptest.NewRecorder()

	cfAuth.Middleware(next).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}
