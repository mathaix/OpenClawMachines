package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/config"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// mockEnrollmentStore implements the store methods used by enrollment handlers.
type mockEnrollmentStore struct {
	store.Store

	createdToken          *store.EnrollmentToken
	createTokenErr        error
	tokens                []store.EnrollmentToken
	listTokensErr         error
	getTokenResult        *store.EnrollmentToken
	getTokenErr           error
	markedTokenUsed       string
	markedTokenHost       int
	markUsedErr           error
	createdHost           *store.Host
	createHostErr         error
	updatedHost           *store.Host
	updateHostErr         error
	updatedMetadataHostID int
	updatedMetadata       json.RawMessage
	updateMetadataErr     error

	// For deregister tests
	getHostResult              *store.Host
	getHostErr                 error
	updatedStatusHostID        int
	updatedStatus              string
	updateStatusErr            error
	markMachinesErrorCalled    bool
	markMachinesErrorHostID    int
	markMachinesErrorErr       error
	markMachinesErrorReturnIDs []string
}

func (m *mockEnrollmentStore) CreateEnrollmentToken(_ context.Context, token *store.EnrollmentToken) error {
	if m.createTokenErr != nil {
		return m.createTokenErr
	}
	token.Token = "test-token-abc123"
	m.createdToken = token
	return nil
}

func (m *mockEnrollmentStore) ListEnrollmentTokens(_ context.Context) ([]store.EnrollmentToken, error) {
	return m.tokens, m.listTokensErr
}

func (m *mockEnrollmentStore) GetEnrollmentToken(_ context.Context, token string) (*store.EnrollmentToken, error) {
	if m.getTokenErr != nil {
		return nil, m.getTokenErr
	}
	if m.getTokenResult != nil {
		return m.getTokenResult, nil
	}
	return nil, fmt.Errorf("token not found")
}

func (m *mockEnrollmentStore) MarkEnrollmentTokenUsed(_ context.Context, token string, hostID int) error {
	m.markedTokenUsed = token
	m.markedTokenHost = hostID
	return m.markUsedErr
}

func (m *mockEnrollmentStore) CreateRegisteredHost(_ context.Context, host *store.Host) error {
	if m.createHostErr != nil {
		return m.createHostErr
	}
	host.ID = 42
	m.createdHost = host
	return nil
}

func (m *mockEnrollmentStore) UpdateHostDetails(_ context.Context, h *store.Host) error {
	m.updatedHost = h
	return m.updateHostErr
}

func (m *mockEnrollmentStore) GetHost(_ context.Context, id int) (*store.Host, error) {
	if m.getHostErr != nil {
		return nil, m.getHostErr
	}
	if m.getHostResult != nil {
		return m.getHostResult, nil
	}
	return nil, fmt.Errorf("host not found")
}

func (m *mockEnrollmentStore) UpdateHostStatus(_ context.Context, id int, status string) error {
	m.updatedStatusHostID = id
	m.updatedStatus = status
	return m.updateStatusErr
}

func (m *mockEnrollmentStore) MarkMachinesOnHostError(_ context.Context, hostID int, message string) ([]string, error) {
	m.markMachinesErrorCalled = true
	m.markMachinesErrorHostID = hostID
	return m.markMachinesErrorReturnIDs, m.markMachinesErrorErr
}

func (m *mockEnrollmentStore) UpdateHostProviderMetadata(_ context.Context, hostID int, metadata json.RawMessage) error {
	m.updatedMetadataHostID = hostID
	m.updatedMetadata = metadata
	return m.updateMetadataErr
}

// mockTunnelManager implements TunnelCreator for testing.
type mockTunnelManager struct {
	createCalled    bool
	configureCalled bool
	dnsCalled       bool
	deleteCalled    bool
	shouldFail      string // "create", "configure", "dns", or ""
	tunnelID        string
	tunnelToken     string
	createdName     string
}

func (m *mockTunnelManager) CreateTunnel(_ context.Context, name string) (string, string, error) {
	m.createCalled = true
	m.createdName = name
	if m.shouldFail == "create" {
		return "", "", fmt.Errorf("tunnel creation failed")
	}
	return m.tunnelID, m.tunnelToken, nil
}

func (m *mockTunnelManager) ConfigureTunnel(_ context.Context, tunnelID, hostname string) error {
	m.configureCalled = true
	if m.shouldFail == "configure" {
		return fmt.Errorf("tunnel configure failed")
	}
	return nil
}

func (m *mockTunnelManager) CreateDNSRoute(_ context.Context, tunnelID, hostname string) error {
	m.dnsCalled = true
	if m.shouldFail == "dns" {
		return fmt.Errorf("dns creation failed")
	}
	return nil
}

func (m *mockTunnelManager) DeleteTunnel(_ context.Context, tunnelID string) error {
	m.deleteCalled = true
	return nil
}

func (m *mockTunnelManager) DeleteTunnelAndDNS(_ context.Context, tunnelID string, hostnames ...string) error {
	m.deleteCalled = true
	if m.shouldFail == "deleteDNS" {
		return fmt.Errorf("delete tunnel and DNS failed")
	}
	return nil
}

func newTestEnrollmentServer(ms *mockEnrollmentStore) *Server {
	return &Server{
		store:                    ms,
		backendURL:               "https://api.example.test",
		vcpuOversubRatio:         2, // default 2x oversubscription
		browserRootfsGCSManifest: config.ExperimentalKernelBrowserManifestURI,
		browserRootfsVersion:     config.StableKernelBrowserRootfsVersion,
	}
}

func TestHandleCreateEnrollmentTokenRequiresTrustedBackendURL(t *testing.T) {
	ms := &mockEnrollmentStore{}
	srv := newTestEnrollmentServer(ms)
	srv.backendURL = ""

	req := httptest.NewRequest(http.MethodPost, "/api/admin/enrollment-tokens", strings.NewReader(`{}`))
	req.Host = "attacker.example"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleCreateEnrollmentToken(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d: %s", w.Code, w.Body.String())
	}
	if ms.createdToken != nil {
		t.Fatal("expected no token to be created without a trusted backend URL")
	}
}

// ---- handleCreateEnrollmentToken tests ----

func TestHandleCreateEnrollmentToken(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		storeErr      error
		wantStatus    int
		wantToken     bool
		checkProvider string
	}{
		{
			name:          "happy path with defaults",
			body:          `{}`,
			wantStatus:    http.StatusCreated,
			wantToken:     true,
			checkProvider: "ovhcloud",
		},
		{
			name:          "explicit provider and class",
			body:          `{"provider":"hetzner","provider_class":"dedicated","expires_in_hours":48}`,
			wantStatus:    http.StatusCreated,
			wantToken:     true,
			checkProvider: "hetzner",
		},
		{
			name:       "invalid JSON body",
			body:       `{invalid`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "store error",
			body:       `{"provider":"ovhcloud"}`,
			storeErr:   fmt.Errorf("db connection failed"),
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &mockEnrollmentStore{createTokenErr: tt.storeErr}
			srv := newTestEnrollmentServer(ms)

			req := httptest.NewRequest(http.MethodPost, "/api/admin/enrollment-tokens", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			srv.handleCreateEnrollmentToken(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d: %s", tt.wantStatus, w.Code, w.Body.String())
			}

			if tt.wantToken {
				var resp map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
					t.Fatalf("failed to parse response: %v", err)
				}
				if resp["token"] == nil || resp["token"] == "" {
					t.Fatal("expected token in response")
				}
				if resp["install_command"] == nil {
					t.Fatal("expected install_command in response")
				}
			}

			if tt.checkProvider != "" && ms.createdToken != nil {
				if ms.createdToken.Provider != tt.checkProvider {
					t.Fatalf("expected provider %q, got %q", tt.checkProvider, ms.createdToken.Provider)
				}
			}
		})
	}
}

// ---- handleListEnrollmentTokens tests ----

func TestHandleListEnrollmentTokens(t *testing.T) {
	tests := []struct {
		name       string
		tokens     []store.EnrollmentToken
		storeErr   error
		wantStatus int
		wantCount  int
	}{
		{
			name:       "empty list",
			tokens:     []store.EnrollmentToken{},
			wantStatus: http.StatusOK,
			wantCount:  0,
		},
		{
			name: "returns tokens",
			tokens: []store.EnrollmentToken{
				{ID: "1", Token: "tok-1", Provider: "ovhcloud", ExpiresAt: time.Now().Add(24 * time.Hour)},
				{ID: "2", Token: "tok-2", Provider: "hetzner", ExpiresAt: time.Now().Add(48 * time.Hour)},
			},
			wantStatus: http.StatusOK,
			wantCount:  2,
		},
		{
			name:       "store error",
			storeErr:   fmt.Errorf("db error"),
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &mockEnrollmentStore{tokens: tt.tokens, listTokensErr: tt.storeErr}
			srv := newTestEnrollmentServer(ms)

			req := httptest.NewRequest(http.MethodGet, "/api/admin/enrollment-tokens", nil)
			w := httptest.NewRecorder()

			srv.handleListEnrollmentTokens(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d: %s", tt.wantStatus, w.Code, w.Body.String())
			}

			if tt.wantCount > 0 {
				var resp []store.EnrollmentToken
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
					t.Fatalf("failed to parse response: %v", err)
				}
				if len(resp) != tt.wantCount {
					t.Fatalf("expected %d tokens, got %d", tt.wantCount, len(resp))
				}
			}
		})
	}
}

// ---- handleAgentRegister tests ----

func TestHandleAgentRegister(t *testing.T) {
	validToken := &store.EnrollmentToken{
		ID:            "et-1",
		Token:         "valid-token",
		Provider:      "ovhcloud",
		ProviderClass: "registered",
		Labels:        json.RawMessage(`{}`),
		ExpiresAt:     time.Now().Add(24 * time.Hour),
	}

	expiredToken := &store.EnrollmentToken{
		ID:            "et-2",
		Token:         "expired-token",
		Provider:      "ovhcloud",
		ProviderClass: "registered",
		Labels:        json.RawMessage(`{}`),
		ExpiresAt:     time.Now().Add(-1 * time.Hour), // expired
	}

	usedHostID := 99
	usedToken := &store.EnrollmentToken{
		ID:            "et-3",
		Token:         "used-token",
		Provider:      "ovhcloud",
		ProviderClass: "registered",
		Labels:        json.RawMessage(`{}`),
		ExpiresAt:     time.Now().Add(24 * time.Hour),
		UsedByHostID:  &usedHostID,
	}

	tests := []struct {
		name           string
		body           string
		getTokenResult *store.EnrollmentToken
		getTokenErr    error
		createHostErr  error
		wantStatus     int
		wantHostID     bool
	}{
		{
			name:           "happy path",
			body:           `{"enrollment_token":"valid-token","agent_endpoint":"http://1.2.3.4:9090"}`,
			getTokenResult: validToken,
			wantStatus:     http.StatusCreated,
			wantHostID:     true,
		},
		{
			name:           "with capabilities",
			body:           `{"enrollment_token":"valid-token","agent_endpoint":"http://1.2.3.4:9090","capabilities":{"cpu_threads":8,"memory_mb":32768}}`,
			getTokenResult: validToken,
			wantStatus:     http.StatusCreated,
			wantHostID:     true,
		},
		{
			name:       "missing enrollment token",
			body:       `{"agent_endpoint":"http://1.2.3.4:9090"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid JSON",
			body:       `{bad json`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:        "invalid token (not found)",
			body:        `{"enrollment_token":"nonexistent"}`,
			getTokenErr: fmt.Errorf("not found"),
			wantStatus:  http.StatusUnauthorized,
		},
		{
			name:           "expired token",
			body:           `{"enrollment_token":"expired-token"}`,
			getTokenResult: expiredToken,
			wantStatus:     http.StatusUnauthorized,
		},
		{
			name:           "already used token",
			body:           `{"enrollment_token":"used-token"}`,
			getTokenResult: usedToken,
			wantStatus:     http.StatusConflict,
		},
		{
			name:           "store error on host creation",
			body:           `{"enrollment_token":"valid-token","agent_endpoint":"http://1.2.3.4:9090"}`,
			getTokenResult: validToken,
			createHostErr:  fmt.Errorf("db error"),
			wantStatus:     http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &mockEnrollmentStore{
				getTokenResult: tt.getTokenResult,
				getTokenErr:    tt.getTokenErr,
				createHostErr:  tt.createHostErr,
			}
			srv := newTestEnrollmentServer(ms)

			req := httptest.NewRequest(http.MethodPost, "/api/agent/register", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			srv.handleAgentRegister(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d: %s", tt.wantStatus, w.Code, w.Body.String())
			}

			if tt.wantHostID {
				var resp map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
					t.Fatalf("failed to parse response: %v", err)
				}
				if resp["host_id"] == nil {
					t.Fatal("expected host_id in response")
				}
				if resp["agent_token"] == nil || resp["agent_token"] == "" {
					t.Fatal("expected agent_token in response")
				}
				if resp["browser_rootfs_gcs_manifest"] != "" {
					t.Fatalf("expected empty browser_rootfs_gcs_manifest, got %v", resp["browser_rootfs_gcs_manifest"])
				}
				if resp["browser_rootfs_version"] != "" {
					t.Fatalf("expected empty browser_rootfs_version, got %v", resp["browser_rootfs_version"])
				}

				// Verify the enrollment token was marked as used
				if ms.markedTokenUsed == "" {
					t.Fatal("expected enrollment token to be marked as used")
				}
			}
		})
	}
}

func TestHandleAgentRegister_CapabilitiesExtracted(t *testing.T) {
	validToken := &store.EnrollmentToken{
		ID:            "et-cap",
		Token:         "cap-token",
		Provider:      "ovhcloud",
		ProviderClass: "registered",
		Labels:        json.RawMessage(`{}`),
		ExpiresAt:     time.Now().Add(24 * time.Hour),
	}

	ms := &mockEnrollmentStore{getTokenResult: validToken}
	srv := newTestEnrollmentServer(ms)

	body := `{"enrollment_token":"cap-token","agent_endpoint":"http://1.2.3.4:9090","capabilities":{"cpu_threads":16,"memory_mb":65536}}`
	req := httptest.NewRequest(http.MethodPost, "/api/agent/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleAgentRegister(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	if ms.createdHost == nil {
		t.Fatal("expected host to be created")
	}
	// cpu_threads=16, default oversubscription ratio=2 → capacity_vcpus=32
	if ms.createdHost.CapacityVCPUs != 32 {
		t.Fatalf("expected 32 vCPUs (16 threads * 2x ratio), got %d", ms.createdHost.CapacityVCPUs)
	}
	if ms.createdHost.CapacityMemoryMB != 65536 {
		t.Fatalf("expected 65536 MB, got %d", ms.createdHost.CapacityMemoryMB)
	}
	if ms.createdHost.Provider != "ovhcloud" {
		t.Fatalf("expected provider 'ovhcloud', got %q", ms.createdHost.Provider)
	}
	if ms.createdHost.LifecycleMode != "registered" {
		t.Fatalf("expected lifecycle_mode 'registered', got %q", ms.createdHost.LifecycleMode)
	}
}

func TestHandleAgentRegister_OversubscriptionRatios(t *testing.T) {
	validToken := &store.EnrollmentToken{
		ID:            "et-ratio",
		Token:         "ratio-token",
		Provider:      "ovhcloud",
		ProviderClass: "registered",
		Labels:        json.RawMessage(`{}`),
		ExpiresAt:     time.Now().Add(24 * time.Hour),
	}

	tests := []struct {
		name      string
		ratio     int
		cpuThread int
		wantVCPUs int
	}{
		{
			name:      "1x ratio (no oversubscription)",
			ratio:     1,
			cpuThread: 8,
			wantVCPUs: 8,
		},
		{
			name:      "2x ratio (default)",
			ratio:     2,
			cpuThread: 8,
			wantVCPUs: 16,
		},
		{
			name:      "3x ratio (max)",
			ratio:     3,
			cpuThread: 4,
			wantVCPUs: 12,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &mockEnrollmentStore{getTokenResult: validToken}
			srv := &Server{
				store:            ms,
				vcpuOversubRatio: tt.ratio,
			}

			body := fmt.Sprintf(`{"enrollment_token":"ratio-token","agent_endpoint":"http://1.2.3.4:9090","capabilities":{"cpu_threads":%d,"memory_mb":32768}}`, tt.cpuThread)
			req := httptest.NewRequest(http.MethodPost, "/api/agent/register", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			srv.handleAgentRegister(w, req)

			if w.Code != http.StatusCreated {
				t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
			}

			if ms.createdHost == nil {
				t.Fatal("expected host to be created")
			}
			if ms.createdHost.CapacityVCPUs != tt.wantVCPUs {
				t.Fatalf("expected %d vCPUs (%d threads * %dx ratio), got %d",
					tt.wantVCPUs, tt.cpuThread, tt.ratio, ms.createdHost.CapacityVCPUs)
			}
		})
	}
}

// ---- handleAgentRegister tunnel tests ----

func TestHandleAgentRegister_TunnelCreation(t *testing.T) {
	validToken := &store.EnrollmentToken{
		ID:            "et-tunnel",
		Token:         "tunnel-test-token",
		Provider:      "ovhcloud",
		ProviderClass: "registered",
		Labels:        json.RawMessage(`{}`),
		ExpiresAt:     time.Now().Add(24 * time.Hour),
	}

	t.Run("tunnel created and token returned", func(t *testing.T) {
		ms := &mockEnrollmentStore{getTokenResult: validToken}
		tm := &mockTunnelManager{tunnelID: "tun-123", tunnelToken: "cf-token-abc"}
		srv := newTestEnrollmentServer(ms)
		srv.SetDataPlaneDomain("openclawmachines.com")
		srv.tunnelCreator = tm

		body := `{"enrollment_token":"tunnel-test-token","agent_endpoint":"http://1.2.3.4:9090"}`
		req := httptest.NewRequest(http.MethodPost, "/api/agent/register", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		srv.handleAgentRegister(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		// Verify tunnel_token is in response
		if resp["tunnel_token"] != "cf-token-abc" {
			t.Fatalf("expected tunnel_token 'cf-token-abc', got %v", resp["tunnel_token"])
		}

		// Verify all tunnel steps were called
		if !tm.createCalled {
			t.Fatal("expected CreateTunnel to be called")
		}
		if !tm.configureCalled {
			t.Fatal("expected ConfigureTunnel to be called")
		}
		if !tm.dnsCalled {
			t.Fatal("expected CreateDNSRoute to be called")
		}

		// Verify host details were updated with tunnel_url
		if ms.updatedHost == nil {
			t.Fatal("expected UpdateHostDetails to be called")
		}
		if ms.updatedHost.TunnelURL == nil || !strings.HasSuffix(*ms.updatedHost.TunnelURL, ".openclawmachines.com") {
			t.Fatalf("expected tunnel_url to end with .openclawmachines.com, got %v", ms.updatedHost.TunnelURL)
		}

		// Verify provider_metadata was updated with tunnel_id
		if ms.updatedMetadata == nil {
			t.Fatal("expected UpdateHostProviderMetadata to be called")
		}
		var metadata map[string]interface{}
		if err := json.Unmarshal(ms.updatedMetadata, &metadata); err != nil {
			t.Fatalf("failed to parse metadata: %v", err)
		}
		if metadata["tunnel_id"] != "tun-123" {
			t.Fatalf("expected tunnel_id 'tun-123' in metadata, got %v", metadata["tunnel_id"])
		}
	})

	t.Run("registration fails when tunnel creation fails", func(t *testing.T) {
		ms := &mockEnrollmentStore{getTokenResult: validToken}
		tm := &mockTunnelManager{shouldFail: "create"}
		srv := newTestEnrollmentServer(ms)
		srv.SetDataPlaneDomain("openclawmachines.com")
		srv.tunnelCreator = tm

		body := `{"enrollment_token":"tunnel-test-token","agent_endpoint":"http://1.2.3.4:9090"}`
		req := httptest.NewRequest(http.MethodPost, "/api/agent/register", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		srv.handleAgentRegister(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
		}

		// Token should NOT be marked as used since we failed before that step
		if ms.markedTokenUsed != "" {
			t.Fatal("enrollment token should not be marked as used on tunnel failure")
		}
	})

	t.Run("configure failure cleans up tunnel", func(t *testing.T) {
		ms := &mockEnrollmentStore{getTokenResult: validToken}
		tm := &mockTunnelManager{tunnelID: "tun-456", tunnelToken: "cf-token-def", shouldFail: "configure"}
		srv := newTestEnrollmentServer(ms)
		srv.SetDataPlaneDomain("openclawmachines.com")
		srv.tunnelCreator = tm

		body := `{"enrollment_token":"tunnel-test-token","agent_endpoint":"http://1.2.3.4:9090"}`
		req := httptest.NewRequest(http.MethodPost, "/api/agent/register", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		srv.handleAgentRegister(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
		}

		// Verify cleanup was attempted
		if !tm.deleteCalled {
			t.Fatal("expected DeleteTunnel to be called for cleanup")
		}
	})

	t.Run("DNS failure cleans up tunnel", func(t *testing.T) {
		ms := &mockEnrollmentStore{getTokenResult: validToken}
		tm := &mockTunnelManager{tunnelID: "tun-789", tunnelToken: "cf-token-ghi", shouldFail: "dns"}
		srv := newTestEnrollmentServer(ms)
		srv.SetDataPlaneDomain("openclawmachines.com")
		srv.tunnelCreator = tm

		body := `{"enrollment_token":"tunnel-test-token","agent_endpoint":"http://1.2.3.4:9090"}`
		req := httptest.NewRequest(http.MethodPost, "/api/agent/register", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		srv.handleAgentRegister(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
		}

		if !tm.deleteCalled {
			t.Fatal("expected DeleteTunnel to be called for cleanup")
		}
	})

	t.Run("no tunnel when tunnelCreator is nil", func(t *testing.T) {
		ms := &mockEnrollmentStore{getTokenResult: validToken}
		srv := newTestEnrollmentServer(ms)
		// tunnelCreator is nil by default

		body := `{"enrollment_token":"tunnel-test-token","agent_endpoint":"http://1.2.3.4:9090"}`
		req := httptest.NewRequest(http.MethodPost, "/api/agent/register", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		srv.handleAgentRegister(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		// tunnel_token should be empty string when no tunnel creator
		if resp["tunnel_token"] != "" {
			t.Fatalf("expected empty tunnel_token, got %v", resp["tunnel_token"])
		}
	})
}

// ---- handleInstallScript tests ----

func TestHandleInstallScript(t *testing.T) {
	tests := []struct {
		name         string
		backendURL   string
		wantContains []string
	}{
		{
			name:       "local request backend URL",
			backendURL: "",
			wantContains: []string{
				"#!/bin/bash",
				"ENROLLMENT_TOKEN",
				"http://127.0.0.1:8080",
				"curl",
				"TUNNEL_TOKEN",
				"cloudflared",
				"/etc/ocm-agent/vm-state.env",
				"/etc/ocm-agent/browser-state.env",
				"BROWSER_ROOTFS_GCS_MANIFEST=${BROWSER_ROOTFS_MANIFEST}",
				"BROWSER_ROOTFS_VERSION=${BROWSER_ROOTFS_VERSION}",
			},
		},
		{
			name:       "custom backend URL",
			backendURL: "https://custom.example.com",
			wantContains: []string{
				"#!/bin/bash",
				"https://custom.example.com",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &mockEnrollmentStore{}
			srv := newTestEnrollmentServer(ms)
			srv.backendURL = tt.backendURL

			req := httptest.NewRequest(http.MethodGet, "/api/agent/install", nil)
			if tt.backendURL == "" {
				req.Host = "127.0.0.1:8080"
			}
			w := httptest.NewRecorder()

			srv.handleInstallScript(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d", w.Code)
			}

			contentType := w.Header().Get("Content-Type")
			if contentType != "text/plain; charset=utf-8" {
				t.Fatalf("expected text/plain content type, got %q", contentType)
			}

			body := w.Body.String()
			for _, s := range tt.wantContains {
				if !strings.Contains(body, s) {
					t.Errorf("expected response to contain %q", s)
				}
			}
		})
	}
}

func TestHandleInstallScriptRejectsUntrustedHostFallback(t *testing.T) {
	ms := &mockEnrollmentStore{}
	srv := newTestEnrollmentServer(ms)
	srv.backendURL = ""

	req := httptest.NewRequest(http.MethodGet, "/api/agent/install", nil)
	req.Host = "evil.example"
	w := httptest.NewRecorder()

	srv.handleInstallScript(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "evil.example") {
		t.Fatal("install script response reflected untrusted Host header")
	}
}

func TestInstallBackendURLAllowsHTTPSOrLoopbackHTTP(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{raw: "https://api.example.com", want: true},
		{raw: "http://localhost:8080", want: true},
		{raw: "http://127.0.0.1:8080", want: true},
		{raw: "http://api.example.com", want: false},
		{raw: "api.example.com", want: false},
	}
	for _, tt := range tests {
		if got := installBackendURLIsAllowed(tt.raw); got != tt.want {
			t.Fatalf("installBackendURLIsAllowed(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

// ---- handleDeregisterHost tests ----

func TestHandleDeregisterHost(t *testing.T) {
	t.Run("deregisters host with tunnel cleanup and machine cleanup", func(t *testing.T) {
		tunnelURL := "ocm-host-123.openclawmachines.com"
		ms := &mockEnrollmentStore{
			getHostResult: &store.Host{
				ID:               42,
				TunnelURL:        &tunnelURL,
				ProviderMetadata: json.RawMessage(`{"tunnel_id":"tun-abc"}`),
			},
		}
		tm := &mockTunnelManager{tunnelID: "tun-abc"}
		srv := newTestEnrollmentServer(ms)
		srv.tunnelCreator = tm

		req := httptest.NewRequest(http.MethodPost, "/api/admin/hosts/42/deregister", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("hostId", "42")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()

		srv.handleDeregisterHost(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		if resp["status"] != "deregistered" {
			t.Fatalf("expected status 'deregistered', got %q", resp["status"])
		}

		// Verify tunnel cleanup was called
		if !tm.deleteCalled {
			t.Fatal("expected DeleteTunnelAndDNS to be called")
		}

		// Verify machine cleanup was called
		if !ms.markMachinesErrorCalled {
			t.Fatal("expected MarkMachinesOnHostError to be called")
		}
		if ms.markMachinesErrorHostID != 42 {
			t.Fatalf("expected MarkMachinesOnHostError called with host 42, got %d", ms.markMachinesErrorHostID)
		}

		if ms.updatedStatusHostID != 42 {
			t.Fatalf("expected UpdateHostStatus called with host 42, got %d", ms.updatedStatusHostID)
		}
		if ms.updatedStatus != "terminated" {
			t.Fatalf("expected status 'terminated', got %q", ms.updatedStatus)
		}
	})

	t.Run("404 for unknown host", func(t *testing.T) {
		ms := &mockEnrollmentStore{
			getHostErr: fmt.Errorf("not found"),
		}
		srv := newTestEnrollmentServer(ms)

		req := httptest.NewRequest(http.MethodPost, "/api/admin/hosts/999/deregister", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("hostId", "999")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()

		srv.handleDeregisterHost(w, req)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("handles host with no tunnel gracefully", func(t *testing.T) {
		ms := &mockEnrollmentStore{
			getHostResult: &store.Host{
				ID:               55,
				ProviderMetadata: json.RawMessage(`{}`),
			},
		}
		srv := newTestEnrollmentServer(ms)

		req := httptest.NewRequest(http.MethodPost, "/api/admin/hosts/55/deregister", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("hostId", "55")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()

		srv.handleDeregisterHost(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		if ms.updatedStatusHostID != 55 {
			t.Fatalf("expected UpdateHostStatus called with host 55, got %d", ms.updatedStatusHostID)
		}
	})

	t.Run("invalid host id", func(t *testing.T) {
		ms := &mockEnrollmentStore{}
		srv := newTestEnrollmentServer(ms)

		req := httptest.NewRequest(http.MethodPost, "/api/admin/hosts/abc/deregister", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("hostId", "abc")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()

		srv.handleDeregisterHost(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("store error on status update", func(t *testing.T) {
		ms := &mockEnrollmentStore{
			getHostResult: &store.Host{
				ID:               42,
				ProviderMetadata: json.RawMessage(`{}`),
			},
			updateStatusErr: fmt.Errorf("db error"),
		}
		srv := newTestEnrollmentServer(ms)

		req := httptest.NewRequest(http.MethodPost, "/api/admin/hosts/42/deregister", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("hostId", "42")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()

		srv.handleDeregisterHost(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("expected status 500, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// --- Plugin stubs (satisfy store.Store interface) ---

func (m *mockEnrollmentStore) ListPluginCatalog(_ context.Context) ([]store.PluginCatalogEntry, error) {
	return nil, nil
}
func (m *mockEnrollmentStore) GetPluginCatalogEntry(_ context.Context, _ string) (*store.PluginCatalogEntry, error) {
	return nil, fmt.Errorf("not found")
}
func (m *mockEnrollmentStore) CreatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockEnrollmentStore) UpdatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockEnrollmentStore) DeletePluginCatalogEntry(_ context.Context, _ string) error {
	return nil
}
func (m *mockEnrollmentStore) ListMachinePlugins(_ context.Context, _ string) ([]store.MachinePlugin, error) {
	return nil, nil
}
func (m *mockEnrollmentStore) EnableMachinePlugin(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockEnrollmentStore) DisableMachinePlugin(_ context.Context, _, _ string) error {
	return nil
}
func (m *mockEnrollmentStore) UpdateMachinePluginOverrides(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockEnrollmentStore) UpdateMachinePluginStatus(_ context.Context, _, _, _, _ string) error {
	return nil
}
