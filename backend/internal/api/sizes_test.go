package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLookupSize(t *testing.T) {
	tests := []struct {
		id    string
		found bool
		vcpus int
	}{
		{"basic", true, 1},
		{"standard", true, 2},
		{"pro", true, 4},
		{"unknown", false, 0},
	}
	for _, tt := range tests {
		s := LookupSize(tt.id)
		if tt.found && s == nil {
			t.Errorf("LookupSize(%q) = nil, want non-nil", tt.id)
		} else if !tt.found && s != nil {
			t.Errorf("LookupSize(%q) = %+v, want nil", tt.id, s)
		} else if tt.found && s.VCPUs != tt.vcpus {
			t.Errorf("LookupSize(%q).VCPUs = %d, want %d", tt.id, s.VCPUs, tt.vcpus)
		}
	}
}

func TestDefaultSize(t *testing.T) {
	d := DefaultSize()
	if d == nil {
		t.Fatal("DefaultSize() = nil")
		return
	}
	if d.ID != "standard" {
		t.Errorf("DefaultSize().ID = %q, want %q", d.ID, "standard")
	}
}

func TestHandleListSizes(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/sizes", nil)
	rec := httptest.NewRecorder()

	srv.handleListSizes(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var sizes []MachineSize
	if err := json.Unmarshal(rec.Body.Bytes(), &sizes); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(sizes) != 3 {
		t.Fatalf("len(sizes) = %d, want 3", len(sizes))
	}

	// Verify canonical order and values
	expected := []struct {
		id    string
		vcpus int
		memMB int
		disk  int
		swap  int
	}{
		{"basic", 1, 3072, 10, 1024},
		{"standard", 2, 6144, 20, 1024},
		{"pro", 4, 12288, 50, 1024},
	}
	for i, e := range expected {
		s := sizes[i]
		if s.ID != e.id || s.VCPUs != e.vcpus || s.MemoryMB != e.memMB || s.DiskGB != e.disk || s.SwapMB != e.swap {
			t.Errorf("sizes[%d] = %+v, want id=%s vcpus=%d mem=%d disk=%d swap=%d",
				i, s, e.id, e.vcpus, e.memMB, e.disk, e.swap)
		}
	}
}
