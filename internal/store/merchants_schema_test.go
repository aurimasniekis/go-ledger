package store

import (
	"context"
	"database/sql"
	"testing"
)

func init() {
	// Migration 0026 adds merchant enrichments and decisions (ADR-0051): both
	// tables start empty and enforce what the domain checks.
	fixtureChecks["0024_manual_payment_replacements.sql"] = func(t *testing.T, db *DB) {
		ctx := context.Background()
		err := db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			for _, table := range []string{"merchant_enrichments", "merchant_reviews"} {
				if n := queryInt(t, tx, "SELECT count(*) FROM "+table); n != 0 {
					t.Errorf("%s holds %d rows after the upgrade, want none", table, n)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		var id string
		err = db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT id FROM bank_transactions LIMIT 1").Scan(&id)
		})
		if err != nil {
			t.Fatal(err)
		}
		const at = "2026-01-27T03:04:05.000000000Z"
		cases := []struct {
			name, query string
			ok          bool
		}{
			{"identified", "INSERT INTO merchant_enrichments (transaction_id, status, name, source, confidence, retrieved_at) VALUES (?, 'identified', 'Acme', 'curated', 900, '" + at + "')", true},
			{"identified without a name", "INSERT INTO merchant_enrichments (transaction_id, status, source, confidence, retrieved_at) VALUES (?, 'identified', 'curated', 900, '" + at + "')", false},
			{"confidence above certainty", "INSERT INTO merchant_enrichments (transaction_id, status, name, source, confidence, retrieved_at) VALUES (?, 'identified', 'Acme', 'curated', 1001, '" + at + "')", false},
			{"ambiguous with one candidate", "INSERT INTO merchant_enrichments (transaction_id, status, source, candidates, retrieved_at) VALUES (?, 'ambiguous', 'curated', '[\"A\"]', '" + at + "')", false},
			{"ambiguous naming a merchant", "INSERT INTO merchant_enrichments (transaction_id, status, name, source, candidates, retrieved_at) VALUES (?, 'ambiguous', 'A', 'curated', '[\"A\",\"B\"]', '" + at + "')", false},
			{"unavailable with an answer time", "INSERT INTO merchant_enrichments (transaction_id, status, source, retrieved_at, last_failed_at) VALUES (?, 'unavailable', 'curated', '" + at + "', '" + at + "')", false},
			{"badly spelled source", "INSERT INTO merchant_enrichments (transaction_id, status, source, retrieved_at) VALUES (?, 'none', 'Curated Source', '" + at + "')", false},
			{"rejection with a name", "INSERT INTO merchant_reviews (transaction_id, decision, name, decided_kind, decided_via, decided_at) VALUES (?, 'rejected', 'x', 'user', 'session', '" + at + "')", false},
			{"correction without a name", "INSERT INTO merchant_reviews (transaction_id, decision, decided_kind, decided_via, decided_at) VALUES (?, 'corrected', 'user', 'session', '" + at + "')", false},
			{"unknown transaction", "INSERT INTO merchant_reviews (transaction_id, decision, decided_kind, decided_via, decided_at) VALUES ('01JZ0000000000000000009999', 'rejected', 'user', 'session', '" + at + "')", false},
			{"rejection", "INSERT INTO merchant_reviews (transaction_id, decision, decided_kind, decided_via, decided_at) VALUES (?, 'rejected', 'user', 'session', '" + at + "')", true},
		}
		for _, c := range cases {
			err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, c.query, id)
				return err
			})
			if (err == nil) != c.ok {
				t.Errorf("%s: err = %v, want ok = %v", c.name, err, c.ok)
			}
		}
		// A decision refuses the deletion of the bank transaction it is about.
		err = db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "DELETE FROM bank_transactions WHERE id = ?", id)
			return err
		})
		if err == nil {
			t.Error("a bank transaction with a merchant decision was deleted")
		}
	}
}
