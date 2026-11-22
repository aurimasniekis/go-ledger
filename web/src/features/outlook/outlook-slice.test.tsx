import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { makeQueryClient } from '@/lib/query'
import { ALL_GRANTS, installApi, ok, renderApp, signedIn, type Req } from '@/test/fake-api'
import { def } from '@/features/organisation/fixtures'
import type { Outlook } from './types'

afterEach(() => vi.unstubAllGlobals())

const eur = (amount: string) => ({ amount, currency: 'EUR' })
const bucket = def('bucket', 'B00', 'Home')

function outlook(over: Partial<Outlook> = {}): Outlook {
  return {
    as_of: '2026-11-16',
    notice: 'Outlook against recorded commitments: it is not a forecast and does not tell you how much money is safe to spend.',
    slice: null,
    window: { from: '2026-10', through: '2026-11', truncated: false, not_recorded_count: 0 },
    currencies: [
      { currency: 'EUR', still_due: eur('200.00'), overdue: eur('100.00'), upcoming: eur('100.00'), overdue_count: 1, unconverted_evidence_count: 0, balance: eur('1250.40'),
        balance_less_still_due: eur('1050.40'), account_count: 1, stale_account_count: 0, oldest_balance_obtained_at: '2026-11-16T08:00:00Z', overdue_months: [],
        balance_as_of: '2026-11-15', payments_after_balance: { count: 0, amount: eur('0.00') } },
    ],
    balances: { shown: true, note: 'Balances are the latest each bank reported.', stale_after_seconds: 86400 },
    accounts: [],
    converted: null,
    planned_outflows: { included: false, note: 'Planned outflows are not part of Ledger yet.' },
    freshness: { computed_at: '2026-11-16T09:00:00Z', occurrences_changed_at: null },
    ...over,
  }
}

const slice = (accounts = 1) => ({
  bucket: { id: bucket.id, name: 'Home', archived: false },
  commitment_count: 2,
  account_count: accounts,
  note: 'Slice of the outlook: only the commitments and the accounts in this bucket are counted. A bucket allocates no money.',
})

describe('Outlook sliced by a bucket (O3)', () => {
  it('asks the server for the slice, labels it as a slice, and can go back to the whole outlook', async () => {
    const user = userEvent.setup()
    const outlookCalls: Req[] = []
    const calls = installApi({
      ...signedIn(undefined, ALL_GRANTS),
      'GET /api/v1/buckets': ok({ items: [bucket], next_cursor: null }),
      'GET /api/v1/outlook': (r) => {
        outlookCalls.push(r)
        return ok(r.query.get('bucket') ? outlook({ slice: slice() }) : outlook())
      },
    })
    renderApp('/outlook', makeQueryClient())
    const select = await screen.findByLabelText('Show only one bucket (a slice of the outlook)')
    expect(screen.queryByTestId('outlook-slice')).toBeNull()
    await screen.findByRole('option', { name: 'Home' })
    await user.selectOptions(select, 'Home')
    const note = await screen.findByTestId('outlook-slice')
    expect(note).toHaveTextContent('Only the bucket Home')
    expect(note).toHaveTextContent('2 commitments and 1 account')
    expect(note).toHaveTextContent('Not the whole outlook')
    expect(note).toHaveTextContent('A bucket allocates no money')
    expect(screen.getByRole('region', { name: 'By currency, bucket Home only' })).toBeInTheDocument()
    expect(screen.getByTestId('outlook-notice')).toHaveTextContent('does not tell you how much money is safe to spend')
    expect(outlookCalls.at(-1)!.query.get('bucket')).toBe(bucket.id)
    await user.click(within(note).getByRole('link', { name: 'Show the whole outlook' }))
    await screen.findByLabelText('Show only one bucket (a slice of the outlook)')
    await vi.waitFor(() => expect(screen.queryByTestId('outlook-slice')).toBeNull())
    expect(outlookCalls.at(-1)!.query.get('bucket')).toBeNull()
    expect(calls.filter((c) => c.path === '/api/v1/outlook').length).toBeGreaterThan(1)
  })

  it('opens a sliced outlook from the address and says no account is in the bucket', async () => {
    installApi({
      ...signedIn(undefined, ALL_GRANTS),
      'GET /api/v1/buckets': ok({ items: [bucket], next_cursor: null }),
      'GET /api/v1/outlook': ok(outlook({ slice: slice(0), currencies: [{ ...outlook().currencies[0]!, balance: null, balance_less_still_due: null, balance_as_of: null, payments_after_balance: null, account_count: 0 }] })),
    })
    renderApp('/outlook?bucket=' + bucket.id, makeQueryClient())
    expect(await screen.findByTestId('outlook-slice')).toHaveTextContent('0 accounts')
    expect(screen.getByText('No bank account is in this bucket, so no balance is compared in this slice.')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Bank accounts in the bucket Home' })).toBeInTheDocument()
  })

  it('offers no bucket choice without organisation:read', async () => {
    const calls = installApi({
      ...signedIn(undefined, ['commitments:read', 'connections:read']),
      'GET /api/v1/outlook': ok(outlook()),
    })
    renderApp('/outlook', makeQueryClient())
    await screen.findByTestId('outlook-notice')
    expect(screen.queryByLabelText('Show only one bucket (a slice of the outlook)')).toBeNull()
    expect(calls.some((c) => c.path === '/api/v1/buckets')).toBe(false)
  })
})
