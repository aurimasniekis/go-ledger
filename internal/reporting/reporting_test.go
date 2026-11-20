package reporting

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/store"
)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func host(name string, ok bool) Options {
	return Options{HostZoneName: func() (string, bool) { return name, ok }, Now: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }}
}

func stored(t *testing.T, db *store.DB) string {
	t.Helper()
	var v string
	err := db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", SettingKey).Scan(&v)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func logger() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}

func TestCaptureFromHostOnFirstStart(t *testing.T) {
	db := openDB(t)
	log, _ := logger()
	s, err := Resolve(context.Background(), db, "", log, host("Europe/Vilnius", true))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "Europe/Vilnius" || s.Source() != SourceHost || stored(t, db) != "Europe/Vilnius" {
		t.Fatalf("got %s %s stored %q", s.Name(), s.Source(), stored(t, db))
	}
}

func TestCaptureOnceThenStoredWins(t *testing.T) {
	db := openDB(t)
	log, _ := logger()
	if _, err := Resolve(context.Background(), db, "", log, host("Europe/Vilnius", true)); err != nil {
		t.Fatal(err)
	}
	// The host moves; the stored zone must not follow it.
	s, err := Resolve(context.Background(), db, "", log, host("America/New_York", true))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "Europe/Vilnius" || s.Source() != SourceStored {
		t.Fatalf("got %s %s", s.Name(), s.Source())
	}
	// Once stored, an unresolvable host no longer matters.
	if _, err := Resolve(context.Background(), db, "", log, host("", false)); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitFirstStartBeatsHost(t *testing.T) {
	db := openDB(t)
	log, buf := logger()
	s, err := Resolve(context.Background(), db, "Asia/Tokyo", log, host("Europe/Vilnius", true))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "Asia/Tokyo" || s.Source() != SourceConfig || stored(t, db) != "Asia/Tokyo" {
		t.Fatalf("got %s %s", s.Name(), s.Source())
	}
	if strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("first capture must not warn: %s", buf)
	}
}

func TestExplicitChangeWarnsAndIsStored(t *testing.T) {
	db := openDB(t)
	log, buf := logger()
	if _, err := Resolve(context.Background(), db, "Europe/Vilnius", log, host("", false)); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	s, err := Resolve(context.Background(), db, "UTC", log, host("", false))
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if s.Name() != "UTC" || s.Source() != SourceConfigChanged || stored(t, db) != "UTC" {
		t.Fatalf("got %s %s stored %q", s.Name(), s.Source(), stored(t, db))
	}
	for _, want := range []string{"level=WARN", "reporting_timezone_changed", "from=Europe/Vilnius", "to=UTC"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q: %s", want, out)
		}
	}
	// Later, an empty config uses the new stored zone without another warning.
	buf.Reset()
	s, err = Resolve(context.Background(), db, "", log, host("", false))
	if err != nil || s.Name() != "UTC" || strings.Contains(buf.String(), "WARN") {
		t.Fatalf("got %v %v %s", s, err, buf)
	}
}

func TestExplicitSameIsQuiet(t *testing.T) {
	db := openDB(t)
	log, buf := logger()
	for i := 0; i < 2; i++ {
		if _, err := Resolve(context.Background(), db, "UTC", log, host("", false)); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(buf.String(), "WARN") {
		t.Fatal(buf.String())
	}
}

func TestUnresolvableIsRefusedAndNothingStored(t *testing.T) {
	db := openDB(t)
	log, _ := logger()
	_, err := Resolve(context.Background(), db, "", log, host("", false))
	if !errors.Is(err, ErrNoZone) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "LEDGER_REPORTING_TIMEZONE") {
		t.Errorf("message should name the setting: %v", err)
	}
	if stored(t, db) != "" {
		t.Fatal("nothing may be stored")
	}
}

func TestLocalAndUnknownRejected(t *testing.T) {
	db := openDB(t)
	log, _ := logger()
	for _, v := range []string{"Local", "Nowhere/Land"} {
		if _, err := Resolve(context.Background(), db, v, log, host("UTC", true)); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
	if stored(t, db) != "" {
		t.Fatal("nothing may be stored")
	}
}

func TestUnloadableStoredZoneIsNotReplacedSilently(t *testing.T) {
	db := openDB(t)
	log, _ := logger()
	err := db.WriteTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO settings (key, value, updated_at) VALUES (?, 'Bad/Zone', ?)", SettingKey, store.Timestamp{Time: time.Now()})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(context.Background(), db, "", log, host("UTC", true)); err == nil {
		t.Fatal("want an error")
	}
	if stored(t, db) != "Bad/Zone" {
		t.Fatal("stored value must be untouched")
	}
	// An explicit value is the operator's way out.
	if s, err := Resolve(context.Background(), db, "UTC", log, host("", false)); err != nil || s.Name() != "UTC" {
		t.Fatalf("got %v %v", s, err)
	}
}
