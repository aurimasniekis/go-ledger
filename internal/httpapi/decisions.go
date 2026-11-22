package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/alerts"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisiontasks"
	"github.com/aurimasniekis/go-ledger/internal/domain/organisation"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// Paths of the decision-task routes (task L6c, ADR-0065).
const (
	SuggestionsPath = APIPrefix + "/suggestions"
	AlertsPath      = APIPrefix + "/alerts"
)

var (
	grantDecisionsRead  = authz.G(authz.ResourceDecisions, authz.ActionRead)
	grantDecisionsWrite = authz.G(authz.ResourceDecisions, authz.ActionWrite)
)

// Error codes of the decision-task routes (all 409).
const (
	// CodeSuggestionNotPending: the suggestion was already accepted, rejected,
	// corrected or superseded; a suggestion's state moves once.
	CodeSuggestionNotPending Code = "suggestion_not_pending"
	// CodeNotReversible: there is nothing to revert: the suggestion was not
	// auto-applied, was already reverted, or the object's category has been
	// changed since by a person or a rule (that decision stands).
	CodeNotReversible Code = "not_reversible"
	// CodeAssignmentChanged: the object's assignment changed while the request
	// ran, so nothing was undone.
	CodeAssignmentChanged Code = "assignment_changed"
	// CodeNothingToApply: the suggestion holds no choice (the model chose
	// none); reject it or correct it with an answer of your own.
	CodeNothingToApply Code = "nothing_to_apply"
	// CodeWrongTask: the operation does not fit the suggestion's task.
	CodeWrongTask Code = "wrong_task"
	// CodeAlertNotOpen: the alert is already dismissed or resolved.
	CodeAlertNotOpen Code = "alert_not_open"
)

// SuggestionReader reads stored suggestions (internal/store/decisions).
type SuggestionReader interface {
	ListSuggestions(ctx context.Context, f decisions.SuggestionFilter) ([]decisions.SuggestionView, error)
	SuggestionView(ctx context.Context, id string) (decisions.SuggestionView, error)
}

// SuggestionActions decides suggestions ([decisiontasks.Service]).
type SuggestionActions interface {
	Accept(ctx context.Context, actor audit.Actor, id string) (decisions.Suggestion, error)
	Correct(ctx context.Context, actor audit.Actor, id string, c decisiontasks.Correction) (decisions.Suggestion, error)
	Reject(ctx context.Context, actor audit.Actor, id string) (decisions.Suggestion, error)
	Revert(ctx context.Context, actor audit.Actor, id string) error
}

// AlertStore reads and closes alerts (internal/store/alerts).
type AlertStore interface {
	Find(ctx context.Context, f alerts.Filter) ([]alerts.Alert, error)
	Get(ctx context.Context, id string) (alerts.Alert, error)
	Close(ctx context.Context, actor audit.Actor, id string, to alerts.State) (alerts.Alert, error)
}

// modelJSON says which provider and model said something.
type modelJSON struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Label is "provider/model", the text to show after "suggested by".
	Label string `json:"label"`
}

func toModelJSON(provider, model string) modelJSON {
	return modelJSON{Provider: provider, Model: model, Label: provider + "/" + model}
}

type decisionJSON struct {
	// Choice is the category the model chose (task categorisation); null when
	// it chose none.
	Choice *string `json:"choice"`
	// Choices are the labels it chose (task label_suggestion).
	Choices []string `json:"choices"`
	// Confidence is the model's own confidence as a decimal string from 0 to
	// 1, or null. It is not proof of anything.
	Confidence *string `json:"confidence"`
}

type reversalJSON struct {
	By deciderJSON `json:"by"`
	At time.Time   `json:"at"`
}

// suggestionJSON is a model suggestion: what it said, who said it, where it
// stands. It is never an assignment: the object's own assignments (and their
// origin) are read from the organisation routes.
type suggestionJSON struct {
	ID         string             `json:"id"`
	Task       decisions.TaskName `json:"task"`
	ObjectType string             `json:"object_type"`
	ObjectID   string             `json:"object_id"`
	// State is pending, accepted, rejected, superseded or reverted (an
	// accepted auto-applied suggestion whose effect was taken back).
	State decisions.SuggestionState `json:"state"`
	// SuggestedBy is the provider and model that said it; it stays when the
	// person accepts it.
	SuggestedBy modelJSON `json:"suggested_by"`
	// Provenance is "suggested by provider/model".
	Provenance string       `json:"provenance"`
	ResponseID *string      `json:"response_id"`
	Decision   decisionJSON `json:"decision"`
	// AutoApply is true when the suggestion was applied automatically as a
	// model-origin assignment (categorisation only); such a suggestion can be
	// reverted.
	AutoApply bool          `json:"auto_apply"`
	Reversal  *reversalJSON `json:"reversal"`
	CreatedAt time.Time     `json:"created_at"`
	DecidedAt *time.Time    `json:"decided_at"`
	Version   int64         `json:"version"`
}

func toSuggestionJSON(v decisions.SuggestionView) suggestionJSON {
	sg := v.Suggestion
	out := suggestionJSON{ID: sg.ID, Task: sg.Task, ObjectType: sg.Object.Type, ObjectID: sg.Object.ID, State: sg.State,
		SuggestedBy: toModelJSON(sg.Provider, sg.Model), Provenance: "suggested by " + sg.Provider + "/" + sg.Model, ResponseID: strPtr(sg.ResponseID),
		Decision:  decisionJSON{Choice: sg.Decision.Choice, Choices: slices.Clone(sg.Decision.Choices), Confidence: strPtr(sg.Decision.Confidence)},
		AutoApply: sg.AutoApply, CreatedAt: sg.CreatedAt, DecidedAt: sg.DecidedAt, Version: sg.Version}
	if out.Decision.Choices == nil {
		out.Decision.Choices = []string{}
	}
	if r := v.Reversal; r != nil {
		out.State = decisions.StateReverted
		out.Reversal = &reversalJSON{By: deciderJSON{Kind: audit.ActorKind(r.ByKind), UserID: strPtr(r.ByUserID)}, At: r.At}
	}
	return out
}

// modelTextJSON is the optional wording a model wrote for an alert, kept apart
// from the alert's own message and labelled with its provider and model.
type modelTextJSON struct {
	Text string `json:"text"`
	modelJSON
	Provenance string `json:"provenance"`
}

// alertJSON is an in-app alert. Message is Ledger's own deterministic wording
// of the facts; ModelText, when present, is extra and never replaces it.
type alertJSON struct {
	ID         string            `json:"id"`
	Kind       alerts.Kind       `json:"kind"`
	ObjectType alerts.ObjectType `json:"object_type"`
	ObjectID   string            `json:"object_id"`
	// Facts are exact values with their currencies, as text.
	Facts   map[string]string `json:"facts"`
	Message string            `json:"message"`
	// ModelText is null unless a model worded the alert.
	ModelText *modelTextJSON `json:"model_text"`
	// State is open, dismissed (a person closed it) or resolved (the
	// condition stopped holding).
	State     alerts.State `json:"state"`
	CreatedAt time.Time    `json:"created_at"`
	ClosedAt  *time.Time   `json:"closed_at"`
	Version   int64        `json:"version"`
}

func toAlertJSON(a alerts.Alert) alertJSON {
	out := alertJSON{ID: a.ID, Kind: a.Kind, ObjectType: a.ObjectType, ObjectID: a.ObjectID, Facts: a.Facts, Message: a.Message,
		State: a.State, CreatedAt: a.CreatedAt, ClosedAt: a.ClosedAt, Version: a.Version}
	if out.Facts == nil {
		out.Facts = map[string]string{}
	}
	if w := a.Wording; w != nil {
		out.ModelText = &modelTextJSON{Text: w.Text, modelJSON: toModelJSON(w.Provider, w.Model), Provenance: "suggested by " + w.Provider + "/" + w.Model}
	}
	return out
}

// decisionError maps the errors of the decision tasks to API errors.
func decisionError(err error) error {
	conflict := func(code Code) error {
		return &Error{Status: http.StatusConflict, Code: code, Message: err.Error(), Cause: err}
	}
	switch {
	case errors.Is(err, decisions.ErrNotPending):
		return conflict(CodeSuggestionNotPending)
	case errors.Is(err, decisions.ErrNotReversible):
		return conflict(CodeNotReversible)
	case errors.Is(err, organisation.ErrAssignmentChanged):
		return conflict(CodeAssignmentChanged)
	case errors.Is(err, decisiontasks.ErrNothingToApply):
		return conflict(CodeNothingToApply)
	case errors.Is(err, decisiontasks.ErrWrongTask):
		return conflict(CodeWrongTask)
	case errors.Is(err, alerts.ErrNotOpen):
		return conflict(CodeAlertNotOpen)
	}
	return organisationError(err)
}

// reviewTasks are the tasks whose suggestions a person reviews. The alerts
// task's answers are attached to the alert as model_text and never queued.
var reviewTasks = []decisions.TaskName{decisions.TaskCategorisation, decisions.TaskLabelSuggestion}

func suggestionsListSpec() ListSpec {
	oneOf := func(what string, ok func(string) bool) func(string) error {
		return func(v string) error {
			if !ok(v) {
				return errors.New(what)
			}
			return nil
		}
	}
	return ListSpec{Filters: map[string]FilterSpec{
		"state": {Ops: []FilterOp{OpEq}, Check: oneOf("state must be pending, accepted, rejected, superseded or reverted", func(v string) bool {
			return decisions.SuggestionState(v).Valid() || decisions.SuggestionState(v) == decisions.StateReverted
		})},
		"task": {Ops: []FilterOp{OpEq}, Check: oneOf("task must be categorisation or label_suggestion", func(v string) bool {
			return slices.Contains(reviewTasks, decisions.TaskName(v))
		})},
		"object_type": {Ops: []FilterOp{OpEq}, Check: oneOf("object_type must be transaction, commitment, account or merchant", func(v string) bool {
			return organisation.ObjectType(v).Valid()
		})},
		"object_id": {Ops: []FilterOp{OpEq}, Check: oneOf("object_id must be an identifier", resource.ValidID)},
	}}
}

func alertsListSpec() ListSpec {
	return ListSpec{Filters: map[string]FilterSpec{
		"state": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
			if !slices.Contains([]alerts.State{alerts.StateOpen, alerts.StateDismissed, alerts.StateResolved}, alerts.State(v)) {
				return errors.New("state must be open, dismissed or resolved")
			}
			return nil
		}},
		"kind": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
			if !slices.Contains([]alerts.Kind{alerts.KindConsentExpiring, alerts.KindAmountChanged, alerts.KindUnmatchedOverdue}, alerts.Kind(v)) {
				return errors.New("kind must be consent_expiring, commitment_amount_changed or unmatched_overdue")
			}
			return nil
		}},
		"object_type": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
			if !slices.Contains([]alerts.ObjectType{alerts.ObjectBankConnection, alerts.ObjectCommitment, alerts.ObjectOccurrence}, alerts.ObjectType(v)) {
				return errors.New("object_type must be bank_connection, commitment or occurrence")
			}
			return nil
		}},
		"object_id": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
			if len(v) > 200 {
				return errors.New("object_id is too long")
			}
			return nil
		}},
	}}
}

// single returns the one value of an equality filter ("" when absent) or a
// validation error when it was repeated.
func single(q ListQuery, field string) (string, error) {
	v := q.Filter(field, OpEq)
	if len(v) > 1 {
		return "", Validation(field, field+" must be given once")
	}
	if len(v) == 1 {
		return v[0], nil
	}
	return "", nil
}

// writeGate refuses a caller that lacks organisation:write (and merchants:write
// for a merchant): accepting, correcting and reverting write assignments, and a
// decisions:write grant alone must not reach them.
func writeGate(r *http.Request, objectType string) error {
	p, ok := authz.FromContext(r.Context())
	if !ok || !p.Allows(grantOrganisationWrite) {
		return Forbidden("this changes the object's assignments and requires the " + grantOrganisationWrite.String() + " grant")
	}
	if organisation.ObjectType(objectType) == organisation.ObjectMerchant && !p.Allows(grantMerchantsWrite) {
		return Forbidden("merchant assignments require the " + grantMerchantsWrite.String() + " grant")
	}
	return nil
}

// registerDecisions registers the suggestion and alert routes (task L6c,
// ADR-0065). Suggestions are model output kept apart from assignments: only
// the person's accept or correct writes one (as the person's own decision),
// and every action is audited by the stores.
func registerDecisions(s *Server, rd SuggestionReader, act SuggestionActions, al AlertStore) {
	item := SuggestionsPath + "/{id}"
	respond := func(w http.ResponseWriter, r *http.Request, id string) {
		v, err := rd.SuggestionView(r.Context(), id)
		if err != nil {
			WriteError(w, r, decisionError(err))
			return
		}
		SetVersion(w, v.Version)
		WriteJSON(w, http.StatusOK, toSuggestionJSON(v))
	}
	// load reads the suggestion and applies the write gate for its object.
	load := func(w http.ResponseWriter, r *http.Request, gate bool) (decisions.SuggestionView, bool) {
		v, err := rd.SuggestionView(r.Context(), r.PathValue("id"))
		if err == nil && gate {
			err = writeGate(r, v.Object.Type)
		}
		if err != nil {
			WriteError(w, r, decisionError(err))
			return v, false
		}
		return v, true
	}

	s.HandleAPI("GET "+SuggestionsPath, Requires(grantDecisionsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, suggestionsListSpec())
		if err != nil {
			WriteError(w, r, err)
			return
		}
		f := decisions.SuggestionFilter{Tasks: reviewTasks, After: q.After, Limit: q.Limit + 1}
		if ts := q.Filter("task", OpEq); len(ts) > 0 {
			f.Tasks = nil
			for _, t := range ts {
				f.Tasks = append(f.Tasks, decisions.TaskName(t))
			}
		}
		for _, v := range q.Filter("state", OpEq) {
			f.States = append(f.States, decisions.SuggestionState(v))
		}
		if f.ObjectType, err = single(q, "object_type"); err == nil {
			f.ObjectID, err = single(q, "object_id")
		}
		if err != nil {
			WriteError(w, r, err)
			return
		}
		items, err := rd.ListSuggestions(r.Context(), f)
		if err != nil {
			WriteError(w, r, decisionError(err))
			return
		}
		out := make([]suggestionJSON, len(items))
		for i, v := range items {
			out[i] = toSuggestionJSON(v)
		}
		WriteJSON(w, http.StatusOK, NewPage(out, q, func(v suggestionJSON) string { return v.ID }))
	}))

	s.HandleAPI("GET "+item, Requires(grantDecisionsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, r.PathValue("id"))
	}))

	s.HandleAPI("POST "+item+"/accept", Requires(grantDecisionsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := load(w, r, true); !ok {
			return
		}
		if _, err := act.Accept(r.Context(), matchActor(r), r.PathValue("id")); err != nil {
			WriteError(w, r, decisionError(err))
			return
		}
		respond(w, r, r.PathValue("id"))
	}))

	s.HandleAPI("POST "+item+"/correct", Requires(grantDecisionsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, ok := load(w, r, true)
		if !ok {
			return
		}
		doc, err := decodeObject(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		c, errs := decodeCorrection(v.Task, doc)
		if len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		if _, err := act.Correct(r.Context(), matchActor(r), r.PathValue("id"), c); err != nil {
			WriteError(w, r, decisionError(err))
			return
		}
		respond(w, r, r.PathValue("id"))
	}))

	s.HandleAPI("POST "+item+"/reject", Requires(grantDecisionsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := load(w, r, false); !ok {
			return
		}
		if _, err := act.Reject(r.Context(), matchActor(r), r.PathValue("id")); err != nil {
			WriteError(w, r, decisionError(err))
			return
		}
		respond(w, r, r.PathValue("id"))
	}))

	s.HandleAPI("POST "+item+"/revert", Requires(grantDecisionsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := load(w, r, true); !ok {
			return
		}
		if err := act.Revert(r.Context(), matchActor(r), r.PathValue("id")); err != nil {
			WriteError(w, r, decisionError(err))
			return
		}
		respond(w, r, r.PathValue("id"))
	}))

	aitem := AlertsPath + "/{id}"
	s.HandleAPI("GET "+AlertsPath, Requires(grantDecisionsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, alertsListSpec())
		if err != nil {
			WriteError(w, r, err)
			return
		}
		f := alerts.Filter{After: q.After, Limit: q.Limit + 1}
		for _, v := range q.Filter("state", OpEq) {
			f.States = append(f.States, alerts.State(v))
		}
		for _, v := range q.Filter("kind", OpEq) {
			f.Kinds = append(f.Kinds, alerts.Kind(v))
		}
		var ot string
		if ot, err = single(q, "object_type"); err == nil {
			f.ObjectType = alerts.ObjectType(ot)
			f.ObjectID, err = single(q, "object_id")
		}
		if err != nil {
			WriteError(w, r, err)
			return
		}
		items, err := al.Find(r.Context(), f)
		if err != nil {
			WriteError(w, r, decisionError(err))
			return
		}
		out := make([]alertJSON, len(items))
		for i, a := range items {
			out[i] = toAlertJSON(a)
		}
		WriteJSON(w, http.StatusOK, NewPage(out, q, func(a alertJSON) string { return a.ID }))
	}))

	s.HandleAPI("GET "+aitem, Requires(grantDecisionsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, err := al.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			WriteError(w, r, decisionError(err))
			return
		}
		SetVersion(w, a.Version)
		WriteJSON(w, http.StatusOK, toAlertJSON(a))
	}))

	s.HandleAPI("POST "+aitem+"/dismiss", Requires(grantDecisionsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, err := al.Close(r.Context(), matchActor(r), r.PathValue("id"), alerts.StateDismissed)
		if err != nil {
			WriteError(w, r, decisionError(err))
			return
		}
		SetVersion(w, a.Version)
		WriteJSON(w, http.StatusOK, toAlertJSON(a))
	}))
}

// maxCorrectionLabels bounds the labels one correction adds.
const maxCorrectionLabels = 50

// decodeCorrection reads the body of a correction for the suggestion's task:
// category_id (an identifier, or null for no category) for categorisation,
// label_ids (a non-empty list of identifiers) for label suggestions.
func decodeCorrection(task decisions.TaskName, doc map[string]json.RawMessage) (decisiontasks.Correction, []commitments.FieldError) {
	var c decisiontasks.Correction
	var errs []commitments.FieldError
	allowed := "category_id"
	if task == decisions.TaskLabelSuggestion {
		allowed = "label_ids"
	}
	for k := range doc {
		if k != allowed {
			errs = append(errs, commitments.FieldError{Field: k, Message: "unknown field " + k + " for a " + string(task) + " suggestion"})
		}
	}
	slices.SortFunc(errs, func(a, b commitments.FieldError) int { return strings.Compare(a.Field, b.Field) })
	raw, ok := doc[allowed]
	switch {
	case !ok:
		errs = append(errs, commitments.FieldError{Field: allowed, Message: allowed + " is required"})
	case task == decisions.TaskCategorisation:
		if !isJSONNull(raw) {
			var id string
			if json.Unmarshal(raw, &id) != nil || !resource.ValidID(id) {
				errs = append(errs, commitments.FieldError{Field: allowed, Message: "category_id must be a category identifier, or null for no category"})
			} else {
				c.CategoryID = &id
			}
		}
	default:
		var ids []string
		if json.Unmarshal(raw, &ids) != nil || len(ids) == 0 || len(ids) > maxCorrectionLabels || slices.ContainsFunc(ids, func(id string) bool { return !resource.ValidID(id) }) {
			errs = append(errs, commitments.FieldError{Field: allowed, Message: "label_ids must be a list of 1 to 50 label identifiers"})
		} else {
			c.LabelIDs = slices.Compact(slices.Sorted(slices.Values(ids)))
		}
	}
	return c, errs
}
