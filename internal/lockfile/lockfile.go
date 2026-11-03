//go:build unix

// Package lockfile provides an advisory lock between homeflix processes
// sharing a data directory.
package lockfile

import (
	"errors"
	"os"
	"syscall"
)

// ErrLocked is returned when another process holds the lock.
var ErrLocked = errors.New("locked by another process")

// TryLock takes an exclusive lock on path without waiting. The returned
// function releases it.
func TryLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
