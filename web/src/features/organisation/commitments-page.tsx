import { usePagedList } from '@/lib/paged'
import { QueryState } from '@/components/query-state'
import { EmptyState } from '@/components/state-panels'
import { Link } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import { Amount, DateText } from '@/components/formatted'
import { holds, useMe } from '@/lib/auth'
import { describeSchedule } from '@/features/commitments/form-model'
import { kindLabel, type Commitment } from '@/features/commitments/types'
import { useLoadAll } from './api'
import { OrganiseDisclosure } from './organise-panel'
import { useOrgFilters } from './org-filters'

type CommitmentRow = Pick<Commitment, 'id' | 'name' | 'kind'> & Partial<Pick<Commitment, 'amount' | 'schedule' | 'start_date' | 'end_date'>>

/**
 * /commitments: every recorded commitment with its amount and schedule, a link to create and edit them (CM-UI), and its Organise
 * control, so one that has no occurrence in the month on screen (ended, not started, yearly) can still be given a category,
 * labels and buckets.
 */
export function CommitmentsPage() {
  const orgFilters = useOrgFilters('commitments')
  const q = usePagedList<CommitmentRow>(['commitments', 'organise'], '/api/v1/commitments', orgFilters.params)
  useLoadAll(q)
  const me = useMe()
  const canWrite = holds(me.data?.grants, 'commitments:write')
  return (
    <section className="space-y-4" aria-labelledby="commitments-title">
      <header className="space-y-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h1 id="commitments-title" className="text-2xl font-semibold">
            Commitments
          </h1>
          {canWrite && (
            <Button asChild>
              <Link to="/commitments/new">New commitment</Link>
            </Button>
          )}
        </div>
        <p className="text-sm text-muted-foreground">
          Every recorded commitment, including those with no occurrence in the month shown on the monthly view. Open one to edit or end it. Open <strong>Organise</strong> to set a category, labels and buckets. Categories, labels and buckets themselves are managed under Categories, labels and buckets.
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
              {orgFilters.active ? 'Choose another category or label, or clear the filter.' : canWrite
                  ? 'Create your first commitment to see its due dates on the monthly view.'
                  : 'Commitments you record appear here.'}
              {!orgFilters.active && canWrite && (
                <p className="mt-2">
                  <Button asChild>
                    <Link to="/commitments/new">New commitment</Link>
                  </Button>
                </p>
              )}
            </EmptyState>
          ) : (
            <>
              <ul className="space-y-3" aria-label="Commitments">
                {items.map((c) => (
                  <li key={c.id} className="space-y-2 rounded-md border border-border bg-surface p-3">
                    <div className="flex flex-wrap items-start justify-between gap-2">
                      <div className="min-w-0 space-y-0.5">
                        <p className="font-medium break-words">{c.name}</p>
                        <p className="flex flex-wrap items-center gap-x-3 text-sm text-muted-foreground">
                          <span>{kindLabel(c.kind)}</span>
                          {c.amount && <Amount amount={c.amount.amount} currency={c.amount.currency} />}
                          {c.schedule && <span>{describeSchedule(c.schedule)}</span>}
                          {c.start_date && (
                            <span>
                              from <DateText value={c.start_date} />
                              {c.end_date && (
                                <>
                                  {' '}
                                  to <DateText value={c.end_date} />
                                </>
                              )}
                            </span>
                          )}
                        </p>
                      </div>
                      <Button asChild variant="outline" size="sm">
                        <Link to="/commitments/$id" params={{ id: c.id }} aria-label={`${canWrite ? 'Edit' : 'View'} ${c.name}`}>
                          {canWrite ? 'Edit' : 'View'}
                        </Link>
                      </Button>
                    </div>
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
