package main

import (
	"os"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/store/storetest"
)

// TestMain starts every new test database from one migrated copy; see
// storetest.RunSeeded. Under -race each run of the embedded migrations costs
// about two seconds and the tests open hundreds of databases.
func TestMain(m *testing.M) { os.Exit(storetest.RunSeeded(m)) }
