package api

import (
	"log/slog"
	"net/http"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// handleListProviderCatalog returns provider catalog entries, optionally filtered by category.
// GET /api/catalog/providers
// GET /api/catalog/providers?category=search
func (s *Server) handleListProviderCatalog(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")

	var entries []store.ProviderCatalogEntry
	var err error
	if category != "" {
		entries, err = s.store.ListProviderCatalogByCategory(r.Context(), category)
	} else {
		entries, err = s.store.ListProviderCatalog(r.Context())
	}
	if err != nil {
		slog.Error("provider_catalog.list_failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	slog.Debug("provider_catalog.listed", "category", category, "count", len(entries))
	writeJSON(w, http.StatusOK, entries)
}
