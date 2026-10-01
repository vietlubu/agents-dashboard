//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package harness

import (
	"os"
	"syscall"
)

// FileInode returns the inode of a file, used to detect a replaced file whose path did
// not change. A file replaced by a rename gets a new inode, which is the only reliable
// signal that stored byte offsets no longer describe this path.
func FileInode(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int64(st.Ino)
	}
	return 0
}
