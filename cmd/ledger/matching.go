package main

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/automatch"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/store"
	matchstore "github.com/aurimasniekis/go-ledger/internal/store/matches"
	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

// matchingRun implements "ledger matching run": one automatic matching run on
// the server, through the API.
func matchingRun(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("matching run")
	if _, rc := m.parse(fs, args, false); rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		r, err := c.RunPaymentMatching(ctx)
		if err != nil {
			return err
		}
		return m.emit(r, func(w io.Writer) {
			kv(w, "candidates", strconv.Itoa(r.Candidates), "confirmed", strconv.Itoa(r.Confirmed), "suggested", strconv.Itoa(r.Suggested),
				"dropped", strconv.Itoa(r.Dropped), "skipped", strconv.Itoa(r.Skipped))
		})
	})
}

// newMatcher builds the automatic payment-matching engine (task X2,
// ADR-0039) on the match store. The engine's store always derives payment facts
// in the reporting zone, whatever the caller passed: WithZone copies, so ms is
// left as it was. Today is the current date in the reporting zone; a run
// requested through the API is audited through al. now defaults to time.Now.
func newMatcher(db *store.DB, al store.AuditWriter, ms *matchstore.Store, zone calendar.Zone, now func() time.Time, log *slog.Logger) *automatch.Engine {
	if now == nil {
		now = time.Now
	}
	return automatch.New(ms.WithZone(zone), automatch.Options{
		Today:    func() (calendar.Date, error) { return zone.Today(now()) },
		Now:      now,
		Log:      log,
		Recorder: recorder{db: db, w: al},
	})
}
