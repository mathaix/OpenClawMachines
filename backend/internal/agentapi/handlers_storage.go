package agentapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const defaultBrowserStorageMountPoint = "/var/lib/ocm-browser"

var (
	configureBrowserStorageOnHost = configureBrowserStorageOnHostImpl
	restartAgentService           = func() error {
		return exec.Command("systemctl", "restart", "ocm-agent").Run()
	}
)

type configureBrowserStorageRequest struct {
	Device       string `json:"device"`
	MountPoint   string `json:"mount_point,omitempty"`
	Format       bool   `json:"format"`
	RestartAgent bool   `json:"restart_agent"`
}

type configureBrowserStorageResult struct {
	BrowserStateDir  string `json:"browser_state_dir"`
	MountPoint       string `json:"mount_point"`
	FSType           string `json:"fs_type"`
	ReflinkSupported bool   `json:"reflink_supported"`
	RestartScheduled bool   `json:"restart_scheduled"`
}

func (s *Server) handleConfigureBrowserStorage(w http.ResponseWriter, r *http.Request) {
	var req configureBrowserStorageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"status":  "error",
			"message": "invalid request body",
		})
		return
	}
	if !updateInProgress.CompareAndSwap(false, true) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"status":  "already_updating",
			"message": "a host update or browser storage operation is already in progress",
		})
		return
	}
	releaseUpdateLock := true
	defer func() {
		if releaseUpdateLock {
			updateInProgress.Store(false)
		}
	}()

	if err := s.ensureNoActiveBrowserVMs(r.Context()); err != nil {
		slog.Warn("browser_storage.configure.rejected_active_browser_vms", "error", err)
		writeJSON(w, http.StatusConflict, map[string]string{
			"status":  "active_browser_vms",
			"message": err.Error(),
		})
		return
	}

	result, err := configureBrowserStorageOnHost(r.Context(), req)
	if err != nil {
		slog.Error("browser_storage.configure.failed", "device", req.Device, "mount_point", req.MountPoint, "format", req.Format, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	if req.RestartAgent {
		result.RestartScheduled = true
		releaseUpdateLock = false
		go func() {
			time.Sleep(500 * time.Millisecond)
			slog.Info("browser_storage.configure.restart_agent")
			if err := restartAgentService(); err != nil {
				slog.Error("browser_storage.configure.restart_failed", "error", err)
				updateInProgress.Store(false)
			}
		}()
	}

	slog.Info("browser_storage.configure.succeeded",
		"device", req.Device,
		"browser_state_dir", result.BrowserStateDir,
		"fs_type", result.FSType,
		"reflink_supported", result.ReflinkSupported,
		"restart_scheduled", result.RestartScheduled)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":            "configured",
		"browser_state_dir": result.BrowserStateDir,
		"mount_point":       result.MountPoint,
		"fs_type":           result.FSType,
		"reflink_supported": result.ReflinkSupported,
		"restart_scheduled": result.RestartScheduled,
	})
}

func (s *Server) ensureNoActiveBrowserVMs(ctx context.Context) error {
	bvms, err := s.orchestrator.ListBrowserVMs(ctx)
	if err != nil {
		return fmt.Errorf("list active browser VMs: %w", err)
	}
	if len(bvms) > 0 {
		return fmt.Errorf("host has %d active browser VM(s); stop browser VMs first", len(bvms))
	}
	return nil
}

func configureBrowserStorageOnHostImpl(ctx context.Context, req configureBrowserStorageRequest) (*configureBrowserStorageResult, error) {
	device, err := cleanDevicePath(req.Device)
	if err != nil {
		return nil, err
	}
	mountPoint, err := cleanBrowserStorageMountPoint(req.MountPoint)
	if err != nil {
		return nil, err
	}
	if err := requireBlockDevice(device); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		return nil, fmt.Errorf("create browser storage mount point: %w", err)
	}

	mountedAt, mounted, err := findMountedTarget(ctx, device)
	if err != nil {
		return nil, err
	}
	if mounted {
		if mountedAt != mountPoint {
			return nil, fmt.Errorf("%s is already mounted at %s, not %s", device, mountedAt, mountPoint)
		}
	} else {
		if req.Format {
			if err := runCommand(ctx, "mkfs.xfs", "-f", "-m", "reflink=1", device); err != nil {
				return nil, fmt.Errorf("format %s as XFS with reflink: %w", device, err)
			}
		} else {
			fsType, err := blockDeviceFSType(ctx, device)
			if err != nil {
				return nil, err
			}
			if fsType != "xfs" {
				if fsType == "" {
					fsType = "unformatted"
				}
				return nil, fmt.Errorf("%s is %s; enable format to create XFS reflink storage", device, fsType)
			}
		}
		uuid, err := blockDeviceUUID(ctx, device)
		if err != nil {
			return nil, err
		}
		if err := ensureFstabMount(uuid, mountPoint); err != nil {
			return nil, err
		}
		if err := runCommand(ctx, "mount", mountPoint); err != nil {
			return nil, fmt.Errorf("mount %s: %w", mountPoint, err)
		}
	}

	if err := os.MkdirAll(filepath.Join(mountPoint, "browser-rootfs"), 0755); err != nil {
		return nil, fmt.Errorf("create browser rootfs cache dir: %w", err)
	}
	if err := writeBrowserStateEnv(mountPoint); err != nil {
		return nil, err
	}
	if err := upsertAgentEnv("BROWSER_STATE_DIR", mountPoint); err != nil {
		return nil, err
	}

	fsType, _ := findmntValue(ctx, mountPoint, "FSTYPE")
	reflinkSupported, err := probeReflinkDir(ctx, mountPoint)
	if err != nil {
		return nil, fmt.Errorf("browser storage mounted but reflink probe failed: %w", err)
	}
	if !reflinkSupported {
		return nil, fmt.Errorf("browser storage mounted at %s but reflink is not supported", mountPoint)
	}

	return &configureBrowserStorageResult{
		BrowserStateDir:  mountPoint,
		MountPoint:       mountPoint,
		FSType:           fsType,
		ReflinkSupported: true,
	}, nil
}

func cleanDevicePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("device is required")
	}
	if strings.ContainsAny(raw, "\x00\r\n\t ") {
		return "", fmt.Errorf("device path must not contain whitespace")
	}
	clean := filepath.Clean(raw)
	if !strings.HasPrefix(clean, "/dev/") || clean == "/dev" {
		return "", fmt.Errorf("device must be an absolute /dev path")
	}
	return clean, nil
}

func cleanBrowserStorageMountPoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = defaultBrowserStorageMountPoint
	}
	if strings.ContainsAny(raw, "\x00\r\n\t ") {
		return "", fmt.Errorf("mount point must not contain whitespace")
	}
	clean := filepath.Clean(raw)
	if !filepath.IsAbs(clean) || clean == "/" {
		return "", fmt.Errorf("mount point must be an absolute non-root path")
	}
	if clean != defaultBrowserStorageMountPoint && !strings.HasPrefix(clean, defaultBrowserStorageMountPoint+"/") {
		return "", fmt.Errorf("mount point must be %s or a child directory", defaultBrowserStorageMountPoint)
	}
	return clean, nil
}

func requireBlockDevice(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat block device: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Mode&syscall.S_IFMT != syscall.S_IFBLK {
		return fmt.Errorf("%s is not a block device", path)
	}
	return nil
}

func findMountedTarget(ctx context.Context, device string) (string, bool, error) {
	out, err := exec.CommandContext(ctx, "findmnt", "-rn", "-S", device, "-o", "TARGET").CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		if trimmed == "" {
			return "", false, nil
		}
		return "", false, fmt.Errorf("find existing mount for %s: %s: %w", device, trimmed, err)
	}
	if trimmed == "" {
		return "", false, nil
	}
	return firstLine(trimmed), true, nil
}

func blockDeviceFSType(ctx context.Context, device string) (string, error) {
	out, err := exec.CommandContext(ctx, "blkid", "-o", "value", "-s", "TYPE", device).CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		return trimmed, nil
	}
	return firstLine(trimmed), nil
}

func blockDeviceUUID(ctx context.Context, device string) (string, error) {
	out, err := exec.CommandContext(ctx, "blkid", "-o", "value", "-s", "UUID", device).CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil || trimmed == "" {
		return "", fmt.Errorf("read UUID for %s: %s: %w", device, trimmed, err)
	}
	return firstLine(trimmed), nil
}

func ensureFstabMount(uuid, mountPoint string) error {
	const fstabPath = "/etc/fstab"
	data, err := os.ReadFile(fstabPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read /etc/fstab: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "UUID="+uuid && fields[1] == mountPoint {
			return nil
		}
	}
	text := string(data)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += fmt.Sprintf("UUID=%s %s xfs defaults,noatime 0 2\n", uuid, mountPoint)
	if err := os.WriteFile(fstabPath, []byte(text), 0644); err != nil {
		return fmt.Errorf("write /etc/fstab: %w", err)
	}
	return nil
}

func writeBrowserStateEnv(browserStateDir string) error {
	if err := os.MkdirAll("/etc/ocm-agent", 0755); err != nil {
		return fmt.Errorf("create /etc/ocm-agent: %w", err)
	}
	if err := os.WriteFile("/etc/ocm-agent/browser-state.env", []byte("BROWSER_STATE_DIR="+browserStateDir+"\n"), 0644); err != nil {
		return fmt.Errorf("write browser-state env: %w", err)
	}
	return nil
}

func upsertAgentEnv(key, value string) error {
	const envPath = "/etc/ocm-agent/agent.env"
	data, err := os.ReadFile(envPath)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(envPath, []byte(key+"="+value+"\n"), 0644)
		}
		return fmt.Errorf("read agent env: %w", err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, key+"=") {
			continue
		}
		lines = append(lines, line)
	}
	lines = append(lines, key+"="+value)
	return os.WriteFile(envPath, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

func findmntValue(ctx context.Context, path, column string) (string, error) {
	out, err := exec.CommandContext(ctx, "findmnt", "-T", path, "-n", "-o", column).CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("findmnt %s for %s: %s: %w", column, path, trimmed, err)
	}
	if trimmed == "" {
		return "", fmt.Errorf("findmnt %s for %s returned empty output", column, path)
	}
	return firstLine(trimmed), nil
}

func probeReflinkDir(ctx context.Context, dir string) (bool, error) {
	probeDir, err := os.MkdirTemp(dir, ".ocm-reflink-probe-")
	if err != nil {
		return false, fmt.Errorf("create reflink probe dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(probeDir) }()
	src := filepath.Join(probeDir, "src")
	dst := filepath.Join(probeDir, "dst")
	if err := os.WriteFile(src, []byte("ocm reflink probe\n"), 0644); err != nil {
		return false, fmt.Errorf("write reflink probe source: %w", err)
	}
	out, err := exec.CommandContext(ctx, "cp", "--reflink=always", src, dst).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("cp --reflink=always: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return true, nil
}

func runCommand(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s: %w", name, strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return strings.TrimSpace(s)
}
