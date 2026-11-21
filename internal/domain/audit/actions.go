package audit

import "time"

// Action names for the events the audit log records. Names are stable: they
// are read by the audit API and by people. Each is registered in [Definitions]
// with the detail fields it may carry.
const (
	SignInSucceeded Action = "auth.signin_succeeded" // Details: method
	SignInFailed    Action = "auth.signin_failed"    // Details: method, reason

	TwoFactorEnabled   Action = "two_factor.enabled"
	TwoFactorDisabled  Action = "two_factor.disabled"
	RecoveryCodesReset Action = "two_factor.recovery_codes_regenerated"

	OIDCLinked   Action = "oidc.linked"   // Details: provider
	OIDCUnlinked Action = "oidc.unlinked" // Details: provider

	TokenCreated   Action = "token.created"    // Details: grants, expires_at
	TokenRevoked   Action = "token.revoked"    //
	TokenWriteUsed Action = "token.write_used" // Details: method, route (the route pattern, never the URL)

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
	signInReasons   = []string{"invalid_credentials", "invalid_second_factor", "account_disabled", "locked_out"}
	mcpOutcomes     = []string{"succeeded", "failed", "denied"}
	correctionKinds = []string{"match", "unmatch", "payment_status", "categorisation", "other"}
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

	SignInSucceeded: {f("method", signInMethods...)},
	SignInFailed:    {f("method", signInMethods...), f("reason", signInReasons...)},

	TwoFactorEnabled:   {f("method", "totp")},
	TwoFactorDisabled:  {f("method", "totp")},
	RecoveryCodesReset: nil,

	OIDCLinked:   {f("provider")},
	OIDCUnlinked: {f("provider")},

	TokenCreated:   {f("grants"), f("expires_at")},
	TokenRevoked:   nil,
	TokenWriteUsed: {f("method"), f("route")},

	MCPToolCalled: {f("tool"), f("outcome", mcpOutcomes...)},

	CorrectionRecorded: {f("kind", correctionKinds...)},
	CorrectionReversed: {f("kind", correctionKinds...)},

	ReportingTimezoneChanged: {f("from"), f("to")},

	CommitmentCreated: {f("kind"), f("currency")},
	CommitmentUpdated: {f("fields")},
	CommitmentDeleted: {f("kind")},

	OccurrencesRegenerated: {f("effective_from"), f("created"), f("updated"), f("removed"), f("kept_paid")},
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
