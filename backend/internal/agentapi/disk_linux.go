//go:build linux

package agentapi

import "syscall"

// diskUsageMB returns total and used disk space in MB for the given path.
func diskUsageMB(path string) (totalMB, usedMB int64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0
	}
	blockSize := int64(stat.Bsize)
	totalMB = int64(stat.Blocks) * blockSize / (1024 * 1024)
	freeMB := int64(stat.Bavail) * blockSize / (1024 * 1024)
	usedMB = totalMB - freeMB
	return totalMB, usedMB
}
