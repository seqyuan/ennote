//go:build !linux && !darwin

package prompts

import (
	"errors"
	"os"
)

// ErrUnsupportedPlatform is returned when the prompts package is used on an
// unsupported platform. The registry, store, and service require Linux or
// macOS POSIX primitives (openat, linkat, renameat, unlinkat with
// AT_SYMLINK_NOFOLLOW).
var ErrUnsupportedPlatform = errors.New("prompts: requires Linux or macOS")

const posixATFDCWD = -1 // unused placeholder; every op returns ErrUnsupportedPlatform

func openDirAt(int, string) (int, error)                    { return -1, ErrUnsupportedPlatform }
func openDirPathNoFollow(string) (int, error)               { return -1, ErrUnsupportedPlatform }
func readDirAt(int, int) ([]os.DirEntry, bool, error)       { return nil, false, ErrUnsupportedPlatform }
func openFileAt(int, string) (int, error)                   { return -1, ErrUnsupportedPlatform }
func openFilePathNoFollow(string) (int, error)              { return -1, ErrUnsupportedPlatform }
func fstatIsRegular(int) (bool, error)                      { return false, ErrUnsupportedPlatform }
func fstatOwner(int) (bool, uint64, error)                  { return false, 0, ErrUnsupportedPlatform }
func readBounded(int, int) ([]byte, error)                  { return nil, ErrUnsupportedPlatform }
func createTempAt(int, string, uint32) (int, string, error) { return -1, "", ErrUnsupportedPlatform }
func writeFull(int, []byte) error                           { return ErrUnsupportedPlatform }
func fsyncFD(int) error                                     { return ErrUnsupportedPlatform }
func closeFD(int)                                           {}
func linkAt(int, string, string) error                      { return ErrUnsupportedPlatform }
func renameAt(int, string, string) error                    { return ErrUnsupportedPlatform }
func unlinkAt(int, string) error                            { return ErrUnsupportedPlatform }
func syncDir(int) error                                     { return ErrUnsupportedPlatform }
