package ledgerclient

import (
	"context"
	"iter"
	"net/url"
)

// Model suggestions and in-app alerts (task L6c, ADR-0065). Everything here
// needs the administrator grant decisions:read (reads) or decisions:write;
// accepting, correcting and reverting a suggestion also need organisation:write
// (and merchants:write for a merchant).

// Suggestion states. Reverted is an accepted, auto-applied suggestion whose
// effect a person took back.
const (
	SuggestionPending    = "pending"
	SuggestionAccepted   = "accepted"
	SuggestionRejected   = "rejected"
	SuggestionSuperseded = "superseded"
	SuggestionReverted   = "reverted"
)

// ModelRef is the provider and model that said something; Label is
// "provider/model".
type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Label    string `json:"label"`
}

// SuggestionDecision is what the model chose: a category (Choice, nil when it
// chose none) or labels (Choices). Confidence is the model's own, a decimal
// string, and proves nothing.
type SuggestionDecision struct {
	Choice     *string  `json:"choice"`
	Choices    []string `json:"choices"`
	Confidence *string  `json:"confidence"`
}

// SuggestionReversal says who took back an auto-applied suggestion and when.
type SuggestionReversal struct {
	By OrganisationActor `json:"by"`
	At string            `json:"at"`
}

// Suggestion is a model suggestion. It is never an assignment: nothing is
// assigned until a person accepts or corrects it (or it was auto-applied).
type Suggestion struct {
	ID          string              `json:"id"`
	Task        string              `json:"task"`
	ObjectType  string              `json:"object_type"`
	ObjectID    string              `json:"object_id"`
	State       string              `json:"state"`
	SuggestedBy ModelRef            `json:"suggested_by"`
	Provenance  string              `json:"provenance"`
	ResponseID  *string             `json:"response_id"`
	Decision    SuggestionDecision  `json:"decision"`
	AutoApply   bool                `json:"auto_apply"`
	Reversal    *SuggestionReversal `json:"reversal"`
	CreatedAt   string              `json:"created_at"`
	DecidedAt   *string             `json:"decided_at"`
	Version     int64               `json:"version"`
}

// SuggestionFilter filters ListSuggestions; empty fields match everything.
type SuggestionFilter struct {
	ListOptions
	// States and Tasks match any of their values.
	States     []string
	Tasks      []string
	ObjectType string
	ObjectID   string
}

func (f SuggestionFilter) query() url.Values {
	q := url.Values{}
	f.apply(q)
	for _, s := range f.States {
		q.Add("state", s)
	}
	for _, t := range f.Tasks {
		q.Add("task", t)
	}
	setIf(q, "object_type", f.ObjectType)
	setIf(q, "object_id", f.ObjectID)
	return q
}

// SuggestionCorrection is the person's own answer in place of the model's.
// For a categorisation set CategoryID (nil with SetCategory true records "no
// category"); for a label suggestion set LabelIDs.
type SuggestionCorrection struct {
	CategoryID  *string
	SetCategory bool
	LabelIDs    []string
}

// ListSuggestions returns one page of suggestions, oldest first.
func (c *Client) ListSuggestions(ctx context.Context, f SuggestionFilter) (Page[Suggestion], error) {
	var p Page[Suggestion]
	err := c.do(ctx, request{op: opListSuggestions, query: f.query()}, &p)
	return p, err
}

// AllSuggestions walks every suggestion matching f (its Cursor is ignored).
func (c *Client) AllSuggestions(ctx context.Context, f SuggestionFilter) iter.Seq2[Suggestion, error] {
	return Walk(ctx, func(ctx context.Context, cur string) (Page[Suggestion], error) {
		f.Cursor = cur
		return c.ListSuggestions(ctx, f)
	})
}

// GetSuggestion returns one suggestion.
func (c *Client) GetSuggestion(ctx context.Context, id string) (*Suggestion, error) {
	var s Suggestion
	if err := c.do(ctx, request{op: opGetSuggestion, pathID: id}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *Client) suggestionAction(ctx context.Context, op operation, id string, body any) (*Suggestion, error) {
	var s Suggestion
	if body == nil {
		body = struct{}{}
	}
	if err := c.do(ctx, request{op: op, pathID: id, body: body}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// AcceptSuggestion applies a pending suggestion as the person's own decision.
func (c *Client) AcceptSuggestion(ctx context.Context, id string) (*Suggestion, error) {
	return c.suggestionAction(ctx, opAcceptSuggestion, id, nil)
}

// CorrectSuggestion applies the person's own answer instead and marks the
// suggestion rejected.
func (c *Client) CorrectSuggestion(ctx context.Context, id string, in SuggestionCorrection) (*Suggestion, error) {
	body := map[string]any{}
	if in.SetCategory || in.CategoryID != nil {
		body["category_id"] = in.CategoryID
	}
	if in.LabelIDs != nil {
		body["label_ids"] = in.LabelIDs
	}
	return c.suggestionAction(ctx, opCorrectSuggestion, id, body)
}

// RejectSuggestion turns a pending suggestion down; nothing is assigned.
func (c *Client) RejectSuggestion(ctx context.Context, id string) (*Suggestion, error) {
	return c.suggestionAction(ctx, opRejectSuggestion, id, nil)
}

// RevertSuggestion takes back an auto-applied categorisation (409
// not_reversible when the category has changed since).
func (c *Client) RevertSuggestion(ctx context.Context, id string) (*Suggestion, error) {
	return c.suggestionAction(ctx, opRevertSuggestion, id, nil)
}

// Alert states.
const (
	AlertOpen      = "open"
	AlertDismissed = "dismissed"
	AlertResolved  = "resolved"
)

// AlertModelText is optional wording a model wrote for an alert.
type AlertModelText struct {
	Text       string `json:"text"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Label      string `json:"label"`
	Provenance string `json:"provenance"`
}

// Alert is an in-app alert. Message is Ledger's own text; ModelText, when
// set, is labelled extra wording and never replaces it.
type Alert struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind"`
	ObjectType string            `json:"object_type"`
	ObjectID   string            `json:"object_id"`
	Facts      map[string]string `json:"facts"`
	Message    string            `json:"message"`
	ModelText  *AlertModelText   `json:"model_text"`
	State      string            `json:"state"`
	CreatedAt  string            `json:"created_at"`
	ClosedAt   *string           `json:"closed_at"`
	Version    int64             `json:"version"`
}

// AlertFilter filters ListAlerts; empty fields match everything.
type AlertFilter struct {
	ListOptions
	States     []string
	Kinds      []string
	ObjectType string
	ObjectID   string
}

func (f AlertFilter) query() url.Values {
	q := url.Values{}
	f.apply(q)
	for _, s := range f.States {
		q.Add("state", s)
	}
	for _, k := range f.Kinds {
		q.Add("kind", k)
	}
	setIf(q, "object_type", f.ObjectType)
	setIf(q, "object_id", f.ObjectID)
	return q
}

// ListAlerts returns one page of alerts, oldest first.
func (c *Client) ListAlerts(ctx context.Context, f AlertFilter) (Page[Alert], error) {
	var p Page[Alert]
	err := c.do(ctx, request{op: opListAlerts, query: f.query()}, &p)
	return p, err
}

// AllAlerts walks every alert matching f (its Cursor is ignored).
func (c *Client) AllAlerts(ctx context.Context, f AlertFilter) iter.Seq2[Alert, error] {
	return Walk(ctx, func(ctx context.Context, cur string) (Page[Alert], error) {
		f.Cursor = cur
		return c.ListAlerts(ctx, f)
	})
}

// GetAlert returns one alert.
func (c *Client) GetAlert(ctx context.Context, id string) (*Alert, error) {
	var a Alert
	if err := c.do(ctx, request{op: opGetAlert, pathID: id}, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// DismissAlert closes an open alert as dismissed; a dismissal sticks.
func (c *Client) DismissAlert(ctx context.Context, id string) (*Alert, error) {
	var a Alert
	if err := c.do(ctx, request{op: opDismissAlert, pathID: id, body: struct{}{}}, &a); err != nil {
		return nil, err
	}
	return &a, nil
}
