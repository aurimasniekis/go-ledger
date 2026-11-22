import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import type { Outlook } from './types'

export const outlookKey = (through: string | undefined, currency: string | undefined, bucket?: string) => ['outlook', through ?? '', currency ?? '', bucket ?? ''] as const

/**
 * The outlook against recorded commitments; through and currency default on the server (current month,
 * reporting.currency). A bucket slices it to that bucket's commitments and accounts.
 */
export function useOutlook(through?: string, currency?: string, bucket?: string) {
  return useQuery({
    queryKey: outlookKey(through, currency, bucket),
    queryFn: ({ signal }) => {
      const q = new URLSearchParams()
      if (through) q.set('through', through)
      if (currency) q.set('currency', currency)
      if (bucket) q.set('bucket', bucket)
      const s = q.toString()
      return apiFetch<Outlook>('/api/v1/outlook' + (s ? '?' + s : ''), { signal })
    },
  })
}
