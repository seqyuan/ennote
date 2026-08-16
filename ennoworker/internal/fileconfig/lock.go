package fileconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// withFileLock runs fn while holding an exclusive advisory lock on
// <path>.lock. The lock file is a runtime artifact that coordinates
// cross-process writes to a shared config file; it is never part of a catalog
// document. It complements the in-process mutex that callers hold, so the
// lock ordering is always in-process mutex -> file lock (no cycle).
func withFileLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open config lock: %w", err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock config: %w", err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	return fn()
}
