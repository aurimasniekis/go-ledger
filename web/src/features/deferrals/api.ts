import { useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import type { AdjustmentKind, DeferralEvent, DeferralPlan, PlanType } from './types'

const base = '/api/v1/deferral-plans'
const ifMatch = (version: number) => ({ 'If-Match': `"${version}"` })

/** Every month view (a plan moves an amount between months) and the outlook read plans, so any plan write refreshes them all. */
export const PLAN_AFFECTED_KEYS = [['occurrences', 'summary'], ['outlook']] as const

export function useRefreshAfterPlanChange() {
  const qc = useQueryClient()
  return (planId?: string) =>
    Promise.all([
      ...PLAN_AFFECTED_KEYS.map((queryKey) => qc.invalidateQueries({ queryKey })),
      planId ? qc.invalidateQueries({ queryKey: historyKey(planId) }) : undefined,
    ])
}

export interface PlanFields {
  plan_type: PlanType
  planned_date: string
  /** Empty or null clears the reason. */
  reason: string | null
}

/** Plans a later payment date for an occurrence. Nothing is saved until the response arrives. */
export const createPlan = (occurrenceId: string, f: PlanFields) =>
  apiFetch<DeferralPlan>(base, { method: 'POST', json: { occurrence_id: occurrenceId, ...f } })

/** Revises an active plan (JSON Merge Patch); the version is the one the screen shows. */
export const revisePlan = (plan: Pick<DeferralPlan, 'id' | 'version'>, f: PlanFields) =>
  apiFetch<DeferralPlan>(`${base}/${encodeURIComponent(plan.id)}`, { method: 'PATCH', json: f, headers: ifMatch(plan.version) })

export const undoPlan = (plan: Pick<DeferralPlan, 'id' | 'version'>, reason: string | null) =>
  apiFetch<DeferralPlan>(`${base}/${encodeURIComponent(plan.id)}/undo`, { method: 'POST', json: reason ? { reason } : {}, headers: ifMatch(plan.version) })

export const getPlan = (id: string) => apiFetch<DeferralPlan>(`${base}/${encodeURIComponent(id)}`)

export const addAdjustment = (plan: Pick<DeferralPlan, 'id' | 'version'>, a: { kind: AdjustmentKind; amount: string; currency: string; note: string | null }) =>
  apiFetch<DeferralPlan>(`${base}/${encodeURIComponent(plan.id)}/adjustments`, {
    method: 'POST',
    json: { kind: a.kind, amount: { amount: a.amount, currency: a.currency }, ...(a.note ? { note: a.note } : {}) },
    headers: ifMatch(plan.version),
  })

const adjustmentPath = (planId: string, adjustmentId: string) => `${base}/${encodeURIComponent(planId)}/adjustments/${encodeURIComponent(adjustmentId)}`

export const removeAdjustment = (plan: Pick<DeferralPlan, 'id' | 'version'>, adjustmentId: string) =>
  apiFetch<DeferralPlan>(adjustmentPath(plan.id, adjustmentId), { method: 'DELETE', headers: ifMatch(plan.version) })

/** Marks a fee paid: the person's own record, matched to no bank transaction and never payment of the occurrence. */
export const markAdjustmentPaid = (plan: Pick<DeferralPlan, 'id' | 'version'>, adjustmentId: string) =>
  apiFetch<DeferralPlan>(adjustmentPath(plan.id, adjustmentId) + '/paid', { method: 'POST', headers: ifMatch(plan.version) })

export const markAdjustmentUnpaid = (plan: Pick<DeferralPlan, 'id' | 'version'>, adjustmentId: string) =>
  apiFetch<DeferralPlan>(adjustmentPath(plan.id, adjustmentId) + '/unpaid', { method: 'POST', headers: ifMatch(plan.version) })

export const historyKey = (planId: string) => ['deferral-plans', planId, 'history'] as const

/** A plan's history, oldest first, read only once the person opens it. */
export function usePlanHistory(planId: string, enabled: boolean) {
  return useQuery({
    queryKey: historyKey(planId),
    enabled,
    queryFn: async ({ signal }) => (await apiFetch<{ items: DeferralEvent[] }>(`${base}/${encodeURIComponent(planId)}/history?limit=200`, { signal })).items,
  })
}
