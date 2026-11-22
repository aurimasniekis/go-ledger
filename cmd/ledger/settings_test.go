package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	domsettings "github.com/aurimasniekis/go-ledger/internal/domain/settings"
	"github.com/aurimasniekis/go-ledger/internal/reporting"
	"github.com/aurimasniekis/go-ledger/internal/store"
	settingsstore "github.com/aurimasniekis/go-ledger/internal/store/settings"
)

// withDB opens the database of a stopped server for a test to change.
func withDB(t *testing.T, path string, fn func(db *store.DB)) {
	t.Helper()
	db, err := store.Open(context.Background(), path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	fn(db)
}

// ledger serve applies the stored settings layer at startup (ADR-0066 point
// 10), under the configuration's pins, and refuses an invalid stored layer.
func TestServeAppliesStoredSettings(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ledger.db")
	startServe(t, dbPath, nil, hostIs("UTC")).stop(t)

	withDB(t, dbPath, func(db *store.DB) {
		st := settingsstore.New(db, store.NewAuditLog(db), nil, nil)
		_, err := st.Write(context.Background(), "mcp", 1, func(domsettings.Stored) (domsettings.Change, error) {
			return domsettings.Change{Values: map[string]json.RawMessage{"enabled": json.RawMessage("false")},
				Event: &audit.Event{Action: audit.SettingsUpdated, Actor: audit.System, Details: map[string]string{"group": "mcp", "fields": "enabled"}}}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	s := startServe(t, dbPath, nil, hostIs("UTC"))
	s.stop(t)
	if logs := s.logs.String(); !strings.Contains(logs, "MCP endpoint disabled") {
		t.Errorf("the stored mcp.enabled=false was not applied:\n%s", logs)
	}
	// The environment pins the key: the stored value is kept but not used.
	s = startServe(t, dbPath, map[string]string{"LEDGER_MCP_ENABLED": "true"}, hostIs("UTC"))
	s.stop(t)
	if logs := s.logs.String(); !strings.Contains(logs, "MCP endpoint enabled") {
		t.Errorf("the pinned mcp.enabled did not win:\n%s", logs)
	}

	// An invalid stored value stops startup, naming the key.
	withDB(t, dbPath, func(db *store.DB) {
		err := db.WriteTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE setting_groups SET stored = '{"burst":0}' WHERE name = 'mcp'`)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	logs := &syncBuffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env := map[string]string{"LEDGER_KEK_VALUE": cmdSentinel, "LEDGER_DATABASE_PATH": dbPath}
	code := runServe(ctx, []string{"--http.listen=127.0.0.1:0", "--http.management_listen="}, logs, serveEnv{
		Options: testOpts(env, nil), Reporting: reporting.Options{HostZoneName: hostIs("UTC")},
	})
	if code != 1 || !strings.Contains(logs.String(), "invalid stored settings") || !strings.Contains(logs.String(), "mcp.burst") {
		t.Errorf("exit %d, logs:\n%s", code, logs.String())
	}
	if strings.Contains(logs.String(), cmdSentinel) {
		t.Error("the logs hold the key-encryption key")
	}
}
