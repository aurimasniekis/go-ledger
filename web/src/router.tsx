import {
  createRootRouteWithContext,
  createRoute,
  createRouter,
  redirect,
  Link,
  Outlet,
} from '@tanstack/react-router'
import type { QueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { ThemeSelect } from '@/components/theme-select'
import { DesignPage } from '@/routes-design'
import { MonthPage } from '@/features/month/month-page'
import { isMonth, monthOf } from '@/features/month/month'
import { AccountNav } from '@/features/auth/account-nav'
import { RequireSession } from '@/features/auth/require-session'
import { SignInPage } from '@/features/auth/sign-in'
import { SecurityPage } from '@/features/account/security'
import { SessionsPage } from '@/features/account/sessions'
import { MyTokensPage } from '@/features/tokens/my-tokens'
import { AdminTokensPage } from '@/features/tokens/admin-tokens'
import { UsersPage } from '@/features/admin/users'
import { RolesPage } from '@/features/admin/roles'

export interface RouterContext {
  queryClient: QueryClient
}

// Minimal shell. U1 owns the real app shell: it should take over this header, the skip link, the
// responsive navigation (AccountNav's links and sign-out belong in it) and the RequireSession layout.
function Shell() {
  return (
    <div className="min-h-screen">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:absolute focus:m-2 focus:rounded-md focus:bg-surface focus:px-3 focus:py-2"
      >
        Skip to content
      </a>
      <header className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-b border-border bg-surface px-4 py-3">
        <span className="font-semibold">Ledger</span>
        <nav aria-label="Main" className="flex flex-wrap items-center gap-2">
          <Link to="/month" className="rounded-md px-2 py-1 hover:bg-muted [&.active]:font-semibold">
            Month
          </Link>
          <AccountNav />
          <ThemeSelect />
        </nav>
      </header>
      <main id="main" tabIndex={-1} className="mx-auto max-w-5xl p-4">
        <Outlet />
      </main>
    </div>
  )
}

function NotFound() {
  return (
    <section className="space-y-3">
      <h1 className="text-2xl font-semibold">Page not found</h1>
      <p className="text-muted-foreground">This address does not match any page in Ledger.</p>
      <Button asChild>
        <Link to="/month">Go to the monthly view</Link>
      </Button>
    </section>
  )
}

const rootRoute = createRootRouteWithContext<RouterContext>()({
  component: Shell,
  notFoundComponent: NotFound,
})

const signInRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/sign-in',
  validateSearch: (s: Record<string, unknown>): { redirect?: string } => ({
    redirect: typeof s.redirect === 'string' ? s.redirect : undefined,
  }),
  component: function SignIn() {
    return <SignInPage redirect={signInRoute.useSearch().redirect} />
  },
})
const designRoute = createRoute({ getParentRoute: () => rootRoute, path: '/design', component: DesignPage })

// Everything below needs a session.
const appRoute = createRoute({ getParentRoute: () => rootRoute, id: 'app', component: RequireSession })
const child = <P extends string>(path: P, component: () => React.JSX.Element) =>
  createRoute({ getParentRoute: () => appRoute, path, component })

// The landing page is the monthly view: "/" leads straight to it (the current month).
const indexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/',
  beforeLoad: () => {
    throw redirect({ to: '/month', replace: true })
  },
})

// The month comes from the address (?month=YYYY-MM); without a valid one it is the current month in the browser's zone (limitation: the server's reporting zone may differ near a month boundary).
const monthRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/month',
  validateSearch: (s: Record<string, unknown>): { month?: string } => (isMonth(s.month) ? { month: s.month } : {}),
  component: function MonthRoute() {
    const { month } = monthRoute.useSearch()
    return <MonthPage month={month ?? monthOf(new Date())} />
  },
})

const routeTree = rootRoute.addChildren([
  signInRoute,
  designRoute,
  appRoute.addChildren([
    indexRoute,
    monthRoute,
    child('/account/security', SecurityPage),
    child('/account/sessions', SessionsPage),
    child('/account/tokens', MyTokensPage),
    child('/admin/users', UsersPage),
    child('/admin/roles', RolesPage),
    child('/admin/tokens', AdminTokensPage),
  ]),
])

export function makeRouter(context: RouterContext, history?: Parameters<typeof createRouter>[0]['history']) {
  return createRouter({ routeTree, context, history, defaultPreload: 'intent' })
}

declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof makeRouter>
  }
}
