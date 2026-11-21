package main

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

// The rates commands list the exchange rates Ledger has cached, report on the
// optional rate provider and ask it for fresh rates (task FX2c, ADR-0048).
// Reading needs rates:read; refresh needs rates:write, an administrator grant.
// Rates are shown exactly as published, with their date and when they were
// retrieved: a stale rate is marked and is never used in a converted total.

func ratesList(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("rates list")
	var f ledgerclient.RateFilter
	fs.StringVar(&f.Provider, "provider", "", "")
	fs.StringVar(&f.Source, "source", "", "")
	fs.StringVar(&f.Base, "base", "", "")
	fs.StringVar(&f.Quote, "quote", "", "")
	fs.StringVar(&f.From, "from", "", "")
	fs.StringVar(&f.Until, "until", "", "")
	l := addListFlags(fs)
	return runList(m, fs, args, l, func(ctx context.Context, cl *ledgerclient.Client, o ledgerclient.ListOptions) (ledgerclient.Page[ledgerclient.CachedRate], error) {
		f.ListOptions = o
		return cl.ListRates(ctx, f)
	}, []string{"PROVIDER", "SOURCE", "PAIR", "RATE", "RATE DATE", "RETRIEVED AT", "FRESHNESS"}, func(r ledgerclient.CachedRate) []any {
		fresh := "ok"
		if r.Stale {
			fresh = "stale (" + deref(r.StaleReason) + ")"
		}
		return []any{r.Provider, r.Source, r.Base + "/" + r.Quote, r.Value, r.Date, r.RetrievedAt, fresh}
	})
}

func ratesStatus(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("rates status")
	if _, rc := m.parse(fs, args, false); rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		s, err := c.GetRatesStatus(ctx)
		if err != nil {
			return err
		}
		return m.emit(s, func(w io.Writer) {
			kv(w, "provider", s.Provider, "source", s.Source, "enabled", strconv.FormatBool(s.Enabled),
				"reporting", deref(s.ReportingCurrency), "refresh", strconv.FormatBool(s.RefreshAvailable),
				"stale after", strconv.Itoa(s.StaleAfterDays)+" days", "last success", deref(s.LastSuccessAt))
			if s.LastRun != nil {
				ratesRunText(w, s.LastRun)
			}
		})
	})
}

func ratesRefresh(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("rates refresh")
	if _, rc := m.parse(fs, args, false); rc != 0 {
		return max(rc, 0)
	}
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		r, err := c.RefreshRates(ctx)
		if err != nil {
			return err
		}
		return m.emit(r, func(w io.Writer) { ratesRunText(w, r) })
	})
}

func ratesRunText(w io.Writer, r *ledgerclient.RatesRefreshRun) {
	kv(w, "last run", r.Trigger+" "+r.Outcome, "started", r.StartedAt, "finished", r.FinishedAt, "refreshed", strconv.Itoa(r.CurrenciesRefreshed))
	if r.Error != nil {
		kv(w, "error", fmt.Sprintf("%s (%s)", r.Error.Kind, r.Error.Code))
	}
}
