package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// composioProxyTokenIssuer distinguishes Composio proxy tokens from other JWTs
// signed with the same secret (user sessions, user-scoped machine tokens). The
// validator requires this exact issuer.
const composioProxyTokenIssuer = "ocm-composio-proxy"

// ComposioProxyClaims are the claims carried by per-machine Composio proxy
// tokens. The plugin running inside a VM presents this token to the backend
// proxy on every Composio call; the backend uses MachineID as the Composio
// user_id, ignoring any user_id supplied in the request body or query.
type ComposioProxyClaims struct {
	MachineID string `json:"machine_id"`
	jwt.RegisteredClaims
}

// IssueComposioProxyToken mints a JWT scoped to one machine for use by the
// in-VM Composio plugin. Config assembly calls this each time it renders a
// machine's config; the token lives for ttl and is refreshed by the next
// config push.
func (a *Auth) IssueComposioProxyToken(machineID string, ttl time.Duration) (string, error) {
	if a == nil || len(a.jwtSecret) == 0 {
		return "", errors.New("auth not configured")
	}
	if machineID == "" {
		return "", errors.New("machine_id is required")
	}
	now := time.Now()
	claims := &ComposioProxyClaims{
		MachineID: machineID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    composioProxyTokenIssuer,
			Subject:   machineID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(a.jwtSecret)
	if err != nil {
		return "", fmt.Errorf("sign composio proxy token: %w", err)
	}
	return signed, nil
}

// ValidateComposioProxyToken parses and validates a Composio proxy token.
// Rejects tokens with a non-HMAC signing method, mismatched issuer, expired
// exp, or missing machine_id.
func (a *Auth) ValidateComposioProxyToken(tokenString string) (*ComposioProxyClaims, error) {
	if a == nil || len(a.jwtSecret) == 0 {
		return nil, errors.New("auth not configured")
	}
	parsed, err := jwt.ParseWithClaims(tokenString, &ComposioProxyClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return a.jwtSecret, nil
	}, jwt.WithIssuer(composioProxyTokenIssuer))
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(*ComposioProxyClaims)
	if !ok || !parsed.Valid {
		return nil, errors.New("invalid composio proxy token")
	}
	if claims.MachineID == "" {
		return nil, errors.New("composio proxy token missing machine_id claim")
	}
	return claims, nil
}
