package main

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisiontasks"
	"github.com/aurimasniekis/go-ledger/internal/store"
	decstore "github.com/aurimasniekis/go-ledger/internal/store/decisions"
	organisationstore "github.com/aurimasniekis/go-ledger/internal/store/organisation"
)

// decisionAdapters are the decision-provider adapters built into this
// version (internal/providers). None yet: OpenRouter, OpenAI, Anthropic and
// TypeSafe are tasks L2 to L5. Each returns nil when its key is unset.
var decisionAdapters = func(config.Decisions) []decisions.Provider { return nil }

// decisionProviderNames lists the providers whose key may be configured.
var decisionProviderNames = []string{"openrouter", "openai", "anthropic", "typesafe"}

// checkDecisions is what `ledger config check` runs: the checks serve makes at
// startup that need no database, so a configuration that check accepts is not
// refused by serve for its decision settings. Both use ValidateConfig against
// the adapters built into this binary.
func checkDecisions(cfg config.Decisions) error {
	dc, ps := cfg.Domain()
	if len(ps) > 0 {
		return fmt.Errorf("decision settings: %s: %s", ps[0].Key, ps[0].Msg)
	}
	var descs []decisions.Descriptor
	for _, p := range decisionAdapters(cfg) {
		descs = append(descs, p.Descriptor())
	}
	if ps := decisions.ValidateConfig(dc, descs); len(ps) > 0 {
		return fmt.Errorf("%w (this version has no decision-provider adapters yet; switch the decisions.tasks.* off)", &decisions.ConfigError{Problems: ps})
	}
	return nil
}

// buildDecisions returns the decision service (ADR-0060), or nil when no task
// is enabled. An enabled task whose provider has no adapter in this build is
// a startup error, never a silent no-op: the operator asked for something
// Ledger cannot do.
func buildDecisions(cfg config.Decisions, db *store.DB, aw store.AuditWriter, now func() time.Time) (*decisions.Service, error) {
	dc, ps := cfg.Domain()
	if len(ps) > 0 {
		return nil, fmt.Errorf("decision settings: %s: %s", ps[0].Key, ps[0].Msg)
	}
	if !dc.AnyEnabled() {
		return nil, nil
	}
	st := decstore.New(db, aw, now, nil)
	svc, err := decisions.New(dc, decisionAdapters(cfg), decisions.Deps{Consents: st, Usage: st, Suggestions: st, Now: now})
	if err != nil {
		return nil, fmt.Errorf("%w (this version has no decision-provider adapters yet; switch the decisions.tasks.* off)", err)
	}
	return svc, nil
}

// buildDecisionTrigger returns what asks the decision tasks about the
// transactions a bank refresh stored (L6d, ADR-0065), or nil when svc is nil
// (no task enabled). It never fails an import: the import engine calls it after
// the refresh was stored, and every failure is logged. No merchant lookup is
// given: nothing classifies a merchant as a transfer yet (ADR-0060 note N4), so
// the merchant name is never sent rather than sent for a transfer.
func buildDecisionTrigger(svc *decisions.Service, cfg config.Decisions, db *store.DB, aw store.AuditWriter, now func() time.Time, facts banking.FactStore, org *organisationstore.Store, log *slog.Logger) *decisiontasks.Trigger {
	if svc == nil {
		return nil
	}
	dc, _ := cfg.Domain()
	st := decstore.New(db, aw, now, nil)
	st.UseCategoryResetter(org)
	st.UseAssigner(org)
	return &decisiontasks.Trigger{
		Tasks:   decisiontasks.New(svc, st, org),
		Source:  facts,
		Enabled: func(n decisions.TaskName) bool { return dc.Tasks[n].Enabled },
		Log:     log,
	}
}
