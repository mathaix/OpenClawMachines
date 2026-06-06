package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// mockDevStore implements the minimal store methods needed for dev mode testing.
type mockDevStore struct {
	store.Store
	users      map[string]*store.User // keyed by cf_sub
	usersByID  map[int]*store.User
	usersEmail map[string]*store.User
	accounts   map[int]*store.Account
	members    map[int]map[int]*store.AccountMember // accountID -> userID -> member
	nextUserID int
	nextAcctID int

	createUserCalled    bool
	createAccountCalled bool
}

func TestSessionCookieAttributesFollowRequestScheme(t *testing.T) {
	srv := &Server{dataPlaneDomain: "localhost"}

	httpReq := httptest.NewRequest(http.MethodGet, "http://localhost:8080/api/auth/me", nil)
	httpCookie := srv.sessionCookie(httpReq, "token", 86400)
	if httpCookie.Secure {
		t.Fatal("local HTTP session cookie must not be Secure")
	}
	if httpCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("local HTTP SameSite = %v, want Lax", httpCookie.SameSite)
	}
	if httpCookie.Domain != "" {
		t.Fatalf("local HTTP Domain = %q, want empty", httpCookie.Domain)
	}

	httpsReq := httptest.NewRequest(http.MethodGet, "http://api.example.com/api/auth/me", nil)
	httpsReq.Header.Set("X-Forwarded-Proto", "https")
	srv.dataPlaneDomain = "example.com"
	httpsCookie := srv.sessionCookie(httpsReq, "token", 86400)
	if !httpsCookie.Secure {
		t.Fatal("forwarded HTTPS session cookie must be Secure")
	}
	if httpsCookie.SameSite != http.SameSiteNoneMode {
		t.Fatalf("forwarded HTTPS SameSite = %v, want None", httpsCookie.SameSite)
	}
	if httpsCookie.Domain != ".example.com" {
		t.Fatalf("forwarded HTTPS Domain = %q, want .example.com", httpsCookie.Domain)
	}
}

func newMockDevStore() *mockDevStore {
	return &mockDevStore{
		users:      make(map[string]*store.User),
		usersByID:  make(map[int]*store.User),
		usersEmail: make(map[string]*store.User),
		accounts:   make(map[int]*store.Account),
		members:    make(map[int]map[int]*store.AccountMember),
		nextUserID: 1,
		nextAcctID: 100,
	}
}

func (m *mockDevStore) GetUserByCfSub(_ context.Context, cfSub string) (*store.User, error) {
	if u, ok := m.users[cfSub]; ok {
		return u, nil
	}
	return nil, fmt.Errorf("user not found by cf_sub %q", cfSub)
}

func (m *mockDevStore) GetUserByEmail(_ context.Context, email string) (*store.User, error) {
	if u, ok := m.usersEmail[email]; ok {
		return u, nil
	}
	return nil, fmt.Errorf("user not found by email %q", email)
}

func (m *mockDevStore) GetUser(_ context.Context, id int) (*store.User, error) {
	if u, ok := m.usersByID[id]; ok {
		return u, nil
	}
	return nil, fmt.Errorf("user %d not found", id)
}

func (m *mockDevStore) CreateUser(_ context.Context, u *store.User) error {
	m.createUserCalled = true
	u.ID = m.nextUserID
	m.nextUserID++
	m.users[u.CfSub] = u
	m.usersByID[u.ID] = u
	m.usersEmail[u.Email] = u
	return nil
}

func (m *mockDevStore) CreateAccount(_ context.Context, a *store.Account) error {
	m.createAccountCalled = true
	a.ID = m.nextAcctID
	m.nextAcctID++
	m.accounts[a.ID] = a
	return nil
}

func (m *mockDevStore) CreateAccountWithOwner(ctx context.Context, a *store.Account, ownerUserID int) error {
	if err := m.CreateAccount(ctx, a); err != nil {
		return err
	}
	return m.AddAccountMember(ctx, &store.AccountMember{
		AccountID: a.ID,
		UserID:    ownerUserID,
		Role:      "owner",
	})
}

func (m *mockDevStore) AddAccountMember(_ context.Context, member *store.AccountMember) error {
	if m.members[member.AccountID] == nil {
		m.members[member.AccountID] = make(map[int]*store.AccountMember)
	}
	m.members[member.AccountID][member.UserID] = member
	return nil
}

func (m *mockDevStore) UpdateUserCfSub(_ context.Context, userID int, cfSub string) error {
	if u, ok := m.usersByID[userID]; ok {
		u.CfSub = cfSub
		m.users[cfSub] = u
		return nil
	}
	return fmt.Errorf("user %d not found", userID)
}

// TestDevAuth_NewUser_AutoCreated verifies that in dev mode, a new user
// hitting /api/auth/me is auto-created with a personal account.
func TestDevAuth_NewUser_AutoCreated(t *testing.T) {
	mockStore := newMockDevStore()
	srv := &Server{
		store:    mockStore,
		authMode: "dev",
	}

	// Wire up a minimal router with dev bypass middleware
	handler := auth.DevBypassMiddleware("newuser@dev.local")(
		srv.userResolverMiddleware(
			http.HandlerFunc(srv.handleAuthMe),
		),
	)

	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", rr.Code, rr.Body.String())
	}

	if !mockStore.createUserCalled {
		t.Error("expected CreateUser to be called for new user")
	}
	if !mockStore.createAccountCalled {
		t.Error("expected CreateAccount to be called for new user")
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	user, ok := resp["user"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected user object in response, got %T", resp["user"])
	}

	if user["email"] != "newuser@dev.local" {
		t.Errorf("email = %v, want newuser@dev.local", user["email"])
	}
	if user["cf_sub"] != "dev-bypass" {
		t.Errorf("cf_sub = %v, want dev-bypass", user["cf_sub"])
	}
	if user["auth_provider"] != "cfaccess" {
		t.Errorf("auth_provider = %v, want cfaccess", user["auth_provider"])
	}
}

// TestDevAuth_ExistingUser_Found verifies that an existing user in dev mode
// is returned without creating a new record.
func TestDevAuth_ExistingUser_Found(t *testing.T) {
	mockStore := newMockDevStore()
	existingUser := &store.User{
		ID:           42,
		Email:        "existing@dev.local",
		CfSub:        "dev-bypass",
		Status:       "active",
		AuthProvider: "cfaccess",
	}
	mockStore.users["dev-bypass"] = existingUser
	mockStore.usersByID[42] = existingUser
	mockStore.usersEmail["existing@dev.local"] = existingUser

	srv := &Server{
		store:    mockStore,
		authMode: "dev",
	}

	handler := auth.DevBypassMiddleware("existing@dev.local")(
		srv.userResolverMiddleware(
			http.HandlerFunc(srv.handleAuthMe),
		),
	)

	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", rr.Code, rr.Body.String())
	}

	if mockStore.createUserCalled {
		t.Error("CreateUser should not be called for existing user")
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	user, ok := resp["user"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected user object in response, got %T", resp["user"])
	}

	// Response uses json tags, so id is float64 from json decoder
	if int(user["id"].(float64)) != 42 {
		t.Errorf("id = %v, want 42", user["id"])
	}
	if user["email"] != "existing@dev.local" {
		t.Errorf("email = %v, want existing@dev.local", user["email"])
	}
}

// TestDevAuth_DefaultEmail verifies that dev mode uses dev@localhost when
// DEV_USER_EMAIL is not provided.
func TestDevAuth_DefaultEmail(t *testing.T) {
	mockStore := newMockDevStore()
	srv := &Server{
		store:    mockStore,
		authMode: "dev",
	}

	// Use empty email to trigger default
	handler := auth.DevBypassMiddleware("")(
		srv.userResolverMiddleware(
			http.HandlerFunc(srv.handleAuthMe),
		),
	)

	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	user, ok := resp["user"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected user object in response, got %T", resp["user"])
	}

	if user["email"] != "dev@localhost" {
		t.Errorf("email = %v, want dev@localhost (default)", user["email"])
	}
}

// --- Plugin stubs (satisfy store.Store interface) ---

func (m *mockDevStore) ListPluginCatalog(_ context.Context) ([]store.PluginCatalogEntry, error) {
	return nil, nil
}
func (m *mockDevStore) GetPluginCatalogEntry(_ context.Context, _ string) (*store.PluginCatalogEntry, error) {
	return nil, fmt.Errorf("not found")
}
func (m *mockDevStore) CreatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockDevStore) UpdatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockDevStore) DeletePluginCatalogEntry(_ context.Context, _ string) error {
	return nil
}
func (m *mockDevStore) ListMachinePlugins(_ context.Context, _ string) ([]store.MachinePlugin, error) {
	return nil, nil
}
func (m *mockDevStore) EnableMachinePlugin(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockDevStore) DisableMachinePlugin(_ context.Context, _, _ string) error {
	return nil
}
func (m *mockDevStore) UpdateMachinePluginOverrides(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockDevStore) UpdateMachinePluginStatus(_ context.Context, _, _, _, _ string) error {
	return nil
}
