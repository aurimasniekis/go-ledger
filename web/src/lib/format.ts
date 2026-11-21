// Instants arrive as UTC RFC 3339 text and are shown in the browser's locale and zone.
// The reporting timezone (an open decision) governs dates in reports, not these timestamps.
const fmt = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })

export function formatInstant(v: string | number | null | undefined): string {
  if (v === null || v === undefined) return '—'
  const d = new Date(v)
  return Number.isNaN(d.getTime()) ? '—' : fmt.format(d)
}
