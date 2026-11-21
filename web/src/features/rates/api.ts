import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import { usePagedList } from '@/lib/paged'
import type { CachedRate, RatesStatus } from './types'

export const ratesKey = ['rates'] as const

/** Provider, freshness policy and the last refresh (rates:read). The provider is optional; this never gates anything. */
export function useRatesStatus() {
  return useQuery({
    queryKey: [...ratesKey, 'status'],
    queryFn: ({ signal }) => apiFetch<RatesStatus>('/api/v1/rates/status', { signal }),
  })
}

/** The cached rates, grouped by provider, source and pair with the newest rate date first (cursor-paged). */
export function useRates() {
  return usePagedList<CachedRate>([...ratesKey, 'list'], '/api/v1/rates', {})
}
