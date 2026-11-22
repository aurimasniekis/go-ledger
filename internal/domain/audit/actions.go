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
	PasswordChanged        Action = "auth.password_changed"        // Details: by (self, administrator, host), sessions_ended, device_cookies_revoked
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
	// PaymentMatchUnconfirmed is a confirmation taken back by the person who
	// made it, inside the undo window: the match is a suggestion again
	// (ADR-0045).
	PaymentMatchUnconfirmed Action = "payment_match.unconfirmed"

	// Merchant decisions (E1, ADR-0051). The target is the bank transaction
	// (target type "transaction"). Details: from (the decision before: none,
	// inherited from a superseded pending transaction, confirmed, corrected or
	// rejected) and via. Never the merchant name: it is
	// free text the person typed or a source returned.
	MerchantConfirmed     Action = "merchant.confirmed"
	MerchantCorrected     Action = "merchant.corrected"
	MerchantRejected      Action = "merchant.rejected"
	MerchantReviewCleared Action = "merchant.review_cleared"

	// Merchant records (task M1, ADR-0058, D-MER 1a). The target is the
	// merchant (target type "merchant", its id: an identifier, never a name).
	// Details: via; origin (user or decision) when created; fields when
	// updated; for a merge, merged_from (the merchant merged away) and the
	// counts of aliases, decisions, categories and labels moved and of
	// assignments kept. Never a name or an alias: they derive from bank text.
	MerchantCreated      Action = "merchant.created"
	MerchantUpdated      Action = "merchant.updated"
	MerchantArchived     Action = "merchant.archived"
	MerchantUnarchived   Action = "merchant.unarchived"
	MerchantDeleted      Action = "merchant.deleted"
	MerchantAliasAdded   Action = "merchant.alias_added"
	MerchantAliasRemoved Action = "merchant.alias_removed"
	MerchantMerged       Action = "merchant.merged"

	// Organisation (O1, ADR-0053). A definition event targets the category,
	// label or bucket (target type "category", "label" or "bucket"); details:
	// kind, via and, as the action says, fields (the members changed, never
	// their values: names are free text) or reassigned (how many assignments
	// moved to the replacement). An assignment event targets the object
	// (target type "transaction", "commitment", "account" or "merchant"; a
	// merchant by its record's id since ADR-0058); details: kind,
	// change, origin, from (the origin of what it replaced, or none), via. An
	// automatic run records one event per object it changed.
	OrganisationDefinitionCreated    Action = "organisation.definition_created"
	OrganisationDefinitionUpdated    Action = "organisation.definition_updated"
	OrganisationDefinitionArchived   Action = "organisation.definition_archived"
	OrganisationDefinitionUnarchived Action = "organisation.definition_unarchived"
	OrganisationDefinitionDeleted    Action = "organisation.definition_deleted"
	OrganisationAssignmentChanged    Action = "organisation.assignment_changed"
	OrganisationAutomaticApplied     Action = "organisation.automatic_applied"

	// Bank connections (B4, ADR-0040). The target is the connection. Details:
	// provider and application (names only: never a session, code, account or
	// institution), reconnect (true when the consent renewed an existing
	// connection) and, for a refresh on request, the run's outcome.
	BankConnectionStarted          Action = "bank_connection.started"
	BankConnectionReconnectStarted Action = "bank_connection.reconnect_started"
	BankConnectionConnected        Action = "bank_connection.connected"
	BankConnectionDisconnected     Action = "bank_connection.disconnected"
	BankRefreshRequested           Action = "bank_connection.refresh_requested"
	// A reconnect replaced the provider session and the old one could not be
	// ended (it may still be live at the bank). Details: provider,
	// application and the error kind, never a session reference.
	BankSessionEndFailed Action = "bank_connection.session_end_failed"
	// A person asked for an automatic matching run through the API (X2,
	// ADR-0039). The matches it wrote are audited one by one as
	// payment_match.* with the system as actor. The target is the run
	// (target ID "run"). Details: the counts.
	// A refresh that failed with reconnect-required (or similar) after the
	// connection was re-authorised or disconnected left the connection as it
	// is. Details: provider, application, kind (the failure's), status (the
	// status it would have set).
	BankStatusUpdateSkipped Action = "bank_connection.status_update_skipped"
	PaymentMatchingRun      Action = "payment_matching.run" // Details: confirmed, suggested, dropped, skipped
	// A person asked for an exchange-rate refresh through the API (FX2c,
	// ADR-0048). The target is the refresh (target type "rates", ID "refresh").
	// Details: the outcome, the number of currencies refreshed and, on failure,
	// the error kind and code: never a URL or a provider message.
	RatesRefreshRequested Action = "rates.refresh_requested"

	// Consent to send data to a decision provider for one purpose (L1,
	// ADR-0060). The target is the consent (target type "decision_consent").
	// Details: provider, purpose and the consented field names. Never the consent text or any field
	// value.
	DecisionConsentGranted Action = "decision_consent.granted"
	DecisionConsentRevoked Action = "decision_consent.revoked"
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
	// "requested" is a stdio call: the server saw the write and the tool
	// name the client asserted, not the outcome (the route's own answer).
	mcpOutcomes = []string{"succeeded", "failed", "denied", "requested"}
	// A session ended by its own user signing out, or terminated from
	// another session of the same user. Sessions ended by a password change
	// are counted in auth.password_changed instead.
	sessionEndReasons = []string{"signed_out", "terminated"}
	passwordChangedBy = []string{"self", "administrator", "host"}
	correctionKinds   = []string{"match", "unmatch", "payment_status", "categorisation", "other"}
	overrideStatuses  = []string{"paid", "unpaid"}
	overrideOrNone    = []string{"none", "paid", "unpaid"}
	refreshOutcomes   = []string{"running", "succeeded", "partial", "failed", "interrupted"}
	matchOrigins      = []string{"auto_rule", "model_suggestion", "user"}
	matchFromStates   = []string{"none", "suggested", "confirmed", "rejected", "reversed"}
	matchVias         = []string{string(ViaSession), string(ViaToken), string(ViaMCP), string(ViaSystem)}
	// Decision providers and purposes (ADR-0060); the decisions package
	// cannot be imported here, its test checks the lists agree.
	decisionProviders = []string{"openrouter", "openai", "anthropic", "typesafe"}
	decisionPurposes  = []string{"categorisation", "label_suggestion", "alerts", "payment_matching"}
)

func deferralFields(extra ...Field) []Field {
	return append([]Field{f("occurrence_id"), f("plan_type", "personal", "creditor_agreed", "documented_grace"), f("planned_date"), f("currency"), f("via", matchVias...)}, extra...)
}

func matchFields() []Field {
	return []Field{f("occurrence_id"), f("transaction_id"), f("origin", matchOrigins...), f("from", matchFromStates...), f("currency"), f("via", matchVias...)}
}

func definitionFields(extra ...Field) []Field {
	return append([]Field{f("kind", "category", "label", "bucket"), f("via", matchVias...)}, extra...)
}

func merchantFields() []Field {
	return []Field{f("from", "none", "inherited", "confirmed", "corrected", "rejected"), f("via", matchVias...), f("merchant_id")}
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
	PasswordChanged:        {f("by", passwordChangedBy...), f("sessions_ended"), f("device_cookies_revoked")},
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
	// cascaded_from: the parent token whose revocation revoked this one (D25-1).
	TokenRevoked: {f("cascaded_from")},
	// interactive_steps records that the request skipped two-factor and
	// recent re-authentication, which API tokens do by design (ADR-0029).
	TokenWriteUsed: {f("method"), f("route"), f("interactive_steps", "skipped")},

	MCPToolCalled: {f("tool"), f("outcome", mcpOutcomes...)},

	CorrectionRecorded: {f("kind", correctionKinds...)},
	CorrectionReversed: {f("kind", correctionKinds...)},

	ReportingTimezoneChanged: {f("from"), f("to")},

	CommitmentCreated: {f("kind"), f("currency")},
	CommitmentUpdated: {f("fields")},
	CommitmentDeleted: {f("kind"), f("unpaid_occurrences_removed"), f("cancelled_referenced"), f("archived")},

	OccurrencesRegenerated: {f("effective_from"), f("created"), f("updated"), f("removed"), f("kept_paid"), f("kept_history"), f("removed_out_of_range"), f("kept_override"), f("cancelled_referenced")},

	ManualPaymentRecorded:          {f("payment_id"), f("currency")},
	ManualPaymentDeleted:           {f("payment_id"), f("amount"), f("currency")},
	ManualPaymentReplaced:          {f("payment_id"), f("match_id"), f("amount"), f("currency"), f("via", matchVias...)},
	ManualPaymentRestored:          {f("payment_id"), f("match_id"), f("amount"), f("currency"), f("via", matchVias...), f("by", "unconfirmed", "unmatched")},
	OccurrenceStatusOverridden:     {f("status", overrideStatuses...), f("previous", overrideOrNone...)},
	OccurrenceStatusOverrideUndone: {f("undone", overrideStatuses...), f("restored", overrideOrNone...)},

	PaymentMatchSuggested:   matchFields(),
	PaymentMatchConfirmed:   matchFields(),
	PaymentMatchRejected:    matchFields(),
	PaymentMatchUnmatched:   matchFields(),
	PaymentMatchCorrected:   matchFields(),
	PaymentMatchReversed:    matchFields(),
	PaymentMatchUnconfirmed: matchFields(),

	MerchantConfirmed:                merchantFields(),
	MerchantCorrected:                merchantFields(),
	MerchantRejected:                 merchantFields(),
	MerchantReviewCleared:            merchantFields(),
	MerchantCreated:                  {f("via", matchVias...), f("origin", "user", "decision")},
	MerchantUpdated:                  {f("via", matchVias...), f("fields")},
	MerchantArchived:                 {f("via", matchVias...)},
	MerchantUnarchived:               {f("via", matchVias...)},
	MerchantDeleted:                  {f("via", matchVias...)},
	MerchantAliasAdded:               {f("via", matchVias...)},
	MerchantAliasRemoved:             {f("via", matchVias...)},
	MerchantMerged:                   {f("via", matchVias...), f("merged_from"), f("aliases"), f("decisions"), f("categories"), f("labels"), f("kept")},
	OrganisationDefinitionCreated:    definitionFields(),
	OrganisationDefinitionUpdated:    definitionFields(f("fields")),
	OrganisationDefinitionArchived:   definitionFields(),
	OrganisationDefinitionUnarchived: definitionFields(),
	OrganisationDefinitionDeleted:    definitionFields(f("reassigned")),
	OrganisationAssignmentChanged: {f("kind", "category", "label", "bucket"), f("change", "set", "none", "reset", "added", "removed"),
		f("origin", "user", "rule", "model"), f("from", "none", "user", "rule", "model"), f("via", matchVias...)},
	OrganisationAutomaticApplied: {f("origin", "rule", "model"), f("category", "true", "false"), f("labels"), f("via", matchVias...)},
	DeferralPlanCreated:          deferralFields(),
	DeferralPlanRevised:          deferralFields(f("fields")),
	DeferralPlanUndone:           deferralFields(),
	DeferralAdjustmentAdded:      deferralFields(f("adjustment_id"), f("kind", "fee", "interest", "other")),
	DeferralAdjustmentRemoved:    deferralFields(f("adjustment_id"), f("kind", "fee", "interest", "other")),
	DeferralAdjustmentPaid:       deferralFields(f("adjustment_id"), f("kind", "fee", "interest", "other")),
	DeferralAdjustmentUnpaid:     deferralFields(f("adjustment_id"), f("kind", "fee", "interest", "other")),

	BankConnectionStarted:          {f("provider"), f("application")},
	BankConnectionReconnectStarted: {f("provider"), f("application")},
	BankConnectionConnected:        {f("provider"), f("application"), f("reconnect", "true", "false")},
	BankConnectionDisconnected:     {f("provider"), f("application")},
	BankRefreshRequested:           {f("provider"), f("application"), f("outcome", refreshOutcomes...)},
	BankSessionEndFailed:           {f("provider"), f("application"), f("kind", "reconnect_required", "rate_limited", "retryable", "permanent", "canceled")},
	BankStatusUpdateSkipped:        {f("provider"), f("application"), f("kind", "reconnect_required", "rate_limited", "retryable", "permanent", "canceled"), f("status", "reconnect_required", "failed")},
	PaymentMatchingRun:             {f("confirmed"), f("suggested"), f("dropped"), f("skipped")},
	DecisionConsentGranted:         {f("provider", decisionProviders...), f("purpose", decisionPurposes...), f("fields"), f("via", matchVias...)},
	DecisionConsentRevoked:         {f("provider", decisionProviders...), f("purpose", decisionPurposes...), f("fields"), f("via", matchVias...)},
	RatesRefreshRequested:          {f("outcome", "succeeded", "partial", "failed", "cancelled"), f("refreshed"), f("error_kind", "unavailable", "rate_limited", "permanent", "internal"), f("error_code")},
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
