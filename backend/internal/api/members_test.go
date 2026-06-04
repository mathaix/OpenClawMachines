package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// mockMemberStore implements store methods needed for member tests.
type mockMemberStore struct {
	store.Store

	members  map[int]map[int]*store.AccountMember // accountID -> userID -> member
	accounts map[int]*store.Account
}

func newMockMemberStore() *mockMemberStore {
	return &mockMemberStore{
		members:  make(map[int]map[int]*store.AccountMember),
		accounts: make(map[int]*store.Account),
	}
}

func (m *mockMemberStore) GetAccountMember(_ context.Context, accountID, userID int) (*store.AccountMember, error) {
	if members, ok := m.members[accountID]; ok {
		if member, ok := members[userID]; ok {
			return member, nil
		}
	}
	return nil, fmt.Errorf("member not found")
}

func (m *mockMemberStore) UpdateAccountMemberRole(_ context.Context, accountID, userID int, role string) error {
	if members, ok := m.members[accountID]; ok {
		if member, ok := members[userID]; ok {
			member.Role = role
			return nil
		}
	}
	return fmt.Errorf("member not found")
}

func (m *mockMemberStore) RemoveAccountMember(_ context.Context, accountID, userID int) error {
	if members, ok := m.members[accountID]; ok {
		if _, ok := members[userID]; ok {
			delete(members, userID)
			return nil
		}
	}
	return fmt.Errorf("member not found")
}

func (m *mockMemberStore) GetAccount(_ context.Context, id int) (*store.Account, error) {
	if a, ok := m.accounts[id]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("account %d not found", id)
}

func (m *mockMemberStore) ListAccountMembers(_ context.Context, accountID int) ([]store.AccountMember, error) {
	var result []store.AccountMember
	if members, ok := m.members[accountID]; ok {
		for _, member := range members {
			result = append(result, *member)
		}
	}
	return result, nil
}

// helper to create a member request with auth claims, account context, and member context.
func memberReq(method, path string, body interface{}, claims *auth.Claims, accountID int, member *store.AccountMember) *http.Request {
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

// helper to set chi URL params on a request (for userId param).
func withUserIDParam(req *http.Request, userID string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("userId", userID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

// addMember is a test helper that adds a member to the mock store.
func addMember(ms *mockMemberStore, accountID, userID int, role string) {
	if ms.members[accountID] == nil {
		ms.members[accountID] = make(map[int]*store.AccountMember)
	}
	ms.members[accountID][userID] = &store.AccountMember{
		AccountID: accountID,
		UserID:    userID,
		Role:      role,
	}
}

func TestUpdateMemberRole(t *testing.T) {
	ownerClaims := &auth.Claims{UserID: 1, Email: "owner@test.com"}
	ownerMember := &store.AccountMember{AccountID: 1, UserID: 1, Role: "owner"}
	adminClaims := &auth.Claims{UserID: 2, Email: "admin@test.com"}
	adminMember := &store.AccountMember{AccountID: 1, UserID: 2, Role: "admin"}

	tests := []struct {
		name         string
		body         map[string]string
		claims       *auth.Claims
		member       *store.AccountMember
		targetUserID string
		setup        func(*mockMemberStore)
		wantStatus   int
		wantErr      string
	}{
		{
			name:         "owner changes member to admin",
			body:         map[string]string{"role": "admin"},
			claims:       ownerClaims,
			member:       ownerMember,
			targetUserID: "3",
			setup: func(ms *mockMemberStore) {
				addMember(ms, 1, 3, "member")
			},
			wantStatus: http.StatusOK,
		},
		{
			name:         "owner changes admin to member",
			body:         map[string]string{"role": "member"},
			claims:       ownerClaims,
			member:       ownerMember,
			targetUserID: "2",
			setup: func(ms *mockMemberStore) {
				addMember(ms, 1, 2, "admin")
			},
			wantStatus: http.StatusOK,
		},
		{
			name:         "admin cannot change roles",
			body:         map[string]string{"role": "admin"},
			claims:       adminClaims,
			member:       adminMember,
			targetUserID: "3",
			setup: func(ms *mockMemberStore) {
				addMember(ms, 1, 3, "member")
			},
			wantStatus: http.StatusForbidden,
			wantErr:    "owner role required",
		},
		{
			name:         "cannot change owner role",
			body:         map[string]string{"role": "admin"},
			claims:       ownerClaims,
			member:       ownerMember,
			targetUserID: "5",
			setup: func(ms *mockMemberStore) {
				addMember(ms, 1, 5, "owner")
			},
			wantStatus: http.StatusForbidden,
			wantErr:    "cannot change the owner",
		},
		{
			name:         "target not found",
			body:         map[string]string{"role": "admin"},
			claims:       ownerClaims,
			member:       ownerMember,
			targetUserID: "999",
			wantStatus:   http.StatusNotFound,
			wantErr:      "member not found",
		},
		{
			name:         "cannot change own role",
			body:         map[string]string{"role": "admin"},
			claims:       ownerClaims,
			member:       ownerMember,
			targetUserID: "1",
			wantStatus:   http.StatusBadRequest,
			wantErr:      "cannot change your own role",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newMockMemberStore()
			if tt.setup != nil {
				tt.setup(ms)
			}
			srv := &Server{store: ms}

			req := memberReq("PUT", "/api/accounts/1/members/"+tt.targetUserID+"/role", tt.body, tt.claims, 1, tt.member)
			req = withUserIDParam(req, tt.targetUserID)
			rr := httptest.NewRecorder()

			http.HandlerFunc(srv.handleUpdateMemberRole).ServeHTTP(rr, req)

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
				var resp map[string]string
				_ = json.NewDecoder(rr.Body).Decode(&resp)
				if resp["role"] != tt.body["role"] {
					t.Errorf("role = %q, want %q", resp["role"], tt.body["role"])
				}
			}
		})
	}
}

func TestRemoveMember(t *testing.T) {
	ownerClaims := &auth.Claims{UserID: 1, Email: "owner@test.com"}
	ownerMember := &store.AccountMember{AccountID: 1, UserID: 1, Role: "owner"}
	adminClaims := &auth.Claims{UserID: 2, Email: "admin@test.com"}
	adminMember := &store.AccountMember{AccountID: 1, UserID: 2, Role: "admin"}
	memberClaims := &auth.Claims{UserID: 3, Email: "member@test.com"}
	regularMember := &store.AccountMember{AccountID: 1, UserID: 3, Role: "member"}

	tests := []struct {
		name         string
		claims       *auth.Claims
		member       *store.AccountMember
		targetUserID string
		setup        func(*mockMemberStore)
		wantStatus   int
		wantErr      string
	}{
		{
			name:         "owner removes member",
			claims:       ownerClaims,
			member:       ownerMember,
			targetUserID: "3",
			setup: func(ms *mockMemberStore) {
				addMember(ms, 1, 3, "member")
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:         "admin removes member",
			claims:       adminClaims,
			member:       adminMember,
			targetUserID: "3",
			setup: func(ms *mockMemberStore) {
				addMember(ms, 1, 3, "member")
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:         "cannot remove owner",
			claims:       adminClaims,
			member:       adminMember,
			targetUserID: "1",
			setup: func(ms *mockMemberStore) {
				addMember(ms, 1, 1, "owner")
			},
			wantStatus: http.StatusForbidden,
			wantErr:    "cannot remove the account owner",
		},
		{
			name:         "non-admin cannot remove",
			claims:       memberClaims,
			member:       regularMember,
			targetUserID: "4",
			setup: func(ms *mockMemberStore) {
				addMember(ms, 1, 4, "member")
			},
			wantStatus: http.StatusForbidden,
			wantErr:    "insufficient permissions",
		},
		{
			name:         "admin cannot remove other admin",
			claims:       adminClaims,
			member:       adminMember,
			targetUserID: "5",
			setup: func(ms *mockMemberStore) {
				addMember(ms, 1, 5, "admin")
			},
			wantStatus: http.StatusForbidden,
			wantErr:    "admins cannot remove other admins",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newMockMemberStore()
			if tt.setup != nil {
				tt.setup(ms)
			}
			srv := &Server{store: ms}

			req := memberReq("DELETE", "/api/accounts/1/members/"+tt.targetUserID, nil, tt.claims, 1, tt.member)
			req = withUserIDParam(req, tt.targetUserID)
			rr := httptest.NewRecorder()

			http.HandlerFunc(srv.handleRemoveMember).ServeHTTP(rr, req)

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

func TestLeaveAccount(t *testing.T) {
	tests := []struct {
		name       string
		claims     *auth.Claims
		member     *store.AccountMember
		setup      func(*mockMemberStore)
		wantStatus int
		wantErr    string
	}{
		{
			name:   "member leaves",
			claims: &auth.Claims{UserID: 3, Email: "member@test.com"},
			member: &store.AccountMember{AccountID: 1, UserID: 3, Role: "member"},
			setup: func(ms *mockMemberStore) {
				addMember(ms, 1, 3, "member")
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:   "admin leaves",
			claims: &auth.Claims{UserID: 2, Email: "admin@test.com"},
			member: &store.AccountMember{AccountID: 1, UserID: 2, Role: "admin"},
			setup: func(ms *mockMemberStore) {
				addMember(ms, 1, 2, "admin")
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "owner cannot leave",
			claims:     &auth.Claims{UserID: 1, Email: "owner@test.com"},
			member:     &store.AccountMember{AccountID: 1, UserID: 1, Role: "owner"},
			wantStatus: http.StatusForbidden,
			wantErr:    "owners cannot leave",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newMockMemberStore()
			if tt.setup != nil {
				tt.setup(ms)
			}
			srv := &Server{store: ms}

			req := memberReq("POST", "/api/accounts/1/leave", nil, tt.claims, 1, tt.member)
			rr := httptest.NewRecorder()

			http.HandlerFunc(srv.handleLeaveAccount).ServeHTTP(rr, req)

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

// --- Plugin stubs (satisfy store.Store interface) ---

func (m *mockMemberStore) ListPluginCatalog(_ context.Context) ([]store.PluginCatalogEntry, error) {
	return nil, nil
}
func (m *mockMemberStore) GetPluginCatalogEntry(_ context.Context, _ string) (*store.PluginCatalogEntry, error) {
	return nil, fmt.Errorf("not found")
}
func (m *mockMemberStore) CreatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockMemberStore) UpdatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockMemberStore) DeletePluginCatalogEntry(_ context.Context, _ string) error {
	return nil
}
func (m *mockMemberStore) ListMachinePlugins(_ context.Context, _ string) ([]store.MachinePlugin, error) {
	return nil, nil
}
func (m *mockMemberStore) EnableMachinePlugin(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockMemberStore) DisableMachinePlugin(_ context.Context, _, _ string) error {
	return nil
}
func (m *mockMemberStore) UpdateMachinePluginOverrides(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockMemberStore) UpdateMachinePluginStatus(_ context.Context, _, _, _, _ string) error {
	return nil
}
