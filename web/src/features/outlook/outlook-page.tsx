import { useEffect, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
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
import type { ExchangeRate, FreshnessState, Outlook, OutlookAccount, OutlookConverted, OutlookCurrency } from './types'

const money = (m: Money) => <Amount amount={m.amount} currency={m.currency} />
/** The staleness threshold as people say it: "24 h", "90 min", "3 days". */
function durationLabel(seconds: number): string {
  if (seconds >= 2 * 86400 && seconds % 86400 === 0) return seconds / 86400 + ' days'
  if (seconds % 3600 === 0) return seconds / 3600 + ' h'
  if (seconds % 60 === 0) return seconds / 60 + ' min'
  return seconds + ' s'
}

type NotFresh = Exclude<FreshnessState, 'fresh'>

/** How each state that is not fresh reads in a count: "1 needs reconnect". */
const NOT_FRESH_WORD: Record<NotFresh, string> = {
  stale: 'stale',
  never_refreshed: 'never refreshed',
  failed: 'last refresh failed',
  reconnect_required: 'needs reconnect',
  disconnected: 'disconnected',
}

/**
 * The states of the counted accounts that hold a balance in one of the currencies and whose data is not fresh, one
 * entry per balance. It mirrors the server's count (stale_account_count, stale_balance_account_count).
 */
function notFreshStates(accounts: OutlookAccount[], currencies: string[]): NotFresh[] {
  const out: NotFresh[] = []
  for (const a of accounts) {
    if (!a.counted || !a.freshness.stale || a.freshness.state === 'fresh') continue
    for (const b of a.balances) if (currencies.includes(b.amount.currency)) out.push(a.freshness.state)
  }
  return out
}

/**
 * Says what is not fresh, by state, so that an account that was refreshed recently but is now disconnected or needs
 * reconnecting is never described as "not refreshed in 24 h". "Not refreshed in <threshold>" is used only when every
 * one is stale; when the states cannot be matched to the server's count, the count is said without a reason.
 */
function notFreshLabel(stale: number, total: number, staleAfter: number | null, states: NotFresh[]): string {
  const head = stale + ' of ' + total + (total === 1 ? ' balance' : ' balances')
  if (states.length !== stale) return head + ' not fresh'
  if (staleAfter && states.every((s) => s === 'stale')) return head + ' not refreshed in ' + durationLabel(staleAfter)
  const counts = new Map<NotFresh, number>()
  for (const s of states) counts.set(s, (counts.get(s) ?? 0) + 1)
  const parts = [...counts].map(([s, n]) => n + ' ' + NOT_FRESH_WORD[s])
  return head + ' not fresh: ' + parts.join(', ')
}

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
  conversion_timed_out: 'the rate was not obtained in time',
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
            <div role="group" aria-label="Commitments counted through" className="flex items-center gap-2">
              <Button variant="outline" size="sm" disabled={shownThrough <= q.data.as_of.slice(0, 7)} onClick={() => go(shiftMonth(shownThrough, -1))}>
                <ChevronLeft aria-hidden className="size-4" />
                <span className="sr-only">Count commitments through {monthLabel(shiftMonth(shownThrough, -1))}</span>
                <span aria-hidden className="hidden sm:inline">Earlier</span>
              </Button>
              <p className="min-w-0 flex-1 text-center text-sm sm:flex-none sm:text-left">
                {q.data.window.from ? <>Everything still owed since your first commitment started ({monthLabel(q.data.window.from)}), through </> : <>Commitments counted through </>}
                <strong>{monthLabel(shownThrough)}</strong>
              </p>
              <Button variant="outline" size="sm" onClick={() => go(shiftMonth(shownThrough, 1))}>
                <span aria-hidden className="hidden sm:inline">Later</span>
                <span className="sr-only">Count commitments through {monthLabel(shiftMonth(shownThrough, 1))}</span>
                <ChevronRight aria-hidden className="size-4" />
              </Button>
            </div>
          )}
          <Freshness data={q.data} status={q.status} error={q.error} isFetching={q.isFetching} dataUpdatedAt={q.dataUpdatedAt} now={now} offline={!online} onRetry={() => void q.refetch()} />
          {q.data.window.not_recorded_count > 0 && (
            <p data-testid="outlook-not-recorded" className="text-sm text-muted-foreground">
              {q.data.window.not_recorded_count} {q.data.window.not_recorded_count === 1 ? 'occurrence' : 'occurrences'} due before tracking started {q.data.window.not_recorded_count === 1 ? 'is' : 'are'} not counted (see the{' '}
              <Link to="/month" className="underline">
                monthly view
              </Link>
              ).
            </p>
          )}
          {q.data.currencies.length === 0 ? (
            <EmptyState id="outlook-empty" level={2} title="Nothing to compare yet">
              No commitment is still due in this window{q.data.balances.shown ? ' and no bank balance is counted' : ''}. Add commitments, or <Link to="/connections" className="underline">connect a bank on the Connections page</Link>, to see the outlook.
            </EmptyState>
          ) : (
            <Currencies lines={q.data.currencies} accounts={q.data.accounts} balancesShown={q.data.balances.shown} staleAfter={q.data.balances.stale_after_seconds} />
          )}
          {q.data.converted && (
            <Converted
              c={q.data.converted}
              accounts={q.data.accounts}
              hasLine={q.data.currencies.some((l) => l.currency === q.data.converted?.currency)}
              balancesShown={q.data.balances.shown}
              staleAfter={q.data.balances.stale_after_seconds}
            />
          )}
          {q.data.converted && (
            <p className="text-sm text-muted-foreground" data-testid="outlook-rates-link">
              Every rate used above shows its date and age. See all{' '}
              <Link to="/rates" className="underline">
                cached exchange rates
              </Link>{' '}
              and when they were retrieved.
            </p>
          )}
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

function Currencies({ lines, accounts, balancesShown, staleAfter }: { lines: OutlookCurrency[]; accounts: OutlookAccount[]; balancesShown: boolean; staleAfter: number | null }) {
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
                <StatusBadge status="stale" label={notFreshLabel(l.stale_account_count, l.account_count, staleAfter, notFreshStates(accounts, [l.currency]))} />
              )}
            </div>
            <dl className="mt-1 grid grid-cols-1 gap-x-4 text-sm sm:grid-cols-[minmax(0,1fr)_auto] sm:gap-y-0.5">
              <dt>
                Bank balance
                {l.balance && l.balance_as_of ? (
                  <>
                    , as of <DateText value={l.balance_as_of} />
                    {l.account_count > 1 ? ' (oldest of ' + l.account_count + ' balances)' : ''}
                  </>
                ) : null}
              </dt>
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
                  <dt>
                    Balance minus recorded commitments still due
                    {l.payments_after_balance && l.payments_after_balance.count > 0 && (
                      // The caveat sits inside the label of the row it qualifies, under the label, beside the amount.
                      <span role="note" data-testid={'outlook-after-' + l.currency} className="block text-xs font-normal">
                        {l.payments_after_balance.count} {l.payments_after_balance.count === 1 ? 'payment' : 'payments'} ({money(l.payments_after_balance.amount)}) recorded as paid after this balance's date{' '}
                        {l.payments_after_balance.count === 1 ? 'is' : 'are'} already off "still due" but may not be in the bank balance yet. The balance is shown as the bank reported it.
                      </span>
                    )}
                  </dt>
                  <dd className={AMOUNT}>{money(l.balance_less_still_due)}</dd>
                </>
              )}
            </dl>
            {l.overdue_months.length > 0 && (
              <div className="mt-1 text-xs">
                <p className="text-muted-foreground">Overdue comes from (open the month to see the items):</p>
                <ul aria-label={'Overdue ' + l.currency + ' by month'} className="flex flex-wrap gap-x-3 gap-y-1">
                  {l.overdue_months.map((m) => (
                    <li key={m.month}>
                      <Link to="/month" search={{ month: m.month }} className="underline">
                        {monthLabel(m.month)}
                      </Link>{' '}
                      ({m.count} {m.count === 1 ? 'item' : 'items'}, {money(m.amount)})
                    </li>
                  ))}
                </ul>
              </div>
            )}
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

function Converted({ c, accounts, hasLine, balancesShown, staleAfter }: { c: OutlookConverted; accounts: OutlookAccount[]; hasLine: boolean; balancesShown: boolean; staleAfter: number | null }) {
  // Nothing besides the reporting currency converts: the figures would only repeat that currency's card, and beside
  // an "incomplete" note they could be read as a total across currencies. Show the explanation alone.
  if (c.included.every((u) => !u.rate)) {
    return (
      <section aria-labelledby="outlook-converted" className="space-y-2 rounded-md border border-border p-3">
        <h2 id="outlook-converted" className="font-semibold">
          Converted to {c.currency} (secondary)
        </h2>
        {c.excluded.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="outlook-converted-none">
            {hasLine ? (
              <>Every amount is already in {c.currency}, so there is nothing to convert: the {c.currency} figures above are the whole picture.</>
            ) : (
              <>There are no amounts yet, so there is nothing to convert to {c.currency}.</>
            )}
          </p>
        ) : (
          <div role="note" className="space-y-2 text-sm" data-testid="outlook-converted-none">
            <p>
              <StatusBadge status="stale" label="No converted total" />{' '}
              {hasLine ? (
                <>No total in {c.currency} is shown, because no other currency could be converted. The {c.currency} figures above are the only {c.currency} amounts; the others are not included:</>
              ) : (
                <>No total in {c.currency} is shown, because no currency could be converted to {c.currency}. There are no {c.currency} amounts on this page; the others are not included:</>
              )}
            </p>
            <ul className="space-y-1">
              {c.excluded.map((x) => (
                <li key={x.currency}>
                  {x.currency}: {EXCLUDED_REASON[x.reason] ?? x.reason}
                  {x.rate ? ' (' + rateText(x.rate) + ')' : ''}.
                </li>
              ))}
            </ul>
          </div>
        )}
      </section>
    )
  }
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
          <StatusBadge status="stale" label={notFreshLabel(c.stale_balance_account_count, c.balance_account_count, staleAfter, notFreshStates(accounts, c.included.map((u) => u.currency)))} />
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
            <dt>Balance minus recorded commitments still due</dt>
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
