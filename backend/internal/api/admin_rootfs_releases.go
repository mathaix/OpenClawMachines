package api

import (
	"net/http"
	"sort"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type RootfsReleaseSummary struct {
	Version   string `json:"version"`
	Channel   string `json:"channel"`
	CreatedAt string `json:"created_at"`
}

// handleAdminListRootfsReleases mirrors handleAdminListOpenClawReleases for
// rootfs. Up to 20 stable + 20 rc, merged and sorted by created_at DESC.
// Mounted under /api/admin so requireSuperuser middleware gates it.
func (s *Server) handleAdminListRootfsReleases(w http.ResponseWriter, r *http.Request) {
	const perChannelLimit = 20
	channels := []string{"stable", "rc"}

	var all []store.ArtifactRelease
	for _, ch := range channels {
		rows, err := s.store.ListArtifactReleases(r.Context(), "rootfs", ch, perChannelLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list "+ch+" releases: "+err.Error())
			return
		}
		all = append(all, rows...)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	out := make([]RootfsReleaseSummary, 0, len(all))
	for _, r := range all {
		out = append(out, RootfsReleaseSummary{
			Version:   r.Version,
			Channel:   r.Channel,
			CreatedAt: r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"releases": out})
}
