# ADR-0062: Organising merchants: UI and MCP

Status: Accepted (task O5, 2026-11-22). Builds on ADR-0053, ADR-0057, ADR-0058 and ADR-0061. It replaces ADR-0057 point 2 (a
merchant is not an MCP object type, and the UI has no merchant assignment).

## Decision

1. **A merchant is an object type like the others.** Assignments key on the merchant record's id (ADR-0058); the routes
   and their grants are unchanged: `/organisation/merchant/{id}` needs `organisation:read|write` and, because merchant
   names derive from bank text, the administrator-only `merchants:read|write` as well.
2. **MCP.** `object_type` of `get_object_organisation`, `set_object_category`, `reset_object_category`, `add_object_label`
   and `remove_object_label` accepts `merchant`; `object_id` is then the merchant record id. A tool carries
   `ObjectGrants`: the extra grant an object type needs (`merchants:read` for the read, `merchants:write` for the changes).
   The server checks it before the call, so a denied call touches nothing and names the missing grant, and the route checks it
   again (`TestMCPHTTPGrantParity` still compares each tool's own grant with its route; the merchant test checks each
   missing grant). The tool descriptions and `docs/mcp.md` state it. Bucket tools still take commitments and accounts only.
3. **Go client.** The five object operations already took any object type; a test covers `merchant`. A CLI command per
   operation exists (O4); nothing is added there.
4. **UI.** The Organise disclosure takes `objectType="merchant"`: it appears on `/merchants/<id>` ("Category and labels"),
   and, for a name that is shown (never for the neutral presentation), inside the Merchant disclosure of a bank transaction
   on the payment match card, keyed by the transaction's `merchant_id`. It needs `organisation:read` and `merchants:read`;
   changing needs `organisation:write` and `merchants:write`, otherwise it is read-only. There are no buckets for a
   merchant. An archived merchant is read-only in the UI (the server refuses new assignments to it, ADR-0058), and a merged one
   shows no control: its assignments moved to the remaining merchant.

## Limits

- The match card shows the control only after the Merchant disclosure is opened (that is where the merchant is read), and
  only when a merchant record is identified for the transaction.
- A merchant list has no category or label column or filter (the API has no inverse query, ADR-0057).
- Tests are offline: component tests with a stateful API stub, MCP and client tests against the real routes; no live bank,
  model or logo service.
