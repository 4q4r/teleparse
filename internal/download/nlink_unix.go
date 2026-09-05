//go:build !windows

package download

import (
	"os"
	"syscall"
)

// hardlinkCount reports how many directory entries reference the file's
// inode; ok is false when the count cannot be determined.
func hardlinkCount(path string) (int64, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}

	return int64(stat.Nlink), true //nolint:gosec // a link count beyond MaxInt64 is physically impossible
}
