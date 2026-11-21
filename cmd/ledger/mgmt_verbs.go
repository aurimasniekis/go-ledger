package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

// multi is a repeatable string flag.
type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

type listFlags struct {
	limit  int
	cursor string
	all    bool
}

func addListFlags(fs *flag.FlagSet) *listFlags {
	l := &listFlags{}
	fs.IntVar(&l.limit, "limit", 0, "")
	fs.StringVar(&l.cursor, "cursor", "", "")
	fs.BoolVar(&l.all, "all", false, "")
	return l
}

func (l *listFlags) opts() ledgerclient.ListOptions {
	return ledgerclient.ListOptions{Limit: l.limit, Cursor: l.cursor}
}

// runList parses the flag set, fetches one page (or every page with --all) and
// prints it. Filters stay fixed across pages, as cursors require.
func runList[T any](m *mgmt, fs *flag.FlagSet, args []string, l *listFlags,
	fetch func(context.Context, *ledgerclient.Client, ledgerclient.ListOptions) (ledgerclient.Page[T], error),
	headers []string, cols func(T) []any) int {
	if _, rc := m.parse(fs, args, false); rc != 0 {
		return max(rc, 0)
	}
	if l.all && l.cursor != "" {
		say(m.stderr, fs.Name()+": --all and --cursor cannot be combined")
		return 2
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		var page ledgerclient.Page[T]
		if l.all {
			items, err := ledgerclient.Collect(ledgerclient.Walk(ctx, func(ctx context.Context, cur string) (ledgerclient.Page[T], error) {
				return fetch(ctx, c, ledgerclient.ListOptions{Limit: l.limit, Cursor: cur})
			}))
			if err != nil {
				return err
			}
			page.Items = items
		} else {
			var err error
			if page, err = fetch(ctx, c, l.opts()); err != nil {
				return err
			}
		}
		if page.Items == nil {
			page.Items = []T{}
		}
		return m.emit(page, func(w io.Writer) {
			tw := table(w, headers...)
			for _, it := range page.Items {
				row(tw, cols(it)...)
			}
			_ = tw.Flush()
			if len(page.Items) == 0 {
				_, _ = fmt.Fprintln(w, "(none)")
			}
			moreHint(w, page.NextCursor)
		})
	})
}

// resolveVersion returns the version a change is based on: --version when
// given, else the object's current version read just before the write. A
// concurrent change in between still fails with version_conflict.
func resolveVersion(explicit int64, read func() (int64, error)) (int64, error) {
	if explicit > 0 {
		return explicit, nil
	}
	return read()
}

func visited(fs *flag.FlagSet) map[string]bool {
	v := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { v[f.Name] = true })
	return v
}

// ---- users ----

func usersList(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("users list")
	var roles multi
	var disabled, prefix string
	fs.Var(&roles, "role", "")
	fs.StringVar(&disabled, "disabled", "", "")
	fs.StringVar(&prefix, "username-prefix", "", "")
	l := addListFlags(fs)
	return runList(m, fs, args, l, func(ctx context.Context, cl *ledgerclient.Client, o ledgerclient.ListOptions) (ledgerclient.Page[ledgerclient.User], error) {
		return cl.ListUsers(ctx, ledgerclient.UserFilter{ListOptions: o, Roles: roles, Disabled: disabled, UsernamePrefix: prefix})
	}, []string{"ID", "USERNAME", "ROLE", "DISABLED", "VERSION"}, func(u ledgerclient.User) []any {
		return []any{u.ID, u.Username, u.Role, u.Disabled, u.Version}
	})
}

func usersGet(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("users get")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		u, err := c.GetUser(ctx, id)
		if err != nil {
			return err
		}
		return m.emit(u, func(w io.Writer) { userText(w, u) })
	})
}

func usersCreate(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("users create")
	var in ledgerclient.NewUser
	var display string
	fs.StringVar(&in.Username, "username", "", "")
	fs.StringVar(&in.Role, "role", "", "")
	fs.StringVar(&display, "display-name", "", "")
	if _, rc := m.parse(fs, args, false); rc != 0 {
		return max(rc, 0)
	}
	if display != "" {
		in.DisplayName = &display
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		u, err := c.CreateUser(ctx, in)
		if err != nil {
			return err
		}
		return m.emit(u, func(w io.Writer) { userText(w, u) })
	})
}

func usersUpdate(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("users update")
	var version int64
	var username, display, role string
	var clearDisplay, disabled bool
	fs.Int64Var(&version, "version", 0, "")
	fs.StringVar(&username, "username", "", "")
	fs.StringVar(&display, "display-name", "", "")
	fs.BoolVar(&clearDisplay, "clear-display-name", false, "")
	fs.StringVar(&role, "role", "", "")
	fs.BoolVar(&disabled, "disabled", false, "")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	seen := visited(fs)
	patch := ledgerclient.MergePatch{}
	if seen["username"] {
		patch["username"] = username
	}
	if seen["display-name"] && clearDisplay {
		say(m.stderr, "users update: --display-name and --clear-display-name cannot be combined")
		return 2
	}
	if seen["display-name"] {
		patch["display_name"] = display
	}
	if clearDisplay {
		patch["display_name"] = nil
	}
	if seen["role"] {
		patch["role"] = role
	}
	if seen["disabled"] {
		patch["disabled"] = disabled
	}
	if len(patch) == 0 {
		say(m.stderr, "users update: nothing to change")
		return 2
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		v, err := resolveVersion(version, func() (int64, error) {
			u, err := c.GetUser(ctx, id)
			if err != nil {
				return 0, err
			}
			return u.Version, nil
		})
		if err != nil {
			return err
		}
		u, err := c.UpdateUser(ctx, id, v, patch)
		if err != nil {
			return err
		}
		return m.emit(u, func(w io.Writer) { userText(w, u) })
	})
}

// ---- commitments ----

func commitmentsList(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("commitments list")
	var kinds, prios, curs multi
	fs.Var(&kinds, "kind", "")
	fs.Var(&prios, "priority", "")
	fs.Var(&curs, "currency", "")
	l := addListFlags(fs)
	return runList(m, fs, args, l, func(ctx context.Context, cl *ledgerclient.Client, o ledgerclient.ListOptions) (ledgerclient.Page[ledgerclient.Commitment], error) {
		return cl.ListCommitments(ctx, ledgerclient.CommitmentFilter{ListOptions: o, Kinds: kinds, Priorities: prios, Currencies: curs})
	}, []string{"ID", "NAME", "KIND", "AMOUNT", "SCHEDULE", "PRIORITY", "VERSION"}, func(c ledgerclient.Commitment) []any {
		return []any{c.ID, c.Name, c.Kind, money(c.Amount), scheduleText(c.Schedule), deref(c.Priority), c.Version}
	})
}

func commitmentsGet(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("commitments get")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		cm, err := c.GetCommitment(ctx, id)
		if err != nil {
			return err
		}
		return m.emit(cm, func(w io.Writer) { commitmentText(w, cm) })
	})
}

func commitmentsCreate(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("commitments create")
	var in ledgerclient.NewCommitment
	var endDate, prio, notes string
	var interval, month int
	fs.StringVar(&in.Name, "name", "", "")
	fs.StringVar(&in.Kind, "kind", "", "")
	fs.StringVar(&in.Amount.Amount, "amount", "", "")
	fs.StringVar(&in.Amount.Currency, "currency", "", "")
	fs.StringVar(&in.Schedule.Frequency, "frequency", "", "")
	fs.IntVar(&interval, "interval", 0, "")
	fs.IntVar(&month, "month", 0, "")
	fs.IntVar(&in.Schedule.Day, "day", 0, "")
	fs.StringVar(&in.StartDate, "start-date", "", "")
	fs.StringVar(&endDate, "end-date", "", "")
	fs.StringVar(&prio, "priority", "", "")
	fs.StringVar(&notes, "notes", "", "")
	if _, rc := m.parse(fs, args, false); rc != 0 {
		return max(rc, 0)
	}
	in.Schedule.Version = 1
	if in.Schedule.Frequency == ledgerclient.FrequencyMonthly && interval == 0 {
		interval = 1 // every month, the documented meaning of 1
	}
	if interval > 0 {
		in.Schedule.Interval = &interval
	}
	if month > 0 {
		in.Schedule.Month = &month
	}
	for v, dst := range map[string]**string{endDate: &in.EndDate, prio: &in.Priority, notes: &in.Notes} {
		if v != "" {
			s := v
			*dst = &s
		}
	}
	// Anything invalid is reported by the server, field by field.
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		cm, err := c.CreateCommitment(ctx, in)
		if err != nil {
			return err
		}
		return m.emit(cm, func(w io.Writer) { commitmentText(w, cm) })
	})
}

func commitmentsUpdate(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("commitments update")
	var version int64
	var name, kind, amount, currency, frequency, startDate, endDate, prio, notes, effective string
	var interval, month, day int
	var clrEnd, clrPrio, clrNotes bool
	fs.Int64Var(&version, "version", 0, "")
	fs.StringVar(&effective, "effective-from", "", "")
	fs.StringVar(&name, "name", "", "")
	fs.StringVar(&kind, "kind", "", "")
	fs.StringVar(&amount, "amount", "", "")
	fs.StringVar(&currency, "currency", "", "")
	fs.StringVar(&frequency, "frequency", "", "")
	fs.IntVar(&interval, "interval", 0, "")
	fs.IntVar(&month, "month", 0, "")
	fs.IntVar(&day, "day", 0, "")
	fs.StringVar(&startDate, "start-date", "", "")
	fs.StringVar(&endDate, "end-date", "", "")
	fs.BoolVar(&clrEnd, "clear-end-date", false, "")
	fs.StringVar(&prio, "priority", "", "")
	fs.BoolVar(&clrPrio, "clear-priority", false, "")
	fs.StringVar(&notes, "notes", "", "")
	fs.BoolVar(&clrNotes, "clear-notes", false, "")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	seen := visited(fs)
	for flagName, clr := range map[string]bool{"end-date": clrEnd, "priority": clrPrio, "notes": clrNotes} {
		if seen[flagName] && clr {
			say(m.stderr, fmt.Sprintf("commitments update: --%s and --clear-%s cannot be combined", flagName, flagName))
			return 2
		}
	}
	patch := ledgerclient.MergePatch{}
	setStr := func(flagName, key, v string, clr bool) {
		if seen[flagName] {
			patch[key] = v
		}
		if clr {
			patch[key] = nil
		}
	}
	setStr("name", "name", name, false)
	setStr("kind", "kind", kind, false)
	setStr("start-date", "start_date", startDate, false)
	setStr("end-date", "end_date", endDate, clrEnd)
	setStr("priority", "priority", prio, clrPrio)
	setStr("notes", "notes", notes, clrNotes)
	setStr("effective-from", "effective_from", effective, false)
	if seen["amount"] || seen["currency"] {
		a := ledgerclient.MergePatch{}
		if seen["amount"] {
			a["amount"] = amount
		}
		if seen["currency"] {
			a["currency"] = currency
		}
		patch["amount"] = a
	}
	if seen["frequency"] || seen["interval"] || seen["month"] || seen["day"] {
		sc := ledgerclient.MergePatch{}
		if seen["frequency"] {
			sc["frequency"] = frequency
			// A merge patch keeps members it does not mention, so switching
			// the form must null the other form's member explicitly.
			switch frequency {
			case ledgerclient.FrequencyMonthly:
				sc["month"] = nil
			case ledgerclient.FrequencyAnnual:
				sc["interval"] = nil
			}
		}
		if seen["interval"] {
			sc["interval"] = interval
		}
		if seen["month"] {
			sc["month"] = month
		}
		if seen["day"] {
			sc["day"] = day
		}
		patch["schedule"] = sc
	}
	if len(patch) == 0 {
		say(m.stderr, "commitments update: nothing to change")
		return 2
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		v, err := resolveVersion(version, func() (int64, error) {
			cm, err := c.GetCommitment(ctx, id)
			if err != nil {
				return 0, err
			}
			return cm.Version, nil
		})
		if err != nil {
			return err
		}
		cm, err := c.UpdateCommitment(ctx, id, v, patch)
		if err != nil {
			return err
		}
		return m.emit(cm, func(w io.Writer) { commitmentText(w, cm) })
	})
}

func commitmentsDelete(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("commitments delete")
	var version int64
	fs.Int64Var(&version, "version", 0, "")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		v, err := resolveVersion(version, func() (int64, error) {
			cm, err := c.GetCommitment(ctx, id)
			if err != nil {
				return 0, err
			}
			return cm.Version, nil
		})
		if err != nil {
			return err
		}
		if err := c.DeleteCommitment(ctx, id, v); err != nil {
			return err
		}
		return m.emit(map[string]any{"deleted": id}, func(w io.Writer) { _, _ = fmt.Fprintln(w, "deleted", id) })
	})
}

// ---- occurrences ----

func occurrencesList(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("occurrences list")
	var month string
	var ids multi
	fs.StringVar(&month, "month", "", "")
	fs.Var(&ids, "commitment-id", "")
	l := addListFlags(fs)
	return runList(m, fs, args, l, func(ctx context.Context, cl *ledgerclient.Client, o ledgerclient.ListOptions) (ledgerclient.Page[ledgerclient.Occurrence], error) {
		return cl.ListOccurrences(ctx, ledgerclient.OccurrenceFilter{ListOptions: o, Month: month, CommitmentIDs: ids})
	}, []string{"ID", "COMMITMENT", "DUE", "AMOUNT", "VERSION"}, func(o ledgerclient.Occurrence) []any {
		return []any{o.ID, o.CommitmentID, o.DueDate, money(o.Amount), o.Version}
	})
}

func occurrencesGet(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("occurrences get")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		o, err := c.GetOccurrence(ctx, id)
		if err != nil {
			return err
		}
		return m.emit(o, func(w io.Writer) { occurrenceText(w, o) })
	})
}

// ---- audit ----

func auditList(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("audit list")
	var actions, kinds multi
	var f ledgerclient.AuditFilter
	fs.Var(&actions, "action", "")
	fs.StringVar(&f.ActionPrefix, "action-prefix", "", "")
	fs.Var(&kinds, "actor-kind", "")
	fs.StringVar(&f.ActorUserID, "actor-user-id", "", "")
	fs.StringVar(&f.ActorTokenID, "actor-token-id", "", "")
	fs.StringVar(&f.TargetType, "target-type", "", "")
	fs.StringVar(&f.TargetID, "target-id", "", "")
	fs.StringVar(&f.Since, "since", "", "")
	fs.StringVar(&f.Until, "until", "", "")
	l := addListFlags(fs)
	return runList(m, fs, args, l, func(ctx context.Context, cl *ledgerclient.Client, o ledgerclient.ListOptions) (ledgerclient.Page[ledgerclient.AuditEntry], error) {
		f.ListOptions, f.Actions, f.ActorKinds = o, actions, kinds
		return cl.ListAudit(ctx, f)
	}, []string{"ID", "AT", "ACTION", "ACTOR", "TARGET"}, func(e ledgerclient.AuditEntry) []any {
		actor := e.ActorKind
		if e.ActorUserID != nil {
			actor += ":" + *e.ActorUserID
		}
		target := "-"
		if e.TargetType != nil {
			target = *e.TargetType + ":" + deref(e.TargetID)
		}
		return []any{e.ID, e.At, e.Action, actor, target}
	})
}
