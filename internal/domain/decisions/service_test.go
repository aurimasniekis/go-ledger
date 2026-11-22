package decisions_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions/decisionstest"
)

var t0 = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

const period = "2026-11"

// Sentinels: values that must never reach a provider.
const (
	sentIBAN         = "LT601010012345678901"
	sentAccount      = "SENTINEL-ACCOUNT-0042"
	sentCounterparty = "Sentinel Personname"
	sentBalance      = "98765.43"
	sentUnknown      = "SENTINEL-UNKNOWN-FIELD"
	catGroceries     = "01JD3Y4C8V9M2Q7W5R6T0XZBN1"
	catRent          = "01JD3Y4C8V9M2Q7W5R6T0XZBN2"
)

func input() decisions.Input {
	return decisions.Input{
		Object: decisions.ObjectRef{Type: "transaction", ID: "01JD3Y4C8V9M2Q7W5R6T0XZBNT"},
		Values: map[decisions.Field]string{
			decisions.FieldDescription:      "CARD PAYMENT MAXIMA VILNIUS",
			decisions.FieldAmount:           "-23.40",
			decisions.FieldCurrency:         "EUR",
			decisions.FieldBookingDate:      "2026-11-20",
			decisions.FieldMerchantName:     "Maxima",
			decisions.FieldIBAN:             sentIBAN,
			decisions.FieldAccountNumber:    sentAccount,
			decisions.FieldCounterpartyName: sentCounterparty,
			decisions.FieldBalance:          sentBalance,
			decisions.Field("secret_note"):  sentUnknown,
		},
		Candidates: []decisions.Candidate{{ID: catGroceries, Name: "Groceries"}, {ID: catRent, Name: "Rent"}},
	}
}

func taskCfg(provider, model string) decisions.TaskConfig {
	return decisions.TaskConfig{Enabled: true, Route: decisions.Route{Provider: provider, Model: model},
		Timeout: 5 * time.Second, MonthlyRequests: 100, Retries: 2}
}

type env struct {
	consents *decisionstest.Consents
	usage    *decisionstest.Usage
	store    *decisionstest.Suggestions
	slept    []time.Duration
	mu       sync.Mutex
}

func newEnv() *env {
	return &env{consents: &decisionstest.Consents{}, usage: &decisionstest.Usage{}, store: &decisionstest.Suggestions{}}
}

func (e *env) service(t *testing.T, cfg decisions.Config, ps ...decisions.Provider) *decisions.Service {
	t.Helper()
	s, err := decisions.New(cfg, ps, decisions.Deps{Consents: e.consents, Usage: e.usage, Suggestions: e.store,
		Now: func() time.Time { return t0 },
		Sleep: func(ctx context.Context, d time.Duration) error {
			e.mu.Lock()
			e.slept = append(e.slept, d)
			e.mu.Unlock()
			return ctx.Err()
		}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func one(task decisions.TaskName, tc decisions.TaskConfig) decisions.Config {
	return decisions.Config{Tasks: map[decisions.TaskName]decisions.TaskConfig{task: tc}}
}

func depsOnly(e *env) decisions.Deps {
	return decisions.Deps{Consents: e.consents, Usage: e.usage, Suggestions: e.store}
}

func assertNoSentinel(t *testing.T, sent [][]byte, extra ...string) {
	t.Helper()
	for _, b := range sent {
		for _, s := range append([]string{sentIBAN, sentAccount, sentCounterparty, sentBalance, sentUnknown, catGroceries, catRent, "secret_note"}, extra...) {
			if bytes.Contains(b, []byte(s)) {
				t.Errorf("request carried %q: %s", s, b)
			}
		}
	}
}

const okChoice = `{"choice":"c1","confidence":0.9}`

// TestDisallowedFieldNeverLeavesProcess: excluded fields (IBAN, account
// number, counterparty name, balance), unknown fields and Ledger's own
// identifiers never reach a provider; a provider's narrower allowlist
// (TypeSafe: no amount, currency or date) holds, also on a fallback route.
func TestDisallowedFieldNeverLeavesProcess(t *testing.T) {
	ctx := context.Background()
	t.Run("default allowlist", func(t *testing.T) {
		e := newEnv()
		e.consents.Grant("openai", decisions.TaskCategorisation)
		p := decisionstest.NewProvider("openai", decisionstest.Reply{Output: okChoice})
		res, err := e.service(t, one(decisions.TaskCategorisation, taskCfg("openai", "m")), p).Run(ctx, decisions.TaskCategorisation, input())
		if err != nil || res.Suggestion == nil {
			t.Fatalf("%+v %v", res, err)
		}
		assertNoSentinel(t, p.Sent())
		if !bytes.Contains(p.Sent()[0], []byte("MAXIMA")) || !bytes.Contains(p.Sent()[0], []byte("Groceries")) {
			t.Errorf("permitted fields missing: %s", p.Sent()[0])
		}
		want := []decisions.Field{decisions.FieldAccountNumber, decisions.FieldBalance, decisions.FieldCounterpartyName, decisions.FieldIBAN, "secret_note"}
		if !slices.Equal(res.Withheld, want) {
			t.Errorf("withheld %v, want %v", res.Withheld, want)
		}
	})
	t.Run("an allowlist naming an excluded field is refused", func(t *testing.T) {
		if _, ps := decisions.ParseFields([]string{"description", "iban", "balance", "counterparty_name", "account_number", "nope"}); len(ps) != 5 {
			t.Fatalf("problems %v", ps)
		}
		tc := taskCfg("openai", "m")
		tc.Fields = []decisions.Field{decisions.FieldDescription, decisions.FieldIBAN, decisions.FieldCandidateNames}
		if _, err := decisions.New(one(decisions.TaskCategorisation, tc), []decisions.Provider{decisionstest.NewProvider("openai")}, depsOnly(newEnv())); err == nil {
			t.Fatal("an allowlist with iban was accepted")
		}
	})
	t.Run("narrower configured allowlist", func(t *testing.T) {
		e := newEnv()
		e.consents.Grant("anthropic", decisions.TaskCategorisation)
		tc := taskCfg("anthropic", "m")
		tc.Fields = []decisions.Field{decisions.FieldDescription, decisions.FieldCandidateNames}
		p := decisionstest.NewProvider("anthropic", decisionstest.Reply{Output: okChoice})
		if _, err := e.service(t, one(decisions.TaskCategorisation, tc), p).Run(ctx, decisions.TaskCategorisation, input()); err != nil {
			t.Fatal(err)
		}
		assertNoSentinel(t, p.Sent(), "-23.40", "2026-11-20", "Maxima\"", "EUR")
	})
	t.Run("typesafe matching sends description, merchant and candidate names only", func(t *testing.T) {
		e := newEnv()
		e.consents.Grant("typesafe", decisions.TaskPaymentMatching)
		p := decisionstest.NewProvider("typesafe", decisionstest.Reply{Output: okChoice})
		res, err := e.service(t, one(decisions.TaskPaymentMatching, taskCfg("typesafe", "system-one")), p).Run(ctx, decisions.TaskPaymentMatching, input())
		if err != nil || res.Suggestion == nil {
			t.Fatalf("%+v %v", res, err)
		}
		assertNoSentinel(t, p.Sent(), "-23.40", "2026-11-20", "EUR", "booking_date", "amount")
	})
	t.Run("fallback route uses its own provider's allowlist", func(t *testing.T) {
		e := newEnv()
		e.consents.Grant("openai", decisions.TaskCategorisation)
		e.consents.Grant("typesafe", decisions.TaskCategorisation)
		tc := taskCfg("openai", "m")
		tc.Fields = decisions.Permitted()
		tc.Fallback = []decisions.Route{{Provider: "typesafe", Model: "system-one"}}
		primary := decisionstest.NewProvider("openai", decisionstest.Reply{Err: &decisions.ProviderError{Kind: decisions.KindUnavailable}})
		fb := decisionstest.NewProvider("typesafe", decisionstest.Reply{Output: okChoice})
		res, err := e.service(t, one(decisions.TaskCategorisation, tc), primary, fb).Run(ctx, decisions.TaskCategorisation, input())
		if err != nil || res.Suggestion == nil || res.Provider != "typesafe" {
			t.Fatalf("%+v %v", res, err)
		}
		if !bytes.Contains(primary.Sent()[0], []byte("-23.40")) {
			t.Error("primary should have had the amount")
		}
		assertNoSentinel(t, fb.Sent(), "-23.40", "2026-11-20", "EUR")
	})
}

// TestInvalidOutputIsRejected: an answer that does not fit the schema or the
// candidates offered is never stored, retried or sent to a fallback.
func TestInvalidOutputIsRejected(t *testing.T) {
	cases := map[decisions.TaskName]map[string]string{
		decisions.TaskCategorisation: {
			"not json":            `Groceries`,
			"empty":               ``,
			"array":               `["c1"]`,
			"unknown member":      `{"choice":"c1","confidence":0.9,"note":"x"}`,
			"missing confidence":  `{"choice":"c1"}`,
			"missing choice":      `{"confidence":0.9}`,
			"confidence above 1":  `{"choice":"c1","confidence":1.5}`,
			"confidence negative": `{"choice":"c1","confidence":-0.1}`,
			"confidence string":   `{"choice":"c1","confidence":"0.9"}`,
			"confidence exp":      `{"choice":"c1","confidence":9e-1}`,
			"not offered":         `{"choice":"c3","confidence":0.9}`,
			"ledger id":           `{"choice":"` + catGroceries + `","confidence":0.9}`,
			"name not key":        `{"choice":"Groceries","confidence":0.9}`,
			"number choice":       `{"choice":1,"confidence":0.9}`,
			"trailing":            okChoice + `{}`,
			"choices member":      `{"choice":"c1","choices":["c1"],"confidence":0.9}`,
			"oversized":           `{"choice":"c1","confidence":0.9` + strings.Repeat(" ", decisions.MaxOutputBytes) + `}`,
		},
		decisions.TaskLabelSuggestion: {
			"repeated":    `{"choices":["c1","c1"],"confidence":0.5}`,
			"too many":    `{"choices":["c1","c2","c3","c4","c5","c6"],"confidence":0.5}`,
			"not offered": `{"choices":["c9"],"confidence":0.5}`,
			"null":        `{"choices":null,"confidence":0.5}`,
		},
		decisions.TaskAlerts: {
			"empty text":   `{"text":"  "}`,
			"too long":     `{"text":"` + strings.Repeat("a", 281) + `"}`,
			"control char": `{"text":"a\tb"}`,
			"confidence":   `{"text":"ok","confidence":1}`,
		},
	}
	for task, outs := range cases {
		for name, out := range outs {
			t.Run(string(task)+"/"+name, func(t *testing.T) {
				e := newEnv()
				e.consents.Grant("openai", task)
				e.consents.Grant("anthropic", task)
				tc := taskCfg("openai", "m")
				tc.Fallback = []decisions.Route{{Provider: "anthropic", Model: "m"}}
				p := decisionstest.NewProvider("openai", decisionstest.Reply{Output: out}, decisionstest.Reply{Output: okChoice})
				fb := decisionstest.NewProvider("anthropic", decisionstest.Reply{Output: okChoice})
				in := input()
				if task == decisions.TaskLabelSuggestion {
					in.Candidates = append(in.Candidates, decisions.Candidate{ID: "x3", Name: "c"}, decisions.Candidate{ID: "x4", Name: "d"}, decisions.Candidate{ID: "x5", Name: "e"}, decisions.Candidate{ID: "x6", Name: "f"})
				}
				if task == decisions.TaskAlerts {
					in.Candidates = nil
				}
				res, err := e.service(t, one(task, tc), p, fb).Run(context.Background(), task, in)
				if err != nil {
					t.Fatal(err)
				}
				if res.Reason != decisions.ReasonInvalidOutput || res.Suggestion != nil || e.store.Len() != 0 || p.Calls() != 1 || fb.Calls() != 0 {
					t.Fatalf("result %+v, stored %d, calls %d/%d", res, e.store.Len(), p.Calls(), fb.Calls())
				}
			})
		}
	}
}

func TestValidOutputIsStoredAsASuggestion(t *testing.T) {
	e := newEnv()
	e.consents.Grant("openai", decisions.TaskCategorisation)
	e.consents.Grant("openai", decisions.TaskLabelSuggestion)
	e.consents.Grant("openai", decisions.TaskAlerts)
	tc := taskCfg("openai", "alias")
	tc.AutoApply = true
	cfg := decisions.Config{Tasks: map[decisions.TaskName]decisions.TaskConfig{
		decisions.TaskCategorisation: tc, decisions.TaskLabelSuggestion: taskCfg("openai", "alias"), decisions.TaskAlerts: taskCfg("openai", "alias")}}
	p := decisionstest.NewProvider("openai",
		decisionstest.Reply{Output: `{"choice":"c2","confidence":0.75}`, Model: "model-2026-11-01", ResponseID: "resp_1"},
		decisionstest.Reply{Output: `{"choice":null,"confidence":0.2}`},
		decisionstest.Reply{Output: `{"choices":["c2","c1"],"confidence":1.0}`},
		decisionstest.Reply{Output: `{"text":" Rent was not matched by its due date. "}`})
	s := e.service(t, cfg, p)
	ctx := context.Background()
	res, err := s.Run(ctx, decisions.TaskCategorisation, input())
	if err != nil || res.Suggestion == nil {
		t.Fatalf("%+v %v", res, err)
	}
	sg := res.Suggestion
	if sg.Decision.Choice == nil || *sg.Decision.Choice != catRent || sg.Decision.Confidence != "0.75" || sg.Model != "model-2026-11-01" ||
		sg.ResponseID != "resp_1" || !sg.AutoApply || sg.State != decisions.StatePending || !sg.CreatedAt.Equal(t0) {
		t.Fatalf("suggestion %+v", sg)
	}
	res, _ = s.Run(ctx, decisions.TaskCategorisation, input())
	if res.Suggestion.Decision.Choice != nil || res.Suggestion.Model != "alias" {
		t.Fatalf("none choice %+v", res.Suggestion)
	}
	if e.store.All[0].State != decisions.StateSuperseded {
		t.Error("the older pending suggestion was not superseded")
	}
	res, _ = s.Run(ctx, decisions.TaskLabelSuggestion, input())
	if !slices.Equal(res.Suggestion.Decision.Choices, []string{catRent, catGroceries}) || res.Suggestion.AutoApply {
		t.Fatalf("labels %+v", res.Suggestion)
	}
	in := input()
	in.Candidates = nil
	res, _ = s.Run(ctx, decisions.TaskAlerts, in)
	if res.Suggestion == nil || res.Suggestion.Decision.Text != "Rent was not matched by its due date." {
		t.Fatalf("alert %+v", res)
	}
	if strings.Contains(string(p.Sent()[3]), "options") || strings.Contains(string(p.Sent()[3]), "Groceries") {
		t.Error("a generate task sent candidates")
	}
}

// TestOutageChangesNothing: every provider failure yields no suggestion and
// stores nothing; only outages move to an explicit fallback.
func TestOutageChangesNothing(t *testing.T) {
	retry := &decisions.ProviderError{Kind: decisions.KindRetryable}
	for name, tt := range map[string]struct {
		replies  []decisionstest.Reply
		reason   decisions.Reason
		attempts int
	}{
		"unavailable":        {[]decisionstest.Reply{{Err: &decisions.ProviderError{Kind: decisions.KindUnavailable}}}, decisions.ReasonUnavailable, 1},
		"unclassified error": {[]decisionstest.Reply{{Err: errors.New("dial tcp: refused")}}, decisions.ReasonUnavailable, 1},
		"rejected":           {[]decisionstest.Reply{{Err: &decisions.ProviderError{Kind: decisions.KindRejected}}}, decisions.ReasonRejected, 1},
		"retries run out":    {[]decisionstest.Reply{{Err: retry}, {Err: retry}, {Err: retry}, {Output: okChoice}}, decisions.ReasonRateLimited, 3},
		"hung provider":      {[]decisionstest.Reply{{Block: true}}, decisions.ReasonTimeout, 1},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv()
			e.consents.Grant("openai", decisions.TaskCategorisation)
			tc := taskCfg("openai", "m")
			tc.Timeout = 50 * time.Millisecond
			if name == "retries run out" {
				tc.Timeout = 5 * time.Second
			}
			p := decisionstest.NewProvider("openai", tt.replies...)
			res, err := e.service(t, one(decisions.TaskCategorisation, tc), p).Run(context.Background(), decisions.TaskCategorisation, input())
			if err != nil {
				t.Fatal(err)
			}
			if res.Suggestion != nil || res.Reason != tt.reason || res.Attempts != tt.attempts || e.store.Len() != 0 {
				t.Fatalf("result %+v stored %d", res, e.store.Len())
			}
			if got := e.usage.Count(decisions.TaskCategorisation, period); got != tt.attempts {
				t.Errorf("counted %d attempts, made %d", got, tt.attempts)
			}
		})
	}
	t.Run("rejected never falls back", func(t *testing.T) {
		e := newEnv()
		e.consents.Grant("openai", decisions.TaskCategorisation)
		e.consents.Grant("anthropic", decisions.TaskCategorisation)
		tc := taskCfg("openai", "m")
		tc.Fallback = []decisions.Route{{Provider: "anthropic", Model: "m"}}
		p := decisionstest.NewProvider("openai", decisionstest.Reply{Err: &decisions.ProviderError{Kind: decisions.KindRejected}})
		fb := decisionstest.NewProvider("anthropic", decisionstest.Reply{Output: okChoice})
		res, _ := e.service(t, one(decisions.TaskCategorisation, tc), p, fb).Run(context.Background(), decisions.TaskCategorisation, input())
		if res.Reason != decisions.ReasonRejected || fb.Calls() != 0 {
			t.Fatalf("%+v, fallback calls %d", res, fb.Calls())
		}
	})
	t.Run("all routes down", func(t *testing.T) {
		e := newEnv()
		e.consents.Grant("openai", decisions.TaskCategorisation)
		e.consents.Grant("anthropic", decisions.TaskCategorisation)
		tc := taskCfg("openai", "m")
		tc.Retries = 0
		tc.Fallback = []decisions.Route{{Provider: "anthropic", Model: "m"}}
		p := decisionstest.NewProvider("openai", decisionstest.Reply{Err: retry})
		fb := decisionstest.NewProvider("anthropic", decisionstest.Reply{Err: &decisions.ProviderError{Kind: decisions.KindUnavailable}})
		res, _ := e.service(t, one(decisions.TaskCategorisation, tc), p, fb).Run(context.Background(), decisions.TaskCategorisation, input())
		if res.Reason != decisions.ReasonUnavailable || res.Attempts != 2 || e.store.Len() != 0 || res.Provider != "anthropic" {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("cancelled context is an error, not a result", func(t *testing.T) {
		e := newEnv()
		e.consents.Grant("openai", decisions.TaskCategorisation)
		ctx, cancel := context.WithCancel(context.Background())
		p := decisionstest.NewProvider("openai", decisionstest.Reply{Block: true})
		go func() { time.Sleep(20 * time.Millisecond); cancel() }()
		if _, err := e.service(t, one(decisions.TaskCategorisation, taskCfg("openai", "m")), p).Run(ctx, decisions.TaskCategorisation, input()); !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v", err)
		}
		if e.store.Len() != 0 {
			t.Fatal("stored")
		}
	})
}

func TestRetriesHonourRetryAfterAndCountEveryAttempt(t *testing.T) {
	e := newEnv()
	e.consents.Grant("openai", decisions.TaskCategorisation)
	p := decisionstest.NewProvider("openai",
		decisionstest.Reply{Err: &decisions.ProviderError{Kind: decisions.KindRetryable, RetryAfter: 200 * time.Millisecond}},
		decisionstest.Reply{Err: &decisions.ProviderError{Kind: decisions.KindRetryable}},
		decisionstest.Reply{Output: okChoice})
	res, err := e.service(t, one(decisions.TaskCategorisation, taskCfg("openai", "m")), p).Run(context.Background(), decisions.TaskCategorisation, input())
	if err != nil || res.Suggestion == nil || res.Attempts != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	if len(e.slept) != 2 || e.slept[0] != 200*time.Millisecond || e.slept[1] < time.Second || e.slept[1] > 1500*time.Millisecond {
		t.Errorf("waits %v", e.slept)
	}
	if e.usage.Count(decisions.TaskCategorisation, period) != 3 {
		t.Error("not every attempt was counted")
	}
}

func TestRetryAfterBeyondTheTimeLimitStops(t *testing.T) {
	e := newEnv()
	e.consents.Grant("openai", decisions.TaskCategorisation)
	p := decisionstest.NewProvider("openai", decisionstest.Reply{Err: &decisions.ProviderError{Kind: decisions.KindRetryable, RetryAfter: time.Hour}}, decisionstest.Reply{Output: okChoice})
	res, _ := e.service(t, one(decisions.TaskCategorisation, taskCfg("openai", "m")), p).Run(context.Background(), decisions.TaskCategorisation, input())
	if res.Reason != decisions.ReasonRateLimited || p.Calls() != 1 || len(e.slept) != 0 {
		t.Fatalf("%+v calls %d", res, p.Calls())
	}
}

func TestMonthlyCapStopsBeforeSending(t *testing.T) {
	e := newEnv()
	e.consents.Grant("openai", decisions.TaskCategorisation)
	tc := taskCfg("openai", "m")
	tc.MonthlyRequests = 2
	p := decisionstest.NewProvider("openai", decisionstest.Reply{Err: &decisions.ProviderError{Kind: decisions.KindRetryable}},
		decisionstest.Reply{Output: okChoice}, decisionstest.Reply{Output: okChoice})
	s := e.service(t, one(decisions.TaskCategorisation, tc), p)
	if res, _ := s.Run(context.Background(), decisions.TaskCategorisation, input()); res.Suggestion == nil || res.Attempts != 2 {
		t.Fatalf("%+v", res)
	}
	res, _ := s.Run(context.Background(), decisions.TaskCategorisation, input())
	if res.Reason != decisions.ReasonBudget || p.Calls() != 2 || res.Attempts != 0 {
		t.Fatalf("%+v calls %d", res, p.Calls())
	}
}

// TestTypeSafeMatchingConsentDoesNotEnableOthers: consent is per provider and
// purpose. Consent for TypeSafe payment matching enables neither TypeSafe for
// another purpose nor any other provider (OpenRouter and what it reaches
// included) for matching, and nothing is sent without it.
func TestTypeSafeMatchingConsentDoesNotEnableOthers(t *testing.T) {
	ctx := context.Background()
	e := newEnv()
	e.consents.Grant(decisions.ProviderTypeSafe, decisions.TaskPaymentMatching)
	ts := decisionstest.NewProvider("typesafe", decisionstest.Reply{Err: &decisions.ProviderError{Kind: decisions.KindUnavailable}}, decisionstest.Reply{Output: okChoice})
	or := decisionstest.NewProvider("openrouter", decisionstest.Reply{Output: okChoice}, decisionstest.Reply{Output: okChoice})
	oa := decisionstest.NewProvider("openai", decisionstest.Reply{Output: okChoice})
	matching := taskCfg("typesafe", "system-one")
	matching.Retries = 0
	matching.Fallback = []decisions.Route{{Provider: "openrouter", Model: "vendor/model"}}
	cfg := decisions.Config{
		Tasks: map[decisions.TaskName]decisions.TaskConfig{
			decisions.TaskPaymentMatching: matching,
			decisions.TaskCategorisation:  taskCfg("typesafe", "system-one"),
			decisions.TaskLabelSuggestion: taskCfg("openai", "m"),
		},
		Upstreams: map[string][]string{"openrouter": {"anthropic"}},
	}
	s := e.service(t, cfg, ts, or, oa)

	res, err := s.Run(ctx, decisions.TaskPaymentMatching, input())
	if err != nil || res.Reason != decisions.ReasonNoConsent || res.Provider != "openrouter" || or.Calls() != 0 {
		t.Fatalf("fallback to openrouter without its consent: %+v %v (openrouter calls %d)", res, err, or.Calls())
	}
	for task, p := range map[decisions.TaskName]*decisionstest.Provider{decisions.TaskCategorisation: ts, decisions.TaskLabelSuggestion: oa} {
		before := p.Calls()
		res, err := s.Run(ctx, task, input())
		if err != nil || res.Reason != decisions.ReasonNoConsent || p.Calls() != before {
			t.Fatalf("%s: %+v %v", task, res, err)
		}
	}
	if e.store.Len() != 0 {
		t.Fatal("something was stored")
	}
	if res, _ := s.Run(ctx, decisions.TaskPaymentMatching, input()); res.Suggestion == nil || res.Provider != "typesafe" {
		t.Fatalf("matching with its own consent: %+v", res)
	}
	// OpenRouter consent for another purpose does not cover matching either.
	e.consents.Grant("openrouter", decisions.TaskCategorisation)
	ts.Script(decisionstest.Reply{Err: &decisions.ProviderError{Kind: decisions.KindUnavailable}})
	if res, _ := s.Run(ctx, decisions.TaskPaymentMatching, input()); res.Reason != decisions.ReasonNoConsent || or.Calls() != 0 {
		t.Fatalf("%+v", res)
	}
}

func TestConfigValidation(t *testing.T) {
	all := []decisions.Provider{decisionstest.NewProvider("openrouter"), decisionstest.NewProvider("openai"),
		decisionstest.NewProvider("anthropic"), decisionstest.NewProvider("typesafe")}
	for name, tt := range map[string]struct {
		task decisions.TaskName
		mod  func(*decisions.TaskConfig)
		want string
	}{
		"typesafe cannot generate":     {decisions.TaskAlerts, func(c *decisions.TaskConfig) { c.Provider = "typesafe" }, "cannot serve the generate capability"},
		"capability mismatch":          {decisions.TaskCategorisation, func(c *decisions.TaskConfig) { c.Capability = decisions.Generate }, "capability must be decide"},
		"auto-apply on labels":         {decisions.TaskLabelSuggestion, func(c *decisions.TaskConfig) { c.AutoApply = true }, "categorisation only"},
		"auto-apply on matching":       {decisions.TaskPaymentMatching, func(c *decisions.TaskConfig) { c.AutoApply = true }, "categorisation only"},
		"openrouter without upstreams": {decisions.TaskCategorisation, func(c *decisions.TaskConfig) { c.Provider = "openrouter" }, "upstream"},
		"fallback without upstreams": {decisions.TaskCategorisation, func(c *decisions.TaskConfig) {
			c.Fallback = []decisions.Route{{Provider: "openrouter", Model: "x"}}
		}, "upstream"},
		"decide without candidate names": {decisions.TaskCategorisation, func(c *decisions.TaskConfig) { c.Fields = []decisions.Field{decisions.FieldDescription} }, "candidate_names"},
		"three retries":                  {decisions.TaskCategorisation, func(c *decisions.TaskConfig) { c.Retries = 3 }, "retries"},
		"no cap":                         {decisions.TaskCategorisation, func(c *decisions.TaskConfig) { c.MonthlyRequests = 0 }, "cap"},
		"no timeout":                     {decisions.TaskCategorisation, func(c *decisions.TaskConfig) { c.Timeout = 0 }, "timeout"},
		"no model":                       {decisions.TaskCategorisation, func(c *decisions.TaskConfig) { c.Model = "" }, "model"},
		"unknown provider":               {decisions.TaskCategorisation, func(c *decisions.TaskConfig) { c.Provider = "mystery" }, "not available"},
		"repeated fallback":              {decisions.TaskCategorisation, func(c *decisions.TaskConfig) { c.Fallback = []decisions.Route{c.Route} }, "repeats"},
		"unknown task":                   {"budgeting", func(*decisions.TaskConfig) {}, "unknown task"},
	} {
		t.Run(name, func(t *testing.T) {
			tc := taskCfg("openai", "m")
			tt.mod(&tc)
			cfg := decisions.Config{Tasks: map[decisions.TaskName]decisions.TaskConfig{tt.task: tc}}
			_, err := decisions.New(cfg, all, depsOnly(newEnv()))
			var ce *decisions.ConfigError
			if !errors.As(err, &ce) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err %v, want %q", err, tt.want)
			}
		})
	}
	t.Run("an enabled task whose adapter is missing", func(t *testing.T) {
		if _, err := decisions.New(one(decisions.TaskCategorisation, taskCfg("anthropic", "m")), nil, depsOnly(newEnv())); err == nil || !strings.Contains(err.Error(), "not available") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("disabled tasks are not checked", func(t *testing.T) {
		tc := taskCfg("typesafe", "")
		tc.Enabled = false
		if _, err := decisions.New(one(decisions.TaskAlerts, tc), nil, depsOnly(newEnv())); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("an adapter whose own descriptor lacks the capability", func(t *testing.T) {
		p := decisionstest.NewProvider("anthropic")
		p.Desc.Capabilities = []decisions.Capability{decisions.Decide}
		if _, err := decisions.New(one(decisions.TaskAlerts, taskCfg("anthropic", "m")), []decisions.Provider{p}, depsOnly(newEnv())); err == nil {
			t.Fatal("accepted")
		}
	})
}

func TestDisabledTaskAndBadInputSendNothing(t *testing.T) {
	e := newEnv()
	e.consents.Grant("openai", decisions.TaskCategorisation)
	p := decisionstest.NewProvider("openai", decisionstest.Reply{Output: okChoice})
	s := e.service(t, one(decisions.TaskCategorisation, taskCfg("openai", "m")), p)
	ctx := context.Background()
	if res, _ := s.Run(ctx, decisions.TaskAlerts, decisions.Input{Object: decisions.ObjectRef{Type: "occurrence", ID: "x"}}); res.Reason != decisions.ReasonDisabled {
		t.Fatalf("%+v", res)
	}
	for name, mod := range map[string]func(*decisions.Input){
		"no candidates":       func(in *decisions.Input) { in.Candidates = nil },
		"duplicate candidate": func(in *decisions.Input) { in.Candidates = append(in.Candidates, in.Candidates[0]) },
		"blank name":          func(in *decisions.Input) { in.Candidates[0].Name = " " },
		"bad object":          func(in *decisions.Input) { in.Object.Type = "Bad Type" },
	} {
		in := input()
		mod(&in)
		if res, _ := s.Run(ctx, decisions.TaskCategorisation, in); res.Reason != decisions.ReasonInvalidInput {
			t.Errorf("%s: %+v", name, res)
		}
	}
	if p.Calls() != 0 {
		t.Fatal("sent")
	}
}

func TestSchemaHoldsNoUserData(t *testing.T) {
	for _, task := range decisions.Tasks() {
		s := string(decisions.Schema(task))
		if !strings.Contains(s, `"additionalProperties":false`) {
			t.Errorf("%s: schema allows extra members: %s", task.Name, s)
		}
	}
	e := newEnv()
	e.consents.Grant("openai", decisions.TaskCategorisation)
	p := decisionstest.NewProvider("openai", decisionstest.Reply{Output: okChoice}, decisionstest.Reply{Output: okChoice})
	s := e.service(t, one(decisions.TaskCategorisation, taskCfg("openai", "m")), p)
	_, _ = s.Run(context.Background(), decisions.TaskCategorisation, input())
	in := input()
	in.Candidates = []decisions.Candidate{{ID: "a", Name: "Utilities"}}
	_, _ = s.Run(context.Background(), decisions.TaskCategorisation, in)
	schema := func(b []byte) string { i := bytes.Index(b, []byte(`"schema":`)); return string(b[i:]) }
	if schema(p.Sent()[0]) != schema(p.Sent()[1]) {
		t.Error("the schema varies with the request")
	}
}

func TestLongValuesAreCut(t *testing.T) {
	e := newEnv()
	e.consents.Grant("openai", decisions.TaskCategorisation)
	p := decisionstest.NewProvider("openai", decisionstest.Reply{Output: okChoice})
	in := input()
	in.Values[decisions.FieldDescription] = strings.Repeat("x", 2000)
	_, _ = e.service(t, one(decisions.TaskCategorisation, taskCfg("openai", "m")), p).Run(context.Background(), decisions.TaskCategorisation, in)
	if bytes.Contains(p.Sent()[0], []byte(strings.Repeat("x", decisions.MaxValueRunes+1))) {
		t.Fatal("a value was not cut")
	}
}
