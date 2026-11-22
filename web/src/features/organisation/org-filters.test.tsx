import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '@/lib/api'
import { makeQueryClient } from '@/lib/query'
import { ALL_GRANTS, installApi, ok, renderApp, signedIn } from '@/test/fake-api'
import { item, summary } from '@/features/month/fixtures'
import { match, occurrence } from '@/features/matches/fixtures'
import { def } from './fixtures'

afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

const page = (items: unknown[]) => ok({ items, next_cursor: null })
const home = def('category', 'H00', 'Home')
const rent = def('category', 'R00', 'Rent', { parent_id: home.id })
const shared = def('label', 'S00', 'Shared')
const definitions = {
  'GET /api/v1/categories': page([home, rent]),
  'GET /api/v1/labels': page([shared]),
  'GET /api/v1/buckets': page([]),
}

describe('category and label filters (O3)', () => {
  it('filters the monthly view on the server and says the totals cover only the matching commitments', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...signedIn(undefined, ALL_GRANTS),
      ...definitions,
      'GET /api/v1/occurrences/summary': (r) =>
        ok(
          r.query.get('category_id')
            ? summary([item('a')], { filter: { category_ids: [r.query.get('category_id')!], label_ids: [], note: 'Filtered: only occurrences of commitments matching the category and label filter are shown and counted.' } })
            : summary([item('a'), item('b')]),
        ),
    })
    renderApp('/month?month=2026-11', makeQueryClient())
    const select = await screen.findByLabelText('Show only occurrences of commitments in category')
    await screen.findByRole('option', { name: 'Home / Rent' })
    expect(screen.queryByTestId('month-filter-note')).toBeNull()
    await user.selectOptions(select, 'Home / Rent')
    const note = await screen.findByTestId('month-filter-note')
    await waitFor(() => expect(note).toHaveTextContent('Filtered: only occurrences of commitments matching'))
    expect(note).toHaveTextContent('The totals below cover only these commitments, not the whole month.')
    const filtered = calls.filter((c) => c.path === '/api/v1/occurrences/summary' && c.query.get('category_id'))
    expect(filtered.at(-1)!.query.get('category_id')).toBe(rent.id)
    expect(filtered.at(-1)!.query.get('month')).toBe('2026-11')
  })

  it('filters the payment matches by their transaction', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...signedIn(undefined, ALL_GRANTS),
      ...definitions,
      'GET /api/v1/payment-matches': (r) => page(r.query.get('label_id') ? [] : [match('a')]),
      'GET /api/v1/occurrences/occ-a': ok(occurrence('a')),
      'GET /api/v1/commitments/c-a': ok({ id: 'c-a', name: 'Rent' }),
    })
    renderApp('/matches', makeQueryClient())
    const select = await screen.findByLabelText('Show only matches whose transaction is with label')
    await screen.findByRole('option', { name: 'Shared' })
    await user.selectOptions(select, 'Shared')
    expect(await screen.findByTestId('matches-filter-note')).toHaveTextContent('label Shared')
    await waitFor(() => expect(calls.some((c) => c.path === '/api/v1/payment-matches' && c.query.get('label_id') === shared.id && c.query.get('state') === 'suggested')).toBe(true))
  })

  it('filters the commitments to organise and has its own empty state', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...signedIn(undefined, ALL_GRANTS),
      ...definitions,
      'GET /api/v1/commitments': (r) => page(r.query.get('category_id') ? [] : [{ id: 'c-a', name: 'Rent', kind: 'bill' }]),
    })
    renderApp('/commitments', makeQueryClient())
    const select = await screen.findByLabelText('Show only commitments in category')
    await screen.findByRole('option', { name: 'Home' })
    await user.selectOptions(select, 'Home')
    expect(await screen.findByText('No commitment matches the filter')).toBeInTheDocument()
    expect(screen.getByTestId('commitments-filter-note')).toHaveTextContent('category Home')
    expect(calls.some((c) => c.path === '/api/v1/commitments' && c.query.get('category_id') === home.id)).toBe(true)
  })

  it('offers no filter without organisation:read, and sends none', async () => {
    const calls = installApi({
      ...signedIn(undefined, ['commitments:read']),
      'GET /api/v1/commitments': page([{ id: 'c-a', name: 'Rent', kind: 'bill' }]),
    })
    renderApp('/commitments', makeQueryClient())
    await screen.findByRole('list', { name: 'Commitments' })
    expect(screen.queryByLabelText(/Show only commitments in category/)).toBeNull()
    expect(calls.some((c) => c.path === '/api/v1/categories')).toBe(false)
  })
})
