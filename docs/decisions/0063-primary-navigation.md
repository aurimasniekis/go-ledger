# ADR-0063: Primary navigation and the mobile menu

Status: Accepted (task UI-POL, 2026-11-22). Builds on ADR-0036, ADR-0061 and ADR-0062. It replaces the parts of ADR-0061
point 1 and ADR-0062 that put Merchants and Organise under the Account menu. The wording and layout are this task's own,
within those ADRs; none is an operator decision.

## Decision

1. **Primary links.** The header's Primary navigation holds the finance pages, each shown only when its grant is held:
   Monthly view, Outlook, Payment matches, Commitments (`commitments:read`), Organisation (`organisation:read`),
   Merchants (`merchants:read`) and Bank connections (`connections:read`). The Account menu keeps what belongs to the
   person or to administration: Your account, Reference (exchange rates) and Administration. "Organise commitments" is
   now Commitments, "Categories, labels, buckets" is Organisation and "Merchant records" is Merchants; the pages and their
   addresses are unchanged.
2. **Small screens.** From lg (1024 px) up the primary links are a row of their own under the brand, Account and theme row (seven links
   do not fit beside them in the 1024 px page width). Below that they sit behind a "Menu" disclosure
   button at least 44 px tall (`aria-expanded`, `aria-controls`) and open as a column of full-size links (at least 44 px
   tall, normal text). Nothing is shortened or shrunk to fit: the former short labels ("Matches", "Connections") and the
   smaller text at 320 to 399 px are gone, so the visible text is always the accessible name (WCAG 2.5.3). The menu closes
   with Escape (focus returns to its button), on a click elsewhere, when focus leaves it and when the page changes. While
   it is closed below lg the links are hidden with CSS only, so they are also out of the keyboard order and the
   accessibility tree.
3. **Account button.** It is 44 px tall below lg as well.

## Limits

- With every grant held the link row is about 930 px wide, so it fits the 1024 px page width without wrapping; a wider font may wrap it to a second line.
- The theme switch keeps its size; its target size was not changed here.
