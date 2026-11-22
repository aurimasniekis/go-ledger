package main

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/alerts"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/monthview"
	"github.com/aurimasniekis/go-ledger/internal/logging"
	"github.com/aurimasniekis/go-ledger/internal/outbox"
	"github.com/aurimasniekis/go-ledger/internal/store"
	alertstore "github.com/aurimasniekis/go-ledger/internal/store/alerts"
	"github.com/aurimasniekis/go-ledger/internal/store/bankfacts"
)

// alertJobName is the "job" log field and metric label of the alert detectors.
const alertJobName = "alert-detectors"

// alertRaisedEvent is the Pulse event for a newly raised alert (task L6,
// ADR-0065): the kind and the object it is about, never the message, an amount
// or any bank text. It is recorded only while events.pulse.enabled and
// alerts.channels lists pulse.
var alertRaisedEvent = outbox.Definition{
	Entity: "alert", Action: "raised", Key: outbox.KeyULID,
	Trigger: "an alert is newly raised and alerts.channels lists pulse (a dismissed alert is never sent again)",
	Fields: []outbox.Field{
		{Name: "kind", Kind: outbox.KindString, Required: true, Doc: "consent_expiring, commitment_amount_changed or unmatched_overdue"},
		{Name: "object_type", Kind: outbox.KindString, Required: true, Doc: "bank_connection, commitment or occurrence"},
		{Name: "object_id", Kind: outbox.KindString, Required: true, Doc: "id of that object"},
	},
}

// pulseAlertChannel records alert.raised in the outbox in its own write
// transaction (a channel runs after the alert is stored; a failure is
// reported and never loses the alert). With publishing off the outbox records
// nothing, but config validation refuses that combination anyway.
func pulseAlertChannel(db *store.DB, ob *outbox.Outbox) alerts.Channel {
	return alerts.Channel{Name: config.AlertChannelPulse, Notify: func(ctx context.Context, a alerts.Alert) error {
		return db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := ob.Enqueue(ctx, tx, outbox.Event{Entity: "alert", Action: "raised", Key: strings.ToLower(a.ID),
				Payload: map[string]any{"kind": string(a.Kind), "object_type": string(a.ObjectType), "object_id": a.ObjectID}})
			return err
		})
	}}
}

// alertChannels builds the channels selected by alerts.channels.
func alertChannels(cfg config.Alerts, db *store.DB, ob *outbox.Outbox) []alerts.Channel {
	var out []alerts.Channel
	for _, c := range cfg.AlertChannels() {
		if c == config.AlertChannelPulse {
			out = append(out, pulseAlertChannel(db, ob))
		}
	}
	return out
}

// alertRunner runs the detectors; passes never overlap.
type alertRunner struct {
	det *alerts.Detector
	log *slog.Logger
	mu  sync.Mutex
	// done is told how a pass ended; nil discards.
	done func(ok bool, took time.Duration)
}

// newAlertRunner wires the detectors to the stores (ADR-0065). Payment state
// is derived exactly as the monthly view derives it.
func newAlertRunner(cfg config.Alerts, db *store.DB, aw store.AuditWriter, ob *outbox.Outbox, parts apiParts, facts *bankfacts.Store, zone calendar.Zone, now func() time.Time, log *slog.Logger) *alertRunner {
	if now == nil {
		now = time.Now
	}
	st := alertstore.New(db, aw, now, nil)
	view := monthview.Service{Occurrences: parts.commitments.Occurrences(), Payments: parts.payments.WithZone(zone), Grace: parts.commitments.Grace(),
		Commitments: parts.commitments, Deferrals: deferralsDep(parts.deferrals.WithZone(zone))}
	det := &alerts.Detector{
		Alerts: alerts.New(st, nil, alertChannels(cfg, db, ob)...), Store: st,
		Connections: facts, Commitments: parts.commitments, Months: view,
		Zone: zone, Today: func() (calendar.Date, error) { return zone.Today(now()) }, ConsentHorizonDays: cfg.ConsentHorizonDays,
	}
	return &alertRunner{det: det, log: log}
}

// Run does one pass; it logs and never panics the caller.
func (r *alertRunner) Run(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	start := time.Now()
	jl, _ := logging.Job(r.log, alertJobName)
	rep, err := r.det.Run(ctx)
	if err != nil && ctx.Err() == nil {
		jl.Warn("alert detection failed", "error", err.Error())
	}
	for _, e := range rep.Delivery {
		jl.Warn("an alert was raised but its wording or channel failed; the alert stands", "error", e.Error())
	}
	if rep.Raised > 0 || rep.Resolved > 0 {
		jl.Info("alerts updated", "raised", rep.Raised, "resolved", rep.Resolved)
	}
	if r.done != nil {
		r.done(err == nil, time.Since(start))
	}
}

// Loop runs a pass at once and then every interval until ctx ends.
func (r *alertRunner) Loop(ctx context.Context, every time.Duration) {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		r.Run(ctx)
		t.Reset(every)
	}
}
