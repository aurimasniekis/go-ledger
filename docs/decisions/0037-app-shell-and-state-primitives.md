# ADR-0037: App shell and shared state primitives

Status: Accepted (2026-11-21)

## Context

Task U1. The screens built so far (sign-in, account security, tokens, administration, the monthly view) each carried
their own header, loading and failure markup and their own focus handling. The product-designer and C4p reviews left
four items for U1: navigation grouping, navigation while signed out, the heading hierarchy, and what the Undo notice
does when the user changes month.

## Decision

1. **Shell** (`components/app-shell.tsx`, `components/nav.tsx`). Skip link, header, then the page. The header holds the
   brand, the **primary navigation** (finance pages: today "Monthly view") and an **Account** disclosure that groups
   "Your account" (Security, Sessions, Your API tokens) and "Administration" (Users, Roles, All API tokens, each shown only
   when the grants allow). Sign out is inside it. While **signed out** (or while the session is loading) the header shows
   only the brand and the theme switch, because the links would bounce back to sign-in. A session confined to
   two-factor enrolment sees only Sign out. The menu is a disclosure (button with `aria-expanded`, plain links), not an
   ARIA menu: Tab works as usual, Escape closes it and returns focus to the button, and it closes on an outside click,
   when focus leaves and when the page changes. Its panel is anchored to the header and spans the width minus a margin
   on small screens, so it never leaves the viewport at 320 CSS px or at 200% zoom. Active links are bold and underlined
   everywhere (`aria-current="page"`).
2. **Heading hierarchy.** One h1 per page. On the monthly view the h1 is the month ("November 2026"), "Monthly view" is
   the line above it and the name used in the navigation; sections are h2 and their members h3. Account-menu group labels
   are not headings, so they never enter a page's outline. A test walks the outline.
3. **State panels** (`components/state-panels.tsx`): `LoadingState`, `EmptyState`, `FailedState`, `DisconnectedState`,
   `StaleNotice`; `QueryState` is built on them. Two new data-state badges join pending: **Disconnected** (the server or a
   provider cannot be reached; what is on screen is the last data that loaded) and **Not saved** (a change the server did
   not accept). They reuse the stale and error hues, so no colour token was added; icon and words carry the meaning.
   A request that never reaches the server (a `TypeError` from fetch) is shown as disconnected, not as a server failure.
   The shell shows an offline banner while the browser reports no network.
4. **Optimistic save** (`lib/optimistic-save.ts`, `useOptimisticSave`). Phases idle, pending, failed, saved. Pending means
   "Not saved yet" and is never presented as saved. The cache is changed ahead of the server only when the caller passes
   `apply`; money-facing changes (the payment form) do not, so nothing shows as recorded before the server confirms.
   On failure an applied change is rolled back, the variables are kept for `retry()`, and the failure is announced. On
   success the cache is reconciled and refetched before the phase becomes saved. The payment form uses it.
5. **Announcer** (`components/announcer.tsx`). Two live regions that stay mounted, polite and assertive, with
   `aria-live` and deliberately no `role`, so they never count as the page's alert or status. It speaks the outcome of an
   action (saved, not saved, month changed, offline, back online) and never moves focus. Repeating a message is
   announced again.
6. **Focus restore** (`lib/focus.ts`; `components/ui/dialog.tsx`). Radix returns focus only to a `DialogTrigger`; the
   dialogs here are opened by a controlled flag, so focus fell to BODY. Dialog contents now remember the element focused
   when they opened and return to it, falling back to the main region. `useFocusRestore` is the same for in-page
   surfaces (the payment form).
7. **Formatting** (`lib/format.ts`, `components/formatted.tsx`). Display only, no arithmetic: `formatAmount` groups the
   digits of the server's decimal string with `Intl.NumberFormat` given the string itself, keeping every digit and the
   original number of decimals; `Amount` shows "1,124.19 EUR" and speaks "1,124.19 Euro"; `DateText` is a `<time>`
   with a locale date that is not shifted by the browser zone. They are available to every screen but the monthly view
   keeps its existing ISO dates and ungrouped amounts: the date and number convention is part of the open D3 scale and
   changing it is a visible product decision, not a side effect of the shell.

## Undo notice across months (proposal, for the operator)

The review asked whether the Undo notice should follow the user to another month. Decision taken in this change, open to
reversal: **it persists until dismissed, undone or the user leaves the page, and it names the commitment, its due date
and the amount**, so it is meaningful in any month. Rationale: clearing it on navigation would silently take away the
only prompt for a reversible action; a notice that explains itself costs nothing. Undo now refreshes the month the payment
was recorded in (before this change it used whichever month was on screen, a bug). Alternatives: clear on navigation
(simple, loses the undo), or an activity list of recent changes with undo (more complete, a product decision, not built).
Limitation: leaving the monthly view for another page drops the notice.

## Consequences

- `AccountNav` is gone; its links live in the account menu. Tests that looked for the flat links open the menu first.
- Verified by Vitest component tests and Playwright checks of the shell at 320 CSS px, at 200% page zoom (640 CSS px, the
  1280 px layout at 200%) and at 320 px with 200% text size. These are viewport emulations in Chromium, not a browser zoom
  on a device. No screen reader was used (U2 owns the manual record).
