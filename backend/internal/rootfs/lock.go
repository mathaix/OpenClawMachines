package rootfs

import (
	"fmt"
	"os"
	"syscall"
)

// RootfsLock provides shared/exclusive file locking for rootfs access.
// VM creation acquires a shared lock (multiple can coexist).
// Rootfs refresh acquires an exclusive lock (blocks until all shared locks release).
type RootfsLock struct {
	path string
}

// NewRootfsLock creates a RootfsLock that uses the given file path as the lock file.
func NewRootfsLock(path string) *RootfsLock {
	return &RootfsLock{path: path}
}

// SharedLock acquires a shared (read) lock. Multiple shared locks can coexist.
// Returns an unlock function that must be called when the lock is no longer needed.
func (l *RootfsLock) SharedLock() (unlock func(), err error) {
	return l.lock(syscall.LOCK_SH)
}

// ExclusiveLock acquires an exclusive (write) lock. Blocks until all shared
// and exclusive locks are released.
// Returns an unlock function that must be called when the lock is no longer needed.
func (l *RootfsLock) ExclusiveLock() (unlock func(), err error) {
	return l.lock(syscall.LOCK_EX)
}

func (l *RootfsLock) lock(how int) (func(), error) {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", l.path, err)
	}

	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("flock %s: %w", l.path, err)
	}

	unlock := func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	return unlock, nil
}
