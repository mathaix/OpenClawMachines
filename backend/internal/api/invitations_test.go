package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// mockInvitationStore implements store methods needed for invitation tests.
type mockInvitationStore struct {
	store.Store

	users        map[int]*store.User
	usersByEmail map[string]*store.User
	accounts     map[int]*store.Account
	members      map[int]map[int]*store.AccountMember // accountID -> userID -> member
	invitations  map[int]*store.AccountInvitation     // id -> invitation
	invByToken   map[string]*store.AccountInvitation  // token -> invitation
	invByAcct    map[int][]store.AccountInvitation    // accountID -> invitations
	invByEmail   map[string][]store.AccountInvitation // email -> pending invitations
	nextInvID    int

	acceptErr error
}

func newMockInvitationStore() *mockInvitationStore {
	return &mockInvitationStore{
		users:        make(map[int]*store.User),
		usersByEmail: make(map[string]*store.User),
		accounts:     make(map[int]*store.Account),
		members:      make(map[int]map[int]*store.AccountMember),
		invitations:  make(map[int]*store.AccountInvitation),
		invByToken:   make(map[string]*store.AccountInvitation),
		invByAcct:    make(map[int][]store.AccountInvitation),
		invByEmail:   make(map[string][]store.AccountInvitation),
		nextInvID:    1,
	}
}

func (m *mockInvitationStore) GetUserByEmail(_ context.Context, email string) (*store.User, error) {
	if u, ok := m.usersByEmail[email]; ok {
		return u, nil
	}
	return nil, fmt.Errorf("user not found by email %q", email)
}

func (m *mockInvitationStore) GetUser(_ context.Context, id int) (*store.User, error) {
	if u, ok := m.users[id]; ok {
		return u, nil
	}
	return nil, fmt.Errorf("user %d not found", id)
}

func (m *mockInvitationStore) GetAccount(_ context.Context, id int) (*store.Account, error) {
	if a, ok := m.accounts[id]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("account %d not found", id)
}

func (m *mockInvitationStore) GetAccountMember(_ context.Context, accountID, userID int) (*store.AccountMember, error) {
	if members, ok := m.members[accountID]; ok {
		if member, ok := members[userID]; ok {
			return member, nil
		}
	}
	return nil, fmt.Errorf("member not found")
}

func (m *mockInvitationStore) CreateInvitation(_ context.Context, inv *store.AccountInvitation) error {
	inv.ID = m.nextInvID
	m.nextInvID++
	inv.Token = fmt.Sprintf("tok-%d", inv.ID)
	inv.Status = "pending"
	inv.CreatedAt = time.Now()
	m.invitations[inv.ID] = inv
	m.invByToken[inv.Token] = inv
	m.invByAcct[inv.AccountID] = append(m.invByAcct[inv.AccountID], *inv)
	return nil
}

func (m *mockInvitationStore) GetInvitationByToken(_ context.Context, token string) (*store.AccountInvitation, error) {
	if inv, ok := m.invByToken[token]; ok {
		return inv, nil
	}
	return nil, fmt.Errorf("invitation not found")
}

func (m *mockInvitationStore) ListInvitationsByAccount(_ context.Context, accountID int) ([]store.AccountInvitation, error) {
	return m.invByAcct[accountID], nil
}

func (m *mockInvitationStore) ListPendingInvitationsByEmail(_ context.Context, email string) ([]store.AccountInvitation, error) {
	return m.invByEmail[email], nil
}

func (m *mockInvitationStore) AcceptInvitation(_ context.Context, token string, userID int) (int64, error) {
	if m.acceptErr != nil {
		return 0, m.acceptErr
	}
	if inv, ok := m.invByToken[token]; ok && inv.Status == "pending" {
		if members, ok := m.members[inv.AccountID]; ok {
			if _, exists := members[userID]; exists {
				return 0, store.ErrAlreadyMember
			}
		}
		inv.Status = "accepted"
		inv.AcceptedBy = &userID
		if m.members[inv.AccountID] == nil {
			m.members[inv.AccountID] = make(map[int]*store.AccountMember)
		}
		m.members[inv.AccountID][userID] = &store.AccountMember{
			AccountID: inv.AccountID,
			UserID:    userID,
			Role:      inv.Role,
		}
		return 1, nil
	}
	return 0, nil
}

func (m *mockInvitationStore) DeclineInvitation(_ context.Context, token string) error {
	if inv, ok := m.invByToken[token]; ok {
		inv.Status = "declined"
		return nil
	}
	return fmt.Errorf("invitation not found")
}

func (m *mockInvitationStore) RevokeInvitation(_ context.Context, accountID, id int) error {
	if inv, ok := m.invitations[id]; ok && inv.AccountID == accountID {
		delete(m.invitations, id)
		return nil
	}
	return fmt.Errorf("invitation not found")
}

func (m *mockInvitationStore) AddAccountMember(_ context.Context, member *store.AccountMember) error {
	if m.members[member.AccountID] == nil {
		m.members[member.AccountID] = make(map[int]*store.AccountMember)
	}
	m.members[member.AccountID][member.UserID] = member
	return nil
}

// helper to create a request with auth claims, account context, and optional member context.
func invReq(method, path string, body interface{}, claims *auth.Claims, accountID int, member *store.AccountMember) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")

	ctx := auth.WithUser(req.Context(), claims)
	ctx = context.WithValue(ctx, accountIDKey, accountID)
	if member != nil {
		ctx = context.WithValue(ctx, accountMemberKey, member)
	}
	return req.WithContext(ctx)
}

// helper to set chi URL params on a request.
func withChiParam(req *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func TestCreateInvitation(t *testing.T) {
	ownerClaims := &auth.Claims{UserID: 1, Email: "owner@test.com"}
	ownerMember := &store.AccountMember{AccountID: 1, UserID: 1, Role: "owner"}

	tests := []struct {
		name       string
		body       map[string]string
		claims     *auth.Claims
		member     *store.AccountMember
		setup      func(*mockInvitationStore)
		wantStatus int
		wantErr    string
	}{
		{
			name:       "happy path",
			body:       map[string]string{"email": "invitee@test.com", "role": "member"},
			claims:     ownerClaims,
			member:     ownerMember,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "empty email",
			body:       map[string]string{"email": "", "role": "member"},
			claims:     ownerClaims,
			member:     ownerMember,
			wantStatus: http.StatusBadRequest,
			wantErr:    "email is required",
		},
		{
			name:       "invalid role",
			body:       map[string]string{"email": "invitee@test.com", "role": "superadmin"},
			claims:     ownerClaims,
			member:     ownerMember,
			wantStatus: http.StatusBadRequest,
			wantErr:    "role must be",
		},
		{
			name:       "self-invite",
			body:       map[string]string{"email": "owner@test.com", "role": "member"},
			claims:     ownerClaims,
			member:     ownerMember,
			wantStatus: http.StatusBadRequest,
			wantErr:    "cannot invite yourself",
		},
		{
			name:   "already a member",
			body:   map[string]string{"email": "existing@test.com", "role": "member"},
			claims: ownerClaims,
			member: ownerMember,
			setup: func(ms *mockInvitationStore) {
				ms.usersByEmail["existing@test.com"] = &store.User{ID: 5, Email: "existing@test.com"}
				ms.users[5] = &store.User{ID: 5, Email: "existing@test.com"}
				if ms.members[1] == nil {
					ms.members[1] = make(map[int]*store.AccountMember)
				}
				ms.members[1][5] = &store.AccountMember{AccountID: 1, UserID: 5, Role: "member"}
			},
			wantStatus: http.StatusConflict,
			wantErr:    "already a member",
		},
		{
			name:   "duplicate pending invitation",
			body:   map[string]string{"email": "invitee@test.com", "role": "member"},
			claims: ownerClaims,
			member: ownerMember,
			setup: func(ms *mockInvitationStore) {
				inv := store.AccountInvitation{
					ID:        99,
					AccountID: 1,
					Email:     "invitee@test.com",
					Role:      "member",
					Status:    "pending",
					InvitedBy: 1,
					ExpiresAt: time.Now().Add(24 * time.Hour),
				}
				ms.invByAcct[1] = append(ms.invByAcct[1], inv)
			},
			wantStatus: http.StatusConflict,
			wantErr:    "pending invitation already exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newMockInvitationStore()
			if tt.setup != nil {
				tt.setup(ms)
			}
			srv := &Server{store: ms}

			req := invReq("POST", "/api/accounts/1/invitations", tt.body, tt.claims, 1, tt.member)
			rr := httptest.NewRecorder()

			http.HandlerFunc(srv.handleCreateInvitation).ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d. Body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if tt.wantErr != "" {
				var resp map[string]string
				_ = json.NewDecoder(rr.Body).Decode(&resp)
				if msg, ok := resp["error"]; !ok || !contains(msg, tt.wantErr) {
					t.Errorf("error = %q, want to contain %q", msg, tt.wantErr)
				}
			}
			if tt.wantStatus == http.StatusCreated {
				var resp map[string]interface{}
				_ = json.NewDecoder(rr.Body).Decode(&resp)
				if resp["token"] == nil || resp["token"] == "" {
					t.Error("expected token in response")
				}
				if resp["email"] != "invitee@test.com" {
					t.Errorf("email = %v, want invitee@test.com", resp["email"])
				}
			}
		})
	}
}

func TestAcceptInvitation(t *testing.T) {
	tests := []struct {
		name       string
		token      string
		claims     *auth.Claims
		setup      func(*mockInvitationStore)
		wantStatus int
		wantErr    string
	}{
		{
			name:   "happy path",
			token:  "valid-token",
			claims: &auth.Claims{UserID: 2, Email: "invitee@test.com"},
			setup: func(ms *mockInvitationStore) {
				ms.invByToken["valid-token"] = &store.AccountInvitation{
					ID:        1,
					AccountID: 1,
					Email:     "invitee@test.com",
					Role:      "member",
					Token:     "valid-token",
					Status:    "pending",
					InvitedBy: 1,
					ExpiresAt: time.Now().Add(24 * time.Hour),
				}
			},
			wantStatus: http.StatusOK,
		},
		{
			name:   "email mismatch",
			token:  "mismatch-token",
			claims: &auth.Claims{UserID: 3, Email: "wrong@test.com"},
			setup: func(ms *mockInvitationStore) {
				ms.invByToken["mismatch-token"] = &store.AccountInvitation{
					ID:        2,
					AccountID: 1,
					Email:     "invitee@test.com",
					Role:      "member",
					Token:     "mismatch-token",
					Status:    "pending",
					InvitedBy: 1,
					ExpiresAt: time.Now().Add(24 * time.Hour),
				}
			},
			wantStatus: http.StatusForbidden,
			wantErr:    "different email",
		},
		{
			name:   "expired invitation",
			token:  "expired-token",
			claims: &auth.Claims{UserID: 2, Email: "invitee@test.com"},
			setup: func(ms *mockInvitationStore) {
				ms.invByToken["expired-token"] = &store.AccountInvitation{
					ID:        3,
					AccountID: 1,
					Email:     "invitee@test.com",
					Role:      "member",
					Token:     "expired-token",
					Status:    "pending",
					InvitedBy: 1,
					ExpiresAt: time.Now().Add(-1 * time.Hour),
				}
			},
			wantStatus: http.StatusGone,
			wantErr:    "expired",
		},
		{
			name:   "already accepted",
			token:  "accepted-token",
			claims: &auth.Claims{UserID: 2, Email: "invitee@test.com"},
			setup: func(ms *mockInvitationStore) {
				ms.invByToken["accepted-token"] = &store.AccountInvitation{
					ID:        4,
					AccountID: 1,
					Email:     "invitee@test.com",
					Role:      "member",
					Token:     "accepted-token",
					Status:    "accepted",
					InvitedBy: 1,
					ExpiresAt: time.Now().Add(24 * time.Hour),
				}
			},
			wantStatus: http.StatusGone,
			wantErr:    "already been accepted",
		},
		{
			name:   "already a member",
			token:  "member-token",
			claims: &auth.Claims{UserID: 2, Email: "invitee@test.com"},
			setup: func(ms *mockInvitationStore) {
				ms.invByToken["member-token"] = &store.AccountInvitation{
					ID:        5,
					AccountID: 1,
					Email:     "invitee@test.com",
					Role:      "member",
					Token:     "member-token",
					Status:    "pending",
					InvitedBy: 1,
					ExpiresAt: time.Now().Add(24 * time.Hour),
				}
				if ms.members[1] == nil {
					ms.members[1] = make(map[int]*store.AccountMember)
				}
				ms.members[1][2] = &store.AccountMember{AccountID: 1, UserID: 2, Role: "member"}
			},
			wantStatus: http.StatusConflict,
			wantErr:    "already a member",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newMockInvitationStore()
			if tt.setup != nil {
				tt.setup(ms)
			}
			srv := &Server{store: ms}

			req := httptest.NewRequest("POST", "/api/invitations/"+tt.token+"/accept", nil)
			ctx := auth.WithUser(req.Context(), tt.claims)
			req = req.WithContext(ctx)
			req = withChiParam(req, "token", tt.token)

			rr := httptest.NewRecorder()
			http.HandlerFunc(srv.handleAcceptInvitation).ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d. Body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if tt.wantErr != "" {
				var resp map[string]string
				_ = json.NewDecoder(rr.Body).Decode(&resp)
				if msg, ok := resp["error"]; !ok || !contains(msg, tt.wantErr) {
					t.Errorf("error = %q, want to contain %q", msg, tt.wantErr)
				}
			}
			if tt.wantStatus == http.StatusOK {
				var resp map[string]interface{}
				_ = json.NewDecoder(rr.Body).Decode(&resp)
				if resp["message"] != "invitation accepted" {
					t.Errorf("message = %v, want 'invitation accepted'", resp["message"])
				}
				inv := ms.invByToken[tt.token]
				if inv != nil {
					if _, ok := ms.members[inv.AccountID][tt.claims.UserID]; !ok {
						t.Errorf("expected user %d to be added to account %d", tt.claims.UserID, inv.AccountID)
					}
				}
			}
		})
	}
}

func TestDeclineInvitation(t *testing.T) {
	tests := []struct {
		name       string
		token      string
		claims     *auth.Claims
		setup      func(*mockInvitationStore)
		wantStatus int
		wantErr    string
	}{
		{
			name:   "happy path",
			token:  "decline-token",
			claims: &auth.Claims{UserID: 2, Email: "invitee@test.com"},
			setup: func(ms *mockInvitationStore) {
				ms.invByToken["decline-token"] = &store.AccountInvitation{
					ID:        1,
					AccountID: 1,
					Email:     "invitee@test.com",
					Role:      "member",
					Token:     "decline-token",
					Status:    "pending",
					InvitedBy: 1,
					ExpiresAt: time.Now().Add(24 * time.Hour),
				}
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:   "email mismatch",
			token:  "other-token",
			claims: &auth.Claims{UserID: 3, Email: "wrong@test.com"},
			setup: func(ms *mockInvitationStore) {
				ms.invByToken["other-token"] = &store.AccountInvitation{
					ID:        2,
					AccountID: 1,
					Email:     "invitee@test.com",
					Role:      "member",
					Token:     "other-token",
					Status:    "pending",
					InvitedBy: 1,
					ExpiresAt: time.Now().Add(24 * time.Hour),
				}
			},
			wantStatus: http.StatusForbidden,
			wantErr:    "different email",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newMockInvitationStore()
			if tt.setup != nil {
				tt.setup(ms)
			}
			srv := &Server{store: ms}

			req := httptest.NewRequest("POST", "/api/invitations/"+tt.token+"/decline", nil)
			ctx := auth.WithUser(req.Context(), tt.claims)
			req = req.WithContext(ctx)
			req = withChiParam(req, "token", tt.token)

			rr := httptest.NewRecorder()
			http.HandlerFunc(srv.handleDeclineInvitation).ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d. Body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if tt.wantErr != "" {
				var resp map[string]string
				_ = json.NewDecoder(rr.Body).Decode(&resp)
				if msg, ok := resp["error"]; !ok || !contains(msg, tt.wantErr) {
					t.Errorf("error = %q, want to contain %q", msg, tt.wantErr)
				}
			}
		})
	}
}

func TestListPendingInvitations(t *testing.T) {
	ms := newMockInvitationStore()
	ms.invByEmail["user@test.com"] = []store.AccountInvitation{
		{ID: 1, AccountID: 1, Email: "user@test.com", Role: "member", Status: "pending"},
		{ID: 2, AccountID: 2, Email: "user@test.com", Role: "admin", Status: "pending"},
	}

	srv := &Server{store: ms}

	req := httptest.NewRequest("GET", "/api/invitations/pending", nil)
	ctx := auth.WithUser(req.Context(), &auth.Claims{UserID: 5, Email: "user@test.com"})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	http.HandlerFunc(srv.handleListPendingInvitations).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", rr.Code, rr.Body.String())
	}

	var invs []store.AccountInvitation
	if err := json.NewDecoder(rr.Body).Decode(&invs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(invs) != 2 {
		t.Errorf("got %d invitations, want 2", len(invs))
	}
}

func TestListPendingInvitations_FiltersExistingMembershipAndDuplicates(t *testing.T) {
	ms := newMockInvitationStore()
	ms.invByEmail["user@test.com"] = []store.AccountInvitation{
		{ID: 3, AccountID: 3, Email: "user@test.com", Role: "member", Status: "pending", CreatedAt: time.Now()},
		{ID: 2, AccountID: 2, Email: "user@test.com", Role: "admin", Status: "pending", CreatedAt: time.Now().Add(-1 * time.Minute)},
		{ID: 1, AccountID: 2, Email: "user@test.com", Role: "member", Status: "pending", CreatedAt: time.Now().Add(-2 * time.Minute)},
	}
	ms.members[3] = map[int]*store.AccountMember{
		5: {AccountID: 3, UserID: 5, Role: "member"},
	}

	srv := &Server{store: ms}

	req := httptest.NewRequest("GET", "/api/invitations/pending", nil)
	ctx := auth.WithUser(req.Context(), &auth.Claims{UserID: 5, Email: "user@test.com"})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	http.HandlerFunc(srv.handleListPendingInvitations).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", rr.Code, rr.Body.String())
	}

	var invs []store.AccountInvitation
	if err := json.NewDecoder(rr.Body).Decode(&invs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(invs) != 1 {
		t.Fatalf("got %d invitations, want 1", len(invs))
	}
	if invs[0].AccountID != 2 {
		t.Fatalf("got account %d, want 2", invs[0].AccountID)
	}
	if invs[0].ID != 2 {
		t.Fatalf("got invitation id %d, want most recent invitation for account 2", invs[0].ID)
	}
}

// contains checks if s contains substr (simple helper to avoid importing strings in tests).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- Plugin stubs (satisfy store.Store interface) ---

func (m *mockInvitationStore) ListPluginCatalog(_ context.Context) ([]store.PluginCatalogEntry, error) {
	return nil, nil
}
func (m *mockInvitationStore) GetPluginCatalogEntry(_ context.Context, _ string) (*store.PluginCatalogEntry, error) {
	return nil, fmt.Errorf("not found")
}
func (m *mockInvitationStore) CreatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockInvitationStore) UpdatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockInvitationStore) DeletePluginCatalogEntry(_ context.Context, _ string) error {
	return nil
}
func (m *mockInvitationStore) ListMachinePlugins(_ context.Context, _ string) ([]store.MachinePlugin, error) {
	return nil, nil
}
func (m *mockInvitationStore) EnableMachinePlugin(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockInvitationStore) DisableMachinePlugin(_ context.Context, _, _ string) error {
	return nil
}
func (m *mockInvitationStore) UpdateMachinePluginOverrides(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockInvitationStore) UpdateMachinePluginStatus(_ context.Context, _, _, _, _ string) error {
	return nil
}
