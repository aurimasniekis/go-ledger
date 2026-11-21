// Package ledgerclient is a Go client for Ledger's Management API (/api/v1).
//
// It is public so other programs, such as Ledger's Terraform provider and the
// ledger command line, use the same code. It speaks only the documented HTTP
// API with a bearer token and imports nothing from Ledger's internal packages.
// Its operations, request fields, response fields and enums are checked
// against the served OpenAPI document by tests (see openapi_test.go), so the
// two cannot drift silently (ADR-0032).
//
// Money is always a decimal string with its currency; the client never
// converts it to a floating-point number.
//
// Writes need the version they are based on (ADR-0011): read the object, then
// pass its Version to Update or Delete. A stale version is an *APIError with
// IsVersionConflict true and the current version in Reconciliation.
package ledgerclient
