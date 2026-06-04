package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// ---- User-facing Integration Handlers ----

type integrationResponse struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Icon               string  `json:"icon"`
	Category           string  `json:"category"`
	Connected          bool    `json:"connected"`
	ConnectedAccountID *string `json:"connected_account_id,omitempty"`
	ConnectedAt        *string `json:"connected_at,omitempty"`
}

func (s *Server) handleListIntegrations(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil || machine.AccountID != accountID {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	catalog, err := s.store.ListConfiguredIntegrations(r.Context())
	if err != nil {
		slog.Error("integrations.list_catalog_failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list integrations")
		return
	}

	// Build response from catalog
	result := make([]integrationResponse, 0, len(catalog))
	for _, entry := range catalog {
		result = append(result, integrationResponse{
			ID:       entry.ID,
			Name:     entry.Name,
			Icon:     entry.Icon,
			Category: entry.Category,
		})
	}

	// If composio client is configured, fetch live connection status
	if s.composioClient != nil && s.composioClient.Enabled() {
		conns, err := s.composioClient.ListConnections(r.Context(), machineID)
		if err != nil {
			slog.Warn("integrations.composio_list_failed", "machine_id", machineID, "error", err)
			// Return catalog without connection status rather than failing
		} else {
			// Build toolkit -> most recent active connection map
			connByToolkit := map[string]struct {
				id        string
				createdAt string
			}{}
			for _, conn := range conns {
				if conn.Status != "active" {
					continue
				}
				existing, ok := connByToolkit[conn.Toolkit]
				if !ok || conn.CreatedAt > existing.createdAt {
					connByToolkit[conn.Toolkit] = struct {
						id        string
						createdAt string
					}{conn.ID, conn.CreatedAt}
				}
			}

			// Match connections to catalog entries
			for i := range result {
				entry := catalog[i]
				if conn, ok := connByToolkit[entry.Toolkit]; ok {
					result[i].Connected = true
					result[i].ConnectedAccountID = &conn.id
					result[i].ConnectedAt = &conn.createdAt

					// Log connect_completed if not already logged
					hasEvent, _ := s.store.HasIntegrationEvent(r.Context(), machineID, entry.ID, "connect_completed")
					if !hasEvent {
						meta, _ := json.Marshal(map[string]string{"connected_account_id": conn.id})
						if err := s.store.LogIntegrationEvent(r.Context(), accountID, machineID, entry.ID, "connect_completed", meta); err != nil {
							slog.Error("integrations.log_connect_completed_failed", "machine_id", machineID, "integration", entry.ID, "error", err)
						} else {
							slog.Info("integrations.connect_completed", "machine_id", machineID, "integration", entry.ID, "connected_account_id", conn.id)
						}
					}
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCreateConnectLink(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	integrationID := chi.URLParam(r, "integration")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil || machine.AccountID != accountID {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	if s.composioClient == nil || !s.composioClient.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "Composio integration not configured")
		return
	}

	entry, err := s.store.GetIntegrationCatalogEntry(r.Context(), integrationID)
	if err != nil {
		writeError(w, http.StatusNotFound, "integration not found")
		return
	}
	if !entry.Enabled {
		writeError(w, http.StatusBadRequest, "integration is disabled")
		return
	}
	if entry.AuthConfigID == nil || *entry.AuthConfigID == "" {
		writeError(w, http.StatusBadRequest, "integration not yet configured (missing auth_config_id)")
		return
	}

	// Log connect_started event
	meta, _ := json.Marshal(map[string]string{"machine_slug": machine.Slug})
	if err := s.store.LogIntegrationEvent(r.Context(), accountID, machineID, integrationID, "connect_started", meta); err != nil {
		slog.Error("integrations.log_connect_started_failed", "machine_id", machineID, "integration", integrationID, "error", err)
	}
	slog.Info("integrations.connect_started", "machine_id", machineID, "integration", integrationID)

	callbackURL := s.publicURL() + "/api/integrations/callback"
	resp, err := s.composioClient.CreateConnectLink(r.Context(), machineID, *entry.AuthConfigID, callbackURL)
	if err != nil {
		slog.Error("integrations.create_connect_link_failed", "machine_id", machineID, "integration", integrationID, "error", err)
		writeError(w, http.StatusBadGateway, "failed to create connect link")
		return
	}

	slog.Info("integrations.connect_link_created", "machine_id", machineID, "integration", integrationID)
	writeJSON(w, http.StatusOK, map[string]string{"url": resp.URL})
}

func (s *Server) handleDeleteIntegration(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	connID := chi.URLParam(r, "connId")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil || machine.AccountID != accountID {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	if s.composioClient == nil || !s.composioClient.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "Composio integration not configured")
		return
	}

	// Ownership check: verify connID belongs to this machine
	conns, err := s.composioClient.ListConnections(r.Context(), machineID)
	if err != nil {
		slog.Error("integrations.verify_ownership_failed", "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to verify connection ownership")
		return
	}
	found := false
	toolkit := ""
	for _, conn := range conns {
		if conn.ID == connID {
			found = true
			toolkit = conn.Toolkit
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "connection not found for this machine")
		return
	}

	// Log disconnected event -- find integration ID from toolkit name
	integrationID := ""
	catalog, _ := s.store.ListConfiguredIntegrations(r.Context())
	for _, entry := range catalog {
		if entry.Toolkit == toolkit {
			integrationID = entry.ID
			break
		}
	}
	meta, _ := json.Marshal(map[string]string{"connected_account_id": connID, "toolkit": toolkit, "machine_slug": machine.Slug})
	if err := s.store.LogIntegrationEvent(r.Context(), accountID, machineID, integrationID, "disconnected", meta); err != nil {
		slog.Error("integrations.log_disconnected_failed", "machine_id", machineID, "integration", integrationID, "error", err)
	}

	if err := s.composioClient.DeleteConnection(r.Context(), connID); err != nil {
		slog.Error("integrations.delete_failed", "machine_id", machineID, "conn_id", connID, "error", err)
		writeError(w, http.StatusBadGateway, "failed to disconnect")
		return
	}

	slog.Info("integrations.disconnected", "machine_id", machineID, "integration", integrationID, "conn_id", connID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleIntegrationCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>Connected</title></head>
<body style="font-family:system-ui;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#111;color:#eee">
<div style="text-align:center">
<h2>Connection successful</h2>
<p>You can close this window.</p>
</div>
<script>
try { window.opener.postMessage({type:"composio-connected"}, "*"); } catch(e) {}
setTimeout(function(){ window.close(); }, 1500);
</script>
</body></html>`)
}

// publicURL returns the backend's public URL (e.g. https://ocm-backend-xxx.run.app).
// This must point to the API server, not the frontend, since the callback route is served here.
func (s *Server) publicURL() string {
	if s.backendURL != "" {
		return s.backendURL
	}
	return "http://localhost:8080"
}

// ---- Admin Integration Catalog Handlers ----

func (s *Server) handleListIntegrationCatalog(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.ListIntegrationCatalog(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list integration catalog")
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleCreateIntegrationCatalogEntry(w http.ResponseWriter, r *http.Request) {
	var entry store.IntegrationCatalogEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if entry.ID == "" || entry.Name == "" || entry.Icon == "" || entry.Toolkit == "" {
		writeError(w, http.StatusBadRequest, "id, name, icon, and toolkit are required")
		return
	}
	if err := s.store.CreateIntegrationCatalogEntry(r.Context(), entry); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create integration")
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (s *Server) handleUpdateIntegrationCatalogEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "integrationId")
	var entry store.IntegrationCatalogEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	entry.ID = id
	if err := s.store.UpdateIntegrationCatalogEntry(r.Context(), entry); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update integration")
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) handleDeleteIntegrationCatalogEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "integrationId")
	if err := s.store.DeleteIntegrationCatalogEntry(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete integration")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
