package commitments

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

type recorder struct {
	events []audit.Event
	fail   error
}

func (r *recorder) WriteAudit(_ context.Context, tx *sql.Tx, e audit.Event) error {
	if tx == nil {
		return errors.New("no transaction")
	}
	if r.fail != nil {
		return r.fail
	}
	r.events = append(r.events, e)
	return nil
}

func (r *recorder) actions() string {
	var out []string
	for _, e := range r.events {
		out = append(out, string(e.Action))
	}
	return strings.Join(out, ",")
}

var t0 = time.Date(2026, 3, 4, 5, 6, 7, 8, time.UTC)

func newStore(t *testing.T) (*Store, *store.DB, *recorder) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rec := &recorder{}
	return New(db, rec, func() time.Time { return t0 }, nil), db, rec
}

// eff is the effective date passed to updates (ADR-0027).
var eff = calendar.MustDate(2026, time.March, 4)

func ptr[T any](v T) *T { return &v }

var alice = audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNA"}

func vps() commitments.Fields {
	return commitments.Fields{Name: "VPS", Kind: commitments.KindServer, Amount: money.MustParse("12.50", "EUR"),
		Schedule: commitments.Monthly(1, 5), StartDate: calendar.MustDate(2026, time.January, 5)}
}

func domainRenewal() commitments.Fields {
	return commitments.Fields{Name: "example.com", Kind: commitments.KindDomain, Amount: money.MustParse("15.000", "USD"),
		Schedule: commitments.Annual(time.February, 29), StartDate: calendar.MustDate(2024, time.February, 29),
		EndDate: ptr(calendar.MustDate(2030, time.December, 31)), Priority: ptr(commitments.PriorityEssential), Notes: ptr("auto-renew off")}
}

func TestCreateGetList(t *testing.T) {
	ctx := context.Background()
	st, _, rec := newStore(t)
	a, err := st.Create(ctx, alice, vps())
	if err != nil {
		t.Fatal(err)
	}
	if !resource.ValidID(a.ID) || a.Version != 1 || !a.CreatedAt.Equal(t0) || !a.UpdatedAt.Equal(t0) {
		t.Fatalf("created %+v", a)
	}
	b, err := st.Create(ctx, alice, domainRenewal())
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Exact amount and scale, schedule, dates and optional members survive the round trip.
	want := domainRenewal()
	if !got.Amount.Identical(want.Amount) || got.Schedule != want.Schedule || got.StartDate != want.StartDate ||
		*got.EndDate != *want.EndDate || *got.Priority != *want.Priority || *got.Notes != *want.Notes || got.Name != want.Name || got.Kind != want.Kind {
		t.Fatalf("get %+v", got)
	}
	if rec.actions() != "commitment.created,commitment.created" || rec.events[1].Details["currency"] != "USD" || rec.events[1].TargetID != b.ID {
		t.Fatalf("audit %+v", rec.events)
	}
	if _, err := st.Get(ctx, "01JD3Y4C8V9M2Q7W5R6T0XZBNZ"); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	all, err := st.List(ctx, commitments.ListFilter{Limit: 10})
	if err != nil || len(all) != 2 || all[0].ID > all[1].ID {
		t.Fatalf("list %v %v", all, err)
	}
	for _, c := range []struct {
		f    commitments.ListFilter
		want int
	}{
		{commitments.ListFilter{Limit: 10, Kinds: []commitments.Kind{commitments.KindDomain}}, 1},
		{commitments.ListFilter{Limit: 10, Kinds: []commitments.Kind{commitments.KindDomain, commitments.KindServer}}, 2},
		{commitments.ListFilter{Limit: 10, Priorities: []commitments.Priority{commitments.PriorityEssential}}, 1},
		{commitments.ListFilter{Limit: 10, Priorities: []commitments.Priority{commitments.PriorityFlexible}}, 0},
		{commitments.ListFilter{Limit: 10, Currencies: []money.Currency{money.MustParseCurrency("EUR")}}, 1},
		{commitments.ListFilter{Limit: 0}, 1}, // Limit+1 rows
		{commitments.ListFilter{Limit: 10, After: all[0].ID}, 1},
	} {
		got, err := st.List(ctx, c.f)
		if err != nil || len(got) != c.want {
			t.Errorf("list %+v: %d %v, want %d", c.f, len(got), err, c.want)
		}
	}
}

func TestCreateValidates(t *testing.T) {
	st, _, rec := newStore(t)
	f := vps()
	f.Name, f.Kind = "", "car"
	_, err := st.Create(context.Background(), alice, f)
	var ve *commitments.ValidationError
	if !errors.As(err, &ve) || len(ve.Errors) != 2 || ve.Errors[0].Field != "name" || ve.Errors[1].Field != "kind" {
		t.Fatalf("err %v", err)
	}
	if len(rec.events) != 0 {
		t.Fatalf("audited an invalid create: %v", rec.events)
	}
}

func TestUpdate(t *testing.T) {
	ctx := context.Background()
	st, _, rec := newStore(t)
	c, err := st.Create(ctx, alice, vps())
	if err != nil {
		t.Fatal(err)
	}
	f := c.Fields
	f.Amount = money.MustParse("14.00", "EUR")
	f.Schedule = commitments.Monthly(3, 31)
	f.Priority = ptr(commitments.PriorityImportant)
	u, err := st.Update(ctx, alice, c.ID, 1, f, eff, eff)
	if err != nil {
		t.Fatal(err)
	}
	if u.Version != 2 || !u.Amount.Identical(f.Amount) || u.Schedule != f.Schedule || *u.Priority != commitments.PriorityImportant || !u.CreatedAt.Equal(t0) {
		t.Fatalf("updated %+v", u)
	}
	// The terms changed, so their regeneration is audited too (ADR-0027).
	if rec.actions() != "commitment.created,commitment.updated,occurrences.regenerated" || rec.events[1].Details["fields"] != "amount,priority,schedule" ||
		rec.events[2].Details["effective_from"] != "2026-03-04" {
		t.Fatalf("audit %+v", rec.events)
	}
	// Stale version.
	var vc *resource.VersionConflictError
	if _, err := st.Update(ctx, alice, c.ID, 1, f, eff, eff); !errors.As(err, &vc) || vc.Current != 2 {
		t.Fatalf("stale: %v", err)
	}
	// No change: no write, no version bump, no audit.
	same, err := st.Update(ctx, alice, c.ID, 2, f, eff, eff)
	if err != nil || same.Version != 2 || len(rec.events) != 3 {
		t.Fatalf("no-op: %+v %v %v", same, err, rec.events)
	}
	// Clearing an optional member.
	f.Priority = nil
	if u, err := st.Update(ctx, alice, c.ID, 2, f, eff, eff); err != nil || u.Priority != nil || u.Version != 3 {
		t.Fatalf("clear: %+v %v", u, err)
	}
	// Invalid fields are refused before anything is read.
	f.EndDate = ptr(calendar.MustDate(2020, time.January, 1))
	var ve *commitments.ValidationError
	if _, err := st.Update(ctx, alice, c.ID, 3, f, eff, eff); !errors.As(err, &ve) || ve.Errors[0].Field != "end_date" {
		t.Fatalf("invalid: %v", err)
	}
	if _, err := st.Update(ctx, alice, "01JD3Y4C8V9M2Q7W5R6T0XZBNZ", 1, vps(), eff, eff); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	// A failing audit write rolls the change back.
	rec.fail = errors.New("audit down")
	f.EndDate = nil
	f.Name = "Renamed"
	if _, err := st.Update(ctx, alice, c.ID, 3, f, eff, eff); err == nil {
		t.Fatal("update succeeded without its audit entry")
	}
	if got, _ := st.Get(ctx, c.ID); got.Name != "VPS" || got.Version != 3 {
		t.Fatalf("not rolled back: %+v", got)
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	st, _, rec := newStore(t)
	c, err := st.Create(ctx, alice, vps())
	if err != nil {
		t.Fatal(err)
	}
	var vc *resource.VersionConflictError
	if err := st.Delete(ctx, alice, c.ID, 2); !errors.As(err, &vc) {
		t.Fatalf("stale: %v", err)
	}
	if err := st.Delete(ctx, alice, c.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(ctx, c.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("still there: %v", err)
	}
	if err := st.Delete(ctx, alice, c.ID, 1); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("twice: %v", err)
	}
	if rec.actions() != "commitment.created,commitment.deleted" || rec.events[1].Details["kind"] != "server" {
		t.Fatalf("audit %+v", rec.events)
	}
}

// TestDeleteBlockedByDependents shows the contract later tables rely on: a
// row that refers to a commitment (an occurrence with payment history, in
// task C2) blocks its delete, and the store reports ErrInUse.
func TestDeleteBlockedByDependents(t *testing.T) {
	ctx := context.Background()
	st, db, _ := newStore(t)
	c, err := st.Create(ctx, alice, vps())
	if err != nil {
		t.Fatal(err)
	}
	err = db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "CREATE TABLE dependent (id TEXT PRIMARY KEY, commitment_id TEXT NOT NULL REFERENCES commitments (id) ON DELETE RESTRICT) STRICT"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO dependent VALUES ('x', ?)", c.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(ctx, alice, c.ID, 1); !errors.Is(err, commitments.ErrInUse) {
		t.Fatalf("delete with dependents: %v", err)
	}
}

// TestSchemaKeepsDefinitionsApart checks that the commitments table holds
// the definition only (no occurrence, payment or bank columns) and that its
// CHECKs refuse what the domain refuses.
func TestSchemaKeepsDefinitionsApart(t *testing.T) {
	ctx := context.Background()
	_, db, _ := newStore(t)
	var cols []string
	err := db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT name FROM pragma_table_info('commitments') ORDER BY cid")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			cols = append(cols, n)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cols, ","); got != "id,name,kind,amount,currency,schedule,start_date,end_date,priority,notes,created_at,updated_at,version" {
		t.Fatalf("columns %s", got)
	}
	insert := func(amount, schedule, start, end, kind, priority string) error {
		return db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			var e, p any
			if end != "" {
				e = end
			}
			if priority != "" {
				p = priority
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO commitments (id, name, kind, amount, currency, schedule, start_date, end_date, priority, created_at, updated_at) VALUES (?, 'x', ?, ?, 'EUR', ?, ?, ?, ?, ?, ?)",
				resource.NewID(), kind, amount, schedule, start, e, p, store.Timestamp{Time: t0}, store.Timestamp{Time: t0})
			return err
		})
	}
	okSchedule := `{"version":1,"frequency":"monthly","interval":1,"day":1}`
	if err := insert("1.00", okSchedule, "2026-01-01", "", "service", ""); err != nil {
		t.Fatalf("valid row: %v", err)
	}
	for _, c := range []struct{ name, amount, schedule, start, end, kind, priority string }{
		{"negative amount", "-1.00", okSchedule, "2026-01-01", "", "service", ""},
		{"zero amount", "0.00", okSchedule, "2026-01-01", "", "service", ""},
		{"no schedule version", "1", `{"frequency":"monthly"}`, "2026-01-01", "", "service", ""},
		{"schedule not an object", "1", "[1]", "2026-01-01", "", "service", ""},
		{"bad date", "1", okSchedule, "2026-02-30", "", "service", ""},
		{"end before start", "1", okSchedule, "2026-02-01", "2026-01-31", "service", ""},
		{"unknown kind", "1", okSchedule, "2026-01-01", "", "car", ""},
		{"unknown priority", "1", okSchedule, "2026-01-01", "", "service", "urgent"},
	} {
		if err := insert(c.amount, c.schedule, c.start, c.end, c.kind, c.priority); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}
