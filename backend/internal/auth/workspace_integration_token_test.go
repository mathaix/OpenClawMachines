package auth

import (
	"testing"
	"time"
)

func TestWorkspaceIntegrationTokenRoundTrip(t *testing.T) {
	a := New("workspace-token-secret-at-least-16")
	token, err := a.IssueWorkspaceIntegrationToken("machine-123", time.Minute)
	if err != nil {
		t.Fatalf("IssueWorkspaceIntegrationToken: %v", err)
	}
	claims, err := a.ValidateWorkspaceIntegrationToken(token)
	if err != nil {
		t.Fatalf("ValidateWorkspaceIntegrationToken: %v", err)
	}
	if claims.MachineID != "machine-123" {
		t.Fatalf("MachineID = %q, want machine-123", claims.MachineID)
	}
	if claims.ID == "" {
		t.Fatal("expected jti claim")
	}
	if claims.IssuedAt == nil {
		t.Fatal("expected iat claim")
	}
	if claims.IssuedAtUnixNano <= 0 {
		t.Fatal("expected high precision issued-at claim")
	}
}

func TestWorkspaceIntegrationTokenRejectsComposioIssuer(t *testing.T) {
	a := New("workspace-token-secret-at-least-16")
	token, err := a.IssueComposioProxyToken("machine-123", time.Minute)
	if err != nil {
		t.Fatalf("IssueComposioProxyToken: %v", err)
	}
	if _, err := a.ValidateWorkspaceIntegrationToken(token); err == nil {
		t.Fatal("expected workspace integration validation to reject composio token")
	}
}

func TestWorkspaceIntegrationTokenRejectsWeakSecret(t *testing.T) {
	a := New("short")
	if _, err := a.IssueWorkspaceIntegrationToken("machine-123", time.Minute); err == nil {
		t.Fatal("expected weak secret to reject token issuance")
	}

	strong := New("workspace-token-secret-at-least-16")
	token, err := strong.IssueWorkspaceIntegrationToken("machine-123", time.Minute)
	if err != nil {
		t.Fatalf("IssueWorkspaceIntegrationToken: %v", err)
	}
	if _, err := a.ValidateWorkspaceIntegrationToken(token); err == nil {
		t.Fatal("expected weak secret to reject token validation")
	}
}
