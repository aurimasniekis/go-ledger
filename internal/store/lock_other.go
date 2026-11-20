//go:build !unix

package store

import "errors"

const lockSupported = false

// ErrInstanceLocked means another Ledger process holds the database's
// instance lock. It is never returned on this platform.
var ErrInstanceLocked = errors.New("store: another Ledger process is using this database")

// InstanceLock is a no-op on platforms without flock (the release targets are
// all Unix, ADR-0019): running two Ledger processes on one database is
// unsupported there and is not detected.
type InstanceLock struct{}

// LockInstance does nothing on this platform.
func LockInstance(dbPath string) (*InstanceLock, error) {
	if err := checkPath(dbPath); err != nil {
		return nil, err
	}
	return &InstanceLock{}, nil
}

// Close does nothing.
func (l *InstanceLock) Close() error { return nil }
