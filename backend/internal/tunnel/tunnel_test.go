package tunnel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockTransport intercepts all HTTP requests and routes them to a handler,
// allowing us to mock Cloudflare API calls without changing any URLs.
type mockTransport struct {
	handler http.Handler
}

func (t *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rr := httptest.NewRecorder()
	t.handler.ServeHTTP(rr, req)
	return rr.Result(), nil
}

// newTestManager creates a Manager with a mock transport backed by the given handler.
func newTestManager(handler http.Handler) *Manager {
	m := New("test-token", "test-account", "test-zone")
	m.client = &http.Client{Transport: &mockTransport{handler: handler}}
	return m
}

func TestCreateVMTunnel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/client/v4/accounts/test-account/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Errorf("unexpected auth header: %s", auth)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["name"] != "ocm-vm-my-machine" {
			t.Errorf("expected tunnel name ocm-vm-my-machine, got %v", body["name"])
		}
		if body["config_src"] != "cloudflare" {
			t.Errorf("expected config_src cloudflare, got %v", body["config_src"])
		}

		result, _ := json.Marshal(map[string]string{
			"id":    "tunnel-abc-123",
			"token": "secret-token-xyz",
		})
		resp := cfResponse{Success: true, Result: result}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	m := newTestManager(mux)
	tunnelID, token, err := m.CreateVMTunnel(context.Background(), "my-machine")
	if err != nil {
		t.Fatalf("CreateVMTunnel: %v", err)
	}
	if tunnelID != "tunnel-abc-123" {
		t.Errorf("expected tunnel ID tunnel-abc-123, got %s", tunnelID)
	}
	if token != "secret-token-xyz" {
		t.Errorf("expected token secret-token-xyz, got %s", token)
	}
}

func TestConfigureVMTunnel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/client/v4/accounts/test-account/cfd_tunnel/tun-123/configurations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Errorf("unexpected auth header: %s", auth)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		config, ok := body["config"].(map[string]interface{})
		if !ok {
			t.Fatal("missing config key in body")
		}
		ingress, ok := config["ingress"].([]interface{})
		if !ok {
			t.Fatal("missing ingress key in config")
		}
		if len(ingress) != 3 {
			t.Fatalf("expected 3 ingress rules, got %d", len(ingress))
		}

		rule0 := ingress[0].(map[string]interface{})
		if rule0["hostname"] != "m-machine.example.com" {
			t.Errorf("expected hostname m-machine.example.com, got %v", rule0["hostname"])
		}
		if rule0["service"] != "http://localhost:8080" {
			t.Errorf("expected service http://localhost:8080, got %v", rule0["service"])
		}

		rule1 := ingress[1].(map[string]interface{})
		if rule1["hostname"] != "ssh-machine.example.com" {
			t.Errorf("expected hostname ssh-machine.example.com, got %v", rule1["hostname"])
		}
		if rule1["service"] != "ssh://localhost:22" {
			t.Errorf("expected service ssh://localhost:22, got %v", rule1["service"])
		}

		rule2 := ingress[2].(map[string]interface{})
		if rule2["service"] != "http_status:404" {
			t.Errorf("expected catch-all http_status:404, got %v", rule2["service"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(cfResponse{Success: true})
	})

	m := newTestManager(mux)
	err := m.ConfigureVMTunnel(context.Background(), "tun-123", "m-machine.example.com", "ssh-machine.example.com")
	if err != nil {
		t.Fatalf("ConfigureVMTunnel: %v", err)
	}
}

func TestDeleteTunnelAndDNS(t *testing.T) {
	var dnsLookupCalled, dnsDeleteCalled, tunnelDeleteCalled bool

	mux := http.NewServeMux()

	// DNS record lookup
	mux.HandleFunc("/client/v4/zones/test-zone/dns_records", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			dnsLookupCalled = true
			if q := r.URL.Query().Get("name"); q != "vm.example.com" {
				t.Errorf("expected DNS lookup for vm.example.com, got %s", q)
			}
			result, _ := json.Marshal([]map[string]string{
				{"id": "dns-rec-1"},
			})
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(cfResponse{Success: true, Result: result})
			return
		}
		t.Errorf("unexpected method %s on dns_records", r.Method)
	})

	// DNS record delete
	mux.HandleFunc("/client/v4/zones/test-zone/dns_records/dns-rec-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		dnsDeleteCalled = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{}`)
	})

	// Tunnel delete
	mux.HandleFunc("/client/v4/accounts/test-account/cfd_tunnel/tun-456", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		tunnelDeleteCalled = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{}`)
	})

	m := newTestManager(mux)
	err := m.DeleteTunnelAndDNS(context.Background(), "tun-456", "vm.example.com")
	if err != nil {
		t.Fatalf("DeleteTunnelAndDNS: %v", err)
	}
	if !dnsLookupCalled {
		t.Error("DNS lookup was not called")
	}
	if !dnsDeleteCalled {
		t.Error("DNS record delete was not called")
	}
	if !tunnelDeleteCalled {
		t.Error("tunnel delete was not called")
	}
}

func TestListTunnels(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/client/v4/accounts/test-account/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if q := r.URL.Query().Get("is_deleted"); q != "false" {
			t.Errorf("expected is_deleted=false, got %s", q)
		}

		result, _ := json.Marshal([]map[string]interface{}{
			{"id": "tun-1", "name": "ocm-vm-alpha", "created_at": "2026-01-15T10:00:00Z"},
			{"id": "tun-2", "name": "ocm-vm-beta", "created_at": "2026-01-16T12:00:00Z"},
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cfResponse{Success: true, Result: result})
	})

	m := newTestManager(mux)
	tunnels, err := m.ListTunnels(context.Background())
	if err != nil {
		t.Fatalf("ListTunnels: %v", err)
	}
	if len(tunnels) != 2 {
		t.Fatalf("expected 2 tunnels, got %d", len(tunnels))
	}
	if tunnels[0].ID != "tun-1" || tunnels[0].Name != "ocm-vm-alpha" {
		t.Errorf("unexpected first tunnel: %+v", tunnels[0])
	}
	if tunnels[1].ID != "tun-2" || tunnels[1].Name != "ocm-vm-beta" {
		t.Errorf("unexpected second tunnel: %+v", tunnels[1])
	}
}

func TestCreateVMTunnel_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/client/v4/accounts/test-account/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"result":  nil,
			"errors": []map[string]string{
				{"message": "authentication token is invalid"},
			},
		})
	})

	m := newTestManager(mux)
	_, _, err := m.CreateVMTunnel(context.Background(), "fail-machine")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); got != "create VM tunnel: authentication token is invalid (status 403)" {
		t.Errorf("unexpected error message: %s", got)
	}
}
