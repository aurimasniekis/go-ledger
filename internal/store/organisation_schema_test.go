package store

import (
	"context"
	"database/sql"
	"testing"
)

func init() {
	// Migration 0028 adds categories, labels, buckets and their assignments
	// (ADR-0053): every table starts empty and nothing existing moves.
	fixtureChecks["0027_commitment_leeway.sql"] = func(t *testing.T, db *DB) {
		err := db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			for _, table := range []string{"categories", "labels", "buckets", "category_assignments", "label_assignments", "bucket_members"} {
				if n := queryInt(t, tx, "SELECT count(*) FROM "+table); n != 0 {
					t.Errorf("%s holds %d rows after the upgrade, want none", table, n)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
