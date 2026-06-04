package api

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

const placementHeartbeatFreshness = 180 * time.Second

// RegionInfo is the public API response for a placement region.
type RegionInfo struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

// regionCapacity is internal bookkeeping used to compute RegionInfo.
type regionCapacity struct {
	code          string
	name          string
	availVCPUs    int
	availMemoryMB int
}

func (s *Server) handleListRegions(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.store.ListHosts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list hosts")
		return
	}

	now := time.Now()
	byRegion := make(map[string]*regionCapacity)
	for _, h := range hosts {
		region := strings.TrimSpace(strings.ToLower(h.Region))
		if region == "" {
			continue
		}
		if h.Status != "ready" {
			continue
		}
		if h.LastHeartbeat == nil || now.Sub(*h.LastHeartbeat) > placementHeartbeatFreshness {
			continue
		}

		entry, ok := byRegion[region]
		if !ok {
			entry = &regionCapacity{code: region, name: regionDisplayName(region)}
			byRegion[region] = entry
		}

		availVCPU := h.CapacityVCPUs - h.UsedVCPUs
		if availVCPU < 0 {
			availVCPU = 0
		}
		availMem := h.CapacityMemoryMB - h.UsedMemoryMB
		if availMem < 0 {
			availMem = 0
		}
		entry.availVCPUs += availVCPU
		entry.availMemoryMB += availMem
	}

	regions := make([]RegionInfo, 0, len(byRegion))
	for _, rc := range byRegion {
		regions = append(regions, RegionInfo{
			Code:      rc.code,
			Name:      rc.name,
			Available: rc.availVCPUs > 0,
		})
	}
	sort.Slice(regions, func(i, j int) bool {
		// Available regions first, then alphabetical.
		if regions[i].Available != regions[j].Available {
			return regions[i].Available
		}
		return regions[i].Code < regions[j].Code
	})

	writeJSON(w, http.StatusOK, regions)
}

func regionDisplayName(code string) string {
	switch code {
	case "us-west", "us-west1":
		return "US West"
	case "us-central", "us-central1":
		return "US Central"
	case "us-east", "us-east1":
		return "US East"
	case "eu-west", "europe-west1":
		return "EU West"
	case "eu-central", "europe-west4":
		return "EU Central"
	case "eu-north":
		return "EU North"
	case "asia-east", "asia-east1":
		return "Asia East"
	case "asia-south":
		return "Asia South"
	case "asia-southeast", "asia-southeast1":
		return "Asia Southeast"
	case "external":
		return "External"
	default:
		// Fallback: title-case each segment.
		parts := strings.Split(code, "-")
		for i := range parts {
			if parts[i] == "" {
				continue
			}
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
		return strings.Join(parts, " ")
	}
}
