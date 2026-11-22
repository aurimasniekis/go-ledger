import { CalendarDays, Hourglass, ShieldCheck } from 'lucide-react'
import { StatusBadge } from '@/components/status-badge'
import { Amount, DateText } from '@/components/formatted'
import { monthLabel } from '@/features/month/month'
import { PLAN_TYPE_LABEL, monthOfDate } from './deferral'
import type { DeferralPlan } from './types'

/**
 * What an active plan says about one occurrence. Every fact is a labelled line with an icon, so the original due date,
 * the overdue status, the planned catch-up date and a documented grace period stay distinguishable without colour.
 * A plan changes when an amount is planned, not whether it is owed: the text says so.
 */
export function PlanSummary({ plan, ownMonth }: { plan: DeferralPlan; ownMonth: string }) {
  const moved = monthOfDate(plan.planned_date) !== ownMonth
  const feeOnly = plan.occurrence_status === 'paid'
  return (
    <div data-testid="plan-summary" className="space-y-1 rounded-md border border-border-control p-2 text-sm">
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-2 gap-y-0.5">
        <dt className="flex items-center gap-1 font-medium">
          <CalendarDays aria-hidden className="size-4" />
          Original due date
        </dt>
        <dd>
          <DateText value={plan.original_due_date} />
          {plan.overdue_since ? <>; overdue since this date</> : null}
        </dd>
        <dt className="flex items-center gap-1 font-medium">
          <Hourglass aria-hidden className="size-4" />
          Planned catch-up date
        </dt>
        <dd>
          <DateText value={plan.planned_date} /> ({monthLabel(plan.planned_month)}); {PLAN_TYPE_LABEL[plan.plan_type].toLowerCase()}
        </dd>
        {plan.grace_until && (
          <>
            <dt className="flex items-center gap-1 font-medium">
              <ShieldCheck aria-hidden className="size-4" />
              Documented grace
            </dt>
            <dd>
              {plan.within_grace ? 'Within documented grace until ' : 'Documented grace ended '}
              <DateText value={plan.grace_until} />
            </dd>
          </>
        )}
      </dl>
      {plan.missed && (
        <p>
          <StatusBadge status="plan_missed" label={plan.missed_reason === 'fee' ? 'Plan date passed, fee still planned' : 'Plan date passed, still owed'} />{' '}
          {plan.missed_reason === 'fee'
            ? 'The planned date has passed and a recorded fee is still planned.'
            : 'The planned date has passed and the amount is still owed. You can plan a new date.'}
        </p>
      )}
      {!feeOnly && (
        <p className="text-muted-foreground">
          {moved ? (
            <>
              Still owed in {monthLabel(ownMonth)} ({<Amount amount={plan.outstanding_amount.amount} currency={plan.outstanding_amount.currency} />}); planned in{' '}
              {monthLabel(plan.planned_month)}.{' '}
            </>
          ) : null}
          A plan is not a payment: it changes when the amount is planned, not whether it is owed.
        </p>
      )}
      {plan.reason && <p className="break-words">Reason: {plan.reason}</p>}
    </div>
  )
}
