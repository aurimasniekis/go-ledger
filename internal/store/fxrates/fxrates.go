// Package fxrates persists the currency-rate cache (FX1, ADR-0022) in the
// fx_rates table of migration 0007_fx_rates. It implements [fx.Cache].
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
// merged and written in one transaction ([fx.MergeRate]). For a key already
// stored, a rate retrieved at the same time or later is merged: a numerically
// different value replaces the old one and keeps it as previous_rate with
// revised_at, and first_retrieved_at restarts; the same value moves
// retrieved_at and confirmed_through forward. An older retrieval changes
// nothing.
func (s *Store) Save(ctx context.Context, rates []fx.Rate) ([]fx.Saved, error) {
	for _, r := range rates {
		if err := r.Validate(); err != nil {
			return nil, err
		}
	}
	if len(rates) == 0 {
		return nil, nil
	}
	out := make([]fx.Saved, 0, len(rates))
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		out = out[:0]
		for _, r := range rates {
			old, ok, err := find(ctx, tx, r.Provider, r.Source, r.Pair, r.Date, true)
			if err != nil {
				return err
			}
			var saved fx.Saved
			switch {
			case ok && r.RetrievedAt.Before(old.RetrievedAt):
				out = append(out, fx.Saved{Rate: old})
				continue
			case ok:
				saved = fx.MergeRate(old, r)
			default:
				saved = fx.Saved{Rate: fx.NewRate(r)}
			}
			m := saved.Rate
			var prev, revisedAt any
			if m.Previous != nil {
				prev, revisedAt = sqlvalue.Amount{Decimal: m.Previous.Value}, store.Timestamp{Time: m.Previous.RevisedAt}
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO fx_rates
    (provider, source, base_currency, quote_currency, rate, rate_date, first_retrieved_at, retrieved_at, confirmed_through, previous_rate, revised_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (provider, source, base_currency, quote_currency, rate_date) DO UPDATE SET
    rate = excluded.rate, first_retrieved_at = excluded.first_retrieved_at, retrieved_at = excluded.retrieved_at,
    confirmed_through = excluded.confirmed_through, previous_rate = excluded.previous_rate, revised_at = excluded.revised_at`,
				m.Provider, m.Source, sqlvalue.Currency{Currency: m.Pair.Base}, sqlvalue.Currency{Currency: m.Pair.Quote},
				sqlvalue.Amount{Decimal: m.Value}, sqlvalue.Date{Date: m.Date},
				store.Timestamp{Time: m.FirstRetrievedAt}, store.Timestamp{Time: m.RetrievedAt},
				sqlvalue.Date{Date: m.ConfirmedThrough}, prev, revisedAt)
			if err != nil {
				return fmt.Errorf("fxrates: save %s %s rate for %s: %w", r.Provider, r.Pair, r.Date, err)
			}
			out = append(out, saved)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// find reads one rate: the one dated exactly date when exact, else the
// newest dated on or before date (no bound when date is zero).
func find(ctx context.Context, q querier, provider, source string, pair fx.Pair, date calendar.Date, exact bool) (fx.Rate, bool, error) {
	query := `SELECT provider, source, base_currency, quote_currency, rate, rate_date, first_retrieved_at, retrieved_at, confirmed_through, previous_rate, revised_at
FROM fx_rates WHERE provider = ? AND source = ? AND base_currency = ? AND quote_currency = ?`
	args := []any{provider, source, sqlvalue.Currency{Currency: pair.Base}, sqlvalue.Currency{Currency: pair.Quote}}
	switch {
	case exact:
		query += " AND rate_date = ?"
		args = append(args, sqlvalue.Date{Date: date})
	case !date.IsZero():
		query += " AND rate_date <= ?"
		args = append(args, sqlvalue.Date{Date: date})
	}
	query += " ORDER BY rate_date DESC LIMIT 1"
	r, err := scanRate(q.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return fx.Rate{}, false, nil
	}
	if err != nil {
		return fx.Rate{}, false, fmt.Errorf("fxrates: find %s %s %s rate: %w", provider, source, pair, err)
	}
	return r, true, nil
}

const rateColumns = `provider, source, base_currency, quote_currency, rate, rate_date, first_retrieved_at, retrieved_at, confirmed_through, previous_rate, revised_at`

type scanner interface{ Scan(dest ...any) error }

// scanRate reads one fx_rates row selected with rateColumns. sql.ErrNoRows is
// returned as is.
func scanRate(sc scanner) (fx.Rate, error) {
	var r fx.Rate
	var base, quote sqlvalue.Currency
	var value sqlvalue.Amount
	var rateDate, confirmed sqlvalue.Date
	var first, retrieved store.Timestamp
	var prev, revisedAt sql.NullString
	if err := sc.Scan(&r.Provider, &r.Source, &base, &quote, &value, &rateDate, &first, &retrieved, &confirmed, &prev, &revisedAt); err != nil {
		return fx.Rate{}, err
	}
	r.Pair = fx.Pair{Base: base.Currency, Quote: quote.Currency}
	r.Value, r.Date, r.ConfirmedThrough = value.Decimal, rateDate.Date, confirmed.Date
	r.FirstRetrievedAt, r.RetrievedAt = first.Time, retrieved.Time
	if prev.Valid {
		var p sqlvalue.Amount
		var at store.Timestamp
		if err := p.Scan(prev.String); err != nil {
			return fx.Rate{}, fmt.Errorf("previous rate: %w", err)
		}
		if err := at.Scan(revisedAt.String); err != nil {
			return fx.Rate{}, fmt.Errorf("revision time: %w", err)
		}
		r.Previous = &fx.Superseded{Value: p.Decimal, RevisedAt: at.Time}
	}
	return r, nil
}

var _ fx.Lister = (*Store)(nil)

// List implements [fx.Lister]: the rates matching f, ordered by provider,
// source, base, quote and then rate date newest first, resuming after f.After.
func (s *Store) List(ctx context.Context, f fx.ListFilter) ([]fx.Rate, error) {
	query := "SELECT " + rateColumns + " FROM fx_rates WHERE 1 = 1"
	var args []any
	if f.Provider != "" {
		query += " AND provider = ?"
		args = append(args, f.Provider)
	}
	if f.Source != "" {
		query += " AND source = ?"
		args = append(args, f.Source)
	}
	if !f.Base.IsZero() {
		query += " AND base_currency = ?"
		args = append(args, sqlvalue.Currency{Currency: f.Base})
	}
	if !f.Quote.IsZero() {
		query += " AND quote_currency = ?"
		args = append(args, sqlvalue.Currency{Currency: f.Quote})
	}
	if !f.From.IsZero() {
		query += " AND rate_date >= ?"
		args = append(args, sqlvalue.Date{Date: f.From})
	}
	if !f.To.IsZero() {
		query += " AND rate_date <= ?"
		args = append(args, sqlvalue.Date{Date: f.To})
	}
	if k := f.After; k != nil {
		base, quote := sqlvalue.Currency{Currency: k.Pair.Base}, sqlvalue.Currency{Currency: k.Pair.Quote}
		// The order mixes ascending keys with a descending date, so the
		// position is spelled out level by level.
		query += ` AND (provider > ?
 OR (provider = ? AND source > ?)
 OR (provider = ? AND source = ? AND base_currency > ?)
 OR (provider = ? AND source = ? AND base_currency = ? AND quote_currency > ?)
 OR (provider = ? AND source = ? AND base_currency = ? AND quote_currency = ? AND rate_date < ?))`
		args = append(args, k.Provider,
			k.Provider, k.Source,
			k.Provider, k.Source, base,
			k.Provider, k.Source, base, quote,
			k.Provider, k.Source, base, quote, sqlvalue.Date{Date: k.Date})
	}
	query += " ORDER BY provider, source, base_currency, quote_currency, rate_date DESC"
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}
	var out []fx.Rate
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		out = out[:0]
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scanRate(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("fxrates: list rates: %w", err)
	}
	return out, nil
}

// Find implements [fx.Cache].
func (s *Store) Find(ctx context.Context, provider, source string, pair fx.Pair, onOrBefore calendar.Date) (fx.Rate, bool, error) {
	var (
		r  fx.Rate
		ok bool
	)
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		r, ok, err = find(ctx, tx, provider, source, pair, onOrBefore, false)
		return err
	})
	return r, ok, err
}
