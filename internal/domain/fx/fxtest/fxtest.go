// Package fxtest provides test doubles for the fx ports: a scripted rate
// provider and an in-memory cache, plus a contract test every [fx.Cache]
// implementation runs. Nothing here touches the network.
package fxtest

import (
	"context"
	"sort"
	"sync"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// Call records one provider call.
type Call struct {
	Op     string // "latest" or "on_date"
	Base   money.Currency
	Quotes []money.Currency
	Date   calendar.Date
}

// Provider is a scripted [fx.Provider]. It publishes the rates added with
// [Provider.Publish]; Latest answers with each pair's newest published rate
// and OnDate with the newest on or before the date. Queued failures are
// returned first, one per call.
type Provider struct {
	name   string
	source string

	mu     sync.Mutex
	quotes map[fx.Pair][]fx.Quote
	fail   []error
	down   error
	calls  []Call
	gate   chan struct{}
	each   bool
	gates  []chan struct{}
	raw    []fx.Quote
}

var _ fx.Provider = (*Provider)(nil)

// NewProvider returns an empty provider called name whose conversion
// source is source.
func NewProvider(name, source string) *Provider {
	return &Provider{name: name, source: source, quotes: map[fx.Pair][]fx.Quote{}}
}

// Name implements [fx.Provider].
func (p *Provider) Name() string { return p.name }

// Source implements [fx.Provider].
func (p *Provider) Source() string { return p.source }

// Publish makes a rate available: base/quote = value on date.
func (p *Provider) Publish(base, quote, value, date, source string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	q := fx.Quote{
		Source: source,
		Pair:   fx.Pair{Base: money.MustParseCurrency(base), Quote: money.MustParseCurrency(quote)},
		Value:  money.MustParseDecimal(value),
		Date:   mustDate(date),
	}
	list := append(p.quotes[q.Pair], q)
	sort.SliceStable(list, func(i, j int) bool { return list[i].Date.Before(list[j].Date) })
	p.quotes[q.Pair] = list
}

// FailNext queues err for the next call.
func (p *Provider) FailNext(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fail = append(p.fail, err)
}

// SetDown makes every call fail with err until SetDown(nil).
func (p *Provider) SetDown(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = err
}

// Calls returns the calls made so far.
func (p *Provider) Calls() []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Call(nil), p.calls...)
}

// SetRaw makes every call answer with quotes verbatim (until SetRaw(nil)),
// for malformed answers the normal publishing rules cannot produce, such as
// two values for one rate.
func (p *Provider) SetRaw(quotes []fx.Quote) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.raw = quotes
}

// Hold makes every call block after it is recorded until release is called,
// so a test can hold concurrent conversions inside one provider call.
func (p *Provider) Hold() (release func()) {
	gate := make(chan struct{})
	p.mu.Lock()
	p.gate = gate
	p.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

// HoldEach makes every call block after it is recorded until release(i)
// is called for its position i in [Provider.Calls], so a test can finish
// concurrent calls one at a time, in an order it chooses. release(i) before
// call i is recorded is the test's mistake and panics.
func (p *Provider) HoldEach() (release func(i int)) {
	p.mu.Lock()
	p.each = true
	p.mu.Unlock()
	return func(i int) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if i >= len(p.gates) {
			panic("fxtest: release of a call that was not made")
		}
		select {
		case <-p.gates[i]:
		default:
			close(p.gates[i])
		}
	}
}

// ReleaseAll stops [Provider.HoldEach] from holding calls made from now on.
func (p *Provider) ReleaseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.each = false
}

func (p *Provider) answer(ctx context.Context, c Call) ([]fx.Quote, error) {
	p.mu.Lock()
	p.calls = append(p.calls, c)
	gate := p.gate
	if p.each {
		gate = make(chan struct{})
		p.gates = append(p.gates, gate)
	}
	p.mu.Unlock()
	if gate != nil {
		// A held call ends with its context, like a real provider's request.
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.fail) > 0 {
		err := p.fail[0]
		p.fail = p.fail[1:]
		return nil, err
	}
	if p.down != nil {
		return nil, p.down
	}
	if p.raw != nil {
		return append([]fx.Quote(nil), p.raw...), nil
	}
	// Per pair and source, the newest rate on or before the date.
	var out []fx.Quote
	for _, q := range c.Quotes {
		list := p.quotes[fx.Pair{Base: c.Base, Quote: q}]
		seen := map[string]bool{}
		for i := len(list) - 1; i >= 0; i-- {
			if !seen[list[i].Source] && (c.Date.IsZero() || !list[i].Date.After(c.Date)) {
				seen[list[i].Source] = true
				out = append(out, list[i])
			}
		}
	}
	return out, nil
}

// Latest implements [fx.Provider].
func (p *Provider) Latest(ctx context.Context, base money.Currency, quotes []money.Currency) ([]fx.Quote, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.answer(ctx, Call{Op: "latest", Base: base, Quotes: append([]money.Currency(nil), quotes...)})
}

// OnDate implements [fx.Provider].
func (p *Provider) OnDate(ctx context.Context, base money.Currency, quotes []money.Currency, date calendar.Date) ([]fx.Quote, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.answer(ctx, Call{Op: "on_date", Base: base, Quotes: append([]money.Currency(nil), quotes...), Date: date})
}

func mustDate(s string) calendar.Date {
	if s == "" {
		return calendar.Date{}
	}
	d, err := calendar.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

type cacheKey struct {
	provider string
	source   string
	pair     fx.Pair
	date     calendar.Date
}

// Cache is an in-memory [fx.Cache] with the same semantics as the SQLite
// store.
type Cache struct {
	mu    sync.Mutex
	rates map[cacheKey]fx.Rate
}

var _ fx.Cache = (*Cache)(nil)

// NewCache returns an empty cache.
func NewCache() *Cache { return &Cache{rates: map[cacheKey]fx.Rate{}} }

// Save implements [fx.Cache].
func (c *Cache) Save(_ context.Context, rates []fx.Rate) ([]fx.Saved, error) {
	for _, r := range rates {
		if err := r.Validate(); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]fx.Saved, 0, len(rates))
	for _, r := range rates {
		r.RetrievedAt, r.FirstRetrievedAt = r.RetrievedAt.UTC(), r.FirstRetrievedAt.UTC()
		k := cacheKey{r.Provider, r.Source, r.Pair, r.Date}
		old, ok := c.rates[k]
		var s fx.Saved
		switch {
		case ok && r.RetrievedAt.Before(old.RetrievedAt):
			out = append(out, fx.Saved{Rate: old})
			continue
		case ok:
			s = fx.MergeRate(old, r)
		default:
			s = fx.Saved{Rate: fx.NewRate(r)}
		}
		c.rates[k] = s.Rate
		out = append(out, s)
	}
	return out, nil
}

// Find implements [fx.Cache].
func (c *Cache) Find(_ context.Context, provider, source string, pair fx.Pair, onOrBefore calendar.Date) (fx.Rate, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var best fx.Rate
	found := false
	for k, r := range c.rates {
		if k.provider != provider || k.source != source || k.pair != pair || (!onOrBefore.IsZero() && k.date.After(onOrBefore)) {
			continue
		}
		if !found || k.date.After(best.Date) {
			best, found = r, true
		}
	}
	return best, found, nil
}
