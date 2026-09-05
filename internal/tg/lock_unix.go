//go:build !windows

package tg

import "syscall"

// lockHandle takes an exclusive non-blocking advisory lock on the open file.
func lockHandle(fd uintptr) error {
	if err := syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err //nolint:wrapcheck // plain sentinel for the caller to wrap
	}

	return nil
}

// unlockHandle releases a previously taken advisory lock.
func unlockHandle(fd uintptr) error {
	return syscall.Flock(int(fd), syscall.LOCK_UN) //nolint:wrapcheck // caller wraps
}
