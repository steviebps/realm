//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)

package storage

import "os"

// tryLockFile always succeeds: this platform has no file locking realm
// supports, so Updates are serialized only within one process.
func tryLockFile(*os.File) (bool, error) {
	return true, nil
}

func replaceFile(src, dst string) error {
	return os.Rename(src, dst)
}

func syncDir(string) error {
	return nil
}
