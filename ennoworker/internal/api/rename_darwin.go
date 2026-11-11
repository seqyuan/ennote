//go:build darwin

package api

import (
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames without replacing the destination (RENAME_EXCL).
func renameNoReplace(from, to string) error {
	err := unix.RenamexNp(from, to, unix.RENAME_EXCL)
	if err == nil {
		return nil
	}
	if err == unix.ENOTSUP || err == unix.EINVAL {
		if _, statErr := os.Lstat(to); statErr == nil {
			return os.ErrExist
		}
		return os.Rename(from, to)
	}
	return err
}
