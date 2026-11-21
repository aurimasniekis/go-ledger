// Instants arrive as UTC RFC 3339 text and are shown in the browser's locale and zone.
// The reporting timezone (an open decision) governs dates in reports, not these timestamps.
const fmt = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })

export function formatInstant(v: string | number | null | undefined): string {
  if (v === null || v === undefined) return '—'
  const d = new Date(v)
  return Number.isNaN(d.getTime()) ? '—' : fmt.format(d)
}

// Display only. Amounts and dates arrive from the server as exact decimal strings and ISO dates; nothing here
// parses an amount into a number or does arithmetic. Intl groups the digits of the decimal string as text.
const DECIMAL = /^-?\d+(\.\d+)?$/
const dateFmt = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeZone: 'UTC' })

/** "2026-11-07" as a locale date ("Nov 7, 2026"); the day is not shifted by the browser's zone. Unparseable text is returned as is. */
export function formatDate(iso: string): string {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(iso)) return iso
  const d = new Date(`${iso}T00:00:00Z`)
  return Number.isNaN(d.getTime()) ? iso : dateFmt.format(d)
}

/** An exact decimal string with locale digit grouping and its own number of decimals ("1124.19" -> "1,124.19"). Not a decimal: returned as is. */
export function formatAmount(amount: string): string {
  if (!DECIMAL.test(amount)) return amount
  const decimals = amount.split('.')[1]?.length ?? 0
  return new Intl.NumberFormat(undefined, { minimumFractionDigits: decimals, maximumFractionDigits: decimals, useGrouping: true }).format(
    amount as Intl.StringNumericLiteral,
  )
}

/** The currency's name for speech ("Euro"); the code when the runtime does not know it. */
export function currencyName(code: string): string {
  try {
    return new Intl.DisplayNames(undefined, { type: 'currency' }).of(code) ?? code
  } catch {
    return code
  }
}
