import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch, setReauthHandler, splitErrors } from '@/lib/api'
import { api } from '@/lib/auth'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent } from '@/components/ui/dialog'
import { Field } from '@/components/field'
import { FormError } from '@/components/form-error'

/**
 * Prompts for the password when the API answers reauthentication_required (ADR-0026 §7), then lets
 * the failed request run again once. It registers itself with the API client, so no caller handles
 * the status. Cancelling resolves false and the original action fails with the server's message.
 * It lives inside the signed-in layout, so it also serves the enrolment-only gate.
 */
export function ReauthProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const waiters = useRef<((ok: boolean) => void)[]>([])
  const [password, setPassword] = useState('')

  const settle = useCallback((ok: boolean) => {
    const w = waiters.current
    waiters.current = []
    setOpen(false)
    setPassword('')
    w.forEach((r) => r(ok))
  }, [])

  useEffect(() => {
    setReauthHandler(
      () =>
        new Promise<boolean>((resolve) => {
          // Several requests may need it at once: one prompt serves them all.
          waiters.current.push(resolve)
          setOpen(true)
        }),
    )
    return () => setReauthHandler(null)
  }, [])

  const check = useMutation({
    mutationFn: () =>
      apiFetch(api('/auth/reauthenticate'), { method: 'POST', json: { password }, noReauth: true }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['session'] })
      check.reset()
      settle(true)
    },
  })
  const { fields, form } = splitErrors(check.error, ['password'])

  return (
    <>
      {children}
      <Dialog
        open={open}
        onOpenChange={(o) => {
          if (!o && !check.isPending) {
            check.reset()
            settle(false)
          }
        }}
      >
        <DialogContent
          title="Confirm your password"
          description="This action needs a recent password check. Your change has not been made yet; it runs as soon as you confirm."
        >
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault()
              check.mutate()
            }}
          >
            <FormError message={form} />
            <Field label="Password" name="password" error={fields.password}>
              {(p) => (
                <Input
                  {...p}
                  type="password"
                  autoComplete="current-password"
                  required
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              )}
            </Field>
            <div className="flex flex-wrap justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={check.isPending}
                onClick={() => {
                  check.reset()
                  settle(false)
                }}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={check.isPending || password === ''}>
                {check.isPending ? 'Checking…' : 'Confirm'}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </>
  )
}
