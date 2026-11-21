import { useEffect } from 'react'
import { Navigate, Outlet, useLocation, useNavigate } from '@tanstack/react-router'
import { ApiError } from '@/lib/api'
import { useSession } from '@/lib/auth'
import { Button } from '@/components/ui/button'
import { FormError } from '@/components/form-error'
import { ReauthProvider } from '@/features/auth/reauth'

/** Where a session confined to two-factor enrolment may go (ADR-0030 §8). */
export const ENROLMENT_PATH = '/account/security'

/**
 * The signed-in gate for every protected page: loading, signed out (to sign-in, remembering where
 * the user was going), confined to enrolment (to the enrolment page only), or open. The server
 * enforces all of it; this only keeps the UI from showing screens that would answer 401 or 403.
 * U1 should take over this layout together with the shell.
 */
export function RequireSession() {
  const s = useSession()
  const loc = useLocation()
  const navigate = useNavigate()
  const signedOut = !s.data && s.error instanceof ApiError && s.error.status === 401
  const href = loc.href
  useEffect(() => {
    // Once per place the user was going, not on every render.
    // The layout stays mounted for a moment after navigating away: never redirect from the sign-in page itself.
    if (signedOut && !href.startsWith('/sign-in')) void navigate({ to: '/sign-in', search: { redirect: href }, replace: true })
  }, [signedOut, href, navigate])
  if (s.data) {
    if (s.data.two_factor.enrollment_required && loc.pathname !== ENROLMENT_PATH) {
      return <Navigate to={ENROLMENT_PATH} replace />
    }
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
