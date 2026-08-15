//go:build !linux

package hooks

func isLinux() bool                { return false }
func linuxProcessGroup() *struct{} { return nil }
