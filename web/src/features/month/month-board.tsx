import { ChevronLeft, ChevronRight, RefreshCw } from 'lucide-react'
import { Link } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import { StatusBadge, type Status } from '@/components/status-badge'
import { DisconnectedState, EmptyState, FailedState, LoadingState } from '@/components/state-panels'
import { Amount, DateText } from '@/components/formatted'
import { isNetworkError } from '@/lib/connection'
import type { ReactNode, Ref } from 'react'
import { ProvenanceMarker, provenanceOf } from './provenance'
import { ageLabel, isZeroAmount, monthLabel, shiftMonth, utcStamp } from './month'
import type { MonthItem, MonthSummary, MonthTotals, PaymentStatus } from './types'

export const OFFLINE_NOTE_ID = 'record-offline-note'
export const REFRESH_OFFLINE_ID = 'refresh-offline-note'

/**
 * How old fetched data may get before the view calls it stale. A placeholder:
 * the freshness thresholds are an unresolved product decision.
 */
export const STALE_AFTER_MS = 5 * 60 * 1000

export interface MonthBoardProps {
  month: string
  /** The query state: pending (first load), error or success. A failed refetch keeps the last data. */
  status: 'pending' | 'error' | 'success'
  data?: MonthSummary
  error?: Error | null
  isFetching: boolean
  /** When the data was fetched (ms since epoch). */
  dataUpdatedAt: number
  now: number
  onMonthChange: (month: string) => void
  onRetry: () => void
  onRecord: (item: MonthItem) => void
  /** The record-payment form, shown in place under the row with this id (the row that opened it). */
  formFor?: string | null
  form?: ReactNode
  /** Whether the session may open the bank connections page (connections:read); the link is hidden otherwise. */
  canViewConnections?: boolean
  /** True while the browser reports no network: recording a payment cannot be saved then. */
  offline?: boolean
  /** The Undo notice, placed right under the month navigation so it reads as part of the page, not of the title. */
  notice?: ReactNode
  /** Occurrences with a bank suggestion waiting for review: their rows point to /matches (unknown when it could not load). */
  waiting?: ReadonlySet<string>
  /** Receives the page title (h1) so focus can fall back to it. */
  titleRef?: Ref<HTMLHeadingElement>
}

/** A money amount: the amount with its currency, one line, never broken (the shared formatter for the month views). */
export const money = (m: { amount: string; currency: string }) => <Amount amount={m.amount} currency={m.currency} />

export function MonthBoard({ titleRef, ...p }: MonthBoardProps) {
  const label = monthLabel(p.month)
  return (
    <div className="space-y-4">
      <div role="group" aria-label="Month" className="flex flex-wrap items-center gap-2">
        <Button variant="outline" size="sm" onClick={() => p.onMonthChange(shiftMonth(p.month, -1))}>
          <ChevronLeft aria-hidden className="size-4" />
          <span className="sr-only">Previous month, {monthLabel(shiftMonth(p.month, -1))}</span>
          <span aria-hidden className="hidden sm:inline">Previous</span>
        </Button>
        <h1 id="month-title" ref={titleRef} tabIndex={-1} className="max-w-full grow basis-32 text-center text-xl font-semibold focus:outline-none sm:w-48 sm:grow-0 sm:basis-auto">
          {label}
        </h1>
        <Button variant="outline" size="sm" onClick={() => p.onMonthChange(shiftMonth(p.month, 1))}>
          <span aria-hidden className="hidden sm:inline">Next</span>
          <span className="sr-only">Next month, {monthLabel(shiftMonth(p.month, 1))}</span>
          <ChevronRight aria-hidden className="size-4" />
        </Button>
      </div>

      {p.notice}

      {p.status === 'pending' && <LoadingState label={label} />}
      {p.status === 'error' && !p.data && (
        isNetworkError(p.error) ? (
          <DisconnectedState onRetry={p.onRetry}>No month has loaded yet, so there is nothing to show. Nothing was changed.</DisconnectedState>
        ) : (
          <FailedState title="Could not load this month" error={p.error} onRetry={p.onRetry}>
            Another month may still load.
          </FailedState>
        )
      )}
      {p.data && (
        <>
          <Freshness {...p} data={p.data} />
          {/* The form opened from the header sits above the list; one opened from a row sits under that row. */}
          {!p.formFor && p.form}
          {p.data.items.length === 0 && !(p.form && p.formFor) ? (
            <EmptyState id="month-empty" level={2} title={`No commitments fall due in ${label}`}>
              Nothing is scheduled for this month. A commitment shows here once its schedule has a due date in it; use the month buttons to look at another month.
            </EmptyState>
          ) : (
            <>
              {p.data.items.length > 0 && <Totals totals={p.data.totals} />}
              <Occurrences items={p.data.items} onRecord={p.onRecord} offline={p.offline} formFor={p.formFor} form={p.form} waiting={p.waiting} />
            </>
          )}
        </>
      )}
    </div>
  )
}

/** What the view knows about the age of what it shows; stale, failed and limited states are all visible. */
function Freshness(p: MonthBoardProps & { data: MonthSummary }) {
  const { data } = p
  const fetchedAge = ageLabel(p.dataUpdatedAt, p.now)
  const failedRefresh = p.status === 'error'
  const stale = !failedRefresh && p.now - p.dataUpdatedAt > STALE_AFTER_MS
  return (
    <section aria-label="Data freshness" className="space-y-2 rounded-md border border-border bg-surface p-3 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        {p.offline ? (
          <StatusBadge status="disconnected" label="Offline" />
        ) : failedRefresh ? (
          <StatusBadge status="error" label="Refresh failed" />
        ) : stale ? (
          <StatusBadge status="stale" label={`Stale, fetched ${fetchedAge}`} />
        ) : null}
        <span>
          Statuses as of <DateText value={data.as_of} /> (reporting time zone); fetched{' '}
          <time dateTime={new Date(p.dataUpdatedAt).toISOString()}>{fetchedAge}</time>.
        </span>
        {p.isFetching && <span role="status">Refreshing…</span>}
        <Button
          variant="ghost"
          size="sm"
          onClick={p.onRetry}
          disabled={p.isFetching || p.offline}
          aria-describedby={p.offline ? REFRESH_OFFLINE_ID : undefined}
        >
          <RefreshCw aria-hidden className="size-4" />
          Refresh
        </Button>
      </div>
      {failedRefresh && (
        <p role="alert">
          The latest refresh failed ({p.error?.message ?? 'request failed'}). You are looking at the last data received, fetched{' '}
          {fetchedAge}; it may be out of date.
        </p>
      )}
      {p.offline && (
        <p id={REFRESH_OFFLINE_ID} className="text-muted-foreground">
          Refresh is unavailable while you are offline; the data shown is what was last fetched.
        </p>
      )}
      <p className="text-muted-foreground">
        {data.freshness.occurrences_changed_at ? (
          <>
            Occurrences last changed{' '}
            <time dateTime={data.freshness.occurrences_changed_at} className="whitespace-nowrap">
              {utcStamp(data.freshness.occurrences_changed_at)}
            </time>
            .{' '}
          </>
        ) : (
          ''
        )}
        {data.freshness.bank.available ? '' : data.freshness.bank.state === 'unknown' ? 'Bank status could not be read just now, so bank data age is unknown.' : 'No bank data connected yet.'}
      </p>
      {data.freshness.bank.available && (
        <BankLine
          bank={data.freshness.bank}
          matched={data.items.filter((i) => i.payment.bank_evidence_count > 0).length}
          canViewConnections={p.canViewConnections ?? false}
        />
      )}
    </section>
  )
}

const BANK_STATE_COPY: Record<string, string> = {
  fresh: 'Bank data is current',
  stale: 'Bank data is stale',
  never_refreshed: 'A bank is connected but no data has been fetched yet',
  failed: 'The latest bank fetch failed; bank data may be out of date',
  disconnected: 'A bank connection is disconnected; its data is not being updated',
  reconnect_required: 'A bank connection needs a new consent; its data is not being updated',
  unknown: 'Bank connection status could not be read, so its data age is unknown',
}

/** The bank half of the freshness: shown only when a connection exists, and never calls a non-fresh state fresh. */
function BankLine({ bank, matched, canViewConnections }: { bank: MonthSummary['freshness']['bank']; matched: number; canViewConnections: boolean }) {
  const ok = bank.state === 'fresh'
  return (
    <p data-testid="month-bank-line" className={ok ? 'text-muted-foreground' : undefined} role={ok ? undefined : 'status'}>
      {BANK_STATE_COPY[bank.state] ?? 'Bank data state is unknown'}
      {bank.last_success_at ? (
        <>
          {' '}(last successful fetch{' '}
          <time dateTime={bank.last_success_at} className="whitespace-nowrap">{utcStamp(bank.last_success_at)}</time>)
        </>
      ) : null}
      . {matched === 0 ? 'No bank transactions matched this month’s commitments.' : `${matched} ${matched === 1 ? 'commitment has' : 'commitments have'} a matched bank payment this month.`}{' '}
      {canViewConnections && <Link to="/connections" className="underline">Bank connections</Link>}
    </p>
  )
}

/** A money amount in a totals list: one line, never broken, whatever the width. */
const AMOUNT = 'mb-1 whitespace-nowrap font-mono sm:mb-0 sm:text-right'
const hasOverdue = (t: MonthTotals) => !isZeroAmount(t.overdue.amount)

function Totals({ totals }: { totals: MonthTotals[] }) {
  return (
    <section aria-labelledby="month-totals" className="space-y-2">
      <h2 id="month-totals" className="font-semibold">
        Recorded commitments, by currency
      </h2>
      <p className="text-sm text-muted-foreground">
        Each currency is totalled on its own; amounts in different currencies are never added together or converted. These
        totals cover recorded commitments and recorded payments only. They are not a forecast and not a measure of money
        that is safe to spend.
      </p>
      <ul className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        {totals.map((t) => (
          <li key={t.currency} className="min-w-0 rounded-md border border-border bg-surface p-3">
            <h3 className="font-semibold">{t.currency}</h3>
            {/* Below sm each label sits over its amount; from sm up label and amount share a row. Only labels wrap: an amount never breaks. */}
            <dl className="mt-1 grid grid-cols-1 gap-x-4 text-sm sm:grid-cols-[minmax(0,1fr)_auto] sm:gap-y-0.5">
              <dt>Scheduled this month</dt>
              <dd data-total-amount className={AMOUNT}>{money(t.due)}</dd>
              <dt>Paid</dt>
              <dd data-total-amount className={AMOUNT}>{money(t.paid)}</dd>
              <dt>Outstanding</dt>
              <dd data-total-amount className={AMOUNT}>{money(t.outstanding)}</dd>
              <dt className={hasOverdue(t) ? 'font-semibold' : undefined}>Overdue (past original due date)</dt>
              <dd data-total-amount className={hasOverdue(t) ? AMOUNT + ' font-semibold text-status-overdue' : AMOUNT}>
                {money(t.overdue)}
              </dd>
              {t.overdue_count > 0 && (
                <dd className="text-xs text-muted-foreground sm:col-span-2 sm:text-right">
                  {t.overdue_count} overdue {t.overdue_count === 1 ? 'occurrence' : 'occurrences'}
                </dd>
              )}
            </dl>
            {!isZeroAmount(t.overpaid.amount) && (
              <p className="mt-1 text-xs text-muted-foreground">Includes {money(t.overpaid)} paid above the amounts due.</p>
            )}
            {t.unconverted_evidence_count > 0 && (
              <p className="mt-1 text-xs text-muted-foreground">
                {t.unconverted_evidence_count} occurrence(s) have payments in another currency, which are not counted here.
              </p>
            )}
          </li>
        ))}
      </ul>
    </section>
  )
}

function Occurrences({
  items,
  onRecord,
  offline,
  formFor,
  form,
  waiting,
}: {
  waiting?: ReadonlySet<string>
  items: MonthItem[]
  onRecord: (i: MonthItem) => void
  offline?: boolean
  formFor?: string | null
  form?: ReactNode
}) {
  const rows = items.map((i) => (
    <OccurrenceRow key={i.id} item={i} onRecord={onRecord} offline={offline} form={i.id === formFor ? form : null} suggestionWaiting={waiting?.has(i.id)} />
  ))
  // The row that opened the form left the list (paid elsewhere): its place stays, in the same list under the
  // same key, so the open form keeps what was typed and can say so.
  if (form && formFor && !items.some((i) => i.id === formFor)) {
    rows.push(<OccurrenceRow key={formFor} item={null} onRecord={onRecord} form={form} />)
  }
  return (
    <section aria-labelledby="month-occurrences" className="space-y-2">
      <h2 id="month-occurrences" className="font-semibold">
        Occurrences
      </h2>
      {items.length > 0 && (
        <p data-testid="status-summary" className="text-sm text-muted-foreground">
          {statusSummary(items)}
        </p>
      )}
      <ul className="space-y-2">{rows}</ul>
    </section>
  )
}

/** "2 overdue · 1 due today · 2 upcoming · 2 paid": a count per status, so the list can be read at a glance. */
export function statusSummary(items: MonthItem[]): string {
  const order: [PaymentStatus, string][] = [
    ['overdue', 'overdue'],
    ['due', 'due today'],
    ['partially_paid', 'partly paid'],
    ['upcoming', 'upcoming'],
    ['paid', 'paid'],
    ['not_recorded', 'not recorded'],
  ]
  return order
    .map(([s, label]) => {
      const rows = items.filter((i) => i.payment.status === s)
      // The server's overdue_count counts every unpaid-or-partly-paid row whose timing is overdue: a partly paid
      // row with an overdue remainder is one of them, so say so instead of hiding it under "partly paid".
      const late = s === 'partially_paid' ? rows.filter((i) => i.payment.timing === 'overdue').length : 0
      return [rows.length, late > 0 ? `${label} (${late} with an overdue remainder)` : label] as const
    })
    .filter(([n]) => n > 0)
    .map(([n, label]) => `${n} ${label}`)
    .join(' · ')
}

function statusOf(i: MonthItem): Status {
  return i.payment.status
}

function OccurrenceRow({ item: i, onRecord, offline, form, suggestionWaiting }: { item: MonthItem | null; onRecord: (i: MonthItem) => void; offline?: boolean; form?: ReactNode; suggestionWaiting?: boolean }) {
  // No item: the occurrence the open form came from has left the list. Same component, same key, so the form stays mounted.
  if (!i) {
    return (
      <li className="space-y-2 rounded-md border border-border bg-surface p-3">
        <div className="space-y-1">
          <p className="text-sm text-muted-foreground">The occurrence this form was opened from is no longer in this month's list.</p>
        </div>
        {form}
      </li>
    )
  }
  const p = i.payment
  const settled = p.status === 'paid' || p.status === 'cancelled'
  const provenance = provenanceOf(i)
  return (
    <li className="space-y-2 rounded-md border border-border bg-surface p-3">
      <div className="space-y-1">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0 flex-1">
          <p className="font-medium break-words">{i.commitment_name || i.commitment_id}</p>
          <p className="text-sm text-muted-foreground">
            Original due date <DateText value={i.due_date} />
          </p>
        </div>
        <p className="shrink-0 whitespace-nowrap font-mono">{money(i.amount)}</p>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <StatusBadge status={statusOf(i)} />
        {p.status === 'partially_paid' && (p.timing === 'overdue' || p.timing === 'due') && (
          <StatusBadge status={p.timing} label={p.timing === 'overdue' ? 'Remainder overdue' : 'Remainder due today'} />
        )}
        {provenance && <ProvenanceMarker kind={provenance} />}
        {p.override?.applied && <span className="text-sm">Marked {p.override.status}: {p.override.reason}</span>}
        {!settled && (
          <Button
            variant="ghost"
            size="sm"
            className="ml-auto h-auto min-h-8 whitespace-normal py-1 text-foreground underline underline-offset-4"
            onClick={() => onRecord(i)}
            disabled={offline}
            aria-describedby={offline ? OFFLINE_NOTE_ID : undefined}
            aria-label={`Record payment for ${i.commitment_name || i.commitment_id}, due ${i.due_date}`}
          >
            Record payment
          </Button>
        )}
      </div>
      {p.timing === 'overdue' && p.grace_until && (
        <p className="text-sm">
          {p.within_grace ? (
            <>Overdue since the original due date; within documented grace until <DateText value={p.grace_until} />.</>
          ) : (
            <>Overdue; documented grace ended <DateText value={p.grace_until} />.</>
          )}
        </p>
      )}
      {suggestionWaiting && !settled && (
        <p className="text-sm" data-testid="suggestion-waiting">
          A bank suggestion is waiting for this occurrence.{' '}
          <Link to="/matches" className="underline underline-offset-4">Review it before recording a payment by hand</Link>, so the same payment is not counted twice.
        </p>
      )}
      {p.timing === 'not_recorded' && (
        <p className="text-sm text-muted-foreground">Due before this commitment was created: not counted in the overdue or outstanding totals. You can still record a payment.</p>
      )}
      {p.status === 'paid' && !isZeroAmount(p.shortfall_amount.amount) && (
        <p className="text-sm">Paid within the commitment's tolerance; {money(p.shortfall_amount)} short.</p>
      )}
      {p.status === 'partially_paid' && (
        <p className="text-sm">
          Paid {money(p.paid_amount)}; outstanding {money(p.outstanding_amount)}.
        </p>
      )}
      {!isZeroAmount(p.overpaid_amount.amount) && <p className="text-sm">Paid {money(p.overpaid_amount)} more than the amount due.</p>}
      {p.unconverted_currencies.length > 0 && (
        <p className="text-sm text-muted-foreground">
          Payments in {p.unconverted_currencies.join(', ')} are recorded but not counted: they are never converted.
        </p>
      )}
      </div>
      {form}
    </li>
  )
}
