//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// mainLockFile stays open, and locked, for the life of the main instance.
var mainLockFile *os.File

// acquireMainLock takes an exclusive flock on deckhand.lock in the data
// folder and holds it for the life of the process. It reports false when
// another process holds it.
func acquireMainLock() bool {
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return false
	}
	f, err := os.OpenFile(filepath.Join(dataDir(), "deckhand.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return false
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return false
	}
	mainLockFile = f
	return true
}

func lockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }

func unlockFile(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
