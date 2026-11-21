import { useEffect, useRef } from 'react'
import { OctagonX } from 'lucide-react'

/**
 * A form-level error. It takes focus when it appears so keyboard and screen-reader users land on it;
 * field errors stay next to their fields.
 */
export function FormError({ message, id }: { message?: string; id?: string }) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (message) ref.current?.focus()
  }, [message])
  if (!message) return null
  return (
    <div
      ref={ref}
      id={id}
      role="alert"
      tabIndex={-1}
      className="flex items-start gap-2 rounded-md border border-status-error bg-status-error-bg p-3 text-sm text-status-error"
    >
      <OctagonX aria-hidden className="mt-0.5 size-4 shrink-0" />
      <span>{message}</span>
    </div>
  )
}
