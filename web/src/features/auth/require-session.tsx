import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import { Outlet, useLocation, useRouter } from '@tanstack/react-router'
import { ApiError } from '@/lib/api'
import { useSession } from '@/lib/auth'
import { Button } from '@/components/ui/button'
import { FormError } from '@/components/form-error'
import { ReauthProvider } from '@/features/auth/reauth'

/** Where a session confined to two-factor enrolment may go (ADR-0030 §8). */
export const ENROLMENT_PATH = '/account/security'

/** Where a bank sends the user back after consent; its address carries a single-use code. */
export const BANK_CALLBACK_PATH = '/connections/callback'

/** What a bank's return carries (the callback page's Redirect). */
export interface BankReturn {
  state?: string
  code?: string
  error?: string
}

const HeldBankReturn = createContext<{ redirect: BankReturn; release: () => void } | null>(null)

/**
 * The bank's return for the callback page: the one RequireSession held in memory while the session check was failing,
 * else the address's own. A held return is released as soon as the page has it (the page keeps its own copy), so it
 * never outlives that visit and never re-enters the address, the history or a Referer.
 */
export function useBankReturn(search: BankReturn): BankReturn {
  const held = useContext(HeldBankReturn)
  useEffect(() => {
    held?.release()
  }, [held])
  return held?.redirect ?? search
}

/**
 * Splits the address a signed-out visitor was heading for into the path to return to and the provider
 * outcome (oidc_error) riding on it. The outcome describes the attempt that just ended, so it belongs on
 * the sign-in page; kept in the redirect it would show again after signing in, as if it were new. The
 * oidc marker (a confirmation) is dropped for the same reason.
 */
export function splitOutcome(href: string): { redirect: string; oidcError: string | undefined } {
  const url = new URL(href, 'http://localhost')
  const oidcError = url.searchParams.get('oidc_error') ?? undefined
  url.searchParams.delete('oidc')
  url.searchParams.delete('oidc_error')
  const query = url.searchParams.toString()
  return { redirect: url.pathname + (query ? `?${query}` : '') + url.hash, oidcError }
}

/**
 * The signed-in gate for every protected page: loading, signed out (to sign-in, remembering where
 * the user was going), confined to enrolment (to the enrolment page only), or open. The server
 * enforces all of it; this only keeps the UI from showing screens that would answer 401 or 403.
 * U1 should take over this layout together with the shell.
 */
export function RequireSession() {
  const s = useSession()
  // A bank's return held in memory while the session check is failing for a reason other than being signed out (the
  // address is cleaned at once), so "Try again" can still complete the connection. It never goes back into the address:
  // the callback page receives it through HeldBankReturn and releases it once it has it.
  const [held, setHeld] = useState<BankReturn | null>(null)
  const release = useCallback(() => setHeld(null), [])
  const heldValue = useMemo(() => (held ? { redirect: held, release } : null), [held, release])
  const loc = useLocation()
  // The router's own navigate is stable; the hook's changes with every location, which would restart a pending navigation.
  const { navigate } = useRouter()
  const signedOut = !s.data && s.error instanceof ApiError && s.error.status === 401
  // The sign-in redirect keeps the place the user was going, without a fragment (the server's return_to refuses one).
  // A bank's return carries a single-use consent code: it is never kept anywhere, so the user is sent to Connections
  // with a notice that the connection was not completed.
  // Routes match case-insensitively and ignore a trailing slash, so the check does too.
  const bankReturn = loc.pathname.replace(/\/+$/, '').toLowerCase() === BANK_CALLBACK_PATH
  const failed = !s.data && !s.isPending && !signedOut
  const href = bankReturn ? '/connections' : loc.pathname + loc.searchStr
  useEffect(() => {
    // Once per place the user was going, not on every render.
    // The layout stays mounted for a moment after navigating away: never redirect from the sign-in page itself.
    if (!signedOut || href.startsWith('/sign-in')) return
    const { redirect, oidcError } = splitOutcome(href)
    void navigate({
      to: '/sign-in',
      search: { redirect, ...(oidcError ? { oidc_error: oidcError } : {}), ...(bankReturn ? { notice: 'bank_incomplete' } : {}) },
      replace: true,
    })
  }, [signedOut, href, bankReturn, navigate])
  const confined = !!s.data?.two_factor.enrollment_required
  // Held only while the bank's return is still the page and the session check is still in doubt: a session confined to
  // enrolment, a sign-out or leaving the callback page each tell the user the connection was not completed, so the code
  // is dropped then and can never be used later (decision C-A6g-callback).
  if (held === null && failed && bankReturn && loc.searchStr) {
    const q = new URLSearchParams(loc.searchStr)
    const r: BankReturn = { state: q.get('state') ?? undefined, code: q.get('code') ?? undefined, error: q.get('error') ?? undefined }
    if (r.state || r.code || r.error) setHeld(r)
  }
  if (held !== null && (!bankReturn || confined || signedOut)) setHeld(null)
  const needsStrip = held !== null && bankReturn && !!loc.searchStr
  useEffect(() => {
    if (needsStrip) void navigate({ to: BANK_CALLBACK_PATH, search: {}, replace: true })
  }, [needsStrip, navigate])
  const confinedAway = confined && loc.pathname !== ENROLMENT_PATH
  // The code leaves the address first, on its own step, and only then does the session move on.
  const stripFirst = confinedAway && bankReturn && !!loc.searchStr
  useEffect(() => {
    if (stripFirst) void navigate({ to: BANK_CALLBACK_PATH, search: {}, replace: true })
    else if (confinedAway) void navigate({ to: ENROLMENT_PATH, search: {}, replace: true, state: (bankReturn ? { bankIncomplete: true } : {}) as never })
  }, [stripFirst, confinedAway, bankReturn, navigate])
  if (s.data) {
    if (s.data.two_factor.enrollment_required && loc.pathname !== ENROLMENT_PATH) {
      // The bank's code is not kept; the user is told the connection was not completed.
      return null
    }
    return (
      <ReauthProvider>
        <HeldBankReturn.Provider value={heldValue}>
          <Outlet />
        </HeldBankReturn.Provider>
      </ReauthProvider>
    )
  }
  if (s.isPending) {
    return (
      <p role="status" className="text-sm text-muted-foreground">
        Checking your session…
      </p>
    )
  }
  if (signedOut) return null
  return (
    <div className="space-y-2">
      <FormError message={`Could not check your session: ${s.error?.message ?? 'unknown error'}`} />
      <Button variant="outline" size="sm" onClick={() => void s.refetch()}>
        Try again
      </Button>
    </div>
  )
}
