import { useEffect, useId, useRef, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Undo2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/status-badge'
import { Amount, DateText } from '@/components/formatted'
import { ApiError } from '@/lib/api'
import { useFocusRestore } from '@/lib/focus'
import { useOptimisticSave } from '@/lib/optimistic-save'
import { ProvenanceMarker, ReplacedMarker } from '@/features/month/provenance'
import { useManualPayments } from '@/features/month/api'
import type { ManualPayment } from '@/features/month/types'
import { confirmReplacing, matchAction, matchesKey } from './api'
import type { PaymentMatch } from './types'

/** The manual records of the match's occurrence that this bank payment could be: not replaced already, same currency. Empty while unknown. */
export function useReplaceableRecords(m: PaymentMatch, enabled: boolean): ManualPayment[] {
  const q = useManualPayments(m.occurrence_id, enabled)
  return (q.data ?? []).filter((p) => !p.replaced_by_match_id && p.amount.currency === m.amount.currency)
}

/** The manual records a confirmed match replaced and that are still replaced (restored ones are taken out). */
export function replacedIds(m: PaymentMatch): string[] {
  const ids = new Set<string>()
  for (const e of m.history) {
    for (const id of e.replaced_manual_payment_ids ?? []) ids.add(id)
    for (const id of e.restored_manual_payment_ids ?? []) ids.delete(id)
  }
  return [...ids]
}

export const REPLACE_EXPLAIN =
  'This bank transaction is the payment you recorded by hand. Ledger confirms the match and your manual record is replaced by it: the record is kept in history and no longer counted, so the payment counts once, from the bank. You can undo this right after, and unmatching later brings your record back.'

export interface ReplaceManualProps {
  match: PaymentMatch
  records: ManualPayment[]
  offline?: boolean
  offlineNoteId?: string
  primary?: boolean
  /** Called once the server has confirmed (never before). */
  onSaved: (saved: PaymentMatch, replaced: ManualPayment[]) => void
}

/**
 * "This is the payment I recorded" (D45): a button and its form, rendered inside the card's action row. The save is not
 * optimistic: until the server answers the card says "Not saved yet", and a failure says nothing changed.
 */
export function ReplaceManual({ match: m, records, offline, offlineNoteId, primary, onSaved }: ReplaceManualProps) {
  const uid = useId()
  const [open, setOpen] = useState(false)
  const [picked, setPicked] = useState<string[]>([])
  const first = useRef<HTMLInputElement>(null)
  const focus = useFocusRestore()
  const save = useOptimisticSave<{ ids: string[]; allowOverpayment?: boolean }, PaymentMatch>({
    queryKey: matchesKey,
    mutationFn: (v) => confirmReplacing(m.id, { version: m.version, manualPaymentIds: v.ids, allowOverpayment: v.allowOverpayment }),
    onSaved: (saved, v) => {
      setOpen(false)
      onSaved(saved, records.filter((r) => v.ids.includes(r.id)))
    },
    messages: { saved: () => 'Saved: the match is confirmed and your manual record is replaced.', failed: () => '' },
  })
  const pending = save.phase === 'pending'
  const failed = save.phase === 'failed'
  const err = save.error instanceof ApiError ? save.error : null
  const overpaid = failed && err?.code === 'occurrence_overpaid' && !save.vars?.allowOverpayment
  useEffect(() => {
    if (open) first.current?.focus()
  }, [open])
  const close = () => {
    setOpen(false)
    save.discard()
    focus.restore()
  }
  const show = () => {
    focus.remember()
    save.discard()
    setPicked(records.length === 1 ? [records[0]!.id] : [])
    setOpen(true)
  }
  const none = picked.length === 0
  return (
    <>
      <Button size="sm" variant={primary ? 'default' : 'outline'} className="h-auto min-h-8 whitespace-normal py-1 text-left aria-disabled:cursor-not-allowed aria-disabled:opacity-50"
        disabled={!!offline || open} aria-describedby={offline ? offlineNoteId : undefined} onClick={show}>
        This is the payment I recorded
      </Button>
      {open && (
        <form
          noValidate
          aria-label="This is the payment I recorded"
          className="w-full basis-full space-y-2 rounded-md border border-border-control p-3"
          onKeyDown={(e) => {
            if (e.key === 'Escape' && !pending) {
              e.stopPropagation()
              close()
            }
          }}
          onSubmit={(e) => {
            e.preventDefault()
            if (pending || offline || none) return
            save.save({ ids: picked })
          }}
        >
          <p className="text-muted-foreground">{REPLACE_EXPLAIN}</p>
          <fieldset className="min-w-0 space-y-1" disabled={false}>
            <legend className="font-medium">Which of your manual records is this payment?</legend>
            {records.map((r, i) => (
              <label key={r.id} className="flex min-w-0 items-start gap-2">
                <input ref={i === 0 ? first : undefined} type="checkbox" className="mt-1 size-4 shrink-0" readOnly={pending} checked={picked.includes(r.id)}
                  onChange={(e) => !pending && setPicked((p) => (e.target.checked ? [...p, r.id] : p.filter((x) => x !== r.id)))} />
                <span className="min-w-0">
                  <Amount className="font-mono" amount={r.amount.amount} currency={r.amount.currency} /> paid on <DateText value={r.paid_on} />
                  {r.note ? <span className="block break-words text-xs text-muted-foreground">{r.note}</span> : null}
                </span>
              </label>
            ))}
          </fieldset>
          {none && <p className="text-xs">Choose at least one record. A record you leave out keeps counting as a payment of its own.</p>}
          <div className="flex flex-wrap items-center gap-2">
            <Button type="submit" size="sm" className="h-auto min-h-8 whitespace-normal py-1 text-left aria-disabled:opacity-50" aria-disabled={pending || none || undefined} disabled={!!offline} aria-describedby={offline ? offlineNoteId : undefined}>
              {pending ? 'Confirming…' : 'Confirm and replace my record'}
            </Button>
            <Button type="button" size="sm" variant="ghost" className="h-auto min-h-8 py-1" aria-disabled={pending || undefined} onClick={() => { if (!pending) close() }}>Cancel</Button>
            {pending && <span role="status"><StatusBadge status="pending" label="Not saved yet" /></span>}
          </div>
          {pending && <p className="text-xs text-muted-foreground">Waiting for the server. Nothing has changed yet: the match is still a suggestion and your record still counts.</p>}
          {failed && (
            <div role="alert" id={`${uid}-err`} className="space-y-2">
              <StatusBadge status="save_failed" label="Not saved" />
              <p>
                {overpaid ? 'The records you did not choose, plus this payment, would pay more than the amount due. Choose every record this payment covers, or allow the overpayment.' : save.error?.message} Nothing was saved: the match and your records are unchanged.
              </p>
              <div className="flex flex-wrap gap-2">
                {!overpaid && err?.code !== 'version_conflict' && <Button size="sm" className="h-auto min-h-8 py-1" onClick={() => save.retry()}>Try again</Button>}
                {overpaid && save.vars && <Button size="sm" variant="outline" className="h-auto min-h-8 whitespace-normal py-1" onClick={() => save.save({ ...save.vars!, allowOverpayment: true })}>Allow overpayment and try again</Button>}
                <Button size="sm" variant="ghost" className="h-auto min-h-8 py-1" onClick={() => save.discard()}>Dismiss</Button>
              </div>
            </div>
          )}
        </form>
      )}
    </>
  )
}

/**
 * What was just saved by "This is the payment I recorded": the confirmed bank match, which manual records it replaced and
 * that they are kept, with its Undo (unconfirm, ADR-0045). The Undo is itself confirmed by the server.
 */
export function ReplacedNotice({ saved, replaced, where, onDone }: { saved: PaymentMatch; replaced: ManualPayment[]; where: string; onDone: () => void }) {
  const qc = useQueryClient()
  const box = useRef<HTMLDivElement>(null)
  const undo = useMutation({
    mutationFn: () => matchAction(saved.id, { action: 'unconfirm', version: saved.version, reason: 'Undone right after confirming as the recorded payment' }),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: matchesKey })
      void qc.invalidateQueries({ queryKey: ['occurrences'] })
    },
  })
  useEffect(() => box.current?.focus(), [undo.isSuccess])
  const records = (
    <ul className="space-y-1">
      {replaced.map((r) => (
        <li key={r.id} className="flex flex-wrap items-center gap-x-2 gap-y-1">
          {undo.isSuccess ? <ProvenanceMarker kind="manual" /> : <ReplacedMarker />}
          <span><Amount className="font-mono" amount={r.amount.amount} currency={r.amount.currency} /> paid on <DateText value={r.paid_on} /></span>
        </li>
      ))}
    </ul>
  )
  return (
    <div ref={box} tabIndex={-1} role="status" data-testid="replaced-notice" className="space-y-2 rounded-md border border-border-control bg-surface p-3 text-sm">
      {undo.isSuccess ? (
        <>
          <p>
            <span className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-0.5 text-sm font-medium text-muted-foreground">
              <Undo2 aria-hidden className="size-4" />
              Undone
            </span>{' '}
            The match for {where} is a suggestion again, and your manual {replaced.length === 1 ? 'record counts' : 'records count'} as payment again:
          </p>
          {records}
          <Button variant="ghost" size="sm" onClick={onDone}>Dismiss</Button>
        </>
      ) : (
        <>
          <p>
            <StatusBadge status="paid" label="Saved" /> Match confirmed for {where}: <Amount amount={saved.amount.amount} currency={saved.amount.currency} /> of the bank transaction of{' '}
            <Amount amount={saved.transaction.amount.amount} currency={saved.transaction.amount.currency} /> now pays this occurrence.
          </p>
          <p className="flex flex-wrap items-center gap-2">
            Paid by <ProvenanceMarker kind="bank" /> <span className="text-muted-foreground">(bank: {saved.transaction.provider})</span>
          </p>
          <p>Your manual {replaced.length === 1 ? 'record' : 'records'} {replaced.length === 1 ? 'was' : 'were'} replaced by it. {replaced.length === 1 ? 'It is' : 'They are'} kept in history and no longer counted, so the payment counts once:</p>
          {records}
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" size="sm" className="h-auto min-h-8 whitespace-normal py-1 text-left aria-disabled:opacity-50" aria-disabled={undo.isPending || undefined} onClick={() => { if (!undo.isPending) undo.mutate() }}>
              {undo.isPending ? 'Undoing…' : 'Undo: bring my record back'}
            </Button>
            <Button variant="ghost" size="sm" aria-disabled={undo.isPending || undefined} onClick={() => { if (!undo.isPending) onDone() }}>Dismiss</Button>
            {undo.isPending && <StatusBadge status="pending" label="Not confirmed yet" />}
          </div>
        </>
      )}
      {undo.isError && (
        <p role="alert">
          <StatusBadge status="save_failed" label="Undo not saved" /> The match stays confirmed and your record stays replaced. {undo.error.message} If the undo window has passed, Unmatch on the Confirmed list brings your record back too, but it closes this pair for good.
        </p>
      )}
    </div>
  )
}
