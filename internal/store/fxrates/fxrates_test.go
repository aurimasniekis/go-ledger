package fxrates_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx/fxtest"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/fxrates"
)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sqliteCache(t *testing.T) fx.Cache { return fxrates.New(openDB(t)) }

func TestSQLiteCacheContract(t *testing.T) { fxtest.CacheContract(t, sqliteCache) }

// TestServiceWithSQLiteCache runs the FX1 acceptance tests (missing rate,
// stale rate, outage keeps cache, no provider call when the cached rate is
// acceptable) against the real store.
func TestServiceWithSQLiteCache(t *testing.T) { fxtest.ServiceSuite(t, sqliteCache) }

// TestSchemaRefusesBadRates checks the table's own guards, below the Go
// validation: a rate that is not a positive exact decimal, a pair with one
// currency, and an inverted retrieval order.
func TestSchemaRefusesBadRates(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	const ts = "2026-11-16T09:00:00.000000000Z"
	insert := func(rate, base, quote, first string) error {
		return db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO fx_rates (provider, base_currency, quote_currency, rate, rate_date, first_retrieved_at, retrieved_at, confirmed_through) VALUES ('p', ?, ?, ?, '2026-11-13', ?, ?, '2026-11-13')", base, quote, rate, first, ts)
			return err
		})
	}
	if err := insert("1.0834", "EUR", "USD", ts); err != nil {
		t.Fatalf("valid row refused: %v", err)
	}
	for name, c := range map[string][4]string{
		"zero":           {"0.00", "EUR", "GBP", ts},
		"negative":       {"-1.2", "EUR", "SEK", ts},
		"exponent":       {"1e3", "EUR", "NOK", ts},
		"same currency":  {"1", "EUR", "EUR", ts},
		"first after":    {"1.1", "EUR", "DKK", "2026-11-17T09:00:00.000000000Z"},
		"lower-case ccy": {"1.1", "eur", "PLN", ts},
	} {
		if err := insert(c[0], c[1], c[2], c[3]); err == nil {
			t.Errorf("%s: row accepted", name)
		}
	}
}
