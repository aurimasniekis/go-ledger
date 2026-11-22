# ADR-0059: Outlook slice by bucket and category/label list filters

Status: accepted (task O3, control decision C-O2-gaps, decision D12)

## Context

Decision D12 makes buckets groupings of commitments and accounts "used to slice the outlook", without allocating money.
ADR-0053 and ADR-0057 built the organisation model and its UI but not the slice, and the lists that show organised
objects could not be filtered by category or label.

## Decision

### The slice

`GET /api/v1/outlook?bucket=<id>` restricts the outlook to the commitments and the accounts that are members of the
bucket. Everything else about the outlook is unchanged and is computed by the same code:

- still due, overdue and upcoming come from the monthly view of the bucket's commitments only
  (`monthview.Service.MonthScoped`), so payment state is derived in one place;
- a balance is counted only for the accounts in the bucket and is still the **whole balance of each account**: a bucket
  holds no money and allocates none, so no share of a balance is ever computed;
- figures stay per currency and are never added across currencies. The converted aggregate is built from the sliced
  per-currency lines under the existing conversion policy (same rates, same exclusions, same incomplete flag);
- the response carries `slice` (the bucket, its commitment and account counts and a fixed note) and the page shows a
  "Slice" label with a link back to the whole outlook. The whole outlook has `slice: null`. The notice (not a forecast,
  never money that is safe to spend) is unchanged and still shown;
- an archived bucket still slices (archiving keeps membership); an unknown or malformed bucket is a validation error;
  an empty bucket gives an empty slice.
- Slicing needs `organisation:read` as well as `commitments:read`; balances in a slice still need `connections:read`.

### Category and label filters on lists

`category_id` and `label_id` (repeatable: any of them; the two combine: both must hold) are accepted by:

| endpoint | what the filter tests |
| --- | --- |
| `GET /commitments` | the commitment's category / labels |
| `GET /occurrences` | the commitment's category / labels |
| `GET /occurrences/summary` | the commitment's; rows **and totals** cover only the matching commitments, and the response says so in `filter` |
| `GET /payment-matches` | the bank transaction's category / labels, including what it inherits from the pending transaction it replaced |

A category also matches its child categories; archived definitions still match. An unknown category or label is a
validation error. The filters need `organisation:read`. A filter that matches more than 5000 objects is refused
(narrow it), which bounds the query. They resolve through `organisation.Store.Select`, which uses the same effective
assignment as the assignment read (the nearest row wins unless it may not replace a farther one), so a list and the
object's own Organise panel never disagree.

MCP: `get_outlook` (with `bucket`) is new, and `list_commitments`, `list_occurrences`, `get_month_summary` and
`list_payment_matches` take `category_id` and `label_id`. The Go client has `GetOutlook` and the filters on its three list
filters. The UI has a bucket choice on the outlook and category and label choices on the monthly view, the payment
matches and the organise-commitments screen.

## Not applicable or not built (traceable)

- **Accounts, transactions and merchants have no list endpoint** of their own that this applies to: accounts are
  shown inside bank connections and the outlook (sliced by bucket), transactions are shown through payment matches
  (filtered here), merchants have their own admin-only screens. No filter was invented for them.
- The Go client still has no method for the monthly summary (`getOccurrencesSummary` is listed as not implemented), so the
  summary filter is reachable through MCP and HTTP only.
- Bank-transaction category filters on matches are applied before paging by transaction ID; a filter matching over 5000
  transactions is refused rather than paged.
- The monthly view's existing client-side bucket filter (ADR-0057) is unchanged: it narrows rows and leaves the totals
  covering the month. The category and label filters are server-side and change the totals; both say which they are.
