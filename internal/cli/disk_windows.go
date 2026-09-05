//go:build windows

package cli

import "golang.org/x/sys/windows"

// diskFreeBytes reports the free space available on the volume containing
// path via GetDiskFreeSpaceEx.
func diskFreeBytes(path string) (uint64, error) {
	var free, total, avail uint64

	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err //nolint:wrapcheck // caller wraps with context
	}

	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &avail); err != nil {
		return 0, err //nolint:wrapcheck // caller wraps with context
	}

	return free, nil
}
