package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/kvstore"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// ---- Account handlers ----

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	claims := auth.UserFromContext(r.Context())
	accounts, err := s.store.ListAccountsByUser(r.Context(), claims.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, accounts)
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	claims := auth.UserFromContext(r.Context())

	var req struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !isValidSlug(req.Slug) {
		writeError(w, http.StatusBadRequest, "invalid slug: must be 2-63 lowercase alphanumeric characters or hyphens, cannot start or end with a hyphen")
		return
	}

	account := &store.Account{
		Name:      req.Name,
		Slug:      req.Slug,
		Plan:      "free",
		CreatedBy: claims.UserID,
	}
	if err := s.store.CreateAccountWithOwner(r.Context(), account, claims.UserID); err != nil {
		if store.IsConflict(err) {
			writeError(w, http.StatusConflict, "an account with this slug already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create account")
		return
	}

	if s.kvStore != nil {
		if err := s.kvStore.PutAccount(r.Context(), account.Slug, kvstore.AccountEntry{
			AccountID: account.ID,
			UserIDs:   []int{claims.UserID},
		}); err != nil {
			slog.Warn("account.create.kv_sync_failed", "account_id", account.ID, "slug", account.Slug, "error", err)
		}
	}

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "account",
		Action:    "account.created",
		Status:    "success",
		ActorType: "user",
		ActorID:   &claims.UserID,
		AccountID: &account.ID,
		Summary:   fmt.Sprintf("Created account '%s'", account.Name),
	})

	writeJSON(w, http.StatusCreated, account)
}

// refreshAccountKVCache refreshes the KV cache entry for an account with the
// current member list. Called after membership changes (accept invitation,
// remove member, leave account) to ensure the Cloudflare Worker's auth cache
// stays in sync.
func (s *Server) refreshAccountKVCache(ctx context.Context, accountID int) {
	if s.kvStore == nil {
		return
	}
	account, err := s.store.GetAccount(ctx, accountID)
	if err != nil {
		slog.Warn("refreshAccountKVCache: failed to get account", "account_id", accountID, "error", err)
		return
	}
	members, err := s.store.ListAccountMembers(ctx, accountID)
	if err != nil {
		slog.Warn("refreshAccountKVCache: failed to list members", "account_id", accountID, "error", err)
		return
	}
	userIDs := make([]int, len(members))
	for i, m := range members {
		userIDs[i] = m.UserID
	}
	if err := s.kvStore.PutAccount(ctx, account.Slug, kvstore.AccountEntry{
		AccountID: account.ID,
		UserIDs:   userIDs,
	}); err != nil {
		slog.Warn("refreshAccountKVCache: failed to update KV", "account_id", accountID, "slug", account.Slug, "error", err)
	}
}

func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	account, err := s.store.GetAccount(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}
	writeJSON(w, http.StatusOK, account)
}

func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	member := accountMemberFromContext(r.Context())
	if member.Role != "owner" {
		writeError(w, http.StatusForbidden, "only account owners can update account settings")
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 100 {
		writeError(w, http.StatusBadRequest, "name must be 1-100 characters")
		return
	}

	if err := s.store.UpdateAccountName(r.Context(), accountID, req.Name); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update account")
		return
	}

	account, err := s.store.GetAccount(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch updated account")
		return
	}

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "account",
		Action:    "account.updated",
		Status:    "success",
		ActorType: "user",
		ActorID:   &member.UserID,
		AccountID: &accountID,
		Summary:   fmt.Sprintf("Renamed account to '%s'", req.Name),
	})

	writeJSON(w, http.StatusOK, account)
}
