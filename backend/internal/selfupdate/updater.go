package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"cloud.google.com/go/storage"
	"github.com/mathaix/openclawmachines/backend/pkg/gcsutil"
	"github.com/mathaix/openclawmachines/backend/pkg/version"
)

const (
	// maxDownloadSize is the maximum allowed download size (256 MB).
	maxDownloadSize = 256 << 20
)

// Package-level vars for testability.
var (
	// installedBinaryPath is where the agent binary lives on the host.
	// Determined at init time from os.Executable() so it works regardless of
	// whether the binary is /usr/local/bin/agent (GCP provisioned) or
	// /usr/local/bin/ocm-agent (enrolled hosts). Overridden in tests.
	installedBinaryPath = resolveInstalledPath()

	// runningBinaryPath resolves to the executable image of the current process.
	// On Linux this is /proc/self/exe; overridden in tests.
	runningBinaryPath = "/proc/self/exe"

	// restartAgent restarts the agent via systemd.
	restartAgent = func() error {
		cmd := exec.Command("systemctl", "restart", "ocm-agent")
		return cmd.Run()
	}

	// verifyVersion execs a binary with --version and checks the output.
	// Package-level var so tests can stub it out.
	verifyVersion = verifyBinaryVersionImpl
)

// resolveInstalledPath returns the absolute path of the current executable,
// following symlinks. Panics on failure — the agent cannot self-update if it
// doesn't know where its own binary lives.
func resolveInstalledPath() string {
	exe, err := os.Executable()
	if err != nil {
		panic(fmt.Sprintf("selfupdate: os.Executable() failed: %v", err))
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		panic(fmt.Sprintf("selfupdate: filepath.EvalSymlinks(%q) failed: %v", exe, err))
	}
	slog.Info("selfupdate.resolved_binary_path", "path", resolved)
	return resolved
}

// Updater checks GCS for new agent versions and applies updates when
// triggered by startup or by the admin "Update" button.
type Updater struct {
	client      *storage.Client
	manifestURI string
	logger      *slog.Logger

	// mu serializes CheckAndUpdate across the startup check and the
	// manual trigger-update HTTP endpoint. Without this, concurrent
	// callers can clobber each other's temp files and race on restart.
	mu sync.Mutex

	// fetchManifest fetches the manifest. Defaults to GCS-backed FetchManifest;
	// overridden in tests.
	fetchManifest func(ctx context.Context) (*Manifest, error)

	// downloadReader returns an io.ReadCloser for the binary at the given URL.
	// Defaults to GCS download; overridden in tests.
	downloadReader func(ctx context.Context, url string) (io.ReadCloser, error)
}

// New creates an Updater that reads the given GCS manifest URI on demand.
// The caller drives updates via CheckAndUpdate (startup check or admin trigger);
// this type no longer runs a background poll.
func New(ctx context.Context, manifestURI string) (*Updater, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create GCS client: %w", err)
	}

	u := &Updater{
		client:      client,
		manifestURI: manifestURI,
		logger:      slog.Default(),
	}
	u.fetchManifest = func(ctx context.Context) (*Manifest, error) {
		return FetchManifest(ctx, u.client, u.manifestURI)
	}
	u.downloadReader = func(ctx context.Context, url string) (io.ReadCloser, error) {
		bucket, object, err := gcsutil.ParseGCSURI(url)
		if err != nil {
			return nil, err
		}
		return u.client.Bucket(bucket).Object(object).NewReader(ctx)
	}
	return u, nil
}

// Close releases the GCS client resources.
func (u *Updater) Close() error {
	if u.client != nil {
		return u.client.Close()
	}
	return nil
}

// CheckAndUpdate checks for a new agent version and applies it if available.
// Returns true if an update was applied (the process should exit/restart).
// Safe for concurrent use — only one check runs at a time.
//
// Four cases:
//  1. Running image SHA == manifest SHA, installed == manifest → fully current, skip.
//  2. Running image SHA == manifest SHA, installed != manifest → running is
//     current but installed binary was corrupted/replaced; re-download to repair.
//  3. Installed binary SHA == manifest SHA, running != manifest → binary
//     already on disk from a prior download; verify and restart.
//  4. Neither matches → download to temp, verify, install, restart.
func (u *Updater) CheckAndUpdate(ctx context.Context) (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.logger.Info("selfupdate.check",
		"manifest", u.manifestURI,
		"current_version", version.Version)

	manifest, err := u.fetchManifest(ctx)
	if err != nil {
		return false, fmt.Errorf("fetch manifest: %w", err)
	}

	// Hash the running process image (/proc/self/exe on Linux), not the
	// file on disk which may have been replaced out-of-band.
	runningHash, runningErr := hashFile(runningBinaryPath)
	runningMatches := runningErr == nil && runningHash == manifest.SHA256

	installedHash, installedErr := hashFile(installedBinaryPath)
	installedMatches := installedErr == nil && installedHash == manifest.SHA256

	switch {
	case runningMatches && installedMatches:
		// Case 1: fully current.
		u.logger.Info("selfupdate.skip",
			"version", manifest.Version,
			"reason", "already_current",
			"sha256", runningHash)
		return false, nil

	case runningMatches && !installedMatches:
		// Case 2: the running process is current but the installed binary on
		// disk diverges (out-of-band replacement, or failed prior install).
		// Re-download to repair installed binary; no restart needed.
		u.logger.Warn("selfupdate.repair_installed",
			"running_sha", runningHash,
			"installed_sha", installedHash,
			"manifest_sha", manifest.SHA256,
			"reason", "installed_binary_diverged")

		if err := u.downloadVerifyAndInstall(ctx, manifest); err != nil {
			return false, fmt.Errorf("repair installed binary: %w", err)
		}
		u.logger.Info("selfupdate.repair_complete", "version", manifest.Version)
		return false, nil

	case !runningMatches && installedMatches:
		// Case 3: installed binary matches manifest but running process is stale.
		// Verify installed binary and restart.
		u.logger.Info("selfupdate.restart_needed",
			"installed_version", manifest.Version,
			"running_version", version.Version,
			"reason", "installed_current_running_stale")

		if err := verifyVersion(installedBinaryPath, manifest.Version); err != nil {
			u.logger.Error("selfupdate.installed_verify_failed", "error", err)
			return false, fmt.Errorf("installed binary verification failed: %w", err)
		}

		u.logger.Info("selfupdate.restarting", "new_version", manifest.Version)
		if err := restartAgent(); err != nil {
			return true, fmt.Errorf("restart agent: %w", err)
		}
		return true, nil

	default:
		// Case 4: neither matches — full download, verify, install, restart.
		u.logger.Info("selfupdate.downloading",
			"current_version", version.Version,
			"new_version", manifest.Version,
			"size_bytes", manifest.SizeBytes)

		if err := u.downloadVerifyAndInstall(ctx, manifest); err != nil {
			return false, fmt.Errorf("download and install: %w", err)
		}

		u.logger.Info("selfupdate.applied", "new_version", manifest.Version)
		if err := restartAgent(); err != nil {
			return true, fmt.Errorf("restart agent: %w", err)
		}
		return true, nil
	}
}

// downloadVerifyAndInstall downloads the binary to a temp file, verifies its
// SHA256 and --version output, and only then replaces the installed binary.
// If any verification step fails, the installed binary is left untouched.
func (u *Updater) downloadVerifyAndInstall(ctx context.Context, manifest *Manifest) error {
	r, err := u.downloadReader(ctx, manifest.URL)
	if err != nil {
		return fmt.Errorf("open agent object: %w", err)
	}
	defer func() { _ = r.Close() }()

	// Limit download size to prevent disk exhaustion.
	limit := int64(maxDownloadSize)
	if manifest.SizeBytes > 0 && manifest.SizeBytes < limit {
		limit = manifest.SizeBytes + 1<<20
	}
	lr := io.LimitReader(r, limit)

	// Download to temp file.
	safeVersion := sanitizeVersion(manifest.Version)
	tmpPath := fmt.Sprintf("%s-%s.tmp", installedBinaryPath, safeVersion)
	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath) // clean up on error; no-op after successful rename
	}()

	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmpFile, hasher), lr); err != nil {
		return fmt.Errorf("download agent binary: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	// Step 1: verify SHA256 of the downloaded temp file.
	gotHash := hex.EncodeToString(hasher.Sum(nil))
	if gotHash != manifest.SHA256 {
		return fmt.Errorf("SHA256 mismatch: got %s, want %s", gotHash, manifest.SHA256)
	}

	// Step 2: verify the temp binary reports the expected version.
	// This catches mismatches before we touch the installed binary.
	if err := verifyVersion(tmpPath, manifest.Version); err != nil {
		u.logger.Error("selfupdate.version_mismatch",
			"temp_path", tmpPath,
			"error", err)
		return fmt.Errorf("pre-install verification failed: %w", err)
	}

	// Step 3: all checks passed — atomic replace the installed binary.
	if err := atomicReplace(tmpPath, installedBinaryPath); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}

	return nil
}

// atomicReplace moves src to dst. If os.Rename fails with EXDEV (cross-device),
// it falls back to copy + rename within the same directory.
func atomicReplace(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}

	// Check for cross-device link error
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}

	// EXDEV fallback: copy to a temp file in dst's directory, then rename
	tmpDst := dst + ".new"
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source for copy: %w", err)
	}
	defer func() { _ = srcFile.Close() }()

	dstFile, err := os.OpenFile(tmpDst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("create copy target: %w", err)
	}
	defer func() {
		_ = dstFile.Close()
		_ = os.Remove(tmpDst)
	}()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copy binary: %w", err)
	}
	if err := dstFile.Close(); err != nil {
		return fmt.Errorf("close copy target: %w", err)
	}

	if err := os.Rename(tmpDst, dst); err != nil {
		return fmt.Errorf("rename copy: %w", err)
	}

	// Clean up the original temp file
	_ = os.Remove(src)
	return nil
}

// verifyBinaryVersionImpl execs the binary with --version and checks
// that it reports the expected version string.
func verifyBinaryVersionImpl(binaryPath, expectedVersion string) error {
	out, err := exec.Command(binaryPath, "--version").Output()
	if err != nil {
		return fmt.Errorf("exec %s --version: %w", binaryPath, err)
	}
	// Output format: "ocm-agent VERSION (commit=... built=...)"
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return fmt.Errorf("unexpected --version output: %s", string(out))
	}
	reportedVersion := fields[1]
	if reportedVersion != expectedVersion {
		return fmt.Errorf("binary reports version %q, manifest expects %q", reportedVersion, expectedVersion)
	}
	return nil
}

// hashFile computes the SHA256 hex digest of a file.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sanitizeVersion strips all characters except alphanumeric, dash, dot, and underscore.
var versionSanitizer = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func sanitizeVersion(v string) string {
	s := versionSanitizer.ReplaceAllString(v, "_")
	if len(s) > 64 {
		s = s[:64]
	}
	if s == "" {
		s = "unknown"
	}
	return filepath.Base(s) // defense against any remaining path traversal
}
