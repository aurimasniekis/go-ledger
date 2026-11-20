// Package calendar holds Ledger's time primitives that are not instants.
//
// Ledger has two kinds of time and keeps them apart:
//
//   - Instants (when something happened: an import, a sign-in, a booking
//     timestamp a provider reports) are time.Time values in UTC.
//   - Calendar dates (a due date, a booking date, a reporting month) are
//     [Date] and [YearMonth]. They have no time of day and no zone, so a due
//     date never moves when the zone or daylight-saving time changes.
//
// [Zone] is the one configured IANA reporting time zone (decision D5). It is
// the only bridge between the two: it says which calendar date an instant
// falls on, and which instants a date or month covers.
//
// Month arithmetic clamps to the end of the month (decision D6): 31 January
// plus one month is 28 or 29 February, and an annual 29 February falls on
// 28 February in a non-leap year.
package calendar
