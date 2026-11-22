import { CircleCheck, Clock, History as HistoryIcon, Trash2 } from 'lucide-react'
import { useId, useRef, useState, useEffect } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useAnnounce } from '@/components/announcer'
import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/status-badge'
import { Amount, DateText } from '@/components/formatted'
import { formatInstantUtc } from '@/lib/format'
import { monthLabel } from '@/features/month/month'
import type { MonthItem } from '@/features/month/types'
import { PlanForm } from './plan-form'
import {
  addAdjustment,
  markAdjustmentPaid,
  markAdjustmentUnpaid,
  removeAdjustment,
  undoPlan,
  usePlanHistory,
  useRefreshAfterPlanChange,
} from './api'
import { ADJUSTMENT_KIND_LABEL, HISTORY_ACTION_LABEL, PLAN_TYPE_LABEL, hasPlannedFee, planFailure } from './deferral'
import type { AdjustmentKind, DeferralAdjustment, DeferralPlan } from './types'

const CONTROL = 'h-9 rounded-md border border-border-control bg-surface px-2 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring'

export type PlanChange = { kind: 'created' | 'revised' | 'undone'; plan: DeferralPlan; item: MonthItem }

export interface PlanDetailsProps {
  item: MonthItem
  plan: DeferralPlan
  offline?: boolean
  onClose: () => void
  onChanged: (c: PlanChange) => void
}

/**
 * The plan of one occurrence: what it says, its fees with their own paid marker, its history, and the ways to change or
 * undo it. Every write waits for the server ("Not saved yet") and reports a refusal in words; the plan shown is always
 * the one the server last sent, so the version used for a write is never an old one.
 */
export function PlanDetails({ item, plan, offline, onClose, onChanged }: PlanDetailsProps) {
  const uid = useId()
  const [view, setView] = useState<'details' | 'revise' | 'undo'>('details')
  const heading = useRef<HTMLHeadingElement>(null)
  useEffect(() => {
    if (view === 'details') heading.current?.focus()
  }, [view])
  const announce = useAnnounce()
  const refresh = useRefreshAfterPlanChange()
  // One write at a time, for the fee actions: each says what it is doing while it waits.
  const act = useMutation({
    mutationFn: ({ run }: { label: string; run: () => Promise<DeferralPlan>; onDone?: () => void; onFailed?: () => void }) => run(),
    onSettled: () => refresh(plan.id),
    onSuccess: (_p, v) => announce(`${v.label}: saved.`),
  })
  const busy = act.isPending
  const planned = hasPlannedFee(plan)
  const name = item.commitment_name || item.commitment_id

  if (view === 'revise') {
    return (
      <PlanForm
        item={item}
        plan={plan}
        offline={offline}
        onClose={() => setView('details')}
        onSaved={(saved, kind) => onChanged({ kind, plan: saved, item })}
      />
    )
  }

  return (
    <section aria-labelledby={`${uid}-h`} className="space-y-3 rounded-md border border-border-control bg-surface p-4">
      <h3 id={`${uid}-h`} ref={heading} tabIndex={-1} className="font-semibold">
        Plan for {name}
      </h3>

      <Fees plan={plan} offline={offline} busy={busy} run={(label, run, cb) => act.mutate({ label, run }, { onSuccess: cb?.onDone, onError: cb?.onFailed })} />

      {act.isPending && (
        <p role="status">
          <StatusBadge status="pending" label="Not saved yet" /> {act.variables?.label}…
        </p>
      )}
      {act.isError && (
        <div role="alert" className="space-y-1">
          <StatusBadge status="save_failed" label="Not saved" />
          <p>{planFailure(act.error)}</p>
          <p className="text-sm text-muted-foreground">Nothing was changed.</p>
        </div>
      )}

      {view === 'undo' ? (
        <UndoForm
          plan={plan}
          item={item}
          offline={offline}
          onCancel={() => setView('details')}
          onUndone={(saved) => onChanged({ kind: 'undone', plan: saved, item })}
        />
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => setView('revise')} disabled={busy || offline}>
            Change plan
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => setView('undo')}
            disabled={busy || offline || planned}
            aria-describedby={planned ? `${uid}-undo-why` : undefined}
          >
            Undo plan
          </Button>
          <Button variant="ghost" size="sm" onClick={onClose} disabled={busy}>
            Close
          </Button>
        </div>
      )}
      {planned && view !== 'undo' && (
        <p id={`${uid}-undo-why`} className="text-sm" data-testid="undo-blocked">
          Undo is not available while a fee on this plan is still planned. Mark the fee paid or remove it first, then undo the plan.
        </p>
      )}
      {offline && <p className="text-sm text-muted-foreground">You are offline, so the plan cannot be changed. It comes back with the connection.</p>}
      <History planId={plan.id} />
    </section>
  )
}

function Fees({ plan, offline, busy, run }: { plan: DeferralPlan; offline?: boolean; busy: boolean; run: (label: string, fn: () => Promise<DeferralPlan>, cb?: { onDone?: () => void; onFailed?: () => void }) => void }) {
  const uid = useId()
  const [adding, setAdding] = useState(false)
  // Counts failed saves of the fee form, so it can move focus back to its input.
  const [failures, setFailures] = useState(0)
  return (
    <div className="space-y-2" data-testid="plan-fees">
      <h4 className="text-sm font-semibold">Fees and interest on this plan</h4>
      <p className="text-sm text-muted-foreground">
        Ledger never adds a fee by itself. A fee you record counts as planned until you mark it paid or remove it. Marking it paid is your own record: it is matched
        to no bank transaction and does not pay the occurrence.
      </p>
      {plan.adjustments.length === 0 ? (
        <p className="text-sm">No fee or interest is recorded.</p>
      ) : (
        <ul className="space-y-1">
          {plan.adjustments.map((a) => (
            <Fee key={a.id} a={a} offline={offline} busy={busy} run={run} plan={plan} />
          ))}
        </ul>
      )}
      {!isZero(plan.unpaid_adjustment_total.amount) && (
        <p className="text-sm">
          Still planned: <Amount amount={plan.unpaid_adjustment_total.amount} currency={plan.unpaid_adjustment_total.currency} />
        </p>
      )}
      {adding ? (
        <FeeForm
          idPrefix={uid}
          currency={plan.original_amount.currency}
          busy={busy}
          failures={failures}
          onCancel={() => setAdding(false)}
          // The form stays open, with what was typed, until the server has saved the fee.
          onAdd={(v) =>
            run('Recording the fee', () => addAdjustment(plan, { ...v, currency: plan.original_amount.currency }), {
              onDone: () => setAdding(false),
              onFailed: () => setFailures((n) => n + 1),
            })
          }
        />
      ) : (
        <Button variant="outline" size="sm" onClick={() => setAdding(true)} disabled={busy || offline}>
          Record a fee or interest
        </Button>
      )}
    </div>
  )
}

const isZero = (a: string) => /^0+(\.0*)?$/.test(a)

function Fee({ a, plan, offline, busy, run }: { a: DeferralAdjustment; plan: DeferralPlan; offline?: boolean; busy: boolean; run: (label: string, fn: () => Promise<DeferralPlan>) => void }) {
  const state = a.removed_at ? 'removed' : a.paid_at ? 'paid' : 'planned'
  return (
    <li data-testid={'fee-' + state} className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-border p-2 text-sm">
      <span className={state === 'removed' ? 'line-through' : undefined}>
        {ADJUSTMENT_KIND_LABEL[a.kind]} <Amount amount={a.amount.amount} currency={a.amount.currency} />
        {a.note ? <span className="break-words">, {a.note}</span> : null}
      </span>
      {state === 'planned' && (
        <span className="inline-flex items-center gap-1 font-medium">
          <Clock aria-hidden className="size-4" />
          Planned, not paid
        </span>
      )}
      {state === 'paid' && a.paid_at && (
        <span className="inline-flex items-center gap-1 font-medium">
          <CircleCheck aria-hidden className="size-4" />
          Marked paid <time dateTime={a.paid_at}>{formatInstantUtc(a.paid_at)}</time>
        </span>
      )}
      {state === 'removed' && a.removed_at && (
        <span className="inline-flex items-center gap-1 font-medium">
          <Trash2 aria-hidden className="size-4" />
          Removed <time dateTime={a.removed_at}>{formatInstantUtc(a.removed_at)}</time>; kept as history, not counted
        </span>
      )}
      {state !== 'removed' && (
        <span className="ml-auto flex flex-wrap gap-2">
          {state === 'planned' ? (
            <Button size="sm" variant="outline" disabled={busy || offline} onClick={() => run('Marking the fee paid', () => markAdjustmentPaid(plan, a.id))}>
              Mark paid
            </Button>
          ) : (
            <Button size="sm" variant="outline" disabled={busy || offline} onClick={() => run('Marking the fee unpaid', () => markAdjustmentUnpaid(plan, a.id))}>
              Mark unpaid
            </Button>
          )}
          <Button size="sm" variant="ghost" disabled={busy || offline} onClick={() => run('Removing the fee', () => removeAdjustment(plan, a.id))}>
            Remove
          </Button>
        </span>
      )}
    </li>
  )
}

function FeeForm({ idPrefix, currency, busy, failures, onAdd, onCancel }: { idPrefix: string; currency: string; busy: boolean; failures: number; onAdd: (v: { kind: AdjustmentKind; amount: string; note: string | null }) => void; onCancel: () => void }) {
  const [kind, setKind] = useState<AdjustmentKind>('fee')
  const [amount, setAmount] = useState('')
  const [note, setNote] = useState('')
  const [error, setError] = useState<string | null>(null)
  const amountInput = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (failures > 0) amountInput.current?.focus()
  }, [failures])
  return (
    <div role="group" aria-label="Record a fee or interest" className="grid gap-2 rounded-md border border-border p-2 sm:grid-cols-3">
      <label className="grid gap-1 text-sm">
        Kind
        <select className={CONTROL} value={kind} onChange={(e) => setKind(e.target.value as AdjustmentKind)}>
          {(Object.keys(ADJUSTMENT_KIND_LABEL) as AdjustmentKind[]).map((k) => (
            <option key={k} value={k}>
              {ADJUSTMENT_KIND_LABEL[k]}
            </option>
          ))}
        </select>
      </label>
      <label className="grid gap-1 text-sm">
        Amount ({currency})
        <input
          ref={amountInput}
          inputMode="decimal"
          autoComplete="off"
          className={CONTROL + ' font-mono'}
          value={amount}
          onChange={(e) => {
            setAmount(e.target.value)
            setError(null)
          }}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? idPrefix + '-fee-err' : undefined}
        />
        {error && (
          <span id={idPrefix + '-fee-err'} className="font-medium">
            {error}
          </span>
        )}
      </label>
      <label className="grid gap-1 text-sm">
        Note (optional)
        <input className={CONTROL} value={note} maxLength={200} onChange={(e) => setNote(e.target.value)} />
      </label>
      <div className="flex flex-wrap gap-2 sm:col-span-3">
        <Button
          size="sm"
          disabled={busy}
          onClick={() => {
            if (!/^\d+(\.\d+)?$/.test(amount.trim()) || isZero(amount.trim())) return setError('Enter a positive amount such as 5.00.')
            onAdd({ kind, amount: amount.trim(), note: note.trim() || null })
          }}
        >
          Save fee
        </Button>
        <Button size="sm" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </div>
  )
}

function UndoForm({ plan, item, offline, onCancel, onUndone }: { plan: DeferralPlan; item: MonthItem; offline?: boolean; onCancel: () => void; onUndone: (p: DeferralPlan) => void }) {
  const [reason, setReason] = useState('')
  const refresh = useRefreshAfterPlanChange()
  const m = useMutation({
    mutationFn: () => undoPlan(plan, reason.trim() || null),
    onSuccess: async (saved) => {
      await refresh(saved.id)
      onUndone(saved)
    },
    onError: () => void refresh(plan.id),
  })
  return (
    <div className="space-y-2 rounded-md border border-border p-2" role="group" aria-label="Undo this plan">
      <p className="text-sm">
        Undo this plan? {item.commitment_name || item.commitment_id} is planned in {monthLabel(item.month)} again, on its original due date,{' '}
        <DateText value={item.due_date} />. Nothing is marked paid and the history of the plan is kept. You can plan a later date again afterwards.
      </p>
      <label className="grid gap-1 text-sm">
        Reason (optional)
        <textarea
          rows={2}
          maxLength={500}
          value={reason}
          disabled={m.isPending}
          onChange={(e) => setReason(e.target.value)}
          className="rounded-md border border-border-control bg-surface px-2 py-1 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
        />
      </label>
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" onClick={() => m.mutate()} disabled={m.isPending || offline}>
          {m.isPending ? 'Undoing…' : 'Undo plan'}
        </Button>
        <Button size="sm" variant="ghost" onClick={onCancel} disabled={m.isPending}>
          Keep the plan
        </Button>
        {m.isPending && (
          <span role="status">
            <StatusBadge status="pending" label="Undo not confirmed yet" />
          </span>
        )}
      </div>
      {m.isError && (
        <div role="alert" className="space-y-1">
          <StatusBadge status="save_failed" label="Undo failed" />
          <p>{planFailure(m.error)}</p>
          <p className="text-sm text-muted-foreground">The plan is still in place.</p>
        </div>
      )}
    </div>
  )
}

function History({ planId }: { planId: string }) {
  const [open, setOpen] = useState(false)
  const q = usePlanHistory(planId, open)
  const uid = useId()
  return (
    <div className="space-y-1">
      <Button variant="ghost" size="sm" aria-expanded={open} aria-controls={uid} onClick={() => setOpen((o) => !o)}>
        <HistoryIcon aria-hidden className="size-4" />
        {open ? 'Hide history' : 'Show history'}
      </Button>
      <div id={uid}>
        {open && q.isPending && <p role="status">Loading the history…</p>}
        {open && q.isError && <p role="alert">The history could not be loaded: {q.error.message}</p>}
        {open && q.data && (
          <ol aria-label="Plan history" className="space-y-1 text-sm">
            {q.data.map((e) => (
              <li key={e.plan_version} className="rounded-md border border-border p-2">
                <span className="font-medium">{HISTORY_ACTION_LABEL[e.action] ?? e.action}</span>{' '}
                <time dateTime={e.at} className="whitespace-nowrap">
                  {formatInstantUtc(e.at)}
                </time>
                <span className="block text-muted-foreground">
                  {PLAN_TYPE_LABEL[e.plan_type]}, planned <DateText value={e.planned_date} />
                  {e.reason ? `; reason: ${e.reason}` : ''}
                </span>
              </li>
            ))}
          </ol>
        )}
      </div>
    </div>
  )
}
