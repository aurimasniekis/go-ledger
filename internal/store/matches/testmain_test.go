package matches_test

import (
	"os"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/store/storetest"
)

// TestMain starts every new test database from one migrated copy; see
// storetest.RunSeeded.
func TestMain(m *testing.M) { os.Exit(storetest.RunSeeded(m)) }
