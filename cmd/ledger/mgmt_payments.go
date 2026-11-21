package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

// parse2 is parse for commands naming an occurrence and a payment: the first
// two arguments are the IDs, flags follow.
func (m *mgmt) parse2(fs *flag.FlagSet, args []string) (string, string, int) {
	if len(args) < 2 || strings.HasPrefix(args[0], "-") || strings.HasPrefix(args[1], "-") {
		say(m.stderr, fs.Name()+": an occurrence ID and a payment ID are required as the first two arguments")
		return "", "", 2
	}
	_, rc := m.parse(fs, args[2:], false)
	return args[0], args[1], rc
}

func occurrencesPayments(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("occurrences payments")
	l := addListFlags(fs)
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		say(m.stderr, fs.Name()+": an object ID is required as the first argument")
		return 2
	}
	id := args[0]
	return runList(m, fs, args[1:], l, func(ctx context.Context, cl *ledgerclient.Client, o ledgerclient.ListOptions) (ledgerclient.Page[ledgerclient.ManualPayment], error) {
		return cl.ListManualPayments(ctx, id, ledgerclient.ManualPaymentFilter{ListOptions: o})
	}, []string{"ID", "PAID ON", "AMOUNT", "NOTE", "VERSION"}, func(p ledgerclient.ManualPayment) []any {
		return []any{p.ID, p.PaidOn, money(p.Amount), deref(p.Note), p.Version}
	})
}

func occurrencesPay(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("occurrences pay")
	var in ledgerclient.NewManualPayment
	var note string
	fs.StringVar(&in.Amount.Amount, "amount", "", "")
	fs.StringVar(&in.Amount.Currency, "currency", "", "")
	fs.StringVar(&in.PaidOn, "paid-on", "", "")
	fs.StringVar(&note, "note", "", "")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	if note != "" {
		in.Note = &note
	}
	// Anything invalid is reported by the server, field by field.
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		p, err := c.RecordManualPayment(ctx, id, in)
		if err != nil {
			return err
		}
		return m.emit(p, func(w io.Writer) { paymentText(w, p) })
	})
}

func occurrencesPayment(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("occurrences payment")
	occ, pay, rc := m.parse2(fs, args)
	if rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		p, err := c.GetManualPayment(ctx, occ, pay)
		if err != nil {
			return err
		}
		return m.emit(p, func(w io.Writer) { paymentText(w, p) })
	})
}

func occurrencesDeletePayment(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("occurrences delete-payment")
	var version int64
	fs.Int64Var(&version, "version", 0, "")
	occ, pay, rc := m.parse2(fs, args)
	if rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		v, err := m.resolveVersion(version, func() (int64, error) {
			p, err := c.GetManualPayment(ctx, occ, pay)
			if err != nil {
				return 0, err
			}
			return p.Version, nil
		})
		if err != nil {
			return err
		}
		if err := c.DeleteManualPayment(ctx, occ, pay, v); err != nil {
			return err
		}
		return m.emit(map[string]any{"deleted": pay}, func(w io.Writer) { _, _ = fmt.Fprintln(w, "deleted", printable(pay)) })
	})
}

func occurrencesOverride(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("occurrences override")
	var version int64
	var in ledgerclient.StatusOverride
	fs.Int64Var(&version, "version", 0, "")
	fs.StringVar(&in.Status, "status", "", "")
	fs.StringVar(&in.Reason, "reason", "", "")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	if in.Status != ledgerclient.OverridePaid && in.Status != ledgerclient.OverrideUnpaid {
		say(m.stderr, "occurrences override: --status must be paid or unpaid")
		return 2
	}
	if in.Reason == "" {
		say(m.stderr, "occurrences override: --reason is required")
		return 2
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		v, err := m.occurrenceVersion(ctx, c, id, version)
		if err != nil {
			return err
		}
		o, err := c.SetStatusOverride(ctx, id, v, in)
		if err != nil {
			return err
		}
		return m.emit(o, func(w io.Writer) { occurrenceText(w, o) })
	})
}

func occurrencesUndoOverride(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("occurrences undo-override")
	var version int64
	fs.Int64Var(&version, "version", 0, "")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		v, err := m.occurrenceVersion(ctx, c, id, version)
		if err != nil {
			return err
		}
		o, err := c.UndoStatusOverride(ctx, id, v)
		if err != nil {
			return err
		}
		return m.emit(o, func(w io.Writer) { occurrenceText(w, o) })
	})
}

func (m *mgmt) occurrenceVersion(ctx context.Context, c *ledgerclient.Client, id string, explicit int64) (int64, error) {
	return m.resolveVersion(explicit, func() (int64, error) {
		o, err := c.GetOccurrence(ctx, id)
		if err != nil {
			return 0, err
		}
		return o.Version, nil
	})
}

// ---- tokens (administrative; own tokens are session-only) ----

func tokensList(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("tokens list")
	var owner string
	fs.StringVar(&owner, "owner-id", "", "")
	l := addListFlags(fs)
	return runList(m, fs, args, l, func(ctx context.Context, cl *ledgerclient.Client, o ledgerclient.ListOptions) (ledgerclient.Page[ledgerclient.APIToken], error) {
		return cl.ListTokens(ctx, ledgerclient.TokenFilter{ListOptions: o, OwnerID: owner})
	}, []string{"ID", "NAME", "OWNER", "HINT", "STATUS", "EXPIRES"}, func(t ledgerclient.APIToken) []any {
		return []any{t.ID, t.Name, t.OwnerID, t.Hint, t.Status, deref(t.ExpiresAt)}
	})
}

func tokensGet(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("tokens get")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		t, err := c.GetToken(ctx, id)
		if err != nil {
			return err
		}
		return m.emit(t, func(w io.Writer) { tokenText(w, t) })
	})
}

func tokensRevoke(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("tokens revoke")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		if err := c.RevokeToken(ctx, id); err != nil {
			return err
		}
		return m.emit(map[string]any{"revoked": id}, func(w io.Writer) { _, _ = fmt.Fprintln(w, "revoked", printable(id)) })
	})
}
