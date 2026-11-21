# Ledger design system

Semantic tokens for the web UI (task F8, [ADR-0014](decisions/0014-design-tokens-and-theme-system.md)). The source of truth is `web/src/index.css`; the `/design` route renders every token and the status badges in the active theme.

## Direction

Night Owl-inspired: deep navy surfaces with restrained teal, cyan, violet and warm accents, and a designed light companion. Corner radii 4-8px, crisp borders, subtle elevation, almost no gradients. Strong type and spacing hierarchy; monospace for identifiers, codes and diffs. No oversized rounded cards, decorative charts or generic AI-product styling.

## Themes

Three choices: **System** (follows the OS), **Light**, **Dark**. The choice is client-only state in `localStorage` (`ledger.theme`) and a `data-theme` attribute on `<html>`; `public/theme-init.js` applies it before first paint. The control is a native `<select>`, so it is keyboard operable and announced by screen readers. Reduced motion is honoured globally (`prefers-reduced-motion: reduce` shortens animations and transitions to ~0).

## Tokens

| Group | Tokens | Use |
| --- | --- | --- |
| Surfaces | `background`, `surface`, `surface-raised`, `muted` | page, panels, popovers, subtle fills |
| Text | `foreground`, `muted-foreground` | body, secondary text |
| Borders | `border` (decorative), `border-control` (3:1), `ring` (3:1) | dividers; form-control boundaries; focus |
| Accents | `primary` (= teal), `accent-teal`, `accent-cyan`, `accent-violet`, `accent-warm`, `accent` (= violet), `secondary` | actions and emphasis |
| Status | `status-paid`, `-upcoming`, `-overdue`, `-deferred`, `-stale`, `-error`, each with a `-bg` tint | commitment and data state |
| Warning | `warning`, `warning-bg` | caution notes on security and account screens (two-factor required, root tokens, show-once secrets); not a status |
| Radius | `rounded-sm` 4px, `rounded-md` 6px, `rounded-lg` 8px (larger steps collapse to 8px) | `rounded-full` only for dots |
| Elevation | `shadow-sm`, `shadow-md` | flat by default; use sparingly |
| Type | `font-sans`, `font-mono` | `<Mono>` for IDs, codes, references |

Use them as Tailwind utilities: `bg-surface text-foreground border-border-control`. The Tailwind default palette, shadows and radii are cleared (`--color-*: initial`), so `bg-red-500` or `bg-white` generate nothing.

## Status is never colour alone

Each status has a distinct icon and a text label, rendered together by `StatusBadge` (`web/src/components/status-badge.tsx`). Icons are decorative (`aria-hidden`); the label is the accessible name. Do not render a status with a coloured dot or text colour only. A bare label is not enough for freshness: pair "Stale" with an "as of" age through the `label` prop (for example `label="Stale, 3 days"`).

Two separate axes, separate types (`CommitmentStatus`, `DataState`), so a data state can never replace a commitment status in a row:

| Axis | Status | Icon | Tier |
| --- | --- | --- | --- |
| Commitment | Paid | check circle | quiet |
| Commitment | Upcoming | clock | default |
| Commitment | Due today | calendar clock | default |
| Commitment | Partly paid | dashed circle | default |
| Commitment | Overdue | triangle alert | strong |
| Commitment | Deferred | hourglass | quiet |
| Data state | Stale | history | default |
| Data state | Error | octagon X | strong |
| Data state | Not saved yet (pending save) | spinner | default |
| Data state | Disconnected | unplug | default |
| Data state | Not saved (save failed) | save off | strong |

**Emphasis tiers** order badges by visual mass so the order survives greyscale: *strong* is a solid fill with `status-strong-foreground` text (things the user must act on or a failure), *default* is a tint with a coloured border, *quiet* is coloured text and icon on a neutral border with no tint (settled or parked). Overdue (amber/orange, triangle) and Error (red/magenta, octagon) differ in hue and shape.

**Hue rules**: status tokens are never equal to, and stay at least 20 CIE76 delta-E from, any accent or the focus ring, and from each other; overdue and error at least 35 apart. The dark focus ring is near-white for that reason. `contrast.test.ts` enforces it in both themes. Violet and warm accents therefore never read as a commitment or freshness state.

**Warning is not a status.** The status hues mean one thing each (a commitment or data state), so a caution on a security screen must not reuse them. `warning` is a neutral warm ink (dark brown in light, sand in dark) with a 2px border and a tint, always shown with a triangle icon and words through `Notice` (`web/src/components/notice.tsx`). Being near-neutral is deliberate: every saturated hue is already taken by an accent or a status, and the contrast test keeps `warning` at least 20 delta-E from all of them.

**Provenance markers** (C4, `features/month/provenance.tsx`) are a third axis next to commitment status and data state: where a settled row came from. *Manual payment* (a payment recorded by hand), *Set by hand* (an override with a reason) and *Bank match* (a confirmed match of a bank transaction), or *Bank match and manual payment* when a row has both. The kind comes from the payload's `manual_evidence_count` and `bank_evidence_count`; evidence of no known kind shows no marker, and a bank match is never labelled manual. They are neutral chips (text plus icon, `border-control`), so a bank fact, a user correction and a manual payment never read as a status.

**Reserved, not yet built** (to add with X4, B4 before those screens): bank-pending transaction, needs-review (ambiguous match). Disconnected and save-failed exist (U1); they reuse the stale and error hues, so icon and words, not a new colour, tell them apart.

## Shell and shared state components (U1, [ADR-0037](decisions/0037-app-shell-and-state-primitives.md))

Reuse these before writing screen-specific markup.

| Need | Use | Notes |
| --- | --- | --- |
| Page chrome | `AppShell` (`components/app-shell.tsx`) | skip link, brand, primary nav, Account menu, theme, offline banner; no navigation while signed out |
| Loading, empty, failed, disconnected, stale | `LoadingState`, `EmptyState`, `FailedState`, `DisconnectedState`, `StaleNotice` (`components/state-panels.tsx`) | `QueryState` wraps a TanStack query with all of them |
| Save with pending, failure and reconcile | `useOptimisticSave` (`lib/optimistic-save.ts`) | pass `apply` only for changes that are safe to show early; money changes show "Not saved yet" until the server confirms |
| Say what happened without moving focus | `useAnnounce()` (`components/announcer.tsx`) | polite for outcomes, assertive for failures; the live regions have no role |
| Give focus back | `useFocusRestore` (`lib/focus.ts`); dialogs do it themselves | falls back to the main region, never BODY |
| Amounts and dates for display | `Amount`, `DateText`, `formatAmount`, `formatDate` | the server's decimal strings are grouped as text; nothing is parsed into a number |

Layout rules the shell checks (Playwright, `e2e/shell-responsive.spec.ts`): no horizontal scrolling and every header control inside the viewport at 320 CSS px, at 200% page zoom and at 320 px with 200% text size. Fixed widths need `max-w-full`, long labels need to wrap (`whitespace-normal`), and a menu panel is anchored to the header, not to the control that opens it.

Headings: one h1 per page, sections h2, members h3. Navigation group labels are not headings.

## Contrast (automated)

`web/src/lib/contrast.test.ts` computes WCAG 2.2 contrast from the CSS in both themes:

- text, accent and status colours: at least 4.5:1 on `background`, `surface`, `surface-raised` and `muted`; each status colour also on its own `-bg` tint; foreground pairs for primary and secondary;
- `border-control` and `ring`: at least 3:1 against the same surfaces (WCAG 1.4.11);
- the solid-badge label (`status-strong-foreground`) on `status-overdue` and `status-error`;
- status hues distinct from accents, ring and each other (delta-E);
- every colour token must be covered or listed in an explicit exemption with a reason (`border` is decorative);
- both themes define the same tokens and the OS-dark media block matches the dark block.

## No one-off colours

Three layers: the cleared Tailwind palette; an ESLint `no-restricted-syntax` rule that rejects hex and functional colours and default-palette class names in `src/` (tests excepted); and `web/src/lib/tokens.test.ts`, which scans every source file. Colour literals belong only in `index.css`. To add a colour: add the token to all three blocks of `index.css`, `@theme inline`, `lib/tokens.ts` and the contrast test.

## Open against the preset (D3)

Only the direction and accessibility blocks of the Night Owl preset are available. Open until the operator supplies the rest: exact palette values (the hexes here are Night Owl-inspired and tuned to the contrast rules, not taken from the preset), typography scale and font choice, spacing scale, density, chart/diff colour conventions, component inventory beyond Button/StatusBadge/Mono, and a decision whether a high-contrast theme is wanted. Windows forced-colors mode and print styles are not designed yet.
