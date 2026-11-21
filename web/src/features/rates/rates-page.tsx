import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/status-badge'
import { EmptyState } from '@/components/state-panels'
import { QueryState } from '@/components/query-state'
import { DateText } from '@/components/formatted'
import { utcStamp } from '@/features/month/month'
import { useRates, useRatesStatus } from './api'
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

/** The read-only /rates screen: the cached exchange rates with provenance, rate date, retrieval time and freshness (ADR-0048). */
export function RatesPage() {
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
      {status.data && <StatusPanel s={status.data} />}
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
