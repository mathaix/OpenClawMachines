package auth

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// FirebaseAuth validates Firebase Authentication ID tokens.
// It fetches Google's public JWKS to verify RS256 signatures.
type FirebaseAuth struct {
	projectID string

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	lastFetch time.Time
}

// FirebaseClaims represents the claims in a Firebase ID token.
type FirebaseClaims struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
	jwt.RegisteredClaims
}

// NewFirebaseAuth creates a new FirebaseAuth for the given GCP project ID.
func NewFirebaseAuth(projectID string) *FirebaseAuth {
	return &FirebaseAuth{
		projectID: projectID,
		keys:      make(map[string]*rsa.PublicKey),
	}
}

// ProjectID returns the configured Firebase project ID.
func (f *FirebaseAuth) ProjectID() string {
	return f.projectID
}

const firebaseJWKSURL = "https://www.googleapis.com/service_accounts/v1/jwk/securetoken@system.gserviceaccount.com"

// fetchJWKS fetches and caches Google's public keys for Firebase token verification.
func (f *FirebaseAuth) fetchJWKS() error {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(firebaseJWKSURL)
	if err != nil {
		return fmt.Errorf("fetch Firebase JWKS: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch Firebase JWKS: status %d", resp.StatusCode)
	}

	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("parse Firebase JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			slog.Warn("firebase.jwks.decode_n", "kid", k.Kid, "error", err)
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			slog.Warn("firebase.jwks.decode_e", "kid", k.Kid, "error", err)
			continue
		}
		n := new(big.Int).SetBytes(nBytes)
		e := 0
		for _, b := range eBytes {
			e = e<<8 + int(b)
		}
		keys[k.Kid] = &rsa.PublicKey{N: n, E: e}
	}

	if len(keys) == 0 {
		return fmt.Errorf("firebase JWKS contains no valid RSA keys")
	}

	f.mu.Lock()
	f.keys = keys
	f.lastFetch = time.Now()
	f.mu.Unlock()

	slog.Info("firebase.jwks.refreshed", "key_count", len(keys))
	return nil
}

// getKey retrieves a cached public key by kid, refreshing JWKS if needed.
func (f *FirebaseAuth) getKey(kid string) (*rsa.PublicKey, error) {
	f.mu.RLock()
	key, ok := f.keys[kid]
	stale := time.Since(f.lastFetch) > time.Hour
	f.mu.RUnlock()

	if ok && !stale {
		return key, nil
	}

	if err := f.fetchJWKS(); err != nil {
		if ok {
			slog.Error("firebase.jwks.refresh_failed_stale", "error", err)
			return nil, fmt.Errorf("JWKS stale and refresh failed: %w", err)
		}
		return nil, fmt.Errorf("JWKS fetch failed: %w", err)
	}

	f.mu.RLock()
	key, ok = f.keys[kid]
	f.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown kid: %s", kid)
	}
	return key, nil
}

// ValidateToken validates a Firebase ID token and returns the claims.
// It checks: RS256 signature, issuer, audience, and expiry.
func (f *FirebaseAuth) ValidateToken(tokenString string) (*FirebaseClaims, error) {
	claims := &FirebaseClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		kid, ok := t.Header["kid"].(string)
		if !ok {
			return nil, fmt.Errorf("missing kid in token header")
		}
		return f.getKey(kid)
	},
		jwt.WithIssuer(fmt.Sprintf("https://securetoken.google.com/%s", f.projectID)),
		jwt.WithAudience(f.projectID),
	)

	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	if claims.Subject == "" {
		return nil, fmt.Errorf("token missing sub claim")
	}

	return claims, nil
}
