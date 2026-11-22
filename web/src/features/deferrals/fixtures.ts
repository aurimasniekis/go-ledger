import type { DeferralAdjustment, DeferralPlan } from './types'

const eur = (amount: string) => ({ amount, currency: 'EUR' })

export function plan(over: Partial<DeferralPlan> = {}): DeferralPlan {
  return {
    id: 'p1', occurrence_id: 'a', commitment_id: 'c-a', plan_type: 'personal', state: 'active', planned_date: '2026-12-05', planned_month: '2026-12', reason: null,
    original_due_date: '2026-11-05', original_amount: eur('10.00'), outstanding_when_planned: eur('10.00'),
    occurrence_status: 'overdue', outstanding_amount: eur('10.00'), overdue_since: '2026-11-05', grace_until: null, within_grace: false,
    missed: false, missed_reason: null, adjustment_total: eur('0.00'), unpaid_adjustment_total: eur('0.00'), adjustments: [], version: 1,
    created_at: '2026-11-10T08:00:00Z', updated_at: '2026-11-10T08:00:00Z', undone_at: null, ...over,
  }
}

export function fee(over: Partial<DeferralAdjustment> = {}): DeferralAdjustment {
  return { id: 'f1', kind: 'fee', amount: eur('5.00'), note: null, created_at: '2026-11-11T08:00:00Z', removed_at: null, paid_at: null, paid_by: null, ...over }
}
