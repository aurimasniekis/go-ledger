import { useId, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Select } from '@/components/ui/input'
import { QueryState } from '@/components/query-state'
import { usePagedList } from '@/lib/paged'
import { matchesKey } from './api'
import { MatchCard } from './match-card'
import type { MatchOrigin, PaymentMatch } from './types'

type Outcome = 'both' | 'rejected' | 'reversed'
const OUTCOMES: { value: Outcome; label: string }[] = [
  { value: 'both', label: 'Rejected and reversed' },
  { value: 'rejected', label: 'Rejected (includes unmatched)' },
  { value: 'reversed', label: 'Reversed (payment bounced)' },
]
const ORIGINS: { value: MatchOrigin | ''; label: string }[] = [
  { value: '', label: 'Any origin' },
  { value: 'auto_rule', label: 'Matching rule' },
  { value: 'model_suggestion', label: 'Model suggestion' },
  { value: 'user', label: 'Created by a person' },
]
const noop = () => {}

/**
 * The Closed list: rejected and reversed matches, read only (ADR-0035: both are final). It reuses the match card, which
 * offers no action in these states, so each entry shows the bank fact, who closed it and how it came (you, the engine,
 * an API token or MCP) and its history. Filters and paging are the server's (state, origin, cursor).
 */
export function ClosedList({ offline }: { offline: boolean }) {
  const uid = useId()
  const [outcome, setOutcome] = useState<Outcome>('both')
  const [origin, setOrigin] = useState<MatchOrigin | ''>('')
  const q = usePagedList<PaymentMatch>(matchesKey, '/api/v1/payment-matches', {
    state: outcome === 'both' ? ['rejected', 'reversed'] : outcome,
    origin: origin || undefined,
  })
  const filtered = outcome !== 'both' || origin !== ''
  return (
    <div className="space-y-3" data-testid="closed-list">
      <p className="text-sm text-muted-foreground">
        Closed matches are final and cannot be changed here. A rejected pair is never suggested again; a reversed match was right but its payment bounced, so its occurrence reopened. The list shows matches in the order the server keeps them, oldest first.
      </p>
      <form role="search" aria-label="Filter closed matches" className="grid gap-3 sm:grid-cols-2" onSubmit={(e) => e.preventDefault()}>
        <label htmlFor={uid + '-outcome'} className="grid gap-1 text-sm">
          Outcome
          <Select id={uid + '-outcome'} value={outcome} onChange={(e) => setOutcome(e.target.value as Outcome)}>
            {OUTCOMES.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
          </Select>
        </label>
        <label htmlFor={uid + '-origin'} className="grid gap-1 text-sm">
          Origin
          <Select id={uid + '-origin'} value={origin} onChange={(e) => setOrigin(e.target.value as MatchOrigin | '')}>
            {ORIGINS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
          </Select>
        </label>
      </form>
      <QueryState query={q} label="closed matches">
        {(items) =>
          items.length === 0 ? (
            <p className="text-sm text-muted-foreground">{filtered ? 'No closed matches fit these filters.' : 'Nothing is closed yet: no suggestion was rejected and no match reversed.'}</p>
          ) : (
            <>
              <ul className="space-y-3" aria-label="Closed matches">
                {items.map((m) => (
                  <li key={m.id}>
                    <MatchCard match={m} offline={offline} onHold={noop} onSaved={noop} />
                  </li>
                ))}
              </ul>
              {q.hasNextPage && (
                <Button variant="outline" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
                  {q.isFetchingNextPage ? 'Loading…' : 'Load more'}
                </Button>
              )}
            </>
          )
        }
      </QueryState>
    </div>
  )
}
