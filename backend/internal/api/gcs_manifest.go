package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/mathaix/openclawmachines/backend/internal/config"
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

	client, err := storage.NewClient(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create GCS client: "+err.Error())
		return
	}
	defer func() { _ = client.Close() }()

	result := &LatestVersionsResponse{}

	// Fetch agent manifest
	agentManifest, err := selfupdate.FetchManifest(ctx, client, "gs://openclawmachines/agent/manifest.json")
	if err != nil {
		slog.Warn("latest_versions.agent_manifest_failed", "error", err)
	} else {
		result.Agent = &ManifestInfo{
			Version: agentManifest.Version,
			BuiltAt: agentManifest.BuiltAt.Format(time.RFC3339),
		}
	}

	// Fetch rootfs manifest
	rootfsManifest, err := selfupdate.FetchManifest(ctx, client, "gs://openclawmachines/rootfs/manifest.json")
	if err != nil {
		slog.Warn("latest_versions.rootfs_manifest_failed", "error", err)
	} else {
		result.Rootfs = &ManifestInfo{
			Version:         rootfsManifest.Version,
			BuiltAt:         rootfsManifest.BuiltAt.Format(time.RFC3339),
			OpenClawVersion: rootfsManifest.OpenClawVersion,
		}
	}

	result.BrowserRootfs = &ManifestInfo{
		Version:     config.StableKernelBrowserRootfsVersion,
		Lineage:     "kernel-images",
		Stability:   "stable",
		ManifestURI: config.ExperimentalKernelBrowserManifestURI,
	}

	kernelBrowserManifest, err := selfupdate.FetchManifest(ctx, client, config.ExperimentalKernelBrowserManifestURI)
	if err != nil {
		slog.Debug("latest_versions.experimental_browser_rootfs_manifest_failed", "error", err)
	} else {
		result.ExperimentalBrowserRootfs = &ManifestInfo{
			Version:     kernelBrowserManifest.Version,
			BuiltAt:     kernelBrowserManifest.BuiltAt.Format(time.RFC3339),
			Lineage:     "kernel-images",
			Stability:   "experimental",
			ManifestURI: config.ExperimentalKernelBrowserManifestURI,
		}
	}

	versionsCache.result = result
	versionsCache.fetchedAt = time.Now()

	writeJSON(w, http.StatusOK, result)
}
