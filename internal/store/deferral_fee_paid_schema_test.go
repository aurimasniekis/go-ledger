package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func init() {
	// Migration 0029 adds the paid marker on deferral plan fees and rebuilds
	// the plan history table for two more actions: every existing fee is
	// unpaid, the history keeps its rows and its append-only triggers, and
	// the new actions are accepted.
	fixtureChecks["0028_organisation.sql"] = func(t *testing.T, db *DB) {
		err := db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			if n := queryInt(t, tx, "SELECT count(*) FROM deferral_plan_adjustments WHERE paid_at IS NOT NULL OR paid_actor_kind IS NOT NULL OR paid_actor_user IS NOT NULL OR paid_via IS NOT NULL"); n != 0 {
				t.Errorf("%d adjustments are paid after the upgrade, want none", n)
			}
			var ddl string
			if err := tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'deferral_plan_events'").Scan(&ddl); err != nil {
				return err
			}
			if !strings.Contains(ddl, "adjustment_paid") || !strings.Contains(ddl, "adjustment_unpaid") {
				t.Error("deferral_plan_events does not accept the paid and unpaid actions")
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'deferral_plan_events'"); n != 2 {
				t.Errorf("deferral_plan_events has %d triggers, want its 2 append-only triggers", n)
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND name = 'deferral_plan_adjustments_only_removed'"); n != 0 {
				t.Error("the old adjustment trigger is still there")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
