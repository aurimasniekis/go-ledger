import type { ReactNode } from 'react'
import type { UseQueryResult } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/status-badge'
import { FormError } from '@/components/form-error'
import { formatInstant } from '@/lib/format'

/**
 * Makes every state of a query visible: loading, failed with nothing to show, failed but showing
 * earlier data (stale), refreshing, and loaded with the time of the last successful fetch. U1 is
 * expected to absorb this into the shared state primitives.
 */
export function QueryState<T>({
  query,
  label,
  children,
}: {
  query: Pick<UseQueryResult<T>, 'data' | 'error' | 'isError' | 'isPending' | 'isFetching' | 'dataUpdatedAt' | 'refetch'>
  label: string
  children: (data: T) => ReactNode
}) {
  if (query.isPending) {
    return (
      <p role="status" className="text-sm text-muted-foreground">
        Loading {label}…
      </p>
    )
  }
  if (query.data === undefined) {
    return (
      <div className="space-y-2">
        <FormError message={`Could not load ${label}: ${query.error?.message ?? 'unknown error'}`} />
        <Button variant="outline" size="sm" onClick={() => void query.refetch()}>
          Try again
        </Button>
      </div>
    )
  }
  return (
    <div className="space-y-3">
      <p role="status" className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        {query.isError ? (
          <>
            <StatusBadge status="stale" />
            <span>
              Could not refresh {label}; showing data from <time dateTime={new Date(query.dataUpdatedAt).toISOString()}>{formatInstant(query.dataUpdatedAt)}</time>.
            </span>
            <Button variant="outline" size="sm" onClick={() => void query.refetch()}>
              Try again
            </Button>
          </>
        ) : query.isFetching ? (
          <span>Refreshing {label}…</span>
        ) : (
          <span>
            Updated <time dateTime={new Date(query.dataUpdatedAt).toISOString()}>{formatInstant(query.dataUpdatedAt)}</time>
          </span>
        )}
      </p>
      {children(query.data)}
    </div>
  )
}
