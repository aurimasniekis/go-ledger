// Package decisions persists the decision layer's consent records, request
// counts and suggestions (task L1, ADR-0060) in the tables of migration
// 0031_decision_providers.
//
// It implements [decisions.Consents], [decisions.Usage] and
// [decisions.Suggestions]. Nothing here reads or writes a bank fact, a
// payment, a match or an organisation assignment: a suggestion is a row of
// its own until a feature applies it through its own rules.
package decisions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

var (
	_ decisions.Consents    = (*Store)(nil)
	_ decisions.Usage       = (*Store)(nil)
	_ decisions.Suggestions = (*Store)(nil)
)

// Store keeps decision data on a [store.DB].
type Store struct {
	db    *store.DB
	audit store.AuditWriter
	now   func() time.Time
	newID func() string
}

// New returns a Store. aw records consent changes and is required; now and
// newID default to time.Now and resource.NewID.
func New(db *store.DB, aw store.AuditWriter, now func() time.Time, newID func() string) *Store {
	if aw == nil {
		panic("decisions.New: an audit writer is required")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = resource.NewID
	}
	return &Store{db: db, audit: aw, now: now, newID: newID}
}

func who(a audit.Actor) (string, any) {
	switch a.Kind {
	case audit.ActorUser, audit.ActorToken:
		if a.UserID != "" {
			return string(a.Kind), a.UserID
		}
		return string(a.Kind), nil
	}
	return string(audit.ActorSystem), nil
}

func consentEvent(action audit.Action, actor audit.Actor, c decisions.Consent, at time.Time) audit.Event {
	return audit.Event{Action: action, Actor: actor, TargetType: "decision_consent", TargetID: c.ID, At: at,
		Details: map[string]string{"provider": c.Provider, "purpose": string(c.Purpose), "fields": joinFieldNames(c.Fields), "via": string(actor.Channel())}}
}

const consentColumns = "id, provider, purpose, terms, fields, granted_by_kind, granted_by_user, granted_at, revoked_by_kind, revoked_by_user, revoked_at, version"

func scanConsent(row interface{ Scan(...any) error }) (decisions.Consent, error) {
	var c decisions.Consent
	var purpose, gk, fields string
	var gu, rk, ru sql.Null[string]
	var granted store.Timestamp
	var revoked sql.Null[store.Timestamp]
	if err := row.Scan(&c.ID, &c.Provider, &purpose, &c.Terms, &fields, &gk, &gu, &granted, &rk, &ru, &revoked, &c.Version); err != nil {
		return decisions.Consent{}, err
	}
	c.Purpose = decisions.TaskName(purpose)
	if err := parseFields(fields, &c.Fields); err != nil {
		return decisions.Consent{}, err
	}
	c.GrantedBy = audit.Actor{Kind: audit.ActorKind(gk), UserID: gu.V}
	c.GrantedAt = granted.Time
	if revoked.Valid {
		t := revoked.V.Time
		c.RevokedAt = &t
		c.RevokedBy = &audit.Actor{Kind: audit.ActorKind(rk.V), UserID: ru.V}
	}
	return c, nil
}

// Grant records consent for one provider and purpose
// ([decisions.ErrConsentExists] when one is active).
func (s *Store) Grant(ctx context.Context, actor audit.Actor, provider string, purpose decisions.TaskName, terms string, fields []decisions.Field) (decisions.Consent, error) {
	terms = strings.TrimSpace(terms)
	if errs := decisions.ValidateConsent(provider, purpose, terms, fields); len(errs) > 0 {
		return decisions.Consent{}, commitments.Validation(errs)
	}
	fieldsJSON, err := marshalFields(fields)
	if err != nil {
		return decisions.Consent{}, err
	}
	now := s.now().UTC()
	c := decisions.Consent{ID: s.newID(), Provider: provider, Purpose: purpose, Terms: terms, Fields: slices.Clone(fields), GrantedBy: actor, GrantedAt: now, Version: 1}
	err = s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM decision_consents WHERE provider = ? AND purpose = ? AND revoked_at IS NULL", provider, string(purpose)).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return decisions.ErrConsentExists
		}
		kind, user := who(actor)
		if _, err := tx.ExecContext(ctx, "INSERT INTO decision_consents (id, provider, purpose, terms, fields, granted_by_kind, granted_by_user, granted_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			c.ID, provider, string(purpose), terms, fieldsJSON, kind, user, store.Timestamp{Time: now}); err != nil {
			return err
		}
		return s.audit.WriteAudit(ctx, tx, consentEvent(audit.DecisionConsentGranted, actor, c, now))
	})
	if err != nil {
		return decisions.Consent{}, err
	}
	return c, nil
}

// Revoke ends the active consent for a provider and purpose
// ([decisions.ErrNoConsent] when there is none).
func (s *Store) Revoke(ctx context.Context, actor audit.Actor, provider string, purpose decisions.TaskName) (decisions.Consent, error) {
	now := s.now().UTC()
	var c decisions.Consent
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		c, err = scanConsent(tx.QueryRowContext(ctx, "SELECT "+consentColumns+" FROM decision_consents WHERE provider = ? AND purpose = ? AND revoked_at IS NULL", provider, string(purpose)))
		if errors.Is(err, sql.ErrNoRows) {
			return decisions.ErrNoConsent
		}
		if err != nil {
			return err
		}
		kind, user := who(actor)
		if _, err := tx.ExecContext(ctx, "UPDATE decision_consents SET revoked_by_kind = ?, revoked_by_user = ?, revoked_at = ?, version = version + 1 WHERE id = ?",
			kind, user, store.Timestamp{Time: now}, c.ID); err != nil {
			return err
		}
		c.RevokedAt, c.RevokedBy, c.Version = &now, &actor, c.Version+1
		return s.audit.WriteAudit(ctx, tx, consentEvent(audit.DecisionConsentRevoked, actor, c, now))
	})
	if err != nil {
		return decisions.Consent{}, err
	}
	return c, nil
}

// Consents lists every consent, active and revoked, oldest first.
func (s *Store) Consents(ctx context.Context) ([]decisions.Consent, error) {
	var out []decisions.Consent
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT "+consentColumns+" FROM decision_consents ORDER BY granted_at, id")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			c, err := scanConsent(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// Granted implements [decisions.Consents]: only an active consent for exactly
// this provider and purpose counts, and it covers the fields it recorded.
func (s *Store) Granted(ctx context.Context, provider string, purpose decisions.TaskName) ([]decisions.Field, bool, error) {
	var raw string
	var ok bool
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT fields FROM decision_consents WHERE provider = ? AND purpose = ? AND revoked_at IS NULL", provider, string(purpose)).Scan(&raw)
		ok = err == nil
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil || !ok {
		return nil, false, err
	}
	var fields []decisions.Field
	if err := parseFields(raw, &fields); err != nil {
		return nil, false, err
	}
	return fields, true, nil
}

func joinFieldNames(fs []decisions.Field) string {
	s := make([]string, len(fs))
	for i, f := range fs {
		s[i] = string(f)
	}
	return strings.Join(s, ",")
}

func marshalFields(fields []decisions.Field) (string, error) {
	b, err := json.Marshal(fields)
	return string(b), err
}

func parseFields(raw string, out *[]decisions.Field) error {
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return fmt.Errorf("decisions: stored consent fields: %w", err)
	}
	return nil
}

// Reserve implements [decisions.Usage]: one attempt is counted, in one write,
// only while fewer than limit are counted for the task in the period.
func (s *Store) Reserve(ctx context.Context, task decisions.TaskName, period string, limit int) (bool, error) {
	if _, err := time.Parse("2006-01", period); err != nil {
		return false, fmt.Errorf("decisions: period %q is not a month", period)
	}
	var reserved bool
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO decision_usage (task, period, attempts, updated_at) SELECT ?, ?, 1, ? WHERE ? > 0
ON CONFLICT (task, period) DO UPDATE SET attempts = attempts + 1, updated_at = excluded.updated_at WHERE attempts < ?`,
			string(task), period, store.Timestamp{Time: s.now().UTC()}, limit, limit)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		reserved = n == 1
		return err
	})
	return reserved, err
}

// Attempts returns the attempts counted for a task in a period.
func (s *Store) Attempts(ctx context.Context, task decisions.TaskName, period string) (int, error) {
	var n int
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT attempts FROM decision_usage WHERE task = ? AND period = ?", string(task), period).Scan(&n)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	return n, err
}

// Record implements [decisions.Suggestions].
func (s *Store) Record(ctx context.Context, ns decisions.NewSuggestion) (decisions.Suggestion, error) {
	if !ns.Object.Valid() {
		return decisions.Suggestion{}, errors.New("decisions: invalid object reference")
	}
	doc, err := json.Marshal(ns.Decision)
	if err != nil {
		return decisions.Suggestion{}, err
	}
	sg := decisions.Suggestion{NewSuggestion: ns, ID: s.newID(), State: decisions.StatePending, Version: 1}
	sg.CreatedAt = ns.CreatedAt.UTC()
	var response any
	if ns.ResponseID != "" {
		response = ns.ResponseID
	}
	err = s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE decision_suggestions SET state = 'superseded', decided_at = ?, version = version + 1 WHERE task = ? AND object_type = ? AND object_id = ? AND state = 'pending'",
			store.Timestamp{Time: sg.CreatedAt}, string(ns.Task), ns.Object.Type, ns.Object.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO decision_suggestions (id, task, provider, model, response_id, object_type, object_id, decision, auto_apply, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			sg.ID, string(ns.Task), ns.Provider, ns.Model, response, ns.Object.Type, ns.Object.ID, string(doc), ns.AutoApply, store.Timestamp{Time: sg.CreatedAt})
		return err
	})
	if err != nil {
		return decisions.Suggestion{}, err
	}
	return sg, nil
}

// SuggestionFilter selects suggestions, newest first.
type SuggestionFilter struct {
	Task   decisions.TaskName
	Object *decisions.ObjectRef
	State  decisions.SuggestionState
	// Limit defaults to 100.
	Limit int
}

// Suggestions lists suggestions matching f.
func (s *Store) Suggestions(ctx context.Context, f SuggestionFilter) ([]decisions.Suggestion, error) {
	q := "SELECT id, task, provider, model, response_id, object_type, object_id, decision, auto_apply, state, created_at, decided_at, version FROM decision_suggestions WHERE 1 = 1"
	var args []any
	if f.Task != "" {
		q += " AND task = ?"
		args = append(args, string(f.Task))
	}
	if f.Object != nil {
		q += " AND object_type = ? AND object_id = ?"
		args = append(args, f.Object.Type, f.Object.ID)
	}
	if f.State != "" {
		q += " AND state = ?"
		args = append(args, string(f.State))
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, f.Limit)
	var out []decisions.Suggestion
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var sg decisions.Suggestion
			var task, state, doc string
			var response sql.Null[string]
			var created store.Timestamp
			var decided sql.Null[store.Timestamp]
			if err := rows.Scan(&sg.ID, &task, &sg.Provider, &sg.Model, &response, &sg.Object.Type, &sg.Object.ID, &doc, &sg.AutoApply, &state, &created, &decided, &sg.Version); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(doc), &sg.Decision); err != nil {
				return err
			}
			sg.Task, sg.State, sg.ResponseID, sg.CreatedAt = decisions.TaskName(task), decisions.SuggestionState(state), response.V, created.Time
			if decided.Valid {
				t := decided.V.Time
				sg.DecidedAt = &t
			}
			out = append(out, sg)
		}
		return rows.Err()
	})
	return out, err
}
