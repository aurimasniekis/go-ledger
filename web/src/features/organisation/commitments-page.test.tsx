import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '@/lib/api'
import { ALL_GRANTS, installApi, ok, renderApp, signedIn } from '@/test/fake-api'
import { assignments, def } from './fixtures'

afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

const page = (items: unknown[]) => ok({ items, next_cursor: null })

describe('organise commitments page', () => {
  it('lists every commitment, also one with no occurrence this month, and organises it from there', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...signedIn(undefined, ALL_GRANTS),
      'GET /api/v1/commitments': page([
        { id: 'c-a', name: 'Rent', kind: 'bill', amount: { amount: '850.00', currency: 'EUR' }, schedule: { version: 1, frequency: 'monthly', interval: 1, day: 5 }, start_date: '2026-01-05', end_date: null },
        { id: 'c-ann', name: 'Insurance', kind: 'bill', schedule: { version: 1, frequency: 'annual', day: 5, month: 3 } },
        { id: 'c-old', name: 'Old gym', kind: 'subscription' },
      ]),
      'GET /api/v1/organisation/commitment/c-old': ok(assignments()),
      'GET /api/v1/categories': page([def('category', 'F00', 'Food')]),
      'GET /api/v1/labels': page([]),
      'GET /api/v1/buckets': page([]),
    })
    renderApp('/commitments')
    const list = await screen.findByRole('list', { name: 'Commitments' })
    expect(within(list).getAllByRole('listitem')).toHaveLength(3)
    const rent = within(list).getAllByRole('listitem')[0]!
    expect(rent).toHaveTextContent('850.00')
    expect(rent).toHaveTextContent('EUR')
    expect(rent).toHaveTextContent('Monthly on day 5')
    expect(rent).toHaveTextContent('2026-01-05')
    expect(within(list).getAllByRole('listitem')[1]).toHaveTextContent('Yearly on 5 March')
    // Nothing about organisation is read until a row's Organise is opened.
    expect(calls.some((c) => c.path.startsWith('/api/v1/organisation/'))).toBe(false)
    await user.click(within(list).getByRole('button', { name: 'Organise Old gym' }))
    const panel = await screen.findByRole('group', { name: 'Organisation of Old gym' })
    expect(await within(panel).findByText(/No category yet/)).toBeInTheDocument()
    expect(within(panel).getByLabelText('Category for Old gym')).toBeInTheDocument()
  })

  it('does not link the page from the menu without commitments:read', async () => {
    installApi({ ...signedIn(undefined, ['organisation:read']), 'GET /api/v1/occurrences/summary': { json: { month: '2026-11', as_of: '2026-11-21', items: [], totals: [], freshness: { computed_at: '2026-11-21T12:00:00Z', occurrences_changed_at: null, bank: { available: false, note: '' } } } } })
    renderApp('/account/security')
    await userEvent.click(await screen.findByRole('button', { name: 'Account' }))
    expect(screen.getByRole('link', { name: 'Categories, labels, buckets' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Organise commitments' })).toBeNull()
  })

  it('is linked from the account menu for a role that can read organisation', async () => {
    installApi({ ...signedIn(undefined, ALL_GRANTS), 'GET /api/v1/commitments': page([]) })
    renderApp('/commitments')
    expect(await screen.findByText('No commitments yet')).toBeInTheDocument()
    expect(screen.getAllByRole('link', { name: 'Organise commitments', hidden: true }).length).toBeGreaterThan(0)
  })
})
