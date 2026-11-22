package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func init() {
	// Migration 0030 gives merchants a record (task M1, ADR-0057, D-MER 1a):
	// one merchant per alias key from the v29 decisions and merchant-keyed
	// assignments, deterministic ids, and every decision, identified answer
	// and assignment refers to its merchant by id. Nothing is lost.
	fixtureChecks["0029_deferral_fee_paid.sql"] = func(t *testing.T, db *DB) {
		const (
			acme    = "0000000000M000000000000001" // "acme shop": an assignment key only
			cafe    = "0000000000M000000000000002" // "café example": decomposed decision name, precomposed key
			lidl    = "0000000000M000000000000003" // "lidl": two decisions in different case
			tx13    = "01JZ0000000000000000000013"
			tx14    = "01JZ0000000000000000000014"
			tx15    = "01JZ0000000000000000000015"
			tx16    = "01JZ0000000000000000000016"
			tx17    = "01JZ0000000000000000000017"
			tx18    = "01JZ0000000000000000000018"
			nfcCafe = "café example"
		)
		err := db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			type merchant struct{ id, name, created, updated string }
			var got []merchant
			rows, err := tx.QueryContext(ctx, "SELECT id, display_name, created_at, updated_at FROM merchants WHERE merged_into_id IS NULL AND archived_at IS NULL AND version = 1 ORDER BY id")
			if err != nil {
				return err
			}
			for rows.Next() {
				var m merchant
				if err := rows.Scan(&m.id, &m.name, &m.created, &m.updated); err != nil {
					return err
				}
				got = append(got, m)
			}
			_ = rows.Close()
			want := []merchant{
				{acme, "acme shop", "2026-02-05T11:00:00.000000000Z", "2026-02-05T14:00:00.000000000Z"},
				{cafe, "Café Example", "2026-02-04T10:00:00.000000000Z", "2026-02-05T10:00:00.000000000Z"},
				{lidl, "LIDL", "2026-02-02T10:00:00.000000000Z", "2026-02-05T13:00:00.000000000Z"},
			}
			if len(got) != len(want) {
				t.Fatalf("merchants after the upgrade = %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("merchant %d = %+v, want %+v", i, got[i], want[i])
				}
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM merchants"); n != 3 {
				t.Errorf("%d merchants, want 3", n)
			}
			for alias, merchant := range map[string]string{"acme shop": acme, nfcCafe: cafe, "lidl": lidl} {
				if n := queryInt(t, tx, "SELECT count(*) FROM merchant_aliases WHERE alias_key = ? AND merchant_id = ? AND origin = 'upgrade' AND id = '0000000000A' || substr(merchant_id, 12)", alias, merchant); n != 1 {
					t.Errorf("alias %q of %s: %d rows, want 1", alias, merchant, n)
				}
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM merchant_aliases"); n != 3 {
				t.Errorf("%d aliases, want 3", n)
			}
			// Decisions keep their names and versions and point at the merchant.
			for _, c := range []struct {
				id, decision, name, merchant string
				version                      int
			}{
				{tx15, "confirmed", "Lidl", lidl, 1},
				{tx16, "corrected", "LIDL", lidl, 2},
				{tx17, "corrected", "Café Example", cafe, 1},
				{tx13, "rejected", "", "", 1},
			} {
				var decision, name string
				var merchant sql.NullString
				var version int
				if err := tx.QueryRowContext(ctx, "SELECT decision, name, merchant_id, version FROM merchant_reviews WHERE transaction_id = ?", c.id).Scan(&decision, &name, &merchant, &version); err != nil {
					return err
				}
				if decision != c.decision || name != c.name || merchant.String != c.merchant || version != c.version {
					t.Errorf("decision on %s = %s %q %q v%d, want %s %q %q v%d", c.id, decision, name, merchant.String, version, c.decision, c.name, c.merchant, c.version)
				}
			}
			// Answers keep their provenance; identified names with an alias resolve.
			for id, want := range map[string]string{tx15: lidl, tx16: lidl, tx14: "", tx17: "", tx18: "", tx13: ""} {
				var merchant sql.NullString
				if err := tx.QueryRowContext(ctx, "SELECT merchant_id FROM merchant_enrichments WHERE transaction_id = ?", id).Scan(&merchant); err != nil {
					return err
				}
				if merchant.String != want {
					t.Errorf("answer of %s resolves to %q, want %q", id, merchant.String, want)
				}
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM merchant_enrichments WHERE recheck = 0"); n != 6 {
				t.Errorf("%d answers without recheck, want all 6", n)
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM merchant_enrichments WHERE transaction_id = ? AND source = 'curated' AND confidence = 950", tx15); n != 1 {
				t.Error("the answer's source and confidence changed")
			}
			// Assignments move from the name key to the merchant id.
			for _, c := range []struct {
				merchant, category, origin string
			}{{lidl, "01JZ0000000000000000000300", "user"}, {cafe, "01JZ0000000000000000000301", "rule"}, {acme, "", "user"}} {
				var category sql.NullString
				var origin string
				if err := tx.QueryRowContext(ctx, "SELECT category_id, origin FROM category_assignments WHERE object_type = 'merchant' AND object_id = ? AND merchant_id = ?", c.merchant, c.merchant).Scan(&category, &origin); err != nil {
					t.Errorf("category assignment of %s: %v", c.merchant, err)
					continue
				}
				if category.String != c.category || origin != c.origin {
					t.Errorf("category of %s = %q (%s), want %q (%s)", c.merchant, category.String, origin, c.category, c.origin)
				}
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM category_assignments WHERE object_type = 'transaction' AND object_id = ? AND transaction_id = ? AND merchant_id IS NULL", tx18, tx18); n != 1 {
				t.Error("the transaction's category assignment did not survive unchanged")
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM label_assignments WHERE object_type = 'merchant' AND object_id = ? AND merchant_id = ? AND removed = 0 AND origin = 'rule' AND source = 'rule-weekly'", lidl, lidl); n != 1 {
				t.Error("lidl's label assignment did not move")
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM label_assignments WHERE object_type = 'merchant' AND object_id = ? AND removed = 1", acme); n != 1 {
				t.Error("acme's removed label did not move")
			}
			for _, table := range []string{"category_assignments", "label_assignments"} {
				if n := queryInt(t, tx, "SELECT count(*) FROM "+table+" WHERE object_type = 'merchant' AND (merchant_id IS NULL OR object_id <> merchant_id)"); n != 0 {
					t.Errorf("%s: %d merchant rows still keyed by name", table, n)
				}
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM sqlite_temp_master"); n != 0 {
				t.Errorf("%d temporary objects left behind", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		// A decision or assignment blocks deleting its merchant.
		err = db.WriteTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "DELETE FROM merchants WHERE id = ?", lidl)
			return err
		})
		if err == nil {
			t.Error("deleting a merchant that decisions refer to succeeded")
		}
	}
}

// TestMerchantRecordUpgradeRefusesCollisions loads the v29 fixture with two
// assignment keys that differ only in Unicode composition for the same
// category slot: the upgrade is refused with a report and nothing changes.
func TestMerchantRecordUpgradeRefusesCollisions(t *testing.T) {
	known, err := EmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join("testdata", "fixtures", "0029_deferral_fee_paid.sql"))
	if err != nil {
		t.Fatal(err)
	}
	path := tempPath(t)
	raw := rawDB(t, path)
	mustExec(t, raw, string(script))
	mustExec(t, raw, "INSERT INTO category_assignments (object_type, object_id, category_id, origin, source, set_by_kind, set_by_via, set_at) VALUES ('merchant', 'café example', NULL, 'user', '', 'system', 'system', '2026-02-06T00:00:00.000000000Z')")
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(context.Background(), path, Options{Migrations: known})
	if err == nil {
		_ = db.Close()
		t.Fatal("the upgrade succeeded despite colliding merchant keys")
	}
	if !strings.Contains(err.Error(), "migration 0030 refused: 1 merchant category assignments") {
		t.Errorf("error = %v, want the refusal report", err)
	}
	check := rawDB(t, path)
	if v := queryInt(t, check, "SELECT max(version) FROM schema_migrations"); v != 29 {
		t.Errorf("schema version after the refusal = %d, want 29", v)
	}
	if n := queryInt(t, check, "SELECT count(*) FROM sqlite_master WHERE name = 'merchants'"); n != 0 {
		t.Error("the refused upgrade left a merchants table")
	}
	if n := queryInt(t, check, "SELECT count(*) FROM category_assignments WHERE object_type = 'merchant'"); n != 4 {
		t.Errorf("%d merchant assignments after the refusal, want the 4 of the fixture", n)
	}
}
