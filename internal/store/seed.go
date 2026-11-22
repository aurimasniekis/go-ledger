package store

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

var (
	seedMu   sync.RWMutex
	seedPath string
)

// UseSeedForNewDatabases makes [Open] start every new database file from a
// byte copy of seed instead of running every embedded migration, until it is
// called again with "". It exists only so test binaries that open hundreds of
// databases (under -race each migration run costs seconds) migrate once, and
// it panics outside a test binary (see [requireTest]): seed
// must be a closed database that Open created with the embedded migrations
// and default options, see [NewSeed]. It applies only when Open has the
// embedded migrations and the real clock and the file is new or empty, so a
// test that injects either, or opens an existing database, runs the real
// migrations as before.
func UseSeedForNewDatabases(seed string) {
	requireTest(inTestBinary(), "UseSeedForNewDatabases")
	seedMu.Lock()
	seedPath = seed
	seedMu.Unlock()
}

// inTestBinary reports whether this is a "go test" binary, which registers the
// test.* flags. It avoids testing.Testing so the production binary does not
// link the testing package.
func inTestBinary() bool { return flag.Lookup("test.v") != nil }

// requireTest panics unless inTest, so the seed hook cannot be used by the
// production binary: it would skip migrations and copy an arbitrary file.
func requireTest(inTest bool, name string) {
	if !inTest {
		panic("store: " + name + " is for test binaries only")
	}
}

// NewSeed creates a migrated database at path with the embedded migrations,
// closes it and returns, for [UseSeedForNewDatabases]. It fails when the
// closed database still has a write-ahead log, which a plain copy would lose.
func NewSeed(path string) error {
	requireTest(inTestBinary(), "NewSeed")
	UseSeedForNewDatabases("")
	db, err := Open(context.Background(), path, Options{})
	if err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() > 0 {
		return errors.New("store: seed database left a write-ahead log")
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
	fi, statErr := os.Stat(path)
	if statErr == nil && fi.Size() > 0 {
		return false, nil
	}
	src, err := os.Open(seed)
	if err != nil {
		return false, fmt.Errorf("store: open seed: %w", err)
	}
	defer func() { _ = src.Close() }()
	// Copy to a private temporary file next to path, then publish it
	// atomically, so a concurrent Open of the same new path never sees or
	// truncates a half-written database.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".seed-*")
	if err != nil {
		return false, fmt.Errorf("store: create seed copy: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op once renamed or linked
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("store: copy seed: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("store: copy seed: %w", err)
	}
	if statErr == nil {
		// An empty file already exists: replace it.
		if err := os.Rename(tmp.Name(), path); err != nil {
			return false, fmt.Errorf("store: create %s: %w", path, err)
		}
		return true, nil
	}
	// New path: link fails if a concurrent Open created it first, in which
	// case that copy stands and ours is dropped.
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("store: create %s: %w", path, err)
	}
	return true, nil
}
