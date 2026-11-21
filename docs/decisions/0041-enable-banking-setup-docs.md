# ADR-0041: Enable Banking setup documentation

Status: Proposed (2026-11-21)

Numbering: 0041. ADR-0039 is automatic payment matching (X2) on work; 0040 is held for PR #77. ADR-0023 (B3) is the adapter's record.

## Context

Task B5 documents how an operator sets up Enable Banking for each access mode, what each mode can and cannot reach,
and which steps only a person can do. Inputs: D10 (app mode is an unrestricted production application, developer mode is
a restricted production application, neither is the sandbox, and connections choose their application), research R1 (the
public documentation, read 2026-11-20), and ADR-0023 (the adapter and the configuration slots).

## Decision

1. The setup page is [docs/enable-banking.md](../enable-banking.md). It is the operator's source for setup and
   configuration. It links the public Enable Banking pages instead of copying them, and it states which parts come from
   the documentation and which are Ledger's own choices.
2. The mode names are Ledger's (D10). The page maps them to Enable Banking's own terms (environment, restricted or
   unrestricted activation) and says plainly that the documentation does not use "app mode" or "developer mode".
3. The page describes only what the code on the branch does. Connection flow, connection screens and the visible mode on a
   connection are marked as arriving with B4. The PSU headers that some banks require are marked as not sent yet.
4. A test (`internal/config/enablebanking_docs_test.go`) fails when the page names a `LEDGER_` variable whose exact name
   is not in the configuration definition, when PEM key material is pasted into it, or when a UUID on it is not one of the
   all-zero placeholders. The page's configuration examples therefore cannot drift from the code silently.
   The test checks variable names and placeholders only; it does not check wording.

## Why

- Operators need to know, before connecting a bank, which mode they are in and what it changes. Accounts a restricted
  application cannot see are dropped without an error, so a missing account is easy to misread as a sync fault.
- Linking the public documentation avoids copying text that Enable Banking may change. The few facts the code depends on
  (signing, environments, consent caps, the rate-limit guidance) are stated with the page that supports them.
- Keeping the Enable Banking mode names apart from Ledger's names avoids an operator assuming that "developer mode" is
  the sandbox or that the documentation promises a behaviour it does not describe.

## Consequences

- The page must be updated when the adapter, the configuration or Enable Banking's documentation changes; the test
  catches only configuration drift, not wording.
- Nothing was verified against a live account (D20). The Terms of Service "Restriction of Use" section was not read, and
  the page says so.
