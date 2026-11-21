package main

import (
	"fmt"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/providers/enablebanking"
)

// bankingProviders builds the configured Enable Banking applications, one per
// enabled mode slot (decision D10, ADR-0023). It parses the private keys, so
// a bad key stops startup; it makes no network call. Errors name the
// configuration key, never the app id or the key.
func bankingProviders(cfg config.EnableBanking) ([]*enablebanking.Provider, error) {
	var out []*enablebanking.Provider
	for _, s := range cfg.Slots() {
		if !s.App.Enabled {
			continue
		}
		prefix := "banking.enable_banking." + s.Mode
		key, err := enablebanking.ParsePrivateKey([]byte(s.App.PrivateKey))
		if err != nil {
			return nil, fmt.Errorf("%s.private_key: %w", prefix, err)
		}
		p, err := enablebanking.New(enablebanking.Config{
			Application: s.App.AppName(s.Mode),
			AppID:       s.App.AppID,
			Mode:        banking.Mode(s.Mode),
			Environment: banking.Environment(s.App.Environment),
			PrivateKey:  key,
			BaseURL:     cfg.BaseURL,
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", prefix, err)
		}
		out = append(out, p)
	}
	return out, nil
}
