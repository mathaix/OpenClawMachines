package api

import (
	"net/http"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// requireMutableMachineConfig allows edits before first boot, but after first
// boot the machine must be actively running so config changes apply to the live
// guest instead of drifting from the persisted data volume state.
func requireMutableMachineConfig(w http.ResponseWriter, machine *store.Machine) bool {
	if machine == nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return false
	}
	if machine.ProvisioningCompletedAt == nil {
		return true
	}
	if machine.Status == "running" {
		return true
	}
	writeError(w, http.StatusConflict, "machine must be running to change config after first boot")
	return false
}
