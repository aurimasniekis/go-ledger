package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

var (
	seedMu   sync.RWMutex
	seedPath string
)

// UseSeedForNewDatabases makes [Open] start every new database file from a
// byte copy of seed instead of running every embedded migration, until it is
// called again with "". It exists only so test binaries that open hundreds of
// databases (under -race each migration run costs seconds) migrate once: seed
// must be a closed database that Open created with the embedded migrations
// and default options, see [NewSeed]. It applies only when Open has the
// embedded migrations and the real clock and the file is new or empty, so a
// test that injects either, or opens an existing database, runs the real
// migrations as before. Production code never calls it.
func UseSeedForNewDatabases(seed string) {
	seedMu.Lock()
	seedPath = seed
	seedMu.Unlock()
}

// NewSeed creates a migrated database at path with the embedded migrations,
// closes it and returns, for [UseSeedForNewDatabases]. It fails when the
// closed database still has a write-ahead log, which a plain copy would lose.
func NewSeed(path string) error {
	UseSeedForNewDatabases("")
	db, err := Open(context.Background(), path, Options{})
	if err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	if _, err := os.Stat(path + "-wal"); err == nil {
		if fi, _ := os.Stat(path + "-wal"); fi.Size() > 0 {
			return errors.New("store: seed database left a write-ahead log")
		}
	}
	return nil
}

// seedNewFile copies the seed to path when one is set and path is new or
// empty. It reports whether it did.
func seedNewFile(path string, opts Options) (bool, error) {
	seedMu.RLock()
	seed := seedPath
	seedMu.RUnlock()
	if seed == "" || opts.Migrations != nil || opts.Now != nil {
		return false, nil
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		return false, nil
	}
	src, err := os.Open(seed)
	if err != nil {
		return false, fmt.Errorf("store: open seed: %w", err)
	}
	defer func() { _ = src.Close() }()
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return false, fmt.Errorf("store: create %s: %w", path, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return false, fmt.Errorf("store: copy seed: %w", err)
	}
	return true, dst.Close()
}
