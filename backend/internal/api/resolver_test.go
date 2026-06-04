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

// mockResolverStore embeds store.Store so unimplemented methods satisfy the
// interface at compile time.  Only the two lookup methods are wired up.
type mockResolverStore struct {
	store.Store
	byCfSub map[string]*store.User
	byEmail map[string]*store.User
}

func (m *mockResolverStore) GetUserByCfSub(_ context.Context, cfSub string) (*store.User, error) {
	if u, ok := m.byCfSub[cfSub]; ok {
		return u, nil
	}
	return nil, fmt.Errorf("user not found by cf_sub %q", cfSub)
}

func (m *mockResolverStore) GetUserByEmail(_ context.Context, email string) (*store.User, error) {
	if u, ok := m.byEmail[email]; ok {
		return u, nil
	}
	return nil, fmt.Errorf("user not found by email %q", email)
}

// claimsCapture is a test handler that records the claims seen after the
// middleware runs.
func claimsCapture(dst **auth.Claims) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*dst = auth.UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
}

func TestUserResolver_AlreadyResolved(t *testing.T) {
	srv := &Server{store: &mockResolverStore{}}

	var got *auth.Claims
	mw := srv.userResolverMiddleware(claimsCapture(&got))

	// Pre-populate context with a Claims that already has a UserID.
	claims := &auth.Claims{UserID: 42, Email: "already@test.com"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(auth.WithUser(req.Context(), claims))

	mw.ServeHTTP(httptest.NewRecorder(), req)

	if got == nil {
		t.Fatal("expected claims in context, got nil")
	}
	if got.UserID != 42 {
		t.Fatalf("expected UserID=42, got %d", got.UserID)
	}
}

func TestUserResolver_CfSubFound(t *testing.T) {
	mock := &mockResolverStore{
		byCfSub: map[string]*store.User{
			"cf-123": {ID: 10, Email: "cf@test.com", CfSub: "cf-123"},
		},
	}
	srv := &Server{store: mock}

	var got *auth.Claims
	mw := srv.userResolverMiddleware(claimsCapture(&got))

	claims := &auth.Claims{UserID: 0, CfSub: "cf-123"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(auth.WithUser(req.Context(), claims))

	mw.ServeHTTP(httptest.NewRecorder(), req)

	if got == nil {
		t.Fatal("expected claims in context, got nil")
	}
	if got.UserID != 10 {
		t.Fatalf("expected UserID=10, got %d", got.UserID)
	}
}

func TestUserResolver_CfSubMiss_EmailFound(t *testing.T) {
	mock := &mockResolverStore{
		byCfSub: map[string]*store.User{}, // cf-miss won't match
		byEmail: map[string]*store.User{
			"found@test.com": {ID: 20, Email: "found@test.com"},
		},
	}
	srv := &Server{store: mock}

	var got *auth.Claims
	mw := srv.userResolverMiddleware(claimsCapture(&got))

	claims := &auth.Claims{UserID: 0, CfSub: "cf-miss", Email: "found@test.com"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(auth.WithUser(req.Context(), claims))

	mw.ServeHTTP(httptest.NewRecorder(), req)

	if got == nil {
		t.Fatal("expected claims in context, got nil")
	}
	if got.UserID != 20 {
		t.Fatalf("expected UserID=20, got %d", got.UserID)
	}
}

func TestUserResolver_NeitherFound(t *testing.T) {
	mock := &mockResolverStore{
		byCfSub: map[string]*store.User{},
		byEmail: map[string]*store.User{},
	}
	srv := &Server{store: mock}

	var got *auth.Claims
	mw := srv.userResolverMiddleware(claimsCapture(&got))

	claims := &auth.Claims{UserID: 0, CfSub: "miss", Email: "miss@test.com"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(auth.WithUser(req.Context(), claims))

	mw.ServeHTTP(httptest.NewRecorder(), req)

	if got == nil {
		t.Fatal("expected claims in context, got nil")
	}
	if got.UserID != 0 {
		t.Fatalf("expected UserID=0, got %d", got.UserID)
	}
}

func TestUserResolver_NoClaims(t *testing.T) {
	srv := &Server{store: &mockResolverStore{}}

	var got *auth.Claims
	mw := srv.userResolverMiddleware(claimsCapture(&got))

	// No claims in context at all.
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	mw.ServeHTTP(httptest.NewRecorder(), req)

	// Handler should still be called (no panic) with nil claims.
	if got != nil {
		t.Fatalf("expected nil claims, got %+v", got)
	}
}

// --- Plugin stubs (satisfy store.Store interface) ---

func (m *mockResolverStore) ListPluginCatalog(_ context.Context) ([]store.PluginCatalogEntry, error) {
	return nil, nil
}
func (m *mockResolverStore) GetPluginCatalogEntry(_ context.Context, _ string) (*store.PluginCatalogEntry, error) {
	return nil, fmt.Errorf("not found")
}
func (m *mockResolverStore) CreatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockResolverStore) UpdatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockResolverStore) DeletePluginCatalogEntry(_ context.Context, _ string) error {
	return nil
}
func (m *mockResolverStore) ListMachinePlugins(_ context.Context, _ string) ([]store.MachinePlugin, error) {
	return nil, nil
}
func (m *mockResolverStore) EnableMachinePlugin(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockResolverStore) DisableMachinePlugin(_ context.Context, _, _ string) error {
	return nil
}
func (m *mockResolverStore) UpdateMachinePluginOverrides(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockResolverStore) UpdateMachinePluginStatus(_ context.Context, _, _, _, _ string) error {
	return nil
}
