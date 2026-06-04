package api

import (
	"net/http"
	"sort"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// OpenClawReleaseSummary is the wire shape returned to the admin UI.
// We deliberately omit SHA256, URL, and Metadata — the dropdown only
// needs version, channel, and a sort key.
type OpenClawReleaseSummary struct {
	Version          string  `json:"version"`
	Channel          string  `json:"channel"`
	CreatedAt        string  `json:"created_at"`
	MinRootfsVersion *string `json:"min_rootfs_version,omitempty"`
}

// handleAdminListOpenClawReleases lists openclaw releases across stable + rc
// channels for the superadmin "pin version" UI. Returns up to 20 of each
// channel, merged and sorted by created_at DESC.
//
// Mounted under /api/admin (requireSuperuser middleware), so this only
// reaches superadmins. End users can't enumerate non-stable versions.
func (s *Server) handleAdminListOpenClawReleases(w http.ResponseWriter, r *http.Request) {
	const perChannelLimit = 20
	channels := []string{"stable", "rc"}

	var all []store.ArtifactRelease
	for _, ch := range channels {
		rows, err := s.store.ListArtifactReleases(r.Context(), "openclaw", ch, perChannelLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list "+ch+" releases: "+err.Error())
			return
		}
		all = append(all, rows...)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	out := make([]OpenClawReleaseSummary, 0, len(all))
	for _, r := range all {
		out = append(out, OpenClawReleaseSummary{
			Version:          r.Version,
			Channel:          r.Channel,
			CreatedAt:        r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			MinRootfsVersion: r.MinRootfsVersion,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"releases": out})
}
