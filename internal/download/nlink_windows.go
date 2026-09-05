//go:build windows

package download

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// hardlinkCount reports the number of hard links through the file-handle
// metadata; ok is false when the count cannot be determined, in which case
// gc never deletes the blob.
func hardlinkCount(path string) (int64, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, false
	}

	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(abs), 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return 0, false
	}

	defer func() { _ = windows.CloseHandle(handle) }() //nolint:errcheck // best-effort handle close

	var info windows.ByHandleFileInformation

	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return 0, false
	}

	return int64(info.NumberOfLinks), true
}
