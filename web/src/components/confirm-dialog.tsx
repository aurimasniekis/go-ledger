import { useState, type ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { AlertDialog, AlertDialogCancel, AlertDialogContent } from '@/components/ui/dialog'
import { FormError } from '@/components/form-error'

/**
 * Confirms an action that cannot be undone. The dialog stays open, with the error shown, when the
 * action fails, and closes only after the server has accepted it: nothing is shown as done before
 * it is.
 */
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  onConfirm,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: ReactNode
  confirmLabel: string
  onConfirm: () => Promise<unknown>
}) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string>()
  const close = (o: boolean) => {
    if (pending) return
    if (!o) setError(undefined)
    onOpenChange(o)
  }
  return (
    <AlertDialog open={open} onOpenChange={close}>
      <AlertDialogContent title={title} description={description}>
        <FormError message={error} />
        <div className="flex flex-wrap justify-end gap-2">
          <AlertDialogCancel asChild>
            <Button variant="outline" disabled={pending}>
              Cancel
            </Button>
          </AlertDialogCancel>
          <Button
            disabled={pending}
            onClick={async () => {
              setPending(true)
              setError(undefined)
              try {
                await onConfirm()
                onOpenChange(false)
              } catch (e) {
                setError(e instanceof Error ? e.message : 'The action failed.')
              } finally {
                setPending(false)
              }
            }}
          >
            {pending ? 'Working…' : confirmLabel}
          </Button>
        </div>
      </AlertDialogContent>
    </AlertDialog>
  )
}
