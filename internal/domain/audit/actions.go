package audit

import "time"

// Action names for the events the audit log records. Names are stable: they
// are read by the audit API and by people. Each is registered in [Definitions]
// with the detail fields it may carry.
const (
	SignInSucceeded Action = "auth.signin_succeeded" // Details: method, recovery_codes_remaining (method recovery_code), device (known)
	SignInFailed    Action = "auth.signin_failed"    // Details: method, reason, device (known)

	// Sessions and credentials (A2, ADR-0026). The target of a session event
	// is the session; of a password or re-authentication event, the user.
	SessionEnded           Action = "auth.session_ended"           // Details: reason (signed_out, terminated)
	PasswordChanged        Action = "auth.password_changed"        // Details: by (self, administrator, host), sessions_ended
	Reauthenticated        Action = "auth.reauthenticated"         // Details: method
	ReauthenticationFailed Action = "auth.reauthentication_failed" // Details: method, reason, device (known)
	// A one-time recovery sign-in issued from the host (break-glass, A6,
	// ADR-0033); its use is auth.signin_succeeded with method
	// recovery_sign_in. The target is the user.
	RecoverySignInIssued Action = "auth.recovery_sign_in_issued" // Details: by (host), expires_at

	// Second factors (A3, ADR-0030). The target is the user.
	TwoFactorEnabled   Action = "two_factor.enabled"                    // Details: method, replaced (true, false), sessions_ended, verified_with
	TwoFactorDisabled  Action = "two_factor.disabled"                   // Details: method, by (self, administrator, host), verified_with
	RecoveryCodesReset Action = "two_factor.recovery_codes_regenerated" // Details: count, verified_with

	// Single-sign-on links (A6, ADR-0033). The target is the user.
	OIDCLinked   Action = "oidc.linked"   // Details: provider, by (self, provisioning)
	OIDCUnlinked Action = "oidc.unlinked" // Details: provider, by (self, host)

	TokenCreated   Action = "token.created"    // Details: grants, root, expires_at, parent_token_id (created by a token; never the token)
	TokenRevoked   Action = "token.revoked"    // the actor is the owner, an administrator or a token with tokens:write
	TokenWriteUsed Action = "token.write_used" // Details: method, route (the route pattern, never the URL), interactive_steps (skipped)

	MCPToolCalled Action = "mcp.tool_called" // state-changing MCP call. Details: tool, outcome

	CorrectionRecorded Action = "correction.recorded" // Details: kind
	CorrectionReversed Action = "correction.reversed" // Details: kind

	ReportingTimezoneChanged Action = "settings.reporting_timezone_changed" // Details: from, to

	// Payment matches (X1, ADR-0035). The target is the match. Details:
	// occurrence_id, transaction_id, origin, from (the state before; none
	// for a new match), currency. Never the amount, a reason or bank text.
	PaymentMatchSuggested Action = "payment_match.suggested"
	PaymentMatchConfirmed Action = "payment_match.confirmed"
	PaymentMatchRejected  Action = "payment_match.rejected"
	PaymentMatchUnmatched Action = "payment_match.unmatched"
	PaymentMatchCorrected Action = "payment_match.corrected"
	PaymentMatchReversed  Action = "payment_match.reversed"
)

// Fixed value sets for detail fields that are not identifiers.
var (
	signInMethods = []string{"password", "totp", "recovery_code", "oidc", "recovery_sign_in"}
	// Reasons never say which part was wrong beyond the stage: a failed
	// password and an unknown username are both invalid_credentials.
	// identity_not_linked: an OIDC identity linked to no account (and no
	// provisioning); identity_mismatch: an OIDC re-authentication returned
	// another identity than the linked one.
	signInReasons = []string{"invalid_credentials", "invalid_second_factor", "account_disabled", "locked_out", "identity_not_linked", "identity_mismatch"}
	mcpOutcomes   = []string{"succeeded", "failed", "denied"}
	// A session ended by its own user signing out, or terminated from
	// another session of the same user. Sessions ended by a password change
	// are counted in auth.password_changed instead.
	sessionEndReasons = []string{"signed_out", "terminated"}
	passwordChangedBy = []string{"self", "administrator", "host"}
	correctionKinds   = []string{"match", "unmatch", "payment_status", "categorisation", "other"}
	overrideStatuses  = []string{"paid", "unpaid"}
	overrideOrNone    = []string{"none", "paid", "unpaid"}
	matchOrigins      = []string{"auto_rule", "model_suggestion", "user"}
	matchFromStates   = []string{"none", "suggested", "confirmed", "rejected", "reversed"}
)

func matchFields() []Field {
	return []Field{f("occurrence_id"), f("transaction_id"), f("origin", matchOrigins...), f("from", matchFromStates...), f("currency")}
}

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

	// device=known: the attempt came from a browser with a valid device
	// cookie and counted against it, not the username (decision D23-1b).
	SignInSucceeded: {f("method", signInMethods...), f("recovery_codes_remaining"), f("device", "known")},
	SignInFailed:    {f("method", signInMethods...), f("reason", signInReasons...), f("device", "known")},

	SessionEnded:           {f("reason", sessionEndReasons...)},
	PasswordChanged:        {f("by", passwordChangedBy...), f("sessions_ended")},
	Reauthenticated:        {f("method", signInMethods...)},
	ReauthenticationFailed: {f("method", signInMethods...), f("reason", signInReasons...), f("device", "known")},
	RecoverySignInIssued:   {f("by", "host"), f("expires_at")},

	// verified_with: the current second factor the user proved for the change
	// (decision D26-1a); absent when TOTP was not enabled before, or from the host.
	TwoFactorEnabled:   {f("method", "totp"), f("replaced", "true", "false"), f("sessions_ended"), f("verified_with", "totp", "recovery_code")},
	TwoFactorDisabled:  {f("method", "totp"), f("by", passwordChangedBy...), f("verified_with", "totp", "recovery_code")},
	RecoveryCodesReset: {f("count"), f("verified_with", "totp", "recovery_code")},

	OIDCLinked:   {f("provider"), f("by", "self", "provisioning")},
	OIDCUnlinked: {f("provider"), f("by", "self", "host")},

	// parent_token_id: the root token of the root account that created this
	// token (decision D25); absent for a session or the host.
	TokenCreated: {f("grants"), f("root", "true", "false"), f("expires_at"), f("parent_token_id")},
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

	PaymentMatchSuggested: matchFields(),
	PaymentMatchConfirmed: matchFields(),
	PaymentMatchRejected:  matchFields(),
	PaymentMatchUnmatched: matchFields(),
	PaymentMatchCorrected: matchFields(),
	PaymentMatchReversed:  matchFields(),
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
