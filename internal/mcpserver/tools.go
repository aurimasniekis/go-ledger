package mcpserver

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
)

// Kind classifies what a tool does, for the MCP tool annotations and for
// audit: only [KindRead] tools are left out of the audit log.
type Kind int

// Tool kinds.
const (
	// KindRead tools never change state (readOnlyHint).
	KindRead Kind = iota + 1
	// KindWrite tools change state without removing anything.
	KindWrite
	// KindDestructive tools remove or overwrite data (destructiveHint). Clients
	// should ask the person before calling them.
	KindDestructive
)

func (k Kind) String() string {
	switch k {
	case KindRead:
		return "read"
	case KindWrite:
		return "write"
	case KindDestructive:
		return "destructive"
	}
	return "invalid"
}

// Tool is one MCP tool: a name, a description, an input schema and the API
// operation it maps to. A tool has no logic of its own: its arguments become
// the path, query, body and version of the operation, which is run through
// the same route as a direct HTTP call (ADR-0037). The grant is the grant of
// that route; a test compares the two so they cannot drift.
type Tool struct {
	Name        string
	Title       string
	Description string
	// Method and Path name the operation, relative to /api/v1, in the form
	// the route is registered: Path may hold {name} placeholders that are
	// filled from arguments of the same name.
	Method string
	Path   string
	Grant  authz.Grant
	Kind   Kind
	// Schema is the JSON Schema of the arguments (an object).
	Schema map[string]any
}

// object builds an object schema that refuses unknown members.
func object(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func enum[T ~string](desc string, vs []T) map[string]any {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = string(v)
	}
	return map[string]any{"type": "string", "enum": out, "description": desc}
}

func idArg(what string) map[string]any {
	return map[string]any{"type": "string", "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$", "description": "The " + what + " identifier (26 upper-case characters)."}
}

func versionArg(what string) map[string]any {
	return map[string]any{"type": "integer", "minimum": 1,
		"description": "The version of the " + what + " this change is based on, as returned by a read. A stale version is refused with a conflict that names the current one."}
}

func pagingArgs(props map[string]any) map[string]any {
	props["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "Page size (default 50)."}
	props["cursor"] = str("The next_cursor of the previous page; valid only with the same filters.")
	return props
}

var monthPattern = "^[0-9]{4}-(0[1-9]|1[0-2])$"

func monthArg(desc string) map[string]any {
	return map[string]any{"type": "string", "pattern": monthPattern, "description": desc}
}

func moneyArg() map[string]any {
	return object(map[string]any{
		"amount":   map[string]any{"type": "string", "pattern": "^[0-9]+(\\.[0-9]+)?$", "description": "A decimal string such as \"12.50\", never a number."},
		"currency": map[string]any{"type": "string", "pattern": "^[A-Z]{3}$", "description": "ISO 4217 code, three upper-case letters."},
	}, "amount", "currency")
}

func withDescription(s map[string]any, desc string) map[string]any {
	s["description"] = desc
	return s
}

var grantCommitmentsRead = authz.G(authz.ResourceCommitments, authz.ActionRead)
var grantCommitmentsWrite = authz.G(authz.ResourceCommitments, authz.ActionWrite)

func registry() []Tool {
	return []Tool{
		{
			Name: "list_commitments", Title: "List commitments", Method: "GET", Path: "/commitments",
			Grant: grantCommitmentsRead, Kind: KindRead,
			Description: "List recurring commitment definitions (name, kind, amount with currency, schedule, dates, priority), oldest first, one page at a time. Filters combine; a repeated filter accepts any of its values.",
			Schema: object(pagingArgs(map[string]any{
				"kind":     map[string]any{"type": "array", "items": enum("", commitments.Kinds()), "description": "Only these kinds."},
				"priority": map[string]any{"type": "array", "items": enum("", commitments.Priorities()), "description": "Only these priorities."},
				"currency": map[string]any{"type": "array", "items": map[string]any{"type": "string", "pattern": "^[A-Z]{3}$"}, "description": "Only these currencies (ISO 4217)."},
			})),
		},
		{
			Name: "get_commitment", Title: "Get a commitment", Method: "GET", Path: "/commitments/{id}",
			Grant: grantCommitmentsRead, Kind: KindRead,
			Description: "Get one commitment definition, including its version (needed to change or delete it).",
			Schema:      object(map[string]any{"id": idArg("commitment")}, "id"),
		},
		{
			Name: "list_occurrences", Title: "List occurrences of a month", Method: "GET", Path: "/occurrences",
			Grant: grantCommitmentsRead, Kind: KindRead,
			Description: "List the dated occurrences of recurring commitments in one reporting month, each with its derived payment state (paid, upcoming, overdue ...). Payment state comes from recorded manual payments and confirmed matches, not from a guess. Cancelled occurrences are left out unless asked for. Months more than 60 months ahead are refused.",
			Schema: object(pagingArgs(map[string]any{
				"month":             monthArg("The reporting month, such as 2026-11."),
				"commitment_id":     map[string]any{"type": "array", "items": idArg("commitment"), "description": "Only occurrences of these commitments."},
				"include_cancelled": map[string]any{"type": "boolean", "description": "Include cancelled occurrences (default false)."},
			}), "month"),
		},
		{
			Name: "get_month_summary", Title: "Get the monthly view", Method: "GET", Path: "/occurrences/summary",
			Grant: grantCommitmentsRead, Kind: KindRead,
			Description: "The monthly view: every occurrence of the month with its payment state, and exact totals per currency (due, paid, outstanding, overdue, overpaid). Currencies are never added together. It describes recorded commitments and recorded payments only; it is not a statement of money that is safe to spend. The freshness member says how current it is.",
			Schema:      object(map[string]any{"month": monthArg("The reporting month, such as 2026-11.")}, "month"),
		},
		{
			Name: "get_occurrence", Title: "Get an occurrence", Method: "GET", Path: "/occurrences/{id}",
			Grant: grantCommitmentsRead, Kind: KindRead,
			Description: "Get one occurrence with its derived payment state.",
			Schema:      object(map[string]any{"id": idArg("occurrence")}, "id"),
		},
		{
			Name: "create_commitment", Title: "Create a commitment", Method: "POST", Path: "/commitments",
			Grant: grantCommitmentsWrite, Kind: KindWrite,
			Description: "Create a recurring commitment. Its occurrences are generated from the schedule. The same validation as the HTTP API applies; field errors name the member that is wrong. Not idempotent: calling it twice creates two commitments.",
			Schema: object(map[string]any{
				"name":       map[string]any{"type": "string", "minLength": 1, "description": "Display name."},
				"kind":       enum("What kind of commitment it is.", commitments.Kinds()),
				"amount":     withDescription(moneyArg(), "The amount due each time, in one currency."),
				"schedule":   scheduleSchema(),
				"start_date": map[string]any{"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$", "description": "First date the commitment applies, YYYY-MM-DD."},
				"end_date":   map[string]any{"type": []string{"string", "null"}, "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$", "description": "Last date it applies, YYYY-MM-DD; omit for none."},
				"priority":   map[string]any{"type": []string{"string", "null"}, "enum": priorityNames(), "description": "essential, important or flexible; omit for none."},
				"notes":      map[string]any{"type": []string{"string", "null"}, "description": "Free-form notes."},
			}, "name", "kind", "amount", "schedule", "start_date"),
		},
		{
			Name: "delete_commitment", Title: "Delete a commitment", Method: "DELETE", Path: "/commitments/{id}",
			Grant: grantCommitmentsWrite, Kind: KindDestructive,
			Description: "Delete a commitment and its unpaid future occurrences. Refused while the commitment is in use (it has payment history). Destructive and not undoable: confirm with the person first.",
			Schema:      object(map[string]any{"id": idArg("commitment"), "version": versionArg("commitment")}, "id", "version"),
		},
	}
}

func priorityNames() []any {
	var out []any
	for _, p := range commitments.Priorities() {
		out = append(out, string(p))
	}
	return append(out, nil)
}

func scheduleSchema() map[string]any {
	return map[string]any{
		"description": "When it falls due (ADR-0024). A day past the end of a month falls on that month's last day.",
		"oneOf": []any{
			object(map[string]any{
				"version":   map[string]any{"const": 1},
				"frequency": map[string]any{"const": "monthly"},
				"interval":  map[string]any{"type": "integer", "minimum": 1, "maximum": 120, "description": "Months between due dates (1 = every month, 3 = quarterly)."},
				"day":       map[string]any{"type": "integer", "minimum": 1, "maximum": 31},
			}, "version", "frequency", "interval", "day"),
			object(map[string]any{
				"version":   map[string]any{"const": 1},
				"frequency": map[string]any{"const": "annual"},
				"month":     map[string]any{"type": "integer", "minimum": 1, "maximum": 12},
				"day":       map[string]any{"type": "integer", "minimum": 1, "maximum": 31},
			}, "version", "frequency", "month", "day"),
		},
	}
}

// Tools returns the tool registry, in listing order. The slice is a copy.
func Tools() []Tool { return registry() }

// ToolByName returns the named tool.
func ToolByName(name string) (Tool, bool) {
	for _, t := range registry() {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Doc renders the registry as Markdown (docs/mcp.md is generated from it).
func Doc() string {
	var b strings.Builder
	for _, t := range registry() {
		fmt.Fprintf(&b, "### `%s`\n\n%s\n\n- Kind: %s\n- Operation: `%s /api/v1%s`\n- Grant: `%s`\n", t.Name, t.Description, t.Kind, t.Method, t.Path, t.Grant)
		schema, _ := json.MarshalIndent(t.Schema, "", "  ")
		fmt.Fprintf(&b, "\n<details><summary>Input schema</summary>\n\n```json\n%s\n```\n\n</details>\n\n", schema)
	}
	return b.String()
}
