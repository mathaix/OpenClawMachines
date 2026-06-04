package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// compatGuardReleases builds the catalog rows needed for the runtime-compat
// guard to fire: an openclaw release that requires a specific minimum rootfs,
// plus two rootfs rows ordered by created_at so the helper can decide which
// is newer.
func compatGuardReleases(minRootfs, olderRootfs string) []store.ArtifactRelease {
	min := minRootfs
	return []store.ArtifactRelease{
		{
			Kind: "openclaw", Version: "v2026.4.15-r1",
			MinRootfsVersion: &min,
			CreatedAt:        time.Date(2026, 4, 18, 0, 0, 0, 0, time.UTC),
		},
		{
			Kind: "rootfs", Version: minRootfs,
			CreatedAt: time.Date(2026, 4, 18, 23, 46, 18, 0, time.UTC),
		},
		{
			Kind: "rootfs", Version: olderRootfs,
			CreatedAt: time.Date(2026, 4, 14, 14, 31, 54, 0, time.UTC),
		},
	}
}

func TestHandleUpgradeMachineOpenClaw_CompatGuard_StaleRootfsRejected(t *testing.T) {
	runtimeSource := "artifact"
	oldRootfs := "cd60435-20260414T143154Z"
	ms := &mockMachineOpenClawStore{
		machine: &store.Machine{
			ID:                  "machine-1",
			AccountID:           1,
			Name:                "Bot",
			Status:              "stopped",
			VCPUs:               2,
			MemoryMB:            4096,
			CreatedAt:           time.Now(),
			ActualRootfsVersion: &oldRootfs,
			RuntimeSource:       &runtimeSource,
		},
		artifactReleases: compatGuardReleases("1ae2d37-20260418T234618Z", oldRootfs),
	}
	srv := &Server{store: ms}

	body := map[string]any{"version": "v2026.4.15-r1"}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines/machine-1/openclaw/upgrade", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(openClawTestContext(req.Context(), 1, "machine-1"))

	rec := httptest.NewRecorder()
	srv.handleUpgradeMachineOpenClaw(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 body=%s", rec.Code, rec.Body.String())
	}
	if ms.updatedMachine != nil {
		t.Fatal("expected no machine persistence on rejection")
	}
	if ms.createOperationCalled {
		t.Fatal("expected no operation to be created on rejection")
	}
}

func TestHandleRollbackMachineOpenClaw_CompatGuard_AllowsRollbackBelowFloor(t *testing.T) {
	// Rolling BACK to a version that has a min_rootfs_version the machine no
	// longer satisfies should still be allowed — rollback by definition reverts
	// to a previously-working state.
	runtimeSource := "artifact"
	oldRootfs := "cd60435-20260414T143154Z"
	ms := &mockMachineOpenClawStore{
		machine: &store.Machine{
			ID:                  "machine-1",
			AccountID:           1,
			Name:                "Bot",
			Status:              "stopped",
			VCPUs:               2,
			MemoryMB:            4096,
			CreatedAt:           time.Now(),
			ActualRootfsVersion: &oldRootfs,
			RuntimeSource:       &runtimeSource,
		},
		artifactReleases: compatGuardReleases("1ae2d37-20260418T234618Z", oldRootfs),
	}
	srv := &Server{store: ms}

	body := map[string]any{"version": "v2026.4.15-r1"}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/machines/machine-1/openclaw/rollback", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(openClawTestContext(req.Context(), 1, "machine-1"))

	rec := httptest.NewRecorder()
	srv.handleRollbackMachineOpenClaw(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRuntimeUpgrade_CompatGuard_IncompatiblePairRejected(t *testing.T) {
	ms := runtimeUpgradeStore()
	ms.artifactReleases = compatGuardReleases("1ae2d37-20260418T234618Z", "cd60435-20260414T143154Z")
	srv := &Server{store: ms}

	// Caller pairs the new openclaw with an older rootfs — must be rejected
	// so the atomic endpoint can't be used to bypass the compat floor.
	req := runtimeUpgradeRequest(t, 1, "machine-1", map[string]any{
		"openclaw_version": "v2026.4.15-r1",
		"rootfs_version":   "cd60435-20260414T143154Z",
	})
	rec := httptest.NewRecorder()
	srv.handleRuntimeUpgrade(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 body=%s", rec.Code, rec.Body.String())
	}
	if ms.updatedMachine != nil {
		t.Fatal("expected no machine persistence on rejection")
	}
	body := rec.Body.String()
	for _, want := range []string{"v2026.4.15-r1", "1ae2d37-20260418T234618Z", "cd60435-20260414T143154Z"} {
		if !bytes.Contains([]byte(body), []byte(want)) {
			t.Errorf("error body missing %q: %s", want, body)
		}
	}
}
