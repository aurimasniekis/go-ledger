package store

import (
	"context"
	"database/sql"
	"log/slog"
	"maps"
	"slices"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
)

// AuditWriter records audit events (ADR-0019). Writes call it inside their
// own transaction, after the change and before commit, so a change and its
// audit entry are kept or lost together once the audit log has a table (task
// A5 implements this port with an append-only table).
type AuditWriter interface {
	WriteAudit(ctx context.Context, tx *sql.Tx, e audit.Event) error
}

// LogAudit is the interim AuditWriter until the audit table exists: it writes
// each event as one structured "audit" log line and ignores tx. Events carry
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
