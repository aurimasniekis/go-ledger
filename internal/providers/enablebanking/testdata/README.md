# Enable Banking fixtures

Synthetic responses written by hand from Enable Banking's OpenAPI schemas
(spec version 1.0.0-1c03cf81, read 2026-11-20). Nothing in this directory
was recorded from a live account: decision D20 rules out live checks in the
initial build. Every identifier, name, amount and reference is invented,
and none belongs to a real person, account or bank.

`{{...}}` placeholders are filled in by the test server (fake_test.go).
The test server builds the session and authorize-session responses itself,
because their account uids change with every session. The `SECRET-ECHO` marker in
the error messages lets the tests check that response messages never end up
in Ledger's errors.
