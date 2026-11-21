package audit

import "time"

// Action names for the events the audit log records. Names are stable: they
// are read by the audit API and by people. Each is registered in [Definitions]
// with the detail fields it may carry.
const (
	SignInSucceeded Action = "auth.signin_succeeded" // Details: method, recovery_codes_remaining (method recovery_code)
	SignInFailed    Action = "auth.signin_failed"    // Details: method, reason

	// Sessions and credentials (A2, ADR-0026). The target of a session event
	// is the session; of a password or re-authentication event, the user.
	SessionEnded           Action = "auth.session_ended"           // Details: reason (signed_out, terminated)
	PasswordChanged        Action = "auth.password_changed"        // Details: by (self, administrator, host), sessions_ended
	Reauthenticated        Action = "auth.reauthenticated"         // Details: method
	ReauthenticationFailed Action = "auth.reauthentication_failed" // Details: method, reason

	// Second factors (A3, ADR-0030). The target is the user.
	TwoFactorEnabled   Action = "two_factor.enabled"                    // Details: method, replaced (true, false), sessions_ended
	TwoFactorDisabled  Action = "two_factor.disabled"                   // Details: method, by (self, administrator, host)
	RecoveryCodesReset Action = "two_factor.recovery_codes_regenerated" // Details: count

	OIDCLinked   Action = "oidc.linked"   // Details: provider
	OIDCUnlinked Action = "oidc.unlinked" // Details: provider

	TokenCreated   Action = "token.created"    // Details: grants, root, expires_at (never the token)
	TokenRevoked   Action = "token.revoked"    // the actor is the owner, an administrator or a token with tokens:write
	TokenWriteUsed Action = "token.write_used" // Details: method, route (the route pattern, never the URL), interactive_steps (skipped)

	MCPToolCalled Action = "mcp.tool_called" // state-changing MCP call. Details: tool, outcome

	CorrectionRecorded Action = "correction.recorded" // Details: kind
	CorrectionReversed Action = "correction.reversed" // Details: kind

	ReportingTimezoneChanged Action = "settings.reporting_timezone_changed" // Details: from, to
)

// Fixed value sets for detail fields that are not identifiers.
var (
	signInMethods = []string{"password", "totp", "recovery_code", "oidc"}
	// Reasons never say which part was wrong beyond the stage: a failed
	// password and an unknown username are both invalid_credentials.
	signInReasons = []string{"invalid_credentials", "invalid_second_factor", "account_disabled", "locked_out"}
	mcpOutcomes   = []string{"succeeded", "failed", "denied"}
	// A session ended by its own user signing out, or terminated from
	// another session of the same user. Sessions ended by a password change
	// are counted in auth.password_changed instead.
	sessionEndReasons = []string{"signed_out", "terminated"}
	passwordChangedBy = []string{"self", "administrator", "host"}
	correctionKinds   = []string{"match", "unmatch", "payment_status", "categorisation", "other"}
	overrideStatuses  = []string{"paid", "unpaid"}
	overrideOrNone    = []string{"none", "paid", "unpaid"}
)

// Field declares one allowed detail field. Enum, when set, lists the only
// accepted values.
type Field struct {
	Name string
	Enum []string
}

func f(name string, enum ...string) Field { return Field{Name: name, Enum: enum} }

// Definitions is every action the log accepts and the detail fields each may
// carry. A new action is added here in the change that raises it.
var Definitions = map[Action][]Field{
	UserCreated:     {f("username"), f("role")},
	UserUpdated:     {f("fields")},
	UserRoleChanged: {f("from"), f("to")},
	UserDisabled:    nil,
	UserEnabled:     nil,

	SignInSucceeded: {f("method", signInMethods...), f("recovery_codes_remaining")},
	SignInFailed:    {f("method", signInMethods...), f("reason", signInReasons...)},

	SessionEnded:           {f("reason", sessionEndReasons...)},
	PasswordChanged:        {f("by", passwordChangedBy...), f("sessions_ended")},
	Reauthenticated:        {f("method", signInMethods...)},
	ReauthenticationFailed: {f("method", signInMethods...), f("reason", signInReasons...)},

	TwoFactorEnabled:   {f("method", "totp"), f("replaced", "true", "false"), f("sessions_ended")},
	TwoFactorDisabled:  {f("method", "totp"), f("by", passwordChangedBy...)},
	RecoveryCodesReset: {f("count")},

	OIDCLinked:   {f("provider")},
	OIDCUnlinked: {f("provider")},

	TokenCreated: {f("grants"), f("root", "true", "false"), f("expires_at")},
	TokenRevoked: nil,
	// interactive_steps records that the request skipped two-factor and
	// recent re-authentication, which API tokens do by design (ADR-0029).
	TokenWriteUsed: {f("method"), f("route"), f("interactive_steps", "skipped")},

	MCPToolCalled: {f("tool"), f("outcome", mcpOutcomes...)},

	CorrectionRecorded: {f("kind", correctionKinds...)},
	CorrectionReversed: {f("kind", correctionKinds...)},

	ReportingTimezoneChanged: {f("from"), f("to")},

	CommitmentCreated: {f("kind"), f("currency")},
	CommitmentUpdated: {f("fields")},
	CommitmentDeleted: {f("kind"), f("unpaid_occurrences_removed")},

	OccurrencesRegenerated: {f("effective_from"), f("created"), f("updated"), f("removed"), f("kept_paid"), f("kept_history")},

	ManualPaymentRecorded:          {f("payment_id"), f("currency")},
	ManualPaymentDeleted:           {f("payment_id"), f("currency")},
	OccurrenceStatusOverridden:     {f("status", overrideStatuses...), f("previous", overrideOrNone...)},
	OccurrenceStatusOverrideUndone: {f("undone", overrideStatuses...), f("restored", overrideOrNone...)},
}

// SignIn returns a sign-in event. An actor of kind anonymous means no account
// matched; the attempted username is never recorded (it may be a mistyped
// password).
func SignIn(actor Actor, ok bool, method, reason string, at time.Time) Event {
	if ok {
		return Event{Action: SignInSucceeded, Actor: actor, TargetType: "user", TargetID: actor.UserID, At: at, Details: map[string]string{"method": method}}
	}
	return Event{Action: SignInFailed, Actor: actor, TargetType: "user", TargetID: actor.UserID, At: at, Details: map[string]string{"method": method, "reason": reason}}
}

// ForToken returns an event about an API token (created, revoked).
func ForToken(a Action, actor Actor, tokenID string, details map[string]string, at time.Time) Event {
	return Event{Action: a, Actor: actor, TargetType: "token", TargetID: tokenID, At: at, Details: details}
}

// MCPCall returns the event for a state-changing MCP tool call made with a
// token; the actor carries the token identity.
func MCPCall(actor Actor, tool, outcome string, at time.Time) Event {
	return Event{Action: MCPToolCalled, Actor: actor, TargetType: "mcp_tool", TargetID: tool, At: at, Details: map[string]string{"tool": tool, "outcome": outcome}}
}
