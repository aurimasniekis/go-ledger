import { useEffect, useRef } from 'react'
import { Link, Outlet, useNavigate, useRouterState, useSearch } from '@tanstack/react-router'
import { AnnouncerProvider, useAnnounce } from '@/components/announcer'
import { ThemeSelect } from '@/components/theme-select'
import { DisconnectedState } from '@/components/state-panels'
import { PrimaryNav, AccountMenu } from '@/components/nav'
import { useOnline } from '@/lib/connection'
import { oidcErrorMessage } from '@/lib/oidc'
import { Button } from '@/components/ui/button'
import { FormError } from '@/components/form-error'

/**
 * The app shell (U1): skip link, header with the brand, the primary navigation, the account menu and
 * the theme switch, then the page. It never shows navigation to someone who is signed out (the links
 * would only bounce back to sign-in); the sign-in page gets the brand and the theme switch only.
 * It also holds the live-region announcer and the offline banner, which keep the last data on screen.
 */
export function AppShell() {
  return (
    <AnnouncerProvider>
      <div className="min-h-screen">
        <a
          href="#main"
          className="sr-only focus:not-sr-only focus:absolute focus:z-50 focus:m-2 focus:rounded-md focus:bg-surface focus:px-3 focus:py-2"
        >
          Skip to content
        </a>
        <header className="relative border-b border-border bg-surface">
          <div className="mx-auto flex max-w-5xl flex-wrap items-center gap-x-2 gap-y-2 px-4 py-2 sm:gap-x-4">
            <Link to="/" className="font-semibold">Ledger</Link>
            <PrimaryNav />
            <div className="ml-auto flex flex-wrap items-center justify-end gap-x-2 gap-y-2 sm:gap-x-3">
              <AccountMenu />
              <ThemeSelect />
            </div>
          </div>
        </header>
        <OfflineBanner />
        <OidcOutcome />
        <main id="main" tabIndex={-1} className="mx-auto max-w-5xl p-4 focus:outline-none">
          <Outlet />
        </main>
      </div>
    </AnnouncerProvider>
  )
}

/**
 * The outcome of a provider round trip (re-authentication, link, sign-in error) on whatever page the
 * user lands on (ADR-0033). Sign-in and Account security already show their own errors, and the
 * security page its own link confirmation, so those are left to them. A confirmed identity does not
 * mean the action that needed it ran: the message says so.
 */
function OidcOutcome() {
  const search = useSearch({ strict: false }) as { oidc?: unknown; oidc_error?: unknown }
  const path = useRouterState({ select: (s) => s.location.pathname })
  const navigate = useNavigate()
  const own = path === '/sign-in' || path === '/account/security'
  const error = typeof search.oidc_error === 'string' && !own ? oidcErrorMessage(search.oidc_error) : null
  const reauthed = search.oidc === 'reauthenticated'
  if (!error && !reauthed) return null
  const dismiss = () =>
    void navigate({ to: '.', search: ((prev: Record<string, unknown>) => ({ ...prev, oidc: undefined, oidc_error: undefined })) as never, replace: true })
  return (
    <div className="mx-auto flex max-w-5xl flex-wrap items-start gap-2 px-4 pt-4">
      <div className="min-w-0 flex-1">
        {error ? (
          <FormError message={error} />
        ) : (
          <p role="status" className="rounded-md border border-border bg-surface p-3 text-sm">
            Your identity was confirmed with the identity provider. The action that needed it has not been repeated yet; run it again.
          </p>
        )}
      </div>
      <Button type="button" variant="outline" onClick={dismiss}>
        Dismiss
      </Button>
    </div>
  )
}

/** Shown while the browser reports no network. Screens keep showing what they last loaded, labelled with its age. */
function OfflineBanner() {
  const online = useOnline()
  const announce = useAnnounce()
  const first = useRef(true)
  useEffect(() => {
    if (first.current) {
      first.current = false
      return
    }
    announce(online ? 'Connection restored.' : 'You are offline. Changes cannot be saved until the connection is back.', online ? 'polite' : 'assertive')
  }, [online, announce])
  if (online) return null
  return (
    <div className="mx-auto max-w-5xl px-4 pt-4">
      <DisconnectedState what="the network">
        You are offline. Pages keep the data they last loaded, with its age; nothing can be saved until the connection is back.
      </DisconnectedState>
    </div>
  )
}
