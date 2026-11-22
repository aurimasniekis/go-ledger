package decisions

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits.
const (
	// MaxRetries is the most retries of one request (decision D13: two).
	MaxRetries = 2
	// MaxTimeout is the longest per-task time limit.
	MaxTimeout = 2 * time.Minute
	// MaxValueRunes is the longest field value or candidate name sent; longer
	// values are cut.
	MaxValueRunes = 500
)

// Route is a provider and a model.
type Route struct {
	Provider string
	Model    string
}

func (r Route) String() string { return r.Provider + ":" + r.Model }

// TaskConfig is one task's configuration (decision D13: per-task {provider,
// model, timeout, budget}, plus the capability of the control default).
type TaskConfig struct {
	Enabled bool
	Route
	// Capability must be the task's; empty means the task's.
	Capability Capability
	// Timeout bounds one run on one route, retries included.
	Timeout time.Duration
	// MonthlyRequests caps attempts per calendar month (UTC), every attempt
	// on every route counted.
	MonthlyRequests int
	// Retries of a failure returned before generation, 0 to MaxRetries.
	Retries int
	// AutoApply marks suggestions for automatic application
	// (categorisation only).
	AutoApply bool
	// Fields is the allowlist for the primary route; nil means
	// [DefaultFields] for its provider. Fallback routes always use their
	// provider's defaults.
	Fields []Field
	// Fallback is an explicit, ordered list of routes tried only when the
	// one before had an outage. Never automatic: empty means none.
	Fallback []Route
}

// Config is the decision layer's configuration.
type Config struct {
	Tasks map[TaskName]TaskConfig
	// Upstreams lists, per routing provider, the upstream providers it may
	// use. A routing provider without any is refused.
	Upstreams map[string][]string
}

// AnyEnabled reports whether some task is enabled.
func (c Config) AnyEnabled() bool {
	for _, t := range c.Tasks {
		if t.Enabled {
			return true
		}
	}
	return false
}

// ConfigError lists configuration problems.
type ConfigError struct{ Problems []string }

func (e *ConfigError) Error() string {
	return "decision providers: " + strings.Join(e.Problems, "; ")
}

// fieldsFor is the allowlist of a route of a task.
func fieldsFor(task TaskName, tc TaskConfig, r Route, primary bool) []Field {
	if primary && tc.Fields != nil {
		return tc.Fields
	}
	return DefaultFields(r.Provider, task)
}

// ValidateConfig checks a configuration against provider descriptors: every
// enabled task is known, its capability is the task's and served by each of
// its routes' providers, limits are in range, auto-apply is only on
// categorisation, fields are permitted, a decide task may send candidate
// names, and a routing provider has upstreams. Disabled tasks are not checked.
func ValidateConfig(c Config, descriptors []Descriptor) []string {
	var ps []string
	names := make([]string, 0, len(c.Tasks))
	for n := range c.Tasks {
		names = append(names, string(n))
	}
	slices.Sort(names)
	for _, n := range names {
		name := TaskName(n)
		tc := c.Tasks[name]
		bad := func(format string, a ...any) { ps = append(ps, string(name)+": "+fmt.Sprintf(format, a...)) }
		t, ok := LookupTask(name)
		if !ok {
			bad("unknown task")
			continue
		}
		if !tc.Enabled {
			continue
		}
		if tc.Capability != "" && tc.Capability != t.Capability {
			bad("capability must be %s for this task", t.Capability)
		}
		if tc.Timeout <= 0 || tc.Timeout > MaxTimeout {
			bad("timeout must be above 0 and at most %s", MaxTimeout)
		}
		if tc.MonthlyRequests < 1 {
			bad("monthly request cap must be at least 1")
		}
		if tc.Retries < 0 || tc.Retries > MaxRetries {
			bad("retries must be from 0 to %d", MaxRetries)
		}
		if tc.AutoApply && !t.AutoApplyAllowed {
			bad("auto-apply is allowed for categorisation only (decision D13)")
		}
		for _, f := range tc.Fields {
			if !IsPermitted(f) {
				bad("field %s is not permitted", f)
			}
		}
		routes := append([]Route{tc.Route}, tc.Fallback...)
		seen := map[Route]bool{}
		for i, r := range routes {
			label := "provider"
			if i > 0 {
				label = fmt.Sprintf("fallback %d", i)
			}
			if seen[r] {
				bad("%s repeats %s", label, r)
			}
			seen[r] = true
			if r.Model == "" {
				bad("%s needs a model", label)
			}
			at := slices.IndexFunc(descriptors, func(d Descriptor) bool { return d.Name == r.Provider })
			if at < 0 {
				bad("%s %q is not available", label, r.Provider)
				continue
			}
			d := descriptors[at]
			if !d.Supports(t.Capability) {
				bad("%s %s cannot serve the %s capability", label, r.Provider, t.Capability)
			}
			if d.Routing && len(c.Upstreams[r.Provider]) == 0 {
				bad("%s %s forwards to other providers: name the allowed upstream providers first (decision D13)", label, r.Provider)
			}
			if t.Capability == Decide && !slices.Contains(fieldsFor(name, tc, r, i == 0), FieldCandidateNames) {
				bad("%s %s: a decide task must be allowed to send candidate_names", label, r.Provider)
			}
		}
	}
	return ps
}

// Deps are the service's ports.
type Deps struct {
	Consents    Consents
	Usage       Usage
	Suggestions Suggestions
	// Now defaults to time.Now; Sleep to a context-aware timer.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// Service runs decision tasks under the policy.
type Service struct {
	cfg       Config
	providers map[string]Provider
	deps      Deps
}

// New returns a service for cfg with the given adapters. Every route of an
// enabled task must have its adapter here, and the adapter's own descriptor
// must serve the task's capability.
func New(cfg Config, providers []Provider, deps Deps) (*Service, error) {
	if deps.Consents == nil || deps.Usage == nil || deps.Suggestions == nil {
		return nil, errors.New("decisions.New: consents, usage and suggestions are required")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Sleep == nil {
		deps.Sleep = sleepCtx
	}
	byName := map[string]Provider{}
	var descs []Descriptor
	for _, p := range providers {
		d := p.Descriptor()
		if _, known := KnownProvider(d.Name); !known {
			return nil, fmt.Errorf("decisions.New: unknown provider %q", d.Name)
		}
		byName[d.Name] = p
		descs = append(descs, d)
	}
	if ps := ValidateConfig(cfg, descs); len(ps) > 0 {
		return nil, &ConfigError{Problems: ps}
	}
	return &Service{cfg: cfg, providers: byName, deps: deps}, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Candidate is one choice offered to a decide task: the caller's identifier
// (never sent) and the name shown to the model.
type Candidate struct {
	ID   string
	Name string
}

// Input is what the caller knows about the object. Values may hold any
// field, the excluded ones included: the policy decides what is sent.
type Input struct {
	Object     ObjectRef
	Values     map[Field]string
	Candidates []Candidate
}

// Reason says why a run produced no suggestion.
type Reason string

// Reasons.
const (
	ReasonDisabled      Reason = "disabled"             // the task is not enabled
	ReasonInvalidInput  Reason = "invalid_input"        // the caller's input does not fit the task
	ReasonNoConsent     Reason = "no_consent"           // no consent for this provider and purpose
	ReasonBudget        Reason = "budget_exhausted"     // the monthly request cap is reached
	ReasonRateLimited   Reason = "rate_limited"         // retryable failures until the retries ran out
	ReasonUnavailable   Reason = "provider_unavailable" // an outage
	ReasonTimeout       Reason = "timeout"              // the task's time limit passed
	ReasonRejected      Reason = "provider_rejected"    // the provider refused the request
	ReasonInvalidOutput Reason = "invalid_output"       // the answer failed validation
)

func (r Reason) outage() bool {
	return r == ReasonRateLimited || r == ReasonUnavailable || r == ReasonTimeout
}

// Result is the outcome of a run. Exactly one of Suggestion and Reason is
// set. A run without a suggestion changed nothing (apart from counting
// attempts against the cap).
type Result struct {
	Suggestion *Suggestion
	Reason     Reason
	// Provider is the route's provider that answered, or the last one tried.
	Provider string
	// Attempts counts provider calls made.
	Attempts int
	// Withheld names input fields that were not sent (never their values).
	Withheld []Field
}

// Run asks the task's provider about one object. It returns an error only
// when Ledger itself failed (a store, a cancelled context); every provider or
// policy failure is a Result with a Reason and no suggestion.
func (s *Service) Run(ctx context.Context, name TaskName, in Input) (Result, error) {
	tc, ok := s.cfg.Tasks[name]
	t, known := LookupTask(name)
	if !ok || !known || !tc.Enabled {
		return Result{Reason: ReasonDisabled}, nil
	}
	if !in.Object.Valid() || !validCandidates(t, in.Candidates) {
		return Result{Reason: ReasonInvalidInput}, nil
	}
	var res Result
	for i, r := range append([]Route{tc.Route}, tc.Fallback...) {
		res.Provider = r.Provider
		granted, err := s.deps.Consents.Granted(ctx, r.Provider, name)
		if err != nil {
			return Result{}, fmt.Errorf("decisions: reading consent: %w", err)
		}
		if !granted {
			res.Reason = ReasonNoConsent
			return res, nil
		}
		req, ids, withheld := s.build(t, tc, r, i == 0, in)
		res.Withheld = withheld
		reason, resp, err := s.call(ctx, t, tc, r, req, &res)
		if err != nil {
			return Result{}, err
		}
		if reason == "" {
			d, verr := Validate(t, resp.Output, ids)
			if verr != nil {
				res.Reason = ReasonInvalidOutput
				return res, nil
			}
			model := resp.Model
			if model == "" {
				model = r.Model
			}
			sg, err := s.deps.Suggestions.Record(ctx, NewSuggestion{Task: name, Provider: r.Provider, Model: model,
				ResponseID: resp.ResponseID, Object: in.Object, Decision: d, AutoApply: tc.AutoApply && t.AutoApplyAllowed,
				CreatedAt: s.deps.Now().UTC()})
			if err != nil {
				return Result{}, fmt.Errorf("decisions: recording the suggestion: %w", err)
			}
			res.Suggestion, res.Reason = &sg, ""
			return res, nil
		}
		res.Reason = reason
		if !reason.outage() {
			return res, nil
		}
	}
	return res, nil
}

func validCandidates(t Task, cs []Candidate) bool {
	if t.Capability != Decide {
		return len(cs) == 0
	}
	if len(cs) == 0 || len(cs) > t.MaxCandidates {
		return false
	}
	seen := map[string]bool{}
	for _, c := range cs {
		if c.ID == "" || strings.TrimSpace(c.Name) == "" || seen[c.ID] {
			return false
		}
		seen[c.ID] = true
	}
	return true
}

func clip(s string) string {
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	if utf8.RuneCountInString(s) <= MaxValueRunes {
		return s
	}
	return string([]rune(s)[:MaxValueRunes])
}

// build applies the allowlist: only fields allowed for this provider and
// purpose are copied into the request; excluded fields never are, whatever
// the allowlist says.
func (s *Service) build(t Task, tc TaskConfig, r Route, primary bool, in Input) (Request, map[string]string, []Field) {
	allowed := fieldsFor(t.Name, tc, r, primary)
	req := Request{Task: t.Name, Capability: t.Capability, Output: t.Output, Model: r.Model, Schema: Schema(t),
		MaxChoices: t.MaxChoices, MaxText: t.MaxText}
	if d, _ := KnownProvider(r.Provider); d.Routing {
		req.Upstreams = slices.Clone(s.cfg.Upstreams[r.Provider])
	}
	var withheld []Field
	for _, f := range Permitted() {
		v, ok := in.Values[f]
		if !ok || f == FieldCandidateNames {
			continue
		}
		if slices.Contains(allowed, f) {
			req.Fields = append(req.Fields, FieldValue{Field: f, Value: clip(v)})
		} else {
			withheld = append(withheld, f)
		}
	}
	for f := range in.Values {
		if !IsPermitted(f) {
			withheld = append(withheld, f)
		}
	}
	slices.Sort(withheld)
	ids := map[string]string{}
	if t.Capability == Decide && slices.Contains(allowed, FieldCandidateNames) {
		for i, c := range in.Candidates {
			k := fmt.Sprintf("c%d", i+1)
			ids[k] = c.ID
			req.Options = append(req.Options, Option{Key: k, Name: clip(c.Name)})
		}
	}
	return req, ids, withheld
}

// call makes the attempts of one route: every attempt is reserved against the
// monthly cap first; only retryable failures are retried, honouring
// Retry-After, within the task's time limit.
func (s *Service) call(ctx context.Context, t Task, tc TaskConfig, r Route, req Request, res *Result) (Reason, Response, error) {
	p := s.providers[r.Provider]
	rctx, cancel := context.WithTimeout(ctx, tc.Timeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		ok, err := s.deps.Usage.Reserve(ctx, t.Name, s.deps.Now().UTC().Format("2006-01"), tc.MonthlyRequests)
		if err != nil {
			return "", Response{}, fmt.Errorf("decisions: counting the request: %w", err)
		}
		if !ok {
			return ReasonBudget, Response{}, nil
		}
		res.Attempts++
		resp, err := p.Call(rctx, req)
		if err == nil {
			return "", resp, nil
		}
		if ctx.Err() != nil {
			return "", Response{}, ctx.Err()
		}
		if rctx.Err() != nil {
			return ReasonTimeout, Response{}, nil
		}
		kind, after := kindOf(err)
		switch kind {
		case KindRejected:
			return ReasonRejected, Response{}, nil
		case KindRetryable:
			if attempt >= tc.Retries {
				return ReasonRateLimited, Response{}, nil
			}
			wait := backoff(attempt, after)
			if dl, ok := rctx.Deadline(); ok && time.Until(dl) < wait {
				return ReasonRateLimited, Response{}, nil
			}
			if err := s.deps.Sleep(rctx, wait); err != nil {
				if ctx.Err() != nil {
					return "", Response{}, ctx.Err()
				}
				return ReasonTimeout, Response{}, nil
			}
		default:
			return ReasonUnavailable, Response{}, nil
		}
	}
}

// backoff is the wait before retry n+1: the provider's Retry-After when it
// sent one, else exponential from 500ms with up to 50% jitter (decision D17).
func backoff(n int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	base := 500 * time.Millisecond << n
	return base + rand.N(base/2+1)
}
