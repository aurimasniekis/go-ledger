import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryHistory } from '@tanstack/react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from '@/App'
import { installApi, signedIn } from '@/test/fake-api'

beforeEach(() => {
  installApi(signedIn())
})
afterEach(() => vi.unstubAllGlobals())

describe('App shell', () => {
  it('leads / to the monthly view, with no placeholder overview', async () => {
    render(<App history={createMemoryHistory({ initialEntries: ['/'] })} />)
    // The page title (h1) is the month; "Monthly view" names the page in the navigation and above the title.
    expect(await screen.findByRole('heading', { level: 1, name: /^\w+ \d{4}$/ })).toBeInTheDocument()
    expect(within(screen.getByRole('navigation', { name: 'Primary' })).getByRole('link', { name: 'Monthly view' })).toBeInTheDocument()
    expect(screen.queryByText(/will appear here/)).toBeNull()
    expect(screen.queryByRole('link', { name: 'Overview' })).toBeNull()
  })

  it('renders a not-found page for an unknown route', async () => {
    render(<App history={createMemoryHistory({ initialEntries: ['/nope'] })} />)
    expect(await screen.findByRole('heading', { name: 'Page not found' })).toBeInTheDocument()
  })

  it('lets the theme be chosen with the keyboard-reachable select', async () => {
    render(<App history={createMemoryHistory({ initialEntries: ['/'] })} />)
    await userEvent.selectOptions(await screen.findByLabelText('Theme'), 'dark')
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(localStorage.getItem('ledger.theme')).toBe('dark')
  })

  it('reaches the theme select by Tab and keeps a visible-focus class', async () => {
    render(<App history={createMemoryHistory({ initialEntries: ['/'] })} />)
    const select = await screen.findByLabelText('Theme')
    for (let i = 0; i < 15 && document.activeElement !== select; i++) await userEvent.tab()
    expect(select).toHaveFocus()
    expect(select.className).toContain('focus-visible:outline-ring')
    expect(select.className).toContain('border-border-control')
  })

  it('renders the design page with every status', async () => {
    render(<App history={createMemoryHistory({ initialEntries: ['/design'] })} />)
    expect(await screen.findByRole('heading', { level: 1, name: 'Design system' })).toBeInTheDocument()
    for (const l of ['Paid', 'Upcoming', 'Overdue', 'Deferred', 'Stale', 'Error']) expect(screen.getAllByText(l).length).toBeGreaterThan(0)
  })
})
