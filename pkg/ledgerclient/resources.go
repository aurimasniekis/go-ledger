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
