import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fail, installApi, ok, renderApp, signedIn } from '@/test/fake-api'
import type { CachedRate, RatesStatus } from './types'

afterEach(() => vi.unstubAllGlobals())

const rate = (over: Partial<CachedRate> = {}): CachedRate => ({
  provider: 'frankfurter', source: 'ECB', base: 'USD', quote: 'EUR', value: '0.90992', date: '2026-11-20',
  retrieved_at: '2026-11-21T09:00:00Z', first_retrieved_at: '2026-11-21T09:00:00Z', confirmed_through: '2026-11-21', stale: false, stale_reason: null, previous: null,
  ...over,
})

const status = (over: Partial<RatesStatus> = {}): RatesStatus => ({
  enabled: true, provider: 'frankfurter', source: 'ECB', reporting_currency: 'EUR', refresh_available: true, refresh_interval_seconds: 86400, refresh_after_seconds: 86400,
  stale_after_days: 5, last_run: null, last_success_at: null, as_of: '2026-11-21T09:30:00Z', ...over,
})

describe('rates page', () => {
  it('lists exact rates with provenance, ISO rate date and retrieval time, and marks a stale rate', async () => {
    installApi({
      ...signedIn(),
      'GET /api/v1/rates/status': ok(status({ last_run: { trigger: 'scheduled', started_at: '2026-11-21T09:00:00Z', finished_at: '2026-11-21T09:00:02Z', outcome: 'succeeded', currencies_refreshed: 2, error: null }, last_success_at: '2026-11-21T09:00:02Z' })),
      'GET /api/v1/rates': ok({
        items: [
          rate({ base: 'GBP', value: '1.1500', date: '2026-11-09', retrieved_at: '2026-11-10T09:00:00Z', stale: true, stale_reason: 'not_retrieved_recently' }),
          rate({ previous: { value: '0.90500', revised_at: '2026-11-21T09:00:00Z' } }),
        ],
        next_cursor: null,
      }),
    })
    renderApp('/rates')
    const table = await screen.findByRole('table', { name: /Cached exchange rates/ })
    const rows = within(table).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(2)
    const gbp = within(rows[0]!)
    expect(gbp.getByRole('rowheader')).toHaveTextContent('GBP/EUR')
    expect(gbp.getByText('1.1500')).toBeInTheDocument()
    expect(gbp.getByText('2026-11-09')).toBeInTheDocument()
    expect(gbp.getByText('2026-11-10 09:00 UTC')).toBeInTheDocument()
    expect(gbp.getByText('frankfurter / ECB')).toBeInTheDocument()
    expect(gbp.getByText('Stale: not retrieved recently')).toBeInTheDocument()
    const usd = within(rows[1]!)
    expect(usd.getByText('0.90992')).toBeInTheDocument()
    expect(usd.getByText('Current')).toBeInTheDocument()
    expect(usd.getByText('revised from 0.90500')).toBeInTheDocument()
    expect(screen.getByText(/Rates come from frankfurter \(ECB\), fetched to EUR/)).toBeInTheDocument()
    expect(screen.getByText(/A stale rate is never used in a converted total/)).toHaveTextContent('not retrieved for 5 days')
    expect(screen.getByTestId('rates-last-run')).toHaveTextContent('scheduled refresh succeeded, 2 currencies refreshed (2026-11-21 09:00 UTC)')
  })

  it('says a failed refresh failed, why, and when the last full success was', async () => {
    installApi({
      ...signedIn(),
      'GET /api/v1/rates/status': ok(status({ last_run: { trigger: 'manual', started_at: '2026-11-21T10:00:00Z', finished_at: '2026-11-21T10:00:15Z', outcome: 'failed', currencies_refreshed: 0, error: { kind: 'unavailable', code: 'timeout' } }, last_success_at: '2026-11-20T09:00:00Z' })),
      'GET /api/v1/rates': ok({ items: [rate()], next_cursor: null }),
    })
    renderApp('/rates')
    const run = await screen.findByTestId('rates-last-run')
    expect(run).toHaveTextContent('manual refresh failed: the rate provider did not answer (timeout), 0 currencies refreshed')
    expect(run).toHaveTextContent('Last fully successful refresh: 2026-11-20 09:00 UTC')
  })

  it('says the provider is off and what that means when nothing is cached', async () => {
    installApi({ ...signedIn(), 'GET /api/v1/rates/status': ok(status({ enabled: false, refresh_available: false })), 'GET /api/v1/rates': ok({ items: [], next_cursor: null }) })
    renderApp('/rates')
    expect(await screen.findByText('Provider off')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'No exchange rates cached yet' })).toBeInTheDocument()
    expect(screen.getByText(/The rate provider is off, so nothing has been fetched/)).toBeInTheDocument()
    expect(screen.queryByTestId('rates-last-run')).toBeNull()
  })

  it('loads more rates with the cursor and keeps the page read-only', async () => {
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/rates/status': ok(status()),
      'GET /api/v1/rates': (r) => ok(r.query.get('cursor') ? { items: [rate({ base: 'SEK', value: '0.09100' })], next_cursor: null } : { items: [rate()], next_cursor: 'abc' }),
    })
    renderApp('/rates')
    await userEvent.click(await screen.findByRole('button', { name: 'Load more rates' }))
    expect(await screen.findByText('SEK/EUR')).toBeInTheDocument()
    expect(calls.filter((c) => c.path === '/api/v1/rates').at(-1)!.query.get('cursor')).toBe('abc')
    expect(screen.queryByRole('button', { name: /refresh/i })).toBeNull()
    expect(calls.every((c) => c.method === 'GET')).toBe(true)
  })

  it('shows a failure when the rates cannot be loaded', async () => {
    installApi({ ...signedIn(), 'GET /api/v1/rates/status': ok(status()), 'GET /api/v1/rates': fail(400, 'validation_failed', 'boom') })
    renderApp('/rates')
    expect(await screen.findByText(/Could not load exchange rates/)).toBeInTheDocument()
  })

  it('offers the page in the account menu only with rates:read', async () => {
    installApi({ ...signedIn(undefined, ['commitments:read']), 'GET /api/v1/rates/status': ok(status()), 'GET /api/v1/rates': ok({ items: [], next_cursor: null }) })
    renderApp('/month')
    await screen.findByRole('link', { name: 'Outlook' })
    await userEvent.click(screen.getByRole('button', { name: 'Account' }))
    expect(screen.queryByRole('link', { name: 'Exchange rates' })).toBeNull()
  })

  it('links to the page from the account menu with rates:read', async () => {
    installApi({ ...signedIn(), 'GET /api/v1/rates/status': ok(status()), 'GET /api/v1/rates': ok({ items: [rate()], next_cursor: null }) })
    renderApp('/month')
    await userEvent.click(await screen.findByRole('button', { name: 'Account' }))
    await userEvent.click(screen.getByRole('link', { name: 'Exchange rates' }))
    expect(await screen.findByRole('heading', { name: 'Exchange rates' })).toBeInTheDocument()
  })

  it('says when the status could not be loaded instead of showing nothing, while the rates still show', async () => {
    installApi({
      ...signedIn(),
      'GET /api/v1/rates/status': fail(400, 'validation_failed', 'boom'),
      'GET /api/v1/rates': ok({ items: [rate()], next_cursor: null }),
    })
    renderApp('/rates')
    expect(await screen.findByText(/Could not load the rate provider status/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
    expect(await screen.findByRole('table', { name: /Cached exchange rates/ })).toBeInTheDocument()
  })

  it('shows a loading state for the status while it loads', async () => {
    installApi({ ...signedIn(), 'GET /api/v1/rates/status': () => new Promise(() => {}), 'GET /api/v1/rates': ok({ items: [rate()], next_cursor: null }) })
    renderApp('/rates')
    expect(await screen.findByText(/Loading the rate provider status/)).toBeInTheDocument()
  })
})
