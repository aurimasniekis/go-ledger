# ADR-0017: Merge patch is RFC 7396 (recursive objects, no-op empty patch)

Status: Accepted (2026-11-20); supersedes part of §2 of [ADR-0015](0015-api-conventions-amendments.md): its sentence "Arrays and nested objects are replaced as a whole"

## Context

ADR-0015 §2 chose JSON Merge Patch (RFC 7396) for partial updates but said nested objects are replaced whole. That is not RFC 7396: the RFC merges object values recursively and `null` inside a nested object deletes that member. A client using a generic merge-patch library (the Terraform provider, MCP tools, curl users) that sends `{"settings":{"x":null}}` expects the other nested members to survive. An accepted ADR is not rewritten, so this ADR supersedes the sentence.

## Decision

1. **Nested objects merge recursively; arrays replace whole.** `httpapi.ApplyMergePatch` (and `MergePatch.Apply` for one member) implements RFC 7396 MergePatch: when the patch value is an object, the target is merged member by member (a `null` member deletes it, any other value is merged in the same way); any non-object patch value (scalar, array, `null` at the top of the value) replaces the target. A resource with a nested-object member calls it with the stored value, then validates the merged result like any other input.
2. **A patch that is not a JSON object** is a 400 whose message says "the patch must be a JSON object".
3. **An empty patch `{}` is a no-op.** The version precondition is still required and checked (428 without one, 409 `version_conflict` when stale, 404 for a missing object), but nothing is written: the version is **not** bumped and the current representation and ETag are returned. A patch holding only the `version` precondition member is the same. This keeps a Terraform apply that changes nothing from churning versions. A resource implements it by checking the version without an update (the example's `Store.CheckVersion`).

## Consequences

- The rest of ADR-0015 §2 stands (media type, 415, null on required members, unknown members, `version` member as precondition).
- ADR-0015's other points are unchanged.
