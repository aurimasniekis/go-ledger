package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestSeedHookPanicsOutsideTests(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("requireTest(false) did not panic")
		}
	}()
	requireTest(false, "x")
}

func TestSeedGuardRecognisesTestBinary(t *testing.T) {
	if !inTestBinary() {
		t.Fatal("inTestBinary is false inside a test binary")
	}
}

// TestSeedConcurrentOpensOfOnePath hammers one new path from several Opens, many
// rounds. The old copy-in-place code (O_TRUNC on the target) could let one Open
// truncate a database another had already opened; that is a timing race, so
// this test can fail against it only probabilistically and cannot be made
// deterministic without a test hook inside seedNewFile. It does catch any
// corrupt or half-copied result deterministically.
func TestSeedConcurrentOpensOfOnePath(t *testing.T) {
	seed := filepath.Join(t.TempDir(), "seed.db")
	if err := NewSeed(seed); err != nil {
		t.Fatal(err)
	}
	UseSeedForNewDatabases(seed)
	t.Cleanup(func() { UseSeedForNewDatabases("") })
	for round := 0; round < 20; round++ {
		seedRound(t, filepath.Join(t.TempDir(), "new.db"))
	}
}

func seedRound(t *testing.T, path string) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := Open(context.Background(), path, Options{})
			if err == nil {
				var res string
				err = db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
					return tx.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&res)
				})
				if err == nil && res != "ok" {
					err = errors.New("integrity_check: " + res)
				}
				if cerr := db.Close(); err == nil {
					err = cerr
				}
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}
