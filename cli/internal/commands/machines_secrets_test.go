package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/cli/internal/api"
)

// secretsHandler returns a handler that serves canned machines for slug resolution
// and handles secret CRUD routes.
func secretsHandler(t *testing.T, machines []api.Machine, secrets []api.SecretEntry) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// GET /api/accounts/1/machines — list (for slug resolution)
		if path == "/api/accounts/1/machines" && r.Method == "GET" {
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(machines)
			return
		}

		// Secret routes per machine
		for _, m := range machines {
			prefix := fmt.Sprintf("/api/accounts/1/machines/%s/secrets", m.ID)

			// GET /api/accounts/1/machines/{id}/secrets — list secrets
			if path == prefix && r.Method == "GET" {
				w.WriteHeader(200)
				json.NewEncoder(w).Encode(secrets)
				return
			}

			// PUT /api/accounts/1/machines/{id}/secrets/{key} — set secret
			if strings.HasPrefix(path, prefix+"/") && r.Method == "PUT" {
				body, _ := io.ReadAll(r.Body)
				var req map[string]string
				json.Unmarshal(body, &req)
				if req["value"] == "" {
					w.WriteHeader(400)
					json.NewEncoder(w).Encode(map[string]string{"error": "value required"})
					return
				}
				w.WriteHeader(200)
				json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				return
			}

			// DELETE /api/accounts/1/machines/{id}/secrets/{key} — delete secret
			if strings.HasPrefix(path, prefix+"/") && r.Method == "DELETE" {
				w.WriteHeader(204)
				return
			}
		}

		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}
}

func newTestSecrets() []api.SecretEntry {
	return []api.SecretEntry{
		{
			ID:        1,
			MachineID: "uuid-1",
			Key:       "API_KEY",
			CreatedAt: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 2, 20, 14, 30, 0, 0, time.UTC),
		},
		{
			ID:        2,
			MachineID: "uuid-1",
			Key:       "DB_PASSWORD",
			CreatedAt: time.Date(2026, 1, 20, 8, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 1, 20, 8, 0, 0, 0, time.UTC),
		},
	}
}

func TestMachineSecretsListCommand(t *testing.T) {
	tests := []struct {
		name      string
		secrets   []api.SecretEntry
		wantParts []string
	}{
		{
			name:      "list secrets with results",
			secrets:   newTestSecrets(),
			wantParts: []string{"KEY", "CREATED", "UPDATED", "API_KEY", "DB_PASSWORD"},
		},
		{
			name:      "list secrets empty",
			secrets:   []api.SecretEntry{},
			wantParts: []string{"No secrets configured on this machine."},
		},
	}

	machines := newTestMachines()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(secretsHandler(t, machines, tt.secrets))
			defer server.Close()

			teardown := setupTestConfig(t, server.URL)
			defer teardown()

			output, err := executeCommand("machines", "secrets", "list", "--machine", "my-machine")
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

func TestMachineSecretsSetCommand(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "set secret without stdin errors in non-interactive mode",
			args:    []string{"machines", "secrets", "set", "--machine", "my-machine", "MY_SECRET"},
			wantErr: true,
		},
		{
			name:    "set secret missing key",
			args:    []string{"machines", "secrets", "set", "--machine", "my-machine"},
			wantErr: true,
		},
		{
			name:    "set secret rejects extra positional args",
			args:    []string{"machines", "secrets", "set", "--machine", "my-machine", "MY_SECRET", "s3cret-value"},
			wantErr: true,
		},
	}

	machines := newTestMachines()
	secrets := newTestSecrets()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(secretsHandler(t, machines, secrets))
			defer server.Close()

			teardown := setupTestConfig(t, server.URL)
			defer teardown()

			_, err := executeCommand(tt.args...)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestMachineSecretsDeleteCommand(t *testing.T) {
	machines := newTestMachines()
	secrets := newTestSecrets()

	server := httptest.NewServer(secretsHandler(t, machines, secrets))
	defer server.Close()

	teardown := setupTestConfig(t, server.URL)
	defer teardown()

	output, err := executeCommand("machines", "secrets", "delete", "--machine", "my-machine", "API_KEY")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, `Secret "API_KEY" deleted from machine My Machine.`) {
		t.Errorf("unexpected output: %s", output)
	}
}
