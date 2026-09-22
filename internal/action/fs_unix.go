//go:build unix

package action

import (
	"fmt"
	"os"
	"syscall"
)

// dirOwnership reports the POSIX owner of a file, used so the recreated cache
// directory keeps the web server's user when the tool runs as root.
func dirOwnership(info os.FileInfo) (uid, gid int, ok bool) {
	st, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

// sameFilesystem reports whether two paths live on the same filesystem, which
// is how the guard detects a quarantine directory that would turn an instant
// inode rename into a full cross-volume copy.
//
// The device fields are compared directly rather than normalized to a common
// integer type first: the field is int32 on darwin and uint64 on linux, so any
// cast is redundant on one platform and necessary on the other, which makes it
// impossible to satisfy the unconvert linter on both at once.
func sameFilesystem(aPath, bPath string, aInfo, bInfo os.FileInfo) (bool, error) {
	aStat, ok := aInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("cannot read filesystem information for %s", aPath)
	}
	bStat, ok := bInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("cannot read filesystem information for %s", bPath)
	}
	return aStat.Dev == bStat.Dev, nil
}
