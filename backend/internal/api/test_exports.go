//go:build integration_db

package api

import (
	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// InvitationEmailInput is the exported version of invitationEmailInput for integration tests.
type InvitationEmailInput = invitationEmailInput

// InvitationEmailResult is the exported version of invitationEmailResult for integration tests.
type InvitationEmailResult = invitationEmailResult

// RunInvitationEmailWorkflowForTest exposes the invitation email workflow function
// for integration testing. This is a typed alias that DBOS can call directly.
func (s *Server) RunInvitationEmailWorkflowForTest(ctx dbos.DBOSContext, input invitationEmailInput) (invitationEmailResult, error) {
	return s.runInvitationEmailWorkflow(ctx, input)
}

// RegisterWorkflowsForTest registers workflow functions using the exported test
// wrappers so that integration tests can call RunWorkflow with the same functions.
// Workflows are registered with the same names as production.
func (s *Server) RegisterWorkflowsForTest(dbosCtx dbos.DBOSContext) {
	if s == nil {
		return
	}
	dbos.RegisterWorkflow(dbosCtx, s.RunInvitationEmailWorkflowForTest, dbos.WithWorkflowName("api.invitation_email"))
}
