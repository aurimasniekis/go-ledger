import type { ReactNode } from 'react'
import { RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/status-badge'
import { ApiError } from '@/lib/api'
import { formatInstant } from '@/lib/format'

/**
 * The shared states of any screen that shows server data: loading, empty, stale, disconnected and failed.
 * Each says what happened, what is still true (for example that nothing was changed) and what to do next.
 * Status is a badge (icon plus words), never colour alone. Roles: loading and stale are "status" (polite),
 * failed and disconnected are "alert". None of them moves focus.
 */

export function LoadingState({ label, className = '' }: { label: string; className?: string }) {
  return (
    <div role="status" aria-busy="true" className={`space-y-2 rounded-md border border-border bg-surface p-4 ${className}`}>
      <p className="text-muted-foreground">Loading {label}…</p>
      <div aria-hidden className="h-4 w-2/3 rounded-sm bg-muted" />
      <div aria-hidden className="h-4 w-1/2 rounded-sm bg-muted" />
    </div>
  )
}

/** Nothing to show yet. The heading level fits the page (h2 under the page title, h3 inside a section). */
export function EmptyState({
  title,
  children,
  action,
  level = 3,
  id,
}: {
  title: ReactNode
  children?: ReactNode
  action?: ReactNode
  level?: 2 | 3
  id: string
}) {
  const H = level === 2 ? 'h2' : 'h3'
  return (
    <section aria-labelledby={id} className="space-y-1 rounded-md border border-border bg-surface p-4">
      <H id={id} className="font-semibold">
        {title}
      </H>
      {children && <div className="text-sm text-muted-foreground">{children}</div>}
      {action}
    </section>
  )
}

function Retry({ onRetry, label = 'Try again' }: { onRetry: () => void; label?: string }) {
  return (
    <Button variant="outline" size="sm" onClick={onRetry}>
      <RefreshCw aria-hidden className="size-4" />
      {label}
    </Button>
  )
}

/** A request that was answered with an error. Nothing is changed by a failed read; say so. */
export function FailedState({
  title,
  error,
  onRetry,
  children,
}: {
  title: string
  error?: Error | null
  onRetry?: () => void
  children?: ReactNode
}) {
  const api = error instanceof ApiError ? error : null
  return (
    <div role="alert" className="space-y-2 rounded-md border border-border-control bg-surface p-4">
      <StatusBadge status="error" label={title} />
      <p>{error?.message ?? 'The request failed.'}</p>
      <p className="text-sm text-muted-foreground">
        Nothing was changed. {api?.requestId ? <>Reference <code>{api.requestId}</code>. </> : null}
        {children}
      </p>
      {onRetry && <Retry onRetry={onRetry} />}
    </div>
  )
}

/** The server or a provider cannot be reached. What is already on screen is the last data that loaded. */
export function DisconnectedState({
  what = 'the server',
  children,
  onRetry,
}: {
  what?: string
  children?: ReactNode
  onRetry?: () => void
}) {
  return (
    <div role="alert" className="space-y-2 rounded-md border border-border-control bg-surface p-4">
      <StatusBadge status="disconnected" />
      <p className="text-sm font-medium">Cannot reach {what}.</p>
      <p className="text-sm">
        {children ?? 'Data already on screen is the last that loaded and may be out of date. Changes cannot be saved until the connection is back.'}
      </p>
      {onRetry && <Retry onRetry={onRetry} />}
    </div>
  )
}

/** Earlier data is still shown because a refresh failed: say how old it is. */
export function StaleNotice({
  label,
  updatedAt,
  onRetry,
  offline = false,
}: {
  label: string
  updatedAt: number
  onRetry: () => void
  offline?: boolean
}) {
  return (
    <p role="status" className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
      <StatusBadge status={offline ? 'disconnected' : 'stale'} />
      <span>
        Could not refresh {label}; showing data from <time dateTime={new Date(updatedAt).toISOString()}>{formatInstant(updatedAt)}</time>.
      </span>
      <Button variant="outline" size="sm" onClick={onRetry}>
        Try again
      </Button>
    </p>
  )
}
