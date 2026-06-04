package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

func TestRequireRole(t *testing.T) {
	tests := []struct {
		name       string
		roles      []string
		member     *store.AccountMember
		wantStatus int
	}{
		{
			name:       "owner allowed when roles include owner and admin",
			roles:      []string{"owner", "admin"},
			member:     &store.AccountMember{AccountID: 1, UserID: 1, Role: "owner"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "admin allowed when roles include owner and admin",
			roles:      []string{"owner", "admin"},
			member:     &store.AccountMember{AccountID: 1, UserID: 2, Role: "admin"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "member denied when roles are owner and admin",
			roles:      []string{"owner", "admin"},
			member:     &store.AccountMember{AccountID: 1, UserID: 3, Role: "member"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "nil member returns 403",
			roles:      []string{"owner", "admin"},
			member:     nil,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "member allowed when roles include member",
			roles:      []string{"owner", "admin", "member"},
			member:     &store.AccountMember{AccountID: 1, UserID: 3, Role: "member"},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Inner handler that just returns 200
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			middleware := requireRole(tt.roles...)
			handler := middleware(inner)

			req := httptest.NewRequest("GET", "/test", nil)
			if tt.member != nil {
				ctx := context.WithValue(req.Context(), accountMemberKey, tt.member)
				req = req.WithContext(ctx)
			}

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d. Body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
		})
	}
}
