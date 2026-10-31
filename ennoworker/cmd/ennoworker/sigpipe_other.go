//go:build !unix

package main

// ignoreSIGPIPE is a no-op on platforms without SIGPIPE: a broken pipe is
// reported as a regular write error, which the stderr log handler ignores.
func ignoreSIGPIPE() {}
