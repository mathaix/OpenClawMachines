package agentapi

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// bearerAuth returns middleware that validates Bearer token authentication.
func bearerAuth(agentToken string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Defense-in-depth: reject all requests if no token is configured.
			// The agent already fatals on empty token at startup, but guard
			// against ConstantTimeCompare([], []) returning 1.
			if agentToken == "" {
				http.Error(w, "server misconfigured", http.StatusInternalServerError)
				return
			}

			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, "missing authorization header", http.StatusUnauthorized)
				return
			}

			token := strings.TrimPrefix(authHeader, "Bearer ")
			if token == authHeader {
				// No "Bearer " prefix found
				http.Error(w, "invalid authorization format", http.StatusUnauthorized)
				return
			}

			if subtle.ConstantTimeCompare([]byte(token), []byte(agentToken)) != 1 {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
