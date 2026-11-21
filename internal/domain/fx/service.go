package fx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// Freshness is the policy deciding when a cached rate is used as is, when
// the provider is asked again, and when a rate is too old to be acceptable.
// Decision D11 sets the defaults ([DefaultFreshness]); configuration may
// override them.
type Freshness struct {
	// RefreshAfter: a cached rate retrieved less than this long ago is used
	// without calling the provider. D11: refresh daily.
	RefreshAfter time.Duration
	// StaleAfter: a rate is stale when Ledger last retrieved it this long
	// ago or longer, or when its date is more than StaleAfter (in whole
	// days) before the date it is used for. D11: 5 days.
	StaleAfter time.Duration
}

// DefaultFreshness is decision D11: refresh daily; a rate is stale after
// 5 days.
var DefaultFreshness = Freshness{RefreshAfter: 24 * time.Hour, StaleAfter: 5 * 24 * time.Hour}

// DefaultFailurePause is how long provider calls pause after an unavailable
// or rate-limited failure that names no delay of its own (bounded by
// Freshness.RefreshAfter).
const DefaultFailurePause = 5 * time.Minute

// Validate checks the policy.
func (f Freshness) Validate() error {
	switch {
	case f.RefreshAfter <= 0:
		return errors.New("fx: freshness refresh interval must be positive")
	case f.StaleAfter < 24*time.Hour:
		return errors.New("fx: freshness stale threshold must be at least one day")
	case f.RefreshAfter > f.StaleAfter:
		return errors.New("fx: freshness refresh interval must not exceed the stale threshold")
	}
	return nil
}

func (f Freshness) staleDays() int { return int(f.StaleAfter / (24 * time.Hour)) }

// Status is the outcome of a conversion.
type Status string

const (
	// StatusOK: converted with an acceptable rate (or no conversion was
	// needed). Only these count toward converted totals.
	StatusOK Status = "ok"
	// StatusStale: a cached rate exists but is older than the freshness
	// policy allows. Rate says which; no value is computed.
	StatusStale Status = "stale"
	// StatusMissing: no rate is available. Reason says why.
	StatusMissing Status = "missing"
)

// Reason explains a stale or missing conversion.
type Reason string

const (
	ReasonNone Reason = ""
	// ReasonNotRetrievedRecently: the rate was last retrieved StaleAfter ago
	// or longer (typically the provider has been unreachable).
	ReasonNotRetrievedRecently Reason = "not_retrieved_recently"
	// ReasonRateDateTooOld: the newest rate's date is more than StaleAfter
	// days before the date it is used for.
	ReasonRateDateTooOld Reason = "rate_date_too_old"
	// ReasonUnconfirmed: a dated conversion found only an earlier rate that
	// the provider has not confirmed as the one applying on that date, and
	// the provider could not be asked.
	ReasonUnconfirmed Reason = "unconfirmed"
	// ReasonNotCached: nothing cached and the provider could not be asked
	// (see RefreshError).
	ReasonNotCached Reason = "not_cached"
	// ReasonNotPublished: the provider answered but publishes no rate for
	// the pair (on or before the date).
	ReasonNotPublished Reason = "not_published"
	// ReasonNoProvider: no rate provider is configured and nothing usable is
	// cached.
	ReasonNoProvider Reason = "no_provider"
)

// Conversion is a converted amount with its provenance.
type Conversion struct {
	Status Status
	Reason Reason
	From   money.Money
	To     money.Currency
	// Value is From converted, rounded half-even to To's minor units (or
	// left exact for a currency without them). Set only for StatusOK.
	Value money.Money
	// Exact is the unrounded product From × Rate.Value. Set only for
	// StatusOK. Its scale is the amount's plus the rate's (up to 48), beyond
	// the 24 digits of scale the SQL amount encoding accepts: store the rate
	// and Value, not Exact.
	Exact money.Money
	// Identity is true when From is already in To: no rate was used.
	Identity bool
	// Rate is the rate used (StatusOK) or found too old (StatusStale); nil
	// otherwise.
	Rate *Rate
	// Age is how long ago Ledger last retrieved Rate.
	Age time.Duration
	// FromCache is true when Rate came from the cache without a successful
	// provider call during this conversion.
	FromCache bool
	// RefreshError is the provider failure when a call was due but failed
	// or was skipped during a rate-limit pause; nil otherwise. The
	// conversion may still be StatusOK from the cache.
	RefreshError error
}

// Acceptable reports whether the conversion may be counted in a converted
// total (decision D5).
func (c Conversion) Acceptable() bool { return c.Status == StatusOK }

// ErrFutureDate is returned for a dated conversion after today (UTC): no
// rate can exist for it yet.
var ErrFutureDate = errors.New("fx: no rate can exist for a future date")

// Options configures a [Service].
type Options struct {
	// Freshness defaults to [DefaultFreshness].
	Freshness Freshness
	// Now defaults to time.Now.
	Now func() time.Time
	// ProviderName and Source name the provider and source whose cached
	// rates are read when Provider is nil (none configured, or disabled):
	// the service then serves the cache only. Ignored when Provider is set.
	ProviderName string
	Source       string
	// FailurePause is the pause after an unavailable or rate_limited
	// failure without a RetryAfter: provider calls stop for this long, so an
	// outage costs one timeout per pause, not one per conversion. Defaults
	// to [DefaultFailurePause]; never longer than Freshness.RefreshAfter.
	FailurePause time.Duration
	// Log receives provider revisions of cached rates; defaults to discard.
	Log *slog.Logger
}

// Service converts money using cached rates, refreshing them from the
// provider when the freshness policy says so. It is safe for concurrent use.
type Service struct {
	cache    Cache
	provider Provider
	name     string
	source   string
	fresh    Freshness
	now      func() time.Time

	failPause time.Duration
	log       *slog.Logger

	mu         sync.Mutex
	pauseUntil time.Time
	pauseErr   error
	flights    map[flightKey]*flight
}

// flightKey identifies one provider question: single-flight shares the
// answer among concurrent callers asking the same pair (and date).
type flightKey struct {
	pair Pair
	date calendar.Date
}

type flight struct {
	done chan struct{}
	rate *Rate
	perr error
	err  error
}

// NewService returns a Service. provider may be nil (cache only).
func NewService(cache Cache, provider Provider, opts Options) (*Service, error) {
	if cache == nil {
		return nil, errors.New("fx: service needs a cache")
	}
	s := &Service{cache: cache, provider: provider, fresh: opts.Freshness, now: opts.Now, name: opts.ProviderName, source: opts.Source, failPause: opts.FailurePause, log: opts.Log, flights: map[flightKey]*flight{}}
	if provider != nil {
		s.name, s.source = provider.Name(), provider.Source()
	}
	if !validProviderName(s.name) {
		return nil, errors.New("fx: provider name must be lower-case letters, digits and underscores")
	}
	if s.fresh == (Freshness{}) {
		s.fresh = DefaultFreshness
	}
	if err := s.fresh.Validate(); err != nil {
		return nil, err
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.failPause <= 0 {
		s.failPause = DefaultFailurePause
	}
	s.failPause = min(s.failPause, s.fresh.RefreshAfter)
	return s, nil
}

// HasProvider reports whether a rate provider is configured; without one the
// service only serves the cache.
func (s *Service) HasProvider() bool { return s.provider != nil }

// ProviderName is the provider whose rates this service uses.
func (s *Service) ProviderName() string { return s.name }

// Source is the source key whose rates this service uses.
func (s *Service) Source() string { return s.source }

func today(now time.Time) calendar.Date {
	y, m, d := now.UTC().Date()
	return calendar.MustDate(y, m, d)
}

// publicationGrace is how long after a UTC day ends its publication is still
// assumed possible: a provider whose day runs behind UTC (or that publishes
// late) may not have published that day's rate yet.
const publicationGrace = 6 * time.Hour

// lastOver is the last UTC day whose publication is certainly over at t: the
// day before the one t - publicationGrace falls in. A retrieval confirms
// nothing later, because the provider may publish that day's rate after the
// retrieval.
func lastOver(t time.Time) calendar.Date {
	d, err := today(t.Add(-publicationGrace)).AddDays(-1)
	if err != nil {
		return today(t)
	}
	return d
}

func (s *Service) start(from money.Money, to money.Currency) (Conversion, Pair, error) {
	if !from.IsValid() {
		return Conversion{}, Pair{}, money.ErrNoCurrency
	}
	if to.IsZero() {
		return Conversion{}, Pair{}, fmt.Errorf("%w: no target currency", ErrInvalidRate)
	}
	return Conversion{From: from, To: to}, Pair{Base: from.Currency(), Quote: to}, nil
}

// Convert converts from into to at the latest acceptable rate. The error is
// for invalid input, cache failures and cancellation only: provider
// failures are reported in the Conversion.
func (s *Service) Convert(ctx context.Context, from money.Money, to money.Currency) (Conversion, error) {
	c, pair, err := s.start(from, to)
	if err != nil {
		return Conversion{}, err
	}
	if pair.Base == pair.Quote {
		return identity(c), nil
	}
	now := s.now()
	cached, ok, err := s.cache.Find(ctx, s.name, s.source, pair, calendar.Date{})
	if err != nil {
		return Conversion{}, err
	}
	// Fresh: retrieved recently, and by a retrieval that confirmed the rate
	// through the last day whose publication was over then (a latest one), not
	// a dated one for an older day.
	if ok && now.Sub(cached.RetrievedAt) < s.fresh.RefreshAfter && !cached.Confirmed().Before(lastOver(cached.RetrievedAt)) {
		return s.result(c, cached, today(now), now, true, nil), nil
	}
	fetched, perr, err := s.fetch(ctx, pair, calendar.Date{})
	if err != nil {
		return Conversion{}, err
	}
	if perr == nil {
		if fetched != nil {
			return s.result(c, *fetched, today(now), now, false, nil), nil
		}
		if !ok {
			return missing(c, ReasonNotPublished, nil), nil
		}
		// The provider no longer publishes the pair: the cached rate ages.
		return s.result(c, cached, today(now), now, true, nil), nil
	}
	if !ok {
		return missing(c, s.notCachedReason(), perr), nil
	}
	return s.result(c, cached, today(now), now, true, perr), nil
}

// ConvertCached is [Service.Convert] without the provider: it only reads the
// cache, so it never blocks on the network and never starts a provider call.
// A cached rate converts when the freshness policy accepts it (a rate retrieved
// more than RefreshAfter ago is still used, with its age, until it is stale); a
// pair with nothing cached is missing. Callers use it once their time for a
// provider answer has run out.
func (s *Service) ConvertCached(ctx context.Context, from money.Money, to money.Currency) (Conversion, error) {
	c, pair, err := s.start(from, to)
	if err != nil {
		return Conversion{}, err
	}
	if pair.Base == pair.Quote {
		return identity(c), nil
	}
	now := s.now()
	cached, ok, err := s.cache.Find(ctx, s.name, s.source, pair, calendar.Date{})
	if err != nil {
		return Conversion{}, err
	}
	if !ok {
		return missing(c, s.notCachedReason(), nil), nil
	}
	return s.result(c, cached, today(now), now, true, nil), nil
}

// ConvertOn converts from into to at the rate that applied on date: the
// provider's rate for that date or, when it published none that day, its
// latest earlier one. A date after today (UTC) is [ErrFutureDate]; today
// uses [Service.Convert].
func (s *Service) ConvertOn(ctx context.Context, from money.Money, to money.Currency, date calendar.Date) (Conversion, error) {
	c, pair, err := s.start(from, to)
	if err != nil {
		return Conversion{}, err
	}
	if date.IsZero() {
		return Conversion{}, errors.New("fx: dated conversion without a date")
	}
	now := s.now()
	switch t := today(now); {
	case date.After(t):
		return Conversion{}, fmt.Errorf("%w: %s", ErrFutureDate, date)
	case date == t:
		return s.Convert(ctx, from, to)
	}
	if pair.Base == pair.Quote {
		return identity(c), nil
	}
	cached, ok, err := s.cache.Find(ctx, s.name, s.source, pair, date)
	if err != nil {
		return Conversion{}, err
	}
	// A cached rate is final for date when it is dated that day, or when a
	// retrieval confirmed it through that day (nothing newer was published
	// up to date). When it was retrieved says nothing about which dates it
	// covered.
	if ok && cached.FinalFor(date) {
		return s.result(c, cached, date, now, true, nil), nil
	}
	fetched, perr, err := s.fetch(ctx, pair, date)
	if err != nil {
		return Conversion{}, err
	}
	if perr == nil {
		if fetched != nil {
			return s.result(c, *fetched, date, now, false, nil), nil
		}
		return missing(c, ReasonNotPublished, nil), nil
	}
	if !ok {
		return missing(c, s.notCachedReason(), perr), nil
	}
	// Only an earlier, unconfirmed rate is known.
	out := s.result(c, cached, date, now, true, perr)
	if out.Status == StatusOK {
		out = staleOf(out, ReasonUnconfirmed)
	}
	return out, nil
}

// Refresh asks the provider for the latest rates from base to each quote and
// caches them, for the daily refresh job. It respects a rate-limit pause.
func (s *Service) Refresh(ctx context.Context, base money.Currency, quotes []money.Currency) ([]Rate, error) {
	if s.provider == nil {
		return nil, errors.New("fx: no rate provider configured")
	}
	if err := s.paused(); err != nil {
		return nil, err
	}
	q, err := s.provider.Latest(ctx, base, quotes)
	if err != nil {
		return nil, s.failed(ctx, "latest", err)
	}
	rates, err := s.store(ctx, q, base, calendar.Date{})
	if err != nil {
		return nil, err
	}
	if err := s.mismatch(rates, base, quotes); err != nil {
		return nil, err
	}
	return rates, nil
}

func (s *Service) notCachedReason() Reason {
	if s.provider == nil {
		return ReasonNoProvider
	}
	return ReasonNotCached
}

// fetch asks the provider for one pair (latest when date is zero) and caches
// what it returns. perr is a provider failure (the caller serves the cache);
// err is a cache failure or cancellation. A nil rate with nil errors means
// the provider publishes nothing for the pair. Concurrent calls for the same
// pair and date share one provider call.
func (s *Service) fetch(ctx context.Context, pair Pair, date calendar.Date) (rate *Rate, perr, err error) {
	if s.provider == nil {
		return nil, errors.New("fx: no rate provider configured"), nil
	}
	key := flightKey{pair, date}
	for {
		s.mu.Lock()
		if f, ok := s.flights[key]; ok {
			s.mu.Unlock()
			select {
			case <-f.done:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
			// The leader's own context ended: ask again for this caller.
			if f.err != nil && ctx.Err() == nil && (errors.Is(f.err, context.Canceled) || errors.Is(f.err, context.DeadlineExceeded)) {
				continue
			}
			return f.rate, f.perr, f.err
		}
		f := &flight{done: make(chan struct{})}
		s.flights[key] = f
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			delete(s.flights, key)
			s.mu.Unlock()
			close(f.done)
		}()
		f.rate, f.perr, f.err = s.fetchOnce(ctx, pair, date)
		return f.rate, f.perr, f.err
	}
}

func (s *Service) fetchOnce(ctx context.Context, pair Pair, date calendar.Date) (rate *Rate, perr, err error) {
	if perr := s.paused(); perr != nil {
		return nil, perr, nil
	}
	op := "latest"
	var quotes []Quote
	if date.IsZero() {
		quotes, err = s.provider.Latest(ctx, pair.Base, []money.Currency{pair.Quote})
	} else {
		op = "on_date"
		quotes, err = s.provider.OnDate(ctx, pair.Base, []money.Currency{pair.Quote}, date)
	}
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, cerr
		}
		return nil, s.failed(ctx, op, err), nil
	}
	rates, err := s.store(ctx, quotes, pair.Base, date)
	if err == nil {
		err = s.mismatch(rates, pair.Base, []money.Currency{pair.Quote})
	}
	if err != nil {
		var pe *ProviderError
		if errors.As(err, &pe) {
			return nil, pe, nil
		}
		return nil, nil, err
	}
	for i := range rates {
		if rates[i].Pair == pair && rates[i].Source == s.source {
			return &rates[i], nil, nil
		}
	}
	return nil, nil, nil
}

// mismatch reports an answer that holds a rate for an asked pair but none
// under the configured source: an adapter misconfiguration, which must not
// look like "the provider publishes no such pair". Each asked pair is checked
// on its own, so one pair answered correctly cannot hide another that was not.
// The rates stay cached under their own source.
func (s *Service) mismatch(rates []Rate, base money.Currency, asked []money.Currency) error {
	for _, q := range asked {
		pair := Pair{Base: base, Quote: q}
		other := ""
		found := false
		for _, r := range rates {
			if r.Pair != pair {
				continue
			}
			if r.Source == s.source {
				found = false
				break
			}
			if !found {
				other, found = r.Source, true
			}
		}
		if found {
			return &ProviderError{Kind: KindPermanent, Provider: s.name, Op: "validate", Code: "source_mismatch",
				Err: fmt.Errorf("%w: configured source %q, answer for %s holds %q", ErrInvalidRate, s.source, pair, other)}
		}
	}
	return nil
}

// store validates and caches what the provider returned and returns the
// rates as stored. A malformed answer (wrong base, non-positive rate, a date
// after the one asked for, two different values for one rate) is a permanent
// provider error and nothing from it is cached. Quotes for currencies that
// were not asked for are cached too: they are real published rates.
func (s *Service) store(ctx context.Context, quotes []Quote, base money.Currency, date calendar.Date) ([]Rate, error) {
	now := s.now().UTC()
	limit := date
	// What a retrieval confirms: the date asked (today, for a latest one) but
	// never past the last day whose publication is certainly over: the
	// provider may publish today's rate after this retrieval. A rate's own
	// date is always confirmed (see below).
	asked := date
	if limit.IsZero() {
		asked = today(now)
		// The provider's calendar may run ahead of UTC (a rate dated in its
		// own zone): allow one day, never more.
		var err error
		if limit, err = today(now).AddDays(1); err != nil {
			return nil, err
		}
	}
	confirmed := asked
	if over := lastOver(now); confirmed.After(over) {
		confirmed = over
	}
	type key struct {
		source string
		pair   Pair
		date   calendar.Date
	}
	seen := make(map[key]money.Decimal, len(quotes))
	rates := make([]Rate, 0, len(quotes))
	for _, q := range quotes {
		r := Rate{Provider: s.name, Source: q.Source, Pair: q.Pair, Value: q.Value, Date: q.Date, RetrievedAt: now, FirstRetrievedAt: now, ConfirmedThrough: confirmed}
		if r.ConfirmedThrough.Before(r.Date) {
			r.ConfirmedThrough = r.Date
		}
		err := r.Validate()
		k := key{q.Source, q.Pair, q.Date}
		switch {
		case err != nil:
		case q.Pair.Base != base:
			err = fmt.Errorf("%w: asked for base %s, got %s", ErrInvalidRate, base, q.Pair)
		case q.Date.After(limit):
			err = fmt.Errorf("%w: %s rate dated %s, after %s", ErrInvalidRate, q.Pair, q.Date, limit)
		default:
			if prev, dup := seen[k]; dup {
				if prev.Cmp(q.Value) != 0 {
					err = fmt.Errorf("%w: two values for the %s rate of %s", ErrInvalidRate, q.Pair, q.Date)
				}
				// An exact repeat adds nothing.
				if err == nil {
					continue
				}
			}
			seen[k] = q.Value
		}
		if err != nil {
			return nil, &ProviderError{Kind: KindPermanent, Provider: s.name, Op: "validate", Code: "invalid_rate", Err: err}
		}
		rates = append(rates, r)
	}
	if len(rates) == 0 {
		return nil, nil
	}
	saved, err := s.cache.Save(ctx, rates)
	if err != nil {
		return nil, err
	}
	out := make([]Rate, len(saved))
	for i, sv := range saved {
		out[i] = sv.Rate
		if rev := sv.Revised; rev != nil {
			s.log.Warn("fx: provider revised a published rate",
				"provider", rev.Provider, "source", rev.Source, "pair", rev.Pair.String(), "rate_date", rev.Date.String(),
				"old", rev.Old.String(), "new", rev.New.String())
		}
	}
	return out, nil
}

func (s *Service) paused() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.now().Before(s.pauseUntil) {
		return s.pauseErr
	}
	return nil
}

// failed classifies a provider failure and, for an unavailable or
// rate-limited one, pauses provider calls: until the provider's RetryAfter
// (capped at StaleAfter, so a hostile or mistaken value cannot silence
// refreshes until restart), else for the default failure pause. A permanent
// failure is specific to the request and pauses nothing.
func (s *Service) failed(ctx context.Context, op string, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	pe := classify(s.name, op, err)
	var pause time.Duration
	switch pe.Kind {
	case KindRateLimited, KindUnavailable:
		pause = s.failPause
		if pe.Kind == KindRateLimited && pe.RetryAfter > 0 {
			pause = min(pe.RetryAfter, s.fresh.StaleAfter)
		}
	}
	if pause > 0 {
		s.mu.Lock()
		// Keep the later deadline: a short pause must not cut a longer one
		// short when failures of different pairs overlap.
		if until := s.now().Add(pause); until.After(s.pauseUntil) {
			s.pauseUntil = until
			s.pauseErr = pe
		}
		s.mu.Unlock()
	}
	return pe
}

// result builds a conversion from rate, judged for use on date.
func (s *Service) result(c Conversion, rate Rate, date calendar.Date, now time.Time, fromCache bool, perr error) Conversion {
	c.Rate = &rate
	c.Age = now.Sub(rate.RetrievedAt)
	c.FromCache = fromCache
	c.RefreshError = perr
	if reason := s.fresh.staleReason(rate, date, now); reason != ReasonNone {
		return staleOf(c, reason)
	}
	exact := c.From.Amount().Mul(rate.Value)
	value := exact
	if digits, ok := c.To.MinorUnits(); ok {
		value = exact.RoundHalfEven(digits)
	}
	var err error
	if c.Exact, err = money.New(exact, c.To); err == nil {
		c.Value, err = money.New(value, c.To)
	}
	if err != nil {
		// Unreachable: To was checked to be set.
		return missing(c, ReasonNotCached, err)
	}
	c.Status = StatusOK
	return c
}

func staleOf(c Conversion, reason Reason) Conversion {
	c.Status, c.Reason = StatusStale, reason
	c.Value, c.Exact = money.Money{}, money.Money{}
	return c
}

func missing(c Conversion, reason Reason, perr error) Conversion {
	c.Status, c.Reason, c.RefreshError = StatusMissing, reason, perr
	c.Rate, c.Value, c.Exact = nil, money.Money{}, money.Money{}
	return c
}

func identity(c Conversion) Conversion {
	c.Status, c.Identity = StatusOK, true
	c.Value, c.Exact = c.From, c.From
	return c
}
