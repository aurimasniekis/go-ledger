package ledgerclient

import (
	"context"
	"iter"
	"net/url"
)

// Merchant decisions about a transaction (ADR-0051).
const (
	MerchantConfirmed = "confirmed"
	MerchantCorrected = "corrected"
	MerchantRejected  = "rejected"
)

// MerchantAlias is a name a transaction's merchant name is matched by. Key is
// the name as matched (NFC, white space collapsed, lower case) and names one
// merchant at most.
type MerchantAlias struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Key       string `json:"key"`
	Origin    string `json:"origin"`
	CreatedAt string `json:"created_at"`
}

// MerchantRecord is a merchant (task M1, ADR-0058): a stable id, the name to
// show and its aliases. MergedInto names the merchant it was merged into; a
// merged merchant is archived.
type MerchantRecord struct {
	ID          string          `json:"id"`
	DisplayName string          `json:"display_name"`
	Aliases     []MerchantAlias `json:"aliases"`
	Archived    bool            `json:"archived"`
	ArchivedAt  *string         `json:"archived_at"`
	MergedInto  *string         `json:"merged_into"`
	Version     int64           `json:"version"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

// NewMerchant is the body of a create. The display name is the first alias.
type NewMerchant struct {
	DisplayName string   `json:"display_name"`
	Aliases     []string `json:"aliases,omitempty"`
}

// MerchantFilter filters ListMerchants. Archived is "true" (archived and
// merged merchants too) or "" (active ones only).
type MerchantFilter struct {
	ListOptions
	Archived string
}

func (f MerchantFilter) query() url.Values {
	q := url.Values{}
	f.apply(q)
	setIf(q, "archived", f.Archived)
	return q
}

// MerchantMoved counts what a merge moved to the remaining merchant.
type MerchantMoved struct {
	Aliases     int `json:"aliases"`
	Decisions   int `json:"decisions"`
	Enrichments int `json:"enrichments"`
	Categories  int `json:"categories"`
	Labels      int `json:"labels"`
}

// MerchantMerge is the answer of a merge: the remaining merchant, what moved,
// and how many assignments of the merged merchant stayed where they were
// because the remaining one had its own for the slot.
type MerchantMerge struct {
	Merchant MerchantRecord `json:"merchant"`
	Moved    MerchantMoved  `json:"moved"`
	Kept     int            `json:"kept"`
}

// MerchantEnrichment is what a lookup source said about a transaction's
// merchant, as stored. LogoRef is opaque: neither Ledger nor this client
// fetches it.
type MerchantEnrichment struct {
	Status  string  `json:"status"`
	Name    *string `json:"name"`
	LogoRef *string `json:"logo_ref"`
	Source  string  `json:"source"`
	// ConfidencePermille is 0 to 1000, an exact integer; nil unless identified.
	ConfidencePermille *int     `json:"confidence_permille"`
	Candidates         []string `json:"candidates"`
	RetrievedAt        *string  `json:"retrieved_at"`
	LastFailedAt       *string  `json:"last_failed_at"`
	MerchantID         *string  `json:"merchant_id"`
}

// MerchantReview is the person's decision about a transaction's merchant.
type MerchantReview struct {
	TransactionID string     `json:"transaction_id"`
	Decision      string     `json:"decision"`
	Name          *string    `json:"name"`
	MerchantID    *string    `json:"merchant_id"`
	By            MatchActor `json:"by"`
	DecidedAt     string     `json:"decided_at"`
	Version       int64      `json:"version"`
}

// TransactionMerchant is the merchant of a bank transaction. Name and
// LogoRef are set only when they may be shown; with Fallback true the caller
// uses a neutral presentation and must not put bank text in the name's place.
type TransactionMerchant struct {
	TransactionID        string              `json:"transaction_id"`
	State                string              `json:"state"`
	MerchantID           *string             `json:"merchant_id"`
	Name                 *string             `json:"name"`
	LogoRef              *string             `json:"logo_ref"`
	IdentitySource       *string             `json:"identity_source"`
	Fallback             bool                `json:"fallback"`
	NeedsReview          bool                `json:"needs_review"`
	ConfirmationOutdated bool                `json:"confirmation_outdated"`
	ReviewInherited      bool                `json:"review_inherited"`
	Enrichment           *MerchantEnrichment `json:"enrichment"`
	Review               *MerchantReview     `json:"review"`
}

// MerchantDecision is the body of a decision: Decision is MerchantConfirmed,
// MerchantCorrected or MerchantRejected. A correction names the merchant by
// Name or by MerchantID of an active record, not both.
type MerchantDecision struct {
	Decision   string `json:"decision"`
	Name       string `json:"name,omitempty"`
	MerchantID string `json:"merchant_id,omitempty"`
}

// MerchantSource describes a merchant-identification source and the
// attribution its licence asks to be shown with its answers.
type MerchantSource struct {
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	Enabled         bool     `json:"enabled"`
	Description     string   `json:"description"`
	SendsOffMachine []string `json:"sends_off_machine"`
	Attribution     *string  `json:"attribution"`
	StoresLogoRef   bool     `json:"stores_logo_ref"`
	RetentionHours  *int     `json:"retention_hours"`
}

// ListMerchants returns one page of merchant records (needs merchants:read,
// administrators only).
func (c *Client) ListMerchants(ctx context.Context, f MerchantFilter) (Page[MerchantRecord], error) {
	var p Page[MerchantRecord]
	err := c.do(ctx, request{op: opListMerchants, query: f.query()}, &p)
	return p, err
}

// AllMerchants walks every merchant matching f (its Cursor is ignored).
func (c *Client) AllMerchants(ctx context.Context, f MerchantFilter) iter.Seq2[MerchantRecord, error] {
	return Walk(ctx, func(ctx context.Context, cur string) (Page[MerchantRecord], error) {
		f.Cursor = cur
		return c.ListMerchants(ctx, f)
	})
}

func (c *Client) merchantRecord(ctx context.Context, req request) (*MerchantRecord, error) {
	var m MerchantRecord
	if err := c.do(ctx, req, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// CreateMerchant creates a merchant (needs merchants:write). An alias another
// merchant has is refused with merchant_alias_taken.
func (c *Client) CreateMerchant(ctx context.Context, in NewMerchant) (*MerchantRecord, error) {
	return c.merchantRecord(ctx, request{op: opCreateMerchant, body: in})
}

// GetMerchant returns one merchant record.
func (c *Client) GetMerchant(ctx context.Context, id string) (*MerchantRecord, error) {
	return c.merchantRecord(ctx, request{op: opGetMerchant, pathID: id})
}

// UpdateMerchant renames the merchant whose current version is version (a
// merge patch of display_name).
func (c *Client) UpdateMerchant(ctx context.Context, id string, version int64, patch MergePatch) (*MerchantRecord, error) {
	return c.merchantRecord(ctx, request{op: opUpdateMerchant, pathID: id, version: version, body: patch})
}

// ArchiveMerchant archives a merchant: it keeps everything that refers to it
// and stops new use by identifier.
func (c *Client) ArchiveMerchant(ctx context.Context, id string, version int64) (*MerchantRecord, error) {
	return c.merchantRecord(ctx, request{op: opArchiveMerchant, pathID: id, version: version, body: struct{}{}})
}

// UnarchiveMerchant makes an archived merchant active again (a merged one stays merged).
func (c *Client) UnarchiveMerchant(ctx context.Context, id string, version int64) (*MerchantRecord, error) {
	return c.merchantRecord(ctx, request{op: opUnarchiveMerchant, pathID: id, version: version, body: struct{}{}})
}

// DeleteMerchant deletes a merchant nothing refers to; otherwise it is
// refused with merchant_in_use (archive or merge it instead).
func (c *Client) DeleteMerchant(ctx context.Context, id string, version int64) error {
	return c.do(ctx, request{op: opDeleteMerchant, pathID: id, version: version}, nil)
}

// AddMerchantAlias adds an alias (merchant_alias_taken when another merchant has it).
func (c *Client) AddMerchantAlias(ctx context.Context, id, name string) (*MerchantRecord, error) {
	return c.merchantRecord(ctx, request{op: opAddMerchantAlias, pathID: id, body: map[string]string{"name": name}})
}

// RemoveMerchantAlias removes an alias.
func (c *Client) RemoveMerchantAlias(ctx context.Context, id, aliasID string) (*MerchantRecord, error) {
	return c.merchantRecord(ctx, request{op: opRemoveMerchantAlias, pathID: id, params: map[string]string{"alias_id": aliasID}})
}

// MergeMerchants merges the merchant fromID into the merchant id, whose
// current version is version. A merge cannot be undone in place.
func (c *Client) MergeMerchants(ctx context.Context, id string, version int64, fromID string) (*MerchantMerge, error) {
	var r MerchantMerge
	if err := c.do(ctx, request{op: opMergeMerchants, pathID: id, version: version, body: map[string]string{"merchant_id": fromID}}, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (c *Client) transactionMerchant(ctx context.Context, req request) (*TransactionMerchant, error) {
	var m TransactionMerchant
	if err := c.do(ctx, req, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// GetTransactionMerchant returns the merchant of a bank transaction.
func (c *Client) GetTransactionMerchant(ctx context.Context, transactionID string) (*TransactionMerchant, error) {
	return c.transactionMerchant(ctx, request{op: opGetTransactionMerchant, pathID: transactionID})
}

// ReviewTransactionMerchant records the person's decision about a
// transaction's merchant (confirm, correct or reject), replacing an earlier one.
func (c *Client) ReviewTransactionMerchant(ctx context.Context, transactionID string, d MerchantDecision) (*TransactionMerchant, error) {
	return c.transactionMerchant(ctx, request{op: opReviewTransactionMerchant, pathID: transactionID, body: d})
}

// ClearTransactionMerchantReview takes the decision back. An inherited
// decision is refused with review_inherited: clear it on the pending
// transaction the answer's review names.
func (c *Client) ClearTransactionMerchantReview(ctx context.Context, transactionID string) (*TransactionMerchant, error) {
	return c.transactionMerchant(ctx, request{op: opClearTransactionMerchantReview, pathID: transactionID})
}

// MerchantSources lists the merchant-identification sources.
func (c *Client) MerchantSources(ctx context.Context) ([]MerchantSource, error) {
	var out struct {
		Sources []MerchantSource `json:"sources"`
	}
	if err := c.do(ctx, request{op: opListMerchantSources}, &out); err != nil {
		return nil, err
	}
	return out.Sources, nil
}
