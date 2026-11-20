package outbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/store"
)

var fixedNow = time.Date(2026, 11, 20, 10, 0, 0, 123456789, time.UTC)

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry(Definition{
		Entity: "widget", Action: "created", Trigger: "test trigger", Key: KeyULID,
		Fields: []Field{
			{Name: "widget_id", Kind: KindString, Required: true},
			{Name: "count", Kind: KindInt},
			{Name: "active", Kind: KindBool},
		},
	})
	return r
}

func setup(t *testing.T) (*store.DB, *Outbox) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	n := 0
	o, err := New(Config{
		Enabled:  true,
		Registry: testRegistry(t), Source: "ledger-test",
		Now:   func() time.Time { return fixedNow },
		NewID: func() string { n++; return fmt.Sprintf("01JD%022d", n) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, o
}

func countRows(t *testing.T, db *store.DB) int {
	t.Helper()
	var n int
	err := db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM outbox").Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEnqueueCommitPersistsEnvelope(t *testing.T) {
	db, o := setup(t)
	ctx := context.Background()
	var got Enqueued
	err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		got, err = o.Enqueue(ctx, tx, Event{Entity: "widget", Action: "created", Key: "01jd0000000000000000000abc", Payload: map[string]any{"widget_id": "abc-1", "count": 3, "active": true}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "evt.ledger.widget.created.01jd0000000000000000000abc" || got.Type != "ledger.widget.created.v1" || len(got.ID) != 26 {
		t.Fatalf("unexpected result %+v", got)
	}
	var subject, typ, source, occurred, payload, state string
	var attempts int
	err = db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT subject, type, source, occurred_at, payload, state, attempts FROM outbox WHERE id = ?", got.ID).
			Scan(&subject, &typ, &source, &occurred, &payload, &state, &attempts)
	})
	if err != nil {
		t.Fatal(err)
	}
	if subject != got.Subject || typ != got.Type || source != "ledger-test" || occurred != "2026-11-20T10:00:00.123456789Z" || state != StatePending || attempts != 0 {
		t.Fatalf("row = %q %q %q %q %q %d", subject, typ, source, occurred, state, attempts)
	}
	if payload != `{"active":true,"count":3,"widget_id":"abc-1"}` {
		t.Fatalf("payload = %s", payload)
	}
}

// Acceptance criterion: a rollback leaves no outbox row.
func TestEnqueueRollsBackWithTransaction(t *testing.T) {
	db, o := setup(t)
	ctx := context.Background()
	boom := errors.New("state change failed")
	err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := o.Enqueue(ctx, tx, Event{Entity: "widget", Action: "created", Payload: map[string]any{"widget_id": "a"}}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if n := countRows(t, db); n != 0 {
		t.Fatalf("outbox has %d rows after rollback, want 0", n)
	}
}

func TestEnqueueRollsBackOnPanic(t *testing.T) {
	db, o := setup(t)
	func() {
		defer func() { _ = recover() }()
		_ = db.WriteTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			_, _ = o.Enqueue(ctx, tx, Event{Entity: "widget", Action: "created", Payload: map[string]any{"widget_id": "a"}})
			panic("boom")
		})
	}()
	if n := countRows(t, db); n != 0 {
		t.Fatalf("outbox has %d rows after panic, want 0", n)
	}
}

func TestEnqueueRejectsInvalidEvents(t *testing.T) {
	db, o := setup(t)
	good := map[string]any{"widget_id": "a"}
	cases := map[string]Event{
		"unregistered":     {Entity: "gadget", Action: "created", Payload: good},
		"uppercase entity": {Entity: "Widget", Action: "created", Payload: good},
		"dotted entity":    {Entity: "wid.get", Action: "created", Payload: good},
		"uppercase key":    {Entity: "widget", Action: "created", Key: "ABC", Payload: good},
		"wildcard key":     {Entity: "widget", Action: "created", Key: "*", Payload: good},
		"empty key token":  {Entity: "widget", Action: "created", Key: "a..b", Payload: good},
		"too long key":     {Entity: "widget", Action: "created", Key: strings.Repeat("a", 300), Payload: good},
		"undeclared field": {Entity: "widget", Action: "created", Payload: map[string]any{"widget_id": "a", "extra": "x"}},
		"missing required": {Entity: "widget", Action: "created", Payload: map[string]any{"count": 1}},
		"wrong kind":       {Entity: "widget", Action: "created", Payload: map[string]any{"widget_id": 5}},
		"float":            {Entity: "widget", Action: "created", Payload: map[string]any{"widget_id": "a", "count": 1.5}},
		"oversized string": {Entity: "widget", Action: "created", Payload: map[string]any{"widget_id": strings.Repeat("x", MaxStringBytes+1)}},
	}
	for name, ev := range cases {
		err := db.WriteTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			_, err := o.Enqueue(ctx, tx, ev)
			return err
		})
		if err == nil {
			t.Errorf("%s: Enqueue succeeded, want an error", name)
		}
	}
	if n := countRows(t, db); n != 0 {
		t.Fatalf("%d rows written by rejected events", n)
	}
}

func TestSubjectValidation(t *testing.T) {
	valid := []string{"evt.ledger.widget.created", "evt.ledger.widget.created.abc", "evt.a.b.c.d.e", "evt.l-1.w_2.c3.k", "evt." + strings.Repeat("a", 100) + ".b.c"}
	for _, s := range valid {
		if err := ValidateSubject(s); err != nil {
			t.Errorf("ValidateSubject(%q) = %v", s, err)
		}
	}
	invalid := []string{
		"", "evt", "evt.", "evt.a", "evt.a.b", "ledger.widget.created.x", "EVT.ledger.widget.created",
		"evt.ledger.Widget.created", "evt.ledger.widget.*", "evt.ledger.widget.>", "evt.ledger..created",
		"evt.ledger.widget.created.", "evt.ledger.wid get.created", "evt.ledger.widget.créé",
		"evt.ledger.widget.created.\n", "evt." + strings.Repeat("a", 253) + ".b.c",
	}
	for _, s := range invalid {
		if err := ValidateSubject(s); err == nil {
			t.Errorf("ValidateSubject(%q) succeeded, want an error", s)
		}
	}
	// exactly 256 bytes is allowed
	s := "evt.ledger.widget." + strings.Repeat("k", 256-len("evt.ledger.widget."))
	if len(s) != 256 || ValidateSubject(s) != nil {
		t.Errorf("a 256 byte subject should be valid")
	}
	if err := ValidateSubject(s + "k"); err == nil {
		t.Errorf("a 257 byte subject should be invalid")
	}
}

func TestTypeValidation(t *testing.T) {
	for _, s := range []string{"ledger.widget.created.v1", "ledger.widget.created.v12", "a.b-c.d_e.v2"} {
		if err := ValidateType(s); err != nil {
			t.Errorf("ValidateType(%q) = %v", s, err)
		}
	}
	for _, s := range []string{"", "ledger.widget.created", "ledger.widget.created.v", "ledger.widget.created.v0", "ledger.widget.created.v01",
		"ledger.widget.created.1", "ledger.widget.created.V1", "ledger.widget.created.v1x", "ledger.widget.created.v1.extra",
		"Ledger.widget.created.v1", "ledger..created.v1", "evt.ledger.widget.created.v1"} {
		if err := ValidateType(s); err == nil {
			t.Errorf("ValidateType(%q) succeeded, want an error", s)
		}
	}
}

func TestDefinitionAllowListRejectsSensitiveFields(t *testing.T) {
	for _, name := range []string{"iban", "account_iban", "password", "api_token", "secret_ref", "raw_payload", "description", "remittance_info", "api_key", "account_number", "counterparty_name", "totp-secret",
		// PU1 re-review N1: free text, holders and cards named with a prefix.
		"user_note", "free_text", "freetext", "holder_name", "account_holder_name", "ultimate_debtor_name", "card_number_masked", "card_last4", "account_no", "name", "message", "note", "debtor_name", "creditor_name", "card_number"} {
		d := Definition{Entity: "widget", Action: "created", Trigger: "t", Fields: []Field{{Name: name, Kind: KindString}}}
		if err := NewRegistry().Register(d); err == nil {
			t.Errorf("field %q was accepted, want rejection", name)
		}
	}
	bad := []Definition{
		{Entity: "Widget", Action: "created", Trigger: "t"},
		{Entity: "widget", Action: "created"}, // no trigger
		{Entity: "widget", Action: "created", Trigger: "t", Fields: []Field{{Name: "Bad", Kind: KindString}}},
		{Entity: "widget", Action: "created", Trigger: "t", Fields: []Field{{Name: "a", Kind: "float"}}},
		{Entity: "widget", Action: "created", Trigger: "t", Fields: []Field{{Name: "a", Kind: KindInt}, {Name: "a", Kind: KindInt}}},
		{Entity: "widget", Action: "created", Trigger: "t", Version: -1},
	}
	for i, d := range bad {
		if err := NewRegistry().Register(d); err == nil {
			t.Errorf("bad definition %d accepted", i)
		}
	}
	r := testRegistry(t)
	if err := r.Register(Definition{Entity: "widget", Action: "created", Trigger: "t"}); err == nil {
		t.Error("duplicate registration accepted")
	}
}

func TestNewValidatesConfig(t *testing.T) {
	if _, err := New(Config{Source: "x"}); err == nil {
		t.Error("nil registry accepted")
	}
	for _, src := range []string{"", "  ", "a\nb", strings.Repeat("a", 129)} {
		if _, err := New(Config{Registry: NewRegistry(), Source: src}); err == nil {
			t.Errorf("source %q accepted", src)
		}
	}
}

func TestEnvelopeIsImmutableAndPendingRowsSurvive(t *testing.T) {
	db, o := setup(t)
	ctx := context.Background()
	var id string
	err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		e, err := o.Enqueue(ctx, tx, Event{Entity: "widget", Action: "created", Payload: map[string]any{"widget_id": "a"}})
		id = e.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) error {
		return db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		})
	}
	for _, q := range []string{
		"UPDATE outbox SET payload = '{}'",
		"UPDATE outbox SET subject = 'evt.ledger.x.y'",
		"UPDATE outbox SET occurred_at = created_at",
		"DELETE FROM outbox",
	} {
		if err := exec(q); err == nil {
			t.Errorf("%q succeeded, want the trigger to abort it", q)
		}
	}
	// A direct insert that bypasses validation still hits the column checks.
	if err := exec("INSERT INTO outbox (id, subject, type, source, occurred_at, payload, next_attempt_at, created_at) VALUES ('x','nope','t.v1','s','2026','{}','2026','2026')"); err == nil {
		t.Error("malformed direct insert succeeded")
	}
	// Status columns may change; a terminal state is final.
	at := store.Timestamp{Time: fixedNow}
	if err := exec("UPDATE outbox SET state='published', published_at=?, attempts=1 WHERE id=?", at, id); err != nil {
		t.Fatalf("mark published: %v", err)
	}
	if err := exec("UPDATE outbox SET state='failed', failed_at=? WHERE id=?", at, id); err == nil {
		t.Error("a published row was moved to failed")
	}
	if err := exec("UPDATE outbox SET state='published', published_at=NULL WHERE id=?", id); err == nil {
		t.Error("published without published_at accepted")
	}
}

func TestPurgeRetention(t *testing.T) {
	db, o := setup(t)
	ctx := context.Background()
	now := fixedNow
	ids := map[string]string{}
	err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for i, n := range []string{"pub-old", "pub-new", "fail-old", "fail-new", "pending-old"} {
			e, err := o.Enqueue(ctx, tx, Event{Entity: "widget", Action: "created", Key: fmt.Sprintf("01jd%022d", i), Payload: map[string]any{"widget_id": n}})
			if err != nil {
				return err
			}
			ids[n] = e.ID
		}
		pub := func(n string, at time.Time) error {
			_, err := tx.ExecContext(ctx, "UPDATE outbox SET state='published', published_at=? WHERE id=?", store.Timestamp{Time: at}, ids[n])
			return err
		}
		fail := func(n string, at time.Time) error {
			_, err := tx.ExecContext(ctx, "UPDATE outbox SET state='failed', failed_at=?, last_error='gave up' WHERE id=?", store.Timestamp{Time: at}, ids[n])
			return err
		}
		for _, e := range []error{
			pub("pub-old", now.Add(-PublishedRetention-time.Second)),
			pub("pub-new", now.Add(-PublishedRetention+time.Second)),
			fail("fail-old", now.Add(-FailedRetention-time.Second)),
			// older than the published window but inside the failed window
			fail("fail-new", now.Add(-PublishedRetention-time.Hour)),
		} {
			if e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var res PurgeResult
	err = db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		res, err = Purge(ctx, tx, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Published != 1 || res.Failed != 1 {
		t.Fatalf("purged %+v, want 1 published and 1 failed", res)
	}
	left := map[string]bool{}
	_ = db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM outbox")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			left[id] = true
		}
		return rows.Err()
	})
	for n, id := range ids {
		want := n == "pub-new" || n == "fail-new" || n == "pending-old"
		if left[id] != want {
			t.Errorf("%s present = %v, want %v", n, left[id], want)
		}
	}
}

func TestDefinitionAllowsOrdinaryFinanceNames(t *testing.T) {
	for _, name := range []string{"credit_limit", "credit_debit", "withdrawal_count", "company_id", "expanded", "discarded", "context_id", "merchant_name", "category_name", "error_message", "textual_id", "cardinality", "notebook_id", "currency", "amount", "account_id"} {
		d := Definition{Entity: "widget", Action: "created", Trigger: "t", Fields: []Field{{Name: name, Kind: KindString}}}
		if err := NewRegistry().Register(d); err != nil {
			t.Errorf("field %q rejected: %v", name, err)
		}
	}
}

func TestSubjectKeyFollowsDeclaredShape(t *testing.T) {
	db, o := setup(t)
	ctx := context.Background()
	enq := func(entity, key string) error {
		return db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := o.Enqueue(ctx, tx, Event{Entity: entity, Action: "created", Key: key, Payload: map[string]any{"widget_id": "a"}})
			return err
		})
	}
	if err := enq("widget", "01jd0000000000000000000001"); err != nil {
		t.Fatalf("valid ULID key: %v", err)
	}
	if err := enq("widget", ""); err != nil {
		t.Fatalf("a declared key is optional: %v", err)
	}
	for _, k := range []string{"lt601010012345678901", "lt60.1010", "01JD0000000000000000000001", "01jd000000000000000000000", "01jd00000000000000000000i1", "81jd0000000000000000000001", "*", ">"} {
		if err := enq("widget", k); err == nil {
			t.Errorf("key %q accepted, want rejection", k)
		}
	}
	// An event that declares no key rejects any key.
	reg := NewRegistry(Definition{Entity: "plain", Action: "created", Trigger: "t"})
	o2, err := New(Config{Enabled: true, Registry: reg, Source: "x"})
	if err != nil {
		t.Fatal(err)
	}
	err = db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := o2.Enqueue(ctx, tx, Event{Entity: "plain", Action: "created", Key: "01jd0000000000000000000001"})
		return err
	})
	if err == nil {
		t.Error("a key on a keyless event was accepted")
	}
	if n := countRows(t, db); n != 2 {
		t.Errorf("%d rows, want 2", n)
	}
}

func TestDisabledOutboxRecordsNothing(t *testing.T) {
	db, _ := setup(t)
	ctx := context.Background()
	o, err := New(Config{Enabled: false, Registry: testRegistry(t), Source: "x"})
	if err != nil {
		t.Fatal(err)
	}
	var got Enqueued
	err = db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		got, err = o.Enqueue(ctx, tx, Event{Entity: "widget", Action: "created", Payload: map[string]any{"widget_id": "a"}})
		return err
	})
	if err != nil || got.Recorded {
		t.Fatalf("disabled enqueue: recorded=%v err=%v", got.Recorded, err)
	}
	if n := countRows(t, db); n != 0 {
		t.Errorf("%d rows while disabled, want 0", n)
	}
	// Mistakes are still caught while disabled.
	for _, ev := range []Event{
		{Entity: "nope", Action: "created"},
		{Entity: "widget", Action: "created", Payload: map[string]any{"widget_id": "a", "iban": "x"}},
		{Entity: "widget", Action: "created", Key: "lt60", Payload: map[string]any{"widget_id": "a"}},
	} {
		_ = db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			if _, err := o.Enqueue(ctx, tx, ev); err == nil {
				t.Errorf("invalid event %+v accepted while disabled", ev)
			}
			return nil
		})
	}
	// Enabling later records only new changes: nothing is replayed.
	on, _ := New(Config{Enabled: true, Registry: testRegistry(t), Source: "x"})
	if n := countRows(t, db); n != 0 {
		t.Fatalf("%d rows after enabling, want 0", n)
	}
	err = db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		got, err = on.Enqueue(ctx, tx, Event{Entity: "widget", Action: "created", Payload: map[string]any{"widget_id": "a"}})
		return err
	})
	if err != nil || !got.Recorded || countRows(t, db) != 1 {
		t.Fatalf("enabled enqueue: %+v %v", got, err)
	}
}

func TestTerminalRowsAreImmutable(t *testing.T) {
	db, o := setup(t)
	ctx := context.Background()
	var id string
	err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		e, err := o.Enqueue(ctx, tx, Event{Entity: "widget", Action: "created", Payload: map[string]any{"widget_id": "a"}})
		id = e.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) error {
		return db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		})
	}
	at := store.Timestamp{Time: fixedNow}
	later := store.Timestamp{Time: fixedNow.AddDate(1, 0, 0)}
	// A pending row's status columns may change freely.
	if err := exec("UPDATE outbox SET attempts=2, last_error='timeout', next_attempt_at=? WHERE id=?", later, id); err != nil {
		t.Fatalf("pending update: %v", err)
	}
	if err := exec("UPDATE outbox SET state='published', published_at=? WHERE id=?", at, id); err != nil {
		t.Fatal(err)
	}
	for q, args := range map[string][]any{
		"UPDATE outbox SET published_at=? WHERE id=?":    {later, id},
		"UPDATE outbox SET attempts=99 WHERE id=?":       {id},
		"UPDATE outbox SET last_error='x' WHERE id=?":    {id},
		"UPDATE outbox SET next_attempt_at=? WHERE id=?": {later, id},
	} {
		if err := exec(q, args...); err == nil {
			t.Errorf("%q succeeded on a published row", q)
		}
	}
	// Same for a failed row.
	var fid string
	_ = db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		e, err := o.Enqueue(ctx, tx, Event{Entity: "widget", Action: "created", Payload: map[string]any{"widget_id": "b"}})
		fid = e.ID
		return err
	})
	if err := exec("UPDATE outbox SET state='failed', failed_at=?, last_error='gave up' WHERE id=?", at, fid); err != nil {
		t.Fatal(err)
	}
	if err := exec("UPDATE outbox SET failed_at=? WHERE id=?", later, fid); err == nil {
		t.Error("failed_at of a failed row was rewritten")
	}
	if err := exec("UPDATE outbox SET last_error='' WHERE id=?", fid); err == nil {
		t.Error("last_error of a failed row was rewritten")
	}
}

func TestColumnChecksRejectMalformedSubjectAndType(t *testing.T) {
	db, _ := setup(t)
	ts := "2026-11-20T10:00:00.123456789Z"
	for _, c := range [][2]string{
		{"EVT.ledger.a.b", "ledger.a.b.v1"},
		{"evt.other.a.b", "ledger.a.b.v1"},
		{"evt.ledger.a.b", "ledger.a.b"},
		{"evt.ledger.a.b", "ledger.a.b.v0"},
	} {
		err := db.WriteTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO outbox (id, subject, type, source, occurred_at, payload, next_attempt_at, created_at) VALUES ('01JD00000000000000000000AA', ?, ?, 's', ?, '{}', ?, ?)", c[0], c[1], ts, ts, ts)
			return err
		})
		if err == nil {
			t.Errorf("insert %v succeeded, want a CHECK failure", c)
		}
	}
}
