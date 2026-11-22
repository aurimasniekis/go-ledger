package main

import (
	"context"
	"log/slog"

	"github.com/aurimasniekis/go-ledger/internal/logging"
	"github.com/aurimasniekis/go-ledger/internal/settings"
)

// bindLogging makes the logging group live (ADR-0066 point 16): a write to
// log.level through the settings API changes the level of the running logger
// without a restart. It is the first consumer of the settings service's
// subscriptions. log.format stays a deployment setting. The change itself is
// on record in the audit log (settings.updated), not in the log. The returned
// function removes the subscription.
func bindLogging(svc *settings.Service, level *slog.LevelVar) (cancel func()) {
	return svc.Subscribe(func(_ context.Context, snap *settings.Snapshot) error {
		lv, err := logging.ParseLevel(snap.Config().Log.Level)
		if err != nil {
			return err
		}
		level.Set(lv)
		return nil
	}, "logging")
}
