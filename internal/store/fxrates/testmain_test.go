package fxrates_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/store"
)

// TestMain migrates one database for the whole test binary and has every new
// database start from a copy of it (see store.UseSeedForNewDatabases); tests
// that inject their own migrations or clock still migrate for real.
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
