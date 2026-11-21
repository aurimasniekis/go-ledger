import { useEffect, useRef } from 'react'
import { Outlet } from '@tanstack/react-router'
import { AnnouncerProvider, useAnnounce } from '@/components/announcer'
import { ThemeSelect } from '@/components/theme-select'
import { DisconnectedState } from '@/components/state-panels'
import { PrimaryNav, AccountMenu } from '@/components/nav'
import { useOnline } from '@/lib/connection'

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
          <div className="mx-auto flex max-w-5xl flex-wrap items-center gap-x-4 gap-y-2 px-4 py-2">
            <span className="font-semibold">Ledger</span>
            <PrimaryNav />
            <div className="ml-auto flex flex-wrap items-center justify-end gap-x-3 gap-y-2">
              <AccountMenu />
              <ThemeSelect />
            </div>
          </div>
        </header>
        <OfflineBanner />
        <main id="main" tabIndex={-1} className="mx-auto max-w-5xl p-4 focus:outline-none">
          <Outlet />
        </main>
      </div>
    </AnnouncerProvider>
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
