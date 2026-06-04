package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
	"github.com/mathaix/openclawmachines/backend/internal/config"
	"github.com/mathaix/openclawmachines/backend/internal/fleet"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// ---- Browser VM handlers ----

func browserVMMayExistOnAgent(bvm *store.BrowserVM) bool {
	if bvm == nil || bvm.HostID == nil {
		return false
	}
	switch bvm.Status {
	case "running", "provisioning", "error":
		return true
	default:
		return false
	}
}

func browserRootfsSelection(image, manifest, version string) (*string, *string, error) {
	image = strings.TrimSpace(image)
	manifest = strings.TrimSpace(manifest)
	version = strings.TrimSpace(version)

	if image != "" && manifest != "" {
		return nil, nil, fmt.Errorf("browser_image cannot be combined with rootfs_manifest")
	}

	switch image {
	case "", "default":
	case "kernel-stable", "kernel-rollback":
		manifest = config.ExperimentalKernelBrowserManifestURI
		if version == "" {
			version = config.StableKernelBrowserRootfsVersion
		}
	case "kernel-latest", "kernel-experimental":
		manifest = config.ExperimentalKernelBrowserManifestURI
	default:
		return nil, nil, fmt.Errorf("browser_image must be one of: default, kernel-stable, kernel-experimental")
	}

	if manifest == config.StableBrowserRootfsManifestURI {
		return nil, nil, fmt.Errorf("legacy CDP browser rootfs is disabled; use kernel-stable or kernel-experimental")
	}
	if manifest != "" && manifest != config.ExperimentalKernelBrowserManifestURI {
		return nil, nil, fmt.Errorf("rootfs_manifest must be a known browser rootfs manifest")
	}
	if version != "" {
		if err := validateRootfsVersion(version); err != nil {
			return nil, nil, err
		}
		if manifest == "" {
			manifest = config.ExperimentalKernelBrowserManifestURI
		}
	}
	if manifest == "" && version == "" {
		return nil, nil, nil
	}
	return browserStringPtr(manifest), browserStringPtr(version), nil
}

func browserStringPtr(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}

func browserVMRequiresReflink(bvm *store.BrowserVM) bool {
	if bvm == nil {
		return false
	}
	manifest := strPtrValue(bvm.DesiredRootfsManifest)
	version := strPtrValue(bvm.DesiredRootfsVersion)
	return manifest == config.ExperimentalKernelBrowserManifestURI &&
		version != config.StableKernelBrowserRootfsVersion
}

func browserVMUsesDisabledLegacyRootfs(bvm *store.BrowserVM) bool {
	if bvm == nil {
		return false
	}
	return strPtrValue(bvm.DesiredRootfsManifest) == config.StableBrowserRootfsManifestURI
}

func (s *Server) handleListBrowserVMs(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())

	bvms, err := s.store.ListBrowserVMsByAccount(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list browser VMs")
		return
	}
	writeJSON(w, http.StatusOK, bvms)
}

func (s *Server) handleCreateBrowserVM(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())

	var req struct {
		Name           string `json:"name"`
		VCPUs          int    `json:"vcpus"`
		MemoryMB       int    `json:"memory_mb"`
		BrowserImage   string `json:"browser_image,omitempty"`
		RootfsManifest string `json:"rootfs_manifest,omitempty"`
		RootfsVersion  string `json:"rootfs_version,omitempty"`
	}
	// The body is optional (all fields default), so io.EOF is not an error.
	// Any other decode failure is a malformed payload and must be rejected —
	// previously we silently defaulted it, which let a typo'd request slip
	// past unnoticed and create a browser VM with unintended defaults.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Name == "" {
		req.Name = "Browser VM"
	}
	if req.VCPUs <= 0 {
		req.VCPUs = 2
	}
	if req.MemoryMB <= 0 {
		req.MemoryMB = 4096
	}
	desiredManifest, desiredVersion, err := browserRootfsSelection(req.BrowserImage, req.RootfsManifest, req.RootfsVersion)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var bvm *store.BrowserVM
	for attempt := 0; attempt < 3; attempt++ {
		bvm = &store.BrowserVM{
			AccountID:             accountID,
			Slug:                  generateShortID(),
			Name:                  req.Name,
			Status:                "stopped",
			VCPUs:                 req.VCPUs,
			MemoryMB:              req.MemoryMB,
			CDPPort:               9222,
			DesiredRootfsManifest: desiredManifest,
			DesiredRootfsVersion:  desiredVersion,
		}
		err := s.store.CreateBrowserVM(r.Context(), bvm)
		if err == nil {
			break
		}
		if !store.IsConflict(err) {
			writeError(w, http.StatusInternalServerError, "failed to create browser VM")
			return
		}
		if attempt == 2 {
			writeError(w, http.StatusConflict, "failed to generate unique slug, please try again")
			return
		}
		// Slug conflict -- retry with a new ID
	}

	slog.Info("browser_vm.created", "id", bvm.ID, "account_id", accountID, "slug", bvm.Slug)
	writeJSON(w, http.StatusCreated, bvm)
}

func (s *Server) handleGetBrowserVM(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	bvmID := chi.URLParam(r, "browserVmId")

	bvm, err := s.store.GetBrowserVM(r.Context(), bvmID)
	if err != nil {
		writeError(w, http.StatusNotFound, "browser VM not found")
		return
	}
	if bvm.AccountID != accountID {
		writeError(w, http.StatusForbidden, "browser VM does not belong to this account")
		return
	}
	// Match the list endpoint's shape: surface the paired machine so the
	// detail page can render it without a second round-trip. Looked up
	// only on the API boundary — internal GetBrowserVM callers don't need
	// it and shouldn't pay for the extra query. ErrNoRows means unpaired,
	// which is a normal state.
	if pm, perr := s.store.GetMachineByBrowserVMID(r.Context(), bvm.ID); perr == nil && pm != nil {
		bvm.PairedMachine = &store.BrowserVMPairedMachine{ID: pm.ID, Name: pm.Name}
	}
	writeJSON(w, http.StatusOK, bvm)
}

func (s *Server) handleDeleteBrowserVM(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	bvmID := chi.URLParam(r, "browserVmId")

	// cleanupCtx inherits request values (auth, tracing) but not cancellation.
	// Any persistence step that mutates durable state MUST use this context
	// so a client disconnect mid-delete cannot strand the DB record with the
	// agent-side VM already destroyed. The actual outbound calls (placement,
	// agent) stay on r.Context() so clients can still cancel the operation.
	cleanupCtx := context.WithoutCancel(r.Context())

	bvm, err := s.store.GetBrowserVM(r.Context(), bvmID)
	if err != nil {
		writeError(w, http.StatusNotFound, "browser VM not found")
		return
	}
	if bvm.AccountID != accountID {
		writeError(w, http.StatusForbidden, "browser VM does not belong to this account")
		return
	}

	// Auto-unpair if paired to a machine — full cleanup (firewall, CDP target, config).
	// Uses cleanupCtx: a canceled request must still tear down the pairing.
	pairedMachine, err := s.store.GetMachineByBrowserVMID(r.Context(), bvmID)
	if err == nil && pairedMachine != nil {
		s.cleanupBrowserPairing(cleanupCtx, pairedMachine, bvm)
		slog.Info("browser_vm.delete.auto_unpaired", "browser_vm_id", bvmID, "machine_id", pairedMachine.ID)
	}

	// Stop if running: call agent to destroy. If the agent call fails (e.g.
	// host unreachable), keep the DB record so the user can retry — deleting
	// it would orphan the Firecracker process on the host with no way to
	// track it from the control plane. Status is set to "error" so it's
	// visible in the UI.
	if browserVMMayExistOnAgent(bvm) {
		host, hostErr := s.store.GetHost(r.Context(), *bvm.HostID)
		if hostErr != nil || host == nil {
			slog.Error("browser_vm.delete.host_lookup_failed", "browser_vm_id", bvmID, "error", hostErr)
			errMsg := "host lookup failed — retry delete later"
			_ = s.store.UpdateBrowserVMStatus(cleanupCtx, bvmID, "error", &errMsg)
			writeError(w, http.StatusServiceUnavailable, "host unavailable — browser VM record kept for retry")
			return
		}
		if s.agentClient != nil {
			if err := s.agentClient.DestroyBrowserVM(r.Context(), host, bvmID); err != nil {
				slog.Error("browser_vm.delete.destroy_failed", "browser_vm_id", bvmID, "error", err)
				errMsg := "agent destroy failed: " + err.Error()
				_ = s.store.UpdateBrowserVMStatus(cleanupCtx, bvmID, "error", &errMsg)
				writeError(w, http.StatusBadGateway,
					"agent destroy failed — browser VM record kept for retry: "+err.Error())
				return
			}
		}
		_ = s.store.ReleaseBrowserVMPlacement(cleanupCtx, bvmID)
		_ = s.store.UnassignBrowserVMFromHost(cleanupCtx, bvmID)
	}

	if err := s.store.DeleteBrowserVM(cleanupCtx, bvmID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete browser VM")
		return
	}

	slog.Info("browser_vm.deleted", "id", bvmID, "account_id", accountID)
	w.WriteHeader(http.StatusNoContent)
}

// callerOccupiesHost reports whether the account currently runs — or is homed
// on — at least one machine on the given host. Used to gate tenant-supplied
// host_id placement: if the caller already knows the host exists via a
// machine they own there, pinning a browser VM to the same host leaks no new
// fleet information.
func (s *Server) callerOccupiesHost(ctx context.Context, accountID, hostID int) (bool, error) {
	machines, err := s.store.ListMachinesByAccount(ctx, accountID)
	if err != nil {
		return false, err
	}
	for _, m := range machines {
		if m.HostID != nil && *m.HostID == hostID {
			return true, nil
		}
		if m.HomeHostID != nil && *m.HomeHostID == hostID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) handleStartBrowserVM(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	bvmID := chi.URLParam(r, "browserVmId")

	// cleanupCtx inherits request values but not cancellation. Every
	// compensating DB write on an error path must use this context — a
	// canceled r.Context() would silently no-op the cleanup and strand the
	// record in "provisioning" with placement capacity debited and IP
	// pinned. See H6 in the browser branch review.
	cleanupCtx := context.WithoutCancel(r.Context())

	bvm, err := s.store.GetBrowserVM(r.Context(), bvmID)
	if err != nil {
		writeError(w, http.StatusNotFound, "browser VM not found")
		return
	}
	if bvm.AccountID != accountID {
		writeError(w, http.StatusForbidden, "browser VM does not belong to this account")
		return
	}
	if bvm.Status != "stopped" && bvm.Status != "error" {
		writeError(w, http.StatusConflict, fmt.Sprintf("browser VM is %s, must be stopped or error to start", bvm.Status))
		return
	}
	if bvm.Status == "error" && bvm.HostID != nil {
		writeError(w, http.StatusConflict, "browser VM has an unknown agent-side resource; stop or delete it before retrying start")
		return
	}
	if browserVMUsesDisabledLegacyRootfs(bvm) {
		writeError(w, http.StatusConflict, "legacy CDP browser rootfs is disabled; create a kernel browser VM")
		return
	}

	var req struct {
		HostID int    `json:"host_id"`
		Region string `json:"region"`
	}
	// Body is optional — placement overrides are superuser opt-ins. Treat
	// io.EOF as "no overrides" but reject any other decode failure so a
	// malformed payload doesn't silently fall through to default placement.
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}

	// host_id pinning is required by the pair-and-create flow so the browser
	// VM lands on the same host as the machine it's paired with (cdpproxy
	// only routes within a host). Allow non-superusers to pin only to a host
	// they already occupy — that preserves H9's fleet-enumeration guard
	// (tenants can't probe host IDs they don't already know about) while
	// keeping the UI workable without superuser credentials.
	//
	// Region pinning stays superuser-only: tenants already choose region
	// indirectly through machine placement, and an unguarded region field
	// would let one account flood a single region's capacity.
	if req.HostID != 0 && !isSuperuser(r.Context()) {
		owned, err := s.callerOccupiesHost(r.Context(), accountID, req.HostID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to verify host ownership")
			return
		}
		if !owned {
			writeError(w, http.StatusForbidden, "host_id must reference a host where this account already runs a machine")
			return
		}
	}
	if req.Region != "" && !isSuperuser(r.Context()) {
		writeError(w, http.StatusForbidden, "region placement overrides require admin role")
		return
	}

	// Set status to provisioning
	if err := s.store.UpdateBrowserVMStatus(r.Context(), bvmID, "provisioning", nil); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update status")
		return
	}

	// Reserve capacity via placement service
	placementReq := fleet.PlacementRequest{
		VCPUs:                 bvm.VCPUs,
		MemoryMB:              bvm.MemoryMB,
		Region:                req.Region,
		TargetHostID:          req.HostID,
		RequireBrowserReflink: browserVMRequiresReflink(bvm),
	}

	host, vmIP, placeErr := s.placement.ReserveBrowserVM(r.Context(), bvmID, placementReq)
	if placeErr != nil {
		errMsg := placeErr.Error()
		_ = s.store.UpdateBrowserVMStatus(cleanupCtx, bvmID, "error", &errMsg)
		slog.Error("browser_vm.start.placement_failed", "browser_vm_id", bvmID, "error", placeErr)
		writeError(w, http.StatusServiceUnavailable, "no capacity available: "+placeErr.Error())
		return
	}

	// Assign to host
	if err := s.store.AssignBrowserVMToHost(r.Context(), bvmID, host.ID, vmIP); err != nil {
		errMsg := err.Error()
		_ = s.store.UpdateBrowserVMStatus(cleanupCtx, bvmID, "error", &errMsg)
		_ = s.store.ReleaseBrowserVMPlacement(cleanupCtx, bvmID)
		slog.Error("browser_vm.start.assign_failed", "browser_vm_id", bvmID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to assign browser VM to host")
		return
	}

	// Call agent to create the VM
	if s.agentClient != nil {
		if err := s.agentClient.CreateBrowserVM(r.Context(), host, bvmID, vmIP, bvm.VCPUs, bvm.MemoryMB, strPtrValue(bvm.DesiredRootfsManifest), strPtrValue(bvm.DesiredRootfsVersion)); err != nil {
			errMsg := err.Error()
			_ = s.store.UpdateBrowserVMStatus(cleanupCtx, bvmID, "error", &errMsg)
			if agentclient.IsOutcomeUnknown(err) {
				slog.Error("browser_vm.start.agent_create_unknown",
					"browser_vm_id", bvmID,
					"host_id", host.ID,
					"vm_ip", vmIP,
					"error", err)
				writeError(w, http.StatusGatewayTimeout, "browser VM create outcome unknown on agent: "+err.Error())
				return
			}
			_ = s.store.ReleaseBrowserVMPlacement(cleanupCtx, bvmID)
			_ = s.store.UnassignBrowserVMFromHost(cleanupCtx, bvmID)
			slog.Error("browser_vm.start.agent_create_failed", "browser_vm_id", bvmID, "error", err)
			writeError(w, http.StatusBadGateway, "failed to create browser VM on agent: "+err.Error())
			return
		}
		if err := s.waitForBrowserVMReady(r.Context(), host, bvmID); err != nil {
			errMsg := err.Error()
			_ = s.store.UpdateBrowserVMStatus(cleanupCtx, bvmID, "error", &errMsg)
			slog.Error("browser_vm.start.readiness_unknown",
				"browser_vm_id", bvmID,
				"host_id", host.ID,
				"vm_ip", vmIP,
				"error", err)
			writeError(w, http.StatusGatewayTimeout, "browser VM readiness timed out; agent resource ownership preserved: "+err.Error())
			return
		}
	}

	// Activate placement and set running. Uses cleanupCtx so a client that
	// disconnects during the final finalization still lands the VM in a
	// clean "running" state rather than leaving it as "provisioning".
	//
	// Activation failure doesn't block the start (the VM is already up
	// and CDP-ready) but leaves the placement row in 'reserved' instead
	// of 'active'. That drift is the kind of thing an operator needs to
	// see, so log it at warn rather than silently dropping the error.
	if err := s.store.ActivateBrowserVMPlacement(cleanupCtx, bvmID); err != nil {
		slog.Warn("browser_vm.start.activate_placement_failed", "browser_vm_id", bvmID, "error", err)
	}
	if err := s.store.UpdateBrowserVMStatus(cleanupCtx, bvmID, "running", nil); err != nil {
		slog.Error("browser_vm.start.status_update_failed", "browser_vm_id", bvmID, "error", err)
	}

	// Re-fetch to return the updated browser VM
	bvm, _ = s.store.GetBrowserVM(r.Context(), bvmID)
	slog.Info("browser_vm.started", "id", bvmID, "host_id", host.ID, "vm_ip", vmIP)
	writeJSON(w, http.StatusOK, bvm)
}

func (s *Server) waitForBrowserVMReady(ctx context.Context, host *store.Host, browserVMID string) error {
	if s.agentClient == nil {
		return nil
	}

	// The kernel-browser rootfs (Ubuntu + Chromium + Neko + Xvfb) can take
	// 2+ minutes to become CDP-ready on first boot. Give it enough time.
	waitCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastErr error
	for {
		health, err := s.agentClient.GetBrowserVMHealth(waitCtx, host, browserVMID)
		if err == nil && health.CDPReachable {
			slog.Info("browser_vm.ready", "browser_vm_id", browserVMID, "cdp_version", health.CDPVersion, "live_reachable", health.LiveReachable)
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("agent status=%s cdp_reachable=%t live_reachable=%t", health.Status, health.CDPReachable, health.LiveReachable)
		}

		select {
		case <-waitCtx.Done():
			return fmt.Errorf("%w: %v", waitCtx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func (s *Server) handleStopBrowserVM(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	bvmID := chi.URLParam(r, "browserVmId")

	// cleanupCtx mirrors the delete handler: every persistence write that
	// finalizes stopped state must survive a client disconnect, while the
	// outbound agent/placement calls stay on r.Context() so clients can
	// still cancel.
	cleanupCtx := context.WithoutCancel(r.Context())

	bvm, err := s.store.GetBrowserVM(r.Context(), bvmID)
	if err != nil {
		writeError(w, http.StatusNotFound, "browser VM not found")
		return
	}
	if bvm.AccountID != accountID {
		writeError(w, http.StatusForbidden, "browser VM does not belong to this account")
		return
	}
	if bvm.Status != "running" && bvm.Status != "provisioning" && (bvm.Status != "error" || bvm.HostID == nil) {
		writeError(w, http.StatusConflict, fmt.Sprintf("browser VM is %s, must be running, provisioning, or error with an assigned host to stop", bvm.Status))
		return
	}

	// Auto-unpair if paired to a machine — full cleanup (firewall, CDP target, config)
	pairedMachine, err := s.store.GetMachineByBrowserVMID(r.Context(), bvmID)
	if err == nil && pairedMachine != nil {
		s.cleanupBrowserPairing(cleanupCtx, pairedMachine, bvm)
		slog.Info("browser_vm.stop.auto_unpaired", "browser_vm_id", bvmID, "machine_id", pairedMachine.ID)
	}

	// Call agent to destroy the VM. A failed destroy CANNOT be swallowed:
	// dropping the DB placement + setting status=stopped while a live
	// Firecracker/Neko lingers on the host would orphan the VM out of the
	// control plane's view. Surface the failure, keep the DB pointing at
	// the host, and let the user retry. See review finding #2 on the
	// browser branch.
	if bvm.HostID != nil && s.agentClient != nil {
		host, hostErr := s.store.GetHost(r.Context(), *bvm.HostID)
		if hostErr != nil || host == nil {
			slog.Error("browser_vm.stop.host_lookup_failed", "browser_vm_id", bvmID, "error", hostErr)
			errMsg := "host lookup failed — retry stop later"
			_ = s.store.UpdateBrowserVMStatus(cleanupCtx, bvmID, "error", &errMsg)
			writeError(w, http.StatusServiceUnavailable, "host unavailable — browser VM record kept for retry")
			return
		}
		if err := s.agentClient.DestroyBrowserVM(r.Context(), host, bvmID); err != nil {
			slog.Error("browser_vm.stop.destroy_failed", "browser_vm_id", bvmID, "error", err)
			errMsg := "agent destroy failed: " + err.Error()
			_ = s.store.UpdateBrowserVMStatus(cleanupCtx, bvmID, "error", &errMsg)
			writeError(w, http.StatusBadGateway,
				"agent destroy failed — browser VM record kept for retry: "+err.Error())
			return
		}
	}

	// Destroy succeeded (or there was no agent to call). Release placement and unassign.
	// Log (don't fail) cleanup errors: the VM is gone from the host, so the stop is
	// user-visibly complete. Drift between DB and host is the kind of thing operators
	// need to see, so mirror the start handler's slog.Warn pattern.
	if err := s.store.ReleaseBrowserVMPlacement(cleanupCtx, bvmID); err != nil {
		slog.Warn("browser_vm.stop.release_placement_failed", "browser_vm_id", bvmID, "error", err)
	}
	if err := s.store.UnassignBrowserVMFromHost(cleanupCtx, bvmID); err != nil {
		slog.Warn("browser_vm.stop.unassign_host_failed", "browser_vm_id", bvmID, "error", err)
	}
	if err := s.store.UpdateBrowserVMStatus(cleanupCtx, bvmID, "stopped", nil); err != nil {
		slog.Error("browser_vm.stop.status_update_failed", "browser_vm_id", bvmID, "error", err)
	}

	// Re-fetch to return the updated browser VM (matches start handler shape)
	bvm, _ = s.store.GetBrowserVM(r.Context(), bvmID)
	slog.Info("browser_vm.stopped", "id", bvmID, "account_id", accountID)
	writeJSON(w, http.StatusOK, bvm)
}

// ---- Pairing handlers ----

func (s *Server) handlePairBrowser(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")

	var req struct {
		BrowserVMID string `json:"browser_vm_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BrowserVMID == "" {
		writeError(w, http.StatusBadRequest, "browser_vm_id is required")
		return
	}

	// Validate machine
	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}
	if machine.Status != "running" {
		writeError(w, http.StatusConflict, "machine must be running to pair a browser VM")
		return
	}

	// Validate browser VM
	bvm, err := s.store.GetBrowserVM(r.Context(), req.BrowserVMID)
	if err != nil {
		writeError(w, http.StatusNotFound, "browser VM not found")
		return
	}
	if bvm.AccountID != accountID {
		writeError(w, http.StatusForbidden, "browser VM does not belong to this account")
		return
	}
	if bvm.Status != "running" {
		writeError(w, http.StatusConflict, "browser VM must be running to pair")
		return
	}

	// Validate same host
	if machine.HostID == nil || bvm.HostID == nil || *machine.HostID != *bvm.HostID {
		writeError(w, http.StatusConflict, "machine and browser VM must be on the same host")
		return
	}

	// Check browser VM is not already paired to another machine
	existingMachine, err := s.store.GetMachineByBrowserVMID(r.Context(), req.BrowserVMID)
	if err == nil && existingMachine != nil && existingMachine.ID != machineID {
		writeError(w, http.StatusConflict, "browser VM is already paired to another machine")
		return
	}

	// Check if the machine already has a different browser VM paired. If
	// so, run the full unpair cleanup for the previous one before swapping
	// — otherwise firewall rules and CDP target for the old VM would be
	// orphaned (the DB pointer is about to be overwritten).
	if machine.BrowserVMID != nil && *machine.BrowserVMID != req.BrowserVMID {
		prevBVM, prevErr := s.store.GetBrowserVM(r.Context(), *machine.BrowserVMID)
		switch {
		case prevErr == nil && prevBVM != nil:
			slog.Info("browser_vm.pair.replacing_previous",
				"machine_id", machineID,
				"previous_browser_vm_id", prevBVM.ID,
				"new_browser_vm_id", req.BrowserVMID)
			s.cleanupBrowserPairing(r.Context(), machine, prevBVM)
		case errors.Is(prevErr, pgx.ErrNoRows):
			// Previous browser VM record is genuinely gone (deleted elsewhere);
			// just clear the DB pointer so the PairBrowserVM call below doesn't
			// trip the unique index.
			_ = s.store.UnpairBrowserVM(r.Context(), machineID)
		default:
			// Transient DB error — do NOT wipe the DB pointer. Doing so would
			// orphan the old browser VM's firewall rules and CDP target on the
			// host with no retry path. Surface the error and let the caller
			// retry the pair request.
			slog.Error("browser_vm.pair.previous_lookup_failed",
				"machine_id", machineID,
				"previous_browser_vm_id", *machine.BrowserVMID,
				"error", prevErr)
			writeError(w, http.StatusServiceUnavailable,
				"failed to look up previously paired browser VM — retry: "+prevErr.Error())
			return
		}
	}

	// Pair in DB
	if err := s.store.PairBrowserVM(r.Context(), machineID, req.BrowserVMID); err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			writeError(w, http.StatusConflict, "browser VM is already paired to another machine")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to pair browser VM")
		return
	}

	// Call agent to add firewall rules. The host lookup is now mandatory:
	// a transient GetHost error would otherwise silently skip the firewall
	// block and return 200 with the DB row paired and the CDP target set
	// (in the next block) but no bridge rules installed — traffic to the
	// browser VM would fail silently. Fail-loud and rollback the pairing.
	if s.agentClient != nil && machine.VMIP != nil {
		host, hostErr := s.store.GetHost(r.Context(), *machine.HostID)
		if hostErr != nil || host == nil {
			_ = s.store.UnpairBrowserVM(r.Context(), machineID)
			slog.Error("browser_vm.pair.host_lookup_failed_for_firewall",
				"browser_vm_id", req.BrowserVMID, "machine_id", machineID,
				"host_id", *machine.HostID, "error", hostErr)
			writeError(w, http.StatusServiceUnavailable,
				"failed to look up host for firewall setup — retry")
			return
		}
		if err := s.agentClient.PairBrowserVM(r.Context(), host, req.BrowserVMID, *machine.VMIP); err != nil {
			// Rollback pairing on firewall failure
			_ = s.store.UnpairBrowserVM(r.Context(), machineID)
			slog.Error("browser_vm.pair.firewall_failed", "browser_vm_id", req.BrowserVMID, "machine_id", machineID, "error", err)
			writeError(w, http.StatusBadGateway, "failed to configure firewall rules: "+err.Error())
			return
		}
	}

	// Set CDP proxy target on agent metadata server (same mechanism as CLI browse).
	// This is required — without it the bridge CDP proxy has no target and browser
	// traffic fails even though the config says cdpUrl points at the proxy.
	// Failure here rolls back the pairing + firewall rules.
	if s.agentClient != nil && bvm.VMIP != nil {
		host, hostErr := s.store.GetHost(r.Context(), *machine.HostID)
		if hostErr != nil || host == nil {
			slog.Error("browser_vm.pair.host_lookup_failed", "machine_id", machineID, "host_id", *machine.HostID, "error", hostErr)
			_ = s.store.UnpairBrowserVM(r.Context(), machineID)
			writeError(w, http.StatusInternalServerError, "failed to resolve host for CDP target")
			return
		}
		cdpTarget := fmt.Sprintf("%s:9222", *bvm.VMIP)
		if err := s.agentClient.SetCDPTarget(r.Context(), host, machineID, cdpTarget); err != nil {
			slog.Error("browser_vm.pair.cdp_target_failed", "machine_id", machineID, "target", cdpTarget, "error", err)
			// Rollback: remove firewall rules and unpair
			if machine.VMIP != nil {
				_ = s.agentClient.UnpairBrowserVM(r.Context(), host, req.BrowserVMID, *machine.VMIP)
			}
			_ = s.store.UnpairBrowserVM(r.Context(), machineID)
			writeError(w, http.StatusBadGateway, "failed to set CDP proxy target: "+err.Error())
			return
		}
	}

	slog.Info("browser_vm.paired", "browser_vm_id", req.BrowserVMID, "machine_id", machineID)

	// Push browser config + restart gateway (same approach as CLI browse)
	go s.pushBrowserConfigAsync(machineID, true)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":        "paired",
		"browser_vm_id": req.BrowserVMID,
		"machine_id":    machineID,
	})
}

func (s *Server) handleUnpairBrowser(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")

	// Validate machine
	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	if machine.BrowserVMID == nil {
		writeError(w, http.StatusConflict, "machine does not have a paired browser VM")
		return
	}
	browserVMID := *machine.BrowserVMID

	// Agent-side cleanup MUST succeed before we drop the DB pointer. If we
	// clear the DB first and then a bridge rule or CDP target removal fails,
	// the machine appears unpaired to the user while the host still routes
	// traffic through the old firewall rules and CDP target — and because
	// the DB reference is gone, there is no retry path left to clean up.
	// So we fail-loud on any agent-side error and leave the DB row intact
	// so the user (or a retry) can try again. The stop/delete flow has its
	// own best-effort cleanup path (cleanupBrowserPairing) for the case
	// where the browser VM is going away regardless.
	if s.agentClient != nil && machine.HostID != nil && machine.VMIP != nil {
		host, hostErr := s.store.GetHost(r.Context(), *machine.HostID)
		if hostErr != nil || host == nil {
			slog.Error("browser_vm.unpair.host_lookup_failed",
				"browser_vm_id", browserVMID, "machine_id", machineID, "error", hostErr)
			writeError(w, http.StatusServiceUnavailable, "failed to look up host for unpair — retry")
			return
		}
		if err := s.agentClient.UnpairBrowserVM(r.Context(), host, browserVMID, *machine.VMIP); err != nil {
			slog.Error("browser_vm.unpair.firewall_failed",
				"browser_vm_id", browserVMID, "machine_id", machineID, "error", err)
			writeError(w, http.StatusBadGateway, "agent failed to remove firewall rules — retry: "+err.Error())
			return
		}
		if err := s.agentClient.ResetCDPTarget(r.Context(), host, machineID); err != nil {
			slog.Error("browser_vm.unpair.cdp_target_reset_failed",
				"machine_id", machineID, "error", err)
			writeError(w, http.StatusBadGateway, "agent failed to reset CDP target — retry: "+err.Error())
			return
		}
	}

	// Agent-side cleanup succeeded (or there was no agent to call). Safe to
	// drop the DB pairing now.
	if err := s.store.UnpairBrowserVM(r.Context(), machineID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to unpair browser VM")
		return
	}

	slog.Info("browser_vm.unpaired", "browser_vm_id", browserVMID, "machine_id", machineID)

	// Remove browser config + restart gateway
	go s.pushBrowserConfigAsync(machineID, false)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":     "unpaired",
		"machine_id": machineID,
	})
}

// cleanupBrowserPairing performs full unpair cleanup: firewall rules, CDP target,
// DB unpair, and config push. Used when stopping/deleting a paired browser VM to
// prevent stale iptables rules and CDP target entries from leaking when the
// browser VM's IP is later reused.
//
// Agent-side cleanup failures are logged and swallowed: for delete/stop, the
// browser VM is going away regardless, and blocking on a dead agent would
// strand the record. The DB side is made crash-safe by migration 072's
// machines.browser_vm_id ON DELETE SET NULL — even if the UnpairBrowserVM
// call below fails, a subsequent DeleteBrowserVM will still succeed and the
// machine's pairing pointer will be auto-cleared instead of FK-erroring.
func (s *Server) cleanupBrowserPairing(ctx context.Context, machine *store.Machine, bvm *store.BrowserVM) {
	// Remove firewall rules and reset CDP target on the agent
	if s.agentClient != nil && machine.HostID != nil && machine.VMIP != nil && bvm != nil {
		host, hostErr := s.store.GetHost(ctx, *machine.HostID)
		if hostErr == nil && host != nil {
			if bvm.VMIP != nil {
				if err := s.agentClient.UnpairBrowserVM(ctx, host, bvm.ID, *machine.VMIP); err != nil {
					slog.Warn("browser_vm.cleanup.firewall_failed", "machine_id", machine.ID, "browser_vm_id", bvm.ID, "error", err)
				}
			}
			if err := s.agentClient.ResetCDPTarget(ctx, host, machine.ID); err != nil {
				slog.Warn("browser_vm.cleanup.cdp_reset_failed", "machine_id", machine.ID, "error", err)
			}
		}
	}

	// Clear DB pairing
	if err := s.store.UnpairBrowserVM(ctx, machine.ID); err != nil {
		slog.Warn("browser_vm.cleanup.unpair_failed", "machine_id", machine.ID, "error", err)
	}

	// Push config to remove cdpUrl (only if machine is running)
	if machine.Status == "running" && machine.HostID != nil {
		go s.pushBrowserConfigAsync(machine.ID, false)
	}
}

// browserConfigPairedJSON is the browser config pushed when a browser VM is paired.
// Uses the bridge gateway IP (192.168.100.1:9222) where the agent's CDP proxy listens.
// The CDP proxy resolves the source VM IP → browser target via SetBrowserTarget/GetBrowserTarget
// and forwards TCP to the actual browser VM. Same proxy mechanism the CLI browse uses.
// Note: headless:false — Chromium runs under Xvfb inside the browser VM
// so Neko can capture the display for WebRTC streaming.
// ssrfPolicy.allowedHostnames permits the bridge IP past OpenClaw v2026.4.14+'s
// fail-closed private-network SSRF default. See configassembly/assembler.go for the
// parallel emit path used on full config re-assembly.
const browserConfigPairedJSON = `{"enabled":true,"cdpUrl":"http://192.168.100.1:9222","attachOnly":true,"noSandbox":true,"headless":false,"ssrfPolicy":{"allowedHostnames":["192.168.100.1"]}}`

// pushBrowserConfigAsync pushes browser config to a running machine after pair/unpair.
// Uses the same single-op ConfigBatch approach as CLI browse (not diff-based),
// because the gateway rejects individual browser sub-keys like "browser.attachOnly".
func (s *Server) pushBrowserConfigAsync(machineID string, paired bool) {
	ctx := context.Background()

	machine, err := s.store.GetMachine(ctx, machineID)
	if err != nil {
		slog.Warn("push_browser_config.get_machine_failed", "machine_id", machineID, "error", err)
		return
	}
	if machine.Status != "running" || machine.HostID == nil || machine.ProxyToken == nil {
		return
	}

	host, err := s.store.GetHost(ctx, *machine.HostID)
	if err != nil || host == nil || s.agentClient == nil {
		return
	}

	var ops []agentclient.ConfigOp
	if paired {
		ops = []agentclient.ConfigOp{{
			Op: "set", Path: "browser", Value: browserConfigPairedJSON, StrictJSON: true,
		}}
	} else {
		ops = []agentclient.ConfigOp{{
			Op: "unset", Path: "browser",
		}}
	}

	errs := s.agentClient.ConfigBatch(ctx, host, machineID, *machine.ProxyToken, ops)
	if len(errs) > 0 {
		slog.Warn("push_browser_config.config_batch_failed", "machine_id", machineID, "error", errs[0])
		return
	}
	slog.Info("push_browser_config.ok", "machine_id", machineID, "paired", paired)

	// Browser config changes require a gateway restart (hot-reload ignores them).
	if err := s.agentClient.RestartGateway(ctx, host, machineID, *machine.ProxyToken); err != nil {
		slog.Warn("push_browser_config.gateway_restart_failed", "machine_id", machineID, "error", err)
	}
}
