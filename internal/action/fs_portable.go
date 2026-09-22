//go:build !unix

package action

import (
	"os"
	"path/filepath"
)

// dirOwnership has no meaningful answer where POSIX ownership does not exist.
// Reporting ok == false keeps the caller from attempting a chown that the
// platform cannot perform.
func dirOwnership(os.FileInfo) (uid, gid int, ok bool) {
	return 0, 0, false
}

// sameFilesystem approximates the check using the volume name.
//
// On Windows the concept that matters is the volume: a rename from C:\ to D:\
// is no more an inode operation than a rename across mount points is on Unix,
// so comparing volume names catches exactly the case the guard exists for.
func sameFilesystem(aPath, bPath string, _, _ os.FileInfo) (bool, error) {
	return filepath.VolumeName(aPath) == filepath.VolumeName(bPath), nil
}
