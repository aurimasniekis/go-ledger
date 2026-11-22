package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

// leewayFlags are the --leeway* flags of commitments create and update
// (task DF2, ADR-0052). Leeway is only ever what the person types: there is no
// default and nothing is looked up.
type leewayFlags struct {
	kind, until, conditions, consequences, source string
	days                                          int
}

var leewayFlagNames = []string{"leeway", "leeway-days", "leeway-until", "leeway-conditions", "leeway-consequences", "leeway-source"}

func (l *leewayFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&l.kind, "leeway", "", "")
	fs.IntVar(&l.days, "leeway-days", 0, "")
	fs.StringVar(&l.until, "leeway-until", "", "")
	fs.StringVar(&l.conditions, "leeway-conditions", "", "")
	fs.StringVar(&l.consequences, "leeway-consequences", "", "")
	fs.StringVar(&l.source, "leeway-source", "", "")
}

// any reports whether a leeway flag was given.
func (l *leewayFlags) any(seen map[string]bool) bool {
	for _, n := range leewayFlagNames {
		if seen[n] {
			return true
		}
	}
	return false
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// create builds the leeway of a new commitment from the flags given. Members
// the person did not give stay nil, and the server validates the whole.
func (l *leewayFlags) create() *ledgerclient.Leeway {
	out := &ledgerclient.Leeway{Kind: l.kind, Until: nonEmpty(l.until), Conditions: nonEmpty(l.conditions),
		Consequences: nonEmpty(l.consequences), Source: nonEmpty(l.source)}
	if l.days != 0 {
		out.Days = &l.days
	}
	return out
}

// patch builds the merge patch of the leeway object. A kind given clears the
// member that belongs to the other kinds, because a merge patch keeps what it
// does not mention.
func (l *leewayFlags) patch(seen map[string]bool) ledgerclient.MergePatch {
	p := ledgerclient.MergePatch{}
	if seen["leeway"] {
		p["kind"] = l.kind
		if l.kind != ledgerclient.LeewayPeriod {
			p["days"] = nil
		}
		if l.kind != ledgerclient.LeewayDeadline {
			p["until"] = nil
		}
	}
	if seen["leeway-days"] {
		p["days"] = l.days
	}
	if seen["leeway-until"] {
		p["until"] = l.until
	}
	for flagName, v := range map[string]string{"leeway-conditions": l.conditions, "leeway-consequences": l.consequences, "leeway-source": l.source} {
		if seen[flagName] {
			if v == "" {
				p[strings.TrimPrefix(flagName, "leeway-")] = nil // an empty value clears the text
			} else {
				p[strings.TrimPrefix(flagName, "leeway-")] = v
			}
		}
	}
	return p
}

// leewayText shows a commitment's leeway for people: not recorded, unknown
// and none are all different from "0 days".
func leewayText(l *ledgerclient.Leeway) string {
	if l == nil {
		return "not recorded"
	}
	s := l.Kind
	switch {
	case l.Days != nil:
		s = fmt.Sprintf("%d days after the due date", *l.Days)
	case l.Until != nil:
		s = "until " + *l.Until
	case l.Kind == ledgerclient.LeewayUnknown:
		s = "unknown (stated by the user)"
	case l.Kind == ledgerclient.LeewayNone:
		s = "none (stated by the user)"
	}
	for _, m := range []struct {
		label string
		v     *string
	}{{"conditions", l.Conditions}, {"consequences", l.Consequences}, {"source", l.Source}} {
		if m.v != nil {
			s += "; " + m.label + ": " + *m.v
		}
	}
	return s
}
