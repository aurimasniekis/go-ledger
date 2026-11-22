import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { QueryState } from '@/components/query-state'
import { useOnline } from '@/lib/connection'
import { holds, useMe } from '@/lib/auth'
import { usePagedList } from '@/lib/paged'
import { useAllDefinitions } from '@/features/organisation/api'
import { decisionsKey } from './api'
import { SuggestionCard, type Names } from './suggestion-card'
import { AlertCard } from './alert-card'
import type { Alert, Suggestion } from './types'

type View = 'review' | 'decided' | 'alerts' | 'closed-alerts'
const VIEWS: { view: View; label: string; empty: string }[] = [
  { view: 'review', label: 'Needs review', empty: 'No suggestions are waiting for you. Suggestions appear here when a model proposes a category or labels.' },
  { view: 'decided', label: 'Decided', empty: 'Nothing has been decided yet.' },
  { view: 'alerts', label: 'Open alerts', empty: 'No open alerts.' },
  { view: 'closed-alerts', label: 'Closed alerts', empty: 'No closed alerts.' },
]
const PARAMS: Record<View, { path: string; state: string[] }> = {
  review: { path: '/api/v1/suggestions', state: ['pending'] },
  decided: { path: '/api/v1/suggestions', state: ['accepted', 'rejected', 'superseded', 'reverted'] },
  alerts: { path: '/api/v1/alerts', state: ['open'] },
  'closed-alerts': { path: '/api/v1/alerts', state: ['dismissed', 'resolved'] },
}

/**
 * Model suggestions to review and in-app alerts (ADR-0067). A suggestion is a proposal, never an assignment; the
 * provider and model behind it are shown with it. Reading needs decisions:read.
 */
export function SuggestionsPage() {
  const online = useOnline()
  const me = useMe()
  const grants = me.data?.grants
  const [view, setView] = useState<View>('review')
  // One notice for the whole page: a save, or a refusal. It lives here because the reload after an action can remove the card that raised it.
  const [done, setDoneState] = useState<{ text: string; refused: boolean } | null>(null)
  const setDone = (text: string | null) => setDoneState(text ? { text, refused: false } : null)
  const setFailed = (text: string) => setDoneState({ text, refused: true })
  const doneRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (done) doneRef.current?.focus()
  }, [done])
  const isAlerts = view === 'alerts' || view === 'closed-alerts'
  const cfg = PARAMS[view]
  const q = usePagedList<Suggestion | Alert>([...decisionsKey, view], cfg.path, { state: cfg.state })
  const orgRead = holds(grants, 'organisation:read')
  const cats = useAllDefinitions('category', undefined, orgRead)
  const labs = useAllDefinitions('label', undefined, orgRead)
  const names: Names = {
    categories: cats.items,
    labels: labs.items,
    complete: cats.complete && labs.complete,
    unavailable: !orgRead || cats.query.isError || labs.query.isError,
  }
  const current = VIEWS.find((v) => v.view === view)!
  const offline = !online

  return (
    <section className="space-y-4" aria-labelledby="suggestions-title">
      <h1 id="suggestions-title" className="text-2xl font-semibold">Suggestions and alerts</h1>
      <p className="text-sm text-muted-foreground">
        A suggestion is a model's proposal for a category or labels. It is never an assignment: nothing changes until you accept it or give your own answer,
        and the provider and model behind it stay recorded. Alerts are Ledger's own notices; a model may add wording, which is labelled.
      </p>
      <div role="group" aria-label="Show" className="flex flex-wrap gap-2">
        {VIEWS.map((v) => (
          <Button key={v.view} variant={v.view === view ? 'default' : 'outline'} size="sm" className="h-auto min-h-8 whitespace-normal px-2 py-1" aria-pressed={v.view === view} onClick={() => { setView(v.view); setDone(null) }}>
            {v.label}
          </Button>
        ))}
      </div>
      {offline && <p role="status" className="text-sm text-muted-foreground">You are offline. What is shown is the last data that loaded; every action is off until the connection is back.</p>}
      {names.unavailable && !isAlerts && (
        <p role="note" className="text-sm text-muted-foreground">
          Category and label names could not be read{orgRead ? '' : ' (needs the organisation read grant)'}, so choices show as identifiers and correcting is off.
        </p>
      )}
      {done && (
        <div ref={doneRef} tabIndex={-1} role={done.refused ? 'alert' : 'status'} className="flex flex-wrap items-center gap-2 rounded-md border border-border-control bg-surface p-3 text-sm">
          <span>{done.text}</span>
          <Button variant="ghost" size="sm" onClick={() => setDone(null)}>Dismiss</Button>
        </div>
      )}
      <QueryState query={q} label={isAlerts ? 'alerts' : 'suggestions'}>
        {(items) =>
          items.length === 0 ? (
            <p className="text-sm text-muted-foreground">{current.empty}</p>
          ) : (
            <>
              <ul className="space-y-3" aria-label={current.label}>
                {items.map((it) => (
                  <li key={it.id}>
                    {isAlerts ? (
                      <AlertCard alert={it as Alert} grants={grants} offline={offline} onSaved={setDone} onFailed={setFailed} />
                    ) : (
                      <SuggestionCard suggestion={it as Suggestion} names={names} grants={grants} offline={offline} onSaved={setDone} onFailed={setFailed} />
                    )}
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
    </section>
  )
}
