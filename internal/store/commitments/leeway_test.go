package commitments

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	"github.com/aurimasniekis/go-ledger/internal/logging"
)

func TestLeewayRoundTripKeepsUnknownDistinct(t *testing.T) {
	ctx := context.Background()
	st, _, rec := newStore(t)
	until := calendar.MustDate(2026, time.February, 20)
	days := 7
	src, cond := "contract 4.2", "if the bank is told"
	for name, l := range map[string]*commitments.Leeway{
		"none recorded": nil,
		"unknown":       {Kind: commitments.LeewayUnknown},
		"none":          {Kind: commitments.LeewayNone, Source: &src},
		"period":        {Kind: commitments.LeewayPeriod, Days: &days, Conditions: &cond, Source: &src},
		"deadline":      {Kind: commitments.LeewayDeadline, Until: &until},
	} {
		f := vps()
		f.Leeway = l
		c, err := st.Create(ctx, alice, f)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := st.Get(ctx, c.ID)
		if err != nil || !commitments.SameLeeway(got.Leeway, l) || (l == nil) != (got.Leeway == nil) {
			t.Errorf("%s: got %+v %v", name, got.Leeway, err)
		}
	}
	// Editing leeway is recorded as a change and does not touch occurrences.
	c, err := st.Create(ctx, alice, vps())
	if err != nil {
		t.Fatal(err)
	}
	rec.events = nil
	f := vps()
	f.Leeway = &commitments.Leeway{Kind: commitments.LeewayUnknown}
	got, err := st.Update(ctx, alice, c.ID, 1, f, eff, eff)
	if err != nil || got.Version != 2 || got.Leeway == nil || got.Leeway.Kind != commitments.LeewayUnknown {
		t.Fatalf("set unknown: %+v %v", got, err)
	}
	if len(rec.events) != 1 || rec.events[0].Action != "commitment.updated" {
		t.Errorf("a leeway edit records %+v, want only commitment.updated (no occurrences regenerated)", rec.events)
	}
	f.Leeway = nil
	if got, err = st.Update(ctx, alice, c.ID, 2, f, eff, eff); err != nil || got.Leeway != nil {
		t.Fatalf("clear: %+v %v", got.Leeway, err)
	}
}

func TestLeewaySchemaKeepsMembersWithTheKind(t *testing.T) {
	ctx := context.Background()
	st, db, _ := newStore(t)
	c, err := st.Create(ctx, alice, vps())
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string) error {
		return db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, q, c.ID)
			return err
		})
	}
	for q, want := range map[string]string{
		"UPDATE commitments SET leeway_kind = 'period' WHERE id = ?":                                "leeway members",
		"UPDATE commitments SET leeway_kind = 'period', leeway_days = 0 WHERE id = ?":               "CHECK",
		"UPDATE commitments SET leeway_kind = 'unknown', leeway_days = 3 WHERE id = ?":              "leeway members",
		"UPDATE commitments SET leeway_kind = 'deadline', leeway_until = '2026-02-30' WHERE id = ?": "CHECK",
		"UPDATE commitments SET leeway_kind = 'soon' WHERE id = ?":                                  "CHECK",
		"UPDATE commitments SET leeway_source = 'a letter' WHERE id = ?":                            "leeway members",
		"UPDATE commitments SET leeway_kind = 'none', leeway_source = '' WHERE id = ?":              "CHECK",
	} {
		if err := exec(q); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", q, err)
		}
	}
	if err := exec("UPDATE commitments SET leeway_kind = 'unknown' WHERE id = ?"); err != nil {
		t.Errorf("explicit unknown refused: %v", err)
	}
}

func TestGraceHookReadsOnlyWhatWasRecorded(t *testing.T) {
	ctx := context.Background()
	st, _, _ := newStore(t)
	days := 5
	mk := func(l *commitments.Leeway) occurrences.Occurrence {
		f := vps()
		f.Leeway = l
		c, err := st.Create(ctx, alice, f)
		if err != nil {
			t.Fatal(err)
		}
		return occurrences.Occurrence{CommitmentID: c.ID, DueDate: calendar.MustDate(2026, time.January, 5)}
	}
	grace := st.Grace()
	if g := grace(ctx, mk(nil)); g.Recorded || g.Until != nil || g.Kind != "" {
		t.Errorf("nothing recorded: %+v", g)
	}
	if g := grace(ctx, mk(&commitments.Leeway{Kind: commitments.LeewayUnknown})); !g.Recorded || g.Kind != commitments.LeewayUnknown || g.Until != nil {
		t.Errorf("unknown: %+v", g)
	}
	if g := grace(ctx, mk(&commitments.Leeway{Kind: commitments.LeewayNone})); !g.Recorded || g.Until != nil {
		t.Errorf("none: %+v", g)
	}
	g := grace(ctx, mk(&commitments.Leeway{Kind: commitments.LeewayPeriod, Days: &days}))
	if !g.Recorded || g.Until == nil || g.Until.String() != "2026-01-10" {
		t.Errorf("period: %+v", g)
	}
	// An unreadable commitment reports nothing recorded, never a date, and the
	// context's logger gets one error line with the identifiers only.
	var logs bytes.Buffer
	lctx := logging.WithLogger(ctx, slog.New(slog.NewTextHandler(&logs, nil)))
	missing := occurrences.Occurrence{ID: "01JD3Y4C8V9M2Q7W5R6T0XZBNZ", CommitmentID: "01JD3Y4C8V9M2Q7W5R6T0XZBNZ", DueDate: calendar.MustDate(2026, time.January, 5)}
	if g := grace(lctx, missing); g != (payments.Grace{}) {
		t.Errorf("missing commitment: %+v", g)
	}
	if line := logs.String(); !strings.Contains(line, "level=ERROR") || !strings.Contains(line, "occurrence_id="+missing.ID) || !strings.Contains(line, "commitment_id="+missing.CommitmentID) {
		t.Errorf("context logger got %q, want an error line with the occurrence and commitment IDs", line)
	}
}

// TestGraceHookUsesTheCallersContext: a read the caller's context has stopped
// reports nothing recorded instead of reading the leeway anyway.
func TestGraceHookUsesTheCallersContext(t *testing.T) {
	ctx := context.Background()
	st, _, _ := newStore(t)
	days := 5
	f := vps()
	f.Leeway = &commitments.Leeway{Kind: commitments.LeewayPeriod, Days: &days}
	c, err := st.Create(ctx, alice, f)
	if err != nil {
		t.Fatal(err)
	}
	o := occurrences.Occurrence{CommitmentID: c.ID, DueDate: calendar.MustDate(2026, time.January, 5)}
	if g := st.Grace()(ctx, o); !g.Recorded || g.Kind != commitments.LeewayPeriod || g.Until == nil {
		t.Fatalf("live context: %+v", g)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	var logs bytes.Buffer
	cancelled = logging.WithLogger(cancelled, slog.New(slog.NewTextHandler(&logs, nil)))
	if g := st.Grace()(cancelled, o); g.Recorded || g.Kind != "" || g.Until != nil {
		t.Errorf("cancelled context: %+v, want nothing recorded", g)
	}
	if logs.Len() != 0 {
		t.Errorf("cancelled context logged %q, want no error line", logs.String())
	}
}
