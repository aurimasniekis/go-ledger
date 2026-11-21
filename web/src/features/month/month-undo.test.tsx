import { QueryClient } from '@tanstack/react-query'
import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '@/lib/api'
import { monthKey } from './api'
import { makeQueryClient } from '@/lib/query'
import { installApi, renderApp, signedIn, type Reply, type Req } from '@/test/fake-api'
import { item, summary } from './fixtures'

const json = (status: number, body: unknown): Reply => ({ status, json: body })
let calls: Req[]
let client: QueryClient
const qc = () => client
beforeEach(() => {
  client = makeQueryClient()
  calls = installApi({
    ...signedIn(),
    'GET /api/v1/occurrences/summary': (r) => json(200, summary([item('a')], { month: r.query.get('month') ?? '2026-11' })),
    'POST /api/v1/occurrences/a/manual-payments': () => json(201, { id: 'p1', occurrence_id: 'a', amount: { amount: '10.00', currency: 'EUR' }, paid_on: '2026-11-21', version: 1 }),
    'DELETE /api/v1/occurrences/a/manual-payments/p1': () => ({ status: 204 }),
  })
})
afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

describe('the Undo notice across months (proposal in the U1 pull request)', () => {
  const record = async () => {
    renderApp('/month?month=2026-11', client)
    await userEvent.click(await screen.findByRole('button', { name: 'Record a payment' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Save payment' }))
    return screen.findByText(/paid on/)
  }

  it('names the occurrence and its due date, so it makes sense in any month', async () => {
    const saved = await record()
    expect(saved).toHaveTextContent('for Rent a, due 2026-11-25')
    expect(saved).toHaveTextContent('in November 2026')
  })

  it('stays after moving to another month, and Undo still refreshes the month the payment was in', async () => {
    await record()
    await userEvent.click(screen.getByRole('button', { name: /Next month/ }))
    await screen.findByRole('heading', { level: 1, name: 'December 2026' })
    expect(screen.getByText(/paid on/)).toBeInTheDocument()
    await waitFor(() => expect(document.querySelector('[data-announcer="polite"]')).toHaveTextContent('Showing December 2026.'))
    const before = calls.filter((c) => c.method === 'GET' && c.query.get('month') === '2026-11').length
    const fetchesOf = (m: string) => calls.filter((c) => c.method === 'GET' && c.query.get('month') === m).length
    const decemberBefore = fetchesOf('2026-12')
    await userEvent.click(screen.getByRole('button', { name: 'Undo this payment' }))
    expect(await screen.findByText(/Payment removed/)).toHaveTextContent('November 2026 view')
    expect(calls.some((c) => c.method === 'DELETE')).toBe(true)
    // Undo acts on its own month (November), not the one on screen (December). December is the active query, so
    // invalidating everything would refetch it at once: its fetch count must not move.
    expect(qc().getQueryState(monthKey('2026-11'))?.isInvalidated).toBe(true)
    expect(fetchesOf('2026-12')).toBe(decemberBefore)
    // November is not on screen, so Undo marks it stale; going back to it fetches it again.
    await userEvent.click(screen.getByRole('button', { name: /Previous month/ }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'GET' && c.query.get('month') === '2026-11').length).toBeGreaterThan(before))
  })

  it('has no timeout: the notice stays after a long wait until it is dismissed or replaced (D37)', async () => {
    // Fake timers are installed before the payment, so any timer the notice starts is faked and would fire below.
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      await record()
      act(() => {
        vi.advanceTimersByTime(60 * 60 * 1000)
      })
      expect(screen.getByText(/paid on/)).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Dismiss' })).toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })

  it('announces the saved payment without moving focus away from the notice', async () => {
    await record()
    await waitFor(() => expect(document.querySelector('[data-announcer="polite"]')).toHaveTextContent(/Payment recorded: 10.00 EUR/))
  })
})
