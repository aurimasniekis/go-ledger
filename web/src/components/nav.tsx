import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { Link, useNavigate, useRouterState } from '@tanstack/react-router'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, Menu } from 'lucide-react'
import { apiFetch, setCsrfToken } from '@/lib/api'
import { api, holds, useMe, useSession } from '@/lib/auth'
import { Button } from '@/components/ui/button'

const link = 'block rounded-md px-2 py-1.5 hover:bg-muted [&.active]:font-semibold [&.active]:underline'

/** A navigation link: at least 44 px tall below lg (WCAG 2.5.8 and the touch-target size the app aims for), compact in the header row from lg up. */
const navLink = `${link} flex min-h-11 items-center lg:min-h-0`

/**
 * The finance pages, always in the header while signed in, never while signed out or confined to enrolment (ADR-0064).
 * From lg (1024 px) up they are a row of links under the brand row (seven links do not fit beside the brand, Account and theme in the 1024 px page width). Below that they sit behind a "Menu" disclosure button (44 px tall,
 * aria-expanded, aria-controls) and open as a list of full-size links, so nothing is shrunk to fit a phone. The links are
 * hidden from everyone, assistive technology included, only by CSS while the menu is closed below lg. The menu closes
 * with Escape (focus returns to its button), on a click elsewhere, when focus leaves it and when the page changes.
 */
export function PrimaryNav() {
  const session = useSession()
  const me = useMe()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  // Open for one place only: navigating elsewhere closes it without an effect.
  const [openAt, setOpenAt] = useState<string | null>(null)
  const open = openAt === pathname
  const wrap = useRef<HTMLDivElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  const panelId = useId()
  useEffect(() => {
    if (!open) return
    const away = (e: PointerEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpenAt(null)
    }
    document.addEventListener('pointerdown', away)
    return () => document.removeEventListener('pointerdown', away)
  }, [open])
  if (!session.data || session.data.two_factor.enrollment_required) return null
  const grants = me.data?.grants
  return (
    <div
      ref={wrap}
      className="order-last w-full"
      onKeyDown={(e) => {
        if (e.key === 'Escape' && open) {
          setOpenAt(null)
          button.current?.focus()
        }
      }}
      onBlur={(e) => {
        const next = e.relatedTarget
        if (open && next instanceof Node && !wrap.current?.contains(next)) setOpenAt(null)
      }}
    >
      <Button ref={button} variant="outline" className="min-h-11 min-w-11 lg:hidden" aria-expanded={open} aria-controls={panelId} onClick={() => setOpenAt(open ? null : pathname)}>
        <Menu aria-hidden className="size-4" />
        Menu
        <ChevronDown aria-hidden className={`size-4 ${open ? 'rotate-180' : ''}`} />
      </Button>
      <nav id={panelId} aria-label="Primary" className={`${open ? 'mt-2 flex' : 'hidden lg:flex'} flex-col text-base lg:mt-0 lg:flex-row lg:flex-wrap lg:gap-x-1`}>
        <Link to="/month" className={navLink}>Monthly view</Link>
        <Link to="/outlook" className={navLink}>Outlook</Link>
        <Link to="/matches" className={navLink}>Payment matches</Link>
        {holds(grants, 'commitments:read') && <Link to="/commitments" className={navLink}>Commitments</Link>}
        {holds(grants, 'organisation:read') && <Link to="/organisation" className={navLink}>Organisation</Link>}
        {holds(grants, 'merchants:read') && <Link to="/merchants" className={navLink}>Merchants</Link>}
        {holds(grants, 'connections:read') && <Link to="/connections" className={navLink}>Bank connections</Link>}
      </nav>
    </div>
  )
}

function Group({ label, children }: { label: string; children: ReactNode }) {
  const id = useId()
  return (
    <div>
      <p id={id} className="px-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </p>
      <ul aria-labelledby={id}>{children}</ul>
    </div>
  )
}

/**
 * Account and administration pages, grouped under one disclosure button, with sign out. A disclosure
 * rather than an ARIA menu: it is plain links in a list, works with Tab, closes with Escape (focus
 * returns to the button), on a click elsewhere, when focus leaves it and when the page changes.
 * The panel is anchored to the header, not to the button (which wraps to any position on a narrow screen):
 * full width minus a margin on small screens, 16rem at the right edge from sm up, so it fits at 320 CSS px and at 200% zoom.
 */
export function AccountMenu() {
  const session = useSession()
  const me = useMe()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  // Open for one place only: navigating elsewhere closes it without an effect.
  const [openAt, setOpenAt] = useState<string | null>(null)
  const open = openAt === pathname
  const wrap = useRef<HTMLDivElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  const panelId = useId()
  const out = useMutation({
    mutationFn: () => apiFetch(api('/auth/sign-out'), { method: 'POST' }),
    onSuccess: async () => {
      setCsrfToken(null)
      qc.clear()
      await navigate({ to: '/sign-in' })
    },
  })
  useEffect(() => {
    if (!open) return
    const away = (e: PointerEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpenAt(null)
    }
    document.addEventListener('pointerdown', away)
    return () => document.removeEventListener('pointerdown', away)
  }, [open])
  if (!session.data) return null
  const confined = session.data.two_factor.enrollment_required
  const grants = me.data?.grants
  const admin = [
    holds(grants, 'users:read') && { to: '/admin/users', label: 'Users' },
    holds(grants, 'users:read') && { to: '/admin/roles', label: 'Roles' },
    holds(grants, 'tokens:read') && { to: '/admin/tokens', label: 'All API tokens' },
  ].filter((x): x is { to: string; label: string } => !!x)

  return (
    <div
      ref={wrap}
      onKeyDown={(e) => {
        if (e.key === 'Escape' && open) {
          setOpenAt(null)
          button.current?.focus()
        }
      }}
      onBlur={(e) => {
        const next = e.relatedTarget
        if (open && next instanceof Node && !wrap.current?.contains(next)) setOpenAt(null)
      }}
    >
      <Button
        ref={button}
        variant="outline"
        size="sm"
        className="max-lg:min-h-11 max-[399px]:px-2"
        aria-expanded={open}
        aria-controls={panelId}
        onClick={() => setOpenAt(open ? null : pathname)}
      >
        Account
        <ChevronDown aria-hidden className={`size-4 ${open ? 'rotate-180' : ''}`} />
      </Button>
      <div
        id={panelId}
        hidden={!open}
        className="absolute inset-x-2 top-full z-30 mt-1 space-y-3 sm:left-auto sm:w-64 rounded-md border border-border bg-surface-raised p-2 shadow-md"
      >
        {!confined && (
          <nav
            aria-label="Account"
            className="space-y-3"
            onClick={(e) => {
              // A link to the page already shown changes nothing, so the menu would stay open: close it and keep focus on its button.
              const a = (e.target as Element).closest('a')
              if (!a) return
              setOpenAt(null)
              if (new URL(a.href, window.location.href).pathname === pathname) button.current?.focus()
            }}
          >
            <Group label="Your account">
              <li><Link to="/account/security" className={link}>Security</Link></li>
              <li><Link to="/account/sessions" className={link}>Sessions</Link></li>
              <li><Link to="/account/tokens" className={link}>Your API tokens</Link></li>
            </Group>
            {holds(grants, 'rates:read') && (
              <Group label="Reference">
                <li><Link to="/rates" className={link}>Exchange rates</Link></li>
              </Group>
            )}
            {admin.length > 0 && (
              <Group label="Administration">
                {admin.map((a) => (
                  <li key={a.to}><Link to={a.to} className={link}>{a.label}</Link></li>
                ))}
              </Group>
            )}
          </nav>
        )}
        <div className="space-y-1 border-t border-border pt-2">
          <Button variant="outline" size="sm" className="w-full" disabled={out.isPending} onClick={() => out.mutate()}>
            {out.isPending ? 'Signing out…' : 'Sign out'}
          </Button>
          {out.isError && (
            <p role="alert" className="text-xs text-status-error">
              Sign-out failed: {out.error.message}
            </p>
          )}
        </div>
      </div>
    </div>
  )
}
