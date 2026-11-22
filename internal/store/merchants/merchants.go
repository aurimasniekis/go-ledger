// Package merchants persists merchant enrichments and the person's merchant
// decisions (task E1, ADR-0051) in the tables of migration 0026_merchants.
//
// It never reads or writes a bank transaction's own columns and nothing here
// touches amounts, occurrences, matches or payment state. A decision is
// written in one transaction with its audit event.
package merchants

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

var _ merchants.Store = (*Store)(nil)

// Store keeps merchant enrichments and reviews on a [store.DB].
type Store struct {
	db        *store.DB
	audit     store.AuditWriter
	now       func() time.Time
	threshold merchants.Confidence
}

// New returns a Store. aw records the audit events of decisions and is
// required; now defaults to time.Now. The reliability threshold defaults to
// [merchants.DefaultThreshold]; see [Store.WithThreshold].
func New(db *store.DB, aw store.AuditWriter, now func() time.Time) *Store {
	if aw == nil {
		panic("merchants.New: an audit writer is required")
	}
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, audit: aw, now: now, threshold: merchants.DefaultThreshold}
}

// WithThreshold returns a copy that treats an unreviewed identity as reliable
// from confidence t (the default is a named constant; D15 fixes the policy,
// not the number).
func (s *Store) WithThreshold(t merchants.Confidence) *Store {
	c := *s
	c.threshold = t
	return &c
}

func transactionExists(ctx context.Context, tx *sql.Tx, id banking.TransactionID) error {
	var one int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM bank_transactions WHERE id = ?", string(id)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return resource.ErrNotFound
	}
	return err
}

func getEnrichment(ctx context.Context, tx *sql.Tx, id banking.TransactionID) (*merchants.Enrichment, error) {
	var e merchants.Enrichment
	var conf sql.Null[int]
	var candidates string
	var retrieved, failed sql.Null[store.Timestamp]
	err := tx.QueryRowContext(ctx, "SELECT status, name, logo_ref, source, confidence, candidates, retrieved_at, last_failed_at FROM merchant_enrichments WHERE transaction_id = ?", string(id)).
		Scan(&e.Status, &e.Name, &e.LogoRef, &e.Source, &conf, &candidates, &retrieved, &failed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.TransactionID = id
	e.Confidence = merchants.Confidence(conf.V)
	if err := json.Unmarshal([]byte(candidates), &e.Candidates); err != nil {
		return nil, fmt.Errorf("merchants: candidates of %s: %w", id, err)
	}
	if len(e.Candidates) == 0 {
		e.Candidates = nil
	}
	e.RetrievedAt = retrieved.V.Time
	if failed.Valid {
		t := failed.V.Time
		e.LastFailedAt = &t
	}
	return &e, nil
}

func getReview(ctx context.Context, tx *sql.Tx, id banking.TransactionID) (*merchants.Review, error) {
	var r merchants.Review
	var user sql.Null[string]
	var kind, via string
	var at store.Timestamp
	err := tx.QueryRowContext(ctx, "SELECT decision, name, decided_kind, decided_user, decided_via, decided_at, version FROM merchant_reviews WHERE transaction_id = ?", string(id)).
		Scan(&r.Decision, &r.Name, &kind, &user, &via, &at, &r.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.TransactionID = id
	r.By = audit.Actor{Kind: audit.ActorKind(kind), UserID: user.V, Via: audit.Via(via)}
	r.Via, r.DecidedAt = audit.Via(via), at.Time
	return &r, nil
}

// maxSupersedeDepth bounds the walk over a chain of superseded rows.
const maxSupersedeDepth = 8

// newerReview orders decisions for inheritance: the later decision wins, and
// a tie goes to the lower transaction ID, so the answer never depends on row
// order.
func newerReview(a, b *merchants.Review) bool {
	if !a.DecidedAt.Equal(b.DecidedAt) {
		return a.DecidedAt.After(b.DecidedAt)
	}
	return a.TransactionID < b.TransactionID
}

// inheritedReview finds the decision a pending transaction left behind when
// its booking arrived under a new key: bankfacts links the old row to the new
// one with superseded_by, and the person's decision was made about the old
// row. It walks the chain back (bounded depth, each row once) and returns the
// most recent decision along it, or nil. A booking can have several superseded
// rows, so the choice is by decision time, not by walk order.
func inheritedReview(ctx context.Context, tx *sql.Tx, id banking.TransactionID) (*merchants.Review, error) {
	seen := map[banking.TransactionID]bool{id: true}
	var best *merchants.Review
	frontier := []banking.TransactionID{id}
	for depth := 0; depth < maxSupersedeDepth && len(frontier) > 0; depth++ {
		var next []banking.TransactionID
		for _, cur := range frontier {
			rows, err := tx.QueryContext(ctx, "SELECT id FROM bank_transactions WHERE superseded_by = ? ORDER BY id", string(cur))
			if err != nil {
				return nil, err
			}
			var preds []banking.TransactionID
			for rows.Next() {
				var p string
				if err := rows.Scan(&p); err != nil {
					_ = rows.Close()
					return nil, err
				}
				preds = append(preds, banking.TransactionID(p))
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
			for _, p := range preds {
				if seen[p] {
					continue
				}
				seen[p] = true
				r, err := getReview(ctx, tx, p)
				if err != nil {
					return nil, err
				}
				if r != nil && (best == nil || newerReview(r, best)) {
					best = r
				}
				next = append(next, p)
			}
		}
		frontier = next
	}
	return best, nil
}

// Audit values of the decision a change replaces (the from detail).
const (
	fromNone      = "none"
	fromInherited = "inherited"
)

// decisionBefore names the decision a change replaces: the transaction's own
// decision, else fromInherited when one is inherited, else fromNone.
func decisionBefore(ctx context.Context, tx *sql.Tx, id banking.TransactionID) (string, error) {
	prev, err := getReview(ctx, tx, id)
	if err != nil {
		return "", err
	}
	if prev != nil {
		return string(prev.Decision), nil
	}
	inherited, err := inheritedReview(ctx, tx, id)
	if err != nil {
		return "", err
	}
	if inherited != nil {
		return fromInherited, nil
	}
	return fromNone, nil
}

func (s *Store) resolveTx(ctx context.Context, tx *sql.Tx, id banking.TransactionID) (merchants.Merchant, error) {
	if err := transactionExists(ctx, tx, id); err != nil {
		return merchants.Merchant{}, err
	}
	e, err := getEnrichment(ctx, tx, id)
	if err != nil {
		return merchants.Merchant{}, err
	}
	r, err := getReview(ctx, tx, id)
	if err != nil {
		return merchants.Merchant{}, err
	}
	inherited := false
	if r == nil {
		// A decision about the superseded pending row stands for its booking.
		if r, err = inheritedReview(ctx, tx, id); err != nil {
			return merchants.Merchant{}, err
		}
		inherited = r != nil
	}
	m := merchants.Resolve(e, r, s.threshold)
	m.TransactionID = id
	m.ReviewInherited = inherited
	return m, nil
}

// Get returns the resolved merchant of a bank transaction, or
// [resource.ErrNotFound] when there is no such transaction. A transaction no
// lookup has been recorded for resolves to [merchants.StateNotLookedUp].
func (s *Store) Get(ctx context.Context, id banking.TransactionID) (merchants.Merchant, error) {
	var m merchants.Merchant
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) (err error) {
		m, err = s.resolveTx(ctx, tx, id)
		return err
	})
	return m, err
}

// RecordAnswer stores a lookup source's answer for a transaction (the entry
// point of the enrichment adapter, task E2). A later answer replaces an
// earlier one. An unavailable answer (the lookup failed, for instance the
// source was down) never replaces an earlier answer: it only records when the
// lookup last failed, so an outage cannot erase what is known. It never
// touches a decision.
func (s *Store) RecordAnswer(ctx context.Context, id banking.TransactionID, a merchants.Answer) (merchants.Merchant, error) {
	if err := commitments.Validation(a.Validate()); err != nil {
		return merchants.Merchant{}, err
	}
	at := s.now().UTC()
	var m merchants.Merchant
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := transactionExists(ctx, tx, id); err != nil {
			return err
		}
		if a.Status == merchants.StatusUnavailable {
			res, err := tx.ExecContext(ctx, "UPDATE merchant_enrichments SET last_failed_at = ? WHERE transaction_id = ?", store.Timestamp{Time: at}, string(id))
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				if _, err := tx.ExecContext(ctx, "INSERT INTO merchant_enrichments (transaction_id, status, source, last_failed_at) VALUES (?, 'unavailable', ?, ?)",
					string(id), a.Source, store.Timestamp{Time: at}); err != nil {
					return err
				}
			}
		} else {
			candidates, err := json.Marshal(append([]string{}, merchants.Candidates(a.Candidates)...))
			if err != nil {
				return err
			}
			var conf any
			if a.Status == merchants.StatusIdentified {
				conf = int(a.Confidence)
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO merchant_enrichments (transaction_id, status, name, logo_ref, source, confidence, candidates, retrieved_at, last_failed_at)"+
				" VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)"+
				" ON CONFLICT (transaction_id) DO UPDATE SET status = excluded.status, name = excluded.name, logo_ref = excluded.logo_ref, source = excluded.source,"+
				" confidence = excluded.confidence, candidates = excluded.candidates, retrieved_at = excluded.retrieved_at, last_failed_at = NULL",
				string(id), string(a.Status), a.Name, a.LogoRef, a.Source, conf, string(candidates), store.Timestamp{Time: at}); err != nil {
				return err
			}
		}
		var err error
		m, err = s.resolveTx(ctx, tx, id)
		return err
	})
	return m, err
}

var reviewActions = map[merchants.Decision]audit.Action{
	merchants.DecisionConfirmed: audit.MerchantConfirmed,
	merchants.DecisionCorrected: audit.MerchantCorrected,
	merchants.DecisionRejected:  audit.MerchantRejected,
}

func storedActor(actor audit.Actor) (kind audit.ActorKind, via audit.Via) {
	switch actor.Kind {
	case audit.ActorUser, audit.ActorToken:
		return actor.Kind, actor.Channel()
	}
	return audit.ActorSystem, audit.ViaSystem
}

func (s *Store) event(ctx context.Context, tx *sql.Tx, a audit.Action, actor audit.Actor, id banking.TransactionID, from string, at time.Time) error {
	_, via := storedActor(actor)
	return s.audit.WriteAudit(ctx, tx, audit.Event{Action: a, Actor: actor, TargetType: merchants.TargetType, TargetID: string(id), At: at,
		Details: map[string]string{"from": from, "via": string(via)}})
}

// Review records the person's decision about a transaction's merchant,
// replacing an earlier one, and returns the merchant as it resolves now. A
// confirmation needs an identified merchant and keeps the name it confirmed
// (merchants.ErrNothingToConfirm otherwise). The decision is stored apart from
// the enrichment and the bank transaction, so neither a new lookup nor a
// re-import of the transaction removes it.
func (s *Store) Review(ctx context.Context, actor audit.Actor, id banking.TransactionID, req merchants.Request) (merchants.Merchant, error) {
	req = req.Normalized()
	if err := commitments.Validation(req.Validate()); err != nil {
		return merchants.Merchant{}, err
	}
	at := s.now().UTC()
	var m merchants.Merchant
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := transactionExists(ctx, tx, id); err != nil {
			return err
		}
		name := req.Name
		if req.Decision == merchants.DecisionConfirmed {
			e, err := getEnrichment(ctx, tx, id)
			if err != nil {
				return err
			}
			if e == nil || e.Status != merchants.StatusIdentified {
				return merchants.ErrNothingToConfirm
			}
			name = e.Name
		}
		from, err := decisionBefore(ctx, tx, id)
		if err != nil {
			return err
		}
		kind, via := storedActor(actor)
		var user any
		if kind != audit.ActorSystem && actor.UserID != "" {
			user = actor.UserID
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO merchant_reviews (transaction_id, decision, name, decided_kind, decided_user, decided_via, decided_at)"+
			" VALUES (?, ?, ?, ?, ?, ?, ?)"+
			" ON CONFLICT (transaction_id) DO UPDATE SET decision = excluded.decision, name = excluded.name, decided_kind = excluded.decided_kind,"+
			" decided_user = excluded.decided_user, decided_via = excluded.decided_via, decided_at = excluded.decided_at, version = merchant_reviews.version + 1",
			string(id), string(req.Decision), name, string(kind), user, string(via), store.Timestamp{Time: at}); err != nil {
			return err
		}
		if err := s.event(ctx, tx, reviewActions[req.Decision], actor, id, from, at); err != nil {
			return err
		}
		m, err = s.resolveTx(ctx, tx, id)
		return err
	})
	return m, err
}

// ClearReview removes the person's decision, so the merchant resolves from the
// enrichment alone again. It returns [merchants.ErrNoReview] when there is
// none, and [merchants.ErrReviewInherited] when the only decision is inherited
// from a superseded pending transaction: that one is cleared instead.
func (s *Store) ClearReview(ctx context.Context, actor audit.Actor, id banking.TransactionID) (merchants.Merchant, error) {
	at := s.now().UTC()
	var m merchants.Merchant
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := transactionExists(ctx, tx, id); err != nil {
			return err
		}
		prev, err := getReview(ctx, tx, id)
		if err != nil {
			return err
		}
		if prev == nil {
			inherited, err := inheritedReview(ctx, tx, id)
			if err != nil {
				return err
			}
			if inherited != nil {
				return merchants.ErrReviewInherited
			}
			return merchants.ErrNoReview
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM merchant_reviews WHERE transaction_id = ?", string(id)); err != nil {
			return err
		}
		if err := s.event(ctx, tx, audit.MerchantReviewCleared, actor, id, string(prev.Decision), at); err != nil {
			return err
		}
		m, err = s.resolveTx(ctx, tx, id)
		return err
	})
	return m, err
}
