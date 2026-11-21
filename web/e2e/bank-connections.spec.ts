import { expect, test, type Page, type Route } from '@playwright/test'

// Bank connection UI end to end (B4): the real built UI in a real browser against a stateful fake of the bank
// connection API that behaves like the fake provider (internal/domain/banking/bankingtest): consent redirect with a
// single-use state, refresh, a withdrawn consent (reconnect required) and a bank outage (retryable). No live bank
// is involved, and no bank login exists anywhere in this test. The Go integration test TestBankConnectionJourney
// runs the same journey against the real handlers and store.

type Conn = Record<string, unknown>
const T0 = '2026-11-21T08:00:00Z'
const ID = '01JD3Y4C8V9M2Q7W5R6T0XZC01'
const USER = '01JD3Y4C8V9M2Q7W5R6T0XZBNA'

class FakeBank {
  conn: Conn | null = null
  pending = new Set<string>()
  outage = false
  revoked = false
  calls: string[] = []

  account(state: 'fresh' | 'reconnect_required' | 'failed', error: unknown = null) {
    return {
      id: '01JD3Y4C8V9M2Q7W5R6T0XZA01', name: 'Everyday', product: 'Current account', currency: 'EUR', identifier_hint: '0001', no_longer_reported: false, transactions_through: '2026-11-20',
      freshness: { state, stale: state !== 'fresh', last_success_at: T0, last_attempt_at: state === 'fresh' ? T0 : '2026-11-21T09:30:00Z', last_error: error, notice_code: null },
      balances: [{ type: 'booked', amount: { amount: '1250.40', currency: 'EUR' }, reference_date: '2026-11-20', obtained_at: T0 }],
    }
  }
  view(over: Conn = {}): Conn {
    return {
      id: ID, provider: 'fake', application: 'fake-app', mode: 'app', environment: 'sandbox', account_access: 'all', institution: { id: 'Fake Bank', name: 'Fake Bank', country: 'FI' },
      status: 'active', needs_reconnect: false, freshness: 'never_refreshed', consent_granted_at: T0, consent_valid_until: '2027-03-30T08:00:00Z', consent_expires_soon: false, last_error: null,
      accounts: [], last_refresh_run: null, schedule: null, as_of: T0, version: 2, created_at: T0, updated_at: T0, ...over,
    }
  }
}

async function fakeApi(page: Page, bank: FakeBank) {
  const json = (route: Route, body: unknown, status = 200, headers: Record<string, string> = {}) =>
    route.fulfill({ status, contentType: 'application/json', headers, body: JSON.stringify(body) })
  const err = (route: Route, status: number, code: string, message: string) => json(route, { code, message, request_id: 'req_e2e' }, status)
  await page.route('**/api/v1/**', async (route) => {
    const req = route.request()
    const path = new URL(req.url()).pathname.replace('/api/v1', '')
    const key = `${req.method()} ${path}`
    bank.calls.push(key)
    const grants = ['commitments:read', 'commitments:write', 'connections:read', 'connections:write']
    switch (key) {
      case 'GET /auth/session':
        return json(route, { user_id: USER, session: { id: 'S', current: true, created_at: T0, last_seen_at: T0, authenticated_at: T0, idle_expires_at: T0, expires_at: T0, user_agent: 'e2e', address: '192.0.2.1' }, csrf_token: 'csrf', recent_auth_until: T0, two_factor: { enabled: false, required: false, enrollment_required: false } })
      case 'GET /me':
        return json(route, { kind: 'user', user_id: USER, token_id: null, role: 'administrator', grants })
      case 'GET /bank-applications':
        return json(route, { items: [{ provider: 'fake', name: 'fake-app', mode: 'app', environment: 'sandbox', account_access: 'all', capabilities: { balance_types: ['booked'], pending_transactions: true, max_consent_validity_days: 180, max_history_days: 730, background_fetches_per_day: 4 } }] })
      case 'GET /bank-applications/fake-app/institutions':
        return json(route, { items: [{ id: 'Fake Bank', name: 'Fake Bank', country: 'FI', max_consent_validity_days: 180 }] })
      case 'GET /bank-connections':
        return json(route, { items: bank.conn ? [bank.conn] : [], next_cursor: null })
      case 'POST /bank-connections': {
        const state = 'state-' + bank.calls.length
        bank.pending.add(state)
        bank.conn = bank.view({ status: 'pending', consent_granted_at: null, consent_valid_until: null })
        return json(route, { connection: bank.conn, authorization_url: `https://bank.example/authorize?state=${state}` }, 201)
      }
      case 'POST /bank-connections/complete': {
        const body = req.postDataJSON() as { state: string; code?: string }
        if (!bank.pending.delete(body.state)) return err(route, 409, 'consent_not_found', 'no consent is waiting for this redirect')
        bank.revoked = false
        bank.conn = bank.view()
        return json(route, bank.conn)
      }
      case `POST /bank-connections/${ID}/refresh`: {
        if (bank.revoked) {
          bank.conn = bank.view({
            status: 'reconnect_required', needs_reconnect: true, freshness: 'reconnect_required', last_error: { kind: 'reconnect_required', code: 'EXPIRED_SESSION' },
            accounts: [bank.account('reconnect_required', { kind: 'reconnect_required', code: 'EXPIRED_SESSION' })],
            last_refresh_run: { id: 'R', started_at: T0, finished_at: T0, outcome: 'failed', error: { kind: 'reconnect_required', code: 'EXPIRED_SESSION' }, accounts_failed: 0, transactions_inserted: 0, transactions_rejected: 0 },
          })
        } else if (bank.outage) {
          bank.conn = bank.view({
            freshness: 'failed', accounts: [bank.account('failed', { kind: 'retryable', code: 'TIMEOUT' })],
            last_refresh_run: { id: 'R', started_at: T0, finished_at: T0, outcome: 'failed', error: { kind: 'retryable', code: 'TIMEOUT' }, accounts_failed: 0, transactions_inserted: 0, transactions_rejected: 0 },
          })
        } else {
          bank.conn = bank.view({
            freshness: 'fresh', accounts: [bank.account('fresh')],
            last_refresh_run: { id: 'R', started_at: T0, finished_at: T0, outcome: 'succeeded', error: null, accounts_failed: 0, transactions_inserted: 3, transactions_rejected: 0 },
          })
        }
        return json(route, bank.conn)
      }
      case `POST /bank-connections/${ID}/reconnect`: {
        const state = 'state-' + bank.calls.length
        bank.pending.add(state)
        return json(route, { connection: bank.conn, authorization_url: `https://bank.example/authorize?state=${state}` })
      }
    }
    return err(route, 404, 'not_found', 'no fake for ' + key)
  })
  // The bank's own page: approving sends the browser back to Ledger's callback with a code. Ledger shows no bank login form.
  await page.route('https://bank.example/**', (route) => {
    const state = new URL(route.request().url()).searchParams.get('state')
    return route.fulfill({
      contentType: 'text/html',
      body: `<!doctype html><title>Fake bank</title><h1>Fake bank</h1><a id="approve" href="http://127.0.0.1:18080/connections/callback?state=${state}&code=fake-code">Approve access</a>`,
    })
  })
}

test('connect, consent redirect, refresh; then a withdrawn consent and an outage look and act differently', async ({ page }) => {
  const bank = new FakeBank()
  await fakeApi(page, bank)

  // Connect: choose the bank in a dialog, by keyboard, and be sent to the bank.
  await page.goto('/connections')
  await expect(page.getByRole('heading', { name: 'No bank connected yet' })).toBeVisible()
  await page.getByRole('button', { name: 'Connect a bank' }).first().click()
  const dialog = page.getByRole('dialog', { name: 'Connect a bank' })
  await expect(dialog.getByText('Ledger never asks for or sees your bank login')).toBeVisible()
  await dialog.getByLabel('Country of the bank').fill('FI')
  await page.keyboard.press('Enter')
  await dialog.getByLabel('Bank', { exact: true }).selectOption('Fake Bank')
  await dialog.getByRole('button', { name: 'Continue to your bank' }).click()

  // At the bank, approve: back on the callback page, which finishes once and cleans the address.
  await expect(page.getByRole('heading', { name: 'Fake bank' })).toBeVisible()
  await page.getByRole('link', { name: 'Approve access' }).click()
  await expect(page.getByText('Fake Bank is connected')).toBeVisible()
  await expect(page).toHaveURL(/\/connections\/callback$/)
  expect(page.url()).not.toContain('fake-code')
  expect(bank.calls.filter((c) => c === 'POST /bank-connections/complete')).toHaveLength(1)

  // The first fetch shows accounts, balances and both times.
  await page.getByRole('button', { name: 'Fetch accounts now' }).click()
  await expect(page.getByText('Fetched 1 account(s).')).toBeVisible()
  await page.getByRole('link', { name: 'Go to bank connections' }).first().click()
  const card = page.getByRole('article', { name: 'Fake Bank' })
  await expect(card.getByText('1,250.40 EUR', { exact: true })).toBeVisible()
  await expect(card.getByText('Up to date')).toBeVisible()
  await expect(card.getByText('Last successful refresh:')).toBeVisible()

  // A bank outage is a temporary problem: retry, never reconnect.
  bank.outage = true
  await card.getByRole('button', { name: 'Refresh now' }).click()
  await expect(card.getByText('Temporary problem').first()).toBeVisible()
  await expect(card.getByRole('button', { name: 'Try again' })).toBeVisible()
  await expect(card.getByRole('button', { name: /Reconnect/ })).toHaveCount(0)
  await expect(card.getByText('1,250.40 EUR', { exact: true })).toBeVisible()

  // The consent is withdrawn at the bank: reconnect, never retry.
  bank.outage = false
  bank.revoked = true
  await card.getByRole('button', { name: 'Try again' }).click()
  await expect(card.getByText('Reconnect needed').first()).toBeVisible()
  await expect(card.getByRole('button', { name: 'Reconnect bank' })).toBeVisible()
  await expect(card.getByRole('button', { name: /Try again|Refresh now/ })).toHaveCount(0)
  await expect(card.getByText('Retrying will not help')).toBeVisible()
  await expect(card.getByText('1,250.40 EUR', { exact: true })).toBeVisible()

  // Reconnect: back through the bank, and the connection is usable again.
  await card.getByRole('button', { name: 'Reconnect bank' }).click()
  await page.getByRole('link', { name: 'Approve access' }).click()
  await expect(page.getByText('Fake Bank is connected')).toBeVisible()
})

test('the connections page fits a 320 px screen without sideways scrolling', async ({ page }) => {
  const bank = new FakeBank()
  bank.conn = bank.view({ freshness: 'fresh', accounts: [bank.account('fresh')], last_refresh_run: { id: 'R', started_at: T0, finished_at: T0, outcome: 'succeeded', error: null, accounts_failed: 0, transactions_inserted: 0, transactions_rejected: 0 } })
  await fakeApi(page, bank)
  await page.setViewportSize({ width: 320, height: 800 })
  await page.goto('/connections')
  await expect(page.getByRole('article', { name: 'Fake Bank' })).toBeVisible()
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(overflow).toBeLessThanOrEqual(0)
})
