import { Undo2 } from 'lucide-react'
import { useEffect, useRef } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/status-badge'
import { DateText } from '@/components/formatted'
import { monthLabel } from '@/features/month/month'
import { getPlan, undoPlan, useRefreshAfterPlanChange } from './api'
import { effectText, planFailure } from './deferral'
import type { PlanChange } from './plan-details'

/**
 * Confirms a saved plan change and, for a new or changed plan, offers its undo; the undo is itself confirmed by the
 * server. Like the payment notice it has no timeout: it stays until dismissed or replaced.
 */
export function PlanNotice({ change, onDone }: { change: PlanChange; onDone: () => void }) {
  const { kind, plan, item } = change
  const name = item.commitment_name || item.commitment_id
  const box = useRef<HTMLDivElement>(null)
  const refresh = useRefreshAfterPlanChange()
  // The plan may have changed since (a fee, a revision elsewhere): undo reads the version the server holds now.
  const undo = useMutation({
    mutationFn: async () => undoPlan(await getPlan(plan.id), null),
    onSettled: () => refresh(plan.id),
  })
  useEffect(() => box.current?.focus(), [undo.isSuccess])
  const undone = kind === 'undone' || undo.isSuccess
  return (
    <div ref={box} tabIndex={-1} role="status" data-testid="plan-notice" className="space-y-1 rounded-md border border-border-control bg-surface p-3">
      {undone ? (
        <p>
          <span className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-0.5 text-sm font-medium text-muted-foreground">
            <Undo2 aria-hidden className="size-4" />
            Plan undone
          </span>{' '}
          {name} is planned in {monthLabel(item.month)} again, on its original due date <DateText value={item.due_date} />. Nothing was marked paid. The plan's history is kept.{' '}
          <Button variant="ghost" size="sm" onClick={onDone}>
            Dismiss
          </Button>
        </p>
      ) : (
        <>
          <p>
            <StatusBadge status="deferred" label={kind === 'created' ? 'Plan saved' : 'Plan changed'} />{' '}
            {name}, originally due <DateText value={item.due_date} />, is planned for <DateText value={plan.planned_date} />. {effectText(item.month, plan.planned_date)} Nothing is marked
            paid.
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" size="sm" onClick={() => undo.mutate()} disabled={undo.isPending}>
              {undo.isPending ? 'Undoing…' : 'Undo this plan'}
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
          <StatusBadge status="save_failed" label="Undo failed" /> The plan is still in place. {planFailure(undo.error)}
        </p>
      )}
    </div>
  )
}
