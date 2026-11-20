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
| Radius | `rounded-sm` 4px, `rounded-md` 6px, `rounded-lg` 8px (larger steps collapse to 8px) | `rounded-full` only for dots |
| Elevation | `shadow-sm`, `shadow-md` | flat by default; use sparingly |
| Type | `font-sans`, `font-mono` | `<Mono>` for IDs, codes, references |

Use them as Tailwind utilities: `bg-surface text-foreground border-border-control`. The Tailwind default palette, shadows and radii are cleared (`--color-*: initial`), so `bg-red-500` or `bg-white` generate nothing.

## Status is never colour alone

Each status has a distinct icon and a text label, rendered together by `StatusBadge` (`web/src/components/status-badge.tsx`): Paid (check), Upcoming (clock), Overdue (alert), Deferred (pause), Stale (history), Error (octagon X). Icons are decorative (`aria-hidden`); the label is the accessible name. Do not render a status with a coloured dot or text colour only. "Stale" is a data-freshness state and "Error" a failure state; neither means a commitment status.

## Contrast (automated)

`web/src/lib/contrast.test.ts` computes WCAG 2.2 contrast from the CSS in both themes:

- text, accent and status colours: at least 4.5:1 on `background`, `surface`, `surface-raised` and `muted`; each status colour also on its own `-bg` tint; foreground pairs for primary and secondary;
- `border-control` and `ring`: at least 3:1 against the same surfaces (WCAG 1.4.11);
- every colour token must be covered or listed in an explicit exemption with a reason (`border` is decorative);
- both themes define the same tokens and the OS-dark media block matches the dark block.

## No one-off colours

Three layers: the cleared Tailwind palette; an ESLint `no-restricted-syntax` rule that rejects hex and functional colours and default-palette class names in `src/` (tests excepted); and `web/src/lib/tokens.test.ts`, which scans every source file. Colour literals belong only in `index.css`. To add a colour: add the token to all three blocks of `index.css`, `@theme inline`, `lib/tokens.ts` and the contrast test.

## Open against the preset (D3)

Only the direction and accessibility blocks of the Night Owl preset are available. Open until the operator supplies the rest: exact palette values (the hexes here are Night Owl-inspired and tuned to the contrast rules, not taken from the preset), typography scale and font choice, spacing scale, density, chart/diff colour conventions, component inventory beyond Button/StatusBadge/Mono, and a decision whether a high-contrast theme is wanted. Windows forced-colors mode and print styles are not designed yet.
