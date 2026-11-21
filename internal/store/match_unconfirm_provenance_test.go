package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func init() {
	// Migration 0023 rebuilds the match history table: every row is kept in
	// its history order (rowid order is chronological), no row gains a
	// provenance or an occurrence version, and the history triggers still hold.
	fixtureChecks["0022_bank_consent_origin.sql"] = func(t *testing.T, db *DB) {
		ctx := context.Background()
		err := db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			if n := queryInt(t, tx, "SELECT count(*) FROM payment_match_events"); n != 3 {
				t.Errorf("%d history entries after the rebuild, want 3", n)
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM payment_match_events WHERE actor_via IS NOT NULL OR occurrence_version IS NOT NULL"); n != 0 {
				t.Errorf("%d history entries gained a provenance or an occurrence version, want none", n)
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM payment_matches WHERE created_by_via IS NOT NULL OR decided_by_via IS NOT NULL"); n != 0 {
				t.Errorf("%d matches gained a provenance, want none", n)
			}
			rows, err := tx.QueryContext(ctx, "SELECT action FROM payment_match_events WHERE match_id = '01JZ0000000000000000000172' ORDER BY rowid")
			if err != nil {
				return err
			}
			defer func() { _ = rows.Close() }()
			var got []string
			for rows.Next() {
				var a string
				if err := rows.Scan(&a); err != nil {
					return err
				}
				got = append(got, a)
			}
			if strings.Join(got, ",") != "suggested,corrected" {
				t.Errorf("history in rowid order = %v, want suggested then corrected", got)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		err = db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			for _, q := range []string{
				"UPDATE payment_match_events SET reason = 'x' WHERE id = '01JZ0000000000000000000171'",
				"DELETE FROM payment_match_events WHERE id = '01JZ0000000000000000000171'",
				"UPDATE payment_matches SET state = 'suggested', decided_by_kind = NULL, decided_by_user = NULL, decided_at = NULL, created_by_via = 'session' WHERE id = '01JZ0000000000000000000170'",
				"UPDATE payment_matches SET state = 'suggested' WHERE id = '01JZ0000000000000000000170'",
			} {
				if _, err := tx.ExecContext(ctx, q); err == nil {
					t.Errorf("%s succeeded, want it refused", q)
				}
			}
			// Unconfirming with the decision cleared is allowed.
			if _, err := tx.ExecContext(ctx, "UPDATE payment_matches SET state = 'suggested', decided_by_kind = NULL, decided_by_user = NULL, decided_at = NULL WHERE id = '01JZ0000000000000000000170'"); err != nil {
				t.Errorf("confirmed -> suggested with the decision cleared: %v", err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
