package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mathaix/openclawmachines/cli/internal/api"
)

// machineListResponse returns a JSON response for GET /api/accounts/{id}/machines
func machineListResponse() []api.Machine {
	return []api.Machine{
		{ID: "machine-1", AccountID: 1, Name: "My Machine", Slug: "my-machine", Status: "stopped"},
	}
}

func TestProvidersListCommand(t *testing.T) {
	tests := []struct {
		name       string
		creds      []api.Credential
		statusCode int
		wantParts  []string
		wantErr    bool
	}{
		{
			name: "list credentials successfully",
			creds: []api.Credential{
				{
					ID:       1,
					Provider: "anthropic",
					Label:    "Production key",
					LastFour: strPtr("ab12"),
					Status:   "active",
				},
				{
					ID:       2,
					Provider: "openai",
					Label:    "Dev key",
					Status:   "active",
				},
			},
			statusCode: 200,
			wantParts:  []string{"anthropic", "Production key", "****ab12", "openai", "Dev key"},
		},
		{
			name:       "empty credentials list",
			creds:      []api.Credential{},
			statusCode: 200,
			wantParts:  []string{"No provider credentials on machine"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/accounts/1/machines" && r.Method == "GET":
					w.WriteHeader(200)
					json.NewEncoder(w).Encode(machineListResponse())
				case r.URL.Path == "/api/accounts/1/machines/machine-1/credentials" && r.Method == "GET":
					w.WriteHeader(tt.statusCode)
					json.NewEncoder(w).Encode(tt.creds)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()

			teardown := setupTestConfigWithMachine(t, server.URL, "machine-1", "My Machine")
			defer teardown()

			output, err := executeCommand("providers", "list")

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
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

func TestProvidersRemoveCommand(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		statusCode int
		wantOutput string
		wantErr    bool
	}{
		{
			name:       "remove credential successfully",
			provider:   "anthropic",
			statusCode: 204,
			wantOutput: "Credential for anthropic removed from My Machine",
		},
		{
			name:       "remove nonexistent credential",
			provider:   "openai",
			statusCode: 404,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/accounts/1/machines" && r.Method == "GET":
					w.WriteHeader(200)
					json.NewEncoder(w).Encode(machineListResponse())
				case r.URL.Path == "/api/accounts/1/machines/machine-1/credentials/"+tt.provider && r.Method == "DELETE":
					w.WriteHeader(tt.statusCode)
					if tt.statusCode != 204 {
						json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
					}
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()

			teardown := setupTestConfigWithMachine(t, server.URL, "machine-1", "My Machine")
			defer teardown()

			output, err := executeCommand("providers", "remove", tt.provider)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(output, tt.wantOutput) {
				t.Errorf("output missing %q\nGot: %s", tt.wantOutput, output)
			}
		})
	}
}

func TestProvidersAddCommand_InvalidProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach the server for invalid provider")
	}))
	defer server.Close()

	teardown := setupTestConfig(t, server.URL)
	defer teardown()

	_, err := executeCommand("providers", "add", "invalid-provider")
	if err == nil {
		t.Fatal("expected error for invalid provider, got nil")
	}
	if !strings.Contains(err.Error(), "invalid provider") {
		t.Errorf("expected 'invalid provider' in error, got: %v", err)
	}
}

func TestProvidersAddCommand_InvalidType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach the server for invalid type")
	}))
	defer server.Close()

	teardown := setupTestConfig(t, server.URL)
	defer teardown()

	_, err := executeCommand("providers", "add", "anthropic", "--type", "invalid_type")
	if err == nil {
		t.Fatal("expected error for invalid type, got nil")
	}
	if !strings.Contains(err.Error(), "invalid credential type") {
		t.Errorf("expected 'invalid credential type' in error, got: %v", err)
	}
}

func TestProvidersAddCommand_MissingArg(t *testing.T) {
	teardown := setupTestConfig(t, "http://localhost:0")
	defer teardown()

	_, err := executeCommand("providers", "add")
	if err == nil {
		t.Fatal("expected error for missing argument, got nil")
	}
}

func TestInferCredentialType(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{"anthropic", "api_key"},
		{"openai", "api_key"},
		{"google", "api_key"},
		{"telegram", "token"},
		{"discord", "token"},
		{"whatsapp", "token"},
		{"some-new-provider", "api_key"},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := inferCredentialType(tt.provider)
			if got != tt.want {
				t.Errorf("inferCredentialType(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}

func TestProvidersListCommand_ShowsTypeColumn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/accounts/1/machines" && r.Method == "GET":
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(machineListResponse())
		case r.URL.Path == "/api/accounts/1/machines/machine-1/credentials" && r.Method == "GET":
			w.WriteHeader(200)
			json.NewEncoder(w).Encode([]api.Credential{
				{
					ID:             1,
					Provider:       "anthropic",
					CredentialType: "api_key",
					Label:          "Prod key",
					LastFour:       strPtr("ab12"),
					Status:         "active",
				},
				{
					ID:             2,
					Provider:       "telegram",
					CredentialType: "token",
					Label:          "Bot token",
					Status:         "active",
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()

	teardown := setupTestConfigWithMachine(t, server.URL, "machine-1", "My Machine")
	defer teardown()

	output, err := executeCommand("providers", "list")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(output, "TYPE") {
		t.Errorf("output missing TYPE column header\nGot: %s", output)
	}
	if !strings.Contains(output, "api_key") {
		t.Errorf("output missing 'api_key' type\nGot: %s", output)
	}
	if !strings.Contains(output, "token") {
		t.Errorf("output missing 'token' type\nGot: %s", output)
	}
}

func strPtr(s string) *string {
	return &s
}
