package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"

	"github.com/mathaix/openclawmachines/backend/internal/agentapi"
	"github.com/mathaix/openclawmachines/backend/internal/apiproxy"
	"github.com/mathaix/openclawmachines/backend/internal/backup"
	"github.com/mathaix/openclawmachines/backend/internal/cdpproxy"
	"github.com/mathaix/openclawmachines/backend/internal/config"
	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/network"
	"github.com/mathaix/openclawmachines/backend/internal/orchestrator"
	"github.com/mathaix/openclawmachines/backend/internal/rootfs"
	"github.com/mathaix/openclawmachines/backend/internal/selfupdate"
	"github.com/mathaix/openclawmachines/backend/internal/vmmetrics/sampler"
	"github.com/mathaix/openclawmachines/backend/pkg/logging"
	"github.com/mathaix/openclawmachines/backend/pkg/version"
)

// VMMetricsCgroupRoot is where systemd-managed Firecracker VM units live in
// cgroup v2. Hard-coded because it's a kernel/systemd convention; if a host
// ever needs an override, add an env-var-backed config field.
const VMMetricsCgroupRoot = "/sys/fs/cgroup/system.slice"

func main() {
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("ocm-agent %s (commit=%s built=%s)\n", version.Version, version.GitCommit, version.BuildTime)
		os.Exit(0)
	}

	logging.Init("agent")

	startTime := time.Now()
	slog.Info("agent.start", "version", version.Version, "commit", version.GitCommit, "built", version.BuildTime)

	// Install systemd drop-in that protects Firecracker children from being
	// killed by the agent's own cgroup on self-update / restart. Idempotent;
	// only triggers a daemon-reload on the first install per host.
	ensureSystemdKillModeOverride()

	// 1. Load config
	cfg, err := config.LoadAgent()
	if err != nil {
		slog.Error("config.load_failed", "error", err)
		os.Exit(1)
	}
	slog.Info("agent.browser_rootfs.config",
		"manifest_uri", cfg.BrowserRootfsGCSManifest,
		"version", cfg.BrowserRootfsVersion,
		"lineage", browserRootfsLineage(cfg.BrowserRootfsGCSManifest),
		"default_manifest_uri", config.DefaultBrowserRootfsManifestURI,
		"default_version", config.DefaultBrowserRootfsVersion,
		"legacy_manifest_uri", config.StableBrowserRootfsManifestURI,
		"kernel_manifest_uri", config.ExperimentalKernelBrowserManifestURI,
		"allow_kernel_browser_full_copy", cfg.AllowKernelBrowserFullCopy)

	// 2. Prefetch GCP instance metadata (if running on GCP)
	prefetchGCPMetadata(cfg)

	// 2a. Env-var fallback for tunnel token (3rd-party enrolled hosts use agent.env)
	if envToken := os.Getenv("TUNNEL_TOKEN"); envToken != "" && cfg.TunnelToken == "" {
		cfg.TunnelToken = envToken
	}

	// 2b. Self-update check on startup (before registering with control plane)
	var updater *selfupdate.Updater
	if cfg.AgentGCSManifest != "" {
		var initErr error
		updater, initErr = selfupdate.New(context.Background(), cfg.AgentGCSManifest)
		if initErr != nil {
			slog.Warn("selfupdate.init_failed", "error", initErr)
		} else {
			startupCtx, startupCancel := context.WithTimeout(context.Background(), 60*time.Second)
			updated, checkErr := updater.CheckAndUpdate(startupCtx)
			startupCancel()
			if checkErr != nil {
				slog.Warn("selfupdate.startup_check_failed", "error", checkErr)
			}
			if updated {
				_ = updater.Close()
				return // systemctl restart will re-launch us
			}
		}
	}

	// Require agent token — without it, the control API is wide open
	if cfg.AgentToken == "" {
		slog.Error("config.missing_agent_token", "error", "FC_AGENT_TOKEN is required")
		os.Exit(1)
	}

	// Root context for all services
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 3. Setup bridge network
	bridgeGateway := strings.Split(cfg.BridgeIP, "/")[0]
	bridge := network.NewBridge(cfg.BridgeName, "192.168.100.0/24", bridgeGateway)

	bridgeStart := time.Now()
	if err := bridge.Setup(); err != nil {
		slog.Error("bridge.setup_failed", "error", err)
		os.Exit(1)
	} else {
		slog.Info("bridge.setup_complete", "duration_ms", time.Since(bridgeStart).Milliseconds())
		// 3b. Clear stale NAT rules from previous runs (TeardownNAT ignores missing rules),
		// then set up fresh rules. Prevents iptables rule accumulation across agent restarts.
		_ = bridge.TeardownNAT()
		if err := bridge.SetupNAT(); err != nil {
			slog.Error("bridge.nat_setup_failed", "error", err)
			os.Exit(1)
		}
	}

	// 4. Start metadata server
	metaSrv := metadata.New(bridgeGateway, 80)
	metaSrv.BackendURL = cfg.BackendURL
	metaSrv.AgentToken = cfg.AgentToken
	metaSrv.AgentVersion = version.Version
	// Wire pull-through secret fetcher so channel tokens resolve on cache miss.
	if cfg.BackendURL != "" && cfg.AgentToken != "" {
		metaSrv.SecretFetcher = metadata.NewHTTPSecretFetcher(cfg.BackendURL, cfg.AgentToken)
	}
	go func() {
		if err := metaSrv.Start(ctx); err != nil {
			if runtime.GOOS != "linux" {
				slog.Warn("metadata.server_not_available", "error", err, "note", "expected on non-Linux")
			} else {
				slog.Error("metadata.server_failed", "error", err)
				os.Exit(1)
			}
		}
	}()

	// 4b. Readiness check for metadata server
	if runtime.GOOS == "linux" {
		metaAddr := fmt.Sprintf("http://%s:80/health", bridgeGateway)
		metaReady := false
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := http.Get(metaAddr) //nolint:gosec
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					metaReady = true
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !metaReady {
			slog.Error("metadata.server_not_ready", "addr", metaAddr, "timeout", "2s")
			os.Exit(1)
		}
		slog.Info("metadata.server_ready", "addr", metaAddr)
	}

	// 5. Start API proxy on bridge IP:4000
	proxy := apiproxy.New(metaSrv, bridgeGateway, 4000)
	if cfg.BackendURL != "" {
		proxy.SetRefreshURL(cfg.BackendURL)
	}
	go func() {
		if err := proxy.Start(ctx); err != nil && ctx.Err() == nil {
			if runtime.GOOS != "linux" {
				slog.Warn("apiproxy.not_available", "error", err, "note", "expected on non-Linux")
			} else {
				slog.Error("apiproxy.failed", "error", err)
				os.Exit(1)
			}
		}
	}()
	slog.Info("apiproxy.starting", "addr", bridgeGateway+":4000")

	// 5b. Start CDP proxy on bridge IP:9222
	cdpProxy := cdpproxy.New(metaSrv, bridgeGateway, 9222)
	go func() {
		if err := cdpProxy.Start(ctx); err != nil && ctx.Err() == nil {
			if runtime.GOOS != "linux" {
				slog.Warn("cdpproxy.not_available", "error", err, "note", "expected on non-Linux")
			} else {
				slog.Error("cdpproxy.failed", "error", err)
				os.Exit(1)
			}
		}
	}()
	slog.Info("cdpproxy.starting", "addr", bridgeGateway+":9222")

	// 6. Init orchestrator
	rootfsLockPath := filepath.Join(cfg.StateDir, ".rootfs.lock")
	rootfsLk := rootfs.NewRootfsLock(rootfsLockPath)
	orchCfg := orchestrator.FirecrackerConfig{
		BridgeName:                 cfg.BridgeName,
		SocketDir:                  cfg.SocketDir,
		RootfsDir:                  cfg.ImagesDir,
		StateDir:                   cfg.StateDir,
		BrowserStateDir:            cfg.BrowserStateDir,
		DataDir:                    cfg.DataDir,
		RuntimeStateDir:            cfg.RuntimeStateDir,
		OpenClawRuntimeDir:         cfg.OpenClawRuntimeDir,
		OpenClawManifestURI:        cfg.OpenClawManifestURI,
		HermesRuntimeDir:           cfg.HermesRuntimeDir,
		HermesManifestURI:          cfg.HermesManifestURI,
		KernelPath:                 cfg.KernelPath,
		DefaultVCPUs:               cfg.VCPUs,
		DefaultMemoryMB:            cfg.MemoryMB,
		GCSRootfsManifest:          cfg.RootfsGCSManifest,
		GCSDownloadTimeout:         time.Duration(cfg.RootfsDownloadTimeout) * time.Second,
		GCSRetryAttempts:           cfg.RootfsRetryAttempts,
		GCSBrowserRootfsManifest:   cfg.BrowserRootfsGCSManifest,
		GCSBrowserRootfsVersion:    cfg.BrowserRootfsVersion,
		AllowKernelBrowserFullCopy: cfg.AllowKernelBrowserFullCopy,
		HostExternalIP:             getExternalIP(),
		RuntimeOwnerKind:           cfg.RuntimeOwnerKind,
		OpenClawDownloadTimeout:    time.Duration(cfg.OpenClawDownloadTimeout) * time.Second,
		OpenClawRetryAttempts:      cfg.OpenClawRetryAttempts,
		HermesDownloadTimeout:      time.Duration(cfg.HermesDownloadTimeout) * time.Second,
		HermesRetryAttempts:        cfg.HermesRetryAttempts,
		RootfsLock:                 rootfsLk,
	}
	orch, err := orchestrator.New(orchCfg, bridge)
	if err != nil {
		slog.Error("orchestrator.init_failed", "error", err)
		os.Exit(1)
	}
	logHostCapabilities(cfg)

	// 6. Connect metadata to orchestrator
	orch.SetMetadataRegistrar(metaSrv)

	// 6b. Recover persisted VM state after metadata/proxy services are available.
	if err := orch.Recover(ctx); err != nil {
		if runtime.GOOS != "linux" && err == orchestrator.ErrNotLinux {
			slog.Warn("orchestrator.recover_not_available", "error", err, "note", "expected on non-Linux")
		} else {
			slog.Error("orchestrator.recover_failed", "error", err)
			os.Exit(1)
		}
	}

	// 7. Initialize backup store (GCS) — when bucket is configured
	// (Master key is no longer needed — per-machine keys come per-RPC)
	var backupSt backup.BackupStore
	if cfg.BackupGCSBucket != "" {
		var gcsOpts []option.ClientOption
		if cfg.GCSServiceAccountKey != "" {
			gcsOpts = append(gcsOpts, option.WithCredentialsJSON([]byte(cfg.GCSServiceAccountKey))) //nolint:staticcheck // TODO: migrate to credentials.NewJSONCredential
		}
		gcsClient, gcsErr := storage.NewClient(ctx, gcsOpts...)
		if gcsErr != nil {
			slog.Warn("backup.gcs_client_failed", "error", gcsErr)
		} else {
			backupSt = backup.NewGCSStore(gcsClient, cfg.BackupGCSBucket, cfg.BackupGCSPrefix)
			slog.Info("backup.store_initialized", "bucket", cfg.BackupGCSBucket, "prefix", cfg.BackupGCSPrefix)
		}
	} else {
		slog.Info("backup.disabled", "reason", "BACKUP_GCS_BUCKET not configured")
	}

	// 8. Create agentapi server
	metadataAddr := fmt.Sprintf("http://%s:80", bridgeGateway)
	srv := agentapi.NewServer(cfg.AgentToken, orch, cfg.ControlAllowedCIDRs, proxy, metadataAddr, updater, metaSrv, rootfsLk, cfg.RootfsGCSManifest != "", backupSt, cfg.DataDir)

	metaSrv.LogCallback = func(machineID, source, line string) {
		srv.LogManagerRef().AppendWithSource(machineID, source, line)
	}

	// 8b. Start background health probes for running VMs
	go srv.StartHealthProbes(ctx)

	// 8d. Start heartbeat to backend (reports IP + version every 60s)
	if cfg.BackendURL != "" && cfg.HostID != "" && cfg.AgentToken != "" {
		go runHeartbeat(ctx, cfg, srv)
	} else {
		slog.Warn("heartbeat.disabled", "backend_url", cfg.BackendURL != "", "host_id", cfg.HostID != "", "agent_token", cfg.AgentToken != "")
	}

	// 8e. Start VM resource sampler (1 Hz cgroup sampling, 10 s batched POST
	// to /api/agent/vm-metrics). Same gating as heartbeat — both need backend
	// + host_id + token. The sampler runs as a goroutine and exits on ctx.
	if cfg.BackendURL != "" && cfg.HostID != "" && cfg.AgentToken != "" {
		startVMMetricsSampler(ctx, cfg)
	} else {
		slog.Warn("vmmetrics.sampler_disabled", "backend_url", cfg.BackendURL != "", "host_id", cfg.HostID != "", "agent_token", cfg.AgentToken != "")
	}

	// 9. Start control server (:9090) + proxy server (:9091)
	controlServer := &http.Server{
		Addr:    ":" + cfg.ControlPort,
		Handler: srv.ControlRouter(),
	}
	// Proxy server binds to all interfaces — Cloud Run backend connects directly
	// for gateway proxy (WebSocket RPC). Auth is per-machine ProxyToken; iptables
	// blocks VM-to-agent access on the bridge interface.
	proxyAddr := ":" + cfg.ProxyPort
	if cfg.TunnelToken == "" {
		slog.Warn("config.missing_tunnel_token", "msg", "no tunnel token — cloudflared will not start")
	}
	proxyServer := &http.Server{
		Addr:    proxyAddr,
		Handler: srv.ProxyRouter(),
	}

	go func() {
		slog.Info("server.control_listen", "port", cfg.ControlPort)
		if err := controlServer.ListenAndServe(); err != http.ErrServerClosed {
			slog.Error("server.control_error", "error", err)
			os.Exit(1)
		}
	}()

	go func() {
		slog.Info("server.proxy_listen", "addr", proxyAddr)
		if err := proxyServer.ListenAndServe(); err != http.ErrServerClosed {
			slog.Error("server.proxy_error", "error", err)
			os.Exit(1)
		}
	}()

	// 10. Cloudflare Tunnel — spawn cloudflared (required in prod, optional in dev)
	var cloudflaredStop func()
	if cfg.TunnelToken != "" {
		slog.Info("cloudflared.start")
		cloudflaredStop = startCloudflared(ctx, cfg.TunnelToken)
	}
	// Note: if no tunnel token, agent still runs for control commands

	// 11. Wait for SIGINT/SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("agent.shutdown_start", "signal", sig.String(), "uptime_ms", time.Since(startTime).Milliseconds())

	// 12. Graceful shutdown

	// Notify control plane before anything else (uses its own context/timeout)
	if cfg.BackendURL != "" && cfg.HostID != "" && cfg.AgentToken != "" {
		notifyShutdown(cfg)
	}

	// Persist VM state so recovery works after restart
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := orch.Shutdown(shutdownCtx); err != nil {
		slog.Warn("orchestrator.shutdown_error", "error", err)
	}
	shutdownCancel()

	cancel() // stop background services (metadata, proxies, heartbeat, health probes)

	// Close GCS client used by self-update
	if updater != nil {
		_ = updater.Close()
	}

	// Stop cloudflared
	if cloudflaredStop != nil {
		cloudflaredStop()
	}

	// Shutdown API proxy
	proxyShutdownCtx, proxyShutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer proxyShutdownCancel()
	if err := proxy.Shutdown(proxyShutdownCtx); err != nil {
		slog.Warn("apiproxy.shutdown_error", "error", err)
	}

	// Shutdown HTTP servers
	_ = controlServer.Close()
	_ = proxyServer.Close()

	slog.Info("agent.shutdown_complete")
}

func browserRootfsLineage(manifestURI string) string {
	manifestURI = strings.TrimSpace(manifestURI)
	if manifestURI == "" {
		return "disabled"
	}
	if config.StableBrowserRootfsManifestURI != "" && manifestURI == config.StableBrowserRootfsManifestURI {
		return "legacy"
	}
	if config.ExperimentalKernelBrowserManifestURI != "" && manifestURI == config.ExperimentalKernelBrowserManifestURI {
		return "kernel-images-experimental"
	}
	return "custom"
}

func buildHostCapabilities(cfg *config.AgentConfig) map[string]interface{} {
	lineage := browserRootfsLineage(cfg.BrowserRootfsGCSManifest)
	browserStorage := detectStorageCapabilities(agentBrowserStateDir(cfg))
	browserStorage["browser_state_dir"] = agentBrowserStateDir(cfg)
	return map[string]interface{}{
		"browser_rootfs": map[string]interface{}{
			"manifest_uri":                   cfg.BrowserRootfsGCSManifest,
			"version_pin":                    cfg.BrowserRootfsVersion,
			"stable_kernel_images_version":   config.StableKernelBrowserRootfsVersion,
			"lineage":                        lineage,
			"default_manifest_uri":           config.DefaultBrowserRootfsManifestURI,
			"legacy_manifest_uri":            config.StableBrowserRootfsManifestURI,
			"kernel_manifest_uri":            config.ExperimentalKernelBrowserManifestURI,
			"kernel_images_experimental":     lineage == "kernel-images-experimental",
			"allow_kernel_browser_full_copy": cfg.AllowKernelBrowserFullCopy,
		},
		"storage":         detectStorageCapabilities(cfg.StateDir),
		"browser_storage": browserStorage,
	}
}

func logHostCapabilities(cfg *config.AgentConfig) {
	caps := buildHostCapabilities(cfg)
	storage, _ := caps["storage"].(map[string]interface{})
	browserStorage, _ := caps["browser_storage"].(map[string]interface{})
	browser, _ := caps["browser_rootfs"].(map[string]interface{})
	browserReflinkSupported, _ := browserStorage["reflink_supported"].(bool)
	lineage, _ := browser["lineage"].(string)

	slog.Info("agent.host_capabilities",
		"state_dir", storage["state_dir"],
		"mount_point", storage["mount_point"],
		"fs_type", storage["fs_type"],
		"reflink_supported", storage["reflink_supported"],
		"browser_state_dir", browserStorage["state_dir"],
		"browser_mount_point", browserStorage["mount_point"],
		"browser_fs_type", browserStorage["fs_type"],
		"browser_reflink_supported", browserReflinkSupported,
		"browser_rootfs_lineage", lineage,
		"allow_kernel_browser_full_copy", cfg.AllowKernelBrowserFullCopy)

	if lineage == "kernel-images-experimental" && cfg.BrowserRootfsVersion != config.StableKernelBrowserRootfsVersion && !browserReflinkSupported && !cfg.AllowKernelBrowserFullCopy {
		slog.Warn("agent.kernel_browser_rootfs.reflink_required",
			"browser_state_dir", browserStorage["state_dir"],
			"browser_fs_type", browserStorage["fs_type"],
			"version_pin", cfg.BrowserRootfsVersion,
			"reflink_error", browserStorage["reflink_error"],
			"note", "new/unpinned Kernel Images browser rootfs requires reflink-capable browser VM storage unless OCM_ALLOW_KERNEL_BROWSER_FULL_COPY=1 is set")
	}
}

func agentBrowserStateDir(cfg *config.AgentConfig) string {
	if dir := strings.TrimSpace(cfg.BrowserStateDir); dir != "" {
		return dir
	}
	return cfg.StateDir
}

func detectStorageCapabilities(stateDir string) map[string]interface{} {
	caps := map[string]interface{}{
		"state_dir": stateDir,
	}
	if fsType, err := findmntValue(stateDir, "FSTYPE"); err == nil {
		caps["fs_type"] = fsType
	} else {
		caps["fs_type_error"] = err.Error()
	}
	if mountPoint, err := findmntValue(stateDir, "TARGET"); err == nil {
		caps["mount_point"] = mountPoint
	} else {
		caps["mount_point_error"] = err.Error()
	}

	reflinkSupported, reflinkErr := probeReflink(stateDir)
	caps["reflink_supported"] = reflinkSupported
	if reflinkErr != nil {
		caps["reflink_error"] = reflinkErr.Error()
	}
	return caps
}

func findmntValue(target, column string) (string, error) {
	out, err := exec.Command("findmnt", "-T", target, "-n", "-o", column).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("findmnt %s: %s: %w", column, strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func probeReflink(dir string) (bool, error) {
	probeDir, err := os.MkdirTemp(dir, ".reflink-probe-")
	if err != nil {
		return false, fmt.Errorf("create reflink probe dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(probeDir) }()

	src := filepath.Join(probeDir, "src")
	dst := filepath.Join(probeDir, "dst")
	if err := os.WriteFile(src, []byte("ocm reflink probe\n"), 0644); err != nil {
		return false, fmt.Errorf("write reflink probe source: %w", err)
	}
	out, err := exec.Command("cp", "--reflink=always", src, dst).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("cp --reflink=always: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return true, nil
}

// notifyShutdown sends a shutdown notification to the control plane.
func notifyShutdown(cfg *config.AgentConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	payload, _ := json.Marshal(map[string]string{
		"host_id": cfg.HostID,
		"reason":  "agent_sigterm",
	})

	req, err := http.NewRequestWithContext(ctx, "POST", cfg.BackendURL+"/api/agent/shutdown-notify", bytes.NewReader(payload))
	if err != nil {
		slog.Warn("shutdown_notify.request_failed", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.AgentToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("shutdown_notify.send_failed", "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		slog.Info("shutdown_notify.sent", "status", resp.StatusCode)
	} else {
		slog.Warn("shutdown_notify.rejected", "status", resp.StatusCode)
	}
}

// startCloudflared spawns `cloudflared tunnel run --token <TOKEN>` as a subprocess.
// It restarts automatically on unexpected exit with exponential backoff.
// Returns a stop function that cleanly shuts down the cloudflared process.
func startCloudflared(ctx context.Context, token string) func() {
	var mu sync.Mutex
	var cmd *exec.Cmd
	stopped := false

	go func() {
		backoff := time.Second
		const maxBackoff = 30 * time.Second

		for {
			mu.Lock()
			if stopped {
				mu.Unlock()
				return
			}

			cmd = exec.CommandContext(ctx, "cloudflared", "tunnel", "run", "--token", token)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			mu.Unlock()

			slog.Info("cloudflared.connector_start")
			err := cmd.Run()

			mu.Lock()
			if stopped {
				mu.Unlock()
				slog.Info("cloudflared.stopped")
				return
			}
			mu.Unlock()

			if ctx.Err() != nil {
				slog.Info("cloudflared.context_cancelled")
				return
			}

			if err != nil {
				slog.Error("cloudflared.exit_error", "error", err, "restart_in", backoff.String())
			} else {
				slog.Warn("cloudflared.exit_unexpected", "restart_in", backoff.String())
			}

			select {
			case <-time.After(backoff):
				// Exponential backoff: 1s, 2s, 4s, 8s, ... max 30s
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return func() {
		mu.Lock()
		stopped = true
		c := cmd
		mu.Unlock()

		if c != nil && c.Process != nil {
			slog.Info("cloudflared.sending_sigterm")
			_ = c.Process.Signal(syscall.SIGTERM)

			// Wait up to 5 seconds for graceful exit
			done := make(chan struct{})
			go func() {
				_ = c.Wait()
				close(done)
			}()

			select {
			case <-done:
				slog.Info("cloudflared.stopped_gracefully")
			case <-time.After(5 * time.Second):
				slog.Warn("cloudflared.force_kill")
				_ = c.Process.Kill()
			}
		}
	}
}

// prefetchGCPMetadata attempts to read agent config from GCP instance metadata.
// On non-GCP environments (dev), this is a no-op.
func prefetchGCPMetadata(cfg *config.AgentConfig) {
	// Try to read from GCP metadata service
	client := &http.Client{}

	pairs := []struct {
		key  string
		dest *string
	}{
		{"agent-token", &cfg.AgentToken},
		{"backend-url", &cfg.BackendURL},
		{"host-id", &cfg.HostID},
		{"tunnel-token", &cfg.TunnelToken},
		{"rootfs-gcs-manifest", &cfg.RootfsGCSManifest},
		{"openclaw-gcs-manifest", &cfg.OpenClawManifestURI},
		{"hermes-gcs-manifest", &cfg.HermesManifestURI},
		{"agent-gcs-manifest", &cfg.AgentGCSManifest},
		{"browser-rootfs-gcs-manifest", &cfg.BrowserRootfsGCSManifest},
		{"browser-rootfs-version", &cfg.BrowserRootfsVersion},
	}

	for _, p := range pairs {
		if *p.dest != "" {
			continue // already set from environment
		}

		req, err := http.NewRequest("GET",
			"http://metadata.google.internal/computeMetadata/v1/instance/attributes/"+p.key, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Metadata-Flavor", "Google")

		resp, err := client.Do(req)
		if err != nil {
			continue // not on GCP, skip silently
		}

		if resp.StatusCode == http.StatusOK {
			buf := make([]byte, 4096)
			n, _ := resp.Body.Read(buf)
			if n > 0 {
				val := strings.TrimSpace(string(buf[:n]))
				*p.dest = val
				slog.Info("metadata.prefetch", "key", p.key)
			}
		}
		_ = resp.Body.Close()
	}
}

// startVMMetricsSampler launches the per-host VM resource sampler. Gated by
// the same {BackendURL, HostID, AgentToken} triple as heartbeat — every host
// that can heartbeat can also push metrics. HostID is parsed once at boot;
// invalid values disable the sampler with a warning rather than crashing.
func startVMMetricsSampler(ctx context.Context, cfg *config.AgentConfig) {
	hostIDInt, err := strconv.Atoi(cfg.HostID)
	if err != nil || hostIDInt <= 0 {
		slog.Warn("vmmetrics.sampler_disabled", "reason", "invalid host_id", "host_id", cfg.HostID)
		return
	}

	pusher := &sampler.HTTPPusher{
		BackendURL: cfg.BackendURL,
		AgentToken: cfg.AgentToken,
	}
	s := sampler.New(sampler.Options{
		Root:   VMMetricsCgroupRoot,
		HostID: hostIDInt,
		Pusher: pusher,
	})

	go func() {
		slog.Info("vmmetrics.sampler_start", "host_id", hostIDInt, "root", VMMetricsCgroupRoot)
		if err := s.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("vmmetrics.sampler_exit", "error", err.Error())
		} else {
			slog.Info("vmmetrics.sampler_exit_clean")
		}
	}()
}

// runHeartbeat sends periodic heartbeats to the backend control plane.
// Reports: host_id, external_ip, agent_version. Runs every 60s.
func runHeartbeat(ctx context.Context, cfg *config.AgentConfig, srv *agentapi.Server) {
	// Send first heartbeat immediately, then every 60s
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	sendHeartbeat(ctx, cfg, srv)
	for {
		select {
		case <-ticker.C:
			sendHeartbeat(ctx, cfg, srv)
		case <-ctx.Done():
			return
		}
	}
}

// backupStoreInit uses mutex+flag instead of sync.Once so that transient
// failures (e.g. GCS client creation) can be retried on the next heartbeat.
var (
	backupStoreInitMu   sync.Mutex
	backupStoreInitDone bool
)

func sendHeartbeat(ctx context.Context, cfg *config.AgentConfig, srv *agentapi.Server) {
	externalIP := getExternalIP()
	if externalIP == "" {
		slog.Warn("heartbeat.no_external_ip")
		return
	}

	// Read staged rootfs versions from cached manifest sidecar files
	var rootfsVer, browserRootfsVer, openclawVer string
	if m, err := rootfs.ReadCachedManifest(filepath.Join(cfg.StateDir, ".rootfs-manifest.json")); err == nil {
		rootfsVer = m.Version
	}
	if m, err := rootfs.ReadCachedManifest(filepath.Join(agentBrowserStateDir(cfg), ".browser-rootfs-manifest.json")); err == nil {
		browserRootfsVer = m.Version
	}
	if m, err := rootfs.ReadCachedManifest(filepath.Join(cfg.StateDir, ".openclaw-manifest.json")); err == nil {
		openclawVer = m.Version
	}

	heartbeatData := map[string]interface{}{
		"host_id":                cfg.HostID,
		"external_ip":            externalIP,
		"agent_version":          version.Version,
		"rootfs_version":         rootfsVer,
		"browser_rootfs_version": browserRootfsVer,
		"openclaw_version":       openclawVer,
		"capabilities":           buildHostCapabilities(cfg),
	}
	if srv != nil {
		vmCount, vmVersions := srv.HeartbeatVMVersions(ctx)
		heartbeatData["vm_count"] = vmCount
		if len(vmVersions) > 0 {
			heartbeatData["vm_versions"] = vmVersions
		}
	}
	if cfg.AgentEndpoint != "" {
		heartbeatData["agent_endpoint"] = cfg.AgentEndpoint
	}
	payload, _ := json.Marshal(heartbeatData)

	url := strings.TrimSuffix(cfg.BackendURL, "/") + "/api/agent/heartbeat"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		slog.Error("heartbeat.request_failed", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.AgentToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("heartbeat.send_failed", "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		slog.Warn("heartbeat.rejected", "status", resp.StatusCode, "body", string(body))
		return
	}

	// Parse heartbeat response for config updates (e.g. backup enabled signal)
	var hbResp struct {
		BackupEnabled bool `json:"backup_enabled,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&hbResp); err == nil && hbResp.BackupEnabled {
		// Lazily initialize backup store on first heartbeat that signals backups are enabled.
		// Uses mutex+flag instead of sync.Once so transient failures can retry.
		backupStoreInitMu.Lock()
		if backupStoreInitDone {
			backupStoreInitMu.Unlock()
		} else if srv.GetBackupStore() != nil {
			// Already initialized at startup — mark done
			backupStoreInitDone = true
			backupStoreInitMu.Unlock()
		} else {
			if cfg.BackupGCSBucket == "" {
				slog.Warn("backup.heartbeat_init.skipped", "reason", "BACKUP_GCS_BUCKET not configured")
				backupStoreInitMu.Unlock()
				return
			}
			if cfg.BackupGCSPrefix == "" {
				cfg.BackupGCSPrefix = "backups"
			}

			var gcsOpts []option.ClientOption
			if cfg.GCSServiceAccountKey != "" {
				gcsOpts = append(gcsOpts, option.WithCredentialsJSON([]byte(cfg.GCSServiceAccountKey))) //nolint:staticcheck // TODO: migrate to credentials.NewJSONCredential
			}
			gcsClient, gcsErr := storage.NewClient(ctx, gcsOpts...)
			if gcsErr != nil {
				slog.Warn("backup.heartbeat_init.gcs_client_failed", "error", gcsErr)
				backupStoreInitMu.Unlock()
			} else {
				bs := backup.NewGCSStore(gcsClient, cfg.BackupGCSBucket, cfg.BackupGCSPrefix)
				srv.SetBackupStore(bs)
				backupStoreInitDone = true
				backupStoreInitMu.Unlock()
				slog.Info("backup.heartbeat_init.complete", "bucket", cfg.BackupGCSBucket, "prefix", cfg.BackupGCSPrefix)
			}
		}
	}

	slog.Debug("heartbeat.sent", "ip", externalIP, "host_id", cfg.HostID)
}

// getExternalIP reads the VM's external IP.
// For enrolled hosts (AGENT_ENDPOINT set), uses that directly to avoid
// a 2-second GCP metadata timeout on every heartbeat. Falls back to
// GCP metadata for GCE hosts, then ifconfig.me as last resort.
func getExternalIP() string {
	// Enrolled hosts: extract IP from AGENT_ENDPOINT (e.g. "http://1.2.3.4:9090")
	if endpoint := os.Getenv("AGENT_ENDPOINT"); endpoint != "" {
		if u, err := url.Parse(endpoint); err == nil {
			if host := u.Hostname(); host != "" {
				return host
			}
		}
	}

	// GCP hosts: read from instance metadata
	if ip := getGCPExternalIP(); ip != "" {
		return ip
	}

	// Last resort: query external IP service
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("https://ifconfig.me")
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

func getGCPExternalIP() string {
	req, err := http.NewRequest("GET",
		"http://metadata.google.internal/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Metadata-Flavor", "Google")

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

// ensureSystemdKillModeOverride installs a systemd drop-in that prevents the
// agent's cgroup cleanup from killing running Firecracker VMs when systemd
// stops the agent (e.g. during self-update). Without this, the default
// KillMode=control-group tears down every Firecracker process along with
// the agent, so Recover() on the next start only finds dead PIDs.
//
// Idempotent: only writes the file if it's missing or has different content,
// and only runs systemctl daemon-reload on change. Silently does nothing if
// the agent is not running under systemd or lacks permission.
func ensureSystemdKillModeOverride() {
	const overrideDir = "/etc/systemd/system/ocm-agent.service.d"
	const overrideFile = overrideDir + "/killmode.conf"
	const desired = `# Managed by ocm-agent. Do not edit by hand.
# Only kill the agent process on stop; leave Firecracker children alive so
# Recover() can reattach them on the next start. Default KillMode=control-group
# would kill every running VM during agent self-updates.
[Service]
KillMode=process
TimeoutStopSec=30
`

	// Quick guard: only attempt this if we're running on a systemd-based host
	// with /etc/systemd/system present. Avoids noisy warnings in tests.
	if _, statErr := os.Stat("/etc/systemd/system"); statErr != nil {
		return
	}

	if existing, err := os.ReadFile(overrideFile); err == nil && string(existing) == desired {
		// Already installed with the same content — nothing to do.
		return
	}

	if err := os.MkdirAll(overrideDir, 0755); err != nil {
		slog.Warn("systemd.override.mkdir_failed", "dir", overrideDir, "error", err)
		return
	}
	if err := os.WriteFile(overrideFile, []byte(desired), 0644); err != nil {
		slog.Warn("systemd.override.write_failed", "path", overrideFile, "error", err)
		return
	}

	slog.Info("systemd.override.installed", "path", overrideFile, "note", "takes effect on next agent restart")

	// Reload systemd so the override is picked up. Best-effort: if this
	// fails (e.g. container without full systemd), the next host-level
	// daemon-reload will pick it up.
	cmd := exec.Command("systemctl", "daemon-reload")
	if err := cmd.Run(); err != nil {
		slog.Warn("systemd.daemon_reload.failed", "error", err,
			"note", "override file is written, but may not take effect until next reboot")
	}
}
