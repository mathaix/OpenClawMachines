package api

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/mathaix/openclawmachines/backend/internal/email"
	"github.com/mathaix/openclawmachines/backend/internal/workflows"
)

type invitationEmailInput struct {
	InvitationToken string `json:"invitation_token"`
	RecipientEmail  string `json:"recipient_email"`
	InviterEmail    string `json:"inviter_email"`
	AccountName     string `json:"account_name"`
	AccountID       int    `json:"account_id"`
	Role            string `json:"role"`
	FrontendURL     string `json:"frontend_url"`
	ExpiryDays      int    `json:"expiry_days"`
}

type invitationEmailResult struct {
	Delivered bool   `json:"delivered"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) runInvitationEmailWorkflow(ctx dbos.DBOSContext, input invitationEmailInput) (invitationEmailResult, error) {
	workflowID, err := dbos.GetWorkflowID(ctx)
	if err != nil {
		return invitationEmailResult{}, err
	}

	// Guard: if email client is not configured (e.g. replayed after restart without RESEND_API_KEY),
	// return success-with-note rather than retrying forever inside the delivery step.
	if s.emailClient == nil {
		slog.Warn("notification.invitation_email.skip", "workflow_id", workflowID, "reason", "email_client_not_configured")
		return invitationEmailResult{Error: "email client not configured"}, nil
	}

	slog.Info("notification.invitation_email.start",
		"workflow_id", workflowID,
		"recipient", input.RecipientEmail,
		"account_id", input.AccountID,
	)

	// Step 1: Render the email
	html, err := dbos.RunAsStep(ctx, func(stepCtx context.Context) (string, error) {
		return email.RenderInvitationEmail(email.InvitationData{
			InviterEmail: input.InviterEmail,
			AccountName:  input.AccountName,
			Role:         input.Role,
			AcceptURL:    fmt.Sprintf("%s/invitations/%s", input.FrontendURL, input.InvitationToken),
			ExpiryDays:   input.ExpiryDays,
		})
	}, dbos.WithStepName("notification.render_email"))
	if err != nil {
		return invitationEmailResult{Error: err.Error()}, err
	}

	// Step 2: Deliver via Resend (with retry)
	_, err = dbos.RunAsStep(ctx, func(stepCtx context.Context) (bool, error) {
		return true, s.emailClient.SendEmail(stepCtx, email.Email{
			From:    "OpenClaw Machines <noreply@openclawmachines.com>",
			To:      input.RecipientEmail,
			Subject: fmt.Sprintf("%s invited you to %s", input.InviterEmail, input.AccountName),
			HTML:    html,
		})
	},
		dbos.WithStepName("notification.deliver_email"),
		dbos.WithStepMaxRetries(5),
		dbos.WithBaseInterval(5*time.Second),
		dbos.WithBackoffFactor(3.0),
		dbos.WithMaxInterval(5*time.Minute),
	)
	if err != nil {
		slog.Error("notification.invitation_email.delivery_failed",
			"workflow_id", workflowID,
			"recipient", input.RecipientEmail,
			"error", err,
		)
		return invitationEmailResult{Error: err.Error()}, err
	}

	slog.Info("notification.invitation_email.delivered",
		"workflow_id", workflowID,
		"recipient", input.RecipientEmail,
	)
	return invitationEmailResult{Delivered: true}, nil
}

// enqueueInvitationEmail fires off the invitation email workflow.
// It is best-effort: failure to enqueue does not fail the invitation creation.
func (s *Server) enqueueInvitationEmail(ctx context.Context, input invitationEmailInput) {
	if s.workflows == nil || s.workflows.Context() == nil {
		slog.Debug("notification.skip", "reason", "workflows_not_enabled")
		return
	}
	if s.emailClient == nil {
		slog.Debug("notification.skip", "reason", "email_not_configured")
		return
	}

	workflowID, err := workflows.NewID()
	if err != nil {
		slog.Warn("notification.id_generation_failed", "error", err)
		return
	}

	// Create projection row
	accountID := input.AccountID
	if _, err := s.workflows.CreateRun(ctx, workflows.CreateRunParams{
		ID:        workflowID,
		Kind:      workflows.KindNotification,
		ScopeType: "account",
		ScopeID:   fmt.Sprintf("%d", accountID),
		AccountID: &accountID,
		Priority:  "normal",
		InputJSON: mustRawJSON(map[string]any{
			"notification_type": "invitation",
			"recipient_email":   input.RecipientEmail,
			"account_name":      input.AccountName,
		}),
	}); err != nil {
		slog.Warn("notification.create_run_failed", "workflow_id", workflowID, "error", err)
		return
	}

	if _, err := dbos.RunWorkflow(
		s.workflows.Context(),
		s.runInvitationEmailWorkflow,
		input,
		dbos.WithWorkflowID(workflowID),
		dbos.WithQueue(workflows.QueueNotifications),
	); err != nil {
		slog.Warn("notification.enqueue_failed", "workflow_id", workflowID, "error", err)
		msg := err.Error()
		_ = s.workflows.Complete(context.Background(), workflowID, workflows.StatusFailed, nil, strPtr("enqueue_failed"), &msg)
	}
}
