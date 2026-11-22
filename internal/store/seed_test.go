package store

import (
	"context"
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

func TestSeedConcurrentOpensOfOnePath(t *testing.T) {
	seed := filepath.Join(t.TempDir(), "seed.db")
	if err := NewSeed(seed); err != nil {
		t.Fatal(err)
	}
	UseSeedForNewDatabases(seed)
	t.Cleanup(func() { UseSeedForNewDatabases("") })
	path := filepath.Join(t.TempDir(), "new.db")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := Open(context.Background(), path, Options{})
			if err == nil {
				err = db.Close()
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
