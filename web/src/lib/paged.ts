import { useInfiniteQuery } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import type { Page } from '@/lib/types'

/** A cursor-paged list (ADR-0011): pages are fetched on request and flattened into one array. */
export function usePagedList<T>(key: readonly unknown[], path: string, params: Record<string, string | string[] | undefined> = {}, opts: { enabled?: boolean; refetchInterval?: number | false | ((pages: Page<T>[]) => number | false) } = {}) {
  return useInfiniteQuery({
    queryKey: [...key, params],
    enabled: opts.enabled,
    refetchInterval: (q: { state: { data?: { pages: Page<T>[] } } }) => {
      const ri = opts.refetchInterval
      return typeof ri === 'function' ? ri(q.state.data?.pages ?? []) : (ri ?? false)
    },
    initialPageParam: '',
    queryFn: ({ pageParam }) => {
      const q = new URLSearchParams()
      for (const [k, v] of Object.entries(params)) {
        if (Array.isArray(v)) for (const x of v) q.append(k, x)
        else if (v) q.set(k, v)
      }
      if (pageParam) q.set('cursor', pageParam)
      const qs = q.toString()
      return apiFetch<Page<T>>(qs ? `${path}?${qs}` : path)
    },
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    select: (d) => d.pages.flatMap((p) => p.items),
  })
}
