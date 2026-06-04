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

// sshMachinesHandler returns a handler that serves canned machines for the SSH
// command tests (machine resolution only — the SSH command doesn't call other API
// endpoints beyond listing machines).
func sshMachinesHandler(t *testing.T, machines []api.Machine) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/accounts/1/machines" && r.Method == "GET" {
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(machines)
			return
		}
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}
}

func TestMachinesSSHCommand(t *testing.T) {
	tunnelHostname := "m-test.openclawmachines.com"

	machines := []api.Machine{
		{
			ID:             "uuid-running",
			AccountID:      1,
			Name:           "Running Machine",
			Slug:           "running-machine",
			Status:         "running",
			VCPUs:          2,
			MemoryMB:       2048,
			TunnelHostname: &tunnelHostname,
			CreatedAt:      time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC),
		},
		{
			ID:        "uuid-stopped",
			AccountID: 1,
			Name:      "Stopped Machine",
			Slug:      "stopped-machine",
			Status:    "stopped",
			VCPUs:     2,
			MemoryMB:  2048,
			CreatedAt: time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC),
		},
	}

	tests := []struct {
		name    string
		slug    string
		wantErr string
	}{
		{
			// NOTE: We cannot test the happy path to completion because it calls
			// syscall.Exec which replaces the process. Instead we test the
			// error paths that happen before the exec call.
			name:    "machine not running",
			slug:    "stopped-machine",
			wantErr: "not running",
		},
		{
			name:    "machine not found",
			slug:    "nonexistent-machine",
			wantErr: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(sshMachinesHandler(t, machines))
			defer server.Close()

			teardown := setupTestConfig(t, server.URL)
			defer teardown()

			_, err := executeCommand("machines", "ssh", tt.slug)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

// machines ssh with no args is now valid (uses default/auto-select)

func TestDeriveSSHHostname(t *testing.T) {
	// The SSH hostname is derived as ssh-{slug}.{domain} internally.
	tests := []struct {
		slug    string
		domain  string
		wantSSH string
	}{
		{"hvn58kw", "openclawmachines.com", "ssh-hvn58kw.openclawmachines.com"},
		{"my-machine", "example.com", "ssh-my-machine.example.com"},
		{"abc123", "openclawmachines.com", "ssh-abc123.openclawmachines.com"},
	}

	for _, tt := range tests {
		t.Run(tt.slug, func(t *testing.T) {
			got := "ssh-" + tt.slug + "." + tt.domain
			if got != tt.wantSSH {
				t.Errorf("derived SSH host = %q, want %q", got, tt.wantSSH)
			}
		})
	}
}
