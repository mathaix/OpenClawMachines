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

// seedTestSizes populates the cached sizes for tests so sizeLabel doesn't need a server.
func seedTestSizes(t *testing.T) {
	t.Helper()
	cachedSizes = []api.MachineSize{
		{ID: "basic", Label: "Basic", VCPUs: 1, MemoryMB: 3072, DiskGB: 10},
		{ID: "standard", Label: "Standard", VCPUs: 2, MemoryMB: 6144, DiskGB: 20},
		{ID: "pro", Label: "Pro", VCPUs: 4, MemoryMB: 12288, DiskGB: 50},
	}
	t.Cleanup(func() { cachedSizes = nil })
}

// machinesHandler returns a handler that serves a canned list of machines.
// It also handles start/stop/delete actions routed by path suffix.
func machinesHandler(t *testing.T, machines []api.Machine) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// POST /api/accounts/1/machines — create
		if path == "/api/accounts/1/machines" && r.Method == "POST" {
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(api.Machine{
				ID:        "new-uuid",
				AccountID: 1,
				Name:      "new-machine",
				Slug:      "new-machine",
				Status:    "provisioning",
				VCPUs:     2,
				MemoryMB:  2048,
				CreatedAt: time.Date(2026, 2, 24, 0, 0, 0, 0, time.UTC),
			})
			return
		}

		// GET /api/accounts/1/machines — list
		if path == "/api/accounts/1/machines" && r.Method == "GET" {
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(machines)
			return
		}

		// GET /api/accounts/1/machines/{id} — get details
		for _, m := range machines {
			if path == "/api/accounts/1/machines/"+m.ID && r.Method == "GET" {
				w.WriteHeader(200)
				json.NewEncoder(w).Encode(m)
				return
			}
		}

		// POST /api/accounts/1/machines/{id}/start
		for _, m := range machines {
			if path == "/api/accounts/1/machines/"+m.ID+"/start" && r.Method == "POST" {
				w.WriteHeader(200)
				return
			}
		}

		// POST /api/accounts/1/machines/{id}/stop
		for _, m := range machines {
			if path == "/api/accounts/1/machines/"+m.ID+"/stop" && r.Method == "POST" {
				w.WriteHeader(200)
				return
			}
		}

		// DELETE /api/accounts/1/machines/{id}
		for _, m := range machines {
			if path == "/api/accounts/1/machines/"+m.ID && r.Method == "DELETE" {
				w.WriteHeader(204)
				return
			}
		}

		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}
}

func newTestMachines() []api.Machine {
	hostname := "my-machine.openclawmachines.com"
	return []api.Machine{
		{
			ID:             "uuid-1",
			AccountID:      1,
			Name:           "My Machine",
			Slug:           "my-machine",
			Status:         "running",
			VCPUs:          2,
			MemoryMB:       6144,
			TunnelHostname: &hostname,
			CreatedAt:      time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC),
		},
		{
			ID:        "uuid-2",
			AccountID: 1,
			Name:      "Stopped Machine",
			Slug:      "stopped-machine",
			Status:    "stopped",
			VCPUs:     4,
			MemoryMB:  12288,
			CreatedAt: time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC),
		},
	}
}

func TestMachinesListCommand(t *testing.T) {
	seedTestSizes(t)
	tests := []struct {
		name      string
		machines  []api.Machine
		wantParts []string
	}{
		{
			name:      "list machines with results",
			machines:  newTestMachines(),
			wantParts: []string{"My Machine", "running", "Standard", "Stopped Machine", "stopped", "Pro"},
		},
		{
			name:      "list machines empty",
			machines:  []api.Machine{},
			wantParts: []string{"No machines found"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(machinesHandler(t, tt.machines))
			defer server.Close()

			teardown := setupTestConfig(t, server.URL)
			defer teardown()

			output, err := executeCommand("machines", "list")
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

func TestMachinesCreateCommand(t *testing.T) {
	seedTestSizes(t)
	tests := []struct {
		name      string
		args      []string
		wantParts []string
		wantErr   bool
	}{
		{
			name:      "create machine with standard size",
			args:      []string{"machines", "create", "--name", "new-machine", "--size", "standard"},
			wantParts: []string{"Machine created successfully", "new-machine"},
		},
		{
			name:      "create machine with pro size",
			args:      []string{"machines", "create", "--name", "new-machine", "--size", "pro"},
			wantParts: []string{"Machine created successfully"},
		},
		{
			name:    "create machine missing name",
			args:    []string{"machines", "create", "--name", "", "--size", "standard"},
			wantErr: true,
		},
		{
			name:    "create machine invalid size",
			args:    []string{"machines", "create", "--name", "test", "--size", "micro"},
			wantErr: true,
		},
	}

	machines := newTestMachines()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(machinesHandler(t, machines))
			defer server.Close()

			teardown := setupTestConfig(t, server.URL)
			defer teardown()

			output, err := executeCommand(tt.args...)

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

func TestMachinesStartCommand(t *testing.T) {
	tests := []struct {
		name      string
		slug      string
		wantParts []string
		wantErr   bool
	}{
		{
			name:      "start existing machine",
			slug:      "my-machine",
			wantParts: []string{"Starting machine: My Machine"},
		},
		{
			name:    "start nonexistent machine",
			slug:    "no-such-machine",
			wantErr: true,
		},
	}

	machines := newTestMachines()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(machinesHandler(t, machines))
			defer server.Close()

			teardown := setupTestConfig(t, server.URL)
			defer teardown()

			output, err := executeCommand("machines", "start", tt.slug)

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

func TestMachinesStopCommand(t *testing.T) {
	tests := []struct {
		name      string
		slug      string
		wantParts []string
		wantErr   bool
	}{
		{
			name:      "stop existing machine",
			slug:      "my-machine",
			wantParts: []string{"Stopping machine: My Machine"},
		},
		{
			name:    "stop nonexistent machine",
			slug:    "no-such-machine",
			wantErr: true,
		},
	}

	machines := newTestMachines()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(machinesHandler(t, machines))
			defer server.Close()

			teardown := setupTestConfig(t, server.URL)
			defer teardown()

			output, err := executeCommand("machines", "stop", tt.slug)

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

func TestMachinesDeleteCommand(t *testing.T) {
	machines := newTestMachines()

	server := httptest.NewServer(machinesHandler(t, machines))
	defer server.Close()

	teardown := setupTestConfig(t, server.URL)
	defer teardown()

	output, err := executeCommand("machines", "delete", "my-machine")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "Deleted machine: My Machine") {
		t.Errorf("unexpected output: %s", output)
	}
}

func TestMachinesGetCommand(t *testing.T) {
	seedTestSizes(t)
	machines := newTestMachines()

	server := httptest.NewServer(machinesHandler(t, machines))
	defer server.Close()

	teardown := setupTestConfig(t, server.URL)
	defer teardown()

	output, err := executeCommand("machines", "get", "my-machine")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantParts := []string{"My Machine", "running", "Standard"}
	for _, part := range wantParts {
		if !strings.Contains(output, part) {
			t.Errorf("output missing %q\nGot: %s", part, output)
		}
	}
}

// machines start with no args is now valid (uses default/auto-select)

func TestValidateSize(t *testing.T) {
	seedTestSizes(t)

	tests := []struct {
		size    string
		wantErr bool
	}{
		{"basic", false},
		{"standard", false},
		{"pro", false},
		{"micro", true},
	}

	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			err := validateSize(tt.size)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSizeLabel(t *testing.T) {
	// Seed cached sizes so tests don't hit the network.
	cachedSizes = []api.MachineSize{
		{ID: "basic", Label: "Basic", VCPUs: 1, MemoryMB: 3072, DiskGB: 10},
		{ID: "standard", Label: "Standard", VCPUs: 2, MemoryMB: 6144, DiskGB: 20},
		{ID: "pro", Label: "Pro", VCPUs: 4, MemoryMB: 12288, DiskGB: 50},
	}
	defer func() { cachedSizes = nil }()

	tests := []struct {
		vcpus    int
		memoryMB int
		want     string
	}{
		{1, 3072, "Basic"},
		{2, 6144, "Standard"},
		{4, 12288, "Pro"},
		{16, 16384, "16vcpu/16384MB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := sizeLabel(tt.vcpus, tt.memoryMB)
			if got != tt.want {
				t.Errorf("sizeLabel(%d, %d) = %q, want %q", tt.vcpus, tt.memoryMB, got, tt.want)
			}
		})
	}
}
