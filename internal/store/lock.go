package store

import (
	"os"
	"path/filepath"
	"syscall"
)

// Lock is the single-instance guard.
//
// An exclusive flock is held for the lifetime of the widget process. The kernel
// releases it when that process exits, which makes it robust against PID reuse
// — the failure the earlier pid-file approach had after a reboot, when an
// unrelated process could inherit our old PID and convince us we were already
// running.
type Lock struct {
	Path string

	// held keeps the descriptor alive for the process lifetime; closing it would
	// drop the lock.
	held *os.File
}

// DefaultLock points at the path under the user's config directory.
func DefaultLock() *Lock { return &Lock{Path: LockPath()} }

func (l *Lock) open() (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(l.Path), 0700); err != nil {
		return nil, err
	}
	return os.OpenFile(l.Path, os.O_CREATE|os.O_RDWR, 0600)
}

// Acquire takes the lock and keeps it. It reports false when another instance
// already holds it.
func (l *Lock) Acquire() bool {
	f, err := l.open()
	if err != nil {
		return false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	l.held = f
	return true
}

// HeldElsewhere probes whether another instance holds the lock. The probe
// acquires and immediately releases, so it does not reserve the slot.
func (l *Lock) HeldElsewhere() bool {
	f, err := l.open()
	if err != nil {
		return false
	}
	defer f.Close()
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil
}
