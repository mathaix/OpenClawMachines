package auth

import (
	"context"
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

// CfAccessAuth validates Cloudflare Access JWTs.
type CfAccessAuth struct {
	teamDomain string
	audTag     string

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	lastFetch time.Time
}

// NewCfAccess creates a new CfAccessAuth for the given team domain and AUD tag.
func NewCfAccess(teamDomain, audTag string) *CfAccessAuth {
	return &CfAccessAuth{
		teamDomain: teamDomain,
		audTag:     audTag,
		keys:       make(map[string]*rsa.PublicKey),
	}
}

type jwksResponse struct {
	Keys []jwkKey `json:"keys"`
}

type jwkKey struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// FetchJWKS fetches and caches the JWKS from Cloudflare Access.
func (c *CfAccessAuth) FetchJWKS() error {
	url := fmt.Sprintf("https://%s.cloudflareaccess.com/cdn-cgi/access/certs", c.teamDomain)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("fetch JWKS: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch JWKS: status %d", resp.StatusCode)
	}

	var jwks jwksResponse
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("parse JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pub, err := parseRSAPublicKey(k.N, k.E)
		if err != nil {
			slog.Warn("cfaccess.jwks.parse_key_failed", "kid", k.Kid, "error", err)
			continue
		}
		keys[k.Kid] = pub
	}

	if len(keys) == 0 {
		return fmt.Errorf("JWKS contains no valid RSA keys")
	}

	c.mu.Lock()
	c.keys = keys
	c.lastFetch = time.Now()
	c.mu.Unlock()

	slog.Info("cfaccess.jwks.refreshed", "key_count", len(keys))
	return nil
}

func parseRSAPublicKey(nBase64, eBase64 string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nBase64)
	if err != nil {
		return nil, fmt.Errorf("decode n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eBase64)
	if err != nil {
		return nil, fmt.Errorf("decode e: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	e := 0
	for _, b := range eBytes {
		e = e<<8 + int(b)
	}

	return &rsa.PublicKey{N: n, E: e}, nil
}

// getKey retrieves a cached public key by kid, refreshing JWKS if needed.
func (c *CfAccessAuth) getKey(kid string) (*rsa.PublicKey, error) {
	c.mu.RLock()
	key, ok := c.keys[kid]
	stale := time.Since(c.lastFetch) > time.Hour
	c.mu.RUnlock()

	if ok && !stale {
		return key, nil
	}

	// Refresh JWKS
	if err := c.FetchJWKS(); err != nil {
		// Fail closed: stale JWKS + fetch failure = reject all
		if ok {
			slog.Error("cfaccess.jwks.refresh_failed_stale", "error", err)
			return nil, fmt.Errorf("JWKS stale and refresh failed: %w", err)
		}
		return nil, fmt.Errorf("JWKS fetch failed: %w", err)
	}

	c.mu.RLock()
	key, ok = c.keys[kid]
	c.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown kid: %s", kid)
	}
	return key, nil
}

// CfAccessClaims represents the claims in a Cloudflare Access JWT.
type CfAccessClaims struct {
	Email string `json:"email"`
	jwt.RegisteredClaims
}

// ValidateCfJWT validates a Cloudflare Access JWT and returns the claims.
func (c *CfAccessAuth) ValidateCfJWT(tokenString string) (*CfAccessClaims, error) {
	claims := &CfAccessClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		kid, ok := t.Header["kid"].(string)
		if !ok {
			return nil, fmt.Errorf("missing kid in token header")
		}
		return c.getKey(kid)
	},
		jwt.WithIssuer(fmt.Sprintf("https://%s.cloudflareaccess.com", c.teamDomain)),
		jwt.WithAudience(c.audTag),
	)

	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	return claims, nil
}

// Middleware returns an HTTP middleware that validates Cloudflare Access JWTs.
func (c *CfAccessAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfJWT := r.Header.Get("Cf-Access-Jwt-Assertion")
		if cfJWT == "" {
			// Fallback: read from CF_Authorization cookie (same JWT, set by CF Access on parent domain)
			if cookie, err := r.Cookie("CF_Authorization"); err == nil {
				cfJWT = cookie.Value
			}
		}
		if cfJWT == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		cfClaims, err := c.ValidateCfJWT(cfJWT)
		if err != nil {
			slog.Warn("cfaccess.jwt.invalid", "error", err, "path", r.URL.Path)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		claims := &Claims{
			Email: cfClaims.Email,
			CfSub: cfClaims.Subject,
		}

		ctx := context.WithValue(r.Context(), userKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
