package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/cli/internal/api"
)

// logsHandler returns a handler that serves canned machines for slug resolution
// and a logs endpoint that returns the given log lines. It records the request
// path/query for assertions.
func logsHandler(t *testing.T, machines []api.Machine, logOutput string, logStatus int) (http.HandlerFunc, *http.Request) {
	t.Helper()
	var captured http.Request
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// GET /api/accounts/1/machines — list (for slug resolution)
		if path == "/api/accounts/1/machines" && r.Method == "GET" {
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(machines)
			return
		}

		// GET /api/accounts/1/machines/{id}/logs
		for _, m := range machines {
			if path == fmt.Sprintf("/api/accounts/1/machines/%s/logs", m.ID) && r.Method == "GET" {
				// Capture the request so tests can inspect query params
				captured = *r
				w.WriteHeader(logStatus)
				fmt.Fprint(w, logOutput)
				return
			}
		}

		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}, &captured
}

func TestMachinesLogsCommand(t *testing.T) {
	hostname := "m-test.openclawmachines.com"
	machines := []api.Machine{
		{
			ID:             "uuid-1",
			AccountID:      1,
			Name:           "My Machine",
			Slug:           "my-machine",
			Status:         "running",
			VCPUs:          2,
			MemoryMB:       2048,
			TunnelHostname: &hostname,
			CreatedAt:      time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC),
		},
		{
			ID:        "uuid-2",
			AccountID: 1,
			Name:      "Stopped Machine",
			Slug:      "stopped-machine",
			Status:    "stopped",
			VCPUs:     2,
			MemoryMB:  2048,
			CreatedAt: time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC),
		},
	}

	t.Run("successful log fetch", func(t *testing.T) {
		logLines := "line1: hello world\nline2: testing logs\nline3: done\n"
		handler, _ := logsHandler(t, machines, logLines, 200)
		server := httptest.NewServer(handler)
		defer server.Close()

		teardown := setupTestConfig(t, server.URL)
		defer teardown()

		output, err := executeCommand("machines", "logs", "my-machine")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !strings.Contains(output, "line1: hello world") {
			t.Errorf("output missing log line 1\nGot: %s", output)
		}
		if !strings.Contains(output, "line2: testing logs") {
			t.Errorf("output missing log line 2\nGot: %s", output)
		}
		if !strings.Contains(output, "line3: done") {
			t.Errorf("output missing log line 3\nGot: %s", output)
		}
	})

	t.Run("with --lines flag sends query parameter", func(t *testing.T) {
		handler, captured := logsHandler(t, machines, "some logs\n", 200)
		server := httptest.NewServer(handler)
		defer server.Close()

		teardown := setupTestConfig(t, server.URL)
		defer teardown()

		_, err := executeCommand("machines", "logs", "my-machine", "--lines", "50")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify the lines query parameter was sent
		gotLines := captured.URL.Query().Get("lines")
		if gotLines != "50" {
			t.Errorf("expected lines=50 query param, got %q (full query: %s)", gotLines, captured.URL.RawQuery)
		}
	})

	t.Run("with --follow flag sends query parameter", func(t *testing.T) {
		handler, captured := logsHandler(t, machines, "streamed log\n", 200)
		server := httptest.NewServer(handler)
		defer server.Close()

		teardown := setupTestConfig(t, server.URL)
		defer teardown()

		output, err := executeCommand("machines", "logs", "my-machine", "--follow")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify the follow query parameter was sent
		gotFollow := captured.URL.Query().Get("follow")
		if gotFollow != "true" {
			t.Errorf("expected follow=true query param, got %q (full query: %s)", gotFollow, captured.URL.RawQuery)
		}

		if !strings.Contains(output, "streamed log") {
			t.Errorf("output missing streamed log content\nGot: %s", output)
		}
	})

	t.Run("with --follow and --lines sends both query parameters", func(t *testing.T) {
		handler, captured := logsHandler(t, machines, "log output\n", 200)
		server := httptest.NewServer(handler)
		defer server.Close()

		teardown := setupTestConfig(t, server.URL)
		defer teardown()

		_, err := executeCommand("machines", "logs", "my-machine", "--follow", "--lines", "25")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		gotLines := captured.URL.Query().Get("lines")
		gotFollow := captured.URL.Query().Get("follow")
		if gotLines != "25" {
			t.Errorf("expected lines=25, got %q", gotLines)
		}
		if gotFollow != "true" {
			t.Errorf("expected follow=true, got %q", gotFollow)
		}
	})

	t.Run("machine not found", func(t *testing.T) {
		handler, _ := logsHandler(t, machines, "", 200)
		server := httptest.NewServer(handler)
		defer server.Close()

		teardown := setupTestConfig(t, server.URL)
		defer teardown()

		_, err := executeCommand("machines", "logs", "nonexistent-machine")
		if err == nil {
			t.Fatal("expected error for nonexistent machine, got nil")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found' in error, got: %v", err)
		}
	})

	t.Run("machine not running", func(t *testing.T) {
		handler, _ := logsHandler(t, machines, "", 200)
		server := httptest.NewServer(handler)
		defer server.Close()

		teardown := setupTestConfig(t, server.URL)
		defer teardown()

		_, err := executeCommand("machines", "logs", "stopped-machine")
		if err == nil {
			t.Fatal("expected error for stopped machine, got nil")
		}
		if !strings.Contains(err.Error(), "not running") {
			t.Errorf("expected 'not running' in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "stopped") {
			t.Errorf("expected 'stopped' status in error, got: %v", err)
		}
	})

	t.Run("API error response", func(t *testing.T) {
		handler, _ := logsHandler(t, machines, `{"error":"internal server error"}`, 500)
		server := httptest.NewServer(handler)
		defer server.Close()

		teardown := setupTestConfig(t, server.URL)
		defer teardown()

		_, err := executeCommand("machines", "logs", "my-machine")
		if err == nil {
			t.Fatal("expected error for API error response, got nil")
		}
		if !strings.Contains(err.Error(), "500") {
			t.Errorf("expected status code 500 in error, got: %v", err)
		}
	})
}

func TestMachinesLogsCommand_MissingArg(t *testing.T) {
	teardown := setupTestConfig(t, "http://localhost:0")
	defer teardown()

	_, err := executeCommand("machines", "logs")
	if err == nil {
		t.Fatal("expected error for missing slug argument, got nil")
	}
}
