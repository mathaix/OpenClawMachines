package api

import (
	"strings"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/auth"
)

// TestSignComposioProxyToken_NoSignerErrors verifies that when the signer
// hasn't been wired (worker mode without SetComposioProxyTokenSigner, dev
// mode without JWT_SECRET), config assembly fails loudly rather than
// silently producing a no-auth config. Silent fallback would reintroduce
// the IDOR the bearer token fix exists to prevent.
func TestSignComposioProxyToken_NoSignerErrors(t *testing.T) {
	s := &Server{}
	_, err := s.signComposioProxyToken("machine-abc")
	if err == nil {
		t.Fatal("expected error when no signer is configured, got nil")
	}
	if !strings.Contains(err.Error(), "signer not configured") {
		t.Errorf("expected 'signer not configured' in error, got: %v", err)
	}
}

// TestSignComposioProxyToken_WithSigner_ProducesValidToken verifies that
// when the signer is wired (the normal main.go path), the resulting token
// validates against the same secret. This also documents that the signer
// and validator are decoupled — the signer lives in the closure set via
// SetComposioProxyTokenSigner, and validation goes through s.auth.
func TestSignComposioProxyToken_WithSigner_ProducesValidToken(t *testing.T) {
	const secret = "test-secret-at-least-16-bytes-long"
	a := auth.New(secret)
	s := &Server{auth: a}
	s.SetComposioProxyTokenSigner(func(machineID string) (string, error) {
		return a.IssueComposioProxyToken(machineID, time.Minute)
	})

	token, err := s.signComposioProxyToken("machine-abc")
	if err != nil {
		t.Fatalf("signComposioProxyToken: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}

	claims, err := s.auth.ValidateComposioProxyToken(token)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if claims.MachineID != "machine-abc" {
		t.Errorf("machine_id = %q, want %q", claims.MachineID, "machine-abc")
	}
}

// TestSignComposioProxyToken_SignerErrorPropagates verifies that a signer
// that fails (e.g. JWT secret rotated mid-flight) surfaces an error rather
// than being swallowed.
func TestSignComposioProxyToken_SignerErrorPropagates(t *testing.T) {
	s := &Server{}
	s.SetComposioProxyTokenSigner(func(machineID string) (string, error) {
		return "", &signerErr{msg: "simulated failure"}
	})
	_, err := s.signComposioProxyToken("machine-abc")
	if err == nil {
		t.Fatal("expected error to propagate, got nil")
	}
	if !strings.Contains(err.Error(), "simulated failure") {
		t.Errorf("expected 'simulated failure' in error, got: %v", err)
	}
}

type signerErr struct{ msg string }

func (e *signerErr) Error() string { return e.msg }
