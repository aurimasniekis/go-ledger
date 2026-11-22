package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

// The suggestions and alerts commands review the model's suggestions and the
// in-app alerts (task L6c, ADR-0065). Reading needs decisions:read and every
// change decisions:write; accepting, correcting and reverting a suggestion
// also write an assignment, so they need organisation:write too (and
// merchants:write for a merchant). All are administrator grants.
//
// A suggestion is model output, never an assignment: the provider and model
// that said it are shown with it and stay when it is accepted.

func init() {
	mgmtVerbs["suggestions"] = map[string]verb{
		"list":    {"list [--state S]... [--task T]... [--object-type TYPE] [--object-id ID] [--limit N] [--cursor C] [--all]   (state: pending, accepted, rejected, superseded, reverted; task: categorisation, label_suggestion; needs decisions:read)", suggestionsList},
		"get":     {"get <id>", suggestionsGet},
		"accept":  {"accept <id>   (applies it as your own decision; also needs organisation:write)", suggestionAction("accept", (*ledgerclient.Client).AcceptSuggestion)},
		"correct": {"correct <id> --category ID | --none | --label ID...   (your own answer instead; the suggestion is marked rejected; also needs organisation:write)", suggestionsCorrect},
		"reject":  {"reject <id>   (nothing is assigned)", suggestionAction("reject", (*ledgerclient.Client).RejectSuggestion)},
		"revert":  {"revert <id>   (takes back an auto-applied category; refused when it changed since; also needs organisation:write)", suggestionAction("revert", (*ledgerclient.Client).RevertSuggestion)},
	}
	mgmtVerbs["alerts"] = map[string]verb{
		"list":    {"list [--state S]... [--kind K]... [--object-type TYPE] [--object-id ID] [--limit N] [--cursor C] [--all]   (state: open, dismissed, resolved; needs decisions:read)", alertsList},
		"get":     {"get <id>", alertsGet},
		"dismiss": {"dismiss <id>   (a dismissal sticks; needs decisions:write)", alertsDismiss},
	}
}

func suggestionText(w io.Writer, s *ledgerclient.Suggestion) {
	kv(w, "id", s.ID, "task", s.Task, "object", s.ObjectType+" "+s.ObjectID, "state", s.State, "suggested by", s.SuggestedBy.Label,
		"choice", deref(s.Decision.Choice), "labels", strings.Join(s.Decision.Choices, ","), "confidence", deref(s.Decision.Confidence),
		"auto-applied", fmt.Sprint(s.AutoApply), "created", s.CreatedAt, "decided", deref(s.DecidedAt), "version", fmt.Sprint(s.Version))
	if r := s.Reversal; r != nil {
		kv(w, "reverted", r.At+" by "+r.By.Kind)
	}
}

func suggestionsList(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("suggestions list")
	var f ledgerclient.SuggestionFilter
	var states, tasks multi
	fs.Var(&states, "state", "")
	fs.Var(&tasks, "task", "")
	fs.StringVar(&f.ObjectType, "object-type", "", "")
	fs.StringVar(&f.ObjectID, "object-id", "", "")
	l := addListFlags(fs)
	return runList(m, fs, args, l, func(ctx context.Context, cl *ledgerclient.Client, o ledgerclient.ListOptions) (ledgerclient.Page[ledgerclient.Suggestion], error) {
		f.ListOptions, f.States, f.Tasks = o, states, tasks
		return cl.ListSuggestions(ctx, f)
	}, []string{"ID", "TASK", "OBJECT", "STATE", "SUGGESTED BY", "CHOICE"}, func(s ledgerclient.Suggestion) []any {
		choice := deref(s.Decision.Choice)
		if len(s.Decision.Choices) > 0 {
			choice = strings.Join(s.Decision.Choices, ",")
		}
		return []any{s.ID, s.Task, s.ObjectType + " " + s.ObjectID, s.State, s.SuggestedBy.Label, choice}
	})
}

func suggestionsGet(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("suggestions get")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		s, err := c.GetSuggestion(ctx, id)
		if err != nil {
			return err
		}
		return m.emit(s, func(w io.Writer) { suggestionText(w, s) })
	})
}

func suggestionAction(name string, do func(*ledgerclient.Client, context.Context, string) (*ledgerclient.Suggestion, error)) func(*mgmt, context.Context, []string) int {
	return func(m *mgmt, _ context.Context, args []string) int {
		fs := m.flags("suggestions " + name)
		id, rc := m.parse(fs, args, true)
		if rc != 0 {
			return max(rc, 0)
		}
		return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
			s, err := do(c, ctx, id)
			if err != nil {
				return err
			}
			return m.emit(s, func(w io.Writer) { suggestionText(w, s) })
		})
	}
}

func suggestionsCorrect(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("suggestions correct")
	var cat string
	var none bool
	var labels multi
	fs.StringVar(&cat, "category", "", "")
	fs.BoolVar(&none, "none", false, "")
	fs.Var(&labels, "label", "")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	var in ledgerclient.SuggestionCorrection
	switch {
	case len(labels) > 0 && cat == "" && !none:
		in.LabelIDs = labels
	case len(labels) == 0 && cat != "" && !none:
		in.CategoryID = &cat
	case len(labels) == 0 && cat == "" && none:
		in.SetCategory = true
	default:
		say(m.stderr, "suggestions correct: give exactly one of --category ID, --none and --label ID (repeatable)")
		return 2
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		s, err := c.CorrectSuggestion(ctx, id, in)
		if err != nil {
			return err
		}
		return m.emit(s, func(w io.Writer) { suggestionText(w, s) })
	})
}

func alertText(w io.Writer, a *ledgerclient.Alert) {
	kv(w, "id", a.ID, "kind", a.Kind, "object", a.ObjectType+" "+a.ObjectID, "state", a.State, "message", a.Message,
		"created", a.CreatedAt, "closed", deref(a.ClosedAt), "version", fmt.Sprint(a.Version))
	if t := a.ModelText; t != nil {
		kv(w, "model text", t.Text, "worded by", t.Label)
	}
}

func alertsList(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("alerts list")
	var f ledgerclient.AlertFilter
	var states, kinds multi
	fs.Var(&states, "state", "")
	fs.Var(&kinds, "kind", "")
	fs.StringVar(&f.ObjectType, "object-type", "", "")
	fs.StringVar(&f.ObjectID, "object-id", "", "")
	l := addListFlags(fs)
	return runList(m, fs, args, l, func(ctx context.Context, cl *ledgerclient.Client, o ledgerclient.ListOptions) (ledgerclient.Page[ledgerclient.Alert], error) {
		f.ListOptions, f.States, f.Kinds = o, states, kinds
		return cl.ListAlerts(ctx, f)
	}, []string{"ID", "KIND", "OBJECT", "STATE", "MESSAGE"}, func(a ledgerclient.Alert) []any {
		return []any{a.ID, a.Kind, a.ObjectType + " " + a.ObjectID, a.State, a.Message}
	})
}

func alertsGet(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("alerts get")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		a, err := c.GetAlert(ctx, id)
		if err != nil {
			return err
		}
		return m.emit(a, func(w io.Writer) { alertText(w, a) })
	})
}

func alertsDismiss(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("alerts dismiss")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		a, err := c.DismissAlert(ctx, id)
		if err != nil {
			return err
		}
		return m.emit(a, func(w io.Writer) { alertText(w, a) })
	})
}
