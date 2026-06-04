package agentapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCidrAllowlist_WarnWhenEmpty(t *testing.T) {
	tests := []struct {
		name        string
		allowedCIDR string
		wantReason  string
	}{
		{
			name:        "empty string",
			allowedCIDR: "",
			wantReason:  "empty",
		},
		{
			name:        "whitespace only",
			allowedCIDR: "   ",
			wantReason:  "empty",
		},
		{
			name:        "all invalid CIDRs",
			allowedCIDR: "not-a-cidr, also-bad",
			wantReason:  "no_valid_cidrs",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Capture slog output. The warn-only middleware logs at WARN level
			// (the name warnAndAllow hints at this), so the capture sink has to
			// be at LevelWarn or lower.
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
			old := slog.Default()
			slog.SetDefault(logger)
			t.Cleanup(func() { slog.SetDefault(old) })

			handler := cidrAllowlist(tc.allowedCIDR)

			// The inner handler returns 200 OK — proves request was allowed through.
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			wrapped := handler(inner)

			req := httptest.NewRequest(http.MethodGet, "/test-path", nil)
			req.RemoteAddr = "10.0.0.1:12345"
			rec := httptest.NewRecorder()

			wrapped.ServeHTTP(rec, req)

			// Request must be allowed through.
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 OK, got %d", rec.Code)
			}

			// Verify warning was logged.
			logged := buf.String()
			if !strings.Contains(logged, "control_api.cidr_allowlist.not_configured") {
				t.Fatalf("expected warning log, got: %s", logged)
			}
			if !strings.Contains(logged, tc.wantReason) {
				t.Fatalf("expected reason=%q in log, got: %s", tc.wantReason, logged)
			}
			if !strings.Contains(logged, "10.0.0.1:12345") {
				t.Fatalf("expected remote_addr in log, got: %s", logged)
			}
			if !strings.Contains(logged, "/test-path") {
				t.Fatalf("expected path in log, got: %s", logged)
			}
		})
	}
}

func TestCidrAllowlist_NoWarnWhenConfigured(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	old := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(old) })

	handler := cidrAllowlist("10.0.0.0/8")

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := handler(inner)

	req := httptest.NewRequest(http.MethodGet, "/test-path", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	rec := httptest.NewRecorder()

	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	logged := buf.String()
	if strings.Contains(logged, "control_api.cidr_allowlist.not_configured") {
		t.Fatalf("should not warn when CIDR is configured, got: %s", logged)
	}
}
