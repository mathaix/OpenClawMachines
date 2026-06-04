package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type mockAdminReleasesStore struct {
	store.Store

	releasesByKindChannel map[string][]store.ArtifactRelease
	listCalls             []listCall
}

type listCall struct {
	Kind    string
	Channel string
	Limit   int
}

func (m *mockAdminReleasesStore) ListArtifactReleases(_ context.Context, kind, channel string, limit int) ([]store.ArtifactRelease, error) {
	m.listCalls = append(m.listCalls, listCall{Kind: kind, Channel: channel, Limit: limit})
	rows := m.releasesByKindChannel[kind+":"+channel]
	if limit > 0 && len(rows) > limit {
		return rows[:limit], nil
	}
	return rows, nil
}

func TestHandleAdminListRootfsReleases_MergesStableAndRC(t *testing.T) {
	ms := &mockAdminReleasesStore{
		releasesByKindChannel: map[string][]store.ArtifactRelease{
			"rootfs:stable": {
				{Kind: "rootfs", Version: "v2026.4.26-r2", Channel: "stable", CreatedAt: time.Date(2026, 4, 28, 19, 15, 15, 0, time.UTC)},
				{Kind: "rootfs", Version: "v2026.4.15-r1", Channel: "stable", CreatedAt: time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)},
			},
			"rootfs:rc": {
				{Kind: "rootfs", Version: "v2026.5.2-r1", Channel: "rc", CreatedAt: time.Date(2026, 5, 4, 23, 51, 15, 0, time.UTC)},
			},
		},
	}
	srv := &Server{store: ms}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/rootfs-releases", nil)
	rec := httptest.NewRecorder()
	srv.handleAdminListRootfsReleases(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Releases []RootfsReleaseSummary `json:"releases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(resp.Releases) != 3 {
		t.Fatalf("expected 3 releases (2 stable + 1 rc), got %d", len(resp.Releases))
	}

	// rc release on 2026-05-04 is most recent → first.
	if resp.Releases[0].Version != "v2026.5.2-r1" || resp.Releases[0].Channel != "rc" {
		t.Fatalf("expected first to be v2026.5.2-r1 (rc), got %+v", resp.Releases[0])
	}
	if resp.Releases[1].Version != "v2026.4.26-r2" || resp.Releases[1].Channel != "stable" {
		t.Fatalf("expected second to be v2026.4.26-r2 (stable), got %+v", resp.Releases[1])
	}
	if resp.Releases[2].Version != "v2026.4.15-r1" {
		t.Fatalf("expected third to be v2026.4.15-r1, got %+v", resp.Releases[2])
	}

	// Confirm both channels were queried with kind=rootfs.
	if len(ms.listCalls) != 2 {
		t.Fatalf("expected 2 ListArtifactReleases calls, got %d", len(ms.listCalls))
	}
	for _, c := range ms.listCalls {
		if c.Kind != "rootfs" {
			t.Fatalf("expected kind=rootfs, got %q", c.Kind)
		}
		if c.Limit != 20 {
			t.Fatalf("expected limit=20, got %d", c.Limit)
		}
	}
}

func TestHandleAdminListRootfsReleases_EmptyResult(t *testing.T) {
	ms := &mockAdminReleasesStore{
		releasesByKindChannel: map[string][]store.ArtifactRelease{},
	}
	srv := &Server{store: ms}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/rootfs-releases", nil)
	rec := httptest.NewRecorder()
	srv.handleAdminListRootfsReleases(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp struct {
		Releases []RootfsReleaseSummary `json:"releases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Releases) != 0 {
		t.Fatalf("expected 0 releases, got %d", len(resp.Releases))
	}
}
