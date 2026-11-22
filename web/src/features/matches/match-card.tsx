import { useEffect, useId, useRef, useState } from 'react'
import { MerchantDisclosure } from '@/features/merchants/transaction-merchant'
import { OrganiseDisclosure } from '@/features/organisation/organise-panel'
import { useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/status-badge'
import { Amount, DateText } from '@/components/formatted'
import { ApiError } from '@/lib/api'
import { isVersionConflict, useOptimisticSave } from '@/lib/optimistic-save'
import { useFocusRestore } from '@/lib/focus'
import { ProvenanceMarker } from '@/features/month/provenance'
import { useManualPayments } from '@/features/month/api'
import { isZeroAmount, monthLabel, utcStamp } from '@/features/month/month'
import { useSession } from '@/lib/auth'
import { Bot, KeyRound } from 'lucide-react'
import { AMBIGUITY_CODES, actorPhrase, decisionLine, deciderMarker, historyVerb, originLine, plainReason, wasUnmatched } from './explain'
import { matchAction, matchesKey, useCommitmentName, useOccurrence, type ActionInput } from './api'
import type { ManualPayment } from '@/features/month/types'
import { ReplaceManual, replacedIds, useReplaceableRecords } from './replace-manual'
import type { MatchAction, MatchState, PaymentMatch } from './types'

/** What Unmatch says about the manual records a match replaced (ADR-0049). A record deleted while replaced stays deleted, so it does not come back (X5j). */
function unmatchRecordsCopy(live: number, deleted: number, held: boolean, check: 'checking' | 'failed' | null): string {
  if (check === 'checking') return ' Whether your manual records of this payment still exist is being checked.'
  if (check === 'failed') return ' Your manual records of this payment could not be checked, so this does not say whether they come back.'
  const parts: string[] = []
  if (live > 0) parts.push(held
    ? `Your manual ${live === 1 ? 'record of this payment already counts' : 'records of this payment already count'} again, because the bank no longer reports the payment. Unmatching keeps ${live === 1 ? 'it' : 'them'} counting.`
    : `Your manual ${live === 1 ? 'record' : 'records'} of this payment ${live === 1 ? 'comes' : 'come'} back and ${live === 1 ? 'counts' : 'count'} again.`)
  if (deleted > 0) parts.push(deleted === 1
    ? 'A manual record of this payment was deleted while it was replaced, so it does not come back.'
    : `${deleted} manual records of this payment were deleted while they were replaced, so they do not come back.`)
  return parts.map((p) => ` ${p}`).join('')
}

/** What Reverse says about the manual records a match replaced. While their list is not settled, it does not say a record counts (X5k). */
function reverseRecordsCopy(live: number, held: boolean, check: 'checking' | 'failed' | null): string {
  if (check === 'checking') return ' Whether your manual records of this payment still exist is being checked.'
  if (check === 'failed') return ' Your manual records of this payment could not be checked, so this does not say whether they count again.'
  if (live === 0) return ''
  if (held) return ` Your manual ${live === 1 ? 'record of this payment counts' : 'records of this payment count'} again right now. Reversing keeps ${live === 1 ? 'it' : 'them'} replaced: ${live === 1 ? 'it stops' : 'they stop'} counting and nothing stands in for the payment.`
  return ' Your manual record of this payment stays replaced (it was that payment); you can record a new payment in the monthly view.'
}

/** What a person may do with a match in each state (ADR-0035 point 5). rejected and reversed are final. */
export function actionsFor(state: MatchState): MatchAction[] {
  if (state === 'suggested') return ['confirm', 'reject', 'correct']
  if (state === 'confirmed') return ['correct', 'unmatch', 'reverse']
  return []
}

const VERB: Record<MatchAction, { button: string; submit: string; busy: string; done: string }> = {
  confirm: { button: 'Confirm match', submit: 'Confirm match', busy: 'Confirming…', done: 'Match confirmed' },
  reject: { button: 'Reject suggestion', submit: 'Reject now', busy: 'Rejecting…', done: 'Suggestion rejected' },
  unmatch: { button: 'Unmatch (final)', submit: 'Unmatch now', busy: 'Unmatching…', done: 'Match removed' },
  correct: { button: 'Correct amount', submit: 'Save correction', busy: 'Saving…', done: 'Amount corrected' },
  reverse: { button: 'Reverse (payment bounced)', submit: 'Reverse now', busy: 'Reversing…', done: 'Match reversed' },
  unconfirm: { button: 'Undo confirmation', submit: 'Undo confirmation', busy: 'Undoing…', done: 'Confirmation undone' },
}

const EXPLAIN: Partial<Record<MatchAction, string>> = {
  reject: 'This is not the payment for that occurrence. The pair stays closed: automatic matching will not suggest it again.',
  unmatch: 'The match was wrong. The occurrence is no longer paid by this transaction. This is final for now: the pair stays closed, automatic matching will not suggest it again, and this screen cannot match it again. You can still record a payment by hand in the monthly view.',
  reverse: 'The match was right but the payment was reversed (bounced, refunded or returned). The occurrence reopens; the history of both decisions is kept.',
  correct: 'Change how much of this transaction pays this occurrence. The transaction and the occurrence stay the same.',
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

/** What a Done notice names: the commitment and month of the occurrence the match pays (D36). */
export interface Subject {
  name?: string
  month?: string
  /** Set when the save was "This is the payment I recorded": the manual records it replaced. */
  replaced?: ManualPayment[]
}

/** "Rent, November 2026": the commitment and the month, with whatever is known. */
export function subjectText(s: Subject): string {
  const month = s.month ? monthLabel(s.month) : ''
  return [s.name ?? 'this commitment', month].filter(Boolean).join(', ')
}

/** The last characters of a transaction id: enough to tell apart identical-looking transactions on one screen. */
export function shortRef(id: string): string {
  return id.slice(-8)
}

export const OFFLINE_NOTE_ID = 'matches-offline-note'

const STATE_WORD: Record<MatchState, string> = { suggested: 'a suggestion', confirmed: 'confirmed', rejected: 'rejected', reversed: 'reversed' }
const DECIMAL_AMOUNT = /^\d+(\.\d+)?$/
const AMOUNT_HELP = 'Enter an amount such as 12.50, more than zero.'
type PaidBy = 'bank' | 'manual' | 'override' | 'unknown'
/**
 * What the already-paid note says, by where the payment comes from. The server counts manual records and a bank match
 * alike when it checks an overpayment, and has no way to link a bank transaction to a manual record: so for a manual
 * payment, confirming records the same payment twice unless the overpayment is allowed on purpose.
 */
const PAID_NOTE: Record<PaidBy, string> = {
  bank: 'Another bank transaction already pays this occurrence in full. Confirming this one would pay more than the amount due, so the server refuses it unless you allow an overpayment. Reject it unless you mean to pay twice.',
  manual: 'You recorded this occurrence as paid by hand. If this transaction is that payment, reject it: your record already counts, and confirming would count it twice (the server refuses that unless you allow an overpayment). Ledger cannot link the two, so confirm only if you paid twice.',
  override: 'This occurrence was marked paid by hand, with a reason, over what the payments show. Confirming this bank transaction would add a payment on top of that, and the server refuses it unless you allow an overpayment. If the transaction is that payment, reject this suggestion; confirm it only if you really paid twice.',
  unknown: 'This occurrence is already paid in full. Confirming this transaction would pay more than the amount due, so the server refuses it unless you allow an overpayment. Reject it unless you mean to pay twice.',
}
/** What the already-paid note says when the manual records can be named and replaced (D45): the bank payment can be linked to them. */
const PAID_NOTE_LINKABLE = 'You recorded this occurrence as paid by hand. If this bank transaction is that payment, choose "This is the payment I recorded": your record is kept in history and replaced by the bank match, so the payment counts once. Confirming it as a separate payment would count it twice (the server refuses that unless you allow an overpayment); do that only if you really paid twice.'
const CTRL = 'h-auto min-h-8 whitespace-normal py-1 text-left aria-disabled:cursor-not-allowed aria-disabled:opacity-50'

export interface MatchCardProps {
  match: PaymentMatch
  /** Offline: the actions are off (the page says why, once, in the element OFFLINE_NOTE_ID). */
  offline?: boolean
  /** How many suggestions on this page compete for the same occurrence (more than one is an ambiguity). */
  competing?: number
  /** Called once the server has confirmed an action (never before). */
  onSaved: (saved: PaymentMatch, action: MatchAction, before: PaymentMatch, subject: Subject) => void
  /** The card has a failed save to show: the page keeps it listed even when a refetch no longer includes the match. */
  onHold: (id: string, match: PaymentMatch | null) => void
  /** True when the match is kept only because its save failed (the list no longer has it). */
  kept?: boolean
}

/**
 * One match for review. Every action is a real save: nothing is shown as changed until the server confirms it
 * (no optimistic cache change, see useOptimisticSave). While the request is out the card says "Not saved yet"
 * and keeps keyboard focus where it was (aria-disabled, not disabled); a failure says nothing was saved and keeps
 * what was typed; a version conflict shows the server's latest version of the match, with only the actions that
 * version still allows, and offers the same change on it only when it still applies.
 */
export function MatchCard({ match: listed, offline, competing = 1, onSaved, onHold, kept }: MatchCardProps) {
  const qc = useQueryClient()
  const uid = useId()
  const me = useSession().data?.user_id
  const occ = useOccurrence(listed.occurrence_id)
  const name = useCommitmentName(occ.data?.commitment_id)
  const [form, setForm] = useState<MatchAction | null>(null)
  const [reason, setReason] = useState('')
  const [amount, setAmount] = useState(listed.amount.amount)
  const [localError, setLocalError] = useState<string | null>(null)
  const field = useRef<HTMLInputElement>(null)
  const focus = useFocusRestore()

  const save = useOptimisticSave<ActionInput, PaymentMatch>({
    queryKey: matchesKey,
    mutationFn: (v) => matchAction(listed.id, v),
    rebase: rebaseInput,
    onSaved: (saved, v) => {
      void qc.invalidateQueries({ queryKey: ['occurrences'] })
      // A confirmation changes what its competitors can do: reload them so they show their new state (R2).
      void qc.invalidateQueries({ queryKey: matchesKey })
      setForm(null)
      onSaved(saved, v.action, m, { name: name.data, month: occ.data?.month })
    },
    messages: { saved: (s) => `Saved: the match is now ${s.state}.`, failed: () => '' },
  })
  const pending = save.phase === 'pending'
  const failed = save.phase === 'failed'
  const err = save.error instanceof ApiError ? save.error : null
  const conflict = isVersionConflict(save.error)
  const rebased = save.vars && err && conflict ? rebaseInput(save.vars, err) : undefined
  const overpaid = failed && err?.code === 'occurrence_overpaid' && !save.vars?.allowOverpayment
  const invalid = failed && err?.status === 422
  const amountInvalid = invalid && !!err?.field?.startsWith('amount')

  // After a conflict the card shows the server's latest copy (from the 409), never the copy the person acted on.
  const latest = conflict ? (err?.reconciliation?.current as PaymentMatch | undefined) : undefined
  const latestKnown = !!latest && typeof latest.state === 'string' && Array.isArray(latest.history)
  const m = conflict && latestKnown ? latest! : listed
  const staleUnknown = conflict && !latestKnown

  // Keep a failed card on the page even if a refetch drops it from the list, and let go once it is resolved.
  const { vars, discard, phase } = save
  useEffect(() => {
    if (phase === 'failed' && vars) onHold(m.id, m)
    else onHold(m.id, null)
  }, [phase, vars, m, onHold])
  useEffect(() => {
    if (form) field.current?.focus()
  }, [form])

  const actions = staleUnknown ? [] : actionsFor(m.state)
  // After a conflict that cannot be re-applied the form has nothing left to send: it is hidden, not left enabled (R1).
  const formLive = form !== null && !(conflict && !rebased)
  // Another transaction already pays this occurrence: a suggestion for it is no longer a choice (R2).
  const pay = occ.data?.payment
  const alreadyPaid = m.state === 'suggested' && pay?.status === 'paid'
  // Where the existing payment comes from decides what is honest to say: a bank match, a manual record or a status set by hand.
  const paidBy: PaidBy = !pay?.override?.applied ? ((pay?.bank_evidence_count ?? 0) > 0 ? 'bank' : (pay?.manual_evidence_count ?? 0) > 0 ? 'manual' : 'unknown') : ((pay.bank_evidence_count ?? 0) > 0 ? 'bank' : 'override')
  const replaceable = useReplaceableRecords(m, m.state === 'suggested' && (pay?.manual_evidence_count ?? 0) > 0)
  const canReplace = m.state === 'suggested' && replaceable.length > 0 && m.transaction.booking_status === 'booked' && m.transaction.countable
  const replacedAll = m.state === 'confirmed' ? replacedIds(m) : []
  // A replaced record deleted since is not listed among the occurrence's manual records, and it stays deleted when the match is unmatched or reversed (X5j).
  const manualList = useManualPayments(m.occurrence_id, replacedAll.length > 0)
  const listedIds = new Set((manualList.data ?? []).map((p) => p.id))
  // A list that is refetching is not settled: a record may have been deleted since the last answer, so the copy says it is being checked.
  const listKnown = manualList.isSuccess && !manualList.isFetching
  const deletedNow = listKnown ? replacedAll.filter((id) => !listedIds.has(id)).length : 0
  const replacedNow = replacedAll.length - deletedNow
  const listCheck = replacedAll.length > 0 && !listKnown ? (manualList.isError && !manualList.isFetching ? 'failed' : 'checking') : null
  // A held match (ADR-0049 amendment A): its replaced records already count again, because the bank no longer reports the payment.
  const heldNow = replacedNow > 0 && !m.counts_as_evidence
  // A final refusal of "This is the payment I recorded" lives here, not in ReplaceManual: the refetch it triggers can unmount that form.
  const [refusal, setRefusal] = useState<string | null>(null)
  const refusalBox = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (refusal) refusalBox.current?.focus()
  }, [refusal])
  const replaceBtn = canReplace && (
    <ReplaceManual match={m} records={replaceable} offline={offline} offlineNoteId={OFFLINE_NOTE_ID} primary={paidBy === 'manual'} onRefused={setRefusal} onStart={() => setRefusal(null)}
      onSaved={(saved, replaced) => { setRefusal(null); onSaved(saved, 'confirm', m, { name: name.data, month: occ.data?.month, replaced }) }} />
  )
  const alertBox = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (conflict) alertBox.current?.focus()
  }, [conflict])
  const t = m.transaction
  const booked = t.booking_status === 'booked'
  const canConfirm = booked && t.countable
  const heading = name.data ?? (occ.isError || name.isError ? 'Unnamed commitment' : 'Loading commitment…')
  const plain = plainReason(m.reason)
  // The reason was written when the suggestion was made: once the occurrence is paid, its ambiguity lines (by engine code) are stale.
  const reasonLines = alreadyPaid ? plain.lines.filter((l) => !(l.code !== null && AMBIGUITY_CODES.includes(l.code))) : plain.lines
  const decision = decisionLine(m, me)
  const marker = deciderMarker(m)
  const shortfall = m.state === 'confirmed' && occ.data?.payment?.status === 'paid' && occ.data.payment.shortfall_amount && !isZeroAmount(occ.data.payment.shortfall_amount.amount) ? occ.data.payment.shortfall_amount : null
  // The reason typed in a form belongs to that form's action only: a one-step Confirm never carries it.
  const run = (action: MatchAction, extra: Partial<ActionInput> = {}) => {
    setRefusal(null)
    save.save({ action, version: m.version, currency: m.amount.currency, ...extra })
  }
  const open = (a: MatchAction) => {
    focus.remember()
    setRefusal(null)
    discard()
    setReason('')
    setAmount(m.amount.amount)
    setLocalError(null)
    setForm(a)
  }
  const close = () => {
    setForm(null)
    setReason('')
    setLocalError(null)
    focus.restore()
  }
  const when = t.booking_date ?? t.value_date ?? t.date
  const btn = (a: MatchAction, variant: 'default' | 'outline' = 'outline') => (
    <Button key={a} size="sm" variant={variant} className={CTRL} aria-disabled={pending || undefined} aria-describedby={offline ? OFFLINE_NOTE_ID : undefined}
      disabled={!!offline || formLive || (a === 'confirm' && !canConfirm)}
      onClick={() => {
        if (pending) return
        if (a === 'confirm') run('confirm')
        else open(a)
      }}>
      {pending && save.vars?.action === a ? VERB[a].busy : a === 'confirm' && canReplace ? 'Confirm as a separate payment' : VERB[a].button}
    </Button>
  )
  const stateBadge = (
    <>
      {m.state === 'suggested' && <span className="rounded-sm border border-border px-1.5 py-0.5 text-xs font-medium">Suggestion</span>}
      {m.state === 'confirmed' && (m.counts_as_evidence
        ? <><StatusBadge status="paid" label="Confirmed, counts as payment" /><ProvenanceMarker kind="bank" /></>
        : <StatusBadge status="stale" label="Confirmed, not counting" />)}
      {m.state === 'rejected' && <span className="rounded-sm border border-border px-1.5 py-0.5 text-xs font-medium">{wasUnmatched(m) ? 'Unmatched' : 'Rejected'}</span>}
      {m.state === 'reversed' && <span className="rounded-sm border border-border px-1.5 py-0.5 text-xs font-medium">Reversed</span>}
      {marker && (
        <span data-decider={marker.kind} className="inline-flex items-center gap-1 rounded-sm border border-border px-1.5 py-0.5 text-xs text-muted-foreground">
          {marker.kind === 'system' ? <Bot aria-hidden className="size-3.5" /> : <KeyRound aria-hidden className="size-3.5" />}
          {marker.label}
        </span>
      )}
      {kept && <StatusBadge status="stale" label={m.state === 'suggested' ? 'No longer in this list' : `Changed elsewhere: now ${m.state}`} />}
    </>
  )

  return (
    <article aria-labelledby={`${uid}-h`} data-match-id={m.id} data-state={m.state} className="space-y-3 rounded-md border border-border bg-surface p-3 text-sm">
      <header className="flex flex-wrap items-center justify-between gap-2">
        <h3 id={`${uid}-h`} className="min-w-0 font-semibold break-words">{heading}</h3>
        <span className="flex min-w-0 flex-wrap items-center gap-2">{stateBadge}</span>
      </header>
      {staleUnknown && (
        <p role="note" className="text-xs">
          <StatusBadge status="stale" label="Out of date" /> This is the version you saw; the latest one could not be loaded, so no action is offered here. Reload the list.
        </p>
      )}
      <dl className="grid gap-x-4 gap-y-1 sm:grid-cols-[auto_1fr]">
        <dt className="text-muted-foreground">Due</dt>
        <dd className="min-w-0">
          {occ.data ? <><DateText value={occ.data.due_date} />, <Amount className="font-mono" amount={occ.data.amount.amount} currency={occ.data.amount.currency} /></> : occ.isError ? 'Could not load the occurrence.' : 'Loading…'}
        </dd>
        <dt className="text-muted-foreground">Bank transaction</dt>
        <dd className="min-w-0 space-y-0.5">
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span><DateText value={when} />, <Amount className="font-mono" amount={t.amount.amount} currency={t.amount.currency} /></span>
            {booked ? <span className="text-xs text-muted-foreground">booked</span> : <StatusBadge status="pending" label="Pending at the bank" />}
          </span>
          <span className="block text-xs text-muted-foreground" data-testid="bank-ref">Transaction ref …{shortRef(t.id)} (Ledger's id for this bank transaction; two transactions with the same text are still two payments)</span>
          {m.bank_text_redacted ? (
            <span className="block text-xs text-muted-foreground">Bank description hidden: it needs the banking:read grant.</span>
          ) : (
            <span className="block break-words" data-testid="bank-text">{[t.counterparty, t.description].filter(Boolean).join(' · ') || 'No description from the bank.'}</span>
          )}
        </dd>
        <dt className="text-muted-foreground">Allocated</dt>
        <dd className="min-w-0">
          {m.state === 'rejected' && !wasUnmatched(m) ? <>Amount proposed <Amount className="font-mono" amount={m.amount.amount} currency={m.amount.currency} /> of this transaction; it did not count as payment</>
            : wasUnmatched(m) ? <><Amount className="font-mono" amount={m.amount.amount} currency={m.amount.currency} /> of this transaction no longer counts: unmatched</>
            : m.state === 'reversed' ? <><Amount className="font-mono" amount={m.amount.amount} currency={m.amount.currency} /> of this transaction no longer counts: payment reversed</>
            : m.state === 'suggested' ? <><Amount className="font-mono" amount={m.amount.amount} currency={m.amount.currency} /> of this transaction would pay this occurrence if you confirm it; it does not count yet</>
            : m.counts_as_evidence ? <><Amount className="font-mono" amount={m.amount.amount} currency={m.amount.currency} /> of this transaction pays this occurrence</>
            : <><Amount className="font-mono" amount={m.amount.amount} currency={m.amount.currency} /> of this transaction does not count as payment right now</>}
        </dd>
        {shortfall && (
          <>
            <dt className="text-muted-foreground">Short</dt>
            <dd className="min-w-0">Paid within the commitment's tolerance; <Amount className="font-mono" amount={shortfall.amount} currency={shortfall.currency} /> short.</dd>
          </>
        )}
        <dt className="text-muted-foreground">Origin</dt>
        <dd className="min-w-0">{originLine(m, me)}</dd>
        {decision && (
          <>
            <dt className="text-muted-foreground">Decision</dt>
            <dd className="min-w-0" data-testid="decision">{decision}</dd>
          </>
        )}
        {(reasonLines.length > 0 || plain.hint || plain.verbatim || alreadyPaid) && (
          <>
            <dt className="text-muted-foreground">Why</dt>
            <dd className="min-w-0 break-words">
              {reasonLines.length > 0 && <ul className="list-disc space-y-0.5 pl-4">{reasonLines.map((l) => <li key={l.text}>{l.text}</li>)}</ul>}
              {plain.verbatim && <p>{plain.verbatim}</p>}
              {plain.hint && <p className="text-xs text-muted-foreground">{plain.hint}</p>}
              {alreadyPaid && <p className="text-xs text-muted-foreground">This reason was written when the suggestion was made and may be out of date.</p>}
            </dd>
          </>
        )}
      </dl>
      {alreadyPaid && (
        <p role="note" data-testid="already-paid" className="text-xs">
          <StatusBadge status="stale" label="Already paid" /> {canReplace && paidBy === 'manual' ? PAID_NOTE_LINKABLE : PAID_NOTE[paidBy]}
        </p>
      )}
      {m.state === 'suggested' && competing > 1 && !alreadyPaid && (
        <p role="note" data-testid="competing" className="text-xs">
          <StatusBadge status="stale" label="Needs your choice" /> {competing} suggestions compete for this occurrence. Check which transaction is the payment, confirm that one and reject the rest.
        </p>
      )}
      {m.state === 'suggested' && !canConfirm && (
        <p className="text-xs text-muted-foreground">
          {t.superseded_by ? 'This pending transaction was replaced by a booked one; reject this suggestion and review the new one.' : !booked ? 'The bank has not booked this transaction yet. Only a booked transaction can be confirmed; it stays a suggestion until then.' : 'The bank no longer reports this transaction, so it cannot be confirmed.'}
        </p>
      )}
      {m.state === 'confirmed' && m.needs_review && (
        <p role="note" className="text-xs">
          <StatusBadge status="stale" label="Bank no longer reports it" /> The bank no longer reports this transaction, so it does not count as payment until it is reported again. Your decision is kept.
        </p>
      )}
      {m.history.length > 0 && (
        <details className="text-xs">
          <summary className="cursor-pointer">History ({m.history.length})</summary>
          <ul className="mt-1 space-y-0.5">
            {m.history.map((e, i) => (
              <li key={i} className="break-words">
                {utcStamp(e.at)}: {historyVerb(e)}{' '}
                {(e.action === 'corrected' || e.action === 'confirmed' || e.action === 'suggested') && <><Amount className="font-mono" amount={e.amount.amount} currency={e.amount.currency} />{' '}</>}
                by {actorPhrase(e.by, me)}{plainReason(e.reason).verbatim ? `: ${e.reason}` : ''}
                {(e.replaced_manual_payment_ids?.length ?? 0) > 0 && ` Replaced ${e.replaced_manual_payment_ids!.length} manual ${e.replaced_manual_payment_ids!.length === 1 ? 'record' : 'records'}, kept in history.`}
                {(e.restored_manual_payment_ids?.length ?? 0) > 0 && ` Released ${e.restored_manual_payment_ids!.length} manual ${e.restored_manual_payment_ids!.length === 1 ? 'record' : 'records'} it had replaced.`}
              </li>
            ))}
          </ul>
        </details>
      )}

      {actions.length > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          {replaceBtn}
          {(alreadyPaid ? [...actions].sort((a, b) => Number(b === 'reject') - Number(a === 'reject')) : actions).map((a) => btn(a, a === (alreadyPaid ? (paidBy === 'bank' || paidBy === 'unknown' ? 'reject' : '') : 'confirm') ? 'default' : 'outline'))}
          {pending && form === null && <span role="status"><StatusBadge status="pending" label="Not saved yet" /></span>}
        </div>
      )}

      {refusal && (
        <div ref={refusalBox} tabIndex={-1} role="alert" data-testid="replace-refused" className="space-y-2 rounded-md border border-border-control p-3">
          <StatusBadge status="save_failed" label="Not saved" />
          <p>{refusal} Nothing was saved: the match and your records are unchanged.</p>
          <Button size="sm" variant="ghost" className="h-auto min-h-8 py-1" onClick={() => setRefusal(null)}>Dismiss</Button>
        </div>
      )}

      {form !== null && formLive && (
        <form
          noValidate
          aria-label={VERB[form].button}
          className="space-y-2 rounded-md border border-border-control p-3"
          onKeyDown={(e) => {
            if (e.key === 'Escape' && !pending) {
              e.stopPropagation()
              close()
            }
          }}
          onSubmit={(e) => {
            e.preventDefault()
            if (pending || offline) return
            const extra: Partial<ActionInput> = reason.trim() ? { reason: reason.trim() } : {}
            if (form === 'correct') {
              const a = amount.trim()
              if (!DECIMAL_AMOUNT.test(a) || isZeroAmount(a)) {
                setLocalError(AMOUNT_HELP)
                field.current?.focus()
                return
              }
              setLocalError(null)
              extra.amount = a
            }
            run(form, extra)
          }}
        >
          <p className="text-muted-foreground">
            {EXPLAIN[form]}
            {form === 'unmatch' && replacedAll.length > 0 && unmatchRecordsCopy(replacedNow, deletedNow, heldNow, listCheck)}
{form === 'reverse' && replacedAll.length > 0 && reverseRecordsCopy(replacedNow, heldNow, listCheck)}
          </p>
          {form === 'correct' && (
            <label className="grid min-w-0 gap-1">
              Allocated amount ({m.amount.currency})
              <input ref={field} inputMode="decimal" autoComplete="off" readOnly={pending} value={amount}
                onChange={(e) => {
                  setAmount(e.target.value)
                  setLocalError(null)
                  if (invalid) discard()
                }}
                aria-invalid={localError || amountInvalid ? true : undefined}
                aria-describedby={localError || amountInvalid ? `${uid}-amount-error` : undefined}
                className="h-auto min-h-9 w-full min-w-0 rounded-md border border-border-control bg-surface px-2 font-mono focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring" />
              {(localError || amountInvalid) && <span id={`${uid}-amount-error`} className="text-xs font-medium">{localError ?? AMOUNT_HELP}</span>}
            </label>
          )}
          <label className="grid min-w-0 gap-1">
            Reason (optional)
            <input ref={form === 'correct' ? undefined : field} maxLength={500} autoComplete="off" readOnly={pending} value={reason} onChange={(e) => setReason(e.target.value)}
              className="h-auto min-h-9 w-full min-w-0 rounded-md border border-border-control bg-surface px-2 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring" />
          </label>
          <div className="flex flex-wrap items-center gap-2">
            {conflict ? (
              <span className="text-xs">The match changed. Use "Save again on the latest version" below, or cancel.</span>
            ) : (
              <Button type="submit" size="sm" className={CTRL} aria-disabled={pending || undefined} disabled={!!offline} aria-describedby={offline ? OFFLINE_NOTE_ID : undefined}>
                {pending ? VERB[form].busy : VERB[form].submit}
              </Button>
            )}
            <Button type="button" size="sm" variant="ghost" className={CTRL} aria-disabled={pending || undefined} onClick={() => { if (!pending) close() }}>Cancel</Button>
            {pending && <span role="status"><StatusBadge status="pending" label="Not saved yet" /></span>}
          </div>
        </form>
      )}
      {pending && <p className="text-xs text-muted-foreground">Waiting for the server. Nothing has changed yet: this match is still {STATE_WORD[m.state]}.</p>}

      {failed && (
        <div ref={alertBox} tabIndex={-1} role="alert" className="space-y-2 focus:outline-none">
          <StatusBadge status="save_failed" label={conflict ? 'Not saved: changed elsewhere' : 'Not saved'} />
          {conflict && err ? (
            <p>
              {latestKnown ? `This match changed elsewhere, so your change was not sent as it was. The latest version is shown above (it is now ${STATE_WORD[m.state]}).` : 'This match changed elsewhere, so your change was not sent as it was. The card above is out of date.'}{' '}
              {rebased ? 'Check it, then save your change again on top of it.' : latestKnown ? (actions.length > 0 ? 'It no longer allows this change; choose from what it allows now.' : 'It no longer allows any change, so nothing more can be done here.') : 'Reload the list to see the latest.'}
            </p>
          ) : invalid ? (
            <p>{amountInvalid ? 'Nothing was saved. Fix the amount above and save again.' : `${save.error?.message} Nothing was saved.`}</p>
          ) : (
            <p>{overpaid ? 'This would pay more than the amount due.' : save.error?.message} Nothing was saved; the match is unchanged.</p>
          )}
          <div className="flex flex-wrap gap-2">
            {conflict && rebased && <Button size="sm" className={CTRL} onClick={() => save.retry()}>Save again on the latest version</Button>}
            {!conflict && !overpaid && !invalid && <Button size="sm" className={CTRL} onClick={() => save.retry()}>Try again</Button>}
            {overpaid && save.vars && <Button size="sm" variant="outline" className={CTRL} onClick={() => save.save({ ...save.vars!, allowOverpayment: true })}>Allow overpayment and try again</Button>}
            <Button size="sm" variant="ghost" className={CTRL} onClick={() => { discard(); if (form && !invalid) close() }}>Dismiss</Button>
          </div>
        </div>
      )}
      <div className="flex flex-wrap items-start gap-x-4 gap-y-1">
      <MerchantDisclosure inline transactionId={t.id} subject={`the ${t.amount.amount} ${t.amount.currency} bank transaction of ${when}`} />
      <OrganiseDisclosure inline objectType="transaction" objectId={t.id} subject={`the ${t.amount.amount} ${t.amount.currency} bank transaction of ${when}${name.data ? ` matched to ${name.data}` : ''}`} />
      </div>
    </article>
  )
}
