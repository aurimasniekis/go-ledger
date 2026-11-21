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
}

func (f OccurrenceFilter) query() url.Values {
	q := url.Values{}
	f.apply(q)
	setIf(q, "month", f.Month)
	addAll(q, "commitment_id", f.CommitmentIDs)
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
