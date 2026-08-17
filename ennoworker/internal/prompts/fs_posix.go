//go:build linux || darwin

package prompts

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// randomSuffix returns an 8-byte hex-encoded random string.
func randomSuffix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read never fails on Linux/macOS.
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// ——— directory operations ———

// posixATFDCWD is the constant to open relative to CWD.
const posixATFDCWD = unix.AT_FDCWD

// openDirAt opens a subdirectory of rootFD with O_DIRECTORY|O_NOFOLLOW.
// The name must be a single path component (no slashes); intermediate
// components are NOT followed from rootFD — the final component is opened
// no-follow, so a symlink at the final position is rejected.
func openDirAt(rootFD int, name string) (int, error) {
	fd, err := unix.Openat(rootFD, name, unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	return fd, nil
}

// openDirPathNoFollow opens a directory by absolute path with O_NOFOLLOW on
// the final component. Intermediate path components are allowed to be
// symlinks (used for settings paths, which live in user-trusted config).
func openDirPathNoFollow(path string) (int, error) {
	fd, err := unix.Open(path, unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	return fd, nil
}

// readDirAt reads up to limit+1 entries from a directory FD using the
// portable per-OS dirent parsing (os.File.ReadDir on a dup of dirFD). The
// caller's fd is NOT closed. Returns at most limit+1 entries and whether the
// limit was reached (an entry beyond `limit` was read).
func readDirAt(dirFD int, limit int) ([]os.DirEntry, bool, error) {
	// Seek to beginning.
	if _, err := unix.Seek(dirFD, 0, 0); err != nil {
		return nil, false, err
	}

	// Dup so os.File ownership does not close the caller's fd.
	dup, err := unix.Dup(dirFD)
	if err != nil {
		return nil, false, err
	}
	f := os.NewFile(uintptr(dup), "prompts-dir")
	defer f.Close()

	all, err := f.ReadDir(-1)
	if err != nil {
		return nil, false, err
	}

	var entries []os.DirEntry
	for _, e := range all {
		entries = append(entries, e)
		if len(entries) > limit {
			return entries, true, nil
		}
	}
	return entries, false, nil
}

// ——— file read operations ———

// openFileAt opens a file relative to dirFD with O_RDONLY|O_NOFOLLOW.
func openFileAt(dirFD int, name string) (int, error) {
	return unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}

// openFilePathNoFollow opens a file by absolute path with O_NOFOLLOW on the
// final component (intermediate symlinks allowed — used for settings paths).
func openFilePathNoFollow(path string) (int, error) {
	return unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}

// fstatIsRegular checks whether fd is a regular file.
func fstatIsRegular(fd int) (bool, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return false, err
	}
	return stat.Mode&syscall.S_IFMT == syscall.S_IFREG, nil
}

// fstatOwner checks whether fd is owned by the current euid and has link
// count 1.
func fstatOwner(fd int) (owned bool, nlink uint64, err error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return false, 0, err
	}
	return stat.Uid == uint32(unix.Geteuid()), uint64(stat.Nlink), nil
}

// readBounded reads from fd until EOF or maxBytes+1 bytes. Returns the data
// (up to maxBytes) or an error if the file exceeds maxBytes. A single Read
// may return a short read, so this loops.
func readBounded(fd int, maxBytes int) ([]byte, error) {
	buf := make([]byte, maxBytes+1)
	total := 0
	for {
		if total > maxBytes {
			return nil, fmt.Errorf("file exceeds %d bytes", maxBytes)
		}
		n, err := unix.Read(fd, buf[total:])
		if err != nil {
			return nil, err
		}
		if n == 0 {
			break // EOF
		}
		total += n
	}
	if total > maxBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", maxBytes)
	}
	return buf[:total], nil
}

// ——— file write / atomic operations ———

// createTemp creates a temporary file in the directory open as dirFD.
// Returns the fd and the basename of the temp file.
func createTempAt(dirFD int, prefix string, mode uint32) (int, string, error) {
	// Generate a unique temp name.
	name := prefix + randomSuffix()
	fd, err := unix.Openat(dirFD, name, unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_RDWR, mode)
	if err != nil {
		return -1, "", err
	}
	return fd, name, nil
}

// writeFull writes all of data to fd.
func writeFull(fd int, data []byte) error {
	for len(data) > 0 {
		n, err := unix.Write(fd, data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// fsyncFD syncs fd to disk.
func fsyncFD(fd int) error { return unix.Fsync(fd) }

// closeFD closes an fd.
func closeFD(fd int) { unix.Close(fd) }

// linkAt creates a hard link from oldname to newname within dirFD. It uses
// flags 0 rather than AT_SYMLINK_NOFOLLOW: the Linux kernel rejects
// AT_SYMLINK_NOFOLLOW for linkat with EINVAL (only AT_SYMLINK_FOLLOW is a
// valid flag there), while newpath is NEVER dereferenced by linkat on either
// Linux or macOS — an existing newpath (regular file OR symlink) yields
// EEXIST without touching any target. oldname is always our own regular temp
// file, so oldpath-follow semantics are irrelevant.
func linkAt(dirFD int, oldname, newname string) error {
	return unix.Linkat(dirFD, oldname, dirFD, newname, 0)
}

// renameAt atomically replaces newname with oldname within dirFD.
func renameAt(dirFD int, oldname, newname string) error {
	return unix.Renameat(dirFD, oldname, dirFD, newname)
}

// unlinkAt removes name from dirFD.
func unlinkAt(dirFD int, name string) error { return unix.Unlinkat(dirFD, name, 0) }

// syncDir syncs the directory fd.
func syncDir(dirFD int) error { return unix.Fsync(dirFD) }
