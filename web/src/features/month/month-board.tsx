import { ChevronLeft, ChevronRight, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { StatusBadge, type Status } from '@/components/status-badge'
import { ApiError } from '@/lib/api'
import { ageLabel, isZeroAmount, monthLabel, shiftMonth, utcStamp } from './month'
import type { MonthItem, MonthSummary, MonthTotals } from './types'

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
}

const money = (m: { amount: string; currency: string }) => `${m.amount} ${m.currency}`

export function MonthBoard(p: MonthBoardProps) {
  const label = monthLabel(p.month)
  return (
    <div className="space-y-4">
      <nav aria-label="Month" className="flex flex-wrap items-center gap-2">
        <Button variant="outline" size="sm" onClick={() => p.onMonthChange(shiftMonth(p.month, -1))}>
          <ChevronLeft aria-hidden className="size-4" />
          <span className="sr-only">Previous month, {monthLabel(shiftMonth(p.month, -1))}</span>
          <span aria-hidden className="hidden sm:inline">Previous</span>
        </Button>
        <h2 className="min-w-36 text-center text-lg font-semibold" aria-live="polite">
          {label}
        </h2>
        <Button variant="outline" size="sm" onClick={() => p.onMonthChange(shiftMonth(p.month, 1))}>
          <span aria-hidden className="hidden sm:inline">Next</span>
          <span className="sr-only">Next month, {monthLabel(shiftMonth(p.month, 1))}</span>
          <ChevronRight aria-hidden className="size-4" />
        </Button>
      </nav>

      {p.status === 'pending' && <Loading label={label} />}
      {p.status === 'error' && !p.data && <Failed error={p.error} onRetry={p.onRetry} />}
      {p.data && (
        <>
          <Freshness {...p} data={p.data} />
          {p.data.items.length === 0 ? <Empty label={label} /> : (
            <>
              <Totals totals={p.data.totals} />
              <Occurrences items={p.data.items} onRecord={p.onRecord} />
            </>
          )}
        </>
      )}
    </div>
  )
}

function Loading({ label }: { label: string }) {
  return (
    <div role="status" aria-busy="true" className="space-y-2 rounded-md border border-border bg-surface p-4">
      <p className="text-muted-foreground">Loading {label}…</p>
      <div aria-hidden className="h-4 w-2/3 rounded-sm bg-muted" />
      <div aria-hidden className="h-4 w-1/2 rounded-sm bg-muted" />
    </div>
  )
}

function Empty({ label }: { label: string }) {
  return (
    <section aria-labelledby="month-empty" className="space-y-1 rounded-md border border-border bg-surface p-4">
      <h3 id="month-empty" className="font-semibold">
        No commitments fall due in {label}
      </h3>
      <p className="text-muted-foreground">
        Nothing is scheduled for this month. A commitment shows here once its schedule has a due date in it; use the month
        buttons to look at another month.
      </p>
    </section>
  )
}

function Failed({ error, onRetry }: { error?: Error | null; onRetry: () => void }) {
  const api = error instanceof ApiError ? error : null
  return (
    <div role="alert" className="space-y-2 rounded-md border border-border-control bg-surface p-4">
      <StatusBadge status="error" label="Could not load this month" />
      <p>{error?.message ?? 'The request failed.'}</p>
      <p className="text-sm text-muted-foreground">
        Nothing was changed. {api?.requestId ? <>Reference <code>{api.requestId}</code>. </> : null}
        Another month may still load.
      </p>
      <Button variant="outline" size="sm" onClick={onRetry}>
        <RefreshCw aria-hidden className="size-4" />
        Try again
      </Button>
    </div>
  )
}

/** What the view knows about the age of what it shows; stale, failed and limited states are all visible. */
function Freshness(p: MonthBoardProps & { data: MonthSummary }) {
  const { data } = p
  const fetchedAge = ageLabel(p.dataUpdatedAt, p.now)
  const failedRefresh = p.status === 'error'
  const stale = failedRefresh || p.now - p.dataUpdatedAt > STALE_AFTER_MS
  return (
    <section aria-label="Data freshness" className="space-y-2 rounded-md border border-border bg-surface p-3 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        {failedRefresh ? (
          <StatusBadge status="error" label="Refresh failed" />
        ) : stale ? (
          <StatusBadge status="stale" label={`Stale, fetched ${fetchedAge}`} />
        ) : null}
        <span>
          Statuses as of <time dateTime={data.as_of}>{data.as_of}</time> (reporting time zone); fetched{' '}
          <time dateTime={new Date(p.dataUpdatedAt).toISOString()}>{fetchedAge}</time>.
        </span>
        {p.isFetching && <span role="status">Refreshing…</span>}
        <Button variant="ghost" size="sm" onClick={p.onRetry} disabled={p.isFetching}>
          <RefreshCw aria-hidden className="size-4" />
          Refresh
        </Button>
      </div>
      {failedRefresh && (
        <p role="alert">
          The latest refresh failed ({p.error?.message ?? 'request failed'}). You are looking at the last data received, from{' '}
          {utcStamp(data.freshness.computed_at)}; it may be out of date.
        </p>
      )}
      <p className="text-muted-foreground">
        {data.freshness.occurrences_changed_at
          ? `Occurrences last changed ${utcStamp(data.freshness.occurrences_changed_at)}. `
          : ''}
        {data.freshness.bank.available ? '' : `Bank data age: not shown. ${data.freshness.bank.note}`}
      </p>
    </section>
  )
}

function Totals({ totals }: { totals: MonthTotals[] }) {
  return (
    <section aria-labelledby="month-totals" className="space-y-2">
      <h3 id="month-totals" className="font-semibold">
        Recorded commitments, by currency
      </h3>
      <p className="text-sm text-muted-foreground">
        Each currency is totalled on its own; amounts in different currencies are never added together or converted. These
        totals cover recorded commitments and recorded payments only. They are not a forecast and not a measure of money
        that is safe to spend.
      </p>
      <ul className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        {totals.map((t) => (
          <li key={t.currency} className="rounded-md border border-border bg-surface p-3">
            <h4 className="font-semibold">{t.currency}</h4>
            <dl className="mt-1 grid grid-cols-[auto_1fr] gap-x-4 gap-y-0.5 text-sm">
              <dt>Due</dt>
              <dd className="text-right font-mono">{t.due.amount}</dd>
              <dt>Paid</dt>
              <dd className="text-right font-mono">{t.paid.amount}</dd>
              <dt>Outstanding</dt>
              <dd className="text-right font-mono">{t.outstanding.amount}</dd>
              <dt>Overdue (past original due date)</dt>
              <dd className="text-right font-mono">
                {t.overdue.amount}
                {t.overdue_count > 0 && <span className="ml-1 font-sans text-muted-foreground">({t.overdue_count})</span>}
              </dd>
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

function Occurrences({ items, onRecord }: { items: MonthItem[]; onRecord: (i: MonthItem) => void }) {
  return (
    <section aria-labelledby="month-occurrences" className="space-y-2">
      <h3 id="month-occurrences" className="font-semibold">
        Occurrences
      </h3>
      <ul className="space-y-2">
        {items.map((i) => (
          <OccurrenceRow key={i.id} item={i} onRecord={onRecord} />
        ))}
      </ul>
    </section>
  )
}

function statusOf(i: MonthItem): Status {
  return i.payment.status
}

function OccurrenceRow({ item: i, onRecord }: { item: MonthItem; onRecord: (i: MonthItem) => void }) {
  const p = i.payment
  const settled = p.status === 'paid'
  return (
    <li className="space-y-1 rounded-md border border-border bg-surface p-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="font-medium break-words">{i.commitment_name || i.commitment_id}</p>
          <p className="text-sm text-muted-foreground">
            Original due date <time dateTime={i.due_date}>{i.due_date}</time>
          </p>
        </div>
        <p className="font-mono">{money(i.amount)}</p>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <StatusBadge status={statusOf(i)} />
        {p.status === 'partially_paid' && (p.timing === 'overdue' || p.timing === 'due') && (
          <StatusBadge status={p.timing} label={p.timing === 'overdue' ? 'Remainder overdue' : 'Remainder due today'} />
        )}
        {p.override?.applied && <span className="text-sm">Set by hand to {p.override.status} ({p.override.reason})</span>}
        {!settled && (
          <Button
            variant="outline"
            size="sm"
            className="ml-auto"
            onClick={() => onRecord(i)}
            aria-label={`Record payment for ${i.commitment_name || i.commitment_id}, due ${i.due_date}`}
          >
            Record payment
          </Button>
        )}
      </div>
      {p.timing === 'overdue' && p.grace_until && (
        <p className="text-sm">
          {p.within_grace ? (
            <>Overdue since the original due date; within documented grace until <time dateTime={p.grace_until}>{p.grace_until}</time>.</>
          ) : (
            <>Overdue; documented grace ended <time dateTime={p.grace_until}>{p.grace_until}</time>.</>
          )}
        </p>
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
    </li>
  )
}
