# ADR-0014: Design tokens and theme system

Status: Accepted (2026-11-20); partial, see Open

## Context

ADR-0008 scaffolded colour tokens for a dark and a light theme with a text-contrast test. F8 completes the token set for the Night Owl-inspired preset (direction and accessibility blocks only; the rest of the preset text is unavailable) and enforces token-only styling. F7 review finding 5 asked for non-text contrast (WCAG 1.4.11) on form-control boundaries.

## Decision

1. **Tokens** live in `web/src/index.css` as CSS variables per theme, mapped into Tailwind with `@theme inline`: surfaces (incl. `surface-raised`), text, `border` (decorative), `border-control` and `ring` (both at least 3:1), accents (teal, cyan, violet, warm), six statuses each with a tint, radii 4/6/8px, two elevations, sans and mono fonts. The unused `danger`, `warning` and `success` tokens are replaced by `status-error`, `status-stale` and `status-paid`.
2. **Status never relies on colour**: `StatusBadge` pairs a distinct icon with a label. Status hues are kept visibly distinct (delta-E tested) from accents, the focus ring and each other; overdue (amber, triangle) and error (red, octagon) are clearly separated. Badges have emphasis tiers (strong solid, default tint, quiet plain) and the types split commitment status from data state. `/design` is a route only, not in the main navigation.
3. **Token-only enforcement** in three layers: the Tailwind default colour, shadow and radius scales are cleared; an ESLint rule rejects colour literals and default-palette classes in `src/`; a Vitest scan checks the same over all sources. Raw colours remain legal only in `index.css`.
4. **Contrast** is tested for every token pair in both themes (4.5:1 text, 3:1 control border and focus ring), with an explicit exemption list; a coverage test fails when a token is added without a pair.
5. **Radii** above 8px collapse to 8px so a stray `rounded-3xl` cannot break the 4-8px rule.
6. **Showcase**: the `/design` route renders tokens and components in the active theme; no Storybook.
7. The theme switch stays a native `select` (keyboard accessible by default), persisted in `localStorage`; Playwright covers keyboard operation, persistence and reduced motion.
8. Values are duplicated across the light, dark and OS-dark blocks because CSS cannot share them; a test keeps the dark blocks identical.

## Open (D3)

Exact preset palette, type scale, spacing scale, density, diff/chart colours and a high-contrast or forced-colors treatment await the operator. The hex values are Night Owl-inspired, tuned to the contrast rules, and replaceable in one file with the tests as the safety net.

## Consequences

Components must use semantic utilities; adding a colour is a deliberate change to four places (documented in docs/design-system.md). Tinted hover states via opacity (`bg-primary/90`) are allowed and are not contrast-tested.
