# Firebase Authentication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Cloudflare Access customer login with Firebase Authentication, keeping CF Access for admin/internal surfaces and `ocm_token` for Worker routing.

**Architecture:** Frontend uses Firebase JS SDK for sign-in (Google, GitHub, email/password). Frontend sends the Firebase ID token to a new backend endpoint `POST /api/auth/session/exchange`, which verifies it via Google's public JWKS, resolves/creates the user, and issues the existing `ocm_token` cookie. The `DualModeMiddleware` is extended to accept Firebase sessions alongside legacy tokens. CF Access is retained for admin routes. CLI auth uses Firebase + PKCE with localhost callback.

**Tech Stack:** Firebase Auth (JS SDK v11), Go `golang-jwt/jwt/v5` (verify RS256 Firebase tokens via Google JWKS), existing HS256 `ocm_token` for Worker routing.

---

## Prerequisites

Before starting implementation, the user must:

1. Go to [Firebase Console](https://console.firebase.google.com/) → select GCP project `clarateach` (or create a new Firebase project linked to it)
2. Enable **Authentication** → Sign-in methods: Google, GitHub, Email/Password
3. Add authorized domains: `openclawmachines.com`, `localhost`
4. Copy the Firebase config object (apiKey, authDomain, projectId) — these go into frontend env vars

The implementation uses placeholder values until the real config is provided.

## File Structure

### New Files

| File | Responsibility |
|------|---------------|
| `backend/internal/auth/firebase.go` | Firebase ID token verifier (fetches Google JWKS, validates RS256 JWTs with Firebase-specific claims) |
| `backend/internal/auth/firebase_test.go` | Unit tests for Firebase token verification |
| `backend/migrations/045_firebase_auth.sql` | Add `identity_issuer`, `identity_subject` columns to users table |
| `frontend/src/lib/firebase.ts` | Firebase SDK initialization and auth helpers |
| `frontend/src/pages/LoginFirebase.tsx` | Firebase-powered login page with provider buttons |

### Modified Files

| File | Changes |
|------|---------|
| `backend/internal/config/config.go` | Add `FirebaseProjectID` config field |
| `backend/cmd/server/main.go` | Initialize `FirebaseAuth`, add new auth mode `firebase`, wire up `handleSessionExchange` |
| `backend/internal/api/server.go` | Add `firebaseAuth` field, `handleSessionExchange` endpoint, update `handleAuthMe`, update auth mode switch |
| `backend/internal/auth/auth.go` | Update `DualModeMiddleware` to accept Firebase sessions, update `Claims` struct |
| `backend/internal/store/store.go` | Add `GetUserByIdentity` to `UserRepo` interface |
| `backend/internal/store/postgres.go` | Implement `GetUserByIdentity`, update `CreateUser` to set identity columns |
| `frontend/src/lib/auth.tsx` | Replace CF Access session with Firebase session, update logout |
| `frontend/src/lib/api.ts` | Remove `getCfAccessJwt()`, use `ocm_token` cookie (already set by backend) |
| `frontend/src/App.tsx` | Update `ProtectedRoute` to redirect to `/login` instead of reloading for CF Access |
| `frontend/src/pages/CliAuth.tsx` | Update to work with Firebase auth instead of CF Access |
| `frontend/package.json` | Add `firebase` dependency |

---

### Task 1: Database Migration — Add Identity Columns

**Files:**
- Create: `backend/migrations/045_firebase_auth.sql`

- [ ] **Step 1: Write the migration SQL**

```sql
-- Add provider-neutral identity columns for Firebase (and future IdPs)
ALTER TABLE users ADD COLUMN IF NOT EXISTS identity_issuer TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS identity_subject TEXT;

-- Unique constraint: one user per issuer+subject pair
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_identity
  ON users(identity_issuer, identity_subject)
  WHERE identity_issuer IS NOT NULL AND identity_subject IS NOT NULL;

-- Backfill existing CF Access users
UPDATE users
  SET identity_issuer = 'cfaccess',
      identity_subject = cf_sub
  WHERE cf_sub IS NOT NULL
    AND cf_sub != ''
    AND identity_issuer IS NULL;
```

- [ ] **Step 2: Verify migration applies cleanly**

Run: `psql "$DATABASE_URL" -f backend/migrations/045_firebase_auth.sql`
Expected: Three ALTER/CREATE/UPDATE statements succeed without error.

- [ ] **Step 3: Commit**

```bash
git add backend/migrations/045_firebase_auth.sql
git commit -m "feat(auth): add identity_issuer/identity_subject columns for Firebase auth"
```

---

### Task 2: Store Layer — Add Identity Lookup

**Files:**
- Modify: `backend/internal/store/store.go:10-21` (User struct)
- Modify: `backend/internal/store/store.go:472-480` (UserRepo interface)
- Modify: `backend/internal/store/postgres.go:43-114` (user queries)

- [ ] **Step 1: Write the failing test**

Create test in `backend/internal/store/postgres_test.go` (or confirm test file exists and add to it):

```go
func TestGetUserByIdentity(t *testing.T) {
	// This test requires a real database connection.
	// Skip if DATABASE_URL is not set.
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := NewPostgresStore(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()

	// Create a user with identity fields
	user := &User{
		Email:           "firebase-test@example.com",
		Name:            "Firebase Test",
		AuthProvider:    "firebase",
		AuthProviderID:  "firebase-uid-123",
		IdentityIssuer:  strPtr("https://securetoken.google.com/clarateach"),
		IdentitySubject: strPtr("firebase-uid-123"),
		Status:          "active",
	}
	err = db.CreateUser(ctx, user)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Look up by identity
	found, err := db.GetUserByIdentity(ctx, "https://securetoken.google.com/clarateach", "firebase-uid-123")
	if err != nil {
		t.Fatalf("get by identity: %v", err)
	}
	if found.ID != user.ID {
		t.Errorf("expected user ID %d, got %d", user.ID, found.ID)
	}

	// Cleanup
	_, _ = db.pool.Exec(ctx, "DELETE FROM users WHERE id = $1", user.ID)
}

func strPtr(s string) *string { return &s }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/store/ -run TestGetUserByIdentity -v`
Expected: Compile error — `IdentityIssuer` field doesn't exist on User struct yet.

- [ ] **Step 3: Update User struct in store.go**

In `backend/internal/store/store.go`, add fields to the `User` struct:

```go
type User struct {
	ID              int       `json:"id"`
	Email           string    `json:"email"`
	Name            string    `json:"name"`
	AvatarURL       *string   `json:"avatar_url,omitempty"`
	AuthProvider    string    `json:"auth_provider"`
	AuthProviderID  string    `json:"auth_provider_id"`
	CfSub           string    `json:"cf_sub,omitempty"`
	IdentityIssuer  *string   `json:"identity_issuer,omitempty"`
	IdentitySubject *string   `json:"identity_subject,omitempty"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
}
```

- [ ] **Step 4: Add GetUserByIdentity to UserRepo interface**

In `backend/internal/store/store.go`, add to `UserRepo` interface:

```go
type UserRepo interface {
	CreateUser(ctx context.Context, user *User) error
	GetUser(ctx context.Context, id int) (*User, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	GetUserByProvider(ctx context.Context, provider, providerID string) (*User, error)
	GetUserByCfSub(ctx context.Context, cfSub string) (*User, error)
	GetUserByIdentity(ctx context.Context, issuer, subject string) (*User, error)
	UpdateUserProvider(ctx context.Context, userID int, provider, providerID string, avatarURL *string) error
	UpdateUserCfSub(ctx context.Context, userID int, cfSub string) error
}
```

- [ ] **Step 5: Implement GetUserByIdentity and update queries in postgres.go**

In `backend/internal/store/postgres.go`, update `CreateUser` to include identity columns:

```go
func (s *PostgresStore) CreateUser(ctx context.Context, user *User) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO users (email, name, avatar_url, auth_provider, auth_provider_id, cf_sub, identity_issuer, identity_subject, status)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8, COALESCE(NULLIF($9, ''), 'active'))
		 RETURNING id, created_at`,
		user.Email, user.Name, user.AvatarURL, user.AuthProvider, user.AuthProviderID,
		user.CfSub, user.IdentityIssuer, user.IdentitySubject, user.Status,
	).Scan(&user.ID, &user.CreatedAt)
}
```

Update all `SELECT` queries (GetUser, GetUserByEmail, GetUserByProvider, GetUserByCfSub) to include the new columns. The scan pattern for each becomes:

```go
`SELECT id, email, name, avatar_url, auth_provider, auth_provider_id, COALESCE(cf_sub, ''), identity_issuer, identity_subject, status, created_at
 FROM users WHERE ...`
).Scan(&u.ID, &u.Email, &u.Name, &u.AvatarURL, &u.AuthProvider, &u.AuthProviderID, &u.CfSub, &u.IdentityIssuer, &u.IdentitySubject, &u.Status, &u.CreatedAt)
```

Add the new `GetUserByIdentity` method:

```go
func (s *PostgresStore) GetUserByIdentity(ctx context.Context, issuer, subject string) (*User, error) {
	u := &User{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, name, avatar_url, auth_provider, auth_provider_id, COALESCE(cf_sub, ''), identity_issuer, identity_subject, status, created_at
		 FROM users WHERE identity_issuer = $1 AND identity_subject = $2`, issuer, subject,
	).Scan(&u.ID, &u.Email, &u.Name, &u.AvatarURL, &u.AuthProvider, &u.AuthProviderID, &u.CfSub, &u.IdentityIssuer, &u.IdentitySubject, &u.Status, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/store/ -run TestGetUserByIdentity -v`
Expected: PASS

- [ ] **Step 7: Run full Go test suite**

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./...`
Expected: Build succeeds (no compile errors from updated scan signatures).

- [ ] **Step 8: Commit**

```bash
git add backend/internal/store/store.go backend/internal/store/postgres.go
git commit -m "feat(auth): add identity_issuer/identity_subject to User and GetUserByIdentity"
```

---

### Task 3: Firebase Token Verifier (Backend)

**Files:**
- Create: `backend/internal/auth/firebase.go`
- Create: `backend/internal/auth/firebase_test.go`

- [ ] **Step 1: Write the failing test**

Create `backend/internal/auth/firebase_test.go`:

```go
package auth

import (
	"testing"
)

func TestNewFirebaseAuth(t *testing.T) {
	fa := NewFirebaseAuth("clarateach")
	if fa == nil {
		t.Fatal("expected non-nil FirebaseAuth")
	}
	if fa.projectID != "clarateach" {
		t.Errorf("expected projectID 'clarateach', got %q", fa.projectID)
	}
}

func TestFirebaseAuth_ValidateToken_InvalidToken(t *testing.T) {
	fa := NewFirebaseAuth("clarateach")
	_, err := fa.ValidateToken("not.a.valid.token")
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
}

func TestFirebaseAuth_ValidateToken_ExpiredToken(t *testing.T) {
	fa := NewFirebaseAuth("clarateach")
	// A well-formed but expired JWT (three base64 segments, valid header)
	// This should fail validation (expired or bad signature)
	_, err := fa.ValidateToken("eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJodHRwczovL3NlY3VyZXRva2VuLmdvb2dsZS5jb20vY2xhcmF0ZWFjaCIsInN1YiI6InRlc3QiLCJhdWQiOiJjbGFyYXRlYWNoIiwiZXhwIjoxMDAwMDAwMDAwfQ.invalidsig")
	if err == nil {
		t.Fatal("expected error for expired/invalid token")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/auth/ -run TestFirebaseAuth -v`
Expected: Compile error — `NewFirebaseAuth` not defined.

- [ ] **Step 3: Implement FirebaseAuth**

Create `backend/internal/auth/firebase.go`:

```go
package auth

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// FirebaseAuth validates Firebase Authentication ID tokens.
// It fetches Google's public JWKS to verify RS256 signatures.
type FirebaseAuth struct {
	projectID string

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	lastFetch time.Time
}

// FirebaseClaims represents the claims in a Firebase ID token.
type FirebaseClaims struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
	jwt.RegisteredClaims
}

// NewFirebaseAuth creates a new FirebaseAuth for the given GCP project ID.
func NewFirebaseAuth(projectID string) *FirebaseAuth {
	return &FirebaseAuth{
		projectID: projectID,
		keys:      make(map[string]*rsa.PublicKey),
	}
}

const firebaseJWKSURL = "https://www.googleapis.com/service_accounts/v1/jwk/securetoken@system.gserviceaccount.com"

// fetchJWKS fetches and caches Google's public keys for Firebase token verification.
func (f *FirebaseAuth) fetchJWKS() error {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(firebaseJWKSURL)
	if err != nil {
		return fmt.Errorf("fetch Firebase JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch Firebase JWKS: status %d", resp.StatusCode)
	}

	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("parse Firebase JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			slog.Warn("firebase.jwks.decode_n", "kid", k.Kid, "error", err)
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			slog.Warn("firebase.jwks.decode_e", "kid", k.Kid, "error", err)
			continue
		}
		n := new(big.Int).SetBytes(nBytes)
		e := 0
		for _, b := range eBytes {
			e = e<<8 + int(b)
		}
		keys[k.Kid] = &rsa.PublicKey{N: n, E: e}
	}

	if len(keys) == 0 {
		return fmt.Errorf("Firebase JWKS contains no valid RSA keys")
	}

	f.mu.Lock()
	f.keys = keys
	f.lastFetch = time.Now()
	f.mu.Unlock()

	slog.Info("firebase.jwks.refreshed", "key_count", len(keys))
	return nil
}

// getKey retrieves a cached public key by kid, refreshing JWKS if needed.
func (f *FirebaseAuth) getKey(kid string) (*rsa.PublicKey, error) {
	f.mu.RLock()
	key, ok := f.keys[kid]
	stale := time.Since(f.lastFetch) > time.Hour
	f.mu.RUnlock()

	if ok && !stale {
		return key, nil
	}

	if err := f.fetchJWKS(); err != nil {
		if ok {
			slog.Error("firebase.jwks.refresh_failed_stale", "error", err)
			return nil, fmt.Errorf("JWKS stale and refresh failed: %w", err)
		}
		return nil, fmt.Errorf("JWKS fetch failed: %w", err)
	}

	f.mu.RLock()
	key, ok = f.keys[kid]
	f.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown kid: %s", kid)
	}
	return key, nil
}

// ValidateToken validates a Firebase ID token and returns the claims.
// It checks: RS256 signature, issuer, audience, and expiry.
func (f *FirebaseAuth) ValidateToken(tokenString string) (*FirebaseClaims, error) {
	claims := &FirebaseClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		kid, ok := t.Header["kid"].(string)
		if !ok {
			return nil, fmt.Errorf("missing kid in token header")
		}
		return f.getKey(kid)
	},
		jwt.WithIssuer(fmt.Sprintf("https://securetoken.google.com/%s", f.projectID)),
		jwt.WithAudience(f.projectID),
	)

	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	if claims.Subject == "" {
		return nil, fmt.Errorf("token missing sub claim")
	}

	return claims, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/auth/ -run TestFirebaseAuth -v`
Expected: All three tests pass (NewFirebaseAuth, InvalidToken, ExpiredToken).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/auth/firebase.go backend/internal/auth/firebase_test.go
git commit -m "feat(auth): add Firebase ID token verifier with Google JWKS"
```

---

### Task 4: Config + Server Wiring — Add Firebase Auth Mode

**Files:**
- Modify: `backend/internal/config/config.go:10-54` (Config struct)
- Modify: `backend/cmd/server/main.go:172-195` (auth setup)
- Modify: `backend/internal/api/server.go:43-76` (Server struct)
- Modify: `backend/internal/api/server.go:143` (NewServer signature)
- Modify: `backend/internal/api/server.go:275-294` (auth mode switch + routes)

- [ ] **Step 1: Add FirebaseProjectID to Config**

In `backend/internal/config/config.go`, add to Config struct after `OpikAPIKey`:

```go
	FirebaseProjectID string // Firebase project ID for ID token verification
```

In the `Load()` function, after the `OpikAPIKey` line:

```go
	cfg.FirebaseProjectID = getEnv("FIREBASE_PROJECT_ID", "clarateach")
```

- [ ] **Step 2: Add firebaseAuth to Server struct**

In `backend/internal/api/server.go`, add to the Server struct:

```go
	firebaseAuth         *auth.FirebaseAuth
```

- [ ] **Step 3: Update NewServer to accept and store FirebaseAuth**

In `backend/internal/api/server.go`, update the `NewServer` function signature to add `firebaseAuth *auth.FirebaseAuth` parameter (after `cfAuth`).

Store it in the server:
```go
	srv := &Server{
		// ... existing fields ...
		firebaseAuth:         firebaseAuth,
		// ...
	}
```

- [ ] **Step 4: Add "firebase" auth mode to server.go route setup**

In the auth mode switch (around line 276), add a new case:

```go
	case "firebase":
		r.Use(auth.FirebaseMiddleware(srv.firebaseAuth, a))
		r.Use(srv.userResolverMiddleware)
```

- [ ] **Step 5: Add public session exchange route**

Right after the public routes block (after `r.Get("/api/invitations/{token}/public", ...)`), add:

```go
	// Firebase session exchange (public — token is the auth)
	if srv.firebaseAuth != nil {
		r.Post("/api/auth/session/exchange", srv.handleSessionExchange)
	}
```

- [ ] **Step 6: Update main.go auth setup to handle firebase mode**

In `backend/cmd/server/main.go`, update the auth setup switch:

```go
	var a *auth.Auth
	var cfAuth *auth.CfAccessAuth
	var firebaseAuth *auth.FirebaseAuth
	switch cfg.AuthMode {
	case "dev":
		slog.Warn("auth.dev_mode_enabled", "email", cfg.DevUserEmail)
		if cfg.JWTSecret != "" && len(cfg.JWTSecret) >= 16 {
			a = auth.New(cfg.JWTSecret)
		}
	case "cfaccess":
		if cfg.CfAccessTeamDomain == "" || cfg.CfAccessAUD == "" {
			slog.Error("cfaccess.config_missing", "error", "CF_ACCESS_TEAM_DOMAIN and CF_ACCESS_AUD required for cfaccess mode")
			os.Exit(1)
		}
		cfAuth = auth.NewCfAccess(cfg.CfAccessTeamDomain, cfg.CfAccessAUD)
		if cfg.JWTSecret != "" && len(cfg.JWTSecret) >= 16 {
			a = auth.New(cfg.JWTSecret)
		}
	case "firebase":
		if cfg.FirebaseProjectID == "" {
			slog.Error("firebase.config_missing", "error", "FIREBASE_PROJECT_ID required for firebase mode")
			os.Exit(1)
		}
		firebaseAuth = auth.NewFirebaseAuth(cfg.FirebaseProjectID)
		if cfg.JWTSecret != "" && len(cfg.JWTSecret) >= 16 {
			a = auth.New(cfg.JWTSecret)
		}
		slog.Info("auth.firebase_enabled", "project_id", cfg.FirebaseProjectID)
	default:
		slog.Error("auth.unknown_mode", "mode", cfg.AuthMode)
		os.Exit(1)
	}
```

Update the `NewServer` call to pass `firebaseAuth`.

- [ ] **Step 7: Build to verify compilation**

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./...`
Expected: Build succeeds.

- [ ] **Step 8: Commit**

```bash
git add backend/internal/config/config.go backend/cmd/server/main.go backend/internal/api/server.go
git commit -m "feat(auth): wire Firebase auth mode into config and server"
```

---

### Task 5: FirebaseMiddleware + Session Exchange Endpoint

**Files:**
- Modify: `backend/internal/auth/auth.go` (add FirebaseMiddleware)
- Modify: `backend/internal/api/server.go` (add handleSessionExchange)

- [ ] **Step 1: Write test for FirebaseMiddleware**

In `backend/internal/auth/firebase_test.go`, add:

```go
func TestFirebaseMiddleware_FallsBackToLegacyToken(t *testing.T) {
	// When no Firebase token is present, middleware should try legacy ocm_token
	legacyAuth := New("test-secret-at-least16")
	token, err := legacyAuth.GenerateToken(42, "user@example.com")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	fa := NewFirebaseAuth("test-project")
	middleware := FirebaseMiddleware(fa, legacyAuth)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := UserFromContext(r.Context())
		if claims == nil {
			t.Fatal("expected claims in context")
		}
		if claims.UserID != 42 {
			t.Errorf("expected UserID 42, got %d", claims.UserID)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.AddCookie(&http.Cookie{Name: "ocm_token", Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestFirebaseMiddleware_RejectsNoAuth(t *testing.T) {
	fa := NewFirebaseAuth("test-project")
	legacyAuth := New("test-secret-at-least16")
	middleware := FirebaseMiddleware(fa, legacyAuth)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/auth/ -run TestFirebaseMiddleware -v`
Expected: Compile error — `FirebaseMiddleware` not defined.

- [ ] **Step 3: Implement FirebaseMiddleware**

In `backend/internal/auth/auth.go`, add:

```go
// FirebaseMiddleware returns a middleware that tries the legacy ocm_token first
// (Bearer header or cookie), then rejects. Firebase ID tokens are only accepted
// at the /api/auth/session/exchange endpoint, not on every request.
// This keeps the hot path fast — no JWKS fetch per request.
func FirebaseMiddleware(fbAuth *FirebaseAuth, legacyAuth *Auth) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Try legacy ocm_token (Bearer header or cookie)
			if legacyAuth != nil {
				var tokenString string
				if header := r.Header.Get("Authorization"); header != "" {
					tokenString = strings.TrimPrefix(header, "Bearer ")
				} else if cookie, err := r.Cookie("ocm_token"); err == nil && cookie.Value != "" {
					tokenString = cookie.Value
				}
				if tokenString != "" {
					claims, err := legacyAuth.ValidateToken(tokenString)
					if err == nil {
						ctx := context.WithValue(r.Context(), userKey, claims)
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
			}

			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/auth/ -run TestFirebaseMiddleware -v`
Expected: Both tests pass.

- [ ] **Step 5: Implement handleSessionExchange**

In `backend/internal/api/server.go`, add the handler:

```go
// handleSessionExchange exchanges a Firebase ID token for an OCM session.
// POST /api/auth/session/exchange
// Body: { "id_token": "<firebase-id-token>" }
// Response: 200 { user } + Set-Cookie: ocm_token
func (s *Server) handleSessionExchange(w http.ResponseWriter, r *http.Request) {
	if s.firebaseAuth == nil {
		writeError(w, http.StatusServiceUnavailable, "Firebase auth not configured")
		return
	}

	var req struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IDToken == "" {
		writeError(w, http.StatusBadRequest, "id_token required")
		return
	}

	// Verify the Firebase ID token
	fbClaims, err := s.firebaseAuth.ValidateToken(req.IDToken)
	if err != nil {
		slog.Warn("firebase.exchange.invalid_token", "error", err)
		writeError(w, http.StatusUnauthorized, "invalid Firebase token")
		return
	}

	issuer := fmt.Sprintf("https://securetoken.google.com/%s", s.firebaseAuth.ProjectID())
	subject := fbClaims.Subject

	// Resolve user: identity → email → auto-create
	user, err := s.store.GetUserByIdentity(r.Context(), issuer, subject)
	if err != nil {
		// Not found by identity — try email
		user, err = s.store.GetUserByEmail(r.Context(), fbClaims.Email)
		if err != nil {
			// Auto-create user
			name := fbClaims.Name
			if name == "" {
				name = fbClaims.Email
			}
			var avatarURL *string
			if fbClaims.Picture != "" {
				avatarURL = &fbClaims.Picture
			}
			user = &User{
				Email:           fbClaims.Email,
				Name:            name,
				AvatarURL:       avatarURL,
				AuthProvider:    "firebase",
				AuthProviderID:  subject,
				IdentityIssuer:  &issuer,
				IdentitySubject: &subject,
				Status:          "active",
			}
			if err := s.store.CreateUser(r.Context(), user); err != nil {
				slog.Error("firebase.user.create.failed", "email", fbClaims.Email, "error", err)
				writeError(w, http.StatusInternalServerError, "failed to create user")
				return
			}
			// Auto-create personal account
			slug := fmt.Sprintf("user-%d", user.ID)
			account := &Account{
				Name:      name + "'s Account",
				Slug:      slug,
				Plan:      "free",
				CreatedBy: user.ID,
			}
			if err := s.store.CreateAccountWithOwner(r.Context(), account, user.ID); err != nil {
				slog.Error("firebase.account.create.failed", "user_id", user.ID, "error", err)
			}
			slog.Info("firebase.user.auto_created", "user_id", user.ID, "email", fbClaims.Email)
		} else {
			// Found by email — link identity
			if err := s.store.UpdateUserProvider(r.Context(), user.ID, "firebase", subject, nil); err != nil {
				slog.Error("firebase.link_identity.failed", "user_id", user.ID, "error", err)
			}
			// TODO: update identity_issuer/identity_subject columns
			slog.Info("firebase.user.linked", "user_id", user.ID, "email", fbClaims.Email)
		}
	}

	// Issue ocm_token
	if s.auth == nil {
		writeError(w, http.StatusInternalServerError, "token signing not configured")
		return
	}
	token, err := s.auth.GenerateToken(user.ID, user.Email)
	if err != nil {
		slog.Error("firebase.exchange.token_failed", "user_id", user.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to generate session")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "ocm_token",
		Value:    token,
		Path:     "/",
		Domain:   ".openclawmachines.com",
		MaxAge:   86400,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{"user": user})
}
```

Note: The handler uses `store.User` directly — add this import alias at the top of server.go if needed, or reference as `store.User`. Looking at the existing code, `handleAuthMe` uses `store.User` directly, so follow the same pattern. Also add a `ProjectID()` accessor to FirebaseAuth:

In `backend/internal/auth/firebase.go`, add:

```go
// ProjectID returns the configured Firebase project ID.
func (f *FirebaseAuth) ProjectID() string {
	return f.projectID
}
```

- [ ] **Step 6: Build to verify compilation**

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./...`
Expected: Build succeeds.

- [ ] **Step 7: Run all auth tests**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/auth/ -v`
Expected: All tests pass.

- [ ] **Step 8: Commit**

```bash
git add backend/internal/auth/auth.go backend/internal/auth/firebase.go backend/internal/api/server.go
git commit -m "feat(auth): add FirebaseMiddleware and session exchange endpoint"
```

---

### Task 6: Update handleAuthMe and handleCliToken for Firebase Mode

**Files:**
- Modify: `backend/internal/api/server.go:1340-1442` (handleAuthMe, handleCliToken)

- [ ] **Step 1: Update handleAuthMe to work with both CF Access and Firebase**

The current `handleAuthMe` only works when `CfSub` is set (CF Access path). In Firebase mode, users authenticate via session exchange and then have a valid `ocm_token` with `UserID` set. Update `handleAuthMe` to handle both paths:

```go
func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	claims := auth.UserFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Path 1: Legacy token with UserID already resolved
	if claims.UserID != 0 {
		user, err := s.store.GetUser(r.Context(), claims.UserID)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "user not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"user": user})
		return
	}

	// Path 2: CF Access — resolve user by cf_sub or email, auto-create if needed
	if claims.CfSub != "" {
		user, err := s.store.GetUserByCfSub(r.Context(), claims.CfSub)
		if err != nil {
			user, err = s.store.GetUserByEmail(r.Context(), claims.Email)
			if err != nil {
				user = &store.User{
					Email:        claims.Email,
					Name:         claims.Email,
					CfSub:        claims.CfSub,
					Status:       "active",
					AuthProvider: "cfaccess",
				}
				if err := s.store.CreateUser(r.Context(), user); err != nil {
					slog.Error("cfaccess.user.create.failed", "email", claims.Email, "error", err)
					writeError(w, http.StatusInternalServerError, "failed to create user")
					return
				}
				slug := fmt.Sprintf("user-%d", user.ID)
				account := &store.Account{
					Name:      user.Name + "'s Account",
					Slug:      slug,
					Plan:      "free",
					CreatedBy: user.ID,
				}
				if err := s.store.CreateAccountWithOwner(r.Context(), account, user.ID); err != nil {
					slog.Error("cfaccess.account.create.failed", "user_id", user.ID, "error", err)
				}
				slog.Info("cfaccess.user.auto_created", "user_id", user.ID, "email", claims.Email)
			} else {
				if err := s.store.UpdateUserCfSub(r.Context(), user.ID, claims.CfSub); err != nil {
					slog.Error("cfaccess.link_cf_sub.failed", "user_id", user.ID, "error", err)
				} else {
					slog.Info("cfaccess.user.linked_cf_sub", "user_id", user.ID, "cf_sub", claims.CfSub)
				}
			}
		}
		// Issue ocm_token cookie
		if s.auth != nil {
			if token, err := s.auth.GenerateToken(user.ID, user.Email); err == nil {
				http.SetCookie(w, &http.Cookie{
					Name:     "ocm_token",
					Value:    token,
					Path:     "/",
					Domain:   ".openclawmachines.com",
					MaxAge:   86400,
					HttpOnly: true,
					Secure:   true,
					SameSite: http.SameSiteNoneMode,
				})
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"user": user})
		return
	}

	writeError(w, http.StatusUnauthorized, "unauthorized")
}
```

- [ ] **Step 2: Update handleCliToken to work with Firebase mode**

In Firebase mode, CLI tokens are issued via the session exchange flow (browser → Firebase login → exchange). The existing `handleCliToken` still works for CF Access mode. For Firebase mode, the CLI will use the session exchange endpoint directly with a Firebase ID token obtained via PKCE browser flow. No changes needed to `handleCliToken` — it already works with `ocm_token` via the legacy path.

However, update the guard to not require `CfSub`:

```go
func (s *Server) handleCliToken(w http.ResponseWriter, r *http.Request) {
	claims := auth.UserFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.auth == nil {
		writeError(w, http.StatusInternalServerError, "token signing not configured")
		return
	}

	// Resolve user from claims (UserID already set by middleware/resolver for both modes)
	var user *store.User
	var err error
	if claims.UserID != 0 {
		user, err = s.store.GetUser(r.Context(), claims.UserID)
	} else if claims.CfSub != "" {
		user, err = s.store.GetUserByCfSub(r.Context(), claims.CfSub)
		if err != nil {
			user, err = s.store.GetUserByEmail(r.Context(), claims.Email)
		}
	}
	if err != nil || user == nil {
		writeError(w, http.StatusNotFound, "user not found — visit the dashboard first to create your account")
		return
	}

	token, err := s.auth.GenerateToken(user.ID, user.Email)
	if err != nil {
		slog.Error("cli_token.generate.failed", "user_id", user.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token": token,
		"email": user.Email,
	})
}
```

- [ ] **Step 3: Build and run tests**

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./... && go test ./internal/auth/ -v`
Expected: Build succeeds, all auth tests pass.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/server.go
git commit -m "feat(auth): update handleAuthMe and handleCliToken for Firebase mode"
```

---

### Task 7: Frontend — Firebase SDK Setup

**Files:**
- Create: `frontend/src/lib/firebase.ts`
- Modify: `frontend/package.json` (add firebase dependency)

- [ ] **Step 1: Install Firebase SDK**

Run: `cd /home/mantiz/OpenClawMachines/frontend && npm install firebase`

- [ ] **Step 2: Create Firebase initialization module**

Create `frontend/src/lib/firebase.ts`:

```typescript
import { initializeApp } from "firebase/app";
import {
  getAuth,
  signInWithPopup,
  GoogleAuthProvider,
  GithubAuthProvider,
  signInWithEmailAndPassword,
  createUserWithEmailAndPassword,
  signOut,
  onAuthStateChanged,
  type User as FirebaseUser,
} from "firebase/auth";

const firebaseConfig = {
  apiKey: import.meta.env.VITE_FIREBASE_API_KEY,
  authDomain: import.meta.env.VITE_FIREBASE_AUTH_DOMAIN,
  projectId: import.meta.env.VITE_FIREBASE_PROJECT_ID,
};

const app = initializeApp(firebaseConfig);
const firebaseAuth = getAuth(app);

const googleProvider = new GoogleAuthProvider();
const githubProvider = new GithubAuthProvider();

export async function signInWithGoogle(): Promise<string> {
  const result = await signInWithPopup(firebaseAuth, googleProvider);
  return result.user.getIdToken();
}

export async function signInWithGithub(): Promise<string> {
  const result = await signInWithPopup(firebaseAuth, githubProvider);
  return result.user.getIdToken();
}

export async function signInWithEmail(email: string, password: string): Promise<string> {
  const result = await signInWithEmailAndPassword(firebaseAuth, email, password);
  return result.user.getIdToken();
}

export async function signUpWithEmail(email: string, password: string): Promise<string> {
  const result = await createUserWithEmailAndPassword(firebaseAuth, email, password);
  return result.user.getIdToken();
}

export async function firebaseSignOut(): Promise<void> {
  await signOut(firebaseAuth);
}

export function onFirebaseAuthChange(callback: (user: FirebaseUser | null) => void): () => void {
  return onAuthStateChanged(firebaseAuth, callback);
}

export async function getFirebaseIdToken(): Promise<string | null> {
  const user = firebaseAuth.currentUser;
  if (!user) return null;
  return user.getIdToken();
}

export { firebaseAuth };
```

- [ ] **Step 3: Commit**

```bash
git add frontend/package.json frontend/package-lock.json frontend/src/lib/firebase.ts
git commit -m "feat(auth): add Firebase SDK initialization and auth helpers"
```

---

### Task 8: Frontend — Login Page with Firebase

**Files:**
- Create: `frontend/src/pages/LoginFirebase.tsx`
- Modify: `frontend/src/App.tsx` (swap Login component)

- [ ] **Step 1: Create the Firebase login page**

Create `frontend/src/pages/LoginFirebase.tsx`:

```tsx
import { useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { signInWithGoogle, signInWithGithub, signInWithEmail, signUpWithEmail } from "../lib/firebase";

const BASE = import.meta.env.VITE_API_URL || "/api";

async function exchangeToken(idToken: string): Promise<{ user: { id: number; email: string } }> {
  const res = await fetch(`${BASE}/auth/session/exchange`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "include",
    body: JSON.stringify({ id_token: idToken }),
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText }));
    throw new Error(body.error || `Exchange failed: ${res.status}`);
  }
  return res.json();
}

export function LoginFirebase() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [mode, setMode] = useState<"login" | "signup">("login");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  const returnTo = (() => {
    const raw = searchParams.get("return") || "/dashboard";
    return raw.startsWith("/") && !raw.startsWith("//") ? raw : "/dashboard";
  })();

  async function handleLogin(getIdToken: () => Promise<string>) {
    setError(null);
    setLoading(true);
    try {
      const idToken = await getIdToken();
      await exchangeToken(idToken);
      // Force a full page reload so AuthProvider picks up the new ocm_token cookie
      window.location.href = returnTo;
    } catch (err: any) {
      setError(err.message || "Login failed");
      setLoading(false);
    }
  }

  return (
    <div className="min-h-screen bg-gray-950 flex items-center justify-center p-4">
      <div className="w-full max-w-sm">
        <div className="text-center mb-8">
          <h1 className="text-2xl font-bold text-white">Sign in to OpenClaw Machines</h1>
          <p className="text-gray-400 text-sm mt-2">Choose a sign-in method to continue</p>
        </div>

        <div className="space-y-3">
          <button
            onClick={() => handleLogin(signInWithGoogle)}
            disabled={loading}
            className="w-full flex items-center justify-center gap-3 px-4 py-3 bg-white text-gray-900 rounded-lg font-medium hover:bg-gray-100 disabled:opacity-50 transition-colors"
          >
            <svg className="w-5 h-5" viewBox="0 0 24 24">
              <path fill="#4285F4" d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 0 1-2.2 3.32v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.1z"/>
              <path fill="#34A853" d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z"/>
              <path fill="#FBBC05" d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z"/>
              <path fill="#EA4335" d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z"/>
            </svg>
            Continue with Google
          </button>

          <button
            onClick={() => handleLogin(signInWithGithub)}
            disabled={loading}
            className="w-full flex items-center justify-center gap-3 px-4 py-3 bg-gray-800 text-white rounded-lg font-medium hover:bg-gray-700 disabled:opacity-50 transition-colors"
          >
            <svg className="w-5 h-5" fill="currentColor" viewBox="0 0 24 24">
              <path d="M12 0c-6.626 0-12 5.373-12 12 0 5.302 3.438 9.8 8.207 11.387.599.111.793-.261.793-.577v-2.234c-3.338.726-4.033-1.416-4.033-1.416-.546-1.387-1.333-1.756-1.333-1.756-1.089-.745.083-.729.083-.729 1.205.084 1.839 1.237 1.839 1.237 1.07 1.834 2.807 1.304 3.492.997.107-.775.418-1.305.762-1.604-2.665-.305-5.467-1.334-5.467-5.931 0-1.311.469-2.381 1.236-3.221-.124-.303-.535-1.524.117-3.176 0 0 1.008-.322 3.301 1.23.957-.266 1.983-.399 3.003-.404 1.02.005 2.047.138 3.006.404 2.291-1.552 3.297-1.23 3.297-1.23.653 1.653.242 2.874.118 3.176.77.84 1.235 1.911 1.235 3.221 0 4.609-2.807 5.624-5.479 5.921.43.372.823 1.102.823 2.222v3.293c0 .319.192.694.801.576 4.765-1.589 8.199-6.086 8.199-11.386 0-6.627-5.373-12-12-12z"/>
            </svg>
            Continue with GitHub
          </button>
        </div>

        <div className="flex items-center gap-3 my-6">
          <div className="flex-1 h-px bg-gray-700" />
          <span className="text-gray-500 text-xs uppercase">or</span>
          <div className="flex-1 h-px bg-gray-700" />
        </div>

        <form
          onSubmit={(e) => {
            e.preventDefault();
            const fn = mode === "signup"
              ? () => signUpWithEmail(email, password)
              : () => signInWithEmail(email, password);
            handleLogin(fn);
          }}
          className="space-y-3"
        >
          <input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="Email address"
            required
            className="w-full px-4 py-3 bg-gray-800 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-brand-500 focus:border-transparent"
          />
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="Password"
            required
            minLength={6}
            className="w-full px-4 py-3 bg-gray-800 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-brand-500 focus:border-transparent"
          />
          <button
            type="submit"
            disabled={loading}
            className="w-full px-4 py-3 bg-brand-600 text-white rounded-lg font-medium hover:bg-brand-700 disabled:opacity-50 transition-colors"
          >
            {loading ? "Signing in..." : mode === "signup" ? "Create account" : "Sign in with email"}
          </button>
        </form>

        <p className="text-center text-gray-500 text-sm mt-4">
          {mode === "login" ? (
            <>
              Don't have an account?{" "}
              <button onClick={() => setMode("signup")} className="text-brand-400 hover:text-brand-300">
                Sign up
              </button>
            </>
          ) : (
            <>
              Already have an account?{" "}
              <button onClick={() => setMode("login")} className="text-brand-400 hover:text-brand-300">
                Sign in
              </button>
            </>
          )}
        </p>

        {error && (
          <div className="mt-4 p-3 bg-red-900/30 border border-red-800 rounded-lg">
            <p className="text-red-400 text-sm">{error}</p>
          </div>
        )}
      </div>
    </div>
  );
}
```

- [ ] **Step 2: Update App.tsx to use LoginFirebase**

In `frontend/src/App.tsx`, replace the Login import and route:

```tsx
// Replace:
import { Login } from "./pages/Login";

// With:
import { LoginFirebase } from "./pages/LoginFirebase";
```

Update the route (remove ProtectedRoute wrapper — login page must be public):

```tsx
// Replace:
<Route path="/login" element={<ProtectedRoute><Login /></ProtectedRoute>} />

// With:
<Route path="/login" element={<LoginFirebase />} />
```

- [ ] **Step 3: Verify frontend builds**

Run: `cd /home/mantiz/OpenClawMachines/frontend && npx tsc --noEmit`
Expected: No type errors.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/pages/LoginFirebase.tsx frontend/src/App.tsx
git commit -m "feat(auth): add Firebase login page with Google, GitHub, and email providers"
```

---

### Task 9: Frontend — Update AuthProvider and API for Firebase

**Files:**
- Modify: `frontend/src/lib/auth.tsx`
- Modify: `frontend/src/lib/api.ts`
- Modify: `frontend/src/App.tsx` (ProtectedRoute)

- [ ] **Step 1: Update AuthProvider to use ocm_token session instead of CF Access**

Replace `frontend/src/lib/auth.tsx`:

```tsx
import { createContext, useContext, useEffect, useState, useCallback, type ReactNode } from "react";
import type { User, Account } from "./types";
import { authMe, listAccounts, listPendingInvitations } from "./api";
import { firebaseSignOut } from "./firebase";

const ACTIVE_ACCOUNT_KEY = "ocm_active_account_id";

interface AuthState {
  user: User | null;
  account: Account | null;
  accounts: Account[];
  loading: boolean;
  accountError: boolean;
  isAdmin: boolean;
  pendingInvitationCount: number;
  setAccount: (account: Account) => void;
  refreshAccounts: () => Promise<void>;
  logout: () => void;
}

const AuthContext = createContext<AuthState>({
  user: null,
  account: null,
  accounts: [],
  loading: true,
  accountError: false,
  isAdmin: false,
  pendingInvitationCount: 0,
  setAccount: () => {},
  refreshAccounts: async () => {},
  logout: () => {},
});

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [account, setAccount] = useState<Account | null>(null);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [loading, setLoading] = useState(true);
  const [accountError, setAccountError] = useState(false);
  const [pendingInvitationCount, setPendingInvitationCount] = useState(0);

  const selectAccount = useCallback((acct: Account) => {
    setAccount(acct);
    localStorage.setItem(ACTIVE_ACCOUNT_KEY, String(acct.id));
  }, []);

  const fetchAccounts = useCallback(async () => {
    try {
      const accts = await listAccounts() ?? [];
      setAccounts(accts);
      if (accts.length > 0) {
        const savedId = localStorage.getItem(ACTIVE_ACCOUNT_KEY);
        const restored = savedId ? accts.find(a => a.id === Number(savedId)) : null;
        setAccount(restored ?? accts[0]);
      }
      setAccountError(false);
    } catch {
      setAccountError(true);
    }
  }, []);

  const fetchPendingInvitations = useCallback(async () => {
    try {
      const invs = await listPendingInvitations();
      setPendingInvitationCount(invs?.length ?? 0);
    } catch {
      // Non-critical
    }
  }, []);

  const refreshAccounts = useCallback(async () => {
    await Promise.all([fetchAccounts(), fetchPendingInvitations()]);
  }, [fetchAccounts, fetchPendingInvitations]);

  useEffect(() => {
    authMe()
      .then(async (res) => {
        setUser(res.user);
        await refreshAccounts();
      })
      .catch(() => {
        // No valid session — user needs to login
      })
      .finally(() => setLoading(false));
  }, [refreshAccounts]);

  const logout = async () => {
    setUser(null);
    setAccount(null);
    setAccounts([]);
    localStorage.removeItem(ACTIVE_ACCOUNT_KEY);

    // Clear ocm_token cookie
    const domains = ["", ".openclawmachines.com"];
    for (const domain of domains) {
      const domainPart = domain ? `; domain=${domain}` : "";
      document.cookie = `ocm_token=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/${domainPart}`;
    }

    // Sign out of Firebase
    try {
      await firebaseSignOut();
    } catch {
      // Firebase sign-out is best-effort
    }

    window.location.href = "/signed-out";
  };

  const isAdmin = user?.email === "mathewma@gmail.com";

  return (
    <AuthContext.Provider value={{
      user, account, accounts, loading, accountError, isAdmin,
      pendingInvitationCount, setAccount: selectAccount, refreshAccounts, logout,
    }}>
      {children}
    </AuthContext.Provider>
  );
}

export const useAuth = () => useContext(AuthContext);
```

- [ ] **Step 2: Update api.ts to remove CF Access JWT forwarding**

In `frontend/src/lib/api.ts`, remove the `getCfAccessJwt` function and the CF Access header injection from `request()`:

```typescript
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...(init?.headers as Record<string, string>),
  };
  // ocm_token cookie is sent automatically via credentials: "include"
  const res = await fetch(`${BASE}${path}`, {
    ...init,
    credentials: "include",
    headers,
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText }));
    throw new ApiError(
      body.error || res.statusText,
      body.code || "unknown",
      res.status,
      res.status === 429 || res.status >= 500,
    );
  }
  if (res.status === 204) return undefined as T;
  return res.json();
}
```

Also remove the `getCfAccessJwt` usage from `downloadMachineBackup` — remove the CF JWT header injection there too.

- [ ] **Step 3: Update ProtectedRoute in App.tsx**

Replace the `ProtectedRoute` component to redirect to `/login` instead of reloading for CF Access:

```tsx
function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const { user, loading } = useAuth();
  if (loading) return <div className="flex items-center justify-center h-screen bg-gray-950" />;
  if (!user) {
    // Redirect to login with return URL
    const returnPath = window.location.pathname + window.location.search;
    window.location.href = `/login?return=${encodeURIComponent(returnPath)}`;
    return <div className="flex items-center justify-center h-screen bg-gray-950" />;
  }
  return <>{children}</>;
}
```

- [ ] **Step 4: Verify frontend builds**

Run: `cd /home/mantiz/OpenClawMachines/frontend && npx tsc --noEmit`
Expected: No type errors.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/auth.tsx frontend/src/lib/api.ts frontend/src/App.tsx
git commit -m "feat(auth): update AuthProvider and API client for Firebase sessions"
```

---

### Task 10: Frontend — Update CliAuth Page

**Files:**
- Modify: `frontend/src/pages/CliAuth.tsx`

- [ ] **Step 1: Update CliAuth to work with Firebase**

The CLI auth flow changes: the CLI opens a browser to `/cli-auth?port=PORT`, the user logs in via Firebase (if not already logged in), and the page calls `/api/auth/cli-token` using the existing `ocm_token` cookie.

```tsx
import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Terminal } from "lucide-react";
import { useAuth } from "../lib/auth";

export function CliAuth() {
  const [searchParams] = useSearchParams();
  const [error, setError] = useState<string | null>(null);
  const { user, loading } = useAuth();

  useEffect(() => {
    if (loading) return;

    // If not logged in, redirect to login with return to cli-auth
    if (!user) {
      const returnPath = `/cli-auth?${searchParams.toString()}`;
      window.location.href = `/login?return=${encodeURIComponent(returnPath)}`;
      return;
    }

    const portStr = searchParams.get("port");
    if (!portStr) {
      setError("Missing port parameter.");
      return;
    }

    const port = parseInt(portStr, 10);
    if (isNaN(port) || port < 1024 || port > 65535) {
      setError("Invalid port number. Must be between 1024 and 65535.");
      return;
    }

    // Request a CLI token — authenticated via ocm_token cookie
    fetch("/api/auth/cli-token", {
      method: "POST",
      credentials: "include",
    })
      .then(async (resp) => {
        if (!resp.ok) {
          const body = await resp.json().catch(() => ({ error: "unknown error" }));
          throw new Error(body.error || `HTTP ${resp.status}`);
        }
        return resp.json();
      })
      .then((data) => {
        window.location.href = `http://localhost:${port}/callback?token=${encodeURIComponent(data.token)}`;
      })
      .catch((err) => {
        setError(`Authentication failed — ${err.message}`);
      });
  }, [searchParams, user, loading]);

  if (error) {
    return (
      <div className="min-h-screen bg-gray-950 flex items-center justify-center px-4">
        <div className="max-w-sm text-center">
          <div className="w-16 h-16 rounded-full bg-red-900/30 flex items-center justify-center mx-auto mb-6">
            <Terminal className="w-7 h-7 text-red-400" />
          </div>
          <h1 className="text-2xl font-bold text-white mb-2">CLI Login Failed</h1>
          <p className="text-gray-400 text-sm">{error}</p>
        </div>
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-gray-950 flex items-center justify-center px-4">
      <div className="max-w-sm text-center">
        <div className="w-16 h-16 rounded-full bg-gray-800 flex items-center justify-center mx-auto mb-6">
          <Terminal className="w-7 h-7 text-orange-400" />
        </div>
        <h1 className="text-2xl font-bold text-white mb-2">
          {loading ? "Loading..." : "Redirecting to CLI..."}
        </h1>
        <p className="text-gray-400 text-sm">
          Completing authentication. You can close this tab once your terminal confirms login.
        </p>
      </div>
    </div>
  );
}
```

Note: Remove `CliAuth` from `ProtectedRoute` in App.tsx since it handles its own auth:

```tsx
// Change:
<Route path="/cli-auth" element={<ProtectedRoute><CliAuth /></ProtectedRoute>} />
// To:
<Route path="/cli-auth" element={<CliAuth />} />
```

- [ ] **Step 2: Verify frontend builds**

Run: `cd /home/mantiz/OpenClawMachines/frontend && npx tsc --noEmit`
Expected: No type errors.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/pages/CliAuth.tsx frontend/src/App.tsx
git commit -m "feat(auth): update CliAuth page for Firebase-based login flow"
```

---

### Task 11: Frontend — Add Logout Endpoint to Backend

**Files:**
- Modify: `backend/internal/api/server.go` (add handleLogout route)

- [ ] **Step 1: Add logout endpoint**

In the public routes section of `server.go` (before the authenticated group), add:

```go
	// Logout (public — clears cookies regardless of session state)
	r.Post("/api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     "ocm_token",
			Value:    "",
			Path:     "/",
			Domain:   ".openclawmachines.com",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteNoneMode,
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
	})
```

- [ ] **Step 2: Update frontend logout to call backend**

In `frontend/src/lib/auth.tsx`, update the logout function to call the backend first:

```tsx
  const logout = async () => {
    setUser(null);
    setAccount(null);
    setAccounts([]);
    localStorage.removeItem(ACTIVE_ACCOUNT_KEY);

    // Tell backend to clear ocm_token cookie
    try {
      await fetch((import.meta.env.VITE_API_URL || "/api") + "/auth/logout", {
        method: "POST",
        credentials: "include",
      });
    } catch {
      // Best-effort — also clear client-side
    }

    // Clear ocm_token cookie client-side as fallback
    const domains = ["", ".openclawmachines.com"];
    for (const domain of domains) {
      const domainPart = domain ? `; domain=${domain}` : "";
      document.cookie = `ocm_token=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/${domainPart}`;
    }

    // Sign out of Firebase
    try {
      await firebaseSignOut();
    } catch {
      // Best-effort
    }

    window.location.href = "/signed-out";
  };
```

- [ ] **Step 3: Add logout API function to api.ts**

In `frontend/src/lib/api.ts`, add:

```typescript
export const authLogout = () =>
  request<{ status: string }>("/auth/logout", { method: "POST" });
```

- [ ] **Step 4: Build both frontend and backend**

Run: `cd /home/mantiz/OpenClawMachines/frontend && npx tsc --noEmit`
Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./...`
Expected: Both succeed.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/server.go frontend/src/lib/auth.tsx frontend/src/lib/api.ts
git commit -m "feat(auth): add logout endpoint and update frontend logout flow"
```

---

### Task 12: Environment Configuration and Deployment

**Files:**
- No code changes — deployment configuration only

- [ ] **Step 1: Add Firebase env vars for frontend**

Create/update `frontend/.env.production` (or wherever VITE_ env vars are set for production):

```bash
VITE_FIREBASE_API_KEY=<from-firebase-console>
VITE_FIREBASE_AUTH_DOMAIN=<project-id>.firebaseapp.com
VITE_FIREBASE_PROJECT_ID=clarateach
```

For local dev, create `frontend/.env.local`:

```bash
VITE_FIREBASE_API_KEY=<from-firebase-console>
VITE_FIREBASE_AUTH_DOMAIN=<project-id>.firebaseapp.com
VITE_FIREBASE_PROJECT_ID=clarateach
```

- [ ] **Step 2: Update backend environment**

Set these environment variables in Cloud Run (GCP Secret Manager or deploy script):

```bash
AUTH_MODE=firebase
FIREBASE_PROJECT_ID=clarateach
```

The `JWT_SECRET` must remain set (it signs `ocm_token`).

- [ ] **Step 3: Run the database migration**

```bash
psql "$DATABASE_URL" -f backend/migrations/045_firebase_auth.sql
```

- [ ] **Step 4: Deploy and verify**

```bash
make deploy-backend
make deploy-frontend
```

Test the login flow:
1. Open `https://app.openclawmachines.com/login`
2. Click "Continue with Google"
3. Firebase popup opens, complete sign-in
4. Redirected to `/dashboard` with valid session
5. Verify `ocm_token` cookie is set
6. Machine subdomain routing still works (Worker validates `ocm_token` — unchanged)

- [ ] **Step 5: Commit env config (if applicable)**

```bash
git add frontend/.env.production
git commit -m "chore: configure Firebase auth environment variables"
```

---

### Task 13: Remove Old CF Access Login Code (Cleanup)

**Files:**
- Delete: `frontend/src/pages/Login.tsx` (old CF Access login page)
- Modify: `frontend/src/App.tsx` (remove old Login import)
- Modify: `frontend/src/pages/SignedOut.tsx` (update to not reference CF Access)

- [ ] **Step 1: Remove old Login.tsx**

Delete `frontend/src/pages/Login.tsx` — it's replaced by `LoginFirebase.tsx`.

- [ ] **Step 2: Clean up SignedOut page**

Read `frontend/src/pages/SignedOut.tsx` and remove any CF Access-specific references. Update the "Sign in again" button to navigate to `/login` instead of triggering CF Access.

- [ ] **Step 3: Remove CF Access cookie clearing from remaining code**

Search for any remaining `CF_Authorization` or `CF_AppSession` references in the frontend and remove them. The frontend should no longer reference CF Access cookies.

- [ ] **Step 4: Remove VITE_CF_TEAM_DOMAIN references**

Search for `VITE_CF_TEAM_DOMAIN` in frontend code and remove — no longer needed for customer flows.

- [ ] **Step 5: Verify frontend builds and tests pass**

Run: `cd /home/mantiz/OpenClawMachines/frontend && npx tsc --noEmit && npm test`
Expected: Clean build, all tests pass.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor(auth): remove old CF Access login code from frontend"
```

---

## Dependency Graph

```
Task 1 (DB migration) ─────────┐
                                ├─→ Task 2 (Store layer) ─→ Task 5 (Session exchange) ─→ Task 6 (Auth handlers)
Task 3 (Firebase verifier) ─────┤                                                        │
                                ├─→ Task 4 (Config wiring) ──────────────────────────────┘
Task 7 (Firebase SDK) ─────────┤
                                ├─→ Task 8 (Login page) ─→ Task 9 (AuthProvider) ─→ Task 10 (CliAuth)
                                │                                                      │
                                └─→ Task 11 (Logout) ────────────────────────────────┘
                                                                                       │
                                                                    Task 12 (Deploy) ──┘
                                                                           │
                                                                    Task 13 (Cleanup)
```

Tasks 1, 3, 7 can be done in parallel. Tasks 2 and 4 depend on 1 and 3 respectively. Tasks 5-6 depend on 2+4. Tasks 8-11 depend on 7. Task 12 depends on all. Task 13 is cleanup after verification.
