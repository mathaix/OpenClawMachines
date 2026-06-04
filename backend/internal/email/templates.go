package email

import (
	"bytes"
	"fmt"
	"html/template"
)

type InvitationData struct {
	InviterEmail string
	AccountName  string
	Role         string
	AcceptURL    string
	ExpiryDays   int
}

var invitationTemplate = template.Must(template.New("invitation").Parse(`<!DOCTYPE html>
<html>
<head><meta charset="utf-8"></head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; max-width: 600px; margin: 0 auto; padding: 20px;">
  <h2 style="color: #1a1a1a;">You've been invited to {{.AccountName}}</h2>
  <p style="color: #4a4a4a; font-size: 16px;">
    <strong>{{.InviterEmail}}</strong> has invited you to join
    <strong>{{.AccountName}}</strong> as a <strong>{{.Role}}</strong> on OpenClaw Machines.
  </p>
  <p style="margin: 30px 0;">
    <a href="{{.AcceptURL}}"
       style="background-color: #2563eb; color: white; padding: 12px 24px; text-decoration: none; border-radius: 6px; font-weight: 600;">
      Accept Invitation
    </a>
  </p>
  <p style="color: #6b7280; font-size: 14px;">
    This invitation expires in {{.ExpiryDays}} days. If you didn't expect this, you can ignore it.
  </p>
</body>
</html>`))

func RenderInvitationEmail(data InvitationData) (string, error) {
	var buf bytes.Buffer
	if err := invitationTemplate.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render invitation email: %w", err)
	}
	return buf.String(), nil
}
