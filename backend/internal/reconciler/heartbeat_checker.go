package reconciler

import "context"

// HeartbeatOnlyChecker is an InstanceChecker that always reports the instance
// as existing. For non-GCP hosts (OVH, Hetzner, customer-owned), there is no
// provider API to check instance existence, so we rely on heartbeat staleness
// alone to detect dead hosts.
type HeartbeatOnlyChecker struct{}

// InstanceExists always returns true — heartbeat staleness handles detection.
func (c *HeartbeatOnlyChecker) InstanceExists(_ context.Context, _, _, _ string) (bool, error) {
	return true, nil
}
