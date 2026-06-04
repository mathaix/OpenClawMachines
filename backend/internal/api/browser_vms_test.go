package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/fleet"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// mockBrowserVMStore provides the minimum surface needed to hit the early
// validation paths of handleStartBrowserVM (before the placement call) and
// to track the mutations handleStopBrowserVM performs on the DB side.
type mockBrowserVMStore struct {
	store.Store

	bvm              *store.BrowserVM
	host             *store.Host
	machines         []store.Machine
	reconcileBVMS    []store.BrowserVM
	machinesByAccErr error

	statusUpdates     []statusUpdateCall
	placementReleased bool
	hostUnassigned    bool
	deleted           bool
	assignedHostID    int
	assignedVMIP      string
	activateCalled    bool

	listEligibleHosts []store.Host
	placeBrowserVMIP  string
	placeBrowserErr   error
}

type statusUpdateCall struct {
	Status string
	ErrMsg string
}

func (m *mockBrowserVMStore) GetBrowserVM(_ context.Context, id string) (*store.BrowserVM, error) {
	if m.bvm != nil && m.bvm.ID == id {
		return m.bvm, nil
	}
	return nil, nil
}

func (m *mockBrowserVMStore) GetMachineByBrowserVMID(_ context.Context, _ string) (*store.Machine, error) {
	return nil, nil
}

func (m *mockBrowserVMStore) GetHost(_ context.Context, id int) (*store.Host, error) {
	if m.host != nil && m.host.ID == id {
		return m.host, nil
	}
	return nil, nil
}

func (m *mockBrowserVMStore) UpdateBrowserVMStatus(_ context.Context, _, status string, errMsg *string) error {
	call := statusUpdateCall{Status: status}
	if errMsg != nil {
		call.ErrMsg = *errMsg
	}
	m.statusUpdates = append(m.statusUpdates, call)
	if m.bvm != nil {
		m.bvm.Status = status
		m.bvm.ErrorMessage = errMsg
	}
	return nil
}

func (m *mockBrowserVMStore) AssignBrowserVMToHost(_ context.Context, _ string, hostID int, vmIP string) error {
	m.assignedHostID = hostID
	m.assignedVMIP = vmIP
	if m.bvm != nil {
		m.bvm.HostID = &hostID
		m.bvm.VMIP = &vmIP
	}
	return nil
}

func (m *mockBrowserVMStore) ReleaseBrowserVMPlacement(_ context.Context, _ string) error {
	m.placementReleased = true
	return nil
}

func (m *mockBrowserVMStore) UnassignBrowserVMFromHost(_ context.Context, _ string) error {
	m.hostUnassigned = true
	if m.bvm != nil {
		m.bvm.HostID = nil
		m.bvm.VMIP = nil
	}
	return nil
}

func (m *mockBrowserVMStore) DeleteBrowserVM(_ context.Context, _ string) error {
	m.deleted = true
	return nil
}

func (m *mockBrowserVMStore) ActivateBrowserVMPlacement(_ context.Context, _ string) error {
	m.activateCalled = true
	return nil
}

func (m *mockBrowserVMStore) ListMachinesByAccount(_ context.Context, _ int) ([]store.Machine, error) {
	if m.machinesByAccErr != nil {
		return nil, m.machinesByAccErr
	}
	return m.machines, nil
}

func (m *mockBrowserVMStore) ListBrowserVMsRequiringReconciliation(_ context.Context) ([]store.BrowserVM, error) {
	return append([]store.BrowserVM(nil), m.reconcileBVMS...), nil
}

func (m *mockBrowserVMStore) ListEligibleHosts(_ context.Context, _ store.HostFilter) ([]store.Host, error) {
	return append([]store.Host(nil), m.listEligibleHosts...), nil
}

func (m *mockBrowserVMStore) PlaceBrowserVMOnHost(_ context.Context, hostID int, _ string, _, _ int) (string, error) {
	if m.placeBrowserErr != nil {
		return "", m.placeBrowserErr
	}
	if m.placeBrowserVMIP != "" {
		return m.placeBrowserVMIP, nil
	}
	return "192.168.100.2", nil
}

func newStartBrowserVMRequest(t *testing.T, email string, body map[string]any) *http.Request {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/browser-vms/bvm-1/start", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{UserID: 1, Email: email}))
	req = req.WithContext(context.WithValue(req.Context(), accountIDKey, 42))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("browserVmId", "bvm-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req
}

func TestBrowserRootfsSelectionPresets(t *testing.T) {
	const browserManifest = "gs://example-ocm-artifacts/kernel-browser-rootfs/manifest.json"
	const browserVersion = "kernel-browser-rootfs-v1"
	cases := []struct {
		name         string
		image        string
		manifest     string
		version      string
		wantManifest *string
		wantVersion  *string
		wantErr      string
	}{
		{name: "default uses host default"},
		{
			name:         "kernel stable pins known-good kernel image",
			image:        "kernel-stable",
			wantManifest: ptr(browserManifest),
			wantVersion:  ptr(browserVersion),
		},
		{
			name:         "kernel experimental uses latest kernel manifest release",
			image:        "kernel-experimental",
			wantManifest: ptr(browserManifest),
		},
		{
			name:         "version without manifest targets kernel manifest",
			version:      browserVersion,
			wantManifest: ptr(browserManifest),
			wantVersion:  ptr(browserVersion),
		},
		{
			name:     "image and manifest are mutually exclusive",
			image:    "kernel-stable",
			manifest: browserManifest,
			wantErr:  "browser_image cannot be combined",
		},
		{
			name:    "legacy image rejected",
			image:   "legacy",
			wantErr: "browser_image must be one of",
		},
		{
			name:    "unknown image rejected",
			image:   "custom",
			wantErr: "browser_image must be one of",
		},
		{
			name:         "custom manifest accepted",
			manifest:     "gs://example/browser-rootfs/manifest.json",
			wantManifest: ptr("gs://example/browser-rootfs/manifest.json"),
		},
		{
			name:    "bad version rejected",
			version: "not a version",
			wantErr: "invalid rootfs version",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			gotManifest, gotVersion, err := browserRootfsSelection(tc.image, tc.manifest, tc.version, browserManifest, browserVersion)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !sameStringPtr(gotManifest, tc.wantManifest) {
				t.Fatalf("manifest = %v, want %v", gotManifest, tc.wantManifest)
			}
			if !sameStringPtr(gotVersion, tc.wantVersion) {
				t.Fatalf("version = %v, want %v", gotVersion, tc.wantVersion)
			}
		})
	}
}

func TestBrowserVMRequiresReflink(t *testing.T) {
	const browserManifest = "gs://example-ocm-artifacts/kernel-browser-rootfs/manifest.json"
	const browserVersion = "kernel-browser-rootfs-v1"
	cases := []struct {
		name    string
		bvm     *store.BrowserVM
		wantReq bool
	}{
		{name: "nil browser VM"},
		{name: "default browser rootfs", bvm: &store.BrowserVM{}},
		{
			name: "kernel stable version allows fallback copy",
			bvm: &store.BrowserVM{
				DesiredRootfsManifest: ptr(browserManifest),
				DesiredRootfsVersion:  ptr(browserVersion),
			},
		},
		{
			name: "kernel latest requires reflink",
			bvm: &store.BrowserVM{
				DesiredRootfsManifest: ptr(browserManifest),
			},
			wantReq: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := browserVMRequiresReflink(tc.bvm); got != tc.wantReq {
				t.Fatalf("browserVMRequiresReflink() = %v, want %v", got, tc.wantReq)
			}
		})
	}
}

func TestBrowserVMUsesDisabledLegacyRootfs(t *testing.T) {
	cases := []struct {
		name string
		bvm  *store.BrowserVM
		want bool
	}{
		{name: "nil browser VM"},
		{name: "host default", bvm: &store.BrowserVM{}},
		{name: "kernel browser rootfs", bvm: &store.BrowserVM{DesiredRootfsManifest: ptr("gs://example-ocm-artifacts/kernel-browser-rootfs/manifest.json")}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := browserVMUsesDisabledLegacyRootfs(tc.bvm); got != tc.want {
				t.Fatalf("browserVMUsesDisabledLegacyRootfs() = %v, want %v", got, tc.want)
			}
		})
	}
}

func sameStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// TestHandleStartBrowserVM_HostIDPlacementGate covers the narrowed H9 gate:
// non-superuser callers may pin host_id only to a host they already occupy
// (prevents fleet enumeration), region stays superuser-only (prevents
// capacity starvation), and superusers bypass both.
func TestHandleStartBrowserVM_HostIDPlacementGate(t *testing.T) {
	hostID := 7
	homeHostID := 9

	cases := []struct {
		name       string
		email      string
		body       map[string]any
		machines   []store.Machine
		wantStatus int
		wantBody   string
	}{
		{
			name:       "tenant without machine on host is rejected",
			email:      "tenant@example.com",
			body:       map[string]any{"host_id": hostID},
			machines:   nil,
			wantStatus: http.StatusForbidden,
			wantBody:   "host_id must reference a host where this account already runs a machine",
		},
		{
			name:       "tenant region override still requires admin",
			email:      "tenant@example.com",
			body:       map[string]any{"region": "us-east1"},
			machines:   nil,
			wantStatus: http.StatusForbidden,
			wantBody:   "region placement overrides require admin role",
		},
		{
			name:  "tenant with machine on same host passes host_id gate",
			email: "tenant@example.com",
			body:  map[string]any{"host_id": hostID},
			machines: []store.Machine{
				{ID: "m-1", HostID: &hostID},
			},
			// Placement will fail against the nil s.placement in this test
			// server — so the gate passed but a downstream panic would be
			// distinguishable from a 403. We only assert the gate didn't
			// fire; we don't run the full handler.
			wantStatus: 0,
			wantBody:   "",
		},
		{
			name:  "tenant with machine homed on host passes host_id gate",
			email: "tenant@example.com",
			body:  map[string]any{"host_id": homeHostID},
			machines: []store.Machine{
				{ID: "m-1", HomeHostID: &homeHostID},
			},
			wantStatus: 0,
			wantBody:   "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ms := &mockBrowserVMStore{
				bvm: &store.BrowserVM{
					ID:        "bvm-1",
					AccountID: 42,
					Status:    "stopped",
				},
				machines: tc.machines,
			}
			srv := &Server{store: ms}

			req := newStartBrowserVMRequest(t, tc.email, tc.body)
			rec := httptest.NewRecorder()

			if tc.wantStatus == 0 {
				// Expect the gate to pass — a downstream nil placement service
				// will panic, which we recover here. Absence of a 403 is the
				// signal; the panic confirms we got past the gate.
				defer func() {
					_ = recover()
					if rec.Code == http.StatusForbidden {
						t.Fatalf("gate rejected owned host; body=%s", rec.Body.String())
					}
				}()
				srv.handleStartBrowserVM(rec, req)
				return
			}

			srv.handleStartBrowserVM(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("expected body to contain %q, got %s", tc.wantBody, rec.Body.String())
			}
		})
	}
}

func newStopBrowserVMRequest(t *testing.T) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/browser-vms/bvm-1/stop", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{UserID: 1, Email: "tenant@example.com"}))
	req = req.WithContext(context.WithValue(req.Context(), accountIDKey, 42))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("browserVmId", "bvm-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req
}

func newBrowserVMTestAgent(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *store.Host, *http.Client) {
	t.Helper()
	server := httptest.NewServer(handler)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	endpoint := "http://agent.test:" + port
	host := &store.Host{ID: 7, AgentEndpoint: &endpoint}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "agent.test:") {
				addr = server.Listener.Addr().String()
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return server, host, &http.Client{Transport: transport}
}

// TestHandleStopBrowserVM_FailsOnDestroyError verifies review finding #2:
// a failed DestroyBrowserVM call must NOT silently drop the DB placement
// and set status=stopped, because that leaves a live Firecracker/Neko
// orphan on the host with no control-plane tracking. The handler must
// return the error, keep status pointing at "error" with the message,
// and leave placement + host assignment intact for retry.
//
// The test drives the real agentclient against a host whose AgentEndpoint
// points at a non-listening IP/port; that produces a deterministic connect
// failure which the handler must translate into 502 + no DB mutation.
func TestHandleStopBrowserVM_FailsOnDestroyError(t *testing.T) {
	// Reserve a TCP port that nothing is listening on so the destroy
	// request fails fast with a "connection refused" we don't have to
	// mock. Using ExternalIP (not AgentEndpoint) routes the agent client
	// through the IP-based URL builder, bypassing agentURL's reject list
	// for 127.0.0.1.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	addr := ln.Addr().String()
	host, _, _ := net.SplitHostPort(addr)
	_ = ln.Close() // free the port so the connect refuses immediately

	externalIP := host
	hostID := 7
	ms := &mockBrowserVMStore{
		bvm: &store.BrowserVM{
			ID:        "bvm-1",
			AccountID: 42,
			Status:    "running",
			HostID:    &hostID,
		},
		host: &store.Host{
			ID:         hostID,
			ExternalIP: &externalIP,
		},
	}
	srv := &Server{
		store:       ms,
		agentClient: agentclient.New("test-token"),
	}
	// Make the destroy call bail out fast so the test is responsive.
	srv.agentClient.SetHTTPClient(&http.Client{Timeout: 500 * time.Millisecond})

	rec := httptest.NewRecorder()
	srv.handleStopBrowserVM(rec, newStopBrowserVMRequest(t))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (BadGateway); body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "agent destroy failed") {
		t.Fatalf("expected error body to mention agent destroy failed, got %s", rec.Body.String())
	}

	// The stop handler must NOT have dropped the placement or host
	// assignment — the VM is still running on that host and needs to be
	// retried to avoid orphaning it.
	if ms.placementReleased {
		t.Fatal("placement must not be released when destroy failed")
	}
	if ms.hostUnassigned {
		t.Fatal("host assignment must not be cleared when destroy failed")
	}

	// Status must never be set to "stopped" and must include an error
	// update carrying the destroy failure message.
	for _, upd := range ms.statusUpdates {
		if upd.Status == "stopped" {
			t.Fatalf("status must not transition to stopped when destroy failed; got updates=%+v", ms.statusUpdates)
		}
	}
	sawErrorWithMessage := false
	for _, upd := range ms.statusUpdates {
		if upd.Status == "error" && strings.Contains(upd.ErrMsg, "agent destroy failed") {
			sawErrorWithMessage = true
			break
		}
	}
	if !sawErrorWithMessage {
		t.Fatalf("expected status=error with destroy-failure message; got updates=%+v", ms.statusUpdates)
	}
}

// TestHandleStopBrowserVM_SuccessReleasesPlacement is the complementary
// happy-path case: when there is no agent (or the destroy succeeds), the
// handler DOES release placement, unassign the host, and set status=stopped.
// Without this paired assertion, TestHandleStopBrowserVM_FailsOnDestroyError
// could drift into over-tightening the handler (e.g., returning 502 even on
// the happy path).
func TestHandleStopBrowserVM_SuccessReleasesPlacement(t *testing.T) {
	hostID := 7
	ms := &mockBrowserVMStore{
		bvm: &store.BrowserVM{
			ID:        "bvm-1",
			AccountID: 42,
			Status:    "running",
			HostID:    &hostID,
		},
	}
	// agentClient == nil → the destroy block is skipped entirely, as if
	// the agent call succeeded (or there is no agent to call).
	srv := &Server{store: ms}

	rec := httptest.NewRecorder()
	srv.handleStopBrowserVM(rec, newStopBrowserVMRequest(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !ms.placementReleased {
		t.Fatal("happy path must release placement")
	}
	if !ms.hostUnassigned {
		t.Fatal("happy path must clear host assignment")
	}
	sawStopped := false
	for _, upd := range ms.statusUpdates {
		if upd.Status == "stopped" {
			sawStopped = true
			break
		}
	}
	if !sawStopped {
		t.Fatalf("happy path must set status to stopped; got updates=%+v", ms.statusUpdates)
	}
}

// TestHandleStartBrowserVM_AcceptsTenantWithoutPinning verifies that a
// non-superuser request that omits host_id and region passes the H9 gate.
// The handler proceeds past the check (and eventually fails at the
// placement/status-update layer because the mock store is minimal), so we
// only assert that the response is NOT the 403 from the H9 check.
func TestHandleStartBrowserVM_AcceptsTenantWithoutPinning(t *testing.T) {
	ms := &mockBrowserVMStore{
		bvm: &store.BrowserVM{
			ID:        "bvm-1",
			AccountID: 42,
			Status:    "stopped",
		},
	}
	srv := &Server{store: ms}

	req := newStartBrowserVMRequest(t, "tenant@example.com", map[string]any{})
	rec := httptest.NewRecorder()
	// Recover from downstream nil-deref panics caused by the mock's absent
	// UpdateBrowserVMStatus / placement — we only care about whether the
	// H9 gate fired.
	defer func() {
		_ = recover()
	}()
	srv.handleStartBrowserVM(rec, req)

	if rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "admin role") {
		t.Fatalf("non-superuser without host_id/region should not hit the admin gate; got %s", rec.Body.String())
	}
}

func TestHandleStartBrowserVM_ReadinessTimeoutPreservesAgentOwnership(t *testing.T) {
	agentServer, host, httpClient := newBrowserVMTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/browser-vms":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"creating"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/browser-vms/bvm-1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"browser_vm_id":"bvm-1","status":"creating","cdp_reachable":false}`))
		default:
			t.Fatalf("unexpected agent request: %s %s", r.Method, r.URL.Path)
		}
	})
	defer agentServer.Close()

	ms := &mockBrowserVMStore{
		bvm: &store.BrowserVM{
			ID:        "bvm-1",
			AccountID: 42,
			Status:    "stopped",
			VCPUs:     2,
			MemoryMB:  4096,
		},
		host:              host,
		listEligibleHosts: []store.Host{*host},
		placeBrowserVMIP:  "192.168.100.2",
	}
	agentCli := agentclient.New("test-token")
	agentCli.SetHTTPClient(httpClient)
	srv := &Server{
		store:       ms,
		placement:   fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "test"}),
		agentClient: agentCli,
	}

	req := newStartBrowserVMRequest(t, "tenant@example.com", map[string]any{})
	ctx, cancel := context.WithTimeout(req.Context(), 40*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	srv.handleStartBrowserVM(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504; body=%s", rec.Code, rec.Body.String())
	}
	if ms.placementReleased {
		t.Fatal("placement must be preserved when readiness times out after agent accepted create")
	}
	if ms.hostUnassigned {
		t.Fatal("host assignment must be preserved when readiness times out after agent accepted create")
	}
	if ms.bvm.HostID == nil || *ms.bvm.HostID != host.ID {
		t.Fatalf("expected browser VM to remain assigned to host %d, got %+v", host.ID, ms.bvm.HostID)
	}
	if ms.bvm.VMIP == nil || *ms.bvm.VMIP != "192.168.100.2" {
		t.Fatalf("expected browser VM IP to remain assigned, got %+v", ms.bvm.VMIP)
	}
	sawError := false
	for _, upd := range ms.statusUpdates {
		if upd.Status == "error" {
			sawError = true
			break
		}
	}
	if !sawError {
		t.Fatalf("expected status=error update, got %+v", ms.statusUpdates)
	}
}

func TestHandleStartBrowserVM_ForwardsDesiredRootfsSelection(t *testing.T) {
	const desiredManifest = "gs://example-ocm-artifacts/kernel-browser-rootfs/manifest.json"
	const desiredVersion = "kernel-browser-rootfs-v1"
	createSeen := false
	agentServer, host, httpClient := newBrowserVMTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/browser-vms":
			createSeen = true
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if req["rootfs_manifest"] != desiredManifest {
				t.Fatalf("rootfs_manifest = %v, want %q", req["rootfs_manifest"], desiredManifest)
			}
			if req["rootfs_version"] != desiredVersion {
				t.Fatalf("rootfs_version = %v, want %q", req["rootfs_version"], desiredVersion)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"creating"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/browser-vms/bvm-1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"browser_vm_id":"bvm-1","status":"running","cdp_reachable":true,"cdp_version":"Chrome/148"}`))
		default:
			t.Fatalf("unexpected agent request: %s %s", r.Method, r.URL.Path)
		}
	})
	defer agentServer.Close()

	ms := &mockBrowserVMStore{
		bvm: &store.BrowserVM{
			ID:                    "bvm-1",
			AccountID:             42,
			Status:                "stopped",
			VCPUs:                 2,
			MemoryMB:              4096,
			DesiredRootfsManifest: ptr(desiredManifest),
			DesiredRootfsVersion:  ptr(desiredVersion),
		},
		host:              host,
		listEligibleHosts: []store.Host{*host},
		placeBrowserVMIP:  "192.168.100.2",
	}
	agentCli := agentclient.New("test-token")
	agentCli.SetHTTPClient(httpClient)
	srv := &Server{
		store:       ms,
		placement:   fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "test"}),
		agentClient: agentCli,
	}

	rec := httptest.NewRecorder()
	srv.handleStartBrowserVM(rec, newStartBrowserVMRequest(t, "tenant@example.com", map[string]any{}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !createSeen {
		t.Fatal("agent create request was not sent")
	}
	if !ms.activateCalled {
		t.Fatal("ready browser VM should activate placement")
	}
}

func TestHandleStartBrowserVM_DefiniteAgentFailureReleasesPlacement(t *testing.T) {
	agentServer, host, httpClient := newBrowserVMTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/browser-vms" {
			t.Fatalf("unexpected agent request: %s %s", r.Method, r.URL.Path)
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	defer agentServer.Close()

	ms := &mockBrowserVMStore{
		bvm: &store.BrowserVM{
			ID:        "bvm-1",
			AccountID: 42,
			Status:    "stopped",
			VCPUs:     2,
			MemoryMB:  4096,
		},
		host:              host,
		listEligibleHosts: []store.Host{*host},
		placeBrowserVMIP:  "192.168.100.2",
	}
	agentCli := agentclient.New("test-token")
	agentCli.SetHTTPClient(httpClient)
	srv := &Server{
		store:       ms,
		placement:   fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "test"}),
		agentClient: agentCli,
	}

	rec := httptest.NewRecorder()
	srv.handleStartBrowserVM(rec, newStartBrowserVMRequest(t, "tenant@example.com", map[string]any{}))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	if !ms.placementReleased {
		t.Fatal("definite agent create failure must release placement")
	}
	if !ms.hostUnassigned {
		t.Fatal("definite agent create failure must clear host assignment")
	}
}

func TestHandleStartBrowserVM_ErrorWithHostMustBeStoppedBeforeRetry(t *testing.T) {
	hostID := 7
	vmIP := "192.168.100.2"
	ms := &mockBrowserVMStore{
		bvm: &store.BrowserVM{
			ID:        "bvm-1",
			AccountID: 42,
			Status:    "error",
			HostID:    &hostID,
			VMIP:      &vmIP,
		},
	}
	srv := &Server{store: ms}

	rec := httptest.NewRecorder()
	srv.handleStartBrowserVM(rec, newStartBrowserVMRequest(t, "tenant@example.com", map[string]any{}))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if ms.placementReleased || ms.hostUnassigned {
		t.Fatalf("start retry must not mutate placement for error+host; released=%t unassigned=%t", ms.placementReleased, ms.hostUnassigned)
	}
}

func TestHandleStopBrowserVM_ErrorWithHostDestroysAndReleasesPlacement(t *testing.T) {
	agentServer, host, httpClient := newBrowserVMTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/browser-vms/bvm-1" {
			t.Fatalf("unexpected agent request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	defer agentServer.Close()

	hostID := host.ID
	ms := &mockBrowserVMStore{
		bvm: &store.BrowserVM{
			ID:        "bvm-1",
			AccountID: 42,
			Status:    "error",
			HostID:    &hostID,
		},
		host: host,
	}
	agentCli := agentclient.New("test-token")
	agentCli.SetHTTPClient(httpClient)
	srv := &Server{store: ms, agentClient: agentCli}

	rec := httptest.NewRecorder()
	srv.handleStopBrowserVM(rec, newStopBrowserVMRequest(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !ms.placementReleased {
		t.Fatal("stop must release placement after destroying error+host browser VM")
	}
	if !ms.hostUnassigned {
		t.Fatal("stop must clear host assignment after destroying error+host browser VM")
	}
	sawStopped := false
	for _, upd := range ms.statusUpdates {
		if upd.Status == "stopped" {
			sawStopped = true
			break
		}
	}
	if !sawStopped {
		t.Fatalf("expected stopped status update, got %+v", ms.statusUpdates)
	}
}

func TestHandleDeleteBrowserVM_ErrorWithHostDestroysBeforeDelete(t *testing.T) {
	agentServer, host, httpClient := newBrowserVMTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/browser-vms/bvm-1" {
			t.Fatalf("unexpected agent request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	defer agentServer.Close()

	hostID := host.ID
	ms := &mockBrowserVMStore{
		bvm: &store.BrowserVM{
			ID:        "bvm-1",
			AccountID: 42,
			Status:    "error",
			HostID:    &hostID,
		},
		host: host,
	}
	agentCli := agentclient.New("test-token")
	agentCli.SetHTTPClient(httpClient)
	srv := &Server{store: ms, agentClient: agentCli}

	req := httptest.NewRequest(http.MethodDelete, "/api/browser-vms/bvm-1", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{UserID: 1, Email: "tenant@example.com"}))
	req = req.WithContext(context.WithValue(req.Context(), accountIDKey, 42))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("browserVmId", "bvm-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleDeleteBrowserVM(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if !ms.placementReleased {
		t.Fatal("delete must release placement after destroying error+host browser VM")
	}
	if !ms.hostUnassigned {
		t.Fatal("delete must clear host assignment after destroying error+host browser VM")
	}
	if !ms.deleted {
		t.Fatal("delete must remove DB browser VM after agent destroy succeeds")
	}
}

func TestHandleListBrowserVMReconcileCandidates(t *testing.T) {
	hostID := 7
	vmIP := "192.168.100.2"
	ms := &mockBrowserVMStore{
		reconcileBVMS: []store.BrowserVM{
			{ID: "bvm-1", AccountID: 42, Status: "error", HostID: &hostID, VMIP: &vmIP},
		},
	}
	srv := &Server{store: ms}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/browser-vms/reconcile-candidates", nil)
	rec := httptest.NewRecorder()
	srv.handleListBrowserVMReconcileCandidates(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "bvm-1") {
		t.Fatalf("expected candidate response to contain bvm-1, got %s", rec.Body.String())
	}
}

func TestHandleReconcileBrowserVM_ReadyAgentMarksRunning(t *testing.T) {
	agentServer, host, httpClient := newBrowserVMTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/browser-vms/bvm-1/health" {
			t.Fatalf("unexpected agent request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"browser_vm_id":"bvm-1","status":"ready","cdp_reachable":true,"cdp_version":"Chrome/148"}`))
	})
	defer agentServer.Close()

	hostID := host.ID
	vmIP := "192.168.100.2"
	ms := &mockBrowserVMStore{
		bvm: &store.BrowserVM{
			ID:        "bvm-1",
			AccountID: 42,
			Status:    "error",
			HostID:    &hostID,
			VMIP:      &vmIP,
		},
		host: host,
	}
	agentCli := agentclient.New("test-token")
	agentCli.SetHTTPClient(httpClient)
	srv := &Server{store: ms, agentClient: agentCli}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/browser-vms/bvm-1/reconcile", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("browserVmId", "bvm-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleReconcileBrowserVM(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !ms.activateCalled {
		t.Fatal("ready agent browser VM must activate placement")
	}
	if ms.bvm.Status != "running" {
		t.Fatalf("expected status running, got %q", ms.bvm.Status)
	}
	if ms.placementReleased || ms.hostUnassigned {
		t.Fatalf("ready reconcile must not release ownership; released=%t unassigned=%t", ms.placementReleased, ms.hostUnassigned)
	}
}

func TestHandleReconcileBrowserVM_AgentNotFoundReleasesPlacement(t *testing.T) {
	agentServer, host, httpClient := newBrowserVMTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/browser-vms/bvm-1/health" {
			t.Fatalf("unexpected agent request: %s %s", r.Method, r.URL.Path)
		}
		http.Error(w, "not found", http.StatusNotFound)
	})
	defer agentServer.Close()

	hostID := host.ID
	vmIP := "192.168.100.2"
	ms := &mockBrowserVMStore{
		bvm: &store.BrowserVM{
			ID:        "bvm-1",
			AccountID: 42,
			Status:    "error",
			HostID:    &hostID,
			VMIP:      &vmIP,
		},
		host: host,
	}
	agentCli := agentclient.New("test-token")
	agentCli.SetHTTPClient(httpClient)
	srv := &Server{store: ms, agentClient: agentCli}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/browser-vms/bvm-1/reconcile", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("browserVmId", "bvm-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleReconcileBrowserVM(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !ms.placementReleased {
		t.Fatal("agent-not-found reconcile must release placement")
	}
	if !ms.hostUnassigned {
		t.Fatal("agent-not-found reconcile must clear host assignment")
	}
	if ms.bvm.Status != "stopped" {
		t.Fatalf("expected status stopped, got %q", ms.bvm.Status)
	}
}

func TestHandleReconcileBrowserVM_NotReadyKeepsOwnership(t *testing.T) {
	agentServer, host, httpClient := newBrowserVMTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/browser-vms/bvm-1/health" {
			t.Fatalf("unexpected agent request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"browser_vm_id":"bvm-1","status":"creating","cdp_reachable":false,"cdp_error":"still booting"}`))
	})
	defer agentServer.Close()

	hostID := host.ID
	vmIP := "192.168.100.2"
	ms := &mockBrowserVMStore{
		bvm: &store.BrowserVM{
			ID:        "bvm-1",
			AccountID: 42,
			Status:    "error",
			HostID:    &hostID,
			VMIP:      &vmIP,
		},
		host: host,
	}
	agentCli := agentclient.New("test-token")
	agentCli.SetHTTPClient(httpClient)
	srv := &Server{store: ms, agentClient: agentCli}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/browser-vms/bvm-1/reconcile", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("browserVmId", "bvm-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleReconcileBrowserVM(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ms.placementReleased || ms.hostUnassigned {
		t.Fatalf("not-ready reconcile must preserve ownership; released=%t unassigned=%t", ms.placementReleased, ms.hostUnassigned)
	}
	if ms.bvm.Status != "error" {
		t.Fatalf("expected status error, got %q", ms.bvm.Status)
	}
	if ms.bvm.ErrorMessage == nil || !strings.Contains(*ms.bvm.ErrorMessage, "still booting") {
		t.Fatalf("expected error message to include agent CDP error, got %+v", ms.bvm.ErrorMessage)
	}
}

func TestReconcileBrowserVMCandidatesOnceProcessesCandidateList(t *testing.T) {
	agentServer, host, httpClient := newBrowserVMTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/browser-vms/bvm-1/health" {
			t.Fatalf("unexpected agent request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"browser_vm_id":"bvm-1","status":"ready","cdp_reachable":true}`))
	})
	defer agentServer.Close()

	hostID := host.ID
	vmIP := "192.168.100.2"
	ms := &mockBrowserVMStore{
		bvm: &store.BrowserVM{
			ID:        "bvm-1",
			AccountID: 42,
			Status:    "error",
			HostID:    &hostID,
			VMIP:      &vmIP,
		},
		host: host,
		reconcileBVMS: []store.BrowserVM{
			{ID: "bvm-1", AccountID: 42, Status: "error", HostID: &hostID, VMIP: &vmIP},
		},
	}
	agentCli := agentclient.New("test-token")
	agentCli.SetHTTPClient(httpClient)
	srv := &Server{store: ms, agentClient: agentCli}

	n, err := srv.reconcileBrowserVMCandidatesOnce(context.Background())
	if err != nil {
		t.Fatalf("reconcile candidates: %v", err)
	}
	if n != 1 {
		t.Fatalf("reconciled count = %d, want 1", n)
	}
	if !ms.activateCalled {
		t.Fatal("candidate reconcile must activate ready placement")
	}
	if ms.bvm.Status != "running" {
		t.Fatalf("expected status running, got %q", ms.bvm.Status)
	}
}
