import type { ComponentProps, ReactNode } from 'react'
import { AlertDialog as AlertPrimitive, Dialog as DialogPrimitive } from 'radix-ui'
import { cn } from '@/lib/utils'

// shadcn/ui Dialog and AlertDialog on Radix, customised to the semantic tokens. Radix traps focus,
// restores it to the trigger on close, and labels the dialog from its title and description.
const overlay = 'fixed inset-0 z-40 bg-foreground/50'
const content =
  'fixed left-1/2 top-1/2 z-50 max-h-[90vh] w-[calc(100vw-2rem)] max-w-lg -translate-x-1/2 -translate-y-1/2 space-y-4 overflow-y-auto rounded-lg border border-border bg-surface-raised p-5 shadow-md focus:outline-none'

export const Dialog = DialogPrimitive.Root
export const DialogTrigger = DialogPrimitive.Trigger

export function DialogContent({
  title,
  description,
  children,
  className,
  ...props
}: { title: ReactNode; description?: ReactNode } & Omit<ComponentProps<typeof DialogPrimitive.Content>, 'title'>) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay className={overlay} />
      <DialogPrimitive.Content className={cn(content, className)} {...props}>
        <DialogPrimitive.Title className="text-lg font-semibold">{title}</DialogPrimitive.Title>
        {description ? (
          <DialogPrimitive.Description className="text-sm text-muted-foreground">{description}</DialogPrimitive.Description>
        ) : (
          <DialogPrimitive.Description className="sr-only">{title}</DialogPrimitive.Description>
        )}
        {children}
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  )
}
export const DialogClose = DialogPrimitive.Close

export const AlertDialog = AlertPrimitive.Root
export function AlertDialogContent({
  title,
  description,
  children,
}: {
  title: ReactNode
  description: ReactNode
  children: ReactNode
}) {
  return (
    <AlertPrimitive.Portal>
      <AlertPrimitive.Overlay className={overlay} />
      <AlertPrimitive.Content className={content}>
        <AlertPrimitive.Title className="text-lg font-semibold">{title}</AlertPrimitive.Title>
        <AlertPrimitive.Description className="text-sm text-muted-foreground">{description}</AlertPrimitive.Description>
        {children}
      </AlertPrimitive.Content>
    </AlertPrimitive.Portal>
  )
}
export const AlertDialogCancel = AlertPrimitive.Cancel
