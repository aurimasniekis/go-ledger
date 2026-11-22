package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/alerts"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	moneypkg "github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/outbox"
	"github.com/aurimasniekis/go-ledger/internal/store"
	alertstore "github.com/aurimasniekis/go-ledger/internal/store/alerts"
)

func alertTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type outboxRow struct{ subject, typ, payload string }

func outboxRows(t *testing.T, db *store.DB) []outboxRow {
	t.Helper()
	var out []outboxRow
	err := db.ReadTx(t.Context(), func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT subject, type, payload FROM outbox ORDER BY created_at, id")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r outboxRow
			if err := rows.Scan(&r.subject, &r.typ, &r.payload); err != nil {
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

func alertOutbox(t *testing.T, db *store.DB, enabled bool) *outbox.Outbox {
	t.Helper()
	ob, err := outbox.New(outbox.Config{Registry: outbox.NewRegistry(alertRaisedEvent), Enabled: enabled, Source: "ledger-test"})
	if err != nil {
		t.Fatal(err)
	}
	return ob
}

func TestPulseAlertChannelRecordsAnOutboxEventWhenEnabled(t *testing.T) {
	db := alertTestDB(t)
	st := alertstore.New(db, store.NewAuditLog(db), func() time.Time { return time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC) }, nil)
	svc := alerts.New(st, nil, pulseAlertChannel(db, alertOutbox(t, db, true)))
	today := calendar.MustDate(2026, 11, 22)
	cond, _ := alerts.UnmatchedOverdue("01JD3Y4C8V9M2Q7W5R6T0XZBNB", calendar.MustDate(2026, 10, 20), today, moneypkg.MustParse("12.50", "EUR"))
	res, err := svc.Raise(t.Context(), audit.System, cond)
	if err != nil || !res.Created || len(res.Delivery) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	rows := outboxRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("outbox rows %d", len(rows))
	}
	if want := "evt.ledger.alert.raised." + strings.ToLower(res.Alert.ID); rows[0].subject != want || rows[0].typ != "ledger.alert.raised.v1" {
		t.Fatalf("%+v want subject %s", rows[0], want)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(rows[0].payload), &p); err != nil {
		t.Fatal(err)
	}
	if len(p) != 3 || p["kind"] != "unmatched_overdue" || p["object_type"] != "occurrence" || p["object_id"] != "01JD3Y4C8V9M2Q7W5R6T0XZBNB" {
		t.Fatalf("payload %v", p)
	}
	// Neither the message nor an amount leaves the machine.
	if strings.Contains(rows[0].payload, "12.50") || strings.Contains(rows[0].payload, "Overdue") {
		t.Fatalf("payload carries the alert's text: %s", rows[0].payload)
	}

	// Raised again (open) or after a dismissal: nothing more is sent.
	if _, err := svc.Raise(t.Context(), audit.System, cond); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Dismiss(t.Context(), audit.System, res.Alert.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Raise(t.Context(), audit.System, cond); err != nil {
		t.Fatal(err)
	}
	if n := len(outboxRows(t, db)); n != 1 {
		t.Fatalf("outbox rows after repeats: %d", n)
	}
}

func TestPulseAlertChannelRecordsNothingWhenPulseIsOff(t *testing.T) {
	db := alertTestDB(t)
	st := alertstore.New(db, store.NewAuditLog(db), nil, nil)
	svc := alerts.New(st, nil, pulseAlertChannel(db, alertOutbox(t, db, false)))
	cond, _ := alerts.AmountChanged("01JD3Y4C8V9M2Q7W5R6T0XZBNC", "o1", moneypkg.MustParse("10.00", "EUR"), moneypkg.MustParse("12.00", "EUR"))
	if res, err := svc.Raise(t.Context(), audit.System, cond); err != nil || !res.Created || len(res.Delivery) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if n := len(outboxRows(t, db)); n != 0 {
		t.Fatalf("outbox rows %d while publishing is off", n)
	}
}
