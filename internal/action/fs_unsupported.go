//go:build !unix

package action

import (
	"fmt"
	"os"
	"runtime"
)

// mage-mediagc ships binaries for Linux and macOS only. This file keeps the
// package compiling on other platforms so that `go build ./...` and
// `go vet ./...` still work on any developer machine, but the guards that make
// the tool safe to point at a live shop cannot be implemented without POSIX
// primitives — and guessing is worse than refusing.
//
// Do not reintroduce a best-effort implementation here. A wrong answer means
// either a silent cross-volume copy that doubles disk usage, or a rename of
// files belonging to the web server, and a code path that no CI job exercises
// cannot be trusted with either.

// dirOwnership has no meaningful answer where POSIX ownership does not exist.
// Reporting ok == false keeps the caller from attempting a chown the platform
// cannot perform.
func dirOwnership(os.FileInfo) (uid, gid int, ok bool) {
	return 0, 0, false
}

// sameFilesystem always fails on a platform that has no POSIX device numbers,
// so quarantine refuses to run rather than risk turning an instant inode
// rename into a full copy that fills the disk.
func sameFilesystem(_, _ string, _, _ os.FileInfo) (bool, error) {
	return false, fmt.Errorf(
		"unsupported platform %s: mage-mediagc requires a Unix-like system "+
			"(Linux, macOS, *BSD), because it must compare filesystem device "+
			"numbers before moving files",
		runtime.GOOS)
}
