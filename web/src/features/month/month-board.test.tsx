import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import { makeQueryClient } from '@/lib/query'
import { installApi, ok } from '@/test/fake-api'
import { ApiError } from '@/lib/api'
import { MonthBoard, OFFLINE_NOTE_ID, STALE_AFTER_MS, statusSummary, type MonthBoardProps } from './month-board'
import { item, payment, summary } from './fixtures'

const T0 = Date.parse('2026-11-21T12:00:00Z')

function board(over: Partial<MonthBoardProps> = {}) {
  const props: MonthBoardProps = {
    month: '2026-11', status: 'success', data: summary([]), error: null, isFetching: false, dataUpdatedAt: T0, now: T0,
    onMonthChange: vi.fn(), onRetry: vi.fn(), onRecord: vi.fn(), ...over,
  }
  render(<QueryClientProvider client={makeQueryClient()}><MonthBoard {...props} /></QueryClientProvider>)
  return props
}

describe('MonthBoard offline', () => {
  it('disables every record action and Refresh while offline, each with its visible reason', () => {
    board({ data: summary([item('a'), item('b')]), offline: true })
    const rows = screen.getAllByRole('button', { name: /^Record payment for / })
    expect(rows).toHaveLength(2)
    for (const b of rows) {
      expect(b).toBeDisabled()
      // The note itself lives on the page (MonthPage), which renders it with this id.
      expect(b).toHaveAttribute('aria-describedby', OFFLINE_NOTE_ID)
    }
    const refresh = screen.getByRole('button', { name: 'Refresh' })
    expect(refresh).toBeDisabled()
    expect(refresh).toHaveAccessibleDescription(/Refresh is unavailable while you are offline/)
    expect(screen.getByText('Offline')).toBeInTheDocument()
  })

  it('keeps the actions and Refresh enabled while online', () => {
    board({ data: summary([item('a')]) })
    expect(screen.getByRole('button', { name: /^Record payment for / })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled()
  })
})

describe('MonthBoard amounts and dates', () => {
  it('groups thousands with a fixed narrow no-break space and shows ISO dates that never wrap', () => {
    const NNBSP = String.fromCharCode(0x202f)
    board({ data: summary([item('a', { amount: { amount: '1124.19', currency: 'EUR' } })]) })
    const shown = [...document.querySelectorAll('[data-amount]')].map((e) => e.textContent)
    expect(shown).toContain(`1${NNBSP}124.19${String.fromCharCode(0xa0)}EUR`)
    const time = screen.getAllByText('2026-11-25')[0]!
    expect(time.tagName).toBe('TIME')
    expect(time).toHaveClass('whitespace-nowrap')
  })
})

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

  it('the month controls are a labelled group, not a navigation landmark', () => {
    board({ data: summary([]) })
    expect(screen.getByRole('group', { name: 'Month' })).toBeInTheDocument()
    expect(screen.queryByRole('navigation', { name: 'Month' })).toBeNull()
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
    expect(screen.getByRole('alert')).toHaveTextContent(/last data received, fetched just now; it may be out of date/)
    expect(screen.getByText('Rent a')).toBeInTheDocument()
  })

  it('refreshing shows a status and disables the refresh button', () => {
    board({ data: summary([item('a')]), isFetching: true })
    expect(screen.getByText('Refreshing…')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeDisabled()
  })

  it('says bank data age is not available instead of implying it is fresh', () => {
    board({ data: summary([item('a')]) })
    expect(screen.getByText(/No bank data connected yet/)).toBeInTheDocument()
    expect(screen.queryByText(/endpoint/)).toBeNull()
  })
})

describe('MonthBoard design follow-ups', () => {
  const rows = () => [
    item('a', { payment: payment({ status: 'overdue', timing: 'overdue' }) }),
    item('b', { payment: payment({ status: 'overdue', timing: 'overdue' }) }),
    item('c', { payment: payment({ status: 'due', timing: 'due' }) }),
    item('d'),
    item('e', { payment: payment({ status: 'paid', evidence_count: 1, manual_evidence_count: 1 }) }),
    item('g', { payment: payment({ status: 'paid', evidence_count: 1, bank_evidence_count: 1 }) }),
    item('h', { payment: payment({ status: 'paid', evidence_count: 2, bank_evidence_count: 1, manual_evidence_count: 1 }) }),
    item('f', { payment: payment({ status: 'paid', evidence_count: 0, override: { status: 'paid', reason: 'cash', set_at: '2026-11-02T00:00:00Z', applied: true } }) }),
  ]

  it('summarises the rows by status so the list scans by status first', () => {
    board({ data: summary(rows()) })
    expect(screen.getByTestId('status-summary')).toHaveTextContent('2 overdue · 1 due today · 1 upcoming · 4 paid')
  })

  it('counts a partly paid row with an overdue remainder the way the server counts overdue occurrences', () => {
    const late = item('a', { payment: payment({ status: 'partially_paid', timing: 'overdue' }) })
    board({ data: summary([late, item('b', { payment: payment({ status: 'partially_paid', timing: 'upcoming' }) })]) })
    expect(screen.getByTestId('status-summary')).toHaveTextContent('2 partly paid (1 with an overdue remainder)')
  })

  it('marks where a settled row came from: manual payment or hand-set status (an override replaces the payment marker); no marker on an unpaid row without a hand-set status', () => {
    board({ data: summary(rows()) })
    const row = (name: string) => screen.getByText(name).closest('li')!
    expect(within(row('Rent e')).getByText('Manual payment')).toBeInTheDocument()
    expect(within(row('Rent g')).getByText('Bank match')).toBeInTheDocument()
    expect(within(row('Rent g')).queryByText('Manual payment')).toBeNull()
    expect(within(row('Rent h')).getByText('Bank match and manual payment')).toBeInTheDocument()
    expect(within(row('Rent f')).getByText('Set by hand')).toBeInTheDocument()
    expect(within(row('Rent f')).getAllByText(/Set by hand/)).toHaveLength(1)
    expect(within(row('Rent f')).queryByText('Manual payment')).toBeNull()
    expect(within(row('Rent d')).queryByText(/Manual payment|Set by hand|Bank match/)).toBeNull()
  })

  it('makes the row action quieter than the status badge', () => {
    board({ data: summary([item('a')]) })
    const btn = screen.getByRole('button', { name: /^Record payment for Rent a/ })
    expect(btn.className).not.toContain('border-border-control') // not the outline variant
    expect(btn.className).toContain('hover:bg-muted') // ghost
    expect(screen.getByText('Upcoming').closest('[data-status]')).toHaveAttribute('data-tier', 'default')
  })

  it('labels the overdue count in words and emphasises a non-zero overdue amount', () => {
    board({ data: summary([item('a'), item('b')]) })
    expect(screen.getByText('1 overdue occurrence')).toBeInTheDocument()
    expect(screen.queryByText('(1)')).toBeNull()
    const amount = screen.getAllByText('10.00 EUR', { selector: '[data-amount]' }).map((e) => e.closest('dd')).find(Boolean)!
    expect(amount.className).toContain('font-semibold')
    expect(screen.getByText('Overdue (past original due date)').className).toContain('font-semibold')
  })

  it('shows the form in place under the row that opened it', () => {
    board({ data: summary([item('a'), item('b')]), formFor: 'b', form: <form aria-label="Record a payment" /> })
    const form = screen.getByRole('form', { name: 'Record a payment' })
    expect(form.closest('li')).toBe(screen.getByText('Rent b').closest('li'))
  })

  it('keeps the month heading a fixed width so the month buttons do not shift', () => {
    board({ month: '2025-06' })
    expect(screen.getByRole('heading', { level: 1, name: 'June 2025' }).className).toContain('w-48')
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

  it('a not-recorded occurrence is muted, never overdue, explained and can still be paid', () => {
    board({ data: summary([item('a', { payment: payment({ status: 'not_recorded', derived_status: 'not_recorded', timing: 'not_recorded' }), due_date: '2026-11-05' })]) })
    expect(screen.getByText('Not recorded').closest('[data-status="not_recorded"]')?.querySelector('svg')).not.toBeNull()
    expect(screen.queryByText('Overdue')).toBeNull()
    expect(screen.getByText(/Due before this commitment was created/)).toBeTruthy()
    expect(screen.getByRole('button', { name: /Record payment/ })).toBeTruthy()
    expect(statusSummary([item('a', { payment: payment({ status: 'not_recorded', timing: 'not_recorded' }) })])).toBe('1 not recorded')
  })

  it('a paid occurrence within the match tolerance shows its shortfall as a detail', () => {
    board({ data: summary([item('a', { payment: payment({ status: 'paid', derived_status: 'paid', timing: null, outstanding_amount: { amount: '0.00', currency: 'EUR' }, shortfall_amount: { amount: '0.50', currency: 'EUR' } }) })]) })
    expect(screen.getByText(/Paid within the commitment's tolerance/).textContent).toMatch(/0\.50\sEUR.*short/)
    expect(document.querySelector('[data-status="paid"]')).not.toBeNull()
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
    expect(within(totals).getByText('5.00 USD')).toBeInTheDocument()
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

describe('a manual record replaced by a bank match (D45)', () => {
  it('shows it apart from the payments that count, with ISO date, a narrow no-break space in the amount and the reason it is not counted', async () => {
    installApi({
      'GET /api/v1/occurrences/a/manual-payments': ok({
        items: [
          { id: 'mp1', occurrence_id: 'a', amount: { amount: '1234.50', currency: 'EUR' }, paid_on: '2026-11-20', note: null, version: 1, replaced_by_match_id: 'm1' },
          { id: 'mp2', occurrence_id: 'a', amount: { amount: '5.00', currency: 'EUR' }, paid_on: '2026-11-19', note: null, version: 1, replaced_by_match_id: null },
        ],
        next_cursor: null,
      }),
    })
    board({ data: summary([item('a', { payment: payment({ status: 'paid', bank_evidence_count: 1, evidence_count: 1 }) })]) })
    const box = await screen.findByTestId('replaced-records')
    expect(within(box).getAllByRole('listitem')).toHaveLength(1)
    expect(box).toHaveTextContent('Manual record replaced')
    expect(box).toHaveTextContent('2026-11-20')
    expect(box.textContent).toContain('1' + String.fromCharCode(0x202f) + '234.50')
    expect(box).toHaveTextContent('kept in history and not counted')
    expect(box).not.toHaveTextContent('2026-11-19')
  })

  it('still lists a replaced record when no bank payment counts any more (after a Reverse)', async () => {
    installApi({
      'GET /api/v1/occurrences/a/manual-payments': ok({
        items: [{ id: 'mp1', occurrence_id: 'a', amount: { amount: '10.00', currency: 'EUR' }, paid_on: '2026-11-20', note: null, version: 1, replaced_by_match_id: 'm1' }],
        next_cursor: null,
      }),
    })
    board({ data: summary([item('a', { payment: payment({ status: 'due' }) })]) })
    expect(await screen.findByTestId('replaced-records')).toHaveTextContent('Manual record replaced')
  })

  it('shows nothing when the records cannot be read', async () => {
    installApi({})
    board({ data: summary([item('a', { payment: payment({ status: 'paid', bank_evidence_count: 1, evidence_count: 1 }) })]) })
    await new Promise((r) => setTimeout(r, 50))
    expect(screen.queryByTestId('replaced-records')).not.toBeInTheDocument()
  })
})
