package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// Exchange-rate routes (task FX2c, ADR-0048).
const (
	RatesPath          = APIPrefix + "/rates"
	RatesStatusPath    = APIPrefix + "/rates/status"
	RatesRefreshesPath = APIPrefix + "/rates/refreshes"
)

var (
	grantRatesRead  = authz.G(authz.ResourceRates, authz.ActionRead)
	grantRatesWrite = authz.G(authz.ResourceRates, authz.ActionWrite)
)

// CodeRatesRefreshUnavailable: 409, no exchange-rate refresh can run: the rate
// provider is not enabled (rates.enabled) or no reporting currency is set.
const CodeRatesRefreshUnavailable Code = "rates_refresh_unavailable"

// RatePolicy is what the rates routes need from the rate service: the stale
// test and the policy it applies, and which provider and source it serves.
// *fx.Service implements it.
type RatePolicy interface {
	StaleReason(r fx.Rate) fx.Reason
	Freshness() fx.Freshness
	HasProvider() bool
	ProviderName() string
	Source() string
}

// RatesRefresher is the refresh job as the routes use it. *fx.RefreshJob
// implements it.
type RatesRefresher interface {
	Request(ctx context.Context) (fx.RunStatus, error)
	Last() (fx.RunStatus, bool)
	LastSuccess() time.Time
}

// RatesDeps wires the rates routes. The zero value (and a nil pointer) answers
// every route with 503.
type RatesDeps struct {
	Lister fx.Lister
	Policy RatePolicy
	// Refresher is nil when no refresh job exists (rates.enabled is false or
	// no reporting currency is set); a manual refresh is then 409.
	Refresher RatesRefresher
	// Target is the reporting currency rates are fetched to (zero: none).
	Target money.Currency
	// Interval is rates.refresh_interval.
	Interval time.Duration
	// Timeout bounds one manual refresh; zero means only the request's own
	// context.
	Timeout time.Duration
	// Audit records manual refreshes.
	Audit audit.Recorder
	Now   func() time.Time
}

func (d *RatesDeps) ready() bool {
	return d != nil && d.Lister != nil && d.Policy != nil
}

type ratePreviousJSON struct {
	Value     string    `json:"value"`
	RevisedAt time.Time `json:"revised_at"`
}

// rateJSON is one cached rate with its provenance. Rate is the exact decimal
// as the provider published it (Value), a string so no client rounds it. The
// shared fields are named like the outlook's exchange rate.
type rateJSON struct {
	Provider         string            `json:"provider"`
	Source           string            `json:"source"`
	Base             string            `json:"base"`
	Quote            string            `json:"quote"`
	Value            string            `json:"value"`
	Date             string            `json:"date"`
	RetrievedAt      time.Time         `json:"retrieved_at"`
	FirstRetrievedAt time.Time         `json:"first_retrieved_at"`
	ConfirmedThrough string            `json:"confirmed_through"`
	Stale            bool              `json:"stale"`
	StaleReason      *string           `json:"stale_reason"`
	Previous         *ratePreviousJSON `json:"previous"`
}

func toRateJSON(r fx.Rate, p RatePolicy) rateJSON {
	out := rateJSON{Provider: r.Provider, Source: r.Source, Base: r.Pair.Base.Code(), Quote: r.Pair.Quote.Code(),
		Value: r.Value.String(), Date: r.Date.String(), RetrievedAt: r.RetrievedAt.UTC(), FirstRetrievedAt: r.FirstRetrievedAt.UTC(),
		ConfirmedThrough: r.Confirmed().String()}
	if why := p.StaleReason(r); why != fx.ReasonNone {
		s := string(why)
		out.Stale, out.StaleReason = true, &s
	}
	if r.Previous != nil {
		out.Previous = &ratePreviousJSON{Value: r.Previous.Value.String(), RevisedAt: r.Previous.RevisedAt.UTC()}
	}
	return out
}

// rateKeySep separates the parts of a rate's list key (a provider name, a
// source key, two currency codes and a date contain none of it).
const rateKeySep = "|"

func rateKeyOf(r rateJSON) string {
	return strings.Join([]string{r.Provider, r.Source, r.Base, r.Quote, r.Date}, rateKeySep)
}

func parseRateKey(s string) (fx.RateKey, bool) {
	parts := strings.Split(s, rateKeySep)
	if len(parts) != 5 || parts[0] == "" || parts[1] == "" {
		return fx.RateKey{}, false
	}
	base, err1 := money.ParseCurrency(parts[2])
	quote, err2 := money.ParseCurrency(parts[3])
	date, err3 := calendar.ParseDate(parts[4])
	if err1 != nil || err2 != nil || err3 != nil {
		return fx.RateKey{}, false
	}
	return fx.RateKey{Provider: parts[0], Source: parts[1], Pair: fx.Pair{Base: base, Quote: quote}, Date: date}, true
}

var providerFilter = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

func checkCurrencyCode(v string) error {
	if _, err := money.ParseCurrency(v); err != nil {
		return errors.New("must be an ISO 4217 currency code in capitals, such as EUR")
	}
	return nil
}

func checkISODate(v string) error {
	if _, err := calendar.ParseDate(v); err != nil {
		return errors.New("must be a date as YYYY-MM-DD")
	}
	return nil
}

var ratesListSpec = ListSpec{
	Filters: map[string]FilterSpec{
		"provider": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
			if !providerFilter.MatchString(v) {
				return errors.New("must be a provider name such as frankfurter")
			}
			return nil
		}},
		"source": {Ops: []FilterOp{OpEq}, Check: checkMax(32)},
		"base":   {Ops: []FilterOp{OpEq}, Check: checkCurrencyCode},
		"quote":  {Ops: []FilterOp{OpEq}, Check: checkCurrencyCode},
		"date":   {Ops: []FilterOp{OpGte, OpLte}, Check: checkISODate},
	},
	CursorKey: func(k string) bool { _, ok := parseRateKey(k); return ok },
}

type rateRunErrorJSON struct {
	Kind string `json:"kind"`
	Code string `json:"code"`
}

// rateRunJSON is one refresh run: what it did and how it ended. It carries a
// provider's error kind and short code only.
type rateRunJSON struct {
	Trigger    string            `json:"trigger"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt time.Time         `json:"finished_at"`
	Outcome    string            `json:"outcome"`
	Refreshed  int               `json:"currencies_refreshed"`
	Error      *rateRunErrorJSON `json:"error"`
}

func toRateRunJSON(s fx.RunStatus) rateRunJSON {
	out := rateRunJSON{Trigger: s.Trigger, StartedAt: s.StartedAt.UTC(), FinishedAt: s.FinishedAt.UTC(), Outcome: s.Outcome, Refreshed: s.Refreshed}
	if s.ErrorKind != "" {
		out.Error = &rateRunErrorJSON{Kind: s.ErrorKind, Code: s.ErrorCode}
	}
	return out
}

type ratesStatusJSON struct {
	// Enabled: a rate provider is configured (rates.enabled). When false the
	// rates listed are what was cached while it was on; nothing refreshes them.
	Enabled           bool    `json:"enabled"`
	Provider          string  `json:"provider"`
	Source            string  `json:"source"`
	ReportingCurrency *string `json:"reporting_currency"`
	// RefreshAvailable: a refresh job exists (enabled and a reporting
	// currency), so POST /rates/refreshes can run.
	RefreshAvailable bool `json:"refresh_available"`
	RefreshInterval  int  `json:"refresh_interval_seconds"`
	// RefreshAfterSeconds and StaleAfterDays are the freshness policy (D11):
	// a rate retrieved this recently is used as is; one older than the stale
	// threshold is never used in a converted total.
	RefreshAfterSeconds int          `json:"refresh_after_seconds"`
	StaleAfterDays      int          `json:"stale_after_days"`
	LastRun             *rateRunJSON `json:"last_run"`
	LastSuccessAt       *time.Time   `json:"last_success_at"`
	AsOf                time.Time    `json:"as_of"`
}

func registerRates(s *Server, d *RatesDeps, now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	unavailable := func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, Unavailable("exchange rates are not available", nil))
	}

	// recordRefresh audits an accepted manual refresh. It uses a context the
	// caller cannot cancel: a client that went away must not erase the record
	// of a run that already asked the provider.
	recordRefresh := func(r *http.Request, run fx.RunStatus, outcome string) error {
		if d.Audit == nil {
			return nil
		}
		if outcome == "" {
			outcome = run.Outcome
		}
		details := map[string]string{"outcome": outcome, "refreshed": strconv.Itoa(run.Refreshed)}
		if run.ErrorKind != "" {
			details["error_kind"], details["error_code"] = run.ErrorKind, run.ErrorCode
		}
		ev := audit.Event{Action: audit.RatesRefreshRequested, Actor: actor(r), TargetType: "rates", TargetID: "refresh", At: now().UTC(), Details: details}
		return d.Audit.Record(context.WithoutCancel(r.Context()), ev)
	}

	// Listing reads the cache only; it never calls the provider. Rates come
	// grouped by provider, source and pair, newest rate date first.
	s.HandleAPI("GET "+RatesPath, Requires(grantRatesRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, ratesListSpec)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		for _, field := range []string{"provider", "source", "base", "quote"} {
			if len(q.Filter(field, OpEq)) > 1 {
				WriteError(w, r, Validation(field, "this filter must be given once"))
				return
			}
		}
		for _, op := range []FilterOp{OpGte, OpLte} {
			if len(q.Filter("date", op)) > 1 {
				WriteError(w, r, Validation("date["+string(op)+"]", "this filter must be given once"))
				return
			}
		}
		if !d.ready() {
			unavailable(w, r)
			return
		}
		f := fx.ListFilter{Limit: q.Limit + 1}
		one := func(field string) string {
			if v := q.Filter(field, OpEq); len(v) == 1 {
				return v[0]
			}
			return ""
		}
		f.Provider, f.Source = one("provider"), one("source")
		f.Base, _ = money.ParseCurrency(one("base"))
		f.Quote, _ = money.ParseCurrency(one("quote"))
		if v := q.Filter("date", OpGte); len(v) == 1 {
			f.From, _ = calendar.ParseDate(v[0])
		}
		if v := q.Filter("date", OpLte); len(v) == 1 {
			f.To, _ = calendar.ParseDate(v[0])
		}
		if q.After != "" {
			k, _ := parseRateKey(q.After)
			f.After = &k
		}
		rates, err := d.Lister.List(r.Context(), f)
		if err != nil {
			WriteError(w, r, Internal(err))
			return
		}
		out := make([]rateJSON, len(rates))
		for i, rt := range rates {
			out[i] = toRateJSON(rt, d.Policy)
		}
		WriteJSON(w, http.StatusOK, NewPage(out, q, rateKeyOf))
	}))

	// Status reports on the rate provider without gating anything: it is
	// optional, so it is never a readiness dependency (ADR-0048).
	s.HandleAPI("GET "+RatesStatusPath, Requires(grantRatesRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.ready() {
			unavailable(w, r)
			return
		}
		fr := d.Policy.Freshness()
		out := ratesStatusJSON{Enabled: d.Policy.HasProvider(), Provider: d.Policy.ProviderName(), Source: d.Policy.Source(),
			RefreshAvailable: d.Refresher != nil, RefreshInterval: int(d.Interval / time.Second),
			RefreshAfterSeconds: int(fr.RefreshAfter / time.Second), StaleAfterDays: int(fr.StaleAfter / (24 * time.Hour)), AsOf: now().UTC()}
		if !d.Target.IsZero() {
			c := d.Target.Code()
			out.ReportingCurrency = &c
		}
		if d.Refresher != nil {
			if last, ok := d.Refresher.Last(); ok {
				run := toRateRunJSON(last)
				out.LastRun = &run
			}
			out.LastSuccessAt = timePtr(d.Refresher.LastSuccess())
		}
		WriteJSON(w, http.StatusOK, out)
	}))

	// A manual refresh asks the provider once for every currency in use,
	// bounded by a timeout, never overlapping another run and limited to one
	// a minute. It is audited whatever its outcome.
	s.HandleAPI("POST "+RatesRefreshesPath, Requires(grantRatesWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No options: the body is empty or an empty object.
		doc, err := decodeObject(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		for k := range doc {
			WriteError(w, r, Validation(k, "unknown field "+k))
			return
		}
		if !d.ready() {
			unavailable(w, r)
			return
		}
		if d.Refresher == nil {
			WriteError(w, r, &Error{Status: http.StatusConflict, Code: CodeRatesRefreshUnavailable,
				Message: "no exchange-rate refresh can run: set rates.enabled and a reporting currency (reporting.currency)"})
			return
		}
		ctx := r.Context()
		if d.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d.Timeout)
			defer cancel()
		}
		run, err := d.Refresher.Request(ctx)
		var soon *fx.TooSoonError
		switch {
		case errors.As(err, &soon):
			w.Header().Set("Retry-After", strconv.Itoa(int((soon.RetryAfter+time.Second-1)/time.Second)))
			WriteError(w, r, RateLimited("exchange rates were refreshed on request a moment ago; try again shortly"))
			return
		case errors.Is(err, fx.ErrRefreshInProgress):
			w.Header().Set("Retry-After", "30")
			WriteError(w, r, &Error{Status: http.StatusConflict, Code: CodeRefreshInProgress, Message: "an exchange-rate refresh is already running; try again shortly", Cause: err})
			return
		case errors.Is(err, fx.ErrRefreshIdle):
			WriteError(w, r, &Error{Status: http.StatusConflict, Code: CodeRatesRefreshUnavailable,
				Message: "no exchange-rate refresh can run: set rates.enabled and a reporting currency (reporting.currency)", Cause: err})
			return
		case errors.Is(err, context.Canceled):
			// The caller went away. A run that had already succeeded keeps
			// its real outcome; one cut short is audited as cancelled
			// (ADR-0048 point 10). A request with no run status never
			// started a run, so it leaves no row.
			switch run.Outcome {
			case "":
				WriteError(w, r, Internal(err))
			case fx.RunSucceeded:
				WriteError(w, r, Internal(errors.Join(err, recordRefresh(r, run, ""))))
			default:
				WriteError(w, r, Internal(errors.Join(err, recordRefresh(r, run, "cancelled"))))
			}
			return
		case err != nil:
			WriteError(w, r, Internal(err))
			return
		}
		if err := recordRefresh(r, run, ""); err != nil {
			WriteError(w, r, Internal(err))
			return
		}
		WriteJSON(w, http.StatusOK, toRateRunJSON(run))
	}))
}
