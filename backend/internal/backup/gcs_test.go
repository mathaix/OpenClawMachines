package backup

import "testing"
import "time"

func TestGCSStoreImplementsInterface(t *testing.T) {
	var _ BackupStore = (*GCSStore)(nil)
}

func TestGCSPathFormat(t *testing.T) {
	s := &GCSStore{prefix: "backups"}
	ts, _ := time.Parse(time.RFC3339, "2026-03-11T08:15:00Z")
	path := s.gcsPath("machine-123", ts)
	expected := "backups/machine-123/20260311T081500Z.ext4.zst.enc"
	if path != expected {
		t.Errorf("gcsPath = %q, want %q", path, expected)
	}
}
