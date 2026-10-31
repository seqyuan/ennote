//go:build unix

package main

import (
	"os/signal"
	"syscall"
)

// ignoreSIGPIPE makes a write to a pipe whose read end has gone away fail with
// EPIPE instead of killing the process with SIGPIPE. The Worker is started by
// ennogate with its stderr attached to a pipe; when the supervisor is killed
// that pipe breaks, and the next routine log write would otherwise terminate
// the Worker and cancel every in-flight Run.
//
// signal.Ignore is process-wide state, and Go leaves it untouched across
// fork/exec, so spawned commands inherit the ignored disposition too: a shell
// pipeline such as `yes | head -1` sees EPIPE (cosmetic: the producer exits on
// a write error instead of dying by signal) rather than any change in output.
// That is preferable to the alternative, since re-enabling SIGPIPE in the child
// would also make the Worker's own writes fatal again.
func ignoreSIGPIPE() {
	signal.Ignore(syscall.SIGPIPE)
}
