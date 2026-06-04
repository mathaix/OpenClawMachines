package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestIssueMachineToken_ValidRoundTrip(t *testing.T) {
	key := "test-signing-key-32bytes-long!!"
	machineID := "machine-123"

	token, expiresAt, err := IssueMachineToken(42, "user@test.com", machineID, 1, []string{"terminal", "gateway"}, key)
	if err != nil {
		t.Fatalf("IssueMachineToken failed: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}
	if expiresAt.Before(time.Now()) {
		t.Fatal("expected future expiry")
	}

	claims, err := ValidateMachineToken(token, key, machineID)
	if err != nil {
		t.Fatalf("ValidateMachineToken failed: %v", err)
	}
	if claims.UserID != 42 {
		t.Errorf("UserID = %d, want 42", claims.UserID)
	}
	if claims.Email != "user@test.com" {
		t.Errorf("Email = %q, want %q", claims.Email, "user@test.com")
	}
	if claims.MachineID != machineID {
		t.Errorf("MachineID = %q, want %q", claims.MachineID, machineID)
	}
	if claims.AccountID != 1 {
		t.Errorf("AccountID = %d, want 1", claims.AccountID)
	}
}

func TestValidateMachineToken_WrongKey(t *testing.T) {
	token, _, err := IssueMachineToken(1, "a@b.com", "m1", 1, []string{"all"}, "correct-key-1234567890123456")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	_, err = ValidateMachineToken(token, "wrong-key-12345678901234567", "m1")
	if err == nil {
		t.Fatal("expected error for wrong key")
	}
}

func TestValidateMachineToken_WrongMachineID(t *testing.T) {
	key := "test-key-12345678901234567890"
	token, _, err := IssueMachineToken(1, "a@b.com", "machine-A", 1, []string{"all"}, key)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	_, err = ValidateMachineToken(token, key, "machine-B")
	if err == nil {
		t.Fatal("expected error for wrong machine ID")
	}
}

func TestValidateMachineToken_Expired(t *testing.T) {
	key := "test-key-12345678901234567890"
	machineID := "m1"

	// Create an expired token manually
	claims := &MachineTokenClaims{
		UserID:    1,
		Email:     "a@b.com",
		MachineID: machineID,
		AccountID: 1,
		Scopes:    []string{"all"},
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			Subject:   "user:1",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(key))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	_, err = ValidateMachineToken(signed, key, machineID)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestMachineTokenClaims_HasScope(t *testing.T) {
	key := "test-key-12345678901234567890"
	token, _, err := IssueMachineToken(1, "a@b.com", "m1", 1, []string{"terminal", "gateway"}, key)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	claims, err := ValidateMachineToken(token, key, "m1")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	if !claims.HasScope("terminal") {
		t.Error("expected HasScope(terminal) = true")
	}
	if !claims.HasScope("gateway") {
		t.Error("expected HasScope(gateway) = true")
	}
}

func TestMachineTokenClaims_HasScope_Missing(t *testing.T) {
	key := "test-key-12345678901234567890"
	token, _, err := IssueMachineToken(1, "a@b.com", "m1", 1, []string{"terminal"}, key)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	claims, err := ValidateMachineToken(token, key, "m1")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	if claims.HasScope("gateway") {
		t.Error("expected HasScope(gateway) = false for token with only terminal scope")
	}
}

func TestMachineTokenClaims_HasScope_All(t *testing.T) {
	key := "test-key-12345678901234567890"
	token, _, err := IssueMachineToken(1, "a@b.com", "m1", 1, []string{"all"}, key)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	claims, err := ValidateMachineToken(token, key, "m1")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	if !claims.HasScope("terminal") {
		t.Error("expected HasScope(terminal) = true with 'all' scope")
	}
	if !claims.HasScope("gateway") {
		t.Error("expected HasScope(gateway) = true with 'all' scope")
	}
	if !claims.HasScope("anything") {
		t.Error("expected HasScope(anything) = true with 'all' scope")
	}
}
