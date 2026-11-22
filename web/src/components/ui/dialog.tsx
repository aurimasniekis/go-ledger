import { useState, type ComponentProps, type ReactNode } from 'react'
import { AlertDialog as AlertPrimitive, Dialog as DialogPrimitive } from 'radix-ui'
import { cn } from '@/lib/utils'
import { focusFirstUsable } from '@/lib/focus'

// shadcn/ui Dialog and AlertDialog on Radix, customised to the semantic tokens. Radix traps focus and labels the
// dialog from its title and description. Radix returns focus only to a <DialogTrigger>; a dialog opened by a
// controlled flag (the usual case here) would drop focus on BODY, so both contents remember what had focus when
// they opened and give it back on close, falling back to the main region when that element is gone.
const overlay = 'fixed inset-0 z-40 bg-foreground/50'
const content =
  'fixed left-1/2 top-1/2 z-50 max-h-[90vh] w-[calc(100vw-2rem)] max-w-lg -translate-x-1/2 -translate-y-1/2 space-y-4 overflow-y-auto rounded-lg border border-border bg-surface-raised p-5 shadow-md focus:outline-none'

/**
 * Remembers the element focused when the dialog content mounted (it is still the opener at that moment).
 * It must run inside the portal: the DialogContent wrapper itself is mounted while the dialog is closed.
 */
function useReturnFocus(custom?: () => boolean) {
  const [opener] = useState(() => (document.activeElement instanceof HTMLElement ? document.activeElement : null))
  return (e: Event) => {
    e.preventDefault()
    // A caller that has a better target (a notice the change produced) takes focus itself and says so by returning true.
    if (custom?.()) return
    focusFirstUsable([opener])
  }
}
function ReturningContent({ focusInstead, ...props }: ComponentProps<typeof DialogPrimitive.Content> & { focusInstead?: () => boolean }) {
  const returnFocus = useReturnFocus(focusInstead)
  return <DialogPrimitive.Content onCloseAutoFocus={returnFocus} {...props} />
}
function ReturningAlertContent(props: ComponentProps<typeof AlertPrimitive.Content>) {
  const returnFocus = useReturnFocus()
  return <AlertPrimitive.Content onCloseAutoFocus={returnFocus} {...props} />
}

export const Dialog = DialogPrimitive.Root
export const DialogTrigger = DialogPrimitive.Trigger

export function DialogContent({
  title,
  description,
  children,
  className,
  focusInstead,
  ...props
}: { title: ReactNode; description?: ReactNode; focusInstead?: () => boolean } & Omit<ComponentProps<typeof DialogPrimitive.Content>, 'title'>) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay className={overlay} />
      <ReturningContent className={cn(content, className)} focusInstead={focusInstead} {...props}>
        <DialogPrimitive.Title className="text-lg font-semibold">{title}</DialogPrimitive.Title>
        {description ? (
          <DialogPrimitive.Description className="text-sm text-muted-foreground">{description}</DialogPrimitive.Description>
        ) : (
          <DialogPrimitive.Description className="sr-only">{title}</DialogPrimitive.Description>
        )}
        {children}
      </ReturningContent>
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
      <ReturningAlertContent className={content}>
        <AlertPrimitive.Title className="text-lg font-semibold">{title}</AlertPrimitive.Title>
        <AlertPrimitive.Description className="text-sm text-muted-foreground">{description}</AlertPrimitive.Description>
        {children}
      </ReturningAlertContent>
    </AlertPrimitive.Portal>
  )
}
export const AlertDialogCancel = AlertPrimitive.Cancel
