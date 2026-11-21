import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installApi, renderApp, signedIn, type Reply, type Req } from '@/test/fake-api'
import type { Outlook } from './types'

const PATH = '/api/v1/outlook'
const eur = (amount: string) => ({ amount, currency: 'EUR' })
const gbp = (amount: string) => ({ amount, currency: 'GBP' })

function outlook(over: Partial<Outlook> = {}): Outlook {
  return {
    as_of: '2026-11-16',
    notice: 'Outlook against recorded commitments: bank balances compared with the commitments Ledger has recorded as still due. It covers recorded commitments only, is not a forecast and does not tell you how much money is safe to spend.',
    window: { from: '2026-09', through: '2026-11', truncated: false },
    currencies: [
      { currency: 'EUR', still_due: eur('260.00'), overdue: eur('160.00'), upcoming: eur('100.00'), overdue_count: 2, unconverted_evidence_count: 0,
        balance: eur('1200.50'), balance_less_still_due: eur('940.50'), account_count: 1, stale_account_count: 0, oldest_balance_obtained_at: '2026-11-16T08:00:00Z' },
      { currency: 'GBP', still_due: gbp('7.50'), overdue: gbp('7.50'), upcoming: gbp('0.00'), overdue_count: 1, unconverted_evidence_count: 0,
        balance: gbp('-20.00'), balance_less_still_due: gbp('-27.50'), account_count: 1, stale_account_count: 1, oldest_balance_obtained_at: '2026-11-13T08:00:00Z' },
    ],
    balances: { shown: true, note: 'Balances are the latest each bank reported; check each account\'s freshness before relying on them.' },
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
    expect(g.getByText('1 of 1 balance not fresh')).toBeInTheDocument()
    // No line in the per-currency section combines EUR and GBP.
    for (const li of within(screen.getByRole('region', { name: 'By currency' })).getAllByRole('listitem')) {
      const t = li.textContent ?? ''
      expect(t.includes('EUR') && t.includes('GBP')).toBe(false)
    }
    expect(calls.find((c) => c.path === PATH)!.query.toString()).toBe('')
  })

  it('flags the converted aggregate as incomplete and secondary, with the excluded rate', async () => {
    responder = () => ({ status: 200, json: outlook() })
    renderApp('/outlook?currency=EUR')
    const conv = await screen.findByRole('region', { name: 'Converted to EUR (secondary)' })
    expect(within(conv).getByText('Incomplete')).toBeInTheDocument()
    expect(within(conv).getByText('1 of 2 balances not fresh')).toBeInTheDocument()
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
    responder = () => ({ status: 200, json: outlook({ balances: { shown: false, note: 'Bank balances are not shown: they need the connections:read grant.' }, accounts: [], converted: null,
      currencies: outlook().currencies.map((l) => ({ ...l, balance: null, balance_less_still_due: null, account_count: 0, stale_account_count: 0, oldest_balance_obtained_at: null })) }) })
    renderApp('/outlook')
    expect(await screen.findByText(/need the connections:read grant/)).toBeInTheDocument()
    expect(within(screen.getByTestId('outlook-EUR')).getByText('Not shown')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Bank accounts' })).toBeNull()
  })

  it('moves the window and keeps the last data when a refresh fails', async () => {
    let n = 0
    responder = (r) => (n++ === 0 ? { status: 200, json: outlook() } : r.query.get('through') === '2026-12' ? { status: 200, json: outlook({ window: { from: '2026-09', through: '2026-12', truncated: false } }) } : { status: 500, json: { code: 'internal', message: 'boom', request_id: 'r' } })
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
})
