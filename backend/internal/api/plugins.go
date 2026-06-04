package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// ---- Admin Plugin Catalog Handlers ----

func (s *Server) handleListPluginCatalog(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.ListPluginCatalog(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list plugins")
		return
	}
	if entries == nil {
		entries = []store.PluginCatalogEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleCreatePluginCatalogEntry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID             string          `json:"id"`
		Name           string          `json:"name"`
		Description    *string         `json:"description,omitempty"`
		Slot           string          `json:"slot"`
		Version        string          `json:"version"`
		InstallKind    string          `json:"install_kind"`
		ArtifactURL    *string         `json:"artifact_url,omitempty"`
		ArtifactSHA256 *string         `json:"artifact_sha256,omitempty"`
		ConfigTemplate json.RawMessage `json:"config_template,omitempty"`
		Status         string          `json:"status"`
		SortOrder      int             `json:"sort_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ID == "" || req.Name == "" || req.Slot == "" {
		writeError(w, http.StatusBadRequest, "id, name, and slot are required")
		return
	}
	if req.InstallKind == "" {
		req.InstallKind = "bundled"
	}
	if req.Status == "" {
		req.Status = "active"
	}
	if req.Version == "" {
		req.Version = "1"
	}

	entry := &store.PluginCatalogEntry{
		ID:             req.ID,
		Name:           req.Name,
		Description:    req.Description,
		Slot:           req.Slot,
		Version:        req.Version,
		InstallKind:    req.InstallKind,
		ArtifactURL:    req.ArtifactURL,
		ArtifactSHA256: req.ArtifactSHA256,
		ConfigTemplate: req.ConfigTemplate,
		Status:         req.Status,
		SortOrder:      req.SortOrder,
	}

	if err := s.store.CreatePluginCatalogEntry(r.Context(), entry); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "plugin with this ID already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create plugin")
		return
	}

	slog.Info("plugin.catalog.created", "id", entry.ID, "slot", entry.Slot)
	writeJSON(w, http.StatusCreated, entry)
}

func (s *Server) handleUpdatePluginCatalogEntry(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "pluginId")

	existing, err := s.store.GetPluginCatalogEntry(r.Context(), pluginID)
	if err != nil {
		if errors.Is(err, store.ErrPluginNotFound) {
			writeError(w, http.StatusNotFound, "plugin not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to look up plugin")
		return
	}

	var req struct {
		Name           *string         `json:"name,omitempty"`
		Description    *string         `json:"description,omitempty"`
		Slot           *string         `json:"slot,omitempty"`
		Version        *string         `json:"version,omitempty"`
		InstallKind    *string         `json:"install_kind,omitempty"`
		ArtifactURL    *string         `json:"artifact_url,omitempty"`
		ArtifactSHA256 *string         `json:"artifact_sha256,omitempty"`
		ConfigTemplate json.RawMessage `json:"config_template,omitempty"`
		Status         *string         `json:"status,omitempty"`
		SortOrder      *int            `json:"sort_order,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.Description != nil {
		existing.Description = req.Description
	}
	if req.Slot != nil {
		existing.Slot = *req.Slot
	}
	if req.Version != nil {
		existing.Version = *req.Version
	}
	if req.InstallKind != nil {
		existing.InstallKind = *req.InstallKind
	}
	if req.ArtifactURL != nil {
		existing.ArtifactURL = req.ArtifactURL
	}
	if req.ArtifactSHA256 != nil {
		existing.ArtifactSHA256 = req.ArtifactSHA256
	}
	if req.ConfigTemplate != nil {
		existing.ConfigTemplate = req.ConfigTemplate
	}
	if req.Status != nil {
		existing.Status = *req.Status
	}
	if req.SortOrder != nil {
		existing.SortOrder = *req.SortOrder
	}

	if err := s.store.UpdatePluginCatalogEntry(r.Context(), existing); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update plugin")
		return
	}

	slog.Info("plugin.catalog.updated", "id", pluginID)
	writeJSON(w, http.StatusOK, existing)
}

func (s *Server) handleDeletePluginCatalogEntry(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "pluginId")

	if err := s.store.DeletePluginCatalogEntry(r.Context(), pluginID); err != nil {
		if errors.Is(err, store.ErrPluginNotFound) {
			writeError(w, http.StatusNotFound, "plugin not found")
			return
		}
		if errors.Is(err, store.ErrPluginStillEnabled) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete plugin")
		return
	}

	slog.Info("plugin.catalog.deleted", "id", pluginID)
	w.WriteHeader(http.StatusNoContent)
}

// ---- Per-Machine Plugin Handlers ----

func (s *Server) handleListMachinePlugins(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}
	plugins, err := s.store.ListMachinePlugins(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list plugins")
		return
	}
	if plugins == nil {
		plugins = []store.MachinePlugin{}
	}
	writeJSON(w, http.StatusOK, plugins)
}

func (s *Server) handleEnableMachinePlugin(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}
	if !requireMutableMachineConfig(w, machine) {
		return
	}

	var req struct {
		PluginID        string          `json:"plugin_id"`
		ConfigOverrides json.RawMessage `json:"config_overrides,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.PluginID == "" {
		writeError(w, http.StatusBadRequest, "plugin_id is required")
		return
	}

	if err := s.store.EnableMachinePlugin(r.Context(), machineID, req.PluginID, req.ConfigOverrides); err != nil {
		slog.Error("machine.plugin.enable.failed", "machine_id", machineID, "plugin_id", req.PluginID, "error", err)
		if errors.Is(err, store.ErrPluginNotFound) {
			writeError(w, http.StatusNotFound, "plugin not found or inactive")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	slog.Info("machine.plugin.enabled", "machine_id", machineID, "plugin_id", req.PluginID)

	// Trigger reconciliation synchronously so the response reflects install result
	reconcileErr := s.reconcileMachinePlugins(machine)

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.plugin_enabled",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Enabled plugin '%s' on '%s'", req.PluginID, machine.Name),
		Detail:    map[string]any{"plugin_id": req.PluginID},
	})

	resp := map[string]string{"status": "ok"}
	if reconcileErr != nil {
		resp["reconcile_warning"] = reconcileErr.Error()
	}
	go s.pushMachineConfigAsync(machineID)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleUpdateMachinePluginOverrides(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	pluginID := chi.URLParam(r, "pluginId")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}
	if !requireMutableMachineConfig(w, machine) {
		return
	}

	var req struct {
		ConfigOverrides json.RawMessage `json:"config_overrides"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.store.UpdateMachinePluginOverrides(r.Context(), machineID, pluginID, req.ConfigOverrides); err != nil {
		if errors.Is(err, store.ErrMachinePluginNotFound) {
			writeError(w, http.StatusNotFound, "plugin not found or disabled")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update plugin overrides")
		return
	}

	slog.Info("machine.plugin.overrides_updated", "machine_id", machineID, "plugin_id", pluginID)
	go s.pushMachineConfigAsync(machineID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDisableMachinePlugin(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	pluginID := chi.URLParam(r, "pluginId")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}
	if !requireMutableMachineConfig(w, machine) {
		return
	}

	if err := s.store.DisableMachinePlugin(r.Context(), machineID, pluginID); err != nil {
		if errors.Is(err, store.ErrMachinePluginNotFound) {
			writeError(w, http.StatusNotFound, "plugin not found or already disabled")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to disable plugin")
		return
	}

	slog.Info("machine.plugin.disabled", "machine_id", machineID, "plugin_id", pluginID)

	// Trigger reconciliation synchronously so the response reflects uninstall result
	if err := s.reconcileMachinePlugins(machine); err != nil {
		slog.Warn("machine.plugin.reconcile_after_disable", "machine_id", machineID, "plugin_id", pluginID, "error", err)
	}

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.plugin_disabled",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Disabled plugin '%s' on '%s'", pluginID, machine.Name),
		Detail:    map[string]any{"plugin_id": pluginID},
	})

	go s.pushMachineConfigAsync(machineID)
	w.WriteHeader(http.StatusNoContent)
}

// reconcileMachinePlugins triggers plugin install/uninstall on a running VM.
// Returns nil if machine isn't running or no reconciliation needed.
// Returns an error if reconciliation was attempted but failed.
func (s *Server) reconcileMachinePlugins(machine *store.Machine) error {
	if machine.Status != "running" || machine.HostID == nil || s.agentClient == nil {
		return nil // not running — plugin will reconcile on next start
	}

	ctx := context.Background()

	plugins, err := s.store.ListMachinePluginsWithCatalog(ctx, machine.ID)
	if err != nil {
		slog.Error("reconcile.plugins.list_failed", "machine_id", machine.ID, "error", err)
		return fmt.Errorf("failed to list plugins: %w", err)
	}

	// Only reconcile if there are non-bundled plugins that need action
	needsReconcile := false
	for _, p := range plugins {
		if p.InstallKind != "bundled" && ((p.Enabled && p.InstallStatus == "pending") || (!p.Enabled && p.InstallStatus == "installed")) {
			needsReconcile = true
			break
		}
	}
	if !needsReconcile {
		return nil
	}

	host, err := s.store.GetHost(ctx, *machine.HostID)
	if err != nil {
		slog.Warn("reconcile.plugins.host_not_found", "machine_id", machine.ID, "host_id", *machine.HostID, "error", err)
		return fmt.Errorf("host not found: %w", err)
	}

	results, err := s.agentClient.ReconcilePlugins(ctx, host, machine.ID, plugins)
	if err != nil {
		slog.Warn("reconcile.plugins.agent_call_failed", "machine_id", machine.ID, "error", err)
		return fmt.Errorf("agent reconciliation failed: %w", err)
	}

	// Update DB with results — collect failures
	var failedPlugins []string
	for _, r := range results {
		if err := s.store.UpdateMachinePluginStatus(ctx, machine.ID, r.PluginID, r.Status, r.Version); err != nil {
			slog.Error("reconcile.plugins.status_update_failed", "machine_id", machine.ID, "plugin_id", r.PluginID, "error", err)
		}
		if r.Status == "failed" {
			failedPlugins = append(failedPlugins, r.PluginID+": "+r.Error)
		}
	}

	slog.Info("reconcile.plugins.complete", "machine_id", machine.ID, "results", len(results), "failed", len(failedPlugins))

	if len(failedPlugins) > 0 {
		return fmt.Errorf("plugin install failed: %s", strings.Join(failedPlugins, "; "))
	}
	return nil
}
