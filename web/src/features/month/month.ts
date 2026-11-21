// Month and time helpers for the monthly view. Calendar arithmetic only; no money.

const MONTH = /^(\d{4})-(0[1-9]|1[0-2])$/

export function isMonth(s: unknown): s is string {
  return typeof s === 'string' && MONTH.test(s)
}

/** The month before or after m (n may be negative). */
export function shiftMonth(m: string, n: number): string {
  const [y, mo] = m.split('-').map(Number) as [number, number]
  const idx = y * 12 + (mo - 1) + n
  return `${String(Math.floor(idx / 12)).padStart(4, '0')}-${String((idx % 12) + 1).padStart(2, '0')}`
}

/** The month a Date falls in, in the browser's time zone. */
export function monthOf(d: Date): string {
  return `${String(d.getFullYear()).padStart(4, '0')}-${String(d.getMonth() + 1).padStart(2, '0')}`
}

export function monthLabel(m: string): string {
  const [y, mo] = m.split('-').map(Number) as [number, number]
  return new Intl.DateTimeFormat('en', { month: 'long', year: 'numeric', timeZone: 'UTC' }).format(new Date(Date.UTC(y, mo - 1, 1)))
}

/** A timestamp as "2026-11-21 12:00 UTC": unambiguous, and the same wherever it is read. */
export function utcStamp(iso: string): string {
  return iso.slice(0, 16).replace('T', ' ') + ' UTC'
}

/** How long ago, in words: "just now", "5 minutes ago", "2 hours ago", "3 days ago". */
export function ageLabel(fromMs: number, nowMs: number): string {
  const s = Math.max(0, Math.round((nowMs - fromMs) / 1000))
  if (s < 60) return 'just now'
  const plural = (n: number, u: string) => `${n} ${u}${n === 1 ? '' : 's'} ago`
  if (s < 3600) return plural(Math.floor(s / 60), 'minute')
  if (s < 86400) return plural(Math.floor(s / 3600), 'hour')
  return plural(Math.floor(s / 86400), 'day')
}

/** True for a decimal string that is zero ("0", "0.00"): a text check, not arithmetic. */
export function isZeroAmount(a: string): boolean {
  return /^0(\.0*)?$/.test(a)
}
