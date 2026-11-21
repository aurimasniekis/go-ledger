// Instants arrive as UTC RFC 3339 text and are shown in the browser's locale and zone.
// The reporting timezone (an open decision) governs dates in reports, not these timestamps.
const fmt = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })

export function formatInstant(v: string | number | null | undefined): string {
  if (v === null || v === undefined) return '—'
  const d = new Date(v)
  return Number.isNaN(d.getTime()) ? '—' : fmt.format(d)
}

// Display only. Amounts and dates arrive from the server as exact decimal strings and ISO dates; nothing here
// parses an amount into a number or does arithmetic. Grouping is done on the decimal text itself, with a fixed
// separator, so it never depends on the browser's locale (decision D38-3a).
const DECIMAL = /^(-?)(\d+)(\.\d+)?$/
const ISO_DATE = /^\d{4}-\d{2}-\d{2}$/

/** The thousands separator: U+202F narrow no-break space, so a grouped amount never breaks inside its digits. */
export const GROUP_SEPARATOR = String.fromCharCode(0x202f)

/** A calendar date is shown as ISO ("2026-11-07"), whatever the locale (D38-3a); anything else is returned as is. */
export function formatDate(iso: string): string {
  return iso
}

/** True for a well-formed ISO calendar date (YYYY-MM-DD) that exists, e.g. not 2026-02-30. */
export function isIsoDate(v: string): boolean {
  if (!ISO_DATE.test(v)) return false
  const d = new Date(`${v}T00:00:00Z`)
  return !Number.isNaN(d.getTime()) && d.toISOString().slice(0, 10) === v
}

/** An exact decimal string with fixed-separator digit grouping and its own decimals ("1124.19" -> "1 124.19", narrow no-break space). Not a decimal: returned as is. */
export function formatAmount(amount: string): string {
  const m = DECIMAL.exec(amount)
  if (!m) return amount
  const grouped = m[2]!.replace(/\B(?=(\d{3})+$)/g, GROUP_SEPARATOR)
  return `${m[1]}${grouped}${m[3] ?? ''}`
}

/** The currency's name for speech ("Euro"); the code when the runtime does not know it. */
export function currencyName(code: string): string {
  try {
    return new Intl.DisplayNames(undefined, { type: 'currency' }).of(code) ?? code
  } catch {
    return code
  }
}
