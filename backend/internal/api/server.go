package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/gorilla/websocket"
	"google.golang.org/api/option"

	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/backup"
	"github.com/mathaix/openclawmachines/backend/internal/composio"
	"github.com/mathaix/openclawmachines/backend/internal/email"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/fleet"
	"github.com/mathaix/openclawmachines/backend/internal/kvstore"
	"github.com/mathaix/openclawmachines/backend/internal/machines"
	"github.com/mathaix/openclawmachines/backend/internal/provisioner"
	"github.com/mathaix/openclawmachines/backend/internal/routing"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/internal/tunnel"
	"github.com/mathaix/openclawmachines/backend/internal/workflows"
	"github.com/mathaix/openclawmachines/backend/pkg/tracing"
	"github.com/mathaix/openclawmachines/backend/pkg/version"
)

type Server struct {
	router                   chi.Router
	store                    store.Store
	auth                     *auth.Auth
	authMode                 string
	cfAuth                   *auth.CfAccessAuth
	firebaseAuth             *auth.FirebaseAuth
	placement                *fleet.PlacementService
	agentClient              *agentclient.Client
	provisioner              *provisioner.Provisioner
	tunnelMgr                *tunnel.Manager
	tunnelCreator            TunnelCreator
	kvStore                  *kvstore.KVStore
	machines                 *machines.RuntimeService
	secretKey                string
	cfServiceTokenID         string
	cfServiceTokenSec        string
	agentToken               string
	rootfsDataVersion        int
	cfSSHCAPubKey            string
	proxyBaseURL             string
	oauthClientID            string
	oauthClientSecret        string
	backendURL               string
	rootfsGCSManifest        string
	agentGCSManifest         string
	browserRootfsGCSManifest string
	browserRootfsVersion     string
	gcsServiceAccountKey     string
	backupMasterKey          string
	backupStore              backup.BackupStore
	vcpuOversubRatio         int
	wsUpgrader               websocket.Upgrader
	workflows                *workflows.Service
	emailClient              *email.Client
	frontendBaseURL          string
	activity                 *events.Activity
	routing                  *routing.Service
	opikAPIURL               string
	composioClient           *composio.Client
	composioAPIURL           string
	// composioProxyTokenSigner mints per-machine Composio proxy tokens. Wired
	// separately from s.auth so it survives worker-mode startup (which
	// constructs Server via NewWorkerServer without an auth instance).
	composioProxyTokenSigner func(machineID string) (string, error)
	dataPlaneDomain          string
	cfAccessAuthDomain       string
	sshCAPrivateKey          string // platform SSH CA private key (PEM)

	// vm-metrics ingest rate limiter, lazily initialized per server.
	vmMetricsRLOnce sync.Once
	vmMetricsRL     *vmMetricsRateLimiter

	// Host-agent updates run after the admin request returns. This map tracks
	// the in-flight operation status exposed through the admin polling endpoint.
	hostUpdateOpsMu sync.RWMutex
	hostUpdateOps   map[string]*hostUpdateOperation
}

// maxVCPUOversubRatio is the upper bound for the vCPU oversubscription ratio,
// matching the capacity_policies_cpu_ratio constraint (BETWEEN 1.0 AND 3.0).
const maxVCPUOversubRatio = 3

// vcpuOversubscriptionRatio reads VCPU_OVERSUBSCRIPTION_RATIO from the
// environment, validates it, and returns a safe value clamped to [1, 3].
func vcpuOversubscriptionRatio() int {
	raw := os.Getenv("VCPU_OVERSUBSCRIPTION_RATIO")
	if raw == "" {
		return 2 // default: 2x oversubscription
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		slog.Warn("vcpu_oversub_ratio.invalid", "value", raw, "error", err, "using_default", 2)
		return 2
	}
	if v < 1 {
		slog.Warn("vcpu_oversub_ratio.below_minimum", "value", v, "clamped_to", 1)
		return 1
	}
	if v > maxVCPUOversubRatio {
		slog.Warn("vcpu_oversub_ratio.exceeds_max", "value", v, "max", maxVCPUOversubRatio, "clamped_to", maxVCPUOversubRatio)
		return maxVCPUOversubRatio
	}
	return v
}

// NewWorkerServer creates a Server with the dependencies needed for workflow
// execution in worker-only mode (no HTTP router, no auth, no CORS).
// Migration workflows need agentClient, placement, machines, and kvStore.
func NewWorkerServer(s store.Store, placementSvc *fleet.PlacementService, agentCli *agentclient.Client, kv *kvstore.KVStore, tunnelMgr *tunnel.Manager, machineRuntime *machines.RuntimeService, backupMasterKey string) *Server {
	srv := &Server{
		store:           s,
		placement:       placementSvc,
		agentClient:     agentCli,
		kvStore:         kv,
		tunnelMgr:       tunnelMgr,
		machines:        machineRuntime,
		backupMasterKey: backupMasterKey,
		hostUpdateOps:   make(map[string]*hostUpdateOperation),
	}
	srv.opikAPIURL = os.Getenv("OPIK_API_URL")
	if srv.opikAPIURL == "" {
		srv.opikAPIURL = os.Getenv("PUBLIC_URL")
		if srv.opikAPIURL != "" {
			srv.opikAPIURL += "/api/opik"
		}
	}
	// Workers execute migration workflows that start/restart VMs, so they need
	// the same RuntimeService callbacks as the full API server.
	if machineRuntime != nil {
		machineRuntime.OnRunning = func(machineID string, isRestart bool) {
			ctx := context.Background()
			machine, err := srv.store.GetMachine(ctx, machineID)
			if err != nil {
				slog.Error("on_running.get_machine_failed", "machine_id", machineID, "error", err)
				return
			}
			if isRestart {
				slog.Info("on_running.restart_detected", "machine_id", machineID)
				if err := srv.waitForGatewayHealth(ctx, machine, 90*time.Second); err != nil {
					slog.Warn("on_running.gateway_not_ready", "machine_id", machineID, "error", err)
				}
				// Push config after restart to reflect any DB changes (e.g. auto-unpaired
				// browser VM) that happened before boot. The data volume has stale config
				// from the previous run; this ensures the running VM gets current state.
				srv.pushMachineConfigAsync(machineID)
			}
		}
	}
	if tunnelMgr != nil {
		srv.tunnelCreator = tunnelMgr
	}
	return srv
}

func NewServer(ctx context.Context, s store.Store, a *auth.Auth, authMode string, cfAuth *auth.CfAccessAuth, firebaseAuth *auth.FirebaseAuth, placementSvc *fleet.PlacementService, agentCli *agentclient.Client, prov *provisioner.Provisioner, tunnelMgr *tunnel.Manager, corsOrigins string, secretKey string, kv *kvstore.KVStore, cfTokenID, cfTokenSecret string, agentToken string, rootfsDataVersion int, devUserEmail string, cfSSHCAPubKey string, proxyBaseURL string, oauthClientID, oauthClientSecret string, machineRuntime *machines.RuntimeService, backendURL, rootfsGCSManifest, agentGCSManifest, browserRootfsGCSManifest, browserRootfsVersion, gcsServiceAccountKey, backupMasterKey, backupGCSBucket, backupGCSPrefix string) *Server {
	ratio := vcpuOversubscriptionRatio()
	slog.Info("vcpu_oversub_ratio.configured", "ratio", ratio)

	srv := &Server{
		store:                    s,
		auth:                     a,
		authMode:                 authMode,
		cfAuth:                   cfAuth,
		firebaseAuth:             firebaseAuth,
		placement:                placementSvc,
		agentClient:              agentCli,
		provisioner:              prov,
		tunnelMgr:                tunnelMgr,
		kvStore:                  kv,
		machines:                 machineRuntime,
		secretKey:                secretKey,
		cfServiceTokenID:         cfTokenID,
		cfServiceTokenSec:        cfTokenSecret,
		agentToken:               agentToken,
		rootfsDataVersion:        rootfsDataVersion,
		cfSSHCAPubKey:            cfSSHCAPubKey,
		proxyBaseURL:             proxyBaseURL,
		oauthClientID:            oauthClientID,
		oauthClientSecret:        oauthClientSecret,
		backendURL:               backendURL,
		rootfsGCSManifest:        rootfsGCSManifest,
		agentGCSManifest:         agentGCSManifest,
		browserRootfsGCSManifest: browserRootfsGCSManifest,
		browserRootfsVersion:     browserRootfsVersion,
		gcsServiceAccountKey:     gcsServiceAccountKey,
		backupMasterKey:          backupMasterKey,
		vcpuOversubRatio:         ratio,
		hostUpdateOps:            make(map[string]*hostUpdateOperation),
	}

	// Initialize backup store for direct GCS downloads (when host is unavailable).
	// Supports both explicit service account key and ADC (workload identity).
	if backupMasterKey == "" {
		slog.Warn("backup.disabled", "reason", "BACKUP_MASTER_KEY not configured — backups will not work")
	} else {
		slog.Info("backup.enabled", "bucket", backupGCSBucket, "prefix", backupGCSPrefix)
		var gcsOpts []option.ClientOption
		if gcsServiceAccountKey != "" {
			gcsOpts = append(gcsOpts, option.WithCredentialsJSON([]byte(gcsServiceAccountKey))) //nolint:staticcheck // TODO: migrate to credentials.NewJSONCredential
		}
		gcsClient, err := storage.NewClient(context.Background(), gcsOpts...)
		if err != nil {
			slog.Warn("backup.control_plane_gcs_init_failed", "error", err)
		} else {
			srv.backupStore = backup.NewGCSStore(gcsClient, backupGCSBucket, backupGCSPrefix)
		}

		// Backfill: enable backups for existing machines that don't have a key yet
		go srv.backfillBackupKeys(context.Background())
	}

	// Opik API URL: explicit env var takes priority, otherwise derive from PUBLIC_URL.
	srv.opikAPIURL = os.Getenv("OPIK_API_URL")
	if srv.opikAPIURL == "" {
		srv.opikAPIURL = os.Getenv("PUBLIC_URL")
		if srv.opikAPIURL != "" {
			srv.opikAPIURL += "/api/opik"
		}
	}

	// Set tunnelCreator to the tunnel manager (satisfies TunnelCreator interface)
	if tunnelMgr != nil {
		srv.tunnelCreator = tunnelMgr
	}

	// Wire function callbacks into the RuntimeService that depend on Server methods
	if machineRuntime != nil {
		machineRuntime.OnRunning = func(machineID string, isRestart bool) {
			ctx := context.Background()
			machine, err := srv.store.GetMachine(ctx, machineID)
			if err != nil {
				slog.Error("on_running.get_machine_failed", "machine_id", machineID, "error", err)
				return
			}
			if isRestart {
				slog.Info("on_running.restart_detected", "machine_id", machineID)
				if err := srv.waitForGatewayHealth(ctx, machine, 90*time.Second); err != nil {
					slog.Warn("on_running.gateway_not_ready", "machine_id", machineID, "error", err)
				}
				// Push config after restart to reflect any DB changes (e.g. auto-unpaired
				// browser VM) that happened before boot. The data volume has stale config
				// from the previous run; this ensures the running VM gets current state.
				srv.pushMachineConfigAsync(machineID)
			}
		}
	}

	r := chi.NewRouter()
	r.Use(slogRequestLogger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(traceIDMiddleware)

	origins := []string{"http://localhost:5173"}
	if corsOrigins != "" {
		origins = strings.Split(corsOrigins, ",")
		for i := range origins {
			origins[i] = strings.TrimSpace(origins[i])
		}
	}

	// Wildcard origin with credentials is a misconfiguration — disable credentials.
	allowCreds := true
	for _, o := range origins {
		if o == "*" {
			allowCreds = false
			break
		}
	}

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "Cf-Access-Jwt-Assertion"},
		AllowCredentials: allowCreds,
		MaxAge:           300,
	}))

	// WebSocket upgrader validates Origin against the same CORS origins.
	srv.wsUpgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // non-browser clients
			}
			for _, allowed := range origins {
				if allowed == "*" || allowed == origin {
					return true
				}
			}
			return false
		},
	}

	// Public routes
	r.Get("/health", srv.handleHealth)
	r.Post("/api/telemetry", srv.handleTelemetry)
	r.Post("/api/waitlist", srv.handleJoinWaitlist)
	r.Put("/api/waitlist", srv.handleUpdateWaitlistSurvey)
	r.Get("/api/invitations/{token}/public", srv.handleGetInvitationPublic)
	r.Get("/api/integrations/callback", srv.handleIntegrationCallback)
	r.Get("/api/platform/config", srv.handlePlatformConfig)
	r.Get("/api/catalog/providers", srv.handleListProviderCatalog)

	// Composio proxy — called by the plugin inside the VM (no agent token available).
	// Safe: only proxies to Composio REST API using the platform key, no secrets exposed.
	r.Get("/api/composio/tools", srv.handleComposioListTools)
	r.Post("/api/composio/actions/{action}/execute", srv.handleComposioExecuteAction)

	// Firebase session exchange (public — the Firebase ID token IS the auth)
	if srv.firebaseAuth != nil {
		r.Post("/api/auth/session/exchange", srv.handleSessionExchange)
	}

	// Logout (public — clears cookies regardless of session state)
	r.Post("/api/auth/logout", srv.handleLogout)

	// Authenticated routes
	r.Group(func(r chi.Router) {
		switch srv.authMode {
		case "dev":
			if os.Getenv("OCM_ALLOW_DEV_AUTH") != "1" {
				slog.Error("auth.dev_mode_blocked", "msg", "AUTH_MODE=dev requires OCM_ALLOW_DEV_AUTH=1 — refusing to start with dev bypass in production")
				panic("AUTH_MODE=dev requires OCM_ALLOW_DEV_AUTH=1")
			}
			r.Use(auth.DevBypassMiddleware(devUserEmail))
			r.Use(srv.userResolverMiddleware)
		case "cfaccess":
			r.Use(auth.DualModeMiddleware(srv.cfAuth, a))
			r.Use(srv.userResolverMiddleware)
		case "firebase":
			r.Use(auth.FirebaseMiddleware(srv.firebaseAuth, a))
			r.Use(srv.userResolverMiddleware)
		default:
			slog.Error("auth.unknown_mode", "mode", srv.authMode)
			panic("unrecognized AUTH_MODE: " + srv.authMode)
		}

		// Auth (authenticated)
		r.Get("/api/auth/me", srv.handleAuthMe)
		r.Post("/api/auth/cli-token", srv.handleCliToken)

		// Models & Sizes
		r.Get("/api/models", srv.handleListModels)
		r.Get("/api/channels", srv.handleListChannels)
		r.Get("/api/sizes", srv.handleListSizes)
		r.Get("/api/regions", srv.handleListRegions)

		// Accounts
		r.Get("/api/accounts", srv.handleListAccounts)
		r.Post("/api/accounts", srv.handleCreateAccount)

		// Invitations (user-scoped, not account-scoped)
		r.Get("/api/invitations/pending", srv.handleListPendingInvitations)
		r.Get("/api/invitations/{token}", srv.handleGetInvitation)
		r.Post("/api/invitations/{token}/accept", srv.handleAcceptInvitation)
		r.Post("/api/invitations/{token}/decline", srv.handleDeclineInvitation)
		r.Get("/api/workflows/{id}", srv.handleGetWorkflow)
		r.Get("/api/workflows/{id}/events", srv.handleListWorkflowEvents)

		// Account-scoped routes
		r.Route("/api/accounts/{accountId}", func(r chi.Router) {
			r.Use(srv.AccountMiddleware)

			r.Get("/", srv.handleGetAccount)
			r.Patch("/", srv.handleUpdateAccount)
			r.Get("/activity", srv.handleListAccountActivity)
			r.Get("/members", srv.handleListMembers)
			r.Put("/members/{userId}/role", srv.handleUpdateMemberRole)
			r.Delete("/members/{userId}", srv.handleRemoveMember)
			r.Post("/members/leave", srv.handleLeaveAccount)
			r.Get("/traces", srv.handleListAccountTraces)
			r.Get("/traces/{traceID}", srv.handleGetAccountTrace)
			r.Put("/traces/{traceID}/tags", srv.handleUpdateTraceTags)
			r.Get("/traces/{traceID}/feedback", srv.handleListTraceFeedback)
			r.Post("/traces/{traceID}/feedback", srv.handleCreateTraceFeedback)

			// Invitations (account-scoped)
			r.Post("/invitations", srv.handleCreateInvitation)
			r.Get("/invitations", srv.handleListInvitations)
			r.Delete("/invitations/{invitationId}", srv.handleRevokeInvitation)

			// Owner/admin-only account operations
			r.Group(func(r chi.Router) {
				r.Use(requireRole("owner", "admin"))

				// Credentials (account-wide routes removed — credentials are machine-scoped)

				// Providers (built-in + custom)
				r.Get("/providers", srv.handleListProviders)
				r.Post("/providers", srv.handleRegisterProvider)
				r.Delete("/providers/{name}", srv.handleUnregisterProvider)

				// Registry (admin CRUD)
				r.Get("/registry", srv.handleAdminListRegistryEntries)
				r.Post("/registry", srv.handleAdminCreateRegistryEntry)
				r.Get("/registry/{entryId}", srv.handleAdminGetRegistryEntry)
				r.Put("/registry/{entryId}", srv.handleAdminUpdateRegistryEntry)
				r.Delete("/registry/{entryId}", srv.handleAdminDeleteRegistryEntry)

				// Usage & Billing
				r.Get("/usage", srv.handleGetAccountUsage)
			})

			// OpenClaw releases (for version picker dropdown)
			r.Get("/openclaw/releases", srv.handleListOpenClawReleases)
			r.Get("/rootfs/releases", srv.handleListRootfsReleases)

			// Machines
			r.Route("/machines", func(r chi.Router) {
				r.Get("/", srv.handleListMachines)
				r.Post("/", srv.handleCreateMachine)

				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", srv.handleGetMachine)
					r.Put("/", srv.handleUpdateMachine)
					r.Get("/progress", srv.handleMachineProgress)
					r.Get("/logs", srv.handleMachineLogs)
					r.Get("/secrets", srv.handleListSecrets)
					r.Get("/credentials", srv.handleListMachineCredentials)
					r.Put("/credentials/{provider}", srv.handleSetMachineCredential)
					r.Post("/credentials/{provider}/test", srv.handleTestMachineCredential)
					r.Delete("/credentials/{provider}", srv.handleDeleteMachineCredential)

					r.Get("/terminal/*", srv.handleTerminalProxy)
					r.HandleFunc("/gateway/*", srv.handleGatewayProxy)
					r.HandleFunc("/gateway", srv.handleGatewayProxy)
					r.HandleFunc("/dashboard/*", srv.handleDashboardProxy)
					r.HandleFunc("/dashboard", srv.handleDashboardProxy)
					r.Get("/files", srv.handleListFiles)
					r.Get("/files/download", srv.handleDownloadFile)
					r.Get("/files/download-zip", srv.handleDownloadZip)
					r.Get("/usage", srv.handleGetMachineUsage)
					r.Get("/usage/breakdown", srv.handleGetMachineUsageBreakdown)
					r.Get("/metrics", srv.handleGetMachineMetrics)
					r.Get("/traces", srv.handleListMachineTraces)
					r.Get("/traces/{traceID}", srv.handleGetMachineTrace)
					r.Get("/activity", srv.handleListMachineActivity)
					r.Post("/start", srv.handleStartMachine)
					r.Post("/stop", srv.handleStopMachine)
					r.Post("/cdp-target", srv.handleSetCDPTarget)
					r.Delete("/cdp-target", srv.handleResetCDPTarget)
					r.Post("/browse-session", srv.handleCreateBrowseSession)
					r.Route("/browse-session/{sessionId}", func(r chi.Router) {
						r.Put("/heartbeat", srv.handleHeartbeatBrowseSession)
						r.Delete("/", srv.handleDeleteBrowseSession)
					})
					r.Post("/rootfs/upgrade", srv.handleUpgradeMachineRootfs)
					r.Post("/rootfs/rollback", srv.handleRollbackMachineRootfs)
					r.Post("/openclaw/upgrade", srv.handleUpgradeMachineOpenClaw)
					r.Post("/openclaw/rollback", srv.handleRollbackMachineOpenClaw)
					r.Post("/runtime/upgrade", srv.handleRuntimeUpgrade)
					r.Post("/rollback", srv.handleRollbackMachine)
					r.Get("/token", srv.handleGetMachineToken)
					r.Post("/ssh-cert", srv.handleSSHCert)

					// Machine config
					r.Get("/capabilities", srv.handleListMachineCapabilities)
					r.Post("/capabilities", srv.handleEnableMachineCapability)
					r.Put("/capabilities/{entryId}", srv.handleUpdateMachineCapabilityOverrides)
					r.Delete("/capabilities/{entryId}", srv.handleDisableMachineCapability)
					r.Put("/identity", srv.handleSetMachineIdentity)
					r.Get("/model", srv.handleGetMachineModel)
					r.Put("/model", srv.handleSetMachineModel)
					r.Get("/models", srv.handleListMachineModels)
					r.Get("/assembled-config", srv.handleGetMachineAssembledConfig)
					r.Post("/config/push", srv.handlePushMachineConfig)
					r.Get("/search-provider", srv.handleGetSearchProvider)
					r.Put("/search-provider", srv.handleSetSearchProvider)
					r.Delete("/search-provider", srv.handleDeleteSearchProvider)

					// Channel state machine transitions
					r.Post("/channels/{channel}/connect", srv.handleChannelConnect)
					r.Post("/channels/{channel}/disconnect", srv.handleChannelDisconnect)
					r.Put("/channels/{channel}/settings", srv.handleChannelSettings)
					r.Put("/channels/{channel}/token", srv.handleChannelUpdateToken)

					r.Get("/workflows", srv.handleListMachineWorkflows)

					// Agent (persona) routes
					r.Get("/agents", srv.handleListMachineAgents)
					r.Get("/agents/{agentId}", srv.handleGetMachineAgent)

					r.Get("/plugins", srv.handleListMachinePlugins)

					// Integrations (Composio)
					r.Get("/integrations", srv.handleListIntegrations)
					r.Post("/integrations/{integration}/connect", srv.handleCreateConnectLink)
					r.Delete("/integrations/{connId}", srv.handleDeleteIntegration)

					// Backups
					r.Get("/backups", srv.handleListMachineBackups)
					r.Post("/backups", srv.handleCreateMachineBackup)
					r.Post("/backups/{backupId}/restore", srv.handleRestoreMachineBackup)
					r.Delete("/backups/{backupId}", srv.handleDeleteMachineBackup)
					r.Get("/backups/{backupId}/download", srv.handleDownloadMachineBackup)

					// Browser VM pairing
					r.Post("/pair-browser", srv.handlePairBrowser)
					r.Delete("/pair-browser", srv.handleUnpairBrowser)

					// Owner/admin-only machine operations
					r.Group(func(r chi.Router) {
						r.Use(requireRole("owner", "admin"))
						r.Delete("/", srv.handleDeleteMachine)
						r.Put("/secrets/{key}", srv.handleSetSecret)
						r.Delete("/secrets/{key}", srv.handleDeleteSecret)
						r.Put("/budget", srv.handleSetBudget)
						r.Delete("/budget", srv.handleDeleteBudget)
						r.Post("/exec", srv.handleMachineExec)
						r.Post("/agents", srv.handleCreateMachineAgent)
						r.Put("/agents/{agentId}", srv.handleUpdateMachineAgent)
						r.Delete("/agents/{agentId}", srv.handleDeleteMachineAgent)
						r.Post("/plugins", srv.handleEnableMachinePlugin)
						r.Put("/plugins/{pluginId}", srv.handleUpdateMachinePluginOverrides)
						r.Delete("/plugins/{pluginId}", srv.handleDisableMachinePlugin)
					})
				})
			})

			// Browser VMs
			r.Route("/browser-vms", func(r chi.Router) {
				r.Get("/", srv.handleListBrowserVMs)
				r.Post("/", srv.handleCreateBrowserVM)
				r.Route("/{browserVmId}", func(r chi.Router) {
					r.Get("/", srv.handleGetBrowserVM)
					r.Post("/start", srv.handleStartBrowserVM)
					r.Post("/stop", srv.handleStopBrowserVM)
					r.HandleFunc("/live", srv.handleBrowserVMLiveProxy)
					r.HandleFunc("/live/*", srv.handleBrowserVMLiveProxy)
					r.Get("/metrics", srv.handleGetBrowserVMMetrics)
					r.Delete("/", srv.handleDeleteBrowserVM)
				})
			})
		})

		// Registry
		r.Get("/api/registry/channels", srv.handleListRegistryChannels)
		r.Get("/api/registry/skills", srv.handleListRegistrySkills)
		r.Get("/api/registry/tools", srv.handleListRegistryTools)
		r.Get("/api/registry/{entryId}", srv.handleGetRegistryEntry)

		// Admin — host management (superuser only)
		r.Route("/api/admin", func(r chi.Router) {
			r.Use(srv.requireSuperuser)
			r.Get("/hosts", srv.handleListHosts)
			r.Get("/hosts/{hostId}/machines", srv.handleListHostMachines)
			r.Get("/hosts/{hostId}/logs", srv.handleHostLogs)
			r.Get("/hosts/{hostId}/vm-stats", srv.handleHostVMStats)
			r.Post("/hosts", srv.handleProvisionHost)
			r.Delete("/hosts/{hostId}", srv.handleDestroyHost)
			r.Post("/hosts/{hostId}/refresh-rootfs", srv.handleRefreshRootfs)
			r.Post("/hosts/{hostId}/trigger-update", srv.handleTriggerHostUpdate)
			r.Post("/hosts/{hostId}/drain-update", srv.handleDrainHostUpdate)
			r.Post("/hosts/{hostId}/configure-browser-storage", srv.handleConfigureHostBrowserStorage)
			r.Get("/host-update-operations/{operationId}", srv.handleGetHostUpdateOperation)
			r.Post("/hosts/{hostId}/maintenance", srv.handleSetHostMaintenance)
			r.Post("/hosts/{hostId}/deregister", srv.handleDeregisterHost)
			r.Post("/machines/{machineId}/reset", srv.handleAdminResetMachine)
			r.Post("/machines/{machineId}/clear-migration", srv.handleAdminClearMigration)
			r.Post("/machines/{machineId}/flag-migration", srv.handleAdminFlagMigration)
			r.Post("/machines/{machineId}/start", srv.handleAdminStartMachine)
			r.Post("/machines/{machineId}/stop", srv.handleAdminStopMachine)
			r.Get("/machines", srv.handleAdminListMachines)
			r.Get("/latest-versions", srv.handleLatestVersions)
			r.Get("/browser-vms/reconcile-candidates", srv.handleListBrowserVMReconcileCandidates)
			r.Post("/browser-vms/{browserVmId}/reconcile", srv.handleReconcileBrowserVM)
			r.Get("/openclaw-releases", srv.handleAdminListOpenClawReleases)
			r.Get("/rootfs-releases", srv.handleAdminListRootfsReleases)
			r.Post("/hosts/enrollment-tokens", srv.handleCreateEnrollmentToken)
			r.Get("/hosts/enrollment-tokens", srv.handleListEnrollmentTokens)
			r.Post("/machines/migrate", srv.handleAdminMigrateMachine)
			r.Get("/workflows", srv.handleAdminListWorkflows)
			r.Get("/workflows/{workflowId}/events", srv.handleAdminListWorkflowEvents)
			r.Get("/events", srv.handleAdminListEvents)

			// Integration catalog (admin CRUD)
			r.Get("/integrations", srv.handleListIntegrationCatalog)
			r.Post("/integrations", srv.handleCreateIntegrationCatalogEntry)
			r.Put("/integrations/{integrationId}", srv.handleUpdateIntegrationCatalogEntry)
			r.Delete("/integrations/{integrationId}", srv.handleDeleteIntegrationCatalogEntry)

			// Plugin catalog (admin CRUD)
			r.Get("/plugins", srv.handleListPluginCatalog)
			r.Post("/plugins", srv.handleCreatePluginCatalogEntry)
			r.Put("/plugins/{pluginId}", srv.handleUpdatePluginCatalogEntry)
			r.Delete("/plugins/{pluginId}", srv.handleDeletePluginCatalogEntry)
		})
	})

	// Internal routes (service-to-service, authenticated via CF Service Token)
	r.Route("/api/internal", func(r chi.Router) {
		r.Use(srv.ServiceTokenMiddleware)
		r.Post("/resolve", srv.handleInternalResolve)
	})

	// Agent endpoints (authenticated via agent token, called by host agents)
	r.Post("/api/agent/heartbeat", srv.handleAgentHeartbeat)
	r.Post("/api/agent/shutdown-notify", srv.handleAgentShutdownNotify)
	r.Post("/api/agent/register", srv.handleAgentRegister)
	r.Post("/api/agent/vm-metrics", srv.handleAgentVMMetrics)
	r.Get("/api/agent/install", srv.handleInstallScript)

	// Agent-authenticated machine operations (called by host agent's metadata server)
	r.Get("/api/agent/machines/{machineID}/agents", srv.handleAgentAuthListAgents)
	r.Post("/api/agent/machines/{machineID}/agents", srv.handleAgentAuthCreateAgent)
	r.Put("/api/agent/machines/{machineID}/agents/{agentId}", srv.handleAgentAuthUpdateAgent)
	r.Delete("/api/agent/machines/{machineID}/agents/{agentId}", srv.handleAgentAuthDeleteAgent)
	r.Post("/api/agent/machines/{machineID}/config/push", srv.handleAgentAuthPushConfig)
	r.Get("/api/agent/machines/{machineID}/plugins", srv.handleAgentAuthListPlugins)
	r.Put("/api/agent/machines/{machineID}/plugins/{pluginId}/status", srv.handleAgentAuthUpdatePluginStatus)
	r.Post("/api/agent/machines/{machineID}/usage", srv.handleAgentAuthRecordUsage)
	r.Post("/api/agent/machines/{machineID}/oauth-token", srv.handleAgentStoreOAuthToken)
	r.Post("/api/agent/machines/{machineID}/refresh-credential", srv.handleRefreshCredentialInternal)
	r.Get("/api/agent/machines/{machineID}/secrets", srv.handleAgentAuthGetSecrets)
	r.Get("/api/agent/machines/{machineID}/composio/tools", srv.handleComposioProxyTools)
	r.Post("/api/agent/machines/{machineID}/composio/actions/{action}/execute", srv.handleComposioProxyExecute)
	r.Post("/api/agent/activity", srv.handleAgentActivity)

	// Opik-compatible tracing API — authenticated via gateway_token
	r.Route("/api/opik/v1/private", func(r chi.Router) {
		// Traces
		r.Post("/traces", srv.handleOpikCreateTrace)
		r.Post("/traces/batch", srv.handleOpikCreateTracesBatch)
		r.Patch("/traces/{traceID}", srv.handleOpikUpdateTrace)

		// Spans
		r.Post("/spans", srv.handleOpikCreateSpan)
		r.Post("/spans/batch", srv.handleOpikCreateSpansBatch)
		r.Patch("/spans/{spanID}", srv.handleOpikUpdateSpan)

		// Projects
		r.Get("/projects", srv.handleOpikListProjects)
		r.Post("/projects", srv.handleOpikCreateProject)
		r.Get("/projects/{projectID}", srv.handleOpikGetProject)
	})

	// Start tunnel reaper to clean up orphaned VM tunnels
	tunnel.StartReaper(ctx, tunnelMgr, s, 10*time.Minute)

	// Reconcile host capacity to match the current oversubscription ratio.
	// This ensures that if the ratio changes, existing hosts get updated.
	srv.reconcileHostCapacity(ctx)

	srv.router = r
	return srv
}

func (s *Server) SetWorkflowService(workflowSvc *workflows.Service) {
	s.workflows = workflowSvc
}

func (s *Server) SetEmailClient(c *email.Client) {
	s.emailClient = c
}

func (s *Server) SetActivity(a *events.Activity) {
	s.activity = a
}

func (s *Server) SetFrontendURL(url string) {
	s.frontendBaseURL = url
}

func (s *Server) SetRouting(r *routing.Service) {
	s.routing = r
}

func (s *Server) SetComposioClient(c *composio.Client) {
	s.composioClient = c
}

func (s *Server) SetComposioAPIURL(url string) {
	s.composioAPIURL = url
}

// ComposioProxyTokenTTL is the lifetime of per-machine Composio proxy tokens.
// Tokens are delivered to VMs in the assembled plugin config; config pushes
// happen at boot and on settings changes but are not strictly periodic, so
// the TTL must cover the longest realistic gap between events. 30 days is a
// conservative middle ground: long enough that a steady-state VM with no
// config changes keeps working, short enough that a leaked token has a
// bounded exposure window. Bumped from 24h after review: a 24h TTL would
// silently break Composio on any VM running >1 day without a config push.
const ComposioProxyTokenTTL = 30 * 24 * time.Hour

// SetComposioProxyTokenSigner wires the function used to mint per-machine
// Composio proxy tokens. Called from main.go in both API and worker modes.
// Decoupled from s.auth because NewWorkerServer doesn't accept an Auth
// instance; without this setter, worker-mode config pushes would fail
// before any reconciliation could complete.
func (s *Server) SetComposioProxyTokenSigner(f func(machineID string) (string, error)) {
	s.composioProxyTokenSigner = f
}

// signComposioProxyToken mints a per-machine Composio proxy token.
// Suitable as a configassembly token signer.
func (s *Server) signComposioProxyToken(machineID string) (string, error) {
	if s.composioProxyTokenSigner == nil {
		return "", fmt.Errorf("composio proxy token: signer not configured")
	}
	return s.composioProxyTokenSigner(machineID)
}

func (s *Server) SetDataPlaneDomain(d string) {
	s.dataPlaneDomain = d
}

func (s *Server) SetCfAccessAuthDomain(d string) {
	s.cfAccessAuthDomain = d
}

func (s *Server) SetSSHCAPrivateKey(key string) {
	s.sshCAPrivateKey = key
}

func (s *Server) frontendURL() string {
	return s.frontendBaseURL
}

// reconcileHostCapacity recalculates capacity_vcpus for all active hosts
// based on their physical CPU count (from capabilities) and the current
// oversubscription ratio. This ensures that changing VCPU_OVERSUBSCRIPTION_RATIO
// takes effect on existing hosts at startup, not only on newly created ones.
func (s *Server) reconcileHostCapacity(ctx context.Context) {
	updated, err := s.store.ReconcileHostCapacityVCPUs(ctx, s.vcpuOversubRatio)
	if err != nil {
		slog.Error("reconcile_host_capacity.failed", "error", err)
		return
	}
	if updated > 0 {
		slog.Info("reconcile_host_capacity.updated", "hosts_updated", updated, "ratio", s.vcpuOversubRatio)
	}
}

func slogRequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("http.request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", middleware.GetReqID(r.Context()),
			"trace_id", ww.Header().Get("X-Trace-ID"),
		)
	})
}

func traceIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get("X-Trace-ID")
		if traceID == "" {
			traceID = tracing.GenerateTraceID()
		}
		ctx := tracing.WithTraceID(r.Context(), traceID)
		w.Header().Set("X-Trace-ID", traceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":     "ok",
		"version":    version.Version,
		"git_commit": version.GitCommit,
		"build_time": version.BuildTime,
	})
}

// ---- Middleware ----

type contextKey string

const (
	accountMemberKey contextKey = "accountMember"
	accountIDKey     contextKey = "accountID"
)

func (s *Server) AccountMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accountID, err := parseIntParam(r, "accountId")
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid account id")
			return
		}

		claims := auth.UserFromContext(r.Context())
		if claims == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		member, err := s.store.GetAccountMember(r.Context(), accountID, claims.UserID)
		if err != nil {
			writeError(w, http.StatusForbidden, "access denied to this account")
			return
		}

		ctx := context.WithValue(r.Context(), accountMemberKey, member)
		ctx = context.WithValue(ctx, accountIDKey, accountID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireSuperuser blocks requests from non-superuser accounts.
func (s *Server) requireSuperuser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isSuperuser(r.Context()) {
			writeError(w, http.StatusForbidden, "admin access required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isSuperuser reports whether the current request is authenticated as the
// superuser. Used by handlers that need to gate individual fields (e.g.
// tenant-supplied host_id/region placement pinning) rather than whole
// routes.
func isSuperuser(ctx context.Context) bool {
	claims := auth.UserFromContext(ctx)
	return claims != nil && claims.Email == "mathewma@gmail.com"
}

// accountIDFromContext returns the account ID set by AccountMiddleware.
func accountIDFromContext(ctx context.Context) int {
	id, _ := ctx.Value(accountIDKey).(int)
	return id
}

// userIDFromContext returns a pointer to the authenticated user's ID, or nil if not available.
func userIDFromContext(ctx context.Context) *int {
	claims := auth.UserFromContext(ctx)
	if claims == nil || claims.UserID == 0 {
		return nil
	}
	return &claims.UserID
}

// accountMemberFromContext returns the account member set by AccountMiddleware.
func accountMemberFromContext(ctx context.Context) *store.AccountMember {
	m, _ := ctx.Value(accountMemberKey).(*store.AccountMember)
	return m
}

// requireOwnerRole checks that the current account member has the "owner" role.
// Returns true if access is denied (and the response has been written).
func requireOwnerRole(w http.ResponseWriter, r *http.Request) bool {
	member := accountMemberFromContext(r.Context())
	if member == nil || member.Role != "owner" {
		writeError(w, http.StatusForbidden, "owner role required for this action")
		return true
	}
	return false
}

// requireRole returns middleware that checks the account member has one of the allowed roles.
func requireRole(roles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			member := accountMemberFromContext(r.Context())
			if member == nil || !allowed[member.Role] {
				writeError(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---- Helpers ----

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func parseIntParam(r *http.Request, name string) (int, error) {
	s := chi.URLParam(r, name)
	var id int
	_, err := fmt.Sscanf(s, "%d", &id)
	return id, err
}

// slugRegex validates slugs used in KV keys, DNS, and tunnel routes.
// Must be 2-63 chars, lowercase alphanumeric, may contain hyphens (not leading/trailing).
var slugRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`)

// isValidSlug checks that a slug matches the required format.
func isValidSlug(slug string) bool {
	return slugRegex.MatchString(slug)
}
