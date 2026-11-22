import { useEffect, useId, useRef, useState } from 'react'
import { useOptimisticSave } from '@/lib/optimistic-save'
import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/status-badge'
import { Amount, DateText } from '@/components/formatted'
import { ApiError } from '@/lib/api'
import { isIsoDate } from '@/lib/format'
import { monthLabel } from '@/features/month/month'
import type { MonthItem } from '@/features/month/types'
import { createPlan, revisePlan, useRefreshAfterPlanChange, type PlanFields } from './api'
import { PLAN_TYPE_HELP, PLAN_TYPE_LABEL, effectText, planFailure } from './deferral'
import type { DeferralPlan, PlanType } from './types'

const CONTROL = 'h-9 rounded-md border border-border-control bg-surface px-2 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring'
const REASON_MAX = 500

export interface PlanFormProps {
  item: MonthItem
  /** Set to revise this plan; absent to make a new one. Its version is the one on screen, read fresh on every render. */
  plan?: DeferralPlan
  offline?: boolean
  onClose: () => void
  onSaved: (plan: DeferralPlan, kind: 'created' | 'revised') => void
}

/**
 * Plan a later payment date, or change an existing plan. Nothing is shown as planned until the server has answered:
 * while the request is out the form says "Not saved yet", and a failure says nothing was saved and keeps what was typed.
 */
export function PlanForm({ item, plan, offline, onClose, onSaved }: PlanFormProps) {
  const uid = useId()
  const [type, setType] = useState<PlanType>(plan?.plan_type ?? 'personal')
  const [date, setDate] = useState(plan?.planned_date ?? '')
  const [reason, setReason] = useState(plan?.reason ?? '')
  const [dateError, setDateError] = useState<string | null>(null)
  const refresh = useRefreshAfterPlanChange()
  const heading = useRef<HTMLHeadingElement>(null)
  useEffect(() => heading.current?.focus(), [])
  const name = item.commitment_name || item.commitment_id

  const save = useOptimisticSave<PlanFields, DeferralPlan>({
    queryKey: ['occurrences', 'summary'],
    mutationFn: (f) => (plan ? revisePlan(plan, f) : createPlan(item.id, f)),
    onSaved: async (saved) => {
      await refresh(saved.id)
      onSaved(saved, plan ? 'revised' : 'created')
    },
    messages: { saved: (s) => `Plan saved: ${name} is planned for ${s.planned_date}. Nothing is marked paid.`, failed: () => '' },
  })
  const pending = save.phase === 'pending'
  const err = save.error instanceof ApiError ? save.error : null
  const fieldError = (f: string) => (err?.field === f ? err.message : undefined)
  const gone = !plan && item.payment.status === 'paid'
  const trimmedDate = date.trim()
  const preview = isIsoDate(trimmedDate) && trimmedDate > item.due_date ? effectText(item.month, trimmedDate) : null

  return (
    <form
      noValidate
      aria-labelledby={`${uid}-h`}
      className="space-y-3 rounded-md border border-border-control bg-surface p-4"
      onSubmit={(e) => {
        e.preventDefault()
        // Checked here, before anything is sent: a malformed or impossible date is never "saved".
        if (!isIsoDate(trimmedDate)) return setDateError('Enter the date as YYYY-MM-DD, for example 2026-12-05.')
        if (trimmedDate <= item.due_date) return setDateError(`The planned date must be after the original due date, ${item.due_date}.`)
        if (reason.length > REASON_MAX) return
        setDateError(null)
        save.save({ plan_type: type, planned_date: trimmedDate, reason: reason.trim() || null })
      }}
    >
      <h3 id={`${uid}-h`} className="font-semibold" tabIndex={-1} ref={heading}>
        {plan ? 'Change the plan' : 'Plan a later payment date'}
      </h3>
      <p className="text-sm text-muted-foreground">
        This records when you plan to pay. It does not mark anything paid, and the original due date and overdue status stay as they are. It is saved only
        when the server confirms it, and you can undo it afterwards.
      </p>
      <p className="flex flex-wrap items-center gap-2 text-sm" data-testid="plan-occurrence">
        <span className="font-medium break-words">{name}</span>
        <span>
          originally due <DateText value={item.due_date} />, still owed <Amount amount={item.payment.outstanding_amount.amount} currency={item.payment.outstanding_amount.currency} />
        </span>
      </p>
      <fieldset className="space-y-1" disabled={pending}>
        <legend className="text-sm font-medium">Kind of plan</legend>
        {(Object.keys(PLAN_TYPE_LABEL) as PlanType[]).map((t) => (
          <label key={t} className="flex items-start gap-2 text-sm">
            <input type="radio" name={`${uid}-type`} value={t} checked={type === t} onChange={() => setType(t)} className="mt-1" />
            <span>
              {PLAN_TYPE_LABEL[t]}
              <span className="block text-xs text-muted-foreground">{PLAN_TYPE_HELP[t]}</span>
            </span>
          </label>
        ))}
      </fieldset>
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="grid content-start gap-1 text-sm">
          Planned date (YYYY-MM-DD)
          <input
            type="text"
            inputMode="numeric"
            autoComplete="off"
            placeholder="YYYY-MM-DD"
            maxLength={10}
            className={CONTROL + ' font-mono'}
            value={date}
            disabled={pending}
            onChange={(e) => {
              setDate(e.target.value)
              setDateError(null)
            }}
            aria-invalid={dateError || fieldError('planned_date') ? true : undefined}
            aria-describedby={dateError || fieldError('planned_date') ? `${uid}-date-err` : undefined}
          />
          {(dateError ?? fieldError('planned_date')) && (
            <span id={`${uid}-date-err`} className="font-medium">
              {dateError ?? fieldError('planned_date')}
            </span>
          )}
        </label>
        <label className="grid content-start gap-1 text-sm">
          Reason (optional)
          <textarea
            rows={2}
            maxLength={REASON_MAX}
            className="rounded-md border border-border-control bg-surface px-2 py-1 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            value={reason}
            disabled={pending}
            onChange={(e) => setReason(e.target.value)}
            aria-describedby={`${uid}-reason-hint`}
          />
          <span id={`${uid}-reason-hint`} className="text-xs text-muted-foreground">
            Kept with the plan's history. Up to {REASON_MAX} characters.
          </span>
        </label>
      </div>
      {preview && (
        <p className="text-sm" data-testid="plan-effect">
          <span className="font-medium">Effect on the months:</span> {preview}
        </p>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <Button type="submit" disabled={pending || gone || offline}>
          {pending ? 'Saving…' : plan ? 'Save changes' : 'Save plan'}
        </Button>
        <Button type="button" variant="ghost" onClick={onClose} disabled={pending}>
          Cancel
        </Button>
        {pending && (
          <span role="status">
            <StatusBadge status="pending" label="Not saved yet" />
          </span>
        )}
      </div>
      {offline && <p className="text-sm text-muted-foreground">You are offline, so a plan cannot be saved. It comes back with the connection.</p>}
      {gone && (
        <div role="alert">
          <StatusBadge status="error" label="Occurrence changed" /> This occurrence is no longer unpaid, so there is nothing to plan for {monthLabel(item.month)}.
        </div>
      )}
      {save.phase === 'failed' && (
        <div role="alert" className="space-y-1">
          <StatusBadge status="save_failed" label="Plan not saved" />
          <p>{planFailure(save.error)}</p>
          <p className="text-sm text-muted-foreground">Nothing was changed. What you typed is still here; fix it or try again.</p>
        </div>
      )}
    </form>
  )
}
