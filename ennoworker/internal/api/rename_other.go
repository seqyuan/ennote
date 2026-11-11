//go:build !linux && !darwin

package api

import "os"

// renameNoReplace has no atomic form on this platform: the caller's existence
// check is the guarantee, and a concurrent writer can still land in the window.
// Linux and macOS — the platforms the release targets — use the real thing.
func renameNoReplace(from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return os.ErrExist
	}
	return os.Rename(from, to)
}
