# ADR-0038: MCP server core (task P-MCP1)

Status: Proposed. Defaults (rate limits, request size, stateless transport) are proposals, not operator decisions.

## Context

Requirements ask for an MCP server on streamable HTTP at one endpoint of the main API, bearer API tokens with
exact grants, tools that map to real API operations and reuse their validation and authorisation, JSON Schemas,
read / write / destructive separation, read-only resources without secrets, an audit entry for every state-changing
call with the token identity, request and rate limits, and a stdio mode for local agents. ADR-0001 and ADR-0011 already say the MCP server "speaks
HTTP" to the Management API; ADR-0021 point 6 says tools declare the grant of the operation they map to.

## Decision

1. **SDK.** `github.com/modelcontextprotocol/go-sdk` **v1.8.0**, the official SDK (research R11, with its review).
   v1.9.0 exists only as pre-releases and is not used. It needs Go 1.25 (the repository is on 1.27), builds with
   `CGO_ENABLED=0` (`make build` passes), and the modules this change adds to go.mod are github.com/google/jsonschema-go v0.4.3, github.com/segmentio/asm
   v1.1.3, github.com/segmentio/encoding v0.5.4, github.com/yosida95/uritemplate/v3 v3.0.2, golang.org/x/sync v0.24.0
   and golang.org/x/time v0.16.0 (the last five indirect); none needs cgo. (go.sum also lists golang-jwt/jwt/v5 and
   google/go-cmp for the SDK's module graph; they are not linked into the binary unless `go list -deps` says so.
   Licences of the transitive modules are not yet checked one by one: OP4's NOTICE must do that.) Licence: the SDK is moving from MIT to Apache-2.0 and its `LICENSE` says both apply to
   different contributions; both are permissive and compatible with Apache-2.0. A third-party notice for it belongs in
   the NOTICE file of task OP4 (this repository has none yet). Upgrades are deliberate: bump the version, run the MCP tests.
2. **A tool is a typed call of an API operation.** `internal/mcpserver` holds a registry: name, description, input JSON
   Schema, method and path, grant, kind. A call validates its arguments against the schema, maps them onto the path
   (`{id}`), `If-Match` (the `version` argument), the query of a read or the JSON body of a write, and runs that request
   through the **server's own handler** with the caller's bearer token. So authentication from current data (revocation
   and a disabled owner apply to the next call), the route's grant check, the domain validation, optimistic locking, the
   D29 `banking:read` redaction and the D32/D34 token rules are the ones of a direct HTTP call, by construction, with no copy
   to keep in step. Alternatives rejected: calling domain services from the tools (a second copy of request decoding
   and validation, and of every later rule) and one SDK server per token (not needed).
3. **Grants are checked twice, on purpose.** The tool declares the route's grant. In the server the principal that the
   route already authenticated is in the context, and `Principal.Allows` is checked first so a denied call gives a clear
   error and touches nothing; the route checks again when the call runs. A parity test compares each tool's grant with its
   route's, and calls every tool as tokens with different grants (including a write grant cut down by a read-only owner),
   asserting the MCP outcome equals the HTTP outcome.
4. **Transport.** `POST /api/v1/mcp`, registered with the new access rule `httpapi.TokenOnly()` (`x-ledger-access: token`): an
   API token is required and sessions get 403; no grant at route level; documented in OpenAPI. The route itself is not
   audited as `token.write_used` (every JSON-RPC message is a POST, reads included): the audited step is the inner write.
   The SDK handler runs **stateless with JSON answers**: each request stands alone, so no session can outlive a revoked
   token and no stream is needed; GET and DELETE are 405. The SDK's DNS-rebinding check is off because it rejects any
   reverse proxy that forwards the public Host header to a loopback listener; it adds nothing here since every request
   needs a token a browser page does not have. **JSON-RPC batches are refused with 400** before the SDK handler: a batch
   would carry many tool calls in one request and get around the per-token rate limit (the SDK accepts batches from a
   client that sends no protocol version; MCP 2025-06-18 removed them).
   Cross-site browser requests are refused with the standard library's
   `http.CrossOriginProtection` (Sec-Fetch-Site and Origin), which the SDK does not apply itself.
5. **Errors.** A permission or API failure is a tool result with `isError` and structured `error` content (a code and a
   message, and the API's error body as detail), so the model sees it and can act (for example name the missing
   grant). Protocol errors are left to the SDK. A missing grant never reveals more than the grant name. The tool list
   is the same for every token: listing everything and denying at call time keeps documentation and clients
   predictable; each description and `_meta` names the grant.
6. **Audit.** Every call of a write or destructive tool writes `mcp.tool_called` (tool and outcome `succeeded`, `failed` or
   `denied`; never arguments or results) naming the token and its owner, whatever the outcome, including calls rejected by
   the schema. The operation itself still writes `token.write_used` **before** it runs and refuses to run if that cannot be
   recorded, so a write cannot happen unaudited. If the final `mcp.tool_called` entry cannot be written the failure is logged;
   the earlier entry exists. Read tools are not audited, like HTTP reads.
7. **Resources.** `ledger://me` (the token's identity and effective grants), `ledger://grants` (the vocabulary) and
   `ledger://openapi` (the API description), each read through the same route, so they carry no secrets and need only a
   valid token.
8. **Limits.** Per token, in memory: a token bucket (`mcp.requests_per_minute` 120, `mcp.burst` 30) answering 429 `rate_limited`
   with Retry-After; `mcp.max_request_bytes` 256 KiB (413), on top of `http.max_body_bytes`; the HTTP server's timeouts and
   graceful shutdown apply. `mcp.enabled` (default true) turns the endpoint into a 404 for token holders.
9. **stdio.** `ledger mcp stdio` serves the same registry on standard input and output as a **client of a running server**
   (URL and token found like the management commands do, new `ledgerclient.Client.Raw`). It does not open the database,
   so it works while `ledger serve` holds the instance lock, and the server authenticates, authorises, validates and audits
   every call. A stdio write is audited as `token.write_used` and, because the client names the tool in an
   `X-Ledger-MCP-Tool` header, also as `mcp.tool_called` with outcome `requested` (the client asked for the tool; it does not say the call succeeded; reworded in ADR-0043): the tool name is asserted by the
   client and the outcome is not known when the entry is written (the route's own result is not echoed). A denied stdio
   write leaves no entry, as for a direct HTTP call. The token never reaches stdout or stderr.
10. **First tools.** `list_commitments`, `get_commitment`, `list_occurrences`, `get_month_summary` and `get_occurrence` (read),
   `create_commitment` (write), `delete_commitment` (destructive; exercises the annotation and `If-Match`). P-MCP2 adds the
   rest; `docs/mcp.md` lists the registry and a test keeps it current.

## Consequences

- Adding a tool is a registry entry and a row in the parity test's sample table; its validation and grants come from the
  route. A tool with no matching route fails the parity test.
- An MCP call costs one in-process HTTP round trip and shows in the access log and metrics as its inner request
  (the outer `POST /api/v1/mcp` is logged too).
- Revocation, demotion and disabling take effect on the next MCP request, with no session to clean up.
- Not verified: MCP clients from third parties (only the SDK's own client is used in tests); the 2026-07-28 `cacheScope`
  default of list results (tool lists are the same for every token, so a public scope is accurate); behaviour behind a
  real reverse proxy; very large result pages (the API's page limit of 200 bounds them).
- Open for the operator: rate and size defaults; whether tool
  lists should be filtered by grant.
