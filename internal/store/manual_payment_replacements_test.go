package store

import (
	"context"
	"database/sql"
	"testing"
)

func init() {
	// Migration 0024 adds the replacement history of manual payments (ADR-0049):
	// existing manual payments keep counting, with no replacement recorded, and
	// the history refuses deletes.
	fixtureChecks["0023_match_unconfirm_provenance.sql"] = func(t *testing.T, db *DB) {
		ctx := context.Background()
		err := db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			if n := queryInt(t, tx, "SELECT count(*) FROM manual_payments WHERE deleted_at IS NULL"); n != 1 {
				t.Errorf("%d manual payments after the upgrade, want 1", n)
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM manual_payment_replacements"); n != 0 {
				t.Errorf("%d replacements after the upgrade, want none", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		err = db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			// The fixture's suggestion is an open match of the record's
			// occurrence, in its currency: it may replace the record. Deleting
			// the replacement is refused.
			if _, err := tx.ExecContext(ctx, "INSERT INTO manual_payment_replacements (id, manual_payment_id, match_id, replaced_event_id, replaced_at)"+
				" VALUES ('01JZ0000000000000000000181', '01JZ0000000000000000000180', '01JZ0000000000000000000172', '01JZ0000000000000000000173', '2026-01-27T03:04:05.000000000Z')"); err != nil {
				t.Errorf("replacement by the occurrence's open match: %v", err)
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM manual_payment_replacements"); err == nil {
				t.Error("a replacement was deleted")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
