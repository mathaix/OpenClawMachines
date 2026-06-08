package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
	"github.com/mathaix/openclawmachines/backend/internal/api"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/composio"
	"github.com/mathaix/openclawmachines/backend/internal/config"
	"github.com/mathaix/openclawmachines/backend/internal/email"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/fleet"
	"github.com/mathaix/openclawmachines/backend/internal/kvstore"
	"github.com/mathaix/openclawmachines/backend/internal/machines"
	"github.com/mathaix/openclawmachines/backend/internal/provisioner"
	"github.com/mathaix/openclawmachines/backend/internal/reconciler"
	"github.com/mathaix/openclawmachines/backend/internal/routing"
	"github.com/mathaix/openclawmachines/backend/internal/secrets"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/internal/tunnel"
	"github.com/mathaix/openclawmachines/backend/internal/vmmetrics/maint"
	"github.com/mathaix/openclawmachines/backend/internal/workflows"
	"github.com/mathaix/openclawmachines/backend/pkg/logging"
	"github.com/mathaix/openclawmachines/backend/pkg/version"
)

func main() {
	logging.Init("control-plane")
	slog.Info("server.start", "version", version.Version, "commit", version.GitCommit, "built", version.BuildTime)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config.load_failed", "error", err)
		os.Exit(1)
	}
	slog.Info("server.profile.configured", "profile", cfg.ControlPlaneProfile)
	slog.Info("server.browser_rootfs.config",
		"manifest_uri", cfg.BrowserRootfsGCSManifest,
		"version", cfg.BrowserRootfsVersion,
		"default_manifest_uri", config.DefaultBrowserRootfsManifestURI,
		"default_version", config.DefaultBrowserRootfsVersion,
		"legacy_manifest_uri", config.StableBrowserRootfsManifestURI,
		"kernel_manifest_uri", config.ExperimentalKernelBrowserManifestURI)

	// Enforce strict mode: must be "api" or "worker", no combined mode.
	// Cloud Run runs as "api" (enqueue-only), worker fleet runs as "worker" (executor-only).
	if cfg.RunMode == "" {
		cfg.RunMode = "api" // default to API-only for Cloud Run
		slog.Warn("server.run_mode_defaulted", "mode", "api", "hint", "set RUN_MODE=worker for executor fleet")
	}
	if cfg.RunMode != "api" && cfg.RunMode != "worker" {
		slog.Error("server.invalid_run_mode", "mode", cfg.RunMode, "valid", "api, worker")
		os.Exit(1)
	}
	isAPI := cfg.RunMode == "api"
	isWorker := cfg.RunMode == "worker"
	if cfg.ControlPlaneProfile == config.ProfileLocal && cfg.AuthMode == "dev" && os.Getenv("OCM_ALLOW_DEV_AUTH") != "1" {
		if err := os.Setenv("OCM_ALLOW_DEV_AUTH", "1"); err != nil {
			slog.Error("auth.dev_mode_allow_failed", "error", err)
			os.Exit(1)
		}
		slog.Warn("auth.dev_mode_allowed_for_local_profile")
	}

	ctx := context.Background()

	db, err := store.NewPostgresStore(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database.connect_failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// Cancellable context for background goroutines
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// --- Worker-only mode: build server first so we can register workflows before DBOS launch ---
	var workerSrv *api.Server
	if isWorker && !isAPI {
		// Workers need infrastructure for migration workflows (agent, placement, KV, tunnel, machines)
		placementSvc := fleet.NewPlacementService(db, fleet.PlacementConfig{
			DefaultRegion: cfg.GCPRegion,
			ExpectedImage: cfg.SnapshotName,
			Strategy:      fleet.Spread,
		})
		agentCli := agentclient.New(cfg.AgentToken)
		tunnelMgr := cloudflareTunnelManagerOrExit(cfg)
		kv := cloudflareKVStoreOrExit(cfg)

		// Resolve encryption key (needed for machine secrets in migrations)
		encryptionKey := cfg.SecretEncryptionKey
		if cfg.GCPSecretName != "" {
			key, err := secrets.FetchSecret(ctx, cfg.GCPSecretName)
			if err != nil {
				slog.Error("config.encryption_key_fetch_failed", "error", err)
				os.Exit(1)
			}
			encryptionKey = key
		} else if encryptionKey == "" && cfg.RequiresHostedIntegrations() {
			slog.Error("config.missing_encryption_key", "error", "no encryption key configured (set GCP_SECRET_NAME or SECRET_ENCRYPTION_KEY)")
			os.Exit(1)
		}
		if encryptionKey != "" && len(encryptionKey) != 32 {
			slog.Error("config.invalid_encryption_key", "error", "encryption key must be exactly 32 bytes", "got_bytes", len(encryptionKey))
			os.Exit(1)
		}

		kvAdapter := routing.NewKVAdapter(kv)
		routeSvc := routing.NewWithDomain(tunnelMgr, kvAdapter, db, cfg.DataPlaneDomain)

		// Single signer instance shared by seed-config assembly (via RuntimeService)
		// and live-config push (via Server.signComposioProxyToken).
		composioSigner := resolveComposioProxyTokenSigner(cfg.JWTSecret)

		machineRuntime := machines.NewRuntimeService(db, placementSvc, agentCli, tunnelMgr, nil, machines.RuntimeConfig{
			RootfsDataVersion:            cfg.RootfsDataVersion,
			CfSSHCAPubKey:                cfg.CfSSHCAPubKey,
			SecretKey:                    encryptionKey,
			ProxyBaseURL:                 cfg.ProxyBaseURL,
			NebiusAPIKey:                 cfg.NebiusAPIKey,
			OpikAPIURL:                   resolveOpikAPIURL(),
			ComposioAPIURL:               resolveComposioAPIURL(),
			ComposioProxyTokenSigner:     composioSigner,
			EnableRuntimeVersionResolver: cfg.EnableRuntimeVersionResolver,
			OpenClawManifestURI:          cfg.OpenClawManifestURI,
			HermesManifestURI:            cfg.HermesManifestURI,
			HermesRootfsManifestURI:      cfg.HermesRootfsManifestURI,
		}, routeSvc)

		workerSrv = api.NewWorkerServer(db, placementSvc, agentCli, kv, tunnelMgr, machineRuntime, cfg.BackupMasterKey)
		workerSrv.SetComposioProxyTokenSigner(composioSigner)
	}

	// --- Workflow service (both modes need this) ---
	// Register callback must be set before NewService when EnableRuntime=true,
	// because DBOS requires workflows registered before Launch.
	var registerFn func(dbos.DBOSContext)
	if workerSrv != nil {
		registerFn = func(dbosCtx dbos.DBOSContext) {
			workerSrv.RegisterWorkflows(dbosCtx)
		}
	}
	workflowSvc, err := workflows.NewService(ctx, db, workflows.Config{
		AppName:       "openclawmachines-control-plane",
		DatabaseURL:   cfg.DatabaseURL,
		EnableRuntime: cfg.EnableDurableWorkflows && isWorker,
		EnableEnqueue: cfg.EnableDurableWorkflows && isAPI,
		Register:      registerFn,
	})
	if err != nil {
		slog.Error("workflow.init_failed", "error", err)
		os.Exit(1)
	}
	defer workflowSvc.Close(10 * time.Second)

	// --- Worker-only mode: wire up remaining dependencies and block ---
	if isWorker && !isAPI {
		workerSrv.SetWorkflowService(workflowSvc)

		// Launch DBOS executor now that workflows are registered
		if err := workflowSvc.Launch(); err != nil {
			slog.Error("workflow.launch_failed", "error", err)
			os.Exit(1)
		}

		if cfg.ResendAPIKey != "" {
			workerSrv.SetEmailClient(email.NewClient(cfg.ResendAPIKey))
			slog.Info("email.configured", "provider", "resend")
		}
		workerSrv.SetFrontendURL(cfg.FrontendURL)

		slog.Info("worker.started", "executor_id", cfg.ExecutorID, "mode", "worker")

		// Health endpoint for MIG auto-healing
		healthMux := http.NewServeMux()
		healthMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			if err := db.Ping(r.Context()); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("db unhealthy"))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		healthServer := &http.Server{Addr: ":" + cfg.Port, Handler: healthMux}
		go func() { _ = healthServer.ListenAndServe() }()

		// Signal handling + preemption
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go watchForPreemption(sigCh)

		<-sigCh
		slog.Info("worker.shutdown_start")
		cancel()
		_ = healthServer.Shutdown(context.Background())
		return
	}

	// --- API mode (or both modes): full infrastructure ---

	// Auth setup based on AUTH_MODE
	var a *auth.Auth
	var cfAuth *auth.CfAccessAuth
	var firebaseAuth *auth.FirebaseAuth
	switch cfg.AuthMode {
	case "dev":
		slog.Warn("auth.dev_mode_enabled", "email", cfg.DevUserEmail)
		if cfg.JWTSecret != "" && len(cfg.JWTSecret) >= 16 {
			a = auth.New(cfg.JWTSecret)
		}
	case "cfaccess":
		if cfg.CfAccessTeamDomain == "" || cfg.CfAccessAUD == "" {
			slog.Error("cfaccess.config_missing", "error", "CF_ACCESS_TEAM_DOMAIN and CF_ACCESS_AUD required for cfaccess mode")
			os.Exit(1)
		}
		cfAuth = auth.NewCfAccess(cfg.CfAccessTeamDomain, cfg.CfAccessAUD)
		if cfg.JWTSecret != "" && len(cfg.JWTSecret) >= 16 {
			a = auth.New(cfg.JWTSecret) // for dual-mode fallback
		}
	case "firebase":
		if cfg.FirebaseProjectID == "" {
			slog.Error("firebase.config_missing", "error", "FIREBASE_PROJECT_ID required for firebase mode")
			os.Exit(1)
		}
		firebaseAuth = auth.NewFirebaseAuth(cfg.FirebaseProjectID)
		if cfg.JWTSecret != "" && len(cfg.JWTSecret) >= 16 {
			a = auth.New(cfg.JWTSecret)
		}
		slog.Info("auth.firebase_enabled", "project_id", cfg.FirebaseProjectID)
	default:
		slog.Error("auth.unknown_mode", "mode", cfg.AuthMode)
		os.Exit(1)
	}

	placementSvc := fleet.NewPlacementService(db, fleet.PlacementConfig{
		DefaultRegion: cfg.GCPRegion,
		ExpectedImage: cfg.SnapshotName,
		Strategy:      fleet.Spread,
	})
	agentCli := agentclient.New(cfg.AgentToken)
	if missing := hostedProvisionerMissingConfig(cfg); missing != "" {
		slog.Error("host.provisioner.not_configured", "error", missing+" is required for CONTROL_PLANE_PROFILE=hosted")
		os.Exit(1)
	}
	tunnelMgr := cloudflareTunnelManagerOrExit(cfg)
	var prov *provisioner.Provisioner
	if cfg.GCPProject != "" && tunnelMgr != nil {
		prov = provisioner.New(provisioner.Config{
			Store:                    db,
			AgentClient:              agentCli,
			Tunnel:                   tunnelMgr,
			Project:                  cfg.GCPProject,
			Zone:                     cfg.GCPZone,
			Region:                   cfg.GCPRegion,
			Snapshot:                 cfg.SnapshotName,
			HostImage:                cfg.HostImage,
			ArtifactBucket:           cfg.ArtifactBucket,
			ArtifactBaseURL:          cfg.ArtifactBaseURL,
			ProvisioningModel:        cfg.HostProvisioningModel,
			AgentToken:               cfg.AgentToken,
			BackendURL:               cfg.BackendURL,
			DataPlaneDomain:          cfg.DataPlaneDomain,
			DataDiskSizeGB:           cfg.DataDiskSizeGB,
			RootfsGCSManifest:        cfg.RootfsGCSManifest,
			AgentGCSManifest:         cfg.AgentGCSManifest,
			BrowserRootfsGCSManifest: cfg.BrowserRootfsGCSManifest,
			BrowserRootfsVersion:     cfg.BrowserRootfsVersion,
			BackupMasterKey:          cfg.BackupMasterKey,
		})
	} else {
		slog.Info("host.provisioner.disabled",
			"profile", cfg.ControlPlaneProfile,
			"reason", "GCP_PROJECT and Cloudflare tunnel configuration are required for managed GCE host provisioning")
	}
	// Resolve encryption key: GCP Secret Manager > env var > fatal
	encryptionKey := cfg.SecretEncryptionKey
	if cfg.GCPSecretName != "" {
		slog.Info("config.fetching_encryption_key", "source", "gcp_secret_manager", "secret_name", cfg.GCPSecretName)
		key, err := secrets.FetchSecret(ctx, cfg.GCPSecretName)
		if err != nil {
			slog.Error("config.encryption_key_fetch_failed", "error", err)
			os.Exit(1)
		}
		encryptionKey = key
	} else if encryptionKey == "" {
		if cfg.RequiresHostedIntegrations() {
			slog.Error("config.missing_encryption_key", "error", "no encryption key configured (set GCP_SECRET_NAME or SECRET_ENCRYPTION_KEY)")
			os.Exit(1)
		}
		slog.Warn("config.encryption_key_disabled", "profile", cfg.ControlPlaneProfile, "hint", "set SECRET_ENCRYPTION_KEY before using credentials or machine secrets")
	}
	if encryptionKey != "" && len(encryptionKey) != 32 {
		slog.Error("config.invalid_encryption_key", "error", "encryption key must be exactly 32 bytes", "got_bytes", len(encryptionKey))
		os.Exit(1)
	}
	cfg.SecretEncryptionKey = encryptionKey

	kv := cloudflareKVStoreOrExit(cfg)

	kvAdapter := routing.NewKVAdapter(kv)
	// Avoid a typed-nil interface: a nil *tunnel.Manager boxed into the
	// routing.TunnelManager interface compares != nil, which defeats the
	// nil-guard in routing.Service.SetupRoute and panics on machine start in
	// profiles without a Cloudflare tunnel (local/operator). Mirror the same
	// guard NewServer uses for tunnelCreator.
	var routeTunnel routing.TunnelManager
	if tunnelMgr != nil {
		routeTunnel = tunnelMgr
	}
	routeSvc := routing.NewWithDomain(routeTunnel, kvAdapter, db, cfg.DataPlaneDomain)

	// Start route projector (DB → KV sync every 60s)
	if kvAdapter != nil {
		routeReader := routing.NewStoreAdapter(db)
		projector := routing.NewProjector(routeReader, kvAdapter)
		go projector.Start(ctx, 60*time.Second)
	} else {
		slog.Info("routing.projector.disabled", "profile", cfg.ControlPlaneProfile, "reason", "Cloudflare KV not configured")
	}

	// Single signer instance shared by seed-config assembly (via RuntimeService)
	// and live-config push (via Server.signComposioProxyToken).
	composioSigner := resolveComposioProxyTokenSigner(cfg.JWTSecret)

	machineRuntime := machines.NewRuntimeService(db, placementSvc, agentCli, tunnelMgr, nil, machines.RuntimeConfig{
		RootfsDataVersion:            cfg.RootfsDataVersion,
		CfSSHCAPubKey:                cfg.CfSSHCAPubKey,
		SecretKey:                    cfg.SecretEncryptionKey,
		ProxyBaseURL:                 cfg.ProxyBaseURL,
		NebiusAPIKey:                 cfg.NebiusAPIKey,
		OpikAPIURL:                   resolveOpikAPIURL(),
		ComposioAPIURL:               resolveComposioAPIURL(),
		ComposioProxyTokenSigner:     composioSigner,
		EnableRuntimeVersionResolver: cfg.EnableRuntimeVersionResolver,
		OpenClawManifestURI:          cfg.OpenClawManifestURI,
		HermesManifestURI:            cfg.HermesManifestURI,
		HermesRootfsManifestURI:      cfg.HermesRootfsManifestURI,
	}, routeSvc)

	srv := api.NewServer(ctx, db, a, cfg.AuthMode, cfAuth, firebaseAuth, placementSvc, agentCli, prov, tunnelMgr, cfg.CORSOrigins, cfg.SecretEncryptionKey, kv, cfg.CFServiceTokenID, cfg.CFServiceTokenSecret, cfg.AgentToken, cfg.RootfsDataVersion, cfg.DevUserEmail, cfg.CfSSHCAPubKey, cfg.ProxyBaseURL, cfg.OAuthClientID, cfg.OAuthClientSecret, machineRuntime, cfg.BackendURL, cfg.RootfsGCSManifest, cfg.AgentGCSManifest, cfg.BrowserRootfsGCSManifest, cfg.BrowserRootfsVersion, cfg.GCSServiceAccountKey, cfg.BackupMasterKey, cfg.BackupGCSBucket, cfg.BackupGCSPrefix)

	srv.SetRouting(routeSvc)

	if key := os.Getenv("COMPOSIO_CONSUMER_KEY"); key != "" {
		srv.SetComposioClient(composio.NewClient("https://backend.composio.dev", key))
		slog.Info("composio.configured")
	}
	srv.SetComposioAPIURL(resolveComposioAPIURL())
	srv.SetComposioProxyTokenSigner(composioSigner)

	// Register workflows then launch executor (order matters: register before launch)
	workflowSvc.RegisterWorkflows(srv.RegisterWorkflows)
	if err := workflowSvc.Launch(); err != nil {
		slog.Error("workflow.launch_failed", "error", err)
		os.Exit(1)
	}

	srv.SetWorkflowService(workflowSvc)

	// Activity log for business-level event tracking
	activityResolver := events.NewStoreResolver(db)
	activityLogger := events.New(db, activityResolver)
	srv.SetActivity(activityLogger)
	if prov != nil {
		prov.SetActivity(activityLogger)
	}
	slog.Info("activity.configured")

	if cfg.ResendAPIKey != "" {
		srv.SetEmailClient(email.NewClient(cfg.ResendAPIKey))
		slog.Info("email.configured", "provider", "resend")
	}
	srv.SetFrontendURL(cfg.FrontendURL)
	srv.SetDataPlaneDomain(cfg.DataPlaneDomain)
	srv.SetCookieDomain(cfg.CookieDomain)
	srv.SetCfAccessAuthDomain(cfg.CfAccessTeamDomain)
	tunnel.StartReaper(ctx, tunnelMgr, db, 10*time.Minute, cfg.DataPlaneDomain)
	if cfg.SSHCAPrivateKey != "" {
		srv.SetSSHCAPrivateKey(cfg.SSHCAPrivateKey)
		slog.Info("ssh_ca.configured")
	}

	// OAuth refresh is now handled inline by the proxy (no background loop).
	srv.StartInvitationExpiryLoop(ctx)
	srv.StartStallDetectionLoop(ctx)
	srv.StartBrowseSessionJanitor(ctx)
	srv.StartBrowserVMReconciler(ctx, 60*time.Second)

	// VM-metrics maintenance: partition lifecycle + downsampling + retention.
	// Each job is advisory-locked so multiple Cloud Run instances cooperate.
	go maint.Run(ctx, db.Pool(), maint.SchedulerOptions{})
	slog.Info("vmmetrics.maint.scheduler.started")

	if cfg.GCPProject == "" {
		slog.Info("reconciler.gcp.disabled", "profile", cfg.ControlPlaneProfile, "reason", "GCP_PROJECT not configured")
	} else {
		computeClient, err := compute.NewInstancesRESTClient(ctx)
		if err != nil {
			slog.Error("reconciler.compute_client.failed", "error", err)
		} else {
			instanceChecker := reconciler.NewGCPInstanceChecker(computeClient)
			hostReconciler := reconciler.New(db, instanceChecker, machineRuntime, cfg.GCPProject, 180*time.Second)
			hostReconciler.SetActivity(activityLogger)
			go hostReconciler.Start(ctx, 60*time.Second)
			slog.Info("reconciler.started")
		}
	}

	httpServer := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: srv,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("server.shutdown_start")
		cancel()
		_ = httpServer.Shutdown(context.Background())
	}()

	slog.Info("server.listen", "port", cfg.Port, "mode", cfg.RunMode)
	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server.error", "error", err)
		os.Exit(1)
	}
}

// watchForPreemption polls the GCE metadata server for the preemption signal.
// When preemption is detected, it sends SIGTERM to the signal channel so main
// unblocks and deferred cleanup (including dbos.Shutdown) runs.
func watchForPreemption(sigCh chan<- os.Signal) {
	client := &http.Client{Timeout: 0} // no timeout — long-poll
	req, err := http.NewRequest("GET",
		"http://metadata.google.internal/computeMetadata/v1/instance/preempted?wait_for_change=true",
		nil)
	if err != nil {
		return
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := client.Do(req)
	if err != nil {
		// Not on GCE, or metadata server unreachable — stop polling
		slog.Debug("preemption.watch_disabled", "error", err)
		return
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if strings.TrimSpace(string(body)) == "TRUE" {
		slog.Warn("preemption.detected", "action", "graceful_shutdown")
		sigCh <- syscall.SIGTERM
	}
}

// resolveOpikAPIURL derives the Opik tracing endpoint URL from env vars.
func resolveOpikAPIURL() string {
	if u := os.Getenv("OPIK_API_URL"); u != "" {
		return u
	}
	if u := os.Getenv("PUBLIC_URL"); u != "" {
		return u + "/api/opik"
	}
	return ""
}

// resolveComposioAPIURL derives the Composio backend proxy URL from env vars.
func resolveComposioAPIURL() string {
	if u := os.Getenv("COMPOSIO_API_URL"); u != "" {
		return u
	}
	if u := os.Getenv("PUBLIC_URL"); u != "" {
		return u + "/api/composio"
	}
	return ""
}

func cloudflareTunnelManagerOrExit(cfg *config.Config) *tunnel.Manager {
	if !cfg.CloudflareTunnelConfigured() {
		if cfg.RequiresHostedIntegrations() {
			slog.Error("tunnel.not_configured", "error", "CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID, CLOUDFLARE_ZONE_ID required for CONTROL_PLANE_PROFILE=hosted")
			os.Exit(1)
		}
		if cfg.HasCloudflareConfig() {
			slog.Warn("tunnel.disabled_incomplete_config", "profile", cfg.ControlPlaneProfile, "hint", "set CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID, and CLOUDFLARE_ZONE_ID to enable tunnels")
		} else {
			slog.Info("tunnel.disabled", "profile", cfg.ControlPlaneProfile)
		}
		return nil
	}
	return tunnel.New(cfg.CloudflareAPIToken, cfg.CloudflareAccountID, cfg.CloudflareZoneID)
}

func hostedProvisionerMissingConfig(cfg *config.Config) string {
	if cfg.RequiresHostedIntegrations() && strings.TrimSpace(cfg.GCPProject) == "" {
		return "GCP_PROJECT"
	}
	return ""
}

func cloudflareKVStoreOrExit(cfg *config.Config) *kvstore.KVStore {
	if !cfg.CloudflareKVConfigured() {
		if cfg.RequiresHostedIntegrations() {
			slog.Error("kv.not_configured", "error", "CLOUDFLARE_ACCOUNT_ID, CLOUDFLARE_KV_NAMESPACE_ID, CLOUDFLARE_API_TOKEN required for CONTROL_PLANE_PROFILE=hosted")
			os.Exit(1)
		}
		if cfg.HasCloudflareConfig() {
			slog.Warn("kv.disabled_incomplete_config", "profile", cfg.ControlPlaneProfile, "hint", "set CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID, and CLOUDFLARE_KV_NAMESPACE_ID to enable KV route sync")
		} else {
			slog.Info("kv.disabled", "profile", cfg.ControlPlaneProfile)
		}
		return nil
	}
	return kvstore.New(cfg.CloudflareAccountID, cfg.CloudflareKVNamespaceID, cfg.CloudflareAPIToken)
}

// resolveComposioProxyTokenSigner returns a closure that mints Composio proxy
// tokens scoped to a machine. nil when JWT_SECRET is not configured (dev mode);
// config assembly logs a warning in that case and the in-VM plugin will be
// unable to authenticate against the backend proxy.
func resolveComposioProxyTokenSigner(jwtSecret string) func(string) (string, error) {
	if jwtSecret == "" || len(jwtSecret) < 16 {
		return nil
	}
	a := auth.New(jwtSecret)
	return func(machineID string) (string, error) {
		return a.IssueComposioProxyToken(machineID, api.ComposioProxyTokenTTL)
	}
}
