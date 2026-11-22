package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/store"
)

// TestMain migrates one database for the whole test binary and has every new
// database of a test start from a copy of it. Under -race each run of the 24
// embedded migrations costs about two seconds, and the tests open hundreds of
// databases; the copy is byte-identical to what Open would have produced, except that
// schema_migrations.applied_at holds the seed's time, not the time of Open.
// Tests that inject their own migrations or clock still migrate for real.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	dir, err := os.MkdirTemp("", "ledger-seed-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed dir:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	seed := filepath.Join(dir, "seed.db")
	if err := store.NewSeed(seed); err != nil {
		fmt.Fprintln(os.Stderr, "seed database:", err)
		return 1
	}
	store.UseSeedForNewDatabases(seed)
	return m.Run()
}
