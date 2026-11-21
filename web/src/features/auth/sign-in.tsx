import { useState } from 'react'
import { Navigate } from '@tanstack/react-router'
import { useMutation } from '@tanstack/react-query'
import { ApiError, apiFetch, splitErrors } from '@/lib/api'
import { api, useSession, useStartSession } from '@/lib/auth'
import type { CurrentSession, SecondFactorChallenge } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/field'
import { FormError } from '@/components/form-error'
import { formatInstant } from '@/lib/format'

/** Only same-origin paths are followed after sign-in. */
export function safeRedirect(r: string | undefined): string {
  return r && r.startsWith('/') && !r.startsWith('//') && !r.startsWith('/sign-in') ? r : '/'
}

/** The form-level message: a rate limit says when to try again; other errors use the server's text. */
function formMessage(err: unknown, form: string | undefined): string | undefined {
  if (err instanceof ApiError && err.code === 'rate_limited') {
    return err.retryAfter
      ? `Too many attempts. Try again in ${err.retryAfter} seconds.`
      : 'Too many attempts. Try again in a little while.'
  }
  return form
}

export function SignInPage({ redirect }: { redirect?: string }) {
  const session = useSession()
  const start = useStartSession()
  const [challenge, setChallenge] = useState<SecondFactorChallenge | null>(null)
  const [expired, setExpired] = useState(false)
  const target = safeRedirect(redirect)

  if (session.data) return <Navigate to={target} replace />

  return (
    <section className="mx-auto max-w-sm space-y-4">
      <h1 className="text-2xl font-semibold">Sign in</h1>
      {challenge ? (
        <SecondFactorStep
          challenge={challenge}
          onDone={start}
          onRestart={(wasExpired) => {
            setExpired(wasExpired)
            setChallenge(null)
          }}
        />
      ) : (
        <PasswordStep
          notice={expired ? 'Your sign-in expired before the second step finished. Sign in again.' : undefined}
          onChallenge={(c) => {
            setExpired(false)
            setChallenge(c)
          }}
          onDone={start}
        />
      )}
    </section>
  )
}

function PasswordStep({
  notice,
  onChallenge,
  onDone,
}: {
  notice?: string
  onChallenge: (c: SecondFactorChallenge) => void
  onDone: (s: CurrentSession) => void
}) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [local, setLocal] = useState<{ username?: string; password?: string }>({})
  const m = useMutation({
    mutationFn: () =>
      apiFetch<CurrentSession | SecondFactorChallenge>(api('/auth/sign-in'), {
        method: 'POST',
        json: { username, password },
      }),
    onSuccess: (r) => {
      setPassword('')
      if ('second_factor_required' in r) onChallenge(r)
      else onDone(r)
    },
  })
  const { fields, form } = splitErrors(m.error, ['username', 'password'])
  const err = { ...fields, ...local }
  return (
    <form
      className="space-y-4"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        const l = {
          username: username.trim() ? undefined : 'Enter your username.',
          password: password ? undefined : 'Enter your password.',
        }
        setLocal(l)
        if (!l.username && !l.password) m.mutate()
      }}
    >
      {notice && <FormError message={notice} />}
      <FormError message={formMessage(m.error, form)} />
      <Field label="Username" name="username" error={err.username}>
        {(p) => (
          <Input
            {...p}
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            autoFocus
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
        )}
      </Field>
      <Field label="Password" name="password" error={err.password}>
        {(p) => (
          <Input {...p} type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
        )}
      </Field>
      <Button type="submit" className="w-full" disabled={m.isPending}>
        {m.isPending ? 'Signing in…' : 'Sign in'}
      </Button>
    </form>
  )
}

function SecondFactorStep({
  challenge,
  onDone,
  onRestart,
}: {
  challenge: SecondFactorChallenge
  onDone: (s: CurrentSession) => void
  onRestart: (expired: boolean) => void
}) {
  const [useRecovery, setUseRecovery] = useState(false)
  const [value, setValue] = useState('')
  const [local, setLocal] = useState<string>()
  const m = useMutation({
    mutationFn: () =>
      apiFetch<CurrentSession>(api('/auth/sign-in/second-factor'), {
        method: 'POST',
        json: { challenge: challenge.challenge, ...(useRecovery ? { recovery_code: value } : { code: value }) },
      }),
    onSuccess: onDone,
    onError: (e) => {
      // An unknown, expired or used-up challenge: the password step starts over.
      if (e instanceof ApiError && e.code === 'unauthenticated') onRestart(true)
    },
  })
  const name = useRecovery ? 'recovery_code' : 'code'
  const { fields, form } = splitErrors(m.error, [name])
  const fieldMsg = local ?? fields[name] ?? (m.error instanceof ApiError && m.error.code === 'invalid_second_factor' ? m.error.message : undefined)
  return (
    <form
      className="space-y-4"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        if (!value.trim()) return setLocal(useRecovery ? 'Enter a recovery code.' : 'Enter the 6-digit code.')
        setLocal(undefined)
        m.mutate()
      }}
    >
      <p className="text-sm text-muted-foreground">
        Your password was accepted. Finish signing in within five minutes (until {formatInstant(challenge.expires_at)}).
      </p>
      <FormError message={fieldMsg ? undefined : formMessage(m.error, form)} />
      <Field
        key={name}
        label={useRecovery ? 'Recovery code' : 'Authenticator code'}
        name={name}
        hint={
          useRecovery
            ? 'One of the recovery codes you saved. Each works once.'
            : 'The 6-digit code from your authenticator app.'
        }
        error={fieldMsg}
      >
        {(p) => (
          <Input
            {...p}
            autoComplete="one-time-code"
            inputMode={useRecovery ? 'text' : 'numeric'}
            spellCheck={false}
            autoFocus
            value={value}
            onChange={(e) => setValue(e.target.value)}
          />
        )}
      </Field>
      <Button type="submit" className="w-full" disabled={m.isPending}>
        {m.isPending ? 'Verifying…' : 'Verify'}
      </Button>
      <div className="flex flex-wrap justify-between gap-2">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => {
            setUseRecovery(!useRecovery)
            setValue('')
            setLocal(undefined)
            m.reset()
          }}
        >
          {useRecovery ? 'Use an authenticator code' : 'Use a recovery code'}
        </Button>
        <Button type="button" variant="ghost" size="sm" onClick={() => onRestart(false)}>
          Start over
        </Button>
      </div>
    </form>
  )
}
