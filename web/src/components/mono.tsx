import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

// Monospace text for identifiers, codes, references and diffs.
export function Mono({ className, ...props }: ComponentProps<'code'>) {
  return <code className={cn('rounded-sm bg-muted px-1 py-0.5 font-mono text-[0.875em]', className)} {...props} />
}
