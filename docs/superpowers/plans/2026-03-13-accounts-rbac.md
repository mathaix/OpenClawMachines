# Accounts & RBAC Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add invitation-based team membership, role-based authorization, and account switching so multiple users can collaborate under shared accounts.

**Architecture:** New `account_invitations` table + `InvitationRepo` for invite CRUD. `requireRole()` middleware layers on top of existing `AccountMiddleware` for role checks. Frontend gets an account switcher in the header, a Members tab in Settings, and an invitation accept page.

**Tech Stack:** Go 1.25 / Chi router / pgx v5 (backend), React 18 / TypeScript / Tailwind (frontend)

**Spec:** `docs/CurrentFeature.md`

---

## File Structure

### Backend — New Files
| File | Responsibility |
|------|---------------|
| `backend/migrations/033_account_invitations.sql` | Invitation table DDL |
| `backend/internal/api/invitations.go` | Invitation HTTP handlers |
| `backend/internal/api/invitations_test.go` | Invitation handler tests |
| `backend/internal/api/members.go` | Member management handlers (role change, remove, leave) |
| `backend/internal/api/members_test.go` | Member handler tests |

### Backend — Modified Files
| File | Changes |
|------|---------|
| `backend/internal/store/store.go` | Add `AccountInvitation` struct, `InvitationRepo` interface, `UpdateAccountMemberRole` to `AccountRepo` |
| `backend/internal/store/postgres.go` | Implement `InvitationRepo` + `UpdateAccountMemberRole` |
| `backend/internal/api/server.go` | Add `requireRole()` middleware, register new routes, apply role checks to existing destructive routes |

### Frontend — New Files
| File | Responsibility |
|------|---------------|
| `frontend/src/components/MembersTab.tsx` | Members list + invite dialog + role management |
| `frontend/src/components/AccountSwitcher.tsx` | Dropdown account selector for header |
| `frontend/src/pages/InvitationAccept.tsx` | Accept/decline invitation page |

### Frontend — Modified Files
| File | Changes |
|------|---------|
| `frontend/src/lib/types.ts` | Add `AccountInvitation` interface |
| `frontend/src/lib/api.ts` | Add invitation + member API functions |
| `frontend/src/lib/auth.tsx` | Add `setAccount()`, `accounts` list, pending invitation count |
| `frontend/src/components/Layout.tsx` | Add `AccountSwitcher`, pending invitation banner |
| `frontend/src/pages/Settings.tsx` | Add "Members" tab |
| `frontend/src/App.tsx` | Add `/invitations/:token` route |

---

## Chunk 1: Backend

### Task 1: Database Migration

**Files:**
- Create: `backend/migrations/033_account_invitations.sql`

- [ ] **Step 1: Write the migration**

```sql
-- Migration 033: Account invitations for team membership
CREATE TABLE account_invitations (
    id              SERIAL PRIMARY KEY,
    account_id      INT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    email           TEXT NOT NULL,
    role            TEXT NOT NULL DEFAULT 'member',
    token           UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    status          TEXT NOT NULL DEFAULT 'pending',
    invited_by      INT NOT NULL REFERENCES users(id),
    accepted_by     INT REFERENCES users(id),
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    responded_at    TIMESTAMPTZ
);

CREATE INDEX idx_invitations_token ON account_invitations(token);
CREATE INDEX idx_invitations_email ON account_invitations(email);
CREATE INDEX idx_invitations_account ON account_invitations(account_id);
```

- [ ] **Step 2: Commit**

```bash
git add backend/migrations/033_account_invitations.sql
git commit -m "feat(accounts): add account_invitations migration"
```

---

### Task 2: Store Types and Interfaces

**Files:**
- Modify: `backend/internal/store/store.go`

- [ ] **Step 1: Add `AccountInvitation` struct after `AccountMember` (after line 38)**

```go
type AccountInvitation struct {
	ID          int        `json:"id"`
	AccountID   int        `json:"account_id"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	Token       string     `json:"-"`
	Status      string     `json:"status"`
	InvitedBy   int        `json:"invited_by"`
	AcceptedBy  *int       `json:"accepted_by,omitempty"`
	ExpiresAt   time.Time  `json:"expires_at"`
	CreatedAt   time.Time  `json:"created_at"`
	RespondedAt *time.Time `json:"responded_at,omitempty"`
}
```

- [ ] **Step 2: Add `UpdateAccountMemberRole` to `AccountRepo` interface (after line 358)**

```go
	UpdateAccountMemberRole(ctx context.Context, accountID, userID int, role string) error
```

- [ ] **Step 3: Add `InvitationRepo` interface (after `AccountRepo`)**

```go
// InvitationRepo handles account invitations.
type InvitationRepo interface {
	CreateInvitation(ctx context.Context, inv *AccountInvitation) error
	GetInvitationByToken(ctx context.Context, token string) (*AccountInvitation, error)
	ListInvitationsByAccount(ctx context.Context, accountID int) ([]AccountInvitation, error)
	ListPendingInvitationsByEmail(ctx context.Context, email string) ([]AccountInvitation, error)
	AcceptInvitation(ctx context.Context, token string, userID int) error
	DeclineInvitation(ctx context.Context, token string) error
	RevokeInvitation(ctx context.Context, id int) error
}
```

- [ ] **Step 4: Add `InvitationRepo` to `Store` aggregate interface (line ~561)**

```go
type Store interface {
	UserRepo
	AccountRepo
	InvitationRepo  // ADD THIS
	MachineRepo
	// ... rest unchanged
}
```

- [ ] **Step 5: Compile check**

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./...`
Expected: FAIL — `PostgresStore` doesn't implement `InvitationRepo` yet. That's correct.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/store/store.go
git commit -m "feat(accounts): add AccountInvitation type and InvitationRepo interface"
```

---

### Task 3: Postgres Implementation — Invitations

**Files:**
- Modify: `backend/internal/store/postgres.go`

- [ ] **Step 1: Implement all `InvitationRepo` methods**

Add after the Account Members section (~line 220). Follow the existing pattern of raw SQL with pgx.

```go
// ---- Account Invitations ----

func (s *PostgresStore) CreateInvitation(ctx context.Context, inv *AccountInvitation) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO account_invitations (account_id, email, role, invited_by, expires_at)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, token, created_at`,
		inv.AccountID, inv.Email, inv.Role, inv.InvitedBy, inv.ExpiresAt,
	).Scan(&inv.ID, &inv.Token, &inv.CreatedAt)
}

func (s *PostgresStore) GetInvitationByToken(ctx context.Context, token string) (*AccountInvitation, error) {
	inv := &AccountInvitation{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, account_id, email, role, token, status, invited_by, accepted_by, expires_at, created_at, responded_at
		 FROM account_invitations WHERE token = $1`, token,
	).Scan(&inv.ID, &inv.AccountID, &inv.Email, &inv.Role, &inv.Token, &inv.Status,
		&inv.InvitedBy, &inv.AcceptedBy, &inv.ExpiresAt, &inv.CreatedAt, &inv.RespondedAt)
	if err != nil {
		return nil, err
	}
	return inv, nil
}

func (s *PostgresStore) ListInvitationsByAccount(ctx context.Context, accountID int) ([]AccountInvitation, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, account_id, email, role, status, invited_by, accepted_by, expires_at, created_at, responded_at
		 FROM account_invitations WHERE account_id = $1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var invs []AccountInvitation
	for rows.Next() {
		var inv AccountInvitation
		if err := rows.Scan(&inv.ID, &inv.AccountID, &inv.Email, &inv.Role, &inv.Status,
			&inv.InvitedBy, &inv.AcceptedBy, &inv.ExpiresAt, &inv.CreatedAt, &inv.RespondedAt); err != nil {
			return nil, err
		}
		invs = append(invs, inv)
	}
	return invs, nil
}

func (s *PostgresStore) ListPendingInvitationsByEmail(ctx context.Context, email string) ([]AccountInvitation, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT i.id, i.account_id, i.email, i.role, i.status, i.invited_by, i.expires_at, i.created_at,
		        a.name as account_name
		 FROM account_invitations i
		 JOIN accounts a ON a.id = i.account_id
		 WHERE LOWER(i.email) = LOWER($1) AND i.status = 'pending' AND i.expires_at > now()
		 ORDER BY i.created_at DESC`, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var invs []AccountInvitation
	for rows.Next() {
		var inv AccountInvitation
		var accountName string
		if err := rows.Scan(&inv.ID, &inv.AccountID, &inv.Email, &inv.Role, &inv.Status,
			&inv.InvitedBy, &inv.ExpiresAt, &inv.CreatedAt, &accountName); err != nil {
			return nil, err
		}
		invs = append(invs, inv)
	}
	return invs, nil
}

func (s *PostgresStore) AcceptInvitation(ctx context.Context, token string, userID int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE account_invitations
		 SET status = 'accepted', accepted_by = $1, responded_at = now()
		 WHERE token = $2 AND status = 'pending'`, userID, token)
	return err
}

func (s *PostgresStore) DeclineInvitation(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE account_invitations
		 SET status = 'declined', responded_at = now()
		 WHERE token = $1 AND status = 'pending'`, token)
	return err
}

func (s *PostgresStore) RevokeInvitation(ctx context.Context, id int) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM account_invitations WHERE id = $1 AND status = 'pending'`, id)
	return err
}
```

- [ ] **Step 2: Add `UpdateAccountMemberRole` (after `GetAccountMember`)**

```go
func (s *PostgresStore) UpdateAccountMemberRole(ctx context.Context, accountID, userID int, role string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE account_members SET role = $1 WHERE account_id = $2 AND user_id = $3`,
		role, accountID, userID)
	return err
}
```

- [ ] **Step 3: Compile check**

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./...`
Expected: PASS — all interfaces now satisfied.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/store/postgres.go
git commit -m "feat(accounts): implement InvitationRepo and UpdateAccountMemberRole"
```

---

### Task 4: Role Middleware + Route Registration

**Files:**
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Write test for `requireRole` middleware**

Create `backend/internal/api/role_middleware_test.go`:

```go
package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openclawmachines/backend/internal/store"
)

func TestRequireRole(t *testing.T) {
	tests := []struct {
		name       string
		member     *store.AccountMember
		allowed    []string
		wantStatus int
	}{
		{"owner allowed", &store.AccountMember{Role: "owner"}, []string{"owner", "admin"}, 200},
		{"admin allowed", &store.AccountMember{Role: "admin"}, []string{"owner", "admin"}, 200},
		{"member denied", &store.AccountMember{Role: "member"}, []string{"owner", "admin"}, 403},
		{"nil member", nil, []string{"owner"}, 403},
		{"member allowed when listed", &store.AccountMember{Role: "member"}, []string{"owner", "admin", "member"}, 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := requireRole(tt.allowed...)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
			}))

			req := httptest.NewRequest("GET", "/test", nil)
			if tt.member != nil {
				ctx := context.WithValue(req.Context(), accountMemberKey, tt.member)
				req = req.WithContext(ctx)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("got %d, want %d", rr.Code, tt.wantStatus)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/api/ -run TestRequireRole -v`
Expected: FAIL — `requireRole` not defined yet.

- [ ] **Step 3: Implement `requireRole` in `server.go`**

Add after `requireOwnerRole` (~line 534):

```go
// requireRole returns middleware that checks the account member has one of the allowed roles.
func requireRole(roles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			member := accountMemberFromContext(r.Context())
			if member == nil || !allowed[member.Role] {
				writeError(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/api/ -run TestRequireRole -v`
Expected: PASS

- [ ] **Step 5: Register invitation and member routes in `server.go`**

In the `r.Route("/api/accounts/{accountId}", ...)` block (~line 243), add:

```go
			// Members (role management)
			r.Put("/members/{userId}/role", srv.handleUpdateMemberRole)
			r.Delete("/members/{userId}", srv.handleRemoveMember)
			r.Post("/members/leave", srv.handleLeaveAccount)

			// Invitations (account-scoped)
			r.Post("/invitations", srv.handleCreateInvitation)
			r.Get("/invitations", srv.handleListInvitations)
			r.Delete("/invitations/{invitationId}", srv.handleRevokeInvitation)
```

Outside the account-scoped route (authenticated but not account-scoped), add:

```go
		// Invitations (user-scoped, not account-scoped)
		r.Get("/api/invitations/pending", srv.handleListPendingInvitations)
		r.Post("/api/invitations/{token}/accept", srv.handleAcceptInvitation)
		r.Post("/api/invitations/{token}/decline", srv.handleDeclineInvitation)
```

- [ ] **Step 6: Apply role checks to existing destructive endpoints**

Wrap credential and machine delete routes with `requireRole("owner", "admin")`. In the machines route block:

```go
			r.Route("/{id}", func(r chi.Router) {
				// ... existing GET routes unchanged ...

				// Destructive actions — owner/admin only
				r.Group(func(r chi.Router) {
					r.Use(requireRole("owner", "admin"))
					r.Delete("/", srv.handleDeleteMachine)
				})
			})
```

Similarly for credential delete:
```go
			r.Group(func(r chi.Router) {
				r.Use(requireRole("owner", "admin"))
				r.Delete("/credentials/{credentialId}", srv.handleDeleteCredential)
			})
```

- [ ] **Step 7: Compile check** (will fail until handlers exist — that's fine)

- [ ] **Step 8: Commit**

```bash
git add backend/internal/api/server.go backend/internal/api/role_middleware_test.go
git commit -m "feat(accounts): add requireRole middleware and register routes"
```

---

### Task 5: Invitation Handlers

**Files:**
- Create: `backend/internal/api/invitations.go`
- Create: `backend/internal/api/invitations_test.go`

- [ ] **Step 1: Write tests for invitation handlers**

Create `backend/internal/api/invitations_test.go` with table-driven tests covering:
- Create invitation: happy path, duplicate email, self-invite rejection, member role can't invite
- Accept invitation: happy path, email mismatch (403), expired, already accepted
- Decline invitation: happy path
- List invitations: returns pending only
- Revoke: removes pending invitation

Use the mock store pattern from `devauth_test.go` — embed `store.Store`, implement only needed methods.

- [ ] **Step 2: Implement handlers in `invitations.go`**

```go
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openclawmachines/backend/internal/auth"
	"github.com/openclawmachines/backend/internal/store"
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

	// Return token explicitly in create response (json:"-" hides it in lists)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":         inv.ID,
		"account_id": inv.AccountID,
		"email":      inv.Email,
		"role":       inv.Role,
		"token":      inv.Token,
		"status":     inv.Status,
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
		writeError(w, http.StatusInternalServerError, err.Error())
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

	if err := s.store.RevokeInvitation(r.Context(), invID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke invitation")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListPendingInvitations returns pending invitations for the authenticated user's email.
func (s *Server) handleListPendingInvitations(w http.ResponseWriter, r *http.Request) {
	claims := auth.UserFromContext(r.Context())
	invs, err := s.store.ListPendingInvitationsByEmail(r.Context(), claims.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, invs)
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

	// Check if already a member
	if _, err := s.store.GetAccountMember(r.Context(), inv.AccountID, claims.UserID); err == nil {
		writeError(w, http.StatusConflict, "you are already a member of this account")
		return
	}

	// Add as member
	member := &store.AccountMember{
		AccountID: inv.AccountID,
		UserID:    claims.UserID,
		Role:      inv.Role,
	}
	if err := s.store.AddAccountMember(r.Context(), member); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add member")
		return
	}

	// Mark invitation as accepted
	if err := s.store.AcceptInvitation(r.Context(), token, claims.UserID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update invitation")
		return
	}

	// Log event
	s.store.CreateAccountEvent(r.Context(), &store.AccountEvent{
		EventType:    "member.invited",
		AccountID:    inv.AccountID,
		ActorUserID:  &inv.InvitedBy,
		TargetUserID: &claims.UserID,
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

	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 3: Run tests**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/api/ -run TestInvitation -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/invitations.go backend/internal/api/invitations_test.go
git commit -m "feat(accounts): invitation handlers with email-match security"
```

---

### Task 6: Member Management Handlers

**Files:**
- Create: `backend/internal/api/members.go`
- Create: `backend/internal/api/members_test.go`

- [ ] **Step 1: Write tests for member handlers**

Test cases:
- Update role: owner can change admin↔member, can't change self, can't set to owner
- Remove member: owner/admin can remove member, admin can't remove owner, can't remove self
- Leave: member can leave, owner can't leave

- [ ] **Step 2: Implement handlers in `members.go`**

```go
package api

import (
	"encoding/json"
	"net/http"

	"github.com/openclawmachines/backend/internal/store"
)

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

	s.store.CreateAccountEvent(r.Context(), &store.AccountEvent{
		EventType:    "member.removed",
		AccountID:    accountID,
		ActorUserID:  &member.UserID,
		TargetUserID: &targetUserID,
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

	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 3: Run tests**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/api/ -run TestMember -v`
Expected: PASS

- [ ] **Step 4: Run full backend test suite**

Run: `cd /home/mantiz/OpenClawMachines && make test-go`
Expected: PASS — no regressions

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/members.go backend/internal/api/members_test.go
git commit -m "feat(accounts): member management handlers (role change, remove, leave)"
```

---

### Task 7: Fix Compilation — Update Mock Stores

Any existing test mock stores that embed `store.Store` will fail to compile because `Store` now includes `InvitationRepo`. Find all mock stores and add stub methods.

**Files:**
- Modify: all test files with mock stores that embed `store.Store`

- [ ] **Step 1: Find all mock stores**

Run: `grep -rn 'store.Store' backend/internal/ --include='*_test.go' | grep struct`

For each mock store found, add stub implementations:

```go
func (m *mockStore) CreateInvitation(_ context.Context, _ *store.AccountInvitation) error { return nil }
func (m *mockStore) GetInvitationByToken(_ context.Context, _ string) (*store.AccountInvitation, error) { return nil, fmt.Errorf("not found") }
func (m *mockStore) ListInvitationsByAccount(_ context.Context, _ int) ([]store.AccountInvitation, error) { return nil, nil }
func (m *mockStore) ListPendingInvitationsByEmail(_ context.Context, _ string) ([]store.AccountInvitation, error) { return nil, nil }
func (m *mockStore) AcceptInvitation(_ context.Context, _ string, _ int) error { return nil }
func (m *mockStore) DeclineInvitation(_ context.Context, _ string) error { return nil }
func (m *mockStore) RevokeInvitation(_ context.Context, _ int) error { return nil }
func (m *mockStore) UpdateAccountMemberRole(_ context.Context, _, _ int, _ string) error { return nil }
```

- [ ] **Step 2: Full compile + test**

Run: `cd /home/mantiz/OpenClawMachines && make test-go`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add -A
git commit -m "fix: update mock stores for InvitationRepo interface"
```

---

## Chunk 2: Frontend

### Task 8: TypeScript Types and API Functions

**Files:**
- Modify: `frontend/src/lib/types.ts`
- Modify: `frontend/src/lib/api.ts`

- [ ] **Step 1: Add `AccountInvitation` type to `types.ts` (after `AccountMember`)**

```typescript
export interface AccountInvitation {
  id: number;
  account_id: number;
  email: string;
  role: "admin" | "member";
  token?: string;        // only present in create response
  status: "pending" | "accepted" | "declined" | "expired";
  invited_by: number;
  accepted_by?: number;
  expires_at: string;
  created_at: string;
  responded_at?: string;
}
```

- [ ] **Step 2: Add API functions to `api.ts` (after line 79)**

```typescript
// Invitations (account-scoped)
export const createInvitation = (accountId: number, data: { email: string; role: string }) =>
  request<AccountInvitation & { token: string }>(`/accounts/${accountId}/invitations`, {
    method: "POST", body: JSON.stringify(data),
  });
export const listInvitations = (accountId: number) =>
  request<AccountInvitation[]>(`/accounts/${accountId}/invitations`);
export const revokeInvitation = (accountId: number, invitationId: number) =>
  request<void>(`/accounts/${accountId}/invitations/${invitationId}`, { method: "DELETE" });

// Invitations (user-scoped)
export const listPendingInvitations = () =>
  request<AccountInvitation[]>("/invitations/pending");
export const acceptInvitation = (token: string) =>
  request<{ account_id: number; role: string }>(`/invitations/${token}/accept`, { method: "POST" });
export const declineInvitation = (token: string) =>
  request<void>(`/invitations/${token}/decline`, { method: "POST" });

// Members
export const updateMemberRole = (accountId: number, userId: number, role: string) =>
  request<{ role: string }>(`/accounts/${accountId}/members/${userId}/role`, {
    method: "PUT", body: JSON.stringify({ role }),
  });
export const removeMember = (accountId: number, userId: number) =>
  request<void>(`/accounts/${accountId}/members/${userId}`, { method: "DELETE" });
export const leaveAccount = (accountId: number) =>
  request<void>(`/accounts/${accountId}/members/leave`, { method: "POST" });
```

- [ ] **Step 3: Add import for `AccountInvitation` in `api.ts`**

Update the existing import line from `types.ts` to include `AccountInvitation`.

- [ ] **Step 4: Typecheck**

Run: `cd /home/mantiz/OpenClawMachines && make typecheck`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/types.ts frontend/src/lib/api.ts
git commit -m "feat(accounts): add invitation types and API functions"
```

---

### Task 9: Auth Context — Multi-Account Support

**Files:**
- Modify: `frontend/src/lib/auth.tsx`

- [ ] **Step 1: Update `AuthState` interface and provider**

Add `accounts` list, `setAccount` function, and `pendingInvitationCount`:

```typescript
interface AuthState {
  user: User | null;
  account: Account | null;
  accounts: Account[];
  loading: boolean;
  accountError: boolean;
  isAdmin: boolean;
  pendingInvitationCount: number;
  setAccount: (account: Account) => void;
  logout: () => void;
}
```

Update `AuthProvider` to:
- Store full `accounts` list in state
- Expose `setAccount` that saves to `localStorage` and updates state
- On load, restore last active account from `localStorage` (fall back to `accounts[0]`)
- Fetch pending invitation count via `listPendingInvitations`

- [ ] **Step 2: Typecheck**

Run: `cd /home/mantiz/OpenClawMachines && make typecheck`

- [ ] **Step 3: Commit**

```bash
git add frontend/src/lib/auth.tsx
git commit -m "feat(accounts): multi-account auth context with account switching"
```

---

### Task 10: Account Switcher Component

**Files:**
- Create: `frontend/src/components/AccountSwitcher.tsx`
- Modify: `frontend/src/components/Layout.tsx`

- [ ] **Step 1: Create `AccountSwitcher.tsx`**

A dropdown button showing the active account name. When clicked, shows all accounts with role badges, plus "+ Create New Account" at the bottom. Clicking an account calls `setAccount()` from auth context.

Pattern: Follow the theme menu dropdown pattern in `Layout.tsx` (lines 72-100) — `useRef` + `useEffect` for click-outside-to-close.

- [ ] **Step 2: Add to `Layout.tsx` header**

Replace the existing title area or add next to it. Also add a pending invitations banner:

```tsx
{pendingInvitationCount > 0 && (
  <div className="bg-brand-50 dark:bg-brand-600/10 border-b border-brand-200 dark:border-brand-600/20 px-6 py-2 text-sm">
    You have {pendingInvitationCount} pending invitation{pendingInvitationCount > 1 ? "s" : ""}.
    <Link to="/dashboard/invitations" className="ml-2 text-brand-600 font-medium">View</Link>
  </div>
)}
```

- [ ] **Step 3: Typecheck + visual check**

Run: `cd /home/mantiz/OpenClawMachines && make typecheck`

- [ ] **Step 4: Commit**

```bash
git add frontend/src/components/AccountSwitcher.tsx frontend/src/components/Layout.tsx
git commit -m "feat(accounts): account switcher dropdown in header"
```

---

### Task 11: Members Tab in Settings

**Files:**
- Create: `frontend/src/components/MembersTab.tsx`
- Modify: `frontend/src/pages/Settings.tsx`

- [ ] **Step 1: Create `MembersTab.tsx`**

The component shows:
1. **Members list** — each row has email, role badge, and actions (Remove button for owner/admin, role dropdown for owner, Leave for self)
2. **Pending invitations section** — each row has email, role, expiry, Revoke button
3. **Invite dialog** — Radix Dialog with email input and role dropdown, shows copyable link after creation

Use `listMembers()`, `listInvitations()`, `createInvitation()`, `revokeInvitation()`, `updateMemberRole()`, `removeMember()`, `leaveAccount()` from `api.ts`.

Fetch user details for member display: the `account_members` endpoint returns `user_id` but not email/name. Either:
- (a) Enhance backend `ListAccountMembers` to JOIN users and return email/name, or
- (b) Add a `ListAccountMembersWithUsers` endpoint

**Recommended**: Option (a) — modify `ListAccountMembers` in `postgres.go` to JOIN users table and add `Email` and `Name` fields to the `AccountMember` struct.

Add to `store.go` `AccountMember`:
```go
type AccountMember struct {
	AccountID int       `json:"account_id"`
	UserID    int       `json:"user_id"`
	Role      string    `json:"role"`
	Email     string    `json:"email,omitempty"`
	Name      string    `json:"name,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
```

Update `postgres.go` `ListAccountMembers`:
```sql
SELECT am.account_id, am.user_id, am.role, am.created_at, u.email, u.name
FROM account_members am
JOIN users u ON u.id = am.user_id
WHERE am.account_id = $1 ORDER BY am.created_at
```

Update `types.ts` `AccountMember`:
```typescript
export interface AccountMember {
  account_id: number;
  user_id: number;
  role: "owner" | "admin" | "member";
  email?: string;
  name?: string;
  created_at: string;
}
```

- [ ] **Step 2: Add "Members" tab to `Settings.tsx`**

```typescript
type SettingsTab = "api-keys" | "channels" | "members" | "usage" | "profile";

const TABS: { id: SettingsTab; label: string }[] = [
  { id: "api-keys", label: "Integrations" },
  { id: "channels", label: "Channels" },
  { id: "members", label: "Members" },
  { id: "usage", label: "Usage & Billing" },
  { id: "profile", label: "Profile" },
];

// In render:
{activeTab === "members" && account && (
  <MembersTab accountId={account.id} />
)}
```

- [ ] **Step 3: Typecheck**

Run: `cd /home/mantiz/OpenClawMachines && make typecheck`

- [ ] **Step 4: Commit**

```bash
git add frontend/src/components/MembersTab.tsx frontend/src/pages/Settings.tsx \
       backend/internal/store/store.go backend/internal/store/postgres.go \
       frontend/src/lib/types.ts
git commit -m "feat(accounts): members tab with invite dialog and role management"
```

---

### Task 12: Invitation Accept Page

**Files:**
- Create: `frontend/src/pages/InvitationAccept.tsx`
- Modify: `frontend/src/App.tsx`

- [ ] **Step 1: Create `InvitationAccept.tsx`**

Page at `/invitations/:token`:
1. On mount, call `acceptInvitation(token)` with a dry-run flag or `getInvitationByToken(token)` — actually, the backend doesn't have a GET-by-token endpoint for unauthenticated preview. So the page should:
   - Show a simple "Loading invitation..." state
   - Call a new `GET /api/invitations/{token}` endpoint to fetch invitation details (account name, role, inviter email)
   - Display: account name, role, invited-by email
   - Two buttons: Accept, Decline
   - Handle error states: expired, already used, email mismatch, invalid token

**Backend addition needed**: Add `GET /api/invitations/{token}` handler that returns invitation details (public fields only — no token in response). This endpoint requires authentication but not account membership.

Add to `invitations.go`:
```go
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

	// Get inviter name
	inviter, _ := s.store.GetUser(r.Context(), inv.InvitedBy)
	inviterEmail := ""
	if inviter != nil {
		inviterEmail = inviter.Email
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":            inv.ID,
		"account_id":    inv.AccountID,
		"account_name":  accountName,
		"email":         inv.Email,
		"role":          inv.Role,
		"status":        inv.Status,
		"inviter_email": inviterEmail,
		"expires_at":    inv.ExpiresAt,
	})
}
```

Add route in `server.go` (authenticated, not account-scoped):
```go
r.Get("/api/invitations/{token}", srv.handleGetInvitation)
```

Add API function in `api.ts`:
```typescript
export const getInvitation = (token: string) =>
  request<{
    id: number; account_id: number; account_name: string;
    email: string; role: string; status: string;
    inviter_email: string; expires_at: string;
  }>(`/invitations/${token}`);
```

- [ ] **Step 2: Add route to `App.tsx`**

```tsx
<Route path="/invitations/:token" element={
  <ProtectedRoute><InvitationAccept /></ProtectedRoute>
} />
```

- [ ] **Step 3: Typecheck**

Run: `cd /home/mantiz/OpenClawMachines && make typecheck`

- [ ] **Step 4: Full test suite**

Run: `cd /home/mantiz/OpenClawMachines && make test-go && make typecheck`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add frontend/src/pages/InvitationAccept.tsx frontend/src/App.tsx \
       backend/internal/api/invitations.go backend/internal/api/server.go \
       frontend/src/lib/api.ts
git commit -m "feat(accounts): invitation accept/decline page with error states"
```

---

### Task 13: Final Integration Test + Cleanup

- [ ] **Step 1: Run full test suite**

Run: `cd /home/mantiz/OpenClawMachines && make test-go && make test-frontend && make typecheck`
Expected: ALL PASS

- [ ] **Step 2: Update `docs/CurrentFeature.md`**

Add "Implementation Status" section listing what was built and what's deferred to Phase 2.

- [ ] **Step 3: Final commit**

```bash
git add -A
git commit -m "feat(accounts): complete Phase 1 — invitations, RBAC, account switching"
```
