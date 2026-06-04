package sampler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/vmmetrics/cgroup"
)

func float32Ptr(f float32) *float32 { return &f }

func TestHTTPPusher_HappyPath(t *testing.T) {
	var capturedAuth string
	var capturedPath string
	var capturedBody wireBody

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &capturedBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := &HTTPPusher{BackendURL: srv.URL, AgentToken: "secret-host-token"}
	now := time.Date(2026, 4, 29, 18, 33, 1, 0, time.UTC)
	err := p.Push(context.Background(), 7, []Point{
		{VMID: "9f3c", Kind: cgroup.KindMachine, Timestamp: now, CPUPct: nil, MemBytes: 100},
		{VMID: "9f3c", Kind: cgroup.KindMachine, Timestamp: now.Add(time.Second), CPUPct: float32Ptr(12.5), MemBytes: 110},
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	if capturedPath != "/api/agent/vm-metrics" {
		t.Errorf("path = %q, want /api/agent/vm-metrics", capturedPath)
	}
	if capturedAuth != "Bearer secret-host-token" {
		t.Errorf("auth = %q, want bearer token", capturedAuth)
	}
	if capturedBody.HostID != 7 {
		t.Errorf("host_id = %d, want 7", capturedBody.HostID)
	}
	if len(capturedBody.Points) != 2 {
		t.Fatalf("points len = %d, want 2", len(capturedBody.Points))
	}
	if capturedBody.Points[0].CPUPct != nil {
		t.Errorf("first cpu_pct = %v, want nil", *capturedBody.Points[0].CPUPct)
	}
	if capturedBody.Points[1].CPUPct == nil || *capturedBody.Points[1].CPUPct < 12 || *capturedBody.Points[1].CPUPct > 13 {
		t.Errorf("second cpu_pct = %v, want ~12.5", capturedBody.Points[1].CPUPct)
	}
	if capturedBody.Points[0].Kind != "machine" {
		t.Errorf("kind = %q, want machine", capturedBody.Points[0].Kind)
	}
}

func TestHTTPPusher_ErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid token"))
	}))
	defer srv.Close()

	p := &HTTPPusher{BackendURL: srv.URL, AgentToken: "bad"}
	err := p.Push(context.Background(), 1, []Point{{VMID: "x", Kind: cgroup.KindMachine, Timestamp: time.Now()}})
	if err == nil {
		t.Fatal("Push returned nil; want error on 401")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid token") {
		t.Errorf("error %q should mention status + body", err.Error())
	}
}

func TestHTTPPusher_EmptyBatchNoOp(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := &HTTPPusher{BackendURL: srv.URL, AgentToken: "x"}
	if err := p.Push(context.Background(), 1, nil); err != nil {
		t.Fatalf("Push(nil): %v", err)
	}
	if called {
		t.Errorf("server was called for empty batch — should short-circuit")
	}
}

func TestHTTPPusher_TrimsTrailingSlashOnURL(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	// Trailing slash on the BackendURL should not produce a double slash.
	p := &HTTPPusher{BackendURL: srv.URL + "/", AgentToken: "x"}
	if err := p.Push(context.Background(), 1, []Point{{VMID: "x", Kind: cgroup.KindMachine, Timestamp: time.Now()}}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if capturedPath != "/api/agent/vm-metrics" {
		t.Errorf("path = %q, want /api/agent/vm-metrics (no double slash)", capturedPath)
	}
}
