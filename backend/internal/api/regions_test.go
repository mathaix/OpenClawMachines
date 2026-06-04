package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type mockRegionStore struct {
	store.Store
	hosts   []store.Host
	listErr error
}

func (m *mockRegionStore) ListHosts(context.Context) ([]store.Host, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.hosts, nil
}

func TestHandleListRegions_AggregatesReadyFreshHosts(t *testing.T) {
	now := time.Now()
	stale := now.Add(-4 * time.Minute)
	fresh := now.Add(-30 * time.Second)

	ms := &mockRegionStore{
		hosts: []store.Host{
			{ID: 1, Region: "us-west", Status: "ready", LastHeartbeat: &fresh, CapacityVCPUs: 16, UsedVCPUs: 6, CapacityMemoryMB: 64000, UsedMemoryMB: 12000},
			{ID: 2, Region: "US-WEST", Status: "ready", LastHeartbeat: &fresh, CapacityVCPUs: 8, UsedVCPUs: 8, CapacityMemoryMB: 32000, UsedMemoryMB: 16000},
			{ID: 3, Region: "eu-central", Status: "ready", LastHeartbeat: &fresh, CapacityVCPUs: 4, UsedVCPUs: 1, CapacityMemoryMB: 16000, UsedMemoryMB: 2000},
			{ID: 4, Region: "eu-central", Status: "draining", LastHeartbeat: &fresh, CapacityVCPUs: 32, UsedVCPUs: 0, CapacityMemoryMB: 128000, UsedMemoryMB: 0},
			{ID: 5, Region: "asia-east", Status: "ready", LastHeartbeat: &stale, CapacityVCPUs: 32, UsedVCPUs: 0, CapacityMemoryMB: 128000, UsedMemoryMB: 0},
			{ID: 6, Region: "", Status: "ready", LastHeartbeat: &fresh, CapacityVCPUs: 4, UsedVCPUs: 0, CapacityMemoryMB: 8000, UsedMemoryMB: 0},
		},
	}
	srv := &Server{store: ms}

	req := httptest.NewRequest(http.MethodGet, "/api/regions", nil)
	rec := httptest.NewRecorder()

	srv.handleListRegions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var regions []RegionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &regions); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(regions) != 2 {
		t.Fatalf("len(regions) = %d, want 2", len(regions))
	}

	// Available regions sorted first, then alphabetical.
	if regions[0].Code != "eu-central" {
		t.Fatalf("regions[0].code = %q, want %q", regions[0].Code, "eu-central")
	}
	if !regions[0].Available {
		t.Fatal("eu-central should be available")
	}

	if regions[1].Code != "us-west" {
		t.Fatalf("regions[1].code = %q, want %q", regions[1].Code, "us-west")
	}
	if !regions[1].Available {
		t.Fatal("us-west should be available (10 vCPUs free)")
	}
	if regions[1].Name != "US West" {
		t.Fatalf("us-west name = %q, want %q", regions[1].Name, "US West")
	}
}

func TestHandleListRegions_UnavailableRegion(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-30 * time.Second)

	ms := &mockRegionStore{
		hosts: []store.Host{
			{ID: 1, Region: "us-west", Status: "ready", LastHeartbeat: &fresh, CapacityVCPUs: 8, UsedVCPUs: 8},
		},
	}
	srv := &Server{store: ms}

	req := httptest.NewRequest(http.MethodGet, "/api/regions", nil)
	rec := httptest.NewRecorder()
	srv.handleListRegions(rec, req)

	var regions []RegionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &regions); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(regions) != 1 {
		t.Fatalf("len(regions) = %d, want 1", len(regions))
	}
	if regions[0].Available {
		t.Fatal("us-west should be unavailable (fully saturated)")
	}
}

func TestHandleListRegions_StoreError(t *testing.T) {
	ms := &mockRegionStore{listErr: errors.New("boom")}
	srv := &Server{store: ms}

	req := httptest.NewRequest(http.MethodGet, "/api/regions", nil)
	rec := httptest.NewRecorder()

	srv.handleListRegions(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
