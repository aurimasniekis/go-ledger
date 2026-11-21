import { useId, useRef, useState, useEffect } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useOptimisticSave } from '@/lib/optimistic-save'
import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/status-badge'
import { ApiError } from '@/lib/api'
import { monthLabel } from './month'
import { deletePayment, monthKey, recordPayment } from './api'
import type { ManualPayment, MonthItem } from './types'

export interface PaymentPanelProps {
  month: string
  asOf: string
  /** Occurrences that can take a payment (not paid). */
  items: MonthItem[]
  initialId: string
  /** The occurrence as it was when the form opened, for when a refetch drops it from items. */
  initialItem: MonthItem
  /** Show the occurrence select (the header entry point); a form opened from a row stays on that row. */
  chooser: boolean
  onClose: () => void
  onSaved: (p: ManualPayment, item: MonthItem) => void
}

/**
 * The form of the screen's primary action: record a manual payment (C3). The
 * payment counts only once the server has answered: while the request is out
 * the panel says "Not saved yet", and a failure says it was not saved and
 * keeps what was typed. Nothing is changed optimistically.
 */
export function PaymentPanel(p: PaymentPanelProps) {
  const [id, setId] = useState(p.initialId)
  const current = p.items.find((i) => i.id === id)
  // Remember the occurrence the form started on: if a refetch drops it (it was
  // paid elsewhere) the form says so and refuses to save, instead of silently
  // switching to another commitment.
  const [known, setKnown] = useState(current ?? p.items.find((i) => i.id === p.initialId) ?? p.initialItem)
  if (current && current !== known) setKnown(current)
  const item = (current ?? known)!
  const gone = !current
  // The suggested amount is the outstanding amount as the server computed it.
  const [amount, setAmount] = useState(item.payment.outstanding_amount.amount)
  const [paidOn, setPaidOn] = useState(p.asOf)
  const heading = useRef<HTMLHeadingElement>(null)
  const uid = useId()
  // Under the page title when opened from the header (h2); inside the occurrences section when opened from a row (h3).
  const Heading = p.chooser ? 'h2' : 'h3'
  useEffect(() => heading.current?.focus(), [])

  // Not optimistic: no `apply`, so nothing is shown as recorded until the server has confirmed it (phase "pending" = "Not saved yet").
  const save = useOptimisticSave<{ amount: string; paid_on: string }, ManualPayment>({
    queryKey: monthKey(p.month),
    mutationFn: (v) =>
      gone ? Promise.reject(new Error('This occurrence is no longer unpaid.')) : recordPayment(item.id, { amount: v.amount, currency: item.amount.currency, paid_on: v.paid_on }),
    onSaved: (saved) => p.onSaved(saved, item),
    messages: { saved: (s) => `Payment recorded: ${s.amount.amount} ${s.amount.currency}. Undo is available above the list.`, failed: () => '' },
  })
  const pending = save.phase === 'pending'
  const err = save.error instanceof ApiError ? save.error : null
  const fieldError = (f: string) => (err?.field === f || err?.field === `${f}.amount` ? err.message : undefined)

  return (
    <form
      aria-labelledby={`${uid}-h`}
      className="space-y-3 rounded-md border border-border-control bg-surface p-4"
      onSubmit={(e) => {
        e.preventDefault()
        save.save({ amount: amount.trim(), paid_on: paidOn })
      }}
    >
      <Heading id={`${uid}-h`} ref={heading} tabIndex={-1} className="font-semibold">
        Record a payment
      </Heading>
      <p className="text-sm text-muted-foreground">
        This records a payment you made. It is saved only when the server confirms it, and you can undo it afterwards.
      </p>
      <p data-testid="panel-occurrence" className="flex flex-wrap items-center gap-2 text-sm">
        <span className="font-medium break-words">{item.commitment_name || item.commitment_id}</span>
        <span>
          due <time dateTime={item.due_date}>{item.due_date}</time>, {item.amount.amount} {item.amount.currency}
        </span>
        <StatusBadge status={item.payment.status} />
      </p>
      <div className="grid gap-3 sm:grid-cols-3">
        {p.chooser && <label className="grid gap-1 text-sm sm:col-span-3">
          Occurrence
          <select
            className="h-9 rounded-md border border-border-control bg-surface px-2 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            value={item.id}
            disabled={pending}
            onChange={(e) => {
              setId(e.target.value)
              const next = p.items.find((i) => i.id === e.target.value)
              if (next) setAmount(next.payment.outstanding_amount.amount)
            }}
          >
            {gone && (
              <option value={item.id} disabled>
                {item.commitment_name || item.commitment_id}, due {item.due_date} (no longer unpaid)
              </option>
            )}
            {p.items.map((i) => (
              <option key={i.id} value={i.id}>
                {i.commitment_name || i.commitment_id}, due {i.due_date} ({i.amount.amount} {i.amount.currency})
              </option>
            ))}
          </select>
        </label>}
        <label className="grid gap-1 text-sm">
          Amount paid ({item.amount.currency})
          <input
            inputMode="decimal"
            autoComplete="off"
            className="h-9 rounded-md border border-border-control bg-surface px-2 font-mono focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            value={amount}
            disabled={pending}
            onChange={(e) => setAmount(e.target.value)}
            aria-invalid={fieldError('amount') ? true : undefined}
            aria-describedby={fieldError('amount') ? `${uid}-amount-err` : undefined}
          />
          {fieldError('amount') && <span id={`${uid}-amount-err`}>{fieldError('amount')}</span>}
        </label>
        <label className="grid gap-1 text-sm">
          Paid on
          <input
            type="date"
            className="h-9 rounded-md border border-border-control bg-surface px-2 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            value={paidOn}
            disabled={pending}
            onChange={(e) => setPaidOn(e.target.value)}
            aria-invalid={err?.field === 'paid_on' ? true : undefined}
            aria-describedby={err?.field === 'paid_on' ? `${uid}-paid-on-err` : undefined}
          />
          {err?.field === 'paid_on' && <span id={`${uid}-paid-on-err`}>{err.message}</span>}
        </label>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button type="submit" disabled={pending || gone}>
          {pending ? 'Saving…' : 'Save payment'}
        </Button>
        <Button type="button" variant="ghost" onClick={p.onClose} disabled={pending}>
          Cancel
        </Button>
        {pending && (
          <span role="status">
            <StatusBadge status="pending" label="Not saved yet" />
          </span>
        )}
      </div>
      {gone && (
        <div role="alert" className="space-y-1">
          <StatusBadge status="error" label="Occurrence changed" />
          <p>
            {item.commitment_name || item.commitment_id}, due {item.due_date}, is no longer unpaid (it may have been paid elsewhere), so
            nothing can be saved for it. {p.chooser ? 'Pick another occurrence or cancel.' : 'Cancel, then record the payment from another row.'}
          </p>
        </div>
      )}
      {save.phase === 'failed' && (
        <div role="alert" className="space-y-1">
          <StatusBadge status="error" label="Payment not saved" />
          <p>{save.error?.message}</p>
          <p className="text-sm text-muted-foreground">Nothing was recorded. What you typed is still here; fix it or try again.</p>
        </div>
      )}
    </form>
  )
}

export interface UndoNoticeProps {
  payment: ManualPayment
  item: MonthItem
  month: string
  onDone: () => void
}

/** Confirms a saved payment and offers its undo; the undo is itself confirmed by the server. */
export function UndoNotice({ payment, item, month, onDone }: UndoNoticeProps) {
  const qc = useQueryClient()
  const box = useRef<HTMLDivElement>(null)
  const undo = useMutation({
    mutationFn: () => deletePayment(payment),
    // Undo acts on the month the payment was recorded in, not the one on screen.
    onSettled: () => qc.invalidateQueries({ queryKey: monthKey(month) }),
  })
  const name = item.commitment_name || item.commitment_id
  // Keyboard focus follows the change: to this notice when it appears and again
  // when its Undo button is replaced by the confirmation.
  useEffect(() => box.current?.focus(), [undo.isSuccess])
  return (
    <div ref={box} tabIndex={-1} role="status" className="space-y-1 rounded-md border border-border-control bg-surface p-3">
      {undo.isSuccess ? (
        <p>
          Payment removed. {name} is back as it was before; the {monthLabel(month)} view has been refreshed.{' '}
          <Button variant="ghost" size="sm" onClick={onDone}>
            Dismiss
          </Button>
        </p>
      ) : (
        <>
          <p>
            Saved: {payment.amount.amount} {payment.amount.currency} paid on {payment.paid_on} for {name}, due {item.due_date}, in {monthLabel(month)}.
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" size="sm" onClick={() => undo.mutate()} disabled={undo.isPending}>
              {undo.isPending ? 'Undoing…' : 'Undo this payment'}
            </Button>
            <Button variant="ghost" size="sm" onClick={onDone} disabled={undo.isPending}>
              Dismiss
            </Button>
            {undo.isPending && <StatusBadge status="pending" label="Undo not confirmed yet" />}
          </div>
        </>
      )}
      {undo.isError && (
        <p role="alert">
          <StatusBadge status="error" label="Undo failed" /> The payment is still recorded. {undo.error.message}
        </p>
      )}
    </div>
  )
}
