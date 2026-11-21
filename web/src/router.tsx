import {
  createRootRouteWithContext,
  createRoute,
  createRouter,
  Link,
  Outlet,
} from '@tanstack/react-router'
import type { QueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { ThemeSelect } from '@/components/theme-select'
import { DesignPage } from '@/routes-design'
import { MonthPage } from '@/features/month/month-page'
import { isMonth, monthOf } from '@/features/month/month'

export interface RouterContext {
  queryClient: QueryClient
}

function Shell() {
  return (
    <div className="min-h-screen">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:absolute focus:m-2 focus:rounded-md focus:bg-surface focus:px-3 focus:py-2"
      >
        Skip to content
      </a>
      <header className="flex items-center justify-between gap-4 border-b border-border bg-surface px-4 py-3">
        <span className="font-semibold">Ledger</span>
        <nav aria-label="Main" className="flex items-center gap-2">
          <Link to="/" className="rounded-md px-2 py-1 hover:bg-muted [&.active]:font-semibold">
            Overview
          </Link>
          <Link to="/month" className="rounded-md px-2 py-1 hover:bg-muted [&.active]:font-semibold">
            Month
          </Link>
          <ThemeSelect />
        </nav>
      </header>
      <main id="main" tabIndex={-1} className="mx-auto max-w-5xl p-4">
        <Outlet />
      </main>
    </div>
  )
}

function Overview() {
  return (
    <section className="space-y-3">
      <h1 className="text-2xl font-semibold">Ledger</h1>
      <p className="text-muted-foreground">
        The monthly view of paid, upcoming and overdue commitments will appear here. Nothing is imported yet.
      </p>
    </section>
  )
}

function NotFound() {
  return (
    <section className="space-y-3">
      <h1 className="text-2xl font-semibold">Page not found</h1>
      <p className="text-muted-foreground">This address does not match any page in Ledger.</p>
      <Button asChild>
        <Link to="/">Go to the overview</Link>
      </Button>
    </section>
  )
}

const rootRoute = createRootRouteWithContext<RouterContext>()({
  component: Shell,
  notFoundComponent: NotFound,
})
const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: Overview })

const designRoute = createRoute({ getParentRoute: () => rootRoute, path: '/design', component: DesignPage })

// The month comes from the address (?month=YYYY-MM); without a valid one it is the current month in the browser's zone (limitation: the server's reporting zone may differ near a month boundary).
const monthRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/month',
  validateSearch: (s: Record<string, unknown>): { month?: string } => (isMonth(s.month) ? { month: s.month } : {}),
  component: function MonthRoute() {
    const { month } = monthRoute.useSearch()
    return <MonthPage month={month ?? monthOf(new Date())} />
  },
})

const routeTree = rootRoute.addChildren([indexRoute, monthRoute, designRoute])

export function makeRouter(context: RouterContext, history?: Parameters<typeof createRouter>[0]['history']) {
  return createRouter({ routeTree, context, history, defaultPreload: 'intent' })
}

declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof makeRouter>
  }
}
