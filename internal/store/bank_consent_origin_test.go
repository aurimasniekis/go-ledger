package store

import (
	"context"
	"database/sql"
	"testing"
)

func init() {
	// Migration 0021 dates a consent that already waits for its redirect by the
	// row's last update and leaves the others without a start.
	fixtureChecks["0020_commitment_match_tolerance.sql"] = func(t *testing.T, db *DB) {
		err := db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			var started sql.NullString
			var by string
			if err := tx.QueryRowContext(ctx, "SELECT consent_started_at, consent_initiator FROM bank_connections WHERE id = '01JZ0000000000000000000090'").Scan(&started, &by); err != nil {
				return err
			}
			if !started.Valid || started.String != "2026-01-22T04:00:00.000000000Z" || by != "" {
				t.Errorf("a waiting consent got start %v and initiator %q, want its last update and none", started, by)
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM bank_connections WHERE consent_state IS NULL AND consent_started_at IS NOT NULL"); n != 0 {
				t.Errorf("%d connections without a waiting consent got a start", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
