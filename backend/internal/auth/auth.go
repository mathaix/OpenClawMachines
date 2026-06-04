package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	UserID    int    `json:"user_id"`
	Email     string `json:"email"`
	AccountID int    `json:"account_id,omitempty"`
	CfSub     string `json:"cf_sub,omitempty"`
	jwt.RegisteredClaims
}

// DualModeMiddleware returns a middleware that tries CF Access JWT first
// (via Cf-Access-Jwt-Assertion header), then falls back to legacy ocm_token cookie.
func DualModeMiddleware(cfAuth *CfAccessAuth, legacyAuth *Auth) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Try CF JWT first. Check multiple sources because Cloudflare strips
			// Cf-Access-Jwt-Assertion from outbound Worker fetch() requests.
			cfJWT := r.Header.Get("Cf-Access-Jwt-Assertion")
			jwtSource := "header"
			if cfJWT == "" {
				// Worker forwards CF JWT via X-Cf-Access-Jwt (not stripped by CF)
				cfJWT = r.Header.Get("X-Cf-Access-Jwt")
				jwtSource = "x-header"
			}
			if cfJWT == "" {
				if cookie, err := r.Cookie("CF_Authorization"); err == nil {
					cfJWT = cookie.Value
					jwtSource = "cookie"
				}
			}
			if r.URL.Path == "/api/auth/me" {
				slog.Info("auth.debug", "path", r.URL.Path, "jwt_source", jwtSource, "has_jwt", cfJWT != "", "jwt_len", len(cfJWT))
			}
			if cfJWT != "" {
				cfClaims, err := cfAuth.ValidateCfJWT(cfJWT)
				if err != nil {
					slog.Warn("auth.cf_jwt_invalid", "path", r.URL.Path, "source", jwtSource, "error", err, "jwt_len", len(cfJWT))
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				claims := &Claims{
					Email: cfClaims.Email,
					CfSub: cfClaims.Subject,
				}
				ctx := context.WithValue(r.Context(), userKey, claims)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Fall back to legacy token
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

type Auth struct {
	jwtSecret []byte
}

func New(secret string) *Auth {
	return &Auth{jwtSecret: []byte(secret)}
}

func (a *Auth) GenerateToken(userID int, email string, accountIDs ...int) (string, error) {
	claims := &Claims{
		UserID: userID,
		Email:  email,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	if len(accountIDs) > 0 {
		claims.AccountID = accountIDs[0]
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(a.jwtSecret)
}

func (a *Auth) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return a.jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}

type contextKey string

const userKey contextKey = "user"

// UserFromContext extracts the authenticated user claims from the request context.
func UserFromContext(ctx context.Context) *Claims {
	claims, _ := ctx.Value(userKey).(*Claims)
	return claims
}

// WithUser stores claims in the context (used by user resolver middleware).
func WithUser(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, userKey, claims)
}

// FirebaseMiddleware returns a middleware that validates requests using the
// legacy ocm_token (Bearer header or cookie). Firebase ID tokens are only
// accepted at the /api/auth/session/exchange endpoint (not on every request),
// keeping the hot path fast with no JWKS fetch per request.
func FirebaseMiddleware(fbAuth *FirebaseAuth, legacyAuth *Auth) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
