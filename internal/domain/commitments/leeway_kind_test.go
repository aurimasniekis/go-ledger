package commitments_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
)

// TestLeewayKindSwitchNamesWhatAPatchMustSend: a merge patch keeps the members
// of the old kind unless it nulls them, so the refusal says what to send.
func TestLeewayKindSwitchNamesWhatAPatchMustSend(t *testing.T) {
	days := 5
	until := calendar.MustDate(2026, time.March, 1)
	cases := []struct {
		name  string
		l     commitments.Leeway
		field string
		hint  string
	}{
		{"period to deadline", commitments.Leeway{Kind: commitments.LeewayDeadline, Until: &until, Days: &days}, "leeway.days", "send days as null"},
		{"deadline to period", commitments.Leeway{Kind: commitments.LeewayPeriod, Days: &days, Until: &until}, "leeway.until", "send until as null"},
	}
	for _, c := range cases {
		var msg string
		for _, e := range c.l.Validate() {
			if e.Field == c.field {
				msg = e.Message
			}
		}
		if !strings.Contains(msg, c.hint) {
			t.Errorf("%s: %s = %q, want it to say %q", c.name, c.field, msg, c.hint)
		}
	}
}
