package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// mockAccountStore implements store methods needed for account tests.
type mockAccountStore struct {
	store.Store

	accounts       map[int]*store.Account
	accountsBySlug map[string]*store.Account
	userAccounts   map[int][]store.Account       // userID -> accounts
	members        map[int][]store.AccountMember // accountID -> members
	nextID         int

	createErr error
}

func newMockAccountStore() *mockAccountStore {
	return &mockAccountStore{
		accounts:       make(map[int]*store.Account),
		accountsBySlug: make(map[string]*store.Account),
		userAccounts:   make(map[int][]store.Account),
		members:        make(map[int][]store.AccountMember),
		nextID:         1,
	}
}

func (m *mockAccountStore) CreateAccountWithOwner(_ context.Context, account *store.Account, ownerUserID int) error {
	if m.createErr != nil {
		return m.createErr
	}
	if _, exists := m.accountsBySlug[account.Slug]; exists {
		// Simulate a pg unique violation — but we can't import pgx in a unit test,
		// so we use a sentinel that store.IsConflict won't match.
		// Instead, the caller should set createErr to trigger the conflict path.
		return fmt.Errorf("duplicate slug")
	}
	account.ID = m.nextID
	m.nextID++
	m.accounts[account.ID] = account
	m.accountsBySlug[account.Slug] = account
	m.userAccounts[ownerUserID] = append(m.userAccounts[ownerUserID], *account)
	return nil
}

func (m *mockAccountStore) GetAccount(_ context.Context, id int) (*store.Account, error) {
	if a, ok := m.accounts[id]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("account %d not found", id)
}

func (m *mockAccountStore) ListAccountsByUser(_ context.Context, userID int) ([]store.Account, error) {
	return m.userAccounts[userID], nil
}

func (m *mockAccountStore) ListAccountMembers(_ context.Context, accountID int) ([]store.AccountMember, error) {
	return m.members[accountID], nil
}

func (m *mockAccountStore) UpdateAccountName(_ context.Context, accountID int, name string) error {
	a, ok := m.accounts[accountID]
	if !ok {
		return fmt.Errorf("account not found")
	}
	a.Name = name
	return nil
}

// helper to create an account request with auth claims.
func acctReq(method, path string, body interface{}, claims *auth.Claims) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	ctx := auth.WithUser(req.Context(), claims)
	return req.WithContext(ctx)
}

// helper to add account context (accountID + member) to a request.
func withAccountCtx(req *http.Request, accountID int, member *store.AccountMember) *http.Request {
	ctx := context.WithValue(req.Context(), accountIDKey, accountID)
	if member != nil {
		ctx = context.WithValue(ctx, accountMemberKey, member)
	}
	return req.WithContext(ctx)
}

func TestCreateAccount(t *testing.T) {
	claims := &auth.Claims{UserID: 1, Email: "user@test.com"}

	// conflictErr simulates a PostgreSQL unique-violation error so store.IsConflict returns true.
	conflictErr := &pgconn.PgError{Code: "23505"}

	tests := []struct {
		name       string
		body       map[string]string
		claims     *auth.Claims
		setup      func(*mockAccountStore)
		wantStatus int
		wantErr    string
	}{
		{
			name:       "happy path",
			body:       map[string]string{"name": "My Team", "slug": "my-team"},
			claims:     claims,
			wantStatus: http.StatusCreated,
		},
		{
			name:   "slug conflict",
			body:   map[string]string{"name": "My Team", "slug": "taken-slug"},
			claims: claims,
			setup: func(ms *mockAccountStore) {
				ms.createErr = conflictErr
			},
			wantStatus: http.StatusConflict,
			wantErr:    "slug already exists",
		},
		{
			name:       "invalid slug — uppercase",
			body:       map[string]string{"name": "My Team", "slug": "Bad-Slug"},
			claims:     claims,
			wantStatus: http.StatusBadRequest,
			wantErr:    "invalid slug",
		},
		{
			name:       "invalid slug — too short",
			body:       map[string]string{"name": "My Team", "slug": "a"},
			claims:     claims,
			wantStatus: http.StatusBadRequest,
			wantErr:    "invalid slug",
		},
		{
			name:       "invalid slug — leading hyphen",
			body:       map[string]string{"name": "My Team", "slug": "-my-team"},
			claims:     claims,
			wantStatus: http.StatusBadRequest,
			wantErr:    "invalid slug",
		},
		{
			name:       "empty slug",
			body:       map[string]string{"name": "My Team", "slug": ""},
			claims:     claims,
			wantStatus: http.StatusBadRequest,
			wantErr:    "invalid slug",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newMockAccountStore()
			if tt.setup != nil {
				tt.setup(ms)
			}
			srv := &Server{store: ms}

			req := acctReq("POST", "/api/accounts", tt.body, tt.claims)
			rr := httptest.NewRecorder()

			http.HandlerFunc(srv.handleCreateAccount).ServeHTTP(rr, req)

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
				var resp store.Account
				_ = json.NewDecoder(rr.Body).Decode(&resp)
				if resp.ID == 0 {
					t.Error("expected account ID to be set")
				}
				if resp.Slug != tt.body["slug"] {
					t.Errorf("slug = %q, want %q", resp.Slug, tt.body["slug"])
				}
			}
		})
	}
}

func TestListAccounts(t *testing.T) {
	tests := []struct {
		name       string
		claims     *auth.Claims
		setup      func(*mockAccountStore)
		wantStatus int
		wantCount  int
	}{
		{
			name:   "returns user accounts",
			claims: &auth.Claims{UserID: 1, Email: "user@test.com"},
			setup: func(ms *mockAccountStore) {
				ms.userAccounts[1] = []store.Account{
					{ID: 1, Name: "Team A", Slug: "team-a"},
					{ID: 2, Name: "Team B", Slug: "team-b"},
				}
			},
			wantStatus: http.StatusOK,
			wantCount:  2,
		},
		{
			name:       "empty list",
			claims:     &auth.Claims{UserID: 99, Email: "new@test.com"},
			wantStatus: http.StatusOK,
			wantCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newMockAccountStore()
			if tt.setup != nil {
				tt.setup(ms)
			}
			srv := &Server{store: ms}

			req := acctReq("GET", "/api/accounts", nil, tt.claims)
			rr := httptest.NewRecorder()

			http.HandlerFunc(srv.handleListAccounts).ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d. Body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}

			var accounts []store.Account
			if err := json.NewDecoder(rr.Body).Decode(&accounts); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(accounts) != tt.wantCount {
				t.Errorf("got %d accounts, want %d", len(accounts), tt.wantCount)
			}
		})
	}
}

func TestGetAccount(t *testing.T) {
	tests := []struct {
		name       string
		accountID  int
		setup      func(*mockAccountStore)
		wantStatus int
	}{
		{
			name:      "found",
			accountID: 1,
			setup: func(ms *mockAccountStore) {
				ms.accounts[1] = &store.Account{ID: 1, Name: "Team A", Slug: "team-a"}
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "not found",
			accountID:  999,
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newMockAccountStore()
			if tt.setup != nil {
				tt.setup(ms)
			}
			srv := &Server{store: ms}

			req := acctReq("GET", "/api/accounts/1", nil, &auth.Claims{UserID: 1, Email: "user@test.com"})
			req = withAccountCtx(req, tt.accountID, nil)
			rr := httptest.NewRecorder()

			http.HandlerFunc(srv.handleGetAccount).ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d. Body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if tt.wantStatus == http.StatusOK {
				var resp store.Account
				_ = json.NewDecoder(rr.Body).Decode(&resp)
				if resp.ID != tt.accountID {
					t.Errorf("id = %d, want %d", resp.ID, tt.accountID)
				}
			}
		})
	}
}

func TestListMembers(t *testing.T) {
	ms := newMockAccountStore()
	ms.members[1] = []store.AccountMember{
		{AccountID: 1, UserID: 1, Role: "owner", Email: "owner@test.com"},
		{AccountID: 1, UserID: 2, Role: "member", Email: "member@test.com"},
		{AccountID: 1, UserID: 3, Role: "admin", Email: "admin@test.com"},
	}

	srv := &Server{store: ms}

	req := acctReq("GET", "/api/accounts/1/members", nil, &auth.Claims{UserID: 1, Email: "owner@test.com"})
	req = withAccountCtx(req, 1, &store.AccountMember{AccountID: 1, UserID: 1, Role: "owner"})
	rr := httptest.NewRecorder()

	http.HandlerFunc(srv.handleListMembers).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", rr.Code, rr.Body.String())
	}

	var members []store.AccountMember
	if err := json.NewDecoder(rr.Body).Decode(&members); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(members) != 3 {
		t.Errorf("got %d members, want 3", len(members))
	}
}

func TestUpdateAccount(t *testing.T) {
	ownerMember := &store.AccountMember{AccountID: 1, UserID: 1, Role: "owner"}
	memberMember := &store.AccountMember{AccountID: 1, UserID: 2, Role: "member"}
	adminMember := &store.AccountMember{AccountID: 1, UserID: 3, Role: "admin"}

	tests := []struct {
		name       string
		body       map[string]string
		member     *store.AccountMember
		wantStatus int
		wantName   string
		wantErr    string
	}{
		{
			name:       "owner can rename",
			body:       map[string]string{"name": "New Name"},
			member:     ownerMember,
			wantStatus: http.StatusOK,
			wantName:   "New Name",
		},
		{
			name:       "member cannot rename",
			body:       map[string]string{"name": "New Name"},
			member:     memberMember,
			wantStatus: http.StatusForbidden,
			wantErr:    "owner",
		},
		{
			name:       "admin cannot rename",
			body:       map[string]string{"name": "New Name"},
			member:     adminMember,
			wantStatus: http.StatusForbidden,
			wantErr:    "owner",
		},
		{
			name:       "empty name",
			body:       map[string]string{"name": ""},
			member:     ownerMember,
			wantStatus: http.StatusBadRequest,
			wantErr:    "1-100 characters",
		},
		{
			name:       "whitespace-only name",
			body:       map[string]string{"name": "   "},
			member:     ownerMember,
			wantStatus: http.StatusBadRequest,
			wantErr:    "1-100 characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newMockAccountStore()
			ms.accounts[1] = &store.Account{ID: 1, Name: "Old Name", Slug: "team-a"}
			srv := &Server{store: ms}

			req := acctReq("PATCH", "/api/accounts/1", tt.body, &auth.Claims{UserID: tt.member.UserID, Email: "user@test.com"})
			req = withAccountCtx(req, 1, tt.member)
			rr := httptest.NewRecorder()

			http.HandlerFunc(srv.handleUpdateAccount).ServeHTTP(rr, req)

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
			if tt.wantName != "" {
				var resp store.Account
				_ = json.NewDecoder(rr.Body).Decode(&resp)
				if resp.Name != tt.wantName {
					t.Errorf("name = %q, want %q", resp.Name, tt.wantName)
				}
			}
		})
	}
}

// --- Plugin stubs (satisfy store.Store interface) ---

func (m *mockAccountStore) ListPluginCatalog(_ context.Context) ([]store.PluginCatalogEntry, error) {
	return nil, nil
}
func (m *mockAccountStore) GetPluginCatalogEntry(_ context.Context, _ string) (*store.PluginCatalogEntry, error) {
	return nil, fmt.Errorf("not found")
}
func (m *mockAccountStore) CreatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockAccountStore) UpdatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockAccountStore) DeletePluginCatalogEntry(_ context.Context, _ string) error {
	return nil
}
func (m *mockAccountStore) ListMachinePlugins(_ context.Context, _ string) ([]store.MachinePlugin, error) {
	return nil, nil
}
func (m *mockAccountStore) EnableMachinePlugin(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockAccountStore) DisableMachinePlugin(_ context.Context, _, _ string) error {
	return nil
}
func (m *mockAccountStore) UpdateMachinePluginOverrides(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockAccountStore) UpdateMachinePluginStatus(_ context.Context, _, _, _, _ string) error {
	return nil
}
