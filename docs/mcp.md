# MCP server

Ledger serves the [Model Context Protocol](https://modelcontextprotocol.io) so an agent can read the monthly view and
manage commitments with the same permissions as the Management API (ADR-0038). It is part of the product: it is not
configured as a tool of the workspace that builds Ledger.

## Connect

- **HTTP (streamable)**: `POST /api/v1/mcp` on the main listener, with `Authorization: Bearer <API token>`. The
  transport is stateless with JSON answers; there is no session and GET is 405. Create a token with the grants the agent
  should have and nothing more (`ledger token create --grant commitments:read ...`, or the web UI); revoking it
  stops the agent at its next request.
- **stdio** for local agents that start a command: `ledger mcp stdio`. It calls a running Ledger server
  (`--url`, `LEDGER_URL`, default http://127.0.0.1:8080) with the token from `--token-file`, `LEDGER_TOKEN` or the client
  configuration, exactly like `ledger commitments list`. Example client entry (Claude Desktop style):

```json
{ "mcpServers": { "ledger": { "command": "ledger", "args": ["mcp", "stdio"],
  "env": { "LEDGER_URL": "http://127.0.0.1:8080", "LEDGER_TOKEN": "<token>" } } } }
```

  Prefer `--token-file` over putting the token in a configuration file other people can read.

## What a call can do

A tool runs one API operation as the token: the same authentication, grant, validation and version checks as a direct
call. Without the grant a tool returns an error naming it (for example `commitments:write`); read the `ledger://me`
resource to see what the token holds. Every call of a write or destructive tool is audited as `mcp.tool_called` with
the tool, its outcome and the token, and the operation itself as `token.write_used`. A write through `ledger mcp stdio`
is recorded as `token.write_used` and as `mcp.tool_called` with outcome `requested`. `requested` means only that the
client asked for that tool: the tool name is the client's own claim, and the entry is written before the operation has
answered, so it says nothing about success. What happened is in the operation's own audit entries
(`token.write_used` and the object's, such as `payment_match.confirmed`), and a denied stdio write leaves no entry. An
answer too large to return is not a failure either: the tool says the operation was done and names the resource, so
read it instead of calling a write tool again. JSON-RPC batches are refused (400): send one message per request. Calls are limited per token
(`mcp.requests_per_minute`, `mcp.burst`, `mcp.max_request_bytes`); `mcp.enabled: false` turns the endpoint off.

Annotations tell a client how careful to be: `readOnlyHint` tools never change data; a destructive tool is marked
`destructiveHint` and should be confirmed with the person first. Results are the API's JSON, as text and as structured
content. Amounts are decimal strings with a currency; currencies are never added together. The monthly view describes
recorded commitments and recorded payments only. It is not a statement of money that is safe to spend.

## Payment matching and manual payments

The match tools expose the review queue and the decisions of [ADR-0035](decisions/0035-payment-matches-and-corrections.md)
and the engine of [ADR-0039](decisions/0039-automatic-payment-matching.md). The rules a client must keep:

- `list_payment_matches` with `state: ["suggested"]` is the review queue. A suggestion never counts as payment. A
  similarity score in its reason is a hint, never proof; an ambiguous or pending suggestion is for the person to decide.
- Confirm, correct, create and record tools change what counts as paid: call them only on the person's instruction.
  `reject_payment_match`, `unmatch_payment_match`, `reverse_payment_match` and `delete_manual_payment` are marked
  destructive: reject, unmatch and reverse are final (the pair stays closed and is not suggested again), so confirm with
  the person first.
- Every change to a match takes its `version`; a stale one is a `version_conflict` naming the current version. Read the
  match again and decide again; never resend the old change blindly.
- An overpayment is refused (`occurrence_overpaid`) unless the person has said the payment really is larger and the call
  sets `allow_overpayment`.
- `run_payment_matching` runs the engine now. Automation never rejects, unmatches or reverses what a person decided.
- The bank's description and counterparty are `null` (`bank_text_redacted`) unless the token holds `banking:read`.
- `create_commitment` takes an optional `match_tolerance` (how far short of the amount a booked payment may fall and
  still be confirmed automatically; never above the amount).

## Resources

| URI | Content |
| --- | --- |
| `ledger://me` | the token's owner, role and effective grants |
| `ledger://grants` | every grant and role of this Ledger |
| `ledger://openapi` | the OpenAPI description every tool maps to |

None contains a secret.

## Tools

This section is generated from the registry (`UPDATE_DOCS=1 go test ./internal/mcpserver -run TestDocsListEveryTool`).

<!-- tools:start -->

### `list_commitments`

List recurring commitment definitions (name, kind, amount with currency, schedule, dates, priority), oldest first, one page at a time. Filters combine; a repeated filter accepts any of its values.

- Kind: read
- Operation: `GET /api/v1/commitments`
- Grant: `commitments:read`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "currency": {
      "description": "Only these currencies (ISO 4217).",
      "items": {
        "pattern": "^[A-Z]{3}$",
        "type": "string"
      },
      "type": "array"
    },
    "cursor": {
      "description": "The next_cursor of the previous page; valid only with the same filters.",
      "type": "string"
    },
    "kind": {
      "description": "Only these kinds.",
      "items": {
        "description": "",
        "enum": [
          "service",
          "server",
          "domain",
          "subscription",
          "debt_repayment"
        ],
        "type": "string"
      },
      "type": "array"
    },
    "limit": {
      "description": "Page size (default 50).",
      "maximum": 200,
      "minimum": 1,
      "type": "integer"
    },
    "priority": {
      "description": "Only these priorities.",
      "items": {
        "description": "",
        "enum": [
          "essential",
          "important",
          "flexible"
        ],
        "type": "string"
      },
      "type": "array"
    }
  },
  "type": "object"
}
```

</details>

### `get_commitment`

Get one commitment definition, including its version (needed to change or delete it).

- Kind: read
- Operation: `GET /api/v1/commitments/{id}`
- Grant: `commitments:read`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "id": {
      "description": "The commitment identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    }
  },
  "required": [
    "id"
  ],
  "type": "object"
}
```

</details>

### `list_occurrences`

List the dated occurrences of recurring commitments in one reporting month, each with its derived payment state (paid, upcoming, overdue ...). Payment state comes from recorded manual payments and confirmed matches, not from a guess. Cancelled occurrences are left out unless asked for. Months more than 60 months ahead are refused.

- Kind: read
- Operation: `GET /api/v1/occurrences`
- Grant: `commitments:read`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "commitment_id": {
      "description": "Only occurrences of these commitments.",
      "items": {
        "description": "The commitment identifier (26 upper-case characters).",
        "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
        "type": "string"
      },
      "type": "array"
    },
    "cursor": {
      "description": "The next_cursor of the previous page; valid only with the same filters.",
      "type": "string"
    },
    "include_cancelled": {
      "description": "Include cancelled occurrences (default false).",
      "type": "boolean"
    },
    "limit": {
      "description": "Page size (default 50).",
      "maximum": 200,
      "minimum": 1,
      "type": "integer"
    },
    "month": {
      "description": "The reporting month, such as 2026-11.",
      "pattern": "^[0-9]{4}-(0[1-9]|1[0-2])$",
      "type": "string"
    }
  },
  "required": [
    "month"
  ],
  "type": "object"
}
```

</details>

### `get_month_summary`

The monthly view: every occurrence of the month with its payment state, and exact totals per currency (due, paid, outstanding, overdue, overpaid). Currencies are never added together. It describes recorded commitments and recorded payments only; it is not a statement of money that is safe to spend. The freshness member says how current it is.

- Kind: read
- Operation: `GET /api/v1/occurrences/summary`
- Grant: `commitments:read`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "month": {
      "description": "The reporting month, such as 2026-11.",
      "pattern": "^[0-9]{4}-(0[1-9]|1[0-2])$",
      "type": "string"
    }
  },
  "required": [
    "month"
  ],
  "type": "object"
}
```

</details>

### `get_occurrence`

Get one occurrence with its derived payment state.

- Kind: read
- Operation: `GET /api/v1/occurrences/{id}`
- Grant: `commitments:read`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "id": {
      "description": "The occurrence identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    }
  },
  "required": [
    "id"
  ],
  "type": "object"
}
```

</details>

### `create_commitment`

Create a recurring commitment. Its occurrences are generated from the schedule. The same validation as the HTTP API applies; field errors name the member that is wrong. Optionally sets match_tolerance for automatic payment matching. Not idempotent: calling it twice creates two commitments. If the answer is reported as too large to return, the commitment WAS created: find it with list_commitments instead of calling this again.

- Kind: write
- Operation: `POST /api/v1/commitments`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "amount": {
      "additionalProperties": false,
      "description": "The amount due each time, in one currency.",
      "properties": {
        "amount": {
          "description": "A decimal string such as \"12.50\", never a number.",
          "pattern": "^[0-9]+(\\.[0-9]+)?$",
          "type": "string"
        },
        "currency": {
          "description": "ISO 4217 code, three upper-case letters.",
          "pattern": "^[A-Z]{3}$",
          "type": "string"
        }
      },
      "required": [
        "amount",
        "currency"
      ],
      "type": "object"
    },
    "end_date": {
      "description": "Last date it applies, YYYY-MM-DD; omit for none.",
      "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$",
      "type": [
        "string",
        "null"
      ]
    },
    "kind": {
      "description": "What kind of commitment it is.",
      "enum": [
        "service",
        "server",
        "domain",
        "subscription",
        "debt_repayment"
      ],
      "type": "string"
    },
    "match_tolerance": {
      "description": "Optional, in the commitment's currency; omit or null for none. How far a booked bank payment may fall short of amount and still be confirmed automatically. A payment above amount is never confirmed automatically.",
      "oneOf": [
        {
          "additionalProperties": false,
          "properties": {
            "amount": {
              "description": "A decimal string such as \"12.50\", never a number.",
              "pattern": "^[0-9]+(\\.[0-9]+)?$",
              "type": "string"
            },
            "currency": {
              "description": "ISO 4217 code, three upper-case letters.",
              "pattern": "^[A-Z]{3}$",
              "type": "string"
            }
          },
          "required": [
            "amount",
            "currency"
          ],
          "type": "object"
        },
        {
          "type": "null"
        }
      ]
    },
    "name": {
      "description": "Display name.",
      "minLength": 1,
      "type": "string"
    },
    "notes": {
      "description": "Free-form notes.",
      "type": [
        "string",
        "null"
      ]
    },
    "priority": {
      "description": "essential, important or flexible; omit for none.",
      "enum": [
        "essential",
        "important",
        "flexible",
        null
      ],
      "type": [
        "string",
        "null"
      ]
    },
    "schedule": {
      "description": "When it falls due (ADR-0024). A day past the end of a month falls on that month's last day.",
      "oneOf": [
        {
          "additionalProperties": false,
          "properties": {
            "day": {
              "maximum": 31,
              "minimum": 1,
              "type": "integer"
            },
            "frequency": {
              "const": "monthly"
            },
            "interval": {
              "description": "Months between due dates (1 = every month, 3 = quarterly).",
              "maximum": 120,
              "minimum": 1,
              "type": "integer"
            },
            "version": {
              "const": 1
            }
          },
          "required": [
            "version",
            "frequency",
            "interval",
            "day"
          ],
          "type": "object"
        },
        {
          "additionalProperties": false,
          "properties": {
            "day": {
              "maximum": 31,
              "minimum": 1,
              "type": "integer"
            },
            "frequency": {
              "const": "annual"
            },
            "month": {
              "maximum": 12,
              "minimum": 1,
              "type": "integer"
            },
            "version": {
              "const": 1
            }
          },
          "required": [
            "version",
            "frequency",
            "month",
            "day"
          ],
          "type": "object"
        }
      ]
    },
    "start_date": {
      "description": "First date the commitment applies, YYYY-MM-DD.",
      "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$",
      "type": "string"
    }
  },
  "required": [
    "name",
    "kind",
    "amount",
    "schedule",
    "start_date"
  ],
  "type": "object"
}
```

</details>

### `delete_commitment`

Delete a commitment and its unpaid future occurrences. Refused while the commitment is in use (it has payment history). Destructive and not undoable: confirm with the person first.

- Kind: destructive
- Operation: `DELETE /api/v1/commitments/{id}`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "id": {
      "description": "The commitment identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "version": {
      "description": "The version of the commitment this change is based on, as returned by a read. A stale version is refused with a conflict that names the current one.",
      "minimum": 1,
      "type": "integer"
    }
  },
  "required": [
    "id",
    "version"
  ],
  "type": "object"
}
```

</details>

### `list_payment_matches`

List links between bank transactions and commitment occurrences, one page at a time. state=suggested is the review queue: proposals that do not count as payment until a person confirms them. Each match keeps apart the bank fact (transaction), who proposed it (origin), the decision (state, decided_by) and whether it counts as payment now (counts_as_evidence). A similarity score in a reason is a hint, never proof. The bank's description and counterparty are null (bank_text_redacted) unless the token holds banking:read.

- Kind: read
- Operation: `GET /api/v1/payment-matches`
- Grant: `commitments:read`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "cursor": {
      "description": "The next_cursor of the previous page; valid only with the same filters.",
      "type": "string"
    },
    "limit": {
      "description": "Page size (default 50).",
      "maximum": 200,
      "minimum": 1,
      "type": "integer"
    },
    "occurrence_id": {
      "description": "Only matches of these occurrences.",
      "items": {
        "description": "The occurrence identifier (26 upper-case characters).",
        "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
        "type": "string"
      },
      "type": "array"
    },
    "origin": {
      "description": "Only these origins.",
      "items": {
        "description": "",
        "enum": [
          "auto_rule",
          "model_suggestion",
          "user"
        ],
        "type": "string"
      },
      "type": "array"
    },
    "state": {
      "description": "Only these states.",
      "items": {
        "description": "",
        "enum": [
          "suggested",
          "confirmed",
          "rejected",
          "reversed"
        ],
        "type": "string"
      },
      "type": "array"
    },
    "transaction_id": {
      "description": "Only matches of these bank transactions.",
      "items": {
        "description": "The bank transaction identifier (26 upper-case characters).",
        "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
        "type": "string"
      },
      "type": "array"
    }
  },
  "type": "object"
}
```

</details>

### `get_payment_match`

Get one payment match with its history, including its version (needed for every change).

- Kind: read
- Operation: `GET /api/v1/payment-matches/{id}`
- Grant: `commitments:read`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "id": {
      "description": "The payment match identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    }
  },
  "required": [
    "id"
  ],
  "type": "object"
}
```

</details>

### `create_payment_match`

Record the person's own confirmed match: a booked, outgoing bank transaction pays an occurrence, in the same currency. It counts as payment at once, so use it only on the person's instruction. Refused for a pending transaction, a transaction already fully allocated, or an overpayment unless allow_overpayment is set. Not idempotent: a second call for the same pair is refused as duplicate_match.

- Kind: write
- Operation: `POST /api/v1/payment-matches`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "allow_overpayment": {
      "description": "Set true only when the person has said the payment really is larger than the amount due. Without it an overpayment is refused (occurrence_overpaid).",
      "type": "boolean"
    },
    "amount": {
      "additionalProperties": false,
      "description": "How much of the transaction pays this occurrence, in the transaction's currency.",
      "properties": {
        "amount": {
          "description": "A decimal string such as \"12.50\", never a number.",
          "pattern": "^[0-9]+(\\.[0-9]+)?$",
          "type": "string"
        },
        "currency": {
          "description": "ISO 4217 code, three upper-case letters.",
          "pattern": "^[A-Z]{3}$",
          "type": "string"
        }
      },
      "required": [
        "amount",
        "currency"
      ],
      "type": "object"
    },
    "occurrence_id": {
      "description": "The occurrence identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "reason": {
      "description": "Why, in 1 to 500 characters; kept on the match and in its history. Optional.",
      "type": "string"
    },
    "transaction_id": {
      "description": "The bank transaction identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    }
  },
  "required": [
    "transaction_id",
    "occurrence_id",
    "amount"
  ],
  "type": "object"
}
```

</details>

### `confirm_payment_match`

Confirm a suggestion: from then on it counts as payment. Only on the person's instruction; the model that suggested a match, a similarity score or an amount that looks right never justifies it. Refused while the transaction is pending, and an overpayment is refused unless allow_overpayment is set. A stale version is refused with version_conflict naming the current one.

- Kind: write
- Operation: `POST /api/v1/payment-matches/{id}/confirm`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "allow_overpayment": {
      "description": "Set true only when the person has said the payment really is larger than the amount due. Without it an overpayment is refused (occurrence_overpaid).",
      "type": "boolean"
    },
    "id": {
      "description": "The payment match identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "reason": {
      "description": "Why, in 1 to 500 characters; kept on the match and in its history. Optional.",
      "type": "string"
    },
    "version": {
      "description": "The version of the payment match this change is based on, as returned by a read. A stale version is refused with a conflict that names the current one.",
      "minimum": 1,
      "type": "integer"
    }
  },
  "required": [
    "id",
    "version"
  ],
  "type": "object"
}
```

</details>

### `correct_payment_match`

Change how much of the transaction pays the occurrence, on a suggested or confirmed match. What it links, its currency and origin never change: for a different target, unmatch and create a new match. A confirmed match is checked again (over-allocation, overpayment).

- Kind: write
- Operation: `POST /api/v1/payment-matches/{id}/correct`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "allow_overpayment": {
      "description": "Set true only when the person has said the payment really is larger than the amount due. Without it an overpayment is refused (occurrence_overpaid).",
      "type": "boolean"
    },
    "amount": {
      "additionalProperties": false,
      "description": "The new allocated amount, in the match's currency.",
      "properties": {
        "amount": {
          "description": "A decimal string such as \"12.50\", never a number.",
          "pattern": "^[0-9]+(\\.[0-9]+)?$",
          "type": "string"
        },
        "currency": {
          "description": "ISO 4217 code, three upper-case letters.",
          "pattern": "^[A-Z]{3}$",
          "type": "string"
        }
      },
      "required": [
        "amount",
        "currency"
      ],
      "type": "object"
    },
    "id": {
      "description": "The payment match identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "reason": {
      "description": "Why, in 1 to 500 characters; kept on the match and in its history. Optional.",
      "type": "string"
    },
    "version": {
      "description": "The version of the payment match this change is based on, as returned by a read. A stale version is refused with a conflict that names the current one.",
      "minimum": 1,
      "type": "integer"
    }
  },
  "required": [
    "id",
    "version",
    "amount"
  ],
  "type": "object"
}
```

</details>

### `reject_payment_match`

Reject a suggestion: it is not the payment for that occurrence. Final: the pair is closed and automatic matching will not suggest it again. Destructive; confirm with the person first.

- Kind: destructive
- Operation: `POST /api/v1/payment-matches/{id}/reject`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "id": {
      "description": "The payment match identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "reason": {
      "description": "Why, in 1 to 500 characters; kept on the match and in its history. Optional.",
      "type": "string"
    },
    "version": {
      "description": "The version of the payment match this change is based on, as returned by a read. A stale version is refused with a conflict that names the current one.",
      "minimum": 1,
      "type": "integer"
    }
  },
  "required": [
    "id",
    "version"
  ],
  "type": "object"
}
```

</details>

### `unmatch_payment_match`

Take back a confirmed match that was wrong: the occurrence stops counting this transaction as payment. Final: the pair is closed and automatic matching will not suggest it again. Destructive; confirm with the person first.

- Kind: destructive
- Operation: `POST /api/v1/payment-matches/{id}/unmatch`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "id": {
      "description": "The payment match identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "reason": {
      "description": "Why, in 1 to 500 characters; kept on the match and in its history. Optional.",
      "type": "string"
    },
    "version": {
      "description": "The version of the payment match this change is based on, as returned by a read. A stale version is refused with a conflict that names the current one.",
      "minimum": 1,
      "type": "integer"
    }
  },
  "required": [
    "id",
    "version"
  ],
  "type": "object"
}
```

</details>

### `reverse_payment_match`

Record that a confirmed match was right but the payment was reversed (bounced, refunded, returned): the occurrence reopens and the history of both decisions is kept. Final. Destructive; confirm with the person first.

- Kind: destructive
- Operation: `POST /api/v1/payment-matches/{id}/reverse`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "id": {
      "description": "The payment match identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "reason": {
      "description": "Why, in 1 to 500 characters; kept on the match and in its history. Optional.",
      "type": "string"
    },
    "version": {
      "description": "The version of the payment match this change is based on, as returned by a read. A stale version is refused with a conflict that names the current one.",
      "minimum": 1,
      "type": "integer"
    }
  },
  "required": [
    "id",
    "version"
  ],
  "type": "object"
}
```

</details>

### `run_payment_matching`

Run the matching engine now (it also runs after every bank refresh). It confirms a booked payment automatically only when every safeguard passes (exact amount or within the commitment's match_tolerance, close to the due date, no ambiguity); everything else becomes a suggestion for a person. It never rejects, unmatches or reverses a decision a person made. Returns the counts; re-running with unchanged data changes nothing. Its automatic confirmations count as payment and can be undone with unmatch_payment_match.

- Kind: write
- Operation: `POST /api/v1/payment-matching/runs`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {},
  "type": "object"
}
```

</details>

### `record_manual_payment`

Mark an occurrence as paid, fully or in part, by recording a payment the person made (cash, a card the bank does not report). Counts as payment at once; use it only on the person's instruction. Same currency as the occurrence. Not idempotent: calling it twice records two payments. If the answer is reported as too large to return, the payment WAS recorded: read the occurrence instead of calling this again.

- Kind: write
- Operation: `POST /api/v1/occurrences/{id}/manual-payments`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "amount": {
      "additionalProperties": false,
      "description": "The amount paid, in the occurrence's currency.",
      "properties": {
        "amount": {
          "description": "A decimal string such as \"12.50\", never a number.",
          "pattern": "^[0-9]+(\\.[0-9]+)?$",
          "type": "string"
        },
        "currency": {
          "description": "ISO 4217 code, three upper-case letters.",
          "pattern": "^[A-Z]{3}$",
          "type": "string"
        }
      },
      "required": [
        "amount",
        "currency"
      ],
      "type": "object"
    },
    "id": {
      "description": "The occurrence identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "note": {
      "description": "Free-form note.",
      "type": [
        "string",
        "null"
      ]
    },
    "paid_on": {
      "description": "The date it was paid, YYYY-MM-DD.",
      "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$",
      "type": "string"
    }
  },
  "required": [
    "id",
    "amount",
    "paid_on"
  ],
  "type": "object"
}
```

</details>

### `delete_manual_payment`

Remove a manual payment record (the undo of record_manual_payment): the occurrence stops counting it. The record is kept as history. Destructive; confirm with the person first.

- Kind: destructive
- Operation: `DELETE /api/v1/occurrences/{id}/manual-payments/{payment_id}`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "id": {
      "description": "The occurrence identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "payment_id": {
      "description": "The manual payment identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "version": {
      "description": "The version of the manual payment this change is based on, as returned by a read. A stale version is refused with a conflict that names the current one.",
      "minimum": 1,
      "type": "integer"
    }
  },
  "required": [
    "id",
    "payment_id",
    "version"
  ],
  "type": "object"
}
```

</details>

<!-- tools:end -->
