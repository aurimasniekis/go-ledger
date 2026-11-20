package fx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// Pair is a currency pair: one unit of Base is worth Rate.Value units of
// Quote.
type Pair struct {
	Base  money.Currency
	Quote money.Currency
}

// String returns "BASE/QUOTE".
func (p Pair) String() string { return p.Base.Code() + "/" + p.Quote.Code() }

// Validate checks that both currencies are set and differ.
func (p Pair) Validate() error {
	switch {
	case p.Base.IsZero() || p.Quote.IsZero():
		return fmt.Errorf("%w: pair %q needs two currencies", ErrInvalidRate, p.String())
	case p.Base == p.Quote:
		return fmt.Errorf("%w: pair %q has one currency on both sides", ErrInvalidRate, p.String())
	}
	return nil
}

// ErrInvalidRate marks a rate or pair that is malformed: a missing currency,
// a rate that is not positive, a missing date. Such a rate is never cached.
var ErrInvalidRate = errors.New("fx: invalid rate")

// Quote is one rate as a provider reports it, before Ledger stamps it.
type Quote struct {
	// Source is the provider's own key for where the rate comes from (for
	// Frankfurter, a provider key such as "ECB", or the adapter's name for
	// blended rates); empty when the provider has only one.
	Source string
	Pair   Pair
	// Value is the exact rate as published, never rounded or recomputed.
	Value money.Decimal
	// Date is the date the provider says the rate applies to. It may be
	// earlier than the date asked for (weekends, holidays).
	Date calendar.Date
}

// Rate is a cached rate with its provenance.
type Rate struct {
	// Provider is the adapter name, such as "frankfurter".
	Provider string
	Source   string
	Pair     Pair
	Value    money.Decimal
	Date     calendar.Date
	// RetrievedAt is the last time (UTC) the provider returned this rate for
	// this date. Asking again and getting the same date back moves it
	// forward: it says how recently Ledger confirmed the rate is current.
	RetrievedAt time.Time
	// FirstRetrievedAt is when Ledger first received this value. When the
	// provider revises the value it restarts at the revision's retrieval, so
	// it never claims Ledger held a value earlier than it did.
	FirstRetrievedAt time.Time
	// ConfirmedThrough is the latest date up to which a retrieval confirmed
	// this rate is the one that applies: the date asked for by a dated
	// retrieval, the retrieval's UTC date for a latest one. Between Date and
	// ConfirmedThrough the provider published nothing newer. Zero on input
	// means Date; caches always return a value of at least Date.
	ConfirmedThrough calendar.Date
	// Previous is the value this one replaced, when the provider revised
	// the rate; nil when it never did.
	Previous *Superseded
}

// Superseded is a value a provider revised away.
type Superseded struct {
	Value money.Decimal
	// RevisedAt is when Ledger received the replacing value.
	RevisedAt time.Time
}

// Revision records that a provider changed the value of a rate it had
// already published for a date.
type Revision struct {
	Provider string
	Source   string
	Pair     Pair
	Date     calendar.Date
	Old, New money.Decimal
	At       time.Time
}

// Confirmed is ConfirmedThrough, or Date when none was recorded.
func (r Rate) Confirmed() calendar.Date {
	if r.ConfirmedThrough.IsZero() || r.ConfirmedThrough.Before(r.Date) {
		return r.Date
	}
	return r.ConfirmedThrough
}

// FinalFor reports whether this rate is known to be the one that applies on
// date: it is dated that day, or a retrieval confirmed it through that day.
// A retrieval time alone proves nothing about which dates it covered.
func (r Rate) FinalFor(date calendar.Date) bool {
	return r.Date == date || !r.Confirmed().Before(date)
}

// Saved is the outcome of caching one rate: the row as stored after the
// merge, and the revision when the value changed.
type Saved struct {
	Rate    Rate
	Revised *Revision
}

// MergeRate combines a stored rate with a retrieval of the same key made at
// the same time or later; both caches use it so they agree. The value is
// compared numerically: a different representation of the same number is no
// revision. A changed value replaces the old one, restarts FirstRetrievedAt,
// records the old value and returns the revision. An unchanged value keeps
// its first retrieval and history and moves RetrievedAt and ConfirmedThrough
// forward (ConfirmedThrough never moves back).
func MergeRate(old, in Rate) Saved {
	if in.Value.Cmp(old.Value) != 0 {
		in.FirstRetrievedAt = in.RetrievedAt
		in.ConfirmedThrough = in.Confirmed()
		in.Previous = &Superseded{Value: old.Value, RevisedAt: in.RetrievedAt}
		return Saved{Rate: in, Revised: &Revision{Provider: in.Provider, Source: in.Source, Pair: in.Pair, Date: in.Date, Old: old.Value, New: in.Value, At: in.RetrievedAt}}
	}
	out := old
	out.RetrievedAt = in.RetrievedAt
	if c := in.Confirmed(); c.After(old.Confirmed()) {
		out.ConfirmedThrough = c
	} else {
		out.ConfirmedThrough = old.Confirmed()
	}
	return Saved{Rate: out}
}

// NewRate normalizes a rate that has no stored predecessor: FirstRetrievedAt
// defaults to RetrievedAt, ConfirmedThrough to at least Date, and Previous
// is dropped.
func NewRate(in Rate) Rate {
	if in.FirstRetrievedAt.IsZero() || in.FirstRetrievedAt.After(in.RetrievedAt) {
		in.FirstRetrievedAt = in.RetrievedAt
	}
	in.ConfirmedThrough = in.Confirmed()
	in.Previous = nil
	return in
}

// Validate checks a rate before it is cached.
func (r Rate) Validate() error {
	if !validProviderName(r.Provider) {
		return fmt.Errorf("%w: provider name must be lower-case letters, digits and underscores", ErrInvalidRate)
	}
	if err := r.Pair.Validate(); err != nil {
		return err
	}
	if r.Value.Sign() <= 0 {
		return fmt.Errorf("%w: %s rate %s is not positive", ErrInvalidRate, r.Pair, r.Value)
	}
	if r.Date.IsZero() {
		return fmt.Errorf("%w: %s rate without a date", ErrInvalidRate, r.Pair)
	}
	if r.RetrievedAt.IsZero() {
		return fmt.Errorf("%w: %s rate without a retrieval time", ErrInvalidRate, r.Pair)
	}
	if !r.ConfirmedThrough.IsZero() && r.ConfirmedThrough.Before(r.Date) {
		return fmt.Errorf("%w: %s rate confirmed through %s, before its date %s", ErrInvalidRate, r.Pair, r.ConfirmedThrough, r.Date)
	}
	return nil
}

func validProviderName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// Provider is the port a rate-source adapter implements. Adapters keep
// provider specifics (URLs, response shapes, status codes, quota headers) to
// themselves and return errors classified as [*ProviderError]. They must
// parse rates from the response text exactly, never through float64.
// Implementations must be safe for concurrent use.
type Provider interface {
	// Name is the adapter name stored with every rate: lower-case letters,
	// digits and underscores.
	Name() string
	// Source is the source key of the rates this adapter, as configured,
	// returns for conversion (for Frankfurter, a provider key such as "ECB"
	// or a name for its blended rates). Rates from different sources differ
	// for the same pair and date, so the cache keeps them apart and the
	// service uses only this source.
	Source() string
	// Latest returns the most recent rates from base to each quote. A quote
	// the provider does not publish is absent from the result, not an error.
	Latest(ctx context.Context, base money.Currency, quotes []money.Currency) ([]Quote, error)
	// OnDate returns the rates that applied on date. The provider may answer
	// with an earlier date (the last day it published); [Quote.Date] says
	// which. It never answers with a later date.
	OnDate(ctx context.Context, base money.Currency, quotes []money.Currency, date calendar.Date) ([]Quote, error)
}

// Cache is the port the rate store implements.
type Cache interface {
	// Save stores rates keyed by (provider, source, pair, date) and returns
	// each as stored, in order. A rate already stored for the key is merged
	// with one retrieved at the same time or later (see [MergeRate]); an
	// older retrieval changes nothing and the stored row is returned. All
	// rates are validated before any is written.
	Save(ctx context.Context, rates []Rate) ([]Saved, error)
	// Find returns the cached rate of provider and source for pair with the
	// latest date on or before onOrBefore (no bound when onOrBefore is zero).
	// ok is false when there is none.
	Find(ctx context.Context, provider, source string, pair Pair, onOrBefore calendar.Date) (rate Rate, ok bool, err error)
}

// ErrorKind classifies provider failures by what Ledger does next.
type ErrorKind string

const (
	// KindRateLimited: the quota is exhausted or the provider refused for
	// now. Ledger serves the cache and does not call again before RetryAfter.
	KindRateLimited ErrorKind = "rate_limited"
	// KindUnavailable: a transient failure (timeout, network error, provider
	// down). Ledger serves the cache.
	KindUnavailable ErrorKind = "unavailable"
	// KindPermanent: the request cannot succeed as made (unsupported
	// currency, malformed response). Ledger serves the cache and surfaces it.
	KindPermanent ErrorKind = "permanent"
)

// Sentinels for errors.Is; each matches every [*ProviderError] of its kind.
var (
	ErrRateLimited = errors.New("fx: rate provider quota exhausted or rate limited")
	ErrUnavailable = errors.New("fx: rate provider unavailable")
	ErrPermanent   = errors.New("fx: rate provider request failed")
)

// ProviderError is a classified provider failure. Its message holds only
// the provider name, the operation, the kind and the provider's error code:
// never credentials, URLs with keys or response bodies. Err is kept for
// errors.Is/As and is not printed.
type ProviderError struct {
	Kind     ErrorKind
	Provider string
	// Op is the operation: "latest" or "on_date".
	Op   string
	Code string
	// RetryAfter is the provider's requested delay, zero if none.
	RetryAfter time.Duration
	Err        error
}

func (e *ProviderError) Error() string {
	msg := "fx: " + e.Provider + " " + e.Op + ": " + string(e.Kind)
	if e.Code != "" {
		msg += ": " + e.Code
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *ProviderError) Unwrap() error { return e.Err }

// Is matches the sentinel of the error's kind.
func (e *ProviderError) Is(target error) bool {
	switch e.Kind {
	case KindRateLimited:
		return target == ErrRateLimited
	case KindUnavailable:
		return target == ErrUnavailable
	case KindPermanent:
		return target == ErrPermanent
	}
	return false
}

// classify turns any provider failure into a [*ProviderError]. An
// unclassified error counts as unavailable: the cache is served either way.
func classify(provider, op string, err error) *ProviderError {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe
	}
	return &ProviderError{Kind: KindUnavailable, Provider: provider, Op: op, Err: err}
}
