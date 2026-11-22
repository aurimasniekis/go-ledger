import { useCallback, useEffect, useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Undo2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/status-badge'
import { QueryState } from '@/components/query-state'
import { Amount } from '@/components/formatted'
import { ApiError } from '@/lib/api'
import { useOnline } from '@/lib/connection'
import { isVersionConflict, useOptimisticSave } from '@/lib/optimistic-save'
import { usePagedList } from '@/lib/paged'
import { matchAction, matchesKey, runMatching } from './api'
import { ReplacedNotice } from './replace-manual'
import { describeAction, MatchCard, OFFLINE_NOTE_ID, subjectText, type Subject } from './match-card'
import { ClosedList } from './closed-list'
import type { MatchAction, MatchingRunResult, PaymentMatch } from './types'

type View = 'suggested' | 'confirmed' | 'closed'
const VIEWS: { state: View; label: string; empty: string }[] = [
  { state: 'suggested', label: 'Needs review', empty: 'No suggestions are waiting. Run matching to look again, or record a payment from the monthly view.' },
  { state: 'confirmed', label: 'Confirmed', empty: 'No confirmed matches yet.' },
  { state: 'closed', label: 'Closed', empty: '' },
]

interface Done {
  saved: PaymentMatch
  action: MatchAction
  before: PaymentMatch
  subject: Subject
}

/**
 * What was just saved. The server has answered by the time it shows. The notice names the commitment and the month
 * (D36) and stays until dismissed or replaced (D37). A correction is undone by correcting back. A confirmation is undone
 * by the server's unconfirm (ADR-0045): the match is a suggestion again. The server decides whether the undo window is
 * still open and says so if not; the Confirmed list keeps the distinct, final Unmatch. Rejected, unmatched and
 * reversed matches are final, so they offer nothing.
 */
function DoneNotice({ done, onDone }: { done: Done; onDone: () => void }) {
  const qc = useQueryClient()
  const box = useRef<HTMLDivElement>(null)
  const { saved, action, before, subject } = done
  const where = subjectText(subject)
  const undoable = action === 'confirm' || action === 'correct'
  const undo = useMutation({
    mutationFn: () =>
      action === 'confirm'
        ? matchAction(saved.id, { action: 'unconfirm', version: saved.version })
        : matchAction(saved.id, { action: 'correct', version: saved.version, amount: before.amount.amount, currency: before.amount.currency }),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: matchesKey })
      void qc.invalidateQueries({ queryKey: ['occurrences'] })
    },
  })
  useEffect(() => box.current?.focus(), [undo.isSuccess])
  // After these refusals the undo can never work on this match: the button goes, so it is not offered again.
  // A version conflict is one of them: the notice holds the version it saved, which is stale now, and resending it cannot work.
  const undoGone = undo.isError && (isVersionConflict(undo.error) || UNDO_FINAL.includes(undo.error instanceof ApiError ? undo.error.code : ''))
  return (
    <div ref={box} tabIndex={-1} role="status" className="space-y-1 rounded-md border border-border-control bg-surface p-3 text-sm">
      {undo.isSuccess ? (
        <p>
          <span className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-0.5 text-sm font-medium text-muted-foreground">
            <Undo2 aria-hidden className="size-4" />
            Undone
          </span>{' '}
          {action === 'confirm'
            ? `The confirmation for ${where} was taken back. The match is a suggestion again under Needs review, and the occurrence is no longer paid by this transaction.`
            : `The amount for ${where} is back to what it was.`}{' '}
          <Button variant="ghost" size="sm" onClick={onDone}>Dismiss</Button>
        </p>
      ) : (
        <>
          <p>
            <StatusBadge status="paid" label="Saved" /> {describeAction(action)} for {where}: <Amount amount={saved.amount.amount} currency={saved.amount.currency} /> of the bank transaction of{' '}
            <Amount amount={saved.transaction.amount.amount} currency={saved.transaction.amount.currency} />.
            {action === 'confirm' && saved.counts_as_evidence && ' It now counts as payment for the occurrence.'}
            {(action === 'reject' || action === 'unmatch' || action === 'reverse') && ' This decision is final; the pair will not be suggested again.'}
          </p>
          {action === 'confirm' && <p className="text-xs">Undo puts it back as a suggestion. It works only while nothing else has changed this match or occurrence; Unmatch in the Confirmed list is final.</p>}
          <div className="flex flex-wrap items-center gap-2">
            {undoable && !undoGone && (
              <Button variant="outline" size="sm" className="h-auto min-h-8 whitespace-normal py-1 text-left aria-disabled:opacity-50" aria-disabled={undo.isPending || undefined} onClick={() => { if (!undo.isPending) undo.mutate() }}>
                {undo.isPending ? 'Undoing…' : action === 'confirm' ? 'Undo confirmation' : 'Undo this correction'}
              </Button>
            )}
            <Button variant="ghost" size="sm" aria-disabled={undo.isPending || undefined} onClick={() => { if (!undo.isPending) onDone() }}>Dismiss</Button>
            {undo.isPending && <StatusBadge status="pending" label="Not confirmed yet" />}
          </div>
        </>
      )}
      {undo.isError && (
        <p role="alert">
          <StatusBadge status="save_failed" label="Undo not saved" /> The change stays as saved. {undoErrorText(undo.error, action)}
        </p>
      )}
    </div>
  )
}

/** Refusals after which Undo cannot work any more (the change stays as saved). */
const UNDO_FINAL = ['match_undo_window_closed', 'match_undo_not_decider', 'invalid_match_state']

/** Why an undo failed, in words; the server's own codes say whether the undo window closed or someone else decided. */
function undoErrorText(e: Error, action: MatchAction): string {
  const code = e instanceof ApiError ? e.code : ''
  if (action === 'confirm' && code === 'match_undo_window_closed') return 'Undo is no longer possible: something else changed this match or occurrence since. The match stays confirmed. If it is wrong, use Unmatch in the Confirmed list (final).'
  if (action === 'confirm' && code === 'match_undo_not_decider') return 'Only the person who confirmed this match can undo it. The match stays confirmed.'
  if (action === 'confirm' && code === 'invalid_match_state') return 'This match was not a suggestion before, so it cannot go back to one. Use Unmatch in the Confirmed list (final).'
  if (action === 'correct' && code === 'invalid_match_state') return 'This match is no longer confirmed, so its amount cannot be put back. The match stays as it is now; reload the list to see its current state.'
  if (isVersionConflict(e)) return 'Something else changed this match since you saved it, so the undo was not applied and is no longer offered here. The match stays as it is now; reload the list to see its current state and change it from there.'
  return e.message
}

/** Asks the engine to run now. Its automatic confirmations follow the safeguards of ADR-0039; each can be corrected or unmatched from the Confirmed list. */
function RunMatching({ offline }: { offline?: boolean }) {
  const [result, setResult] = useState<MatchingRunResult | null>(null)
  const save = useOptimisticSave<void, MatchingRunResult>({
    queryKey: matchesKey,
    mutationFn: () => runMatching(),
    onSaved: (r) => setResult(r),
    messages: { saved: (r) => `Matching finished: ${r.confirmed} confirmed, ${r.suggested} suggested.`, failed: () => '' },
  })
  const pending = save.phase === 'pending'
  return (
    <div className="space-y-1">
      <Button variant="outline" className="h-auto min-h-9 max-w-full whitespace-normal py-1 text-left aria-disabled:cursor-not-allowed aria-disabled:opacity-50" aria-disabled={pending || undefined} disabled={!!offline} aria-describedby={offline ? OFFLINE_NOTE_ID : undefined} onClick={() => { if (pending) return; setResult(null); save.save() }}>
        {pending ? 'Running…' : 'Run matching now'}
      </Button>
      {pending && <span role="status" className="ml-2 text-sm">Running matching. Nothing has changed yet.</span>}
      {result && (
        <p role="status" className="text-sm" data-testid="run-result">
          Looked at {result.candidates} candidate pairs: {result.confirmed} confirmed automatically, {result.suggested} new suggestions, {result.dropped} stale suggestions dropped, {result.skipped} skipped.
        </p>
      )}
      {save.phase === 'failed' && (
        <p role="alert" className="text-sm">
          <StatusBadge status="save_failed" label="Matching did not run" /> {save.error?.message} Nothing was changed.{' '}
          <Button size="sm" variant="ghost" onClick={() => save.save()}>Try again</Button>
        </p>
      )}
    </div>
  )
}

/** The /matches screen: review suggestions, confirm or reject them, and correct what is confirmed. */
export function MatchesPage() {
  const online = useOnline()
  const [state, setState] = useState<View>('suggested')
  const view = VIEWS.find((v) => v.state === state)!
  const q = usePagedList<PaymentMatch>(matchesKey, '/api/v1/payment-matches', { state: state === 'closed' ? undefined : state }, { enabled: state !== 'closed' })
  const [done, setDone] = useState<Done | null>(null)
  // Cards whose last save failed stay listed even when a refetch no longer has them (changed elsewhere), in the view they failed in.
  const [held, setHeld] = useState<Record<string, { match: PaymentMatch; view: View }>>({})
  const viewRef = useRef<View>(state)
  useEffect(() => {
    viewRef.current = state
  }, [state])
  const onHold = useCallback((id: string, m: PaymentMatch | null) => {
    setHeld((h) => {
      if (m === null) {
        if (!(id in h)) return h
        const rest = { ...h }
        delete rest[id]
        return rest
      }
      return h[id]?.match === m ? h : { ...h, [id]: { match: m, view: h[id]?.view ?? viewRef.current } }
    })
  }, [])
  const offline = !online

  return (
    <section className="space-y-4" aria-labelledby="matches-title">
      <h1 id="matches-title" className="text-2xl font-semibold">Payment matches</h1>
      <p className="text-sm text-muted-foreground">
        A match says that a bank transaction paid a commitment occurrence. Only confirmed matches count as payment. A suggestion is a proposal: a similar description or amount is a hint, never proof. You can also{' '}
        <Link to="/month" className="underline">record a payment manually from the monthly view</Link>.
      </p>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div role="group" aria-label="Show" className="grid min-w-0 grid-cols-3 gap-2">
          {VIEWS.map((v) => (
            <Button key={v.state} variant={v.state === state ? 'default' : 'outline'} size="sm" className="h-auto min-h-8 min-w-0 whitespace-normal px-2 py-1" aria-pressed={v.state === state} onClick={() => setState(v.state)}>
              {v.label}
            </Button>
          ))}
        </div>
        {state !== 'closed' && <RunMatching offline={offline} />}
      </div>
      {offline && (
        <p id={OFFLINE_NOTE_ID} className="text-sm text-muted-foreground">
          You are offline. The matches below are the last ones that loaded; every action is off until the connection is back.
        </p>
      )}
      {done && done.subject.replaced?.length ? <ReplacedNotice key={done.saved.id + done.saved.version} saved={done.saved} replaced={done.subject.replaced} where={subjectText(done.subject)} onDone={() => setDone(null)} /> : done && <DoneNotice key={done.saved.id + done.saved.version} done={done} onDone={() => setDone(null)} />}
      {state === 'closed' ? <ClosedList offline={offline} /> : <QueryState query={q} label="payment matches">
        {(items) => {
          const ids = new Set(items.map((m) => m.id))
          const kept = Object.values(held).filter((h) => !ids.has(h.match.id) && h.view === state).map((h) => h.match)
          if (items.length === 0 && kept.length === 0) return <p className="text-sm text-muted-foreground">{view.empty}</p>
          // Suggestions for one occurrence sit together: more than one is an ambiguity the person has to settle.
          const shown = [...items, ...kept]
          const perOccurrence = new Map<string, PaymentMatch[]>()
          for (const m of shown) perOccurrence.set(m.occurrence_id, [...(perOccurrence.get(m.occurrence_id) ?? []), m])
          const ordered = state === 'suggested' ? [...perOccurrence.values()].flat() : shown
          return (
            <>
              <ul className="space-y-3" aria-label={view.label}>
                {ordered.map((m) => (
                  <li key={m.id}>
                    <MatchCard
                      match={held[m.id] && !ids.has(m.id) ? held[m.id]!.match : m}
                      kept={!ids.has(m.id)}
                      competing={state === 'suggested' ? perOccurrence.get(m.occurrence_id)!.length : 1}
                      offline={offline}
                      onHold={onHold}
                      onSaved={(saved, action, before, subject) => setDone({ saved, action, before, subject })}
                    />
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
        }}
      </QueryState>}
    </section>
  )
}
