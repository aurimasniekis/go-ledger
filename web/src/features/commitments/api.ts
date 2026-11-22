import { useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import type { Commitment } from './types'

const base = '/api/v1/commitments'

export const commitmentKey = (id: string) => ['commitments', 'detail', id] as const

export function useCommitment(id: string) {
  return useQuery({ queryKey: commitmentKey(id), queryFn: ({ signal }) => apiFetch<Commitment>(`${base}/${encodeURIComponent(id)}`, { signal }) })
}

export const createCommitment = (body: Record<string, unknown>) => apiFetch<Commitment>(base, { method: 'POST', json: body })

/** A merge patch (RFC 7396) with the version the form was loaded at; the server refuses it when the commitment changed since. */
export const patchCommitment = (id: string, version: number, body: Record<string, unknown>) =>
  apiFetch<Commitment>(`${base}/${encodeURIComponent(id)}`, { method: 'PATCH', json: { ...body, version } })

export const deleteCommitment = (id: string, version: number) =>
  apiFetch<void>(`${base}/${encodeURIComponent(id)}`, { method: 'DELETE', headers: { 'If-Match': `"${version}"` } })

/**
 * What a change to a commitment makes stale: the list, the commitment, and everything derived from its occurrences
 * (the monthly view, the outlook, matches, deferral plans). They are refetched, so the server stays the truth.
 */
export function useInvalidateCommitments() {
  const qc = useQueryClient()
  return () =>
    Promise.all(['commitments', 'occurrences', 'month', 'outlook', 'matches'].map((k) => qc.invalidateQueries({ queryKey: [k] })))
}
