package decisions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxOutputBytes is the largest answer accepted.
const MaxOutputBytes = 16 << 10

// Decision is a validated answer, with candidate keys mapped back to the
// caller's identifiers.
type Decision struct {
	// Choice is the chosen candidate's identifier (OutputChoice); nil means
	// the model chose none.
	Choice *string `json:"choice,omitempty"`
	// Choices are the chosen candidates' identifiers (OutputChoices).
	Choices []string `json:"choices,omitempty"`
	// Text is the answer of a generate task (OutputText).
	Text string `json:"text,omitempty"`
	// Confidence is the model's own confidence, a decimal from 0 to 1 kept as
	// text (never a float); empty for text. It is not proof of anything.
	Confidence string `json:"confidence,omitempty"`
}

// OutputError is an answer that failed validation. Reason never quotes the
// answer (it may echo request content).
type OutputError struct{ Reason string }

func (e *OutputError) Error() string { return "invalid model output: " + e.Reason }

var (
	keyPattern        = regexp.MustCompile(`^c[1-9][0-9]{0,3}$`)
	confidencePattern = regexp.MustCompile(`^(0(\.[0-9]{1,6})?|1(\.0{1,6})?)$`)
)

// Schema returns the task's output schema (JSON Schema 2020-12). It is static
// per task and holds no user data: candidates are opaque keys.
func Schema(t Task) json.RawMessage {
	conf := map[string]any{"type": "number", "minimum": 0, "maximum": 1}
	key := map[string]any{"type": "string", "pattern": keyPattern.String()}
	var props map[string]any
	var required []string
	switch t.Output {
	case OutputChoice:
		props = map[string]any{"choice": map[string]any{"anyOf": []any{key, map[string]any{"type": "null"}}}, "confidence": conf}
		required = []string{"choice", "confidence"}
	case OutputChoices:
		props = map[string]any{"choices": map[string]any{"type": "array", "items": key, "uniqueItems": true, "maxItems": t.MaxChoices}, "confidence": conf}
		required = []string{"choices", "confidence"}
	case OutputText:
		props = map[string]any{"text": map[string]any{"type": "string", "minLength": 1, "maxLength": t.MaxText}}
		required = []string{"text"}
	}
	b, err := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required})
	if err != nil {
		panic(err)
	}
	return b
}

type rawAnswer struct {
	Choice     json.RawMessage `json:"choice"`
	Choices    *[]string       `json:"choices"`
	Text       *string         `json:"text"`
	Confidence json.RawMessage `json:"confidence"`
}

// Validate checks a raw answer against the task's schema and the options the
// request offered, and maps keys back through ids (key to identifier).
func Validate(t Task, raw []byte, ids map[string]string) (Decision, error) {
	bad := func(format string, a ...any) (Decision, error) {
		return Decision{}, &OutputError{Reason: fmt.Sprintf(format, a...)}
	}
	if len(raw) == 0 {
		return bad("empty")
	}
	if len(raw) > MaxOutputBytes {
		return bad("larger than %d bytes", MaxOutputBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var a rawAnswer
	if err := dec.Decode(&a); err != nil {
		return bad("not a JSON object of the task's schema")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return bad("trailing data after the object")
	}
	var d Decision
	if t.Output == OutputChoice || t.Output == OutputChoices {
		if a.Text != nil {
			return bad("unexpected member text")
		}
		if a.Confidence == nil {
			return bad("confidence is required")
		}
		// A JSON number only, written plainly: a string, an exponent or a
		// value outside 0 to 1 is refused.
		if !confidencePattern.Match(a.Confidence) {
			return bad("confidence must be a number from 0 to 1 with at most 6 decimals")
		}
		d.Confidence = string(a.Confidence)
	}
	switch t.Output {
	case OutputChoice:
		if a.Choices != nil {
			return bad("unexpected member choices")
		}
		if a.Choice == nil {
			return bad("choice is required")
		}
		if string(a.Choice) == "null" {
			return d, nil
		}
		var k string
		if err := json.Unmarshal(a.Choice, &k); err != nil {
			return bad("choice must be a candidate key or null")
		}
		id, ok := ids[k]
		if !ok || !keyPattern.MatchString(k) {
			return bad("choice is not one of the candidates offered")
		}
		d.Choice = &id
	case OutputChoices:
		if a.Choice != nil {
			return bad("unexpected member choice")
		}
		if a.Choices == nil {
			return bad("choices is required")
		}
		if len(*a.Choices) > t.MaxChoices {
			return bad("more than %d choices", t.MaxChoices)
		}
		seen := map[string]bool{}
		d.Choices = []string{}
		for _, k := range *a.Choices {
			id, ok := ids[k]
			if !ok || !keyPattern.MatchString(k) {
				return bad("a choice is not one of the candidates offered")
			}
			if seen[k] {
				return bad("a choice is repeated")
			}
			seen[k] = true
			d.Choices = append(d.Choices, id)
		}
	case OutputText:
		if a.Choice != nil || a.Choices != nil || a.Confidence != nil {
			return bad("only text is expected")
		}
		if a.Text == nil {
			return bad("text is required")
		}
		s := strings.TrimSpace(*a.Text)
		switch {
		case s == "":
			return bad("text is empty")
		case !utf8.ValidString(s):
			return bad("text is not valid UTF-8")
		case utf8.RuneCountInString(s) > t.MaxText:
			return bad("text is longer than %d characters", t.MaxText)
		case strings.IndexFunc(s, func(r rune) bool { return r != '\n' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) }) >= 0:
			return bad("text contains control or invisible formatting characters")
		}
		d.Text = s
	default:
		return bad("the task has no output kind")
	}
	return d, nil
}
