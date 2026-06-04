package fleet

import (
	"math"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// EffectiveCapacity computes allocatable resources from a host and its policy.
func EffectiveCapacity(host *store.Host, policy *store.CapacityPolicy) (vcpus, memMB int) {
	vcpus = int(math.Floor(float64(host.CapacityVCPUs-policy.ReserveVCPUs) * policy.CPUOvercommitRatio))
	memMB = int(math.Floor(float64(host.CapacityMemoryMB-policy.ReserveMemoryMB) * policy.MemoryOvercommitRatio))
	if vcpus < 0 {
		vcpus = 0
	}
	if memMB < 0 {
		memMB = 0
	}
	return
}
