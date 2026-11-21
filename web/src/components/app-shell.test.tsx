import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '@/lib/api'
import { fail, installApi, renderApp, session, signedIn } from '@/test/fake-api'

afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

/** Heading levels in document order: the outline must start at 1 and never skip a level going down. */
function outline() {
  return screen.getAllByRole('heading').map((h) => Number(h.tagName[1]))
}
function expectSoundOutline() {
  const levels = outline()
  expect(levels.filter((l) => l === 1)).toHaveLength(1)
  expect(levels[0]).toBe(1)
  levels.slice(1).forEach((l, i) => expect(l - levels[i]!, `heading ${i + 1} follows level ${levels[i]}`).toBeLessThanOrEqual(1))
}

describe('shell while signed out', () => {
  it('shows the brand and the theme switch but no navigation or account menu', async () => {
    installApi({ 'GET /api/v1/auth/session': fail(401, 'unauthenticated', 'Sign in.') })
    renderApp('/sign-in')
    expect(await screen.findByRole('heading', { level: 1, name: 'Sign in' })).toBeInTheDocument()
    expect(screen.getByLabelText('Theme')).toBeInTheDocument()
    expect(screen.queryByRole('navigation')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Account' })).toBeNull()
    expect(screen.queryByRole('link', { name: 'Monthly view' })).toBeNull()
    expect(screen.getByRole('link', { name: 'Skip to content' })).toHaveAttribute('href', '#main')
  })
})

describe('shell while signed in', () => {
  const open = async (grants?: string[]) => {
    installApi({ ...signedIn(session(), grants), 'GET /api/v1/occurrences/summary': { json: { month: '2026-11', as_of: '2026-11-21', items: [], totals: [], freshness: { computed_at: '2026-11-21T12:00:00Z', occurrences_changed_at: null, bank: { available: false, note: '' } } } } })
    renderApp('/month?month=2026-11')
    return screen.findByRole('button', { name: 'Account' })
  }

  it('keeps the finance pages primary and groups account and administration pages under one menu', async () => {
    const button = await open()
    const primary = screen.getByRole('navigation', { name: 'Primary' })
    expect(within(primary).getAllByRole('link').map((l) => l.getAttribute('aria-label') ?? l.textContent)).toEqual(['Monthly view', 'Outlook', 'Payment matches'])
    expect(within(primary).getByRole('link', { name: 'Monthly view' })).toHaveAttribute('aria-current', 'page')
    expect(button).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('link', { name: 'Security' })).toBeNull() // hidden until opened
    await userEvent.click(button)
    expect(button).toHaveAttribute('aria-expanded', 'true')
    const account = screen.getByRole('navigation', { name: 'Account' })
    const groups = within(account).getAllByRole('list')
    expect(groups[0]).toHaveAccessibleName('Your account')
    expect(within(groups[0]!).getAllByRole('link').map((l) => l.textContent)).toEqual(['Security', 'Sessions', 'Your API tokens'])
    expect(groups[1]).toHaveAccessibleName('Administration')
    expect(within(groups[1]!).getAllByRole('link').map((l) => l.textContent)).toEqual(['Users', 'Roles', 'All API tokens'])
    expect(within(account).queryByRole('heading')).toBeNull() // group labels are not headings: the outline stays the page's
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeInTheDocument()
  })

  it('closes the Account menu when a link to the page already shown is followed, and keeps focus on its button', async () => {
    // Navigating to another page closes the menu by itself (it is open only on the path it was opened on), so only a
    // link to the current page exercises the close.
    installApi({ ...signedIn(), 'GET /api/v1/occurrences/summary': { json: { month: '2026-11', as_of: '2026-11-21', items: [], totals: [], freshness: { computed_at: '2026-11-21T12:00:00Z', occurrences_changed_at: null, bank: { available: false, note: '' } } } } })
    renderApp('/account/security')
    const button = await screen.findByRole('button', { name: 'Account' })
    await userEvent.click(button)
    expect(button).toHaveAttribute('aria-expanded', 'true')
    await userEvent.click(within(screen.getByRole('navigation', { name: 'Account' })).getByRole('link', { name: 'Security' }))
    await waitFor(() => expect(button).toHaveAttribute('aria-expanded', 'false'))
    expect(button).toHaveFocus()
  })

  it('offers only the pages the grants allow', async () => {
    await userEvent.click(await open(['commitments:read']))
    expect(screen.getByRole('link', { name: 'Security' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Users' })).toBeNull()
    expect(screen.queryByText('Administration')).toBeNull()
  })

  it('closes with Escape and returns focus to the button', async () => {
    const button = await open()
    await userEvent.click(button)
    screen.getByRole('link', { name: 'Security' }).focus()
    await userEvent.keyboard('{Escape}')
    expect(button).toHaveAttribute('aria-expanded', 'false')
    expect(button).toHaveFocus()
  })

  it('closes on a click elsewhere, when focus tabs out, and after choosing a page', async () => {
    const button = await open()
    await userEvent.click(button)
    await userEvent.click(document.body)
    expect(button).toHaveAttribute('aria-expanded', 'false')
    await userEvent.click(button)
    await userEvent.click(screen.getByRole('link', { name: 'Monthly view' })) // focus moves outside the menu
    expect(button).toHaveAttribute('aria-expanded', 'false')
    await userEvent.click(button)
    await userEvent.click(screen.getByRole('link', { name: 'Sessions' }))
    await screen.findByRole('heading', { level: 1, name: 'Sessions' })
    expect(button).toHaveAttribute('aria-expanded', 'false')
  })

  it('is reachable by keyboard: Tab to the button, Enter opens, the first link is next', async () => {
    const button = await open()
    button.focus()
    await userEvent.keyboard('{Enter}')
    await userEvent.tab()
    expect(screen.getByRole('link', { name: 'Security' })).toHaveFocus()
  })

  it('has a sound heading outline on the month page', async () => {
    await open()
    await screen.findByRole('heading', { level: 1, name: 'November 2026' })
    await screen.findByRole('heading', { name: /No commitments fall due/ })
    expectSoundOutline()
  })

  it('shows an offline banner that keeps the page and announces the change', async () => {
    await open()
    await screen.findByRole('heading', { level: 1, name: 'November 2026' })
    act(() => {
      window.dispatchEvent(new Event('offline'))
    })
    Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
    act(() => {
      window.dispatchEvent(new Event('offline'))
    })
    const banner = await screen.findByText(/You are offline\. Pages keep the data/)
    expect(banner.closest('[role="alert"]')).toHaveTextContent('Disconnected')
    expect(screen.getByRole('heading', { level: 1, name: 'November 2026' })).toBeInTheDocument()
    await waitFor(() => expect(document.querySelector('[data-announcer="assertive"]')).toHaveTextContent('You are offline'))
    Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
    act(() => {
      window.dispatchEvent(new Event('online'))
    })
    await waitFor(() => expect(screen.queryByText(/You are offline\. Pages keep/)).toBeNull())
    await waitFor(() => expect(document.querySelector('[data-announcer="polite"]')).toHaveTextContent('Connection restored.'))
  })
})
