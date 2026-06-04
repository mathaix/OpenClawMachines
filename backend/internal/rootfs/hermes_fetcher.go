package rootfs

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/klauspost/compress/zstd"
	"github.com/mathaix/openclawmachines/backend/pkg/gcsutil"
)

const (
	defaultHermesEntrypointRelpath = ".venv/bin/hermes"
	defaultHermesVenvRelpath       = ".venv"
)

// HermesConfig holds Hermes artifact distribution settings.
type HermesConfig struct {
	ManifestURI     string
	DownloadTimeout time.Duration
	RetryAttempts   int
}

// HermesRuntimePaths describes the runtime entrypoint and virtualenv paths.
type HermesRuntimePaths struct {
	EntrypointRelpath string `json:"entrypoint_relpath,omitempty"`
	VenvRelpath       string `json:"venv_relpath,omitempty"`
}

// HermesReleaseManifest describes a versioned Hermes runtime artifact.
type HermesReleaseManifest struct {
	SchemaVersion int                `json:"schema_version,omitempty"`
	Kind          string             `json:"kind,omitempty"`
	Version       string             `json:"version"`
	Channel       string             `json:"channel,omitempty"`
	BuiltAt       time.Time          `json:"built_at,omitempty"`
	GitCommit     string             `json:"git_commit,omitempty"`
	ArtifactURL   string             `json:"artifact_url"`
	Compression   string             `json:"compression,omitempty"`
	SizeBytes     int64              `json:"size_bytes,omitempty"`
	SHA256        string             `json:"sha256"`
	Runtime       HermesRuntimePaths `json:"runtime,omitempty"`
}

// ParseHermesReleaseManifest reads and validates a Hermes release manifest.
func ParseHermesReleaseManifest(r io.Reader) (*HermesReleaseManifest, error) {
	var m HermesReleaseManifest
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode hermes release manifest: %w", err)
	}
	if strings.TrimSpace(m.Version) == "" {
		return nil, fmt.Errorf("manifest missing required field: version")
	}
	if err := ValidateOpenClawVersion(m.Version); err != nil {
		return nil, err
	}
	if strings.TrimSpace(m.ArtifactURL) == "" {
		return nil, fmt.Errorf("manifest missing required field: artifact_url")
	}
	if strings.TrimSpace(m.SHA256) == "" {
		return nil, fmt.Errorf("manifest missing required field: sha256")
	}
	if strings.TrimSpace(m.Compression) == "" {
		m.Compression = "zstd"
	}
	if strings.TrimSpace(m.Runtime.EntrypointRelpath) == "" {
		m.Runtime.EntrypointRelpath = defaultHermesEntrypointRelpath
	}
	if strings.TrimSpace(m.Runtime.VenvRelpath) == "" {
		m.Runtime.VenvRelpath = defaultHermesVenvRelpath
	}
	return &m, nil
}

// HermesFetcher downloads and caches versioned Hermes runtimes.
type HermesFetcher struct {
	cfg HermesConfig

	mu     sync.Mutex
	client *storage.Client

	fetchReleaseManifestOverride func(ctx context.Context, version string) (*HermesReleaseManifest, error)
	openURIOverride              func(ctx context.Context, uri string) (io.ReadCloser, error)
}

// NewHermesFetcher creates a Hermes artifact fetcher.
func NewHermesFetcher(_ context.Context, cfg HermesConfig) (*HermesFetcher, error) {
	return &HermesFetcher{cfg: cfg}, nil
}

// Close releases GCS client resources.
func (f *HermesFetcher) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.client == nil {
		return nil
	}
	err := f.client.Close()
	f.client = nil
	return err
}

// EnsureRelease ensures the requested Hermes release is staged under runtimeDir.
func (f *HermesFetcher) EnsureRelease(ctx context.Context, runtimeDir, version string) (string, error) {
	version = strings.TrimSpace(version)
	if err := ValidateOpenClawVersion(version); err != nil {
		return "", err
	}
	runtimeDir = strings.TrimSpace(runtimeDir)
	if runtimeDir == "" {
		return "", fmt.Errorf("hermes runtime dir is required")
	}
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return "", fmt.Errorf("create hermes runtime dir: %w", err)
	}

	releasePath := filepath.Join(runtimeDir, "releases", version)
	if HermesReleaseReady(releasePath) {
		return releasePath, nil
	}

	lock := NewRootfsLock(filepath.Join(runtimeDir, ".hermes.lock"))
	unlock, err := lock.ExclusiveLock()
	if err != nil {
		return "", fmt.Errorf("acquire hermes artifact lock: %w", err)
	}
	defer unlock()

	if HermesReleaseReady(releasePath) {
		return releasePath, nil
	}

	if err := os.MkdirAll(filepath.Join(runtimeDir, "releases"), 0o755); err != nil {
		return "", fmt.Errorf("create hermes releases dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(runtimeDir, "staging"), 0o755); err != nil {
		return "", fmt.Errorf("create hermes staging dir: %w", err)
	}

	attempts := f.cfg.RetryAttempts
	if attempts <= 0 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			delay := time.Duration(attempt) * time.Second
			slog.Info("hermes.runtime.retry", "version", version, "attempt", attempt, "delay", delay)
			if err := sleepWithContext(ctx, delay); err != nil {
				return "", err
			}
		}
		lastErr = f.ensureReleaseOnce(ctx, runtimeDir, version, releasePath)
		if lastErr == nil {
			return releasePath, nil
		}
		slog.Warn("hermes.runtime.stage_failed", "version", version, "attempt", attempt, "error", lastErr)
	}

	return "", fmt.Errorf("stage hermes release %s: %w", version, lastErr)
}

func (f *HermesFetcher) ensureReleaseOnce(ctx context.Context, runtimeDir, version, releasePath string) error {
	manifest, err := f.fetchReleaseManifest(ctx, version)
	if err != nil {
		return fmt.Errorf("fetch release manifest: %w", err)
	}
	if manifest.Version != version {
		return fmt.Errorf("release manifest version mismatch: got %s, want %s", manifest.Version, version)
	}
	if manifest.Compression != "zstd" {
		return fmt.Errorf("unsupported hermes artifact compression: %s", manifest.Compression)
	}

	txnID, err := generateTxnID()
	if err != nil {
		return err
	}
	txnDir := filepath.Join(runtimeDir, "staging", txnID)
	extractDir := filepath.Join(txnDir, "release")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return fmt.Errorf("create hermes staging txn dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(txnDir) }()

	reader, err := f.openURI(ctx, manifest.ArtifactURL)
	if err != nil {
		return fmt.Errorf("open artifact: %w", err)
	}
	defer func() { _ = reader.Close() }()

	if err := extractHermesTarZstd(reader, manifest, extractDir); err != nil {
		return err
	}

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal release manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(extractDir, "manifest.json"), manifestBytes, 0o644); err != nil {
		return fmt.Errorf("write staged release manifest: %w", err)
	}

	if err := os.Rename(extractDir, releasePath); err != nil {
		if HermesReleaseReady(releasePath) {
			return nil
		}
		return fmt.Errorf("publish release %s: %w", version, err)
	}

	slog.Info("hermes.runtime.staged", "version", version, "release_path", releasePath)
	return nil
}

func (f *HermesFetcher) fetchReleaseManifest(ctx context.Context, version string) (*HermesReleaseManifest, error) {
	if f.fetchReleaseManifestOverride != nil {
		return f.fetchReleaseManifestOverride(ctx, version)
	}
	manifestURI, err := f.releaseManifestURI(version)
	if err != nil {
		return nil, err
	}
	reader, err := f.openURI(ctx, manifestURI)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	return ParseHermesReleaseManifest(reader)
}

func (f *HermesFetcher) releaseManifestURI(version string) (string, error) {
	if err := ValidateOpenClawVersion(version); err != nil {
		return "", err
	}
	manifestURI := strings.TrimSpace(f.cfg.ManifestURI)
	if manifestURI == "" {
		return "", fmt.Errorf("hermes manifest URI not configured")
	}
	if strings.Contains(manifestURI, "{version}") {
		return strings.ReplaceAll(manifestURI, "{version}", version), nil
	}
	if strings.HasPrefix(manifestURI, "gs://") {
		bucket, object, err := gcsutil.ParseGCSURI(manifestURI)
		if err != nil {
			return "", err
		}
		dir := pathpkg.Dir(object)
		if dir == "." {
			dir = ""
		}
		if dir == "" {
			return fmt.Sprintf("gs://%s/releases/%s/manifest.json", bucket, version), nil
		}
		return fmt.Sprintf("gs://%s/%s/releases/%s/manifest.json", bucket, strings.Trim(dir, "/"), version), nil
	}
	path, err := localPathFromURI(manifestURI)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "releases", version, "manifest.json"), nil
}

func (f *HermesFetcher) openURI(ctx context.Context, uri string) (io.ReadCloser, error) {
	timeoutCtx, cancel := f.withDownloadTimeout(ctx)
	if f.openURIOverride != nil {
		reader, err := f.openURIOverride(timeoutCtx, uri)
		if err != nil {
			cancel()
			return nil, err
		}
		return &cancelOnCloseReadCloser{ReadCloser: reader, cancel: cancel}, nil
	}
	if strings.HasPrefix(uri, "gs://") {
		client, err := f.ensureClient(timeoutCtx)
		if err != nil {
			cancel()
			return nil, err
		}
		bucket, object, err := gcsutil.ParseGCSURI(uri)
		if err != nil {
			cancel()
			return nil, err
		}
		reader, err := client.Bucket(bucket).Object(object).NewReader(timeoutCtx)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("open GCS object %s: %w", uri, err)
		}
		return &cancelOnCloseReadCloser{ReadCloser: reader, cancel: cancel}, nil
	}
	path, err := localPathFromURI(uri)
	if err != nil {
		cancel()
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open local artifact %s: %w", path, err)
	}
	return &cancelOnCloseReadCloser{ReadCloser: file, cancel: cancel}, nil
}

func (f *HermesFetcher) ensureClient(ctx context.Context) (*storage.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.client != nil {
		return f.client, nil
	}
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create GCS client: %w", err)
	}
	f.client = client
	return client, nil
}

func (f *HermesFetcher) withDownloadTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := f.cfg.DownloadTimeout
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

// HermesReleaseReady reports whether a staged release has the expected files.
func HermesReleaseReady(releasePath string) bool {
	if releasePath == "" {
		return false
	}
	runtimePaths := HermesRuntimePathsForRelease(releasePath)
	entrypoint := filepath.Join(releasePath, runtimePaths.EntrypointRelpath)
	info, err := os.Stat(entrypoint)
	if err != nil || info.IsDir() {
		return false
	}
	venvDir := filepath.Join(releasePath, runtimePaths.VenvRelpath)
	if info, err := os.Stat(venvDir); err != nil || !info.IsDir() {
		return false
	}
	return true
}

func extractHermesTarZstd(src io.Reader, manifest *HermesReleaseManifest, dstDir string) error {
	hasher := sha256.New()
	tee := io.TeeReader(src, hasher)

	zr, err := zstd.NewReader(tee)
	if err != nil {
		return fmt.Errorf("create zstd reader: %w", err)
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	var totalExtracted int64
	extractLimit := maxExtractedBytes
	if manifest != nil && manifest.SizeBytes > 0 && manifest.SizeBytes*10 < extractLimit {
		extractLimit = manifest.SizeBytes * 10
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read hermes artifact tar: %w", err)
		}
		target, err := secureTarPath(dstDir, hdr.Name)
		if err != nil {
			return err
		}
		if target == "" {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("create dir %s: %w", target, err)
			}
		case tar.TypeReg:
			if hdr.Size > maxSingleFileBytes {
				return fmt.Errorf("file %s exceeds max size (%d > %d)", hdr.Name, hdr.Size, maxSingleFileBytes)
			}
			if totalExtracted+hdr.Size > extractLimit {
				return fmt.Errorf("cumulative extracted size exceeds limit (%d bytes)", extractLimit)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create parent dir for %s: %w", target, err)
			}
			if err := ensureParentResolvedWithinRoot(dstDir, target); err != nil {
				return err
			}
			if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing to overwrite symlink %s", target)
			} else if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("lstat %s: %w", target, err)
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("create file %s: %w", target, err)
			}
			written, copyErr := io.Copy(file, io.LimitReader(tr, maxSingleFileBytes+1))
			if copyErr != nil {
				_ = file.Close()
				return fmt.Errorf("write file %s: %w", target, copyErr)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("close file %s: %w", target, err)
			}
			totalExtracted += written
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create parent dir for symlink %s: %w", target, err)
			}
			if err := ensureParentResolvedWithinRoot(dstDir, target); err != nil {
				return err
			}
			if _, err := secureSymlinkTargetPath(dstDir, target, hdr.Linkname); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("create symlink %s -> %s: %w", target, hdr.Linkname, err)
			}
		case tar.TypeLink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create parent dir for hardlink %s: %w", target, err)
			}
			if err := ensureParentResolvedWithinRoot(dstDir, target); err != nil {
				return err
			}
			linkTarget, err := secureTarPath(dstDir, hdr.Linkname)
			if err != nil {
				return err
			}
			if linkTarget == "" {
				return fmt.Errorf("invalid hermes artifact hardlink target %q", hdr.Linkname)
			}
			info, err := os.Lstat(linkTarget)
			if err != nil {
				return fmt.Errorf("stat hardlink target %s: %w", linkTarget, err)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("hardlink target %s is not a regular file", linkTarget)
			}
			if err := os.Link(linkTarget, target); err != nil {
				return fmt.Errorf("create hardlink %s -> %s: %w", target, linkTarget, err)
			}
		default:
			return fmt.Errorf("unsupported hermes artifact entry %s with type %d", hdr.Name, hdr.Typeflag)
		}
	}

	gotHash := hex.EncodeToString(hasher.Sum(nil))
	if gotHash != manifest.SHA256 {
		return fmt.Errorf("hermes artifact SHA256 mismatch: got %s, want %s", gotHash, manifest.SHA256)
	}

	if !HermesReleaseReady(dstDir) {
		return fmt.Errorf("staged hermes artifact is missing entrypoint or venv")
	}
	return nil
}

// HermesRuntimePathsForRelease reads the staged manifest and returns runtime paths.
func HermesRuntimePathsForRelease(releasePath string) HermesRuntimePaths {
	runtimePaths := HermesRuntimePaths{
		EntrypointRelpath: defaultHermesEntrypointRelpath,
		VenvRelpath:       defaultHermesVenvRelpath,
	}
	manifestPath := filepath.Join(releasePath, "manifest.json")
	file, err := os.Open(manifestPath)
	if err != nil {
		return runtimePaths
	}
	defer func() { _ = file.Close() }()

	manifest, err := ParseHermesReleaseManifest(file)
	if err != nil {
		return runtimePaths
	}
	if ep := sanitizeRuntimeRelpath(manifest.Runtime.EntrypointRelpath); ep != "" {
		runtimePaths.EntrypointRelpath = ep
	}
	if venv := sanitizeRuntimeRelpath(manifest.Runtime.VenvRelpath); venv != "" {
		runtimePaths.VenvRelpath = venv
	}
	return runtimePaths
}
