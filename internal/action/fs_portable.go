//go:build !unix

package action

import (
	"hash/fnv"
	"os"
	"path/filepath"
)

// dirOwnership has no meaningful answer where POSIX ownership does not exist.
// Reporting ok == false keeps the caller from attempting a chown that the
// platform cannot perform.
func dirOwnership(os.FileInfo) (uid, gid int, ok bool) {
	return 0, 0, false
}

// deviceID approximates the same-filesystem check using the volume name.
//
// On Windows the concept that matters is the volume: a rename from C:\ to D:\
// is no more an inode operation than a rename across mount points is on Unix,
// so comparing volume names catches exactly the case the check exists to catch.
// The name is hashed so the result is comparable as a uint64 without pulling in
// a platform-specific syscall.
func deviceID(path string, _ os.FileInfo) (uint64, bool) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(filepath.VolumeName(path)))
	return h.Sum64(), true
}
