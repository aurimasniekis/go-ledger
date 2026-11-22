// Package storetest holds helpers shared by test binaries that open many
// databases. Production code must not import it.
package storetest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/store"
)

// RunSeeded migrates one database for the whole test binary, has every new
// database start from a copy of it (see [store.UseSeedForNewDatabases]) and
// runs the tests, returning the exit code for os.Exit. Tests that inject their
// own migrations or clock still migrate for real. The copy is byte-identical
// to what Open would have produced, except that schema_migrations.applied_at
// holds the seed's time. Use it as:
//
//	func TestMain(m *testing.M) { os.Exit(storetest.RunSeeded(m)) }
func RunSeeded(m *testing.M) int {
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
