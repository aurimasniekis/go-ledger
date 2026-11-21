import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import type { Outlook } from './types'

export const outlookKey = (through: string | undefined, currency: string | undefined) => ['outlook', through ?? '', currency ?? ''] as const

/** The outlook against recorded commitments; through and currency default on the server (current month, reporting.currency). */
export function useOutlook(through?: string, currency?: string) {
  return useQuery({
    queryKey: outlookKey(through, currency),
    queryFn: ({ signal }) => {
      const q = new URLSearchParams()
      if (through) q.set('through', through)
      if (currency) q.set('currency', currency)
      const s = q.toString()
      return apiFetch<Outlook>('/api/v1/outlook' + (s ? '?' + s : ''), { signal })
    },
  })
}
