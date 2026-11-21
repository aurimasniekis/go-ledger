package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

const (
	auUser  = "01JD3Y4C8V9M2Q7W5R6T0XZBNA"
	auOther = "01JD3Y4C8V9M2Q7W5R6T0XZBNB"
	auToken = "01JD3Y4C8V9M2Q7W5R6T0XZBNT"
)

var (
	auAt    = time.Date(2026, 3, 4, 5, 6, 7, 8, time.UTC)
	auAdmin = audit.Actor{Kind: audit.ActorUser, UserID: auUser}
	auTok   = audit.Actor{Kind: audit.ActorToken, UserID: auUser, TokenID: auToken}
	auAnon  = audit.Actor{Kind: audit.ActorAnonymous}
)

// listedEvents is one event for every kind of event the audit log must
// record (task A5), written through the real writer.
func listedEvents() map[string]audit.Event {
	ev := func(a audit.Action, actor audit.Actor, tt, tid string, d map[string]string) audit.Event {
		return audit.Event{Action: a, Actor: actor, TargetType: tt, TargetID: tid, At: auAt, Details: d}
	}
	return map[string]audit.Event{
		"sign-in success":           audit.SignIn(auAdmin, true, "password", "", auAt),
		"sign-in failure":           audit.SignIn(auAnon, false, "password", "invalid_credentials", auAt),
		"sign-in failure known":     audit.SignIn(audit.Actor{Kind: audit.ActorUser, UserID: auOther}, false, "totp", "invalid_second_factor", auAt),
		"2FA enabled":               ev(audit.TwoFactorEnabled, auAdmin, "user", auUser, map[string]string{"method": "totp"}),
		"2FA disabled":              ev(audit.TwoFactorDisabled, auAdmin, "user", auOther, map[string]string{"method": "totp"}),
		"2FA recovery codes":        ev(audit.RecoveryCodesReset, auAdmin, "user", auUser, nil),
		"OIDC link":                 ev(audit.OIDCLinked, auAdmin, "user", auUser, map[string]string{"provider": "example"}),
		"OIDC unlink":               ev(audit.OIDCUnlinked, auAdmin, "user", auUser, map[string]string{"provider": "example"}),
		"role change":               ev(audit.UserRoleChanged, auAdmin, "user", auOther, map[string]string{"from": "read-only", "to": "administrator"}),
		"token create":              audit.ForToken(audit.TokenCreated, auAdmin, auToken, map[string]string{"grants": "users:read,audit:read", "expires_at": "2027-01-01T00:00:00Z"}, auAt),
		"token revoke":              audit.ForToken(audit.TokenRevoked, auAdmin, auToken, nil, auAt),
		"token write use":           ev(audit.TokenWriteUsed, auTok, "route", "/api/v1/users", map[string]string{"method": "POST", "route": "/api/v1/users"}),
		"state-changing MCP call":   audit.MCPCall(auTok, "update_user", "succeeded", auAt),
		"finance correction":        ev(audit.CorrectionRecorded, auAdmin, "occurrence", "01JD3Y4C8V9M2Q7W5R6T0XZBNC", map[string]string{"kind": "payment_status"}),
		"finance correction revert": ev(audit.CorrectionReversed, auAdmin, "occurrence", "01JD3Y4C8V9M2Q7W5R6T0XZBNC", map[string]string{"kind": "payment_status"}),
		"reporting timezone":        ev(audit.ReportingTimezoneChanged, audit.System, "setting", "reporting.timezone", map[string]string{"from": "UTC", "to": "Europe/Vilnius"}),
	}
}

func TestAuditLogRecordsEachListedEvent(t *testing.T) {
	ctx := context.Background()
	al := NewAuditLog(openTest(t, tempPath(t), Options{}))
	for name, e := range listedEvents() {
		t.Run(name, func(t *testing.T) {
			if err := al.Record(ctx, e); err != nil {
				t.Fatal(err)
			}
			got, err := al.List(ctx, audit.Filter{Actions: []audit.Action{e.Action}, TargetID: e.TargetID, ActorKinds: []audit.ActorKind{e.Actor.Kind}})
			if err != nil || len(got) == 0 {
				t.Fatalf("List = %v, %v", got, err)
			}
			r := got[len(got)-1]
			if r.Action != e.Action || r.Actor != e.Actor || r.TargetType != e.TargetType || r.TargetID != e.TargetID || !r.At.Equal(e.At) {
				t.Errorf("read back %+v, wrote %+v", r.Event, e)
			}
			if len(r.Details) != len(e.Details) {
				t.Errorf("details %v, wrote %v", r.Details, e.Details)
			}
			for k, v := range e.Details {
				if r.Details[k] != v {
					t.Errorf("detail %s = %q, wrote %q", k, r.Details[k], v)
				}
			}
			if len(r.ID) != 26 {
				t.Errorf("ID %q", r.ID)
			}
		})
	}
	if t.Failed() {
		return
	}
	all, _ := al.List(ctx, audit.Filter{Limit: 100})
	if len(all) != len(listedEvents()) {
		t.Errorf("%d entries, want %d", len(all), len(listedEvents()))
	}
}

func init() {
	fixtureChecks["0006_users.sql"] = func(t *testing.T, db *DB) {
		err := db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			if n := queryInt(t, tx, "SELECT count(*) FROM users WHERE (username = 'owner' AND role = 'administrator' AND disabled = 0) OR (username = 'viewer' AND disabled = 1 AND version = 2)"); n != 2 {
				t.Errorf("users changed by the upgrade: %d of 2 rows intact", n)
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM audit_log"); n != 0 {
				t.Errorf("audit_log has %d rows after upgrade, want an empty table", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditIDsIncreaseWithinAMillisecond(t *testing.T) {
	fixed := "01JD3Y4C8V9M2Q7W5R6T0XZZZZ"
	next := monotonicID(func() string { return fixed })
	a, b, c := next(), next(), next()
	if a >= b || b >= c || a != fixed || b != "01JD3Y4C8V9M2Q7W5R6T0Y0000" {
		t.Errorf("ids %s %s %s", a, b, c)
	}
	for _, id := range []string{a, b, c} {
		if !resource.ValidID(id) {
			t.Errorf("%s is not a valid ID", id)
		}
	}
}

func TestAuditTokenActorKeepsTokenIdentity(t *testing.T) {
	ctx := context.Background()
	al := NewAuditLog(openTest(t, tempPath(t), Options{}))
	if err := al.Record(ctx, audit.MCPCall(auTok, "update_user", "denied", auAt)); err != nil {
		t.Fatal(err)
	}
	got, _ := al.List(ctx, audit.Filter{ActorTokenID: auToken})
	if len(got) != 1 || got[0].Actor.UserID != auUser || got[0].Actor.Kind != audit.ActorToken {
		t.Fatalf("got %+v", got)
	}
}

func TestAuditLogIsAppendOnly(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, tempPath(t), Options{})
	al := NewAuditLog(db)
	if err := al.Record(ctx, listedEvents()["role change"]); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"UPDATE audit_log SET action = 'user.created'",
		"UPDATE audit_log SET details = '{}'",
		"DELETE FROM audit_log",
	} {
		err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error { _, err := tx.ExecContext(ctx, q); return err })
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: err = %v, want the append-only refusal", q, err)
		}
	}
	if got, _ := al.List(ctx, audit.Filter{}); len(got) != 1 {
		t.Errorf("%d entries after the refused changes, want 1", len(got))
	}
}

func TestAuditEntryIsWithdrawnWithItsTransaction(t *testing.T) {
	ctx := context.Background()
	al := NewAuditLog(openTest(t, tempPath(t), Options{}))
	boom := errors.New("boom")
	err := al.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := al.WriteAudit(ctx, tx, listedEvents()["role change"]); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if got, _ := al.List(ctx, audit.Filter{}); len(got) != 0 {
		t.Errorf("a rolled-back change left %d audit entries", len(got))
	}
	if err := al.WriteAudit(ctx, nil, listedEvents()["role change"]); err == nil {
		t.Error("WriteAudit without a transaction succeeded")
	}
}

func TestAuditLogRefusesSecretsAndFreeText(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, tempPath(t), Options{})
	al := NewAuditLog(db)
	const secret = "sentinel-secret-value"
	for name, e := range map[string]audit.Event{
		"password in details": {Action: audit.SignInFailed, Actor: auAnon, At: auAt, Details: map[string]string{"method": "password", "password": secret}},
		"body as a reason":    {Action: audit.SignInFailed, Actor: auAnon, At: auAt, Details: map[string]string{"reason": secret}},
		"token value":         {Action: audit.TokenCreated, Actor: auAdmin, At: auAt, Details: map[string]string{"token": secret}},
		"unknown action":      {Action: "request.body", Actor: auAdmin, At: auAt, Details: map[string]string{"body": secret}},
	} {
		if err := al.Record(ctx, e); !errors.Is(err, audit.ErrInvalid) || strings.Contains(err.Error(), secret) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if got, _ := al.List(ctx, audit.Filter{}); len(got) != 0 {
		t.Errorf("refused events were stored: %v", got)
	}
	// The schema has no free-form payload column: only these columns exist,
	// and details is constrained to a JSON object.
	var cols []string
	err := db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT name FROM pragma_table_info('audit_log') ORDER BY cid")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			cols = append(cols, n)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "seq id action actor_kind actor_user_id actor_token_id target_type target_id occurred_at details"
	if strings.Join(cols, " ") != want {
		t.Errorf("audit_log columns changed: %v (a new column needs an ADR-0025 review: no request bodies, no secrets)", cols)
	}
	err = db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO audit_log (id, action, actor_kind, occurred_at, details)
			VALUES ('01JD3Y4C8V9M2Q7W5R6T0XZBNA', 'user.enabled', 'system', '2026-01-01T00:00:00.000000000Z', 'not json')`)
		return err
	})
	if err == nil {
		t.Error("the table accepted details that are not a JSON object")
	}
}

func TestAuditListFiltersAndPages(t *testing.T) {
	ctx := context.Background()
	al := NewAuditLog(openTest(t, tempPath(t), Options{}))
	events := listedEvents()
	order := []string{"sign-in success", "sign-in failure", "token create", "token revoke", "token write use", "role change"}
	for i, name := range order {
		e := events[name]
		e.At = auAt.Add(time.Duration(i) * time.Hour)
		if err := al.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	names := func(f audit.Filter) string {
		got, err := al.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range got {
			out = append(out, string(r.Action))
		}
		return strings.Join(out, ",")
	}
	if got := names(audit.Filter{ActionPrefix: "token."}); got != "token.created,token.revoked,token.write_used" {
		t.Errorf("prefix: %s", got)
	}
	if got := names(audit.Filter{Actions: []audit.Action{audit.SignInFailed, audit.UserRoleChanged}}); got != "auth.signin_failed,user.role_changed" {
		t.Errorf("any of: %s", got)
	}
	if got := names(audit.Filter{ActorKinds: []audit.ActorKind{audit.ActorAnonymous}}); got != "auth.signin_failed" {
		t.Errorf("actor kind: %s", got)
	}
	if got := names(audit.Filter{ActorUserID: auUser, TargetType: "token"}); got != "token.created,token.revoked" {
		t.Errorf("actor+target: %s", got)
	}
	if got := names(audit.Filter{From: auAt.Add(2 * time.Hour), To: auAt.Add(4 * time.Hour)}); got != "token.created,token.revoked" {
		t.Errorf("time range [from, to): %s", got)
	}
	// Paging: fetch Limit+1 after the previous page's last ID.
	var seen []string
	after := ""
	for {
		got, err := al.List(ctx, audit.Filter{After: after, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range got[:min(2, len(got))] {
			seen = append(seen, r.ID)
		}
		if len(got) <= 2 {
			break
		}
		after = got[1].ID
	}
	if len(seen) != len(order) || !slices.IsSorted(seen) {
		t.Errorf("paging saw %d IDs (sorted: %v), want %d", len(seen), slices.IsSorted(seen), len(order))
	}
}
