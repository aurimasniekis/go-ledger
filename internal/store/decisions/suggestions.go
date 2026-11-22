package decisions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

const suggestionColumns = "id, task, provider, model, response_id, object_type, object_id, decision, auto_apply, state, created_at, decided_at, version"

func scanSuggestion(row interface{ Scan(...any) error }) (decisions.Suggestion, error) {
	var sg decisions.Suggestion
	var task, state, doc string
	var response sql.Null[string]
	var created store.Timestamp
	var decided sql.Null[store.Timestamp]
	if err := row.Scan(&sg.ID, &task, &sg.Provider, &sg.Model, &response, &sg.Object.Type, &sg.Object.ID, &doc, &sg.AutoApply, &state, &created, &decided, &sg.Version); err != nil {
		return decisions.Suggestion{}, err
	}
	if err := json.Unmarshal([]byte(doc), &sg.Decision); err != nil {
		return decisions.Suggestion{}, err
	}
	sg.Task, sg.State, sg.ResponseID, sg.CreatedAt = decisions.TaskName(task), decisions.SuggestionState(state), response.V, created.Time
	if decided.Valid {
		t := decided.V.Time
		sg.DecidedAt = &t
	}
	return sg, nil
}

func suggestionEvent(action audit.Action, actor audit.Actor, sg decisions.Suggestion, at time.Time) audit.Event {
	return audit.Event{Action: action, Actor: actor, TargetType: "decision_suggestion", TargetID: sg.ID, At: at,
		Details: map[string]string{"task": string(sg.Task), "provider": sg.Provider, "via": string(actor.Channel())}}
}

// Suggestion returns one suggestion (resource.ErrNotFound when there is none).
func (s *Store) Suggestion(ctx context.Context, id string) (decisions.Suggestion, error) {
	var sg decisions.Suggestion
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		sg, err = scanSuggestion(tx.QueryRowContext(ctx, "SELECT "+suggestionColumns+" FROM decision_suggestions WHERE id = ?", id))
		if errors.Is(err, sql.ErrNoRows) {
			return resource.ErrNotFound
		}
		return err
	})
	return sg, err
}

// Decide moves a pending suggestion to a final state once, and records the
// audit event named by action, in one transaction. A suggestion that is no
// longer pending is [decisions.ErrNotPending]. An empty action records no
// event (a suggestion superseded by the system, which is not a decision).
func (s *Store) Decide(ctx context.Context, actor audit.Actor, id string, to decisions.SuggestionState, action audit.Action) (decisions.Suggestion, error) {
	if to == decisions.StatePending || !to.Valid() {
		return decisions.Suggestion{}, errors.New("decisions: a suggestion moves to accepted, rejected or superseded")
	}
	now := s.now().UTC()
	var out decisions.Suggestion
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE decision_suggestions SET state = ?, decided_at = ?, version = version + 1 WHERE id = ? AND state = 'pending'", string(to), store.Timestamp{Time: now}, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var one int
			if err := tx.QueryRowContext(ctx, "SELECT 1 FROM decision_suggestions WHERE id = ?", id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
				return resource.ErrNotFound
			} else if err != nil {
				return err
			}
			return decisions.ErrNotPending
		}
		if out, err = scanSuggestion(tx.QueryRowContext(ctx, "SELECT "+suggestionColumns+" FROM decision_suggestions WHERE id = ?", id)); err != nil {
			return err
		}
		if action == "" {
			return nil
		}
		return s.audit.WriteAudit(ctx, tx, suggestionEvent(action, actor, out, now))
	})
	return out, err
}

// RecordReversal records that a person took back an auto-applied suggestion's
// effect, with its audit event. Only an accepted suggestion that was marked
// for auto-apply can be reverted, once ([decisions.ErrNotReversible]).
func (s *Store) RecordReversal(ctx context.Context, actor audit.Actor, id string) error {
	now := s.now().UTC()
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		sg, err := scanSuggestion(tx.QueryRowContext(ctx, "SELECT "+suggestionColumns+" FROM decision_suggestions WHERE id = ?", id))
		if errors.Is(err, sql.ErrNoRows) {
			return resource.ErrNotFound
		}
		if err != nil {
			return err
		}
		if sg.State != decisions.StateAccepted || !sg.AutoApply {
			return decisions.ErrNotReversible
		}
		var done int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM decision_suggestion_reversals WHERE suggestion_id = ?", id).Scan(&done); err != nil {
			return err
		}
		if done > 0 {
			return decisions.ErrNotReversible
		}
		kind, user := who(actor)
		if _, err := tx.ExecContext(ctx, "INSERT INTO decision_suggestion_reversals (suggestion_id, reverted_by_kind, reverted_by_user, reverted_at) VALUES (?, ?, ?, ?)",
			id, kind, user, store.Timestamp{Time: now}); err != nil {
			return err
		}
		return s.audit.WriteAudit(ctx, tx, suggestionEvent(audit.DecisionSuggestionReverted, actor, sg, now))
	})
}

// Reverted reports whether a suggestion's effect was taken back.
func (s *Store) Reverted(ctx context.Context, id string) (bool, error) {
	var n int
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM decision_suggestion_reversals WHERE suggestion_id = ?", id).Scan(&n)
	})
	return n > 0, err
}

// Declined reports whether a person has turned down a suggestion of the task
// for the object: rejected, corrected (a rejection with their own answer) or
// reverted. A model run does not ask the same question again.
func (s *Store) Declined(ctx context.Context, task decisions.TaskName, obj decisions.ObjectRef) (bool, error) {
	var n int
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM decision_suggestions d
WHERE d.task = ? AND d.object_type = ? AND d.object_id = ?
AND (d.state = 'rejected' OR EXISTS (SELECT 1 FROM decision_suggestion_reversals r WHERE r.suggestion_id = d.id))`,
			string(task), obj.Type, obj.ID).Scan(&n)
	})
	return n > 0, err
}
