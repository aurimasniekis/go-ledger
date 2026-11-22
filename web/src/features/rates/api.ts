import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import { usePagedList } from '@/lib/paged'
import type { CachedRate, RatesRun, RatesStatus } from './types'

export const ratesKey = ['rates'] as const

/** Provider, freshness policy and the last refresh (rates:read). The provider is optional; this never gates anything. */
export function useRatesStatus(enabled = true) {
  return useQuery({
    enabled,
    queryKey: [...ratesKey, 'status'],
    queryFn: ({ signal }) => apiFetch<RatesStatus>('/api/v1/rates/status', { signal }),
  })
}

/** The cached rates, grouped by provider, source and pair with the newest rate date first (cursor-paged). */
export function useRates(enabled = true) {
  return usePagedList<CachedRate>([...ratesKey, 'list'], '/api/v1/rates', {}, { enabled })
}

/** Ask the provider for fresh rates now (rates:write, administrators; at most one a minute, never two at once). */
export function useRefreshRates() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => apiFetch<RatesRun>('/api/v1/rates/refreshes', { method: 'POST' }),
    // A refresh only adds or revises cached rates, and a refused or failed one changes nothing: reload either way.
    onSettled: () => qc.invalidateQueries({ queryKey: ratesKey }),
  })
}
