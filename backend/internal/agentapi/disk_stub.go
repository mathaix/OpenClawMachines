//go:build !linux

package agentapi

// diskUsageMB is a stub for non-Linux platforms.
func diskUsageMB(path string) (totalMB, usedMB int64) {
	return 0, 0
}
