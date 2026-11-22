package main

import (
	"fmt"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
	"github.com/aurimasniekis/go-ledger/internal/store"
	decstore "github.com/aurimasniekis/go-ledger/internal/store/decisions"
)

// decisionAdapters are the decision-provider adapters built into this
// version (internal/providers). None yet: OpenRouter, OpenAI, Anthropic and
// TypeSafe are tasks L2 to L5. Each returns nil when its key is unset.
var decisionAdapters = func(config.Decisions) []decisions.Provider { return nil }

// decisionProviderNames lists the providers whose key may be configured.
var decisionProviderNames = []string{"openrouter", "openai", "anthropic", "typesafe"}

// buildDecisions returns the decision service (ADR-0059), or nil when no task
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
