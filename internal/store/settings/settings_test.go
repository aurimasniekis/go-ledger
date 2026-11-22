package settings_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/settings"
	"github.com/aurimasniekis/go-ledger/internal/store"
	settingsstore "github.com/aurimasniekis/go-ledger/internal/store/settings"
)

var t0 = time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func patch(kv ...string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = json.RawMessage(kv[i+1])
	}
	return out
}

type auditRow struct{ action, actor, targetType, targetID, details string }

func auditRows(t *testing.T, db *store.DB) []auditRow {
	t.Helper()
	var out []auditRow
	err := db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT action, actor_kind, target_type, target_id, details FROM audit_log ORDER BY id")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r auditRow
			if err := rows.Scan(&r.action, &r.actor, &r.targetType, &r.targetID, &r.details); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStoreVersionRules(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	st := settingsstore.New(db, store.NewAuditLog(db), func() time.Time { return t0 }, nil)
	ev := func() settings.Change {
		return settings.Change{Values: patch("level", `"warn"`), Event: &audit.Event{Action: audit.SettingsUpdated, Actor: audit.System,
			Details: map[string]string{"group": "logging", "fields": "level"}}}
	}
	decide := func(settings.Stored) (settings.Change, error) { return ev(), nil }
	// No row: version 1 is the only accepted one.
	if _, err := st.Write(ctx, "logging", 2, decide); !conflict(err, 2, 1) {
		t.Errorf("first write with version 2: %v", err)
	}
	row, err := st.Write(ctx, "logging", 1, decide)
	if err != nil || row.Version != 2 || !row.Exists || !row.CreatedAt.Equal(t0) {
		t.Fatalf("first write: %+v, %v", row, err)
	}
	// A second "first write" loses.
	if _, err := st.Write(ctx, "logging", 1, decide); !conflict(err, 1, 2) {
		t.Errorf("second first write: %v", err)
	}
	// No event: nothing changes, not even the version.
	row, err = st.Write(ctx, "logging", 2, func(cur settings.Stored) (settings.Change, error) { return settings.Change{Values: cur.Values}, nil })
	if err != nil || row.Version != 2 {
		t.Errorf("no-op write: %+v, %v", row, err)
	}
	// A refused decision or a refused audit event rolls back everything.
	boom := errors.New("refused")
	if _, err := st.Write(ctx, "logging", 2, func(settings.Stored) (settings.Change, error) { return settings.Change{}, boom }); !errors.Is(err, boom) {
		t.Errorf("refused decision: %v", err)
	}
	if _, err := st.Write(ctx, "logging", 2, func(settings.Stored) (settings.Change, error) {
		c := ev()
		c.Values = patch("level", `"debug"`)
		c.Event.Details["value"] = "debug" // not a declared detail field
		return c, nil
	}); err == nil {
		t.Error("an invalid audit event was accepted")
	}
	rows, _ := st.Groups(ctx)
	if len(rows) != 1 || rows[0].Version != 2 || string(rows[0].Values["level"]) != `"warn"` {
		t.Errorf("rows after refused writes = %+v", rows)
	}
	if n := len(auditRows(t, db)); n != 1 {
		t.Errorf("%d audit entries, want 1", n)
	}
	if _, err := st.Write(ctx, "Bad Name", 1, decide); err == nil {
		t.Error("an invalid group name was accepted")
	}
}

func conflict(err error, expected, current int64) bool {
	var vc *resource.VersionConflictError
	return errors.As(err, &vc) && vc.Expected == expected && vc.Current == current
}

// The table refuses what the service never writes: a name outside the
// pattern, a stored document that is not a JSON object.
func TestSettingGroupsConstraints(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	ts := store.FormatTime(t0)
	for _, tc := range []struct{ name, stored string }{
		{"Bad", "{}"}, {"a..b", "{}"}, {"a.b.c", "{}"}, {"a.", "{}"}, {"a.1", "{}"}, {"1a", "{}"}, {"mcp", "[]"}, {"mcp", "not json"},
	} {
		err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO setting_groups (id, name, stored, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
				resource.NewID(), tc.name, tc.stored, ts, ts)
			return err
		})
		if err == nil {
			t.Errorf("name %q stored %q was accepted", tc.name, tc.stored)
		}
	}
	for _, name := range []string{"mcp", "decision_tasks.categorisation"} {
		if !settings.NamePattern.MatchString(name) {
			t.Errorf("%s does not match the name pattern", name)
		}
		err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO setting_groups (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)", resource.NewID(), name, ts, ts)
			return err
		})
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
