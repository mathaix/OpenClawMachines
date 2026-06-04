package tunnel

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

type mockMachineChecker struct {
	active map[string]bool
}

func (m *mockMachineChecker) IsMachineActive(_ context.Context, slug string) (bool, error) {
	return m.active[slug], nil
}

func TestReap_DeletesOrphans(t *testing.T) {
	var mu sync.Mutex
	var deletedTunnels []string
	var deletedDNS []string

	mux := http.NewServeMux()

	// List tunnels
	mux.HandleFunc("GET /client/v4/accounts/test-acct/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		resp := cfResponse{
			Success: true,
		}
		tunnels := []TunnelInfo{
			{ID: "tun-active", Name: "ocm-vm-active-slug", CreatedAt: time.Now()},
			{ID: "tun-orphan", Name: "ocm-vm-orphan-slug", CreatedAt: time.Now()},
			{ID: "tun-other", Name: "ocm-other-tunnel", CreatedAt: time.Now()},
		}
		resp.Result, _ = json.Marshal(tunnels)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// DNS lookup (for DeleteDNSRoute)
	mux.HandleFunc("GET /client/v4/zones/test-zone/dns_records", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hostname := r.URL.Query().Get("name")
		deletedDNS = append(deletedDNS, hostname)
		mu.Unlock()
		resp := cfResponse{Success: true}
		resp.Result, _ = json.Marshal([]struct{ ID string }{{ID: "dns-1"}})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Delete DNS record
	mux.HandleFunc("DELETE /client/v4/zones/test-zone/dns_records/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cfResponse{Success: true})
	})

	// Delete tunnel
	mux.HandleFunc("DELETE /client/v4/accounts/test-acct/cfd_tunnel/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		// Extract tunnel ID from path
		deletedTunnels = append(deletedTunnels, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cfResponse{Success: true})
	})

	mgr := New("test-token", "test-acct", "test-zone")
	mgr.client = &http.Client{Transport: &mockTransport{handler: mux}}

	checker := &mockMachineChecker{
		active: map[string]bool{
			"active-slug": true,
			"orphan-slug": false,
		},
	}

	reapWithDomain(context.Background(), mgr, checker, "example.com")

	// Verify: orphan tunnel was deleted, active was not
	mu.Lock()
	defer mu.Unlock()

	if len(deletedTunnels) != 1 {
		t.Fatalf("expected 1 tunnel deleted, got %d: %v", len(deletedTunnels), deletedTunnels)
	}
	if len(deletedDNS) != 2 {
		t.Fatalf("expected 2 DNS lookups for orphan HTTP/SSH hostnames, got %d: %v", len(deletedDNS), deletedDNS)
	}
	wantDNS := map[string]bool{
		"m-orphan-slug.example.com":   false,
		"ssh-orphan-slug.example.com": false,
	}
	for _, name := range deletedDNS {
		if _, ok := wantDNS[name]; !ok {
			t.Fatalf("unexpected DNS lookup %q; got all lookups %v", name, deletedDNS)
		}
		wantDNS[name] = true
	}
	for name, seen := range wantDNS {
		if !seen {
			t.Fatalf("expected DNS lookup for %q, got %v", name, deletedDNS)
		}
	}
}

func TestReap_KeepsActiveTunnels(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /client/v4/accounts/test-acct/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		resp := cfResponse{Success: true}
		tunnels := []TunnelInfo{
			{ID: "tun-1", Name: "ocm-vm-running", CreatedAt: time.Now()},
		}
		resp.Result, _ = json.Marshal(tunnels)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mgr := New("test-token", "test-acct", "test-zone")
	mgr.client = &http.Client{Transport: &mockTransport{handler: mux}}

	checker := &mockMachineChecker{
		active: map[string]bool{"running": true},
	}

	// Should not panic or attempt any deletes
	reapWithDomain(context.Background(), mgr, checker, "example.com")
}
