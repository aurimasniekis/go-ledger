import { usePagedList } from '@/lib/paged'
import { QueryState } from '@/components/query-state'
import { EmptyState } from '@/components/state-panels'
import { Amount, DateText } from '@/components/formatted'
import { Button } from '@/components/ui/button'
import { useLoadAll } from './api'
import { OrganiseDisclosure } from './organise-panel'
import { useOrgFilters } from './org-filters'

interface CommitmentRow {
  id: string
  name: string
  kind: string
  amount?: { amount: string; currency: string }
  schedule?: { frequency: string; interval?: number; day: number; month?: number }
  start_date?: string
  end_date?: string | null
}

const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December']

function scheduleText(s: NonNullable<CommitmentRow['schedule']>): string {
  if (s.frequency === 'annual') return `Yearly on ${s.day} ${MONTHS[(s.month ?? 0) - 1] ?? ''}`.trim()
  return (s.interval ?? 1) > 1 ? `Every ${s.interval} months on day ${s.day}` : `Monthly on day ${s.day}`
}

/**
 * /commitments: every recorded commitment with its Organise control, so one that has no occurrence in the month on screen
 * (ended, not started, yearly) can still be given a category, labels and buckets. It changes nothing else about a commitment.
 */
export function CommitmentsPage() {
  const orgFilters = useOrgFilters('commitments')
  const q = usePagedList<CommitmentRow>(['commitments', 'organise'], '/api/v1/commitments', orgFilters.params)
  useLoadAll(q)
  return (
    <section className="space-y-4" aria-labelledby="commitments-title">
      <header className="space-y-2">
        <h1 id="commitments-title" className="text-2xl font-semibold">
          Organise commitments
        </h1>
        <p className="text-sm text-muted-foreground">
          Every recorded commitment, including those with no occurrence in the month shown on the monthly view. Open <strong>Organise</strong> to set a category, labels and buckets. Categories, labels and buckets themselves are managed under Categories, labels and buckets.
        </p>
      </header>
      {orgFilters.control}
      {orgFilters.active && (
        <p role="status" data-testid="commitments-filter-note" className="text-sm text-muted-foreground">
          Showing only commitments with {orgFilters.describe}.
        </p>
      )}
      <QueryState query={q} label="commitments">
        {(items) =>
          items.length === 0 ? (
            <EmptyState id="commitments-empty" level={2} title={orgFilters.active ? 'No commitment matches the filter' : 'No commitments yet'}>
              {orgFilters.active ? 'Choose another category or label, or clear the filter.' : 'Commitments you record appear here.'}
            </EmptyState>
          ) : (
            <>
              <ul className="space-y-3" aria-label="Commitments">
                {items.map((c) => (
                  <li key={c.id} className="space-y-2 rounded-md border border-border bg-surface p-3">
                    <p className="font-medium break-words">{c.name}</p>
                    {(c.amount || c.schedule || c.start_date) && (
                      <p className="flex flex-wrap gap-x-3 text-sm text-muted-foreground">
                        {c.amount && <Amount amount={c.amount.amount} currency={c.amount.currency} />}
                        {c.schedule && <span>{scheduleText(c.schedule)}</span>}
                        {c.start_date && (
                          <span>
                            From <DateText value={c.start_date} />
                            {c.end_date ? (
                              <>
                                {' '}to <DateText value={c.end_date} />
                              </>
                            ) : null}
                          </span>
                        )}
                      </p>
                    )}
                    <OrganiseDisclosure objectType="commitment" objectId={c.id} subject={c.name} />
                  </li>
                ))}
              </ul>
              {q.hasNextPage && (
                <Button variant="outline" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
                  {q.isFetchingNextPage ? 'Loading…' : 'Load more commitments'}
                </Button>
              )}
            </>
          )
        }
      </QueryState>
    </section>
  )
}
