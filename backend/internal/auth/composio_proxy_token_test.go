package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const composioTestSecret = "test-secret-at-least-16-bytes-long"

func TestIssueComposioProxyToken_RoundTrip(t *testing.T) {
	a := New(composioTestSecret)

	token, err := a.IssueComposioProxyToken("machine-abc", 30*time.Minute)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}

	claims, err := a.ValidateComposioProxyToken(token)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if claims.MachineID != "machine-abc" {
		t.Errorf("machine_id roundtrip mismatch: got %q want %q", claims.MachineID, "machine-abc")
	}
	if claims.Issuer != composioProxyTokenIssuer {
		t.Errorf("issuer = %q, want %q", claims.Issuer, composioProxyTokenIssuer)
	}
}

func TestValidateComposioProxyToken_EmptyMachineID(t *testing.T) {
	a := New(composioTestSecret)

	if _, err := a.IssueComposioProxyToken("", time.Minute); err == nil {
		t.Fatal("expected error for empty machine ID, got nil")
	}
}

func TestValidateComposioProxyToken_Expired(t *testing.T) {
	a := New(composioTestSecret)

	// Issue with a negative TTL to get an already-expired token.
	token, err := a.IssueComposioProxyToken("machine-abc", -1*time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	_, err = a.ValidateComposioProxyToken(token)
	if err == nil {
		t.Fatal("expected validation error for expired token, got nil")
	}
	if !strings.Contains(err.Error(), "expired") && !strings.Contains(err.Error(), "exp") {
		t.Errorf("expected expiry-related error, got: %v", err)
	}
}

func TestValidateComposioProxyToken_BadSignature(t *testing.T) {
	a := New(composioTestSecret)
	token, err := a.IssueComposioProxyToken("machine-abc", time.Minute)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	other := New("different-secret-also-long-enough")
	if _, err := other.ValidateComposioProxyToken(token); err == nil {
		t.Fatal("expected validation error for token signed with different secret, got nil")
	}
}

func TestValidateComposioProxyToken_RejectsUserSessionToken(t *testing.T) {
	// A token issued by GenerateToken (user session) must not validate as a
	// composio proxy token, even though it's signed with the same key. The
	// issuer claim guards this.
	a := New(composioTestSecret)
	userToken, err := a.GenerateToken(42, "user@example.com")
	if err != nil {
		t.Fatalf("generate user token: %v", err)
	}

	if _, err := a.ValidateComposioProxyToken(userToken); err == nil {
		t.Fatal("expected validation error for user session token, got nil")
	}
}

func TestValidateComposioProxyToken_RejectsNonHMAC(t *testing.T) {
	// Craft an RS256-signed token and ensure the validator rejects it.
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa keygen: %v", err)
	}
	claims := &ComposioProxyClaims{
		MachineID: "machine-abc",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    composioProxyTokenIssuer,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   "machine-abc",
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(rsaKey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	a := New(composioTestSecret)
	if _, err := a.ValidateComposioProxyToken(signed); err == nil {
		t.Fatal("expected validation error for RS256-signed token, got nil")
	}
}

func TestValidateComposioProxyToken_Malformed(t *testing.T) {
	a := New(composioTestSecret)
	for _, bad := range []string{"", "not-a-jwt", "aaaa.bbbb.cccc"} {
		if _, err := a.ValidateComposioProxyToken(bad); err == nil {
			t.Errorf("expected error for %q, got nil", bad)
		}
	}
}
