package api

import (
	"context"
	"log/slog"
	"time"
)

const invitationExpiryInterval = 1 * time.Hour

// StartInvitationExpiryLoop runs a background goroutine that periodically
// marks expired pending invitations as 'expired'.
func (s *Server) StartInvitationExpiryLoop(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(invitationExpiryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				expired, err := s.store.ExpireOldInvitations(ctx)
				if err != nil {
					slog.Error("invitation_expiry.failed", "error", err)
				} else if expired > 0 {
					slog.Info("invitation_expiry.completed", "expired_count", expired)
				}
			}
		}
	}()
	slog.Info("invitation_expiry.loop.started", "interval", invitationExpiryInterval)
}
