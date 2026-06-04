package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// MachineTokenClaims contains claims for per-machine access tokens.
type MachineTokenClaims struct {
	UserID    int      `json:"user_id"`
	Email     string   `json:"email"`
	MachineID string   `json:"machine_id"`
	AccountID int      `json:"account_id"`
	Scopes    []string `json:"scopes"`
	jwt.RegisteredClaims
}

// IssueMachineToken creates a short-lived HS256 JWT for accessing a specific machine.
func IssueMachineToken(userID int, email, machineID string, accountID int, scopes []string, signingKey string) (string, time.Time, error) {
	expiresAt := time.Now().Add(5 * time.Minute)
	claims := &MachineTokenClaims{
		UserID:    userID,
		Email:     email,
		MachineID: machineID,
		AccountID: accountID,
		Scopes:    scopes,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   fmt.Sprintf("user:%d", userID),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(signingKey))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign machine token: %w", err)
	}
	return signed, expiresAt, nil
}

// ValidateMachineToken validates a machine access token and checks the machine ID matches.
func ValidateMachineToken(tokenString, signingKey, expectedMachineID string) (*MachineTokenClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &MachineTokenClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(signingKey), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*MachineTokenClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	if claims.MachineID != expectedMachineID {
		return nil, fmt.Errorf("machine ID mismatch: got %s, expected %s", claims.MachineID, expectedMachineID)
	}
	return claims, nil
}

// HasScope checks if the claims include a specific scope.
func (c *MachineTokenClaims) HasScope(scope string) bool {
	for _, s := range c.Scopes {
		if s == scope || s == "all" {
			return true
		}
	}
	return false
}
