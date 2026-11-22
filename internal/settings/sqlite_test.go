package settings_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	reg "github.com/aurimasniekis/go-ledger/internal/domain/settings"
	"github.com/aurimasniekis/go-ledger/internal/settings"
	"github.com/aurimasniekis/go-ledger/internal/store"
	settingsstore "github.com/aurimasniekis/go-ledger/internal/store/settings"
)

const sentinel = "SENTINEL-settings-store-0123456789"

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

func base(t *testing.T, e map[string]string) *config.Result {
	t.Helper()
	e["LEDGER_KEK_VALUE"] = sentinel + "-kek"
	res, err := config.Load(config.Options{
		LookupEnv: func(k string) (string, bool) { v, ok := e[k]; return v, ok },
		ReadFile: func(name string) ([]byte, error) {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		},
		Environ: func() []string { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
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

func dump(t *testing.T, db *store.DB) string {
	t.Helper()
	var b strings.Builder
	err := db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		for _, q := range []string{"SELECT name || stored FROM setting_groups", "SELECT action || details FROM audit_log", "SELECT key || value FROM settings"} {
			rows, err := tx.QueryContext(ctx, q)
			if err != nil {
				return err
			}
			for rows.Next() {
				var s string
				if err := rows.Scan(&s); err != nil {
					_ = rows.Close()
					return err
				}
				b.WriteString(s + "\n")
			}
			_ = rows.Close()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// The service over the SQLite store: the first write inserts the row with
// version 2, later writes bump it, a reopened service applies the stored
// layer, and the audit log records group and member names only.
func TestSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	st := settingsstore.New(db, store.NewAuditLog(db), func() time.Time { return t0 }, nil)
	svc, err := settings.Open(ctx, base(t, map[string]string{}), st, settings.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, audit.System, "mcp", 1, patch("burst", "5", "max_request_bytes", "4096")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, audit.System, "reporting", 1, patch("currency", `"EUR"`)); err != nil {
		t.Fatal(err)
	}
	snap, err := svc.Update(ctx, audit.System, "mcp", 2, patch("burst", "null", "requests_per_minute", "77"))
	if err != nil {
		t.Fatal(err)
	}
	if g := snap.Group("mcp"); g.Version != 3 || !g.UpdatedAt.Equal(t0) {
		t.Errorf("mcp row = %+v", g)
	}
	rows, err := st.Groups(ctx)
	if err != nil || len(rows) != 2 || rows[0].Name != "mcp" || rows[1].Name != "reporting" {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	if got := string(mustJSON(t, rows[0].Values)); got != `{"max_request_bytes":4096,"requests_per_minute":77}` {
		t.Errorf("stored mcp = %s", got)
	}
	got := auditRows(t, db)
	want := []auditRow{
		{"settings.updated", "system", "setting_group", "mcp", `{"fields":"burst,max_request_bytes","group":"mcp"}`},
		{"settings.updated", "system", "setting_group", "reporting", `{"fields":"currency","group":"reporting"}`},
		{"settings.updated", "system", "setting_group", "mcp", `{"fields":"burst,requests_per_minute","group":"mcp"}`},
	}
	if len(got) != len(want) {
		t.Fatalf("audit = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("audit[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	for _, r := range got {
		for _, v := range []string{"4096", "77", "EUR"} {
			if strings.Contains(r.details, v) {
				t.Errorf("audit details %s carry the value %s", r.details, v)
			}
		}
	}

	// A restart applies the stored layer.
	again, err := settings.Open(ctx, base(t, map[string]string{}), st, settings.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := again.Snapshot().Config()
	if c.MCP.RequestsPerMinute != 77 || c.MCP.MaxRequestBytes != 4096 || c.MCP.Burst != 30 || c.Reporting.Currency != "EUR" {
		t.Errorf("after restart: %+v, currency %q", c.MCP, c.Reporting.Currency)
	}
	if v, _ := again.View("mcp", nil); v.Version != 3 || v.Sources["requests_per_minute"] != "api" {
		t.Errorf("view after restart = %+v", v)
	}

	// Reset removes the stored members and bumps the version.
	snap, err = again.Reset(ctx, audit.System, "mcp", 3)
	if err != nil || snap.Group("mcp").Version != 4 || snap.Config().MCP.RequestsPerMinute != 120 {
		t.Fatalf("reset: %v, %+v", err, snap.Group("mcp"))
	}
	if last := auditRows(t, db); last[len(last)-1].action != "settings.reset" || last[len(last)-1].details != `{"fields":"max_request_bytes,requests_per_minute","group":"mcp"}` {
		t.Errorf("reset audit = %+v", last[len(last)-1])
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Sentinel: a secret sent in a write never reaches the database, the audit
// log or the error, and a secret in the environment never reaches a view.
func TestSecretsNeverReachStorage(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	st := settingsstore.New(db, store.NewAuditLog(db), func() time.Time { return t0 }, nil)
	svc, err := settings.Open(ctx, base(t, map[string]string{"LEDGER_AUTH_OIDC_CLIENT_SECRET": sentinel}), st, settings.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for group, member := range map[string]string{"oidc": "client_secret", "decision_providers.anthropic": "api_key", "decision_providers.openrouter": "api_key"} {
		_, err := svc.Update(ctx, audit.System, group, 1, patch(member, `"`+sentinel+`"`))
		if err == nil {
			t.Errorf("%s.%s: the write was accepted", group, member)
		} else if strings.Contains(err.Error(), sentinel) {
			t.Errorf("%s.%s: the error repeats the value: %v", group, member, err)
		}
	}
	if strings.Contains(dump(t, db), sentinel) {
		t.Error("the database holds the sentinel")
	}
	for _, g := range reg.Groups() {
		v, err := svc.View(g.Name, nil)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := json.Marshal(v); strings.Contains(string(b), sentinel) {
			t.Errorf("the view of %s holds the sentinel", g.Name)
		}
	}
}
