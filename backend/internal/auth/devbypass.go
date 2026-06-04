package auth

import (
	"context"
	"log/slog"
	"net/http"
)

// DevBypassMiddleware returns a middleware that synthesizes fake CF Access claims
// from the given email. It should only be used in development.
func DevBypassMiddleware(email string) func(http.Handler) http.Handler {
	if email == "" {
		email = "dev@localhost"
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slog.Warn("auth.dev_bypass", "email", email, "method", r.Method, "path", r.URL.Path)
			claims := &Claims{
				Email: email,
				CfSub: "dev-bypass",
			}
			ctx := context.WithValue(r.Context(), userKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
