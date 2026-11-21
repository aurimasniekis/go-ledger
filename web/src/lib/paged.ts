import { useInfiniteQuery } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import type { Page } from '@/lib/types'

/** A cursor-paged list (ADR-0011): pages are fetched on request and flattened into one array. */
export function usePagedList<T>(key: readonly unknown[], path: string, params: Record<string, string | undefined> = {}) {
  return useInfiniteQuery({
    queryKey: [...key, params],
    initialPageParam: '',
    queryFn: ({ pageParam }) => {
      const q = new URLSearchParams()
      for (const [k, v] of Object.entries(params)) if (v) q.set(k, v)
      if (pageParam) q.set('cursor', pageParam)
      const qs = q.toString()
      return apiFetch<Page<T>>(qs ? `${path}?${qs}` : path)
    },
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    select: (d) => d.pages.flatMap((p) => p.items),
  })
}
