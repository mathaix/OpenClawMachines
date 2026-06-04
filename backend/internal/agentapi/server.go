package agentapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/mathaix/openclawmachines/backend/internal/apiproxy"
	"github.com/mathaix/openclawmachines/backend/internal/backup"
	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/orchestrator"
	"github.com/mathaix/openclawmachines/backend/internal/rootfs"
	"github.com/mathaix/openclawmachines/backend/internal/selfupdate"
)

type agentUpdater interface {
	CheckAndUpdate(ctx context.Context) (bool, error)
}

type Server struct {
	agentToken     string
	startTime      time.Time
	orchestrator   orchestrator.Orchestrator
	progress       *ProgressManager
	logMgr         *LogManager
	upgradeMu      sync.Mutex
	activeUpgrades map[string]struct{}
	allowedCIDRs   string
	proxy          *apiproxy.Proxy
	metadataAddr   string       // e.g. "http://192.168.100.1:80" for readiness probes
	updater        agentUpdater // nil if self-update not configured
	metaSrv        *metadata.Server
	rootfsLock     *rootfs.RootfsLock
	gcsMode        bool               // true when rootfs is staged from GCS (refresh-rootfs should be disabled)
	backupStore    backup.BackupStore // nil if backups not configured
	backupMu       sync.RWMutex       // guards backupStore for hot-reload via heartbeat
	dataDir        string             // path to data volumes, e.g. /var/lib/ocm/data
	vmProxyPort    int                // port for VM auth proxy (default 8080), overridable for tests
}

type HeartbeatVMVersion struct {
	Rootfs   string `json:"rootfs,omitempty"`
	OpenClaw string `json:"openclaw,omitempty"`
}

func NewServer(agentToken string, orch orchestrator.Orchestrator, allowedCIDRs string, proxy *apiproxy.Proxy, metadataAddr string, updater *selfupdate.Updater, metaSrv *metadata.Server, rootfsLock *rootfs.RootfsLock, gcsMode bool, backupStore backup.BackupStore, dataDir string) *Server {
	return &Server{
		agentToken:     agentToken,
		startTime:      time.Now(),
		orchestrator:   orch,
		progress:       NewProgressManager(),
		logMgr:         NewLogManager(1000),
		activeUpgrades: make(map[string]struct{}),
		allowedCIDRs:   allowedCIDRs,
		proxy:          proxy,
		metadataAddr:   metadataAddr,
		updater:        updater,
		gcsMode:        gcsMode,
		metaSrv:        metaSrv,
		rootfsLock:     rootfsLock,
		backupStore:    backupStore,
		dataDir:        dataDir,
		vmProxyPort:    8080,
	}
}

func (s *Server) beginUpgrade(machineID string) bool {
	s.upgradeMu.Lock()
	defer s.upgradeMu.Unlock()
	if _, exists := s.activeUpgrades[machineID]; exists {
		return false
	}
	s.activeUpgrades[machineID] = struct{}{}
	return true
}

func (s *Server) finishUpgrade(machineID string) {
	s.upgradeMu.Lock()
	defer s.upgradeMu.Unlock()
	delete(s.activeUpgrades, machineID)
}

// SetBackupStore hot-swaps the backup store (called from heartbeat when config arrives).
func (s *Server) SetBackupStore(bs backup.BackupStore) {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	s.backupStore = bs
}

// GetBackupStore returns the current backup store (thread-safe).
// Exported so cmd/agent can check if the store is already initialized.
func (s *Server) GetBackupStore() backup.BackupStore {
	s.backupMu.RLock()
	defer s.backupMu.RUnlock()
	return s.backupStore
}

// HeartbeatVMVersions returns the current VM count and per-machine version info
// suitable for heartbeat reporting.
func (s *Server) HeartbeatVMVersions(ctx context.Context) (int, map[string]HeartbeatVMVersion) {
	if s == nil || s.orchestrator == nil {
		return 0, nil
	}
	vms, err := s.orchestrator.List(ctx)
	if err != nil {
		return 0, nil
	}
	versions := make(map[string]HeartbeatVMVersion, len(vms))
	for _, vm := range vms {
		if vm.Status != "running" && vm.Status != "starting" {
			continue
		}
		if vm.RootfsVersion == "" && vm.OpenClawVersion == "" {
			continue
		}
		versions[vm.MachineID] = HeartbeatVMVersion{
			Rootfs:   vm.RootfsVersion,
			OpenClaw: vm.OpenClawVersion,
		}
	}
	return len(vms), versions
}

// ControlRouter returns the router for the control API (port 9090, firewalled).
func (s *Server) ControlRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(cidrAllowlist(s.allowedCIDRs))
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Public
	r.Get("/health", s.handleHealth)
	r.Get("/ready", s.handleReady)

	// Authenticated (Control Plane only)
	r.Group(func(r chi.Router) {
		r.Use(bearerAuth(s.agentToken))
		r.Get("/info", s.handleInfo)
		r.Get("/logs", s.handleAgentLogs)
		r.Post("/refresh-rootfs", s.handleRefreshRootfs)
		r.Get("/vms", s.handleListVMs)
		r.Post("/vms", s.handleCreateVM)
		r.Get("/vms/{machineID}", s.handleGetVM)
		r.Get("/vms/{machineID}/diagnostics", s.handleVMDiagnostics)
		r.Patch("/vms/{machineID}/secrets", s.handleUpdateSecrets)
		r.Patch("/vms/{machineID}/credentials", s.handleUpdateVMCredentials)
		r.Put("/vms/{machineID}/credentials", s.handleReplaceVMCredentials)
		r.Patch("/vms/{machineID}/config", s.handleUpdateVMConfig)
		r.Post("/vms/{machineID}/upgrade", s.handleUpgradeVM)
		r.Post("/vms/{machineID}/stop", s.handleStopVM)
		r.Post("/vms/{machineID}/rollback", s.handleRollbackVM)
		r.Delete("/vms/{machineID}", s.handleDestroyVM)
		r.Post("/drain", s.handleDrain)
		r.Post("/trigger-update", s.handleTriggerUpdate)
		r.Post("/configure-browser-storage", s.handleConfigureBrowserStorage)
		r.Post("/vms/{machineID}/reconcile-plugins", s.handleReconcilePlugins)

		// Usage endpoints
		r.Get("/usage", s.handleGetUsage)
		r.Get("/usage/{machineID}", s.handleGetMachineUsage)
		r.Post("/usage/{machineID}/flush", s.handleFlushMachineUsage)

		// CDP target remap
		r.Post("/vms/{machineID}/cdp-target", s.handleSetCDPTarget)
		r.Delete("/vms/{machineID}/cdp-target", s.handleResetCDPTarget)

		// Backup endpoints
		r.Post("/vms/{machineID}/backup", s.handleCreateBackup)
		r.Post("/vms/{machineID}/restore", s.handleRestoreBackup)
		r.Get("/vms/{machineID}/backup-download", s.handleBackupDownload)
		r.Delete("/vms/{machineID}/backup-object", s.handleDeleteBackupObject)

		// Browser VM endpoints
		r.Route("/browser-vms", func(r chi.Router) {
			r.Post("/", s.handleCreateBrowserVM)
			r.Route("/{browserVmId}", func(r chi.Router) {
				r.Get("/health", s.handleBrowserVMHealth)
				r.Delete("/", s.handleDestroyBrowserVM)
				r.Post("/pair", s.handlePairBrowserVM)
				r.Post("/unpair", s.handleUnpairBrowserVM)
			})
		})
	})

	return r
}

// ProxyRouter returns the router for the proxy API (port 9091, Cloudflare tunneled).
func (s *Server) ProxyRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Get("/health", s.handleHealth)
	r.Get("/ready", s.handleReady)

	// Per-VM proxy routes (all require proxy token)
	r.Get("/proxy/{machineID}/health", s.handleHealthProxy)
	r.Get("/proxy/{machineID}/progress", s.HandleProgress)
	r.Get("/proxy/{machineID}/logs", s.HandleLogs)
	r.HandleFunc("/proxy/{machineID}/gateway/*", s.handleGatewayProxy)
	r.HandleFunc("/proxy/{machineID}/gateway", s.handleGatewayProxy)
	r.HandleFunc("/proxy/{machineID}/dashboard/*", s.handleDashboardProxy)
	r.HandleFunc("/proxy/{machineID}/dashboard", s.handleDashboardProxy)

	r.Get("/proxy/{machineID}/gateway-health", s.handleGatewayHealthProxy)
	r.Post("/proxy/{machineID}/restart-gateway", s.handleRestartGateway)

	r.HandleFunc("/proxy/{machineID}/terminal/*", s.handleTerminalProxy)
	r.HandleFunc("/proxy/{machineID}/files/*", s.handleFilesProxy)
	r.Post("/proxy/{machineID}/exec", s.handleExecProxy)
	r.Post("/proxy/{machineID}/write-file", s.handleWriteFileProxy)

	r.Group(func(r chi.Router) {
		r.Use(bearerAuth(s.agentToken))
		r.HandleFunc("/browser-vms/{browserVmId}/live", s.handleBrowserVMLiveProxy)
		r.HandleFunc("/browser-vms/{browserVmId}/live/*", s.handleBrowserVMLiveProxy)
	})

	return r
}

// ProgressManager returns the server's progress manager for external use.
func (s *Server) ProgressManagerRef() *ProgressManager {
	return s.progress
}

// LogManager returns the server's log manager for external use.
func (s *Server) LogManagerRef() *LogManager {
	return s.logMgr
}

// handleGetUsage returns usage records for all machines.
func (s *Server) handleGetUsage(w http.ResponseWriter, r *http.Request) {
	if s.proxy == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{})
		return
	}
	writeJSON(w, http.StatusOK, s.proxy.GetUsage())
}

// handleGetMachineUsage returns usage records for a specific machine.
func (s *Server) handleGetMachineUsage(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")
	if s.proxy == nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}
	writeJSON(w, http.StatusOK, s.proxy.GetMachineUsage(machineID))
}

// handleFlushMachineUsage returns and removes usage records for a specific machine.
func (s *Server) handleFlushMachineUsage(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")
	if s.proxy == nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}
	writeJSON(w, http.StatusOK, s.proxy.FlushUsage(machineID))
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
