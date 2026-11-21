package mcpserver

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
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
// the same route as a direct HTTP call (ADR-0038). The grant is the grant of
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
	// IfMatchArg names the top-level argument sent as the If-Match version
	// (such as "version"); empty when the operation takes none. It must be a
	// required integer argument and is not part of the body.
	IfMatchArg string
	Kind       Kind
	// OpenWorld marks a tool whose call reaches an external provider (a bank),
	// reported as the openWorldHint annotation.
	OpenWorld bool
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

var grantConnectionsRead = authz.G(authz.ResourceConnections, authz.ActionRead)
var grantConnectionsWrite = authz.G(authz.ResourceConnections, authz.ActionWrite)
var grantCommitmentsRead = authz.G(authz.ResourceCommitments, authz.ActionRead)
var grantCommitmentsWrite = authz.G(authz.ResourceCommitments, authz.ActionWrite)

func registry() []Tool {
	return append(append(coreTools(), matchTools()...), bankTools()...)
}

func coreTools() []Tool {
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
			Description: "Create a recurring commitment. Its occurrences are generated from the schedule. The same validation as the HTTP API applies; field errors name the member that is wrong. Optionally sets match_tolerance for automatic payment matching. Not idempotent: calling it twice creates two commitments. If the answer is reported as too large to return, the commitment WAS created: find it with list_commitments instead of calling this again.",
			Schema: object(map[string]any{
				"name":       map[string]any{"type": "string", "minLength": 1, "description": "Display name."},
				"kind":       enum("What kind of commitment it is.", commitments.Kinds()),
				"amount":     withDescription(moneyArg(), "The amount due each time, in one currency."),
				"schedule":   scheduleSchema(),
				"start_date": map[string]any{"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$", "description": "First date the commitment applies, YYYY-MM-DD."},
				"end_date":   map[string]any{"type": []string{"string", "null"}, "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$", "description": "Last date it applies, YYYY-MM-DD; omit for none."},
				"priority":   map[string]any{"type": []string{"string", "null"}, "enum": priorityNames(), "description": "essential, important or flexible; omit for none."},
				"notes":      map[string]any{"type": []string{"string", "null"}, "description": "Free-form notes."},
				"match_tolerance": map[string]any{"oneOf": []any{moneyArg(), map[string]any{"type": "null"}},
					"description": "Optional, in the commitment's currency; omit or null for none. How far a booked bank payment may fall short of amount and still be confirmed automatically. A payment above amount is never confirmed automatically."},
			}, "name", "kind", "amount", "schedule", "start_date"),
		},
		{
			Name: "delete_commitment", Title: "Delete a commitment", Method: "DELETE", Path: "/commitments/{id}",
			Grant: grantCommitmentsWrite, IfMatchArg: "version", Kind: KindDestructive,
			Description: "Delete a commitment and its unpaid future occurrences. Refused while the commitment is in use (it has payment history). Destructive and not undoable: confirm with the person first.",
			Schema:      object(map[string]any{"id": idArg("commitment"), "version": versionArg("commitment")}, "id", "version"),
		},
	}
}

// matchTools are the payment-matching tools (task X4, ADR-0035, ADR-0039). The similarity score in a suggestion's
// reason is a hint, never proof of a payment, and only a person decides: write tools must be used on the person's
// instruction, never to settle an ambiguous suggestion on their behalf.
func matchTools() []Tool {
	reason := str("Why, in 1 to 500 characters; kept on the match and in its history. Optional.")
	allow := map[string]any{"type": "boolean", "description": "Set true only when the person has said the payment really is larger than the amount due. Without it an overpayment is refused (occurrence_overpaid)."}
	action := func(name, title, path, desc string, kind Kind, extra map[string]any, required ...string) Tool {
		props := map[string]any{"id": idArg("payment match"), "version": versionArg("payment match"), "reason": reason}
		for k, v := range extra {
			props[k] = v
		}
		return Tool{Name: name, Title: title, Method: "POST", Path: path, Grant: grantCommitmentsWrite, IfMatchArg: "version", Kind: kind, Description: desc,
			Schema: object(props, append([]string{"id", "version"}, required...)...)}
	}
	return []Tool{
		{
			Name: "list_payment_matches", Title: "List payment matches", Method: "GET", Path: "/payment-matches",
			Grant: grantCommitmentsRead, Kind: KindRead,
			Description: "List links between bank transactions and commitment occurrences, one page at a time. state=suggested is the review queue: proposals that do not count as payment until a person confirms them. Each match keeps apart the bank fact (transaction), who proposed it (origin), the decision (state, decided_by) and whether it counts as payment now (counts_as_evidence). A similarity score in a reason is a hint, never proof. The bank's description and counterparty are null (bank_text_redacted) unless the token holds banking:read.",
			Schema: object(pagingArgs(map[string]any{
				"state":          map[string]any{"type": "array", "items": enum("", matches.States()), "description": "Only these states."},
				"origin":         map[string]any{"type": "array", "items": enum("", matches.Origins()), "description": "Only these origins."},
				"occurrence_id":  map[string]any{"type": "array", "items": idArg("occurrence"), "description": "Only matches of these occurrences."},
				"transaction_id": map[string]any{"type": "array", "items": idArg("bank transaction"), "description": "Only matches of these bank transactions."},
			})),
		},
		{
			Name: "get_payment_match", Title: "Get a payment match", Method: "GET", Path: "/payment-matches/{id}",
			Grant: grantCommitmentsRead, Kind: KindRead,
			Description: "Get one payment match with its history, including its version (needed for every change).",
			Schema:      object(map[string]any{"id": idArg("payment match")}, "id"),
		},
		{
			Name: "create_payment_match", Title: "Match a transaction to an occurrence", Method: "POST", Path: "/payment-matches",
			Grant: grantCommitmentsWrite, Kind: KindWrite,
			Description: "Record the person's own confirmed match: a booked, outgoing bank transaction pays an occurrence, in the same currency. It counts as payment at once, so use it only on the person's instruction. Refused for a pending transaction, a transaction already fully allocated, or an overpayment unless allow_overpayment is set. Not idempotent: a second call for the same pair is refused as duplicate_match.",
			Schema: object(map[string]any{
				"transaction_id":    idArg("bank transaction"),
				"occurrence_id":     idArg("occurrence"),
				"amount":            withDescription(moneyArg(), "How much of the transaction pays this occurrence, in the transaction's currency."),
				"allow_overpayment": allow,
				"reason":            reason,
			}, "transaction_id", "occurrence_id", "amount"),
		},
		action("confirm_payment_match", "Confirm a suggested match", "/payment-matches/{id}/confirm",
			"Confirm a suggestion: from then on it counts as payment. Only on the person's instruction; the model that suggested a match, a similarity score or an amount that looks right never justifies it. Refused while the transaction is pending, and an overpayment is refused unless allow_overpayment is set. A stale version is refused with version_conflict naming the current one.",
			KindWrite, map[string]any{"allow_overpayment": allow}),
		action("correct_payment_match", "Correct a match's amount", "/payment-matches/{id}/correct",
			"Change how much of the transaction pays the occurrence, on a suggested or confirmed match. What it links, its currency and origin never change: for a different target, unmatch and create a new match. A confirmed match is checked again (over-allocation, overpayment).",
			KindWrite, map[string]any{"amount": withDescription(moneyArg(), "The new allocated amount, in the match's currency."), "allow_overpayment": allow}, "amount"),
		action("reject_payment_match", "Reject a suggested match", "/payment-matches/{id}/reject",
			"Reject a suggestion: it is not the payment for that occurrence. Final: the pair is closed and automatic matching will not suggest it again. Destructive; confirm with the person first.",
			KindDestructive, nil),
		action("unmatch_payment_match", "Unmatch a confirmed match", "/payment-matches/{id}/unmatch",
			"Take back a confirmed match that was wrong: the occurrence stops counting this transaction as payment. Final: the pair is closed and automatic matching will not suggest it again. Destructive; confirm with the person first.",
			KindDestructive, nil),
		action("unconfirm_payment_match", "Undo a confirmation", "/payment-matches/{id}/unconfirm",
			"Take back a confirmation right after it was made, on the person's instruction: the match becomes a suggestion again, exactly as before, and stops counting as payment. Only the person who confirmed it may (this token counts as its owner); otherwise match_undo_not_decider. Only while the confirmation is still the match's latest change, the person's latest match decision, and nothing else changed the occurrence since; otherwise match_undo_window_closed, and unmatch_payment_match is the way to take it back. A match created confirmed cannot be unconfirmed (invalid_match_state).",
			KindWrite, nil),
		action("confirm_payment_match_replacing", "Confirm a match as the payment already recorded", "/payment-matches/{id}/confirm-replacing",
			"Confirm a suggestion as the payment the person already recorded by hand (\"this is the payment I recorded\"): the listed manual payments of the match's occurrence are marked replaced by the match and stop counting, so the payment is not counted twice; they stay listed with replaced_by_match_id. Only on the person's instruction, for the records they name; never infer it from similar amounts or dates. Each record must be of the match's occurrence (manual_payment_other_occurrence), in its currency, not deleted and not already replaced (manual_payment_not_replaceable). An overpayment by what remains is refused unless allow_overpayment is set. unconfirm_payment_match inside the undo window makes the records count again.",
			KindWrite, map[string]any{
				"manual_payment_ids": map[string]any{"type": "array", "items": idArg("manual payment"), "minItems": 1, "maxItems": 20, "uniqueItems": true,
					"description": "The manual payments of the match's occurrence that this bank payment is."},
				"allow_overpayment": allow,
			}, "manual_payment_ids"),
		action("reverse_payment_match", "Reverse a confirmed match", "/payment-matches/{id}/reverse",
			"Record that a confirmed match was right but the payment was reversed (bounced, refunded, returned): the occurrence reopens and the history of both decisions is kept. Final. Destructive; confirm with the person first.",
			KindDestructive, nil),
		{
			Name: "run_payment_matching", Title: "Run automatic payment matching", Method: "POST", Path: "/payment-matching/runs",
			Grant: grantCommitmentsWrite, Kind: KindWrite,
			Description: "Run the matching engine now (it also runs after every bank refresh). It confirms a booked payment automatically only when every safeguard passes (exact amount or within the commitment's match_tolerance, close to the due date, no ambiguity); everything else becomes a suggestion for a person. It never rejects, unmatches or reverses a decision a person made. Returns the counts; re-running with unchanged data changes nothing. Its automatic confirmations count as payment and can be undone with unmatch_payment_match.",
			Schema:      object(map[string]any{}),
		},
		{
			Name: "record_manual_payment", Title: "Record a manual payment", Method: "POST", Path: "/occurrences/{id}/manual-payments",
			Grant: grantCommitmentsWrite, Kind: KindWrite,
			Description: "Mark an occurrence as paid, fully or in part, by recording a payment the person made (cash, a card the bank does not report). Counts as payment at once; use it only on the person's instruction. Same currency as the occurrence. Not idempotent: calling it twice records two payments. If the answer is reported as too large to return, the payment WAS recorded: read the occurrence instead of calling this again.",
			Schema: object(map[string]any{
				"id":      idArg("occurrence"),
				"amount":  withDescription(moneyArg(), "The amount paid, in the occurrence's currency."),
				"paid_on": map[string]any{"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$", "description": "The date it was paid, YYYY-MM-DD."},
				"note":    map[string]any{"type": []string{"string", "null"}, "description": "Free-form note."},
			}, "id", "amount", "paid_on"),
		},
		{
			Name: "delete_manual_payment", Title: "Delete a manual payment", Method: "DELETE", Path: "/occurrences/{id}/manual-payments/{payment_id}",
			Grant: grantCommitmentsWrite, IfMatchArg: "version", Kind: KindDestructive,
			Description: "Remove a manual payment record (the undo of record_manual_payment): the occurrence stops counting it. The record is kept as history. Destructive; confirm with the person first.",
			Schema: object(map[string]any{
				"id":         idArg("occurrence"),
				"payment_id": idArg("manual payment"),
				"version":    versionArg("manual payment"),
			}, "id", "payment_id", "version"),
		},
	}
}

// bankTools are the bank connection tools (task B4f, ADR-0040). The grants are administrator-only
// (connections:read|write). Only reads and refresh are exposed: starting, renewing and ending a connection involve
// the person at the bank's consent page or end access to bank data, so they are done with the CLI or the web UI.
// Nothing returned holds a provider session, an authorisation code, an account handle or a full account number.
func bankTools() []Tool {
	return []Tool{
		{
			Name: "list_bank_applications", Title: "List bank provider applications", Method: "GET", Path: "/bank-applications",
			Grant: grantConnectionsRead, Kind: KindRead,
			Description: "List the configured bank-data provider applications with their mode (app or developer: the provider's own terms, not production versus sandbox), environment, which accounts they can reach and what they support. A capability of zero means not known, never none.",
			Schema:      object(map[string]any{}),
		},
		{
			Name: "list_bank_institutions", Title: "List banks an application offers", Method: "GET", Path: "/bank-applications/{application}/institutions",
			Grant: grantConnectionsRead, Kind: KindRead, OpenWorld: true,
			Description: "Ask the provider which banks the named application offers in a country. A read; the provider may be slow or unavailable (the error says whether to retry later).",
			Schema: object(map[string]any{
				"application": str("The application name, as returned by list_bank_applications."),
				"country":     map[string]any{"type": "string", "pattern": "^[A-Z]{2}$", "description": "ISO 3166-1 alpha-2 code in capitals, such as DE."},
			}, "application", "country"),
		},
		{
			Name: "list_bank_connections", Title: "List bank connections", Method: "GET", Path: "/bank-connections",
			Grant: grantConnectionsRead, Kind: KindRead,
			Description: "List bank connections, oldest first, one page at a time, each with its status, consent validity, accounts, latest balances and data freshness (when data was last fetched successfully, whether it is stale, and why a refresh failed). Check freshness before relying on a balance: stored figures stay visible after a failed refresh, labelled as from the last success. An account with no reported balance shows none, never zero. Bank account numbers are shown only as a four-character hint.",
			Schema:      object(pagingArgs(map[string]any{})),
		},
		{
			Name: "get_bank_connection", Title: "Get a bank connection", Method: "GET", Path: "/bank-connections/{id}",
			Grant: grantConnectionsRead, Kind: KindRead,
			Description: "Get one bank connection with its accounts, balances, freshness, last refresh run and scheduler state. While refresh_running is true a refresh is still going on in the background: read it again later.",
			Schema:      object(map[string]any{"id": idArg("bank connection")}, "id"),
		},
		{
			Name: "refresh_bank_connection", Title: "Refresh a bank connection now", Method: "POST", Path: "/bank-connections/{id}/refresh",
			Grant: grantConnectionsWrite, Kind: KindWrite, OpenWorld: true,
			Description: "Fetch the connection's accounts, balances and transactions from the bank now, only on the person's request: it calls the bank, which limits how often it may be asked. A refresh that failed still answers successfully: read last_refresh_run.error.kind and the accounts' freshness. reconnect_required means only a new consent helps (the person must reconnect with the CLI or web UI; retrying cannot work); retryable and rate_limited mean try later; stored data is never changed by a failure. If refresh_running is true the refresh outlived the wait and continues in the background: read the connection again with get_bank_connection instead of calling this again.",
			Schema:      object(map[string]any{"id": idArg("bank connection")}, "id"),
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
