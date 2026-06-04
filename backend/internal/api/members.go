package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mathaix/openclawmachines/backend/internal/events"
)

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	members, err := s.store.ListAccountMembers(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, members)
}

// handleUpdateMemberRole changes a member's role. Owner only.
func (s *Server) handleUpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	if requireOwnerRole(w, r) {
		return
	}

	accountID := accountIDFromContext(r.Context())
	targetUserID, err := parseIntParam(r, "userId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	// Can't change own role
	member := accountMemberFromContext(r.Context())
	if member.UserID == targetUserID {
		writeError(w, http.StatusBadRequest, "cannot change your own role")
		return
	}

	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Role != "admin" && req.Role != "member" {
		writeError(w, http.StatusBadRequest, "role must be 'admin' or 'member'")
		return
	}

	// Verify target is a member
	target, err := s.store.GetAccountMember(r.Context(), accountID, targetUserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "member not found")
		return
	}
	if target.Role == "owner" {
		writeError(w, http.StatusForbidden, "cannot change the owner's role")
		return
	}

	if err := s.store.UpdateAccountMemberRole(r.Context(), accountID, targetUserID, req.Role); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update role")
		return
	}

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "account",
		Action:    "member.role_changed",
		Status:    "success",
		ActorType: "user",
		ActorID:   &member.UserID,
		AccountID: &accountID,
		Summary:   fmt.Sprintf("Changed member role to '%s'", req.Role),
		Detail:    map[string]any{"target_user_id": targetUserID, "new_role": req.Role},
	})

	writeJSON(w, http.StatusOK, map[string]string{"role": req.Role})
}

// handleRemoveMember removes a member from the account. Owner or admin.
func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	member := accountMemberFromContext(r.Context())
	if member == nil || (member.Role != "owner" && member.Role != "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	accountID := accountIDFromContext(r.Context())
	targetUserID, err := parseIntParam(r, "userId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	// Can't remove yourself via this endpoint (use /leave)
	if member.UserID == targetUserID {
		writeError(w, http.StatusBadRequest, "use the leave endpoint to remove yourself")
		return
	}

	// Check target exists and isn't the owner
	target, err := s.store.GetAccountMember(r.Context(), accountID, targetUserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "member not found")
		return
	}
	if target.Role == "owner" {
		writeError(w, http.StatusForbidden, "cannot remove the account owner")
		return
	}

	// Admin can't remove other admins
	if member.Role == "admin" && target.Role == "admin" {
		writeError(w, http.StatusForbidden, "admins cannot remove other admins")
		return
	}

	if err := s.store.RemoveAccountMember(r.Context(), accountID, targetUserID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove member")
		return
	}

	// Refresh KV cache so the Cloudflare Worker stops authorizing the removed user
	s.refreshAccountKVCache(r.Context(), accountID)

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "account",
		Action:    "member.removed",
		Status:    "success",
		ActorType: "user",
		ActorID:   &member.UserID,
		AccountID: &accountID,
		Summary:   fmt.Sprintf("Removed member (user %d)", targetUserID),
		Detail:    map[string]any{"target_user_id": targetUserID},
	})

	w.WriteHeader(http.StatusNoContent)
}

// handleLeaveAccount allows a non-owner to leave an account.
func (s *Server) handleLeaveAccount(w http.ResponseWriter, r *http.Request) {
	member := accountMemberFromContext(r.Context())
	if member == nil {
		writeError(w, http.StatusForbidden, "not a member")
		return
	}

	if member.Role == "owner" {
		writeError(w, http.StatusForbidden, "owners cannot leave — transfer ownership first")
		return
	}

	accountID := accountIDFromContext(r.Context())
	if err := s.store.RemoveAccountMember(r.Context(), accountID, member.UserID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to leave account")
		return
	}

	// Refresh KV cache so the Cloudflare Worker stops authorizing the departed user
	s.refreshAccountKVCache(r.Context(), accountID)

	w.WriteHeader(http.StatusNoContent)
}
