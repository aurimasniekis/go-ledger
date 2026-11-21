package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

// The bank commands manage bank connections through the Management API (task
// B4f, ADR-0040). They need connections:read or connections:write, which are
// administrator grants. They never ask for or print bank credentials or
// consent codes: "connect" and "reconnect" print the bank's consent page,
// which the person opens in a browser, and the connection completes when the
// bank redirects to Ledger's callback page, not through the CLI.

func bankApplications(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("bank applications")
	if _, rc := m.parse(fs, args, false); rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		apps, err := c.ListBankApplications(ctx)
		if err != nil {
			return err
		}
		if apps == nil {
			apps = []ledgerclient.BankApplication{}
		}
		return m.emit(map[string]any{"items": apps}, func(w io.Writer) {
			tw := table(w, "NAME", "PROVIDER", "MODE", "ENVIRONMENT", "ACCOUNT ACCESS", "MAX CONSENT DAYS", "MAX HISTORY DAYS")
			for _, a := range apps {
				row(tw, a.Name, a.Provider, a.Mode, a.Environment, a.AccountAccess, zeroUnknown(a.Capabilities.MaxConsentValidityDays), zeroUnknown(a.Capabilities.MaxHistoryDays))
			}
			_ = tw.Flush()
			if len(apps) == 0 {
				_, _ = fmt.Fprintln(w, "(none configured)")
			}
		})
	})
}

// zeroUnknown shows 0 ("not known" in the API) as unknown, never as none.
func zeroUnknown(n int) string {
	if n == 0 {
		return "unknown"
	}
	return strconv.Itoa(n)
}

func bankInstitutions(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("bank institutions")
	var country string
	fs.StringVar(&country, "country", "", "")
	app, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	if country == "" {
		say(m.stderr, "bank institutions: --country is required (ISO 3166-1 alpha-2 in capitals, such as DE)")
		return 2
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		items, err := c.ListBankInstitutions(ctx, app, country)
		if err != nil {
			return err
		}
		if items == nil {
			items = []ledgerclient.BankInstitution{}
		}
		return m.emit(map[string]any{"items": items}, func(w io.Writer) {
			tw := table(w, "ID", "NAME", "COUNTRY", "MAX CONSENT DAYS")
			for _, i := range items {
				row(tw, i.ID, i.Name, i.Country, zeroUnknown(i.MaxConsentValidityDays))
			}
			_ = tw.Flush()
			if len(items) == 0 {
				_, _ = fmt.Fprintln(w, "(none)")
			}
		})
	})
}

func bankList(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("bank list")
	l := addListFlags(fs)
	return runList(m, fs, args, l, func(ctx context.Context, cl *ledgerclient.Client, o ledgerclient.ListOptions) (ledgerclient.Page[ledgerclient.BankConnection], error) {
		return cl.ListBankConnections(ctx, o)
	}, []string{"ID", "BANK", "APPLICATION", "STATUS", "FRESHNESS", "ACCOUNTS", "REFRESHING", "VERSION"}, func(b ledgerclient.BankConnection) []any {
		return []any{b.ID, b.Institution.Name, b.Application, b.Status, b.Freshness, len(b.Accounts), b.RefreshRunning, b.Version}
	})
}

func bankGet(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("bank get")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		b, err := c.GetBankConnection(ctx, id)
		if err != nil {
			return err
		}
		return m.emit(b, func(w io.Writer) { bankConnectionText(w, b) })
	})
}

func bankConnect(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("bank connect")
	var in ledgerclient.NewBankConnection
	fs.StringVar(&in.Application, "application", "", "")
	fs.StringVar(&in.InstitutionID, "institution-id", "", "")
	fs.StringVar(&in.Country, "country", "", "")
	fs.StringVar(&in.PSUType, "psu-type", "", "")
	fs.IntVar(&in.ValidForDays, "valid-for-days", 0, "")
	if _, rc := m.parse(fs, args, false); rc != 0 {
		return max(rc, 0)
	}
	if in.Application == "" || in.InstitutionID == "" || in.Country == "" {
		say(m.stderr, "bank connect: --application, --institution-id and --country are required (see: ledger bank applications, ledger bank institutions)")
		return 2
	}
	// Anything else invalid is reported by the server, field by field.
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		s, err := c.StartBankConnection(ctx, in)
		if err != nil {
			return err
		}
		return m.emit(s, func(w io.Writer) { consentStartText(w, s) })
	})
}

func bankReconnect(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("bank reconnect")
	var days int
	fs.IntVar(&days, "valid-for-days", 0, "")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		s, err := c.ReconnectBankConnection(ctx, id, days)
		if err != nil {
			return err
		}
		return m.emit(s, func(w io.Writer) { consentStartText(w, s) })
	})
}

func bankRefresh(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("bank refresh")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		b, err := c.RefreshBankConnection(ctx, id)
		if err != nil {
			return err
		}
		return m.emit(b, func(w io.Writer) { bankConnectionText(w, b) })
	})
}

func bankDisconnect(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("bank disconnect")
	var version int64
	fs.Int64Var(&version, "version", 0, "")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		v, err := m.resolveVersion(version, func() (int64, error) {
			b, err := c.GetBankConnection(ctx, id)
			if err != nil {
				return 0, err
			}
			return b.Version, nil
		})
		if err != nil {
			return err
		}
		b, err := c.DisconnectBankConnection(ctx, id, v)
		if err != nil {
			return err
		}
		return m.emit(b, func(w io.Writer) { bankConnectionText(w, b) })
	})
}

// consentStartText prints the consent page. The URL is the one thing the
// person needs; it holds a one-time state, never a bank credential.
func consentStartText(w io.Writer, s *ledgerclient.BankConsentStart) {
	kv(w, "connection", s.Connection.ID, "status", s.Connection.Status, "bank", s.Connection.Institution.Name)
	_, _ = fmt.Fprintln(w, "Open this page in a browser and approve access at the bank:")
	_, _ = fmt.Fprintln(w, "  "+printable(s.AuthorizationURL))
	_, _ = fmt.Fprintln(w, "The connection becomes active when the bank redirects back to Ledger's web UI; until then it is not connected and nothing is fetched.")
}

func bankConnectionText(w io.Writer, b *ledgerclient.BankConnection) {
	pairs := []string{"id", b.ID, "bank", b.Institution.Name + " (" + b.Institution.Country + ")",
		"application", b.Application + " (" + b.Mode + " mode, " + b.Environment + ")",
		"status", b.Status, "freshness", b.Freshness, "as of", b.AsOf,
		"consent until", deref(b.ConsentValidUntil), "version", strconv.FormatInt(b.Version, 10)}
	if b.NeedsReconnect {
		pairs = append(pairs, "action", "reconnect needed: ledger bank reconnect "+b.ID)
	}
	if b.ConsentExpiresSoon {
		pairs = append(pairs, "notice", "consent ends soon")
	}
	if b.RefreshRunning {
		pairs = append(pairs, "refreshing", "yes: a refresh is still running, read the connection again later")
	}
	if e := b.LastError; e != nil {
		pairs = append(pairs, "last error", e.Kind+" "+e.Code)
	}
	if r := b.LastRefreshRun; r != nil {
		s := r.Outcome + " at " + r.StartedAt
		if r.Error != nil {
			s += " (" + r.Error.Kind + " " + r.Error.Code + ")"
		}
		pairs = append(pairs, "last refresh", s)
	}
	kv(w, pairs...)
	if len(b.Accounts) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w)
	tw := table(w, "ACCOUNT", "NAME", "CURRENCY", "HINT", "FRESHNESS", "LAST SUCCESS", "BALANCES")
	for _, a := range b.Accounts {
		var bal []string
		for _, x := range a.Balances {
			bal = append(bal, x.Type+" "+money(x.Amount)+" (obtained "+x.ObtainedAt+")")
		}
		balances := strings.Join(bal, "; ")
		if balances == "" {
			balances = "none reported"
		}
		state := a.Freshness.State
		if a.NoLongerReported {
			state += " (no longer reported)"
		}
		row(tw, a.ID, a.Name, a.Currency, deref(a.IdentifierHint), state, deref(a.Freshness.LastSuccessAt), balances)
	}
	_ = tw.Flush()
}
