import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api'
import { MonthBoard, STALE_AFTER_MS, type MonthBoardProps } from './month-board'
import { item, payment, summary } from './fixtures'

const T0 = Date.parse('2026-11-21T12:00:00Z')

function board(over: Partial<MonthBoardProps> = {}) {
  const props: MonthBoardProps = {
    month: '2026-11', status: 'success', data: summary([]), error: null, isFetching: false, dataUpdatedAt: T0, now: T0,
    onMonthChange: vi.fn(), onRetry: vi.fn(), onRecord: vi.fn(), ...over,
  }
  render(<MonthBoard {...props} />)
  return props
}

describe('MonthBoard states', () => {
  it('loading: announces what is loading and shows no figures', () => {
    board({ status: 'pending', data: undefined })
    const s = screen.getByRole('status')
    expect(s).toHaveAttribute('aria-busy', 'true')
    expect(s).toHaveTextContent('Loading November 2026')
    expect(screen.queryByText('Recorded commitments, by currency')).toBeNull()
  })

  it('empty: says nothing falls due, with no totals and no stale claim', () => {
    board({ data: summary([]) })
    expect(screen.getByRole('heading', { name: 'No commitments fall due in November 2026' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Recorded commitments, by currency' })).toBeNull()
    expect(screen.queryByText(/Stale/)).toBeNull()
  })

  it('stale: data older than the threshold is labelled with text, an icon and its age', () => {
    board({ data: summary([item('a')]), now: T0 + STALE_AFTER_MS + 120_000 })
    const badge = screen.getByText(/Stale, fetched 7 minutes ago/)
    expect(badge.closest('[data-status="stale"]')?.querySelector('svg[aria-hidden="true"]')).not.toBeNull()
    expect(screen.queryByText('Refresh failed')).toBeNull()
  })

  it('fresh data carries no stale badge but still says when it was fetched', () => {
    board({ data: summary([item('a')]), now: T0 + 5_000 })
    expect(screen.queryByText(/Stale/)).toBeNull()
    expect(screen.getByText(/fetched/)).toHaveTextContent('just now')
  })

  it('failed with nothing to show: an alert with the server message, a request reference and a retry', async () => {
    const props = board({ status: 'error', data: undefined, error: new ApiError(500, { message: 'database is busy', request_id: 'req_1' }) })
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent('Could not load this month')
    expect(alert).toHaveTextContent('database is busy')
    expect(alert).toHaveTextContent('req_1')
    await userEvent.click(within(alert).getByRole('button', { name: 'Try again' }))
    expect(props.onRetry).toHaveBeenCalled()
  })

  it('failed refresh keeps the last data, marks it stale and says it may be out of date', () => {
    board({ status: 'error', data: summary([item('a')]), error: new ApiError(503, { message: 'unavailable' }) })
    expect(screen.getByText('Refresh failed')).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent(/last data received, from 2026-11-21 12:00 UTC; it may be out of date/)
    expect(screen.getByText('Rent a')).toBeInTheDocument()
  })

  it('refreshing shows a status and disables the refresh button', () => {
    board({ data: summary([item('a')]), isFetching: true })
    expect(screen.getByText('Refreshing…')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeDisabled()
  })

  it('says bank data age is not available instead of implying it is fresh', () => {
    board({ data: summary([item('a')]) })
    expect(screen.getByText(/Bank data age: not shown/)).toBeInTheDocument()
  })
})

describe('MonthBoard content', () => {
  it('shows original due date, amount with currency and a text+icon status per occurrence', () => {
    board({
      data: summary([
        item('a', { payment: payment({ status: 'overdue', timing: 'overdue', derived_status: 'overdue' }), due_date: '2026-11-05' }),
        item('b', { payment: payment({ status: 'due', timing: 'due', derived_status: 'due' }) }),
        item('c', { payment: payment({ status: 'paid', paid_amount: { amount: '10.00', currency: 'EUR' }, outstanding_amount: { amount: '0.00', currency: 'EUR' } }) }),
      ]),
    })
    const rows = within(screen.getByRole('heading', { name: 'Occurrences' }).closest('section')!).getAllByRole('listitem')
    expect(rows).toHaveLength(3)
    expect(rows[0]).toHaveTextContent('Original due date 2026-11-05')
    expect(rows[0]).toHaveTextContent('10.00 EUR')
    for (const [row, label] of [[rows[0], 'Overdue'], [rows[1], 'Due today'], [rows[2], 'Paid']] as const) {
      const badge = within(row!).getByText(label).closest('[data-status]')!
      expect(badge.querySelector('svg[aria-hidden="true"]')).not.toBeNull()
    }
  })

  it('a partly paid occurrence past its due date shows the overdue timing as text and an icon, with the grace note', () => {
    board({ data: summary([item('a', { payment: payment({ status: 'partially_paid', timing: 'overdue', paid_amount: { amount: '4.00', currency: 'EUR' },
      outstanding_amount: { amount: '6.00', currency: 'EUR' }, grace_until: '2026-11-30', within_grace: true }) })]) })
    expect(screen.getByText('Partly paid')).toBeInTheDocument()
    const badge = screen.getByText('Remainder overdue')
    expect(badge.closest('[data-status="overdue"]')?.querySelector('svg[aria-hidden="true"]')).not.toBeNull()
    expect(screen.getByText(/within documented grace until/)).toBeInTheDocument()
  })

  it('a partly paid occurrence due today says the remainder is due today', () => {
    board({ data: summary([item('a', { payment: payment({ status: 'partially_paid', timing: 'due' }) })]) })
    expect(screen.getByText('Remainder due today').closest('[data-status="due"]')?.querySelector('svg')).not.toBeNull()
  })

  it('a cancelled occurrence shows "Cancelled", never overdue, and offers no payment', () => {
    board({ data: summary([item('a', { payment: payment({ status: 'cancelled', derived_status: 'cancelled', timing: null }), due_date: '2026-11-05' })]) })
    expect(screen.getByText('Cancelled').closest('[data-status="cancelled"]')).not.toBeNull()
    expect(screen.queryByText('Overdue')).toBeNull()
    expect(screen.queryByRole('button', { name: /Record payment/ })).toBeNull()
  })

  it('a partly paid occurrence not yet late shows no overdue text', () => {
    board({ data: summary([item('a', { payment: payment({ status: 'partially_paid', timing: 'upcoming' }) })]) })
    expect(screen.queryByText(/Remainder/)).toBeNull()
  })

  it('annotates an overdue occurrence within documented grace, keeping it overdue', () => {
    board({ data: summary([item('a', { payment: payment({ status: 'overdue', timing: 'overdue', grace_until: '2026-11-30', within_grace: true }) })]) })
    expect(screen.getByText('Overdue')).toBeInTheDocument()
    expect(screen.getByText(/within documented grace until/)).toHaveTextContent('2026-11-30')
  })

  it('shows per-currency totals as received, with the not-a-forecast wording', () => {
    const s = summary([item('a'), item('b')])
    s.totals.push({ ...s.totals[0]!, currency: 'USD', due: { amount: '5.00', currency: 'USD' } })
    board({ data: s })
    const totals = screen.getByRole('heading', { name: 'Recorded commitments, by currency' }).closest('section')!
    expect(within(totals).getAllByRole('listitem')).toHaveLength(2)
    expect(totals).toHaveTextContent('never added together or converted')
    expect(totals).toHaveTextContent('not a forecast and not a measure of money that is safe to spend')
    expect(within(totals).getByText('5.00')).toBeInTheDocument()
  })

  it('notes evidence in another currency that is not counted', () => {
    board({ data: summary([item('a', { payment: payment({ unconverted_currencies: ['USD'] }) })]) })
    expect(screen.getByText(/Payments in USD are recorded but not counted/)).toBeInTheDocument()
  })

  it('offers a record-payment button on unpaid rows only, named for its row, reachable by keyboard', async () => {
    const props = board({
      data: summary([item('a'), item('b', { payment: payment({ status: 'paid' }) })]),
    })
    expect(screen.getAllByRole('button', { name: /Record payment for/ })).toHaveLength(1)
    await userEvent.tab()
    let hit = false
    for (let i = 0; i < 8 && !hit; i++) {
      await userEvent.tab()
      hit = document.activeElement?.getAttribute('aria-label')?.startsWith('Record payment for Rent a') ?? false
    }
    expect(hit).toBe(true)
    await userEvent.keyboard('{Enter}')
    expect(props.onRecord).toHaveBeenCalledWith(expect.objectContaining({ id: 'a' }))
  })

  it('navigates months with named buttons', async () => {
    const props = board()
    await userEvent.click(screen.getByRole('button', { name: 'Previous month, October 2026' }))
    expect(props.onMonthChange).toHaveBeenLastCalledWith('2026-10')
    await userEvent.click(screen.getByRole('button', { name: 'Next month, December 2026' }))
    expect(props.onMonthChange).toHaveBeenLastCalledWith('2026-12')
  })
})
