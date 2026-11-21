import { useEffect, useId, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/status-badge'
import { Amount, DateText } from '@/components/formatted'
import { ApiError } from '@/lib/api'
import { isVersionConflict, useOptimisticSave } from '@/lib/optimistic-save'
import { useFocusRestore } from '@/lib/focus'
import { formatInstant } from '@/lib/format'
import { matchAction, matchesKey, useCommitmentName, useOccurrence, type ActionInput } from './api'
import type { MatchAction, MatchOrigin, MatchState, PaymentMatch } from './types'

/** What a person may do with a match in each state (ADR-0035 point 5). rejected and reversed are final. */
export function actionsFor(state: MatchState): MatchAction[] {
  if (state === 'suggested') return ['confirm', 'reject', 'correct']
  if (state === 'confirmed') return ['correct', 'unmatch', 'reverse']
  return []
}

const VERB: Record<MatchAction, { button: string; submit: string; busy: string; done: string }> = {
  confirm: { button: 'Confirm match', submit: 'Confirm match', busy: 'Confirming…', done: 'Match confirmed' },
  reject: { button: 'Reject suggestion', submit: 'Reject now', busy: 'Rejecting…', done: 'Suggestion rejected' },
  unmatch: { button: 'Unmatch (it was wrong)', submit: 'Unmatch now', busy: 'Unmatching…', done: 'Match removed' },
  correct: { button: 'Correct amount', submit: 'Save correction', busy: 'Saving…', done: 'Amount corrected' },
  reverse: { button: 'Reverse (payment bounced)', submit: 'Reverse now', busy: 'Reversing…', done: 'Match reversed' },
}

const EXPLAIN: Partial<Record<MatchAction, string>> = {
  reject: 'This is not the payment for that occurrence. The pair stays closed: automatic matching will not suggest it again.',
  unmatch: 'The match was wrong. The occurrence is no longer paid by this transaction. The pair stays closed: automatic matching will not suggest it again.',
  reverse: 'The match was right but the payment was reversed (bounced, refunded or returned). The occurrence reopens; the history of both decisions is kept.',
  correct: 'Change how much of this transaction pays this occurrence. The transaction and the occurrence stay the same.',
}

const ORIGIN: Record<MatchOrigin, string> = {
  auto_rule: 'Suggested by a matching rule. A similarity score in the reason is a hint, never proof.',
  model_suggestion: 'Suggested by a model. It is never confirmed automatically; only you can confirm it.',
  user: 'Matched by you.',
}

export function describeAction(action: MatchAction): string {
  return VERB[action].done
}

/** After a version conflict: the same change on the server's current version, but only while it still makes sense. */
export function rebaseInput(v: ActionInput, conflict: ApiError): ActionInput | undefined {
  const current = conflict.reconciliation?.current as Partial<PaymentMatch> | undefined
  const version = conflict.reconciliation?.current_version ?? current?.version
  if (!version) return undefined
  if (current?.state && !actionsFor(current.state).includes(v.action)) return undefined
  return { ...v, version }
}

export interface MatchCardProps {
  match: PaymentMatch
  /** Offline or otherwise unable to save: the actions are off and the reason is said. */
  disabledReason?: string
  /** Called once the server has confirmed an action (never before). */
  onSaved: (saved: PaymentMatch, action: MatchAction, before: PaymentMatch) => void
  /** The card has a failed save to show: the page keeps it listed even when a refetch no longer includes the match. */
  onHold: (id: string, match: PaymentMatch | null) => void
  /** True when the match is kept only because its save failed (the list no longer has it). */
  kept?: boolean
}

/**
 * One match for review. Every action is a real save: nothing is shown as changed until the server confirms it
 * (no optimistic cache change, see useOptimisticSave). While the request is out the card says "Not saved yet";
 * a failure says nothing was saved and keeps what was typed; a version conflict loads the latest version and
 * offers the same change on it only when it still applies.
 */
export function MatchCard({ match: m, disabledReason, onSaved, onHold, kept }: MatchCardProps) {
  const qc = useQueryClient()
  const uid = useId()
  const occ = useOccurrence(m.occurrence_id)
  const name = useCommitmentName(occ.data?.commitment_id)
  const [form, setForm] = useState<MatchAction | null>(null)
  const [reason, setReason] = useState('')
  const [amount, setAmount] = useState(m.amount.amount)
  const field = useRef<HTMLInputElement>(null)
  const focus = useFocusRestore()

  const save = useOptimisticSave<ActionInput, PaymentMatch>({
    queryKey: matchesKey,
    mutationFn: (v) => matchAction(m.id, v),
    rebase: rebaseInput,
    onSaved: (saved, v) => {
      void qc.invalidateQueries({ queryKey: ['occurrences'] })
      setForm(null)
      onSaved(saved, v.action, m)
    },
    messages: { saved: (s) => `Saved: the match is now ${s.state}.`, failed: () => '' },
  })
  const pending = save.phase === 'pending'
  const failed = save.phase === 'failed'
  const err = save.error instanceof ApiError ? save.error : null
  const conflict = isVersionConflict(save.error)
  const rebased = save.vars && err && conflict ? rebaseInput(save.vars, err) : undefined
  const overpaid = failed && err?.code === 'occurrence_overpaid' && !save.vars?.allowOverpayment

  // Keep a failed card on the page even if a refetch drops it from the list, and let go once it is resolved.
  const { vars, discard, phase } = save
  useEffect(() => {
    if (phase === 'failed' && vars) onHold(m.id, m)
    else onHold(m.id, null)
  }, [phase, vars, m, onHold])
  useEffect(() => {
    if (form) field.current?.focus()
  }, [form])

  const actions = actionsFor(m.state)
  const t = m.transaction
  const booked = t.booking_status === 'booked'
  const canConfirm = booked && t.countable
  const heading = name.data ?? occ.data?.commitment_id ?? 'Occurrence'
  const run = (action: MatchAction, extra: Partial<ActionInput> = {}) =>
    save.save({ action, version: m.version, currency: m.amount.currency, ...(reason.trim() ? { reason: reason.trim() } : {}), ...extra })
  const open = (a: MatchAction) => {
    focus.remember()
    discard()
    setReason('')
    setAmount(m.amount.amount)
    setForm(a)
  }
  const close = () => {
    setForm(null)
    focus.restore()
  }
  const when = t.booking_date ?? t.value_date ?? t.date
  const btn = (a: MatchAction, variant: 'default' | 'outline' = 'outline') => (
    <Button key={a} size="sm" variant={variant} disabled={pending || !!disabledReason || form !== null || (a === 'confirm' && !canConfirm)}
      onClick={() => (a === 'confirm' ? run('confirm') : open(a))}>
      {pending && save.vars?.action === a ? VERB[a].busy : VERB[a].button}
    </Button>
  )

  return (
    <article aria-labelledby={`${uid}-h`} data-match-id={m.id} data-state={m.state} className="space-y-3 rounded-md border border-border bg-surface p-3 text-sm">
      <header className="flex flex-wrap items-center justify-between gap-2">
        <h3 id={`${uid}-h`} className="font-semibold break-words">{heading}</h3>
        <span className="flex flex-wrap items-center gap-2">
          {m.state === 'suggested' && <span className="rounded-sm border border-border px-1.5 py-0.5 text-xs font-medium">Suggestion</span>}
          {m.state === 'confirmed' && (m.counts_as_evidence
            ? <StatusBadge status="paid" label="Confirmed, counts as payment" />
            : <StatusBadge status="stale" label="Confirmed, not counting" />)}
          {m.state === 'rejected' && <span className="rounded-sm border border-border px-1.5 py-0.5 text-xs font-medium">Rejected</span>}
          {m.state === 'reversed' && <span className="rounded-sm border border-border px-1.5 py-0.5 text-xs font-medium">Reversed</span>}
          {kept && <StatusBadge status="stale" label="No longer in this list" />}
        </span>
      </header>
      <dl className="grid gap-x-4 gap-y-1 sm:grid-cols-[auto_1fr]">
        <dt className="text-muted-foreground">Due</dt>
        <dd>
          {occ.data ? <><DateText value={occ.data.due_date} />, <Amount amount={occ.data.amount.amount} currency={occ.data.amount.currency} /></> : occ.isError ? 'Could not load the occurrence.' : 'Loading…'}
        </dd>
        <dt className="text-muted-foreground">Bank transaction</dt>
        <dd className="space-y-0.5">
          <span className="flex flex-wrap items-center gap-2">
            <DateText value={when} />, <Amount amount={t.amount.amount} currency={t.amount.currency} />
            {booked ? <span className="text-xs text-muted-foreground">booked</span> : <StatusBadge status="pending" label="Pending at the bank" />}
          </span>
          {m.bank_text_redacted ? (
            <span className="block text-xs text-muted-foreground">Bank description hidden: it needs the banking:read grant.</span>
          ) : (
            <span className="block break-words" data-testid="bank-text">{[t.counterparty, t.description].filter(Boolean).join(' · ') || 'No description from the bank.'}</span>
          )}
        </dd>
        <dt className="text-muted-foreground">Allocated</dt>
        <dd><Amount amount={m.amount.amount} currency={m.amount.currency} /> of this transaction pays this occurrence</dd>
        <dt className="text-muted-foreground">Origin</dt>
        <dd>{ORIGIN[m.origin]}</dd>
        {m.reason && (<><dt className="text-muted-foreground">Reason</dt><dd className="break-words">{m.reason}</dd></>)}
      </dl>
      {m.state === 'suggested' && !canConfirm && (
        <p className="text-xs text-muted-foreground">
          {t.superseded_by ? 'This pending transaction was replaced by a booked one; reject this suggestion and review the new one.' : !booked ? 'The bank has not booked this transaction yet. Only a booked transaction can be confirmed; it stays a suggestion until then.' : 'The bank no longer reports this transaction, so it cannot be confirmed.'}
        </p>
      )}
      {m.state === 'confirmed' && m.needs_review && (
        <p role="note" className="text-xs">
          <StatusBadge status="stale" label="Needs review" /> The bank no longer reports this transaction, so it does not count as payment until it is reported again. Your decision is kept.
        </p>
      )}
      {m.history.length > 0 && (
        <details className="text-xs">
          <summary className="cursor-pointer">History ({m.history.length})</summary>
          <ul className="mt-1 space-y-0.5">
            {m.history.map((e, i) => (
              <li key={i}>
                {formatInstant(e.at)}: {e.action} → {e.state}, <Amount amount={e.amount.amount} currency={e.amount.currency} />, by {e.by.kind === 'system' ? 'the matching engine' : e.by.kind}{e.reason ? `: ${e.reason}` : ''}
              </li>
            ))}
          </ul>
        </details>
      )}

      {actions.length > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          {actions.map((a) => btn(a, a === 'confirm' ? 'default' : 'outline'))}
          {pending && form === null && <span role="status"><StatusBadge status="pending" label="Not saved yet" /></span>}
        </div>
      )}
      {disabledReason && actions.length > 0 && <p className="text-xs text-muted-foreground">{disabledReason}</p>}

      {form !== null && (
        <form
          noValidate
          aria-label={VERB[form].button}
          className="space-y-2 rounded-md border border-border-control p-3"
          onSubmit={(e) => {
            e.preventDefault()
            run(form, form === 'correct' ? { amount: amount.trim() } : {})
          }}
        >
          <p className="text-muted-foreground">{EXPLAIN[form]}</p>
          {form === 'correct' && (
            <label className="grid gap-1">
              Allocated amount ({m.amount.currency})
              <input ref={field} inputMode="decimal" autoComplete="off" disabled={pending} value={amount} onChange={(e) => setAmount(e.target.value)}
                aria-invalid={err?.field === 'amount' ? true : undefined}
                className="h-9 rounded-md border border-border-control bg-surface px-2 font-mono focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring" />
              {err?.field?.startsWith('amount') && <span>{err.message}</span>}
            </label>
          )}
          <label className="grid gap-1">
            Reason (optional)
            <input ref={form === 'correct' ? undefined : field} maxLength={500} autoComplete="off" disabled={pending} value={reason} onChange={(e) => setReason(e.target.value)}
              className="h-9 rounded-md border border-border-control bg-surface px-2 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring" />
          </label>
          <div className="flex flex-wrap items-center gap-2">
            <Button type="submit" size="sm" disabled={pending || !!disabledReason}>{pending ? VERB[form].busy : VERB[form].submit}</Button>
            <Button type="button" size="sm" variant="ghost" disabled={pending} onClick={close}>Cancel</Button>
            {pending && <span role="status"><StatusBadge status="pending" label="Not saved yet" /></span>}
          </div>
        </form>
      )}
      {pending && <p className="text-xs text-muted-foreground">Waiting for the server. Nothing has changed yet: this match is still {m.state}.</p>}

      {failed && (
        <div role="alert" className="space-y-2">
          <StatusBadge status="save_failed" label={conflict ? 'Not saved: changed elsewhere' : 'Not saved'} />
          {conflict && err ? (
            <p>
              This match changed elsewhere, so your change was not sent as it was. The latest version is shown above.{' '}
              {rebased ? 'Check it, then save your change again on top of it.' : 'It no longer allows this change, so nothing more can be done here.'}
            </p>
          ) : (
            <p>{overpaid ? 'This would pay more than the amount due. ' : ''}{save.error?.message} Nothing was saved; the match is unchanged.</p>
          )}
          <div className="flex flex-wrap gap-2">
            {conflict && rebased && <Button size="sm" onClick={() => save.retry()}>Save again on the latest version</Button>}
            {!conflict && !overpaid && <Button size="sm" onClick={() => save.retry()}>Try again</Button>}
            {overpaid && save.vars && <Button size="sm" variant="outline" onClick={() => save.save({ ...save.vars!, allowOverpayment: true })}>Allow overpayment and try again</Button>}
            <Button size="sm" variant="ghost" onClick={() => { discard(); if (form) close() }}>Dismiss</Button>
          </div>
        </div>
      )}
    </article>
  )
}
