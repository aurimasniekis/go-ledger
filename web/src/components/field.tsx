import { useId, type ReactNode } from 'react'
import { cn } from '@/lib/utils'

export interface ControlProps {
  id: string
  name: string
  'aria-describedby'?: string
  'aria-invalid'?: true
}

/**
 * A labelled form control. The label is a real <label>; the hint and the error are tied to the
 * control with aria-describedby, and the control is aria-invalid while there is an error, so a
 * screen reader announces the problem with the field (field-associated errors, ADR-0006).
 */
export function Field({
  label,
  name,
  hint,
  error,
  className,
  children,
}: {
  label: ReactNode
  name: string
  hint?: ReactNode
  error?: string
  className?: string
  children: (props: ControlProps) => ReactNode
}) {
  const uid = useId()
  const id = `${uid}-${name}`
  const hintId = hint ? `${id}-hint` : undefined
  const errId = error ? `${id}-error` : undefined
  const describedBy = [hintId, errId].filter(Boolean).join(' ') || undefined
  return (
    <div className={cn('space-y-1', className)}>
      <label htmlFor={id} className="block text-sm font-medium">
        {label}
      </label>
      {children({ id, name, 'aria-describedby': describedBy, 'aria-invalid': error ? true : undefined })}
      {hint && (
        <p id={hintId} className="text-xs text-muted-foreground">
          {hint}
        </p>
      )}
      {error && (
        <p id={errId} className="text-xs font-medium text-status-error">
          {error}
        </p>
      )}
    </div>
  )
}
