package rootfs

import (
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
	"time"

	"cloud.google.com/go/storage"
	"github.com/klauspost/compress/zstd"
	"github.com/mathaix/openclawmachines/backend/pkg/gcsutil"
)

// Config holds GCS rootfs distribution settings.
type Config struct {
	ManifestURI     string
	DownloadTimeout time.Duration
	RetryAttempts   int
	Prefix          string // filename prefix (default "rootfs", use "browser-rootfs" for browser VM)
}

// Fetcher downloads and caches rootfs images from GCS.
type Fetcher struct {
	client *storage.Client
	cfg    Config

	// fetchManifestOverride is a test hook. If set, it replaces fetchManifest.
	fetchManifestOverride func(ctx context.Context) (*Manifest, error)
}

// NewFetcher creates a GCS-backed rootfs fetcher.
func NewFetcher(ctx context.Context, cfg Config) (*Fetcher, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create GCS client: %w", err)
	}
	return &Fetcher{client: client, cfg: cfg}, nil
}

// EnsureRootfs downloads the rootfs specified by the manifest to stateDir,
// skipping the download if a cached copy with matching SHA256 exists.
// Returns the path to the rootfs file.
func (f *Fetcher) EnsureRootfs(ctx context.Context, stateDir string) (string, error) {
	prefix := f.cfg.Prefix
	if prefix == "" {
		prefix = "rootfs"
	}
	rootfsPath := filepath.Join(stateDir, ".base-"+prefix+".ext4")
	cachePath := filepath.Join(stateDir, "."+prefix+"-manifest.json")

	// Fetch manifest
	fetchFn := f.fetchManifest
	if f.fetchManifestOverride != nil {
		fetchFn = f.fetchManifestOverride
	}
	manifest, err := fetchFn(ctx)
	if err != nil {
		// If manifest fetch fails, fall back to cached rootfs if available
		if _, statErr := os.Stat(rootfsPath); statErr == nil {
			slog.Warn("rootfs.gcs.manifest.fetch_failed_using_cache",
				"error", err,
				"cached_path", rootfsPath)
			return rootfsPath, nil
		}
		return "", fmt.Errorf("fetch manifest (no cached rootfs available): %w", err)
	}
	slog.Info("rootfs.gcs.manifest.fetched",
		"version", manifest.Version,
		"sha256", manifest.SHA256[:min(12, len(manifest.SHA256))]+"...",
		"size_bytes", manifest.SizeBytes)

	// Check cache
	if cached, err := readCachedManifest(cachePath); err == nil {
		if cached.SHA256 == manifest.SHA256 {
			if _, err := os.Stat(rootfsPath); err == nil {
				slog.Info("rootfs.gcs.cache.hit", "version", manifest.Version)
				return rootfsPath, nil
			}
		}
	}

	// Download with retries
	var lastErr error
	for attempt := 1; attempt <= f.cfg.RetryAttempts; attempt++ {
		if attempt > 1 {
			delay := time.Duration(attempt) * 5 * time.Second
			slog.Info("rootfs.gcs.retry", "attempt", attempt, "delay", delay)
			time.Sleep(delay)
		}

		lastErr = f.downloadAndVerify(ctx, manifest, rootfsPath)
		if lastErr == nil {
			// Write cache sidecar
			if err := writeCachedManifest(cachePath, manifest); err != nil {
				slog.Warn("rootfs.gcs.cache.write_failed", "error", err)
			}
			return rootfsPath, nil
		}
		slog.Warn("rootfs.gcs.download.failed", "attempt", attempt, "error", lastErr)
	}

	return "", fmt.Errorf("rootfs download failed after %d attempts: %w", f.cfg.RetryAttempts, lastErr)
}

// EnsureRootfsVersioned downloads the rootfs to a versioned path under baseDir.
// The path format is {baseDir}/releases/{version}.ext4, mirroring the OpenClaw
// runtime release layout. Multiple versions can coexist in the releases directory.
// Returns the path to the rootfs file.
func (f *Fetcher) EnsureRootfsVersioned(ctx context.Context, baseDir string) (string, error) {
	// Fetch manifest to learn the version
	fetchFn := f.fetchManifest
	if f.fetchManifestOverride != nil {
		fetchFn = f.fetchManifestOverride
	}
	manifest, err := fetchFn(ctx)
	if err != nil {
		return "", fmt.Errorf("fetch manifest: %w", err)
	}
	slog.Info("rootfs.gcs.manifest.fetched",
		"version", manifest.Version,
		"sha256", manifest.SHA256[:min(12, len(manifest.SHA256))]+"...",
		"size_bytes", manifest.SizeBytes)

	// Versioned release path
	releaseDir := filepath.Join(baseDir, "releases")
	if err := os.MkdirAll(releaseDir, 0755); err != nil {
		return "", fmt.Errorf("create release dir: %w", err)
	}
	rootfsPath := filepath.Join(releaseDir, manifest.Version+".ext4")
	sidecarPath := rootfsPath + ".manifest.json"

	// Check if already staged — file exists and sidecar matches
	if _, err := os.Stat(rootfsPath); err == nil {
		if cached, cacheErr := readCachedManifest(sidecarPath); cacheErr == nil && cached.SHA256 == manifest.SHA256 {
			slog.Info("rootfs.gcs.versioned.cache.hit", "version", manifest.Version, "path", rootfsPath)
			return rootfsPath, nil
		}
	}

	// Download with retries
	var lastErr error
	for attempt := 1; attempt <= f.cfg.RetryAttempts; attempt++ {
		if attempt > 1 {
			delay := time.Duration(attempt) * 5 * time.Second
			slog.Info("rootfs.gcs.retry", "attempt", attempt, "delay", delay)
			time.Sleep(delay)
		}
		lastErr = f.downloadAndVerify(ctx, manifest, rootfsPath)
		if lastErr == nil {
			if err := writeCachedManifest(sidecarPath, manifest); err != nil {
				slog.Warn("rootfs.gcs.versioned.sidecar.write_failed", "error", err)
			}
			return rootfsPath, nil
		}
		slog.Warn("rootfs.gcs.versioned.download.failed", "attempt", attempt, "error", lastErr)
	}
	return "", fmt.Errorf("rootfs download failed after %d attempts: %w", f.cfg.RetryAttempts, lastErr)
}

// EnsureRootfsRelease downloads a specific rootfs version to a versioned path
// under baseDir. The path format is {baseDir}/releases/{version}.ext4.
func (f *Fetcher) EnsureRootfsRelease(ctx context.Context, baseDir, version string) (string, error) {
	manifest, err := f.fetchReleaseManifest(ctx, version)
	if err != nil {
		return "", fmt.Errorf("fetch release manifest: %w", err)
	}
	slog.Info("rootfs.gcs.release_manifest.fetched",
		"version", manifest.Version,
		"sha256", manifest.SHA256[:min(12, len(manifest.SHA256))]+"...",
		"size_bytes", manifest.SizeBytes)

	releaseDir := filepath.Join(baseDir, "releases")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		return "", fmt.Errorf("create release dir: %w", err)
	}
	rootfsPath := filepath.Join(releaseDir, manifest.Version+".ext4")
	sidecarPath := rootfsPath + ".manifest.json"

	if _, err := os.Stat(rootfsPath); err == nil {
		if cached, cacheErr := readCachedManifest(sidecarPath); cacheErr == nil && cached.SHA256 == manifest.SHA256 {
			slog.Info("rootfs.gcs.release.cache.hit", "version", manifest.Version, "path", rootfsPath)
			return rootfsPath, nil
		}
	}

	var lastErr error
	for attempt := 1; attempt <= f.cfg.RetryAttempts; attempt++ {
		if attempt > 1 {
			delay := time.Duration(attempt) * 5 * time.Second
			slog.Info("rootfs.gcs.retry", "attempt", attempt, "delay", delay)
			time.Sleep(delay)
		}
		lastErr = f.downloadAndVerify(ctx, manifest, rootfsPath)
		if lastErr == nil {
			if err := writeCachedManifest(sidecarPath, manifest); err != nil {
				slog.Warn("rootfs.gcs.release.sidecar.write_failed", "error", err)
			}
			return rootfsPath, nil
		}
		slog.Warn("rootfs.gcs.release.download.failed", "attempt", attempt, "error", lastErr)
	}
	return "", fmt.Errorf("rootfs release download failed after %d attempts: %w", f.cfg.RetryAttempts, lastErr)
}

// FetchManifest returns the current manifest without downloading the rootfs.
// Used by callers that need to resolve the version before deciding to download.
func (f *Fetcher) FetchManifest(ctx context.Context) (*Manifest, error) {
	fetchFn := f.fetchManifest
	if f.fetchManifestOverride != nil {
		fetchFn = f.fetchManifestOverride
	}
	return fetchFn(ctx)
}

func (f *Fetcher) fetchReleaseManifest(ctx context.Context, version string) (*Manifest, error) {
	manifestURI, err := f.releaseManifestURI(version)
	if err != nil {
		return nil, err
	}
	bucket, object, err := gcsutil.ParseGCSURI(manifestURI)
	if err == nil {
		r, err := f.client.Bucket(bucket).Object(object).NewReader(ctx)
		if err != nil {
			return nil, fmt.Errorf("open rootfs release manifest object: %w", err)
		}
		defer func() { _ = r.Close() }()
		return ParseManifest(r)
	}

	path, pathErr := localPathFromURI(manifestURI)
	if pathErr != nil {
		return nil, pathErr
	}
	file, fileErr := os.Open(path)
	if fileErr != nil {
		return nil, fmt.Errorf("open rootfs release manifest file: %w", fileErr)
	}
	defer func() { _ = file.Close() }()
	return ParseManifest(file)
}

func (f *Fetcher) releaseManifestURI(version string) (string, error) {
	if version == "" {
		return "", fmt.Errorf("rootfs version is required")
	}
	manifestURI := f.cfg.ManifestURI
	if manifestURI == "" {
		return "", fmt.Errorf("rootfs manifest URI not configured")
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
			return fmt.Sprintf("gs://%s/manifest-%s.json", bucket, version), nil
		}
		return fmt.Sprintf("gs://%s/%s/manifest-%s.json", bucket, strings.Trim(dir, "/"), version), nil
	}

	path, err := localPathFromURI(manifestURI)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "manifest-"+version+".json"), nil
}

// Close releases the GCS client resources.
func (f *Fetcher) Close() error {
	return f.client.Close()
}

func (f *Fetcher) fetchManifest(ctx context.Context) (*Manifest, error) {
	bucket, object, err := gcsutil.ParseGCSURI(f.cfg.ManifestURI)
	if err == nil {
		r, err := f.client.Bucket(bucket).Object(object).NewReader(ctx)
		if err != nil {
			return nil, fmt.Errorf("open manifest object: %w", err)
		}
		defer func() { _ = r.Close() }()
		return ParseManifest(r)
	}

	path, pathErr := localPathFromURI(f.cfg.ManifestURI)
	if pathErr != nil {
		return nil, pathErr
	}
	file, fileErr := os.Open(path)
	if fileErr != nil {
		return nil, fmt.Errorf("open manifest file: %w", fileErr)
	}
	defer func() { _ = file.Close() }()
	return ParseManifest(file)
}

func (f *Fetcher) downloadAndVerify(ctx context.Context, manifest *Manifest, dstPath string) error {
	dlCtx, cancel := context.WithTimeout(ctx, f.cfg.DownloadTimeout)
	defer cancel()

	slog.Info("rootfs.gcs.download.starting",
		"url", manifest.URL,
		"compressed_size_bytes", manifest.CompressedSizeBytes)
	start := time.Now()

	var r io.ReadCloser
	bucket, object, err := gcsutil.ParseGCSURI(manifest.URL)
	if err == nil {
		r, err = f.client.Bucket(bucket).Object(object).NewReader(dlCtx)
		if err != nil {
			return fmt.Errorf("open rootfs object: %w", err)
		}
	} else {
		path, pathErr := localPathFromURI(manifest.URL)
		if pathErr != nil {
			return pathErr
		}
		r, err = os.Open(path)
		if err != nil {
			return fmt.Errorf("open rootfs file: %w", err)
		}
	}
	defer func() { _ = r.Close() }()

	// Decompress + hash in one pass
	zr, err := zstd.NewReader(r)
	if err != nil {
		return fmt.Errorf("create zstd reader: %w", err)
	}
	defer zr.Close()

	tmpFile, tmpPath, err := createRootfsTempFile(dstPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath) // clean up on error; no-op after successful rename
	}()

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmpFile, hasher), zr)
	if err != nil {
		return fmt.Errorf("decompress rootfs: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	// Verify SHA256
	gotHash := hex.EncodeToString(hasher.Sum(nil))
	if gotHash != manifest.SHA256 {
		return fmt.Errorf("SHA256 mismatch: got %s, want %s", gotHash, manifest.SHA256)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, dstPath); err != nil {
		return fmt.Errorf("rename rootfs: %w", err)
	}

	slog.Info("rootfs.gcs.download.completed",
		"version", manifest.Version,
		"size_bytes", written,
		"duration_ms", time.Since(start).Milliseconds())
	return nil
}

// ReadCachedManifest reads a cached manifest sidecar file from disk.
// Used by the agent heartbeat to report which rootfs version is staged.
func ReadCachedManifest(path string) (*Manifest, error) {
	return readCachedManifest(path)
}

func readCachedManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// createRootfsTempFile returns a unique temp file in the destination
// directory, suitable for atomic rename onto dstPath once the download +
// hash verify complete. The unique name is load-bearing: a fixed
// "<dstPath>.tmp" suffix would let two concurrent EnsureRootfs calls for
// the same version clobber each other mid-stream, producing a corrupt blob
// that still passed SHA verification because whichever writer finished
// last won the hash comparison.
func createRootfsTempFile(dstPath string) (*os.File, string, error) {
	tmpFile, err := os.CreateTemp(filepath.Dir(dstPath), filepath.Base(dstPath)+".tmp.*")
	if err != nil {
		return nil, "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Chmod(0o644); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return nil, "", fmt.Errorf("chmod temp file: %w", err)
	}
	return tmpFile, tmpPath, nil
}

func writeCachedManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
