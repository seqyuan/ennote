//go:build linux

package hooks

import (
	"syscall"
)

func isLinux() bool { return true }
func linuxProcessGroup() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
