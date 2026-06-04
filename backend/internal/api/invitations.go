package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// handleCreateInvitation creates a new invitation. Requires owner or admin role.
func (s *Server) handleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	member := accountMemberFromContext(r.Context())
	if member == nil || (member.Role != "owner" && member.Role != "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	accountID := accountIDFromContext(r.Context())
	claims := auth.UserFromContext(r.Context())

	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}
	if req.Role != "admin" && req.Role != "member" {
		writeError(w, http.StatusBadRequest, "role must be 'admin' or 'member'")
		return
	}

	// Don't allow inviting yourself
	if strings.EqualFold(req.Email, claims.Email) {
		writeError(w, http.StatusBadRequest, "cannot invite yourself")
		return
	}

	// Check if already a member
	existing, _ := s.store.GetUserByEmail(r.Context(), req.Email)
	if existing != nil {
		if _, err := s.store.GetAccountMember(r.Context(), accountID, existing.ID); err == nil {
			writeError(w, http.StatusConflict, "user is already a member of this account")
			return
		}
	}
	// Prevent duplicate pending invitations for the same account/email pair.
	invs, err := s.store.ListInvitationsByAccount(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check existing invitations")
		return
	}
	for _, inv := range invs {
		if inv.Status == "pending" && strings.EqualFold(inv.Email, req.Email) {
			writeError(w, http.StatusConflict, "a pending invitation already exists for this email")
			return
		}
	}

	inv := &store.AccountInvitation{
		AccountID: accountID,
		Email:     req.Email,
		Role:      req.Role,
		InvitedBy: claims.UserID,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}
	if err := s.store.CreateInvitation(r.Context(), inv); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create invitation")
		return
	}

	// Enqueue invitation email (best-effort, does not block response)
	account, _ := s.store.GetAccount(r.Context(), accountID)
	accountName := ""
	if account != nil {
		accountName = account.Name
	}
	s.enqueueInvitationEmail(r.Context(), invitationEmailInput{
		InvitationToken: inv.Token,
		RecipientEmail:  inv.Email,
		InviterEmail:    claims.Email,
		AccountName:     accountName,
		AccountID:       accountID,
		Role:            inv.Role,
		FrontendURL:     s.frontendURL(),
		ExpiryDays:      7,
	})

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "account",
		Action:    "member.invited",
		Status:    "success",
		ActorType: "user",
		ActorID:   &claims.UserID,
		AccountID: &accountID,
		Summary:   fmt.Sprintf("Invited %s as %s", req.Email, req.Role),
		Detail:    map[string]any{"email": req.Email, "role": req.Role},
	})

	// Return token explicitly in create response (json:"-" hides it in lists)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":         inv.ID,
		"account_id": inv.AccountID,
		"email":      inv.Email,
		"role":       inv.Role,
		"token":      inv.Token,
		"status":     "pending",
		"expires_at": inv.ExpiresAt,
		"created_at": inv.CreatedAt,
	})
}

// handleListInvitations lists invitations for an account. Requires owner or admin.
func (s *Server) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	member := accountMemberFromContext(r.Context())
	if member == nil || (member.Role != "owner" && member.Role != "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	accountID := accountIDFromContext(r.Context())
	invs, err := s.store.ListInvitationsByAccount(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list invitations")
		return
	}
	writeJSON(w, http.StatusOK, invs)
}

// handleRevokeInvitation cancels a pending invitation. Requires owner or admin.
func (s *Server) handleRevokeInvitation(w http.ResponseWriter, r *http.Request) {
	member := accountMemberFromContext(r.Context())
	if member == nil || (member.Role != "owner" && member.Role != "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	invID, err := parseIntParam(r, "invitationId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid invitation id")
		return
	}

	accountID := accountIDFromContext(r.Context())
	if err := s.store.RevokeInvitation(r.Context(), accountID, invID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke invitation")
		return
	}

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "account",
		Action:    "invitation.revoked",
		Status:    "success",
		ActorType: "user",
		ActorID:   &member.UserID,
		AccountID: &accountID,
		Summary:   "Revoked invitation",
		Detail:    map[string]any{"invitation_id": invID},
	})

	w.WriteHeader(http.StatusNoContent)
}

// handleListPendingInvitations returns pending invitations for the authenticated user's email.
func (s *Server) handleListPendingInvitations(w http.ResponseWriter, r *http.Request) {
	claims := auth.UserFromContext(r.Context())
	invs, err := s.store.ListPendingInvitationsByEmail(r.Context(), claims.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pending invitations")
		return
	}

	// Sort by CreatedAt descending so the de-dup loop keeps the newest invitation per account.
	sort.Slice(invs, func(i, j int) bool {
		return invs[i].CreatedAt.After(invs[j].CreatedAt)
	})

	filtered := make([]store.AccountInvitation, 0, len(invs))
	seenAccounts := make(map[int]bool, len(invs))
	for _, inv := range invs {
		if seenAccounts[inv.AccountID] {
			continue
		}
		seenAccounts[inv.AccountID] = true
		if _, err := s.store.GetAccountMember(r.Context(), inv.AccountID, claims.UserID); err == nil {
			continue
		}
		filtered = append(filtered, inv)
	}
	writeJSON(w, http.StatusOK, filtered)
}

// handleAcceptInvitation accepts an invitation. Email must match.
func (s *Server) handleAcceptInvitation(w http.ResponseWriter, r *http.Request) {
	claims := auth.UserFromContext(r.Context())
	token := chi.URLParam(r, "token")

	inv, err := s.store.GetInvitationByToken(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusNotFound, "invitation not found or already used")
		return
	}

	if inv.Status != "pending" {
		writeError(w, http.StatusGone, "invitation has already been "+inv.Status)
		return
	}

	if time.Now().After(inv.ExpiresAt) {
		writeError(w, http.StatusGone, "invitation has expired")
		return
	}

	// Email must match (security: prevent link-forwarding)
	if !strings.EqualFold(claims.Email, inv.Email) {
		writeError(w, http.StatusForbidden, "this invitation was sent to a different email address")
		return
	}

	// Claim the invitation atomically — must succeed before granting membership
	rows, err := s.store.AcceptInvitation(r.Context(), token, claims.UserID)
	if err != nil {
		if errors.Is(err, store.ErrAlreadyMember) {
			writeError(w, http.StatusConflict, "you are already a member of this account")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update invitation")
		return
	}
	if rows == 0 {
		// Another request already claimed or revoked this invitation
		writeError(w, http.StatusGone, "invitation is no longer available")
		return
	}

	// Refresh KV cache so the Cloudflare Worker authorizes the new member
	s.refreshAccountKVCache(r.Context(), inv.AccountID)

	invAccountID := inv.AccountID
	s.activity.Log(r.Context(), events.LogParams{
		Category:  "account",
		Action:    "member.accepted",
		Status:    "success",
		ActorType: "user",
		ActorID:   &claims.UserID,
		AccountID: &invAccountID,
		Summary:   fmt.Sprintf("Accepted invitation as %s", inv.Role),
		Detail:    map[string]any{"role": inv.Role},
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"account_id": inv.AccountID,
		"role":       inv.Role,
		"message":    "invitation accepted",
	})
}

// handleDeclineInvitation declines an invitation. Email must match.
func (s *Server) handleDeclineInvitation(w http.ResponseWriter, r *http.Request) {
	claims := auth.UserFromContext(r.Context())
	token := chi.URLParam(r, "token")

	inv, err := s.store.GetInvitationByToken(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusNotFound, "invitation not found")
		return
	}

	if inv.Status != "pending" {
		writeError(w, http.StatusGone, "invitation has already been "+inv.Status)
		return
	}

	if !strings.EqualFold(claims.Email, inv.Email) {
		writeError(w, http.StatusForbidden, "this invitation was sent to a different email address")
		return
	}

	if err := s.store.DeclineInvitation(r.Context(), token); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decline invitation")
		return
	}

	invAccountID := inv.AccountID
	s.activity.Log(r.Context(), events.LogParams{
		Category:  "account",
		Action:    "invitation.declined",
		Status:    "success",
		ActorType: "user",
		ActorID:   &claims.UserID,
		AccountID: &invAccountID,
		Summary:   "Declined invitation",
	})

	w.WriteHeader(http.StatusNoContent)
}

// handleGetInvitation returns invitation details for the accept/decline page.
func (s *Server) handleGetInvitation(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	inv, err := s.store.GetInvitationByToken(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusNotFound, "invitation not found")
		return
	}

	// Get account name for display
	account, _ := s.store.GetAccount(r.Context(), inv.AccountID)
	accountName := ""
	if account != nil {
		accountName = account.Name
	}

	// Get inviter email
	inviter, _ := s.store.GetUser(r.Context(), inv.InvitedBy)
	inviterEmail := ""
	if inviter != nil {
		inviterEmail = inviter.Email
	}

	// Redact email fields if the requester is not the invitee
	responseEmail := inv.Email
	claims := auth.UserFromContext(r.Context())
	if claims == nil || !strings.EqualFold(claims.Email, inv.Email) {
		responseEmail = ""
		inviterEmail = ""
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":            inv.ID,
		"account_id":    inv.AccountID,
		"account_name":  accountName,
		"email":         responseEmail,
		"role":          inv.Role,
		"status":        inv.Status,
		"inviter_email": inviterEmail,
		"expires_at":    inv.ExpiresAt,
	})
}

// handleGetInvitationPublic returns minimal invitation details without authentication.
// Only reveals account name, role, status, and expiry — all info already in the email.
func (s *Server) handleGetInvitationPublic(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	inv, err := s.store.GetInvitationByToken(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusNotFound, "invitation not found")
		return
	}

	account, _ := s.store.GetAccount(r.Context(), inv.AccountID)
	accountName := ""
	if account != nil {
		accountName = account.Name
	}

	inviter, _ := s.store.GetUser(r.Context(), inv.InvitedBy)
	inviterEmail := ""
	if inviter != nil {
		inviterEmail = inviter.Email
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"account_name":  accountName,
		"role":          inv.Role,
		"status":        inv.Status,
		"inviter_email": inviterEmail,
		"expires_at":    inv.ExpiresAt,
	})
}
