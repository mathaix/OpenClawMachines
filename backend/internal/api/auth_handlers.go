package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// userResolverMiddleware resolves the UserID for CF Access / dev bypass authenticated requests.
// CF Access middleware sets Email + CfSub but not UserID (it doesn't have DB access).
// This middleware looks up the user by cf_sub or email and updates the context claims.
func (s *Server) userResolverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := auth.UserFromContext(r.Context())
		if claims == nil || claims.UserID != 0 {
			// No claims or already resolved — pass through
			next.ServeHTTP(w, r)
			return
		}

		// Try to resolve user from cf_sub, then email
		var user *store.User
		var err error
		if claims.CfSub != "" {
			user, err = s.store.GetUserByCfSub(r.Context(), claims.CfSub)
		}
		if user == nil && claims.Email != "" {
			user, err = s.store.GetUserByEmail(r.Context(), claims.Email)
		}

		if err == nil && user != nil {
			claims.UserID = user.ID
			ctx := auth.WithUser(r.Context(), claims)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// User not found — allow request to continue (handleAuthMe will auto-create)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	claims := auth.UserFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Firebase/legacy token path: UserID already resolved by middleware
	if claims.UserID != 0 {
		user, err := s.store.GetUser(r.Context(), claims.UserID)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "user not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"user": user})
		return
	}

	// CF Access path: resolve user by cf_sub or email, auto-create if needed
	if claims.CfSub != "" {
		user, err := s.store.GetUserByCfSub(r.Context(), claims.CfSub)
		if err != nil {
			// Not found by cf_sub — try email
			user, err = s.store.GetUserByEmail(r.Context(), claims.Email)
			if err != nil {
				// Auto-create user
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
				// Auto-create personal account
				slug := fmt.Sprintf("user-%d", user.ID)
				account := &store.Account{
					Name:      user.Name + "'s Account",
					Slug:      slug,
					CreatedBy: user.ID,
				}
				if err := s.store.CreateAccountWithOwner(r.Context(), account, user.ID); err != nil {
					slog.Error("cfaccess.account.create.failed", "user_id", user.ID, "error", err)
				} else {
					s.activity.Log(r.Context(), events.LogParams{
						Category:  "account",
						Action:    "account.created",
						Status:    "success",
						ActorType: "system",
						AccountID: &account.ID,
						Summary:   fmt.Sprintf("Auto-created account for %s", user.Email),
						Detail:    map[string]any{"trigger": "first_login", "provider": "cfaccess"},
					})
				}
				slog.Info("cfaccess.user.auto_created", "user_id", user.ID, "email", claims.Email)
			} else {
				// Found by email — link cf_sub
				if err := s.store.UpdateUserCfSub(r.Context(), user.ID, claims.CfSub); err != nil {
					slog.Error("cfaccess.link_cf_sub.failed", "user_id", user.ID, "error", err)
				} else {
					slog.Info("cfaccess.user.linked_cf_sub", "user_id", user.ID, "cf_sub", claims.CfSub)
				}
			}
		}
		// Issue ocm_token cookie so the data plane Worker can authenticate
		// subdomain requests (Worker validates HS256 JWTs, not CF Access RS256).
		if s.auth != nil {
			if token, err := s.auth.GenerateToken(user.ID, user.Email); err == nil {
				http.SetCookie(w, s.sessionCookie(r, token, 86400))
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"user": user})
		return
	}

	writeError(w, http.StatusUnauthorized, "unauthorized")
}

// handleCliToken issues a first-party OCM JWT for CLI authentication.
// The request is authenticated via middleware (CF Access or Firebase ocm_token).
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

	// Resolve user from claims
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

	fbClaims, err := s.firebaseAuth.ValidateToken(req.IDToken)
	if err != nil {
		slog.Warn("firebase.exchange.invalid_token", "error", err)
		writeError(w, http.StatusUnauthorized, "invalid Firebase token")
		return
	}
	if !fbClaims.EmailVerified {
		slog.Warn("firebase.exchange.email_not_verified", "email", fbClaims.Email)
		writeError(w, http.StatusForbidden, "email not verified — please verify your email first")
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
			user = &store.User{
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
			account := &store.Account{
				Name:      name + "'s Account",
				Slug:      slug,
				CreatedBy: user.ID,
			}
			if err := s.store.CreateAccountWithOwner(r.Context(), account, user.ID); err != nil {
				slog.Error("firebase.account.create.failed", "user_id", user.ID, "error", err)
			} else {
				s.activity.Log(r.Context(), events.LogParams{
					Category:  "account",
					Action:    "account.created",
					Status:    "success",
					ActorType: "system",
					AccountID: &account.ID,
					Summary:   fmt.Sprintf("Auto-created account for %s", user.Email),
					Detail:    map[string]any{"trigger": "first_login", "provider": "firebase"},
				})
			}
			slog.Info("firebase.user.auto_created", "user_id", user.ID, "email", fbClaims.Email)
		} else {
			// Found by email — link identity
			if err := s.store.UpdateUserProvider(r.Context(), user.ID, "firebase", subject, nil); err != nil {
				slog.Error("firebase.link_identity.failed", "user_id", user.ID, "error", err)
			}
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

	http.SetCookie(w, s.sessionCookie(r, token, 86400))

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "auth",
		Action:    "auth.login",
		Status:    "success",
		ActorType: "user",
		ActorID:   &user.ID,
		Summary:   fmt.Sprintf("Logged in via Firebase (%s)", user.Email),
		Detail:    map[string]any{"provider": "firebase", "email": user.Email},
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{"user": user})
}

// handleLogout clears the OCM session cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, s.sessionCookie(r, "", -1))
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (s *Server) sessionCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	secure := requestIsSecure(r)
	sameSite := http.SameSiteLaxMode
	if secure {
		sameSite = http.SameSiteNoneMode
	}
	return &http.Cookie{
		Name:     "ocm_token",
		Value:    value,
		Path:     "/",
		Domain:   s.cookieDomain(),
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: sameSite,
	}
}
