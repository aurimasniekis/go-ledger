package decisions

import (
	"slices"
)

// Capability is what a task asks of a provider (control default extending
// D13).
type Capability string

// Capabilities.
const (
	// Decide is a closed-set choice or score over candidates the caller
	// offers (categorisation, labels, matching suggestions).
	Decide Capability = "decide"
	// Generate is free text (alert wording).
	Generate Capability = "generate"
)

// Valid reports whether c is a capability.
func (c Capability) Valid() bool { return c == Decide || c == Generate }

// OutputKind is the shape of a task's answer.
type OutputKind string

// Output kinds.
const (
	// OutputChoice is one candidate (or none) with a confidence.
	OutputChoice OutputKind = "choice"
	// OutputChoices is a set of candidates (possibly empty) with a confidence.
	OutputChoices OutputKind = "choices"
	// OutputText is a short free text.
	OutputText OutputKind = "text"
)

// TaskName names a decision task. It is also the purpose consent is given
// for: consent is per provider and per purpose.
type TaskName string

// Tasks (requirements: categorisation, label suggestions and alerts; the
// optional payment-matching suggestions of X3 use the same gate).
const (
	TaskCategorisation  TaskName = "categorisation"
	TaskLabelSuggestion TaskName = "label_suggestion"
	TaskAlerts          TaskName = "alerts"
	TaskPaymentMatching TaskName = "payment_matching"
)

// Task describes one decision task.
type Task struct {
	Name       TaskName
	Capability Capability
	Output     OutputKind
	// AutoApplyAllowed: decision D13 allows opt-in auto-apply for
	// categorisation only; every other task is review only.
	AutoApplyAllowed bool
	// MaxCandidates is the most candidates one request may offer (decide).
	MaxCandidates int
	// MaxChoices is the most candidates one answer may pick (OutputChoices).
	MaxChoices int
	// MaxText is the longest answer in characters (OutputText).
	MaxText int
}

var registry = []Task{
	{Name: TaskCategorisation, Capability: Decide, Output: OutputChoice, AutoApplyAllowed: true, MaxCandidates: 200},
	{Name: TaskLabelSuggestion, Capability: Decide, Output: OutputChoices, MaxCandidates: 200, MaxChoices: 5},
	{Name: TaskAlerts, Capability: Generate, Output: OutputText, MaxText: 280},
	{Name: TaskPaymentMatching, Capability: Decide, Output: OutputChoice, MaxCandidates: 50},
}

// Tasks returns the registry, in a fixed order.
func Tasks() []Task { return slices.Clone(registry) }

// TaskNames returns every task name.
func TaskNames() []TaskName {
	out := make([]TaskName, len(registry))
	for i, t := range registry {
		out[i] = t.Name
	}
	return out
}

// LookupTask returns the task named n.
func LookupTask(n TaskName) (Task, bool) {
	for _, t := range registry {
		if t.Name == n {
			return t, true
		}
	}
	return Task{}, false
}
