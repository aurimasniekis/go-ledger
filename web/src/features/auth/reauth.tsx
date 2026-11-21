import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch, setReauthHandler, splitErrors } from '@/lib/api'
import { api } from '@/lib/auth'
import { methodsQuery, navigation } from '@/lib/oidc'
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
  // With passwords off the prompt re-authenticates at the identity provider instead. An unreadable
  // answer keeps the password prompt.
  const methods = useQuery(methodsQuery)
  const passwordOff = methods.data?.password.enabled === false

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

  // Leaving the page with the prompt open must not strand the requests waiting on it.
  useEffect(
    () => () => {
      const w = waiters.current
      waiters.current = []
      w.forEach((r) => r(false))
    },
    [],
  )

  const check = useMutation({
    mutationFn: () =>
      apiFetch(api('/auth/reauthenticate'), { method: 'POST', json: { password }, noReauth: true }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['session'] })
      check.reset()
      settle(true)
    },
  })
  const provider = useMutation({
    mutationFn: async () => {
      const here = window.location.pathname + window.location.search
      const r = await apiFetch<{ authorization_url: string }>(api('/auth/oidc/reauthenticate'), {
        method: 'POST',
        json: { return_to: here.startsWith('/sign-in') ? '/' : here },
        noReauth: true,
      })
      navigation.goTo(r.authorization_url)
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
          title={passwordOff ? 'Confirm with your identity provider' : 'Confirm your password'}
          description={
            passwordOff
              ? 'This action needs a recent sign-in at your identity provider. Your change has not been made yet; you will be sent there and back, then can repeat it.'
              : 'This action needs a recent password check. Your change has not been made yet; it runs as soon as you confirm.'
          }
        >
          {passwordOff ? (
            <div className="space-y-4">
              <FormError message={provider.error?.message} />
              <div className="flex flex-wrap justify-end gap-2">
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => {
                    provider.reset()
                    settle(false)
                  }}
                >
                  Cancel
                </Button>
                <Button type="button" disabled={provider.isPending} onClick={() => provider.mutate()}>
                  {provider.isPending ? 'Starting…' : 'Continue to your provider'}
                </Button>
              </div>
            </div>
          ) : (
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
          )}
        </DialogContent>
      </Dialog>
    </>
  )
}
