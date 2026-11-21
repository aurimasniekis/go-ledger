import type { ReactNode } from 'react'
import type { UseQueryResult } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { FormError } from '@/components/form-error'
import { DisconnectedState, LoadingState, StaleNotice } from '@/components/state-panels'
import { formatInstantUtc } from '@/lib/format'
import { isNetworkError } from '@/lib/connection'

/**
 * Makes every state of a query visible: loading, failed with nothing to show (or disconnected), failed
 * but showing earlier data (stale), refreshing, and loaded with the time of the last successful fetch.
 * Built on the shared state panels.
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
  if (query.isPending) return <LoadingState label={label} />
  if (query.data === undefined) {
    if (isNetworkError(query.error)) return <DisconnectedState onRetry={() => void query.refetch()}>Could not load {label}: there is no connection to the server. Nothing is shown because nothing has loaded yet.</DisconnectedState>
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
      {query.isError ? (
        <StaleNotice label={label} updatedAt={query.dataUpdatedAt} offline={isNetworkError(query.error)} onRetry={() => void query.refetch()} />
      ) : (
        <p role="status" className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          {query.isFetching ? (
            <span>Refreshing {label}…</span>
          ) : (
            <span>
              Updated <time dateTime={new Date(query.dataUpdatedAt).toISOString()}>{formatInstantUtc(query.dataUpdatedAt)}</time>
            </span>
          )}
        </p>
      )}
      {children(query.data)}
    </div>
  )
}
