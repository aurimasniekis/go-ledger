import { useEffect, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import { usePagedList } from '@/lib/paged'
import type { MatchAction, MatchingRunResult, OccurrenceLite, PaymentMatch } from './types'

/** Every cached query of this screen starts with this key, so one invalidation reloads them all. */
export const matchesKey = ['payment-matches'] as const

const base = '/api/v1/payment-matches'

export interface ActionInput {
  action: MatchAction
  /** The match version the person saw: a stale one is refused with 409 version_conflict. */
  version: number
  reason?: string
  /** Only for correct. */
  amount?: string
  currency?: string
  allowOverpayment?: boolean
}

/** Runs one action on a match. The response is the saved match: nothing is saved until it arrives. */
export function matchAction(id: string, a: ActionInput): Promise<PaymentMatch> {
  const json: Record<string, unknown> = { version: a.version }
  if (a.reason) json.reason = a.reason
  if (a.action === 'correct') json.amount = { amount: a.amount, currency: a.currency }
  if (a.allowOverpayment && (a.action === 'correct' || a.action === 'confirm')) json.allow_overpayment = true
  return apiFetch<PaymentMatch>(`${base}/${encodeURIComponent(id)}/${a.action}`, {
    method: 'POST',
    headers: { 'If-Match': `"${a.version}"` },
    json,
  })
}

/** Asks the matching engine to run now (it also runs after every bank refresh). */
export function runMatching(): Promise<MatchingRunResult> {
  return apiFetch<MatchingRunResult>('/api/v1/payment-matching/runs', { method: 'POST', json: {} })
}

/**
 * The occurrences that have a suggestion waiting, for a pointer from the month view. It reads every page of the queue,
 * and is unknown (no pointer) until the last one has loaded. A failure just means no pointer.
 */
export function useWaitingSuggestions(): ReadonlySet<string> | undefined {
  const q = usePagedList<PaymentMatch>(matchesKey, base, { state: 'suggested' })
  const { hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage } = q
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage && !isFetchNextPageError) void fetchNextPage()
  }, [hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage])
  return useMemo(() => (q.data && !hasNextPage ? new Set(q.data.map((m) => m.occurrence_id)) : undefined), [q.data, hasNextPage])
}

export function useOccurrence(id: string) {
  return useQuery({ queryKey: ['occurrences', 'detail', id], queryFn: ({ signal }) => apiFetch<OccurrenceLite>(`/api/v1/occurrences/${encodeURIComponent(id)}`, { signal }) })
}

export function useCommitmentName(id: string | undefined) {
  return useQuery({
    queryKey: ['commitments', 'name', id],
    enabled: !!id,
    queryFn: async ({ signal }) => (await apiFetch<{ name: string }>(`/api/v1/commitments/${encodeURIComponent(id!)}`, { signal })).name,
  })
}
