package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/mathaix/openclawmachines/backend/internal/selfupdate"
)

type latestVersionsCache struct {
	mu        sync.Mutex
	result    *LatestVersionsResponse
	fetchedAt time.Time
	ttl       time.Duration
}

type LatestVersionsResponse struct {
	Agent                     *ManifestInfo `json:"agent"`
	Rootfs                    *ManifestInfo `json:"rootfs"`
	BrowserRootfs             *ManifestInfo `json:"browser_rootfs,omitempty"`
	ExperimentalBrowserRootfs *ManifestInfo `json:"experimental_browser_rootfs,omitempty"`
}

type ManifestInfo struct {
	Version         string `json:"version"`
	BuiltAt         string `json:"built_at,omitempty"`
	OpenClawVersion string `json:"openclaw_version,omitempty"`
	Lineage         string `json:"lineage,omitempty"`
	Stability       string `json:"stability,omitempty"`
	ManifestURI     string `json:"manifest_uri,omitempty"`
}

var versionsCache = &latestVersionsCache{ttl: 60 * time.Second}

func (s *Server) handleLatestVersions(w http.ResponseWriter, r *http.Request) {
	versionsCache.mu.Lock()
	defer versionsCache.mu.Unlock()

	if versionsCache.result != nil && time.Since(versionsCache.fetchedAt) < versionsCache.ttl {
		writeJSON(w, http.StatusOK, versionsCache.result)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	result := &LatestVersionsResponse{}
	if s.agentGCSManifest == "" && s.rootfsGCSManifest == "" && s.browserRootfsGCSManifest == "" {
		versionsCache.result = result
		versionsCache.fetchedAt = time.Now()
		writeJSON(w, http.StatusOK, result)
		return
	}

	client, err := storage.NewClient(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create GCS client: "+err.Error())
		return
	}
	defer func() { _ = client.Close() }()

	// Fetch agent manifest
	if s.agentGCSManifest != "" {
		agentManifest, err := selfupdate.FetchManifest(ctx, client, s.agentGCSManifest)
		if err != nil {
			slog.Warn("latest_versions.agent_manifest_failed", "error", err)
		} else {
			result.Agent = &ManifestInfo{
				Version:     agentManifest.Version,
				BuiltAt:     agentManifest.BuiltAt.Format(time.RFC3339),
				ManifestURI: s.agentGCSManifest,
			}
		}
	}

	// Fetch rootfs manifest
	if s.rootfsGCSManifest != "" {
		rootfsManifest, err := selfupdate.FetchManifest(ctx, client, s.rootfsGCSManifest)
		if err != nil {
			slog.Warn("latest_versions.rootfs_manifest_failed", "error", err)
		} else {
			result.Rootfs = &ManifestInfo{
				Version:         rootfsManifest.Version,
				BuiltAt:         rootfsManifest.BuiltAt.Format(time.RFC3339),
				OpenClawVersion: rootfsManifest.OpenClawVersion,
				ManifestURI:     s.rootfsGCSManifest,
			}
		}
	}

	if s.browserRootfsGCSManifest != "" {
		browserManifest, err := selfupdate.FetchManifest(ctx, client, s.browserRootfsGCSManifest)
		if err != nil {
			slog.Debug("latest_versions.browser_rootfs_manifest_failed", "error", err)
			result.BrowserRootfs = &ManifestInfo{
				Version:     s.browserRootfsVersion,
				Lineage:     "operator",
				Stability:   "configured",
				ManifestURI: s.browserRootfsGCSManifest,
			}
		} else {
			result.BrowserRootfs = &ManifestInfo{
				Version:     browserManifest.Version,
				BuiltAt:     browserManifest.BuiltAt.Format(time.RFC3339),
				Lineage:     "operator",
				Stability:   "configured",
				ManifestURI: s.browserRootfsGCSManifest,
			}
		}
	}

	versionsCache.result = result
	versionsCache.fetchedAt = time.Now()

	writeJSON(w, http.StatusOK, result)
}
