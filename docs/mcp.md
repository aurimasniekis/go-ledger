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
- `unconfirm_payment_match` takes back a confirmation right after it was made: the match is a suggestion again. Only the
  person who confirmed it may (a token counts as its owner), and only while nothing changed since
  ([ADR-0045](decisions/0045-unconfirm-and-match-provenance.md)); otherwise `match_undo_not_decider` or
  `match_undo_window_closed`, and unmatch is the way back. Every match change made through MCP records `via: "mcp"`.
- Every change to a match takes its `version`; a stale one is a `version_conflict` naming the current version. Read the
  match again and decide again; never resend the old change blindly.
- An overpayment is refused (`occurrence_overpaid`) unless the person has said the payment really is larger and the call
  sets `allow_overpayment`.
- `run_payment_matching` runs the engine now. Automation never rejects, unmatches or reverses what a person decided.
- The bank's description and counterparty are `null` (`bank_text_redacted`) unless the token holds `banking:read`.
- `create_commitment` takes an optional `match_tolerance` (how far short of the amount a booked payment may fall and
  still be confirmed automatically; never above the amount).

## Bank connections

`list_bank_applications`, `list_bank_institutions`, `list_bank_connections` and `get_bank_connection` read bank connections
([ADR-0040](decisions/0040-bank-connection-api-and-ui.md)); `refresh_bank_connection` fetches a connection's data now. They
need `connections:read` or `connections:write`, which are administrator-only grants: a token cannot hold them for a
read-only owner. Rules a client must keep:

- Check freshness before relying on a balance. After a failed refresh the stored figures stay, marked stale; an account
  with no reported balance shows none, never zero. Bank account numbers appear only as a four-character hint.
- `refresh_bank_connection` calls the bank, which limits how often it may be asked: use it on the person's request. A
  refresh that failed is a successful call; read `last_refresh_run.error.kind`. `reconnect_required` means only a new
  consent helps (retrying cannot); `retryable` and `rate_limited` mean try later. If `refresh_running` is true the
  refresh continues in the background: read the connection again instead of refreshing again.
- Starting, renewing and disconnecting a connection are not tools. They need the person at the bank's consent page or end
  access to bank data, so they are done with `ledger bank connect|reconnect|disconnect` or the web UI. No tool returns or
  accepts a consent code, a provider session or bank credentials.

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

The monthly view: every occurrence of the month with its payment state, and exact totals per currency (due, paid, outstanding, overdue, overpaid, and the planned figures with unpaid deferral fees under their own currency). Each row's deferral carries missed and missed_reason (plan date passed: principal still owed, or a fee still planned). Currencies are never added together. It describes recorded commitments and recorded payments only; it is not a statement of money that is safe to spend. The freshness member says how current it is.

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

Create a recurring commitment. Its occurrences are generated from the schedule. The same validation as the HTTP API applies; field errors name the member that is wrong. Optionally sets match_tolerance for automatic payment matching and leeway (the grace the creditor allows, only as the person states it: never guess it, and use kind 'unknown' when they say they do not know; omit it when they said nothing). Not idempotent: calling it twice creates two commitments. If the answer is reported as too large to return, the commitment WAS created: find it with list_commitments instead of calling this again.

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
    "leeway": {
      "additionalProperties": false,
      "description": "Optional; omit when the person said nothing. What the person knows about the grace after a due date. kind unknown: they do not know. none: no grace. period: days (1 to 365) after each due date. deadline: until a fixed date. Never infer it from payment history or from the commitment's name.",
      "properties": {
        "conditions": {
          "description": "What the grace depends on.",
          "maxLength": 500,
          "type": [
            "string",
            "null"
          ]
        },
        "consequences": {
          "description": "Known fees or other consequences of paying late.",
          "maxLength": 500,
          "type": [
            "string",
            "null"
          ]
        },
        "days": {
          "description": "Only for kind period.",
          "maximum": 365,
          "minimum": 1,
          "type": [
            "integer",
            "null"
          ]
        },
        "kind": {
          "enum": [
            "unknown",
            "none",
            "period",
            "deadline"
          ],
          "type": "string"
        },
        "source": {
          "description": "Where the information comes from.",
          "maxLength": 200,
          "type": [
            "string",
            "null"
          ]
        },
        "until": {
          "description": "Only for kind deadline, YYYY-MM-DD.",
          "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$",
          "type": [
            "string",
            "null"
          ]
        }
      },
      "required": [
        "kind"
      ],
      "type": [
        "object",
        "null"
      ]
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

### `unconfirm_payment_match`

Take back a confirmation right after it was made, on the person's instruction: the match becomes a suggestion again, exactly as before, and stops counting as payment. Only the person who confirmed it may (this token counts as its owner); otherwise match_undo_not_decider. Only while the confirmation is still the match's latest change, the person's latest match decision, and nothing else changed the occurrence since; otherwise match_undo_window_closed, and unmatch_payment_match is the way to take it back. A match created confirmed cannot be unconfirmed (invalid_match_state).

- Kind: write
- Operation: `POST /api/v1/payment-matches/{id}/unconfirm`
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

### `confirm_payment_match_replacing`

Confirm a suggestion as the payment the person already recorded by hand ("this is the payment I recorded"): the listed manual payments of the match's occurrence are marked replaced by the match and stop counting, so the payment is not counted twice; they stay listed with replaced_by_match_id. Only on the person's instruction, for the records they name; never infer it from similar amounts or dates. Each record must be of the match's occurrence (manual_payment_other_occurrence), in its currency, not deleted and not already replaced (manual_payment_not_replaceable). An overpayment by what remains is refused unless allow_overpayment is set. If the bank re-reported a payment that an earlier match of this occurrence already replaces (the record is then not replaceable here), unmatch that earlier match first: it restores the record. Then confirm the new transaction as replacing it. Do not use allow_overpayment for that: it counts the restored record and the new match together, shown as overpaid. unconfirm_payment_match inside the undo window makes the records count again.

- Kind: write
- Operation: `POST /api/v1/payment-matches/{id}/confirm-replacing`
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
    "manual_payment_ids": {
      "description": "The manual payments of the match's occurrence that this bank payment is.",
      "items": {
        "description": "The manual payment identifier (26 upper-case characters).",
        "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
        "type": "string"
      },
      "maxItems": 20,
      "minItems": 1,
      "type": "array",
      "uniqueItems": true
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
    "manual_payment_ids"
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

### `list_deferral_plans`

List plans to pay outstanding occurrences later, oldest first, one page at a time, each with its type, planned date, explicit fees (each with its paid marker), the occurrence's original due date and amount, and the occurrence's live payment state (a plan never changes it: a deferred occurrence stays outstanding and, past its original due date, overdue). missed is true when the plan's date has passed while the occurrence is overdue and still owes something (missed_reason principal) or while a recorded fee is still planned, even when the principal is paid (missed_reason fee); it is derived when read, is not payment state and clears when the occurrence is paid and every fee is paid or removed, or the plan is revised or undone. Filters combine; a repeated filter accepts any of its values.

- Kind: read
- Operation: `GET /api/v1/deferral-plans`
- Grant: `commitments:read`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "commitment_id": {
      "description": "Only plans of these commitments' occurrences.",
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
    "limit": {
      "description": "Page size (default 50).",
      "maximum": 200,
      "minimum": 1,
      "type": "integer"
    },
    "occurrence_id": {
      "description": "Only plans of these occurrences.",
      "items": {
        "description": "The occurrence identifier (26 upper-case characters).",
        "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
        "type": "string"
      },
      "type": "array"
    },
    "planned_month": {
      "description": "Only plans whose planned date is in this month, such as 2026-11.",
      "pattern": "^[0-9]{4}-(0[1-9]|1[0-2])$",
      "type": "string"
    },
    "state": {
      "description": "active or undone.",
      "items": {
        "description": "",
        "enum": [
          "active",
          "undone"
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

### `get_deferral_plan`

Get one deferral plan, including its version (needed to revise or undo it), its adjustments with their paid markers, and missed and missed_reason (plan date passed: principal still owed, or a fee still planned).

- Kind: read
- Operation: `GET /api/v1/deferral-plans/{id}`
- Grant: `commitments:read`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "id": {
      "description": "The deferral plan identifier (26 upper-case characters).",
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

### `list_deferral_plan_history`

Every change of a deferral plan, oldest first: created, revised, adjustments added, removed, marked paid or unpaid, undone, each with the type and planned date after it, who made it and how, and the reason recorded with it.

- Kind: read
- Operation: `GET /api/v1/deferral-plans/{id}/history`
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
    "id": {
      "description": "The deferral plan identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "limit": {
      "description": "Page size (default 50).",
      "maximum": 200,
      "minimum": 1,
      "type": "integer"
    }
  },
  "required": [
    "id"
  ],
  "type": "object"
}
```

</details>

### `create_deferral_plan`

Record the person's plan to pay an outstanding occurrence on a later date, in the same month or a later one. It is NOT a payment: the occurrence keeps its due date and amount and stays outstanding and, past its due date, overdue. The monthly view then plans its outstanding amount in the planned date's month instead of its own, never in both. Refused for an occurrence that owes nothing (nothing_outstanding), that already has an active plan (deferral_plan_exists: revise it instead) or for a planned date not after the due date. Fees are never added automatically: record them with add_deferral_adjustment only when the person states them. Not idempotent while no plan exists.

- Kind: write
- Operation: `POST /api/v1/deferral-plans`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "occurrence_id": {
      "description": "The occurrence identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "plan_type": {
      "description": "personal: the person's own plan; creditor_agreed: a later date the creditor agreed to; documented_grace: a grace period the creditor documents, ending on planned_date. Never record creditor_agreed or documented_grace unless the person says the creditor agreed or documents it.",
      "enum": [
        "personal",
        "creditor_agreed",
        "documented_grace"
      ],
      "type": "string"
    },
    "planned_date": {
      "description": "The date the person plans to pay, YYYY-MM-DD, after the occurrence's due date.",
      "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$",
      "type": "string"
    },
    "reason": {
      "description": "Why, in 1 to 500 characters; optional (null for none).",
      "maxLength": 500,
      "minLength": 1,
      "type": [
        "string",
        "null"
      ]
    }
  },
  "required": [
    "occurrence_id",
    "plan_type",
    "planned_date"
  ],
  "type": "object"
}
```

</details>

### `revise_deferral_plan`

Change an active plan's type, planned date or reason (send only what changes; null clears the reason). The previous values stay in the plan's history. Refused once the plan was undone or the occurrence owes nothing.

- Kind: write
- Operation: `PATCH /api/v1/deferral-plans/{id}`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "id": {
      "description": "The deferral plan identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "plan_type": {
      "description": "personal: the person's own plan; creditor_agreed: a later date the creditor agreed to; documented_grace: a grace period the creditor documents, ending on planned_date. Never record creditor_agreed or documented_grace unless the person says the creditor agreed or documents it.",
      "enum": [
        "personal",
        "creditor_agreed",
        "documented_grace"
      ],
      "type": "string"
    },
    "planned_date": {
      "description": "The new planned date, YYYY-MM-DD, after the occurrence's due date.",
      "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$",
      "type": "string"
    },
    "reason": {
      "description": "Why, in 1 to 500 characters; optional (null for none).",
      "maxLength": 500,
      "minLength": 1,
      "type": [
        "string",
        "null"
      ]
    },
    "version": {
      "description": "The version of the deferral plan this change is based on, as returned by a read. A stale version is refused with a conflict that names the current one.",
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

### `undo_deferral_plan`

End an active plan: the occurrence's outstanding amount is planned in its own month again. Refused while a fee is still planned (deferral_adjustments_unpaid): mark it paid or remove it first. Nothing about the occurrence or its payments changes; the plan is kept as history and cannot be revised afterwards (a new plan can be made). Destructive; confirm with the person first.

- Kind: destructive
- Operation: `POST /api/v1/deferral-plans/{id}/undo`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "id": {
      "description": "The deferral plan identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "reason": {
      "description": "Why, in 1 to 500 characters; optional (null for none).",
      "maxLength": 500,
      "minLength": 1,
      "type": [
        "string",
        "null"
      ]
    },
    "version": {
      "description": "The version of the deferral plan this change is based on, as returned by a read. A stale version is refused with a conflict that names the current one.",
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

### `add_deferral_adjustment`

Record an explicit fee, interest or other charge the person states for an active plan, in the plan's currency. It adds to the planned total of the plan's month, under its own currency, and stays planned until it is marked paid (mark_deferral_adjustment_paid) or removed, even after the occurrence's principal is paid; it never changes the occurrence's amount and is never payment evidence for it. Never infer a fee. Not idempotent: calling it twice records two charges.

- Kind: write
- Operation: `POST /api/v1/deferral-plans/{id}/adjustments`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "amount": {
      "additionalProperties": false,
      "description": "The charge, greater than zero, in the plan's currency.",
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
      "description": "The deferral plan identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "kind": {
      "description": "fee, interest or other.",
      "enum": [
        "fee",
        "interest",
        "other"
      ],
      "type": "string"
    },
    "note": {
      "description": "Free-form note, up to 500 characters.",
      "maxLength": 500,
      "type": [
        "string",
        "null"
      ]
    },
    "version": {
      "description": "The version of the deferral plan this change is based on, as returned by a read. A stale version is refused with a conflict that names the current one.",
      "minimum": 1,
      "type": "integer"
    }
  },
  "required": [
    "id",
    "version",
    "kind",
    "amount"
  ],
  "type": "object"
}
```

</details>

### `remove_deferral_adjustment`

Remove an adjustment of an active plan; it is kept as history and no longer counts. Destructive; confirm with the person first.

- Kind: destructive
- Operation: `DELETE /api/v1/deferral-plans/{id}/adjustments/{adjustment_id}`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "adjustment_id": {
      "description": "The adjustment identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "id": {
      "description": "The deferral plan identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "version": {
      "description": "The version of the deferral plan this change is based on, as returned by a read. A stale version is refused with a conflict that names the current one.",
      "minimum": 1,
      "type": "integer"
    }
  },
  "required": [
    "id",
    "adjustment_id",
    "version"
  ],
  "type": "object"
}
```

</details>

### `mark_deferral_adjustment_paid`

Record that the person paid a fee, interest or other adjustment of an active plan, so it is no longer planned. Only on the person's statement. It is not payment evidence for the occurrence and does not touch its principal. Reversible with unmark_deferral_adjustment_paid; marking an already paid adjustment is refused (deferral_adjustment_paid).

- Kind: write
- Operation: `POST /api/v1/deferral-plans/{id}/adjustments/{adjustment_id}/paid`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "adjustment_id": {
      "description": "The adjustment identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "id": {
      "description": "The deferral plan identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "version": {
      "description": "The version of the deferral plan this change is based on, as returned by a read. A stale version is refused with a conflict that names the current one.",
      "minimum": 1,
      "type": "integer"
    }
  },
  "required": [
    "id",
    "adjustment_id",
    "version"
  ],
  "type": "object"
}
```

</details>

### `unmark_deferral_adjustment_paid`

Reverse mark_deferral_adjustment_paid: the adjustment is planned again. Refused when it is not marked paid (deferral_adjustment_not_paid).

- Kind: write
- Operation: `POST /api/v1/deferral-plans/{id}/adjustments/{adjustment_id}/unpaid`
- Grant: `commitments:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "adjustment_id": {
      "description": "The adjustment identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "id": {
      "description": "The deferral plan identifier (26 upper-case characters).",
      "pattern": "^[0-9A-HJKMNP-TV-Z]{26}$",
      "type": "string"
    },
    "version": {
      "description": "The version of the deferral plan this change is based on, as returned by a read. A stale version is refused with a conflict that names the current one.",
      "minimum": 1,
      "type": "integer"
    }
  },
  "required": [
    "id",
    "adjustment_id",
    "version"
  ],
  "type": "object"
}
```

</details>

### `list_bank_applications`

List the configured bank-data provider applications with their mode (app or developer: the provider's own terms, not production versus sandbox), environment, which accounts they can reach and what they support. A capability of zero means not known, never none.

- Kind: read
- Operation: `GET /api/v1/bank-applications`
- Grant: `connections:read`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {},
  "type": "object"
}
```

</details>

### `list_bank_institutions`

Ask the provider which banks the named application offers in a country. A read; the provider may be slow or unavailable (the error says whether to retry later).

- Kind: read
- Operation: `GET /api/v1/bank-applications/{application}/institutions`
- Grant: `connections:read`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "application": {
      "description": "The application name, as returned by list_bank_applications.",
      "type": "string"
    },
    "country": {
      "description": "ISO 3166-1 alpha-2 code in capitals, such as DE.",
      "pattern": "^[A-Z]{2}$",
      "type": "string"
    }
  },
  "required": [
    "application",
    "country"
  ],
  "type": "object"
}
```

</details>

### `list_bank_connections`

List bank connections, oldest first, one page at a time, each with its status, consent validity, accounts, latest balances and data freshness (when data was last fetched successfully, whether it is stale, and why a refresh failed). Check freshness before relying on a balance: stored figures stay visible after a failed refresh, labelled as from the last success. An account with no reported balance shows none, never zero. Bank account numbers are shown only as a four-character hint.

- Kind: read
- Operation: `GET /api/v1/bank-connections`
- Grant: `connections:read`

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
    }
  },
  "type": "object"
}
```

</details>

### `get_bank_connection`

Get one bank connection with its accounts, balances, freshness, last refresh run and scheduler state. While refresh_running is true a refresh is still going on in the background: read it again later.

- Kind: read
- Operation: `GET /api/v1/bank-connections/{id}`
- Grant: `connections:read`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "id": {
      "description": "The bank connection identifier (26 upper-case characters).",
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

### `refresh_bank_connection`

Fetch the connection's accounts, balances and transactions from the bank now, only on the person's request: it calls the bank, which limits how often it may be asked. A refresh that failed still answers successfully: read last_refresh_run.error.kind and the accounts' freshness. reconnect_required means only a new consent helps (the person must reconnect with the CLI or web UI; retrying cannot work); retryable and rate_limited mean try later; stored data is never changed by a failure. If refresh_running is true the refresh outlived the wait and continues in the background: read the connection again with get_bank_connection instead of calling this again. A connection refreshed within the cooldown (banking.refresh.request_cooldown, default 5 minutes) is refused with HTTP 429 refresh_cooldown and a retry-after wait: do not retry before it; the stored data and refresh_available_at on get_bank_connection say what is current.

- Kind: write
- Operation: `POST /api/v1/bank-connections/{id}/refresh`
- Grant: `connections:write`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "id": {
      "description": "The bank connection identifier (26 upper-case characters).",
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

### `list_exchange_rates`

List the exchange rates Ledger has cached, which its converted totals use, one page at a time, ordered by provider, source and pair with the newest rate date first. Each has the provider, the source (ECB or blended: rates of different sources are never mixed), the pair, the exact value as published (a string, never rounded), the rate date (the day it is for; a provider publishes on working days only), when Ledger retrieved it and whether it is stale (not retrieved for 5 days, or dated more than 5 days ago). A stale rate is never used in a converted total. It reads the local cache and never calls the rate provider.

- Kind: read
- Operation: `GET /api/v1/rates`
- Grant: `rates:read`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "base": {
      "description": "Only rates from this currency.",
      "pattern": "^[A-Z]{3}$",
      "type": "string"
    },
    "cursor": {
      "description": "The next_cursor of the previous page; valid only with the same filters.",
      "type": "string"
    },
    "date[gte]": {
      "description": "Only rates dated on or after this date (YYYY-MM-DD).",
      "format": "date",
      "type": "string"
    },
    "date[lte]": {
      "description": "Only rates dated on or before this date (YYYY-MM-DD).",
      "format": "date",
      "type": "string"
    },
    "limit": {
      "description": "Page size (default 50).",
      "maximum": 200,
      "minimum": 1,
      "type": "integer"
    },
    "provider": {
      "description": "Only rates of this provider adapter, for example frankfurter.",
      "type": "string"
    },
    "quote": {
      "description": "Only rates to this currency.",
      "pattern": "^[A-Z]{3}$",
      "type": "string"
    },
    "source": {
      "description": "Only rates of this source key, for example ECB.",
      "type": "string"
    }
  },
  "type": "object"
}
```

</details>

### `get_exchange_rate_status`

Report whether an exchange-rate provider is configured (it is optional), which source it serves, the reporting currency rates are fetched to, the freshness policy (stale after 5 days by default) and how the last refresh went since Ledger started (outcome and the provider's error kind and code, never a URL). When the provider is off, cached rates stay listed but nothing refreshes them.

- Kind: read
- Operation: `GET /api/v1/rates/status`
- Grant: `rates:read`

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {},
  "type": "object"
}
```

</details>

<!-- tools:end -->
