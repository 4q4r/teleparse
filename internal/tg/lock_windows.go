//go:build windows

package tg

import "golang.org/x/sys/windows"

// lockHandle takes an exclusive lock over the whole file via LockFileEx;
// closing the handle releases it (Close is the unlock path).
func lockHandle(fd uintptr) error {
	const lockfileExclusiveLock = 0x00000002

	if err := windows.LockFileEx(windows.Handle(fd), lockfileExclusiveLock, 0, 1, 0, &windows.Overlapped{}); err != nil {
		return err //nolint:wrapcheck // plain sentinel for the caller to wrap
	}

	return nil
}

// unlockHandle is a no-op on Windows: the OS releases the range lock when
// the file handle closes, which AccountLock.Close always does.
func unlockHandle(_ uintptr) error {
	return nil
}
