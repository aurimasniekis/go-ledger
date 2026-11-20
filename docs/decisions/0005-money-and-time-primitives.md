# ADR-0005: Money and time primitives

Status: Proposed (2026-11-20)

## Context

Ledger must keep amounts exact, keep the source form of bank amounts (provenance), never combine currencies without an explicit conversion, and keep due dates apart from instants. The operator decided (D5): any ISO 4217 code that appears in the data; decimal storage with a stored scale that keeps the source exact; half-even rounding only at presentation and conversion; one configured IANA reporting time zone (default: the host zone captured at setup); per-currency totals first, a converted aggregate second with rate provenance. D6 adds month-end clamping and date-only due dates.

## Decision

1. **`internal/domain/money`.** `Decimal` is an arbitrary-precision integer coefficient (`math/big.Int`) with a non-negative scale. The scale is part of the value: `1.50` and `1.5` are `Equal` but not `Identical`. Parsing accepts only plain decimal text (sign, digits, optional fraction; at most 24 fraction digits and 60 digits). No exponents, separators or locale forms: those belong to the UI.
2. **Add, Sub and Cmp are exact.** The result has the larger of the two scales. `Mul` is exact and adds the scales; it exists for conversion. There is no division.
3. **`Money` is a Decimal plus a `Currency`.** `Add`, `Sub` and `Cmp` across currencies return `ErrCurrencyMismatch`. `Sum` over several currencies returns `*MixedCurrencyError` (matches `ErrMixedCurrencies`). `Totals` keeps one total per currency. Nothing in the package converts.
4. **Currency codes.** Any three upper-case ASCII letters are accepted. A table of ISO 4217 codes gives `Known()` and the minor units. An unknown but well-formed code is kept rather than refused, so imported data is never dropped.
5. **Rounding.** Only `RoundHalfEven` exists, and it is used only at presentation (`Money.ForDisplay`, which pads or rounds to the ISO minor units and reports whether it rounded) and conversion. A code with no or unknown minor units keeps the amount's own scale. Storage never rounds.
6. **JSON.** An amount is a JSON string, never a JSON number, so it never passes through `float64`. Money is `{"amount":"12.50","currency":"EUR"}`, and unknown or missing fields are errors.
7. **`internal/domain/calendar`.** `Date` (`YYYY-MM-DD`) and `YearMonth` hold no time of day or zone. Instants stay `time.Time` in UTC. `AddMonthsClamped` and `ClampedDate` clamp to the month's last day (D6), so an annual 29 February falls on 28 February in a non-leap year. Schedules step from the anchor date and never accumulate clamping.
8. **`calendar.Zone` is the reporting zone.** It is loaded from an explicit IANA name; `""` and `"Local"` are refused. It maps instants to dates (`DateOf`, `Today(now)`) and dates and months to half-open UTC ranges (`DayRange`, `MonthRange`), which handles 23- and 25-hour days and skipped midnights. `HostZoneName` finds the host's IANA name so that setup can capture it once.
9. **Storage (`internal/store/sqlvalue`).** Amounts are TEXT in their exact canonical form, currencies TEXT codes and dates TEXT `YYYY-MM-DD`, in STRICT tables with the CHECK expressions `AmountCheck`, `CurrencyCheck` and `DateCheck`. The Valuer and Scanner types `Amount`, `Currency` and `Date` write and read them. Scanners accept only text. The amount Valuer refuses a scale it could not read back. **SQL never does arithmetic on amounts** (no `SUM`, `total()` or `ORDER BY amount`): Go computes totals with `money.Totals`.
10. **No binary floating point.** `nofloat_test.go` type-checks `money`, `calendar` and `sqlvalue`, and fails on any float or complex expression (untyped constants and calls such as `Duration.Hours` included) or any `big.Float`. It scans their test files for float names and literals. A second test proves that the check catches each case.

## Why

- **Decimal with a stored scale, not integer minor units:** D5(a). Minor units would round or refuse a source amount with more digits than ISO lists (nothing guarantees a provider reports no more digits than ISO lists), and the stored form would depend on a display-precision table. The storage design depends neither on rounding nor on display precision.
- **`big.Int`, not `int64`:** a total or a conversion product cannot overflow silently. The cost is that a Decimal is not comparable with `==`. Its methods document that.
- **Own type, not a third-party decimal library:** the common Go libraries expose float conversions, which would undercut the float ban. The needed surface is small, and property tests check it against `math/big.Rat`.
- **TEXT, not NUMERIC or REAL:** SQLite's numeric affinity and aggregate functions use binary floating point and drop the scale. A test shows the scanner refusing a NUMERIC column's value.
- **No host-dependent zone:** a zone that follows the host would move stored due dates and month boundaries when the host changes.

## Consequences

- A conversion (FX tasks) multiplies with `Decimal.Mul` by an exact rate, rounds half-even to the target's minor units and keeps rate provenance. Items without an acceptable rate are excluded and flagged (D5). The rate type, rate provenance and converted aggregate are not part of this change.
- Sorting or filtering by amount in SQL needs a separate, derived sortable column if it is ever needed. It must not replace the exact column.
- **To wire after the configuration (F2) and store (F5) pull requests merge:**
  - Build the reporting `calendar.Zone` from `reporting.timezone`.
  - Persist the reporting zone (control default, 2026-11-20; a separate wiring task, not this change): on the first start against a new database, resolve the zone once (explicit `reporting.timezone`, else `calendar.HostZoneName()`) and store it in the database. Refuse startup if no IANA name resolves (no silent UTC). An empty config later means "use the stored zone"; an explicit value that differs is applied with a warning and an audit entry. F2 validation already rejects `Local`, like `LoadZone`.
  - Migrations that add money or date columns copy the `sqlvalue` CHECK helpers next to the store's timestamp check. The store's schema-convention test enforces the names: `*_date`, `*_amount` (with a sibling `*_currency`) and `*_currency` are TEXT with the matching CHECK.
- JSON: a `null` or non-string amount, currency, `Decimal` or `Date` is an error, never zero. `Decimal` is not comparable with `==` (compile error); use `Cmp`/`Equal`.
- Calendar arithmetic that leaves years 1..9999 returns `ErrInvalidDate` (`AddDays`, `AddMonthsClamped`, `AddMonths`, `DateOf`, `DayRange`, `MonthRange`). `RoundHalfEven` panics on a negative scale.
- The amount Valuer and CHECK also bound the digit count (`MaxDigits`) and require the canonical form (no leading zeros, no negative zero), so a stored value can always be read back.
- The float ban test covers the money, calendar and sqlvalue packages only. Packages for commitments, matching, outlook and FX must be added to `moneyPackages` as they land (see docs/plan.md).
- Locale formatting (grouping, symbols) is the UI's job. `ForDisplay` fixes only the digits.
