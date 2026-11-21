import { useEffect, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { ChevronLeft, ChevronRight, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { StatusBadge, type Status } from '@/components/status-badge'
import { DisconnectedState, EmptyState, FailedState, LoadingState } from '@/components/state-panels'
import { Amount, DateText } from '@/components/formatted'
import { isNetworkError, useOnline } from '@/lib/connection'
import { STALE_AFTER_MS } from '@/features/month/month-board'
import { ageLabel, isZeroAmount, monthLabel, shiftMonth, utcStamp } from '@/features/month/month'
import type { Money } from '@/features/month/types'
import { useOutlook } from './api'
import type { ExchangeRate, Outlook, OutlookAccount, OutlookConverted, OutlookCurrency } from './types'

const money = (m: Money) => <Amount amount={m.amount} currency={m.currency} />
const AMOUNT = 'mb-1 whitespace-nowrap font-mono sm:mb-0 sm:text-right'

const FRESHNESS: Record<OutlookAccount['freshness']['state'], { status: Status | null; label: string }> = {
  fresh: { status: null, label: 'Fresh' },
  stale: { status: 'stale', label: 'Stale' },
  failed: { status: 'error', label: 'Last refresh failed' },
  reconnect_required: { status: 'disconnected', label: 'Reconnect required' },
  disconnected: { status: 'disconnected', label: 'Disconnected' },
  never_refreshed: { status: 'stale', label: 'Never refreshed' },
}

const NOT_COUNTED: Record<NonNullable<OutlookAccount['not_counted_reason']>, string> = {
  no_balance: 'Not counted: the bank has reported no balance for this account.',
  no_longer_reported: 'Not counted: the bank no longer reports this account.',
  unsupported_balance_type: 'Not counted: the bank reports only a balance type Ledger cannot interpret.',
}

const EXCLUDED_REASON: Record<string, string> = {
  not_retrieved_recently: 'the exchange rate was last retrieved too long ago',
  rate_date_too_old: 'the newest exchange rate is too old',
  unconfirmed: 'the exchange rate could not be confirmed',
  not_cached: 'no exchange rate is available and the rate provider could not be reached',
  not_published: 'the rate provider publishes no rate for it',
  no_provider: 'no exchange-rate provider is configured',
  conversion_failed: 'the conversion failed',
}

/** The /outlook screen: bank balances compared with recorded commitments still due, per currency first. */
export function OutlookPage({ through, currency }: { through?: string; currency?: string }) {
  const navigate = useNavigate()
  const q = useOutlook(through, currency)
  const online = useOnline()
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 30_000)
    return () => clearInterval(t)
  }, [])
  const shownThrough = q.data?.window.through ?? through
  const go = (m: string) => void navigate({ to: '/outlook', search: { through: m, ...(currency ? { currency } : {}) } })

  return (
    <section className="space-y-4" aria-labelledby="outlook-title">
      <h1 id="outlook-title" className="text-xl font-semibold">
        Outlook against recorded commitments
      </h1>
      {q.status === 'pending' && <LoadingState label="the outlook" />}
      {q.status === 'error' && !q.data && (
        isNetworkError(q.error) ? (
          <DisconnectedState onRetry={() => void q.refetch()}>No outlook has loaded yet, so there is nothing to show. Nothing was changed.</DisconnectedState>
        ) : (
          <FailedState title="Could not load the outlook" error={q.error} onRetry={() => void q.refetch()} />
        )
      )}
      {q.data && (
        <>
          <p data-testid="outlook-notice" className="rounded-md border border-border bg-surface p-3 text-sm">
            {q.data.notice}
          </p>
          {shownThrough && (
            <div role="group" aria-label="Commitments counted through" className="flex flex-wrap items-center gap-2">
              <Button variant="outline" size="sm" disabled={shownThrough <= q.data.as_of.slice(0, 7)} onClick={() => go(shiftMonth(shownThrough, -1))}>
                <ChevronLeft aria-hidden className="size-4" />
                <span className="sr-only">Count commitments through {monthLabel(shiftMonth(shownThrough, -1))}</span>
                <span aria-hidden className="hidden sm:inline">Earlier</span>
              </Button>
              <p className="text-sm">
                Commitments due through <strong>{monthLabel(shownThrough)}</strong>
                {q.data.window.from ? <>, from {monthLabel(q.data.window.from)}</> : null}
              </p>
              <Button variant="outline" size="sm" onClick={() => go(shiftMonth(shownThrough, 1))}>
                <span aria-hidden className="hidden sm:inline">Later</span>
                <span className="sr-only">Count commitments through {monthLabel(shiftMonth(shownThrough, 1))}</span>
                <ChevronRight aria-hidden className="size-4" />
              </Button>
            </div>
          )}
          <Freshness data={q.data} status={q.status} error={q.error} isFetching={q.isFetching} dataUpdatedAt={q.dataUpdatedAt} now={now} offline={!online} onRetry={() => void q.refetch()} />
          {q.data.currencies.length === 0 ? (
            <EmptyState id="outlook-empty" level={2} title="Nothing to compare yet">
              No commitment is still due in this window{q.data.balances.shown ? ' and no bank balance is counted' : ''}. Add commitments or connect a bank to see the outlook.
            </EmptyState>
          ) : (
            <Currencies lines={q.data.currencies} balancesShown={q.data.balances.shown} />
          )}
          {q.data.converted && <Converted c={q.data.converted} balancesShown={q.data.balances.shown} />}
          <Accounts data={q.data} />
          <p className="text-sm text-muted-foreground">{q.data.planned_outflows.note}</p>
        </>
      )}
    </section>
  )
}

function Freshness(p: {
  data: Outlook
  status: 'pending' | 'error' | 'success'
  error: Error | null
  isFetching: boolean
  dataUpdatedAt: number
  now: number
  offline: boolean
  onRetry: () => void
}) {
  const fetchedAge = ageLabel(p.dataUpdatedAt, p.now)
  const failed = p.status === 'error'
  const stale = !failed && p.now - p.dataUpdatedAt > STALE_AFTER_MS
  return (
    <section aria-label="Data freshness" className="space-y-2 rounded-md border border-border bg-surface p-3 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        {p.offline ? (
          <StatusBadge status="disconnected" label="Offline" />
        ) : failed ? (
          <StatusBadge status="error" label="Refresh failed" />
        ) : stale ? (
          <StatusBadge status="stale" label={'Stale, fetched ' + fetchedAge} />
        ) : null}
        <span>
          Statuses as of <DateText value={p.data.as_of} /> (reporting time zone); fetched <time dateTime={new Date(p.dataUpdatedAt).toISOString()}>{fetchedAge}</time>.
        </span>
        {p.isFetching && <span role="status">Refreshing…</span>}
        <Button variant="ghost" size="sm" onClick={p.onRetry} disabled={p.isFetching || p.offline}>
          <RefreshCw aria-hidden className="size-4" />
          Refresh
        </Button>
      </div>
      {failed && (
        <p role="alert">
          The latest refresh failed ({p.error?.message ?? 'request failed'}). You are looking at the last data received, fetched {fetchedAge}; it may be out of date.
        </p>
      )}
      {p.data.window.truncated && <p>Occurrences more than 240 months before the last month counted are not included.</p>}
      <p className="text-muted-foreground">{p.data.balances.note}</p>
    </section>
  )
}

function Currencies({ lines, balancesShown }: { lines: OutlookCurrency[]; balancesShown: boolean }) {
  return (
    <section aria-labelledby="outlook-currencies" className="space-y-2">
      <h2 id="outlook-currencies" className="font-semibold">
        By currency
      </h2>
      <p className="text-sm text-muted-foreground">
        Each currency is compared on its own; amounts in different currencies are never added together here.
      </p>
      <ul className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        {lines.map((l) => (
          <li key={l.currency} data-testid={'outlook-' + l.currency} className="min-w-0 rounded-md border border-border bg-surface p-3">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="font-semibold">{l.currency}</h3>
              {l.stale_account_count > 0 && (
                <StatusBadge status="stale" label={l.stale_account_count + ' of ' + l.account_count + (l.account_count === 1 ? ' balance' : ' balances') + ' not fresh'} />
              )}
            </div>
            <dl className="mt-1 grid grid-cols-1 gap-x-4 text-sm sm:grid-cols-[minmax(0,1fr)_auto] sm:gap-y-0.5">
              <dt>Bank balance</dt>
              <dd className={AMOUNT}>
                {l.balance ? money(l.balance) : <span className="font-sans text-muted-foreground">{balancesShown ? 'No balance in ' + l.currency : 'Not shown'}</span>}
              </dd>
              <dt>Recorded commitments still due</dt>
              <dd className={AMOUNT}>{money(l.still_due)}</dd>
              <dt className={isZeroAmount(l.overdue.amount) ? undefined : 'font-semibold'}>of which overdue (past original due date)</dt>
              <dd className={isZeroAmount(l.overdue.amount) ? AMOUNT : AMOUNT + ' font-semibold text-status-overdue'}>{money(l.overdue)}</dd>
              <dt>of which upcoming</dt>
              <dd className={AMOUNT}>{money(l.upcoming)}</dd>
              {l.balance_less_still_due && (
                <>
                  <dt>Balance minus commitments still due</dt>
                  <dd className={AMOUNT}>{money(l.balance_less_still_due)}</dd>
                </>
              )}
            </dl>
            {l.oldest_balance_obtained_at && (
              <p className="mt-1 text-xs text-muted-foreground">
                Oldest balance obtained <time dateTime={l.oldest_balance_obtained_at}>{utcStamp(l.oldest_balance_obtained_at)}</time>.
              </p>
            )}
            {l.unconverted_evidence_count > 0 && (
              <p className="mt-1 text-xs text-muted-foreground">
                {l.unconverted_evidence_count} occurrence(s) have payments in another currency, which are not counted here.
              </p>
            )}
          </li>
        ))}
      </ul>
    </section>
  )
}

function rateText(r: ExchangeRate): string {
  return '1 ' + r.base + ' = ' + r.value + ' ' + r.quote + ', rate of ' + r.date + ' from ' + r.provider + ' (' + r.source + '), retrieved ' + utcStamp(r.retrieved_at)
}

function Converted({ c, balancesShown }: { c: OutlookConverted; balancesShown: boolean }) {
  return (
    <section aria-labelledby="outlook-converted" className="space-y-2 rounded-md border border-border p-3">
      <h2 id="outlook-converted" className="font-semibold">
        Converted to {c.currency} (secondary)
      </h2>
      <p className="text-sm text-muted-foreground">
        An approximation for orientation: each currency is converted at its latest acceptable exchange rate and rounded to {c.currency}. The figures by currency above are the reference.
      </p>
      {!c.complete && (
        <div role="note" className="flex flex-wrap items-start gap-2 text-sm">
          <StatusBadge status="stale" label="Incomplete" />
          <ul className="space-y-1">
            {c.excluded.map((x) => (
              <li key={x.currency}>
                {x.currency} is not included: {EXCLUDED_REASON[x.reason] ?? x.reason}
                {x.rate ? ' (' + rateText(x.rate) + ')' : ''}.
              </li>
            ))}
          </ul>
        </div>
      )}
      {balancesShown && c.stale_balance_account_count > 0 && (
        <div role="note" className="flex flex-wrap items-center gap-2 text-sm">
          <StatusBadge status="stale" label={c.stale_balance_account_count + ' of ' + c.balance_account_count + (c.balance_account_count === 1 ? ' balance' : ' balances') + ' not fresh'} />
          <span>The bank balances below include data that is not fresh.</span>
        </div>
      )}
      <dl className="grid grid-cols-1 gap-x-4 text-sm sm:max-w-md sm:grid-cols-[minmax(0,1fr)_auto] sm:gap-y-0.5">
        {balancesShown && (
          <>
            <dt>Bank balances</dt>
            <dd className={AMOUNT}>{c.balance ? money(c.balance) : <span className="font-sans text-muted-foreground">None included</span>}</dd>
          </>
        )}
        <dt>Recorded commitments still due</dt>
        <dd className={AMOUNT}>{money(c.still_due)}</dd>
        <dt>of which overdue</dt>
        <dd className={AMOUNT}>{money(c.overdue)}</dd>
        {c.balance_less_still_due && (
          <>
            <dt>Balance minus commitments still due</dt>
            <dd className={AMOUNT}>{money(c.balance_less_still_due)}</dd>
          </>
        )}
      </dl>
      {c.included.some((u) => u.rate) && (
        <ul className="space-y-1 text-xs text-muted-foreground" aria-label="Exchange rates used">
          {c.included
            .filter((u) => u.rate)
            .map((u) => (
              <li key={u.currency}>
                {rateText(u.rate!)}
                {u.refresh_failed ? '; the latest refresh failed, so the cached rate was used' : ''}.
              </li>
            ))}
        </ul>
      )}
    </section>
  )
}

function Accounts({ data }: { data: Outlook }) {
  if (!data.balances.shown) return null
  return (
    <section aria-labelledby="outlook-accounts" className="space-y-2">
      <h2 id="outlook-accounts" className="font-semibold">
        Bank accounts
      </h2>
      {data.accounts.length === 0 ? (
        <p className="text-sm text-muted-foreground">No bank account is connected, so no balance is compared.</p>
      ) : (
        <ul className="space-y-2">
          {data.accounts.map((a) => {
            const f = FRESHNESS[a.freshness.state]
            return (
              <li key={a.id} data-testid={'account-' + a.id} className="space-y-1 rounded-md border border-border bg-surface p-3 text-sm">
                <div className="flex flex-wrap items-center gap-2">
                  <p className="font-medium break-words">{a.name || 'Unnamed account'}</p>
                  <span className="text-muted-foreground">{a.institution}</span>
                  {f.status ? <StatusBadge status={f.status} label={f.label} /> : <span className="text-xs text-muted-foreground">{f.label}</span>}
                </div>
                {a.balances.map((b) => (
                  <p key={b.amount.currency}>
                    <span className="font-mono">{money(b.amount)}</span> {b.type} balance
                    {b.reference_date ? <>, for <DateText value={b.reference_date} /></> : null}, obtained{' '}
                    <time dateTime={b.obtained_at}>{utcStamp(b.obtained_at)}</time>
                  </p>
                ))}
                <p className="text-muted-foreground">
                  {a.freshness.last_success_at ? <>Last successful refresh <time dateTime={a.freshness.last_success_at}>{utcStamp(a.freshness.last_success_at)}</time>. </> : 'Never refreshed successfully. '}
                  {a.not_counted_reason ? NOT_COUNTED[a.not_counted_reason] : null}
                </p>
              </li>
            )
          })}
        </ul>
      )}
    </section>
  )
}
