import { act, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fail, installApi, ok, renderApp, signedIn, type Handler, type Reply } from '@/test/fake-api'
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
    expect(calls.every((c) => c.method === 'GET')).toBe(true)
  })

  it('shows a failure when the rates cannot be loaded', async () => {
    installApi({ ...signedIn(), 'GET /api/v1/rates/status': ok(status()), 'GET /api/v1/rates': fail(400, 'validation_failed', 'boom') })
    renderApp('/rates')
    expect(await screen.findByText(/Could not load exchange rates/)).toBeInTheDocument()
  })

  it('shows the standard not-permitted state, and calls no rates endpoint, without rates:read', async () => {
    const calls = installApi({ ...signedIn(undefined, ['commitments:read']) })
    renderApp('/rates')
    expect(await screen.findByRole('alert')).toHaveTextContent(/not permitted/)
    expect(screen.queryByRole('button', { name: /refresh/i })).toBeNull()
    expect(calls.some((c) => c.path.startsWith('/api/v1/rates'))).toBe(false)
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

  describe('Refresh now', () => {
    const page = (extra: Record<string, Handler> = {}, st: Partial<RatesStatus> = {}) =>
      installApi({ ...signedIn(), 'GET /api/v1/rates/status': ok(status(st)), 'GET /api/v1/rates': ok({ items: [rate()], next_cursor: null }), ...extra })
    const run = { trigger: 'manual', started_at: '2026-11-21T10:00:00Z', finished_at: '2026-11-21T10:00:02Z', outcome: 'succeeded', currencies_refreshed: 2, error: null }

    it('is absent for a role without rates:write', async () => {
      installApi({ ...signedIn(undefined, ['rates:read']), 'GET /api/v1/rates/status': ok(status()), 'GET /api/v1/rates': ok({ items: [], next_cursor: null }) })
      renderApp('/rates')
      await screen.findByRole('heading', { name: 'Exchange rates' })
      expect(screen.queryByRole('button', { name: 'Refresh now' })).toBeNull()
    })

    it('shows pending, then success with the time and a reversible-feedback note', async () => {
      let release: (r: Reply) => void = () => {}
      const calls = page({ 'POST /api/v1/rates/refreshes': () => new Promise<Reply>((r) => (release = r)) })
      renderApp('/rates')
      await userEvent.click(await screen.findByRole('button', { name: 'Refresh now' }))
      const pending = await screen.findByRole('button', { name: 'Refreshing…' })
      expect(pending).toHaveAttribute('aria-disabled', 'true')
      expect(screen.getByTestId('rates-refresh-result')).toHaveTextContent('Asking the rate provider')
      release(ok(run))
      expect(await screen.findByText(/Refreshed 2 currencies at/)).toHaveTextContent('2026-11-21 10:00:02 UTC'.slice(0, 16))
      expect(screen.getByText(/nothing is deleted/)).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Refresh now' })).toBeEnabled()
      expect(calls.filter((c) => c.method === 'POST')).toHaveLength(1)
    })

    it('says when a rate-limited refresh can be retried and keeps the button off until then', async () => {
      page({ 'POST /api/v1/rates/refreshes': { ...fail(429, 'rate_limited', 'slow down'), headers: { 'Retry-After': '45' } } })
      renderApp('/rates')
      await userEvent.click(await screen.findByRole('button', { name: 'Refresh now' }))
      const alert = await screen.findByRole('alert')
      expect(alert).toHaveTextContent(/Nothing changed/)
      expect(alert).toHaveTextContent(/try again at \d{4}-\d\d-\d\d \d\d:\d\d UTC/)
      const btn = screen.getByRole('button', { name: 'Refresh now' })
      expect(btn).toHaveAttribute('aria-disabled', 'true')
    })

    it('says a refresh is already running (409) and changes nothing', async () => {
      page({ 'POST /api/v1/rates/refreshes': fail(409, 'refresh_in_progress', 'busy') })
      renderApp('/rates')
      await userEvent.click(await screen.findByRole('button', { name: 'Refresh now' }))
      expect(await screen.findByRole('alert')).toHaveTextContent('A refresh is already running. Nothing changed; wait a moment and check the list.')
    })

    it('keeps keyboard focus on the button while pending and ignores clicks', async () => {
      let release: (r: Reply) => void = () => {}
      const calls = page({ 'POST /api/v1/rates/refreshes': () => new Promise<Reply>((r) => (release = r)) })
      renderApp('/rates')
      const btn = await screen.findByRole('button', { name: 'Refresh now' })
      btn.focus()
      await userEvent.keyboard('{Enter}')
      const pending = await screen.findByRole('button', { name: 'Refreshing…' })
      expect(pending).toHaveAttribute('aria-disabled', 'true')
      expect(pending).not.toBeDisabled()
      expect(pending).toHaveFocus()
      await userEvent.keyboard('{Enter}')
      expect(calls.filter((c) => c.method === 'POST')).toHaveLength(1)
      await act(async () => release(ok(run)))
      expect(await screen.findByText(/Refreshed 2 currencies at/)).toBeInTheDocument()
    })

    it('re-enables the button after the Retry-After wait, and keeps focus while it waits', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true })
      try {
        const calls = page({ 'POST /api/v1/rates/refreshes': { ...fail(429, 'rate_limited', 'slow down'), headers: { 'Retry-After': '45' } } })
        renderApp('/rates')
        const btn = await screen.findByRole('button', { name: 'Refresh now' })
        btn.focus()
        await userEvent.keyboard('{Enter}')
        await screen.findByRole('alert')
        expect(btn).toHaveAttribute('aria-disabled', 'true')
        expect(btn).toHaveFocus()
        await userEvent.keyboard('{Enter}')
        expect(calls.filter((c) => c.method === 'POST')).toHaveLength(1)
        await act(async () => { vi.advanceTimersByTime(44_000) })
        expect(screen.getByRole('button', { name: 'Refresh now' })).toHaveAttribute('aria-disabled', 'true')
        await act(async () => { vi.advanceTimersByTime(2_000) })
        expect(screen.getByRole('button', { name: 'Refresh now' })).not.toHaveAttribute('aria-disabled')
      } finally {
        vi.useRealTimers()
      }
    })

    it('clears the retry timer when the page unmounts', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true })
      try {
        page({ 'POST /api/v1/rates/refreshes': { ...fail(429, 'rate_limited', 'slow down'), headers: { 'Retry-After': '45' } } })
        const view = renderApp('/rates')
        const waits: unknown[] = []
        const realSet = globalThis.setTimeout
        const setSpy = vi.spyOn(globalThis, 'setTimeout').mockImplementation(((fn: () => void, ms?: number, ...rest: unknown[]) => {
          const id = (realSet as (...a: unknown[]) => unknown)(fn, ms, ...rest)
          if ((ms ?? 0) >= 45_000 && (ms ?? 0) <= 45_100) waits.push(id)
          return id
        }) as typeof setTimeout)
        const clearSpy = vi.spyOn(globalThis, 'clearTimeout')
        await userEvent.click(await screen.findByRole('button', { name: 'Refresh now' }))
        await screen.findByRole('alert')
        expect(waits).toHaveLength(1)
        clearSpy.mockClear()
        view.unmount()
        expect(clearSpy).toHaveBeenCalledWith(waits[0])
        setSpy.mockRestore()
        clearSpy.mockRestore()
      } finally {
        vi.useRealTimers()
      }
    })

    it('says when refreshing is unavailable and changes nothing', async () => {
      page({ 'POST /api/v1/rates/refreshes': fail(409, 'rates_refresh_unavailable', 'off') })
      renderApp('/rates')
      await userEvent.click(await screen.findByRole('button', { name: 'Refresh now' }))
      expect(await screen.findByRole('alert')).toHaveTextContent(/cannot be refreshed right now.*Nothing changed/)
    })

    it('is disabled with the reason when no refresh can run', async () => {
      page({}, { enabled: false, refresh_available: false })
      renderApp('/rates')
      expect(await screen.findByText(/No refresh can run/)).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Refresh now' })).toHaveAttribute('aria-disabled', 'true')
    })
  })
})
