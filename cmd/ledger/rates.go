package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	moneypkg "github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/providers/frankfurter"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/fxrates"
)

// buildRates builds the exchange-rate service (FX2, ADR-0046) and, when
// rates.enabled and a reporting currency is set, the daily refresh job.
//
// The service is always built: it reads the cache, so rates retrieved while
// the provider was enabled stay usable (and visibly aged) after it is turned
// off, and with an empty cache every conversion says no_provider. Only
// rates.enabled gives it a provider. Nothing contacts Frankfurter at startup:
// the first request or the job's first cycle does.
func buildRates(cfg config.Rates, reporting moneypkg.Currency, db *store.DB, now func() time.Time, log *slog.Logger) (*fx.Service, *fx.RefreshJob, error) {
	opts := fx.Options{
		Freshness: fx.Freshness{RefreshAfter: cfg.RefreshInterval, StaleAfter: cfg.StaleAfter},
		Now:       now, Log: log,
		ProviderName: frankfurter.Name, Source: frankfurter.NormalizeSource(cfg.Source),
	}
	var provider fx.Provider
	if cfg.Enabled {
		c, err := frankfurter.New(frankfurter.Options{
			BaseURL: cfg.BaseURL, Source: cfg.Source, Timeout: cfg.Timeout, Now: now,
			// Never follow a redirect: a base URL that redirects would send
			// the request somewhere the operator did not configure.
			HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		})
		if err != nil {
			return nil, nil, fmt.Errorf("exchange rates: %w", err)
		}
		provider = c
	}
	svc, err := fx.NewService(fxrates.New(db), provider, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("exchange rates: %w", err)
	}
	if provider == nil || reporting.IsZero() {
		return svc, nil, nil
	}
	return svc, &fx.RefreshJob{
		Service: svc, Target: reporting, Interval: cfg.RefreshInterval, Log: log,
		Currencies: func(ctx context.Context) ([]moneypkg.Currency, error) { return currenciesInUse(ctx, db) },
	}, nil
}

// currenciesInUse lists the distinct currencies of bank accounts, balances
// and commitments: the ones a converted total may need a rate for.
func currenciesInUse(ctx context.Context, db *store.DB) ([]moneypkg.Currency, error) {
	var out []moneypkg.Currency
	err := db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		out = out[:0]
		rows, err := tx.QueryContext(ctx, `SELECT currency FROM bank_accounts UNION SELECT currency FROM bank_balance_snapshots UNION SELECT currency FROM commitments ORDER BY 1`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var code string
			if err := rows.Scan(&code); err != nil {
				return err
			}
			c, err := moneypkg.ParseCurrency(code)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("listing currencies in use: %w", err)
	}
	return out, nil
}
