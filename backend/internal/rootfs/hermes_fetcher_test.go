package rootfs

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestEnsureHermesRelease_ExtractsLocalArtifactWithHardlinks(t *testing.T) {
	stateDir := t.TempDir()
	runtimeDir := filepath.Join(stateDir, "runtime")
	manifestRoot := filepath.Join(stateDir, "manifests")
	version := "hermes-test-1"

	artifactPath, sha := writeTestHermesArtifact(t, filepath.Join(manifestRoot, "releases", version))
	writeTestHermesManifest(t, filepath.Join(manifestRoot, "releases", version, "manifest.json"), version, artifactPath, sha)

	fetcher, err := NewHermesFetcher(context.Background(), HermesConfig{
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
	if !HermesReleaseReady(releasePath) {
		t.Fatalf("staged release at %s is not ready", releasePath)
	}

	sourceInfo, err := os.Stat(filepath.Join(releasePath, "web/node_modules/esbuild/bin/esbuild"))
	if err != nil {
		t.Fatalf("stat hardlink source: %v", err)
	}
	targetInfo, err := os.Stat(filepath.Join(releasePath, "web/node_modules/.bin/esbuild"))
	if err != nil {
		t.Fatalf("stat hardlink target: %v", err)
	}
	if !os.SameFile(sourceInfo, targetInfo) {
		t.Fatal("expected extracted esbuild entries to be hardlinks to the same file")
	}
}

func writeTestHermesArtifact(t *testing.T, dir string) (string, string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create artifact dir: %v", err)
	}

	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	entries := []struct {
		name string
		mode int64
	}{
		{".venv/", 0o755},
		{".venv/bin/", 0o755},
		{"web/", 0o755},
		{"web/node_modules/", 0o755},
		{"web/node_modules/.bin/", 0o755},
		{"web/node_modules/esbuild/", 0o755},
		{"web/node_modules/esbuild/bin/", 0o755},
	}
	for _, entry := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Typeflag: tar.TypeDir, Mode: entry.mode}); err != nil {
			t.Fatalf("write dir header %s: %v", entry.name, err)
		}
	}
	writeTarFile(t, tw, ".venv/bin/hermes", []byte("#!/bin/sh\nexit 0\n"), 0o755)
	writeTarFile(t, tw, "web/node_modules/esbuild/bin/esbuild", []byte("#!/bin/sh\nexit 0\n"), 0o755)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "web/node_modules/.bin/esbuild",
		Typeflag: tar.TypeLink,
		Linkname: "web/node_modules/esbuild/bin/esbuild",
		Mode:     0o755,
	}); err != nil {
		t.Fatalf("write hardlink header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}

	var zstdBuf bytes.Buffer
	zw, err := zstd.NewWriter(&zstdBuf)
	if err != nil {
		t.Fatalf("create zstd writer: %v", err)
	}
	if _, err := zw.Write(tarBuf.Bytes()); err != nil {
		t.Fatalf("compress artifact: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zstd writer: %v", err)
	}

	artifactPath := filepath.Join(dir, "hermes-test.tar.zst")
	if err := os.WriteFile(artifactPath, zstdBuf.Bytes(), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	sum := sha256.Sum256(zstdBuf.Bytes())
	return artifactPath, hex.EncodeToString(sum[:])
}

func writeTarFile(t *testing.T, tw *tar.Writer, name string, content []byte, mode int64) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Typeflag: tar.TypeReg,
		Mode:     mode,
		Size:     int64(len(content)),
	}); err != nil {
		t.Fatalf("write file header %s: %v", name, err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write file %s: %v", name, err)
	}
}

func writeTestHermesManifest(t *testing.T, path, version, artifactPath, sha string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create manifest dir: %v", err)
	}
	info, err := os.Stat(artifactPath)
	if err != nil {
		t.Fatalf("stat artifact: %v", err)
	}
	manifest := fmt.Sprintf(`{
		"version": %q,
		"artifact_url": %q,
		"compression": "zstd",
		"size_bytes": %d,
		"sha256": %q,
		"runtime": {
			"entrypoint_relpath": ".venv/bin/hermes",
			"venv_relpath": ".venv"
		}
	}`, version, "file://"+artifactPath, info.Size(), sha)
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}
