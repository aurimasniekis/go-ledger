import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/status-badge'
import { EmptyState } from '@/components/state-panels'
import { QueryState } from '@/components/query-state'
import { DateText } from '@/components/formatted'
import { utcStamp } from '@/features/month/month'
import { useEffect, useRef, useState } from 'react'
import { ApiError } from '@/lib/api'
import { holds, useMe } from '@/lib/auth'
import { LoadingState } from '@/components/state-panels'
import { Notice } from '@/components/notice'
import { useRates, useRatesStatus, useRefreshRates } from './api'
import type { CachedRate, RatesRun, RatesStatus } from './types'

const STALE_REASON: Record<NonNullable<CachedRate['stale_reason']>, string> = {
  not_retrieved_recently: 'not retrieved recently',
  rate_date_too_old: 'rate date too old',
}

const RUN_OUTCOME: Record<RatesRun['outcome'], string> = {
  succeeded: 'succeeded',
  partial: 'partly succeeded',
  failed: 'failed',
}

const ERROR_KIND: Record<string, string> = {
  unavailable: 'the rate provider did not answer',
  rate_limited: 'the rate provider asked for a pause',
  permanent: 'the rate provider refused or sent an unusable answer',
  internal: 'Ledger could not run the refresh',
}

function lastRunText(run: RatesRun): string {
  const what = RUN_OUTCOME[run.outcome]
  const why = run.error ? ': ' + (ERROR_KIND[run.error.kind] ?? run.error.kind) + ' (' + run.error.code + ')' : ''
  return run.trigger + ' refresh ' + what + why + ', ' + run.currencies_refreshed + ' ' + (run.currencies_refreshed === 1 ? 'currency' : 'currencies') + ' refreshed'
}

function StatusPanel({ s }: { s: RatesStatus }) {
  return (
    <section aria-label="Rate provider" className="space-y-1 rounded-md border border-border bg-surface p-3 text-sm">
      <p className="flex flex-wrap items-center gap-2">
        {s.enabled ? (
          <span className="font-medium">
            Rates come from {s.provider} ({s.source})
            {s.reporting_currency ? ', fetched to ' + s.reporting_currency : ''}.
          </span>
        ) : (
          <>
            <StatusBadge status="disconnected" label="Provider off" />
            <span>
              No exchange-rate provider is configured (rates.enabled). The rates below were cached while it was on; nothing refreshes them.
            </span>
          </>
        )}
      </p>
      <p>
        A rate is stale when it was not retrieved for {s.stale_after_days} days, or is dated more than {s.stale_after_days} days ago. A stale rate is never used in a converted total.
      </p>
      {s.enabled &&
        (s.last_run ? (
          <p data-testid="rates-last-run">
            Last refresh since Ledger started: {lastRunText(s.last_run)} (<time dateTime={s.last_run.finished_at}>{utcStamp(s.last_run.finished_at)}</time>).
            {s.last_success_at && s.last_run.outcome !== 'succeeded' && (
              <>
                {' '}
                Last fully successful refresh: <time dateTime={s.last_success_at}>{utcStamp(s.last_success_at)}</time>.
              </>
            )}
          </p>
        ) : (
          <p data-testid="rates-last-run">No refresh has finished since Ledger started.</p>
        ))}
    </section>
  )
}

/** The Refresh now control (administrators): one request at a time, at most one a minute, and every outcome is said in words. */
function RefreshControl({ status }: { status?: RatesStatus }) {
  const refresh = useRefreshRates()
  // The wall-clock time a refused refresh may be retried: kept so the button stays off until then.
  const [retryAt, setRetryAt] = useState<number | null>(null)
  const [waiting, setWaiting] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  // Do not set state on an unmounted page when the wait ends.
  useEffect(() => () => clearTimeout(timer.current), [])
  const err = refresh.error instanceof ApiError ? refresh.error : undefined
  const notRunnable = status !== undefined && !status.refresh_available
  // The button stays focusable while it cannot act (aria-disabled, as the matches UI does), so keyboard focus is not lost.
  const blocked = refresh.isPending || waiting || notRunnable
  const run = () => {
    if (blocked) return
    refresh.mutate(undefined, {
      onSuccess: () => {
        setRetryAt(null)
        setWaiting(false)
      },
      onError: (e) => {
        if (e instanceof ApiError && e.status === 429) {
          const at = Date.now() + (e.retryAfter ?? 60) * 1000
          setRetryAt(at)
          setWaiting(true)
          clearTimeout(timer.current)
          timer.current = setTimeout(() => setWaiting(false), at - Date.now() + 50)
        }
      },
    })
  }
  return (
    <section aria-label="Refresh exchange rates" className="space-y-2 rounded-md border border-border bg-surface p-3 text-sm">
      <div className="flex flex-wrap items-center gap-3">
        <Button
          variant="outline"
          size="sm"
          className="aria-disabled:cursor-not-allowed aria-disabled:opacity-50"
          aria-disabled={blocked || undefined}
          aria-describedby="rates-refresh-help"
          onClick={run}
        >
          {refresh.isPending ? 'Refreshing…' : 'Refresh now'}
        </Button>
        <span id="rates-refresh-help" className="text-muted-foreground">
          {notRunnable
            ? 'No refresh can run: the rate provider is off or no reporting currency is set.'
            : 'Asks the provider for fresh rates. Allowed once a minute. It only adds or revises cached rates; nothing is deleted, and a rate it revises keeps the value it replaced.'}
        </span>
      </div>
      <div role="status" aria-live="polite" data-testid="rates-refresh-result" className="empty:hidden">
        {refresh.isPending && <p>Asking the rate provider for fresh rates…</p>}
        {refresh.isSuccess && refresh.data.outcome === 'succeeded' && (
          <p>
            Refreshed {refresh.data.currencies_refreshed} {refresh.data.currencies_refreshed === 1 ? 'currency' : 'currencies'} at{' '}
            <time dateTime={refresh.data.finished_at}>{utcStamp(refresh.data.finished_at)}</time>. The list below is up to date; earlier values of revised rates are kept.
          </p>
        )}
        {refresh.isSuccess && refresh.data.outcome !== 'succeeded' && (
          <p>
            The refresh {RUN_OUTCOME[refresh.data.outcome]}
            {refresh.data.error ? ': ' + (ERROR_KIND[refresh.data.error.kind] ?? refresh.data.error.kind) : ''}; {refresh.data.currencies_refreshed}{' '}
            {refresh.data.currencies_refreshed === 1 ? 'currency was' : 'currencies were'} refreshed. Rates it could not fetch keep their cached values and age, and may be stale. You can try again in a minute.
          </p>
        )}
      </div>
      {err && !refresh.isPending && (
        <Notice role="alert">
          {err.status === 429 ? (
            <>
              Refreshed a moment ago, so this one was not run. Nothing changed.
              {retryAt !== null && (
                <>
                  {' '}
                  You can try again at <time dateTime={new Date(retryAt).toISOString()}>{utcStamp(new Date(retryAt).toISOString())}</time>
                  .
                </>
              )}
            </>
          ) : err.code === 'refresh_in_progress' ? (
            'A refresh is already running. Nothing changed; wait a moment and check the list.'
          ) : err.code === 'rates_refresh_unavailable' || err.status === 503 ? (
            'Exchange rates cannot be refreshed right now: the provider is off, not set up or unavailable. Nothing changed and the cached rates are still shown. You can try again later.'
          ) : (
            'The refresh could not be started: ' + err.message + '. Nothing changed.'
          )}
        </Notice>
      )}
      {refresh.isError && !err && (
        <Notice role="alert">The refresh could not reach Ledger: {refresh.error.message}. Nothing changed; try again.</Notice>
      )}
    </section>
  )
}

/** The /rates screen, for administrators: the cached exchange rates with provenance, rate date, retrieval time and freshness (ADR-0048, D-FX-R). */
export function RatesPage() {
  const me = useMe()
  if (me.isPending) return <LoadingState label="your permissions" />
  if (!holds(me.data?.grants, 'rates:read')) {
    return (
      <section className="space-y-3" aria-labelledby="rates-title">
        <h1 id="rates-title" className="text-xl font-semibold">
          Exchange rates
        </h1>
        <Notice role="alert">
          You are not permitted to see this page: the cached exchange rates are for administrators. Converted totals in the outlook and month views are not affected and still show the rate each one used.
        </Notice>
      </section>
    )
  }
  return <RatesContent canRefresh={holds(me.data?.grants, 'rates:write')} />
}

function RatesContent({ canRefresh }: { canRefresh: boolean }) {
  const status = useRatesStatus()
  const list = useRates()
  return (
    <section className="space-y-4" aria-labelledby="rates-title">
      <h1 id="rates-title" className="text-xl font-semibold">
        Exchange rates
      </h1>
      <p className="rounded-md border border-border bg-surface p-3 text-sm">
        These are the published reference rates Ledger converts with in the outlook's secondary totals. They are not what a bank or card would charge, and each shows the day it is for and when Ledger last retrieved it.
      </p>
      {canRefresh && <RefreshControl status={status.data} />}
      <QueryState query={status} label="the rate provider status">
        {(s) => <StatusPanel s={s} />}
      </QueryState>
      <QueryState query={list} label="exchange rates">
        {(rates) =>
          rates.length === 0 ? (
            <EmptyState id="rates-empty" level={2} title="No exchange rates cached yet">
              {status.data && !status.data.enabled
                ? 'The rate provider is off, so nothing has been fetched. Amounts in other currencies are left out of converted totals and say why.'
                : 'Rates are fetched when a converted total needs one and by the daily refresh. None has been retrieved yet.'}
            </EmptyState>
          ) : (
            <>
              <div className="overflow-x-auto">
                <table className="w-full text-left text-sm">
                  <caption className="sr-only">Cached exchange rates, newest rate date first within each pair</caption>
                  <thead>
                    <tr className="border-b border-border">
                      <th scope="col" className="py-2 pr-3">Pair</th>
                      <th scope="col" className="py-2 pr-3 sm:text-right">Rate</th>
                      <th scope="col" className="py-2 pr-3">Rate date</th>
                      <th scope="col" className="py-2 pr-3">Retrieved</th>
                      <th scope="col" className="py-2 pr-3">Source</th>
                      <th scope="col" className="py-2">Freshness</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rates.map((r) => (
                      <tr key={[r.provider, r.source, r.base, r.quote, r.date].join('|')} className="border-b border-border align-top">
                        <th scope="row" className="py-2 pr-3 font-medium">
                          {r.base}/{r.quote}
                        </th>
                        <td className="py-2 pr-3 font-mono sm:text-right">
                          {r.value}
                          {r.previous && <span className="block text-xs text-muted-foreground">revised from {r.previous.value}</span>}
                        </td>
                        <td className="py-2 pr-3">
                          <DateText value={r.date} />
                        </td>
                        <td className="py-2 pr-3">
                          <time dateTime={r.retrieved_at}>{utcStamp(r.retrieved_at)}</time>
                        </td>
                        <td className="py-2 pr-3">
                          {r.provider} / {r.source}
                        </td>
                        <td className="py-2">
                          {r.stale ? <StatusBadge status="stale" label={'Stale: ' + (r.stale_reason ? STALE_REASON[r.stale_reason] : 'old')} /> : <span>Current</span>}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {list.hasNextPage && (
                <Button variant="outline" disabled={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                  {list.isFetchingNextPage ? 'Loading…' : 'Load more rates'}
                </Button>
              )}
            </>
          )
        }
      </QueryState>
    </section>
  )
}
