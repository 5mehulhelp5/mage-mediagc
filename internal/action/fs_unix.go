//go:build unix

package action

import (
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

// deviceID returns the device the file lives on, which is how the
// same-filesystem check detects a quarantine directory that would turn an
// instant inode rename into a full cross-volume copy.
func deviceID(_ string, info os.FileInfo) (uint64, bool) {
	st, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, false
	}
	return uint64(st.Dev), true
}
