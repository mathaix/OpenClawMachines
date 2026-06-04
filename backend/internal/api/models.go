package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/configassembly"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type modelResponse struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	Description string  `json:"description"`
	Source      string  `json:"source"`
	Tier        *string `json:"tier,omitempty"`
	SortOrder   int     `json:"sort_order"`
	Provider    string  `json:"provider"`
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	kind := store.NormalizeMachineKind(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind"))))
	if !store.ValidMachineKind(kind) {
		writeError(w, http.StatusBadRequest, "kind must be one of: openclaw, hermes")
		return
	}

	models, err := s.store.ListModelCatalog(r.Context())
	if err != nil {
		slog.Error("models.list.failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list models")
		return
	}

	resp := make([]modelResponse, len(models))
	for i, m := range models {
		resp[i] = modelResponse{
			ID:          m.ID,
			Label:       m.Label,
			Description: m.Description,
			Source:      m.Source,
			Tier:        m.Tier,
			SortOrder:   m.SortOrder,
			Provider:    m.Provider,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleListMachineModels returns models available for a specific machine,
// using the same filtering logic as config assembly (AvailableCatalogIDs).
func (s *Server) handleListMachineModels(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")

	models, err := s.store.ListModelCatalog(r.Context())
	if err != nil {
		slog.Error("models.machine_list.failed", "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list models")
		return
	}

	// Get credentials for this machine to determine which providers are configured
	creds, err := s.store.ListMachineCredentials(r.Context(), machineID)
	if err != nil {
		slog.Error("models.list_credentials.failed", "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list credentials")
		return
	}
	providers := make(map[string]bool, len(creds))
	for _, c := range creds {
		providers[c.Provider] = true
	}

	// Build catalog models for the shared filter
	catalog := make([]configassembly.CatalogModel, len(models))
	for i, m := range models {
		catalog[i] = configassembly.CatalogModel{
			ID:       m.ID,
			Source:   m.Source,
			Provider: m.Provider,
		}
	}

	available := configassembly.AvailableCatalogIDs(catalog, providers)

	var resp []modelResponse
	for _, m := range models {
		if !available[m.ID] {
			continue
		}
		resp = append(resp, modelResponse{
			ID:          m.ID,
			Label:       m.Label,
			Description: m.Description,
			Source:      m.Source,
			Tier:        m.Tier,
			SortOrder:   m.SortOrder,
			Provider:    m.Provider,
		})
	}
	if resp == nil {
		resp = []modelResponse{}
	}
	writeJSON(w, http.StatusOK, resp)
}
