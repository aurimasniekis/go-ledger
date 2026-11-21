package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// TestNoForeignKeyCascadesFromBankFacts enforces ADR-0013: every foreign key
// that refers to a bank-fact table (bank_*) uses ON DELETE RESTRICT and does
// not cascade updates, so deleting or rewriting a bank fact can never
// silently delete or blank out user data (or other bank facts). Bank facts
// are deleted only by the explicit purge path.
func TestNoForeignKeyCascadesFromBankFacts(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, tempPath(t), Options{})
	err := db.ReadTx(ctx, func(_ context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT m.name, f.\"table\", f.\"from\", f.on_delete, f.on_update FROM sqlite_schema m, pragma_foreign_key_list(m.name) f WHERE m.type = 'table'")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		refs := 0
		for rows.Next() {
			var table, parent, col, onDelete, onUpdate string
			if err := rows.Scan(&table, &parent, &col, &onDelete, &onUpdate); err != nil {
				return err
			}
			if !strings.HasPrefix(parent, "bank_") {
				continue
			}
			refs++
			if onDelete != "RESTRICT" {
				t.Errorf("%s.%s refers to %s with ON DELETE %s; bank facts are referenced with ON DELETE RESTRICT", table, col, parent, onDelete)
			}
			if onUpdate == "CASCADE" || onUpdate == "SET NULL" || onUpdate == "SET DEFAULT" {
				t.Errorf("%s.%s refers to %s with ON UPDATE %s", table, col, parent, onUpdate)
			}
		}
		if refs == 0 {
			t.Error("no foreign keys to bank_* tables found; the check is not looking at the schema")
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
}

func init() {
	fixtureChecks["0004_bank_facts.sql"] = func(t *testing.T, db *DB) {
		ctx := context.Background()
		err := db.ReadTx(ctx, func(_ context.Context, tx *sql.Tx) error {
			for table, want := range map[string]int{"bank_connections": 1, "bank_accounts": 1, "bank_balance_snapshots": 1, "bank_transactions": 2, "outbox": 2} {
				if n := queryInt(t, tx, "SELECT count(*) FROM "+table); n != want {
					t.Errorf("%s has %d rows after upgrade, want %d from the fixture", table, n, want)
				}
			}
			for _, table := range []string{"bank_refresh_runs", "bank_account_sync"} {
				if n := queryInt(t, tx, "SELECT count(*) FROM "+table); n != 0 {
					t.Errorf("%s has %d rows after upgrade, want an empty table", table, n)
				}
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM bank_transactions WHERE no_longer_reported_at IS NULL AND superseded_by IS NULL"); n != 2 {
				t.Errorf("upgrade changed transaction state: %d open rows", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	fixtureChecks["0005_bank_refresh.sql"] = func(t *testing.T, db *DB) {
		ctx := context.Background()
		err := db.ReadTx(ctx, func(_ context.Context, tx *sql.Tx) error {
			if n := queryInt(t, tx, "SELECT count(*) FROM bank_refresh_runs"); n != 2 {
				t.Errorf("bank_refresh_runs has %d rows after upgrade, want the 2 from the fixture", n)
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM bank_refresh_runs WHERE outcome = 'succeeded' AND transactions_inserted = 2 AND balances_added = 1"); n != 1 {
				t.Errorf("the succeeded refresh run changed or is missing: %d matching rows", n)
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM bank_refresh_runs WHERE outcome = 'failed' AND error_kind = 'rate_limited' AND error_code = 'fake-rate-limit'"); n != 1 {
				t.Errorf("the failed refresh run changed or is missing: %d matching rows", n)
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM bank_account_sync WHERE transactions_through = '2026-01-05' AND last_success_at = '2026-01-06T04:00:05.000000000Z' AND last_error_code = 'fake-rate-limit'"); n != 1 {
				t.Errorf("bank_account_sync changed or is missing: %d matching rows", n)
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM bank_transactions"); n != 2 {
				t.Errorf("bank_transactions has %d rows after upgrade, want the 2 from the fixture", n)
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM users"); n != 0 {
				t.Errorf("users has %d rows after upgrade, want an empty table", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	fixtureChecks["0003_outbox.sql"] = func(t *testing.T, db *DB) {
		ctx := context.Background()
		err := db.ReadTx(ctx, func(_ context.Context, tx *sql.Tx) error {
			if n := queryInt(t, tx, "SELECT count(*) FROM outbox"); n != 2 {
				t.Errorf("outbox has %d rows after upgrade, want the 2 from the fixture", n)
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM outbox WHERE state = 'pending'"); n != 1 {
				t.Errorf("%d pending outbox rows after upgrade, want 1", n)
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM bank_connections"); n != 0 {
				t.Errorf("bank_connections has %d rows after upgrade", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	fixtureChecks["0002_settings.sql"] = func(t *testing.T, db *DB) {
		ctx := context.Background()
		err := db.ReadTx(ctx, func(_ context.Context, tx *sql.Tx) error {
			var zone string
			if err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = 'reporting.timezone'").Scan(&zone); err != nil {
				return err
			}
			if zone != "Europe/Vilnius" {
				t.Errorf("reporting zone = %q after upgrade", zone)
			}
			for _, table := range []string{"bank_connections", "bank_accounts", "bank_balance_snapshots", "bank_transactions"} {
				if n := queryInt(t, tx, "SELECT count(*) FROM "+table); n != 0 {
					t.Errorf("%s has %d rows after upgrade, want an empty table", table, n)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
