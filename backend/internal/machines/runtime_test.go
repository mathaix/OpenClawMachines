package machines

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
	"github.com/mathaix/openclawmachines/backend/internal/fleet"
	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/routing"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

// mockStore implements store.Store with configurable behavior for the methods
// used by RuntimeService. Unused methods panic with "not implemented".
type mockStore struct {
	// Tracking calls
	deleteMachineCalled    bool
	deleteMachineID        string
	setMachineTokensCalled bool
	setMachineTokensID     string

	// GetAccount behavior
	getAccountResult *store.Account
	getAccountErr    error

	// DeleteMachine behavior
	deleteMachineErr error

	// ListSecretsWithValues behavior
	listSecretsResult []store.Secret
	listSecretsErr    error

	// ListMachineCredentialsWithValues behavior
	listMachineCredsResult []store.Credential

	// ListCustomProviders behavior
	listCustomProvidersResult []store.CustomProvider

	// ListAccountMembers behavior
	listAccountMembersResult []store.AccountMember

	// GetUser behavior
	getUserResult *store.User

	// ListMachineCapabilities behavior
	listMachineCapabilitiesResult []store.MachineCapability

	// Operation tracking
	createOperationCalled   int
	activeOperation         *store.MachineOperation
	createOperationErr      error
	completeOperationCalled bool
	completeOperationState  string
	completeOperationErrMsg *string

	// Placement mock fields
	softStopCalled     bool
	softStopMachineID  string
	softStopHostID     int
	softStopVCPUs      int
	softStopMemoryMB   int
	reAllocateCalled   bool
	reAllocateErr      error
	getHostResult      *store.Host
	getHostErr         error
	releaseCalled      bool
	unassignCalled     bool
	placeMachineResult *store.Host
	placeVMIP          string
	placeErr           error

	// GetMachine override for PlacementService.Release
	getMachineResult *store.Machine

	// Per-method call tracking
	clearTunnelCalled bool
	lastStatusUpdate  string

	// Browser VM pairing behavior (used by auto-unpair tests)
	getBrowserVMResult *store.BrowserVM
	getBrowserVMErr    error
	unpairBrowserCalls []string
	unpairBrowserErr   error

	// ProviderCatalog behavior
	listProviderCatalogByCategoryResult []store.ProviderCatalogEntry
}

type mockTunnelManager struct {
	createCalled bool
	createSlug   string
}

func (m *mockTunnelManager) CreateVMTunnel(_ context.Context, slug string) (string, string, error) {
	m.createCalled = true
	m.createSlug = slug
	return "new-tunnel-id", "new-tunnel-token", nil
}

func (m *mockTunnelManager) ConfigureVMTunnel(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockTunnelManager) CreateDNSRoute(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockTunnelManager) DeleteTunnelAndDNS(_ context.Context, _ string, _ ...string) error {
	return nil
}

// --- Methods used by RuntimeService ---

func (m *mockStore) GetAccount(_ context.Context, id int) (*store.Account, error) {
	if m.getAccountErr != nil {
		return nil, m.getAccountErr
	}
	if m.getAccountResult != nil {
		return m.getAccountResult, nil
	}
	return &store.Account{ID: id, Slug: "test-account"}, nil
}

func (m *mockStore) DeleteMachine(_ context.Context, id string) error {
	m.deleteMachineCalled = true
	m.deleteMachineID = id
	return m.deleteMachineErr
}

func (m *mockStore) SetMachineTokens(_ context.Context, machineID, gatewayToken, proxyToken string) error {
	m.setMachineTokensCalled = true
	m.setMachineTokensID = machineID
	return nil
}

func (m *mockStore) ListSecretsWithValues(_ context.Context, _ string) ([]store.Secret, error) {
	return m.listSecretsResult, m.listSecretsErr
}

func (m *mockStore) ListMachineCredentialsWithValues(_ context.Context, _ string) ([]store.Credential, error) {
	return m.listMachineCredsResult, nil
}

func (m *mockStore) ListCustomProviders(_ context.Context, _ int) ([]store.CustomProvider, error) {
	return m.listCustomProvidersResult, nil
}

func (m *mockStore) ListAccountMembers(_ context.Context, _ int) ([]store.AccountMember, error) {
	return m.listAccountMembersResult, nil
}

func (m *mockStore) GetUser(_ context.Context, id int) (*store.User, error) {
	if m.getUserResult != nil {
		return m.getUserResult, nil
	}
	return &store.User{ID: id, Email: "test@example.com"}, nil
}

func (m *mockStore) ListMachineCapabilities(_ context.Context, _ string) ([]store.MachineCapability, error) {
	return m.listMachineCapabilitiesResult, nil
}

func (m *mockStore) UpdateMachineVersion(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockStore) UpdateMachineTunnel(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockStore) ClearMachineTunnel(_ context.Context, _ string) error {
	m.clearTunnelCalled = true
	return nil
}

func (m *mockStore) UpdateMachineStatus(_ context.Context, _ string, status string, _ *string) error {
	m.lastStatusUpdate = status
	return nil
}

func (m *mockStore) GetMachine(_ context.Context, id string) (*store.Machine, error) {
	if m.getMachineResult != nil {
		return m.getMachineResult, nil
	}
	return &store.Machine{ID: id, Slug: "test-machine", AccountID: 1}, nil
}

// --- Operation methods ---

func (m *mockStore) CreateMachineOperation(_ context.Context, op *store.MachineOperation) error {
	m.createOperationCalled++
	if m.createOperationErr != nil {
		return m.createOperationErr
	}
	op.ID = "op-test-001"
	op.CreatedAt = time.Now()
	return nil
}

func (m *mockStore) GetActiveMachineOperation(_ context.Context, _ string) (*store.MachineOperation, error) {
	if m.activeOperation != nil {
		return m.activeOperation, nil
	}
	return nil, fmt.Errorf("no active operation")
}

func (m *mockStore) CompleteMachineOperation(_ context.Context, _ string, state string, errMsg *string) error {
	m.completeOperationCalled = true
	m.completeOperationState = state
	m.completeOperationErrMsg = errMsg
	return nil
}

func (m *mockStore) ExpireStaleOperations(_ context.Context, _ time.Duration) (int, error) {
	return 0, nil
}

// --- CAS method ---

func (m *mockStore) UpdateMachineStatusCAS(_ context.Context, _, _, _ string, _ *string) (bool, error) {
	return true, nil
}
func (m *mockStore) FindStuckMachines(_ context.Context, _ time.Duration) ([]store.Machine, error) {
	return nil, nil
}

// --- Methods used by PlacementService (via PlacementStore) ---

func (m *mockStore) SoftStopMachine(_ context.Context, machineID string, hostID, vcpus, memoryMB, diskMB int) error {
	m.softStopCalled = true
	m.softStopMachineID = machineID
	m.softStopHostID = hostID
	m.softStopVCPUs = vcpus
	m.softStopMemoryMB = memoryMB
	return nil
}

func (m *mockStore) ReAllocateHostCapacity(_ context.Context, _ string, _, _, _, _ int) (string, error) {
	m.reAllocateCalled = true
	if m.reAllocateErr != nil {
		return "", m.reAllocateErr
	}
	if m.getMachineResult != nil && m.getMachineResult.VMIP != nil {
		return *m.getMachineResult.VMIP, nil
	}
	return "192.168.100.42", nil
}

func (m *mockStore) GetHost(_ context.Context, id int) (*store.Host, error) {
	if m.getHostResult != nil {
		return m.getHostResult, nil
	}
	if m.getHostErr != nil {
		return nil, m.getHostErr
	}
	return &store.Host{ID: id}, nil
}

func (m *mockStore) PlaceMachineOnHost(_ context.Context, _ string, _, _ int, _, _ string) (*store.Host, string, error) {
	if m.placeErr != nil {
		return nil, "", m.placeErr
	}
	return m.placeMachineResult, m.placeVMIP, nil
}

func (m *mockStore) PlaceMachineOnSpecificHost(_ context.Context, _ string, _ int, _, _ int) (*store.Host, string, error) {
	if m.placeErr != nil {
		return nil, "", m.placeErr
	}
	return m.placeMachineResult, m.placeVMIP, nil
}

func (m *mockStore) ReleaseHostCapacity(_ context.Context, _, _, _ int) error {
	m.releaseCalled = true
	return nil
}

func (m *mockStore) UnassignMachineFromHost(_ context.Context, _ string) error {
	m.unassignCalled = true
	return nil
}

// --- All remaining Store interface methods (unused, panic on call) ---

func (m *mockStore) CreateUser(context.Context, *store.User) error {
	panic("not implemented")
}
func (m *mockStore) GetUserByEmail(context.Context, string) (*store.User, error) {
	panic("not implemented")
}
func (m *mockStore) GetUserByProvider(context.Context, string, string) (*store.User, error) {
	panic("not implemented")
}
func (m *mockStore) GetUserByCfSub(context.Context, string) (*store.User, error) {
	panic("not implemented")
}
func (m *mockStore) GetUserByIdentity(context.Context, string, string) (*store.User, error) {
	panic("not implemented")
}
func (m *mockStore) UpdateUserProvider(context.Context, int, string, string, *string) error {
	panic("not implemented")
}
func (m *mockStore) UpdateUserCfSub(context.Context, int, string) error {
	panic("not implemented")
}
func (m *mockStore) CreateAccount(context.Context, *store.Account) error {
	panic("not implemented")
}
func (m *mockStore) CreateAccountWithOwner(context.Context, *store.Account, int) error {
	panic("not implemented")
}
func (m *mockStore) GetAccountBySlug(context.Context, string) (*store.Account, error) {
	panic("not implemented")
}
func (m *mockStore) ListAccountsByUser(context.Context, int) ([]store.Account, error) {
	panic("not implemented")
}
func (m *mockStore) AddAccountMember(context.Context, *store.AccountMember) error {
	panic("not implemented")
}
func (m *mockStore) RemoveAccountMember(context.Context, int, int) error {
	panic("not implemented")
}
func (m *mockStore) GetAccountMember(context.Context, int, int) (*store.AccountMember, error) {
	panic("not implemented")
}
func (m *mockStore) UpdateAccountMemberRole(context.Context, int, int, string) error {
	panic("not implemented")
}
func (m *mockStore) UpdateAccountName(context.Context, int, string) error {
	panic("not implemented")
}
func (m *mockStore) CreateMachine(context.Context, *store.Machine) error {
	panic("not implemented")
}
func (m *mockStore) GetMachineBySlug(context.Context, string) (*store.Machine, error) {
	panic("not implemented")
}
func (m *mockStore) ListMachinesByAccount(context.Context, int) ([]store.Machine, error) {
	panic("not implemented")
}
func (m *mockStore) ListMachinesByHost(context.Context, int) ([]store.Machine, error) {
	panic("not implemented")
}
func (m *mockStore) ListAllMachines(context.Context) ([]store.Machine, error) {
	panic("not implemented")
}
func (m *mockStore) UpdateMachine(context.Context, *store.Machine) error {
	panic("not implemented")
}
func (m *mockStore) AssignMachineToHost(context.Context, string, int, string) error {
	panic("not implemented")
}
func (m *mockStore) ListRunningRoutes(context.Context) ([]store.RouteData, error) {
	panic("not implemented")
}
func (m *mockStore) CreateHost(context.Context, *store.Host) error {
	panic("not implemented")
}
func (m *mockStore) ListHosts(context.Context) ([]store.Host, error) {
	panic("not implemented")
}
func (m *mockStore) FindHostWithCapacity(context.Context, int, int, string, string) (*store.Host, error) {
	panic("not implemented")
}
func (m *mockStore) ListEligibleHosts(_ context.Context, _ store.HostFilter) ([]store.Host, error) {
	if m.placeErr != nil {
		return nil, m.placeErr
	}
	if m.placeMachineResult != nil {
		return []store.Host{*m.placeMachineResult}, nil
	}
	return nil, nil
}
func (m *mockStore) PlaceOnHost(_ context.Context, _ int, _ string, _, _, _ int) (string, error) {
	if m.placeErr != nil {
		return "", m.placeErr
	}
	return m.placeVMIP, nil
}
func (m *mockStore) ReleaseHostCapacityWithDisk(_ context.Context, _, _, _, _ int) error {
	m.releaseCalled = true
	return nil
}
func (m *mockStore) AllocateHostCapacity(context.Context, int, int, int) error {
	panic("not implemented")
}
func (m *mockStore) UpdateHostStatus(context.Context, int, string) error {
	panic("not implemented")
}
func (m *mockStore) UpdateHostStatusMessage(context.Context, int, string, string) error {
	panic("not implemented")
}
func (m *mockStore) UpdateHostDetails(context.Context, *store.Host) error {
	panic("not implemented")
}
func (m *mockStore) DeleteHost(context.Context, int) error { panic("not implemented") }
func (m *mockStore) UpdateHostSourceImage(context.Context, int, string) error {
	panic("not implemented")
}
func (m *mockStore) UpdateHostVersions(context.Context, int, string, string) error {
	panic("not implemented")
}
func (m *mockStore) UpdateHostHeartbeat(_ context.Context, _ int, _ string, _ string, _ string, _ string, _ string, _ json.RawMessage) (bool, error) {
	panic("not implemented")
}
func (m *mockStore) SetMachineBudget(context.Context, string, int64) error {
	panic("not implemented")
}
func (m *mockStore) ClearMachineBudget(context.Context, string) error {
	panic("not implemented")
}
func (m *mockStore) GetOpikSpendByMachine(context.Context, int, string) (int64, error) {
	return 0, nil
}
func (m *mockStore) GetOpikUsageByMachine(context.Context, int, string, time.Time, int) ([]store.LLMUsage, error) {
	return nil, nil
}
func (m *mockStore) GetOpikUsageBreakdown(context.Context, int, string, string, time.Time) ([]store.UsageBucket, error) {
	return nil, nil
}
func (m *mockStore) SetSecret(context.Context, string, string, string) error {
	panic("not implemented")
}
func (m *mockStore) ListSecrets(context.Context, string) ([]store.Secret, error) {
	panic("not implemented")
}
func (m *mockStore) DeleteSecret(context.Context, string, string) error {
	panic("not implemented")
}
func (m *mockStore) ListMachineCredentials(context.Context, string) ([]store.Credential, error) {
	panic("not implemented")
}
func (m *mockStore) SetMachineCredential(context.Context, string, *store.Credential) error {
	panic("not implemented")
}
func (m *mockStore) DeleteMachineCredential(context.Context, string, string) error {
	panic("not implemented")
}
func (m *mockStore) GetMachineCredentialWithValue(context.Context, string, string) (*store.Credential, error) {
	panic("not implemented")
}
func (m *mockStore) UpdateCredentialStatus(context.Context, string, string, string, *string) error {
	panic("not implemented")
}
func (m *mockStore) UpdateCredentialTokens(context.Context, int, string, string, time.Time) error {
	panic("not implemented")
}
func (m *mockStore) ResolveRoute(context.Context, string, string) (*store.ResolvedRoute, error) {
	panic("not implemented")
}
func (m *mockStore) IsMachineActive(context.Context, string) (bool, error) {
	panic("not implemented")
}
func (m *mockStore) AddToWaitlist(context.Context, string, string) error {
	panic("not implemented")
}
func (m *mockStore) UpdateWaitlistSurvey(context.Context, string, json.RawMessage) error {
	panic("not implemented")
}
func (m *mockStore) ListRegistryEntries(context.Context, string) ([]store.RegistryEntry, error) {
	panic("not implemented")
}
func (m *mockStore) ListRegistryEntriesAdmin(context.Context, string) ([]store.RegistryEntry, error) {
	panic("not implemented")
}
func (m *mockStore) GetRegistryEntry(context.Context, string) (*store.RegistryEntry, error) {
	panic("not implemented")
}
func (m *mockStore) CreateRegistryEntry(context.Context, *store.RegistryEntry) error {
	panic("not implemented")
}
func (m *mockStore) UpdateRegistryEntry(context.Context, *store.RegistryEntry) error {
	panic("not implemented")
}
func (m *mockStore) DeleteRegistryEntry(context.Context, string) error {
	panic("not implemented")
}
func (m *mockStore) EnableMachineCapability(context.Context, string, string, *string) error {
	panic("not implemented")
}
func (m *mockStore) UpdateMachineCapabilityOverrides(context.Context, string, string, json.RawMessage) error {
	panic("not implemented")
}
func (m *mockStore) DisableMachineCapability(context.Context, string, string) error {
	panic("not implemented")
}
func (m *mockStore) GetMachineConfig(context.Context, string) (*store.MachineConfigRecord, error) {
	return nil, fmt.Errorf("no config")
}
func (m *mockStore) SetMachineIdentity(context.Context, string, *string, *string) error {
	panic("not implemented")
}
func (m *mockStore) SetMachineAssembledConfig(context.Context, string, json.RawMessage, int) error {
	return nil
}
func (m *mockStore) SetMachinePreferredModel(context.Context, string, string) error {
	panic("not implemented")
}
func (m *mockStore) GetMachinePreferredModel(context.Context, string) (string, error) {
	return "", nil
}
func (m *mockStore) CreateCustomProvider(context.Context, *store.CustomProvider) error {
	panic("not implemented")
}
func (m *mockStore) DeleteCustomProvider(context.Context, int, string) error {
	panic("not implemented")
}
func (m *mockStore) GetCustomProvider(context.Context, int, string) (*store.CustomProvider, error) {
	panic("not implemented")
}
func (m *mockStore) ListStaleHosts(context.Context, time.Duration) ([]store.Host, error) {
	panic("not implemented")
}
func (m *mockStore) ListUnreachableHosts(context.Context) ([]store.Host, error) {
	panic("not implemented")
}
func (m *mockStore) SetHostMaintenanceMode(context.Context, int, bool) error {
	return nil
}
func (m *mockStore) ReconcileHostCapacityVCPUs(context.Context, int) (int, error) {
	panic("not implemented")
}
func (m *mockStore) MarkMachinesOnHostError(context.Context, int, string) ([]string, error) {
	panic("not implemented")
}
func (m *mockStore) ReservePlacement(_ context.Context, machineID string, hostID int, vmIP, _ string) (*store.Placement, error) {
	return &store.Placement{ID: "pl-test-001", MachineID: machineID, HostID: hostID, VMIP: vmIP, State: "reserved"}, nil
}
func (m *mockStore) ActivatePlacement(_ context.Context, _ string) (bool, error) {
	return true, nil
}
func (m *mockStore) ReleasePlacement(_ context.Context, _ string) error {
	return nil
}
func (m *mockStore) GetActivePlacement(_ context.Context, _ string) (*store.Placement, error) {
	return nil, fmt.Errorf("no active placement")
}
func (m *mockStore) ListActivePlacementsByHost(_ context.Context, _ int) ([]store.Placement, error) {
	return nil, nil
}
func (m *mockStore) ResolveCapacityPolicy(_ context.Context, _ int, _ string) (*store.CapacityPolicy, error) {
	return nil, fmt.Errorf("no policy")
}
func (m *mockStore) UpdateMachineHomeHost(_ context.Context, _ string, _ *int) error {
	return nil
}
func (m *mockStore) ReleaseMigrationSource(_ context.Context, _ string, _ int, _, _ int, _ bool) error {
	return nil
}

// --- Enrollment methods (satisfy store.Store interface) ---

func (m *mockStore) CreateEnrollmentToken(context.Context, *store.EnrollmentToken) error {
	panic("not implemented")
}
func (m *mockStore) GetEnrollmentToken(context.Context, string) (*store.EnrollmentToken, error) {
	panic("not implemented")
}
func (m *mockStore) ListEnrollmentTokens(context.Context) ([]store.EnrollmentToken, error) {
	panic("not implemented")
}
func (m *mockStore) MarkEnrollmentTokenUsed(context.Context, string, int) error {
	panic("not implemented")
}
func (m *mockStore) CreateRegisteredHost(context.Context, *store.Host) error {
	panic("not implemented")
}
func (m *mockStore) UpdateHostAgentEndpoint(context.Context, int, string, string) error {
	panic("not implemented")
}
func (m *mockStore) GetHostByAgentToken(context.Context, string) (*store.Host, error) {
	panic("not implemented")
}
func (m *mockStore) UpdateHostProviderMetadata(context.Context, int, json.RawMessage) error {
	panic("not implemented")
}
func (m *mockStore) CreateBackupRecord(context.Context, *store.BackupRecord) error {
	panic("not implemented")
}
func (m *mockStore) ListBackupRecords(_ context.Context, _ string) ([]store.BackupRecord, error) {
	return nil, nil
}
func (m *mockStore) GetBackupRecord(context.Context, int) (*store.BackupRecord, error) {
	panic("not implemented")
}
func (m *mockStore) DeleteBackupRecord(context.Context, int) error {
	panic("not implemented")
}
func (m *mockStore) DeleteAllBackupRecords(context.Context, string) error {
	panic("not implemented")
}
func (m *mockStore) EnableBackups(context.Context, string, []byte) error {
	panic("not implemented")
}
func (m *mockStore) DisableBackups(context.Context, string) error {
	panic("not implemented")
}
func (m *mockStore) ListMachinesWithoutBackupKey(context.Context) ([]string, error) {
	return nil, nil
}

// --- MachineAgentRepo stubs ---

func (m *mockStore) CreateMachineAgent(_ context.Context, _ *store.MachineAgent) error {
	panic("not implemented")
}
func (m *mockStore) ListMachineAgents(_ context.Context, _ string) ([]store.MachineAgent, error) {
	return nil, nil
}
func (m *mockStore) GetMachineAgent(_ context.Context, _, _ string) (*store.MachineAgent, error) {
	panic("not implemented")
}
func (m *mockStore) UpdateMachineAgent(_ context.Context, _ *store.MachineAgent) error {
	panic("not implemented")
}
func (m *mockStore) DeleteMachineAgent(_ context.Context, _, _ string) error {
	panic("not implemented")
}
func (m *mockStore) DemoteDefaultAgents(_ context.Context, _ string) error {
	panic("not implemented")
}

// --- ArtifactReleaseRepo stubs ---

func (m *mockStore) CreateArtifactRelease(_ context.Context, _ *store.ArtifactRelease) error {
	return fmt.Errorf("CreateArtifactRelease not implemented in mock")
}
func (m *mockStore) GetArtifactRelease(_ context.Context, _, _ string) (*store.ArtifactRelease, error) {
	return nil, fmt.Errorf("GetArtifactRelease not implemented in mock")
}
func (m *mockStore) ListArtifactReleases(_ context.Context, _, _ string, _ int) ([]store.ArtifactRelease, error) {
	return nil, nil
}
func (m *mockStore) GetLatestArtifactRelease(_ context.Context, _, _ string) (*store.ArtifactRelease, error) {
	return nil, fmt.Errorf("no release")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// --- Tests ---

func TestStart_GeneratesTokensWhenMissing(t *testing.T) {
	ms := &mockStore{
		placeMachineResult: &store.Host{ID: 1},
		placeVMIP:          "10.0.0.2",
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	// Machine with nil tokens — should trigger token generation
	machine := &store.Machine{
		ID:           "m-test-002",
		Slug:         "test-machine-2",
		VCPUs:        2,
		MemoryMB:     2048,
		GatewayToken: nil,
		ProxyToken:   nil,
	}

	_, _, err := rs.Start(context.Background(), 1, machine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !ms.setMachineTokensCalled {
		t.Fatal("SetMachineTokens should have been called when tokens are nil")
	}
	if ms.setMachineTokensID != "m-test-002" {
		t.Fatalf("SetMachineTokens called with wrong machine ID: got %s, want m-test-002", ms.setMachineTokensID)
	}

	// Verify tokens were set on the machine struct
	if machine.GatewayToken == nil {
		t.Fatal("GatewayToken should have been set on machine")
	}
	if machine.ProxyToken == nil {
		t.Fatal("ProxyToken should have been set on machine")
	}
	if len(*machine.GatewayToken) != 32 { // 16 bytes = 32 hex chars
		t.Fatalf("GatewayToken should be 32 hex chars, got %d", len(*machine.GatewayToken))
	}
	if len(*machine.ProxyToken) != 32 {
		t.Fatalf("ProxyToken should be 32 hex chars, got %d", len(*machine.ProxyToken))
	}
}

func TestStart_SkipsTokenGenerationWhenPresent(t *testing.T) {
	ms := &mockStore{
		placeMachineResult: &store.Host{ID: 1},
		placeVMIP:          "10.0.0.2",
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	gw := "existing-gateway-token"
	px := "existing-proxy-token"
	machine := &store.Machine{
		ID:           "m-test-003",
		Slug:         "test-machine-3",
		VCPUs:        2,
		MemoryMB:     2048,
		GatewayToken: &gw,
		ProxyToken:   &px,
	}

	_, _, err := rs.Start(context.Background(), 1, machine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ms.setMachineTokensCalled {
		t.Fatal("SetMachineTokens should NOT be called when tokens are already present")
	}
}

func TestStart_RestartSetsStartingStatus(t *testing.T) {
	hostID := 42
	vmIP := "10.0.0.5"
	now := time.Now()
	ms := &mockStore{
		getHostResult: &store.Host{
			ID:            hostID,
			Status:        "ready",
			LastHeartbeat: &now,
		},
		getMachineResult: &store.Machine{
			ID:        "m-restart-status",
			Slug:      "test-restart",
			AccountID: 1,
			HostID:    &hostID,
			VMIP:      &vmIP,
			VCPUs:     2,
			MemoryMB:  2048,
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})
	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	_, _, err := rs.Start(context.Background(), 1, ms.getMachineResult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ms.lastStatusUpdate != "starting" {
		t.Fatalf("expected lastStatusUpdate to be 'starting', got %q (restart should set 'starting', not 'provisioning')", ms.lastStatusUpdate)
	}
}

func TestStop_SucceedsWithNilKVStore(t *testing.T) {
	ms := &mockStore{}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	// Create service with nil kvStore — DeleteRouteFromKV should handle nil gracefully.
	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	machine := &store.Machine{
		ID:        "m-test-010",
		Slug:      "test-stop",
		AccountID: 1,
		HostID:    nil, // no host — skip agent call
		VCPUs:     2,
		MemoryMB:  2048,
	}

	err := rs.Stop(context.Background(), machine)
	if err != nil {
		t.Fatalf("expected no error with nil kvStore, got: %v", err)
	}
}

func TestStop_SoftReleasesCapacity(t *testing.T) {
	hostID := 5
	ms := &mockStore{
		getMachineResult: &store.Machine{
			ID:        "m-test-011",
			Slug:      "test-stop-release",
			AccountID: 1,
			HostID:    &hostID,
			VCPUs:     2,
			MemoryMB:  2048,
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	machine := ms.getMachineResult

	err := rs.Stop(context.Background(), machine)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !ms.softStopCalled {
		t.Fatal("SoftStopMachine should have been called via SoftReleaseMachine")
	}
	if ms.softStopMachineID != "m-test-011" {
		t.Fatalf("expected machine_id m-test-011, got %s", ms.softStopMachineID)
	}
	if ms.softStopHostID != 5 {
		t.Fatalf("expected host_id 5, got %d", ms.softStopHostID)
	}
}

func TestDelete_DeletesMachineRecord(t *testing.T) {
	ms := &mockStore{
		getAccountResult: &store.Account{ID: 1, Slug: "test-account"},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	// With nil kvStore, Delete should handle nil gracefully and proceed to DeleteMachine.
	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	machine := &store.Machine{
		ID:        "m-test-020",
		Slug:      "test-delete",
		AccountID: 1,
		HostID:    nil, // no host — skip agent/release
		TunnelID:  nil, // no tunnel — skip tunnel cleanup
	}

	err := rs.Delete(context.Background(), machine)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !ms.deleteMachineCalled {
		t.Fatal("DeleteMachine should have been called")
	}
	if ms.deleteMachineID != "m-test-020" {
		t.Fatalf("expected machine ID m-test-020, got %s", ms.deleteMachineID)
	}
}

func TestDelete_ReleasesHostCapacity(t *testing.T) {
	hostID := 3
	ms := &mockStore{
		getMachineResult: &store.Machine{
			ID:        "m-test-021",
			Slug:      "test-delete-host",
			AccountID: 1,
			HostID:    &hostID,
			VCPUs:     2,
			MemoryMB:  2048,
			TunnelID:  nil,
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	machine := ms.getMachineResult

	err := rs.Delete(context.Background(), machine)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !ms.releaseCalled {
		t.Fatal("ReleaseHostCapacity should have been called")
	}
	if !ms.unassignCalled {
		t.Fatal("UnassignMachineFromHost should have been called")
	}
}

func TestStart_RejectsDuplicateOperation(t *testing.T) {
	ms := &mockStore{
		activeOperation: &store.MachineOperation{
			ID:        "op-existing",
			MachineID: "m-test-dup",
			Kind:      "start",
			State:     "in_progress",
		},
		placeMachineResult: &store.Host{ID: 1},
		placeVMIP:          "10.0.0.2",
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	machine := &store.Machine{
		ID:       "m-test-dup",
		Slug:     "test-dup",
		VCPUs:    2,
		MemoryMB: 2048,
	}

	_, _, err := rs.Start(context.Background(), 1, machine)
	if err == nil {
		t.Fatal("expected error when duplicate operation exists")
	}
	if !strings.Contains(err.Error(), "in-flight") {
		t.Fatalf("expected 'in-flight' in error, got: %v", err)
	}
}

func TestStartWithOperation_ReusesExistingLock(t *testing.T) {
	ms := &mockStore{
		activeOperation: &store.MachineOperation{
			ID:        "op-migrate",
			MachineID: "m-test-reuse",
			Kind:      "migrate",
			State:     "in_progress",
		},
		placeMachineResult: &store.Host{ID: 7},
		placeVMIP:          "10.0.0.7",
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	machine := &store.Machine{
		ID:       "m-test-reuse",
		Slug:     "test-reuse",
		VCPUs:    2,
		MemoryMB: 2048,
	}

	host, vmIP, err := rs.StartWithOperation(context.Background(), 1, machine, "op-migrate")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if host == nil || host.ID != 7 {
		t.Fatalf("expected host 7, got %#v", host)
	}
	if vmIP != "10.0.0.7" {
		t.Fatalf("expected vmIP 10.0.0.7, got %q", vmIP)
	}
	if ms.createOperationCalled != 0 {
		t.Fatalf("expected no new operation, got %d creates", ms.createOperationCalled)
	}
	if ms.completeOperationCalled {
		t.Fatal("StartWithOperation should not complete the caller-owned operation")
	}
}

func TestStartWithOperation_RejectsWrongActiveOperation(t *testing.T) {
	ms := &mockStore{
		activeOperation: &store.MachineOperation{
			ID:        "op-other",
			MachineID: "m-test-reuse",
			Kind:      "stop",
			State:     "in_progress",
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	machine := &store.Machine{
		ID:       "m-test-reuse",
		Slug:     "test-reuse",
		VCPUs:    2,
		MemoryMB: 2048,
	}

	_, _, err := rs.StartWithOperation(context.Background(), 1, machine, "op-migrate")
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	if !strings.Contains(err.Error(), "expected op-migrate") {
		t.Fatalf("expected operation mismatch in error, got: %v", err)
	}
	if ms.createOperationCalled != 0 {
		t.Fatalf("expected no new operation, got %d creates", ms.createOperationCalled)
	}
}

func TestStop_IdempotentOnDuplicate(t *testing.T) {
	ms := &mockStore{
		activeOperation: &store.MachineOperation{
			ID:        "op-existing",
			MachineID: "m-test-stop-dup",
			Kind:      "stop",
			State:     "in_progress",
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	machine := &store.Machine{
		ID:       "m-test-stop-dup",
		Slug:     "test-stop-dup",
		VCPUs:    2,
		MemoryMB: 2048,
	}

	err := rs.Stop(context.Background(), machine)
	if err != nil {
		t.Fatalf("expected no error for idempotent stop, got: %v", err)
	}
}

func TestStopForUpdate_SkipsTunnelAndKVCleanup(t *testing.T) {
	hostID := 7
	tunnelID := "tunnel-abc"
	ms := &mockStore{
		getMachineResult: &store.Machine{
			ID:        "m-test-update",
			Slug:      "test-update",
			AccountID: 1,
			HostID:    &hostID,
			VCPUs:     2,
			MemoryMB:  2048,
			TunnelID:  &tunnelID,
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	// agentClient is nil — StopVM call is skipped (guarded by rs.agentClient != nil)
	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	machine := ms.getMachineResult

	err := rs.StopForUpdate(context.Background(), machine)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Capacity should have been soft-released
	if !ms.softStopCalled {
		t.Fatal("SoftStopMachine should have been called via placement.Release(ReleaseSoft)")
	}

	// Tunnel cleanup must NOT happen — machine returns to same host after update
	if ms.clearTunnelCalled {
		t.Fatal("ClearMachineTunnel should NOT be called by StopForUpdate")
	}

	// Status must be set to "stopped"
	if ms.lastStatusUpdate != "stopped" {
		t.Fatalf("expected lastStatusUpdate to be 'stopped', got %q", ms.lastStatusUpdate)
	}
}

func TestUpgradeWithOperation_RecreatesViaAgentUpgradeEndpoint(t *testing.T) {
	hostID := 7
	vmIP := "10.0.0.7"
	externalIP := "127.0.0.1"
	gatewayToken := "gateway-token"
	proxyToken := "proxy-token"
	signingKey := "signing-key"
	hostname := "m-test.openclawmachines.com"
	lastHeartbeat := time.Now()

	ms := &mockStore{
		activeOperation: &store.MachineOperation{
			ID:        "op-upgrade",
			MachineID: "m-test-upgrade",
			Kind:      "openclaw_update",
			State:     "in_progress",
		},
		getHostResult: &store.Host{
			ID:            hostID,
			Status:        "ready",
			ExternalIP:    &externalIP,
			LastHeartbeat: &lastHeartbeat,
		},
	}

	agent := agentclient.New("test-token")
	agent.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost || req.URL.Path != "/vms/m-test-upgrade/upgrade" {
				return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
			}
			body, _ := io.ReadAll(req.Body)
			payload := string(body)
			if !strings.Contains(payload, `"machine_id":"m-test-upgrade"`) {
				t.Fatalf("expected machine_id in payload, got %s", payload)
			}
			if !strings.Contains(payload, `"vm_ip":"10.0.0.7"`) {
				t.Fatalf("expected vm_ip in payload, got %s", payload)
			}
			return &http.Response{
				StatusCode: http.StatusAccepted,
				Body:       io.NopCloser(strings.NewReader(`{"machine_id":"m-test-upgrade","status":"upgrading","vm_ip":"10.0.0.7"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	rs := NewRuntimeService(ms, nil, agent, nil, nil, RuntimeConfig{}, nil)

	machine := &store.Machine{
		ID:             "m-test-upgrade",
		Slug:           "test-upgrade",
		AccountID:      1,
		HostID:         &hostID,
		VMIP:           &vmIP,
		VCPUs:          2,
		MemoryMB:       2048,
		GatewayToken:   &gatewayToken,
		ProxyToken:     &proxyToken,
		SigningKey:     &signingKey,
		TunnelHostname: &hostname,
		TunnelToken:    "tunnel-token",
	}

	host, gotIP, err := rs.UpgradeWithOperation(context.Background(), 1, machine, "op-upgrade")
	if err != nil {
		t.Fatalf("UpgradeWithOperation returned error: %v", err)
	}
	if host == nil || host.ID != hostID {
		t.Fatalf("expected host %d, got %#v", hostID, host)
	}
	if gotIP != vmIP {
		t.Fatalf("expected vmIP %s, got %s", vmIP, gotIP)
	}
	if ms.lastStatusUpdate != "starting" {
		t.Fatalf("expected machine status to be set to starting, got %q", ms.lastStatusUpdate)
	}
	if ms.clearTunnelCalled {
		t.Fatal("UpgradeWithOperation should not clear machine tunnel state")
	}
}

func TestUpgradeWithOperation_RefreshesTransientTunnelToken(t *testing.T) {
	hostID := 7
	vmIP := "10.0.0.7"
	externalIP := "127.0.0.1"
	gatewayToken := "gateway-token"
	proxyToken := "proxy-token"
	lastHeartbeat := time.Now()

	ms := &mockStore{
		activeOperation: &store.MachineOperation{
			ID:        "op-upgrade",
			MachineID: "m-test-upgrade",
			Kind:      "rootfs_update",
			State:     "in_progress",
		},
		getHostResult: &store.Host{
			ID:            hostID,
			Status:        "ready",
			ExternalIP:    &externalIP,
			LastHeartbeat: &lastHeartbeat,
		},
	}

	agent := agentclient.New("test-token")
	agent.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			payload := string(body)
			if !strings.Contains(payload, `"tunnel_token":"new-tunnel-token"`) {
				t.Fatalf("expected refreshed tunnel_token in payload, got %s", payload)
			}
			if !strings.Contains(payload, `"vm_hostname":"m-test-upgrade.openclawmachines.com"`) {
				t.Fatalf("expected refreshed vm_hostname in payload, got %s", payload)
			}
			return &http.Response{
				StatusCode: http.StatusAccepted,
				Body:       io.NopCloser(strings.NewReader(`{"machine_id":"m-test-upgrade","status":"upgrading","vm_ip":"10.0.0.7"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	tunnels := &mockTunnelManager{}
	routeSvc := routing.New(tunnels, nil, ms)
	rs := NewRuntimeService(ms, nil, agent, nil, nil, RuntimeConfig{}, routeSvc)

	machine := &store.Machine{
		ID:           "m-test-upgrade",
		Slug:         "test-upgrade",
		AccountID:    1,
		HostID:       &hostID,
		VMIP:         &vmIP,
		VCPUs:        2,
		MemoryMB:     2048,
		GatewayToken: &gatewayToken,
		ProxyToken:   &proxyToken,
	}

	if _, _, err := rs.UpgradeWithOperation(context.Background(), 1, machine, "op-upgrade"); err != nil {
		t.Fatalf("UpgradeWithOperation returned error: %v", err)
	}
	if !tunnels.createCalled {
		t.Fatal("expected route setup to create a fresh tunnel")
	}
	if tunnels.createSlug != "test-upgrade" {
		t.Fatalf("created tunnel slug = %q, want test-upgrade", tunnels.createSlug)
	}
	if machine.TunnelToken != "new-tunnel-token" {
		t.Fatalf("machine tunnel token = %q, want new-tunnel-token", machine.TunnelToken)
	}
}

func TestStart_RestartCreateFailurePreservesHostAffinity(t *testing.T) {
	hostID := 7
	vmIP := "10.0.0.7"
	externalIP := "127.0.0.1"
	gatewayToken := "gateway-token"
	proxyToken := "proxy-token"
	lastHeartbeat := time.Now()
	ms := &mockStore{
		getMachineResult: &store.Machine{
			ID:           "m-test-restart-failure",
			Slug:         "test-restart-failure",
			AccountID:    1,
			HostID:       &hostID,
			VMIP:         &vmIP,
			VCPUs:        2,
			MemoryMB:     2048,
			GatewayToken: &gatewayToken,
			ProxyToken:   &proxyToken,
		},
		getHostResult: &store.Host{ID: hostID, Status: "ready", ExternalIP: &externalIP, LastHeartbeat: &lastHeartbeat},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	agent := agentclient.New("test-token")
	agent.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case req.Method == http.MethodDelete && strings.Contains(req.URL.Path, "/vms/"):
				return &http.Response{
					StatusCode: http.StatusNoContent,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     make(http.Header),
				}, nil
			case req.Method == http.MethodPost && req.URL.Path == "/vms":
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(strings.NewReader("create failed")),
					Header:     make(http.Header),
				}, nil
			default:
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     make(http.Header),
				}, nil
			}
		}),
	})

	rs := NewRuntimeService(ms, placementSvc, agent, nil, nil, RuntimeConfig{}, nil)
	machine := ms.getMachineResult

	_, _, err := rs.Start(context.Background(), 1, machine)
	if err == nil {
		t.Fatal("expected restart create failure")
	}
	if !ms.softStopCalled {
		t.Fatal("expected restart failure to soft-release capacity")
	}
	if ms.unassignCalled {
		t.Fatal("restart failure should not hard-unassign the machine")
	}
	if ms.clearTunnelCalled {
		t.Fatal("restart failure should not clear the existing tunnel")
	}
}

func TestStopWithOperation_ReusesExistingLock(t *testing.T) {
	hostID := 9
	ms := &mockStore{
		activeOperation: &store.MachineOperation{
			ID:        "op-migrate",
			MachineID: "m-test-stop-reuse",
			Kind:      "migrate",
			State:     "in_progress",
		},
		getMachineResult: &store.Machine{
			ID:        "m-test-stop-reuse",
			Slug:      "test-stop-reuse",
			AccountID: 1,
			HostID:    &hostID,
			VCPUs:     2,
			MemoryMB:  2048,
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)
	machine := ms.getMachineResult

	if err := rs.StopWithOperation(context.Background(), machine, "op-migrate"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ms.createOperationCalled != 0 {
		t.Fatalf("expected no new operation, got %d creates", ms.createOperationCalled)
	}
	if ms.completeOperationCalled {
		t.Fatal("StopWithOperation should not complete the caller-owned operation")
	}
	if !ms.softStopCalled {
		t.Fatal("expected soft stop during StopWithOperation")
	}
}

func TestDelete_RejectsDuringStart(t *testing.T) {
	ms := &mockStore{
		activeOperation: &store.MachineOperation{
			ID:        "op-existing",
			MachineID: "m-test-del",
			Kind:      "start",
			State:     "in_progress",
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	machine := &store.Machine{
		ID:       "m-test-del",
		Slug:     "test-del",
		VCPUs:    2,
		MemoryMB: 2048,
	}

	err := rs.Delete(context.Background(), machine)
	if err == nil {
		t.Fatal("expected error when start operation is in-flight")
	}
	if !strings.Contains(err.Error(), "in-flight start") {
		t.Fatalf("expected 'in-flight start' in error, got: %v", err)
	}
}

func TestStart_CompletesOperationOnError(t *testing.T) {
	ms := &mockStore{
		placeErr: fmt.Errorf("no hosts available"),
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	machine := &store.Machine{
		ID:       "m-test-op-fail",
		Slug:     "test-op-fail",
		VCPUs:    2,
		MemoryMB: 2048,
	}

	_, _, err := rs.Start(context.Background(), 1, machine)
	if err == nil {
		t.Fatal("expected error")
	}

	if !ms.completeOperationCalled {
		t.Fatal("CompleteMachineOperation should have been called on error")
	}
	if ms.completeOperationState != "failed" {
		t.Fatalf("expected operation state 'failed', got '%s'", ms.completeOperationState)
	}
}

func TestStart_PlacementError(t *testing.T) {
	ms := &mockStore{
		placeErr: fmt.Errorf("no hosts available"),
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	machine := &store.Machine{
		ID:       "m-test-004",
		Slug:     "test-no-capacity",
		VCPUs:    2,
		MemoryMB: 2048,
	}

	_, _, err := rs.Start(context.Background(), 1, machine)
	if err == nil {
		t.Fatal("expected error when no hosts available")
	}
	if !strings.Contains(err.Error(), "no hosts available") {
		t.Fatalf("expected 'no hosts available' in error, got: %v", err)
	}
}

func TestStart_ReturnsHostAndVMIP(t *testing.T) {
	expectedHost := &store.Host{ID: 42, VMName: "worker-42"}
	ms := &mockStore{
		placeMachineResult: expectedHost,
		placeVMIP:          "10.0.0.5",
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	machine := &store.Machine{
		ID:       "m-test-006",
		Slug:     "test-returns",
		VCPUs:    2,
		MemoryMB: 2048,
	}

	host, vmIP, err := rs.Start(context.Background(), 1, machine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With nil agentClient, Start succeeds with "placed.no.agent" path
	if host == nil {
		t.Fatal("expected non-nil host")
	} else if host.ID != 42 {
		t.Fatalf("expected host ID 42, got %d", host.ID)
	}
	if vmIP != "10.0.0.5" {
		t.Fatalf("expected vmIP 10.0.0.5, got %s", vmIP)
	}
}

// TestStart_BrowserVMAutoUnpair locks in the policy from the browser branch
// review: RuntimeService.Start must only auto-unpair a paired browser VM
// when the store authoritatively reports a host mismatch. Transient lookup
// errors used to take the same branch, which would silently drop a valid
// pairing on a single flaky DB call — and unpair failures used to be
// logged and swallowed, which would log "auto_unpair" while the pair was
// still in the DB.
func TestStart_BrowserVMAutoUnpair(t *testing.T) {
	paired := "bvm-001"
	sameHost := 42
	otherHost := 99

	cases := []struct {
		name            string
		bvm             *store.BrowserVM
		bvmErr          error
		unpairErr       error
		wantErrContains string
		wantUnpairCalls int
	}{
		{
			name:            "same host — no unpair",
			bvm:             &store.BrowserVM{ID: paired, HostID: &sameHost, Status: "running"},
			wantUnpairCalls: 0,
		},
		{
			name:            "cross host — unpair once",
			bvm:             &store.BrowserVM{ID: paired, HostID: &otherHost, Status: "running"},
			wantUnpairCalls: 1,
		},
		{
			name:            "browser VM row missing (ErrNoRows) — treat as unpair signal",
			bvm:             nil,
			bvmErr:          pgx.ErrNoRows,
			wantUnpairCalls: 1,
		},
		{
			name:            "browser VM has no host — treat as cross-host",
			bvm:             &store.BrowserVM{ID: paired, Status: "stopped"},
			wantUnpairCalls: 1,
		},
		{
			name:            "transient lookup error — surface, do not unpair",
			bvmErr:          fmt.Errorf("pg: temporary failure"),
			wantErrContains: "look up paired browser VM",
			wantUnpairCalls: 0,
		},
		{
			name:            "unpair DB failure — surface, do not silently log",
			bvm:             &store.BrowserVM{ID: paired, HostID: &otherHost, Status: "running"},
			unpairErr:       fmt.Errorf("pg: unpair write failed"),
			wantErrContains: "auto-unpair browser VM",
			wantUnpairCalls: 1,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ms := &mockStore{
				placeMachineResult: &store.Host{ID: sameHost, VMName: "worker-42"},
				placeVMIP:          "10.0.0.5",
				getBrowserVMResult: tc.bvm,
				getBrowserVMErr:    tc.bvmErr,
				unpairBrowserErr:   tc.unpairErr,
			}
			placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})
			rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

			machine := &store.Machine{
				ID:          "m-bvm-test",
				Slug:        "bvm-test",
				VCPUs:       2,
				MemoryMB:    2048,
				BrowserVMID: &paired,
			}

			_, _, err := rs.Start(context.Background(), 1, machine)
			if tc.wantErrContains == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrContains)
				}
				if !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErrContains, err)
				}
			}
			if got := len(ms.unpairBrowserCalls); got != tc.wantUnpairCalls {
				t.Fatalf("UnpairBrowserVM calls = %d, want %d (calls: %v)", got, tc.wantUnpairCalls, ms.unpairBrowserCalls)
			}
		})
	}
}

func TestStart_IncludesSearchProvidersInVMRequest(t *testing.T) {
	externalIP := "127.0.0.1"
	lastHeartbeat := time.Now()

	braveHost := "api.search.brave.com"
	braveAuth := "api_key_header"
	braveAuthHeader := "X-Subscription-Token"

	ms := &mockStore{
		placeMachineResult: &store.Host{
			ID:            1,
			Status:        "ready",
			ExternalIP:    &externalIP,
			LastHeartbeat: &lastHeartbeat,
		},
		placeVMIP: "10.0.0.2",
		listProviderCatalogByCategoryResult: []store.ProviderCatalogEntry{
			{
				ID:           "brave",
				Category:     "search",
				UpstreamHost: &braveHost,
				Scheme:       "https",
				AuthMethod:   &braveAuth,
				AuthHeader:   &braveAuthHeader,
			},
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	var capturedBody []byte
	agent := agentclient.New("test-token")
	agent.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case req.Method == http.MethodDelete && strings.Contains(req.URL.Path, "/vms/"):
				// Pre-cleanup DestroyVM call
				return &http.Response{
					StatusCode: http.StatusNoContent,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     make(http.Header),
				}, nil
			case req.Method == http.MethodPost && req.URL.Path == "/vms":
				capturedBody, _ = io.ReadAll(req.Body)
				return &http.Response{
					StatusCode: http.StatusCreated,
					Body:       io.NopCloser(strings.NewReader(`{"machine_id":"m-search-test","status":"running","vm_ip":"10.0.0.2"}`)),
					Header:     make(http.Header),
				}, nil
			default:
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     make(http.Header),
				}, nil
			}
		}),
	})

	gatewayToken := "gw-token"
	proxyToken := "px-token"
	machine := &store.Machine{
		ID:           "m-search-test",
		Slug:         "search-test",
		AccountID:    1,
		VCPUs:        2,
		MemoryMB:     2048,
		GatewayToken: &gatewayToken,
		ProxyToken:   &proxyToken,
	}

	rs := NewRuntimeService(ms, placementSvc, agent, nil, nil, RuntimeConfig{}, nil)
	_, _, err := rs.Start(context.Background(), 1, machine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(capturedBody) == 0 {
		t.Fatal("expected agent CreateVM request to be captured")
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}

	cpRaw, ok := payload["custom_providers"]
	if !ok {
		t.Fatal("expected custom_providers in request body")
	}

	var providers []map[string]interface{}
	if err := json.Unmarshal(cpRaw, &providers); err != nil {
		t.Fatalf("failed to parse custom_providers: %v", err)
	}

	// Find the "brave" entry
	var found bool
	for _, p := range providers {
		if p["name"] == "brave" {
			found = true
			if p["upstream_host"] != "api.search.brave.com" {
				t.Fatalf("expected upstream_host 'api.search.brave.com', got %v", p["upstream_host"])
			}
			if p["scheme"] != "https" {
				t.Fatalf("expected scheme 'https', got %v", p["scheme"])
			}
			if p["auth_method"] != "api_key_header" {
				t.Fatalf("expected auth_method 'api_key_header', got %v", p["auth_method"])
			}
			if p["auth_header"] != "X-Subscription-Token" {
				t.Fatalf("expected auth_header 'X-Subscription-Token', got %v", p["auth_header"])
			}
			// Search providers should not be flagged as LLM
			if p["is_llm"] != false {
				t.Fatalf("expected is_llm false, got %v", p["is_llm"])
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected 'brave' in custom_providers, got %s", string(cpRaw))
	}
}

func TestStart_SearchProviderUsesRuntimeProviderID(t *testing.T) {
	externalIP := "127.0.0.1"
	lastHeartbeat := time.Now()

	geminiHost := "generativelanguage.googleapis.com"
	geminiAuth := "query_param"
	geminiAuthHeader := "key"
	geminiRuntimeID := "gemini"

	ms := &mockStore{
		placeMachineResult: &store.Host{
			ID:            1,
			Status:        "ready",
			ExternalIP:    &externalIP,
			LastHeartbeat: &lastHeartbeat,
		},
		placeVMIP: "10.0.0.2",
		listProviderCatalogByCategoryResult: []store.ProviderCatalogEntry{
			{
				ID:                "gemini-search",
				Category:          "search",
				UpstreamHost:      &geminiHost,
				Scheme:            "https",
				AuthMethod:        &geminiAuth,
				AuthHeader:        &geminiAuthHeader,
				RuntimeProviderID: &geminiRuntimeID,
			},
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	var capturedBody []byte
	agent := agentclient.New("test-token")
	agent.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case req.Method == http.MethodDelete && strings.Contains(req.URL.Path, "/vms/"):
				return &http.Response{
					StatusCode: http.StatusNoContent,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     make(http.Header),
				}, nil
			case req.Method == http.MethodPost && req.URL.Path == "/vms":
				capturedBody, _ = io.ReadAll(req.Body)
				return &http.Response{
					StatusCode: http.StatusCreated,
					Body:       io.NopCloser(strings.NewReader(`{"machine_id":"m-remap-test","status":"running","vm_ip":"10.0.0.2"}`)),
					Header:     make(http.Header),
				}, nil
			default:
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     make(http.Header),
				}, nil
			}
		}),
	})

	gatewayToken := "gw-token"
	proxyToken := "px-token"
	machine := &store.Machine{
		ID:           "m-remap-test",
		Slug:         "remap-test",
		AccountID:    1,
		VCPUs:        2,
		MemoryMB:     2048,
		GatewayToken: &gatewayToken,
		ProxyToken:   &proxyToken,
	}

	rs := NewRuntimeService(ms, placementSvc, agent, nil, nil, RuntimeConfig{}, nil)
	_, _, err := rs.Start(context.Background(), 1, machine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(capturedBody) == 0 {
		t.Fatal("expected agent CreateVM request to be captured")
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}

	cpRaw, ok := payload["custom_providers"]
	if !ok {
		t.Fatal("expected custom_providers in request body")
	}

	var providers []map[string]interface{}
	if err := json.Unmarshal(cpRaw, &providers); err != nil {
		t.Fatalf("failed to parse custom_providers: %v", err)
	}

	// The provider name should be "gemini" (RuntimeProviderID), not "gemini-search" (catalog ID)
	var foundGemini, foundGeminiSearch bool
	for _, p := range providers {
		if p["name"] == "gemini" {
			foundGemini = true
			if p["upstream_host"] != "generativelanguage.googleapis.com" {
				t.Fatalf("expected upstream_host 'generativelanguage.googleapis.com', got %v", p["upstream_host"])
			}
			if p["auth_method"] != "query_param" {
				t.Fatalf("expected auth_method 'query_param', got %v", p["auth_method"])
			}
			if p["auth_header"] != "key" {
				t.Fatalf("expected auth_header 'key', got %v", p["auth_header"])
			}
		}
		if p["name"] == "gemini-search" {
			foundGeminiSearch = true
		}
	}
	if !foundGemini {
		t.Fatalf("expected provider with name 'gemini' (RuntimeProviderID), got %s", string(cpRaw))
	}
	if foundGeminiSearch {
		t.Fatal("provider should use RuntimeProviderID 'gemini', not catalog ID 'gemini-search'")
	}
}

// --- Plugin stubs (satisfy store.Store interface) ---

func (m *mockStore) ListPluginCatalog(_ context.Context) ([]store.PluginCatalogEntry, error) {
	return nil, nil
}
func (m *mockStore) GetPluginCatalogEntry(_ context.Context, _ string) (*store.PluginCatalogEntry, error) {
	return nil, fmt.Errorf("not found")
}
func (m *mockStore) CreatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockStore) UpdatePluginCatalogEntry(_ context.Context, _ *store.PluginCatalogEntry) error {
	return nil
}
func (m *mockStore) DeletePluginCatalogEntry(_ context.Context, _ string) error {
	return nil
}
func (m *mockStore) ListMachinePlugins(_ context.Context, _ string) ([]store.MachinePlugin, error) {
	return nil, nil
}
func (m *mockStore) ListMachinePluginsWithCatalog(_ context.Context, _ string) ([]store.MachinePluginWithCatalog, error) {
	return nil, nil
}
func (m *mockStore) EnableMachinePlugin(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockStore) DisableMachinePlugin(_ context.Context, _, _ string) error {
	return nil
}
func (m *mockStore) UpdateMachinePluginOverrides(_ context.Context, _, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockStore) UpdateMachinePluginStatus(_ context.Context, _, _, _, _ string) error {
	return nil
}

func (m *mockStore) ListModelCatalog(_ context.Context) ([]store.ModelCatalogEntry, error) {
	smart := "smart"
	balanced := "balanced"
	fast := "fast"
	nebiusQwen := "nebius/Qwen/Qwen3.5-397B-A17B"
	nebiusMinimax := "nebius/MiniMaxAI/MiniMax-M2.5"
	nebiusGPT := "nebius/openai/gpt-oss-120b"
	return []store.ModelCatalogEntry{
		{ID: "qwen/qwen3.5-397b", Label: "Smart", Source: "platform", Tier: &smart, GatewayModelID: &nebiusQwen, Provider: "nebius", Enabled: true, SortOrder: 1},
		{ID: "minimax/minimax-m2.5", Label: "Balanced", Source: "platform", Tier: &balanced, GatewayModelID: &nebiusMinimax, Provider: "nebius", Enabled: true, SortOrder: 2},
		{ID: "openai/gpt-oss-120b", Label: "Fast", Source: "platform", Tier: &fast, GatewayModelID: &nebiusGPT, Provider: "nebius", Enabled: true, SortOrder: 3},
		{ID: "anthropic/claude-sonnet-4-6", Label: "Claude Sonnet 4.6", Source: "byok", Provider: "anthropic", Enabled: true, SortOrder: 10},
		{ID: "openai-codex/gpt-5.5", Label: "GPT-5.5", Source: "subscription", Provider: "openai-codex", Enabled: true, SortOrder: 19},
		{ID: "openai-codex/gpt-5.4", Label: "GPT-5.4", Source: "subscription", Provider: "openai-codex", Enabled: true, SortOrder: 20},
	}, nil
}

func (m *mockStore) GetModelCatalogEntry(_ context.Context, modelID string) (*store.ModelCatalogEntry, error) {
	entries, _ := m.ListModelCatalog(context.Background())
	for _, e := range entries {
		if e.ID == modelID {
			return &e, nil
		}
	}
	return nil, nil
}

func (m *mockStore) ListModelPricingHistory(_ context.Context, _ string) ([]store.ModelPricingHistory, error) {
	return nil, nil
}

func (m *mockStore) InsertModelPricing(_ context.Context, _ *store.ModelPricingHistory) error {
	return nil
}

// BrowserVMRepo stubs
func (m *mockStore) CreateBrowserVM(context.Context, *store.BrowserVM) error { return nil }
func (m *mockStore) GetBrowserVM(_ context.Context, _ string) (*store.BrowserVM, error) {
	if m.getBrowserVMErr != nil {
		return nil, m.getBrowserVMErr
	}
	if m.getBrowserVMResult != nil {
		return m.getBrowserVMResult, nil
	}
	return nil, fmt.Errorf("not found")
}
func (m *mockStore) ListBrowserVMsByAccount(context.Context, int) ([]store.BrowserVM, error) {
	return nil, nil
}
func (m *mockStore) ListBrowserVMsRequiringReconciliation(context.Context) ([]store.BrowserVM, error) {
	return nil, nil
}
func (m *mockStore) UpdateBrowserVMStatus(context.Context, string, string, *string) error {
	return nil
}
func (m *mockStore) AssignBrowserVMToHost(context.Context, string, int, string) error { return nil }
func (m *mockStore) UnassignBrowserVMFromHost(context.Context, string) error          { return nil }
func (m *mockStore) DeleteBrowserVM(context.Context, string) error                    { return nil }
func (m *mockStore) PairBrowserVM(context.Context, string, string) error              { return nil }
func (m *mockStore) UnpairBrowserVM(_ context.Context, machineID string) error {
	m.unpairBrowserCalls = append(m.unpairBrowserCalls, machineID)
	return m.unpairBrowserErr
}
func (m *mockStore) GetMachineByBrowserVMID(context.Context, string) (*store.Machine, error) {
	return nil, fmt.Errorf("not found")
}
func (m *mockStore) PlaceBrowserVMOnHost(context.Context, int, string, int, int) (string, error) {
	return "", nil
}
func (m *mockStore) ActivateBrowserVMPlacement(context.Context, string) error { return nil }
func (m *mockStore) ReleaseBrowserVMPlacement(context.Context, string) error  { return nil }

// ProviderCatalogRepo stubs
func (m *mockStore) ListProviderCatalog(context.Context) ([]store.ProviderCatalogEntry, error) {
	return nil, nil
}
func (m *mockStore) ListProviderCatalogByCategory(_ context.Context, category string) ([]store.ProviderCatalogEntry, error) {
	if category == "search" && m.listProviderCatalogByCategoryResult != nil {
		return m.listProviderCatalogByCategoryResult, nil
	}
	return nil, nil
}
func (m *mockStore) GetProviderCatalogEntry(context.Context, string) (*store.ProviderCatalogEntry, error) {
	return nil, fmt.Errorf("not found")
}

// ConfigRepo stubs for new platform override methods
func (m *mockStore) SetMachineModelFallbacks(context.Context, string, []string) error {
	return nil
}
func (m *mockStore) UpdateMachinePlatformOverride(context.Context, string, string, string) error {
	return nil
}
func (m *mockStore) DeleteMachinePlatformOverride(context.Context, string, string) error {
	return nil
}

// --- Helper: build an agent that captures CreateVM request body ---

// captureAgentTransport returns a roundTripFunc that captures the CreateVM POST
// body and responds with a successful creation. It also handles the pre-cleanup
// DestroyVM DELETE call.
func captureAgentTransport(capturedBody *[]byte, machineID string) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodDelete && strings.Contains(req.URL.Path, "/vms/"):
			return &http.Response{
				StatusCode: http.StatusNoContent,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
			}, nil
		case req.Method == http.MethodPost && req.URL.Path == "/vms":
			*capturedBody, _ = io.ReadAll(req.Body)
			return &http.Response{
				StatusCode: http.StatusCreated,
				Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"machine_id":%q,"status":"running","vm_ip":"10.0.0.2"}`, machineID))),
				Header:     make(http.Header),
			}, nil
		default:
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
			}, nil
		}
	}
}

// mustEncrypt encrypts a value with the given key and panics on error.
func mustEncrypt(t *testing.T, plaintext, key string) string {
	t.Helper()
	enc, err := crypto.Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("mustEncrypt: %v", err)
	}
	return enc
}

// parseVMRequestPayload unmarshals captured body into a typed agentclient.VMRequest.
func parseVMRequestPayload(t *testing.T, body []byte) agentclient.VMRequest {
	t.Helper()
	var req agentclient.VMRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("parseVMRequestPayload: %v", err)
	}
	return req
}

// --- Phase tests for Start() refactoring safety net ---

func TestStart_SecretDecryption(t *testing.T) {
	const secretKey = "01234567890123456789012345678901" // 32 bytes

	t.Run("decrypts secrets into VM payload", func(t *testing.T) {
		externalIP := "127.0.0.1"
		lastHeartbeat := time.Now()

		ms := &mockStore{
			placeMachineResult: &store.Host{
				ID:            1,
				Status:        "ready",
				ExternalIP:    &externalIP,
				LastHeartbeat: &lastHeartbeat,
			},
			placeVMIP: "10.0.0.2",
			listSecretsResult: []store.Secret{
				{ID: 1, MachineID: "m-secret-test", Key: "DB_PASSWORD", EncryptedValue: mustEncrypt(t, "hunter2", secretKey)},
				{ID: 2, MachineID: "m-secret-test", Key: "API_TOKEN", EncryptedValue: mustEncrypt(t, "tok-abc123", secretKey)},
			},
		}
		placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

		var capturedBody []byte
		agent := agentclient.New("test-token")
		agent.SetHTTPClient(&http.Client{Transport: captureAgentTransport(&capturedBody, "m-secret-test")})

		gatewayToken := "gw-token"
		proxyToken := "px-token"
		machine := &store.Machine{
			ID:           "m-secret-test",
			Slug:         "secret-test",
			AccountID:    1,
			VCPUs:        2,
			MemoryMB:     2048,
			GatewayToken: &gatewayToken,
			ProxyToken:   &proxyToken,
		}

		rs := NewRuntimeService(ms, placementSvc, agent, nil, nil, RuntimeConfig{SecretKey: secretKey}, nil)
		_, _, err := rs.Start(context.Background(), 1, machine)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(capturedBody) == 0 {
			t.Fatal("expected agent CreateVM request to be captured")
		}
		req := parseVMRequestPayload(t, capturedBody)

		if len(req.Secrets) != 2 {
			t.Fatalf("expected 2 secrets, got %d", len(req.Secrets))
		}
		if req.Secrets["DB_PASSWORD"] != "hunter2" {
			t.Fatalf("expected DB_PASSWORD=hunter2, got %q", req.Secrets["DB_PASSWORD"])
		}
		if req.Secrets["API_TOKEN"] != "tok-abc123" {
			t.Fatalf("expected API_TOKEN=tok-abc123, got %q", req.Secrets["API_TOKEN"])
		}
	})

	t.Run("empty secrets when secretKey is empty", func(t *testing.T) {
		externalIP := "127.0.0.1"
		lastHeartbeat := time.Now()

		ms := &mockStore{
			placeMachineResult: &store.Host{
				ID:            1,
				Status:        "ready",
				ExternalIP:    &externalIP,
				LastHeartbeat: &lastHeartbeat,
			},
			placeVMIP: "10.0.0.2",
			listSecretsResult: []store.Secret{
				{ID: 1, MachineID: "m-secret-nokey", Key: "DB_PASSWORD", EncryptedValue: "encrypted-blob"},
			},
		}
		placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

		var capturedBody []byte
		agent := agentclient.New("test-token")
		agent.SetHTTPClient(&http.Client{Transport: captureAgentTransport(&capturedBody, "m-secret-nokey")})

		gatewayToken := "gw-token"
		proxyToken := "px-token"
		machine := &store.Machine{
			ID:           "m-secret-nokey",
			Slug:         "secret-nokey",
			AccountID:    1,
			VCPUs:        2,
			MemoryMB:     2048,
			GatewayToken: &gatewayToken,
			ProxyToken:   &proxyToken,
		}

		// Empty SecretKey
		rs := NewRuntimeService(ms, placementSvc, agent, nil, nil, RuntimeConfig{SecretKey: ""}, nil)
		_, _, err := rs.Start(context.Background(), 1, machine)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		req := parseVMRequestPayload(t, capturedBody)
		if len(req.Secrets) != 0 {
			t.Fatalf("expected empty secrets when secretKey is empty, got %d", len(req.Secrets))
		}
	})
}

func TestStart_CredentialClassification(t *testing.T) {
	const secretKey = "01234567890123456789012345678901"

	externalIP := "127.0.0.1"
	lastHeartbeat := time.Now()

	braveHost := "api.search.brave.com"
	braveAuth := "api_key_header"

	ms := &mockStore{
		placeMachineResult: &store.Host{
			ID:            1,
			Status:        "ready",
			ExternalIP:    &externalIP,
			LastHeartbeat: &lastHeartbeat,
		},
		placeVMIP: "10.0.0.2",
		listMachineCredsResult: []store.Credential{
			{ID: 1, Provider: "anthropic", CredentialType: "api_key", EncryptedValue: mustEncrypt(t, "sk-ant-xxx", secretKey)},
			{ID: 2, Provider: "brave", CredentialType: "api_key", EncryptedValue: mustEncrypt(t, "brave-key-xxx", secretKey)},
			{ID: 3, Provider: "discord", CredentialType: "token", EncryptedValue: mustEncrypt(t, "discord-tok-xxx", secretKey)},
		},
		// brave is in the search catalog, so it counts as LLM-like for injection
		listProviderCatalogByCategoryResult: []store.ProviderCatalogEntry{
			{
				ID:           "brave",
				Category:     "search",
				UpstreamHost: &braveHost,
				Scheme:       "https",
				AuthMethod:   &braveAuth,
			},
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	var capturedBody []byte
	agent := agentclient.New("test-token")
	agent.SetHTTPClient(&http.Client{Transport: captureAgentTransport(&capturedBody, "m-cred-class")})

	gatewayToken := "gw-token"
	proxyToken := "px-token"
	machine := &store.Machine{
		ID:           "m-cred-class",
		Slug:         "cred-class",
		AccountID:    1,
		VCPUs:        2,
		MemoryMB:     2048,
		GatewayToken: &gatewayToken,
		ProxyToken:   &proxyToken,
	}

	rs := NewRuntimeService(ms, placementSvc, agent, nil, nil, RuntimeConfig{SecretKey: secretKey}, nil)
	_, _, err := rs.Start(context.Background(), 1, machine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := parseVMRequestPayload(t, capturedBody)

	// anthropic is a known LLM provider — should be in llm_keys
	if _, ok := req.LLMKeys["anthropic"]; !ok {
		t.Fatal("expected 'anthropic' in llm_keys")
	}
	if req.LLMKeys["anthropic"].Value != "sk-ant-xxx" {
		t.Fatalf("expected anthropic value 'sk-ant-xxx', got %q", req.LLMKeys["anthropic"].Value)
	}

	// brave is a search catalog provider — should be in llm_keys (keyed by runtime ID = "brave")
	if _, ok := req.LLMKeys["brave"]; !ok {
		t.Fatal("expected 'brave' in llm_keys (search providers are injected as LLM keys)")
	}
	if req.LLMKeys["brave"].Value != "brave-key-xxx" {
		t.Fatalf("expected brave value 'brave-key-xxx', got %q", req.LLMKeys["brave"].Value)
	}

	// discord is a channel key, not an LLM provider — should NOT be in llm_keys
	if _, ok := req.LLMKeys["discord"]; ok {
		t.Fatal("expected 'discord' NOT in llm_keys (channel keys are not injected at boot)")
	}
}

func TestStart_HermesTelegramAllowedUsersInSeedEnv(t *testing.T) {
	const secretKey = "01234567890123456789012345678901"

	externalIP := "127.0.0.1"
	lastHeartbeat := time.Now()
	ms := &mockStore{
		placeMachineResult: &store.Host{
			ID:            1,
			Status:        "ready",
			ExternalIP:    &externalIP,
			LastHeartbeat: &lastHeartbeat,
		},
		placeVMIP: "10.0.0.2",
		listMachineCredsResult: []store.Credential{
			{ID: 1, Provider: "telegram", CredentialType: "token", EncryptedValue: mustEncrypt(t, "tg-token-xxx", secretKey)},
		},
		listMachineCapabilitiesResult: []store.MachineCapability{
			{
				EntryID:         "telegram",
				EntryType:       "channel",
				Enabled:         true,
				ConfigOverrides: json.RawMessage(`{"channels":{"telegram":{"allowedUsers":"12345, 67890"}}}`),
			},
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	var capturedBody []byte
	agent := agentclient.New("test-token")
	agent.SetHTTPClient(&http.Client{Transport: captureAgentTransport(&capturedBody, "m-hermes-telegram")})

	gatewayToken := "gw-token"
	proxyToken := "px-token"
	machine := &store.Machine{
		ID:           "m-hermes-telegram",
		Kind:         store.MachineKindHermes,
		Slug:         "hermes-telegram",
		AccountID:    1,
		VCPUs:        2,
		MemoryMB:     2048,
		GatewayToken: &gatewayToken,
		ProxyToken:   &proxyToken,
	}

	rs := NewRuntimeService(ms, placementSvc, agent, nil, nil, RuntimeConfig{SecretKey: secretKey}, nil)
	_, _, err := rs.Start(context.Background(), 1, machine, StartOptions{SkipPoll: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(capturedBody) == 0 {
		t.Fatal("expected agent CreateVM request to be captured")
	}

	req := parseVMRequestPayload(t, capturedBody)
	env := string(req.HermesEnv)
	if !strings.Contains(env, "TELEGRAM_BOT_TOKEN=tg-token-xxx\n") {
		t.Fatalf("Hermes env missing telegram token:\n%s", env)
	}
	if !strings.Contains(env, "TELEGRAM_ALLOWED_USERS=12345,67890\n") {
		t.Fatalf("Hermes env missing normalized allowed users:\n%s", env)
	}
}

func TestStart_CredentialRuntimeIDRemapping(t *testing.T) {
	const secretKey = "01234567890123456789012345678901"

	externalIP := "127.0.0.1"
	lastHeartbeat := time.Now()

	geminiHost := "generativelanguage.googleapis.com"
	geminiAuth := "query_param"
	geminiRuntimeID := "gemini"

	ms := &mockStore{
		placeMachineResult: &store.Host{
			ID:            1,
			Status:        "ready",
			ExternalIP:    &externalIP,
			LastHeartbeat: &lastHeartbeat,
		},
		placeVMIP: "10.0.0.2",
		listMachineCredsResult: []store.Credential{
			{ID: 1, Provider: "gemini-search", CredentialType: "api_key", EncryptedValue: mustEncrypt(t, "gemini-key-xxx", secretKey)},
		},
		listProviderCatalogByCategoryResult: []store.ProviderCatalogEntry{
			{
				ID:                "gemini-search",
				Category:          "search",
				UpstreamHost:      &geminiHost,
				Scheme:            "https",
				AuthMethod:        &geminiAuth,
				RuntimeProviderID: &geminiRuntimeID,
			},
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	var capturedBody []byte
	agent := agentclient.New("test-token")
	agent.SetHTTPClient(&http.Client{Transport: captureAgentTransport(&capturedBody, "m-remap-cred")})

	gatewayToken := "gw-token"
	proxyToken := "px-token"
	machine := &store.Machine{
		ID:           "m-remap-cred",
		Slug:         "remap-cred",
		AccountID:    1,
		VCPUs:        2,
		MemoryMB:     2048,
		GatewayToken: &gatewayToken,
		ProxyToken:   &proxyToken,
	}

	rs := NewRuntimeService(ms, placementSvc, agent, nil, nil, RuntimeConfig{SecretKey: secretKey}, nil)
	_, _, err := rs.Start(context.Background(), 1, machine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := parseVMRequestPayload(t, capturedBody)

	// Credential for "gemini-search" should be remapped to key "gemini" (RuntimeProviderID)
	if _, ok := req.LLMKeys["gemini"]; !ok {
		t.Fatal("expected llm_keys to have key 'gemini' (RuntimeProviderID)")
	}
	if req.LLMKeys["gemini"].Value != "gemini-key-xxx" {
		t.Fatalf("expected gemini key value 'gemini-key-xxx', got %q", req.LLMKeys["gemini"].Value)
	}
	// The original catalog ID should NOT appear as a key
	if _, ok := req.LLMKeys["gemini-search"]; ok {
		t.Fatal("expected llm_keys NOT to have key 'gemini-search' (should be remapped to RuntimeProviderID)")
	}
}

func TestStart_NebiusKeyAlwaysIncluded(t *testing.T) {
	externalIP := "127.0.0.1"
	lastHeartbeat := time.Now()

	ms := &mockStore{
		placeMachineResult: &store.Host{
			ID:            1,
			Status:        "ready",
			ExternalIP:    &externalIP,
			LastHeartbeat: &lastHeartbeat,
		},
		placeVMIP:              "10.0.0.2",
		listMachineCredsResult: nil, // no user credentials at all
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	var capturedBody []byte
	agent := agentclient.New("test-token")
	agent.SetHTTPClient(&http.Client{Transport: captureAgentTransport(&capturedBody, "m-nebius-test")})

	gatewayToken := "gw-token"
	proxyToken := "px-token"
	machine := &store.Machine{
		ID:           "m-nebius-test",
		Slug:         "nebius-test",
		AccountID:    1,
		VCPUs:        2,
		MemoryMB:     2048,
		GatewayToken: &gatewayToken,
		ProxyToken:   &proxyToken,
	}

	rs := NewRuntimeService(ms, placementSvc, agent, nil, nil, RuntimeConfig{
		NebiusAPIKey: "test-nebius-key",
	}, nil)
	_, _, err := rs.Start(context.Background(), 1, machine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := parseVMRequestPayload(t, capturedBody)

	if _, ok := req.LLMKeys["nebius"]; !ok {
		t.Fatal("expected 'nebius' in llm_keys even with no user credentials")
	}
	if req.LLMKeys["nebius"].Value != "test-nebius-key" {
		t.Fatalf("expected nebius value 'test-nebius-key', got %q", req.LLMKeys["nebius"].Value)
	}
	if req.LLMKeys["nebius"].CredentialType != "api_key" {
		t.Fatalf("expected nebius credential_type 'api_key', got %q", req.LLMKeys["nebius"].CredentialType)
	}
}

func TestResolveRuntimeSelection_HermesUsesDefaultManifests(t *testing.T) {
	rs := NewRuntimeService(&mockStore{}, nil, nil, nil, nil, RuntimeConfig{}, nil)

	rootfsVersion := "hermes-rootfs-v1"
	hermesVersion := "hermes-runtime-v1"
	machine := &store.Machine{
		ID:                     "m-hermes-runtime",
		Kind:                   store.MachineKindHermes,
		DesiredRootfsVersion:   &rootfsVersion,
		DesiredOpenclawVersion: &hermesVersion,
	}

	selection := rs.resolveRuntimeSelection(context.Background(), machine, "")
	if selection.Kind != store.MachineKindHermes {
		t.Fatalf("expected Hermes kind, got %q", selection.Kind)
	}
	if selection.RootfsManifestURI != defaultHermesRootfsManifestURI {
		t.Fatalf("expected Hermes rootfs manifest %q, got %q", defaultHermesRootfsManifestURI, selection.RootfsManifestURI)
	}
	if selection.HermesManifestURI != defaultHermesManifestURI {
		t.Fatalf("expected Hermes runtime manifest %q, got %q", defaultHermesManifestURI, selection.HermesManifestURI)
	}
	if selection.ResolvedHermesVersion != hermesVersion {
		t.Fatalf("expected Hermes version %q, got %q", hermesVersion, selection.ResolvedHermesVersion)
	}
	if selection.ResolvedOpenClawVersion != "" {
		t.Fatalf("expected no OpenClaw version for Hermes machine, got %q", selection.ResolvedOpenClawVersion)
	}
}

func TestStart_TokenGeneration(t *testing.T) {
	ms := &mockStore{
		placeMachineResult: &store.Host{ID: 1},
		placeVMIP:          "10.0.0.2",
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})
	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	machine := &store.Machine{
		ID:           "m-token-gen",
		Slug:         "token-gen",
		VCPUs:        2,
		MemoryMB:     2048,
		GatewayToken: nil,
		ProxyToken:   nil,
	}

	_, _, err := rs.Start(context.Background(), 1, machine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !ms.setMachineTokensCalled {
		t.Fatal("SetMachineTokens should have been called when tokens are nil")
	}
	if machine.GatewayToken == nil {
		t.Fatal("GatewayToken should have been set on machine after Start")
	}
	if machine.ProxyToken == nil {
		t.Fatal("ProxyToken should have been set on machine after Start")
	}
	// Tokens are 16 random bytes → 32 hex chars
	if len(*machine.GatewayToken) != 32 {
		t.Fatalf("GatewayToken should be 32 hex chars, got %d", len(*machine.GatewayToken))
	}
	if len(*machine.ProxyToken) != 32 {
		t.Fatalf("ProxyToken should be 32 hex chars, got %d", len(*machine.ProxyToken))
	}
}

func TestStart_TokenPreservation(t *testing.T) {
	ms := &mockStore{
		placeMachineResult: &store.Host{ID: 1},
		placeVMIP:          "10.0.0.2",
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})
	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	gw := "existing-gateway-token-preserved"
	px := "existing-proxy-token-preserved"
	machine := &store.Machine{
		ID:           "m-token-preserve",
		Slug:         "token-preserve",
		VCPUs:        2,
		MemoryMB:     2048,
		GatewayToken: &gw,
		ProxyToken:   &px,
	}

	_, _, err := rs.Start(context.Background(), 1, machine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ms.setMachineTokensCalled {
		t.Fatal("SetMachineTokens should NOT be called when tokens are already present")
	}
	if *machine.GatewayToken != gw {
		t.Fatalf("GatewayToken should be preserved, got %q", *machine.GatewayToken)
	}
	if *machine.ProxyToken != px {
		t.Fatalf("ProxyToken should be preserved, got %q", *machine.ProxyToken)
	}
}

func TestStart_FirstBootAssemblesSeedConfig(t *testing.T) {
	externalIP := "127.0.0.1"
	lastHeartbeat := time.Now()

	ms := &mockStore{
		placeMachineResult: &store.Host{
			ID:            1,
			Status:        "ready",
			ExternalIP:    &externalIP,
			LastHeartbeat: &lastHeartbeat,
		},
		placeVMIP: "10.0.0.2",
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	var capturedBody []byte
	agent := agentclient.New("test-token")
	agent.SetHTTPClient(&http.Client{Transport: captureAgentTransport(&capturedBody, "m-firstboot")})

	gatewayToken := "gw-token"
	proxyToken := "px-token"
	machine := &store.Machine{
		ID:           "m-firstboot",
		Slug:         "firstboot",
		AccountID:    1,
		VCPUs:        2,
		MemoryMB:     2048,
		GatewayToken: &gatewayToken,
		ProxyToken:   &proxyToken,
		HostID:       nil, // first boot — no prior host assignment
	}

	rs := NewRuntimeService(ms, placementSvc, agent, nil, nil, RuntimeConfig{}, nil)
	_, _, err := rs.Start(context.Background(), 1, machine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := parseVMRequestPayload(t, capturedBody)

	if len(req.OpenClawConf) == 0 {
		t.Fatal("expected non-empty openclaw_config on first boot (seed config should be assembled)")
	}
}

func TestStart_RestartSkipsSeedConfig(t *testing.T) {
	externalIP := "127.0.0.1"
	lastHeartbeat := time.Now()
	hostID := 42
	vmIP := "10.0.0.5"

	ms := &mockStore{
		getHostResult: &store.Host{
			ID:            hostID,
			Status:        "ready",
			ExternalIP:    &externalIP,
			LastHeartbeat: &lastHeartbeat,
		},
		getMachineResult: &store.Machine{
			ID:        "m-restart-seed",
			Slug:      "restart-seed",
			AccountID: 1,
			HostID:    &hostID,
			VMIP:      &vmIP,
			VCPUs:     2,
			MemoryMB:  2048,
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	var capturedBody []byte
	agent := agentclient.New("test-token")
	agent.SetHTTPClient(&http.Client{Transport: captureAgentTransport(&capturedBody, "m-restart-seed")})

	machine := ms.getMachineResult
	// Set tokens so they are not nil (restart path still needs tokens)
	gatewayToken := "gw-token"
	proxyToken := "px-token"
	machine.GatewayToken = &gatewayToken
	machine.ProxyToken = &proxyToken

	rs := NewRuntimeService(ms, placementSvc, agent, nil, nil, RuntimeConfig{}, nil)
	_, _, err := rs.Start(context.Background(), 1, machine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := parseVMRequestPayload(t, capturedBody)

	if len(req.OpenClawConf) != 0 {
		t.Fatalf("expected empty openclaw_config on restart (seed config should be skipped), got %d bytes", len(req.OpenClawConf))
	}
}

func TestStart_PlacementFreshBoot(t *testing.T) {
	expectedHost := &store.Host{ID: 55, VMName: "worker-55"}
	ms := &mockStore{
		placeMachineResult: expectedHost,
		placeVMIP:          "10.0.0.55",
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})
	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	machine := &store.Machine{
		ID:       "m-place-fresh",
		Slug:     "place-fresh",
		VCPUs:    2,
		MemoryMB: 2048,
		HostID:   nil, // fresh boot — no prior host
	}

	host, vmIP, err := rs.Start(context.Background(), 1, machine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if host == nil || host.ID != 55 {
		t.Fatalf("expected host ID 55, got %v", host)
	}
	if vmIP != "10.0.0.55" {
		t.Fatalf("expected vmIP 10.0.0.55, got %s", vmIP)
	}
}

func TestStart_PlacementRestart(t *testing.T) {
	hostID := 42
	vmIP := "10.0.0.5"
	now := time.Now()

	ms := &mockStore{
		getHostResult: &store.Host{
			ID:            hostID,
			Status:        "ready",
			LastHeartbeat: &now,
		},
		getMachineResult: &store.Machine{
			ID:        "m-place-restart",
			Slug:      "place-restart",
			AccountID: 1,
			HostID:    &hostID,
			VMIP:      &vmIP,
			VCPUs:     2,
			MemoryMB:  2048,
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})
	rs := NewRuntimeService(ms, placementSvc, nil, nil, nil, RuntimeConfig{}, nil)

	host, gotIP, err := rs.Start(context.Background(), 1, ms.getMachineResult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if host == nil || host.ID != hostID {
		t.Fatalf("expected host ID %d, got %v", hostID, host)
	}
	if gotIP != vmIP {
		t.Fatalf("expected vmIP %s, got %s", vmIP, gotIP)
	}
	// Restart path uses RecoverAffinity, which goes through PlaceOnHost (not PlaceMachineOnHost)
	// Status should be "starting" for restarts
	if ms.lastStatusUpdate != "starting" {
		t.Fatalf("expected status 'starting' for restart, got %q", ms.lastStatusUpdate)
	}
}

func TestStart_CreateVMFailureReleasesPlacement(t *testing.T) {
	externalIP := "127.0.0.1"
	lastHeartbeat := time.Now()

	hostID := 1
	vmIP := "10.0.0.2"
	ms := &mockStore{
		placeMachineResult: &store.Host{
			ID:            hostID,
			Status:        "ready",
			ExternalIP:    &externalIP,
			LastHeartbeat: &lastHeartbeat,
		},
		placeVMIP: vmIP,
		// GetMachine must return a machine with HostID set so placement.Release
		// actually performs the capacity release (it returns early if HostID is nil).
		getMachineResult: &store.Machine{
			ID:        "m-create-fail",
			Slug:      "create-fail",
			AccountID: 1,
			VCPUs:     2,
			MemoryMB:  2048,
			HostID:    &hostID,
			VMIP:      &vmIP,
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	agent := agentclient.New("test-token")
	agent.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case req.Method == http.MethodDelete && strings.Contains(req.URL.Path, "/vms/"):
				return &http.Response{
					StatusCode: http.StatusNoContent,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     make(http.Header),
				}, nil
			case req.Method == http.MethodPost && req.URL.Path == "/vms":
				// Simulate agent returning 500
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(strings.NewReader("internal error")),
					Header:     make(http.Header),
				}, nil
			default:
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     make(http.Header),
				}, nil
			}
		}),
	})

	gatewayToken := "gw-token"
	proxyToken := "px-token"
	machine := &store.Machine{
		ID:           "m-create-fail",
		Slug:         "create-fail",
		AccountID:    1,
		VCPUs:        2,
		MemoryMB:     2048,
		GatewayToken: &gatewayToken,
		ProxyToken:   &proxyToken,
		HostID:       nil, // fresh boot — no prior host
	}

	rs := NewRuntimeService(ms, placementSvc, agent, nil, nil, RuntimeConfig{}, nil)
	_, _, err := rs.Start(context.Background(), 1, machine)
	if err == nil {
		t.Fatal("expected error from CreateVM failure")
	}
	if !strings.Contains(err.Error(), "failed to create VM") {
		t.Fatalf("expected 'failed to create VM' in error, got: %v", err)
	}
	// On first-boot CreateVM failure, placement should be hard-released.
	// ReleaseHard calls ReleaseHostCapacityWithDisk (which sets releaseCalled)
	// and UnassignMachineFromHost (which sets unassignCalled).
	if !ms.releaseCalled {
		t.Fatal("expected placement release (ReleaseHostCapacityWithDisk) after CreateVM failure")
	}
	if !ms.unassignCalled {
		t.Fatal("expected UnassignMachineFromHost after first-boot CreateVM failure")
	}
	// Tunnel cleanup requires tunnelMgr != nil && machine.TunnelID != nil.
	// In this test tunnelMgr is nil, so ClearMachineTunnel should NOT be called.
	if ms.clearTunnelCalled {
		t.Fatal("ClearMachineTunnel should not be called when tunnelMgr is nil")
	}
	// The error should propagate correctly
	if ms.completeOperationState != "failed" {
		t.Fatalf("expected operation state 'failed', got %q", ms.completeOperationState)
	}
}

// TestStart_CredentialTypes verifies that credential_type from the store is
// preserved in the LLM keys payload sent to the agent.
func TestStart_CredentialTypes(t *testing.T) {
	const secretKey = "01234567890123456789012345678901"

	externalIP := "127.0.0.1"
	lastHeartbeat := time.Now()

	ms := &mockStore{
		placeMachineResult: &store.Host{
			ID:            1,
			Status:        "ready",
			ExternalIP:    &externalIP,
			LastHeartbeat: &lastHeartbeat,
		},
		placeVMIP: "10.0.0.2",
		listMachineCredsResult: []store.Credential{
			{ID: 1, Provider: "anthropic", CredentialType: "api_key", EncryptedValue: mustEncrypt(t, "sk-ant-xxx", secretKey)},
			{ID: 2, Provider: "openai", CredentialType: "subscription_key", EncryptedValue: mustEncrypt(t, "sk-oai-xxx", secretKey)},
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	var capturedBody []byte
	agent := agentclient.New("test-token")
	agent.SetHTTPClient(&http.Client{Transport: captureAgentTransport(&capturedBody, "m-cred-type")})

	gatewayToken := "gw-token"
	proxyToken := "px-token"
	machine := &store.Machine{
		ID:           "m-cred-type",
		Slug:         "cred-type",
		AccountID:    1,
		VCPUs:        2,
		MemoryMB:     2048,
		GatewayToken: &gatewayToken,
		ProxyToken:   &proxyToken,
	}

	rs := NewRuntimeService(ms, placementSvc, agent, nil, nil, RuntimeConfig{SecretKey: secretKey}, nil)
	_, _, err := rs.Start(context.Background(), 1, machine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := parseVMRequestPayload(t, capturedBody)

	if req.LLMKeys["anthropic"].CredentialType != "api_key" {
		t.Fatalf("expected anthropic credential_type 'api_key', got %q", req.LLMKeys["anthropic"].CredentialType)
	}
	if req.LLMKeys["openai"].CredentialType != "subscription_key" {
		t.Fatalf("expected openai credential_type 'subscription_key', got %q", req.LLMKeys["openai"].CredentialType)
	}
}

// TestStart_NoCredentialsWithoutSecretKey verifies that credentials are not
// decrypted or injected when the secret key is not configured.
func TestStart_NoCredentialsWithoutSecretKey(t *testing.T) {
	externalIP := "127.0.0.1"
	lastHeartbeat := time.Now()

	ms := &mockStore{
		placeMachineResult: &store.Host{
			ID:            1,
			Status:        "ready",
			ExternalIP:    &externalIP,
			LastHeartbeat: &lastHeartbeat,
		},
		placeVMIP: "10.0.0.2",
		listMachineCredsResult: []store.Credential{
			{ID: 1, Provider: "anthropic", CredentialType: "api_key", EncryptedValue: "encrypted-blob"},
		},
	}
	placementSvc := fleet.NewPlacementService(ms, fleet.PlacementConfig{DefaultRegion: "us-central1"})

	var capturedBody []byte
	agent := agentclient.New("test-token")
	agent.SetHTTPClient(&http.Client{Transport: captureAgentTransport(&capturedBody, "m-no-key")})

	gatewayToken := "gw-token"
	proxyToken := "px-token"
	machine := &store.Machine{
		ID:           "m-no-key",
		Slug:         "no-key",
		AccountID:    1,
		VCPUs:        2,
		MemoryMB:     2048,
		GatewayToken: &gatewayToken,
		ProxyToken:   &proxyToken,
	}

	// No SecretKey — credentials should not be injected
	rs := NewRuntimeService(ms, placementSvc, agent, nil, nil, RuntimeConfig{SecretKey: ""}, nil)
	_, _, err := rs.Start(context.Background(), 1, machine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := parseVMRequestPayload(t, capturedBody)

	// Without secret key, llm_keys should be empty (except nebius if configured, which it isn't here)
	if len(req.LLMKeys) != 0 {
		t.Fatalf("expected empty llm_keys when SecretKey is not set, got %d entries: %v", len(req.LLMKeys), req.LLMKeys)
	}
}

// Ensure CredentialEntry uses metadata.CredentialEntry for type correctness
var _ map[string]metadata.CredentialEntry = agentclient.VMRequest{}.LLMKeys
