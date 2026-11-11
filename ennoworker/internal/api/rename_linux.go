//go:build linux

package api

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames without ever replacing the destination.
//
// On POSIX a plain rename silently overwrites the target, so "check then rename"
// is a race with any other writer. RENAME_NOREPLACE makes the kernel do the check
// and the move as one action: the only outcomes are "moved" or EEXIST.
func renameNoReplace(from, to string) error {
	err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
	if err == nil {
		return nil
	}
	// A filesystem without RENAME_NOREPLACE support answers EINVAL/ENOSYS;
	// fall back to the racy-but-portable path rather than refusing the rename.
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
		if _, statErr := os.Lstat(to); statErr == nil {
			return os.ErrExist
		}
		return os.Rename(from, to)
	}
	return err
}
