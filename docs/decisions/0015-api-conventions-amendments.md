# ADR-0015: API conventions amendments (version conflicts, partial updates)

Status: Accepted (2026-11-20); supersedes parts of [ADR-0011](0011-api-conventions-and-openapi.md); the sentence on nested objects in §2 is superseded by [ADR-0017](0017-merge-patch-recursion.md)

## Context

ADR-0011 set the Management API conventions. Its review found gaps that are cheap to close now and breaking once the first real resource ships: one 409 code covered two kinds of conflict, the partial-update convention was undefined (and the example could not clear a field), the ADR allowed a body version on DELETE that no handler could honour, and the paging text promised more than keyset paging gives. An accepted ADR is not rewritten, so this ADR supersedes the affected parts of ADR-0011 and leaves the rest in force.

## Decision

1. **A stale version is 409 `version_conflict`** (`httpapi.CodeVersionConflict`, `httpapi.VersionConflict`). It replaces `conflict` in ADR-0011 §7 and always carries `reconciliation`. `conflict` stays for every other state conflict (for example a name that is already taken) and never carries `reconciliation`. Clients tell the two apart by `code`, not by the presence of `reconciliation`. Status stays 409, not 412 (D17).
2. **Partial updates are JSON Merge Patch (RFC 7396).** `PATCH` with `Content-Type: application/merge-patch+json`; any other type is **415 `unsupported_media_type`** (a new code). An absent member is unchanged; `null` clears an optional member and is a 400 naming the member on a required one; unknown members are 400. Arrays and nested objects are replaced as a whole (RFC 7396 has no element-wise merge); a resource with such members says so in its OpenAPI description. The version precondition may be the `version` member of the patch: it is the precondition, never stored data. `httpapi.DecodeMergePatch` implements this; OpenAPI documents the request body as `application/merge-patch+json`. A resource may offer `PUT` for full replacement where useful. The Terraform provider uses `PATCH` and sends explicit `null` for attributes removed from the configuration. The example resource's optional `note` demonstrates absent versus null.
3. **DELETE takes the version from `If-Match` only.** It has no body, so ADR-0011's "or in the body's version" applies to PATCH (and PUT) only. Without `If-Match` it is 428. This is the simpler of the two options in the review, and every non-browser client can set a header.
4. **Paging stability** (replaces the stability sentence in ADR-0011 §5). Keyset paging is exact for items that already existed when a walk began: concurrent inserts and deletes never make it skip or repeat one of them. An item created during a walk appears only if its ULID sorts after the cursor; within one millisecond the random part decides, and a clock stepping back can place a newer item earlier. So ID order is creation order only to the millisecond, there is no snapshot, and items created during a walk may or may not appear. The OpenAPI description says so.
5. **Contract check.** `httpapi.CheckContract` also refuses an API route pattern ending in `/`, which would match every deeper path like a `{name...}` catch-all.
6. **Canonical `If-Match`.** A tag with a leading zero (`"01"`) is 400, like the documented pattern `^"[1-9][0-9]*"$`; RFC 9110 strong comparison is byte-exact.

## Why

- A frozen code cannot be split later without breaking clients; a dedicated code lets the Terraform provider and MCP tools branch on `code` alone.
- Merge Patch is the smallest convention that lets a client clear an optional field, which the Terraform provider needs for removed attributes, and it needs no per-resource operation list. JSON Patch (RFC 6902) was rejected as heavier to write and to validate; PUT-only was rejected because it forces read-modify-write on every change.
- If-Match-only DELETE avoids inventing a body or query version for one verb.

## Consequences

- Real resources must register `PATCH` as merge patch, document `application/merge-patch+json`, and treat `null` per field as above.
- A field that is optional but cannot be expressed as null (a non-nullable column) must be declared required.
- ADR-0011 is marked partly superseded; its §7 conflict code, its "or in the body's version" for deletes and its §5 stability sentence no longer apply.
