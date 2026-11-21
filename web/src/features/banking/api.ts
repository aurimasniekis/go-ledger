import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import { usePagedList } from '@/lib/paged'
import type { BankApplication, BankConnection, BankInstitution, ConsentStart } from './types'

export const REFRESH_POLL_MS = 3000
export const connectionsKey = ['bank-connections'] as const
const base = '/api/v1/bank-connections'

export function useConnections() {
  // While a refresh is still running in the background (D40.4) the list is polled until it ends.
  return usePagedList<BankConnection>(connectionsKey, base, {}, { refetchInterval: (pages) => (pages.some((p) => p.items.some((c) => c.refresh_running)) ? REFRESH_POLL_MS : false) })
}

export function useApplications(enabled = true) {
  return useQuery({
    queryKey: ['bank-applications'],
    enabled,
    queryFn: () => apiFetch<{ items: BankApplication[] }>('/api/v1/bank-applications').then((r) => r.items),
  })
}

export function listInstitutions(application: string, country: string): Promise<BankInstitution[]> {
  return apiFetch<{ items: BankInstitution[] }>(
    `/api/v1/bank-applications/${encodeURIComponent(application)}/institutions?country=${encodeURIComponent(country)}`,
  ).then((r) => r.items)
}

export function startConnection(body: { application: string; institution_id: string; country: string }): Promise<ConsentStart> {
  return apiFetch<ConsentStart>(base, { method: 'POST', json: body })
}

export function reconnect(id: string): Promise<ConsentStart> {
  return apiFetch<ConsentStart>(`${base}/${encodeURIComponent(id)}/reconnect`, { method: 'POST' })
}

/** Refreshes now. A 200 answer can still carry a failed run: read last_refresh_run. */
export function refresh(id: string): Promise<BankConnection> {
  return apiFetch<BankConnection>(`${base}/${encodeURIComponent(id)}/refresh`, { method: 'POST' })
}

export function disconnect(c: Pick<BankConnection, 'id' | 'version'>): Promise<BankConnection> {
  return apiFetch<BankConnection>(`${base}/${encodeURIComponent(c.id)}/disconnect`, { method: 'POST', headers: { 'If-Match': `"${c.version}"` } })
}

/** What the bank's redirect brought back; the code is sent once and never kept. */
export function completeConsent(body: { state: string; code?: string; error?: string }): Promise<BankConnection> {
  return apiFetch<BankConnection>(`${base}/complete`, { method: 'POST', json: body })
}
