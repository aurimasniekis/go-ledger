package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/store"
	matchstore "github.com/aurimasniekis/go-ledger/internal/store/matches"
)

// TestMatcherStoreUsesTheReportingZone pins the wiring in ledger serve: the
// matching engine is built before registerAPI applies the reporting zone, so
// newMatcher must hand the engine a zoned store itself. A zoned store derives
// facts in the zone; the one passed in must stay unzoned (WithZone copies).
func TestMatcherStoreUsesTheReportingZone(t *testing.T) {
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	zone := calendar.MustLoadZone("Europe/Vilnius")
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	al := store.NewAuditLog(db)
	ms := matchstore.New(db, al, func() time.Time { return now }, nil)

	eng := newMatcher(db, al, ms, zone, func() time.Time { return now }, nil)
	got, ok := eng.Store().(*matchstore.Store)
	if !ok {
		t.Fatalf("engine store is %T, want *matches.Store", eng.Store())
	}
	if name := got.Zone().Name(); name != "Europe/Vilnius" {
		t.Errorf("engine store zone = %q, want Europe/Vilnius", name)
	}
	if !ms.Zone().IsZero() {
		t.Errorf("newMatcher changed the caller's store: zone %q", ms.Zone().Name())
	}
}
