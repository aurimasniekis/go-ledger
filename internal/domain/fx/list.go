package fx

import (
	"context"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// RateKey identifies one cached rate. It is the position of a list cursor.
type RateKey struct {
	Provider string
	Source   string
	Pair     Pair
	Date     calendar.Date
}

// ListFilter selects cached rates for [Lister.List]. Zero fields match
// everything.
type ListFilter struct {
	Provider string
	Source   string
	Base     money.Currency
	Quote    money.Currency
	// From and To bound the rate date, both inclusive.
	From, To calendar.Date
	// After resumes the list after this rate (see [Lister]).
	After *RateKey
	// Limit is the most rates returned; zero means no limit.
	Limit int
}

// Lister lists cached rates for display. The order is fixed: provider,
// source, base currency and quote currency ascending, then rate date newest
// first. It is not part of [Cache]: conversion never lists.
type Lister interface {
	List(ctx context.Context, f ListFilter) ([]Rate, error)
}

// StaleReason says why a cached rate counts as stale today (decision D11), or
// is empty when it does not: Ledger last retrieved it [Freshness.StaleAfter]
// ago or longer ([ReasonNotRetrievedRecently]), or its date is more than
// StaleAfter (in whole days) before today ([ReasonRateDateTooOld]). The same
// test decides whether a conversion may use the rate.
func (s *Service) StaleReason(r Rate) Reason {
	now := s.now()
	return s.fresh.staleReason(r, today(now), now)
}

// Freshness returns the policy the service applies.
func (s *Service) Freshness() Freshness { return s.fresh }

func (f Freshness) staleReason(r Rate, date calendar.Date, now time.Time) Reason {
	if date == today(now) && now.Sub(r.RetrievedAt) >= f.StaleAfter {
		return ReasonNotRetrievedRecently
	}
	if r.Date.DaysUntil(date) > f.staleDays() {
		return ReasonRateDateTooOld
	}
	return ReasonNone
}
