import { useId } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Bell, BellOff, Bot } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/status-badge'
import { ApiError } from '@/lib/api'
import { formatInstantUtc } from '@/lib/format'
import { holds } from '@/lib/auth'
import { ACTION_ERRORS, decisionsKey, dismissAlert } from './api'
import { ObjectRef } from './suggestion-card'
import type { Alert, AlertKind } from './types'

const KIND: Record<AlertKind, string> = {
  consent_expiring: 'Bank consent expiring',
  commitment_amount_changed: 'Commitment amount changed',
  unmatched_overdue: 'Overdue with no matched payment',
}

/** A fact key as words: "days_left" is "days left". */
const factLabel = (k: string) => k.replace(/_/g, ' ')

/**
 * One in-app alert. The message is Ledger's own wording of the facts; wording a model added is kept apart, labelled
 * with its provider and model, and never replaces it. Dismissing is sticky and needs decisions:write.
 */
export function AlertCard({ alert: a, grants, offline, onSaved, onFailed }: { alert: Alert; grants: readonly string[] | undefined; offline: boolean; onSaved: (m: string) => void; onFailed: (m: string) => void }) {
  const qc = useQueryClient()
  const titleId = useId()
  const dismiss = useMutation({
    mutationFn: () => dismissAlert(a.id),
    onSuccess: () => onSaved(`Dismissed: "${KIND[a.kind]}". It stays in the closed list.`),
    // The page says why: a closed alert leaves the open list on the reload below, and this card with it.
    onError: (e) => onFailed(e instanceof ApiError ? (ACTION_ERRORS[e.code] ?? e.message) : 'Could not reach the server. Nothing was saved.'),
    onSettled: () => void qc.invalidateQueries({ queryKey: decisionsKey }),
  })
  const facts = Object.entries(a.facts)
  return (
    <article aria-labelledby={titleId} className="space-y-2 rounded-md border border-border bg-surface p-3 text-sm">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id={titleId} className="font-semibold">
          {KIND[a.kind] ?? a.kind}: <ObjectRef type={a.object_type} id={a.object_id} />
        </h2>
        <span data-state={a.state} className="inline-flex items-center gap-1 rounded-sm border border-border px-1.5 py-0.5 text-xs font-medium">
          {a.state === 'open' ? <Bell aria-hidden className="size-3.5" /> : <BellOff aria-hidden className="size-3.5" />}
          {a.state === 'open' ? 'Open' : a.state === 'dismissed' ? 'Dismissed' : 'Resolved: no longer true'}
        </span>
      </div>
      <p>{a.message}</p>
      {facts.length > 0 && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 text-xs">
          {facts.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-muted-foreground">{factLabel(k)}</dt>
              <dd className="font-mono">{v}</dd>
            </div>
          ))}
        </dl>
      )}
      {a.model_text && (
        <blockquote className="space-y-1 border-l-2 border-border-control pl-3" data-testid="model-text">
          <p>{a.model_text.text}</p>
          <p className="flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
            <Bot aria-hidden className="size-3.5" />
            {a.model_text.provenance}. Extra wording from a model; the message above is Ledger's own.
          </p>
        </blockquote>
      )}
      <p className="text-xs text-muted-foreground">
        Raised <time dateTime={a.created_at}>{formatInstantUtc(a.created_at)}</time>
        {a.closed_at && (
          <>
            {' '}· closed <time dateTime={a.closed_at}>{formatInstantUtc(a.closed_at)}</time>
          </>
        )}
      </p>
      {dismiss.isPending && <StatusBadge status="pending" label="Not saved yet" />}
      {a.state === 'open' && holds(grants, 'decisions:write') && (
        <Button size="sm" variant="outline" className="min-h-9" disabled={offline || dismiss.isPending} onClick={() => dismiss.mutate()}>
          Dismiss
        </Button>
      )}
      {a.state === 'open' && holds(grants, 'decisions:write') && <p className="text-xs text-muted-foreground">Dismissing is final: it cannot be undone from here. The alert stays in Closed alerts.</p>}
      {a.state === 'open' && !holds(grants, 'decisions:write') && <p className="text-xs text-muted-foreground">Dismissing needs the decisions write grant.</p>}
    </article>
  )
}
