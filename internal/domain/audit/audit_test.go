package audit

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
)

const (
	uid = "01JD3Y4C8V9M2Q7W5R6T0XZBNA"
	tid = "01JD3Y4C8V9M2Q7W5R6T0XZBNT"
)

var (
	user  = Actor{Kind: ActorUser, UserID: uid}
	token = Actor{Kind: ActorToken, UserID: uid, TokenID: tid}
	at    = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
)

func TestEveryDefinedActionHasValidSample(t *testing.T) {
	for a, fields := range Definitions {
		d := map[string]string{}
		for _, f := range fields {
			v := "x"
			if len(f.Enum) > 0 {
				v = f.Enum[0]
			}
			d[f.Name] = v
		}
		if err := Validate(Event{Action: a, Actor: user, At: at, Details: d}); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
}

func TestListedEventsAreDefined(t *testing.T) {
	for _, a := range []Action{SignInSucceeded, SignInFailed, TwoFactorEnabled, TwoFactorDisabled, OIDCLinked, OIDCUnlinked,
		UserRoleChanged, TokenCreated, TokenRevoked, TokenWriteUsed, MCPToolCalled, CorrectionRecorded, CorrectionReversed, ReportingTimezoneChanged} {
		if _, ok := Definitions[a]; !ok {
			t.Errorf("%s is not defined", a)
		}
	}
}

func TestValidateRefuses(t *testing.T) {
	const secret = "hunter2-SECRET-sentinel"
	long := strings.Repeat("a", MaxValueBytes+1)
	cases := map[string]Event{
		"unknown action":         {Action: "made.up", Actor: user, At: at},
		"unknown detail field":   {Action: UserDisabled, Actor: user, At: at, Details: map[string]string{"body": secret}},
		"password field":         {Action: SignInFailed, Actor: Actor{Kind: ActorAnonymous}, At: at, Details: map[string]string{"method": "password", "password": secret}},
		"enum violation":         {Action: SignInFailed, Actor: Actor{Kind: ActorAnonymous}, At: at, Details: map[string]string{"reason": secret}},
		"long value":             {Action: OIDCLinked, Actor: user, At: at, Details: map[string]string{"provider": long}},
		"control character":      {Action: OIDCLinked, Actor: user, At: at, Details: map[string]string{"provider": "a\nb" + secret}},
		"no time":                {Action: UserEnabled, Actor: user},
		"user actor no user":     {Action: UserEnabled, Actor: Actor{Kind: ActorUser}, At: at},
		"token actor no token":   {Action: UserEnabled, Actor: Actor{Kind: ActorToken, UserID: uid}, At: at},
		"system actor with user": {Action: UserEnabled, Actor: Actor{Kind: ActorSystem, UserID: uid}, At: at},
		"unknown actor kind":     {Action: UserEnabled, Actor: Actor{Kind: "robot"}, At: at},
		"long target":            {Action: UserEnabled, Actor: user, At: at, TargetID: strings.Repeat("a", MaxTargetID+1)},
	}
	for name, e := range cases {
		err := Validate(e)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: the error repeats a value: %v", name, err)
		}
	}
}

func TestConstructors(t *testing.T) {
	e := SignIn(Actor{Kind: ActorAnonymous}, false, "password", "invalid_credentials", at)
	if e.Action != SignInFailed || e.TargetID != "" || Validate(e) != nil {
		t.Errorf("failed sign-in: %+v %v", e, Validate(e))
	}
	if e := SignIn(user, true, "oidc", "", at); e.Action != SignInSucceeded || e.TargetID != uid || Validate(e) != nil {
		t.Errorf("sign-in: %+v", e)
	}
	if e := ForToken(TokenCreated, user, tid, map[string]string{"grants": "users:read"}, at); e.TargetType != "token" || Validate(e) != nil {
		t.Errorf("token: %+v", e)
	}
	if e := MCPCall(token, "update_user", "succeeded", at); e.Actor.TokenID != tid || Validate(e) != nil {
		t.Errorf("mcp: %+v", e)
	}
	if a := ActorOf(authz.TokenPrincipal(tid, uid, authz.RoleAdministrator, nil)); a != token {
		t.Errorf("ActorOf token = %+v", a)
	}
}

func TestActionNamesAreStable(t *testing.T) {
	var got []string
	for a := range Definitions {
		got = append(got, string(a))
	}
	slices.Sort(got)
	want := "auth.password_changed auth.reauthenticated auth.reauthentication_failed auth.recovery_sign_in_issued auth.session_ended auth.signin_failed auth.signin_succeeded bank_connection.connected bank_connection.disconnected bank_connection.reconnect_started bank_connection.refresh_requested bank_connection.started commitment.created commitment.deleted commitment.updated correction.recorded correction.reversed manual_payment.deleted manual_payment.recorded mcp.tool_called occurrence.status_overridden occurrence.status_override_undone occurrences.regenerated oidc.linked oidc.unlinked payment_match.confirmed payment_match.corrected payment_match.rejected payment_match.reversed payment_match.suggested payment_match.unmatched settings.reporting_timezone_changed token.created token.revoked token.write_used two_factor.disabled two_factor.enabled two_factor.recovery_codes_regenerated user.created user.disabled user.enabled user.role_changed user.updated"
	if strings.Join(got, " ") != want {
		t.Errorf("action names changed (they are public); got\n%s\nwant\n%s", strings.Join(got, " "), want)
	}
}
