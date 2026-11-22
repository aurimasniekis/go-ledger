import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fail, installApi, ok, renderApp, signedIn, ALL_GRANTS } from '@/test/fake-api'
import { setCsrfToken } from '@/lib/api'
import { account, application, connection, outage, reconnectRequired, run } from './fixtures'
import { goToBank } from './redirect'

vi.mock('./redirect', async (orig) => ({ ...(await orig<typeof import('./redirect')>()), goToBank: vi.fn() }))

afterEach(() => {
  vi.unstubAllGlobals()
  vi.mocked(goToBank).mockReset()
  setCsrfToken(null)
})
beforeEach(() => vi.mocked(goToBank).mockReset())

const list = (items = [connection()]) => ({ 'GET /api/v1/bank-connections': ok({ items, next_cursor: null }) })
// Amounts use fixed-separator grouping (D38-3a) and appear visually and for screen readers.
const BALANCE = /1\s250\.40\s+EUR/
const card = async (name: string) => within(await screen.findByRole('article', { name }))

describe('bank connections page', () => {
  it('shows every account with its balances, last successful refresh and last attempt', async () => {
    installApi({ ...signedIn(), ...list() })
    renderApp('/connections')
    const c = await card('Fake Bank')
    expect(c.getByText('Up to date')).toBeInTheDocument()
    const acc = c.getByRole('list', { name: 'Accounts at Fake Bank' })
    expect(within(acc).getByText('Everyday')).toBeInTheDocument()
    expect(within(acc).getAllByText(/Last successful refresh:/)).toHaveLength(1)
    expect(within(acc).getAllByText(/Last attempt:/)).toHaveLength(1)
    // Amounts are shown as exact decimal text with their currency.
    expect(within(acc).getAllByText(BALANCE).length).toBeGreaterThan(0)
    expect(within(acc).getByText('Booked')).toBeInTheDocument()
    expect(c.getByText(/sandbox/)).toBeInTheDocument()
  })

  it('does not invent a balance when the bank reported none', async () => {
    installApi({ ...signedIn(), ...list([connection({ accounts: [account({ balances: [], currency: 'XXX' })] })]) })
    renderApp('/connections')
    const c = await card('Fake Bank')
    expect(c.getByText(/has not reported a balance/)).toBeInTheDocument()
    expect(c.queryByText(/0\.00/)).toBeNull()
    expect(c.getByText(/several currencies/)).toBeInTheDocument()
  })

  it('offers one Organise control per account, also for an account with several balances or none', async () => {
    installApi({
      ...signedIn(),
      ...list([connection({ accounts: [account({ name: 'Everyday' }), account({ id: '01JD3Y4C8V9M2Q7W5R6T0XZA02', name: 'Savings', balances: [] })] })]),
    })
    renderApp('/connections')
    const c = await card('Fake Bank')
    // Everyday reports two balances (booked, available): still one control, outside the balance list.
    expect(c.getAllByRole('button', { name: 'Organise Everyday' })).toHaveLength(1)
    expect(within(c.getByRole('list', { name: 'Balances of Everyday' })).queryByRole('button')).toBeNull()
    // Savings has no balance at all and can still be organised.
    expect(c.getAllByRole('button', { name: 'Organise Savings' })).toHaveLength(1)
  })

  it('makes reconnect and retry visibly and functionally distinct', async () => {
    installApi({
      ...signedIn(),
      ...list([reconnectRequired({ id: 'A', institution: { id: 'a', name: 'Consent Bank', country: 'FI' } }), outage({ id: 'B', institution: { id: 'b', name: 'Outage Bank', country: 'FI' } })]),
    })
    renderApp('/connections')
    const rc = await card('Consent Bank')
    const out = await card('Outage Bank')

    // Reconnect: strong badge, reconnect action, no retry, says retrying will not help.
    expect(rc.getAllByText('Reconnect needed')).toHaveLength(2) // the connection's panel and its account
    expect(rc.getByRole('button', { name: 'Reconnect bank' })).toBeInTheDocument()
    expect(rc.queryByRole('button', { name: /Try again|Refresh now/ })).toBeNull()
    expect(rc.getByText(/Retrying will not help/)).toBeInTheDocument()
    expect(rc.getByText(/Refreshing is off until you reconnect/)).toBeInTheDocument()
    expect(rc.getAllByText('Reconnect needed')[0]!.closest('[data-tier]')).toHaveAttribute('data-tier', 'strong')

    // Retry: default-tier badge, retry action, no reconnect, says the consent is fine.
    expect(out.getAllByText('Temporary problem')).toHaveLength(2)
    expect(out.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
    expect(out.queryByRole('button', { name: /Reconnect/ })).toBeNull()
    expect(out.getByText(/Your consent is fine/)).toBeInTheDocument()
    expect(out.queryByText(/Retrying will not help/)).toBeNull()
    expect(out.getAllByText('Temporary problem')[0]!.closest('[data-tier]')).toHaveAttribute('data-tier', 'default')
    expect(out.getByText(/Ledger will try again by itself after/)).toBeInTheDocument()
    expect(rc.queryByText('Temporary problem')).toBeNull()
    expect(out.queryByText('Reconnect needed')).toBeNull()
  })

  it('keeps the last good data visible, marked stale, when the consent is gone or the bank is down', async () => {
    installApi({ ...signedIn(), ...list([reconnectRequired()]) })
    renderApp('/connections')
    const c = await card('Fake Bank')
    expect(c.getAllByText('Reconnect needed').length).toBeGreaterThan(1)
    expect(c.getAllByText(BALANCE).length).toBeGreaterThan(0)
    expect(c.getByText(/figures above are from the last successful refresh/)).toBeInTheDocument()
  })

  it('shows a stale account that has not failed', async () => {
    const stale = account({ freshness: { ...account().freshness, state: 'stale', stale: true, last_success_at: '2026-11-18T08:00:00Z' } })
    installApi({ ...signedIn(), ...list([connection({ freshness: 'stale', accounts: [stale] })]) })
    renderApp('/connections')
    const c = await card('Fake Bank')
    expect(c.getByText('Stale')).toBeInTheDocument()
    expect(c.getByText(/Older than 24 hours/)).toBeInTheDocument()
  })

  it('shows pending, refused, disconnected, never-fetched and partial states in words', async () => {
    installApi({
      ...signedIn(),
      ...list([
        connection({ id: 'P', institution: { id: 'p', name: 'Pending Bank', country: 'FI' }, status: 'pending', accounts: [], last_refresh_run: null }),
        connection({ id: 'F', institution: { id: 'f', name: 'Refused Bank', country: 'FI' }, status: 'failed', last_error: { kind: 'permanent', code: 'consent_refused' }, accounts: [], last_refresh_run: null }),
        connection({ id: 'D', institution: { id: 'd', name: 'Gone Bank', country: 'FI' }, status: 'disconnected' }),
        connection({ id: 'N', institution: { id: 'n', name: 'New Bank', country: 'FI' }, accounts: [], last_refresh_run: null }),
        connection({ id: 'X', institution: { id: 'x', name: 'Partial Bank', country: 'FI' }, last_refresh_run: run({ outcome: 'partial', accounts_failed: 1 }) }),
      ]),
    })
    renderApp('/connections')
    expect((await card('Pending Bank')).getByText('Waiting for the bank')).toBeInTheDocument()
    expect((await card('Pending Bank')).getByRole('button', { name: 'Remove' })).toBeInTheDocument()
    const f = await card('Refused Bank')
    expect(f.getByText('Could not connect')).toBeInTheDocument()
    expect(f.getByText(/Nothing was connected/)).toBeInTheDocument()
    const d = await card('Gone Bank')
    expect(d.getByText('Bank disconnected', { selector: '[data-status]' })).toBeInTheDocument()
    expect(d.getByRole('button', { name: 'Reconnect bank' })).toBeInTheDocument()
    expect(d.queryByRole('button', { name: 'Disconnect' })).toBeNull()
    expect((await card('New Bank')).getByText(/No accounts have been fetched yet/)).toBeInTheDocument()
    expect((await card('Partial Bank')).getByText(/failed for 1/)).toBeInTheDocument()
  })

  it('refreshes on request, says it is not done yet, then shows what came back', async () => {
    const user = userEvent.setup()
    let release: (v: unknown) => void = () => {}
    const gate = new Promise((r) => (release = r))
    let refreshed = false
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/bank-connections': () => ok({ items: [refreshed ? connection({ version: 4, last_refresh_run: run({ id: 'R2' }) }) : connection()], next_cursor: null }),
      'POST /api/v1/bank-connections/01JD3Y4C8V9M2Q7W5R6T0XZC01/refresh': async () => {
        await gate
        refreshed = true
        return ok(connection({ version: 4 }))
      },
    })
    renderApp('/connections')
    const c = await card('Fake Bank')
    await user.click(c.getByRole('button', { name: 'Refresh now' }))
    expect(await c.findByText(/Nothing below has changed yet/)).toBeInTheDocument()
    expect(c.getByRole('button', { name: 'Refreshing…' })).toBeDisabled()
    release(null)
    await waitFor(() => expect(c.queryByText(/Nothing below has changed yet/)).toBeNull())
    expect(calls.filter((x) => x.method === 'POST')).toHaveLength(1)
  })

  it('shows a refresh still running at the deadline and polls until it ends (D40.4)', async () => {
    const user = userEvent.setup()
    const running = connection({ refresh_running: true, last_refresh_run: run({ id: 'R3', outcome: 'running', finished_at: null }) })
    let polls = -1 // -1: nothing requested yet
    installApi({
      ...signedIn(),
      'GET /api/v1/bank-connections': () => {
        if (polls >= 0) polls++
        if (polls < 0) return ok({ items: [connection()], next_cursor: null })
        return ok({ items: [polls <= 2 ? running : connection({ version: 5, last_refresh_run: run({ id: 'R3' }) })], next_cursor: null })
      },
      'POST /api/v1/bank-connections/01JD3Y4C8V9M2Q7W5R6T0XZC01/refresh': () => {
        polls = 0
        return ok(running)
      },
    })
    renderApp('/connections')
    const c = await card('Fake Bank')
    await user.click(c.getByRole('button', { name: 'Refresh now' }))
    expect(await c.findByText(/still running at the bank/)).toBeInTheDocument()
    expect(c.getByRole('button', { name: 'Refreshing…' })).toBeDisabled()
    await waitFor(() => expect(c.queryByText(/still running at the bank/)).toBeNull(), { timeout: 10000 })
    expect(c.getByRole('button', { name: 'Refresh now' })).toBeEnabled()
  }, 15000)

  it('turns a refresh that failed at the bank into the retry state, not a reconnect', async () => {
    const user = userEvent.setup()
    let after = false
    installApi({
      ...signedIn(),
      'GET /api/v1/bank-connections': () => ok({ items: [after ? outage() : connection()], next_cursor: null }),
      'POST /api/v1/bank-connections/01JD3Y4C8V9M2Q7W5R6T0XZC01/refresh': () => {
        after = true
        return ok(outage())
      },
    })
    renderApp('/connections')
    const c = await card('Fake Bank')
    await user.click(c.getByRole('button', { name: 'Refresh now' }))
    expect((await c.findAllByText('Temporary problem')).length).toBeGreaterThan(0)
    expect(c.queryByText('Reconnect needed')).toBeNull()
    expect(c.queryByRole('button', { name: /Reconnect/ })).toBeNull()
    expect(c.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
  })

  it('explains a refresh already running and a refresh the server could not accept', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      ...list(),
      'POST /api/v1/bank-connections/01JD3Y4C8V9M2Q7W5R6T0XZC01/refresh': fail(409, 'refresh_in_progress', 'a refresh of this connection is already running; try again shortly'),
    })
    renderApp('/connections')
    const c = await card('Fake Bank')
    await user.click(c.getByRole('button', { name: 'Refresh now' }))
    expect(await c.findByRole('alert')).toHaveTextContent('already running')
    // Earlier data is untouched.
    expect(c.getAllByText(BALANCE).length).toBeGreaterThan(0)
  })

  it('disables Refresh during the cooldown, says how long, and enables it when it ends (D-B6c)', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      ...list([connection({ as_of: '2026-11-22T09:00:00Z', refresh_available_at: '2026-11-22T09:04:10Z' })]),
    })
    renderApp('/connections')
    const c = await card('Fake Bank')
    // 4 min 10 s left rounds up: never "0 min" while it is still refused.
    const btn = c.getByRole('button', { name: 'Refreshed just now, try again in 5 min' })
    expect(btn).toBeDisabled()
    expect(c.queryByRole('button', { name: 'Refresh now' })).toBeNull()
    await user.click(btn) // a disabled button sends nothing
  })

  it('enables Refresh in the reconnected state (the cooldown does not carry over a reconnect, C-B6c-reconnect)', async () => {
    const failed = run({ outcome: 'failed', started_at: '2026-11-22T08:00:00Z', error: { kind: 'reconnect_required', code: 'EXPIRED_SESSION' } })
    installApi({
      ...signedIn(),
      ...list([connection({ as_of: '2026-11-22T09:00:00Z', consent_granted_at: '2026-11-22T08:50:00Z', last_refresh_run: failed, refresh_available_at: null })]),
    })
    renderApp('/connections')
    const c = await card('Fake Bank')
    expect(c.getByText(/nothing has been fetched since reconnecting/)).toBeInTheDocument()
    expect(c.getByRole('button', { name: 'Refresh now' })).toBeEnabled()
  })

  it('counts the cooldown in whole minutes, rounded up, from the server clock', async () => {
    const { cooldownMinutes } = await import('./connection-card')
    const c = { as_of: '2026-11-22T09:00:00Z', refresh_available_at: '2026-11-22T09:05:00Z' }
    expect(cooldownMinutes(c, 0)).toBe(5)
    expect(cooldownMinutes(c, 60_000)).toBe(4)
    expect(cooldownMinutes(c, 4 * 60_000 + 59_000)).toBe(1)
    expect(cooldownMinutes(c, 5 * 60_000)).toBe(0)
    expect(cooldownMinutes({ ...c, refresh_available_at: null }, 0)).toBe(0)
  })

  it('re-enables Refresh once the cooldown has run out on the page', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      installApi({
        ...signedIn(),
        ...list([connection({ as_of: '2026-11-22T09:00:00Z', refresh_available_at: '2026-11-22T09:01:00Z' })]),
      })
      renderApp('/connections')
      const c = await card('Fake Bank')
      expect(c.getByRole('button', { name: 'Refreshed just now, try again in 1 min' })).toBeDisabled()
      await vi.advanceTimersByTimeAsync(61_000)
      await waitFor(() => expect(c.getByRole('button', { name: 'Refresh now' })).toBeEnabled())
    } finally {
      vi.useRealTimers()
    }
  })

  it('explains a refresh refused by the cooldown without calling it a failure', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      ...list(),
      'POST /api/v1/bank-connections/01JD3Y4C8V9M2Q7W5R6T0XZC01/refresh': fail(429, 'refresh_cooldown', 'this connection was refreshed a moment ago; a refresh on request is allowed again in 3 min'),
    })
    renderApp('/connections')
    const c = await card('Fake Bank')
    await user.click(c.getByRole('button', { name: 'Refresh now' }))
    expect(await c.findByRole('alert')).toHaveTextContent('refreshed a moment ago')
    expect(c.getAllByText(BALANCE).length).toBeGreaterThan(0)
  })

  it('starts a reconnect and sends the browser to the bank', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...signedIn(),
      ...list([reconnectRequired()]),
      'POST /api/v1/bank-connections/01JD3Y4C8V9M2Q7W5R6T0XZC01/reconnect': ok({ connection: reconnectRequired(), authorization_url: 'https://bank.example/authorize?state=s1' }),
    })
    renderApp('/connections')
    const c = await card('Fake Bank')
    await user.click(c.getByRole('button', { name: 'Reconnect bank' }))
    await waitFor(() => expect(goToBank).toHaveBeenCalledWith('https://bank.example/authorize?state=s1'))
    expect(calls.some((x) => x.method === 'POST' && x.path.endsWith('/reconnect'))).toBe(true)
  })

  it('warns before a consent ends and offers an early reconnect without hiding refresh', async () => {
    installApi({ ...signedIn(), ...list([connection({ consent_expires_soon: true, consent_valid_until: '2026-11-30T08:00:00Z' })]) })
    renderApp('/connections')
    const c = await card('Fake Bank')
    expect(c.getByText(/The bank consent ends on/)).toBeInTheDocument()
    expect(c.getByRole('button', { name: 'Reconnect now' })).toBeInTheDocument()
    expect(c.getByRole('button', { name: 'Refresh now' })).toBeInTheDocument()
  })

  it('disconnects only after confirmation and with the connection version', async () => {
    const user = userEvent.setup()
    let gone = false
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/bank-connections': () => ok({ items: [gone ? connection({ status: 'disconnected', version: 4 }) : connection()], next_cursor: null }),
      'POST /api/v1/bank-connections/01JD3Y4C8V9M2Q7W5R6T0XZC01/disconnect': () => {
        gone = true
        return ok(connection({ status: 'disconnected', version: 4 }))
      },
    })
    renderApp('/connections')
    const c = await card('Fake Bank')
    await user.click(c.getByRole('button', { name: 'Disconnect' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(calls.filter((x) => x.method === 'POST')).toHaveLength(0)
    await user.click(within(dialog).getByRole('button', { name: 'Disconnect bank' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    const post = calls.find((x) => x.method === 'POST')!
    expect(post.headers['If-Match']).toBe('"3"')
    expect(await c.findByText('Bank disconnected', { selector: '[data-status]' })).toBeInTheDocument()
  })

  it('keeps the dialog and the connection as they were when the disconnect is refused', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      ...list(),
      'POST /api/v1/bank-connections/01JD3Y4C8V9M2Q7W5R6T0XZC01/disconnect': fail(409, 'version_conflict', 'the object was changed by another request'),
    })
    renderApp('/connections')
    const c = await card('Fake Bank')
    await user.click(c.getByRole('button', { name: 'Disconnect' }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Disconnect bank' }))
    expect(await within(screen.getByRole('alertdialog')).findByRole('alert')).toHaveTextContent('changed by another request')
    expect(c.queryByText('Bank disconnected', { selector: '[data-status]' })).toBeNull()
  })

  it('offers no changes to a role that can only read connections', async () => {
    installApi({ ...signedIn(undefined, ALL_GRANTS.filter((g) => g !== 'connections:write')), ...list() })
    renderApp('/connections')
    const c = await card('Fake Bank')
    // Organise is the organisation control of an account (organisation:read), not a change to the connection.
    expect(c.queryAllByRole('button').filter((b) => !(b.getAttribute('aria-label') ?? '').startsWith('Organise'))).toHaveLength(0)
    expect(screen.queryByRole('button', { name: 'Connect a bank' })).toBeNull()
    expect(screen.getByText(/can see connections but not change them/)).toBeInTheDocument()
  })

  it('says so when there is no connection yet', async () => {
    installApi({ ...signedIn(), ...list([]) })
    renderApp('/connections')
    expect(await screen.findByRole('heading', { name: 'No bank connected yet' })).toBeInTheDocument()
  })

  it('says nothing is shown, rather than showing an empty list, when Ledger cannot be reached', async () => {
    installApi({
      ...signedIn(),
      'GET /api/v1/bank-connections': () => {
        throw new TypeError('offline')
      },
    })
    renderApp('/connections', new QueryClient({ defaultOptions: { queries: { retry: false } } }))
    expect(await screen.findByText(/no connection to the server/)).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'No bank connected yet' })).toBeNull()
  })
})

describe('connect flow', () => {
  const apps = { 'GET /api/v1/bank-applications': ok({ items: [application()] }) }
  const banks = { 'GET /api/v1/bank-applications/fake-app/institutions': ok({ items: [{ id: 'Fake Bank', name: 'Fake Bank', country: 'FI', max_consent_validity_days: 180 }] }) }

  it('finds banks in a country, starts consent and sends the browser to the bank; the pending entry is not shown as connected', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...signedIn(),
      ...list([]),
      ...apps,
      ...banks,
      'POST /api/v1/bank-connections': ok({ connection: connection({ status: 'pending', accounts: [], last_refresh_run: null }), authorization_url: 'https://bank.example/authorize?state=s1' }, 201),
    })
    renderApp('/connections')
    await user.click((await screen.findAllByRole('button', { name: 'Connect a bank' }))[0]!)
    const dialog = await screen.findByRole('dialog', { name: 'Connect a bank' })
    expect(await within(dialog).findByText(/Ledger never asks for or sees your bank login/)).toBeInTheDocument()
    // The only application is chosen for you and its reach is explained.
    expect(within(dialog).getByText(/Every account you approve at the bank/)).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: 'Find banks' }))
    expect(await within(dialog).findByText('Enter the two-letter country code of the bank, such as DE.')).toBeInTheDocument()
    await user.type(within(dialog).getByLabelText('Country of the bank'), 'fi')
    expect(within(dialog).getByLabelText('Country of the bank')).toHaveValue('FI')
    await user.click(within(dialog).getByRole('button', { name: 'Find banks' }))
    await user.click(await within(dialog).findByRole('button', { name: 'Continue to your bank' }))
    expect(await within(dialog).findByText('Choose a bank.')).toBeInTheDocument()
    await user.selectOptions(within(dialog).getByLabelText('Bank'), 'Fake Bank')
    await user.click(within(dialog).getByRole('button', { name: 'Continue to your bank' }))
    await waitFor(() => expect(goToBank).toHaveBeenCalledWith('https://bank.example/authorize?state=s1'))
    const post = calls.find((x) => x.method === 'POST')!
    expect(post.body).toEqual({ application: 'fake-app', institution_id: 'Fake Bank', country: 'FI' })
  })

  it('names the developer-mode limit when several applications exist', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      ...list([]),
      'GET /api/v1/bank-applications': ok({ items: [application(), application({ name: 'fake-dev', mode: 'developer', account_access: 'linked_only', environment: 'production' })] }),
    })
    renderApp('/connections')
    await user.click((await screen.findAllByRole('button', { name: 'Connect a bank' }))[0]!)
    const dialog = await screen.findByRole('dialog')
    await user.selectOptions(await within(dialog).findByLabelText('Provider application'), 'fake-dev')
    expect(within(dialog).getByText(/Only accounts linked to this application/)).toBeInTheDocument()
  })

  it('shows a provider outage while looking for banks as retryable, with nothing started', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...signedIn(),
      ...list([]),
      ...apps,
      'GET /api/v1/bank-applications/fake-app/institutions': { status: 503, json: { code: 'bank_unavailable', message: 'the bank or provider is not available right now; try again later. Nothing was lost', request_id: 'r' }, headers: { 'Retry-After': '30' } },
    })
    renderApp('/connections')
    await user.click((await screen.findAllByRole('button', { name: 'Connect a bank' }))[0]!)
    const dialog = await screen.findByRole('dialog')
    await user.type(await within(dialog).findByLabelText('Country of the bank'), 'FI')
    await user.click(within(dialog).getByRole('button', { name: 'Find banks' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(/try again later.*30 seconds/)
    expect(calls.some((x) => x.method === 'POST')).toBe(false)
  })

  it('says connecting is not set up when no redirect URL is configured', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      ...list([]),
      ...apps,
      ...banks,
      'POST /api/v1/bank-connections': fail(409, 'bank_connect_unavailable', 'connecting a bank is not set up'),
    })
    renderApp('/connections')
    await user.click((await screen.findAllByRole('button', { name: 'Connect a bank' }))[0]!)
    const dialog = await screen.findByRole('dialog')
    await user.type(await within(dialog).findByLabelText('Country of the bank'), 'FI')
    await user.click(within(dialog).getByRole('button', { name: 'Find banks' }))
    await user.selectOptions(await within(dialog).findByLabelText('Bank'), 'Fake Bank')
    await user.click(within(dialog).getByRole('button', { name: 'Continue to your bank' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('banking.redirect_url')
    expect(goToBank).not.toHaveBeenCalled()
  })

  it('says so when no provider is configured', async () => {
    const user = userEvent.setup()
    installApi({ ...signedIn(), ...list([]), 'GET /api/v1/bank-applications': ok({ items: [] }) })
    renderApp('/connections')
    await user.click((await screen.findAllByRole('button', { name: 'Connect a bank' }))[0]!)
    expect(await screen.findByText(/No banking provider is configured/)).toBeInTheDocument()
  })
})

describe('consent callback', () => {
  const done = connection({ status: 'active', accounts: [], last_refresh_run: null, freshness: 'never_refreshed' })

  it('completes the consent once, removes the code from the address and offers the first fetch', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...signedIn(),
      ...list([done]),
      'POST /api/v1/bank-connections/complete': ok(done),
      'POST /api/v1/bank-connections/01JD3Y4C8V9M2Q7W5R6T0XZC01/refresh': ok(connection()),
    })
    renderApp('/connections/callback?state=s1&code=c1')
    expect(await screen.findByText(/Fake Bank is connected/)).toBeInTheDocument()
    const completes = calls.filter((x) => x.path.endsWith('/complete'))
    expect(completes).toHaveLength(1)
    expect(completes[0]!.body).toEqual({ state: 's1', code: 'c1' })
    expect(screen.getByText(/No accounts or balances have been fetched yet/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Fetch accounts now' }))
    expect(await screen.findByText(/Fetched 1 account/)).toBeInTheDocument()
    expect(window.location.href).not.toContain('c1')
  })

  it("sends the bank's error instead of a code when the user refused", async () => {
    const calls = installApi({
      ...signedIn(),
      'POST /api/v1/bank-connections/complete': fail(422, 'bank_request_rejected', 'the provider refused the request (consent_refused); trying again unchanged will not help'),
    })
    renderApp('/connections/callback?state=s1&error=access_denied')
    expect(await screen.findByText('Approval refused')).toBeInTheDocument()
    expect(calls.find((x) => x.path.endsWith('/complete'))!.body).toEqual({ state: 's1', error: 'access_denied' })
    expect(screen.getByText(/Nothing was connected/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull()
  })

  it('offers a retry that works when the bank could not be reached, and says the approval is not lost', async () => {
    const user = userEvent.setup()
    let n = 0
    const calls = installApi({
      ...signedIn(),
      ...list([done]),
      'POST /api/v1/bank-connections/complete': () => (++n === 1 ? fail(503, 'bank_unavailable', 'the bank or provider is not available right now') : ok(done)),
    })
    renderApp('/connections/callback?state=s1&code=c1')
    expect(await screen.findByText('Temporary problem')).toBeInTheDocument()
    expect(screen.getByText(/your approval is not lost/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByText(/Fake Bank is connected/)).toBeInTheDocument()
    // The same state and code are sent again, from memory, not from the address.
    const completes = calls.filter((x) => x.path.endsWith('/complete'))
    expect(completes.map((x) => x.body)).toEqual([
      { state: 's1', code: 'c1' },
      { state: 's1', code: 'c1' },
    ])
  })

  it('says an approval that was already used is not a new connection', async () => {
    installApi({ ...signedIn(), 'POST /api/v1/bank-connections/complete': fail(409, 'consent_not_found', 'no consent is waiting for this redirect') })
    renderApp('/connections/callback?state=s1&code=c1')
    expect(await screen.findByText('Approval already used')).toBeInTheDocument()
    expect(screen.getByText(/Nothing new was connected/)).toBeInTheDocument()
  })

  it('does nothing without a redirect to finish', async () => {
    const calls = installApi({ ...signedIn() })
    renderApp('/connections/callback')
    expect(await screen.findByText(/only works when your bank sends you back/)).toBeInTheDocument()
    expect(calls.some((x) => x.path.endsWith('/complete'))).toBe(false)
  })
})
