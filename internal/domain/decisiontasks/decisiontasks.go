// Package decisiontasks runs the model-assisted organisation tasks (task L6,
// ADR-0065, decision D13): categorisation and label suggestions, and the
// wording of in-app alerts.
//
// It sits between the decision layer (internal/domain/decisions: consent,
// allowlist, cap, validation, suggestion storage) and the organisation model
// (internal/domain/organisation: categories and labels with origins). The
// rules it keeps:
//
//   - model output is a pending suggestion, kept apart from assignments, until
//     a person accepts, corrects or rejects it;
//   - the only automatic application is a categorisation the operator opted
//     in to auto-apply: it is written as a model-origin assignment (which a
//     rule or a person always outranks), audited, and reversible;
//   - accepting or correcting writes the person's own (user-origin)
//     assignment: the suggestion keeps the provider and model that said it;
//   - nothing here reads bank text beyond the permitted fields, and nothing
//     is sent for an object that already has a person's or a rule's decision.
package decisiontasks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/organisation"
)

// Runner is the decision service ([decisions.Service]).
type Runner interface {
	Run(ctx context.Context, name decisions.TaskName, in decisions.Input) (decisions.Result, error)
}

// Suggestions is the suggestion store (internal/store/decisions).
type Suggestions interface {
	Suggestion(ctx context.Context, id string) (decisions.Suggestion, error)
	Decide(ctx context.Context, actor audit.Actor, id string, to decisions.SuggestionState, action audit.Action) (decisions.Suggestion, error)
	RecordReversal(ctx context.Context, actor audit.Actor, id string) error
	Declined(ctx context.Context, task decisions.TaskName, obj decisions.ObjectRef) (bool, error)
	Pending(ctx context.Context, task decisions.TaskName, obj decisions.ObjectRef) (bool, error)
}

// Organisation is the part of the organisation store the tasks use.
type Organisation interface {
	Get(ctx context.Context, obj organisation.ObjectRef) (organisation.Assignments, error)
	ListDefinitions(ctx context.Context, k organisation.Kind, f organisation.ListFilter) ([]organisation.Definition, error)
	ApplyAutomatic(ctx context.Context, actor audit.Actor, obj organisation.ObjectRef, a organisation.Automatic) (organisation.AutomaticResult, error)
	SetCategory(ctx context.Context, actor audit.Actor, obj organisation.ObjectRef, categoryID *string) (organisation.Assignments, error)
	ResetAutomaticCategory(ctx context.Context, actor audit.Actor, obj organisation.ObjectRef, source, categoryID string) error
	AddLabels(ctx context.Context, actor audit.Actor, obj organisation.ObjectRef, labelIDs []string) (organisation.Assignments, error)
}

// Errors (beyond those of the decisions and organisation packages).
var (
	// ErrNothingToApply means the suggestion holds no choice.
	ErrNothingToApply = errors.New("the suggestion holds nothing to apply")
	// ErrWrongTask means the operation does not fit the suggestion's task.
	ErrWrongTask = errors.New("the operation does not fit the suggestion's task")
)

// SystemActor is who applies a suggestion automatically.
var SystemActor = audit.Actor{Kind: audit.ActorSystem, Via: audit.ViaSystem}

// Facts are what the caller knows about the object to organise. There is no
// field for an IBAN, an account number, the counterparty's name or a balance:
// they cannot be passed (decision D13), and the decision layer filters the
// rest by the provider's allowlist.
type Facts struct {
	Object      organisation.ObjectRef
	Description string
	// MerchantName is the merchant record's display name; leave empty when
	// the merchant is unknown. It is not sent when MerchantIsTransfer: a
	// transfer's merchant name may be a person's name (ADR-0060 review N4).
	MerchantName       string
	MerchantIsTransfer bool
	Amount             money.Money
	BookingDate        calendar.Date
}

func (f Facts) values() map[decisions.Field]string {
	v := map[decisions.Field]string{}
	if f.Description != "" {
		v[decisions.FieldDescription] = f.Description
	}
	if f.MerchantName != "" && !f.MerchantIsTransfer {
		v[decisions.FieldMerchantName] = f.MerchantName
	}
	if f.Amount.IsValid() {
		v[decisions.FieldAmount] = f.Amount.Amount().String()
		v[decisions.FieldCurrency] = f.Amount.Currency().String()
	}
	if !f.BookingDate.IsZero() {
		v[decisions.FieldBookingDate] = f.BookingDate.String()
	}
	return v
}

// Skip says why no question was asked.
type Skip string

// Skips. A skipped object costs no request.
const (
	SkipAssigned     Skip = "assigned"      // a person or a rule already decided
	SkipDeclined     Skip = "declined"      // a person turned a suggestion down before
	SkipPending      Skip = "pending"       // a suggestion for the object still awaits review
	SkipNoCandidates Skip = "no_candidates" // nothing to choose from
	SkipTooMany      Skip = "too_many"      // more candidates than the task offers
)

// Outcome is what a suggestion run did.
type Outcome struct {
	// Skipped is set when nothing was asked.
	Skipped Skip
	// Result is the decision layer's result: a suggestion, or a reason.
	Result decisions.Result
	// Applied is true when the suggestion was auto-applied.
	Applied bool
	// Kept lists proposals auto-apply left unapplied (the suggestion then
	// stays pending for review).
	Kept []organisation.Kept
	// NoChoice is true when the model chose nothing (nothing to review).
	NoChoice bool
}

// Service runs the tasks.
type Service struct {
	runner Runner
	sugg   Suggestions
	org    Organisation
}

// New returns a Service.
func New(r Runner, s Suggestions, o Organisation) *Service {
	return &Service{runner: r, sugg: s, org: o}
}

// SourceOf names a suggestion as an assignment's source: "provider:model".
func SourceOf(sg decisions.Suggestion) string {
	s := sg.Provider + ":" + sg.Model
	if len(s) > organisation.MaxSource {
		s = s[:organisation.MaxSource]
	}
	return strings.ToValidUTF8(s, "")
}

func candidates(ctx context.Context, org Organisation, k organisation.Kind, max int) ([]decisions.Candidate, Skip, error) {
	active := false
	var all []organisation.Definition
	after := ""
	for {
		page, err := org.ListDefinitions(ctx, k, organisation.ListFilter{After: after, Limit: 200, Archived: &active})
		if err != nil {
			return nil, "", err
		}
		n := min(len(page), 200)
		all = append(all, page[:n]...)
		if len(page) <= 200 || len(all) > max {
			break
		}
		after = page[n-1].ID
	}
	if len(all) > max {
		return nil, SkipTooMany, nil
	}
	names := map[string]string{}
	for _, d := range all {
		names[d.ID] = d.Name
	}
	out := make([]decisions.Candidate, 0, len(all))
	for _, d := range all {
		name := d.Name
		if d.ParentID != nil {
			if p, ok := names[*d.ParentID]; ok {
				name = p + " / " + d.Name
			}
		}
		out = append(out, decisions.Candidate{ID: d.ID, Name: name})
	}
	return out, "", nil
}

func objectOf(o organisation.ObjectRef) decisions.ObjectRef {
	return decisions.ObjectRef{Type: string(o.Type), ID: o.ID}
}

// SuggestCategory asks the categorisation task about one object. It is
// skipped, without a request, when a person or a rule has categorised the
// object (a model never replaces either), when a person turned a suggestion
// down before, or when there is no active category. With auto-apply on for the
// task the choice is written as a model-origin category and audited; otherwise
// it waits as a pending suggestion.
func (s *Service) SuggestCategory(ctx context.Context, f Facts) (Outcome, error) {
	cur, err := s.org.Get(ctx, f.Object)
	if err != nil {
		return Outcome{}, err
	}
	if c := cur.Category; c != nil && c.Origin != organisation.OriginModel {
		return Outcome{Skipped: SkipAssigned}, nil
	}
	task, _ := decisions.LookupTask(decisions.TaskCategorisation)
	cands, skip, err := candidates(ctx, s.org, organisation.KindCategory, task.MaxCandidates)
	if err != nil {
		return Outcome{}, err
	}
	if skip == "" && len(cands) == 0 {
		skip = SkipNoCandidates
	}
	if skip != "" {
		return Outcome{Skipped: skip}, nil
	}
	return s.ask(ctx, decisions.TaskCategorisation, f, cands)
}

// SuggestLabels asks the label task about one object. Labels the object has,
// or the person removed, are not offered. The answer is always a pending
// suggestion: labels are never auto-applied (decision D13).
func (s *Service) SuggestLabels(ctx context.Context, f Facts) (Outcome, error) {
	cur, err := s.org.Get(ctx, f.Object)
	if err != nil {
		return Outcome{}, err
	}
	task, _ := decisions.LookupTask(decisions.TaskLabelSuggestion)
	cands, skip, err := candidates(ctx, s.org, organisation.KindLabel, task.MaxCandidates+len(cur.Labels)+len(cur.Removed))
	if err != nil {
		return Outcome{}, err
	}
	have := map[string]bool{}
	for _, l := range cur.Labels {
		have[l.Label.ID] = true
	}
	for _, l := range cur.Removed {
		have[l.Label.ID] = true
	}
	cands = slices.DeleteFunc(cands, func(c decisions.Candidate) bool { return have[c.ID] })
	if skip == "" && len(cands) > task.MaxCandidates {
		skip = SkipTooMany
	}
	if skip == "" && len(cands) == 0 {
		skip = SkipNoCandidates
	}
	if skip != "" {
		return Outcome{Skipped: skip}, nil
	}
	return s.ask(ctx, decisions.TaskLabelSuggestion, f, cands)
}

func (s *Service) ask(ctx context.Context, name decisions.TaskName, f Facts, cands []decisions.Candidate) (Outcome, error) {
	ref := objectOf(f.Object)
	if declined, err := s.sugg.Declined(ctx, name, ref); err != nil {
		return Outcome{}, err
	} else if declined {
		return Outcome{Skipped: SkipDeclined}, nil
	}
	if pending, err := s.sugg.Pending(ctx, name, ref); err != nil {
		return Outcome{}, err
	} else if pending {
		return Outcome{Skipped: SkipPending}, nil
	}
	res, err := s.runner.Run(ctx, name, decisions.Input{Object: ref, Values: f.values(), Candidates: cands})
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{Result: res}
	sg := res.Suggestion
	if sg == nil {
		return out, nil
	}
	choice := sg.Decision.Choice != nil
	if name == decisions.TaskLabelSuggestion {
		choice = len(sg.Decision.Choices) > 0
	}
	if !choice {
		// The model chose none: nothing to review. The row stays as a record
		// of the question and leaves the pending queue.
		out.NoChoice = true
		_, err := s.sugg.Decide(ctx, SystemActor, sg.ID, decisions.StateSuperseded, audit.DecisionSuggestionDismissed)
		return out, err
	}
	if name != decisions.TaskCategorisation || !sg.AutoApply {
		return out, nil
	}
	r, err := s.org.ApplyAutomatic(ctx, SystemActor, f.Object, organisation.Automatic{Origin: organisation.OriginModel, Source: SourceOf(*sg), CategoryID: sg.Decision.Choice})
	if err != nil {
		return out, fmt.Errorf("decisiontasks: auto-applying the category: %w", err)
	}
	out.Kept = r.Kept
	if r.CategoryApplied {
		updated, err := s.sugg.Decide(ctx, SystemActor, sg.ID, decisions.StateAccepted, audit.DecisionSuggestionAutoApplied)
		if err != nil {
			return out, err
		}
		out.Applied, out.Result.Suggestion = true, &updated
	}
	return out, nil
}

func (s *Service) pending(ctx context.Context, id string) (decisions.Suggestion, organisation.ObjectRef, error) {
	sg, err := s.sugg.Suggestion(ctx, id)
	if err != nil {
		return sg, organisation.ObjectRef{}, err
	}
	if sg.State != decisions.StatePending {
		return sg, organisation.ObjectRef{}, decisions.ErrNotPending
	}
	obj := organisation.ObjectRef{Type: organisation.ObjectType(sg.Object.Type), ID: sg.Object.ID}
	if sg.Task != decisions.TaskCategorisation && sg.Task != decisions.TaskLabelSuggestion {
		return sg, obj, ErrWrongTask
	}
	if !obj.Type.Valid() {
		return sg, obj, ErrWrongTask
	}
	return sg, obj, nil
}

// Accept applies a pending suggestion as the person's own decision (a
// user-origin assignment, which nothing automatic replaces) and marks it
// accepted. The object and the definitions are checked again by the
// organisation store: a vanished object or an archived definition is refused,
// and the suggestion stays pending.
func (s *Service) Accept(ctx context.Context, actor audit.Actor, id string) (decisions.Suggestion, error) {
	sg, obj, err := s.pending(ctx, id)
	if err != nil {
		return sg, err
	}
	switch sg.Task {
	case decisions.TaskCategorisation:
		if sg.Decision.Choice == nil {
			return sg, ErrNothingToApply
		}
		if _, err := s.org.SetCategory(ctx, actor, obj, sg.Decision.Choice); err != nil {
			return sg, err
		}
	default:
		if len(sg.Decision.Choices) == 0 {
			return sg, ErrNothingToApply
		}
		if _, err := s.org.AddLabels(ctx, actor, obj, sg.Decision.Choices); err != nil {
			return sg, err
		}
	}
	return s.sugg.Decide(ctx, actor, id, decisions.StateAccepted, audit.DecisionSuggestionAccepted)
}

// Correction is the person's own answer in place of the model's.
type Correction struct {
	// CategoryID is the category the person chooses; nil records "no
	// category" (categorisation only).
	CategoryID *string
	// LabelIDs are the labels the person adds (label suggestions only).
	LabelIDs []string
}

// Correct applies the person's own answer instead of the suggestion's and marks
// the suggestion rejected (the person disagreed); the model is not asked about
// the object again.
func (s *Service) Correct(ctx context.Context, actor audit.Actor, id string, c Correction) (decisions.Suggestion, error) {
	sg, obj, err := s.pending(ctx, id)
	if err != nil {
		return sg, err
	}
	switch sg.Task {
	case decisions.TaskCategorisation:
		if _, err := s.org.SetCategory(ctx, actor, obj, c.CategoryID); err != nil {
			return sg, err
		}
	default:
		if _, err := s.org.AddLabels(ctx, actor, obj, c.LabelIDs); err != nil {
			return sg, err
		}
	}
	return s.sugg.Decide(ctx, actor, id, decisions.StateRejected, audit.DecisionSuggestionCorrected)
}

// Reject turns a pending suggestion down; nothing is assigned, and the model
// is not asked about the object again.
func (s *Service) Reject(ctx context.Context, actor audit.Actor, id string) (decisions.Suggestion, error) {
	if _, _, err := s.pending(ctx, id); err != nil {
		return decisions.Suggestion{}, err
	}
	return s.sugg.Decide(ctx, actor, id, decisions.StateRejected, audit.DecisionSuggestionRejected)
}

// Revert takes back an auto-applied category: the model-origin assignment is
// removed (the object has no category again), and the model is not asked
// about the object again. It is refused ([decisions.ErrNotReversible]) when the
// suggestion was not auto-applied, or when the object's category is no longer
// the one it applied (the person or a rule has changed it since: that decision
// stands and is theirs to change).
func (s *Service) Revert(ctx context.Context, actor audit.Actor, id string) error {
	sg, err := s.sugg.Suggestion(ctx, id)
	if err != nil {
		return err
	}
	if sg.Task != decisions.TaskCategorisation || sg.State != decisions.StateAccepted || !sg.AutoApply || sg.Decision.Choice == nil {
		return decisions.ErrNotReversible
	}
	obj := organisation.ObjectRef{Type: organisation.ObjectType(sg.Object.Type), ID: sg.Object.ID}
	// The check and the reset are one transaction in the organisation store:
	// a decision made in between is never undone.
	if err := s.org.ResetAutomaticCategory(ctx, actor, obj, SourceOf(sg), *sg.Decision.Choice); errors.Is(err, organisation.ErrAssignmentChanged) {
		return decisions.ErrNotReversible
	} else if err != nil {
		return err
	}
	return s.sugg.RecordReversal(ctx, actor, id)
}
