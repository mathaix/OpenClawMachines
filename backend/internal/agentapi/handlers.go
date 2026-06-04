package agentapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/orchestrator"
	"github.com/mathaix/openclawmachines/backend/pkg/version"
)

const (
	rootfsSourcePath = "/var/lib/ocm/images/rootfs.ext4"
	rootfsCachePath  = "/var/lib/ocm/vms/.base-rootfs.ext4"
)

// VMRequest is the request body for creating a VM.
type VMRequest struct {
	MachineID        string                              `json:"machine_id"`
	MachineKind      string                              `json:"machine_kind,omitempty"`
	MachineName      string                              `json:"machine_name"`
	MachineSlug      string                              `json:"machine_slug"`
	VCPUs            int                                 `json:"vcpus"`
	MemoryMB         int                                 `json:"memory_mb"`
	VMIP             string                              `json:"vm_ip"`
	GatewayToken     string                              `json:"gateway_token"` // for OpenClaw gateway auth
	ProxyToken       string                              `json:"proxy_token"`   // for proxy API auth (browser/terminal/logs)
	OpenClawConf     []byte                              `json:"openclaw_config"`
	HermesConfigYAML []byte                              `json:"hermes_config_yaml,omitempty"`
	HermesEnv        []byte                              `json:"hermes_env,omitempty"`
	HermesAuthJSON   []byte                              `json:"hermes_auth_json,omitempty"`
	HermesSoul       []byte                              `json:"hermes_soul,omitempty"`
	LLMEndpoint      string                              `json:"llm_endpoint"`
	Secrets          map[string]string                   `json:"secrets"`
	LLMKeys          map[string]metadata.CredentialEntry `json:"llm_keys,omitempty"`
	AccountID        int                                 `json:"account_id"`
	BudgetMicrocents *int64                              `json:"budget_microcents,omitempty"`
	DataVolumeGB     int                                 `json:"data_volume_gb"`
	DataVersion      int                                 `json:"data_version"`
	SigningKey       string                              `json:"signing_key,omitempty"`
	TunnelToken      string                              `json:"tunnel_token,omitempty"`
	VmHostname       string                              `json:"vm_hostname,omitempty"`
	OwnerEmails      []string                            `json:"owner_emails,omitempty"`
	CfCaPubKey       string                              `json:"cf_ca_pubkey,omitempty"`
	CustomProviders  []metadata.CustomProviderConfig     `json:"custom_providers,omitempty"`
	Souls            []metadata.SoulEntry                `json:"souls,omitempty"`
	RuntimeSelection *metadata.RuntimeSelection          `json:"runtime_selection,omitempty"`
}

// VMResponse is the response for VM operations.
type VMResponse struct {
	MachineID       string `json:"machine_id"`
	VMIP            string `json:"vm_ip"`
	Status          string `json:"status"`
	RootfsVersion   string `json:"rootfs_version,omitempty"`
	OpenClawVersion string `json:"openclaw_version,omitempty"`
	VCPUs           int    `json:"vcpus"`
	MemoryMB        int    `json:"memory_mb"`
	DiskTotalMB     int64  `json:"disk_total_mb,omitempty"`
	DiskUsedMB      int64  `json:"disk_used_mb,omitempty"`
}

func (req *VMRequest) normalizeForMachine(machineID string) {
	if strings.TrimSpace(req.MachineID) == "" {
		req.MachineID = machineID
	}
}

func validateVMRequest(req VMRequest) error {
	if req.MachineID == "" || req.VMIP == "" {
		return fmt.Errorf("machine_id and vm_ip are required")
	}
	if req.GatewayToken == "" {
		return fmt.Errorf("gateway_token is required")
	}
	if req.ProxyToken == "" {
		return fmt.Errorf("proxy_token is required")
	}
	if req.SigningKey == "" {
		return fmt.Errorf("signing_key is required")
	}
	if req.TunnelToken == "" {
		return fmt.Errorf("tunnel_token is required")
	}
	if req.VmHostname == "" {
		return fmt.Errorf("vm_hostname is required")
	}
	return nil
}

func runtimeSelectionVersions(selection *metadata.RuntimeSelection) (string, string) {
	if selection == nil {
		return "", ""
	}
	return strings.TrimSpace(selection.ResolvedRootfsVersion), strings.TrimSpace(selection.ResolvedOpenClawVersion)
}

func vmConfigFromRequest(req VMRequest) orchestrator.VMConfig {
	return orchestrator.VMConfig{
		MachineID:        req.MachineID,
		MachineKind:      req.MachineKind,
		MachineName:      req.MachineName,
		MachineSlug:      req.MachineSlug,
		VCPUs:            req.VCPUs,
		MemoryMB:         req.MemoryMB,
		VMIP:             req.VMIP,
		GatewayToken:     req.GatewayToken,
		ProxyToken:       req.ProxyToken,
		OpenClawConf:     req.OpenClawConf,
		HermesConfigYAML: req.HermesConfigYAML,
		HermesEnv:        req.HermesEnv,
		HermesAuthJSON:   req.HermesAuthJSON,
		HermesSoul:       req.HermesSoul,
		LLMEndpoint:      req.LLMEndpoint,
		Secrets:          req.Secrets,
		LLMKeys:          req.LLMKeys,
		AccountID:        req.AccountID,
		BudgetMicrocents: req.BudgetMicrocents,
		DataVolumeGB:     req.DataVolumeGB,
		DataVersion:      req.DataVersion,
		SigningKey:       req.SigningKey,
		TunnelToken:      req.TunnelToken,
		VmHostname:       req.VmHostname,
		OwnerEmails:      req.OwnerEmails,
		CfCaPubKey:       req.CfCaPubKey,
		CustomProviders:  req.CustomProviders,
		Souls:            req.Souls,
		RuntimeSelection: req.RuntimeSelection,
	}
}

// writeOrchestratorError maps orchestrator errors to HTTP status codes.
// VM-not-found becomes 404 so callers retry with the right semantics
// (e.g. drop the cached pairing) instead of treating it as a server fault.
func writeOrchestratorError(w http.ResponseWriter, err error) {
	if errors.Is(err, orchestrator.ErrVMNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	var vmCount int
	if s.orchestrator != nil {
		if vms, err := s.orchestrator.List(r.Context()); err == nil {
			vmCount = len(vms)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":     "ok",
		"uptime":     time.Since(s.startTime).String(),
		"version":    version.Version,
		"git_commit": version.GitCommit,
		"vm_count":   vmCount,
	})
}

// handleReady is a dependency-aware readiness check.
// Unlike /health (which always returns 200), /ready probes:
//   - Orchestrator: can list VMs
//   - Metadata server: reachable on bridge network
//   - Disk: scratch disk has >1GB free
//
// Returns 200 if all checks pass, 503 with details if any fail.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	type checkResult struct {
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	checks := map[string]checkResult{}
	allOK := true

	// 1. Orchestrator check
	if s.orchestrator != nil {
		if _, err := s.orchestrator.List(r.Context()); err != nil {
			checks["orchestrator"] = checkResult{Status: "fail", Error: err.Error()}
			allOK = false
		} else {
			checks["orchestrator"] = checkResult{Status: "ok"}
		}
	} else {
		checks["orchestrator"] = checkResult{Status: "fail", Error: "not initialized"}
		allOK = false
	}

	// 2. Metadata server check
	if s.metadataAddr != "" {
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get(s.metadataAddr + "/health")
		if err != nil {
			checks["metadata"] = checkResult{Status: "fail", Error: err.Error()}
			allOK = false
		} else {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				checks["metadata"] = checkResult{Status: "ok"}
			} else {
				checks["metadata"] = checkResult{Status: "fail", Error: fmt.Sprintf("status %d", resp.StatusCode)}
				allOK = false
			}
		}
	}

	// 3. Disk space check (scratch disk at /mnt/scratch or StateDir)
	var stat syscall.Statfs_t
	scratchPath := "/mnt/scratch"
	if err := syscall.Statfs(scratchPath, &stat); err != nil {
		// Not fatal — may not have a scratch disk in dev
		checks["disk"] = checkResult{Status: "skip", Error: "scratch disk not mounted"}
	} else {
		freeBytes := stat.Bavail * uint64(stat.Bsize)
		freeMB := freeBytes / (1024 * 1024)
		if freeMB < 1024 {
			checks["disk"] = checkResult{Status: "fail", Error: fmt.Sprintf("%d MB free (need >1024 MB)", freeMB)}
			allOK = false
		} else {
			checks["disk"] = checkResult{Status: "ok"}
		}
	}

	status := http.StatusOK
	statusStr := "ready"
	if !allOK {
		status = http.StatusServiceUnavailable
		statusStr = "not_ready"
	}

	writeJSON(w, status, map[string]interface{}{
		"status": statusStr,
		"checks": checks,
	})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	var vmCount int
	var vms []orchestrator.VMInstance
	if s.orchestrator != nil {
		var err error
		vms, err = s.orchestrator.List(r.Context())
		if err == nil {
			vmCount = len(vms)
		}
	}

	machineIDs := make([]string, 0, len(vms))
	for _, vm := range vms {
		machineIDs = append(machineIDs, vm.MachineID)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "ok",
		"uptime":      time.Since(s.startTime).String(),
		"version":     version.Version,
		"vm_count":    vmCount,
		"machine_ids": machineIDs,
	})
}

func (s *Server) handleListVMs(w http.ResponseWriter, r *http.Request) {
	if s.orchestrator == nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	vms, err := s.orchestrator.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	responses := make([]VMResponse, 0, len(vms))
	for _, vm := range vms {
		resp := VMResponse{
			MachineID:       vm.MachineID,
			VMIP:            vm.VMIP,
			Status:          vm.Status,
			RootfsVersion:   vm.RootfsVersion,
			OpenClawVersion: vm.OpenClawVersion,
			VCPUs:           vm.VCPUs,
			MemoryMB:        vm.MemoryMB,
		}
		if vm.DataVolumePath != "" {
			resp.DiskTotalMB, resp.DiskUsedMB = diskUsageMB(vm.DataVolumePath)
		}
		responses = append(responses, resp)
	}

	writeJSON(w, http.StatusOK, responses)
}

func (s *Server) handleCreateVM(w http.ResponseWriter, r *http.Request) {
	var req VMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := validateVMRequest(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	createStart := time.Now()
	slog.Info("vm.create_start", "machine_id", req.MachineID, "vcpus", req.VCPUs, "memory_mb", req.MemoryMB, "vm_ip", req.VMIP, "trace_id", r.Header.Get("X-Trace-ID"))

	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	cfg := vmConfigFromRequest(req)

	// Pre-register VM in "creating" state so GetVM can track progress/errors
	s.orchestrator.RegisterPending(cfg)

	// Create VM asynchronously — the progress SSE stream tracks boot progress
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		emitter := s.progress.Emitter(req.MachineID)
		logWriter := s.logMgr.Writer(req.MachineID)

		emitter.Emit("allocating", "Allocating resources")
		_, _ = logWriter.Write([]byte("Starting VM creation...\n"))

		emitter.Emit("rootfs", "Preparing root filesystem")
		emitter.Emit("network", "Configuring network")
		emitter.Emit("booting", "Booting MicroVM")

		if err := s.orchestrator.Create(ctx, cfg); err != nil {
			slog.Error("vm.create_failed", "machine_id", req.MachineID, "account_id", req.AccountID, "vcpus", req.VCPUs, "memory_mb", req.MemoryMB, "error", err, "duration_ms", time.Since(createStart).Milliseconds())
			s.orchestrator.SetError(req.MachineID, err.Error())
			emitter.Emit("error", err.Error())
			_, _ = fmt.Fprintf(logWriter, "Error: %v\n", err)
			return
		}

		slog.Info("vm.boot.completed", "machine_id", req.MachineID, "duration_ms", time.Since(createStart).Milliseconds())
		emitter.Emit("booted", "MicroVM booted, waiting for services")
		_, _ = logWriter.Write([]byte("VM booted, waiting for gateway...\n"))

		// Phase 1: Wait for auth proxy (port 8080) — starts quickly after boot.
		authProxyReady := false
		for i := 0; i < 30; i++ {
			select {
			case <-ctx.Done():
				slog.Warn("vm.authproxy.context_cancelled", "machine_id", req.MachineID, "duration_ms", time.Since(createStart).Milliseconds())
				emitter.Emit("machine_ready", "Machine is running")
				_, _ = logWriter.Write([]byte("VM is ready (auth proxy poll cancelled)\n"))
				return
			default:
			}
			if probePort(cfg.VMIP, 8080, 2*time.Second) {
				authProxyReady = true
				break
			}
			slog.Debug("vm.authproxy.poll", "machine_id", req.MachineID, "attempt", i+1, "vm_ip", cfg.VMIP)
			time.Sleep(2 * time.Second)
		}

		if !authProxyReady {
			slog.Warn("vm.authproxy.timeout", "machine_id", req.MachineID, "duration_ms", time.Since(createStart).Milliseconds())
			emitter.Emit("openclaw_ready", "Auth proxy did not start — gateway unreachable")
			_, _ = logWriter.Write([]byte("Warning: auth proxy did not respond within timeout\n"))
			emitter.Emit("machine_ready", "Machine is running")
			_, _ = logWriter.Write([]byte("VM is ready\n"))
			return
		}

		slog.Info("vm.authproxy.ready", "machine_id", req.MachineID, "duration_ms", time.Since(createStart).Milliseconds())
		_, _ = logWriter.Write([]byte("Auth proxy is ready, waiting for gateway...\n"))

		// Phase 2: Wait for gateway to actually be ready (takes ~55s).
		// Poll the PTY supervisor's /health endpoint which reports gateway process status.
		gatewayReady := false
		for i := 0; i < 45; i++ {
			select {
			case <-ctx.Done():
				slog.Warn("vm.gateway.context_cancelled", "machine_id", req.MachineID, "duration_ms", time.Since(createStart).Milliseconds())
				emitter.Emit("openclaw_ready", "Gateway poll cancelled (timeout)")
				_, _ = logWriter.Write([]byte("Warning: gateway poll cancelled due to context timeout\n"))
				emitter.Emit("machine_ready", "Machine is running")
				_, _ = logWriter.Write([]byte("VM is ready\n"))
				return
			default:
			}
			if probeGatewayHealth(cfg.VMIP, 3*time.Second) {
				gatewayReady = true
				break
			}
			slog.Debug("vm.gateway.poll", "machine_id", req.MachineID, "attempt", i+1, "vm_ip", cfg.VMIP)
			time.Sleep(2 * time.Second)
		}

		if gatewayReady {
			slog.Info("vm.gateway.ready", "machine_id", req.MachineID, "duration_ms", time.Since(createStart).Milliseconds())
			emitter.Emit("openclaw_ready", "OpenClaw gateway is ready")
			_, _ = logWriter.Write([]byte("Gateway is ready\n"))
		} else {
			slog.Warn("vm.gateway.timeout", "machine_id", req.MachineID, "duration_ms", time.Since(createStart).Milliseconds())
			emitter.Emit("openclaw_ready", "Gateway may still be starting")
			_, _ = logWriter.Write([]byte("Warning: gateway did not respond within timeout\n"))
		}

		emitter.Emit("machine_ready", "Machine is running")
		_, _ = logWriter.Write([]byte("VM is ready\n"))
	}()

	writeJSON(w, http.StatusCreated, VMResponse{
		MachineID: req.MachineID,
		VMIP:      req.VMIP,
		Status:    "creating",
		VCPUs:     req.VCPUs,
		MemoryMB:  req.MemoryMB,
	})
}

func (s *Server) handleUpgradeVM(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	var req VMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	req.normalizeForMachine(machineID)
	if req.MachineID != machineID {
		http.Error(w, "machine_id must match URL", http.StatusBadRequest)
		return
	}
	if err := validateVMRequest(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}
	if !s.beginUpgrade(machineID) {
		http.Error(w, "upgrade already in progress", http.StatusConflict)
		return
	}

	upgradeStart := time.Now()
	cfg := vmConfigFromRequest(req)
	rootfsVersion, openClawVersion := runtimeSelectionVersions(req.RuntimeSelection)

	go func() {
		defer s.finishUpgrade(req.MachineID)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		emitter := s.progress.Emitter(req.MachineID)
		logWriter := s.logMgr.Writer(req.MachineID)

		emitter.Emit("stopping", "Stopping existing VM")
		_, _ = logWriter.Write([]byte("Stopping VM for in-place recreate...\n"))
		emitter.Emit("recreating", "Recreating VM with preserved data volume")
		_, _ = logWriter.Write([]byte("Recreating VM with preserved data volume...\n"))

		if err := s.orchestrator.Upgrade(ctx, cfg); err != nil {
			slog.Error("vm.upgrade_failed", "machine_id", req.MachineID, "account_id", req.AccountID, "error", err, "duration_ms", time.Since(upgradeStart).Milliseconds())
			emitter.Emit("error", err.Error())
			_, _ = fmt.Fprintf(logWriter, "Error: %v\n", err)
			if !errors.Is(err, orchestrator.ErrUpgradeRestoredPreviousVM) {
				s.orchestrator.RegisterPending(cfg)
				s.orchestrator.SetError(req.MachineID, err.Error())
			}
			return
		}

		slog.Info("vm.upgrade.completed", "machine_id", req.MachineID, "duration_ms", time.Since(upgradeStart).Milliseconds())
		emitter.Emit("booted", "Replacement MicroVM booted")
		_, _ = logWriter.Write([]byte("Replacement VM booted, waiting for services...\n"))
		emitter.Emit("machine_ready", "Machine upgrade applied")
		_, _ = logWriter.Write([]byte("Upgrade completed\n"))
	}()

	writeJSON(w, http.StatusAccepted, VMResponse{
		MachineID:       req.MachineID,
		VMIP:            req.VMIP,
		Status:          "upgrading",
		RootfsVersion:   rootfsVersion,
		OpenClawVersion: openClawVersion,
		VCPUs:           req.VCPUs,
		MemoryMB:        req.MemoryMB,
	})
}

func (s *Server) handleGetVM(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	vm, err := s.orchestrator.Get(r.Context(), machineID)
	if err != nil {
		http.Error(w, "vm not found", http.StatusNotFound)
		return
	}

	resp := VMResponse{
		MachineID:       vm.MachineID,
		VMIP:            vm.VMIP,
		Status:          vm.Status,
		RootfsVersion:   vm.RootfsVersion,
		OpenClawVersion: vm.OpenClawVersion,
		VCPUs:           vm.VCPUs,
		MemoryMB:        vm.MemoryMB,
	}
	if vm.DataVolumePath != "" {
		resp.DiskTotalMB, resp.DiskUsedMB = diskUsageMB(vm.DataVolumePath)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDestroyVM(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	slog.Info("vm.destroy_start", "machine_id", machineID, "trace_id", r.Header.Get("X-Trace-ID"))

	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	if err := s.orchestrator.Destroy(r.Context(), machineID); err != nil {
		slog.Error("vm.destroy_failed", "machine_id", machineID, "error", err)
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStopVM(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	slog.Info("vm.stop_start", "machine_id", machineID, "trace_id", r.Header.Get("X-Trace-ID"))

	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	if err := s.orchestrator.Stop(r.Context(), machineID); err != nil {
		slog.Error("vm.stop_failed", "machine_id", machineID, "error", err)
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRollbackVM(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")
	slog.Info("vm.rollback_start", "machine_id", machineID)

	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	if err := s.orchestrator.RollbackDataVolume(machineID); err != nil {
		slog.Error("vm.rollback_failed", "machine_id", machineID, "error", err)
		if strings.Contains(err.Error(), "is running") {
			http.Error(w, err.Error(), http.StatusConflict)
		} else {
			http.Error(w, err.Error(), http.StatusNotFound)
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "rolled_back"})
}

func (s *Server) handleUpdateSecrets(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	var secrets map[string]string
	if err := json.NewDecoder(r.Body).Decode(&secrets); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := s.orchestrator.UpdateSecrets(r.Context(), machineID, secrets); err != nil {
		slog.Error("vm.secrets_update_failed", "machine_id", machineID, "error", err)
		writeOrchestratorError(w, err)
		return
	}

	slog.Info("vm.secrets_updated", "machine_id", machineID, "key_count", len(secrets), "trace_id", r.Header.Get("X-Trace-ID"))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleUpdateVMCredentials(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	var llmKeys map[string]metadata.CredentialEntry
	if err := json.NewDecoder(r.Body).Decode(&llmKeys); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := s.orchestrator.UpdateLLMKeys(r.Context(), machineID, llmKeys); err != nil {
		slog.Error("vm.credentials_update_failed", "machine_id", machineID, "error", err)
		writeOrchestratorError(w, err)
		return
	}

	slog.Info("vm.credentials_updated", "machine_id", machineID, "key_count", len(llmKeys), "trace_id", r.Header.Get("X-Trace-ID"))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReplaceVMCredentials(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	var llmKeys map[string]metadata.CredentialEntry
	if err := json.NewDecoder(r.Body).Decode(&llmKeys); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := s.orchestrator.ReplaceLLMKeys(r.Context(), machineID, llmKeys); err != nil {
		slog.Error("vm.credentials_replace_failed", "machine_id", machineID, "error", err)
		writeOrchestratorError(w, err)
		return
	}

	slog.Info("vm.credentials_replaced", "machine_id", machineID, "key_count", len(llmKeys), "trace_id", r.Header.Get("X-Trace-ID"))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleUpdateVMConfig(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	if err := s.orchestrator.UpdateConfig(r.Context(), machineID, body); err != nil {
		slog.Error("vm.config_update_failed", "machine_id", machineID, "error", err)
		writeOrchestratorError(w, err)
		return
	}

	slog.Info("vm.config_updated", "machine_id", machineID, "config_bytes", len(body), "trace_id", r.Header.Get("X-Trace-ID"))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// pluginReconcileRequest is sent by the backend with the desired plugin state.
type pluginReconcileRequest struct {
	Plugins []pluginDesiredState `json:"plugins"`
}

type pluginDesiredState struct {
	PluginID       string  `json:"plugin_id"`
	Enabled        bool    `json:"enabled"`
	InstallKind    string  `json:"install_kind"`
	ArtifactURL    *string `json:"artifact_url,omitempty"`
	CatalogVersion string  `json:"catalog_version"`
	InstallStatus  string  `json:"install_status"`
}

type pluginReconcileResult struct {
	PluginID string `json:"plugin_id"`
	Status   string `json:"status"`  // "installed", "uninstalled", "failed"
	Version  string `json:"version"` // version that was installed
	Error    string `json:"error,omitempty"`
}

// handleReconcilePlugins installs or uninstalls plugins inside a running VM.
// The backend pushes the desired state; the agent executes and returns results.
func (s *Server) handleReconcilePlugins(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	vm, err := s.orchestrator.Get(r.Context(), machineID)
	if err != nil {
		http.Error(w, "VM not found", http.StatusNotFound)
		return
	}
	if vm.Status != "running" {
		http.Error(w, "VM is not running", http.StatusConflict)
		return
	}

	var req pluginReconcileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	var results []pluginReconcileResult
	for _, p := range req.Plugins {
		// Skip bundled plugins — they don't need installation
		if p.InstallKind == "bundled" {
			continue
		}

		result := pluginReconcileResult{
			PluginID: p.PluginID,
			Version:  p.CatalogVersion,
		}

		if p.Enabled && p.InstallStatus == "pending" {
			// Install the plugin
			if p.ArtifactURL == nil || *p.ArtifactURL == "" {
				result.Status = "failed"
				result.Error = "no artifact_url specified"
				results = append(results, result)
				continue
			}
			exitCode, stdout, stderr := s.execInVM(vm.VMIP, []string{"openclaw", "plugin", "install", *p.ArtifactURL})
			if exitCode == 0 {
				result.Status = "installed"
				slog.Info("plugin.installed", "machine_id", machineID, "plugin_id", p.PluginID, "artifact", *p.ArtifactURL)
			} else {
				result.Status = "failed"
				result.Error = strings.TrimSpace(stderr)
				if result.Error == "" {
					result.Error = strings.TrimSpace(stdout)
				}
				slog.Warn("plugin.install_failed", "machine_id", machineID, "plugin_id", p.PluginID, "exit_code", exitCode, "stderr", stderr)
			}
		} else if !p.Enabled && p.InstallStatus == "installed" {
			// Uninstall the plugin
			exitCode, _, stderr := s.execInVM(vm.VMIP, []string{"openclaw", "plugin", "uninstall", p.PluginID})
			if exitCode == 0 {
				result.Status = "uninstalled"
				slog.Info("plugin.uninstalled", "machine_id", machineID, "plugin_id", p.PluginID)
			} else {
				result.Status = "failed"
				result.Error = strings.TrimSpace(stderr)
				slog.Warn("plugin.uninstall_failed", "machine_id", machineID, "plugin_id", p.PluginID, "exit_code", exitCode, "stderr", stderr)
			}
		} else {
			// No action needed
			continue
		}

		results = append(results, result)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"results": results})
}

// execInVM runs a command inside a VM via the PTY server's /exec endpoint.
// Returns exit code, stdout, and stderr.
func (s *Server) execInVM(vmIP string, command []string) (int, string, string) {
	reqBody, err := json.Marshal(map[string]interface{}{"command": command})
	if err != nil {
		return -1, "", fmt.Sprintf("failed to marshal exec request: %v", err)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	targetURL := fmt.Sprintf("http://%s:7681/exec", vmIP)

	resp, err := client.Post(targetURL, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return -1, "", fmt.Sprintf("failed to reach VM: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var execResp struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&execResp); err != nil {
		return -1, "", fmt.Sprintf("failed to parse exec response: %v", err)
	}

	return execResp.ExitCode, execResp.Stdout, execResp.Stderr
}

func (s *Server) handleVMDiagnostics(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	vm, err := s.orchestrator.Get(r.Context(), machineID)
	if err != nil {
		http.Error(w, "vm not found", http.StatusNotFound)
		return
	}

	type checkResult struct {
		Reachable bool   `json:"reachable"`
		LatencyMs int64  `json:"latency_ms,omitempty"`
		Error     string `json:"error,omitempty"`
	}

	type diagnostics struct {
		MachineID string                 `json:"machine_id"`
		VMIP      string                 `json:"vm_ip"`
		Status    string                 `json:"status"`
		Checks    map[string]interface{} `json:"checks"`
	}

	diag := diagnostics{
		MachineID: vm.MachineID,
		VMIP:      vm.VMIP,
		Status:    vm.Status,
		Checks:    make(map[string]interface{}),
	}

	// Auth proxy check
	start := time.Now()
	authOK := checkPort(vm.VMIP, 8080, 2*time.Second)
	diag.Checks["auth_proxy"] = checkResult{
		Reachable: authOK,
		LatencyMs: time.Since(start).Milliseconds(),
	}

	// Gateway check
	start = time.Now()
	gwOK := probeGatewayHealth(vm.VMIP, 2*time.Second)
	diag.Checks["gateway"] = checkResult{
		Reachable: gwOK,
		LatencyMs: time.Since(start).Milliseconds(),
	}

	writeJSON(w, http.StatusOK, diag)
}

// ---- Standalone browser VM handlers ----

func (s *Server) handleCreateBrowserVM(w http.ResponseWriter, r *http.Request) {
	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		BrowserVMID    string `json:"browser_vm_id"`
		VMIP           string `json:"vm_ip"`
		VCPUs          int    `json:"vcpus"`
		MemoryMB       int    `json:"memory_mb"`
		RootfsManifest string `json:"rootfs_manifest,omitempty"`
		RootfsVersion  string `json:"rootfs_version,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.BrowserVMID == "" || req.VMIP == "" {
		http.Error(w, "browser_vm_id and vm_ip are required", http.StatusBadRequest)
		return
	}
	createStart := time.Now()
	slog.Info("browser_vm.create_start",
		"id", req.BrowserVMID,
		"vm_ip", req.VMIP,
		"vcpus", req.VCPUs,
		"memory_mb", req.MemoryMB)

	cfg := orchestrator.BrowserVMConfig{
		BrowserVMID:    req.BrowserVMID,
		VMIP:           req.VMIP,
		VCPUs:          req.VCPUs,
		MemoryMB:       req.MemoryMB,
		RootfsManifest: req.RootfsManifest,
		RootfsVersion:  req.RootfsVersion,
	}

	// Detach from r.Context(): once the agent accepts this side-effecting
	// create, a backend/client timeout must not cancel Firecracker startup
	// after placement has been assigned. CDP readiness continues in the
	// orchestrator after CreateBrowserVM records the VM as creating.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := s.orchestrator.CreateBrowserVM(ctx, cfg); err != nil {
		slog.Error("browser_vm.create.failed",
			"id", req.BrowserVMID,
			"vm_ip", req.VMIP,
			"vcpus", req.VCPUs,
			"memory_mb", req.MemoryMB,
			"duration_ms", time.Since(createStart).Milliseconds(),
			"error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("browser_vm.create.completed",
		"id", req.BrowserVMID,
		"vm_ip", req.VMIP,
		"vcpus", req.VCPUs,
		"memory_mb", req.MemoryMB,
		"duration_ms", time.Since(createStart).Milliseconds())

	writeJSON(w, http.StatusAccepted, map[string]string{
		"browser_vm_id": req.BrowserVMID,
		"status":        "creating",
	})
}

func (s *Server) handleDestroyBrowserVM(w http.ResponseWriter, r *http.Request) {
	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	bvmID := chi.URLParam(r, "browserVmId")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := s.orchestrator.DestroyBrowserVM(ctx, bvmID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleBrowserVMHealth(w http.ResponseWriter, r *http.Request) {
	bvmID := chi.URLParam(r, "browserVmId")

	bvms, err := s.orchestrator.ListBrowserVMs(r.Context())
	if err != nil {
		http.Error(w, "failed to list browser VMs", http.StatusInternalServerError)
		return
	}

	var found *orchestrator.BrowserVMInstance
	for i := range bvms {
		if bvms[i].ID == bvmID {
			found = &bvms[i]
			break
		}
	}
	if found == nil {
		http.Error(w, "browser VM not found on this host", http.StatusNotFound)
		return
	}

	start := time.Now()
	cdpVersion, cdpErr := probeCDPVersion(found.VMIP, 2*time.Second)
	cdpLatency := time.Since(start).Milliseconds()

	start = time.Now()
	liveViewOK := checkPort(found.VMIP, kernelBrowserLivePort, 2*time.Second)
	liveLatency := time.Since(start).Milliseconds()

	status := found.Status
	if cdpErr == nil {
		status = "ready"
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"browser_vm_id":   found.ID,
		"vm_ip":           found.VMIP,
		"status":          status,
		"cdp_error":       found.CDPError,
		"cdp_reachable":   cdpErr == nil,
		"cdp_version":     cdpVersion,
		"cdp_latency_ms":  cdpLatency,
		"live_reachable":  liveViewOK,
		"live_latency_ms": liveLatency,
		"live_port":       kernelBrowserLivePort,
	})
}

func (s *Server) handlePairBrowserVM(w http.ResponseWriter, r *http.Request) {
	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	bvmID := chi.URLParam(r, "browserVmId")
	var req struct {
		MachineVMIP string `json:"machine_vm_ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if net.ParseIP(req.MachineVMIP) == nil {
		http.Error(w, "machine_vm_ip is required and must be a valid IP", http.StatusBadRequest)
		return
	}

	bvms, err := s.orchestrator.ListBrowserVMs(r.Context())
	if err != nil {
		slog.Error("browser_vm.pair.list_failed", "browser_vm_id", bvmID, "error", err)
		http.Error(w, "failed to list browser VMs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var browserVMIP string
	for _, bvm := range bvms {
		if bvm.ID == bvmID {
			browserVMIP = bvm.VMIP
			break
		}
	}
	if browserVMIP == "" {
		http.Error(w, "browser VM not found on this host", http.StatusNotFound)
		return
	}

	if err := s.orchestrator.PairBrowserVM(req.MachineVMIP, browserVMIP); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.metaSrv == nil {
		http.Error(w, "metadata server not available", http.StatusServiceUnavailable)
		return
	}
	s.metaSrv.SetBrowserTarget(req.MachineVMIP, net.JoinHostPort(browserVMIP, "9222"))
	slog.Info("cdp.target.set", "machine_vm_ip", req.MachineVMIP, "target", net.JoinHostPort(browserVMIP, "9222"), "browser_vm_id", bvmID)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleUnpairBrowserVM(w http.ResponseWriter, r *http.Request) {
	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	bvmID := chi.URLParam(r, "browserVmId")
	var req struct {
		MachineVMIP string `json:"machine_vm_ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if net.ParseIP(req.MachineVMIP) == nil {
		http.Error(w, "machine_vm_ip is required and must be a valid IP", http.StatusBadRequest)
		return
	}

	bvms, err := s.orchestrator.ListBrowserVMs(r.Context())
	if err != nil {
		slog.Error("browser_vm.unpair.list_failed", "browser_vm_id", bvmID, "error", err)
		http.Error(w, "failed to list browser VMs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var browserVMIP string
	for _, bvm := range bvms {
		if bvm.ID == bvmID {
			browserVMIP = bvm.VMIP
			break
		}
	}
	// Idempotent: if the agent has no record of this browser VM, there are
	// no bridge rules on this host tied to its IP — nothing to remove.
	// Return 200 so the control plane's delete/cleanup flow can complete
	// against orphan DB rows instead of getting stuck on a 404.
	if browserVMIP == "" {
		slog.Info("browser_vm.unpair.already_gone", "browser_vm_id", bvmID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "already_unpaired"})
		return
	}

	if err := s.orchestrator.UnpairBrowserVM(req.MachineVMIP, browserVMIP); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.metaSrv == nil {
		http.Error(w, "metadata server not available", http.StatusServiceUnavailable)
		return
	}
	s.metaSrv.ResetBrowserTarget(req.MachineVMIP)
	slog.Info("cdp.target.reset", "machine_vm_ip", req.MachineVMIP, "browser_vm_id", bvmID)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRefreshRootfs(w http.ResponseWriter, r *http.Request) {
	// In GCS mode, rootfs is staged from GCS manifest. Refresh-rootfs would overwrite the
	// GCS-staged image with the embedded local image, causing a stale-image regression.
	if s.gcsMode {
		http.Error(w, `{"error":"refresh-rootfs is disabled in GCS mode — rootfs is managed via GCS manifest"}`, http.StatusConflict)
		return
	}

	src := rootfsSourcePath
	dst := rootfsCachePath
	tmpDst := dst + ".tmp"

	slog.Info("rootfs.refresh_start", "src", src, "dst", dst)

	// Acquire exclusive lock to prevent concurrent rootfs refresh and VM create races
	if s.rootfsLock != nil {
		unlock, lockErr := s.rootfsLock.ExclusiveLock()
		if lockErr != nil {
			slog.Error("rootfs.lock_failed", "error", lockErr)
			http.Error(w, fmt.Sprintf("failed to acquire rootfs lock: %v", lockErr), http.StatusInternalServerError)
			return
		}
		defer unlock()
	}

	srcFile, err := os.Open(src)
	if err != nil {
		slog.Error("rootfs.open_failed", "error", err, "path", src)
		http.Error(w, fmt.Sprintf("failed to open source: %v", err), http.StatusInternalServerError)
		return
	}
	defer func() { _ = srcFile.Close() }()

	tmpFile, err := os.Create(tmpDst)
	if err != nil {
		slog.Error("rootfs.create_failed", "error", err, "path", tmpDst)
		http.Error(w, fmt.Sprintf("failed to create temp file: %v", err), http.StatusInternalServerError)
		return
	}

	if _, err := io.Copy(tmpFile, srcFile); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpDst)
		slog.Error("rootfs.copy_failed", "error", err)
		http.Error(w, fmt.Sprintf("failed to copy: %v", err), http.StatusInternalServerError)
		return
	}

	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpDst)
		slog.Error("rootfs.sync_failed", "error", err)
		http.Error(w, fmt.Sprintf("failed to sync temp file: %v", err), http.StatusInternalServerError)
		return
	}

	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpDst)
		slog.Error("rootfs.close_failed", "error", err)
		http.Error(w, fmt.Sprintf("failed to close temp file: %v", err), http.StatusInternalServerError)
		return
	}

	if err := os.Rename(tmpDst, dst); err != nil {
		_ = os.Remove(tmpDst)
		slog.Error("rootfs.rename_failed", "error", err)
		http.Error(w, fmt.Sprintf("failed to rename temp file: %v", err), http.StatusInternalServerError)
		return
	}

	slog.Info("rootfs.refresh_complete")
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "rootfs cache refreshed",
	})
}

const maxLogLines = 10000

func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request) {
	lines := 100
	if q := r.URL.Query().Get("lines"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			lines = n
		}
	}
	if lines > maxLogLines {
		lines = maxLogLines
	}

	// Use journalctl to get ocm-agent logs
	cmd := exec.CommandContext(r.Context(), "journalctl", "-u", "ocm-agent", "-n", strconv.Itoa(lines), "--no-pager")
	output, err := cmd.Output()
	if err != nil {
		slog.Error("agent.logs_fetch_failed", "error", err)
		http.Error(w, fmt.Sprintf("failed to get logs: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"logs": string(output),
	})
}

// handleSetCDPTarget remaps the CDP proxy target for a VM.
func (s *Server) handleSetCDPTarget(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	vm, err := s.orchestrator.Get(r.Context(), machineID)
	if err != nil {
		http.Error(w, "vm not found", http.StatusNotFound)
		return
	}

	var req struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Target == "" {
		http.Error(w, "target is required", http.StatusBadRequest)
		return
	}

	if s.metaSrv == nil {
		http.Error(w, "metadata server not available", http.StatusServiceUnavailable)
		return
	}

	// Validate and resolve target address.
	// The CLI sends "127.0.0.1:9222" meaning "port 9222 inside the VM" (via reverse SSH tunnel).
	// Browser VM pairing sends "<browser_vm_ip>:9222" for a companion on the same bridge.
	// The CDP proxy runs on the host, so it needs a bridge-reachable IP.
	host, port, err := net.SplitHostPort(req.Target)
	if err != nil {
		http.Error(w, "invalid target: must be host:port", http.StatusBadRequest)
		return
	}
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		host = vm.VMIP
	}
	// Only allow the VM's own IP OR the IP of a currently-running browser VM
	// managed by this agent. Prevents lateral access to other machines on the
	// bridge or to agent-level services like the bridge gateway (192.168.100.1).
	allowed := host == vm.VMIP
	if !allowed && s.orchestrator != nil {
		if bvms, lerr := s.orchestrator.ListBrowserVMs(r.Context()); lerr == nil {
			for _, bvm := range bvms {
				if bvm.VMIP == host {
					allowed = true
					break
				}
			}
		}
	}
	if !allowed {
		http.Error(w, "target host must be the VM's own IP or a running browser VM's IP", http.StatusBadRequest)
		return
	}
	target := net.JoinHostPort(host, port)

	s.metaSrv.SetBrowserTarget(vm.VMIP, target)
	slog.Info("cdp.target.set", "machine_id", machineID, "vm_ip", vm.VMIP, "target", target)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "target": target})
}

// handleResetCDPTarget reverts the CDP proxy target to the companion browser VM.
func (s *Server) handleResetCDPTarget(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	vm, err := s.orchestrator.Get(r.Context(), machineID)
	if err != nil {
		http.Error(w, "vm not found", http.StatusNotFound)
		return
	}

	if s.metaSrv == nil {
		http.Error(w, "metadata server not available", http.StatusServiceUnavailable)
		return
	}

	s.metaSrv.ResetBrowserTarget(vm.VMIP)
	slog.Info("cdp.target.reset", "machine_id", machineID, "vm_ip", vm.VMIP)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- Backup handlers ----

type BackupRequest struct {
	EncryptionKey []byte `json:"encryption_key"`
}

type BackupResponse struct {
	GCSPath         string `json:"gcs_path"`
	SizeBytes       int64  `json:"size_bytes"`
	CompressedBytes int64  `json:"compressed_bytes"`
	ChecksumSHA256  string `json:"checksum_sha256"`
	HMAC            []byte `json:"hmac"`
	Nonce           []byte `json:"nonce"`
	Timestamp       string `json:"timestamp"`
}

type RestoreRequest struct {
	GCSPath       string `json:"gcs_path"`
	EncryptionKey []byte `json:"encryption_key"`
	Nonce         []byte `json:"nonce"`
	ExpectedHMAC  []byte `json:"expected_hmac"`
}

func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	var req BackupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	bs := s.GetBackupStore()
	if bs == nil {
		http.Error(w, "backups not configured", http.StatusServiceUnavailable)
		return
	}

	dataVolPath := filepath.Join(s.dataDir, machineID+".ext4")
	if _, err := os.Stat(dataVolPath); os.IsNotExist(err) {
		http.Error(w, "no data volume found for machine", http.StatusNotFound)
		return
	}

	info, err := bs.Upload(r.Context(), machineID, dataVolPath, req.EncryptionKey)
	if err != nil {
		slog.Error("backup.create.failed", "machine_id", machineID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, BackupResponse{
		GCSPath:         info.GCSPath,
		SizeBytes:       info.SizeBytes,
		CompressedBytes: info.CompressedBytes,
		ChecksumSHA256:  info.ChecksumSHA256,
		HMAC:            info.HMAC,
		Nonce:           info.Nonce,
		Timestamp:       info.Timestamp.Format(time.RFC3339),
	})
}

func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	var req RestoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	bs := s.GetBackupStore()
	if bs == nil {
		http.Error(w, "backups not configured", http.StatusServiceUnavailable)
		return
	}

	dataVolPath := filepath.Join(s.dataDir, machineID+".ext4")

	if err := bs.Download(r.Context(), req.GCSPath, dataVolPath, req.EncryptionKey, req.Nonce, req.ExpectedHMAC); err != nil {
		slog.Error("backup.restore.failed", "machine_id", machineID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Remove version sidecar so ensureDataVolume re-evaluates on next boot.
	// Without this, restoring an older backup leaves a stale version marker
	// that causes the upgrade path to be skipped.
	versionPath := filepath.Join(s.dataDir, machineID+".version")
	if err := os.Remove(versionPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("backup.restore.version_sidecar_remove_failed", "path", versionPath, "error", err)
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	gcsPath := r.URL.Query().Get("gcs_path")
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "tar.gz"
	}

	encKeyHex := r.Header.Get("X-Backup-Key")
	nonceHex := r.Header.Get("X-Backup-Nonce")
	hmacHex := r.Header.Get("X-Backup-HMAC")

	if gcsPath == "" || encKeyHex == "" || nonceHex == "" || hmacHex == "" {
		http.Error(w, "missing required parameters", http.StatusBadRequest)
		return
	}

	bs := s.GetBackupStore()
	if bs == nil {
		http.Error(w, "backups not configured", http.StatusServiceUnavailable)
		return
	}

	encKey, err := hex.DecodeString(encKeyHex)
	if err != nil {
		http.Error(w, "invalid encryption key encoding", http.StatusBadRequest)
		return
	}
	nonce, err := hex.DecodeString(nonceHex)
	if err != nil {
		http.Error(w, "invalid nonce encoding", http.StatusBadRequest)
		return
	}
	hmacBytes, err := hex.DecodeString(hmacHex)
	if err != nil {
		http.Error(w, "invalid hmac encoding", http.StatusBadRequest)
		return
	}

	if format == "tar.gz" {
		// Buffer tar.gz to temp file so we can return an error status if it fails
		tmpTarGz, err := os.CreateTemp("", "backup-tgz-*")
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer func() { _ = os.Remove(tmpTarGz.Name()) }()
		defer func() { _ = tmpTarGz.Close() }()

		if err := bs.StreamTarGz(r.Context(), gcsPath, encKey, nonce, hmacBytes, tmpTarGz); err != nil {
			slog.Error("backup.download.failed", "machine_id", machineID, "error", err)
			http.Error(w, "backup export failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if _, err := tmpTarGz.Seek(0, 0); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-backup.tar.gz"`, machineID))
		_, _ = io.Copy(w, tmpTarGz)
	} else {
		// Stream raw ext4 (download, decrypt, decompress, stream)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-backup.ext4"`, machineID))

		tmpDir, err := os.MkdirTemp("", "backup-raw-*")
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer func() { _ = os.RemoveAll(tmpDir) }()

		ext4Path := filepath.Join(tmpDir, "volume.ext4")
		if err := bs.Download(r.Context(), gcsPath, ext4Path, encKey, nonce, hmacBytes); err != nil {
			http.Error(w, "download failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		f, err := os.Open(ext4Path)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer func() { _ = f.Close() }()
		_, _ = io.Copy(w, f)
	}
}

func (s *Server) handleDeleteBackupObject(w http.ResponseWriter, r *http.Request) {
	gcsPath := r.URL.Query().Get("gcs_path")
	if gcsPath == "" {
		http.Error(w, "gcs_path required", http.StatusBadRequest)
		return
	}
	bs := s.GetBackupStore()
	if bs == nil {
		http.Error(w, "backups not configured", http.StatusServiceUnavailable)
		return
	}
	if err := bs.Delete(r.Context(), gcsPath); err != nil {
		slog.Error("backup.delete_object.failed", "gcs_path", gcsPath, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
