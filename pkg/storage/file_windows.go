//go:build windows

package storage

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// tryLockFile takes an exclusive lock on f's first byte without blocking,
// reporting false when another handle holds it.
func tryLockFile(f *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, new(windows.Overlapped))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return false, nil
	default:
		return false, err
	}
}

// replaceFile renames src over dst. Windows refuses to replace a file that
// another process has open without sharing deletes, as a reader in another
// realm process may briefly have, so the rename is retried for a while.
func replaceFile(src, dst string) error {
	var err error
	for wait := time.Millisecond; wait < time.Second; wait *= 2 {
		err = os.Rename(src, dst)
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return err
		}
		time.Sleep(wait)
	}
	return err
}

// syncDir does nothing: Windows cannot sync a directory, and NTFS journals
// renames.
func syncDir(string) error {
	return nil
}
