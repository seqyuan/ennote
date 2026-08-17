//go:build !linux

package hooks

import (
	"syscall"
)

func isLinux() bool { return false }
func linuxProcessGroup() *syscall.SysProcAttr { return nil }
