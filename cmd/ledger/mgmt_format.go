package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

func table(w io.Writer, headers ...string) *tabwriter.Writer {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	row(tw, toAny(headers)...)
	return tw
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func row(tw io.Writer, cols ...any) {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = printable(strings.ReplaceAll(fmt.Sprint(c), "\t", " "))
	}
	_, _ = fmt.Fprintln(tw, strings.Join(parts, "\t"))
}

// unsafeRune reports control characters (Cc), format characters such as the
// bidirectional overrides (Cf), and line and paragraph separators (Zl, Zp).
func unsafeRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
}

// printable makes server-supplied text printable: control, bidi and line-separator
// characters, such as newlines and escape sequences, are written as visible escapes so they cannot
// break the table or key layout or drive the terminal. JSON output needs no
// such care: encoding/json already escapes them.
func printable(s string) string {
	if !strings.ContainsFunc(s, unsafeRune) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case !unsafeRune(r):
			b.WriteRune(r)
		case r < 0x100:
			_, _ = fmt.Fprintf(&b, `\x%02x`, r)
		default:
			_, _ = fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}

// kv prints "key: value" lines from alternating keys and values.
func kv(w io.Writer, pairs ...string) {
	for i := 0; i+1 < len(pairs); i += 2 {
		_, _ = fmt.Fprintf(w, "%-12s %s\n", pairs[i]+":", printable(pairs[i+1]))
	}
}

func money(m ledgerclient.Money) string { return m.Amount + " " + m.Currency }

func scheduleText(s ledgerclient.Schedule) string {
	switch s.Frequency {
	case ledgerclient.FrequencyMonthly:
		iv := 1
		if s.Interval != nil {
			iv = *s.Interval
		}
		return fmt.Sprintf("every %d month(s) on day %d", iv, s.Day)
	case ledgerclient.FrequencyAnnual:
		m := 0
		if s.Month != nil {
			m = *s.Month
		}
		return fmt.Sprintf("yearly on %02d-%02d", m, s.Day)
	}
	return s.Frequency
}

func moreHint(w io.Writer, next *string) {
	if next != nil {
		_, _ = fmt.Fprintf(w, "\nmore: rerun with --cursor %s (or --all)\n", printable(*next))
	}
}

func userText(w io.Writer, u *ledgerclient.User) {
	kv(w, "id", u.ID, "username", u.Username, "display name", deref(u.DisplayName), "role", u.Role,
		"disabled", fmt.Sprint(u.Disabled), "version", fmt.Sprint(u.Version), "created", u.CreatedAt, "updated", u.UpdatedAt)
}

func commitmentText(w io.Writer, c *ledgerclient.Commitment) {
	kv(w, "id", c.ID, "name", c.Name, "kind", c.Kind, "amount", money(c.Amount), "schedule", scheduleText(c.Schedule),
		"start date", c.StartDate, "end date", deref(c.EndDate), "priority", deref(c.Priority), "notes", deref(c.Notes),
		"match tolerance", tolText(c.MatchTolerance), "leeway", leewayText(c.Leeway), "version", fmt.Sprint(c.Version), "updated", c.UpdatedAt)
}

// tolText shows a commitment's match tolerance; empty when none is set.
func tolText(m *ledgerclient.Money) string {
	if m == nil {
		return ""
	}
	return money(*m)
}

func occurrenceText(w io.Writer, o *ledgerclient.Occurrence) {
	p := o.Payment
	kv(w, "id", o.ID, "commitment", o.CommitmentID, "month", o.Month, "due date", o.DueDate, "amount", money(o.Amount), "version", fmt.Sprint(o.Version),
		"status", p.Status, "timing", deref(p.Timing), "paid", money(p.PaidAmount), "outstanding", money(p.OutstandingAmount), "overpaid", money(p.OverpaidAmount),
		"not counted", strings.Join(p.UnconvertedCurrencies, ", "), "evidence", fmt.Sprint(p.EvidenceCount))
	if p.Status != p.DerivedStatus {
		kv(w, "derived", p.DerivedStatus)
	}
	if ov := p.Override; ov != nil {
		kv(w, "override", fmt.Sprintf("%s (applied: %t) since %s: %s", ov.Status, ov.Applied, ov.SetAt, ov.Reason))
	}
}

func paymentText(w io.Writer, p *ledgerclient.ManualPayment) {
	kv(w, "id", p.ID, "occurrence", p.OccurrenceID, "amount", money(p.Amount), "paid on", p.PaidOn, "note", deref(p.Note),
		"version", fmt.Sprint(p.Version), "created", p.CreatedAt)
}

func tokenText(w io.Writer, t *ledgerclient.APIToken) {
	grants := strings.Join(t.Grants, ", ")
	if t.Root {
		grants = "(root)"
	}
	kv(w, "id", t.ID, "name", t.Name, "owner", t.OwnerID, "hint", t.Hint, "status", t.Status, "grants", grants,
		"created", t.CreatedAt, "expires", deref(t.ExpiresAt), "last used", deref(t.LastUsedAt), "revoked", deref(t.RevokedAt))
}
