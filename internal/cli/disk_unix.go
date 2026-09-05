//go:build !windows

package cli

import "syscall"

// diskFreeBytes reports the free space available to unprivileged users on
// the filesystem containing path.
func diskFreeBytes(path string) (uint64, error) {
	var stats syscall.Statfs_t

	if err := syscall.Statfs(path, &stats); err != nil {
		return 0, err //nolint:wrapcheck // caller wraps with context
	}

	if stats.Bsize <= 0 {
		return 0, nil
	}

	return stats.Bavail * uint64(stats.Bsize), nil
}
