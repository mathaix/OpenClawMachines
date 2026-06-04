package rootfs

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/klauspost/compress/zstd"
	"github.com/mathaix/openclawmachines/backend/pkg/gcsutil"
)

const (
	defaultOpenClawEntrypointRelpath = "bin/openclaw"
	defaultOpenClawPluginsRelpath    = "dist/extensions"

	// maxExtractedBytes is a hard cap on total extracted bytes from a single
	// OpenClaw artifact tar to prevent zip-bomb denial-of-service. 2 GiB is
	// generous — a typical OpenClaw release is ~200 MiB extracted.
	maxExtractedBytes int64 = 2 << 30

	// maxSingleFileBytes caps any individual file within the archive.
	maxSingleFileBytes int64 = 512 << 20
)

var openClawVersionRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,126}[A-Za-z0-9])?$`)

// OpenClawConfig holds OpenClaw artifact distribution settings.
type OpenClawConfig struct {
	ManifestURI     string
	DownloadTimeout time.Duration
	RetryAttempts   int
}

// OpenClawRuntimePaths describes the runtime entrypoint and bundled plugins.
type OpenClawRuntimePaths struct {
	EntrypointRelpath     string `json:"entrypoint_relpath,omitempty"`
	BundledPluginsRelpath string `json:"bundled_plugins_relpath,omitempty"`
}

// OpenClawReleaseManifest describes a versioned OpenClaw runtime artifact.
type OpenClawReleaseManifest struct {
	SchemaVersion int                  `json:"schema_version,omitempty"`
	Kind          string               `json:"kind,omitempty"`
	Version       string               `json:"version"`
	Channel       string               `json:"channel,omitempty"`
	BuiltAt       time.Time            `json:"built_at,omitempty"`
	GitCommit     string               `json:"git_commit,omitempty"`
	ArtifactURL   string               `json:"artifact_url"`
	Compression   string               `json:"compression,omitempty"`
	SizeBytes     int64                `json:"size_bytes,omitempty"`
	SHA256        string               `json:"sha256"`
	Runtime       OpenClawRuntimePaths `json:"runtime,omitempty"`
}

// ParseOpenClawReleaseManifest reads and validates a release manifest.
func ParseOpenClawReleaseManifest(r io.Reader) (*OpenClawReleaseManifest, error) {
	var m OpenClawReleaseManifest
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode openclaw release manifest: %w", err)
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
		m.Runtime.EntrypointRelpath = defaultOpenClawEntrypointRelpath
	}
	if strings.TrimSpace(m.Runtime.BundledPluginsRelpath) == "" {
		m.Runtime.BundledPluginsRelpath = defaultOpenClawPluginsRelpath
	}
	return &m, nil
}

// ValidateOpenClawVersion rejects values that could escape host/runtime paths.
func ValidateOpenClawVersion(version string) error {
	version = strings.TrimSpace(version)
	switch {
	case version == "":
		return fmt.Errorf("openclaw version is required")
	case strings.Contains(version, ".."):
		return fmt.Errorf("invalid openclaw version %q", version)
	case !openClawVersionRe.MatchString(version):
		return fmt.Errorf("invalid openclaw version %q", version)
	default:
		return nil
	}
}

// OpenClawFetcher downloads and caches versioned OpenClaw runtimes.
type OpenClawFetcher struct {
	cfg OpenClawConfig

	mu     sync.Mutex
	client *storage.Client

	fetchReleaseManifestOverride func(ctx context.Context, version string) (*OpenClawReleaseManifest, error)
	openURIOverride              func(ctx context.Context, uri string) (io.ReadCloser, error)
}

type cancelOnCloseReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelOnCloseReadCloser) Close() error {
	err := r.ReadCloser.Close()
	if r.cancel != nil {
		r.cancel()
	}
	return err
}

// NewOpenClawFetcher creates an OpenClaw artifact fetcher.
func NewOpenClawFetcher(_ context.Context, cfg OpenClawConfig) (*OpenClawFetcher, error) {
	return &OpenClawFetcher{cfg: cfg}, nil
}

// Close releases the GCS client resources.
func (f *OpenClawFetcher) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.client == nil {
		return nil
	}
	err := f.client.Close()
	f.client = nil
	return err
}

// EnsureRelease ensures the requested OpenClaw release is staged under runtimeDir.
func (f *OpenClawFetcher) EnsureRelease(ctx context.Context, runtimeDir, version string) (string, error) {
	version = strings.TrimSpace(version)
	if err := ValidateOpenClawVersion(version); err != nil {
		return "", err
	}
	runtimeDir = strings.TrimSpace(runtimeDir)
	if runtimeDir == "" {
		return "", fmt.Errorf("openclaw runtime dir is required")
	}
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		return "", fmt.Errorf("create openclaw runtime dir: %w", err)
	}

	releasePath := filepath.Join(runtimeDir, "releases", version)
	if openClawReleaseReady(releasePath) {
		return releasePath, nil
	}

	lock := NewRootfsLock(filepath.Join(runtimeDir, ".openclaw.lock"))
	unlock, err := lock.ExclusiveLock()
	if err != nil {
		return "", fmt.Errorf("acquire openclaw artifact lock: %w", err)
	}
	defer unlock()

	if openClawReleaseReady(releasePath) {
		return releasePath, nil
	}

	if err := os.MkdirAll(filepath.Join(runtimeDir, "releases"), 0755); err != nil {
		return "", fmt.Errorf("create openclaw releases dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(runtimeDir, "staging"), 0755); err != nil {
		return "", fmt.Errorf("create openclaw staging dir: %w", err)
	}

	attempts := f.cfg.RetryAttempts
	if attempts <= 0 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			delay := time.Duration(attempt) * time.Second
			slog.Info("openclaw.runtime.retry", "version", version, "attempt", attempt, "delay", delay)
			if err := sleepWithContext(ctx, delay); err != nil {
				return "", err
			}
		}

		lastErr = f.ensureReleaseOnce(ctx, runtimeDir, version, releasePath)
		if lastErr == nil {
			return releasePath, nil
		}
		slog.Warn("openclaw.runtime.stage_failed", "version", version, "attempt", attempt, "error", lastErr)
	}

	return "", fmt.Errorf("stage openclaw release %s: %w", version, lastErr)
}

func (f *OpenClawFetcher) ensureReleaseOnce(ctx context.Context, runtimeDir, version, releasePath string) error {
	manifest, err := f.fetchReleaseManifest(ctx, version)
	if err != nil {
		return fmt.Errorf("fetch release manifest: %w", err)
	}
	if manifest.Version != version {
		return fmt.Errorf("release manifest version mismatch: got %s, want %s", manifest.Version, version)
	}
	if manifest.Compression != "zstd" {
		return fmt.Errorf("unsupported openclaw artifact compression: %s", manifest.Compression)
	}

	txnID, err := generateTxnID()
	if err != nil {
		return err
	}
	txnDir := filepath.Join(runtimeDir, "staging", txnID)
	extractDir := filepath.Join(txnDir, "release")
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		return fmt.Errorf("create openclaw staging txn dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(txnDir) }()

	reader, err := f.openURI(ctx, manifest.ArtifactURL)
	if err != nil {
		return fmt.Errorf("open artifact: %w", err)
	}
	defer func() { _ = reader.Close() }()

	if err := extractOpenClawTarZstd(reader, manifest, extractDir); err != nil {
		return err
	}

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal release manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(extractDir, "manifest.json"), manifestBytes, 0644); err != nil {
		return fmt.Errorf("write staged release manifest: %w", err)
	}

	if err := os.Rename(extractDir, releasePath); err != nil {
		if openClawReleaseReady(releasePath) {
			return nil
		}
		return fmt.Errorf("publish release %s: %w", version, err)
	}

	slog.Info("openclaw.runtime.staged", "version", version, "release_path", releasePath)
	return nil
}

func (f *OpenClawFetcher) fetchReleaseManifest(ctx context.Context, version string) (*OpenClawReleaseManifest, error) {
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
	return ParseOpenClawReleaseManifest(reader)
}

func (f *OpenClawFetcher) releaseManifestURI(version string) (string, error) {
	if err := ValidateOpenClawVersion(version); err != nil {
		return "", err
	}
	manifestURI := strings.TrimSpace(f.cfg.ManifestURI)
	if manifestURI == "" {
		return "", fmt.Errorf("openclaw manifest URI not configured")
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

func (f *OpenClawFetcher) openURI(ctx context.Context, uri string) (io.ReadCloser, error) {
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

func (f *OpenClawFetcher) ensureClient(ctx context.Context) (*storage.Client, error) {
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

func (f *OpenClawFetcher) withDownloadTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := f.cfg.DownloadTimeout
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func openClawReleaseReady(releasePath string) bool {
	if releasePath == "" {
		return false
	}
	runtimePaths := RuntimePathsForRelease(releasePath)
	entrypoint := filepath.Join(releasePath, runtimePaths.EntrypointRelpath)
	info, err := os.Stat(entrypoint)
	if err != nil || info.IsDir() {
		return false
	}
	pluginsDir := filepath.Join(releasePath, runtimePaths.BundledPluginsRelpath)
	if info, err := os.Stat(pluginsDir); err != nil || !info.IsDir() {
		return false
	}
	return true
}

func extractOpenClawTarZstd(src io.Reader, manifest *OpenClawReleaseManifest, dstDir string) error {
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
		// 10x multiplier accounts for zstd decompression + hard links extracted
		// as independent copies (pnpm artifacts have many hard links).
		extractLimit = manifest.SizeBytes * 10
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read openclaw artifact tar: %w", err)
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
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("create parent dir for %s: %w", target, err)
			}
			if err := ensureParentResolvedWithinRoot(dstDir, target); err != nil {
				return err
			}
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
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
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
			if written > maxSingleFileBytes {
				_ = file.Close()
				return fmt.Errorf("file %s exceeded declared size during extraction", hdr.Name)
			}
			totalExtracted += written
			if err := file.Close(); err != nil {
				return fmt.Errorf("close file %s: %w", target, err)
			}
		case tar.TypeLink:
			// Hard link: resolve the link target within the extraction root
			// and copy the file instead of creating an actual hard link.
			// This avoids nlink>1 which OpenClaw's openBoundaryFileSync rejects.
			linkTarget := filepath.Join(dstDir, filepath.Clean(hdr.Linkname))
			if err := ensureParentResolvedWithinRoot(dstDir, linkTarget); err != nil {
				return fmt.Errorf("hard link target %s escapes root: %w", hdr.Linkname, err)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("create parent dir for hard link %s: %w", target, err)
			}
			if err := ensureParentResolvedWithinRoot(dstDir, target); err != nil {
				return err
			}
			srcFile, err := os.Open(linkTarget)
			if err != nil {
				return fmt.Errorf("open hard link source %s: %w", linkTarget, err)
			}
			dstFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
			if err != nil {
				_ = srcFile.Close()
				return fmt.Errorf("create hard link copy %s: %w", target, err)
			}
			written, copyErr := io.Copy(dstFile, srcFile)
			_ = srcFile.Close()
			if closeErr := dstFile.Close(); closeErr != nil {
				return fmt.Errorf("close hard link copy %s: %w", target, closeErr)
			}
			if copyErr != nil {
				return fmt.Errorf("copy hard link %s: %w", target, copyErr)
			}
			totalExtracted += written
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
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
		default:
			return fmt.Errorf("unsupported openclaw artifact entry %s with type %d", hdr.Name, hdr.Typeflag)
		}
	}

	gotHash := hex.EncodeToString(hasher.Sum(nil))
	if gotHash != manifest.SHA256 {
		return fmt.Errorf("openclaw artifact SHA256 mismatch: got %s, want %s", gotHash, manifest.SHA256)
	}

	entrypoint := filepath.Join(dstDir, manifest.Runtime.EntrypointRelpath)
	info, err := os.Stat(entrypoint)
	if err != nil {
		return fmt.Errorf("staged artifact missing entrypoint %s: %w", manifest.Runtime.EntrypointRelpath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("staged artifact entrypoint %s is a directory", manifest.Runtime.EntrypointRelpath)
	}
	pluginsDir := filepath.Join(dstDir, manifest.Runtime.BundledPluginsRelpath)
	if info, err := os.Stat(pluginsDir); err != nil || !info.IsDir() {
		if err != nil {
			return fmt.Errorf("staged artifact missing bundled plugins dir %s: %w", manifest.Runtime.BundledPluginsRelpath, err)
		}
		return fmt.Errorf("staged artifact bundled plugins path %s is not a directory", manifest.Runtime.BundledPluginsRelpath)
	}

	return nil
}

func secureTarPath(dstDir, name string) (string, error) {
	cleaned := filepath.Clean(strings.TrimPrefix(name, "./"))
	switch cleaned {
	case ".", "":
		return "", nil
	case "..":
		return "", fmt.Errorf("invalid openclaw artifact path %q", name)
	}
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid openclaw artifact path %q", name)
	}
	target := filepath.Join(dstDir, cleaned)
	rel, err := filepath.Rel(dstDir, target)
	if err != nil {
		return "", fmt.Errorf("resolve openclaw artifact path %q: %w", name, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid openclaw artifact path %q", name)
	}
	return target, nil
}

// sanitizeRuntimeRelpath validates a manifest-provided relative path.
// Returns the cleaned path, or empty string if the path is invalid
// (absolute, empty, or attempts directory traversal).
func sanitizeRuntimeRelpath(value string) string {
	value = filepath.Clean(strings.TrimSpace(value))
	if value == "" || value == "." {
		return ""
	}
	if filepath.IsAbs(value) || value == ".." || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
		return ""
	}
	return value
}

// RuntimePathsForRelease reads the staged manifest.json in a release directory
// and returns the runtime-relative paths for the entrypoint and bundled plugins.
func RuntimePathsForRelease(releasePath string) OpenClawRuntimePaths {
	runtimePaths := OpenClawRuntimePaths{
		EntrypointRelpath:     defaultOpenClawEntrypointRelpath,
		BundledPluginsRelpath: defaultOpenClawPluginsRelpath,
	}
	manifestPath := filepath.Join(releasePath, "manifest.json")
	file, err := os.Open(manifestPath)
	if err != nil {
		return runtimePaths
	}
	defer func() { _ = file.Close() }()

	manifest, err := ParseOpenClawReleaseManifest(file)
	if err != nil {
		return runtimePaths
	}
	if ep := sanitizeRuntimeRelpath(manifest.Runtime.EntrypointRelpath); ep != "" {
		runtimePaths.EntrypointRelpath = ep
	}
	if bp := sanitizeRuntimeRelpath(manifest.Runtime.BundledPluginsRelpath); bp != "" {
		runtimePaths.BundledPluginsRelpath = bp
	}
	return runtimePaths
}

func ensureParentResolvedWithinRoot(root, target string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve root %s: %w", root, err)
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return fmt.Errorf("resolve parent dir for %s: %w", target, err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedParent)
	if err != nil {
		return fmt.Errorf("resolve path for %s: %w", target, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid openclaw artifact path %q", target)
	}
	return nil
}

func secureSymlinkTargetPath(root, linkPath, linkname string) (string, error) {
	if strings.TrimSpace(linkname) == "" {
		return "", fmt.Errorf("invalid openclaw artifact symlink target %q", linkname)
	}
	if filepath.IsAbs(linkname) {
		return "", fmt.Errorf("invalid openclaw artifact symlink target %q", linkname)
	}
	targetPath := filepath.Join(filepath.Dir(linkPath), linkname)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve root %s: %w", root, err)
	}
	cleanTarget := filepath.Clean(targetPath)
	rel, err := filepath.Rel(resolvedRoot, cleanTarget)
	if err != nil {
		return "", fmt.Errorf("resolve openclaw symlink target %q: %w", linkname, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid openclaw artifact symlink target %q", linkname)
	}
	return cleanTarget, nil
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func localPathFromURI(uri string) (string, error) {
	if strings.HasPrefix(uri, "file://") {
		parsed, err := url.Parse(uri)
		if err != nil {
			return "", fmt.Errorf("parse file URI %s: %w", uri, err)
		}
		if parsed.Path == "" {
			return "", fmt.Errorf("file URI missing path: %s", uri)
		}
		return parsed.Path, nil
	}
	return uri, nil
}

func generateTxnID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate openclaw staging transaction id: %w", err)
	}
	return "ocm-openclaw-" + hex.EncodeToString(b), nil
}
