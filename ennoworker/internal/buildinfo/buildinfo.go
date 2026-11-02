// Package buildinfo carries the identity of the running build.
package buildinfo

// Version is the release version. scripts/build-workers.mjs stamps it into the
// binary with `-ldflags -X`; an unstamped local build reports "dev" rather than
// claiming a release it was not built as. It is the only source of the version
// a process reports, so /v1/runtime and a frozen Run snapshot can never disagree
// about which build produced them.
var Version = "dev"
