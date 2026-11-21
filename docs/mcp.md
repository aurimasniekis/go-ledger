# MCP server

Ledger serves the [Model Context Protocol](https://modelcontextprotocol.io) so an agent can read the monthly view and
manage commitments with the same permissions as the Management API (ADR-0037). It is part of the product: it is not
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
the tool, its outcome and the token, and the operation itself as `token.write_used`. Calls are limited per token
(`mcp.requests_per_minute`, `mcp.burst`, `mcp.max_request_bytes`); `mcp.enabled: false` turns the endpoint off.

Annotations tell a client how careful to be: `readOnlyHint` tools never change data; a destructive tool is marked
`destructiveHint` and should be confirmed with the person first. Results are the API's JSON, as text and as structured
content. Amounts are decimal strings with a currency; currencies are never added together. The monthly view describes
recorded commitments and recorded payments only. It is not a statement of money that is safe to spend.

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

Create a recurring commitment. Its occurrences are generated from the schedule. The same validation as the HTTP API applies; field errors name the member that is wrong. Not idempotent: calling it twice creates two commitments.

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

<!-- tools:end -->
