import { useState } from 'react'
import { Navigate } from '@tanstack/react-router'
import { useMutation, useQuery } from '@tanstack/react-query'
import { ApiError, apiFetch, splitErrors } from '@/lib/api'
import { api, useSession, useStartSession } from '@/lib/auth'
import type { CurrentSession, SecondFactorChallenge } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/field'
import { FormError } from '@/components/form-error'
import { formatInstant } from '@/lib/format'
import { FALLBACK_METHODS, methodsQuery, navigation, oidcErrorMessage, recoveryErrorMessage } from '@/lib/oidc'

/**
 * The second-factor challenge the single-sign-on callback leaves in the address fragment
 * (/sign-in?step=second_factor#second_factor=<token>). It is removed from the address bar at once so it is not kept in history.
 */
function takeFragmentChallenge(): SecondFactorChallenge | null {
  const m = /^#second_factor=(.+)$/.exec(window.location.hash)
  if (!m) return null
  let token: string
  try {
    token = decodeURIComponent(m[1] ?? '')
  } catch {
    return null
  }
  const params = new URLSearchParams(window.location.search)
  params.delete('step')
  const rest = params.toString()
  window.history.replaceState(window.history.state, '', window.location.origin + window.location.pathname + (rest ? `?${rest}` : ''))
  return { second_factor_required: true, challenge: token, methods: ['totp', 'recovery_code'] }
}

/** Only same-origin paths are followed after sign-in. */
export function safeRedirect(r: string | undefined): string {
  // Backslashes and control characters are rejected: browsers read `/\` as `//`.
  if (!r || [...r].some((c) => c === '\\' || c.charCodeAt(0) < 0x20 || c.charCodeAt(0) === 0x7f)) return '/'
  return r.startsWith('/') && !r.startsWith('//') && !r.startsWith('/sign-in') ? r : '/'
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

export function SignInPage({ redirect, oidcError, recoveryError, notice }: { redirect?: string; oidcError?: string; recoveryError?: string; notice?: string }) {
  const session = useSession()
  const start = useStartSession()
  const methods = useQuery(methodsQuery)
  const m = methods.data ?? FALLBACK_METHODS
  const [challenge, setChallenge] = useState<SecondFactorChallenge | null>(takeFragmentChallenge)
  const [expired, setExpired] = useState(false)
  const target = safeRedirect(redirect)

  if (session.data) return <Navigate to={target} replace />

  return (
    <section className="mx-auto max-w-sm space-y-4">
      <h1 className="text-2xl font-semibold">Sign in</h1>
      {!challenge && notice === 'bank_incomplete' && (
        <p role="status" data-testid="bank-incomplete" className="text-sm">
          The bank connection was not completed because your session had ended. Sign in, then start connecting the bank again from Connections.
        </p>
      )}
      {!challenge && oidcError && <FormError message={oidcErrorMessage(oidcError)} />}
      {!challenge && recoveryError && <FormError message={recoveryErrorMessage(recoveryError)} />}
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
        <>
          {m.password.enabled && (
            <PasswordStep
              notice={expired ? 'Your sign-in expired before the second step finished. Sign in again.' : undefined}
              onChallenge={(c) => {
                setExpired(false)
                setChallenge(c)
              }}
              onDone={start}
            />
          )}
          {!m.password.enabled && expired && <FormError message="Your sign-in expired before the second step finished. Sign in again." />}
          {m.oidc.enabled && m.oidc.authorize_url && (
            <div className="space-y-2">
              {m.password.enabled && <p className="text-center text-sm text-muted-foreground">or</p>}
              <Button
                type="button"
                variant={m.password.enabled ? 'outline' : 'default'}
                className="w-full"
                onClick={() => navigation.goTo(`${m.oidc.authorize_url}?return_to=${encodeURIComponent(target)}`)}
              >
                Sign in with {m.oidc.name ?? 'single sign-on'}
              </Button>
            </div>
          )}
          {!m.password.enabled && !m.oidc.enabled && (
            <FormError message="No sign-in method is available. Ask whoever runs this Ledger." />
          )}
        </>
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
        Your first step was accepted. {challenge.expires_at ? `Finish signing in within five minutes (until ${formatInstant(challenge.expires_at)}).` : 'Finish signing in within five minutes.'}
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
