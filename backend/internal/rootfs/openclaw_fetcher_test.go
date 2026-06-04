package rootfs

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestParseOpenClawReleaseManifest_Valid(t *testing.T) {
	input := `{
		"version": "v2026.4.5",
		"artifact_url": "gs://example-ocm-artifacts/openclaw/releases/v2026.4.5/openclaw-v2026.4.5-linux-amd64.tar.zst",
		"sha256": "abc123",
		"runtime": {
			"entrypoint_relpath": "bin/openclaw",
			"bundled_plugins_relpath": "dist/extensions"
		}
	}`
	manifest, err := ParseOpenClawReleaseManifest(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse release manifest: %v", err)
	}
	if manifest.Version != "v2026.4.5" {
		t.Fatalf("version = %q, want v2026.4.5", manifest.Version)
	}
	if manifest.Compression != "zstd" {
		t.Fatalf("compression = %q, want zstd", manifest.Compression)
	}
	if manifest.Runtime.EntrypointRelpath != "bin/openclaw" {
		t.Fatalf("entrypoint_relpath = %q", manifest.Runtime.EntrypointRelpath)
	}
}

func TestEnsureOpenClawRelease_ExtractsLocalArtifact(t *testing.T) {
	stateDir := t.TempDir()
	runtimeDir := filepath.Join(stateDir, "runtime")
	manifestRoot := filepath.Join(stateDir, "manifests")
	version := "v2026.4.5"

	artifactPath, sha := writeTestOpenClawArtifact(t, filepath.Join(manifestRoot, "releases", version), version, map[string]string{
		"bin/openclaw":             "#!/bin/sh\nexit 0\n",
		"dist/extensions/test.txt": "ok\n",
	})
	writeTestOpenClawManifest(t, filepath.Join(manifestRoot, "releases", version, "manifest.json"), version, artifactPath, sha)
	if err := os.WriteFile(filepath.Join(manifestRoot, "manifest-stable.json"), []byte(`{"current_version":"`+version+`"}`), 0644); err != nil {
		t.Fatalf("write channel manifest: %v", err)
	}

	fetcher, err := NewOpenClawFetcher(context.Background(), OpenClawConfig{
		ManifestURI:     filepath.Join(manifestRoot, "manifest-stable.json"),
		DownloadTimeout: 5 * time.Second,
		RetryAttempts:   1,
	})
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	defer func() { _ = fetcher.Close() }()

	releasePath, err := fetcher.EnsureRelease(context.Background(), runtimeDir, version)
	if err != nil {
		t.Fatalf("ensure release: %v", err)
	}
	if !openClawReleaseReady(releasePath) {
		t.Fatalf("staged release at %s is not ready", releasePath)
	}
	if _, err := os.Stat(filepath.Join(releasePath, "manifest.json")); err != nil {
		t.Fatalf("expected staged manifest.json: %v", err)
	}
}

func TestEnsureOpenClawRelease_VerifyChecksum(t *testing.T) {
	stateDir := t.TempDir()
	runtimeDir := filepath.Join(stateDir, "runtime")
	manifestRoot := filepath.Join(stateDir, "manifests")
	version := "v2026.4.6"

	artifactPath, _ := writeTestOpenClawArtifact(t, filepath.Join(manifestRoot, "releases", version), version, map[string]string{
		"bin/openclaw":             "#!/bin/sh\nexit 0\n",
		"dist/extensions/test.txt": "ok\n",
	})
	writeTestOpenClawManifest(t, filepath.Join(manifestRoot, "releases", version, "manifest.json"), version, artifactPath, strings.Repeat("0", 64))
	if err := os.WriteFile(filepath.Join(manifestRoot, "manifest-stable.json"), []byte(`{"current_version":"`+version+`"}`), 0644); err != nil {
		t.Fatalf("write channel manifest: %v", err)
	}

	fetcher, err := NewOpenClawFetcher(context.Background(), OpenClawConfig{
		ManifestURI:     filepath.Join(manifestRoot, "manifest-stable.json"),
		DownloadTimeout: 5 * time.Second,
		RetryAttempts:   1,
	})
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	defer func() { _ = fetcher.Close() }()

	_, err = fetcher.EnsureRelease(context.Background(), runtimeDir, version)
	if err == nil {
		t.Fatal("expected checksum verification error")
	}
	if !strings.Contains(err.Error(), "SHA256 mismatch") {
		t.Fatalf("expected SHA256 mismatch error, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(runtimeDir, "releases", version)); !os.IsNotExist(statErr) {
		t.Fatalf("release dir should not exist after checksum failure, stat err = %v", statErr)
	}
}

func TestEnsureOpenClawRelease_HonorsDownloadTimeout(t *testing.T) {
	const version = "v2026.4.7"

	fetcher, err := NewOpenClawFetcher(context.Background(), OpenClawConfig{
		DownloadTimeout: 20 * time.Millisecond,
		RetryAttempts:   1,
	})
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	defer func() { _ = fetcher.Close() }()

	fetcher.fetchReleaseManifestOverride = func(context.Context, string) (*OpenClawReleaseManifest, error) {
		return &OpenClawReleaseManifest{
			Version:     version,
			ArtifactURL: "test://artifact",
			Compression: "zstd",
			SHA256:      strings.Repeat("a", 64),
			Runtime: OpenClawRuntimePaths{
				EntrypointRelpath:     "bin/openclaw",
				BundledPluginsRelpath: "dist/extensions",
			},
		}, nil
	}
	fetcher.openURIOverride = func(ctx context.Context, _ string) (io.ReadCloser, error) {
		return readCloserFunc{
			read: func(_ []byte) (int, error) {
				<-ctx.Done()
				return 0, ctx.Err()
			},
			close: func() error { return nil },
		}, nil
	}

	_, err = fetcher.EnsureRelease(context.Background(), t.TempDir(), version)
	if err == nil {
		t.Fatal("expected download timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got: %v", err)
	}
}

func TestValidateOpenClawVersion_RejectsTraversal(t *testing.T) {
	for _, version := range []string{"../escape", "..", "openclaw/2026.04.05"} {
		if err := ValidateOpenClawVersion(version); err == nil {
			t.Fatalf("expected %q to be rejected", version)
		}
	}
	if err := ValidateOpenClawVersion("openclaw-2026.04.05"); err != nil {
		t.Fatalf("expected valid version to pass, got: %v", err)
	}
}

func TestExtractOpenClawTarZstd_RejectsEscapingSymlink(t *testing.T) {
	var compressed bytes.Buffer
	writer, err := zstd.NewWriter(&compressed)
	if err != nil {
		t.Fatalf("create zstd writer: %v", err)
	}
	tw := tar.NewWriter(writer)
	writeHeader := func(hdr *tar.Header) {
		t.Helper()
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %s: %v", hdr.Name, err)
		}
	}

	writeHeader(&tar.Header{Name: "bin", Typeflag: tar.TypeSymlink, Linkname: "/tmp"})
	writeHeader(&tar.Header{Name: "bin/openclaw", Mode: 0755, Size: int64(len("#!/bin/sh\n"))})
	if _, err := tw.Write([]byte("#!/bin/sh\n")); err != nil {
		t.Fatalf("write tar file: %v", err)
	}
	writeHeader(&tar.Header{Name: "dist/extensions/test.txt", Mode: 0644, Size: int64(len("ok"))})
	if _, err := tw.Write([]byte("ok")); err != nil {
		t.Fatalf("write tar file: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zstd writer: %v", err)
	}

	err = extractOpenClawTarZstd(bytes.NewReader(compressed.Bytes()), &OpenClawReleaseManifest{
		Version:     "v2026.4.8",
		ArtifactURL: "file:///tmp/openclaw.tar.zst",
		Compression: "zstd",
		SHA256:      strings.Repeat("a", 64),
		Runtime: OpenClawRuntimePaths{
			EntrypointRelpath:     "bin/openclaw",
			BundledPluginsRelpath: "dist/extensions",
		},
	}, t.TempDir())
	if err == nil {
		t.Fatal("expected escaping symlink to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid openclaw artifact symlink target") {
		t.Fatalf("expected symlink target error, got: %v", err)
	}
}

type readCloserFunc struct {
	read  func([]byte) (int, error)
	close func() error
}

func (r readCloserFunc) Read(p []byte) (int, error) { return r.read(p) }
func (r readCloserFunc) Close() error               { return r.close() }

func writeTestOpenClawArtifact(t *testing.T, dir, version string, files map[string]string) (string, string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("create artifact dir: %v", err)
	}
	artifactPath := filepath.Join(dir, "openclaw-"+version+"-linux-amd64.tar.zst")
	file, err := os.Create(artifactPath)
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	defer func() { _ = file.Close() }()

	hasher := sha256.New()
	writer, err := zstd.NewWriter(file)
	if err != nil {
		t.Fatalf("create zstd writer: %v", err)
	}
	tw := tar.NewWriter(writer)

	for name, contents := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(contents)),
		}
		if strings.HasPrefix(name, "bin/") {
			hdr.Mode = 0755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(contents)); err != nil {
			t.Fatalf("write tar file %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zstd writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close artifact file: %v", err)
	}

	data, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read artifact for checksum: %v", err)
	}
	hasher.Write(data)
	return artifactPath, hex.EncodeToString(hasher.Sum(nil))
}

func writeTestOpenClawManifest(t *testing.T, path, version, artifactPath, sha string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create manifest dir: %v", err)
	}
	manifest := `{
		"schema_version": 1,
		"kind": "openclaw-runtime",
		"version": "` + version + `",
		"artifact_url": "` + artifactPath + `",
		"compression": "zstd",
		"sha256": "` + sha + `",
		"runtime": {
			"entrypoint_relpath": "bin/openclaw",
			"bundled_plugins_relpath": "dist/extensions"
		}
	}`
	if err := os.WriteFile(path, []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}
