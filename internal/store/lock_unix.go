//go:build unix

package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

const lockSupported = true

// ErrInstanceLocked means another Ledger process holds the database's
// instance lock.
var ErrInstanceLocked = errors.New("store: another Ledger process is using this database")

// InstanceLock is an exclusive advisory lock on "<db>.lock" that enforces one
// Ledger process per database (ADR-0016, ADR-0019). The kernel releases it
// when the process exits, however it exits, so a crash never leaves a stale
// lock.
type InstanceLock struct{ f *os.File }

// LockInstance takes the instance lock for the database at dbPath without
// waiting. It creates "<db>.lock" with mode 0600 if needed (the file stays;
// only the lock matters) and returns ErrInstanceLocked if the lock is held.
// The lock is advisory: it stops Ledger processes, not other programs.
func LockInstance(dbPath string) (*InstanceLock, error) {
	if err := checkPath(dbPath); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(dbPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (lock file %s.lock)", ErrInstanceLocked, dbPath)
		}
		return nil, fmt.Errorf("store: lock %s.lock: %w", dbPath, err)
	}
	return &InstanceLock{f: f}, nil
}

// Close releases the lock.
func (l *InstanceLock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close() // closing the descriptor drops the flock
	l.f = nil
	return err
}
