package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/logging"
)

// AuditPath is the audit log collection.
const AuditPath = APIPrefix + "/audit"

var grantAuditRead = authz.G(authz.ResourceAudit, authz.ActionRead)

// auditEntryJSON is the API representation of an audit entry. It has no
// free-form payload: details holds only the fields the action declares.
type auditEntryJSON struct {
	ID         string            `json:"id"`
	Action     audit.Action      `json:"action"`
	ActorKind  audit.ActorKind   `json:"actor_kind"`
	ActorUser  *string           `json:"actor_user_id"`
	ActorToken *string           `json:"actor_token_id"`
	TargetType *string           `json:"target_type"`
	TargetID   *string           `json:"target_id"`
	At         time.Time         `json:"at"`
	Details    map[string]string `json:"details"`
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toAuditJSON(r audit.Record) auditEntryJSON {
	d := r.Details
	if d == nil {
		d = map[string]string{}
	}
	return auditEntryJSON{ID: r.ID, Action: r.Action, ActorKind: r.Actor.Kind, ActorUser: nonEmpty(r.Actor.UserID),
		ActorToken: nonEmpty(r.Actor.TokenID), TargetType: nonEmpty(r.TargetType), TargetID: nonEmpty(r.TargetID), At: r.At.UTC(), Details: d}
}

func checkULID(v string) error {
	if !resource.ValidID(v) {
		return errors.New("must be an identifier")
	}
	return nil
}

// checkMax limits a filter value to n bytes, like the column it is compared to.
func checkMax(n int) func(string) error {
	return func(v string) error {
		if len(v) > n {
			return fmt.Errorf("must be at most %d characters", n)
		}
		return nil
	}
}

func checkTime(v string) error {
	if _, err := time.Parse(time.RFC3339Nano, v); err != nil {
		return errors.New("must be an RFC 3339 timestamp")
	}
	return nil
}

var auditListSpec = ListSpec{Filters: map[string]FilterSpec{
	"action": {Ops: []FilterOp{OpEq, OpPrefix}, Check: func(v string) error {
		if v == "" || len(v) > 64 {
			return errors.New("action must be 1 to 64 characters")
		}
		return nil
	}},
	"actor_kind": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
		switch audit.ActorKind(v) {
		case audit.ActorUser, audit.ActorToken, audit.ActorSystem, audit.ActorAnonymous:
			return nil
		}
		return errors.New("actor_kind must be user, token, system or anonymous")
	}},
	"actor_user_id":  {Ops: []FilterOp{OpEq}, Check: checkULID},
	"actor_token_id": {Ops: []FilterOp{OpEq}, Check: checkULID},
	"target_type":    {Ops: []FilterOp{OpEq}, Check: checkMax(32)},
	"target_id":      {Ops: []FilterOp{OpEq}, Check: checkMax(64)},
	"at":             {Ops: []FilterOp{OpGte, OpLt}, Check: checkTime},
}}

func registerAudit(s *Server, rd audit.Reader) {
	// The audit log is read-only through the API and administrator-only
	// (authz.ResourceAudit is AdminOnly). Entries are in ID order, oldest first.
	s.HandleAPI("GET "+AuditPath, Requires(grantAuditRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, auditListSpec)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		// ADR-0011: a single-value filter given twice is refused, never
		// dropped (which would widen the result to every entry).
		for _, field := range []string{"actor_user_id", "actor_token_id", "target_type", "target_id"} {
			if len(q.Filter(field, OpEq)) > 1 {
				WriteError(w, r, Validation(field, "this filter must be given once"))
				return
			}
		}
		f := audit.Filter{After: q.After, Limit: q.Limit}
		for _, v := range q.Filter("action", OpEq) {
			f.Actions = append(f.Actions, audit.Action(v))
		}
		if p := q.Filter("action", OpPrefix); len(p) == 1 {
			f.ActionPrefix = p[0]
		}
		for _, v := range q.Filter("actor_kind", OpEq) {
			f.ActorKinds = append(f.ActorKinds, audit.ActorKind(v))
		}
		one := func(field string) string {
			if v := q.Filter(field, OpEq); len(v) == 1 {
				return v[0]
			}
			return ""
		}
		f.ActorUserID, f.ActorTokenID = one("actor_user_id"), one("actor_token_id")
		f.TargetType, f.TargetID = one("target_type"), one("target_id")
		if v := q.Filter("at", OpGte); len(v) == 1 {
			f.From, _ = time.Parse(time.RFC3339Nano, v[0])
		}
		if v := q.Filter("at", OpLt); len(v) == 1 {
			f.To, _ = time.Parse(time.RFC3339Nano, v[0])
		}
		items, err := rd.List(r.Context(), f)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]auditEntryJSON, len(items))
		for i, rec := range items {
			out[i] = toAuditJSON(rec)
		}
		WriteJSON(w, http.StatusOK, NewPage(out, q, func(e auditEntryJSON) string { return e.ID }))
	}))
}

// WithAuditRecorder makes every state-changing API request made with an API
// token write a token.write_used entry (token identity, method and route
// pattern, never the URL or body) before the handler runs. If the entry
// cannot be written the request is refused with a 500 and nothing changes:
// a token write is never unaudited. Interactive sign-in steps such as 2FA are
// skipped for tokens, which is why their writes are logged.
func WithAuditRecorder(rec audit.Recorder, now func() time.Time) Option {
	if now == nil {
		now = time.Now
	}
	return func(s *Server) {
		s.auditTokenWrites = func(r *http.Request, p authz.Principal, pattern string) error {
			e := audit.Event{Action: audit.TokenWriteUsed, Actor: audit.ActorOf(p), TargetType: "route", TargetID: truncate(pattern, audit.MaxTargetID),
				At: now().UTC(), Details: map[string]string{"method": r.Method, "route": truncate(pattern, audit.MaxValueBytes)}}
			return rec.Record(r.Context(), e)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// auditTokenWrite records a token's state-changing request, if configured.
// It reports whether the request may proceed.
func (s *Server) auditTokenWrite(w http.ResponseWriter, r *http.Request, p authz.Principal, pattern string) bool {
	if s.auditTokenWrites == nil || p.Kind != authz.PrincipalToken || safeMethod(r.Method) {
		return true
	}
	// pattern is "METHOD /path"; keep the path.
	if _, path, ok := strings.Cut(pattern, " "); ok {
		pattern = path
	}
	if err := s.auditTokenWrites(r, p, pattern); err != nil {
		logging.FromContext(r.Context()).ErrorContext(r.Context(), "cannot record the audit entry of a token write; the request was refused", "error", err.Error())
		WriteError(w, r, Internal(err))
		return false
	}
	return true
}
