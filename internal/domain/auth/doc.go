// Package auth holds the rules for interactive sign-in (task A2, ADR-0026):
// password policy and Argon2id hashing, sign-in rate limits, server-side
// sessions with idle and absolute timeouts, CSRF tokens bound to a session,
// and the recent re-authentication that sensitive actions require.
//
// Bank-sourced data never passes through here. Passwords, session tokens and
// CSRF tokens are never logged, audited or returned in errors: the store keeps
// only the Argon2id hash of a password and SHA-256 hashes of the session and
// CSRF tokens, and audit events carry identifiers and fixed reason codes.
//
// The package is pure business logic: storage is the [Store] port
// (internal/store/auth implements it) and HTTP lives in internal/httpapi.
package auth
