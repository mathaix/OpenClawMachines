package api

import "net/http"

// MachineSize defines a machine size tier with resource allocations.
type MachineSize struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	VCPUs       int    `json:"vcpus"`
	MemoryMB    int    `json:"memory_mb"`
	SwapMB      int    `json:"swap_mb"`
	DiskGB      int    `json:"disk_gb"`
	Description string `json:"description"`
	IsDefault   bool   `json:"is_default,omitempty"`
}

// MachineSizes is the canonical list of available machine sizes.
var MachineSizes = []MachineSize{
	{ID: "basic", Label: "Basic", VCPUs: 1, MemoryMB: 3072, SwapMB: 1024, DiskGB: 10, Description: "1 vCPU, 3 GB RAM, 10 GB disk"},
	{ID: "standard", Label: "Standard", VCPUs: 2, MemoryMB: 6144, SwapMB: 1024, DiskGB: 20, Description: "2 vCPUs, 6 GB RAM, 20 GB disk", IsDefault: true},
	{ID: "pro", Label: "Pro", VCPUs: 4, MemoryMB: 12288, SwapMB: 1024, DiskGB: 50, Description: "4 vCPUs, 12 GB RAM, 50 GB disk"},
}

// LookupSize returns the MachineSize for a given size ID, or nil if not found.
func LookupSize(id string) *MachineSize {
	for i := range MachineSizes {
		if MachineSizes[i].ID == id {
			return &MachineSizes[i]
		}
	}
	return nil
}

// DefaultSize returns the default machine size.
func DefaultSize() *MachineSize {
	for i := range MachineSizes {
		if MachineSizes[i].IsDefault {
			return &MachineSizes[i]
		}
	}
	return &MachineSizes[0]
}

func (s *Server) handleListSizes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, MachineSizes)
}
