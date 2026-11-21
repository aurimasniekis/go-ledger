import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, apiFetch, splitErrors } from '@/lib/api'
import { api, useSession } from '@/lib/auth'
import type { RecoveryCodes, TOTPEnrollment, TwoFactorStatus } from '@/lib/types'
import { formatInstant } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/field'
import { FormError } from '@/components/form-error'
import { Mono } from '@/components/mono'
import { AlertDialog, AlertDialogCancel, AlertDialogContent } from '@/components/ui/dialog'
import { QueryState } from '@/components/query-state'
import { ShowOnceDialog } from '@/components/show-once-dialog'

/**
 * Account security: two-factor enrolment, recovery codes, disabling, password change. Under required
 * 2FA with no authenticator the session is confined to enrolment, and this page shows only that.
 * While two-factor is on, replacing the authenticator, turning it off and replacing the recovery
 * codes also ask for a current code from the authenticator or a recovery code (decision D26-1a).
 */
export function SecurityPage() {
  const session = useSession()
  const confined = session.data?.two_factor.enrollment_required === true
  // Recovery codes live only in this state while their dialog is open.
  const [codes, setCodes] = useState<string[] | null>(null)
  return (
    <section className="space-y-8">
      <h1 id="security-heading" tabIndex={-1} className="text-2xl font-semibold focus:outline-none">Account security</h1>
      {confined && (
        <p role="note" className="rounded-md border border-status-overdue bg-status-overdue-bg p-3 text-sm text-status-overdue">
          This Ledger requires two-factor authentication. Set up an authenticator below to continue; nothing else is
          available until you do.
        </p>
      )}
      <TwoFactor onCodes={setCodes} />
      {!confined && <PasswordForm />}
      {codes && (
        <ShowOnceDialog
          title="Your recovery codes"
          description="Each code signs you in once if you lose your authenticator. Earlier codes no longer work."
          lines={codes}
          listLabel="Recovery codes"
          filename="ledger-recovery-codes.txt"
          returnFocusId="security-heading"
          onDone={() => setCodes(null)}
        />
      )}
    </section>
  )
}

function TwoFactor({ onCodes }: { onCodes: (codes: string[]) => void }) {
  const qc = useQueryClient()
  const status = useQuery({ queryKey: ['two-factor'], queryFn: () => apiFetch<TwoFactorStatus>(api('/auth/two-factor')) })
  const [enrolling, setEnrolling] = useState(false)
  const [disabling, setDisabling] = useState(false)
  const [regenerating, setRegenerating] = useState(false)
  const refresh = () =>
    Promise.all([qc.invalidateQueries({ queryKey: ['two-factor'] }), qc.invalidateQueries({ queryKey: ['session'] })])

  return (
    <div className="space-y-3" aria-labelledby="tf-h" role="group">
      <h2 id="tf-h" className="text-lg font-semibold">
        Two-factor authentication
      </h2>
      <QueryState query={status} label="two-factor status">
        {(s) => (
          <div className="space-y-3">
            {s.enabled ? (
              <p className="text-sm">
                <strong>On</strong>: authenticator app since {formatInstant(s.enabled_at)}. {s.recovery_codes_remaining} recovery{' '}
                {s.recovery_codes_remaining === 1 ? 'code' : 'codes'} left.
              </p>
            ) : (
              <p className="text-sm">
                <strong>Off.</strong> Signing in needs only your password.
                {s.required && ' This Ledger requires two-factor authentication.'}
              </p>
            )}
            {!enrolling && (
              <div className="flex flex-wrap gap-2">
                <Button onClick={() => setEnrolling(true)}>{s.enabled ? 'Replace authenticator' : 'Set up an authenticator'}</Button>
                {s.enabled && (
                  <Button variant="outline" onClick={() => setRegenerating(true)}>
                    Regenerate recovery codes
                  </Button>
                )}
                {s.enabled && !s.required && (
                  <Button variant="outline" onClick={() => setDisabling(true)}>
                    Turn off two-factor
                  </Button>
                )}
              </div>
            )}
            {s.enabled && s.required && (
              <p className="text-xs text-muted-foreground">
                Two-factor cannot be turned off while this Ledger requires it; replace the authenticator instead.
              </p>
            )}
            {enrolling && (
              <Enrollment
                replacing={s.enabled}
                onCancel={() => setEnrolling(false)}
                onEnabled={async (c) => {
                  setEnrolling(false)
                  onCodes(c)
                  await refresh()
                }}
              />
            )}
          </div>
        )}
      </QueryState>
      <SecondFactorDialog
        open={disabling}
        onOpenChange={setDisabling}
        title="Turn off two-factor authentication?"
        description="Signing in will need only your password, and your recovery codes stop working. You can set it up again at any time."
        confirmLabel="Turn off"
        onConfirm={async (factor) => {
          await apiFetch(api('/auth/two-factor'), { method: 'DELETE', json: factorBody(factor) })
          await refresh()
        }}
      />
      <SecondFactorDialog
        open={regenerating}
        onOpenChange={setRegenerating}
        title="Replace your recovery codes?"
        description="You get ten new codes, shown once. Every earlier code stops working at once."
        confirmLabel="Replace codes"
        onConfirm={async (factor) => {
          const r = await apiFetch<RecoveryCodes>(api('/auth/two-factor/recovery-codes'), { method: 'POST', json: factorBody(factor) })
          onCodes(r.recovery_codes)
          await refresh()
        }}
      />
    </div>
  )
}

function Enrollment({
  replacing,
  onCancel,
  onEnabled,
}: {
  replacing: boolean
  onCancel: () => void
  onEnabled: (codes: string[]) => void | Promise<void>
}) {
  const [enrol, setEnrol] = useState<TOTPEnrollment | null>(null)
  const [code, setCode] = useState('')
  const [factor, setFactor] = useState<CurrentFactor>(emptyFactor)
  const [local, setLocal] = useState<string>()
  const [factorLocal, setFactorLocal] = useState<string>()
  const start = useMutation({
    gcTime: 0,
    mutationFn: () => apiFetch<TOTPEnrollment>(api('/auth/two-factor/totp'), { method: 'POST' }),
    onSuccess: (e) => setEnrol(e),
  })
  const confirm = useMutation({
    gcTime: 0,
    mutationFn: () =>
      apiFetch<RecoveryCodes>(api('/auth/two-factor/totp/confirm'), {
        method: 'POST',
        json: replacing ? { code, ...factorBody(factor) } : { code },
      }),
    onSuccess: (r) => {
      setEnrol(null)
      start.reset()
      void onEnabled(r.recovery_codes)
    },
  })
  const { fields, form } = splitErrors(confirm.error, ['code', ...FACTOR_FIELDS])
  const wrongCode = confirm.error instanceof ApiError && confirm.error.code === 'invalid_second_factor' && !confirm.error.field?.startsWith('current_')
  const codeError = local ?? fields.code ?? (wrongCode ? confirm.error?.message : undefined)
  const factorError = factorLocal ?? fields.current_code ?? fields.current_recovery_code

  if (!enrol) {
    return (
      <div className="space-y-3 rounded-md border border-border p-4">
        <p className="text-sm">
          You will add Ledger to an authenticator app with a secret key, then confirm with a code. You may be asked for your
          password first.
        </p>
        <FormError message={start.error?.message} />
        <div className="flex gap-2">
          <Button onClick={() => start.mutate()} disabled={start.isPending}>
            {start.isPending ? 'Starting…' : 'Start'}
          </Button>
          <Button variant="outline" onClick={onCancel} disabled={start.isPending}>
            Cancel
          </Button>
        </div>
      </div>
    )
  }
  return (
    <form
      className="space-y-4 rounded-md border border-border p-4"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        const missingCode = !code.trim()
        const missingFactor = replacing && !factor.value.trim()
        setLocal(missingCode ? 'Enter the 6-digit code from your authenticator.' : undefined)
        setFactorLocal(missingFactor ? factorMissing(factor) : undefined)
        if (missingCode || missingFactor) return
        confirm.mutate()
      }}
    >
      <h3 className="font-semibold">Add Ledger to your authenticator</h3>
      <p className="text-sm text-muted-foreground">
        Enter this key in your authenticator app (choose “enter a setup key”, time-based, 6 digits). It is shown only now.
        Ledger does not draw a QR code in this version, so there is nothing to scan; the otpauth link below is for apps that
        open it.
      </p>
      <dl className="space-y-2 text-sm">
        <div>
          <dt className="font-medium">Setup key</dt>
          <dd>
            <Mono className="break-all select-all" aria-label="Setup key">
              {enrol.secret}
            </Mono>
          </dd>
        </div>
        <div>
          <dt className="font-medium">otpauth link</dt>
          <dd>
            <Mono className="break-all select-all">{enrol.otpauth_uri}</Mono>
          </dd>
        </div>
      </dl>
      <p className="text-xs text-muted-foreground">Confirm before {formatInstant(enrol.expires_at)}.</p>
      <FormError message={codeError || factorError ? undefined : form} />
      <Field label={replacing ? 'Code from your new authenticator' : 'Code from your authenticator'} name="code" error={codeError}>
        {(p) => (
          <Input {...p} inputMode="numeric" autoComplete="one-time-code" autoFocus value={code} onChange={(e) => setCode(e.target.value)} />
        )}
      </Field>
      {replacing && <CurrentFactorField value={factor} onChange={setFactor} error={factorError} />}
      <div className="flex gap-2">
        <Button type="submit" disabled={confirm.isPending}>
          {confirm.isPending ? 'Confirming…' : 'Confirm and turn on'}
        </Button>
        <Button type="button" variant="outline" disabled={confirm.isPending} onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  )
}

/** The current second factor a change to two-factor needs while it is on (decision D26-1a). */
type CurrentFactor = { kind: 'totp' | 'recovery'; value: string }
const emptyFactor: CurrentFactor = { kind: 'totp', value: '' }
const FACTOR_FIELDS = ['current_code', 'current_recovery_code'] as const

function factorBody(f: CurrentFactor) {
  return f.kind === 'totp' ? { current_code: f.value.trim() } : { current_recovery_code: f.value.trim() }
}

function factorMissing(f: CurrentFactor) {
  return f.kind === 'totp' ? 'Enter the current 6-digit code from your authenticator.' : 'Enter one of your recovery codes.'
}

/**
 * Asks for a code from the authenticator in use now, or, for someone without it at hand, one of
 * their recovery codes. Switching clears what was typed, so a code never goes to the wrong field.
 */
function CurrentFactorField({ value, onChange, error }: { value: CurrentFactor; onChange: (f: CurrentFactor) => void; error?: string }) {
  const totp = value.kind === 'totp'
  return (
    <div className="space-y-1">
      <Field
        label={totp ? 'Current code from your authenticator' : 'One of your recovery codes'}
        name={totp ? 'current_code' : 'current_recovery_code'}
        hint={
          totp
            ? 'Two-factor is on, so this change also needs a code from the authenticator you use now.'
            : 'Two-factor is on, so this change needs a recovery code if your authenticator is not at hand. The code is used up.'
        }
        error={error}
      >
        {(p) => (
          <Input
            {...p}
            inputMode={totp ? 'numeric' : 'text'}
            autoComplete={totp ? 'one-time-code' : 'off'}
            value={value.value}
            onChange={(e) => onChange({ ...value, value: e.target.value })}
          />
        )}
      </Field>
      <Button type="button" variant="ghost" size="sm" onClick={() => onChange({ kind: totp ? 'recovery' : 'totp', value: '' })}>
        {totp ? 'Use a recovery code instead' : 'Use your authenticator instead'}
      </Button>
    </div>
  )
}

/**
 * Confirms a change to two-factor that needs the current second factor: the dialog asks for it,
 * stays open with the error on the field when the server refuses it, and closes only after the
 * server has accepted the change.
 */
function SecondFactorDialog({
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
  description: string
  confirmLabel: string
  onConfirm: (factor: CurrentFactor) => Promise<unknown>
}) {
  const [factor, setFactor] = useState<CurrentFactor>(emptyFactor)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [local, setLocal] = useState<string>()
  const close = (o: boolean) => {
    if (pending) return
    if (!o) {
      setError(null)
      setLocal(undefined)
      setFactor(emptyFactor)
    }
    onOpenChange(o)
  }
  const { fields, form } = splitErrors(error, FACTOR_FIELDS)
  const fieldError = local ?? fields.current_code ?? fields.current_recovery_code
  return (
    <AlertDialog open={open} onOpenChange={close}>
      <AlertDialogContent title={title} description={description}>
        <form
          className="space-y-4"
          noValidate
          onSubmit={async (e) => {
            e.preventDefault()
            if (!factor.value.trim()) return setLocal(factorMissing(factor))
            setLocal(undefined)
            setPending(true)
            setError(null)
            try {
              await onConfirm(factor)
              setFactor(emptyFactor)
              onOpenChange(false)
            } catch (err) {
              setError(err)
            } finally {
              setPending(false)
            }
          }}
        >
          <FormError message={fieldError ? undefined : form} />
          <CurrentFactorField value={factor} onChange={setFactor} error={fieldError} />
          <div className="flex flex-wrap justify-end gap-2">
            <AlertDialogCancel asChild>
              <Button type="button" variant="outline" disabled={pending}>
                Cancel
              </Button>
            </AlertDialogCancel>
            <Button type="submit" disabled={pending}>
              {pending ? 'Working…' : confirmLabel}
            </Button>
          </div>
        </form>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function PasswordForm() {
  const [pw, setPw] = useState('')
  const [again, setAgain] = useState('')
  const [local, setLocal] = useState<string>()
  const m = useMutation({
    mutationFn: () => apiFetch<{ sessions_ended: number }>(api('/auth/password'), { method: 'PUT', json: { password: pw } }),
    onSuccess: () => {
      setPw('')
      setAgain('')
    },
  })
  const { fields, form } = splitErrors(m.error, ['password'])
  return (
    <form
      className="max-w-sm space-y-4"
      noValidate
      aria-labelledby="pw-h"
      onSubmit={(e) => {
        e.preventDefault()
        if (pw !== again) return setLocal('The two passwords do not match.')
        setLocal(undefined)
        m.mutate()
      }}
    >
      <h2 id="pw-h" className="text-lg font-semibold">
        Change password
      </h2>
      <FormError message={form} />
      <Field label="New password" name="password" error={fields.password}>
        {(p) => <Input {...p} type="password" autoComplete="new-password" value={pw} onChange={(e) => { setPw(e.target.value); m.reset() }} />}
      </Field>
      <Field label="Repeat the new password" name="again" error={local}>
        {(p) => <Input {...p} type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} />}
      </Field>
      <Button type="submit" disabled={m.isPending}>
        {m.isPending ? 'Saving…' : 'Change password'}
      </Button>
      {m.isSuccess && (
        <p role="status" className="text-sm">
          Password changed.{' '}
          {m.data.sessions_ended > 0 ? `${m.data.sessions_ended} other session(s) were signed out.` : 'No other sessions were open.'}
        </p>
      )}
    </form>
  )
}
