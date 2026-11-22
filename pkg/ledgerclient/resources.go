package ledgerclient

import (
	"context"
	"iter"
	"net/url"
)

func addAll(q url.Values, key string, vals []string) {
	for _, v := range vals {
		q.Add(key, v)
	}
}

func setIf(q url.Values, key, val string) {
	if val != "" {
		q.Set(key, val)
	}
}

// Me returns who the token acts as and its effective grants.
func (c *Client) Me(ctx context.Context) (*Principal, error) {
	var p Principal
	if err := c.do(ctx, request{op: opGetMe}, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Grants returns the grant vocabulary and the roles.
func (c *Client) Grants(ctx context.Context) (*GrantVocabulary, error) {
	var g GrantVocabulary
	if err := c.do(ctx, request{op: opGetGrants}, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// UserFilter filters ListUsers. Disabled is "true", "false" or "" (both).
type UserFilter struct {
	ListOptions
	Roles          []string
	Disabled       string
	UsernamePrefix string
}

func (f UserFilter) query() url.Values {
	q := url.Values{}
	f.apply(q)
	addAll(q, "role", f.Roles)
	setIf(q, "disabled", f.Disabled)
	setIf(q, "username[prefix]", f.UsernamePrefix)
	return q
}

// ListUsers returns one page of users.
func (c *Client) ListUsers(ctx context.Context, f UserFilter) (Page[User], error) {
	var p Page[User]
	err := c.do(ctx, request{op: opListUsers, query: f.query()}, &p)
	return p, err
}

// AllUsers walks every user matching f (its Cursor is ignored).
func (c *Client) AllUsers(ctx context.Context, f UserFilter) iter.Seq2[User, error] {
	return Walk(ctx, func(ctx context.Context, cur string) (Page[User], error) {
		f.Cursor = cur
		return c.ListUsers(ctx, f)
	})
}

// GetUser returns one user.
func (c *Client) GetUser(ctx context.Context, id string) (*User, error) {
	var u User
	if err := c.do(ctx, request{op: opGetUser, pathID: id}, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser creates an enabled user.
func (c *Client) CreateUser(ctx context.Context, in NewUser) (*User, error) {
	var u User
	if err := c.do(ctx, request{op: opCreateUser, body: in}, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// UpdateUser applies a merge patch to the user whose current version is
// version (sent as If-Match). Users are never deleted: patch "disabled".
func (c *Client) UpdateUser(ctx context.Context, id string, version int64, patch MergePatch) (*User, error) {
	var u User
	if err := c.do(ctx, request{op: opUpdateUser, pathID: id, version: version, body: patch}, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// CommitmentFilter filters ListCommitments.
type CommitmentFilter struct {
	ListOptions
	Kinds      []string
	Priorities []string
	Currencies []string
}

func (f CommitmentFilter) query() url.Values {
	q := url.Values{}
	f.apply(q)
	addAll(q, "kind", f.Kinds)
	addAll(q, "priority", f.Priorities)
	addAll(q, "currency", f.Currencies)
	return q
}

// ListCommitments returns one page of commitment definitions.
func (c *Client) ListCommitments(ctx context.Context, f CommitmentFilter) (Page[Commitment], error) {
	var p Page[Commitment]
	err := c.do(ctx, request{op: opListCommit, query: f.query()}, &p)
	return p, err
}

// AllCommitments walks every commitment matching f (its Cursor is ignored).
func (c *Client) AllCommitments(ctx context.Context, f CommitmentFilter) iter.Seq2[Commitment, error] {
	return Walk(ctx, func(ctx context.Context, cur string) (Page[Commitment], error) {
		f.Cursor = cur
		return c.ListCommitments(ctx, f)
	})
}

// GetCommitment returns one commitment definition.
func (c *Client) GetCommitment(ctx context.Context, id string) (*Commitment, error) {
	var m Commitment
	if err := c.do(ctx, request{op: opGetCommit, pathID: id}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// CreateCommitment records a commitment definition. A validation failure is an
// *APIError listing every invalid field (FieldErrors).
func (c *Client) CreateCommitment(ctx context.Context, in NewCommitment) (*Commitment, error) {
	var m Commitment
	if err := c.do(ctx, request{op: opCreateCommit, body: in}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// UpdateCommitment applies a merge patch to the commitment whose current
// version is version. The patch may hold "effective_from" (YYYY-MM-DD), which
// is not a member of the commitment but says from when changed terms reach its
// occurrences. When switching a schedule between monthly and annual, send the
// whole schedule with explicit nils for the members that no longer apply.
func (c *Client) UpdateCommitment(ctx context.Context, id string, version int64, patch MergePatch) (*Commitment, error) {
	var m Commitment
	if err := c.do(ctx, request{op: opUpdateCommit, pathID: id, version: version, body: patch}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// DeleteCommitment deletes the commitment whose current version is version.
func (c *Client) DeleteCommitment(ctx context.Context, id string, version int64) error {
	return c.do(ctx, request{op: opDeleteCommit, pathID: id, version: version}, nil)
}

// OccurrenceFilter filters ListOccurrences. Month (YYYY-MM) is required.
type OccurrenceFilter struct {
	ListOptions
	Month         string
	CommitmentIDs []string
	// IncludeCancelled also lists cancelled occurrences, which are left out by default.
	IncludeCancelled bool
}

func (f OccurrenceFilter) query() url.Values {
	q := url.Values{}
	f.apply(q)
	setIf(q, "month", f.Month)
	addAll(q, "commitment_id", f.CommitmentIDs)
	if f.IncludeCancelled {
		q.Set("include_cancelled", "true")
	}
	return q
}

// ListOccurrences returns one page of a reporting month's occurrences.
func (c *Client) ListOccurrences(ctx context.Context, f OccurrenceFilter) (Page[Occurrence], error) {
	var p Page[Occurrence]
	err := c.do(ctx, request{op: opListOccur, query: f.query()}, &p)
	return p, err
}

// AllOccurrences walks every occurrence of the month (Cursor is ignored).
func (c *Client) AllOccurrences(ctx context.Context, f OccurrenceFilter) iter.Seq2[Occurrence, error] {
	return Walk(ctx, func(ctx context.Context, cur string) (Page[Occurrence], error) {
		f.Cursor = cur
		return c.ListOccurrences(ctx, f)
	})
}

// GetOccurrence returns one occurrence.
func (c *Client) GetOccurrence(ctx context.Context, id string) (*Occurrence, error) {
	var o Occurrence
	if err := c.do(ctx, request{op: opGetOccur, pathID: id}, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// AuditFilter filters ListAudit. Since and Until are RFC 3339 times
// (at[gte], at[lt]).
type AuditFilter struct {
	ListOptions
	Actions      []string
	ActionPrefix string
	ActorKinds   []string
	ActorUserID  string
	ActorTokenID string
	TargetType   string
	TargetID     string
	Since, Until string
}

func (f AuditFilter) query() url.Values {
	q := url.Values{}
	f.apply(q)
	addAll(q, "action", f.Actions)
	setIf(q, "action[prefix]", f.ActionPrefix)
	addAll(q, "actor_kind", f.ActorKinds)
	setIf(q, "actor_user_id", f.ActorUserID)
	setIf(q, "actor_token_id", f.ActorTokenID)
	setIf(q, "target_type", f.TargetType)
	setIf(q, "target_id", f.TargetID)
	setIf(q, "at[gte]", f.Since)
	setIf(q, "at[lt]", f.Until)
	return q
}

// ListAudit returns one page of audit entries, oldest first.
func (c *Client) ListAudit(ctx context.Context, f AuditFilter) (Page[AuditEntry], error) {
	var p Page[AuditEntry]
	err := c.do(ctx, request{op: opListAudit, query: f.query()}, &p)
	return p, err
}

// AllAudit walks every audit entry matching f (its Cursor is ignored).
func (c *Client) AllAudit(ctx context.Context, f AuditFilter) iter.Seq2[AuditEntry, error] {
	return Walk(ctx, func(ctx context.Context, cur string) (Page[AuditEntry], error) {
		f.Cursor = cur
		return c.ListAudit(ctx, f)
	})
}

// ManualPaymentFilter pages ListManualPayments.
type ManualPaymentFilter struct{ ListOptions }

// ListManualPayments returns one page of an occurrence's manual payments,
// ordered by paid_on, then ID.
func (c *Client) ListManualPayments(ctx context.Context, occurrenceID string, f ManualPaymentFilter) (Page[ManualPayment], error) {
	q := url.Values{}
	f.apply(q)
	var p Page[ManualPayment]
	err := c.do(ctx, request{op: opListManualPayments, pathID: occurrenceID, query: q}, &p)
	return p, err
}

// AllManualPayments walks every manual payment of an occurrence.
func (c *Client) AllManualPayments(ctx context.Context, occurrenceID string, f ManualPaymentFilter) iter.Seq2[ManualPayment, error] {
	return Walk(ctx, func(ctx context.Context, cur string) (Page[ManualPayment], error) {
		f.Cursor = cur
		return c.ListManualPayments(ctx, occurrenceID, f)
	})
}

// RecordManualPayment records a payment made by hand. It is payment evidence:
// the occurrence's status is derived again and its version increases. The
// amount must be positive and in the occurrence's currency.
func (c *Client) RecordManualPayment(ctx context.Context, occurrenceID string, in NewManualPayment) (*ManualPayment, error) {
	var m ManualPayment
	if err := c.do(ctx, request{op: opRecordManualPayment, pathID: occurrenceID, body: in}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// GetManualPayment returns one manual payment.
func (c *Client) GetManualPayment(ctx context.Context, occurrenceID, paymentID string) (*ManualPayment, error) {
	var m ManualPayment
	if err := c.do(ctx, request{op: opGetManualPayment, pathID: occurrenceID, pathID2: paymentID}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// DeleteManualPayment deletes the payment whose current version is version
// (the payment's, sent as If-Match; not the occurrence's).
func (c *Client) DeleteManualPayment(ctx context.Context, occurrenceID, paymentID string, version int64) error {
	return c.do(ctx, request{op: opDeleteManualPayment, pathID: occurrenceID, pathID2: paymentID, version: version}, nil)
}

// SetStatusOverride overrides the occurrence's derived status with a reason.
// version is the occurrence's current version (If-Match). A "paid" override
// needs payment evidence (409 payment_evidence_required otherwise).
func (c *Client) SetStatusOverride(ctx context.Context, occurrenceID string, version int64, in StatusOverride) (*Occurrence, error) {
	var o Occurrence
	if err := c.do(ctx, request{op: opSetOverride, pathID: occurrenceID, version: version, body: in}, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// UndoStatusOverride undoes the last override change of the occurrence whose
// current version is version (404 when none is active).
func (c *Client) UndoStatusOverride(ctx context.Context, occurrenceID string, version int64) (*Occurrence, error) {
	var o Occurrence
	if err := c.do(ctx, request{op: opUndoOverride, pathID: occurrenceID, version: version}, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// PaymentMatchFilter filters ListPaymentMatches; each list matches any of
// its values.
type PaymentMatchFilter struct {
	ListOptions
	TransactionIDs []string
	OccurrenceIDs  []string
	States         []string
	Origins        []string
}

func (f PaymentMatchFilter) query() url.Values {
	q := url.Values{}
	f.apply(q)
	addAll(q, "transaction_id", f.TransactionIDs)
	addAll(q, "occurrence_id", f.OccurrenceIDs)
	addAll(q, "state", f.States)
	addAll(q, "origin", f.Origins)
	return q
}

// ListPaymentMatches returns one page of payment matches, oldest first, each
// with its bank transaction and history.
func (c *Client) ListPaymentMatches(ctx context.Context, f PaymentMatchFilter) (Page[PaymentMatch], error) {
	var p Page[PaymentMatch]
	err := c.do(ctx, request{op: opListMatches, query: f.query()}, &p)
	return p, err
}

// AllPaymentMatches walks every payment match matching f (its Cursor is
// ignored).
func (c *Client) AllPaymentMatches(ctx context.Context, f PaymentMatchFilter) iter.Seq2[PaymentMatch, error] {
	return Walk(ctx, func(ctx context.Context, cur string) (Page[PaymentMatch], error) {
		f.Cursor = cur
		return c.ListPaymentMatches(ctx, f)
	})
}

// CreatePaymentMatch records the user's confirmed match of part or all of a
// booked bank transaction to an occurrence.
func (c *Client) CreatePaymentMatch(ctx context.Context, in NewPaymentMatch) (*PaymentMatch, error) {
	var m PaymentMatch
	if err := c.do(ctx, request{op: opCreateMatch, body: in}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// GetPaymentMatch returns one payment match.
func (c *Client) GetPaymentMatch(ctx context.Context, id string) (*PaymentMatch, error) {
	var m PaymentMatch
	if err := c.do(ctx, request{op: opGetMatch, pathID: id}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (c *Client) matchAction(ctx context.Context, op operation, id string, version int64, body any) (*PaymentMatch, error) {
	var m PaymentMatch
	if err := c.do(ctx, request{op: op, pathID: id, version: version, body: body}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ConfirmPaymentMatch confirms a suggested match whose current version is
// version.
func (c *Client) ConfirmPaymentMatch(ctx context.Context, id string, version int64, d MatchDecision) (*PaymentMatch, error) {
	return c.matchAction(ctx, opConfirmMatch, id, version, d)
}

// RejectPaymentMatch rejects a suggested match.
func (c *Client) RejectPaymentMatch(ctx context.Context, id string, version int64, reason *string) (*PaymentMatch, error) {
	return c.matchAction(ctx, opRejectMatch, id, version, MatchDecision{Reason: reason})
}

// UnmatchPaymentMatch undoes a confirmed match: the transaction does not pay
// the occurrence.
func (c *Client) UnmatchPaymentMatch(ctx context.Context, id string, version int64, reason *string) (*PaymentMatch, error) {
	return c.matchAction(ctx, opUnmatchMatch, id, version, MatchDecision{Reason: reason})
}

// UnconfirmPaymentMatch takes back a confirmation: the match is a suggestion
// again. Only the person who confirmed it may, and only inside the server's
// undo window (ADR-0045); otherwise the server answers match_undo_not_decider
// or match_undo_window_closed.
func (c *Client) UnconfirmPaymentMatch(ctx context.Context, id string, version int64, reason *string) (*PaymentMatch, error) {
	return c.matchAction(ctx, opUnconfirmMatch, id, version, MatchDecision{Reason: reason})
}

// ConfirmPaymentMatchReplacing confirms a suggested match "as the payment I
// recorded" (ADR-0049): the listed manual payments of its occurrence are
// marked replaced by it and stop counting, so the payment is not counted
// twice. UnconfirmPaymentMatch inside the undo window makes them count again.
func (c *Client) ConfirmPaymentMatchReplacing(ctx context.Context, id string, version int64, in MatchReplacement) (*PaymentMatch, error) {
	return c.matchAction(ctx, opReplaceMatch, id, version, in)
}

// CorrectPaymentMatch changes the allocated amount of a suggested or
// confirmed match.
func (c *Client) CorrectPaymentMatch(ctx context.Context, id string, version int64, in MatchCorrection) (*PaymentMatch, error) {
	return c.matchAction(ctx, opCorrectMatch, id, version, in)
}

// ReversePaymentMatch marks a confirmed match reversed (the payment was
// reversed); the occurrence reopens.
func (c *Client) ReversePaymentMatch(ctx context.Context, id string, version int64, reason *string) (*PaymentMatch, error) {
	return c.matchAction(ctx, opReverseMatch, id, version, MatchDecision{Reason: reason})
}

// RunPaymentMatching runs the automatic payment matching once (needs
// commitments:write). It is idempotent: over unchanged data a second run
// writes nothing. Matches it confirms passed every safeguard; the others are
// suggestions to review (ListPaymentMatches with state suggested).
func (c *Client) RunPaymentMatching(ctx context.Context) (*PaymentMatchingRun, error) {
	var out PaymentMatchingRun
	if err := c.do(ctx, request{op: opRunMatching, body: struct{}{}}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RateFilter filters ListRates. Empty fields match everything; From and Until
// are dates (YYYY-MM-DD) bounding the rate date, both inclusive.
type RateFilter struct {
	ListOptions
	Provider string
	Source   string
	Base     string
	Quote    string
	From     string
	Until    string
}

func (f RateFilter) query() url.Values {
	q := url.Values{}
	f.apply(q)
	setIf(q, "provider", f.Provider)
	setIf(q, "source", f.Source)
	setIf(q, "base", f.Base)
	setIf(q, "quote", f.Quote)
	setIf(q, "date[gte]", f.From)
	setIf(q, "date[lte]", f.Until)
	return q
}

// ListRates returns one page of cached exchange rates (needs rates:read),
// grouped by provider, source and pair with the newest rate date first. It
// reads Ledger's cache and never calls the rate provider.
func (c *Client) ListRates(ctx context.Context, f RateFilter) (Page[CachedRate], error) {
	var p Page[CachedRate]
	err := c.do(ctx, request{op: opListRates, query: f.query()}, &p)
	return p, err
}

// AllRates walks every cached rate matching f (its Cursor is ignored).
func (c *Client) AllRates(ctx context.Context, f RateFilter) iter.Seq2[CachedRate, error] {
	return Walk(ctx, func(ctx context.Context, cur string) (Page[CachedRate], error) {
		f.Cursor = cur
		return c.ListRates(ctx, f)
	})
}

// GetRatesStatus reports whether a rate provider is configured, the freshness
// policy and how the last refresh went (needs rates:read).
func (c *Client) GetRatesStatus(ctx context.Context) (*RatesStatus, error) {
	var out RatesStatus
	if err := c.do(ctx, request{op: opRatesStatus}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RefreshRates asks the rate provider for fresh rates now (needs rates:write,
// an administrator grant). The run is bounded, never overlaps another and is
// limited to one a minute (a 429 rate_limited error). A run that fails is not
// an error: the returned run's Outcome and Error say so and the cache is
// unchanged.
func (c *Client) RefreshRates(ctx context.Context) (*RatesRefreshRun, error) {
	var out RatesRefreshRun
	if err := c.do(ctx, request{op: opRefreshRates, body: struct{}{}}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TokenFilter filters ListTokens (every user's tokens; needs tokens:read).
type TokenFilter struct {
	ListOptions
	OwnerID string
}

func (f TokenFilter) query() url.Values {
	q := url.Values{}
	f.apply(q)
	setIf(q, "owner_id", f.OwnerID)
	return q
}

// ListTokens returns one page of API tokens, including expired and revoked
// ones. It never returns a token's secret.
func (c *Client) ListTokens(ctx context.Context, f TokenFilter) (Page[APIToken], error) {
	var p Page[APIToken]
	err := c.do(ctx, request{op: opListTokens, query: f.query()}, &p)
	return p, err
}

// AllTokens walks every API token matching f (its Cursor is ignored).
func (c *Client) AllTokens(ctx context.Context, f TokenFilter) iter.Seq2[APIToken, error] {
	return Walk(ctx, func(ctx context.Context, cur string) (Page[APIToken], error) {
		f.Cursor = cur
		return c.ListTokens(ctx, f)
	})
}

// GetToken returns any user's API token (needs tokens:read).
func (c *Client) GetToken(ctx context.Context, id string) (*APIToken, error) {
	var t APIToken
	if err := c.do(ctx, request{op: opGetToken, pathID: id}, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateMyToken creates an API token owned by the caller and returns it with
// its secret, shown only in this response. Only a root token of the root
// account (the first user, normally made by the bootstrap command) may call
// it; any other token gets 403 forbidden (decision D25). The new token is
// bounded by the root account's current role; give Grants, or Root for a root
// token (then Warning explains its power).
func (c *Client) CreateMyToken(ctx context.Context, in NewAPIToken) (*CreatedAPIToken, error) {
	var t CreatedAPIToken
	if err := c.do(ctx, request{op: opCreateMyToken, body: in}, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// RevokeToken revokes any user's API token at once (needs tokens:write).
// Revoking a revoked token succeeds and changes nothing.
func (c *Client) RevokeToken(ctx context.Context, id string) error {
	return c.do(ctx, request{op: opRevokeToken, pathID: id}, nil)
}

// ListBankApplications returns the configured provider applications (needs
// connections:read, an administrator grant).
func (c *Client) ListBankApplications(ctx context.Context) ([]BankApplication, error) {
	var out struct {
		Items []BankApplication `json:"items"`
	}
	if err := c.do(ctx, request{op: opListBankApps}, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// ListBankInstitutions asks the provider which banks the named application
// offers in a country (an ISO 3166-1 alpha-2 code in capitals).
func (c *Client) ListBankInstitutions(ctx context.Context, application, country string) ([]BankInstitution, error) {
	var out struct {
		Items []BankInstitution `json:"items"`
	}
	if err := c.do(ctx, request{op: opListBankInstitutions, pathID: application, query: url.Values{"country": {country}}}, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// ListBankConnections returns one page of bank connections with their
// accounts, balances and freshness, oldest first.
func (c *Client) ListBankConnections(ctx context.Context, o ListOptions) (Page[BankConnection], error) {
	var p Page[BankConnection]
	q := url.Values{}
	o.apply(q)
	err := c.do(ctx, request{op: opListBankConns, query: q}, &p)
	return p, err
}

// AllBankConnections walks every bank connection (o.Cursor is ignored).
func (c *Client) AllBankConnections(ctx context.Context, o ListOptions) iter.Seq2[BankConnection, error] {
	return Walk(ctx, func(ctx context.Context, cur string) (Page[BankConnection], error) {
		o.Cursor = cur
		return c.ListBankConnections(ctx, o)
	})
}

// GetBankConnection returns one connection with its accounts and freshness.
func (c *Client) GetBankConnection(ctx context.Context, id string) (*BankConnection, error) {
	var b BankConnection
	if err := c.do(ctx, request{op: opGetBankConn, pathID: id}, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// StartBankConnection records a pending connection and returns the bank's
// consent page in AuthorizationURL. The person opens it in a browser; Ledger
// never receives bank credentials, and the connection becomes active only when
// the bank redirects to Ledger's callback page (this client does not complete
// consents). Needs connections:write. Not idempotent: calling it twice starts
// two connections.
func (c *Client) StartBankConnection(ctx context.Context, in NewBankConnection) (*BankConsentStart, error) {
	var s BankConsentStart
	if err := c.do(ctx, request{op: opStartBankConn, body: in}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ReconnectBankConnection starts a new consent for an existing connection (one
// that needs it, or to renew early) and returns the consent page. The
// connection stays as it was until the person approves. validForDays 0 asks for
// the bank's own validity.
func (c *Client) ReconnectBankConnection(ctx context.Context, id string, validForDays int) (*BankConsentStart, error) {
	var s BankConsentStart
	body := struct {
		ValidForDays int `json:"valid_for_days,omitempty"`
	}{validForDays}
	if err := c.do(ctx, request{op: opReconnectBankConn, pathID: id, body: body}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// RefreshBankConnection fetches the connection's data now. A refresh that
// failed is still a successful call: read LastRefreshRun.Error and the
// accounts' freshness (stored data is never changed by a failure). When
// RefreshRunning is true the refresh outlived the wait and continues in the
// background: read the connection again later. Needs connections:write.
func (c *Client) RefreshBankConnection(ctx context.Context, id string) (*BankConnection, error) {
	var b BankConnection
	if err := c.do(ctx, request{op: opRefreshBankConn, pathID: id, body: struct{}{}}, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// DisconnectBankConnection ends the provider session of the connection whose
// current version is version. Stored accounts, balances and transactions stay
// as history. Nothing changes if the provider cannot be reached.
func (c *Client) DisconnectBankConnection(ctx context.Context, id string, version int64) (*BankConnection, error) {
	var b BankConnection
	if err := c.do(ctx, request{op: opDisconnectBankConn, pathID: id, version: version, body: struct{}{}}, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// DeferralPlanFilter filters ListDeferralPlans; each list matches any of its
// values. PlannedMonth (YYYY-MM) keeps plans planned in that month.
type DeferralPlanFilter struct {
	ListOptions
	OccurrenceIDs []string
	CommitmentIDs []string
	States        []string
	PlannedMonth  string
}

func (f DeferralPlanFilter) query() url.Values {
	q := url.Values{}
	f.apply(q)
	addAll(q, "occurrence_id", f.OccurrenceIDs)
	addAll(q, "commitment_id", f.CommitmentIDs)
	addAll(q, "state", f.States)
	if f.PlannedMonth != "" {
		q.Set("planned_month", f.PlannedMonth)
	}
	return q
}

// ListDeferralPlans returns one page of deferral plans, oldest first.
func (c *Client) ListDeferralPlans(ctx context.Context, f DeferralPlanFilter) (Page[DeferralPlan], error) {
	var p Page[DeferralPlan]
	err := c.do(ctx, request{op: opListDeferrals, query: f.query()}, &p)
	return p, err
}

// AllDeferralPlans walks every deferral plan matching f (its Cursor is
// ignored).
func (c *Client) AllDeferralPlans(ctx context.Context, f DeferralPlanFilter) iter.Seq2[DeferralPlan, error] {
	return Walk(ctx, func(ctx context.Context, cur string) (Page[DeferralPlan], error) {
		f.Cursor = cur
		return c.ListDeferralPlans(ctx, f)
	})
}

func (c *Client) deferral(ctx context.Context, req request) (*DeferralPlan, error) {
	var p DeferralPlan
	if err := c.do(ctx, req, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// CreateDeferralPlan defers an outstanding occurrence. It never marks
// anything paid; the occurrence stays outstanding (and overdue past its due
// date) while its amount is planned in the planned date's month.
func (c *Client) CreateDeferralPlan(ctx context.Context, in NewDeferralPlan) (*DeferralPlan, error) {
	return c.deferral(ctx, request{op: opCreateDeferral, body: in})
}

// GetDeferralPlan returns one deferral plan.
func (c *Client) GetDeferralPlan(ctx context.Context, id string) (*DeferralPlan, error) {
	return c.deferral(ctx, request{op: opGetDeferral, pathID: id})
}

// ReviseDeferralPlan applies a merge patch (plan_type, planned_date, reason)
// to the active plan whose current version is version.
func (c *Client) ReviseDeferralPlan(ctx context.Context, id string, version int64, patch MergePatch) (*DeferralPlan, error) {
	return c.deferral(ctx, request{op: opReviseDeferral, pathID: id, version: version, body: patch})
}

// UndoDeferralPlan ends the active plan whose current version is version;
// the occurrence's amount is planned in its own month again.
func (c *Client) UndoDeferralPlan(ctx context.Context, id string, version int64, in DeferralUndo) (*DeferralPlan, error) {
	return c.deferral(ctx, request{op: opUndoDeferral, pathID: id, version: version, body: in})
}

// ListDeferralPlanHistory returns one page of a plan's history, oldest first.
func (c *Client) ListDeferralPlanHistory(ctx context.Context, id string, o ListOptions) (Page[DeferralPlanEvent], error) {
	q := url.Values{}
	o.apply(q)
	var p Page[DeferralPlanEvent]
	err := c.do(ctx, request{op: opDeferralHistory, pathID: id, query: q}, &p)
	return p, err
}

// AddDeferralAdjustment records an explicit fee or charge on the active plan
// whose current version is version.
func (c *Client) AddDeferralAdjustment(ctx context.Context, id string, version int64, in NewDeferralAdjustment) (*DeferralPlan, error) {
	return c.deferral(ctx, request{op: opAddDeferralAdjust, pathID: id, version: version, body: in})
}

// MarkDeferralAdjustmentPaid records that the person paid an adjustment of the
// active plan whose current version is version. It is not payment evidence for
// the occurrence and is reversible with UnmarkDeferralAdjustmentPaid.
func (c *Client) MarkDeferralAdjustmentPaid(ctx context.Context, id, adjustmentID string, version int64) (*DeferralPlan, error) {
	return c.deferral(ctx, request{op: opMarkAdjustmentPaid, pathID: id, pathID2: adjustmentID, version: version})
}

// UnmarkDeferralAdjustmentPaid clears an adjustment's paid marker: it is
// planned again.
func (c *Client) UnmarkDeferralAdjustmentPaid(ctx context.Context, id, adjustmentID string, version int64) (*DeferralPlan, error) {
	return c.deferral(ctx, request{op: opUnmarkAdjustmentPaid, pathID: id, pathID2: adjustmentID, version: version})
}

// RemoveDeferralAdjustment removes an adjustment of the active plan whose
// current version is version; it stays in the plan as history.
func (c *Client) RemoveDeferralAdjustment(ctx context.Context, id, adjustmentID string, version int64) (*DeferralPlan, error) {
	return c.deferral(ctx, request{op: opRemoveDeferralAdjust, pathID: id, pathID2: adjustmentID, version: version})
}
