// Shapes of the deferral plan API (OpenAPI DeferralPlan, ADR-0050). Amounts are decimal strings with their currency: the
// UI displays them and never does arithmetic on them (ADR-0005). A plan is not payment: nothing here marks anything paid.

import type { Money, PaymentStatus } from '@/features/month/types'

export type PlanType = 'personal' | 'creditor_agreed' | 'documented_grace'
export type AdjustmentKind = 'fee' | 'interest' | 'other'
export type MissedReason = 'principal' | 'fee'

export interface DeferralAdjustment {
  id: string
  kind: AdjustmentKind
  amount: Money
  note: string | null
  created_at: string
  /** Set once removed: kept as history, no longer counted. */
  removed_at: string | null
  /** When the person marked the fee paid; null while it is still planned. Their own statement, not bank evidence. */
  paid_at: string | null
  paid_by: { kind: string; user_id?: string | null; via?: string | null } | null
}

export interface DeferralPlan {
  id: string
  occurrence_id: string
  commitment_id: string
  plan_type: PlanType
  state: 'active' | 'undone'
  planned_date: string
  planned_month: string
  reason: string | null
  /** History fixed when the plan was made. */
  original_due_date: string
  original_amount: Money
  outstanding_when_planned: Money
  /** The occurrence's live state. */
  occurrence_status: PaymentStatus
  outstanding_amount: Money
  overdue_since: string | null
  grace_until: string | null
  within_grace: boolean
  /** The planned date has passed and something planned is still unpaid (the principal or a fee). */
  missed: boolean
  missed_reason: MissedReason | null
  adjustment_total: Money
  unpaid_adjustment_total: Money
  adjustments: DeferralAdjustment[]
  version: number
  created_at: string
  updated_at: string
  undone_at: string | null
}

export type HistoryAction = 'created' | 'revised' | 'undone' | 'adjustment_added' | 'adjustment_removed' | 'adjustment_paid' | 'adjustment_unpaid'

export interface DeferralEvent {
  action: HistoryAction
  plan_version: number
  plan_type: PlanType
  planned_date: string
  reason: string | null
  adjustment_id: string | null
  by: { kind: string; user_id?: string | null; via?: string | null }
  at: string
}
