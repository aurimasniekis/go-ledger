package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// AuditWriter records audit events (ADR-0021). Writes call it inside their
// own transaction, after the change and before commit, so a change and its
// audit entry are kept or lost together once the audit log has a table (task
// A5 implements this port with an append-only table, [AuditLog]).
type AuditWriter interface {
	WriteAudit(ctx context.Context, tx *sql.Tx, e audit.Event) error
}

// LogAudit is an AuditWriter that writes each event as one structured "audit"
// log line and ignores tx. ledger serve uses [AuditLog] instead; this is for
// callers with no database. Events carry
// only identifiers and fixed non-secret details. Unlike a table, a line logged
// for a transaction that later fails to commit is not withdrawn.
func LogAudit(log *slog.Logger) AuditWriter { return logAudit{log} }

type logAudit struct{ log *slog.Logger }

func (l logAudit) WriteAudit(ctx context.Context, _ *sql.Tx, e audit.Event) error {
	attrs := []any{
		"action", string(e.Action),
		"actor_kind", string(e.Actor.Kind),
		"actor_user_id", e.Actor.UserID,
		"target_type", e.TargetType,
		"target_id", e.TargetID,
		"at", FormatTime(e.At),
	}
	if e.Actor.TokenID != "" {
		attrs = append(attrs, "actor_token_id", e.Actor.TokenID)
	}
	for _, k := range slices.Sorted(maps.Keys(e.Details)) {
		attrs = append(attrs, "detail_"+k, e.Details[k])
	}
	l.log.InfoContext(ctx, "audit", attrs...)
	return nil
}

// AuditLog is the append-only audit log (ADR-0025): it implements
// [AuditWriter] and audit.Recorder, and reads the log for the admin API.
type AuditLog struct {
	db    *DB
	newID func() string
}

var (
	_ AuditWriter    = (*AuditLog)(nil)
	_ audit.Recorder = (*AuditLog)(nil)
	_ audit.Reader   = (*AuditLog)(nil)
)

// NewAuditLog returns the audit log of db.
func NewAuditLog(db *DB) *AuditLog { return &AuditLog{db: db, newID: monotonicID(resource.NewID)} }

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// monotonicID wraps an ID source so that IDs from one process always increase,
// even within one millisecond, where plain ULIDs are random: the log is listed
// in ID order, which must be the order entries were written in.
func monotonicID(next func() string) func() string {
	var mu sync.Mutex
	var last string
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		id := next()
		if id <= last {
			b := []byte(last)
			i := len(b) - 1
			for ; i >= 0; i-- {
				if b[i] != 'Z' {
					b[i] = crockford[strings.IndexByte(crockford, b[i])+1]
					break
				}
				b[i] = '0'
			}
			if i >= 0 {
				id = string(b)
			}
		}
		last = id
		return id
	}
}

// WriteAudit validates e and inserts it in tx, so the entry commits or rolls
// back with the change it records. An invalid event is refused with an error
// that does not repeat its values, which makes the caller's change fail too.
func (a *AuditLog) WriteAudit(ctx context.Context, tx *sql.Tx, e audit.Event) error {
	if tx == nil {
		return fmt.Errorf("store: audit entry %s needs a transaction", e.Action)
	}
	if err := audit.Validate(e); err != nil {
		return err
	}
	details := e.Details
	if details == nil {
		details = map[string]string{}
	}
	body, err := json.Marshal(details) // sorted keys, so the stored text is stable
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_log
		(id, action, actor_kind, actor_user_id, actor_token_id, target_type, target_id, occurred_at, details)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.newID(), string(e.Action), string(e.Actor.Kind), e.Actor.UserID, e.Actor.TokenID,
		e.TargetType, e.TargetID, Timestamp{Time: e.At}, string(body))
	if err != nil {
		return fmt.Errorf("store: write audit entry %s: %w", e.Action, err)
	}
	return nil
}

// Record writes e in a transaction of its own, for events that are not part of
// another change (a failed sign-in, the use of a token).
func (a *AuditLog) Record(ctx context.Context, e audit.Event) error {
	return a.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error { return a.WriteAudit(ctx, tx, e) })
}

// List returns records in ID order that match f, up to f.Limit+1.
func (a *AuditLog) List(ctx context.Context, f audit.Filter) ([]audit.Record, error) {
	where := []string{"id > ?"}
	args := []any{f.After}
	in := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		where = append(where, col+" IN (?"+strings.Repeat(", ?", len(vals)-1)+")")
		for _, v := range vals {
			args = append(args, v)
		}
	}
	eq := func(col, v string) {
		if v != "" {
			where = append(where, col+" = ?")
			args = append(args, v)
		}
	}
	actions := make([]string, len(f.Actions))
	for i, x := range f.Actions {
		actions[i] = string(x)
	}
	in("action", actions)
	kinds := make([]string, len(f.ActorKinds))
	for i, k := range f.ActorKinds {
		kinds[i] = string(k)
	}
	in("actor_kind", kinds)
	if f.ActionPrefix != "" {
		where = append(where, "substr(action, 1, length(?)) = ?")
		args = append(args, f.ActionPrefix, f.ActionPrefix)
	}
	eq("actor_user_id", f.ActorUserID)
	eq("actor_token_id", f.ActorTokenID)
	eq("target_type", f.TargetType)
	eq("target_id", f.TargetID)
	if !f.From.IsZero() {
		where = append(where, "occurred_at >= ?")
		args = append(args, FormatTime(f.From))
	}
	if !f.To.IsZero() {
		where = append(where, "occurred_at < ?")
		args = append(args, FormatTime(f.To))
	}
	limit := f.Limit
	if limit < 1 {
		limit = 50
	}
	args = append(args, limit+1)
	var out []audit.Record
	err := a.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, action, actor_kind, actor_user_id, actor_token_id,
			target_type, target_id, occurred_at, details FROM audit_log WHERE `+strings.Join(where, " AND ")+" ORDER BY id LIMIT ?", args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r audit.Record
			var action, kind, details string
			var at Timestamp
			if err := rows.Scan(&r.ID, &action, &kind, &r.Actor.UserID, &r.Actor.TokenID, &r.TargetType, &r.TargetID, &at, &details); err != nil {
				return err
			}
			r.Action, r.Actor.Kind, r.At = audit.Action(action), audit.ActorKind(kind), at.Time
			if err := json.Unmarshal([]byte(details), &r.Details); err != nil {
				return fmt.Errorf("store: audit entry %s has unreadable details: %w", r.ID, err)
			}
			if len(r.Details) == 0 {
				r.Details = nil
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
