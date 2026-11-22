package decisions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
	"github.com/aurimasniekis/go-ledger/internal/domain/organisation"
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

// reversalOf reads the reversal columns selected after the suggestion's own.
func reversalOf(kind sql.Null[string], user sql.Null[string], at sql.Null[store.Timestamp]) *decisions.Reversal {
	if !at.Valid {
		return nil
	}
	return &decisions.Reversal{ByKind: kind.V, ByUserID: user.V, At: at.V.Time}
}

const viewColumns = "d.id, d.task, d.provider, d.model, d.response_id, d.object_type, d.object_id, d.decision, d.auto_apply, d.state, d.created_at, d.decided_at, d.version, r.reverted_by_kind, r.reverted_by_user, r.reverted_at"

const viewFrom = " FROM decision_suggestions d LEFT JOIN decision_suggestion_reversals r ON r.suggestion_id = d.id"

func scanView(row interface{ Scan(...any) error }) (decisions.SuggestionView, error) {
	var sg decisions.Suggestion
	var task, state, doc string
	var response sql.Null[string]
	var created store.Timestamp
	var decided, revertedAt sql.Null[store.Timestamp]
	var kind, user sql.Null[string]
	if err := row.Scan(&sg.ID, &task, &sg.Provider, &sg.Model, &response, &sg.Object.Type, &sg.Object.ID, &doc, &sg.AutoApply, &state, &created, &decided, &sg.Version, &kind, &user, &revertedAt); err != nil {
		return decisions.SuggestionView{}, err
	}
	if err := json.Unmarshal([]byte(doc), &sg.Decision); err != nil {
		return decisions.SuggestionView{}, err
	}
	sg.Task, sg.State, sg.ResponseID, sg.CreatedAt = decisions.TaskName(task), decisions.SuggestionState(state), response.V, created.Time
	if decided.Valid {
		t := decided.V.Time
		sg.DecidedAt = &t
	}
	return decisions.SuggestionView{Suggestion: sg, Reversal: reversalOf(kind, user, revertedAt)}, nil
}

// SuggestionView returns one suggestion with its reversal
// (resource.ErrNotFound when there is none).
func (s *Store) SuggestionView(ctx context.Context, id string) (decisions.SuggestionView, error) {
	var v decisions.SuggestionView
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		v, err = scanView(tx.QueryRowContext(ctx, "SELECT "+viewColumns+viewFrom+" WHERE d.id = ?", id))
		if errors.Is(err, sql.ErrNoRows) {
			return resource.ErrNotFound
		}
		return err
	})
	return v, err
}

// ListSuggestions returns suggestions matching f, oldest first.
func (s *Store) ListSuggestions(ctx context.Context, f decisions.SuggestionFilter) ([]decisions.SuggestionView, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var where []string
	var args []any
	if len(f.Tasks) > 0 {
		where = append(where, "d.task IN ("+placeholders(len(f.Tasks))+")")
		for _, t := range f.Tasks {
			args = append(args, string(t))
		}
	}
	if len(f.States) > 0 {
		var ors []string
		for _, st := range f.States {
			switch st {
			case decisions.StateReverted:
				ors = append(ors, "r.suggestion_id IS NOT NULL")
			case decisions.StateAccepted:
				ors = append(ors, "(d.state = 'accepted' AND r.suggestion_id IS NULL)")
			default:
				ors = append(ors, "d.state = ?")
				args = append(args, string(st))
			}
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}
	if f.ObjectType != "" {
		where = append(where, "d.object_type = ?")
		args = append(args, f.ObjectType)
	}
	if f.ObjectID != "" {
		where = append(where, "d.object_id = ?")
		args = append(args, f.ObjectID)
	}
	if f.After != "" {
		where = append(where, "d.id > ?")
		args = append(args, f.After)
	}
	q := "SELECT " + viewColumns + viewFrom
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY d.id LIMIT ?"
	args = append(args, limit)
	var out []decisions.SuggestionView
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			v, err := scanView(rows)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

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
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error { return s.recordReversalTx(ctx, tx, actor, id) })
}

// CategoryResetter resets an object's category only while it is still the
// given model-origin assignment, and runs inTx in the same transaction
// (internal/store/organisation implements it).
type CategoryResetter interface {
	ResetAutomaticCategoryWith(ctx context.Context, actor audit.Actor, obj organisation.ObjectRef, source, categoryID string, inTx func(ctx context.Context, tx *sql.Tx) error) error
}

// UseCategoryResetter sets the organisation store RevertAutomatic resets
// categories through. Both stores must use the same database.
func (s *Store) UseCategoryResetter(r CategoryResetter) { s.resetter = r }

// RevertAutomatic takes back an auto-applied category and records the
// reversal in one transaction: the conditional reset (the object's own
// category is still the model's) and the reversal row with its audit event
// commit together or not at all. A category changed since is
// [decisions.ErrNotReversible], as is a suggestion that cannot be reverted or
// was reverted already; nothing changes then.
func (s *Store) RevertAutomatic(ctx context.Context, actor audit.Actor, id string, obj organisation.ObjectRef, source, categoryID string) error {
	if s.resetter == nil {
		return errors.New("decisions: no category resetter is set")
	}
	err := s.resetter.ResetAutomaticCategoryWith(ctx, actor, obj, source, categoryID, func(ctx context.Context, tx *sql.Tx) error {
		return s.recordReversalTx(ctx, tx, actor, id)
	})
	if errors.Is(err, organisation.ErrAssignmentChanged) {
		return decisions.ErrNotReversible
	}
	return err
}

func (s *Store) recordReversalTx(ctx context.Context, tx *sql.Tx, actor audit.Actor, id string) error {
	now := s.now().UTC()
	{
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
	}
}

// Reverted reports whether a suggestion's effect was taken back.
func (s *Store) Reverted(ctx context.Context, id string) (bool, error) {
	var n int
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM decision_suggestion_reversals WHERE suggestion_id = ?", id).Scan(&n)
	})
	return n > 0, err
}

// Pending reports whether a suggestion of the task for the object is still
// waiting for review: asking again would only supersede it.
func (s *Store) Pending(ctx context.Context, task decisions.TaskName, obj decisions.ObjectRef) (bool, error) {
	var n int
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM decision_suggestions WHERE task = ? AND object_type = ? AND object_id = ? AND state = 'pending'",
			string(task), obj.Type, obj.ID).Scan(&n)
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
