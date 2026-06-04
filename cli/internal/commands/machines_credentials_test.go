package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/cli/internal/api"
)

// machineCredsHandler returns a handler that serves machine list (for resolveMachine)
// and machine credential routes.
func machineCredsHandler(t *testing.T, machines []api.Machine, creds []api.Credential) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// GET /api/accounts/1/machines — list (needed by resolveMachine)
		if path == "/api/accounts/1/machines" && r.Method == "GET" {
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(machines)
			return
		}

		// Match credential routes for each machine
		for _, m := range machines {
			base := "/api/accounts/1/machines/" + m.ID + "/credentials"

			// GET /api/accounts/1/machines/{id}/credentials — list credentials
			if path == base && r.Method == "GET" {
				w.WriteHeader(200)
				json.NewEncoder(w).Encode(creds)
				return
			}
		}

		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}
}

func newTestCredentials() []api.Credential {
	lastFour := "ab12"
	validated := time.Date(2026, 2, 20, 12, 0, 0, 0, time.UTC)
	return []api.Credential{
		{
			ID:             1,
			AccountID:      1,
			MachineID:      "machine-1",
			Provider:       "anthropic",
			CredentialType: "api_key",
			Label:          "My Anthropic Key",
			LastValidated:  &validated,
			LastFour:       &lastFour,
			Status:         "active",
			CreatedAt:      time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
			UpdatedAt:      time.Date(2026, 2, 20, 12, 0, 0, 0, time.UTC),
		},
		{
			ID:             2,
			AccountID:      1,
			MachineID:      "machine-1",
			Provider:       "openai",
			CredentialType: "api_key",
			Label:          "OpenAI Production",
			Status:         "active",
			CreatedAt:      time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
			UpdatedAt:      time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		},
	}
}

func TestMachineCredsListCommand(t *testing.T) {
	tests := []struct {
		name      string
		creds     []api.Credential
		args      []string
		wantParts []string
	}{
		{
			name:  "list credentials with results",
			creds: newTestCredentials(),
			args:  []string{"machines", "credentials", "list", "--machine", "my-machine"},
			wantParts: []string{
				"PROVIDER", "TYPE", "LABEL", "LAST FOUR",
				"anthropic", "api_key", "My Anthropic Key", "ab12",
				"openai", "OpenAI Production",
			},
		},
		{
			name:      "list credentials empty",
			creds:     []api.Credential{},
			args:      []string{"machines", "credentials", "list", "--machine", "my-machine"},
			wantParts: []string{"No credentials on machine"},
		},
	}

	machines := newTestMachines()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(machineCredsHandler(t, machines, tt.creds))
			defer server.Close()

			teardown := setupTestConfig(t, server.URL)
			defer teardown()

			output, err := executeCommand(tt.args...)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			for _, part := range tt.wantParts {
				if !strings.Contains(output, part) {
					t.Errorf("output missing %q\nGot: %s", part, output)
				}
			}
		})
	}
}
