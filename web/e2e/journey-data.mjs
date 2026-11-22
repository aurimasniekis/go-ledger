// Dates for the offline main journey, shared by the simulated bank (journey-server.mjs) and the spec. Everything is
// relative to "now" in UTC, so the journey is the same on any day: the data sits in the previous month (always past)
// and the current month. Days stay at or below 28, so every month has them.

export const LEDGER_PORT = 18081
export const BANK_PORT = 18082
export const JOURNEY_USER = 'journey'
// A fixed test value for a throwaway database: not a secret.
export const JOURNEY_PASSWORD = 'journey-only-not-a-secret'

const pad = (n) => String(n).padStart(2, '0')
// The month name as the UI prints it (month.ts uses the 'en' locale in UTC).
const monthName = (d) => new Intl.DateTimeFormat('en', { month: 'long', timeZone: 'UTC' }).format(d)

export function journeyDates(now = new Date()) {
  const y = now.getUTCFullYear()
  const m = now.getUTCMonth() // 0-based
  const prev = new Date(Date.UTC(y, m - 1, 1))
  const month = (d) => d.getUTCFullYear() + '-' + pad(d.getUTCMonth() + 1)
  const prevMonth = month(prev)
  return { prevMonth, prevMonthName: monthName(prev), currentMonth: month(new Date(Date.UTC(y, m, 1))), nextMonth: month(new Date(Date.UTC(y, m + 1, 1))), day: (d) => prevMonth + '-' + pad(d) }
}

// The deferral journey (DF3) has its own server instance: own ports, own bank data, own database.
export const DEFERRAL_LEDGER_PORT = 18083
export const DEFERRAL_BANK_PORT = 18084

// The month and year as the UI prints them (month.ts: 'en', UTC).
const monthName2 = (d) => new Intl.DateTimeFormat('en', { month: 'long', year: 'numeric', timeZone: 'UTC' }).format(d)

// Dates for the deferral journey, in the next month and the one after (UTC): occurrences there are upcoming whatever day it is, so
// they can be planned (an occurrence is overdue-and-untracked in the past, see ADR-0047). Days stay at or below 28.
export function deferralDates(now = new Date()) {
  const y = now.getUTCFullYear()
  const m = now.getUTCMonth()
  const next = new Date(Date.UTC(y, m + 1, 1))
  const later = new Date(Date.UTC(y, m + 2, 1))
  const month = (d) => d.getUTCFullYear() + '-' + pad(d.getUTCMonth() + 1)
  return {
    month: month(next),
    monthName: monthName2(next),
    laterMonth: month(later),
    laterMonthName: monthName2(later),
    // The occurrence's due date, a later date in the same month, and the date it is carried to in the month after.
    due: month(next) + '-20',
    within: month(next) + '-27',
    carried: month(later) + '-03',
  }
}
