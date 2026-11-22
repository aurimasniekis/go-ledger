package enrichment

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
)

// Candidate is a transaction waiting for a lookup, with the bank text it
// starts from.
type Candidate struct {
	ID banking.TransactionID
	Subject
}

// Store is what the service needs from persistence (internal/store/merchants).
type Store interface {
	// Pending lists up to limit transactions that have no answer yet, or whose
	// answer is none or unavailable and older than recheckBefore.
	Pending(ctx context.Context, limit int, recheckBefore time.Time) ([]Candidate, error)
	RecordAnswer(ctx context.Context, id banking.TransactionID, a merchants.Answer) (merchants.Merchant, error)
	// Confirmations lists the person's confirmed and corrected mappings.
	Confirmations(ctx context.Context) ([]Confirmation, error)
	// DeleteAnswersBefore deletes the source's answers retrieved before t and
	// reports how many. It never touches a decision.
	DeleteAnswersBefore(ctx context.Context, source string, t time.Time) (int, error)
}

// Defaults of a pass.
const (
	DefaultBatch = 100
	// RecheckAfter is how long a none or unavailable answer stands before the
	// transaction is looked up again.
	RecheckAfter = 24 * time.Hour
)

// Service runs lookups. Local sources are consulted first; the remote source,
// if any, only for what they cannot identify.
type Service struct {
	Store   Store
	Curated Source
	Learned *Learned
	// Remote is nil unless the operator enabled it (D15).
	Remote *Remote
	Log    *slog.Logger
	Now    func() time.Time
	Batch  int
}

// Report counts what a pass did. It carries no names or bank text.
type Report struct {
	Looked, Identified, Ambiguous, None, Failed, Expired int
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Enrich looks one subject up and returns the answer to record. It never fails:
// a failing remote source is an unavailable answer, which the store keeps from
// replacing an earlier answer.
func (s *Service) Enrich(ctx context.Context, sub Subject) merchants.Answer {
	var best *merchants.Answer
	consider := func(a merchants.Answer, err error) {
		if err != nil || ctx.Err() != nil {
			return
		}
		if a.Status == merchants.StatusIdentified && (best == nil || best.Status != merchants.StatusIdentified || a.Confidence > best.Confidence) ||
			a.Status == merchants.StatusAmbiguous && best == nil {
			best = &a
		}
	}
	if s.Curated != nil {
		consider(s.Curated.Lookup(ctx, sub))
	}
	if s.Learned != nil {
		consider(s.Learned.Lookup(ctx, sub))
	}
	if best != nil && best.Status == merchants.StatusIdentified {
		return *best
	}
	if s.Remote != nil && ctx.Err() == nil {
		a, err := s.Remote.Lookup(ctx, sub)
		switch {
		case err != nil:
			if s.Log != nil {
				s.Log.Warn("remote merchant lookup failed", "error", err.Error())
			}
			if best == nil {
				return merchants.Answer{Status: merchants.StatusUnavailable, Source: SourceRemote}
			}
		case a.Status == merchants.StatusIdentified || a.Status == merchants.StatusAmbiguous && best == nil:
			return a
		}
	}
	if best != nil {
		return *best
	}
	return merchants.Answer{Status: merchants.StatusNone, Source: SourceNone}
}

// Run is one pass: expire the remote answers the licence no longer lets Ledger
// keep, reload the person's confirmations, and look up the pending
// transactions. Per-transaction failures are counted and logged, never
// returned. The error is for the pass itself (the store could not be read) and
// callers on the import path only log it.
func (s *Service) Run(ctx context.Context) (Report, error) {
	var rep Report
	if s.Remote != nil && s.Remote.Licence().Retention > 0 {
		n, err := s.Store.DeleteAnswersBefore(ctx, SourceRemote, s.now().Add(-s.Remote.Licence().Retention))
		if err != nil {
			return rep, errors.New("enrichment: expiring remote answers failed")
		}
		rep.Expired = n
	}
	if s.Learned != nil {
		cs, err := s.Store.Confirmations(ctx)
		if err != nil {
			return rep, errors.New("enrichment: reading confirmations failed")
		}
		s.Learned.Load(cs)
	}
	batch := s.Batch
	if batch <= 0 {
		batch = DefaultBatch
	}
	pending, err := s.Store.Pending(ctx, batch, s.now().Add(-RecheckAfter))
	if err != nil {
		return rep, errors.New("enrichment: listing pending transactions failed")
	}
	for _, c := range pending {
		if ctx.Err() != nil {
			break
		}
		a := s.Enrich(ctx, c.Subject)
		if _, err := s.Store.RecordAnswer(ctx, c.ID, a); err != nil {
			rep.Failed++
			if s.Log != nil {
				s.Log.Warn("recording a merchant answer failed", "transaction", string(c.ID), "error", err.Error())
			}
			continue
		}
		rep.Looked++
		switch a.Status {
		case merchants.StatusIdentified:
			rep.Identified++
		case merchants.StatusAmbiguous:
			rep.Ambiguous++
		case merchants.StatusUnavailable:
			rep.Failed++
		default:
			rep.None++
		}
	}
	return rep, nil
}
