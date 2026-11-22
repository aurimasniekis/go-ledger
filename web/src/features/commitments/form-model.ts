import { isIsoDate } from '@/lib/format'
import type { Commitment, CommitmentKind, CommitmentPriority, LeewayKind } from './types'

/**
 * The commitment form's values, all as text so nothing is rounded or reinterpreted on the way: an amount stays the decimal
 * string the user typed (scale kept, ADR-0005) and is sent as that string. Nothing here does arithmetic on money.
 */
export interface FormValues {
  name: string
  kind: CommitmentKind
  amount: string
  currency: string
  frequency: 'monthly' | 'annual'
  interval: string
  day: string
  month: string
  startDate: string
  endDate: string
  priority: '' | CommitmentPriority
  notes: string
  tolerance: string
  leewayKind: '' | LeewayKind
  leewayDays: string
  leewayUntil: string
  conditions: string
  consequences: string
  source: string
  /** Edit only: the date changed terms reach occurrences from; empty is the server's default (today in its reporting zone). */
  effectiveFrom: string
}

export const emptyValues = (): FormValues => ({
  name: '', kind: 'subscription', amount: '', currency: '', frequency: 'monthly', interval: '1', day: '', month: '1', startDate: '', endDate: '',
  priority: '', notes: '', tolerance: '', leewayKind: '', leewayDays: '', leewayUntil: '', conditions: '', consequences: '', source: '', effectiveFrom: '',
})

export function toValues(c: Commitment): FormValues {
  const s = c.schedule
  return {
    name: c.name, kind: c.kind, amount: c.amount.amount, currency: c.amount.currency,
    frequency: s.frequency, interval: s.frequency === 'monthly' ? String(s.interval) : '1', day: String(s.day), month: s.frequency === 'annual' ? String(s.month) : '1',
    startDate: c.start_date, endDate: c.end_date ?? '', priority: c.priority ?? '', notes: c.notes ?? '', tolerance: c.match_tolerance?.amount ?? '',
    leewayKind: c.leeway?.kind ?? '', leewayDays: c.leeway?.days != null ? String(c.leeway.days) : '', leewayUntil: c.leeway?.until ?? '',
    conditions: c.leeway?.conditions ?? '', consequences: c.leeway?.consequences ?? '', source: c.leeway?.source ?? '', effectiveFrom: '',
  }
}

const DECIMAL = /^\d+(\.\d+)?$/
const WHOLE = /^\d+$/

/** Client-side checks that only save a round trip; the server stays authoritative and its answers are shown per field too. */
export function validate(v: FormValues): Partial<Record<keyof FormValues, string>> {
  const e: Partial<Record<keyof FormValues, string>> = {}
  if (!v.name.trim()) e.name = 'Give the commitment a name.'
  else if ([...v.name.trim()].length > 100) e.name = 'The name can be at most 100 characters.'
  if (!DECIMAL.test(v.amount.trim()) || /^0+(\.0+)?$/.test(v.amount.trim())) e.amount = 'Enter an amount greater than zero as a plain decimal, for example 12.50.'
  if (!/^[A-Z]{3}$/.test(v.currency.trim())) e.currency = 'Enter the currency as three capital letters, for example EUR.'
  const day = Number(v.day)
  if (!WHOLE.test(v.day.trim()) || day < 1 || day > 31) e.day = 'Enter a day of the month from 1 to 31. A day past the end of a shorter month falls on its last day.'
  if (v.frequency === 'monthly') {
    const n = Number(v.interval)
    if (!WHOLE.test(v.interval.trim()) || n < 1 || n > 120) e.interval = 'Enter a number of months from 1 to 120.'
  } else {
    const m = Number(v.month)
    if (!WHOLE.test(v.month.trim()) || m < 1 || m > 12) e.month = 'Choose a month.'
  }
  if (!isIsoDate(v.startDate.trim())) e.startDate = 'Enter the start date as YYYY-MM-DD, for example 2026-11-01.'
  if (v.endDate.trim() && !isIsoDate(v.endDate.trim())) e.endDate = 'Enter the end date as YYYY-MM-DD, or leave it empty.'
  else if (v.endDate.trim() && !e.startDate && v.endDate.trim() < v.startDate.trim()) e.endDate = 'The end date cannot be before the start date.'
  if (v.tolerance.trim() && !DECIMAL.test(v.tolerance.trim())) e.tolerance = 'Enter the tolerance as a plain decimal such as 0.50, or leave it empty.'
  if (v.notes.length > 2000) e.notes = 'Notes can be at most 2000 characters.'
  if (v.leewayKind === 'period') {
    const n = Number(v.leewayDays)
    if (!WHOLE.test(v.leewayDays.trim()) || n < 1 || n > 365) e.leewayDays = 'Enter a number of days from 1 to 365, or choose "No grace".'
  }
  if (v.leewayKind === 'deadline' && !isIsoDate(v.leewayUntil.trim())) e.leewayUntil = 'Enter the deadline as YYYY-MM-DD.'
  if (v.effectiveFrom.trim() && !isIsoDate(v.effectiveFrom.trim())) e.effectiveFrom = 'Enter the date as YYYY-MM-DD, or leave it empty for today.'
  return e
}

const text = (s: string) => (s.trim() === '' ? null : s.trim())

function scheduleBody(v: FormValues) {
  // Members that do not belong to the frequency are sent as null: in a patch that removes them (a merge patch), and the
  // server refuses them otherwise.
  return v.frequency === 'monthly'
    ? { version: 1, frequency: 'monthly', interval: Number(v.interval), day: Number(v.day), month: null }
    : { version: 1, frequency: 'annual', month: Number(v.month), day: Number(v.day), interval: null }
}

function leewayBody(v: FormValues) {
  if (v.leewayKind === '') return null
  return {
    kind: v.leewayKind,
    days: v.leewayKind === 'period' ? Number(v.leewayDays) : null,
    until: v.leewayKind === 'deadline' ? v.leewayUntil.trim() : null,
    conditions: text(v.conditions), consequences: text(v.consequences), source: text(v.source),
  }
}

const money = (amount: string, currency: string) => ({ amount: amount.trim(), currency: currency.trim() })

/** The body of a create: every member the API accepts. */
export function createBody(v: FormValues): Record<string, unknown> {
  const sch = scheduleBody(v)
  const body: Record<string, unknown> = {
    name: v.name.trim(), kind: v.kind, amount: money(v.amount, v.currency),
    schedule: Object.fromEntries(Object.entries(sch).filter(([, x]) => x !== null)),
    start_date: v.startDate.trim(), end_date: text(v.endDate), priority: v.priority || null, notes: text(v.notes),
  }
  if (v.tolerance.trim()) body.match_tolerance = money(v.tolerance, v.currency)
  const lw = leewayBody(v)
  if (lw) body.leeway = lw
  return body
}

/**
 * The merge patch of an edit: only members that differ from what was loaded. A changed nested member (amount, schedule,
 * match tolerance, leeway) is sent whole, with explicit nulls where a member goes away. Empty when nothing changed.
 */
export function patchBody(c: Commitment, v: FormValues): Record<string, unknown> {
  const o = toValues(c)
  const p: Record<string, unknown> = {}
  if (v.name.trim() !== o.name) p.name = v.name.trim()
  if (v.kind !== o.kind) p.kind = v.kind
  // A currency change moves the tolerance's currency too (it is always in the commitment's currency).
  const toleranceMoved = v.tolerance.trim() !== o.tolerance.trim() || (v.tolerance.trim() !== '' && v.currency.trim() !== o.currency)
  if (v.amount.trim() !== o.amount || v.currency.trim() !== o.currency) p.amount = money(v.amount, v.currency)
  if (v.frequency !== o.frequency || v.day.trim() !== o.day || v.interval.trim() !== o.interval || v.month.trim() !== o.month) {
    // Compare only the members that apply to the frequency.
    const monthly = v.frequency === 'monthly'
    const same = v.frequency === o.frequency && v.day.trim() === o.day && (monthly ? v.interval.trim() === o.interval : v.month.trim() === o.month)
    if (!same) p.schedule = scheduleBody(v)
  }
  if (v.startDate.trim() !== o.startDate) p.start_date = v.startDate.trim()
  if (v.endDate.trim() !== o.endDate) p.end_date = text(v.endDate)
  if (v.priority !== o.priority) p.priority = v.priority || null
  if (v.notes.trim() !== o.notes.trim()) p.notes = text(v.notes)
  if (toleranceMoved) p.match_tolerance = v.tolerance.trim() ? money(v.tolerance, v.currency) : null
  const lwChanged =
    v.leewayKind !== o.leewayKind || (v.leewayKind !== '' && (v.conditions.trim() !== o.conditions.trim() || v.consequences.trim() !== o.consequences.trim() || v.source.trim() !== o.source.trim() ||
      (v.leewayKind === 'period' && v.leewayDays.trim() !== o.leewayDays) || (v.leewayKind === 'deadline' && v.leewayUntil.trim() !== o.leewayUntil)))
  if (lwChanged) p.leeway = leewayBody(v)
  if (Object.keys(p).length > 0 && v.effectiveFrom.trim() && changesOccurrences(p)) p.effective_from = v.effectiveFrom.trim()
  return p
}

/** True when a patch touches what occurrences are made from: amount, schedule, start or end date (ADR-0027, point 4). */
export const changesOccurrences = (p: Record<string, unknown>) => ['amount', 'schedule', 'start_date', 'end_date'].some((k) => k in p)

/** Request member names (as the server reports them) mapped to the form fields that show them. */
const FIELD_OF: [string, keyof FormValues][] = [
  ['amount.amount', 'amount'], ['amount.currency', 'currency'], ['amount', 'amount'],
  ['schedule.interval', 'interval'], ['schedule.month', 'month'], ['schedule.day', 'day'], ['schedule.frequency', 'frequency'], ['schedule.version', 'frequency'], ['schedule', 'frequency'],
  ['start_date', 'startDate'], ['end_date', 'endDate'], ['match_tolerance', 'tolerance'],
  ['leeway.kind', 'leewayKind'], ['leeway.days', 'leewayDays'], ['leeway.until', 'leewayUntil'], ['leeway.conditions', 'conditions'], ['leeway.consequences', 'consequences'], ['leeway.source', 'source'],
  ['effective_from', 'effectiveFrom'], ['name', 'name'], ['kind', 'kind'], ['priority', 'priority'], ['notes', 'notes'], ['leeway', 'leewayKind'],
]
export function formFieldOf(serverField: string): keyof FormValues | undefined {
  return FIELD_OF.find(([k]) => serverField === k || serverField.startsWith(k + '.'))?.[1]
}

/** A short plain-language form of a schedule, for list rows and notes. */
export function describeSchedule(s: Commitment['schedule']): string {
  const ord = (n: number) => (n % 100 >= 11 && n % 100 <= 13 ? n + 'th' : n + ({ 1: 'st', 2: 'nd', 3: 'rd' }[n % 10] ?? 'th'))
  if (s.frequency === 'monthly') return s.interval === 1 ? `Monthly on the ${ord(s.day)}` : `Every ${s.interval} months on the ${ord(s.day)}`
  const name = new Intl.DateTimeFormat('en', { month: 'long', timeZone: 'UTC' }).format(new Date(Date.UTC(2001, s.month - 1, 1)))
  return `Yearly on ${name} ${s.day}`
}

/** Today as YYYY-MM-DD in UTC (the browser's clock; the server decides by the reporting zone). */
export const todayIso = (now: Date = new Date()) => now.toISOString().slice(0, 10)

/**
 * Whether this edit makes the server remove unpaid occurrences that are already overdue (removeOutOfRange in the occurrence store):
 * a new end date before today, or a new start date later than the current one. Null when it does not.
 * Occurrences with payment evidence or a status override stay; ones only deleted payments refer to are cancelled, not deleted.
 */
export function overdueRemovalWarning(c: Commitment, v: FormValues, today: string = todayIso()): string | null {
  const p = patchBody(c, v)
  const parts: string[] = []
  if (typeof p.end_date === 'string' && p.end_date < today) {
    parts.push(`The end date ${p.end_date} is before today. Unpaid occurrences due after it will be removed, including overdue ones, so they disappear from the monthly view and the outlook.`)
  }
  if (typeof p.start_date === 'string' && p.start_date > c.start_date) {
    parts.push(`The start date moves later, to ${p.start_date}. Unpaid occurrences due before it will be removed, including overdue ones, so they disappear from the monthly view and the outlook.`)
  }
  if (parts.length === 0) return null
  return parts.join(' ') + ' Occurrences with a payment or a status override you set stay. Undo does not bring back occurrences before today.'
}
