// Dates for the offline main journey, shared by the simulated bank (journey-server.mjs) and the spec. Everything is
// relative to "now" in UTC, so the journey is the same on any day: the data sits in the previous month (always past)
// and the current month. Days stay at or below 28, so every month has them.

export const LEDGER_PORT = 18081
export const BANK_PORT = 18082
export const JOURNEY_USER = 'journey'
// A fixed test value for a throwaway database: not a secret.
export const JOURNEY_PASSWORD = 'journey-only-not-a-secret'

const pad = (n) => String(n).padStart(2, '0')

export function journeyDates(now = new Date()) {
  const y = now.getUTCFullYear()
  const m = now.getUTCMonth() // 0-based
  const prev = new Date(Date.UTC(y, m - 1, 1))
  const month = (d) => d.getUTCFullYear() + '-' + pad(d.getUTCMonth() + 1)
  const prevMonth = month(prev)
  return { prevMonth, currentMonth: month(new Date(Date.UTC(y, m, 1))), nextMonth: month(new Date(Date.UTC(y, m + 1, 1))), day: (d) => prevMonth + '-' + pad(d) }
}
