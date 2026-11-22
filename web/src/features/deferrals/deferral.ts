import { ApiError } from '@/lib/api'
import { monthLabel } from '@/features/month/month'
import type { AdjustmentKind, DeferralAdjustment, DeferralPlan, HistoryAction, PlanType } from './types'

// Wording for plans. A plan records when the person intends to pay: it is not payment, not forgiveness and not an
// agreement unless the person says the creditor agreed. The words stay neutral (no "late", "failed", "behind").

export const PLAN_TYPE_LABEL: Record<PlanType, string> = {
  personal: 'My own plan',
  creditor_agreed: 'Agreed with the creditor',
  documented_grace: 'Documented grace period',
}

export const PLAN_TYPE_HELP: Record<PlanType, string> = {
  personal: 'You decided on a later date yourself. Nobody else has agreed to it.',
  creditor_agreed: 'The creditor agreed to the later date. Ledger does not check this; it records what you tell it.',
  documented_grace: 'A grace period is written down somewhere (a contract or a letter). The planned date is where it ends.',
}

export const ADJUSTMENT_KIND_LABEL: Record<AdjustmentKind, string> = { fee: 'Fee', interest: 'Interest', other: 'Other charge' }

export const HISTORY_ACTION_LABEL: Record<HistoryAction, string> = {
  created: 'Plan made',
  revised: 'Plan changed',
  undone: 'Plan undone',
  adjustment_added: 'Fee recorded',
  adjustment_removed: 'Fee removed',
  adjustment_paid: 'Fee marked paid',
  adjustment_unpaid: 'Fee marked unpaid',
}

/** A fee that still counts as planned: neither removed nor marked paid. */
export const isFeePlanned = (a: DeferralAdjustment) => !a.removed_at && !a.paid_at

export const hasPlannedFee = (p: Pick<DeferralPlan, 'adjustments'>) => p.adjustments.some(isFeePlanned)

/** The month (YYYY-MM) of a calendar date. */
export const monthOfDate = (date: string) => date.slice(0, 7)

/**
 * What a planned date does to the months, in words. Both months are named; nothing is said to be paid and the amount
 * stays owed in its own month.
 */
export function effectText(ownMonth: string, plannedDate: string): string {
  const target = monthOfDate(plannedDate)
  if (target === ownMonth) {
    return `It stays planned in ${monthLabel(ownMonth)}, where it falls due. Only the planned date is new.`
  }
  return (
    `In ${monthLabel(ownMonth)} it stays owed and keeps its original due date and overdue status, but it is no longer planned there. ` +
    `In ${monthLabel(target)} it is planned, and listed under carried in from earlier months.`
  )
}

/** The refusals people can act on, in plain words; anything else keeps the server's message. */
export function planFailure(e: unknown): string {
  if (!(e instanceof ApiError)) return e instanceof Error ? e.message : 'The request failed.'
  switch (e.code) {
    case 'deferral_adjustments_unpaid':
      return 'A fee on this plan is still planned. Mark it paid or remove it first, then undo the plan.'
    case 'version_conflict':
      return 'This plan changed elsewhere. The latest version has been loaded; review it and try again.'
    case 'deferral_plan_exists':
      return 'This occurrence already has a plan. The latest version has been loaded; change or undo that one instead.'
    case 'deferral_plan_undone':
      return 'This plan was already undone, so it can no longer be changed.'
    case 'nothing_outstanding':
      return 'Nothing is owed on this occurrence any more (it may have been paid), so there is nothing to plan.'
    case 'occurrence_cancelled':
      return 'This occurrence was cancelled, so it cannot be planned.'
    case 'deferral_adjustment_removed':
      return 'This fee was already removed.'
    case 'deferral_adjustment_paid':
      return 'This fee is already marked paid.'
    case 'deferral_adjustment_not_paid':
      return 'This fee is not marked paid.'
    default:
      return e.message
  }
}
