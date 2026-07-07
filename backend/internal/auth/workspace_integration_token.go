package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const workspaceIntegrationTokenIssuer = "ocm-workspace-integrations"
const minWorkspaceIntegrationJWTSecretBytes = 16

// WorkspaceIntegrationClaims are carried by per-machine tokens used by the
// native OCM workspace integrations MCP server and short-lived compatibility
// bridge. The backend derives workspace scope from MachineID and ignores
// caller-supplied machine/workspace identifiers.
type WorkspaceIntegrationClaims struct {
	MachineID        string `json:"machine_id"`
	IssuedAtUnixNano int64  `json:"iat_ns"`
	jwt.RegisteredClaims
}

func (a *Auth) IssueWorkspaceIntegrationToken(machineID string, ttl time.Duration) (string, error) {
	if err := a.validateWorkspaceIntegrationSecret(); err != nil {
		return "", err
	}
	if machineID == "" {
		return "", errors.New("machine_id is required")
	}
	if ttl <= 0 {
		return "", errors.New("ttl must be positive")
	}
	tokenID, err := randomWorkspaceIntegrationTokenID()
	if err != nil {
		return "", err
	}
	now := time.Now()
	claims := &WorkspaceIntegrationClaims{
		MachineID:        machineID,
		IssuedAtUnixNano: now.UnixNano(),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    workspaceIntegrationTokenIssuer,
			Subject:   machineID,
			ID:        tokenID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(a.jwtSecret)
	if err != nil {
		return "", fmt.Errorf("sign workspace integration token: %w", err)
	}
	return signed, nil
}

func (a *Auth) ValidateWorkspaceIntegrationToken(tokenString string) (*WorkspaceIntegrationClaims, error) {
	if err := a.validateWorkspaceIntegrationSecret(); err != nil {
		return nil, err
	}
	parsed, err := jwt.ParseWithClaims(tokenString, &WorkspaceIntegrationClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return a.jwtSecret, nil
	}, jwt.WithIssuer(workspaceIntegrationTokenIssuer))
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(*WorkspaceIntegrationClaims)
	if !ok || !parsed.Valid {
		return nil, errors.New("invalid workspace integration token")
	}
	if claims.MachineID == "" {
		return nil, errors.New("workspace integration token missing machine_id claim")
	}
	if claims.ID == "" {
		return nil, errors.New("workspace integration token missing jti claim")
	}
	if claims.IssuedAt == nil {
		return nil, errors.New("workspace integration token missing iat claim")
	}
	if claims.IssuedAtUnixNano <= 0 {
		return nil, errors.New("workspace integration token missing high precision issued-at claim")
	}
	return claims, nil
}

func (a *Auth) validateWorkspaceIntegrationSecret() error {
	if a == nil || len(a.jwtSecret) == 0 {
		return errors.New("auth not configured")
	}
	if len(a.jwtSecret) < minWorkspaceIntegrationJWTSecretBytes {
		return errors.New("workspace integration jwt secret is too weak")
	}
	return nil
}

func randomWorkspaceIntegrationTokenID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate workspace integration token id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
