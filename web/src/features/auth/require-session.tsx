import { useEffect, useState } from 'react'
import { Outlet, useLocation, useRouter, useSearch } from '@tanstack/react-router'
import { ApiError } from '@/lib/api'
import { useSession } from '@/lib/auth'
import { Button } from '@/components/ui/button'
import { FormError } from '@/components/form-error'
import { ReauthProvider } from '@/features/auth/reauth'

/** Where a session confined to two-factor enrolment may go (ADR-0030 §8). */
export const ENROLMENT_PATH = '/account/security'

/** Where a bank sends the user back after consent; its address carries a single-use code. */
export const BANK_CALLBACK_PATH = '/connections/callback'

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
  // address is cleaned meanwhile), so "Try again" can still complete the connection.
  const [held, setHeld] = useState<{ search: string; stripped: boolean } | null>(null)
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
  // What the matched route sees: the restored address counts only once the route itself has it.
  const matched = useSearch({ strict: false }) as { state?: unknown; code?: unknown; error?: unknown }
  const hasReturn = !!(matched.state || matched.code || matched.error)
  // State kept from render to render (set while rendering, never inside an effect).
  if (failed && bankReturn && held === null && loc.searchStr) setHeld({ search: loc.searchStr, stripped: false })
  if (held && !held.stripped && !loc.searchStr && !hasReturn) setHeld({ ...held, stripped: true })
  if (held?.stripped && hasReturn) setHeld(null)
  const heldSearch = held?.search
  const needsStrip = !!held && !held.stripped && !!loc.searchStr
  const needsRestore = !!held?.stripped && !!s.data && !s.data.two_factor.enrollment_required && !hasReturn
  useEffect(() => {
    if (needsStrip) void navigate({ to: BANK_CALLBACK_PATH, search: {}, replace: true })
    else if (needsRestore && heldSearch) void navigate({ to: BANK_CALLBACK_PATH, search: Object.fromEntries(new URLSearchParams(heldSearch)), replace: true })
  }, [needsStrip, needsRestore, heldSearch, navigate])
  // Nothing is rendered until the page's route has the restored address: the page reads its code on first render.
  const confinedAway = !!s.data?.two_factor.enrollment_required && loc.pathname !== ENROLMENT_PATH
  const bankNotice = bankReturn || held !== null
  // The code leaves the address first, on its own step, and only then does the session move on.
  const stripFirst = confinedAway && bankReturn && !!loc.searchStr
  useEffect(() => {
    if (stripFirst) void navigate({ to: BANK_CALLBACK_PATH, search: {}, replace: true })
    else if (confinedAway) void navigate({ to: ENROLMENT_PATH, search: {}, replace: true, state: (bankNotice ? { bankIncomplete: true } : {}) as never })
  }, [stripFirst, confinedAway, bankNotice, navigate])
  const restoring = !!s.data && held !== null && bankReturn
  if (s.data) {
    if (s.data.two_factor.enrollment_required && loc.pathname !== ENROLMENT_PATH) {
      // The bank's code is not kept; the user is told the connection was not completed.
      return null
    }
    if (restoring) return null
    return (
      <ReauthProvider>
        <Outlet />
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
