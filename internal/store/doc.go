// Package store owns persistence: SQLite connection setup (WAL, busy timeout,
// foreign keys), explicit forward-only migrations and transaction helpers.
//
// It holds no business rules and no HTTP types. Domain packages declare the
// storage operations they need; this package implements them. See ADR-0001.
package store
