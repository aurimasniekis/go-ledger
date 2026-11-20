// Package fxrates persists the currency-rate cache (FX1, ADR-0022) in the
// fx_rates table of migration 0005_fx_rates. It implements [fx.Cache].
package fxrates

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/sqlvalue"
)

// Store implements [fx.Cache] on a [store.DB].
type Store struct {
	db *store.DB
}

var _ fx.Cache = (*Store)(nil)

// New returns a Store.
func New(db *store.DB) *Store { return &Store{db: db} }

// Save implements [fx.Cache]: every rate is validated first, then all are
// written in one transaction. For a key already stored, a rate retrieved at
// the same time or later replaces the value and retrieved_at; an
// older retrieval changes nothing. first_retrieved_at is never changed.
func (s *Store) Save(ctx context.Context, rates []fx.Rate) error {
	for _, r := range rates {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	if len(rates) == 0 {
		return nil
	}
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, r := range rates {
			first := r.FirstRetrievedAt
			if first.IsZero() || first.After(r.RetrievedAt) {
				first = r.RetrievedAt
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO fx_rates
    (provider, source, base_currency, quote_currency, rate, rate_date, first_retrieved_at, retrieved_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (provider, source, base_currency, quote_currency, rate_date) DO UPDATE SET
    rate = excluded.rate, retrieved_at = excluded.retrieved_at
WHERE excluded.retrieved_at >= fx_rates.retrieved_at`,
				r.Provider, r.Source, sqlvalue.Currency{Currency: r.Pair.Base}, sqlvalue.Currency{Currency: r.Pair.Quote},
				sqlvalue.Amount{Decimal: r.Value}, sqlvalue.Date{Date: r.Date},
				store.Timestamp{Time: first}, store.Timestamp{Time: r.RetrievedAt})
			if err != nil {
				return fmt.Errorf("fxrates: save %s %s rate for %s: %w", r.Provider, r.Pair, r.Date, err)
			}
		}
		return nil
	})
}

// Find implements [fx.Cache].
func (s *Store) Find(ctx context.Context, provider, source string, pair fx.Pair, onOrBefore calendar.Date) (fx.Rate, bool, error) {
	q := `SELECT provider, source, base_currency, quote_currency, rate, rate_date, first_retrieved_at, retrieved_at
FROM fx_rates WHERE provider = ? AND source = ? AND base_currency = ? AND quote_currency = ?`
	args := []any{provider, source, sqlvalue.Currency{Currency: pair.Base}, sqlvalue.Currency{Currency: pair.Quote}}
	if !onOrBefore.IsZero() {
		q += " AND rate_date <= ?"
		args = append(args, sqlvalue.Date{Date: onOrBefore})
	}
	q += " ORDER BY rate_date DESC LIMIT 1"
	var r fx.Rate
	var base, quote sqlvalue.Currency
	var value sqlvalue.Amount
	var date sqlvalue.Date
	var first, retrieved store.Timestamp
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, q, args...).Scan(&r.Provider, &r.Source, &base, &quote, &value, &date, &first, &retrieved)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return fx.Rate{}, false, nil
	}
	if err != nil {
		return fx.Rate{}, false, fmt.Errorf("fxrates: find %s %s %s rate: %w", provider, source, pair, err)
	}
	r.Pair = fx.Pair{Base: base.Currency, Quote: quote.Currency}
	r.Value, r.Date = value.Decimal, date.Date
	r.FirstRetrievedAt, r.RetrievedAt = first.Time, retrieved.Time
	return r, true, nil
}
