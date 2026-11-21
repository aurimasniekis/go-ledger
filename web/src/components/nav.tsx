import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { Link, useNavigate, useRouterState } from '@tanstack/react-router'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ChevronDown } from 'lucide-react'
import { apiFetch, setCsrfToken } from '@/lib/api'
import { api, holds, useMe, useSession } from '@/lib/auth'
import { Button } from '@/components/ui/button'

const link = 'block rounded-md px-2 py-1.5 hover:bg-muted [&.active]:font-semibold [&.active]:underline'

/**
 * A link label that is shorter below 768 px, so the header stays at most two rows from 320 px up. Below sm the links
 * have no gap and only 2 px of padding each side: the four links with an administrator's Connections need 283 of the 288 px
 * available at 320 px, so they stay on one row.
 * The accessible name is always the full one (aria-label), and the short text is one of its words (WCAG 2.5.3).
 */
function Short({ short, full }: { short: string; full: string }) {
  return (
    <>
      <span aria-hidden className="md:hidden">{short}</span>
      <span aria-hidden className="hidden md:inline">{full}</span>
    </>
  )
}

/** The finance pages: always in the header while signed in, never while signed out or confined to enrolment. */
export function PrimaryNav() {
  const session = useSession()
  const me = useMe()
  if (!session.data || session.data.two_factor.enrollment_required) return null
  return (
    <nav aria-label="Primary" className="order-last flex w-full flex-wrap gap-x-0 text-sm sm:order-none sm:w-auto sm:gap-x-2 sm:text-base [&>a]:px-0.5 sm:[&>a]:px-2">
      <Link to="/month" className={link} aria-label="Monthly view">
        <Short short="Monthly" full="Monthly view" />
      </Link>
      <Link to="/outlook" className={link}>
        Outlook
      </Link>
      <Link to="/matches" className={link} aria-label="Payment matches">
        <Short short="Matches" full="Payment matches" />
      </Link>
      {holds(me.data?.grants, 'connections:read') && (
        <Link to="/connections" className={link} aria-label="Bank connections">
          <Short short="Connections" full="Bank connections" />
        </Link>
      )}
    </nav>
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
