package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/mathaix/openclawmachines/backend/pkg/version"
)

// validSHA256 is a proper 64-char hex SHA256 for use in test manifests.
const validSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// fileHash returns the hex SHA256 of content.
func fileHash(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testUpdater builds an Updater wired to temp files and fake GCS for use in
// tests. It returns the updater, the temp dir (for inspection), and a cleanup
// function that restores all package-level vars.
type testHarness struct {
	updater       *Updater
	dir           string
	runningPath   string
	installedPath string

	// Counters for observing side effects.
	restartCalls  atomic.Int32
	downloadCalls atomic.Int32

	// cleanup restores package-level vars.
	cleanup func()
}

func newTestHarness(t *testing.T, manifest *Manifest, binaryContent []byte) *testHarness {
	t.Helper()
	dir := t.TempDir()

	h := &testHarness{
		dir:           dir,
		runningPath:   filepath.Join(dir, "running"),
		installedPath: filepath.Join(dir, "installed"),
	}

	// Save and override package-level vars.
	origRunning := runningBinaryPath
	origInstalled := installedBinaryPath
	origRestart := restartAgent
	origVerify := verifyVersion

	runningBinaryPath = h.runningPath
	installedBinaryPath = h.installedPath
	restartAgent = func() error {
		h.restartCalls.Add(1)
		return nil
	}
	// Stub verifyVersion to always pass (we're not testing real binaries).
	verifyVersion = func(path, expected string) error {
		return nil
	}

	h.cleanup = func() {
		runningBinaryPath = origRunning
		installedBinaryPath = origInstalled
		restartAgent = origRestart
		verifyVersion = origVerify
	}

	h.updater = &Updater{
		logger: discardLogger(),
		fetchManifest: func(ctx context.Context) (*Manifest, error) {
			return manifest, nil
		},
		downloadReader: func(ctx context.Context, url string) (io.ReadCloser, error) {
			h.downloadCalls.Add(1)
			return io.NopCloser(bytes.NewReader(binaryContent)), nil
		},
	}

	return h
}

// --- Tests that exercise real CheckAndUpdate control flow ---

func TestCheckAndUpdate_Case1_FullyCurrent(t *testing.T) {
	binaryContent := []byte("the-current-binary")
	sha := fileHash(binaryContent)

	manifest := &Manifest{
		Version:   "v2.0",
		SHA256:    sha,
		URL:       "gs://example-ocm-artifacts/agent/agent-v2.0",
		SizeBytes: int64(len(binaryContent)),
	}

	h := newTestHarness(t, manifest, binaryContent)
	defer h.cleanup()

	// Both running and installed match manifest.
	if err := os.WriteFile(h.runningPath, binaryContent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.installedPath, binaryContent, 0755); err != nil {
		t.Fatal(err)
	}

	updated, err := h.updater.CheckAndUpdate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated {
		t.Error("expected no update, got updated=true")
	}
	if h.restartCalls.Load() != 0 {
		t.Error("expected no restart")
	}
	if h.downloadCalls.Load() != 0 {
		t.Error("expected no download")
	}
}

func TestCheckAndUpdate_Case2_RepairInstalled(t *testing.T) {
	binaryContent := []byte("the-current-binary")
	sha := fileHash(binaryContent)

	manifest := &Manifest{
		Version:   "v2.0",
		SHA256:    sha,
		URL:       "gs://example-ocm-artifacts/agent/agent-v2.0",
		SizeBytes: int64(len(binaryContent)),
	}

	h := newTestHarness(t, manifest, binaryContent)
	defer h.cleanup()

	// Running matches manifest, but installed is corrupted.
	if err := os.WriteFile(h.runningPath, binaryContent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.installedPath, []byte("corrupted-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	updated, err := h.updater.CheckAndUpdate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated {
		t.Error("expected no restart for repair, got updated=true")
	}
	if h.restartCalls.Load() != 0 {
		t.Error("expected no restart for repair")
	}
	if h.downloadCalls.Load() != 1 {
		t.Errorf("expected 1 download for repair, got %d", h.downloadCalls.Load())
	}

	// Installed binary should now contain the manifest binary.
	got, _ := os.ReadFile(h.installedPath)
	if string(got) != string(binaryContent) {
		t.Errorf("installed binary not repaired: got %q", got)
	}
}

func TestCheckAndUpdate_Case3_RestartNeeded(t *testing.T) {
	newBinary := []byte("the-new-binary-v2")
	sha := fileHash(newBinary)

	manifest := &Manifest{
		Version:   "v2.0",
		SHA256:    sha,
		URL:       "gs://example-ocm-artifacts/agent/agent-v2.0",
		SizeBytes: int64(len(newBinary)),
	}

	h := newTestHarness(t, manifest, newBinary)
	defer h.cleanup()

	// Installed matches manifest, but running is old.
	if err := os.WriteFile(h.runningPath, []byte("old-binary-v1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.installedPath, newBinary, 0755); err != nil {
		t.Fatal(err)
	}

	updated, err := h.updater.CheckAndUpdate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !updated {
		t.Error("expected restart, got updated=false")
	}
	if h.restartCalls.Load() != 1 {
		t.Errorf("expected 1 restart, got %d", h.restartCalls.Load())
	}
	if h.downloadCalls.Load() != 0 {
		t.Error("expected no download — binary already installed")
	}
}

func TestCheckAndUpdate_Case4_FullDownload(t *testing.T) {
	newBinary := []byte("the-new-binary-v2")
	sha := fileHash(newBinary)

	manifest := &Manifest{
		Version:   "v2.0",
		SHA256:    sha,
		URL:       "gs://example-ocm-artifacts/agent/agent-v2.0",
		SizeBytes: int64(len(newBinary)),
	}

	h := newTestHarness(t, manifest, newBinary)
	defer h.cleanup()

	// Neither running nor installed matches.
	if err := os.WriteFile(h.runningPath, []byte("old-binary-v1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.installedPath, []byte("also-old-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	updated, err := h.updater.CheckAndUpdate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !updated {
		t.Error("expected update+restart, got updated=false")
	}
	if h.restartCalls.Load() != 1 {
		t.Errorf("expected 1 restart, got %d", h.restartCalls.Load())
	}
	if h.downloadCalls.Load() != 1 {
		t.Errorf("expected 1 download, got %d", h.downloadCalls.Load())
	}

	// Installed binary should now contain the new binary.
	got, _ := os.ReadFile(h.installedPath)
	if string(got) != string(newBinary) {
		t.Errorf("installed binary not updated: got %q", got)
	}
}

func TestCheckAndUpdate_VerifyFailure_LeavesInstalledUntouched(t *testing.T) {
	newBinary := []byte("the-new-binary-v2")
	sha := fileHash(newBinary)

	manifest := &Manifest{
		Version:   "v2.0",
		SHA256:    sha,
		URL:       "gs://example-ocm-artifacts/agent/agent-v2.0",
		SizeBytes: int64(len(newBinary)),
	}

	h := newTestHarness(t, manifest, newBinary)
	defer h.cleanup()

	// Override verifyVersion to fail.
	verifyVersion = func(path, expected string) error {
		return fmt.Errorf("binary reports wrong version")
	}

	originalInstalled := []byte("original-installed-binary")
	if err := os.WriteFile(h.runningPath, []byte("old-running"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.installedPath, originalInstalled, 0755); err != nil {
		t.Fatal(err)
	}

	updated, err := h.updater.CheckAndUpdate(context.Background())
	if err == nil {
		t.Fatal("expected error from verify failure")
	}
	if updated {
		t.Error("expected no update on verify failure")
	}
	if h.restartCalls.Load() != 0 {
		t.Error("expected no restart on verify failure")
	}

	// Installed binary must be untouched.
	got, _ := os.ReadFile(h.installedPath)
	if string(got) != string(originalInstalled) {
		t.Errorf("installed binary was modified on verify failure: got %q, want %q", got, originalInstalled)
	}
}

func TestCheckAndUpdate_NeverMutatesVersion(t *testing.T) {
	origVersion := version.Version
	defer func() { version.Version = origVersion }()

	version.Version = "old-version-abc"

	binaryContent := []byte("binary-content")
	sha := fileHash(binaryContent)

	manifest := &Manifest{
		Version:   "new-version-xyz",
		SHA256:    sha,
		URL:       "gs://example-ocm-artifacts/agent/agent-new",
		SizeBytes: int64(len(binaryContent)),
	}

	// Test all four cases — none should mutate version.Version.
	cases := []struct {
		name             string
		runningContent   []byte
		installedContent []byte
	}{
		{"case1", binaryContent, binaryContent},
		{"case2_repair", binaryContent, []byte("corrupted")},
		{"case3_restart", []byte("old"), binaryContent},
		{"case4_download", []byte("old"), []byte("also-old")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			version.Version = "old-version-abc"

			h := newTestHarness(t, manifest, binaryContent)
			defer h.cleanup()

			if err := os.WriteFile(h.runningPath, tc.runningContent, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(h.installedPath, tc.installedContent, 0755); err != nil {
				t.Fatal(err)
			}

			_, _ = h.updater.CheckAndUpdate(context.Background())

			if version.Version != "old-version-abc" {
				t.Errorf("version.Version mutated to %q", version.Version)
			}
		})
	}
}

func TestCheckAndUpdate_ConcurrentCallsSerialized(t *testing.T) {
	binaryContent := []byte("binary-content")
	sha := fileHash(binaryContent)

	manifest := &Manifest{
		Version:   "v2.0",
		SHA256:    sha,
		URL:       "gs://example-ocm-artifacts/agent/agent-v2.0",
		SizeBytes: int64(len(binaryContent)),
	}

	h := newTestHarness(t, manifest, binaryContent)
	defer h.cleanup()

	// Both running and installed match — case 1, quick return.
	if err := os.WriteFile(h.runningPath, binaryContent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.installedPath, binaryContent, 0755); err != nil {
		t.Fatal(err)
	}

	// Run many concurrent calls — none should panic or produce errors.
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.updater.CheckAndUpdate(context.Background())
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent CheckAndUpdate error: %v", err)
	}
}

func TestCheckAndUpdate_SHA256Mismatch_LeavesInstalledUntouched(t *testing.T) {
	// Manifest says SHA is X, but download returns content with SHA Y.
	realContent := []byte("real-binary")
	wrongContent := []byte("corrupted-download")

	manifest := &Manifest{
		Version:   "v2.0",
		SHA256:    fileHash(realContent), // expect real content's hash
		URL:       "gs://example-ocm-artifacts/agent/agent-v2.0",
		SizeBytes: int64(len(realContent)),
	}

	h := newTestHarness(t, manifest, wrongContent) // but serve wrong content
	defer h.cleanup()

	originalInstalled := []byte("original-installed")
	if err := os.WriteFile(h.runningPath, []byte("old-running"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.installedPath, originalInstalled, 0755); err != nil {
		t.Fatal(err)
	}

	_, err := h.updater.CheckAndUpdate(context.Background())
	if err == nil {
		t.Fatal("expected SHA256 mismatch error")
	}
	if !strings.Contains(err.Error(), "SHA256 mismatch") {
		t.Errorf("expected SHA256 mismatch in error, got: %v", err)
	}

	// Installed binary must be untouched.
	got, _ := os.ReadFile(h.installedPath)
	if string(got) != string(originalInstalled) {
		t.Errorf("installed binary modified despite SHA256 mismatch")
	}
}

// --- Original helper tests (kept) ---

func TestParseManifest(t *testing.T) {
	t.Setenv("OCM_TRUSTED_ARTIFACT_BUCKETS", "example-ocm-artifacts")
	tests := []struct {
		name    string
		json    string
		wantVer string
		wantErr string
	}{
		{
			name:    "valid manifest",
			json:    `{"version":"abc123","sha256":"` + validSHA256 + `","url":"gs://example-ocm-artifacts/agent/agent-abc123","size_bytes":1024,"built_at":"2026-01-01T00:00:00Z"}`,
			wantVer: "abc123",
		},
		{
			name:    "missing version",
			json:    `{"sha256":"` + validSHA256 + `","url":"gs://example-ocm-artifacts/agent/agent-abc123"}`,
			wantErr: "version",
		},
		{
			name:    "missing sha256",
			json:    `{"version":"abc123","url":"gs://example-ocm-artifacts/agent/agent-abc123"}`,
			wantErr: "sha256",
		},
		{
			name:    "invalid sha256 format - wrong length",
			json:    `{"version":"abc123","sha256":"deadbeef","url":"gs://example-ocm-artifacts/agent/agent-abc123"}`,
			wantErr: "invalid sha256",
		},
		{
			name:    "invalid sha256 format - not hex",
			json:    `{"version":"abc123","sha256":"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz","url":"gs://example-ocm-artifacts/agent/agent-abc123"}`,
			wantErr: "not valid hex",
		},
		{
			name:    "missing url",
			json:    `{"version":"abc123","sha256":"` + validSHA256 + `"}`,
			wantErr: "url",
		},
		{
			name:    "untrusted bucket",
			json:    `{"version":"abc123","sha256":"` + validSHA256 + `","url":"gs://evil-bucket/agent/agent-abc123"}`,
			wantErr: "untrusted bucket",
		},
		{
			name:    "non-gcs url",
			json:    `{"version":"abc123","sha256":"` + validSHA256 + `","url":"https://example.com/agent"}`,
			wantErr: "gs://",
		},
		{
			name:    "invalid json",
			json:    `{not json}`,
			wantErr: "decode",
		},
		{
			name:    "empty json",
			json:    `{}`,
			wantErr: "version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := ParseManifest(strings.NewReader(tt.json))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if m.Version != tt.wantVer {
				t.Errorf("version = %q, want %q", m.Version, tt.wantVer)
			}
		})
	}
}

func TestValidateSHA256(t *testing.T) {
	tests := []struct {
		name    string
		hash    string
		wantErr bool
	}{
		{"valid hash", validSHA256, false},
		{"too short", "deadbeef", true},
		{"too long", validSHA256 + "aa", true},
		{"not hex", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", true},
		{"empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSHA256(tt.hash)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSHA256(%q) error = %v, wantErr %v", tt.hash, err, tt.wantErr)
			}
		})
	}
}

func TestValidateTrustedURL(t *testing.T) {
	t.Setenv("OCM_TRUSTED_ARTIFACT_BUCKETS", "example-ocm-artifacts")
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"trusted bucket", "gs://example-ocm-artifacts/agent/agent-v1", false},
		{"untrusted bucket", "gs://evil-bucket/agent/agent-v1", true},
		{"non-gcs url", "https://example.com/agent", true},
		{"empty url", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTrustedURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTrustedURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestSanitizeVersion(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"normal version", "v1.2.3", "v1.2.3"},
		{"commit hash", "abc123-20260101T120000Z", "abc123-20260101T120000Z"},
		{"path traversal", "../../../etc/passwd", ".._.._.._etc_passwd"},
		{"special chars", "v1.0; rm -rf /", "v1.0__rm_-rf__"},
		{"empty", "", "unknown"},
		{"very long", strings.Repeat("a", 100), strings.Repeat("a", 64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeVersion(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeVersion(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile")
	content := []byte("hello world")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}

	h := sha256.Sum256(content)
	expectedHash := hex.EncodeToString(h[:])

	got, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile() error: %v", err)
	}
	if got != expectedHash {
		t.Errorf("hashFile() = %q, want %q", got, expectedHash)
	}

	_, err = hashFile(filepath.Join(dir, "nonexistent"))
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestAtomicReplace(t *testing.T) {
	t.Run("same filesystem rename", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "src")
		dst := filepath.Join(dir, "dst")

		if err := os.WriteFile(src, []byte("new content"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, []byte("old content"), 0755); err != nil {
			t.Fatal(err)
		}

		if err := atomicReplace(src, dst); err != nil {
			t.Fatalf("atomicReplace failed: %v", err)
		}

		got, _ := os.ReadFile(dst)
		if string(got) != "new content" {
			t.Errorf("dst content = %q, want %q", got, "new content")
		}

		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Error("expected source to be removed after rename")
		}
	})
}

func TestAtomicReplaceMissingSrc(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "nonexistent")
	dst := filepath.Join(dir, "dst")

	err := atomicReplace(src, dst)
	if err == nil {
		t.Fatal("expected error for missing source, got nil")
	}
	if !os.IsNotExist(err) && !strings.Contains(err.Error(), "no such file") {
		if pe, ok := err.(*os.LinkError); ok {
			if pe.Err != syscall.ENOENT {
				t.Errorf("unexpected error type: %v", err)
			}
		}
	}
}
