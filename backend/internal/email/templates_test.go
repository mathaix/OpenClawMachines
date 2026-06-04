package email

import (
	"strings"
	"testing"
)

func TestRenderInvitationEmail(t *testing.T) {
	html, err := RenderInvitationEmail(InvitationData{
		InviterEmail: "bob@example.com",
		AccountName:  "My Team",
		Role:         "member",
		AcceptURL:    "https://openclawmachines.com/invitations/inv_abc123",
		ExpiryDays:   7,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(html, "bob@example.com") {
		t.Error("expected inviter email in output")
	}
	if !strings.Contains(html, "My Team") {
		t.Error("expected account name in output")
	}
	if !strings.Contains(html, "inv_abc123") {
		t.Error("expected accept link in output")
	}
	if !strings.Contains(html, "7 days") {
		t.Error("expected expiry in output")
	}
}
