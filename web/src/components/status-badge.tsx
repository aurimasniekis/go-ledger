import type { ComponentType } from 'react'
import { CircleAlert, CircleCheck, CirclePause, Clock, History, OctagonX } from 'lucide-react'
import { cn } from '@/lib/utils'

// Commitment and data-state statuses. Meaning is carried by the icon and the
// text label together; colour only reinforces it (WCAG 1.4.1).
export const STATUSES = {
  paid: { label: 'Paid', Icon: CircleCheck, tone: 'text-status-paid bg-status-paid-bg' },
  upcoming: { label: 'Upcoming', Icon: Clock, tone: 'text-status-upcoming bg-status-upcoming-bg' },
  overdue: { label: 'Overdue', Icon: CircleAlert, tone: 'text-status-overdue bg-status-overdue-bg' },
  deferred: { label: 'Deferred', Icon: CirclePause, tone: 'text-status-deferred bg-status-deferred-bg' },
  stale: { label: 'Stale', Icon: History, tone: 'text-status-stale bg-status-stale-bg' },
  error: { label: 'Error', Icon: OctagonX, tone: 'text-status-error bg-status-error-bg' },
} satisfies Record<string, { label: string; Icon: ComponentType<{ className?: string }>; tone: string }>

export type Status = keyof typeof STATUSES

export function StatusBadge({ status, label, className }: { status: Status; label?: string; className?: string }) {
  const { label: defaultLabel, Icon, tone } = STATUSES[status]
  return (
    <span
      data-status={status}
      className={cn(
        'inline-flex items-center gap-1 rounded-sm border border-current px-1.5 py-0.5 text-xs font-medium',
        tone,
        className,
      )}
    >
      <Icon aria-hidden className="size-3.5" />
      {label ?? defaultLabel}
    </span>
  )
}
