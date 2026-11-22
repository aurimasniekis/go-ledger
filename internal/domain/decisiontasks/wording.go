package decisiontasks

import (
	"context"

	"github.com/aurimasniekis/go-ledger/internal/domain/alerts"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
)

// AlertWording writes optional wording for in-app alerts with the alerts
// decision task (a generate task). It sends only Ledger's own alert message
// (its wording of the facts), never bank text, and the answer is attached to
// the alert as labelled model text: it never replaces the message.
type AlertWording struct {
	runner Runner
	sugg   Suggestions
}

// NewAlertWording returns an alerts.Wordsmith.
func NewAlertWording(r Runner, s Suggestions) *AlertWording { return &AlertWording{runner: r, sugg: s} }

var _ alerts.Wordsmith = (*AlertWording)(nil)

// Word implements alerts.Wordsmith. A failure of any kind (disabled task, no
// consent, outage, invalid output) is "no wording", not an error.
func (w *AlertWording) Word(ctx context.Context, a alerts.Alert) (alerts.Wording, bool, error) {
	res, err := w.runner.Run(ctx, decisions.TaskAlerts, decisions.Input{
		Object: decisions.ObjectRef{Type: "alert", ID: a.ID},
		Values: map[decisions.Field]string{decisions.FieldDescription: a.Message},
	})
	if err != nil {
		return alerts.Wording{}, false, err
	}
	sg := res.Suggestion
	if sg == nil {
		return alerts.Wording{}, false, nil
	}
	// The text is attached to the alert, so the suggestion row has done its
	// job and leaves the review queue.
	if _, err := w.sugg.Decide(ctx, SystemActor, sg.ID, decisions.StateSuperseded, ""); err != nil {
		return alerts.Wording{}, false, err
	}
	if sg.Decision.Text == "" {
		return alerts.Wording{}, false, nil
	}
	return alerts.Wording{Text: sg.Decision.Text, Provider: sg.Provider, Model: sg.Model}, true, nil
}
