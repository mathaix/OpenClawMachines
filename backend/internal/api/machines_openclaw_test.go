package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// openClawTestContext builds a request context with account ID, chi route params,
// and auth claims set — matching what production middleware injects.
func openClawTestContext(ctx context.Context, accountID int, machineID string) context.Context {
	ctx = context.WithValue(ctx, accountIDKey, accountID)
	ctx = auth.WithUser(ctx, &auth.Claims{UserID: 1, Email: "test@example.com", AccountID: accountID})
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", machineID)
	return context.WithValue(ctx, chi.RouteCtxKey, rctx)
}

type mockMachineOpenClawStore struct {
	store.Store

	machine                     *store.Machine
	updatedMachine              *store.Machine
	artifactReleases            []store.ArtifactRelease
	updateMachineVersionCalled  bool
	updateMachineVersionVersion string
	createOperationCalled       bool
	createOperationErr          error
	completeOperationCalled     bool
}

func (m *mockMachineOpenClawStore) GetMachine(_ context.Context, id string) (*store.Machine, error) {
	if m.machine == nil || m.machine.ID != id {
		return nil, fmt.Errorf("machine not found")
	}
	cp := *m.machine
	return &cp, nil
}

func (m *mockMachineOpenClawStore) UpdateMachine(_ context.Context, machine *store.Machine) error {
	cp := *machine
	m.updatedMachine = &cp
	m.machine = &cp
	return nil
}

func (m *mockMachineOpenClawStore) UpdateMachineVersion(_ context.Context, _, _, version string) error {
	m.updateMachineVersionCalled = true
	m.updateMachineVersionVersion = version
	if m.machine != nil {
		cp := *m.machine
		cp.ResolvedOpenclawVersion = &version
		cp.OpenclawVersion = &version
		m.machine = &cp
	}
	return nil
}

func (m *mockMachineOpenClawStore) CreateMachineOperation(_ context.Context, op *store.MachineOperation) error {
	m.createOperationCalled = true
	if m.createOperationErr != nil {
		return m.createOperationErr
	}
	op.ID = "op-test-1"
	return nil
}

func (m *mockMachineOpenClawStore) CompleteMachineOperation(_ context.Context, _ string, _ string, _ *string) error {
	m.completeOperationCalled = true
	return nil
}

func (m *mockMachineOpenClawStore) ListArtifactReleases(_ context.Context, kind, channel string, limit int) ([]store.ArtifactRelease, error) {
	out := make([]store.ArtifactRelease, 0, len(m.artifactReleases))
	for _, rel := range m.artifactReleases {
		if rel.Kind != kind || rel.Channel != channel {
			continue
		}
		out = append(out, rel)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *mockMachineOpenClawStore) GetArtifactRelease(_ context.Context, kind, version string) (*store.ArtifactRelease, error) {
	for i := range m.artifactReleases {
		r := m.artifactReleases[i]
		if r.Kind == kind && r.Version == version {
			cp := r
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("release not found")
}

func TestHandleUpgradeMachineOpenClaw_PersistsPinnedArtifactSelection(t *testing.T) {
	channel := "stable"
	versionSource := "channel"
	runtimeSource := "artifact"
	ms := &mockMachineOpenClawStore{
		machine: &store.Machine{
			ID:             "machine-1",
			AccountID:      1,
			Name:           "Bot",
			Status:         "stopped",
			VCPUs:          2,
			MemoryMB:       4096,
			CreatedAt:      time.Now(),
			DesiredChannel: &channel,
			VersionSource:  &versionSource,
			RuntimeSource:  &runtimeSource,
		},
	}
	srv := &Server{store: ms}

	body := map[string]any{
		"version":   "openclaw-2026.04.06",
		"apply_now": true,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines/machine-1/openclaw/upgrade", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(openClawTestContext(req.Context(), 1, "machine-1"))

	rec := httptest.NewRecorder()
	srv.handleUpgradeMachineOpenClaw(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ms.updatedMachine == nil {
		t.Fatal("expected machine to be updated")
	}
	if ms.updatedMachine.DesiredOpenclawVersion == nil || *ms.updatedMachine.DesiredOpenclawVersion != "openclaw-2026.04.06" {
		t.Fatalf("desired_openclaw_version = %v, want openclaw-2026.04.06", ms.updatedMachine.DesiredOpenclawVersion)
	}
	if ms.updatedMachine.DesiredChannel != nil {
		t.Fatalf("desired_channel = %v, want nil after pinning openclaw version", ms.updatedMachine.DesiredChannel)
	}
	if ms.updatedMachine.VersionSource == nil || *ms.updatedMachine.VersionSource != "pinned" {
		t.Fatalf("version_source = %v, want pinned", ms.updatedMachine.VersionSource)
	}
	if ms.updatedMachine.RuntimeSource == nil || *ms.updatedMachine.RuntimeSource != "artifact" {
		t.Fatalf("runtime_source = %v, want artifact", ms.updatedMachine.RuntimeSource)
	}

	var resp runtimeChangeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "pending_restart" {
		t.Fatalf("response status = %q, want pending_restart", resp.Status)
	}
}

func TestHandleListRootfsReleases(t *testing.T) {
	ms := &mockMachineOpenClawStore{
		artifactReleases: []store.ArtifactRelease{
			{Kind: "rootfs", Version: "rootfs-2026.04.08", Channel: "stable", CreatedAt: time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)},
			{Kind: "rootfs", Version: "rootfs-2026.04.07", Channel: "stable", CreatedAt: time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC)},
			{Kind: "hermes-rootfs", Version: "hermes-rootfs-2026.04.09", Channel: "stable", CreatedAt: time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC)},
		},
	}
	srv := &Server{store: ms}

	req := httptest.NewRequest(http.MethodGet, "/api/accounts/1/rootfs/releases?channel=stable", nil)
	rec := httptest.NewRecorder()
	srv.handleListRootfsReleases(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(resp))
	}
	if resp[0]["version"] != "rootfs-2026.04.08" || resp[0]["exact_version"] != "rootfs-2026.04.08" {
		t.Fatalf("unexpected first release: %+v", resp[0])
	}
}

func TestHandleListRootfsReleases_HermesUsesHermesRootfsKind(t *testing.T) {
	ms := &mockMachineOpenClawStore{
		artifactReleases: []store.ArtifactRelease{
			{Kind: "rootfs", Version: "rootfs-2026.04.08", Channel: "stable", CreatedAt: time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)},
			{Kind: "hermes-rootfs", Version: "hermes-rootfs-2026.04.09", Channel: "stable", CreatedAt: time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC)},
		},
	}
	srv := &Server{store: ms}

	req := httptest.NewRequest(http.MethodGet, "/api/accounts/1/rootfs/releases?channel=stable&kind=hermes", nil)
	rec := httptest.NewRecorder()
	srv.handleListRootfsReleases(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp) != 1 {
		t.Fatalf("expected 1 release, got %d", len(resp))
	}
	if resp[0]["version"] != "hermes-rootfs-2026.04.09" || resp[0]["exact_version"] != "hermes-rootfs-2026.04.09" {
		t.Fatalf("unexpected hermes rootfs release: %+v", resp[0])
	}
}

func TestHandleListOpenClawReleases_HermesUsesHermesRuntimeKind(t *testing.T) {
	ms := &mockMachineOpenClawStore{
		artifactReleases: []store.ArtifactRelease{
			{Kind: "openclaw", Version: "openclaw-2026.04.08", Channel: "stable", CreatedAt: time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)},
			{Kind: "hermes", Version: "hermes-2026.04.09", Channel: "stable", CreatedAt: time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC)},
		},
	}
	srv := &Server{store: ms}

	req := httptest.NewRequest(http.MethodGet, "/api/accounts/1/openclaw/releases?channel=stable&kind=hermes", nil)
	rec := httptest.NewRecorder()
	srv.handleListOpenClawReleases(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp) != 1 {
		t.Fatalf("expected 1 release, got %d", len(resp))
	}
	if resp[0]["version"] != "hermes-2026.04.09" || resp[0]["exact_version"] != "hermes-2026.04.09" {
		t.Fatalf("unexpected hermes runtime release: %+v", resp[0])
	}
}

func TestHandleRollbackMachineOpenClaw_RejectsMissingVersion(t *testing.T) {
	runtimeSource := "artifact"
	ms := &mockMachineOpenClawStore{
		machine: &store.Machine{
			ID:            "machine-1",
			AccountID:     1,
			Name:          "Bot",
			Status:        "running",
			VCPUs:         2,
			MemoryMB:      4096,
			CreatedAt:     time.Now(),
			RuntimeSource: &runtimeSource,
		},
	}
	srv := &Server{store: ms}

	body := map[string]any{
		"version": "   ",
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines/machine-1/openclaw/rollback", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(openClawTestContext(req.Context(), 1, "machine-1"))

	rec := httptest.NewRecorder()
	srv.handleRollbackMachineOpenClaw(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if ms.updatedMachine != nil {
		t.Fatal("expected request to be rejected before persistence")
	}
}

func TestHandleRollbackMachineOpenClaw_PreservesExplicitRuntimeSourceOnQueuedChange(t *testing.T) {
	currentVersion := "openclaw-2026.04.06"
	runtimeSource := "artifact"
	ms := &mockMachineOpenClawStore{
		machine: &store.Machine{
			ID:                      "machine-1",
			AccountID:               1,
			Name:                    "Bot",
			Status:                  "stopped",
			VCPUs:                   2,
			MemoryMB:                4096,
			CreatedAt:               time.Now(),
			ResolvedOpenclawVersion: &currentVersion,
			RuntimeSource:           &runtimeSource,
		},
	}
	srv := &Server{store: ms}

	body := map[string]any{
		"version":        "openclaw-2026.04.05",
		"runtime_source": "artifact",
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines/machine-1/openclaw/rollback", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(openClawTestContext(req.Context(), 1, "machine-1"))

	rec := httptest.NewRecorder()
	srv.handleRollbackMachineOpenClaw(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ms.updatedMachine == nil {
		t.Fatal("expected machine to be updated")
	}
	if ms.updatedMachine.RuntimeSource == nil || *ms.updatedMachine.RuntimeSource != "artifact" {
		t.Fatalf("runtime_source = %v, want artifact", ms.updatedMachine.RuntimeSource)
	}
	if ms.updatedMachine.DesiredOpenclawVersion == nil || *ms.updatedMachine.DesiredOpenclawVersion != "openclaw-2026.04.05" {
		t.Fatalf("desired_openclaw_version = %v, want openclaw-2026.04.05", ms.updatedMachine.DesiredOpenclawVersion)
	}
}

func TestHandleUpgradeMachineOpenClaw_RejectsInvalidVersion(t *testing.T) {
	ms := &mockMachineOpenClawStore{
		machine: &store.Machine{
			ID:        "machine-1",
			AccountID: 1,
			Name:      "Bot",
			Status:    "stopped",
			VCPUs:     2,
			MemoryMB:  4096,
			CreatedAt: time.Now(),
		},
	}
	srv := &Server{store: ms}

	body := map[string]any{
		"version": "../escape",
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines/machine-1/openclaw/upgrade", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(openClawTestContext(req.Context(), 1, "machine-1"))

	rec := httptest.NewRecorder()
	srv.handleUpgradeMachineOpenClaw(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if ms.createOperationCalled {
		t.Fatal("expected invalid version to be rejected before creating an operation")
	}
	if ms.updatedMachine != nil {
		t.Fatal("expected invalid version to be rejected before persistence")
	}
}

func TestHandleUpgradeMachineOpenClaw_FirstImmediateUpgradeAcquiresOperationBeforePersistence(t *testing.T) {
	ms := &mockMachineOpenClawStore{
		machine: &store.Machine{
			ID:        "machine-1",
			AccountID: 1,
			Name:      "Bot",
			Status:    "running",
			VCPUs:     2,
			MemoryMB:  4096,
			CreatedAt: time.Now(),
		},
		createOperationErr: errors.New("operation already in progress"),
	}
	srv := &Server{store: ms}

	body := map[string]any{
		"version":   "openclaw-2026.04.06",
		"apply_now": true,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines/machine-1/openclaw/upgrade", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(openClawTestContext(req.Context(), 1, "machine-1"))

	rec := httptest.NewRecorder()
	srv.handleUpgradeMachineOpenClaw(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !ms.createOperationCalled {
		t.Fatal("expected immediate upgrade to attempt the machine operation lock")
	}
	if ms.updatedMachine != nil {
		t.Fatal("expected no machine persistence before the operation lock is acquired")
	}
	if ms.updateMachineVersionCalled {
		t.Fatal("expected no resolved-version persistence before the machine operation lock is acquired")
	}
}

func TestHandleUpgradeMachineRootfs_PersistsPinnedArtifactSelection(t *testing.T) {
	openclawVersion := "openclaw-2026.04.06"
	versionSource := "channel"
	runtimeSource := "artifact"
	ms := &mockMachineOpenClawStore{
		machine: &store.Machine{
			ID:                      "machine-1",
			AccountID:               1,
			Name:                    "Bot",
			Status:                  "stopped",
			VCPUs:                   2,
			MemoryMB:                4096,
			CreatedAt:               time.Now(),
			ResolvedOpenclawVersion: &openclawVersion,
			VersionSource:           &versionSource,
			RuntimeSource:           &runtimeSource,
		},
	}
	srv := &Server{store: ms}

	body := map[string]any{
		"version":   "rootfs-2026.04.08",
		"apply_now": true,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines/machine-1/rootfs/upgrade", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(openClawTestContext(req.Context(), 1, "machine-1"))

	rec := httptest.NewRecorder()
	srv.handleUpgradeMachineRootfs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ms.updatedMachine == nil {
		t.Fatal("expected machine to be updated")
	}
	if ms.updatedMachine.DesiredRootfsVersion == nil || *ms.updatedMachine.DesiredRootfsVersion != "rootfs-2026.04.08" {
		t.Fatalf("desired_rootfs_version = %v, want rootfs-2026.04.08", ms.updatedMachine.DesiredRootfsVersion)
	}
	if ms.updatedMachine.DesiredChannel != nil {
		t.Fatalf("desired_channel = %v, want nil after pinning rootfs version", ms.updatedMachine.DesiredChannel)
	}
	if ms.updatedMachine.VersionSource == nil || *ms.updatedMachine.VersionSource != "pinned" {
		t.Fatalf("version_source = %v, want pinned", ms.updatedMachine.VersionSource)
	}

	var resp runtimeChangeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "pending_restart" {
		t.Fatalf("response status = %q, want pending_restart", resp.Status)
	}
}

func TestHandleUpgradeMachineRootfs_RejectsInvalidVersion(t *testing.T) {
	ms := &mockMachineOpenClawStore{
		machine: &store.Machine{
			ID:        "machine-1",
			AccountID: 1,
			Name:      "Bot",
			Status:    "stopped",
			VCPUs:     2,
			MemoryMB:  4096,
			CreatedAt: time.Now(),
		},
	}
	srv := &Server{store: ms}

	body := map[string]any{
		"version": "../escape",
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines/machine-1/rootfs/upgrade", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(openClawTestContext(req.Context(), 1, "machine-1"))

	rec := httptest.NewRecorder()
	srv.handleUpgradeMachineRootfs(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if ms.createOperationCalled {
		t.Fatal("expected invalid version to be rejected before creating an operation")
	}
}

func TestHandleUpgradeMachineRootfs_FirstImmediateUpgradeAcquiresOperationBeforePersistence(t *testing.T) {
	ms := &mockMachineOpenClawStore{
		machine: &store.Machine{
			ID:        "machine-1",
			AccountID: 1,
			Name:      "Bot",
			Status:    "running",
			VCPUs:     2,
			MemoryMB:  4096,
			CreatedAt: time.Now(),
		},
		createOperationErr: errors.New("operation already in progress"),
	}
	srv := &Server{store: ms}

	body := map[string]any{
		"version":   "rootfs-2026.04.08",
		"apply_now": true,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines/machine-1/rootfs/upgrade", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(openClawTestContext(req.Context(), 1, "machine-1"))

	rec := httptest.NewRecorder()
	srv.handleUpgradeMachineRootfs(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !ms.createOperationCalled {
		t.Fatal("expected immediate upgrade to attempt the machine operation lock")
	}
	if ms.updatedMachine != nil {
		t.Fatal("expected no machine persistence before the operation lock is acquired")
	}
	if ms.updateMachineVersionCalled {
		t.Fatal("expected no resolved-version persistence before the machine operation lock is acquired")
	}
}

func TestHandleRollbackMachineRootfs_PersistsPinnedArtifactSelection(t *testing.T) {
	openclawVersion := "openclaw-2026.04.06"
	currentRootfs := "rootfs-2026.04.08"
	runtimeSource := "artifact"
	ms := &mockMachineOpenClawStore{
		machine: &store.Machine{
			ID:                      "machine-1",
			AccountID:               1,
			Name:                    "Bot",
			Status:                  "stopped",
			VCPUs:                   2,
			MemoryMB:                4096,
			CreatedAt:               time.Now(),
			ResolvedRootfsVersion:   &currentRootfs,
			ResolvedOpenclawVersion: &openclawVersion,
			RuntimeSource:           &runtimeSource,
		},
	}
	srv := &Server{store: ms}

	body := map[string]any{
		"version":   "rootfs-2026.04.07",
		"apply_now": false,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines/machine-1/rootfs/rollback", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(openClawTestContext(req.Context(), 1, "machine-1"))

	rec := httptest.NewRecorder()
	srv.handleRollbackMachineRootfs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ms.updatedMachine == nil {
		t.Fatal("expected machine to be updated")
	}
	if ms.updatedMachine.DesiredRootfsVersion == nil || *ms.updatedMachine.DesiredRootfsVersion != "rootfs-2026.04.07" {
		t.Fatalf("desired_rootfs_version = %v, want rootfs-2026.04.07", ms.updatedMachine.DesiredRootfsVersion)
	}
	var resp runtimeChangeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "pending_restart" {
		t.Fatalf("response status = %q, want pending_restart", resp.Status)
	}
}

func TestHandleRollbackMachineRootfs_RejectsInvalidVersion(t *testing.T) {
	ms := &mockMachineOpenClawStore{
		machine: &store.Machine{
			ID:        "machine-1",
			AccountID: 1,
			Name:      "Bot",
			Status:    "stopped",
			VCPUs:     2,
			MemoryMB:  4096,
			CreatedAt: time.Now(),
		},
	}
	srv := &Server{store: ms}

	body := map[string]any{
		"version": "../escape",
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines/machine-1/rootfs/rollback", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(openClawTestContext(req.Context(), 1, "machine-1"))

	rec := httptest.NewRecorder()
	srv.handleRollbackMachineRootfs(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if ms.createOperationCalled {
		t.Fatal("expected invalid version to be rejected before creating an operation")
	}
}
