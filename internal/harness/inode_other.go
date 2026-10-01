//go:build !darwin && !linux && !freebsd && !netbsd && !openbsd && !dragonfly

package harness

import "os"

// FileInode returns 0 on platforms without a stable inode, where a changed size or
// modification time is the only available signal.
func FileInode(os.FileInfo) int64 { return 0 }
