package rootfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestParseManifest_Valid(t *testing.T) {
	input := `{
		"version": "abc1234-20260224T120000Z",
		"sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"size_bytes": 2147483648,
		"compressed_size_bytes": 943718400,
		"url": "gs://example-ocm-artifacts/rootfs/rootfs-abc1234.ext4.zst",
		"built_at": "2026-02-24T12:00:00Z",
		"openclaw_version": "0.8.3",
		"agent_version": "abc1234-20260224T120000Z",
		"data_version": 1
	}`
	m, err := ParseManifest(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.SHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("SHA256 = %q", m.SHA256)
	}
	if m.URL != "gs://example-ocm-artifacts/rootfs/rootfs-abc1234.ext4.zst" {
		t.Errorf("URL = %q", m.URL)
	}
	if m.Compression != "zstd" {
		t.Errorf("Compression = %q, want \"zstd\"", m.Compression)
	}
	if m.Version != "abc1234-20260224T120000Z" {
		t.Errorf("Version = %q", m.Version)
	}
	if m.SizeBytes != 2147483648 {
		t.Errorf("SizeBytes = %d", m.SizeBytes)
	}
}

func TestParseManifest_WithCompression(t *testing.T) {
	input := `{
		"sha256": "abc123",
		"url": "gs://bucket/file.zst",
		"compression": "lz4"
	}`
	m, err := ParseManifest(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Compression != "lz4" {
		t.Errorf("Compression = %q, want \"lz4\"", m.Compression)
	}
}

func TestParseManifest_MissingSHA256(t *testing.T) {
	input := `{
		"url": "gs://bucket/file.zst",
		"version": "abc123"
	}`
	_, err := ParseManifest(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for missing sha256")
	}
	if !strings.Contains(err.Error(), "sha256") {
		t.Errorf("error should mention sha256, got: %v", err)
	}
}

func TestParseManifest_MissingURL(t *testing.T) {
	input := `{
		"sha256": "abc123",
		"version": "abc123"
	}`
	_, err := ParseManifest(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for missing url")
	}
	if !strings.Contains(err.Error(), "url") {
		t.Errorf("error should mention url, got: %v", err)
	}
}

func TestParseManifest_InvalidJSON(t *testing.T) {
	input := `not json`
	_, err := ParseManifest(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestEnsureRootfs_ManifestFailure_CachedExists(t *testing.T) {
	stateDir := t.TempDir()

	// Create a cached rootfs file at the expected path
	cachedPath := filepath.Join(stateDir, ".base-rootfs.ext4")
	if err := os.WriteFile(cachedPath, []byte("fake rootfs content"), 0644); err != nil {
		t.Fatalf("failed to create cached rootfs: %v", err)
	}

	f := &Fetcher{
		cfg: Config{
			Prefix:          "rootfs",
			RetryAttempts:   1,
			DownloadTimeout: 5 * time.Second,
		},
		fetchManifestOverride: func(ctx context.Context) (*Manifest, error) {
			return nil, fmt.Errorf("simulated GCS failure")
		},
	}

	got, err := f.EnsureRootfs(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("expected success with cached rootfs, got error: %v", err)
	}
	if got != cachedPath {
		t.Errorf("returned path = %q, want %q", got, cachedPath)
	}
}

// TestCreateRootfsTempFileConcurrentCallsProduceUniqueNames locks in the
// C3 fix: concurrent EnsureRootfs invocations for the same destination
// path must each receive a distinct temp file. Before the fix, the code
// used a fixed "<dst>.tmp" suffix so two downloaders would write into the
// same file and one would corrupt the other mid-stream — the SHA check
// still passed because whichever writer finished last won the hash
// comparison.
func TestCreateRootfsTempFileConcurrentCallsProduceUniqueNames(t *testing.T) {
	dir := t.TempDir()
	dstPath := filepath.Join(dir, "rootfs.ext4")

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)

	results := make(chan string, goroutines)
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			f, path, err := createRootfsTempFile(dstPath)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = f.Close() }()
			// Write a goroutine-specific marker so if two goroutines
			// landed on the same file name, the assertion below would
			// see a collision from one of them.
			if _, err := f.WriteString(path); err != nil {
				errs <- err
				return
			}
			results <- path
		}()
	}

	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("createRootfsTempFile returned error: %v", err)
	}

	seen := make(map[string]struct{})
	for path := range results {
		if _, dup := seen[path]; dup {
			t.Fatalf("createRootfsTempFile returned duplicate temp path %q — concurrent callers would clobber each other", path)
		}
		seen[path] = struct{}{}

		// Temp file must live in the destination directory so the
		// downstream os.Rename stays atomic on the same filesystem.
		if gotDir := filepath.Dir(path); gotDir != dir {
			t.Fatalf("temp path %q is in %q, want %q (atomic rename requires same dir)", path, gotDir, dir)
		}
		// Base name must carry the destination prefix so ad-hoc cleanup
		// tools (find/rm) can spot leftover temp files.
		if !strings.HasPrefix(filepath.Base(path), "rootfs.ext4.tmp.") {
			t.Fatalf("temp path %q lost the rootfs prefix", path)
		}

		// Clean up — downloadAndVerify would normally rename or remove
		// these, but the test calls the helper directly.
		_ = os.Remove(path)
	}

	if len(seen) != goroutines {
		t.Fatalf("expected %d unique temp paths, got %d", goroutines, len(seen))
	}
}

func TestEnsureRootfsVersioned_LocalManifestAndArtifact(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	artifactPath := filepath.Join(dir, "rootfs.ext4.zst")
	payload := []byte("local rootfs payload")

	artifactFile, err := os.Create(artifactPath)
	if err != nil {
		t.Fatalf("create compressed artifact: %v", err)
	}
	zw, err := zstd.NewWriter(artifactFile)
	if err != nil {
		t.Fatalf("create zstd writer: %v", err)
	}
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("compress payload: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zstd writer: %v", err)
	}
	if err := artifactFile.Close(); err != nil {
		t.Fatalf("close artifact file: %v", err)
	}
	artifactInfo, err := os.Stat(artifactPath)
	if err != nil {
		t.Fatalf("stat artifact: %v", err)
	}

	sum := sha256.Sum256(payload)
	manifestPath := filepath.Join(dir, "manifest.json")
	manifest := fmt.Sprintf(`{
		"version": "local-rootfs",
		"sha256": %q,
		"size_bytes": %d,
		"compressed_size_bytes": %d,
		"url": %q,
		"built_at": "2026-05-12T00:00:00Z",
		"compression": "zstd"
	}`, hex.EncodeToString(sum[:]), len(payload), artifactInfo.Size(), "file://"+artifactPath)
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	f := &Fetcher{
		cfg: Config{
			ManifestURI:     manifestPath,
			DownloadTimeout: 5 * time.Second,
			RetryAttempts:   1,
		},
	}

	got, err := f.EnsureRootfsVersioned(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("ensure local rootfs: %v", err)
	}
	gotBytes, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read downloaded rootfs: %v", err)
	}
	if string(gotBytes) != string(payload) {
		t.Fatalf("downloaded rootfs = %q, want %q", gotBytes, payload)
	}
}

func TestEnsureRootfs_ManifestFailure_NoCached(t *testing.T) {
	stateDir := t.TempDir()

	f := &Fetcher{
		cfg: Config{
			Prefix:          "rootfs",
			RetryAttempts:   1,
			DownloadTimeout: 5 * time.Second,
		},
		fetchManifestOverride: func(ctx context.Context) (*Manifest, error) {
			return nil, fmt.Errorf("simulated GCS failure")
		},
	}

	_, err := f.EnsureRootfs(context.Background(), stateDir)
	if err == nil {
		t.Fatal("expected error when no cached rootfs exists")
	}
	if !strings.Contains(err.Error(), "no cached rootfs available") {
		t.Errorf("error should mention no cached rootfs, got: %v", err)
	}
}
