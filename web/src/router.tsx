import {
  createRootRouteWithContext,
  createRoute,
  createRouter,
  redirect,
  Link,
} from '@tanstack/react-router'
import type { QueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { AppShell } from '@/components/app-shell'
import { DesignPage } from '@/routes-design'
import { MonthPage } from '@/features/month/month-page'
import { MatchesPage } from '@/features/matches/matches-page'
import { isMonth, monthOf } from '@/features/month/month'
import { OutlookPage } from '@/features/outlook/outlook-page'
import { CommitmentsPage } from '@/features/organisation/commitments-page'
import { OrganisationPage } from '@/features/organisation/organisation-page'
import { RatesPage } from '@/features/rates/rates-page'
import { RequireSession } from '@/features/auth/require-session'
import { SignInPage } from '@/features/auth/sign-in'
import { SecurityPage } from '@/features/account/security'
import { SessionsPage } from '@/features/account/sessions'
import { MyTokensPage } from '@/features/tokens/my-tokens'
import { AdminTokensPage } from '@/features/tokens/admin-tokens'
import { UsersPage } from '@/features/admin/users'
import { RolesPage } from '@/features/admin/roles'
import { ConnectionsPage } from '@/features/banking/connections-page'
import { ConsentCallbackPage } from '@/features/banking/callback-page'

export interface RouterContext {
  queryClient: QueryClient
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
  component: AppShell,
  notFoundComponent: NotFound,
})

const signInRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/sign-in',
  validateSearch: (s: Record<string, unknown>): { redirect?: string; oidc_error?: string; recovery_error?: string } => ({
    redirect: typeof s.redirect === 'string' ? s.redirect : undefined,
    oidc_error: typeof s.oidc_error === 'string' ? s.oidc_error : undefined,
    recovery_error: typeof s.recovery_error === 'string' ? s.recovery_error : undefined,
  }),
  component: function SignIn() {
    const s = signInRoute.useSearch()
    return <SignInPage redirect={s.redirect} oidcError={s.oidc_error} recoveryError={s.recovery_error} />
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

// Bank connections (B4). The callback is where a bank sends the user back after consent: its address carries a
// single-use state and code, which the page uses once and removes from the address.
const connectionsRoute = createRoute({ getParentRoute: () => appRoute, path: '/connections', component: ConnectionsPage })
const connectionsCallbackRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/connections/callback',
  validateSearch: (s: Record<string, unknown>): { state?: string; code?: string; error?: string } => ({
    state: typeof s.state === 'string' ? s.state : undefined,
    code: typeof s.code === 'string' ? s.code : undefined,
    error: typeof s.error === 'string' ? s.error : undefined,
  }),
  component: function ConsentCallback() {
    return <ConsentCallbackPage redirect={connectionsCallbackRoute.useSearch()} />
  },
})

// The outlook (FX3): ?through=YYYY-MM and ?currency=XXX are optional; the server defaults them.
const outlookRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/outlook',
  validateSearch: (s: Record<string, unknown>): { through?: string; currency?: string } => ({
    ...(isMonth(s.through) ? { through: s.through } : {}),
    ...(typeof s.currency === 'string' && /^[A-Z]{3}$/.test(s.currency) ? { currency: s.currency } : {}),
  }),
  component: function OutlookRoute() {
    const { through, currency } = outlookRoute.useSearch()
    return <OutlookPage through={through} currency={currency} />
  },
})

const routeTree = rootRoute.addChildren([
  signInRoute,
  designRoute,
  appRoute.addChildren([
    indexRoute,
    monthRoute,
    child('/matches', MatchesPage),
    connectionsRoute,
    connectionsCallbackRoute,
    outlookRoute,
    child('/rates', RatesPage),
    child('/organisation', OrganisationPage),
    child('/commitments', CommitmentsPage),
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
