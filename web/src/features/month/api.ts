import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import type { ManualPayment, MonthSummary } from './types'

export const monthKey = (month: string) => ['occurrences', 'summary', month] as const

/**
 * One month of the monthly view. category_id and label_id restrict it to the commitments in them: the rows and the totals
 * then cover only those (the response's filter member says so).
 */
export function useMonthSummary(month: string, filter: { category_id?: string; label_id?: string } = {}) {
  return useQuery({
    // The unfiltered month keeps the plain key (what a payment's Undo invalidates by prefix); a filter adds to it.
    queryKey: filter.category_id || filter.label_id ? [...monthKey(month), filter] : monthKey(month),
    queryFn: ({ signal }) => {
      const q = new URLSearchParams({ month })
      if (filter.category_id) q.set('category_id', filter.category_id)
      if (filter.label_id) q.set('label_id', filter.label_id)
      return apiFetch<MonthSummary>(`/api/v1/occurrences/summary?${q.toString()}`, { signal })
    },
  })
}

export const manualPaymentsKey = (occurrenceId: string) => ['occurrences', 'manual-payments', occurrenceId] as const

/** An occurrence's manual payments, replaced ones included (they carry replaced_by_match_id). One page of up to 200 is all a record list needs. */
export function useManualPayments(occurrenceId: string, enabled = true) {
  return useQuery({
    queryKey: manualPaymentsKey(occurrenceId),
    enabled,
    queryFn: async ({ signal }) =>
      (await apiFetch<{ items: ManualPayment[] }>(`/api/v1/occurrences/${encodeURIComponent(occurrenceId)}/manual-payments?limit=200`, { signal })).items,
  })
}

/** Records a manual payment (C3). The response is the saved record: nothing is saved until it arrives. */
export function recordPayment(occurrenceId: string, body: { amount: string; currency: string; paid_on: string }): Promise<ManualPayment> {
  return apiFetch<ManualPayment>(`/api/v1/occurrences/${encodeURIComponent(occurrenceId)}/manual-payments`, {
    method: 'POST',
    json: { amount: { amount: body.amount, currency: body.currency }, paid_on: body.paid_on },
  })
}

/** Deletes a manual payment record: the undo of recordPayment. */
export function deletePayment(p: Pick<ManualPayment, 'id' | 'occurrence_id' | 'version'>): Promise<void> {
  return apiFetch<void>(`/api/v1/occurrences/${encodeURIComponent(p.occurrence_id)}/manual-payments/${encodeURIComponent(p.id)}`, {
    method: 'DELETE',
    headers: { 'If-Match': `"${p.version}"` },
  })
}
