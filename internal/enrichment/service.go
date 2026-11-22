package enrichment

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
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
	// Pending lists up to limit transactions that have no answer yet (newest
	// first), then those flagged for a recheck (a decision about another
	// transaction of the same merchant changed), then those whose answer is
	// unavailable, or none when recheckNone is set, and last tried before
	// recheckBefore, and those whose none or ambiguous answer is older than
	// changedBefore (zero: none), oldest attempt first.
	Pending(ctx context.Context, limit int, recheckBefore time.Time, recheckNone bool, changedBefore time.Time) ([]Candidate, error)
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
	// RemoteRetention is how long remote-sourced answers may be kept; zero
	// keeps them until replaced. It applies even after the remote source was
	// switched off, so what it cached is still purged. When zero, an enabled
	// Remote's licence retention is used.
	RemoteRetention time.Duration

	mu sync.Mutex
	// changedAt is when the person's confirmations last changed (or the first
	// pass after a start). Answers of none or ambiguous older than it are
	// looked up again, batch after batch over as many passes as it takes, so
	// a change reaches every older row and not only the first batch (from the
	// PR #124 review).
	changedAt time.Time
}

// Report counts what a pass did. It carries no names or bank text.
type Report struct {
	Looked, Identified, Ambiguous, None, Failed, Expired int
}

func (s *Service) retention() time.Duration {
	if s.RemoteRetention > 0 {
		return s.RemoteRetention
	}
	if s.Remote != nil {
		return s.Remote.Licence().Retention
	}
	return 0
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
	if s.Learned != nil && s.Learned.Decided(sub) {
		return s.personDecided(ctx, sub)
	}
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

// personDecided answers for a key the person has already decided about: their
// mapping or rejection beats any curated or remote identity for the same key. A
// curated answer is only kept when it names the same merchant as the person's
// single mapping. Otherwise the person's suggestion (or an ambiguous answer when
// they named several) is returned, never the curated identity; a rejection
// answers none.
func (s *Service) personDecided(ctx context.Context, sub Subject) merchants.Answer {
	la, err := s.Learned.Lookup(ctx, sub)
	if err != nil || la.Status == merchants.StatusNone {
		return merchants.Answer{Status: merchants.StatusNone, Source: SourceNone}
	}
	if la.Status == merchants.StatusIdentified && s.Curated != nil {
		if ca, err := s.Curated.Lookup(ctx, sub); err == nil && ca.Status == merchants.StatusIdentified && strings.EqualFold(ca.Name, la.Name) {
			return ca
		}
	}
	return la
}

// Run is one pass: expire the remote answers the licence no longer lets Ledger
// keep, reload the person's confirmations, and look up the pending
// transactions. Per-transaction failures are counted and logged, never
// returned. The error is for the pass itself (the store could not be read) and
// callers on the import path only log it.
func (s *Service) Run(ctx context.Context) (Report, error) {
	var rep Report
	if ret := s.retention(); ret > 0 {
		n, err := s.Store.DeleteAnswersBefore(ctx, SourceRemote, s.now().Add(-ret))
		if err != nil {
			return rep, errors.New("enrichment: expiring remote answers failed")
		}
		rep.Expired = n
	}
	// A none answer can only change when something new could identify the
	// transaction: a remote source, or a changed confirmation. The curated list
	// changes with the code, so a first pass after a start always rechecks.
	recheckNone := s.Remote != nil || s.Learned == nil
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Learned != nil {
		cs, err := s.Store.Confirmations(ctx)
		if err != nil {
			return rep, errors.New("enrichment: reading confirmations failed")
		}
		if s.Learned.Load(cs) {
			s.changedAt = s.now()
		}
	}
	batch := s.Batch
	if batch <= 0 {
		batch = DefaultBatch
	}
	pending, err := s.Store.Pending(ctx, batch, s.now().Add(-RecheckAfter), recheckNone, s.changedAt)
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
