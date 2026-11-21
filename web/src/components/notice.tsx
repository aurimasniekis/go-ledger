import type { ReactNode } from 'react'
import { TriangleAlert } from 'lucide-react'
import { cn } from '@/lib/utils'

/**
 * A caution note on security and account screens (two-factor required, root tokens, show-once secrets).
 * It uses the warning token, never a status hue: overdue and the other status colours mean one thing,
 * a commitment or data state. The icon and the text carry the meaning; colour only reinforces it.
 */
export function Notice({
  children,
  role = 'note',
  className,
  id,
}: {
  children: ReactNode
  role?: 'note' | 'alert'
  className?: string
  id?: string
}) {
  return (
    <p
      id={id}
      role={role}
      data-notice="warning"
      className={cn('flex items-start gap-2 rounded-md border-2 border-warning bg-warning-bg p-3 text-sm text-warning', className)}
    >
      <TriangleAlert aria-hidden className="mt-0.5 size-4 shrink-0" />
      <span>{children}</span>
    </p>
  )
}
