import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installApi, renderApp, signedIn, type Reply, type Req } from '@/test/fake-api'
import type { FreshnessState, Outlook, OutlookAccount } from './types'

const PATH = '/api/v1/outlook'
const eur = (amount: string) => ({ amount, currency: 'EUR' })
const gbp = (amount: string) => ({ amount, currency: 'GBP' })

function outlook(over: Partial<Outlook> = {}): Outlook {
  return {
    as_of: '2026-11-16',
    notice: 'Outlook against recorded commitments: bank balances compared with the commitments Ledger has recorded as still due. It covers recorded commitments only, is not a forecast and does not tell you how much money is safe to spend.',
    window: { from: '2026-09', through: '2026-11', truncated: false, not_recorded_count: 0 },
    currencies: [
      { currency: 'EUR', still_due: eur('260.00'), overdue: eur('160.00'), upcoming: eur('100.00'), overdue_count: 2, unconverted_evidence_count: 0,
        balance: eur('1200.50'), balance_less_still_due: eur('940.50'), account_count: 1, stale_account_count: 0, oldest_balance_obtained_at: '2026-11-16T08:00:00Z',
        overdue_months: [{ month: '2026-09', count: 1, amount: eur('100.00') }, { month: '2026-10', count: 1, amount: eur('60.00') }],
        balance_as_of: '2026-11-15', payments_after_balance: { count: 2, amount: eur('35.00') } },
      { currency: 'GBP', still_due: gbp('7.50'), overdue: gbp('7.50'), upcoming: gbp('0.00'), overdue_count: 1, unconverted_evidence_count: 0,
        balance: gbp('-20.00'), balance_less_still_due: gbp('-27.50'), account_count: 1, stale_account_count: 1, oldest_balance_obtained_at: '2026-11-13T08:00:00Z',
        overdue_months: [{ month: '2026-11', count: 1, amount: gbp('7.50') }], balance_as_of: '2026-11-13', payments_after_balance: { count: 0, amount: gbp('0.00') } },
    ],
    balances: { shown: true, note: 'Balances are the latest each bank reported; check each account\'s freshness before relying on them.', stale_after_seconds: 86400 },
    accounts: [
      { id: 'a1', connection_id: 'c1', name: 'Main', institution: 'Bank One', currency: 'EUR', counted: true, not_counted_reason: null,
        balances: [{ type: 'booked', provider_type: 'CLBD', amount: eur('1200.50'), reference_date: '2026-11-15', obtained_at: '2026-11-16T08:00:00Z' }],
        freshness: { state: 'fresh', stale: false, last_success_at: '2026-11-16T08:00:00Z', last_attempt_at: '2026-11-16T08:00:00Z', last_error_kind: null, last_error_code: null, notice_code: null } },
      { id: 'a2', connection_id: 'c1', name: 'Pounds', institution: 'Bank One', currency: 'GBP', counted: true, not_counted_reason: null,
        balances: [{ type: 'booked', provider_type: 'CLBD', amount: gbp('-20.00'), reference_date: null, obtained_at: '2026-11-13T08:00:00Z' }],
        freshness: { state: 'stale', stale: true, last_success_at: '2026-11-13T08:00:00Z', last_attempt_at: '2026-11-13T08:00:00Z', last_error_kind: null, last_error_code: null, notice_code: null } },
      { id: 'a3', connection_id: 'c1', name: 'Wallet', institution: 'Bank One', currency: 'XXX', counted: false, not_counted_reason: 'no_balance', balances: [],
        freshness: { state: 'fresh', stale: false, last_success_at: '2026-11-16T08:00:00Z', last_attempt_at: '2026-11-16T08:00:00Z', last_error_kind: null, last_error_code: null, notice_code: null } },
    ],
    converted: {
      currency: 'EUR', complete: false, still_due: eur('260.00'), overdue: eur('160.00'), balance: eur('1200.50'), balance_less_still_due: eur('940.50'), balance_account_count: 2, stale_balance_account_count: 1,
      included: [{ currency: 'EUR', rate: null, age_seconds: null, from_cache: false, refresh_failed: false }],
      excluded: [{ currency: 'GBP', status: 'stale', reason: 'not_retrieved_recently',
        rate: { provider: 'frankfurter', source: 'ECB', base: 'GBP', quote: 'EUR', value: '1.1500', date: '2026-11-09', retrieved_at: '2026-11-10T09:00:00Z', confirmed_through: '2026-11-10' } }],
    },
    planned_outflows: { included: false, note: 'Planned outflows (deferral plans) are not part of Ledger yet; only recorded commitments are compared.' },
    freshness: { computed_at: '2026-11-16T09:00:00Z', occurrences_changed_at: '2026-11-16T08:00:00Z' },
    ...over,
  }
}

/** A copy of an account with another freshness state; stale for every state but fresh, as the server reports it. */
function withState(a: OutlookAccount, state: FreshnessState, id = a.id): OutlookAccount {
  return { ...a, id, freshness: { ...a.freshness, state, stale: state !== 'fresh' } }
}

let calls: Req[]
let responder: (r: Req) => Reply

beforeEach(() => {
  calls = installApi({ ...signedIn(), ['GET ' + PATH]: (r) => responder(r) })
})
afterEach(() => vi.unstubAllGlobals())

describe('Outlook page', () => {
  it('shows every currency on its own with the notice, and never a mixed total', async () => {
    responder = () => ({ status: 200, json: outlook() })
    renderApp('/outlook')
    expect(await screen.findByRole('heading', { level: 1, name: 'Outlook against recorded commitments' })).toBeInTheDocument()
    expect(await screen.findByTestId('outlook-notice')).toHaveTextContent('does not tell you how much money is safe to spend')
    const e = within(screen.getByTestId('outlook-EUR'))
    expect(e.getByText('Recorded commitments still due')).toBeInTheDocument()
    expect(e.getAllByText(/940.50/).length).toBeGreaterThan(0)
    const g = within(screen.getByTestId('outlook-GBP'))
    expect(g.getAllByText(/-27.50/).length).toBeGreaterThan(0)
    expect(g.getByText('1 of 1 balance not refreshed in 24 h')).toBeInTheDocument()
    // No line in the per-currency section combines EUR and GBP.
    for (const li of within(screen.getByRole('region', { name: 'By currency' })).getAllByRole('listitem')) {
      const t = li.textContent ?? ''
      expect(t.includes('EUR') && t.includes('GBP')).toBe(false)
    }
    expect(calls.find((c) => c.path === PATH)!.query.toString()).toBe('')
  })

  it('shows planned outflows beside, never instead of, what is still due: deferred in, deferred beyond and fees in words', async () => {
    const base = outlook()
    const withPlans = outlook({
      currencies: [
        { ...base.currencies[0]!, planned_outflows: { total: eur('205.00'), deferred_in: eur('100.00'), deferred_beyond: eur('60.00'), adjustments: eur('5.00'), missed_count: 2, missed_outstanding: eur('30.00') } },
        { ...base.currencies[1]!, planned_outflows: { total: gbp('7.50'), deferred_in: gbp('0.00'), deferred_beyond: gbp('0.00'), adjustments: gbp('0.00') } },
      ],
    })
    responder = () => ({ status: 200, json: withPlans })
    renderApp('/outlook')
    const e = within(await screen.findByTestId('outlook-EUR'))
    // The obligation rows are unchanged by plans.
    expect(e.getByText('Recorded commitments still due').nextElementSibling).toHaveTextContent('260.00 EUR')
    expect(e.getByText('of which overdue (past original due date)').nextElementSibling).toHaveTextContent('160.00 EUR')
    expect(e.getByTestId('outlook-planned-heading')).toHaveTextContent('Planned outflows')
    expect(e.getByTestId('outlook-planned-heading').nextElementSibling?.tagName).toBe('DD')
    expect(e.getByTestId('outlook-planned-missed')).toHaveTextContent('2 plans missed')
    expect(e.getByTestId('outlook-planned-missed').nextElementSibling).toHaveTextContent('30.00 EUR')
    expect(e.getByText('Planned to be paid through November 2026').nextElementSibling).toHaveTextContent('205.00 EUR')
    expect(e.getByTestId('outlook-planned-deferred-in')).toHaveTextContent('of which deferred in: still due, planned in a later month than its own')
    expect(e.getByTestId('outlook-planned-deferred-in').nextElementSibling).toHaveTextContent('100.00 EUR')
    expect(e.getByTestId('outlook-planned-fees')).toHaveTextContent('fees and charges still planned (not part of "still due")')
    expect(e.getByTestId('outlook-planned-beyond')).toHaveTextContent('Still due but planned after November 2026 (deferred beyond this view)')
    // A currency with no plan effect shows its planned total only, with no zero rows.
    const g = within(screen.getByTestId('outlook-GBP'))
    expect(g.getByText('Planned to be paid through November 2026').nextElementSibling).toHaveTextContent('7.50 GBP')
    expect(g.queryByTestId('outlook-planned-deferred-in')).toBeNull()
    expect(g.queryByTestId('outlook-planned-beyond')).toBeNull()
    expect(g.queryByTestId('outlook-planned-missed')).toBeNull()
  })

  it('shows the converted totals without calling a rates endpoint, and links to the rates page only with rates:read', async () => {
    responder = () => ({ status: 200, json: outlook() })
    // A role without rates:read (D-FX-R): the totals carry their own rates, so nothing here needs the rates API.
    calls = installApi({ ...signedIn(undefined, ['commitments:read', 'connections:read']), ['GET ' + PATH]: (r) => responder(r) })
    const first = renderApp('/outlook?currency=EUR')
    expect(await screen.findByRole('region', { name: 'Converted to EUR (secondary)' })).toBeInTheDocument()
    expect(screen.queryByTestId('outlook-rates-link')).toBeNull()
    expect(calls.some((c) => c.path.startsWith('/api/v1/rates'))).toBe(false)
    first.unmount()
    calls = installApi({ ...signedIn(), ['GET ' + PATH]: (r) => responder(r) })
    renderApp('/outlook?currency=EUR')
    expect(await screen.findByTestId('outlook-rates-link')).toBeInTheDocument()
  })

  it('flags the converted aggregate as incomplete and secondary, with the excluded rate', async () => {
    // GBP converts (a rate is used) while a third currency cannot: the figures are shown, with the exclusion beside them.
    const base = outlook()
    const usdStale = withState({ ...base.accounts[0]!, id: 'a4', name: 'Dollars', currency: 'USD', balances: [{ ...base.accounts[0]!.balances[0]!, amount: { amount: '10.00', currency: 'USD' } }] }, 'stale')
    responder = () => ({ status: 200, json: outlook({ accounts: [...base.accounts, usdStale], converted: { ...base.converted!, included: [{ currency: 'EUR', rate: null, age_seconds: null, from_cache: false, refresh_failed: false },
      { currency: 'USD', rate: { provider: 'frankfurter', source: 'ECB', base: 'USD', quote: 'EUR', value: '0.9000', date: '2026-11-16', retrieved_at: '2026-11-16T09:00:00Z', confirmed_through: '2026-11-16' }, age_seconds: 60, from_cache: false, refresh_failed: false }] } }) })
    renderApp('/outlook?currency=EUR')
    const conv = await screen.findByRole('region', { name: 'Converted to EUR (secondary)' })
    expect(within(conv).getByText('Incomplete')).toBeInTheDocument()
    expect(within(conv).getByText('1 of 2 balances not refreshed in 24 h')).toBeInTheDocument()
    expect(within(conv).getByText('Balance minus recorded commitments still due')).toBeInTheDocument()
    expect(within(conv).getByText(/GBP is not included: the exchange rate was last retrieved too long ago/)).toHaveTextContent('1 GBP = 1.1500 EUR, rate of 2026-11-09 from frankfurter (ECB)')
    expect(calls.find((c) => c.path === PATH)!.query.get('currency')).toBe('EUR')
  })

  it('shows account freshness and why an account is not counted', async () => {
    responder = () => ({ status: 200, json: outlook() })
    renderApp('/outlook')
    expect(within(await screen.findByTestId('account-a2')).getByText('Stale')).toBeInTheDocument()
    expect(within(screen.getByTestId('account-a3')).getByText(/Not counted: the bank has reported no balance/)).toBeInTheDocument()
  })

  it('says balances are not shown without the grant', async () => {
    responder = () => ({ status: 200, json: outlook({ balances: { shown: false, note: 'Bank balances are not shown: they need the connections:read grant.', stale_after_seconds: null }, accounts: [], converted: null,
      currencies: outlook().currencies.map((l) => ({ ...l, balance: null, balance_less_still_due: null, account_count: 0, stale_account_count: 0, oldest_balance_obtained_at: null, balance_as_of: null, payments_after_balance: null })) }) })
    renderApp('/outlook')
    expect(await screen.findByText(/need the connections:read grant/)).toBeInTheDocument()
    expect(within(screen.getByTestId('outlook-EUR')).getByText('Not shown')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Bank accounts' })).toBeNull()
  })

  it('moves the window and keeps the last data when a refresh fails', async () => {
    let n = 0
    responder = (r) => (n++ === 0 ? { status: 200, json: outlook() } : r.query.get('through') === '2026-12' ? { status: 200, json: outlook({ window: { from: '2026-09', through: '2026-12', truncated: false, not_recorded_count: 0 } }) } : { status: 500, json: { code: 'internal', message: 'boom', request_id: 'r' } })
    renderApp('/outlook')
    await screen.findByTestId('outlook-EUR')
    await userEvent.click(screen.getByRole('button', { name: 'Count commitments through December 2026' }))
    expect(await screen.findByText('December 2026')).toBeInTheDocument()
    expect(calls.filter((c) => c.path === PATH).at(-1)!.query.get('through')).toBe('2026-12')
  })

  it('shows the failed state when nothing has loaded', async () => {
    responder = () => ({ status: 400, json: { code: 'validation_failed', message: 'through is out of range', request_id: 'r1' } })
    renderApp('/outlook')
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not load the outlook')
  })

  it('shows the balance date with each balance and the payments recorded after it, never netting them in', async () => {
    responder = () => ({ status: 200, json: outlook() })
    renderApp('/outlook')
    const e = within(await screen.findByTestId('outlook-EUR'))
    expect(e.getByText(/Bank balance/)).toHaveTextContent('Bank balance, as of 2026-11-15')
    expect(e.getAllByText(/1.200.50/).length).toBeGreaterThan(0)
    const note = screen.getByTestId('outlook-after-EUR')
    expect(note).toHaveTextContent(/^2 payments \(35\.00\sEUR.*\) recorded as paid after this balance's date are already off "still due" but may not be in the bank balance yet\. The balance is shown as the bank reported it\.$/)
    // No caveat when nothing was recorded after the balance's date.
    expect(screen.queryByTestId('outlook-after-GBP')).toBeNull()
    expect(screen.getByTestId('outlook-GBP')).toHaveTextContent('Bank balance, as of 2026-11-13')
  })

  it('links each overdue month to its monthly view', async () => {
    responder = () => ({ status: 200, json: outlook() })
    renderApp('/outlook')
    const list = within(await screen.findByRole('list', { name: 'Overdue EUR by month' }))
    expect(list.getByRole('link', { name: 'September 2026' })).toHaveAttribute('href', '/month?month=2026-09')
    expect(list.getByRole('link', { name: 'October 2026' })).toHaveAttribute('href', '/month?month=2026-10')
    expect(list.getAllByRole('listitem')[1]).toHaveTextContent(/\(1 item, 60\.00\sEUR/)
  })

  it('shows only the explanation when nothing besides the reporting currency converts', async () => {
    responder = () => ({ status: 200, json: outlook({ converted: { ...outlook().converted!, excluded: [{ currency: 'GBP', status: 'missing', reason: 'no_provider', rate: null }] } }) })
    renderApp('/outlook?currency=EUR')
    const conv = await screen.findByRole('region', { name: 'Converted to EUR (secondary)' })
    expect(within(conv).getByText(/no other currency could be converted/)).toBeInTheDocument()
    expect(within(conv).getByText(/GBP: no exchange-rate provider is configured/)).toBeInTheDocument()
    expect(conv.querySelector('dl')).toBeNull()
    expect(within(conv).queryByText(/940.50/)).toBeNull()
  })

  it('says there is nothing to convert when every amount is in the reporting currency', async () => {
    responder = () => ({ status: 200, json: outlook({ converted: { ...outlook().converted!, complete: true, excluded: [] } }) })
    renderApp('/outlook?currency=EUR')
    expect(await screen.findByTestId('outlook-converted-none')).toHaveTextContent('Every amount is already in EUR')
  })

  it('says how many occurrences before tracking started are not counted, and how fresh is fresh', async () => {
    responder = () => ({ status: 200, json: outlook({ window: { from: '2025-12', through: '2026-11', truncated: false, not_recorded_count: 3 }, balances: { ...outlook().balances, stale_after_seconds: 5400 } }) })
    renderApp('/outlook')
    expect(await screen.findByTestId('outlook-not-recorded')).toHaveTextContent('3 occurrences were due before the commitments they belong to were created, so they are left out of these figures. They show as "not recorded" in the monthly view for the month they were due.')
    expect(within(screen.getByTestId('outlook-GBP')).getByText('1 of 1 balance not refreshed in 90 min')).toBeInTheDocument()
    expect(screen.getByRole('group', { name: 'Commitments counted through' })).toHaveTextContent('Everything still owed since your first commitment started (December 2025), through November 2026')
    expect(screen.getByRole('group', { name: 'Commitments counted through' }).className).not.toContain('flex-wrap')
  })

  it('words the not-fresh count by state, so an account refreshed recently but now needing reconnect is not called stale', async () => {
    const base = outlook()
    responder = () => ({ status: 200, json: outlook({
      accounts: base.accounts.map((a) => (a.id === 'a2' ? withState(a, 'reconnect_required') : a)),
    }) })
    renderApp('/outlook')
    const g = within(await screen.findByTestId('outlook-GBP'))
    expect(g.getByText('1 of 1 balance not fresh: 1 needs reconnect')).toBeInTheDocument()
    expect(g.queryByText(/not refreshed in/)).toBeNull()
  })

  it('names each state when the not-fresh balances differ, and says "not refreshed" only for stale ones', async () => {
    const base = outlook()
    const eur = base.accounts[0]!
    responder = () => ({ status: 200, json: outlook({
      currencies: base.currencies.map((l) => (l.currency === 'EUR' ? { ...l, account_count: 3, stale_account_count: 2 } : l)),
      accounts: [...base.accounts, withState(eur, 'disconnected', 'a6'), withState(eur, 'stale', 'a7')],
    }) })
    renderApp('/outlook')
    const e = within(await screen.findByTestId('outlook-EUR'))
    expect(e.getByText('2 of 3 balances not fresh: 1 disconnected, 1 stale')).toBeInTheDocument()
    expect(e.queryByText(/not refreshed in/)).toBeNull()
  })

  it('names a failed refresh and a never-refreshed account by their own state', async () => {
    const base = outlook()
    responder = () => ({ status: 200, json: outlook({
      accounts: base.accounts.map((a) => (a.id === 'a2' ? withState(a, 'failed') : a)),
    }) })
    renderApp('/outlook')
    expect(within(await screen.findByTestId('outlook-GBP')).getByText('1 of 1 balance not fresh: 1 last refresh failed')).toBeInTheDocument()
  })

  it('falls back to a plain "not fresh" when the states cannot be matched to the count', async () => {
    const base = outlook()
    responder = () => ({ status: 200, json: outlook({ accounts: base.accounts.map((a) => withState(a, 'fresh')) }) })
    renderApp('/outlook')
    const g = within(await screen.findByTestId('outlook-GBP'))
    expect(g.getByText('1 of 1 balance not fresh')).toBeInTheDocument()
    expect(g.queryByText(/not refreshed in/)).toBeNull()
  })

  it('does not refer to figures above when the reporting currency has no line', async () => {
    const base = outlook()
    responder = () => ({ status: 200, json: outlook({
      currencies: [base.currencies[1]!],
      accounts: base.accounts.filter((a) => a.id !== 'a1'),
      converted: { currency: 'USD', complete: false, still_due: { amount: '7.50', currency: 'GBP' }, overdue: { amount: '7.50', currency: 'GBP' }, balance: null, balance_less_still_due: null,
        balance_account_count: 0, stale_balance_account_count: 0, included: [], excluded: [{ currency: 'GBP', status: 'stale', reason: 'not_retrieved_recently', rate: null }] },
    }) })
    renderApp('/outlook?currency=USD')
    const conv = await screen.findByRole('region', { name: 'Converted to USD (secondary)' })
    expect(within(conv).getByText(/There are no USD amounts on this page; the others are not included:/)).toBeInTheDocument()
    expect(conv).not.toHaveTextContent('figures above')
    expect(within(conv).getByText(/GBP: the exchange rate was last retrieved too long ago/)).toBeInTheDocument()
  })

  it('says there is nothing to convert, without naming figures, when no amount is recorded at all', async () => {
    responder = () => ({ status: 200, json: outlook({ currencies: [], accounts: [], converted: { currency: 'USD', complete: true, still_due: { amount: '0.00', currency: 'USD' }, overdue: { amount: '0.00', currency: 'USD' }, balance: null, balance_less_still_due: null,
      balance_account_count: 0, stale_balance_account_count: 0, included: [], excluded: [] } }) })
    renderApp('/outlook?currency=USD')
    const none = await screen.findByTestId('outlook-converted-none')
    expect(none).toHaveTextContent('There are no amounts yet, so there is nothing to convert to USD.')
    expect(none).not.toHaveTextContent('figures above')
  })

  it('ties the payments-after-balance caveat to the balance-minus-still-due row', async () => {
    responder = () => ({ status: 200, json: outlook() })
    renderApp('/outlook')
    const e = within(await screen.findByTestId('outlook-EUR'))
    const row = e.getByText('Balance minus recorded commitments still due')
    expect(row.tagName).toBe('DT')
    expect(row).toContainElement(screen.getByTestId('outlook-after-EUR'))
    expect(row).toHaveTextContent(/2 payments \(35\.00/)
    // The GBP line has no payments after its balance, so its row has no caveat.
    expect(within(screen.getByTestId('outlook-GBP')).queryByRole('note')).toBeNull()
  })

  it('points the empty state at the Connections page', async () => {
    responder = () => ({ status: 200, json: outlook({ currencies: [], converted: null, accounts: [] }) })
    renderApp('/outlook')
    expect(await screen.findByRole('link', { name: 'connect a bank on the Connections page' })).toHaveAttribute('href', '/connections')
  })
})
