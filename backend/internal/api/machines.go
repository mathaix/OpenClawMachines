package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/backup"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/fleet"
	"github.com/mathaix/openclawmachines/backend/internal/orchestrator"
	"github.com/mathaix/openclawmachines/backend/internal/rootfs"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

// ---- Machine handlers ----

func (s *Server) handleListMachines(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machines, err := s.store.ListMachinesByAccount(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, machines)
}

func (s *Server) handleCreateMachine(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	claims := auth.UserFromContext(r.Context())

	var req struct {
		Name            string            `json:"name"`
		Kind            string            `json:"kind,omitempty"`
		Size            string            `json:"size"`
		PreferredRegion string            `json:"preferred_region,omitempty"`
		RootfsVersion   string            `json:"rootfs_version,omitempty"`
		OpenclawVersion string            `json:"openclaw_version,omitempty"`
		HermesVersion   string            `json:"hermes_version,omitempty"`
		Channel         string            `json:"channel,omitempty"`
		RuntimeSource   *string           `json:"runtime_source,omitempty"`
		AutoStart       bool              `json:"auto_start"`
		Secrets         map[string]string `json:"secrets,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	machineKind := store.NormalizeMachineKind(strings.ToLower(strings.TrimSpace(req.Kind)))
	if !store.ValidMachineKind(machineKind) {
		writeError(w, http.StatusBadRequest, "kind must be one of: openclaw, hermes")
		return
	}
	preferredRegion := strings.ToLower(strings.TrimSpace(req.PreferredRegion))
	if preferredRegion != "" && !isValidRegionCode(preferredRegion) {
		writeError(w, http.StatusBadRequest, "invalid preferred_region format")
		return
	}
	desiredRootfsVersion := normalizeOptionalString(req.RootfsVersion)
	desiredOpenclawVersion := normalizeOptionalString(req.OpenclawVersion)
	if machineKind == store.MachineKindHermes && desiredOpenclawVersion == nil {
		desiredOpenclawVersion = normalizeOptionalString(req.HermesVersion)
	}
	desiredChannel := normalizeOptionalString(req.Channel)
	runtimeSource, ok := normalizePresentString(req.RuntimeSource)
	if !ok {
		writeError(w, http.StatusBadRequest, "runtime_source must be: artifact")
		return
	}
	if desiredChannel != nil && !isValidMachineChannel(*desiredChannel) {
		writeError(w, http.StatusBadRequest, "channel must be one of: stable, rc, dev")
		return
	}
	if desiredChannel != nil && (desiredRootfsVersion != nil || desiredOpenclawVersion != nil) {
		writeError(w, http.StatusBadRequest, "channel cannot be combined with pinned rootfs_version/openclaw_version")
		return
	}
	if desiredRootfsVersion != nil {
		if err := validateRootfsVersion(*desiredRootfsVersion); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid rootfs_version: %v", err))
			return
		}
	}
	if desiredOpenclawVersion != nil {
		if err := rootfs.ValidateOpenClawVersion(*desiredOpenclawVersion); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid openclaw_version: %v", err))
			return
		}
	}
	if runtimeSource != nil && !isValidRuntimeSource(*runtimeSource) {
		writeError(w, http.StatusBadRequest, "runtime_source must be: artifact")
		return
	}
	if runtimeSource == nil {
		runtimeSource = machineStrPtr("artifact")
	}
	versionSource := deriveMachineVersionSource(desiredRootfsVersion, desiredOpenclawVersion, desiredChannel)
	slog.Info("machine.create.request_versions",
		"account_id", accountID,
		"kind", machineKind,
		"name", req.Name,
		"desired_rootfs_version", strPtrValue(desiredRootfsVersion),
		"desired_openclaw_version", strPtrValue(desiredOpenclawVersion),
		"desired_channel", strPtrValue(desiredChannel),
		"version_source", versionSource,
	)

	// Resolve machine size from size ID, or use default.
	var size *MachineSize
	if req.Size != "" {
		size = LookupSize(req.Size)
		if size == nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown size %q — use GET /api/sizes for valid options", req.Size))
			return
		}
	} else {
		size = DefaultSize()
	}

	// Auto-generate short slug with retry on conflict
	var machine *store.Machine
	for attempt := 0; attempt < 3; attempt++ {
		var preferredRegionPtr *string
		if preferredRegion != "" {
			preferredRegionPtr = &preferredRegion
		}
		machine = &store.Machine{
			AccountID:              accountID,
			Kind:                   machineKind,
			Name:                   req.Name,
			Slug:                   generateShortID(),
			PreferredRegion:        preferredRegionPtr,
			Status:                 "stopped",
			VCPUs:                  size.VCPUs,
			MemoryMB:               size.MemoryMB,
			DataVolumeGB:           size.DiskGB,
			DesiredRootfsVersion:   desiredRootfsVersion,
			DesiredOpenclawVersion: desiredOpenclawVersion,
			DesiredChannel:         desiredChannel,
			VersionSource:          &versionSource,
			RuntimeSource:          runtimeSource,
		}
		err := s.store.CreateMachine(r.Context(), machine)
		if err == nil {
			slog.Info("machine.create.persisted_versions",
				"machine_id", machine.ID,
				"desired_rootfs_version", strPtrValue(machine.DesiredRootfsVersion),
				"desired_openclaw_version", strPtrValue(machine.DesiredOpenclawVersion),
				"desired_channel", strPtrValue(machine.DesiredChannel),
				"version_source", strPtrValue(machine.VersionSource),
			)
			break
		}
		if !store.IsConflict(err) {
			writeError(w, http.StatusInternalServerError, "failed to create machine")
			return
		}
		if attempt == 2 {
			writeError(w, http.StatusConflict, "failed to generate unique slug, please try again")
			return
		}
		// Slug conflict — retry with a new ID
	}

	// Auto-enable memory-core plugin for all new machines
	if machine.Kind == store.MachineKindOpenClaw {
		if err := s.store.EnableMachinePlugin(r.Context(), machine.ID, "memory-core", nil); err != nil {
			slog.Error("machine.create.memory_plugin.failed", "machine_id", machine.ID, "error", err)
		}

		// Auto-enable opik-openclaw observability plugin for all new OpenClaw machines.
		if err := s.store.EnableMachinePlugin(r.Context(), machine.ID, "opik-openclaw", nil); err != nil {
			slog.Error("machine.create.opik_plugin.failed", "machine_id", machine.ID, "error", err)
		}

		// Auto-enable composio integration plugin for all new OpenClaw machines.
		if err := s.store.EnableMachinePlugin(r.Context(), machine.ID, "composio", nil); err != nil {
			slog.Warn("machine.create.composio_plugin.failed", "machine_id", machine.ID, "error", err)
		} else {
			slog.Info("machine.create.composio_plugin.enabled", "machine_id", machine.ID)
		}
	}

	// Auto-enable backups for all new machines
	if s.backupMasterKey != "" {
		machineKey, err := backup.GenerateKey()
		if err != nil {
			slog.Error("machine.create.backup_key.generate.failed", "machine_id", machine.ID, "error", err)
		} else {
			masterKey, err := hex.DecodeString(s.backupMasterKey)
			if err != nil {
				slog.Error("machine.create.backup_key.master_decode.failed", "machine_id", machine.ID, "error", err)
			} else {
				encryptedKey, err := backup.EncryptKey(machineKey, masterKey)
				if err != nil {
					slog.Error("machine.create.backup_key.encrypt.failed", "machine_id", machine.ID, "error", err)
				} else if err := s.store.EnableBackups(r.Context(), machine.ID, encryptedKey); err != nil {
					slog.Error("machine.create.backup_enable.failed", "machine_id", machine.ID, "error", err)
				} else {
					machine.BackupsEnabled = true
				}
			}
		}
	}

	// Store secrets if provided
	if len(req.Secrets) > 0 && s.secretKey != "" {
		for key, value := range req.Secrets {
			encrypted, err := crypto.Encrypt(value, s.secretKey)
			if err != nil {
				slog.Error("machine.create.secret.encrypt.failed", "machine_id", machine.ID, "secret_key", key, "error", err)
				continue
			}
			if err := s.store.SetSecret(r.Context(), machine.ID, key, encrypted); err != nil {
				slog.Error("machine.create.secret.store.failed", "machine_id", machine.ID, "secret_key", key, "error", err)
			}
		}
	}

	// Log machine creation event
	s.activity.Log(r.Context(), events.LogParams{
		Category:  "machine",
		Action:    "machine.created",
		Status:    "success",
		ActorType: "user",
		ActorID:   &claims.UserID,
		AccountID: &accountID,
		MachineID: &machine.ID,
		Summary:   fmt.Sprintf("Created machine '%s'", machine.Name),
	})

	// If auto_start not requested, return machine as-is
	if !req.AutoStart {
		writeJSON(w, http.StatusCreated, machine)
		return
	}

	// Auto-start: attempt to start the machine immediately
	_, _, err := s.machines.Start(r.Context(), accountID, machine)
	if err != nil {
		slog.Error("machine.create.auto_start.failed", "machine_id", machine.ID, "error", err)
		errMsg := err.Error()
		s.activity.Log(r.Context(), events.LogParams{
			Category:  "machine",
			Action:    "machine.start_failed",
			Status:    "failure",
			ActorType: "user",
			ActorID:   &claims.UserID,
			AccountID: &accountID,
			MachineID: &machine.ID,
			Summary:   fmt.Sprintf("Failed to auto-start '%s': %s", machine.Name, err),
			ErrorMsg:  &errMsg,
		})
		// Return the machine with the start error — creation succeeded
		writeJSON(w, http.StatusCreated, struct {
			*store.Machine
			StartError string `json:"start_error,omitempty"`
		}{Machine: machine, StartError: err.Error()})
		return
	}

	machine.Status = "provisioning"
	writeJSON(w, http.StatusCreated, machine)
}

// generateShortID returns a 7-character random alphanumeric string (a-z, 0-9).
// Uses crypto/rand for secure randomness. 36^7 ≈ 78 billion combinations.
func generateShortID() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 7)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	for i := range b {
		b[i] = charset[b[i]%byte(len(charset))]
	}
	return string(b)
}

var validRegions = map[string]bool{
	// GCP regions
	"us-west1":        true,
	"us-central1":     true,
	"us-east1":        true,
	"europe-west1":    true,
	"europe-west4":    true,
	"asia-east1":      true,
	"asia-southeast1": true,
	// Non-GCP / OVH hosts
	"external": true,
	// Simplified aliases (future use)
	"us-west":        true,
	"us-central":     true,
	"us-east":        true,
	"eu-west":        true,
	"eu-central":     true,
	"eu-north":       true,
	"asia-east":      true,
	"asia-south":     true,
	"asia-southeast": true,
}

func isValidRegionCode(code string) bool {
	return validRegions[code]
}

var validMachineChannels = map[string]bool{
	"stable": true,
	"rc":     true,
	"dev":    true,
}

func isValidMachineChannel(channel string) bool {
	return validMachineChannels[channel]
}

var validRuntimeSources = map[string]bool{
	"artifact": true,
}

func isValidRuntimeSource(source string) bool {
	return validRuntimeSources[source]
}

func normalizeOptionalString(raw string) *string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func normalizePresentString(raw *string) (*string, bool) {
	if raw == nil {
		return nil, true
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil, false
	}
	return &trimmed, true
}

func deriveMachineVersionSource(rootfsVersion, openclawVersion, channel *string) string {
	if rootfsVersion != nil || openclawVersion != nil {
		return "pinned"
	}
	if channel != nil {
		return "channel"
	}
	return "default"
}

func machineStrPtr(v string) *string {
	return &v
}

func strPtrValue(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func validateRootfsVersion(version string) error {
	version = strings.TrimSpace(version)
	switch {
	case version == "":
		return fmt.Errorf("rootfs version is required")
	case strings.Contains(version, ".."):
		return fmt.Errorf("invalid rootfs version %q", version)
	case strings.ContainsAny(version, `/\`):
		return fmt.Errorf("invalid rootfs version %q", version)
	case strings.ContainsAny(version, " \t\r\n"):
		return fmt.Errorf("invalid rootfs version %q", version)
	default:
		return nil
	}
}

func (s *Server) handleGetMachine(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	writeJSON(w, http.StatusOK, machine)
}

// checkOpenClawRootfsPairCompat enforces the runtime-compat guard for an
// openclaw+rootfs version pair: if the openclaw release carries a
// min_rootfs_version, rootfsVersion must be at least that new. Fails open
// when the openclaw release or either rootfs version is not in
// artifact_releases (dev builds, stale catalog) so the guard doesn't
// over-reject. Returns *store.IncompatibleRootfsError when the pair is unsafe.
func (s *Server) checkOpenClawRootfsPairCompat(ctx context.Context, openclawVersion, rootfsVersion string) error {
	if rootfsVersion == "" {
		return nil
	}
	release, err := s.store.GetArtifactRelease(ctx, "openclaw", openclawVersion)
	if err != nil || release == nil || release.MinRootfsVersion == nil {
		return nil
	}
	minVer := *release.MinRootfsVersion
	if rootfsVersion == minVer {
		return nil
	}
	minRow, err := s.store.GetArtifactRelease(ctx, "rootfs", minVer)
	if err != nil || minRow == nil {
		return nil
	}
	curRow, err := s.store.GetArtifactRelease(ctx, "rootfs", rootfsVersion)
	if err != nil || curRow == nil {
		return nil
	}
	return store.CheckOpenClawRootfsCompat(release, rootfsVersion, minRow.CreatedAt, curRow.CreatedAt)
}

// checkOpenClawPinCompat evaluates the compat guard against the machine's
// current running rootfs (actual, falling back to resolved). Used by endpoints
// that pin openclaw without simultaneously pinning rootfs.
func (s *Server) checkOpenClawPinCompat(ctx context.Context, openclawVersion string, m *store.Machine) error {
	currentRootfs := ""
	if m.ActualRootfsVersion != nil {
		currentRootfs = *m.ActualRootfsVersion
	}
	if currentRootfs == "" && m.ResolvedRootfsVersion != nil {
		currentRootfs = *m.ResolvedRootfsVersion
	}
	return s.checkOpenClawRootfsPairCompat(ctx, openclawVersion, currentRootfs)
}

func (s *Server) handleUpdateMachine(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	var req struct {
		Name            string  `json:"name"`
		VCPUs           int     `json:"vcpus"`
		MemoryMB        int     `json:"memory_mb"`
		CustomDomain    *string `json:"custom_domain"`
		BackupsEnabled  *bool   `json:"backups_enabled"`
		RootfsVersion   *string `json:"rootfs_version"`
		OpenclawVersion *string `json:"openclaw_version"`
		Channel         *string `json:"channel"`
		RuntimeSource   *string `json:"runtime_source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name != "" {
		machine.Name = req.Name
	}
	if req.VCPUs > 0 {
		if req.VCPUs < 1 || req.VCPUs > 8 {
			writeError(w, http.StatusBadRequest, "vcpus must be between 1 and 8")
			return
		}
		machine.VCPUs = req.VCPUs
	}
	if req.MemoryMB > 0 {
		if req.MemoryMB < 2048 || req.MemoryMB > 8192 {
			writeError(w, http.StatusBadRequest, "memory_mb must be between 2048 and 8192")
			return
		}
		machine.MemoryMB = req.MemoryMB
	}
	if req.CustomDomain != nil {
		machine.CustomDomain = req.CustomDomain
	}
	selectionChanged := false
	var requestedRootfsVersion *string
	var requestedOpenclawVersion *string
	var requestedChannel *string
	if req.RootfsVersion != nil {
		requestedRootfsVersion = normalizeOptionalString(*req.RootfsVersion)
		selectionChanged = true
	}
	if req.OpenclawVersion != nil {
		requestedOpenclawVersion = normalizeOptionalString(*req.OpenclawVersion)
		selectionChanged = true
	}
	if req.Channel != nil {
		requestedChannel = normalizeOptionalString(*req.Channel)
		selectionChanged = true
	}
	if requestedChannel != nil && (requestedRootfsVersion != nil || requestedOpenclawVersion != nil) {
		writeError(w, http.StatusBadRequest, "channel cannot be combined with pinned rootfs_version/openclaw_version")
		return
	}
	if requestedRootfsVersion != nil {
		if err := validateRootfsVersion(*requestedRootfsVersion); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid rootfs_version: %v", err))
			return
		}
	}
	if requestedOpenclawVersion != nil {
		if err := rootfs.ValidateOpenClawVersion(*requestedOpenclawVersion); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid openclaw_version: %v", err))
			return
		}
	}
	// Runtime-compat guard: if openclaw is being pinned but rootfs is not
	// updated in the same request, reject pins that require a newer rootfs
	// than the machine currently runs. Prevents Node-ABI crash loops.
	if requestedOpenclawVersion != nil && requestedRootfsVersion == nil {
		if err := s.checkOpenClawPinCompat(r.Context(), *requestedOpenclawVersion, machine); err != nil {
			var incompat *store.IncompatibleRootfsError
			if errors.As(err, &incompat) {
				writeError(w, http.StatusUnprocessableEntity, incompat.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("compat check: %v", err))
			return
		}
	}
	if requestedChannel != nil {
		machine.DesiredRootfsVersion = nil
		machine.DesiredOpenclawVersion = nil
	}
	if requestedRootfsVersion != nil || requestedOpenclawVersion != nil {
		machine.DesiredChannel = nil
	}
	if req.RootfsVersion != nil {
		machine.DesiredRootfsVersion = requestedRootfsVersion
	}
	if req.OpenclawVersion != nil {
		machine.DesiredOpenclawVersion = requestedOpenclawVersion
	}
	if req.Channel != nil {
		machine.DesiredChannel = requestedChannel
	}
	if req.RuntimeSource != nil {
		runtimeSource, ok := normalizePresentString(req.RuntimeSource)
		if !ok {
			writeError(w, http.StatusBadRequest, "runtime_source must be: artifact")
			return
		}
		machine.RuntimeSource = runtimeSource
	}
	if machine.DesiredChannel != nil && !isValidMachineChannel(*machine.DesiredChannel) {
		writeError(w, http.StatusBadRequest, "channel must be one of: stable, rc, dev")
		return
	}
	if machine.DesiredChannel != nil && (machine.DesiredRootfsVersion != nil || machine.DesiredOpenclawVersion != nil) {
		writeError(w, http.StatusBadRequest, "channel cannot be combined with pinned rootfs_version/openclaw_version")
		return
	}
	if machine.RuntimeSource == nil {
		machine.RuntimeSource = machineStrPtr("artifact")
	}
	if !isValidRuntimeSource(*machine.RuntimeSource) {
		writeError(w, http.StatusBadRequest, "runtime_source must be: artifact")
		return
	}
	if selectionChanged {
		versionSource := deriveMachineVersionSource(
			machine.DesiredRootfsVersion,
			machine.DesiredOpenclawVersion,
			machine.DesiredChannel,
		)
		machine.VersionSource = &versionSource
	}

	if req.BackupsEnabled != nil {
		if *req.BackupsEnabled && !machine.BackupsEnabled {
			// Only generate a new key if this machine has never had backups (no existing key).
			// Re-enabling preserves the original key so old backups remain decryptable.
			encryptedKey := machine.BackupKey
			if len(encryptedKey) == 0 {
				machineKey, err := backup.GenerateKey()
				if err != nil {
					writeError(w, http.StatusInternalServerError, "failed to generate backup key")
					return
				}
				masterKey, err := hex.DecodeString(s.backupMasterKey)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "backup not configured")
					return
				}
				encryptedKey, err = backup.EncryptKey(machineKey, masterKey)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "failed to encrypt backup key")
					return
				}
			}
			if err := s.store.EnableBackups(r.Context(), id, encryptedKey); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to enable backups")
				return
			}
			machine.BackupsEnabled = true
		} else if !*req.BackupsEnabled {
			if err := s.store.DisableBackups(r.Context(), id); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to disable backups")
				return
			}
			machine.BackupsEnabled = false
		}
	}

	if err := s.store.UpdateMachine(r.Context(), machine); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, machine)
}

func (s *Server) handleListOpenClawReleases(w http.ResponseWriter, r *http.Request) {
	channel := r.URL.Query().Get("channel")
	if channel == "" {
		channel = "stable"
	}
	machineKind := store.NormalizeMachineKind(r.URL.Query().Get("kind"))
	if !store.ValidMachineKind(machineKind) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid machine kind %q", r.URL.Query().Get("kind")))
		return
	}
	artifactKind := runtimeReleaseArtifactKind(machineKind)
	releases, err := s.store.ListArtifactReleases(r.Context(), artifactKind, channel, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Group by major version (e.g. "v2026.4") and return the latest revision for each.
	// Users pick a major version; the backend resolves the exact revision.
	type releaseItem struct {
		Version      string `json:"version"`       // major version shown in UI (e.g. "v2026.4")
		ExactVersion string `json:"exact_version"` // resolved revision (e.g. "v2026.4.2-r2")
		Channel      string `json:"channel"`
		CreatedAt    string `json:"created_at"`
	}
	seen := map[string]bool{}
	var out []releaseItem
	for _, rel := range releases {
		major := rel.Version
		if artifactKind == "openclaw" {
			major = majorVersion(rel.Version)
		}
		if seen[major] {
			continue
		}
		seen[major] = true
		out = append(out, releaseItem{
			Version:      major,
			ExactVersion: rel.Version,
			Channel:      rel.Channel,
			CreatedAt:    rel.CreatedAt.Format("2006-01-02"),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListRootfsReleases(w http.ResponseWriter, r *http.Request) {
	channel := r.URL.Query().Get("channel")
	if channel == "" {
		channel = "stable"
	}
	machineKind := store.NormalizeMachineKind(r.URL.Query().Get("kind"))
	if !store.ValidMachineKind(machineKind) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid machine kind %q", r.URL.Query().Get("kind")))
		return
	}
	releases, err := s.store.ListArtifactReleases(r.Context(), rootfsReleaseArtifactKind(machineKind), channel, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type releaseItem struct {
		Version      string `json:"version"`
		ExactVersion string `json:"exact_version"`
		Channel      string `json:"channel"`
		CreatedAt    string `json:"created_at"`
	}
	out := make([]releaseItem, 0, len(releases))
	for _, rel := range releases {
		out = append(out, releaseItem{
			Version:      rel.Version,
			ExactVersion: rel.Version,
			Channel:      rel.Channel,
			CreatedAt:    rel.CreatedAt.Format("2006-01-02"),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func rootfsReleaseArtifactKind(machineKind string) string {
	if store.NormalizeMachineKind(machineKind) == store.MachineKindHermes {
		return "hermes-rootfs"
	}
	return "rootfs"
}

func runtimeReleaseArtifactKind(machineKind string) string {
	if store.NormalizeMachineKind(machineKind) == store.MachineKindHermes {
		return "hermes"
	}
	return "openclaw"
}

// majorVersion extracts "v2026.4.2" from "v2026.4.2-r2".
// Keeps major.minor.patch, strips build suffixes like "-r2".
func majorVersion(v string) string {
	// Strip everything after first "-"
	if idx := strings.Index(v, "-"); idx > 0 {
		return v[:idx]
	}
	return v
}

type runtimeChangeResponse struct {
	Status              string         `json:"status"`
	TargetVersion       string         `json:"target_version"`
	RolledBackToVersion string         `json:"rolled_back_to_version,omitempty"`
	Error               string         `json:"error,omitempty"`
	Machine             *store.Machine `json:"machine,omitempty"`
}

func (s *Server) handleUpgradeMachineRootfs(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	claims := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	var req struct {
		Version       string  `json:"version"`
		ApplyNow      bool    `json:"apply_now"`
		RuntimeSource *string `json:"runtime_source,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	targetVersion := strings.TrimSpace(req.Version)
	if err := validateRootfsVersion(targetVersion); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	runtimeSource, ok := normalizePresentString(req.RuntimeSource)
	if !ok {
		writeError(w, http.StatusBadRequest, "runtime_source must be: artifact")
		return
	}
	if runtimeSource == nil {
		runtimeSource = machineStrPtr("artifact")
	}
	if !isValidRuntimeSource(*runtimeSource) {
		writeError(w, http.StatusBadRequest, "runtime_source must be: artifact")
		return
	}

	operationCtx := r.Context()
	cancelOperation := func() {}
	if req.ApplyNow && machine.Status == "running" {
		operationCtx, cancelOperation = context.WithTimeout(context.Background(), 10*time.Minute)
	}
	defer cancelOperation()

	updateOp := &store.MachineOperation{MachineID: machine.ID, Kind: "rootfs_update"}
	if err := s.store.CreateMachineOperation(operationCtx, updateOp); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	completeOp := func(state string, err error) {
		var errMsg *string
		if err != nil {
			msg := err.Error()
			errMsg = &msg
		}
		_ = s.store.CompleteMachineOperation(context.Background(), updateOp.ID, state, errMsg)
	}

	lockedMachine, err := s.store.GetMachine(operationCtx, id)
	if err != nil {
		completeOp("failed", err)
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if lockedMachine.AccountID != accountID {
		completeOp("failed", fmt.Errorf("machine does not belong to this account"))
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	previousRootfsVersion := currentMachineRootfsVersion(lockedMachine)
	previousOpenClawVersion := currentMachineOpenClawVersion(lockedMachine)
	previousRuntimeSource := currentMachineRuntimeSource(lockedMachine)
	hasPendingChange := lockedMachine.DesiredRootfsVersion != nil &&
		strings.TrimSpace(*lockedMachine.DesiredRootfsVersion) != "" &&
		strings.TrimSpace(*lockedMachine.DesiredRootfsVersion) != targetVersion
	if previousRootfsVersion != "" && targetVersion == previousRootfsVersion && *runtimeSource == previousRuntimeSource && !hasPendingChange {
		completeOp("completed", nil)
		s.logMachineRootfsActivity(r.Context(), claims, accountID, lockedMachine, "success",
			fmt.Sprintf("Rootfs upgrade already using %s", targetVersion),
			map[string]any{
				"target_version": targetVersion,
				"runtime_source": *runtimeSource,
				"apply_now":      req.ApplyNow,
				"noop":           true,
			},
			nil,
		)
		writeJSON(w, http.StatusOK, runtimeChangeResponse{
			Status:        "updated",
			TargetVersion: targetVersion,
			Machine:       lockedMachine,
		})
		return
	}

	pendingMachine := cloneMachineForOpenClawChange(lockedMachine)
	applyPinnedRootfsVersion(pendingMachine, targetVersion, *runtimeSource)

	if !req.ApplyNow || lockedMachine.Status != "running" {
		if err := s.store.UpdateMachine(operationCtx, pendingMachine); err != nil {
			completeOp("failed", err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		completeOp("completed", nil)
		s.logMachineRootfsActivity(r.Context(), claims, accountID, pendingMachine, "success",
			fmt.Sprintf("Queued rootfs upgrade to %s", targetVersion),
			map[string]any{
				"target_version": targetVersion,
				"runtime_source": *runtimeSource,
				"apply_now":      req.ApplyNow,
				"queued":         true,
			},
			nil,
		)
		writeJSON(w, http.StatusOK, runtimeChangeResponse{
			Status:        "pending_restart",
			TargetVersion: targetVersion,
			Machine:       pendingMachine,
		})
		return
	}

	updatedMachine, applyErr := s.restartMachineWithOpenClawHealthGate(operationCtx, pendingMachine, updateOp.ID)
	if applyErr == nil {
		persistedMachine, persistErr := s.persistPinnedMachineRootfsChange(operationCtx, pendingMachine.ID, targetVersion, previousOpenClawVersion, *runtimeSource)
		if persistErr != nil {
			completeOp("failed", persistErr)
			writeError(w, http.StatusInternalServerError, persistErr.Error())
			return
		}
		completeOp("completed", nil)
		s.logMachineRootfsActivity(context.Background(), claims, accountID, persistedMachine, "success",
			fmt.Sprintf("Applied rootfs upgrade to %s", targetVersion),
			map[string]any{
				"target_version":   targetVersion,
				"previous_version": previousRootfsVersion,
				"runtime_source":   *runtimeSource,
				"apply_now":        true,
			},
			nil,
		)
		writeJSON(w, http.StatusOK, runtimeChangeResponse{
			Status:        "updated",
			TargetVersion: targetVersion,
			Machine:       persistedMachine,
		})
		return
	}

	applyErrMsg := applyErr.Error()
	completeOp("failed", applyErr)

	if errors.Is(applyErr, orchestrator.ErrUpgradeRestoredPreviousVM) {
		responseMachine := updatedMachine
		if responseMachine == nil {
			responseMachine = lockedMachine
		}
		s.logMachineRootfsActivity(context.Background(), claims, accountID, responseMachine, "failure",
			fmt.Sprintf("Rootfs upgrade to %s failed and the previous VM was already restored", targetVersion),
			map[string]any{
				"target_version":        targetVersion,
				"previous_version":      previousRootfsVersion,
				"runtime_source":        *runtimeSource,
				"apply_now":             true,
				"rollback_already_done": true,
			},
			&applyErrMsg,
		)
		writeJSON(w, http.StatusConflict, runtimeChangeResponse{
			Status:              "rolled_back",
			TargetVersion:       targetVersion,
			RolledBackToVersion: previousRootfsVersion,
			Error:               fmt.Sprintf("rootfs upgrade failed and the previous VM was already restored: %s", applyErr),
			Machine:             responseMachine,
		})
		return
	}

	s.logMachineRootfsActivity(context.Background(), claims, accountID, lockedMachine, "failure",
		fmt.Sprintf("Rootfs upgrade to %s failed", targetVersion),
		map[string]any{
			"target_version":   targetVersion,
			"previous_version": previousRootfsVersion,
			"runtime_source":   *runtimeSource,
			"apply_now":        true,
		},
		&applyErrMsg,
	)
	writeJSON(w, http.StatusConflict, runtimeChangeResponse{
		Status:        "failed",
		TargetVersion: targetVersion,
		Error:         fmt.Sprintf("rootfs upgrade failed: %s", applyErr),
		Machine:       lockedMachine,
	})
}

func (s *Server) handleRollbackMachineRootfs(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	claims := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	var req struct {
		Version       string  `json:"version"`
		ApplyNow      bool    `json:"apply_now"`
		RuntimeSource *string `json:"runtime_source,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	targetVersion := strings.TrimSpace(req.Version)
	if err := validateRootfsVersion(targetVersion); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	runtimeSource, ok := normalizePresentString(req.RuntimeSource)
	if !ok {
		writeError(w, http.StatusBadRequest, "runtime_source must be: artifact")
		return
	}
	if runtimeSource == nil {
		runtimeSource = machineStrPtr("artifact")
	}
	if !isValidRuntimeSource(*runtimeSource) {
		writeError(w, http.StatusBadRequest, "runtime_source must be: artifact")
		return
	}

	operationCtx := r.Context()
	cancelOperation := func() {}
	if req.ApplyNow && machine.Status == "running" {
		operationCtx, cancelOperation = context.WithTimeout(context.Background(), 10*time.Minute)
	}
	defer cancelOperation()

	updateOp := &store.MachineOperation{MachineID: machine.ID, Kind: "rootfs_rollback"}
	if err := s.store.CreateMachineOperation(operationCtx, updateOp); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	completeOp := func(state string, err error) {
		var errMsg *string
		if err != nil {
			msg := err.Error()
			errMsg = &msg
		}
		_ = s.store.CompleteMachineOperation(context.Background(), updateOp.ID, state, errMsg)
	}

	lockedMachine, err := s.store.GetMachine(operationCtx, id)
	if err != nil {
		completeOp("failed", err)
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if lockedMachine.AccountID != accountID {
		completeOp("failed", fmt.Errorf("machine does not belong to this account"))
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	previousRootfsVersion := currentMachineRootfsVersion(lockedMachine)
	previousOpenClawVersion := currentMachineOpenClawVersion(lockedMachine)

	pendingMachine := cloneMachineForOpenClawChange(lockedMachine)
	applyPinnedRootfsVersion(pendingMachine, targetVersion, *runtimeSource)

	if !req.ApplyNow || lockedMachine.Status != "running" {
		if err := s.store.UpdateMachine(operationCtx, pendingMachine); err != nil {
			completeOp("failed", err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		completeOp("completed", nil)
		s.logMachineRootfsActivity(r.Context(), claims, accountID, pendingMachine, "success",
			fmt.Sprintf("Queued rootfs rollback to %s", targetVersion),
			map[string]any{
				"target_version": targetVersion,
				"runtime_source": *runtimeSource,
				"apply_now":      req.ApplyNow,
				"queued":         true,
				"rollback":       true,
			},
			nil,
		)
		writeJSON(w, http.StatusOK, runtimeChangeResponse{
			Status:        "pending_restart",
			TargetVersion: targetVersion,
			Machine:       pendingMachine,
		})
		return
	}

	updatedMachine, applyErr := s.restartMachineWithOpenClawHealthGate(operationCtx, pendingMachine, updateOp.ID)
	if applyErr == nil {
		persistedMachine, persistErr := s.persistPinnedMachineRootfsChange(operationCtx, pendingMachine.ID, targetVersion, previousOpenClawVersion, *runtimeSource)
		if persistErr != nil {
			completeOp("failed", persistErr)
			writeError(w, http.StatusInternalServerError, persistErr.Error())
			return
		}
		completeOp("completed", nil)
		s.logMachineRootfsActivity(context.Background(), claims, accountID, persistedMachine, "success",
			fmt.Sprintf("Rolled back rootfs to %s", targetVersion),
			map[string]any{
				"target_version":   targetVersion,
				"previous_version": previousRootfsVersion,
				"runtime_source":   *runtimeSource,
				"apply_now":        true,
				"rollback":         true,
			},
			nil,
		)
		writeJSON(w, http.StatusOK, runtimeChangeResponse{
			Status:              "rolled_back",
			TargetVersion:       targetVersion,
			RolledBackToVersion: targetVersion,
			Machine:             persistedMachine,
		})
		return
	}

	applyErrMsg := applyErr.Error()
	completeOp("failed", applyErr)

	if errors.Is(applyErr, orchestrator.ErrUpgradeRestoredPreviousVM) {
		responseMachine := updatedMachine
		if responseMachine == nil {
			responseMachine = lockedMachine
		}
		s.logMachineRootfsActivity(context.Background(), claims, accountID, responseMachine, "failure",
			fmt.Sprintf("Rootfs rollback to %s failed and the previous VM was already restored", targetVersion),
			map[string]any{
				"target_version":        targetVersion,
				"previous_version":      previousRootfsVersion,
				"runtime_source":        *runtimeSource,
				"apply_now":             true,
				"rollback":              true,
				"rollback_already_done": true,
			},
			&applyErrMsg,
		)
		writeJSON(w, http.StatusConflict, runtimeChangeResponse{
			Status:              "rolled_back",
			TargetVersion:       targetVersion,
			RolledBackToVersion: previousRootfsVersion,
			Error:               fmt.Sprintf("rootfs rollback failed and the previous VM was already restored: %s", applyErr),
			Machine:             responseMachine,
		})
		return
	}

	s.logMachineRootfsActivity(context.Background(), claims, accountID, lockedMachine, "failure",
		fmt.Sprintf("Rootfs rollback to %s failed", targetVersion),
		map[string]any{
			"target_version":   targetVersion,
			"previous_version": previousRootfsVersion,
			"runtime_source":   *runtimeSource,
			"apply_now":        true,
			"rollback":         true,
		},
		&applyErrMsg,
	)
	writeJSON(w, http.StatusConflict, runtimeChangeResponse{
		Status:              "rollback_failed",
		TargetVersion:       targetVersion,
		RolledBackToVersion: previousRootfsVersion,
		Error:               fmt.Sprintf("rootfs rollback failed: %s", applyErr),
		Machine:             lockedMachine,
	})
}

func (s *Server) handleUpgradeMachineOpenClaw(w http.ResponseWriter, r *http.Request) {
	s.handleApplyMachineOpenClawChange(w, r, false)
}

func (s *Server) handleRollbackMachineOpenClaw(w http.ResponseWriter, r *http.Request) {
	s.handleApplyMachineOpenClawChange(w, r, true)
}

func (s *Server) handleApplyMachineOpenClawChange(w http.ResponseWriter, r *http.Request, rollback bool) {
	accountID := accountIDFromContext(r.Context())
	claims := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	var req struct {
		Version       string  `json:"version"`
		ApplyNow      bool    `json:"apply_now"`
		RuntimeSource *string `json:"runtime_source,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	targetVersion := strings.TrimSpace(req.Version)
	if err := rootfs.ValidateOpenClawVersion(targetVersion); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Runtime-compat guard (upgrade only; rollback is allowed to revert below the floor).
	// The openclaw/upgrade endpoint pins openclaw without changing rootfs, so the
	// machine's current rootfs must satisfy the target release's min_rootfs_version.
	if !rollback {
		if err := s.checkOpenClawPinCompat(r.Context(), targetVersion, machine); err != nil {
			var incompat *store.IncompatibleRootfsError
			if errors.As(err, &incompat) {
				writeError(w, http.StatusUnprocessableEntity, incompat.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("compat check: %v", err))
			return
		}
	}

	runtimeSource, ok := normalizePresentString(req.RuntimeSource)
	if !ok {
		writeError(w, http.StatusBadRequest, "runtime_source must be: artifact")
		return
	}
	if runtimeSource == nil {
		runtimeSource = machineStrPtr("artifact")
	}
	if !isValidRuntimeSource(*runtimeSource) {
		writeError(w, http.StatusBadRequest, "runtime_source must be: artifact")
		return
	}

	runningNow := machine.Status == "running"
	actionName := "upgrade"
	successStatus := "updated"
	queuedStatus := "pending_restart"
	operationKind := "openclaw_update"
	if rollback {
		actionName = "rollback"
		successStatus = "rolled_back"
		operationKind = "openclaw_rollback"
	}

	operationCtx := r.Context()
	cancelOperation := func() {}
	if req.ApplyNow && runningNow {
		operationCtx, cancelOperation = context.WithTimeout(context.Background(), 10*time.Minute)
	}
	defer cancelOperation()

	updateOp := &store.MachineOperation{MachineID: machine.ID, Kind: operationKind}
	if err := s.store.CreateMachineOperation(operationCtx, updateOp); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	completeOp := func(state string, err error) {
		var errMsg *string
		if err != nil {
			msg := err.Error()
			errMsg = &msg
		}
		_ = s.store.CompleteMachineOperation(context.Background(), updateOp.ID, state, errMsg)
	}

	lockedMachine, err := s.store.GetMachine(operationCtx, id)
	if err != nil {
		completeOp("failed", err)
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if lockedMachine.AccountID != accountID {
		completeOp("failed", fmt.Errorf("machine does not belong to this account"))
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}
	runningNow = lockedMachine.Status == "running"

	previousVersion := currentMachineOpenClawVersion(lockedMachine)
	previousRuntimeSource := currentMachineRuntimeSource(lockedMachine)
	previousRootfsVersion := currentMachineRootfsVersion(lockedMachine)
	// Also check if there's a different queued version — even if target matches the
	// currently resolved version, we're not a noop if there's a pending change.
	hasPendingChange := lockedMachine.DesiredOpenclawVersion != nil &&
		strings.TrimSpace(*lockedMachine.DesiredOpenclawVersion) != "" &&
		strings.TrimSpace(*lockedMachine.DesiredOpenclawVersion) != targetVersion
	isFirstVersion := previousVersion == ""
	if !isFirstVersion && targetVersion == previousVersion && *runtimeSource == previousRuntimeSource && !hasPendingChange {
		completeOp("completed", nil)
		s.logMachineOpenClawActivity(r.Context(), claims, accountID, lockedMachine, rollback, "success",
			fmt.Sprintf("OpenClaw %s already using %s", actionName, targetVersion),
			map[string]any{
				"target_version": targetVersion,
				"runtime_source": *runtimeSource,
				"apply_now":      req.ApplyNow,
				"noop":           true,
			},
			nil,
		)
		writeJSON(w, http.StatusOK, runtimeChangeResponse{
			Status:        successStatus,
			TargetVersion: targetVersion,
			Machine:       lockedMachine,
		})
		return
	}

	pendingMachine := cloneMachineForOpenClawChange(lockedMachine)
	applyPinnedOpenClawVersion(pendingMachine, targetVersion, *runtimeSource)

	if !req.ApplyNow || !runningNow {
		if err := s.store.UpdateMachine(operationCtx, pendingMachine); err != nil {
			completeOp("failed", err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		completeOp("completed", nil)
		s.logMachineOpenClawActivity(r.Context(), claims, accountID, pendingMachine, rollback, "success",
			fmt.Sprintf("Queued OpenClaw %s to %s", actionName, targetVersion),
			map[string]any{
				"target_version": targetVersion,
				"runtime_source": *runtimeSource,
				"apply_now":      req.ApplyNow,
				"queued":         true,
			},
			nil,
		)
		writeJSON(w, http.StatusOK, runtimeChangeResponse{
			Status:        queuedStatus,
			TargetVersion: targetVersion,
			Machine:       pendingMachine,
		})
		return
	}

	updatedMachine, applyErr := s.restartMachineWithOpenClawHealthGate(operationCtx, pendingMachine, updateOp.ID)
	if applyErr == nil {
		persistedMachine, persistErr := s.persistPinnedMachineOpenClawChange(operationCtx, pendingMachine.ID, previousRootfsVersion, targetVersion, *runtimeSource)
		if persistErr != nil {
			completeOp("failed", persistErr)
			writeError(w, http.StatusInternalServerError, persistErr.Error())
			return
		}
		completeOp("completed", nil)
		s.logMachineOpenClawActivity(context.Background(), claims, accountID, persistedMachine, rollback, "success",
			fmt.Sprintf("Applied OpenClaw %s to %s", actionName, targetVersion),
			map[string]any{
				"target_version":   targetVersion,
				"previous_version": previousVersion,
				"runtime_source":   *runtimeSource,
				"apply_now":        true,
			},
			nil,
		)
		writeJSON(w, http.StatusOK, runtimeChangeResponse{
			Status:        successStatus,
			TargetVersion: targetVersion,
			Machine:       persistedMachine,
		})
		return
	}

	applyErrMsg := applyErr.Error()
	_ = updatedMachine
	completeOp("failed", applyErr)

	if errors.Is(applyErr, orchestrator.ErrUpgradeRestoredPreviousVM) {
		responseMachine := updatedMachine
		if responseMachine == nil {
			responseMachine = lockedMachine
		}
		s.logMachineOpenClawActivity(context.Background(), claims, accountID, responseMachine, rollback, "failure",
			fmt.Sprintf("OpenClaw %s to %s failed and the previous VM was already restored", actionName, targetVersion),
			map[string]any{
				"target_version":          targetVersion,
				"previous_version":        previousVersion,
				"runtime_source":          *runtimeSource,
				"apply_now":               true,
				"rollback_attempted":      false,
				"rollback_already_done":   true,
				"rollback_target":         previousVersion,
				"rollback_runtime_source": previousRuntimeSource,
			},
			&applyErrMsg,
		)
		writeJSON(w, http.StatusConflict, runtimeChangeResponse{
			Status:              "rolled_back",
			TargetVersion:       targetVersion,
			RolledBackToVersion: previousVersion,
			Error:               fmt.Sprintf("openclaw %s failed and the previous VM was already restored: %s", actionName, applyErr),
			Machine:             responseMachine,
		})
		return
	}

	rolledBackMachine, rollbackErr := s.rollbackMachineOpenClawVersion(operationCtx, pendingMachine.ID, previousRootfsVersion, previousVersion, previousRuntimeSource)
	if rollbackErr != nil {
		rollbackErrMsg := rollbackErr.Error()
		statusMsg := fmt.Sprintf("OpenClaw %s to %s failed and rollback to %s also failed — manual intervention required", actionName, targetVersion, previousVersion)
		_ = s.store.UpdateMachineStatus(operationCtx, pendingMachine.ID, "error", &statusMsg)
		s.logMachineOpenClawActivity(context.Background(), claims, accountID, lockedMachine, rollback, "failure",
			statusMsg,
			map[string]any{
				"target_version":       targetVersion,
				"previous_version":     previousVersion,
				"runtime_source":       *runtimeSource,
				"apply_now":            true,
				"rollback_attempted":   true,
				"rollback_target":      previousVersion,
				"rollback_runtime_src": previousRuntimeSource,
			},
			&rollbackErrMsg,
		)
		writeJSON(w, http.StatusInternalServerError, runtimeChangeResponse{
			Status:              "rollback_failed",
			TargetVersion:       targetVersion,
			RolledBackToVersion: previousVersion,
			Error:               fmt.Sprintf("openclaw %s failed: %s; rollback failed: %s", actionName, applyErr, rollbackErr),
			Machine:             rolledBackMachine,
		})
		return
	}

	s.logMachineOpenClawActivity(context.Background(), claims, accountID, rolledBackMachine, rollback, "failure",
		fmt.Sprintf("OpenClaw %s to %s failed health gate and rolled back to %s", actionName, targetVersion, previousVersion),
		map[string]any{
			"target_version":       targetVersion,
			"previous_version":     previousVersion,
			"runtime_source":       *runtimeSource,
			"apply_now":            true,
			"rollback_attempted":   true,
			"rollback_target":      previousVersion,
			"rollback_runtime_src": previousRuntimeSource,
		},
		&applyErrMsg,
	)
	writeJSON(w, http.StatusConflict, runtimeChangeResponse{
		Status:              "rolled_back",
		TargetVersion:       targetVersion,
		RolledBackToVersion: previousVersion,
		Error:               fmt.Sprintf("openclaw %s failed health gate: %s", actionName, applyErr),
		Machine:             rolledBackMachine,
	})
}

func (s *Server) restartMachineWithOpenClawHealthGate(ctx context.Context, machine *store.Machine, operationID string) (*store.Machine, error) {
	if machine.HostID == nil {
		return nil, fmt.Errorf("machine has no host assignment")
	}
	if s.machines == nil {
		return nil, fmt.Errorf("machine runtime service is not configured")
	}
	host, _, err := s.machines.UpgradeWithOperation(ctx, machine.AccountID, machine, operationID)
	if err != nil {
		if errors.Is(err, orchestrator.ErrUpgradeRestoredPreviousVM) {
			updatedMachine, reloadErr := s.store.GetMachine(ctx, machine.ID)
			if reloadErr != nil {
				return nil, fmt.Errorf("upgrade runtime in place: %w (and reload machine after restore failed: %v)", err, reloadErr)
			}
			return updatedMachine, fmt.Errorf("upgrade runtime in place: %w", err)
		}
		return nil, fmt.Errorf("upgrade runtime in place: %w", err)
	}

	placementID := ""
	if placement, err := s.store.GetActivePlacement(ctx, machine.ID); err == nil && placement != nil {
		placementID = placement.ID
	}
	if err := s.waitForMachineRunningOnHost(ctx, machine.ID, host, placementID, 3*time.Minute); err != nil {
		return nil, err
	}

	updatedMachine, err := s.store.GetMachine(ctx, machine.ID)
	if err != nil {
		return nil, fmt.Errorf("reload machine after openclaw change: %w", err)
	}
	if err := s.waitForGatewayHealth(ctx, updatedMachine, 90*time.Second); err != nil {
		return nil, fmt.Errorf("gateway health gate failed: %w", err)
	}
	return updatedMachine, nil
}

func (s *Server) rollbackMachineOpenClawVersion(ctx context.Context, machineID, rootfsVersion, version, runtimeSource string) (*store.Machine, error) {
	if strings.TrimSpace(version) == "" {
		return nil, fmt.Errorf("no rollback version available")
	}

	machine, err := s.store.GetMachine(ctx, machineID)
	if err != nil {
		return nil, fmt.Errorf("reload machine for rollback: %w", err)
	}
	if machine.HostID == nil {
		return machine, fmt.Errorf("machine has no host assignment for rollback")
	}

	rollbackOp := &store.MachineOperation{MachineID: machine.ID, Kind: "openclaw_rollback"}
	if err := s.store.CreateMachineOperation(ctx, rollbackOp); err != nil {
		return machine, fmt.Errorf("create rollback operation: %w", err)
	}
	completeOp := func(state string, err error) {
		var errMsg *string
		if err != nil {
			msg := err.Error()
			errMsg = &msg
		}
		_ = s.store.CompleteMachineOperation(context.Background(), rollbackOp.ID, state, errMsg)
	}

	pendingMachine := cloneMachineForOpenClawChange(machine)
	applyPinnedOpenClawVersion(pendingMachine, version, runtimeSource)

	rolledBackMachine, restartErr := s.restartMachineWithOpenClawHealthGate(ctx, pendingMachine, rollbackOp.ID)
	if restartErr != nil {
		completeOp("failed", restartErr)
		return machine, fmt.Errorf("restart rollback target: %w", restartErr)
	}
	_ = rolledBackMachine

	persistedMachine, err := s.persistPinnedMachineOpenClawChange(ctx, machine.ID, rootfsVersion, version, runtimeSource)
	if err != nil {
		completeOp("failed", err)
		return machine, fmt.Errorf("persist rollback selection: %w", err)
	}
	completeOp("completed", nil)
	return persistedMachine, nil
}

// handleRuntimeUpgrade atomically pins both rootfs and openclaw desired versions.
// It is the endpoint the UI's combined Upgrade banner hits, so two writes can't
// race with a concurrent machine start between them. The per-field compat guard
// on PATCH /machines is bypassed here by design — both fields move together.
func (s *Server) handleRuntimeUpgrade(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	var req struct {
		RootfsVersion   string `json:"rootfs_version"`
		OpenclawVersion string `json:"openclaw_version"`
		RuntimeSource   string `json:"runtime_source,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	rootfsVersion := strings.TrimSpace(req.RootfsVersion)
	openclawVersion := strings.TrimSpace(req.OpenclawVersion)
	if rootfsVersion == "" {
		writeError(w, http.StatusBadRequest, "rootfs_version is required")
		return
	}
	if openclawVersion == "" {
		writeError(w, http.StatusBadRequest, "openclaw_version is required")
		return
	}
	if err := validateRootfsVersion(rootfsVersion); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid rootfs_version: %v", err))
		return
	}
	if err := rootfs.ValidateOpenClawVersion(openclawVersion); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid openclaw_version: %v", err))
		return
	}

	// Verify the proposed pair is internally compatible. Otherwise a caller
	// could submit an old rootfs together with a new openclaw and bypass the
	// per-field guard on PATCH /machines.
	if err := s.checkOpenClawRootfsPairCompat(r.Context(), openclawVersion, rootfsVersion); err != nil {
		var incompat *store.IncompatibleRootfsError
		if errors.As(err, &incompat) {
			writeError(w, http.StatusUnprocessableEntity, incompat.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("compat check: %v", err))
		return
	}

	runtimeSource := strings.TrimSpace(req.RuntimeSource)
	if runtimeSource == "" {
		if machine.RuntimeSource != nil && *machine.RuntimeSource != "" {
			runtimeSource = *machine.RuntimeSource
		} else {
			runtimeSource = "artifact"
		}
	}
	if !isValidRuntimeSource(runtimeSource) {
		writeError(w, http.StatusBadRequest, "runtime_source must be: artifact")
		return
	}

	applyPinnedRootfsVersion(machine, rootfsVersion, runtimeSource)
	applyPinnedOpenClawVersion(machine, openclawVersion, runtimeSource)

	if err := s.store.UpdateMachine(r.Context(), machine); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runtimeChangeResponse{
		Status:        "pending_restart",
		TargetVersion: openclawVersion,
		Machine:       machine,
	})
}

func applyPinnedOpenClawVersion(machine *store.Machine, version, runtimeSource string) {
	version = strings.TrimSpace(version)
	runtimeSource = strings.TrimSpace(runtimeSource)
	machine.DesiredOpenclawVersion = &version
	machine.DesiredChannel = nil
	versionSource := "pinned"
	machine.VersionSource = &versionSource
	machine.RuntimeSource = &runtimeSource
}

func applyPinnedRootfsVersion(machine *store.Machine, version, runtimeSource string) {
	version = strings.TrimSpace(version)
	runtimeSource = strings.TrimSpace(runtimeSource)
	machine.DesiredRootfsVersion = &version
	machine.DesiredChannel = nil
	versionSource := "pinned"
	machine.VersionSource = &versionSource
	machine.RuntimeSource = &runtimeSource
}

func currentMachineOpenClawVersion(machine *store.Machine) string {
	return firstMachineString(
		machine.ResolvedOpenclawVersion,
		machine.OpenclawVersion,
		machine.DesiredOpenclawVersion,
	)
}

func currentMachineRootfsVersion(machine *store.Machine) string {
	return firstMachineString(
		machine.ResolvedRootfsVersion,
		machine.RootfsSnapshot,
		machine.DesiredRootfsVersion,
	)
}

func currentMachineRuntimeSource(machine *store.Machine) string {
	return firstMachineString(machine.RuntimeSource, machineStrPtr("artifact"))
}

func cloneMachineForOpenClawChange(machine *store.Machine) *store.Machine {
	if machine == nil {
		return nil
	}
	cp := *machine
	return &cp
}

func (s *Server) persistPinnedMachineOpenClawChange(ctx context.Context, machineID, rootfsVersion, targetVersion, runtimeSource string) (*store.Machine, error) {
	if err := s.store.UpdateMachineVersion(ctx, machineID, rootfsVersion, targetVersion); err != nil {
		return nil, fmt.Errorf("persist resolved openclaw version: %w", err)
	}
	machine, err := s.store.GetMachine(ctx, machineID)
	if err != nil {
		return nil, fmt.Errorf("reload machine after version update: %w", err)
	}
	applyPinnedOpenClawVersion(machine, targetVersion, runtimeSource)
	if err := s.store.UpdateMachine(ctx, machine); err != nil {
		return nil, fmt.Errorf("persist desired openclaw selection: %w", err)
	}
	return machine, nil
}

func (s *Server) persistPinnedMachineRootfsChange(ctx context.Context, machineID, targetVersion, openClawVersion, runtimeSource string) (*store.Machine, error) {
	if err := s.store.UpdateMachineVersion(ctx, machineID, targetVersion, openClawVersion); err != nil {
		return nil, fmt.Errorf("persist resolved rootfs version: %w", err)
	}
	machine, err := s.store.GetMachine(ctx, machineID)
	if err != nil {
		return nil, fmt.Errorf("reload machine after version update: %w", err)
	}
	applyPinnedRootfsVersion(machine, targetVersion, runtimeSource)
	if err := s.store.UpdateMachine(ctx, machine); err != nil {
		return nil, fmt.Errorf("persist desired rootfs selection: %w", err)
	}
	return machine, nil
}

func firstMachineString(values ...*string) string {
	for _, value := range values {
		if value == nil {
			continue
		}
		if trimmed := strings.TrimSpace(*value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (s *Server) logMachineOpenClawActivity(ctx context.Context, claims *auth.Claims, accountID int, machine *store.Machine, rollback bool, status, summary string, detail map[string]any, errMsg *string) {
	action := "machine.openclaw_updated"
	if rollback {
		action = "machine.openclaw_rolled_back"
	}
	if status != "success" {
		action += "_failed"
	}

	params := events.LogParams{
		Category:  "machine",
		Action:    action,
		Status:    status,
		ActorType: "user",
		AccountID: &accountID,
		Summary:   summary,
		Detail:    detail,
		ErrorMsg:  errMsg,
	}
	if claims != nil {
		params.ActorID = &claims.UserID
	}
	if machine != nil {
		params.MachineID = &machine.ID
	}
	s.activity.Log(ctx, params)
}

func (s *Server) logMachineRootfsActivity(ctx context.Context, claims *auth.Claims, accountID int, machine *store.Machine, status, summary string, detail map[string]any, errMsg *string) {
	action := "machine.rootfs_updated"
	if status != "success" {
		action += "_failed"
	}

	params := events.LogParams{
		Category:  "machine",
		Action:    action,
		Status:    status,
		ActorType: "user",
		AccountID: &accountID,
		Summary:   summary,
		Detail:    detail,
		ErrorMsg:  errMsg,
	}
	if claims != nil {
		params.ActorID = &claims.UserID
	}
	if machine != nil {
		params.MachineID = &machine.ID
	}
	s.activity.Log(ctx, params)
}

func (s *Server) handleDeleteMachine(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	claims := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	// Best-effort cleanup of Composio connections
	if s.composioClient != nil && s.composioClient.Enabled() {
		if err := s.composioClient.DeleteAllConnections(r.Context(), machine.ID); err != nil {
			slog.Warn("machine.delete.composio_cleanup_failed", "machine_id", machine.ID, "error", err)
		} else {
			slog.Info("machine.delete.composio_cleanup_done", "machine_id", machine.ID)
		}
	}

	if err := s.machines.Delete(r.Context(), machine); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "machine",
		Action:    "machine.deleted",
		Status:    "success",
		ActorType: "user",
		ActorID:   &claims.UserID,
		AccountID: &accountID,
		MachineID: &machine.ID,
		Summary:   fmt.Sprintf("Deleted machine '%s'", machine.Name),
	})

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStartMachine(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	claims := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	if machine.Status == "running" {
		writeError(w, http.StatusConflict, "machine already running")
		return
	}

	host, vmIP, err := s.machines.Start(r.Context(), accountID, machine)
	if err != nil {
		errMsg := err.Error()
		s.activity.Log(r.Context(), events.LogParams{
			Category:  "machine",
			Action:    "machine.start_failed",
			Status:    "failure",
			ActorType: "user",
			ActorID:   &claims.UserID,
			AccountID: &accountID,
			MachineID: &machine.ID,
			Summary:   fmt.Sprintf("Failed to start '%s': %s", machine.Name, err),
			ErrorMsg:  &errMsg,
		})
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	hostID := host.ID
	s.activity.Log(r.Context(), events.LogParams{
		Category:  "machine",
		Action:    "machine.started",
		Status:    "success",
		ActorType: "user",
		ActorID:   &claims.UserID,
		AccountID: &accountID,
		MachineID: &machine.ID,
		HostID:    &hostID,
		Summary:   fmt.Sprintf("Started '%s' on %s", machine.Name, host.VMName),
	})

	// "starting" for restart (has host assignment), "provisioning" for first boot
	status := "provisioning"
	if machine.HostID != nil {
		status = "starting"
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  status,
		"host_id": host.ID,
		"vm_ip":   vmIP,
	})
}

func (s *Server) handleStopMachine(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	claims := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	if err := s.machines.Stop(r.Context(), machine); err != nil {
		errMsg := err.Error()
		s.activity.Log(r.Context(), events.LogParams{
			Category:  "machine",
			Action:    "machine.stop_failed",
			Status:    "failure",
			ActorType: "user",
			ActorID:   &claims.UserID,
			AccountID: &accountID,
			MachineID: &machine.ID,
			Summary:   fmt.Sprintf("Failed to stop '%s': %s", machine.Name, err),
			ErrorMsg:  &errMsg,
		})
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "machine",
		Action:    "machine.stopped",
		Status:    "success",
		ActorType: "user",
		ActorID:   &claims.UserID,
		AccountID: &accountID,
		MachineID: &machine.ID,
		Summary:   fmt.Sprintf("Stopped '%s'", machine.Name),
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *Server) handleSetCDPTarget(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}
	if machine.Status != "running" {
		writeError(w, http.StatusConflict, "machine is not running")
		return
	}

	var req struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Target == "" {
		writeError(w, http.StatusBadRequest, "target is required")
		return
	}

	if s.agentClient != nil && machine.HostID != nil {
		host, err := s.store.GetHost(r.Context(), *machine.HostID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to resolve host")
			return
		}
		if err := s.agentClient.SetCDPTarget(r.Context(), host, machine.ID, req.Target); err != nil {
			slog.Error("machine.cdp_target.set.failed", "machine_id", machine.ID, "error", err)
			writeError(w, http.StatusBadGateway, "failed to set CDP target on agent")
			return
		}
	}

	claims := auth.UserFromContext(r.Context())
	logParams := events.LogParams{
		Category:  "machine",
		Action:    "machine.cdp_target_set",
		Status:    "success",
		ActorType: "user",
		AccountID: &accountID,
		MachineID: &id,
		Summary:   fmt.Sprintf("CDP target set to '%s' on '%s'", req.Target, machine.Name),
		Detail:    map[string]any{"target": req.Target},
	}
	if claims != nil {
		logParams.ActorID = &claims.UserID
	}
	s.activity.Log(r.Context(), logParams)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleResetCDPTarget(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	if s.agentClient != nil && machine.HostID != nil {
		host, err := s.store.GetHost(r.Context(), *machine.HostID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to resolve host")
			return
		}
		if err := s.agentClient.ResetCDPTarget(r.Context(), host, machine.ID); err != nil {
			slog.Error("machine.cdp_target.reset.failed", "machine_id", machine.ID, "error", err)
			writeError(w, http.StatusBadGateway, "failed to reset CDP target on agent")
			return
		}
	}

	claims := auth.UserFromContext(r.Context())
	logParams := events.LogParams{
		Category:  "machine",
		Action:    "machine.cdp_target_reset",
		Status:    "success",
		ActorType: "user",
		AccountID: &accountID,
		MachineID: &id,
		Summary:   fmt.Sprintf("CDP target reset on '%s'", machine.Name),
	}
	if claims != nil {
		logParams.ActorID = &claims.UserID
	}
	s.activity.Log(r.Context(), logParams)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleGetMachineToken(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	claims := auth.UserFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	if machine.Status != "running" {
		writeError(w, http.StatusConflict, "machine is not running")
		return
	}

	if machine.SigningKey == nil || *machine.SigningKey == "" {
		writeError(w, http.StatusServiceUnavailable, "machine signing key not available")
		return
	}

	token, expiresAt, err := auth.IssueMachineToken(claims.UserID, claims.Email, machine.ID, accountID, []string{"all"}, *machine.SigningKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}

	hostname := "m-" + machine.Slug + ".openclawmachines.com"

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token":      token,
		"expires_at": expiresAt.Format(time.RFC3339),
		"hostname":   hostname,
	})
}

func (s *Server) handleRollbackMachine(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	// Stop VM if running
	if machine.Status == "running" && s.agentClient != nil && machine.HostID != nil {
		host, err := s.store.GetHost(r.Context(), *machine.HostID)
		if err == nil {
			_ = s.agentClient.StopVM(r.Context(), host, machine.ID)
		}
		_ = s.placement.Release(r.Context(), machine.ID, fleet.ReleaseSoft)
		_ = s.machines.CleanupMachineRouteAndTunnel(r.Context(), machine.ID)
	}

	// Call agent to rollback data volume
	if s.agentClient != nil && machine.HostID != nil {
		host, err := s.store.GetHost(r.Context(), *machine.HostID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to get host")
			return
		}
		if err := s.agentClient.RollbackVM(r.Context(), host, machine.ID); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
	} else {
		writeError(w, http.StatusBadRequest, "machine has no host assignment")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "rolled_back"})
}
